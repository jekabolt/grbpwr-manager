package mail

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/storefront/tokenhash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// testWebhookSecret is the canonical svix example signing secret (whsec_ + base64).
// The handler now fails closed without a configured secret, so tests exercise the
// real verified path by signing their payloads with this secret.
const testWebhookSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// testUnsubPepper keys the per-recipient list-unsubscribe token in tests.
const testUnsubPepper = "test-unsubscribe-pepper-value"

func newTestWebhookHandler(t *testing.T, mailMock *mocks.MockMail) *WebhookHandler {
	t.Helper()
	repoMock := mocks.NewMockRepository(t)
	repoMock.On("Mail").Return(mailMock).Maybe()
	h, err := NewWebhookHandler(repoMock, testWebhookSecret, testUnsubPepper)
	require.NoError(t, err)
	return h
}

// newSignedResendRequest builds a POST request whose body carries a valid svix
// signature for h's secret, so it passes HandleResendEvent's verification.
func newSignedResendRequest(t *testing.T, h *WebhookHandler, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	id := "msg_test"
	ts := time.Now()
	sig, err := h.wh.Sign(id, ts, []byte(body))
	require.NoError(t, err)
	req.Header.Set("svix-id", id)
	req.Header.Set("svix-timestamp", strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set("svix-signature", sig)
	return req
}

func TestParseEventDate(t *testing.T) {
	h := &WebhookHandler{}

	t.Run("valid RFC3339", func(t *testing.T) {
		ts := "2024-05-10T14:30:00Z"
		got := h.parseEventDate(ts)
		assert.Equal(t, 2024, got.Year())
		assert.Equal(t, time.May, got.Month())
		assert.Equal(t, 10, got.Day())
	})

	t.Run("empty string falls back to now", func(t *testing.T) {
		before := time.Now().UTC().Add(-time.Second)
		got := h.parseEventDate("")
		after := time.Now().UTC().Add(time.Second)
		assert.True(t, got.After(before), "should be after before")
		assert.True(t, got.Before(after), "should be before after")
	})

	t.Run("invalid string falls back to now", func(t *testing.T) {
		before := time.Now().UTC().Add(-time.Second)
		got := h.parseEventDate("not-a-date")
		after := time.Now().UTC().Add(time.Second)
		assert.True(t, got.After(before))
		assert.True(t, got.Before(after))
	})
}

// TestHandleResendEvent_NoSecretFailsClosed verifies the handler refuses events
// when no signing secret is configured (h.wh == nil): an unsigned body must never
// be trusted to mutate the suppression list.
func TestHandleResendEvent_NoSecretFailsClosed(t *testing.T) {
	repoMock := mocks.NewMockRepository(t)
	h, err := NewWebhookHandler(repoMock, "", testUnsubPepper)
	require.NoError(t, err)

	body := `{"type":"email.bounced","data":{"to":["victim@example.com"]}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

	// No suppression must be attempted (repoMock has no expectations).
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// TestHandleResendEvent_BadSignatureRejected verifies a body that does not match
// the configured secret's signature is rejected with 401 and not processed.
func TestHandleResendEvent_BadSignatureRejected(t *testing.T) {
	repoMock := mocks.NewMockRepository(t)
	h, err := NewWebhookHandler(repoMock, testWebhookSecret, testUnsubPepper)
	require.NoError(t, err)

	body := `{"type":"email.bounced","data":{"to":["victim@example.com"]}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("svix-id", "msg_test")
	req.Header.Set("svix-timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("svix-signature", "v1,not-a-valid-signature")
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestHandleListUnsubscribe_ValidToken verifies a correct per-recipient token
// unsubscribes the address.
func TestHandleListUnsubscribe_ValidToken(t *testing.T) {
	email := "user@example.com"
	subMock := mocks.NewMockSubscribers(t)
	subMock.On("UpsertSubscription", mock.Anything, email, false).Return(true, nil).Once()
	repoMock := mocks.NewMockRepository(t)
	repoMock.On("Subscribers").Return(subMock).Maybe()
	h, err := NewWebhookHandler(repoMock, testWebhookSecret, testUnsubPepper)
	require.NoError(t, err)

	emailB64 := base64.StdEncoding.EncodeToString([]byte(email))
	token := tokenhash.Hash(testUnsubPepper, unsubscribeTokenValue(email))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("email_b64", emailB64)
	req.SetPathValue("token", token)
	rr := httptest.NewRecorder()

	h.HandleListUnsubscribe(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	subMock.AssertExpectations(t)
}

// TestHandleListUnsubscribe_InvalidToken verifies a wrong token is rejected with
// 403 and no subscription change is attempted.
func TestHandleListUnsubscribe_InvalidToken(t *testing.T) {
	email := "user@example.com"
	repoMock := mocks.NewMockRepository(t) // no Subscribers() expectation: must not be called
	h, err := NewWebhookHandler(repoMock, testWebhookSecret, testUnsubPepper)
	require.NoError(t, err)

	emailB64 := base64.StdEncoding.EncodeToString([]byte(email))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("email_b64", emailB64)
	req.SetPathValue("token", "deadbeef")
	rr := httptest.NewRecorder()

	h.HandleListUnsubscribe(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// TestHandleListUnsubscribe_NoPepperFailsClosed verifies the endpoint refuses
// requests when no pepper is configured.
func TestHandleListUnsubscribe_NoPepperFailsClosed(t *testing.T) {
	repoMock := mocks.NewMockRepository(t)
	h, err := NewWebhookHandler(repoMock, testWebhookSecret, "")
	require.NoError(t, err)

	email := "user@example.com"
	emailB64 := base64.StdEncoding.EncodeToString([]byte(email))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("email_b64", emailB64)
	req.SetPathValue("token", "anything")
	rr := httptest.NewRecorder()

	h.HandleListUnsubscribe(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestHandleResendEvent_DeliveredIncrementsMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "delivered", mock.AnythingOfType("time.Time")).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.delivered","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, newSignedResendRequest(t, h, body))

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_BouncedIncrementsBounceAndSuppresses(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "bounced", mock.AnythingOfType("time.Time")).
		Return(nil).Once()
	mailMock.On("AddSuppression", mock.Anything, "user@example.com", entity.SuppressionReasonBounce).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.bounced","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, newSignedResendRequest(t, h, body))

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_FailedIncrementsBounceAndSuppresses(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "bounced", mock.AnythingOfType("time.Time")).
		Return(nil).Once()
	mailMock.On("AddSuppression", mock.Anything, "user@example.com", entity.SuppressionReasonBounce).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.failed","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, newSignedResendRequest(t, h, body))

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_OpenedIncrementsMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "opened", mock.AnythingOfType("time.Time")).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.opened","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, newSignedResendRequest(t, h, body))

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_ClickedIncrementsMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "clicked", mock.AnythingOfType("time.Time")).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.clicked","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, newSignedResendRequest(t, h, body))

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_DelayedNoMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	// No IncrementEmailMetric call expected for delivery_delayed

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.delivery_delayed","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, newSignedResendRequest(t, h, body))

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestIncrementMetric_LogsOnError(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "delivered", mock.AnythingOfType("time.Time")).
		Return(assert.AnError).Once()

	repoMock := mocks.NewMockRepository(t)
	repoMock.On("Mail").Return(mailMock).Maybe()
	h := &WebhookHandler{repo: repoMock}

	// Should not panic even when IncrementEmailMetric returns an error
	h.incrementMetric(context.Background(), "delivered", time.Now().UTC())
	mailMock.AssertExpectations(t)
}
