package settings

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetDictionaryInfo returns dictionary info with all translations.
// Uses repFunc to access cross-domain data (Language, Settings).
func (s *Store) GetDictionaryInfo(ctx context.Context) (*entity.DictionaryInfo, error) {
	var dict entity.DictionaryInfo
	var err error

	if dict.Categories, err = s.getCategories(ctx); err != nil {
		return nil, fmt.Errorf("failed to get categories: %w", err)
	}

	if dict.Measurements, err = s.getMeasurements(ctx); err != nil {
		return nil, fmt.Errorf("failed to get measurements: %w", err)
	}

	if dict.PaymentMethods, err = s.getPaymentMethod(ctx); err != nil {
		return nil, fmt.Errorf("failed to get payment methods: %w", err)
	}

	if dict.OrderStatuses, err = s.getOrderStatuses(ctx); err != nil {
		return nil, fmt.Errorf("failed to get order statuses: %w", err)
	}

	if dict.Promos, err = s.getPromos(ctx); err != nil {
		return nil, fmt.Errorf("failed to get promos: %w", err)
	}

	if dict.ShipmentCarriers, err = getShipmentCarriers(ctx, s.DB); err != nil {
		return nil, fmt.Errorf("failed to get shipment carriers: %w", err)
	}

	if dict.Sizes, err = s.getSizes(ctx); err != nil {
		return nil, fmt.Errorf("failed to get sizes: %w", err)
	}

	if dict.Collections, err = s.getCollections(ctx); err != nil {
		return nil, fmt.Errorf("failed to get collections: %w", err)
	}

	rep := s.repFunc()
	if dict.Languages, err = rep.Language().GetAllLanguages(ctx); err != nil {
		return nil, fmt.Errorf("failed to get languages: %w", err)
	}

	dict.Announce, err = s.GetAnnounce(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get announce: %w", err)
	}

	dict.ComplimentaryShippingPrices, err = s.GetComplimentaryShippingPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get complimentary shipping prices: %w", err)
	}

	dict.BackgroundHeroColor, err = s.GetBackgroundHeroColor(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get background hero color: %w", err)
	}

	if dict.ProductTags, err = s.getProductTags(ctx); err != nil {
		return nil, fmt.Errorf("failed to get product tags: %w", err)
	}

	if dict.Colors, err = s.getColors(ctx); err != nil {
		return nil, fmt.Errorf("failed to get colors: %w", err)
	}

	if dict.Tags, err = s.getTags(ctx); err != nil {
		return nil, fmt.Errorf("failed to get tags: %w", err)
	}

	if dict.CategorySizeSystems, err = s.getCategorySizeSystems(ctx); err != nil {
		return nil, fmt.Errorf("failed to get category size systems: %w", err)
	}

	if dict.Fibers, err = s.getFibers(ctx); err != nil {
		return nil, fmt.Errorf("failed to get fibers: %w", err)
	}

	return &dict, nil
}

// getFibers returns the controlled fibre vocabulary (S17/P0.4), ordered by code. Archived entries are
// included (flagged via archived_at) so the admin composition editor can show/filter them client-side,
// mirroring how the colour dictionary surfaces its full set to the picker.
func (s *Store) getFibers(ctx context.Context) ([]entity.Fiber, error) {
	query := `SELECT code, name, archived_at FROM fiber ORDER BY code`
	fibers, err := storeutil.QueryListNamed[entity.Fiber](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get fibers: %w", err)
	}
	return fibers, nil
}

// getTags returns the controlled merchandising tag dictionary (R9) from the `tag` table, ordered by
// code. Archived entries are included (flagged via archived_at) so the admin tag picker can show/filter
// them, mirroring getColors/getFibers. This is what makes a freshly created tag (CreateTag) visible in
// GetDictionary immediately — the usage-derived getProductTags only lists tags on published products.
func (s *Store) getTags(ctx context.Context) ([]entity.TagDict, error) {
	query := `SELECT id, code, name, archived_at FROM tag ORDER BY code`
	tags, err := storeutil.QueryListNamed[entity.TagDict](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get tags: %w", err)
	}
	return tags, nil
}

// getCategorySizeSystems returns the category -> permitted size-system(s) mapping (S10/WS5, migration
// 0175): the same read backs the admin dictionary (size picker) and, via the cache, server-side
// size-write validation (entity.ResolveSizeSystemPolicy).
func (s *Store) getCategorySizeSystems(ctx context.Context) ([]entity.CategorySizeSystem, error) {
	query := `SELECT id, category_id, type_id, size_system FROM category_size_system`
	rows, err := storeutil.QueryListNamed[entity.CategorySizeSystem](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get category size systems: %w", err)
	}
	return rows, nil
}

// getColors returns the controlled colour dictionary, ordered by code.
func (s *Store) getColors(ctx context.Context) ([]entity.Color, error) {
	query := `SELECT id, code, name, hex FROM color ORDER BY code`
	colors, err := storeutil.QueryListNamed[entity.Color](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get colors: %w", err)
	}
	return colors, nil
}

