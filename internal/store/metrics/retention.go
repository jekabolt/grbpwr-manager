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

type retentionStore struct {
	*Store
}

func (s *Store) retention() *retentionStore {
	return &retentionStore{Store: s}
}

// GetCohortRetention computes monthly acquisition cohort retention matrix.
// Groups customers by the month of their first order, then tracks period retention:
// M1 = customers who ordered specifically in month 1, M2 = month 2, etc.
func (rs *retentionStore) GetCohortRetention(ctx context.Context, from, to time.Time) ([]entity.CohortRetentionRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())

	query := fmt.Sprintf(`
		WITH order_revenue_base AS (
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
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER('%s')
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER('%s')
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.order_status_id IN (%s)
					AND co.placed >= :from
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
			JOIN customer_order co ON ob.id = co.id
			JOIN buyer b ON co.id = b.order_id
		),
		customer_first_order AS (
			SELECT
				b.email,
				MIN(co.placed) AS first_order_date
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.order_status_id IN (%s)
			GROUP BY b.email
		),
		cohort_orders AS (
			SELECT
				cfo.email,
				DATE_FORMAT(cfo.first_order_date, '%%Y-%%m-01') AS cohort_month,
				TIMESTAMPDIFF(MONTH, cfo.first_order_date, orb.placed) AS months_since_first,
				orb.revenue_base
			FROM customer_first_order cfo
			JOIN order_revenue_base orb ON cfo.email = orb.email
			WHERE cfo.first_order_date >= :from AND cfo.first_order_date < :to
		)
		SELECT
			cohort_month,
			COUNT(DISTINCT email) AS cohort_size,
			COUNT(DISTINCT CASE WHEN months_since_first = 1 THEN email END) AS m1,
			COUNT(DISTINCT CASE WHEN months_since_first = 2 THEN email END) AS m2,
			COUNT(DISTINCT CASE WHEN months_since_first = 3 THEN email END) AS m3,
			COUNT(DISTINCT CASE WHEN months_since_first = 4 THEN email END) AS m4,
			COUNT(DISTINCT CASE WHEN months_since_first = 5 THEN email END) AS m5,
			COUNT(DISTINCT CASE WHEN months_since_first = 6 THEN email END) AS m6,
			COALESCE(SUM(CASE WHEN months_since_first = 1 THEN revenue_base END), 0) AS m1_revenue,
			COALESCE(SUM(CASE WHEN months_since_first = 2 THEN revenue_base END), 0) AS m2_revenue,
			COALESCE(SUM(CASE WHEN months_since_first = 3 THEN revenue_base END), 0) AS m3_revenue,
			COALESCE(SUM(CASE WHEN months_since_first = 4 THEN revenue_base END), 0) AS m4_revenue,
			COALESCE(SUM(CASE WHEN months_since_first = 5 THEN revenue_base END), 0) AS m5_revenue,
			COALESCE(SUM(CASE WHEN months_since_first = 6 THEN revenue_base END), 0) AS m6_revenue
		FROM cohort_orders
		GROUP BY cohort_month
		ORDER BY cohort_month
	`, baseCurrency, baseCurrency, statusIDs, statusIDs)

	type row struct {
		CohortMonth string          `db:"cohort_month"`
		CohortSize  int64           `db:"cohort_size"`
		M1          int64           `db:"m1"`
		M2          int64           `db:"m2"`
		M3          int64           `db:"m3"`
		M4          int64           `db:"m4"`
		M5          int64           `db:"m5"`
		M6          int64           `db:"m6"`
		M1Revenue   decimal.Decimal `db:"m1_revenue"`
		M2Revenue   decimal.Decimal `db:"m2_revenue"`
		M3Revenue   decimal.Decimal `db:"m3_revenue"`
		M4Revenue   decimal.Decimal `db:"m4_revenue"`
		M5Revenue   decimal.Decimal `db:"m5_revenue"`
		M6Revenue   decimal.Decimal `db:"m6_revenue"`
	}
	params := map[string]any{"from": from, "to": to}
	rows, err := storeutil.QueryListNamed[row](ctx, rs.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("get cohort retention: %w", err)
	}
	result := make([]entity.CohortRetentionRow, 0, len(rows))
	for _, r := range rows {
		month, err := storeutil.ParseDateStr(r.CohortMonth)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.CohortRetentionRow{
			CohortMonth: month,
			CohortSize:  r.CohortSize,
			M1:          r.M1,
			M2:          r.M2,
			M3:          r.M3,
			M4:          r.M4,
			M5:          r.M5,
			M6:          r.M6,
			M1Revenue:   r.M1Revenue,
			M2Revenue:   r.M2Revenue,
			M3Revenue:   r.M3Revenue,
			M4Revenue:   r.M4Revenue,
			M5Revenue:   r.M5Revenue,
			M6Revenue:   r.M6Revenue,
		})
	}
	return result, nil
}

