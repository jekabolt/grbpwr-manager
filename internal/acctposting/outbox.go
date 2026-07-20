package acctposting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// settledRetryInterval is the fixed defer for a Stripe order_paid whose settlement has not arrived
// (ErrNotReady) or a refund whose sale is not posted yet (awaiting sale posting) — a short, constant
// wait, not exponential: the fact is expected to become ready soon.
const settledRetryInterval = 5 * time.Minute

// eventBackoff paces the retry of an event that hit an unexpected posting error: min(1m * 2^attempts,
// 6h). attempts is the count BEFORE this failure (MarkEventFailed increments it).
func eventBackoff(attempts int) time.Duration {
	const baseDelay = time.Minute
	const maxDelay = 6 * time.Hour
	d := baseDelay
	for i := 0; i < attempts; i++ {
		d *= 2
		if d >= maxDelay {
			return maxDelay
		}
	}
	return d
}

// processOutbox is phase 1: pull due order events on the pool and post/defer/skip each. A phase-level
// error (the list read, or an event-mark write that itself fails) fails the tick; per-event outcomes
// (posted, deferred, skipped, recorded-failure) do not.
func (w *Worker) processOutbox(ctx context.Context) error {
	events, err := w.repo.Accounting().ListPendingEvents(ctx, w.c.BatchSize)
	if err != nil {
		return fmt.Errorf("list pending events: %w", err)
	}
	for i := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.processEvent(ctx, events[i]); err != nil {
			return err
		}
	}
	return nil
}

// processEvent dispatches one outbox event. It returns an error only for infrastructure failures
// (a Mark*/Tx write that itself failed) — a handled outcome (skip/defer/recorded posting error)
// returns nil so the tick stays healthy.
func (w *Worker) processEvent(ctx context.Context, ev entity.AcctEvent) error {
	// Pre-start: an event for an order paid/refunded before the cutover is not booked ("start from
	// zero", docs/plan-accounting/03, FAQ 31). This is the first check for BOTH event types.
	if ev.OccurredAt.Before(w.startDate) {
		return w.skipEvent(ctx, ev.Id, "pre-start event")
	}

	switch ev.EventType {
	case entity.AcctEventOrderPaid:
		return w.processOrderPaid(ctx, ev)
	case entity.AcctEventOrderRefund:
		return w.processOrderRefund(ctx, ev)
	default:
		// The DB CHECK constrains event_type, so this is unreachable in practice; skip loudly rather
		// than loop on an unknown type.
		return w.skipEvent(ctx, ev.Id, "unknown event type "+string(ev.EventType))
	}
}

// processOrderPaid posts an order sale (S1). Readiness/skip decisions come from the builder's
// sentinels (grossEUR): settled-pending → defer, non-EUR non-Stripe → manual skip.
func (w *Worker) processOrderPaid(ctx context.Context, ev entity.AcctEvent) error {
	var p entity.AcctOrderPaidPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return w.skipEvent(ctx, ev.Id, "invalid order_paid payload: "+err.Error())
	}

	facts, err := w.repo.Accounting().GetOrderFactsForPosting(ctx, p.OrderUUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return w.skipEvent(ctx, ev.Id, "order not found for order_paid")
		}
		return fmt.Errorf("order facts %s: %w", p.OrderUUID, err)
	}

	// Resolve the VAT regime + rate on the pool (not in a Tx). A missing rate for a VAT-bearing regime
	// skips the event with an alert instead of posting a zero rate (07 §7.4.14).
	vd, skipReason, err := w.resolveVatDecision(ctx, facts)
	if err != nil {
		return fmt.Errorf("resolve vat %s: %w", p.OrderUUID, err)
	}
	if skipReason != "" {
		return w.skipEvent(ctx, ev.Id, skipReason)
	}

	entry, buildErr := accounting.BuildOrderSaleEntry(*facts, vd, ev.OccurredAt)
	// The resolved regime is snapshotted onto the order in the same Tx that posts the sale (§1.3).
	return w.postOrDefer(ctx, ev, entry, buildErr, facts.UUID, string(vd.Regime))
}

