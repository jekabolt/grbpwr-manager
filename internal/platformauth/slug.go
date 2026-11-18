package platformauth

import (
	"crypto/rand"
	"strconv"
	"strings"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"golang.org/x/text/unicode/norm"
)

// maxSlugLen is the longest workspace address (tenant.ValidSlug).
const maxSlugLen = 40

// translitRune maps one Cyrillic or special Latin letter (already lowercased) to Latin, ГОСТ-style.
// ok is false for a rune it does not know; "" with ok=true drops the rune (ъ, ь).
func translitRune(r rune) (string, bool) {
	switch r {
	case 'а':
		return "a", true
	case 'б':
		return "b", true
	case 'в':
		return "v", true
	case 'г':
		return "g", true
	case 'ґ':
		return "g", true
	case 'д':
		return "d", true
	case 'е', 'ё', 'э':
		return "e", true
	case 'є':
		return "ye", true
	case 'ж':
		return "zh", true
	case 'з':
		return "z", true
	case 'и':
		return "i", true
	case 'і':
		return "i", true
	case 'ї':
		return "yi", true
	case 'й':
		return "y", true
	case 'к':
		return "k", true
	case 'л':
		return "l", true
	case 'м':
		return "m", true
	case 'н':
		return "n", true
	case 'о':
		return "o", true
	case 'п':
		return "p", true
	case 'р':
		return "r", true
	case 'с':
		return "s", true
	case 'т':
		return "t", true
	case 'у', 'ў':
		return "u", true
	case 'ф':
		return "f", true
	case 'х':
		return "kh", true
	case 'ц':
		return "ts", true
	case 'ч':
		return "ch", true
	case 'ш':
		return "sh", true
	case 'щ':
		return "shch", true
	case 'ъ', 'ь':
		return "", true
	case 'ы':
		return "y", true
	case 'ю':
		return "yu", true
	case 'я':
		return "ya", true
	// Latin letters NFD does not decompose.
	case 'ß':
		return "ss", true
	case 'ø':
		return "o", true
	case 'æ':
		return "ae", true
	case 'œ':
		return "oe", true
	case 'đ', 'ð':
		return "d", true
	case 'ł':
		return "l", true
	case 'þ':
		return "th", true
	case 'ı':
		return "i", true
	}
	return "", false
}

// Transliterate turns a team name into slug material: lowercase, Cyrillic and Latin diacritics to
// plain Latin, every run of anything else to one hyphen, no hyphen at the ends, at most 40 chars.
// It does not check the result against tenant.ValidSlug (SuggestSlug does).
func Transliterate(name string) string {
	var b strings.Builder
	hyphen := false
	emit := func(s string) {
		for _, c := range s {
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				b.WriteRune(c)
				hyphen = false
			} else if !hyphen && b.Len() > 0 {
				b.WriteByte('-')
				hyphen = true
			}
		}
	}
	for _, r := range strings.ToLower(name) {
		if s, ok := translitRune(r); ok {
			emit(s)
			continue
		}
		// NFD splits "é" into "e" + a combining mark; drop the marks.
		var plain strings.Builder
		for _, d := range norm.NFD.String(string(r)) {
			if !unicode.Is(unicode.Mn, d) {
				plain.WriteRune(d)
			}
		}
		p := plain.String()
		if p == "" {
			continue // a lone combining mark
		}
		if s, ok := translitRune([]rune(p)[0]); ok && len([]rune(p)) == 1 {
			emit(s)
			continue
		}
		emit(p)
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxSlugLen {
		out = strings.TrimRight(out[:maxSlugLen], "-")
	}
	return out
}

// SuggestSlug proposes a free workspace address for a team name: the transliteration, or
// team-<4 random> when that is empty or invalid; when taken, -2, -3 … appended (trimmed to fit).
// taken reports whether a valid slug is already held; an error from it stops the search.
func SuggestSlug(name string, taken func(string) (bool, error)) (string, error) {
	base := Transliterate(name)
	if tenant.ValidSlug(base) != nil {
		r, err := randomSlugTail(4)
		if err != nil {
			return "", err
		}
		base = "team-" + r
	}
	for n := 1; n <= 50; n++ {
		cand := base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			head := base
			if len(head)+len(suffix) > maxSlugLen {
				head = strings.TrimRight(head[:maxSlugLen-len(suffix)], "-")
			}
			cand = head + suffix
		}
		if tenant.ValidSlug(cand) != nil {
			continue
		}
		t, err := taken(cand)
		if err != nil {
			return "", err
		}
		if !t {
			return cand, nil
		}
	}
	// Fifty namesakes: fall back to a random address.
	for range 10 {
		r, err := randomSlugTail(6)
		if err != nil {
			return "", err
		}
		cand := "team-" + r
		t, err := taken(cand)
		if err != nil {
			return "", err
		}
		if !t {
			return cand, nil
		}
	}
	return "", errNoSlug
}

func randomSlugTail(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		// 256 % 36 = 4: the bias of the first four symbols is ~1.5%, harmless for a suggestion.
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}
