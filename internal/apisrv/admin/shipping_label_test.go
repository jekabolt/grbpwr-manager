package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func wgg(grams int32) sql.NullInt32 {
	return sql.NullInt32{Int32: grams, Valid: true}
}

func qty(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func TestDeriveParcel(t *testing.T) {
	tests := []struct {
		name         string
		items        []entity.OrderItemParcel
		wantGrams    int
		wantComplete bool
		wantMissing  []int32
		wantL        int
		wantW        int
		wantH        int
	}{
		{
			name: "all weights present, largest box wins",
			items: []entity.OrderItemParcel{
				{ProductId: 1, Quantity: qty(2), WeightGrossGrams: wgg(750), BoxDimensions: sql.NullString{String: "30×22×10 см", Valid: true}},
				{ProductId: 2, Quantity: qty(1), WeightGrossGrams: wgg(1250), BoxDimensions: sql.NullString{String: "40 x 30 x 20", Valid: true}},
			},
			wantGrams: 1500 + 1250, wantComplete: true, wantMissing: nil,
			wantL: 40, wantW: 30, wantH: 20,
		},
		{
			name: "missing weight flags product and marks incomplete",
			items: []entity.OrderItemParcel{
				{ProductId: 1, Quantity: qty(1), WeightGrossGrams: wgg(500), BoxDimensions: sql.NullString{String: "20x15x5", Valid: true}},
				{ProductId: 7, Quantity: qty(3), WeightGrossGrams: sql.NullInt32{}, BoxDimensions: sql.NullString{}},
			},
			wantGrams: 500, wantComplete: false, wantMissing: []int32{7},
			wantL: 20, wantW: 15, wantH: 5,
		},
		{
			name: "no dimensions anywhere -> zero box, still complete on weight",
			items: []entity.OrderItemParcel{
				{ProductId: 1, Quantity: qty(1), WeightGrossGrams: wgg(300), BoxDimensions: sql.NullString{}},
			},
			wantGrams: 300, wantComplete: true, wantMissing: nil,
			wantL: 0, wantW: 0, wantH: 0,
		},
		{
			name: "zero quantity treated as one",
			items: []entity.OrderItemParcel{
				{ProductId: 1, Quantity: qty(0), WeightGrossGrams: wgg(400), BoxDimensions: sql.NullString{}},
			},
			wantGrams: 400, wantComplete: true, wantMissing: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parcel, complete, missing := deriveParcel(tt.items)
			if parcel.WeightGrams != tt.wantGrams {
				t.Errorf("weight grams = %d, want %d", parcel.WeightGrams, tt.wantGrams)
			}
			if complete != tt.wantComplete {
				t.Errorf("complete = %v, want %v", complete, tt.wantComplete)
			}
			if len(missing) != len(tt.wantMissing) {
				t.Errorf("missing = %v, want %v", missing, tt.wantMissing)
			}
			for i := range tt.wantMissing {
				if i < len(missing) && missing[i] != tt.wantMissing[i] {
					t.Errorf("missing[%d] = %d, want %d", i, missing[i], tt.wantMissing[i])
				}
			}
			if parcel.LengthCM != tt.wantL || parcel.WidthCM != tt.wantW || parcel.HeightCM != tt.wantH {
				t.Errorf("box = %dx%dx%d, want %dx%dx%d", parcel.LengthCM, parcel.WidthCM, parcel.HeightCM, tt.wantL, tt.wantW, tt.wantH)
			}
			if parcel.BoxType != "custom" {
				t.Errorf("box type = %q, want custom", parcel.BoxType)
			}
		})
	}
}

