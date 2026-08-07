package entity

import (
	"fmt"
	"sort"
	"strings"
)

// СОСТАВ РАСКЛАДКИ (Ф2): a marker stopped being «one size × N комплектов» and became a map, size →
// how many garments of that size the раскладка cuts in one spread.
//
// WHY IT HAD TO STOP BEING A SCALAR. A mixed состав packs TIGHTER than a homogeneous one: the small
// pieces of one size settle into the межлекальные выпады left by another, and the cutter lays
// exactly such spreads. «Size + комплекты» could not express that layout at all, so every norm on
// file was measured on a spread nobody actually cuts. It also made the per-garment figure a mean
// over one size only, which is why the same change has to withhold that mean on a mixed раскладка —
// see MarkerScalarNormRefusal below.
//
// WHERE THE AUTHORITY LIVES. In the layout BLOB (`TechCardMarkerLayout.composition`), because the
// blob is self-contained by construction (0257) and its pieces are tagged with sizes — a blob that
// says which size each contour belongs to but not how many of each size are cut is no longer
// self-contained. `tech_card_marker_size` (0273) is a PROJECTION of that same fact, written in the
// same transaction, and exists because the card's marker list travels WITHOUT the blob and the
// состав is what identifies a marker in that list.

// MarkerLayoutSchemaWithComposition is the first blob version able to express a состав (and a size
// on a piece). Deliberately NOT the same constant as MarkerLayoutSchemaWithFlip: that one gates the
// Ф1 directional-cloth exemption, and bumping it in step with the schema would hand the exemption
// back to every v3 marker — i.e. would repeal Ф1.6 the day after it shipped.
const MarkerLayoutSchemaWithComposition = 4

// MaxMarkerCompositionSizes bounds the состав. It is a sanity ceiling, not a business rule: 32 is
// well past any real размерный ряд, and the real limits bite far earlier — maxMarkerPieces (300)
// caps unique contours, which is Σ over sizes of the pieces in that size, so 30 pieces across 10
// sizes already hits it. The plan explicitly forbids a hard «no more than two sizes» rule
// (03-composition.md): the operator decides, with the budget on screen.
const MaxMarkerCompositionSizes = 32

// MarkerCompositionEntry is one line of a состав: this many GARMENTS of this size. It lives in
// entity and not only in dto because the STORE validates it — membership of the size in the card's
// размерный ряд is a fact only the database can witness — and because it is what the store writes
// into tech_card_marker_size.
type MarkerCompositionEntry struct {
	SizeId   int `db:"size_id"`
	Quantity int `db:"quantity"`
}

// Stable machine-readable reason codes for the composition refusals. A client switches on these.
const (
	// ReasonCompositionMissing — neither a состав nor the legacy (size_id, sets) pair was sent.
	ReasonCompositionMissing = "composition_missing"
	// ReasonCompositionPredatesSchema — the blob carries a состав (or a size on a piece) while
	// declaring a version in which neither field existed. Not a policy refusal: the payload is
	// impossible.
	ReasonCompositionPredatesSchema = "composition_predates_schema"
	// ReasonCompositionNotOnCard — a size of the состав is not in the card's размерный ряд.
	ReasonCompositionNotOnCard = "composition_not_on_card"
)

// ValidateMarkerComposition checks the SHAPE of a состав — everything decidable without the
// database. Membership of the sizes in the card's ряд is the store's (requireCardSizes).
//
// The list is validated as a SET: a duplicated size_id is refused rather than summed, because
// summing would make two payloads that differ only in how the client split a size mean the same
// thing, and the blob (whose bytes are compared by the Ф0.5 regression probe) would stop being a
// function of the состав.
func ValidateMarkerComposition(entries []MarkerCompositionEntry) error {
	if len(entries) > MaxMarkerCompositionSizes {
		return fmt.Errorf("composition has %d sizes, max %d", len(entries), MaxMarkerCompositionSizes)
	}
	seen := make(map[int]bool, len(entries))
	for i, e := range entries {
		if e.SizeId <= 0 {
			return fmt.Errorf("composition[%d].size_id is required", i)
		}
		if e.Quantity < 1 {
			// Never «drop the row and carry on»: a zero here is a client that meant to exclude the
			// size and left its pieces in the blob, and those pieces would then be cut zero times by
			// the instance formula while still counting against the caps.
			return fmt.Errorf("composition[%d].quantity must be at least 1 (size %d)", i, e.SizeId)
		}
		if seen[e.SizeId] {
			return fmt.Errorf("composition lists size %d twice", e.SizeId)
		}
		seen[e.SizeId] = true
	}
	return nil
}

