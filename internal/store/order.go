package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

type orderStore struct {
	*MYSQLStore
}

// Order returns an object implementing order interface
func (ms *MYSQLStore) Order() dependency.Order {
	return &orderStore{
		MYSQLStore: ms,
	}
}

func validateOrderItems(ctx context.Context, rep dependency.Repository, items []entity.OrderItem) error {
	for _, item := range items {
		query := `SELECT quantity FROM product_size WHERE product_id = :productId AND size_id = :sizeId`
		availableQuantity, err := QueryNamedOne[int](ctx, rep.DB(), query, map[string]interface{}{
			"productId": item.ProductID,
			"sizeId":    item.SizeID,
		})
		if err != nil {
			return fmt.Errorf("error while getting available quantity: %w", err)
		}

		if availableQuantity < item.Quantity {
			return fmt.Errorf("Insufficient quantity for Product ID %d, Size ID %d", item.ProductID, item.SizeID)
		}
	}
	return nil
}

func calculateTotalAmount(ctx context.Context, rep dependency.Repository, items []entity.OrderItem) (decimal.Decimal, error) {
	var totalAmount decimal.Decimal

	// Build the CASE WHEN part of the SQL query for each product to align the quantities
	var caseStatements []string
	for _, item := range items {
		caseStatements = append(caseStatements, fmt.Sprintf("WHEN product.id = %d THEN %d", item.ProductID, item.Quantity))
	}

	caseSQL := strings.Join(caseStatements, " ")

	// Build the SQL query
	query := fmt.Sprintf(`
		SELECT SUM((price * (1 - sale_percentage / 100)) * CASE %s END)
		FROM product
		WHERE id IN (%s)
	`,
		caseSQL,
		strings.Join(strings.Fields(fmt.Sprint(items)), ","),
	)
	totalAmount, err := QueryNamedOne[decimal.Decimal](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return decimal.Zero, fmt.Errorf("error while calculating total amount: %w", err)
	}

	return totalAmount, nil
}

func insertAddresses(ctx context.Context, rep dependency.Repository, shippingAddress, billingAddress *entity.Address) (int, int, error) {
	var shippingID, billingID int64
	query := `
		INSERT INTO address (street, house_number, apartment_number, city, state, country, postal_code) 
		VALUES (:street, :house_number, :apartment_number, :city, :state, :country, :postal_code)`

	if *shippingAddress == *billingAddress {
		// If shipping and billing addresses are the same, insert only once
		result, err := rep.DB().NamedExecContext(ctx, query, shippingAddress)
		if err != nil {
			return 0, 0, err
		}
		shippingID, err = result.LastInsertId()
		if err != nil {
			return 0, 0, err
		}
		billingID = shippingID
	} else {
		// If they are different, insert both
		result, err := rep.DB().NamedExecContext(ctx, query, shippingAddress)
		if err != nil {
			return 0, 0, err
		}
		shippingID, err = result.LastInsertId()
		if err != nil {
			return 0, 0, err
		}

		result, err = rep.DB().NamedExecContext(ctx, query, billingAddress)
		if err != nil {
			return 0, 0, err
		}
		billingID, err = result.LastInsertId()
		if err != nil {
			return 0, 0, err
		}
	}
	return int(shippingID), int(billingID), nil
}

func insertBuyer(ctx context.Context, rep dependency.Repository, b *entity.Buyer) (int, error) {
	var buyerID int
	query := `
	INSERT INTO buyer 
	(first_name, last_name, email, phone, receive_promo_emails, billing_address_id, shipping_address_id)
	VALUES (:first_name, :last_name, :email, :phone, :receive_promo_emails, :billing_address_id, :shipping_address_id)
	`
	result, err := rep.DB().NamedExecContext(ctx, query, b)
	if err != nil {
		return 0, err
	}
	lastID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	buyerID = int(lastID)
	return buyerID, nil
}

