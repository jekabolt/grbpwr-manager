package entity

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ─── T45: a colourway is a free name + an ordered palette + an immutable SKU token ───
//
// Until T45 one dictionary code (product.color_code, 17 colours) played four roles at once: the
// colour's identity, the colour segment of the SKU, the per-style uniqueness key and the catalogue
// filter. The owner's decisions (27.09, 06-COLOURWAYS-RESEARCH.md §7) split them:
//
//   - the SKU segment is product.sku_color_token — minted by the server when the colourway is
//     created and never changed afterwards (unique per style);
//   - product.color_code stays, mandatory, as the dictionary FAMILY tag (filters, aux-output
//     assembly matching); several colourways of one style may share a family once migration
//     0377 drops uniq_product_style_color (the second T45 push — until then the family unique
//     refuses the second one as ErrColorwayFamilyTaken);
//   - the colour itself is an ordered palette of 1…8 colours (product_colour), each a Pantone code
//     or a free label, with a hex for preview; the first colour is the main one and is mirrored into
//     product.pantone / pantone_system / dev_hex for readers that predate the palette;
//   - the name the buyer reads is the operator's name (product.dev_name) plus per-language
//     translations (product_colour_name_i18n); a missing translation falls back to the name.
//
// A colourway with ZERO palette rows is a legacy single-colour colourway: its colour is still read
// from pantone / dev_hex / the dictionary family exactly as before T45.

// ColorwayColour is one colour of a colourway palette (one product_colour row). Position is its
// place in the palette (0 = the main colour) and is implied by the slice order on writes.
type ColorwayColour struct {
	Label         string // free words, e.g. «bone white»; required when Pantone is empty
	Hex           string // "#RRGGBB" screen preview, or "" when none is known
	Pantone       string // Pantone code as typed, e.g. «19-4005 TCX»; required when Label is empty
	PantoneSystem string // the Pantone book (TCX, TPG, C, …); only together with Pantone
}

const (
	// ColorwayPaletteMaxColours bounds a palette (owner's decision 5: 1…8 colours, first = main).
	ColorwayPaletteMaxColours = 8
	// Column widths of product_colour (0375), counted in characters like MySQL counts VARCHAR.
	ColorwayColourLabelMaxRunes   = 64
	ColorwayColourPantoneMaxRunes = 64
	ColorwayColourSystemMaxRunes  = 8
	// ColorwayNameI18nMaxRunes is the width of product_colour_name_i18n.name (0375).
	ColorwayNameI18nMaxRunes = 128
	// ColorwayNameI18nMaxEntries bounds one write; the storefront has a handful of languages.
	ColorwayNameI18nMaxEntries = 64
)

