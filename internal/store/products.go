package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
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

func insertProduct(ctx context.Context, rep dependency.Repository, product *entity.ProductInsert) (*entity.Product, error) {
	query := `
	INSERT INTO product 
	(preorder, name, brand, sku, color, color_hex, country_of_origin, thumbnail, price, sale_percentage, category_id, description, hidden, target_gender)
	VALUES (:preorder, :name, :brand, :sku, :color, :colorHex, :countryOfOrigin, :thumbnail, :price, :salePercentage, :categoryId, :description, :hidden, :targetGender)`
	id, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
		"preorder":        product.Preorder,
		"name":            product.Name,
		"brand":           product.Brand,
		"sku":             product.SKU,
		"color":           product.Color,
		"colorHex":        product.ColorHex,
		"countryOfOrigin": product.CountryOfOrigin,
		"thumbnail":       product.Thumbnail,
		"price":           product.Price,
		"salePercentage":  product.SalePercentage,
		"categoryId":      product.CategoryID,
		"description":     product.Description,
		"hidden":          product.Hidden,
		"targetGender":    product.TargetGender,
	})
	if err != nil {
		return nil, err
	}

	return &entity.Product{
		ID:            id,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		ProductInsert: *product,
	}, nil
}

func insertSizeMeasurements(ctx context.Context, rep dependency.Repository, sizeMeasurements []entity.SizeWithMeasurementInsert, productID int) ([]entity.SizeWithMeasurement, error) {
	var result []entity.SizeWithMeasurement

	for _, sizeMeasurement := range sizeMeasurements {
		query := `
		INSERT INTO product_size (product_id, size_id, quantity)
		VALUES (:productId, :sizeId, :quantity)
		`
		_, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"productId": productID,
			"sizeId":    sizeMeasurement.ProductSize.SizeID,
			"quantity":  sizeMeasurement.ProductSize.Quantity,
		})
		if err != nil {
			return nil, fmt.Errorf("error inserting into product_size: %v", err)
		}

		var measurements []entity.ProductMeasurement

		for _, measurement := range sizeMeasurement.Measurements {
			query := `
				INSERT INTO size_measurement (product_id, product_size_id, measurement_name_id, measurement_value)
				VALUES (:productId, :productSizeId, :measurementNameId, :measurementValue)
			`
			err := ExecNamed(ctx, rep.DB(), query, map[string]any{
				"productId":         productID,
				"productSizeId":     sizeMeasurement.ProductSize.SizeID,
				"measurementNameId": measurement.MeasurementNameID,
				"measurementValue":  measurement.MeasurementValue.String(),
			})

			if err != nil {
				return nil, fmt.Errorf("error inserting into size_measurement: %v", err)
			}

			measurements = append(measurements, entity.ProductMeasurement{
				ProductID:         productID,
				ProductSizeID:     sizeMeasurement.ProductSize.SizeID,
				MeasurementNameID: measurement.MeasurementNameID,
				MeasurementValue:  measurement.MeasurementValue,
			})
		}

		result = append(result, entity.SizeWithMeasurement{
			ProductSize: entity.ProductSize{
				ID:        sizeMeasurement.ProductSize.SizeID,
				ProductID: productID,
				Quantity:  sizeMeasurement.ProductSize.Quantity,
				SizeID:    sizeMeasurement.ProductSize.SizeID,
			},
			Measurements: measurements,
		})
	}

	return result, nil
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

// sizesAndMeasurements converts []SizeWithMeasurementInsert to []ProductSizeInsert and []ProductMeasurementInsert
func sizesAndMeasurements(sizesWithMeasurements []entity.SizeWithMeasurement) ([]entity.ProductSize, []entity.ProductMeasurement) {
	var sizes []entity.ProductSize
	var measurements []entity.ProductMeasurement

	for _, sizeWithMeasurement := range sizesWithMeasurements {
		// Convert ProductSizeInsert to ProductSize
		size := entity.ProductSize{
			Quantity:  sizeWithMeasurement.ProductSize.Quantity,
			SizeID:    sizeWithMeasurement.ProductSize.SizeID,
			ProductID: sizeWithMeasurement.ProductSize.ProductID,
		}
		sizes = append(sizes, size)

		// Convert []ProductMeasurementInsert to []ProductMeasurement
		for _, measurementInsert := range sizeWithMeasurement.Measurements {
			measurement := entity.ProductMeasurement{
				MeasurementNameID: measurementInsert.MeasurementNameID,
				MeasurementValue:  measurementInsert.MeasurementValue,
				ProductID:         size.ProductID,
			}
			measurements = append(measurements, measurement)
		}
	}

	return sizes, measurements
}

