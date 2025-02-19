package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"log/slog"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
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
		return nil, errors.New("zero items to validate")
	}

	// Get product IDs from items
	prdIds := getProductIdsFromItems(items)

	// Get product details by IDs
	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	// Create a map for product details with ProductId as the key
	prdMap := make(map[int]entity.Product)
	for _, prd := range prds {
		prdMap[prd.Id] = prd
	}

	// Get product sizes (stock) details by item details
	prdSizes, err := getProductsSizesByIds(ctx, rep, items)
	if err != nil {
		return nil, fmt.Errorf("can't get products sizes by ids: %w", err)
	}

	// Create a map for product sizes with a combination of ProductId and SizeId as the key
	prdSizeMap := make(map[string]entity.ProductSize)
	for _, prdSize := range prdSizes {
		key := fmt.Sprintf("%d-%d", prdSize.ProductId, prdSize.SizeId)
		prdSizeMap[key] = prdSize
	}

	// Initialize a slice to store the valid order items
	validItems := make([]entity.OrderItem, 0, len(items))

	for _, item := range items {
		// Create a key for the current item to look up in the prdSizeMap
		sizeKey := fmt.Sprintf("%d-%d", item.ProductId, item.SizeId)
		prdSize, exists := prdSizeMap[sizeKey]

		// Check if the size exists and if the quantity is available
		if !exists || !prdSize.QuantityDecimal().GreaterThan(decimal.Zero) {
			continue
		}

		// Adjust quantity if necessary
		if item.QuantityDecimal().GreaterThan(prdSize.QuantityDecimal()) {
			item.Quantity = prdSize.QuantityDecimal()
		}

		// Look up the product in the prdMap
		prd, exists := prdMap[item.ProductId]
		if !exists {
			continue
		}

		// Set price and sale percentage from product details
		item.ProductPrice = prd.PriceDecimal()
		if prd.SalePercentageDecimal().GreaterThan(decimal.Zero) {
			item.ProductSalePercentage = prd.SalePercentageDecimal()
			item.ProductPriceWithSale = prd.PriceDecimal().Mul(decimal.NewFromInt(100).Sub(prd.SalePercentageDecimal()).Div(decimal.NewFromInt(100)))
		} else {
			item.ProductPriceWithSale = prd.PriceDecimal()
		}

		validItem := entity.OrderItem{
			OrderItemInsert: item,
			Thumbnail:       prd.ThumbnailMediaURL,
			BlurHash:        prd.BlurHash.String,
			ProductName:     prd.Name,
			ProductBrand:    prd.Brand,
			Color:           prd.Color,
			SKU:             prd.SKU,
			Slug:            dto.GetProductSlug(prd.Id, prd.Brand, prd.Name, prd.TargetGender.String()),
			TopCategoryId:   prd.TopCategoryId,
			SubCategoryId:   int(prd.SubCategoryId.Int32),
			TypeId:          int(prd.TypeId.Int32),
			TargetGender:    prd.TargetGender,
			Preorder:        prd.Preorder,
		}

		validItems = append(validItems, validItem)
	}

	return validItems, nil
}

// compareItems return true if items are equal
func compareItems(items, validItems []entity.OrderItemInsert, onlyQuantity bool) bool {
	// Sort both slices
	sort.Sort(entity.OrderItemsByProductId(items))
	sort.Sort(entity.OrderItemsByProductId(validItems))

	// Compare lengths
	if len(items) != len(validItems) {
		return false
	}

	// Compare each element
	for i := range items {
		if onlyQuantity {
			// Compare only ProductId, SizeId, and Quantity
			if items[i].ProductId != validItems[i].ProductId ||
				items[i].SizeId != validItems[i].SizeId ||
				items[i].QuantityDecimal().Cmp(validItems[i].QuantityDecimal()) != 0 {
				return false
			}
		} else {
			// Compare all fields of OrderItemInsert
			if items[i].ProductId != validItems[i].ProductId ||
				items[i].ProductPriceDecimal().Cmp(validItems[i].ProductPriceDecimal()) != 0 ||
				items[i].ProductSalePercentageDecimal().Cmp(validItems[i].ProductSalePercentageDecimal()) != 0 ||
				items[i].QuantityDecimal().Cmp(validItems[i].QuantityDecimal()) != 0 ||
				items[i].SizeId != validItems[i].SizeId {
				return false
			}
		}
	}
	return true
}

func calculateTotalAmount(ctx context.Context, rep dependency.Repository, items []entity.ProductInfoProvider) (decimal.Decimal, error) {
	if len(items) == 0 {
		return decimal.Zero, errors.New("no items to calculate total amount")
	}

	// Merge items by ProductId, ignoring size
	itemsNoSizeId := make([]entity.OrderItemInsert, 0, len(items))
	for _, item := range items {
		itemsNoSizeId = append(itemsNoSizeId, entity.OrderItemInsert{
			ProductId: item.GetProductId(),
			Quantity:  item.GetQuantity(),
		})
	}
	itemsNoSizeId = mergeOrderItems(itemsNoSizeId)

	// Build SQL query
	var (
		caseBuilder strings.Builder
		idsBuilder  strings.Builder
	)

	caseBuilder.Grow(len(itemsNoSizeId) * 20) // Pre-allocate space
	idsBuilder.Grow(len(itemsNoSizeId) * 10)  // Pre-allocate space

	first := true
	for _, item := range itemsNoSizeId {
		if !item.QuantityDecimal().IsPositive() {
			return decimal.Zero, fmt.Errorf("quantity for product ID %d is not positive", item.ProductId)
		}

		if !first {
			caseBuilder.WriteString(" ")
			idsBuilder.WriteString(", ")
		}
		first = false

		caseBuilder.WriteString(fmt.Sprintf("WHEN product.id = %d THEN %s", item.ProductId, item.QuantityDecimal().String()))
		idsBuilder.WriteString(fmt.Sprintf("%d", item.ProductId))
	}

	query := fmt.Sprintf(`
		SELECT SUM(price * (1 - COALESCE(sale_percentage, 0) / 100) * CASE %s END) AS total_amount
		FROM product
		WHERE id IN (%s)
	`, caseBuilder.String(), idsBuilder.String())

	var totalAmount decimal.Decimal
	if err := rep.DB().GetContext(ctx, &totalAmount, query); err != nil {
		return decimal.Zero, err
	}

	return totalAmount.Round(2), nil
}

