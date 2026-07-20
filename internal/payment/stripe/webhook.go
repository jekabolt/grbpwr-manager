package stripe

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
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
	case stripe.EventTypeChargeDisputeCreated, stripe.EventTypeChargeDisputeClosed:
		// A chargeback: enqueue an order_dispute accounting event (the acctposting worker posts it —
		// created books Dr 4040 + Dr 6050 / Cr 1030, closed-won reverses). A transient enqueue failure is
		// a 5xx so Stripe retries; a malformed payload or a dispute whose order can't be resolved is
		// acknowledged (retrying won't help).
		closed := event.Type == stripe.EventTypeChargeDisputeClosed
		if err := proc.recordDisputeFromWebhook(ctx, event.Data.Raw, closed); err != nil {
			slog.Default().ErrorContext(ctx, "stripe webhook: can't record dispute",
				slog.String("event_id", event.ID),
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

// recordDisputeFromWebhook enqueues an order_dispute accounting event for a Stripe chargeback (phase 2,
// wave 4 — §4.3). It resolves the order from the dispute's PaymentIntent, extracts the disputed amount and
// dispute fee from the dispute's balance transactions (booked in the Stripe balance currency, EUR — the
// account settlement currency), and hands both to the outbox. The acctposting worker builds the entry.
// Producers always enqueue regardless of whether the posting worker is enabled (the queue drains from the
// cutover); EnqueueEvent is idempotent on (event_type, source_key). Returns an error only for a transient
// failure (so Stripe retries); a malformed payload or an unresolvable order is a benign ack.
func (p *Processor) recordDisputeFromWebhook(ctx context.Context, raw json.RawMessage, closed bool) error {
	var d stripe.Dispute
	if err := json.Unmarshal(raw, &d); err != nil {
		slog.Default().ErrorContext(ctx, "stripe webhook: can't parse dispute", slog.String("err", err.Error()))
		return nil // a malformed payload will not parse on retry — ack it
	}

	piID := ""
	if d.PaymentIntent != nil {
		piID = d.PaymentIntent.ID
	}
	if piID == "" {
		slog.Default().WarnContext(ctx, "stripe webhook: dispute has no payment_intent, ignoring",
			slog.String("dispute_id", d.ID))
		return nil
	}

	orderFull, err := p.rep.Order().GetOrderByPaymentIntentId(ctx, piID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			slog.Default().WarnContext(ctx, "stripe webhook: no order for disputed payment_intent, ignoring",
				slog.String("dispute_id", d.ID), slog.String("payment_intent_id", piID))
			return nil
		}
		return fmt.Errorf("get order for dispute %s: %w", d.ID, err)
	}

	// The disputed amount and dispute fee come from the balance transactions (the withdrawal from the
	// Stripe balance): amount = Σ|amount| of the withdrawal legs, fee = Σ fee. Both are already in the
	// account settlement currency (EUR). Empty balance transactions (Stripe has not withdrawn yet) leave
	// amount 0 (the worker falls back to the order's settled base) and fee unknown.
	feeBase, amountBase := decimal.Zero, decimal.Zero
	for _, bt := range d.BalanceTransactions {
		if bt == nil {
			continue
		}
		cur := string(bt.Currency)
		feeBase = feeBase.Add(AmountFromSmallestUnit(bt.Fee, cur))
		if bt.Amount < 0 {
			amountBase = amountBase.Add(AmountFromSmallestUnit(-bt.Amount, cur))
		}
	}

	suffix, occurredAt := "open", time.Unix(d.Created, 0).UTC()
	if closed {
		suffix, occurredAt = "close", p.rep.Now().UTC()
	}

	payload := entity.AcctOrderDisputePayload{
		OrderUUID:  orderFull.Order.UUID,
		DisputeID:  d.ID,
		Closed:     closed,
		Won:        d.Status == stripe.DisputeStatusWon,
		AmountBase: amountBase,
		FeeBase:    feeBase,
		FeeKnown:   len(d.BalanceTransactions) > 0,
		Currency:   string(d.Currency),
	}
	if err := p.rep.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
		EventType:  entity.AcctEventOrderDispute,
		SourceKey:  fmt.Sprintf("dispute:%s:%s", d.ID, suffix),
		Payload:    payload,
		OccurredAt: occurredAt,
	}); err != nil {
		return fmt.Errorf("enqueue dispute event %s: %w", d.ID, err)
	}
	return nil
}
