package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// returnByProductRawRow is the raw DB row before aggregation.
type returnByProductRawRow struct {
	ProductID     int     `db:"product_id"`
	ProductName   string  `db:"product_name"`
	RefundReason  string  `db:"refund_reason"`
	RefundedQty   int64   `db:"refunded_qty"`
	TotalSold     int64   `db:"total_sold"`
	ReturnRatePct float64 `db:"return_rate_pct"`
}

type analyticsStore struct {
	*MYSQLStore
}

// Analytics returns an object implementing Analytics interface
func (ms *MYSQLStore) Analytics() dependency.Analytics {
	return &analyticsStore{
		MYSQLStore: ms,
	}
}

// GetSlowMovers returns bottom-performing products ordered by revenue (ascending).
// Products with zero sales in the period appear first with revenue=0.
func (as *analyticsStore) GetSlowMovers(ctx context.Context, from, to time.Time, limit int) ([]entity.SlowMoverRow, error) {
	statusIDs := joinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH product_sales AS (
			SELECT
				oi.product_id,
				SUM(oi.product_price * oi.quantity) AS revenue,
				SUM(oi.quantity)                     AS units_sold,
				MAX(co.placed)                       AS last_sale_date
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id
		)
		SELECT
			p.id AS product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1),
				p.brand
			) AS product_name,
			COALESCE(ps.revenue, 0) AS revenue,
			COALESCE(ps.units_sold, 0) AS units_sold,
			DATEDIFF(NOW(), p.created_at) AS days_in_stock,
			ps.last_sale_date
		FROM product p
		LEFT JOIN product_sales ps ON ps.product_id = p.id
		WHERE p.hidden = 0
		ORDER BY revenue ASC, days_in_stock DESC
		LIMIT :limit
	`, statusIDs)

	result, err := QueryListNamed[entity.SlowMoverRow](ctx, as.DB(), query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get slow movers: %w", err)
	}
	return result, nil
}

// normalizeRefundReason maps DB refund_reason text to chart keys.
func normalizeRefundReason(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return "other"
	}
	switch {
	case strings.Contains(lower, "size") && !strings.Contains(lower, "quality"):
		return "wrong_size"
	case strings.Contains(lower, "defective") || strings.Contains(lower, "damaged"):
		return "defective"
	case strings.Contains(lower, "quality") || strings.Contains(lower, "wrong item") || strings.Contains(lower, "not as described"):
		return "not_as_described"
	case strings.Contains(lower, "changed") || strings.Contains(lower, "mistake") || strings.Contains(lower, "ordered by mistake"):
		return "changed_mind"
	default:
		return "other"
	}
}

// GetReturnByProduct returns return/refund rate per product with breakdown by refund reason.
// Uses refunded_order_item for accurate refund quantities (not order_item).
func (as *analyticsStore) GetReturnByProduct(ctx context.Context, from, to time.Time, limit int) ([]entity.ReturnByProductRow, error) {
	statusIDs := joinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH sold AS (
			SELECT
				oi.product_id,
				SUM(oi.quantity) AS total_sold
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%[1]s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id
		),
		returned_by_reason AS (
			SELECT
				oi.product_id,
				COALESCE(NULLIF(TRIM(co.refund_reason), ''), 'Not specified') AS refund_reason,
				SUM(roi.quantity_refunded) AS refunded_qty
			FROM refunded_order_item roi
			JOIN order_item oi ON roi.order_item_id = oi.id
			JOIN customer_order co ON roi.order_id = co.id
			WHERE co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id, COALESCE(NULLIF(TRIM(co.refund_reason), ''), 'Not specified')
		)
		SELECT
			p.id AS product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1),
				p.brand
			) AS product_name,
			COALESCE(r.refund_reason, '') AS refund_reason,
			COALESCE(r.refunded_qty, 0) AS refunded_qty,
			s.total_sold,
			CASE WHEN s.total_sold > 0 AND COALESCE(r.refunded_qty, 0) > 0
				THEN r.refunded_qty / s.total_sold * 100
				ELSE 0
			END AS return_rate_pct
		FROM sold s
		JOIN product p ON p.id = s.product_id
		LEFT JOIN returned_by_reason r ON r.product_id = s.product_id
		WHERE p.deleted_at IS NULL
		ORDER BY s.total_sold DESC, p.id, r.refund_reason
	`, statusIDs)

	rawRows, err := QueryListNamed[returnByProductRawRow](ctx, as.DB(), query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
	})
	if err != nil {
		return nil, fmt.Errorf("get return by product: %w", err)
	}

	// Aggregate by product: group raw rows, normalize reasons, build Reasons map
	byProduct := make(map[int]*entity.ReturnByProductRow)
	for _, r := range rawRows {
		row, ok := byProduct[r.ProductID]
		if !ok {
			row = &entity.ReturnByProductRow{
				ProductName:     r.ProductName,
				TotalReturnRate: 0,
				Reasons:         make(map[string]float64),
			}
			byProduct[r.ProductID] = row
		}
		if r.ReturnRatePct > 0 {
			key := normalizeRefundReason(r.RefundReason)
			row.Reasons[key] += r.ReturnRatePct
			row.TotalReturnRate += r.ReturnRatePct
		}
	}

	// Build result sorted by total_return_rate descending, limit
	result := make([]entity.ReturnByProductRow, 0, len(byProduct))
	for _, row := range byProduct {
		result = append(result, *row)
	}
	sortReturnByProductDesc(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func sortReturnByProductDesc(rows []entity.ReturnByProductRow) {
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].TotalReturnRate > rows[j].TotalReturnRate
	})
}

