package order

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func getOrderStatus(orderStatusId int) (*cache.Status, error) {
	orderStatus, ok := cache.GetOrderStatusById(orderStatusId)
	if !ok {
		return nil, fmt.Errorf("order status does not exist: order status id %d", orderStatusId)
	}
	return &orderStatus, nil
}

func validateOrderStatus(order *entity.Order, allowedStatuses ...entity.OrderStatusName) (*cache.Status, error) {
	orderStatus, err := getOrderStatus(order.OrderStatusId)
	if err != nil {
		return nil, err
	}

	if len(allowedStatuses) == 0 {
		return orderStatus, nil
	}

	for _, allowed := range allowedStatuses {
		if orderStatus.Status.Name == allowed {
			return orderStatus, nil
		}
	}

	return nil, fmt.Errorf("invalid order status '%s', expected one of: %v", orderStatus.Status.Name, allowedStatuses)
}

func validateOrderStatusNot(order *entity.Order, disallowedStatuses ...entity.OrderStatusName) (*cache.Status, error) {
	orderStatus, err := getOrderStatus(order.OrderStatusId)
	if err != nil {
		return nil, err
	}

	for _, disallowed := range disallowedStatuses {
		if orderStatus.Status.Name == disallowed {
			return nil, fmt.Errorf("order status cannot be '%s'", disallowed)
		}
	}

	return orderStatus, nil
}

// ValidStatusTransitions defines allowed status transitions.
var ValidStatusTransitions = map[entity.OrderStatusName][]entity.OrderStatusName{
	entity.Placed: {
		entity.AwaitingPayment,
		entity.Cancelled,
	},
	entity.AwaitingPayment: {
		entity.Confirmed,
		entity.Cancelled,
	},
	entity.Confirmed: {
		entity.Shipped,
		entity.RefundInProgress,
		entity.Refunded,
		entity.Cancelled,
	},
	entity.Shipped: {
		entity.Delivered,
		entity.PendingReturn,
	},
	entity.Delivered: {
		entity.PendingReturn,
		entity.Refunded,
		entity.PartiallyRefunded,
	},
	entity.PendingReturn: {
		entity.Refunded,
		entity.PartiallyRefunded,
	},
	entity.RefundInProgress: {
		entity.Refunded,
		entity.PartiallyRefunded,
	},
	entity.PartiallyRefunded: {
		entity.Refunded,
		entity.PartiallyRefunded,
	},
	entity.Cancelled: {},
	entity.Refunded:  {},
}

func isValidStatusTransition(currentStatus, newStatus entity.OrderStatusName) bool {
	allowedTransitions, exists := ValidStatusTransitions[currentStatus]
	if !exists {
		return false
	}

	for _, allowed := range allowedTransitions {
		if allowed == newStatus {
			return true
		}
	}
	return false
}

func insertOrderStatusHistoryEntry(ctx context.Context, db dependency.DB, orderId int, statusId int, changedBy string, notes string) error {
	query := `
		INSERT INTO order_status_history (order_id, order_status_id, changed_by, notes)
		VALUES (:orderId, :statusId, :changedBy, :notes)`
	return storeutil.ExecNamed(ctx, db, query, map[string]any{
		"orderId":   orderId,
		"statusId":  statusId,
		"changedBy": changedBy,
		"notes":     notes,
	})
}

func updateOrderStatusWithValidation(ctx context.Context, db dependency.DB, orderId int, newStatusId int, changedBy string, notes string) error {
	var currentStatusId int
	query := `SELECT order_status_id FROM customer_order WHERE id = ?`
	err := db.GetContext(ctx, &currentStatusId, query, orderId)
	if err != nil {
		return fmt.Errorf("get current status: %w", err)
	}

	currentStatus, err := getOrderStatus(currentStatusId)
	if err != nil {
		return fmt.Errorf("get current status name: %w", err)
	}

	newStatus, err := getOrderStatus(newStatusId)
	if err != nil {
		return fmt.Errorf("get new status name: %w", err)
	}

	if !isValidStatusTransition(currentStatus.Status.Name, newStatus.Status.Name) {
		return fmt.Errorf(
			"invalid status transition: cannot change from %s to %s",
			currentStatus.Status.Name,
			newStatus.Status.Name,
		)
	}

	updateQuery := `
		UPDATE customer_order 
		SET order_status_id = :newStatusId,
			modified = CURRENT_TIMESTAMP
		WHERE id = :orderId
	`

	err = storeutil.ExecNamed(ctx, db, updateQuery, map[string]any{
		"orderId":     orderId,
		"newStatusId": newStatusId,
	})
	if err != nil {
		return fmt.Errorf("update order status: %w", err)
	}

	return insertOrderStatusHistoryEntry(ctx, db, orderId, newStatusId, changedBy, notes)
}

func updateOrderStatus(ctx context.Context, db dependency.DB, orderId int, orderStatusId int) error {
	return updateOrderStatusWithValidation(ctx, db, orderId, orderStatusId, "system", "")
}

