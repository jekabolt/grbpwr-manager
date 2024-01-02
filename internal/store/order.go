package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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

// validateOrderItems returns a slice of order items that are available in stock
func validateOrderItems(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert) ([]entity.OrderItemInsert, error) {
	oii := []entity.OrderItemInsert{}
	for _, item := range items {
		query := `SELECT * FROM product_size WHERE product_id = :productId AND size_id = :sizeId`
		ps, err := QueryNamedOne[entity.ProductSize](ctx, rep.DB(), query, map[string]interface{}{
			"productId": item.ProductID,
			"sizeId":    item.SizeID,
		})
		if err != nil {
			return nil, fmt.Errorf("error while getting available quantity: %w", err)
		}

		// if the quantity is greater than or equal to the available quantity, add it to the slice
		if ps.Quantity.GreaterThanOrEqual(item.Quantity) {
			oii = append(oii, item)
			continue
		}
		// if the quantity is less than the available quantity, add the item with the available quantity to the slice
		if !ps.Quantity.IsZero() && ps.Quantity.LessThan(item.Quantity) {
			item.Quantity = ps.Quantity
			oii = append(oii, item)
		}
	}

	return oii, nil
}

// compareItems return true if items are equal
func compareItems(items []entity.OrderItemInsert, validItems []entity.OrderItemInsert) bool {
	if len(items) != len(validItems) {
		return false
	}
	for i := range items {
		if items[i] != validItems[i] {
			return false
		}
	}
	return true
}

func calculateTotalAmount[T entity.ProductInfoProvider](ctx context.Context, rep dependency.Repository, items []T) (decimal.Decimal, error) {
	if len(items) == 0 {
		return decimal.Zero, errors.New("no items to calculate total amount")
	}

	// this made to calculate total amount without matter of size
	// cause it going to be merged mapped by product id and we'll got correct total amount
	itemsNoSizeID := make([]entity.OrderItemInsert, 0, len(items))
	for _, item := range items {
		itemsNoSizeID = append(itemsNoSizeID, entity.OrderItemInsert{
			ProductID: item.GetProductID(),
			Quantity:  item.GetQuantity(),
		})
	}
	itemsNoSizeID = mergeOrderItems(itemsNoSizeID)

	var (
		caseStatements []string
		productIDs     []string
	)

	for _, item := range itemsNoSizeID {
		productID := item.GetProductID()
		quantity := item.GetQuantity()
		if !quantity.IsPositive() { // Ensure that the quantity is a positive number
			return decimal.Zero, fmt.Errorf("quantity for product ID %d is not positive", productID)
		}
		caseStatements = append(caseStatements, fmt.Sprintf("WHEN product.id = %d THEN %s", productID, quantity.String()))
		productIDs = append(productIDs, fmt.Sprintf("%d", productID))
	}

	caseSQL := strings.Join(caseStatements, " ")
	idsSQL := strings.Join(productIDs, ", ")

	query := fmt.Sprintf(`
		SELECT SUM(price * (1 - sale_percentage / 100) * CASE %s END) AS total_amount
		FROM product
		WHERE id IN (%s)
	`, caseSQL, idsSQL)

	var totalAmount decimal.Decimal
	err := rep.DB().GetContext(ctx, &totalAmount, query)
	if err != nil {
		return decimal.Zero, fmt.Errorf("error while calculating total amount: %w", err)
	}

	return totalAmount, nil
}

func insertAddresses(ctx context.Context, rep dependency.Repository, shippingAddress, billingAddress *entity.AddressInsert) (int, int, error) {
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

func insertBuyer(ctx context.Context, rep dependency.Repository, b *entity.BuyerInsert, sAdr, bAdr int) (int, error) {
	var buyerID int
	query := `
	INSERT INTO buyer 
	(first_name, last_name, email, phone, receive_promo_emails, billing_address_id, shipping_address_id)
	VALUES (:firstName, :lastName, :email, :phone, :receivePromoEmails, :billingAddressId, :shippingAddressId)
	`
	buyer := entity.Buyer{
		BuyerInsert:       *b,
		BillingAddressID:  bAdr,
		ShippingAddressID: sAdr,
	}

	buyerID, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"firstName":          buyer.FirstName,
		"lastName":           buyer.LastName,
		"email":              buyer.Email,
		"phone":              buyer.Phone,
		"receivePromoEmails": buyer.ReceivePromoEmails,
		"billingAddressId":   buyer.BillingAddressID,
		"shippingAddressId":  buyer.ShippingAddressID,
	})
	if err != nil {
		return 0, fmt.Errorf("can't insert buyer: %w", err)
	}

	return buyerID, nil
}

