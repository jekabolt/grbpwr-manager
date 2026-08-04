package accounting

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BuildProductionReceiveEntry builds the production_receive journal entry when a run is received
// (rule P1, docs/plan-accounting/04-posting-rules.md). source_key = productionReceiveSourceKey(run,
// version) — '<id>' for the first post, '<id>:vN' for a re-post after a reversal, mirroring
// opex/shipping. Without the version a reversed entry kept the bare key forever and, with
// (source_type, source_key) UNIQUE, every re-post collapsed onto it: the run could never be posted
// again, permanently failing period close and heading the posting worker's batch each tick.
// occurred_at = received_at.
//
// By receive time the run's material issues (M3/M5) are already on 1120 WIP. What is not yet in the
// ledger is the run's manual costs (CMT, logistics, duty...) — the entry capitalises the
// still-uncapitalised DELTA into WIP against AP ("capitalize once", Phase 5) — and the same event
// relieves WIP into Finished Goods by the GOOD-unit share:
//
//	TOTAL_WIP = LEDGER_WIP + MANUAL_ALL     (LEDGER_WIP = costed post-cutover issues posted by M3/M5)
//	TARGET    = TOTAL_WIP × allGood ÷ allReceived    (full TOTAL_WIP when nothing was ever counted)
//	final:    FG = TARGET − Σ siblings' posted FG    (true-up: rounding never strands money on 1120)
//	partial:  FG = min( (TOTAL_WIP − Σ posted FG) × good ÷ max(planned − priorUnits, thisUnits),
//	                    TARGET − Σ posted FG )
//
// Phase 7 resolves the defect share ON THE FINAL receipt (partials leave it in WIP — only the
// closed series knows the real defect rate): scrap units up to normalLossRate × allReceived are
// NORMAL loss, absorbed into the good units' FG value; the abnormal excess is written off
// Dr 5040 / Cr 1120. Seconds-dispositioned units are recovered value (B-grade stock at zero cost
// v1) — their share also rides with the good units, never written off. An AUXILIARY run posts NO
// FG side at all: its output is a material, already moved off 1120 by M2 (receipt_production) at
// receive time — posting FG too double-relieved WIP (latent Phase-4 defect, fixed here). Sibling
// aggregates come from posted_manual_base/posted_fg_base, written in the same tx as each entry, so
// the arithmetic is exact under any posting order.
//
// startDate is accounting.start_date (the cutover). LEDGER_WIP is derived here from r.Issues,
// counting only costed issue_production/return_production movements with CreatedAt >= startDate —
// a mirror of what M3/M5 actually posted. A run opened before cutover has issues that were never
// debited to 1120, so including them would credit 1120 below what was ever debited there; those are
// excluded and flagged instead. Uncosted post-cutover issues and uncosted manual cost lines are
// likewise excluded from their respective totals and flagged, never invented at a made-up cost.
//
// Returns ErrSkipEmpty when there is nothing positive to post (all uncosted, no manual cost).
// normalLossRate is the expected-waste threshold (config accounting.defect_normal_loss_rate, e.g.
// 0.05 = 5% of all received units) — the boundary between absorbed and written-off scrap.
func BuildProductionReceiveEntry(r entity.AcctRunFacts, startDate time.Time, version int, normalLossRate decimal.Decimal) (entity.AcctJournalEntryInsert, error) {
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
		// Round per issue, exactly as M3/M5 rounded when they posted these to 1120 WIP — summing the
		// unrounded products here would drift by cents against the posted ledger WIP and leave a
		// residual on 1120 forever (A-3).
		value := iss.Quantity.Mul(iss.UnitCostBase.Decimal).Round(2)
		switch iss.MovementType {
		case entity.MaterialMovementIssueProduction:
			ledgerWIP = ledgerWIP.Add(value)
		case entity.MaterialMovementReturnProduction:
			ledgerWIP = ledgerWIP.Sub(value)
		}
	}

	manualNow := manualCost.Round(2)

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

	// Capitalize ONCE (Phase 5): this receipt's entry books only the manual money no sibling entry
	// has capitalised yet — the delta between the run's costed manual total as of now and what the
	// other receipts' live entries already put on 1120/2010. On a single-receipt run the delta is
	// the whole total, byte-identical to the Phase 4 rule. A negative delta (a cost row deleted
	// after a sibling posted it) is not compensatable here — flag it, post nothing negative.
	manual := manualNow.Sub(r.OtherPostedManualBase)
	if manual.IsNegative() {
		caveats = append(caveats, "manual costs shrank below the already-capitalised total; nothing further capitalised")
		manual = decimal.Zero
	}

	// The good-unit share (Phase 5): of everything that ever entered this run's WIP
	// (posted material issues + ALL capitalised manual, including this delta), finished goods may
	// carry only the share earned by GOOD units — the defect share stays on 1120 for the write-off
	// phase. A receipt set with NO counted units at all (the 0231 legacy backfill of a grid edited
	// back to NULLs) is not scrap: it transfers everything, exactly as the run did before receipts.
	// The distributable total is what the ledger actually HOLDS, not what the cost table says now:
	// after a clamped negative delta (costs shrank below the already-capitalised total) 1120 still
	// carries the siblings' larger figure, and distributing only manualNow would strand the
	// difference on 1120 past the final true-up (adversarial #5).
	totalWIPGross := ledgerWIP.Round(2).Add(decimal.Max(manualNow, r.OtherPostedManualBase))
	// Single-receipt facts (Phase 4 loaders, legacy-shaped tests) carry no All* aggregates — this
	// receipt IS the run's whole receipt set then.
	allGood, allReceived := r.AllGoodQty, r.AllReceivedQty
	if allReceived == 0 {
		allGood, allReceived = r.GoodQtyTotal, r.GoodQtyTotal+r.DefectQtyTotal
	}
	// No units counted ANYWHERE (the 0231 zero-line legacy backfill) is the pre-receipt world:
	// final semantics, full transfer — a partial with zero counts cannot exist (the command
	// refuses it), so this can only be legacy data.
	isFinal := r.IsFinal || r.ReceiptID == 0 || allReceived == 0
	goodShareTarget := totalWIPGross
	if allReceived > 0 {
		goodShareTarget = totalWIPGross.
			Mul(decimal.NewFromInt(int64(allGood))).
			Div(decimal.NewFromInt(int64(allReceived))).Round(2)
	}

	// The defect write-off (Phase 7): only the FINAL receipt splits the run's scrap into normal
	// (absorbed by the good units) and abnormal (expensed) — partials cannot know the closed
	// series' defect rate and keep the Phase 5 behaviour (the defect share waits on 1120).
	var fg, writeOff decimal.Decimal
	switch {
	case r.IsAux:
		// M2 already relieved 1120 into 1110 for the output material at receive time — the FG side
		// (and any write-off) must not exist here at all, or WIP is relieved twice.
	case isFinal:
		// What is still on 1120 for this run — the outer bound of everything this entry may move.
		remaining := totalWIPGross.Sub(r.OtherPostedFGBase)
		if remaining.IsPositive() {
			if allGood == 0 && allReceived > 0 && r.ReceiptID > 0 {
				// A run that produced NO good units has nothing to absorb the loss into — the
				// whole remainder is the loss event, normal allowance or not. That includes a
				// seconds-only run: its physical output exists as B stock, but B carries ZERO cost
				// in v1 (unpriced, unsellable), so its production cost is expensed, not
				// capitalised into inventory nobody valued (adversarial #4 — distinct caption so
				// the operator sees which case fired).
				writeOff = remaining
				if r.AllScrapQty == 0 {
					caveats = append(caveats, "seconds-only run closed: remaining WIP written off (B stock carries zero cost v1)")
				} else {
					caveats = append(caveats, "all-scrap run closed: entire remaining WIP written off")
				}
			} else {
				allowance := decimal.NewFromInt(int64(allReceived)).Mul(normalLossRate).Floor()
				abnormal := decimal.NewFromInt(int64(r.AllScrapQty)).Sub(allowance)
				if abnormal.IsPositive() && allReceived > 0 {
					writeOff = totalWIPGross.
						Mul(abnormal).
						Div(decimal.NewFromInt(int64(allReceived))).Round(2)
					if writeOff.GreaterThan(remaining) {
						// The pro-rata figure exceeds what is still on 1120 (partials already
						// relieved most of it) — the excess stays capitalised in the good units,
						// and the caveat must say what was ACTUALLY expensed (adversarial #7).
						caveats = append(caveats, fmt.Sprintf(
							"abnormal defect loss: %s unit(s) beyond the %s-unit allowance; write-off capped at the remaining WIP %s (the excess stays capitalised)",
							abnormal.String(), allowance.String(), remaining.String()))
						writeOff = remaining
					} else {
						caveats = append(caveats, fmt.Sprintf(
							"abnormal defect loss: %s unit(s) beyond the %s-unit normal allowance written off",
							abnormal.String(), allowance.String()))
					}
				}
				// True-up: the good units carry everything that is not written off — their own
				// share, the normal-loss share and the seconds share (B stock is zero-cost v1).
				// Intermediate roundings cancel out here; nothing strands on 1120.
				fg = remaining.Sub(writeOff)
			}
		} else if remaining.IsNegative() {
			fg = remaining // surfaces through the negative-FG caveat below
		}
	default:
		// A partial delivery relieves the CURRENT WIP balance pro-rata against the units still
		// expected (plan floor: never less than this receipt's own units, so the ratio is ≤ 1 even
		// on an over-plan delivery), and never beyond the good-unit share siblings have left over.
		thisUnits := r.GoodQtyTotal + r.DefectQtyTotal
		remaining := r.PlannedQtyTotal - (allReceived - thisUnits)
		if remaining < thisUnits {
			remaining = thisUnits
		}
		wipBalance := totalWIPGross.Sub(r.OtherPostedFGBase)
		if remaining > 0 {
			fg = wipBalance.
				Mul(decimal.NewFromInt(int64(r.GoodQtyTotal))).
				Div(decimal.NewFromInt(int64(remaining))).Round(2)
		}
		if cap := goodShareTarget.Sub(r.OtherPostedFGBase); fg.GreaterThan(cap) {
			fg = cap
		}
	}

	allScrap := r.ReceiptID > 0 && allGood == 0 && allReceived > 0

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
	case allScrap && !isFinal:
		if totalWIPGross.IsPositive() {
			caveats = append(caveats, "all-scrap receipt: cost left in WIP pending the series close")
		}
	case fg.IsPositive():
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc1130, Side: entity.AcctSideDebit, Amount: fg},
			entity.AcctJournalLineInsert{AccountCode: Acc1120, Side: entity.AcctSideCredit, Amount: fg},
		)
	case fg.IsNegative():
		// Negative FG (post-cutover returns shrank WIP below what siblings already transferred, or
		// a late partial after the final trued up) — a negative finished-goods transfer is not
		// representable; leave the ledger as-is and flag it.
		caveats = append(caveats, "non-positive finished-goods amount; FG transfer skipped")
	}
	// Abnormal defect loss out of WIP into the write-off expense.
	if writeOff.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: Acc5040, Side: entity.AcctSideDebit, Amount: writeOff},
			entity.AcctJournalLineInsert{AccountCode: Acc1120, Side: entity.AcctSideCredit, Amount: writeOff},
		)
	}

	if len(lines) == 0 {
		if len(caveats) > 0 {
			return entity.AcctJournalEntryInsert{}, &EmptyReceiptError{Caveats: caveats}
		}
		return entity.AcctJournalEntryInsert{}, ErrSkipEmpty
	}

	entry := entity.AcctJournalEntryInsert{
		OccurredAt:  r.ReceivedAt,
		Description: fmt.Sprintf("production run %d received: %s", r.RunID, r.TechCardName),
		SourceType:  entity.AcctSourceProductionReceive,
		SourceKey:   productionReceiveSourceKey(r, version),
		CreatedBy:   createdBySystem,
		Lines:       lines,
	}
	applyCaveats(&entry, caveats)
	return entry, nil
}

// productionReceiveSourceKey is 'receipt:<receipt id>' for the first version, ':vN'-suffixed for a
// re-post (N > 1) — the receipt became the accounting unit in Phase 4 (0235 rewrote the legacy
// '<run id>' keys). Facts without a receipt id (legacy-shaped tests) fall back to the old run-id
// family so the scheme stays one function.
func productionReceiveSourceKey(r entity.AcctRunFacts, version int) string {
	key := strconv.Itoa(r.RunID)
	if r.ReceiptID > 0 {
		key = "receipt:" + strconv.Itoa(r.ReceiptID)
	}
	if version > 1 {
		return fmt.Sprintf("%s:v%d", key, version)
	}
	return key
}
