// Package product implements product CRUD, stock management, waitlist, and SKU generation.
package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// TxFunc executes f within a serializable transaction with deadlock retry.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// RepFunc returns the current repository.
type RepFunc func() dependency.Repository

// Store implements dependency.Products.
type Store struct {
	storeutil.Base
	txFunc  TxFunc
	repFunc RepFunc
}

// New creates a new product store.
func New(base storeutil.Base, txFunc TxFunc, repFunc RepFunc) *Store {
	return &Store{Base: base, txFunc: txFunc, repFunc: repFunc}
}

// Tx implements dependency.ContextStore.
func (s *Store) Tx(ctx context.Context, fn func(ctx context.Context, store dependency.Repository) error) error {
	return s.txFunc(ctx, fn)
}

// ErrProductInOrders is returned when attempting to delete a product that exists in any order_item.
var ErrProductInOrders = errors.New("product exists in orders")

// AddProduct adds a new product to the product store.
func (s *Store) AddProduct(ctx context.Context, prd *entity.ColorwayNew) (int, error) {
	var prdId int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		if !prd.Product.ProductBodyInsert.SalePercentage.Valid || prd.Product.ProductBodyInsert.SalePercentage.Decimal.LessThan(decimal.Zero) {
			prd.Product.ProductBodyInsert.SalePercentage = decimal.NullDecimal{
				Valid:   true,
				Decimal: decimal.NewFromFloat(0),
			}
		}
		// PR6: every product is a colourway of a style. A product created through the product admin
		// has no explicit style, so synthesise a minimal one from its own header — "одиночка = стиль
		// с 1 цветомоделью" (North Star). style_id is NOT NULL, so this must precede the insert.
		styleId, err := createSyntheticStyle(ctx, rep.DB(), prd.Product)
		if err != nil {
			return fmt.Errorf("can't create style for product: %w", err)
		}
		// Write the garment-level fields onto the synthesised style. Must precede MintProductSKUs,
		// which now resolves season (a style field) from the style.
		if err := writeStyleFields(ctx, rep.DB(), styleId, prd.Product.ProductBodyInsert); err != nil {
			return fmt.Errorf("can't write style fields: %w", err)
		}

		// product.id is AUTO_INCREMENT: let the DB assign it (no more time.Now().Unix(), which
		// collided when two products were created in the same second). The SKU is minted after the
		// sizes exist, from resolved dictionary segments (base + per-size variant).
		prdId, err = insertProduct(ctx, rep.DB(), prd.Product, styleId)
		if err != nil {
			return fmt.Errorf("can't insert product: %w", err)
		}

		err = insertProductTranslations(ctx, rep.DB(), prdId, prd.Product.Translations)
		if err != nil {
			return fmt.Errorf("can't insert product translations: %w", err)
		}

		err = insertSizeMeasurements(ctx, rep.DB(), prd.SizeMeasurements, prdId)
		if err != nil {
			return fmt.Errorf("can't insert size measurements: %w", err)
		}

		if err := MintProductSKUs(ctx, rep.DB(), prdId); err != nil {
			return fmt.Errorf("can't mint product SKUs: %w", err)
		}

		if err := recordStockChangeFromSizeMeasurements(ctx, rep, prdId, prd.SizeMeasurements, entity.StockChangeSourceAdminNewProduct, entity.StockChangeReasonInitialStock); err != nil {
			return fmt.Errorf("can't record stock change history: %w", err)
		}

		err = insertMedia(ctx, rep.DB(), prd.MediaIds, prdId)
		if err != nil {
			return fmt.Errorf("can't insert media: %w", err)
		}
		_, err = insertTags(ctx, rep.DB(), prd.Tags, prdId)
		if err != nil {
			return fmt.Errorf("can't insert tags: %w", err)
		}

		err = validateRequiredCurrencies(prd.Prices)
		if err != nil {
			return fmt.Errorf("price validation failed: %w", err)
		}

		err = insertProductPrices(ctx, rep.DB(), prdId, prd.Prices)
		if err != nil {
			return fmt.Errorf("can't insert product prices: %w", err)
		}

		return nil
	})
	if err != nil {
		return prdId, fmt.Errorf("can't add product: %w", err)
	}

	return prdId, nil
}

// UpdateProduct updates an existing product.
func (s *Store) UpdateProduct(ctx context.Context, prd *entity.ColorwayNew, id int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		err := updateProduct(ctx, rep.DB(), prd.Product, id)
		if err != nil {
			return fmt.Errorf("can't update product: %w", err)
		}

		err = insertProductTranslations(ctx, rep.DB(), id, prd.Product.Translations)
		if err != nil {
			return fmt.Errorf("can't update product translations: %w", err)
		}

		// Stably reconcile the colourway's variants: existing sizes keep their row id and (frozen) SKU,
		// only quantity is refreshed; new sizes are inserted; removed-and-unsold sizes are deleted. A
		// blind delete+reinsert here would blank every product_size.sku, and because MintProductSKUs is
		// a no-op on a frozen product it would permanently destroy the variant identity of a sold
		// colourway (P0). The style-level size chart is a separate concern and IS fully replaced.
		err = reconcileVariants(ctx, rep.DB(), prd.SizeMeasurements, id)
		if err != nil {
			return fmt.Errorf("can't reconcile product variants: %w", err)
		}

		err = replaceStyleChart(ctx, rep.DB(), prd.SizeMeasurements, id)
		if err != nil {
			return fmt.Errorf("can't update style size chart: %w", err)
		}

		if err := recordStockChangeFromSizeMeasurements(ctx, rep, id, prd.SizeMeasurements, entity.StockChangeSourceManualAdjustment, entity.StockChangeReasonCorrection); err != nil {
			return fmt.Errorf("can't record stock change history: %w", err)
		}

		// Re-mint SKUs so an attribute change (colour, season, ...) is reflected while the SKU is
		// unlocked; sizes were delete+reinserted above, and MintProductSKUs regenerates base + every
		// variant. It is a no-op once the product is frozen (sku_locked_at set).
		if err := MintProductSKUs(ctx, rep.DB(), id); err != nil {
			return fmt.Errorf("can't re-mint product SKUs: %w", err)
		}

		err = updateProductMedia(ctx, rep.DB(), id, prd.MediaIds)
		if err != nil {
			return fmt.Errorf("can't update product media: %w", err)
		}

		err = updateProductTags(ctx, rep.DB(), id, prd.Tags)
		if err != nil {
			return fmt.Errorf("can't update product tags: %w", err)
		}

		err = validateRequiredCurrencies(prd.Prices)
		if err != nil {
			return fmt.Errorf("price validation failed: %w", err)
		}

		err = upsertProductPrices(ctx, rep.DB(), id, prd.Prices)
		if err != nil {
			return fmt.Errorf("can't update product prices: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("can't add product: %w", err)
	}

	return nil
}

// DeleteProductById soft deletes a product by its ID.
// Returns ErrProductInOrders if the product exists in any order_item.
func (s *Store) DeleteProductById(ctx context.Context, id int) error {
	type countRow struct {
		N int `db:"n"`
	}
	c, err := storeutil.QueryNamedOne[countRow](ctx, s.DB, `SELECT COUNT(*) AS n FROM order_item WHERE product_id = :productId`, map[string]any{"productId": id})
	if err != nil {
		return fmt.Errorf("check product in orders: %w", err)
	}
	if c.N > 0 {
		return ErrProductInOrders
	}

	query := "UPDATE product SET deleted_at = CURRENT_TIMESTAMP WHERE id = :id AND deleted_at IS NULL"
	return storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id": id,
	})
}

