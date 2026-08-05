package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ListVariantSeconds returns a colourway's B-grade (factory seconds) variants with their manual
// price lists (product_size_price, 0251), ordered by size. B rows are excluded from GetProduct and
// every storefront read, so this is the admin's only read surface for seconds stock. Variants with
// an empty price list are not sellable (fail-closed).
func (s *Store) ListVariantSeconds(ctx context.Context, productID int) ([]entity.SecondsVariant, error) {
	variants, err := storeutil.QueryListNamed[entity.Variant](ctx, s.DB,
		`SELECT id, quantity, product_id, size_id, sku, status, grade
		 FROM product_size WHERE product_id = :pid AND grade = 'B' ORDER BY size_id`,
		map[string]any{"pid": productID})
	if err != nil {
		return nil, fmt.Errorf("list seconds variants: %w", err)
	}
	if len(variants) == 0 {
		return []entity.SecondsVariant{}, nil
	}
	ids := make([]int, len(variants))
	for i := range variants {
		ids[i] = variants[i].Id
	}
	prices, err := storeutil.QueryListNamed[struct {
		VariantID int `db:"product_size_id"`
		entity.ColorwayPriceInsert
	}](ctx, s.DB,
		`SELECT product_size_id, currency, price FROM product_size_price
		 WHERE product_size_id IN (:ids) ORDER BY product_size_id, currency`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("list seconds variant prices: %w", err)
	}
	byVariant := make(map[int][]entity.ColorwayPriceInsert, len(variants))
	for _, p := range prices {
		byVariant[p.VariantID] = append(byVariant[p.VariantID], p.ColorwayPriceInsert)
	}
	out := make([]entity.SecondsVariant, len(variants))
	for i, v := range variants {
		out[i] = entity.SecondsVariant{Variant: v, Prices: byVariant[v.Id]}
	}
	return out, nil
}

// SetVariantPrice atomically replaces the manual price set of a B-grade variant (0251): the given
// prices become the variant's whole price list; an empty slice clears it, making the variant
// unsellable again (fail-closed). Grade 'A' targets are refused (entity.ErrVariantPriceNotSeconds)
// — A prices live on the colourway. A non-empty set must pass the per-price catalogue rules
// (selling currency, positive, currency minimum — validateColorwayPrices) and include the base
// currency: order lines snapshot the B base price, and without it metrics would silently borrow
// the A-grade catalogue price. Unlike A prices there is NO required-currency completeness — a B
// variant priced only in EUR simply doesn't sell in other currencies.
func (s *Store) SetVariantPrice(ctx context.Context, variantID int, prices []entity.ColorwayPriceInsert) error {
	if len(prices) > 0 {
		if err := validateColorwayPrices(prices); err != nil {
			return &entity.ValidationError{Message: err.Error()}
		}
		base := strings.ToUpper(cache.GetBaseCurrency())
		seen := make(map[string]bool, len(prices))
		hasBase := false
		for _, p := range prices {
			cur := strings.ToUpper(p.Currency)
			if seen[cur] {
				return &entity.ValidationError{Message: fmt.Sprintf("duplicate price for currency %s", cur)}
			}
			seen[cur] = true
			if cur == base {
				hasBase = true
			}
		}
		if !hasBase {
			return &entity.ValidationError{Message: fmt.Sprintf("a non-empty price set must include the base currency %s (order lines snapshot the base price)", base)}
		}
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		row, err := storeutil.QueryNamedOne[struct {
			Grade string `db:"grade"`
		}](ctx, db, `SELECT grade FROM product_size WHERE id = :id FOR UPDATE`, map[string]any{"id": variantID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("variant %d: %w", variantID, sql.ErrNoRows)
			}
			return fmt.Errorf("lock variant %d for pricing: %w", variantID, err)
		}
		if row.Grade != entity.VariantGradeB {
			return fmt.Errorf("variant %d: %w", variantID, entity.ErrVariantPriceNotSeconds)
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM product_size_price WHERE product_size_id = :id`,
			map[string]any{"id": variantID}); err != nil {
			return fmt.Errorf("clear variant %d prices: %w", variantID, err)
		}
		if len(prices) == 0 {
			return nil
		}
		rows := make([]map[string]any, 0, len(prices))
		for _, p := range prices {
			cur := strings.ToUpper(p.Currency)
			rows = append(rows, map[string]any{
				"product_size_id": variantID,
				"currency":        cur,
				"price":           dto.RoundForCurrency(p.Price, cur),
			})
		}
		if err := storeutil.BulkInsert(ctx, db, "product_size_price", rows); err != nil {
			return fmt.Errorf("insert variant %d prices: %w", variantID, err)
		}
		return nil
	})
}
