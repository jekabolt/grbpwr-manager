package store

import (
	"context"
	"fmt"
	"log/slog"
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
	(id, preorder, name, brand, sku, color, color_hex, country_of_origin, thumbnail_id, price, sale_percentage, top_category_id, sub_category_id, type_id, model_wears_height_cm, model_wears_size_id, description, care_instructions, composition, hidden, target_gender)
	VALUES (:id, :preorder, :name, :brand, :sku, :color, :colorHex, :countryOfOrigin, :thumbnailId, :price, :salePercentage, :topCategoryId, :subCategoryId, :typeId, :modelWearsHeightCm, :modelWearsSizeId, :description, :careInstructions, :composition, :hidden, :targetGender)`

	params := map[string]any{
		"id":                 id,
		"preorder":           product.Preorder,
		"name":               product.Name,
		"brand":              product.Brand,
		"sku":                product.SKU,
		"color":              product.Color,
		"colorHex":           product.ColorHex,
		"countryOfOrigin":    product.CountryOfOrigin,
		"thumbnailId":        product.ThumbnailMediaID,
		"price":              product.Price,
		"salePercentage":     product.SalePercentage,
		"topCategoryId":      product.TopCategoryId,
		"subCategoryId":      product.SubCategoryId,
		"typeId":             product.TypeId,
		"modelWearsHeightCm": product.ModelWearsHeightCm,
		"modelWearsSizeId":   product.ModelWearsSizeId,
		"description":        product.Description,
		"hidden":             product.Hidden,
		"targetGender":       product.TargetGender,
		"careInstructions":   product.CareInstructions,
		"composition":        product.Composition,
	}

	slog.Default().Error("insertProduct", slog.Any("query", query), slog.Any("params", params))

	id, err := ExecNamedLastId(ctx, rep.DB(), query, params)
	if err != nil {
		return id, err
	}

	return id, nil
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

	rowsPrdSizes := make([]map[string]any, 0, len(sizeMeasurements))
	rowsPrdMeasurements := make([]map[string]any, 0, len(sizeMeasurements))
	for _, sm := range sizeMeasurements {
		row := map[string]any{
			"product_id": productID,
			"size_id":    sm.ProductSize.SizeId,
			"quantity":   sm.ProductSize.QuantityDecimal(),
		}
		rowsPrdSizes = append(rowsPrdSizes, row)

		for _, m := range sm.Measurements {
			row := map[string]any{
				"product_id":          productID,
				"product_size_id":     sm.ProductSize.SizeId,
				"measurement_name_id": m.MeasurementNameId,
				"measurement_value":   m.MeasurementValue,
			}
			rowsPrdMeasurements = append(rowsPrdMeasurements, row)
		}

	}
	err := BulkInsert(ctx, rep.DB(), "product_size", rowsPrdSizes)
	if err != nil {
		return fmt.Errorf("can't insert product sizes: %w", err)
	}

	err = BulkInsert(ctx, rep.DB(), "size_measurement", rowsPrdMeasurements)
	if err != nil {
		return fmt.Errorf("can't insert product measurements: %w", err)
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
		if !prd.Product.SalePercentage.Valid || prd.Product.SalePercentage.Decimal.LessThan(decimal.Zero) {
			prd.Product.SalePercentage = decimal.NullDecimal{
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
		slog.Default().DebugContext(ctx, "product", slog.Any("product", prd.Product.Preorder))
		err := updateProduct(ctx, rep, prd.Product, id)
		if err != nil {
			return fmt.Errorf("can't update product: %w", err)
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
		name = :name, 
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
		description = :description, 
		hidden = :hidden,
		target_gender = :targetGender,
		care_instructions = :careInstructions,
		composition = :composition
	WHERE id = :id
	`
	return ExecNamed(ctx, rep.DB(), query, map[string]any{
		"preorder":           prd.Preorder,
		"name":               prd.Name,
		"brand":              prd.Brand,
		"sku":                prd.SKU,
		"color":              prd.Color,
		"colorHex":           prd.ColorHex,
		"countryOfOrigin":    prd.CountryOfOrigin,
		"thumbnailId":        prd.ThumbnailMediaID,
		"price":              prd.Price,
		"salePercentage":     prd.SalePercentage,
		"topCategoryId":      prd.TopCategoryId,
		"subCategoryId":      prd.SubCategoryId,
		"typeId":             prd.TypeId,
		"modelWearsHeightCm": prd.ModelWearsHeightCm,
		"modelWearsSizeId":   prd.ModelWearsSizeId,
		"description":        prd.Description,
		"hidden":             prd.Hidden,
		"targetGender":       prd.TargetGender,
		"careInstructions":   prd.CareInstructions,
		"composition":        prd.Composition,
		"id":                 id,
	})
}

