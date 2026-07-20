package accounting

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildOrderRefundEntry builds the order_refund journal entry (rule S2,
// docs/plan-accounting/04-posting-rules.md). It reverses the revenue side of a sale by the same EUR
// share k the sale used (precondition, checked by the worker: the S1 entry for this order exists,
// so k matches), and returns previously-costed items to inventory.
//
// sourceKey is the entry's idempotency key, "uuid:seq" — the refund sequence is assigned upstream
// when RefundOrder enqueues the outbox event (it cannot be recovered from the aggregate
// refunded_amount later), so the worker passes the resolved key in rather than the builder guessing
// it. occurredAt is the refund moment from the event. items carry the per-line unit cost; the
// refunded quantity per line comes from refund.RefundedByItem.
//
// The acquirer fee is deliberately not reversed (a Stripe refund does not return the fee). Returns
// the same skip/not-ready sentinels as the sale builder, plus ErrDegenerateAmounts when the EUR
// refund rounds to <= 0.
func BuildOrderRefundEntry(
	f entity.AcctOrderFacts,
	refund entity.AcctOrderRefundPayload,
	items []entity.AcctOrderItemFact,
	vd VatDecision,
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

	// R — EUR value of this refund (order-currency amount, shipping included, at the sale's share).
	rOrd := refund.RefundAmount
	r := rOrd.Mul(k).Round(2)
	if r.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}

	// Regime caveats (unknown destination, wdt without vat id, ...) travel onto the refund entry too.
	caveats := append([]string(nil), vd.Caveats...)

	// VAT portion of the refund, derived from the RESOLVED REGIME rate (mirrors S1's inclusive
	// extraction) and proportional to the refunded fraction — so a full refund reverses exactly what S1
	// posted to 2070 and a partial refund a proportional share, leaving no residual. export / wdt / none
	// post no VAT on the sale, so their refund reverses none — regardless of the sale-time vat_amount
	// snapshot (which the pre-regime code wrongly reversed, corrupting 2070/JPK/OSS). Same cascading
	// guard as S1: a VAT share >= the refund is not carved out.
	vatr := decimal.Zero
	if RegimeHasVAT(vd.Regime) && vd.RatePct.IsPositive() {
		refundRatio := rOrd.Div(f.TotalPrice)
		vatr = vatInclusive(g, vd.RatePct).Mul(refundRatio).Round(2)
	}
	if vatr.GreaterThanOrEqual(r) {
		vatr = decimal.Zero
		caveats = append(caveats, "vat exceeds refund; VAT reversal line dropped")
	}
	// Advisory cross-check against the sale-time snapshot (scaled to this refund), mirroring S1: a >1%
	// gap between the regime reversal and the snapshot is flagged, never re-posted.
	if vatr.IsPositive() && f.VatAmount.Valid {
		snap := f.VatAmount.Decimal.Mul(rOrd.Div(f.TotalPrice)).Mul(k).Round(2)
		if vatSnapshotDiffers(vatr, snap) {
			caveats = append(caveats, "vat snapshot mismatch")
		}
	}

	// NETr is the balancing remainder of the money returned.
	netr := r.Sub(vatr)

	lines := []entity.AcctJournalLineInsert{
		{AccountCode: Acc4040, Side: entity.AcctSideDebit, Amount: netr}, // contra-revenue
	}
	if vatr.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc2070, Side: entity.AcctSideDebit, Amount: vatr})
	}
	// Money back to the same account S1 debited (1030 / 1010 / 1040).
	lines = append(lines, entity.AcctJournalLineInsert{AccountCode: moneyAccount(f.PaymentMethodName), Side: entity.AcctSideCredit, Amount: r})

	// Stock returned to inventory — the ledger mirrors RefundOrder's RestoreStockForProductSizes.
	// Costed refunded lines only.
	cogsr, uncosted, unknownItems := refundCOGS(items, refund.RefundedByItem)
	if cogsr.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc1130, Side: entity.AcctSideDebit, Amount: cogsr},
			entity.AcctJournalLineInsert{AccountCode: Acc5050, Side: entity.AcctSideCredit, Amount: cogsr},
		)
	}
	if len(uncosted) > 0 {
		caveats = append(caveats, "COGS return understated; missing cost for product(s): "+joinProductIDs(uncosted))
	}
	if len(unknownItems) > 0 {
		caveats = append(caveats, "refund references order item(s) not on the order; COGS return understated: "+joinProductIDs(unknownItems))
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("order refund %s", refund.OrderUUID),
		SourceType:  entity.AcctSourceOrderRefund,
		SourceKey:   sourceKey,
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}

// refundCOGS sums cost x refunded-quantity over the costed refunded lines and returns the uncosted
// product ids. refundedByItem maps order_item.id -> refunded quantity; lines absent from it (or
// with a non-positive quantity) are not part of this refund.
func refundCOGS(items []entity.AcctOrderItemFact, refundedByItem map[int]int64) (decimal.Decimal, []int, []int) {
	total := decimal.Zero
	var uncosted []int
	known := make(map[int]bool, len(items))
	for _, it := range items {
		known[it.Id] = true
		qty, ok := refundedByItem[it.Id]
		if !ok || qty <= 0 {
			continue
		}
		// Clamp the refunded quantity to what was actually sold on the line: a refund can never return
		// more units than were sold, so a bad payload cannot over-credit COGS / inventory (A-4).
		if sold := it.Quantity.IntPart(); qty > sold {
			qty = sold
		}
		if qty <= 0 {
			continue
		}
		if it.UnitCost.Valid {
			total = total.Add(it.UnitCost.Decimal.Mul(decimal.NewFromInt(qty)))
		} else {
			uncosted = append(uncosted, it.ProductId)
		}
	}
	// Refunded order_item ids that are not on the order's lines cannot be costed — surface them instead
	// of dropping silently (the COGS return is understated by their cost).
	var unknownItems []int
	for id, qty := range refundedByItem {
		if qty > 0 && !known[id] {
			unknownItems = append(unknownItems, id)
		}
	}
	return total.Round(2), uncosted, unknownItems
}
