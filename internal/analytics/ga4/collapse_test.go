package ga4

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// TestProdAggregatorCollapsesBaseAndVariantItemID is the regression test for problem 020: before the
// fix, storefront view/list/cart sent the base SKU as item_id while checkout/purchase (browser +
// backend GA4MP) sent the variant SKU, so GA4's own itemId dimension split one product's funnel across
// two "products". The aggregator must fold both shapes into a single (date, base SKU) row regardless
// of which raw item_id GA4 reports the metric under.
func TestProdAggregatorCollapsesBaseAndVariantItemID(t *testing.T) {
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	agg := newProdAggregator()

	// view_item_list / view_item / add_to_cart historically carried the base SKU.
	if ok := agg.add(day, "SS26-00021-BLK", "Bomber Jacket", 10, 3, 0, decimal.Zero); !ok {
		t.Fatal("add(base SKU row) = false, want true")
	}
	// checkout/purchase (and backend GA4MP) carry the variant SKU — two different sizes sold.
	if ok := agg.add(day, "SS26-00021-BLK-25", "Bomber Jacket", 0, 0, 2, decimal.NewFromInt(200)); !ok {
		t.Fatal("add(variant SKU row, size 25) = false, want true")
	}
	if ok := agg.add(day, "SS26-00021-BLK-30", "Bomber Jacket", 0, 0, 1, decimal.NewFromInt(100)); !ok {
		t.Fatal("add(variant SKU row, size 30) = false, want true")
	}

	got := agg.result()
	if len(got) != 1 {
		t.Fatalf("result() returned %d product rows, want 1 (base+variant item_id must collapse to one product family): %+v", len(got), got)
	}

	row := got[0]
	if row.ProductID != "SS26-00021-BLK" {
		t.Errorf("ProductID = %q, want the base SKU", row.ProductID)
	}
	if row.ItemsViewed != 10 {
		t.Errorf("ItemsViewed = %d, want 10", row.ItemsViewed)
	}
	if row.AddToCarts != 3 {
		t.Errorf("AddToCarts = %d, want 3", row.AddToCarts)
	}
	if row.Purchases != 3 {
		t.Errorf("Purchases = %d, want 3 (2 + 1 summed across both sold variants)", row.Purchases)
	}
	if !row.Revenue.Equal(decimal.NewFromInt(300)) {
		t.Errorf("Revenue = %s, want 300 (200 + 100 summed across both sold variants)", row.Revenue.String())
	}
}

// TestProdAggregatorSeparatesByDateAndProduct ensures the collapse is scoped to (date, base SKU) —
// different days and different products must never merge into each other.
func TestProdAggregatorSeparatesByDateAndProduct(t *testing.T) {
	day1 := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	agg := newProdAggregator()

	agg.add(day1, "SS26-00021-BLK-25", "Bomber Jacket", 1, 0, 0, decimal.Zero)
	agg.add(day2, "SS26-00021-BLK-25", "Bomber Jacket", 1, 0, 0, decimal.Zero) // same product, next day
	agg.add(day1, "SS26-00008-RED-25", "Cargo Pants", 1, 0, 0, decimal.Zero)   // same day, different product

	got := agg.result()
	if len(got) != 3 {
		t.Fatalf("result() returned %d rows, want 3 (date and product are both part of the key): %+v", len(got), got)
	}
}

// TestProdAggregatorSkipsUnrecognizedItemID asserts a garbage/blank item_id is dropped rather than
// silently grouped under an empty-string product key.
func TestProdAggregatorSkipsUnrecognizedItemID(t *testing.T) {
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	agg := newProdAggregator()

	if ok := agg.add(day, "(not set)", "", 5, 0, 0, decimal.Zero); ok {
		t.Error("add(garbage item_id) = true, want false")
	}
	if got := agg.result(); len(got) != 0 {
		t.Errorf("result() = %+v, want empty (garbage row must not appear)", got)
	}
}

// TestProdAggregatorKeepsFirstNonEmptyName pins that a later empty/duplicate itemName never blanks
// out a name already recorded for the collapsed family.
func TestProdAggregatorKeepsFirstNonEmptyName(t *testing.T) {
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	agg := newProdAggregator()

	agg.add(day, "SS26-00021-BLK-25", "Bomber Jacket", 1, 0, 0, decimal.Zero)
	agg.add(day, "SS26-00021-BLK-30", "", 1, 0, 0, decimal.Zero)

	got := agg.result()
	if len(got) != 1 || got[0].ProductName != "Bomber Jacket" {
		t.Errorf("result() = %+v, want ProductName = %q retained", got, "Bomber Jacket")
	}
}
