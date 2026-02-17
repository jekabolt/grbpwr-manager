package mail

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func createTestMailer(t *testing.T) *Mailer {
	config := &Config{
		APIKey:    "test-api-key",
		FromEmail: "test@example.com",
		FromName:  "Test Mailer",
		ReplyTo:   "reply@example.com",
	}

	mailDBMock := mocks.NewMockMail(t)
	mailer, err := new(config, mailDBMock)
	require.NoError(t, err)

	return mailer
}

func TestNew(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		config := &Config{
			APIKey:    "test-api-key",
			FromEmail: "test@example.com",
			FromName:  "Test Mailer",
			ReplyTo:   "reply@example.com",
		}

		mailDBMock := mocks.NewMockMail(t)
		mailer, err := New(config, mailDBMock)

		assert.NoError(t, err)
		assert.NotNil(t, mailer)
	})

	t.Run("Empty API Key", func(t *testing.T) {
		config := &Config{
			APIKey:    "",
			FromEmail: "test@example.com",
			FromName:  "Test Mailer",
		}

		mailDBMock := mocks.NewMockMail(t)
		mailer, err := New(config, mailDBMock)

		assert.Error(t, err)
		assert.Nil(t, mailer)
		assert.Contains(t, err.Error(), "incomplete config")
	})

	t.Run("Empty From Email", func(t *testing.T) {
		config := &Config{
			APIKey:    "test-api-key",
			FromEmail: "",
			FromName:  "Test Mailer",
		}

		mailDBMock := mocks.NewMockMail(t)
		mailer, err := New(config, mailDBMock)

		assert.Error(t, err)
		assert.Nil(t, mailer)
	})
}

func TestParseTemplates(t *testing.T) {
	mailer := createTestMailer(t)

	// Verify all main templates are loaded
	expectedTemplates := []templateName{
		"new_subscriber.gohtml",
		"order_cancelled.gohtml",
		"order_confirmed.gohtml",
		"order_shipped.gohtml",
		"promo_code.gohtml",
		"refund_initiated.gohtml",
	}

	for _, tmplName := range expectedTemplates {
		tmpl, exists := mailer.templates[tmplName]
		assert.True(t, exists, "Template %s should be loaded", tmplName)
		assert.NotNil(t, tmpl, "Template %s should not be nil", tmplName)
	}

	// Verify partials are not in main templates map
	partialTemplates := []string{
		"email_header.gohtml",
		"email_footer.gohtml",
		"order_items_list.gohtml",
		"order_totals.gohtml",
		"divider.gohtml",
		"spacer.gohtml",
		"signature.gohtml",
		"order_track_link.gohtml",
	}

	for _, partial := range partialTemplates {
		_, exists := mailer.templates[templateName(partial)]
		assert.False(t, exists, "Partial %s should not be in main templates map", partial)
	}
}

func TestSendNewSubscriber(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)
	mailDBMock.On("UpdateSent", ctx, 1).Return(nil)

	// Mock the sender
	senderMock := mocks.NewMockSender(t)
	mailer.cli = senderMock
	senderMock.On("PostEmails", ctx, mock.Anything).Return(nil, nil)

	err := mailer.SendNewSubscriber(ctx, repMock, "test@example.com")

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)
}

func TestQueueNewSubscriber(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	// Queue method should only insert, not send or update
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)

	err := mailer.QueueNewSubscriber(ctx, repMock, "test@example.com")

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)
	// Verify that UpdateSent was NOT called (email is queued, not sent immediately)
}

func TestSendOrderConfirmation(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)
	mailDBMock.On("UpdateSent", ctx, 1).Return(nil)

	senderMock := mocks.NewMockSender(t)
	mailer.cli = senderMock
	senderMock.On("PostEmails", ctx, mock.Anything).Return(nil, nil)

	orderDetails := &dto.OrderConfirmed{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN CONFIRMED",
		OrderUUID:           "test-uuid-123",
		TotalPrice:          "100.00",
		OrderItems:          []dto.OrderItem{},
		PromoExist:          false,
		PromoDiscountAmount: "0",
		HasFreeShipping:     false,
		ShippingPrice:       "10.00",
		EmailB64:            base64.StdEncoding.EncodeToString([]byte("test@example.com")),
	}

	err := mailer.SendOrderConfirmation(ctx, repMock, "test@example.com", orderDetails)

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)

	t.Run("Empty OrderUUID", func(t *testing.T) {
		invalidDetails := &dto.OrderConfirmed{
			OrderUUID: "",
		}

		err := mailer.SendOrderConfirmation(ctx, repMock, "test@example.com", invalidDetails)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "incomplete order details")
	})
}

