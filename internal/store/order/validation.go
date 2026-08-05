package order

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/canonical"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/slug"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// getProductsByIds uses the Products interface to fetch products, avoiding
// duplication of the complex product query from the products store.
func getProductsByIds(ctx context.Context, rep dependency.Repository, productIds []int) ([]entity.Colorway, error) {
	if len(productIds) == 0 {
		return []entity.Colorway{}, nil
	}
	return rep.Products().GetProductsByIds(ctx, productIds)
}

func canonicalProductName(translations []entity.ColorwayTranslationInsert, fallback string) string {
	if name, ok := canonical.ProductName(translations, cache.GetLanguages()); ok {
		return name
	}
	return fallback
}

func getProductsSizesByIds(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) ([]entity.Variant, error) {
	return getProductsSizesByIdsWithLock(ctx, db, items, false)
}

func getProductsSizesByIdsForUpdate(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) ([]entity.Variant, error) {
	return getProductsSizesByIdsWithLock(ctx, db, items, true)
}

func getProductsSizesByIdsWithLock(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert, forUpdate bool) ([]entity.Variant, error) {
	if len(items) == 0 {
		return []entity.Variant{}, nil
	}

	var productSizeParams []interface{}
	var conditions []string
	for _, item := range items {
		conditions = append(conditions, "(product_id = ? AND size_id = ? AND grade = ?)")
		productSizeParams = append(productSizeParams, item.ProductId, item.SizeId, gradeOrA(item.Grade))
	}

	query := fmt.Sprintf(`
		SELECT product_id, size_id, quantity, grade
		FROM product_size
		WHERE status = 1 AND (%s)`, joinConditions(conditions))

	if forUpdate {
		query += " FOR UPDATE"
	}

	var prdSizes []entity.Variant
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

// gradeOrA normalises an order line's grade: empty (pre-0251 rows, legacy callers, unresolved
// lines) means the sellable default 'A'.
func gradeOrA(grade string) string {
	return entity.NormalizeVariantGrade(grade)
}

// fetchVariantPrices loads the manual per-variant prices (product_size_price, 0251) for the
// B-grade lines in items, keyed variantID → UPPER(currency) → price. A-grade lines price from
// the product catalogue and are skipped. Empty map when the cart has no B lines.
func fetchVariantPrices(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) (map[int]map[string]decimal.Decimal, error) {
	seen := make(map[int]struct{}, len(items))
	ids := make([]int, 0, len(items))
	for _, it := range items {
		if it.Grade != entity.VariantGradeB || it.VariantID == 0 {
			continue
		}
		if _, ok := seen[it.VariantID]; ok {
			continue
		}
		seen[it.VariantID] = struct{}{}
		ids = append(ids, it.VariantID)
	}
	out := make(map[int]map[string]decimal.Decimal, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		VariantID int             `db:"product_size_id"`
		Currency  string          `db:"currency"`
		Price     decimal.Decimal `db:"price"`
	}](ctx, db, `SELECT product_size_id, currency, price FROM product_size_price WHERE product_size_id IN (:ids)`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("fetch variant prices: %w", err)
	}
	for _, r := range rows {
		if out[r.VariantID] == nil {
			out[r.VariantID] = make(map[string]decimal.Decimal)
		}
		out[r.VariantID][strings.ToUpper(r.Currency)] = r.Price
	}
	return out, nil
}

// resolvedVariantRow is the product_size projection used to resolve an order line's variant addressing.
type resolvedVariantRow struct {
	ID        int    `db:"id"`
	ProductID int    `db:"product_id"`
	SizeID    int    `db:"size_id"`
	SKU       string `db:"sku"`
	Grade     string `db:"grade"`
}

