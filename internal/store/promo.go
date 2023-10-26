package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
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

func (ms *MYSQLStore) AddPromo(ctx context.Context, promo *entity.PromoCodeInsert) error {
	err := ExecNamed(ctx, ms.DB(), `INSERT INTO promo_codes (code, free_shipping, discount, expiration, allowed) VALUES
		(:code, :freeShipping, :discount, :expiration, :allowed)`, map[string]any{
		"code":         promo.Code,
		"freeShipping": promo.FreeShipping,
		"discount":     promo.Discount,
		"expiration":   promo.Expiration,
		"allowed":      promo.Allowed,
	})
	if err != nil {
		return fmt.Errorf("failed to add promo code: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) ListPromos(ctx context.Context) ([]entity.PromoCode, error) {
	query := `
	SELECT * FROM promo_codes`
	promos, err := QueryListNamed[entity.PromoCode](ctx, ms.DB(), query, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("can't get PromoCode list: %w", err)
	}
	return promos, nil
}

func (ms *MYSQLStore) DeletePromoCode(ctx context.Context, code string) error {
	err := ExecNamed(ctx, ms.DB(), `DELETE FROM promo_codes WHERE code = :code`, map[string]any{
		"code": code,
	})
	if err != nil {
		return fmt.Errorf("failed to delete promo code: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) DisablePromoCode(ctx context.Context, code string) error {
	err := ExecNamed(ctx, ms.DB(), `UPDATE promo_codes SET allowed = false WHERE code = :code`, map[string]any{
		"code": code,
	})
	if err != nil {
		return fmt.Errorf("failed to disable promo code: %w", err)
	}
	return nil
}