// SortMarkerComposition orders a состав by size_id, in place. Order is part of the stored blob and
// of the wire, and it must not depend on the order a client happened to build its form in — the
// Ф0.5 regression probe asserts «same input ⇒ same blob», and a marker list that reorders itself
// between reads reads as data changing.
func SortMarkerComposition(entries []MarkerCompositionEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].SizeId < entries[j].SizeId })
}

// TotalUnitsOf sums a состав: how many GARMENTS the раскладка cuts. This is the divisor behind
// consumption_per_unit_cm, and it is computed by the SERVER, once, at save time — never accepted
// from the wire, and never re-derived by a reader from the child rows (see 0273 on why the scalar
// is a column: an aggregate that silently loses a child row silently raises the cost of everything
// the marker нормирует).
func TotalUnitsOf(entries []MarkerCompositionEntry) int {
	total := 0
	for _, e := range entries {
		total += e.Quantity
	}
	return total
}

// CompositionPredatesSchema reports a layout that carries a состав (or a size on a piece) while
// declaring a version in which neither field could exist. Mirror of FlipPredatesSchema and refused
// for the same reason: a blob that lies about its own format has to be rejected before anything
// downstream trusts the version for a grandfathering decision. Unlike the flip case there is no
// exemption to steal here — but a client that writes v4 fields under v3 is broken in a way that will
// produce a v3 blob with a состав nobody reads, and that is a silently wrong marker.
func CompositionPredatesSchema(f MarkerLayoutFacts) bool {
	return (f.HasComposition || f.HasPieceSize) &&
		f.SchemaVersion > 0 && f.SchemaVersion < MarkerLayoutSchemaWithComposition
}

// MarkerScalarNormRefusal returns the prose refusing to hand out a SCALAR per-garment norm for this
// раскладка, or "" when the scalar is honest. Non-empty exactly when the состав holds more than one
// size.
//
// WHY A REFUSAL AND NOT A LABEL. used_length / total_units on a mixed состав is the MEAN across the
// sizes: it overstates S and understates XL by exactly the spread Ф2 exists to remove. Labelling it
// would leave the number in place, and the number does not stay a label — a client copies it into
// tech_card_colorway_usage.consumption with consumption_source='marker', after which nothing
// downstream can tell it from a measured norm; the release snapshot then freezes it forever. The
// caption is read once by one person; the figure is read by costing for the life of the style.
//
// It takes nothing away. A homogeneous раскладка is applied exactly as before, and applying a MIXED
// one to a per-size recipe never worked at all — this is an unclosed gap until Ф2.4 (расход по
// площади), not a regression. So the text has to name Ф2.4 and offer an action, or it reads as a
// breakage.
// The sizes are NOT enumerated in the text on purpose: the состав rides the same summary message
// (TechCardMarkerSummary.composition), the client already holds the size dictionary, and a server
// that spelled out «S×1 M×2» would be rendering — badly, by id — something the screen renders
// properly. The prose carries the RULE; the data is beside it.
func MarkerScalarNormRefusal(name string, composition []MarkerCompositionEntry) string {
	if len(composition) <= 1 {
		return ""
	}
	return fmt.Sprintf("раскладка %q снята на смешанном составе (%d размеров, %d изделий), и расход на изделие "+
		"у неё — СРЕДНЕЕ по составу: мелкие размеры оно завышает, крупные занижает. В рецепт такое число не "+
		"пишется. Примените раскладку одного размера, либо дождитесь пер-размерного расхода (Ф2.4), который "+
		"распределит длину настила по площадям деталей.",
		strings.TrimSpace(name), len(composition), TotalUnitsOf(composition))
}
