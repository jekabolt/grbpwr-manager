package campaignrender

import (
	"github.com/jekabolt/grbpwr-manager/internal/canonical"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func pickTranslation(
	translations []entity.EmailBlockTranslation,
	languageID int,
	langs []entity.Language,
) entity.EmailBlockTranslation {
	for _, translation := range translations {
		if translation.LanguageID == languageID {
			return translation
		}
	}
	selected, _ := canonical.Select(
		translations,
		func(translation entity.EmailBlockTranslation) int { return translation.LanguageID },
		canonical.IsDefaultFunc(langs),
	)
	return selected
}

// SelectSubject uses the same requested/default/smallest-id language policy as
// block copy, so preview and eventual delivery cannot select different text.
func SelectSubject(
	subjects []entity.SubjectTranslation,
	languageID int,
	langs []entity.Language,
) string {
	for _, subject := range subjects {
		if subject.LanguageID == languageID {
			return subject.Subject
		}
	}
	selected, ok := canonical.Select(
		subjects,
		func(subject entity.SubjectTranslation) int { return subject.LanguageID },
		canonical.IsDefaultFunc(langs),
	)
	if !ok {
		return ""
	}
	return selected.Subject
}
