package entity

import (
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrTechCardConflict is returned by UpdateTechCard when the caller's
// expected_lock_version no longer matches the stored one (a concurrent edit).
var ErrTechCardConflict = errors.New("tech card was modified concurrently")

// ErrTechCardReleased is returned when content of a RELEASED tech card is edited
// without first re-opening it to DRAFT (a released card is frozen for the factory).
var ErrTechCardReleased = errors.New("tech card is released and frozen; re-open to draft to edit")

// TechCardStage is the development stage of a tech card. It mirrors the
// common.TechCardStage proto enum and is stored as a string in tech_card.stage.
type TechCardStage string

const (
	TechCardStageProto TechCardStage = "proto" // prototype
	TechCardStageFit   TechCardStage = "fit"   // fit sample
	TechCardStageSMS   TechCardStage = "sms"   // salesman sample
	TechCardStagePP    TechCardStage = "pp"    // pre-production
	TechCardStageProd  TechCardStage = "prod"  // production
)

// ValidTechCardStages is the set of accepted tech-card stages.
var ValidTechCardStages = map[TechCardStage]bool{
	TechCardStageProto: true,
	TechCardStageFit:   true,
	TechCardStageSMS:   true,
	TechCardStagePP:    true,
	TechCardStageProd:  true,
}

// IsValidTechCardStage reports whether s is an accepted stage.
func IsValidTechCardStage(s TechCardStage) bool {
	return ValidTechCardStages[s]
}

// TechCardApprovalState is the gating release state of a tech card, orthogonal to
// TechCardStage. It mirrors the common.TechCardApprovalState proto enum and is
// stored as a string in tech_card.approval_state.
type TechCardApprovalState string

const (
	TechCardApprovalDraft    TechCardApprovalState = "draft"
	TechCardApprovalInReview TechCardApprovalState = "in_review"
	TechCardApprovalApproved TechCardApprovalState = "approved"
	TechCardApprovalReleased TechCardApprovalState = "released"
	TechCardApprovalObsolete TechCardApprovalState = "obsolete"
)

// ValidTechCardApprovalStates is the set of accepted approval states.
var ValidTechCardApprovalStates = map[TechCardApprovalState]bool{
	TechCardApprovalDraft:    true,
	TechCardApprovalInReview: true,
	TechCardApprovalApproved: true,
	TechCardApprovalReleased: true,
	TechCardApprovalObsolete: true,
}

// IsValidTechCardApprovalState reports whether s is an accepted approval state.
func IsValidTechCardApprovalState(s TechCardApprovalState) bool {
	return ValidTechCardApprovalStates[s]
}

// TechCardMeasurementUnit is the unit for the card's geometry (callout dimensions
// and the future POM). It mirrors the common.TechCardMeasurementUnit proto enum
// and is stored as a string in tech_card.measurement_unit.
type TechCardMeasurementUnit string

const (
	TechCardUnitCm TechCardMeasurementUnit = "cm"
	TechCardUnitMm TechCardMeasurementUnit = "mm"
)

// ValidTechCardMeasurementUnits is the set of accepted measurement units.
var ValidTechCardMeasurementUnits = map[TechCardMeasurementUnit]bool{
	TechCardUnitCm: true,
	TechCardUnitMm: true,
}

// IsValidTechCardMeasurementUnit reports whether u is an accepted unit.
func IsValidTechCardMeasurementUnit(u TechCardMeasurementUnit) bool {
	return ValidTechCardMeasurementUnits[u]
}

// TechCardMediaKind classifies a tech-card sketch image. It mirrors the
// common.TechCardMediaKind proto enum and is stored as a string in
// tech_card_media.kind.
type TechCardMediaKind string

const (
	TechCardMediaFront     TechCardMediaKind = "front"
	TechCardMediaBack      TechCardMediaKind = "back"
	TechCardMediaDetail    TechCardMediaKind = "detail"
	TechCardMediaLining    TechCardMediaKind = "lining"
	TechCardMediaPreview   TechCardMediaKind = "preview"
	TechCardMediaMoodboard TechCardMediaKind = "moodboard"
	TechCardMediaReference TechCardMediaKind = "reference"
	TechCardMediaSwatch    TechCardMediaKind = "swatch"
)

// ValidTechCardMediaKinds is the set of accepted sketch-media kinds.
var ValidTechCardMediaKinds = map[TechCardMediaKind]bool{
	TechCardMediaFront:     true,
	TechCardMediaBack:      true,
	TechCardMediaDetail:    true,
	TechCardMediaLining:    true,
	TechCardMediaPreview:   true,
	TechCardMediaMoodboard: true,
	TechCardMediaReference: true,
	TechCardMediaSwatch:    true,
}

// IsValidTechCardMediaKind reports whether k is an accepted media kind.
func IsValidTechCardMediaKind(k TechCardMediaKind) bool {
	return ValidTechCardMediaKinds[k]
}

// TechCardMediaItem is a writable sketch-media reference (id + kind).
type TechCardMediaItem struct {
	MediaId int               `db:"media_id"`
	Kind    TechCardMediaKind `db:"kind"`
	Caption sql.NullString    `db:"caption"`
}

// TechCardMediaFull is a resolved sketch-media reference for display.
type TechCardMediaFull struct {
	Media MediaFull
	Kind  TechCardMediaKind
}

// TechCardCallout is a numbered detail note pointing at the technical sketch.
type TechCardCallout struct {
	Number      int                 `db:"callout_number"`
	Part        sql.NullString      `db:"part"`
	Description sql.NullString      `db:"description"`
	Dimensions  sql.NullString      `db:"dimensions"`
	MediaId     sql.NullInt32       `db:"media_id"` // sketch this callout is pinned to
	PosX        decimal.NullDecimal `db:"pos_x"`    // normalised 0..1 marker position
	PosY        decimal.NullDecimal `db:"pos_y"`
}

// TechCardRevision is one entry in the revision log.
type TechCardRevision struct {
	Version      sql.NullString `db:"version"`
	RevisionDate sql.NullTime   `db:"revision_date"`
	Author       sql.NullString `db:"author"`
	Section      sql.NullString `db:"section"`
	ChangeNote   sql.NullString `db:"change_note"`
}

// TechCardBomSection groups a BOM line by material family. Mirrors the
// common.TechCardBomSection proto enum; stored as a string in tech_card_bom_item.section.
type TechCardBomSection string

const (
	BomSectionFabric      TechCardBomSection = "fabric"
	BomSectionLining      TechCardBomSection = "lining"
	BomSectionInterlining TechCardBomSection = "interlining"
	BomSectionInsulation  TechCardBomSection = "insulation"
	BomSectionHardware    TechCardBomSection = "hardware"
	BomSectionThread      TechCardBomSection = "thread"
	BomSectionLabel       TechCardBomSection = "label"
	BomSectionPackaging   TechCardBomSection = "packaging"
	BomSectionTrim        TechCardBomSection = "trim" // soft trims (бейка / тесьма / резинка / кант / шнур / лента)
)

// ValidTechCardBomSections is the set of accepted BOM sections.
var ValidTechCardBomSections = map[TechCardBomSection]bool{
	BomSectionFabric:      true,
	BomSectionLining:      true,
	BomSectionInterlining: true,
	BomSectionInsulation:  true,
	BomSectionHardware:    true,
	BomSectionThread:      true,
	BomSectionLabel:       true,
	BomSectionPackaging:   true,
	BomSectionTrim:        true,
}

// IsValidTechCardBomSection reports whether s is an accepted BOM section.
func IsValidTechCardBomSection(s TechCardBomSection) bool {
	return ValidTechCardBomSections[s]
}

// TechCardLabDipStatus is the lab-dip lifecycle of a colourway. Mirrors the
// common.TechCardLabDipStatus proto enum; stored in tech_card_colorway.lab_dip_status.
type TechCardLabDipStatus string

const (
	LabDipPending   TechCardLabDipStatus = "pending"
	LabDipSubmitted TechCardLabDipStatus = "submitted"
	LabDipApproved  TechCardLabDipStatus = "approved"
	LabDipRejected  TechCardLabDipStatus = "rejected"
)

// ValidTechCardLabDipStatuses is the set of accepted lab-dip statuses.
var ValidTechCardLabDipStatuses = map[TechCardLabDipStatus]bool{
	LabDipPending:   true,
	LabDipSubmitted: true,
	LabDipApproved:  true,
	LabDipRejected:  true,
}

// IsValidTechCardLabDipStatus reports whether s is an accepted lab-dip status.
func IsValidTechCardLabDipStatus(s TechCardLabDipStatus) bool {
	return ValidTechCardLabDipStatuses[s]
}

// TechCardColorway is a development colourway (Sheet «Колористика»).
type TechCardColorway struct {
	Id                 int                  `db:"id"`
	Code               sql.NullString       `db:"code"`
	Name               string               `db:"name"`
	LabDipStatus       TechCardLabDipStatus `db:"lab_dip_status"`
	ProductId          sql.NullInt32        `db:"product_id"`
	Comment            sql.NullString       `db:"comment"`
	Pantone            sql.NullString       `db:"pantone"`
	PantoneSystem      sql.NullString       `db:"pantone_system"`
	Hex                sql.NullString       `db:"hex"`
	SwatchMediaId      sql.NullInt32        `db:"swatch_media_id"`
	LabDipRound        sql.NullInt32        `db:"lab_dip_round"`
	LabDipSubmittedAt  sql.NullTime         `db:"lab_dip_submitted_at"`
	LabDipDecidedAt    sql.NullTime         `db:"lab_dip_decided_at"`
	LabDipDecidedBy    sql.NullString       `db:"lab_dip_decided_by"`
	LabDipRejectReason sql.NullString       `db:"lab_dip_reject_reason"`
}

// TechCardBomColorwayColor is the colour of a BOM material in a colourway. On
// write, ColorwayIndex points into TechCardInsert.Colorways (full-replace has no
// stable colourway ids yet); on read it is resolved from the stored colorway id.
type TechCardBomColorwayColor struct {
	ColorwayIndex int            `db:"-"`
	Color         sql.NullString `db:"color"`
	Pantone       sql.NullString `db:"pantone"`
}

// TechCardBomItem is one bill-of-materials line (Sheet «Спецификация»).
type TechCardBomItem struct {
	Id          int                 `db:"id"`
	Section     TechCardBomSection  `db:"section"`
	Name        string              `db:"name"`
	Placement   sql.NullString      `db:"placement"`
	Supplier    sql.NullString      `db:"supplier"`
	SupplierRef sql.NullString      `db:"supplier_ref"`
	Color       sql.NullString      `db:"color"`
	Composition sql.NullString      `db:"composition"`
	Spec        sql.NullString      `db:"spec"`
	Consumption decimal.NullDecimal `db:"consumption"`
	Unit        sql.NullString      `db:"unit"`
	Quantity    decimal.NullDecimal `db:"quantity"`
	UnitPrice   decimal.NullDecimal `db:"unit_price"`
	Currency    sql.NullString      `db:"currency"`
	Comment     sql.NullString      `db:"comment"`
	// fabric data for the cutter / marker (Phase 3.5c)
	FabricWidth     decimal.NullDecimal `db:"fabric_width"`
	FabricWeightGsm decimal.NullDecimal `db:"fabric_weight_gsm"`
	FabricDirection sql.NullString      `db:"fabric_direction"`
	WastagePercent  decimal.NullDecimal `db:"wastage_percent"`
	// ColorwayColors are the per-colourway colours (in-memory; persisted to
	// tech_card_bom_colorway).
	ColorwayColors []TechCardBomColorwayColor `db:"-"`
	// SizeConsumptions is the per-size material rate (in-memory; persisted to
	// tech_card_bom_consumption). When non-empty it grades fabric usage per size.
	SizeConsumptions []TechCardBomSizeConsumption `db:"-"`
}

// TechCardBomSizeConsumption is the per-size consumption (норма расхода) of a BOM
// material — different sizes consume different amounts of fabric.
type TechCardBomSizeConsumption struct {
	SizeId      int             `db:"size_id"`
	Consumption decimal.Decimal `db:"consumption"`
}

// TechCardFabricDirection enumerates the cutting layout a fabric requires.
type TechCardFabricDirection string

const (
	FabricDirectionAny    TechCardFabricDirection = "any"
	FabricDirectionOneWay TechCardFabricDirection = "one_way"
	FabricDirectionTwoWay TechCardFabricDirection = "two_way"
)

var ValidTechCardFabricDirections = map[TechCardFabricDirection]bool{
	FabricDirectionAny: true, FabricDirectionOneWay: true, FabricDirectionTwoWay: true,
}

// LineTotal returns quantity*unit_price (falling back to consumption*unit_price
// when quantity is unset), grossed up by wastage_percent when set. Invalid (no
// price) yields an invalid NullDecimal.
func (b *TechCardBomItem) LineTotal() decimal.NullDecimal {
	if !b.UnitPrice.Valid {
		return decimal.NullDecimal{}
	}
	qty := b.Quantity
	if !qty.Valid {
		qty = b.Consumption
	}
	if !qty.Valid {
		return decimal.NullDecimal{}
	}
	total := qty.Decimal.Mul(b.UnitPrice.Decimal)
	if b.WastagePercent.Valid {
		total = total.Mul(decimal.NewFromInt(1).Add(b.WastagePercent.Decimal.Div(decimal.NewFromInt(100))))
	}
	return decimal.NullDecimal{Decimal: total, Valid: true}
}

// SizeRunTotal returns the total material cost over the whole size run when this line has
// per-size consumption: Σ(consumption_size × order_qty_size) × unit_price, grossed up by
// wastage_percent. orderQtyBySize maps size_id → order quantity (a size with no order
// quantity contributes nothing). It returns an INVALID NullDecimal when there is no
// per-size consumption, no unit_price, or no order quantities at all — so the costing
// rollup can fall back to the per-garment LineTotal (the user's "if per-size empty → old
// formula" rule, extended to "no quantities yet → old formula"). NOTE: a per-size line's
// run total is order-scale, whereas LineTotal is per-garment; a costing that mixes both
// (some lines per-size, some not) mixes scales — kept per-line by explicit choice.
func (b *TechCardBomItem) SizeRunTotal(orderQtyBySize map[int]int) decimal.NullDecimal {
	if len(b.SizeConsumptions) == 0 || !b.UnitPrice.Valid {
		return decimal.NullDecimal{}
	}
	totalQty := decimal.Zero
	for _, sc := range b.SizeConsumptions {
		qty, ok := orderQtyBySize[sc.SizeId]
		if !ok || qty <= 0 {
			continue
		}
		totalQty = totalQty.Add(sc.Consumption.Mul(decimal.NewFromInt(int64(qty))))
	}
	if totalQty.IsZero() {
		return decimal.NullDecimal{}
	}
	total := totalQty.Mul(b.UnitPrice.Decimal)
	if b.WastagePercent.Valid {
		total = total.Mul(decimal.NewFromInt(1).Add(b.WastagePercent.Decimal.Div(decimal.NewFromInt(100))))
	}
	return decimal.NullDecimal{Decimal: total, Valid: true}
}

// TechCardSizeQuantity is the production order quantity for a size (size run).
type TechCardSizeQuantity struct {
	SizeId   int `db:"size_id"`
	OrderQty int `db:"order_qty"`
}

// TechCardSizePattern is a final PDF cut pattern (выкройка) for one size of a tech card.
type TechCardSizePattern struct {
	SizeId    int            `db:"size_id"`
	URL       string         `db:"url"`
	Filename  sql.NullString `db:"filename"`
	SizeBytes sql.NullInt64  `db:"size_bytes"`
}

// TechCardPomGrade is the graded value of a POM point for a size.
type TechCardPomGrade struct {
	SizeId int             `db:"size_id"`
	Value  decimal.Decimal `db:"value"`
}

// TechCardPomActual is an actual measured value, optionally from a fitting and at
// a specific size (so it can be compared to that size's grade ± tolerance).
type TechCardPomActual struct {
	FittingId sql.NullInt32   `db:"fitting_id"`
	SizeId    sql.NullInt32   `db:"size_id"`
	Label     sql.NullString  `db:"label"`
	Value     decimal.Decimal `db:"value"`
}

// TechCardPomPoint is a point of measure with its grade and actuals (Sheet «Измерения»).
type TechCardPomPoint struct {
	Id             int                 `db:"id"`
	Section        sql.NullString      `db:"section"`
	Code           sql.NullString      `db:"code"`
	Name           string              `db:"name"`
	HowToMeasure   sql.NullString      `db:"how_to_measure"`
	BaseValue      decimal.NullDecimal `db:"base_value"`
	TolerancePlus  decimal.NullDecimal `db:"tolerance_plus"`
	ToleranceMinus decimal.NullDecimal `db:"tolerance_minus"`
	Grades         []TechCardPomGrade  `db:"-"`
	Actuals        []TechCardPomActual `db:"-"`
}

// TechCardLabelType classifies a label/tag. Mirrors the common.TechCardLabelType
// proto enum; stored as a string in tech_card_label.label_type.
type TechCardLabelType string

const (
	LabelTypeMain    TechCardLabelType = "main"
	LabelTypeSize    TechCardLabelType = "size"
	LabelTypeCare    TechCardLabelType = "care"
	LabelTypeOrigin  TechCardLabelType = "origin"
	LabelTypeFlag    TechCardLabelType = "flag"
	LabelTypeHangtag TechCardLabelType = "hangtag"
	LabelTypeBarcode TechCardLabelType = "barcode"
	LabelTypeSpecial TechCardLabelType = "special"
)

// ValidTechCardLabelTypes is the set of accepted label types.
var ValidTechCardLabelTypes = map[TechCardLabelType]bool{
	LabelTypeMain:    true,
	LabelTypeSize:    true,
	LabelTypeCare:    true,
	LabelTypeOrigin:  true,
	LabelTypeFlag:    true,
	LabelTypeHangtag: true,
	LabelTypeBarcode: true,
	LabelTypeSpecial: true,
}

// IsValidTechCardLabelType reports whether t is an accepted label type.
func IsValidTechCardLabelType(t TechCardLabelType) bool {
	return ValidTechCardLabelTypes[t]
}

// TechCardConstruction holds general workmanship parameters (Sheet «Обработка», 1:1).
type TechCardConstruction struct {
	MainStitchType  sql.NullString `db:"main_stitch_type"`
	StitchDensity   sql.NullString `db:"stitch_density"`
	OverlockThreads sql.NullString `db:"overlock_threads"`
	SeamAllowances  sql.NullString `db:"seam_allowances"`
	HemFinish       sql.NullString `db:"hem_finish"`
	Pressing        sql.NullString `db:"pressing"`
	MachineClass    sql.NullString `db:"machine_class"`
	Notes           sql.NullString `db:"notes"`
	// labour rate (Phase 3.5b): per-minute cost; × Σ(operation SAM) = labour cost.
	LabourRate         decimal.NullDecimal `db:"labour_rate"`
	LabourRateCurrency sql.NullString      `db:"labour_rate_currency"`
}

// TechCardOperationType is the machine / stitch class of an operation. Mirrors the
// common.TechCardOperationType proto enum; stored as a string in
// tech_card_operation.operation_type ("unknown" when unset).
type TechCardOperationType string

const (
	OpTypeUnknown      TechCardOperationType = "unknown"
	OpTypeLockstitch   TechCardOperationType = "lockstitch"
	OpTypeDoubleNeedle TechCardOperationType = "double_needle"
	OpTypeOverlock     TechCardOperationType = "overlock"
	OpTypeCoverstitch  TechCardOperationType = "coverstitch"
	OpTypeChainstitch  TechCardOperationType = "chainstitch"
	OpTypeBlindhem     TechCardOperationType = "blindhem"
	OpTypeBartack      TechCardOperationType = "bartack"
	OpTypeButtonhole   TechCardOperationType = "buttonhole"
	OpTypeButtonAttach TechCardOperationType = "button_attach"
	OpTypeFusing       TechCardOperationType = "fusing"
	OpTypeHandwork     TechCardOperationType = "handwork"
	OpTypeOther        TechCardOperationType = "other"
)

// ValidTechCardOperationTypes is the set of accepted operation types (excluding the
// "unknown" default, which is applied implicitly when unset).
var ValidTechCardOperationTypes = map[TechCardOperationType]bool{
	OpTypeLockstitch: true, OpTypeDoubleNeedle: true, OpTypeOverlock: true,
	OpTypeCoverstitch: true, OpTypeChainstitch: true, OpTypeBlindhem: true,
	OpTypeBartack: true, OpTypeButtonhole: true, OpTypeButtonAttach: true,
	OpTypeFusing: true, OpTypeHandwork: true, OpTypeOther: true,
}

// TechCardConstructionZone is the display-grouping band of an operation. Mirrors the
// common.TechCardConstructionZone proto enum; stored as a string in
// tech_card_operation.zone ("unknown" when unset).
type TechCardConstructionZone string

const (
	ZoneUnknown     TechCardConstructionZone = "unknown"
	ZoneOuter       TechCardConstructionZone = "outer"
	ZoneLining      TechCardConstructionZone = "lining"
	ZoneInterlining TechCardConstructionZone = "interlining"
	ZoneOther       TechCardConstructionZone = "other"
)

// ValidTechCardConstructionZones is the set of accepted zones (excluding the
// "unknown" default, which is applied implicitly when unset).
var ValidTechCardConstructionZones = map[TechCardConstructionZone]bool{
	ZoneOuter: true, ZoneLining: true, ZoneInterlining: true, ZoneOther: true,
}

// TechCardOperation is one per-node sewing operation (Sheet «Обработка»).
type TechCardOperation struct {
	OperationNumber sql.NullInt32       `db:"operation_number"`
	Node            string              `db:"node"`
	Description     sql.NullString      `db:"description"`
	SeamType        sql.NullString      `db:"seam_type"`
	Machine         sql.NullString      `db:"machine"`
	StitchesPerCm   decimal.NullDecimal `db:"stitches_per_cm"`
	TopstitchWidth  sql.NullString      `db:"topstitch_width"`
	SeamAllowance   sql.NullString      `db:"seam_allowance"`
	Thread          sql.NullString      `db:"thread"`
	Needle          sql.NullString      `db:"needle"`
	Attachment      sql.NullString      `db:"attachment"`
	TimeNorm        decimal.NullDecimal `db:"time_norm"`
	Note            sql.NullString      `db:"note"`
	// classification + links (Phase 3.5d)
	OperationType TechCardOperationType    `db:"operation_type"` // machine/stitch class; "unknown" = unset
	Zone          TechCardConstructionZone `db:"zone"`           // display-grouping band; "unknown" = unset
	// BomItemIndex is the 0-based index into the submitted bom_items of the material
	// this operation applies; NULL = no reference (index 0 is a valid reference).
	BomItemIndex sql.NullInt32 `db:"bom_item_index"`
	// CalloutNumber links the operation to a TechCardCallout.number; NULL/0 = none.
	CalloutNumber sql.NullInt32 `db:"callout_number"`
}

// TechCardIssueSeverity / TechCardIssueStatus classify a maker-flagged issue.
type TechCardIssueSeverity string

const (
	IssueSeverityLow    TechCardIssueSeverity = "low"
	IssueSeverityMedium TechCardIssueSeverity = "medium"
	IssueSeverityHigh   TechCardIssueSeverity = "high"
)

var ValidTechCardIssueSeverities = map[TechCardIssueSeverity]bool{
	IssueSeverityLow: true, IssueSeverityMedium: true, IssueSeverityHigh: true,
}

type TechCardIssueStatus string

const (
	IssueStatusOpen     TechCardIssueStatus = "open"
	IssueStatusResolved TechCardIssueStatus = "resolved"
	IssueStatusWontfix  TechCardIssueStatus = "wontfix"
)

var ValidTechCardIssueStatuses = map[TechCardIssueStatus]bool{
	IssueStatusOpen: true, IssueStatusResolved: true, IssueStatusWontfix: true,
}

// TechCardIssue is a maker-flagged construction problem (Sheet «Обработка»).
type TechCardIssue struct {
	OperationNumber sql.NullInt32         `db:"operation_number"`
	CalloutNumber   sql.NullInt32         `db:"callout_number"`
	RaisedBy        sql.NullString        `db:"raised_by"`
	Severity        TechCardIssueSeverity `db:"severity"`
	Status          TechCardIssueStatus   `db:"status"`
	Description     string                `db:"description"`
	ResolutionNote  sql.NullString        `db:"resolution_note"`
}

// TechCardSignoffSection / TechCardSignoffState classify a per-section sign-off.
type TechCardSignoffSection string

const (
	SignoffDesign       TechCardSignoffSection = "design"
	SignoffConstruction TechCardSignoffSection = "construction"
	SignoffPom          TechCardSignoffSection = "pom"
	SignoffMaterials    TechCardSignoffSection = "materials"
	SignoffColour       TechCardSignoffSection = "colour"
	SignoffLabels       TechCardSignoffSection = "labels"
	SignoffPackaging    TechCardSignoffSection = "packaging"
	SignoffCosting      TechCardSignoffSection = "costing"
)

var ValidTechCardSignoffSections = map[TechCardSignoffSection]bool{
	SignoffDesign: true, SignoffConstruction: true, SignoffPom: true, SignoffMaterials: true,
	SignoffColour: true, SignoffLabels: true, SignoffPackaging: true, SignoffCosting: true,
}

type TechCardSignoffState string

const (
	SignoffStatePending  TechCardSignoffState = "pending"
	SignoffStateApproved TechCardSignoffState = "approved"
	SignoffStateRejected TechCardSignoffState = "rejected"
)

var ValidTechCardSignoffStates = map[TechCardSignoffState]bool{
	SignoffStatePending: true, SignoffStateApproved: true, SignoffStateRejected: true,
}

// TechCardSignoff records one responsible role's sign-off of a sheet.
type TechCardSignoff struct {
	Section  TechCardSignoffSection `db:"section"`
	State    TechCardSignoffState   `db:"state"`
	SignedBy sql.NullString         `db:"signed_by"`
	SignedAt sql.NullTime           `db:"signed_at"`
	Note     sql.NullString         `db:"note"`
}

// TechCardLabel is one label/tag spec (Sheet «Этикетки и упаковка»).
type TechCardLabel struct {
	LabelType  TechCardLabelType `db:"label_type"`
	Content    sql.NullString    `db:"content"`
	Placement  sql.NullString    `db:"placement"`
	Attachment sql.NullString    `db:"attachment"`
	Size       sql.NullString    `db:"size"`
	Note       sql.NullString    `db:"note"`
}

// TechCardPackaging holds the packaging spec (Sheet «Этикетки и упаковка», 1:1).
type TechCardPackaging struct {
	FoldingMethod sql.NullString      `db:"folding_method"`
	Polybag       sql.NullString      `db:"polybag"`
	BagSticker    sql.NullString      `db:"bag_sticker"`
	Inserts       sql.NullString      `db:"inserts"`
	UnitsPerBox   sql.NullInt32       `db:"units_per_box"`
	BoxMarking    sql.NullString      `db:"box_marking"`
	BoxDimensions sql.NullString      `db:"box_dimensions"`
	WeightNet     decimal.NullDecimal `db:"weight_net"`
	WeightGross   decimal.NullDecimal `db:"weight_gross"`
	Notes         sql.NullString      `db:"notes"`
}

// TechCardCosting holds the manually-entered cost articles (Sheet «Калькуляция», 1:1).
// The materials rollup and total are computed on read (see dto), not stored.
type TechCardCosting struct {
	CmtCost          decimal.NullDecimal `db:"cmt_cost"`
	HardwareCost     decimal.NullDecimal `db:"hardware_cost"`
	PackagingCost    decimal.NullDecimal `db:"packaging_cost"`
	LogisticsCost    decimal.NullDecimal `db:"logistics_cost"`
	OverheadCost     decimal.NullDecimal `db:"overhead_cost"`
	DefectPercent    decimal.NullDecimal `db:"defect_percent"`
	MarkupMultiplier decimal.NullDecimal `db:"markup_multiplier"`
	WholesalePrice   decimal.NullDecimal `db:"wholesale_price"`
	RetailPrice      decimal.NullDecimal `db:"retail_price"`
	Currency         sql.NullString      `db:"currency"`
	Notes            sql.NullString      `db:"notes"`
}

// TechCardInsert is the writable payload for a tech card (header + construction
// description + child sections). Child slices are full replacements on update.
type TechCardInsert struct {
	StyleNumber       string                  `db:"style_number"`
	Name              string                  `db:"name"`
	Brand             sql.NullString          `db:"brand"`
	Season            sql.NullString          `db:"season"`
	Collection        sql.NullString          `db:"collection"`
	CategoryId        sql.NullInt32           `db:"category_id"`
	TargetGender      sql.NullString          `db:"target_gender"`
	Stage             TechCardStage           `db:"stage"`
	Status            sql.NullString          `db:"status"`
	ApprovalState     TechCardApprovalState   `db:"approval_state"`
	ApprovedBy        sql.NullString          `db:"approved_by"`
	ApprovedAt        sql.NullTime            `db:"approved_at"`
	ReleasedAt        sql.NullTime            `db:"released_at"`
	Version           sql.NullString          `db:"version"`
	RevisionDate      sql.NullTime            `db:"revision_date"`
	BaseModelId       sql.NullInt32           `db:"base_model_id"`
	BaseSampleSizeId  sql.NullInt32           `db:"base_sample_size_id"`
	Designer          sql.NullString          `db:"designer"`
	Constructor       sql.NullString          `db:"constructor"`
	Technologist      sql.NullString          `db:"technologist"`
	TargetCost        decimal.NullDecimal     `db:"target_cost"`
	TargetRetailPrice decimal.NullDecimal     `db:"target_retail_price"`
	Currency          sql.NullString          `db:"currency"`
	MeasurementUnit   TechCardMeasurementUnit `db:"measurement_unit"`
	// construction description
	Description  sql.NullString `db:"description"`
	Concept      sql.NullString `db:"concept"`
	Silhouette   sql.NullString `db:"silhouette"`
	Collar       sql.NullString `db:"collar"`
	Fastening    sql.NullString `db:"fastening"`
	Pockets      sql.NullString `db:"pockets"`
	SleeveCuff   sql.NullString `db:"sleeve_cuff"`
	ExtraDetails sql.NullString `db:"extra_details"`
	Topstitching sql.NullString `db:"topstitching"`
	AuxMaterials sql.NullString `db:"aux_materials"`
	Notes        sql.NullString `db:"notes"`
	// child sections (in-memory only; persisted to their own tables)
	SizeIds    []int               `db:"-"`
	ProductIds []int               `db:"-"`
	Media      []TechCardMediaItem `db:"-"`
	Callouts   []TechCardCallout   `db:"-"`
	Revisions  []TechCardRevision  `db:"-"`
	// materials (Phase 2)
	BomItems  []TechCardBomItem  `db:"-"`
	Colorways []TechCardColorway `db:"-"`
	PomPoints []TechCardPomPoint `db:"-"`
	// production (Phase 3); 1:1 sections are nil when unset
	Construction   *TechCardConstruction  `db:"-"`
	Operations     []TechCardOperation    `db:"-"`
	Labels         []TechCardLabel        `db:"-"`
	Packaging      *TechCardPackaging     `db:"-"`
	Costing        *TechCardCosting       `db:"-"`
	Issues         []TechCardIssue        `db:"-"`
	SizeQuantities []TechCardSizeQuantity `db:"-"`
	Signoffs       []TechCardSignoff      `db:"-"`
	Patterns       []TechCardSizePattern  `db:"-"`
}

// TechCardListFilter holds optional filters for listing tech cards. Empty/zero
// fields mean "no filter".
type TechCardListFilter struct {
	Stage     string // tech_card.stage exact match
	Gender    string // tech_card.target_gender exact match
	Brand     string // case-insensitive substring on brand
	Season    string // case-insensitive substring on season
	Name      string // case-insensitive substring on name or style_number
	ProductId int    // only cards linked to this product
}

// TechCard is a stored tech card (tech_card row + child sections + resolved media).
type TechCard struct {
	Id          int `db:"id"`
	LockVersion int `db:"lock_version"`
	TechCardInsert
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	// ResolvedMedia carries the sketch media with their MediaFull resolved.
	ResolvedMedia []TechCardMediaFull `db:"-"`
}
