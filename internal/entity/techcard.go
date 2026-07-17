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

// ErrTechCardPurposeLocked is returned by UpdateTechCard when the caller tries to change a card's
// purpose (sellable↔auxiliary) after it already has production runs or linked products — the switch
// would strand a batch's stock destination or a product link (NF-07).
var ErrTechCardPurposeLocked = errors.New("tech card purpose cannot change once it has runs or products")

// TechCardStage is the development stage of a tech card. It mirrors the
// common.TechCardStage proto enum and is stored as a string in tech_card.stage.
type TechCardStage string

const (
	TechCardStageIdea  TechCardStage = "idea"  // draft: moodboard/concept before a style number (NF-03)
	TechCardStageProto TechCardStage = "proto" // prototype
	TechCardStageFit   TechCardStage = "fit"   // fit sample
	TechCardStageSMS   TechCardStage = "sms"   // salesman sample
	TechCardStagePP    TechCardStage = "pp"    // pre-production
	TechCardStageProd  TechCardStage = "prod"  // production
)

// ValidTechCardStages is the set of accepted tech-card stages.
var ValidTechCardStages = map[TechCardStage]bool{
	TechCardStageIdea:  true,
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

// TechCardPurpose is what a card produces: a sellable product or an auxiliary item (NF-07). It
// mirrors the common.TechCardPurpose proto enum and is stored as a string in tech_card.purpose.
type TechCardPurpose string

const (
	TechCardPurposeSellable  TechCardPurpose = "sellable"  // produces a catalog product (default)
	TechCardPurposeAuxiliary TechCardPurpose = "auxiliary" // produces a packaging material (dust bag, shopper…)
)

// ValidTechCardPurposes is the set of accepted card purposes.
var ValidTechCardPurposes = map[TechCardPurpose]bool{
	TechCardPurposeSellable:  true,
	TechCardPurposeAuxiliary: true,
}

// StyleNumberSource records how a tech card's style_number was set (Q1): `generated` = the server
// proposed it from the season+sequence contract; `manual` = the owner deliberately overrode the
// proposal (and the value passed the strict format validator). Mirrors the common.StyleNumberSource
// proto enum; stored in tech_card.style_number_source (CHECK generated|manual, DEFAULT generated).
type StyleNumberSource string

const (
	StyleNumberSourceGenerated StyleNumberSource = "generated"
	StyleNumberSourceManual    StyleNumberSource = "manual"
)

// ValidStyleNumberSources is the set of accepted provenance values.
var ValidStyleNumberSources = map[StyleNumberSource]bool{
	StyleNumberSourceGenerated: true,
	StyleNumberSourceManual:    true,
}

// IsValidStyleNumberSource reports whether s is an accepted provenance value.
func IsValidStyleNumberSource(s StyleNumberSource) bool { return ValidStyleNumberSources[s] }

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
	ColorCode          string               `db:"color_code"`
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
	// BaseSku and Status are populated on the style read path (enrichMaterials) so GetStyle can emit
	// the derived AdminColorwayRef (R1/§3.3). BaseSku is NULL for an unminted draft colourway.
	BaseSku sql.NullString `db:"sku"`
	Status  ColorwayStatus `db:"lifecycle_status"`
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
	// PieceIndex is an optional 0-based arrow into TechCardInsert.Pieces saying which cut-piece
	// this consumption norm is about; NULL = the whole garment (informational, NF-05).
	PieceIndex sql.NullInt32 `db:"piece_index"`
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
	Id int `db:"id"`
	// MaterialId optionally links this BOM line to a catalog material (task 10). The line still
	// keeps its own snapshot fields, so the card is self-contained and unaffected if the
	// catalog entry later changes; the link only powers reverse lookups (which cards use a
	// material) and admin-side pre-fill. NULL for free-text / legacy lines.
	MaterialId  sql.NullInt64       `db:"material_id"`
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
	FoldingMethod sql.NullString `db:"folding_method"`
	Polybag       sql.NullString `db:"polybag"`
	BagSticker    sql.NullString `db:"bag_sticker"`
	Inserts       sql.NullString `db:"inserts"`
	UnitsPerBox   sql.NullInt32  `db:"units_per_box"`
	BoxMarking    sql.NullString `db:"box_marking"`
	BoxDimensions sql.NullString `db:"box_dimensions"`
	// WeightNetGrams / WeightGrossGrams are the packaging weights in whole grams (0 / NULL = unset).
	// Integer grams instead of the old ambiguous DECIMAL(8,3) kilograms, so the shipping-label
	// weight derivation reads grams with no unit conversion.
	WeightNetGrams   sql.NullInt32  `db:"weight_net_grams"`
	WeightGrossGrams sql.NullInt32  `db:"weight_gross_grams"`
	Notes            sql.NullString `db:"notes"`
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

// TechCardDevExpense is one row of a style's development (R&D) cost journal (task 14): a one-off
// "spent Amount on Kind" record, not time-tracking. Amount is in Currency; AmountBase folds it to
// the base currency (via costing FX or a manual override, unset when no rate). FittingId optionally
// ties the cost to a try-on round (e.g. a sample built for that round). Development cost is a
// period cost and is never seeded into product.cost_price.
type TechCardDevExpense struct {
	Id          int                 `db:"id"`
	TechCardId  int                 `db:"tech_card_id"`
	Kind        string              `db:"kind"` // sample|materials|labour|outsourcing|other
	Description sql.NullString      `db:"description"`
	Amount      decimal.Decimal     `db:"amount"`
	Currency    string              `db:"currency"`
	AmountBase  decimal.NullDecimal `db:"amount_base"`
	FittingId   sql.NullInt32       `db:"fitting_id"`
	SampleId    sql.NullInt32       `db:"sample_id"` // optional link to a sample (NF-04)
	IncurredAt  sql.NullTime        `db:"incurred_at"`
	CreatedAt   time.Time           `db:"created_at"`
}

// CostBreakdown is the per-unit COGS decomposition in base currency (EUR): the cost articles
// that (summed and grossed up by defect%) make the unit cost seeded into product.cost_price.
// Snapshotted onto product.cost_breakdown JSON at seed time so COGS-of-sold analytics can
// attribute a period's cost of goods to materials vs CMT vs packaging etc. The component
// amounts are pre-defect (raw); defect_pct is carried alongside. A manual cost_price (no card)
// leaves cost_breakdown NULL, which the structure report honestly reports as unattributed.
type CostBreakdown struct {
	Materials decimal.Decimal `json:"materials"`
	Cmt       decimal.Decimal `json:"cmt"`
	Hardware  decimal.Decimal `json:"hardware"`
	Packaging decimal.Decimal `json:"packaging"`
	Logistics decimal.Decimal `json:"logistics"`
	Overhead  decimal.Decimal `json:"overhead"`
	DefectPct decimal.Decimal `json:"defect_pct"`
}

// CostingFxRate is a manual FX rate used to fold a multi-currency tech-card costing into the
// base currency. RateToBase is how many base-currency units one unit of Currency is worth; the
// latest ValidFrom on or before today is the effective rate.
type CostingFxRate struct {
	Currency   string          `db:"currency"`
	RateToBase decimal.Decimal `db:"rate_to_base"`
	ValidFrom  time.Time       `db:"valid_from"`
}

// ValidTechCardGrainlines is the accepted долевая set (mirrors the DB CHECK on tech_card_piece).
var ValidTechCardGrainlines = map[string]bool{
	"lengthwise": true, "crosswise": true, "bias": true, "any": true,
}

// TechCardPieceMaterial maps ONE cut-piece to its fabric (and optional fusing) for ONE colourway.
// ColorwayID is the explicit colourway id = product.id (R1/§14.3); the old positional colorway_index
// is gone (colourways are no longer style children). BOM refs stay positional into bom_items,
// consistent with usages/operations. It is a grandchild of the card (full-replace via its piece).
type TechCardPieceMaterial struct {
	Id                 int            `db:"id"`
	ColorwayID         int            `db:"colorway_id"`           // explicit colourway id = product.id
	BomItemIndex       sql.NullInt32  `db:"bom_item_index"`        // 0-based index into bom_items (the fabric); NULL = unset
	FusingBomItemIndex sql.NullInt32  `db:"fusing_bom_item_index"` // 0-based index into bom_items (the fusing); NULL = none
	Note               sql.NullString `db:"note"`
}

// TechCardPiece is one structural cut-piece of the garment (полочка, спинка, обтачка…): how many
// per garment, whether mirrored/paired, its grainline (долевая) and whether it is fused (клеевая).
// Materials picks, per colourway, which BOM fabric it is cut from. Full-replace child of the card.
type TechCardPiece struct {
	Id               int                     `db:"id"`
	Name             string                  `db:"name"`
	PiecesPerGarment int                     `db:"pieces_per_garment"`
	Mirrored         bool                    `db:"mirrored"`
	Grainline        string                  `db:"grainline"`
	Fused            bool                    `db:"fused"`
	CalloutNumber    sql.NullInt32           `db:"callout_number"`
	Note             sql.NullString          `db:"note"`
	Materials        []TechCardPieceMaterial `db:"-"`
}

// TechCardInsert is the writable payload for a tech card (header + child sections).
// Child slices are full replacements on update. The construction description lives in
// Details; the header carries no cost targets (pricing is on Costing).
type TechCardInsert struct {
	// StyleNumber is NULL for an `idea` draft (NF-03) and required from `proto` onward.
	StyleNumber sql.NullString `db:"style_number"`
	// StyleNumberSource is the provenance of StyleNumber (Q1): `generated` (server-proposed) or
	// `manual` (owner override). A manual override must pass the strict style-number format validator;
	// global UNIQUE(style_number) is the authority on collisions. Empty defaults to `generated`.
	StyleNumberSource StyleNumberSource `db:"style_number_source"`
	// CreatedBy/UpdatedBy are server-stamped audit usernames (norm §2.11, GetAdminUsername). They are
	// on the writable payload only so the store can persist them; the API never reads them from the
	// wire — the handler overwrites them — and surfaces them read-only on the TechCard message.
	CreatedBy string `db:"created_by"`
	UpdatedBy string `db:"updated_by"`
	// Purpose is `sellable` (default) or `auxiliary` (NF-07). An auxiliary card (dust bag, garment
	// bag, shopper) is not sold: its run output is received into OutputMaterialId in the material
	// warehouse, and it may not link products.
	Purpose          TechCardPurpose `db:"purpose"`
	OutputMaterialId sql.NullInt64   `db:"output_material_id"` // material an auxiliary run receipts into
	Name             string          `db:"name"`
	Brand            sql.NullString  `db:"brand"`
	// SeasonLabel is a DB-only canonical projection (e.g. SS26), derived from the normalized pair.
	// It is never accepted from the public contract.
	SeasonLabel  sql.NullString `db:"season"`
	SeasonCode   sql.NullString `db:"season_code"`
	SeasonYear   sql.NullInt32  `db:"season_year"`
	Collection   sql.NullString `db:"collection"`
	CategoryId   sql.NullInt32  `db:"category_id"`
	TargetGender sql.NullString `db:"target_gender"`
	// Garment-level catalogue fields (PR6 P2): invariant across a style's colourways (one
	// pattern, colour is the only axis that varies), so they live on the STYLE. Colourways
	// (products) read them from here; the duplicated product columns are dropped in step 3.
	// top/sub/type_category mirror the product taxonomy (all → category(id)); the legacy
	// single category_id above is a separate optional tag and is untouched.
	Fit                sql.NullString          `db:"fit"`
	Composition        sql.NullString          `db:"composition"` // JSON column
	CareInstructions   sql.NullString          `db:"care_instructions"`
	ModelWearsHeightCm sql.NullInt32           `db:"model_wears_height_cm"`
	ModelWearsSizeId   sql.NullInt32           `db:"model_wears_size_id"`
	TopCategoryId      sql.NullInt32           `db:"top_category_id"`
	SubCategoryId      sql.NullInt32           `db:"sub_category_id"`
	TypeId             sql.NullInt32           `db:"type_id"`
	Stage              TechCardStage           `db:"stage"`
	Status             sql.NullString          `db:"status"`
	ApprovalState      TechCardApprovalState   `db:"approval_state"`
	ApprovedBy         sql.NullString          `db:"approved_by"`
	ApprovedAt         sql.NullTime            `db:"approved_at"`
	ReleasedAt         sql.NullTime            `db:"released_at"`
	Version            sql.NullString          `db:"version"`
	RevisionDate       sql.NullTime            `db:"revision_date"`
	BaseModelId        sql.NullInt32           `db:"base_model_id"`
	BaseSampleSizeId   sql.NullInt32           `db:"base_sample_size_id"`
	Designer           sql.NullString          `db:"designer"`
	Constructor        sql.NullString          `db:"constructor"`
	Technologist       sql.NullString          `db:"technologist"`
	MeasurementUnit    TechCardMeasurementUnit `db:"measurement_unit"`
	Concept            sql.NullString          `db:"concept"` // design concept / intent (designer)
	Notes              sql.NullString          `db:"notes"`
	// child sections (in-memory only; persisted to their own tables)
	SizeIds   []int               `db:"-"`
	Media     []TechCardMediaItem `db:"-"`
	Callouts  []TechCardCallout   `db:"-"`
	Revisions []TechCardRevision  `db:"-"`
	Details   []TechCardDetail    `db:"-"` // construction-description aspects (+ media)
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
	Pieces         []TechCardPiece        `db:"-"` // structural cut-pieces + per-colourway fabric mapping (NF-05)
}

// TechCardListFilter holds optional filters for listing tech cards. Empty/zero
// fields mean "no filter".
type TechCardListFilter struct {
	Stage      string     // tech_card.stage exact match
	Gender     string     // tech_card.target_gender exact match
	Brand      string     // case-insensitive substring on brand
	SeasonCode SeasonEnum // exact normalized pair; empty means no season filter
	SeasonYear int
	Name       string // case-insensitive substring on name or style_number
	ProductId  int    // only cards linked to this product
	Purpose    string // tech_card.purpose exact match (sellable|auxiliary); "" = no filter.
	// A product-linking picker passes "sellable" so auxiliary (packaging) cards, which can never
	// produce a SKU, do not clutter the choice (PR5-E).
}

// TechCardRole is a responsible-account role on a tech card (Q5). Mirrors the common.TechCardRole
// proto enum; stored in tech_card_role_assignment.role (CHECK). Replaces the free-text
// designer/constructor/technologist/approved_by strings — approval is now the `approver` role plus a
// server-stamped journal event, not a free-text name.
type TechCardRole string

const (
	RoleDesigner     TechCardRole = "designer"
	RoleConstructor  TechCardRole = "constructor"
	RoleTechnologist TechCardRole = "technologist"
	RolePatternMaker TechCardRole = "pattern_maker"
	RoleGrader       TechCardRole = "grader"
	RoleApprover     TechCardRole = "approver"
	RoleOther        TechCardRole = "other"
)

// ValidTechCardRoles is the set of accepted role keys (mirrors the DB CHECK).
var ValidTechCardRoles = map[TechCardRole]bool{
	RoleDesigner: true, RoleConstructor: true, RoleTechnologist: true,
	RolePatternMaker: true, RoleGrader: true, RoleApprover: true, RoleOther: true,
}

// IsValidTechCardRole reports whether r is an accepted role.
func IsValidTechCardRole(r TechCardRole) bool { return ValidTechCardRoles[r] }

// TechCardRoleAssignment is one "this admin account is <role> of this card" record (Q5), multi per
// role. AdminUsername is resolved from admins on read (never written). AssignedBy/AssignedAt are the
// audit stamp of who created the assignment.
type TechCardRoleAssignment struct {
	Id            int          `db:"id"`
	TechCardId    int          `db:"tech_card_id"`
	Role          TechCardRole `db:"role"`
	AdminId       int          `db:"admin_id"`
	AdminUsername string       `db:"admin_username"` // resolved via JOIN admins; read-only
	AssignedBy    string       `db:"assigned_by"`
	AssignedAt    time.Time    `db:"assigned_at"`
}

// TechCard is a stored tech card (tech_card row + child sections + resolved media).
type TechCard struct {
	Id          int `db:"id"`
	LockVersion int `db:"lock_version"`
	TechCardInsert
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	// RoleAssignments is the card's responsible-account roles (Q5), populated on the single-card read
	// (GetTechCardById); empty on list views.
	RoleAssignments []TechCardRoleAssignment `db:"-"`
	// ResolvedMedia carries the sketch media with their MediaFull resolved.
	ResolvedMedia []TechCardMediaFull `db:"-"`
	// PreviewURL is a thumbnail chosen for list/gallery views (B-9): first moodboard image for an
	// IDEA card, else the PREVIEW-kind sketch (fallback first technical, then any). Populated only by
	// ListTechCards; empty elsewhere.
	PreviewURL string `db:"-"`
}

// LinkedProductIDs returns the style's live (non-archived) colourway product ids. PR6 R1: a style's
// colourways are its products; the old tech_card_product-derived ProductIds is now this projection of
// the enriched colourways (Colorways[i].ProductId is the id when the colourway is not archived, NULL
// otherwise — matching the old lifecycle_status <> 4 filter). Requires the card to be enriched.
func (tc *TechCard) LinkedProductIDs() []int {
	ids := make([]int, 0, len(tc.Colorways))
	for i := range tc.Colorways {
		if tc.Colorways[i].ProductId.Valid {
			ids = append(ids, int(tc.Colorways[i].ProductId.Int32))
		}
	}
	return ids
}

// StylePipelineColumn is one lifecycle-stage column of the development board (gap-01): the stage,
// the total number of cards in it, and a few light preview cards (most-recently-updated first).
type StylePipelineColumn struct {
	Stage TechCardStage
	Count int
	Cards []TechCard
}

// TechCardReleaseMeta is the header of an immutable release snapshot (task 11) without the
// JSON blob — used for listing a card's releases. UnitCost/Currency are the base-currency
// planned unit cost frozen at release time (NULL when it could not be folded to base).
type TechCardReleaseMeta struct {
	Id         int `db:"id"`
	TechCardId int `db:"tech_card_id"`
	// ReleaseNumber is the user-facing "Rev.N" the factory reads (Q1): auto MAX+1 per tech card,
	// assigned by the store on save. This is the tech card's real "version" — the free-text `version`
	// string it replaces is retired.
	ReleaseNumber int                 `db:"release_number"`
	Version       sql.NullString      `db:"version"`
	ReleasedBy    sql.NullString      `db:"released_by"`
	UnitCost      decimal.NullDecimal `db:"unit_cost"`
	Currency      sql.NullString      `db:"currency"`
	CreatedAt     time.Time           `db:"created_at"`
}

// TechCardRelease is a full release snapshot: the metadata plus the raw proto-JSON blob of the
// enriched contract TechCard as it stood at release. The blob is opaque to the store; callers
// parse it (and degrade gracefully on an incompatible blob, hero-v2 style). On write, Id and
// CreatedAt are DB-generated.
type TechCardRelease struct {
	TechCardReleaseMeta
	Snapshot string `db:"snapshot"`
}
