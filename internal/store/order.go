package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func validateOrderItemsStockAvailability(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert) ([]entity.OrderItem, error) {
	// Check if there are no items provided
	if len(items) == 0 {
		return nil, errors.New("no items to validate")
	}

	// Get product IDs from items
	prdIds := getProductIdsFromItems(items)

	// Get product details by IDs
	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	// Get product sizes (stock) details by item details
	prdSizes, err := getProductsSizesByIds(ctx, rep, items)
	if err != nil {
		return nil, fmt.Errorf("can't get products sizes by ids: %w", err)
	}

	// Initialize a slice to store the valid order items
	validItems := make([]entity.OrderItem, 0, len(items))

	for _, item := range items {
		itemValid := false

		for _, prdSize := range prdSizes {
			if item.ProductID == prdSize.ProductID && item.SizeID == prdSize.SizeID {
				if prdSize.Quantity.GreaterThan(decimal.Zero) {
					// Adjust quantity if necessary
					if item.Quantity.GreaterThan(prdSize.Quantity) {
						item.Quantity = prdSize.Quantity
					}

					for _, prd := range prds {
						if item.ProductID == prd.ID {
							// Set price and sale percentage from product details
							item.ProductPrice = prd.Price
							if prd.SalePercentage.Valid {
								item.ProductSalePercentage = prd.SalePercentage.Decimal
							}
							validItem := entity.OrderItem{
								OrderItemInsert: item,
								Thumbnail:       prd.ThumbnailMediaURL,
								ProductName:     prd.Name,
								ProductBrand:    prd.Brand,
								SKU:             prd.SKU,
								CategoryID:      prd.CategoryID,
							}

							// Add item to valid list as it passed all checks
							validItems = append(validItems, validItem)
							itemValid = true
							break
						}
					}

					if itemValid {
						break // Item is valid and processed, no need to check further
					}
				}
			}
		}
	}

	return validItems, nil
}

// compareItems return true if items are equal
func compareItems(items, validItems []entity.OrderItemInsert) bool {
	// Sort both slices
	sort.Sort(entity.OrderItemsByProductID(items))
	sort.Sort(entity.OrderItemsByProductID(validItems))

	// Compare lengths
	if len(items) != len(validItems) {
		return false
	}

	// Compare each element
	for i := range items {
		// Compare all fields of OrderItemInsert
		if items[i].ProductID != validItems[i].ProductID ||
			items[i].ProductPrice.Cmp(validItems[i].ProductPrice) != 0 ||
			items[i].ProductSalePercentage.Cmp(validItems[i].ProductSalePercentage) != 0 ||
			items[i].Quantity.Cmp(validItems[i].Quantity) != 0 ||
			items[i].SizeID != validItems[i].SizeID {
			return false
		}
	}
	return true
}

func calculateTotalAmount(ctx context.Context, rep dependency.Repository, items []entity.ProductInfoProvider) (decimal.Decimal, error) {
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
		if !item.Quantity.IsPositive() { // Ensure that the quantity is a positive number
			return decimal.Zero, fmt.Errorf("quantity for product ID %d is not positive", item.ProductID)
		}
		caseStatements = append(caseStatements, fmt.Sprintf("WHEN product.id = %d THEN %s", item.ProductID, item.Quantity.String()))
		productIDs = append(productIDs, fmt.Sprintf("%d", item.ProductID))
	}

	caseSQL := strings.Join(caseStatements, " ")
	idsSQL := strings.Join(productIDs, ", ")

	query := fmt.Sprintf(`
		SELECT SUM(price * (1 - COALESCE(sale_percentage, 0) / 100) * CASE %s END) AS total_amount
		FROM product
		WHERE id IN (%s)
	`, caseSQL, idsSQL)

	var totalAmount decimal.Decimal
	err := rep.DB().GetContext(ctx, &totalAmount, query)
	if err != nil {
		return decimal.Zero, err
	}

	return totalAmount, nil
}

