package techcard

import "testing"

// TestMaterialCodePrefix pins the deterministic per-type prefix used by the auto-generated warehouse
// code (#68): it is derived from the material's CTI class, case-insensitively, with 'other'/unknown
// falling back to the generic MAT.
func TestMaterialCodePrefix(t *testing.T) {
	cases := map[string]string{
		"fabric":    "FAB",
		"hardware":  "HRD",
		"thread":    "THR",
		"packaging": "PKG",
		"other":     "MAT",
		"":          "MAT", // empty normalises to 'other'
		"FABRIC":    "FAB", // case-insensitive
		"weird":     "MAT", // unknown class -> generic
	}
	for class, want := range cases {
		if got := materialCodePrefix(class); got != want {
			t.Errorf("materialCodePrefix(%q) = %q, want %q", class, got, want)
		}
	}
}

// TestNormalizeMaterialPurpose pins the purpose default (#40): empty/whitespace normalises to 'both'
// and any explicit value is lower-cased/trimmed.
func TestNormalizeMaterialPurpose(t *testing.T) {
	cases := map[string]string{
		"":             "both",
		"   ":          "both",
		"sample":       "sample",
		"SAMPLE":       "sample",
		" production ": "production",
		"both":         "both",
	}
	for in, want := range cases {
		if got := normalizeMaterialPurpose(in); got != want {
			t.Errorf("normalizeMaterialPurpose(%q) = %q, want %q", in, got, want)
		}
	}
}
