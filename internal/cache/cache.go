package cache

import (
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"golang.org/x/exp/slog"
)

type Cache struct {
	Category        *CategoryCache
	Measurement     *MeasurementCache
	OrderStatus     *OrderStatusCache
	PaymentMethod   *PaymentMethodCache
	Promo           *PromoCache
	ShipmentCarrier *ShipmentCarrierCache
	Size            *SizeCache
	Dict            *dto.Dict
}

func NewCache(
	categories []entity.Category,
	measurements []entity.MeasurementName,
	orderStatuses []entity.OrderStatus,
	paymentMethods []entity.PaymentMethod,
	promos []entity.PromoCode,
	shipmentCarriers []entity.ShipmentCarrier,
	sizes []entity.Size,
) (dependency.Cache, error) {
	cc, err := newCategoryCache(categories)
	if err != nil {
		slog.Default().Error("cant get all categories",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	mc, err := newMeasurementCache(measurements)
	if err != nil {
		slog.Default().Error("cant get all measurements",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	oc, err := newOrderStatusCache(orderStatuses)
	if err != nil {
		slog.Default().Error("cant get all order statuses",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	pc, err := newPaymentMethodCache(paymentMethods)
	if err != nil {
		slog.Default().Error("cant get all payment methods",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	sc, err := newSizeCache(sizes)
	if err != nil {
		slog.Default().Error("cant get all sizes",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	return &Cache{
		Category:        cc,
		Measurement:     mc,
		OrderStatus:     oc,
		PaymentMethod:   pc,
		Promo:           newPromoCache(promos),
		ShipmentCarrier: newShipmentCarrierCache(shipmentCarriers),
		Size:            sc,
		Dict: &dto.Dict{
			Categories:       categories,
			Measurements:     measurements,
			OrderStatuses:    orderStatuses,
			PaymentMethods:   paymentMethods,
			Promos:           promos,
			ShipmentCarriers: shipmentCarriers,
			Sizes:            sizes,
		},
	}, nil
}

func (c *Cache) GetDict() *dto.Dict {
	return c.Dict
}

// category
func (c *Cache) GetCategoryByID(id int) (*entity.Category, bool) {
	return c.Category.GetCategoryByID(id)
}
func (c *Cache) GetCategoryByName(category entity.CategoryEnum) (entity.Category, bool) {
	return c.Category.GetCategoryByName(category)
}

// measurement
func (c *Cache) GetMeasurementByID(id int) (*entity.MeasurementName, bool) {
	return c.Measurement.GetMeasurementByID(id)
}
func (c *Cache) GetMeasurementsByName(measurement entity.MeasurementNameEnum) (entity.MeasurementName, bool) {
	return c.Measurement.GetMeasurementsByName(measurement)
}

// order status
func (c *Cache) GetOrderStatusByID(id int) (*entity.OrderStatus, bool) {
	return c.OrderStatus.GetOrderStatusByID(id)
}
func (c *Cache) GetOrderStatusByName(orderStatus entity.OrderStatusName) (entity.OrderStatus, bool) {
	return c.OrderStatus.GetOrderStatusByName(orderStatus)
}

// payment method
func (c *Cache) GetPaymentMethodByID(id int) (*entity.PaymentMethod, bool) {
	return c.PaymentMethod.GetPaymentMethodByID(id)
}
func (c *Cache) GetPaymentMethodsByName(paymentMethod entity.PaymentMethodName) (entity.PaymentMethod, bool) {
	return c.PaymentMethod.GetPaymentMethodsByName(paymentMethod)
}

// promo
func (c *Cache) GetPromoByID(id int) (*entity.PromoCode, bool) {
	return c.Promo.GetPromoByID(id)
}
func (c *Cache) GetPromoByName(paymentMethod string) (entity.PromoCode, bool) {
	return c.Promo.GetPromoByName(paymentMethod)
}
func (c *Cache) AddPromo(promo entity.PromoCode) {
	c.Promo.AddPromo(promo)
}
func (c *Cache) DeletePromo(code string) {
	c.Promo.DeletePromo(code)
}
func (c *Cache) DisablePromo(code string) {
	c.Promo.DisablePromo(code)
}

// shipment carrier
func (c *Cache) GetShipmentCarrierByID(id int) (*entity.ShipmentCarrier, bool) {
	return c.ShipmentCarrier.GetShipmentCarrierByID(id)
}
func (c *Cache) GetShipmentCarriersByName(carrier string) (entity.ShipmentCarrier, bool) {
	return c.ShipmentCarrier.GetShipmentCarriersByName(carrier)
}

// size
func (c *Cache) GetSizeByID(id int) (*entity.Size, bool) {
	return c.Size.GetSizeByID(id)
}
func (c *Cache) GetSizesByName(size entity.SizeEnum) (entity.Size, bool) {
	return c.Size.GetSizesByName(size)
}
