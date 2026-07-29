package mail

import "testing"

// TestLocalNameFunc verifies the render-time product-name selector: it returns the
// current render locale's "Brand Name" when present, and falls back to the supplied
// default-language name otherwise (missing locale, empty value, or nil map). This is
// how an order line follows the recipient's resolved email locale even when an explicit
// account email_language overrides the purchase-time locale hint.
func TestLocalNameFunc(t *testing.T) {
	m := createTestMailer(t)

	names := map[string]string{
		"en": "GRBPWR TEE",
		"ja": "GRBPWR ティー",
		"zh": "GRBPWR T恤",
		"ko": "", // present-but-empty must fall back, not render blank
	}
	const def = "GRBPWR TEE"

	cases := []struct {
		locale string
		names  map[string]string
		want   string
	}{
		{"ja", names, "GRBPWR ティー"},      // exact locale match
		{"zh", names, "GRBPWR T恤"},       // cn->zh canonical match
		{"ko", names, def},               // present-but-empty -> default
		{"fr", names, def},               // locale absent -> default
		{"en", names, "GRBPWR TEE"},      // default render still goes through the map
		{"de", nil, def},                 // nil map -> default
		{"it", map[string]string{}, def}, // empty map -> default
	}

	for _, c := range cases {
		fn, ok := localeFuncMap(m.catalog.Localizer(c.locale))["localName"].(func(map[string]string, string) string)
		if !ok {
			t.Fatalf("[%s] localName func not registered with expected signature", c.locale)
		}
		if got := fn(c.names, def); got != c.want {
			t.Errorf("[%s] localName = %q, want %q", c.locale, got, c.want)
		}
	}
}
