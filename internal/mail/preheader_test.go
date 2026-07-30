package mail

import (
	"html"
	"regexp"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/stretchr/testify/require"
)

// preheaderKeyRe extracts the catalog key each template uses for its inbox preview line,
// e.g. `"preheader" (t "order.confirmed.preheader")`.
var preheaderKeyRe = regexp.MustCompile(`"preheader"\s+(?:\(or\s+\.Preheader\s+)?\(t "([a-z0-9.]+)"\)`)

// TestTemplatePreheadersAreLocalized asserts every transactional template renders its
// preview line from the catalog and that the key exists in en.json. Without this, a
// missing key would silently render as the raw key text in the recipient's inbox — the
// 19x7 render smoke test cannot catch it (it only looks for leaked "{{" actions).
func TestTemplatePreheadersAreLocalized(t *testing.T) {
	en := loadLocaleStrings(t, defaultLocale)

	entries, err := templatesFS.ReadDir("templates")
	require.NoError(t, err)

	seen := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := templatesFS.ReadFile("templates/" + e.Name())
		require.NoError(t, err)
		body := string(b)

		m := preheaderKeyRe.FindStringSubmatch(body)
		require.NotNilf(t, m, "%s does not render its preheader from the catalog (expected `\"preheader\" (t \"…\")`)", e.Name())
		key := m[1]
		require.Truef(t, strings.HasSuffix(key, ".preheader"), "%s uses %q as its preheader key", e.Name(), key)
		require.NotEmptyf(t, en[key], "%s references preheader key %q which is missing from en.json", e.Name(), key)
		seen++
	}
	require.Equal(t, 19, seen, "expected one preheader per transactional template")
}

// TestPreheaderFollowsRecipientLocale renders every template for a ja recipient with
// localization ON and asserts the hidden preview line carries the ja catalog string, not the
// EN one — the inbox preview must not stay English next to a localized subject and body.
// With localization OFF the EN string is still what renders (pre-feature output).
func TestPreheaderFollowsRecipientLocale(t *testing.T) {
	en := loadLocaleStrings(t, defaultLocale)
	ja := loadLocaleStrings(t, "ja")

	localized, err := new(&Config{
		APIKey: "test-api-key", FromEmail: "test@example.com", FromName: "Test Mailer",
		LocalizationEnabled: true,
	}, mocks.NewMockMail(t), langRepoStub{})
	require.NoError(t, err)
	plain := createTestMailer(t)

	for _, s := range emailSamples() {
		key := strings.TrimSuffix(string(s.tn), ".gohtml")
		if s.tn == EventInvite {
			continue // preview line is the admin-authored heading, not a catalog string
		}

		req, err := localized.buildSendMailRequest(langEmail("ja"), s.tn, s.data)
		require.NoErrorf(t, err, "render %s", s.tn)
		enVal, jaVal := preheaderOf(t, en, s.tn), preheaderOf(t, ja, s.tn)
		require.Containsf(t, *req.Html, jaVal, "%s: ja preheader missing", key)
		if jaVal != enVal {
			require.NotContainsf(t, *req.Html, enVal, "%s: EN preheader leaked into the ja render", key)
		}

		req, err = plain.buildSendMailRequest("customer@example.com", s.tn, s.data)
		require.NoErrorf(t, err, "render %s", s.tn)
		require.Containsf(t, *req.Html, enVal, "%s: EN preheader missing with localization off", key)
	}
}

// preheaderOf returns the catalog preheader the template tn renders, from the given locale
// map, HTML-escaped the way html/template writes it into the preview div.
func preheaderOf(t *testing.T, loc map[string]string, tn templateName) string {
	t.Helper()
	b, err := templatesFS.ReadFile("templates/" + string(tn))
	require.NoError(t, err)
	m := preheaderKeyRe.FindStringSubmatch(string(b))
	require.NotNilf(t, m, "%s renders no catalog preheader", tn)
	v := loc[m[1]]
	require.NotEmptyf(t, v, "missing %s", m[1])
	return html.EscapeString(v)
}