func insertPaymentRecord(ctx context.Context, rep dependency.Repository, paymentMethod *entity.PaymentMethod) (int, error) {

	insertQuery := `
		INSERT INTO payment (payment_method_id, transaction_amount, is_transaction_done)
		VALUES (:paymentMethodId, 0, false);
	`

	paymentID, err := ExecNamedLastId(ctx, rep.DB(), insertQuery, map[string]interface{}{
		"paymentMethodId": paymentMethod.ID,
	})
	if err != nil {
		return 0, fmt.Errorf("can't insert payment record: %w", err)
	}

	return paymentID, nil
}

func insertOrderItems(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, orderID int) error {
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

func deleteOrderItems(ctx context.Context, rep dependency.Repository, orderId int) error {
	query := `DELETE FROM order_item WHERE order_id = :orderId`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return fmt.Errorf("can't delete order items: %w", err)
	}
	return nil
}

func insertShipment(ctx context.Context, rep dependency.Repository, sc *entity.ShipmentCarrier) (int, error) {
	query := `
	INSERT INTO shipment (carrier_id)
	VALUES (:carrierId)
	`
	id, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"carrierId": sc.ID,
	})
	if err != nil {
		return 0, fmt.Errorf("can't insert shipment: %w", err)
	}
	return id, nil
}

func insertOrder(ctx context.Context, rep dependency.Repository, order *entity.Order) (int, string, error) {
	var err error
	query := `
	INSERT INTO customer_order
	 (uuid, buyer_id, payment_id, shipment_id, total_price, order_status_id, promo_id)
	 VALUES (:uuid, :buyerId, :paymentId, :shipmentId, :totalPrice, :orderStatusId, :promoId)
	 `

	uuid := uuid.New().String()
	order.ID, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"uuid":          uuid,
		"buyerId":       order.BuyerID,
		"paymentId":     order.PaymentID,
		"shipmentId":    order.ShipmentId,
		"totalPrice":    order.TotalPrice,
		"orderStatusId": order.OrderStatusID,
		"promoId":       order.PromoID,
	})
	if err != nil {
		return 0, "", fmt.Errorf("can't insert order: %w", err)
	}
	return order.ID, uuid, nil
}

// mergeOrderItems maps the order items by ProductID and SizeID
func mergeOrderItems(items []entity.OrderItemInsert) []entity.OrderItemInsert {
	mergedItems := make(map[string]entity.OrderItemInsert)

	for _, item := range items {
		if item.Quantity.IsZero() {
			continue // Skip items with zero quantity
		}
		key := fmt.Sprintf("%d-%d", item.ProductID, item.SizeID)
		if existingItem, ok := mergedItems[key]; ok {
			existingItem.Quantity = existingItem.Quantity.Add(item.Quantity)
			mergedItems[key] = existingItem
		} else {
			mergedItems[key] = item
		}
	}

	// Convert the map back into a slice
	var mergedSlice []entity.OrderItemInsert
	for _, item := range mergedItems {
		mergedSlice = append(mergedSlice, item)
	}

	return mergedSlice
}