// getProductTags returns every distinct tag attached to a visible (non-hidden,
// non-deleted) product, ordered alphabetically. Mirrors getCollections.
func (s *Store) getProductTags(ctx context.Context) ([]string, error) {
	query := `
		SELECT DISTINCT pt.tag AS tag
		FROM product_tag pt
		JOIN product p ON pt.product_id = p.id
		WHERE p.lifecycle_status = 2
			AND pt.tag != ''
		ORDER BY pt.tag
	`

	type tagResult struct {
		Tag string `db:"tag"`
	}

	results, err := storeutil.QueryListNamed[tagResult](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get product tags: %w", err)
	}

	tags := make([]string, 0, len(results))
	for _, r := range results {
		tags = append(tags, r.Tag)
	}
	return tags, nil
}

func (s *Store) getCategories(ctx context.Context) ([]entity.Category, error) {
	query := `
        SELECT 
            c.id as category_id,
            c.name as category_name,
            c.level_id, 
            cl.name as level_name, 
            c.parent_id
        FROM category c
        JOIN category_level cl ON c.level_id = cl.id
    `
	categories, err := storeutil.QueryListNamed[entity.Category](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get categories: %w", err)
	}

	categoryMenCounts, err := s.getCategoriesCountsByGender(ctx, entity.Male)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	categoryWomenCounts, err := s.getCategoriesCountsByGender(ctx, entity.Female)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	for i := range categories {
		if count, exists := categoryMenCounts[categories[i].ID]; exists {
			categories[i].CountMen = count
		}
		if count, exists := categoryWomenCounts[categories[i].ID]; exists {
			categories[i].CountWomen = count
		}
	}

	return categories, nil
}

func (s *Store) getMeasurements(ctx context.Context) ([]entity.MeasurementName, error) {
	query := `
		SELECT 
			mn.id,
			mn.name
		FROM measurement_name mn
	`
	measurements, err := storeutil.QueryListNamed[entity.MeasurementName](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get measurements: %w", err)
	}

	return measurements, nil
}