func getOrderStatusHistory(ctx context.Context, db dependency.DB, orderId int) ([]entity.OrderStatusHistoryWithStatus, error) {
	query := `
		SELECT 
			osh.id,
			osh.order_id,
			osh.order_status_id,
			osh.changed_at,
			osh.changed_by,
			osh.notes,
			os.name as status_name
		FROM order_status_history osh
		JOIN order_status os ON osh.order_status_id = os.id
		WHERE osh.order_id = ?
		ORDER BY osh.changed_at ASC
	`

	var history []entity.OrderStatusHistoryWithStatus
	err := db.SelectContext(ctx, &history, query, orderId)
	return history, err
}

func updateOrderStatusAndRefundedAmountWithValidation(ctx context.Context, db dependency.DB, orderId int, orderStatusId int, refundedAmount decimal.Decimal, refundReason string, changedBy string) error {
	var currentStatusId int
	query := `SELECT order_status_id FROM customer_order WHERE id = ?`
	err := db.GetContext(ctx, &currentStatusId, query, orderId)
	if err != nil {
		return fmt.Errorf("get current status: %w", err)
	}

	currentStatus, err := getOrderStatus(currentStatusId)
	if err != nil {
		return fmt.Errorf("get current status name: %w", err)
	}

	newStatus, err := getOrderStatus(orderStatusId)
	if err != nil {
		return fmt.Errorf("get new status name: %w", err)
	}

	if !isValidStatusTransition(currentStatus.Status.Name, newStatus.Status.Name) {
		return fmt.Errorf(
			"invalid status transition: cannot change from %s to %s",
			currentStatus.Status.Name,
			newStatus.Status.Name,
		)
	}

	updateQuery := `
		UPDATE customer_order 
		SET order_status_id = :orderStatusId,
			refunded_amount = :refundedAmount,
			refund_reason = COALESCE(NULLIF(:refundReason, ''), refund_reason),
			modified = CURRENT_TIMESTAMP
		WHERE id = :orderId
	`

	err = storeutil.ExecNamed(ctx, db, updateQuery, map[string]any{
		"orderId":        orderId,
		"orderStatusId":  orderStatusId,
		"refundedAmount": refundedAmount.Round(2),
		"refundReason":   refundReason,
	})
	if err != nil {
		return fmt.Errorf("update order: %w", err)
	}

	notes := fmt.Sprintf("Refunded amount: %s", refundedAmount.String())
	return insertOrderStatusHistoryEntry(ctx, db, orderId, orderStatusId, changedBy, notes)
}

func updateOrderStatusAndAccumulateRefundedAmount(ctx context.Context, db dependency.DB, orderId int, orderStatusId int, refundedAmount decimal.Decimal, refundReason string) error {
	return updateOrderStatusAndAccumulateRefundedAmountWithValidation(ctx, db, orderId, orderStatusId, refundedAmount, refundReason, "admin")
}

func updateOrderStatusAndAccumulateRefundedAmountWithValidation(ctx context.Context, db dependency.DB, orderId int, orderStatusId int, refundedAmount decimal.Decimal, refundReason string, changedBy string) error {
	var currentStatusId int
	query := `SELECT order_status_id FROM customer_order WHERE id = ?`
	err := db.GetContext(ctx, &currentStatusId, query, orderId)
	if err != nil {
		return fmt.Errorf("get current status: %w", err)
	}

	currentStatus, err := getOrderStatus(currentStatusId)
	if err != nil {
		return fmt.Errorf("get current status name: %w", err)
	}

	newStatus, err := getOrderStatus(orderStatusId)
	if err != nil {
		return fmt.Errorf("get new status name: %w", err)
	}

	if !isValidStatusTransition(currentStatus.Status.Name, newStatus.Status.Name) {
		return fmt.Errorf(
			"invalid status transition: cannot change from %s to %s",
			currentStatus.Status.Name,
			newStatus.Status.Name,
		)
	}

	updateQuery := `
		UPDATE customer_order 
		SET order_status_id = :orderStatusId,
			refunded_amount = refunded_amount + :refundedAmount,
			refund_reason = COALESCE(NULLIF(:refundReason, ''), refund_reason),
			modified = CURRENT_TIMESTAMP
		WHERE id = :orderId
	`

	err = storeutil.ExecNamed(ctx, db, updateQuery, map[string]any{
		"orderId":        orderId,
		"orderStatusId":  orderStatusId,
		"refundedAmount": refundedAmount.Round(2),
		"refundReason":   refundReason,
	})
	if err != nil {
		return fmt.Errorf("update order: %w", err)
	}

	notes := fmt.Sprintf("Refunded amount: %s (accumulated)", refundedAmount.String())
	return insertOrderStatusHistoryEntry(ctx, db, orderId, orderStatusId, changedBy, notes)
}
