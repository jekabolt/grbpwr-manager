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

func (s *Store) getRevenueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				COALESCE(ob.total_settled_base, ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0)
					* 100.0 / (100 + ob.vat_rate_pct) AS revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					co.total_settled_base, COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(co.vat_rate_pct, 0) AS vat_rate_pct
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount, co.vat_rate_pct
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getOrdersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt, 0 AS value
		FROM customer_order co
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Count int             `db:"cnt"`
		Value decimal.Decimal `db:"value"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getSubscribersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt
		FROM subscriber
		WHERE created_at IS NOT NULL AND created_at >= :from AND created_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Count: r.Count, Value: decimal.NewFromInt(int64(r.Count))}
	}
	return result, nil
}

func (s *Store) getGrossRevenueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				(ob.items_list_price + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) AS gross_revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * oi.quantity), 0) AS items_list_price,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.placed
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(gross_revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

// getGrossRevenueTotal returns total revenue at list prices (before any discounts or refunds) + shipping.
// This is the true "gross" revenue: items_list_price + shipping, before product sale discounts,
// promo code discounts, or refunds are applied. Matches the sum of getGrossRevenueByPeriod.
func (s *Store) getGrossRevenueTotal(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT
				(ob.items_list_price + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) AS gross_revenue_base
			FROM (
				SELECT co.id,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * oi.quantity), 0) AS items_list_price,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id
			) ob
		)
		SELECT COALESCE(SUM(gross_revenue_base), 0) AS total
		FROM order_base
	`
	type row struct {
		Total decimal.Decimal `db:"total"`
	}
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return decimal.Zero, err
	}
	return r.Total, nil
}

func (s *Store) getRefundsByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				refunded_amount * COALESCE(ob.total_settled_base, ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) / NULLIF(ob.total_price, 0) AS refunded_base
			FROM (
				SELECT co.id, co.placed, co.total_settled_base, COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIdsRefund)
				AND COALESCE(co.refunded_amount, 0) > 0
				GROUP BY co.id, co.placed, co.refunded_amount, co.total_price
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(refunded_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIdsRefund": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getAvgOrderValueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				COALESCE(ob.total_settled_base, ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0)
					* 100.0 / (100 + ob.vat_rate_pct) AS revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					co.total_settled_base, COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(co.vat_rate_pct, 0) AS vat_rate_pct
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount, co.vat_rate_pct
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(revenue_base), 0) / NULLIF(COUNT(*), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		val := r.Value
		if r.Count == 0 {
			val = decimal.Zero
		}
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: val.Round(2), Count: r.Count}
	}
	return result, nil
}

