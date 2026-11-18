package designgen

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Teams mode (02-DESIGN §10): one tick serves many teams, and no team may hold the others up.
//
//   - CLEANUP IS NOT CLAIMING. Reviving dead leases, closing overdue runs and sweeping the AI ledger
//     run for every team the cleanup loop lists — suspended and `ai`-off ones included — because
//     they settle work the team already started. Claiming (and so spending) runs only for the teams
//     the work loop lists: active, `ai` on.
//   - NO PROVIDER CALL IN THE WALK. The walk only cleans up and claims (short queue verbs, each team
//     under teamQueueTimeout). A claimed batch executes on its own goroutine in one of the worker's
//     slots (SetTeamLoops' parallel, default 2); a team with a batch in flight claims nothing more,
//     and with every slot busy nobody claims — the lease invariant of applyDefaults still holds
//     because a batch starts the moment it is claimed.
//   - A FAILING TEAM BACKS OFF ALONE (teamGate), on the worker's own backoff curve; the others go on.

// teamQueueTimeout bounds one team's cleanup or claim inside the walk: a locked or broken team
// database costs the walk this much, once, and then that team backs off.
const teamQueueTimeout = queueTimeout

// teamGate is the per-team state of the teams-mode tick. Safe for concurrent use: the loops serve
// several teams at once and batches finish on their own goroutines.
type teamGate struct {
	mu sync.Mutex
	st map[int64]*teamState
}

type teamState struct {
	failures  int
	notBefore time.Time
	busy      bool
	sweptAt   time.Time
}

func (g *teamGate) state(id int64) *teamState {
	if g.st == nil {
		g.st = map[int64]*teamState{}
	}
	s := g.st[id]
	if s == nil {
		s = &teamState{}
		g.st[id] = s
	}
	return s
}

// due — the team is not backing off at now.
func (g *teamGate) due(id int64, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !now.Before(g.state(id).notBefore)
}

// take marks the team busy if it is due and idle; false = leave it this tick.
func (g *teamGate) take(id int64, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.state(id)
	if s.busy || now.Before(s.notBefore) {
		return false
	}
	s.busy = true
	return true
}

func (g *teamGate) release(id int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(id).busy = false
}

// record books one outcome: success clears the backoff, failure pushes notBefore along workerBackoff.
func (g *teamGate) record(id int64, ok bool, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.state(id)
	if ok {
		s.failures, s.notBefore = 0, time.Time{}
		return
	}
	s.failures++
	s.notBefore = now.Add(workerBackoff(s.failures))
}

// sweepDue reports whether the team's ledger sweep is due and, if so, stamps it.
func (g *teamGate) sweepDue(id int64, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.state(id)
	if !s.sweptAt.IsZero() && now.Sub(s.sweptAt) < ledgerSweepEvery {
		return false
	}
	s.sweptAt = now
	return true
}

func teamIDOf(ctx context.Context) (int64, string) {
	t, _ := tenant.From(ctx)
	return t.ID, t.Slug
}

// teamsTick is one teams-mode tick: cleanup over the cleanup loop, then claims over the work loop.
// It returns false — the worker backs off as a whole — only when neither team list could be read.
func (w *Worker) teamsTick(ctx context.Context) bool {
	now := w.clock()
	var listedErr, workErr error
	if w.cleanupTeams != nil {
		var served atomic.Int32
		err := w.cleanupTeams(ctx, func(tctx context.Context) error {
			served.Add(1)
			w.cleanupTeam(tctx, now)
			return nil // logged by cleanupTeam
		})
		if err != nil && served.Load() == 0 {
			listedErr = err
		}
	}
	var served atomic.Int32
	err := w.teams(ctx, func(tctx context.Context) error {
		served.Add(1)
		w.claimForTeam(tctx, now)
		return nil
	})
	if err != nil && served.Load() == 0 {
		workErr = err
	}
	if ctx.Err() != nil {
		return true
	}
	if workErr != nil && (w.cleanupTeams == nil || listedErr != nil) {
		return w.failed(ctx, "list teams", workErr)
	}
	if listedErr != nil {
		slog.ErrorContext(ctx, "design generation worker: the cleanup team list could not be read",
			slog.String("err", listedErr.Error()))
	}
	w.tracker.MarkSuccess()
	return true
}

// cleanupTeam revives, closes overdue runs and sweeps the ledger for the team ctx carries, under
// teamQueueTimeout. Never claims, never calls a provider.
func (w *Worker) cleanupTeam(ctx context.Context, now time.Time) {
	defer saferun.Recover(ctx, workerName)
	id, slug := teamIDOf(ctx)
	if !w.gate.due(id, now) {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, teamQueueTimeout)
	defer cancel()
	if w.ledger != nil && w.gate.sweepDue(id, now) {
		sweepLedger(qctx, w.ledger, w.workerLedgerSweepAge(), workerName)
	}
	if qerr := w.reviveAndClose(qctx); qerr != nil {
		w.gate.record(id, false, now)
		slog.ErrorContext(ctx, "design generation worker failed",
			slog.String("action", qerr.action), slog.Int64("tid", id), slog.String("slug", slug),
			slog.String("err", qerr.err.Error()))
	}
}

// claimForTeam claims a batch for the team ctx carries when the team is due and idle and a slot is
// free, and hands the batch to its own goroutine. The walk never waits for a provider.
func (w *Worker) claimForTeam(ctx context.Context, now time.Time) {
	defer saferun.Recover(ctx, workerName)
	id, slug := teamIDOf(ctx)
	if !w.gate.take(id, now) {
		return
	}
	select {
	case w.slots <- struct{}{}:
	default:
		w.gate.release(id) // every slot busy: the next tick
		return
	}
	done := func(ok bool) {
		<-w.slots
		w.gate.release(id)
		w.gate.record(id, ok, w.clock())
	}
	// A panic between here and the batch goroutine (recovered above) must not keep the slot and
	// the team's busy mark forever: the guard frees both unless the slot was released normally or
	// handed to the batch goroutine.
	owned := true
	defer func() {
		if owned {
			done(false)
		}
	}()
	runs, token, err := w.claimBatch(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "design generation worker failed",
			slog.String("action", "claim design runs"), slog.Int64("tid", id), slog.String("slug", slug),
			slog.String("err", err.Error()))
		owned = false
		done(false)
		return
	}
	if len(runs) == 0 {
		owned = false
		done(true)
		return
	}
	owned = false // the batch goroutine owns the slot from here
	w.wg.Add(1)
	go func(runs []entity.DesignRun) {
		defer w.wg.Done()
		ok := false
		defer func() { done(ok) }()
		defer saferun.Recover(ctx, workerName)
		run := w.executeBatch
		if w.runBatch != nil {
			run = w.runBatch
		}
		if err := run(ctx, runs, token); err != nil {
			w.tracker.MarkError(err)
			slog.ErrorContext(ctx, "design generation worker failed",
				slog.String("action", "execute design run"), slog.Int64("tid", id), slog.String("slug", slug),
				slog.String("err", err.Error()))
			return
		}
		ok = true
	}(runs)
}
