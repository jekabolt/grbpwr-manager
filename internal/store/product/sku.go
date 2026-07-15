package product

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// SKU format (SKU redesign, tasks 01-06):
//
//	base:    {SEASON}{YY}-{MODEL:5}-{COLOR:3}      fixed 14, e.g. SS26-00021-BLK
//	variant: {base}-{SIZE_ORD:2}                   fixed 17, e.g. SS26-00021-BLK-04
//
// The builders below are PURE: all style-vs-standalone resolution, dictionary lookups and DB
// collision checks happen in the store resolver (BuildProductSKUs, task 07) which feeds these
// validated segments. Keeping the builders pure makes them fully unit-testable.

const (
	// BaseSKULen is the fixed character length of a base SKU (SS26-00021-BLK).
	BaseSKULen = 14
	// VariantSKULen is the fixed character length of a variant SKU (SS26-00021-BLK-04).
	VariantSKULen = 17

	// colorFallback is the last-resort colour segment; it is a seeded dictionary code so it is also
	// FK-valid if ever written to color_code (though the generator only ever puts it in the string).
	colorFallback = "UNK"
	// seasonFallback is used when a product/style has no valid season enum.
	seasonFallback = "SS"
)

// SKUSegments are the resolved, dictionary-checked inputs for one product's SKU. The store resolver
// (task 07) fills these from either the linked style (colourway) or the standalone product.
type SKUSegments struct {
	Season    entity.SeasonEnum // SS/FW/PF/RC; empty -> seasonFallback
	Year      int               // full year, e.g. 2026 (only the last two digits are used)
	ModelNo   int               // 1..99999
	ColorCode string            // resolved dictionary code (3 chars) when known; else ""
	ColorName string            // free-text colour name, used to derive a segment when ColorCode==""
}

// BuildBaseSKU renders the fixed-14 base SKU from resolved segments.
func BuildBaseSKU(s SKUSegments) string {
	return fmt.Sprintf("%s-%s-%s",
		seasonSegment(s.Season, s.Year),
		modelSegment(s.ModelNo),
		colorSegment(s.ColorCode, s.ColorName),
	)
}

// BuildVariantSKU appends the 2-digit size ordinal to a base SKU (fixed 17).
func BuildVariantSKU(base string, sizeOrd int) string {
	return fmt.Sprintf("%s-%s", base, sizeSegment(sizeOrd))
}

// seasonSegment is {SEASON}{YY}: a valid 2-letter season code (fallback SS) + 2-digit year.
func seasonSegment(season entity.SeasonEnum, year int) string {
	code := string(season)
	if !entity.IsValidSeason(season) {
		code = seasonFallback
	}
	yy := year % 100
	if yy < 0 {
		yy = 0
	}
	return fmt.Sprintf("%s%02d", code, yy)
}

// modelSegment is the 5-digit zero-padded model number. Numbers wider than 5 digits (>99999,
// astronomically unlikely) are rendered as-is, breaking the fixed length — the store logs on
// approach to the ceiling.
func modelSegment(modelNo int) string {
	if modelNo < 0 {
		modelNo = 0
	}
	return fmt.Sprintf("%05d", modelNo)
}

// sizeSegment is the 2-digit zero-padded size ordinal. The store refuses to build a variant SKU when
// the ordinal is 0 (unseeded size), so 0 never reaches here in practice.
func sizeSegment(ord int) string {
	if ord < 0 {
		ord = 0
	}
	return fmt.Sprintf("%02d", ord)
}

// colorSegment resolves the 3-char colour segment: a known dictionary code wins; otherwise the
// free-text name is transliterated and its first three Latin letters are used; failing that, UNK.
// NOTE: a transliterated fallback is only ever placed in the SKU string, never written to
// product.color_code (which is FK-constrained to the dictionary) — see plan review 70 §3.2.
func colorSegment(code, name string) string {
	if c := sanitizeAlpha(code); len(c) >= 3 {
		return c[:3]
	}
	if c := sanitizeAlpha(Translit(name)); len(c) >= 3 {
		return c[:3]
	}
	return colorFallback
}

// sanitizeAlpha uppercases s and keeps only ASCII letters A-Z.
func sanitizeAlpha(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cyrillicToLatin maps Russian Cyrillic letters to a Latin transliteration (BGN/PCGN-ish, uppercased
// at call sites). Multi-letter results ( zh, kh, ...) are fine — colorSegment takes the first three.
var cyrillicToLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya",
}

// Translit transliterates Cyrillic to Latin and drops any character that is neither an ASCII letter
// nor digit. Latin/digit input passes through unchanged. Used for the colour-name fallback and for
// any free-text that must reduce to the SKU charset.
func Translit(s string) string {
	var b strings.Builder
	for _, r := range s {
		lower := unicode.ToLower(r)
		if repl, ok := cyrillicToLatin[lower]; ok {
			if unicode.IsUpper(r) {
				b.WriteString(strings.ToUpper(repl))
			} else {
				b.WriteString(repl)
			}
			continue
		}
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
