package order

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// getProductsByIds uses the Products interface to fetch products, avoiding
// duplication of the complex product query from the products store.
func getProductsByIds(ctx context.Context, rep dependency.Repository, productIds []int) ([]entity.Product, error) {
	if len(productIds) == 0 {
		return []entity.Product{}, nil
	}
	return rep.Products().GetProductsByIds(ctx, productIds)
}

func getProductsSizesByIds(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) ([]entity.ProductSize, error) {
	return getProductsSizesByIdsWithLock(ctx, db, items, false)
}

func getProductsSizesByIdsForUpdate(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) ([]entity.ProductSize, error) {
	return getProductsSizesByIdsWithLock(ctx, db, items, true)
}

func getProductsSizesByIdsWithLock(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert, forUpdate bool) ([]entity.ProductSize, error) {
	if len(items) == 0 {
		return []entity.ProductSize{}, nil
	}

	var productSizeParams []interface{}
	var conditions []string
	for _, item := range items {
		conditions = append(conditions, "(product_id = ? AND size_id = ?)")
		productSizeParams = append(productSizeParams, item.ProductId, item.SizeId)
	}

	query := fmt.Sprintf(`
		SELECT product_id, size_id, quantity
		FROM product_size
		WHERE %s`, joinConditions(conditions))

	if forUpdate {
		query += " FOR UPDATE"
	}

	var prdSizes []entity.ProductSize
	err := db.SelectContext(ctx, &prdSizes, query, productSizeParams...)
	if err != nil {
		return nil, fmt.Errorf("get product sizes: %w", err)
	}
	return prdSizes, nil
}

func joinConditions(conditions []string) string {
	if len(conditions) == 0 {
		return "1=0"
	}
	result := conditions[0]
	for _, c := range conditions[1:] {
		result += " OR " + c
	}
	return result
}

func validateOrderItemsStockAvailability(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	return validateOrderItemsStockAvailabilityWithLock(ctx, rep, items, currency, false)
}

func validateOrderItemsStockAvailabilityForUpdate(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	return validateOrderItemsStockAvailabilityWithLock(ctx, rep, items, currency, true)
}

func validateOrderItemsStockAvailabilityWithLock(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, currency string, forUpdate bool) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	if len(items) == 0 {
		return nil, nil, &entity.ValidationError{Message: "zero items to validate"}
	}

	prdIds := getProductIdsFromItems(items)

	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	prdMap := make(map[int]entity.Product)
	for _, prd := range prds {
		prdMap[prd.Id] = prd
	}

	db := rep.DB()
	var prdSizes []entity.ProductSize
	if forUpdate {
		prdSizes, err = getProductsSizesByIdsForUpdate(ctx, db, items)
	} else {
		prdSizes, err = getProductsSizesByIds(ctx, db, items)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products sizes by ids: %w", err)
	}

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

func validateOrderItemsStockForCustomOrder(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	if len(items) == 0 {
		return nil, nil, &entity.ValidationError{Message: "zero items to validate"}
	}
	prdIds := getProductIdsFromItems(items)
	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products by ids: %w", err)
	}
	prdMap := make(map[int]entity.Product)
	for _, prd := range prds {
		prdMap[prd.Id] = prd
	}
	db := rep.DB()
	prdSizes, err := getProductsSizesByIdsForUpdate(ctx, db, items)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products sizes by ids: %w", err)
	}
	prdSizeMap := make(map[string]entity.ProductSize)
	for _, ps := range prdSizes {
		prdSizeMap[fmt.Sprintf("%d-%d", ps.ProductId, ps.SizeId)] = ps
	}
	validItems := make([]entity.OrderItem, 0, len(items))
	adjustments := make([]entity.OrderItemAdjustment, 0)
	for _, item := range items {
		sizeKey := fmt.Sprintf("%d-%d", item.ProductId, item.SizeId)
		prdSize, exists := prdSizeMap[sizeKey]
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
		var productName string
		if len(prd.ProductDisplay.ProductBody.Translations) > 0 {
			productName = prd.ProductDisplay.ProductBody.Translations[0].Name
		}
		pb := &prd.ProductDisplay.ProductBody
		validItems = append(validItems, entity.OrderItem{
			OrderItemInsert: item,
			Thumbnail:       prd.ProductDisplay.Thumbnail.ThumbnailMediaURL,
			BlurHash:        prd.ProductDisplay.Thumbnail.BlurHash.String,
			ProductBrand:    pb.ProductBodyInsert.Brand,
			Color:           pb.ProductBodyInsert.Color,
			SKU:             prd.SKU,
			Slug:            dto.GetProductSlug(prd.Id, pb.ProductBodyInsert.Brand, productName, pb.ProductBodyInsert.TargetGender.String()),
			TopCategoryId:   pb.ProductBodyInsert.TopCategoryId,
			SubCategoryId:   pb.ProductBodyInsert.SubCategoryId,
			TypeId:          pb.ProductBodyInsert.TypeId,
			TargetGender:    pb.ProductBodyInsert.TargetGender,
			Preorder:        pb.ProductBodyInsert.Preorder,
			Translations:    pb.Translations,
		})
	}
	return validItems, adjustments, nil
}

