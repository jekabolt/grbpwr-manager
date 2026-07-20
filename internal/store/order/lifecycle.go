package order

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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
//
// alreadyRefunded (read from the refunded_order_item ledger under FOR UPDATE) makes this
// idempotent: full-refund paths expand only the REMAINING (not-yet-refunded) units, so a
// re-run of an already-completed refund restores zero stock.
func determineRefundScope(currentStatus entity.OrderStatusName, orderItems []entity.OrderItem, orderItemIDs []int32, alreadyRefunded map[int]int64) ([]refundItem, *cache.Status, error) {
	if currentStatus == entity.Confirmed {
		return orderItemsToRefundItems(orderItems, alreadyRefunded), &cache.OrderStatusRefunded, nil
	}

	partialItems, err := validateAndMapOrderItems(orderItems, orderItemIDs, alreadyRefunded)
	if err != nil {
		return nil, nil, err
	}

	if partialItems == nil {
		return orderItemsToRefundItems(orderItems, alreadyRefunded), &cache.OrderStatusRefunded, nil
	}

	if refundCoversFullOrder(orderItems, orderItemIDs, alreadyRefunded) {
		return orderItemsToRefundItems(orderItems, alreadyRefunded), &cache.OrderStatusRefunded, nil
	}

	return partialItems, &cache.OrderStatusPartiallyRefunded, nil
}

