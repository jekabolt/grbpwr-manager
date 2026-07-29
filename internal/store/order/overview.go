package order

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type orderStatusCount struct {
	Status string `db:"status"`
	Count  int    `db:"count"`
}

// GetOrdersOverview returns whole-table status counts and UTC-today order totals.
// Revenue is grouped by customer_order.currency so unlike currencies are never
// summed together.
func (s *Store) GetOrdersOverview(ctx context.Context, todayStart time.Time) (*entity.OrdersOverview, error) {
	statusRows, err := storeutil.QueryListNamed[orderStatusCount](ctx, s.DB, `
		SELECT os.name AS status, COUNT(*) AS count
		FROM customer_order co
		JOIN order_status os ON os.id = co.order_status_id
		GROUP BY os.name
		ORDER BY os.name`, nil)
	if err != nil {
		return nil, fmt.Errorf("can't count orders by status: %w", err)
	}

	statusCounts := make(map[string]int, len(statusRows))
	for _, row := range statusRows {
		statusCounts[row.Status] = row.Count
	}

	params := map[string]any{"todayStart": todayStart}
	todayOrders, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*)
		FROM customer_order
		WHERE placed >= :todayStart`, params)
	if err != nil {
		return nil, fmt.Errorf("can't count today's orders: %w", err)
	}

	todayRevenue, err := storeutil.QueryListNamed[entity.MoneyByCurrency](ctx, s.DB, `
		SELECT currency, SUM(total_price) AS amount
		FROM customer_order
		WHERE placed >= :todayStart
		GROUP BY currency
		ORDER BY currency`, params)
	if err != nil {
		return nil, fmt.Errorf("can't sum today's order revenue: %w", err)
	}

	return &entity.OrdersOverview{
		StatusCounts: statusCounts,
		TodayOrders:  todayOrders,
		TodayRevenue: todayRevenue,
	}, nil
}