func insertPaymentRecord(ctx context.Context, rep dependency.Repository, paymentMethod *entity.PaymentMethod) (int, error) {
	// Check if the payment method ID exists
	query :=
		`SELECT EXISTS (SELECT 1 FROM payment_method WHERE id = :paymentMethodId)`

	params := map[string]interface{}{
		"paymentMethodId": paymentMethod.ID,
	}

	exists, err := QueryNamedOne[bool](ctx, rep.DB(), query, params)
	if err != nil {
		return 0, fmt.Errorf("can't get payment method by id: %w", err)
	}
	if !exists {
		return 0, fmt.Errorf("payment method ID does not exist")
	}

	insertQuery := `
		INSERT INTO payment (payment_method_id, transaction_amount, is_transaction_done)
		VALUES (:paymentMethodId, 0, false);
	`

	paymentID, err := ExecNamedLastId(ctx, rep.DB(), insertQuery, params)
	if err != nil {
		return 0, fmt.Errorf("can't insert payment record: %w", err)
	}

	return paymentID, nil
}

func insertOrderItems(ctx context.Context, rep dependency.Repository, items []entity.OrderItem, orderID int) error {
	if len(items) == 0 {
		return fmt.Errorf("no order items to insert")
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row := map[string]any{
			"order_id":   orderID,
			"product_id": item.ProductID,
			"quantity":   item.Quantity,
			"size_id":    item.SizeID,
		}
		rows = append(rows, row)
	}

	return BulkInsert(ctx, rep.DB(), "order_item", rows)
}

func insertShipment(ctx context.Context, rep dependency.Repository, sc *entity.ShipmentCarrier) (int, error) {
	query := `
	INSERT INTO shipment (carrier_id)
	VALUES (:carrierId)
	`
	id, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"carrierId": sc.Carrier,
	})
	if err != nil {
		return 0, fmt.Errorf("can't insert shipment: %w", err)
	}
	return id, nil
}

func insertOrder(ctx context.Context, rep dependency.Repository, order *entity.Order) (int, error) {
	var err error
	query := `
	INSERT INTO order
	 (buyer_id, placed, payment_id, shipment_id, total_price, order_status_id, promo_id)
	 VALUES (:buyerId, :placed, :paymentId, :shipmentId, :totalPrice, :orderStatusId, :promoId)
	 `
	order.ID, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"buyerId":       order.BuyerID,
		"placed":        order.Placed,
		"paymentId":     order.PaymentID,
		"totalPrice":    order.TotalPrice,
		"orderStatusId": order.OrderStatusID,
		"promoId":       order.PromoID,
	})
	if err != nil {
		return 0, fmt.Errorf("can't insert order: %w", err)
	}
	return order.ID, nil
}

// mergeOrderItems maps the order items by ProductID and SizeID
func mergeOrderItems(items []entity.OrderItem) []entity.OrderItem {
	// Create a map with a key as ProductID + SizeID and value as OrderItem
	mappedItems := make(map[string]entity.OrderItem)

	for _, item := range items {
		// Create a unique key for each ProductID and SizeID combination
		key := fmt.Sprintf("%d_%d", item.ProductID, item.SizeID)

		// If this key exists, update the Quantity
		if existingItem, exists := mappedItems[key]; exists {
			existingItem.Quantity += item.Quantity
			mappedItems[key] = existingItem
		} else {
			// Else add a new item to the map
			mappedItems[key] = item
		}
	}

	// Convert map values to a slice
	var aggregatedItems []entity.OrderItem
	for _, item := range mappedItems {
		aggregatedItems = append(aggregatedItems, item)
	}

	return aggregatedItems
}

