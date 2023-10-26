package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func (ms *MYSQLStore) initCache(ctx context.Context) (dependency.Cache, error) {
	categories, err := getCategories(ctx, ms)
	if err != nil {
		return nil, err
	}
	measurements, err := getMeasurements(ctx, ms)
	if err != nil {
		return nil, err
	}
	orderStatuses, err := getOrderStatuses(ctx, ms)
	if err != nil {
		return nil, err
	}
	paymentMethod, err := getPaymentMethod(ctx, ms)
	if err != nil {
		return nil, err
	}
	promos, err := getPromos(ctx, ms)
	if err != nil {
		return nil, err
	}
	shipmentCarriers, err := getShipmentCarriers(ctx, ms)
	if err != nil {
		return nil, err
	}
	sizes, err := getSizes(ctx, ms)
	if err != nil {
		return nil, err
	}

	c, err := cache.NewCache(categories, measurements, orderStatuses, paymentMethod, promos, shipmentCarriers, sizes)
	if err != nil {
		return nil, fmt.Errorf("can't init cache: %w", err)
	}

	return c, nil
}

func getCategories(ctx context.Context, rep dependency.Repository) ([]entity.Category, error) {
	query := `
	SELECT * FROM category`
	categories, err := QueryListNamed[entity.Category](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get Category by id: %w", err)
	}
	return categories, nil
}

func getMeasurements(ctx context.Context, rep dependency.Repository) ([]entity.MeasurementName, error) {
	query := `
	SELECT * FROM measurement_name`
	measurements, err := QueryListNamed[entity.MeasurementName](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get MeasurementName by id: %w", err)
	}
	return measurements, nil
}

func getPaymentMethod(ctx context.Context, rep dependency.Repository) ([]entity.PaymentMethod, error) {
	query := `
	SELECT * FROM payment_method`
	paymentMethods, err := QueryListNamed[entity.PaymentMethod](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return paymentMethods, nil
}

func getOrderStatuses(ctx context.Context, rep dependency.Repository) ([]entity.OrderStatus, error) {
	query := `
	SELECT * FROM order_status`
	orderStatuses, err := QueryListNamed[entity.OrderStatus](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return orderStatuses, nil
}

func getPromos(ctx context.Context, rep dependency.Repository) ([]entity.PromoCode, error) {
	query := `
	SELECT * FROM promo_code`
	promos, err := QueryListNamed[entity.PromoCode](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PaymentMethod by id: %w", err)
	}
	return promos, nil
}

func getShipmentCarriers(ctx context.Context, rep dependency.Repository) ([]entity.ShipmentCarrier, error) {
	query := `
	SELECT * FROM shipment_carrier`
	shipmentCarriers, err := QueryListNamed[entity.ShipmentCarrier](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get ShipmentCarrier by id: %w", err)
	}
	return shipmentCarriers, nil
}

func getSizes(ctx context.Context, rep dependency.Repository) ([]entity.Size, error) {
	query := `
	SELECT * FROM size`
	sizes, err := QueryListNamed[entity.Size](ctx, rep.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get size by id: %w", err)
	}
	return sizes, nil
}
