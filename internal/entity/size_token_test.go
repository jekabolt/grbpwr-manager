package entity

import (
	"reflect"
	"testing"
)

// TestSizeTokensOfMatchesTheClientFixtures is a PORT TEST, not a behaviour test: every case here is
// taken from the comment on sizeTokensOf() in the admin's block-code.ts, which is the original. The
// server's copy exists only because the gate has to resolve a stored token back to a size, and the
// one thing that must never happen is the two answering differently — a card would then read as
// «size L is not in the files» on the server while the panel shows L present.
//
// The heuristic half (deriveBlockSizes) is deliberately NOT ported, and this test is the reason the
// split is safe: what crossed the boundary has no thresholds, no cross-item state and nothing to
// tune, so there is no mechanism by which it could drift.
func TestSizeTokensOfMatchesTheClientFixtures(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		// The dictionary shape the comment states: code, numeric equivalent, system.
		{"code and number", "xs_44ta_m", []string{"xs", "44"}},
		{"a size written by number only still yields its code", "l_50ta_m", []string{"l", "50"}},
		// A grader writes EITHER the code or the number, so both are accepted and either is enough.
		{"no numeric segment yields the code alone", "onesize", []string{"onesize"}},
		{"second segment without digits yields the code alone", "m_ta_m", []string{"m"}},
		{"case and padding are normalised", "  XS_44TA_M  ", []string{"xs", "44"}},
		// An empty name can never be matched — visible, rather than a token of "" that would match
		// every malformed block name in the file.
		{"empty name yields nothing", "", nil},
		{"whitespace-only name yields nothing", "   ", nil},
		{"a leading underscore does not produce an empty token", "_44ta_m", []string{"44"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SizeTokensOf(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SizeTokensOf(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeSizeToken ports bareToken(): the comparison sees letters and digits only, because a
// real file writes the base size in decoration («BP_<S>») and «<S>» and «S» are one size.
func TestNormalizeSizeToken(t *testing.T) {
	cases := map[string]string{
		"<S>":  "s",
		"S":    "s",
		" xl ": "xl",
		"44":   "44",
		"—":    "",
		"":     "",
		"XS-2": "xs2",
	}
	for in, want := range cases {
		if got := NormalizeSizeToken(in); got != want {
			t.Errorf("NormalizeSizeToken(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSizeCoveredByTokens: ANY of a size's tokens is enough. This mirrors the panel's own missingIn,
// which lists a size as missing only when NONE of its tokens was found — demanding both would report
// every size of every card that spells its sizes one way as absent.
func TestSizeCoveredByTokens(t *testing.T) {
	byCode := map[string]bool{"xs": true}
	byNumber := map[string]bool{"44": true}
	neither := map[string]bool{"m": true}
	if !SizeCoveredByTokens("xs_44ta_m", byCode) {
		t.Error("a file spelling the CODE covers the size")
	}
	if !SizeCoveredByTokens("xs_44ta_m", byNumber) {
		t.Error("a file spelling the NUMBER covers the size")
	}
	if SizeCoveredByTokens("xs_44ta_m", neither) {
		t.Error("a file spelling neither does not cover the size")
	}
	// A size with no tokens can never be covered — and must not be covered vacuously.
	if SizeCoveredByTokens("", map[string]bool{"": true}) {
		t.Error("a nameless size must not be matched by an empty token")
	}
}
