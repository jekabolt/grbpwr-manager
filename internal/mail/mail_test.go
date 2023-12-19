package mail

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func skipCI(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}
}

var conf *Config = &Config{
	APIKey:         "re_A8vDWbxd_5s8CuKU93kGc4VtuiLmLJLLi",
	FromEmail:      "info@grbpwr.com",
	FromName:       "grbpwr.com",
	ReplyTo:        "info@grbpwr.com",
	WorkerInterval: time.Millisecond * 50,
}

func TestMailer(t *testing.T) {
	skipCI(t)

	mailDBMock := mocks.NewMail(t)

	m, err := New(conf, mailDBMock)
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

func TestMailerStartStop(t *testing.T) {
	// Mock the MailDB dependency
	mailDBMock := mocks.NewMail(t)

	ctx := context.Background()
	// // Setup the mock to return immediately
	// mailDBMock.EXPECT().GetAllUnsent(ctx, false).Return([]entity.SendEmailRequest{}, nil)

	// Create a new Mailer instance
	mailer, err := New(conf, mailDBMock)
	assert.NoError(t, err, "Failed to create Mailer instance")

	err = mailer.Stop()
	assert.Error(t, err)

	// Start the Mailer
	err = mailer.Start(ctx)
	assert.NoError(t, err, "Mailer should start without error")

	// Allow some time for the goroutine to run
	time.Sleep(100 * time.Millisecond) // Adjust time as needed

	// Stop the Mailer
	err = mailer.Stop()
	assert.NoError(t, err, "Mailer should stop without error")
	// Attempt to stop the Mailer again
	err = mailer.Stop()
	assert.Error(t, err)

}
func TestMailerLimit(t *testing.T) {
	// Mock the MailDB and Sender dependencies
	mailDBMock := mocks.NewMail(t)
	senderMock := mocks.NewSender(t)

	ctx := context.Background()
	ser := entity.SendEmailRequest{
		Id:      1,
		To:      "test@test.com",
		Subject: "test",
		Html:    "<html><body>test</body></html>",
		From:    conf.FromEmail,
		ReplyTo: conf.ReplyTo,
		Sent:    false,
	}

	// Setup the mock to return a list of unsent emails
	mailDBMock.EXPECT().GetAllUnsent(mock.Anything, false).Return([]entity.SendEmailRequest{ser}, nil)

	// Mock the sender to return StatusTooManyRequests
	senderMock.EXPECT().PostEmails(mock.Anything, mock.Anything, mock.Anything).Return(&http.Response{
		StatusCode: http.StatusTooManyRequests,
	}, nil)

	// Act
	// Create a new Mailer instance
	mailer, err := New(conf, mailDBMock)
	assert.NoError(t, err, "Failed to create Mailer instance")
	mailer.cli = senderMock

	// Start the Mailer
	err = mailer.Start(ctx)
	assert.NoError(t, err, "Mailer should start without error")

	time.Sleep(500 * time.Millisecond) // Adjust time as needed
	// Stop the Mailer
	err = mailer.Stop()
	assert.NoError(t, err, "Mailer should stop without error")

}

func TestMailerSuccess(t *testing.T) {
	// Mock the MailDB and Sender dependencies
	mailDBMock := mocks.NewMail(t)
	senderMock := mocks.NewSender(t)

	ctx := context.Background()
	ser := entity.SendEmailRequest{
		Id:      1,
		To:      "test@test.com",
		Subject: "test",
		Html:    "<html><body>test</body></html>",
		From:    conf.FromEmail,
		ReplyTo: conf.ReplyTo,
		Sent:    false,
	}

	// Setup the mock to return a list of unsent emails
	mailDBMock.EXPECT().GetAllUnsent(mock.Anything, false).Return([]entity.SendEmailRequest{ser}, nil)

	mailDBMock.EXPECT().UpdateSent(mock.Anything, ser.Id).Return(nil)

	// Mock the sender to return StatusTooManyRequests
	senderMock.EXPECT().PostEmails(mock.Anything, mock.Anything, mock.Anything).Return(&http.Response{
		StatusCode: http.StatusOK,
	}, nil)

	// Act
	// Create a new Mailer instance
	mailer, err := New(conf, mailDBMock)
	assert.NoError(t, err, "Failed to create Mailer instance")
	mailer.cli = senderMock

	// Start the Mailer
	err = mailer.Start(ctx)
	assert.NoError(t, err, "Mailer should start without error")

	time.Sleep(500 * time.Millisecond) // Adjust time as needed
	// Stop the Mailer
	err = mailer.Stop()
	assert.NoError(t, err, "Mailer should stop without error")

}

func TestMailerError(t *testing.T) {
	// Mock the MailDB and Sender dependencies
	mailDBMock := mocks.NewMail(t)
	senderMock := mocks.NewSender(t)

	ctx := context.Background()
	ser := entity.SendEmailRequest{
		Id:      1,
		To:      "test@test.com",
		Subject: "test",
		Html:    "<html><body>test</body></html>",
		From:    conf.FromEmail,
		ReplyTo: conf.ReplyTo,
		Sent:    false,
	}

	// Setup the mock to return a list of unsent emails
	mailDBMock.EXPECT().GetAllUnsent(mock.Anything, false).Return([]entity.SendEmailRequest{ser}, nil)

	mailDBMock.EXPECT().AddError(mock.Anything, ser.Id, mock.Anything).Return(nil)

	// Mock the sender to return StatusTooManyRequests
	senderMock.EXPECT().PostEmails(mock.Anything, mock.Anything, mock.Anything).Return(&http.Response{
		StatusCode: http.StatusBadRequest,
	}, nil)

	// Act
	// Create a new Mailer instance
	mailer, err := New(conf, mailDBMock)
	assert.NoError(t, err, "Failed to create Mailer instance")
	mailer.cli = senderMock

	// Start the Mailer
	err = mailer.Start(ctx)
	assert.NoError(t, err, "Mailer should start without error")

	time.Sleep(500 * time.Millisecond) // Adjust time as needed
	// Stop the Mailer
	err = mailer.Stop()
	assert.NoError(t, err, "Mailer should stop without error")

}
