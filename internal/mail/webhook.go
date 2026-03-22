package mail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	svix "github.com/svix/svix-webhooks/go"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// resendEventType represents the type field in a Resend webhook payload.
type resendEventType string

const (
	eventEmailBounced        resendEventType = "email.bounced"
	eventEmailComplained     resendEventType = "email.complained"
	eventEmailDelivered      resendEventType = "email.delivered"
	eventEmailSuppressed     resendEventType = "email.suppressed"
	eventEmailFailed         resendEventType = "email.failed"
	eventEmailDeliveryDelayed resendEventType = "email.delivery_delayed"
	eventEmailOpened         resendEventType = "email.opened"
	eventEmailClicked        resendEventType = "email.clicked"
)

// resendBounce contains bounce-specific metadata.
type resendBounce struct {
	Message string `json:"message"`
	SubType string `json:"subType"`
	Type    string `json:"type"`
}

// resendWebhookData is the data field common to all Resend webhook events.
type resendWebhookData struct {
	EmailID string       `json:"email_id"`
	To      []string     `json:"to"`
	Bounce  *resendBounce `json:"bounce,omitempty"`
}

// resendWebhookEvent is the top-level Resend webhook payload.
type resendWebhookEvent struct {
	Type      resendEventType   `json:"type"`
	CreatedAt string            `json:"created_at"`
	Data      resendWebhookData `json:"data"`
}

// WebhookHandler handles inbound Resend webhook events.
// It verifies the Svix signature when a webhook secret is configured, then
// adds bounced/complained addresses to the suppression list.
type WebhookHandler struct {
	repo   dependency.Repository
	secret string
	wh     *svix.Webhook
}

// NewWebhookHandler creates a WebhookHandler. If secret is empty, signature
// verification is skipped (development / unconfigured deployments).
func NewWebhookHandler(repo dependency.Repository, secret string) (*WebhookHandler, error) {
	h := &WebhookHandler{repo: repo, secret: secret}
	if secret != "" {
		wh, err := svix.NewWebhook(secret)
		if err != nil {
			return nil, fmt.Errorf("invalid webhook secret: %w", err)
		}
		h.wh = wh
	}
	return h, nil
}