// resolveVariantAddressing fills each line's canonical variant fields (VariantID, ProductId, SizeId,
// VariantSKU, Grade) from whichever addressing the caller supplied: the public variant_sku (storefront
// submit), the internal variant_id (admin custom order), or an already-resolved (product_id, size_id)
// pair (order re-validation loaded from the DB). The sku/id modes carry the variant's own grade — a
// '-B' SKU submitted directly resolves to the B row (0251: sellable once manually priced). The pair
// mode stays pinned to grade 'A': (product_id, size_id) alone is ambiguous once a B row exists, and
// every pair-addressed caller re-validates lines whose variant_id is already resolved (B lines re-enter
// through the id mode). It is idempotent (re-resolving a resolved line reproduces the same fields).
// Lines that map to no live product_size are left with ProductId==0 so the availability check
// downstream drops them as an out-of-stock adjustment, identified by the retained VariantSKU.
// Read-only: variants are archived, never deleted (FK RESTRICT), so the row an address resolves to is
// stable under the later FOR UPDATE lock.
func resolveVariantAddressing(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) error {
	var params []interface{}
	var conds []string
	for i := range items {
		it := &items[i]
		switch {
		case it.VariantID != 0:
			conds = append(conds, "id = ?")
			params = append(params, it.VariantID)
		case it.ProductId != 0 && it.SizeId != 0:
			conds = append(conds, "(product_id = ? AND size_id = ? AND grade = 'A')")
			params = append(params, it.ProductId, it.SizeId)
		case it.VariantSKU != "":
			conds = append(conds, "sku = ?")
			params = append(params, it.VariantSKU)
		}
	}
	if len(conds) == 0 {
		return nil
	}
	query := `SELECT id, product_id, size_id, COALESCE(sku, '') AS sku, grade FROM product_size WHERE ` + joinConditions(conds)
	var rows []resolvedVariantRow
	if err := db.SelectContext(ctx, &rows, query, params...); err != nil {
		return fmt.Errorf("resolve variant addressing: %w", err)
	}
	byID := make(map[int]int, len(rows))
	bySKU := make(map[string]int, len(rows))
	byPair := make(map[[2]int]int, len(rows))
	for i := range rows {
		byID[rows[i].ID] = i
		if rows[i].SKU != "" {
			bySKU[rows[i].SKU] = i
		}
		if rows[i].Grade == entity.VariantGradeA {
			byPair[[2]int{rows[i].ProductID, rows[i].SizeID}] = i
		}
	}
	for i := range items {
		it := &items[i]
		idx := -1
		switch {
		case it.VariantID != 0:
			if j, ok := byID[it.VariantID]; ok {
				idx = j
			}
		case it.ProductId != 0 && it.SizeId != 0:
			if j, ok := byPair[[2]int{it.ProductId, it.SizeId}]; ok {
				idx = j
			}
		case it.VariantSKU != "":
			if j, ok := bySKU[it.VariantSKU]; ok {
				idx = j
			}
		}
		if idx < 0 {
			continue // unresolved: dropped downstream as an out-of-stock adjustment
		}
		r := rows[idx]
		it.VariantID = r.ID
		it.ProductId = r.ProductID
		it.SizeId = r.SizeID
		it.VariantSKU = r.SKU
		it.Grade = r.Grade
	}
	return nil
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

	// Resolve the public/internal variant addressing to the denormalised (product_id, size_id) pair the
	// rest of this path keys on, plus the stable variant_id/variant_sku snapshots the insert writes.
	if err := resolveVariantAddressing(ctx, rep.DB(), items); err != nil {
		return nil, nil, fmt.Errorf("can't resolve variant addressing: %w", err)
	}

	prdIds := getProductIdsFromItems(items)

	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	prdMap := make(map[int]entity.Colorway)
	for _, prd := range prds {
		prdMap[prd.Id] = prd
	}

	db := rep.DB()
	var prdSizes []entity.Variant
	if forUpdate {
		prdSizes, err = getProductsSizesByIdsForUpdate(ctx, db, items)
	} else {
		prdSizes, err = getProductsSizesByIds(ctx, db, items)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products sizes by ids: %w", err)
	}

	prdSizeMap := make(map[string]entity.Variant)
	for _, prdSize := range prdSizes {
		key := fmt.Sprintf("%d-%d-%s", prdSize.ProductId, prdSize.SizeId, gradeOrA(prdSize.Grade))
		prdSizeMap[key] = prdSize
	}

	// Manual per-variant prices for the B-grade lines in the cart (0251). Loaded once; a B line
	// with no price row in the order currency is rejected (fail-closed — unpriced seconds are
	// not sellable), mirroring getProductPrice's missing-currency error for A lines.
	variantPrices, err := fetchVariantPrices(ctx, db, items)
	if err != nil {
		return nil, nil, fmt.Errorf("can't fetch variant prices: %w", err)
	}

	validItems := make([]entity.OrderItem, 0, len(items))
	adjustments := make([]entity.OrderItemAdjustment, 0)

	for _, item := range items {
		// Reject non-positive quantities outright: this is malformed/abusive input
		// (the raw quantity feeds subtotal math, so a negative value would produce a
		// negative total / store credit), not a soft out-of-stock adjustment.
		if item.Quantity.LessThanOrEqual(decimal.Zero) {
			return nil, nil, &entity.ValidationError{Message: fmt.Sprintf("invalid quantity for product %d size %d: must be positive", item.ProductId, item.SizeId)}
		}

		sizeKey := fmt.Sprintf("%d-%d-%s", item.ProductId, item.SizeId, gradeOrA(item.Grade))
		prdSize, exists := prdSizeMap[sizeKey]

		if !exists || !prdSize.QuantityDecimal().GreaterThan(decimal.Zero) {
			adjustments = append(adjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				VariantSKU:        item.VariantSKU,
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
				VariantSKU:        item.VariantSKU,
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
				VariantSKU:        item.VariantSKU,
				RequestedQuantity: item.QuantityDecimal(),
				AdjustedQuantity:  decimal.Zero,
				Reason:            entity.AdjustmentReasonOutOfStock,
			})
			continue
		}

		productBody := &prd.ProductDisplay.ProductBody
		if item.Grade == entity.VariantGradeB {
			// B-grade sells ONLY at its manual per-variant price (0251): exact, no sale
			// percentage on top (the manual figure IS the discounted price), fail-closed
			// when unpriced in the order currency.
			bPrice, ok := variantPrices[item.VariantID][strings.ToUpper(currency)]
			if !ok {
				return nil, nil, &entity.ValidationError{Message: fmt.Sprintf("seconds variant %s has no manual price in currency %s", item.VariantSKU, currency)}
			}
			if verr := requirePositivePrice(item.ProductId, bPrice); verr != nil {
				return nil, nil, verr
			}
			item.ProductPrice = bPrice
			item.ProductSalePercentage = decimal.Zero
			item.ProductPriceWithSale = bPrice
		} else {
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
		}

		productName := canonicalProductName(productBody.Translations, "")

		validItem := entity.OrderItem{
			OrderItemInsert: item,
			Thumbnail:       prd.ProductDisplay.Thumbnail.ThumbnailMediaURL,
			BlurHash:        prd.ProductDisplay.Thumbnail.BlurHash.String,
			ProductBrand:    productBody.ProductBodyInsert.Brand,
			Color:           productBody.ProductBodyInsert.Color,
			SKU:             item.VariantSKU,
			Slug:            slug.ProductPath(productName, prd.SKU),
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
	if err := resolveVariantAddressing(ctx, rep.DB(), items); err != nil {
		return nil, nil, fmt.Errorf("can't resolve variant addressing: %w", err)
	}
	prdIds := getProductIdsFromItems(items)
	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products by ids: %w", err)
	}
	prdMap := make(map[int]entity.Colorway)
	for _, prd := range prds {
		prdMap[prd.Id] = prd
	}
	db := rep.DB()
	prdSizes, err := getProductsSizesByIdsForUpdate(ctx, db, items)
	if err != nil {
		return nil, nil, fmt.Errorf("can't get products sizes by ids: %w", err)
	}
	prdSizeMap := make(map[string]entity.Variant)
	for _, ps := range prdSizes {
		prdSizeMap[fmt.Sprintf("%d-%d-%s", ps.ProductId, ps.SizeId, gradeOrA(ps.Grade))] = ps
	}
	// Custom orders price lines from the admin-supplied amounts, but a B-grade line still requires
	// its manual base-currency price row (0251): the insert snapshots product_price_base from it,
	// and without the row every money metric would COALESCE onto the A catalogue price. Checked
	// early so the operator gets a clean "set the variant price first" instead of a late insert error.
	variantPrices, err := fetchVariantPrices(ctx, db, items)
	if err != nil {
		return nil, nil, fmt.Errorf("can't fetch variant prices: %w", err)
	}
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	validItems := make([]entity.OrderItem, 0, len(items))
	adjustments := make([]entity.OrderItemAdjustment, 0)
	for _, item := range items {
		if item.Quantity.LessThanOrEqual(decimal.Zero) {
			return nil, nil, &entity.ValidationError{Message: fmt.Sprintf("invalid quantity for product %d size %d: must be positive", item.ProductId, item.SizeId)}
		}
		// The admin-supplied custom price must satisfy the same positive-price invariant as a standard
		// catalogue line (problem 044): a zero/negative custom_price is a hard rejection, not a silent
		// comp sale. Checked here, before any order/stock/payment mutation, so a bad price creates nothing.
		if verr := requirePositivePrice(item.ProductId, item.ProductPriceDecimal()); verr != nil {
			return nil, nil, verr
		}
		if item.Grade == entity.VariantGradeB {
			if _, ok := variantPrices[item.VariantID][baseCurrency]; !ok {
				return nil, nil, &entity.ValidationError{Message: fmt.Sprintf("seconds variant %s has no manual price set — set the variant price before selling it (even at a custom amount)", item.VariantSKU)}
			}
		}
		sizeKey := fmt.Sprintf("%d-%d-%s", item.ProductId, item.SizeId, gradeOrA(item.Grade))
		prdSize, exists := prdSizeMap[sizeKey]
		if !exists || !prdSize.QuantityDecimal().GreaterThan(decimal.Zero) {
			adjustments = append(adjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				VariantSKU:        item.VariantSKU,
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
				VariantSKU:        item.VariantSKU,
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
				VariantSKU:        item.VariantSKU,
				RequestedQuantity: item.QuantityDecimal(),
				AdjustedQuantity:  decimal.Zero,
				Reason:            entity.AdjustmentReasonOutOfStock,
			})
			continue
		}
		productName := canonicalProductName(prd.ProductDisplay.ProductBody.Translations, "")
		pb := &prd.ProductDisplay.ProductBody
		validItems = append(validItems, entity.OrderItem{
			OrderItemInsert: item,
			Thumbnail:       prd.ProductDisplay.Thumbnail.ThumbnailMediaURL,
			BlurHash:        prd.ProductDisplay.Thumbnail.BlurHash.String,
			ProductBrand:    pb.ProductBodyInsert.Brand,
			Color:           pb.ProductBodyInsert.Color,
			SKU:             item.VariantSKU,
			Slug:            slug.ProductPath(productName, prd.SKU),
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
	// Key on the caller-supplied variant identity, not the (product_id, size_id) pair: merge runs before
	// resolveVariantAddressing on the create paths, so the pair is still zero for storefront (variant_sku)
	// and admin (variant_id) input. Keying on all four fields merges genuinely identical lines while
	// keeping distinct unresolved lines separate (so each surfaces its own adjustment).
	type itemKey struct {
		VariantSKU string
		VariantID  int
		ProductId  int
		SizeId     int
	}

	mergedItems := make(map[itemKey]entity.OrderItemInsert)

	for _, item := range items {
		if item.Quantity.IsZero() {
			continue
		}

		key := itemKey{VariantSKU: item.VariantSKU, VariantID: item.VariantID, ProductId: item.ProductId, SizeId: item.SizeId}

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
				VariantSKU:        item.VariantSKU,
				RequestedQuantity: requestedQty,
				AdjustedQuantity:  maxQuantity.Round(0),
				Reason:            entity.AdjustmentReasonQuantityCapped,
			})
		}
	}
	return items, adjustments
}

