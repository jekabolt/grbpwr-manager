package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type productStore struct {
	*MYSQLStore
}

// Products returns an object implementing product interface
func (ms *MYSQLStore) Products() dependency.Products {
	return &productStore{
		MYSQLStore: ms,
	}
}

func insertProduct(ctx context.Context, rep dependency.Repository, product *entity.ProductInsert, id int) (int, error) {
	query := `
	INSERT INTO product 
	(id, preorder, brand, color, color_hex, country_of_origin, thumbnail_id, price, sale_percentage, top_category_id, sub_category_id, type_id, model_wears_height_cm, model_wears_size_id, care_instructions, composition, hidden, target_gender, version, collection)
	VALUES (:id, :preorder, :brand, :color, :colorHex, :countryOfOrigin, :thumbnailId, :price, :salePercentage, :topCategoryId, :subCategoryId, :typeId, :modelWearsHeightCm, :modelWearsSizeId, :careInstructions, :composition, :hidden, :targetGender, :version, :collection)`

	params := map[string]any{
		"id":                 id,
		"preorder":           product.ProductBodyInsert.Preorder,
		"brand":              product.ProductBodyInsert.Brand,
		"color":              product.ProductBodyInsert.Color,
		"colorHex":           product.ProductBodyInsert.ColorHex,
		"countryOfOrigin":    product.ProductBodyInsert.CountryOfOrigin,
		"thumbnailId":        product.ThumbnailMediaID,
		"price":              product.ProductBodyInsert.Price,
		"salePercentage":     product.ProductBodyInsert.SalePercentage,
		"topCategoryId":      product.ProductBodyInsert.TopCategoryId,
		"subCategoryId":      product.ProductBodyInsert.SubCategoryId,
		"typeId":             product.ProductBodyInsert.TypeId,
		"modelWearsHeightCm": product.ProductBodyInsert.ModelWearsHeightCm,
		"modelWearsSizeId":   product.ProductBodyInsert.ModelWearsSizeId,
		"hidden":             product.ProductBodyInsert.Hidden,
		"targetGender":       product.ProductBodyInsert.TargetGender,
		"careInstructions":   product.ProductBodyInsert.CareInstructions,
		"composition":        product.ProductBodyInsert.Composition,
		"version":            product.ProductBodyInsert.Version,
		"collection":         product.ProductBodyInsert.Collection,
	}

	// slog.Default().Error("insertProduct", slog.Any("query", query), slog.Any("params", params))

	id, err := ExecNamedLastId(ctx, rep.DB(), query, params)
	if err != nil {
		return id, err
	}

	return id, nil
}

func insertProductTranslations(ctx context.Context, rep dependency.Repository, productId int, translations []entity.ProductTranslationInsert) error {
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

	// Delete existing translations first
	deleteQuery := "DELETE FROM product_translation WHERE product_id = :productId"
	err := ExecNamed(ctx, rep.DB(), deleteQuery, map[string]any{
		"productId": productId,
	})
	if err != nil {
		return fmt.Errorf("failed to delete existing translations: %w", err)
	}

	// Insert new translations
	return BulkInsert(ctx, rep.DB(), "product_translation", rows)
}

func deleteSizeMeasurements(ctx context.Context, rep dependency.Repository, productID int) error {
	query := "DELETE FROM product_size WHERE product_id = :productId"
	err := ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"productId": productID,
	})
	if err != nil {
		return fmt.Errorf("can't delete product sizes: %w", err)
	}

	query = "DELETE FROM size_measurement WHERE product_id = :productId"
	err = ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"productId": productID,
	})
	if err != nil {
		return fmt.Errorf("can't delete product measurements: %w", err)
	}
	return nil
}

func insertSizeMeasurements(ctx context.Context, rep dependency.Repository, sizeMeasurements []entity.SizeWithMeasurementInsert, productID int) error {
	// Validate measurement_name_id values are not zero
	for _, sm := range sizeMeasurements {
		for _, m := range sm.Measurements {
			if m.MeasurementNameId == 0 {
				return fmt.Errorf("invalid measurement_name_id: cannot be 0")
			}
		}
	}

	// Insert product sizes one by one to get their IDs, then collect measurements
	rowsPrdMeasurements := make([]map[string]any, 0)

	for _, sm := range sizeMeasurements {
		// Insert product size and get the generated ID
		query := `INSERT INTO product_size (product_id, size_id, quantity) VALUES (:productId, :sizeId, :quantity)`
		productSizeID, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"productId": productID,
			"sizeId":    sm.ProductSize.SizeId,
			"quantity":  sm.ProductSize.QuantityDecimal(),
		})
		if err != nil {
			return fmt.Errorf("can't insert product size: %w", err)
		}

		// Now add measurements using the correct product_size_id
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

	// Bulk insert all measurements at once
	if len(rowsPrdMeasurements) > 0 {
		err := BulkInsert(ctx, rep.DB(), "size_measurement", rowsPrdMeasurements)
		if err != nil {
			return fmt.Errorf("can't insert product measurements: %w", err)
		}
	}

	return nil
}

