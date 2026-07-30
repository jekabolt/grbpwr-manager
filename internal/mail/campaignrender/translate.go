package campaignrender

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/canonical"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// pickTranslation resolves the copy for one language. The exact-language row wins, but every
// field it leaves empty falls back to the canonical (default-language) row: locales are routinely
// only partly filled — hand-authored one field at a time, or left short by an auto-translate run
// that skipped/failed a string — and returning such a row wholesale rendered blocks with missing
// copy in the localized email while the default language showed the full text.
func pickTranslation(
	translations []entity.EmailBlockTranslation,
	languageID int,
	langs []entity.Language,
) entity.EmailBlockTranslation {
	fallback, hasFallback := canonical.Select(
		translations,
		func(translation entity.EmailBlockTranslation) int { return translation.LanguageID },
		canonical.IsDefaultFunc(langs),
	)
	for _, translation := range translations {
		if translation.LanguageID == languageID {
			if hasFallback && fallback.LanguageID != languageID {
				fillEmptyTranslationFields(&translation, fallback)
			}
			return translation
		}
	}
	return fallback
}

// fillEmptyTranslationFields copies every field the selected translation left empty from the
// canonical one, so a partial localization degrades field-by-field instead of block-by-block.
func fillEmptyTranslationFields(dst *entity.EmailBlockTranslation, src entity.EmailBlockTranslation) {
	for _, field := range []struct {
		target *string
		source string
	}{
		{&dst.Heading, src.Heading},
		{&dst.Subheading, src.Subheading},
		{&dst.Body, src.Body},
		{&dst.Caption, src.Caption},
		{&dst.CTALabel, src.CTALabel},
		{&dst.CTAURL, src.CTAURL},
		{&dst.AltText, src.AltText},
		{&dst.Preheader, src.Preheader},
	} {
		if strings.TrimSpace(*field.target) == "" {
			*field.target = field.source
		}
	}
	if len(dst.Links) == 0 {
		dst.Links = src.Links
	}
}

// SelectSubject uses the same requested/default/smallest-id language policy as
// block copy, so preview and eventual delivery cannot select different text. An
// empty subject row counts as absent, so a locale added without a subject falls
// back to the default language instead of sending with no subject at all.
func SelectSubject(
	subjects []entity.SubjectTranslation,
	languageID int,
	langs []entity.Language,
) string {
	for _, subject := range subjects {
		if subject.LanguageID == languageID {
			if strings.TrimSpace(subject.Subject) != "" {
				return subject.Subject
			}
			break
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
