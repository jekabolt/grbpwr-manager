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

// GetCountryEconomics assembles per-country profitability (analytics-v2 task 08). Revenue and order
// counts are taken from the same by-country breakdown the geography tab already shows (so the two tie
// out); COGS/margin, order-level shipping + payment fees + discount, and average customer LTV are each
// added by a targeted query and merged by country. Countries are the top 50 by revenue, revenue-desc —
// exactly the set (and order) getRevenueByGeography returns — so a country the revenue breakdown omits
// is omitted here too. All attribution is by the order's shipping-address country, matching by_country.
func (s *Store) GetCountryEconomics(ctx context.Context, from, to time.Time) ([]entity.CountryEconomicsRow, error) {
	base, err := s.getRevenueByGeography(ctx, from, to, "country", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("country economics revenue: %w", err)
	}
	if len(base) == 0 {
		return nil, nil
	}
	margin, err := s.getCountryMargin(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("country economics margin: %w", err)
	}
	orderLevel, err := s.getCountryOrderCosts(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("country economics order costs: %w", err)
	}
	ltv, err := s.getCountryLTV(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("country economics ltv: %w", err)
	}

	hundred := decimal.NewFromInt(100)
	rows := make([]entity.CountryEconomicsRow, 0, len(base))
	for _, bc := range base {
		m := margin[bc.Country]
		o := orderLevel[bc.Country]
		lv := ltv[bc.Country]

		grossMargin := m.CostedRevenue.Sub(m.Cogs).Round(2)
		contribution := grossMargin.Sub(o.ShippingCost).Sub(o.PaymentFees).Round(2)
		row := entity.CountryEconomicsRow{
			Country:            bc.Country,
			Revenue:            bc.Value.Round(2),
			Orders:             bc.Count,
			RevenueCost:        m.Cogs.Round(2),
			GrossMargin:        grossMargin,
			ShippingCost:       o.ShippingCost.Round(2),
			PaymentFees:        o.PaymentFees.Round(2),
			ContributionMargin: contribution,
			TotalDiscount:      o.TotalDiscount.Round(2),
			LtvAvg:             lv.LtvAvg.Round(2),
			LtvSample:          lv.LtvSample,
		}
		if m.CostedRevenue.GreaterThan(decimal.Zero) {
			row.GrossMarginPct = grossMargin.Div(m.CostedRevenue).Mul(hundred).Round(2).InexactFloat64()
			row.CostCoveragePct = m.CostedRevenue.Div(m.TotalRevenue).Mul(hundred).Round(2).InexactFloat64()
		}
		if bc.Count > 0 {
			row.ProfitPerOrder = contribution.Div(decimal.NewFromInt(int64(bc.Count))).Round(2)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type countryMarginAgg struct {
	Country       string          `db:"country"`
	CostedRevenue decimal.Decimal `db:"costed_revenue"`
	Cogs          decimal.Decimal `db:"cogs"`
	TotalRevenue  decimal.Decimal `db:"total_revenue"`
}

// getCountryMargin is getMarginMetrics grouped by shipping-address country: costed revenue, COGS and
// total product revenue per country, using the same order_factors apportionment (so a country's margin
// is derived on the same basis as the global one). COGS covers only items with a cost snapshot; the
// costed/total split lets the caller report cost_coverage_pct per country.
func (s *Store) getCountryMargin(ctx context.Context, from, to time.Time) (map[string]countryMarginAgg, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH %s
		SELECT a.country AS country,
			COALESCE(SUM(CASE WHEN COALESCE(oi.cost_price_at_sale, p.cost_price) IS NOT NULL AND COALESCE(oi.product_price_base, pp_base.price) IS NOT NULL
				THEN COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s
				ELSE 0 END), 0) AS costed_revenue,
			COALESCE(SUM(CASE WHEN COALESCE(oi.cost_price_at_sale, p.cost_price) IS NOT NULL AND COALESCE(oi.product_price_base, pp_base.price) IS NOT NULL
				THEN COALESCE(oi.cost_price_at_sale, p.cost_price) * oi.quantity * %s
				ELSE 0 END), 0) AS cogs,
			COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s), 0) AS total_revenue
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN order_factors ofac ON ofac.order_id = oi.order_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN buyer b ON b.order_id = oi.order_id
		JOIN address a ON b.shipping_address_id = a.id
		GROUP BY a.country
	`, orderFactorsCTE, itemAdjExpr, costAdjExpr, itemAdjExpr)
	rows, err := storeutil.QueryListNamed[countryMarginAgg](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]countryMarginAgg, len(rows))
	for _, r := range rows {
		out[r.Country] = r
	}
	return out, nil
}

type countryOrderAgg struct {
	Country       string          `db:"country"`
	ShippingCost  decimal.Decimal `db:"shipping_cost"`
	PaymentFees   decimal.Decimal `db:"payment_fees"`
	TotalDiscount decimal.Decimal `db:"total_discount"`
}

// getCountryOrderCosts sums the ORDER-level costs per shipping-address country: logistics (actual
// carrier cost, tariff fallback, plus the return leg), captured Stripe payment fees, and the total
// discount (product-sale + promo). Each order is aggregated once (inner GROUP BY co.id) before rolling
// up by country, so joining order_item for the discount subtotal does not fan the per-order costs.
// Payment fee is the captured co.payment_fee only (no estimate) — a non-Stripe order contributes 0.
func (s *Store) getCountryOrderCosts(ctx context.Context, from, to time.Time) (map[string]countryOrderAgg, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_agg AS (
			SELECT co.id, a.country AS country,
				COALESCE(MAX(ship.actual_cost), MAX(scp.price), 0) AS ship_cost,
				COALESCE(MAX(ship.return_shipping_cost), 0) AS return_cost,
				COALESCE(MAX(co.payment_fee), 0) AS payment_fee,
				COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0) AS discount,
				COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_net_base,
				COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * COALESCE(oi.product_sale_percentage, 0) / 100.0 * oi.quantity), 0) AS product_sale_disc
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment ship ON co.id = ship.order_id
			LEFT JOIN shipment_carrier_price scp ON ship.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			JOIN buyer b ON b.order_id = co.id
			JOIN address a ON b.shipping_address_id = a.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
			GROUP BY co.id, a.country
		)
		SELECT country,
			COALESCE(SUM(ship_cost + return_cost), 0) AS shipping_cost,
			COALESCE(SUM(payment_fee), 0) AS payment_fees,
			COALESCE(SUM(items_net_base * discount / 100.0 + product_sale_disc), 0) AS total_discount
		FROM order_agg
		GROUP BY country
	`
	rows, err := storeutil.QueryListNamed[countryOrderAgg](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]countryOrderAgg, len(rows))
	for _, r := range rows {
		out[r.Country] = r
	}
	return out, nil
}

