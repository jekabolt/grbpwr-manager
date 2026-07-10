package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func (s *Store) getShippingCostMetrics(ctx context.Context, from, to time.Time) (avgCost, totalCost decimal.Decimal, _ error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT 
			COALESCE(AVG(scp.price), 0) AS avg_cost,
			COALESCE(SUM(scp.price), 0) AS total_cost
		FROM shipment s
		JOIN customer_order co ON s.order_id = co.id
		JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
	`
	params := map[string]any{
		"from":         from,
		"to":           to,
		"baseCurrency": baseCurrency,
		"statusIds":    cache.OrderStatusIDsForNetRevenue(),
	}
	rows, err := storeutil.QueryListNamed[struct {
		AvgCost   decimal.Decimal `db:"avg_cost"`
		TotalCost decimal.Decimal `db:"total_cost"`
	}](ctx, s.DB, query, params)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("getShippingCostMetrics: %w", err)
	}
	if len(rows) == 0 {
		return decimal.Zero, decimal.Zero, nil
	}
	return rows[0].AvgCost, rows[0].TotalCost, nil
}

// getPaymentFees sums the Stripe processing fees (base currency) captured on orders placed
// in [from, to) over the net-revenue statuses. Unlike revenue and COGS it is NOT
// refund-adjusted: Stripe keeps the fee even when an order is refunded, so the full captured
// fee is the real contribution-margin cost. Orders without a captured fee (payment_fee NULL:
// pre-feature, non-Stripe, unpaid) contribute nothing.
func (s *Store) getPaymentFees(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	query := `
		SELECT COALESCE(SUM(co.payment_fee), 0) AS total_fees
		FROM customer_order co
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
	`
	row, err := storeutil.QueryNamedOne[struct {
		TotalFees decimal.Decimal `db:"total_fees"`
	}](ctx, s.DB, query, map[string]any{
		"from":      from,
		"to":        to,
		"statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("getPaymentFees: %w", err)
	}
	return row.TotalFees, nil
}
