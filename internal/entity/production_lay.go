package entity

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// НАСТИЛ (production_run_lay / production_run_lay_section, migration 0281) — the ordered list of
// sections (раскладка × слои) a run lays on ONE pair (colourway, BOM slot), plus the facts common to
// the whole lay: the facing mode, the end losses, and the snapshot of the quantities it was built
// for.
//
// A lay is a PLAN, and it is its own aggregate on purpose. It is saved by its own command, never as
// a child of the run's save: a full replace of the run's children on every run save is literally
// what killed migration 0119 (dropped by 0243), and production_run_cost still demonstrates the
// failure mode next door. Both levels carry a client-minted 26-character key so a save DIFFS by
// identity instead of re-minting rows — Ф5б hangs the consumption fact and the cutting receipt off
// production_run_lay_section.id, and a row that is re-created on every note edit cannot be a FK
// target (the same argument migration 0230 made for the run's plan lines).

// ErrProductionRunLayNotFound is returned when a lay addressed by (run, lay_key) does not exist.
// Resolved with a SELECT, never with RowsAffected — see SaveLay.
var ErrProductionRunLayNotFound = errors.New("production run lay not found")

// ErrProductionRunLayConflict is returned when the caller's expected_lock_version does not match the
// stored one, when it is ABSENT on an existing lay, or when two writers race to create the same
// lay_key (the unique index refuses the second INSERT). PRESENCE, not magnitude: the run's own
// contract treats 0 as "skip the check" (productionrun.go), which is a hole 0120 documented and Ф6
// ruled a defect — a new object does not inherit it.
var ErrProductionRunLayConflict = errors.New("production run lay was modified concurrently; reload and retry")

// ErrProductionRunLocked is returned when the run is in a terminal state (received, closed,
// cancelled). A lay is a plan, not history: once the run has stopped being a plan, its lays stop
// being editable.
var ErrProductionRunLocked = errors.New("a received, closed or cancelled production run has no editable lay plan")

// ErrProductionRunLayNotApplicable is returned for an AUXILIARY tech card: it has no colourways, no
// cut pieces and no раскладки, so a lay plan is not a thing that can exist there. Surfaced as
// FailedPrecondition under ProductionRunLayNotApplicableKey — an explicit "not applicable", never an
// empty list, because an empty list reads as an invitation to build one.
var ErrProductionRunLayNotApplicable = errors.New("an auxiliary tech card has no lay plan")

// ProductionRunLayNotApplicableKey is the stable machine-readable reason the API layer attaches to
// ErrProductionRunLayNotApplicable. It names the FACT, not the screen.
const ProductionRunLayNotApplicableKey = "lay_plan_not_applicable"

// ProductionLayMode is how the plies of a lay lie relative to one another. Stored as its lowercase
// string in production_run_lay.mode, whose CHECK (chk_prlay_mode, 0281) closes the dictionary by
// spelling AND by case.
//
// dto.LayFaceMode carries the same two strings for the coverage arithmetic, deliberately declared
// there so that pure geometry file does not depend on the schema; the values are asserted equal in
// TestProductionLayModeMatchesSchema and in the dto's own test.
type ProductionLayMode string

const (
	// ProductionLayModeFaceUp — every ply the same way up: each piece of a mirrored pair needs its
	// own placement in the раскладка.
	ProductionLayModeFaceUp ProductionLayMode = "face_up"
	// ProductionLayModeFaceToFace — plies alternate face up and face down, so ONE placement yields a
	// left and a right from a PAIR of plies. Which is why the ply count has to be even.
	ProductionLayModeFaceToFace ProductionLayMode = "face_to_face"
)

// ValidProductionLayModes is the set of storable facing modes; it mirrors chk_prlay_mode (0281).
var ValidProductionLayModes = map[ProductionLayMode]bool{
	ProductionLayModeFaceUp:     true,
	ProductionLayModeFaceToFace: true,
}

