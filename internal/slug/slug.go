// Package slug builds the public, human-decorated URLs for products and archive/timeline entries.
// The "pretty" part is DECORATIVE and computed (never stored); the resolve key is the tail token
// (base-SKU for a product, code for an archive), which the frontend extracts and passes to the
// GetProductBySKU / GetArchiveByCode resolvers. This is the one shared implementation — it replaces
// the old ad-hoc dto.GetProductSlug / dto.GetArchiveSlug / dto.GetIdFromSlug builders.
package slug

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// MaxPrettyLen caps the decorative segment so a long product/archive name can't produce an unbounded
// path. It only affects the pretty part; the resolve token is appended after.
const MaxPrettyLen = 80

// Public URL prefixes and the errors the tail parsers return when a path is not a valid tail of the
// expected kind.
const (
	productPrefix  = "/p/"
	timelinePrefix = "/timeline/"
)

var (
	// ErrNotProductTail is returned by ParseProductTail for anything that is not "/p/{pretty-}{base-SKU}".
	ErrNotProductTail = errors.New("slug: not a product tail")
	// ErrNotArchiveTail is returned by ParseArchiveTail for anything that is not "/timeline/{pretty-}{code}".
	ErrNotArchiveTail = errors.New("slug: not an archive tail")
)

// productTailRe matches a base SKU anchored at the END of the pretty-stripped, upper-cased tail:
// {SEASON:2}{YY:2}-{MODEL:5}-{COLOR:3} (fixed 14, e.g. SS26-00021-BLK, mirrors product.BuildBaseSKU).
// The optional greedy `(?:.+-)?` swallows the decorative pretty segment (any number of hyphens, and
// even SKU-like fragments), so the captured group is always the final triplet. Because the SKU is
// anchored at `$`, a variant-size suffix (`-04`), an emergency collision suffix (`…2`) or any trailing
// garbage leaves the string unmatched — those are rejected, not truncated to a base.
var productTailRe = regexp.MustCompile(`^(?:.+-)?([A-Z]{2}[0-9]{2}-[0-9]{5}-[A-Z0-9]{3})$`)

// archiveTailRe matches an archive code anchored at the end: "AR" + 1..10 upper base36 chars (mirrors
// entity.ValidArchiveCode / migration 0148). The code carries no hyphen, so the greedy prefix cleanly
// separates it from the pretty segment.
var archiveTailRe = regexp.MustCompile(`^(?:.+-)?(AR[0-9A-Z]{1,10})$`)

// ParseProductTail parses a public product URL tail "/p/{pretty-}{base-SKU}" and returns the
// normalized upper-case base SKU (e.g. SS26-00021-BLK). This is the single shared parser for the
// storefront cutover and analytics — every consumer must use it instead of ad-hoc splitting, since
// the pretty segment can itself contain hyphens and SKU-like fragments.
//
// It accepts only the exact "/p/" prefix, an optional pretty segment, and the base SKU as the final
// token; the input is case-normalized. It rejects: a wrong/missing prefix, a query or fragment, a
// wrong-width season/model/color, a variant-size suffix, a collision suffix, an empty token and any
// trailing garbage.
func ParseProductTail(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, productPrefix)
	if !ok || strings.ContainsAny(rest, "?#") {
		return "", ErrNotProductTail
	}
	m := productTailRe.FindStringSubmatch(strings.ToUpper(rest))
	if m == nil {
		return "", ErrNotProductTail
	}
	return m[1], nil
}

// ParseArchiveTail parses a public timeline URL tail "/timeline/{pretty-}{code}" and returns the
// normalized upper-case archive code (e.g. AR000C). Same contract and guarantees as ParseProductTail:
// exact "/timeline/" prefix, optional pretty segment, code as the final token, case-normalized;
// rejects wrong prefix, query/fragment, empty token and trailing garbage.
func ParseArchiveTail(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, timelinePrefix)
	if !ok || strings.ContainsAny(rest, "?#") {
		return "", ErrNotArchiveTail
	}
	m := archiveTailRe.FindStringSubmatch(strings.ToUpper(rest))
	if m == nil {
		return "", ErrNotArchiveTail
	}
	return m[1], nil
}

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
