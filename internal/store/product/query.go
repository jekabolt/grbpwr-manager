package product

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
)

// productStockExpr is the single SQL scalar for a product's total available stock: the sum of its
// sizes' quantities (0 when it has none), correlated on p.id. soldOutSelect derives the sold_out
// flag from it. Both are reused across every product list/detail/low-stock query so the definition
// can't drift; the Go equivalent for locally-loaded sizes is entity.SoldOutFromSizes (PR5-B).
const productStockExpr = `COALESCE((SELECT SUM(COALESCE(ps.quantity, 0)) FROM product_size ps WHERE ps.product_id = p.id), 0)`

// soldOutSelect is the sold_out projection: a product is sold out when its total stock is <= 0 (50-B).
// Uses <=, not just = 0, to agree with entity.SoldOutFromSizes: anomalous data (e.g. a negative total
// from an oversell race/bug) must still read as sold out on both the SQL and Go paths, not diverge
// into "not sold out" here while Go says otherwise.
const soldOutSelect = productStockExpr + ` <= 0 AS sold_out`

// styleCompositionSelect projects a style's displayed composition (P4-flyover M1, 04-MAZE-FLYOVER.md):
// the structured style_composition rows (S17/WS3-Ф5), rendered as a JSON array of
// {fiber_code,name,percent} objects, take priority over the legacy free-text tech_card.composition
// column. JSON_ARRAYAGG returns NULL over zero rows, so COALESCE falls through to the legacy value
// automatically for a style with no derived/manual composition yet — the legacy column is the
// fallback, not dropped here (that is a later guarded M3, after every style is backfilled).
const styleCompositionSelect = `COALESCE(
		(SELECT JSON_ARRAYAGG(JSON_OBJECT('fiber_code', sc.fiber_code, 'name', COALESCE(f.name, sc.fiber_code), 'percent', sc.percent))
		 FROM style_composition sc LEFT JOIN fiber f ON f.code = sc.fiber_code
		 WHERE sc.tech_card_id = sty.id),
		sty.composition
	)`

// GetProductsPaged returns a paged list of products based on provided parameters.
func (s *Store) GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, showHidden bool) ([]entity.Colorway, int, error) {
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

	if showHidden {
		// Admin view: everything except ARCHIVED(4) — draft and hidden colourways are still shown (R6).
		whereClauses = append(whereClauses, "p.lifecycle_status <> 4")
	} else {
		// Storefront: only publicly-visible colourways — lifecycle_status ACTIVE(2) (R6).
		whereClauses = append(whereClauses, "p.lifecycle_status = 2")

		// Tier gating: return ONLY products the viewer is eligible to buy for
		// their tier (resolved from the auth token; 0 for guests). Hacker-only
		// items (min_tier=99) are shown exclusively to hacker accounts.
		viewerTier := int16(0)
		if filterConditions != nil {
			viewerTier = filterConditions.ViewerTier
		}
		whereClauses = append(whereClauses, `(
			(p.min_tier <= :viewerTier AND p.min_tier <> 99)
			OR (p.min_tier = 99 AND :viewerTier = 99)
		)`)
		args["viewerTier"] = viewerTier
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
			whereClauses = append(whereClauses, "sty.target_gender IN (:targetGenders)")
			genders := make([]string, len(filterConditions.Gender))
			for i, g := range filterConditions.Gender {
				genders[i] = string(g)
			}
			args["targetGenders"] = genders
		}
		if len(filterConditions.ColorCodes) > 0 {
			whereClauses = append(whereClauses, "p.color_code IN (:colorCodes)")
			args["colorCodes"] = filterConditions.ColorCodes
		}
		if len(filterConditions.TopCategoryIds) != 0 {
			whereClauses = append(whereClauses, "sty.top_category_id IN (:topCategoryIds)")
			args["topCategoryIds"] = filterConditions.TopCategoryIds
		}
		if len(filterConditions.ExcludeTopCategoryIds) != 0 {
			whereClauses = append(whereClauses, "sty.top_category_id NOT IN (:excludeTopCategoryIds)")
			args["excludeTopCategoryIds"] = filterConditions.ExcludeTopCategoryIds
		}
		if len(filterConditions.SubCategoryIds) != 0 {
			whereClauses = append(whereClauses, "sty.sub_category_id IN (:subCategoryIds)")
			args["subCategoryIds"] = filterConditions.SubCategoryIds
		}
		if len(filterConditions.TypeIds) != 0 {
			whereClauses = append(whereClauses, "sty.type_id IN (:typeIds)")
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
			whereClauses = append(whereClauses, "sty.collection IN (:collections)")
			args["collections"] = filterConditions.Collections
		}
		if len(filterConditions.Seasons) != 0 {
			seasons := make([]string, len(filterConditions.Seasons))
			for i, ss := range filterConditions.Seasons {
				seasons[i] = string(ss)
			}
			whereClauses = append(whereClauses, "sty.season_code IN (:seasons)")
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

	prds := make([]entity.Colorway, 0, len(prdResults))
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		product.Prices = priceMap[prdResult.Id]
		prds = append(prds, product)
	}

	return prds, count, nil
}

