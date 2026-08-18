package dto

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// ВЫХОД НАСТИЛА (Ф4.5). Everything in this file is a PURE FUNCTION of a stored marker blob plus a
// handful of card facts. It answers exactly one chain of questions, and nothing here touches the
// database, the store or the wire:
//
//	блоб маркера        → сколько экземпляров каждой детали кладёт ОДИН СЛОЙ   (MarkerYieldFromBlob,
//	                                                                            PerLayerInstances)
//	слои × режим        → сколько экземпляров ФИЗИЧЕСКИ выкроено               (LayerCutInstances)
//	cut_symmetry (0275) → сколько ИЗДЕЛИЙ из этой детали получится             (PieceYieldGarments)
//	минимум по деталям  → статус клетки (колорвей, размер)                     (CoverageCell)
//
// Steps 0 and 4 of §6.2 — resolving a piece to its BOM slot and choosing WHICH pieces are required
// for a colourway — are deliberately NOT here: both need the card and the plan, and both already
// have one implementation each (planBomLine / planSlotSections in production_material_plan.go). A
// second copy of either is how a plan and a coverage badge start disagreeing about the same run.
//
// ЧТО ЭТО НЕ ДЕЛАЕТ, И ПОЧЕМУ ЭТО ВАЖНО. It never invents a number to stand in for one it does not
// have. Every step carries a THIRD answer next to «столько-то» and «ноль» — «не могу сказать» — and
// that answer propagates all the way to the cell. A zero here means «доказано, что не выкроено»; an
// unknown means «доказать нечем». Collapsing the two is the whole failure mode this file exists to
// prevent: an unknown read as zero invents a shortage that is not there, and an unknown read as OK
// signs off a run that cannot be cut.

// Layout blob schema versions (TechCardMarkerLayout.schema_version, techcard.proto). Named because
// two of them are load-bearing HERE and the numbers alone say nothing at the call site.
const (
	// markerLayoutSchemaOriginal is the original geometry: no piece_line_key, so a piece in such a
	// blob cannot be attributed to a cut-piece of the card at all.
	markerLayoutSchemaOriginal = 1
	// markerLayoutSchemaPieceKeys added piece_line_key/block_name — the first version whose pieces
	// can be matched to tech_card_piece.line_key.
	markerLayoutSchemaPieceKeys = 2
	// markerLayoutSchemaFlip added `flipped` on a placement (Ф1). BELOW THIS VERSION CHIRALITY IS
	// UNKNOWABLE, not absent: both hands may have been separate DXF blocks laid out as ordinary
	// pieces, so «zero mirrored placements» is not evidence that no right fronts were cut.
	markerLayoutSchemaFlip = 3
	// markerLayoutSchemaComposition added layout.composition and piece.size_id (Ф2).
	markerLayoutSchemaComposition = 4
)

// MarkerPieceKey identifies a piece of a marker in the terms of the CARD, not of the blob:
// TechCardMarkerPiece.piece_id is parse-local and stable only inside one blob (techcard.proto:2098),
// so it cannot key anything that outlives the parse.
type MarkerPieceKey struct {
	// PieceLineKey is tech_card_piece.line_key as resolved at save time. EMPTY is a real key here and
	// means the blob could not attribute this piece to the card — a schema-1 blob, or a v2+ blob whose
	// piece was unresolved when it was saved. It is kept as a bucket rather than dropped so the
	// instances it holds are visible to anyone auditing the parse; it must never be read as an answer
	// about a card piece. MarkerYield.UnattributedPieces is the flag that says so out loud.
	PieceLineKey string
	// SizeId is the size whose GRADATION this contour belongs to (schema 4). 0 = the piece does not
	// grade, so one set of it is cut for every garment of the whole состав.
	SizeId int
}

// MarkerPieceCounts is how many instances of one piece a marker carries, split by chirality.
//
// The split is the INPUT of the coverage question, never a variable of it: a mirrored pair is n left
// panels and n right ones, and a marker that lays 44 as-drawn and 0 mirrored has cut 44 left fronts
// and no right ones however many pieces it "placed". That is the exact defect `flipped` was added to
// make expressible (techcard.proto, TechCardMarkerPlacement.flipped).
type MarkerPieceCounts struct {
	AsDrawn  int // placements with flipped = false
	Mirrored int // placements with flipped = true
}

