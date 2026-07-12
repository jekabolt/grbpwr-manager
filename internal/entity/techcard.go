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

// TechCardMediaCategory is which of the two sketch lists a media item belongs to:
// moodboard (mood / inspiration / reference) vs technical (flat sketches used in
// construction). Stored as a string in tech_card_media.category; the item's Kind is
// the within-list sub-classifier.
type TechCardMediaCategory string

const (
	TechCardMediaCategoryMoodboard TechCardMediaCategory = "moodboard"
	TechCardMediaCategoryTechnical TechCardMediaCategory = "technical"
)

// TechCardMediaItem is a writable sketch-media reference (id + category + kind).
type TechCardMediaItem struct {
	MediaId  int                   `db:"media_id"`
	Category TechCardMediaCategory `db:"category"`
	Kind     TechCardMediaKind     `db:"kind"`
	Caption  sql.NullString        `db:"caption"`
}

// TechCardMediaFull is a resolved sketch-media reference for display.
type TechCardMediaFull struct {
	Media    MediaFull
	Category TechCardMediaCategory
	Kind     TechCardMediaKind
	Caption  sql.NullString
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
	BomSectionTrim        TechCardBomSection = "trim"       // soft trims (бейка / тесьма / резинка / кант / шнур / лента)
	BomSectionDecoration  TechCardBomSection = "decoration" // принт / вышивка / аппликация / патч / стразы
	BomSectionOther       TechCardBomSection = "other"      // прочее (catch-all)
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
	BomSectionDecoration:  true,
	BomSectionOther:       true,
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
	// Usages is the colour's material recipe (in-memory; persisted to
	// tech_card_colorway_usage). Each entry binds a catalog BOM article to a garment
	// part, the colour it takes in this colourway, and its consumption.
	Usages []TechCardColorwayUsage `db:"-"`
}

// TechCardColorwayUsage is one material use inside a colourway: which catalog article
// (BomItemIndex) goes on which garment part (Placement), the colour it takes here, and
// how much is consumed (per-garment Consumption/Quantity and/or per-size). The BOM is a
// pure article catalog; per-colourway divergence lives here.
type TechCardColorwayUsage struct {
	Id           int                 `db:"id"`
	BomItemIndex sql.NullInt32       `db:"bom_item_index"` // 0-based index into the submitted bom_items; NULL = unset
	Placement    sql.NullString      `db:"placement"`
	Color        sql.NullString      `db:"color"`
	Pantone      sql.NullString      `db:"pantone"`
	Consumption  decimal.NullDecimal `db:"consumption"` // per-garment rate (measured materials)
	Quantity     decimal.NullDecimal `db:"quantity"`    // count (countable trims)
	// SizeConsumptions is the per-size material rate (in-memory; persisted to
	// tech_card_colorway_usage_consumption). When non-empty it grades usage per size.
	SizeConsumptions []TechCardBomSizeConsumption `db:"-"`
}

// LineTotal is the usage's per-garment material cost, resolved against its catalog
// article (bom). It is INVALID (the cost moves to SizeRunTotal) when the usage has
// per-size consumption. A countable trim (Quantity, no Consumption) is Quantity ×
// unit_price with no wastage; a measured material is Consumption × unit_price grossed
// up by the article's wastage_percent.
func (u *TechCardColorwayUsage) LineTotal(bom *TechCardBomItem) decimal.NullDecimal {
	if len(u.SizeConsumptions) > 0 || bom == nil || !bom.UnitPrice.Valid {
		return decimal.NullDecimal{}
	}
	if u.Quantity.Valid {
		return decimal.NullDecimal{Decimal: u.Quantity.Decimal.Mul(bom.UnitPrice.Decimal), Valid: true}
	}
	if !u.Consumption.Valid {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: applyWastage(u.Consumption.Decimal.Mul(bom.UnitPrice.Decimal), bom.WastagePercent), Valid: true}
}

// SizeRunTotal is the usage's whole-run material cost when it has per-size consumption:
// Σ(consumption_size × order_qty_size) × unit_price, grossed up by the article's
// wastage_percent. orderQtyBySize maps size_id → order quantity (a size with no order
// quantity contributes nothing). INVALID when there is no per-size consumption, no
// unit_price, or no order quantities yet (the cost is then 0, per the costing rule).
func (u *TechCardColorwayUsage) SizeRunTotal(bom *TechCardBomItem, orderQtyBySize map[int]int) decimal.NullDecimal {
	if len(u.SizeConsumptions) == 0 || bom == nil || !bom.UnitPrice.Valid {
		return decimal.NullDecimal{}
	}
	totalQty := decimal.Zero
	for _, sc := range u.SizeConsumptions {
		qty, ok := orderQtyBySize[sc.SizeId]
		if !ok || qty <= 0 {
			continue
		}
		totalQty = totalQty.Add(sc.Consumption.Mul(decimal.NewFromInt(int64(qty))))
	}
	if totalQty.IsZero() {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: applyWastage(totalQty.Mul(bom.UnitPrice.Decimal), bom.WastagePercent), Valid: true}
}

