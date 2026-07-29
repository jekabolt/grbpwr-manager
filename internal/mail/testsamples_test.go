package mail

import (
	"encoding/base64"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

// emailSample pairs a template with representative render data.
type emailSample struct {
	tn   templateName
	data interface{}
}

// emailSamples returns one representative data payload per transactional template, shared by
// the preview writer (TestRenderAllEmails) and the 19×7 locale smoke test
// (TestAllTemplatesRenderInAllLocales). Order-bearing samples carry LocalizedNames so the
// localName selector is exercised.
func emailSamples() []emailSample {
	b64 := base64.StdEncoding.EncodeToString([]byte("customer@example.com"))

	items := []dto.OrderItem{
		{
			Name:           "OVERSIZED WOOL COAT",
			LocalizedNames: map[string]string{"en": "OVERSIZED WOOL COAT", "ja": "オーバーサイズ ウールコート", "zh": "超大廓形羊毛大衣"},
			Thumbnail:      "https://picsum.photos/seed/coat/200", Size: "M", Quantity: 1, Price: "420.00",
		},
		{
			Name:           "TECHNICAL CARGO TROUSERS",
			LocalizedNames: map[string]string{"en": "TECHNICAL CARGO TROUSERS"},
			Thumbnail:      "https://picsum.photos/seed/cargo/200", Size: "L", Quantity: 2, Price: "180.00",
		},
	}

	return []emailSample{
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
		{OrderDelivered, &dto.OrderDelivered{
			Preheader:      "YOUR GRBPWR ORDER HAS BEEN DELIVERED",
			BuyerName:      "Alex",
			OrderUUID:      "ord-ab12cd34",
			CurrencySymbol: "€",
			SubtotalPrice:  "780.00",
			TotalPrice:     "780.00",
			OrderItems:     items,
			EmailB64:       b64,
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
}
