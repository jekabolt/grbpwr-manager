package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

type settingsStore struct {
	*MYSQLStore
}

// Settings returns an object implementing Settings interface
func (ms *MYSQLStore) Settings() dependency.Settings {
	return &settingsStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) SetShipmentCarrierAllowance(ctx context.Context, carrier string, allowance bool) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `UPDATE shipment_carrier SET allowed = :allowed WHERE carrier = :carrier`
		err := ExecNamed(ctx, ms.DB(), query, map[string]any{
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

func (ms *MYSQLStore) SetShipmentCarrierPrices(ctx context.Context, carrier string, prices map[string]decimal.Decimal) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Get carrier ID
		type CarrierId struct {
			Id int `db:"id"`
		}
		query := `SELECT id FROM shipment_carrier WHERE carrier = :carrier`
		carrierResult, err := QueryNamedOne[CarrierId](ctx, ms.DB(), query, map[string]any{"carrier": carrier})
		if err != nil {
			return fmt.Errorf("failed to get shipment carrier ID: %w", err)
		}
		carrierId := carrierResult.Id

		// Upsert prices for each currency
		for currency, price := range prices {
			upsertQuery := `
				INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price)
				VALUES (:carrierId, :currency, :price)
				ON DUPLICATE KEY UPDATE price = :price, updated_at = NOW()`
			err := ExecNamed(ctx, ms.DB(), upsertQuery, map[string]any{
				"carrierId": carrierId,
				"currency":  currency,
				"price":     price,
			})
			if err != nil {
				return fmt.Errorf("failed to upsert shipment carrier price for currency %s: %w", currency, err)
			}
		}

		// Refresh cache
		carriers, err := ms.getShipmentCarriers(ctx)
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

func (ms *MYSQLStore) SetPaymentMethodAllowance(ctx context.Context, paymentMethod entity.PaymentMethodName, allowance bool) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `UPDATE payment_method SET allowed = :allowed WHERE name = :method`
		err := ExecNamed(ctx, ms.DB(), query, map[string]any{
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

func (ms *MYSQLStore) SetSiteAvailability(ctx context.Context, available bool) error {
	cache.SetSiteAvailability(available)
	return nil
}

func (ms *MYSQLStore) SetMaxOrderItems(ctx context.Context, count int) error {
	cache.SetMaxOrderItems(count)
	return nil
}

func (ms *MYSQLStore) SetBigMenu(ctx context.Context, bigMenu bool) error {
	cache.SetBigMenu(bigMenu)
	return nil
}

func (ms *MYSQLStore) SetAnnounce(ctx context.Context, link string, translations []entity.AnnounceTranslation) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Update link
		linkQuery := `UPDATE announce SET link = :link WHERE id = 1`
		err := ExecNamed(ctx, ms.DB(), linkQuery, map[string]any{
			"link": link,
		})
		if err != nil {
			return fmt.Errorf("failed to update announce link: %w", err)
		}

		// Delete all existing announce translations
		deleteQuery := `DELETE FROM announce_translation`
		err = ExecNamed(ctx, ms.DB(), deleteQuery, map[string]any{})
		if err != nil {
			return fmt.Errorf("failed to delete existing announce translations: %w", err)
		}

		// Insert new translations
		for _, translation := range translations {
			insertQuery := `INSERT INTO announce_translation (language_id, text) VALUES (:languageId, :text)`
			err := ExecNamed(ctx, ms.DB(), insertQuery, map[string]any{
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

func (ms *MYSQLStore) GetAnnounce(ctx context.Context) (*entity.AnnounceWithTranslations, error) {
	// Get the announce link
	linkQuery := `SELECT link FROM announce WHERE id = 1`
	announce, err := QueryNamedOne[entity.Announce](ctx, ms.DB(), linkQuery, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get announce link: %w", err)
	}

	// Get the translations
	translationsQuery := `SELECT id, language_id, text, created_at, updated_at FROM announce_translation ORDER BY language_id`
	translations, err := QueryListNamed[entity.AnnounceTranslation](ctx, ms.DB(), translationsQuery, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get announce translations: %w", err)
	}

	return &entity.AnnounceWithTranslations{
		Link:         announce.Link,
		Translations: translations,
	}, nil
}
