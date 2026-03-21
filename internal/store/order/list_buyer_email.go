package order

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ListOrdersFullByBuyerEmailPaged returns orders for a buyer email, newest first, with total count.
func (s *Store) ListOrdersFullByBuyerEmailPaged(ctx context.Context, email string, limit, offset int) ([]entity.OrderFull, int, error) {
	countQ := `
		SELECT COUNT(*)
		FROM customer_order co
		INNER JOIN buyer b ON co.id = b.order_id
		WHERE b.email = :email`
	total, err := storeutil.QueryCountNamed(ctx, s.DB, countQ, map[string]any{"email": email})
	if err != nil {
		return nil, 0, fmt.Errorf("count orders by email: %w", err)
	}
	if total == 0 {
		return []entity.OrderFull{}, 0, nil
	}

	dataQ := `
		SELECT co.*
		FROM customer_order co
		INNER JOIN buyer b ON co.id = b.order_id
		WHERE b.email = :email
		ORDER BY co.placed DESC
		LIMIT :limit OFFSET :offset`
	orders, err := storeutil.QueryListNamed[entity.Order](ctx, s.DB, dataQ, map[string]any{
		"email":  email,
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list orders by email: %w", err)
	}

	rep := s.repFunc()
	ofs, err := fetchOrderInfo(ctx, rep, orders)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch order info: %w", err)
	}
	return ofs, total, nil
}