func insertAddresses(ctx context.Context, rep dependency.Repository, shippingAddress, billingAddress *entity.AddressInsert) (int, int, error) {
	var shippingID, billingID int64
	query := `
		INSERT INTO address (country, state, city, address_line_one, address_line_two, company, postal_code)
		VALUES (:country, :state, :city, :address_line_one, :address_line_two, :company, :postal_code);
	`

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
	(first_name, last_name, email, phone, billing_address_id, shipping_address_id)
	VALUES (:firstName, :lastName, :email, :phone, :billingAddressId, :shippingAddressId)
	`
	buyer := entity.Buyer{
		BuyerInsert:       *b,
		BillingAddressID:  bAdr,
		ShippingAddressID: sAdr,
	}

	buyerID, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"firstName":         buyer.FirstName,
		"lastName":          buyer.LastName,
		"email":             buyer.Email,
		"phone":             buyer.Phone,
		"billingAddressId":  buyer.BillingAddressID,
		"shippingAddressId": buyer.ShippingAddressID,
	})
	if err != nil {
		return 0, fmt.Errorf("can't insert buyer: %w", err)
	}

	return buyerID, nil
}

func insertPaymentRecord(ctx context.Context, rep dependency.Repository, paymentMethodId int) (int, error) {

	insertQuery := `
		INSERT INTO payment (payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done)
		VALUES (:paymentMethodId, 0, 0, false);
	`

	paymentID, err := ExecNamedLastId(ctx, rep.DB(), insertQuery, map[string]interface{}{
		"paymentMethodId": paymentMethodId,
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
			"order_id":                orderID,
			"product_id":              item.ProductID,
			"product_price":           item.ProductPrice,
			"product_sale_percentage": item.ProductSalePercentage,
			"quantity":                item.Quantity,
			"size_id":                 item.SizeID,
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

// mergeOrderItems merges the order items by summing up the quantities of items with the same product ID and size ID.
// It skips items with zero quantity and returns a new slice of merged order items.
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

// adjustQuantities adjusts the quantity of the items if it exceeds the maxOrderItemPerSize
func adjustQuantities(maxOrderItemPerSize int, items []entity.OrderItemInsert) []entity.OrderItemInsert {
	maxQuantity := decimal.NewFromInt(int64(maxOrderItemPerSize))
	for i, item := range items {
		// Check if the item quantity exceeds the maxOrderItemPerSize
		if item.Quantity.Cmp(maxQuantity) > 0 {
			items[i].Quantity = maxQuantity
		}
	}
	return items
}

func (ms *MYSQLStore) validateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert) ([]entity.OrderItem, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no order items to insert")
	}
	// map items by product id and size id
	items = mergeOrderItems(items)

	// adjust quantities if it exceeds the maxOrderItemPerSize
	items = adjustQuantities(ms.cache.GetDict().MaxOrderItems, items)

	// validate items stock availability
	validItems, err := validateOrderItemsStockAvailability(ctx, ms, items)
	if err != nil {
		return nil, fmt.Errorf("error while validating order items: %w", err)
	}
	if len(validItems) == 0 {
		return nil, fmt.Errorf("no valid order items to insert")
	}
	return validItems, nil
}

// ValidateOrderItemsInsert validates the order items and returns the valid items and the total amount
func (ms *MYSQLStore) ValidateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert) ([]entity.OrderItem, decimal.Decimal, error) {
	var err error

	validItems, err := ms.validateOrderItemsInsert(ctx, items)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("error while validating order items: %w", err)
	}
	if len(validItems) == 0 {
		return nil, decimal.Zero, fmt.Errorf("no valid order items to insert")
	}
	validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

	providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
	total, err := calculateTotalAmount(ctx, ms, providers)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("error while calculating total amount: %w", err)
	}
	if total.IsZero() {
		return nil, decimal.Zero, fmt.Errorf("total amount is zero")
	}

	return validItems, total, nil
}

func (ms *MYSQLStore) ValidateOrderByUUID(ctx context.Context, uuid string) (*entity.OrderFull, error) {
	var err error

	orderFull, err := ms.GetOrderFullByUUID(ctx, uuid)
	if err != nil {
		return nil, fmt.Errorf("error while getting order by uuid: %w", err)
	}

	oStatus, ok := ms.cache.GetOrderStatusById(orderFull.Order.OrderStatusID)
	if !ok {
		return nil, fmt.Errorf("order status is not exists")
	}

	if oStatus.Name != entity.Placed {
		return orderFull, nil
	}

	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)

	var customErr error
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		validItems, subtotal, err := rep.Order().ValidateOrderItemsInsert(ctx, items)
		if err != nil {
			err := cancelOrder(ctx, rep, orderFull)
			if err != nil {
				return fmt.Errorf("can't cancel order while applying promo code: %w", err)
			}
			return fmt.Errorf("error while validating order items: %w", err)
		}

		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		ok = compareItems(items, validItemsInsert)
		if !ok {
			// valid items not equal to order items
			// we have to update current order items
			err := updateOrderItems(ctx, rep, validItemsInsert, orderFull.Order.ID)
			if err != nil {
				return fmt.Errorf("error while updating order items: %w", err)
			}
			_, err = updateTotalAmount(ctx, rep, orderFull.Order.ID, subtotal, orderFull.PromoCode, orderFull.Shipment)
			if err != nil {
				return fmt.Errorf("error while updating total amount: %w", err)
			}
			customErr = fmt.Errorf("order items are not valid and were updated")
			return nil
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	if customErr != nil {
		return nil, customErr
	}

	return orderFull, nil

}

func (ms *MYSQLStore) CreateOrder(ctx context.Context, orderNew *entity.OrderNew, receivePromo bool) (*entity.Order, bool, error) {

	if len(orderNew.Items) == 0 {
		return nil, false, fmt.Errorf("no order items to insert")
	}

	if orderNew.ShippingAddress == nil || orderNew.BillingAddress == nil {
		return nil, false, fmt.Errorf("shipping and billing addresses are required")
	}

	if orderNew.Buyer == nil {
		return nil, false, fmt.Errorf("buyer is required")
	}

	paymentMethod, ok := ms.cache.GetPaymentMethodByName(orderNew.PaymentMethod)
	if !ok || !paymentMethod.Allowed {
		return nil, false, fmt.Errorf("payment method is not exists")
	}

	shipmentCarrier, ok := ms.cache.GetShipmentCarrierById(orderNew.ShipmentCarrierId)
	if !ok || !shipmentCarrier.Allowed {
		return nil, false, fmt.Errorf("shipment carrier is not exists")
	}

	order := &entity.Order{}
	sendEmail := false
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		orderNew.Items = mergeOrderItems(orderNew.Items)

		validItems, subtotal, err := rep.Order().ValidateOrderItemsInsert(ctx, orderNew.Items)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		promo, ok := ms.cache.GetPromoByName(orderNew.PromoCode)
		if !ok {
			promo = entity.PromoCode{}
		}
		// check if promo is allowed and not expired
		if !promo.Allowed || promo.Expiration.Before(time.Now()) {
			promo = entity.PromoCode{}
		}

		shipmentId, err := insertShipment(ctx, rep, shipmentCarrier)
		if err != nil {
			return fmt.Errorf("error while inserting shipment: %w", err)
		}

		if !promo.Discount.Equals(decimal.Zero) {
			subtotal = subtotal.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
		}

		if !promo.FreeShipping {
			subtotal = subtotal.Add(shipmentCarrier.Price)
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

		if receivePromo {
			subscribed, err := ms.Subscribers().IsSubscribed(ctx, orderNew.Buyer.Email)
			if err != nil {
				return fmt.Errorf("error while checking subscription: %w", err)
			}
			if !subscribed {
				sendEmail = true
				err := ms.Subscribers().UpsertSubscription(ctx, orderNew.Buyer.Email, true)
				if err != nil {
					return fmt.Errorf("error while upserting subscription: %w", err)
				}
			}
		}

		paymentID, err := insertPaymentRecord(ctx, rep, paymentMethod.ID)
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
			TotalPrice:    subtotal,
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

		err = insertOrderItems(ctx, rep, validItemsInsert, orderId)
		if err != nil {
			return fmt.Errorf("error while inserting order items: %w", err)
		}

		return nil
	})

	return order, sendEmail, err
}

func getOrdersItems(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int][]entity.OrderItem, error) {
	// Check if there are no order IDs provided
	if len(orderIds) == 0 {
		return map[int][]entity.OrderItem{}, nil
	}

	query := `
        SELECT 
			oi.id,
			oi.order_id,
			oi.product_id,
			oi.quantity,
			oi.size_id,
			oi.product_price,
			oi.product_sale_percentage,
			m.thumbnail,
			p.name AS product_name,
			p.brand AS product_brand,
			p.sku AS product_sku,
			p.category_id AS category_id 
        FROM order_item oi
        JOIN product p ON oi.product_id = p.id
		JOIN media m ON p.thumbnail_id = m.id
        WHERE oi.order_id IN (:orderIds)
    `

	// Execute the query with named parameters
	ois, err := QueryListNamed[entity.OrderItem](ctx, rep.DB(), query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, err
	}

	// Initialize a map to store the order items grouped by order ID
	orderItemsMap := make(map[int][]entity.OrderItem)

	// Group order items by order ID
	for _, oi := range ois {
		orderItemsMap[oi.OrderID] = append(orderItemsMap[oi.OrderID], oi)
	}

	return orderItemsMap, nil
}
func getOrderItemsInsert(ctx context.Context, rep dependency.Repository, orderId int) ([]entity.OrderItemInsert, error) {

	query := `SELECT 
		product_id,
		product_price,
		product_sale_percentage,
		quantity,
		size_id
	size_id FROM order_item WHERE order_id = :orderId`
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

func shipmentsByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]*entity.Shipment, error) {
	if len(orderIds) == 0 {
		return map[int]*entity.Shipment{}, nil
	}

	query := `
	SELECT 
		customer_order.id as order_id,
		shipment.id,
		shipment.created_at,
		shipment.updated_at,
		shipment.carrier_id,
		shipment.tracking_code,
		shipment.shipping_date,
		shipment.estimated_arrival_date
	FROM shipment
	JOIN customer_order ON shipment.id = customer_order.shipment_id
	WHERE customer_order.id IN (:orderIds)`

	query, params, err := MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	rows, err := rep.DB().QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	shipments := make(map[int]*entity.Shipment)

	for rows.Next() {
		var shipment entity.Shipment
		var orderId int

		err := rows.Scan(
			&orderId,
			&shipment.ID,
			&shipment.CreatedAt,
			&shipment.UpdatedAt,
			&shipment.CarrierID,
			&shipment.TrackingCode,
			&shipment.ShippingDate,
			&shipment.EstimatedArrivalDate,
		)
		if err != nil {
			return nil, err
		}

		shipments[orderId] = &shipment
	}

	// Check for any errors during iteration
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Check if all order IDs were found
	if len(shipments) != len(orderIds) {
		return nil, errors.New("not all order IDs were found")
	}

	return shipments, nil
}

func (ms *MYSQLStore) ApplyPromoCode(ctx context.Context, orderUUID string, promoCode string) (*entity.OrderFull, error) {
	promo, ok := ms.cache.GetPromoByName(promoCode)
	if !ok || !promo.Allowed || promo.Expiration.Before(time.Now()) {
		return nil, fmt.Errorf("promo code is not exists or not allowed or expired")
	}

	orderFull, err := ms.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}

	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		os, ok := ms.cache.GetOrderStatusById(orderFull.Order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists")
		}

		if os.Name != entity.Placed {
			return fmt.Errorf("bad order status for applying promo code must be placed got: %s", os.Name)
		}

		items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)

		validItems, subtotal, err := rep.Order().ValidateOrderItemsInsert(ctx, items)
		if err != nil {
			err := cancelOrder(ctx, rep, orderFull)
			if err != nil {
				return fmt.Errorf("can't cancel order while applying promo code: %w", err)
			}
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		ok = compareItems(items, validItemsInsert)
		if !ok {
			// valid items not equal to order items
			// we have to update current order items
			err := updateOrderItems(ctx, rep, validItemsInsert, orderFull.Order.ID)
			if err != nil {
				return fmt.Errorf("error while updating order items: %w", err)
			}
		}

		orderFull.Order.PromoID = sql.NullInt32{
			Int32: int32(promo.ID),
			Valid: true,
		}

		grandTotal, err := updateTotalAmount(ctx, rep, orderFull.Order.ID, subtotal, &promo, orderFull.Shipment)
		if err != nil {
			return fmt.Errorf("error while updating total amount: %w", err)
		}
		orderFull.Order.TotalPrice = grandTotal

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("can't apply promo code: %w", err)
	}
	return orderFull, nil
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
func (ms *MYSQLStore) UpdateOrderItems(ctx context.Context, orderUUID string, items []entity.OrderItemInsert) (*entity.OrderFull, error) {

	orderFull, err := ms.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}

	oStatus, ok := ms.cache.GetOrderStatusById(orderFull.Order.OrderStatusID)
	if !ok {
		return nil, fmt.Errorf("order status is not exists")
	}

	if oStatus.Name != entity.Placed {
		return nil, fmt.Errorf("bad order status for updating items must be placed got: %s", oStatus.Name)
	}

	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		validItems, subtotal, err := rep.Order().ValidateOrderItemsInsert(ctx, items)
		if err != nil {
			err := cancelOrder(ctx, rep, orderFull)
			if err != nil {
				return fmt.Errorf("can't cancel order while applying promo code: %w", err)
			}
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		err = updateOrderItems(ctx, rep, validItemsInsert, orderFull.Order.ID)
		if err != nil {
			return fmt.Errorf("error while updating order items: %w", err)
		}
		getOrdersItems, err := getOrdersItems(ctx, rep, []int{orderFull.Order.ID})
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}
		orderFull.OrderItems = getOrdersItems[orderFull.Order.ID]

		grandTotal, err := updateTotalAmount(ctx, rep, orderFull.Order.ID, subtotal, orderFull.PromoCode, orderFull.Shipment)
		if err != nil {
			return fmt.Errorf("error while updating total amount: %w", err)
		}
		orderFull.Order.TotalPrice = grandTotal

		return nil
	})
	if err != nil {
		return orderFull, fmt.Errorf("can't update order items: %w", err)
	}

	return orderFull, err
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

func (ms *MYSQLStore) GetOrderByUUID(ctx context.Context, uuid string) (*entity.Order, error) {
	return getOrderByUUID(ctx, ms, uuid)
}

func (ms *MYSQLStore) CheckPaymentPendingByUUID(ctx context.Context, uuid string) (*entity.Payment, *entity.Order, error) {
	order, err := ms.GetOrderByUUID(ctx, uuid)
	if err != nil {
		return nil, order, fmt.Errorf("can't get order by uuid: %w", err)
	}

	os, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
	if !ok {
		return nil, order, fmt.Errorf("order status is not exists by id: %d", order.OrderStatusID)
	}

	if os.Name != entity.AwaitingPayment {
		return nil, order, fmt.Errorf("bad order status for checking payment pending must be awaiting payment got: %s", os.Name)
	}

	p, err := ms.GetPaymentByOrderUUID(ctx, order.UUID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get payment by order id",
			slog.String("err", err.Error()),
		)
		return nil, order, status.Errorf(codes.Internal, "can't get payment by order id")
	}

	return p, order, nil
}

func updateOrderStatus(ctx context.Context, rep dependency.Repository, orderId int, orderStatusId int) error {
	query := `UPDATE customer_order SET order_status_id = :orderStatusId WHERE id = :orderId`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":       orderId,
		"orderStatusId": orderStatusId,
	})
	if err != nil {
		return err
	}
	return nil
}

func updateOrderPayment(ctx context.Context, rep dependency.Repository, paymentId int, payment *entity.PaymentInsert) error {
	query := `
	UPDATE payment 
	SET transaction_amount = :transactionAmount,
		transaction_amount_payment_currency = :transactionAmountPaymentCurrency,
		transaction_id = :transactionId,
		is_transaction_done = :isTransactionDone,
		payment_method_id = :paymentMethodId,
		payer = :payer,
		payee = :payee
	WHERE id = :paymentId`

	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"transactionAmount":                payment.TransactionAmount,
		"transactionAmountPaymentCurrency": payment.TransactionAmountPaymentCurrency,
		"transactionId":                    payment.TransactionID,
		"isTransactionDone":                payment.IsTransactionDone,
		"paymentMethodId":                  payment.PaymentMethodID,
		"payer":                            payment.Payer,
		"payee":                            payment.Payee,
		"paymentId":                        paymentId,
	})

	if err != nil {
		return err
	}
	return nil
}

func (ms *MYSQLStore) UpdateTotalPaymentCurrency(ctx context.Context, orderUUID string, tapc decimal.Decimal) error {
	query := `        
	UPDATE payment 
	SET transaction_amount_payment_currency = :tapc 
	WHERE id = (
		SELECT payment_id FROM customer_order 
		WHERE uuid = :orderUUID
	)`
	err := ExecNamed(ctx, ms.db, query, map[string]any{
		"tapc":      tapc,
		"orderUUID": orderUUID,
	})
	if err != nil {
		return fmt.Errorf("can't update total payment currency: %w", err)
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

// updateTotalAmount calculates the total amount for an order by considering the subtotal, promo code, and shipment details.
// It checks if the promo code is allowed and not expired. If it is, the promo code is reset to an empty value.
// If the promo code does not offer free shipping, the shipment carrier price is added to the subtotal.
// If the promo code offers a discount, the subtotal is multiplied by (100 - discount) / 100.
// Finally, it updates the order's total promo and returns the calculated subtotal.
// If any error occurs during the process, it returns an error along with a zero subtotal.
func updateTotalAmount(ctx context.Context, rep dependency.Repository, orderId int, subtotal decimal.Decimal, promo *entity.PromoCode, shipment *entity.Shipment) (decimal.Decimal, error) {
	// check if promo is allowed and not expired
	if !promo.Allowed || promo.Expiration.Before(time.Now()) {
		promo = &entity.PromoCode{}
	}

	if !promo.Discount.Equals(decimal.Zero) {
		subtotal = subtotal.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
	}

	if !promo.FreeShipping {
		shipmentCarrier, ok := rep.Cache().GetShipmentCarrierById(shipment.CarrierID)
		if !ok {
			return decimal.Zero, fmt.Errorf("shipment carrier is not exists")
		}
		subtotal = subtotal.Add(shipmentCarrier.Price)
	}

	err := updateOrderTotalPromo(ctx, rep, orderId, promo.ID, subtotal)
	if err != nil {
		return decimal.Zero, fmt.Errorf("can't update order total promo: %w", err)
	}

	return subtotal, nil
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

// InsertOrderPayment inserts order payment info for invoice
func (ms *MYSQLStore) InsertOrderInvoice(ctx context.Context, orderUUID string, addr string, pm *entity.PaymentMethod) (*entity.OrderFull, error) {

	pm, ok := ms.cache.GetPaymentMethodById(pm.ID)
	if !ok || !pm.Allowed {
		return nil, fmt.Errorf("payment method is not exists: payment method id %v", pm)
	}

	orderFull, err := ms.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order by uuid %s: %w", orderUUID, err)
	}

	if orderFull.Order.TotalPrice.IsZero() {
		err := cancelOrder(ctx, ms, orderFull)
		if err != nil {
			return nil, fmt.Errorf("can't cancel order invoice creation: %w", err)
		}
		return nil, fmt.Errorf("total price is zero")
	}

	orderStatus, ok := ms.cache.GetOrderStatusById(orderFull.Order.OrderStatusID)
	if !ok {
		return nil, fmt.Errorf("order status is not exists: order status id %d", orderFull.Order.OrderStatusID)
	}

	if orderStatus.Name != entity.Placed && orderStatus.Name != entity.Cancelled {
		return nil, fmt.Errorf("order status is not placed and not cancelled: order status %s", orderStatus.Name)
	}

	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)

	var customErr error
	// var p *entity.Payment
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		validItems, subtotal, err := rep.Order().ValidateOrderItemsInsert(ctx, items)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't validate order items",
				slog.String("err", err.Error()),
			)
			err := cancelOrder(ctx, rep, orderFull)
			if err != nil {
				return fmt.Errorf("can't cancel order : %w", err)
			}
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		ok = compareItems(items, validItemsInsert)
		if !ok {
			// valid items not equal to order items
			// we have to update current order items
			err := updateOrderItems(ctx, rep, validItemsInsert, orderFull.Order.ID)
			if err != nil {
				return fmt.Errorf("error while updating order items: %w", err)
			}
			_, err = updateTotalAmount(ctx, rep, orderFull.Order.ID, subtotal, orderFull.PromoCode, orderFull.Shipment)
			if err != nil {
				return fmt.Errorf("error while updating total amount: %w", err)
			}
			customErr = fmt.Errorf("order items are not valid and were updated")
			return nil
		}

		err = rep.Products().ReduceStockForProductSizes(ctx, validItemsInsert)
		if err != nil {
			return fmt.Errorf("error while reducing stock for product sizes: %w", err)
		}

		orderFull.Payment.PaymentMethodID = pm.ID
		orderFull.Payment.IsTransactionDone = false
		orderFull.Payment.TransactionAmount = orderFull.Order.TotalPrice
		orderFull.Payment.TransactionAmountPaymentCurrency = orderFull.Order.TotalPrice
		orderFull.Payment.Payee = sql.NullString{
			String: addr,
			Valid:  true,
		}

		// TODO:
		// // convert base currency to payment currency in this case to USD
		// totalUSD, err := p.rates.ConvertFromBaseCurrency(dto.USD, payment.TransactionAmount)
		// if err != nil {
		// 	return fmt.Errorf("can't convert to base currency: %w", err)
		// }

		err = updateOrderPayment(ctx, rep, orderFull.Order.PaymentID, &orderFull.Payment.PaymentInsert)
		if err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		newStatus, _ := ms.cache.GetOrderStatusByName(entity.AwaitingPayment)

		err = updateOrderStatus(ctx, rep, orderFull.Order.ID, newStatus.ID)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	if customErr != nil {
		return nil, customErr
	}

	return orderFull, nil
}

func updateOrderShipment(ctx context.Context, rep dependency.Repository, orderId int, shipment *entity.Shipment) error {
	query := `
    UPDATE shipment
    SET 
        tracking_code = :trackingCode,
        carrier_id = :carrierId,
        shipping_date = :shippingDate,
        estimated_arrival_date = :estimatedArrivalDate
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

// SetTrackingNumber sets the tracking number for an order, returns the shipment and the order UUID.
func (ms *MYSQLStore) SetTrackingNumber(ctx context.Context, orderUUID string, trackingCode string) (*entity.OrderBuyerShipment, error) {
	order, err := getOrderByUUID(ctx, ms, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}

	orderStatus, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
	if !ok {
		return nil, fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
	}

	if !(orderStatus.Name == entity.Confirmed || orderStatus.Name == entity.Shipped) {
		return nil, fmt.Errorf("bad order status for setting tracking number must be confirmed got: %s", orderStatus.Name)
	}

	shipment, err := getOrderShipment(ctx, ms, order.ID)
	if err != nil {
		return nil, fmt.Errorf("can't get order shipment: %w", err)
	}

	shipment.TrackingCode = sql.NullString{
		String: trackingCode,
		Valid:  true,
	}

	buyer, err := getBuyerById(ctx, ms, order.BuyerID)
	if err != nil {
		return nil, fmt.Errorf("can't get buyer by id: %w", err)
	}

	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		err = updateOrderShipment(ctx, rep, order.ID, shipment)
		if err != nil {
			return fmt.Errorf("can't update order shipment: %w", err)
		}

		statusShipped, ok := ms.cache.GetOrderStatusByName(entity.Shipped)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Shipped)
		}

		err = updateOrderStatus(ctx, rep, order.ID, statusShipped.ID)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("can't set tracking number: %w", err)
	}
	return &entity.OrderBuyerShipment{
		Order:    order,
		Buyer:    buyer,
		Shipment: shipment,
	}, nil
}

func paymentsByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[string]*entity.Payment, error) {
	if len(orderIds) == 0 {
		return map[string]*entity.Payment{}, nil
	}

	query := `
	SELECT 
		customer_order.uuid as order_uuid,
		payment.id, 
		payment.payment_method_id,
		payment.transaction_id, 
		payment.transaction_amount, 
		payment.transaction_amount_payment_currency,
		payment.payer, 
		payment.payee, 
		payment.is_transaction_done,
		payment.created_at,
		payment.modified_at
	FROM payment
	JOIN customer_order ON payment.id = customer_order.payment_id
	WHERE customer_order.id IN (:orderIds)`

	query, params, err := MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	// Execute the query
	rows, err := rep.DB().QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	payments := make(map[string]*entity.Payment)

	type paymentOrderUUID struct {
		OrderUUID string `db:"order_uuid"`
		*entity.Payment
	}

	for rows.Next() {
		var paymentRow paymentOrderUUID

		err := rows.StructScan(&paymentRow)
		if err != nil {
			return nil, err
		}
		payments[paymentRow.OrderUUID] = paymentRow.Payment
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Check if all order IDs were found
	if len(payments) != len(orderIds) {
		return nil, errors.New("not all order IDs were found")
	}

	return payments, nil
}

func getBuyerById(ctx context.Context, rep dependency.Repository, buyerId int) (*entity.Buyer, error) {
	query := `
	SELECT * FROM buyer WHERE id = :buyerId`
	buyer, err := QueryNamedOne[entity.Buyer](ctx, rep.DB(), query, map[string]interface{}{
		"buyerId": buyerId,
	})
	if err != nil {
		return nil, err
	}
	return &buyer, nil
}

func buyersByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]*entity.Buyer, error) {

	if len(orderIds) == 0 {
		return map[int]*entity.Buyer{}, nil
	}

	query := `
	SELECT 
		customer_order.id AS order_id,
		buyer.id AS id,
		buyer.first_name AS first_name,
		buyer.last_name AS last_name,
		buyer.email AS email,
		buyer.phone AS phone,
		buyer.billing_address_id AS billing_address_id,
		buyer.shipping_address_id AS shipping_address_id,
		subscriber.receive_promo_emails AS receive_promo_emails
	FROM buyer
	JOIN customer_order ON buyer.id = customer_order.buyer_id
	LEFT JOIN subscriber ON buyer.email = subscriber.email
	WHERE customer_order.id IN (:orderIds)`

	query, params, err := MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	rows, err := rep.DB().QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buyers := make(map[int]*entity.Buyer)

	for rows.Next() {
		type buyerOrderId struct {
			OrderID int `db:"order_id"`
			*entity.Buyer
		}
		var buyerRow buyerOrderId

		err := rows.StructScan(&buyerRow)
		if err != nil {
			return nil, err
		}
		buyers[buyerRow.OrderID] = buyerRow.Buyer
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(buyers) != len(orderIds) {
		return nil, errors.New("not all order IDs were found")
	}

	return buyers, nil

}

type addressFull struct {
	shipping *entity.Address
	billing  *entity.Address
}

func addressesByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]addressFull, error) {

	if len(orderIds) == 0 {
		return map[int]addressFull{}, nil
	}

	query := `
	SELECT 
		co.id AS order_id,
		billing.country AS billing_country,
		billing.state AS billing_state,
		billing.city AS billing_city,
		billing.address_line_one AS billing_address_line_one,
		billing.address_line_two AS billing_address_line_two,
		billing.company AS billing_company,
		billing.postal_code AS billing_postal_code,
		shipping.country AS shipping_country,
		shipping.state AS shipping_state,
		shipping.city AS shipping_city,
		shipping.address_line_one AS shipping_address_line_one,
		shipping.address_line_two AS shipping_address_line_two,
		shipping.company AS shipping_company,
		shipping.postal_code AS shipping_postal_code
	FROM 
		customer_order co
	INNER JOIN 
		buyer b ON co.buyer_id = b.id
	INNER JOIN 
		address billing ON b.billing_address_id = billing.id
	INNER JOIN 
		address shipping ON b.shipping_address_id = shipping.id
	WHERE 
		co.id IN (:orderIds)`

	query, params, err := MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	rows, err := rep.DB().QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	addresses := make(map[int]addressFull)

	for rows.Next() {
		var shipping entity.Address
		var billing entity.Address
		var orderId int

		// TODO: rows.StructScan
		err := rows.Scan(
			&orderId,
			&billing.Country,
			&billing.State,
			&billing.City,
			&billing.AddressLineOne,
			&billing.AddressLineTwo,
			&billing.Company,
			&billing.PostalCode,
			&shipping.Country,
			&shipping.State,
			&shipping.City,
			&shipping.AddressLineOne,
			&shipping.AddressLineTwo,
			&shipping.Company,
			&shipping.PostalCode,
		)
		if err != nil {
			return nil, err
		}

		addresses[orderId] = addressFull{
			shipping: &shipping,
			billing:  &billing,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(addresses) != len(orderIds) {
		return nil, errors.New("not all order IDs were found")
	}

	return addresses, nil

}

func getOrderIds(orders []entity.Order) []int {
	var orderIds []int
	for _, order := range orders {
		orderIds = append(orderIds, order.ID)
	}
	return orderIds
}

func promosByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]*entity.PromoCode, error) {
	if len(orderIds) == 0 {
		return map[int]*entity.PromoCode{}, nil
	}

	query := `
    SELECT 
		customer_order.id as order_id,
        promo_code.id, 
        promo_code.code, 
        promo_code.free_shipping, 
        promo_code.discount, 
        promo_code.expiration, 
        promo_code.voucher, 
        promo_code.allowed
    FROM promo_code
    JOIN customer_order ON promo_code.id = customer_order.promo_id
    WHERE customer_order.id IN (:orderIds)`

	query, params, err := MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	// Execute the query
	rows, err := rep.DB().QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	promos := make(map[int]*entity.PromoCode)

	// Iterate over the rows
	for rows.Next() {
		var orderId int
		var promo entity.PromoCode

		// Scan the values into variables
		err := rows.Scan(
			&orderId,
			&promo.ID,
			&promo.Code,
			&promo.FreeShipping,
			&promo.Discount,
			&promo.Expiration,
			&promo.Voucher,
			&promo.Allowed,
		)
		if err != nil {
			return nil, err
		}

		// Populate the map
		promos[orderId] = &promo
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return promos, nil
}

func fetchOrderInfo(ctx context.Context, rep dependency.Repository, orders []entity.Order) ([]entity.OrderFull, error) {

	ids := getOrderIds(orders)

	orderItems, err := getOrdersItems(ctx, rep, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get order items: %w", err)
	}

	payments, err := paymentsByOrderIds(ctx, rep, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get payment by id: %w", err)
	}

	shipments, err := shipmentsByOrderIds(ctx, rep, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get order shipment: %w", err)
	}

	promos, err := promosByOrderIds(ctx, rep, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get order promos: %w", err)
	}

	buyers, err := buyersByOrderIds(ctx, rep, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get buyers order by ids %w", err)
	}

	addressesFull, err := addressesByOrderIds(ctx, rep, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get addresses by id: %w", err)
	}

	ofs := []entity.OrderFull{}

	for _, order := range orders {

		if _, ok := promos[order.ID]; !ok {
			promos[order.ID] = &entity.PromoCode{}
		}
		addrs := addressesFull[order.ID]

		orderIn := order
		of := entity.OrderFull{
			Order:      &orderIn,
			OrderItems: orderItems[order.ID],
			Payment:    payments[order.UUID],
			Shipment:   shipments[order.ID],
			Buyer:      buyers[order.ID],
			PromoCode:  promos[order.ID],
			Billing:    addrs.billing,
			Shipping:   addrs.shipping,
		}
		ofs = append(ofs, of)

	}

	return ofs, nil
}

func (ms *MYSQLStore) GetPaymentByOrderUUID(ctx context.Context, orderUUID string) (*entity.Payment, error) {
	query := `
	SELECT * FROM payment WHERE id = (SELECT payment_id FROM customer_order WHERE uuid = :orderUUID)`
	payment, err := QueryNamedOne[entity.Payment](ctx, ms.DB(), query, map[string]interface{}{
		"orderUUID": orderUUID,
	})
	if err != nil {
		return nil, err
	}
	return &payment, nil
}

// GetOrderItems retrieves all order items for a given order.
func (ms *MYSQLStore) GetOrderById(ctx context.Context, orderId int) (*entity.OrderFull, error) {
	order, err := getOrderById(ctx, ms, orderId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("order is not found")
		}
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}
	ofs, err := fetchOrderInfo(ctx, ms, []entity.Order{*order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
	}
	return &ofs[0], nil
}

func (ms *MYSQLStore) GetOrderFullByUUID(ctx context.Context, uuid string) (*entity.OrderFull, error) {
	order, err := getOrderByUUID(ctx, ms, uuid)
	if err != nil {
		return nil, err
	}
	ofs, err := fetchOrderInfo(ctx, ms, []entity.Order{*order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
	}
	if len(ofs) == 0 {
		return nil, fmt.Errorf("order is not found")
	}

	return &ofs[0], nil
}

func (ms *MYSQLStore) GetOrdersByStatusAndPaymentTypePaged(
	ctx context.Context,
	email string,
	statusId,
	paymentMethodId,
	orderId int,
	lim,
	off int,
	of entity.OrderFactor) ([]entity.Order, error) {

	query := fmt.Sprintf(`
		SELECT 
			co.*
		FROM 
			customer_order co 
		INNER JOIN 
			payment p ON co.payment_id = p.id
		INNER JOIN 
			buyer b ON b.id = co.buyer_id
		WHERE 
			(:status = 0 OR co.order_status_id = :status) 
			AND (:paymentMethod = 0 OR p.payment_method_id = :paymentMethod)
			AND (:email = '' OR b.email = :email)
			AND (:orderId = 0 OR co.id = :orderId)
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
		"limit":         lim,
		"offset":        off,
	}

	orders, err := QueryListNamed[entity.Order](ctx, ms.DB(), query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get orders by status and payment method: %w", err)
	}

	return orders, nil
}

// TODO: reuse getOrdersByStatusPaymentAndEmailPaged
func getOrdersByStatusAndPayment(ctx context.Context, rep dependency.Repository, orderStatusId int, paymentMethodIds ...int) ([]entity.Order, error) {
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
        JOIN payment p ON co.payment_id = p.id
        WHERE co.order_status_id = :status AND p.payment_method_id IN (:paymentMethodIds)
        `
		params["paymentMethodIds"] = paymentMethodIds
	} else {
		query += `
        WHERE co.order_status_id = :status
        `
	}

	orders, err := QueryListNamed[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"status":           orderStatusId,
		"paymentMethodIds": paymentMethodIds,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get orders by status and payment method: %w", err)
	}

	return orders, nil
}

// GetAwaitingPaymentsByPaymentType retrieves all orders with the status "awaiting payment"
// and the given payment method if payment method is not provided it returns all orders with the status "awaiting payment".
func (ms *MYSQLStore) GetAwaitingPaymentsByPaymentType(ctx context.Context, pmn ...entity.PaymentMethodName) ([]entity.PaymentOrderUUID, error) {

	pmIds := []int{}
	for _, pmn := range pmn {
		pm, ok := ms.cache.GetPaymentMethodByName(pmn)
		if ok {
			pmIds = append(pmIds, pm.ID)
		}
	}

	os, ok := ms.cache.GetOrderStatusByName(entity.AwaitingPayment)
	if !ok {
		return nil, fmt.Errorf("order status is not exists: order status id %v", entity.AwaitingPayment)
	}

	orders, err := getOrdersByStatusAndPayment(ctx, ms, os.ID, pmIds...)
	if err != nil {
		return nil, err
	}

	oids := []int{}
	for _, o := range orders {
		oids = append(oids, o.ID)
	}

	mpo, err := paymentsByOrderIds(ctx, ms, oids)
	if err != nil {
		return nil, fmt.Errorf("can't get payments by order ids: %w", err)
	}

	poids := []entity.PaymentOrderUUID{}
	for oUUID, p := range mpo {
		poids = append(poids, entity.PaymentOrderUUID{
			OrderUUID: oUUID,
			Payment:   *p,
		})
	}

	return poids, nil
}

func (ms *MYSQLStore) ExpireOrderPayment(ctx context.Context, orderUUID string) (*entity.Payment, error) {

	payment := &entity.Payment{}
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUID(ctx, rep, orderUUID)
		if err != nil {
			return err
		}

		os, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		}

		// get payment by order id
		payment, err = ms.GetPaymentByOrderUUID(ctx, order.UUID)
		if err != nil {
			return fmt.Errorf("can't get payment by order id: %w", err)
		}

		// if order status is not awaiting payment we can't expire payment
		// because payment is already done, canceled,
		// refunded, delivered or already expired and got status placed
		if os.Name != entity.AwaitingPayment {
			slog.DebugContext(ctx, "trying to expire order status is not awaiting payment: order status",
				slog.String("order_status", os.Name.String()),
			)
			return nil
		}

		orderItems, err := getOrderItemsInsert(ctx, rep, order.ID)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		err = rep.Products().RestoreStockForProductSizes(ctx, orderItems)
		if err != nil {
			return fmt.Errorf("can't restore stock for product sizes: %w", err)
		}

		statusPlaced, _ := ms.cache.GetOrderStatusByName(entity.Placed)

		err = updateOrderStatus(ctx, rep, order.ID, statusPlaced.ID)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		// set payment to initial state
		pi := &entity.PaymentInsert{
			PaymentMethodID:                  payment.PaymentMethodID,
			TransactionID:                    sql.NullString{Valid: false},
			TransactionAmount:                decimal.Zero,
			TransactionAmountPaymentCurrency: decimal.Zero,
			Payer:                            sql.NullString{Valid: true},
			Payee:                            sql.NullString{Valid: true},
			IsTransactionDone:                false,
		}

		err = updateOrderPayment(ctx, rep, order.PaymentID, pi)
		if err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return payment, err
}

func (ms *MYSQLStore) OrderPaymentDone(ctx context.Context, orderUUID string, p *entity.Payment) (*entity.Payment, error) {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		order, err := getOrderByUUID(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		if order.PromoID.Int32 != 0 {
			err := rep.Promo().DisableVoucher(ctx, order.PromoID)
			if err != nil {
				return fmt.Errorf("can't disable voucher: %w", err)
			}
		}

		os, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		}

		if os.Name != entity.AwaitingPayment {
			return nil
		}

		statusConfirmed, _ := ms.cache.GetOrderStatusByName(entity.Confirmed)

		err = updateOrderStatus(ctx, rep, order.ID, statusConfirmed.ID)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		p.PaymentInsert.IsTransactionDone = true

		err = updateOrderPayment(ctx, rep, p.ID, &p.PaymentInsert)
		if err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		return nil
	})
	if err != nil {
		return p, err
	}

	return p, nil
}

