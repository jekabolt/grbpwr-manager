// Package settings implements system settings and dictionary cache operations.
package settings

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// TxFunc executes f within a serializable transaction with deadlock retry.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// RepFunc returns the current repository.
type RepFunc func() dependency.Repository

// Store implements dependency.Settings and dependency.Cache.
type Store struct {
	storeutil.Base
	txFunc  TxFunc
	repFunc RepFunc
}

// New creates a new settings store.
func New(base storeutil.Base, txFunc TxFunc, repFunc RepFunc) *Store {
	return &Store{Base: base, txFunc: txFunc, repFunc: repFunc}
}

// SetShipmentCarrierAllowance updates the allowed status of a shipment carrier.
func (s *Store) SetShipmentCarrierAllowance(ctx context.Context, carrier string, allowance bool) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `UPDATE shipment_carrier SET allowed = :allowed WHERE carrier = :carrier`
		err := storeutil.ExecNamed(ctx, rep.DB(), query, map[string]any{
			"carrier": carrier,
			"allowed": allowance,
		})
		if err != nil {
			return fmt.Errorf("failed to update shipment carrier allowance: %w", err)
		}
		cache.UpdateShipmentCarrierAllowance(carrier, allowance)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier allowance: %w", err)
	}
	return nil
}

// AddShipmentCarrier adds a new shipment carrier with prices and allowed regions.
func (s *Store) AddShipmentCarrier(ctx context.Context, carrier *entity.ShipmentCarrierInsert, prices map[string]decimal.Decimal, allowedRegions []string) (int, error) {
	var carrierId int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `INSERT INTO shipment_carrier (carrier, tracking_url, allowed, description, expected_delivery_time)
			VALUES (:carrier, :trackingUrl, :allowed, :description, :expectedDeliveryTime)`
		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(), query, map[string]any{
			"carrier":              carrier.Carrier,
			"trackingUrl":          carrier.TrackingURL,
			"allowed":              carrier.Allowed,
			"description":          carrier.Description,
			"expectedDeliveryTime": carrier.ExpectedDeliveryTime,
		})
		if err != nil {
			return fmt.Errorf("failed to insert shipment carrier: %w", err)
		}
		carrierId = id

		for currency, price := range prices {
			if price.IsNegative() {
				return fmt.Errorf("price for %s cannot be negative", currency)
			}
			priceQuery := `INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price) VALUES (:carrierId, :currency, :price)`
			if err := storeutil.ExecNamed(ctx, rep.DB(), priceQuery, map[string]any{
				"carrierId": carrierId,
				"currency":  currency,
				"price":     dto.RoundForCurrency(price, currency),
			}); err != nil {
				return fmt.Errorf("failed to insert price for %s: %w", currency, err)
			}
		}

		for _, region := range allowedRegions {
			regionQuery := `INSERT INTO shipment_carrier_region (shipment_carrier_id, region) VALUES (:carrierId, :region)`
			if err := storeutil.ExecNamed(ctx, rep.DB(), regionQuery, map[string]any{
				"carrierId": carrierId,
				"region":    region,
			}); err != nil {
				return fmt.Errorf("failed to insert region %s: %w", region, err)
			}
		}

		carriers, err := getShipmentCarriers(ctx, rep.DB())
		if err != nil {
			return fmt.Errorf("failed to refresh shipment carriers: %w", err)
		}
		cache.UpdateShipmentCarriers(carriers)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to add shipment carrier: %w", err)
	}
	return carrierId, nil
}

