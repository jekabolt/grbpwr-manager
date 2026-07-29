package mail

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestCampaignFooterStrings verifies the Mailer resolves campaign-footer labels from the shared
// catalog by mapping the recipient language_id to a locale code (canonicalized: cn→zh), and that a
// non-en language differs from en.
func TestCampaignFooterStrings(t *testing.T) {
	m := createTestMailer(t)
	langs := []entity.Language{
		{Id: 1, Code: "en", IsDefault: true},
		{Id: 5, Code: "ja"},
		{Id: 6, Code: "cn"}, // admin-side code; canonicalizes to zh
	}

	en := m.CampaignFooterStrings(1, langs)
	if en.Help == "" || en.Faq == "" || en.UnsubWord == "" {
		t.Fatalf("en footer has empty fields: %+v", en)
	}
	// Must match the catalog directly for en.
	if want := m.catalog.Localizer("en").S("common.footer.help", nil); en.Help != want {
		t.Errorf("en.Help = %q, want %q", en.Help, want)
	}

	ja := m.CampaignFooterStrings(5, langs)
	if want := m.catalog.Localizer("ja").S("common.footer.help", nil); ja.Help != want {
		t.Errorf("ja.Help = %q, want catalog ja %q", ja.Help, want)
	}
	if ja.Help == en.Help {
		t.Errorf("ja.Help == en.Help (%q) — footer not localized", ja.Help)
	}

	// language_id 6 (code cn) must resolve to the zh catalog entry (cn→zh canonicalization).
	cn := m.CampaignFooterStrings(6, langs)
	if want := m.catalog.Localizer("zh").S("common.footer.help", nil); cn.Help != want {
		t.Errorf("cn(id6).Help = %q, want zh catalog %q", cn.Help, want)
	}

	// Unknown language id → default locale (en).
	unknown := m.CampaignFooterStrings(999, langs)
	if unknown.Help != en.Help {
		t.Errorf("unknown language id should default to en: got %q", unknown.Help)
	}
}
