package dto

import "testing"

func TestFormatSizeName(t *testing.T) {
	cases := map[string]string{
		// tailored (chest) codes — migration 0018
		"xs_44ta_m":  "XS · 44",
		"xl_52ta_m":  "XL · 52",
		"xxs_32ta_f": "XXS · 32",
		"xxl_54ta_m": "XXL · 54",
		// bottoms (waist) codes — migration 0019
		"xxs_23bo_f": "XXS · 23",
		"xs_28bo_m":  "XS · 28",
		// plain letter, shoe, and unrecognised values pass through unchanged
		"m":       "m",
		"os":      "os",
		"42":      "42",
		"35.5":    "35.5",
		"unknown": "unknown",
		"":        "",
	}
	for in, want := range cases {
		if got := FormatSizeName(in); got != want {
			t.Errorf("FormatSizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