// HandleResendEvent processes POST /api/webhooks/resend.
func (h *WebhookHandler) HandleResendEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		slog.Default().ErrorContext(ctx, "resend webhook: failed to read body",
			slog.String("err", err.Error()))
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	if h.wh != nil {
		if err := h.wh.Verify(body, r.Header); err != nil {
			slog.Default().WarnContext(ctx, "resend webhook: signature verification failed",
				slog.String("err", err.Error()))
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	var event resendWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		slog.Default().ErrorContext(ctx, "resend webhook: failed to parse payload",
			slog.String("err", err.Error()))
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	eventDate := h.parseEventDate(event.CreatedAt)

	switch event.Type {
	case eventEmailBounced:
		h.incrementMetric(ctx, "bounced", eventDate)
		for _, to := range event.Data.To {
			if err := h.repo.Mail().AddSuppression(ctx, to, entity.SuppressionReasonBounce); err != nil {
				slog.Default().ErrorContext(ctx, "resend webhook: failed to add bounce suppression",
					slog.String("email", to),
					slog.String("err", err.Error()))
			} else {
				slog.Default().InfoContext(ctx, "resend webhook: suppressed bounced address",
					slog.String("email", to),
					slog.String("emailId", event.Data.EmailID))
			}
		}

	case eventEmailComplained:
		for _, to := range event.Data.To {
			if err := h.repo.Mail().AddSuppression(ctx, to, entity.SuppressionReasonComplaint); err != nil {
				slog.Default().ErrorContext(ctx, "resend webhook: failed to add complaint suppression",
					slog.String("email", to),
					slog.String("err", err.Error()))
			} else {
				slog.Default().InfoContext(ctx, "resend webhook: suppressed complained address",
					slog.String("email", to),
					slog.String("emailId", event.Data.EmailID))
			}
		}

	case eventEmailSuppressed:
		for _, to := range event.Data.To {
			if err := h.repo.Mail().AddSuppression(ctx, to, entity.SuppressionReasonBounce); err != nil {
				slog.Default().ErrorContext(ctx, "resend webhook: failed to add suppressed address",
					slog.String("email", to),
					slog.String("err", err.Error()))
			} else {
				slog.Default().InfoContext(ctx, "resend webhook: suppressed address (resend suppression list)",
					slog.String("email", to),
					slog.String("emailId", event.Data.EmailID))
			}
		}

	case eventEmailFailed:
		h.incrementMetric(ctx, "bounced", eventDate)
		for _, to := range event.Data.To {
			if err := h.repo.Mail().AddSuppression(ctx, to, entity.SuppressionReasonBounce); err != nil {
				slog.Default().ErrorContext(ctx, "resend webhook: failed to add failed email suppression",
					slog.String("email", to),
					slog.String("err", err.Error()))
			} else {
				slog.Default().InfoContext(ctx, "resend webhook: suppressed failed address",
					slog.String("email", to),
					slog.String("emailId", event.Data.EmailID))
			}
		}

	case eventEmailDelivered:
		h.incrementMetric(ctx, "delivered", eventDate)
		slog.Default().InfoContext(ctx, "resend webhook: email delivered",
			slog.String("emailId", event.Data.EmailID))

	case eventEmailOpened:
		h.incrementMetric(ctx, "opened", eventDate)
		slog.Default().InfoContext(ctx, "resend webhook: email opened",
			slog.String("emailId", event.Data.EmailID))

	case eventEmailClicked:
		h.incrementMetric(ctx, "clicked", eventDate)
		slog.Default().InfoContext(ctx, "resend webhook: email clicked",
			slog.String("emailId", event.Data.EmailID))

	case eventEmailDeliveryDelayed:
		slog.Default().InfoContext(ctx, "resend webhook: email delivery delayed",
			slog.String("emailId", event.Data.EmailID))

	default:
		slog.Default().InfoContext(ctx, "resend webhook: unhandled event type",
			slog.String("type", string(event.Type)))
	}

	w.WriteHeader(http.StatusOK)
}

// parseEventDate parses the RFC3339 created_at string from a Resend webhook event.
// Falls back to the current UTC time when the string is empty or unparseable.
func (h *WebhookHandler) parseEventDate(createdAt string) time.Time {
	if createdAt != "" {
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// incrementMetric increments a named counter in email_daily_metrics, logging on failure.
func (h *WebhookHandler) incrementMetric(ctx context.Context, metricType string, date time.Time) {
	if err := h.repo.Mail().IncrementEmailMetric(ctx, metricType, date); err != nil {
		slog.Default().ErrorContext(ctx, "resend webhook: failed to increment email metric",
			slog.String("metric", metricType),
			slog.String("err", err.Error()))
	}
}

// HandleListUnsubscribe processes POST /api/webhooks/list-unsubscribe/{email_b64}.
// Email clients POST List-Unsubscribe=One-Click to this endpoint for one-click unsubscribe.
func (h *WebhookHandler) HandleListUnsubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	emailB64 := r.PathValue("email_b64")
	if emailB64 == "" {
		http.Error(w, "missing email", http.StatusBadRequest)
		return
	}

	emailBytes, err := base64.StdEncoding.DecodeString(emailB64)
	if err != nil {
		slog.Default().WarnContext(ctx, "list-unsubscribe: invalid base64",
			slog.String("email_b64", emailB64),
			slog.String("err", err.Error()))
		http.Error(w, "invalid email encoding", http.StatusBadRequest)
		return
	}

	email := string(emailBytes)
	if _, err := validateEmailAddress(email); err != nil {
		slog.Default().WarnContext(ctx, "list-unsubscribe: invalid email",
			slog.String("email", email),
			slog.String("err", err.Error()))
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}

	if _, err := h.repo.Subscribers().UpsertSubscription(ctx, email, false); err != nil {
		slog.Default().ErrorContext(ctx, "list-unsubscribe: failed to unsubscribe",
			slog.String("email", email),
			slog.String("err", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Default().InfoContext(ctx, "list-unsubscribe: one-click unsubscribe processed",
		slog.String("email", email))
	w.WriteHeader(http.StatusOK)
}
