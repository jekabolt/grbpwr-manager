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

type inventoryStore struct {
	*Store
}

func (s *Store) inventory() *inventoryStore {
	return &inventoryStore{Store: s}
}

// GetInventoryHealth returns current stock levels with average daily sales rate
// and estimated days-on-hand for each product/size combination.
func (is *inventoryStore) GetInventoryHealth(ctx context.Context, from, to time.Time, limit int) ([]entity.InventoryHealthRow, error) {
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH daily_sales AS (
			SELECT
				oi.product_id,
				oi.size_id,
				SUM(oi.quantity) AS total_sold
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id, oi.size_id
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
			COALESCE(ds.total_sold / GREATEST(DATEDIFF(:to, :from), 1), 0) AS avg_daily_sales,
			CASE
				WHEN COALESCE(ds.total_sold, 0) = 0 THEN 99999
				ELSE ps.quantity / (ds.total_sold / GREATEST(DATEDIFF(:to, :from), 1))
			END AS days_on_hand,
			it.reorder_point,
			it.target_days_cover,
			it.lead_time_days
		FROM product_size ps
		JOIN product p ON p.id = ps.product_id
		JOIN size s ON s.id = ps.size_id
		LEFT JOIN daily_sales ds ON ds.product_id = ps.product_id AND ds.size_id = ps.size_id
		LEFT JOIN inventory_target it ON it.product_id = ps.product_id AND it.size_id = ps.size_id
		WHERE ps.quantity > 0
			AND p.deleted_at IS NULL
			AND (p.hidden IS NULL OR p.hidden = 0)
		ORDER BY days_on_hand ASC
		LIMIT :limit
	`, statusIDs)

	result, err := storeutil.QueryListNamed[entity.InventoryHealthRow](ctx, is.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get inventory health: %w", err)
	}
	for i := range result {
		applyReorderDecision(&result[i])
	}
	return result, nil
}

// applyReorderDecision derives HasTarget and NeedsReorder from the SKU's stock, sales
// velocity and its optional targets. A SKU needs reordering when it is at/below its reorder
// point, or — while it is actually selling — when its days of cover would run out within the
// supplier lead time or fall short of the target cover. The no-sales sentinel (days_on_hand
// = 99999) is excluded from the cover checks, so a dead SKU only trips via the reorder point.
func applyReorderDecision(r *entity.InventoryHealthRow) {
	// Always set from the sales velocity so the client never has to detect the days_on_hand
	// sentinel to know whether the SKU moved in the window.
	r.IsSelling = r.AvgDailySales > 0
	r.HasTarget = r.ReorderPoint.Valid || r.TargetDaysCover.Valid || r.LeadTimeDays.Valid
	if !r.HasTarget {
		return
	}
	if r.ReorderPoint.Valid && int64(r.Quantity) <= r.ReorderPoint.Int64 {
		r.NeedsReorder = true
	}
	if r.AvgDailySales > 0 {
		if r.LeadTimeDays.Valid && r.DaysOnHand <= float64(r.LeadTimeDays.Int64) {
			r.NeedsReorder = true
		}
		if r.TargetDaysCover.Valid && r.DaysOnHand < float64(r.TargetDaysCover.Int64) {
			r.NeedsReorder = true
		}
	}
}

// UpsertInventoryTargets sets per-SKU reorder targets (insert or replace by product+size).
// A NULL field clears that threshold. Empty input is a no-op.
func (is *inventoryStore) UpsertInventoryTargets(ctx context.Context, targets []entity.InventoryTargetInsert) error {
	for _, t := range targets {
		if err := storeutil.ExecNamed(ctx, is.DB, `
			INSERT INTO inventory_target (product_id, size_id, reorder_point, target_days_cover, lead_time_days)
			VALUES (:product_id, :size_id, :reorder_point, :target_days_cover, :lead_time_days)
			ON DUPLICATE KEY UPDATE
				reorder_point = VALUES(reorder_point),
				target_days_cover = VALUES(target_days_cover),
				lead_time_days = VALUES(lead_time_days)`,
			map[string]any{
				"product_id":        t.ProductID,
				"size_id":           t.SizeID,
				"reorder_point":     t.ReorderPoint,
				"target_days_cover": t.TargetDaysCover,
				"lead_time_days":    t.LeadTimeDays,
			}); err != nil {
			return fmt.Errorf("upsert inventory target (product %d size %d): %w", t.ProductID, t.SizeID, err)
		}
	}
	return nil
}

// GetSellThroughByDrop rolls each release/drop cohort (product.collection) into
// decision-grade totals: distinct products, lifetime net units sold, current on-hand units,
// sell-through % (sold / (sold + remaining)), and lifetime net revenue. Sell-through is
// inherently cumulative, so it is computed lifetime (current state) — the from/to window is
// intentionally NOT applied to the sold/revenue aggregation. Products with an empty
// collection (untagged) are excluded. Sorted by revenue desc, limited.
func (is *inventoryStore) GetSellThroughByDrop(ctx context.Context, from, to time.Time, limit int) ([]entity.SellThroughByDropRow, error) {
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH sold AS (
			SELECT
				oi.product_id,
				SUM(oi.quantity) AS units_sold,
				COALESCE(SUM(pp_base.price * oi.quantity), 0) AS revenue
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			WHERE co.order_status_id IN (%s)
			GROUP BY oi.product_id
		),
		stock AS (
			SELECT product_id, SUM(quantity) AS units_remaining
			FROM product_size
			GROUP BY product_id
		)
		SELECT
			p.collection,
			COUNT(DISTINCT p.id) AS product_count,
			COALESCE(SUM(s.units_sold), 0) AS units_sold,
			COALESCE(SUM(st.units_remaining), 0) AS units_remaining,
			COALESCE(SUM(s.units_sold), 0) + COALESCE(SUM(st.units_remaining), 0) AS units_bought,
			COALESCE(SUM(s.revenue), 0) AS revenue,
			COALESCE(SUM(CASE WHEN p.cost_price IS NOT NULL THEN p.cost_price * COALESCE(s.units_sold, 0) ELSE 0 END), 0) AS revenue_cost,
			COALESCE(SUM(CASE WHEN p.cost_price IS NOT NULL THEN COALESCE(s.revenue, 0) ELSE 0 END), 0) AS costed_revenue,
			CASE
				WHEN COALESCE(SUM(s.units_sold), 0) + COALESCE(SUM(st.units_remaining), 0) > 0
				THEN COALESCE(SUM(s.units_sold), 0) * 100.0 / (COALESCE(SUM(s.units_sold), 0) + COALESCE(SUM(st.units_remaining), 0))
				ELSE 0
			END AS sell_through_pct
		FROM product p
		LEFT JOIN sold s ON s.product_id = p.id
		LEFT JOIN stock st ON st.product_id = p.id
		WHERE p.deleted_at IS NULL
			AND p.collection IS NOT NULL AND p.collection <> ''
		GROUP BY p.collection
		ORDER BY revenue DESC
		LIMIT :limit
	`, statusIDs)

	result, err := storeutil.QueryListNamed[entity.SellThroughByDropRow](ctx, is.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get sell-through by drop: %w", err)
	}
	if len(result) == 0 {
		return result, nil
	}

	// days_to_50pct needs a per-drop daily sales timeline. Fetch it once (lifetime, same
	// net-revenue universe as the aggregate above) and compute the crossing in Go.
	timeline, err := is.sellThroughTimeline(ctx, statusIDs)
	if err != nil {
		return nil, err
	}

	hundred := decimal.NewFromInt(100)
	for i := range result {
		r := &result[i]
		// Margin over the costed subset. HasCost=false ⇒ no costed sales, so N/A not 0.
		r.HasCost = r.CostedRevenue.IsPositive()
		if r.HasCost {
			r.GrossMargin = r.CostedRevenue.Sub(r.RevenueCost)
			pct, _ := r.GrossMargin.Div(r.CostedRevenue).Mul(hundred).Float64()
			r.GrossMarginPct = pct
		}
		// days_to_50pct is only defined once the drop has actually reached 50% sell-through.
		if r.SellThroughPct >= 50 {
			r.DaysTo50Pct = daysToSellThrough(timeline[r.Collection], r.UnitsBought, 50)
		}
	}
	return result, nil
}

