package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
	"unicode"

	"log/slog"

	"crypto/rand"

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

// getProductPrice returns the price for the specified currency from the product's prices array
// Returns an error if the currency is not found
func getProductPrice(prd *entity.Product, currency string) (decimal.Decimal, error) {
	for _, price := range prd.Prices {
		if price.Currency == currency {
			return price.Price, nil
		}
	}
	return decimal.Zero, fmt.Errorf("product %d does not have a price in currency %s", prd.Id, currency)
}

func validateOrderItemsStockAvailability(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, error) {
	// Check if there are no items provided
	if len(items) == 0 {
		return nil, &entity.ValidationError{Message: "zero items to validate"}
	}

	// Get product IDs from items
	prdIds := getProductIdsFromItems(items)

	// We no longer need default language as we return all translations

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
		productBody := &prd.ProductDisplay.ProductBody
		productPrice, err := getProductPrice(&prd, currency)
		if err != nil {
			return nil, &entity.ValidationError{Message: fmt.Sprintf("product %d does not have a price in currency %s", prd.Id, currency)}
		}
		item.ProductPrice = productPrice
		if productBody.SalePercentageDecimal().GreaterThan(decimal.Zero) {
			item.ProductSalePercentage = productBody.SalePercentageDecimal()
			item.ProductPriceWithSale = productPrice.Mul(decimal.NewFromInt(100).Sub(productBody.SalePercentageDecimal()).Div(decimal.NewFromInt(100)))
		} else {
			item.ProductPriceWithSale = productPrice
		}

		// Get the first translation for display (or empty string if no translations)
		var productName string
		if len(productBody.Translations) > 0 {
			productName = productBody.Translations[0].Name
		}

		validItem := entity.OrderItem{
			OrderItemInsert: item,
			Thumbnail:       prd.ProductDisplay.Thumbnail.ThumbnailMediaURL,
			BlurHash:        prd.ProductDisplay.Thumbnail.BlurHash.String,
			ProductBrand:    productBody.ProductBodyInsert.Brand,
			Color:           productBody.ProductBodyInsert.Color,
			SKU:             prd.SKU,
			Slug:            dto.GetProductSlug(prd.Id, productBody.ProductBodyInsert.Brand, productName, productBody.ProductBodyInsert.TargetGender.String()),
			TopCategoryId:   productBody.ProductBodyInsert.TopCategoryId,
			SubCategoryId:   productBody.ProductBodyInsert.SubCategoryId,
			TypeId:          productBody.ProductBodyInsert.TypeId,
			TargetGender:    productBody.ProductBodyInsert.TargetGender,
			Preorder:        productBody.ProductBodyInsert.Preorder,
			Translations:    productBody.Translations,
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

	// Calculate total directly from items (which already have validated prices)
	// Formula: price * (1 - sale_percentage / 100) * quantity
	var totalAmount decimal.Decimal

	for _, item := range items {
		if !item.GetQuantity().IsPositive() {
			return decimal.Zero, &entity.ValidationError{Message: fmt.Sprintf("quantity for product ID %d is not positive", item.GetProductId())}
		}

		// Get the base price
		price := item.GetProductPrice()

		// Apply sale percentage if present
		salePercentage := item.GetProductSalePercentage()
		if salePercentage.GreaterThan(decimal.Zero) {
			price = price.Mul(decimal.NewFromInt(100).Sub(salePercentage).Div(decimal.NewFromInt(100)))
		}

		// Multiply by quantity and add to total
		totalAmount = totalAmount.Add(price.Mul(item.GetQuantity()))
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

// sanitizePhone removes all non-digit characters from the phone number
// and validates that it's between 7-15 digits as required by buyer_chk_2 constraint
func sanitizePhone(phone string) (string, error) {
	// Remove all non-digit characters
	var builder strings.Builder
	for _, r := range phone {
		if unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	sanitized := builder.String()

	// Validate length: must be between 7 and 15 digits
	if len(sanitized) < 7 || len(sanitized) > 15 {
		return "", fmt.Errorf("phone number must be between 7 and 15 digits after sanitization, got %d digits", len(sanitized))
	}

	return sanitized, nil
}

func insertBuyer(ctx context.Context, rep dependency.Repository, b *entity.BuyerInsert, sAdr, bAdr int) error {
	// Sanitize phone number to meet database constraint requirements
	phone, err := sanitizePhone(b.Phone)
	if err != nil {
		return fmt.Errorf("invalid phone number: %w", err)
	}

	query := `
	INSERT INTO buyer 
		(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
	VALUES 
		(:orderId, :firstName, :lastName, :email, :phone, :billingAddressId, :shippingAddressId)
	`

	err = ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"orderId":           b.OrderId,
		"firstName":         b.FirstName,
		"lastName":          b.LastName,
		"email":             b.Email,
		"phone":             phone,
		"billingAddressId":  bAdr,
		"shippingAddressId": sAdr,
	})
	if err != nil {
		return fmt.Errorf("can't insert buyer: %w", err)
	}

	return nil
}

func insertPaymentRecord(ctx context.Context, rep dependency.Repository, paymentMethodId, orderId int, expiredAt time.Time) error {

	insertQuery := `
		INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done, expired_at)
		VALUES (:orderId, :paymentMethodId, 0, 0, false, :expiredAt);
	`

	err := ExecNamed(ctx, rep.DB(), insertQuery, map[string]interface{}{
		"orderId":         orderId,
		"paymentMethodId": paymentMethodId,
		"expiredAt":       sql.NullTime{Time: expiredAt, Valid: true},
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

func insertShipment(ctx context.Context, rep dependency.Repository, sc *entity.ShipmentCarrier, orderId int, currency string) error {
	price, err := sc.PriceDecimal(currency)
	if err != nil {
		return fmt.Errorf("can't get shipment carrier price for currency %s: %w", currency, err)
	}
	query := `
	INSERT INTO shipment (carrier_id, order_id, cost)
	VALUES (:carrierId, :orderId, :cost)
	`
	err = ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"carrierId": sc.Id,
		"orderId":   orderId,
		"cost":      price,
	})
	if err != nil {
		return fmt.Errorf("can't insert shipment: %w", err)
	}
	return nil
}

// generateOrderReference generates a short, human-friendly, unique order reference (ORD-XXXXXXX)
func generateOrderReference() string {
	const (
		prefix   = "ORD-"
		length   = 7
		alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		base     = int64(len(alphabet))
	)
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(base))
		if err != nil {
			panic(err) // Should never happen
		}
		b[i] = alphabet[n.Int64()]
	}
	return prefix + string(b)
}

func insertOrder(ctx context.Context, rep dependency.Repository, order *entity.Order) (int, string, error) {
	var err error
	query := `
	INSERT INTO customer_order
	 (uuid, total_price, currency, order_status_id, promo_id)
	 VALUES (:uuid, :totalPrice, :currency, :orderStatusId, :promoId)
	`

	orderRef := generateOrderReference()
	order.Id, err = ExecNamedLastId(ctx, rep.DB(), query, map[string]interface{}{
		"uuid":          orderRef,
		"totalPrice":    order.TotalPriceDecimal(),
		"currency":      order.Currency,
		"orderStatusId": order.OrderStatusId,
		"promoId":       order.PromoId,
	})
	if err != nil {
		return 0, "", fmt.Errorf("can't insert order: %w", err)
	}
	return order.Id, orderRef, nil
}

// fetchAndMapByOrderId is a generic helper that fetches items and maps them by order ID.
// This eliminates code duplication across multiple functions that follow the same pattern:
// - check if orderIds is empty
// - query with IN clause using QueryListNamed
// - build a map with orderId as key
func fetchAndMapByOrderId[T any](
	ctx context.Context,
	rep dependency.Repository,
	orderIds []int,
	query string,
	extractor func(T) int,
) (map[int]T, error) {
	if len(orderIds) == 0 {
		return map[int]T{}, nil
	}

	items, err := QueryListNamed[T](ctx, rep.DB(), query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, err
	}

	result := make(map[int]T, len(orderIds))
	for _, item := range items {
		result[extractor(item)] = item
	}
	return result, nil
}

// getOrderStatus retrieves and validates that an order status exists in cache.
// Returns the status and an error if not found.
func getOrderStatus(orderStatusId int) (*cache.Status, error) {
	orderStatus, ok := cache.GetOrderStatusById(orderStatusId)
	if !ok {
		return nil, fmt.Errorf("order status does not exist: order status id %d", orderStatusId)
	}
	return &orderStatus, nil
}

// validateOrderStatus validates that an order is in one of the allowed statuses.
// If allowedStatuses is empty, only checks that the status exists.
// Returns the current status and an error if validation fails.
func validateOrderStatus(order *entity.Order, allowedStatuses ...entity.OrderStatusName) (*cache.Status, error) {
	orderStatus, err := getOrderStatus(order.OrderStatusId)
	if err != nil {
		return nil, err
	}

	// If no allowed statuses specified, just return the status
	if len(allowedStatuses) == 0 {
		return orderStatus, nil
	}

	// Check if current status is in allowed list
	for _, allowed := range allowedStatuses {
		if orderStatus.Status.Name == allowed {
			return orderStatus, nil
		}
	}

	return nil, fmt.Errorf("invalid order status '%s', expected one of: %v", orderStatus.Status.Name, allowedStatuses)
}

// validateOrderStatusNot validates that an order is NOT in any of the disallowed statuses.
// Returns the current status and an error if validation fails.
func validateOrderStatusNot(order *entity.Order, disallowedStatuses ...entity.OrderStatusName) (*cache.Status, error) {
	orderStatus, err := getOrderStatus(order.OrderStatusId)
	if err != nil {
		return nil, err
	}

	// Check if current status is in disallowed list
	for _, disallowed := range disallowedStatuses {
		if orderStatus.Status.Name == disallowed {
			return nil, fmt.Errorf("order status cannot be '%s'", disallowed)
		}
	}

	return orderStatus, nil
}

// validateAndUpdateOrderIfNeeded validates order items and updates them if they've changed.
// This eliminates code duplication across multiple functions that follow the same pattern:
// - Validate order items
// - If validation fails, optionally cancel the order
// - Compare validated items with original items
// - If different, update order items and total amount
// Returns (itemsChanged, error)
func validateAndUpdateOrderIfNeeded(
	ctx context.Context,
	rep dependency.Repository,
	orderFull *entity.OrderFull,
	cancelOnValidationFailure bool,
) (bool, error) {
	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)

	// Validate the order items
	oiv, err := rep.Order().ValidateOrderItemsInsert(ctx, items, orderFull.Order.Currency)
	if err != nil {
		// If validation fails and we should cancel, do it
		if cancelOnValidationFailure {
			if cancelErr := cancelOrder(ctx, rep, &orderFull.Order, items, entity.StockChangeSourceOrderCancelled); cancelErr != nil {
				return false, fmt.Errorf("cannot cancel order after validation failure: %w", cancelErr)
			}
		}
		return false, fmt.Errorf("error validating order items: %w", err)
	}

	// Convert the valid items back to their insert form
	validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(oiv.ValidItems)

	// If the validated items differ from the original items, update them
	if !compareItems(items, validItemsInsert, false) {
		// Update the order items
		if err := updateOrderItems(ctx, rep, validItemsInsert, orderFull.Order.Id); err != nil {
			return false, fmt.Errorf("error updating order items: %w", err)
		}

		// Update the total amount based on the new items
		if _, err := updateTotalAmount(ctx, rep, orderFull.Order.Id, oiv.SubtotalDecimal(), orderFull.PromoCode, orderFull.Shipment); err != nil {
			return false, fmt.Errorf("error updating total amount: %w", err)
		}

		return true, nil // Items were changed
	}

	return false, nil // No changes
}

