package accounting

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildShippingActualEntry builds the shipping_actual journal entry for a shipment's actual carrier cost
// (phase 2, wave 3, feature 3.1 — docs/plan-accounting-phase2/03-wave3-pnl-completion.md §3.1). It books
// the manually-entered shipment.actual_cost (+ return_shipping_cost) as OPEX: Dr 6030 Shipping &
// Fulfillment / Cr 2030 Accrued Expenses. This pairs the 4110 shipping INCOME that phase 1 recognised
// with no expense side — removing that permanent P&L caveat.
//
// The source is mutable (actual_cost is entered with delay and can be corrected), so the worker reposts
// on a change with a versioned source_key: 'ship:<id>' for the first version, 'ship:<id>:vN' for a
// repost (mirrors opex). occurred_at is the shipping_date (fallback: the row's last-update instant, with
// a caveat). A shipment with no positive cost posts nothing (ErrSkipEmpty) and the worker reverses any
// prior version. Rounding is on the 2dp DECIMAL inputs only; the Cr 2030 line is the balancing total.
func BuildShippingActualEntry(f entity.AcctShipmentCostFacts, version int) (entity.AcctJournalEntryInsert, error) {
	var caveats []string

	actual := decimal.Zero
	if f.ActualCost.Valid {
		actual = f.ActualCost.Decimal.Round(2)
	}
	ret := decimal.Zero
	if f.ReturnShippingCost.Valid {
		ret = f.ReturnShippingCost.Decimal.Round(2)
	}
	// A negative carrier cost is nonsensical (a credit note is booked manually) — drop it with a caveat
	// rather than post a negative line (chk_acct_line_amount forbids it anyway).
	if actual.IsNegative() {
		actual = decimal.Zero
		caveats = append(caveats, "negative actual_cost dropped")
	}
	if ret.IsNegative() {
		ret = decimal.Zero
		caveats = append(caveats, "negative return_shipping_cost dropped")
	}

	total := actual.Add(ret)
	if total.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrSkipEmpty
	}

	lines := make([]entity.AcctJournalLineInsert, 0, 3)
	if actual.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc6030, Side: entity.AcctSideDebit, Amount: actual})
	}
	if ret.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{
			AccountCode: Acc6030, Side: entity.AcctSideDebit, Amount: ret,
			Note: nullStr("return shipping"),
		})
	}
	// Balancing credit: the whole shipping cost accrued.
	lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc2030, Side: entity.AcctSideCredit, Amount: total})

	occurredAt, dated := shippingOccurredAt(f)
	if !dated {
		caveats = append(caveats, "shipping_date missing; dated to last update")
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("shipping cost %s (shipment %d)", f.OrderUUID, f.ShipmentID),
		SourceType:  entity.AcctSourceShippingActual,
		SourceKey:   shippingSourceKey(f.ShipmentID, version),
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}

// shippingOccurredAt is the shipment's posting instant: shipping_date when set, else the row's
// last-update instant (dated=false flags the fallback for a caveat).
func shippingOccurredAt(f entity.AcctShipmentCostFacts) (t time.Time, dated bool) {
	if f.ShippingDate.Valid {
		return f.ShippingDate.Time, true
	}
	return f.UpdatedAt, false
}

// shippingSourceKey is 'ship:<id>' for the first version, 'ship:<id>:vN' for a repost (N > 1).
func shippingSourceKey(shipmentID, version int) string {
	if version > 1 {
		return fmt.Sprintf("ship:%d:v%d", shipmentID, version)
	}
	return fmt.Sprintf("ship:%d", shipmentID)
}
