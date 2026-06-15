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

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.runOnce(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// runOnce performs a single reconciliation tick. The deferred saferun.Recover
// keeps a panic in this iteration from killing the worker loop.
func (w *Worker) runOnce(ctx context.Context) {
	defer saferun.Recover(ctx, "stripereconcile")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	olderThan := time.Now().UTC().Add(-w.c.PreOrderThreshold)
	ok := true
	for _, c := range w.cleaners {
		if err := ctx.Err(); err != nil {
			// Aborted mid-tick (shutdown / timeout): don't record success.
			return
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
}