// validatePaymentMethod validates a payment method by name and checks if it's allowed.
// Returns the payment method wrapper or an error if validation fails.
func validatePaymentMethod(pmn entity.PaymentMethodName) (*cache.PaymentMethod, error) {
	pm, ok := cache.GetPaymentMethodByName(pmn)
	if !ok {
		return nil, fmt.Errorf("payment method '%s' does not exist", pmn)
	}
	if !pm.Method.Allowed {
		return nil, fmt.Errorf("payment method '%s' is not allowed", pmn)
	}
	return &pm, nil
}

// validatePaymentMethodAllowed validates that a payment method is allowed.
// Returns an error if the payment method is not allowed.
func validatePaymentMethodAllowed(pm *entity.PaymentMethod) error {
	if !pm.Allowed {
		return fmt.Errorf("payment method is not allowed: payment method id %d", pm.Id)
	}
	return nil
}

// validateShipmentCarrier validates a shipment carrier by ID and checks if it's allowed.
// Returns the shipment carrier or an error if validation fails.
func validateShipmentCarrier(carrierId int) (*entity.ShipmentCarrier, error) {
	carrier, ok := cache.GetShipmentCarrierById(carrierId)
	if !ok {
		return nil, fmt.Errorf("shipment carrier does not exist: carrier id %d", carrierId)
	}
	if !carrier.Allowed {
		return nil, fmt.Errorf("shipment carrier is not allowed: carrier id %d", carrierId)
	}
	return &carrier, nil
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

func (ms *MYSQLStore) validateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, error) {

	// adjust quantities if it exceeds the maxOrderItemPerSize
	items = adjustQuantities(cache.GetMaxOrderItems(), items)

	slog.Default().InfoContext(ctx, "items", slog.Any("items", items))

	// validate items stock availability
	validItems, err := validateOrderItemsStockAvailability(ctx, ms, items, currency)
	if err != nil {
		// Preserve ValidationError, wrap other errors
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			return nil, err
		}
		return nil, fmt.Errorf("error while validating order items: %w", err)
	}
	if len(validItems) == 0 {
		return nil, &entity.ValidationError{Message: "no valid order items: products or sizes not found, or out of stock"}
	}

	return validItems, nil
}

