package order

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetOrderById retrieves a full order by its numeric ID.
func (s *Store) GetOrderById(ctx context.Context, orderId int) (*entity.OrderFull, error) {
	order, err := getOrderById(ctx, s.DB, orderId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("order is not found")
		}
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}
	rep := s.repFunc()
	ofs, err := fetchOrderInfo(ctx, rep, []entity.Order{*order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
	}
	return &ofs[0], nil
}

// GetOrderFullByUUID retrieves a full order with status history by UUID.
func (s *Store) GetOrderFullByUUID(ctx context.Context, uuid string) (*entity.OrderFull, error) {
	order, err := getOrderByUUID(ctx, s.DB, uuid)
	if err != nil {
		return nil, err
	}
	rep := s.repFunc()
	ofs, err := fetchOrderInfo(ctx, rep, []entity.Order{*order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
	}
	if len(ofs) == 0 {
		return nil, fmt.Errorf("order is not found")
	}

	statusHistory, err := getOrderStatusHistory(ctx, s.DB, order.Id)
	if err != nil {
		return nil, fmt.Errorf("get status history: %w", err)
	}
	ofs[0].StatusHistory = statusHistory

	return &ofs[0], nil
}

// GetOrderByUUIDAndEmail retrieves a full order by UUID and buyer email.
func (s *Store) GetOrderByUUIDAndEmail(ctx context.Context, orderUUID string, email string) (*entity.OrderFull, error) {
	query := `
		SELECT co.*
		FROM customer_order co
		INNER JOIN buyer b ON co.id = b.order_id
		WHERE co.uuid = :orderUUID AND b.email = :email
	`

	order, err := storeutil.QueryNamedOne[entity.Order](ctx, s.DB, query, map[string]interface{}{
		"orderUUID": orderUUID,
		"email":     email,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order by uuid and email: %w", err)
	}

	rep := s.repFunc()
	ofs, err := fetchOrderInfo(ctx, rep, []entity.Order{order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
	}

	statusHistory, err := getOrderStatusHistory(ctx, s.DB, order.Id)
	if err != nil {
		return nil, fmt.Errorf("get status history: %w", err)
	}
	ofs[0].StatusHistory = statusHistory

	return &ofs[0], nil
}

// GetOrderByUUID retrieves a basic order by UUID.
func (s *Store) GetOrderByUUID(ctx context.Context, uuid string) (*entity.Order, error) {
	return getOrderByUUID(ctx, s.DB, uuid)
}

// GetOrderByPaymentIntentId retrieves an order by its PaymentIntent ID for idempotency.
func (s *Store) GetOrderByPaymentIntentId(ctx context.Context, paymentIntentId string) (*entity.OrderFull, error) {
	query := `
    SELECT co.*
    FROM customer_order co
    JOIN payment p ON p.order_id = co.id
    WHERE p.client_secret = :paymentIntentId;`

	order, err := storeutil.QueryNamedOne[entity.Order](ctx, s.DB, query, map[string]interface{}{
		"paymentIntentId": paymentIntentId,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get order by payment intent ID: %w", err)
	}

	rep := s.repFunc()
	ofs, err := fetchOrderInfo(ctx, rep, []entity.Order{order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
	}
	if len(ofs) == 0 {
		return nil, fmt.Errorf("order not found")
	}

	return &ofs[0], nil
}

// GetOrdersByStatusAndPaymentTypePaged retrieves orders filtered by status, payment method, email, etc.
func (s *Store) GetOrdersByStatusAndPaymentTypePaged(ctx context.Context, email string, orderUUID string, statusId, paymentMethodId, orderId, lim, off int, of entity.OrderFactor) ([]entity.Order, error) {
	query := fmt.Sprintf(`
		SELECT 
			co.*
		FROM 
			customer_order co 
		INNER JOIN 
			payment p ON co.id = p.order_id
		INNER JOIN 
			buyer b ON co.id = b.order_id
		WHERE 
			(:status = 0 OR co.order_status_id = :status) 
			AND (:paymentMethod = 0 OR p.payment_method_id = :paymentMethod)
			AND (:email = '' OR b.email = :email)
			AND (:orderId = 0 OR co.id = :orderId)
			AND (:orderUUID = '' OR co.uuid = :orderUUID)
		ORDER BY 
			co.modified %s
		LIMIT 
			:limit
		OFFSET 
			:offset
		`, of.String())

	params := map[string]interface{}{
		"email":         email,
		"status":        statusId,
		"paymentMethod": paymentMethodId,
		"orderId":       orderId,
		"orderUUID":     orderUUID,
		"limit":         lim,
		"offset":        off,
	}

	orders, err := storeutil.QueryListNamed[entity.Order](ctx, s.DB, query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get orders by status and payment method: %w", err)
	}

	return orders, nil
}

// GetStuckPlacedOrders returns orders in Placed status older than the given time.
func (s *Store) GetStuckPlacedOrders(ctx context.Context, olderThan time.Time) ([]entity.Order, error) {
	query := `
    SELECT co.*
    FROM customer_order co
    WHERE co.order_status_id = :status AND co.placed < :olderThan
    `
	orders, err := storeutil.QueryListNamed[entity.Order](ctx, s.DB, query, map[string]interface{}{
		"status":    cache.OrderStatusPlaced.Status.Id,
		"olderThan": olderThan,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get stuck placed orders: %w", err)
	}
	return orders, nil
}

// GetExpiredAwaitingPaymentOrders returns orders in AwaitingPayment where payment expired.
func (s *Store) GetExpiredAwaitingPaymentOrders(ctx context.Context, now time.Time) ([]entity.Order, error) {
	query := `
    SELECT co.*
    FROM customer_order co
    JOIN payment p ON co.id = p.order_id
    WHERE co.order_status_id = :status AND p.expired_at IS NOT NULL AND p.expired_at < :now
    `
	orders, err := storeutil.QueryListNamed[entity.Order](ctx, s.DB, query, map[string]interface{}{
		"status": cache.OrderStatusAwaitingPayment.Status.Id,
		"now":    now,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get expired awaiting payment orders: %w", err)
	}
	return orders, nil
}

// AddOrderComment adds a comment to an order.
func (s *Store) AddOrderComment(ctx context.Context, orderUUID string, comment string) error {
	_, err := getOrderByUUID(ctx, s.DB, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get order by UUID: %w", err)
	}

	query := `
		UPDATE customer_order
		SET order_comment = :comment
		WHERE uuid = :uuid`

	err = storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"comment": comment,
		"uuid":    orderUUID,
	})
	if err != nil {
		return fmt.Errorf("can't update order comment: %w", err)
	}

	return nil
}
