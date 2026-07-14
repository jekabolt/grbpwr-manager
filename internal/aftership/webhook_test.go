package aftership

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
)

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func postWebhook(t *testing.T, h *WebhookHandler, body []byte, sig string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/aftership", bytes.NewReader(body))
	if sig != "" {
		req.Header.Set("aftership-hmac-sha256", sig)
	}
	rr := httptest.NewRecorder()
	h.HandleAftershipEvent(rr, req)
	return rr.Code
}

func TestWebhookEnabled(t *testing.T) {
	if NewWebhookHandler("", nil, nil).Enabled() {
		t.Fatal("empty secret must disable the handler")
	}
	if !NewWebhookHandler("s", nil, nil).Enabled() {
		t.Fatal("non-empty secret must enable the handler")
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	// A bad signature must be rejected before any repository call.
	repo := mocks.NewMockRepository(t)
	h := NewWebhookHandler("secret", repo, mocks.NewMockMailer(t))
	body := []byte(`{"msg":{"tag":"Delivered","tracking_number":"TN1"}}`)
	if code := postWebhook(t, h, body, "not-the-real-sig"); code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
	if code := postWebhook(t, h, body, ""); code != http.StatusUnauthorized {
		t.Fatalf("missing signature: want 401, got %d", code)
	}
}

func TestWebhookDeliversOnDeliveredTag(t *testing.T) {
	const secret = "secret"
	repo := mocks.NewMockRepository(t)
	order := mocks.NewMockOrder(t)
	mailer := mocks.NewMockMailer(t)
	repo.EXPECT().Order().Return(order)
	order.EXPECT().GetOrderUUIDByTrackingCode(mock.Anything, "TN1").Return("uuid-1", nil)
	order.EXPECT().DeliverOrderWithSource(mock.Anything, "uuid-1", "aftership", mock.Anything).Return(true, nil)
	order.EXPECT().GetOrderFullByUUID(mock.Anything, "uuid-1").Return(&entity.OrderFull{
		Order: entity.Order{UUID: "uuid-1", Currency: "EUR"},
		Buyer: entity.Buyer{BuyerInsert: entity.BuyerInsert{Email: "b@e.com", FirstName: "B"}},
	}, nil)
	mailer.EXPECT().SendOrderDelivered(mock.Anything, mock.Anything, "b@e.com", mock.Anything).Return(nil)

	h := NewWebhookHandler(secret, repo, mailer)
	body := []byte(`{"event":"tracking_update","msg":{"tag":"Delivered","tracking_number":"TN1","slug":"dhl"}}`)
	if code := postWebhook(t, h, body, sign(secret, body)); code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
}

func TestWebhookIgnoresNonDelivered(t *testing.T) {
	const secret = "secret"
	// A non-delivered tag must be acknowledged without any repository call.
	repo := mocks.NewMockRepository(t)
	h := NewWebhookHandler(secret, repo, mocks.NewMockMailer(t))
	body := []byte(`{"msg":{"tag":"InTransit","tracking_number":"TN1"}}`)
	if code := postWebhook(t, h, body, sign(secret, body)); code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
}

func TestWebhookUnknownTrackingAcked(t *testing.T) {
	const secret = "secret"
	repo := mocks.NewMockRepository(t)
	order := mocks.NewMockOrder(t)
	repo.EXPECT().Order().Return(order)
	order.EXPECT().GetOrderUUIDByTrackingCode(mock.Anything, "TNX").Return("", sql.ErrNoRows)

	h := NewWebhookHandler(secret, repo, mocks.NewMockMailer(t))
	body := []byte(`{"msg":{"tag":"Delivered","tracking_number":"TNX"}}`)
	if code := postWebhook(t, h, body, sign(secret, body)); code != http.StatusOK {
		t.Fatalf("want 200 (ack + ignore), got %d", code)
	}
}