// GetProductsPaged
// Parameters:
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

	slog.Default().DebugContext(ctx, "listQuery", slog.String("listQuery", listQuery))

	// Set limit and offset
	args["limit"] = limit
	args["offset"] = offset

	// Fetch products
	prds, err := QueryListNamed[entity.Product](ctx, ms.db, listQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get products: %w", err)
	}

	return prds, count, nil
}

func (ms *MYSQLStore) GetProductsByIds(ctx context.Context, ids []int) ([]entity.Product, error) {
	if len(ids) == 0 {
		return []entity.Product{}, nil
	}

	query := `
	SELECT 
		p.*,
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

	prds, err := QueryListNamed[entity.Product](ctx, ms.db, query, map[string]any{
		"ids": ids,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	// Create a map for quick lookup
	prdMap := make(map[int]entity.Product)
	for _, p := range prds {
		prdMap[p.Id] = p
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
		p.*,
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
	WHERE p.id IN (SELECT pt.product_id FROM product_tag pt WHERE pt.tag = :tag) AND p.hidden = 0`

	prds, err := QueryListNamed[entity.Product](ctx, ms.db, query, map[string]any{
		"tag": tag,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	return prds, nil
}

// buildQuery refactored to use named parameters and to include limit and offset
func buildQuery(sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, whereClauses []string, limit int, offset int) (string, string) {
	baseQuery := `
	SELECT 
		p.*,
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
		media m
	ON 
		p.thumbnail_id = m.id`

	countQuery := "SELECT COUNT(*) FROM product p JOIN media m ON p.thumbnail_id = m.id"

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
	return ms.getProductDetails(ctx, map[string]any{"id": id}, true) // No year filter needed
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

	// Building the WHERE clause of the query with named parameters to prevent SQL injection
	// Building the WHERE clause of the query with named parameters to prevent SQL injection
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
		p.name,
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
		p.description,
		p.hidden,
		p.target_gender,
		p.care_instructions,
		p.composition,
		m.id AS thumbnail_id,
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
		media m
	ON 
		p.thumbnail_id = m.id 
	WHERE %s`, strings.Join(whereClauses, " AND "))

	// Include or exclude hidden products based on the showHidden flag
	if !showHidden {
		query += " AND p.hidden = false"
	}
	type product struct {
		entity.Product
		ThumbnailID int       `db:"thumbnail_id"`
		CreatedAt   time.Time `db:"thumbnail_created_at"`
	}

	slog.Default().DebugContext(ctx, "query", slog.String("query", query))
	slog.Default().DebugContext(ctx, "params", slog.Any("params", params))
	prd, err := QueryNamedOne[product](ctx, ms.db, query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get product: %w", err)
	}
	prd.Product.MediaFull.Id = prd.ThumbnailID
	prd.Product.MediaFull.CreatedAt = prd.CreatedAt

	productInfo.Product = &prd.Product

	// fetch sizes
	query = `SELECT * FROM product_size WHERE product_id = :id`

	sizes, err := QueryListNamed[entity.ProductSize](ctx, ms.db, query, map[string]any{
		"id": prd.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get sizes: %w", err)
	}
	productInfo.Sizes = sizes

	// fetch measurements
	query = `SELECT * FROM size_measurement	WHERE product_id = :id`

	measurements, err := QueryListNamed[entity.ProductMeasurement](ctx, ms.db, query, map[string]any{
		"id": prd.Id,
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
		WHERE pm.product_id = :id;
	`
	productInfo.Media, err = QueryListNamed[entity.MediaFull](ctx, ms.db, query, map[string]interface{}{
		"id": prd.Id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get media: %w", err)
	}

	// Fetch Tags
	query = "SELECT * FROM product_tag WHERE product_id = :id"
	productInfo.Tags, err = QueryListNamed[entity.ProductTag](ctx, ms.db, query, map[string]interface{}{
		"id": prd.Id,
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
			p.name,
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
			p.description,
			p.hidden,
			p.target_gender,
			p.care_instructions,
			p.composition,
			m.id AS thumbnail_id,
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
			media m
		ON 
			p.thumbnail_id = m.id 
		WHERE p.id IN (:productIds)`

	type product struct {
		entity.Product
		ThumbnailID int       `db:"thumbnail_id"`
		CreatedAt   time.Time `db:"thumbnail_created_at"`
	}

	prds, err := QueryListNamed[product](ctx, rep.DB(), query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query hero products: %w", err)
	}

	products := make([]entity.Product, 0, len(prds))

	for _, p := range prds {
		prd := p.Product
		prd.MediaFull.Id = p.ThumbnailID
		prd.MediaFull.CreatedAt = p.CreatedAt

		products = append(products, prd)
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
