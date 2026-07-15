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

// TestProductIsPubliclyVisible pins that only 'active' is public-facing (PR5-A).
func TestProductIsPubliclyVisible(t *testing.T) {
	cases := map[ProductStatus]bool{
		ProductStatusActive:   true,
		ProductStatusHidden:   false,
		ProductStatusArchived: false,
		ProductStatus(""):     false,
	}
	for st, want := range cases {
		p := &Product{Status: st}
		if got := p.IsPubliclyVisible(); got != want {
			t.Errorf("status %q: IsPubliclyVisible = %v, want %v", st, got, want)
		}
	}
}