func insertMedia(ctx context.Context, rep dependency.Repository, mediaIds []int, productID int) error {
	rows := make([]map[string]any, 0, len(mediaIds))
	for _, mId := range mediaIds {
		row := map[string]any{
			"product_id": productID,
			"media_id":   mId,
		}
		rows = append(rows, row)
	}
	return BulkInsert(ctx, rep.DB(), "product_media", rows)
}

func insertTags(ctx context.Context, rep dependency.Repository, tagsInsert []entity.ProductTagInsert, productID int) ([]entity.ProductTag, error) {
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

	return tags, BulkInsert(ctx, rep.DB(), "product_tag", rows)
}

// AddProduct adds a new product to the product store.
func (ms *MYSQLStore) AddProduct(ctx context.Context, prd *entity.ProductNew) (int, error) {
	var prdId int
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		if !prd.Product.ProductBodyInsert.SalePercentage.Valid || prd.Product.ProductBodyInsert.SalePercentage.Decimal.LessThan(decimal.Zero) {
			prd.Product.ProductBodyInsert.SalePercentage = decimal.NullDecimal{
				Valid:   true,
				Decimal: decimal.NewFromFloat(0),
			}
		}
		// generate unique id
		id := time.Now().Unix()
		prdId, err = insertProduct(ctx, rep, prd.Product, int(id))
		if err != nil {
			return fmt.Errorf("can't insert product: %w", err)
		}

		// Insert translations
		err = insertProductTranslations(ctx, rep, prdId, prd.Product.Translations)
		if err != nil {
			return fmt.Errorf("can't insert product translations: %w", err)
		}

		err = insertSizeMeasurements(ctx, rep, prd.SizeMeasurements, prdId)
		if err != nil {
			return fmt.Errorf("can't insert size measurements: %w", err)
		}

		err = insertMedia(ctx, rep, prd.MediaIds, prdId)
		if err != nil {
			return fmt.Errorf("can't insert media: %w", err)
		}
		_, err = insertTags(ctx, rep, prd.Tags, prdId)
		if err != nil {
			return fmt.Errorf("can't insert tags: %w", err)
		}

		return nil
	})
	if err != nil {
		return prdId, fmt.Errorf("can't add product: %w", err)
	}

	return prdId, nil
}