// resolveVatDecision assembles VatFacts from the order facts + the worker's ship-from origin, resolves
// the regime, and looks up the regime's vat_rate. It returns a non-empty skipReason when a VAT-bearing
// regime has no rate configured (07 §7.4.14); the error return is reserved for infrastructure failures.
func (w *Worker) resolveVatDecision(ctx context.Context, facts *entity.AcctOrderFacts) (accounting.VatDecision, string, error) {
	buyerVatID := ""
	if facts.BuyerVatID.Valid {
		buyerVatID = strings.TrimSpace(facts.BuyerVatID.String)
	}
	regime, caveats := accounting.ResolveVatRegime(accounting.VatFacts{
		DestCountry:   facts.DestCountry,
		OriginCountry: w.c.OriginCountry,
		IsB2B:         buyerVatID != "",
		BuyerVatID:    buyerVatID,
		PaymentMethod: facts.PaymentMethodName,
	})
	vd := accounting.VatDecision{Regime: regime, Caveats: caveats}
	if !accounting.RegimeHasVAT(regime) {
		return vd, "", nil
	}

	rateCountry := accounting.RegimeRateCountry(regime, facts.DestCountry, w.c.OriginCountry)
	rates, err := w.repo.Accounting().GetVatRatesFor(ctx, []string{rateCountry})
	if err != nil {
		return vd, "", fmt.Errorf("get vat rate %s: %w", rateCountry, err)
	}
	rate, ok := rates[strings.ToUpper(strings.TrimSpace(rateCountry))]
	if !ok {
		return vd, "vat rate missing for " + rateCountry, nil
	}
	vd.RatePct = rate
	return vd, "", nil
}

// processOrderRefund posts an order refund (S2), but only once the sale (S1) for the order exists —
// the refund's EUR share k must match the one the sale used (docs/plan-accounting/03/04). Until then
// it defers ("awaiting sale posting"); a refund of a never-posted (pre-cutover / non-EUR) order stays
// deferred and is resolved manually via the reconciliation report.
func (w *Worker) processOrderRefund(ctx context.Context, ev entity.AcctEvent) error {
	var p entity.AcctOrderRefundPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return w.skipEvent(ctx, ev.Id, "invalid order_refund payload: "+err.Error())
	}

	exists, err := w.saleEntryExists(ctx, p.OrderUUID)
	if err != nil {
		return fmt.Errorf("check sale posted %s: %w", p.OrderUUID, err)
	}
	if !exists {
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, "awaiting sale posting", settledRetryInterval); err != nil {
			return fmt.Errorf("defer refund event %d: %w", ev.Id, err)
		}
		return nil
	}

	facts, err := w.repo.Accounting().GetOrderFactsForPosting(ctx, p.OrderUUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return w.skipEvent(ctx, ev.Id, "order not found for order_refund")
		}
		return fmt.Errorf("order facts %s: %w", p.OrderUUID, err)
	}

	// items = facts.Items as-is; the builder takes the refunded quantity per line from
	// p.RefundedByItem. source_key is the resolved "uuid:seq" assigned at enqueue time.
	entry, buildErr := accounting.BuildOrderRefundEntry(*facts, p, facts.Items, ev.SourceKey, ev.OccurredAt)
	// A refund does not (re)write vat_regime — the sale already snapshotted it.
	return w.postOrDefer(ctx, ev, entry, buildErr, "", "")
}

