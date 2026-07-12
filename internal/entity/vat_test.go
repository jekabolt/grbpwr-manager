package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestVatFromInclusive(t *testing.T) {
	d := decimal.RequireFromString
	cases := []struct {
		name       string
		gross, pct string
		want       string
	}{
		{"21pct", "121", "21", "21"},
		{"20pct", "120", "20", "20"},
		{"19pct", "119", "19", "19"},
		{"zero rate", "100", "0", "0"},
		{"negative rate guarded to zero", "100", "-5", "0"},
		{"zero gross", "0", "21", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := VatFromInclusive(d(c.gross), d(c.pct))
			if !got.Equal(d(c.want)) {
				t.Errorf("VatFromInclusive(%s, %s) = %s, want %s", c.gross, c.pct, got, c.want)
			}
		})
	}
}
