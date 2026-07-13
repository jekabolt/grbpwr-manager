package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// GetInventoryValuation is the money view of the warehouse (task 16): the cost frozen in stock,
// how much of it is dead (in stock but unsold in the window), and how much was written off in
// the period. Stock is valued at the current plan cost_price (v1); products with no cost are
// counted as uncosted (value unknown) rather than zero, so coverage stays honest. from/to bound
// the sales window (for dead-stock detection) and the write-off period.
func (s *Store) GetInventoryValuation(ctx context.Context, from, to time.Time, limit int) (*entity.InventoryValuation, error) {
	// Per-product on-hand + cost + units sold in the window. Sales are LEFT-joined (one row per
	// product) so the product_size fan-out sums on-hand correctly and MAX carries the sold units.
	stockQuery := `
		WITH sold AS (
			SELECT oi.product_id, SUM(oi.quantity) AS units
			FROM order_item oi
			JOIN customer_order co ON co.id = oi.order_id
			WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
			GROUP BY oi.product_id
		)
		SELECT
			p.id AS product_id,
			COALESCE((SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1), p.brand) AS product_name,
			p.cost_price AS cost_price,
			COALESCE(SUM(ps.quantity), 0) AS on_hand,
			COALESCE(MAX(sold.units), 0) AS sold_units
		FROM product p
		JOIN product_size ps ON ps.product_id = p.id
		LEFT JOIN sold ON sold.product_id = p.id
		WHERE p.deleted_at IS NULL
		GROUP BY p.id, product_name, p.cost_price
		HAVING on_hand > 0`

	rows, err := storeutil.QueryListNamed[struct {
		ProductID   int                 `db:"product_id"`
		ProductName string              `db:"product_name"`
		CostPrice   decimal.NullDecimal `db:"cost_price"`
		OnHand      int64               `db:"on_hand"`
		SoldUnits   int64               `db:"sold_units"`
	}](ctx, s.DB, stockQuery, map[string]any{
		"from":      from,
		"to":        to,
		"statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return nil, fmt.Errorf("get inventory valuation stock: %w", err)
	}

	out := &entity.InventoryValuation{}
	costed := make([]entity.InventoryValuationRow, 0, len(rows))
	for _, r := range rows {
		out.TotalOnHandUnits += r.OnHand
		if !r.CostPrice.Valid {
			out.UncostedStockUnits += r.OnHand
			out.UncostedStockProducts++
			continue
		}
		value := r.CostPrice.Decimal.Mul(decimal.NewFromInt(r.OnHand))
		out.TotalStockValue = out.TotalStockValue.Add(value)
		out.CostedOnHandUnits += r.OnHand
		costed = append(costed, entity.InventoryValuationRow{
			ProductID:   r.ProductID,
			ProductName: r.ProductName,
			OnHand:      r.OnHand,
			UnitCost:    r.CostPrice.Decimal,
			Value:       value.Round(2),
			SoldUnits:   r.SoldUnits,
		})
	}
	out.TotalStockValue = out.TotalStockValue.Round(2)
	if out.TotalOnHandUnits > 0 {
		out.CoveragePct = decimal.NewFromInt(out.CostedOnHandUnits).
			Div(decimal.NewFromInt(out.TotalOnHandUnits)).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}

	// Top by frozen value (all costed in-stock products), and dead stock (those unsold in the
	// window), each ranked by value descending and capped at limit.
	byValue := make([]entity.InventoryValuationRow, len(costed))
	copy(byValue, costed)
	sort.SliceStable(byValue, func(i, j int) bool { return byValue[i].Value.GreaterThan(byValue[j].Value) })
	out.TopByValue = capRows(byValue, limit)

	dead := make([]entity.InventoryValuationRow, 0, len(byValue))
	for _, r := range byValue {
		if r.SoldUnits == 0 {
			dead = append(dead, r)
		}
	}
	out.DeadStock = capRows(dead, limit)

	// Write-offs (damage/loss manual adjustments) in the period, valued at current cost_price.
	// Units count every write-off; value only the costed ones (uncosted value is unknown).
	writeOff, err := storeutil.QueryNamedOne[struct {
		Value decimal.Decimal `db:"value"`
		Units int64           `db:"units"`
	}](ctx, s.DB, `
		SELECT
			COALESCE(SUM(ABS(h.quantity_delta) * p.cost_price), 0) AS value,
			CAST(COALESCE(SUM(ABS(h.quantity_delta)), 0) AS SIGNED) AS units
		FROM product_stock_change_history h
		JOIN product p ON p.id = h.product_id
		WHERE h.source = 'manual_adjustment'
			AND h.reason IN ('damage', 'loss')
			AND h.quantity_delta < 0
			AND h.created_at >= :from AND h.created_at < :to`,
		map[string]any{"from": from, "to": to})
	if err != nil {
		return nil, fmt.Errorf("get inventory valuation write-offs: %w", err)
	}
	out.WriteOffsValue = writeOff.Value.Round(2)
	out.WriteOffsUnits = writeOff.Units

	return out, nil
}

// capRows returns the first limit rows (all when limit <= 0).
func capRows(rows []entity.InventoryValuationRow, limit int) []entity.InventoryValuationRow {
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}