// GetProductByIdShowHidden returns a product by its ID, including hidden products.
func (s *Store) GetProductByIdShowHidden(ctx context.Context, id int) (*entity.ColorwayFull, error) {
	return s.getProductDetails(ctx, map[string]any{"id": id}, true)
}

// GetProductByIdNoHidden returns a product by its ID, excluding hidden products.
func (s *Store) GetProductByIdNoHidden(ctx context.Context, id int) (*entity.ColorwayFull, error) {
	return s.getProductDetails(ctx, map[string]any{"id": id}, false)
}

// GetProductBySKU returns a product by its base SKU (the public resolve key, case-insensitive since
// product.sku is stored uppercase and MySQL string compares are case-insensitive), excluding hidden
// products. This is the storefront resolver for the /p/{pretty}-{sku} URL scheme.
func (s *Store) GetProductBySKU(ctx context.Context, sku string) (*entity.ColorwayFull, error) {
	return s.getProductDetails(ctx, map[string]any{"sku": strings.ToUpper(sku)}, false)
}

// createSyntheticStyle inserts a minimal tech_card (style) for a newly-created product and returns
// its id. In the target model (PR6) every product is a colourway of a style; a product created via
// the product admin carries no explicit style, so we synthesise one from its own header fields (name
// from the first translation, brand/season/collection/gender). The style_number is 'AUTO-' + a unique
// suffix (mirrors migration 0138's convention for backfilled standalones); the operator can flesh the
// style out later. Only style_number + name are NOT NULL on tech_card, the rest default.
func createSyntheticStyle(ctx context.Context, db dependency.DB, product *entity.ColorwayInsert) (int, error) {
	name := "Product"
	if len(product.Translations) > 0 && strings.TrimSpace(product.Translations[0].Name) != "" {
		name = strings.TrimSpace(product.Translations[0].Name)
	}
	// Minimal header (style_number + name only, both NOT NULL). The garment-level fields
	// (brand/season/category/fit/...) are written by writeStyleFields once the style exists —
	// the single source of that field list, shared with the update path (PR6 P2).
	return storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO tech_card (style_number, name)
		VALUES (CONCAT('AUTO-', UUID_SHORT()), :name)`,
		map[string]any{"name": name})
}

// writeStyleFields writes the garment-level catalogue fields onto the STYLE (tech_card). These are
// invariant across a style's colourways (one pattern, colour is the only axis that varies), so the
// style owns them and every colourway (product) reads them from here (PR6 P2). Called after
// createSyntheticStyle on add and from updateProduct on edit — editing any colourway's style-level
// field updates the style, hence all its colourways, which is the intended semantics.
func styleFieldParams(b entity.ColorwayBodyInsert) map[string]any {
	return map[string]any{
		"brand":              b.Brand,
		"season":             string(b.Season),
		"collection":         b.Collection,
		"targetGender":       string(b.TargetGender),
		"fit":                b.Fit,
		"composition":        b.Composition,
		"careInstructions":   b.CareInstructions,
		"modelWearsHeightCm": b.ModelWearsHeightCm,
		"modelWearsSizeId":   b.ModelWearsSizeId,
		"topCategoryId":      b.TopCategoryId,
		"subCategoryId":      b.SubCategoryId,
		"typeId":             b.TypeId,
	}
}

const styleFieldsSet = `
	brand = :brand,
	season_code = :season,
	collection = :collection,
	target_gender = :targetGender,
	fit = :fit,
	composition = :composition,
	care_instructions = :careInstructions,
	model_wears_height_cm = :modelWearsHeightCm,
	model_wears_size_id = :modelWearsSizeId,
	top_category_id = :topCategoryId,
	sub_category_id = :subCategoryId,
	type_id = :typeId`

func writeStyleFields(ctx context.Context, db dependency.DB, styleId int, b entity.ColorwayBodyInsert) error {
	params := styleFieldParams(b)
	params["styleId"] = styleId
	return storeutil.ExecNamed(ctx, db,
		`UPDATE tech_card SET`+styleFieldsSet+` WHERE id = :styleId`, params)
}

func insertProduct(ctx context.Context, db dependency.DB, product *entity.ColorwayInsert, styleId int) (int, error) {
	// id is AUTO_INCREMENT (omitted). sku starts as '' and is minted by MintProductSKUs once the
	// sizes exist. color_code is the required dictionary FK and is the sole color identity.
	// style_id (PR6) is the product's style (colourway->style invariant); AddProduct synthesises one.
	// A cost provided at create time is manual admin input, so stamp its provenance
	// (source='manual', updated_at=now); no cost leaves the provenance columns NULL.
	// Garment-level fields (brand/season/collection/target_gender/fit/composition/care/model_wears/
	// category top-sub-type) live on the STYLE now (PR6 P2) and are written by writeStyleFields, not
	// here. This INSERT carries only colourway-level columns (colour, media, price, country, ...).
	query := `
	INSERT INTO product
	(sku, style_id, preorder, color, color_code, color_hex, country_of_origin, thumbnail_id, secondary_thumbnail_id, sale_percentage, hidden, min_tier, cost_price, cost_price_source, cost_price_updated_at)
	VALUES ('', :styleId, :preorder, (SELECT c.name FROM color c WHERE c.code = :colorCode), :colorCode, :colorHexOverride, :countryOfOrigin, :thumbnailId, :secondaryThumbnailId, :salePercentage, :hidden, :minTier, :costPrice,
		CASE WHEN :costPrice IS NOT NULL THEN 'manual' ELSE NULL END,
		CASE WHEN :costPrice IS NOT NULL THEN NOW() ELSE NULL END)`

	params := map[string]any{
		"styleId":              styleId,
		"preorder":             product.ProductBodyInsert.Preorder,
		"colorCode":            product.ProductBodyInsert.ColorCode,
		"colorHexOverride":     product.ProductBodyInsert.ColorHexOverride,
		"countryOfOrigin":      product.ProductBodyInsert.CountryOfOrigin,
		"thumbnailId":          product.ThumbnailMediaID,
		"secondaryThumbnailId": product.SecondaryThumbnailMediaID,
		"salePercentage":       product.ProductBodyInsert.SalePercentage,
		"hidden":               product.ProductBodyInsert.Hidden,
		"minTier":              product.ProductBodyInsert.MinTier,
		"costPrice":            product.CostPrice,
	}

	id, err := storeutil.ExecNamedLastId(ctx, db, query, params)
	if err != nil {
		return id, err
	}

	return id, nil
}

func insertProductTranslations(ctx context.Context, db dependency.DB, productId int, translations []entity.ColorwayTranslationInsert) error {
	if len(translations) == 0 {
		return fmt.Errorf("translations array cannot be empty")
	}

	rows := make([]map[string]any, 0, len(translations))
	for _, t := range translations {
		row := map[string]any{
			"product_id":  productId,
			"language_id": t.LanguageId,
			"name":        t.Name,
			"description": t.Description,
		}
		rows = append(rows, row)
	}

	deleteQuery := "DELETE FROM product_translation WHERE product_id = :productId"
	err := storeutil.ExecNamed(ctx, db, deleteQuery, map[string]any{
		"productId": productId,
	})
	if err != nil {
		return fmt.Errorf("failed to delete existing translations: %w", err)
	}

	return storeutil.BulkInsert(ctx, db, "product_translation", rows)
}

// reconcileVariants stably reconciles a colourway's product_size rows against the update payload:
// existing sizes keep their row id AND SKU (only quantity is refreshed), new sizes are inserted (SKU
// minted afterwards), and sizes dropped from the payload are removed ONLY when they carry no order
// history. A sold variant that the operator happened to omit is preserved with its SKU/identity intact
// rather than deleted — an ordinary edit of a frozen colourway must not destroy variant SKUs (P0), and
// deleting a sold size would also orphan/cascade its order history.
func reconcileVariants(ctx context.Context, db dependency.DB, sizeMeasurements []entity.SizeWithMeasurementInsert, productID int) error {
	existing, err := storeutil.QueryListNamed[struct {
		SizeID int `db:"size_id"`
	}](ctx, db, `SELECT size_id FROM product_size WHERE product_id = :id`, map[string]any{"id": productID})
	if err != nil {
		return fmt.Errorf("can't load existing variants: %w", err)
	}
	have := make(map[int]bool, len(existing))
	for _, e := range existing {
		have[e.SizeID] = true
	}
	want := make(map[int]bool, len(sizeMeasurements))
	for _, sm := range sizeMeasurements {
		want[sm.ProductSize.SizeId] = true
	}

	// Upsert every incoming size: refresh quantity on an existing row (preserving id + sku), insert a
	// new one (sku stays NULL until minted).
	for _, sm := range sizeMeasurements {
		if have[sm.ProductSize.SizeId] {
			if err := storeutil.ExecNamed(ctx, db,
				`UPDATE product_size SET quantity = :quantity WHERE product_id = :productId AND size_id = :sizeId`,
				map[string]any{"productId": productID, "sizeId": sm.ProductSize.SizeId, "quantity": sm.ProductSize.QuantityDecimal()}); err != nil {
				return fmt.Errorf("can't update variant (size %d): %w", sm.ProductSize.SizeId, err)
			}
			continue
		}
		if _, err := storeutil.ExecNamedLastId(ctx, db,
			`INSERT INTO product_size (product_id, size_id, quantity) VALUES (:productId, :sizeId, :quantity)`,
			map[string]any{"productId": productID, "sizeId": sm.ProductSize.SizeId, "quantity": sm.ProductSize.QuantityDecimal()}); err != nil {
			return fmt.Errorf("can't insert variant (size %d): %w", sm.ProductSize.SizeId, err)
		}
	}

	// Remove sizes no longer in the payload — but never a sold one (would orphan order history).
	for _, e := range existing {
		if want[e.SizeID] {
			continue
		}
		sold, err := storeutil.QueryNamedOne[struct {
			N int `db:"n"`
		}](ctx, db, `SELECT COUNT(*) AS n FROM order_item WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"pid": productID, "sid": e.SizeID})
		if err != nil {
			return fmt.Errorf("can't check variant order history (size %d): %w", e.SizeID, err)
		}
		if sold.N > 0 {
			slog.WarnContext(ctx, "product update: keeping a removed size that has order history",
				slog.Int("product_id", productID), slog.Int("size_id", e.SizeID))
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM product_size WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"pid": productID, "sid": e.SizeID}); err != nil {
			return fmt.Errorf("can't delete removed variant (size %d): %w", e.SizeID, err)
		}
	}
	return nil
}