// UpdateShipmentCarrier updates an existing shipment carrier, replacing prices and regions.
func (s *Store) UpdateShipmentCarrier(ctx context.Context, id int, carrier *entity.ShipmentCarrierInsert, prices map[string]decimal.Decimal, allowedRegions []string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `UPDATE shipment_carrier SET carrier = :carrier, tracking_url = :trackingUrl, allowed = :allowed, description = :description, expected_delivery_time = :expectedDeliveryTime WHERE id = :id`
		if err := storeutil.ExecNamed(ctx, rep.DB(), query, map[string]any{
			"id":                   id,
			"carrier":              carrier.Carrier,
			"trackingUrl":          carrier.TrackingURL,
			"allowed":              carrier.Allowed,
			"description":          carrier.Description,
			"expectedDeliveryTime": carrier.ExpectedDeliveryTime,
		}); err != nil {
			return fmt.Errorf("failed to update shipment carrier: %w", err)
		}

		if err := storeutil.ExecNamed(ctx, rep.DB(), `DELETE FROM shipment_carrier_price WHERE shipment_carrier_id = :carrierId`, map[string]any{"carrierId": id}); err != nil {
			return fmt.Errorf("failed to delete existing prices: %w", err)
		}
		for currency, price := range prices {
			if price.IsNegative() {
				return fmt.Errorf("price for %s cannot be negative", currency)
			}
			priceQuery := `INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price) VALUES (:carrierId, :currency, :price)`
			if err := storeutil.ExecNamed(ctx, rep.DB(), priceQuery, map[string]any{
				"carrierId": id,
				"currency":  currency,
				"price":     dto.RoundForCurrency(price, currency),
			}); err != nil {
				return fmt.Errorf("failed to insert price for %s: %w", currency, err)
			}
		}

		if err := storeutil.ExecNamed(ctx, rep.DB(), `DELETE FROM shipment_carrier_region WHERE shipment_carrier_id = :carrierId`, map[string]any{"carrierId": id}); err != nil {
			return fmt.Errorf("failed to delete existing regions: %w", err)
		}
		for _, region := range allowedRegions {
			regionQuery := `INSERT INTO shipment_carrier_region (shipment_carrier_id, region) VALUES (:carrierId, :region)`
			if err := storeutil.ExecNamed(ctx, rep.DB(), regionQuery, map[string]any{
				"carrierId": id,
				"region":    region,
			}); err != nil {
				return fmt.Errorf("failed to insert region %s: %w", region, err)
			}
		}

		carriers, err := getShipmentCarriers(ctx, rep.DB())
		if err != nil {
			return fmt.Errorf("failed to refresh shipment carriers: %w", err)
		}
		cache.UpdateShipmentCarriers(carriers)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier: %w", err)
	}
	return nil
}

