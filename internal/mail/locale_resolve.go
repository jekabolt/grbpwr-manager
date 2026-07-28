package mail

// resolveLocale picks the recipient's email language code.
//
// Phase Ф1 is flag-gated: when localization is disabled (the default, and prod until
// sign-off) it returns the default locale, so every email renders in English —
// behavior identical to before the feature. Later phases extend this to the full
// precedence: explicit storefront_account.email_language → per-event hint
// (order/subscriber/request locale) → storefront_account.default_language → default.
func (m *Mailer) resolveLocale(email string) string {
	if !m.c.LocalizationEnabled {
		return defaultLocale
	}
	// TODO(localization): consult the account store for an explicit email_language,
	// then the event hint, then default_language. Until that is wired, default.
	_ = email
	return defaultLocale
}
