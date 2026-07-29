package mail

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/localeutil"
)

// resolveLocale picks the recipient's email language code. Precedence:
//
//  1. storefront_account.email_language   (explicit, sticky — wins)
//  2. hint                                (event locale: order/subscriber/request)
//  3. storefront_account.default_language (last-known browsing locale)
//  4. defaultLocale ("en")
//
// When localization is disabled (the default, and prod until sign-off) it short-circuits
// to the default locale, so output stays byte-identical to the pre-feature behavior. Every
// candidate is clamped to the supported locale set; an unrecognized one falls through
// rather than masquerading as "en", so a lower-priority but valid signal still wins.
//
// TODO: thread the caller's context once buildSendMailRequest carries one; the per-send
// account lookup currently uses context.Background().
func (m *Mailer) resolveLocale(ctx context.Context, email, hint string) string {
	if !m.c.LocalizationEnabled {
		return defaultLocale
	}

	var emailLang, defaultLang string
	if m.langRepo != nil && email != "" {
		if el, dl, err := m.langRepo.GetRecipientLanguage(ctx, email); err == nil {
			emailLang, defaultLang = el, dl
		}
		// On error: fall through to hint/default — a failed lookup must never block a send.
	}

	if v := localeutil.Canonical(emailLang); v != "" {
		return v
	}
	if v := localeutil.Canonical(hint); v != "" {
		return v
	}
	if v := localeutil.Canonical(defaultLang); v != "" {
		return v
	}
	return defaultLocale
}
