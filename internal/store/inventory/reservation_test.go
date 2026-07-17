package inventory

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(i int64) decimal.Decimal { return decimal.NewFromInt(i) }

// fakeResolve maps a product id to a fixed resolved recipe, for the pure aggregation test.
func fakeResolve(m map[int]resolvedRecipe) func(orderPackagingLine) (resolvedRecipe, error) {
	return func(ln orderPackagingLine) (resolvedRecipe, error) { return m[ln.ProductId], nil }
}

func TestAggregatePackaging_AllGlobalMatchesFlatBehaviour(t *testing.T) {
	// Two colourways, both resolving to the same global recipe: one box (mat 10) + one dust bag per
	// unit (mat 20). Box counted once for the whole order; dust bag scales with total units (2+3=5).
	global := resolvedRecipe{Key: "global", Rows: []recipeRow{
		{MaterialId: 10, QtyPerOrder: d(1), QtyPerItem: d(0)},
		{MaterialId: 20, QtyPerOrder: d(0), QtyPerItem: d(1)},
	}}
	lines := []orderPackagingLine{
		{ProductId: 1, Qty: d(2)},
		{ProductId: 2, Qty: d(3)},
	}
	got, err := aggregatePackaging(lines, fakeResolve(map[int]resolvedRecipe{1: global, 2: global}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]decimal.Decimal{10: d(1), 20: d(5)}
	assertReq(t, got, want)
}

func TestAggregatePackaging_MixedScopesBoxOncePerDistinctRecipe(t *testing.T) {
	// Product 1 has its own product-scope recipe (box mat 10 + dust bag mat 20); product 2 falls back
	// to global (box mat 30 + dust bag mat 40 ×2/unit). Each distinct recipe contributes its box once.
	p1 := resolvedRecipe{Key: "product:1", Rows: []recipeRow{
		{MaterialId: 10, QtyPerOrder: d(1), QtyPerItem: d(0)},
		{MaterialId: 20, QtyPerOrder: d(0), QtyPerItem: d(1)},
	}}
	glob := resolvedRecipe{Key: "global", Rows: []recipeRow{
		{MaterialId: 30, QtyPerOrder: d(1), QtyPerItem: d(0)},
		{MaterialId: 40, QtyPerOrder: d(0), QtyPerItem: d(2)},
	}}
	lines := []orderPackagingLine{
		{ProductId: 1, Qty: d(2)},
		{ProductId: 2, Qty: d(3)},
	}
	got, err := aggregatePackaging(lines, fakeResolve(map[int]resolvedRecipe{1: p1, 2: glob}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]decimal.Decimal{10: d(1), 20: d(2), 30: d(1), 40: d(6)}
	assertReq(t, got, want)
}

func TestAggregatePackaging_ZeroAndInactiveDropped(t *testing.T) {
	// A recipe whose only line is zero-qty contributes nothing (dropped from the requirement).
	empty := resolvedRecipe{Key: "global", Rows: []recipeRow{
		{MaterialId: 50, QtyPerOrder: d(0), QtyPerItem: d(0)},
	}}
	got, err := aggregatePackaging([]orderPackagingLine{{ProductId: 1, Qty: d(4)}},
		fakeResolve(map[int]resolvedRecipe{1: empty}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty requirement, got %v", got)
	}
}

func assertReq(t *testing.T, got, want map[int]decimal.Decimal) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("materials: got %v want %v", got, want)
	}
	for m, w := range want {
		g, ok := got[m]
		if !ok {
			t.Fatalf("material %d missing in %v", m, got)
		}
		if !g.Equal(w) {
			t.Errorf("material %d: got %s want %s", m, g, w)
		}
	}
}
