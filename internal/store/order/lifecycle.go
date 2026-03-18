package order

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// refundItem holds order_item_id and OrderItemInsert for stock restoration and refunded_order_item inserts.
type refundItem struct {
	OrderItemId     int
	OrderItemInsert entity.OrderItemInsert
}

// validateAndMapOrderItems maps order item IDs to refundItem, skipping IDs that don't
// belong to the order. Each occurrence of an ID = 1 unit to refund (e.g. [1,1,1] = 3 units).
// Returns nil for full refund (empty IDs).
// alreadyRefunded maps order_item_id to quantity already refunded.
func validateAndMapOrderItems(orderItems []entity.OrderItem, orderItemIDs []int32, alreadyRefunded map[int]int64) ([]refundItem, error) {
	if len(orderItemIDs) == 0 {
		return nil, nil // Signal full refund
	}

	itemByID := make(map[int]entity.OrderItem, len(orderItems))
	for _, item := range orderItems {
		itemByID[item.Id] = item
	}

	requestedRefund := make(map[int]int64)
	for _, id := range orderItemIDs {
		requestedRefund[int(id)]++
	}

	itemsToRefund := make([]refundItem, 0, len(orderItemIDs))

	for orderItemId, requestedQty := range requestedRefund {
		item, ok := itemByID[orderItemId]
		if !ok {
			continue
		}

		originalQty := item.Quantity.IntPart()
		refundedQty := alreadyRefunded[orderItemId]
		remainingQty := originalQty - refundedQty

		if remainingQty <= 0 {
			return nil, fmt.Errorf("order item %d already fully refunded", orderItemId)
		}

		actualRefundQty := requestedQty
		if actualRefundQty > remainingQty {
			return nil, fmt.Errorf("cannot refund %d units of order item %d: only %d units remaining (original: %d, already refunded: %d)",
				requestedQty, orderItemId, remainingQty, originalQty, refundedQty)
		}

		for i := int64(0); i < actualRefundQty; i++ {
			insert := item.OrderItemInsert
			insert.Quantity = decimal.NewFromInt(1)
			itemsToRefund = append(itemsToRefund, refundItem{OrderItemId: item.Id, OrderItemInsert: insert})
		}
	}

	if len(itemsToRefund) == 0 {
		return nil, fmt.Errorf("no valid order items to refund")
	}

	return itemsToRefund, nil
}

// refundCoversFullOrder returns true when the requested orderItemIDs cover all remaining
// refundable quantity for all order items.
func refundCoversFullOrder(orderItems []entity.OrderItem, orderItemIDs []int32, alreadyRefunded map[int]int64) bool {
	requested := make(map[int]int64)
	for _, id := range orderItemIDs {
		requested[int(id)]++
	}

	for _, item := range orderItems {
		req := requested[item.Id]
		originalQty := item.Quantity.IntPart()
		refundedQty := alreadyRefunded[item.Id]
		remainingQty := originalQty - refundedQty

		if req < remainingQty {
			return false
		}
	}
	return true
}