func (ms *MYSQLStore) CreateOrder(ctx context.Context,
	items []entity.OrderItem,
	shippingAddress *entity.Address,
	billingAddress *entity.Address,
	buyer *entity.Buyer,
	paymentMethodId int,
	shipmentCarrierId int,
	promoCode string,
) (*entity.Order, error) {

	order := &entity.Order{}
	ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		items = mergeOrderItems(items)

		err := validateOrderItems(ctx, rep, items)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}

		err = rep.Products().ReduceStockForProductSizes(ctx, items)
		if err != nil {
			return fmt.Errorf("error while reducing stock for product sizes: %w", err)
		}

		prdIds := []int{}
		for _, i := range items {
			prdIds = append(prdIds, i.ProductID)
		}

		total, err := calculateTotalAmount(ctx, rep, items)
		if err != nil {
			return fmt.Errorf("error while calculating total amount: %w", err)
		}

		promo, ok := ms.cache.GetPromoByName(promoCode)
		if !ok {
			promo = entity.PromoCode{}
		}
		// check if promo is allowed and not expired
		if !promo.Allowed && promo.Expiration < time.Now().Unix() {
			promo = entity.PromoCode{}
		}

		shipmentCarrier, ok := ms.cache.GetShipmentCarrierByID(shipmentCarrierId)
		if !ok {
			return fmt.Errorf("shipment carrier is not exists")
		}

		if !promo.FreeShipping {
			total = total.Add(shipmentCarrier.Price)
		}

		shipmentId, err := insertShipment(ctx, rep, shipmentCarrier)
		if err != nil {
			return fmt.Errorf("error while inserting shipment: %w", err)
		}

		if !promo.Discount.Equals(decimal.Zero) {
			total = total.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
		}

		shippingAddressId, billingAddressId, err := insertAddresses(ctx, rep, shippingAddress, billingAddress)
		if err != nil {
			return fmt.Errorf("error while inserting addresses: %w", err)
		}

		buyer.ShippingAddressID = shippingAddressId
		buyer.BillingAddressID = billingAddressId
		buyerID, err := insertBuyer(ctx, rep, buyer)
		if err != nil {
			return fmt.Errorf("error while inserting buyer: %w", err)
		}

		paymentMethod, ok := ms.cache.GetPaymentMethodByID(paymentMethodId)
		if !ok {
			return fmt.Errorf("payment method is not exists")
		}

		paymentID, err := insertPaymentRecord(ctx, rep, paymentMethod)
		if err != nil {
			return fmt.Errorf("error while inserting payment record: %w", err)
		}

		placed, _ := ms.cache.GetOrderStatusByName(entity.Placed)

		order = &entity.Order{
			BuyerID:       buyerID,
			PaymentID:     paymentID,
			TotalPrice:    total,
			PromoID:       promo.ID,
			ShipmentId:    shipmentId,
			OrderStatusID: placed.ID,
		}

		orderId, err := insertOrder(ctx, rep, order)
		if err != nil {
			return fmt.Errorf("error while inserting final order: %w", err)
		}
		err = insertOrderItems(ctx, rep, items, orderId)
		if err != nil {
			return fmt.Errorf("error while inserting order items: %w", err)
		}

		return nil
	})

	return order, nil
}

func getOrderItems(ctx context.Context, rep dependency.Repository, orderId int) ([]entity.OrderItem, error) {
	query := `SELECT id, order_id, product_id, quantity, size_id FROM order_item WHERE order_id = :orderId`
	ois, err := QueryListNamed[entity.OrderItem](ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order items: %w", err)
	}
	return ois, nil
}

func getOrderShipment(ctx context.Context, rep dependency.Repository, orderId int) (*entity.Shipment, error) {
	query := `
	SELECT 
		s.id, s.created_at, s.updated_at, s.carrier_id, s.tracking_code, s.shipping_date, s.estimated_arrival_date 
	FROM shipment s 
	INNER JOIN customer_order co
		ON s.id = o.shipment_id 
	WHERE co.id = :orderId`

	s, err := QueryNamedOne[entity.Shipment](ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order shipment: %w", err)
	}
	return &s, nil
}

