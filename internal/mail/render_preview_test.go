package mail

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/stretchr/testify/require"
)

// TestRenderAllEmails renders every email template with representative mock data
// and writes the resulting HTML to ./preview_out/<template>.html plus an index.html.
// Run with:
//
//	go test ./internal/mail -run TestRenderAllEmails -v
//
// then open internal/mail/preview_out/index.html in a browser.
func TestRenderAllEmails(t *testing.T) {
	mailer := createTestMailer(t)

	b64 := base64.StdEncoding.EncodeToString([]byte("customer@example.com"))

	items := []dto.OrderItem{
		{Name: "OVERSIZED WOOL COAT", Thumbnail: "https://picsum.photos/seed/coat/200", Size: "M", Quantity: 1, Price: "420.00"},
		{Name: "TECHNICAL CARGO TROUSERS", Thumbnail: "https://picsum.photos/seed/cargo/200", Size: "L", Quantity: 2, Price: "180.00"},
	}

	type sample struct {
		tn   templateName
		data interface{}
	}

	samples := []sample{
		{AccountLogin, &struct {
			Preheader, EmailB64, OTPCode, MagicLinkURL string
		}{
			Preheader:    "Your GRBPWR sign-in code",
			EmailB64:     " ",
			OTPCode:      "482915",
			MagicLinkURL: "https://grbpwr.com/account/verify?token=abc123def456",
		}},
		{NewSubscriber, &struct {
			Preheader, EmailB64 string
		}{
			Preheader: "WELCOME TO GRBPWR",
			EmailB64:  b64,
		}},
		{OrderConfirmed, &dto.OrderConfirmed{
			Preheader:           "YOUR GRBPWR ORDER HAS BEEN CONFIRMED",
			BuyerName:           "Alex",
			OrderUUID:           "ord-ab12cd34",
			CurrencySymbol:      "€",
			SubtotalPrice:       "780.00",
			TotalPrice:          "790.00",
			OrderItems:          items,
			PromoExist:          true,
			PromoDiscountAmount: "40.00",
			HasFreeShipping:     false,
			ShippingPrice:       "10.00",
			EmailB64:            b64,
		}},
		{OrderShipped, &dto.OrderShipment{
			Preheader:           "YOUR GRBPWR ORDER HAS BEEN SHIPPED",
			BuyerName:           "Alex",
			OrderUUID:           "ord-ab12cd34",
			CurrencySymbol:      "€",
			SubtotalPrice:       "780.00",
			TotalPrice:          "780.00",
			OrderItems:          items,
			PromoExist:          false,
			PromoDiscountAmount: "0",
			HasFreeShipping:     true,
			ShippingPrice:       "0.00",
			EmailB64:            b64,
		}},
		{OrderCancelled, &dto.OrderCancelled{
			Preheader: "YOUR GRBPWR ORDER HAS BEEN CANCELLED",
			BuyerName: "Alex",
			OrderUUID: "ord-ab12cd34",
			EmailB64:  b64,
		}},
		{OrderRefundInitiated, &dto.OrderRefundInitiated{
			Preheader: "YOUR GRBPWR REFUND HAS BEEN INITIATED",
			BuyerName: "Alex",
			OrderUUID: "ord-ab12cd34",
			EmailB64:  b64,
		}},
		{OrderPendingReturn, &dto.OrderPendingReturn{
			Preheader: "YOUR GRBPWR RETURN HAS BEEN REQUESTED",
			BuyerName: "Alex",
			OrderUUID: "ord-ab12cd34",
			EmailB64:  b64,
		}},
		{PromoCode, &dto.PromoCodeDetails{
			Preheader:      "YOUR GRBPWR PROMO CODE",
			BuyerName:      "Alex",
			PromoCode:      "GRBPWR20",
			DiscountAmount: 20,
			EmailB64:       b64,
		}},
		{BackInStock, &dto.BackInStock{
			Preheader:   "YOUR WAITLIST ITEM IS BACK IN STOCK",
			BuyerName:   "Alex",
			ProductName: "OVERSIZED WOOL COAT",
			Brand:       "GRBPWR",
			Size:        "M",
			Thumbnail:   "https://picsum.photos/seed/coat/400",
			ProductURL:  "https://grbpwr.com/product/oversized-wool-coat",
			EmailB64:    b64,
		}},
		{TierUpgrade, &dto.TierChangeEmail{
			Preheader:       "YOUR GRBPWR TIER HAS CHANGED",
			EmailB64:        " ",
			Name:            "Alex",
			TierDisplay:     "grbpwr++",
			PrevTierDisplay: "grbpwr+",
			SpendEUR:        "2,480",
			ThresholdEUR:    "2,000",
			NextReview:      "2026-12-31",
			IsBackfill:      false,
		}},
		{TierDowngrade, &dto.TierChangeEmail{
			Preheader:       "YOUR GRBPWR TIER HAS CHANGED",
			EmailB64:        " ",
			Name:            "Alex",
			TierDisplay:     "grbpwr+",
			PrevTierDisplay: "grbpwr++",
			SpendEUR:        "1,240",
			ThresholdEUR:    "2,000",
			NextReview:      "2026-12-31",
		}},
		{DowngradeReminder, &dto.TierChangeEmail{
			Preheader:       "KEEP YOUR GRBPWR TIER",
			EmailB64:        " ",
			Name:            "Alex",
			TierDisplay:     "grbpwr++",
			PrevTierDisplay: "grbpwr++",
			SpendEUR:        "1,640",
			ThresholdEUR:    "2,000",
			NextReview:      "2026-06-30",
		}},
		{TierRollbackAfterRefund, &dto.TierChangeEmail{
			Preheader:       "YOUR GRBPWR TIER WAS ADJUSTED",
			EmailB64:        " ",
			Name:            "Alex",
			TierDisplay:     "grbpwr+",
			PrevTierDisplay: "grbpwr++",
			SpendEUR:        "1,180",
			ThresholdEUR:    "2,000",
		}},
		{FirstPurchaseThanks, &dto.TierChangeEmail{
			Preheader:   "THANK YOU FROM GRBPWR",
			EmailB64:    " ",
			Name:        "Alex",
			TierDisplay: "grbpwr",
			SpendEUR:    "420",
		}},
		{UnsubscribeConfirmation, &dto.UnsubscribeConfirmationEmail{
			Preheader: "YOU'VE BEEN UNSUBSCRIBED",
			EmailB64:  " ",
			Name:      "Alex",
		}},
		{BirthdayGift, &dto.BirthdayEmail{
			Preheader: "A GIFT FROM GRBPWR",
			EmailB64:  b64,
			Name:      "Alex",
			PromoCode: "HBD2026",
		}},
		{EventInvite, &dto.MemberCustomEmail{
			Preheader: "YOU'RE INVITED",
			EmailB64:  b64,
			Name:      "Alex",
			Heading:   "GRBPWR SS26 PRESENTATION",
			Body:      "Join us for an intimate preview of the SS26 collection.\n\nDoors open at 19:00. Drinks provided.",
			CTALabel:  "RSVP",
			CTAURL:    "https://grbpwr.com/events/ss26-rsvp",
		}},
		{HackerInvite, &dto.HackerInviteEmail{
			Preheader: "YOUR GRBPWR HACKER INVITE",
			EmailB64:  " ",
			InviteURL: "https://grbpwr.com/hacker/redeem?token=xyz789",
			ExpiresAt: "2026-06-07",
		}},
	}

	outDir := "preview_out"
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	var links []string
	for _, s := range samples {
		req, err := mailer.buildSendMailRequest("customer@example.com", s.tn, s.data)
		require.NoError(t, err, "render %s", s.tn)
		require.NotNil(t, req.Html)

		fname := strings.TrimSuffix(string(s.tn), ".gohtml") + ".html"
		require.NoError(t, os.WriteFile(filepath.Join(outDir, fname), []byte(*req.Html), 0o644))
		links = append(links, "<li><a href=\""+fname+"\">"+req.Subject+"</a> <code>"+string(s.tn)+"</code></li>")
		t.Logf("rendered %-32s -> %s/%s", s.tn, outDir, fname)
	}

	index := "<!doctype html><meta charset=\"utf-8\"><title>GRBPWR email previews</title>" +
		"<body style=\"font-family:monospace;padding:24px;\"><h1>GRBPWR email previews</h1><ul>" +
		strings.Join(links, "\n") + "</ul></body>"
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "index.html"), []byte(index), 0o644))
	t.Logf("open %s/index.html", outDir)
}
