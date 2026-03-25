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
			END AS days_on_hand
		FROM product_size ps
		JOIN product p ON p.id = ps.product_id
		JOIN size s ON s.id = ps.size_id
		LEFT JOIN daily_sales ds ON ds.product_id = ps.product_id AND ds.size_id = ps.size_id
		WHERE ps.quantity > 0
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
	return result, nil
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