func updateOrderTotalPromo(ctx context.Context, rep dependency.Repository, orderId int, promoId int) error {
	query := `
	UPDATE order
	SET promo_id = :promoId,
		total_price = :totalPrice
	WHERE id = :orderId`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return fmt.Errorf("can't update order total promo: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) ApplyPromoCode(ctx context.Context, orderId int, promoCode string) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		promo, ok := ms.cache.GetPromoByName(promoCode)
		if !ok {
			return fmt.Errorf("promo code is not valid")
		}
		if !promo.FreeShipping && promo.Discount.Equals(decimal.Zero) {
			return fmt.Errorf("promo code is not valid")
		}

		items, err := getOrderItems(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		// total no promo
		total, err := calculateTotalAmount(ctx, rep, items)
		if err != nil {
			return fmt.Errorf("can't get total order amount: %w", err)
		}

		shipment, err := getOrderShipment(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order shipment: %w", err)
		}

		shipmentCarrier, ok := ms.cache.GetShipmentCarrierByID(shipment.CarrierID)
		if err != nil {
			return fmt.Errorf("error while getting shipment carrier by id: %w", err)
		}

		if !promo.FreeShipping {
			total = total.Add(shipmentCarrier.Price)
		}

		total = total.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))

		err = updateOrderTotalPromo(ctx, rep, int(orderId), promo.ID)
		if err != nil {
			return fmt.Errorf("can't update order total promo: %w", err)
		}

		return nil
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("can't apply promo code: %w", err)
	}
	return total, nil
}

func getOrderPromo(ctx context.Context, rep dependency.Repository, orderId int) (*entity.PromoCode, error) {
	query := `
	SELECT 
		pc.id, pc.code, pc.free_shipping, pc.discount, pc.expiration, pc.allowed 
	FROM promo_code AS pc 
	INNER JOIN customer_order AS co ON 
		co.promo_id = pc.id 
	WHERE co.id = :orderId AND pc.expiration >= NOW() AND pc.allowed = 1`

	promo, err := QueryNamedOne[entity.PromoCode](ctx, rep.DB(), query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}

	return &promo, nil
}

func getOrderShipmentCarrier(ctx context.Context, rep dependency.Repository, orderId int) (*entity.ShipmentCarrier, error) {
	query := `
	SELECT
		sc.id, sc.carrier, sc.price, sc.allowed
	FROM shipment_carrier AS sc
	INNER JOIN shipment AS s ON	
		s.carrier_id = sc.id
	INNER JOIN customer_order AS co ON
		co.shipment_id = s.id
	WHERE co.id = :orderId`

	carrier, err := QueryNamedOne[entity.ShipmentCarrier](ctx, rep.DB(), query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order shipment carrier: %w", err)
	}
	return &carrier, nil
}

// UpdateOrderItems update order items
func (ms *MYSQLStore) UpdateOrderItems(ctx context.Context, orderId int, items []entity.OrderItem) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		// TODO: ?? delete all order if len items == 0

		items = mergeOrderItems(items)

		query := `DELETE FROM order_item WHERE order_id = :orderId`
		err := ExecNamed(ctx, rep.DB(), query, map[string]any{
			"orderId": orderId,
		})
		if err != nil {
			return fmt.Errorf("can't delete order items: %w", err)
		}

		err = insertOrderItems(ctx, rep, items, orderId)
		if err != nil {
			return fmt.Errorf("can't insert order items: %w", err)
		}

		total, err := calculateTotalAmount(ctx, rep, items)
		if err != nil {
			return fmt.Errorf("can't get total order amount: %w", err)
		}

		promo, err := getOrderPromo(ctx, rep, orderId)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("error while getting promo by code: %w", err)
			}
			promo = &entity.PromoCode{}
		}

		shipmentCarrier, err := getOrderShipmentCarrier(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order shipment: %w", err)
		}

		if !promo.FreeShipping {
			total = total.Add(shipmentCarrier.Price)
		}

		if !promo.Discount.Equals(decimal.Zero) {
			total = total.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
		}

		err = updateOrderTotalPromo(ctx, rep, orderId, promo.ID)
		if err != nil {
			return fmt.Errorf("can't update order total promo: %w", err)
		}

		return nil
	})
}

func getOrderTotalPrice(ctx context.Context, rep dependency.Repository, orderId int) (decimal.Decimal, error) {
	query := `
	SELECT total_price FROM customer_order WHERE id = :orderId`
	total, err := QueryNamedOne[decimal.Decimal](ctx, rep.DB(), query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("can't get order total price: %w", err)
	}
	return total, nil
}

