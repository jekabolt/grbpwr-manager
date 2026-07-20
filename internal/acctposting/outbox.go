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
	"github.com/shopspring/decimal"
)

// settledRetryInterval is the fixed defer for a Stripe order_paid whose settlement has not arrived
// (ErrNotReady) or a refund whose sale is not posted yet (awaiting sale posting) — a short, constant
// wait, not exponential: the fact is expected to become ready soon.
const settledRetryInterval = 5 * time.Minute

// maxDeadLetterAttempts / maxOrphanRefundAttempts bound the retry/defer loops so an event that will
// never post — a deterministic build/post bug (B-5) or a refund whose sale never posts (H-2) — is
// flagged needs_review after the cap instead of retrying forever and soft-locking ClosePeriod.
const (
	maxDeadLetterAttempts   = 12
	maxOrphanRefundAttempts = 24
)

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
	case entity.AcctEventOrderShipped:
		return w.processOrderShipped(ctx, ev)
	case entity.AcctEventOrderDelivered:
		return w.processOrderDelivered(ctx, ev)
	case entity.AcctEventOrderDispute:
		return w.processOrderDispute(ctx, ev)
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
		// H-1: a missing/non-positive vat_rate is RECOVERABLE (the operator adds the rate) — defer
		// (retryable) instead of a terminal skip, so the order stays pending and blocks ClosePeriod
		// (gate #2) rather than closing the month with a silent, invisible revenue hole.
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, skipReason, settledRetryInterval); err != nil {
			return fmt.Errorf("defer vat-rate-missing event %d: %w", ev.Id, err)
		}
		return nil
	}

	// Cutover (phase 2, wave 2): a post-cutover Stripe order recognises revenue on DELIVERY — S1n posts
	// only the prepayment now (money in, VAT, 2090 liability); sale/COGS land at order_delivered_sale.
	// Custom/cash and pre-cutover orders keep the old confirmed-time S1. The resolved regime is
	// snapshotted onto the order in the same Tx that posts the entry (§1.3).
	if w.usesDeliveredPolicy(facts, ev.OccurredAt) {
		entry, buildErr := accounting.BuildOrderPrepaymentEntry(*facts, vd, ev.OccurredAt)
		return w.postOrDefer(ctx, ev, []entity.AcctJournalEntryInsert{entry}, buildErr, facts.UUID, string(vd.Regime))
	}
	entry, buildErr := accounting.BuildOrderSaleEntry(*facts, vd, ev.OccurredAt)
	return w.postOrDefer(ctx, ev, []entity.AcctJournalEntryInsert{entry}, buildErr, facts.UUID, string(vd.Regime))
}

// usesDeliveredPolicy decides whether an order recognises revenue on delivery (wave 2): the feature must
// be armed (accounting.delivered_recognition_from set), the order must be a Stripe/storefront order, and
// it must have been PAID on or after the cutover (paidAt is the order_paid event's occurred_at). Custom/
// cash (born-Confirmed) and pre-cutover orders keep the old confirmed-time S1 forever (07 §7.4.4). The
// boundary matches the phase-1 start-date rule: paid exactly at the cutover instant is new policy.
func (w *Worker) usesDeliveredPolicy(f *entity.AcctOrderFacts, paidAt time.Time) bool {
	if w.deliveredRecognitionFrom.IsZero() {
		return false
	}
	if f.PaymentMethodName != entity.CARD && f.PaymentMethodName != entity.CARD_TEST {
		return false
	}
	return !paidAt.Before(w.deliveredRecognitionFrom)
}

