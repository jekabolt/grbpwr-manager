package metrics

import (
	"context"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

type valueBand struct {
	label string
	from  decimal.Decimal
	to    decimal.Decimal // zero = open-ended (last band)
}

// orderValueBandDefs are the fixed order-value buckets (base currency, net revenue). Fixed edges
// (not quantiles) keep the histogram comparable period-to-period at this data scale; when volume
// grows these could become operator-tunable like the alert thresholds.
func orderValueBandDefs() []valueBand {
	d := decimal.NewFromInt
	return []valueBand{
		{"€0–50", d(0), d(50)},
		{"€50–100", d(50), d(100)},
		{"€100–150", d(100), d(150)},
		{"€150–200", d(150), d(200)},
		{"€200–300", d(200), d(300)},
		{"€300–500", d(300), d(500)},
		{"€500+", d(500), decimal.Zero},
	}
}

// orderValueBandIndex returns the index of the band containing rev. Bands are [from, to); the last
// band is open-ended. A heavily-refunded order with net revenue below the first edge falls in band 0.
func orderValueBandIndex(rev decimal.Decimal, bands []valueBand) int {
	for i, b := range bands {
		if b.to.IsZero() { // open-ended last band
			return i
		}
		if rev.LessThan(b.to) {
			return i
		}
	}
	return len(bands) - 1
}

// GetOrderValueBands buckets net-revenue orders placed in [from, to) into fixed order-value bands
// (base currency), returning per-band order/revenue counts, their shares and in-band AOV. Order
// value is the same net-of-VAT, refund-prorated figure as AOV, so bands are internally consistent
// with the headline average. Empty bands are returned with zeros so the axis is stable on the UI.
func (s *Store) GetOrderValueBands(ctx context.Context, from, to time.Time) ([]entity.OrderValueBandRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT
				COALESCE(ob.total_settled_base, ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0)
					* 100.0 / (100 + ob.vat_rate_pct) AS revenue_base
			FROM (
				SELECT co.id,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(co.promo_free_shipping), MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price, co.total_settled_base, COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(co.vat_rate_pct, 0) AS vat_rate_pct
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount, co.total_settled_base, co.vat_rate_pct
			) ob
		)
		SELECT COALESCE(revenue_base, 0) AS revenue_base FROM order_base
	`
	revs, err := storeutil.QueryListNamed[struct {
		Rev decimal.Decimal `db:"revenue_base"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}

	bands := orderValueBandDefs()
	orders := make([]int, len(bands))
	revenue := make([]decimal.Decimal, len(bands))
	var totalOrders int
	var totalRevenue decimal.Decimal
	for _, r := range revs {
		i := orderValueBandIndex(r.Rev, bands)
		orders[i]++
		revenue[i] = revenue[i].Add(r.Rev)
		totalOrders++
		totalRevenue = totalRevenue.Add(r.Rev)
	}

	hundred := decimal.NewFromInt(100)
	rows := make([]entity.OrderValueBandRow, len(bands))
	for i, b := range bands {
		row := entity.OrderValueBandRow{
			Label:   b.label,
			From:    b.from,
			To:      b.to,
			Orders:  orders[i],
			Revenue: revenue[i].Round(2),
		}
		if totalOrders > 0 {
			row.OrdersSharePct = decimal.NewFromInt(int64(orders[i])).Div(decimal.NewFromInt(int64(totalOrders))).Mul(hundred).Round(2).InexactFloat64()
		}
		if totalRevenue.GreaterThan(decimal.Zero) {
			row.RevenueSharePct = revenue[i].Div(totalRevenue).Mul(hundred).Round(2).InexactFloat64()
		}
		if orders[i] > 0 {
			row.AvgOrderValue = revenue[i].Div(decimal.NewFromInt(int64(orders[i]))).Round(2)
		}
		rows[i] = row
	}
	return rows, nil
}
