package order

import (
	"context"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// getVatRatePct resolves the destination VAT rate (percent) for a shipping country (ISO
// 3166-1 alpha-2, e.g. "DE") from the vat_rate config table. A malformed country or a
// destination absent from the table returns 0 — export / non-EU sales carry no EU VAT. Used
// to snapshot the rate onto each order at sale time.
func getVatRatePct(ctx context.Context, db dependency.DB, country string) (decimal.Decimal, error) {
	cc := strings.ToUpper(strings.TrimSpace(country))
	if len(cc) != 2 {
		return decimal.Zero, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		Rate decimal.Decimal `db:"rate_pct"`
	}](ctx, db, `SELECT rate_pct FROM vat_rate WHERE country_code = :cc LIMIT 1`,
		map[string]any{"cc": cc})
	if err != nil {
		return decimal.Zero, err
	}
	if len(rows) == 0 {
		return decimal.Zero, nil
	}
	return rows[0].Rate, nil
}
