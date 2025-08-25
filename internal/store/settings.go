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

func (ms *MYSQLStore) SetAnnounce(ctx context.Context, announce string) error {
	cache.SetAnnounce(announce)
	return nil
}
