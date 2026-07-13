package entity

import (
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrProductionRunAlreadyReceived is returned by ReceiveProductionRun when the run has already
// been received (or closed) — receiving again would double-count stock.
var ErrProductionRunAlreadyReceived = errors.New("production run has already been received")

// ErrProductionRunReceivedImmutable is returned by DeleteProductionRun when the run has already
// been received (or closed): its stock increment and any cost_price it seeded are already applied,
// so deleting the run would orphan those side effects. Cancel/adjust the run instead of deleting.
var ErrProductionRunReceivedImmutable = errors.New("a received production run cannot be deleted")

// ErrProductionRunLineProductMissing is returned by ReceiveProductionRun when a line has a
// received quantity but no product_id — receive needs to know which product's stock to increment.
var ErrProductionRunLineProductMissing = errors.New("a production run line with a received quantity has no product")

// ErrProductionRunNothingReceived is returned by ReceiveProductionRun when no line carries a
// positive received quantity — there is nothing to book into stock.
var ErrProductionRunNothingReceived = errors.New("production run has no received quantities")

// ErrProductionRunHasMovements is returned by DeleteProductionRun when material has been issued to
// (or returned from) the run: those stock movements are applied facts (the FK is ON DELETE SET
// NULL, so a delete would orphan them from the run). Cancel the run instead of deleting it.
var ErrProductionRunHasMovements = errors.New("production run has material movements; cancel it instead of deleting")

// ProductionRunStatus is the lifecycle state of a production run (партия). It mirrors the
// common.ProductionRunStatus proto enum and is stored as its lowercase string in the DB.
type ProductionRunStatus string

const (
	ProductionRunPlanned    ProductionRunStatus = "planned"
	ProductionRunInProgress ProductionRunStatus = "in_progress"
	ProductionRunReceived   ProductionRunStatus = "received"
	ProductionRunClosed     ProductionRunStatus = "closed"
	ProductionRunCancelled  ProductionRunStatus = "cancelled"
)

// ValidProductionRunStatuses is the set of accepted run statuses.
var ValidProductionRunStatuses = map[ProductionRunStatus]bool{
	ProductionRunPlanned:    true,
	ProductionRunInProgress: true,
	ProductionRunReceived:   true,
	ProductionRunClosed:     true,
	ProductionRunCancelled:  true,
}

// IsValidProductionRunStatus reports whether s is an accepted run status.
func IsValidProductionRunStatus(s ProductionRunStatus) bool { return ValidProductionRunStatuses[s] }

// ProductionRunLine is one colour-model × size line of a run: which product (colourway) at which
// size, the planned quantity, and — once received — the received and defective counts (NULL until
// received) that drive plan/fact. ProductId may be NULL while planning (the colourway may not be
// published as a product yet), but every line with a received quantity must carry it at receive
// time. This replaces the old flat per-size grid so one marker (раскладка) can yield several
// colour-models in a single batch (NF-06).
type ProductionRunLine struct {
	Id          int           `db:"id"`
	ProductId   sql.NullInt32 `db:"product_id"`
	SizeId      int           `db:"size_id"`
	PlannedQty  int           `db:"planned_qty"`
	ReceivedQty sql.NullInt64 `db:"received_qty"`
	DefectQty   sql.NullInt64 `db:"defect_qty"`
}

// ProductionRunCostKind is the article category of an actual production-run cost. It mirrors the
// common.ProductionRunCostKind proto enum and is stored as its lowercase string.
type ProductionRunCostKind string

const (
	ProductionRunCostMaterials ProductionRunCostKind = "materials"
	ProductionRunCostCMT       ProductionRunCostKind = "cmt"
	ProductionRunCostHardware  ProductionRunCostKind = "hardware"
	ProductionRunCostPackaging ProductionRunCostKind = "packaging"
	ProductionRunCostLogistics ProductionRunCostKind = "logistics"
	ProductionRunCostDuty      ProductionRunCostKind = "duty"
	ProductionRunCostOther     ProductionRunCostKind = "other"
)

// ValidProductionRunCostKinds is the set of accepted cost article kinds.
var ValidProductionRunCostKinds = map[ProductionRunCostKind]bool{
	ProductionRunCostMaterials: true,
	ProductionRunCostCMT:       true,
	ProductionRunCostHardware:  true,
	ProductionRunCostPackaging: true,
	ProductionRunCostLogistics: true,
	ProductionRunCostDuty:      true,
	ProductionRunCostOther:     true,
}

// IsValidProductionRunCostKind reports whether k is an accepted cost kind.
func IsValidProductionRunCostKind(k ProductionRunCostKind) bool {
	return ValidProductionRunCostKinds[k]
}

// ProductionRunCost is one actual cost article incurred for a run (phase 2). Amount is in
// Currency; AmountBase is the base-currency equivalent (server-folded via the costing FX rates
// when not supplied) so run totals and plan/fact are a plain SUM with no read-time FX.
type ProductionRunCost struct {
	Id          int                   `db:"id"`
	RunId       int                   `db:"run_id"`
	Kind        ProductionRunCostKind `db:"kind"`
	Description sql.NullString        `db:"description"`
	Amount      decimal.Decimal       `db:"amount"`
	Currency    string                `db:"currency"`
	AmountBase  decimal.NullDecimal   `db:"amount_base"`
	IncurredAt  sql.NullTime          `db:"incurred_at"`
}

// ProductionRunInsert is the writable payload for a run (header + size grid + actual costs).
// PlannedUnitCost and PlannedCurrency are server-snapshotted at plan time (from the linked
// tech_card_release or the live card's computed costing) — they are set by the service layer,
// never taken from the client, and are frozen once set so the run's plan does not drift when the
// card is edited afterwards.
type ProductionRunInsert struct {
	TechCardId          int                 `db:"tech_card_id"`
	ReleaseId           sql.NullInt64       `db:"release_id"`
	Status              ProductionRunStatus `db:"status"`
	StartedAt           sql.NullTime        `db:"started_at"`
	ReceivedAt          sql.NullTime        `db:"received_at"`
	PlannedUnitCost     decimal.NullDecimal `db:"planned_unit_cost"`
	PlannedCurrency     sql.NullString      `db:"planned_currency"`
	MarkerEfficiencyPct decimal.NullDecimal `db:"marker_efficiency_pct"` // % fabric utilisation from the nesting software (NF-06)
	MarkerNotes         sql.NullString      `db:"marker_notes"`          // free marker/раскладка parameters
	Notes               sql.NullString      `db:"notes"`
	Lines               []ProductionRunLine `db:"-"`
	Costs               []ProductionRunCost `db:"-"`
}

// ProductionRun is a stored production run (production_run row + its line grid). MaterialMovements
// is the read-only ledger of material issued/returned to this run (NF-06), loaded on Get; it feeds
// the materials-from-stock figure in the actuals and the "issued" column of the material plan.
type ProductionRun struct {
	Id int `db:"id"`
	ProductionRunInsert
	MaterialMovements []MaterialMovement `db:"-"`
	CreatedAt         time.Time          `db:"created_at"`
	UpdatedAt         time.Time          `db:"updated_at"`
}

// ProductionRunListFilter narrows ListProductionRuns. Zero-value fields mean "no filter".
type ProductionRunListFilter struct {
	TechCardId int                 // only runs of this tech card
	Status     ProductionRunStatus // only runs in this status
}
