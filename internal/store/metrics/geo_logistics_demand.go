package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// GetCountryLogistics returns per-country fulfilment and returns (analytics-v2 task 09): placed→shipped
// and placed→delivered durations, on-time rate, average shipping cost, and refund rate/orders. Country
// order and set come from the same by-country revenue breakdown the geography tab shows (top 50 by
// revenue, revenue-desc). Durations use the delivery.go computation applied per country; refund rate is
// the refunded base amount over the country's gross (net revenue + refunds), computed over net-revenue
// orders so numerator and denominator cover the same orders (full refunds live in the global section).
func (s *Store) GetCountryLogistics(ctx context.Context, from, to time.Time) ([]entity.CountryLogisticsRow, error) {
	base, err := s.getRevenueByGeography(ctx, from, to, "country", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("country logistics revenue: %w", err)
	}
	if len(base) == 0 {
		return nil, nil
	}
	logRows, err := s.getCountryLogisticsRows(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("country logistics durations: %w", err)
	}
	refunds, err := s.getCountryRefunds(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("country logistics refunds: %w", err)
	}

	agg := map[string]*logisticsAgg{}
	for _, r := range logRows {
		a := agg[r.Country]
		if a == nil {
			a = &logisticsAgg{}
			agg[r.Country] = a
		}
		if r.ShippedAt.Valid {
			if d := daysBetween(r.Placed, r.ShippedAt.Time); d > 0 {
				a.placedToShipped = append(a.placedToShipped, d)
			}
		}
		if r.DeliveredAt.Valid {
			if d := daysBetween(r.Placed, r.DeliveredAt.Time); d > 0 {
				a.placedToDelivered = append(a.placedToDelivered, d)
			}
			if r.ETA.Valid {
				a.onTimeTotal++
				if !dateOnlyUTC(r.DeliveredAt.Time).After(dateOnlyUTC(r.ETA.Time)) {
					a.onTimeHit++
				}
			}
		}
		if r.ShippingCost.Valid {
			a.shipSum = a.shipSum.Add(r.ShippingCost.Decimal).Add(r.ReturnCost)
			a.shipCount++
		}
	}

	out := make([]entity.CountryLogisticsRow, 0, len(base))
	for _, bc := range base {
		a := agg[bc.Country]
		if a == nil {
			a = &logisticsAgg{}
		}
		row := entity.CountryLogisticsRow{
			Country:                  bc.Country,
			AvgDaysPlacedToShipped:   round2(mean(a.placedToShipped)),
			AvgDaysPlacedToDelivered: round2(mean(a.placedToDelivered)),
			DeliveredSample:          len(a.placedToDelivered),
			RefundOrders:             refunds[bc.Country].orders,
		}
		if a.onTimeTotal > 0 {
			row.OnTimeRatePct = round2(float64(a.onTimeHit) / float64(a.onTimeTotal) * 100)
		}
		if a.shipCount > 0 {
			row.AvgShippingCost = a.shipSum.Div(decimal.NewFromInt(int64(a.shipCount))).Round(2)
		}
		// Refund rate = refunded / gross, where gross = net revenue (post-refund) + refunded, so it is
		// self-consistent without a separate pre-refund gross query.
		if rf := refunds[bc.Country].base; rf.GreaterThan(decimal.Zero) {
			gross := bc.Value.Add(rf)
			if gross.GreaterThan(decimal.Zero) {
				row.RefundRatePct = rf.Div(gross).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
			}
		}
		out = append(out, row)
	}
	return out, nil
}

type logisticsAgg struct {
	placedToShipped, placedToDelivered []float64
	onTimeTotal, onTimeHit             int
	shipSum                            decimal.Decimal
	shipCount                          int
}

type countryLogisticsRaw struct {
	Country      string              `db:"country"`
	Placed       time.Time           `db:"placed"`
	ShippedAt    sql.NullTime        `db:"shipped_at"`
	DeliveredAt  sql.NullTime        `db:"delivered_at"`
	ETA          sql.NullTime        `db:"eta"`
	ShippingCost decimal.NullDecimal `db:"shipping_cost"`
	ReturnCost   decimal.Decimal     `db:"return_cost"`
}

