package metrics

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ListVatRates returns every configured destination-country VAT rate, ordered by country code.
func (s *Store) ListVatRates(ctx context.Context) ([]entity.VatRate, error) {
	rows, err := storeutil.QueryListNamed[entity.VatRate](ctx, s.DB,
		`SELECT country_code, rate_pct, valid_from FROM vat_rate ORDER BY country_code`, nil)
	if err != nil {
		return nil, fmt.Errorf("list vat rates: %w", err)
	}
	return rows, nil
}

// UpsertVatRates inserts or updates the standard VAT rate for each given country (keyed by
// country_code). valid_from is written when non-zero, otherwise it keeps its existing value on
// update or defaults to today on insert. Callers validate the rate/country before calling.
func (s *Store) UpsertVatRates(ctx context.Context, rates []entity.VatRate) error {
	for _, r := range rates {
		cc := strings.ToUpper(strings.TrimSpace(r.CountryCode))
		params := map[string]any{"cc": cc, "rate": r.RatePct}
		q := `INSERT INTO vat_rate (country_code, rate_pct) VALUES (:cc, :rate)
		      ON DUPLICATE KEY UPDATE rate_pct = VALUES(rate_pct)`
		if !r.ValidFrom.IsZero() {
			params["vf"] = r.ValidFrom
			q = `INSERT INTO vat_rate (country_code, rate_pct, valid_from) VALUES (:cc, :rate, :vf)
			     ON DUPLICATE KEY UPDATE rate_pct = VALUES(rate_pct), valid_from = VALUES(valid_from)`
		}
		if err := storeutil.ExecNamed(ctx, s.DB, q, params); err != nil {
			return fmt.Errorf("upsert vat rate %s: %w", cc, err)
		}
	}
	return nil
}
