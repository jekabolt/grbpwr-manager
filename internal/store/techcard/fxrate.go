package techcard

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// GetCostingFxRatesToBase returns the effective manual FX rate per currency (the latest one
// dated on or before today), keyed by UPPERCASE ISO currency → how many base-currency units
// one unit is worth. Used to fold a multi-currency tech-card costing into the base currency.
func (s *Store) GetCostingFxRatesToBase(ctx context.Context) (map[string]decimal.Decimal, error) {
	rows, err := storeutil.QueryListNamed[entity.CostingFxRate](ctx, s.DB, `
		SELECT f.currency, f.rate_to_base, f.valid_from
		FROM costing_fx_rate f
		JOIN (
			SELECT currency, MAX(valid_from) AS vf
			FROM costing_fx_rate
			WHERE valid_from <= CURDATE()
			GROUP BY currency
		) m ON m.currency = f.currency AND m.vf = f.valid_from`,
		map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("get costing fx rates: %w", err)
	}
	out := make(map[string]decimal.Decimal, len(rows))
	for _, r := range rows {
		out[strings.ToUpper(r.Currency)] = r.RateToBase
	}
	return out, nil
}

// ListCostingFxRates returns every stored rate (all effective dates), newest first, for the
// admin management surface.
func (s *Store) ListCostingFxRates(ctx context.Context) ([]entity.CostingFxRate, error) {
	rows, err := storeutil.QueryListNamed[entity.CostingFxRate](ctx, s.DB,
		`SELECT currency, rate_to_base, valid_from FROM costing_fx_rate ORDER BY currency, valid_from DESC`,
		map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list costing fx rates: %w", err)
	}
	return rows, nil
}

// UpsertCostingFxRates inserts or updates rates by (currency, valid_from). An empty slice is a
// no-op.
func (s *Store) UpsertCostingFxRates(ctx context.Context, rates []entity.CostingFxRate) error {
	for _, r := range rates {
		if err := storeutil.ExecNamed(ctx, s.DB, `
			INSERT INTO costing_fx_rate (currency, rate_to_base, valid_from)
			VALUES (:currency, :rate, :validFrom)
			ON DUPLICATE KEY UPDATE rate_to_base = VALUES(rate_to_base)`,
			map[string]any{
				"currency":  strings.ToUpper(r.Currency),
				"rate":      r.RateToBase,
				"validFrom": r.ValidFrom,
			}); err != nil {
			return fmt.Errorf("upsert costing fx rate %s: %w", r.Currency, err)
		}
	}
	return nil
}