// IsHexColour reports whether s is a "#RRGGBB" colour (either case).
func IsHexColour(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// NormalizeColorwayPalette validates and canonicalises a palette that is about to be WRITTEN.
// field is the wire path of the list ("development.colours"); violations name the offending
// element ("development.colours[2].label").
//
// Rules (owner's decisions 5 and 6): 1…8 colours; each colour is a Pantone code OR a free label
// (at least one of the two); a Pantone system is only meaningful next to a Pantone code; hex is
// optional and, when given, must be #RRGGBB. Values are trimmed, hex and the system upper-cased.
func NormalizeColorwayPalette(colours []ColorwayColour, field string) ([]ColorwayColour, *ValidationError) {
	if len(colours) == 0 || len(colours) > ColorwayPaletteMaxColours {
		return nil, NewFieldViolation(field, "palette_size",
			fmt.Sprintf("%d colours", len(colours)),
			fmt.Sprintf("a palette holds 1 to %d colours, the main one first", ColorwayPaletteMaxColours))
	}
	out := make([]ColorwayColour, 0, len(colours))
	for i, c := range colours {
		at := fmt.Sprintf("%s[%d]", field, i)
		n := ColorwayColour{
			Label:         strings.TrimSpace(c.Label),
			Hex:           strings.ToUpper(strings.TrimSpace(c.Hex)),
			Pantone:       strings.TrimSpace(c.Pantone),
			PantoneSystem: strings.ToUpper(strings.TrimSpace(c.PantoneSystem)),
		}
		if n.Pantone == "" && n.Label == "" {
			return nil, NewFieldViolation(at, "empty_colour", "",
				"name the colour: a Pantone code, or a label when no Pantone fits")
		}
		if n.PantoneSystem != "" && n.Pantone == "" {
			return nil, NewFieldViolation(at+".pantone_system", "system_without_pantone", n.PantoneSystem,
				"a Pantone system belongs to a Pantone code; send the code or leave the system empty")
		}
		if n.Hex != "" && !IsHexColour(n.Hex) {
			return nil, NewFieldViolation(at+".hex", "invalid_hex", n.Hex, "send the preview colour as #RRGGBB, or leave it empty")
		}
		if utf8.RuneCountInString(n.Label) > ColorwayColourLabelMaxRunes {
			return nil, NewFieldViolation(at+".label", "too_long", "",
				fmt.Sprintf("a colour label is at most %d characters", ColorwayColourLabelMaxRunes))
		}
		if utf8.RuneCountInString(n.Pantone) > ColorwayColourPantoneMaxRunes {
			return nil, NewFieldViolation(at+".pantone", "too_long", "",
				fmt.Sprintf("a Pantone code is at most %d characters", ColorwayColourPantoneMaxRunes))
		}
		if utf8.RuneCountInString(n.PantoneSystem) > ColorwayColourSystemMaxRunes {
			return nil, NewFieldViolation(at+".pantone_system", "too_long", n.PantoneSystem,
				fmt.Sprintf("a Pantone system is at most %d characters (TCX, TPG, C, …)", ColorwayColourSystemMaxRunes))
		}
		out = append(out, n)
	}
	return out, nil
}

// NormalizeColorwayNameI18n validates a per-language name write keyed by language id. A non-empty
// value upserts that language's translation; "" deletes it (the reader then falls back to the
// operator's name). Unknown language ids are the store's to refuse (it holds the language table).
func NormalizeColorwayNameI18n(names map[int]string, field string) (map[int]string, *ValidationError) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > ColorwayNameI18nMaxEntries {
		return nil, NewFieldViolation(field, "too_many", fmt.Sprintf("%d languages", len(names)),
			fmt.Sprintf("send at most %d translations per write", ColorwayNameI18nMaxEntries))
	}
	// Sorted, so a request with two faults is always refused on the same one.
	langs := make([]int, 0, len(names))
	for lang := range names {
		langs = append(langs, lang)
	}
	sort.Ints(langs)
	out := make(map[int]string, len(names))
	for _, lang := range langs {
		name := names[lang]
		at := fmt.Sprintf("%s[%d]", field, lang)
		if lang <= 0 {
			return nil, NewFieldViolation(at, "invalid_language", "", "key the translation by a language id from the dictionary")
		}
		name = strings.TrimSpace(name)
		if utf8.RuneCountInString(name) > ColorwayNameI18nMaxRunes {
			return nil, NewFieldViolation(at, "too_long", "",
				fmt.Sprintf("a translated colourway name is at most %d characters", ColorwayNameI18nMaxRunes))
		}
		out[lang] = name
	}
	return out, nil
}

// CheckColorwayPaletteName is the rule that a colourway WITH a palette carries its own name (owner's
// decision 3: a colourway is a name + its translations + a palette). Without one, product.color —
// the name orders, lays and the storefront cart print — silently falls back to the family's
// dictionary name, and a «black and white» colourway would read as «black» everywhere.
//
// hasPalette and name are the state AFTER the write; introducing says whether this write is the
// one bringing the palette (a create with colours, a legacy colourway given its first palette) or
// leaves a standing one (a rename), which is all that changes the words. nil = the rule holds.
func CheckColorwayPaletteName(hasPalette bool, name string, introducing bool) *ValidationError {
	if !hasPalette || strings.TrimSpace(name) != "" {
		return nil
	}
	if introducing {
		return NewFieldViolation("development.name", "name_required_with_palette", "",
			"a colourway with a palette carries its own name — send development.name with the colours")
	}
	return NewFieldViolation("development.name", "name_required_with_palette", "",
		"this colourway has a palette, and a palette colourway carries its own name — rename it instead "+
			"of clearing the name")
}

// ─── nearest dictionary family (owner's decision 4) ───

// oklab is a colour in Björn Ottosson's OKLab space (2020): perceptually close to uniform while
// being two 3×3 matrices and a cube root away from sRGB.
type oklab struct{ l, a, b float64 }

