package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/shopspring/decimal"
)

// MinimumAmountForCurrency returns the minimum charge amount for the currency, or zero if unknown.
func MinimumAmountForCurrency(c string) decimal.Decimal {
	return currency.Minimum(c)
}

// ValidatePriceMeetsMinimum returns an error if price is below the currency minimum.
func ValidatePriceMeetsMinimum(price decimal.Decimal, c string) error {
	return currency.ValidateMinimum(price, c)
}

// IsStripeChargeable reports whether an order in this currency can be charged through Stripe. Kept as
// a defence-in-depth guard: under the corrected model USDT is expense-only and never a selling/order
// currency, so this is now equivalent to IsSupported and the Stripe-boundary callers never actually
// reject on it (harmless dead code).
func IsStripeChargeable(c string) bool {
	return currency.IsStripeChargeable(c)
}

// IsExpenseCurrency reports whether c may denominate an EXPENSE/accounting amount: any selling
// currency plus USDT (accounting-only). This is the single predicate every expense-currency validator
// in this package uses. It is exposed as a thin wrapper so the validators can call it by bare name
// without the local `currency` string variable shadowing the currency package.
func IsExpenseCurrency(c string) bool {
	return currency.IsExpenseCurrency(c)
}

// IsZeroDecimalCurrency returns true for currencies with no decimal places (KRW, JPY, etc.)
func IsZeroDecimalCurrency(c string) bool {
	return currency.IsZeroDecimal(c)
}

// DecimalPlacesForCurrency returns the number of decimal places for the currency.
func DecimalPlacesForCurrency(c string) int32 {
	return currency.DecimalPlaces(c)
}

// RoundForCurrency rounds amount to the appropriate precision for the currency.
func RoundForCurrency(amount decimal.Decimal, c string) decimal.Decimal {
	return currency.Round(amount, c)
}

// CurrencySymbol returns the display symbol for the currency (e.g. "€", "$"),
// or the uppercased ISO code when no symbol is known.
func CurrencySymbol(c string) string {
	return currency.Symbol(c)
}
