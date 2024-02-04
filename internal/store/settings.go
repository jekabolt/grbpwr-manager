package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
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
		err := ms.cache.UpdateShipmentCarrierAllowance(carrier, allowance)
		if err != nil {
			return fmt.Errorf("failed to update shipment carrier allowance: %w", err)
		}
		query := `UPDATE shipment_carrier SET allowed = :allowed WHERE carrier = :carrier`
		err = ExecNamed(ctx, ms.DB(), query, map[string]any{
			"carrier": carrier,
			"allowed": allowance,
		})
		if err != nil {
			return fmt.Errorf("failed to update shipment carrier allowance: %w", err)
		}
		return nil

	})
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier allowance: %w", err)
	}
	return nil

}
func (ms *MYSQLStore) SetShipmentCarrierPrice(ctx context.Context, carrier string, price decimal.Decimal) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		err := ms.cache.UpdateShipmentCarrierCost(carrier, price)
		if err != nil {
			return fmt.Errorf("failed to update shipment carrier price: %w", err)
		}

		query := `UPDATE shipment_carrier SET price = :price WHERE carrier = :carrier`
		err = ExecNamed(ctx, ms.DB(), query, map[string]any{
			"carrier": carrier,
			"price":   price,
		})
		if err != nil {
			return fmt.Errorf("failed to update shipment carrier price: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update shipment carrier price: %w", err)
	}
	return nil
}
func (ms *MYSQLStore) SetPaymentMethodAllowance(ctx context.Context, paymentMethod string, allowance bool) error {
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		err := ms.cache.UpdatePaymentMethodAllowance(paymentMethod, allowance)
		if err != nil {
			return fmt.Errorf("failed to update payment method allowance: %w", err)
		}
		query := `UPDATE payment_method SET allowed = :allowed WHERE method = :method`
		err = ExecNamed(ctx, ms.DB(), query, map[string]any{
			"method":  paymentMethod,
			"allowed": allowance,
		})
		if err != nil {
			return fmt.Errorf("failed to update payment method allowance: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update payment method allowance: %w", err)
	}
	return nil
}

// TODO:
func (ms *MYSQLStore) SetSiteAvailability(ctx context.Context, available bool) error {
	ms.cache.SetSiteAvailability(available)
	return nil
}
