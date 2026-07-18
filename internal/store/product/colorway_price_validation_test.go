package product

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// fullRequiredPrices builds a valid, complete required-currency price set (amounts well above every
// per-currency minimum) — the "everything present and individually valid" baseline.
func fullRequiredPrices() []entity.ColorwayPriceInsert {
	var ps []entity.ColorwayPriceInsert
	for _, c := range currency.RequiredCurrencies() {
		ps = append(ps, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	return ps
}

// TestValidateColorwayPrices_AllowsPartialAndEmpty is the unit-level P0 assertion: the DRAFT-tolerant
// per-price validator does NOT require the full currency set (that gate moved to the →ACTIVE edge), so
// an empty or partial price list passes — which is what lets CreateColorway mint a DRAFT with no/partial
// prices instead of failing with "missing required currencies".
func TestValidateColorwayPrices_AllowsPartialAndEmpty(t *testing.T) {
	if err := validateColorwayPrices(nil); err != nil {
		t.Fatalf("empty prices must pass per-price validation (a draft may have none): %v", err)
	}
	partial := []entity.ColorwayPriceInsert{{Currency: "EUR", Price: decimal.NewFromInt(100)}}
	if err := validateColorwayPrices(partial); err != nil {
		t.Fatalf("partial prices must pass per-price validation (a draft may be incomplete): %v", err)
	}
}

// TestValidateColorwayPrices_RejectsInvalidAmounts asserts per-price validity is ALWAYS on: a supplied
// price that is zero/negative or below its currency minimum is rejected even though completeness is not.
func TestValidateColorwayPrices_RejectsInvalidAmounts(t *testing.T) {
	if err := validateColorwayPrices([]entity.ColorwayPriceInsert{{Currency: "EUR", Price: decimal.Zero}}); err == nil {
		t.Fatal("a zero price must be rejected by per-price validation")
	}
	if err := validateColorwayPrices([]entity.ColorwayPriceInsert{{Currency: "EUR", Price: decimal.NewFromInt(-1)}}); err == nil {
		t.Fatal("a negative price must be rejected by per-price validation")
	}
	// EUR minimum is 0.50; 0.01 is a present, positive, but below-minimum price.
	if err := validateColorwayPrices([]entity.ColorwayPriceInsert{{Currency: "EUR", Price: decimal.NewFromFloat(0.01)}}); err == nil {
		t.Fatal("a below-minimum price must be rejected by per-price validation")
	}
}

// TestValidateRequiredCurrenciesPresent_Completeness asserts the completeness gate: the full required
// set passes, and any missing required currency fails and is named in the error.
func TestValidateRequiredCurrenciesPresent_Completeness(t *testing.T) {
	if err := validateRequiredCurrenciesPresent(fullRequiredPrices()); err != nil {
		t.Fatalf("the full required currency set must satisfy the completeness gate: %v", err)
	}
	if err := validateRequiredCurrenciesPresent(nil); err == nil {
		t.Fatal("an empty price set must fail the completeness gate")
	}

	req := currency.RequiredCurrencies()
	if len(req) < 2 {
		t.Fatalf("expected at least two required currencies, got %v", req)
	}
	dropped := req[len(req)-1]
	var partial []entity.ColorwayPriceInsert
	for _, c := range req[:len(req)-1] {
		partial = append(partial, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	err := validateRequiredCurrenciesPresent(partial)
	if err == nil {
		t.Fatalf("dropping required currency %s must fail the completeness gate", dropped)
	}
	if !strings.Contains(err.Error(), dropped) {
		t.Fatalf("completeness error must name the missing currency %s: %v", dropped, err)
	}
}

// TestValidateRequiredCurrencies_FullContractUnchanged asserts the legacy AddProduct/UpdateProduct
// validator still enforces BOTH completeness and per-price validity (completeness reported first), so
// the direct-to-ACTIVE legacy write paths are unchanged by the gate move.
func TestValidateRequiredCurrencies_FullContractUnchanged(t *testing.T) {
	if err := validateRequiredCurrencies(fullRequiredPrices()); err != nil {
		t.Fatalf("a full, valid set must pass the legacy full validator: %v", err)
	}
	partial := []entity.ColorwayPriceInsert{{Currency: "EUR", Price: decimal.NewFromInt(100)}}
	err := validateRequiredCurrencies(partial)
	if err == nil {
		t.Fatal("the legacy full validator must still reject an incomplete set")
	}
	if !strings.Contains(err.Error(), "missing required currencies") {
		t.Fatalf("incomplete set must report completeness first: %v", err)
	}
}
