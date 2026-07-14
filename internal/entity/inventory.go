package entity

import (
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// Material-warehouse errors (new-flow NF-01).
var (
	// ErrInsufficientMaterialStock is returned when an issue/write-off would drive on-hand below
	// zero. The message carries the available quantity so the API can surface it.
	ErrInsufficientMaterialStock = errors.New("insufficient material stock")
	// ErrMaterialArchived is returned when issuing from an archived material (receipts/returns of
	// an archived material are still allowed — you can wind a discontinued material down).
	ErrMaterialArchived = errors.New("material is archived")
	// ErrMaterialIssueTargetInvalid is returned when the issue/return target (a production run or
	// a sample) does not exist or is not in a state that accepts material movement (e.g. a run that
	// is already received/closed/cancelled).
	ErrMaterialIssueTargetInvalid = errors.New("material issue target invalid")
	// ErrMaterialUnitLocked is returned when changing a material's unit of measure after it has
	// stock movements — the historical quantities would become meaningless.
	ErrMaterialUnitLocked = errors.New("material unit cannot change once it has stock movements")
	// ErrMaterialCodeTaken is returned when a material's internal code duplicates another
	// non-archived material's code.
	ErrMaterialCodeTaken = errors.New("material code already in use")
	// ErrMaterialNotFound is returned by a warehouse operation whose material id does not exist.
	ErrMaterialNotFound = errors.New("material not found")
	// ErrExcessiveMaterialReturn is returned when a return exceeds the quantity still outstanding
	// (issued minus already returned) on the target — returning more than was issued would mint
	// phantom stock and drive the target's material cost negative.
	ErrExcessiveMaterialReturn = errors.New("return exceeds the material still issued to this target")
	// ErrInsufficientMaterialLot is returned when an issue draws more from a specific lot than the lot
	// has remaining (gap-07 v2 D).
	ErrInsufficientMaterialLot = errors.New("insufficient material lot remaining")
	// ErrMaterialLotMismatch is returned when a referenced lot belongs to a different material.
	ErrMaterialLotMismatch = errors.New("material lot belongs to a different material")
)

// MaterialLot is a received batch (roll / dye-lot) of a material (gap-07 v2 D): a supplier lot code
// with a running remaining quantity, for traceability and colour matching. UnitCost is informational
// only — valuation stays moving-average on MaterialStock; lots are NOT a FIFO costing basis.
type MaterialLot struct {
	Id           int                 `db:"id"`
	MaterialId   int                 `db:"material_id"`
	LotCode      string              `db:"lot_code"`
	SupplierDoc  sql.NullString      `db:"supplier_doc"`
	ReceivedQty  decimal.Decimal     `db:"received_qty"`
	RemainingQty decimal.Decimal     `db:"remaining_qty"`
	UnitCost     decimal.NullDecimal `db:"unit_cost"`
	Currency     sql.NullString      `db:"currency"`
	ReceivedAt   sql.NullTime        `db:"received_at"`
	Note         sql.NullString      `db:"note"`
	Archived     bool                `db:"archived"`
}

// MaterialPriceSourcePurchase marks a price point that entered the history from a stock receipt
// (a real purchase document), as opposed to a manual catalog entry or a production-run cost.
const MaterialPriceSourcePurchase = "purchase"

// MaterialMovementType enumerates the kinds of material-stock movement. quantity is always
// non-negative; the type (with on_hand before/after) encodes the direction.
type MaterialMovementType string

const (
	MaterialMovementReceipt           MaterialMovementType = "receipt"            // purchase-in
	MaterialMovementReceiptProduction MaterialMovementType = "receipt_production" // our own auxiliary run lands in stock (NF-07)
	MaterialMovementIssueProduction   MaterialMovementType = "issue_production"   // issued into a production run
	MaterialMovementIssueSample       MaterialMovementType = "issue_sample"       // issued to a sample
	MaterialMovementReturnProduction  MaterialMovementType = "return_production"  // unused remainder back from a run
	MaterialMovementReturnSample      MaterialMovementType = "return_sample"      // returned from a sample
	MaterialMovementAdjustment        MaterialMovementType = "adjustment"         // stock count (set/adjust)
	MaterialMovementWriteoff          MaterialMovementType = "writeoff"           // damage/loss/defect
)

// ValidMaterialMovementTypes is the closed set enforced by the DB CHECK and validated in the dto.
var ValidMaterialMovementTypes = map[MaterialMovementType]struct{}{
	MaterialMovementReceipt: {}, MaterialMovementReceiptProduction: {},
	MaterialMovementIssueProduction: {}, MaterialMovementIssueSample: {},
	MaterialMovementReturnProduction: {}, MaterialMovementReturnSample: {},
	MaterialMovementAdjustment: {}, MaterialMovementWriteoff: {},
}

// Material adjustment reasons (a subset shared with product stock-count semantics). Packaging is
// added for NF-07 (winding down produced auxiliary items).
const (
	MaterialAdjustReasonStockCount = "stock_count"
	MaterialAdjustReasonDamage     = "damage"
	MaterialAdjustReasonLoss       = "loss"
	MaterialAdjustReasonFound      = "found"
	MaterialAdjustReasonCorrection = "correction"
	MaterialAdjustReasonPackaging  = "packaging"
	MaterialAdjustReasonScrap      = "scrap" // cutting waste / offcuts from a marker layout (NF-06, gap-04)
	MaterialAdjustReasonOther      = "other"
)

// MaterialStock is a material's maintained on-hand balance and moving-average unit cost (base
// currency). One row per material, created lazily on first movement.
type MaterialStock struct {
	MaterialId      int                 `db:"material_id"`
	OnHand          decimal.Decimal     `db:"on_hand"`
	AvgUnitCostBase decimal.NullDecimal `db:"avg_unit_cost_base"`
	UpdatedAt       time.Time           `db:"updated_at"`
}

// MaterialMovement is one row of the append-only stock ledger.
type MaterialMovement struct {
	Id              int                  `db:"id"`
	MaterialId      int                  `db:"material_id"`
	MovementType    MaterialMovementType `db:"movement_type"`
	Quantity        decimal.Decimal      `db:"quantity"`
	OnHandBefore    decimal.Decimal      `db:"on_hand_before"`
	OnHandAfter     decimal.Decimal      `db:"on_hand_after"`
	UnitCost        decimal.NullDecimal  `db:"unit_cost"`
	Currency        sql.NullString       `db:"currency"`
	UnitCostBase    decimal.NullDecimal  `db:"unit_cost_base"`
	ProductionRunId sql.NullInt32        `db:"production_run_id"`
	SampleId        sql.NullInt32        `db:"sample_id"`
	TechCardId      sql.NullInt32        `db:"tech_card_id"`
	// ProductId is the colour-model an issue to a run was cut for (gap-07 v2 C); NULL = shared /
	// unattributed. Lets a run's material cost break down per colourway.
	ProductId sql.NullInt32  `db:"product_id"`
	Lot       sql.NullString `db:"lot"`
	// LotId is the structured lot (roll / dye-lot) this movement received into or drew from (gap-07 v2
	// D). NULL when no lot was tracked. The free-text Lot above is kept for backward compatibility.
	LotId         sql.NullInt32  `db:"lot_id"`
	SupplierDoc   sql.NullString `db:"supplier_doc"`
	Reason        sql.NullString `db:"reason"`
	Comment       sql.NullString `db:"comment"`
	AdminUsername string         `db:"admin_username"`
	OccurredAt    sql.NullTime   `db:"occurred_at"`
	CreatedAt     time.Time      `db:"created_at"`
}

// MaterialReceiptInsert is the payload of a stock receipt (purchase-in or produced-in). UnitCost is
// in Currency; an empty UnitCost is allowed (a quantity-only receipt that does not move the average
// and is flagged uncosted). ProductionRunId is set only for a receipt_production (NF-07).
type MaterialReceiptInsert struct {
	MaterialId      int
	Quantity        decimal.Decimal
	UnitCost        decimal.NullDecimal
	Currency        string
	ProductionRunId sql.NullInt32
	Lot             sql.NullString
	SupplierDoc     sql.NullString
	OccurredAt      sql.NullTime
	Comment         sql.NullString
	AdminUsername   string
	// FromProduction marks a receipt_production (auxiliary-run output) rather than a purchase.
	// UnitCost is then the run's actual per-unit base cost, already in the base currency.
	FromProduction bool
}

// MaterialIssueInsert is the payload of an issue to (or return from) a production run or a sample.
// Exactly one of ProductionRunId / SampleId must be set. IsReturn flips issue_* to return_*.
type MaterialIssueInsert struct {
	MaterialId      int
	Quantity        decimal.Decimal
	ProductionRunId sql.NullInt32
	SampleId        sql.NullInt32
	// ProductId optionally names the colour-model (product) an issue to a run is for (gap-07 v2 C);
	// only meaningful with ProductionRunId set. NULL = shared / unattributed.
	ProductId sql.NullInt32
	// LotId optionally draws this issue from a specific structured lot / roll (gap-07 v2 D); a return
	// with a LotId puts the quantity back on that lot. NULL = no lot tracking.
	LotId         sql.NullInt32
	IsReturn      bool
	OccurredAt    sql.NullTime
	Comment       sql.NullString
	AdminUsername string
}

// MaterialAdjustMode selects how AdjustMaterialStock changes the balance.
type MaterialAdjustMode string

const (
	MaterialAdjustModeSet      MaterialAdjustMode = "set"      // on_hand becomes Quantity (movement adjustment)
	MaterialAdjustModeAdjust   MaterialAdjustMode = "adjust"   // on_hand += Quantity (signed; movement adjustment)
	MaterialAdjustModeWriteoff MaterialAdjustMode = "writeoff" // on_hand -= Quantity (Quantity>0; movement writeoff)
)

// MaterialAdjustInsert is the payload of a stock count or write-off. For Set/Writeoff Quantity is a
// non-negative magnitude; for Adjust it is a signed delta.
type MaterialAdjustInsert struct {
	MaterialId    int
	Mode          MaterialAdjustMode
	Quantity      decimal.Decimal
	Reason        string
	Comment       sql.NullString
	AdminUsername string
}

// MaterialStockRow is a catalog material joined with its stock balance, valuation and low-stock
// flag — the shape of the warehouse list. AvgUnitCostBase/StockValueBase are confidential (costing
// field-shaping strips them for accounts without costing:read).
type MaterialStockRow struct {
	Material        Material
	OnHand          decimal.Decimal
	AvgUnitCostBase decimal.NullDecimal
	StockValueBase  decimal.NullDecimal
	MinStock        decimal.NullDecimal
	BelowMinStock   bool
}

// MaterialStockFilter narrows the warehouse list.
type MaterialStockFilter struct {
	Section       string
	Query         string // matches name / code / supplier_ref
	WithStockOnly bool   // only materials with on_hand > 0
	BelowMinOnly  bool   // only materials under their min_stock
}

// MaterialMovementFilter narrows the movement ledger.
type MaterialMovementFilter struct {
	MaterialId      int
	ProductionRunId int
	SampleId        int
	MovementType    MaterialMovementType
	// Optional inclusive occurred_at DATE bounds (YYYY-MM-DD); empty = open (B-5).
	OccurredFrom string
	OccurredTo   string
}

// PackagingBomItem is one line of the global packaging recipe (gap-07 v2 B): a material consumed on
// ship, `QtyPerOrder` once per shipment plus `QtyPerItem` × the order's unit count. MaterialName is
// resolved on read (List) for display; it is ignored on write.
type PackagingBomItem struct {
	Id           int             `db:"id"`
	MaterialId   int             `db:"material_id"`
	MaterialName string          `db:"material_name"`
	MaterialUnit sql.NullString  `db:"material_unit"`
	QtyPerOrder  decimal.Decimal `db:"qty_per_order"`
	QtyPerItem   decimal.Decimal `db:"qty_per_item"`
	Active       bool            `db:"active"`
}