// IsValidProductionLayMode reports whether m is a storable facing mode.
func IsValidProductionLayMode(m ProductionLayMode) bool { return ValidProductionLayModes[m] }

// RequiresEvenPlies reports whether the mode pairs plies. face_to_face does: the last unpaired ply
// yields only one hand, and counting it as half a pair is exactly the arithmetic that produces 44
// left fronts on paper and 22 garments in the cutting room.
func (m ProductionLayMode) RequiresEvenPlies() bool { return m == ProductionLayModeFaceToFace }

// Ply bounds mirror chk_prlays_plies (0281). Enforced in Go as well as in the schema so the refusal
// names the offending section instead of surfacing MySQL error 3819.
const (
	ProductionLayPliesMin = 1
	ProductionLayPliesMax = 500
)

// EndLossCm bounds mirror chk_prlay_end_loss (0281).
var (
	ProductionLayEndLossMinCm = decimal.Zero
	ProductionLayEndLossMaxCm = decimal.NewFromInt(100)
)

// ProductionLayKeyLen is the exact length of a lay key and of a section key: the CHAR(26) columns
// and the 26-character ULID the admin client mints.
const ProductionLayKeyLen = ProductionRunLineKeyLen

// IsValidProductionLayKey reports whether k is an acceptable lay/section key. Delegates to the run
// line rule so the repository has ONE definition of "a 26-character stable key" — a second charset
// would eventually reject a key this server itself minted.
func IsValidProductionLayKey(k string) bool { return IsValidProductionRunLineKey(k) }

// MintProductionLayKey creates the stable identity of a lay (or of one of its sections) for a
// payload that arrived without one. Delegates to the run line minter for the same reason: one
// encoder, so the two can never drift.
func MintProductionLayKey() (string, error) { return MintProductionRunLineKey() }

// ProductionLayActualMethod is HOW the consumption fact of a настил was measured (Ф5б.2, migration
// 0285). Stored as its lowercase string in production_run_lay.actual_method, whose CHECK
// (chk_prlay_actual_method) closes the dictionary by spelling AND by case.
//
// The method is part of the fact and not a footnote: рулон до/после and взвешивание lie in different
// directions and by different amounts (a tape reads the roll's outer length and ignores the core; a
// scale reads everything the roll still carries, размотка included). A number whose provenance is
// unknown cannot be argued with, which is why chk_prlay_actual_complete refuses a quantity without
// one.
type ProductionLayActualMethod string

const (
	// ProductionLayActualMethodRollBeforeAfter — the roll was measured before and after the lay, and
	// the fact is the difference. Naturally a LENGTH.
	ProductionLayActualMethodRollBeforeAfter ProductionLayActualMethod = "roll_before_after"
	// ProductionLayActualMethodWeighed — what was laid (or what is left) was put on the scale.
	// Naturally a WEIGHT, which is why the drift below refuses to compare it with a plan measured in
	// centimetres instead of guessing a conversion.
	ProductionLayActualMethodWeighed ProductionLayActualMethod = "weighed"
)

// ValidProductionLayActualMethods mirrors chk_prlay_actual_method (0285).
var ValidProductionLayActualMethods = map[ProductionLayActualMethod]bool{
	ProductionLayActualMethodRollBeforeAfter: true,
	ProductionLayActualMethodWeighed:         true,
}

// IsValidProductionLayActualMethod reports whether m is a storable measuring method.
func IsValidProductionLayActualMethod(m ProductionLayActualMethod) bool {
	return ValidProductionLayActualMethods[m]
}