func (c oklab) chroma() float64 { return math.Hypot(c.a, c.b) }
func (c oklab) hue() float64    { return math.Atan2(c.b, c.a) }

func srgbChannelToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// hexToOKLab converts "#RRGGBB" to OKLab; ok=false for anything else.
func hexToOKLab(hex string) (oklab, bool) {
	hex = strings.TrimSpace(hex)
	if !IsHexColour(hex) {
		return oklab{}, false
	}
	var rgb [3]float64
	for i := 0; i < 3; i++ {
		var v int
		if _, err := fmt.Sscanf(hex[1+2*i:3+2*i], "%02x", &v); err != nil {
			return oklab{}, false
		}
		rgb[i] = srgbChannelToLinear(float64(v) / 255)
	}
	r, g, b := rgb[0], rgb[1], rgb[2]
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return oklab{
		l: 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		a: 1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		b: 0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}, true
}

// The family measure. WHY NOT PLAIN EUCLIDEAN OKLab: the dictionary's anchors are pure swatches
// (BLK #000000, NAV #1A2238, GRY #808080 — 0130), while a fabric black is not #000000: Pantone
// 19-4005 TCX, 19-3911 TCX, 19-0303 TCX sit at OKLab lightness 0.27–0.30, which is nearer to
// NAV (0.26) than to BLK (0). Plain distance filed every one of them under navy. The fix is how
// colour naming works for people: first «is it grey-scale at all», then lightness for the greys and
// hue for the colours.
const (
	// A colour is NEUTRAL (reads grey-scale) when its chroma is under familyNeutralChromaFloor, or
	// under familyNeutralSaturation of its lightness but never above familyNeutralChromaCap — the cap
	// keeps light pastels (mint, blush, lavender) out of the greys.
	familyNeutralChromaFloor = 0.02
	familyNeutralSaturation  = 0.10
	familyNeutralChromaCap   = 0.035
	// Weights of the chromatic distance: lightness and chroma matter less than hue, because the
	// dictionary anchors are saturated and a muted fabric (olive, burgundy, forest) differs from its
	// family's anchor mostly in chroma. The hue weight is per radian.
	familyLightnessWeight = 0.35
	familyChromaWeight    = 0.30
	familyHueWeight       = 0.15
)

func (c oklab) neutral() bool {
	ch := c.chroma()
	return ch < familyNeutralChromaFloor || ch < math.Min(familyNeutralSaturation*c.l, familyNeutralChromaCap)
}

// familyDistance is how far a dictionary anchor p is from the colour t, for filing t under a
// family: a neutral t is compared by (down-weighted) lightness and its residual tint; a chromatic t
// by hue first, then lightness and chroma.
func familyDistance(t, p oklab) float64 {
	dl := familyLightnessWeight * (t.l - p.l)
	if t.neutral() {
		da, db := t.a-p.a, t.b-p.b
		return math.Sqrt(dl*dl + da*da + db*db)
	}
	dc := familyChromaWeight * (t.chroma() - p.chroma())
	dh := math.Abs(t.hue() - p.hue())
	if dh > math.Pi {
		dh = 2*math.Pi - dh
	}
	dh *= familyHueWeight
	return math.Sqrt(dl*dl + dc*dc + dh*dh)
}

// NearestColourFamily proposes the dictionary family for a colour (owner's decision 4: the server
// proposes the family nearest to the main colour's hex; the operator confirms or changes it).
//
// Candidates are the NON-ARCHIVED dictionary colours with a hex (a retired family cannot file a new
// colourway; a row without a hex cannot be compared). A neutral colour is matched against the
// neutral families only and a chromatic one against the chromatic ones — falling back to all
// candidates when its own class has none — by familyDistance, in OKLab. Ties resolve to the lower
// code, so the answer is deterministic. ok=false when hex is not #RRGGBB or nothing can be compared.
func NearestColourFamily(hex string, colours []Color) (Color, bool) {
	target, ok := hexToOKLab(hex)
	if !ok {
		return Color{}, false
	}
	type candidate struct {
		c Color
		p oklab
	}
	var same, all []candidate
	for _, c := range colours {
		if c.ArchivedAt.Valid || !c.Hex.Valid {
			continue
		}
		p, ok := hexToOKLab(c.Hex.String)
		if !ok {
			continue
		}
		all = append(all, candidate{c, p})
		if p.neutral() == target.neutral() {
			same = append(same, candidate{c, p})
		}
	}
	pool := same
	if len(pool) == 0 {
		pool = all
	}
	var best Color
	bestDist := math.Inf(1)
	found := false
	for _, cand := range pool {
		d := familyDistance(target, cand.p)
		if !found || d < bestDist || (d == bestDist && cand.c.Code < best.Code) {
			best, bestDist, found = cand.c, d, true
		}
	}
	return best, found
}

