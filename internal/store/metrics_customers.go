package store

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func (ms *MYSQLStore) getRepeatCustomerMetrics(ctx context.Context, from, to time.Time) (repeatRate, avgOrders, avgDays decimal.Decimal, err error) {
	type emailOrders struct {
		Email  string `db:"email"`
		Orders int    `db:"orders"`
	}
	query := `
		SELECT b.email, COUNT(*) AS orders
		FROM customer_order co
		JOIN buyer b ON co.id = b.order_id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY b.email
	`
	rows, err := QueryListNamed[emailOrders](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, err
	}

	var repeatCount int
	var totalOrders int
	for _, r := range rows {
		totalOrders += r.Orders
		if r.Orders > 1 {
			repeatCount++
		}
	}

	totalCustomers := len(rows)
	if totalCustomers == 0 {
		return decimal.Zero, decimal.Zero, decimal.Zero, nil
	}
	repeatRate = decimal.NewFromInt(int64(repeatCount)).Div(decimal.NewFromInt(int64(totalCustomers))).Mul(decimal.NewFromInt(100))
	avgOrders = decimal.NewFromInt(int64(totalOrders)).Div(decimal.NewFromInt(int64(totalCustomers)))

	// Avg days between orders for repeat buyers — computed in SQL with LAG, no row materialization
	q2 := `
		SELECT AVG(gap_days) AS avg_days
		FROM (
			SELECT DATEDIFF(co.placed, LAG(co.placed) OVER (PARTITION BY b.email ORDER BY co.placed)) AS gap_days
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
		) t
		WHERE gap_days IS NOT NULL
	`
	params := map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()}
	avgDaysRow, err := QueryNamedOne[struct {
		AvgDays *float64 `db:"avg_days"`
	}](ctx, ms.DB(), q2, params)
	if err != nil {
		return repeatRate, avgOrders, decimal.Zero, err
	}
	if avgDaysRow.AvgDays != nil {
		avgDays = decimal.NewFromFloat(*avgDaysRow.AvgDays)
	}
	return repeatRate, avgOrders, avgDays, nil
}

func (ms *MYSQLStore) getCLVStats(ctx context.Context, from, to time.Time) (entity.CLVStats, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, b.email,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id,
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
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
			JOIN customer_order co ON ob.id = co.id
			JOIN buyer b ON co.id = b.order_id
		)
		SELECT email, COALESCE(SUM(revenue_base), 0) AS clv
		FROM order_base
		GROUP BY email
	`
	rows, err := QueryListNamed[struct {
		Email string          `db:"email"`
		CLV   decimal.Decimal `db:"clv"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return entity.CLVStats{}, err
	}

	if len(rows) == 0 {
		return entity.CLVStats{}, nil
	}

	clvs := make([]float64, 0, len(rows))
	for _, r := range rows {
		f, ok := r.CLV.Float64()
		if !ok {
			continue
		}
		clvs = append(clvs, f)
	}
	sort.Float64s(clvs)

	if len(clvs) == 0 {
		return entity.CLVStats{}, nil
	}
	mean := 0.0
	for _, v := range clvs {
		mean += v
	}
	mean /= float64(len(clvs))

	median := 0.0
	if len(clvs)%2 == 1 {
		median = clvs[len(clvs)/2]
	} else {
		median = (clvs[len(clvs)/2-1] + clvs[len(clvs)/2]) / 2
	}

	p90Idx := int(math.Ceil(float64(len(clvs))*0.9)) - 1
	if p90Idx < 0 {
		p90Idx = 0
	}
	p90 := clvs[p90Idx]

	return entity.CLVStats{
		Mean:   decimal.NewFromFloat(mean).Round(2),
		Median: decimal.NewFromFloat(median).Round(2),
		P90:    decimal.NewFromFloat(p90).Round(2),
	}, nil
}

func (ms *MYSQLStore) getRevenueByPromo(ctx context.Context, from, to time.Time) ([]entity.PromoMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, ob.promo_id, ob.code, ob.discount,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, pc.id AS promo_id, pc.code, pc.discount,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount, pc.id, pc.code, pc.discount
			) ob
		)
		SELECT code, COUNT(*) AS orders_count,
			COALESCE(SUM(revenue_base), 0) AS revenue,
			COALESCE(AVG(discount), 0) AS avg_discount
		FROM order_base
		GROUP BY promo_id, code
		ORDER BY revenue DESC
		LIMIT 20
	`
	rows, err := QueryListNamed[struct {
		Code        string          `db:"code"`
		OrdersCount int             `db:"orders_count"`
		Revenue     decimal.Decimal `db:"revenue"`
		AvgDiscount decimal.Decimal `db:"avg_discount"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.PromoMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.PromoMetric{
			PromoCode:   r.Code,
			OrdersCount: r.OrdersCount,
			Revenue:     r.Revenue,
			AvgDiscount: r.AvgDiscount,
		}
	}
	return result, nil
}

func (ms *MYSQLStore) getOrdersByStatus(ctx context.Context, from, to time.Time) ([]entity.StatusCount, error) {
	query := `
		SELECT os.name, COUNT(*) AS cnt
		FROM customer_order co
		JOIN order_status os ON co.order_status_id = os.id
		WHERE co.placed >= :from AND co.placed < :to
		GROUP BY co.order_status_id, os.name
		ORDER BY cnt DESC
	`
	rows, err := QueryListNamed[struct {
		Name  string `db:"name"`
		Count int    `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.StatusCount, len(rows))
	for i, r := range rows {
		result[i] = entity.StatusCount{StatusName: r.Name, Count: r.Count}
	}
	return result, nil
}