// replaceStyleChart fully replaces the STYLE's size chart (tech_card_size_measurement) from the update
// payload. The chart is style-level (shared by all colourways) and carries no SKU/stock/identity, so a
// delete+reinsert is correct here — unlike the per-colourway variants handled by reconcileVariants.
func replaceStyleChart(ctx context.Context, db dependency.DB, sizeMeasurements []entity.SizeWithMeasurementInsert, productID int) error {
	for _, sm := range sizeMeasurements {
		for _, m := range sm.Measurements {
			if m.MeasurementNameId == 0 {
				return fmt.Errorf("invalid measurement_name_id: cannot be 0")
			}
		}
	}
	styleRow, err := storeutil.QueryNamedOne[struct {
		StyleId int `db:"style_id"`
	}](ctx, db, `SELECT style_id FROM product WHERE id = :id`, map[string]any{"id": productID})
	if err != nil {
		return fmt.Errorf("can't resolve product style: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, db,
		`DELETE FROM tech_card_size_measurement WHERE tech_card_id = :styleId`,
		map[string]any{"styleId": styleRow.StyleId}); err != nil {
		return fmt.Errorf("can't clear style size chart: %w", err)
	}
	rows := make([]map[string]any, 0)
	for _, sm := range sizeMeasurements {
		for _, m := range sm.Measurements {
			rows = append(rows, map[string]any{
				"tech_card_id":        styleRow.StyleId,
				"size_id":             sm.ProductSize.SizeId,
				"measurement_name_id": m.MeasurementNameId,
				"measurement_value":   m.MeasurementValue,
			})
		}
	}
	if len(rows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_size_measurement", rows); err != nil {
			return fmt.Errorf("can't insert style size chart: %w", err)
		}
	}
	return nil
}

