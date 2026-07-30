package order

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
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
//
// The TODAY figures count only orders that were actually paid — the same status set every metrics /
// dashboard revenue query filters on (cache.OrderStatusIDsForNetRevenue: confirmed, shipped,
// delivered, partially_refunded). A customer_order row is inserted the moment checkout STARTS
// (CreateOrder writes status `placed` with total_price already filled) and plenty of those are never
// paid — the ordercleanup worker cancels them later, keeping their `placed` timestamp — so counting
// every row with placed >= todayStart would inflate the ops header with abandoned checkouts and
// cancelled orders and structurally contradict every dashboard number.
//
// status_counts stays whole-table on purpose: it exists to show how many orders sit in each state,
// `placed` and `cancelled` included.
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

	paidStatusIDs := cache.OrderStatusIDsForNetRevenue()
	if len(paidStatusIDs) == 0 {
		// Order statuses come from the dictionary cache, seeded at boot. Without them there is no way to
		// tell a paid order from an abandoned checkout (and an empty slice fails sqlx.In anyway), so say
		// so rather than fall back to the inflated unfiltered totals.
		return nil, fmt.Errorf("can't compute today's order totals: order status cache is empty")
	}

	params := map[string]any{"todayStart": todayStart, "statusIds": paidStatusIDs}
	todayOrders, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*)
		FROM customer_order
		WHERE placed >= :todayStart
			AND order_status_id IN (:statusIds)`, params)
	if err != nil {
		return nil, fmt.Errorf("can't count today's orders: %w", err)
	}

	todayRevenue, err := storeutil.QueryListNamed[entity.MoneyByCurrency](ctx, s.DB, `
		SELECT currency, SUM(total_price) AS amount
		FROM customer_order
		WHERE placed >= :todayStart
			AND order_status_id IN (:statusIds)
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
