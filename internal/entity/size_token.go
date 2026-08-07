package entity

import (
	"strings"
	"unicode"
)

// РАЗМЕРНЫЕ ТОКЕНЫ ИМЕНИ СЛОВАРНОГО РАЗМЕРА (Ф6.3) — a PORT, and a deliberately tiny one.
//
// The heuristic that decides which token of a DXF block name IS a size (deriveBlockSizes in the
// admin's block-code.ts) is NOT ported and never will be. It is non-compositional — its verdict
// depends on the whole set of names at once, through a «at least two size tails on one name stem»
// rule and a frequency floor of max(2, max/2) — so a second implementation would not merely be
// duplicated code, it would give DIFFERENT ANSWERS than the button the operator pressed, and it
// would diverge silently.
//
// What IS ported is the other half: turning a DICTIONARY SIZE NAME into the tokens a file might
// spell it with. That one is NORMALISATION, not heuristics: it has no thresholds, no cross-item
// state, and nothing to tune. It is a split and a lowercase, which is exactly why it was chosen as
// the half that may cross the wire boundary — there is no mechanism by which it could drift.
//
// The counterpart in the client is sizeTokensOf() in
// src/components/managers/tech-card/components/nesting/block-code.ts, and the fixture test beside
// this file holds the same examples that file's comment states.

// NormalizeSizeToken reduces a token to what the comparison sees: letters and digits only,
// lower-cased. Port of the client's bareToken().
//
// It exists because a real грaдалка writes the base size in decoration — «BP_<S>» with angle
// brackets — and «<S>» and «S» must be ONE size. Stripping punctuation is the whole of it; the
// token as WRITTEN is never shown from here, only compared.
func NormalizeSizeToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// SizeTokensOf turns a size dictionary NAME into every token a DXF file might spell that size with.
//
// Dictionary names are written «xs_44ta_m»: the code, the numeric equivalent, the system. A
// градалка writes EITHER the code («XS») OR the number («44»), so both are accepted — a size is
// covered when the files carry any one of its tokens, never all of them.
//
// Returns tokens already normalised through NormalizeSizeToken, so a caller comparing against a
// stored index never has to remember to normalise (and cannot forget to). An empty or nameless size
// yields no tokens at all, which reads as «this size can never be matched» — correct, and visible,
// rather than a token of "" that would match every malformed block name.
func SizeTokensOf(dictionaryName string) []string {
	name := strings.ToLower(strings.TrimSpace(dictionaryName))
	if name == "" {
		return nil
	}
	parts := strings.Split(name, "_")
	out := make([]string, 0, 2)
	if code := NormalizeSizeToken(parts[0]); code != "" {
		out = append(out, code)
	}
	if len(parts) > 1 {
		if digits := leadingDigitRun(parts[1]); digits != "" {
			out = append(out, digits)
		}
	}
	return out
}

// leadingDigitRun returns the FIRST run of digits in s, mirroring the client's /\d+/ match on the
// second segment: «44ta» → "44". Empty when the segment carries no digits at all.
func leadingDigitRun(s string) string {
	start := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			return s[start:i]
		}
	}
	if start >= 0 {
		return s[start:]
	}
	return ""
}

// SizeCoveredByTokens reports whether a size named `dictionaryName` is present in a parsed token
// set. ANY of the size's tokens is enough — the same rule the patterns panel already applies when it
// lists «нет L» (missingIn: a size is missing only when NONE of its tokens was found).
func SizeCoveredByTokens(dictionaryName string, found map[string]bool) bool {
	for _, t := range SizeTokensOf(dictionaryName) {
		if found[t] {
			return true
		}
	}
	return false
}
