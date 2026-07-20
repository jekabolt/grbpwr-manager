package accounting

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildDisputeEntry builds the order_dispute journal entry for a Stripe chargeback that was OPENED
// (phase 2, wave 4 — docs/plan-accounting-phase2/04-wave4-money.md §4.3). Stripe pulls the disputed
// amount (and a dispute fee) from the Stripe balance, so this books:
//
//	Dr 4040 Returns & Refunds (disputed amount)   [contra-revenue — a forced refund]
//	Dr 6050 Merchant Processing Fees (dispute fee) [only when the fee is known]
//	Cr 1030 Payment Processor (amount + fee)       [money removed from Stripe]
//
// COGS is deliberately untouched: a chargeback does not return the goods (07 §7.4.6). A closed-WON
// dispute reverses this entry (the worker calls ReverseJournalEntry — append-only); a closed-LOST one
// leaves it standing (the money stayed gone).
//
// disputedAmount and fee are the account's settlement currency (EUR) — Stripe balance transactions are
// booked in the balance currency. feeKnown is false when Stripe did not (yet) report balance transactions:
// the fee line is dropped and a caveat is raised (still book the amount). source_key 'dispute:<id>' makes
// a redelivered webhook idempotent.
func BuildDisputeEntry(disputedAmount, fee decimal.Decimal, feeKnown bool, orderUUID, disputeID, currency string, occurredAt time.Time) (entity.AcctJournalEntryInsert, error) {
	amount := disputedAmount.Round(2)
	if amount.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}

	var caveats []string
	f := decimal.Zero
	if feeKnown {
		f = fee.Round(2)
		if f.IsNegative() {
			f = decimal.Zero
		}
	} else {
		caveats = append(caveats, "dispute fee unavailable from Stripe; disputed amount only")
	}
	if currency != "" && !isBaseCurrency(currency) {
		// The amount came from the EUR balance transactions even when the dispute is presented in another
		// currency; flag it so a mismatch is visible rather than silently trusted.
		caveats = append(caveats, "dispute presented in "+currency+"; amount booked from EUR balance")
	}

	lines := []entity.AcctJournalLineInsert{
		{AccountCode: Acc4040, Side: entity.AcctSideDebit, Amount: amount},
	}
	if f.IsPositive() {
		lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc6050, Side: entity.AcctSideDebit, Amount: f})
	}
	// Balancing credit: everything Stripe pulled from the processor balance.
	lines = append(lines, entity.AcctJournalLineInsert{AccountCode: Acc1030, Side: entity.AcctSideCredit, Amount: amount.Add(f)})

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: fmt.Sprintf("order dispute %s (%s)", orderUUID, disputeID),
		SourceType:  entity.AcctSourceOrderDispute,
		SourceKey:   fmt.Sprintf("dispute:%s", disputeID),
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}
