package currency

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Zero-decimal currencies per Stripe/ISO 4217: no minor units (e.g. KRW, JPY)
var zeroDecimalCurrencies = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true,
	"JPY": true, "KMF": true, "KRW": true, "MGA": true,
	"PYG": true, "RWF": true, "UGX": true, "VND": true,
	"VUV": true, "XAF": true, "XOF": true, "XPF": true,
}

// Three-decimal currencies per ISO 4217 (minor unit = 1/1000): the smallest unit
// is a thousandth, so charging them requires x1000, not x100. None are currently
// configured for the shop, but deriving the factor from the exponent keeps the
// minor-unit conversion correct-by-construction if one is ever added.
var threeDecimalCurrencies = map[string]bool{
	"BHD": true, "IQD": true, "JOD": true, "KWD": true,
	"LYD": true, "OMR": true, "TND": true,
}

// Minimum charge amounts per currency (Stripe minimums for payment processing)
var minimumAmounts = map[string]decimal.Decimal{
	"EUR": decimal.NewFromFloat(0.50),
	"USD": decimal.NewFromFloat(0.50),
	"GBP": decimal.NewFromFloat(0.30),
	"JPY": decimal.NewFromInt(50),
	"KRW": decimal.NewFromInt(100),
	"CNY": decimal.NewFromFloat(1.00),
}

// symbols maps ISO 4217 codes to their display symbol for the currencies the
// shop supports. Unknown codes fall back to the uppercased code itself.
var symbols = map[string]string{
	"EUR": "€",
	"USD": "$",
	"GBP": "£",
	"JPY": "¥",
	"CNY": "¥",
	"KRW": "₩",
}

// Symbol returns the display symbol for the currency, or the uppercased ISO
// code when no symbol is known.
func Symbol(c string) string {
	if s, ok := symbols[strings.ToUpper(c)]; ok {
		return s
	}
	return strings.ToUpper(c)
}

// IsZeroDecimal returns true for currencies with no decimal places (KRW, JPY, etc.)
func IsZeroDecimal(c string) bool {
	return zeroDecimalCurrencies[strings.ToUpper(c)]
}

// DecimalPlaces returns the number of decimal places for the currency.
func DecimalPlaces(c string) int32 {
	switch {
	case IsZeroDecimal(c):
		return 0
	case threeDecimalCurrencies[strings.ToUpper(c)]:
		return 3
	default:
		return 2
	}
}

// Round rounds amount to the appropriate precision for the currency.
func Round(amount decimal.Decimal, c string) decimal.Decimal {
	return amount.Round(DecimalPlaces(c))
}

// Minimum returns the minimum charge amount for the currency, or zero if unknown.
func Minimum(c string) decimal.Decimal {
	if min, ok := minimumAmounts[strings.ToUpper(c)]; ok {
		return min
	}
	return decimal.Zero
}

// ValidateMinimum returns an error if price is below the currency minimum.
func ValidateMinimum(price decimal.Decimal, c string) error {
	min := Minimum(c)
	if min.IsZero() {
		return nil
	}
	if price.LessThan(min) {
		return fmt.Errorf("%s price %s is below minimum %s", c, price.String(), min.String())
	}
	return nil
}
