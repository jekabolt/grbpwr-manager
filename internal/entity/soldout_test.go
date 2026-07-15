package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestSoldOutFromSizes(t *testing.T) {
	sz := func(q int64) ProductSize { return ProductSize{Quantity: decimal.NewFromInt(q)} }

	cases := []struct {
		name  string
		sizes []ProductSize
		want  bool
	}{
		{"no sizes", nil, true},
		{"empty slice", []ProductSize{}, true},
		{"all zero", []ProductSize{sz(0), sz(0)}, true},
		{"one positive", []ProductSize{sz(0), sz(3)}, false},
		{"all positive", []ProductSize{sz(1), sz(2)}, false},
		{"net zero from negative (degenerate)", []ProductSize{sz(5), sz(-5)}, true},
	}
	for _, c := range cases {
		if got := SoldOutFromSizes(c.sizes); got != c.want {
			t.Errorf("%s: SoldOutFromSizes = %v, want %v", c.name, got, c.want)
		}
	}
}
