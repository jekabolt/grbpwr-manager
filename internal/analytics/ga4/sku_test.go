package ga4

import "testing"

func TestBaseSKUFromItemID(t *testing.T) {
	tests := []struct {
		name   string
		itemID string
		want   string
		wantOK bool
	}{
		{"variant SKU collapses to base", "SS26-00021-BLK-25", "SS26-00021-BLK", true},
		{"variant SKU shoe collapses to base", "FW25-00099-WHT-76", "FW25-00099-WHT", true},
		{"bare base SKU passes through", "SS26-00021-BLK", "SS26-00021-BLK", true},
		{"empty", "", "", false},
		{"garbage", "(not set)", "", false},
		{"lowercase (URL-cased, not item_id-cased)", "ss26-00021-blk-25", "", false},
		{"legacy 10-digit numeric id (pre-SKU-redesign)", "1234567890", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := baseSKUFromItemID(tt.itemID)
			if ok != tt.wantOK {
				t.Fatalf("baseSKUFromItemID(%q) ok = %v, want %v", tt.itemID, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("baseSKUFromItemID(%q) = %q, want %q", tt.itemID, got, tt.want)
			}
		})
	}
}