// orderItemsToRefundItems expands each order item into one refundItem per remaining
// (not-yet-refunded) unit. Units already recorded in the refunded_order_item ledger are
// skipped so stock is restored only once even if the refund is retried.
func orderItemsToRefundItems(orderItems []entity.OrderItem, alreadyRefunded map[int]int64) []refundItem {
	out := make([]refundItem, 0, len(orderItems))
	for _, item := range orderItems {
		remaining := item.Quantity.IntPart() - alreadyRefunded[item.Id]
		for i := int64(0); i < remaining; i++ {
			insert := item.OrderItemInsert
			insert.Quantity = decimal.NewFromInt(1)
			out = append(out, refundItem{OrderItemId: item.Id, OrderItemInsert: insert})
		}
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
		// Stamp the shipped-at time so the delivery-sync worker's timer safety net (and the
		// frontend's estimated arrival) have a reference point. Set only on the first ship, so a
		// re-ship / tracking-code correction doesn't reset the auto-deliver clock.
		if !shipment.ShippingDate.Valid {
			shipment.ShippingDate = sql.NullTime{Time: time.Now().UTC(), Valid: true}
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

// SetShipmentActualCost records the real carrier invoice (actualCost) and the optional
// return-leg cost (returnShippingCost) for an order's shipment, keyed by order UUID. These
// feed contribution-margin analytics, which otherwise falls back to the customer-charged
// carrier price (shipment.cost). Both values are base currency (EUR); an invalid
// decimal.NullDecimal clears the corresponding column. Errors if no shipment matches the UUID.
func (s *Store) SetShipmentActualCost(ctx context.Context, orderUUID string, actualCost, returnShippingCost decimal.NullDecimal) error {
	query := `
	UPDATE shipment sh
	JOIN customer_order co ON co.id = sh.order_id
	SET sh.actual_cost = :actualCost,
		sh.return_shipping_cost = :returnShippingCost
	WHERE co.uuid = :uuid`
	rows, err := storeutil.ExecNamedRows(ctx, s.DB, query, map[string]any{
		"uuid":               orderUUID,
		"actualCost":         actualCost,
		"returnShippingCost": returnShippingCost,
	})
	if err != nil {
		return fmt.Errorf("can't set shipment actual cost: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("no shipment found for order uuid %s", orderUUID)
	}
	return nil
}

// RefundOrder processes a full or partial refund for an order.
func (s *Store) RefundOrder(ctx context.Context, orderUUID string, orderItemIDs []int32, reason, reasonCode string, refundShipping bool) error {
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

		// A Confirmed-order full refund restores stock without passing through cancelOrder —
		// release the order's open packaging claims as well (WS2 residual #1): it will not ship.
		if err := releaseOpenPackagingClaims(ctx, rep.DB(), order.Id); err != nil {
			return fmt.Errorf("release packaging reservations: %w", err)
		}

		refundedByItem := make(map[int]int64)
		for _, r := range itemsToRefund {
			refundedByItem[r.OrderItemId] += r.OrderItemInsert.Quantity.IntPart()
		}
		if err := insertRefundedOrderItems(ctx, rep.DB(), order.Id, refundedByItem); err != nil {
			return fmt.Errorf("insert refunded order items: %w", err)
		}

		refundedAmount := refundAmountFromItems(itemsForStock, order.Currency)

		// Shipping is refunded at most once. order was read FOR UPDATE above, so
		// order.ShippingRefunded reflects the locked row: a second partial refund
		// with refundShipping=true skips the shipping portion (without erroring the
		// whole refund) instead of adding the shipping cost twice. Free-shipping
		// orders never add cost and so never set the marker.
		if refundShipping && !order.ShippingRefunded {
			shipment, err := getOrderShipment(ctx, rep.DB(), order.Id)
			if err != nil {
				return fmt.Errorf("get order shipment: %w", err)
			}
			if !shipment.FreeShipping {
				refundedAmount = refundedAmount.Add(shipment.CostDecimal(order.Currency))
				if err := markShippingRefunded(ctx, rep.DB(), order.Id); err != nil {
					return fmt.Errorf("mark shipping refunded: %w", err)
				}
			}
		}

		if err := updateOrderStatusAndAccumulateRefundedAmount(ctx, rep.DB(), order.Id, targetStatus.Status.Id, refundedAmount, reason, reasonCode); err != nil {
			return err
		}

		// Accounting outbox (push producer, docs/plan-accounting/03): THIS refund's amount cannot be
		// recovered later from the aggregate customer_order.refunded_amount (further refunds accumulate
		// on top of it), so it is carried in the payload. An order may be refunded several times
		// (partially_refunded), so the source_key gets a per-order sequence = count of this order's
		// existing order_refund events + 1. The count runs in the same tx, under the order's FOR UPDATE
		// lock taken above, so there is no race (03 / 09 FAQ 11). A failure rolls the refund back.
		existingRefundEvents, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
			SELECT COUNT(*) FROM acct_event
			WHERE event_type = :event_type AND source_key LIKE :prefix`,
			map[string]any{"event_type": string(entity.AcctEventOrderRefund), "prefix": order.UUID + ":%"})
		if err != nil {
			return fmt.Errorf("count acct order_refund events: %w", err)
		}
		seq := existingRefundEvents + 1
		if err := rep.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType: entity.AcctEventOrderRefund,
			SourceKey: fmt.Sprintf("%s:%d", order.UUID, seq),
			Payload: entity.AcctOrderRefundPayload{
				OrderUUID:      order.UUID,
				RefundAmount:   refundedAmount,
				OrderCurrency:  order.Currency,
				RefundedByItem: refundedByItem,
			},
			OccurredAt: s.Now(),
		}); err != nil {
			return fmt.Errorf("enqueue acct order_refund event: %w", err)
		}
		return nil
	})
}

// setShipmentDeliveredAt stamps the shipment's delivered_at (base for delivery analytics and the
// exact moment the order was marked delivered). Idempotent: a re-run overwrites with the same
// intent. A missing shipment row is not an error (0 rows affected).
func setShipmentDeliveredAt(ctx context.Context, db dependency.DB, orderId int, t time.Time) error {
	query := `UPDATE shipment SET delivered_at = :deliveredAt WHERE order_id = :orderId`
	return storeutil.ExecNamed(ctx, db, query, map[string]any{
		"orderId":     orderId,
		"deliveredAt": t,
	})
}

// deliverOrderTx marks an order delivered inside an open transaction: it validates the current
// status, transitions to Delivered (recording changedBy/notes in order_status_history), and
// stamps shipment.delivered_at. It is idempotent and reports whether THIS call performed the
// transition: an order already Delivered, or in a status from which delivery is not valid (e.g. a
// return/refund path), is a no-op returning transitioned=false without error — so the worker and
// webhook can retry freely and never fight a manual return.
func deliverOrderTx(ctx context.Context, db dependency.DB, orderUUID, changedBy, notes string, deliveredAt time.Time) (bool, error) {
	order, err := getOrderByUUIDForUpdate(ctx, db, orderUUID)
	if err != nil {
		return false, fmt.Errorf("can't get order by uuid: %w", err)
	}
	st, err := getOrderStatus(order.OrderStatusId)
	if err != nil {
		return false, err
	}
	switch st.Status.Name {
	case entity.Delivered:
		return false, nil // already delivered — nothing to do
	case entity.Confirmed, entity.Shipped:
		// eligible — fall through
	default:
		return false, nil // pending_return / refund / cancelled — do not force-deliver
	}
	if err := updateOrderStatusWithValidation(ctx, db, order.Id, cache.OrderStatusDelivered.Status.Id, changedBy, notes); err != nil {
		return false, fmt.Errorf("can't update order status: %w", err)
	}
	if err := setShipmentDeliveredAt(ctx, db, order.Id, deliveredAt); err != nil {
		return false, fmt.Errorf("can't set delivered_at: %w", err)
	}
	return true, nil
}

// DeliveredOrder marks an order delivered (manual admin / fulfillment path), attributed to
// "system". Idempotent no-op if the order is already delivered or not in a deliverable status.
func (s *Store) DeliveredOrder(ctx context.Context, orderUUID string) error {
	_, err := s.DeliverOrderWithSource(ctx, orderUUID, "system", "")
	return err
}

// DeliverOrderWithSource marks an order delivered, attributing the change to changedBy with the
// given notes, and reports whether THIS call performed the transition (true) or found the order
// already delivered / ineligible (false). Callers use the bool to send the delivered email at
// most once. Used by the delivery-sync worker (real AfterShip signal or the timer safety net) and
// the AfterShip webhook.
func (s *Store) DeliverOrderWithSource(ctx context.Context, orderUUID, changedBy, notes string) (bool, error) {
	var transitioned bool
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		transitioned, err = deliverOrderTx(ctx, rep.DB(), orderUUID, changedBy, notes, time.Now().UTC())
		return err
	})
	if err != nil {
		return false, fmt.Errorf("can't mark order delivered: %w", err)
	}
	return transitioned, nil
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
