// Package localeutil normalizes storefront locale codes to the canonical email locale
// set (the 7 storefront locales). It is shared by the request boundary — where the
// captured purchase/signup/account locale is sanitized before persistence — and by the
// mailer's language resolver, so both agree on what a valid locale is.
package localeutil

import "strings"

// Supported is the canonical email locale set = the 7 storefront locales, in the same
// order as the storefront's next-intl routing config. en is the default/fallback.
var Supported = []string{"en", "fr", "de", "it", "ja", "zh", "ko"}

// Default is the fallback locale.
const Default = "en"

// Canonical lowercases the code, strips any region subtag (en-US -> en), maps the
// admin-side cn/kr codes to their ISO zh/ko equivalents, and returns the code when it is
// supported, else "".
//
// It returns "" (not Default) for unknown/empty input so a caller persisting a captured
// locale stores NULL rather than a wrong "en" — the mailer applies its own default at
// render time. Use IsSupported / or compare against "" to decide whether a code was
// recognized.
func Canonical(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	switch c {
	case "cn":
		c = "zh"
	case "kr":
		c = "ko"
	}
	for _, s := range Supported {
		if c == s {
			return c
		}
	}
	return ""
}

// CanonicalOrDefault is Canonical but returns Default instead of "" for unknown input.
// Use it where a locale is always required (e.g. selecting a render locale).
func CanonicalOrDefault(code string) string {
	if c := Canonical(code); c != "" {
		return c
	}
	return Default
}

// IsSupported reports whether code maps to a supported locale.
func IsSupported(code string) bool { return Canonical(code) != "" }