// EffectiveTotal is the usage's contribution to the materials rollup: its whole-run
// SizeRunTotal when it has per-size consumption (order-scale), otherwise its per-garment
// LineTotal. Mirrors the «per-size if present, else per-garment» rule applied per usage.
func (u *TechCardColorwayUsage) EffectiveTotal(bom *TechCardBomItem, orderQtyBySize map[int]int) decimal.NullDecimal {
	if rt := u.SizeRunTotal(bom, orderQtyBySize); rt.Valid {
		return rt
	}
	return u.LineTotal(bom)
}

// UnitTotal is the usage's PER-GARMENT material cost for costing. A per-garment usage
// (measured Consumption or countable Quantity) uses its LineTotal directly. A usage graded
// ONLY per size has no single per-garment rate, so its per-garment figure is the whole-run
// SizeRunTotal divided by totalOrderQty (a qty-weighted average) — this keeps per-unit and
// per-order on ONE scale, since unit × totalOrderQty recovers the run. INVALID when neither
// is available (e.g. per-size only with no order quantities yet).
func (u *TechCardColorwayUsage) UnitTotal(bom *TechCardBomItem, orderQtyBySize map[int]int, totalOrderQty int) decimal.NullDecimal {
	if lt := u.LineTotal(bom); lt.Valid {
		return lt
	}
	if totalOrderQty > 0 {
		if rt := u.SizeRunTotal(bom, orderQtyBySize); rt.Valid {
			return decimal.NullDecimal{Decimal: rt.Decimal.Div(decimal.NewFromInt(int64(totalOrderQty))), Valid: true}
		}
	}
	return decimal.NullDecimal{}
}

// applyWastage grosses a base cost up by wastage_percent when set (× (1 + pct/100)).
func applyWastage(base decimal.Decimal, wastagePercent decimal.NullDecimal) decimal.Decimal {
	if !wastagePercent.Valid {
		return base
	}
	return base.Mul(decimal.NewFromInt(1).Add(wastagePercent.Decimal.Div(decimal.NewFromInt(100))))
}

