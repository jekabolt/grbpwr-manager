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

// ErrProductionRunReceiveViaUpdate is returned by UpdateProductionRun when the client tries to set
// status=received directly: `received` is reached only through ReceiveProductionRun, which books the
// stock and seeds cost_price. Flipping it via a plain update would mark a run received with no stock.
var ErrProductionRunReceiveViaUpdate = errors.New("a production run is marked received only by receiving it")

// ErrProductionRunHasOpenIssues is returned by UpdateProductionRun when moving an open run to a
// terminal state (cancelled/closed) while material is still issued to it (net issues > 0): that
// material would silently drop out of WIP without being received or written off. Return or write off
// the issued material first.
var ErrProductionRunHasOpenIssues = errors.New("return or write off issued material before closing/cancelling the run")

// ErrProductionRunLineProductUnlinked is returned at receive when a received line's product is not
// one of the run's tech-card products (a colour-model that does not belong to this style).
var ErrProductionRunLineProductUnlinked = errors.New("a received line's product is not linked to the run's tech card")

// ErrProductionRunLineSizeUnlinked is returned at receive when a received line's size is not part of
// the run's tech-card size grid — booking it would mint sellable stock in a size the style never had.
var ErrProductionRunLineSizeUnlinked = errors.New("a received line's size is not part of the run's tech card size grid")

// ErrProductionRunConcurrentModification is returned at receive when the run's received quantities
// changed between the handler's read and the receive transaction (a concurrent edit). The caller
// should reload and retry.
var ErrProductionRunConcurrentModification = errors.New("production run changed during receive; reload and retry")

// ErrProductionRunCardChange is returned by UpdateProductionRun when the update tries to move the
// run to a different tech card: the planned-cost snapshot, the issued material's denormalised style
// (movement.tech_card_id) and the style-economics roll-ups are all anchored to the card the run was
// created for — re-pointing it would silently move WIP and actuals to another style while the
// movement journal keeps the old one (g25-13). Create a new run for the other style instead.
var ErrProductionRunCardChange = errors.New("a production run cannot move to another tech card; create a new run")

// ErrProductionRunConflict is returned by UpdateProductionRun when the caller passed a positive
// expected_lock_version that no longer matches the stored one — the run was edited concurrently
// between the read and the save. The caller should reload and retry (mirrors ErrTechCardConflict).
var ErrProductionRunConflict = errors.New("production run was modified concurrently; reload and retry")

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

// ProductionMarkerSource is the CAD/nesting software (or hand entry) a marker record came from. It
// mirrors the common.ProductionMarkerSource proto enum and is stored as its lowercase string.
type ProductionMarkerSource string

const (
	ProductionMarkerSourceGerber  ProductionMarkerSource = "gerber"
	ProductionMarkerSourceOptitex ProductionMarkerSource = "optitex"
	ProductionMarkerSourceLectra  ProductionMarkerSource = "lectra"
	ProductionMarkerSourceAudaces ProductionMarkerSource = "audaces"
	ProductionMarkerSourceManual  ProductionMarkerSource = "manual"
	ProductionMarkerSourceOther   ProductionMarkerSource = "other"
)

// ValidProductionMarkerSources is the set of accepted marker sources.
var ValidProductionMarkerSources = map[ProductionMarkerSource]bool{
	ProductionMarkerSourceGerber:  true,
	ProductionMarkerSourceOptitex: true,
	ProductionMarkerSourceLectra:  true,
	ProductionMarkerSourceAudaces: true,
	ProductionMarkerSourceManual:  true,
	ProductionMarkerSourceOther:   true,
}

// IsValidProductionMarkerSource reports whether s is an accepted marker source.
func IsValidProductionMarkerSource(s ProductionMarkerSource) bool {
	return ValidProductionMarkerSources[s]
}

// ProductionRunMarker is one imported nesting marker (раскладка / lay) of a run (gap-07 v2 E): the
// CAD source, the fabric width and lay length it was nested on, the units it yields, its
// fabric-utilisation %, an optional fabric/size, and a reference URL to the exported marker file.
// It is planning/traceability data — nothing here feeds the run's actual cost or cost_price.
type ProductionRunMarker struct {
	Id             int                    `db:"id"`
	Source         ProductionMarkerSource `db:"source"`
	MarkerName     sql.NullString         `db:"marker_name"`
	SizeId         sql.NullInt32          `db:"size_id"` // single-size marker; NULL = mixed-size lay
	MaterialId     sql.NullInt32          `db:"material_id"`
	MarkerWidth    decimal.NullDecimal    `db:"marker_width"`     // fabric width the marker was made for (cm)
	LayLength      decimal.NullDecimal    `db:"lay_length"`       // marker / lay length (cm)
	UnitsPerMarker sql.NullInt32          `db:"units_per_marker"` // garments yielded by one marker
	EfficiencyPct  decimal.NullDecimal    `db:"efficiency_pct"`   // fabric utilisation % (per-marker)
	MarkerFileUrl  sql.NullString         `db:"marker_file_url"`  // bare URL of the exported marker file
	Notes          sql.NullString         `db:"notes"`
}

