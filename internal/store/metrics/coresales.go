package metrics

import (
	"context"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func (s *Store) getCoreSalesMetrics(ctx context.Context, from, to time.Time) (revenue decimal.Decimal, orders int, aov decimal.Decimal, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	type row struct {
		Revenue decimal.Decimal `db:"revenue"`
		Orders  int             `db:"orders"`
	}
	query := `
		WITH order_base AS (
			SELECT co.id,
				COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
				COALESCE(MAX(scp.price), 0) AS shipment_base,
				COALESCE(MAX(pc.discount), 0) AS discount,
				COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
				co.total_price,
				COALESCE(co.refunded_amount, 0) AS refunded_amount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
			GROUP BY co.id, co.total_price, co.refunded_amount
		)
		SELECT
			COALESCE(SUM(
				(items_base * (100 - discount) / 100.0 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END)
				* (total_price - refunded_amount) / NULLIF(total_price, 0)
			), 0) AS revenue,
			COUNT(*) AS orders
		FROM order_base
	`
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return decimal.Zero, 0, decimal.Zero, err
	}
	revenue = r.Revenue
	orders = r.Orders
	if orders > 0 {
		aov = revenue.Div(decimal.NewFromInt(int64(orders))).Round(2)
	}
	return revenue, orders, aov, nil
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
				COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
				COALESCE(MAX(scp.price), 0) AS shipment_base,
				COALESCE(MAX(pc.discount), 0) AS discount,
				COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
				co.total_price,
				COALESCE(co.refunded_amount, 0) AS refunded_amount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds) AND (co.refunded_amount IS NOT NULL AND co.refunded_amount > 0)
			GROUP BY co.id, co.total_price, co.refunded_amount
		)
		SELECT
			COALESCE(SUM(
				refunded_amount * (items_base * (100 - discount) / 100.0 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END) / NULLIF(total_price, 0)
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
		SELECT COALESCE(SUM(pp_base.price * COALESCE(oi.product_sale_percentage, 0) / 100.0 * oi.quantity), 0) AS v
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
				COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
				COALESCE(pc.discount, 0) AS discount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds) AND co.promo_id IS NOT NULL
			GROUP BY co.id, pc.discount
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
