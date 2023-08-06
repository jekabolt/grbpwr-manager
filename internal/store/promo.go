package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

type promoStore struct {
	*MYSQLStore
}

// Promo returns an object implementing Promo interface
func (ms *MYSQLStore) Promo() dependency.Promo {
	return &promoStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) AddPromo(ctx context.Context, promo *dto.PromoCode) error {
	_, err := ms.DB().ExecContext(ctx, `
		INSERT INTO promo_codes 
		(code, free_shipping, sale, expiration, allowed) VALUES 
		(?, ?, ?, ?, ?)`,
		promo.Code, promo.FreeShipping, promo.Sale, promo.Expiration, promo.Allowed)
	if err != nil {
		return fmt.Errorf("failed to insert promo code: %w", err)
	}
	return nil
}
func (ms *MYSQLStore) GetAllPromoCodes(ctx context.Context) ([]dto.PromoCode, error) {
	rows, err := ms.DB().QueryContext(ctx,
		`SELECT id, code, free_shipping, sale, expiration, allowed FROM promo_codes`)
	if err != nil {
		return nil, fmt.Errorf("failed to get all promo codes: %w", err)
	}
	defer rows.Close()
	promos := []dto.PromoCode{}
	for rows.Next() {
		promo := dto.PromoCode{}
		err := rows.Scan(&promo.ID, &promo.Code, &promo.FreeShipping, &promo.Sale, &promo.Expiration, &promo.Allowed)
		if err != nil {
			return nil, fmt.Errorf("failed to scan promo: %w", err)
		}
		promos = append(promos, promo)
	}
	return promos, nil
}
func (ms *MYSQLStore) DeletePromoCode(ctx context.Context, code string) error {
	_, err := ms.DB().ExecContext(ctx, `DELETE FROM promo_codes WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("failed to delete promo code: %w", err)
	}
	return nil
}
func (ms *MYSQLStore) GetPromoByCode(ctx context.Context, code string) (*dto.PromoCode, error) {
	row := ms.DB().QueryRowContext(ctx, `
		SELECT id, code, free_shipping, sale, expiration, allowed 
		FROM promo_codes WHERE code = ?`, code)
	promo := dto.PromoCode{}
	err := row.Scan(&promo.ID, &promo.Code, &promo.FreeShipping, &promo.Sale, &promo.Expiration, &promo.Allowed)
	if err != nil {
		return nil, fmt.Errorf("failed to scan promo: %w", err)
	}
	return &promo, nil
}

func (ms *MYSQLStore) DisablePromoCode(ctx context.Context, code string) error {
	_, err := ms.DB().ExecContext(ctx, `UPDATE promo_codes SET allowed = FALSE WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("failed to disable promo code: %w", err)
	}
	return nil
}