// determineRefundScope determines which items to refund and the target status based on
// the current order status and requested item IDs.
func determineRefundScope(currentStatus entity.OrderStatusName, orderItems []entity.OrderItem, orderItemIDs []int32, alreadyRefunded map[int]int64) ([]refundItem, *cache.Status, error) {
	if currentStatus == entity.Confirmed {
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	partialItems, err := validateAndMapOrderItems(orderItems, orderItemIDs, alreadyRefunded)
	if err != nil {
		return nil, nil, err
	}

	if partialItems == nil {
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	if refundCoversFullOrder(orderItems, orderItemIDs, alreadyRefunded) {
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	return partialItems, &cache.OrderStatusPartiallyRefunded, nil
}

func orderItemsToRefundItems(orderItems []entity.OrderItem) []refundItem {
	out := make([]refundItem, len(orderItems))
	for i, item := range orderItems {
		out[i] = refundItem{OrderItemId: item.Id, OrderItemInsert: item.OrderItemInsert}
	}
	return out
}

// SetTrackingNumber sets the tracking code for an order and updates status to Shipped.
func (s *Store) SetTrackingNumber(ctx context.Context, orderUUID string, trackingCode string) (*entity.OrderBuyerShipment, error) {
	var order *entity.Order
	var shipment *entity.Shipment
	var buyer *entity.Buyer

	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		order, err = getOrderByUUIDForUpdate(ctx, rep.DB(), orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		_, err = validateOrderStatus(order, entity.Confirmed, entity.Shipped)
		if err != nil {
			return fmt.Errorf("bad order status for setting tracking number: %w", err)
		}

		shipment, err = getOrderShipment(ctx, rep.DB(), order.Id)
		if err != nil {
			return fmt.Errorf("can't get order shipment: %w", err)
		}

		shipment.TrackingCode = sql.NullString{
			String: trackingCode,
			Valid:  true,
		}

		buyer, err = getBuyerById(ctx, rep.DB(), order.Id)
		if err != nil {
			return fmt.Errorf("can't get buyer by id: %w", err)
		}

		err = updateOrderShipment(ctx, rep.DB(), shipment)
		if err != nil {
			return fmt.Errorf("can't update order shipment: %w", err)
		}

		err = updateOrderStatus(ctx, rep.DB(), order.Id, cache.OrderStatusShipped.Status.Id)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("can't set tracking number: %w", err)
	}

	order, err = getOrderByUUID(ctx, s.DB, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order after update: %w", err)
	}

	return &entity.OrderBuyerShipment{
		Order:    order,
		Buyer:    buyer,
		Shipment: shipment,
	}, nil
}

// RefundOrder processes a full or partial refund for an order.
func (s *Store) RefundOrder(ctx context.Context, orderUUID string, orderItemIDs []int32, reason string, refundShipping bool) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUIDForUpdate(ctx, rep.DB(), orderUUID)
		if err != nil {
			return err
		}

		orderStatus, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}
		os := orderStatus.Status.Name

		allowed := os == entity.RefundInProgress || os == entity.PendingReturn || os == entity.Delivered || os == entity.Confirmed || os == entity.PartiallyRefunded
		if !allowed {
			return fmt.Errorf("order status must be refund_in_progress, pending_return, delivered, confirmed or partially_refunded, got %s", orderStatus.Status.Name)
		}
		if os == entity.Confirmed && len(orderItemIDs) > 0 {
			return fmt.Errorf("confirmed orders support only full refund")
		}

		itemsMap, err := getOrdersItems(ctx, rep.DB(), order.Id)
		if err != nil {
			return fmt.Errorf("get order items: %w", err)
		}

		orderItems := itemsMap[order.Id]
		if len(orderItems) == 0 {
			return fmt.Errorf("order has no items")
		}

		alreadyRefundedMap, err := getRefundedQuantitiesByOrderIds(ctx, rep.DB(), []int{order.Id})
		if err != nil {
			return fmt.Errorf("get already refunded quantities: %w", err)
		}
		alreadyRefunded := alreadyRefundedMap[order.Id]
		if alreadyRefunded == nil {
			alreadyRefunded = make(map[int]int64)
		}

		itemsToRefund, targetStatus, err := determineRefundScope(
			orderStatus.Status.Name,
			orderItems,
			orderItemIDs,
			alreadyRefunded,
		)
		if err != nil {
			return err
		}

		itemsForStock := make([]entity.OrderItemInsert, len(itemsToRefund))
		for i := range itemsToRefund {
			itemsForStock[i] = itemsToRefund[i].OrderItemInsert
		}
		history := &entity.StockHistoryParams{
			Source:    entity.StockChangeSourceOrderReturned,
			OrderId:   order.Id,
			OrderUUID: order.UUID,
		}
		if err := rep.Products().RestoreStockForProductSizes(ctx, itemsForStock, history); err != nil {
			return fmt.Errorf("restore stock: %w", err)
		}

		refundedByItem := make(map[int]int64)
		for _, r := range itemsToRefund {
			refundedByItem[r.OrderItemId] += r.OrderItemInsert.Quantity.IntPart()
		}
		if err := insertRefundedOrderItems(ctx, rep.DB(), order.Id, refundedByItem); err != nil {
			return fmt.Errorf("insert refunded order items: %w", err)
		}

		refundedAmount := refundAmountFromItems(itemsForStock, order.Currency)

		if refundShipping {
			shipment, err := getOrderShipment(ctx, rep.DB(), order.Id)
			if err != nil {
				return fmt.Errorf("get order shipment: %w", err)
			}
			if !shipment.FreeShipping {
				refundedAmount = refundedAmount.Add(shipment.CostDecimal(order.Currency))
			}
		}

		return updateOrderStatusAndAccumulateRefundedAmount(ctx, rep.DB(), order.Id, targetStatus.Status.Id, refundedAmount, reason)
	})
}

// DeliveredOrder updates order status to Delivered.
func (s *Store) DeliveredOrder(ctx context.Context, orderUUID string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUID(ctx, rep.DB(), orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		_, err = validateOrderStatus(order, entity.Confirmed, entity.Shipped)
		if err != nil {
			return fmt.Errorf("order status can be only Confirmed or Shipped: %w", err)
		}

		err = updateOrderStatus(ctx, rep.DB(), order.Id, cache.OrderStatusDelivered.Status.Id)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("can't update order payment: %w", err)
	}

	return nil
}

