package aiprov

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExtractJSONObject pins the cut translate has always made (moved verbatim in E1), so the
// Anthropic transport and the translate service read one model answer the same way.
//
// MUTATIONS (each measured red → restored green): `end := strings.IndexByte(s, '}')` → the nested
// row; the fence strip removed → the "fence, then prose with braces" row; a balanced scan in place of
// first-'{'-to-last-'}' → the "trailing braces" row (the cut is kept verbatim on purpose, see the doc).
func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"bare object", `{"a":1}`, `{"a":1}`, true},
		{"prose around a nested object", `Here you go: {"a":{"b":2}} hope it helps`, `{"a":{"b":2}}`, true},
		{"fence, then prose with braces", "```json\n{\"a\":1}\n```\nNote: {x} is a placeholder", `{"a":1}`, true},
		{"trailing braces are inside the cut (verbatim, not balanced)", `{"a":1} then {b}`, `{"a":1} then {b}`, true},
		{"no object", "no json here", "", false},
		{"braces reversed", "} {", "", false},
		{"empty", "   ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ExtractJSONObject(tc.in)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}
