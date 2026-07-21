package acctposting

import (
	"context"
	"errors"
	"fmt"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds the DB work done in a single posting pass, so one stuck query can't block the
// loop forever or stall graceful shutdown. It is generous relative to WorkerInterval because a tick
// posts up to BatchSize movement entries plus the outbox/runs/opex phases.
const tickTimeout = 2 * time.Minute

// Backoff bounds for consecutive-failure backoff (base * 2^(n-1), capped at backoffMax); a
// successful tick resets it. Mirrors ordercleanup/opexmaterialize. This is the WORKER-level backoff
// for infrastructure failures (e.g. the DB is down) — distinct from the per-event backoff stored in
// acct_event.next_retry_at, which paces the retry of one specific unpostable event.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	// Drain once at startup so a fresh boot (or a just-enabled worker with a backlog) doesn't wait a
	// full interval before posting.
	w.runOnce(ctx)

	var consecutiveFailures int
	for {
		select {
		case <-ticker.C:
			if w.runOnce(ctx) {
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			delay := backoffDelay(consecutiveFailures)
			slog.Default().WarnContext(ctx, "acctposting: backing off after failed tick",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			timer := time.NewTimer(delay)
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

// backoffDelay returns the extra inter-iteration delay for the given number of consecutive failures:
// base * 2^(n-1), capped at backoffMax.
func backoffDelay(consecutiveFailures int) time.Duration {
	delay := backoffBase
	for i := 1; i < consecutiveFailures; i++ {
		delay *= 2
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}

// runOnce performs one posting pass with a per-tick timeout and panic guard, updating the health
// tracker. It reports whether the tick fully succeeded — skips (uncosted movements, non-EUR orders)
// and waits (settled-pending, awaiting-sale) are NOT failures, so they do not flip health stale.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "acctposting")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	if err := w.RunOnce(ctx); err != nil {
		w.tracker.MarkError(err)
		return false
	}
	w.tracker.MarkSuccess()
	return true
}

// RunOnce runs the four posting phases once, each isolated: a phase-level (infrastructure) error is
// logged and the following phases still run, and the joined error is returned (nil when every phase
// succeeded). Per-fact skips/waits/recorded-failures are handled inside each phase and are not
// errors here. Exported so the integration test can drive a deterministic single pass.
func (w *Worker) RunOnce(ctx context.Context) error {
	if w.startDateErr != nil {
		return w.startDateErr
	}
	if w.deliveredRecognitionFromErr != nil {
		return w.deliveredRecognitionFromErr
	}
	if w.startDate.IsZero() {
		return fmt.Errorf("acctposting: start date not configured")
	}

	var errs []error
	phase := func(name string, fn func(context.Context) error) {
		if err := fn(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			slog.Default().ErrorContext(ctx, "acctposting: phase failed",
				slog.String("phase", name),
				slog.String("err", err.Error()),
			)
		}
	}

	phase("outbox", w.processOutbox)
	phase("movements", w.processMovements)
	phase("runs", w.processRuns)
	phase("opex", w.processOpex)
	phase("shipping", w.processShipping)
	phase("devexpenses", w.processDevExpenses)

	return errors.Join(errs...)
}
