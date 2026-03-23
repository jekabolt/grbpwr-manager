package frontend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// getOrderExpirationDuration returns configurable expiration from settings, or handler default when not set.
func (s *Server) getOrderExpirationDuration(handler dependency.Invoicer) time.Duration {
	if sec := cache.GetOrderExpirationSeconds(); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return handler.ExpirationDuration()
}

func (s *Server) getPaymentHandler(ctx context.Context, pm entity.PaymentMethodName) (dependency.Invoicer, error) {
	switch pm {
	case entity.CARD:
		return s.stripePayment, nil
	case entity.CARD_TEST:
		return s.stripePaymentTest, nil
	default:
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}
}

// retryInsertFiatInvoiceAfterItemsUpdated is called when InsertFiatInvoice returns ErrOrderItemsUpdated.
// Order items were updated in DB (stock/price changed). We update the PaymentIntent amount to match
// the new order total, then retry InsertFiatInvoice (items now match, so it should succeed).
func (s *Server) retryInsertFiatInvoiceAfterItemsUpdated(ctx context.Context, orderUUID string, paymentIntentId string, pm entity.PaymentMethod, expirationDuration time.Duration, handler dependency.Invoicer) (*entity.OrderFull, error) {
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("get updated order: %w", err)
	}

	orderTotal := orderFull.Order.TotalPriceDecimal()
	if err = handler.UpdatePaymentIntentAmount(ctx, paymentIntentId, orderTotal, orderFull.Order.Currency); err != nil {
		return nil, fmt.Errorf("update payment intent amount: %w", err)
	}

	orderFull, err = s.repo.Order().InsertFiatInvoice(ctx, orderUUID, paymentIntentId, pm, time.Now().UTC().Add(expirationDuration))
	if err != nil {
		return nil, fmt.Errorf("retry insert fiat invoice: %w", err)
	}
	return orderFull, nil
}

// ensurePaymentIntentAmountMatchesOrder verifies the PaymentIntent amount matches the order total.
// If not (e.g. order was updated on ErrOrderItemsUpdated), updates the PaymentIntent before the client pays.
func (s *Server) ensurePaymentIntentAmountMatchesOrder(ctx context.Context, handler dependency.Invoicer, paymentIntentId string, orderFull *entity.OrderFull) error {
	stripePi, err := handler.GetPaymentIntentByID(ctx, paymentIntentId)
	if err != nil {
		return fmt.Errorf("get payment intent: %w", err)
	}
	piAmount := stripe.AmountFromSmallestUnit(stripePi.Amount, string(stripePi.Currency))
	orderTotal := orderFull.Order.TotalPriceDecimal()
	if piAmount.Equal(orderTotal) {
		return nil
	}
	slog.Default().InfoContext(ctx, "PaymentIntent amount mismatch on retry, updating",
		slog.String("payment_intent_id", paymentIntentId),
		slog.String("pi_amount", piAmount.String()),
		slog.String("order_total", orderTotal.String()),
	)
	return handler.UpdatePaymentIntentAmount(ctx, paymentIntentId, orderTotal, orderFull.Order.Currency)
}

// isOrderEligibleForReturn checks if an order is eligible for return based on delivery date and order age
// Returns (eligible bool, reason string)
func isOrderEligibleForReturn(orderFull *entity.OrderFull, statusName entity.OrderStatusName) (bool, string) {
	const (
		maxDaysSinceDelivery = 14
		maxDaysSincePlaced   = 90
	)

	now := time.Now().UTC()

	// // Check if order was placed more than 60 days ago
	// daysSincePlaced := now.Sub(orderFull.Order.Placed).Hours() / 24
	// if daysSincePlaced > maxDaysSincePlaced {
	// 	return false, "order was placed more than 60 days ago and is no longer eligible for return"
	// }

	// If order is delivered, check if delivered more than 14 days ago
	if statusName == entity.Delivered {
		// Find when order was delivered from status history
		var deliveredAt time.Time
		for _, history := range orderFull.StatusHistory {
			if history.StatusName == entity.Delivered {
				deliveredAt = history.ChangedAt
				break
			}
		}

		if !deliveredAt.IsZero() {
			daysSinceDelivery := now.Sub(deliveredAt).Hours() / 24
			if daysSinceDelivery > maxDaysSinceDelivery {
				return false, "order was delivered more than 14 days ago and is no longer eligible for return"
			}
		}
	}

	return true, ""
}

// cartFingerprintForPreOrder returns a deterministic hash of cart contents and client identity for session matching.
// Same cart + same client = same fingerprint. Includes clientSession so different clients with identical carts get different sessions.
func cartFingerprintForPreOrder(amount decimal.Decimal, currency, country, promoCode string, shipmentCarrierId int32, items []entity.OrderItemInsert, clientSession string) string {
	sorted := make([]entity.OrderItemInsert, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ProductId != sorted[j].ProductId {
			return sorted[i].ProductId < sorted[j].ProductId
		}
		return sorted[i].SizeId < sorted[j].SizeId
	})
	data := fmt.Sprintf("%s|%s|%s|%s|%d|%s", amount.String(), currency, country, promoCode, shipmentCarrierId, clientSession)
	for _, i := range sorted {
		data += fmt.Sprintf("|%d:%d:%s", i.ProductId, i.SizeId, i.Quantity.String())
	}
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}
