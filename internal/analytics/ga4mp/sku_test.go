package ga4mp

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func TestSplitVariantSKU(t *testing.T) {
	tests := []struct {
		name     string
		sku      string
		wantBase string
		wantSize string
		wantOK   bool
	}{
		{"canonical apparel M", "SS26-00021-BLK-25", "SS26-00021-BLK", "25", true},
		{"canonical apparel OS", "SS26-00021-BLK-05", "SS26-00021-BLK", "05", true},
		{"canonical shoe EU48", "FW25-00099-WHT-76", "FW25-00099-WHT", "76", true},
		{"base only, no size tail (not a variant)", "SS26-00021-BLK", "", "", false},
		{"empty", "", "", "", false},
		{"lowercase season", "ss26-00021-BLK-25", "", "", false},
		{"legacy placeholder snapshot", "LEGACY-UNKNOWN-42", "", "", false},
		{"too short", "SS26-00021-BLK-2", "", "", false},
		{"too long", "SS26-00021-BLK-025", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, size, ok := splitVariantSKU(tt.sku)
			if ok != tt.wantOK {
				t.Fatalf("splitVariantSKU(%q) ok = %v, want %v", tt.sku, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if base != tt.wantBase {
				t.Errorf("splitVariantSKU(%q) base = %q, want %q", tt.sku, base, tt.wantBase)
			}
			if size != tt.wantSize {
				t.Errorf("splitVariantSKU(%q) size = %q, want %q", tt.sku, size, tt.wantSize)
			}
			// base + "-" + size must reconstruct the original input exactly.
			if base+"-"+size != tt.sku {
				t.Errorf("splitVariantSKU(%q) = (%q,%q), does not reconstruct the input", tt.sku, base, size)
			}
		})
	}
}

// TestBuildPurchaseItemsEventContract pins the R3/problem-020 GA4 item identity contract for the
// backend (server-side) purchase event: item_id is the variant SKU snapshot, item_group_id is the
// base SKU and item_variant is the public size ordinal, both derived ONLY from that snapshot string
// (no live product/size lookup — GA4MP reads only order_item snapshots).
func TestBuildPurchaseItemsEventContract(t *testing.T) {
	orderItems := []entity.OrderItem{
		{
			ProductBrand: "GRBPWR",
			Translations: []entity.ColorwayTranslationInsert{{LanguageId: 1, Name: "Bomber Jacket"}},
			OrderItemInsert: entity.OrderItemInsert{
				ProductPrice:         decimal.NewFromInt(100),
				ProductPriceWithSale: decimal.NewFromInt(100),
				Quantity:             decimal.NewFromInt(2),
			},
			SKU: "SS26-00021-BLK-25",
		},
		{
			ProductBrand: "GRBPWR",
			OrderItemInsert: entity.OrderItemInsert{
				ProductPrice:         decimal.NewFromInt(50),
				ProductPriceWithSale: decimal.NewFromInt(45),
				Quantity:             decimal.NewFromInt(1),
			},
			SKU: "LEGACY-UNKNOWN-7", // pre-contract snapshot: must degrade gracefully
		},
	}

	items := buildPurchaseItems(orderItems)
	if len(items) != 2 {
		t.Fatalf("buildPurchaseItems returned %d items, want 2", len(items))
	}

	got := items[0]
	if got.ItemID != "SS26-00021-BLK-25" {
		t.Errorf("item[0].ItemID = %q, want the variant SKU snapshot", got.ItemID)
	}
	if got.ItemGroupID != "SS26-00021-BLK" {
		t.Errorf("item[0].ItemGroupID = %q, want the base SKU (variant[:14])", got.ItemGroupID)
	}
	if got.ItemVariant != "25" {
		t.Errorf("item[0].ItemVariant = %q, want the size ordinal segment", got.ItemVariant)
	}
	if got.ItemName != "GRBPWR Bomber Jacket" {
		t.Errorf("item[0].ItemName = %q, want brand + translated name (not the SKU)", got.ItemName)
	}
	if got.Quantity != 2 {
		t.Errorf("item[0].Quantity = %d, want 2", got.Quantity)
	}

	legacy := items[1]
	if legacy.ItemID != "LEGACY-UNKNOWN-7" {
		t.Errorf("item[1].ItemID = %q, want the raw snapshot even when non-canonical", legacy.ItemID)
	}
	if legacy.ItemGroupID != "" || legacy.ItemVariant != "" {
		t.Errorf("item[1] non-canonical snapshot must omit item_group_id/item_variant rather than emit a wrong key, got group=%q variant=%q",
			legacy.ItemGroupID, legacy.ItemVariant)
	}
}
