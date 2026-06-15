package stripereconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds the DB/Stripe work done in a single reconciliation tick, so
// one stuck query or hung connection can't block the loop forever or stall
// graceful shutdown.
const tickTimeout = 30 * time.Second

// Backoff bounds for consecutive-failure backoff. On a failed tick an extra
// delay (base * 2^(n-1), capped at backoffMax) is waited before the next
// iteration so a persistently failing dependency (DB / Stripe) isn't hammered
// every WorkerInterval. A successful tick resets the backoff. See ga4sync for
// the established pattern in this codebase.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	// consecutiveFailures drives the extra backoff delay applied after a failed
	// tick. Reset to 0 on the first successful tick.
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
			slog.Default().WarnContext(ctx, "stripe reconcile: backing off after failed tick",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			// Wait the extra backoff on top of the ticker interval, but stay
			// responsive to shutdown — never time.Sleep blindly.
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

// backoffDelay returns the extra inter-iteration delay for the given number of
// consecutive failures: base * 2^(n-1), capped at backoffMax.
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

// runOnce performs a single reconciliation tick and reports whether it fully
// succeeded. The deferred saferun.Recover keeps a panic in this iteration from
// killing the worker loop.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "stripereconcile")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	olderThan := time.Now().UTC().Add(-w.c.PreOrderThreshold)
	ok := true
	for _, c := range w.cleaners {
		if err := ctx.Err(); err != nil {
			// Aborted mid-tick (shutdown / timeout): don't record success, and
			// report ok so this abort isn't counted as a dependency failure for
			// backoff (the loop catches ctx.Done() / re-ticks on its own).
			return true
		}
		if err := c.CleanupOrphanedPreOrderPaymentIntents(ctx, olderThan); err != nil {
			ok = false
			w.tracker.MarkError(err)
			slog.Default().ErrorContext(ctx, "stripe reconcile: cleanup orphaned pre-order PIs failed",
				slog.String("err", err.Error()),
			)
		}
	}

	// Record success only when every cleaner completed without error.
	if ok {
		w.tracker.MarkSuccess()
	}
	return ok
}
