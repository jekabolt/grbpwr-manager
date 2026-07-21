package product

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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

// styleCompositionSelect projects a style's displayed composition as LEGACY PLAIN TEXT ONLY (M1 fix).
// It used to COALESCE the structured style_composition rows in as a JSON array once any existed
// (P4-flyover M1, 04-MAZE-FLYOVER.md) — a data-triggered overload of one string field's wire shape,
// not version-gated: the day fibre-composition authoring or a backfill landed, this field would
// silently flip from free text to JSON for every affected style, in the public storefront API too. That
// JSON-transition is cancelled: composition is the legacy tech_card.composition column, always plain
// text. The structured projection lives in the separate styleCompositionEntriesSelect below, wired to
// its own typed field.
const styleCompositionSelect = `JSON_UNQUOTE(sty.composition)`

// styleCompositionEntriesSelect projects a style's structured fibre composition (S17/WS3-Ф5) as a JSON
// array of {fiber_code,name,percent} objects, for the typed composition_entries wire field (M1 fix) —
// the replacement for the JSON-in-string overload removed from styleCompositionSelect above. Selected
// alongside (never instead of) styleCompositionSelect. JSON_ARRAYAGG returns NULL over zero rows (a
// style with no derived/manual composition yet); the caller unmarshals a NULL/empty result into an
// empty slice.
const styleCompositionEntriesSelect = `(SELECT JSON_ARRAYAGG(JSON_OBJECT('fiber_code', sc.fiber_code, 'name', COALESCE(f.name, sc.fiber_code), 'percent', sc.percent))
		FROM style_composition sc LEFT JOIN fiber f ON f.code = sc.fiber_code
		WHERE sc.tech_card_id = sty.id)`

// lifecycleStatusFilter builds the ADMIN paged-list lifecycle WHERE clause from an explicit status set
// (task: honour the full statuses filter). It returns the clause plus the []int IN-args to bind under
// :lifecycleStatuses (nil when the clause is self-contained). An empty/nil set defaults to ACTIVE-only,
// matching the pre-filter admin default; invalid statuses (UNKNOWN or out-of-range) are dropped, and if
// that empties the set it also falls back to ACTIVE-only (fail-safe — a malformed filter never widens
// exposure). It NEVER emits storefront tier gating: that is storefront-only and lives in the caller's
// non-admin branch. []int (not []uint8) is used deliberately so sqlx.In expands it as an IN-list rather
// than treating a []byte as a single scalar.
func lifecycleStatusFilter(statuses []entity.ColorwayStatus) (string, []int) {
	seen := make(map[entity.ColorwayStatus]bool, len(statuses))
	valid := make([]int, 0, len(statuses))
	for _, st := range statuses {
		if st.IsValid() && !seen[st] {
			seen[st] = true
			valid = append(valid, int(st))
		}
	}
	if len(valid) == 0 {
		return "p.lifecycle_status = 2", nil
	}
	return "p.lifecycle_status IN (:lifecycleStatuses)", valid
}

