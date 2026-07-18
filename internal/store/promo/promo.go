// Package promo implements promotional code management operations.
package promo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Store implements dependency.Promo.
type Store struct {
	storeutil.Base
}

// New creates a new promo store.
func New(base storeutil.Base) *Store {
	return &Store{Base: base}
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.UTC().Location())
}

// AddPromo adds a new promo code.
func (s *Store) AddPromo(ctx context.Context, promo *entity.PromoCodeInsert) error {
	expiration := startOfDay(promo.Expiration)
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, `
	INSERT INTO promo_code (code, free_shipping, discount, expiration, start, voucher, allowed) VALUES
		(:code, :freeShipping, :discount, :expiration, :start, :voucher, :allowed)`, map[string]any{
		"code":         promo.Code,
		"freeShipping": promo.FreeShipping,
		"discount":     promo.Discount,
		"expiration":   expiration,
		"start":        promo.Start,
		"voucher":      promo.Voucher,
		"allowed":      promo.Allowed,
	})
	if err != nil {
		return fmt.Errorf("failed to add promo code: %w", err)
	}
	cache.AddPromo(entity.PromoCode{
		Id:              id,
		PromoCodeInsert: *promo,
	})

	return nil
}

// UpdatePromoCode updates an existing promo code in place, identified by promo.Code. It replaces the
// mutable fields (free_shipping / discount / expiration / start / voucher / allowed — including
// re-enabling a disabled code via allowed=true) without touching the row's id, so no usage/creation
// data is lost (the wave-1 delete-then-recreate workaround dropped it). A code that does not exist is
// reported as sql.ErrNoRows (mapped to NOT_FOUND upstream).
func (s *Store) UpdatePromoCode(ctx context.Context, promo *entity.PromoCodeInsert) error {
	expiration := startOfDay(promo.Expiration)
	rows, err := storeutil.ExecNamedRows(ctx, s.DB, `
	UPDATE promo_code
	SET free_shipping = :freeShipping, discount = :discount, expiration = :expiration,
	    start = :start, voucher = :voucher, allowed = :allowed
	WHERE code = :code`, map[string]any{
		"code":         promo.Code,
		"freeShipping": promo.FreeShipping,
		"discount":     promo.Discount,
		"expiration":   expiration,
		"start":        promo.Start,
		"voucher":      promo.Voucher,
		"allowed":      promo.Allowed,
	})
	if err != nil {
		return fmt.Errorf("failed to update promo code: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	// Refresh the in-memory cache from DB truth so the row's id and the normalized expiration stay in
	// sync (the promo cache is keyed by code, so this replaces the existing entry in place).
	updated, err := storeutil.QueryNamedOne[entity.PromoCode](ctx, s.DB,
		`SELECT * FROM promo_code WHERE code = :code`, map[string]any{"code": promo.Code})
	if err != nil {
		return fmt.Errorf("failed to reload updated promo code: %w", err)
	}
	cache.AddPromo(updated)

	return nil
}

// ListPromos returns a paginated list of promo codes.
func (s *Store) ListPromos(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.PromoCode, error) {
	query := fmt.Sprintf(`
	SELECT * FROM promo_code
	ORDER BY id %s
	LIMIT :limit OFFSET :offset`, orderFactor.String())

	promos, err := storeutil.QueryListNamed[entity.PromoCode](ctx, s.DB, query, map[string]any{
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get PromoCode list: %w", err)
	}

	return promos, nil
}

// DeletePromoCode deletes a promo code by its code.
func (s *Store) DeletePromoCode(ctx context.Context, code string) error {
	err := storeutil.ExecNamed(ctx, s.DB, `DELETE FROM promo_code WHERE code = :code`, map[string]any{
		"code": code,
	})
	if err != nil {
		return fmt.Errorf("failed to delete promo code: %w", err)
	}
	cache.DeletePromo(code)

	return nil
}

// DisablePromoCode disables a promo code by its code.
func (s *Store) DisablePromoCode(ctx context.Context, code string) error {
	err := storeutil.ExecNamed(ctx, s.DB, `UPDATE promo_code SET allowed = false WHERE code = :code`, map[string]any{
		"code": code,
	})
	if err != nil {
		return fmt.Errorf("failed to disable promo code: %w", err)
	}
	cache.DisablePromo(code)

	return nil
}

// DisableVoucher disables a voucher promo code if applicable.
func (s *Store) DisableVoucher(ctx context.Context, promoID sql.NullInt32) error {
	if !promoID.Valid || promoID.Int32 == 0 {
		return nil
	}

	promo, ok := cache.GetPromoById(int(promoID.Int32))
	if !ok {
		return nil
	}

	if promo.Voucher {
		// DisablePromoCode already updates the in-memory cache.
		if err := s.DisablePromoCode(ctx, promo.Code); err != nil {
			return fmt.Errorf("failed to disable voucher: %w", err)
		}
	}

	return nil
}