// TODO:
func (ms *MYSQLStore) RefundOrder(ctx context.Context, orderUUID string) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		order, err := getOrderByUUID(ctx, rep, orderUUID)
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

		err = updateOrderStatus(ctx, rep, order.ID, statusShipped.ID)
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

// TODO:
func (ms *MYSQLStore) DeliveredOrder(ctx context.Context, orderUUID string) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		order, err := getOrderByUUID(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		orderStatus, ok := ms.cache.GetOrderStatusById(order.OrderStatusID)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusID)
		}

		if orderStatus.Name != entity.Shipped && orderStatus.Name != entity.Confirmed {
			return fmt.Errorf("order status can be only in (Confirmed, Shipped): order status %s", orderStatus.Name)
		}

		statusDelivered, ok := ms.cache.GetOrderStatusByName(entity.Delivered)
		if !ok {
			return fmt.Errorf("order status is not exists: order status name %s", entity.Refunded)
		}

		err = updateOrderStatus(ctx, rep, order.ID, statusDelivered.ID)
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

func removePromo(ctx context.Context, rep dependency.Repository, orderId int) error {
	query := `UPDATE customer_order SET promo_id = NULL WHERE id = :orderId`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return fmt.Errorf("can't remove promo: %w", err)
	}
	return nil
}