// ─── the SKU colour token (owner's decisions 1 and 2) ───

var skuColorTokenRe = regexp.MustCompile(`^[A-Z0-9]{3}$`)

// IsValidSkuColorToken reports whether t has the shape of the SKU colour segment: exactly three
// upper-case alphanumerics (the grbpwr-sku-v1 colour segment).
func IsValidSkuColorToken(t string) bool { return skuColorTokenRe.MatchString(t) }

// skuTokenFillerWords are words that carry no colour and would only waste a letter of the token
// («black AND white», «navy WITH red», Russian «и»).
var skuTokenFillerWords = map[string]bool{
	"AND": true, "OR": true, "WITH": true, "THE": true, "OF": true, "ON": true, "IN": true,
	"AT": true, "BY": true, "TO": true, "PLUS": true, "A": true, "AN": true, "I": true, "S": true,
}

// cyrillicToLatin transliterates Russian letters so a Cyrillic name mints a readable token instead
// of falling straight to the C01… sequence («Чёрный» → CHERNYY → CHY).
var cyrillicToLatin = map[rune]string{
	'А': "A", 'Б': "B", 'В': "V", 'Г': "G", 'Д': "D", 'Е': "E", 'Ё': "E", 'Ж': "ZH", 'З': "Z",
	'И': "I", 'Й': "Y", 'К': "K", 'Л': "L", 'М': "M", 'Н': "N", 'О': "O", 'П': "P", 'Р': "R",
	'С': "S", 'Т': "T", 'У': "U", 'Ф': "F", 'Х': "KH", 'Ц': "TS", 'Ч': "CH", 'Ш': "SH", 'Щ': "SHCH",
	'Ъ': "", 'Ы': "Y", 'Ь': "", 'Э': "E", 'Ю': "YU", 'Я': "YA",
}

// skuTokenWords folds a colourway name to upper-case ASCII words: Cyrillic transliterated,
// diacritics stripped (NFD), split on anything that is not A–Z / 0–9, filler words dropped unless
// nothing else is left. Cyrillic goes first because NFD would otherwise decompose Й into И + ◌̆
// and lose the letter the transliteration table means.
func skuTokenWords(name string) []string {
	var folded strings.Builder
	for _, r := range strings.ToUpper(name) {
		if lat, ok := cyrillicToLatin[r]; ok {
			folded.WriteString(lat)
			continue
		}
		for _, d := range norm.NFD.String(string(r)) {
			switch {
			case unicode.Is(unicode.Mn, d):
				continue // a combining mark left by NFD (É → E + ◌́)
			case d >= 'A' && d <= 'Z', d >= '0' && d <= '9':
				folded.WriteRune(d)
			default:
				folded.WriteRune(' ')
			}
		}
	}
	all := strings.Fields(folded.String())
	words := make([]string, 0, len(all))
	for _, w := range all {
		if !skuTokenFillerWords[w] {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return all
	}
	return words
}

func isSkuTokenVowel(b byte) bool {
	switch b {
	case 'A', 'E', 'I', 'O', 'U':
		return true
	}
	return false
}

// skuTokenSkeleton is the first letter of a word followed by its consonants (and digits), with
// doubled letters collapsed: BLACK → BLCK, OFF → OF, WHITE → WHT.
func skuTokenSkeleton(w string) string {
	if w == "" {
		return ""
	}
	sk := []byte{w[0]}
	for i := 1; i < len(w); i++ {
		c := w[i]
		if isSkuTokenVowel(c) || c == sk[len(sk)-1] {
			continue
		}
		sk = append(sk, c)
	}
	return string(sk)
}

// skuTokenPrimary is the token a name reads as, before any collision: one word → first, second and
// last skeleton letters (BLACK → BLK, a word with a shorter skeleton → its first three letters:
// RED → RED); two words → first letter, last skeleton letter of the first word, first letter of
// the second (BLACK WHITE → BKW, OFF WHITE → OFW); three or more → the initials of the first three.
func skuTokenPrimary(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		w := words[0]
		sk := skuTokenSkeleton(w)
		if len(sk) >= 3 {
			return sk[:2] + sk[len(sk)-1:]
		}
		if len(w) >= 3 {
			return w[:3]
		}
		return ""
	case 2:
		sk := skuTokenSkeleton(words[0])
		return words[0][:1] + sk[len(sk)-1:] + words[1][:1]
	default:
		return words[0][:1] + words[1][:1] + words[2][:1]
	}
}

