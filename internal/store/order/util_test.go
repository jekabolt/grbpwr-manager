package order

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// TestGetProductPrice pins the order-time pricing invariant (PR5-D): the order currency must be
// present and its price positive; a missing or non-positive price fails the order rather than
// mispricing it at zero.
func TestGetProductPrice(t *testing.T) {
	prd := &entity.Colorway{
		Id: 7,
		Prices: []entity.ColorwayPrice{
			{Currency: "EUR", Price: decimal.NewFromInt(100)},
			{Currency: "USD", Price: decimal.Zero},
		},
	}

	// Present + positive (case-insensitive).
	if p, err := getProductPrice(prd, "eur"); err != nil || !p.Equal(decimal.NewFromInt(100)) {
		t.Errorf("EUR: got (%s, %v), want (100, nil)", p, err)
	}
	// Present but non-positive → error, not a zero sale.
	if _, err := getProductPrice(prd, "USD"); err == nil {
		t.Error("USD price 0 should be rejected as non-positive")
	}
	// Missing currency → error.
	if _, err := getProductPrice(prd, "GBP"); err == nil {
		t.Error("missing GBP price should be rejected")
	}
}