func (ms *MYSQLStore) CreateOrder(ctx context.Context, orderNew *entity.OrderNew) (*entity.Order, error) {

	if len(orderNew.Items) == 0 {
		return nil, fmt.Errorf("no order items to insert")
	}

	if orderNew.ShippingAddress == nil || orderNew.BillingAddress == nil {
		return nil, fmt.Errorf("shipping and billing addresses are required")
	}

	if orderNew.Buyer == nil {
		return nil, fmt.Errorf("buyer is required")
	}

	paymentMethod, ok := ms.cache.GetPaymentMethodById(orderNew.PaymentMethodId)
	if !ok {
		return nil, fmt.Errorf("payment method is not exists")
	}

	shipmentCarrier, ok := ms.cache.GetShipmentCarrierById(orderNew.ShipmentCarrierId)
	if !ok {
		return nil, fmt.Errorf("shipment carrier is not exists")
	}

	order := &entity.Order{}
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		orderNew.Items = mergeOrderItems(orderNew.Items)

		validItems, err := validateOrderItems(ctx, rep, orderNew.Items)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		if len(validItems) == 0 {
			return fmt.Errorf("no valid order items to insert")
		}

		total, err := calculateTotalAmount(ctx, rep, validItems)
		if err != nil {
			return fmt.Errorf("error while calculating total amount: %w", err)
		}

		promo, ok := ms.cache.GetPromoByName(orderNew.PromoCode)
		if !ok {
			promo = entity.PromoCode{}
		}
		// check if promo is allowed and not expired
		if !promo.Allowed || promo.Expiration.Before(time.Now()) {
			promo = entity.PromoCode{}
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

		shippingAddressId, billingAddressId, err := insertAddresses(ctx, rep,
			orderNew.ShippingAddress, orderNew.BillingAddress)
		if err != nil {
			return fmt.Errorf("error while inserting addresses: %w", err)
		}

		buyerID, err := insertBuyer(ctx, rep, orderNew.Buyer, shippingAddressId, billingAddressId)
		if err != nil {
			return fmt.Errorf("error while inserting buyer: %w", err)
		}

		paymentID, err := insertPaymentRecord(ctx, rep, paymentMethod)
		if err != nil {
			return fmt.Errorf("error while inserting payment record: %w", err)
		}

		placed, _ := ms.cache.GetOrderStatusByName(entity.Placed)

		prId := sql.NullInt32{}
		if promo.ID == 0 {
			prId = sql.NullInt32{}
		} else {
			prId = sql.NullInt32{
				Int32: int32(promo.ID),
				Valid: true,
			}
		}

		order = &entity.Order{
			BuyerID:       buyerID,
			PaymentID:     paymentID,
			TotalPrice:    total,
			PromoID:       prId,
			ShipmentId:    shipmentId,
			OrderStatusID: placed.ID,
		}

		orderId, uuid, err := insertOrder(ctx, rep, order)
		if err != nil {
			return fmt.Errorf("error while inserting final order: %w", err)
		}
		order.ID = orderId
		order.UUID = uuid

		err = insertOrderItems(ctx, rep, validItems, orderId)
		if err != nil {
			return fmt.Errorf("error while inserting order items: %w", err)
		}

		return nil
	})

	return order, err
}