// ProductionRunLayActualInput is the consumption fact as a WRITER states it: how much really went,
// in which unit, measured how. Ф5б.2.
//
// «ФАКТ ЦЕЛИКОМ ИЛИ НИКАК» (chk_prlay_actual_complete). A quantity implies a unit and a method; the
// implication is ONE-WAY, because a unit chosen in a form before the quantity was typed is a
// half-filled form and not a lie. ValidateProductionRunLayActual is the Go twin of that CHECK and
// exists so the refusal NAMES the missing field instead of surfacing MySQL error 3819.
//
// Qty invalid means CLEAR: the fact is withdrawn whole (quantity, unit, method, and the who/when
// stamp with them). A measurement that turned out to be somebody else's roll must be removable, and
// leaving the stamp behind would leave a signature under a number that is gone.
type ProductionRunLayActualInput struct {
	// Qty is in Uom, not in the article's unit and not in centimetres. It is > 0 when present —
	// chk_prlay_actual_qty; a zero consumption is not a measurement, it is an empty field.
	Qty decimal.NullDecimal
	// Uom comes from the CLOSED vocabulary of Ф5а.3 (MaterialUnit), never free text: a fact in metres
	// against a plan in kilograms is a silently wrong subtraction, which is the whole reason the
	// vocabulary exists.
	Uom    MaterialUnit
	Method ProductionLayActualMethod
}

// HasQty reports whether the input states a quantity (as opposed to withdrawing the fact).
func (a ProductionRunLayActualInput) HasQty() bool { return a.Qty.Valid }

// ValidateProductionRunLayActual enforces «факт целиком или никак» in Go, with the offending field
// named. field is the payload path of the fact's parent (e.g. "lay"), so the violation points at
// lay.actual_qty rather than at "the request".
//
// The unit and the method are validated WHENEVER they are non-empty, quantity or no quantity: the
// schema's CHECKs on them (chk_prlay_actual_uom, chk_prlay_actual_method) fire on a half-filled form
// just as hard, and a MySQL 3819 is not an answer a cutting room can act on.
func ValidateProductionRunLayActual(field string, a ProductionRunLayActualInput) error {
	if a.Qty.Valid && !a.Qty.Decimal.IsPositive() {
		return NewFieldViolation(field+".actual_qty", "out_of_range", a.Qty.Decimal.String(),
			"a consumption fact is a positive quantity; leave it empty to withdraw the fact instead")
	}
	if raw := strings.TrimSpace(string(a.Uom)); raw != "" {
		if _, ok := NormalizeMaterialUnit(raw); !ok {
			return NewFieldViolation(field+".actual_uom", "unknown_unit", raw,
				"measure the fact in one of the known units (m, cm, mm, m2, g, kg, pcs, pair, set, cone, roll)")
		}
	} else if a.Qty.Valid {
		return NewFieldViolation(field+".actual_uom", "required", "",
			"say what the quantity is measured in — a fact without a unit is a number nothing can be added to")
	}
	if raw := ProductionLayActualMethod(strings.TrimSpace(string(a.Method))); raw != "" {
		if !IsValidProductionLayActualMethod(raw) {
			return NewFieldViolation(field+".actual_method", "unknown_method", string(raw),
				"say how it was measured: roll_before_after or weighed")
		}
	} else if a.Qty.Valid {
		return NewFieldViolation(field+".actual_method", "required", "",
			"say how it was measured: roll_before_after or weighed")
	}
	return nil
}

// ProductionRunLayQtyEntry is one (size, quantity) pair of a lay's quantity snapshot. The JSON tags
// are load-bearing: this is the exact shape stored in production_run_lay.qty_snapshot, written by
// the server only — a snapshot accepted from the client could be forged, and a forged snapshot
// silences the very "quantities changed" badge it exists to raise.
type ProductionRunLayQtyEntry struct {
	SizeId int `json:"size_id" db:"size_id"`
	Qty    int `json:"qty" db:"qty"`
}

