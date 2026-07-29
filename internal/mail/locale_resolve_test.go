package mail

import (
	"context"
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/stretchr/testify/mock"
)

func TestResolveLocale(t *testing.T) {
	ctx := context.Background()
	const email = "customer@example.com"

	cases := []struct {
		name                         string
		enabled                      bool
		emailLang, defaultLang, hint string
		lookupErr                    error
		want                         string
	}{
		{"flag off short-circuits to en", false, "fr", "it", "de", nil, "en"},
		{"explicit email_language wins", true, "fr", "it", "de", nil, "fr"},
		{"hint used when no email_language", true, "", "it", "de", nil, "de"},
		{"default_language when no email_language/hint", true, "", "it", "", nil, "it"},
		{"email_language cn normalizes to zh", true, "cn", "", "", nil, "zh"},
		{"kr default_language normalizes to ko", true, "", "kr", "", nil, "ko"},
		{"junk hint ignored, falls to default_language", true, "", "ja", "xx", nil, "ja"},
		{"all empty falls to en", true, "", "", "", nil, "en"},
		{"lookup error falls to hint", true, "fr", "it", "de", errors.New("db down"), "de"},
		{"lookup error, no hint, falls to en", true, "fr", "it", "", errors.New("db down"), "en"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lr := mocks.NewMockRecipientLanguage(t)
			// .Maybe() because the flag-off case returns before consulting the repo.
			lr.On("GetRecipientLanguage", mock.Anything, email).
				Return(tc.emailLang, tc.defaultLang, tc.lookupErr).Maybe()

			m := &Mailer{c: &Config{LocalizationEnabled: tc.enabled}, langRepo: lr}
			if got := m.resolveLocale(ctx, email, tc.hint); got != tc.want {
				t.Errorf("resolveLocale = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveLocaleNilRepo(t *testing.T) {
	// A nil language repo (e.g. bare test mailer) must not panic; it falls through to en.
	m := &Mailer{c: &Config{LocalizationEnabled: true}}
	if got := m.resolveLocale(context.Background(), "x@y.io", ""); got != "en" {
		t.Errorf("resolveLocale(nil repo) = %q, want en", got)
	}
	// With a valid hint and nil repo, the hint wins.
	if got := m.resolveLocale(context.Background(), "x@y.io", "ja"); got != "ja" {
		t.Errorf("resolveLocale(nil repo, hint ja) = %q, want ja", got)
	}
}
