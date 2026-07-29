package campaign

import (
	"database/sql"
	"testing"
)

// ns is a valid sql.NullString; nn is NULL.
func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

var nn = sql.NullString{}

// TestResolveFanoutLanguageID covers the campaign per-recipient language precedence:
// email_language (explicit, sticky) → default_language → the default language id, with
// cn/kr↔zh/ko canonicalization so those recipients resolve instead of falling back.
func TestResolveFanoutLanguageID(t *testing.T) {
	// Mirrors the seeded `language` table (migration 0002): cn/kr codes, canonicalized to zh/ko.
	byCanonical := map[string]int{"en": 1, "fr": 2, "de": 3, "it": 4, "ja": 5, "zh": 6, "ko": 7}
	const defaultID = 1

	cases := []struct {
		name        string
		emailLang   sql.NullString
		defaultLang sql.NullString
		want        int
	}{
		{"email_language wins over default", ns("ja"), ns("fr"), 5},
		{"falls to default_language when no email_language", nn, ns("de"), 3},
		{"zh (stored) resolves via cn row", ns("zh"), nn, 6},
		{"ko (stored) resolves via kr row", nn, ns("ko"), 7},
		{"admin-side cn also resolves to zh id", ns("cn"), nn, 6},
		{"region subtag stripped (fr-FR → fr)", ns("fr-FR"), nn, 2},
		{"unknown email_language falls through to default_language", ns("xx"), ns("it"), 4},
		{"both empty/invalid → default id", nn, nn, 1},
		{"empty strings (not NULL) → default id", ns(""), ns(""), 1},
		{"unknown everywhere → default id", ns("zz"), ns("qq"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveFanoutLanguageID(
				fanoutCandidateRow{EmailLanguage: c.emailLang, DefaultLanguage: c.defaultLang},
				byCanonical, defaultID,
			)
			if got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}
