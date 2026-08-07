package entity

import (
	"database/sql"
	"errors"
	"sort"
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
	CreatedBy    string            `db:"created_by"`
	UpdatedBy    string            `db:"updated_by"`
	CreatedAt    time.Time         `db:"created_at"`
	UpdatedAt    time.Time         `db:"updated_at"`

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