// dropSalePoint is one day's net units sold for a drop, used to find the 50%-sell-through date.
type dropSalePoint struct {
	Date  time.Time `db:"d"`
	Units int64     `db:"units"`
}

// sellThroughTimeline returns, per collection, the daily net units sold over all time (same
// net-revenue statuses as the aggregate), ascending by date — the input to daysToSellThrough.
func (is *inventoryStore) sellThroughTimeline(ctx context.Context, statusIDs string) (map[string][]dropSalePoint, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Collection string    `db:"collection"`
		Date       time.Time `db:"d"`
		Units      int64     `db:"units"`
	}](ctx, is.DB, fmt.Sprintf(`
		SELECT p.collection AS collection, DATE(co.placed) AS d, SUM(oi.quantity) AS units
		FROM order_item oi
		JOIN customer_order co ON oi.order_id = co.id
		JOIN product p ON oi.product_id = p.id
		WHERE co.order_status_id IN (%s)
			AND p.deleted_at IS NULL
			AND p.collection IS NOT NULL AND p.collection <> ''
		GROUP BY p.collection, DATE(co.placed)
		ORDER BY p.collection, d
	`, statusIDs), map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("sell-through timeline: %w", err)
	}
	m := make(map[string][]dropSalePoint)
	for _, r := range rows {
		m[r.Collection] = append(m[r.Collection], dropSalePoint{Date: r.Date, Units: r.Units})
	}
	return m, nil
}