func insertAddresses(ctx context.Context, rep dependency.Repository, shippingAddress, billingAddress *entity.AddressInsert) (int, int, error) {
	// Query to insert an address
	query := `
		INSERT INTO address (country, state, city, address_line_one, address_line_two, company, postal_code)
		VALUES (:country, :state, :city, :address_line_one, :address_line_two, :company, :postal_code);
	`

	// Helper function to execute the insertion and return the last inserted ID
	insertAddress := func(address *entity.AddressInsert) (int64, error) {
		result, err := rep.DB().NamedExecContext(ctx, query, address)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}

	// If shipping and billing addresses are the same, insert once and use the same ID
	if *shippingAddress == *billingAddress {
		id, err := insertAddress(shippingAddress)
		if err != nil {
			return 0, 0, err
		}
		return int(id), int(id), nil
	}

	// Otherwise, insert both addresses
	shippingID, err := insertAddress(shippingAddress)
	if err != nil {
		return 0, 0, err
	}

	billingID, err := insertAddress(billingAddress)
	if err != nil {
		return 0, 0, err
	}

	return int(shippingID), int(billingID), nil
}

func insertBuyer(ctx context.Context, rep dependency.Repository, b *entity.BuyerInsert, sAdr, bAdr int) error {
	query := `
	INSERT INTO buyer 
		(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
	VALUES 
		(:orderId, :firstName, :lastName, :email, :phone, :billingAddressId, :shippingAddressId)
	`

	err := ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"orderId":           b.OrderId,
		"firstName":         b.FirstName,
		"lastName":          b.LastName,
		"email":             b.Email,
		"phone":             b.Phone,
		"billingAddressId":  bAdr,
		"shippingAddressId": sAdr,
	})
	if err != nil {
		return fmt.Errorf("can't insert buyer: %w", err)
	}

	return nil
}

func insertPaymentRecord(ctx context.Context, rep dependency.Repository, paymentMethodId, orderId int) error {

	insertQuery := `
		INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done)
		VALUES (:orderId, :paymentMethodId, 0, 0, false);
	`

	err := ExecNamed(ctx, rep.DB(), insertQuery, map[string]interface{}{
		"orderId":         orderId,
		"paymentMethodId": paymentMethodId,
	})
	if err != nil {
		return fmt.Errorf("can't insert payment record: %w", err)
	}

	return nil
}

func insertOrderItems(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, orderID int) error {
	if len(items) == 0 {
		return fmt.Errorf("no order items to insert")
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row := map[string]any{
			"order_id":                orderID,
			"product_id":              item.ProductId,
			"product_price":           item.ProductPriceDecimal(),
			"product_sale_percentage": item.ProductSalePercentageDecimal(),
			"quantity":                item.QuantityDecimal(),
			"size_id":                 item.SizeId,
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

func insertShipment(ctx context.Context, rep dependency.Repository, sc *entity.ShipmentCarrier, orderId int) error {
	query := `
	INSERT INTO shipment (carrier_id, order_id, cost)
	VALUES (:carrierId, :orderId, :cost)
	`
	err := ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"carrierId": sc.Id,
		"orderId":   orderId,
		"cost":      sc.PriceDecimal(),
	})
	if err != nil {
		return fmt.Errorf("can't insert shipment: %w", err)
	}
	return nil
}

func insertOrder(ctx context.Context, rep dependency.Repository, order *entity.Order) (int, string, error) {
	var err error
	query := `
	INSERT INTO customer_order
	 (uuid, total_price, order_status_id, promo_id)
	 VALUES (:uuid, :totalPrice, :orderStatusId, :promoId)
	 `

	uuid := uuid.New().String()
	order.Id, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"uuid":          uuid,
		"totalPrice":    order.TotalPriceDecimal(),
		"orderStatusId": order.OrderStatusId,
		"promoId":       order.PromoId,
	})
	if err != nil {
		return 0, "", fmt.Errorf("can't insert order: %w", err)
	}
	return order.Id, uuid, nil
}

// mergeOrderItems merges the order items by summing up the quantities of items with the same product ID and size ID.
// It skips items with zero quantity and returns a new slice of merged order items.
func mergeOrderItems(items []entity.OrderItemInsert) []entity.OrderItemInsert {
	type itemKey struct {
		ProductId int
		SizeId    int
	}

	mergedItems := make(map[itemKey]entity.OrderItemInsert)

	for _, item := range items {
		if item.Quantity.IsZero() {
			continue // Skip items with zero quantity
		}

		key := itemKey{ProductId: item.ProductId, SizeId: item.SizeId}

		if existingItem, ok := mergedItems[key]; ok {
			// Update the quantity by adding the quantities
			existingItem.Quantity = existingItem.QuantityDecimal().Add(item.QuantityDecimal())
			mergedItems[key] = existingItem
		} else {
			mergedItems[key] = item
		}
	}

	// Pre-allocate the slice with the exact size of the map
	mergedSlice := make([]entity.OrderItemInsert, 0, len(mergedItems))
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
		if item.QuantityDecimal().Cmp(maxQuantity) > 0 {
			items[i].Quantity = maxQuantity.Round(0)
		}
	}
	return items
}

