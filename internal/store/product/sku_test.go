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
			name: "colour from free-text name (no code) -> translit first 3",
			in:   SKUSegments{Season: entity.SeasonFW, Year: 2025, ModelNo: 3, ColorName: "navy"},
			want: "FW25-00003-NAV",
		},
		{
			name: "colour from Cyrillic name",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 5, ColorName: "чёрный"},
			want: "SS26-00005-CHE",
		},
		{
			name: "no colour info -> UNK fallback",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 5},
			want: "SS26-00005-UNK",
		},
		{
			name: "name too short for 3 letters -> UNK",
			in:   SKUSegments{Season: entity.SeasonSS, Year: 2026, ModelNo: 5, ColorName: "ab"},
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

func TestTranslit(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"navy", "navy"},
		{"off-white", "offwhite"}, // non-alnum dropped
		{"чёрный", "chernyy"},
		{"Красный", "Krasnyy"},
		{"blue 2", "blue2"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Translit(tt.in); got != tt.want {
			t.Errorf("Translit(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestColorSegment(t *testing.T) {
	tests := []struct {
		code, name, want string
	}{
		{"BLK", "black", "BLK"},         // code wins
		{"", "navy", "NAV"},             // translit name
		{"", "off-white", "OFF"},        // non-alpha dropped, first 3
		{"", "чёрный", "CHE"},           // cyrillic
		{"", "", colorFallback},         // nothing -> UNK
		{"", "ab", colorFallback},       // <3 letters -> UNK
		{"xy", "green", "GRE"},          // short code ignored, name used
	}
	for _, tt := range tests {
		if got := colorSegment(tt.code, tt.name); got != tt.want {
			t.Errorf("colorSegment(%q,%q) = %q, want %q", tt.code, tt.name, got, tt.want)
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
