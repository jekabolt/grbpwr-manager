package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func (ms *MYSQLStore) getRevenueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
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
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getOrdersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt, 0 AS value
		FROM customer_order co
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Count int             `db:"cnt"`
		Value decimal.Decimal `db:"value"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getSubscribersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt
		FROM subscriber
		WHERE created_at IS NOT NULL AND created_at >= :from AND created_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Count: r.Count, Value: decimal.NewFromInt(int64(r.Count))}
	}
	return result, nil
}

func (ms *MYSQLStore) getGrossRevenueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) AS gross_revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
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
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getRefundsByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				refunded_amount * (ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) / NULLIF(ob.total_price, 0) AS refunded_base
			FROM (
				SELECT co.id, co.placed, COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
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
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIdsRefund": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getAvgOrderValueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
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
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(revenue_base), 0) / NULLIF(COUNT(*), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
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

func (ms *MYSQLStore) getUnitsSoldByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
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
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getNewVsReturningCustomersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) (newCustomers, returningCustomers []entity.TimeSeriesPoint, err error) {
	query := fmt.Sprintf(`
		WITH ranked AS (
			SELECT co.placed,
				%s AS bucket,
				ROW_NUMBER() OVER (PARTITION BY b.email ORDER BY co.placed) AS rn
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.order_status_id IN (:statusIds)
		)
		SELECT bucket AS d,
			SUM(CASE WHEN rn = 1 THEN 1 ELSE 0 END) AS new_cnt,
			SUM(CASE WHEN rn > 1 THEN 1 ELSE 0 END) AS ret_cnt
		FROM ranked
		WHERE placed >= :from AND placed < :to
		GROUP BY bucket
		ORDER BY d
	`, dateExpr)
	rows, err := QueryListNamed[struct {
		D      time.Time `db:"d"`
		NewCnt int       `db:"new_cnt"`
		RetCnt int       `db:"ret_cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
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

func (ms *MYSQLStore) getShippedByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
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
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "shippedStatusId": cache.OrderStatusShipped.Status.Id})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.Count)), Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getDeliveredByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
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
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "deliveredStatusId": cache.OrderStatusDelivered.Status.Id})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.Count)), Count: r.Count}
	}
	return result, nil
}
