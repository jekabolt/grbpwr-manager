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
	query := `SELECT * FROM category`
	categories, err := QueryListNamed[entity.Category](ctx, ms.db, query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get Category by id: %w", err)
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
