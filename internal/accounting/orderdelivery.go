package accounting

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Delivered revenue-recognition builders (phase 2, wave 2 — docs/plan-accounting-phase2/
// 02-wave2-delivered.md + 11-wave2-synthesis.md). Post-cutover Stripe orders recognise revenue on
// DELIVERY, not payment, via a three-entry chain keyed by the order UUID:
//
//	payment   → order_prepayment      Dr money G / Cr 2070 VAT / Cr 2090 (G−VAT)      [no COGS]
//	shipped   → order_transit         Dr 1140 / Cr 1130  at Σ order-time cost
//	delivered → order_delivered_sale  Dr 2090 / Cr revenue NET + Cr 4110 SHIP ; Dr 5010 / Cr 1140
//
// The VAT tax point stays at PAYMENT (07 §7.4.5): 2070 is credited once, in the prepayment entry, and
// delivery never touches VAT. After delivery the chain's cumulative movement equals the old order_sale
// entry (2090 and 1140 both net to zero), so a post-delivery refund reuses BuildOrderRefundEntry (S2)
// unchanged; a pre-delivery refund unwinds the prepayment (BuildOrderPreDeliveredRefundEntry).
//
// All builders keep the two package invariants: round to 2dp only on final line amounts, and make the
// last line a balancing difference (never an independent product), so ValidateBalanced holds by
// construction.

// BuildOrderPrepaymentEntry builds the order_prepayment entry (S1n): money in, VAT recognised now, the
// remainder parked as a customer-prepayment LIABILITY on 2090 until delivery. No revenue and no COGS —
// those land at delivered. VAT uses the resolved regime rate (identical to S1); the fee is booked as in
// S1. Returns the same ErrNotReady / ErrSkipNonEUR / ErrDegenerateAmounts sentinels as the sale builder.
func BuildOrderPrepaymentEntry(f entity.AcctOrderFacts, vd VatDecision, occurredAt time.Time) (entity.AcctJournalEntryInsert, error) {
	g, err := grossEUR(f)
	if err != nil {
		return entity.AcctJournalEntryInsert{}, err
	}
	if f.TotalPrice.Sign() <= 0 || g.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}
	k := g.Div(f.TotalPrice)

	caveats := append([]string(nil), vd.Caveats...)

	// VAT from the regime rate (inclusive from gross); export / wdt / none post no VAT line. Same guards
	// and snapshot cross-check as BuildOrderSaleEntry.
	vat := decimal.Zero
	if RegimeHasVAT(vd.Regime) && vd.RatePct.IsPositive() {
		vat = vatInclusive(g, vd.RatePct)
	}
	if vat.GreaterThanOrEqual(g) {
		vat = decimal.Zero
		caveats = append(caveats, "vat exceeds gross; VAT line dropped")
	}
	if vat.IsPositive() && f.VatAmount.Valid {
		if snap := f.VatAmount.Decimal.Mul(k); vatSnapshotDiffers(vat, snap) {
			caveats = append(caveats, vatSnapshotCaveat(vat, snap))
		}
	}

	// prepay is the balancing remainder — the amount delivery will drain from 2090 (= NET + SHIP).
	prepay := g.Sub(vat)

	lines := []entity.AcctJournalLineInsert{
		{AccountCode: moneyAccount(f.PaymentMethodName), Side: entity.AcctSideDebit, Amount: g},
	}
	if vat.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc2070, Side: entity.AcctSideCredit, Amount: vat})
	}
	lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc2090, Side: entity.AcctSideCredit, Amount: prepay})

	// Acquirer fee, exactly as S1: Dr 6050 / Cr the same money account. Post-cutover routing is
	// Stripe-only (see the worker), so this is the captured 1030 fee, but the general form is kept.
	if fee := orderFee(f, g); fee.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc6050, Side: entity.AcctSideDebit, Amount: fee},
			entity.AcctJournalLineInsert{AccountCode: moneyAccount(f.PaymentMethodName), Side: entity.AcctSideCredit, Amount: fee},
		)
		if !isStripe(f.PaymentMethodName) {
			caveats = append(caveats, "fee estimated from method model")
		}
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("order prepayment %s", f.UUID),
		SourceType:  entity.AcctSourceOrderPrepayment,
		SourceKey:   f.UUID,
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}