// AddProduct adds a new product to the product store.
func (ms *MYSQLStore) AddProduct(ctx context.Context, prd *entity.ProductNew) (*entity.ProductFull, error) {

	pi := &entity.ProductFull{}
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		if !prd.Product.SalePercentage.Valid || prd.Product.SalePercentage.Decimal.LessThan(decimal.Zero) {
			prd.Product.SalePercentage = decimal.NullDecimal{
				Valid:   true,
				Decimal: decimal.NewFromFloat(0),
			}
		}
		pi.Product, err = insertProduct(ctx, rep, prd.Product)
		if err != nil {
			return fmt.Errorf("can't insert product: %w", err)
		}

		sizesWithMeasurements, err := insertSizeMeasurements(ctx, rep, prd.SizeMeasurements, pi.Product.ID)
		if err != nil {
			return fmt.Errorf("can't insert size measurements: %w", err)
		}
		pi.Sizes, pi.Measurements = sizesAndMeasurements(sizesWithMeasurements)

		err = insertMedia(ctx, rep, prd.MediaIds, pi.Product.ID)
		if err != nil {
			return fmt.Errorf("can't insert media: %w", err)
		}
		pi.Tags, err = insertTags(ctx, rep, prd.Tags, pi.Product.ID)
		if err != nil {
			return fmt.Errorf("can't insert tags: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("can't add product: %w", err)
	}

	return pi, nil
}

func (ms *MYSQLStore) UpdateProduct(ctx context.Context, prd *entity.ProductInsert, id int) error {
	if !prd.SalePercentage.Valid || prd.SalePercentage.Decimal.LessThan(decimal.Zero) {
		prd.SalePercentage = decimal.NullDecimal{
			Valid:   true,
			Decimal: decimal.NewFromFloat(0),
		}
	}
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
		thumbnail = :thumbnail, 
		price = :price, 
		sale_percentage = :salePercentage,
		category_id = :categoryId, 
		description = :description, 
		hidden = :hidden,
		target_gender = :targetGender
	WHERE id = :id
	`
	return ExecNamed(ctx, ms.db, query, map[string]any{
		"preorder":        prd.Preorder,
		"name":            prd.Name,
		"brand":           prd.Brand,
		"sku":             prd.SKU,
		"color":           prd.Color,
		"colorHex":        prd.ColorHex,
		"countryOfOrigin": prd.CountryOfOrigin,
		"thumbnail":       prd.Thumbnail,
		"price":           prd.Price,
		"salePercentage":  prd.SalePercentage,
		"categoryId":      prd.CategoryID,
		"description":     prd.Description,
		"hidden":          prd.Hidden,
		"targetGender":    prd.TargetGender,
		"id":              id,
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
		whereClauses = append(whereClauses, "hidden = :isHidden")
		args["isHidden"] = 0
	}

	// Handle filters
	if filterConditions != nil {
		if filterConditions.To.GreaterThan(decimal.Zero) && filterConditions.From.LessThanOrEqual(filterConditions.To) &&
			filterConditions.From.GreaterThanOrEqual(decimal.Zero) {
			whereClauses = append(whereClauses, "price BETWEEN :priceFrom AND :priceTo")
			args["priceFrom"] = filterConditions.From
			args["priceTo"] = filterConditions.To

		}
		if filterConditions.OnSale {
			whereClauses = append(whereClauses, "sale_percentage > 0")
		}
		if filterConditions.Color != "" {
			whereClauses = append(whereClauses, "color = :color")
			args["color"] = filterConditions.Color
		}
		if len(filterConditions.CategoryIds) != 0 {
			whereClauses = append(whereClauses, "category_id IN (:categoryIds)")
			args["categoryIds"] = filterConditions.CategoryIds
		}
		if len(filterConditions.SizesIds) > 0 {
			whereClauses = append(whereClauses, "id IN (SELECT product_id FROM product_size WHERE size_id IN (:sizes))")
			args["sizes"] = filterConditions.SizesIds
		}
		if filterConditions.Preorder {
			whereClauses = append(whereClauses, "preorder IS NOT NULL AND preorder <> ''")
		}
		if filterConditions.ByTag != "" {
			whereClauses = append(whereClauses, "id IN (SELECT product_id FROM product_tag WHERE tag = :tag)")
			args["tag"] = filterConditions.ByTag
		}
	}

	listQuery, countQuery := buildQuery(sortFactors, orderFactor, whereClauses, limit, offset)
	count, err := QueryCountNamed(ctx, ms.db, countQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get product count: %w", err)
	}

	// slog.Default().DebugContext(ctx, "listQuery", slog.String("listQuery", listQuery))
	// slog.Default().DebugContext(ctx, "countQuery", slog.String("countQuery", countQuery))

	// Add limit and offset
	args["limit"] = limit
	args["offset"] = offset

	prds, err := QueryListNamed[entity.Product](ctx, ms.db, listQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get products: %w", err)
	}

	return prds, count, nil
}

// buildQuery refactored to use named parameters and to include limit and offset
func buildQuery(sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, whereClauses []string, limit int, offset int) (string, string) {
	baseQuery := "SELECT * FROM product"
	countQuery := "SELECT COUNT(*) FROM product"

	if len(whereClauses) > 0 {
		conditions := " WHERE " + strings.Join(whereClauses, " AND ")
		baseQuery += conditions
		countQuery += conditions
	}

	if len(sortFactors) > 0 {
		ordering := " ORDER BY " + strings.Join(entity.SortFactorsToSS(sortFactors), ", ")
		if orderFactor != "" {
			ordering += " " + string(orderFactor)
		} else {
			ordering += " " + string(entity.Ascending)
		}
		baseQuery += ordering
	}
	baseQuery += " LIMIT " + strconv.Itoa(limit) + " OFFSET " + strconv.Itoa(offset)

	return baseQuery, countQuery
}

// GetProductByIdShowHidden returns a product by its ID, potentially including hidden products.
func (ms *MYSQLStore) GetProductByIdShowHidden(ctx context.Context, id int) (*entity.ProductFull, error) {
	return ms.getProductDetails(ctx, map[string]any{"id": id}, true) // No year filter needed
}

// GetProductByNameNoHidden returns a product by its name, excluding hidden products.
func (ms *MYSQLStore) GetProductByNameNoHidden(ctx context.Context, id int, name string) (*entity.ProductFull, error) {
	filters := map[string]any{
		"name": name,
		"id":   id,
	}
	return ms.getProductDetails(ctx, filters, false)
}

// getProductDetails fetches product details based on a specific field and value.
func (ms *MYSQLStore) getProductDetails(ctx context.Context, filters map[string]any, showHidden bool) (*entity.ProductFull, error) {
	var productInfo entity.ProductFull

	// Building the WHERE clause of the query with named parameters to prevent SQL injection
	whereClauses := []string{}
	params := map[string]interface{}{}
	for key, value := range filters {
		keyCamel := toCamelCase(key)
		whereClause := fmt.Sprintf("%s = :%s", key, keyCamel)
		whereClauses = append(whereClauses, whereClause)
		params[keyCamel] = value
	}

	query := fmt.Sprintf("SELECT * FROM product WHERE %s", strings.Join(whereClauses, " AND "))

	// Include or exclude hidden products based on the showHidden flag
	if !showHidden {
		query += " AND hidden = false"
	}
	// Execute the query using a safe, parameterized approach
	prd, err := QueryNamedOne[entity.Product](ctx, ms.db, query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get product: %w", err)
	}
	productInfo.Product = &prd

	// fetch sizes
	query = `SELECT * FROM product_size WHERE product_id = :id`

	sizes, err := QueryListNamed[entity.ProductSize](ctx, ms.db, query, map[string]any{
		"id": prd.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get sizes: %w", err)
	}
	productInfo.Sizes = sizes

	// fetch measurements
	query = `SELECT * FROM size_measurement	WHERE product_id = :id`

	measurements, err := QueryListNamed[entity.ProductMeasurement](ctx, ms.db, query, map[string]any{
		"id": prd.ID,
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
			m.compressed_height
		FROM media m
		INNER JOIN product_media pm ON m.id = pm.media_id
		WHERE pm.product_id = :id;
	`
	productInfo.Media, err = QueryListNamed[entity.MediaFull](ctx, ms.db, query, map[string]interface{}{
		"id": prd.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get media: %w", err)
	}

	// Fetch Tags
	query = "SELECT * FROM product_tag WHERE product_id = :id"
	productInfo.Tags, err = QueryListNamed[entity.ProductTag](ctx, ms.db, query, map[string]interface{}{
		"id": prd.ID,
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
	for i, part := range parts {
		if i > 0 { // Only title-case the second part onwards
			parts[i] = strings.Title(part)
		} else {
			parts[i] = strings.ToLower(part) // Ensure the first part is all lower case
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
			"productId": item.ProductID,
			"sizeId":    item.SizeID,
		})
		if err != nil {
			return fmt.Errorf("error checking current quantity: %w", err)
		}

		if productSize.Quantity.Add(item.Quantity.Neg()).LessThan(decimal.Zero) {
			return fmt.Errorf("cannot decrease available sizes: insufficient quantity for product ID: %d, size ID: %d", item.ProductID, item.SizeID)
		}

		query = `UPDATE product_size SET quantity = quantity - :quantity WHERE product_id = :productId AND size_id = :sizeId`
		err = ExecNamed(ctx, ms.db, query, map[string]any{
			"quantity":  item.Quantity,
			"productId": item.ProductID,
			"sizeId":    item.SizeID,
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
			"quantity":  item.Quantity,
			"productId": item.ProductID,
			"sizeId":    item.SizeID,
		})
		if err != nil {
			return fmt.Errorf("can't restore product quantity for sizes: %w", err)
		}
	}
	return nil
}

func (ms *MYSQLStore) deleteProductMeasurement(ctx context.Context, mUpd entity.ProductMeasurementUpdate) error {
	query := "DELETE FROM size_measurement WHERE product_size_id = :productSizeId AND measurement_name_id = :measurementNameId"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"productSizeId":     mUpd.SizeId,
		"measurementNameId": mUpd.MeasurementNameId,
	})
}

func (ms *MYSQLStore) UpdateProductMeasurements(ctx context.Context, productId int, mUpd []entity.ProductMeasurementUpdate) error {
	for _, mu := range mUpd {
		if mu.MeasurementValue.LessThan(decimal.Zero) {
			slog.Default().ErrorContext(ctx, "can't update product measurements: negative value",
				slog.Int("product_id", productId),
				slog.Int("size_id", mu.SizeId),
				slog.Int("measurement_name_id", mu.MeasurementNameId),
				slog.String("measurement_value", mu.MeasurementValue.String()),
			)
			continue
		}

		if mu.MeasurementValue.Equal(decimal.Zero) {
			err := ms.deleteProductMeasurement(ctx, mu)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't delete product measurement",
					slog.Int("product_id", productId),
					slog.Int("size_id", mu.SizeId),
					slog.Int("measurement_name_id", mu.MeasurementNameId),
					slog.String("measurement_value", mu.MeasurementValue.String()),
					slog.String("err", err.Error()),
				)
			}
			continue
		}

		query := `
		INSERT INTO size_measurement 
			(product_id, product_size_id, measurement_name_id, measurement_value) 
		VALUES 
			(:productId, :productSizeId, :measurementNameId, :measurementValue) 
		ON DUPLICATE KEY UPDATE 
			measurement_value = VALUES(measurement_value);
		`
		err := ExecNamed(ctx, ms.db, query, map[string]any{
			"productId":         productId,
			"productSizeId":     mu.SizeId,
			"measurementNameId": mu.MeasurementNameId,
			"measurementValue":  mu.MeasurementValue,
		})
		if err != nil {
			return fmt.Errorf("can't update product measurements: %w", err)
		}
	}
	return nil
}

func (ms *MYSQLStore) UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error {

	sz, ok := ms.cache.GetSizeById(sizeId)
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
		"sizeId":    sz.ID,
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

func (ms *MYSQLStore) AddProductTag(ctx context.Context, productId int, tag string) error {
	query := "INSERT INTO product_tag (product_id, tag) VALUES (:productId, :tag)"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"productId": productId,
		"tag":       tag,
	})
}

func (ms *MYSQLStore) DeleteProductTag(ctx context.Context, productId int, tag string) error {
	query := "DELETE FROM product_tag WHERE product_id = :productId AND tag = :tag"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"productId": productId,
		"tag":       tag,
	})
}

// for orders

func getProductsByIds(ctx context.Context, rep dependency.Repository, productIds []int) ([]entity.Product, error) {
	if len(productIds) == 0 {
		return []entity.Product{}, nil
	}
	query := `
	SELECT * FROM product WHERE id IN (:productIds)`

	products, err := QueryListNamed[entity.Product](ctx, rep.DB(), query, map[string]interface{}{
		"productIds": productIds,
	})
	if err != nil {
		return nil, err
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
		productSizeParams = append(productSizeParams, item.ProductID, item.SizeID)
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
		ids[i] = item.ProductID
	}
	return ids
}