func insertSizeMeasurements(ctx context.Context, db dependency.DB, sizeMeasurements []entity.SizeWithMeasurementInsert, productID int) error {
	for _, sm := range sizeMeasurements {
		for _, m := range sm.Measurements {
			if m.MeasurementNameId == 0 {
				return fmt.Errorf("invalid measurement_name_id: cannot be 0")
			}
		}
	}

	// The per-size stock stays on the colourway (product_size); the measurement values move to the
	// STYLE (tech_card_size_measurement), keyed by size (PR6 P3), so all colourways of a style share
	// one size chart. deleteSizeMeasurements cleared the style's chart on update; a new product's
	// synthesised style starts empty — so a plain insert never conflicts on the UNIQUE key.
	styleRow, err := storeutil.QueryNamedOne[struct {
		StyleId int `db:"style_id"`
	}](ctx, db, `SELECT style_id FROM product WHERE id = :id`, map[string]any{"id": productID})
	if err != nil {
		return fmt.Errorf("can't resolve product style: %w", err)
	}

	rowsStyleMeasurements := make([]map[string]any, 0)

	for _, sm := range sizeMeasurements {
		query := `INSERT INTO product_size (product_id, size_id, quantity) VALUES (:productId, :sizeId, :quantity)`
		_, err := storeutil.ExecNamedLastId(ctx, db, query, map[string]any{
			"productId": productID,
			"sizeId":    sm.ProductSize.SizeId,
			"quantity":  sm.ProductSize.QuantityDecimal(),
		})
		if err != nil {
			return fmt.Errorf("can't insert product size: %w", err)
		}

		for _, m := range sm.Measurements {
			row := map[string]any{
				"tech_card_id":        styleRow.StyleId,
				"size_id":             sm.ProductSize.SizeId,
				"measurement_name_id": m.MeasurementNameId,
				"measurement_value":   m.MeasurementValue,
			}
			rowsStyleMeasurements = append(rowsStyleMeasurements, row)
		}
	}

	if len(rowsStyleMeasurements) > 0 {
		err := storeutil.BulkInsert(ctx, db, "tech_card_size_measurement", rowsStyleMeasurements)
		if err != nil {
			return fmt.Errorf("can't insert product measurements: %w", err)
		}
	}

	return nil
}

func insertMedia(ctx context.Context, db dependency.DB, mediaIds []int, productID int) error {
	rows := make([]map[string]any, 0, len(mediaIds))
	for _, mId := range mediaIds {
		row := map[string]any{
			"product_id": productID,
			"media_id":   mId,
		}
		rows = append(rows, row)
	}
	return storeutil.BulkInsert(ctx, db, "product_media", rows)
}

func insertTags(ctx context.Context, db dependency.DB, tagsInsert []entity.ColorwayTagInsert, productID int) ([]entity.ColorwayTag, error) {
	uniqueTags := make(map[string]bool)
	uniqueTagsSlice := make([]entity.ColorwayTagInsert, 0)
	rows := make([]map[string]any, 0)

	tags := make([]entity.ColorwayTag, 0)

	for _, t := range tagsInsert {
		if _, exists := uniqueTags[t.Tag]; !exists {
			uniqueTags[t.Tag] = true
			uniqueTagsSlice = append(uniqueTagsSlice, t)
		}
	}

	for _, t := range uniqueTagsSlice {
		tags = append(tags, entity.ColorwayTag{
			ColorwayTagInsert: t,
		})
		row := map[string]any{
			"product_id": productID,
			"tag":        t.Tag,
		}
		rows = append(rows, row)
	}

	return tags, storeutil.BulkInsert(ctx, db, "product_tag", rows)
}