// BuildOrderTransitEntry builds the order_transit entry (shipped): finished goods leave 1130 for 1140
// Inventory in Transit at Σ order-time cost (the same snapshot COGS uses — CLAUDE.md sales-margin
// chain, never the live warehouse). ErrSkipEmpty when nothing is costed (the worker records the event
// processed). occurredAt is the ship instant.
func BuildOrderTransitEntry(f entity.AcctOrderFacts, occurredAt time.Time) (entity.AcctJournalEntryInsert, error) {
	cost, uncosted := saleCOGS(f.Items)
	if cost.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrSkipEmpty
	}
	lines := []entity.AcctJournalLineInsert{
		{AccountCode: Acc1140, Side: entity.AcctSideDebit, Amount: cost},
		{AccountCode: Acc1130, Side: entity.AcctSideCredit, Amount: cost},
	}
	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("order shipped to transit %s", f.UUID),
		SourceType:  entity.AcctSourceOrderTransit,
		SourceKey:   f.UUID,
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	if len(uncosted) > 0 {
		applyCaveats(&entry, []string{"transit understated; missing cost for product(s): " + joinProductIDs(uncosted)})
	}
	return entry, nil
}

// BuildOrderDeliveredSaleEntry builds the order_delivered_sale entry (S1d): it drains the prepayment
// into NET revenue + 4110 shipping and recognises COGS. VAT is NOT touched (already on 2070 from S1n).
//
// It drains the EXACT posted balances rather than recomputing G−VAT: prepaymentNet is the order's
// remaining 2090 balance and transitCost its remaining 1140 balance, both read from the ledger by the
// worker (GetOrderPostingState). This guarantees 2090 and 1140 drain to zero even if the vat_rate was
// edited between payment and delivery, or a partial pre-delivery refund already reduced the prepayment
// (synthesis D1 — kills the drift both design passes flagged). The worker guarantees a transit entry
// exists first (posting a synthetic one for a Confirmed→Delivered-direct order, D2), so transitCost is
// the authoritative outstanding 1140, never a fresh warehouse valuation.
func BuildOrderDeliveredSaleEntry(f entity.AcctOrderFacts, prepaymentNet, transitCost decimal.Decimal, occurredAt time.Time) (entity.AcctJournalEntryInsert, error) {
	if prepaymentNet.Sign() <= 0 {
		// Nothing left to recognise (a fully pre-refunded prepayment); the worker records it processed.
		return entity.AcctJournalEntryInsert{}, ErrSkipEmpty
	}
	g, err := grossEUR(f)
	if err != nil {
		return entity.AcctJournalEntryInsert{}, err
	}
	if f.TotalPrice.Sign() <= 0 || g.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}
	k := g.Div(f.TotalPrice)

	var caveats []string

	// Shipping is proportional (same as S1). It cannot claim the whole prepayment remainder (that would
	// zero out revenue) — guard drops it with a caveat, mirroring S1's post-VAT guard.
	ship := decimal.Zero
	if !(f.FreeShipping.Valid && f.FreeShipping.Bool) && f.ShipmentCost.Valid {
		ship = f.ShipmentCost.Decimal.Mul(k).Round(2)
	}
	if ship.GreaterThanOrEqual(prepaymentNet) {
		ship = decimal.Zero
		caveats = append(caveats, "shipping exceeds prepayment remainder; shipping line dropped")
	}

	// revenue is the balancing remainder of the drained prepayment.
	revenue := prepaymentNet.Sub(ship)

	// Revenue credit, optionally split into a full-price credit + a 4030 Discounts contra (3.3) — the
	// SAME shared split as S1, so a delivered-chain promo order also books its discount analytics. The
	// split preserves the entry balance (2090 still drains exactly prepaymentNet) and the P&L total.
	revLines, discCaveat := revenueLines(saleRevenueAccount(f), revenue, f.PromoDiscountPct)
	if discCaveat != "" {
		caveats = append(caveats, discCaveat)
	}

	lines := []entity.AcctJournalLineInsert{
		{AccountCode: Acc2090, Side: entity.AcctSideDebit, Amount: prepaymentNet},
	}
	lines = append(lines, revLines...)
	if ship.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc4110, Side: entity.AcctSideCredit, Amount: ship})
	}
	// COGS drains the posted transit balance. Zero only when the order was never costed (the transit
	// builder already caveated that) — then no COGS line, consistent with saleCOGS returning zero.
	if transitCost.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc5010, Side: entity.AcctSideDebit, Amount: transitCost},
			entity.AcctJournalLineInsert{AccountCode: Acc1140, Side: entity.AcctSideCredit, Amount: transitCost},
		)
	} else {
		caveats = append(caveats, "COGS not recognised; no costed transit balance for order")
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("order delivered sale %s", f.UUID),
		SourceType:  entity.AcctSourceOrderDeliveredSale,
		SourceKey:   f.UUID,
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}