func TestQueueOrderConfirmation(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)

	orderDetails := &dto.OrderConfirmed{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN CONFIRMED",
		OrderUUID:           "test-uuid-123",
		TotalPrice:          "100.00",
		OrderItems:          []dto.OrderItem{},
		PromoExist:          false,
		PromoDiscountAmount: "0",
		HasFreeShipping:     false,
		ShippingPrice:       "10.00",
		EmailB64:            base64.StdEncoding.EncodeToString([]byte("test@example.com")),
	}

	err := mailer.QueueOrderConfirmation(ctx, repMock, "test@example.com", orderDetails)

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)
	// Verify that UpdateSent was NOT called (email is queued, not sent immediately)

	t.Run("Empty OrderUUID", func(t *testing.T) {
		invalidDetails := &dto.OrderConfirmed{
			OrderUUID: "",
		}

		err := mailer.QueueOrderConfirmation(ctx, repMock, "test@example.com", invalidDetails)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "incomplete order details")
	})
}

func TestSendOrderCancellation(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)
	mailDBMock.On("UpdateSent", ctx, 1).Return(nil)

	senderMock := mocks.NewMockSender(t)
	mailer.cli = senderMock
	senderMock.On("PostEmails", ctx, mock.Anything).Return(nil, nil)

	cancelDetails := &dto.OrderCancelled{
		Preheader: "YOUR GRBPWR ORDER HAS BEEN CANCELLED",
		OrderUUID: "test-uuid-cancel",
		EmailB64:  base64.StdEncoding.EncodeToString([]byte("test@example.com")),
	}

	err := mailer.SendOrderCancellation(ctx, repMock, "test@example.com", cancelDetails)

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)
}

func TestSendOrderShipped(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)
	mailDBMock.On("UpdateSent", ctx, 1).Return(nil)

	senderMock := mocks.NewMockSender(t)
	mailer.cli = senderMock
	senderMock.On("PostEmails", ctx, mock.Anything).Return(nil, nil)

	shipmentDetails := &dto.OrderShipment{
		Preheader:           "YOUR GRBPWR ORDER HAS BEEN SHIPPED",
		OrderUUID:           "test-uuid-shipped",
		EmailB64:            base64.StdEncoding.EncodeToString([]byte("test@example.com")),
		OrderItems:          []dto.OrderItem{},
		TotalPrice:          "150.00",
		PromoExist:          true,
		PromoDiscountAmount: "10",
		HasFreeShipping:     true,
		ShippingPrice:       "0.00",
	}

	err := mailer.SendOrderShipped(ctx, repMock, "test@example.com", shipmentDetails)

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)

	t.Run("Empty OrderUUID", func(t *testing.T) {
		invalidDetails := &dto.OrderShipment{
			OrderUUID: "",
		}

		err := mailer.SendOrderShipped(ctx, repMock, "test@example.com", invalidDetails)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "incomplete shipment details")
	})
}

func TestSendRefundInitiated(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)
	mailDBMock.On("UpdateSent", ctx, 1).Return(nil)

	senderMock := mocks.NewMockSender(t)
	mailer.cli = senderMock
	senderMock.On("PostEmails", ctx, mock.Anything).Return(nil, nil)

	refundDetails := &dto.OrderRefundInitiated{
		Preheader: "YOUR GRBPWR REFUND HAS BEEN INITIATED",
		OrderUUID: "test-uuid-refund",
		EmailB64:  base64.StdEncoding.EncodeToString([]byte("test@example.com")),
	}

	err := mailer.SendRefundInitiated(ctx, repMock, "test@example.com", refundDetails)

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)

	t.Run("Empty OrderUUID", func(t *testing.T) {
		invalidDetails := &dto.OrderRefundInitiated{
			OrderUUID: "",
		}

		err := mailer.SendRefundInitiated(ctx, repMock, "test@example.com", invalidDetails)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "incomplete refund details")
	})
}

