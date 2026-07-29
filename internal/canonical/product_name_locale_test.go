package canonical

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestProductNameForLanguageID(t *testing.T) {
	langs := []entity.Language{
		{Id: 1, Code: "en", IsDefault: true},
		{Id: 5, Code: "ja"},
		{Id: 6, Code: "cn"},
	}
	tr := []entity.ColorwayTranslationInsert{
		{LanguageId: 1, Name: "SYSTEM JACKET"},
		{LanguageId: 5, Name: "システム ジャケット"},
		{LanguageId: 6, Name: ""}, // present but empty → must fall back, not render blank
	}

	cases := []struct {
		name       string
		items      []entity.ColorwayTranslationInsert
		languageID int
		want       string
	}{
		{"exact language match", tr, 5, "システム ジャケット"},
		{"missing language → canonical default (en)", tr, 4, "SYSTEM JACKET"},
		{"empty translation → canonical default", tr, 6, "SYSTEM JACKET"},
		{"default language itself", tr, 1, "SYSTEM JACKET"},
		{"no translations → not ok", nil, 5, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ProductNameForLanguageID(c.items, c.languageID, langs)
			if c.want == "" {
				if ok {
					t.Errorf("expected ok=false, got %q", got)
				}
				return
			}
			if !ok || got != c.want {
				t.Errorf("got (%q, %v), want (%q, true)", got, ok, c.want)
			}
		})
	}
}
