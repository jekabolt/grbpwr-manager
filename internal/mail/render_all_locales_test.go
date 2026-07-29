package mail

import (
	"context"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/stretchr/testify/require"
)

// TestAllTemplatesRenderInAllLocales renders every transactional template in every supported
// locale (19×7) with localization ON, asserting each produces non-empty HTML with a subject
// and no leaked raw catalog keys. It is the structural safety net for translations: a template
// that references a key a locale mistranslates into a broken placeholder, or a plural/markup
// mismatch, surfaces here rather than in a customer's inbox. Content is validated separately by
// TestLocaleFilesIntegrity.
func TestAllTemplatesRenderInAllLocales(t *testing.T) {
	m, err := new(&Config{
		APIKey:              "test-api-key",
		FromEmail:           "test@example.com",
		FromName:            "Test Mailer",
		ReplyTo:             "reply@example.com",
		LocalizationEnabled: true,
	}, mocks.NewMockMail(t), langRepoStub{})
	require.NoError(t, err)

	samples := emailSamples()
	require.Len(t, samples, 19, "expected one sample per transactional template")

	for _, code := range supportedLocales {
		for _, s := range samples {
			// Route resolution to this locale via the stub's email_language.
			req, err := m.buildSendMailRequest(langEmail(code), s.tn, s.data)
			require.NoErrorf(t, err, "render %s in %s", s.tn, code)
			require.NotNilf(t, req.Html, "nil html for %s in %s", s.tn, code)
			require.NotEmptyf(t, strings.TrimSpace(*req.Html), "empty html for %s in %s", s.tn, code)
			require.NotEmptyf(t, req.Subject, "empty subject for %s in %s", s.tn, code)
			// No un-rendered template action or leaked catalog key should survive.
			require.NotContainsf(t, *req.Html, "{{", "unrendered action in %s/%s", s.tn, code)
			require.NotContainsf(t, req.Subject, "{{", "unrendered action in subject %s/%s", s.tn, code)
		}
	}
}

// langRepoStub resolves each recipient's email_language from the local part of its address
// (see langEmail), so the smoke test can force any locale through the real resolver path.
type langRepoStub struct{}

func langEmail(code string) string { return code + "@example.com" }

func (langRepoStub) GetRecipientLanguage(_ context.Context, email string) (emailLang, defaultLang string, err error) {
	return strings.SplitN(email, "@", 2)[0], "", nil
}
