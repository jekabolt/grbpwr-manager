package currency

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestDecimalPlaces(t *testing.T) {
	cases := map[string]int32{
		"JPY": 0, "KRW": 0, "krw": 0, // zero-decimal, case-insensitive
		"EUR": 2, "USD": 2, "gbp": 2, "CNY": 2, "PLN": 2,
		"USDT": 2, "usdt": 2, // 4-char accounting currency, two-decimal, case-insensitive (symbol/decimals retained)
		"KWD": 3, "bhd": 3, // three-decimal
	}
	for c, want := range cases {
		if got := DecimalPlaces(c); got != want {
			t.Errorf("DecimalPlaces(%q) = %d, want %d", c, got, want)
		}
	}
}

func TestValidateMinimumFailsClosed(t *testing.T) {
	// Supported currency at/above minimum: ok.
	if err := ValidateMinimum(decimal.NewFromFloat(10), "EUR"); err != nil {
		t.Errorf("EUR 10 should pass: %v", err)
	}
	// Supported currency below minimum: rejected.
	if err := ValidateMinimum(decimal.NewFromFloat(0.10), "EUR"); err == nil {
		t.Error("EUR 0.10 should be rejected as below minimum")
	}
	// Unsupported currency: rejected (previously returned nil, bypassing the guard).
	if err := ValidateMinimum(decimal.NewFromFloat(1000), "CHF"); err == nil {
		t.Error("unsupported currency CHF should be rejected")
	}
	// USDT is expense/accounting-only, NOT a selling currency: it has no selling minimum and must be
	// rejected here just like any other unsupported currency.
	if err := ValidateMinimum(decimal.NewFromFloat(1000), "USDT"); err == nil {
		t.Error("USDT is not a selling currency and must be rejected by ValidateMinimum")
	}
}

// TestRequiredCurrenciesMatchSupported guards against drift: the "price set is complete" required list
// (single source of truth for product prices + shipping carriers) must equal the supported
// (minimumAmounts) currency set, so a currency can't be required without being priced, or priced
// without being required. USDT is in NEITHER set — it is an expense/accounting-only currency (see
// TestIsExpenseCurrency), never sold.
func TestRequiredCurrenciesMatchSupported(t *testing.T) {
	required := RequiredCurrencies()
	if len(required) != len(minimumAmounts) {
		t.Fatalf("required currencies (%d) and supported/minimumAmounts (%d) differ in size", len(required), len(minimumAmounts))
	}
	for _, c := range required {
		if !IsSupported(c) {
			t.Errorf("required currency %q is not in the supported (minimumAmounts) set", c)
		}
		if c == "USDT" {
			t.Error("USDT must NOT be a required/selling currency (it is expense/accounting-only)")
		}
	}
	// MissingRequired reports the canonical-ordered gap. USDT is NOT required, so it never appears.
	got := MissingRequired(map[string]bool{"EUR": true, "USD": true})
	want := []string{"GBP", "JPY", "CNY", "KRW", "PLN"}
	if len(got) != len(want) {
		t.Fatalf("MissingRequired = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MissingRequired = %v, want %v", got, want)
		}
	}
}

func TestIsSupported(t *testing.T) {
	// The seven SELLING currencies are supported (case-insensitive).
	for _, c := range []string{"EUR", "usd", "GBP", "JPY", "KRW", "CNY", "PLN"} {
		if !IsSupported(c) {
			t.Errorf("IsSupported(%q) = false, want true", c)
		}
	}
	// USDT is expense/accounting-only and is NOT supported/sellable; CHF/KWD/"" are unknown.
	for _, c := range []string{"USDT", "usdt", "CHF", "KWD", ""} {
		if IsSupported(c) {
			t.Errorf("IsSupported(%q) = true, want false", c)
		}
	}
}

// TestIsExpenseCurrency pins the expense-vs-selling split: an EXPENSE currency is any selling currency
// (IsSupported) PLUS USDT. USDT is accepted as an expense currency yet is NOT a selling currency — the
// asymmetry this whole change-set enforces. Unknown codes are neither.
func TestIsExpenseCurrency(t *testing.T) {
	// Every selling currency is also a valid expense currency.
	for _, c := range RequiredCurrencies() {
		if !IsExpenseCurrency(c) {
			t.Errorf("IsExpenseCurrency(%q) = false, want true (every selling currency is an expense currency)", c)
		}
	}
	// USDT is a valid EXPENSE currency (case-insensitive) but NOT a selling currency.
	for _, c := range []string{"USDT", "usdt"} {
		if !IsExpenseCurrency(c) {
			t.Errorf("IsExpenseCurrency(%q) = false, want true (USDT is expense-only)", c)
		}
		if IsSupported(c) {
			t.Errorf("IsSupported(%q) = true, want false (USDT must not be a selling currency)", c)
		}
	}
	// Unknown / empty codes are neither expense nor selling currencies.
	for _, c := range []string{"CHF", "KWD", "XYZ", ""} {
		if IsExpenseCurrency(c) {
			t.Errorf("IsExpenseCurrency(%q) = true, want false", c)
		}
	}
}

// TestSellingCurrenciesChargeableUSDTIsNot pins the Stripe surface: every selling/required currency is
// Stripe-chargeable (nothing priced-yet-unchargeable remains), and USDT — no longer a supported/order
// currency — is not chargeable. The Stripe-boundary guards on IsStripeChargeable are kept as harmless
// dead code; this asserts they never wrongly reject a real selling currency.
func TestSellingCurrenciesChargeableUSDTIsNot(t *testing.T) {
	for _, c := range RequiredCurrencies() {
		if !IsStripeChargeable(c) {
			t.Errorf("IsStripeChargeable(%q) = false, want true (every selling currency must be chargeable)", c)
		}
	}
	// USDT and unsupported currencies are never chargeable.
	for _, c := range []string{"USDT", "usdt", "CHF"} {
		if IsStripeChargeable(c) {
			t.Errorf("IsStripeChargeable(%q) = true, want false", c)
		}
	}
}

// TestActiveGateDoesNotRequireUSDT mirrors the colourway ->ACTIVE completeness gate
// (store/product/lifecycle.go: checkColorwayRequiredCurrencies), which builds the provided set from a
// colourway's persisted product_price rows and flags whatever MissingRequired returns. A colourway
// priced in every SELLING currency (no USDT) must have NO missing required currency, so it can go
// ACTIVE without any USDT price — the corrected model (this replaces the old TestActiveGateRequiresUSDT,
// which wrongly required USDT).
func TestActiveGateDoesNotRequireUSDT(t *testing.T) {
	selling := map[string]bool{"EUR": true, "USD": true, "GBP": true, "JPY": true, "CNY": true, "KRW": true, "PLN": true}
	if missing := MissingRequired(selling); len(missing) != 0 {
		t.Fatalf("MissingRequired(all selling currencies) = %v, want [] (USDT must NOT be required)", missing)
	}
	// Adding a USDT price changes nothing — it is not part of the required set.
	selling["USDT"] = true
	if missing := MissingRequired(selling); len(missing) != 0 {
		t.Fatalf("MissingRequired(selling + USDT) = %v, want []", missing)
	}
	// Dropping a genuine selling currency (PLN) is still reported as missing.
	delete(selling, "PLN")
	delete(selling, "USDT")
	missing := MissingRequired(selling)
	if len(missing) != 1 || missing[0] != "PLN" {
		t.Fatalf("MissingRequired(missing PLN) = %v, want [PLN]", missing)
	}
}
