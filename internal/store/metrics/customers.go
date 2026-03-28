package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func (s *Store) getRepeatCustomerMetrics(ctx context.Context, from, to time.Time) (repeatRate, avgOrders, avgDays decimal.Decimal, err error) {
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
	rows, err := storeutil.QueryListNamed[emailOrders](ctx, s.DB, query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
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
	avgDaysRow, err := storeutil.QueryNamedOne[struct {
		AvgDays *float64 `db:"avg_days"`
	}](ctx, s.DB, q2, params)
	if err != nil {
		return repeatRate, avgOrders, decimal.Zero, err
	}
	if avgDaysRow.AvgDays != nil {
		avgDays = decimal.NewFromFloat(*avgDaysRow.AvgDays)
	}
	return repeatRate, avgOrders, avgDays, nil
}

func (s *Store) getCLVStats(ctx context.Context, from, to time.Time) (entity.CLVStats, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, b.email,
				(ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
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
			) ob
			JOIN customer_order co ON ob.id = co.id
			JOIN buyer b ON co.id = b.order_id
		)
		SELECT email, COALESCE(SUM(revenue_base), 0) AS clv
		FROM order_base
		GROUP BY email
	`
	rows, err := storeutil.QueryListNamed[struct {
		Email string          `db:"email"`
		CLV   decimal.Decimal `db:"clv"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return entity.CLVStats{}, err
	}

	if len(rows) == 0 {
		return entity.CLVStats{}, nil
	}

	clvs := make([]decimal.Decimal, len(rows))
	for i, r := range rows {
		clvs[i] = r.CLV
	}
	return calculateCLVStatsFromDecimals(clvs), nil
}

// calculateCLVStatsFromDecimals computes mean, median, and p90 without float64.
// decimal.Decimal.Float64() is avoided: it returns ok=false for most money values
// because binary floats rarely match decimals exactly.
func calculateCLVStatsFromDecimals(clvs []decimal.Decimal) entity.CLVStats {
	if len(clvs) == 0 {
		return entity.CLVStats{}
	}
	sort.Slice(clvs, func(i, j int) bool {
		return clvs[i].LessThan(clvs[j])
	})

	n := len(clvs)
	sum := decimal.Zero
	for _, v := range clvs {
		sum = sum.Add(v)
	}
	mean := sum.Div(decimal.NewFromInt(int64(n)))

	var median decimal.Decimal
	if n%2 == 1 {
		median = clvs[n/2]
	} else {
		median = clvs[n/2-1].Add(clvs[n/2]).Div(decimal.NewFromInt(2))
	}

	// p90 index: ceil(0.9*n) - 1, same as int(math.Ceil(float64(n)*0.9)) - 1
	p90Idx := (9*n + 9) / 10 - 1
	if p90Idx < 0 {
		p90Idx = 0
	}
	p90 := clvs[p90Idx]

	return entity.CLVStats{
		Mean:       mean.Round(2),
		Median:     median.Round(2),
		P90:        p90.Round(2),
		SampleSize: n,
	}
}

