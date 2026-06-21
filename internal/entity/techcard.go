package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

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
	TechCardMediaFront   TechCardMediaKind = "front"
	TechCardMediaBack    TechCardMediaKind = "back"
	TechCardMediaDetail  TechCardMediaKind = "detail"
	TechCardMediaLining  TechCardMediaKind = "lining"
	TechCardMediaPreview TechCardMediaKind = "preview"
)

// ValidTechCardMediaKinds is the set of accepted sketch-media kinds.
var ValidTechCardMediaKinds = map[TechCardMediaKind]bool{
	TechCardMediaFront:   true,
	TechCardMediaBack:    true,
	TechCardMediaDetail:  true,
	TechCardMediaLining:  true,
	TechCardMediaPreview: true,
}

// IsValidTechCardMediaKind reports whether k is an accepted media kind.
func IsValidTechCardMediaKind(k TechCardMediaKind) bool {
	return ValidTechCardMediaKinds[k]
}

// TechCardMediaItem is a writable sketch-media reference (id + kind).
type TechCardMediaItem struct {
	MediaId int               `db:"media_id"`
	Kind    TechCardMediaKind `db:"kind"`
}

// TechCardMediaFull is a resolved sketch-media reference for display.
type TechCardMediaFull struct {
	Media MediaFull
	Kind  TechCardMediaKind
}

// TechCardCallout is a numbered detail note pointing at the technical sketch.
type TechCardCallout struct {
	Number      int            `db:"callout_number"`
	Part        sql.NullString `db:"part"`
	Description sql.NullString `db:"description"`
	Dimensions  sql.NullString `db:"dimensions"`
	MediaId     sql.NullInt32  `db:"media_id"` // sketch this callout is pinned to
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
	Id           int                  `db:"id"`
	Code         sql.NullString       `db:"code"`
	Name         string               `db:"name"`
	LabDipStatus TechCardLabDipStatus `db:"lab_dip_status"`
	ProductId    sql.NullInt32        `db:"product_id"`
	Comment      sql.NullString       `db:"comment"`
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
	// ColorwayColors are the per-colourway colours (in-memory; persisted to
	// tech_card_bom_colorway).
	ColorwayColors []TechCardBomColorwayColor `db:"-"`
}

// LineTotal returns quantity*unit_price, falling back to consumption*unit_price
// when quantity is unset. Invalid (no price) yields an invalid NullDecimal.
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
	return decimal.NullDecimal{Decimal: qty.Decimal.Mul(b.UnitPrice.Decimal), Valid: true}
}

// TechCardPomGrade is the graded value of a POM point for a size.
type TechCardPomGrade struct {
	SizeId int             `db:"size_id"`
	Value  decimal.Decimal `db:"value"`
}

// TechCardPomActual is an actual measured value, optionally from a fitting.
type TechCardPomActual struct {
	FittingId sql.NullInt32   `db:"fitting_id"`
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
}

// TechCardOperation is one per-node sewing operation (Sheet «Обработка»).
type TechCardOperation struct {
	Node           string              `db:"node"`
	Description    sql.NullString      `db:"description"`
	SeamType       sql.NullString      `db:"seam_type"`
	StitchesPerCm  decimal.NullDecimal `db:"stitches_per_cm"`
	TopstitchWidth sql.NullString      `db:"topstitch_width"`
	Thread         sql.NullString      `db:"thread"`
	Note           sql.NullString      `db:"note"`
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
	Construction *TechCardConstruction `db:"-"`
	Operations   []TechCardOperation   `db:"-"`
	Labels       []TechCardLabel       `db:"-"`
	Packaging    *TechCardPackaging    `db:"-"`
	Costing      *TechCardCosting      `db:"-"`
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
	Id int `db:"id"`
	TechCardInsert
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	// ResolvedMedia carries the sketch media with their MediaFull resolved.
	ResolvedMedia []TechCardMediaFull `db:"-"`
}
