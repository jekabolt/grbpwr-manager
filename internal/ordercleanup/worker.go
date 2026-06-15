package ordercleanup

import (
	"context"
	"fmt"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds the DB work done in a single cleanup tick, so one stuck
// query can't block the loop forever or stall graceful shutdown.
const tickTimeout = 30 * time.Second

// Backoff bounds for consecutive-failure backoff. On a failed tick an extra
// delay (base * 2^(n-1), capped at backoffMax) is waited before the next
// iteration so a persistently failing dependency (e.g. the DB) isn't hammered
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
			slog.Default().WarnContext(ctx, "ordercleanup: backing off after failed tick",
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

// runOnce performs a single cleanup tick and reports whether it fully succeeded.
// The deferred saferun.Recover keeps a panic in this iteration from killing the
// worker loop (and the whole process).
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "ordercleanup")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	ok := true
	if err := w.cancelStuckPlacedOrders(ctx); err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "can't cancel stuck placed orders",
			slog.String("err", err.Error()),
		)
	}
	if err := w.cancelExpiredAwaitingPaymentOrders(ctx); err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "can't cancel expired awaiting payment orders",
			slog.String("err", err.Error()),
		)
	}

	// Record success only when the whole tick completed without error, so
	// staleness reflects real failures.
	if ok {
		w.tracker.MarkSuccess()
	}
	return ok
}

func (w *Worker) cancelStuckPlacedOrders(ctx context.Context) error {
	olderThan := time.Now().UTC().Add(-w.c.PlacedThreshold)
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
	now := time.Now().UTC()
	orders, err := w.repo.Order().GetExpiredAwaitingPaymentOrders(ctx, now)
	if err != nil {
		return fmt.Errorf("can't get expired awaiting payment orders: %w", err)
	}

	for _, order := range orders {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Prefer the provider-checked expiry so a payment that actually succeeded
		// (but whose in-process monitor was lost) is confirmed, not cancelled.
		// Fall back to the store-level path only when no expirer is wired.
		var err error
		if w.expirer != nil {
			err = w.expirer.ExpireOrderPayment(ctx, order.UUID)
		} else {
			_, err = w.repo.Order().ExpireOrderPayment(ctx, order.UUID)
		}
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