func (ms *MYSQLStore) validateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert) ([]entity.OrderItem, error) {

	// adjust quantities if it exceeds the maxOrderItemPerSize
	items = adjustQuantities(cache.GetMaxOrderItems(), items)

	slog.Default().InfoContext(ctx, "items", slog.Any("items", items))

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
func (ms *MYSQLStore) ValidateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert) (*entity.OrderItemValidation, error) {
	// Return early if there are no items
	if len(items) == 0 {
		return nil, fmt.Errorf("no order items to insert")
	}

	// Make a copy of the original items to avoid modifying the input slice
	copiedItems := make([]entity.OrderItemInsert, len(items))
	copy(copiedItems, items)

	// Merge order items by product id and size id on the copied items
	mergedItems := mergeOrderItems(copiedItems)

	// Validate the merged order items
	validItems, err := ms.validateOrderItemsInsert(ctx, mergedItems)
	if err != nil {
		return nil, fmt.Errorf("error while validating order items: %w", err)
	}

	// Return early if no valid items
	if len(validItems) == 0 {
		return nil, fmt.Errorf("zero valid order items to insert")
	}

	// Convert valid items for further processing
	validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

	// Convert to providers and calculate total
	providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
	total, err := calculateTotalAmount(ctx, ms, providers)
	if err != nil {
		return nil, fmt.Errorf("error while calculating total amount: %w", err)
	}

	// Return early if total is zero
	if total.IsZero() {
		return nil, fmt.Errorf("total amount is zero")
	}

	// Compare the original (copied) and valid items and return the validation result
	return &entity.OrderItemValidation{
		ValidItems: validItems,
		Subtotal:   total.Round(2),
		HasChanged: !compareItems(copiedItems, validItemsInsert, true),
	}, nil
}

func (ms *MYSQLStore) ValidateOrderByUUID(ctx context.Context, uuid string) (*entity.OrderFull, error) {
	// Retrieve the full order by UUID
	orderFull, err := ms.GetOrderFullByUUID(ctx, uuid)
	if err != nil {
		return nil, fmt.Errorf("error while getting order by uuid: %w", err)
	}

	// Check the order status from the cache
	oStatus, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
	if !ok {
		return nil, fmt.Errorf("order status does not exist")
	}

	// If the order status is not 'Placed', return the order as is
	if oStatus.Status.Name != entity.Placed {
		return orderFull, nil
	}

	// Convert order items for validation
	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)

	// Begin a transaction for order validation
	var customErr error
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Validate the order items
		oiv, err := rep.Order().ValidateOrderItemsInsert(ctx, items)
		if err != nil {
			// If validation fails, cancel the order
			if cancelErr := cancelOrder(ctx, rep, &orderFull.Order, entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)); cancelErr != nil {
				return fmt.Errorf("cannot cancel order while applying promo code: %w", cancelErr)
			}
			return fmt.Errorf("error while validating order items: %w", err)
		}

		// Convert the valid items back to their insert form
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(oiv.ValidItems)

		// If the validated items differ from the original items, update them
		if !compareItems(items, validItemsInsert, false) {
			// Update the order items
			if err := updateOrderItems(ctx, rep, validItemsInsert, orderFull.Order.Id); err != nil {
				return fmt.Errorf("error while updating order items: %w", err)
			}

			// Update the total amount based on the new items
			if _, err := updateTotalAmount(ctx, rep, orderFull.Order.Id, oiv.SubtotalDecimal(), orderFull.PromoCode, orderFull.Shipment); err != nil {
				return fmt.Errorf("error while updating total amount: %w", err)
			}

			// Set a custom error to indicate the items were updated
			customErr = fmt.Errorf("order items are not valid and were updated")
			return nil
		}

		return nil
	})

	// If the transaction encountered an error, return it
	if err != nil {
		return nil, err
	}

	// If a custom error was set during validation, return it
	if customErr != nil {
		return nil, customErr
	}

	// Return the fully validated order
	return orderFull, nil
}

// CreateOrder creates a new order with the provided details
func (ms *MYSQLStore) CreateOrder(ctx context.Context, orderNew *entity.OrderNew, receivePromo bool) (*entity.Order, bool, error) {

	// Validate order input
	if err := validateOrderInput(orderNew); err != nil {
		return nil, false, err
	}

	paymentMethod, ok := cache.GetPaymentMethodByName(orderNew.PaymentMethod)
	if !ok || !paymentMethod.Method.Allowed {
		return nil, false, fmt.Errorf("payment method does not exist")
	}

	shipmentCarrier, ok := cache.GetShipmentCarrierById(orderNew.ShipmentCarrierId)
	if !ok || !shipmentCarrier.Allowed {
		return nil, false, fmt.Errorf("shipment carrier does not exist")
	}

	// Initialize variables
	order := &entity.Order{}
	sendEmail := false

	// Execute the transaction
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		// Merge and validate order items
		orderNew.Items = mergeOrderItems(orderNew.Items)

		promo, ok := cache.GetPromoByCode(orderNew.PromoCode)
		if !ok || !promo.IsAllowed() {
			promo = entity.PromoCode{}
		}
		prId := sql.NullInt32{
			Int32: int32(promo.Id),
			Valid: promo.Id > 0,
		}

		oiv, err := rep.Order().ValidateOrderItemsInsert(ctx, orderNew.Items)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(oiv.ValidItems)

		totalPrice := promo.SubtotalWithPromo(oiv.Subtotal, shipmentCarrier.PriceDecimal())

		order = &entity.Order{
			TotalPrice:    totalPrice,
			PromoId:       prId,
			OrderStatusId: cache.OrderStatusPlaced.Status.Id,
		}

		// Insert order and related entities
		err = ms.insertOrderDetails(ctx, rep, order, validItemsInsert, &shipmentCarrier, orderNew)
		if err != nil {
			return fmt.Errorf("error while inserting order details: %w", err)
		}

		// Handle promotional email subscription
		if receivePromo {
			if err := ms.handlePromoSubscription(ctx, orderNew.Buyer.Email, &sendEmail); err != nil {
				return fmt.Errorf("error while handling promotional subscription: %w", err)
			}
		}

		// Insert payment record
		err = insertPaymentRecord(ctx, rep, paymentMethod.Method.Id, order.Id)
		if err != nil {
			return fmt.Errorf("error while inserting payment record: %w", err)
		}

		return nil
	})

	return order, sendEmail, err
}