func updateOrderShipping(ctx context.Context, rep dependency.Repository, orderId int, newShipmentCarrier *entity.ShipmentCarrier) error {
	query := `UPDATE shipment SET carrier_id = :carrierId WHERE id = (SELECT shipment_id FROM customer_order WHERE id = :orderId)`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":   orderId,
		"carrierId": newShipmentCarrier.ID,
	})
	if err != nil {
		return fmt.Errorf("can't update order shipping: %w", err)
	}
	return nil
}

// UpdateOrderShippingCarrier is used to update the shipping carrier of an order and total price if changed.
func (ms *MYSQLStore) UpdateOrderShippingCarrier(ctx context.Context, orderId int, shipmentCarrierId int) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		orderShipmentCarrier, err := getOrderShipmentCarrier(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order shipment: %w", err)
		}
		if orderShipmentCarrier.ID == shipmentCarrierId {
			return nil
		}

		newShipmentCarrier, ok := ms.cache.GetShipmentCarrierByID(shipmentCarrierId)
		if !ok {
			return fmt.Errorf("shipment carrier is not exists")
		}

		promo, err := getOrderPromo(ctx, rep, orderId)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("error while getting promo by code: %w", err)
			}
			promo = &entity.PromoCode{}
		}

		total, err := getOrderTotalPrice(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order total price: %w", err)
		}

		if !promo.FreeShipping {
			total = total.Add(newShipmentCarrier.Price).Sub(orderShipmentCarrier.Price)
		}

		err = updateOrderShipping(ctx, rep, orderId, newShipmentCarrier)
		if err != nil {
			return fmt.Errorf("error while inserting shipment: %w", err)
		}

		err = updateOrderTotalPromo(ctx, rep, orderId, promo.ID)
		if err != nil {
			return fmt.Errorf("can't update order total promo: %w", err)
		}

		return nil
	})
}

func getOrderById(ctx context.Context, rep dependency.Repository, orderId int) (*entity.Order, error) {
	query := `
	SELECT * from customer_order WHERE id = :orderId`
	order, err := QueryNamedOne[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}
	return &order, nil
}

func updateOrderStatus(ctx context.Context, rep dependency.Repository, orderId int, orderStatusId int) error {
	query := `UPDATE order SET order_status_id = :orderStatusId WHERE id = :orderId`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":       orderId,
		"orderStatusId": orderStatusId,
	})
	if err != nil {
		return fmt.Errorf("can't update order status: %w", err)
	}
	return nil
}

func updateOrderPayment(ctx context.Context, rep dependency.Repository, paymentId int, payment *entity.Payment) error {
	query := `
	UPDATE payment 
	SET transaction_amount = :transactionAmount,
		transaction_id = :transactionId,
		is_transaction_done = :isTransactionDone,
		payment_method_id = :paymentMethodId,
		payer = :payer,
		payee = :payee
	WHERE id = :paymentId`

	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"transactionAmount": payment.TransactionAmount,
		"transactionId":     payment.TransactionID,
		"isTransactionDone": payment.IsTransactionDone,
		"paymentMethodId":   payment.PaymentMethodID,
		"payer":             payment.Payer,
		"payee":             payment.Payee,
	})

	if err != nil {
		return fmt.Errorf("can't update order payment: %w", err)
	}
	return nil
}

// OrderPaymentDone updates the payment status of an order and adds payment info to order.
func (ms *MYSQLStore) OrderPaymentDone(ctx context.Context, orderId int, payment *entity.Payment) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		order, err := getOrderById(ctx, rep, orderId)
		if err != nil {
			return err
		}
		orderStatus, ok := ms.cache.GetOrderStatusByID(order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		}

		if orderStatus.Name != entity.Placed {
			return fmt.Errorf("order status is not placed: order status %s", orderStatus.Name)
		}

		if payment.TransactionAmount.LessThan(order.TotalPrice) {
			return fmt.Errorf("payment amount is less than order total price: %s", payment.TransactionAmount.String())
		}

		_, ok = ms.cache.GetPaymentMethodByID(payment.PaymentMethodID)
		if !ok {
			return fmt.Errorf("payment method is not exists: payment method id %d", payment.PaymentMethodID)
		}

		statusPlaced, ok := ms.cache.GetOrderStatusByName(entity.Placed)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Placed)
		}

		err = updateOrderStatus(ctx, rep, orderId, statusPlaced.ID)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		err = updateOrderPayment(ctx, rep, order.PaymentID, payment)
		if err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("can't update order payment: %w", err)
	}

	return nil
}