func TestSendPromoCode(t *testing.T) {
	ctx := context.Background()
	mailer := createTestMailer(t)

	repMock := mocks.NewMockRepository(t)
	mailDBMock := mocks.NewMockMail(t)

	repMock.On("Mail").Return(mailDBMock)
	mailDBMock.On("AddMail", ctx, mock.Anything).Return(1, nil)
	mailDBMock.On("UpdateSent", ctx, 1).Return(nil)

	senderMock := mocks.NewMockSender(t)
	mailer.cli = senderMock
	senderMock.On("PostEmails", ctx, mock.Anything).Return(nil, nil)

	promoDetails := &dto.PromoCodeDetails{
		Preheader:      "YOUR GRBPWR PROMO CODE",
		PromoCode:      "TESTPROMO10",
		DiscountAmount: 10,
	}

	err := mailer.SendPromoCode(ctx, repMock, "test@example.com", promoDetails)

	assert.NoError(t, err)
	repMock.AssertExpectations(t)
	mailDBMock.AssertExpectations(t)

	t.Run("Empty PromoCode", func(t *testing.T) {
		invalidDetails := &dto.PromoCodeDetails{
			PromoCode: "",
		}

		err := mailer.SendPromoCode(ctx, repMock, "test@example.com", invalidDetails)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "incomplete promo code details")
	})
}

func TestBuildSendMailRequest(t *testing.T) {
	mailer := createTestMailer(t)

	t.Run("Valid Template", func(t *testing.T) {
		data := struct {
			Preheader string
		}{
			Preheader: "WELCOME TO GRBPWR",
		}
		req, err := mailer.buildSendMailRequest("test@example.com", NewSubscriber, data)

		assert.NoError(t, err)
		assert.NotNil(t, req)
		assert.Equal(t, []string{"test@example.com"}, req.To)
		assert.Equal(t, "Welcome to GRBPWR", req.Subject)
		assert.NotNil(t, req.Html)
		assert.True(t, strings.Contains(*req.Html, "WELCOME"))
	})

	t.Run("Invalid Template", func(t *testing.T) {
		data := struct {
			Preheader string
		}{
			Preheader: "TEST",
		}
		req, err := mailer.buildSendMailRequest("test@example.com", "nonexistent.gohtml", data)

		assert.Error(t, err)
		assert.Nil(t, req)
		assert.Contains(t, err.Error(), "template not found")
	})

	t.Run("Order Confirmation Template", func(t *testing.T) {
		data := &dto.OrderConfirmed{
			Preheader:           "YOUR GRBPWR ORDER HAS BEEN CONFIRMED",
			OrderUUID:           "test-123",
			TotalPrice:          "100.00",
			OrderItems:          []dto.OrderItem{},
			PromoExist:          false,
			PromoDiscountAmount: "0",
			HasFreeShipping:     false,
			ShippingPrice:       "10.00",
			EmailB64:            base64.StdEncoding.EncodeToString([]byte("test@example.com")),
		}

		req, err := mailer.buildSendMailRequest("test@example.com", OrderConfirmed, data)

		assert.NoError(t, err)
		assert.NotNil(t, req)
		assert.Equal(t, "Your order has been confirmed", req.Subject)
		assert.Contains(t, *req.Html, "test-123")
		assert.Contains(t, *req.Html, "HAS BEEN PLACED")
	})

	t.Run("Order Cancellation Template", func(t *testing.T) {
		data := &dto.OrderCancelled{
			Preheader: "YOUR GRBPWR ORDER HAS BEEN CANCELLED",
			OrderUUID: "cancel-456",
			EmailB64:  base64.StdEncoding.EncodeToString([]byte("test@example.com")),
		}

		req, err := mailer.buildSendMailRequest("test@example.com", OrderCancelled, data)

		assert.NoError(t, err)
		assert.NotNil(t, req)
		assert.Equal(t, "Your order has been cancelled", req.Subject)
		assert.Contains(t, *req.Html, "cancel-456")
		assert.Contains(t, *req.Html, "HAS BEEN CANCELLED")
	})

	t.Run("Refund Initiated Template", func(t *testing.T) {
		data := &dto.OrderRefundInitiated{
			Preheader: "YOUR GRBPWR REFUND HAS BEEN INITIATED",
			OrderUUID: "refund-789",
			EmailB64:  base64.StdEncoding.EncodeToString([]byte("test@example.com")),
		}

		req, err := mailer.buildSendMailRequest("test@example.com", OrderRefundInitiated, data)

		assert.NoError(t, err)
		assert.NotNil(t, req)
		assert.Equal(t, "Your refund has been initiated", req.Subject)
		assert.Contains(t, *req.Html, "refund-789")
		assert.Contains(t, *req.Html, "REFUND HAS BEEN INITIATED")
	})

	t.Run("Promo Code Template", func(t *testing.T) {
		data := &dto.PromoCodeDetails{
			Preheader:      "YOUR GRBPWR PROMO CODE",
			PromoCode:      "SAVE20",
			DiscountAmount: 20,
		}

		req, err := mailer.buildSendMailRequest("test@example.com", PromoCode, data)

		assert.NoError(t, err)
		assert.NotNil(t, req)
		assert.Equal(t, "Your promo code", req.Subject)
		assert.Contains(t, *req.Html, "SAVE20")
		assert.Contains(t, *req.Html, "20")
	})
}

