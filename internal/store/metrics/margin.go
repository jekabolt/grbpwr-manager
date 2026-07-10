package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// applyProductMargin fills the margin fields of a product breakdown row from its current
// cost. When the product has no cost set the fields stay zero and HasCost is false, so the
// API can render "N/A" rather than a misleading 100% margin. revenueCost is Σ(cost × net
// qty); the caller's Value is the matched net revenue.
func applyProductMargin(pm *entity.ProductMetric, unitCost decimal.NullDecimal, revenueCost decimal.Decimal) {
	if !unitCost.Valid {
		return
	}
	pm.HasCost = true
	pm.UnitCost = unitCost.Decimal
	pm.RevenueCost = revenueCost.Round(2)
	pm.GrossMargin = pm.Value.Sub(pm.RevenueCost).Round(2)
	if pm.Value.GreaterThan(decimal.Zero) {
		pm.GrossMarginPct = pm.GrossMargin.Div(pm.Value).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
}

// getMarginMetrics computes cost of goods (COGS) and the revenue it is matched against,
// in base currency, for net-revenue orders in [from, to). Each line item is joined to its
// product's cost_price. Because not every product has a cost set, it returns three sums so
// the caller can report an honest margin plus its coverage:
//
//   - costedRevenue: net product revenue of items that HAVE a cost (the margin denominator)
//   - cogs:          Σ(cost × qty), refund-adjusted, for those same items
//   - totalRevenue:  net product revenue of ALL items (the coverage denominator)
//
// Revenue reuses the same list-price × itemAdj apportionment as the product breakdowns so
// it ties out with TopProductsByRevenue; COGS uses costAdj (refund proration only — cost is
// not discounted or FX-scaled). The costed subset requires BOTH a cost_price and a base
// currency price, so an item can never contribute cost without matched revenue. Shipping is
// excluded from all three (it is a separate line on the dashboard).
func (s *Store) getMarginMetrics(ctx context.Context, from, to time.Time) (costedRevenue, cogs, totalRevenue decimal.Decimal, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	type row struct {
		CostedRevenue decimal.Decimal `db:"costed_revenue"`
		Cogs          decimal.Decimal `db:"cogs"`
		TotalRevenue  decimal.Decimal `db:"total_revenue"`
	}
	query := fmt.Sprintf(`
		WITH %s
		SELECT
			COALESCE(SUM(CASE WHEN p.cost_price IS NOT NULL AND pp_base.price IS NOT NULL
				THEN pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s
				ELSE 0 END), 0) AS costed_revenue,
			COALESCE(SUM(CASE WHEN p.cost_price IS NOT NULL AND pp_base.price IS NOT NULL
				THEN p.cost_price * oi.quantity * %s
				ELSE 0 END), 0) AS cogs,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s), 0) AS total_revenue
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN order_factors ofac ON ofac.order_id = oi.order_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
	`, orderFactorsCTE, itemAdjExpr, costAdjExpr, itemAdjExpr)
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	return r.CostedRevenue.Round(2), r.Cogs.Round(2), r.TotalRevenue.Round(2), nil
}
