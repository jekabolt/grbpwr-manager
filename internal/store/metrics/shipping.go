package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func (s *Store) getShippingCostMetrics(ctx context.Context, from, to time.Time) (avgCost, totalCost decimal.Decimal, _ error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT 
			COALESCE(AVG(scp.price), 0) AS avg_cost,
			COALESCE(SUM(scp.price), 0) AS total_cost
		FROM shipment s
		JOIN customer_order co ON s.order_id = co.id
		JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
	`
	params := map[string]any{
		"from":         from,
		"to":           to,
		"baseCurrency": baseCurrency,
		"statusIds":    cache.OrderStatusIDsForNetRevenue(),
	}
	rows, err := storeutil.QueryListNamed[struct {
		AvgCost   decimal.Decimal `db:"avg_cost"`
		TotalCost decimal.Decimal `db:"total_cost"`
	}](ctx, s.DB, query, params)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("getShippingCostMetrics: %w", err)
	}
	if len(rows) == 0 {
		return decimal.Zero, decimal.Zero, nil
	}
	return rows[0].AvgCost, rows[0].TotalCost, nil
}
