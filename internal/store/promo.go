package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.UTC().Location())
}
func (ms *MYSQLStore) AddPromo(ctx context.Context, promo *entity.PromoCodeInsert) error {
	expiration := startOfDay(promo.Expiration)
	id, err := ExecNamedLastId(ctx, ms.DB(), `
	INSERT INTO promo_code (code, free_shipping, discount, expiration, voucher, allowed) VALUES
		(:code, :freeShipping, :discount, :expiration, :voucher, :allowed)`, map[string]any{
		"code":         promo.Code,
		"freeShipping": promo.FreeShipping,
		"discount":     promo.Discount,
		"expiration":   expiration,
		"voucher":      promo.Voucher,
		"allowed":      promo.Allowed,
	})
	if err != nil {
		return fmt.Errorf("failed to add promo code: %w", err)
	}
	ms.cache.AddPromo(entity.PromoCode{
		ID:              id,
		PromoCodeInsert: *promo,
	})

	return nil
}

func (ms *MYSQLStore) ListPromos(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.PromoCode, error) {
	query := fmt.Sprintf(`
	SELECT * FROM promo_code
	ORDER BY id %s
	LIMIT :limit OFFSET :offset`, orderFactor.String())

	promos, err := QueryListNamed[entity.PromoCode](ctx, ms.DB(), query, map[string]interface{}{
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get PromoCode list: %w", err)
	}

	return promos, nil
}

func (ms *MYSQLStore) DeletePromoCode(ctx context.Context, code string) error {
	err := ExecNamed(ctx, ms.DB(), `DELETE FROM promo_code WHERE code = :code`, map[string]any{
		"code": code,
	})
	if err != nil {
		return fmt.Errorf("failed to delete promo code: %w", err)
	}
	ms.cache.DeletePromo(code)

	return nil
}

func (ms *MYSQLStore) DisablePromoCode(ctx context.Context, code string) error {
	err := ExecNamed(ctx, ms.DB(), `UPDATE promo_code SET allowed = false WHERE code = :code`, map[string]any{
		"code": code,
	})
	if err != nil {
		return fmt.Errorf("failed to disable promo code: %w", err)
	}
	ms.cache.DisablePromo(code)

	return nil
}

func (ms *MYSQLStore) DisableVoucher(ctx context.Context, promoID sql.NullInt32) error {
	if !promoID.Valid || promoID.Int32 == 0 {
		return nil
	}

	promo, ok := ms.cache.GetPromoById(int(promoID.Int32))
	if !ok {
		return nil
	}

	if promo.Voucher {
		err := ms.DisablePromoCode(ctx, promo.Code)
		if err != nil {
			return fmt.Errorf("failed to disable voucher: %w", err)
		}
		ms.cache.DisablePromo(promo.Code)
	}

	return nil
}