// GetProductsPaged returns a paged list of products based on provided parameters. statuses is the
// ADMIN-only lifecycle-status filter (see lifecycleStatusFilter) and is consulted ONLY when showHidden is
// true (the admin path); the storefront path (showHidden=false) ignores it and always returns ACTIVE-only
// with tier gating.
func (s *Store) GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, statuses []entity.ColorwayStatus, showHidden bool) ([]entity.Colorway, int, error) {
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
		// Admin view: honour the explicit lifecycle-status filter (empty = ACTIVE-only default; a set may
		// include DRAFT/HIDDEN/ARCHIVED and combinations union). NO storefront tier gating is applied here
		// — the admin catalogue is never filtered by viewer tier. Storefront visibility logic lives solely
		// in the else branch below and must never leak into or widen the admin path.
		clause, statusArgs := lifecycleStatusFilter(statuses)
		whereClauses = append(whereClauses, clause)
		if statusArgs != nil {
			args["lifecycleStatuses"] = statusArgs
		}
	} else {
		// Storefront: only publicly-visible colourways — lifecycle_status ACTIVE(2) (R6). This excludes
		// HIDDEN(3) and ARCHIVED(4). The admin statuses filter is intentionally ignored on this path.
		whereClauses = append(whereClauses, "p.lifecycle_status = 2")

		viewerTier := int16(0)
		exclusive := false
		if filterConditions != nil {
			viewerTier = filterConditions.ViewerTier
			exclusive = filterConditions.Exclusive
		}
		args["viewerTier"] = viewerTier

		// Tier-locked TEASER visibility. Tier-gated rows are NO LONGER filtered out here — they are
		// RETURNED inline so the storefront can render them as locked teaser cards to everyone (guests
		// included). The per-viewer `locked` flag is applied later in the DTO projection from the SAME
		// predicate (entity.TierCanPurchase), so what is displayed as locked and what is enforced can
		// never diverge.
		//
		// The ONE thing still hidden here is hidden_for_non_qualified = TRUE: such a row must NEVER
		// reach a viewer who does not qualify for it (leak-proofing), so it is excluded in SQL — not in
		// the projection, which only runs after the row has already been returned. `qualifies` mirrors
		// TierCanPurchase exactly (hacker=99 requires viewerTier=99; any other min_tier requires
		// viewerTier >= min_tier). Net effect per row:
		//   - hidden_for_non_qualified = FALSE → always returned (locked teaser for non-qualifying
		//     viewers, unlocked for qualifying). This is also the hacker-99 rule: a non-hidden
		//     min_tier=99 row shows as a locked teaser to lower tiers and unlocked to hackers.
		//   - hidden_for_non_qualified = TRUE  → returned ONLY to a qualifying viewer; fully hidden
		//     from everyone else.
		qualifies := "((p.min_tier <= :viewerTier AND p.min_tier <> 99) OR (p.min_tier = 99 AND :viewerTier = 99))"
		whereClauses = append(whereClauses, "(p.hidden_for_non_qualified = 0 OR "+qualifies+")")

		// EXCLUSIVE catalogue (server-controlled flag): restrict to tier-gated items only (min_tier > 0)
		// so the dedicated exclusive listing shows only locked/gated teasers. This can only NARROW the
		// result set — the hidden_for_non_qualified exclusion above always applies — so it never
		// bypasses hiding even if the flag is client-triggered (see entity.FilterConditions.Exclusive).
		if exclusive {
			whereClauses = append(whereClauses, "p.min_tier > 0")
		}
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
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, COALESCE(sty.brand, '') AS brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, ` + styleCompositionSelect + ` AS composition, ` + styleCompositionEntriesSelect + ` AS composition_entries,
		p.thumbnail_id, p.secondary_thumbnail_id,
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
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, COALESCE(sty.brand, '') AS brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, ` + styleCompositionSelect + ` AS composition, ` + styleCompositionEntriesSelect + ` AS composition_entries,
		p.thumbnail_id, p.secondary_thumbnail_id,
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
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, COALESCE(sty.brand, '') AS brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, ` + styleCompositionSelect + ` AS composition, ` + styleCompositionEntriesSelect + ` AS composition_entries,
		p.thumbnail_id, p.secondary_thumbnail_id,
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

func (s *Store) getProductDetails(ctx context.Context, filters map[string]any, showHidden bool, includeArchived bool) (*entity.ColorwayFull, error) {
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
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, COALESCE(sty.brand, '') AS brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.care_instructions, `+styleCompositionSelect+` AS composition, `+styleCompositionEntriesSelect+` AS composition_entries,
		p.thumbnail_id, p.secondary_thumbnail_id,
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
	LEFT JOIN media m ON p.thumbnail_id = m.id
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id
	WHERE %s`, strings.Join(whereClauses, " AND "))

	// Lifecycle filter (R6): storefront sees only ACTIVE(2) — which excludes both HIDDEN(3) and
	// ARCHIVED(4). The admin (showHidden) sees everything except ARCHIVED(4) — including drafts and
	// hidden colourways — UNLESS includeArchived is set, in which case an ARCHIVED colourway is also
	// returned (admin read-only detail of an archived colourway). includeArchived is admin-only and must
	// never be reachable from a storefront caller (showHidden is always false on the storefront path).
	if !showHidden {
		query += " AND p.lifecycle_status = 2"
	} else if !includeArchived {
		query += " AND p.lifecycle_status <> 4"
	}

	type productDetailsResult struct {
		productQueryResult
		// NULL when the colourway has no thumbnail (DRAFT, LEFT JOIN media miss) — see the LEFT JOIN above.
		ThumbnailCreatedAt sql.NullTime `db:"thumbnail_created_at"`
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
	if prdResult.ThumbnailCreatedAt.Valid {
		product.ProductDisplay.Thumbnail.CreatedAt = prdResult.ThumbnailCreatedAt.Time
	}
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
		p.id, p.created_at, p.updated_at, p.deleted_at, p.preorder, COALESCE(sty.brand, '') AS brand, COALESCE(p.sku, '') AS sku,
		p.color, p.color_code, p.color_hex, p.country_of_origin, p.sale_percentage,
		sty.top_category_id, sty.sub_category_id, sty.type_id,
		sty.model_wears_height_cm, sty.model_wears_size_id, sty.target_gender,
		sty.season_code AS season, sty.care_instructions, ` + styleCompositionSelect + ` AS composition, ` + styleCompositionEntriesSelect + ` AS composition_entries,
		p.thumbnail_id,
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
	LEFT JOIN media m ON p.thumbnail_id = m.id
	LEFT JOIN media sm ON p.secondary_thumbnail_id = sm.id` + priceJoin

	countQuery := `SELECT COUNT(DISTINCT p.id) FROM product p
	JOIN tech_card sty ON sty.id = p.style_id
		LEFT JOIN media m ON p.thumbnail_id = m.id
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