func getOrderItems(ctx context.Context, rep dependency.Repository, orderId int) ([]entity.OrderItem, error) {
	query := `SELECT id, order_id, product_id, quantity, size_id FROM order_item WHERE order_id = :orderId`
	ois, err := QueryListNamed[entity.OrderItem](ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return ois, nil
}
func getOrderItemsInsert(ctx context.Context, rep dependency.Repository, orderId int) ([]entity.OrderItemInsert, error) {
	query := `SELECT product_id, quantity, size_id FROM order_item WHERE order_id = :orderId`
	ois, err := QueryListNamed[entity.OrderItemInsert](ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return ois, nil
}

func getOrderShipment(ctx context.Context, rep dependency.Repository, orderId int) (*entity.Shipment, error) {
	query := `
	SELECT 
		s.id, s.created_at, s.updated_at, s.carrier_id, s.tracking_code, s.shipping_date, s.estimated_arrival_date 
	FROM shipment s 
	INNER JOIN customer_order co
		ON s.id = co.shipment_id 
	WHERE co.id = :orderId`

	s, err := QueryNamedOne[entity.Shipment](ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func updateOrderTotalPromo(ctx context.Context, rep dependency.Repository, orderId int, promoId int, totalPrice decimal.Decimal) error {
	query := `
	UPDATE customer_order
	SET promo_id = :promoId,
		total_price = :totalPrice
	WHERE id = :orderId`

	promoIdNull := sql.NullInt32{}
	if promoId == 0 {
		promoIdNull = sql.NullInt32{}
	} else {
		promoIdNull = sql.NullInt32{
			Int32: int32(promoId),
			Valid: true,
		}
	}

	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":    orderId,
		"promoId":    promoIdNull,
		"totalPrice": totalPrice,
	})
	if err != nil {
		return err
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
		if !promo.FreeShipping && promo.Discount.Equals(decimal.Zero) ||
			!promo.Allowed || promo.Expiration.Before(time.Now()) {
			promo = entity.PromoCode{
				PromoCodeInsert: entity.PromoCodeInsert{
					Discount: decimal.Zero,
				},
			}
		}

		items, err := getOrderItemsInsert(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}
		validItems, err := validateOrderItems(ctx, rep, items)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		if len(validItems) == 0 {
			// no valid items we have to set order status to canceled
			statusCanceled, ok := ms.cache.GetOrderStatusByName(entity.Cancelled)
			if !ok {
				return fmt.Errorf("order status is not exists: order status name %s", entity.Cancelled)
			}
			err := updateOrderStatus(ctx, rep, orderId, statusCanceled.ID)
			if err != nil {
				return fmt.Errorf("can't update order status: %w", err)
			}
			return fmt.Errorf("order items are not valid")
		}

		ok = compareItems(items, validItems)
		if !ok {
			// valid items not equal to order items
			// we have to update current order items
			err := updateOrderItems(ctx, rep, validItems, orderId)
			if err != nil {
				return fmt.Errorf("error while updating order items: %w", err)
			}
		}

		order, err := getOrderById(ctx, ms, orderId)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}
		order.PromoID = sql.NullInt32{
			Int32: int32(promo.ID),
			Valid: true,
		}

		total, err = updateTotalAmount(ctx, rep, validItems, order)
		if err != nil {
			return fmt.Errorf("error while updating total amount: %w", err)
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
		return nil, err
	}
	return &carrier, nil
}

// UpdateOrderItems update order items
func (ms *MYSQLStore) UpdateOrderItems(ctx context.Context, orderId int, items []entity.OrderItemInsert) (decimal.Decimal, error) {
	total := decimal.Zero

	order, err := getOrderById(ctx, ms, orderId)
	if err != nil {
		return total, fmt.Errorf("can't get order by id: %w", err)
	}

	oStatus, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
	if !ok {
		return total, fmt.Errorf("order status is not exists")
	}

	if oStatus.Name != entity.Placed {
		return total, fmt.Errorf("bad order status for updating items must be placed got: %s", oStatus.Name)
	}

	items = mergeOrderItems(items)

	if len(items) == 0 {
		err := ms.CancelOrder(ctx, orderId)
		if err != nil {
			return total, fmt.Errorf("can't cancel order while update items is: %w", err)
		}
		// early return  if no items
		return total, nil
	}

	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		validItems, err := validateOrderItems(ctx, rep, items)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		if len(validItems) == 0 {
			// no valid items we have to set order status to canceled
			statusCanceled, ok := ms.cache.GetOrderStatusByName(entity.Cancelled)
			if !ok {
				return fmt.Errorf("order status is not exists: order status name %s", entity.Cancelled)
			}
			err := updateOrderStatus(ctx, rep, orderId, statusCanceled.ID)
			if err != nil {
				return fmt.Errorf("can't update order status: %w", err)
			}
			return fmt.Errorf("order items are not valid")
		}

		err = updateOrderItems(ctx, rep, validItems, orderId)
		if err != nil {
			return fmt.Errorf("error while updating order items: %w", err)
		}

		total, err = updateTotalAmount(ctx, rep, validItems, order)
		if err != nil {
			return fmt.Errorf("error while updating total amount: %w", err)
		}

		return nil
	})
	if err != nil {
		return total, fmt.Errorf("can't update order items: %w", err)
	}

	return total, err
}