func TestParseBoxDimensions(t *testing.T) {
	tests := []struct {
		in      string
		l, w, h int
		ok      bool
	}{
		{"30×22×10 см", 30, 22, 10, true},
		{"40 x 30 x 20", 40, 30, 20, true},
		{"20x15x5", 20, 15, 5, true},
		{"30 х 22 х 10", 30, 22, 10, true}, // cyrillic kha
		{"30.5 × 22.2 × 10.9", 30, 22, 10, true},
		{"25*18*8cm", 25, 18, 8, true},
		{"just two 30x20", 0, 0, 0, false},
		{"", 0, 0, 0, false},
		{"no numbers here", 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			l, w, h, ok := parseBoxDimensions(tt.in)
			if ok != tt.ok || l != tt.l || w != tt.w || h != tt.h {
				t.Errorf("parseBoxDimensions(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)", tt.in, l, w, h, ok, tt.l, tt.w, tt.h, tt.ok)
			}
		})
	}
}

func TestFormatParcelDimensions(t *testing.T) {
	if got := formatParcelDimensions(entity.LabelParcel{LengthCM: 30, WidthCM: 22, HeightCM: 10}); got != "30x22x10 cm" {
		t.Errorf("got %q, want 30x22x10 cm", got)
	}
	if got := formatParcelDimensions(entity.LabelParcel{WeightGrams: 500}); got != "" {
		t.Errorf("got %q, want empty (no dims)", got)
	}
}

func TestNeedsCustoms(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DE", "DE", false}, // domestic
		{"DE", "FR", false}, // intra-EU
		{"DE", "US", true},  // EU -> non-EU
		{"DE", "GB", true},  // EU -> UK (post-Brexit, not in euISO2)
		{"US", "DE", true},  // non-EU -> EU
		{"", "US", false},   // unknown origin
		{"DE", "", false},   // unknown destination
	}
	for _, tt := range tests {
		if got := needsCustoms(tt.from, tt.to); got != tt.want {
			t.Errorf("needsCustoms(%q,%q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestBuildCustoms(t *testing.T) {
	hs := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	t.Run("builds items and declared value", func(t *testing.T) {
		items := []entity.OrderItemParcel{
			{
				ProductId: 1, Quantity: qty(2), SKU: "SKU-1",
				ProductPriceWithSale: decimal.RequireFromString("49.90"),
				WeightGrossGrams:     wgg(500),
				HSCode:               hs("6109100010"),
				CountryCode:          hs("PT"),
				CustomsDescription:   hs("Cotton t-shirt"),
			},
		}
		c, err := buildCustoms(items, "EUR")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.Purpose != "merchandise" {
			t.Errorf("customs header = %+v", c)
		}
		if len(c.Items) != 1 {
			t.Fatalf("expected 1 item, got %d", len(c.Items))
		}
		it := c.Items[0]
		if it.Description != "Cotton t-shirt" || it.Quantity != 2 || it.HSCode != "6109100010" ||
			it.OriginISO2 != "PT" || it.PriceCurrency != "EUR" || it.WeightGrams != 500 {
			t.Errorf("item = %+v", it)
		}
		if !it.PriceAmount.Equal(decimal.RequireFromString("49.90")) {
			t.Errorf("price = %s, want 49.90", it.PriceAmount)
		}
	})

	t.Run("description falls back to SKU", func(t *testing.T) {
		items := []entity.OrderItemParcel{
			{ProductId: 1, Quantity: qty(1), SKU: "SKU-9", ProductPriceWithSale: decimal.NewFromInt(10),
				HSCode: hs("6109100010"), CountryCode: hs("PT")},
		}
		c, err := buildCustoms(items, "EUR")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.Items[0].Description != "SKU-9" {
			t.Errorf("description = %q, want SKU-9", c.Items[0].Description)
		}
	})

	t.Run("missing hs code or origin errors", func(t *testing.T) {
		items := []entity.OrderItemParcel{
			{ProductId: 42, Quantity: qty(1), SKU: "X", HSCode: sql.NullString{}, CountryOfOrigin: sql.NullString{}},
		}
		if _, err := buildCustoms(items, "EUR"); err == nil {
			t.Fatal("expected error for missing customs data")
		}
	})
}
