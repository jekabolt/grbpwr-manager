// Package product implements product CRUD, stock management, waitlist, and SKU generation.
package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
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
func (s *Store) AddProduct(ctx context.Context, prd *entity.ProductNew) (int, error) {
	var prdId int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		if !prd.Product.ProductBodyInsert.SalePercentage.Valid || prd.Product.ProductBodyInsert.SalePercentage.Decimal.LessThan(decimal.Zero) {
			prd.Product.ProductBodyInsert.SalePercentage = decimal.NullDecimal{
				Valid:   true,
				Decimal: decimal.NewFromFloat(0),
			}
		}
		id := time.Now().Unix()
		sku := GenerateSKU(prd.Product, int(id))
		prdId, err = insertProduct(ctx, rep.DB(), prd.Product, int(id), sku)
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
func (s *Store) UpdateProduct(ctx context.Context, prd *entity.ProductNew, id int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		err := updateProduct(ctx, rep.DB(), prd.Product, id)
		if err != nil {
			return fmt.Errorf("can't update product: %w", err)
		}

		err = insertProductTranslations(ctx, rep.DB(), id, prd.Product.Translations)
		if err != nil {
			return fmt.Errorf("can't update product translations: %w", err)
		}

		err = deleteSizeMeasurements(ctx, rep.DB(), id)
		if err != nil {
			return fmt.Errorf("can't delete product sizes: %w", err)
		}

		err = insertSizeMeasurements(ctx, rep.DB(), prd.SizeMeasurements, id)
		if err != nil {
			return fmt.Errorf("can't update product measurements: %w", err)
		}

		if err := recordStockChangeFromSizeMeasurements(ctx, rep, id, prd.SizeMeasurements, entity.StockChangeSourceManualAdjustment, entity.StockChangeReasonCorrection); err != nil {
			return fmt.Errorf("can't record stock change history: %w", err)
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
func (s *Store) GetProductByIdShowHidden(ctx context.Context, id int) (*entity.ProductFull, error) {
	return s.getProductDetails(ctx, map[string]any{"id": id}, true)
}

// GetProductByIdNoHidden returns a product by its ID, excluding hidden products.
func (s *Store) GetProductByIdNoHidden(ctx context.Context, id int) (*entity.ProductFull, error) {
	return s.getProductDetails(ctx, map[string]any{"id": id}, false)
}

func insertProduct(ctx context.Context, db dependency.DB, product *entity.ProductInsert, id int, sku string) (int, error) {
	query := `
	INSERT INTO product
	(id, sku, preorder, brand, color, color_hex, country_of_origin, thumbnail_id, secondary_thumbnail_id, sale_percentage, top_category_id, sub_category_id, type_id, model_wears_height_cm, model_wears_size_id, care_instructions, composition, hidden, target_gender, season, version, collection, fit)
	VALUES (:id, :sku, :preorder, :brand, :color, :colorHex, :countryOfOrigin, :thumbnailId, :secondaryThumbnailId, :salePercentage, :topCategoryId, :subCategoryId, :typeId, :modelWearsHeightCm, :modelWearsSizeId, :careInstructions, :composition, :hidden, :targetGender, :season, :version, :collection, :fit)`

	params := map[string]any{
		"id":                   id,
		"sku":                  sku,
		"preorder":             product.ProductBodyInsert.Preorder,
		"brand":                product.ProductBodyInsert.Brand,
		"color":                product.ProductBodyInsert.Color,
		"colorHex":             product.ProductBodyInsert.ColorHex,
		"countryOfOrigin":      product.ProductBodyInsert.CountryOfOrigin,
		"thumbnailId":          product.ThumbnailMediaID,
		"secondaryThumbnailId": product.SecondaryThumbnailMediaID,
		"salePercentage":       product.ProductBodyInsert.SalePercentage,
		"topCategoryId":        product.ProductBodyInsert.TopCategoryId,
		"subCategoryId":        product.ProductBodyInsert.SubCategoryId,
		"typeId":               product.ProductBodyInsert.TypeId,
		"modelWearsHeightCm":   product.ProductBodyInsert.ModelWearsHeightCm,
		"modelWearsSizeId":     product.ProductBodyInsert.ModelWearsSizeId,
		"hidden":               product.ProductBodyInsert.Hidden,
		"targetGender":         product.ProductBodyInsert.TargetGender,
		"season":               product.ProductBodyInsert.Season,
		"careInstructions":     product.ProductBodyInsert.CareInstructions,
		"composition":          product.ProductBodyInsert.Composition,
		"version":              product.ProductBodyInsert.Version,
		"collection":           product.ProductBodyInsert.Collection,
		"fit":                  product.ProductBodyInsert.Fit,
	}

	id, err := storeutil.ExecNamedLastId(ctx, db, query, params)
	if err != nil {
		return id, err
	}

	return id, nil
}

func insertProductTranslations(ctx context.Context, db dependency.DB, productId int, translations []entity.ProductTranslationInsert) error {
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

func deleteSizeMeasurements(ctx context.Context, db dependency.DB, productID int) error {
	query := "DELETE FROM product_size WHERE product_id = :productId"
	err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"productId": productID,
	})
	if err != nil {
		return fmt.Errorf("can't delete product sizes: %w", err)
	}

	query = "DELETE FROM size_measurement WHERE product_id = :productId"
	err = storeutil.ExecNamed(ctx, db, query, map[string]any{
		"productId": productID,
	})
	if err != nil {
		return fmt.Errorf("can't delete product measurements: %w", err)
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

	rowsPrdMeasurements := make([]map[string]any, 0)

	for _, sm := range sizeMeasurements {
		query := `INSERT INTO product_size (product_id, size_id, quantity) VALUES (:productId, :sizeId, :quantity)`
		productSizeID, err := storeutil.ExecNamedLastId(ctx, db, query, map[string]any{
			"productId": productID,
			"sizeId":    sm.ProductSize.SizeId,
			"quantity":  sm.ProductSize.QuantityDecimal(),
		})
		if err != nil {
			return fmt.Errorf("can't insert product size: %w", err)
		}

		for _, m := range sm.Measurements {
			row := map[string]any{
				"product_id":          productID,
				"product_size_id":     productSizeID,
				"measurement_name_id": m.MeasurementNameId,
				"measurement_value":   m.MeasurementValue,
			}
			rowsPrdMeasurements = append(rowsPrdMeasurements, row)
		}
	}

	if len(rowsPrdMeasurements) > 0 {
		err := storeutil.BulkInsert(ctx, db, "size_measurement", rowsPrdMeasurements)
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

func insertTags(ctx context.Context, db dependency.DB, tagsInsert []entity.ProductTagInsert, productID int) ([]entity.ProductTag, error) {
	uniqueTags := make(map[string]bool)
	uniqueTagsSlice := make([]entity.ProductTagInsert, 0)
	rows := make([]map[string]any, 0)

	tags := make([]entity.ProductTag, 0)

	for _, t := range tagsInsert {
		if _, exists := uniqueTags[t.Tag]; !exists {
			uniqueTags[t.Tag] = true
			uniqueTagsSlice = append(uniqueTagsSlice, t)
		}
	}

	for _, t := range uniqueTagsSlice {
		tags = append(tags, entity.ProductTag{
			ProductTagInsert: t,
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

func updateProduct(ctx context.Context, db dependency.DB, prd *entity.ProductInsert, id int) error {
	query := `
	UPDATE product 
	SET 
		preorder = :preorder, 
		brand = :brand, 
		color = :color, 
		color_hex = :colorHex, 
		country_of_origin = :countryOfOrigin, 
		thumbnail_id = :thumbnailId, 
		secondary_thumbnail_id = :secondaryThumbnailId,
		sale_percentage = :salePercentage,
		top_category_id = :topCategoryId, 
		sub_category_id = :subCategoryId, 
		type_id = :typeId, 
		model_wears_height_cm = :modelWearsHeightCm, 
		model_wears_size_id = :modelWearsSizeId, 
		hidden = :hidden,
		target_gender = :targetGender,
		season = :season,
		care_instructions = :careInstructions,
		composition = :composition,
		version = :version,
		collection = :collection,
		fit = :fit
	WHERE id = :id
	`
	return storeutil.ExecNamed(ctx, db, query, map[string]any{
		"preorder":             prd.ProductBodyInsert.Preorder,
		"brand":                prd.ProductBodyInsert.Brand,
		"color":                prd.ProductBodyInsert.Color,
		"colorHex":             prd.ProductBodyInsert.ColorHex,
		"countryOfOrigin":      prd.ProductBodyInsert.CountryOfOrigin,
		"thumbnailId":          prd.ThumbnailMediaID,
		"secondaryThumbnailId": prd.SecondaryThumbnailMediaID,
		"salePercentage":       prd.ProductBodyInsert.SalePercentage,
		"topCategoryId":        prd.ProductBodyInsert.TopCategoryId,
		"subCategoryId":        prd.ProductBodyInsert.SubCategoryId,
		"typeId":               prd.ProductBodyInsert.TypeId,
		"modelWearsHeightCm":   prd.ProductBodyInsert.ModelWearsHeightCm,
		"modelWearsSizeId":     prd.ProductBodyInsert.ModelWearsSizeId,
		"hidden":               prd.ProductBodyInsert.Hidden,
		"targetGender":         prd.ProductBodyInsert.TargetGender,
		"season":               prd.ProductBodyInsert.Season,
		"careInstructions":     prd.ProductBodyInsert.CareInstructions,
		"composition":          prd.ProductBodyInsert.Composition,
		"version":              prd.ProductBodyInsert.Version,
		"collection":           prd.ProductBodyInsert.Collection,
		"fit":                  prd.ProductBodyInsert.Fit,
		"id":                   id,
	})
}

func validateRequiredCurrencies(prices []entity.ProductPriceInsert) error {
	requiredCurrencies := map[string]bool{
		"EUR": true, "USD": true, "JPY": true,
		"CNY": true, "KRW": true, "GBP": true,
	}

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

	var missingCurrencies []string
	for currency := range requiredCurrencies {
		if !providedCurrencies[currency] {
			missingCurrencies = append(missingCurrencies, currency)
		}
	}

	if len(missingCurrencies) > 0 {
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

func insertProductPrices(ctx context.Context, db dependency.DB, productId int, prices []entity.ProductPriceInsert) error {
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

func upsertProductPrices(ctx context.Context, db dependency.DB, productId int, prices []entity.ProductPriceInsert) error {
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

func updateProductTags(ctx context.Context, db dependency.DB, productId int, tagsInsert []entity.ProductTagInsert) error {
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
	ColorHex           string              `db:"color_hex"`
	CountryOfOrigin    string              `db:"country_of_origin"`
	SalePercentage     decimal.NullDecimal `db:"sale_percentage"`
	TopCategoryId      int                 `db:"top_category_id"`
	SubCategoryId      sql.NullInt32       `db:"sub_category_id"`
	TypeId             sql.NullInt32       `db:"type_id"`
	ModelWearsHeightCm sql.NullInt32       `db:"model_wears_height_cm"`
	ModelWearsSizeId   sql.NullInt32       `db:"model_wears_size_id"`
	CareInstructions   sql.NullString      `db:"care_instructions"`
	Composition        sql.NullString      `db:"composition"`
	Version            string              `db:"version"`
	Hidden             sql.NullBool        `db:"hidden"`
	TargetGender       entity.GenderEnum   `db:"target_gender"`
	Season             entity.SeasonEnum   `db:"season"`
	Collection         string              `db:"collection"`
	Fit                sql.NullString      `db:"fit"`

	ThumbnailId                 int            `db:"thumbnail_id"`
	SecondaryThumbnailId        sql.NullInt32  `db:"secondary_thumbnail_id"`
	SecondaryThumbnailCreatedAt sql.NullTime   `db:"secondary_thumbnail_created_at"`
	ThumbnailFullSize           string         `db:"full_size"`
	ThumbnailFullSizeW          int            `db:"full_size_width"`
	ThumbnailFullSizeH          int            `db:"full_size_height"`
	ThumbnailThumb              string         `db:"thumbnail"`
	ThumbnailThumbW             int            `db:"thumbnail_width"`
	ThumbnailThumbH             int            `db:"thumbnail_height"`
	ThumbnailCompressed         string         `db:"compressed"`
	ThumbnailCompressedW        int            `db:"compressed_width"`
	ThumbnailCompressedH        int            `db:"compressed_height"`
	ThumbnailBlurHash           string         `db:"blur_hash"`
	SecondaryFullSize           sql.NullString `db:"secondary_full_size"`
	SecondaryFullSizeW          sql.NullInt32  `db:"secondary_full_size_width"`
	SecondaryFullSizeH          sql.NullInt32  `db:"secondary_full_size_height"`
	SecondaryThumb              sql.NullString `db:"secondary_thumbnail"`
	SecondaryThumbW             sql.NullInt32  `db:"secondary_thumbnail_width"`
	SecondaryThumbH             sql.NullInt32  `db:"secondary_thumbnail_height"`
	SecondaryCompressed         sql.NullString `db:"secondary_compressed"`
	SecondaryCompressedW        sql.NullInt32  `db:"secondary_compressed_width"`
	SecondaryCompressedH        sql.NullInt32  `db:"secondary_compressed_height"`
	SecondaryBlurHash           sql.NullString `db:"secondary_blur_hash"`
	SoldOut                     bool           `db:"sold_out"`
}

func (pqr *productQueryResult) toProduct(translations []entity.ProductTranslationInsert) entity.Product {
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

	return entity.Product{
		Id:        pqr.Id,
		CreatedAt: pqr.CreatedAt,
		UpdatedAt: pqr.UpdatedAt,
		DeletedAt: pqr.DeletedAt,
		Slug:      pqr.Slug,
		SKU:       pqr.SKU,
		SoldOut:   pqr.SoldOut,
		ProductDisplay: entity.ProductDisplay{
			ProductBody: entity.ProductBody{
				ProductBodyInsert: entity.ProductBodyInsert{
					Preorder:           pqr.Preorder,
					Brand:              pqr.Brand,
					Collection:         pqr.Collection,
					Color:              pqr.Color,
					ColorHex:           pqr.ColorHex,
					CountryOfOrigin:    pqr.CountryOfOrigin,
					SalePercentage:     pqr.SalePercentage,
					TopCategoryId:      pqr.TopCategoryId,
					SubCategoryId:      pqr.SubCategoryId,
					TypeId:             pqr.TypeId,
					ModelWearsHeightCm: pqr.ModelWearsHeightCm,
					ModelWearsSizeId:   pqr.ModelWearsSizeId,
					CareInstructions:   pqr.CareInstructions,
					Composition:        pqr.Composition,
					Version:            pqr.Version,
					Hidden:             pqr.Hidden,
					TargetGender:       pqr.TargetGender,
					Season:             pqr.Season,
					Fit:                pqr.Fit,
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

func fetchProductTranslations(ctx context.Context, db dependency.DB, productIds []int) (map[int][]entity.ProductTranslationInsert, error) {
	if len(productIds) == 0 {
		return map[int][]entity.ProductTranslationInsert{}, nil
	}

	query := `SELECT product_id, language_id, name, description FROM product_translation WHERE product_id IN (:productIds) ORDER BY product_id, language_id`

	translations, err := storeutil.QueryListNamed[entity.ProductTranslation](ctx, db, query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	translationMap := make(map[int][]entity.ProductTranslationInsert)
	for _, t := range translations {
		translationMap[t.ProductId] = append(translationMap[t.ProductId], entity.ProductTranslationInsert{
			LanguageId:  t.LanguageId,
			Name:        t.Name,
			Description: t.Description,
		})
	}

	return translationMap, nil
}

func fetchProductPrices(ctx context.Context, db dependency.DB, productIds []int) (map[int][]entity.ProductPrice, error) {
	if len(productIds) == 0 {
		return map[int][]entity.ProductPrice{}, nil
	}

	query := `SELECT id, product_id, currency, price, created_at, updated_at FROM product_price WHERE product_id IN (:productIds) ORDER BY product_id, currency`

	prices, err := storeutil.QueryListNamed[entity.ProductPrice](ctx, db, query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get product prices: %w", err)
	}

	priceMap := make(map[int][]entity.ProductPrice)
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