func recordStockChangeFromSizeMeasurements(ctx context.Context, rep dependency.Repository, productID int, sizeMeasurements []entity.SizeWithMeasurementInsert, source entity.StockChangeSource, reason entity.StockChangeReason) error {
	if len(sizeMeasurements) == 0 {
		return nil
	}
	adminUsername := auth.GetAdminUsername(ctx)
	entries := make([]entity.StockChangeInsert, 0, len(sizeMeasurements))
	for _, sm := range sizeMeasurements {
		qty := sm.ProductSize.QuantityDecimal()
		e := entity.StockChangeInsert{
			ProductId:      sql.NullInt32{Int32: int32(productID), Valid: true},
			SizeId:         sql.NullInt32{Int32: int32(sm.ProductSize.SizeId), Valid: true},
			QuantityDelta:  qty,
			QuantityBefore: decimal.Zero,
			QuantityAfter:  qty,
			Source:         string(source),
			Reason:         sql.NullString{String: string(reason), Valid: true},
		}
		if adminUsername != "" {
			e.AdminUsername = sql.NullString{String: adminUsername, Valid: true}
		}
		entries = append(entries, e)
	}
	return rep.Products().RecordStockChange(ctx, entries)
}

func updateProduct(ctx context.Context, db dependency.DB, prd *entity.ColorwayInsert, id int) error {
	// Colourway-level columns only. The garment-level fields (brand/season/collection/target_gender/
	// fit/composition/care/model_wears/category top-sub-type) live on the STYLE now (PR6 P2) and are
	// written to tech_card by writeStyleFields below — editing any colourway updates its style, hence
	// all the style's colourways, which is the intended invariant.
	query := `
	UPDATE product
	SET
		preorder = :preorder,
		color = (SELECT c.name FROM color c WHERE c.code = :colorCode),
		color_code = :colorCode,
		color_hex = :colorHexOverride,
		country_of_origin = :countryOfOrigin,
		thumbnail_id = :thumbnailId,
		secondary_thumbnail_id = :secondaryThumbnailId,
		sale_percentage = :salePercentage,
		hidden = :hidden,
		min_tier = :minTier,
		-- Preserve the stored cost when the caller omits it (NULL param), so ordinary
		-- product edits from the admin panel (which does not carry cost) never wipe it.
		-- When a cost IS supplied it is manual admin input, so mark it manual, drop any
		-- tech-card provenance, and stamp the time. When omitted, provenance is untouched.
		-- (No colon may appear anywhere in these comments — sqlx.Named scans the raw query
		-- string and would read a bare colon as an empty bind name and fail the statement.)
		cost_price = COALESCE(:costPrice, cost_price),
		cost_price_source = CASE WHEN :costPrice IS NOT NULL THEN 'manual' ELSE cost_price_source END,
		cost_price_tech_card_id = CASE WHEN :costPrice IS NOT NULL THEN NULL ELSE cost_price_tech_card_id END,
		cost_price_updated_at = CASE WHEN :costPrice IS NOT NULL THEN NOW() ELSE cost_price_updated_at END
	WHERE id = :id
	`
	if err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"preorder":             prd.ProductBodyInsert.Preorder,
		"colorCode":            prd.ProductBodyInsert.ColorCode,
		"colorHexOverride":     prd.ProductBodyInsert.ColorHexOverride,
		"countryOfOrigin":      prd.ProductBodyInsert.CountryOfOrigin,
		"thumbnailId":          prd.ThumbnailMediaID,
		"secondaryThumbnailId": prd.SecondaryThumbnailMediaID,
		"salePercentage":       prd.ProductBodyInsert.SalePercentage,
		"hidden":               prd.ProductBodyInsert.Hidden,
		"minTier":              prd.ProductBodyInsert.MinTier,
		"costPrice":            prd.CostPrice,
		"id":                   id,
	}); err != nil {
		return err
	}
	// Write the garment-level fields onto the product's style. Joined on product.id so we do not need
	// to fetch style_id separately.
	styleParams := styleFieldParams(prd.ProductBodyInsert)
	styleParams["id"] = id
	return storeutil.ExecNamed(ctx, db,
		`UPDATE tech_card st JOIN product p ON p.style_id = st.id SET`+styleFieldsSet+` WHERE p.id = :id`,
		styleParams)
}

// AssignPrimaryTechCardIfUnset makes techCardID the primary (authoritative-for-costing) card
// of each given product that has no primary yet, so the first card to link a product becomes
// its primary. Products with an existing primary (this or another card) are left untouched.
func (s *Store) AssignPrimaryTechCardIfUnset(ctx context.Context, techCardID int, productIDs []int) error {
	if len(productIDs) == 0 {
		return nil
	}
	return storeutil.ExecNamed(ctx, s.DB,
		`UPDATE product SET primary_tech_card_id = :tc WHERE id IN (:ids) AND primary_tech_card_id IS NULL`,
		map[string]any{"tc": techCardID, "ids": productIDs})
}

// SeedProductsCostPriceFromTechCard writes cost (base currency) as the tech-card-sourced cost
// of every product whose PRIMARY card is techCardID and whose cost is not manually set, and
// which the card currently links. It never overwrites a manual cost. Returns the number of
// products updated. Used by the best-effort seed on tech-card save.
func (s *Store) SeedProductsCostPriceFromTechCard(ctx context.Context, techCardID int, cost decimal.Decimal) (int64, error) {
	return storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE product p
		JOIN tech_card_product tcp ON tcp.product_id = p.id AND tcp.tech_card_id = :tc
		SET p.cost_price = :cost,
			p.cost_price_source = 'tech_card',
			p.cost_price_tech_card_id = :tc,
			p.cost_price_updated_at = NOW()
		WHERE p.primary_tech_card_id = :tc
			AND (p.cost_price_source IS NULL OR p.cost_price_source = 'tech_card')`,
		map[string]any{"tc": techCardID, "cost": cost})
}

// SeedProductsCostBreakdownFromTechCard writes the per-unit COGS decomposition JSON (base
// currency) onto every product whose PRIMARY card is techCardID, whose cost is not manually
// set, and which the card currently links — the SAME target set and predicate as
// SeedProductsCostPriceFromTechCard, so cost_price and cost_breakdown never drift. A NULL
// breakdown clears any stale decomposition (e.g. the cost is still base-seedable but its
// components are no longer convertible). Returns the number of products updated.
func (s *Store) SeedProductsCostBreakdownFromTechCard(ctx context.Context, techCardID int, breakdown sql.NullString) (int64, error) {
	return storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE product p
		JOIN tech_card_product tcp ON tcp.product_id = p.id AND tcp.tech_card_id = :tc
		SET p.cost_breakdown = :breakdown
		WHERE p.primary_tech_card_id = :tc
			AND (p.cost_price_source IS NULL OR p.cost_price_source = 'tech_card')`,
		map[string]any{"tc": techCardID, "breakdown": breakdown})
}