func (s *Store) getPaymentMethod(ctx context.Context) ([]entity.PaymentMethod, error) {
	query := `SELECT * FROM payment_method`
	paymentMethods, err := storeutil.QueryListNamed[entity.PaymentMethod](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return paymentMethods, nil
}

func (s *Store) getOrderStatuses(ctx context.Context) ([]entity.OrderStatus, error) {
	query := `SELECT * FROM order_status`
	orderStatuses, err := storeutil.QueryListNamed[entity.OrderStatus](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return orderStatuses, nil
}

func (s *Store) getCategoriesCountsByGender(ctx context.Context, gender entity.GenderEnum) (map[int]int, error) {
	query := `
		SELECT 
			category_id, 
			COUNT(*) as count
		FROM (
			SELECT DISTINCT sty.top_category_id as category_id, p.id as product_id
			FROM product p
		JOIN tech_card sty ON sty.id = p.style_id 
			WHERE p.lifecycle_status = 2 AND sty.target_gender IN (:gender, 'unisex')
			
			UNION
			
			SELECT DISTINCT sty.sub_category_id as category_id, p.id as product_id
			FROM product p
		JOIN tech_card sty ON sty.id = p.style_id 
			WHERE p.lifecycle_status = 2 AND sty.target_gender IN (:gender, 'unisex') AND sty.sub_category_id IS NOT NULL
			
			UNION
			
			SELECT DISTINCT sty.type_id as category_id, p.id as product_id
			FROM product p
		JOIN tech_card sty ON sty.id = p.style_id 
			WHERE p.lifecycle_status = 2 AND sty.target_gender IN (:gender, 'unisex') AND sty.type_id IS NOT NULL
		) AS category_products
		GROUP BY category_id
	`

	type categoryCount struct {
		CategoryID int `db:"category_id"`
		Count      int `db:"count"`
	}

	results, err := storeutil.QueryListNamed[categoryCount](ctx, s.DB, query, map[string]any{
		"gender": gender.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("can't get %s counts by category: %w", gender, err)
	}

	counts := make(map[int]int)
	for _, result := range results {
		counts[result.CategoryID] = result.Count
	}

	return counts, nil
}

func (s *Store) getPromos(ctx context.Context) ([]entity.PromoCode, error) {
	query := `SELECT * FROM promo_code`
	promos, err := storeutil.QueryListNamed[entity.PromoCode](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return promos, nil
}

func (s *Store) getSizes(ctx context.Context) ([]entity.Size, error) {
	query := `SELECT * FROM size`
	sizes, err := storeutil.QueryListNamed[entity.Size](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get size by id: %w", err)
	}

	sizeMenCounts, err := s.getSizeCountsByGender(ctx, entity.Male)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	sizeWomenCounts, err := s.getSizeCountsByGender(ctx, entity.Female)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	for i := range sizes {
		if count, exists := sizeMenCounts[sizes[i].Id]; exists {
			sizes[i].CountMen = count
		}
		if count, exists := sizeWomenCounts[sizes[i].Id]; exists {
			sizes[i].CountWomen = count
		}
	}

	return sizes, nil
}

func (s *Store) getSizeCountsByGender(ctx context.Context, gender entity.GenderEnum) (map[int]int, error) {
	query := `
		SELECT 
			size_id, 
			COUNT(*) as count
		FROM (
			SELECT DISTINCT ps.size_id, p.id as product_id
			FROM product_size ps
			JOIN product p ON ps.product_id = p.id
		JOIN tech_card sty ON sty.id = p.style_id
			WHERE p.lifecycle_status = 2 AND sty.target_gender IN (:gender, 'unisex')
		) AS size_products
		GROUP BY size_id
	`

	type sizeCount struct {
		SizeID int `db:"size_id"`
		Count  int `db:"count"`
	}

	results, err := storeutil.QueryListNamed[sizeCount](ctx, s.DB, query, map[string]any{
		"gender": gender.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("can't get %s counts by size: %w", gender, err)
	}

	counts := make(map[int]int)
	for _, result := range results {
		counts[result.SizeID] = result.Count
	}

	return counts, nil
}

// getCollections returns the controlled collection dictionary (R9) from the `collection` table,
// ordered by name, with each entry's published-product counts per gender attached.
//
// The dictionary table is the SOURCE of the list, not the products. This used to read
// SELECT DISTINCT tech_card.collection FROM published products, which meant a collection created in
// the admin dictionary was invisible everywhere GetDictionary is read -- the dictionary screen that
// had just created it, and the tech-card collection picker -- until some product shipped under that
// name. Same bug getTags was fixed for, and the same shape of fix.
//
// Archived entries are returned flagged (never filtered here) so a style still holding an archived
// collection keeps rendering its label; the picker filters them out for NEW selections.
//
// Legacy free-text names carried on a style but absent from the dictionary are unioned in, keyed by
// name with a zero ID -- dropping them would silently break the storefront filters for collections
// that predate 0154's backfill or were written straight to tech_card.collection.
func (s *Store) getCollections(ctx context.Context) ([]entity.Collection, error) {
	dictRows, err := storeutil.QueryListNamed[entity.CollectionDict](ctx, s.DB,
		`SELECT id, code, name, translations, archived_at FROM collection ORDER BY name`,
		map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get collections: %w", err)
	}

	type collectionResult struct {
		Name string `db:"name"`
	}
	legacy, err := storeutil.QueryListNamed[collectionResult](ctx, s.DB, `
		SELECT DISTINCT
			sty.collection as name
		FROM product p
		JOIN tech_card sty ON sty.id = p.style_id
		WHERE sty.collection IS NOT NULL
			AND sty.collection != ''
			AND p.lifecycle_status = 2
		ORDER BY sty.collection
	`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get legacy collection names: %w", err)
	}

	collectionMenCounts, err := s.getCollectionCountsByGender(ctx, entity.Male)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	collectionWomenCounts, err := s.getCollectionCountsByGender(ctx, entity.Female)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	collections := make([]entity.Collection, 0, len(dictRows)+len(legacy))
	seen := make(map[string]bool, len(dictRows))
	appendCollection := func(c entity.Collection) {
		if seen[c.Name] {
			return
		}
		seen[c.Name] = true
		c.CountMen = collectionMenCounts[c.Name]
		c.CountWomen = collectionWomenCounts[c.Name]
		collections = append(collections, c)
	}

	for _, d := range dictRows {
		appendCollection(entity.Collection{
			ID:       d.ID,
			Code:     d.Code,
			Name:     d.Name,
			Archived: d.ArchivedAt.Valid,
		})
	}
	for _, l := range legacy {
		appendCollection(entity.Collection{Name: l.Name})
	}

	return collections, nil
}

func (s *Store) getCollectionCountsByGender(ctx context.Context, gender entity.GenderEnum) (map[string]int, error) {
	query := `
		SELECT 
			sty.collection as name, 
			COUNT(DISTINCT p.id) as count
		FROM product p
		JOIN tech_card sty ON sty.id = p.style_id
		WHERE sty.collection IS NOT NULL 
			AND sty.collection != ''
			AND p.lifecycle_status = 2
			AND sty.target_gender IN (:gender, 'unisex')
		GROUP BY sty.collection
	`

	type collectionCount struct {
		Name  string `db:"name"`
		Count int    `db:"count"`
	}

	results, err := storeutil.QueryListNamed[collectionCount](ctx, s.DB, query, map[string]any{
		"gender": gender.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("can't get %s counts by collection: %w", gender, err)
	}

	counts := make(map[string]int)
	for _, result := range results {
		counts[result.Name] = result.Count
	}

	return counts, nil
}
