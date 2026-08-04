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

// rowMargin holds the derived per-row margin fields shared by ProductMetric, SlowMoverRow
// and RevenueParetoRow. All zero (and HasCost=false) when the product has no cost set.
type rowMargin struct {
	HasCost        bool
	UnitCost       decimal.Decimal
	RevenueCost    decimal.Decimal
	GrossMargin    decimal.Decimal
	GrossMarginPct float64
}

// computeRowMargin derives a row's margin from its matched revenue and cost inputs. When
// unitCost is NULL the product has no cost set, so every field stays zero and HasCost is
// false — the API can then render "N/A" rather than a misleading 100% margin. revenueCost
// is Σ(cost × qty) on the same basis as `revenue` (the caller matches the two).
func computeRowMargin(revenue decimal.Decimal, unitCost decimal.NullDecimal, revenueCost decimal.Decimal) rowMargin {
	if !unitCost.Valid {
		return rowMargin{}
	}
	rm := rowMargin{HasCost: true, UnitCost: unitCost.Decimal, RevenueCost: revenueCost.Round(2)}
	rm.GrossMargin = revenue.Sub(rm.RevenueCost).Round(2)
	if revenue.GreaterThan(decimal.Zero) {
		rm.GrossMarginPct = rm.GrossMargin.Div(revenue).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
	return rm
}

// applyProductMargin fills the margin fields of a product breakdown row from its sale-time
// cost. When the sold lines carry no cost the fields stay zero and HasCost is false, so the
// API can render "N/A" rather than a misleading 100% margin. The margin is computed over the
// COSTED SUBSET (costedRevenue − revenueCost), never the row's full Value: with snapshot-only
// costing a product routinely mixes pre-costing (uncosted) and costed lines, and matching the
// full revenue against the partial cost would inflate margin exactly on the least-costed rows.
// pm.Value keeps the full revenue for display.
func applyProductMargin(pm *entity.ProductMetric, costedRevenue decimal.Decimal, unitCost decimal.NullDecimal, revenueCost decimal.Decimal) {
	rm := computeRowMargin(costedRevenue, unitCost, revenueCost)
	pm.HasCost, pm.UnitCost, pm.RevenueCost = rm.HasCost, rm.UnitCost, rm.RevenueCost
	pm.GrossMargin, pm.GrossMarginPct = rm.GrossMargin, rm.GrossMarginPct
}

// getMarginMetrics computes cost of goods (COGS) and the revenue it is matched against,
// in base currency, for net-revenue orders in [from, to). Each line item's cost is its
// sale-time snapshot (order_item.cost_price_at_sale) and nothing else — the product's live
// cost_price is deliberately NOT consulted (owner decision 2026-08-04): lines placed before
// the snapshot column existed, or before the product was costed, count as uncosted forever
// rather than drifting with today's cost. Because not every sold line carries a snapshot,
// it returns three sums so the caller can report an honest margin plus its coverage:
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
			COALESCE(SUM(CASE WHEN oi.cost_price_at_sale IS NOT NULL AND COALESCE(oi.product_price_base, pp_base.price) IS NOT NULL
				THEN COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s
				ELSE 0 END), 0) AS costed_revenue,
			COALESCE(SUM(CASE WHEN oi.cost_price_at_sale IS NOT NULL AND COALESCE(oi.product_price_base, pp_base.price) IS NOT NULL
				THEN oi.cost_price_at_sale * oi.quantity * %s
				ELSE 0 END), 0) AS cogs,
			COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s), 0) AS total_revenue
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

// getUncostedSoldProductIDs returns the IDs of products whose [from, to) net-revenue sales
// include lines with no sale-time cost snapshot AND that still have no cost_price today,
// ordered by their period revenue descending. These are the products darkening the margin
// coverage figure that the operator can still act on — setting a cost (via a tech card)
// makes their future sales costed. Products whose cost has since been set are excluded
// even if their historical uncosted lines keep the window's coverage below 100%: those
// lines are frozen facts (snapshot-only COGS, no live fallback) and no action fixes them.
func (s *Store) getUncostedSoldProductIDs(ctx context.Context, from, to time.Time) ([]int, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH %s
		SELECT oi.product_id
		FROM order_item oi
		JOIN product p ON p.id = oi.product_id
		JOIN order_factors ofac ON ofac.order_id = oi.order_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		WHERE oi.cost_price_at_sale IS NULL AND p.cost_price IS NULL AND p.lifecycle_status <> 4
		GROUP BY oi.product_id
		ORDER BY COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s), 0) DESC
	`, orderFactorsCTE, itemAdjExpr)
	rows, err := storeutil.QueryListNamed[struct {
		ProductID int `db:"product_id"`
	}](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int, len(rows))
	for i, r := range rows {
		ids[i] = r.ProductID
	}
	return ids, nil
}