// GetOrderSequenceMetrics returns AOV and timing by order sequence number.
// Revenue is normalized to base currency via product_price join.
func (rs *retentionStore) GetOrderSequenceMetrics(ctx context.Context, from, to time.Time) ([]entity.OrderSequenceMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())

	query := fmt.Sprintf(`
		WITH order_revenue_base AS (
			SELECT
				co.id,
				co.placed,
				(COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) * (100 - COALESCE(MAX(pc.discount), 0)) / 100.0
					+ CASE WHEN COALESCE(MAX(pc.free_shipping), 0) THEN 0 ELSE COALESCE(MAX(scp.price), 0) END)
					* (co.total_price - COALESCE(co.refunded_amount, 0)) / NULLIF(co.total_price, 0) AS revenue_base
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.order_status_id IN (%s)
				AND co.placed >= :from AND co.placed < :to
			GROUP BY co.id, co.placed, co.total_price, co.refunded_amount
		),
		numbered_orders AS (
			SELECT
				b.email,
				orb.revenue_base,
				orb.placed,
				ROW_NUMBER() OVER (PARTITION BY b.email ORDER BY orb.placed) AS order_num,
				LAG(orb.placed) OVER (PARTITION BY b.email ORDER BY orb.placed) AS prev_placed
			FROM order_revenue_base orb
			JOIN buyer b ON orb.id = b.order_id
		)
		SELECT
			order_num,
			COUNT(*) AS order_count,
			AVG(revenue_base) AS avg_order_value,
			AVG(CASE WHEN prev_placed IS NOT NULL
				THEN DATEDIFF(placed, prev_placed)
			END) AS avg_days_since_prev
		FROM numbered_orders
		WHERE order_num <= 5
		GROUP BY order_num
		ORDER BY order_num
	`, statusIDs)

	type row struct {
		OrderNumber      int             `db:"order_num"`
		OrderCount       int64           `db:"order_count"`
		AvgOrderValue    decimal.Decimal `db:"avg_order_value"`
		AvgDaysSincePrev *float64        `db:"avg_days_since_prev"`
	}
	params := map[string]any{"baseCurrency": baseCurrency, "from": from, "to": to}
	rows, err := storeutil.QueryListNamed[row](ctx, rs.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("get order sequence metrics: %w", err)
	}
	result := make([]entity.OrderSequenceMetric, 0, len(rows))
	for _, r := range rows {
		m := entity.OrderSequenceMetric{
			OrderNumber: r.OrderNumber, OrderCount: r.OrderCount, AvgOrderValue: r.AvgOrderValue,
		}
		if r.AvgDaysSincePrev != nil {
			m.AvgDaysSincePrev = *r.AvgDaysSincePrev
		}
		result = append(result, m)
	}
	return result, nil
}

// GetEntryProducts returns top products bought by first-time customers.
// Revenue is normalized to base currency via product_price join.
func (rs *retentionStore) GetEntryProducts(ctx context.Context, from, to time.Time, limit int) ([]entity.EntryProductMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())

	query := fmt.Sprintf(`
		WITH first_orders AS (
			SELECT
				b.email,
				MIN(co.id) AS first_order_id
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.order_status_id IN (%s)
			GROUP BY b.email
			HAVING MIN(co.placed) >= :from AND MIN(co.placed) < :to
		)
		SELECT
			oi.product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1),
				p.brand
			) AS product_name,
			COUNT(*) AS purchase_count,
			SUM(pp_base.price * oi.quantity) AS total_revenue
		FROM first_orders fo
		JOIN order_item oi ON oi.order_id = fo.first_order_id
		JOIN product p ON p.id = oi.product_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		WHERE p.deleted_at IS NULL AND (p.hidden IS NULL OR p.hidden = 0)
		GROUP BY oi.product_id, product_name
		ORDER BY purchase_count DESC
		LIMIT :limit
	`, statusIDs)

	result, err := storeutil.QueryListNamed[entity.EntryProductMetric](ctx, rs.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get entry products: %w", err)
	}
	return result, nil
}