// processOrderShipped posts the order_transit entry (wave 2): finished goods move 1130 → 1140 at
// order-time cost. It runs only for a new-chain order (order_prepayment posted); an old/custom order is
// a definitive "pre-policy order" skip, and a new order whose prepayment has not posted yet defers.
func (w *Worker) processOrderShipped(ctx context.Context, ev entity.AcctEvent) error {
	var p entity.AcctOrderShippedPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return w.skipEvent(ctx, ev.Id, "invalid order_shipped payload: "+err.Error())
	}
	st, err := w.repo.Accounting().GetOrderPostingState(ctx, p.OrderUUID)
	if err != nil {
		return fmt.Errorf("order posting state %s: %w", p.OrderUUID, err)
	}
	if !st.Prepayment {
		return w.skipShippedDelivered(ctx, ev, st)
	}
	facts, err := w.repo.Accounting().GetOrderFactsForPosting(ctx, p.OrderUUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return w.skipEvent(ctx, ev.Id, "order not found for order_shipped")
		}
		return fmt.Errorf("order facts %s: %w", p.OrderUUID, err)
	}
	entry, buildErr := accounting.BuildOrderTransitEntry(*facts, ev.OccurredAt)
	return w.postOrDefer(ctx, ev, []entity.AcctJournalEntryInsert{entry}, buildErr, "", "")
}

// processOrderDelivered posts the order_delivered_sale entry (S1d, wave 2): it drains the prepayment
// into revenue + 4110 shipping and recognises COGS from the posted transit. It runs only for a new-chain
// order; if the order was delivered directly from Confirmed (no shipped event, legal at
// lifecycle.go:393-400), it synthesizes the missing transit entry first — both entries in one Tx — so
// S1d's exact 1140 drain has its debit (synthesis D2). The EXACT posted 2090 / 1140 balances are passed
// to the builder so a vat-rate edit or a partial pre-delivery refund cannot leave a residual (D1).
func (w *Worker) processOrderDelivered(ctx context.Context, ev entity.AcctEvent) error {
	var p entity.AcctOrderDeliveredPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return w.skipEvent(ctx, ev.Id, "invalid order_delivered payload: "+err.Error())
	}
	st, err := w.repo.Accounting().GetOrderPostingState(ctx, p.OrderUUID)
	if err != nil {
		return fmt.Errorf("order posting state %s: %w", p.OrderUUID, err)
	}
	if !st.Prepayment {
		return w.skipShippedDelivered(ctx, ev, st)
	}
	if st.DeliveredSale {
		// Replay after the sale already posted — the CreateJournalEntry idempotency would no-op it anyway;
		// record the event processed without a second build.
		return w.skipEvent(ctx, ev.Id, "delivered sale already posted")
	}
	facts, err := w.repo.Accounting().GetOrderFactsForPosting(ctx, p.OrderUUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return w.skipEvent(ctx, ev.Id, "order not found for order_delivered")
		}
		return fmt.Errorf("order facts %s: %w", p.OrderUUID, err)
	}

	var entries []entity.AcctJournalEntryInsert
	transitCost := st.Remaining1140
	if !st.Transit {
		// Confirmed→Delivered direct: synthesize the transit leg so S1d can drain 1140 (D2). Its cost is
		// the amount S1d then drains.
		tEntry, tErr := accounting.BuildOrderTransitEntry(*facts, ev.OccurredAt)
		switch {
		case tErr == nil:
			entries = append(entries, tEntry)
			transitCost = lineAmount(tEntry, accounting.Acc1140, entity.AcctSideDebit)
		case errors.Is(tErr, accounting.ErrSkipEmpty):
			transitCost = decimal.Zero // uncosted order — S1d recognises revenue with no COGS (+ caveat)
		default:
			return w.postOrDefer(ctx, ev, nil, tErr, "", "") // ErrNotReady / degenerate / bug
		}
	}

	sEntry, sErr := accounting.BuildOrderDeliveredSaleEntry(*facts, st.Remaining2090, transitCost, ev.OccurredAt)
	if sErr != nil {
		// ErrSkipEmpty (prepayment fully refunded before delivery) → skip; else the standard handler.
		return w.postOrDefer(ctx, ev, nil, sErr, "", "")
	}
	entries = append(entries, sEntry)
	return w.postOrDefer(ctx, ev, entries, nil, "", "")
}