// TechCardBomItem is one bill-of-materials line — a catalog article (Sheet
// «Спецификация»). The per-colourway colour, placement and consumption live on
// TechCardColorwayUsage; the BOM line is a pure material-article catalog entry.
type TechCardBomItem struct {
	Id          int                 `db:"id"`
	Section     TechCardBomSection  `db:"section"`
	Name        string              `db:"name"`
	Supplier    sql.NullString      `db:"supplier"`
	SupplierRef sql.NullString      `db:"supplier_ref"`
	Color       sql.NullString      `db:"color"` // base/reference colour (per-colourway colour is on the usage)
	Composition sql.NullString      `db:"composition"`
	Spec        sql.NullString      `db:"spec"`
	Unit        sql.NullString      `db:"unit"`
	UnitPrice   decimal.NullDecimal `db:"unit_price"`
	Currency    sql.NullString      `db:"currency"`
	Comment     sql.NullString      `db:"comment"`
	// fabric data for the cutter / marker (Phase 3.5c)
	FabricWidth     decimal.NullDecimal `db:"fabric_width"`
	FabricWeightGsm decimal.NullDecimal `db:"fabric_weight_gsm"`
	FabricDirection sql.NullString      `db:"fabric_direction"`
	WastagePercent  decimal.NullDecimal `db:"wastage_percent"`
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

// TechCardDetail is one aspect of the construction description (Sheet «Титул», lower
// block) with optional reference images. Replaces the flat construction-description
// strings (silhouette/collar/fastening/…); Key is freeform.
type TechCardDetail struct {
	Id       int            `db:"id"`
	Key      sql.NullString `db:"detail_key"`  // aspect name (silhouette/collar/…); freeform
	Text     sql.NullString `db:"detail_text"` // the description for this aspect
	MediaIds []int          `db:"-"`           // FK media(id); persisted to tech_card_detail_media
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
	// this operation applies; NULL = no reference (index 0 is a valid reference). When
	// set it wins; otherwise the material resolves via Placement against the selected
	// colourway's usages.
	BomItemIndex sql.NullInt32 `db:"bom_item_index"`
	// CalloutNumber links the operation to a TechCardCallout.number; NULL/0 = none.
	CalloutNumber sql.NullInt32 `db:"callout_number"`
	// Placement is the garment part this operation works on; resolves the real material
	// via the selected colourway's usages (normalized trim+lower match). NULL = unset.
	Placement sql.NullString `db:"placement"`
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
	SignoffMaterials    TechCardSignoffSection = "materials"
	SignoffColour       TechCardSignoffSection = "colour"
	SignoffLabels       TechCardSignoffSection = "labels"
	SignoffPackaging    TechCardSignoffSection = "packaging"
	SignoffCosting      TechCardSignoffSection = "costing"
)

var ValidTechCardSignoffSections = map[TechCardSignoffSection]bool{
	SignoffDesign: true, SignoffConstruction: true, SignoffMaterials: true,
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

// TechCardCosting holds the manually-entered per-unit cost articles (Sheet
// «Калькуляция», 1:1), all in a single currency. The materials line and the unit/order
// totals are computed on read (see dto), not stored. Pricing (markup/wholesale/retail)
// was removed — it lives on the published product.
type TechCardCosting struct {
	CmtCost       decimal.NullDecimal `db:"cmt_cost"`
	HardwareCost  decimal.NullDecimal `db:"hardware_cost"`
	PackagingCost decimal.NullDecimal `db:"packaging_cost"`
	LogisticsCost decimal.NullDecimal `db:"logistics_cost"`
	OverheadCost  decimal.NullDecimal `db:"overhead_cost"`
	DefectPercent decimal.NullDecimal `db:"defect_percent"`
	Currency      sql.NullString      `db:"currency"`
	Notes         sql.NullString      `db:"notes"`
}

// CostingFxRate is a manual FX rate used to fold a multi-currency tech-card costing into the
// base currency. RateToBase is how many base-currency units one unit of Currency is worth; the
// latest ValidFrom on or before today is the effective rate.
type CostingFxRate struct {
	Currency   string          `db:"currency"`
	RateToBase decimal.Decimal `db:"rate_to_base"`
	ValidFrom  time.Time       `db:"valid_from"`
}

// TechCardInsert is the writable payload for a tech card (header + child sections).
// Child slices are full replacements on update. The construction description lives in
// Details; the header carries no cost targets (pricing is on Costing).
type TechCardInsert struct {
	StyleNumber      string                  `db:"style_number"`
	Name             string                  `db:"name"`
	Brand            sql.NullString          `db:"brand"`
	Season           sql.NullString          `db:"season"`
	Collection       sql.NullString          `db:"collection"`
	CategoryId       sql.NullInt32           `db:"category_id"`
	TargetGender     sql.NullString          `db:"target_gender"`
	Stage            TechCardStage           `db:"stage"`
	Status           sql.NullString          `db:"status"`
	ApprovalState    TechCardApprovalState   `db:"approval_state"`
	ApprovedBy       sql.NullString          `db:"approved_by"`
	ApprovedAt       sql.NullTime            `db:"approved_at"`
	ReleasedAt       sql.NullTime            `db:"released_at"`
	Version          sql.NullString          `db:"version"`
	RevisionDate     sql.NullTime            `db:"revision_date"`
	BaseModelId      sql.NullInt32           `db:"base_model_id"`
	BaseSampleSizeId sql.NullInt32           `db:"base_sample_size_id"`
	Designer         sql.NullString          `db:"designer"`
	Constructor      sql.NullString          `db:"constructor"`
	Technologist     sql.NullString          `db:"technologist"`
	MeasurementUnit  TechCardMeasurementUnit `db:"measurement_unit"`
	Concept          sql.NullString          `db:"concept"` // design concept / intent (designer)
	Notes            sql.NullString          `db:"notes"`
	// child sections (in-memory only; persisted to their own tables)
	SizeIds    []int               `db:"-"`
	ProductIds []int               `db:"-"`
	Media      []TechCardMediaItem `db:"-"`
	Callouts   []TechCardCallout   `db:"-"`
	Revisions  []TechCardRevision  `db:"-"`
	Details    []TechCardDetail    `db:"-"` // construction-description aspects (+ media)
	// materials (Phase 2)
	BomItems  []TechCardBomItem  `db:"-"` // article catalog
	Colorways []TechCardColorway `db:"-"` // colourways carry the usage recipe
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