// ProductionRunInsert is the writable payload for a run (header + size grid + actual costs).
// PlannedUnitCost and PlannedCurrency are server-snapshotted at plan time (from the linked
// tech_card_release or the live card's computed costing) — they are set by the service layer,
// never taken from the client, and are frozen once set so the run's plan does not drift when the
// card is edited afterwards.
type ProductionRunInsert struct {
	TechCardId          int                   `db:"tech_card_id"`
	ReleaseId           sql.NullInt64         `db:"release_id"`
	Status              ProductionRunStatus   `db:"status"`
	StartedAt           sql.NullTime          `db:"started_at"`
	ReceivedAt          sql.NullTime          `db:"received_at"`
	PlannedUnitCost     decimal.NullDecimal   `db:"planned_unit_cost"`
	PlannedCurrency     sql.NullString        `db:"planned_currency"`
	MarkerEfficiencyPct decimal.NullDecimal   `db:"marker_efficiency_pct"` // % fabric utilisation from the nesting software (NF-06)
	MarkerNotes         sql.NullString        `db:"marker_notes"`          // free marker/раскладка parameters
	Notes               sql.NullString        `db:"notes"`
	Lines               []ProductionRunLine   `db:"-"`
	Costs               []ProductionRunCost   `db:"-"`
	Markers             []ProductionRunMarker `db:"-"` // imported nesting markers (gap-07 v2 E)
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
	LockVersion       int                `db:"lock_version"` // optimistic-lock token, bumped on every update (#9)
}

// ProductionRunListFilter narrows ListProductionRuns. Zero-value fields mean "no filter".
type ProductionRunListFilter struct {
	TechCardId int                 // only runs of this tech card
	Status     ProductionRunStatus // only runs in this status
	// StaleDays > 0 restricts the result to "stale" runs — still open (planned/in_progress) and
	// created more than StaleDays days ago, matching the stale_open_production_run dashboard alert (#10).
	StaleDays int
}

// NetReceivedQty sums received quantities across all lines.
func (r *ProductionRun) NetReceivedQty() int64 {
	var received int64
	for _, ln := range r.Lines {
		if ln.ReceivedQty.Valid {
			received += ln.ReceivedQty.Int64
		}
	}
	return received
}

// ActualUnitCostBase returns the run's actual unit cost in the base currency, valid only when it is
// trustworthy for seeding cost_price: some quantity was received, at least one cost source exists (a
// manual cost article and/or a stock issue), EVERY manual article folded to base, and NO stock issue
// is uncosted (either would understate the cost). It is the same figure the read-side actuals emit as
// actual_unit_cost (manual + materials-from-stock, net of returns) ÷ total received. The result is
// rounded to money scale (2). This lives on the entity so both the dto (display) and the store
// (seeding cost_price inside the receive transaction) compute it identically.
func (r *ProductionRun) ActualUnitCostBase() decimal.NullDecimal {
	if r == nil {
		return decimal.NullDecimal{}
	}
	received := r.NetReceivedQty()
	if received == 0 {
		return decimal.NullDecimal{}
	}
	total := decimal.Zero
	haveManual := len(r.Costs) > 0
	for _, c := range r.Costs {
		if !c.AmountBase.Valid {
			return decimal.NullDecimal{} // partial fold → not trustworthy for cost_price
		}
		total = total.Add(c.AmountBase.Decimal)
	}
	haveStock := false
	for _, m := range r.MaterialMovements {
		switch m.MovementType {
		case MaterialMovementIssueProduction:
			haveStock = true
			if !m.UnitCostBase.Valid {
				return decimal.NullDecimal{} // an uncosted issue understates the total
			}
			total = total.Add(m.Quantity.Mul(m.UnitCostBase.Decimal))
		case MaterialMovementReturnProduction:
			if m.UnitCostBase.Valid {
				total = total.Sub(m.Quantity.Mul(m.UnitCostBase.Decimal))
			}
		}
	}
	if !haveManual && !haveStock {
		return decimal.NullDecimal{} // no cost source at all
	}
	return decimal.NullDecimal{Decimal: total.Div(decimal.NewFromInt(received)).RoundBank(2), Valid: true}
}
