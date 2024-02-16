package cache

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
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
	Hero            *HeroCache
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
		Hero:            newHeroCache(&entity.HeroFull{}),
		Dict: &dto.Dict{
			Categories:       categories,
			Measurements:     measurements,
			OrderStatuses:    orderStatuses,
			PaymentMethods:   paymentMethods,
			Promos:           promos,
			ShipmentCarriers: shipmentCarriers,
			Sizes:            sizes,
			SiteEnabled:      true,
		},
	}, nil
}

func (c *Cache) GetDict() *dto.Dict {
	return c.Dict
}

// category
func (c *Cache) GetCategoryById(id int) (*entity.Category, bool) {
	return c.Category.GetCategoryById(id)
}
func (c *Cache) GetCategoryByName(category entity.CategoryEnum) (entity.Category, bool) {
	return c.Category.GetCategoryByName(category)
}

// measurement
func (c *Cache) GetMeasurementById(id int) (*entity.MeasurementName, bool) {
	return c.Measurement.GetMeasurementById(id)
}
func (c *Cache) GetMeasurementsByName(measurement entity.MeasurementNameEnum) (entity.MeasurementName, bool) {
	return c.Measurement.GetMeasurementsByName(measurement)
}

// order status
func (c *Cache) GetOrderStatusById(id int) (*entity.OrderStatus, bool) {
	return c.OrderStatus.GetOrderStatusById(id)
}
func (c *Cache) GetOrderStatusByName(orderStatus entity.OrderStatusName) (entity.OrderStatus, bool) {
	return c.OrderStatus.GetOrderStatusByName(orderStatus)
}

// payment method
func (c *Cache) GetPaymentMethodById(id int) (*entity.PaymentMethod, bool) {
	return c.PaymentMethod.GetPaymentMethodById(id)
}
func (c *Cache) GetPaymentMethodsByName(paymentMethod entity.PaymentMethodName) (entity.PaymentMethod, bool) {
	return c.PaymentMethod.GetPaymentMethodsByName(paymentMethod)
}

func (c *Cache) UpdatePaymentMethodAllowance(pm entity.PaymentMethodName, allowance bool) error {
	err := c.PaymentMethod.UpdatePaymentMethodAllowance(pm, allowance)
	if err != nil {
		return fmt.Errorf("failed to update payment method allowance: %w", err)
	}
	pms := []entity.PaymentMethod{}
	pmm := c.PaymentMethod.GetAllPaymentMethods()
	for id, pmv := range pmm {
		pmv.ID = id
		pms = append(pms, pmv)
	}
	c.Dict.PaymentMethods = pms
	return nil
}

// promo
func (c *Cache) GetPromoById(id int) (*entity.PromoCode, bool) {
	return c.Promo.GetPromoById(id)
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
func (c *Cache) GetShipmentCarrierById(id int) (*entity.ShipmentCarrier, bool) {
	return c.ShipmentCarrier.GetShipmentCarrierById(id)
}
func (c *Cache) GetShipmentCarriersByName(carrier string) (entity.ShipmentCarrier, bool) {
	return c.ShipmentCarrier.GetShipmentCarriersByName(carrier)
}
func (c *Cache) UpdateShipmentCarrierAllowance(carrier string, allowance bool) error {
	err := c.ShipmentCarrier.UpdateShipmentCarrierAllowance(carrier, allowance)
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier allowance: %w", err)
	}
	scs := []entity.ShipmentCarrier{}
	scsm := c.ShipmentCarrier.GetAllShipmentCarriers()
	for id, scsv := range scsm {
		scsv.ID = id
		scs = append(scs, scsv)
	}
	c.Dict.ShipmentCarriers = scs
	return nil
}
func (c *Cache) UpdateShipmentCarrierCost(carrier string, cost decimal.Decimal) error {
	err := c.ShipmentCarrier.UpdateShipmentCarrierCost(carrier, cost)
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier price: %w", err)
	}
	scs := []entity.ShipmentCarrier{}
	scsm := c.ShipmentCarrier.GetAllShipmentCarriers()
	for id, scsv := range scsm {
		scsv.ID = id
		scs = append(scs, scsv)
	}
	c.Dict.ShipmentCarriers = scs
	return nil
}

// size
func (c *Cache) GetSizeById(id int) (*entity.Size, bool) {
	return c.Size.GetSizeById(id)
}
func (c *Cache) GetSizesByName(size entity.SizeEnum) (entity.Size, bool) {
	return c.Size.GetSizesByName(size)
}

// hero

func (c *Cache) GetHero() *entity.HeroFull {
	return c.Hero.GetHero()
}

func (c *Cache) UpdateHero(hf *entity.HeroFull) {
	c.Hero.UpdateHero(hf)
}

func (c *Cache) SetSiteAvailability(available bool) {
	c.Dict.SiteEnabled = available
}