func updateOrderShipment(ctx context.Context, rep dependency.Repository, orderId int, shipment *entity.Shipment) error {
	query := `
	UPDATE shipment
	SET 
		tracking_code = :trackingCode,
		shipping_date = :shippingDate,
		estimated_arrival_date = :estimatedArrivalDate
		carrier_id = :carrierId
		shipping_date = :shippingDate
	WHERE id = (SELECT shipment_id FROM customer_order WHERE id = :orderId)`

	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":              orderId,
		"carrierId":            shipment.CarrierID,
		"trackingCode":         shipment.TrackingCode,
		"shippingDate":         shipment.ShippingDate,
		"estimatedArrivalDate": shipment.EstimatedArrivalDate,
	})
	if err != nil {
		return fmt.Errorf("can't get order shipment: %w", err)
	}
	return nil
}

// UpdateShippingStatus updates the shipping status of an order.
func (ms *MYSQLStore) UpdateShippingInfo(ctx context.Context, orderId int, shipment *entity.Shipment) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// TODO: ?? check if order status is confirmed, shipped, delivered or refunded
		// order, err := getOrderById(ctx, rep, orderId)
		// if err != nil {
		// 	return err
		// }

		// orderStatus, ok := ms.cache.GetOrderStatusByID(order.OrderStatusID)
		// if !ok {
		// 	return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		// }

		// if orderStatus.Name != entity.Confirmed ||
		// 	orderStatus.Name != entity.Shipped ||
		// 	orderStatus.Name != entity.Delivered ||
		// 	orderStatus.Name != entity.Refunded {
		// 	return fmt.Errorf("order status is not confirmed, shipped, delivered or refunded: order status %s", orderStatus.Name)
		// }

		_, ok := ms.cache.GetShipmentCarrierByID(shipment.CarrierID)
		if !ok {
			return fmt.Errorf("shipment carrier is not exists: shipment carrier id %d", shipment.CarrierID)
		}
		err := updateOrderShipment(ctx, rep, orderId, shipment)
		if err != nil {
			return fmt.Errorf("can't update order shipment: %w", err)
		}

		statusShipped, ok := ms.cache.GetOrderStatusByName(entity.Shipped)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Shipped)
		}

		err = updateOrderStatus(ctx, rep, orderId, statusShipped.ID)
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