// skipShippedDelivered disposes a shipped/delivered event on an order with no prepayment entry: an old
// chain order (order_sale exists) is a definitive "pre-policy order" skip — its stock and revenue were
// posted at confirmed; otherwise the order_paid event simply has not posted yet, so defer, and after the
// cap skip (a pre-policy/manual order that will never post a prepayment).
func (w *Worker) skipShippedDelivered(ctx context.Context, ev entity.AcctEvent, st entity.AcctOrderPostingState) error {
	if st.LegacySale {
		return w.skipEvent(ctx, ev.Id, "pre-policy order")
	}
	if w.deliveredRecognitionFrom.IsZero() {
		// Feature off: this order has no prepayment (skipShippedDelivered is only reached when
		// !st.Prepayment) and never will, so a shipped/delivered event can never post a transit/delivered
		// leg. Skip immediately instead of deferring — otherwise a non-EUR/pending order would block
		// ClosePeriod for ~2h before the cap. Keeps the feature-off path truly inert.
		return w.skipEvent(ctx, ev.Id, "delivered recognition off")
	}
	if ev.Attempts >= maxOrphanRefundAttempts {
		return w.skipEvent(ctx, ev.Id, "no prepayment posted (pre-policy/manual order)")
	}
	if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, "awaiting prepayment posting", settledRetryInterval); err != nil {
		return fmt.Errorf("defer shipped/delivered event %d: %w", ev.Id, err)
	}
	return nil
}

// lineAmount returns the amount of the first line in entry matching the account code and side (zero if
// none) — used to read a synthesized transit entry's 1140 debit so the delivered sale drains exactly it.
func lineAmount(entry entity.AcctJournalEntryInsert, code string, side entity.AcctSide) decimal.Decimal {
	for _, l := range entry.Lines {
		if l.AccountCode == code && l.Side == side {
			return l.Amount
		}
	}
	return decimal.Zero
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
	return w.vatDecisionForRegime(ctx, regime, caveats, facts.DestCountry)
}

// refundVatDecision reverses a refund at the SAME regime the sale snapshotted onto the order
// (customer_order.vat_regime), not a fresh resolution — so a later change to the resolver inputs (the
// ship-from origin config, or a buyer VAT id added after the sale) cannot make the reversal diverge
// from what S1 actually posted to 2070 (which would re-introduce the C-1 residual this fixes). It falls
// back to re-resolution only for a legacy order that predates the regime snapshot (vat_regime NULL).
// (The rate itself is not persisted, so an operator editing the vat_rate row between sale and refund
// can still drift; that surfaces as the advisory "vat snapshot mismatch" caveat — tracked separately.)
func (w *Worker) refundVatDecision(ctx context.Context, facts *entity.AcctOrderFacts) (accounting.VatDecision, string, error) {
	regime := strings.TrimSpace(facts.VatRegime.String)
	if !facts.VatRegime.Valid || regime == "" {
		return w.resolveVatDecision(ctx, facts)
	}
	return w.vatDecisionForRegime(ctx, entity.VatRegime(regime), nil, facts.DestCountry)
}

