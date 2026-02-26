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

// ErrOrderItemsUpdated is returned when order items were validated and updated
// (e.g. prices changed, items unavailable). Caller should NOT cancel the order.
var ErrOrderItemsUpdated = errors.New("order items are not valid and were updated")

// errPaymentRecordNotFound indicates the payment record for the order was not found (0 rows updated).
// Can occur due to replication lag or if order creation didn't commit before association.
var errPaymentRecordNotFound = errors.New("payment record not found for order")

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

func validateOrderItemsStockAvailability(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	return validateOrderItemsStockAvailabilityWithLock(ctx, rep, items, currency, false)
}

// validateOrderItemsStockAvailabilityForUpdate validates stock and locks product_size rows to prevent race conditions
func validateOrderItemsStockAvailabilityForUpdate(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	return validateOrderItemsStockAvailabilityWithLock(ctx, rep, items, currency, true)
}

func validateOrderItemsStockAvailabilityWithLock(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, currency string, forUpdate bool) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	// Check if there are no items provided
	if len(items) == 0 {
		return nil, nil, &entity.ValidationError{Message: "zero items to validate"}
	}

	// Get product IDs from items
	prdIds := getProductIdsFromItems(items)

	// We no longer need default language as we return all translations

	// Get product details by IDs
	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	// Create a map for product details with ProductId as the key
	prdMap := make(map[int]entity.Product)
	for _, prd := range prds {
		prdMap[prd.Id] = prd
	}

	// Get product sizes (stock) details by item details
	var prdSizes []entity.ProductSize
	if forUpdate {
		prdSizes, err = getProductsSizesByIdsForUpdate(ctx, rep, items)
	} else {
		prdSizes, err = getProductsSizesByIds(ctx, rep, items)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products sizes by ids: %w", err)
	}

	// Create a map for product sizes with a combination of ProductId and SizeId as the key
	prdSizeMap := make(map[string]entity.ProductSize)
	for _, prdSize := range prdSizes {
		key := fmt.Sprintf("%d-%d", prdSize.ProductId, prdSize.SizeId)
		prdSizeMap[key] = prdSize
	}

	validItems := make([]entity.OrderItem, 0, len(items))
	adjustments := make([]entity.OrderItemAdjustment, 0)

	for _, item := range items {
		sizeKey := fmt.Sprintf("%d-%d", item.ProductId, item.SizeId)
		prdSize, exists := prdSizeMap[sizeKey]

		// Out of stock or size doesn't exist: record adjustment and skip
		if !exists || !prdSize.QuantityDecimal().GreaterThan(decimal.Zero) {
			adjustments = append(adjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				RequestedQuantity: item.QuantityDecimal(),
				AdjustedQuantity:  decimal.Zero,
				Reason:            entity.AdjustmentReasonOutOfStock,
			})
			continue
		}

		// Reduce quantity if requested exceeds available: record adjustment
		requestedQty := item.QuantityDecimal()
		if requestedQty.GreaterThan(prdSize.QuantityDecimal()) {
			item.Quantity = prdSize.QuantityDecimal()
			adjustments = append(adjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				RequestedQuantity: requestedQty,
				AdjustedQuantity:  prdSize.QuantityDecimal(),
				Reason:            entity.AdjustmentReasonQuantityReduced,
			})
		}

		// Look up the product in the prdMap
		prd, exists := prdMap[item.ProductId]
		if !exists {
			adjustments = append(adjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				RequestedQuantity: item.QuantityDecimal(),
				AdjustedQuantity:  decimal.Zero,
				Reason:            entity.AdjustmentReasonOutOfStock,
			})
			continue
		}

		// Set price and sale percentage from product details
		productBody := &prd.ProductDisplay.ProductBody
		productPrice, err := getProductPrice(&prd, currency)
		if err != nil {
			return nil, nil, &entity.ValidationError{Message: fmt.Sprintf("product %d does not have a price in currency %s", prd.Id, currency)}
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

	return validItems, adjustments, nil
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

func calculateTotalAmount(ctx context.Context, rep dependency.Repository, items []entity.ProductInfoProvider, currency string) (decimal.Decimal, error) {
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

	return dto.RoundForCurrency(totalAmount, currency), nil
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

func insertShipment(ctx context.Context, rep dependency.Repository, sc *entity.ShipmentCarrier, orderId int, cost decimal.Decimal, freeShipping bool) error {
	query := `
	INSERT INTO shipment (carrier_id, order_id, cost, free_shipping)
	VALUES (:carrierId, :orderId, :cost, :freeShipping)
	`
	err := ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"carrierId":    sc.Id,
		"orderId":      orderId,
		"cost":         cost,
		"freeShipping": freeShipping,
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
	return validateAndUpdateOrderIfNeededWithLock(ctx, rep, orderFull, cancelOnValidationFailure, false)
}

// validateAndUpdateOrderIfNeededForUpdate is like validateAndUpdateOrderIfNeeded but locks product_size rows
func validateAndUpdateOrderIfNeededForUpdate(
	ctx context.Context,
	rep dependency.Repository,
	orderFull *entity.OrderFull,
	cancelOnValidationFailure bool,
) (bool, error) {
	return validateAndUpdateOrderIfNeededWithLock(ctx, rep, orderFull, cancelOnValidationFailure, true)
}

func validateAndUpdateOrderIfNeededWithLock(
	ctx context.Context,
	rep dependency.Repository,
	orderFull *entity.OrderFull,
	cancelOnValidationFailure bool,
	lockStock bool,
) (bool, error) {
	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)

	// Validate the order items
	var oiv *entity.OrderItemValidation
	var err error

	if lockStock {
		// Use the internal method that locks stock rows
		if os, ok := rep.Order().(*orderStore); ok {
			oiv, err = os.validateOrderItemsInsertForUpdate(ctx, items, orderFull.Order.Currency)
		} else {
			return false, fmt.Errorf("cannot cast to orderStore for stock locking")
		}
	} else {
		oiv, err = rep.Order().ValidateOrderItemsInsert(ctx, items, orderFull.Order.Currency)
	}

	if err != nil {
		// If validation fails and we should cancel, do it
		if cancelOnValidationFailure {
			if cancelErr := cancelOrder(ctx, rep, &orderFull.Order, items, entity.StockChangeSourceOrderCancelled, ""); cancelErr != nil {
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
		if _, err := updateTotalAmount(ctx, rep, orderFull.Order.Id, oiv.SubtotalDecimal(), orderFull.PromoCode, orderFull.Shipment, orderFull.Order.Currency); err != nil {
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

// validateShipmentCarrier validates a shipment carrier by ID, checks if it's allowed, and if it serves the shipping region.
// shippingCountry is the ISO 3166-1 alpha-2 code from the shipping address; if empty, geo check is skipped.
// Returns the shipment carrier or an error if validation fails.
func validateShipmentCarrier(carrierId int, shippingCountry string) (*entity.ShipmentCarrier, error) {
	carrier, ok := cache.GetShipmentCarrierById(carrierId)
	if !ok {
		return nil, fmt.Errorf("shipment carrier does not exist: carrier id %d", carrierId)
	}
	if !carrier.Allowed {
		return nil, fmt.Errorf("shipment carrier is not allowed: carrier id %d", carrierId)
	}
	// Geo restriction: if carrier has allowed regions and we have a country, verify the region
	if shippingCountry != "" && len(carrier.AllowedRegions) > 0 {
		region, ok := entity.CountryToRegion(shippingCountry)
		if !ok {
			return nil, fmt.Errorf("shipping country %s could not be mapped to a region", shippingCountry)
		}
		if !carrier.AvailableForRegion(region) {
			return nil, fmt.Errorf("shipment carrier does not serve region %s", region)
		}
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

// adjustQuantities adjusts the quantity of the items if it exceeds the maxOrderItemPerSize.
// Returns the adjusted items and any adjustments made (quantity_capped).
func adjustQuantities(maxOrderItemPerSize int, items []entity.OrderItemInsert) ([]entity.OrderItemInsert, []entity.OrderItemAdjustment) {
	maxQuantity := decimal.NewFromInt(int64(maxOrderItemPerSize))
	adjustments := make([]entity.OrderItemAdjustment, 0)
	for i, item := range items {
		if item.QuantityDecimal().Cmp(maxQuantity) > 0 {
			requestedQty := items[i].QuantityDecimal()
			items[i].Quantity = maxQuantity.Round(0)
			adjustments = append(adjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				RequestedQuantity: requestedQty,
				AdjustedQuantity:  maxQuantity.Round(0),
				Reason:            entity.AdjustmentReasonQuantityCapped,
			})
		}
	}
	return items, adjustments
}

func (ms *MYSQLStore) validateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	// adjust quantities if it exceeds the maxOrderItemPerSize
	items, capAdjustments := adjustQuantities(cache.GetMaxOrderItems(), items)

	slog.Default().InfoContext(ctx, "items", slog.Any("items", items))

	// validate items stock availability
	validItems, stockAdjustments, err := validateOrderItemsStockAvailability(ctx, ms, items, currency)
	if err != nil {
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("error while validating order items: %w", err)
	}
	if len(validItems) == 0 {
		return nil, nil, &entity.ValidationError{Message: "no valid order items: products or sizes not found, or out of stock"}
	}

	// Combine adjustments (cap first, then stock)
	allAdjustments := make([]entity.OrderItemAdjustment, 0, len(capAdjustments)+len(stockAdjustments))
	allAdjustments = append(allAdjustments, capAdjustments...)
	allAdjustments = append(allAdjustments, stockAdjustments...)

	return validItems, allAdjustments, nil
}

// ValidateOrderItemsInsert validates the order items and returns the valid items and the total amount
func (ms *MYSQLStore) ValidateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert, currency string) (*entity.OrderItemValidation, error) {
	return ms.validateOrderItemsInsertWithLock(ctx, items, currency, false)
}

// ValidateOrderItemsInsertWithReservation validates order items with stock reservation awareness
// This method is a placeholder that calls the standard validation - reservation logic is handled at the service layer
func (ms *MYSQLStore) ValidateOrderItemsInsertWithReservation(ctx context.Context, items []entity.OrderItemInsert, currency string, sessionID string) (*entity.OrderItemValidation, error) {
	// The actual reservation logic is handled in the frontend server layer
	// This method exists to satisfy the interface and maintain consistency
	return ms.validateOrderItemsInsertWithLock(ctx, items, currency, false)
}

// validateOrderItemsInsertForUpdate validates order items and locks product_size rows to prevent race conditions
func (ms *MYSQLStore) validateOrderItemsInsertForUpdate(ctx context.Context, items []entity.OrderItemInsert, currency string) (*entity.OrderItemValidation, error) {
	return ms.validateOrderItemsInsertWithLock(ctx, items, currency, true)
}

func (ms *MYSQLStore) validateOrderItemsInsertWithLock(ctx context.Context, items []entity.OrderItemInsert, currency string, lockStock bool) (*entity.OrderItemValidation, error) {
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
	var validItems []entity.OrderItem
	var itemAdjustments []entity.OrderItemAdjustment
	var err error

	if lockStock {
		validItems, itemAdjustments, err = validateOrderItemsStockAvailabilityForUpdate(ctx, ms, mergedItems, currency)
	} else {
		validItems, itemAdjustments, err = ms.validateOrderItemsInsert(ctx, mergedItems, currency)
	}

	if err != nil {
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
	total, err := calculateTotalAmount(ctx, ms, providers, currency)
	if err != nil {
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
		ValidItems:      validItems,
		Subtotal:        dto.RoundForCurrency(total, currency),
		HasChanged:      !compareItems(copiedItems, validItemsInsert, true),
		ItemAdjustments: itemAdjustments,
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
		return nil, ErrOrderItemsUpdated
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

	// Validate shipment carrier (including geo restrictions)
	shippingCountry := ""
	if orderNew.ShippingAddress != nil {
		shippingCountry = orderNew.ShippingAddress.Country
	}
	shipmentCarrier, err := validateShipmentCarrier(orderNew.ShipmentCarrierId, shippingCountry)
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
		validItems, _, err := ms.validateOrderItemsInsert(ctx, orderNew.Items, orderNew.Currency)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		// Calculate total from validated items
		providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
		subtotal, err := calculateTotalAmount(ctx, rep, providers, orderNew.Currency)
		if err != nil {
			return fmt.Errorf("error while calculating total amount: %w", err)
		}

		shipmentPrice, err := shipmentCarrier.PriceDecimal(orderNew.Currency)
		if err != nil {
			return fmt.Errorf("can't get shipment carrier price for currency %s: %w", orderNew.Currency, err)
		}

		// Complimentary shipping: waive shipping when subtotal meets threshold (threshold=0 means disabled for that currency)
		freeShipping := false
		complimentaryPrices := cache.GetComplimentaryShippingPrices()
		if threshold, ok := complimentaryPrices[strings.ToUpper(orderNew.Currency)]; ok && threshold.GreaterThan(decimal.Zero) {
			if subtotal.GreaterThanOrEqual(threshold) {
				shipmentPrice = decimal.Zero
				freeShipping = true
			}
		}
		if promo.FreeShipping {
			shipmentPrice = decimal.Zero
			freeShipping = true
		}

		totalPrice := promo.SubtotalWithPromo(subtotal, shipmentPrice, dto.DecimalPlacesForCurrency(orderNew.Currency))

		order = &entity.Order{
			TotalPrice:    totalPrice,
			Currency:      orderNew.Currency,
			PromoId:       prId,
			OrderStatusId: cache.OrderStatusPlaced.Status.Id,
		}

		// Insert order and related entities
		err = ms.insertOrderDetails(ctx, rep, order, validItemsInsert, shipmentCarrier, shipmentPrice, freeShipping, orderNew)
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
func (ms *MYSQLStore) insertOrderDetails(ctx context.Context, rep dependency.Repository, order *entity.Order, validItemsInsert []entity.OrderItemInsert, carrier *entity.ShipmentCarrier, shipmentCost decimal.Decimal, freeShipping bool, orderNew *entity.OrderNew) error {
	var err error
	order.Id, order.UUID, err = insertOrder(ctx, rep, order)
	if err != nil {
		return fmt.Errorf("error while inserting final order: %w", err)
	}

	slog.Info("inserting order items", "order_id", order.Id, "items", validItemsInsert)
	if err = insertOrderItems(ctx, rep, validItemsInsert, order.Id); err != nil {
		return fmt.Errorf("error while inserting order items: %w", err)
	}
	if err = insertShipment(ctx, rep, carrier, order.Id, shipmentCost, freeShipping); err != nil {
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

// getOrderByUUIDForUpdate locks the order row for update to prevent race conditions
func getOrderByUUIDForUpdate(ctx context.Context, rep dependency.Repository, uuid string) (*entity.Order, error) {
	query := `
	SELECT * from customer_order WHERE uuid = :uuid FOR UPDATE`
	order, err := QueryNamedOne[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"uuid": uuid,
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// getOrderByUUIDAndEmailForUpdate locks the order row for update and verifies email ownership
func getOrderByUUIDAndEmailForUpdate(ctx context.Context, rep dependency.Repository, orderUUID string, email string) (*entity.Order, error) {
	query := `
		SELECT co.*
		FROM customer_order co
		INNER JOIN buyer b ON co.id = b.order_id
		WHERE co.uuid = :orderUUID AND b.email = :email
		FOR UPDATE
	`
	order, err := QueryNamedOne[entity.Order](ctx, rep.DB(), query, map[string]interface{}{
		"orderUUID": orderUUID,
		"email":     email,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order by uuid and email: %w", err)
	}
	return &order, nil
}

func (ms *MYSQLStore) GetOrderByUUID(ctx context.Context, uuid string) (*entity.Order, error) {
	return getOrderByUUID(ctx, ms, uuid)
}

// ValidStatusTransitions defines allowed status transitions
// Key: current status, Value: slice of allowed next statuses
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
	// Terminal states - no transitions allowed
	entity.Cancelled:         {},
	entity.Refunded:          {},
	entity.PartiallyRefunded: {},
}

// isValidStatusTransition checks if transition from currentStatus to newStatus is allowed
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

// updateOrderStatusWithValidation updates order status with transition validation.
// Returns error if the status transition is not allowed.
func updateOrderStatusWithValidation(
	ctx context.Context,
	rep dependency.Repository,
	orderId int,
	newStatusId int,
	changedBy string,
	notes string,
) error {
	// Get current order status
	var currentStatusId int
	query := `SELECT order_status_id FROM customer_order WHERE id = ?`
	err := rep.DB().GetContext(ctx, &currentStatusId, query, orderId)
	if err != nil {
		return fmt.Errorf("get current status: %w", err)
	}

	// Get status names for validation
	currentStatus, err := getOrderStatus(currentStatusId)
	if err != nil {
		return fmt.Errorf("get current status name: %w", err)
	}

	newStatus, err := getOrderStatus(newStatusId)
	if err != nil {
		return fmt.Errorf("get new status name: %w", err)
	}

	// Validate transition
	if !isValidStatusTransition(currentStatus.Status.Name, newStatus.Status.Name) {
		return fmt.Errorf(
			"invalid status transition: cannot change from %s to %s",
			currentStatus.Status.Name,
			newStatus.Status.Name,
		)
	}

	// Update status
	updateQuery := `
		UPDATE customer_order 
		SET order_status_id = :newStatusId,
			modified = CURRENT_TIMESTAMP
		WHERE id = :orderId
	`

	err = ExecNamed(ctx, rep.DB(), updateQuery, map[string]any{
		"orderId":     orderId,
		"newStatusId": newStatusId,
	})
	if err != nil {
		return fmt.Errorf("update order status: %w", err)
	}

	return insertOrderStatusHistoryEntry(ctx, rep, orderId, newStatusId, changedBy, notes)
}

// updateOrderStatus is a wrapper for backward compatibility
func updateOrderStatus(ctx context.Context, rep dependency.Repository, orderId int, orderStatusId int) error {
	return updateOrderStatusWithValidation(ctx, rep, orderId, orderStatusId, "system", "")
}

// insertOrderStatusHistoryEntry inserts a single entry into order_status_history.
func insertOrderStatusHistoryEntry(ctx context.Context, rep dependency.Repository, orderId int, statusId int, changedBy string, notes string) error {
	query := `
		INSERT INTO order_status_history (order_id, order_status_id, changed_by, notes)
		VALUES (:orderId, :statusId, :changedBy, :notes)`
	return ExecNamed(ctx, rep.DB(), query, map[string]any{
		"orderId":   orderId,
		"statusId":  statusId,
		"changedBy": changedBy,
		"notes":     notes,
	})
}

// getOrderStatusHistory retrieves the complete status history for an order
func getOrderStatusHistory(ctx context.Context, rep dependency.Repository, orderId int) ([]entity.OrderStatusHistoryWithStatus, error) {
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
	err := rep.DB().SelectContext(ctx, &history, query, orderId)
	return history, err
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

// updateOrderStatusAndRefundedAmountWithValidation updates order status and refunded amount with validation.
// refundReason is optional; when non-empty it updates customer_order.refund_reason.
func updateOrderStatusAndRefundedAmountWithValidation(
	ctx context.Context,
	rep dependency.Repository,
	orderId int,
	orderStatusId int,
	refundedAmount decimal.Decimal,
	refundReason string,
	changedBy string,
) error {
	// Get current order status for validation
	var currentStatusId int
	query := `SELECT order_status_id FROM customer_order WHERE id = ?`
	err := rep.DB().GetContext(ctx, &currentStatusId, query, orderId)
	if err != nil {
		return fmt.Errorf("get current status: %w", err)
	}

	// Get status names for validation
	currentStatus, err := getOrderStatus(currentStatusId)
	if err != nil {
		return fmt.Errorf("get current status name: %w", err)
	}

	newStatus, err := getOrderStatus(orderStatusId)
	if err != nil {
		return fmt.Errorf("get new status name: %w", err)
	}

	// Validate transition
	if !isValidStatusTransition(currentStatus.Status.Name, newStatus.Status.Name) {
		return fmt.Errorf(
			"invalid status transition: cannot change from %s to %s",
			currentStatus.Status.Name,
			newStatus.Status.Name,
		)
	}

	// Update status, refunded amount, and optionally refund reason
	updateQuery := `
		UPDATE customer_order 
		SET order_status_id = :orderStatusId,
			refunded_amount = :refundedAmount,
			refund_reason = COALESCE(NULLIF(:refundReason, ''), refund_reason),
			modified = CURRENT_TIMESTAMP
		WHERE id = :orderId
	`

	err = ExecNamed(ctx, rep.DB(), updateQuery, map[string]any{
		"orderId":        orderId,
		"orderStatusId":  orderStatusId,
		"refundedAmount": refundedAmount.Round(2),
		"refundReason":   refundReason,
	})
	if err != nil {
		return fmt.Errorf("update order: %w", err)
	}

	notes := fmt.Sprintf("Refunded amount: %s", refundedAmount.String())
	return insertOrderStatusHistoryEntry(ctx, rep, orderId, orderStatusId, changedBy, notes)
}

func updateOrderStatusAndRefundedAmount(ctx context.Context, rep dependency.Repository, orderId int, orderStatusId int, refundedAmount decimal.Decimal, refundReason string) error {
	return updateOrderStatusAndRefundedAmountWithValidation(ctx, rep, orderId, orderStatusId, refundedAmount, refundReason, "admin")
}

func refundAmountFromItems(items []entity.OrderItemInsert, currency string) decimal.Decimal {
	var sum decimal.Decimal
	for _, item := range items {
		sum = sum.Add(item.ProductPriceWithSale.Mul(item.Quantity))
	}
	return dto.RoundForCurrency(sum, currency)
}

func updateOrderPayment(ctx context.Context, rep dependency.Repository, orderId int, payment entity.PaymentInsert) error {
	query := `
	UPDATE payment 
	SET transaction_amount = :transactionAmount,
		transaction_amount_payment_currency = :transactionAmountPaymentCurrency,
		transaction_id = :transactionId,
		is_transaction_done = :isTransactionDone,
		payment_method_id = :paymentMethodId,
		client_secret = :clientSecret,
		expired_at = :expiredAt,
		payment_method_type = :paymentMethodType
	WHERE order_id = :orderId`

	params := map[string]any{
		"transactionAmount":                payment.TransactionAmount,
		"transactionAmountPaymentCurrency": payment.TransactionAmountPaymentCurrency,
		"transactionId":                    payment.TransactionID,
		"isTransactionDone":                payment.IsTransactionDone,
		"paymentMethodId":                  payment.PaymentMethodID,
		"clientSecret":                     payment.ClientSecret,
		"orderId":                          orderId,
		"expiredAt":                        payment.ExpiredAt,
		"paymentMethodType":                payment.PaymentMethodType,
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

// AssociatePaymentIntentWithOrder stores the PaymentIntent ID in the payment record before InsertFiatInvoice.
// This ensures GetOrderByPaymentIntentId finds the order on retry when InsertFiatInvoice returns ErrOrderItemsUpdated,
// preventing duplicate order creation.
func (ms *MYSQLStore) AssociatePaymentIntentWithOrder(ctx context.Context, orderUUID string, paymentIntentId string) error {
	query := `
	UPDATE payment 
	SET client_secret = :paymentIntentId 
	WHERE order_id = (
		SELECT id FROM customer_order 
		WHERE uuid = :orderUUID
	)`

	rows, err := ms.db.NamedExecContext(ctx, query, map[string]any{
		"paymentIntentId": paymentIntentId,
		"orderUUID":       orderUUID,
	})
	if err != nil {
		return fmt.Errorf("can't associate payment intent with order: %w", err)
	}

	if rowsAffected, err := rows.RowsAffected(); err != nil || rowsAffected == 0 {
		return fmt.Errorf("can't associate payment intent with order: %w", errPaymentRecordNotFound)
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
func updateTotalAmount(ctx context.Context, rep dependency.Repository, orderId int, subtotal decimal.Decimal, promo entity.PromoCode, shipment entity.Shipment, currency string) (decimal.Decimal, error) {
	if !promo.IsAllowed() {
		promo = entity.PromoCode{}
	}

	// Re-evaluate complimentary shipping: get carrier's actual price and check threshold
	shipmentCost := shipment.CostDecimal(currency)
	freeShipping := false
	carrier, carrierOk := cache.GetShipmentCarrierById(shipment.CarrierId)
	if carrierOk {
		carrierPrice, err := carrier.PriceDecimal(currency)
		if err == nil {
			shipmentCost = carrierPrice
			complimentaryPrices := cache.GetComplimentaryShippingPrices()
			if threshold, ok := complimentaryPrices[strings.ToUpper(currency)]; ok && threshold.GreaterThan(decimal.Zero) {
				if subtotal.GreaterThanOrEqual(threshold) {
					shipmentCost = decimal.Zero
					freeShipping = true
				}
			}
		}
	}
	if promo.FreeShipping {
		shipmentCost = decimal.Zero
		freeShipping = true
	}

	// Update stored shipment cost and free_shipping flag
	if err := updateShipmentCostAndFreeShipping(ctx, rep, shipment.Id, shipmentCost, freeShipping); err != nil {
		return decimal.Zero, fmt.Errorf("can't update shipment cost: %w", err)
	}

	subtotal = promo.SubtotalWithPromo(subtotal, shipmentCost, dto.DecimalPlacesForCurrency(currency))

	err := updateOrderTotalPromo(ctx, rep, orderId, promo.Id, subtotal)
	if err != nil {
		return decimal.Zero, fmt.Errorf("can't update order total promo: %w", err)
	}

	return subtotal, nil
}

func updateShipmentCostAndFreeShipping(ctx context.Context, rep dependency.Repository, shipmentId int, cost decimal.Decimal, freeShipping bool) error {
	query := `UPDATE shipment SET cost = :cost, free_shipping = :freeShipping WHERE id = :id`
	return ExecNamed(ctx, rep.DB(), query, map[string]any{
		"id":           shipmentId,
		"cost":         cost,
		"freeShipping": freeShipping,
	})
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
		"totalPrice": totalPrice,
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

	// Convert order items to insert format and validate them
	var itemsChanged bool
	var orderFull *entity.OrderFull

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Lock the order row to prevent race conditions with concurrent invoice insertions or cancellations
		order, err := getOrderByUUIDForUpdate(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("cannot get order by UUID %s: %w", orderUUID, err)
		}

		// Validate order status is Placed (do not process payment for Cancelled orders)
		_, err = validateOrderStatus(order, entity.Placed)
		if err != nil {
			return err
		}

		// Get full order details (items, buyer, etc.) inside transaction
		orderItems, err := getOrderItemsInsert(ctx, rep, order.Id)
		if err != nil {
			return fmt.Errorf("cannot get order items: %w", err)
		}

		// Fetch full order info
		ofs, err := fetchOrderInfo(ctx, rep, []entity.Order{*order})
		if err != nil {
			return fmt.Errorf("cannot fetch order info: %w", err)
		}
		if len(ofs) == 0 {
			return fmt.Errorf("order is not found")
		}
		orderFull = &ofs[0]

		// Check if the order's total price is zero or below currency minimum
		if orderFull.Order.TotalPrice.IsZero() {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice: order total is zero, cancelling",
				slog.String("order_uuid", orderUUID),
				slog.Int("order_id", orderFull.Order.Id),
				slog.String("currency", orderFull.Order.Currency),
			)
			if err := cancelOrder(ctx, rep, &orderFull.Order, orderItems, entity.StockChangeSourceOrderCancelled, ""); err != nil {
				return fmt.Errorf("cannot cancel order: %w", err)
			}
			return fmt.Errorf("total price is zero")
		}
		if err := dto.ValidatePriceMeetsMinimum(orderFull.Order.TotalPrice, orderFull.Order.Currency); err != nil {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice: order total below currency minimum",
				slog.String("order_uuid", orderUUID),
				slog.String("total", orderFull.Order.TotalPrice.String()),
				slog.String("currency", orderFull.Order.Currency),
				slog.String("err", err.Error()),
			)
			return fmt.Errorf("order total below currency minimum: %w", err)
		}

		// Validate and update order with stock row locking to prevent race conditions
		itemsChanged, err = validateAndUpdateOrderIfNeededForUpdate(ctx, rep, orderFull, true)
		if err != nil {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice: order validation failed, cancelling",
				slog.String("order_uuid", orderUUID),
				slog.String("err", err.Error()),
			)
			return err
		}

		// If items changed, return early so we don't reduce stock or process payment
		if itemsChanged {
			return nil
		}

		// Reduce stock for valid items (validation already locked the rows, so this is atomic)
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
		return nil, ErrOrderItemsUpdated
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
	var order *entity.Order
	var shipment *entity.Shipment
	var buyer *entity.Buyer

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		order, err = getOrderByUUIDForUpdate(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		// Validate order status is Confirmed or Shipped
		_, err = validateOrderStatus(order, entity.Confirmed, entity.Shipped)
		if err != nil {
			return fmt.Errorf("bad order status for setting tracking number: %w", err)
		}

		shipment, err = getOrderShipment(ctx, rep, order.Id)
		if err != nil {
			return fmt.Errorf("can't get order shipment: %w", err)
		}

		shipment.TrackingCode = sql.NullString{
			String: trackingCode,
			Valid:  true,
		}

		buyer, err = getBuyerById(ctx, rep, order.Id)
		if err != nil {
			return fmt.Errorf("can't get buyer by id: %w", err)
		}

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

	// Read committed order with updated status for return
	order, err = getOrderByUUID(ctx, ms, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order after update: %w", err)
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

	// MySQL connections only support one query at a time. When inside a transaction
	// (single connection), concurrent goroutines corrupt the protocol and cause
	// "driver: bad connection" errors. Run sequentially in that case.
	if rep.InTx() {
		var err error
		orderItems, err = getOrdersItems(ctx, rep, ids...)
		if err != nil {
			return nil, fmt.Errorf("can't get order items: %w", err)
		}
		payments, err = paymentsByOrderIds(ctx, rep, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get payment by id: %w", err)
		}
		shipments, err = shipmentsByOrderIds(ctx, rep, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get order shipment: %w", err)
		}
		promos, err = promosByOrderIds(ctx, rep, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get order promos: %w", err)
		}
		buyers, err = buyersByOrderIds(ctx, rep, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get buyers order by ids %w", err)
		}
		addresses, err = addressesByOrderIds(ctx, rep, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get addresses by id: %w", err)
		}
		refundedByItem, err = getRefundedQuantitiesByOrderIds(ctx, rep, ids)
		if err != nil {
			return nil, fmt.Errorf("get refunded quantities: %w", err)
		}
	} else {
		// Outside a transaction: use errgroup for parallel fetches across pooled connections
		g, ctx := errgroup.WithContext(ctx)

		g.Go(func() error {
			var err error
			orderItems, err = getOrdersItems(ctx, rep, ids...)
			if err != nil {
				return fmt.Errorf("can't get order items: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			payments, err = paymentsByOrderIds(ctx, rep, ids)
			if err != nil {
				return fmt.Errorf("can't get payment by id: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			shipments, err = shipmentsByOrderIds(ctx, rep, ids)
			if err != nil {
				return fmt.Errorf("can't get order shipment: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			promos, err = promosByOrderIds(ctx, rep, ids)
			if err != nil {
				return fmt.Errorf("can't get order promos: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			buyers, err = buyersByOrderIds(ctx, rep, ids)
			if err != nil {
				return fmt.Errorf("can't get buyers order by ids %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			addresses, err = addressesByOrderIds(ctx, rep, ids)
			if err != nil {
				return fmt.Errorf("can't get addresses by id: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			refundedByItem, err = getRefundedQuantitiesByOrderIds(ctx, rep, ids)
			if err != nil {
				return fmt.Errorf("get refunded quantities: %w", err)
			}
			return nil
		})

		if err := g.Wait(); err != nil {
			return nil, err
		}
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

	// Get status history
	statusHistory, err := getOrderStatusHistory(ctx, ms, order.Id)
	if err != nil {
		return nil, fmt.Errorf("get status history: %w", err)
	}
	ofs[0].StatusHistory = statusHistory

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

	// Get status history
	statusHistory, err := getOrderStatusHistory(ctx, ms, order.Id)
	if err != nil {
		return nil, fmt.Errorf("get status history: %w", err)
	}
	ofs[0].StatusHistory = statusHistory

	return &ofs[0], nil
}

func (ms *MYSQLStore) GetOrdersByStatusAndPaymentTypePaged(
	ctx context.Context,
	email string,
	orderUUID string,
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
			AND (:orderUUID = '' OR co.uuid = :orderUUID)
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
		"orderUUID":     orderUUID,
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
		// Lock the order row to prevent race conditions with concurrent payment confirmations
		order, err := getOrderByUUIDForUpdate(ctx, rep, orderUUID)
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
			IsTransactionDone:                false,
		}

		if payment.IsTransactionDone {
			return nil
		}

		// Update order payment
		if err := updateOrderPayment(ctx, rep, order.Id, paymentUpdate); err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		err = cancelOrder(ctx, rep, order, orderItems, entity.StockChangeSourceOrderExpired, "")
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

func (ms *MYSQLStore) OrderPaymentDone(ctx context.Context, orderUUID string, p *entity.Payment) (bool, error) {
	wasUpdated := false

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		// Lock the order row to prevent race conditions with concurrent payment confirmations
		order, err := getOrderByUUIDForUpdate(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		// Check if order is in AwaitingPayment status (idempotency check)
		os, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}

		if os.Status.Name != entity.AwaitingPayment {
			// Order already confirmed or in different state - return early (idempotent)
			// wasUpdated remains false to prevent duplicate emails
			return nil
		}

		if order.PromoId.Int32 != 0 {
			err := rep.Promo().DisableVoucher(ctx, order.PromoId)
			if err != nil {
				return fmt.Errorf("can't disable voucher: %w", err)
			}
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

		// Mark that we successfully updated the order
		wasUpdated = true
		return nil
	})
	if err != nil {
		return false, err
	}

	return wasUpdated, nil
}

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

	// Count requested refund units per order_item_id
	requestedRefund := make(map[int]int64)
	for _, id := range orderItemIDs {
		requestedRefund[int(id)]++
	}

	itemsToRefund := make([]refundItem, 0, len(orderItemIDs))

	// Validate and build refund items
	for orderItemId, requestedQty := range requestedRefund {
		item, ok := itemByID[orderItemId]
		if !ok {
			continue // Skip: item does not belong to order
		}

		// Calculate remaining refundable quantity
		originalQty := item.Quantity.IntPart()
		refundedQty := alreadyRefunded[orderItemId]
		remainingQty := originalQty - refundedQty

		if remainingQty <= 0 {
			return nil, fmt.Errorf("order item %d already fully refunded", orderItemId)
		}

		// Cap requested quantity to remaining refundable quantity
		actualRefundQty := requestedQty
		if actualRefundQty > remainingQty {
			return nil, fmt.Errorf("cannot refund %d units of order item %d: only %d units remaining (original: %d, already refunded: %d)",
				requestedQty, orderItemId, remainingQty, originalQty, refundedQty)
		}

		// Add each unit as a separate refundItem (for RestoreStock to sum correctly)
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
// refundable quantity for all order items (e.g. if item has qty 3, already refunded 1, requesting 2 = full refund).
// alreadyRefunded maps order_item_id to quantity already refunded.
func refundCoversFullOrder(orderItems []entity.OrderItem, orderItemIDs []int32, alreadyRefunded map[int]int64) bool {
	// Count requested units per order_item id
	requested := make(map[int]int64)
	for _, id := range orderItemIDs {
		requested[int(id)]++
	}

	// Each order item must have requested >= remaining refundable quantity
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
// alreadyRefunded maps order_item_id to quantity already refunded.
func determineRefundScope(currentStatus entity.OrderStatusName, orderItems []entity.OrderItem, orderItemIDs []int32, alreadyRefunded map[int]int64) ([]refundItem, *cache.Status, error) {
	// Confirmed: full refund only (orderItemIDs validated as empty by caller)
	if currentStatus == entity.Confirmed {
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	// RefundInProgress, PendingReturn, Delivered: full or partial
	partialItems, err := validateAndMapOrderItems(orderItems, orderItemIDs, alreadyRefunded)
	if err != nil {
		return nil, nil, err
	}

	if partialItems == nil {
		// Empty IDs = full refund
		return orderItemsToRefundItems(orderItems), &cache.OrderStatusRefunded, nil
	}

	// orderItemIDs covers all remaining refundable quantities = full refund
	if refundCoversFullOrder(orderItems, orderItemIDs, alreadyRefunded) {
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
// Allowed statuses: refund_in_progress, pending_return, delivered, confirmed.
// Full or partial: RefundInProgress, PendingReturn, Delivered.
// Full refund only: Confirmed.
func (ms *MYSQLStore) RefundOrder(ctx context.Context, orderUUID string, orderItemIDs []int32, reason string) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUIDForUpdate(ctx, rep, orderUUID)
		if err != nil {
			return err
		}

		orderStatus, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}
		os := orderStatus.Status.Name

		allowed := os == entity.RefundInProgress || os == entity.PendingReturn || os == entity.Delivered || os == entity.Confirmed
		if !allowed {
			return fmt.Errorf("order status must be refund_in_progress, pending_return, delivered or confirmed, got %s", orderStatus.Status.Name)
		}
		if os == entity.Confirmed && len(orderItemIDs) > 0 {
			return fmt.Errorf("confirmed orders support only full refund")
		}

		itemsMap, err := getOrdersItems(ctx, rep, order.Id)
		if err != nil {
			return fmt.Errorf("get order items: %w", err)
		}

		orderItems := itemsMap[order.Id]
		if len(orderItems) == 0 {
			return fmt.Errorf("order has no items")
		}

		// Get already refunded quantities to prevent over-refunding
		alreadyRefundedMap, err := getRefundedQuantitiesByOrderIds(ctx, rep, []int{order.Id})
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

		refundedAmount := refundAmountFromItems(itemsForStock, order.Currency)
		return updateOrderStatusAndRefundedAmount(ctx, rep, order.Id, targetStatus.Status.Id, refundedAmount, reason)
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

func cancelOrder(ctx context.Context, rep dependency.Repository, order *entity.Order, orderItems []entity.OrderItemInsert, source entity.StockChangeSource, refundReason string) error {
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
		err := removePromo(ctx, rep, order.Id)
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

// GetStuckPlacedOrders returns orders in Placed status older than the given time.
// These are orders that never reached InsertFiatInvoice (e.g. crash/network failure after CreateOrder).
func (ms *MYSQLStore) GetStuckPlacedOrders(ctx context.Context, olderThan time.Time) ([]entity.Order, error) {
	query := `
    SELECT co.*
    FROM customer_order co
    WHERE co.order_status_id = :status AND co.placed < :olderThan
    `
	orders, err := QueryListNamed[entity.Order](ctx, ms.DB(), query, map[string]interface{}{
		"status":    cache.OrderStatusPlaced.Status.Id,
		"olderThan": olderThan,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get stuck placed orders: %w", err)
	}
	return orders, nil
}

// GetExpiredAwaitingPaymentOrders returns orders in AwaitingPayment status where payment.expired_at < now.
// Safety net for when payment monitors fail to expire orders on time.
func (ms *MYSQLStore) GetExpiredAwaitingPaymentOrders(ctx context.Context, now time.Time) ([]entity.Order, error) {
	query := `
    SELECT co.*
    FROM customer_order co
    JOIN payment p ON co.id = p.order_id
    WHERE co.order_status_id = :status AND p.expired_at IS NOT NULL AND p.expired_at < :now
    `
	orders, err := QueryListNamed[entity.Order](ctx, ms.DB(), query, map[string]interface{}{
		"status": cache.OrderStatusAwaitingPayment.Status.Id,
		"now":    now,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.Order{}, nil
		}
		return nil, fmt.Errorf("can't get expired awaiting payment orders: %w", err)
	}
	return orders, nil
}

func (ms *MYSQLStore) CancelOrder(ctx context.Context, orderUUID string) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Lock the order row to prevent race conditions with concurrent status updates
		order, err := getOrderByUUIDForUpdate(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		// Get order items inside the transaction
		orderItems, err := getOrderItemsInsert(ctx, rep, order.Id)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		err = cancelOrder(ctx, rep, order, orderItems, entity.StockChangeSourceOrderCancelled, "")
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

// SetOrderStatusToPendingReturn sets an order status to PendingReturn with validation
func (ms *MYSQLStore) SetOrderStatusToPendingReturn(ctx context.Context, orderUUID string, changedBy string) error {
	pendingReturnStatus, ok := cache.GetOrderStatusByName(entity.PendingReturn)
	if !ok {
		return fmt.Errorf("can't get order status by name %s", entity.PendingReturn)
	}

	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUIDForUpdate(ctx, rep, orderUUID)
		if err != nil {
			return fmt.Errorf("get order by uuid: %w", err)
		}
		return updateOrderStatusWithValidation(ctx, rep, order.Id, pendingReturnStatus.Status.Id, changedBy, "User requested return")
	})
}

// CancelOrderByUser allows a user to cancel or request a refund for their order
func (ms *MYSQLStore) CancelOrderByUser(ctx context.Context, orderUUID string, email string, reason string) (*entity.OrderFull, error) {
	// Update order status and reason in transaction
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Lock the order row and verify ownership to prevent race conditions
		order, err := getOrderByUUIDAndEmailForUpdate(ctx, rep, orderUUID, email)
		if err != nil {
			return fmt.Errorf("order not found: %w", err)
		}

		// Get current order status
		orderStatus, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}

		currentStatus := orderStatus.Status.Name

		// Check if order is already in a refund/cancellation state
		if currentStatus == entity.Cancelled ||
			currentStatus == entity.PendingReturn ||
			currentStatus == entity.RefundInProgress ||
			currentStatus == entity.Refunded ||
			currentStatus == entity.PartiallyRefunded {
			return fmt.Errorf("order already in refund progress or refunded: current status %s", currentStatus)
		}

		// Determine action based on current status
		switch currentStatus {
		case entity.Placed, entity.AwaitingPayment:
			// Use cancelOrder for shared logic (restore stock, remove promo, status update)
			orderItems, err := getOrderItemsInsert(ctx, rep, order.Id)
			if err != nil {
				return fmt.Errorf("can't get order items: %w", err)
			}
			if err := cancelOrder(ctx, rep, order, orderItems, entity.StockChangeSourceOrderCancelled, reason); err != nil {
				return fmt.Errorf("can't cancel order: %w", err)
			}
		case entity.Confirmed:
			// Set to RefundInProgress
			query := `
				UPDATE customer_order
				SET order_status_id = :orderStatusId,
					refund_reason = :refundReason
				WHERE id = :orderId`
			err = ExecNamed(ctx, rep.DB(), query, map[string]any{
				"orderId":       order.Id,
				"orderStatusId": cache.OrderStatusRefundInProgress.Status.Id,
				"refundReason":  reason,
			})
			if err != nil {
				return fmt.Errorf("can't update order status and reason: %w", err)
			}
			if err := insertOrderStatusHistoryEntry(ctx, rep, order.Id, cache.OrderStatusRefundInProgress.Status.Id, "user", reason); err != nil {
				return fmt.Errorf("can't insert order status history: %w", err)
			}
		case entity.Shipped, entity.Delivered:
			// Set to PendingReturn
			query := `
				UPDATE customer_order
				SET order_status_id = :orderStatusId,
					refund_reason = :refundReason
				WHERE id = :orderId`
			err = ExecNamed(ctx, rep.DB(), query, map[string]any{
				"orderId":       order.Id,
				"orderStatusId": cache.OrderStatusPendingReturn.Status.Id,
				"refundReason":  reason,
			})
			if err != nil {
				return fmt.Errorf("can't update order status and reason: %w", err)
			}
			if err := insertOrderStatusHistoryEntry(ctx, rep, order.Id, cache.OrderStatusPendingReturn.Status.Id, "user", reason); err != nil {
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

	// Refresh order details after update
	orderFull, err := ms.GetOrderByUUIDAndEmail(ctx, orderUUID, email)
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
