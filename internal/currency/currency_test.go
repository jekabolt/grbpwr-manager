package currency

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestDecimalPlaces(t *testing.T) {
	cases := map[string]int32{
		"JPY": 0, "KRW": 0, "krw": 0, // zero-decimal, case-insensitive
		"EUR": 2, "USD": 2, "gbp": 2, "CNY": 2, "PLN": 2,
		"USDT": 2, "usdt": 2, // 4-char accounting currency, two-decimal, case-insensitive
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
// (minimumAmounts) currency set, so a currency can't be required without being priced, or priced
// without being required. NOTE: "supported/priced" is NOT "Stripe-chargeable" — USDT is in both sets
// here yet is not chargeable via Stripe (see TestPricedButNotStripeChargeable).
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
	// MissingRequired reports the canonical-ordered gap. USDT is required too, so it appears last.
	got := MissingRequired(map[string]bool{"EUR": true, "USD": true})
	want := []string{"GBP", "JPY", "CNY", "KRW", "PLN", "USDT"}
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
	// USDT is supported/priced (it has a minimum and is a required currency) even though it is not
	// Stripe-chargeable.
	for _, c := range []string{"EUR", "usd", "GBP", "JPY", "KRW", "CNY", "PLN", "USDT", "usdt"} {
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

// TestPricedButNotStripeChargeable pins the priced-vs-chargeable decoupling: USDT is a required,
// supported, priced currency, yet must NOT be chargeable through Stripe (it is settled manually).
// Every OTHER required currency must be Stripe-chargeable, so the non-chargeable set stays exactly
// {USDT} and no future required currency is silently made non-chargeable.
func TestPricedButNotStripeChargeable(t *testing.T) {
	// USDT: priced/supported but not Stripe-chargeable.
	if !IsSupported("USDT") {
		t.Error("USDT should be supported/priced")
	}
	if IsStripeChargeable("USDT") || IsStripeChargeable("usdt") {
		t.Error("USDT must NOT be Stripe-chargeable (settled manually)")
	}
	// Every required currency other than USDT must be Stripe-chargeable.
	for _, c := range RequiredCurrencies() {
		wantChargeable := c != "USDT"
		if got := IsStripeChargeable(c); got != wantChargeable {
			t.Errorf("IsStripeChargeable(%q) = %v, want %v", c, got, wantChargeable)
		}
	}
	// An unsupported currency is never chargeable.
	if IsStripeChargeable("CHF") {
		t.Error("unsupported CHF must not be Stripe-chargeable")
	}
}

// TestActiveGateRequiresUSDT mirrors the colourway ->ACTIVE completeness gate
// (store/product/lifecycle.go: checkColorwayRequiredCurrencies), which builds the provided set from a
// colourway's persisted product_price rows and flags whatever MissingRequired returns. A colourway
// priced in every PRE-USDT currency but lacking USDT must now be reported as missing exactly USDT, so
// it cannot go ACTIVE — the consistency this change-set requires.
func TestActiveGateRequiresUSDT(t *testing.T) {
	preUSDT := map[string]bool{"EUR": true, "USD": true, "GBP": true, "JPY": true, "CNY": true, "KRW": true, "PLN": true}
	missing := MissingRequired(preUSDT)
	if len(missing) != 1 || missing[0] != "USDT" {
		t.Fatalf("MissingRequired(all-but-USDT) = %v, want [USDT]", missing)
	}
	// With USDT present too, the set is complete (gate passes).
	preUSDT["USDT"] = true
	if missing := MissingRequired(preUSDT); len(missing) != 0 {
		t.Fatalf("MissingRequired(complete incl. USDT) = %v, want []", missing)
	}
}