// Total is the instance count ignoring chirality — meaningful only for `fold`, where the outline is
// symmetric by construction and the two hands are the same piece.
func (c MarkerPieceCounts) Total() int { return c.AsDrawn + c.Mirrored }

// MarkerYield is everything coverage takes out of one layout blob, distilled in one parse.
//
// Пара к MarkerLayoutFactsFromBlob (techcard.go): same idiom, same tolerance, different question.
// That one asks whether a stored placement is upside down; this one asks what the marker CUTS. They
// stay separate because the save path must be able to read a blob it would refuse to accept, and
// coverage must be able to refuse a blob the save path already stored.
type MarkerYield struct {
	// SchemaVersion is the version the BLOB declares. For `flipped` this field is the authority —
	// techcard.proto says so explicitly: a mirrored placement in a blob declaring less than 3 is an
	// impossible payload, not a policy question. (The rotation POLICY reads the server-written column
	// tech_card_marker.layout_schema_version instead; that is a different number for a different
	// decision, and this one must not be used for it.)
	SchemaVersion int
	// TotalUnits is Σ Composition — garments, not pieces. 0 when the blob carries no состав; see
	// CompositionKnown.
	TotalUnits int
	// Composition is size_id → garments of that size. NIL on a legacy blob (schema < 4): the состав
	// lives on the marker SUMMARY there, and WithSummaryComposition is how it gets folded in. Nil is
	// «блоб не знает состава», never «состав пуст» — techcard.proto states a composition is never
	// empty on the wire.
	Composition map[int]int
	// Pieces is instances PLACED in the marker, by piece and size — counted off the PLACEMENTS, so
	// this number is «сколько легло», never «сколько просили разложить».
	//
	// THE TWO USED TO BE THE SAME THING AND ARE NOT ANY MORE. Until 0299 SaveMarker refused every
	// layout with PlacedCount != TotalCount, so a stored blob's placements were complete by
	// construction and this field could be read as the marker's full yield. Черновик (is_draft) is
	// exactly the row where they differ: the движок ran out of budget, some pieces were never laid,
	// and counting the placements of one would understate its yield by however many are missing —
	// which lands here as a coverage BLOCKER on a настил nobody actually laid short.
	//
	// A draft never reaches this parse: requireLaySectionMarkers (0299) refuses a черновик as a
	// section's раскладка, and a section is the only route from a stored marker into coverage. The
	// property this field rests on is therefore that guard, not the save path — which is why the
	// sentence naming it is here rather than a comment about placed_count elsewhere.
	Pieces map[MarkerPieceKey]MarkerPieceCounts
	// PieceNames is piece_line_key → the piece's name as written in the blob, for messages. Only
	// attributed pieces appear.
	PieceNames map[string]string
	// UnattributedPieces is how many DISTINCT pieces of the blob carry no piece_line_key.
	//
	// NOT IN §6.1's struct, and added on purpose. Without it a schema-1 blob is indistinguishable
	// from a marker that simply does not cut the piece being asked about: both answer «no entry for
	// that key». One of those is an honest zero and the other is «неатрибутируемо», and §6.3 puts
	// them on opposite sides of the table — proven shortage versus UNKNOWN. SchemaVersion < 2 is not
	// a sufficient proxy either: techcard.proto allows piece_line_key to be "" on a v2+ piece that
	// was unresolved at save time.
	UnattributedPieces int
}

// CompositionKnown reports whether the BLOB itself said which garments it cuts. False is the honest
// «не могу» for every legacy blob and is the reason PerLayerInstances refuses rather than dividing
// by a zero TotalUnits.
func (y MarkerYield) CompositionKnown() bool { return len(y.Composition) > 0 }

// ChiralityKnown reports whether «zero mirrored placements» is EVIDENCE or merely absence. False
// below schema 3, where the field did not exist — see markerLayoutSchemaFlip.
func (y MarkerYield) ChiralityKnown() bool { return y.SchemaVersion >= markerLayoutSchemaFlip }