func compareItems(items, validItems []entity.OrderItemInsert, onlyQuantity bool) bool {
	sort.Sort(entity.OrderItemsByProductId(items))
	sort.Sort(entity.OrderItemsByProductId(validItems))

	if len(items) != len(validItems) {
		return false
	}

	for i := range items {
		if onlyQuantity {
			if items[i].ProductId != validItems[i].ProductId ||
				items[i].SizeId != validItems[i].SizeId ||
				items[i].QuantityDecimal().Cmp(validItems[i].QuantityDecimal()) != 0 {
				return false
			}
		} else {
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

func calculateTotalAmount(items []entity.ProductInfoProvider, currency string) (decimal.Decimal, error) {
	if len(items) == 0 {
		return decimal.Zero, errors.New("no items to calculate total amount")
	}

	var totalAmount decimal.Decimal

	for _, item := range items {
		if !item.GetQuantity().IsPositive() {
			return decimal.Zero, &entity.ValidationError{Message: fmt.Sprintf("quantity for product ID %d is not positive", item.GetProductId())}
		}

		price := item.GetProductPrice()

		salePercentage := item.GetProductSalePercentage()
		if salePercentage.GreaterThan(decimal.Zero) {
			price = price.Mul(decimal.NewFromInt(100).Sub(salePercentage).Div(decimal.NewFromInt(100)))
		}

		totalAmount = totalAmount.Add(price.Mul(item.GetQuantity()))
	}

	return dto.RoundForCurrency(totalAmount, currency), nil
}

func mergeOrderItems(items []entity.OrderItemInsert) []entity.OrderItemInsert {
	type itemKey struct {
		ProductId int
		SizeId    int
	}

	mergedItems := make(map[itemKey]entity.OrderItemInsert)

	for _, item := range items {
		if item.Quantity.IsZero() {
			continue
		}

		key := itemKey{ProductId: item.ProductId, SizeId: item.SizeId}

		if existingItem, ok := mergedItems[key]; ok {
			existingItem.Quantity = existingItem.QuantityDecimal().Add(item.QuantityDecimal())
			mergedItems[key] = existingItem
		} else {
			mergedItems[key] = item
		}
	}

	mergedSlice := make([]entity.OrderItemInsert, 0, len(mergedItems))
	for _, item := range mergedItems {
		mergedSlice = append(mergedSlice, item)
	}

	return mergedSlice
}

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

func (s *Store) validateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert, currency string) ([]entity.OrderItem, []entity.OrderItemAdjustment, error) {
	items, capAdjustments := adjustQuantities(cache.GetMaxOrderItems(), items)

	slog.Default().InfoContext(ctx, "items", slog.Any("items", items))

	validItems, stockAdjustments, err := validateOrderItemsStockAvailability(ctx, s.repFunc(), items, currency)
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

	allAdjustments := make([]entity.OrderItemAdjustment, 0, len(capAdjustments)+len(stockAdjustments))
	allAdjustments = append(allAdjustments, capAdjustments...)
	allAdjustments = append(allAdjustments, stockAdjustments...)

	return validItems, allAdjustments, nil
}

func (s *Store) validateOrderItemsInsertWithLock(ctx context.Context, items []entity.OrderItemInsert, currency string, lockStock bool) (*entity.OrderItemValidation, error) {
	if len(items) == 0 {
		return nil, &entity.ValidationError{Message: "no order items to insert"}
	}

	copiedItems := make([]entity.OrderItemInsert, len(items))
	copy(copiedItems, items)

	mergedItems := mergeOrderItems(copiedItems)

	var validItems []entity.OrderItem
	var itemAdjustments []entity.OrderItemAdjustment
	var err error

	if lockStock {
		validItems, itemAdjustments, err = validateOrderItemsStockAvailabilityForUpdate(ctx, s.repFunc(), mergedItems, currency)
	} else {
		validItems, itemAdjustments, err = s.validateOrderItemsInsert(ctx, mergedItems, currency)
	}

	if err != nil {
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			return nil, err
		}
		return nil, fmt.Errorf("error while validating order items: %w", err)
	}

	if len(validItems) == 0 {
		return nil, &entity.ValidationError{Message: "zero valid order items to insert"}
	}

	validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

	providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
	total, err := calculateTotalAmount(providers, currency)
	if err != nil {
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			return nil, err
		}
		return nil, fmt.Errorf("error while calculating total amount: %w", err)
	}

	if total.IsZero() {
		return nil, &entity.ValidationError{Message: "total amount is zero"}
	}

	return &entity.OrderItemValidation{
		ValidItems:      validItems,
		Subtotal:        dto.RoundForCurrency(total, currency),
		HasChanged:      !compareItems(copiedItems, validItemsInsert, true),
		ItemAdjustments: itemAdjustments,
	}, nil
}

// ValidateOrderItemsInsert validates the order items and returns the valid items and the total amount.
func (s *Store) ValidateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert, currency string) (*entity.OrderItemValidation, error) {
	return s.validateOrderItemsInsertWithLock(ctx, items, currency, false)
}