// GetReturnBySize returns return/refund rate per size across all products.
func (as *analyticsStore) GetReturnBySize(ctx context.Context, from, to time.Time) ([]entity.ReturnBySizeRow, error) {
	statusIDs := joinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH sold AS (
			SELECT
				oi.size_id,
				SUM(oi.quantity) AS total_sold
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.size_id
		),
		returned AS (
			SELECT
				oi.size_id,
				SUM(oi.quantity) AS returned_qty
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (
				SELECT os.id FROM order_status os WHERE os.name IN ('refunded', 'partially_refunded')
			)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.size_id
		)
		SELECT
			s.size_id,
			sz.name AS size_name,
			s.total_sold,
			COALESCE(r.returned_qty, 0) AS total_returned,
			CASE WHEN s.total_sold > 0
				THEN COALESCE(r.returned_qty, 0) / s.total_sold * 100
				ELSE 0
			END AS return_rate
		FROM sold s
		JOIN size sz ON sz.id = s.size_id
		LEFT JOIN returned r ON r.size_id = s.size_id
		ORDER BY return_rate DESC
	`, statusIDs)

	result, err := QueryListNamed[entity.ReturnBySizeRow](ctx, as.DB(), query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
	})
	if err != nil {
		return nil, fmt.Errorf("get return by size: %w", err)
	}
	return result, nil
}

// GetSizeAnalytics returns units sold and revenue by product+size with % of product total.
func (as *analyticsStore) GetSizeAnalytics(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeAnalyticsRow, error) {
	statusIDs := joinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH size_sales AS (
			SELECT
				oi.product_id,
				oi.size_id,
				SUM(oi.quantity) AS units_sold,
				SUM(oi.product_price * oi.quantity) AS revenue
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id, oi.size_id
		),
		product_totals AS (
			SELECT product_id, SUM(units_sold) AS total_units
			FROM size_sales
			GROUP BY product_id
		)
		SELECT
			ss.product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1),
				p.brand
			) AS product_name,
			ss.size_id,
			s.name AS size_name,
			ss.units_sold,
			ss.revenue,
			CASE WHEN pt.total_units > 0
				THEN ss.units_sold / pt.total_units * 100
				ELSE 0
			END AS pct_of_product
		FROM size_sales ss
		JOIN product p ON p.id = ss.product_id
		JOIN size s ON s.id = ss.size_id
		JOIN product_totals pt ON pt.product_id = ss.product_id
		ORDER BY ss.revenue DESC
		LIMIT :limit
	`, statusIDs)

	result, err := QueryListNamed[entity.SizeAnalyticsRow](ctx, as.DB(), query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get size analytics: %w", err)
	}
	return result, nil
}