// Attributable reports whether piece lookups against this blob mean anything at all. False as soon
// as ONE piece is unattributed, and bluntly so: an unattributed piece may be another DXF block of
// the very piece being asked about, so every answer off this blob would be a floor, and a floor read
// as a fact turns into a shortage nobody can reproduce. Refusing wholesale costs an UNKNOWN badge;
// answering low costs a BLOCKER on a run that is fine.
//
// The version is checked as well as the keys, and not merely as a shortcut: piece_line_key did not
// exist before schema 2, so a v1 blob CANNOT be attributable whatever its bytes happen to contain.
// protojson is tolerant enough to fill the field on a blob declaring 1, and a reader that trusted
// the field alone would attribute pieces off a document whose writer never resolved any.
func (y MarkerYield) Attributable() bool {
	return y.SchemaVersion >= markerLayoutSchemaPieceKeys && y.UnattributedPieces == 0
}

// WithSummaryComposition returns a copy of y with the состав supplied from the marker's SUMMARY —
// the pre-Ф2 shape «one size, N комплектов», which migration 0273 projected onto every legacy row.
//
// It is a no-op returning an error when the blob already carries a состав: two sources for one fact
// is how they start to disagree, and the blob is the one that was measured.
func (y MarkerYield) WithSummaryComposition(sizeID, garments int) (MarkerYield, error) {
	if y.CompositionKnown() {
		return y, fmt.Errorf("marker layout already carries a composition (%d sizes); the summary's must not override it", len(y.Composition))
	}
	if sizeID <= 0 {
		return y, fmt.Errorf("summary size_id must be positive, got %d", sizeID)
	}
	if garments < 1 {
		return y, fmt.Errorf("summary sets must be at least 1, got %d", garments)
	}
	y.Composition = map[int]int{sizeID: garments}
	y.TotalUnits = garments
	return y, nil
}

