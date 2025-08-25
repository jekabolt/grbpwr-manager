package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type cacheStore struct {
	*MYSQLStore
}

// Hero returns an object implementing hero interface
func (ms *MYSQLStore) Cache() dependency.Cache {
	return &cacheStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) GetDictionaryInfo(ctx context.Context) (*entity.DictionaryInfo, error) {
	var dict entity.DictionaryInfo
	var err error

	if dict.Categories, err = ms.getCategories(ctx); err != nil {
		return nil, fmt.Errorf("failed to get categories: %w", err)
	}

	if dict.Measurements, err = ms.getMeasurements(ctx); err != nil {
		return nil, fmt.Errorf("failed to get measurements: %w", err)
	}

	if dict.PaymentMethods, err = ms.getPaymentMethod(ctx); err != nil {
		return nil, fmt.Errorf("failed to get payment methods: %w", err)
	}

	if dict.OrderStatuses, err = ms.getOrderStatuses(ctx); err != nil {
		return nil, fmt.Errorf("failed to get order statuses: %w", err)
	}

	if dict.Promos, err = ms.getPromos(ctx); err != nil {
		return nil, fmt.Errorf("failed to get promos: %w", err)
	}

	if dict.ShipmentCarriers, err = ms.getShipmentCarriers(ctx); err != nil {
		return nil, fmt.Errorf("failed to get shipment carriers: %w", err)
	}

	if dict.Sizes, err = ms.getSizes(ctx); err != nil {
		return nil, fmt.Errorf("failed to get sizes: %w", err)
	}

	return &dict, nil
}

// Existing methods for fetching individual entities remain the same
func (ms *MYSQLStore) getCategories(ctx context.Context) ([]entity.Category, error) {
	query := `
        SELECT category_id, category_name, level_id, level_name, parent_id FROM view_categories
    `
	categories, err := QueryListNamed[entity.Category](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get Category by id: %w", err)
	}

	// Get category counts efficiently in bulk
	categoryMenCounts, err := ms.getCategoriesMenCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	categoryWomenCounts, err := ms.getCategoriesWomenCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	// Map counts to categories
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

func (ms *MYSQLStore) getMeasurements(ctx context.Context) ([]entity.MeasurementName, error) {
	query := `SELECT * FROM measurement_name`
	measurements, err := QueryListNamed[entity.MeasurementName](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get MeasurementName by id: %w", err)
	}
	return measurements, nil
}

func (ms *MYSQLStore) getPaymentMethod(ctx context.Context) ([]entity.PaymentMethod, error) {
	query := `SELECT * FROM payment_method`
	paymentMethods, err := QueryListNamed[entity.PaymentMethod](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return paymentMethods, nil
}

func (ms *MYSQLStore) getOrderStatuses(ctx context.Context) ([]entity.OrderStatus, error) {
	query := `SELECT * FROM order_status`
	orderStatuses, err := QueryListNamed[entity.OrderStatus](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return orderStatuses, nil
}

// getCategoriesMenCounts returns a map of category ID to men's product count
func (ms *MYSQLStore) getCategoriesMenCounts(ctx context.Context) (map[int]int, error) {
	query := `
		SELECT 
			category_id, 
			COUNT(*) as count
		FROM (
			SELECT DISTINCT p.top_category_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.target_gender IN ('male', 'unisex')
			
			UNION
			
			SELECT DISTINCT p.sub_category_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.target_gender IN ('male', 'unisex') AND p.sub_category_id IS NOT NULL
			
			UNION
			
			SELECT DISTINCT p.type_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.target_gender IN ('male', 'unisex') AND p.type_id IS NOT NULL
		) AS category_products
		GROUP BY category_id
	`

	type categoryCount struct {
		CategoryID int `db:"category_id"`
		Count      int `db:"count"`
	}

	results, err := QueryListNamed[categoryCount](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get men counts by category: %w", err)
	}

	counts := make(map[int]int)
	for _, result := range results {
		counts[result.CategoryID] = result.Count
	}

	return counts, nil
}

// getCategoriesWomenCounts returns a map of category ID to women's product count
func (ms *MYSQLStore) getCategoriesWomenCounts(ctx context.Context) (map[int]int, error) {
	query := `
		SELECT 
			category_id, 
			COUNT(*) as count
		FROM (
			SELECT DISTINCT p.top_category_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.target_gender IN ('female', 'unisex')
			
			UNION
			
			SELECT DISTINCT p.sub_category_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.target_gender IN ('female', 'unisex') AND p.sub_category_id IS NOT NULL
			
			UNION
			
			SELECT DISTINCT p.type_id as category_id, p.id as product_id
			FROM product p 
			WHERE p.hidden = 0 AND p.target_gender IN ('female', 'unisex') AND p.type_id IS NOT NULL
		) AS category_products
		GROUP BY category_id
	`

	type categoryCount struct {
		CategoryID int `db:"category_id"`
		Count      int `db:"count"`
	}

	results, err := QueryListNamed[categoryCount](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get women counts by category: %w", err)
	}

	counts := make(map[int]int)
	for _, result := range results {
		counts[result.CategoryID] = result.Count
	}

	return counts, nil
}

// getCountMen counts products that target male or unisex gender for a given category ID
func (ms *MYSQLStore) getCountMen(ctx context.Context, categoryID int) (int, error) {
	query := `
		SELECT COUNT(*) FROM product p 
		WHERE p.hidden = 0 
		AND p.target_gender IN ('male', 'unisex')
		AND (p.top_category_id = :categoryId OR p.sub_category_id = :categoryId OR p.type_id = :categoryId)
	`

	count, err := QueryCountNamed(ctx, ms.db, query, map[string]interface{}{
		"categoryId": categoryID,
	})
	if err != nil {
		return 0, fmt.Errorf("can't get count men for category %d: %w", categoryID, err)
	}

	return count, nil
}

// getCountWomen counts products that target female or unisex gender for a given category ID
func (ms *MYSQLStore) getCountWomen(ctx context.Context, categoryID int) (int, error) {
	query := `
		SELECT COUNT(*) FROM product p 
		WHERE p.hidden = 0 
		AND p.target_gender IN ('female', 'unisex')
		AND (p.top_category_id = :categoryId OR p.sub_category_id = :categoryId OR p.type_id = :categoryId)
	`

	count, err := QueryCountNamed(ctx, ms.db, query, map[string]interface{}{
		"categoryId": categoryID,
	})
	if err != nil {
		return 0, fmt.Errorf("can't get count women for category %d: %w", categoryID, err)
	}

	return count, nil
}

func (ms *MYSQLStore) getPromos(ctx context.Context) ([]entity.PromoCode, error) {
	query := `SELECT * FROM promo_code`
	promos, err := QueryListNamed[entity.PromoCode](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return promos, nil
}

func (ms *MYSQLStore) getShipmentCarriers(ctx context.Context) ([]entity.ShipmentCarrier, error) {
	query := `SELECT * FROM shipment_carrier`
	shipmentCarriers, err := QueryListNamed[entity.ShipmentCarrier](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get ShipmentCarrier by id: %w", err)
	}
	return shipmentCarriers, nil
}

func (ms *MYSQLStore) getSizes(ctx context.Context) ([]entity.Size, error) {
	query := `SELECT * FROM size`
	sizes, err := QueryListNamed[entity.Size](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get size by id: %w", err)
	}
	return sizes, nil
}