// DeleteShipmentCarrier deletes a shipment carrier if it's not referenced by any shipment.
func (s *Store) DeleteShipmentCarrier(ctx context.Context, id int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		type countRow struct {
			N int `db:"n"`
		}
		c, err := storeutil.QueryNamedOne[countRow](ctx, rep.DB(), `SELECT COUNT(*) as n FROM shipment WHERE carrier_id = :carrierId`, map[string]any{"carrierId": id})
		if err != nil {
			return fmt.Errorf("failed to check shipment references: %w", err)
		}
		if c.N > 0 {
			return fmt.Errorf("cannot delete carrier: %d shipment(s) reference it", c.N)
		}

		if err := storeutil.ExecNamed(ctx, rep.DB(), `DELETE FROM shipment_carrier WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to delete shipment carrier: %w", err)
		}

		carriers, err := getShipmentCarriers(ctx, rep.DB())
		if err != nil {
			return fmt.Errorf("failed to refresh shipment carriers: %w", err)
		}
		cache.UpdateShipmentCarriers(carriers)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to delete shipment carrier: %w", err)
	}
	return nil
}

// SetShipmentCarrierPrices upserts prices for a shipment carrier.
func (s *Store) SetShipmentCarrierPrices(ctx context.Context, carrier string, prices map[string]decimal.Decimal) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		type CarrierId struct {
			Id int `db:"id"`
		}
		query := `SELECT id FROM shipment_carrier WHERE carrier = :carrier`
		carrierResult, err := storeutil.QueryNamedOne[CarrierId](ctx, rep.DB(), query, map[string]any{"carrier": carrier})
		if err != nil {
			return fmt.Errorf("failed to get shipment carrier ID: %w", err)
		}
		carrierId := carrierResult.Id

		for currency, price := range prices {
			if price.IsNegative() {
				return fmt.Errorf("price for %s cannot be negative", currency)
			}
			upsertQuery := `
				INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price)
				VALUES (:carrierId, :currency, :price)
				ON DUPLICATE KEY UPDATE price = :price, updated_at = NOW()`
			err := storeutil.ExecNamed(ctx, rep.DB(), upsertQuery, map[string]any{
				"carrierId": carrierId,
				"currency":  currency,
				"price":     dto.RoundForCurrency(price, currency),
			})
			if err != nil {
				return fmt.Errorf("failed to upsert shipment carrier price for currency %s: %w", currency, err)
			}
		}

		carriers, err := getShipmentCarriers(ctx, rep.DB())
		if err != nil {
			return fmt.Errorf("failed to refresh shipment carriers: %w", err)
		}
		cache.UpdateShipmentCarriers(carriers)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier prices: %w", err)
	}
	return nil
}

// SetPaymentIsProd sets the payment mode.
func (s *Store) SetPaymentIsProd(ctx context.Context, isProd bool) error {
	cache.SetPaymentIsProd(isProd)
	return nil
}

// SetPaymentMethodAllowance updates the allowed status of a payment method.
func (s *Store) SetPaymentMethodAllowance(ctx context.Context, paymentMethod entity.PaymentMethodName, allowance bool) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `UPDATE payment_method SET allowed = :allowed WHERE name = :method`
		err := storeutil.ExecNamed(ctx, rep.DB(), query, map[string]any{
			"method":  paymentMethod,
			"allowed": allowance,
		})
		if err != nil {
			return fmt.Errorf("failed to update payment method allowance: %w", err)
		}
		cache.UpdatePaymentMethodAllowance(paymentMethod, allowance)
		cache.RefreshEntityPaymentMethods()
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update payment method allowance: %w", err)
	}
	return nil
}

// SetSiteAvailability sets the site availability.
func (s *Store) SetSiteAvailability(ctx context.Context, available bool) error {
	cache.SetSiteAvailability(available)
	return nil
}

// SetMaxOrderItems sets the maximum number of order items.
func (s *Store) SetMaxOrderItems(ctx context.Context, count int) error {
	cache.SetMaxOrderItems(count)
	return nil
}

// SetBigMenu sets the big menu flag.
func (s *Store) SetBigMenu(ctx context.Context, bigMenu bool) error {
	cache.SetBigMenu(bigMenu)
	return nil
}

// SetOrderExpirationSeconds sets the order expiration timeout.
func (s *Store) SetOrderExpirationSeconds(ctx context.Context, seconds int) error {
	cache.SetOrderExpirationSeconds(seconds)
	return nil
}

// SetAnnounce updates the announce link and translations.
func (s *Store) SetAnnounce(ctx context.Context, link string, translations []entity.AnnounceTranslation) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		linkQuery := `UPDATE announce SET link = :link WHERE id = 1`
		err := storeutil.ExecNamed(ctx, rep.DB(), linkQuery, map[string]any{
			"link": link,
		})
		if err != nil {
			return fmt.Errorf("failed to update announce link: %w", err)
		}

		deleteQuery := `DELETE FROM announce_translation`
		err = storeutil.ExecNamed(ctx, rep.DB(), deleteQuery, map[string]any{})
		if err != nil {
			return fmt.Errorf("failed to delete existing announce translations: %w", err)
		}

		for _, translation := range translations {
			insertQuery := `INSERT INTO announce_translation (language_id, text) VALUES (:languageId, :text)`
			err := storeutil.ExecNamed(ctx, rep.DB(), insertQuery, map[string]any{
				"languageId": translation.LanguageId,
				"text":       translation.Text,
			})
			if err != nil {
				return fmt.Errorf("failed to insert announce translation: %w", err)
			}
		}

		cache.SetAnnounce(link, translations)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update announce: %w", err)
	}
	return nil
}

// GetAnnounce returns the current announce with translations.
func (s *Store) GetAnnounce(ctx context.Context) (*entity.AnnounceWithTranslations, error) {
	linkQuery := `SELECT link FROM announce WHERE id = 1`
	announce, err := storeutil.QueryNamedOne[entity.Announce](ctx, s.DB, linkQuery, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get announce link: %w", err)
	}

	translationsQuery := `SELECT id, language_id, text, created_at, updated_at FROM announce_translation ORDER BY language_id`
	translations, err := storeutil.QueryListNamed[entity.AnnounceTranslation](ctx, s.DB, translationsQuery, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get announce translations: %w", err)
	}

	return &entity.AnnounceWithTranslations{
		Link:         announce.Link,
		Translations: translations,
	}, nil
}

// SetComplimentaryShippingPrices upserts complimentary shipping prices.
func (s *Store) SetComplimentaryShippingPrices(ctx context.Context, prices map[string]decimal.Decimal) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		for currency, price := range prices {
			rounded := dto.RoundForCurrency(price, currency)
			if err := dto.ValidatePriceMeetsMinimum(rounded, currency); err != nil {
				return fmt.Errorf("price validation for %s: %w", currency, err)
			}
			upsertQuery := `
				INSERT INTO complimentary_shipping_price (currency, price)
				VALUES (:currency, :price)
				ON DUPLICATE KEY UPDATE price = :price, updated_at = NOW()`
			err := storeutil.ExecNamed(ctx, rep.DB(), upsertQuery, map[string]any{
				"currency": currency,
				"price":    rounded,
			})
			if err != nil {
				return fmt.Errorf("failed to upsert complimentary shipping price for currency %s: %w", currency, err)
			}
		}

		pricesFromDB, err := getComplimentaryShippingPrices(ctx, rep.DB())
		if err != nil {
			return fmt.Errorf("failed to refresh complimentary shipping prices: %w", err)
		}
		cache.UpdateComplimentaryShippingPrices(pricesFromDB)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update complimentary shipping prices: %w", err)
	}
	return nil
}

// GetComplimentaryShippingPrices returns all complimentary shipping prices.
func (s *Store) GetComplimentaryShippingPrices(ctx context.Context) (map[string]decimal.Decimal, error) {
	return getComplimentaryShippingPrices(ctx, s.DB)
}

func getComplimentaryShippingPrices(ctx context.Context, db dependency.DB) (map[string]decimal.Decimal, error) {
	type ComplimentaryShippingPrice struct {
		Currency string          `db:"currency"`
		Price    decimal.Decimal `db:"price"`
	}
	query := `SELECT currency, price FROM complimentary_shipping_price`
	results, err := storeutil.QueryListNamed[ComplimentaryShippingPrice](ctx, db, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get complimentary shipping prices: %w", err)
	}

	prices := make(map[string]decimal.Decimal)
	for _, result := range results {
		prices[result.Currency] = result.Price
	}
	return prices, nil
}

// getShipmentCarriers fetches all shipment carriers with prices and regions.
func getShipmentCarriers(ctx context.Context, db dependency.DB) ([]entity.ShipmentCarrier, error) {
	query := `SELECT id, carrier, tracking_url, allowed, description, expected_delivery_time FROM shipment_carrier`
	shipmentCarriers, err := storeutil.QueryListNamed[entity.ShipmentCarrier](ctx, db, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't get ShipmentCarrier by id: %w", err)
	}

	if len(shipmentCarriers) > 0 {
		carrierIds := make([]int, len(shipmentCarriers))
		for i := range shipmentCarriers {
			carrierIds[i] = shipmentCarriers[i].Id
		}

		prices, err := fetchShipmentCarrierPrices(ctx, db, carrierIds)
		if err != nil {
			return nil, fmt.Errorf("can't get shipment carrier prices: %w", err)
		}

		regions, err := fetchShipmentCarrierRegions(ctx, db, carrierIds)
		if err != nil {
			return nil, fmt.Errorf("can't get shipment carrier regions: %w", err)
		}

		for i := range shipmentCarriers {
			shipmentCarriers[i].Prices = prices[shipmentCarriers[i].Id]
			shipmentCarriers[i].AllowedRegions = regions[shipmentCarriers[i].Id]
		}
	}

	return shipmentCarriers, nil
}

func fetchShipmentCarrierRegions(ctx context.Context, db dependency.DB, carrierIds []int) (map[int][]string, error) {
	if len(carrierIds) == 0 {
		return map[int][]string{}, nil
	}

	type regionRow struct {
		ShipmentCarrierId int    `db:"shipment_carrier_id"`
		Region            string `db:"region"`
	}
	query := `SELECT shipment_carrier_id, region FROM shipment_carrier_region WHERE shipment_carrier_id IN (:carrierIds) ORDER BY shipment_carrier_id, region`
	rows, err := storeutil.QueryListNamed[regionRow](ctx, db, query, map[string]any{
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

func fetchShipmentCarrierPrices(ctx context.Context, db dependency.DB, carrierIds []int) (map[int][]entity.ShipmentCarrierPrice, error) {
	if len(carrierIds) == 0 {
		return map[int][]entity.ShipmentCarrierPrice{}, nil
	}

	query := `SELECT id, shipment_carrier_id, currency, price, created_at, updated_at FROM shipment_carrier_price WHERE shipment_carrier_id IN (:carrierIds) ORDER BY shipment_carrier_id, currency`

	prices, err := storeutil.QueryListNamed[entity.ShipmentCarrierPrice](ctx, db, query, map[string]any{
		"carrierIds": carrierIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get shipment carrier prices: %w", err)
	}

	priceMap := make(map[int][]entity.ShipmentCarrierPrice)
	for _, p := range prices {
		priceMap[p.ShipmentCarrierId] = append(priceMap[p.ShipmentCarrierId], p)
	}

	return priceMap, nil
}