// MarkerYieldFromBlob distils one stored layout blob into what coverage needs, in a single parse.
//
// TOLERANT ON PURPOSE, exactly like MarkerLayoutFactsFromBlob: DiscardUnknown so a blob written by a
// newer server stays readable after a rollback, and no judgement at all about geometry (an angle
// outside the four is not this function's business). The error is reserved for a blob that is not a
// coherent document — one that does not parse, one with no placements, one whose placement points at
// a piece that is not there, and one that claims a mirror in a version where the field did not
// exist. Every one of those would otherwise be READ AS A SMALLER NUMBER, and a smaller number here
// is a shortage the workshop cannot reproduce.
func MarkerYieldFromBlob(blob string) (MarkerYield, error) {
	var l pb_common.TechCardMarkerLayout
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(blob), &l); err != nil {
		return MarkerYield{}, fmt.Errorf("stored marker layout does not parse: %w", err)
	}

	version := int(l.GetSchemaVersion())
	if version == 0 {
		// A blob that arrives with 0 is a v1 blob — the same reading MarkerLayoutFactsFromPb's
		// contract states. Normalised here so ChiralityKnown never has to special-case it.
		version = markerLayoutSchemaOriginal
	}
	if version < markerLayoutSchemaOriginal || version > markerLayoutSchemaComposition {
		// A version we have never written. Not refused — DiscardUnknown exists so a newer blob stays
		// readable — but a version ABOVE ours may have redefined what a placement counts as, so the
		// only safe thing is to keep the number and let ChiralityKnown/Attributable speak. Below 1 is
		// corruption, and that one IS refused.
		if version < markerLayoutSchemaOriginal {
			return MarkerYield{}, fmt.Errorf("stored marker layout declares schema_version %d", version)
		}
	}

	out := MarkerYield{
		SchemaVersion: version,
		Pieces:        make(map[MarkerPieceKey]MarkerPieceCounts),
		PieceNames:    make(map[string]string),
	}

	if entries := l.GetComposition(); len(entries) > 0 {
		comp := make([]entity.MarkerCompositionEntry, 0, len(entries))
		for _, e := range entries {
			comp = append(comp, entity.MarkerCompositionEntry{SizeId: int(e.GetSizeId()), Quantity: int(e.GetQuantity())})
		}
		// The same validator the save path ran. Re-run rather than trusted because a duplicated size
		// silently doubles TotalUnits, and TotalUnits is the denominator every size-agnostic piece is
		// split by — the error would land as a wrong yield on a piece the blob never mentions.
		if err := entity.ValidateMarkerComposition(comp); err != nil {
			return MarkerYield{}, fmt.Errorf("stored marker layout composition: %w", err)
		}
		out.Composition = make(map[int]int, len(comp))
		for _, e := range comp {
			out.Composition[e.SizeId] = e.Quantity
			out.TotalUnits += e.Quantity
		}
	}

	// piece_id → the piece, so placements can be attributed. Duplicated ids are corruption: the
	// second one would silently shadow the first and every placement of the first would be counted
	// against the wrong contour.
	byID := make(map[int32]*pb_common.TechCardMarkerPiece, len(l.GetPieces()))
	keyOf := make(map[int32]MarkerPieceKey, len(l.GetPieces()))
	for _, p := range l.GetPieces() {
		if _, dup := byID[p.GetPieceId()]; dup {
			return MarkerYield{}, fmt.Errorf("stored marker layout lists piece_id %d twice", p.GetPieceId())
		}
		byID[p.GetPieceId()] = p
		lineKey := p.GetPieceLineKey()
		keyOf[p.GetPieceId()] = MarkerPieceKey{PieceLineKey: lineKey, SizeId: int(p.GetSizeId())}
		if lineKey == "" {
			out.UnattributedPieces++
			continue
		}
		if _, seen := out.PieceNames[lineKey]; !seen {
			out.PieceNames[lineKey] = p.GetName()
		}
	}

	placements := l.GetPlacements()
	if len(placements) == 0 {
		// No marker that can reach coverage has an empty layout. SaveMarker demands total_count >= 1
		// and refuses placed_count > total_count outright; a layout that placed NOTHING is a черновик
		// (0299), and a черновик is refused as a section's раскладка by requireLaySectionMarkers, so
		// it never gets parsed here. An empty one is therefore either a blob that was never a marker
		// or one that lost its placements, and answering «this marker cuts nothing» would put a
		// BLOCKER on the run for a parse failure.
		return MarkerYield{}, errors.New("stored marker layout carries no placements")
	}
	for i, pl := range placements {
		key, ok := keyOf[pl.GetPieceId()]
		if !ok {
			return MarkerYield{}, fmt.Errorf("stored marker layout: placement %d references unknown piece_id %d", i, pl.GetPieceId())
		}
		counts := out.Pieces[key]
		if pl.GetFlipped() {
			if version < markerLayoutSchemaFlip {
				// techcard.proto calls this an impossible payload in so many words: the field did not
				// exist before 3. Counting it anyway would report a chirality the writer never wrote;
				// dropping it would understate the piece. Neither is an answer.
				return MarkerYield{}, fmt.Errorf(
					"stored marker layout: placement %d is flipped in a blob declaring schema_version %d (flipped exists from %d)",
					i, version, markerLayoutSchemaFlip)
			}
			counts.Mirrored++
		} else {
			counts.AsDrawn++
		}
		out.Pieces[key] = counts
	}
	return out, nil
}

// MarkerPieceInstances is the answer to «сколько экземпляров этой детали кладёт ОДИН СЛОЙ этого
// маркера» — with the third answer built into the type.
type MarkerPieceInstances struct {
	Counts MarkerPieceCounts
	// Known is false on the ZERO VALUE, and that is the point: a caller who forgets to set it gets
	// «не могу сказать», which costs an UNKNOWN badge, rather than «ноль», which invents a shortage.
	Known bool
	// Caveats name the pieces whose per-size split did not come out whole (see PerLayerInstances).
	// They do not change the number; they explain why it is a floor.
	Caveats []string
}