// postOrDefer applies the builder outcome for an order event: sentinel waits defer the event,
// sentinel skips mark it processed with a disposition note, an unexpected builder error backs it off,
// and a clean build is written (entry + MarkEventProcessed) in one short Tx. It returns an error only
// when a mark/Tx write itself fails (infrastructure).
func (w *Worker) postOrDefer(ctx context.Context, ev entity.AcctEvent, entry entity.AcctJournalEntryInsert, buildErr error, orderUUID, vatRegime string) error {
	switch {
	case errors.Is(buildErr, accounting.ErrNotReady):
		// Stripe settlement not captured yet — defer; warn if it has waited too long (a stuck capture
		// pipeline, surfaced not masked). MarkEventFailed bumps the EVENT's attempts, not the worker's.
		if age := w.repo.Now().UTC().Sub(ev.OccurredAt); age > w.c.SettledWaitMax {
			slog.Default().WarnContext(ctx, "acctposting: order_paid settled base still pending past threshold",
				slog.String("source_key", ev.SourceKey),
				slog.Duration("age", age),
			)
		}
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, "settled pending", settledRetryInterval); err != nil {
			return fmt.Errorf("defer event %d: %w", ev.Id, err)
		}
		return nil

	case errors.Is(buildErr, accounting.ErrSkipNonEUR):
		return w.skipEvent(ctx, ev.Id, "non-eur non-stripe order, manual entry required")

	case errors.Is(buildErr, accounting.ErrDegenerateAmounts):
		return w.skipEvent(ctx, ev.Id, "degenerate amounts")

	case buildErr != nil:
		// Unexpected builder error (a bug): record it on the event with exponential backoff so it is
		// retried and visible, without failing the whole tick.
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, buildErr.Error(), eventBackoff(ev.Attempts)); err != nil {
			return fmt.Errorf("mark event %d failed: %w", ev.Id, err)
		}
		slog.Default().ErrorContext(ctx, "acctposting: build order entry failed",
			slog.String("source_key", ev.SourceKey),
			slog.String("err", buildErr.Error()),
		)
		return nil
	}

	// Clean build: create the entry, snapshot the VAT regime onto the order (order_paid only), and mark
	// the event processed — atomically (FAQ 7 — "entry exists, event pending" is impossible).
	txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, _, e := rep.Accounting().CreateJournalEntry(ctx, entry); e != nil {
			return e
		}
		if vatRegime != "" {
			if e := rep.Accounting().SetOrderVatRegime(ctx, orderUUID, vatRegime); e != nil {
				return e
			}
		}
		return rep.Accounting().MarkEventProcessed(ctx, ev.Id)
	})
	if txErr != nil {
		// A deterministic posting error (e.g. ErrAcctPeriodClosed on a late event — FAQ 12) is recorded
		// on the event to retry with backoff; if MarkEventFailed ALSO fails, that is infrastructure and
		// fails the tick.
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, txErr.Error(), eventBackoff(ev.Attempts)); err != nil {
			return fmt.Errorf("mark event %d failed after tx: %w", ev.Id, err)
		}
		slog.Default().ErrorContext(ctx, "acctposting: post order entry failed",
			slog.String("source_key", ev.SourceKey),
			slog.String("err", txErr.Error()),
		)
		return nil
	}
	return nil
}

// skipEvent records a terminal disposition on an event: MarkEventFailed writes the reason (last_error)
// first, then MarkEventProcessed sets processed_at WITHOUT clearing last_error, so the reason survives
// as the note the reconciliation report reads. A crash between the two re-runs the skip idempotently.
func (w *Worker) skipEvent(ctx context.Context, id int64, reason string) error {
	if err := w.repo.Accounting().MarkEventFailed(ctx, id, reason, 0); err != nil {
		return fmt.Errorf("record skip reason for event %d: %w", id, err)
	}
	if err := w.repo.Accounting().MarkEventProcessed(ctx, id); err != nil {
		return fmt.Errorf("mark event %d processed: %w", id, err)
	}
	return nil
}

// saleEntryExists reports whether the order_sale (S1) entry for orderUUID has been posted — an O(1)
// (source_type, source_key) unique-index lookup (uniq_acct_entry_source), so it no longer pages the
// ledger over a date window.
func (w *Worker) saleEntryExists(ctx context.Context, orderUUID string) (bool, error) {
	return w.repo.Accounting().EntryExistsBySource(ctx, entity.AcctSourceOrderSale, orderUUID)
}
