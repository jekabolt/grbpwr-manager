package accounting

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildOrderSaleEntry builds the order_sale journal entry for a paid order (rule S1,
// docs/plan-accounting/04-posting-rules.md). occurredAt is the recognition moment — the order's
// payment confirmation, taken from the outbox event, not the placed date.
//
// It derives one EUR share k = G/total_price and applies it to VAT and shipping; NET is the
// balancing remainder, so Σ credit on the revenue side always equals G exactly. COGS is a separate
// balanced pair from the order-time cost snapshot. Degenerate-amount guards run in the fixed order
// of the spec (gross -> VAT -> shipping) and each collapses a bad component to zero with a caveat
// rather than emitting an invalid line.
//
// Returns ErrNotReady (Stripe settlement pending — retry), ErrSkipNonEUR (non-Stripe non-EUR — book
// manually) or ErrDegenerateAmounts (non-positive total/gross — skip) when no entry can be built.
func BuildOrderSaleEntry(f entity.AcctOrderFacts, occurredAt time.Time) (entity.AcctJournalEntryInsert, error) {
	g, err := grossEUR(f)
	if err != nil {
		return entity.AcctJournalEntryInsert{}, err
	}
	// Guard 1: degenerate amounts. Also protects the k division below from a zero/negative total.
	if f.TotalPrice.Sign() <= 0 || g.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}
	k := g.Div(f.TotalPrice)

	var caveats []string

	// VAT (inclusive), proportional. Guard 2: a snapshot larger than gross is not carved out.
	vat := decimal.Zero
	if f.VatAmount.Valid {
		vat = f.VatAmount.Decimal.Mul(k).Round(2)
	} else {
		caveats = append(caveats, "vat_amount is null; VAT not separated from revenue")
	}
	if vat.GreaterThanOrEqual(g) {
		vat = decimal.Zero
		caveats = append(caveats, "vat exceeds gross; VAT line dropped")
	}

	// Shipping, proportional. shipment.cost only when not free-shipped. Guard 3 applies after VAT
	// is final: shipping cannot claim the whole post-VAT remainder (that would zero out revenue).
	ship := decimal.Zero
	if !(f.FreeShipping.Valid && f.FreeShipping.Bool) && f.ShipmentCost.Valid {
		ship = f.ShipmentCost.Decimal.Mul(k).Round(2)
	}
	if ship.GreaterThanOrEqual(g.Sub(vat)) {
		ship = decimal.Zero
		caveats = append(caveats, "shipping exceeds remainder; shipping line dropped")
	}

	// NET is the balancing remainder — strictly > 0 after the two guards above.
	net := g.Sub(vat).Sub(ship)

	lines := []entity.AcctJournalLineInsert{
		{AccountCode: moneyAccount(f.PaymentMethodName), Side: entity.AcctSideDebit, Amount: g},
		{AccountCode: revenueAccount(f.PaymentMethodName), Side: entity.AcctSideCredit, Amount: net},
	}
	if ship.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc4110, Side: entity.AcctSideCredit, Amount: ship})
	}
	if vat.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc2070, Side: entity.AcctSideCredit, Amount: vat})
	}

	// Acquirer fee: the captured Stripe fee (PaymentFee), or, for non-Stripe methods, an estimate
	// derived from the payment method's fee model (see orderFee). Booked Dr 6050 / Cr 1030 per the
	// spec table (fee reduces the processor balance).
	if fee := orderFee(f, g); fee.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc6050, Side: entity.AcctSideDebit, Amount: fee},
			entity.AcctJournalLineInsert{AccountCode: Acc1030, Side: entity.AcctSideCredit, Amount: fee},
		)
		if !isStripe(f.PaymentMethodName) {
			caveats = append(caveats, "fee estimated from method model")
		}
	}

	// COGS from the order-time cost snapshot (UnitCost is already COALESCE(cost_price_at_sale,
	// product.cost_price) in the facts — CLAUDE.md sales-margin chain). Costed lines only; uncosted
	// lines understate COGS and are named in a caveat, never invented.
	cogs, uncosted := saleCOGS(f.Items)
	if cogs.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc5010, Side: entity.AcctSideDebit, Amount: cogs},
			entity.AcctJournalLineInsert{AccountCode: Acc1130, Side: entity.AcctSideCredit, Amount: cogs},
		)
	}
	if len(uncosted) > 0 {
		caveats = append(caveats, "COGS understated; missing cost for product(s): "+joinProductIDs(uncosted))
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("order sale %s", f.UUID),
		SourceType:  entity.AcctSourceOrderSale,
		SourceKey:   f.UUID,
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}

// saleCOGS sums the costed order lines (UnitCost x Quantity, rounded once) and returns the product
// ids of the uncosted lines. UnitCost is invalid only when both the sale snapshot and the live
// product.cost_price were NULL.
func saleCOGS(items []entity.AcctOrderItemFact) (decimal.Decimal, []int) {
	total := decimal.Zero
	var uncosted []int
	for _, it := range items {
		if it.UnitCost.Valid {
			total = total.Add(it.UnitCost.Decimal.Mul(it.Quantity))
		} else {
			uncosted = append(uncosted, it.ProductId)
		}
	}
	return total.Round(2), uncosted
}
