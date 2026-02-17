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
