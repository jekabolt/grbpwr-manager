package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Knetic/go-namedParameterQuery"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slog"
)

type productStore struct {
	*MYSQLStore
}

// ParticipateStore returns an object implementing participate interface
func (ms *MYSQLStore) Products() dependency.Products {
	return &productStore{
		MYSQLStore: ms,
	}
}

func insertProduct(ctx context.Context, rep dependency.Repository, product *entity.ProductInsert) (*entity.Product, error) {
	query := `
	INSERT INTO product 
	(preorder, name, brand, sku, color, color_hex, country_of_origin, thumbnail, price, category_id, description, hidden, target_gender)
	VALUES (:preorder, :name, :brand, :sku, :color, :colorHex, :countryOfOrigin, :thumbnail, :price, :categoryId, :description, :hidden, :targetGender)`
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
		"categoryId":      product.CategoryID,
		"description":     product.Description,
		"hidden":          product.Hidden,
		"targetGender":    product.TargetGender,
	})
	if err != nil {
		return nil, fmt.Errorf("can't insert product: %w", err)
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
		productSizeID, err := ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
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
				"productSizeId":     productSizeID,
				"measurementNameId": measurement.MeasurementNameID,
				"measurementValue":  measurement.MeasurementValue.String(),
			})

			if err != nil {
				return nil, fmt.Errorf("error inserting into size_measurement: %v", err)
			}

			measurements = append(measurements, entity.ProductMeasurement{
				ProductID:         productID,
				ProductSizeID:     productSizeID,
				MeasurementNameID: measurement.MeasurementNameID,
				MeasurementValue:  measurement.MeasurementValue,
			})
		}

		result = append(result, entity.SizeWithMeasurement{
			ProductSize: entity.ProductSize{
				ID:        productSizeID,
				ProductID: productID,
				Quantity:  sizeMeasurement.ProductSize.Quantity,
				SizeID:    sizeMeasurement.ProductSize.SizeID,
			},
			Measurements: measurements,
		})
	}

	return result, nil
}