// GetProductsByIds returns a list of products by their IDs.
func (s *Store) GetProductsByIds(ctx context.Context, ids []int) ([]entity.Colorway, error) {
	if len(ids) == 0 {
		return []entity.Colorway{}, nil
	}

	query := `
	SELECT 
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, sty.brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, ` + styleCompositionSelect + ` AS composition, p.thumbnail_id, p.secondary_thumbnail_id,
		sty.collection, sty.fit, p.min_tier, p.hidden_for_non_qualified, p.lifecycle_status, p.style_id,
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
		` + soldOutSelect + `
	FROM product p
	JOIN tech_card sty ON sty.id = p.style_id
	JOIN media m ON p.thumbnail_id = m.id 
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id 
	WHERE p.id IN (:ids) AND p.lifecycle_status = 2`

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

	prdMap := make(map[int]entity.Colorway)
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		product.Prices = priceMap[prdResult.Id]
		prdMap[product.Id] = product
	}

	result := make([]entity.Colorway, 0, len(ids))
	for _, id := range ids {
		if p, ok := prdMap[id]; ok {
			result = append(result, p)
		}
	}

	return result, nil
}

// GetLowStockProducts returns visible, non-deleted products whose total stock
// is in the range (0, threshold], ordered by ascending stock (closest to
// selling out first), limited to `limit`.
func (s *Store) GetLowStockProducts(ctx context.Context, threshold int, limit int) ([]entity.Colorway, error) {
	if threshold <= 0 {
		threshold = 3
	}
	if limit <= 0 {
		limit = 8
	}

	query := `
	SELECT
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, sty.brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, ` + styleCompositionSelect + ` AS composition, p.thumbnail_id, p.secondary_thumbnail_id,
		sty.collection, sty.fit, p.min_tier, p.hidden_for_non_qualified, p.lifecycle_status, p.style_id,
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
		` + soldOutSelect + `
	FROM product p
	JOIN tech_card sty ON sty.id = p.style_id
	JOIN media m ON p.thumbnail_id = m.id
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id
	WHERE p.lifecycle_status = 2
		AND ` + productStockExpr + ` BETWEEN 1 AND :threshold
	ORDER BY ` + productStockExpr + ` ASC
	LIMIT :limit`

	prdResults, err := storeutil.QueryListNamed[productQueryResult](ctx, s.DB, query, map[string]any{
		"threshold": threshold,
		"limit":     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get low stock products: %w", err)
	}
	if len(prdResults) == 0 {
		return []entity.Colorway{}, nil
	}

	ids := make([]int, 0, len(prdResults))
	for _, r := range prdResults {
		ids = append(ids, r.Id)
	}

	translationMap, err := fetchProductTranslations(ctx, s.DB, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	priceMap, err := fetchProductPrices(ctx, s.DB, ids)
	if err != nil {
		return nil, fmt.Errorf("can't get product prices: %w", err)
	}

	result := make([]entity.Colorway, 0, len(prdResults))
	for _, r := range prdResults {
		product := r.toProduct(translationMap[r.Id])
		product.Prices = priceMap[r.Id]
		result = append(result, product)
	}

	return result, nil
}

// GetProductsByTag returns a list of products by their tag.
func (s *Store) GetProductsByTag(ctx context.Context, tag string) ([]entity.Colorway, error) {
	if tag == "" {
		return []entity.Colorway{}, nil
	}

	query := `
	SELECT 
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, sty.brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, ` + styleCompositionSelect + ` AS composition, p.thumbnail_id, p.secondary_thumbnail_id,
		sty.collection, sty.fit, p.min_tier, p.hidden_for_non_qualified, p.lifecycle_status, p.style_id,
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
		` + soldOutSelect + `
	FROM product p
	JOIN tech_card sty ON sty.id = p.style_id
	JOIN media m ON p.thumbnail_id = m.id 
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id 
	WHERE p.id IN (SELECT ptag.product_id FROM product_tag ptag WHERE ptag.tag = :tag) AND p.lifecycle_status = 2`

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

	prds := make([]entity.Colorway, 0, len(prdResults))
	for _, prdResult := range prdResults {
		translations := translationMap[prdResult.Id]
		product := prdResult.toProduct(translations)
		product.Prices = priceMap[prdResult.Id]
		prds = append(prds, product)
	}

	return prds, nil
}

func (s *Store) getProductDetails(ctx context.Context, filters map[string]any, showHidden bool) (*entity.ColorwayFull, error) {
	var productInfo entity.ColorwayFull

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
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, sty.brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, `+styleCompositionSelect+` AS composition, p.thumbnail_id, p.secondary_thumbnail_id,
		sty.collection, sty.season_code AS season, sty.fit, p.min_tier, p.hidden_for_non_qualified, p.lifecycle_status, p.style_id, p.published_at,
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
	JOIN tech_card sty ON sty.id = p.style_id
	JOIN media m ON p.thumbnail_id = m.id
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id
	WHERE %s`, strings.Join(whereClauses, " AND "))

	// Lifecycle filter (R6): storefront sees only ACTIVE(2); the admin (showHidden) sees everything
	// except ARCHIVED(4) — including drafts and hidden colourways.
	if showHidden {
		query += " AND p.lifecycle_status <> 4"
	} else {
		query += " AND p.lifecycle_status = 2"
	}

	type productDetailsResult struct {
		productQueryResult
		ThumbnailCreatedAt time.Time `db:"thumbnail_created_at"`
	}

	prdResult, err := storeutil.QueryNamedOne[productDetailsResult](ctx, s.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get product: %w", err)
	}

	// Queries 2–7 all key on the product id from the row above and are mutually
	// independent (no transaction, nothing threaded between them — SoldOut is
	// computed locally in Go). Run them concurrently so PDP latency is roughly one
	// round-trip instead of the sum of six. sqlx's *DB is safe for concurrent use.
	pid := prdResult.Id
	idParams := map[string]any{"id": pid}

	mediaQuery := `
		SELECT m.id, m.created_at, m.full_size, m.full_size_width, m.full_size_height,
			m.thumbnail, m.thumbnail_width, m.thumbnail_height,
			m.compressed, m.compressed_width, m.compressed_height, m.blur_hash
		FROM media m
		INNER JOIN product_media pm ON m.id = pm.media_id
		WHERE pm.product_id = :id
		ORDER BY pm.id;
	`

	var (
		translationMap map[int][]entity.ColorwayTranslationInsert
		priceMap       map[int][]entity.ColorwayPrice
		sizes          []entity.Variant
		measurements   []entity.ProductMeasurement
		media          []entity.MediaFull
		tags           []entity.ColorwayTag
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	g.Go(func() error {
		var e error
		if translationMap, e = fetchProductTranslations(gctx, s.DB, []int{pid}); e != nil {
			return fmt.Errorf("can't get product translations: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		if priceMap, e = fetchProductPrices(gctx, s.DB, []int{pid}); e != nil {
			return fmt.Errorf("can't get prices: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		if sizes, e = storeutil.QueryListNamed[entity.Variant](gctx, s.DB, `SELECT * FROM product_size WHERE product_id = :id`, idParams); e != nil {
			return fmt.Errorf("can't get sizes: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		// The size chart lives on the style now (PR6 P3): reconstruct the per-colourway view by joining
		// the style's chart to this product's sizes, preserving the product_size_id the frontend keys on.
		measurementQuery := `
			SELECT ssm.id AS id, p.id AS product_id, ps.id AS product_size_id,
			       ssm.measurement_name_id, ssm.measurement_value
			FROM tech_card_size_measurement ssm
			JOIN product p ON p.id = :id AND ssm.tech_card_id = p.style_id
			JOIN product_size ps ON ps.product_id = p.id AND ps.size_id = ssm.size_id`
		if measurements, e = storeutil.QueryListNamed[entity.ProductMeasurement](gctx, s.DB, measurementQuery, idParams); e != nil {
			return fmt.Errorf("can't get measurements: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		if media, e = storeutil.QueryListNamed[entity.MediaFull](gctx, s.DB, mediaQuery, idParams); e != nil {
			return fmt.Errorf("can't get media: %w", e)
		}
		return nil
	})
	g.Go(func() error {
		var e error
		if tags, e = storeutil.QueryListNamed[entity.ColorwayTag](gctx, s.DB, `SELECT * FROM product_tag WHERE product_id = :id`, idParams); e != nil {
			return fmt.Errorf("can't get tags: %w", e)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	product := prdResult.toProduct(translationMap[pid])
	product.ProductDisplay.Thumbnail.CreatedAt = prdResult.ThumbnailCreatedAt
	if product.ProductDisplay.SecondaryThumbnail != nil && prdResult.SecondaryThumbnailCreatedAt.Valid {
		product.ProductDisplay.SecondaryThumbnail.CreatedAt = prdResult.SecondaryThumbnailCreatedAt.Time
	}
	if prices, ok := priceMap[pid]; ok {
		product.Prices = prices
	} else {
		product.Prices = []entity.ColorwayPrice{}
	}

	product.SoldOut = entity.SoldOutFromSizes(sizes)

	productInfo.Product = &product
	productInfo.Sizes = sizes
	productInfo.Measurements = measurements
	productInfo.Media = media
	productInfo.Tags = tags
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
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, sty.brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.season_code AS season, sty.care_instructions, ` + styleCompositionSelect + ` AS composition, p.thumbnail_id,
		p.secondary_thumbnail_id, sty.collection, sty.fit, p.min_tier, p.hidden_for_non_qualified, p.lifecycle_status, p.style_id,
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
		` + soldOutSelect + `
	FROM product p
	JOIN tech_card sty ON sty.id = p.style_id
	JOIN media m ON p.thumbnail_id = m.id
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id` + priceJoin

	countQuery := `SELECT COUNT(DISTINCT p.id) FROM product p
	JOIN tech_card sty ON sty.id = p.style_id 
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
