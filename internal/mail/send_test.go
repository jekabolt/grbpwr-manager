package mail

import (
	"os"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

func skipCI(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}
}

func TestMailer(t *testing.T) {
	skipCI(t)

	c := &Config{
		APIKey:    "",
		FromEmail: "info@grbpwr.com",
		FromName:  "info grbpwr",
	}

	m, err := New(c)
	assert.NoError(t, err)

	to := "jekabolt@yahoo.com"

	err = m.SendNewSubscriber(to)
	assert.NoError(t, err)

	err = m.SendOrderConfirmation(to, &dto.OrderConfirmed{
		OrderID:         "123",
		Name:            "jekabolt",
		OrderDate:       "2021-09-01",
		TotalAmount:     100,
		PaymentMethod:   string(entity.Card),
		PaymentCurrency: "EUR",
	})
	assert.NoError(t, err)

	err = m.SendOrderCancellation(to, &dto.OrderCancelled{
		OrderID:          "123",
		Name:             "jekabolt",
		CancellationDate: "2021-09-01",
		RefundAmount:     100,
		PaymentMethod:    string(entity.Eth),
		PaymentCurrency:  "ETH",
	})
	assert.NoError(t, err)

	err = m.SendOrderShipped(to, &dto.OrderShipment{
		OrderID:        "123",
		Name:           "jekabolt",
		ShippingDate:   "2021-09-01",
		TotalAmount:    100,
		TrackingNumber: "123456789",
		TrackingURL:    "https://www.tracking.grbpwr.com/",
	})
	assert.NoError(t, err)

	err = m.SendPromoCode(to, &dto.PromoCodeDetails{
		PromoCode:       "test",
		HasFreeShipping: true,
		DiscountAmount:  100,
		ExpirationDate:  "2021-09-01",
	})
	assert.NoError(t, err)

}