func getPaymentById(ctx context.Context, rep dependency.Repository, paymentId int) (*entity.Payment, error) {
	query := `
	SELECT * FROM payment WHERE id = :paymentId`
	payment, err := QueryNamedOne[entity.Payment](ctx, rep.DB(), query, map[string]interface{}{
		"paymentId": paymentId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get payment by id: %w", err)
	}
	return &payment, nil
}

func getBuyerById(ctx context.Context, rep dependency.Repository, buyerId int) (*entity.Buyer, error) {
	query := `
	SELECT * FROM buyer WHERE id = :buyerId`
	buyer, err := QueryNamedOne[entity.Buyer](ctx, rep.DB(), query, map[string]interface{}{
		"buyerId": buyerId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get buyer by id: %w", err)
	}
	return &buyer, nil
}

func getAddressId(ctx context.Context, rep dependency.Repository, addressId int) (entity.Address, error) {
	query := `
	SELECT * FROM address WHERE id = :addressId`
	address, err := QueryNamedOne[entity.Address](ctx, rep.DB(), query, map[string]interface{}{
		"addressId": addressId,
	})
	if err != nil {
		return entity.Address{}, fmt.Errorf("can't get address by id: %w", err)
	}
	return address, nil
}

func (ms *MYSQLStore) fetchOrderInfo(ctx context.Context, order *entity.Order) (*entity.OrderInfo, error) {
	orderItems, err := getOrderItems(ctx, ms, order.ID)
	if err != nil {
		return nil, fmt.Errorf("can't get order items: %w", err)
	}

	payment, err := getPaymentById(ctx, ms, order.PaymentID)
	if err != nil {
		return nil, fmt.Errorf("can't get payment by id: %w", err)
	}

	paymentMethod, ok := ms.cache.GetPaymentMethodByID(payment.PaymentMethodID)
	if !ok {
		return nil, fmt.Errorf("payment method is not exists")
	}

	shipment, err := getOrderShipment(ctx, ms, order.ID)
	if err != nil {
		return nil, fmt.Errorf("can't get order shipment: %w", err)
	}
	shipmentCarrier, ok := ms.cache.GetShipmentCarrierByID(shipment.CarrierID)
	if !ok {
		return nil, fmt.Errorf("shipment carrier is not exists")
	}

	promo := &entity.PromoCode{}
	if order.PromoID != 0 {
		promo, ok = ms.cache.GetPromoByID(order.PromoID)
		if !ok {
			return nil, fmt.Errorf("promo code is not exists")
		}
	}

	orderStatus, ok := ms.cache.GetOrderStatusByID(order.OrderStatusID)
	if !ok {
		return nil, fmt.Errorf("order status is not exists")
	}

	buyer, err := getBuyerById(ctx, ms, order.BuyerID)
	if err != nil {
		return nil, fmt.Errorf("can't get buyer by id: %w", err)
	}

	shippingAddress, err := getAddressId(ctx, ms, buyer.ShippingAddressID)
	if err != nil {
		return nil, fmt.Errorf("can't get shipping address by id: %w", err)
	}
	billingAddress, err := getAddressId(ctx, ms, buyer.BillingAddressID)
	if err != nil {
		return nil, fmt.Errorf("can't get billing address by id: %w", err)
	}

	orderInfo := entity.OrderInfo{
		Order:           order,
		OrderItems:      orderItems,
		Payment:         payment,
		PaymentMethod:   paymentMethod,
		Shipment:        shipment,
		ShipmentCarrier: shipmentCarrier,
		PromoCode:       promo,
		OrderStatus:     orderStatus,
		Buyer:           buyer,
		Billing:         &billingAddress,
		Shipping:        &shippingAddress,
		Placed:          order.Placed,
		Modified:        order.Modified,
		TotalPrice:      order.TotalPrice,
	}

	return &orderInfo, nil
}

// GetOrderItems retrieves all order items for a given order.
func (ms *MYSQLStore) GetOrderById(ctx context.Context, orderId int) (*entity.OrderInfo, error) {

	order, err := getOrderById(ctx, ms, orderId)
	if err != nil {
		return nil, err
	}

	return ms.fetchOrderInfo(ctx, order)
}

func getOrdersByEmail(ctx context.Context, rep dependency.Repository, email string) ([]entity.Order, error) {
	query := `
	SELECT 
		co.id,
		co.buyer_id,
		co.placed,
		co.modified,
		co.payment_id,
		co.total_price,
		co.order_status_id,
		co.shipment_id,
		co.promo_id
	FROM buyer b 
	INNER JOIN customer_order co 
		ON b.id = co.buyer_id 
	WHERE b.email = :email
	`
	orders, err := QueryListNamed[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"email": email,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get orders by email: %w", err)
	}

	return orders, nil
}

func (ms *MYSQLStore) GetOrdersByEmail(ctx context.Context, email string) ([]entity.OrderInfo, error) {
	orders, err := getOrdersByEmail(ctx, ms, email)
	if err != nil {
		return nil, err
	}

	var ordersInfo []entity.OrderInfo
	for _, order := range orders {
		orderInfo, err := ms.fetchOrderInfo(ctx, &order)
		if err != nil {
			return nil, err
		}
		ordersInfo = append(ordersInfo, *orderInfo)
	}

	return ordersInfo, nil
}

func getOrdersByStatus(ctx context.Context, rep dependency.Repository, orderStatusId int) ([]entity.Order, error) {
	query := `
	SELECT 
		co.id,
		co.buyer_id,
		co.placed,
		co.modified,
		co.payment_id,
		co.total_price,
		co.order_status_id,
		co.shipment_id,
		co.promo_id
	FROM customer_order co 
	WHERE order_status_id = :status
	`

	orders, err := QueryListNamed[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"status": orderStatusId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get orders by status: %w", err)
	}

	return orders, nil
}

func (ms *MYSQLStore) GetOrdersByStatus(ctx context.Context, status entity.OrderStatusName) ([]entity.OrderInfo, error) {
	os, ok := ms.cache.GetOrderStatusByName(status)
	if !ok {
		return nil, fmt.Errorf("order status is not exists: order status id %v", status)
	}

	orders, err := getOrdersByStatus(ctx, ms, os.ID)
	if err != nil {
		return nil, err
	}

	var ordersInfo []entity.OrderInfo
	for _, order := range orders {
		orderInfo, err := ms.fetchOrderInfo(ctx, &order)
		if err != nil {
			return nil, err
		}
		ordersInfo = append(ordersInfo, *orderInfo)
	}

	return ordersInfo, nil
}

func (ms *MYSQLStore) RefundOrder(ctx context.Context, orderId int) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		//TODO: ?? check if status
		// order, err := getOrderById(ctx, rep, orderId)
		// if err != nil {
		// 	return err
		// }

		// orderStatus, ok := ms.cache.GetOrderStatusByID(order.OrderStatusID)
		// if !ok {
		// 	return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		// }

		// if orderStatus.Name != entity.Confirmed ||
		// 	orderStatus.Name != entity.Shipped ||
		// 	orderStatus.Name != entity.Delivered {
		// 	return fmt.Errorf("order status is not confirmed, shipped or delivered: order status %s", orderStatus.Name)
		// }

		statusShipped, ok := ms.cache.GetOrderStatusByName(entity.Refunded)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Refunded)
		}

		err := updateOrderStatus(ctx, rep, orderId, statusShipped.ID)
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