// PerLayerInstances resolves ONE card cut-piece at ONE size against this marker: how many instances
// of it a single layer lays down, by chirality.
//
// It implements §6.2 step 1, and the proportional split there is not a heuristic — it is the formula
// the blob declares about itself (techcard.proto, TechCardMarkerPiece.quantity):
//
//	instances = quantity × (size_id > 0 ? composition[size_id].quantity : total_units)
//
// A piece with no size does not grade, so ONE set of it is cut for every garment of the whole
// состав; its instances therefore divide between the sizes exactly as the состав does. A blob may
// carry BOTH forms of the same card piece — graded contours per size plus a size-agnostic block —
// and both contributions are added.
//
// UNKNOWN (Known = false) is returned, never a zero, when:
//   - the blob cannot attribute its pieces at all (Attributable);
//   - the blob does not know its состав (CompositionKnown) — call WithSummaryComposition first.
//
// An honest ZERO is returned when the состав does not cut that size at all, and when the marker
// simply does not carry that piece. Those are facts about this marker, not gaps in it.
func (y MarkerYield) PerLayerInstances(pieceLineKey string, sizeID int) MarkerPieceInstances {
	if pieceLineKey == "" {
		// The empty key is the unattributed bucket, never a card piece. Answering it would hand back
		// someone else's instances.
		return MarkerPieceInstances{}
	}
	if !y.Attributable() || !y.CompositionKnown() {
		return MarkerPieceInstances{}
	}
	garments, cuts := y.Composition[sizeID]
	if !cuts {
		// This marker does not cut that size. Honest zero: the cloth was laid, this size was not on it.
		return MarkerPieceInstances{Known: true}
	}

	out := MarkerPieceInstances{Known: true}
	graded := y.Pieces[MarkerPieceKey{PieceLineKey: pieceLineKey, SizeId: sizeID}]
	out.Counts.AsDrawn += graded.AsDrawn
	out.Counts.Mirrored += graded.Mirrored

	agnostic := y.Pieces[MarkerPieceKey{PieceLineKey: pieceLineKey}]
	if agnostic.Total() > 0 {
		asDrawn, exactA := splitByComposition(agnostic.AsDrawn, garments, y.TotalUnits)
		mirrored, exactM := splitByComposition(agnostic.Mirrored, garments, y.TotalUnits)
		out.Counts.AsDrawn += asDrawn
		out.Counts.Mirrored += mirrored
		if !exactA || !exactM {
			// An unbalanced expansion: the blob laid a number of instances that does not divide
			// between the sizes it declares. Floor, and say which piece, because the difference is a
			// real panel somebody has to account for by hand.
			name := y.PieceNames[pieceLineKey]
			if name == "" {
				name = pieceLineKey
			}
			out.Caveats = append(out.Caveats, fmt.Sprintf(
				"piece %q: %d ungraded instances do not divide evenly between the composition's sizes (%d garments), floored",
				name, agnostic.Total(), y.TotalUnits))
		}
	}
	return out
}

// splitByComposition apportions `instances` of a size-agnostic piece to one size, and reports
// whether the division came out whole.
func splitByComposition(instances, garmentsOfSize, totalUnits int) (int, bool) {
	if instances <= 0 || totalUnits <= 0 {
		return 0, true
	}
	n := instances * garmentsOfSize
	return n / totalUnits, n%totalUnits == 0
}

// LayFaceMode is how the plies of a настил lie relative to one another (production_run_lay.mode,
// 0290). Declared here as a plain string type — the same closed dictionary the column's CHECK
// carries — so this file stays a pure function of its inputs and does not wait on the schema.
type LayFaceMode string

const (
	// LayFaceModeFaceUp — every ply the same way up. Each placement yields its own chirality.
	LayFaceModeFaceUp LayFaceMode = "face_up"
	// LayFaceModeFaceToFace — plies alternate face up and face down, so ONE placement yields half its
	// instances as drawn and half mirrored. This is what makes a half set of выкройки cut pairs.
	LayFaceModeFaceToFace LayFaceMode = "face_to_face"
)

