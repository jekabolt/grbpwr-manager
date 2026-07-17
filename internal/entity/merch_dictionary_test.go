package entity

import (
	"errors"
	"testing"
)

func TestValidateColorCode(t *testing.T) {
	valid := []string{"BLK", "WHT", "A1B", "000", "ZZ9"}
	for _, c := range valid {
		if err := ValidateColorCode(c); err != nil {
			t.Errorf("ValidateColorCode(%q) = %v, want nil", c, err)
		}
	}
	invalid := []string{"", "bl", "BLK1", "bl k", "blk", "B_K", "B-K", "ÄÖÜ", "AB"}
	for _, c := range invalid {
		if err := ValidateColorCode(c); err == nil {
			t.Errorf("ValidateColorCode(%q) = nil, want error", c)
		}
	}
}

func TestNormalizeColorCode(t *testing.T) {
	cases := map[string]string{
		"blk":   "BLK",
		" wht ": "WHT",
		"Ofw":   "OFW",
	}
	for in, want := range cases {
		if got := NormalizeColorCode(in); got != want {
			t.Errorf("NormalizeColorCode(%q) = %q, want %q", in, got, want)
		}
	}
	// Normalize + validate is the create path.
	if err := ValidateColorCode(NormalizeColorCode("  blk ")); err != nil {
		t.Errorf("normalize+validate lowercase padded code: %v", err)
	}
}

func TestNormalizeDictSlug(t *testing.T) {
	cases := map[string]string{
		"Spring/Summer 26": "SPRING_SUMMER_26",
		"  --Core--  ":     "CORE",
		"streetwear":       "STREETWEAR",
		"FW '25":           "FW_25",
		"a.b.c":            "A_B_C",
		"   ":              "",
		"!!!":              "",
	}
	for in, want := range cases {
		if got := NormalizeDictSlug(in); got != want {
			t.Errorf("NormalizeDictSlug(%q) = %q, want %q", in, got, want)
		}
	}
	// Cap at 64 and trim any resulting boundary underscore.
	long := ""
	for i := 0; i < 40; i++ {
		long += "ab "
	}
	if got := NormalizeDictSlug(long); len(got) > 64 {
		t.Errorf("NormalizeDictSlug long value length = %d, want <= 64", len(got))
	}
}

func TestCheckExpectedRevision(t *testing.T) {
	if err := CheckExpectedRevision(0, 42); err != nil {
		t.Errorf("expected=0 opts out, got %v", err)
	}
	if err := CheckExpectedRevision(42, 42); err != nil {
		t.Errorf("expected==current, got %v", err)
	}
	err := CheckExpectedRevision(41, 42)
	if err == nil {
		t.Fatalf("expected mismatch to error")
	}
	if !errors.Is(err, ErrDictionaryVersionConflict) {
		t.Errorf("mismatch error = %v, want ErrDictionaryVersionConflict", err)
	}
}