// GetRevenuePareto returns cumulative revenue % by ranked products.
// Revenue is normalized to base currency via product_price join.
func (rs *retentionStore) GetRevenuePareto(ctx context.Context, from, to time.Time, limit int) ([]entity.RevenueParetoRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())

	query := fmt.Sprintf(`
		WITH product_revenue AS (
			SELECT
				oi.product_id,
				COALESCE(
					(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1),
					p.brand
				) AS product_name,
				SUM(pp_base.price * oi.quantity) AS revenue
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			JOIN product p ON p.id = oi.product_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			WHERE co.order_status_id IN (%s)
				AND co.placed >= :from AND co.placed < :to
				AND p.deleted_at IS NULL
				AND (p.hidden IS NULL OR p.hidden = 0)
			GROUP BY oi.product_id, product_name
		),
		ranked AS (
			SELECT
				product_id,
				product_name,
				revenue,
				ROW_NUMBER() OVER (ORDER BY revenue DESC) AS rank_num
			FROM product_revenue
		),
		total AS (
			SELECT SUM(revenue) AS total_revenue FROM product_revenue
		)
		SELECT
			r.rank_num,
			r.product_id,
			r.product_name,
			r.revenue,
			SUM(r.revenue) OVER (ORDER BY r.rank_num ROWS UNBOUNDED PRECEDING) / t.total_revenue * 100 AS cumulative_pct
		FROM ranked r
		CROSS JOIN total t
		ORDER BY r.rank_num
		LIMIT :limit
	`, statusIDs)

	result, err := storeutil.QueryListNamed[entity.RevenueParetoRow](ctx, rs.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get revenue pareto: %w", err)
	}
	return result, nil
}

// GetCustomerSpendingCurve returns avg cumulative spend per customer by order sequence.
// Revenue is normalized to base currency via product_price join.
func (rs *retentionStore) GetCustomerSpendingCurve(ctx context.Context, from, to time.Time) ([]entity.SpendingCurvePoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())

	query := fmt.Sprintf(`
		WITH order_revenue_base AS (
			SELECT
				co.id,
				co.placed,
				(COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) * (100 - COALESCE(MAX(pc.discount), 0)) / 100.0
					+ CASE WHEN COALESCE(MAX(pc.free_shipping), 0) THEN 0 ELSE COALESCE(MAX(scp.price), 0) END)
					* (co.total_price - COALESCE(co.refunded_amount, 0)) / NULLIF(co.total_price, 0) AS revenue_base
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.order_status_id IN (%s)
				AND co.placed >= :from AND co.placed < :to
			GROUP BY co.id, co.placed, co.total_price, co.refunded_amount
		),
		numbered_orders AS (
			SELECT
				b.email,
				orb.revenue_base,
				ROW_NUMBER() OVER (PARTITION BY b.email ORDER BY orb.placed) AS order_num
			FROM order_revenue_base orb
			JOIN buyer b ON orb.id = b.order_id
		),
		cumulative AS (
			SELECT
				email,
				order_num,
				SUM(revenue_base) OVER (PARTITION BY email ORDER BY order_num) AS cumulative_spend
			FROM numbered_orders
		)
		SELECT
			order_num,
			AVG(cumulative_spend) AS avg_cumulative_spend,
			COUNT(DISTINCT email) AS customer_count
		FROM cumulative
		WHERE order_num <= 10
		GROUP BY order_num
		ORDER BY order_num
	`, statusIDs)

	result, err := storeutil.QueryListNamed[entity.SpendingCurvePoint](ctx, rs.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
	})
	if err != nil {
		return nil, fmt.Errorf("get customer spending curve: %w", err)
	}
	return result, nil
}

// GetCategoryLoyalty returns a cross-tab of first vs second purchase categories for repeat customers.
func (rs *retentionStore) GetCategoryLoyalty(ctx context.Context, from, to time.Time) ([]entity.CategoryLoyaltyRow, error) {
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())

	query := fmt.Sprintf(`
		WITH ordered AS (
			SELECT
				b.email,
				co.id AS order_id,
				co.placed,
				ROW_NUMBER() OVER (PARTITION BY b.email ORDER BY co.placed) AS order_num
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.order_status_id IN (%s)
				AND co.placed >= :from AND co.placed < :to
		),
		first_cat AS (
			SELECT o.email, c.name AS category_name
			FROM ordered o
			JOIN order_item oi ON oi.order_id = o.order_id
			JOIN product p ON p.id = oi.product_id
			JOIN category c ON c.id = COALESCE(p.sub_category_id, p.top_category_id)
			WHERE o.order_num = 1
		),
		second_cat AS (
			SELECT o.email, c.name AS category_name
			FROM ordered o
			JOIN order_item oi ON oi.order_id = o.order_id
			JOIN product p ON p.id = oi.product_id
			JOIN category c ON c.id = COALESCE(p.sub_category_id, p.top_category_id)
			WHERE o.order_num = 2
		)
		SELECT
			fc.category_name AS first_category,
			sc.category_name AS second_category,
			COUNT(*) AS customer_count
		FROM first_cat fc
		JOIN second_cat sc ON fc.email = sc.email
		GROUP BY fc.category_name, sc.category_name
		ORDER BY customer_count DESC
	`, statusIDs)

	result, err := storeutil.QueryListNamed[entity.CategoryLoyaltyRow](ctx, rs.DB, query, map[string]any{
		"from": from,
		"to":   to,
	})
	if err != nil {
		return nil, fmt.Errorf("get category loyalty: %w", err)
	}
	return result, nil
}
