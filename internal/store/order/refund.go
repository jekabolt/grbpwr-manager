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
	"github.com/jekabolt/grbpwr-manager/internal/store/inventory"
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

	// Release the order's open packaging reservations (PLM rework §2.8, S22) atomically with the stock
	// restore: a cancelled order will never ship, so its soft packaging holds are returned (no physical
	// writeoff — on_hand is untouched). This is the single choke point for every stock-restoring cancel
	// path (admin CancelOrder, user cancel, payment-fail, order-items-validation-fail), so those callers
	// need not release packaging separately.
	if err := releaseOpenPackagingClaims(ctx, rep.DB(), order.Id); err != nil {
		return fmt.Errorf("can't release packaging reservations: %w", err)
	}

	return nil
}

// releaseOpenPackagingClaims closes an order's still-open packaging reservation claims (a 'reserve'
// with no 'consume'/'release') with 'release' rows, idempotently. It is a plain statement on the
// caller's transaction so it runs atomically inside cancelOrder without opening a nested transaction.
//
// L2 fix (review-plm-backend.md): this used to duplicate the inventory store's open-claim SQL inline
// ("the two must stay in sync" by comment convention, a silent-drift risk if the open-claim/event
// definition ever changed in only one place). It now delegates to the single definition,
// inventory.ReleaseOpenClaimsInTx, passing "" for the acting username the same way the inline SQL
// always stamped created_by='' here (every caller of this function is a system-triggered cancel path,
// not an admin action with a real actor) — same event vocabulary, same idempotency, one implementation.
func releaseOpenPackagingClaims(ctx context.Context, db dependency.DB, orderID int) error {
	if err := inventory.ReleaseOpenClaimsInTx(ctx, db, orderID, ""); err != nil {
		return fmt.Errorf("release packaging reservations for order %d: %w", orderID, err)
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