// vatDecisionForRegime looks up a resolved regime's vat_rate (07 §7.4.14): a VAT-bearing regime with
// no configured rate returns a non-empty skipReason; the error return is reserved for infra failures.
func (w *Worker) vatDecisionForRegime(ctx context.Context, regime entity.VatRegime, caveats []string, destCountry string) (accounting.VatDecision, string, error) {
	vd := accounting.VatDecision{Regime: regime, Caveats: caveats}
	if !accounting.RegimeHasVAT(regime) {
		return vd, "", nil
	}

	rateCountry := accounting.RegimeRateCountry(regime, destCountry, w.c.OriginCountry)
	rates, err := w.repo.Accounting().GetVatRatesFor(ctx, []string{rateCountry})
	if err != nil {
		return vd, "", fmt.Errorf("get vat rate %s: %w", rateCountry, err)
	}
	rate, ok := rates[strings.ToUpper(strings.TrimSpace(rateCountry))]
	if !ok {
		return vd, "vat rate missing for " + rateCountry, nil
	}
	if !rate.IsPositive() {
		// A 0 (or negative) rate for a VAT-bearing regime is a config error, not a genuine 0%: posting a
		// silent 0% VAT would understate the liability. Skip with an alert, same as a missing rate (A-7).
		return vd, "vat rate is non-positive for " + rateCountry, nil
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

	st, err := w.repo.Accounting().GetOrderPostingState(ctx, p.OrderUUID)
	if err != nil {
		return fmt.Errorf("order posting state %s: %w", p.OrderUUID, err)
	}

	// The paid event posts exactly one recognition entry (old order_sale OR new order_prepayment), so
	// both existing is an impossible mixed chain — flag rather than guess which to reverse.
	if st.LegacySale && st.Prepayment {
		return w.needsReview(ctx, ev.Id, "mixed old+new recognition chain, manual entry required")
	}

	// Neither recognition entry is posted yet: the refund's EUR share k must match the entry it reverses,
	// so defer (H-2) — a sale that will NEVER post (pre-cutover / non-EUR) dead-letters after the cap.
	if !st.LegacySale && !st.Prepayment {
		if ev.Attempts >= maxOrphanRefundAttempts {
			return w.needsReview(ctx, ev.Id, "orphan refund: recognition not posted, manual entry required")
		}
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, "awaiting sale posting", settledRetryInterval); err != nil {
			return fmt.Errorf("defer refund event %d: %w", ev.Id, err)
		}
		return nil
	}

	// A new-chain order that is operationally delivered (order_delivered event enqueued) but whose S1d has
	// not posted yet must DEFER, not take the pre-delivery branch — otherwise it would unwind a 2090 that
	// S1d is about to drain and misclassify a post-delivery return as a prepayment reversal (synthesis
	// D8). It clears as soon as the delivered event posts S1d.
	if st.Prepayment && !st.DeliveredSale && st.DeliveredEvent {
		if ev.Attempts >= maxOrphanRefundAttempts {
			return w.needsReview(ctx, ev.Id, "refund awaiting delivered sale posting, manual entry required")
		}
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, "awaiting delivered sale posting", settledRetryInterval); err != nil {
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

	// Reverse at the SAME VAT regime the sale snapshotted onto the order, so the refund matches exactly
	// what was posted to 2070 (S2/pre-delivery mirror S1/S1n), even if the resolver inputs changed after.
	vd, skipReason, err := w.refundVatDecision(ctx, facts)
	if err != nil {
		return fmt.Errorf("resolve refund vat %s: %w", p.OrderUUID, err)
	}
	if skipReason != "" {
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, skipReason, settledRetryInterval); err != nil {
			return fmt.Errorf("defer refund event %d: %w", ev.Id, err)
		}
		return nil
	}

	// source_key is the resolved "uuid:seq" assigned at enqueue time; RefundedByItem drives COGS return.
	var entry entity.AcctJournalEntryInsert
	var buildErr error
	if st.LegacySale || st.DeliveredSale {
		// Old chain, or a new chain already delivered (cumulative state == old S1): the existing S2 reversal.
		entry, buildErr = accounting.BuildOrderRefundEntry(*facts, p, facts.Items, vd, ev.SourceKey, ev.OccurredAt)
	} else {
		// New chain, not yet delivered: unwind the 2090 prepayment (+ transit stock if it had shipped).
		entry, buildErr = accounting.BuildOrderPreDeliveredRefundEntry(*facts, p, facts.Items, vd, st.Transit, ev.SourceKey, ev.OccurredAt)
	}
	// A refund does not (re)write vat_regime — recognition already snapshotted it.
	return w.postOrDefer(ctx, ev, []entity.AcctJournalEntryInsert{entry}, buildErr, "", "")
}