func getOrderTotalPrice(ctx context.Context, rep dependency.Repository, orderId int) (decimal.Decimal, error) {
	query := fmt.Sprintf(`SELECT total_price FROM customer_order WHERE id = %d`, orderId)
	var total decimal.Decimal
	err := rep.DB().GetContext(ctx, &total, query)
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
func (ms *MYSQLStore) UpdateOrderShippingCarrier(ctx context.Context, orderId int, shipmentCarrierId int) (decimal.Decimal, error) {
	total := decimal.Zero
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		orderShipmentCarrier, err := getOrderShipmentCarrier(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order shipment carrier: %w", err)
		}
		if orderShipmentCarrier.ID == shipmentCarrierId {
			return nil
		}

		newShipmentCarrier, ok := ms.cache.GetShipmentCarrierById(shipmentCarrierId)
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

		total, err = getOrderTotalPrice(ctx, rep, orderId)
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

		err = updateOrderTotalPromo(ctx, rep, orderId, promo.ID, total)
		if err != nil {
			return fmt.Errorf("can't update order total promo: %w", err)
		}

		return nil
	})
	if err != nil {
		return total, fmt.Errorf("can't update order shipping carrier: %w", err)
	}
	return total, nil
}

func getOrderById(ctx context.Context, rep dependency.Repository, orderId int) (*entity.Order, error) {
	query := `
	SELECT * from customer_order WHERE id = :orderId`
	order, err := QueryNamedOne[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func getOrderByUUID(ctx context.Context, rep dependency.Repository, uuid string) (*entity.Order, error) {
	query := `
	SELECT * from customer_order WHERE uuid = :uuid`
	order, err := QueryNamedOne[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"uuid": uuid,
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func updateOrderStatus(ctx context.Context, rep dependency.Repository, orderId int, orderStatusId int) error {
	query := `UPDATE customer_order SET order_status_id = :orderStatusId WHERE id = :orderId`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":       orderId,
		"orderStatusId": orderStatusId,
	})
	if err != nil {
		return fmt.Errorf("can't update order status: %w", err)
	}
	return nil
}

func updateOrderPayment(ctx context.Context, rep dependency.Repository, paymentId int, payment *entity.PaymentInsert) error {
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

func updateOrderItems(ctx context.Context, rep dependency.Repository, validItems []entity.OrderItemInsert, orderId int) error {
	err := deleteOrderItems(ctx, rep, orderId)
	if err != nil {
		return fmt.Errorf("error while deleting order items: %w", err)
	}
	err = insertOrderItems(ctx, rep, validItems, orderId)
	if err != nil {
		return fmt.Errorf("error while inserting order items: %w", err)
	}
	return nil
}

func updateTotalAmount(ctx context.Context, rep dependency.Repository, validItems []entity.OrderItemInsert, order *entity.Order) (decimal.Decimal, error) {
	// total no promo no shipment costs no promo discount
	total, err := calculateTotalAmount(ctx, rep, validItems)
	if err != nil {
		return total, fmt.Errorf("error while calculating total amount: %w", err)
	}

	promo, ok := rep.Cache().GetPromoById(int(order.PromoID.Int32))
	if !ok {
		promo = &entity.PromoCode{}
	}

	// check if promo is allowed and not expired
	if !promo.Allowed || promo.Expiration.Before(time.Now()) {
		promo = &entity.PromoCode{}
	}

	if !promo.FreeShipping {
		shipment, err := getOrderShipment(ctx, rep, order.ID)
		if err != nil {
			return total, fmt.Errorf("can't get order shipment: %w", err)
		}
		shipmentCarrier, ok := rep.Cache().GetShipmentCarrierById(shipment.CarrierID)
		if !ok {
			return total, fmt.Errorf("shipment carrier is not exists")
		}
		total = total.Add(shipmentCarrier.Price)
	}

	if !promo.Discount.Equals(decimal.Zero) {
		total = total.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
	}

	err = updateOrderTotalPromo(ctx, rep, order.ID, promo.ID, total)
	if err != nil {
		return total, fmt.Errorf("can't update order total promo: %w", err)
	}

	return total, nil
}

// OrderPaymentDone updates the payment status of an order and adds payment info to order.
func (ms *MYSQLStore) OrderPaymentDone(ctx context.Context, orderUUID string, payment *entity.PaymentInsert) error {

	_, ok := ms.cache.GetPaymentMethodById(payment.PaymentMethodID)
	if !ok {
		return fmt.Errorf("payment method is not exists: payment method id %d", payment.PaymentMethodID)
	}

	order, err := getOrderByUUID(ctx, ms, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get order by uuid %s: %w", orderUUID, err)
	}

	orderStatus, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
	if !ok {
		return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
	}

	if orderStatus.Name != entity.Placed {
		return fmt.Errorf("order status is not placed: order status %s", orderStatus.Name)
	}

	if payment.TransactionAmount.LessThan(order.TotalPrice) {
		return fmt.Errorf("payment amount is less than order total price: %s", payment.TransactionAmount.String())
	}

	statusConfirmed, ok := ms.Cache().GetOrderStatusByName(entity.Confirmed)
	if !ok {
		return fmt.Errorf("order status is not exists: order status name %s", entity.Confirmed)
	}

	var customErr error

	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		orderItems, err := getOrderItemsInsert(ctx, rep, order.ID)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		validItems, err := validateOrderItems(ctx, rep, orderItems)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		if len(validItems) == 0 {
			// no valid items we have to set order status to canceled
			statusCanceled, ok := ms.cache.GetOrderStatusByName(entity.Cancelled)
			if !ok {
				return fmt.Errorf("order status is not exists: order status name %s", entity.Cancelled)
			}
			err := updateOrderStatus(ctx, rep, order.ID, statusCanceled.ID)
			if err != nil {
				return fmt.Errorf("can't update order status: %w", err)
			}

			// early return if no valid items
			customErr = fmt.Errorf("order items are not valid")
			return nil
		}

		ok := compareItems(orderItems, validItems)
		if !ok {
			// valid items not equal to order items
			// we have to update current order items and total amount
			err = updateOrderItems(ctx, rep, validItems, order.ID)
			if err != nil {
				return fmt.Errorf("error while updating order items: %w", err)
			}

			_, err = updateTotalAmount(ctx, rep, validItems, order)
			if err != nil {
				return fmt.Errorf("error while updating total amount: %w", err)
			}

			// early return if items updated
			customErr = fmt.Errorf("order items are not valid and were updated")
			return nil
		}

		err = rep.Products().ReduceStockForProductSizes(ctx, validItems)
		if err != nil {
			return fmt.Errorf("error while reducing stock for product sizes: %w", err)
		}

		err = updateOrderStatus(ctx, rep, order.ID, statusConfirmed.ID)
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
		return err
	}
	if customErr != nil {
		return customErr
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
		return fmt.Errorf("can't update order shipment: %w", err)
	}
	return nil
}

// UpdateShippingStatus updates the shipping status of an order.
func (ms *MYSQLStore) UpdateShippingInfo(ctx context.Context, orderId int, shipment *entity.Shipment) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderById(ctx, rep, orderId)
		if err != nil {
			return err
		}

		orderStatus, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		}

		if orderStatus.Name != entity.Confirmed {
			return fmt.Errorf("order status is not confirmed: order status %s", orderStatus.Name)
		}

		_, ok = ms.cache.GetShipmentCarrierById(shipment.CarrierID)
		if !ok {
			return fmt.Errorf("shipment carrier is not exists: shipment carrier id %d", shipment.CarrierID)
		}
		err = updateOrderShipment(ctx, rep, orderId, shipment)
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
		return nil, err
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

func fetchOrderInfo(ctx context.Context, rep dependency.Repository, order *entity.Order) (*entity.OrderFull, error) {
	orderItems, err := getOrderItems(ctx, rep, order.ID)
	if err != nil {
		return nil, fmt.Errorf("can't get order items: %w", err)
	}

	payment, err := getPaymentById(ctx, rep, order.PaymentID)
	if err != nil {
		return nil, fmt.Errorf("can't get payment by id: %w", err)
	}

	paymentMethod, ok := rep.Cache().GetPaymentMethodById(payment.PaymentMethodID)
	if !ok {
		return nil, fmt.Errorf("payment method is not exists")
	}

	shipment, err := getOrderShipment(ctx, rep, order.ID)
	if err != nil {
		return nil, fmt.Errorf("can't get order shipment: %w", err)
	}
	shipmentCarrier, ok := rep.Cache().GetShipmentCarrierById(shipment.CarrierID)
	if !ok {
		return nil, fmt.Errorf("shipment carrier is not exists")
	}

	promo := &entity.PromoCode{}
	if order.PromoID.Int32 != 0 && order.PromoID.Valid {
		promo, ok = rep.Cache().GetPromoById(int(order.PromoID.Int32))
		if !ok {
			return nil, fmt.Errorf("promo code is not exists")
		}
	}

	orderStatus, ok := rep.Cache().GetOrderStatusById(order.OrderStatusID)
	if !ok {
		return nil, fmt.Errorf("order status is not exists")
	}

	buyer, err := getBuyerById(ctx, rep, order.BuyerID)
	if err != nil {
		return nil, fmt.Errorf("can't get buyer by id: %w", err)
	}

	shippingAddress, err := getAddressId(ctx, rep, buyer.ShippingAddressID)
	if err != nil {
		return nil, fmt.Errorf("can't get shipping address by id: %w", err)
	}
	billingAddress, err := getAddressId(ctx, rep, buyer.BillingAddressID)
	if err != nil {
		return nil, fmt.Errorf("can't get billing address by id: %w", err)
	}

	orderInfo := entity.OrderFull{
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
func (ms *MYSQLStore) GetOrderById(ctx context.Context, orderId int) (*entity.OrderFull, error) {

	order, err := getOrderById(ctx, ms, orderId)
	if err != nil {
		return nil, err
	}

	return fetchOrderInfo(ctx, ms, order)
}

func (ms *MYSQLStore) GetOrderByUUID(ctx context.Context, uuid string) (*entity.OrderFull, error) {
	order, err := getOrderByUUID(ctx, ms, uuid)
	if err != nil {
		return nil, err
	}
	return fetchOrderInfo(ctx, ms, order)
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

func (ms *MYSQLStore) GetOrdersByEmail(ctx context.Context, email string) ([]entity.OrderFull, error) {
	orders, err := getOrdersByEmail(ctx, ms, email)
	if err != nil {
		return nil, err
	}

	var ordersInfo []entity.OrderFull
	for _, order := range orders {
		orderInfo, err := fetchOrderInfo(ctx, ms, &order)
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

func (ms *MYSQLStore) GetOrdersByStatus(ctx context.Context, status entity.OrderStatusName) ([]entity.OrderFull, error) {
	os, ok := ms.cache.GetOrderStatusByName(status)
	if !ok {
		return nil, fmt.Errorf("order status is not exists: order status id %v", status)
	}

	orders, err := getOrdersByStatus(ctx, ms, os.ID)
	if err != nil {
		return nil, err
	}

	var ordersInfo []entity.OrderFull
	for _, order := range orders {
		orderInfo, err := fetchOrderInfo(ctx, ms, &order)
		if err != nil {
			return nil, err
		}
		ordersInfo = append(ordersInfo, *orderInfo)
	}

	return ordersInfo, nil
}

func (ms *MYSQLStore) RefundOrder(ctx context.Context, orderId int) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		order, err := getOrderById(ctx, rep, orderId)
		if err != nil {
			return err
		}

		orderStatus, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		}

		if orderStatus.Name != entity.Delivered {
			return fmt.Errorf("order status can be only in (Confirmed, Delivered): order status %s", orderStatus.Name)
		}

		statusShipped, ok := ms.cache.GetOrderStatusByName(entity.Refunded)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Refunded)
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

func (ms *MYSQLStore) DeliveredOrder(ctx context.Context, orderId int) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

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
		order, err := getOrderById(ctx, rep, orderId)
		if err != nil {
			return err
		}

		orderStatus, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		}

		if orderStatus.Name != entity.Placed {
			return fmt.Errorf("order status can be only in (Placed): order status %s", orderStatus.Name)
		}

		items, err := getOrderItemsInsert(ctx, rep, orderId)
		if err != nil {
			return fmt.Errorf("can't get order items insert: %w", err)
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
