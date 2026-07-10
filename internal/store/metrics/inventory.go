package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
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
			SUM(CASE WHEN sa.sell_through_pct > 0 THEN 1 ELSE 0 END) * 100 / COUNT(*) AS efficiency_pct
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