func TestTemplateSubjects(t *testing.T) {
	expectedSubjects := map[templateName]string{
		NewSubscriber:        "Welcome to GRBPWR",
		OrderCancelled:       "Your order has been cancelled",
		OrderConfirmed:       "Your order has been confirmed",
		OrderShipped:         "Your order has been shipped",
		OrderRefundInitiated: "Your refund has been initiated",
		PromoCode:            "Your promo code",
	}

	for tmplName, expectedSubject := range expectedSubjects {
		subject, exists := templateSubjects[tmplName]
		assert.True(t, exists, "Subject should exist for template %s", tmplName)
		assert.Equal(t, expectedSubject, subject, "Subject mismatch for template %s", tmplName)
	}
}

func TestTemplateRendering(t *testing.T) {
	mailer := createTestMailer(t)

	t.Run("Order Confirmed with Items", func(t *testing.T) {
		data := &dto.OrderConfirmed{
			Preheader:     "Your GRBPWR order has been confirmed",
			OrderUUID:     "uuid-123",
			SubtotalPrice: "200.00",
			TotalPrice:    "200.00",
			OrderItems: []dto.OrderItem{
				{
					Name:      "Test Product",
					Thumbnail: "https://example.com/image.jpg",
					Size:      "M",
					Quantity:  2,
					Price:     "100.00",
				},
			},
			PromoExist:          true,
			PromoDiscountAmount: "10",
			HasFreeShipping:     true,
			ShippingPrice:       "0.00",
			EmailB64:            base64.StdEncoding.EncodeToString([]byte("test@example.com")),
		}

		req, err := mailer.buildSendMailRequest("test@example.com", OrderConfirmed, data)

		require.NoError(t, err)
		html := *req.Html

		// Check order details
		assert.Contains(t, html, "uuid-123")
		assert.Contains(t, html, "200.00")

		// Check product item
		assert.Contains(t, html, "Test Product")
		assert.Contains(t, html, "https://example.com/image.jpg")
		assert.Contains(t, html, "M")

		// Check promo
		assert.Contains(t, html, "DISCOUNT")
		assert.Contains(t, html, "10")

		// Check free shipping
		assert.Contains(t, html, "FREE")
	})

	t.Run("Order Shipped with Items", func(t *testing.T) {
		data := &dto.OrderShipment{
			Preheader:     "Your GRBPWR order has been shipped",
			OrderUUID:     "ship-456",
			EmailB64:      base64.StdEncoding.EncodeToString([]byte("test@example.com")),
			SubtotalPrice: "50.00",
			OrderItems: []dto.OrderItem{
				{
					Name:      "Shipped Product",
					Thumbnail: "https://example.com/shipped.jpg",
					Size:      "L",
					Quantity:  1,
					Price:     "50.00",
				},
			},
			TotalPrice:          "60.00",
			PromoExist:          false,
			PromoDiscountAmount: "0",
			HasFreeShipping:     false,
			ShippingPrice:       "10.00",
		}

		req, err := mailer.buildSendMailRequest("test@example.com", OrderShipped, data)

		require.NoError(t, err)
		html := *req.Html

		assert.Contains(t, html, "ship-456")
		assert.Contains(t, html, "HAS BEEN SHIPPED")
		assert.Contains(t, html, "Shipped Product")
	})
}