// GetDeadStock returns SKUs with stock >0 that had zero sales for >180 days.
// Evaluates as of :to; last_sale considers orders in [from, to] with fallback to orders before :from.
func (as *analyticsStore) GetDeadStock(ctx context.Context, from, to time.Time, limit int) ([]entity.DeadStockRow, error) {
	statusIDs := joinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH last_sales_in_period AS (
			SELECT oi.product_id, oi.size_id, MAX(co.placed) AS last_sale_date
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%[1]s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id, oi.size_id
		),
		last_sales_before AS (
			SELECT oi.product_id, oi.size_id, MAX(co.placed) AS last_sale_date
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%[1]s)
				AND co.currency = :baseCurrency
				AND co.placed < :from
			GROUP BY oi.product_id, oi.size_id
		),
		last_sales AS (
			SELECT
				COALESCE(p.product_id, b.product_id) AS product_id,
				COALESCE(p.size_id, b.size_id) AS size_id,
				COALESCE(p.last_sale_date, b.last_sale_date) AS last_sale_date
			FROM last_sales_in_period p
			LEFT JOIN last_sales_before b ON p.product_id = b.product_id AND p.size_id = b.size_id
			UNION
			SELECT b.product_id, b.size_id, b.last_sale_date
			FROM last_sales_before b
			LEFT JOIN last_sales_in_period p ON p.product_id = b.product_id AND p.size_id = b.size_id
			WHERE p.product_id IS NULL
		)
		SELECT
			ps.product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1),
				p.brand
			) AS product_name,
			ps.size_id,
			s.name AS size_name,
			ps.quantity,
			DATEDIFF(:to, COALESCE(ls.last_sale_date, p.created_at)) AS days_without_sale,
			ps.quantity * COALESCE(pp.price, 0) AS stock_value
		FROM product_size ps
		JOIN product p ON p.id = ps.product_id
		LEFT JOIN product_price pp ON p.id = pp.product_id AND UPPER(pp.currency) = UPPER(:baseCurrency)
		JOIN size s ON s.id = ps.size_id
		LEFT JOIN last_sales ls ON ls.product_id = ps.product_id AND ls.size_id = ps.size_id
		WHERE ps.quantity > 0
			AND DATEDIFF(:to, COALESCE(ls.last_sale_date, p.created_at)) > 180
		ORDER BY days_without_sale DESC
		LIMIT :limit
	`, statusIDs)

	result, err := QueryListNamed[entity.DeadStockRow](ctx, as.DB(), query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get dead stock: %w", err)
	}
	return result, nil
}

// GetProductTrend compares revenue for each product in the current period
// vs the same-length previous period. Sorted by change_pct ascending (worst decliners first).
func (as *analyticsStore) GetProductTrend(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductTrendRow, error) {
	statusIDs := joinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	dur := to.Sub(from)
	prevFrom := from.Add(-dur)
	prevTo := from

	query := fmt.Sprintf(`
		WITH current_period AS (
			SELECT
				oi.product_id,
				SUM(oi.product_price * oi.quantity) AS revenue,
				SUM(oi.quantity) AS units
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%[1]s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id
		),
		previous_period AS (
			SELECT
				oi.product_id,
				SUM(oi.product_price * oi.quantity) AS revenue,
				SUM(oi.quantity) AS units
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%[1]s)
				AND co.currency = :baseCurrency
				AND co.placed >= :prevFrom AND co.placed < :prevTo
			GROUP BY oi.product_id
		)
		SELECT
			COALESCE(c.product_id, pr.product_id) AS product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt
				 WHERE pt.product_id = COALESCE(c.product_id, pr.product_id)
				 ORDER BY pt.language_id LIMIT 1),
				p.brand
			) AS product_name,
			COALESCE(c.revenue, 0) AS current_revenue,
			COALESCE(pr.revenue, 0) AS previous_revenue,
			CASE
				WHEN COALESCE(pr.revenue, 0) > 0
				THEN (COALESCE(c.revenue, 0) - pr.revenue) / pr.revenue * 100
				WHEN COALESCE(c.revenue, 0) > 0 THEN 100
				ELSE 0
			END AS change_pct,
			COALESCE(c.units, 0) AS current_units,
			COALESCE(pr.units, 0) AS previous_units
		FROM current_period c
		LEFT JOIN previous_period pr ON pr.product_id = c.product_id
		JOIN product p ON p.id = COALESCE(c.product_id, pr.product_id)
		UNION ALL
		SELECT
			pr.product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt WHERE pt.product_id = pr.product_id ORDER BY pt.language_id LIMIT 1),
				p.brand
			),
			0, pr.revenue,
			-100, 0, pr.units
		FROM previous_period pr
		LEFT JOIN current_period c ON c.product_id = pr.product_id
		JOIN product p ON p.id = pr.product_id
		WHERE c.product_id IS NULL
		ORDER BY change_pct ASC
		LIMIT :limit
	`, statusIDs)

	result, err := QueryListNamed[entity.ProductTrendRow](ctx, as.DB(), query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"prevFrom":     prevFrom,
		"prevTo":       prevTo,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get product trend: %w", err)
	}
	return result, nil
}
