package metrics

import (
	"context"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// getCoreSalesMetrics returns headline revenue (base currency) over [from, to). Revenue is
// NET of VAT — prices are VAT-inclusive, so the per-order gross-incl-VAT figure is multiplied
// by 100/(100+vat_rate_pct) using the rate snapshotted on the order (0 for pre-feature/export
// → no change). grossInclVat is the same figure before removing VAT (what the company actually
// collected), and vatAmount = grossInclVat − revenueNet. AOV is computed on net revenue.
func (s *Store) getCoreSalesMetrics(ctx context.Context, from, to time.Time) (revenueNet, grossInclVat, vatAmount decimal.Decimal, orders int, aov decimal.Decimal, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	type row struct {
		RevenueNet   decimal.Decimal `db:"revenue_net"`
		GrossInclVat decimal.Decimal `db:"gross_incl_vat"`
		Orders       int             `db:"orders"`
	}
	query := `
		WITH order_base AS (
			SELECT co.id,
				COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
				COALESCE(MAX(scp.price), 0) AS shipment_base,
				COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0) AS discount,
				COALESCE(MAX(co.promo_free_shipping), MAX(pc.free_shipping), 0) AS free_shipping,
				co.total_price,
				co.total_settled_base,
				COALESCE(co.vat_rate_pct, 0) AS vat_rate_pct,
				COALESCE(co.refunded_amount, 0) AS refunded_amount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
			GROUP BY co.id, co.total_price, co.refunded_amount, co.total_settled_base, co.vat_rate_pct
		),
		order_rev AS (
			SELECT
				COALESCE(total_settled_base, items_base * (100 - discount) / 100.0 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END)
					* (total_price - refunded_amount) / NULLIF(total_price, 0) AS gross_ivat,
				vat_rate_pct
			FROM order_base
		)
		SELECT
			COALESCE(SUM(gross_ivat), 0) AS gross_incl_vat,
			COALESCE(SUM(gross_ivat * 100.0 / (100 + vat_rate_pct)), 0) AS revenue_net,
			COUNT(*) AS orders
		FROM order_rev
	`
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, 0, decimal.Zero, err
	}
	revenueNet = r.RevenueNet.Round(2)
	grossInclVat = r.GrossInclVat.Round(2)
	vatAmount = grossInclVat.Sub(revenueNet).Round(2)
	orders = r.Orders
	if orders > 0 {
		aov = revenueNet.Div(decimal.NewFromInt(int64(orders))).Round(2)
	}
	return revenueNet, grossInclVat, vatAmount, orders, aov, nil
}

