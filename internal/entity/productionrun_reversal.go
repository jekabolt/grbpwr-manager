package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Phase 6 (plan 05): receipt reversal — errors, command params and result.

// ErrProductionRunReceiptNotFound is returned when the receipt id names no receipt of the given run.
var ErrProductionRunReceiptNotFound = errors.New("receipt not found on this production run")

// ErrProductionRunReceiptAlreadyReversed is returned when the receipt already carries reversed_by —
// the reversal happened; retrying is not an error to merge, it is a fact to report.
var ErrProductionRunReceiptAlreadyReversed = errors.New("receipt is already reversed")

// ErrProductionRunReversalOfReversal is returned when the target is itself a reversal row.
var ErrProductionRunReversalOfReversal = errors.New("a reversal row cannot be reversed")

// ErrProductionRunReversalClosedRun is returned when the run is closed: its books are done; reopen
// is deliberately not supported (v1).
var ErrProductionRunReversalClosedRun = errors.New("a closed run's receipts cannot be reversed")

// ErrProductionRunHasReceiptHistory is returned when deleting a run whose receipt history (live,
// reversed or reversal rows) still exists — the history is an audit fact the FK protects.
var ErrProductionRunHasReceiptHistory = errors.New("the run has receipt history; a run with receipts cannot be deleted")

// ErrProductionRunReversalFinalFirst is returned when a PARTIAL receipt is targeted while a live
// FINAL exists: reversals unwind newest-first, otherwise the partial's FG share strands on WIP
// under a run no receipt can ever post to again (adversarial #3).
var ErrProductionRunReversalFinalFirst = errors.New("reverse the final receipt first: while it stands, reversing a partial would strand its cost in WIP forever")

// ErrProductionRunReversalAux is returned for receipts of auxiliary runs (v1): their output landed
// in the material warehouse and compounded the moving average of every bucket it touched — one for
// a single-output card, one per colour for a card with colour variants (0253) — so the honest undo
// is an adjustment or write-off on those materials, not a reversal of the receipt.
var ErrProductionRunReversalAux = errors.New("an auxiliary run's receipt cannot be reversed; adjust or write off the output material (each colour's own bucket) instead")

// ErrProductionRunReversalPeriodClosed is returned when the receipt's original accounting entry
// sits in a closed period (v1 refuses; a compensating cross-period entry is v2).
var ErrProductionRunReversalPeriodClosed = errors.New("the receipt's accounting period is closed; reopen it or correct via a manual entry")

// ProductionRunReversalShortfallItem is one (product, size, grade) whose on-hand stock cannot give
// the receipt's units back — they were sold (or already left the shelf some other way). Grade 'B'
// marks the seconds stock of the pair (Phase 7).
type ProductionRunReversalShortfallItem struct {
	ProductID int
	SizeID    int
	Grade     string
	Requested int
	OnHand    int
}

// ProductionRunReversalShortfallError blocks a reversal that would drive stock negative or steal
// units another batch (or a sale) already claimed. It lists EVERY short variant so the operator
// fixes the whole problem in one pass, not one error at a time.
type ProductionRunReversalShortfallError struct {
	Items []ProductionRunReversalShortfallItem
}

func (e *ProductionRunReversalShortfallError) Error() string {
	parts := make([]string, 0, len(e.Items))
	for _, it := range e.Items {
		grade := ""
		if it.Grade != "" && it.Grade != VariantGradeA {
			grade = " grade " + it.Grade
		}
		parts = append(parts, fmt.Sprintf("product %d size %d%s needs %d, on hand %d", it.ProductID, it.SizeID, grade, it.Requested, it.OnHand))
	}
	return "insufficient stock to reverse the receipt (units were sold or moved); " + strings.Join(parts, "; ")
}

// ProductCostReseed is the tech-card estimate the handler computed for one product, applied by the
// store ONLY when this run still claims the product's cost_price. Cost invalid = the card estimate
// is not computable → the claim is cleared to NULL (honestly unknown) instead of reseeded.
type ProductCostReseed struct {
	Cost      decimal.NullDecimal
	Breakdown sql.NullString
}

// ReverseProductionRunReceiptParams is the reversal command. The handler resolves RBAC, the aux
// refusal and the tech-card reseed figures; the store owns every stateful precondition under the
// run lock (the lock is also the idempotency guard: "unreversed receipt" can hold only once).
type ReverseProductionRunReceiptParams struct {
	RunID               int
	ReceiptID           int
	Reason              string
	ExpectedLockVersion int
	Username            string
	// Tech-card reseed inputs (plan 05 item 5): per-product estimate from the run's card as the
	// handler read it — deliberately NOT lock-version-fenced (removing the dead run claim beats
	// losing the reversal to a concurrent card edit; a stale estimate is the card's own history).
	// Products absent from the map clear to NULL when this run claims them.
	CardID int
	Reseed map[int]ProductCostReseed
}

// ReverseProductionRunReceiptResult reports what the reversal actually did.
type ReverseProductionRunReceiptResult struct {
	ReversalReceiptID int
	// CompensatedFGBase is the Dr 1120 / Cr 1130 amount booked (invalid when the receipt had no
	// live entry or its entry carried no FG transfer — nothing to compensate).
	CompensatedFGBase decimal.NullDecimal
	// Cost-price outcomes per product id (reseeded to card estimate / cleared to NULL / left
	// untouched because a later source superseded the run's claim).
	CostPriceReseeded []int
	CostPriceCleared  []int
	CostPriceSkipped  []int
}

// ProductionRunEventReceiptReversed is the production_run_event.event_type of a receipt reversal.
const ProductionRunEventReceiptReversed = "receipt_reversed"

// ProductionRunEventUnitsScrapped is the production_run_event.event_type recording a receipt's
// defected units (Phase 7): scrap counts (cost resolved by the posting rule) and seconds counts
// (booked into B-grade stock). The event is the scrap trace — scrap writes NO stock movement.
const ProductionRunEventUnitsScrapped = "units_scrapped"
