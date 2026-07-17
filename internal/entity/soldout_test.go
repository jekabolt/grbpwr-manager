package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestSoldOutFromSizes(t *testing.T) {
	sz := func(q int64) Variant { return Variant{Quantity: decimal.NewFromInt(q)} }

	cases := []struct {
		name  string
		sizes []Variant
		want  bool
	}{
		{"no sizes", nil, true},
		{"empty slice", []Variant{}, true},
		{"all zero", []Variant{sz(0), sz(0)}, true},
		{"one positive", []Variant{sz(0), sz(3)}, false},
		{"all positive", []Variant{sz(1), sz(2)}, false},
		{"net zero from negative (degenerate)", []Variant{sz(5), sz(-5)}, true},
		// 50-B: anomalous data (e.g. an oversell race/bug driving a size's stock negative) must
		// still read as sold out, not merely "not sold out" -- <=0 covers 0 AND negative, matching
		// the SQL soldOutSelect projection (internal/store/product/query.go) which uses the same
		// <=0 comparison rather than a strict =0.
		{"single negative size (anomalous data)", []Variant{sz(-3)}, true},
		{"net negative across sizes (anomalous data)", []Variant{sz(2), sz(-5)}, true},
	}
	for _, c := range cases {
		if got := SoldOutFromSizes(c.sizes); got != c.want {
			t.Errorf("%s: SoldOutFromSizes = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestProductIsPubliclyVisible pins that only ACTIVE is public-facing (R6).
func TestProductIsPubliclyVisible(t *testing.T) {
	cases := map[ColorwayStatus]bool{
		ColorwayStatusDraft:    false,
		ColorwayStatusActive:   true,
		ColorwayStatusHidden:   false,
		ColorwayStatusArchived: false,
		ColorwayStatusUnknown:  false,
	}
	for st, want := range cases {
		p := &Colorway{LifecycleStatus: st}
		if got := p.IsPubliclyVisible(); got != want {
			t.Errorf("status %s: IsPubliclyVisible = %v, want %v", st, got, want)
		}
	}
}
