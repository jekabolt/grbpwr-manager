package campaignrender

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestTranslateFallbackMatrix(t *testing.T) {
	t.Parallel()
	langs := []entity.Language{
		{Id: 1, Code: "en", IsDefault: true},
		{Id: 2, Code: "pl"},
	}
	translations := []entity.EmailBlockTranslation{
		{LanguageID: 2, Heading: "PL"},
		{LanguageID: 1, Heading: "EN"},
	}

	tests := []struct {
		name    string
		items   []entity.EmailBlockTranslation
		wantID  int
		want    string
		langs   []entity.Language
		request int
	}{
		{name: "exact", items: translations, request: 2, langs: langs, wantID: 2, want: "PL"},
		{name: "default english", items: translations, request: 9, langs: langs, wantID: 1, want: "EN"},
		{name: "smallest id", items: translations, request: 9, wantID: 1, want: "EN"},
		{name: "empty", request: 9},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pickTranslation(tt.items, tt.request, tt.langs)
			if got.LanguageID != tt.wantID || got.Heading != tt.want {
				t.Fatalf("pickTranslation() = %#v, want id=%d heading=%q", got, tt.wantID, tt.want)
			}
		})
	}
}

func TestTranslateFillsEmptyFieldsFromDefaultLanguage(t *testing.T) {
	t.Parallel()
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}, {Id: 2, Code: "fr"}}
	translations := []entity.EmailBlockTranslation{
		{
			LanguageID: 1,
			Heading:    "EN HEADING",
			Body:       "<p>EN body</p>",
			CTALabel:   "SHOP",
			CTAURL:     "https://grbpwr.com/en",
			Links:      []entity.EmailLink{{Label: "EN", URL: "https://grbpwr.com/en"}},
		},
		// A partly authored / partly auto-translated locale: heading only.
		{LanguageID: 2, Heading: "FR HEADING"},
	}

	got := pickTranslation(translations, 2, langs)
	if got.Heading != "FR HEADING" {
		t.Fatalf("localized heading was overwritten: %q", got.Heading)
	}
	if got.Body != "<p>EN body</p>" || got.CTALabel != "SHOP" || got.CTAURL != "https://grbpwr.com/en" {
		t.Fatalf("empty fields did not fall back to the default language: %#v", got)
	}
	if len(got.Links) != 1 {
		t.Fatalf("links did not fall back to the default language: %#v", got.Links)
	}
	// The default-language row must not be mutated by the merge.
	if translations[0].Heading != "EN HEADING" || translations[1].Body != "" {
		t.Fatalf("pickTranslation mutated the stored translations: %#v", translations)
	}
}

func TestSelectSubjectTreatsEmptyRowAsAbsent(t *testing.T) {
	t.Parallel()
	langs := []entity.Language{{Id: 1, IsDefault: true}, {Id: 2}}
	subjects := []entity.SubjectTranslation{
		{LanguageID: 1, Subject: "EN subject"},
		{LanguageID: 2, Subject: "   "},
	}
	if got := SelectSubject(subjects, 2, langs); got != "EN subject" {
		t.Fatalf("empty localized subject should fall back, got %q", got)
	}
}

func TestTranslateSubjectUsesSameFallback(t *testing.T) {
	t.Parallel()
	subjects := []entity.SubjectTranslation{
		{LanguageID: 2, Subject: "PL subject"},
		{LanguageID: 1, Subject: "EN subject"},
	}
	langs := []entity.Language{{Id: 1, IsDefault: true}, {Id: 2}}
	if got := SelectSubject(subjects, 2, langs); got != "PL subject" {
		t.Fatalf("exact subject = %q", got)
	}
	if got := SelectSubject(subjects, 9, langs); got != "EN subject" {
		t.Fatalf("default subject = %q", got)
	}
	if got := SelectSubject(nil, 9, langs); got != "" {
		t.Fatalf("empty subject = %q", got)
	}
}
