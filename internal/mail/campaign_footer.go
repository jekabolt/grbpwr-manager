package mail

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/localeutil"
)

// CampaignFooterStrings resolves the localized campaign-footer labels for a recipient's language,
// reusing the transactional i18n catalog (common.footer.* keys) so campaign emails carry the same
// footer copy as transactional ones. The recipient language_id is mapped to a locale code via the
// language set (canonicalized: cn/kr→zh/ko); an unknown language resolves to the default locale.
//
// This is intentionally NOT gated by MAILER_LOCALIZATION_ENABLED: campaigns are multi-language by
// design (the fanout captures a per-recipient language_id and the body/subject already localize
// unconditionally), so the footer must match. Missing catalog keys degrade to en (Loc.S), and an
// empty result would fall back to the template's English literal.
func (m *Mailer) CampaignFooterStrings(languageID int, langs []entity.Language) entity.EmailFooterStrings {
	code := defaultLocale
	for _, l := range langs {
		if l.Id == languageID {
			if c := localeutil.Canonical(l.Code); c != "" {
				code = c
			}
			break
		}
	}
	loc := m.catalog.Localizer(code)
	return entity.EmailFooterStrings{
		Help:      loc.S("common.footer.help", nil),
		Faq:       loc.S("common.footer.faq", nil),
		Aftersale: loc.S("common.footer.aftersale", nil),
		UnsubPre:  loc.S("common.footer.unsub_pre", nil),
		UnsubWord: loc.S("common.footer.unsub_word", nil),
	}
}
