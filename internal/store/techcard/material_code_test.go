package techcard

import "testing"

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