// ValidateOrderItemsInsert validates the order items and returns the valid items and the total amount
func (ms *MYSQLStore) ValidateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert, currency string) (*entity.OrderItemValidation, error) {
	// Return early if there are no items
	if len(items) == 0 {
		return nil, &entity.ValidationError{Message: "no order items to insert"}
	}

	// Make a copy of the original items to avoid modifying the input slice
	copiedItems := make([]entity.OrderItemInsert, len(items))
	copy(copiedItems, items)

	// Merge order items by product id and size id on the copied items
	mergedItems := mergeOrderItems(copiedItems)

	// Validate the merged order items with the specified currency
	validItems, err := ms.validateOrderItemsInsert(ctx, mergedItems, currency)
	if err != nil {
		// Preserve ValidationError, wrap other errors
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			return nil, err
		}
		return nil, fmt.Errorf("error while validating order items: %w", err)
	}

	// Return early if no valid items
	if len(validItems) == 0 {
		return nil, &entity.ValidationError{Message: "zero valid order items to insert"}
	}

	// Convert valid items for further processing
	validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

	// Convert to providers and calculate total
	providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
	total, err := calculateTotalAmount(ctx, ms, providers)
	if err != nil {
		// Check if it's a validation error from calculateTotalAmount
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			return nil, err
		}
		return nil, fmt.Errorf("error while calculating total amount: %w", err)
	}

	// Return early if total is zero
	if total.IsZero() {
		return nil, &entity.ValidationError{Message: "total amount is zero"}
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

	// Check the order status - if not 'Placed', return the order as is
	oStatus, err := getOrderStatus(orderFull.Order.OrderStatusId)
	if err != nil {
		return nil, err
	}

	if oStatus.Status.Name != entity.Placed {
		return orderFull, nil
	}

	// Begin a transaction for order validation
	var itemsChanged bool
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		itemsChanged, err = validateAndUpdateOrderIfNeeded(ctx, rep, orderFull, true)
		return err
	})

	// If the transaction encountered an error, return it
	if err != nil {
		return nil, err
	}

	// If items were changed during validation, return an error
	if itemsChanged {
		return nil, fmt.Errorf("order items are not valid and were updated")
	}

	// Return the fully validated order
	return orderFull, nil
}

