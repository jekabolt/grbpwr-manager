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
func (ms *MYSQLStore) SetShipmentCarrierPrice(ctx context.Context, carrier string, price decimal.Decimal) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		query := `UPDATE shipment_carrier SET price = :price WHERE carrier = :carrier`
		err := ExecNamed(ctx, ms.DB(), query, map[string]any{
			"carrier": carrier,
			"price":   price,
		})
		if err != nil {
			return fmt.Errorf("failed to update shipment carrier price: %w", err)
		}
		cache.UpdateShipmentCarrierCost(carrier, price)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier price: %w", err)
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

func (ms *MYSQLStore) SetAnnounceTranslations(ctx context.Context, translations []entity.AnnounceTranslation) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// First, delete all existing announce translations
		query := `DELETE FROM announce_translation`
		err := ExecNamed(ctx, ms.DB(), query, map[string]any{})
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

		cache.SetAnnounceTranslations(translations)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update announce translations: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) GetAnnounceTranslations(ctx context.Context) ([]entity.AnnounceTranslation, error) {
	query := `SELECT id, language_id, text, created_at, updated_at FROM announce_translation ORDER BY language_id`

	translations, err := QueryListNamed[entity.AnnounceTranslation](ctx, ms.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get announce translations: %w", err)
	}

	return translations, nil
}