// getPeakRevenueDay returns the single calendar day with the highest net revenue in [from, to),
// with that day's net revenue (base currency) and net-revenue order count. found is false when
// the period has no revenue. Uses the same net-revenue basis as getCoreSalesMetrics but rolls up
// by DATE(placed) regardless of chart granularity; ties are broken by the earlier date.
func (s *Store) getPeakRevenueDay(ctx context.Context, from, to time.Time) (day time.Time, revenue decimal.Decimal, orders int, found bool, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.placed,
				COALESCE(ob.total_settled_base, ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0)
					* 100.0 / (100 + ob.vat_rate_pct) AS revenue_base
			FROM (
				SELECT co.id, co.placed,
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
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount, co.total_settled_base, co.vat_rate_pct
			) ob
		)
		SELECT DATE(placed) AS d, COALESCE(SUM(revenue_base), 0) AS value, COUNT(*) AS cnt
		FROM order_base
		GROUP BY DATE(placed)
		ORDER BY value DESC, d ASC
		LIMIT 1
	`
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return time.Time{}, decimal.Zero, 0, false, err
	}
	if len(rows) == 0 {
		return time.Time{}, decimal.Zero, 0, false, nil
	}
	return rows[0].D, rows[0].Value.Round(2), rows[0].Count, true, nil
}

func (s *Store) getItemsPerOrder(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	type row struct {
		TotalItems int `db:"total_items"`
		Orders     int `db:"orders"`
	}
	query := `
		SELECT COALESCE(SUM(item_count), 0) AS total_items, COUNT(*) AS orders
		FROM (
			SELECT co.id, SUM(oi.quantity) AS item_count
			FROM customer_order co
			JOIN order_item oi ON co.id = oi.order_id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
			GROUP BY co.id
		) AS order_items
	`
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return decimal.Zero, err
	}
	if r.Orders == 0 {
		return decimal.Zero, nil
	}
	return decimal.NewFromInt(int64(r.TotalItems)).Div(decimal.NewFromInt(int64(r.Orders))).Round(2), nil
}

func (s *Store) getRefundMetrics(ctx context.Context, from, to time.Time) (refundedAmount decimal.Decimal, refundedOrders int, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	type row struct {
		Amount decimal.Decimal `db:"amount"`
		Count  int             `db:"cnt"`
	}
	query := `
		WITH order_base AS (
			SELECT co.id,
				COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
				COALESCE(MAX(scp.price), 0) AS shipment_base,
				COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0) AS discount,
				COALESCE(MAX(co.promo_free_shipping), MAX(pc.free_shipping), 0) AS free_shipping,
				co.total_price,
				co.total_settled_base,
				COALESCE(co.refunded_amount, 0) AS refunded_amount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds) AND (co.refunded_amount IS NOT NULL AND co.refunded_amount > 0)
			GROUP BY co.id, co.total_price, co.refunded_amount, co.total_settled_base
		)
		SELECT
			COALESCE(SUM(
				refunded_amount * COALESCE(total_settled_base, items_base * (100 - discount) / 100.0 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END) / NULLIF(total_price, 0)
			), 0) AS amount,
			COUNT(*) AS cnt
		FROM order_base
	`
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return decimal.Zero, 0, err
	}
	return r.Amount, r.Count, nil
}

// getDiscountComponents returns discount amounts in base currency: (1) line-item product sale
// percentage off list price, (2) extra percentage off subtotal from an applied promo_code.
// Total discount shown on the dashboard is product_sale + promo_code. Promo usage rate counts
// only orders with promo_id set, so high (1) with zero promo orders is expected.
func (s *Store) getDiscountComponents(ctx context.Context, from, to time.Time) (productSale, promoCode decimal.Decimal, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	params := map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()}
	productDiscount, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * COALESCE(oi.product_sale_percentage, 0) / 100.0 * oi.quantity), 0) AS v
		FROM customer_order co
		JOIN order_item oi ON co.id = oi.order_id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
	`, params)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	promoDiscount, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		WITH order_items_base AS (
			SELECT co.id,
				COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
				COALESCE(co.promo_discount_pct, pc.discount, 0) AS discount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds) AND co.promo_id IS NOT NULL
			GROUP BY co.id, co.promo_discount_pct, pc.discount
		)
		SELECT COALESCE(SUM(items_base * discount / 100.0), 0) AS v
		FROM order_items_base
	`, params)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	return productDiscount.V, promoDiscount.V, nil
}

func (s *Store) getPromoUsageCount(ctx context.Context, from, to time.Time) (int, error) {
	type row struct {
		N int `db:"n"`
	}
	query := `
		SELECT COUNT(*) AS n FROM customer_order
		WHERE placed >= :from AND placed < :to
		AND order_status_id IN (:statusIds) AND promo_id IS NOT NULL
	`
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return 0, err
	}
	return r.N, nil
}

func (s *Store) GetPeriodOrderCount(ctx context.Context, from, to time.Time) (int, error) {
	query := `
		SELECT COUNT(*) AS cnt
		FROM customer_order
		WHERE placed >= :from AND placed < :to
		AND order_status_id IN (:statusIds)
	`
	type row struct {
		Cnt int `db:"cnt"`
	}
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{
		"from":      from,
		"to":        to,
		"statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return 0, err
	}
	return r.Cnt, nil
}