func (s *Store) getUnitsSoldByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d,
			COALESCE(SUM(oi.quantity), 0) AS value,
			COUNT(DISTINCT co.id) AS cnt
		FROM customer_order co
		JOIN order_item oi ON co.id = oi.order_id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getNewVsReturningCustomersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) (newCustomers, returningCustomers []entity.TimeSeriesPoint, err error) {
	query := fmt.Sprintf(`
		WITH first_order AS (
			-- A customer first order is taken across ALL statuses; filtering to
			-- net-revenue statuses here would treat someone whose first order was
			-- cancelled or unpaid as new on a later order, overcounting new customers.
			SELECT b.email, MIN(co.placed) AS first_placed
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			GROUP BY b.email
		),
		period_orders AS (
			SELECT co.placed,
				%s AS bucket,
				b.email
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
		)
		SELECT po.bucket AS d,
			SUM(CASE WHEN fo.first_placed >= :from THEN 1 ELSE 0 END) AS new_cnt,
			SUM(CASE WHEN fo.first_placed < :from THEN 1 ELSE 0 END) AS ret_cnt
		FROM period_orders po
		JOIN first_order fo ON po.email = fo.email
		GROUP BY po.bucket
		ORDER BY d
	`, dateExpr)
	rows, err := storeutil.QueryListNamed[struct {
		D      time.Time `db:"d"`
		NewCnt int       `db:"new_cnt"`
		RetCnt int       `db:"ret_cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, nil, err
	}
	newCustomers = make([]entity.TimeSeriesPoint, len(rows))
	returningCustomers = make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		newCustomers[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.NewCnt)), Count: r.NewCnt}
		returningCustomers[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.RetCnt)), Count: r.RetCnt}
	}
	return newCustomers, returningCustomers, nil
}

type newVsReturningRevenueResult struct {
	newOrders, retOrders   int
	newRevenue, retRevenue decimal.Decimal
	newByDay, retByDay     []entity.TimeSeriesPoint
}

// getNewVsReturningRevenue splits net revenue and order counts by whether the buyer's first-ever
// order (across ALL statuses — same rule as getNewVsReturningCustomersByPeriod, so a cancelled
// early order still marks the buyer returning) falls in [from, to). Returns aggregate totals plus
// per-bucket daily revenue series (Value=EUR, Count=orders). Revenue uses the same net-of-VAT /
// refund-prorated basis as headline revenue, so new+returning reconciles with commerce revenue.
func (s *Store) getNewVsReturningRevenue(ctx context.Context, from, to time.Time, dateExpr string) (newVsReturningRevenueResult, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	bucketExpr := strings.ReplaceAll(dateExpr, "co.placed", "ob.placed")
	query := fmt.Sprintf(`
		WITH first_order AS (
			SELECT b.email, MIN(co.placed) AS first_placed
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			GROUP BY b.email
		),
		order_base AS (
			SELECT ob.id, ob.placed, ob.email,
				COALESCE(ob.total_settled_base, ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0)
					* 100.0 / (100 + ob.vat_rate_pct) AS revenue_base
			FROM (
				SELECT co.id, co.placed, b.email,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price, co.total_settled_base, COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(co.vat_rate_pct, 0) AS vat_rate_pct
				FROM customer_order co
				JOIN buyer b ON co.id = b.order_id
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.placed, b.email, co.total_price, co.refunded_amount, co.total_settled_base, co.vat_rate_pct
			) ob
		)
		SELECT CASE WHEN fo.first_placed >= :from THEN 1 ELSE 0 END AS is_new,
			%s AS d,
			COUNT(*) AS orders,
			COALESCE(SUM(ob.revenue_base), 0) AS revenue
		FROM order_base ob
		JOIN first_order fo ON fo.email = ob.email
		GROUP BY is_new, %s
		ORDER BY d
	`, bucketExpr, bucketExpr)
	rows, err := storeutil.QueryListNamed[struct {
		IsNew   int             `db:"is_new"`
		D       time.Time       `db:"d"`
		Orders  int             `db:"orders"`
		Revenue decimal.Decimal `db:"revenue"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return newVsReturningRevenueResult{}, err
	}
	var res newVsReturningRevenueResult
	for _, r := range rows {
		pt := entity.TimeSeriesPoint{Date: r.D, Value: r.Revenue.Round(2), Count: r.Orders}
		if r.IsNew == 1 {
			res.newOrders += r.Orders
			res.newRevenue = res.newRevenue.Add(r.Revenue)
			res.newByDay = append(res.newByDay, pt)
		} else {
			res.retOrders += r.Orders
			res.retRevenue = res.retRevenue.Add(r.Revenue)
			res.retByDay = append(res.retByDay, pt)
		}
	}
	res.newRevenue = res.newRevenue.Round(2)
	res.retRevenue = res.retRevenue.Round(2)
	return res, nil
}

func (s *Store) getShippedByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt
		FROM (
			SELECT osh.order_id, MIN(osh.changed_at) AS shipped_at
			FROM order_status_history osh
			WHERE osh.order_status_id = :shippedStatusId
			GROUP BY osh.order_id
		) AS first_shipped
		WHERE shipped_at >= :from AND shipped_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "shippedStatusId": cache.OrderStatusShipped.Status.Id})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.Count)), Count: r.Count}
	}
	return result, nil
}

func (s *Store) getDeliveredByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt
		FROM (
			SELECT osh.order_id, MIN(osh.changed_at) AS delivered_at
			FROM order_status_history osh
			WHERE osh.order_status_id = :deliveredStatusId
			GROUP BY osh.order_id
		) AS first_delivered
		WHERE delivered_at >= :from AND delivered_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := storeutil.QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "deliveredStatusId": cache.OrderStatusDelivered.Status.Id})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.Count)), Count: r.Count}
	}
	return result, nil
}