// NormalizeProductionRunLayQty returns the canonical form of a quantity set: duplicates summed by
// size, non-positive quantities dropped, sorted by size_id. Both the stored snapshot and the live
// comparison go through it, so "stale" is a statement about the DATA and not about the order rows
// happened to arrive in.
func NormalizeProductionRunLayQty(entries []ProductionRunLayQtyEntry) []ProductionRunLayQtyEntry {
	if len(entries) == 0 {
		return []ProductionRunLayQtyEntry{}
	}
	bySize := make(map[int]int, len(entries))
	for _, e := range entries {
		bySize[e.SizeId] += e.Qty
	}
	out := make([]ProductionRunLayQtyEntry, 0, len(bySize))
	for sizeID, qty := range bySize {
		if qty <= 0 {
			continue
		}
		out = append(out, ProductionRunLayQtyEntry{SizeId: sizeID, Qty: qty})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SizeId < out[j].SizeId })
	return out
}

// ProductionRunLayQuantitiesStale reports whether the run's CURRENT quantities for the lay's
// colourway differ from the snapshot the lay was built against. It is a pure comparison of the two
// canonical forms: the badge must be a function of the data, and nothing but an explicit
// reaffirmation or a real section edit may clear it.
func ProductionRunLayQuantitiesStale(snapshot, current []ProductionRunLayQtyEntry) bool {
	a := NormalizeProductionRunLayQty(snapshot)
	b := NormalizeProductionRunLayQty(current)
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}

// ProductionRunLaySectionInsert is one writable section of a lay: which раскладка, how many plies,
// where in the order.
//
// SectionKey — and not the position, and not (marker_id, plies) — is the identity. Position is an
// EDITABLE attribute, so keying on it would make swapping two sections read as deleting both and
// creating two others; and changing the ply count is the single most common edit there is, so
// keying on it would re-mint the row that Ф5б is about to hang a consumption fact on. A row has to
// be durable BEFORE anything points at it — the verbatim lesson of 0230.
type ProductionRunLaySectionInsert struct {
	SectionKey string
	MarkerId   int
	Plies      int
	Position   int
}

// ProductionRunLaySection is a stored section, with the раскладка facts a reader needs to turn it
// into a length. The marker geometry rides along from the join because every consumer of a lay
// needs it and re-reading markers per section would be an N+1 over a number the operator controls.
type ProductionRunLaySection struct {
	Id         int    `db:"id"`
	LayId      int    `db:"lay_id"`
	SectionKey string `db:"section_key"`
	MarkerId   int    `db:"marker_id"`
	Plies      int    `db:"plies"`
	Position   int    `db:"position"`
	// MarkerName / MarkerUsedLengthCm / MarkerFabricWidthCm / MarkerTotalUnits come from
	// tech_card_marker. MarkerBomItemId is carried so a reader can see that a раскладка has since
	// lost its BOM line (fk_tcm_bom is SET NULL, 0257) and no longer matches the lay's slot — the
	// check has to exist on READ too, not only on write.
	MarkerName          string              `db:"marker_name"`
	MarkerUsedLengthCm  decimal.NullDecimal `db:"marker_used_length_cm"`
	MarkerFabricWidthCm decimal.NullDecimal `db:"marker_fabric_width_cm"`
	MarkerTotalUnits    sql.NullInt64       `db:"marker_total_units"`
	MarkerBomItemId     sql.NullInt64       `db:"marker_bom_item_id"`
}