// ValidLayFaceModes mirrors chk_prlay_mode (0290), spelling and case both.
var ValidLayFaceModes = map[LayFaceMode]bool{LayFaceModeFaceUp: true, LayFaceModeFaceToFace: true}

// ErrLayModeParity is an ODD ply count in face-to-face. The section contributes NOTHING and the cell
// takes a blocker: the last unpaired ply yields only one hand, and pretending it gave half a pair is
// exactly the arithmetic that produces 44 left fronts on paper and 22 garments in the cutting room.
var ErrLayModeParity = errors.New("face_to_face lay needs an even number of plies")

// LayerCutInstances turns per-layer instances into instances PHYSICALLY CUT by one section of a
// настил (§6.2 step 2). Errors are not advisory: on any of them the section's contribution is zero,
// which is why the zero MarkerPieceCounts rides back with them.
func LayerCutInstances(perLayer MarkerPieceCounts, mode LayFaceMode, plies int) (MarkerPieceCounts, error) {
	if !ValidLayFaceModes[mode] {
		return MarkerPieceCounts{}, fmt.Errorf("unknown lay mode %q (want %s|%s)", mode, LayFaceModeFaceUp, LayFaceModeFaceToFace)
	}
	if plies < 1 {
		return MarkerPieceCounts{}, fmt.Errorf("plies must be at least 1, got %d", plies)
	}
	if perLayer.AsDrawn < 0 || perLayer.Mirrored < 0 {
		return MarkerPieceCounts{}, fmt.Errorf("instance counts must not be negative, got %+v", perLayer)
	}
	switch mode {
	case LayFaceModeFaceUp:
		return MarkerPieceCounts{AsDrawn: perLayer.AsDrawn * plies, Mirrored: perLayer.Mirrored * plies}, nil
	case LayFaceModeFaceToFace:
		if plies%2 != 0 {
			return MarkerPieceCounts{}, ErrLayModeParity
		}
		half := perLayer.Total() * (plies / 2)
		return MarkerPieceCounts{AsDrawn: half, Mirrored: half}, nil
	}
	return MarkerPieceCounts{}, fmt.Errorf("unhandled lay mode %q", mode)
}

// PieceYield is how many GARMENTS the instances cut of one piece are good for.
type PieceYield struct {
	Garments int
	// Overcut is instances that cannot become garments through this piece — the mirror images of an
	// `identical` piece, which are not that piece at all. Reported so a настил that quietly wastes
	// half its cloth is visible; it never adds to Garments.
	Overcut int
	// Known is false on the ZERO VALUE. See MarkerPieceInstances.Known — same reason, same direction.
	Known bool
}

// PieceYieldGarments applies §6.2 step 3: КАК КРОИТСЯ (tech_card_piece.cut_symmetry, 0275) turns
// instances into garments.
//
//	identical — n congruent copies:  ⌊as_drawn / n⌋; mirrored instances are overcut, not this piece
//	mirrored  — n = 2k chiral pairs: ⌊min(as_drawn, mirrored) / k⌋
//	fold      — the outline is symmetric by construction, so chirality does not arise: ⌊total / n⌋
//	NULL      — НЕ РАЗМЕЧЕНО ⇒ UNKNOWN, and never `identical`
//
// The NULL case is the one 0275 exists for. entity.TechCardPiece says it in the same words: an
// unmarked mirrored piece cut from a half set of patterns yields 44 left fronts and no right ones,
// and the only thing standing between that and a signed-off run is a visible «не размечено».
//
// chiralityKnown is MarkerYield.ChiralityKnown, and it only ever matters for `mirrored`: below
// schema 3 a blob has no `flipped`, so a zero mirrored count is absence of evidence and reporting a
// shortage off it would fire on EVERY legacy marker at once. `identical` and `fold` do not consult
// it — one reads as-drawn, the other reads the total, and neither statement changes.
func PieceYieldGarments(cut MarkerPieceCounts, sym sql.NullString, piecesPerGarment int, chiralityKnown bool) PieceYield {
	if !sym.Valid {
		return PieceYield{}
	}
	if piecesPerGarment < 1 {
		// A piece that is cut zero times per garment is not a piece; refusing to divide by it is not
		// the same statement as «unlimited garments».
		return PieceYield{}
	}
	if cut.AsDrawn < 0 || cut.Mirrored < 0 {
		return PieceYield{}
	}
	switch entity.TechCardPieceCutSymmetry(sym.String) {
	case entity.PieceCutSymmetryIdentical:
		return PieceYield{Garments: cut.AsDrawn / piecesPerGarment, Overcut: cut.Mirrored, Known: true}
	case entity.PieceCutSymmetryMirrored:
		if !chiralityKnown {
			return PieceYield{}
		}
		if piecesPerGarment%2 != 0 {
			// chk_tcp_mirrored_needs_even_count (0275) guarantees this cannot be stored; an odd count
			// reaching here means the row bypassed the check, and there is no defined rounding rule.
			return PieceYield{}
		}
		pairs := piecesPerGarment / 2
		return PieceYield{Garments: min(cut.AsDrawn, cut.Mirrored) / pairs, Known: true}
	case entity.PieceCutSymmetryFold:
		return PieceYield{Garments: cut.Total() / piecesPerGarment, Known: true}
	}
	// A value the dictionary does not contain. Not «unknown symmetry, assume identical»: that is the
	// exact substitution 0275 was written to make impossible.
	return PieceYield{}
}