func insertMedia(ctx context.Context, rep dependency.Repository, media []entity.ProductMediaInsert, productID int) ([]entity.ProductMedia, error) {
	rows := make([]map[string]any, 0, len(media))
	medias := make([]entity.ProductMedia, 0, len(media))
	for _, m := range media {
		medias = append(medias, entity.ProductMedia{
			ProductMediaInsert: m,
		})
		row := map[string]any{
			"product_id": productID,
			"full_size":  m.FullSize,
			"thumbnail":  m.Thumbnail,
			"compressed": m.Compressed,
		}
		rows = append(rows, row)

	}

	return medias, BulkInsert(ctx, rep.DB(), "product_media", rows)
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
		pi.Product, err = insertProduct(ctx, rep, prd.Product)
		if err != nil {
			return fmt.Errorf("can't insert product: %w", err)
		}

		sizesWithMeasurements, err := insertSizeMeasurements(ctx, rep, prd.SizeMeasurements, pi.Product.ID)
		if err != nil {
			return fmt.Errorf("can't insert size measurements: %w", err)
		}
		pi.Sizes, pi.Measurements = sizesAndMeasurements(sizesWithMeasurements)

		pi.Media, err = insertMedia(ctx, rep, prd.Media, pi.Product.ID)
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
func (ms *MYSQLStore) GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, showHidden bool) ([]entity.Product, error) {

	if len(sortFactors) > 0 {
		for _, sf := range sortFactors {
			if !entity.IsValidSortFactor(string(sf)) {
				return nil, fmt.Errorf("invalid sort factor: %s", sf)
			}
		}
	}

	// TODO:
	// if orderFactor == "" {
	// 	orderFactor = entity.Ascending
	// }

	// Initialize
	baseQuery := "SELECT * FROM product"
	var whereClauses []string
	args := make(map[string]interface{})

	// Handle hidden products
	if !showHidden {
		whereClauses = append(whereClauses, "hidden = :isHidden")
		args["isHidden"] = 0
	}

	// Handle filters
	if filterConditions != nil {
		if filterConditions.PriceFromTo.From.IsPositive() || filterConditions.PriceFromTo.To.IsPositive() {
			whereClauses = append(whereClauses, "price BETWEEN :priceFrom AND :priceTo")
			args["priceFrom"] = filterConditions.PriceFromTo.From
			args["priceTo"] = filterConditions.PriceFromTo.To
		}
		if filterConditions.OnSale {
			whereClauses = append(whereClauses, "sale_percentage > 0")
		}
		if filterConditions.Color != "" {
			whereClauses = append(whereClauses, "color = :color")
			args["color"] = filterConditions.Color
		}
		if filterConditions.CategoryId != 0 {
			whereClauses = append(whereClauses, "category_id = :categoryId")
			args["categoryId"] = filterConditions.CategoryId
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

	// Build SQL query with named parameters
	fullQuery, err := buildQuery(baseQuery, sortFactors, orderFactor, whereClauses, limit, offset)
	if err != nil {
		return nil, err
	}

	// Add limit and offset
	args["limit"] = limit
	args["offset"] = offset

	// Create NamedParameterQuery and set values
	queryNamed := namedParameterQuery.NewNamedParameterQuery(fullQuery)
	queryNamed.SetValuesFromMap(args)

	// Parse query and prepare args
	query, argsSlice, err := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if err != nil {
		return nil, fmt.Errorf("sqlx in: %w", err)
	}

	slog.Default().DebugCtx(ctx, "paged query",
		slog.String("query", query),
	)
	slog.Default().DebugCtx(ctx, "paged argsSlice",
		slog.Any("argsSlice", argsSlice),
	)
	// Execute query
	return selectProducts(ctx, ms, query, argsSlice)
}

// buildQuery refactored to use named parameters and to include limit and offset
func buildQuery(baseQuery string, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, whereClauses []string, limit int, offset int) (string, error) {
	if len(whereClauses) > 0 {
		baseQuery += " WHERE " + strings.Join(whereClauses, " AND ")
	}
	if len(sortFactors) > 0 {
		baseQuery += " ORDER BY " + strings.Join(entity.SortFactorsToSS(sortFactors), ", ")
		if orderFactor != "" {
			baseQuery += " " + string(orderFactor)
		} else {
			baseQuery += " " + string(entity.Ascending)
		}
	}

	baseQuery += " LIMIT :limit OFFSET :offset"

	return baseQuery, nil
}

// selectProducts - assuming this is similar to your existing function
func selectProducts(ctx context.Context, rep dependency.Repository, query string, args []interface{}) ([]entity.Product, error) {
	var products []entity.Product
	err := rep.DB().SelectContext(ctx, &products, query, args...)
	if err != nil {
		return nil, fmt.Errorf("can't select products: %w", err)
	}
	return products, nil
}

// GetProductByID returns a product by its ID.
func (ms *MYSQLStore) GetProductByID(ctx context.Context, id int) (*entity.ProductFull, error) {
	var productInfo entity.ProductFull
	var err error

	// Fetch Product
	query := `SELECT * FROM product WHERE id = :id`

	prd, err := QueryNamedOne[entity.Product](ctx, ms.db, query, map[string]any{
		"id": id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get product: %w", err)
	}

	productInfo.Product = &prd

	// fetch sizes
	query = `SELECT * FROM product_size WHERE product_id = :id`

	sizes, err := QueryListNamed[entity.ProductSize](ctx, ms.db, query, map[string]any{
		"id": id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get sizes: %w", err)
	}
	productInfo.Sizes = sizes

	// fetch measurements
	query = `SELECT * FROM size_measurement	WHERE product_id = :id`

	measurements, err := QueryListNamed[entity.ProductMeasurement](ctx, ms.db, query, map[string]any{
		"id": id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get measurements: %w", err)
	}
	productInfo.Measurements = measurements

	// Fetch Media
	query = "SELECT * FROM product_media WHERE product_id = :id"
	productInfo.Media, err = QueryListNamed[entity.ProductMedia](ctx, ms.db, query, map[string]interface{}{
		"id": id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get media: %w", err)
	}

	// Fetch Tags
	query = "SELECT * FROM product_tag WHERE product_id = :id"
	productInfo.Tags, err = QueryListNamed[entity.ProductTag](ctx, ms.db, query, map[string]interface{}{
		"id": id,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get tags: %w", err)
	}

	return &productInfo, nil
}

// DeleteProductByID deletes a product by its ID.
func (ms *MYSQLStore) DeleteProductByID(ctx context.Context, id int) error {
	query := "DELETE FROM product WHERE id = :id"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"id": id,
	})
}

// HideProductByID hides/unhides a product by its ID.
func (ms *MYSQLStore) HideProductByID(ctx context.Context, id int, hide bool) error {
	query := "UPDATE product SET hidden = :hidden WHERE id = :id"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"hidden": hide,
		"id":     id,
	})
}

// SetSaleByID sets sale percentage for a product by its ID.
func (ms *MYSQLStore) SetSaleByID(ctx context.Context, id int, salePercent decimal.Decimal) error {
	query := "UPDATE product SET sale_percentage = :salePercentage WHERE id = :id"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"salePercentage": salePercent,
		"id":             id,
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

func (ms *MYSQLStore) UpdateProductPreorder(ctx context.Context, productID int, preorder string) error {
	query := `UPDATE product SET preorder = :preorder WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"preorder": preorder,
		"id":       productID,
	})
}

func (ms *MYSQLStore) UpdateProductName(ctx context.Context, productID int, name string) error {
	query := `UPDATE product SET name = :name WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"name": name,
		"id":   productID,
	})
}

func (ms *MYSQLStore) UpdateProductSKU(ctx context.Context, productID int, sku string) error {
	query := `UPDATE product SET sku = :sku WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"sku": sku,
		"id":  productID,
	})
}

func (ms *MYSQLStore) UpdateProductColorAndColorHex(ctx context.Context, productID int, color, colorHex string) error {
	query := `UPDATE product SET color = :color, color_hex = :colorHex WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"color":    color,
		"colorHex": colorHex,
		"id":       productID,
	})
}