// ProductionRunLayInsert is the writable payload of ONE lay. It addresses exactly one lay by
// LayKey, and SaveLay can see no other: a lay missing from a payload is NOT touched, because
// deletion is DeleteLay's job alone. That asymmetry with the sections (a section missing from the
// payload IS deleted — the full list of sections is what a lay IS) is the structural answer to the
// cause of death of 0119.
type ProductionRunLayInsert struct {
	// LayKey is client-minted; empty means "create", and the server mints one.
	LayKey     string
	ColorwayId int
	// BomLineKey addresses the cloth slot by its STABLE key, not by id (the line_key world of
	// 0230/0159). It is also snapshotted onto the row, so a lay whose slot later leaves the BOM
	// (fk_prlay_bom is SET NULL) can still NAME what it lost instead of going quiet.
	BomLineKey   string
	Mode         ProductionLayMode
	EndLossCm    decimal.Decimal
	Name         string
	Note         sql.NullString
	DisplayOrder int
	Sections     []ProductionRunLaySectionInsert
	// QtySnapshot is deliberately ABSENT: the server computes it from ITS OWN run lines.

	// LotId and Actual carry PRESENCE, and they are the only two fields of this payload that do
	// (Ф5б.1/Ф5б.2). nil means «this payload does not speak about the lot / about the fact» and the
	// stored value survives untouched; a non-nil value replaces it.
	//
	// WHY THESE TWO ARE NOT PLAIN FIELDS. Every other attribute here is full-replace, and that is
	// safe because the lock refuses a payload built against an older version. The lock does NOT
	// protect against a writer that reads the CURRENT lay and simply does not know a field exists —
	// and that writer is guaranteed to exist here: every client written before migration 0285 sends
	// a lay payload with no lot and no fact, with a perfectly correct expected version. Full replace
	// would let the planner's save silently erase the cutting room's MEASUREMENT — a number that
	// cannot be re-derived, because the roll has already been cut. Absence must therefore mean
	// silence, exactly as it does one level up, where a lay missing from a payload is not deleted.
	//
	// LotId: a pointer to a positive id BINDS (and snapshots the lot's code); a pointer to 0 or less
	// UNBINDS. Actual: a non-nil fact with a quantity RECORDS it; a non-nil fact without one
	// WITHDRAWS it.
	LotId  *int
	Actual *ProductionRunLayActualInput
}

// ProductionRunLay is a stored lay with its sections and both quantity sets.
type ProductionRunLay struct {
	Id           int    `db:"id"`
	RunId        int    `db:"run_id"`
	LayKey       string `db:"lay_key"`
	ColorwayId   int    `db:"colorway_id"`
	ColorwayName string `db:"colorway_name"`
	// BomItemId is NULL when the slot has been deleted from the BOM (ON DELETE SET NULL). Such a lay
	// is BROKEN: it still names its slot through BomLineKey and must be reported, never silently
	// counted.
	BomItemId    sql.NullInt64     `db:"bom_item_id"`
	BomLineKey   string            `db:"bom_line_key"`
	BomItemName  sql.NullString    `db:"bom_item_name"`
	Mode         ProductionLayMode `db:"mode"`
	EndLossCm    decimal.Decimal   `db:"end_loss_cm"`
	Name         string            `db:"name"`
	Note         sql.NullString    `db:"note"`
	DisplayOrder int               `db:"display_order"`
	LockVersion  int               `db:"lock_version"`

	// LotId is the roll this lay was laid off (0285, ON DELETE SET NULL). Invalid means either «no
	// lot was ever named» or «the lot has since been deleted» — and LotCode is what tells the two
	// apart, because it is a NOT NULL snapshot taken when the binding was made. That is the price
	// SET NULL is paid with: a lay that lost its roll must still be able to NAME it. Same fork, same
	// answer as BomItemId/BomLineKey in 0281.
	LotId   sql.NullInt64 `db:"lot_id"`
	LotCode string        `db:"lot_code"`
	// The lot facts ride along from the join for the same reason the marker geometry does: every
	// reader of a lay needs them (the width check of Ф5б, the shade note on the lay screen, the
	// per-lot reserve of a перекрой), and re-reading the lot per lay would be an N+1 over a number
	// the operator controls. All three are NULL exactly when the lot is gone.
	LotMaterialId      sql.NullInt64       `db:"lot_material_id"`
	LotMeasuredWidthCm decimal.NullDecimal `db:"lot_measured_width_cm"`
	LotShadeCode       sql.NullString      `db:"lot_shade_code"`

	// ActualQty..ActualAt are the consumption FACT (Ф5б.2): how much cloth really went, in which
	// unit, measured how, by whom, when. Invalid ActualQty means the fact has not been recorded —
	// NOT that nothing was consumed.
	//
	// The fact lives HERE and not on production_run.actual_wastage_percent, which is untouched: that
	// one is a percentage on the RUN serving the non-marker lines of the material plan (marker norms
	// ignore it by construction), while this is a measurement of ONE roll laid into ONE lay. Making
	// one number answer both questions is precisely the merge Ф5б refused.
	ActualQty    decimal.NullDecimal `db:"actual_qty"`
	ActualUom    sql.NullString      `db:"actual_uom"`
	ActualMethod sql.NullString      `db:"actual_method"`
	ActualBy     string              `db:"actual_by"`
	ActualAt     sql.NullTime        `db:"actual_at"`

	CreatedBy string    `db:"created_by"`
	UpdatedBy string    `db:"updated_by"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`

	Sections []ProductionRunLaySection `db:"-"`
	// QtySnapshot is what the run planned for this colourway when the lay was last BUILT;
	// QtyCurrent is what it plans today. Their inequality is QuantitiesStale — the badge.
	QtySnapshot     []ProductionRunLayQtyEntry `db:"-"`
	QtyCurrent      []ProductionRunLayQtyEntry `db:"-"`
	QuantitiesStale bool                       `db:"-"`
}