func cancelOrder(ctx context.Context, rep dependency.Repository, orderFull *entity.OrderFull) error {
	orderStatus, ok := rep.Cache().GetOrderStatusById(orderFull.Order.OrderStatusID)
	if !ok {
		return fmt.Errorf("order status is not exists: order status id %d", orderFull.Order.OrderStatusID)
	}
	st := orderStatus.Name
	if st == entity.Cancelled {
		return nil
	}

	if st == entity.Refunded ||
		st == entity.Delivered ||
		st == entity.Shipped ||
		st == entity.Confirmed {
		return fmt.Errorf("order status can't be canceled: order status %s", st)
	}

	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)
	if st == entity.AwaitingPayment {
		err := rep.Products().RestoreStockForProductSizes(ctx, items)
		if err != nil {
			return fmt.Errorf("can't restore stock for product sizes: %w", err)
		}
	}

	// err := deleteOrderItems(ctx, rep, orderFull.Order.ID)
	// if err != nil {
	// 	return fmt.Errorf("can't delete order items: %w", err)
	// }

	// err = setZeroTotal(ctx, rep, orderFull.Order.ID)
	// if err != nil {
	// 	return fmt.Errorf("can't set zero total: %w", err)
	// }

	if orderFull.PromoCode.ID != 0 {
		err := removePromo(ctx, rep, orderFull.Order.ID)
		if err != nil {
			return fmt.Errorf("can't remove promo: %w", err)
		}
	}

	statusCancelled, ok := rep.Cache().GetOrderStatusByName(entity.Cancelled)
	if !ok {
		return fmt.Errorf("can't get order status by name %s", entity.Cancelled)
	}

	err := updateOrderStatus(ctx, rep, orderFull.Order.ID, statusCancelled.ID)
	if err != nil {
		return fmt.Errorf("can't update order status: %w", err)
	}

	return nil

}

func (ms *MYSQLStore) CancelOrder(ctx context.Context, orderUUID string) error {
	orderFull, err := ms.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get order by id: %w", err)
	}

	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		err = cancelOrder(ctx, rep, orderFull)
		if err != nil {
			return fmt.Errorf("can't cancel order: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}
