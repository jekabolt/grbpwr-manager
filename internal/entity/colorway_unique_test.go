package entity

import (
	"errors"
	"fmt"
	"testing"
)

// T45 round 2 — which per-style uniqueness refused a colourway write. Both refusals ARE the
// «exists» every caller already answers, each keeps its own identity, and only the exact index
// names classify. MUTATION: match by «contains color» — the family index is read as the token.
func TestColorwayDuplicateKeyNamesTheIndexExactly(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"MySQL 8, token", errors.New("Error 1062 (23000): Duplicate entry '12-BKW' for key 'product.uniq_product_style_sku_color_token'"), ErrColorwaySkuTokenTaken},
		{"MySQL 8, family", errors.New("Error 1062 (23000): Duplicate entry '12-BLK' for key 'product.uniq_product_style_color'"), ErrColorwayFamilyTaken},
		{"MySQL 5.7, family", errors.New("Error 1062: Duplicate entry '12-BLK' for key 'uniq_product_style_color'"), ErrColorwayFamilyTaken},
		{"wrapped by the store", fmt.Errorf("can't insert colourway: %w",
			errors.New("Error 1062 (23000): Duplicate entry '7-C01' for key 'product.uniq_product_style_sku_color_token'")), ErrColorwaySkuTokenTaken},
		{"another index of product", errors.New("Error 1062 (23000): Duplicate entry '' for key 'product.sku'"), nil},
		{"a longer name with ours as a prefix", errors.New("Error 1062 (23000): Duplicate entry 'x' for key 'product.uniq_product_style_color_v2'"), nil},
		{"not a duplicate at all", errors.New("Error 1091 (42000): Can't DROP 'uniq_product_style_color'; check that column/key exists"), nil},
		{"nil", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ColorwayDuplicateKey(tc.err); got != tc.want {
				t.Fatalf("ColorwayDuplicateKey = %v, want %v", got, tc.want)
			}
		})
	}
}

// Both refusals are ErrColorwayColorExists for errors.Is — through any wrapping — and neither is
// the other. MUTATION: drop the Is method — the «exists» answers stop recognising them.
func TestColorwayUniquenessRefusalsAreExists(t *testing.T) {
	for _, sentinel := range []error{ErrColorwaySkuTokenTaken, ErrColorwayFamilyTaken} {
		wrapped := fmt.Errorf("relink colourway 41 to style 9: %w", fmt.Errorf("%w: detail", sentinel))
		if !errors.Is(wrapped, ErrColorwayColorExists) {
			t.Fatalf("%v is not an «exists»", sentinel)
		}
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("%v lost its identity", sentinel)
		}
	}
	if errors.Is(ErrColorwaySkuTokenTaken, ErrColorwayFamilyTaken) || errors.Is(ErrColorwayFamilyTaken, ErrColorwaySkuTokenTaken) {
		t.Fatal("the token and the family are two refusals")
	}
	if errors.Is(ErrColorwayColorExists, ErrColorwaySkuTokenTaken) {
		t.Fatal("the plain «exists» is not a token refusal")
	}
}

// Owner's decision 3: a palette colourway carries its own name. MUTATION: drop the TrimSpace — a
// blank name passes.
func TestCheckColorwayPaletteName(t *testing.T) {
	for _, tc := range []struct {
		name        string
		hasPalette  bool
		colourway   string
		introducing bool
		refuse      bool
	}{
		{"no palette, no name", false, "", false, false},
		{"a palette and a name", true, "Black and White", true, false},
		{"a palette, no name", true, "", true, true},
		{"a palette, a blank name", true, " \t", true, true},
		{"a standing palette, the name cleared", true, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ve := CheckColorwayPaletteName(tc.hasPalette, tc.colourway, tc.introducing)
			if !tc.refuse {
				if ve != nil {
					t.Fatalf("refused: %s", ve.Message)
				}
				return
			}
			if ve == nil {
				t.Fatal("passed")
			}
			if ve.Field != "development.name" || ve.Reason != "name_required_with_palette" {
				t.Fatalf("violation %s/%s", ve.Field, ve.Reason)
			}
		})
	}
	// The two wordings: the one for a write that brings the palette tells to send the name with it,
	// the other tells not to clear it.
	if a, b := CheckColorwayPaletteName(true, "", true), CheckColorwayPaletteName(true, "", false); a.Message == b.Message {
		t.Fatal("introducing a palette and clearing a name are told apart")
	}
}
