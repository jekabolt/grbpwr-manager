package stripereconcile

import (
	"context"
	"log/slog"
	"time"
)

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			olderThan := time.Now().Add(-w.c.PreOrderThreshold)
			for _, c := range w.cleaners {
				if err := ctx.Err(); err != nil {
					return
				}
				if err := c.CleanupOrphanedPreOrderPaymentIntents(ctx, olderThan); err != nil {
					slog.Default().ErrorContext(ctx, "stripe reconcile: cleanup orphaned pre-order PIs failed",
						slog.String("err", err.Error()),
					)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