// processOrderDispute posts a Stripe chargeback (phase 2, wave 4 — §4.3). The opened event books the
// dispute (Dr 4040 + Dr 6050 / Cr 1030); a closed-WON event reverses that entry (append-only), a
// closed-LOST one leaves it standing. The disputed amount and fee come from the payload (Stripe balance
// transactions, EUR); if Stripe did not report the amount, the order's settled base is used as a fallback.
func (w *Worker) processOrderDispute(ctx context.Context, ev entity.AcctEvent) error {
	var p entity.AcctOrderDisputePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return w.skipEvent(ctx, ev.Id, "invalid order_dispute payload: "+err.Error())
	}

	if p.Closed {
		return w.processDisputeClosed(ctx, ev, p)
	}

	// Opened: book the chargeback. Prefer the balance-transaction EUR amount; fall back to the order's
	// settled base when Stripe has not (yet) reported it.
	amount := p.AmountBase
	fallbackUsed := false
	if amount.Sign() <= 0 {
		facts, err := w.repo.Accounting().GetOrderFactsForPosting(ctx, p.OrderUUID)
		switch {
		case err == nil && facts.TotalSettledBase.Valid && facts.TotalSettledBase.Decimal.IsPositive():
			amount = facts.TotalSettledBase.Decimal
			fallbackUsed = true
		case err == nil, errors.Is(err, sql.ErrNoRows):
			// No usable amount from either source — degenerate; postOrDefer flags it for manual review.
		default:
			return fmt.Errorf("order facts %s: %w", p.OrderUUID, err)
		}
	}

	entry, buildErr := accounting.BuildDisputeEntry(amount, p.FeeBase, p.FeeKnown, p.OrderUUID, p.DisputeID, p.Currency, ev.OccurredAt)
	if buildErr == nil && fallbackUsed {
		appendCaveat(&entry, "disputed amount taken from order settled base (Stripe amount unavailable)")
	}
	return w.postOrDefer(ctx, ev, []entity.AcctJournalEntryInsert{entry}, buildErr, "", "")
}

// processDisputeClosed disposes a closed chargeback: a loss leaves the open entry standing (the money
// stayed gone); a win reverses it in one Tx with the event mark. A missing open entry (the dispute was
// never booked — pre-start / no amount) or an already-reversed one is a benign skip.
func (w *Worker) processDisputeClosed(ctx context.Context, ev entity.AcctEvent, p entity.AcctOrderDisputePayload) error {
	if !p.Won {
		return w.skipEvent(ctx, ev.Id, "dispute lost; open entry stands")
	}
	open, err := w.repo.Accounting().GetEntryBySource(ctx, entity.AcctSourceOrderDispute, "dispute:"+p.DisputeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The created event may not have posted the open entry yet — Stripe can deliver dispute.closed
			// before dispute.created on out-of-order retries. DEFER with backoff so the won reversal
			// self-heals once the open entry exists; a terminal skip here would leave a recovered
			// chargeback permanently booked as a contra-revenue + fee loss (MED-1).
			if ev.Attempts >= maxDeadLetterAttempts {
				return w.needsReview(ctx, ev.Id, "dispute won but open dispute entry never posted, manual entry required")
			}
			if e := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, "dispute won; awaiting open dispute entry", eventBackoff(ev.Attempts)); e != nil {
				return fmt.Errorf("defer dispute-won event %d: %w", ev.Id, e)
			}
			return nil
		}
		return fmt.Errorf("get open dispute entry %s: %w", p.DisputeID, err)
	}
	if open.ReversedBy.Valid {
		return w.skipEvent(ctx, ev.Id, "dispute won; open entry already reversed")
	}
	txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, e := rep.Accounting().ReverseJournalEntry(ctx, open.Id, "dispute "+p.DisputeID+" won", createdBySystemUser); e != nil {
			return e
		}
		return rep.Accounting().MarkEventProcessed(ctx, ev.Id)
	})
	if txErr != nil {
		if errors.Is(txErr, entity.ErrAcctAlreadyReversed) {
			return w.skipEvent(ctx, ev.Id, "dispute won; open entry already reversed")
		}
		// A closed period (reopen resolves it) or another posting error — retry with backoff.
		if err := w.repo.Accounting().MarkEventFailed(ctx, ev.Id, txErr.Error(), eventBackoff(ev.Attempts)); err != nil {
			return fmt.Errorf("mark dispute-close event %d failed: %w", ev.Id, err)
		}
		return nil
	}
	return nil
}

