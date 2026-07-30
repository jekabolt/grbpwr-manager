package mail

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/localeutil"
)

// recipientLanguageTimeout bounds the per-send account language lookup. The lookup is a
// nice-to-have (a failure degrades to the hint/default locale), so it must never hold an
// inline send — e.g. the order confirmation on the payment path — behind a stalled query
// or an exhausted connection pool.
const recipientLanguageTimeout = 1500 * time.Millisecond

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
// The lookup is always bounded by recipientLanguageTimeout, so a stalled query degrades to
// the hint/default locale instead of blocking the send indefinitely.
//
// TODO: thread the caller's context once buildSendMailRequest carries one; the per-send
// account lookup currently starts from context.Background(), so the caller's cancellation
// is not observed (its deadline is bounded by recipientLanguageTimeout regardless).
func (m *Mailer) resolveLocale(ctx context.Context, email, hint string) string {
	if !m.c.LocalizationEnabled {
		return defaultLocale
	}

	var emailLang, defaultLang string
	if m.langRepo != nil && email != "" {
		lookupCtx, cancel := context.WithTimeout(ctx, recipientLanguageTimeout)
		el, dl, err := m.langRepo.GetRecipientLanguage(lookupCtx, email)
		cancel()
		if err == nil {
			emailLang, defaultLang = el, dl
		}
		// On error (including the timeout): fall through to hint/default — a failed or slow
		// lookup must never block a send.
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