func (ms *MYSQLStore) DeliveredOrder(ctx context.Context, orderId int) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		//TODO: ?? check if status
		// order, err := getOrderById(ctx, rep, orderId)
		// if err != nil {
		// 	return err
		// }

		// orderStatus, ok := ms.cache.GetOrderStatusByID(order.OrderStatusID)
		// if !ok {
		// 	return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		// }

		// if orderStatus.Name != entity.Confirmed ||
		// 	orderStatus.Name != entity.Shipped ||
		// 	orderStatus.Name != entity.Delivered ||
		// 	orderStatus.Name != entity.Refunded {
		// 	return fmt.Errorf("order status is not confirmed, shipped or delivered: order status %s", orderStatus.Name)
		// }

		statusRefunded, ok := ms.cache.GetOrderStatusByName(entity.Refunded)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Refunded)
		}

		err := updateOrderStatus(ctx, rep, orderId, statusRefunded.ID)
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

func (ms *MYSQLStore) CancelOrder(ctx context.Context, orderId int) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// order, err := getOrderById(ctx, rep, orderId)
		// if err != nil {
		// 	return err
		// }

		// orderStatus, ok := ms.cache.GetOrderStatusByID(order.OrderStatusID)
		// if !ok {
		// 	return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		// }

		// TODO: ?? check if status
		// if orderStatus.Name != entity.Confirmed ||
		// 	orderStatus.Name != entity.Shipped ||
		// 	orderStatus.Name != entity.Delivered ||
		// 	orderStatus.Name != entity.Cancelled ||
		// 	orderStatus.Name != entity.Refunded {
		// 	return fmt.Errorf("order status is not confirmed, shipped, delivered, cancelled or refunded: order status %s", orderStatus.Name)
		// }

		items, err := getOrderItems(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		err = rep.Products().RestoreStockForProductSizes(ctx, items)
		if err != nil {
			return fmt.Errorf("can't restore stock for product sizes: %w", err)
		}

		statusCancelled, ok := ms.cache.GetOrderStatusByName(entity.Cancelled)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Refunded)
		}

		err = updateOrderStatus(ctx, rep, orderId, statusCancelled.ID)
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
