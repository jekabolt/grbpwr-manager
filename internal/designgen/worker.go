package designgen

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// workerName is what the health registry, the logs and saferun call this thing. One spelling.
const workerName = "designgen"

// queueTimeout bounds the two queue verbs at the top of a tick (revive + claim). They are short
// transactions on a small table; a tick that cannot do them in half a minute is a tick that should
// back off rather than wait.
const queueTimeout = 30 * time.Second

// overdueGrace — the sweep closes a capped run this long after its cap: a live worker fails it at
// the cap itself; the sweep is for a worker that died.
const overdueGrace = 30 * time.Second

// Backoff between failing ticks, copied from campaigndispatch: a broken database or a broken
// provider must not become a hot loop.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

// Worker drains the DESIGN band's generation queue.
type Worker struct {
	c         *Config
	store     runStore
	media     mediaResolver
	sink      MediaSink
	providers Providers
	// objects reads the BYTES of an input picture, which exactly one kind needs: the playground
	// builds a marked copy and a close crop of every marked area from them (freeform_derive.go).
	//
	// ⚠ IT IS SET IN New AND NOWHERE ELSE, so newWorker — the seam every test in this package
	// builds on — keeps its signature and its callers. A worker without it refuses a freeform run
	// that marks an area, loudly and before any money moves, rather than sending the run with the
	// person's markup silently missing.
	objects objectFetcher

	// now is the pickup guard's clock (submitSettleGrace); nil = time.Now. Tests set it.
	now func() time.Time

	// ledger books every physical provider call (B-07, ledger.go); nil = no ledger, and then not
	// one line of a pass differs from before it existed. Set by New (WithLedger) — newWorker, the
	// tests' seam, keeps its signature, and a test that wants the ledger sets the field. The tick
	// also sweeps its stale `dispatching` rows, at most once per ledgerSweepEvery (ledgerSweptAt).
	ledger        callLedger
	ledgerSweptAt time.Time

	// teams / cleanupTeams walk the teams a tick serves (SetTeamLoops): cleanupTeams every listed
	// team (suspended and `ai`-off included), teams only those that may claim and spend. nil teams =
	// one runOnce on the ctx as given — the tests' seam and the pre-teams behaviour (teams mode off).
	// slots bounds the claimed batches executing at once; gate keeps per-team backoff, the busy
	// flag and the ledger-sweep clock (teams.go).
	teams        TeamLoop
	cleanupTeams TeamLoop
	slots        chan struct{}
	gate         teamGate
	// runBatch replaces executeBatch in the teams-mode tick; nil = executeBatch. Tests only.
	runBatch func(ctx context.Context, runs []entity.DesignRun, token string) error

	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// New builds the worker against the real repository and bucket.
//
// The caller is expected to have checked Config.Enabled first — see app.go, where a disabled
// feature means the worker is never constructed at all. Start refuses a second time anyway, on the
// principle that the safe failure mode for something that spends money is "does not run".
//
// Options: WithLedger books every physical provider call into the AI ledger (B-07).
func New(c *Config, repo dependency.Repository, files dependency.FileStore, providers Providers, opts ...Option) (*Worker, error) {
	if c == nil {
		d := DefaultConfig()
		c = &d
	}
	applyDefaults(c)
	if repo == nil {
		return nil, fmt.Errorf("designgen: a worker without a repository has nothing to drain")
	}
	if files == nil {
		return nil, fmt.Errorf("designgen: a worker without a bucket has nowhere to put what it buys")
	}
	w := newWorker(c, repo.Design(), repo.Media(), NewBucketSink(files, repo), providers)
	// ТОТ ЖЕ FileStore, ЧТО У ПРИЁМНИКА, И ЭТО НЕ ВТОРАЯ ЗАВИСИМОСТЬ. Приёмник им ПИШЕТ готовые
	// картинки, производные плейграунда — ЧИТАЮТ исходные; один бакет, два глагола.
	w.objects = files
	w.ledger = applyOptions(opts).ledger
	return w, nil
}

// newWorker is the seam the tests use: the same worker over a fake store, a fake sink and fake
// providers. THIS PACKAGE NEVER OPENS A DATABASE IN A TEST — outside CI the store's TestMain reads
// a production DSN and drops every table.
func newWorker(c *Config, store runStore, media mediaResolver, sink MediaSink, providers Providers) *Worker {
	return &Worker{c: c, store: store, media: media, sink: sink, providers: providers}
}

func (w *Worker) Name() string { return workerName }

// TeamLoop walks the teams one tick serves: fn runs once per team with ctx bound to that team
// (tenant.With). The app builds it on tenant.Each and decides who is served — the worker skips
// suspended teams and teams with `ai` off, the sweeper serves every listed team. A non-nil error
// with no team served means the list itself failed. Teams mode off: the directory lists team #1
// only, so a tick is exactly one runOnce on team #1, as before teams.
type TeamLoop func(ctx context.Context, fn func(ctx context.Context) error) error

// SetTeamLoops makes every tick run per team (teams wave, 02-DESIGN §10): cleanup over every team
// cleanup serves, claims over the teams work serves, and at most parallel claimed batches
// executing at once, outside the walk. Call before Start.
func (w *Worker) SetTeamLoops(cleanup, work TeamLoop, parallel int) {
	if parallel < 1 {
		parallel = 1
	}
	w.cleanupTeams, w.teams, w.slots = cleanup, work, make(chan struct{}, parallel)
}

func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// Start launches the loop. A disabled worker starts nothing and says so once.
func (w *Worker) Start(ctx context.Context) error {
	if !w.c.Enabled {
		slog.Default().InfoContext(ctx, "design generation worker is disabled; the queue will not be drained",
			slog.String("flag", EnvEnabled))
		return nil
	}
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("design generation worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() { w.run(w.ctx) })
	slog.Default().InfoContext(ctx, "design generation worker started",
		slog.Duration("interval", w.c.WorkerInterval),
		slog.Int("batch", w.c.BatchSize),
		slog.Duration("lease", w.c.ClaimLease),
		slog.Duration("run_timeout", w.c.RunTimeout))
	return nil
}

// Stop cancels the loop and waits for the tick in flight.
//
// The wait is what makes the settle path safe: App.Stop stops workers before closing the database,
// so a pass that is writing the result of a call it already paid for finishes those writes against
// a live pool.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return nil
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()
	var failures int
	for {
		select {
		case <-ticker.C:
			if w.tick(ctx) {
				failures = 0
				continue
			}
			failures++
			timer := time.NewTimer(workerBackoff(failures))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func workerBackoff(failures int) time.Duration {
	delay := backoffBase
	for i := 1; i < failures; i++ {
		if delay >= backoffMax/2 {
			return backoffMax
		}
		delay *= 2
	}
	return delay
}

// tick is one tick: runOnce without team loops (teams mode off, exactly as before teams), otherwise
// teamsTick (teams.go).
func (w *Worker) tick(ctx context.Context) bool {
	if w.teams == nil {
		return w.runOnce(ctx)
	}
	return w.teamsTick(ctx)
}

// runOnce is one tick: revive, claim, and take each claimed run through a pass.
//
// THE TWO PHASES HAVE DIFFERENT CLOCKS, AND THEY HAVE TO. The queue verbs are short and get a
// short bound; a pass contains a provider call and gets RunTimeout, and applyDefaults keeps the
// WHOLE BATCH — BatchSize × RunTimeout — strictly under the claim lease. Не один прогон, а пачка:
// runOnce строго последовательна, поэтому последний прогон пачки ждёт всех предыдущих и всё это
// время жжёт СВОЮ лизу. Инвариант на один прогон был бы верен арифметически и всё равно позволил
// очереди воскресить строку, чей воркер жив и прямо сейчас за неё платит.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, workerName)

	// The AI ledger's stale `dispatching` rows (B-07). FIRST, so a tick that backs off below still
	// swept; its failure is logged inside and never fails the tick — bookkeeping is not a reason to
	// stop generating. At most once per ledgerSweepEvery; a no-op without a ledger.
	w.maybeSweepLedger(ctx)

	qctx, qcancel := context.WithTimeout(ctx, queueTimeout)
	err := w.reviveAndClose(qctx)
	qcancel()
	if err != nil {
		return w.failed(ctx, err.action, err.err)
	}
	runs, token, cerr := w.claimBatch(ctx)
	if cerr != nil {
		return w.failed(ctx, "claim design runs", cerr)
	}
	if len(runs) == 0 {
		w.tracker.MarkSuccess()
		return true
	}
	if err := w.executeBatch(ctx, runs, token); err != nil {
		return w.failed(ctx, "execute design run", err)
	}
	w.tracker.MarkSuccess()
	return true
}

// queueError names the queue verb that failed, for failed's log line.
type queueError struct {
	action string
	err    error
}

// reviveAndClose is the cleanup half of a tick: no claim, no money.
func (w *Worker) reviveAndClose(qctx context.Context) *queueError {
	// Leases die with the workers that held them — a redeploy in the middle of a generation is the
	// ordinary case, not the exception. Without this sweep such a run stays `running` forever,
	// because the claim predicate only looks at `pending`.
	if _, err := w.store.ReviveExpiredRuns(qctx); err != nil {
		return &queueError{"revive expired design runs", err}
	}
	// THE WALL-CLOCK CAP (owner 05.10): a capped image run past its cap is closed `timed_out` here
	// instead of spinning for its lease. Grace of one tick-ish minute over the cap, because a live
	// worker fails such a run itself at the cap (runOnce's deadline + failRunAt).
	if _, err := w.store.CloseOverdueRuns(qctx, entity.DesignOverdueSweep{
		Kinds: entity.DesignCappedRunKinds(), Cap: w.imageRunCap() + overdueGrace,
	}); err != nil {
		return &queueError{"close overdue design runs", err}
	}
	return nil
}

// claimBatch claims up to BatchSize pending runs under a fresh token and shortens the lease of the
// capped ones. No provider call.
func (w *Worker) claimBatch(ctx context.Context) ([]entity.DesignRun, string, error) {
	qctx, qcancel := context.WithTimeout(ctx, queueTimeout)
	// A FRESH TOKEN PER TICK. It is the identity of this batch of claims, and it is what every
	// closing write is checked against; a token shared between two processes, or reused after a
	// lease was swept, would let one worker close a run another one is running.
	token := uuid.NewString()
	runs, err := w.store.ClaimRuns(qctx, w.c.BatchSize, w.c.ClaimLease, token)
	qcancel()
	if err != nil || len(runs) == 0 {
		return nil, token, err
	}
	// A capped run's lease ends shortly after its cap (claimTailAfterCap), so a worker that died
	// holding it is swept in minutes; a failed shortening only keeps the full lease.
	for _, run := range runs {
		if d, ok := w.runDeadline(run); ok {
			cctx, ccancel := context.WithTimeout(ctx, queueTimeout)
			if err := w.store.CapClaim(cctx, run.Id, token, w.capClaimWithin(d)); err != nil {
				slog.WarnContext(ctx, "design generation: the claim of a capped run kept its full lease",
					slog.Int("run_id", run.Id), slog.String("err", err.Error()))
			}
			ccancel()
		}
	}
	return runs, token, nil
}

// executeBatch takes each claimed run through a pass, one after another (the lease invariant of
// applyDefaults is per batch). The first failing pass ends the batch.
func (w *Worker) executeBatch(ctx context.Context, runs []entity.DesignRun, token string) error {
	for _, run := range runs {
		if ctx.Err() != nil {
			// Shutting down. The runs still claimed keep their lease and are revived by whichever
			// instance comes up next; nothing has been paid for them.
			return nil
		}
		rctx, rcancel := context.WithTimeout(ctx, w.c.RunTimeout)
		// A capped run's provider phase ends at its wall-clock cap; the settle that follows runs on
		// its own fresh budget, and a failure past the cap closes the run timed_out (failRunAt).
		if d, ok := w.runDeadline(run); ok {
			var dcancel context.CancelFunc
			rctx, dcancel = context.WithDeadline(rctx, d)
			inner := rcancel
			rcancel = func() { dcancel(); inner() }
		}
		err := w.execute(rctx, run, token)
		rcancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) failed(ctx context.Context, action string, err error) bool {
	w.tracker.MarkError(err)
	slog.ErrorContext(ctx, "design generation worker failed",
		slog.String("action", action),
		slog.String("err", err.Error()))
	return false
}
