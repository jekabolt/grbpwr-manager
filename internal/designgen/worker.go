package designgen

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// workerName is what the health registry, the logs and saferun call this thing. One spelling.
const workerName = "designgen"

// queueTimeout bounds the two queue verbs at the top of a tick (revive + claim). They are short
// transactions on a small table; a tick that cannot do them in half a minute is a tick that should
// back off rather than wait.
const queueTimeout = 30 * time.Second

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
func New(c *Config, repo dependency.Repository, files dependency.FileStore, providers Providers) (*Worker, error) {
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
	return newWorker(c, repo.Design(), repo.Media(), NewBucketSink(files, repo), providers), nil
}

// newWorker is the seam the tests use: the same worker over a fake store, a fake sink and fake
// providers. THIS PACKAGE NEVER OPENS A DATABASE IN A TEST — outside CI the store's TestMain reads
// a production DSN and drops every table.
func newWorker(c *Config, store runStore, media mediaResolver, sink MediaSink, providers Providers) *Worker {
	return &Worker{c: c, store: store, media: media, sink: sink, providers: providers}
}

func (w *Worker) Name() string { return workerName }

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
			if w.runOnce(ctx) {
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

// runOnce is one tick: revive, claim, and take each claimed run through a pass.
//
// THE TWO PHASES HAVE DIFFERENT CLOCKS, AND THEY HAVE TO. The queue verbs are short and get a
// short bound; a pass contains a provider call and gets RunTimeout, which applyDefaults keeps
// strictly under the claim lease — the invariant that stops the queue from reviving a run whose
// worker is still alive and paying for it.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, workerName)

	qctx, qcancel := context.WithTimeout(ctx, queueTimeout)
	// Leases die with the workers that held them — a redeploy in the middle of a generation is the
	// ordinary case, not the exception. Without this sweep such a run stays `running` forever,
	// because the claim predicate only looks at `pending`.
	if _, err := w.store.ReviveExpiredRuns(qctx); err != nil {
		qcancel()
		return w.failed(ctx, "revive expired design runs", err)
	}
	// A FRESH TOKEN PER TICK. It is the identity of this batch of claims, and it is what every
	// closing write is checked against; a token shared between two processes, or reused after a
	// lease was swept, would let one worker close a run another one is running.
	token := uuid.NewString()
	runs, err := w.store.ClaimRuns(qctx, w.c.BatchSize, w.c.ClaimLease, token)
	qcancel()
	if err != nil {
		return w.failed(ctx, "claim design runs", err)
	}
	if len(runs) == 0 {
		w.tracker.MarkSuccess()
		return true
	}

	for _, run := range runs {
		if ctx.Err() != nil {
			// Shutting down. The runs still claimed keep their lease and are revived by whichever
			// instance comes up next; nothing has been paid for them.
			return true
		}
		rctx, rcancel := context.WithTimeout(ctx, w.c.RunTimeout)
		err := w.execute(rctx, run, token)
		rcancel()
		if err != nil {
			return w.failed(ctx, "execute design run", err)
		}
	}
	w.tracker.MarkSuccess()
	return true
}

func (w *Worker) failed(ctx context.Context, action string, err error) bool {
	w.tracker.MarkError(err)
	slog.ErrorContext(ctx, "design generation worker failed",
		slog.String("action", action),
		slog.String("err", err.Error()))
	return false
}
