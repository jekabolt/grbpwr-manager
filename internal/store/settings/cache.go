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

	return &dict, nil
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
			SELECT DISTINCT p.top_category_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.deleted_at IS NULL AND p.target_gender IN (:gender, 'unisex')
			
			UNION
			
			SELECT DISTINCT p.sub_category_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.deleted_at IS NULL AND p.target_gender IN (:gender, 'unisex') AND p.sub_category_id IS NOT NULL
			
			UNION
			
			SELECT DISTINCT p.type_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.deleted_at IS NULL AND p.target_gender IN (:gender, 'unisex') AND p.type_id IS NOT NULL
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
			WHERE p.hidden = 0 AND p.deleted_at IS NULL AND p.target_gender IN (:gender, 'unisex')
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

func (s *Store) getCollections(ctx context.Context) ([]entity.Collection, error) {
	query := `
		SELECT DISTINCT 
			p.collection as name
		FROM product p
		WHERE p.collection IS NOT NULL 
			AND p.collection != ''
			AND p.hidden = 0 
			AND p.deleted_at IS NULL
		ORDER BY p.collection
	`

	type collectionResult struct {
		Name string `db:"name"`
	}

	results, err := storeutil.QueryListNamed[collectionResult](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get collections: %w", err)
	}

	collectionMenCounts, err := s.getCollectionCountsByGender(ctx, entity.Male)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	collectionWomenCounts, err := s.getCollectionCountsByGender(ctx, entity.Female)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	collections := make([]entity.Collection, 0, len(results))
	for _, result := range results {
		collection := entity.Collection{
			Name: result.Name,
		}

		if count, exists := collectionMenCounts[result.Name]; exists {
			collection.CountMen = count
		}
		if count, exists := collectionWomenCounts[result.Name]; exists {
			collection.CountWomen = count
		}

		collections = append(collections, collection)
	}

	return collections, nil
}

func (s *Store) getCollectionCountsByGender(ctx context.Context, gender entity.GenderEnum) (map[string]int, error) {
	query := `
		SELECT 
			p.collection as name, 
			COUNT(DISTINCT p.id) as count
		FROM product p
		WHERE p.collection IS NOT NULL 
			AND p.collection != ''
			AND p.hidden = 0 
			AND p.deleted_at IS NULL 
			AND p.target_gender IN (:gender, 'unisex')
		GROUP BY p.collection
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
