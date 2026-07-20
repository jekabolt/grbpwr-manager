package accounting

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildOrderSaleEntry builds the order_sale journal entry for a paid order (rule S1,
// docs/plan-accounting/04-posting-rules.md, VAT reworked by phase 2 wave 1
// docs/plan-accounting-phase2/01-wave1-vat.md §1.3). occurredAt is the recognition moment — the
// order's payment confirmation, taken from the outbox event, not the placed date.
//
// VAT is derived from the RESOLVED REGIME's rate (vd), not the order's snapshot: it is the inclusive
// extraction G × rate/(100+rate) for the VAT regimes (oss / pl_domestic / uk_stock_domestic) and
// absent for export / wdt. The snapshot vat_amount is retained only as a cross-check — a regime-vs-
// snapshot gap over 1% raises a "vat snapshot mismatch" caveat, never a re-post. Revenue for a B2B
// order (buyer VAT id present, incl. wdt / UK B2B) is credited to 4310 Wholesale instead of 4010/4020.
//
// Everything else is unchanged: one EUR share k = G/total_price scales shipping; NET is the balancing
// remainder, so Σ credit equals G exactly; COGS is a separate balanced pair from the order-time cost
// snapshot; the phase-1 cascading guards still collapse a bad component to zero with a caveat.
//
// Returns ErrNotReady (Stripe settlement pending — retry), ErrSkipNonEUR (non-Stripe non-EUR — book
// manually) or ErrDegenerateAmounts (non-positive total/gross — skip) when no entry can be built.
func BuildOrderSaleEntry(f entity.AcctOrderFacts, vd VatDecision, occurredAt time.Time) (entity.AcctJournalEntryInsert, error) {
	g, err := grossEUR(f)
	if err != nil {
		return entity.AcctJournalEntryInsert{}, err
	}
	// Guard 1: degenerate amounts. Also protects the k division below from a zero/negative total.
	if f.TotalPrice.Sign() <= 0 || g.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}
	k := g.Div(f.TotalPrice)

	// Resolver caveats (unknown destination, wdt without vat id, ...) travel onto the entry.
	caveats := append([]string(nil), vd.Caveats...)

	// VAT from the regime rate (inclusive from gross), NOT the snapshot proportion. export / wdt / none
	// post no VAT line. Guard 2 (a VAT >= gross is dropped) is kept though unreachable via the inclusive
	// formula, as a defence against a pathological rate.
	vat := decimal.Zero
	if RegimeHasVAT(vd.Regime) && vd.RatePct.IsPositive() {
		vat = vatInclusive(g, vd.RatePct)
	}
	if vat.GreaterThanOrEqual(g) {
		vat = decimal.Zero
		caveats = append(caveats, "vat exceeds gross; VAT line dropped")
	}
	// Cross-check the regime VAT against the sale-time snapshot (scaled by k); a >1% gap is advisory.
	if vat.IsPositive() && f.VatAmount.Valid && vatSnapshotDiffers(vat, f.VatAmount.Decimal.Mul(k)) {
		caveats = append(caveats, "vat snapshot mismatch")
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
		{AccountCode: saleRevenueAccount(f), Side: entity.AcctSideCredit, Amount: net},
	}
	if ship.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc4110, Side: entity.AcctSideCredit, Amount: ship})
	}
	if vat.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc2070, Side: entity.AcctSideCredit, Amount: vat})
	}

	// Acquirer fee: the captured Stripe fee (PaymentFee), or, for non-Stripe methods, an estimate
	// derived from the payment method's fee model (see orderFee). Booked Dr 6050 / Cr the SAME money
	// account the sale debited (the fee reduces that balance): 1030 for Stripe, 1010 cash, 1040
	// bank-invoice. Always crediting 1030 left a phantom negative on the processor account for a
	// non-Stripe method that carries an estimated fee (A-2).
	if fee := orderFee(f, g); fee.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc6050, Side: entity.AcctSideDebit, Amount: fee},
			entity.AcctJournalLineInsert{AccountCode: moneyAccount(f.PaymentMethodName), Side: entity.AcctSideCredit, Amount: fee},
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

// oneHundred / vatMismatchTolerance are the inclusive-VAT denominator base and the snapshot-vs-regime
// tolerance (1%, 07 §7.4.2).
var (
	oneHundred           = decimal.NewFromInt(100)
	vatMismatchTolerance = decimal.NewFromFloat(0.01)
)

// vatInclusive extracts the VAT contained in a VAT-inclusive gross at rate% : G × rate/(100+rate),
// rounded to 2dp. It is strictly less than G for any finite non-negative rate.
func vatInclusive(gross, ratePct decimal.Decimal) decimal.Decimal {
	return gross.Mul(ratePct).Div(ratePct.Add(oneHundred)).Round(2)
}

// vatSnapshotDiffers reports whether the regime VAT diverges from the sale-time snapshot VAT by more
// than the tolerance. A non-positive snapshot cannot be compared (returns false).
func vatSnapshotDiffers(regimeVat, snapshotVat decimal.Decimal) bool {
	if snapshotVat.Sign() <= 0 {
		return false
	}
	return regimeVat.Sub(snapshotVat).Abs().Div(snapshotVat).GreaterThan(vatMismatchTolerance)
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
