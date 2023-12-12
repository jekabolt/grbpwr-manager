package mail

import (
	"context"
	"os"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
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
		APIKey:    "re_A8vDWbxd_5s8CuKU93kGc4VtuiLmLJLLi",
		FromEmail: "info@grbpwr.com",
		FromName:  "grbpwr.com",
	}

	mailDBMock := mocks.NewMail(t)

	m, err := New(c, mailDBMock)
	ctx := context.Background()
	assert.NoError(t, err)

	to := "jekabolt@yahoo.com"

	_, err = m.SendNewSubscriber(ctx, to)
	assert.NoError(t, err)

	// _, err = m.SendOrderConfirmation(ctx, to, &dto.OrderConfirmed{
	// 	OrderID:         "123",
	// 	Name:            "jekabolt",
	// 	OrderDate:       "2021-09-01",
	// 	TotalAmount:     100,
	// 	PaymentMethod:   string(entity.Card),
	// 	PaymentCurrency: "EUR",
	// })
	// assert.NoError(t, err)

	// _, err = m.SendOrderCancellation(ctx, to, &dto.OrderCancelled{
	// 	OrderID:          "123",
	// 	Name:             "jekabolt",
	// 	CancellationDate: "2021-09-01",
	// 	RefundAmount:     100,
	// 	PaymentMethod:    string(entity.Eth),
	// 	PaymentCurrency:  "ETH",
	// })
	// assert.NoError(t, err)

	// _, err = m.SendOrderShipped(ctx, to, &dto.OrderShipment{
	// 	OrderID:        "123",
	// 	Name:           "jekabolt",
	// 	ShippingDate:   "2021-09-01",
	// 	TotalAmount:    100,
	// 	TrackingNumber: "123456789",
	// 	TrackingURL:    "https://www.tracking.grbpwr.com/",
	// })
	// assert.NoError(t, err)

	// _, err = m.SendPromoCode(ctx, to, &dto.PromoCodeDetails{
	// 	PromoCode:       "test",
	// 	HasFreeShipping: true,
	// 	DiscountAmount:  100,
	// 	ExpirationDate:  "2021-09-01",
	// })
	// assert.NoError(t, err)

}