// CreateOrder creates a new order with the provided details
func (ms *MYSQLStore) CreateOrder(ctx context.Context, orderNew *entity.OrderNew, receivePromo bool, expiredAt time.Time) (*entity.Order, bool, error) {

	// Validate order input
	if err := validateOrderInput(orderNew); err != nil {
		return nil, false, err
	}

	// Validate payment method
	paymentMethod, err := validatePaymentMethod(orderNew.PaymentMethod)
	if err != nil {
		return nil, false, err
	}

	// Validate shipment carrier
	shipmentCarrier, err := validateShipmentCarrier(orderNew.ShipmentCarrierId)
	if err != nil {
		return nil, false, err
	}

	// Initialize variables
	order := &entity.Order{}
	sendEmail := false

	// Execute the transaction
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

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

		// Validate order items with currency-specific pricing
		validItems, err := ms.validateOrderItemsInsert(ctx, orderNew.Items, orderNew.Currency)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		// Calculate total from validated items
		providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
		subtotal, err := calculateTotalAmount(ctx, rep, providers)
		if err != nil {
			return fmt.Errorf("error while calculating total amount: %w", err)
		}

		shipmentPrice, err := shipmentCarrier.PriceDecimal(orderNew.Currency)
		if err != nil {
			return fmt.Errorf("can't get shipment carrier price for currency %s: %w", orderNew.Currency, err)
		}
		totalPrice := promo.SubtotalWithPromo(subtotal, shipmentPrice)

		order = &entity.Order{
			TotalPrice:    totalPrice,
			Currency:      orderNew.Currency,
			PromoId:       prId,
			OrderStatusId: cache.OrderStatusPlaced.Status.Id,
		}

		// Insert order and related entities
		err = ms.insertOrderDetails(ctx, rep, order, validItemsInsert, shipmentCarrier, orderNew)
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
		err = insertPaymentRecord(ctx, rep, paymentMethod.Method.Id, order.Id, expiredAt)
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
	if err = insertShipment(ctx, rep, carrier, order.Id, order.Currency); err != nil {
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

	*sendEmail = true

	isSubscribed, err := ms.Subscribers().UpsertSubscription(ctx, email, true)
	if err != nil {
		return fmt.Errorf("error while upserting subscription: %w", err)
	}

	if !isSubscribed {
		*sendEmail = false
	}

	return nil
}

func getOrdersItems(ctx context.Context, rep dependency.Repository, orderIds ...int) (map[int][]entity.OrderItem, error) {
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

	// Extract product IDs to fetch translations
	productIds := make([]int, 0, len(ois))
	for _, oi := range ois {
		productIds = append(productIds, oi.ProductId)
	}

	// Fetch all translations for these products
	translationMap, err := fetchProductTranslations(ctx, rep.DB(), productIds)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	// Create a map with initial capacity to reduce memory allocations
	orderItemsMap := make(map[int][]entity.OrderItem, len(orderIds))

	// Iterate over fetched order items and group them by order ID
	for _, oi := range ois {
		// Set translations for this order item
		oi.Translations = translationMap[oi.ProductId]

		productName := "product"
		if len(translationMap[oi.ProductId]) > 0 {
			productName = translationMap[oi.ProductId][0].Name
		}

		// Generate the slug for each order item
		oi.Slug = dto.GetProductSlug(oi.ProductId, oi.ProductBrand, productName, oi.TargetGender.String())
		// Append the order item to the corresponding order ID group
		orderItemsMap[oi.OrderId] = append(orderItemsMap[oi.OrderId], oi)
	}

	return orderItemsMap, nil
}

type refundedQuantityRow struct {
	OrderItemId      int   `db:"order_item_id"`
	OrderId          int   `db:"order_id"`
	QuantityRefunded int64 `db:"quantity_refunded"`
}

func getRefundedQuantitiesByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[int]map[int]int64, error) {
	if len(orderIds) == 0 {
		return map[int]map[int]int64{}, nil
	}
	query := `
		SELECT order_item_id, order_id, SUM(quantity_refunded) AS quantity_refunded
		FROM refunded_order_item
		WHERE order_id IN (:orderIds)
		GROUP BY order_item_id, order_id
	`
	rows, err := QueryListNamed[refundedQuantityRow](ctx, rep.DB(), query, map[string]any{"orderIds": orderIds})
	if err != nil {
		return nil, fmt.Errorf("get refunded quantities: %w", err)
	}
	result := make(map[int]map[int]int64)
	for _, r := range rows {
		if result[r.OrderId] == nil {
			result[r.OrderId] = make(map[int]int64)
		}
		result[r.OrderId][r.OrderItemId] = r.QuantityRefunded
	}
	return result, nil
}

func mergeRefundedOrderItems(orderItems map[int][]entity.OrderItem, refundedByItem map[int]map[int]int64) map[int][]entity.OrderItem {
	result := make(map[int][]entity.OrderItem, len(orderItems))
	for orderId, items := range orderItems {
		qtyMap := refundedByItem[orderId]
		if qtyMap == nil {
			continue
		}
		for _, item := range items {
			if qty := qtyMap[item.Id]; qty > 0 {
				item.Quantity = decimal.NewFromInt(qty)
				result[orderId] = append(result[orderId], item)
			}
		}
	}
	return result
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
	query := `
	SELECT 
		s.*
	FROM shipment s 
	WHERE s.order_id IN (:orderIds)`

	shipments, err := fetchAndMapByOrderId[entity.Shipment](ctx, rep, orderIds, query, func(s entity.Shipment) int {
		return s.OrderId
	})
	if err != nil {
		return nil, fmt.Errorf("can't get shipments by order ids: %w", err)
	}

	return shipments, nil
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

func insertRefundedOrderItems(ctx context.Context, rep dependency.Repository, orderId int, refundedByItem map[int]int64) error {
	for orderItemId, qty := range refundedByItem {
		query := `INSERT INTO refunded_order_item (order_id, order_item_id, quantity_refunded) VALUES (:orderId, :orderItemId, :quantityRefunded)`
		if err := ExecNamed(ctx, rep.DB(), query, map[string]any{
			"orderId":          orderId,
			"orderItemId":      orderItemId,
			"quantityRefunded": qty,
		}); err != nil {
			return fmt.Errorf("insert refunded_order_item: %w", err)
		}
	}
	return nil
}

func updateOrderStatusAndRefundedAmount(ctx context.Context, rep dependency.Repository, orderId int, orderStatusId int, refundedAmount decimal.Decimal) error {
	query := `UPDATE customer_order SET order_status_id = :orderStatusId, refunded_amount = :refundedAmount WHERE id = :orderId`
	err := ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":        orderId,
		"orderStatusId":  orderStatusId,
		"refundedAmount": refundedAmount.Round(2),
	})
	if err != nil {
		return fmt.Errorf("can't update order status and refunded amount: %w", err)
	}
	return nil
}

