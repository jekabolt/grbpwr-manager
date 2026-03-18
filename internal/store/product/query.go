package product

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// GetProductsPaged returns a paged list of products based on provided parameters.
func (s *Store) GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, showHidden bool) ([]entity.Product, int, error) {
	if len(sortFactors) > 0 {
		for _, sf := range sortFactors {
			if !entity.IsValidSortFactor(string(sf)) {
				return nil, 0, fmt.Errorf("invalid sort factor: %s", sf)
			}
		}
	}

	var priceSortRequested bool
	for _, sf := range sortFactors {
		if sf == entity.Price {
			priceSortRequested = true
			if filterConditions == nil || filterConditions.Currency == "" {
				return nil, 0, fmt.Errorf("price sorting requires currency to be specified in filter conditions")
			}
			break
		}
	}

	var whereClauses []string
	args := make(map[string]interface{})

	whereClauses = append(whereClauses, "p.deleted_at IS NULL")

	if !showHidden {
		whereClauses = append(whereClauses, "p.hidden = :isHidden")
		args["isHidden"] = 0
	}

	var priceJoinRequired bool
	if priceSortRequested {
		priceJoinRequired = true
		if filterConditions.Currency != "" && filterConditions.From.IsZero() && filterConditions.To.IsZero() {
			whereClauses = append(whereClauses, "pp.currency = :currency")
			args["currency"] = filterConditions.Currency
		}
	}

	if filterConditions != nil && filterConditions.Currency != "" {
		if filterConditions.From.LessThan(decimal.Zero) {
			return nil, 0, fmt.Errorf("price range cannot be negative")
		}
		if filterConditions.From.GreaterThan(filterConditions.To) && !filterConditions.To.Equals(decimal.Zero) {
			return nil, 0, fmt.Errorf("invalid price range: from cannot be greater than to unless to is unset")
		}

		priceExpr := "pp.price * (1 - COALESCE(p.sale_percentage, 0) / 100)"

		switch {
		case filterConditions.From.IsZero() && filterConditions.To.GreaterThan(decimal.Zero):
			whereClauses = append(whereClauses, fmt.Sprintf("pp.currency = :currency AND %s BETWEEN 0 AND :priceTo", priceExpr))
			args["currency"] = filterConditions.Currency
			args["priceTo"] = filterConditions.To
			priceJoinRequired = true
		case filterConditions.From.GreaterThan(decimal.Zero) && filterConditions.To.IsZero():
			whereClauses = append(whereClauses, fmt.Sprintf("pp.currency = :currency AND %s >= :priceFrom", priceExpr))
			args["currency"] = filterConditions.Currency
			args["priceFrom"] = filterConditions.From
			priceJoinRequired = true
		case filterConditions.From.IsZero() && filterConditions.To.IsZero():
			// no price filtering
		case filterConditions.From.GreaterThan(decimal.Zero) && filterConditions.To.GreaterThan(decimal.Zero):
			whereClauses = append(whereClauses, fmt.Sprintf("pp.currency = :currency AND %s BETWEEN :priceFrom AND :priceTo", priceExpr))
			args["currency"] = filterConditions.Currency
			args["priceFrom"] = filterConditions.From
			args["priceTo"] = filterConditions.To
			priceJoinRequired = true
		}
	}

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
		if len(filterConditions.Collections) != 0 {
			whereClauses = append(whereClauses, "p.collection IN (:collections)")
			args["collections"] = filterConditions.Collections
		}
		if len(filterConditions.Seasons) != 0 {
			seasons := make([]string, len(filterConditions.Seasons))
			for i, ss := range filterConditions.Seasons {
				seasons[i] = string(ss)
			}
			whereClauses = append(whereClauses, "p.season IN (:seasons)")
			args["seasons"] = seasons
		}
	}

	listQuery, countQuery := buildQuery(sortFactors, orderFactor, whereClauses, limit, offset, priceJoinRequired)
	count, err := storeutil.QueryCountNamed(ctx, s.DB, countQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get product count: %w", err)
	}

	args["limit"] = limit
	args["offset"] = offset

	prdResults, err := storeutil.QueryListNamed[productQueryResult](ctx, s.DB, listQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get products: %w", err)
	}

	productIds := make([]int, 0, len(prdResults))
	for _, prdResult := range prdResults {
		productIds = append(productIds, prdResult.Id)
	}

	translationMap, err := fetchProductTranslations(ctx, s.DB, productIds)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get product translations: %w", err)
	}

	priceMap, err := fetchProductPrices(ctx, s.DB, productIds)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get product prices: %w", err)
	}

	prds := make([]entity.Product, 0, len(prdResults))
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		product.Prices = priceMap[prdResult.Id]
		prds = append(prds, product)
	}

	return prds, count, nil
}