// skuTokenAlphabet is the order of the exhaustive fallback: letters before digits.
const skuTokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// MintColorwaySkuToken returns the SKU colour token for a NEW colourway called name whose
// dictionary family is family. It is deterministic — the same inputs give the same token — and it
// never returns a token that is already in taken (every token the style holds, archived colourways
// included, since a frozen SKU keeps its token for ever) or a dictionary code other than the
// colourway's OWN family (a custom «Midnight» minted as NAV would read as the navy family in every
// SKU). The candidates, in order:
//
//  1. the name's primary reading (skuTokenPrimary): BLACK → BLK, «black and white» → BKW;
//  2. the first letter of the name followed by every ordered pair of its remaining letters —
//     skeleton letters first, then the vowels — and then by each of those letters plus a digit 2…9;
//  3. C01…C99;
//  4. the whole space AAA…999 in skuTokenAlphabet order (only reachable in a style that already
//     holds every earlier candidate, which no real style does).
//
// The result always has the shape IsValidSkuColorToken accepts.
func MintColorwaySkuToken(name, family string, dictionaryCodes []string, taken map[string]bool) string {
	family = NormalizeColorCode(family)
	forbidden := make(map[string]bool, len(dictionaryCodes))
	for _, c := range dictionaryCodes {
		if c = NormalizeColorCode(c); c != family {
			forbidden[c] = true
		}
	}
	usable := func(t string) bool {
		return IsValidSkuColorToken(t) && !taken[t] && !forbidden[t]
	}

	words := skuTokenWords(name)
	if t := skuTokenPrimary(words); usable(t) {
		return t
	}
	if len(words) > 0 {
		first := words[0][0]
		// The letters after the first, in reading order: every word's skeleton first, then the
		// vowels the skeletons dropped — consonants carry a colour name better than vowels do.
		var consonants, vowels []byte
		seen := map[byte]bool{}
		for wi, w := range words {
			sk := skuTokenSkeleton(w)
			start := 0
			if wi == 0 {
				start = 1 // the first letter is the anchor, not a candidate letter
			}
			for i := start; i < len(sk); i++ {
				if !seen[sk[i]] {
					seen[sk[i]] = true
					consonants = append(consonants, sk[i])
				}
			}
		}
		for _, w := range words {
			for i := 0; i < len(w); i++ {
				if isSkuTokenVowel(w[i]) && !seen[w[i]] {
					seen[w[i]] = true
					vowels = append(vowels, w[i])
				}
			}
		}
		rest := append(consonants, vowels...)
		for i := 0; i < len(rest); i++ {
			for j := i + 1; j < len(rest); j++ {
				if t := string([]byte{first, rest[i], rest[j]}); usable(t) {
					return t
				}
			}
		}
		for i := 0; i < len(rest); i++ {
			for d := byte('2'); d <= '9'; d++ {
				if t := string([]byte{first, rest[i], d}); usable(t) {
					return t
				}
			}
		}
	}
	for n := 1; n <= 99; n++ {
		if t := fmt.Sprintf("C%02d", n); usable(t) {
			return t
		}
	}
	for _, a := range []byte(skuTokenAlphabet) {
		for _, b := range []byte(skuTokenAlphabet) {
			for _, c := range []byte(skuTokenAlphabet) {
				if t := string([]byte{a, b, c}); usable(t) {
					return t
				}
			}
		}
	}
	return "" // unreachable: 46 656 tokens cannot all be taken by one style
}

