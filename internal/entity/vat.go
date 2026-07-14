package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// VatRate is the standard VAT rate for a destination country (ISO 3166-1 alpha-2), used to
// compute net-of-VAT revenue. One row per country (the rate in force); ValidFrom is
// informational. A destination absent from the table is treated as 0% (export / non-EU).
type VatRate struct {
	CountryCode string          `db:"country_code"`
	RatePct     decimal.Decimal `db:"rate_pct"`
	ValidFrom   time.Time       `db:"valid_from"`
}

// VatFromInclusive returns the VAT portion contained in a VAT-inclusive gross amount at ratePct
// (e.g. gross 121 at 21% → 21). Returns zero for a non-positive rate.
func VatFromInclusive(gross, ratePct decimal.Decimal) decimal.Decimal {
	if ratePct.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	hundred := decimal.NewFromInt(100)
	return gross.Mul(ratePct).Div(hundred.Add(ratePct))
}