// ForceSetProductCostPriceFromTechCard writes cost (base currency) as the tech-card-sourced
// cost of a single product, overriding any manual value. Used by the explicit
// SyncProductCostFromTechCard admin action.
func (s *Store) ForceSetProductCostPriceFromTechCard(ctx context.Context, productID, techCardID int, cost decimal.Decimal) error {
	return storeutil.ExecNamed(ctx, s.DB, `
		UPDATE product
		SET cost_price = :cost,
			cost_price_source = 'tech_card',
			cost_price_tech_card_id = :tc,
			cost_price_updated_at = NOW()
		WHERE id = :id`,
		map[string]any{"id": productID, "tc": techCardID, "cost": cost})
}

// SetPrimaryTechCard repoints a product's authoritative-for-costing card.
func (s *Store) SetPrimaryTechCard(ctx context.Context, productID, techCardID int) error {
	return storeutil.ExecNamed(ctx, s.DB,
		`UPDATE product SET primary_tech_card_id = :tc WHERE id = :id`,
		map[string]any{"id": productID, "tc": techCardID})
}

// SetProductCustoms sets a product's international-shipping customs data (HS code + declared
// description). Empty values clear the corresponding column (stored NULL). country_of_origin is NOT
// written here: it is a required core product field (set via the product form) that customs reuses
// as the origin_country — writing it from the customs path would fight the product form and could
// blank a NOT NULL column.
func (s *Store) SetProductCustoms(ctx context.Context, productID int, customs entity.ColorwayCustoms) error {
	return storeutil.ExecNamed(ctx, s.DB,
		`UPDATE product SET hs_code = :hs, customs_description = :descr WHERE id = :id`,
		map[string]any{
			"id":    productID,
			"hs":    customs.HSCode,
			"descr": customs.CustomsDescription,
		})
}