// CancelOrder cancels an order (admin-initiated). Restores stock and sets status to Cancelled.
func (s *Store) CancelOrder(ctx context.Context, orderUUID string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUIDForUpdate(ctx, rep.DB(), orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order: %w", err)
		}

		orderItems, err := getOrderItemsInsert(ctx, rep.DB(), order.Id)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		return cancelOrder(ctx, rep, order, orderItems, entity.StockChangeSourceOrderCancelled, "")
	})
}

// SetOrderStatusToPendingReturn sets order status to PendingReturn (admin-initiated).
func (s *Store) SetOrderStatusToPendingReturn(ctx context.Context, orderUUID string, changedBy string) error {
	pendingReturnStatus, ok := cache.GetOrderStatusByName(entity.PendingReturn)
	if !ok {
		return fmt.Errorf("can't get order status by name %s", entity.PendingReturn)
	}

	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUIDForUpdate(ctx, rep.DB(), orderUUID)
		if err != nil {
			return fmt.Errorf("get order by uuid: %w", err)
		}
		return updateOrderStatusWithValidation(ctx, rep.DB(), order.Id, pendingReturnStatus.Status.Id, changedBy, "User requested return")
	})
}

// CancelOrderByUser allows a user to cancel or request a refund for their order.
func (s *Store) CancelOrderByUser(ctx context.Context, orderUUID string, email string, reason string) (*entity.OrderFull, error) {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUIDAndEmailForUpdate(ctx, rep.DB(), orderUUID, email)
		if err != nil {
			return fmt.Errorf("order not found: %w", err)
		}

		orderStatus, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}

		currentStatus := orderStatus.Status.Name

		if currentStatus == entity.Cancelled ||
			currentStatus == entity.PendingReturn ||
			currentStatus == entity.RefundInProgress ||
			currentStatus == entity.Refunded ||
			currentStatus == entity.PartiallyRefunded {
			return fmt.Errorf("order already in refund progress or refunded: current status %s", currentStatus)
		}

		switch currentStatus {
		case entity.Placed, entity.AwaitingPayment:
			orderItems, err := getOrderItemsInsert(ctx, rep.DB(), order.Id)
			if err != nil {
				return fmt.Errorf("can't get order items: %w", err)
			}
			if err := cancelOrder(ctx, rep, order, orderItems, entity.StockChangeSourceOrderCancelled, reason); err != nil {
				return fmt.Errorf("can't cancel order: %w", err)
			}
		case entity.Confirmed:
			query := `
				UPDATE customer_order
				SET order_status_id = :orderStatusId,
					refund_reason = :refundReason
				WHERE id = :orderId`
			err = storeutil.ExecNamed(ctx, rep.DB(), query, map[string]any{
				"orderId":       order.Id,
				"orderStatusId": cache.OrderStatusRefundInProgress.Status.Id,
				"refundReason":  reason,
			})
			if err != nil {
				return fmt.Errorf("can't update order status and reason: %w", err)
			}
			if err := insertOrderStatusHistoryEntry(ctx, rep.DB(), order.Id, cache.OrderStatusRefundInProgress.Status.Id, "user", reason); err != nil {
				return fmt.Errorf("can't insert order status history: %w", err)
			}
		case entity.Shipped, entity.Delivered:
			query := `
				UPDATE customer_order
				SET order_status_id = :orderStatusId,
					refund_reason = :refundReason
				WHERE id = :orderId`
			err = storeutil.ExecNamed(ctx, rep.DB(), query, map[string]any{
				"orderId":       order.Id,
				"orderStatusId": cache.OrderStatusPendingReturn.Status.Id,
				"refundReason":  reason,
			})
			if err != nil {
				return fmt.Errorf("can't update order status and reason: %w", err)
			}
			if err := insertOrderStatusHistoryEntry(ctx, rep.DB(), order.Id, cache.OrderStatusPendingReturn.Status.Id, "user", reason); err != nil {
				return fmt.Errorf("can't insert order status history: %w", err)
			}
		default:
			return fmt.Errorf("cannot cancel order with status: %s", currentStatus)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	orderFull, err := s.repFunc().Order().GetOrderByUUIDAndEmail(ctx, orderUUID, email)
	if err != nil {
		return nil, fmt.Errorf("can't refresh order details: %w", err)
	}

	return orderFull, nil
}