// GetCustomerSegmentation returns AOV-based customer segmentation (high/medium/low tiers).
func (s *Store) GetCustomerSegmentation(ctx context.Context, from, to time.Time) ([]entity.CustomerSegmentRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
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
),
order_revenue AS (
	SELECT 
		ob.id,
		(ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END)
			* (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
	FROM order_base ob
),
customer_aov AS (
	SELECT 
		b.email,
		COUNT(*) AS orders,
		SUM(orv.revenue_base) AS revenue,
		AVG(orv.revenue_base) AS aov
	FROM order_revenue orv
	JOIN buyer b ON orv.id = b.order_id
	GROUP BY b.email
),
ranked AS (
	SELECT 
		*,
		NTILE(5) OVER (ORDER BY aov) AS quintile
	FROM customer_aov
)
SELECT 
	email,
	orders,
	revenue,
	aov,
	CASE 
		WHEN quintile = 5 THEN 'high'
		WHEN quintile >= 2 THEN 'medium'
		ELSE 'low'
	END AS segment
FROM ranked
ORDER BY aov DESC
LIMIT 500
	`

	params := map[string]any{
		"from":         from,
		"to":           to,
		"baseCurrency": baseCurrency,
		"statusIds":    cache.OrderStatusIDsForNetRevenue(),
	}

	rows, err := storeutil.QueryListNamed[struct {
		Email   string          `db:"email"`
		Orders  int64           `db:"orders"`
		Revenue decimal.Decimal `db:"revenue"`
		AOV     decimal.Decimal `db:"aov"`
		Segment string          `db:"segment"`
	}](ctx, s.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("GetCustomerSegmentation: %w", err)
	}

	result := make([]entity.CustomerSegmentRow, len(rows))
	for i, r := range rows {
		result[i] = entity.CustomerSegmentRow{
			Email:         r.Email,
			OrderCount:    r.Orders,
			TotalRevenue:  r.Revenue,
			AvgOrderValue: r.AOV,
			Segment:       r.Segment,
		}
	}

	return result, nil
}

func (s *Store) getRevenueByPromo(ctx context.Context, from, to time.Time) ([]entity.PromoMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, ob.promo_id, ob.code, ob.discount,
				(ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, pc.id AS promo_id, pc.code, pc.discount,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
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
	rows, err := storeutil.QueryListNamed[struct {
		Code        string          `db:"code"`
		OrdersCount int             `db:"orders_count"`
		Revenue     decimal.Decimal `db:"revenue"`
		AvgDiscount decimal.Decimal `db:"avg_discount"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
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

// rfmLabel maps RFM scores (1-5) to standard 11-segment RFM labels.
// Checks are ordered from most specific to most general to avoid misclassification.
func rfmLabel(r, f, m int) string {
	// Champions: High R, F, M (recently purchased, frequently, high value)
	if r >= 4 && f >= 4 && m >= 4 {
		return "Champions"
	}
	// Can't Lose Them: High spenders who haven't been back (check before At Risk)
	if r == 1 && f >= 4 && m >= 4 {
		return "Can't Lose Them"
	}
	// Loyal: Good R, F, M (not Champions)
	if r >= 3 && f >= 3 && m >= 3 {
		return "Loyal"
	}
	// Potential Loyalists: Recent customers with moderate frequency/monetary
	if r >= 4 && f >= 2 && f <= 3 {
		return "Potential Loyalist"
	}
	// At Risk: Good spenders who haven't purchased recently
	if r <= 2 && f >= 3 && m >= 3 {
		return "At Risk"
	}
	// Lost: Lowest recency and engagement (including single-purchase customers who never returned)
	if r <= 2 && f == 1 {
		return "Lost"
	}
	// New Customers: Recent first-time buyers
	if r >= 3 && f == 1 {
		return "New Customers"
	}
	// Promising: Recent activity but low frequency/monetary
	if r >= 3 && f == 2 && m <= 2 {
		return "Promising"
	}
	// Need Attention: Mid-level customers slipping away
	if r == 3 && f >= 2 && m >= 2 {
		return "Need Attention"
	}
	// About to Sleep: Below average recency, frequency, monetary
	if r == 2 && f >= 2 && m >= 2 {
		return "About to Sleep"
	}
	// Hibernating: Low recency, frequency, but some past value
	if r <= 2 && f >= 2 && m <= 2 {
		return "Hibernating"
	}
	// Default fallback
	return "Other"
}

// GetRFMAnalysis returns RFM (Recency, Frequency, Monetary) customer segmentation.
func (s *Store) GetRFMAnalysis(ctx context.Context, from, to time.Time) ([]entity.RFMSegmentRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
WITH order_base AS (
	SELECT ob.id, b.email, co.placed,
		(ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
	FROM (
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
	) ob
	JOIN customer_order co ON ob.id = co.id
	JOIN buyer b ON co.id = b.order_id
),
customer_metrics AS (
	SELECT 
		ob.email,
		DATEDIFF(NOW(), MAX(ob.placed)) AS recency_days,
		COUNT(*) AS frequency,
		SUM(ob.revenue_base) AS monetary,
		MAX(ob.placed) AS last_purchase
	FROM order_base ob
	GROUP BY ob.email
),
scored AS (
	SELECT 
		*,
		(6 - NTILE(5) OVER (ORDER BY recency_days)) AS r_score,
		NTILE(5) OVER (ORDER BY frequency) AS f_score,
		NTILE(5) OVER (ORDER BY monetary) AS m_score
	FROM customer_metrics
)
SELECT email, r_score, f_score, m_score, last_purchase, frequency, monetary
FROM scored
ORDER BY r_score DESC, f_score DESC, m_score DESC
LIMIT 500
	`

	params := map[string]any{
		"from":         from,
		"to":           to,
		"baseCurrency": baseCurrency,
		"statusIds":    cache.OrderStatusIDsForNetRevenue(),
	}

	rows, err := storeutil.QueryListNamed[struct {
		Email        string          `db:"email"`
		RScore       int             `db:"r_score"`
		FScore       int             `db:"f_score"`
		MScore       int             `db:"m_score"`
		LastPurchase time.Time       `db:"last_purchase"`
		Frequency    int64           `db:"frequency"`
		Monetary     decimal.Decimal `db:"monetary"`
	}](ctx, s.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("GetRFMAnalysis: %w", err)
	}

	result := make([]entity.RFMSegmentRow, len(rows))
	for i, r := range rows {
		result[i] = entity.RFMSegmentRow{
			Email:          r.Email,
			RecencyScore:   r.RScore,
			FrequencyScore: r.FScore,
			MonetaryScore:  r.MScore,
			RFMLabel:       rfmLabel(r.RScore, r.FScore, r.MScore),
			LastPurchase:   r.LastPurchase,
			OrderCount:     r.Frequency,
			TotalSpent:     r.Monetary,
		}
	}

	return result, nil
}

func (s *Store) getOrdersByStatus(ctx context.Context, from, to time.Time) ([]entity.StatusCount, error) {
	query := `
		SELECT os.name, COUNT(*) AS cnt
		FROM customer_order co
		JOIN order_status os ON co.order_status_id = os.id
		WHERE co.placed >= :from AND co.placed < :to
		GROUP BY co.order_status_id, os.name
		ORDER BY cnt DESC
	`
	rows, err := storeutil.QueryListNamed[struct {
		Name  string `db:"name"`
		Count int    `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to})
	if err != nil {
		return nil, err
	}
	result := make([]entity.StatusCount, len(rows))
	for i, r := range rows {
		result[i] = entity.StatusCount{StatusName: r.Name, Count: r.Count}
	}
	return result, nil
}