func (ms *MYSQLStore) UpdateProduct(ctx context.Context, prd *entity.ProductNew, id int) error {

	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// product
		// slog.Default().DebugContext(ctx, "product", slog.Any("product", prd.Product.Preorder))
		err := updateProduct(ctx, rep, prd.Product, id)
		if err != nil {
			return fmt.Errorf("can't update product: %w", err)
		}

		// Update translations
		err = insertProductTranslations(ctx, rep, id, prd.Product.Translations)
		if err != nil {
			return fmt.Errorf("can't update product translations: %w", err)
		}

		// measurements
		err = deleteSizeMeasurements(ctx, rep, id)
		if err != nil {
			return fmt.Errorf("can't delete product sizes: %w", err)
		}

		err = insertSizeMeasurements(ctx, rep, prd.SizeMeasurements, id)
		if err != nil {
			return fmt.Errorf("can't update product measurements: %w", err)
		}

		// media
		err = updateProductMedia(ctx, rep, id, prd.MediaIds)
		if err != nil {
			return fmt.Errorf("can't update product media: %w", err)
		}

		// tags
		err = updateProductTags(ctx, rep, id, prd.Tags)
		if err != nil {
			return fmt.Errorf("can't update product tags: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("can't add product: %w", err)
	}

	return nil
}

func updateProduct(ctx context.Context, rep dependency.Repository, prd *entity.ProductInsert, id int) error {
	query := `
	UPDATE product 
	SET 
		preorder = :preorder, 
		brand = :brand, 
		sku = :sku, 
		color = :color, 
		color_hex = :colorHex, 
		country_of_origin = :countryOfOrigin, 
		thumbnail_id = :thumbnailId, 
		price = :price, 
		sale_percentage = :salePercentage,
		top_category_id = :topCategoryId, 
		sub_category_id = :subCategoryId, 
		type_id = :typeId, 
		model_wears_height_cm = :modelWearsHeightCm, 
		model_wears_size_id = :modelWearsSizeId, 
		hidden = :hidden,
		target_gender = :targetGender,
		care_instructions = :careInstructions,
		composition = :composition
	WHERE id = :id
	`
	return ExecNamed(ctx, rep.DB(), query, map[string]any{
		"preorder":           prd.ProductBodyInsert.Preorder,
		"brand":              prd.ProductBodyInsert.Brand,
		"color":              prd.ProductBodyInsert.Color,
		"colorHex":           prd.ProductBodyInsert.ColorHex,
		"countryOfOrigin":    prd.ProductBodyInsert.CountryOfOrigin,
		"thumbnailId":        prd.ThumbnailMediaID,
		"price":              prd.ProductBodyInsert.Price,
		"salePercentage":     prd.ProductBodyInsert.SalePercentage,
		"topCategoryId":      prd.ProductBodyInsert.TopCategoryId,
		"subCategoryId":      prd.ProductBodyInsert.SubCategoryId,
		"typeId":             prd.ProductBodyInsert.TypeId,
		"modelWearsHeightCm": prd.ProductBodyInsert.ModelWearsHeightCm,
		"modelWearsSizeId":   prd.ProductBodyInsert.ModelWearsSizeId,
		"hidden":             prd.ProductBodyInsert.Hidden,
		"targetGender":       prd.ProductBodyInsert.TargetGender,
		"careInstructions":   prd.ProductBodyInsert.CareInstructions,
		"composition":        prd.ProductBodyInsert.Composition,
		"collection":         prd.ProductBodyInsert.Collection,
		"id":                 id,
	})
}

// GetProductsPaged
// Parameters:
//   - languageId: The language ID for translations
//   - limit: The maximum number of products per page.
//   - offset: The starting offset for retrieving products.
//   - sortFactors: Sorting factors
//   - orderFactor: Order factor
//   - filterConditions: Filtering conditions.
//   - showHidden: Show hidden products
//
// filterConditions possible takes:
//   - price from-to
//   - on sale
//   - color
//   - category
//   - sizes available
//   - preorder
//   - by tags
//
// GetProductsPaged rewritten to use go-namedParameterQuery
func (ms *MYSQLStore) GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, showHidden bool) ([]entity.Product, int, error) {
	// Validate sort factors
	if len(sortFactors) > 0 {
		for _, sf := range sortFactors {
			if !entity.IsValidSortFactor(string(sf)) {
				return nil, 0, fmt.Errorf("invalid sort factor: %s", sf)
			}
		}
	}

	var whereClauses []string
	args := make(map[string]interface{})

	// Handle hidden products
	if !showHidden {
		whereClauses = append(whereClauses, "p.hidden = :isHidden")
		args["isHidden"] = 0
	}

	// Handle price filtering
	if filterConditions != nil {
		if filterConditions.From.LessThan(decimal.Zero) {
			return nil, 0, fmt.Errorf("price range cannot be negative")
		}
		if filterConditions.From.GreaterThan(filterConditions.To) && !filterConditions.To.Equals(decimal.Zero) {
			return nil, 0, fmt.Errorf("invalid price range: from cannot be greater than to unless to is unset")
		}

		switch {
		case filterConditions.From.IsZero() && filterConditions.To.GreaterThan(decimal.Zero):
			// Case 1: from = nil & to = x > 0
			whereClauses = append(whereClauses, "p.price * (1 - COALESCE(p.sale_percentage, 0) / 100) BETWEEN 0 AND :priceTo")
			args["priceTo"] = filterConditions.To
		case filterConditions.From.GreaterThan(decimal.Zero) && filterConditions.To.IsZero():
			// Case 2: from = x > 0 & to = nil
			whereClauses = append(whereClauses, "p.price * (1 - COALESCE(p.sale_percentage, 0) / 100) >= :priceFrom")
			args["priceFrom"] = filterConditions.From
		case filterConditions.From.IsZero() && filterConditions.To.IsZero():
			// Case 3: from = nil & to = nil
			// No additional filtering needed
		case filterConditions.From.GreaterThan(decimal.Zero) && filterConditions.To.GreaterThan(decimal.Zero):
			// Case 4: from > 0 & to > 0
			whereClauses = append(whereClauses, "p.price * (1 - COALESCE(p.sale_percentage, 0) / 100) BETWEEN :priceFrom AND :priceTo")
			args["priceFrom"] = filterConditions.From
			args["priceTo"] = filterConditions.To
		}
	}

	// Additional filters
	if filterConditions != nil {
		if filterConditions.OnSale {
			whereClauses = append(whereClauses, "p.sale_percentage > 0")
		}
		if len(filterConditions.Gender) != 0 {
			whereClauses = append(whereClauses, "p.target_gender IN (:targetGenders)")
			genders := make([]string, len(filterConditions.Gender))
			for i, g := range filterConditions.Gender {
				genders[i] = string(g)
			}
			args["targetGenders"] = genders
		}
		if filterConditions.Color != "" {
			whereClauses = append(whereClauses, "p.color = :color")
			args["color"] = filterConditions.Color
		}
		if len(filterConditions.TopCategoryIds) != 0 {
			whereClauses = append(whereClauses, "p.top_category_id IN (:topCategoryIds)")
			args["topCategoryIds"] = filterConditions.TopCategoryIds
		}
		if len(filterConditions.SubCategoryIds) != 0 {
			whereClauses = append(whereClauses, "p.sub_category_id IN (:subCategoryIds)")
			args["subCategoryIds"] = filterConditions.SubCategoryIds
		}
		if len(filterConditions.TypeIds) != 0 {
			whereClauses = append(whereClauses, "p.type_id IN (:typeIds)")
			args["typeIds"] = filterConditions.TypeIds
		}
		if len(filterConditions.SizesIds) > 0 {
			whereClauses = append(whereClauses, "p.id IN (SELECT ps.product_id FROM product_size ps WHERE ps.size_id IN (:sizes))")
			args["sizes"] = filterConditions.SizesIds
		}
		if filterConditions.Preorder {
			whereClauses = append(whereClauses, "p.preorder IS NOT NULL AND p.preorder <> ''")
		}
		if filterConditions.ByTag != "" {
			whereClauses = append(whereClauses, "p.id IN (SELECT pt.product_id FROM product_tag pt WHERE pt.tag = :tag)")
			args["tag"] = filterConditions.ByTag
		}
	}

	// Build and execute the queries
	listQuery, countQuery := buildQuery(sortFactors, orderFactor, whereClauses, limit, offset)
	count, err := QueryCountNamed(ctx, ms.db, countQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get product count: %w", err)
	}

	// Set limit and offset
	args["limit"] = limit
	args["offset"] = offset

	// Fetch products using intermediate struct
	prdResults, err := QueryListNamed[productQueryResult](ctx, ms.db, listQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get products: %w", err)
	}

	// Extract product IDs to fetch translations
	productIds := make([]int, 0, len(prdResults))
	for _, prdResult := range prdResults {
		productIds = append(productIds, prdResult.Id)
	}

	// Fetch all translations for these products
	translationMap, err := fetchProductTranslations(ctx, ms.db, productIds)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get product translations: %w", err)
	}

	// Convert to final Product structs
	prds := make([]entity.Product, 0, len(prdResults))
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id] // Get translations for this product
		product := prdResult.toProduct(translations)
		prds = append(prds, product)
	}

	return prds, count, nil
}

