package product

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestBuildBaseSKU(t *testing.T) {
	tests := []struct {
		name string
		in   SKUSegments
		want string
	}{
		{
			name: "spec example — code colour",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 21, ColorCode: "BLK"},
			want: "SS26-00021-BLK",
		},
		{
			name: "styled colourways share model, differ by colour",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 8, ColorCode: "RED"},
			want: "SS26-00008-RED",
		},
		{
			name: "invalid missing dictionary code -> defensive UNK",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 5},
			want: "SS26-00005-UNK",
		},
		{
			name: "invalid/empty season -> SS fallback",
			in:   SKUSegments{Season: "", Year: 2026, ModelNo: 1, ColorCode: "WHT"},
			want: "SS26-00001-WHT",
		},
		{
			name: "off-white code kept to 3",
			in:   SKUSegments{Season: entity.SeasonRC, Year: 2027, ModelNo: 99999, ColorCode: "OFW"},
			want: "RC27-99999-OFW",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildBaseSKU(tt.in)
			if got != tt.want {
				t.Fatalf("BuildBaseSKU = %q, want %q", got, tt.want)
			}
			if len(got) != BaseSKULen {
				t.Errorf("base SKU %q length = %d, want %d", got, len(got), BaseSKULen)
			}
			// stability: same input -> same output
			if again := BuildBaseSKU(tt.in); again != got {
				t.Errorf("BuildBaseSKU not stable: %q != %q", again, got)
			}
		})
	}
}

func TestBuildVariantSKU(t *testing.T) {
	base := "SS26-00021-BLK"
	tests := []struct {
		name string
		ord  int
		want string
	}{
		{"apparel M (ord 25)", 25, "SS26-00021-BLK-25"},
		{"apparel OS (ord 5)", 5, "SS26-00021-BLK-05"},
		{"shoe 35 (ord 50)", 50, "SS26-00021-BLK-50"},
		{"shoe 48 (ord 76)", 76, "SS26-00021-BLK-76"},
		{"composite (ord 70)", 70, "SS26-00021-BLK-70"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildVariantSKU(base, tt.ord)
			if got != tt.want {
				t.Fatalf("BuildVariantSKU = %q, want %q", got, tt.want)
			}
			if len(got) != VariantSKULen {
				t.Errorf("variant SKU %q length = %d, want %d", got, len(got), VariantSKULen)
			}
		})
	}
}

func TestColorSegment(t *testing.T) {
	tests := []struct {
		code, want string
	}{
		{"BLK", "BLK"},
		{"", colorFallback},
		{"xy", colorFallback},
		{"black", colorFallback},
	}
	for _, tt := range tests {
		if got := colorSegment(tt.code); got != tt.want {
			t.Errorf("colorSegment(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestSeasonSegment(t *testing.T) {
	tests := []struct {
		season entity.SeasonEnum
		year   int
		want   string
	}{
		{entity.SeasonSS, 2026, "SS26"},
		{entity.SeasonFW, 2025, "FW25"},
		{entity.SeasonPF, 2030, "PF30"},
		{"", 2026, "SS26"},   // invalid -> SS
		{"XX", 2026, "SS26"}, // invalid -> SS
		{entity.SeasonSS, 2000, "SS00"},
	}
	for _, tt := range tests {
		if got := seasonSegment(tt.season, tt.year); got != tt.want {
			t.Errorf("seasonSegment(%q,%d) = %q, want %q", tt.season, tt.year, got, tt.want)
		}
	}
}
