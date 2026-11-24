package mail

import (
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	// shopSenderSuffix closes every team shop's sender name: "<shop> via lekalo". The platform owns
	// the sending address; the suffix says so in every inbox, whatever the shop is called.
	shopSenderSuffix = " via lekalo"
	// shopFallbackName stands in for a name the checks refuse (empty, reserved, a look-alike).
	shopFallbackName = "shop"
	// shopNameMaxRunes bounds the display name (the sender line and the letter header).
	shopNameMaxRunes = 40
)

// reservedShopKey reports whether a look-alike key names the platform or the storefront brand:
// no team shop may appear as grbpwr or lekalo, spelled any way.
func reservedShopKey(key string) bool {
	for _, word := range []string{"grbpwr", "lekalo"} {
		if strings.Contains(key, word) {
			return true
		}
	}
	return false
}

// cleanShopName keeps what may stand in a sender name and a letter header: letters, digits, marks,
// spaces and a few marks of punctuation. Controls, invisible format characters (bidi overrides,
// zero-width joiners), quotes, angle brackets and the at sign go, so a name can neither break the
// header nor pass for an address. Runs of spaces collapse; the result is at most 40 runes.
func cleanShopName(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		keep := unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
		if !keep {
			switch r {
			case '-', '\'', '&', '.', '!', '+', '_':
				keep = true
			}
		}
		if !keep {
			space = b.Len() > 0
			continue
		}
		if n >= shopNameMaxRunes {
			break
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// lookalikeKey folds a name to the letters it reads as: compatibility forms decomposed, accents
// dropped, case folded, common Cyrillic and Greek homoglyphs and digit stand-ins mapped to the Latin
// letter they imitate, "rn" read as "m" and "vv" as "w", everything else removed. Symbol stand-ins
// (! | @ $) are read as letters, or dropped when dropSymbols is set: "1ekal0" and "Acme!" must both be
// caught. Two names that share a key look the same in an inbox.
func lookalikeKey(s string, dropSymbols bool) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		if dropSymbols && symbolStandIn(r) {
			continue
		}
		if m := homoglyph(r); m != 0 {
			r = m
		}
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	k := b.String()
	k = strings.ReplaceAll(k, "rn", "m")
	k = strings.ReplaceAll(k, "vv", "w")
	return k
}

// lookalikeKeys is every non-empty reading of a name (symbols as letters, symbols dropped).
func lookalikeKeys(s string) []string {
	var keys []string
	for _, drop := range []bool{false, true} {
		if k := lookalikeKey(s, drop); k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// symbolStandIn: punctuation that homoglyph reads as a letter.
func symbolStandIn(r rune) bool {
	switch r {
	case '!', '|', '@', '$':
		return true
	}
	return false
}

// readsReserved reports whether any reading of a name is grbpwr or lekalo.
func readsReserved(name string) bool {
	for _, k := range lookalikeKeys(name) {
		if reservedShopKey(k) {
			return true
		}
	}
	return false
}

// homoglyph maps a lower-case rune that imitates a Latin letter to that letter; 0 when it imitates
// none. The letter i is read as l (an upper-case I and a lower-case l look alike in most inboxes).
func homoglyph(r rune) rune {
	switch r {
	case '0', 'о', 'ο', 'σ':
		return 'o'
	case '1', '|', '!', 'i', 'ı', 'і', 'ι', 'ӏ', 'l':
		return 'l'
	case '3', 'е', 'ё', 'є', 'ε':
		return 'e'
	case '4', '@', 'а', 'α':
		return 'a'
	case '5', '$', 'ѕ':
		return 's'
	case '7', 'т', 'τ':
		return 't'
	case '8', '6', 'в', 'ь', 'β':
		return 'b'
	case '9', 'ɡ':
		return 'g'
	case 'р', 'ρ':
		return 'p'
	case 'с', 'ϲ':
		return 'c'
	case 'у', 'γ':
		return 'y'
	case 'х', 'χ':
		return 'x'
	case 'к', 'κ':
		return 'k'
	case 'м':
		return 'm'
	case 'н', 'η':
		return 'h'
	case 'г':
		return 'r'
	case 'ԁ':
		return 'd'
	case 'ν':
		return 'v'
	case 'ω', 'ш':
		return 'w'
	}
	return 0
}

// shopDisplayName is the name a team shop's letters carry: the cleaned name, or "shop" when it is
// empty, reads as grbpwr or lekalo, or reads the same as another team's name or brand (taken). Two
// teams that share a name both get "shop": neither can be told from the other, so neither may use it.
func shopDisplayName(name string, taken []string) string {
	clean := cleanShopName(name)
	keys := lookalikeKeys(clean)
	if len(keys) == 0 || readsReserved(clean) {
		return shopFallbackName
	}
	for _, other := range taken {
		for _, k := range lookalikeKeys(cleanShopName(other)) {
			if slices.Contains(keys, k) {
				return shopFallbackName
			}
		}
	}
	return clean
}
