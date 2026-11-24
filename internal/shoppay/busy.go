package shoppay

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RefundWindow is how long after a card order was paid a refund may still go through the Stripe
// that took it. A team shop keeps its Stripe (no disconnect, no other account) while it has a card
// order awaiting its payment or a refund, or paid within this window: otherwise the order's payment
// or refund would reach no processor.
const RefundWindow = 30 * 24 * time.Hour

// Orders answers about a team's card orders (the team's order store; ctx carries the team).
type Orders interface {
	// CountBusyCardOrders counts card orders awaiting a payment or a refund, or paid since paidSince.
	CountBusyCardOrders(ctx context.Context, paidSince time.Time) (int, error)
}

func errPaymentsBusy() error { return status.Error(codes.FailedPrecondition, "shop_payments_busy") }

// paymentsIdle refuses (shop_payments_busy) while team t's card orders still need its Stripe.
func (s *Service) paymentsIdle(ctx context.Context, t tenant.Team) error {
	if s.d.Orders == nil {
		return nil
	}
	n, err := s.d.Orders.CountBusyCardOrders(tenant.With(ctx, t), s.d.Now().Add(-RefundWindow))
	if err != nil {
		return err
	}
	if n > 0 {
		return errPaymentsBusy()
	}
	return nil
}
