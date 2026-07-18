package currency

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestDecimalPlaces(t *testing.T) {
	cases := map[string]int32{
		"JPY": 0, "KRW": 0, "krw": 0, // zero-decimal, case-insensitive
		"EUR": 2, "USD": 2, "gbp": 2, "CNY": 2, "PLN": 2,
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
}

// TestRequiredCurrenciesMatchSupported guards against drift: the "price set is complete" required
// list (single source of truth for product prices + shipping carriers) must equal the supported
// (Stripe-minimum) currency set, so a currency can't be required without being chargeable, or
// chargeable without being required.
func TestRequiredCurrenciesMatchSupported(t *testing.T) {
	required := RequiredCurrencies()
	if len(required) != len(minimumAmounts) {
		t.Fatalf("required currencies (%d) and supported/minimumAmounts (%d) differ in size", len(required), len(minimumAmounts))
	}
	for _, c := range required {
		if !IsSupported(c) {
			t.Errorf("required currency %q is not in the supported (minimumAmounts) set", c)
		}
	}
	// MissingRequired reports the canonical-ordered gap.
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
	for _, c := range []string{"EUR", "usd", "GBP", "JPY", "KRW", "CNY", "PLN"} {
		if !IsSupported(c) {
			t.Errorf("IsSupported(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"CHF", "KWD", ""} {
		if IsSupported(c) {
			t.Errorf("IsSupported(%q) = true, want false", c)
		}
	}
}
