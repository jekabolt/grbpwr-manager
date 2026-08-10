package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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

// ErrMarkerNotFound is returned when the addressed saved раскладка (tech_card_marker) does not
// exist. The API layer maps it to NotFound.
var ErrMarkerNotFound = errors.New("marker not found")

// ErrMarkerIncomplete refuses saving a раскладка whose layout did not place every piece
// (placed_count < total_count): a layout that dropped pieces is not a consumption norm, and
// letting it through would quietly understate fabric per garment. FailedPrecondition.
var ErrMarkerIncomplete = errors.New("marker layout is incomplete — not every piece was placed")

// ErrMarkerUsedByLay refuses the MANUAL delete of a раскладка that a секция настила stands on
// (Ф4, §5.4). It is the application's RESTRICT in a place the schema cannot hold one:
// fk_prlays_marker is ON DELETE CASCADE, and it has to be — DeleteProductionRun is not
// transactional and relies on cascades (productionrun.go), so a RESTRICT anywhere in that tree
// would make deleting a run succeed or fail depending on the order InnoDB happened to walk it.
//
// So the invariant is held on the ONE path a human chooses deliberately: deleting the marker out
// from under a lay silently shrinks that lay's plan. The CASCADE path (the run itself being
// deleted) needs no guard — there the lays die too.
//
// The error VALUE says only what it is; the store wraps it with the настилы by name, because
// «нельзя» sends the operator looking and «стоит в настиле «BLACK · основная»» sends them to the
// screen that can free it. The API layer maps it to FailedPrecondition.
var ErrMarkerUsedByLay = errors.New("marker is used by a lay section")

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
	// MaterialArchived is the bucket's catalog state. A colour pointing at archived nomenclature must
	// never be prescribed to a packer, so the packing spec downgrades it to unresolved.
	MaterialArchived bool      `db:"material_archived"`
	CreatedBy        string    `db:"created_by"`
	UpdatedBy        string    `db:"updated_by"`
	CreatedAt        time.Time `db:"created_at"`
	UpdatedAt        time.Time `db:"updated_at"`
}

// MarkerSource is the provenance of a saved раскладка's geometry, stored in
// tech_card_marker.source. Mirrors the CHECK chk_tcm_source in migration 0257 (drift is caught by
// TestMarkerSourceEnumMatchesMigration).
type MarkerSource string

const (
	MarkerSourceAuto     MarkerSource = "auto"     // the nesting engine's layout as it ran
	MarkerSourceManual   MarkerSource = "manual"   // operator-adjusted placements (Ф5)
	MarkerSourceImported MarkerSource = "imported" // external CAD marker (reserved)
)

// ValidMarkerSources is the set of accepted marker provenance values.
var ValidMarkerSources = map[MarkerSource]bool{
	MarkerSourceAuto:     true,
	MarkerSourceManual:   true,
	MarkerSourceImported: true,
}

// TechCardMarkerInsert is the writable payload of one saved раскладка (tech_card_marker, 0257).
// Layout is the opaque proto-JSON blob of common.TechCardMarkerLayout — self-contained contours +
// placements, marshalled at the API layer (idiom: tech_card_release.snapshot). BomLineKey is the
// stable wire identity of the BOM fabric line this marker measures; the store resolves it to
// bom_item_id ("" = not linked).
type TechCardMarkerInsert struct {
	// SizeId / Sets are the ЛЕГАСИ pair (Ф2). INVALID on every marker written with a состав; carried
	// only for a payload from a STALE ADMIN BUNDLE, which is stored byte-for-byte in the old shape.
	// sql.NullInt64 rather than int is a forcing function, not decoration: 0273 made both columns
	// nullable, and an int here would have turned every reader into a runtime
	// «converting NULL to int is unsupported» — on a path where ONE such row fails the whole
	// GetTechCard read, the раскройный лист and the immutable release snapshot with it.
	SizeId     sql.NullInt64 `db:"size_id"`
	Name       string        `db:"name"`
	Source     MarkerSource  `db:"source"`
	BomLineKey string        `db:"-"`
	// ColorwayId pins the colourway whose ARTICLE this layout was measured on (0264). 0 = not
	// colourway-specific. It matters because the width does: a colourway names its own catalog
	// article per slot, and the same pieces on a 140 cm and a 150 cm roll are two different
	// markers with two different lengths.
	ColorwayId int `db:"-"`
	// ProductionRunId makes this a РАСКРОЙНЫЙ marker owned by that прогон (tech_card_marker.run_id,
	// 0282). 0 = КАРТОЧНЫЙ — the норма and every marker that existed before Ф4.
	//
	// IT IS IMMUTABLE AFTER CREATION, and the store refuses a save that would change it. The column
	// is not an attribute of the раскладка, it is WHO OWNS ITS LIFE: a run marker dies with its run
	// by FK CASCADE, is hidden from every card list, and is the only thing a секция настила may
	// point at. Flipping it either way silently re-homes those three facts — a card marker turned
	// run marker acquires an expiry date nobody asked for, and a run marker turned card marker
	// outlives the sections that reference it. Whoever needs the other kind COPIES the geometry
	// (решение Р2): a copy is one click and states its own provenance, a mutation states nothing.
	//
	// `db:"-"` because the write path names it explicitly in the INSERT and deliberately omits it
	// from the UPDATE — the same arrangement is_norm has, and for the same reason.
	ProductionRunId int             `db:"-"`
	FabricWidthCm   decimal.Decimal `db:"fabric_width_cm"`
	GapCm           decimal.Decimal `db:"gap_cm"`
	EdgeMarginCm    decimal.Decimal `db:"edge_margin_cm"`
	// SelvedgeCm snapshots the кромка (cm per edge) the layout ran with, from the effective
	// article at save time — keeps the waste decomposition auditable after material edits.
	SelvedgeCm      decimal.Decimal `db:"selvedge_cm"`
	AllowCrossGrain bool            `db:"allow_cross_grain"`
	Sets            sql.NullInt64   `db:"sets"` // ЛЕГАСИ, see SizeId
	UsedLengthCm    decimal.Decimal `db:"used_length_cm"`
	// Composition is the NORMALISED состав — after the legacy substitution in dto, so it is never
	// empty on a save that got this far, and every entry carries quantity >= 1. The store validates
	// its sizes against the card's ряд and writes it to tech_card_marker_size in the same
	// transaction as the row.
	//
	// It is the ONLY carrier of the garment count on this struct. total_units is written from
	// TotalUnitsOf(Composition) at the same moment as the child rows, so the stored divisor and its
	// own children cannot disagree — a second field here would be exactly the «two copies, which one
	// wins» question the wire deliberately refuses to have.
	Composition []MarkerCompositionEntry `db:"-"`
	// EfficiencyPct stays INVALID when the engine did not report one (a manual/imported marker) —
	// NULL in the column, unset on the wire.
	EfficiencyPct decimal.NullDecimal `db:"efficiency_pct"`
	PlacedCount   int                 `db:"placed_count"`
	TotalCount    int                 `db:"total_count"`
	Layout        string              `db:"layout"`
	// DistilStoredLayout parses a STORED layout blob into facts, injected by the API layer for the
	// same reason LayoutFacts is distilled there: the store holds the bytes and must not learn to
	// read them (0257/0268 — the geometry is opaque to storage, and the read path deliberately
	// survives a blob that does not parse, which a parser in the write path would turn into a hard
	// failure). The store calls this at most once, inside its transaction, and only when the
	// exemption is actually being considered.
	//
	// УСЛОВИЯ СЪЁМКИ (Ф3) — the rules this раскладка was measured under. ALL FIVE ARE OPTIONAL and
	// INVALID means «not recorded», never «zero»: a bundle that predates Ф3 sends none of them, and
	// such a save is accepted and becomes «старая норма» rather than being refused (Ф1 settled the
	// same argument on fabric_direction). See internal/entity/marker_conditions.go for the rules that
	// read them.
	//
	// GrainLayer's EMPTY STRING IS SIGNIFICANT and means «do not orient» — hence sql.NullString and
	// not string. Folding "" into NULL would turn a deliberate «не разворачивать» into «unknown», and
	// a rebuild would then orient pieces the operator forbade orienting.
	SeamAllowanceMm    decimal.NullDecimal `db:"seam_allowance_mm"`
	ContourAllowanceMm decimal.NullDecimal `db:"contour_allowance_mm"`
	ContourLayer       sql.NullString      `db:"contour_layer"`
	GrainLayer         sql.NullString      `db:"grain_layer"`
	AllowFlip          sql.NullBool        `db:"allow_flip"`
	// PieceSetFp is the fingerprint of the CARD's cut-piece set at save time (Ф3.6). Deliberately NOT
	// on the wire and NOT accepted from a payload: the client does not know the stored set, and a
	// client-sent fingerprint would be both forgeable and stale on any concurrent card edit. The store
	// computes it inside the save transaction, from the very rows that transaction sees.
	PieceSetFp sql.NullString `db:"piece_set_fp"`
	// It travels here rather than as an argument so the repository interface — and every mock of it
	// — stays a data contract. nil means «no stored facts available», which the rule reads as «cannot
	// forgive», never as «nothing was there».
	DistilStoredLayout func(blob string) (MarkerLayoutFacts, error) `db:"-"`
	// LayoutFacts are the handful of things the SAVE PATH has to know about the blob it is storing
	// (Ф1): its schema version, and whether any placement is upside down. Distilled at the API layer
	// and carried as facts because the blob stays opaque past it — the store persists Layout and
	// never parses it, and a second parser there would be a second definition of the format.
	LayoutFacts MarkerLayoutFacts `db:"-"`
}

