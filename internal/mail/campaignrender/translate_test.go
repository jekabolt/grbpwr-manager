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
