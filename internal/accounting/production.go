package accounting

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildProductionReceiveEntry builds the production_receive journal entry when a run is received
// (rule P1, docs/plan-accounting/04-posting-rules.md). source_key = run id; occurred_at =
// received_at.
//
// By receive time the run's material issues (M3/M5) are already on 1120 WIP. What is not yet in the
// ledger is the run's manual costs (CMT, logistics, duty...) — booked now into WIP against AP — and
// the same event moves the whole run's WIP to Finished Goods:
//
//	FG = MANUAL + LEDGER_WIP   (LEDGER_WIP = the costed, post-cutover issues actually posted by M3/M5)
//
// startDate is accounting.start_date (the cutover). LEDGER_WIP is derived here from r.Issues,
// counting only costed issue_production/return_production movements with CreatedAt >= startDate —
// a mirror of what M3/M5 actually posted. A run opened before cutover has issues that were never
// debited to 1120, so including them would credit 1120 below what was ever debited there; those are
// excluded and flagged instead. Uncosted post-cutover issues and uncosted manual cost lines are
// likewise excluded from their respective totals and flagged, never invented at a made-up cost.
//
// Returns ErrSkipEmpty when there is nothing positive to post (all uncosted, no manual cost).
func BuildProductionReceiveEntry(r entity.AcctRunFacts, startDate time.Time) (entity.AcctJournalEntryInsert, error) {
	manualCost := decimal.Zero
	manualUncostedCount := 0
	for _, c := range r.Costs {
		if c.AmountBase.Valid {
			manualCost = manualCost.Add(c.AmountBase.Decimal)
		} else {
			manualUncostedCount++
		}
	}

	ledgerWIP := decimal.Zero
	preCutoverIssues := false
	uncostedIssues := false
	for _, iss := range r.Issues {
		if iss.CreatedAt.Before(startDate) {
			preCutoverIssues = true
			continue
		}
		if !iss.UnitCostBase.Valid {
			uncostedIssues = true
			continue
		}
		value := iss.Quantity.Mul(iss.UnitCostBase.Decimal)
		switch iss.MovementType {
		case entity.MaterialMovementIssueProduction:
			ledgerWIP = ledgerWIP.Add(value)
		case entity.MaterialMovementReturnProduction:
			ledgerWIP = ledgerWIP.Sub(value)
		}
	}

	manual := manualCost.Round(2)
	fg := manual.Add(ledgerWIP).Round(2)

	var caveats []string
	if uncostedIssues {
		caveats = append(caveats, "finished goods understated; run has uncosted material issues")
	}
	if manualUncostedCount > 0 {
		caveats = append(caveats, fmt.Sprintf("%d manual cost line(s) have no base amount and were skipped", manualUncostedCount))
	}
	if preCutoverIssues {
		caveats = append(caveats, "pre-cutover WIP excluded")
	}

	var lines []entity.AcctJournalLineInsert
	// Manual production costs capitalised into WIP against AP.
	if manual.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc1120, Side: entity.AcctSideDebit, Amount: manual},
			entity.AcctJournalLineInsert{AccountCode: Acc2010, Side: entity.AcctSideCredit, Amount: manual},
		)
	}
	// WIP -> Finished Goods.
	switch {
	case fg.IsPositive():
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc1130, Side: entity.AcctSideDebit, Amount: fg},
			entity.AcctJournalLineInsert{AccountCode: Acc1120, Side: entity.AcctSideCredit, Amount: fg},
		)
	case !fg.IsZero():
		// Negative FG (post-cutover returns outran issues in the costed set) — a negative
		// finished-goods transfer is not representable; leave WIP as-is and flag it.
		caveats = append(caveats, "non-positive finished-goods amount; FG transfer skipped")
	}

	if len(lines) == 0 {
		return entity.AcctJournalEntryInsert{}, ErrSkipEmpty
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  r.ReceivedAt,
		Description: fmt.Sprintf("production run %d received: %s", r.RunID, r.TechCardName),
		SourceType:  entity.AcctSourceProductionReceive,
		SourceKey:   strconv.Itoa(r.RunID),
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}