// Broken reports whether the lay lost its BOM slot. A broken lay names the slot it lost
// (BomLineKey) and must drop out of coverage and demand with an explicit finding.
func (l ProductionRunLay) Broken() bool { return !l.BomItemId.Valid }

// HasActual reports whether the consumption fact has been recorded. A lay without one is not a lay
// that consumed nothing.
func (l ProductionRunLay) HasActual() bool { return l.ActualQty.Valid }

// LotDetached reports whether the lay names a lot that no longer exists (ON DELETE SET NULL fired).
// Such a lay is not broken — it is still a plan and still a fact — but every consumer that wanted
// the lot's WIDTH or SHADE has to answer UNKNOWN rather than «fits», and the screen has to show the
// remembered code so the roll can be found on paper.
func (l ProductionRunLay) LotDetached() bool { return !l.LotId.Valid && l.LotCode != "" }

// ActualUnit resolves the fact's stored unit against the closed vocabulary (Ф5а.3). ok=false for an
// absent unit and for anything the vocabulary does not know — the caller must then treat the number
// as unaddable, never as metres.
func (l ProductionRunLay) ActualUnit() (MaterialUnit, bool) {
	if !l.ActualUom.Valid {
		return "", false
	}
	return NormalizeMaterialUnit(l.ActualUom.String)
}

// Machine-readable reasons a lay's plan/fact drift cannot be stated. They are REASONS and not a
// zero, because «0%» is the most confident wrong answer this phase can give: it reads as «the plan
// held exactly», which is the one conclusion nobody has earned.
const (
	// LayDriftReasonNoActual — nobody has measured this lay yet.
	LayDriftReasonNoActual = "no_actual"
	// LayDriftReasonNoPlan — the lay has no positive planned length (no sections, or раскладки with
	// no measured length). Dividing by it would be an invention, not a drift.
	LayDriftReasonNoPlan = "no_plan"
	// LayDriftReasonUnitUnknown — the stored unit is not in the vocabulary at all.
	LayDriftReasonUnitUnknown = "unit_unknown"
	// LayDriftReasonUnitNotLength — the fact is in a unit the lay's plan is not measured in (kg is
	// the live case: взвешивание). Converting it would need the article's density and width, i.e. a
	// guess wearing a number's clothes — the same refusal the material plan makes when a slot's unit
	// is not metres.
	LayDriftReasonUnitNotLength = "unit_not_length"
)

// ProductionRunLayDriftVerdict is the plan/fact comparison of ONE lay: THREE values, like every
// other verdict in this phase — known, or absent WITH a reason.
type ProductionRunLayDriftVerdict struct {
	Known bool
	// Drift is факт / план − 1: +0.08 means eight per cent more cloth went than the lay planned.
	// Meaningless unless Known.
	Drift decimal.Decimal
	// PlannedInFactUnit is the lay's plan restated in the unit the fact was measured in, so a screen
	// can put the two numbers side by side without redoing the conversion. Meaningless unless Known.
	PlannedInFactUnit decimal.Decimal
	// Reason is empty exactly when Known.
	Reason string
}