// daysToSellThrough returns the whole days from the first sale to the day cumulative units sold
// first reached targetPct percent of initialUnits. It is invalid (null) when there are no
// sales, the inputs are degenerate, or the target is never reached (the drop hasn't sold that
// share yet). points must be sorted ascending by date.
func daysToSellThrough(points []dropSalePoint, initialUnits int64, targetPct float64) sql.NullInt64 {
	if len(points) == 0 || initialUnits <= 0 || targetPct <= 0 {
		return sql.NullInt64{}
	}
	threshold := targetPct / 100 * float64(initialUnits)
	first := points[0].Date
	var cumulative int64
	for _, pt := range points {
		cumulative += pt.Units
		if float64(cumulative) >= threshold {
			days := int64(pt.Date.Sub(first).Hours() / 24)
			if days < 0 {
				days = 0
			}
			return sql.NullInt64{Int64: days, Valid: true}
		}
	}
	return sql.NullInt64{}
}

// GetSizeRunEfficiency returns the percentage of sizes that have any sales activity
// for each product. Sell-through is computed from initial stock vs current stock.
func (is *inventoryStore) GetSizeRunEfficiency(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeRunEfficiencyRow, error) {
	statusIDs := storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue())
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	query := fmt.Sprintf(`
		WITH sold_qty AS (
			SELECT
				oi.product_id,
				oi.size_id,
				SUM(oi.quantity) AS total_sold
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
			WHERE co.order_status_id IN (%s)
				AND co.currency = :baseCurrency
				AND co.placed >= :from AND co.placed < :to
			GROUP BY oi.product_id, oi.size_id
		),
		size_analysis AS (
			SELECT
				ps.product_id,
				ps.size_id,
				(ps.quantity + COALESCE(sq.total_sold, 0)) AS initial_qty,
				COALESCE(sq.total_sold, 0) AS sold,
				CASE
					WHEN (ps.quantity + COALESCE(sq.total_sold, 0)) > 0
					THEN COALESCE(sq.total_sold, 0) * 100 / (ps.quantity + COALESCE(sq.total_sold, 0))
					ELSE 0
				END AS sell_through_pct
			FROM product_size ps
			LEFT JOIN sold_qty sq ON sq.product_id = ps.product_id AND sq.size_id = ps.size_id
		)
		SELECT
			sa.product_id,
			COALESCE(
				(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1),
				p.brand
			) AS product_name,
			COUNT(*) AS total_sizes,
			SUM(CASE WHEN sa.sell_through_pct > 0 THEN 1 ELSE 0 END) AS sold_through_sizes,
			SUM(CASE WHEN sa.sell_through_pct > 0 THEN 1 ELSE 0 END) * 100 / COUNT(*) AS efficiency_pct,
			COALESCE(SUM(sa.initial_qty), 0) AS units_bought,
			COALESCE(SUM(sa.sold), 0) AS units_sold,
			CASE
				WHEN COALESCE(SUM(sa.initial_qty), 0) > 0
				THEN SUM(sa.sold) * 100.0 / SUM(sa.initial_qty)
				ELSE 0
			END AS sell_through_pct
		FROM size_analysis sa
		JOIN product p ON p.id = sa.product_id
		WHERE sa.initial_qty > 0
			AND p.deleted_at IS NULL
			AND (p.hidden IS NULL OR p.hidden = 0)
		GROUP BY sa.product_id, product_name
		ORDER BY efficiency_pct DESC
		LIMIT :limit
	`, statusIDs)

	result, err := storeutil.QueryListNamed[entity.SizeRunEfficiencyRow](ctx, is.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get size run efficiency: %w", err)
	}
	return result, nil
}
