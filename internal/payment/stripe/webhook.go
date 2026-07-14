package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/webhook"
)

// maxStripeWebhookBody caps the request body read for a webhook event (64 KiB).
const maxStripeWebhookBody = 1 << 16

// WebhookHandler verifies and dispatches inbound Stripe webhook events. It holds
// one (signing secret, processor) endpoint per configured Stripe processor
// (live/test), so a single /api/webhooks/stripe route serves both modes: the
// secret that validates the signature identifies the owning processor.
type WebhookHandler struct {
	endpoints []webhookEndpoint
}

type webhookEndpoint struct {
	secret    string
	processor *Processor
}

// NewWebhookHandler builds a handler from the given processors. Processors with
// an empty WebhookSecret are skipped (their endpoint is disabled), so a
// test-only deployment can configure just the test secret.
func NewWebhookHandler(processors ...*Processor) *WebhookHandler {
	h := &WebhookHandler{}
	for _, p := range processors {
		if p != nil && p.c != nil && p.c.WebhookSecret != "" {
			h.endpoints = append(h.endpoints, webhookEndpoint{secret: p.c.WebhookSecret, processor: p})
		}
	}
	return h
}

// Enabled reports whether at least one signing secret is configured.
func (h *WebhookHandler) Enabled() bool {
	return len(h.endpoints) > 0
}

// HandleStripeEvent is the HTTP entry point for Stripe webhook deliveries. It
// verifies the signature against the configured secrets and, for a succeeded
// PaymentIntent, confirms the corresponding order. Unhandled event types are
// acknowledged with 200 so Stripe stops retrying them. A 5xx is returned only
// for transient failures, which Stripe will retry.
func (h *WebhookHandler) HandleStripeEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStripeWebhookBody))
	if err != nil {
		slog.Default().ErrorContext(ctx, "stripe webhook: can't read body", slog.String("err", err.Error()))
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	sig := r.Header.Get("Stripe-Signature")

	// Verify against each configured secret; the one that validates identifies
	// the owning processor (live vs test).
	var event stripe.Event
	var proc *Processor
	for _, ep := range h.endpoints {
		ev, verr := webhook.ConstructEvent(payload, sig, ep.secret)
		if verr == nil {
			event = ev
			proc = ep.processor
			break
		}
	}
	if proc == nil {
		slog.Default().WarnContext(ctx, "stripe webhook: signature verification failed for all configured secrets")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			slog.Default().ErrorContext(ctx, "stripe webhook: can't parse payment_intent",
				slog.String("event_id", event.ID),
				slog.String("err", err.Error()),
			)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := proc.confirmPaymentFromWebhook(ctx, &pi); err != nil {
			// Transient failure: return 5xx so Stripe retries delivery.
			slog.Default().ErrorContext(ctx, "stripe webhook: can't confirm payment",
				slog.String("payment_intent_id", pi.ID),
				slog.String("err", err.Error()),
			)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	default:
		slog.Default().DebugContext(ctx, "stripe webhook: ignoring event type", slog.String("type", string(event.Type)))
	}

	w.WriteHeader(http.StatusOK)
}

// confirmPaymentFromWebhook marks the order behind a succeeded PaymentIntent as
// paid. It routes through updateOrderAsPaid (the shared confirmation choke point),
// so it is idempotent and shares the underpayment guard with the monitor and the
// lazy CheckForTransactions path.
func (p *Processor) confirmPaymentFromWebhook(ctx context.Context, pi *stripe.PaymentIntent) error {
	orderUUID := pi.Metadata["order_id"]
	if orderUUID == "" {
		// Pre-order PaymentIntents (cart sessions) have no order yet — ignore.
		slog.Default().InfoContext(ctx, "stripe webhook: payment_intent has no order_id metadata, ignoring",
			slog.String("payment_intent_id", pi.ID))
		return nil
	}

	payment, err := p.rep.Order().GetPaymentByOrderUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get payment for order %s: %w", orderUUID, err)
	}

	// The payment method type, card, receipt and settled amount are captured from the charge
	// in capturePaymentDetails (via updateOrderAsPaid), shared by every confirmation path.
	received := AmountFromSmallestUnit(pi.AmountReceived, string(pi.Currency))
	if err := p.updateOrderAsPaid(ctx, p.rep, orderUUID, *payment, received); err != nil {
		// Underpaid: order stays AwaitingPayment (flagged for review). Acknowledge
		// the event so Stripe does not retry it.
		if errors.Is(err, ErrUnderpaid) {
			slog.Default().WarnContext(ctx, "stripe webhook: underpaid order left for manual review",
				slog.String("order_uuid", orderUUID),
				slog.String("payment_intent_id", pi.ID),
			)
			return nil
		}
		return fmt.Errorf("can't mark order %s as paid: %w", orderUUID, err)
	}

	// Free the in-process monitor goroutine promptly (best effort).
	_ = p.CancelMonitorPayment(orderUUID)
	return nil
}