func refundAmountFromItems(items []entity.OrderItemInsert) decimal.Decimal {
	var sum decimal.Decimal
	for _, item := range items {
		sum = sum.Add(item.ProductPriceWithSale.Mul(item.Quantity).Round(2))
	}
	return sum.Round(2)
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
		client_secret = :clientSecret,
		expired_at = :expiredAt
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
		"expiredAt":                        payment.ExpiredAt,
	}

	slog.Default().InfoContext(ctx, "update order payment", "params", params, "query", query)

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

func (ms *MYSQLStore) insertOrderInvoice(ctx context.Context, orderUUID string, addrOrSecret string, pm entity.PaymentMethod, expiredAt time.Time) (*entity.OrderFull, error) {
	// Validate payment method is allowed
	if err := validatePaymentMethodAllowed(&pm); err != nil {
		return nil, err
	}

	// Get order details by UUID
	orderFull, err := ms.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("cannot get order by UUID %s: %w", orderUUID, err)
	}

	// Check if the order's total price is zero
	if orderFull.Order.TotalPrice.IsZero() {
		if err := cancelOrder(ctx, ms, &orderFull.Order, entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems), entity.StockChangeSourceOrderCancelled); err != nil {
			return nil, fmt.Errorf("cannot cancel order: %w", err)
		}
		return nil, fmt.Errorf("total price is zero")
	}

	// Validate order status is Placed or Cancelled
	_, err = validateOrderStatus(&orderFull.Order, entity.Placed, entity.Cancelled)
	if err != nil {
		return nil, err
	}

	// Convert order items to insert format and validate them
	var itemsChanged bool
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		itemsChanged, err = validateAndUpdateOrderIfNeeded(ctx, rep, orderFull, true)
		if err != nil {
			slog.Default().ErrorContext(ctx, "cannot validate order items", slog.String("err", err.Error()))
			return err
		}

		// If items changed, return early so we don't reduce stock or process payment
		if itemsChanged {
			return nil
		}

		// Reduce stock for valid items
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)
		history := &entity.StockHistoryParams{
			Source:    entity.StockChangeSourceOrderPlaced,
			OrderId:   orderFull.Order.Id,
			OrderUUID: orderFull.Order.UUID,
		}
		if err := rep.Products().ReduceStockForProductSizes(ctx, validItemsInsert, history); err != nil {
			return fmt.Errorf("error reducing stock for product sizes: %w", err)
		}

		// Update order payment details based on the payment method
		return ms.processPayment(ctx, rep, orderFull, addrOrSecret, pm, expiredAt)
	})
	if err != nil {
		return nil, err
	}
	if itemsChanged {
		return nil, fmt.Errorf("order items are not valid and were updated")
	}

	// Refresh order details after updating status
	orderFull, err = ms.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("cannot refresh order details: %w", err)
	}

	return orderFull, nil
}

