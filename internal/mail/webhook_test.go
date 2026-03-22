package mail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newTestWebhookHandler(t *testing.T, mailMock *mocks.MockMail) *WebhookHandler {
	t.Helper()
	repoMock := mocks.NewMockRepository(t)
	repoMock.On("Mail").Return(mailMock).Maybe()
	h, err := NewWebhookHandler(repoMock, "")
	require.NoError(t, err)
	return h
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

func TestHandleResendEvent_DeliveredIncrementsMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "delivered", mock.AnythingOfType("time.Time")).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.delivered","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

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
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

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
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_OpenedIncrementsMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "opened", mock.AnythingOfType("time.Time")).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.opened","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_ClickedIncrementsMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	mailMock.On("IncrementEmailMetric", mock.Anything, "clicked", mock.AnythingOfType("time.Time")).
		Return(nil).Once()

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.clicked","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mailMock.AssertExpectations(t)
}

func TestHandleResendEvent_DelayedNoMetric(t *testing.T) {
	mailMock := mocks.NewMockMail(t)
	// No IncrementEmailMetric call expected for delivery_delayed

	h := newTestWebhookHandler(t, mailMock)

	body := `{"type":"email.delivery_delayed","created_at":"2024-05-10T14:30:00Z","data":{"email_id":"abc123","to":["user@example.com"]}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.HandleResendEvent(rr, req)

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
