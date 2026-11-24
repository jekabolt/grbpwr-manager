package order

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// CountBusyCardOrders counts the card orders that still need the Stripe that took them: awaiting
// their payment or a refund (any age), or paid (placed) since paidSince, so a refund may still come.
// A team shop keeps its Stripe while this is not zero (X18-L3).
func (s *Store) CountBusyCardOrders(ctx context.Context, paidSince time.Time) (int, error) {
	const q = `
		SELECT COUNT(*)
		FROM customer_order co
		JOIN payment p ON p.order_id = co.id
		JOIN payment_method pm ON pm.id = p.payment_method_id
		JOIN order_status os ON os.id = co.order_status_id
		WHERE pm.name IN (:card, :cardTest)
			AND (os.name IN (:awaiting, :pendingReturn, :refunding)
				OR (os.name IN (:confirmed, :shipped, :delivered, :partlyRefunded) AND co.placed >= :paidSince))`
	n, err := storeutil.QueryCountNamed(ctx, s.DB, q, map[string]any{
		"card":           string(entity.CARD),
		"cardTest":       string(entity.CARD_TEST),
		"awaiting":       string(entity.AwaitingPayment),
		"pendingReturn":  string(entity.PendingReturn),
		"refunding":      string(entity.RefundInProgress),
		"confirmed":      string(entity.Confirmed),
		"shipped":        string(entity.Shipped),
		"delivered":      string(entity.Delivered),
		"partlyRefunded": string(entity.PartiallyRefunded),
		"paidSince":      paidSince.UTC(),
	})
	if err != nil {
		return 0, fmt.Errorf("count busy card orders: %w", err)
	}
	return n, nil
}