// Helper function for validating order input
func validateOrderInput(orderNew *entity.OrderNew) error {
	if len(orderNew.Items) == 0 {
		return fmt.Errorf("no order items to insert")
	}
	if orderNew.ShippingAddress == nil || orderNew.BillingAddress == nil {
		return fmt.Errorf("shipping and billing addresses are required")
	}
	if orderNew.Buyer == nil {
		return fmt.Errorf("buyer is required")
	}
	return nil
}

// Helper function to insert order details
func (ms *MYSQLStore) insertOrderDetails(ctx context.Context, rep dependency.Repository, order *entity.Order, validItemsInsert []entity.OrderItemInsert, carrier *entity.ShipmentCarrier, orderNew *entity.OrderNew) error {
	var err error
	order.Id, order.UUID, err = insertOrder(ctx, rep, order)
	if err != nil {
		return fmt.Errorf("error while inserting final order: %w", err)
	}

	slog.Info("inserting order items", "order_id", order.Id, "items", validItemsInsert)
	if err = insertOrderItems(ctx, rep, validItemsInsert, order.Id); err != nil {
		return fmt.Errorf("error while inserting order items: %w", err)
	}
	if err = insertShipment(ctx, rep, carrier, order.Id); err != nil {
		return fmt.Errorf("error while inserting shipment: %w", err)
	}
	shippingAddressId, billingAddressId, err := insertAddresses(ctx, rep, orderNew.ShippingAddress, orderNew.BillingAddress)
	if err != nil {
		return fmt.Errorf("error while inserting addresses: %w", err)
	}
	orderNew.Buyer.OrderId = order.Id
	if err = insertBuyer(ctx, rep, orderNew.Buyer, shippingAddressId, billingAddressId); err != nil {
		return fmt.Errorf("error while inserting buyer: %w", err)
	}
	return nil
}

// Helper function to handle promotional subscription
func (ms *MYSQLStore) handlePromoSubscription(ctx context.Context, email string, sendEmail *bool) error {
	subscribed, err := ms.Subscribers().IsSubscribed(ctx, email)
	if err != nil {
		return fmt.Errorf("error while checking subscription: %w", err)
	}
	if !subscribed {
		*sendEmail = true
		if err := ms.Subscribers().UpsertSubscription(ctx, email, true); err != nil {
			return fmt.Errorf("error while upserting subscription: %w", err)
		}
	}
	return nil
}

func getOrdersItems(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int][]entity.OrderItem, error) {
	// Return early if no order IDs are provided
	if len(orderIds) == 0 {
		return map[int][]entity.OrderItem{}, nil
	}

	// Optimized SQL query with relevant columns indexed for better performance
	query := `
        SELECT 
			oi.id,
			oi.order_id,
			oi.product_id,
			oi.quantity,
			oi.size_id,
			oi.product_price,
			oi.product_sale_percentage,
			oi.product_price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) AS product_price_with_sale,
			m.thumbnail,
			m.blur_hash,
			p.name AS product_name,
			p.brand AS product_brand,
			p.sku AS product_sku,
			p.color AS color,
			p.top_category_id AS top_category_id,
			p.sub_category_id AS sub_category_id,
			p.type_id AS type_id,
			p.target_gender AS target_gender,
			p.preorder AS preorder
        FROM order_item oi
        JOIN product p ON oi.product_id = p.id
		JOIN media m ON p.thumbnail_id = m.id
        WHERE oi.order_id IN (:orderIds)
    `

	// Fetching the order items using the query and the provided order IDs
	ois, err := QueryListNamed[entity.OrderItem](ctx, rep.DB(), query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, err
	}

	// Create a map with initial capacity to reduce memory allocations
	orderItemsMap := make(map[int][]entity.OrderItem, len(orderIds))

	// Iterate over fetched order items and group them by order ID
	for _, oi := range ois {
		// Generate the slug for each order item
		oi.Slug = dto.GetProductSlug(oi.ProductId, oi.ProductBrand, oi.ProductName, oi.TargetGender.String())
		// Append the order item to the corresponding order ID group
		orderItemsMap[oi.OrderId] = append(orderItemsMap[oi.OrderId], oi)
	}

	return orderItemsMap, nil
}

func getOrderItemsInsert(ctx context.Context, rep dependency.Repository, orderId int) ([]entity.OrderItemInsert, error) {
	// Optimized SQL query, ensuring all selected columns are necessary and indexed properly
	query := `
		SELECT 
			product_id,
			product_price,
			product_sale_percentage,
			product_price * (1 - COALESCE(product_sale_percentage, 0) / 100) AS product_price_with_sale,
			quantity,
			size_id
		FROM order_item 
		WHERE order_id = :orderId
	`

	// Fetching the order items for the specified order ID
	ois, err := QueryListNamed[entity.OrderItemInsert](ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order items by order id: %w", err)
	}

	// Return the fetched order items
	return ois, nil
}