type countryLtvAgg struct {
	Country   string          `db:"country"`
	LtvAvg    decimal.Decimal `db:"ltv_avg"`
	LtvSample int             `db:"ltv_sample"`
}

// getCountryLTV is the average lifetime customer value per country. A customer is placed in the country
// of their LATEST order (deterministic and explainable, unlike a mode); their lifetime value is the sum
// of ALL their net-revenue orders (all-time, gross-of-VAT customer spend — the lifetime-value convention,
// intentionally NOT the net-of-VAT period revenue used for the revenue/AOV columns on this tab). Only
// customers who ordered IN the period are averaged, so the window shapes WHICH customers count, but each
// customer's value is their full history. It is therefore a DIFFERENT quantity from the profitability
// tab's period-realized LTV (getCLVStats, in-period revenue per customer) and the two will not reconcile.
// Small countries will have a tiny ltv_sample — the caller surfaces it so the UI can grey a noisy average.
func (s *Store) getCountryLTV(ctx context.Context, from, to time.Time) (map[string]countryLtvAgg, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH cust_period AS (
			SELECT DISTINCT b.email
			FROM customer_order co
			JOIN buyer b ON b.order_id = co.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
		),
		cust_country AS (
			SELECT email, country FROM (
				SELECT b.email, a.country,
					ROW_NUMBER() OVER (PARTITION BY b.email ORDER BY co.placed DESC, co.id DESC) AS rn
				FROM customer_order co
				JOIN buyer b ON b.order_id = co.id
				JOIN address a ON b.shipping_address_id = a.id
				WHERE co.order_status_id IN (:statusIds)
			) x WHERE x.rn = 1
		),
		order_rev_all AS (
			SELECT b.email,
				COALESCE(ob.total_settled_base, ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS rev
			FROM (
				SELECT co.id, co.total_price, co.total_settled_base,
					COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(co.promo_free_shipping), MAX(pc.free_shipping), 0) AS free_shipping
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.total_settled_base, co.refunded_amount
			) ob
			JOIN buyer b ON b.order_id = ob.id
		),
		lifetime AS (
			SELECT email, COALESCE(SUM(rev), 0) AS clv FROM order_rev_all GROUP BY email
		)
		SELECT cc.country AS country,
			COALESCE(AVG(l.clv), 0) AS ltv_avg,
			COUNT(*) AS ltv_sample
		FROM cust_period cp
		JOIN lifetime l ON l.email = cp.email
		JOIN cust_country cc ON cc.email = cp.email
		GROUP BY cc.country
	`
	rows, err := storeutil.QueryListNamed[countryLtvAgg](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]countryLtvAgg, len(rows))
	for _, r := range rows {
		out[r.Country] = r
	}
	return out, nil
}