// getCountryLogisticsRows returns one timestamp/shipping tuple per net-revenue order placed in the
// window, tagged with its shipping-address country — the raw material the caller groups by country.
func (s *Store) getCountryLogisticsRows(ctx context.Context, from, to time.Time) ([]countryLogisticsRaw, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT a.country AS country, co.placed,
			MIN(CASE WHEN os.name = 'shipped' THEN h.changed_at END) AS shipped_at,
			MIN(CASE WHEN os.name = 'delivered' THEN h.changed_at END) AS delivered_at,
			MAX(sh.estimated_arrival_date) AS eta,
			COALESCE(MAX(sh.actual_cost), MAX(scp.price)) AS shipping_cost,
			COALESCE(MAX(sh.return_shipping_cost), 0) AS return_cost
		FROM customer_order co
		JOIN buyer b ON b.order_id = co.id
		JOIN address a ON b.shipping_address_id = a.id
		LEFT JOIN order_status_history h ON h.order_id = co.id
		LEFT JOIN order_status os ON os.id = h.order_status_id
		LEFT JOIN shipment sh ON sh.order_id = co.id
		LEFT JOIN shipment_carrier_price scp ON sh.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY co.id, a.country, co.placed
	`
	return storeutil.QueryListNamed[countryLogisticsRaw](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
}

type countryRefund struct {
	base   decimal.Decimal
	orders int
}

// getCountryRefunds returns the refunded base amount and refunded-order count per country over
// net-revenue orders (so it pairs with the by-country net revenue). Refunded base per order mirrors
// getRefundMetrics: refunded_amount scaled to base by the order's settled/reconstructed total.
func (s *Store) getCountryRefunds(ctx context.Context, from, to time.Time) (map[string]countryRefund, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT co.id, a.country AS country,
				COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
				COALESCE(MAX(scp.price), 0) AS shipment_base,
				COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0) AS discount,
				COALESCE(MAX(co.promo_free_shipping), MAX(pc.free_shipping), 0) AS free_shipping,
				co.total_price, co.total_settled_base,
				COALESCE(co.refunded_amount, 0) AS refunded_amount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			JOIN buyer b ON b.order_id = co.id
			JOIN address a ON b.shipping_address_id = a.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds) AND co.refunded_amount IS NOT NULL AND co.refunded_amount > 0
			GROUP BY co.id, a.country, co.total_price, co.refunded_amount, co.total_settled_base
		)
		SELECT country,
			COALESCE(SUM(refunded_amount * COALESCE(total_settled_base, items_base * (100 - discount) / 100.0 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END) / NULLIF(total_price, 0)), 0) AS refunded_base,
			COUNT(*) AS refund_orders
		FROM order_base
		GROUP BY country
	`
	rows, err := storeutil.QueryListNamed[struct {
		Country      string          `db:"country"`
		RefundedBase decimal.Decimal `db:"refunded_base"`
		RefundOrders int             `db:"refund_orders"`
	}](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]countryRefund, len(rows))
	for _, r := range rows {
		out[r.Country] = countryRefund{base: r.RefundedBase, orders: r.RefundOrders}
	}
	return out, nil
}

// GetCountryDemand returns the DB side of per-country demand (analytics-v2 task 09): orders, AOV,
// new-vs-returning customers and the top 3 categories, keyed by shipping-address country. Sessions,
// conversion and the caveat are left zero here — the caller merges GA4 sessions (a different store) and
// computes conversion, because GA4 country names must first be mapped to ISO-2. Country order/set match
// the by-country revenue breakdown.
func (s *Store) GetCountryDemand(ctx context.Context, from, to time.Time) ([]entity.CountryDemandRow, error) {
	base, err := s.getRevenueByGeography(ctx, from, to, "country", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("country demand revenue: %w", err)
	}
	if len(base) == 0 {
		return nil, nil
	}
	nvr, err := s.getCountryNewReturning(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("country demand new/returning: %w", err)
	}
	cats, err := s.getCountryTopCategories(ctx, from, to, 3)
	if err != nil {
		return nil, fmt.Errorf("country demand categories: %w", err)
	}

	out := make([]entity.CountryDemandRow, 0, len(base))
	for _, bc := range base {
		nr := nvr[bc.Country]
		row := entity.CountryDemandRow{
			Country:            bc.Country,
			Orders:             bc.Count,
			NewCustomers:       nr.newCustomers,
			ReturningCustomers: nr.returningCustomers,
			TopCategories:      cats[bc.Country],
		}
		if bc.AvgOrderValue != nil {
			row.AOV = *bc.AvgOrderValue
		}
		if total := nr.newCustomers + nr.returningCustomers; total > 0 {
			row.NewSharePct = round2(float64(nr.newCustomers) / float64(total) * 100)
		}
		out = append(out, row)
	}
	return out, nil
}