// ProductionRunLayDrift computes the plan/fact drift of ONE lay. plannedClothCm is the lay's PLANNED
// length in centimetres — раскладка length × plies + end losses, the Ф4 arithmetic — and it is
// PASSED IN rather than recomputed here: that sum already has one owner in dto, and a second
// implementation would drift from it long before any cloth did.
//
// THE DRIFT IS COMPUTED, NEVER STORED. It is a function of two numbers that both already exist, and
// a stored copy would be a second source of truth that goes stale the first time a section is
// edited — the plan would move and the remembered drift would not.
//
// NO CUTTING COEFFICIENT IS INVOLVED, and that is the load-bearing part. Ф4 ruled that the article's
// coefficient is NOT applied on the lay path, so plannedClothCm is pure geometry — and the drift is
// therefore exactly the quantity the coefficient is supposed to cover (shrinkage, defect avoidance,
// splicing, shade banding). Had the coefficient been applied to the plan, calibrating it from this
// number would be circular: the coefficient would correct the plan, and the plan would then correct
// the coefficient.
func ProductionRunLayDrift(plannedClothCm decimal.Decimal, l ProductionRunLay) ProductionRunLayDriftVerdict {
	if !l.ActualQty.Valid {
		return ProductionRunLayDriftVerdict{Reason: LayDriftReasonNoActual}
	}
	unit, ok := l.ActualUnit()
	if !ok {
		return ProductionRunLayDriftVerdict{Reason: LayDriftReasonUnitUnknown}
	}
	perUnitCm, ok := materialUnitCentimetres(unit)
	if !ok {
		return ProductionRunLayDriftVerdict{Reason: LayDriftReasonUnitNotLength}
	}
	if !plannedClothCm.IsPositive() {
		return ProductionRunLayDriftVerdict{Reason: LayDriftReasonNoPlan}
	}
	// The FACT is converted into the plan's centimetres (exact multiplications), and the plan is
	// restated in the fact's unit only for display. Converting in this direction keeps the ratio free
	// of an intermediate rounding.
	actualCm := l.ActualQty.Decimal.Mul(perUnitCm)
	return ProductionRunLayDriftVerdict{
		Known:             true,
		Drift:             actualCm.Div(plannedClothCm).Sub(decimal.NewFromInt(1)).Round(6),
		PlannedInFactUnit: plannedClothCm.Div(perUnitCm).Round(3),
	}
}

// materialUnitCentimetres is how many centimetres ONE of the given unit is, for the LENGTH units
// only. It is deliberately private and deliberately tiny: it exists to answer «can this fact be
// compared with a lay's plan at all», not to become a general unit-conversion table — a general one
// would eventually be asked to turn kilograms into metres, which needs the article's grammage and
// width and is therefore a different question with a different owner.
func materialUnitCentimetres(u MaterialUnit) (decimal.Decimal, bool) {
	switch u {
	case MaterialUnitM:
		return decimal.NewFromInt(100), true
	case MaterialUnitCm:
		return decimal.NewFromInt(1), true
	case MaterialUnitMm:
		return decimal.NewFromFloat(0.1), true
	}
	return decimal.Zero, false
}

// TotalPlies is Σ plies over the sections — the multiplier the end losses apply to.
func (l ProductionRunLay) TotalPlies() int {
	total := 0
	for _, s := range l.Sections {
		total += s.Plies
	}
	return total
}

// ProductionRunLayList is the answer to "show me this run's lay plan". Applicable is stated
// EXPLICITLY rather than implied by an empty list: "there is no such thing here" and "none built
// yet" are different sentences, and only the second one is an invitation.
type ProductionRunLayList struct {
	Applicable          bool
	NotApplicableReason string
	Lays                []ProductionRunLay
}