// validateOrderItemsTierAccess is the server-authoritative purchase block for tier-gated products.
// It rejects the order if ANY line resolves to a product whose min_tier the buyer does not satisfy
// (entity.TierCanPurchase, including the hacker=99 rule). buyerTier is the un-spoofable tier the
// caller resolved from the authenticated storefront identity (0 for guests); it is INDEPENDENT of
// whatever the storefront displayed — a locked teaser rendered to a guest is what makes the item
// visible, this is what makes it unbuyable. Returns a field-tagged *entity.ValidationError (mapped
// to InvalidArgument by apierr at the RPC boundary); a non-gated cart returns nil.
//
// It operates on a COPY of items so resolving variant addressing here does not disturb the caller's
// slice (CreateOrder re-resolves inside its transaction). Products not returned by getProductsByIds
// (unknown/inactive) are simply absent and dropped downstream as out-of-stock — the tier gate only
// concerns products that actually exist and could otherwise be purchased.
func validateOrderItemsTierAccess(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert, buyerTier int16) error {
	if len(items) == 0 {
		return nil
	}

	copied := make([]entity.OrderItemInsert, len(items))
	copy(copied, items)
	if err := resolveVariantAddressing(ctx, rep.DB(), copied); err != nil {
		return fmt.Errorf("can't resolve variant addressing for tier gate: %w", err)
	}

	prdIds := getProductIdsFromItems(copied)
	prds, err := getProductsByIds(ctx, rep, prdIds)
	if err != nil {
		return fmt.Errorf("can't get products for tier gate: %w", err)
	}

	for i := range prds {
		prd := &prds[i]
		if entity.TierCanPurchase(buyerTier, prd.MinTier()) {
			continue
		}
		howToFix := fmt.Sprintf("requires membership tier %d or higher", prd.MinTier())
		if prd.MinTier() == entity.TierCodeHacker {
			howToFix = "invite-only product; a hacker-tier account is required"
		}
		return entity.NewFieldViolation("items", "tier_locked", prd.SKU, howToFix)
	}
	return nil
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

	allAdjustments := make([]entity.OrderItemAdjustment, 0, len(capAdjustments)+len(stockAdjustments))
	allAdjustments = append(allAdjustments, capAdjustments...)
	allAdjustments = append(allAdjustments, stockAdjustments...)

	if len(validItems) == 0 {
		// Return the adjustments alongside the error so callers that want to
		// surface what was removed (e.g. pre-checkout validation) still can.
		return nil, allAdjustments, &entity.ValidationError{Message: "no valid order items: products or sizes not found, or out of stock"}
	}

	return validItems, allAdjustments, nil
}

// emptyChangedValidation builds a validation result for a cart that ended up
// with no valid items during pre-checkout. It signals HasChanged so the
// frontend refreshes the cart, and carries the adjustments explaining why each
// item was dropped.
func emptyChangedValidation(adjustments []entity.OrderItemAdjustment) *entity.OrderItemValidation {
	return &entity.OrderItemValidation{
		ValidItems:      []entity.OrderItem{},
		Subtotal:        decimal.Zero,
		HasChanged:      true,
		ItemAdjustments: adjustments,
	}
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
			// Pre-checkout validation (non-lock): an all-invalid cart is not a hard
			// failure. Return an empty, changed result so the frontend can refresh
			// its cart instead of receiving an error.
			if !lockStock {
				return emptyChangedValidation(itemAdjustments), nil
			}
			return nil, err
		}
		return nil, fmt.Errorf("error while validating order items: %w", err)
	}

	if len(validItems) == 0 {
		if !lockStock {
			return emptyChangedValidation(itemAdjustments), nil
		}
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
