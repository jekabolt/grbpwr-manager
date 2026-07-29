package mail

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// placeholderRe matches go-i18n/text-template interpolation tokens like {{.OrderID}}.
var placeholderRe = regexp.MustCompile(`\{\{\s*\.[A-Za-z0-9_]+\s*\}\}`)

// htmlTagRe matches HTML tags (open, close, self-closing) so a translation can be checked
// to preserve the exact inline markup an en message carries (e.g. the emphasis span).
var htmlTagRe = regexp.MustCompile(`</?[A-Za-z][^>]*>`)

// loadLocaleStrings reads locales/<code>.json from the embedded FS and asserts every value
// is a flat JSON string (the catalog uses no CLDR plural objects). Returns key -> value.
func loadLocaleStrings(t *testing.T, code string) map[string]string {
	t.Helper()
	b, err := localesFS.ReadFile("locales/" + code + ".json")
	if err != nil {
		t.Fatalf("read locale %s: %v", code, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("locale %s is not valid JSON: %v", code, err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			t.Errorf("locale %s key %q is not a flat string (plural objects are not used): %s", code, k, v)
			continue
		}
		out[k] = s
	}
	return out
}

func placeholderSet(s string) []string {
	m := placeholderRe.FindAllString(s, -1)
	set := make(map[string]struct{}, len(m))
	for _, p := range m {
		// normalize inner whitespace: {{ .X }} == {{.X}}
		set[strings.ReplaceAll(p, " ", "")] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func htmlTagSet(s string) []string {
	m := htmlTagRe.FindAllString(s, -1)
	out := append([]string(nil), m...)
	sort.Strings(out)
	return out
}

// TestLocaleFilesIntegrity is the gate for translation drafts (Ф2). For every non-en locale
// file it enforces, per key present:
//   - the key exists in en (no stray keys the fallback would ignore),
//   - the interpolation placeholder set matches en exactly (a dropped/renamed {{.Var}} would
//     render blank or leak the literal),
//   - the inline HTML tag set matches en exactly (so a tHTML message keeps its markup and a
//     plain message never gains markup).
//
// It deliberately does NOT require completeness — untranslated keys fall back to en by design
// during rollout — but it reports per-locale coverage so progress is visible.
func TestLocaleFilesIntegrity(t *testing.T) {
	en := loadLocaleStrings(t, defaultLocale)
	if len(en) == 0 {
		t.Fatal("en.json is empty")
	}

	for _, code := range supportedLocales {
		if code == defaultLocale {
			continue
		}
		loc := loadLocaleStrings(t, code)
		for key, val := range loc {
			enVal, ok := en[key]
			if !ok {
				t.Errorf("[%s] key %q is not present in en.json", code, key)
				continue
			}
			if enPH, locPH := placeholderSet(enVal), placeholderSet(val); !equalStrs(enPH, locPH) {
				t.Errorf("[%s] key %q placeholder mismatch: en=%v got=%v", code, key, enPH, locPH)
			}
			if enTags, locTags := htmlTagSet(enVal), htmlTagSet(val); !equalStrs(enTags, locTags) {
				t.Errorf("[%s] key %q HTML tag mismatch: en=%v got=%v", code, key, enTags, locTags)
			}
		}
		t.Logf("locale %s coverage: %d/%d keys translated", code, len(loc), len(en))
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