// processPayment processes payment details based on the payment method
func (ms *MYSQLStore) processPayment(ctx context.Context, rep dependency.Repository, orderFull *entity.OrderFull, addrOrSecret string, pm entity.PaymentMethod, expiredAt time.Time) error {
	orderFull.Payment.PaymentMethodID = pm.Id
	orderFull.Payment.IsTransactionDone = false
	orderFull.Payment.TransactionAmount = orderFull.Order.TotalPriceDecimal()
	orderFull.Payment.TransactionAmountPaymentCurrency = orderFull.Order.TotalPriceDecimal()
	orderFull.Payment.PaymentInsert.ExpiredAt = sql.NullTime{Time: expiredAt, Valid: true}

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
func (ms *MYSQLStore) InsertCryptoInvoice(ctx context.Context, orderUUID string, payeeAddress string, pm entity.PaymentMethod, expiredAt time.Time) (*entity.OrderFull, error) {
	return ms.insertOrderInvoice(ctx, orderUUID, payeeAddress, pm, expiredAt)
}

// InsertFiatInvoice handles fiat-specific invoice insertion
func (ms *MYSQLStore) InsertFiatInvoice(ctx context.Context, orderUUID string, clientSecret string, pm entity.PaymentMethod, expiredAt time.Time) (*entity.OrderFull, error) {
	return ms.insertOrderInvoice(ctx, orderUUID, clientSecret, pm, expiredAt)
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

	// Validate order status is Confirmed or Shipped
	_, err = validateOrderStatus(order, entity.Confirmed, entity.Shipped)
	if err != nil {
		return nil, fmt.Errorf("bad order status for setting tracking number: %w", err)
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

	// Refresh order details after updating status
	order, err = getOrderByUUID(ctx, ms, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't refresh order details: %w", err)
	}

	return &entity.OrderBuyerShipment{
		Order:    order,
		Buyer:    buyer,
		Shipment: shipment,
	}, nil
}

func paymentsByOrderIds(ctx context.Context, rep dependency.Repository, orderIds []int) (map[string]entity.Payment, error) {
	if len(orderIds) == 0 {
		return map[string]entity.Payment{}, nil
	}

	type paymentOrderUUID struct {
		OrderUUID string `db:"order_uuid"`
		entity.Payment
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
		payment.modified_at,
		payment.expired_at
	FROM payment
	JOIN customer_order ON payment.order_id = customer_order.id
	WHERE customer_order.id IN (:orderIds)`

	query, params, err := MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	rows, err := rep.DB().QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	// Preallocate map with capacity of orderIds length to reduce memory reallocations
	payments := make(map[string]entity.Payment, len(orderIds))

	for rows.Next() {
		var paymentRow paymentOrderUUID

		if err := rows.StructScan(&paymentRow); err != nil {
			return nil, fmt.Errorf("row scan failed: %w", err)
		}
		payments[paymentRow.OrderUUID] = paymentRow.Payment
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	// We don't need to check if all order IDs were found because payments might not exist yet
	// for newly created orders

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

	buyers, err := fetchAndMapByOrderId[entity.Buyer](ctx, rep, orderIds, query, func(b entity.Buyer) int {
		return b.OrderId
	})
	if err != nil {
		return nil, fmt.Errorf("can't get buyers by order ids: %w", err)
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

	// We need to use a struct that includes order_id since PromoCode doesn't have it
	type promoWithOrderId struct {
		OrderId int `db:"order_id"`
		entity.PromoCode
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
		var promoRow promoWithOrderId

		// Scan the row into the struct
		if err := rows.StructScan(&promoRow); err != nil {
			return nil, err
		}

		promos[promoRow.OrderId] = promoRow.PromoCode
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return promos, nil
}

func fetchOrderInfo(ctx context.Context, rep dependency.Repository, orders []entity.Order) ([]entity.OrderFull, error) {
	ids := getOrderIds(orders)

	var (
		orderItems     map[int][]entity.OrderItem
		refundedByItem map[int]map[int]int64 // orderId -> orderItemId -> quantityRefunded
		payments       map[string]entity.Payment
		shipments      map[int]entity.Shipment
		promos         map[int]entity.PromoCode
		buyers         map[int]entity.Buyer
		addresses      map[int]addressFull
	)

	// Use errgroup to handle concurrency and errors more elegantly
	g, ctx := errgroup.WithContext(ctx)

	// Fetch order items
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		orderItems, err = getOrdersItems(ctx, rep, ids...)
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

	// Fetch refunded quantities (parallel with other fetches)
	g.Go(func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		refundedByItem, err = getRefundedQuantitiesByOrderIds(ctx, rep, ids)
		if err != nil {
			return fmt.Errorf("get refunded quantities: %w", err)
		}
		return nil
	})

	// Wait for all goroutines to complete
	if err := g.Wait(); err != nil {
		return nil, err
	}

	refundedOrderItems := mergeRefundedOrderItems(orderItems, refundedByItem)

	ofs := make([]entity.OrderFull, 0, len(orders))

	// Assemble OrderFull objects
	for _, order := range orders {
		// Handle missing data gracefully with default values
		if _, ok := promos[order.Id]; !ok {
			promos[order.Id] = entity.PromoCode{}
		}
		orderItemsList := orderItems[order.Id]
		refundedItems := refundedOrderItems[order.Id]
		if refundedItems == nil {
			refundedItems = []entity.OrderItem{}
		}
		payment := payments[order.UUID]
		shipment := shipments[order.Id]
		buyer := buyers[order.Id]
		addrs := addresses[order.Id]

		ofs = append(ofs, entity.OrderFull{
			Order:              order,
			OrderItems:         orderItemsList,
			RefundedOrderItems: refundedItems,
			Payment:            payment,
			Shipment:           shipment,
			Buyer:              buyer,
			PromoCode:          promos[order.Id],
			Billing:            addrs.billing,
			Shipping:           addrs.shipping,
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

// GetOrderByPaymentIntentId retrieves an order by its PaymentIntent ID (client_secret) for idempotency
func (ms *MYSQLStore) GetOrderByPaymentIntentId(ctx context.Context, paymentIntentId string) (*entity.OrderFull, error) {
	query := `
    SELECT co.*
    FROM customer_order co
    JOIN payment p ON p.order_id = co.id
    WHERE p.client_secret = :paymentIntentId;`

	order, err := QueryNamedOne[entity.Order](ctx, ms.DB(), query, map[string]interface{}{
		"paymentIntentId": paymentIntentId,
	})

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // Return nil if not found (not an error for idempotency check)
		}
		return nil, fmt.Errorf("failed to get order by payment intent ID: %w", err)
	}

	// Fetch full order details
	ofs, err := fetchOrderInfo(ctx, ms, []entity.Order{order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
	}

	if len(ofs) == 0 {
		return nil, fmt.Errorf("order not found")
	}

	return &ofs[0], nil
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

func (ms *MYSQLStore) GetOrderByUUIDAndEmail(ctx context.Context, orderUUID string, email string) (*entity.OrderFull, error) {
	query := `
		SELECT co.*
		FROM customer_order co
		INNER JOIN buyer b ON co.id = b.order_id
		WHERE co.uuid = :orderUUID AND b.email = :email
	`

	order, err := QueryNamedOne[entity.Order](ctx, ms.DB(), query, map[string]interface{}{
		"orderUUID": orderUUID,
		"email":     email,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order by uuid and email: %w", err)
	}

	ofs, err := fetchOrderInfo(ctx, ms, []entity.Order{order})
	if err != nil {
		return nil, fmt.Errorf("can't fetch order info: %w", err)
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

		// Check if the order status is "awaiting payment"
		orderStatus, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}

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

		err = cancelOrder(ctx, rep, order, orderItems, entity.StockChangeSourceOrderExpired)
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

		// Check if order is in AwaitingPayment status
		os, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
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

// refundItem holds order_item_id and OrderItemInsert for stock restoration and refunded_order_item inserts.
type refundItem struct {
	OrderItemId     int
	OrderItemInsert entity.OrderItemInsert
}

// validateAndMapOrderItems maps order item IDs to refundItem, skipping IDs that don't
// belong to the order. Each occurrence of an ID = 1 unit to refund (e.g. [1,1,1] = 3 units).
// Returns nil for full refund (empty IDs).
func validateAndMapOrderItems(orderItems []entity.OrderItem, orderItemIDs []int32) ([]refundItem, error) {
	if len(orderItemIDs) == 0 {
		return nil, nil // Signal full refund
	}

	itemByID := make(map[int]entity.OrderItem, len(orderItems))
	for _, item := range orderItems {
		itemByID[item.Id] = item
	}

	itemsToRefund := make([]refundItem, 0, len(orderItemIDs))

	for _, id := range orderItemIDs {
		item, ok := itemByID[int(id)]
		if !ok {
			continue // Skip: item does not belong to order
		}
		// Each occurrence = 1 unit to refund; use Quantity=1 so RestoreStock sums correctly
		insert := item.OrderItemInsert
		insert.Quantity = decimal.NewFromInt(1)
		itemsToRefund = append(itemsToRefund, refundItem{OrderItemId: item.Id, OrderItemInsert: insert})
	}

	if len(itemsToRefund) == 0 {
		return nil, fmt.Errorf("no valid order items to refund")
	}

	return itemsToRefund, nil
}

// refundCoversFullOrder returns true when the requested orderItemIDs cover all order items
// with at least the full quantity each (e.g. orderItems [1,2,2], orderItemIDs [1,2,2] = full refund).
func refundCoversFullOrder(orderItems []entity.OrderItem, orderItemIDs []int32) bool {
	// Count requested units per order_item id
	requested := make(map[int]int64)
	for _, id := range orderItemIDs {
		requested[int(id)]++
	}

	// Each order item must have requested >= its quantity
	for _, item := range orderItems {
		req := requested[item.Id]
		qty := item.Quantity.IntPart()
		if req < qty {
			return false
		}
	}
	return true
}

// determineRefundScope determines which items to refund and the target status based on
// the current order status and requested item IDs.
func determineRefundScope(currentStatus entity.OrderStatusName, orderItems []entity.OrderItem, orderItemIDs []int32) ([]refundItem, *cache.Status, error) {
	// Full refund for RefundInProgress regardless of orderItemIDs
	if currentStatus == entity.RefundInProgress {
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	// PendingReturn: check for full vs partial
	partialItems, err := validateAndMapOrderItems(orderItems, orderItemIDs)
	if err != nil {
		return nil, nil, err
	}

	if partialItems == nil {
		// Empty IDs = full refund
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	// orderItemIDs covers all items with full quantities = full refund (e.g. [1,2,2] for items 1,2 with qty 1,2)
	if refundCoversFullOrder(orderItems, orderItemIDs) {
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	// Partial refund
	return partialItems, &cache.OrderStatusPartiallyRefunded, nil
}

func orderItemsToRefundItems(orderItems []entity.OrderItem) []refundItem {
	out := make([]refundItem, len(orderItems))
	for i, item := range orderItems {
		out[i] = refundItem{OrderItemId: item.Id, OrderItemInsert: item.OrderItemInsert}
	}
	return out
}

// RefundOrder processes a full or partial refund for an order.
// for orders in RefundInProgress status, always performs full refund.
// for orders in PendingReturn status, performs full or partial refund based on orderItemIDs.
func (ms *MYSQLStore) RefundOrder(ctx context.Context, orderUUID string, orderItemIDs []int32) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUID(ctx, rep, orderUUID)
		if err != nil {
			return err
		}

		orderStatus, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}

		if orderStatus.Status.Name != entity.RefundInProgress && orderStatus.Status.Name != entity.PendingReturn {
			return fmt.Errorf("order status must be refund_in_progress or pending_return, got %s", orderStatus.Status.Name)
		}

		itemsMap, err := getOrdersItems(ctx, rep, order.Id)
		if err != nil {
			return fmt.Errorf("get order items: %w", err)
		}

		orderItems := itemsMap[order.Id]
		if len(orderItems) == 0 {
			return fmt.Errorf("order has no items")
		}

		itemsToRefund, targetStatus, err := determineRefundScope(
			orderStatus.Status.Name,
			orderItems,
			orderItemIDs,
		)
		if err != nil {
			return err
		}

		itemsForStock := make([]entity.OrderItemInsert, len(itemsToRefund))
		for i := range itemsToRefund {
			itemsForStock[i] = itemsToRefund[i].OrderItemInsert
		}
		history := &entity.StockHistoryParams{
			Source:    entity.StockChangeSourceOrderRefunded,
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
		if err := insertRefundedOrderItems(ctx, rep, order.Id, refundedByItem); err != nil {
			return fmt.Errorf("insert refunded order items: %w", err)
		}

		refundedAmount := refundAmountFromItems(itemsForStock)
		return updateOrderStatusAndRefundedAmount(ctx, rep, order.Id, targetStatus.Status.Id, refundedAmount)
	})
}

// TODO:
func (ms *MYSQLStore) DeliveredOrder(ctx context.Context, orderUUID string) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		order, err := getOrderByUUID(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		// Validate order status is Confirmed or Shipped
		_, err = validateOrderStatus(order, entity.Confirmed, entity.Shipped)
		if err != nil {
			return fmt.Errorf("order status can be only Confirmed or Shipped: %w", err)
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

func cancelOrder(ctx context.Context, rep dependency.Repository, order *entity.Order, orderItems []entity.OrderItemInsert, source entity.StockChangeSource) error {
	// Validate order status - if already cancelled, nothing to do
	orderStatus, err := getOrderStatus(order.OrderStatusId)
	if err != nil {
		return err
	}

	st := orderStatus.Status.Name
	if st == entity.Cancelled {
		return nil
	}

	// Check if order can be cancelled (not in non-cancellable states)
	_, err = validateOrderStatusNot(order, entity.Refunded, entity.PartiallyRefunded, entity.Delivered, entity.Shipped, entity.Confirmed)
	if err != nil {
		return fmt.Errorf("order cannot be cancelled: %w", err)
	}

	if st == entity.AwaitingPayment {
		history := &entity.StockHistoryParams{
			Source:    source,
			OrderId:   order.Id,
			OrderUUID: order.UUID,
		}
		err := rep.Products().RestoreStockForProductSizes(ctx, orderItems, history)
		if err != nil {
			return fmt.Errorf("can't restore stock for product sizes: %w", err)
		}
	}

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

	err = updateOrderStatus(ctx, rep, order.Id, statusCancelled.Status.Id)
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
		err = cancelOrder(ctx, rep, &orderFull.Order, entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems), entity.StockChangeSourceOrderCancelled)
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

// CancelOrderByUser allows a user to cancel or request a refund for their order
func (ms *MYSQLStore) CancelOrderByUser(ctx context.Context, orderUUID string, email string, reason string) (*entity.OrderFull, error) {
	// Get order by UUID and email to verify ownership
	orderFull, err := ms.GetOrderByUUIDAndEmail(ctx, orderUUID, email)
	if err != nil {
		return nil, fmt.Errorf("order not found: %w", err)
	}

	// Get current order status
	orderStatus, err := getOrderStatus(orderFull.Order.OrderStatusId)
	if err != nil {
		return nil, err
	}

	currentStatus := orderStatus.Status.Name

	// Check if order is already in a refund/cancellation state
	if currentStatus == entity.Cancelled ||
		currentStatus == entity.PendingReturn ||
		currentStatus == entity.RefundInProgress ||
		currentStatus == entity.Refunded ||
		currentStatus == entity.PartiallyRefunded {
		return nil, fmt.Errorf("order already in refund progress or refunded: current status %s", currentStatus)
	}

	// Determine new status based on current status
	var newStatus *cache.Status
	switch currentStatus {
	case entity.Placed, entity.AwaitingPayment:
		// Set to Cancelled
		newStatus = &cache.OrderStatusCancelled
	case entity.Confirmed:
		// Set to RefundInProgress
		newStatus = &cache.OrderStatusRefundInProgress
	case entity.Shipped, entity.Delivered:
		// Set to PendingReturn
		newStatus = &cache.OrderStatusPendingReturn
	default:
		return nil, fmt.Errorf("cannot cancel order with status: %s", currentStatus)
	}

	// Update order status and reason in transaction
	err = ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Update order status and refund reason
		query := `
			UPDATE customer_order
			SET order_status_id = :orderStatusId,
				refund_reason = :refundReason
			WHERE id = :orderId`

		err := ExecNamed(ctx, rep.DB(), query, map[string]any{
			"orderId":       orderFull.Order.Id,
			"orderStatusId": newStatus.Status.Id,
			"refundReason":  reason,
		})
		if err != nil {
			return fmt.Errorf("can't update order status and reason: %w", err)
		}

		// If order was in AwaitingPayment, restore stock
		if currentStatus == entity.AwaitingPayment {
			orderItems := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)
			history := &entity.StockHistoryParams{
				Source:    entity.StockChangeSourceOrderCancelled,
				OrderId:   orderFull.Order.Id,
				OrderUUID: orderFull.Order.UUID,
			}
			err := rep.Products().RestoreStockForProductSizes(ctx, orderItems, history)
			if err != nil {
				return fmt.Errorf("can't restore stock for product sizes: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Refresh order details after update
	orderFull, err = ms.GetOrderByUUIDAndEmail(ctx, orderUUID, email)
	if err != nil {
		return nil, fmt.Errorf("can't refresh order details: %w", err)
	}

	return orderFull, nil
}

// AddOrderComment adds a comment to an order
func (ms *MYSQLStore) AddOrderComment(ctx context.Context, orderUUID string, comment string) error {
	// Get order by UUID to verify it exists
	_, err := getOrderByUUID(ctx, ms, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get order by UUID: %w", err)
	}

	// Update order comment
	query := `
		UPDATE customer_order
		SET order_comment = :comment
		WHERE uuid = :uuid`

	err = ExecNamed(ctx, ms.db, query, map[string]any{
		"comment": comment,
		"uuid":    orderUUID,
	})
	if err != nil {
		return fmt.Errorf("can't update order comment: %w", err)
	}

	return nil
}
