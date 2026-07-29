package mail

import (
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/stretchr/testify/require"
)

// newLocalizedTestMailer builds a mailer with localization ON and no langRepo, so
// resolveLocale falls straight through to the data's locale hint (guest-order path).
func newLocalizedTestMailer(t *testing.T) *Mailer {
	t.Helper()
	m, err := new(&Config{
		APIKey:              "test-api-key",
		FromEmail:           "test@example.com",
		FromName:            "Test Mailer",
		ReplyTo:             "reply@example.com",
		LocalizationEnabled: true,
	}, mocks.NewMockMail(t), nil)
	require.NoError(t, err)
	return m
}

// TestOrderEmailLocalizesProductName renders a real order-confirmation email end-to-end
// and asserts the order line shows the product name in the recipient's resolved locale,
// falling back to the default-language name when that locale has no translation.
func TestOrderEmailLocalizesProductName(t *testing.T) {
	m := newLocalizedTestMailer(t)

	// Item 1 has a ja translation; item 2 does not (must fall back to its default Name).
	items := []dto.OrderItem{
		{
			Name:           "GRBPWR WOOL COAT",
			LocalizedNames: map[string]string{"en": "GRBPWR WOOL COAT", "ja": "GRBPWR ウールコート"},
			Size:           "M", Quantity: 1, Price: "420.00",
		},
		{
			Name:           "GRBPWR CARGO TROUSERS",
			LocalizedNames: map[string]string{"en": "GRBPWR CARGO TROUSERS"},
			Size:           "L", Quantity: 1, Price: "180.00",
		},
	}
	data := &dto.OrderConfirmed{
		Locale:         "ja", // purchase-time hint; with no account this is the resolved locale
		BuyerName:      "Alex",
		OrderUUID:      "ord-ab12cd34",
		CurrencySymbol: "€",
		SubtotalPrice:  "600.00",
		TotalPrice:     "600.00",
		OrderItems:     items,
		EmailB64:       "Y3VzdG9tZXJAZXhhbXBsZS5jb20=",
	}

	req, err := m.buildSendMailRequest("customer@example.com", OrderConfirmed, data)
	require.NoError(t, err)
	require.NotNil(t, req.Html)
	html := *req.Html

	// ja render: the translated name appears, the English one for that line does not.
	require.Contains(t, html, "GRBPWR ウールコート", "ja product name must render")
	require.NotContains(t, html, "GRBPWR WOOL COAT", "English name for the translated line must not appear")
	// The untranslated line falls back to its default-language Name.
	require.Contains(t, html, "GRBPWR CARGO TROUSERS", "untranslated line must fall back to default name")

	// Subject still localizes/interpolates the order id (sanity check the render is ja-bound).
	require.NotEmpty(t, req.Subject)
	require.True(t, strings.Contains(req.Subject, "ORD-AB12CD34"), "subject interpolates uppercased order id, got %q", req.Subject)
}