// GetProductCustoms returns a product's customs data. country_of_origin is the existing core product
// field (free-text manufacture country), returned read-only for display. Returns sql.ErrNoRows if
// the product does not exist.
func (s *Store) GetProductCustoms(ctx context.Context, productID int) (*entity.ColorwayCustoms, error) {
	c, err := storeutil.QueryNamedOne[entity.ColorwayCustoms](ctx, s.DB,
		`SELECT hs_code, country_of_origin, customs_description FROM product WHERE id = :id`,
		map[string]any{"id": productID})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetProductCostInfo returns the confidential COGS/provenance fields of a product (admin
// surface only). Returns sql.ErrNoRows if the product does not exist.
func (s *Store) GetProductCostInfo(ctx context.Context, id int) (*entity.ColorwayCostInfo, error) {
	ci, err := storeutil.QueryNamedOne[entity.ColorwayCostInfo](ctx, s.DB,
		`SELECT cost_price, cost_price_source, cost_price_tech_card_id, cost_price_updated_at, primary_tech_card_id
		 FROM product WHERE id = :id`,
		map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

// IsProductLinkedToTechCard reports whether the given product is currently linked to the card.
func (s *Store) IsProductLinkedToTechCard(ctx context.Context, productID, techCardID int) (bool, error) {
	rows, err := storeutil.QueryListNamed[struct {
		N int `db:"n"`
	}](ctx, s.DB,
		`SELECT 1 AS n FROM tech_card_product WHERE product_id = :pid AND tech_card_id = :tc LIMIT 1`,
		map[string]any{"pid": productID, "tc": techCardID})
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func validateRequiredCurrencies(prices []entity.ColorwayPriceInsert) error {
	providedCurrencies := make(map[string]bool)
	var zeroPriceCurrencies []string
	var belowMinCurrencies []string
	for _, price := range prices {
		currency := strings.ToUpper(price.Currency)
		providedCurrencies[currency] = true

		if price.Price.LessThanOrEqual(decimal.Zero) {
			zeroPriceCurrencies = append(zeroPriceCurrencies, currency)
			continue
		}

		rounded := dto.RoundForCurrency(price.Price, currency)
		if err := dto.ValidatePriceMeetsMinimum(rounded, currency); err != nil {
			belowMinCurrencies = append(belowMinCurrencies, err.Error())
		}
	}

	if missingCurrencies := currency.MissingRequired(providedCurrencies); len(missingCurrencies) > 0 {
		return fmt.Errorf("missing required currencies: %s", strings.Join(missingCurrencies, ", "))
	}

	if len(zeroPriceCurrencies) > 0 {
		return fmt.Errorf("prices must be greater than zero for currencies: %s", strings.Join(zeroPriceCurrencies, ", "))
	}

	if len(belowMinCurrencies) > 0 {
		return fmt.Errorf("prices below currency minimum: %s", strings.Join(belowMinCurrencies, "; "))
	}

	return nil
}

func insertProductPrices(ctx context.Context, db dependency.DB, productId int, prices []entity.ColorwayPriceInsert) error {
	if len(prices) == 0 {
		return nil
	}

	rows := make([]map[string]any, 0, len(prices))
	for _, p := range prices {
		row := map[string]any{
			"product_id": productId,
			"currency":   p.Currency,
			"price":      dto.RoundForCurrency(p.Price, p.Currency),
		}
		rows = append(rows, row)
	}

	return storeutil.BulkInsert(ctx, db, "product_price", rows)
}

func deleteProductPrices(ctx context.Context, db dependency.DB, productId int) error {
	query := "DELETE FROM product_price WHERE product_id = :productId"
	return storeutil.ExecNamed(ctx, db, query, map[string]any{
		"productId": productId,
	})
}

func upsertProductPrices(ctx context.Context, db dependency.DB, productId int, prices []entity.ColorwayPriceInsert) error {
	if err := deleteProductPrices(ctx, db, productId); err != nil {
		return fmt.Errorf("failed to delete existing prices: %w", err)
	}
	return insertProductPrices(ctx, db, productId, prices)
}

func updateProductMedia(ctx context.Context, db dependency.DB, productId int, mediaIds []int) error {
	query := "DELETE FROM product_media WHERE product_id = :productId"
	err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"productId": productId,
	})
	if err != nil {
		return fmt.Errorf("can't delete product media: %w", err)
	}

	err = insertMedia(ctx, db, mediaIds, productId)
	if err != nil {
		return fmt.Errorf("can't insert media: %w", err)
	}
	return nil
}

func updateProductTags(ctx context.Context, db dependency.DB, productId int, tagsInsert []entity.ColorwayTagInsert) error {
	query := "DELETE FROM product_tag WHERE product_id = :productId"
	err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"productId": productId,
	})
	if err != nil {
		return fmt.Errorf("can't delete product tags: %w", err)
	}

	tags := make([]string, 0)
	for _, ti := range tagsInsert {
		tags = append(tags, ti.Tag)
	}

	rows := make([]map[string]any, 0, len(tags))
	for _, tag := range tags {
		row := map[string]any{
			"product_id": productId,
			"tag":        tag,
		}
		rows = append(rows, row)
	}
	err = storeutil.BulkInsert(ctx, db, "product_tag", rows)
	if err != nil {
		return fmt.Errorf("can't insert product tags: %w", err)
	}
	return nil
}

// productQueryResult represents the flat database result that needs to be mapped to the nested Product structure.
type productQueryResult struct {
	Id        int          `db:"id"`
	CreatedAt time.Time    `db:"created_at"`
	UpdatedAt time.Time    `db:"updated_at"`
	DeletedAt sql.NullTime `db:"deleted_at"`
	Slug      string       `db:"slug"`

	Preorder           sql.NullTime        `db:"preorder"`
	Brand              string              `db:"brand"`
	SKU                string              `db:"sku"`
	Color              string              `db:"color"`
	ColorCode          string              `db:"color_code"`
	ColorHexOverride   sql.NullString      `db:"color_hex"`
	CountryOfOrigin    string              `db:"country_of_origin"`
	SalePercentage     decimal.NullDecimal `db:"sale_percentage"`
	TopCategoryId      int                 `db:"top_category_id"`
	SubCategoryId      sql.NullInt32       `db:"sub_category_id"`
	TypeId             sql.NullInt32       `db:"type_id"`
	ModelWearsHeightCm sql.NullInt32       `db:"model_wears_height_cm"`
	ModelWearsSizeId   sql.NullInt32       `db:"model_wears_size_id"`
	CareInstructions   sql.NullString      `db:"care_instructions"`
	Composition        sql.NullString      `db:"composition"`
	Hidden             sql.NullBool        `db:"hidden"`
	TargetGender       entity.GenderEnum   `db:"target_gender"`
	Season             entity.SeasonEnum   `db:"season"`
	Collection         string              `db:"collection"`
	Fit                sql.NullString      `db:"fit"`

	MinTier               int16 `db:"min_tier"`
	HiddenForNonQualified bool  `db:"hidden_for_non_qualified"`

	ThumbnailId                 int                   `db:"thumbnail_id"`
	SecondaryThumbnailId        sql.NullInt32         `db:"secondary_thumbnail_id"`
	SecondaryThumbnailCreatedAt sql.NullTime          `db:"secondary_thumbnail_created_at"`
	ThumbnailFullSize           string                `db:"full_size"`
	ThumbnailFullSizeW          int                   `db:"full_size_width"`
	ThumbnailFullSizeH          int                   `db:"full_size_height"`
	ThumbnailThumb              string                `db:"thumbnail"`
	ThumbnailThumbW             int                   `db:"thumbnail_width"`
	ThumbnailThumbH             int                   `db:"thumbnail_height"`
	ThumbnailCompressed         string                `db:"compressed"`
	ThumbnailCompressedW        int                   `db:"compressed_width"`
	ThumbnailCompressedH        int                   `db:"compressed_height"`
	ThumbnailBlurHash           string                `db:"blur_hash"`
	SecondaryFullSize           sql.NullString        `db:"secondary_full_size"`
	SecondaryFullSizeW          sql.NullInt32         `db:"secondary_full_size_width"`
	SecondaryFullSizeH          sql.NullInt32         `db:"secondary_full_size_height"`
	SecondaryThumb              sql.NullString        `db:"secondary_thumbnail"`
	SecondaryThumbW             sql.NullInt32         `db:"secondary_thumbnail_width"`
	SecondaryThumbH             sql.NullInt32         `db:"secondary_thumbnail_height"`
	SecondaryCompressed         sql.NullString        `db:"secondary_compressed"`
	SecondaryCompressedW        sql.NullInt32         `db:"secondary_compressed_width"`
	SecondaryCompressedH        sql.NullInt32         `db:"secondary_compressed_height"`
	SecondaryBlurHash           sql.NullString        `db:"secondary_blur_hash"`
	SoldOut                     bool                  `db:"sold_out"`
	Status                      entity.ColorwayStatus `db:"status"`
	StyleId                     int                   `db:"style_id"`
}

func (pqr *productQueryResult) toProduct(translations []entity.ColorwayTranslationInsert) entity.Colorway {
	var secondaryThumbnail *entity.MediaFull
	if pqr.SecondaryThumbnailId.Valid {
		secondaryCreatedAt := pqr.CreatedAt
		if pqr.SecondaryThumbnailCreatedAt.Valid {
			secondaryCreatedAt = pqr.SecondaryThumbnailCreatedAt.Time
		}
		secondaryThumbnail = &entity.MediaFull{
			Id:        int(pqr.SecondaryThumbnailId.Int32),
			CreatedAt: secondaryCreatedAt,
			MediaItem: entity.MediaItem{
				FullSizeMediaURL:   pqr.SecondaryFullSize.String,
				FullSizeWidth:      int(pqr.SecondaryFullSizeW.Int32),
				FullSizeHeight:     int(pqr.SecondaryFullSizeH.Int32),
				ThumbnailMediaURL:  pqr.SecondaryThumb.String,
				ThumbnailWidth:     int(pqr.SecondaryThumbW.Int32),
				ThumbnailHeight:    int(pqr.SecondaryThumbH.Int32),
				CompressedMediaURL: pqr.SecondaryCompressed.String,
				CompressedWidth:    int(pqr.SecondaryCompressedW.Int32),
				CompressedHeight:   int(pqr.SecondaryCompressedH.Int32),
				BlurHash:           pqr.SecondaryBlurHash,
			},
		}
	}

	return entity.Colorway{
		Id:        pqr.Id,
		CreatedAt: pqr.CreatedAt,
		UpdatedAt: pqr.UpdatedAt,
		DeletedAt: pqr.DeletedAt,
		Slug:      pqr.Slug,
		SKU:       pqr.SKU,
		SoldOut:   pqr.SoldOut,
		Status:    pqr.Status,
		StyleId:   pqr.StyleId,
		ProductDisplay: entity.ColorwayDisplay{
			ProductBody: entity.ColorwayBody{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Preorder:              pqr.Preorder,
					Brand:                 pqr.Brand,
					Collection:            pqr.Collection,
					Color:                 pqr.Color,
					ColorCode:             pqr.ColorCode,
					ColorHexOverride:      pqr.ColorHexOverride,
					CountryOfOrigin:       pqr.CountryOfOrigin,
					SalePercentage:        pqr.SalePercentage,
					TopCategoryId:         pqr.TopCategoryId,
					SubCategoryId:         pqr.SubCategoryId,
					TypeId:                pqr.TypeId,
					ModelWearsHeightCm:    pqr.ModelWearsHeightCm,
					ModelWearsSizeId:      pqr.ModelWearsSizeId,
					CareInstructions:      pqr.CareInstructions,
					Composition:           pqr.Composition,
					Hidden:                pqr.Hidden,
					TargetGender:          pqr.TargetGender,
					Season:                pqr.Season,
					Fit:                   pqr.Fit,
					MinTier:               pqr.MinTier,
					HiddenForNonQualified: pqr.HiddenForNonQualified,
				},
				Translations: translations,
			},
			Thumbnail: entity.MediaFull{
				Id:        pqr.ThumbnailId,
				CreatedAt: pqr.CreatedAt,
				MediaItem: entity.MediaItem{
					FullSizeMediaURL:   pqr.ThumbnailFullSize,
					FullSizeWidth:      pqr.ThumbnailFullSizeW,
					FullSizeHeight:     pqr.ThumbnailFullSizeH,
					ThumbnailMediaURL:  pqr.ThumbnailThumb,
					ThumbnailWidth:     pqr.ThumbnailThumbW,
					ThumbnailHeight:    pqr.ThumbnailThumbH,
					CompressedMediaURL: pqr.ThumbnailCompressed,
					CompressedWidth:    pqr.ThumbnailCompressedW,
					CompressedHeight:   pqr.ThumbnailCompressedH,
					BlurHash:           sql.NullString{String: pqr.ThumbnailBlurHash, Valid: pqr.ThumbnailBlurHash != ""},
				},
			},
			SecondaryThumbnail: secondaryThumbnail,
		},
	}
}