// CoverageStatus is the verdict on one (colourway, size) cell.
//
// The ZERO VALUE IS UNKNOWN on purpose: a cell nobody filled in has proved nothing, and the
// direction a forgotten assignment falls in should cost a grey badge, not a green one.
type CoverageStatus int

const (
	// CoverageStatusUnknown — покрытие доказать нечем. Not a shortage and not an approval.
	CoverageStatusUnknown CoverageStatus = iota
	// CoverageStatusOK — every required piece proved enough garments.
	CoverageStatusOK
	// CoverageStatusBlocker — a shortage was PROVEN, by pieces whose yield is known.
	CoverageStatusBlocker
)

func (s CoverageStatus) String() string {
	switch s {
	case CoverageStatusUnknown:
		return "UNKNOWN"
	case CoverageStatusOK:
		return "OK"
	case CoverageStatusBlocker:
		return "BLOCKER"
	}
	return fmt.Sprintf("CoverageStatus(%d)", int(s))
}

// coverageSeverity ranks the statuses by the §6.2 step-5 ladder: BLOCKER beats UNKNOWN beats OK.
//
// IT IS NOT THE CONSTANTS' NUMERIC ORDER, and it must not become it. The constants are ordered so
// that the zero value is UNKNOWN (see CoverageStatus); the ladder is ordered so that a proven
// shortage wins. Making one serve both would force the zero value to be OK — a silent approval on
// every uninitialised cell. TestCoverageStatusLadderIsNotNumericOrder pins the divergence.
func coverageSeverity(s CoverageStatus) int {
	switch s {
	case CoverageStatusOK:
		return 0
	case CoverageStatusUnknown:
		return 1
	case CoverageStatusBlocker:
		return 2
	}
	return 1
}

// WorstCoverageStatus folds several cells into one verdict by that ladder. It exists so nobody
// writes `max(a, b)` over the raw constants, which would answer OK for a cell that is UNKNOWN.
func WorstCoverageStatus(ss ...CoverageStatus) CoverageStatus {
	if len(ss) == 0 {
		return CoverageStatusUnknown
	}
	worst := ss[0]
	for _, s := range ss[1:] {
		if coverageSeverity(s) > coverageSeverity(worst) {
			worst = s
		}
	}
	return worst
}

// CoverageCell accumulates the per-piece verdicts of ONE cell (колорвей, размер) and applies §6.2
// steps 4 and 5.
//
// IT IS AN ACCUMULATOR AND NOT A STRUCT OF NUMBERS on purpose. The ladder — «доказанная нехватка
// бьёт UNKNOWN, UNKNOWN бьёт OK» — is one line of arithmetic that is wrong in a way nothing catches
// if it is written twice. Here it exists once, behind Status(), and the only way to feed the cell is
// Add, which takes a PieceYield whose zero value already means UNKNOWN. A caller cannot reach the
// minimum without going through a piece, and cannot skip a piece by handing in a zero.
type CoverageCell struct {
	plannedQty int
	known      []knownPieceYield
	unknown    int
}

