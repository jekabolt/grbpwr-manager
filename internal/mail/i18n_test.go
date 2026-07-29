package mail

import "testing"

// TestCatalogFallsBackToEnForEmptyLocale proves the per-key fallback: the 6 non-en locale
// files ship empty, so every key must resolve to the en content until translations land.
func TestCatalogFallsBackToEnForEmptyLocale(t *testing.T) {
	m := createTestMailer(t) // constructs the catalog from the embedded locales

	for _, code := range []string{"fr", "de", "it", "ja", "zh", "ko"} {
		loc := m.catalog.Localizer(code)
		if loc.Code() != code {
			t.Errorf("Localizer(%q).Code() = %q", code, loc.Code())
		}
		if got := loc.S("order.confirmed.heading", nil); got != "ORDER CONFIRMED" {
			t.Errorf("[%s] order.confirmed.heading = %q, want en fallback %q", code, got, "ORDER CONFIRMED")
		}
		if got := loc.S("account.login.subject", nil); got != "Your sign-in code" {
			t.Errorf("[%s] account.login.subject = %q, want en fallback %q", code, got, "Your sign-in code")
		}
		// A subject key with interpolation must still fall back AND interpolate.
		if got := loc.S("order.confirmed.subject", map[string]any{"OrderID": "ORD-1"}); got != "Order ORD-1 confirmed" {
			t.Errorf("[%s] order.confirmed.subject = %q, want %q", code, got, "Order ORD-1 confirmed")
		}
	}

	// Unknown locale clamps to en; a truly missing key echoes the key (never empty).
	if got := m.catalog.Localizer("xx").Code(); got != "en" {
		t.Errorf("Localizer(xx).Code() = %q, want en", got)
	}
	if got := m.catalog.Localizer("en").S("does.not.exist", nil); got != "does.not.exist" {
		t.Errorf("missing key = %q, want the key echoed", got)
	}
}