type countryNewReturning struct {
	newCustomers, returningCustomers int
}

// getCountryNewReturning counts distinct customers per country split by whether their first-ever order
// (across ALL statuses — same rule as the global new-vs-returning split) falls in the period. A
// customer is counted once per country they ordered to in the window.
func (s *Store) getCountryNewReturning(ctx context.Context, from, to time.Time) (map[string]countryNewReturning, error) {
	query := `
		WITH first_order AS (
			SELECT b.email, MIN(co.placed) AS first_placed
			FROM customer_order co
			JOIN buyer b ON b.order_id = co.id
			GROUP BY b.email
		),
		period_customers AS (
			SELECT DISTINCT b.email, a.country
			FROM customer_order co
			JOIN buyer b ON b.order_id = co.id
			JOIN address a ON b.shipping_address_id = a.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
		)
		SELECT pc.country,
			SUM(CASE WHEN fo.first_placed >= :from THEN 1 ELSE 0 END) AS new_customers,
			SUM(CASE WHEN fo.first_placed < :from THEN 1 ELSE 0 END) AS returning_customers
		FROM period_customers pc
		JOIN first_order fo ON fo.email = pc.email
		GROUP BY pc.country
	`
	rows, err := storeutil.QueryListNamed[struct {
		Country            string `db:"country"`
		NewCustomers       int    `db:"new_customers"`
		ReturningCustomers int    `db:"returning_customers"`
	}](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]countryNewReturning, len(rows))
	for _, r := range rows {
		out[r.Country] = countryNewReturning{newCustomers: r.NewCustomers, returningCustomers: r.ReturningCustomers}
	}
	return out, nil
}

// getCountryTopCategories returns the top-N categories by net revenue per country, with each category's
// share of that country's total category revenue. Item-level basis (same order_factors apportionment as
// the global category breakdown), attributed to the order's shipping-address country.
func (s *Store) getCountryTopCategories(ctx context.Context, from, to time.Time, topN int) (map[string][]entity.CategoryMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH %s
		SELECT a.country AS country, p.top_category_id AS category_id, c.name AS category_name,
			COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s), 0) AS value,
			SUM(oi.quantity) AS cnt
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN order_factors ofac ON ofac.order_id = oi.order_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN category c ON p.top_category_id = c.id
		JOIN buyer b ON b.order_id = oi.order_id
		JOIN address a ON b.shipping_address_id = a.id
		GROUP BY a.country, p.top_category_id, c.name
		ORDER BY a.country, value DESC
	`, orderFactorsCTE, itemAdjExpr)
	rows, err := storeutil.QueryListNamed[struct {
		Country             string          `db:"country"`
		CategoryId          int             `db:"category_id"`
		CategoryName        string          `db:"category_name"`
		Value               decimal.Decimal `db:"value"`
		Count               int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, err
	}

	// Total category revenue per country (for the share), then keep the top N (rows are value-desc
	// within each country already).
	countryTotal := map[string]decimal.Decimal{}
	for _, r := range rows {
		countryTotal[r.Country] = countryTotal[r.Country].Add(r.Value)
	}
	out := map[string][]entity.CategoryMetric{}
	for _, r := range rows {
		if len(out[r.Country]) >= topN {
			continue
		}
		cm := entity.CategoryMetric{
			CategoryId:          r.CategoryId,
			CategoryName:        r.CategoryName,
			CategoryDisplayName: formatCategoryDisplayName(r.CategoryName),
			Value:               r.Value.Round(2),
			Count:               r.Count,
		}
		if tot := countryTotal[r.Country]; tot.GreaterThan(decimal.Zero) {
			cm.SharePct = r.Value.Div(tot).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
		}
		out[r.Country] = append(out[r.Country], cm)
	}
	return out, nil
}