type knownPieceYield struct {
	pieceLineKey string
	garments     int
}

// NewCoverageCell starts a cell for a run line's planned quantity.
func NewCoverageCell(plannedQty int) *CoverageCell {
	if plannedQty < 0 {
		plannedQty = 0
	}
	return &CoverageCell{plannedQty: plannedQty}
}

// Add records one REQUIRED piece's verdict. An unknown yield is counted as unknown — it never
// becomes a zero (which would fabricate a shortage) and never disappears (which would fabricate an
// approval).
func (c *CoverageCell) Add(pieceLineKey string, y PieceYield) {
	if !y.Known {
		c.unknown++
		return
	}
	g := y.Garments
	if g < 0 {
		g = 0
	}
	c.known = append(c.known, knownPieceYield{pieceLineKey: pieceLineKey, garments: g})
}

// PlannedQty is the run line's quantity this cell is judged against.
func (c *CoverageCell) PlannedQty() int { return c.plannedQty }

// UnknownPieceCount is how many required pieces could not answer. Non-zero is the number the client
// shows next to a grey badge; it is the reason the cell is not green.
func (c *CoverageCell) UnknownPieceCount() int { return c.unknown }

// KnownMin is the minimum garments over the pieces that COULD answer, and false when no piece could.
func (c *CoverageCell) KnownMin() (int, bool) {
	if len(c.known) == 0 {
		return 0, false
	}
	m := c.known[0].garments
	for _, k := range c.known[1:] {
		if k.garments < m {
			m = k.garments
		}
	}
	return m, true
}

// CoveredQty is the cell's provisioned quantity, capped at the planned one.
//
// WHEN Status() IS UNKNOWN THIS IS A LOWER BOUND, not a fact — «не меньше чем» — because the pieces
// that stayed silent could only pull it down. The client is required to render it as such; the
// number alone cannot say so, which is why UnknownPieceCount travels beside it.
func (c *CoverageCell) CoveredQty() int {
	m, ok := c.KnownMin()
	if !ok {
		return 0
	}
	if m > c.plannedQty {
		return c.plannedQty
	}
	if m < 0 {
		return 0
	}
	return m
}

// Status applies the ladder of §6.2 step 5, in this order and no other:
//
//	known_min < planned_qty  → BLOCKER  — the shortage is PROVEN; an unknown elsewhere does not soften it
//	any unknown piece        → UNKNOWN  — there is nothing to prove coverage with
//	otherwise                → OK
//
// A cell with no pieces at all is UNKNOWN, not OK: «нечего покрывать» is a statement about the card
// (an aux card, §6.3) and is made by not building a cell, not by building an empty one. «Нет ни
// одного настила» is likewise NOT this case — there every required piece is added with a zero cut
// count, yields zero garments, and the cell comes out BLOCKER on an honest zero.
func (c *CoverageCell) Status() CoverageStatus {
	m, ok := c.KnownMin()
	if ok && m < c.plannedQty {
		return CoverageStatusBlocker
	}
	if c.unknown > 0 || !ok {
		return CoverageStatusUnknown
	}
	return CoverageStatusOK
}

// BlockingPieceKeys names the pieces that produced the minimum — what §6.4 maps to
// blocking_bom_item_ids. Empty unless the cell is a BLOCKER: on an OK or UNKNOWN cell nothing is
// blocking, and handing back the argmin anyway invites a client to render it as a fault. Sorted, so
// the same cell reports the same list every time.
func (c *CoverageCell) BlockingPieceKeys() []string {
	if c.Status() != CoverageStatusBlocker {
		return nil
	}
	m, ok := c.KnownMin()
	if !ok {
		return nil
	}
	var out []string
	for _, k := range c.known {
		if k.garments == m {
			out = append(out, k.pieceLineKey)
		}
	}
	sort.Strings(out)
	return out
}