func (ms *MYSQLStore) UpdateProductCountryOfOrigin(ctx context.Context, productID int, countryOfOrigin string) error {
	query := `UPDATE product SET country_of_origin = :countryOfOrigin WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"countryOfOrigin": countryOfOrigin,
		"id":              productID,
	})
}

func (ms *MYSQLStore) UpdateProductTargetGender(ctx context.Context, productID int, gender entity.GenderEnum) error {
	ok := entity.ValidProductTargetGenders[gender]
	if !ok {
		return fmt.Errorf("invalid gender")
	}
	query := `UPDATE product SET target_gender = :targetGender WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"targetGender": gender,
		"id":           productID,
	})
}

func (ms *MYSQLStore) UpdateProductBrand(ctx context.Context, productID int, brand string) error {
	query := `UPDATE product SET brand = :brand WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"brand": brand,
		"id":    productID,
	})
}

func (ms *MYSQLStore) UpdateProductThumbnail(ctx context.Context, productID int, thumbnail string) error {
	query := `UPDATE product SET thumbnail = :thumbnail WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"thumbnail": thumbnail,
		"id":        productID,
	})
}

func (ms *MYSQLStore) UpdateProductPrice(ctx context.Context, productID int, price decimal.Decimal) error {
	query := `UPDATE product SET price = :price WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"price": price,
		"id":    productID,
	})
}

func (ms *MYSQLStore) UpdateProductSale(ctx context.Context, productID int, sale decimal.Decimal) error {
	query := `UPDATE product SET sale_percentage = :salePercentage WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"salePercentage": sale,
		"id":             productID,
	})
}

func (ms *MYSQLStore) UpdateProductCategory(ctx context.Context, productID int, categoryID int) error {
	cat, ok := ms.cache.GetCategoryByID(categoryID)
	if !ok {
		return fmt.Errorf("can't get category by id: %d", categoryID)
	}
	query := `UPDATE product SET category_id = :categoryId WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"categoryId": cat.ID,
		"id":         productID,
	})
}

func (ms *MYSQLStore) UpdateProductDescription(ctx context.Context, productID int, description string) error {
	query := `UPDATE product SET description = :description WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"description": description,
		"id":          productID,
	})
}

func (ms *MYSQLStore) DeleteProductMeasurement(ctx context.Context, id int) error {
	query := `DELETE FROM size_measurement WHERE id = :id`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"id": id,
	})
}

func (ms *MYSQLStore) AddProductMeasurement(ctx context.Context, productId, sizeId, measurementNameId int, measurementValue decimal.Decimal) error {

	measurement, ok := ms.cache.GetMeasurementByID(measurementNameId)
	if !ok {
		return fmt.Errorf("can't get measurement by name: %d", measurementNameId)
	}

	size, ok := ms.cache.GetSizeByID(sizeId)
	if !ok {
		return fmt.Errorf("can't get size by name: %d", sizeId)
	}

	query := `
	INSERT INTO size_measurement 
    	(product_id, product_size_id, measurement_name_id, measurement_value) 
	VALUES
   		(:productId, :productSizeId, :measurementNameId, :measurementValue) 
	ON DUPLICATE KEY UPDATE 
   		measurement_value = VALUES(measurement_value);
	`
	err := ExecNamed(context.Background(), ms.db, query, map[string]any{
		"productId":         productId,
		"productSizeId":     size.ID,
		"measurementNameId": measurement.ID,
		"measurementValue":  measurementValue,
	})

	if err != nil {
		return fmt.Errorf("can't insert product measurement: %w", err)
	}

	return nil
}

func (ms *MYSQLStore) UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error {

	sz, ok := ms.cache.GetSizeByID(sizeId)
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

func (ms *MYSQLStore) DeleteProductMedia(ctx context.Context, productMediaId int) error {
	query := "DELETE FROM product_media WHERE id = :id"
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"id": productMediaId,
	})
}

func (ms *MYSQLStore) AddProductMedia(ctx context.Context, productId int, fullSize string, thumbnail string, compressed string) error {
	query := `INSERT INTO product_media (product_id, full_size, thumbnail, compressed) VALUES (:productId, :fullSize, :thumbnail, :compressed)`
	return ExecNamed(ctx, ms.db, query, map[string]interface{}{
		"productId":  productId,
		"fullSize":   fullSize,
		"thumbnail":  thumbnail,
		"compressed": compressed,
	})
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