func getOrderShipment(ctx context.Context, rep dependency.Repository, orderId int) (*entity.Shipment, error) {
	query := `
	SELECT 
		s.* 
	FROM shipment s 
	WHERE s.order_id = :orderId`

	s, err := QueryNamedOne[entity.Shipment](ctx, rep.DB(), query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func shipmentsByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]entity.Shipment, error) {
	if len(orderIds) == 0 {
		return map[int]entity.Shipment{}, nil
	}

	query := `
	SELECT 
		s.*
	FROM shipment s 
	WHERE s.order_id IN (:orderIds)`

	params := map[string]interface{}{
		"orderIds": orderIds,
	}

	shipments, err := QueryListNamed[entity.Shipment](ctx, rep.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get shipments by order ids: %w", err)
	}

	sm := make(map[int]entity.Shipment)
	for _, s := range shipments {
		sm[s.OrderId] = s
	}

	return sm, nil

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

func updateOrderPayment(ctx context.Context, rep dependency.Repository, orderId int, payment entity.PaymentInsert) error {
	query := `
	UPDATE payment 
	SET transaction_amount = :transactionAmount,
		transaction_amount_payment_currency = :transactionAmountPaymentCurrency,
		transaction_id = :transactionId,
		is_transaction_done = :isTransactionDone,
		payment_method_id = :paymentMethodId,
		payer = :payer,
		payee = :payee,
		client_secret = :clientSecret
	WHERE order_id = :orderId`

	params := map[string]any{
		"transactionAmount":                payment.TransactionAmount,
		"transactionAmountPaymentCurrency": payment.TransactionAmountPaymentCurrency,
		"transactionId":                    payment.TransactionID,
		"isTransactionDone":                payment.IsTransactionDone,
		"paymentMethodId":                  payment.PaymentMethodID,
		"payer":                            payment.Payer,
		"payee":                            payment.Payee,
		"clientSecret":                     payment.ClientSecret,
		"orderId":                          orderId,
	}

	_, err := rep.DB().NamedExecContext(ctx, query, params)
	if err != nil {
		return fmt.Errorf("failed to update payment: %w", err)
	}

	return nil
}

func (ms *MYSQLStore) UpdateTotalPaymentCurrency(ctx context.Context, orderUUID string, tapc decimal.Decimal) error {
	query := `        
	UPDATE payment 
	SET transaction_amount_payment_currency = :tapc 
	WHERE order_id = (
		SELECT id FROM customer_order 
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
func updateTotalAmount(ctx context.Context, rep dependency.Repository, orderId int, subtotal decimal.Decimal, promo entity.PromoCode, shipment entity.Shipment) (decimal.Decimal, error) {
	// check if promo is allowed and not expired
	if !promo.IsAllowed() {
		promo = entity.PromoCode{}
	}

	subtotal = promo.SubtotalWithPromo(subtotal, shipment.CostDecimal())

	err := updateOrderTotalPromo(ctx, rep, orderId, promo.Id, subtotal)
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

	promoIdNull := sql.NullInt32{
		Int32: int32(promoId),
		Valid: promoId != 0,
	}

	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":    orderId,
		"promoId":    promoIdNull,
		"totalPrice": totalPrice.Round(2),
	})
	if err != nil {
		return err
	}
	return nil
}

func (ms *MYSQLStore) insertOrderInvoice(ctx context.Context, orderUUID string, addrOrSecret string, pm entity.PaymentMethod) (*entity.OrderFull, error) {
	// Retrieve payment method from cache and check validity
	if !pm.Allowed {
		return nil, fmt.Errorf("payment method does not exist or is not allowed: payment method id %v", pm.Id)
	}

	// Get order details by UUID
	orderFull, err := ms.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("cannot get order by UUID %s: %w", orderUUID, err)
	}

	// Check if the order's total price is zero
	if orderFull.Order.TotalPrice.IsZero() {
		if err := cancelOrder(ctx, ms, &orderFull.Order, entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)); err != nil {
			return nil, fmt.Errorf("cannot cancel order: %w", err)
		}
		return nil, fmt.Errorf("total price is zero")
	}

	// Retrieve and validate order status from cache
	orderStatus, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
	if !ok {
		return nil, fmt.Errorf("order status does not exist: order status id %d", orderFull.Order.OrderStatusId)
	}
	if orderStatus.Status.Name != entity.Placed && orderStatus.Status.Name != entity.Cancelled {
		return nil, fmt.Errorf("order status is not placed or cancelled: current status %s", orderStatus.Status.Name)
	}

	// Convert order items to insert format and validate them
	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)
	var customErr error
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		oiv, err := rep.Order().ValidateOrderItemsInsert(ctx, items)
		if err != nil {
			slog.Default().ErrorContext(ctx, "cannot validate order items", slog.String("err", err.Error()))
			if err := cancelOrder(ctx, rep, &orderFull.Order, entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)); err != nil {
				return fmt.Errorf("cannot cancel order: %w", err)
			}
			return fmt.Errorf("error validating order items: %w", err)
		}

		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(oiv.ValidItems)
		if !compareItems(items, validItemsInsert, false) {
			if err := updateOrderItems(ctx, rep, validItemsInsert, orderFull.Order.Id); err != nil {
				return fmt.Errorf("error updating order items: %w", err)
			}
			if _, err := updateTotalAmount(ctx, rep, orderFull.Order.Id, oiv.SubtotalDecimal(), orderFull.PromoCode, orderFull.Shipment); err != nil {
				return fmt.Errorf("error updating total amount: %w", err)
			}
			customErr = fmt.Errorf("order items are not valid and were updated")
			return nil
		}

		// Reduce stock for valid items
		if err := rep.Products().ReduceStockForProductSizes(ctx, validItemsInsert); err != nil {
			return fmt.Errorf("error reducing stock for product sizes: %w", err)
		}

		// Update order payment details based on the payment method
		return ms.processPayment(ctx, rep, orderFull, addrOrSecret, pm)
	})
	if err != nil {
		return nil, err
	}
	if customErr != nil {
		return nil, customErr
	}

	return orderFull, nil
}

// processPayment processes payment details based on the payment method
func (ms *MYSQLStore) processPayment(ctx context.Context, rep dependency.Repository, orderFull *entity.OrderFull, addrOrSecret string, pm entity.PaymentMethod) error {
	orderFull.Payment.PaymentMethodID = pm.Id
	orderFull.Payment.IsTransactionDone = false
	orderFull.Payment.TransactionAmount = orderFull.Order.TotalPriceDecimal()
	orderFull.Payment.TransactionAmountPaymentCurrency = orderFull.Order.TotalPriceDecimal()

	switch pm.Name {

	case entity.USDT_TRON, entity.USDT_TRON_TEST:
		orderFull.Payment.Payee = sql.NullString{String: addrOrSecret, Valid: true}
	case entity.CARD, entity.CARD_TEST:
		orderFull.Payment.ClientSecret = sql.NullString{String: addrOrSecret, Valid: true}
	default:
		return fmt.Errorf("unsupported payment method: %s", pm.Name)
	}

	if err := updateOrderPayment(ctx, rep, orderFull.Order.Id, orderFull.Payment.PaymentInsert); err != nil {
		return fmt.Errorf("cannot update order payment: %w", err)
	}

	// Set order status to "Awaiting Payment"
	if err := updateOrderStatus(ctx, rep, orderFull.Order.Id, cache.OrderStatusAwaitingPayment.Status.Id); err != nil {
		return fmt.Errorf("cannot update order status: %w", err)
	}

	return nil
}

// InsertCryptoInvoice handles crypto-specific invoice insertion
func (ms *MYSQLStore) InsertCryptoInvoice(ctx context.Context, orderUUID string, payeeAddress string, pm entity.PaymentMethod) (*entity.OrderFull, error) {
	return ms.insertOrderInvoice(ctx, orderUUID, payeeAddress, pm)
}

// InsertFiatInvoice handles fiat-specific invoice insertion
func (ms *MYSQLStore) InsertFiatInvoice(ctx context.Context, orderUUID string, clientSecret string, pm entity.PaymentMethod) (*entity.OrderFull, error) {
	return ms.insertOrderInvoice(ctx, orderUUID, clientSecret, pm)
}

func updateOrderShipment(ctx context.Context, rep dependency.Repository, shipment *entity.Shipment) error {
	query := `
    UPDATE shipment
    SET 
        tracking_code = :trackingCode,
        carrier_id = :carrierId,
        shipping_date = :shippingDate,
        estimated_arrival_date = :estimatedArrivalDate
    WHERE order_id = :orderId`

	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":              shipment.OrderId,
		"carrierId":            shipment.CarrierId,
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

	orderStatus, ok := cache.GetOrderStatusById(order.OrderStatusId)
	if !ok {
		return nil, fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusId)
	}

	if !(orderStatus.Status.Name == entity.Confirmed || orderStatus.Status.Name == entity.Shipped) {
		return nil, fmt.Errorf("bad order status for setting tracking number must be confirmed got: %s", orderStatus.Status.Name)
	}

	shipment, err := getOrderShipment(ctx, ms, order.Id)
	if err != nil {
		return nil, fmt.Errorf("can't get order shipment: %w", err)
	}

	shipment.TrackingCode = sql.NullString{
		String: trackingCode,
		Valid:  true,
	}

	buyer, err := getBuyerById(ctx, ms, order.Id)
	if err != nil {
		return nil, fmt.Errorf("can't get buyer by id: %w", err)
	}

	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		err = updateOrderShipment(ctx, rep, shipment)
		if err != nil {
			return fmt.Errorf("can't update order shipment: %w", err)
		}

		err = updateOrderStatus(ctx, rep, order.Id, cache.OrderStatusShipped.Status.Id)
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

func paymentsByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[string]entity.Payment, error) {
	if len(orderIds) == 0 {
		return make(map[string]entity.Payment), nil
	}

	query := `
	SELECT 
		customer_order.uuid as order_uuid,
		payment.id,
		payment.order_id, 
		payment.payment_method_id,
		payment.transaction_id, 
		payment.transaction_amount, 
		payment.transaction_amount_payment_currency,
		payment.payer, 
		payment.payee, 
		payment.client_secret,
		payment.is_transaction_done,
		payment.created_at,
		payment.modified_at
	FROM payment
	JOIN customer_order ON payment.order_id = customer_order.id
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
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	// Preallocate map with capacity of orderIds length to reduce memory reallocations
	payments := make(map[string]entity.Payment, len(orderIds))

	type paymentOrderUUID struct {
		OrderUUID string `db:"order_uuid"`
		entity.Payment
	}

	for rows.Next() {
		var paymentRow paymentOrderUUID

		// Scan the row into the struct
		if err := rows.StructScan(&paymentRow); err != nil {
			return nil, fmt.Errorf("row scan failed: %w", err)
		}
		payments[paymentRow.OrderUUID] = paymentRow.Payment
	}

	// Check for errors during row iteration
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	// Check if all order IDs were found
	if len(payments) != len(orderIds) {
		return nil, fmt.Errorf("not all order IDs were found: expected %d, got %d", len(orderIds), len(payments))
	}

	return payments, nil
}

func getBuyerById(ctx context.Context, rep dependency.Repository, orderId int) (*entity.Buyer, error) {
	query := `
	SELECT * FROM buyer WHERE order_id = :orderId`
	buyer, err := QueryNamedOne[entity.Buyer](ctx, rep.DB(), query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return &buyer, nil
}
func buyersByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]entity.Buyer, error) {
	if len(orderIds) == 0 {
		return make(map[int]entity.Buyer), nil
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
		COALESCE(subscriber.receive_promo_emails, FALSE) AS receive_promo_emails
	FROM buyer
	JOIN customer_order ON buyer.order_id = customer_order.id
	LEFT JOIN subscriber ON buyer.email = subscriber.email
	WHERE customer_order.id IN (:orderIds)`

	bos, err := QueryListNamed[entity.Buyer](ctx, rep.DB(), query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get buyers by order ids: %w", err)
	}

	buyers := make(map[int]entity.Buyer, len(orderIds))
	for _, bo := range bos {
		buyers[bo.OrderId] = bo
	}

	// Check if all order IDs were found
	if len(buyers) != len(orderIds) {
		return nil, fmt.Errorf("not all order IDs were found: expected %d, got %d", len(orderIds), len(buyers))
	}

	return buyers, nil
}

type addressFull struct {
	shipping entity.Address
	billing  entity.Address
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
		buyer b ON co.id = b.order_id
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
			shipping: shipping,
			billing:  billing,
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
	orderIds := make([]int, len(orders))
	for i, order := range orders {
		orderIds[i] = order.Id
	}
	return orderIds
}

func promosByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]entity.PromoCode, error) {
	if len(orderIds) == 0 {
		return map[int]entity.PromoCode{}, nil
	}

	query := `
    SELECT 
		customer_order.id as order_id,
        promo_code.id, 
        promo_code.code, 
        promo_code.free_shipping, 
        promo_code.discount, 
        promo_code.expiration, 
		promo_code.start,
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

	promos := make(map[int]entity.PromoCode)

	// Iterate over the rows
	for rows.Next() {
		var orderId int
		var promo entity.PromoCode

		// Scan the values into variables
		err := rows.Scan(
			&orderId,
			&promo.Id,
			&promo.Code,
			&promo.FreeShipping,
			&promo.Discount,
			&promo.Expiration,
			&promo.Start,
			&promo.Voucher,
			&promo.Allowed,
		)
		if err != nil {
			return nil, err
		}

		// Populate the map
		promos[orderId] = promo
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return promos, nil
}

func fetchOrderInfo(ctx context.Context, rep dependency.Repository, orders []entity.Order) ([]entity.OrderFull, error) {
	ids := getOrderIds(orders)

	var (
		orderItems map[int][]entity.OrderItem
		payments   map[string]entity.Payment
		shipments  map[int]entity.Shipment
		promos     map[int]entity.PromoCode
		buyers     map[int]entity.Buyer
		addresses  map[int]addressFull
	)

	// Use errgroup to handle concurrency and errors more elegantly
	g, ctx := errgroup.WithContext(ctx)

	// Fetch order items
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		orderItems, err = getOrdersItems(ctx, rep, ids)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}
		return nil
	})

	// Fetch payments
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		payments, err = paymentsByOrderIds(ctx, rep, ids)
		if err != nil {
			return fmt.Errorf("can't get payment by id: %w", err)
		}
		return nil
	})

	// Fetch shipments
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		shipments, err = shipmentsByOrderIds(ctx, rep, ids)
		if err != nil {
			return fmt.Errorf("can't get order shipment: %w", err)
		}
		return nil
	})

	// Fetch promos
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		promos, err = promosByOrderIds(ctx, rep, ids)
		if err != nil {
			return fmt.Errorf("can't get order promos: %w", err)
		}
		return nil
	})

	// Fetch buyers
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		buyers, err = buyersByOrderIds(ctx, rep, ids)
		if err != nil {
			return fmt.Errorf("can't get buyers order by ids %w", err)
		}
		return nil
	})

	// Fetch addresses
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		addresses, err = addressesByOrderIds(ctx, rep, ids)
		if err != nil {
			return fmt.Errorf("can't get addresses by id: %w", err)
		}
		return nil
	})

	// Wait for all goroutines to complete
	if err := g.Wait(); err != nil {
		return nil, err
	}

	ofs := make([]entity.OrderFull, 0, len(orders))

	// Assemble OrderFull objects
	for _, order := range orders {
		// Handle missing data gracefully with default values
		if _, ok := promos[order.Id]; !ok {
			promos[order.Id] = entity.PromoCode{}
		}
		orderItemsList := orderItems[order.Id]
		payment := payments[order.UUID]
		shipment := shipments[order.Id]
		buyer := buyers[order.Id]
		addrs := addresses[order.Id]

		ofs = append(ofs, entity.OrderFull{
			Order:      order,
			OrderItems: orderItemsList,
			Payment:    payment,
			Shipment:   shipment,
			Buyer:      buyer,
			PromoCode:  promos[order.Id],
			Billing:    addrs.billing,
			Shipping:   addrs.shipping,
		})
	}

	return ofs, nil
}