// TechCardMarkerSummary is a stored marker without its layout blob — the shape that rides
// GetTechCard.markers (the blob is 60-100 KB and travels only on GetTechCardMarker). BomLineKey /
// BomItemName / BomItemUnit are JOIN projections off the linked BOM line; all three stay INVALID
// when the marker is unlinked or its slot was deleted (bom_item_id went NULL).
type TechCardMarkerSummary struct {
	Id         int `db:"id"`
	TechCardId int `db:"tech_card_id"`
	// ЛЕГАСИ (Ф2), INVALID on a marker with a состав — read Composition / TotalUnits instead. See
	// TechCardMarkerInsert.SizeId for why the type, and not just the column, had to change.
	SizeId     sql.NullInt64 `db:"size_id"`
	Name       string        `db:"name"`
	Source     string        `db:"source"`
	BomItemId  sql.NullInt64 `db:"bom_item_id"`
	ColorwayId sql.NullInt64 `db:"colorway_id"`
	// RunId is the прогон this РАСКРОЙНАЯ раскладка was taken for (tech_card_marker.run_id, 0282).
	// INVALID = КАРТОЧНЫЙ marker — the норма and every marker that existed before Ф4, and every marker
	// saved without a production_run_id since. Written once by SaveMarker at CREATE and never moved:
	// see TechCardMarkerInsert.ProductionRunId for why a change of owner is refused rather than
	// applied.
	//
	// It is projected on EVERY summary read, the card's own included, even though that read now
	// filters run markers out: the lay plan's fitness check (dto.LayMarkerScopeCheck) has to be able
	// to say «это КАРТОЧНЫЙ маркер» rather than infer ownership from the fact that some query
	// filtered on it.
	RunId           sql.NullInt64       `db:"run_id"`
	BomLineKey      sql.NullString      `db:"bom_line_key"`
	BomItemName     sql.NullString      `db:"bom_item_name"`
	BomItemUnit     sql.NullString      `db:"bom_item_unit"`
	FabricWidthCm   decimal.Decimal     `db:"fabric_width_cm"`
	GapCm           decimal.Decimal     `db:"gap_cm"`
	EdgeMarginCm    decimal.Decimal     `db:"edge_margin_cm"`
	SelvedgeCm      decimal.Decimal     `db:"selvedge_cm"`
	AllowCrossGrain bool                `db:"allow_cross_grain"`
	Sets            sql.NullInt64       `db:"sets"` // ЛЕГАСИ, see SizeId
	UsedLengthCm    decimal.Decimal     `db:"used_length_cm"`
	EfficiencyPct   decimal.NullDecimal `db:"efficiency_pct"`
	PlacedCount     int                 `db:"placed_count"`
	TotalCount      int                 `db:"total_count"`
	// TotalUnits is the stored garment count (0273). INVALID only for a row written by the OLD
	// binary during a deploy overlap — the column is nullable and the old INSERT does not list it.
	// Readers go through TotalUnitsOrLegacy, never through this field directly.
	TotalUnits sql.NullInt64 `db:"total_units"`
	// Composition comes from tech_card_marker_size in a second query (one per card read, not N+1),
	// so it is not a column of this row — hence `db:"-"`. Readers go through CompositionOrLegacy.
	Composition []MarkerCompositionEntry `db:"-"`
	// УСЛОВИЯ СЪЁМКИ (Ф3), exactly as recorded. INVALID = «not recorded», never zero; a row whose
	// SeamAllowanceMm is INVALID is «старая норма» (IsLegacyNorm below).
	SeamAllowanceMm    decimal.NullDecimal `db:"seam_allowance_mm"`
	ContourAllowanceMm decimal.NullDecimal `db:"contour_allowance_mm"`
	ContourLayer       sql.NullString      `db:"contour_layer"`
	GrainLayer         sql.NullString      `db:"grain_layer"`
	AllowFlip          sql.NullBool        `db:"allow_flip"`
	// IsNorm marks THE нормировочная раскладка of this card for its cloth. Written ONLY by
	// SetMarkerNorm — SaveMarker neither reads nor writes it, so re-saving geometry can neither seize
	// the norm nor lose it. Exclusivity within (card, bom_item_id) is held by that transaction rather
	// than by a UNIQUE index; see entity.SelectNorm for why, and for the tiebreak every reader owes.
	IsNorm bool `db:"is_norm"`
	// PieceSetFp is the fingerprint of the CARD's cut-piece set as it stood when this раскладка was
	// saved. INVALID = never recorded (a marker from before Ф3) or unfingerprintable — which readers
	// must render as UNKNOWN, never as «changed».
	PieceSetFp sql.NullString `db:"piece_set_fp"`
	// CardPieceSetFp is the fingerprint of the card's set TODAY, stamped by the store on the read
	// paths that have the pieces to hand. INVALID = today's set cannot be fingerprinted either.
	// Not a column — the comparison is a fact about the card, not about the row.
	CardPieceSetFp sql.NullString `db:"-"`
	// NormConflict is "" on a healthy card and the prose reporting «more than one norm on this cloth»
	// otherwise, stamped by the store from entity.NormConflictReport. Not a column: the conflict is a
	// property of the SET of a card's markers, and one row cannot see it.
	NormConflict string    `db:"-"`
	CreatedBy    string    `db:"created_by"`
	UpdatedBy    string    `db:"updated_by"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// Allowance is what this раскладка states about the allowance on the cloth — three-valued, see
// entity.MarkerAllowance.
func (m TechCardMarkerSummary) Allowance() MarkerAllowance {
	return MarkerAllowanceOf(m.SeamAllowanceMm, m.ContourAllowanceMm)
}

// IsLegacyNorm reports «СТАРАЯ НОРМА»: a раскладка that does not say what it laid out or under which
// rules. The category is DERIVED — there is no flag column and no migration marked anything, because
// the unmarked stays old by itself (04-marker-conditions.md). A readiness gate must not count such a
// marker as a valid measurement: it was taken along an unknown line, at a nominal width, under an
// unrecorded flip policy.
func (m TechCardMarkerSummary) IsLegacyNorm() bool { return !m.SeamAllowanceMm.Valid }

// PieceSetStatus compares the fingerprint stored with this раскладка against the card's set today.
//
// AN UNRECORDED FINGERPRINT IS NEVER «CHANGED». That is the requirement, and it is also the only way
// not to flood every marker taken before Ф3 with a badge: UNKNOWN is a grey caption («набор при
// съёмке не записан»), which is the other half of the «старая норма» category — not an alarm.
func (m TechCardMarkerSummary) PieceSetStatus() MarkerPieceSetStatus {
	if !m.PieceSetFp.Valid || m.PieceSetFp.String == "" {
		return MarkerPieceSetUnknown
	}
	if !m.CardPieceSetFp.Valid || m.CardPieceSetFp.String == "" {
		return MarkerPieceSetUnknown
	}
	if m.PieceSetFp.String == m.CardPieceSetFp.String {
		return MarkerPieceSetMatches
	}
	return MarkerPieceSetChanged
}

// ЛЕГАСИ-ФОРМА ПОБЕЖДАЕТ ПРОЕКЦИЮ, and the order of these two readers is the whole reason this
// comment exists. A row whose `sets` is VALID is BY DEFINITION a row in the pre-Ф2 shape, and the
// only writer that produces that shape and does not also write total_units + tech_card_marker_size
// is THE OLD BINARY — whose UPDATE lists `sets` and `used_length_cm` and knows neither of the other
// two. So on such a row the projection is not merely older, it is the number 0273 backfilled at
// migration time, and `sets` is what an operator changed afterwards.
//
// Reading the column first inflated costing silently and permanently: backfill leaves
// total_units = 1, the operator re-saves through the old container with sets = 4 and
// used_length_cm = 1200, and the wire then carries consumption_per_unit_cm = 1200 against a truth of
// 300 — with scalar_apply_refusal EMPTY, because a one-entry состав reads as homogeneous, so the Р2
// guard is disarmed at exactly the moment it is needed. The client copies that into
// tech_card_colorway_usage.consumption with consumption_source='marker' and nothing recomputes it.
//
// The window is not «a few minutes of rolling deploy»: if the new container fails its health check,
// DigitalOcean keeps the OLD deploy serving indefinitely, and this project has been there.
//
// There is no symmetric hazard: no writer updates total_units without also writing `sets` and the
// children, so a VALID `sets` can never be the stale half of the pair.

// TotalUnitsOrLegacy is how many GARMENTS this раскладка cuts. Sources in order: the legacy `sets`
// (authoritative whenever present — see above), then the total_units column, then 1.
//
// It cannot return 0, and that is the whole point — the old guard here was
// `if m.Sets <= 0 { return m.UsedLengthCm }`, which did not divide by zero and instead reported the
// WHOLE spread as the consumption of one garment: a silently N-fold-inflated norm on a path whose
// output a client writes straight into a recipe. Making the denominator unable to be zero removes
// the branch that produced it. The trailing 1 is an ARITHMETIC guard only — a row that reaches it
// has no состав at all, and the wire path withholds its norm rather than emitting 1 (see
// MarkerScalarNormRefusal).
func (m TechCardMarkerSummary) TotalUnitsOrLegacy() int {
	if m.Sets.Valid && m.Sets.Int64 >= 1 {
		return int(m.Sets.Int64)
	}
	if m.TotalUnits.Valid && m.TotalUnits.Int64 >= 1 {
		return int(m.TotalUnits.Int64)
	}
	return 1
}

// CompositionOrLegacy is the раскладка's состав, and it reads the LEGACY PAIR FIRST for the reason
// above: while size_id is VALID the row is in the pre-Ф2 shape and (size_id, sets) is the freshest
// statement about it — the child rows may be the migration's projection of a `sets` that has since
// changed underneath them. Once size_id goes NULL the row belongs to the Ф2 writer, which writes the
// children and the scalar in one transaction, and the projection is the only answer there is.
//
// Returning EMPTY is a real outcome, not an impossible one: a Down that dropped the projection, an
// ops delete, a partial restore. Callers must treat empty as «this раскладка no longer states what
// it cuts» and withhold — never as «one garment» (see MarkerScalarNormRefusal).
func (m TechCardMarkerSummary) CompositionOrLegacy() []MarkerCompositionEntry {
	if m.SizeId.Valid && m.SizeId.Int64 > 0 {
		return []MarkerCompositionEntry{{SizeId: int(m.SizeId.Int64), Quantity: m.TotalUnitsOrLegacy()}}
	}
	if len(m.Composition) > 0 {
		return m.Composition
	}
	return nil
}

// ConsumptionPerUnitCm is fabric length per ONE garment: used_length_cm / total_units. Derived,
// never stored, so it cannot drift from its inputs.
//
// NOT SAFE TO PUT ON THE WIRE ON ITS OWN. On a MIXED состав it is the MEAN across sizes, and on a
// раскладка that lost its состав entirely it is the whole spread. Every caller that emits it must
// consult ScalarNormRefusal() first — TechCardMarkerSummaryToPb does, and it is the only producer.
// Kept computable because the mean is still the honest answer to «how much cloth does this spread
// use per garment», which is what a length verdict and the marker list want.
func (m TechCardMarkerSummary) ConsumptionPerUnitCm() decimal.Decimal {
	return m.UsedLengthCm.Div(decimal.NewFromInt(int64(m.TotalUnitsOrLegacy())))
}

// PerSizeConsumption is this раскладка's расход per garment OF EACH SIZE of its состав (Ф2.4) — the
// measured length distributed by piece area, so a mixed настил stops having only a mean to offer.
// Derived, never stored, for the same reason ConsumptionPerUnitCm is: it is a function of
// used_length and of the состав, and a stored copy would outlive a correction to either.
//
// One entry per состав line, in the same order. An INVALID ConsumptionCm on any line means this
// раскладка cannot say — never that the mean should be used instead.
func (m TechCardMarkerSummary) PerSizeConsumption() []MarkerSizeConsumption {
	return MarkerPerSizeConsumption(m.CompositionOrLegacy(), m.UsedLengthCm)
}

// ScalarNormRefusal is "" when this marker's consumption_per_unit_cm may be applied to a recipe, and
// the reason not to otherwise. See entity.MarkerScalarNormRefusal — it reads the SAME slice
// PerSizeConsumption returns, so the refusal and the remedy it names cannot disagree.
func (m TechCardMarkerSummary) ScalarNormRefusal() string {
	return MarkerScalarNormRefusal(m.Name, m.PerSizeConsumption())
}

// TechCardMarker is a full stored marker: the summary plus the opaque layout blob.
type TechCardMarker struct {
	TechCardMarkerSummary
	Layout string `db:"layout"`
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

// TechCardBomPurpose is НАЗНАЧЕНИЕ — what the garment uses a roll-goods line FOR. It is a SECOND
// axis beside Section, not a refinement of it: a pocket-bag fabric, a contrast fabric and a mesh
// second layer are all genuinely section='fabric' (cloth sold by length, laid out on the same
// marker, grossed up by the same wastage) and differ only in role. Several lines may share one
// purpose — naming a subset of the fabrics is the whole point of the field.
//
// The list is CLOSED because the field exists to GROUP. A free-text role stops grouping the moment
// one operator writes "карманка" and the next writes "мешковина кармана"; the escape hatch is
// therefore BomPurposeOther plus a separate note, never a free-form purpose.
//
// Mirrors the common.TechCardBomPurpose proto enum and the chk_bom_item_purpose DB CHECK (0265);
// stored as a NULLABLE string in tech_card_bom_item.purpose, where NULL means "not sorted yet".
type TechCardBomPurpose string

const (
	BomPurposeMain        TechCardBomPurpose = "main"        // основной материал
	BomPurposeLining      TechCardBomPurpose = "lining"      // подкладка
	BomPurposePocketing   TechCardBomPurpose = "pocketing"   // карманка
	BomPurposeInterfacing TechCardBomPurpose = "interfacing" // бортовка / прокладка
	BomPurposeInsulation  TechCardBomPurpose = "insulation"  // утеплитель
	BomPurposeContrast    TechCardBomPurpose = "contrast"    // контраст / отделочная
	BomPurposeMesh        TechCardBomPurpose = "mesh"        // сетка / второй слой
	BomPurposeOther       TechCardBomPurpose = "other"       // другое — meaning lives in PurposeNote
)

// BomPurposeOrder is the vocabulary IN PRESENTATION ORDER — the same order the admin panel lists
// назначения in (bom-purpose.ts bomPurposeOrder). Anything that shows purpose-keyed groups to a
// human reads this instead of inventing its own sort, so the печать and the screens it is printed
// from agree on which группа comes first. Kept in lockstep with the set below by
// TestBomPurposeOrderCoversVocabulary.
var BomPurposeOrder = []TechCardBomPurpose{
	BomPurposeMain,
	BomPurposeLining,
	BomPurposePocketing,
	BomPurposeInterfacing,
	BomPurposeInsulation,
	BomPurposeContrast,
	BomPurposeMesh,
	BomPurposeOther,
}

// ValidTechCardBomPurposes is the set of accepted BOM purposes. Kept in lockstep with the DB CHECK
// by TestBomPurposeDBCheckNoDrift and with the proto enum by TestBomPurposeEnumNoDrift.
var ValidTechCardBomPurposes = map[TechCardBomPurpose]bool{
	BomPurposeMain:        true,
	BomPurposeLining:      true,
	BomPurposePocketing:   true,
	BomPurposeInterfacing: true,
	BomPurposeInsulation:  true,
	BomPurposeContrast:    true,
	BomPurposeMesh:        true,
	BomPurposeOther:       true,
}

// IsValidTechCardBomPurpose reports whether p is an accepted BOM purpose.
func IsValidTechCardBomPurpose(p TechCardBomPurpose) bool {
	return ValidTechCardBomPurposes[p]
}

// TechCardBomKind is ЧТО ЭТО ЗА ПОЗИЦИЯ — the mirror image of TechCardBomPurpose. Purpose says what
// the garment uses a ROLL-GOODS line FOR; kind says what a NON-roll-goods line IS. They are two
// halves of one classification and never appear on the same row: purpose is legal only on
// fabric/lining/interlining/insulation, kind only on the sections that are neither roll goods nor
// label.
//
// NOT NAMED "role" — TechCardRole already means WHO is responsible for the card, and a second
// "role" meaning "what this button is" would be misread permanently.
//
// ONE FLAT VOCABULARY, not one per family: a value already implies its family (a zipper is hardware
// and nothing else), so a per-family type would encode the section twice and let the copies drift.
// The pairing lives in bomKindHomeSection below — as DATA, so that ValidTechCardBomKinds and the
// section a kind belongs to can never be two lists that disagree.
//
// Mirrors the common.TechCardBomKind proto enum and the chk_bom_item_kind DB CHECK (0278); stored as
// a NULLABLE string in tech_card_bom_item.kind, where NULL means "not classified yet".
type TechCardBomKind string

const (
	// ФУРНИТУРА (home section: hardware) — countable fittings the garment stops working without.
	BomKindZipper        TechCardBomKind = "zipper"         // молния в сборе
	BomKindZipperSlider  TechCardBomKind = "zipper_slider"  // бегунок, если закупается отдельно
	BomKindButton        TechCardBomKind = "button"         // пуговица
	BomKindSnap          TechCardBomKind = "snap"           // кнопка
	BomKindRivet         TechCardBomKind = "rivet"          // НЕСУЩАЯ заклёпка; декоративная — BomKindStud
	BomKindEyelet        TechCardBomKind = "eyelet"         // люверс / блочка
	BomKindHookAndBar    TechCardBomKind = "hook_and_bar"   // крючок-петля
	BomKindSnapHook      TechCardBomKind = "snap_hook"      // карабин
	BomKindBuckle        TechCardBomKind = "buckle"         // пряжка
	BomKindStrapAdjuster TechCardBomKind = "strap_adjuster" // регулятор лямки
	BomKindRing          TechCardBomKind = "ring"           // кольцо / полукольцо
	BomKindToggle        TechCardBomKind = "toggle"         // фиксатор-«клык»
	BomKindCordStopper   TechCardBomKind = "cord_stopper"   // фиксатор шнура; сам шнур — BomKindDrawcord
	BomKindCordEnd       TechCardBomKind = "cord_end"       // наконечник шнура
	BomKindMagnet        TechCardBomKind = "magnet"         // магнитная застёжка
	BomKindChain         TechCardBomKind = "chain"          // цепь

	// ОТДЕЛОЧНЫЕ (home section: trim) — soft goods sold by length.
	BomKindElastic  TechCardBomKind = "elastic"   // резинка
	BomKindDrawcord TechCardBomKind = "drawcord"  // шнур; его фиксатор — BomKindCordStopper
	BomKindBinding  TechCardBomKind = "binding"   // бейка
	BomKindTape     TechCardBomKind = "tape"      // тесьма
	BomKindPiping   TechCardBomKind = "piping"    // кант
	BomKindWebbing  TechCardBomKind = "webbing"   // стропа
	BomKindHookLoop TechCardBomKind = "hook_loop" // велкро — TRIM, метражная лента, не фурнитура
	BomKindBoning   TechCardBomKind = "boning"    // регилин
	BomKindLace     TechCardBomKind = "lace"      // кружево
	BomKindRibbing  TechCardBomKind = "ribbing"   // трикотажная резинка

	// ДЕКОР (home section: decoration) — applied ONTO the garment; holds nothing together.
	BomKindPrint        TechCardBomKind = "print"
	BomKindEmbroidery   TechCardBomKind = "embroidery"
	BomKindApplique     TechCardBomKind = "applique"
	BomKindPatch        TechCardBomKind = "patch"
	BomKindHeatTransfer TechCardBomKind = "heat_transfer"
	BomKindRhinestone   TechCardBomKind = "rhinestone"
	BomKindSequin       TechCardBomKind = "sequin"
	BomKindStud         TechCardBomKind = "stud" // декоративная клёпка; несущая — BomKindRivet
	BomKindFoil         TechCardBomKind = "foil"
	BomKindLaser        TechCardBomKind = "laser"

	// НИТКИ (home section: thread) — split by the JOB, which is what picks the machine.
	BomKindSewingThread     TechCardBomKind = "sewing_thread"
	BomKindTopstitchThread  TechCardBomKind = "topstitch_thread"
	BomKindOverlockThread   TechCardBomKind = "overlock_thread"
	BomKindButtonholeThread TechCardBomKind = "buttonhole_thread"
	BomKindEmbroideryThread TechCardBomKind = "embroidery_thread"
	BomKindElasticThread    TechCardBomKind = "elastic_thread"

	// УПАКОВКА (home section: packaging). Spelt like TechCardAuxSubtype wherever the two name the
	// SAME object, so the aux card that MAKES it and the BOM line that CONSUMES it read as one word.
	BomKindPolybag       TechCardBomKind = "polybag"
	BomKindCarton        TechCardBomKind = "carton"         // транспортный короб (не AuxSubtypeBox)
	BomKindHanger        TechCardBomKind = "hanger"         // == AuxSubtypeHanger
	BomKindHangtagString TechCardBomKind = "hangtag_string" // шнурок ярлыка, не сам AuxSubtypeHangtag
	BomKindSticker       TechCardBomKind = "sticker"        // == AuxSubtypeSticker
	BomKindTissue        TechCardBomKind = "tissue"
	BomKindDustBag       TechCardBomKind = "dust_bag"     // == AuxSubtypeDustBag
	BomKindGarmentCase   TechCardBomKind = "garment_case" // == AuxSubtypeGarmentCase
	BomKindInsertCard    TechCardBomKind = "insert_card"  // печатная карточка, уже AuxSubtypeInsert

	// ДРУГОЕ — meaning lives in KindNote, never in a shadow value on one of the 51 real kinds.
	BomKindOther TechCardBomKind = "other"
)

// BomKindAnySection is the sentinel bomKindHomeSection uses for a kind with no single home. It is
// NOT a section: the empty string is absent from ValidTechCardBomSections, so it can never be
// mistaken for one. Only BomKindOther maps to it, and deliberately — «другое» is the escape hatch of
// every eligible family at once, so pinning it to one family would make the other families' escape
// hatch unreachable and push their strays into a wrong real kind.
const BomKindAnySection TechCardBomSection = ""

// bomKindHomeSection is THE table of the kind↔section pairing, and the single source of the
// vocabulary itself: ValidTechCardBomKinds is derived from its keys just below, so a kind cannot be
// added to one and forgotten in the other. A kind is legal ONLY in its home section (plus
// BomKindAnySection's wildcard); the check runs in the store, next to the покупной purpose check —
// see upsertTechCardBom. Which sections are eligible AT ALL is a third, derived thing that lives
// beside rollGoodsSectionList in internal/store/techcard, because it is that list's complement and
// hand-copying it is the exact drift that comment forbids.
var bomKindHomeSection = map[TechCardBomKind]TechCardBomSection{
	BomKindZipper:        BomSectionHardware,
	BomKindZipperSlider:  BomSectionHardware,
	BomKindButton:        BomSectionHardware,
	BomKindSnap:          BomSectionHardware,
	BomKindRivet:         BomSectionHardware,
	BomKindEyelet:        BomSectionHardware,
	BomKindHookAndBar:    BomSectionHardware,
	BomKindSnapHook:      BomSectionHardware,
	BomKindBuckle:        BomSectionHardware,
	BomKindStrapAdjuster: BomSectionHardware,
	BomKindRing:          BomSectionHardware,
	BomKindToggle:        BomSectionHardware,
	BomKindCordStopper:   BomSectionHardware,
	BomKindCordEnd:       BomSectionHardware,
	BomKindMagnet:        BomSectionHardware,
	BomKindChain:         BomSectionHardware,

	BomKindElastic:  BomSectionTrim,
	BomKindDrawcord: BomSectionTrim,
	BomKindBinding:  BomSectionTrim,
	BomKindTape:     BomSectionTrim,
	BomKindPiping:   BomSectionTrim,
	BomKindWebbing:  BomSectionTrim,
	BomKindHookLoop: BomSectionTrim,
	BomKindBoning:   BomSectionTrim,
	BomKindLace:     BomSectionTrim,
	BomKindRibbing:  BomSectionTrim,

	BomKindPrint:        BomSectionDecoration,
	BomKindEmbroidery:   BomSectionDecoration,
	BomKindApplique:     BomSectionDecoration,
	BomKindPatch:        BomSectionDecoration,
	BomKindHeatTransfer: BomSectionDecoration,
	BomKindRhinestone:   BomSectionDecoration,
	BomKindSequin:       BomSectionDecoration,
	BomKindStud:         BomSectionDecoration,
	BomKindFoil:         BomSectionDecoration,
	BomKindLaser:        BomSectionDecoration,

	BomKindSewingThread:     BomSectionThread,
	BomKindTopstitchThread:  BomSectionThread,
	BomKindOverlockThread:   BomSectionThread,
	BomKindButtonholeThread: BomSectionThread,
	BomKindEmbroideryThread: BomSectionThread,
	BomKindElasticThread:    BomSectionThread,

	BomKindPolybag:       BomSectionPackaging,
	BomKindCarton:        BomSectionPackaging,
	BomKindHanger:        BomSectionPackaging,
	BomKindHangtagString: BomSectionPackaging,
	BomKindSticker:       BomSectionPackaging,
	BomKindTissue:        BomSectionPackaging,
	BomKindDustBag:       BomSectionPackaging,
	BomKindGarmentCase:   BomSectionPackaging,
	BomKindInsertCard:    BomSectionPackaging,

	BomKindOther: BomKindAnySection,
}

// ValidTechCardBomKinds is the set of accepted BOM kinds, DERIVED from bomKindHomeSection so the
// vocabulary and the pairing table are physically the same list. Kept in lockstep with the DB CHECK
// by TestBomKindDBCheckNoDrift and with the proto enum by TestBomKindEnumNoDrift.
var ValidTechCardBomKinds = func() map[TechCardBomKind]bool {
	m := make(map[TechCardBomKind]bool, len(bomKindHomeSection))
	for k := range bomKindHomeSection {
		m[k] = true
	}
	return m
}()

// IsValidTechCardBomKind reports whether k is an accepted BOM kind.
func IsValidTechCardBomKind(k TechCardBomKind) bool {
	return ValidTechCardBomKinds[k]
}

// BomKindHomeSection returns the one section a kind is native to. ok is false for an unknown kind;
// a known kind whose home is BomKindAnySection is legal in EVERY kind-eligible section. Eligibility
// itself is not answered here on purpose — it is the complement of the roll-goods list and is
// derived where that list lives, so restating it here would create the second copy.
func BomKindHomeSection(k TechCardBomKind) (TechCardBomSection, bool) {
	s, ok := bomKindHomeSection[k]
	return s, ok
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

// Consumption provenance values (tech_card_colorway_usage.consumption_source, 0261; 'dxf' 0294).
const (
	ConsumptionSourceManual = "manual"
	ConsumptionSourceMarker = "marker"
	// ConsumptionSourceDxf is a norm computed from the pattern sheets: Σ(площадь деталей) ÷
	// раскройная ширина. It is NETTO — it contains no inter-piece waste, no selvedge and no
	// end-of-lay loss, because a pattern says nothing about how the pieces were laid. That is
	// exactly why it grosses up like a manual norm (wastageApplies below): the honest total is
	// netto × the slot's declared cutting percentage.
	//
	// It is NOT a weaker 'marker'. A marker MEASURED a layout; this one measures the garment. The
	// two answer different questions, and the difference is auditable: dxf carries no waste
	// decomposition and no norm stamp (a раскладка id would be a claim about a layout that never
	// happened) — normalized() in the store clears both for every non-marker source.
	ConsumptionSourceDxf = "dxf"
)

// ValidConsumptionSources is the set of accepted consumption provenance values.
var ValidConsumptionSources = map[string]bool{
	ConsumptionSourceManual: true,
	ConsumptionSourceMarker: true,
	ConsumptionSourceDxf:    true,
}

// wastageApplies reports whether the article's wastage_percent may gross this usage's cost
// up. A marker-sourced norm came from a measured раскладка whose length already CONTAINS
// the cutting waste (and the selvedge rides the per-running-metre price), so grossing it
// again would double-count — the exact trap PIECES-WASTAGE-DESIGN §2.3 retires.
//
// EVERY OTHER SOURCE GROSSES, and 'dxf' (0294) belongs on that side deliberately, not by
// omission: a pattern area is netto, so the percentage is the only thing that pays for the
// cloth between the pieces. The failure mode to keep in mind is the slot whose
// wastage_percent is NULL — applyWastage then multiplies by nothing and a netto norm reaches
// purchasing as if it were a total. The readiness gate blocks a run on exactly that pair
// (dxf + no declared percentage) rather than letting the arithmetic be silently short.
func (u *TechCardColorwayUsage) wastageApplies() bool {
	return u.ConsumptionSource.String != ConsumptionSourceMarker
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
	// PieceLineKey is the wire reference to the cut-piece this usage ASSIGNS its material to («деталь
	// X кроится из артикула Y» — NOT a norm binding, see IsPieceMaterialAssignment): the stable
	// line_key of the style's tech_card_piece (WS4). The store resolves it to PieceId, the real
	// FK (usage.piece_id RESTRICT). It replaces the positional PieceIndex, kept for the transition.
	PieceLineKey string              `db:"-"`
	BomItemIndex sql.NullInt32       `db:"bom_item_index"` // 0-based index into the submitted bom_items; NULL = unset
	Placement    sql.NullString      `db:"placement"`
	Color        sql.NullString      `db:"color"`
	Pantone      sql.NullString      `db:"pantone"`
	Consumption  decimal.NullDecimal `db:"consumption"` // per-garment rate (measured materials)
	Quantity     decimal.NullDecimal `db:"quantity"`    // count (countable trims)
	// PieceIndex is an optional 0-based arrow into TechCardInsert.Pieces saying which cut-piece
	// this row assigns its material to; NULL = a garment-level row — the carrier of the slot's
	// consumption norm (see IsPieceMaterialAssignment). Legacy positional form of the binding.
	PieceIndex sql.NullInt32 `db:"piece_index"`
	// SizeConsumptions is the per-size material rate (in-memory; persisted to
	// tech_card_colorway_usage_consumption). When non-empty it grades usage per size.
	SizeConsumptions []TechCardBomSizeConsumption `db:"-"`
	// MaterialId pins the CONCRETE catalog article this colourway takes for the slot (the BOM
	// line is the role; the pin is the article). NULL = inherit the slot default
	// (bom_item.material_id), so a later default change keeps propagating to colourways that
	// never diverged. FK material(id) ON DELETE RESTRICT (0221).
	MaterialId sql.NullInt64 `db:"material_id"`
	// ConsumptionSource is the norm's provenance: 'manual' (default; wastage_percent grosses cost
	// up as always), 'marker' (the norm came from a saved раскладка whose length already CONTAINS
	// the cutting waste — costing must NOT gross it up again), or 'dxf' (0294; netto pattern area
	// ÷ раскройная ширина — grosses up like manual, see ConsumptionSourceDxf). On WRITE the null
	// state is proto presence, mirroring MaterialIdSet: Valid=false means the field was absent from
	// a stale client's payload and the store preserves the stored provenance triple.
	ConsumptionSource sql.NullString `db:"consumption_source"`
	// WasteSelvedgePct / WasteCutPct decompose a marker-sourced norm's waste (кромка / рез) for
	// DISPLAY — never multiplied into any cost. NULL on manual rows.
	WasteSelvedgePct decimal.NullDecimal `db:"waste_selvedge_pct"`
	WasteCutPct      decimal.NullDecimal `db:"waste_cut_pct"`
	// NormMarkerId is the Ф6.8 stamp (0291): WHICH saved раскладка this consumption was applied
	// from. NULL = not applied from a marker (typed in, estimated) or applied before Ф6. There is
	// deliberately NO FK — раскладки get deleted, and a dangling id honestly reads as «раскладка
	// удалена», which is what the audit is for. On WRITE the presence protocol is NormMarkerIdSet.
	NormMarkerId sql.NullInt64 `db:"norm_marker_id"`
	// NormAppliedAt is the SERVER stamp of when the norm was applied (0291). OUTPUT-ONLY: the wire
	// value is ignored on write, and the store moves it ONLY when the (source, marker) pair
	// changes — stamping it on every recipe write would let an edit of a NEIGHBOURING field refresh
	// it and thereby extinguish the «раскладка изменена» indicator, i.e. the breakage would look
	// exactly like its own absence. NULL whenever NormMarkerId is NULL.
	NormAppliedAt sql.NullTime `db:"norm_applied_at"`
	// MaterialIdSet mirrors the wire field's presence (proto3 `optional`): false = the client
	// did not send material_id at all — an old client's full-replace recipe write must PRESERVE
	// the existing pin; true = MaterialId is authoritative (invalid/0 explicitly clears the pin).
	// Not persisted.
	MaterialIdSet bool `db:"-"`
	// NormMarkerIdSet mirrors norm_marker_id's wire presence, for the same reason and with the same
	// price as MaterialIdSet: the recipe is written by FULL REPLACE, so a column the client did not
	// echo back is erased silently. false = absent → the store PRESERVES the stored stamp,
	// REGARDLESS of whether consumption_source was sent (a client that knows about 'marker' but not
	// about the stamp is precisely today's deployed client, and it must not blank the audit by
	// saving a recipe); true = NormMarkerId is authoritative, an explicit 0 clearing the stamp.
	// Not persisted.
	NormMarkerIdSet bool `db:"-"`
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

// IsPieceMaterialAssignment reports whether this recipe row is bound to a concrete cut-piece,
// through ANY of the three representations of that binding: the resolved PieceId FK (store
// reads), the wire/snapshot PieceLineKey, or the legacy positional PieceIndex.
//
// Such a row is the ASSIGNMENT of a material to a piece («деталь X кроится из артикула Y») and
// NOT a carrier of a consumption norm. Расход ткани — свойство ИЗДЕЛИЯ (решение владельца,
// T1/T8): the garment-level row of the same slot carries the norm, so a piece-bound row must
// contribute NOTHING to costing, the run's material requirement, planned run cost or the
// readiness verdict — and, symmetrically, a piece-bound row with no number is NOT a «missing
// norm» and must never block a run whose garment-level row has one. A colourway whose usages
// are ALL piece-bound has, for every computation, an EMPTY recipe.
//
// The consumers that filter through THIS predicate — one rule, not N copies of `piece_id IS
// NULL` that drift apart — are the NORM rollups: colorwayCost / ComputeColorwayUnitCost, the
// style cost estimate, the run material plan, run readiness (normChecks, unitCoverage), the
// frozen release costs (via its pb mirror in dto), and the norm-money methods below
// (LineTotal / SizeRunTotal / BaseSizeTotal). Readers that deliberately DO look at piece-bound
// rows, because the ASSIGNMENT half of the row is exactly their business:
//   - the cutting plan (production_cut_plan.go) — what is cut from what;
//   - the lay-article resolver (dto.ResolveLayArticle) — a piece row's PIN can name the slot's
//     article when no garment-level row pins one;
//   - identity prefetches (LinkedMaterials id harvesting) — a piece pin's article needs a name;
//   - the lot-validation / calibration SQL candidate sets (internal/store/productionrun/lays.go),
//     which admit pins from ALL rows by construction.
func (u *TechCardColorwayUsage) IsPieceMaterialAssignment() bool {
	return (u.PieceId.Valid && u.PieceId.Int64 > 0) ||
		u.PieceLineKey != "" ||
		u.PieceIndex.Valid
}

// LineTotal is the usage's per-garment material cost, resolved against its catalog
// article (bom). It is INVALID (the cost moves to SizeRunTotal) when the usage has
// per-size consumption. A countable trim (Quantity, no Consumption) is Quantity ×
// unit_price with no wastage; a measured material is Consumption × unit_price grossed
// up by the article's wastage_percent.
//
// A PIECE-BOUND ROW HAS NO NORM-MONEY. IsPieceMaterialAssignment rows carry no norm (T8), so
// the rollups skip them entirely — and the money methods must agree: a legacy number typed on
// such a row otherwise ships a line_total to the wire that the cost no longer contains, i.e.
// two contradicting figures on one card and an invitation to re-sum them. Guarded HERE, in the
// methods, and not in ConvertRecipeUsagesToPb: these three methods (LineTotal / SizeRunTotal /
// BaseSizeTotal) are the single definition of «this row's norm-money» — EffectiveTotal and
// UnitTotal compose them — while the converter is just one reader, and a guard there would
// leave every other (and every future) caller to rediscover the rule.
func (u *TechCardColorwayUsage) LineTotal(bom *TechCardBomItem) decimal.NullDecimal {
	if u.IsPieceMaterialAssignment() {
		return decimal.NullDecimal{}
	}
	if len(u.SizeConsumptions) > 0 || bom == nil || !bom.UnitPrice.Valid {
		return decimal.NullDecimal{}
	}
	if u.Quantity.Valid {
		return decimal.NullDecimal{Decimal: u.Quantity.Decimal.Mul(bom.UnitPrice.Decimal), Valid: true}
	}
	if !u.Consumption.Valid {
		return decimal.NullDecimal{}
	}
	if !u.wastageApplies() {
		return decimal.NullDecimal{Decimal: u.Consumption.Decimal.Mul(bom.UnitPrice.Decimal), Valid: true}
	}
	return decimal.NullDecimal{Decimal: applyWastage(u.Consumption.Decimal.Mul(bom.UnitPrice.Decimal), bom.WastagePercent), Valid: true}
}

// SizeRunTotal is the usage's whole-run material cost when it has per-size consumption:
// Σ(consumption_size × order_qty_size) × unit_price, grossed up by the article's
// wastage_percent. orderQtyBySize maps size_id → order quantity (a size with no order
// quantity contributes nothing). INVALID when there is no per-size consumption, no
// unit_price, or no order quantities yet (the cost is then 0, per the costing rule).
func (u *TechCardColorwayUsage) SizeRunTotal(bom *TechCardBomItem, orderQtyBySize map[int]int) decimal.NullDecimal {
	// Piece-bound row → no norm-money; see LineTotal for the argument and for why the guard
	// lives in the methods rather than the wire converter.
	if u.IsPieceMaterialAssignment() {
		return decimal.NullDecimal{}
	}
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
	if !u.wastageApplies() {
		return decimal.NullDecimal{Decimal: totalQty.Mul(bom.UnitPrice.Decimal), Valid: true}
	}
	return decimal.NullDecimal{Decimal: applyWastage(totalQty.Mul(bom.UnitPrice.Decimal), bom.WastagePercent), Valid: true}
}

// EffectiveTotal is the usage's contribution to a WHOLE-RUN rollup: its SizeRunTotal when it
// has per-size consumption (order-scale), otherwise its per-garment LineTotal. Mirrors the
// «per-size if present, else per-garment» rule applied per usage.
//
// NOT the costing basis — that is UnitTotal, and since the base-size change the two no longer
// share a denominator. This stays a display/whole-run helper for a caller that genuinely has a
// quantity per size in hand (a real production run's lines), never for standard cost.
func (u *TechCardColorwayUsage) EffectiveTotal(bom *TechCardBomItem, orderQtyBySize map[int]int) decimal.NullDecimal {
	if rt := u.SizeRunTotal(bom, orderQtyBySize); rt.Valid {
		return rt
	}
	return u.LineTotal(bom)
}

// BaseSizeTotal is a size-graded usage's PER-GARMENT material cost on the style's BASE SAMPLE
// SIZE: the norm recorded for baseSizeID × unit_price, grossed up by the article's
// wastage_percent — the same arithmetic LineTotal does for an ungraded norm, with the base
// size's number standing in as the norm.
//
// INVALID (and deliberately so) when the usage is not size-graded, when the card names NO base
// sample size (baseSizeID <= 0), when this usage carries no norm for that size, or when the
// article has no price. Every one of those is a question nobody has answered, and the caller
// must carry it out as «непосчитано» — see UnitTotal.
func (u *TechCardColorwayUsage) BaseSizeTotal(bom *TechCardBomItem, baseSizeID int) decimal.NullDecimal {
	// Piece-bound row → no norm-money; see LineTotal for the argument.
	if u.IsPieceMaterialAssignment() {
		return decimal.NullDecimal{}
	}
	if len(u.SizeConsumptions) == 0 || baseSizeID <= 0 || bom == nil || !bom.UnitPrice.Valid {
		return decimal.NullDecimal{}
	}
	for _, sc := range u.SizeConsumptions {
		if sc.SizeId != baseSizeID {
			continue
		}
		total := sc.Consumption.Mul(bom.UnitPrice.Decimal)
		if !u.wastageApplies() {
			return decimal.NullDecimal{Decimal: total, Valid: true}
		}
		return decimal.NullDecimal{Decimal: applyWastage(total, bom.WastagePercent), Valid: true}
	}
	return decimal.NullDecimal{}
}

// UnitTotal is the usage's PER-GARMENT material cost for costing — the standard cost of the
// style. A per-garment usage (measured Consumption or countable Quantity) uses its LineTotal
// directly. A usage graded ONLY per size is costed on the BASE SAMPLE SIZE (BaseSizeTotal).
// INVALID when neither is available, and an invalid result is the caller's signal to treat the
// whole recipe as uncosted (dto's hasUnpriced), never to substitute a number.
//
// THE BASIS AND WHY IT CHANGED. This used to be SizeRunTotal ÷ Σ size_quantities — the card's
// «типовой тираж для калькуляции» averaged the graded norms into one figure. That denominator
// was a fiction: tech_card.size_quantities is an illustrative mix, real quantities live on a
// production_run, and it decided what went into product.cost_price and from there into every
// margin. It was also arithmetically unsound: the denominator summed EVERY size carrying a
// positive quantity while the numerator summed only the sizes for which THIS usage had a norm,
// so a partially-graded usage was divided by a run it never covered and came out systematically
// CHEAP. A style's standard cost is now the base size's own norm — one size somebody actually
// drafted and approved — and it moves only when that norm moves.
//
// WHAT MUST NOT COME BACK: a fallback to the median/average/first size when the base size is
// unset or ungraded. That silently re-labels a number nobody approved as the approved cost. The
// honest answer is no number, and the flag on the wire.
func (u *TechCardColorwayUsage) UnitTotal(bom *TechCardBomItem, baseSizeID int) decimal.NullDecimal {
	if lt := u.LineTotal(bom); lt.Valid {
		return lt
	}
	return u.BaseSizeTotal(bom, baseSizeID)
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
	MaterialId sql.NullInt64      `db:"material_id"`
	Section    TechCardBomSection `db:"section"`
	// Purpose (0265) is НАЗНАЧЕНИЕ on its own axis beside Section — see TechCardBomPurpose. Accepted
	// only on a roll-goods line (fabric/lining/interlining/insulation); INVALID (NULL) means "not
	// sorted yet" and is what every line predating 0265 carries, deliberately never guessed.
	Purpose sql.NullString `db:"purpose"`
	// PurposeOmitted / IsSampleOmitted — поле ОТСУТСТВОВАЛО на проводе, а не «пришло пустым».
	// Карточка сохраняется целиком, админка это SPA, и вкладка со старым бандлом этих полей не шлёт
	// вовсе; без различения её сейв стёр бы назначение у ВСЕХ строк карточки — бесследно, потому
	// что полей нет в дайджесте подписи, а NULL неотличим от «ещё не разложили».
	//
	// Признак НЕГАТИВНЫЙ намеренно: нулевое значение означает «писать как обычно», поэтому любой
	// внутренний конструктор (тесты, сидер, миграционные утилиты) продолжает работать как писал.
	// Позитивный «Set» с дефолтом false молча превратил бы их всех в ничего-не-пишущих.
	PurposeOmitted bool `db:"-"`
	// PurposeNote explains a BomPurposeOther line. Legal only alongside that purpose — the DB CHECK
	// chk_bom_item_purpose_note enforces it — so the note can never quietly become a ninth purpose.
	PurposeNote sql.NullString `db:"purpose_note"`
	// Kind (0278) is ЧТО ЭТО ЗА ПОЗИЦИЯ — the mirror of Purpose on the other half of the BOM, see
	// TechCardBomKind. Accepted only on a line that is NEITHER roll goods NOR a label, and only in
	// the kind's own home section; INVALID (NULL) means "not classified yet" and is what every line
	// predating 0278 carries, deliberately never guessed.
	Kind sql.NullString `db:"kind"`
	// KindOmitted / KindNoteOmitted — поле ОТСУТСТВОВАЛО на проводе, а не «пришло пустым». Тот же
	// НЕГАТИВНЫЙ смысл и та же причина, что у PurposeOmitted: карточка сохраняется целиком, админка
	// это SPA, и вкладка со старым бандлом этих полей не шлёт вовсе — без различения её сейв стёр бы
	// классификацию у ВСЕХ строк карточки, и бесследно, потому что поля не входят в дайджест подписи,
	// а NULL неотличим от «ещё не классифицировали». Нулевое значение = «пиши как обычно», поэтому
	// любой внутренний конструктор (тесты, сидер, миграционные утилиты) работает как писал.
	//
	// Оба флага ставятся ВМЕСТЕ (см. parseTechCardBomItems): колонки связаны в БД
	// (chk_bom_item_kind_note), поэтому запись одной половины при сохранённой второй — это строка,
	// которую MySQL обязан отвергнуть сырым 3819. Пара живёт и меняется как одно целое.
	KindOmitted bool `db:"-"`
	// KindNote explains a BomKindOther line. Legal only alongside that kind — the DB CHECK
	// chk_bom_item_kind_note enforces it — so the note can never quietly become a 52nd kind.
	KindNote        sql.NullString `db:"kind_note"`
	KindNoteOmitted bool           `db:"-"`
	// IsSample marks the yardage the SAMPLE is sewn from. A flag rather than a purpose value because
	// a sample is a sample MAIN plus a sample LINING; folded into Purpose the two would collapse.
	IsSample        bool                `db:"is_sample"`
	IsSampleOmitted bool                `db:"-"`
	Name            string              `db:"name"`
	Supplier        sql.NullString      `db:"supplier"`
	SupplierRef     sql.NullString      `db:"supplier_ref"`
	Color           sql.NullString      `db:"color"` // base/reference colour (per-colourway colour is on the usage)
	Composition     sql.NullString      `db:"composition"`
	Spec            sql.NullString      `db:"spec"`
	Unit            sql.NullString      `db:"unit"`
	UnitPrice       decimal.NullDecimal `db:"unit_price"`
	Currency        sql.NullString      `db:"currency"`
	Comment         sql.NullString      `db:"comment"`
	// fabric data for the cutter / marker (Phase 3.5c)
	FabricWidth     decimal.NullDecimal `db:"fabric_width"`
	FabricWeightGsm decimal.NullDecimal `db:"fabric_weight_gsm"`
	FabricDirection sql.NullString      `db:"fabric_direction"`
	// FabricDirectionOmitted — поле ОТСУТСТВОВАЛО на проводе, а не «пришло пустым», same negative
	// sense and same reason as PurposeOmitted above: a tab holding an older bundle does not send it,
	// and a proto3 enum's zero value is UNKNOWN, so without the distinction that tab's save would
	// clear направление on every line of the card. Since Ф1 that erasure is not cosmetic — it
	// un-saves every раскладка on the card until somebody fills the column back in.
	FabricDirectionOmitted bool                `db:"-"`
	WastagePercent         decimal.NullDecimal `db:"wastage_percent"`
	// Stored price provenance (production-costing Phase 3): where unit_price came from and when it
	// was stamped. Server-owned — set by the save path ('manual' when the price changes hands
	// through UpdateTechCard) and by the reprice action ('catalog'); NULL on pre-provenance rows.
	// Deliberately NOT part of the signed MATERIALS digest projection: metadata about a value must
	// not stale a sign-off whose value did not change.
	PriceSource     sql.NullString `db:"price_source"`
	PriceSnapshotAt sql.NullTime   `db:"price_snapshot_at"`
	// READ-ONLY enrichment (0259, Ф9.1): the width the cutter actually has for this line and the
	// linked article's selvedge. effective = COALESCE(line's own fabric_width, article width) —
	// the line's snapshot wins, the catalog is the fallback the audit found missing; the client
	// derives usable width as effective − 2×selvedge. Populated only by the single-card read's
	// enrichment SELECT; zero on writes and every other query, never persisted.
	EffectiveFabricWidthCm decimal.NullDecimal `db:"effective_fabric_width_cm"`
	SelvedgeCm             decimal.NullDecimal `db:"selvedge_cm"`
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
// the url's extension. A graded DXF carries the whole range at once, so a sheet is filed under one
// size of the card or under none at all (SizeId 0); see the field.
type TechCardSizePattern struct {
	// SizeId is the size this sheet is FILED UNDER, and 0 (stored NULL since 0281) means it is filed
	// under none — the sheet is graded and its sizes live in the file's block names, which only the
	// browser reads. It is a storage slot, not a statement about the file: the server's answer to
	// «есть ли выкройка на этот размер» comes from tech_card_pattern_size_index (0280), and no
	// consumer of a sheet (viewer, раскладка, piece matching, marker export) reads this field.
	//
	// A non-zero value must still be a size of the card's range; 0 is accepted whatever the range is,
	// which is what lets a DXF land on a card whose size range nobody has filled in yet.
	SizeId int `db:"size_id"`
	// LineKey is the row's stable identity across saves and file replacement (the url changes when a
	// sheet is replaced; the line_key does not). Empty on WRITE = legacy/stale client — the store then
	// matches by (size_id, url) and mints a key server-side.
	LineKey string `db:"line_key"`
	// BomLineKey binds the sheet to the fabric BOM line it is cut from. Write semantics mirror Name:
	// Valid=false means absent from the payload (carry the stored binding forward), Valid=true writes
	// as given with empty unbinding (stored as NULL).
	//
	// LEGACY HALF of the binding since 0267 — read it through entity.ResolveFabricScope, never on its
	// own. It stays because it cannot be migrated: a sheet bound to line L has no purpose to move to
	// until somebody sorts L, and 0265 deliberately guessed for nobody.
	BomLineKey sql.NullString `db:"bom_line_key"`
	// FabricPurpose binds the sheet to a НАЗНАЧЕНИЕ (0265) instead of to one line — «это лекало
	// основной ткани», which is the honest statement at card level, where no article is in play.
	// Presence semantics are BomLineKey's exactly: Valid=false means the field was ABSENT from the
	// payload and the store carries the stored value forward (a stale client cannot wipe a binding it
	// never saw); Valid=true writes as given, with "" clearing it back to NULL.
	FabricPurpose sql.NullString `db:"fabric_purpose"`
	URL           string         `db:"url"`
	Filename      sql.NullString `db:"filename"`
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
	// AuxSubtypeToteBag is a шоппер — the carrier the customer takes the purchase away in and keeps
	// using. Distinct from a dust bag and a кофр: it is cut, sewn and costed as its own item, and an
	// assembly bill has to name which carrier a style ships with (migration 0255).
	AuxSubtypeToteBag TechCardAuxSubtype = "tote_bag"
	AuxSubtypeBox     TechCardAuxSubtype = "box"
	AuxSubtypeInsert  TechCardAuxSubtype = "insert"
	AuxSubtypeHanger  TechCardAuxSubtype = "hanger"
	AuxSubtypeOther   TechCardAuxSubtype = "other"
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
	AuxSubtypeToteBag:     true,
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
// The card's DEFAULTS — what an operation inherits when it does not override.
//
// It used to be a block of free-text notes that nothing inherited, sharing its suggestion lists with
// the operation editor while the operator retyped the same «5 мм» on every step. The typed fields
// below are the half an operation can actually inherit; hem finish and pressing stay prose because
// they genuinely are.
//
// THE SEAM ALLOWANCE IS NOT HERE. The card's standard is TechCard.RequiredSeamAllowanceMm, which
// 0277 deliberately put on the card rather than in this section: a field in a section's digest
// projection marks every signed-off approval of that section as stale the moment it is added. A
// second card-level allowance would also be a second answer to a settled question.
type TechCardConstruction struct {
	HemFinish sql.NullString `db:"hem_finish"`
	Pressing  sql.NullString `db:"pressing"`
	Notes     sql.NullString `db:"notes"`
	// Inherited by TechCardOperation.SeamClass when that one is unset.
	DefaultSeamClass sql.NullString `db:"default_seam_class"`
	// Inherited by TechCardOperation.StitchesPerCm. STITCHES PER CENTIMETRE — density is quoted per
	// cm in every language and is not part of the millimetre switch.
	DefaultStitchesPerCm decimal.NullDecimal `db:"default_stitches_per_cm"`
	// 3 | 4 | 5; NULL = unset. The name does not reuse the removed `overlock_threads` (a string).
	OverlockThreadCount sql.NullInt32 `db:"overlock_thread_count"`
}

// The stitch-density band, in STITCHES PER CENTIMETRE. Below 1 a seam falls apart, above 20 the
// needle perforates the cloth into a tear line; 3-5 is ordinary garment sewing.
//
// THESE TWO NUMBERS ALSO STAND IN THE SCHEMA (chk_construction_stitches, 0289), and the Go check
// exists because the CHECK alone answers with error 3819 — no field, no words, surfaced as a bare
// Internal. A closed range the operator can trip by typing «0» has to refuse in a sentence next to
// the control; the constraint stays as the net underneath. Move one and move the other.
const (
	MinStitchesPerCm = 1
	MaxStitchesPerCm = 20
)

// ValidateStitchesPerCm checks a stitch density — the card default or a step's override. Unset is
// accepted (that is «inherit» on a step and «not configured» on the card); only a value being SET has
// to be sane. ZERO IS NOT a legal setting here, unlike the seam allowance: an allowance of zero means
// «the выкройки carry the cut line», whereas a density of zero means a seam with no stitches in it.
func ValidateStitchesPerCm(field string, v decimal.NullDecimal) error {
	if !v.Valid {
		return nil
	}
	if v.Decimal.Exponent() < -2 {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 2 decimal places — the column stores hundredths and the rest would be dropped silently")
	}
	if v.Decimal.LessThan(decimal.NewFromInt(MinStitchesPerCm)) ||
		v.Decimal.GreaterThan(decimal.NewFromInt(MaxStitchesPerCm)) {
		return NewFieldViolation(field, "out_of_range", v.Decimal.String(),
			fmt.Sprintf("density is stitches per CENTIMETRE and runs %d to %d (3-5 is ordinary sewing); clear the field to leave it unset rather than entering 0",
				MinStitchesPerCm, MaxStitchesPerCm))
	}
	return nil
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

// TechCardGarmentZone says WHERE ON THE GARMENT a step works. Mirrors the common.TechCardGarmentZone
// proto enum; stored as a string in tech_card_operation.zone ("unknown" when unset, and REJECTED on
// write — zone is one of the two fields a step cannot be saved without).
//
// It replaces TechCardConstructionZone, which held only the three material bands and therefore could
// not answer «where»; the free-text `placement` carried that and is gone.
type TechCardGarmentZone string

// GarmentZoneTokens is THE vocabulary of garment areas, in reading order (material bands first, then
// areas, then other) — and it has TWO consumers, which is the whole point of it being one slice.
//
// The fitting change-request zone (0256) got here first: it started as a mirror of the old three
// construction bands and widened to garment areas, because a fitting remark is about where on the
// garment the problem is. An operation needs the same answer for the same reason. Holding the same
// eighteen tokens in two places is the drift 0278's header argues against at length, so
// ValidGarmentZones (operations) and ValidFittingChangeZones (fitting) are both DERIVED from here.
//
// A slice, not a map, because a Go map iterates randomly and the same rejection would print its list
// in a different order every time.
var GarmentZoneTokens = []string{
	"unknown", // unset; legal in storage, rejected on an operation write
	// material bands
	"outer", "lining", "interlining",
	// garment areas
	"sleeve", "collar", "neckline", "armhole", "shoulder", "chest", "waist", "hip", "hem",
	"pocket", "closure", "back", "front",
	"other",
}

const (
	ZoneUnknown     TechCardGarmentZone = "unknown"
	ZoneOuter       TechCardGarmentZone = "outer"
	ZoneLining      TechCardGarmentZone = "lining"
	ZoneInterlining TechCardGarmentZone = "interlining"
	ZoneOther       TechCardGarmentZone = "other"
)

// ValidGarmentZones is the set accepted on an operation, derived from GarmentZoneTokens minus the
// "unknown" placeholder — an operation must name a real zone.
var ValidGarmentZones = func() map[TechCardGarmentZone]bool {
	m := make(map[TechCardGarmentZone]bool, len(GarmentZoneTokens))
	for _, z := range GarmentZoneTokens {
		if z == string(ZoneUnknown) {
			continue
		}
		m[TechCardGarmentZone(z)] = true
	}
	return m
}()

// TechCardSeamClass is the ISO 4916 seam class. Stored as a string in tech_card_operation.seam_class
// and tech_card_construction.default_seam_class; "" / NULL = inherit.
//
// It replaces the free-text seam_type, whose suggestion list answered two questions with one value:
// «стачной взаутюжку» and «стачной вразутюжку» are ONE class (SS plain) pressed two different ways.
// Pressing direction is prose on TechCardConstruction.Pressing and is not a seam class.
type TechCardSeamClass string

var SeamClassTokens = []string{
	"ss_plain", "ss_french",
	"ls_lapped", "ls_flat_felled",
	"ef_hem_raw", "ef_hem_turned", "ef_faced",
	"bs_bound", "fs_flat", "os_topstitch",
	"other",
}

var ValidSeamClasses = tokenSet[TechCardSeamClass](SeamClassTokens)

// TechCardAttachmentKind is the folder / guide / presser foot a step runs with. There is no "none"
// token: for a presser foot «none» and «not specified» are the same fact downstream, and offering
// both would make an operator choose between two spellings of nothing.
type TechCardAttachmentKind string

var AttachmentKindTokens = []string{
	"binder", "hemmer_folder", "scroll_foot", "zipper_foot", "invisible_zipper_foot",
	"edge_guide", "piping_foot", "elastic_attachment", "other",
}

var ValidAttachmentKinds = tokenSet[TechCardAttachmentKind](AttachmentKindTokens)

// TechCardTopstitchMode replaces the free-text topstitch_width, whose real values were three
// different KINDS of answer at once: «нет», «в край» (a placement, not a width) and «2 × 6 мм»
// (a width AND a row count).
type TechCardTopstitchMode string

const (
	TopstitchEdge  TechCardTopstitchMode = "edge"
	TopstitchWidth TechCardTopstitchMode = "width"
)

var TopstitchModeTokens = []string{"edge", "width"}

var ValidTopstitchModes = tokenSet[TechCardTopstitchMode](TopstitchModeTokens)

// tokenSet turns a token slice into the membership set its validator uses, so the slice stays the
// single source and the two can never disagree.
func tokenSet[T ~string](tokens []string) map[T]bool {
	m := make(map[T]bool, len(tokens))
	for _, t := range tokens {
		m[T(t)] = true
	}
	return m
}

// MaxTopstitchRows caps the row count. Four rows of topstitching is already decorative extremity;
// past that it is a typo, and an unbounded count reaches the printed sheet as nonsense.
const MaxTopstitchRows = 4

// TechCardOperation is one sewing step of the assembly order (Sheet «Обработка»).
//
// Eleven fields left in the operations break, and none of them are coming back as an optional
// variant: `node`/`description`/`seam_type`/`topstitch_width`/`thread`/`machine`/`seam_allowance`/
// `needle`/`time_norm`/`attachment`/`placement`. Four of those were written by the CODE (a preset,
// the linked BOM line, the joined piece names) and then stored as facts and hashed into a signed
// digest; one duplicated an attribute the thread article already carries; and `node` asked a
// question with no single answer, which is why the pattern-maker who met it could not fill it.
//
// What is left is what a step IS: a verb, a place, what it joins, what it consumes, how long it
// takes — plus overrides that are unset when they agree with the card.
type TechCardOperation struct {
	OperationNumber sql.NullInt32       `db:"operation_number"`
	SMV             decimal.NullDecimal `db:"smv"`  // minutes; the ONLY time field (time_norm is gone)
	Note            sql.NullString      `db:"note"` // the only free text on a step

	// The two REQUIRED fields, and the only two. Both are closed lists — nothing with free input is
	// mandatory on a step ever again, which is exactly what made `node` a trap.
	OperationType TechCardOperationType `db:"operation_type"` // the verb; "unknown" rejected on write
	Zone          TechCardGarmentZone   `db:"zone"`           // where on the garment; "unknown" rejected

	// Overrides. NULL/"" = INHERIT from the card, and the inherited value is never written back into
	// the row: the moment it is, «the technologist chose 4 st/cm» stops being distinguishable from
	// «it defaulted to 4», which is the defect this break exists to remove.
	StitchesPerCm    decimal.NullDecimal `db:"stitches_per_cm"` // STITCHES PER CM — not part of the mm switch
	SeamClass        sql.NullString      `db:"seam_class"`
	SeamAllowanceMm  decimal.NullDecimal `db:"seam_allowance_mm"` // millimetres; 0 is a REAL setting
	TopstitchMode    sql.NullString      `db:"topstitch_mode"`
	TopstitchWidthMm decimal.NullDecimal `db:"topstitch_width_mm"`
	TopstitchRows    sql.NullInt32       `db:"topstitch_rows"`
	AttachmentKind   sql.NullString      `db:"attachment_kind"`
	AttachmentSizeMm decimal.NullDecimal `db:"attachment_size_mm"`

	// CalloutNumber links the operation to a TechCardCallout.number; NULL/0 = none.
	CalloutNumber sql.NullInt32 `db:"callout_number"`

	// PieceLineKeys is the wire reference to the cut-pieces this operation works on, by their stable
	// TechCardPiece.line_key (WS4). The store resolves them to PieceIds. Not persisted (db:"-").
	PieceLineKeys []string `db:"-"`
	// PieceIds are the resolved tech_card_piece FKs, held in the tech_card_operation_piece join
	// table (0199) rather than a column: an assembly operation spans as many pieces as it joins.
	PieceIds []int `db:"-"`
	// BomLineKeys / BomIds are the off-part materials this operation consumes (thread, fusing), held
	// in tech_card_operation_bom (0200). Many-to-many for the same reason the piece links are.
	// The legacy single bom_line_key / bom_item_id / bom_item_index went with the break — the chip
	// row WAS the answer, and the single field was a second one.
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
	LineKey          string `db:"line_key"`
	PiecesPerGarment int    `db:"pieces_per_garment"`
	// RETIRED (0266). The cut list no longer expands this ×2 — 0266 folded the doubling into
	// pieces_per_garment and cleared the flag, so a stored true is historical noise. Kept only so an
	// existing row still round-trips; nothing reads it.
	//
	// AND IT CANNOT BE DROPPED. It sits in constructionProjection's tuple, which json.Marshal encodes
	// POSITIONALLY, so removing it shifts the CONSTRUCTION digest of every card in the database and
	// marks every approved sign-off "changed since approved" at deploy time — for nothing. Frozen
	// false is the cheap state; the rebase is the expensive one. See 0275's header.
	Mirrored bool `db:"mirrored"`
	// CutSymmetry is HOW the piece is cut (0275): how the PiecesPerGarment panels relate to one
	// another. INVALID (NULL) means НЕ РАЗМЕЧЕНО — nobody has answered the question — and is not the
	// same as `identical`. Readers must keep the two apart: an unmarked mirrored piece cut from a half
	// set of patterns yields 44 left fronts and no right ones, and the only thing that prevents it is a
	// visible "not marked".
	//
	// It multiplies NOTHING. The count lives whole in PiecesPerGarment (0266 folded the mirror
	// doubling into it); this field only explains the number already there.
	CutSymmetry sql.NullString `db:"cut_symmetry"`
	// CutSymmetryOmitted — the field was ABSENT on the wire, not "arrived empty": same negative sense
	// and same reason as FabricDirectionOmitted on the BOM line. A tab holding an older bundle does not
	// send it, and a bare proto3 enum's zero value is UNKNOWN, so without the distinction that tab's
	// save would clear the marking on every piece of the card — and unlike направление, the marking
	// cannot be recovered without a human holding the patterns.
	CutSymmetryOmitted bool          `db:"-"`
	Grainline          string        `db:"grainline"`
	Fused              bool          `db:"fused"`
	CalloutNumber      sql.NullInt32 `db:"callout_number"`
	// Detached is set by the store when the piece's callout_number no longer resolves to a callout on
	// the card (its source sketch callout was removed): the piece survives, visibly detached, instead
	// of being silently dropped (orphan-control, S8). Output-only; clients do not set it.
	Detached  bool                    `db:"detached"`
	Note      sql.NullString          `db:"note"`
	Materials []TechCardPieceMaterial `db:"-"`
}

// TechCardPieceCutSymmetry is HOW the panels of one cut-piece relate to each other (0275). All three
// values are statements about MIRROR SYMMETRY, which is why one closed field carries them all rather
// than a "pairing" flag plus an "on the fold" flag:
//
//   - identical — there is no mirror anywhere: n congruent copies;
//   - mirrored  — the instances are related by reflection: n splits into two chiral halves;
//   - fold      — the piece is related to ITSELF by reflection: cutting on the fold unions half the
//     pattern with its own mirror image, so the resulting outline is symmetric by construction.
//
// From the third bullet: `fold` and `mirrored` together are geometrically IMPOSSIBLE, not merely
// unusual — reflecting a symmetric outline is congruent to the outline itself. "On the fold AND
// needed twice" (cuffs) is therefore `fold` with PiecesPerGarment = 2; the chirality question does
// not arise.
//
// NONE of them multiplies anything. Migration 0266 folded the mirror doubling into
// pieces_per_garment precisely so that "4 identical" and "2 mirrored pairs" became the same row; a
// multiplier here would resurrect the bug 0266 was written to kill, because the tech pack prints
// pieces_per_garment and never the total.
type TechCardPieceCutSymmetry string

const (
	PieceCutSymmetryIdentical TechCardPieceCutSymmetry = "identical"
	PieceCutSymmetryMirrored  TechCardPieceCutSymmetry = "mirrored"
	PieceCutSymmetryFold      TechCardPieceCutSymmetry = "fold"
)

// ValidTechCardPieceCutSymmetries mirrors the DB CHECK chk_tcp_cut_symmetry (0275). "Not marked" is
// deliberately absent: it is the NULL column, not a value, so it can never be written as one.
var ValidTechCardPieceCutSymmetries = map[TechCardPieceCutSymmetry]bool{
	PieceCutSymmetryIdentical: true, PieceCutSymmetryMirrored: true, PieceCutSymmetryFold: true,
}

// ValidatePieceCutSymmetry checks one piece's marking against its panel count, with a readable
// message, BEFORE the row reaches MySQL. An INVALID (unset) symmetry is accepted — «не размечено» is
// a legal state and the only honest one for every row that predates 0275.
//
// The evenness rule is not cosmetics: a mirrored pair splits in half, so the nesting expansion hands
// the engine flippedQuantity = n/2, and for n = 3 nobody has defined a rounding rule — the state is
// unresolvable, not ugly. It is enforced twice on purpose (0272's precedent: the schema carries the
// invariant, Go carries the wording). The DB copy is a two-column CHECK, so it also fires on an
// UPDATE that touches only pieces_per_garment; without this function first, the operator would get a
// raw 3819 naming a column they did not edit.
func ValidatePieceCutSymmetry(field string, sym sql.NullString, piecesPerGarment int) error {
	if !sym.Valid {
		return nil
	}
	v := TechCardPieceCutSymmetry(sym.String)
	if !ValidTechCardPieceCutSymmetries[v] {
		return NewFieldViolation(field, "unknown cut symmetry", sym.String,
			"pick one of: identical (одинаковые копии), mirrored (зеркальные пары), fold (крой по сгибу)")
	}
	if v != PieceCutSymmetryMirrored {
		return nil
	}
	if piecesPerGarment < 2 || piecesPerGarment%2 != 0 {
		return NewFieldViolation(field,
			"зеркальная пара делится пополам — количество на изделие должно быть чётным и не меньше двух",
			strconv.Itoa(piecesPerGarment),
			"две строки по одной штуке — это «одинаковые» по штуке каждая; «зеркальные пары» ставят на ОДНУ строку с чётным количеством")
	}
	return nil
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
	TargetDropDate sql.NullTime `db:"target_drop_date"`
	// RequiredSeamAllowanceMm is this style's ТРЕБУЕМЫЙ ПРИПУСК in MILLIMETRES (Ф3.2) — both the
	// standard a readiness gate compares a раскладка's recorded allowance against AND the head of the
	// cascade the sewing steps inherit from (workshop → this → operation.SeamAllowanceMm). INVALID =
	// fall back to the workshop default, which may itself be unset, and then there is NO STANDARD and
	// the consumer must return «no verdict» rather than substitute zero (0 is a legal setting here —
	// see entity.RequiredSeamAllowanceMm).
	//
	// MILLIMETRES since 0290: one unit for the whole allowance chain, so nothing converts between the
	// standard and the step that must honour it. Cutting-table length and fabric edge margin stay in
	// CENTIMETRES — a 12 m table reads worse in millimetres — and the field-name suffix is the guard.
	//
	// It lives on the card header and is in NO section digest projection, deliberately: adding a field
	// to one would mark every signed-off approval of that section as edited-since-signing, on every
	// card at once. (The free-text twin it used to be contrasted with, TechCardConstruction's
	// «5 мм» note, no longer exists — the operations break removed it.)
	RequiredSeamAllowanceMm decimal.NullDecimal `db:"required_seam_allowance_mm"`

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
	// PieceDxfAliases maps DXF block names to cut-pieces, scoped per fabric slot (§2.2). Presence is
	// carried separately (proto3 cannot tell empty-repeated from absent): PieceDxfAliasesSet=false
	// means the payload did not speak — the store preserves stored aliases, so a stale client cannot
	// wipe mappings it never saw; true means full replace with the slice (empty = clear all).
	PieceDxfAliases    []TechCardPieceDxfAlias `db:"-"`
	PieceDxfAliasesSet bool                    `db:"-"`
}

// CostingBaseSizeID is the size a style's STANDARD COST is computed on: its base sample size, or
// 0 when the card names none. Every consumer of the costing basis goes through this one accessor
// precisely so that «the card has no base size» has exactly ONE answer everywhere — 0, which
// TechCardColorwayUsage.UnitTotal turns into an uncosted (не посчитано) size-graded line. The
// moment two call sites resolve it themselves, one of them grows a fallback and the style quietly
// gets a cost nobody approved.
func (tc *TechCardInsert) CostingBaseSizeID() int {
	if tc == nil || !tc.BaseSampleSizeId.Valid || tc.BaseSampleSizeId.Int32 <= 0 {
		return 0
	}
	return int(tc.BaseSampleSizeId.Int32)
}

// TechCardPieceDxfAlias is one DXF block-name → cut-piece mapping, scoped to a fabric slot
// (bom_line_key, the same binding pattern rows carry). BlockName is stored normalized (trim +
// collapsed inner whitespace); the DB UNIQUE per (card, slot, block) is case-insensitive, so
// spelling-case variants collapse into one alias within a slot — the wanted cross-size dedupe.
// PieceLineKey is the wire reference (stable TechCardPiece.line_key); the store resolves it to
// PieceId on write and joins it back on read.
// Since 0267 the scope is a НАЗНАЧЕНИЕ (FabricPurpose) when the card has been sorted, and the
// legacy BomLineKey only when it has not — resolved by entity.ResolveFabricScope, never by reading
// one of the two fields alone. The DB's uniqueness moved with it, onto the generated column
// scope_key = COALESCE(fabric_purpose, bom_line_key): swapping the purpose into the OLD index would
// have made two same-named blocks of two lines sharing one purpose a duplicate, and the store fails
// the WHOLE card save on a duplicate.
type TechCardPieceDxfAlias struct {
	// BomLineKey is the legacy line scope, and doubles as compatibility on a purpose-scoped row: when
	// the purpose owns exactly ONE line the writer records that line here too, so a reader that
	// predates 0267 still sees the binding it understands. Empty when the purpose owns several — there
	// is no single honest answer then, and inventing one is how a class silently becomes an article.
	BomLineKey string `db:"bom_line_key"`
	// FabricPurpose is the scope proper ("" = not purpose-scoped; the row falls back to BomLineKey).
	FabricPurpose string `db:"fabric_purpose"`
	BlockName     string `db:"block_name"`
	PieceId       int    `db:"piece_id"`
	PieceLineKey  string `db:"piece_line_key"`
}

// Scope resolves this alias's binding against the card's cloth lines. The one call every consumer
// makes; see entity/fabric_scope.go for why there is exactly one.
func (a TechCardPieceDxfAlias) Scope(lines []RollGoodsLine) FabricScope {
	return ResolveFabricScope(a.FabricPurpose, a.BomLineKey, lines)
}

// ScopeKey is the uniqueness bucket alone, for the paths that have no BOM to resolve against
// (payload dedupe, keying stored rows). Mirrors the generated column by construction.
func (a TechCardPieceDxfAlias) ScopeKey() string {
	return FabricScopeKey(a.FabricPurpose, a.BomLineKey)
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

	// PatternSizes is GONE, not moved: it counted DISTINCT tech_card_size_pattern.size_id against the
	// range, and Р4 (0280) already took the `patterns` checklist row off it because it lied in both
	// directions. 0281 makes the count permanently wrong on top of dead — a graded sheet is filed
	// under NO size, so an all-sizeless card would report 0 of N. Coverage is
	// tech_card_pattern_size_index and nothing else; anyone wanting «how many sizes have выкройки»
	// must read that, so the tempting field does not sit here waiting to be picked up again.

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
	// Markers are the card's saved раскладки (0257), summaries only — the layout blob travels
	// exclusively on GetTechCardMarker. Populated on the single-card read; nil on lists/writes.
	Markers []TechCardMarkerSummary `db:"-"`
	// MarkerCount is the list-view count of the same thing, batched per page like ColorwayCount.
	MarkerCount int `db:"-"`
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
