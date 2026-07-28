package mail

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.json
var localesFS embed.FS

// supportedLocales is the canonical email locale set = the 7 storefront locales.
// en is the default and the per-key fallback for every other locale.
var supportedLocales = []string{"en", "fr", "de", "it", "ja", "zh", "ko"}

const defaultLocale = "en"

// normalizeLocale lowercases, strips any region subtag, maps the admin-side
// cn/kr codes to their ISO zh/ko equivalents, and clamps to a supported locale.
// Unknown or empty input → defaultLocale. This is the single place locale strings
// are sanitized before a Localizer is chosen.
func normalizeLocale(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	switch c {
	case "cn":
		c = "zh"
	case "kr":
		c = "ko"
	}
	for _, s := range supportedLocales {
		if c == s {
			return c
		}
	}
	return defaultLocale
}

// Catalog holds the parsed go-i18n bundle and a Localizer per supported locale.
// Built once at Mailer construction from the embedded locales/*.json files.
type Catalog struct {
	bundle *i18n.Bundle
	locs   map[string]*i18n.Localizer
}

func newCatalog() (*Catalog, error) {
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)
	for _, code := range supportedLocales {
		if _, err := bundle.LoadMessageFileFS(localesFS, "locales/"+code+".json"); err != nil {
			return nil, fmt.Errorf("load locale %s: %w", code, err)
		}
	}
	c := &Catalog{bundle: bundle, locs: make(map[string]*i18n.Localizer, len(supportedLocales))}
	for _, code := range supportedLocales {
		// Fallback chain: requested locale → en. go-i18n tries tags left→right.
		c.locs[code] = i18n.NewLocalizer(bundle, code, defaultLocale)
	}
	return c, nil
}

// Localizer returns a locale-bound translator, clamping code to a supported locale.
func (c *Catalog) Localizer(code string) *Loc {
	code = normalizeLocale(code)
	return &Loc{code: code, l: c.locs[code]}
}

// Loc is a locale-bound translator handed to templates and the subject builder.
type Loc struct {
	code string
	l    *i18n.Localizer
}

// Code returns the resolved locale code (e.g. "ja").
func (t *Loc) Code() string { return t.code }

// S returns the localized string for key, interpolating data (a map or struct).
// A missing key falls back to en (via the localizer chain) and finally to the key
// itself — never empty, never a hard error — so a missing translation degrades to
// English rather than breaking the email.
func (t *Loc) S(key string, data any) string {
	s, err := t.l.Localize(&i18n.LocalizeConfig{MessageID: key, TemplateData: data})
	if err != nil || s == "" {
		return key
	}
	return s
}

// Plural returns the localized CLDR-pluralized string for key given count n.
// data may carry extra interpolation vars; {{.Count}} is set to n.
func (t *Loc) Plural(key string, n int, data map[string]any) string {
	if data == nil {
		data = map[string]any{}
	}
	data["Count"] = n
	s, err := t.l.Localize(&i18n.LocalizeConfig{MessageID: key, TemplateData: data, PluralCount: n})
	if err != nil || s == "" {
		return key
	}
	return s
}

// pairsToMap turns alternating key/value template args into a map, mirroring the
// existing dict helper so `{{ t "key" "Name" .Name }}` works in templates.
func pairsToMap(kv ...any) map[string]any {
	if len(kv) == 0 {
		return nil
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	return m
}

// emph wraps a value in the brand emphasis span, HTML-escaping the value first so
// untrusted content cannot inject markup. Use it inside a tHTML message that carries a
// matching {{.Var}} placeholder, e.g.
//
//	{{ tHTML "tier.thanks.body" "Tier" (emph .TierDisplay) }}
//
// with the catalog value "... WELCOME TO {{.Tier}} ...".
func emph(v any) template.HTML {
	return template.HTML(`<span style="color:#0E0E0C;">` + html.EscapeString(fmt.Sprint(v)) + `</span>`)
}

// localeFuncMap returns the per-render, locale-bound overrides for the i18n template
// funcs. Registered onto a cloned template just before execution so the shared parsed
// templates are never mutated.
//
// t returns an auto-escaped string (for plain copy). tHTML returns template.HTML for
// catalog messages that contain trusted inline markup (e.g. an emphasis span around an
// interpolated value); its interpolated values must be pre-escaped (see emph) — no
// user-controlled free text is ever passed to a tHTML message.
func localeFuncMap(loc *Loc) map[string]any {
	return map[string]any{
		"t": func(key string, kv ...any) string {
			return loc.S(key, pairsToMap(kv...))
		},
		"tHTML": func(key string, kv ...any) template.HTML {
			return template.HTML(loc.S(key, pairsToMap(kv...)))
		},
		"emph": emph,
		"plural": func(key string, n int, kv ...any) string {
			return loc.Plural(key, n, pairsToMap(kv...))
		},
		"curLang": loc.Code,
	}
}