func (ms *MYSQLStore) GetPaymentByOrderUUID(ctx context.Context, orderUUID string) (*entity.Payment, error) {
	query := `
    SELECT p.*
    FROM payment p
    JOIN customer_order co ON p.order_id = co.id
    WHERE co.uuid = :orderUUID;`

	payment, err := QueryNamedOne[entity.Payment](ctx, ms.DB(), query, map[string]interface{}{
		"orderUUID": orderUUID,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get payment by order UUID: %w", err)
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
			payment p ON co.id = p.order_id
		INNER JOIN 
			buyer b ON co.id = b.order_id
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
        JOIN payment p ON co.id = p.order_id
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
		pm, ok := cache.GetPaymentMethodByName(pmn)
		if ok {
			pmIds = append(pmIds, pm.Method.Id)
		}
	}

	orders, err := getOrdersByStatusAndPayment(ctx, ms, cache.OrderStatusAwaitingPayment.Status.Id, pmIds...)
	if err != nil {
		return nil, err
	}

	oids := []int{}
	for _, o := range orders {
		oids = append(oids, o.Id)
	}

	mpo, err := paymentsByOrderIds(ctx, ms, oids)
	if err != nil {
		return nil, fmt.Errorf("can't get payments by order ids: %w", err)
	}

	poids := []entity.PaymentOrderUUID{}
	for oUUID, payment := range mpo {
		poids = append(poids, entity.PaymentOrderUUID{
			OrderUUID: oUUID,
			Payment:   payment,
		})
	}

	return poids, nil
}

func (ms *MYSQLStore) ExpireOrderPayment(ctx context.Context, orderUUID string) (*entity.Payment, error) {
	var payment *entity.Payment

	// Use transaction context
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUID(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		// Fetch order status from cache
		orderStatus, ok := cache.GetOrderStatusById(order.OrderStatusId)
		if !ok {
			return fmt.Errorf("order status does not exist: order status id %d", order.OrderStatusId)
		}

		// Check if the order status is not "awaiting payment"
		if orderStatus.Status.Name != entity.AwaitingPayment {
			slog.DebugContext(ctx, "order status is not awaiting payment, no expiration needed",
				slog.String("order_status", orderStatus.PB.String()),
			)
			return nil
		}

		// Get payment by order UUID
		payment, err = ms.GetPaymentByOrderUUID(ctx, order.UUID)
		if err != nil {
			return fmt.Errorf("can't get payment by order id: %w", err)
		}

		// Get order items
		orderItems, err := getOrderItemsInsert(ctx, rep, order.Id)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		// Prepare payment update to initial state
		paymentUpdate := entity.PaymentInsert{
			PaymentMethodID:                  payment.PaymentMethodID,
			TransactionID:                    sql.NullString{Valid: false},
			TransactionAmount:                decimal.Zero,
			TransactionAmountPaymentCurrency: decimal.Zero,
			Payer:                            sql.NullString{Valid: false},
			Payee:                            sql.NullString{Valid: false},
			IsTransactionDone:                false,
		}

		// TODO: check if payment is already done

		// Update order payment
		if err := updateOrderPayment(ctx, rep, order.Id, paymentUpdate); err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		err = cancelOrder(ctx, rep, order, orderItems)
		if err != nil {
			return fmt.Errorf("can't cancel order: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return payment, nil
}

func (ms *MYSQLStore) OrderPaymentDone(ctx context.Context, orderUUID string, p *entity.Payment) (*entity.Payment, error) {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		order, err := getOrderByUUID(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		if order.PromoId.Int32 != 0 {
			err := rep.Promo().DisableVoucher(ctx, order.PromoId)
			if err != nil {
				return fmt.Errorf("can't disable voucher: %w", err)
			}
		}

		os, ok := cache.GetOrderStatusById(order.OrderStatusId)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusId)
		}

		if os.Status.Name != entity.AwaitingPayment {
			return nil
		}

		err = updateOrderStatus(ctx, rep, order.Id, cache.OrderStatusConfirmed.Status.Id)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		p.PaymentInsert.IsTransactionDone = true

		err = updateOrderPayment(ctx, rep, order.Id, p.PaymentInsert)
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

		orderStatus, ok := cache.GetOrderStatusById(order.OrderStatusId)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusId)
		}

		if orderStatus.Status.Name != entity.Delivered {
			return fmt.Errorf("order status can be only in (Confirmed, Delivered): order status %s", orderStatus.Status.Name)
		}

		err = updateOrderStatus(ctx, rep, order.Id, cache.OrderStatusRefunded.Status.Id)
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

		orderStatus, ok := cache.GetOrderStatusById(order.OrderStatusId)
		if !ok {
			return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusId)
		}

		if orderStatus.Status.Name != entity.Shipped && orderStatus.Status.Name != entity.Confirmed {
			return fmt.Errorf("order status can be only in (Confirmed, Shipped): order status %s", orderStatus.Status.Name)
		}

		err = updateOrderStatus(ctx, rep, order.Id, cache.OrderStatusDelivered.Status.Id)
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

func cancelOrder(ctx context.Context, rep dependency.Repository, order *entity.Order, orderItems []entity.OrderItemInsert) error {
	orderStatus, ok := cache.GetOrderStatusById(order.OrderStatusId)
	if !ok {
		return fmt.Errorf("order status is not exists: order status id %d", order.OrderStatusId)
	}
	st := orderStatus.Status.Name
	if st == entity.Cancelled {
		return nil
	}

	if st == entity.Refunded ||
		st == entity.Delivered ||
		st == entity.Shipped ||
		st == entity.Confirmed {
		return fmt.Errorf("order status can't be canceled: order status %s", st)
	}

	if st == entity.AwaitingPayment {
		err := rep.Products().RestoreStockForProductSizes(ctx, orderItems)
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

	if order.PromoId.Int32 != 0 {
		err := removePromo(ctx, rep, int(order.PromoId.Int32))
		if err != nil {
			return fmt.Errorf("can't remove promo: %w", err)
		}
	}

	statusCancelled, ok := cache.GetOrderStatusByName(entity.Cancelled)
	if !ok {
		return fmt.Errorf("can't get order status by name %s", entity.Cancelled)
	}

	err := updateOrderStatus(ctx, rep, order.Id, statusCancelled.Status.Id)
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
		err = cancelOrder(ctx, rep, &orderFull.Order, entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems))
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