func (ms *MYSQLStore) GetProductsByIds(ctx context.Context, ids []int) ([]entity.Product, error) {
	if len(ids) == 0 {
		return []entity.Product{}, nil
	}

	query := `
	SELECT 
		p.id,
		p.created_at,
		p.updated_at,
		p.preorder,
		p.brand,
		p.sku,
		p.color,
		p.color_hex,
		p.country_of_origin,
		p.price,
		p.sale_percentage,
		p.top_category_id,
		p.sub_category_id,
		p.type_id,
		p.model_wears_height_cm,
		p.model_wears_size_id,
		p.hidden,
		p.target_gender,
		p.care_instructions,
		p.composition,
		p.thumbnail_id,
		p.version,
		p.collection,
		m.full_size,
		m.full_size_width,
		m.full_size_height,
		m.thumbnail,
		m.thumbnail_width,
		m.thumbnail_height,
		m.compressed,
		m.compressed_width,
		m.compressed_height,
		m.blur_hash
	FROM 
		product p
	JOIN
		media m ON p.thumbnail_id = m.id 
	WHERE p.id IN (:ids) AND p.hidden = 0`

	prdResults, err := QueryListNamed[productQueryResult](ctx, ms.db, query, map[string]any{
		"ids": ids,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	// Fetch all translations for these products
	translationMap, err := fetchProductTranslations(ctx, ms.db, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	// Convert to final Product structs and create a map for quick lookup
	prdMap := make(map[int]entity.Product)
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		prdMap[product.Id] = product
	}

	// Create the result slice in the order of input ids
	result := make([]entity.Product, 0, len(ids))
	for _, id := range ids {
		if p, ok := prdMap[id]; ok {
			result = append(result, p)
		}
	}

	return result, nil
}

func (ms *MYSQLStore) GetProductsByTag(ctx context.Context, tag string) ([]entity.Product, error) {
	if tag == "" {
		return []entity.Product{}, nil
	}

	query := `
	SELECT 
		p.id,
		p.created_at,
		p.updated_at,
		p.preorder,
		p.brand,
		p.sku,
		p.color,
		p.color_hex,
		p.country_of_origin,
		p.price,
		p.sale_percentage,
		p.top_category_id,
		p.sub_category_id,
		p.type_id,
		p.model_wears_height_cm,
		p.model_wears_size_id,
		p.hidden,
		p.target_gender,
		p.care_instructions,
		p.composition,
		p.thumbnail_id,
		p.version,
		p.collection,
		m.full_size,
		m.full_size_width,
		m.full_size_height,
		m.thumbnail,
		m.thumbnail_width,
		m.thumbnail_height,
		m.compressed,
		m.compressed_width,
		m.compressed_height,
		m.blur_hash
	FROM 
		product p
	JOIN 
		media m ON p.thumbnail_id = m.id 
	WHERE p.id IN (SELECT ptag.product_id FROM product_tag ptag WHERE ptag.tag = :tag) AND p.hidden = 0`

	prdResults, err := QueryListNamed[productQueryResult](ctx, ms.db, query, map[string]any{
		"tag": tag,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get products by tag: %w", err)
	}

	// Extract product IDs to fetch translations
	productIds := make([]int, 0, len(prdResults))
	for _, prdResult := range prdResults {
		productIds = append(productIds, prdResult.Id)
	}

	// Fetch all translations for these products
	translationMap, err := fetchProductTranslations(ctx, ms.db, productIds)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	// Convert to final Product structs
	prds := make([]entity.Product, 0, len(prdResults))
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		prds = append(prds, product)
	}

	return prds, nil
}

// buildQuery refactored to use named parameters and to include limit and offset
// productQueryResult represents the flat database result that needs to be mapped to the nested Product structure
type productQueryResult struct {
	// Basic product fields
	Id        int       `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	Slug      string    `db:"slug"`

	// Product body fields (from product table)
	Preorder           sql.NullTime        `db:"preorder"`
	Brand              string              `db:"brand"`
	SKU                string              `db:"sku"`
	Color              string              `db:"color"`
	ColorHex           string              `db:"color_hex"`
	CountryOfOrigin    string              `db:"country_of_origin"`
	Price              decimal.Decimal     `db:"price"`
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
	Collection         string              `db:"collection"`

	// Translation fields - these will be populated separately
	// Name        string `db:"name"`
	// Description string `db:"description"`

	// Thumbnail media fields
	ThumbnailId          int    `db:"thumbnail_id"`
	ThumbnailFullSize    string `db:"full_size"`
	ThumbnailFullSizeW   int    `db:"full_size_width"`
	ThumbnailFullSizeH   int    `db:"full_size_height"`
	ThumbnailThumb       string `db:"thumbnail"`
	ThumbnailThumbW      int    `db:"thumbnail_width"`
	ThumbnailThumbH      int    `db:"thumbnail_height"`
	ThumbnailCompressed  string `db:"compressed"`
	ThumbnailCompressedW int    `db:"compressed_width"`
	ThumbnailCompressedH int    `db:"compressed_height"`
	ThumbnailBlurHash    string `db:"blur_hash"`
}

// toProduct converts the flat database result to the nested Product structure
// Translations will be populated separately
func (pqr *productQueryResult) toProduct(translations []entity.ProductTranslationInsert) entity.Product {
	return entity.Product{
		Id:        pqr.Id,
		CreatedAt: pqr.CreatedAt,
		UpdatedAt: pqr.UpdatedAt,
		Slug:      pqr.Slug,
		SKU:       pqr.SKU,
		ProductDisplay: entity.ProductDisplay{
			ProductBody: entity.ProductBody{
				ProductBodyInsert: entity.ProductBodyInsert{
					Preorder:           pqr.Preorder,
					Brand:              pqr.Brand,
					Collection:         pqr.Collection,
					Color:              pqr.Color,
					ColorHex:           pqr.ColorHex,
					CountryOfOrigin:    pqr.CountryOfOrigin,
					Price:              pqr.Price,
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
				},
				Translations: translations,
			},
			Thumbnail: entity.MediaFull{
				Id:        pqr.ThumbnailId,
				CreatedAt: pqr.CreatedAt, // Using product created_at as fallback
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
		},
	}
}

// fetchProductTranslations fetches all translations for given product IDs
func fetchProductTranslations(ctx context.Context, db dependency.DB, productIds []int) (map[int][]entity.ProductTranslationInsert, error) {
	if len(productIds) == 0 {
		return map[int][]entity.ProductTranslationInsert{}, nil
	}

	query := `SELECT product_id, language_id, name, description FROM product_translation WHERE product_id IN (:productIds) ORDER BY product_id, language_id`

	translations, err := QueryListNamed[entity.ProductTranslation](ctx, db, query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	// Group translations by product ID
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

func buildQuery(sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, whereClauses []string, limit int, offset int) (string, string) {
	baseQuery := `
	SELECT 
		p.id,
		p.created_at,
		p.updated_at,
		p.preorder,
		p.brand,
		p.sku,
		p.color,
		p.color_hex,
		p.country_of_origin,
		p.price,
		p.sale_percentage,
		p.top_category_id,
		p.sub_category_id,
		p.type_id,
		p.model_wears_height_cm,
		p.model_wears_size_id,
		p.hidden,
		p.target_gender,
		p.care_instructions,
		p.composition,
		p.thumbnail_id,
		p.version,
		p.collection,
		m.full_size,
		m.full_size_width,
		m.full_size_height,
		m.thumbnail,
		m.thumbnail_width,
		m.thumbnail_height,
		m.compressed,
		m.compressed_width,
		m.compressed_height,
		m.blur_hash
	FROM 
		product p
	JOIN 
		media m ON p.thumbnail_id = m.id`

	countQuery := `SELECT COUNT(*) FROM product p 
		JOIN media m ON p.thumbnail_id = m.id`

	// Add WHERE clause if there are conditions
	if len(whereClauses) > 0 {
		conditions := " WHERE " + strings.Join(whereClauses, " AND ")
		baseQuery += conditions
		countQuery += conditions
	}

	// Add ORDER BY clause if sorting factors are provided
	if len(sortFactors) > 0 {
		ordering := " ORDER BY " + strings.Join(entity.SortFactorsToSS(sortFactors), ", ")
		if orderFactor != "" {
			ordering += " " + string(orderFactor)
		} else {
			ordering += " " + string(entity.Ascending)
		}
		baseQuery += ordering
	}

	// Add LIMIT and OFFSET for pagination
	baseQuery += " LIMIT :limit OFFSET :offset"

	return baseQuery, countQuery
}

// GetProductByIdShowHidden returns a product by its ID, potentially including hidden products.
func (ms *MYSQLStore) GetProductByIdShowHidden(ctx context.Context, id int) (*entity.ProductFull, error) {
	return ms.getProductDetails(ctx, map[string]any{"id": id}, true)
}

// GetProductByIdNoHidden returns a product by its ID, excluding hidden products.
func (ms *MYSQLStore) GetProductByIdNoHidden(ctx context.Context, id int) (*entity.ProductFull, error) {
	filters := map[string]any{
		"id": id,
	}
	return ms.getProductDetails(ctx, filters, false)
}

// getProductDetails fetches product details based on a specific field and value.
func (ms *MYSQLStore) getProductDetails(ctx context.Context, filters map[string]any, showHidden bool) (*entity.ProductFull, error) {
	var productInfo entity.ProductFull

	whereClauses := []string{}
	params := map[string]interface{}{}
	for key, value := range filters {
		keyCamel := toCamelCase(key)
		whereClause := fmt.Sprintf("p.%s = :%s", keyCamel, keyCamel) // Corrected column reference
		whereClauses = append(whereClauses, whereClause)
		params[keyCamel] = value
	}

	query := fmt.Sprintf(`
	SELECT 
		p.id,
		p.created_at,
		p.updated_at,
		p.preorder,
		p.brand,
		p.sku,
		p.color,
		p.color_hex,
		p.country_of_origin,
		p.price,
		p.sale_percentage,
		p.top_category_id,
		p.sub_category_id,
		p.type_id,
		p.model_wears_height_cm,
		p.model_wears_size_id,
		p.hidden,
		p.target_gender,
		p.care_instructions,
		p.composition,
		p.thumbnail_id,
		p.version,
		p.collection,
		m.created_at AS thumbnail_created_at, 
		m.full_size,
		m.full_size_width,
		m.full_size_height,
		m.thumbnail,
		m.thumbnail_width,
		m.thumbnail_height,
		m.compressed,
		m.compressed_width,
		m.compressed_height,
		m.blur_hash
	FROM 
		product p
	JOIN 
		media m ON p.thumbnail_id = m.id
	WHERE %s`, strings.Join(whereClauses, " AND "))

	// Include or exclude hidden products based on the showHidden flag
	if !showHidden {
		query += " AND p.hidden = false"
	}

	type productDetailsResult struct {
		productQueryResult
		ThumbnailCreatedAt time.Time `db:"thumbnail_created_at"`
	}

	prdResult, err := QueryNamedOne[productDetailsResult](ctx, ms.db, query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get product: %w", err)
	}

	// Fetch all translations for this product
	translationMap, err := fetchProductTranslations(ctx, ms.db, []int{prdResult.Id})
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	// Convert to Product struct
	translations := translationMap[prdResult.Id]
	product := prdResult.toProduct(translations)
	// Set the correct thumbnail created_at
	product.ProductDisplay.Thumbnail.CreatedAt = prdResult.ThumbnailCreatedAt

	productInfo.Product = &product

	// fetch sizes
	query = `SELECT * FROM product_size WHERE product_id = :id`

	sizes, err := QueryListNamed[entity.ProductSize](ctx, ms.db, query, map[string]any{
		"id": product.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get sizes: %w", err)
	}
	productInfo.Sizes = sizes

	// fetch measurements
	query = `SELECT * FROM size_measurement	WHERE product_id = :id`

	measurements, err := QueryListNamed[entity.ProductMeasurement](ctx, ms.db, query, map[string]any{
		"id": product.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get measurements: %w", err)
	}
	productInfo.Measurements = measurements

	// Fetch Media
	query = `
		SELECT 
			m.id,
			m.created_at,
			m.full_size,
			m.full_size_width,
			m.full_size_height,
			m.thumbnail,
			m.thumbnail_width,
			m.thumbnail_height,
			m.compressed,
			m.compressed_width,
			m.compressed_height,
			m.blur_hash
		FROM media m
		INNER JOIN product_media pm ON m.id = pm.media_id
		WHERE pm.product_id = :id
		ORDER BY pm.id;
	`
	productInfo.Media, err = QueryListNamed[entity.MediaFull](ctx, ms.db, query, map[string]interface{}{
		"id": product.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get media: %w", err)
	}

	// Fetch Tags
	query = "SELECT * FROM product_tag WHERE product_id = :id"
	productInfo.Tags, err = QueryListNamed[entity.ProductTag](ctx, ms.db, query, map[string]interface{}{
		"id": product.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get tags: %w", err)
	}

	return &productInfo, nil
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

// DeleteProductById deletes a product by its ID.
func (ms *MYSQLStore) DeleteProductById(ctx context.Context, id int) error {
	query := "DELETE FROM product WHERE id = :id"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"id": id,
	})
}

func (ms *MYSQLStore) ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert) error {
	for _, item := range items {

		query := `SELECT * FROM product_size WHERE product_id = :productId AND size_id = :sizeId`
		productSize, err := QueryNamedOne[entity.ProductSize](ctx, ms.db, query, map[string]any{
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
		})
		if err != nil {
			return fmt.Errorf("error checking current quantity: %w", err)
		}

		if productSize.QuantityDecimal().Add(item.Quantity.Neg()).LessThan(decimal.Zero) {
			return fmt.Errorf("cannot decrease available sizes: insufficient quantity for product ID: %d, size ID: %d", item.ProductId, item.SizeId)
		}

		query = `UPDATE product_size SET quantity = quantity - :quantity WHERE product_id = :productId AND size_id = :sizeId`
		err = ExecNamed(ctx, ms.db, query, map[string]any{
			"quantity":  item.QuantityDecimal(),
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
		})
		if err != nil {
			return fmt.Errorf("can't decrease available sizes: %w", err)
		}
	}
	return nil
}

func (ms *MYSQLStore) RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert) error {
	for _, item := range items {
		updateQuery := `UPDATE product_size SET quantity = quantity + :quantity WHERE product_id = :productId AND size_id = :sizeId`
		err := ExecNamed(ctx, ms.db, updateQuery, map[string]any{
			"quantity":  item.QuantityDecimal(),
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
		})
		if err != nil {
			return fmt.Errorf("can't restore product quantity for sizes: %w", err)
		}
	}
	return nil
}

func (ms *MYSQLStore) UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error {

	sz, ok := cache.GetSizeById(sizeId)
	if !ok {
		return fmt.Errorf("can't get size by id: %d", sizeId)
	}

	query := `
		INSERT INTO product_size 
			(product_id, size_id, quantity) 
		VALUES 
			(:productId, :sizeId, :quantity) 
		ON DUPLICATE KEY UPDATE quantity = :quantity
	`
	err := ExecNamed(ctx, ms.db, query, map[string]any{
		"productId": productId,
		"sizeId":    sz.Id,
		"quantity":  quantity,
	})
	if err != nil {
		return fmt.Errorf("can't insert product size: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) DeleteProductMedia(ctx context.Context, productId, mediaId int) error {
	query := "DELETE FROM product_media WHERE product_id = :productId AND media_id = :mediaId"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"productId": productId,
		"mediaId":   mediaId,
	})
}

func (ms *MYSQLStore) AddProductMedia(ctx context.Context, productId int, mediaIds []int) error {
	err := insertMedia(ctx, ms, mediaIds, productId)
	if err != nil {
		return fmt.Errorf("can't insert media: %w", err)
	}
	return nil
}

func updateProductMedia(ctx context.Context, rep dependency.Repository, productId int, mediaIds []int) error {
	query := "DELETE FROM product_media WHERE product_id = :productId"
	err := ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
		"productId": productId,
	})
	if err != nil {
		return fmt.Errorf("can't delete product media: %w", err)
	}

	err = insertMedia(ctx, rep, mediaIds, productId)
	if err != nil {
		return fmt.Errorf("can't insert media: %w", err)
	}
	return nil
}

// TODO: rm (ms *MYSQLStore) from the function signature
func updateProductTags(ctx context.Context, rep dependency.Repository, productId int, tagsInsert []entity.ProductTagInsert) error {
	query := "DELETE FROM product_tag WHERE product_id = :productId"
	err := ExecNamed(ctx, rep.DB(), query, map[string]interface{}{
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
	err = BulkInsert(ctx, rep.DB(), "product_tag", rows)
	if err != nil {
		return fmt.Errorf("can't insert product tags: %w", err)
	}
	return nil

}

// for orders

func getProductsByIds(ctx context.Context, rep dependency.Repository, productIds []int) ([]entity.Product, error) {
	if len(productIds) == 0 {
		return []entity.Product{}, nil
	}
	query := `
	SELECT 
			p.id,
			p.created_at,
			p.updated_at,
			p.preorder,
			p.brand,
			p.sku,
			p.color,
			p.color_hex,
			p.country_of_origin,
			p.price,
			p.sale_percentage,
			p.top_category_id,
			p.sub_category_id,
			p.type_id,
			p.model_wears_height_cm,
			p.model_wears_size_id,
			p.hidden,
			p.target_gender,
			p.care_instructions,
			p.composition,
			p.thumbnail_id,
			p.version,
			p.collection,
			m.created_at AS thumbnail_created_at, 
			m.full_size,
			m.full_size_width,
			m.full_size_height,
			m.thumbnail,
			m.thumbnail_width,
			m.thumbnail_height,
			m.compressed,
			m.compressed_width,
			m.compressed_height,
			m.blur_hash
		FROM 
			product p
		JOIN 
			media m ON p.thumbnail_id = m.id
	WHERE p.id IN (:productIds)`

	type productOrderResult struct {
		productQueryResult
		ThumbnailCreatedAt time.Time `db:"thumbnail_created_at"`
	}

	prdResults, err := QueryListNamed[productOrderResult](ctx, rep.DB(), query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query hero products: %w", err)
	}

	// Fetch all translations for these products
	translationMap, err := fetchProductTranslations(ctx, rep.DB(), productIds)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	products := make([]entity.Product, 0, len(prdResults))

	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		// Set the correct thumbnail created_at
		product.ProductDisplay.Thumbnail.CreatedAt = prdResult.ThumbnailCreatedAt

		products = append(products, product)
	}
	return products, nil
}

func getProductsSizesByIds(ctx context.Context, rep dependency.Repository, items []entity.OrderItemInsert) ([]entity.ProductSize, error) {
	if len(items) == 0 {
		return []entity.ProductSize{}, nil
	}

	var productSizeParams []interface{}
	productSizeQuery := "SELECT * FROM product_size WHERE "

	productSizeConditions := []string{}
	for _, item := range items {
		productSizeConditions = append(productSizeConditions, "(product_id = ? AND size_id = ?)")
		productSizeParams = append(productSizeParams, item.ProductId, item.SizeId)
	}

	productSizeQuery += strings.Join(productSizeConditions, " OR ")

	var productSizes []entity.ProductSize

	rows, err := rep.DB().QueryxContext(ctx, productSizeQuery, productSizeParams...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ps entity.ProductSize
		err := rows.StructScan(&ps)
		if err != nil {
			return nil, err
		}
		productSizes = append(productSizes, ps)
	}

	// Check for errors encountered during iteration over rows.
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return productSizes, nil
}

func getProductIdsFromItems(items []entity.OrderItemInsert) []int {
	ids := make([]int, len(items))
	for i, item := range items {
		ids[i] = item.ProductId
	}
	return ids
}
