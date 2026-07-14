package aftership

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
)

// maxWebhookBody caps the request body read for a webhook event (1 MiB).
const maxWebhookBody = 1 << 20

// WebhookHandler verifies and processes AfterShip tracking webhooks. On a Delivered event it marks
// the matching order delivered (idempotent, via the shared source-aware transition) and sends the
// delivered email. All other events are acknowledged and ignored.
type WebhookHandler struct {
	secret string
	rep    dependency.Repository
	mailer dependency.Mailer
}

// NewWebhookHandler builds an AfterShip webhook handler. An empty secret disables the endpoint
// (Enabled() == false), mirroring the Stripe webhook handler.
func NewWebhookHandler(secret string, rep dependency.Repository, mailer dependency.Mailer) *WebhookHandler {
	return &WebhookHandler{secret: strings.TrimSpace(secret), rep: rep, mailer: mailer}
}

// Enabled reports whether a signing secret is configured (the route is mounted only when enabled).
func (h *WebhookHandler) Enabled() bool { return h.secret != "" }

// webhookPayload is the subset of the AfterShip webhook body we read. The tracking object is
// delivered under "msg".
type webhookPayload struct {
	Event string `json:"event"`
	Msg   struct {
		Tag            string `json:"tag"`
		TrackingNumber string `json:"tracking_number"`
		Slug           string `json:"slug"`
	} `json:"msg"`
}

// HandleAftershipEvent is the HTTP entry point for AfterShip webhook deliveries. It verifies the
// HMAC signature and, for a Delivered tag, marks the matching order delivered and sends the
// delivered email. Unhandled events are acknowledged with 200 so AfterShip stops retrying them; a
// 5xx is returned only for transient failures, which AfterShip retries.
func (h *WebhookHandler) HandleAftershipEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		slog.Default().ErrorContext(ctx, "aftership webhook: can't read body", slog.String("err", err.Error()))
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if !h.verifySignature(r, payload) {
		slog.Default().WarnContext(ctx, "aftership webhook: signature verification failed")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var ev webhookPayload
	if err := json.Unmarshal(payload, &ev); err != nil {
		slog.Default().ErrorContext(ctx, "aftership webhook: can't parse body", slog.String("err", err.Error()))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !strings.EqualFold(ev.Msg.Tag, tagDelivered) {
		slog.Default().DebugContext(ctx, "aftership webhook: ignoring non-delivered event", slog.String("tag", ev.Msg.Tag))
		w.WriteHeader(http.StatusOK)
		return
	}
	if ev.Msg.TrackingNumber == "" {
		slog.Default().WarnContext(ctx, "aftership webhook: delivered event without tracking number")
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := h.markDelivered(ctx, ev.Msg.TrackingNumber); err != nil {
		slog.Default().ErrorContext(ctx, "aftership webhook: can't mark delivered",
			slog.String("tracking_number", ev.Msg.TrackingNumber),
			slog.String("err", err.Error()),
		)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// markDelivered resolves the tracking number to an order and marks it delivered. The delivered
// email is sent best-effort only when this call performed the transition, so a webhook retry (or a
// prior manual/timer delivery) never double-sends. An unknown tracking number is acknowledged and
// ignored.
func (h *WebhookHandler) markDelivered(ctx context.Context, trackingNumber string) error {
	orderUUID, err := h.rep.Order().GetOrderUUIDByTrackingCode(ctx, trackingNumber)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			slog.Default().InfoContext(ctx, "aftership webhook: no order for tracking number, ignoring",
				slog.String("tracking_number", trackingNumber))
			return nil
		}
		return fmt.Errorf("resolve order by tracking code: %w", err)
	}

	transitioned, err := h.rep.Order().DeliverOrderWithSource(ctx, orderUUID, "aftership", "auto-delivered: AfterShip reported delivered")
	if err != nil {
		return fmt.Errorf("mark order delivered: %w", err)
	}
	if !transitioned {
		return nil // already delivered / not eligible — no email
	}

	if err := mail.SendOrderDeliveredForUUID(ctx, h.rep, h.mailer, orderUUID); err != nil {
		// Email is best-effort: the status is already delivered, so never fail the webhook (and
		// force AfterShip to retry) on an email hiccup. The mail worker retries transient sends.
		slog.Default().ErrorContext(ctx, "aftership webhook: can't send delivered email",
			slog.String("order_uuid", orderUUID), slog.String("err", err.Error()))
	}
	slog.Default().InfoContext(ctx, "order auto-delivered from AfterShip webhook", slog.String("order_uuid", orderUUID))
	return nil
}

// verifySignature checks the AfterShip HMAC-SHA256 signature (base64 of HMAC over the raw body
// with the webhook secret). AfterShip has shipped the signature under two header names across
// versions, so both are accepted.
func (h *WebhookHandler) verifySignature(r *http.Request, body []byte) bool {
	sig := r.Header.Get("aftership-hmac-sha256")
	if sig == "" {
		sig = r.Header.Get("as-signature-hmac-sha256")
	}
	if sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}