// BuildOrderPreDeliveredRefundEntry builds an order_refund entry for a NEW-CHAIN order refunded BEFORE
// delivery: revenue was never recognised, so — unlike S2 — there is no 4040 contra-revenue and no
// 5010/5050 COGS reversal. It unwinds the 2090 prepayment + its 2070 VAT share (proportional to the
// refund, exactly as S2 splits VAT, so a full refund reverses the whole prepayment and a partial one a
// proportional share) and returns the money. If the order had shipped (transitPosted), the transit
// stock is returned Dr 1130 / Cr 1140 (it never became COGS). A residual cent from independent
// rounding across multiple partial refunds is drained by S1d's exact 2090 drain at delivery, or — for
// an order that never delivers — surfaced by the prepayments reconciliation block (same accepted stance
// as S2's contra-revenue rounding).
//
// sourceKey is the "uuid:seq" idempotency key the worker resolves. vd mirrors S1n's regime.
func BuildOrderPreDeliveredRefundEntry(
	f entity.AcctOrderFacts,
	refund entity.AcctOrderRefundPayload,
	items []entity.AcctOrderItemFact,
	vd VatDecision,
	transitPosted bool,
	sourceKey string,
	occurredAt time.Time,
) (entity.AcctJournalEntryInsert, error) {
	g, err := grossEUR(f)
	if err != nil {
		return entity.AcctJournalEntryInsert{}, err
	}
	if f.TotalPrice.Sign() <= 0 || g.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}
	k := g.Div(f.TotalPrice)

	rOrd := refund.RefundAmount
	r := rOrd.Mul(k).Round(2)
	if r.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}

	caveats := append([]string(nil), vd.Caveats...)

	// VAT portion of the refund — regime rate, proportional to the refunded fraction (mirrors S2).
	vatr := decimal.Zero
	if RegimeHasVAT(vd.Regime) && vd.RatePct.IsPositive() {
		vatr = vatInclusive(g, vd.RatePct).Mul(rOrd.Div(f.TotalPrice)).Round(2)
	}
	if vatr.GreaterThanOrEqual(r) {
		vatr = decimal.Zero
		caveats = append(caveats, "vat exceeds refund; VAT reversal line dropped")
	}
	if vatr.IsPositive() && f.VatAmount.Valid {
		snap := f.VatAmount.Decimal.Mul(rOrd.Div(f.TotalPrice)).Mul(k).Round(2)
		if vatSnapshotDiffers(vatr, snap) {
			caveats = append(caveats, vatSnapshotCaveat(vatr, snap))
		}
	}

	// netBack is the balancing remainder unwound from the 2090 prepayment liability.
	netBack := r.Sub(vatr)

	lines := []entity.AcctJournalLineInsert{
		{AccountCode: Acc2090, Side: entity.AcctSideDebit, Amount: netBack},
	}
	if vatr.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc2070, Side: entity.AcctSideDebit, Amount: vatr})
	}
	lines = append(lines, entity.AcctJournalLineInsert{AccountCode: moneyAccount(f.PaymentMethodName), Side: entity.AcctSideCredit, Amount: r})

	// Return transit stock to finished goods only if it had shipped (stock sits in 1140). If it never
	// shipped, S1n moved no stock, so there is nothing to return.
	if transitPosted {
		cogsr, uncosted, unknownItems := refundCOGS(items, refund.RefundedByItem)
		if cogsr.IsPositive() {
			lines = append(lines,
				entity.AcctJournalLineInsert{AccountCode: Acc1130, Side: entity.AcctSideDebit, Amount: cogsr},
				entity.AcctJournalLineInsert{AccountCode: Acc1140, Side: entity.AcctSideCredit, Amount: cogsr},
			)
		}
		if len(uncosted) > 0 {
			caveats = append(caveats, "transit return understated; missing cost for product(s): "+joinProductIDs(uncosted))
		}
		if len(unknownItems) > 0 {
			caveats = append(caveats, "refund references order item(s) not on the order; transit return understated: "+joinProductIDs(unknownItems))
		}
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("order prepayment refund %s", refund.OrderUUID),
		SourceType:  entity.AcctSourceOrderRefund,
		SourceKey:   sourceKey,
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}
