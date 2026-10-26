package stripe

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	stripego "github.com/stripe/stripe-go/v79"
	stripewebhook "github.com/stripe/stripe-go/v79/webhook"
)

func TestPaymentIntentWebhookKnownAndForeignOrders(t *testing.T) {
	const signingSecret = "test_signing_secret"

	tests := []struct {
		name      string
		orderUUID string
		foreign   bool
	}{
		{name: "known order", orderUUID: "order-local"},
		{name: "foreign order", orderUUID: "order-other-environment", foreign: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orders := mocks.NewMockOrder(t)
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Order().Return(orders).Maybe()

			if tt.foreign {
				orders.EXPECT().GetPaymentByOrderUUID(mock.Anything, tt.orderUUID).Return(nil, sql.ErrNoRows)
			} else {
				payment := &entity.Payment{PaymentInsert: entity.PaymentInsert{
					TransactionAmountPaymentCurrency: decimal.NewFromInt(10),
				}}
				orders.EXPECT().GetPaymentByOrderUUID(mock.Anything, tt.orderUUID).Return(payment, nil)
				orders.EXPECT().OrderPaymentDone(mock.Anything, tt.orderUUID, mock.Anything).Return(false, nil)
			}

			processor := &Processor{
				c:       &Config{WebhookSecret: signingSecret},
				rep:     repo,
				monCtxt: make(map[string]monEntry),
			}
			handler := NewWebhookHandler(processor)
			payload := []byte(fmt.Sprintf(`{"id":"evt_1","object":"event","api_version":%q,"type":"payment_intent.succeeded","data":{"object":{"id":"pi_1","amount_received":1000,"currency":"eur","metadata":{"order_id":%q}}}}`, stripego.APIVersion, tt.orderUUID))
			signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
				Payload: payload,
				Secret:  signingSecret,
			})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/stripe", bytes.NewReader(payload))
			req.Header.Set("Stripe-Signature", signed.Header)
			res := httptest.NewRecorder()
			handler.HandleStripeEvent(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
			}
		})
	}
}
