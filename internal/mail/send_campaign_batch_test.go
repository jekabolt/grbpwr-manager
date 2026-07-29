package mail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

func batchTestMailer(t *testing.T, handler http.Handler) (*Mailer, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := resend.NewClient(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	return &Mailer{
		cli:    client,
		c:      &Config{},
		budget: NewResendBudget(1000, 2, 1),
	}, server
}

func batchRequest(to string) resend.SendEmailRequest {
	html := "<p>hello</p>"
	text := "hello"
	return resend.SendEmailRequest{
		From:    "GRBPWR <info@grbpwr.com>",
		To:      []string{to},
		Subject: "Hello",
		Html:    &html,
		Text:    &text,
	}
}

func TestSendCampaignBatchIdempotencyHeaderAndOrderedIDs(t *testing.T) {
	var callbackCalled atomic.Bool
	mailer, _ := batchTestMailer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "grbpwr-campaign-7-batch" {
			t.Errorf("Idempotency-Key = %q", got)
		}
		if !callbackCalled.Load() {
			t.Error("provider request arrived before persistence callback completed")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"email-0"},{"id":"email-1"}]}`)
	}))

	ids, err := mailer.SendCampaignBatch(
		context.Background(),
		[]resend.SendEmailRequest{batchRequest("a@example.com"), batchRequest("b@example.com")},
		"grbpwr-campaign-7-batch",
		func() error {
			callbackCalled.Store(true)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids) != "[email-0 email-1]" {
		t.Fatalf("ordered ids = %v", ids)
	}
}

func TestSendCampaignBatchResponseClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		ambiguous bool
	}{
		{name: "rate limited", status: 429, body: `{"message":"slow"}`},
		{name: "server", status: 503, body: `{"message":"down"}`},
		{name: "permanent", status: 422, body: `{"message":"bad"}`},
		{name: "malformed success", status: 200, body: `{"data":[]}`, ambiguous: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mailer, _ := batchTestMailer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status == 429 {
					w.Header().Set("Retry-After", "3")
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			_, err := mailer.SendCampaignBatch(
				context.Background(),
				[]resend.SendEmailRequest{batchRequest("a@example.com")},
				"key",
				func() error { return nil },
			)
			var sendErr *CampaignBatchSendError
			if !errors.As(err, &sendErr) {
				t.Fatalf("error = %T %v", err, err)
			}
			if sendErr.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", sendErr.StatusCode, tt.status)
			}
			if sendErr.Ambiguous != tt.ambiguous {
				t.Fatalf("ambiguous = %v", sendErr.Ambiguous)
			}
			if tt.status == 429 && sendErr.RetryAfter != 3*time.Second {
				t.Fatalf("retry after = %v", sendErr.RetryAfter)
			}
		})
	}
}

func TestSendCampaignBatchDisabledMakesZeroProviderCalls(t *testing.T) {
	var calls atomic.Int32
	mailer, _ := batchTestMailer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	mailer.c.Disabled = true
	_, err := mailer.SendCampaignBatch(
		context.Background(),
		[]resend.SendEmailRequest{batchRequest("a@example.com")},
		"key",
		func() error {
			t.Fatal("disabled send invoked before-POST callback")
			return nil
		},
	)
	if !errors.Is(err, ErrSuppressedDisabled) {
		t.Fatalf("error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
}