// GetProductsByIds returns a list of products by their IDs.
func (s *Store) GetProductsByIds(ctx context.Context, ids []int) ([]entity.Product, error) {
	if len(ids) == 0 {
		return []entity.Product{}, nil
	}

	query := `
	SELECT 
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, p.brand, p.sku,
		p.color, p.color_hex, p.country_of_origin, p.sale_percentage,
		p.top_category_id, p.sub_category_id, p.type_id,
		p.model_wears_height_cm, p.model_wears_size_id, p.hidden, p.target_gender,
		p.care_instructions, p.composition, p.thumbnail_id, p.secondary_thumbnail_id,
		p.version, p.collection, p.fit,
		m.full_size, m.full_size_width, m.full_size_height,
		m.thumbnail, m.thumbnail_width, m.thumbnail_height,
		m.compressed, m.compressed_width, m.compressed_height, m.blur_hash,
		sm.created_at AS secondary_thumbnail_created_at,
		sm.full_size AS secondary_full_size, sm.full_size_width AS secondary_full_size_width,
		sm.full_size_height AS secondary_full_size_height,
		sm.thumbnail AS secondary_thumbnail, sm.thumbnail_width AS secondary_thumbnail_width,
		sm.thumbnail_height AS secondary_thumbnail_height,
		sm.compressed AS secondary_compressed, sm.compressed_width AS secondary_compressed_width,
		sm.compressed_height AS secondary_compressed_height, sm.blur_hash AS secondary_blur_hash,
		COALESCE((SELECT SUM(COALESCE(ps.quantity, 0)) FROM product_size ps WHERE ps.product_id = p.id), 0) = 0 AS sold_out
	FROM product p
	JOIN media m ON p.thumbnail_id = m.id 
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id 
	WHERE p.id IN (:ids) AND p.hidden = 0 AND p.deleted_at IS NULL`

	prdResults, err := storeutil.QueryListNamed[productQueryResult](ctx, s.DB, query, map[string]any{
		"ids": ids,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get products by ids: %w", err)
	}

	translationMap, err := fetchProductTranslations(ctx, s.DB, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	priceMap, err := fetchProductPrices(ctx, s.DB, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get product prices: %w", err)
	}

	prdMap := make(map[int]entity.Product)
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		product.Prices = priceMap[prdResult.Id]
		prdMap[product.Id] = product
	}

	result := make([]entity.Product, 0, len(ids))
	for _, id := range ids {
		if p, ok := prdMap[id]; ok {
			result = append(result, p)
		}
	}

	return result, nil
}

// GetProductsByTag returns a list of products by their tag.
func (s *Store) GetProductsByTag(ctx context.Context, tag string) ([]entity.Product, error) {
	if tag == "" {
		return []entity.Product{}, nil
	}

	query := `
	SELECT 
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, p.brand, p.sku,
		p.color, p.color_hex, p.country_of_origin, p.sale_percentage,
		p.top_category_id, p.sub_category_id, p.type_id,
		p.model_wears_height_cm, p.model_wears_size_id, p.hidden, p.target_gender,
		p.care_instructions, p.composition, p.thumbnail_id, p.secondary_thumbnail_id,
		p.version, p.collection, p.fit,
		m.full_size, m.full_size_width, m.full_size_height,
		m.thumbnail, m.thumbnail_width, m.thumbnail_height,
		m.compressed, m.compressed_width, m.compressed_height, m.blur_hash,
		sm.created_at AS secondary_thumbnail_created_at,
		sm.full_size AS secondary_full_size, sm.full_size_width AS secondary_full_size_width,
		sm.full_size_height AS secondary_full_size_height,
		sm.thumbnail AS secondary_thumbnail, sm.thumbnail_width AS secondary_thumbnail_width,
		sm.thumbnail_height AS secondary_thumbnail_height,
		sm.compressed AS secondary_compressed, sm.compressed_width AS secondary_compressed_width,
		sm.compressed_height AS secondary_compressed_height, sm.blur_hash AS secondary_blur_hash,
		COALESCE((SELECT SUM(COALESCE(ps.quantity, 0)) FROM product_size ps WHERE ps.product_id = p.id), 0) = 0 AS sold_out
	FROM product p
	JOIN media m ON p.thumbnail_id = m.id 
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id 
	WHERE p.id IN (SELECT ptag.product_id FROM product_tag ptag WHERE ptag.tag = :tag) AND p.hidden = 0 AND p.deleted_at IS NULL`

	prdResults, err := storeutil.QueryListNamed[productQueryResult](ctx, s.DB, query, map[string]any{
		"tag": tag,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get products by tag: %w", err)
	}

	productIds := make([]int, 0, len(prdResults))
	for _, prdResult := range prdResults {
		productIds = append(productIds, prdResult.Id)
	}

	translationMap, err := fetchProductTranslations(ctx, s.DB, productIds)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	priceMap, err := fetchProductPrices(ctx, s.DB, productIds)
	if err != nil {
		return nil, fmt.Errorf("can't get product prices: %w", err)
	}

	prds := make([]entity.Product, 0, len(prdResults))
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		product.Prices = priceMap[prdResult.Id]
		prds = append(prds, product)
	}

	return prds, nil
}

func (s *Store) getProductDetails(ctx context.Context, filters map[string]any, showHidden bool) (*entity.ProductFull, error) {
	var productInfo entity.ProductFull

	whereClauses := []string{}
	params := map[string]interface{}{}
	for key, value := range filters {
		keyCamel := toCamelCase(key)
		whereClause := fmt.Sprintf("p.%s = :%s", keyCamel, keyCamel)
		whereClauses = append(whereClauses, whereClause)
		params[keyCamel] = value
	}

	query := fmt.Sprintf(`
	SELECT 
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, p.brand, p.sku,
		p.color, p.color_hex, p.country_of_origin, p.sale_percentage,
		p.top_category_id, p.sub_category_id, p.type_id,
		p.model_wears_height_cm, p.model_wears_size_id, p.hidden, p.target_gender,
		p.care_instructions, p.composition, p.thumbnail_id, p.secondary_thumbnail_id,
		p.version, p.collection, p.season, p.fit,
		m.created_at AS thumbnail_created_at,
		m.full_size, m.full_size_width, m.full_size_height,
		m.thumbnail, m.thumbnail_width, m.thumbnail_height,
		m.compressed, m.compressed_width, m.compressed_height, m.blur_hash,
		sm.created_at AS secondary_thumbnail_created_at,
		sm.full_size AS secondary_full_size, sm.full_size_width AS secondary_full_size_width,
		sm.full_size_height AS secondary_full_size_height,
		sm.thumbnail AS secondary_thumbnail, sm.thumbnail_width AS secondary_thumbnail_width,
		sm.thumbnail_height AS secondary_thumbnail_height,
		sm.compressed AS secondary_compressed, sm.compressed_width AS secondary_compressed_width,
		sm.compressed_height AS secondary_compressed_height, sm.blur_hash AS secondary_blur_hash
	FROM product p
	JOIN media m ON p.thumbnail_id = m.id
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id
	WHERE %s`, strings.Join(whereClauses, " AND "))

	query += " AND p.deleted_at IS NULL"

	if !showHidden {
		query += " AND p.hidden = false"
	}

	type productDetailsResult struct {
		productQueryResult
		ThumbnailCreatedAt time.Time `db:"thumbnail_created_at"`
	}

	prdResult, err := storeutil.QueryNamedOne[productDetailsResult](ctx, s.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get product: %w", err)
	}

	translationMap, err := fetchProductTranslations(ctx, s.DB, []int{prdResult.Id})
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	translations := translationMap[prdResult.Id]
	product := prdResult.toProduct(translations)
	product.ProductDisplay.Thumbnail.CreatedAt = prdResult.ThumbnailCreatedAt
	if product.ProductDisplay.SecondaryThumbnail != nil && prdResult.SecondaryThumbnailCreatedAt.Valid {
		product.ProductDisplay.SecondaryThumbnail.CreatedAt = prdResult.SecondaryThumbnailCreatedAt.Time
	}

	priceMap, err := fetchProductPrices(ctx, s.DB, []int{product.Id})
	if err != nil {
		return nil, fmt.Errorf("can't get prices: %w", err)
	}
	if prices, ok := priceMap[product.Id]; ok {
		product.Prices = prices
	} else {
		product.Prices = []entity.ProductPrice{}
	}

	productInfo.Product = &product

	sizesQuery := `SELECT * FROM product_size WHERE product_id = :id`
	sizes, err := storeutil.QueryListNamed[entity.ProductSize](ctx, s.DB, sizesQuery, map[string]any{"id": product.Id})
	if err != nil {
		return nil, fmt.Errorf("can't get sizes: %w", err)
	}
	productInfo.Sizes = sizes

	totalQuantity := decimal.Zero
	for _, size := range sizes {
		totalQuantity = totalQuantity.Add(size.Quantity)
	}
	product.SoldOut = totalQuantity.LessThanOrEqual(decimal.Zero)
	productInfo.Product.SoldOut = product.SoldOut

	measurementsQuery := `SELECT * FROM size_measurement WHERE product_id = :id`
	measurements, err := storeutil.QueryListNamed[entity.ProductMeasurement](ctx, s.DB, measurementsQuery, map[string]any{"id": product.Id})
	if err != nil {
		return nil, fmt.Errorf("can't get measurements: %w", err)
	}
	productInfo.Measurements = measurements

	mediaQuery := `
		SELECT m.id, m.created_at, m.full_size, m.full_size_width, m.full_size_height,
			m.thumbnail, m.thumbnail_width, m.thumbnail_height,
			m.compressed, m.compressed_width, m.compressed_height, m.blur_hash
		FROM media m
		INNER JOIN product_media pm ON m.id = pm.media_id
		WHERE pm.product_id = :id
		ORDER BY pm.id;
	`
	productInfo.Media, err = storeutil.QueryListNamed[entity.MediaFull](ctx, s.DB, mediaQuery, map[string]any{"id": product.Id})
	if err != nil {
		return nil, fmt.Errorf("can't get media: %w", err)
	}

	tagsQuery := "SELECT * FROM product_tag WHERE product_id = :id"
	productInfo.Tags, err = storeutil.QueryListNamed[entity.ProductTag](ctx, s.DB, tagsQuery, map[string]any{"id": product.Id})
	if err != nil {
		return nil, fmt.Errorf("can't get tags: %w", err)
	}

	productInfo.Prices = product.Prices

	return &productInfo, nil
}

func buildQuery(sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, whereClauses []string, limit int, offset int, priceJoinRequired bool) (string, string) {
	priceJoin := ""
	if priceJoinRequired {
		priceJoin = "\n\tJOIN product_price pp ON p.id = pp.product_id"
	}

	baseQuery := `
	SELECT 
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, p.brand, p.sku,
		p.color, p.color_hex, p.country_of_origin, p.sale_percentage,
		p.top_category_id, p.sub_category_id, p.type_id,
		p.model_wears_height_cm, p.model_wears_size_id, p.hidden, p.target_gender,
		p.season, p.care_instructions, p.composition, p.thumbnail_id,
		p.secondary_thumbnail_id, p.version, p.collection, p.fit,
		m.full_size, m.full_size_width, m.full_size_height,
		m.thumbnail, m.thumbnail_width, m.thumbnail_height,
		m.compressed, m.compressed_width, m.compressed_height, m.blur_hash,
		sm.created_at AS secondary_thumbnail_created_at,
		sm.full_size AS secondary_full_size, sm.full_size_width AS secondary_full_size_width,
		sm.full_size_height AS secondary_full_size_height,
		sm.thumbnail AS secondary_thumbnail, sm.thumbnail_width AS secondary_thumbnail_width,
		sm.thumbnail_height AS secondary_thumbnail_height,
		sm.compressed AS secondary_compressed, sm.compressed_width AS secondary_compressed_width,
		sm.compressed_height AS secondary_compressed_height, sm.blur_hash AS secondary_blur_hash,
		COALESCE((SELECT SUM(COALESCE(ps.quantity, 0)) FROM product_size ps WHERE ps.product_id = p.id), 0) = 0 AS sold_out
	FROM product p
	JOIN media m ON p.thumbnail_id = m.id
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id` + priceJoin

	countQuery := `SELECT COUNT(DISTINCT p.id) FROM product p 
		JOIN media m ON p.thumbnail_id = m.id
		LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id` + priceJoin

	if len(whereClauses) > 0 {
		conditions := " WHERE " + strings.Join(whereClauses, " AND ")
		baseQuery += conditions
		countQuery += conditions
	}

	if len(sortFactors) > 0 {
		var orderFields []string
		for _, sf := range sortFactors {
			if sf == entity.Price && priceJoinRequired {
				orderFields = append(orderFields, "pp.price * (1 - COALESCE(p.sale_percentage, 0) / 100)")
			} else {
				orderFields = append(orderFields, string(sf))
			}
		}
		ordering := " ORDER BY " + strings.Join(orderFields, ", ")
		if orderFactor != "" {
			ordering += " " + string(orderFactor)
		} else {
			ordering += " " + string(entity.Ascending)
		}
		baseQuery += ordering
	}

	baseQuery += " LIMIT :limit OFFSET :offset"

	return baseQuery, countQuery
}
