package order

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func refundAmountFromItems(items []entity.OrderItemInsert, currency string) decimal.Decimal {
	var sum decimal.Decimal
	for _, item := range items {
		sum = sum.Add(item.ProductPriceWithSale.Mul(item.Quantity))
	}
	return dto.RoundForCurrency(sum, currency)
}

// markShippingRefunded flags that the shipping cost has been refunded for this
// order so a subsequent partial refund cannot refund it again. Must be called
// within the RefundOrder transaction that holds the customer_order row FOR UPDATE.
func markShippingRefunded(ctx context.Context, db dependency.DB, orderId int) error {
	query := `UPDATE customer_order SET shipping_refunded = TRUE WHERE id = :orderId`
	if err := storeutil.ExecNamed(ctx, db, query, map[string]any{"orderId": orderId}); err != nil {
		return fmt.Errorf("update shipping_refunded: %w", err)
	}
	return nil
}

// stockRestoreMode describes how stock should be handled when cancelling an
// order in a given status. Stock is only ever reduced on the
// Placed -> AwaitingPayment transition (InsertFiatInvoice), so restoring it for
// a status that never reduced it would inflate inventory.
type stockRestoreMode int

const (
	stockRestoreNone    stockRestoreMode = iota // status never reduced stock (e.g. Placed)
	stockRestoreSilent                          // reduced at invoice time; restore without history
	stockRestoreHistory                         // reduced for a confirmed+ order; restore with history
)

func stockRestoreModeForCancel(st entity.OrderStatusName) stockRestoreMode {
	switch st {
	case entity.Placed:
		return stockRestoreNone
	case entity.AwaitingPayment:
		return stockRestoreSilent
	default:
		return stockRestoreHistory
	}
}

func cancelOrder(ctx context.Context, rep dependency.Repository, order *entity.Order, orderItems []entity.OrderItemInsert, source entity.StockChangeSource, refundReason string) error {
	orderStatus, err := getOrderStatus(order.OrderStatusId)
	if err != nil {
		return err
	}

	st := orderStatus.Status.Name
	if st == entity.Cancelled {
		return nil
	}

	_, err = validateOrderStatusNot(order, entity.Refunded, entity.PartiallyRefunded, entity.Delivered, entity.Shipped, entity.Confirmed)
	if err != nil {
		return fmt.Errorf("order cannot be cancelled: %w", err)
	}

	// Restore stock only for statuses that actually reduced it; restoring for a
	// Placed order (which never reduced stock) would inflate inventory.
	switch stockRestoreModeForCancel(st) {
	case stockRestoreNone:
		// Stock was never reduced for this status; nothing to restore.
	case stockRestoreSilent:
		if err := rep.Products().RestoreStockSilently(ctx, orderItems); err != nil {
			return fmt.Errorf("can't restore stock for product sizes: %w", err)
		}
	case stockRestoreHistory:
		history := &entity.StockHistoryParams{
			Source:    source,
			OrderId:   order.Id,
			OrderUUID: order.UUID,
		}
		if err := rep.Products().RestoreStockForProductSizes(ctx, orderItems, history); err != nil {
			return fmt.Errorf("can't restore stock for product sizes: %w", err)
		}
	}

	if order.PromoId.Int32 != 0 {
		err := removePromo(ctx, rep.DB(), order.Id)
		if err != nil {
			return fmt.Errorf("can't remove promo: %w", err)
		}
	}

	statusCancelled, ok := cache.GetOrderStatusByName(entity.Cancelled)
	if !ok {
		return fmt.Errorf("can't get order status by name %s", entity.Cancelled)
	}

	err = updateOrderStatus(ctx, rep.DB(), order.Id, statusCancelled.Status.Id)
	if err != nil {
		return fmt.Errorf("can't update order status: %w", err)
	}

	return nil
}

func getOrdersByStatusAndPayment(ctx context.Context, db dependency.DB, orderStatusId int, paymentMethodIds ...int) ([]entity.Order, error) {
	query := `
    SELECT 
        co.*
    FROM customer_order co 
    `

	var params = map[string]interface{}{
		"status": orderStatusId,
	}

	if len(paymentMethodIds) > 0 {
		query += `
        JOIN payment p ON co.id = p.order_id
        WHERE co.order_status_id = :status AND p.payment_method_id IN (:paymentMethodIds)
        `
		params["paymentMethodIds"] = paymentMethodIds
	} else {
		query += `
        WHERE co.order_status_id = :status
        `
	}

	orders, err := storeutil.QueryListNamed[entity.Order](ctx, db, query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get orders by status and payment method: %w", err)
	}

	return orders, nil
}
