// Package slug builds the public, human-decorated URLs for products and archive/timeline entries.
// The "pretty" part is DECORATIVE and computed (never stored); the resolve key is the tail token
// (base-SKU for a product, code for an archive), which the frontend extracts and passes to the
// GetProductBySKU / GetArchiveByCode resolvers. This is the one shared implementation — it replaces
// the old ad-hoc dto.GetProductSlug / dto.GetArchiveSlug / dto.GetIdFromSlug builders.
package slug

import (
	"regexp"
	"strings"
	"unicode"
)

// MaxPrettyLen caps the decorative segment so a long product/archive name can't produce an unbounded
// path. It only affects the pretty part; the resolve token is appended after.
const MaxPrettyLen = 80

var nonKebab = regexp.MustCompile(`[^a-z0-9]+`)

// Kebab lowercases s, transliterates Cyrillic to Latin, replaces every run of non [a-z0-9] with a
// single "-", trims leading/trailing "-", and truncates to MaxPrettyLen (re-trimming any "-" the cut
// exposed). It is deterministic and returns "" for input with no usable characters.
func Kebab(s string) string {
	s = translit(s)
	s = strings.ToLower(s)
	s = nonKebab.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > MaxPrettyLen {
		s = strings.Trim(s[:MaxPrettyLen], "-")
	}
	return s
}

// ProductPath builds "/p/{kebab(name)}-{lower(sku)}". When the name kebabs to empty it degrades to
// "/p/{lower(sku)}" (no leading dash). sku is the base SKU (the resolve key).
func ProductPath(name, sku string) string {
	return joinPretty("/p/", Kebab(name), strings.ToLower(sku))
}

// TimelinePath builds "/timeline/{kebab(heading)}-{code}". code is the archive resolve key and is
// used verbatim (already URL-safe).
func TimelinePath(heading, code string) string {
	return joinPretty("/timeline/", Kebab(heading), code)
}

// joinPretty assembles prefix + pretty + "-" + token, omitting the pretty/"-" when pretty is empty.
func joinPretty(prefix, pretty, token string) string {
	if pretty == "" {
		return prefix + token
	}
	return prefix + pretty + "-" + token
}

// cyrillicToLatin maps Russian Cyrillic to a Latin transliteration.
var cyrillicToLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya",
}

// translit converts Cyrillic runes to Latin; other runes pass through (Kebab lowercases and strips
// the non-[a-z0-9] ones afterwards).
func translit(s string) string {
	var b strings.Builder
	for _, r := range s {
		if repl, ok := cyrillicToLatin[unicode.ToLower(r)]; ok {
			b.WriteString(repl)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
