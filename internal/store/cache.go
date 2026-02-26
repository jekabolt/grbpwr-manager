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

// GetDictionaryInfo returns dictionary info with all translations
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

	if dict.Collections, err = ms.getCollections(ctx); err != nil {
		return nil, fmt.Errorf("failed to get collections: %w", err)
	}

	if dict.Languages, err = ms.Language().GetAllLanguages(ctx); err != nil {
		return nil, fmt.Errorf("failed to get languages: %w", err)
	}

	dict.Announce, err = ms.Settings().GetAnnounce(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get announce: %w", err)
	}

	dict.ComplimentaryShippingPrices, err = ms.Settings().GetComplimentaryShippingPrices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get complimentary shipping prices: %w", err)
	}

	return &dict, nil
}

// Existing methods for fetching individual entities remain the same
func (ms *MYSQLStore) getCategories(ctx context.Context) ([]entity.Category, error) {
	// Get category info with English name directly
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
	categories, err := QueryListNamed[entity.Category](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get categories: %w", err)
	}

	// Get category counts efficiently in bulk
	categoryMenCounts, err := ms.getCategoriesCountsByGender(ctx, entity.Male)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	categoryWomenCounts, err := ms.getCategoriesCountsByGender(ctx, entity.Female)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	// Map counts to categories
	for i := range categories {
		// Add counts
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
	// Get measurement info with English name directly
	query := `
		SELECT 
			mn.id,
			mn.name
		FROM measurement_name mn
	`
	measurements, err := QueryListNamed[entity.MeasurementName](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get measurements: %w", err)
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

// getCategoriesCountsByGender returns a map of category ID to product count for a specific gender
func (ms *MYSQLStore) getCategoriesCountsByGender(ctx context.Context, gender entity.GenderEnum) (map[int]int, error) {
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

	results, err := QueryListNamed[categoryCount](ctx, ms.db, query, map[string]interface{}{
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

func (ms *MYSQLStore) getPromos(ctx context.Context) ([]entity.PromoCode, error) {
	query := `SELECT * FROM promo_code`
	promos, err := QueryListNamed[entity.PromoCode](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return promos, nil
}

func (ms *MYSQLStore) getShipmentCarriers(ctx context.Context) ([]entity.ShipmentCarrier, error) {
	query := `SELECT id, carrier, tracking_url, allowed, description, expected_delivery_time FROM shipment_carrier`
	shipmentCarriers, err := QueryListNamed[entity.ShipmentCarrier](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get ShipmentCarrier by id: %w", err)
	}

	// Load prices and regions for all shipment carriers
	if len(shipmentCarriers) > 0 {
		carrierIds := make([]int, len(shipmentCarriers))
		for i := range shipmentCarriers {
			carrierIds[i] = shipmentCarriers[i].Id
		}

		prices, err := ms.fetchShipmentCarrierPrices(ctx, carrierIds)
		if err != nil {
			return nil, fmt.Errorf("can't get shipment carrier prices: %w", err)
		}

		regions, err := ms.fetchShipmentCarrierRegions(ctx, carrierIds)
		if err != nil {
			return nil, fmt.Errorf("can't get shipment carrier regions: %w", err)
		}

		// Assign prices and regions to carriers
		for i := range shipmentCarriers {
			shipmentCarriers[i].Prices = prices[shipmentCarriers[i].Id]
			shipmentCarriers[i].AllowedRegions = regions[shipmentCarriers[i].Id]
		}
	}

	return shipmentCarriers, nil
}

// fetchShipmentCarrierRegions fetches all regions for given shipment carrier IDs
func (ms *MYSQLStore) fetchShipmentCarrierRegions(ctx context.Context, carrierIds []int) (map[int][]string, error) {
	if len(carrierIds) == 0 {
		return map[int][]string{}, nil
	}

	type regionRow struct {
		ShipmentCarrierId int    `db:"shipment_carrier_id"`
		Region            string `db:"region"`
	}
	query := `SELECT shipment_carrier_id, region FROM shipment_carrier_region WHERE shipment_carrier_id IN (:carrierIds) ORDER BY shipment_carrier_id, region`
	rows, err := QueryListNamed[regionRow](ctx, ms.db, query, map[string]any{
		"carrierIds": carrierIds,
	})
	if err != nil {
		return nil, err
	}

	regionMap := make(map[int][]string)
	for _, r := range rows {
		regionMap[r.ShipmentCarrierId] = append(regionMap[r.ShipmentCarrierId], r.Region)
	}
	return regionMap, nil
}

// fetchShipmentCarrierPrices fetches all prices for given shipment carrier IDs
func (ms *MYSQLStore) fetchShipmentCarrierPrices(ctx context.Context, carrierIds []int) (map[int][]entity.ShipmentCarrierPrice, error) {
	if len(carrierIds) == 0 {
		return map[int][]entity.ShipmentCarrierPrice{}, nil
	}

	query := `SELECT id, shipment_carrier_id, currency, price, created_at, updated_at FROM shipment_carrier_price WHERE shipment_carrier_id IN (:carrierIds) ORDER BY shipment_carrier_id, currency`

	prices, err := QueryListNamed[entity.ShipmentCarrierPrice](ctx, ms.db, query, map[string]any{
		"carrierIds": carrierIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get shipment carrier prices: %w", err)
	}

	// Group prices by carrier ID
	priceMap := make(map[int][]entity.ShipmentCarrierPrice)
	for _, p := range prices {
		priceMap[p.ShipmentCarrierId] = append(priceMap[p.ShipmentCarrierId], p)
	}

	return priceMap, nil
}

func (ms *MYSQLStore) getSizes(ctx context.Context) ([]entity.Size, error) {
	query := `SELECT * FROM size`
	sizes, err := QueryListNamed[entity.Size](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get size by id: %w", err)
	}

	// Get size counts efficiently in bulk
	sizeMenCounts, err := ms.getSizeCountsByGender(ctx, entity.Male)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	sizeWomenCounts, err := ms.getSizeCountsByGender(ctx, entity.Female)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	// Map counts to sizes
	for i := range sizes {
		// Add counts
		if count, exists := sizeMenCounts[sizes[i].Id]; exists {
			sizes[i].CountMen = count
		}
		if count, exists := sizeWomenCounts[sizes[i].Id]; exists {
			sizes[i].CountWomen = count
		}
	}

	return sizes, nil
}

// getSizeCountsByGender returns a map of size ID to product count for a specific gender
func (ms *MYSQLStore) getSizeCountsByGender(ctx context.Context, gender entity.GenderEnum) (map[int]int, error) {
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

	results, err := QueryListNamed[sizeCount](ctx, ms.db, query, map[string]interface{}{
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

func (ms *MYSQLStore) getCollections(ctx context.Context) ([]entity.Collection, error) {
	// Get distinct collections from products
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

	results, err := QueryListNamed[collectionResult](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get collections: %w", err)
	}

	// Get collection counts efficiently in bulk
	collectionMenCounts, err := ms.getCollectionCountsByGender(ctx, entity.Male)
	if err != nil {
		return nil, fmt.Errorf("failed to get men counts: %w", err)
	}

	collectionWomenCounts, err := ms.getCollectionCountsByGender(ctx, entity.Female)
	if err != nil {
		return nil, fmt.Errorf("failed to get women counts: %w", err)
	}

	// Convert to Collection entities and map counts
	collections := make([]entity.Collection, 0, len(results))
	for _, result := range results {
		collection := entity.Collection{
			Name: result.Name,
		}

		// Add counts
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

// getCollectionCountsByGender returns a map of collection name to product count for a specific gender
func (ms *MYSQLStore) getCollectionCountsByGender(ctx context.Context, gender entity.GenderEnum) (map[string]int, error) {
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

	results, err := QueryListNamed[collectionCount](ctx, ms.db, query, map[string]interface{}{
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
