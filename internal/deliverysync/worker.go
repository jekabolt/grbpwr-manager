package deliverysync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds the work done in a single tick (DB + AfterShip calls), so one stuck
// dependency can't block the loop forever or stall graceful shutdown.
const tickTimeout = 2 * time.Minute

// Backoff bounds for consecutive-failure backoff, mirroring ordercleanup/ga4sync.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

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
			slog.Default().WarnContext(ctx, "deliverysync: backing off after failed tick",
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

// backoffDelay returns base * 2^(n-1), capped at backoffMax.
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

// runOnce performs a single delivery-sync tick and reports whether it fully succeeded. Per-order
// failures are logged and skipped (they don't fail the tick); only a failure to enumerate the work
// marks the tick failed so staleness reflects a real dependency outage.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "deliverysync")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	if err := w.syncDeliveries(ctx); err != nil {
		w.ht.MarkError(err)
		slog.Default().ErrorContext(ctx, "deliverysync: tick failed", slog.String("err", err.Error()))
		return false
	}
	w.ht.MarkSuccess()
	return true
}

func (w *Worker) syncDeliveries(ctx context.Context) error {
	orders, err := w.repo.Order().GetShippedOrdersForDeliverySync(ctx)
	if err != nil {
		return fmt.Errorf("can't get shipped orders for delivery sync: %w", err)
	}
	now := time.Now().UTC()
	for _, o := range orders {
		if err := ctx.Err(); err != nil {
			return err
		}
		w.processOrder(ctx, o, now)
	}
	return nil
}

// processOrder decides one shipped order's fate: prefer a real AfterShip Delivered signal
// (transition + email), otherwise apply the per-carrier timer safety net (silent transition, no
// email). Per-order errors are logged and swallowed so one bad order doesn't fail the whole tick.
func (w *Worker) processOrder(ctx context.Context, o entity.ShipmentToAutoDeliver, now time.Time) {
	carrier, carrierOK := cache.GetShipmentCarrierById(o.CarrierId)
	trackingCode := strings.TrimSpace(o.TrackingCode.String)

	// 1) Real signal: poll AfterShip for trackable carriers that have a tracking code.
	if carrierOK && carrier.Trackable() && o.TrackingCode.Valid && trackingCode != "" {
		st, err := w.tracker.GetTrackingStatus(ctx, carrier.Slug(), trackingCode)
		switch {
		case err != nil:
			slog.Default().WarnContext(ctx, "deliverysync: can't get tracking status",
				slog.String("order_uuid", o.OrderUUID), slog.String("err", err.Error()))
			// fall through to the timer safety net
		case !st.Found:
			// Not registered yet — register so AfterShip starts monitoring and emits webhooks;
			// the status is checked on the next tick.
			if err := w.tracker.RegisterTracking(ctx, carrier.Slug(), trackingCode); err != nil {
				slog.Default().WarnContext(ctx, "deliverysync: can't register tracking",
					slog.String("order_uuid", o.OrderUUID), slog.String("err", err.Error()))
			}
			return
		case st.Delivered:
			w.deliver(ctx, o.OrderUUID, "aftership", "auto-delivered: AfterShip reported delivered (reconcile)", true)
			return
		}
		// tracked but not delivered yet — fall through to the timer safety net
	}

	// 2) Timer safety net: no real Delivered signal → silently deliver after the window elapses.
	window := w.c.FallbackDefault
	if carrierOK {
		window = carrier.AutoDeliverAfter(w.c.FallbackDefault)
	}
	if now.Sub(o.ShippingDate) >= window {
		note := fmt.Sprintf("auto-delivered: %s elapsed since shipment, no carrier confirmation", window)
		w.deliver(ctx, o.OrderUUID, "system-timeout", note, false)
	}
}

// deliver performs the source-aware transition and, only when this call actually delivered the
// order and sendEmail is set, sends the delivered email (best-effort). The transitioned guard makes
// the whole thing idempotent — a webhook that already delivered the order, or a repeated tick,
// sends no second email.
func (w *Worker) deliver(ctx context.Context, orderUUID, changedBy, notes string, sendEmail bool) {
	transitioned, err := w.repo.Order().DeliverOrderWithSource(ctx, orderUUID, changedBy, notes)
	if err != nil {
		slog.Default().ErrorContext(ctx, "deliverysync: can't mark delivered",
			slog.String("order_uuid", orderUUID),
			slog.String("source", changedBy),
			slog.String("err", err.Error()),
		)
		return
	}
	if !transitioned {
		return
	}
	slog.Default().InfoContext(ctx, "order auto-delivered",
		slog.String("order_uuid", orderUUID),
		slog.String("source", changedBy),
	)
	if !sendEmail {
		return
	}
	if err := mail.SendOrderDeliveredForUUID(ctx, w.repo, w.mailer, orderUUID); err != nil {
		slog.Default().ErrorContext(ctx, "deliverysync: can't send delivered email",
			slog.String("order_uuid", orderUUID), slog.String("err", err.Error()))
	}
}
