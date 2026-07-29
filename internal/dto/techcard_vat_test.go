package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// TestCostingFxNetOfVat pins the netting formula to the one the realised-sales margin already uses
// (internal/store/metrics.netOfVat: net = gross × 100/(100+rate)). If these two ever diverge the two
// admin screens go back to disagreeing about the same style, which is the bug this closes.
func TestCostingFxNetOfVat(t *testing.T) {
	tests := []struct {
		name  string
		rate  decimal.NullDecimal
		gross string
		want  string
		ok    bool
	}{
		{
			name:  "PL 23% out of a VAT-inclusive price",
			rate:  decimal.NullDecimal{Decimal: decimal.RequireFromString("23"), Valid: true},
			gross: "123", want: "100", ok: true,
		},
		{
			name:  "GB 20%",
			rate:  decimal.NullDecimal{Decimal: decimal.RequireFromString("20"), Valid: true},
			gross: "240", want: "200", ok: true,
		},
		{
			// An export destination has no VAT to remove. Returning the gross figure under the name
			// "net" would be the same overstatement in a new place, so the caller must get "no".
			name: "no rate on file nets nothing",
			rate: decimal.NullDecimal{}, gross: "123", ok: false,
		},
		{
			name: "a zero rate is still nothing to net",
			rate: decimal.NullDecimal{Decimal: decimal.Zero, Valid: true}, gross: "123", ok: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := CostingFx{VatRatePct: tt.rate}
			got, ok := fx.netOfVat(decimal.RequireFromString(tt.gross))
			if ok != tt.ok {
				t.Fatalf("netOfVat ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if !got.Round(2).Equal(decimal.RequireFromString(tt.want)) {
				t.Fatalf("netOfVat(%s) = %s, want %s", tt.gross, got.Round(2), tt.want)
			}
		})
	}
}

// TestNetColorwayPricesToPb covers the emitted field: every currency is netted at the one resolved
// rate (the operator is asking "if I sell at these prices into this market"), and a read with no rate
// emits nothing at all rather than a copy of the gross list.
func TestNetColorwayPricesToPb(t *testing.T) {
	prices := []entity.ColorwayPrice{
		{Currency: "EUR", Price: decimal.RequireFromString("123.00")},
		{Currency: "PLN", Price: decimal.RequireFromString("492.00")},
	}
	fx := CostingFx{VatRatePct: decimal.NullDecimal{Decimal: decimal.RequireFromString("23"), Valid: true}}

	got := netColorwayPricesToPb(prices, fx)
	if len(got) != 2 {
		t.Fatalf("got %d net prices, want 2", len(got))
	}
	if got[0].GetCurrency() != "EUR" || got[0].GetPrice().GetValue() != "100.00" {
		t.Fatalf("EUR net = %s %s, want EUR 100.00", got[0].GetCurrency(), got[0].GetPrice().GetValue())
	}
	if got[1].GetCurrency() != "PLN" || got[1].GetPrice().GetValue() != "400.00" {
		t.Fatalf("PLN net = %s %s, want PLN 400.00", got[1].GetCurrency(), got[1].GetPrice().GetValue())
	}

	if out := netColorwayPricesToPb(prices, CostingFx{}); out != nil {
		t.Fatalf("with no rate on file net_prices must be empty, got %d entries", len(out))
	}
	if out := netColorwayPricesToPb(nil, fx); out != nil {
		t.Fatalf("a colourway with no prices has no net prices, got %d entries", len(out))
	}
}
