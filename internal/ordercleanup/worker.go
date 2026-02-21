package ordercleanup

import (
	"context"
	"fmt"
	"time"

	"log/slog"
)

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := w.cancelStuckPlacedOrders(ctx); err != nil {
				slog.Default().ErrorContext(ctx, "can't cancel stuck placed orders",
					slog.String("err", err.Error()),
				)
			}
			if err := w.cancelExpiredAwaitingPaymentOrders(ctx); err != nil {
				slog.Default().ErrorContext(ctx, "can't cancel expired awaiting payment orders",
					slog.String("err", err.Error()),
				)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) cancelStuckPlacedOrders(ctx context.Context) error {
	olderThan := time.Now().Add(-w.c.PlacedThreshold)
	orders, err := w.repo.Order().GetStuckPlacedOrders(ctx, olderThan)
	if err != nil {
		return fmt.Errorf("can't get stuck placed orders: %w", err)
	}

	for _, order := range orders {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := w.repo.Order().CancelOrder(ctx, order.UUID); err != nil {
			slog.Default().ErrorContext(ctx, "can't cancel stuck placed order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", order.UUID),
				slog.Int("order_id", order.Id),
			)
			continue
		}
		if w.reservationMgr != nil {
			w.reservationMgr.Release(ctx, order.UUID)
		}
		slog.Default().InfoContext(ctx, "cancelled stuck placed order",
			slog.String("order_uuid", order.UUID),
			slog.Int("order_id", order.Id),
		)
	}

	return nil
}

func (w *Worker) cancelExpiredAwaitingPaymentOrders(ctx context.Context) error {
	now := time.Now()
	orders, err := w.repo.Order().GetExpiredAwaitingPaymentOrders(ctx, now)
	if err != nil {
		return fmt.Errorf("can't get expired awaiting payment orders: %w", err)
	}

	for _, order := range orders {
		if err := ctx.Err(); err != nil {
			return err
		}

		_, err := w.repo.Order().ExpireOrderPayment(ctx, order.UUID)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't expire awaiting payment order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", order.UUID),
				slog.Int("order_id", order.Id),
			)
			continue
		}
		if w.reservationMgr != nil {
			w.reservationMgr.Release(ctx, order.UUID)
		}
		slog.Default().InfoContext(ctx, "expired awaiting payment order (safety net)",
			slog.String("order_uuid", order.UUID),
			slog.Int("order_id", order.Id),
		)
	}

	return nil
}