func fetchProductTranslations(ctx context.Context, db dependency.DB, productIds []int) (map[int][]entity.ColorwayTranslationInsert, error) {
	if len(productIds) == 0 {
		return map[int][]entity.ColorwayTranslationInsert{}, nil
	}

	query := `SELECT product_id, language_id, name, description FROM product_translation WHERE product_id IN (:productIds) ORDER BY product_id, language_id`

	translations, err := storeutil.QueryListNamed[entity.ColorwayTranslation](ctx, db, query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	translationMap := make(map[int][]entity.ColorwayTranslationInsert)
	for _, t := range translations {
		translationMap[t.ProductId] = append(translationMap[t.ProductId], entity.ColorwayTranslationInsert{
			LanguageId:  t.LanguageId,
			Name:        t.Name,
			Description: t.Description,
		})
	}

	return translationMap, nil
}

func fetchProductPrices(ctx context.Context, db dependency.DB, productIds []int) (map[int][]entity.ColorwayPrice, error) {
	if len(productIds) == 0 {
		return map[int][]entity.ColorwayPrice{}, nil
	}

	query := `SELECT id, product_id, currency, price, created_at, updated_at FROM product_price WHERE product_id IN (:productIds) ORDER BY product_id, currency`

	prices, err := storeutil.QueryListNamed[entity.ColorwayPrice](ctx, db, query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get product prices: %w", err)
	}

	priceMap := make(map[int][]entity.ColorwayPrice)
	for _, p := range prices {
		priceMap[p.ProductId] = append(priceMap[p.ProductId], p)
	}

	return priceMap, nil
}

func toCamelCase(s string) string {
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "_")
	titleCaser := cases.Title(language.English)
	for i, part := range parts {
		if i > 0 {
			parts[i] = titleCaser.String(part)
		} else {
			parts[i] = strings.ToLower(part)
		}
	}
	return strings.Join(parts, "")
}