// ValidateOrderItemsInsertWithReservation validates order items with stock reservation awareness.
func (s *Store) ValidateOrderItemsInsertWithReservation(ctx context.Context, items []entity.OrderItemInsert, currency string, sessionID string) (*entity.OrderItemValidation, error) {
	return s.validateOrderItemsInsertWithLock(ctx, items, currency, false)
}

// validateOrderItemsInsertForUpdate validates order items and locks product_size rows.
func (s *Store) validateOrderItemsInsertForUpdate(ctx context.Context, items []entity.OrderItemInsert, currency string) (*entity.OrderItemValidation, error) {
	return s.validateOrderItemsInsertWithLock(ctx, items, currency, true)
}

// validateAndUpdateOrderIfNeeded validates order items and updates them if they've changed.
func validateAndUpdateOrderIfNeeded(ctx context.Context, rep dependency.Repository, os *Store, orderFull *entity.OrderFull, cancelOnValidationFailure bool) (bool, error) {
	return validateAndUpdateOrderIfNeededWithLock(ctx, rep, os, orderFull, cancelOnValidationFailure, false)
}

// validateAndUpdateOrderIfNeededForUpdate is like validateAndUpdateOrderIfNeeded but locks product_size rows.
func validateAndUpdateOrderIfNeededForUpdate(ctx context.Context, rep dependency.Repository, os *Store, orderFull *entity.OrderFull, cancelOnValidationFailure bool) (bool, error) {
	return validateAndUpdateOrderIfNeededWithLock(ctx, rep, os, orderFull, cancelOnValidationFailure, true)
}

func validateAndUpdateOrderIfNeededWithLock(ctx context.Context, rep dependency.Repository, os *Store, orderFull *entity.OrderFull, cancelOnValidationFailure bool, lockStock bool) (bool, error) {
	items := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)

	var oiv *entity.OrderItemValidation
	var err error

	if lockStock {
		oiv, err = os.validateOrderItemsInsertForUpdate(ctx, items, orderFull.Order.Currency)
	} else {
		oiv, err = os.ValidateOrderItemsInsert(ctx, items, orderFull.Order.Currency)
	}

	if err != nil {
		if cancelOnValidationFailure {
			if cancelErr := cancelOrder(ctx, rep, &orderFull.Order, items, entity.StockChangeSourceOrderCancelled, ""); cancelErr != nil {
				return false, fmt.Errorf("cannot cancel order after validation failure: %w", cancelErr)
			}
		}
		return false, fmt.Errorf("error validating order items: %w", err)
	}

	validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(oiv.ValidItems)

	if !compareItems(items, validItemsInsert, false) {
		if err := updateOrderItems(ctx, rep.DB(), validItemsInsert, orderFull.Order.Id); err != nil {
			return false, fmt.Errorf("error updating order items: %w", err)
		}

		if _, err := updateTotalAmount(ctx, rep.DB(), orderFull.Order.Id, oiv.SubtotalDecimal(), orderFull.PromoCode, orderFull.Shipment, orderFull.Order.Currency); err != nil {
			return false, fmt.Errorf("error updating total amount: %w", err)
		}

		return true, nil
	}

	return false, nil
}

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

func validatePaymentMethodAllowed(pm *entity.PaymentMethod) error {
	if !pm.Allowed {
		return fmt.Errorf("payment method is not allowed: payment method id %d", pm.Id)
	}
	return nil
}

func validateShipmentCarrier(carrierId int, shippingCountry string) (*entity.ShipmentCarrier, error) {
	carrier, ok := cache.GetShipmentCarrierById(carrierId)
	if !ok {
		return nil, fmt.Errorf("shipment carrier does not exist: carrier id %d", carrierId)
	}
	if !carrier.Allowed {
		return nil, fmt.Errorf("shipment carrier is not allowed: carrier id %d", carrierId)
	}
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

// ValidateOrderByUUID validates an order by UUID, updating items if needed.
func (s *Store) ValidateOrderByUUID(ctx context.Context, uuid string) (*entity.OrderFull, error) {
	orderFull, err := s.GetOrderFullByUUID(ctx, uuid)
	if err != nil {
		return nil, fmt.Errorf("error while getting order by uuid: %w", err)
	}

	oStatus, err := getOrderStatus(orderFull.Order.OrderStatusId)
	if err != nil {
		return nil, err
	}

	if oStatus.Status.Name != entity.Placed {
		return orderFull, nil
	}

	var itemsChanged bool
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		txOrderStore := &Store{Base: storeutil.Base{DB: rep.DB(), Now: s.Now}, txFunc: s.txFunc, repFunc: func() dependency.Repository { return rep }}
		var err error
		itemsChanged, err = validateAndUpdateOrderIfNeeded(ctx, rep, txOrderStore, orderFull, true)
		return err
	})

	if err != nil {
		return nil, err
	}

	if itemsChanged {
		return nil, ErrOrderItemsUpdated
	}

	return orderFull, nil
}