// SortedDictionaryCodes returns the codes of a dictionary list, sorted — the stable input
// MintColorwaySkuToken takes.
func SortedDictionaryCodes(colours []Color) []string {
	out := make([]string, 0, len(colours))
	for _, c := range colours {
		out = append(out, c.Code)
	}
	sort.Strings(out)
	return out
}

// ─── «apply to slots» (owner's decision 7) ───

// ColorwayPaletteSlotAssignment names one material slot — a style BOM line, by its stable
// line_key — and the position in the colourway's SAVED palette whose colour it takes.
type ColorwayPaletteSlotAssignment struct {
	BomLineKey     string
	ColourPosition int
}

// ColorwayPaletteApplyResult is what ApplyColorwayPaletteToSlots did.
type ColorwayPaletteApplyResult struct {
	LockVersion int // the shared tech_card.lock_version after the write
	RowsUpdated int // garment-level recipe rows recoloured
	RowsCreated int // colour-only garment-level rows added for slots that had none
}

// ColorwayPaletteApplyMaxAssignments bounds one «apply to slots» call; a card has far fewer
// colour-bearing BOM lines.
const ColorwayPaletteApplyMaxAssignments = 64

// ErrColorwayNoPalette refuses «apply to slots» on a colourway that has no palette yet — there is
// nothing to apply. The API layer maps it to FailedPrecondition.
var ErrColorwayNoPalette = errors.New("the colourway has no palette yet")

// NormalizePaletteSlotAssignments checks the shape of an «apply to slots» request before anything
// is read: 1…64 assignments, each naming a BOM line key and a non-negative palette position, and no
// line named twice (two colours for one slot would be decided by request order). Keys are trimmed.
// Whether the key is a line of the style and the position exists in the palette is the store's
// check — it holds both.
func NormalizePaletteSlotAssignments(in []ColorwayPaletteSlotAssignment) ([]ColorwayPaletteSlotAssignment, *ValidationError) {
	if len(in) == 0 || len(in) > ColorwayPaletteApplyMaxAssignments {
		return nil, NewFieldViolation("assignments", "assignment_count", fmt.Sprintf("%d assignments", len(in)),
			fmt.Sprintf("name 1 to %d slots, each with the palette position it takes", ColorwayPaletteApplyMaxAssignments))
	}
	out := make([]ColorwayPaletteSlotAssignment, 0, len(in))
	seen := make(map[string]int, len(in))
	for i, a := range in {
		key := strings.TrimSpace(a.BomLineKey)
		if key == "" {
			return nil, NewFieldViolation(fmt.Sprintf("assignments[%d].bom_line_key", i), "required", "",
				"name the slot by its BOM line_key")
		}
		if prev, dup := seen[key]; dup {
			return nil, NewFieldViolation(fmt.Sprintf("assignments[%d].bom_line_key", i), "duplicate_slot",
				fmt.Sprintf("%s (already assigned by assignments[%d])", key, prev),
				"assign each slot once")
		}
		seen[key] = i
		if a.ColourPosition < 0 || a.ColourPosition >= ColorwayPaletteMaxColours {
			return nil, NewFieldViolation(fmt.Sprintf("assignments[%d].colour_position", i), "out_of_palette",
				fmt.Sprintf("position %d", a.ColourPosition),
				fmt.Sprintf("a palette position is 0 (the main colour) to %d", ColorwayPaletteMaxColours-1))
		}
		out = append(out, ColorwayPaletteSlotAssignment{BomLineKey: key, ColourPosition: a.ColourPosition})
	}
	return out, nil
}

// SlotPantone is the Pantone a recipe row takes from a palette colour: the code as typed, with its
// book appended when the code does not already end with it («19-4052» + TCX → «19-4052 TCX»;
// «19-4052 TCX» + TCX stays). The recipe keeps ONE pantone string per row. A result wider than
// the recipe column (64 characters) keeps the code alone.
func SlotPantone(c ColorwayColour) string {
	code := strings.TrimSpace(c.Pantone)
	book := strings.TrimSpace(c.PantoneSystem)
	if code == "" || book == "" {
		return code
	}
	fields := strings.Fields(strings.ToUpper(code))
	if len(fields) > 0 && fields[len(fields)-1] == strings.ToUpper(book) {
		return code
	}
	joined := code + " " + book
	if utf8.RuneCountInString(joined) > ColorwayColourPantoneMaxRunes {
		return code
	}
	return joined
}
