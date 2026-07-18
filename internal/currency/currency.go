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
	"PLN": decimal.NewFromFloat(2.00),
	// USDT is a priced/accounting currency, NOT a Stripe-chargeable one (IsStripeChargeable is
	// false for it; USDT orders are settled manually off-Stripe). Its entry here is the price floor
	// used by ValidateMinimum for product prices / order totals, mirroring the USD stablecoin peg — it
	// never becomes a Stripe charge. Keeping it in this map also keeps the requiredCurrencies⇔
	// minimumAmounts sync invariant intact (see requiredCurrencies).
	"USDT": decimal.NewFromFloat(0.50),
}

// symbols maps ISO 4217 codes to their display symbol for the currencies the
// shop supports. Unknown codes fall back to the uppercased code itself.
var symbols = map[string]string{
	"EUR":  "€",
	"USD":  "$",
	"GBP":  "£",
	"JPY":  "¥",
	"CNY":  "¥",
	"KRW":  "₩",
	"PLN":  "zł",
	"USDT": "₮",
}

// requiredCurrencies is the ordered set of currencies that every product price list and every
// shipping-carrier price map MUST provide — the single source of truth for "the price set is
// complete" checks (previously duplicated as a map in store/product and a slice in
// apisrv/admin/shipment). It mirrors the keys of minimumAmounts (the supported set); a test asserts
// the two stay in sync so a currency can't be added to one without the other.
//
// USDT is REQUIRED/priced but NOT Stripe-chargeable: every product and carrier must carry a USDT
// price (so a colourway can go ACTIVE and the storefront can quote USDT), yet a USDT order is settled
// manually off-Stripe and is rejected at the Stripe boundary (see IsStripeChargeable). "Required to be
// priced" and "chargeable via Stripe" are therefore distinct sets — the decoupling this currency
// introduces.
var requiredCurrencies = []string{"EUR", "USD", "GBP", "JPY", "CNY", "KRW", "PLN", "USDT"}

// RequiredCurrencies returns, as a fresh slice, the currencies every price list must include.
func RequiredCurrencies() []string {
	return append([]string(nil), requiredCurrencies...)
}

// MissingRequired returns the required currencies absent from provided (a case-insensitive set of
// currency codes the caller has already collected), in canonical order. Empty means the set is
// complete.
func MissingRequired(provided map[string]bool) []string {
	var missing []string
	for _, c := range requiredCurrencies {
		if !provided[c] {
			missing = append(missing, c)
		}
	}
	return missing
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

// nonStripeCurrencies are priced/accounting currencies the shop lists prices in and may record
// orders in, but CANNOT charge through Stripe — they are settled manually off-Stripe. USDT (a USD
// stablecoin) is the only one today: products carry a USDT price and an order may be booked in USDT,
// but Stripe cannot charge it. Membership here is what makes a currency "priced but not chargeable".
var nonStripeCurrencies = map[string]bool{
	"USDT": true,
}

// IsSupported reports whether the shop recognises and prices this currency — i.e. it is a required/
// priced currency with a configured minimum. It is NOT the same as "chargeable via Stripe": a
// supported currency may be settled manually off-Stripe (USDT). Gate manual order-currency acceptance
// and price validation on this; gate the Stripe payment path on IsStripeChargeable instead. The set of
// currencies with a configured minimum (minimumAmounts) is the source of truth.
func IsSupported(c string) bool {
	_, ok := minimumAmounts[strings.ToUpper(c)]
	return ok
}

// IsStripeChargeable reports whether an order in this currency can be charged through Stripe. It is
// strictly narrower than IsSupported: the currency must be supported AND not one of the manually-
// settled nonStripeCurrencies (USDT). The Stripe payment boundary (createPaymentIntent /
// CreatePreOrderPaymentIntent) MUST gate on this, never on IsSupported, so a USDT order is never sent
// to Stripe.
func IsStripeChargeable(c string) bool {
	return IsSupported(c) && !nonStripeCurrencies[strings.ToUpper(c)]
}

// ValidateMinimum returns an error if the currency is not one the shop supports,
// or if price is below its minimum. It fails closed: an unknown currency used to
// return nil here (Minimum -> 0 -> "no minimum"), silently bypassing the
// Stripe-minimum guard for any code outside the supported set.
func ValidateMinimum(price decimal.Decimal, c string) error {
	min, ok := minimumAmounts[strings.ToUpper(c)]
	if !ok {
		return fmt.Errorf("unsupported currency %q", c)
	}
	if price.LessThan(min) {
		return fmt.Errorf("%s price %s is below minimum %s", c, price.String(), min.String())
	}
	return nil
}
