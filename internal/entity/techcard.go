package entity

import (
	"database/sql"
	"errors"
	"strings"
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
// purpose (sellable↔auxiliary) after something downstream has committed to the old answer: a SOLD
// colourway (final), a non-cancelled production run, a still-live colourway, or use as an assembly
// component (NF-07). Until the first sale the flip stays open — mis-filing a dust bag as a garment
// is a data-entry mistake the operator must be able to correct. The store WRAPS this sentinel with
// the references that actually pin the card ("...: 1 live colourway linked to it"), because the bare
// rule made an operator whose card has no runs at all read the message as wrong rather than as
// "archive the colourway first".
var ErrTechCardPurposeLocked = errors.New("tech card purpose cannot change while the card is still referenced")

// ErrTechCardNotAuxiliary is returned by the output-variant writes when the target card is a
// SELLABLE style. A colour variant is a bucket in the material warehouse, which is the one place a
// sellable card must never produce into — its colours are colourways (products, SKUs, product
// stock). The API layer maps it to FailedPrecondition.
var ErrTechCardNotAuxiliary = errors.New("colour variants are only for auxiliary tech cards")

// ErrOutputVariantMaterialClaimed is returned when the material a variant wants to produce into is
// already the output bucket of another variant (uniq_tcov_material). One bucket must belong to one
// colour of one card or its moving average blends two physically different articles and every cost
// derived from it becomes a lie. Nothing stopped N legacy cards from sharing one
// tech_card.output_material_id, so an "adopt this material as a colour" flow WILL hit this. The API
// layer maps it to FailedPrecondition; the store wraps it with the card that already holds the claim.
var ErrOutputVariantMaterialClaimed = errors.New("material is already the output of another colour variant")

// ErrOutputVariantUnitMismatch is returned when a card's variant materials would end up measured in
// different units. A run's received quantity is booked per variant but counted once, so mixing pcs
// and metres across one card's buckets makes the run total meaningless — and material.unit freezes
// on the first movement (ErrMaterialUnitLocked), so a wrong unit caught later may be unrepairable.
// The API layer maps it to FailedPrecondition.
//
// KNOWN HOLE: this is enforced at claim time only. checkMaterialUnitChange freezes a material's unit
// once it has movements, so a bucket that has never been received into can still have its unit
// edited in the materials admin afterwards, drifting a card back into a mixed state behind this
// guard's back. Phase 3 revisits it when receipts start booking per variant (that is the first point
// where the mixed state does real arithmetic damage rather than sitting inert).
var ErrOutputVariantUnitMismatch = errors.New("all colour variants of a card must share one unit of measure")

// ErrOutputVariantNotFound is returned when the addressed variant row does not exist (or has already
// been deleted). The API layer maps it to NotFound.
var ErrOutputVariantNotFound = errors.New("colour variant not found")

// ErrOutputVariantReferencedByRun is returned when a colour a production run has planned into is
// asked to be deleted (0253's fk_prl_output_variant RESTRICTs it too, as a 1451 that names no way
// forward). Deactivation is the retirement that keeps the run's grid, the colour's warehouse bucket
// and its history intact. The API layer maps it to FailedPrecondition.
var ErrOutputVariantReferencedByRun = errors.New("colour variant is referenced by production run lines; deactivate it instead")

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

// techCardStageOrder is the lifecycle ordinal of each stage (idea=0 … prod=5): a higher number is a
// later stage. It is the single source of truth for telling a forward stage move from a backward
// ("regressing") one — the development-board pipeline (GetStylePipeline) renders its columns in this
// same order.
var techCardStageOrder = map[TechCardStage]int{
	TechCardStageIdea:  0,
	TechCardStageProto: 1,
	TechCardStageFit:   2,
	TechCardStageSMS:   3,
	TechCardStagePP:    4,
	TechCardStageProd:  5,
}

// TechCardStageOrdinal returns the lifecycle position of s (idea=0 … prod=5) and whether s is a
// known stage. A move to a strictly smaller ordinal is a backward (regressing) transition; a move
// to an equal-or-greater ordinal is a same-stage or forward transition.
func TechCardStageOrdinal(s TechCardStage) (int, bool) {
	o, ok := techCardStageOrder[s]
	return o, ok
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

// TechCardOutputVariantInsert is the writable payload of one colour variant of an AUXILIARY card's
// warehouse output (tech_card_output_variant, migration 0252): "this card, in this colour, produces
// into that material". MaterialId 0 on create asks the store to auto-create the bucket from the
// card and the colour; on update it means "leave the bucket where it is".
type TechCardOutputVariantInsert struct {
	// Id addresses an EXISTING row; 0 asks for a create. It lives on the insert rather than beside
	// it because this is a single-row upsert, not the full-replace shape the assembly bill uses: a
	// variant becomes the FK target of a production-run line (phase 3), so its identity must survive
	// an edit instead of being re-minted by a delete-all + re-insert.
	Id         int    `db:"id"`
	ColorCode  string `db:"color_code"`
	MaterialId int    `db:"material_id"`
	// Active is the normal retirement switch: a discontinued colour stops being plannable and stops
	// counting toward the card's list totals, but keeps its bucket, its stock and its history.
	Active bool `db:"active"`
}

// TechCardOutputVariant is a stored colour variant with the read-only identity resolved for display:
// the colour's name, the bucket's name/unit, and its current on-hand balance. Zero variants on a
// card is legacy single-output mode (tech_card.output_material_id) and is not represented here.
type TechCardOutputVariant struct {
	TechCardOutputVariantInsert
	TechCardId int `db:"tech_card_id"`
	// ColorName / MaterialName / Unit are JOIN projections (color, material) — read-only, never
	// written. OnHand is LEFT JOINed from material_stock and stays INVALID when the bucket has no
	// stock row at all, because "no balance recorded" is not the same statement as "none left".
	ColorName    string              `db:"color_name"`
	MaterialName string              `db:"material_name"`
	Unit         string              `db:"unit"`
	OnHand       decimal.NullDecimal `db:"on_hand"`
	CreatedBy    string              `db:"created_by"`
	UpdatedBy    string              `db:"updated_by"`
	CreatedAt    time.Time           `db:"created_at"`
	UpdatedAt    time.Time           `db:"updated_at"`
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

// TechCardRevision is one entry in the server-stamped auto-journal (Q1): who/what/when across a
// card's significant transitions. Author/Action/Section/ChangeNote/CreatedAt are set by the server.
type TechCardRevision struct {
	Author     sql.NullString `db:"author"`      // server-stamped acting admin username
	Section    sql.NullString `db:"section"`     // header|sketch|bom|... (enum-valued)
	Action     sql.NullString `db:"action"`      // created|updated|approved|released|reverted|role_assigned|other
	ChangeNote sql.NullString `db:"change_note"` // human summary
	CreatedAt  sql.NullTime   `db:"created_at"`  // when the server stamped this entry
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
	// CostPrice is the colourway's own COGS (product.cost_price) with its provenance. Costing is
	// otherwise style-level, so this is the only place the tech-card read can say that one colourway
	// costs more than another. Money: stripped without costing:read, like the rest of the read.
	CostPrice          decimal.NullDecimal `db:"cost_price"`
	CostPriceSource    sql.NullString      `db:"cost_price_source"`
	CostPriceUpdatedAt sql.NullTime        `db:"cost_price_updated_at"`
	// Prices is the colourway's retail price list (product_price), loaded alongside the colourway so a
	// margin can be drawn without fanning GetColorwayByID out over every colourway of the style.
	Prices []ColorwayPrice `db:"-"`
	// LabDipRounds is the colourway's lab-dip round journal (product_lab_dip_round), oldest first. The
	// LabDip* scalars above are its latest entry.
	LabDipRounds []ColorwayLabDipRound `db:"-"`
	// BaseSku and Status are populated on the style read path (enrichMaterials) so GetStyle can emit
	// the derived AdminColorwayRef (R1/§3.3). BaseSku is NULL for an unminted draft colourway.
	BaseSku sql.NullString `db:"sku"`
	Status  ColorwayStatus `db:"lifecycle_status"`
	// LockVersion is the colourway's optimistic-lock token surfaced on the derived AdminColorwayRef: it
	// is the parent style's shared tech_card.lock_version (R2/R4), NOT a product column. It is populated
	// in enrichMaterials from the owning card and echoed by the admin into
	// UpdateColorwayRequest.expected_colorway_version for a safe optimistic-locked lab-dip write.
	LockVersion int `db:"-"`
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
	Id int `db:"id"`
	// BomItemId is the real FK to the referenced BOM line (S2/S3). It is the durable reference the
	// store resolves and writes; BomItemIndex is the legacy positional reference kept during the
	// transition (dropped in M3). PieceId is the equivalent FK replacing PieceIndex.
	BomItemId sql.NullInt64 `db:"bom_item_id"`
	PieceId   sql.NullInt64 `db:"piece_id"`
	// BomLineKey is the wire reference used by the recipe write-path: the stable line_key of the
	// style's BOM line this usage consumes. The store resolves it to BomItemId. Not persisted (db:"-").
	BomLineKey string `db:"-"`
	// PieceLineKey is the wire reference to the cut-piece this usage's consumption norm is about: the
	// stable line_key of the style's tech_card_piece (WS4). The store resolves it to PieceId, the real
	// FK (usage.piece_id RESTRICT). It replaces the positional PieceIndex, kept for the transition.
	PieceLineKey string              `db:"-"`
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
	// MaterialId pins the CONCRETE catalog article this colourway takes for the slot (the BOM
	// line is the role; the pin is the article). NULL = inherit the slot default
	// (bom_item.material_id), so a later default change keeps propagating to colourways that
	// never diverged. FK material(id) ON DELETE RESTRICT (0221).
	MaterialId sql.NullInt64 `db:"material_id"`
	// MaterialIdSet mirrors the wire field's presence (proto3 `optional`): false = the client
	// did not send material_id at all — an old client's full-replace recipe write must PRESERVE
	// the existing pin; true = MaterialId is authoritative (invalid/0 explicitly clears the pin).
	// Not persisted.
	MaterialIdSet bool `db:"-"`
}

// EffectiveMaterialId resolves the article this usage actually consumes: the colourway's pin
// when set, else the slot default carried by the resolved BOM line. 0 = no article at all
// (an unfilled slot — the caller must surface it, not skip it silently). A pin EQUAL to the
// slot default reports pinned=false: it behaves as the default everywhere (snapshot pricing,
// no "(pinned)" marker), matching pinShadowBom — one rule, not two.
func (u *TechCardColorwayUsage) EffectiveMaterialId(bom *TechCardBomItem) (id int, pinned bool) {
	if u.MaterialId.Valid && u.MaterialId.Int64 > 0 {
		if bom != nil && bom.MaterialId.Valid && bom.MaterialId.Int64 == u.MaterialId.Int64 {
			return int(u.MaterialId.Int64), false
		}
		return int(u.MaterialId.Int64), true
	}
	if bom != nil && bom.MaterialId.Valid {
		return int(bom.MaterialId.Int64), false
	}
	return 0, false
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
	// LineKey is the BOM line's stable wire identity (S2/S3): a client-generated ULID assigned when
	// the line is first created in the UI (before the first save), immutable thereafter. The server
	// keyed-reconciles by line_key so the row's id survives edits — that stable id is what lets
	// operations/pieces/colorway-usages hold a real FK instead of a fragile positional index. Empty
	// on a legacy payload; the store then generates one on insert.
	LineKey string `db:"line_key"`
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
	// Stored price provenance (production-costing Phase 3): where unit_price came from and when it
	// was stamped. Server-owned — set by the save path ('manual' when the price changes hands
	// through UpdateTechCard) and by the reprice action ('catalog'); NULL on pre-provenance rows.
	// Deliberately NOT part of the signed MATERIALS digest projection: metadata about a value must
	// not stale a sign-off whose value did not change.
	PriceSource     sql.NullString `db:"price_source"`
	PriceSnapshotAt sql.NullTime   `db:"price_snapshot_at"`
}

// BOM price provenance values (tech_card_bom_item.price_source).
const (
	BomPriceSourceManual  = "manual"  // typed/edited through a card save
	BomPriceSourceCatalog = "catalog" // pulled from the material catalog by RepriceTechCardBom
)

// RepricedBomLine is one catalog-linked BOM line the reprice action visited: the price it had, the
// price the catalog resolves to now (invalid when the catalog has no usable current price), and
// whether the stored line actually changed.
type RepricedBomLine struct {
	LineKey     string
	Name        string
	Section     TechCardBomSection
	OldPrice    decimal.NullDecimal
	OldCurrency string
	NewPrice    decimal.NullDecimal
	NewCurrency string
	Changed     bool
}

// CostingMigrationException is one row of the Phase 2 scalar→BOM migration's exception report —
// hardware/packaging money migration 0237 refused to move mechanically, waiting for a manual
// transfer into the BOM (read-only; the table is populated by the migration alone).
type CostingMigrationException struct {
	TechCardId    int             `db:"tech_card_id"`
	StyleNumber   sql.NullString  `db:"style_number"`
	TechCardName  string          `db:"tech_card_name"`
	Article       string          `db:"article"`
	Kind          string          `db:"kind"`
	Amount        decimal.Decimal `db:"amount"`
	Currency      sql.NullString  `db:"currency"`
	ApprovalState string          `db:"approval_state"`
	CreatedAt     time.Time       `db:"created_at"`
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

// TechCardSizePattern is a final cut pattern (выкройка) file — PDF or DXF, told apart by
// the url's extension — for one size of a tech card.
type TechCardSizePattern struct {
	SizeId   int            `db:"size_id"`
	URL      string         `db:"url"`
	Filename sql.NullString `db:"filename"`
	// Name is the operator-entered display name. On WRITE the null state is proto presence,
	// not emptiness: Valid=false means the field was absent from the payload (a stale client)
	// and the store carries the stored name forward by (size_id, url); Valid=true means write
	// as given, with an empty string clearing the name (stored as NULL).
	Name      sql.NullString `db:"name"`
	SizeBytes sql.NullInt64  `db:"size_bytes"`
	// Version is the sheet's revision within its (style, size). 0 on the wire means "assign one":
	// patterns are a full-replace child, so the store re-derives this on every save from the rows it
	// is about to delete — a url it has seen keeps its number, a new one takes MAX+1.
	Version int `db:"version"`
	// UploadedAt is when the PDF was first attached. Server-owned: carried across the full-replace by
	// matching the url, so it means what it says instead of drifting to "last card save".
	UploadedAt sql.NullTime `db:"uploaded_at"`
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

// TechCardAuxSubtype sub-classifies an AUXILIARY tech card (purpose=auxiliary) into the concrete kind
// of non-sold item it produces. It refines tech_card.purpose (an auxiliary card produces a MATERIAL via
// output_material_id and has no product row) and is stored nullable in tech_card.aux_subtype. Mirrors the
// common.TechCardAuxSubtype proto enum and the DB CHECK chk_tech_card_aux_subtype (migration 0173).
type TechCardAuxSubtype string

const (
	AuxSubtypeBrandLabel TechCardAuxSubtype = "brand_label"
	AuxSubtypeCareLabel  TechCardAuxSubtype = "care_label"
	AuxSubtypeSizeLabel  TechCardAuxSubtype = "size_label"
	AuxSubtypeHangtag    TechCardAuxSubtype = "hangtag"
	AuxSubtypeSticker    TechCardAuxSubtype = "sticker"
	AuxSubtypeDustBag    TechCardAuxSubtype = "dust_bag"
	// AuxSubtypeGarmentCase is a кофр — the carrier a garment travels/hangs in. Distinct from a dust
	// bag: it is cut, sewn and costed as its own item, and an assembly bill has to name which of the
	// two a style ships with (migration 0227).
	AuxSubtypeGarmentCase TechCardAuxSubtype = "garment_case"
	AuxSubtypeBox         TechCardAuxSubtype = "box"
	AuxSubtypeInsert      TechCardAuxSubtype = "insert"
	AuxSubtypeHanger      TechCardAuxSubtype = "hanger"
	AuxSubtypeOther       TechCardAuxSubtype = "other"
)

// ValidTechCardAuxSubtypes is the closed set enforced by the DB CHECK; it backs the entity<->DB drift
// test (internal/store/migrationlint) against migration 0173's chk_tech_card_aux_subtype.
var ValidTechCardAuxSubtypes = map[TechCardAuxSubtype]bool{
	AuxSubtypeBrandLabel:  true,
	AuxSubtypeCareLabel:   true,
	AuxSubtypeSizeLabel:   true,
	AuxSubtypeHangtag:     true,
	AuxSubtypeSticker:     true,
	AuxSubtypeDustBag:     true,
	AuxSubtypeGarmentCase: true,
	AuxSubtypeBox:         true,
	AuxSubtypeInsert:      true,
	AuxSubtypeHanger:      true,
	AuxSubtypeOther:       true,
}

// IsValidTechCardAuxSubtype reports whether s is an accepted auxiliary sub-type.
func IsValidTechCardAuxSubtype(s TechCardAuxSubtype) bool {
	return ValidTechCardAuxSubtypes[s]
}

// AuxSubtypeFromName is the deterministic name → sub-type heuristic used to backfill EXISTING auxiliary
// cards. It MUST stay identical to migration 0173's backfill CASE (first matching branch wins,
// most-specific first); ok=false means "no confident match" and the caller leaves aux_subtype NULL rather
// than guessing. Matching is case-insensitive substring.
func AuxSubtypeFromName(name string) (TechCardAuxSubtype, bool) {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "dust"):
		return AuxSubtypeDustBag, true
	case strings.Contains(n, "hangtag"), strings.Contains(n, "hang tag"), strings.Contains(n, "hang-tag"):
		return AuxSubtypeHangtag, true
	case strings.Contains(n, "care"):
		return AuxSubtypeCareLabel, true
	case strings.Contains(n, "size label"), strings.Contains(n, "size-label"):
		return AuxSubtypeSizeLabel, true
	case strings.Contains(n, "brand"):
		return AuxSubtypeBrandLabel, true
	case strings.Contains(n, "sticker"):
		return AuxSubtypeSticker, true
	case strings.Contains(n, "hanger"):
		return AuxSubtypeHanger, true
	case strings.Contains(n, "insert"):
		return AuxSubtypeInsert, true
	case strings.Contains(n, "box"):
		return AuxSubtypeBox, true
	case strings.Contains(n, "shopper"), strings.Contains(n, "garment bag"):
		return AuxSubtypeDustBag, true
	default:
		return "", false
	}
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
	SMV             decimal.NullDecimal `db:"smv"` // standard minute value; NULL = unset
	Note            sql.NullString      `db:"note"`
	// classification + links (Phase 3.5d)
	OperationType TechCardOperationType    `db:"operation_type"` // machine/stitch class; "unknown" = unset
	Zone          TechCardConstructionZone `db:"zone"`           // display-grouping band; "unknown" = unset
	// BomItemId is the real FK to the referenced BOM line (S2/S3), resolved and written by the store;
	// BomItemIndex is the legacy positional reference kept during the transition (dropped in M3).
	BomItemId sql.NullInt64 `db:"bom_item_id"`
	// BomLineKey is the wire reference to that BOM line by its stable line_key (WS3 follow-up:
	// positionality off the wire). The store resolves it to BomItemId; not persisted (db:"-").
	BomLineKey string `db:"-"`
	// BomItemIndex is the 0-based index into the submitted bom_items of the material
	// this operation applies; NULL = no reference (index 0 is a valid reference). When
	// set it wins; otherwise the material resolves via Placement against the selected
	// colourway's usages.
	BomItemIndex sql.NullInt32 `db:"bom_item_index"`
	// CalloutNumber links the operation to a TechCardCallout.number; NULL/0 = none.
	CalloutNumber sql.NullInt32 `db:"callout_number"`
	// Placement is the garment part this operation works on; resolves the real material
	// via the selected colourway's usages (normalized trim+lower match). NULL = unset.
	// It is a human LABEL, not a join key -- PieceIds below is the real reference.
	Placement sql.NullString `db:"placement"`
	// PieceLineKeys is the wire reference to the cut-pieces this operation works on, by their stable
	// TechCardPiece.line_key (WS4). The store resolves them to PieceIds. Not persisted (db:"-").
	PieceLineKeys []string `db:"-"`
	// PieceIds are the resolved tech_card_piece FKs, held in the tech_card_operation_piece join
	// table (0199) rather than a column: an assembly operation spans as many pieces as it joins.
	// This is deliberately many-to-many, unlike TechCardColorwayUsage.PieceId, which is 1:1 because
	// a consumption norm is about exactly one piece. Not persisted on the row itself (db:"-").
	PieceIds []int `db:"-"`
	// BomLineKeys / BomIds are the off-part materials this operation consumes (thread, fusing), held
	// in tech_card_operation_bom (0200). Many-to-many for the same reason the piece links are: one
	// operation can join several materials. The legacy single BomLineKey/BomItemId above stays as
	// the first entry during the transition. Not persisted on the row itself (db:"-").
	BomLineKeys []string `db:"-"`
	BomIds      []int    `db:"-"`
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
	// SignedDigest fingerprints the section's content at the moment it was approved, so a stale
	// approval survives a reload (dto.TechCardSectionDigests). Server-owned: stamped on the way in,
	// never accepted from the wire. NULL for a pending/rejected section; an approved legacy row with
	// no digest is unverifiable and therefore stale for release-readiness purposes.
	SignedDigest sql.NullString `db:"signed_digest"`
}

// TechCardLabel is one label/tag spec (Sheet «Этикетки и упаковка»).
type TechCardLabel struct {
	LabelType  TechCardLabelType `db:"label_type"`
	Content    sql.NullString    `db:"content"`
	Placement  sql.NullString    `db:"placement"`
	Attachment sql.NullString    `db:"attachment"`
	Size       sql.NullString    `db:"size"`
	Note       sql.NullString    `db:"note"`
	// BomItemId links this free-text label SPEC to the physical label MATERIAL's BOM line
	// (tech_card_bom_item), the §2.8 S21-unification bridge. NULL = unlinked. FK ON DELETE SET NULL.
	BomItemId sql.NullInt32 `db:"bom_item_id"`
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
	// hardware_cost / packaging_cost were removed in production-costing Phase 2: both duplicated
	// first-class BOM sections priced through the per-colourway recipe (migration 0237 moved draft
	// cards' scalars into synthetic BOM lines; the columns are retained for the exception report but
	// are dead to the application).
	CmtCost       decimal.NullDecimal `db:"cmt_cost"`
	LogisticsCost decimal.NullDecimal `db:"logistics_cost"`
	OverheadCost  decimal.NullDecimal `db:"overhead_cost"`
	DefectPercent decimal.NullDecimal `db:"defect_percent"`
	Currency      sql.NullString      `db:"currency"`
	Notes         sql.NullString      `db:"notes"`
	// TargetMarginPct is the gross margin THIS style is expected to make (0..100). Unset falls back to
	// the house default in alert_setting, resolved onto the read as effective_target_margin_pct.
	TargetMarginPct decimal.NullDecimal `db:"target_margin_pct"`
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
	Id         int `db:"id"`
	ColorwayID int `db:"colorway_id"` // explicit colourway id = product.id
	// BomItemId / FusingBomItemId are the real FKs to the fabric / fusing BOM lines (S2/S3), resolved
	// and written by the store; the *Index columns are the legacy positional refs kept for the
	// transition (dropped in M3).
	BomItemId          sql.NullInt64 `db:"bom_item_id"`
	FusingBomItemId    sql.NullInt64 `db:"fusing_bom_item_id"`
	BomItemIndex       sql.NullInt32 `db:"bom_item_index"`        // 0-based index into bom_items (the fabric); NULL = unset
	FusingBomItemIndex sql.NullInt32 `db:"fusing_bom_item_index"` // 0-based index into bom_items (the fusing); NULL = none
	// BomLineKey / FusingBomLineKey are the wire references to the fabric / fusing BOM line by stable
	// line_key (WS3 follow-up); the store resolves them to BomItemId / FusingBomItemId. Not persisted.
	BomLineKey       string         `db:"-"`
	FusingBomLineKey string         `db:"-"`
	Note             sql.NullString `db:"note"`
}

// TechCardPiece is one structural cut-piece of the garment (полочка, спинка, обтачка…): how many
// per garment, whether mirrored/paired, its grainline (долевая) and whether it is fused (клеевая).
// Materials picks, per colourway, which BOM fabric it is cut from. Keyed-upserted child of the card:
// LineKey is the stable client token the store reconciles by (S8, mirrors BOM's line_key in §2.3), so
// a piece's id stays stable across saves — which is what lets a colourway recipe usage hold a real
// piece_id FK RESTRICT (the deferred half of 0159).
type TechCardPiece struct {
	Id   int    `db:"id"`
	Name string `db:"name"`
	// LineKey is the client-generated ULID assigned when the piece is created in the UI (before the
	// first save); immutable; the wire identity the upsert-diff keys on. Empty on a legacy/keyless
	// payload → the store mints one.
	LineKey          string        `db:"line_key"`
	PiecesPerGarment int           `db:"pieces_per_garment"`
	Mirrored         bool          `db:"mirrored"` // Q6: the piece is CUT AS A MIRRORED PAIR (not a decorative flag); the cut-list expands it ×2.
	Grainline        string        `db:"grainline"`
	Fused            bool          `db:"fused"`
	CalloutNumber    sql.NullInt32 `db:"callout_number"`
	// Detached is set by the store when the piece's callout_number no longer resolves to a callout on
	// the card (its source sketch callout was removed): the piece survives, visibly detached, instead
	// of being silently dropped (orphan-control, S8). Output-only; clients do not set it.
	Detached  bool                    `db:"detached"`
	Note      sql.NullString          `db:"note"`
	Materials []TechCardPieceMaterial `db:"-"`
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
	// AuxSubtype sub-classifies an auxiliary card (brand_label/care_label/…/other); NULL for sellable
	// cards and for unclassified auxiliary ones. Only meaningful when Purpose == auxiliary (DB gate
	// chk_tech_card_aux_subtype_purpose). Additive (WS7); the sellable path never reads it.
	AuxSubtype sql.NullString `db:"aux_subtype"`
	Name       string         `db:"name"`
	Brand      sql.NullString `db:"brand"`
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
	Fit sql.NullString `db:"fit"`
	// Composition is the legacy free-text column (e.g. "100% Cotton"). M1 fix: always plain text on
	// the wire — never overloaded with the structured composition, which is TechCard.CompositionEntries.
	Composition        sql.NullString        `db:"composition"`
	CareInstructions   sql.NullString        `db:"care_instructions"`
	ModelWearsHeightCm sql.NullInt32         `db:"model_wears_height_cm"`
	ModelWearsSizeId   sql.NullInt32         `db:"model_wears_size_id"`
	TopCategoryId      sql.NullInt32         `db:"top_category_id"`
	SubCategoryId      sql.NullInt32         `db:"sub_category_id"`
	TypeId             sql.NullInt32         `db:"type_id"`
	Stage              TechCardStage         `db:"stage"`
	Status             sql.NullString        `db:"status"`
	ApprovalState      TechCardApprovalState `db:"approval_state"`
	ApprovedAt         sql.NullTime          `db:"approved_at"`
	ReleasedAt         sql.NullTime          `db:"released_at"`
	// TargetDropDate is the calendar day this style is planned to drop (production cockpit): owner-set
	// planning intent, the anchor a run's PromisedAt is judged against. Unlike ApprovedAt/ReleasedAt
	// it is client-writable, and it is a DATE column — the time of day is dropped at the dto boundary.
	TargetDropDate   sql.NullTime            `db:"target_drop_date"`
	BaseModelId      sql.NullInt32           `db:"base_model_id"`
	BaseSampleSizeId sql.NullInt32           `db:"base_sample_size_id"`
	MeasurementUnit  TechCardMeasurementUnit `db:"measurement_unit"`
	// MeasurementUnitSet separates "the client chose a unit" from "the field was absent". The unit is a
	// fact ABOUT the numbers already in tech_card_size_measurement (a bare DECIMAL with no unit of its
	// own), so an absent field must preserve the stored unit rather than fall back to the create-time
	// default — otherwise an old cm card is re-read as mm, 10× off, by any save that omits the field.
	MeasurementUnitSet bool           `db:"-"`
	Concept            sql.NullString `db:"concept"` // design concept / intent (designer)
	Notes              sql.NullString `db:"notes"`
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
	// CategoryIds narrows to cards under ANY of these category nodes, matched at ANY level of the
	// taxonomy (category_id / top / sub / type). One id, whichever level the operator picked in a
	// category browser, is enough — the client does not have to expand the tree itself. Empty = no
	// filter.
	CategoryIds []int
	// A product-linking picker passes "sellable" so auxiliary (packaging) cards, which can never
	// produce a SKU, do not clutter the choice (PR5-E).
}

// TechCardReadinessFacts is the raw state a style's readiness checklist is scored against: plain
// counts and presence flags, gathered in one round trip.
//
// It carries NO judgement on purpose. WHICH counts gate WHICH transition is studio policy that gets
// re-tuned (today "an SMS sample before pp", tomorrow maybe two), so the rules live in the apisrv
// layer and this struct stays a stable set of facts — a rule change is then a Go edit, never a
// rewrite of a twenty-subselect query.
type TechCardReadinessFacts struct {
	Stage         TechCardStage         `db:"stage"`
	ApprovalState TechCardApprovalState `db:"approval_state"`

	HasStyleNumber    bool `db:"has_style_number"` // set AND non-empty (idea-stage cards have none)
	HasCategory       bool `db:"has_category"`
	HasBaseSampleSize bool `db:"has_base_sample_size"`

	Sizes      int `db:"sizes"`
	Pieces     int `db:"pieces"`
	Operations int `db:"operations"`

	BomLines       int `db:"bom_lines"`
	BomFabricLines int `db:"bom_fabric_lines"`
	BomLinkedLines int `db:"bom_linked_lines"` // lines resolved to a catalog material

	// Sample counts EXCLUDE scrapped samples: a binned prototype is not evidence that the stage it
	// belongs to actually happened.
	Samples      int `db:"samples"`
	ProtoSamples int `db:"proto_samples"`
	FitSamples   int `db:"fit_samples"`
	SmsSamples   int `db:"sms_samples"`
	PpSamples    int `db:"pp_samples"`

	Fittings           int `db:"fittings"`
	FittingsApproved   int `db:"fittings_approved"`
	OpenChangeRequests int `db:"open_change_requests"` // fitting_change_request.status = 'open' (S26)

	// Colourways are this style's products minus the archived (soft-deleted) ones — a retired
	// colourway is not something the factory will be asked to make, so it must not gate a release.
	LiveColorways          int `db:"live_colorways"`
	LabDipPendingColorways int `db:"lab_dip_pending_colorways"`

	ProductionRuns         int `db:"production_runs"`
	ProductionRunsReceived int `db:"production_runs_received"`

	// PatternSizes counts sizes OF THE CURRENT RANGE that carry at least one pattern sheet, so a
	// leftover sheet for a size since dropped from the grade cannot fake full coverage.
	PatternSizes int `db:"pattern_sizes"`

	HasCosting         bool `db:"has_costing"`
	HasCostingCurrency bool `db:"has_costing_currency"`

	Signoffs         int `db:"signoffs"`
	SignoffsApproved int `db:"signoffs_approved"`
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
	// CompositionEntries is the style's structured fibre composition (S17/M1 fix), populated on the
	// single-card read alongside the legacy free-text Composition (TechCardInsert.Composition) — never
	// instead of it. Empty when the style has no style_composition rows yet.
	CompositionEntries []CompositionEntry `db:"-"`
	// ResolvedMedia carries the sketch media with their MediaFull resolved.
	ResolvedMedia []TechCardMediaFull `db:"-"`
	// PreviewURL is a thumbnail chosen for list/gallery views (B-9): first moodboard image for an
	// IDEA card, else the PREVIEW-kind sketch (fallback first technical, then any). Populated only by
	// ListTechCards; empty elsewhere.
	PreviewURL string `db:"-"`
	// ColorwayCount is the style's LIVE (non-archived) colourway count, resolved for list/board views
	// in one batched query per page. Populated only by the list paths; 0 elsewhere.
	ColorwayCount int `db:"-"`
	// OutputMaterialName / OutputMaterialOnHand describe an AUXILIARY card's warehouse output (its
	// run receipts into OutputMaterialId), so an aux picker can show "820 on hand" from the list
	// alone instead of one GetTechCard plus a warehouse read per card. List paths only.
	OutputMaterialName   string              `db:"-"`
	OutputMaterialOnHand decimal.NullDecimal `db:"-"`
	// OutputVariants is the card's colour dimension over that single output (0252): one warehouse
	// bucket per colour. Populated on the single-card read only; EMPTY means legacy single-output
	// mode (OutputMaterialId is then the whole answer), never "not loaded yet" on that path.
	OutputVariants []TechCardOutputVariant `db:"-"`
	// OutputVariantCount / OutputVariantsOnHand are the list-view summary of the same thing over the
	// ACTIVE variants only — "3 colours · 820 on hand" — resolved in one batched query per page like
	// ColorwayCount above. List paths only; on-hand stays INVALID when no bucket has a stock row.
	OutputVariantCount   int                 `db:"-"`
	OutputVariantsOnHand decimal.NullDecimal `db:"-"`
	// LinkedMaterials resolves every catalog article the card references — BOM slot defaults
	// (bom_item.material_id) AND colourway pins (usage.material_id) — to its identity and latest
	// price, keyed by material id. Populated on the single-card read; the costing prices a pinned
	// article, and the production plan labels/converts its rollup rows, from this one map. Nil on
	// list views and writes; every consumer must degrade to the BOM line's own snapshot fields.
	LinkedMaterials map[int]MaterialWithPrice `db:"-"`
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
