package mail

import (
	"encoding/json"
	"testing"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// TestCatalogLoadsLocaleContent proves each non-en locale file is actually loaded and
// resolves to its own translation (not the en value, not the raw key). It is the companion
// to TestLocaleFilesIntegrity: integrity checks structure, this checks the content is live.
func TestCatalogLoadsLocaleContent(t *testing.T) {
	m := createTestMailer(t)

	// A heading translated in every locale — must come back non-empty and not the raw key.
	const key = "order.confirmed.heading"
	en := m.catalog.Localizer(defaultLocale).S(key, nil)

	for _, code := range supportedLocales {
		loc := m.catalog.Localizer(code)
		if loc.Code() != code {
			t.Errorf("Localizer(%q).Code() = %q", code, loc.Code())
		}
		got := loc.S(key, nil)
		if got == "" || got == key {
			t.Errorf("[%s] %s did not resolve to content: %q", code, key, got)
		}
		if code != defaultLocale && got == en {
			t.Errorf("[%s] %s = %q equals the en value — locale content not loaded", code, key, got)
		}
	}

	// Unknown locale clamps to en; a truly missing key echoes the key (never empty).
	if got := m.catalog.Localizer("xx").Code(); got != defaultLocale {
		t.Errorf("Localizer(xx).Code() = %q, want %q", got, defaultLocale)
	}
	if got := m.catalog.Localizer("en").S("does.not.exist", nil); got != "does.not.exist" {
		t.Errorf("missing key = %q, want the key echoed", got)
	}
}

// TestLocEnFallback proves the load-bearing per-key en fallback in Loc.localize, independent
// of the shipped catalog files (which are now complete): go-i18n's built-in tag fallback does
// NOT resolve en for a key missing from a partial non-en locale, so Loc.localize must. A key
// present in en but absent from the locale resolves to en; a key missing everywhere echoes;
// and an empty locale value falls through to en rather than rendering blank.
func TestLocEnFallback(t *testing.T) {
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)
	mustLoad := func(code, body string) {
		if _, err := bundle.ParseMessageFileBytes([]byte(body), "l."+code+".json"); err != nil {
			t.Fatalf("parse %s: %v", code, err)
		}
	}
	// en has both keys; the partial locale "zz" has an empty value for one and omits the other.
	mustLoad("en", `{"present":"EN VALUE","blankable":"EN BLANKABLE"}`)
	mustLoad("zz", `{"blankable":""}`)

	loc := &Loc{
		code: "zz",
		l:    i18n.NewLocalizer(bundle, "zz", "en"),
		en:   i18n.NewLocalizer(bundle, "en"),
	}

	if got := loc.S("present", nil); got != "EN VALUE" {
		t.Errorf("missing-in-locale key: got %q, want en fallback %q", got, "EN VALUE")
	}
	if got := loc.S("blankable", nil); got != "EN BLANKABLE" {
		t.Errorf("empty-in-locale value: got %q, want en fallback %q", got, "EN BLANKABLE")
	}
	if got := loc.S("nowhere", nil); got != "nowhere" {
		t.Errorf("missing-everywhere key: got %q, want the key echoed", got)
	}
}
