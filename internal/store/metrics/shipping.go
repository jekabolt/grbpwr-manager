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

// getShippingCostMetrics returns the average and total logistics cost (base currency) over
// orders placed in [from, to) in the net-revenue statuses. Per shipment it prefers the real
// carrier invoice (shipment.actual_cost) and falls back to the configured carrier price
// (shipment_carrier_price) when no invoice was entered, then adds the return-leg cost
// (shipment.return_shipping_cost) so reverse logistics is not invisible. Shipments with no
// cost signal at all (neither an invoice nor a configured carrier price) are excluded so they
// don't drag the average toward zero.
func (s *Store) getShippingCostMetrics(ctx context.Context, from, to time.Time) (avgCost, totalCost decimal.Decimal, _ error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT
			COALESCE(AVG(COALESCE(s.actual_cost, scp.price) + COALESCE(s.return_shipping_cost, 0)), 0) AS avg_cost,
			COALESCE(SUM(COALESCE(s.actual_cost, scp.price) + COALESCE(s.return_shipping_cost, 0)), 0) AS total_cost
		FROM shipment s
		JOIN customer_order co ON s.order_id = co.id
		LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		AND COALESCE(s.actual_cost, scp.price) IS NOT NULL
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

// getPaymentFees sums the processing fees (base currency) on orders placed in [from, to) over
// the net-revenue statuses. It prefers the actual captured Stripe fee (customer_order.
// payment_fee); for an order with no captured fee (bank-invoice / cash / non-EUR-settled /
// pre-feature) it ESTIMATES the fee from the payment method's model
// (order_base × fee_pct/100 + fee_fixed), where order_base is the settled base amount or the
// EUR snapshot. Fees are NOT refund-adjusted (Stripe keeps the fee on refunds). Returns the
// combined total and, separately, the estimated portion so callers can report coverage.
func (s *Store) getPaymentFees(ctx context.Context, from, to time.Time) (total, estimated decimal.Decimal, _ error) {
	// One payment row per order in practice; MAX(payment_method_id) collapses any duplicates so
	// an order is never counted twice. est_fee = order_base × fee_pct/100 + fee_fixed, floored at 0.
	query := `
		SELECT
			COALESCE(SUM(COALESCE(co.payment_fee,
				GREATEST(COALESCE(co.total_settled_base, co.total_price_eur, 0) * pm.fee_pct / 100 + pm.fee_fixed, 0))), 0) AS total_fees,
			COALESCE(SUM(CASE WHEN co.payment_fee IS NULL
				THEN GREATEST(COALESCE(co.total_settled_base, co.total_price_eur, 0) * pm.fee_pct / 100 + pm.fee_fixed, 0)
				ELSE 0 END), 0) AS estimated_fees
		FROM customer_order co
		JOIN (SELECT order_id, MAX(payment_method_id) AS pmid FROM payment GROUP BY order_id) pp ON pp.order_id = co.id
		JOIN payment_method pm ON pm.id = pp.pmid
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
	`
	row, err := storeutil.QueryNamedOne[struct {
		TotalFees     decimal.Decimal `db:"total_fees"`
		EstimatedFees decimal.Decimal `db:"estimated_fees"`
	}](ctx, s.DB, query, map[string]any{
		"from":      from,
		"to":        to,
		"statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("getPaymentFees: %w", err)
	}
	return row.TotalFees, row.EstimatedFees, nil
}