// createdBySystemUser stamps automated reversals (matches acct_journal_entry.created_by default).
const createdBySystemUser = "system"

// postOrDefer applies the builder outcome for an order event: sentinel waits defer the event, sentinel
// skips mark it processed with a disposition note, an unexpected builder error backs it off, and a clean
// build writes ALL entries (a delivered event posts a synthesized transit + the delivered sale) plus
// MarkEventProcessed in one short Tx. It returns an error only when a mark/Tx write itself fails
// (infrastructure). buildErr, when set, is the single build error to dispatch on (entries is ignored).
func (w *Worker) postOrDefer(ctx context.Context, ev entity.AcctEvent, entries []entity.AcctJournalEntryInsert, buildErr error, orderUUID, vatRegime string) error {
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
		// H-1: cannot auto-post (needs a manual entry) — flag for review (visible + blocks close)
		// instead of a silent terminal skip.
		return w.needsReview(ctx, ev.Id, "non-eur non-stripe order, manual entry required")

	case errors.Is(buildErr, accounting.ErrDegenerateAmounts):
		return w.needsReview(ctx, ev.Id, "degenerate amounts, manual entry required")

	case errors.Is(buildErr, accounting.ErrSkipEmpty):
		// A transit with nothing costed, or a delivered sale whose prepayment was fully refunded before
		// delivery — nothing to post; record the event processed with the disposition note.
		return w.skipEvent(ctx, ev.Id, "nothing to post (uncosted or fully refunded)")

	case buildErr != nil:
		// Unexpected builder error (a bug): retry with exponential backoff so it is visible without
		// failing the tick. B-5: a deterministic bug that never succeeds would retry forever and block
		// close, so after the cap flag it for review (dead-letter) instead.
		if ev.Attempts >= maxDeadLetterAttempts {
			return w.needsReview(ctx, ev.Id, "dead-letter (build): "+buildErr.Error())
		}
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
		for i := range entries {
			if _, _, e := rep.Accounting().CreateJournalEntry(ctx, entries[i]); e != nil {
				return e
			}
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
		// fails the tick. B-5: cap the retries so a permanent error dead-letters (flagged for review)
		// instead of blocking close forever — EXCEPT ErrAcctPeriodClosed, which clears on a reopen.
		if !errors.Is(txErr, entity.ErrAcctPeriodClosed) && ev.Attempts >= maxDeadLetterAttempts {
			return w.needsReview(ctx, ev.Id, "dead-letter (post): "+txErr.Error())
		}
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

// needsReview terminally disposes an event that cannot post automatically and flags it for an operator
// (H-1 manual entry, H-2 orphan refund, B-5 dead-letter): MarkEventNeedsReview sets processed_at (out
// of the pending window, retries stop) + needs_review + the reason. ClosePeriod blocks the event's
// month until it is reprocessed (after the cause is fixed) or resolved (posted manually).
func (w *Worker) needsReview(ctx context.Context, id int64, reason string) error {
	if err := w.repo.Accounting().MarkEventNeedsReview(ctx, id, reason); err != nil {
		return fmt.Errorf("flag event %d for review: %w", id, err)
	}
	return nil
}
