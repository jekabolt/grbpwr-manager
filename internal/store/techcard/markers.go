package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Saved раскладки (markers, tech_card_marker, migration 0257): the measured fabric layout of a
// СОСТАВ of pattern pieces, self-contained geometry included. A marker is a MEASUREMENT — costing
// reads consumption per garment off it (used_length_cm / total_units) — not a structural reference,
// which is why its BOM link degrades to NULL instead of blocking BOM edits, and why nothing else
// FKs it.
//
// Ф2 (0273) replaced «one size × N комплектов» with a map size → garments: the row's size_id/sets
// went nullable and legacy, the состав lives in tech_card_marker_size, and total_units is the
// garment count costing divides by. See internal/entity/marker_composition.go for the rule.
//
// Writes are last-write-wins on purpose. Neither fitting_change_request nor
// tech_card_output_variant carries a lock_version; concurrency collapses inside the SERIALIZABLE
// write transaction. Marker writes must NOT bump tech_card.lock_version — saving a раскладка from
// the nesting modal would otherwise 409 the same operator's open card form.

// markerSummaryColumns is the explicit list every summary read uses. Explicit, not SELECT * —
// layout must never ride a summary query, and JSON columns read via * resurface the
// quoted-JSON-scalar bug (see UnquoteLegacyComposition).
const markerSummaryColumns = `
	m.id, m.tech_card_id, m.size_id, m.name, m.source, m.bom_item_id, m.colorway_id,
	b.line_key AS bom_line_key, b.name AS bom_item_name, b.unit AS bom_item_unit,
	m.fabric_width_cm, m.gap_cm, m.edge_margin_cm, m.selvedge_cm, m.allow_cross_grain, m.sets,
	m.total_units, m.used_length_cm, m.efficiency_pct, m.placed_count, m.total_count,
	m.seam_allowance_cm, m.contour_allowance_cm, m.contour_layer, m.grain_layer, m.allow_flip,
	m.is_norm, m.piece_set_fp,
	m.created_by, m.updated_by, m.created_at, m.updated_at`

// ListMarkerSummaries returns a card's saved раскладки without their layout blobs, newest first,
// each with its СОСТАВ attached. Runs on the caller's connection so the single-card read sees one
// snapshot.
//
// ORDER BY dropped m.size_id at the head (Ф2) — deliberately, not by accident. Grouping the list by
// size stopped meaning anything the moment a раскладка could cut several sizes at once, and left
// alone it would have kept sorting: with size_id NULL on every marker with a состав, MySQL puts
// those first, so the list would have re-ordered itself in production under a rule nobody chose.
// Newest first is the rule we choose.
//
// The card's CUT-PIECES arrive as a PARAMETER and not as a query of this function's own, for two
// reasons. The single-card read has already loaded them, so the Ф3.6 comparison («did the set of
// pieces change since this раскладка was taken») costs nothing here. And an argument the compiler
// demands cannot be forgotten: a caller that skipped it would get a list in which every раскладка
// silently reads «набор при съёмке не записан», which is indistinguishable from the truth.
func listMarkerSummaries(ctx context.Context, db dependency.DB, techCardID int,
	pieces []entity.TechCardPiece) ([]entity.TechCardMarkerSummary, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardMarkerSummary](ctx, db, `
		SELECT `+markerSummaryColumns+`
		FROM tech_card_marker m
		LEFT JOIN tech_card_bom_item b ON b.id = m.bom_item_id
		WHERE m.tech_card_id = :id
		ORDER BY m.updated_at DESC, m.id DESC`, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("can't list tech card markers: %w", err)
	}
	if err := attachMarkerComposition(ctx, db, rows); err != nil {
		return nil, err
	}
	cardFP := entity.PieceSetFingerprintNull(entity.PieceSetEntriesOf(pieces))
	peers := entity.NormPeersOf(rows)
	for i := range rows {
		rows[i].CardPieceSetFp = cardFP
		rows[i].NormConflict = entity.NormConflictReport(peers, entity.MarkerNormScope(rows[i]))
	}
	return rows, nil
}

// GetMarker returns one marker WITH its layout blob — the only read that carries it.
func (s *Store) GetMarker(ctx context.Context, id int) (*entity.TechCardMarker, error) {
	row, err := storeutil.QueryNamedOne[entity.TechCardMarker](ctx, s.DB, `
		SELECT `+markerSummaryColumns+`, m.layout
		FROM tech_card_marker m
		LEFT JOIN tech_card_bom_item b ON b.id = m.bom_item_id
		WHERE m.id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: marker %d", entity.ErrMarkerNotFound, id)
		}
		return nil, fmt.Errorf("load marker %d: %w", id, err)
	}
	one := []entity.TechCardMarkerSummary{row.TechCardMarkerSummary}
	if err := attachMarkerComposition(ctx, s.DB, one); err != nil {
		return nil, err
	}
	// The two card-wide facts this row cannot see for itself. They cost a query each HERE, unlike on
	// the card read, and both are paid rather than skipped: a piece_set_status that answered UNKNOWN
	// on this path while the list beside it answered CHANGED, and a norm_conflict that was empty here
	// and set there, would be worse than either answer — a field whose meaning depends on which RPC
	// you asked is a field nobody can act on.
	cardFP, err := cardPieceSetFingerprint(ctx, s.DB, one[0].TechCardId)
	if err != nil {
		return nil, err
	}
	one[0].CardPieceSetFp = cardFP
	peers, err := cardNormPeers(ctx, s.DB, one[0].TechCardId)
	if err != nil {
		return nil, err
	}
	one[0].NormConflict = entity.NormConflictReport(peers, entity.MarkerNormScope(one[0]))
	row.TechCardMarkerSummary = one[0]
	return &row, nil
}

// markerPieceSetQuery reads the card's cut-piece set for the Ф3.6 fingerprint — the two fields the
// fingerprint hashes and nothing else. Held as a var so a test can bind it without a database: sqlx
// reads EVERY ':' as a named parameter, a comment included, and a bind error would take both the
// marker save and the marker read down at request time.
//
// No ORDER BY on purpose: entity.PieceSetFingerprint sorts by line_key itself, precisely so the
// fingerprint is a function of the SET and not of the card read's display_order.
var markerPieceSetQuery = `
	SELECT line_key, pieces_per_garment
	FROM tech_card_piece
	WHERE tech_card_id = :card`

// cardPieceSetFingerprint fingerprints the card's cut-piece set as the given connection sees it.
// Called on the WRITE side inside the SERIALIZABLE save transaction and on the single-marker READ;
// the card read uses the pieces it already holds. All three go through
// entity.PieceSetFingerprintNull — one function, every side, which is the invariant the whole
// comparison rests on.
func cardPieceSetFingerprint(ctx context.Context, db dependency.DB, techCardID int) (sql.NullString, error) {
	rows, err := storeutil.QueryListNamed[entity.PieceSetEntry](ctx, db, markerPieceSetQuery,
		map[string]any{"card": techCardID})
	if err != nil {
		return sql.NullString{}, fmt.Errorf("load cut-pieces of tech card %d: %w", techCardID, err)
	}
	return entity.PieceSetFingerprintNull(rows), nil
}

// markerNormPeersQuery reads every раскладка of a card that carries the norm flag. Narrow by
// idx_tcm_card_norm (tech_card_id, is_norm), and held as a var for the ':' reason above.
var markerNormPeersQuery = `
	SELECT id, name, bom_item_id, updated_at
	FROM tech_card_marker
	WHERE tech_card_id = :card AND is_norm = TRUE`

// cardNormPeers loads the card's designated norms so a single-marker read can tell whether its own
// cloth carries more than one. The winner is picked in Go by entity.SelectNorm, NOT by an ORDER BY
// here — the tiebreak has to be the same expression on every path, and one written in SQL on one
// path and in Go on another is two tiebreaks waiting to disagree.
func cardNormPeers(ctx context.Context, db dependency.DB, techCardID int) ([]entity.NormPeer, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Id        int           `db:"id"`
		Name      string        `db:"name"`
		BomItemId sql.NullInt64 `db:"bom_item_id"`
		UpdatedAt sql.NullTime  `db:"updated_at"`
	}](ctx, db, markerNormPeersQuery, map[string]any{"card": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load norm markers of tech card %d: %w", techCardID, err)
	}
	out := make([]entity.NormPeer, 0, len(rows))
	for _, r := range rows {
		out = append(out, entity.NormPeer{
			Id:   r.Id,
			Name: r.Name,
			Scope: entity.NormScope{
				BomItemId: r.BomItemId.Int64,
				Bound:     r.BomItemId.Valid,
			},
			UpdatedAt: r.UpdatedAt.Time,
		})
	}
	return out, nil
}

// markerCompositionQuery reads the СОСТАВ of a set of markers in ONE round trip. Held as a var so a
// test can bind it without a database: sqlx reads EVERY ':' as a named parameter — including one
// inside a `--` comment — and a bind error would take the whole card read down at request time.
//
// ORDER BY marker_id, size_id makes the emitted состав a stable function of the data rather than of
// InnoDB's mood: the list row shows it verbatim, and a summary that reshuffles between two reads of
// an unchanged marker reads as the data having changed.
var markerCompositionQuery = `
	SELECT marker_id, size_id, quantity
	FROM tech_card_marker_size
	WHERE marker_id IN (:ids)
	ORDER BY marker_id, size_id`

// attachMarkerComposition fills in Composition on a batch of summaries. ONE query for the whole
// card, not one per marker — the card read already carries a dozen child loads and an N+1 here would
// scale with a number the operator controls.
//
// A marker with no child rows is left EMPTY on purpose and is not an error: it is the deploy-overlap
// row (0273), and entity.CompositionOrLegacy turns it back into a состав из одного размера from the
// legacy columns. Inventing something here would put that fallback in two places.
func attachMarkerComposition(ctx context.Context, db dependency.DB, rows []entity.TechCardMarkerSummary) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]int, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].Id)
	}
	children, err := storeutil.QueryListNamed[struct {
		MarkerId int `db:"marker_id"`
		SizeId   int `db:"size_id"`
		Quantity int `db:"quantity"`
	}](ctx, db, markerCompositionQuery, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("load marker composition: %w", err)
	}
	byMarker := make(map[int][]entity.MarkerCompositionEntry, len(rows))
	for _, c := range children {
		byMarker[c.MarkerId] = append(byMarker[c.MarkerId],
			entity.MarkerCompositionEntry{SizeId: c.SizeId, Quantity: c.Quantity})
	}
	for i := range rows {
		rows[i].Composition = byMarker[rows[i].Id]
	}
	return nil
}

// SaveMarker creates (id == 0) or fully replaces (id > 0) one saved раскладка and returns its id.
// The layout blob has no partial update — Ф5's manual adjustment re-saves the whole marker with
// source='manual'. Validation of the payload's FORM lives in dto; everything checked here is a
// fact only the database can witness: the card's approval state, the membership of every size of the
// СОСТАВ in the card's range, the BOM line's identity, the name's uniqueness on the card.
func (s *Store) SaveMarker(ctx context.Context, techCardID, id int, ins entity.TechCardMarkerInsert, username string) (int, error) {
	if id < 0 {
		return 0, entity.NewFieldViolation("id", "must_not_be_negative", "", "leave it 0 to save a new marker")
	}
	// An incomplete OR overfull layout is refused before the transaction even opens: only
	// placed == total is a consumption norm (placed > total would store a "complete" marker
	// against a wrong denominator).
	if ins.PlacedCount != ins.TotalCount {
		return 0, fmt.Errorf("%w: %d of %d pieces placed", entity.ErrMarkerIncomplete, ins.PlacedCount, ins.TotalCount)
	}
	var savedID int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// Content write to card-owned data: a released card refuses, inside the tx like every
		// sibling guard so a concurrent release cannot slip past the SERIALIZABLE read.
		if err := storeutil.RequireMutableTechCard(ctx, db, techCardID); err != nil {
			return err
		}
		// Ownership FIRST for an addressed id: a marker of another card must read as gone
		// before any validation detail (bom keys, sizes) leaks a differential answer. Resolved
		// with a SELECT, not RowsAffected on the UPDATE below — the driver counts rows CHANGED
		// (no clientFoundRows in the DSN), and that UPDATE has no guaranteed-changing column
		// (the lock_version bump was deliberately dropped), so a byte-identical re-save would
		// report 0 rows and read as a phantom 404.
		//
		// The stored bom line and colourway come back with it, because an UNCHANGED value must not
		// be re-validated. Both guards below ask whether a target is still ELIGIBLE — a roll-goods
		// section, a live colourway — and eligibility can lapse after the fact: archiving a
		// colourway is a first-class card action, and a BOM line can be reclassified from fabric to
		// trim. Re-checking an unchanged value would then make the marker permanently un-saveable:
		// adjusting one placement would fail on an attribution the operator never touched and has
		// no control to clear. The guards exist to stop NEW bad attributions, not to retro-invalidate
		// stored measurements.
		//
		// The stored LAYOUT rides along for the same journey and one more reason: the two facts that
		// may grant the directional-cloth exemption (Ф1.6) both live on the row — its policy
		// generation, and the geometry it already carries. Read here, INSIDE the transaction that
		// validates against them, neither can be forged by the payload nor changed underneath the
		// decision by a concurrent save. Fetching them in the API layer instead would cost a second
		// round trip and put a TOCTOU between «what the row contains» and «what the row is judged
		// against» — on the one decision that lets forbidden geometry through.
		//
		// The blob itself is not parsed here: the bytes are handed to the distiller the API layer
		// injected (ins.DistilStoredLayout). The storage layer holding geometry it cannot read is the
		// arrangement 0257 and 0268 both rest on.
		var stored struct {
			Id            int64          `db:"id"`
			BomItemId     sql.NullInt64  `db:"bom_item_id"`
			BomLineKey    sql.NullString `db:"bom_line_key"`
			ColorwayId    sql.NullInt64  `db:"colorway_id"`
			SchemaVersion int            `db:"layout_schema_version"`
			Layout        string         `db:"layout"`
		}
		if id > 0 {
			row, err := storeutil.QueryNamedOne[struct {
				Id            int64          `db:"id"`
				BomItemId     sql.NullInt64  `db:"bom_item_id"`
				BomLineKey    sql.NullString `db:"bom_line_key"`
				ColorwayId    sql.NullInt64  `db:"colorway_id"`
				SchemaVersion int            `db:"layout_schema_version"`
				Layout        string         `db:"layout"`
			}](ctx, db, `SELECT m.id, m.bom_item_id, b.line_key AS bom_line_key, m.colorway_id,
					m.layout_schema_version, m.layout
				FROM tech_card_marker m
				LEFT JOIN tech_card_bom_item b ON b.id = m.bom_item_id
				WHERE m.id = :id AND m.tech_card_id = :tech_card_id`,
				map[string]any{"id": id, "tech_card_id": techCardID})
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: marker %d is not a раскладка of tech card %d",
						entity.ErrMarkerNotFound, id, techCardID)
				}
				return fmt.Errorf("resolve marker %d: %w", id, err)
			}
			stored = row
		}
		// Every size of the СОСТАВ must be in the card's range AT SAVE TIME. Like pattern rows, a
		// marker may outlive its sizes leaving the range later — it stays a valid measurement — but
		// minting a new one against a foreign size is always a client bug. Asked of the NORMALISED
		// состав, so the legacy path checks exactly the one size it always did.
		if err := requireCardSizes(ctx, db, techCardID, ins.Composition); err != nil {
			return err
		}
		bomItemID := sql.NullInt64{}
		// ONE definition of «the marker stays on the cloth it was already attributed to», read twice:
		// here, to keep a stored line whose section has since changed, and by the direction rule,
		// which may forgive stored geometry only on the fabric it was measured against. Two copies
		// would drift, and the day they drifted the pass would transfer across cloth in silence.
		key := strings.TrimSpace(ins.BomLineKey)
		bindingUnchanged := key != "" && stored.BomLineKey.Valid && strings.EqualFold(stored.BomLineKey.String, key)
		if bindingUnchanged {
			// Unchanged binding: keep the stored line even if its section is no longer roll goods.
			bomItemID = stored.BomItemId
		} else if key != "" {
			// Roll goods only, the same four families a pattern sheet and a cut-piece alias bind to.
			// A marker MEASURES A LENGTH OF CLOTH: bound to a thread or hardware line it would be a
			// consumption norm for something that is counted, not laid out. The RPC used to accept
			// any line of the card and only the UI kept it honest — which is not a guarantee, it is
			// a habit.
			row, err := storeutil.QueryNamedOne[struct {
				Id int64 `db:"id"`
			}](ctx, db, `SELECT id FROM tech_card_bom_item
				WHERE tech_card_id = :card AND line_key = :key AND `+rollGoodsSectionIn,
				rollGoodsSectionArgs(map[string]any{"card": techCardID, "key": key}))
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return entity.NewFieldViolation("bom_line_key", "not_found", key,
						"pick a fabric, lining, interlining or insulation BOM line of this card, or leave the marker unlinked")
				}
				return fmt.Errorf("resolve bom line %q of tech card %d: %w", key, techCardID, err)
			}
			bomItemID = sql.NullInt64{Int64: row.Id, Valid: true}
		}
		// The verdict answers two things the generation below depends on: was this geometry judged
		// against cloth at all, and did it spend the row's pre-Ф1 pass. Its zero value — «not judged,
		// not exempted» — is what an unlinked marker leaves behind, and it correctly holds the
		// generation where it is rather than claiming a judgement nobody made.
		var verdict entity.MarkerDirectionVerdict
		// НАПРАВЛЕНИЕ ТКАНИ decides whether this layout may exist at all (Ф1.5/Ф1.6), and only the
		// database can answer: the direction sits on the BOM line (0073) and the scope the marker
		// falls into may be SEVERAL lines (0267). The rule itself is one unit-tested function in
		// entity — here we only hand it the card's cloth lines.
		//
		// Keyed off the PAYLOAD's line, so an unlinked marker is skipped entirely: no bom_line_key
		// means no cloth to ask about, and that must stay saveable — it was legal before Ф1 and the
		// geometry is just as valid without an attribution.
		if key != "" {
			lines, err := fabricDirectionLines(ctx, db, techCardID)
			if err != nil {
				return err
			}
			// Everything the exemption may rest on comes off the ROW, in this transaction: its
			// generation is 0 for a create (no history, no pass), its binding says whether the cloth
			// is the same one the geometry was measured against, and its blob says what that geometry
			// actually is.
			v, err := entity.ValidateMarkerFabricDirection(key, lines, ins.LayoutFacts,
				entity.StoredMarkerRow{
					Generation:       stored.SchemaVersion,
					BindingUnchanged: bindingUnchanged,
					Facts:            storedMarkerFacts(ctx, id, stored.Layout, ins.DistilStoredLayout),
				},
				fabricLineNamer(ctx, db, techCardID))
			if err != nil {
				return err
			}
			verdict = v
		}
		// The colourway a раскладка is measured FOR (0264). It must be a colourway OF THIS CARD:
		// a colourway is a product row whose style_id is the card (0151 merged the domains), and
		// the FK alone would accept any product in the catalogue — attributing a layout to another
		// style's colourway, which then offers it in that style's recipe at a width from here.
		// ARCHIVED(4) is excluded to match the card read, which drops archived colourways entirely
		// — a marker pointing at one would be attributed to something the operator cannot see.
		colorwayID := sql.NullInt64{}
		if ins.ColorwayId > 0 && stored.ColorwayId.Valid && stored.ColorwayId.Int64 == int64(ins.ColorwayId) {
			// Unchanged attribution: keep it even if the colourway has since been archived.
			colorwayID = stored.ColorwayId
		} else if ins.ColorwayId > 0 {
			n, err := storeutil.QueryCountNamed(ctx, db,
				`SELECT COUNT(*) FROM product WHERE id = :cw AND style_id = :card AND lifecycle_status <> 4`,
				map[string]any{"cw": ins.ColorwayId, "card": techCardID})
			if err != nil {
				return fmt.Errorf("check colorway %d on tech card %d: %w", ins.ColorwayId, techCardID, err)
			}
			if n == 0 {
				return entity.NewFieldViolation("colorway_id", "not_on_card",
					fmt.Sprintf("colorway %d", ins.ColorwayId),
					"the marker's colourway must be a live colourway of this tech card, or leave it unset")
			}
			colorwayID = sql.NullInt64{Int64: int64(ins.ColorwayId), Valid: true}
		}
		// ПОКОЛЕНИЕ ПОЛИТИКИ, не копия пэйлоада. The column answers «what is the newest policy this
		// row's geometry has been judged under», so the server writes its own constant — copying the
		// payload's schema_version made the «unforgeable stored fact» client-written one request
		// later: create a compliant marker declaring 1 (accepted, nothing to refuse), then update it
		// with a 180° and the stored 1 bought the exemption.
		//
		// It moves forward only when this save actually held the geometry against the policy AND did
		// not spend a pass. The two exclusions are different facts and both matter:
		//
		//   • a save that SPENT the exemption leaves the column alone, or the pass would be one-shot
		//     — the row stamped by the very save it was granted for, and the second rename of a
		//     legacy раскладка refused forever;
		//   • a save that judged NOTHING (an unlinked or dangling marker) leaves it alone too,
		//     because stamping it would claim a judgement no cloth ever made. That case used to
		//     ratchet, so unlinking a legacy marker and re-linking it silently cost it its pass; now
		//     that the exemption forgives only geometry already on file, the column no longer has to
		//     carry that defence and can mean exactly what it says.
		//
		// Everything else ratchets, so a legacy row whose geometry is compliant — the overwhelming
		// majority — is judged under the current policy the first time anybody touches it and never
		// gets a pass again.
		generation := stored.SchemaVersion
		if generation <= 0 || (verdict.Judged && !verdict.Exempted) {
			generation = entity.MarkerLayoutSchemaWithFlip
		}
		// ОТПЕЧАТОК НАБОРА ДЕТАЛЕЙ (Ф3.6), computed HERE and never taken from the payload. The client
		// does not hold the stored set, so a client-sent fingerprint would be both forgeable and stale
		// against any concurrent card edit; taken here, inside the SERIALIZABLE transaction, the set the
		// fingerprint saw IS the set this transaction committed against. NULL when unfingerprintable —
		// readers turn that into UNKNOWN, never into «changed».
		pieceSetFp, err := cardPieceSetFingerprint(ctx, db, techCardID)
		if err != nil {
			return err
		}
		params := map[string]any{
			"id":                id,
			"tech_card_id":      techCardID,
			"size_id":           ins.SizeId,
			"bom_item_id":       bomItemID,
			"name":              ins.Name,
			"source":            string(ins.Source),
			"fabric_width_cm":   ins.FabricWidthCm,
			"gap_cm":            ins.GapCm,
			"edge_margin_cm":    ins.EdgeMarginCm,
			"selvedge_cm":       ins.SelvedgeCm,
			"allow_cross_grain": ins.AllowCrossGrain,
			"sets":              ins.Sets,
			// Derived HERE, from the very slice the child rows are written from a few lines below, so
			// the divisor of money and its own детали are written from one value inside one
			// transaction and cannot come apart.
			"total_units":    entity.TotalUnitsOf(ins.Composition),
			"used_length_cm": ins.UsedLengthCm,
			"efficiency_pct": ins.EfficiencyPct,
			"placed_count":   ins.PlacedCount,
			"total_count":    ins.TotalCount,
			"layout":         ins.Layout,
			"schema_version": generation,
			"colorway_id":    colorwayID,
			// УСЛОВИЯ СЪЁМКИ (Ф3), stored exactly as sent — INVALID stays NULL, which reads as «not
			// recorded». A stale bundle sends none of them and its row honestly becomes «старая норма».
			//
			// A RE-SAVE THEREFORE OVERWRITES THEM WITH WHATEVER THE PAYLOAD CARRIES, including nothing.
			// That is the same last-write-wins contract the rest of this row has, and the client owes
			// the counterpart: the manual-adjustment path must round-trip the conditions it read, or
			// nudging one placement would strip a fresh раскладка back to «старая норма».
			//
			// PRESERVING THEM ON ABSENCE WAS CONSIDERED AND IS WRONG. Every save carries a COMPLETE new
			// geometry (pieces and placements are both required), so a payload without conditions is not
			// «the same раскладка, described more briefly» — it is a new measurement by a client that
			// cannot state what it measured. Carrying the old conditions onto it would attach an
			// allowance and a contour layer measured for OTHER geometry, i.e. label a раскладка with a
			// number nobody took for it. Degrading to «старая норма» is honest and recoverable by
			// re-taking the раскладка; a false label is neither.
			"seam_allowance_cm":    ins.SeamAllowanceCm,
			"contour_allowance_cm": ins.ContourAllowanceCm,
			"contour_layer":        ins.ContourLayer,
			"grain_layer":          ins.GrainLayer,
			"allow_flip":           ins.AllowFlip,
			"piece_set_fp":         pieceSetFp,
			"username":             username,
		}
		// is_norm is DELIBERATELY ABSENT from this map and from both statements below. Designation is
		// SetMarkerNorm's alone: re-saving geometry must neither seize the norm nor lose it, and a
		// stale bundle must not be able to clear it by not knowing the field exists.
		//
		// РОВНО ОДНО ИСКЛЮЧЕНИЕ, и оно ниже в UPDATE: ПЕРЕПРИВЯЗКА К ДРУГОЙ ТКАНИ. Эксклюзивность
		// нормы скоупится парой (карточка, bom_item_id), то есть «одна норма на ткань», — и
		// сохранение, сменившее ткань, переносит признак В ЧУЖОЙ СКОУП. Обе развязки назначал бы
		// не человек, а случайность: если у целевой ткани норма уже есть, их станет две, причём
		// свежий updated_at отдаёт победу переехавшему и вытесняет действующую норму; если нормы
		// там нет, ткань молча её приобретает, а исходная — молча теряет. Поэтому переезд СНИМАЕТ
		// признак: назначить норму заново — одно осознанное действие, а вот отобрать её у другой
		// ткани молча нельзя ничем. Сравнение через <=> (NULL-safe): bom_item_id обнуляем.
		//
		// И ПОТОМУ ЖЕ ЭТО ПРИСВАИВАНИЕ СТОИТ ПЕРВЫМ В SET. MySQL вычисляет список слева направо и
		// видит УЖЕ ОБНОВЛЁННЫЕ колонки (док: «SET col1 = 1, col2 = col1» кладёт в col2 единицу),
		// так что после `bom_item_id = :bom_item_id` сравнение читало бы новое значение с самим
		// собой — всегда истина, признак не снимался бы никогда, и защита была бы декоративной.
		if id > 0 {
			// The addressed row must already be a marker of THIS card — a foreign id is reported as
			// gone, not silently adopted. Ownership is resolved with a SELECT, like the
			// output-variant upsert, NOT via RowsAffected on the UPDATE: the driver counts rows
			// CHANGED (no clientFoundRows in the DSN), and this UPDATE has no guaranteed-changing
			// column (the lock_version bump was deliberately dropped) — a byte-identical re-save
			// would report 0 rows and read as a phantom 404.
			if _, err := storeutil.QueryNamedOne[struct {
				Id int64 `db:"id"`
			}](ctx, db, `SELECT id FROM tech_card_marker WHERE id = :id AND tech_card_id = :tech_card_id`,
				map[string]any{"id": id, "tech_card_id": techCardID}); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: marker %d is not a раскладка of tech card %d",
						entity.ErrMarkerNotFound, id, techCardID)
				}
				return fmt.Errorf("resolve marker %d: %w", id, err)
			}
			if _, err := storeutil.ExecNamedRows(ctx, db, `
				UPDATE tech_card_marker
				SET is_norm = IF(:bom_item_id <=> bom_item_id, is_norm, FALSE),
				    size_id = :size_id, bom_item_id = :bom_item_id, colorway_id = :colorway_id,
				    name = :name, source = :source,
				    fabric_width_cm = :fabric_width_cm, gap_cm = :gap_cm,
				    edge_margin_cm = :edge_margin_cm, selvedge_cm = :selvedge_cm,
				    allow_cross_grain = :allow_cross_grain,
				    sets = :sets, total_units = :total_units,
				    used_length_cm = :used_length_cm, efficiency_pct = :efficiency_pct,
				    placed_count = :placed_count, total_count = :total_count, layout = :layout,
				    layout_schema_version = :schema_version,
				    seam_allowance_cm = :seam_allowance_cm,
				    contour_allowance_cm = :contour_allowance_cm,
				    contour_layer = :contour_layer, grain_layer = :grain_layer,
				    allow_flip = :allow_flip, piece_set_fp = :piece_set_fp,
				    updated_by = :username
				WHERE id = :id AND tech_card_id = :tech_card_id`, params); err != nil {
				return fmt.Errorf("update marker %d: %w", id, err)
			}
			if err := replaceMarkerComposition(ctx, db, id, ins.Composition); err != nil {
				return err
			}
			savedID = id
			return nil
		}
		// A create is judged under today's policy by definition — there is no history to protect, and
		// stamping anything older would let a marker authored today claim a pass tomorrow.
		params["schema_version"] = entity.MarkerLayoutSchemaWithFlip
		newID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_marker
				(tech_card_id, size_id, bom_item_id, colorway_id, name, source, fabric_width_cm, gap_cm,
				 edge_margin_cm, selvedge_cm, allow_cross_grain, sets, total_units, used_length_cm,
				 efficiency_pct, placed_count, total_count, layout, layout_schema_version,
				 seam_allowance_cm, contour_allowance_cm, contour_layer, grain_layer, allow_flip,
				 piece_set_fp, created_by, updated_by)
			VALUES (:tech_card_id, :size_id, :bom_item_id, :colorway_id, :name, :source, :fabric_width_cm, :gap_cm,
				 :edge_margin_cm, :selvedge_cm, :allow_cross_grain, :sets, :total_units, :used_length_cm,
				 :efficiency_pct, :placed_count, :total_count, :layout, :schema_version,
				 :seam_allowance_cm, :contour_allowance_cm, :contour_layer, :grain_layer, :allow_flip,
				 :piece_set_fp, :username, :username)`, params)
		if err != nil {
			return fmt.Errorf("create marker on tech card %d: %w", techCardID, err)
		}
		if err := replaceMarkerComposition(ctx, db, newID, ins.Composition); err != nil {
			return err
		}
		savedID = newID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return savedID, nil
}

// fabricDirectionLinesQuery reads the card's WHOLE BOM, ordered exactly as the card read orders it
// (display_order, id — see the bom load in materials.go). Both halves of that sentence are load
// bearing:
//
//   - the whole BOM, because a refusal is field-tagged `bom_items[i].fabric_direction` and i must be
//     the row's position in the array the CLIENT holds. An index taken over roll goods alone would
//     land on whatever sits at that position in the full list — a thread, a button — and pin the
//     error on the wrong control. Roll goods are then filtered in Go, through the same
//     rollGoodsSections map the SQL fragment is built from, so the two cannot mean different things;
//   - the ORDER BY, because the refusal lists rows and their order must be stable. Without it MySQL
//     may hand back the same state in a different order on a retry, and two identical saves would
//     name two different rows.
//
// The NAME is deliberately read RAW here — bi.name, which is empty on a catalogue-linked line — and
// resolved through `material` only when a refusal is actually being written (fabricLineNamer below).
// The join belongs off this path: this query runs inside a SERIALIZABLE transaction, where InnoDB
// promotes a plain SELECT to FOR SHARE, so joining the catalogue on every marker save would let a
// concurrent material edit block or deadlock an unrelated раскладка — reported as a 500, which is
// neither true nor retryable. Names are prose for a refusal; prose can afford a second query.
//
// Held as a var so a test can bind it without a database: sqlx reads EVERY ':' as a named parameter,
// and a bind error would take the whole save path down at request time.
var fabricDirectionLinesQuery = `
	SELECT COALESCE(line_key, '') AS line_key, COALESCE(purpose, '') AS purpose,
	       COALESCE(name, '') AS name, COALESCE(fabric_direction, '') AS fabric_direction,
	       is_sample, section
	FROM tech_card_bom_item
	WHERE tech_card_id = :id
	ORDER BY display_order, id`

// fabricLineNamer resolves the display names of catalogue-linked lines, and is called ONLY while a
// refusal is being built — i.e. on a transaction already destined to roll back, so the share locks it
// takes on `material` cost nothing anyone is waiting on. Names are resolved exactly as the card read
// resolves them, so the refusal speaks the vocabulary of the screen it sends the operator to.
func fabricLineNamer(ctx context.Context, db dependency.DB, techCardID int) entity.FabricLineNamer {
	return func(lineKeys []string) map[string]string {
		if len(lineKeys) == 0 {
			return nil
		}
		rows, err := storeutil.QueryListNamed[struct {
			LineKey string `db:"line_key"`
			Name    string `db:"name"`
		}](ctx, db, `SELECT COALESCE(bi.line_key, '') AS line_key, COALESCE(m.name, '') AS name
			FROM tech_card_bom_item bi
			JOIN material m ON m.id = bi.material_id
			WHERE bi.tech_card_id = :id AND bi.line_key IN (:keys)`,
			map[string]any{"id": techCardID, "keys": lineKeys})
		if err != nil {
			// A refusal that cannot name the row is still a refusal; it falls back to the line_key.
			slog.Default().ErrorContext(ctx, "can't resolve catalogue names for a marker refusal",
				slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
			return nil
		}
		out := make(map[string]string, len(rows))
		for _, r := range rows {
			out[r.LineKey] = r.Name
		}
		return out
	}
}

// fabricDirectionLines loads the card's cloth lines with both halves of the binding scope, their
// направление and their position in the card's BOM — everything
// entity.ValidateMarkerFabricDirection needs and nothing else.
//
// Only the four roll-goods families survive the filter: a thread or a button has no direction to
// have, and letting one through would make a nonsense row able to block a раскладка. A line that has
// since left roll goods is therefore absent, which is exactly right — the marker whose binding it
// still is resolves to a dangling scope and stays saveable, as it was before Ф1.
func fabricDirectionLines(ctx context.Context, db dependency.DB, techCardID int) ([]entity.FabricDirectionLine, error) {
	rows, err := storeutil.QueryListNamed[struct {
		LineKey   string `db:"line_key"`
		Purpose   string `db:"purpose"`
		Name      string `db:"name"`
		Direction string `db:"fabric_direction"`
		IsSample  bool   `db:"is_sample"`
		Section   string `db:"section"`
	}](ctx, db, fabricDirectionLinesQuery, map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load bom lines of tech card %d: %w", techCardID, err)
	}
	out := make([]entity.FabricDirectionLine, 0, len(rows))
	for i, r := range rows {
		if !rollGoodsSections[r.Section] {
			continue
		}
		out = append(out, entity.FabricDirectionLine{
			Index: i, LineKey: r.LineKey, Purpose: r.Purpose, Name: r.Name,
			IsSample: r.IsSample, Direction: r.Direction,
		})
	}
	return out, nil
}

// requireCardSizes verifies every size of the СОСТАВ belongs to the card's current range, with a
// readable refusal (the FK alone would let ANY dictionary size in — membership is a card fact, not a
// dictionary one).
//
// ONE query and ONE refusal listing ALL the offending sizes, not the first one: with a состав the
// operator's fix is a table of rows, and reporting them one per round trip would make a four-row
// mistake four saves. The refusal is field-tagged `composition` even on the legacy path, because
// that is the only field a client of the current contract holds — a stale bundle sending size_id
// still gets prose naming the size.
func requireCardSizes(ctx context.Context, db dependency.DB, techCardID int, composition []entity.MarkerCompositionEntry) error {
	if len(composition) == 0 {
		// Unreachable through the API layer (dto refuses an empty состав before the transaction
		// opens), and it must not degrade into "nothing to check": an empty состав means total_units
		// = 0, i.e. a divisor of zero for every costing read downstream.
		return entity.NewFieldViolation("composition", entity.ReasonCompositionMissing, "",
			"the раскладка must say how many garments of which sizes it cuts")
	}
	ids := make([]int, 0, len(composition))
	for _, c := range composition {
		ids = append(ids, c.SizeId)
	}
	rows, err := storeutil.QueryListNamed[struct {
		SizeId int `db:"size_id"`
	}](ctx, db, cardSizeMembershipQuery, map[string]any{"card": techCardID, "sizes": ids})
	if err != nil {
		return fmt.Errorf("check composition sizes on tech card %d: %w", techCardID, err)
	}
	onCard := make(map[int]bool, len(rows))
	for _, r := range rows {
		onCard[r.SizeId] = true
	}
	var missing []string
	for _, id := range ids {
		if !onCard[id] {
			missing = append(missing, fmt.Sprintf("%d", id))
		}
	}
	if len(missing) > 0 {
		return entity.NewFieldViolation("composition", entity.ReasonCompositionNotOnCard,
			strings.Join(missing, ", "),
			fmt.Sprintf("the раскладка cuts size(s) %s, which are not in this card's размерный ряд — "+
				"add them to the card or drop them from the состав", strings.Join(missing, ", ")))
	}
	return nil
}

// cardSizeMembershipQuery asks, in one round trip, which of a состав's sizes are actually in the
// card's ряд — the difference is what the refusal names.
var cardSizeMembershipQuery = `
	SELECT size_id FROM tech_card_size WHERE tech_card_id = :card AND size_id IN (:sizes)`

// markerCompositionDeleteQuery / markerCompositionInsertQuery are held as vars for the same reason
// the direction query is: a stray ':' anywhere in a named query — a comment included — fails at bind
// time, and the only thing that would catch it otherwise is a MySQL-backed test.
var (
	markerCompositionDeleteQuery = `DELETE FROM tech_card_marker_size WHERE marker_id = :marker_id`
	markerCompositionInsertQuery = `
		INSERT INTO tech_card_marker_size (marker_id, size_id, quantity)
		VALUES (:marker_id, :size_id, :quantity)`
)

// replaceMarkerComposition rewrites a marker's СОСТАВ wholesale — the full-replace idiom this
// repository uses for every owned child set (BOM, sizes, variants). It runs INSIDE the marker's
// SERIALIZABLE transaction, immediately after the row, so total_units and the child rows are written
// atomically from one ins.Composition and cannot come apart: the scalar is the divisor of money, and
// a divisor that disagrees with its own children would be wrong without being visibly wrong.
func replaceMarkerComposition(ctx context.Context, db dependency.DB, markerID int,
	composition []entity.MarkerCompositionEntry) error {
	if _, err := storeutil.ExecNamedRows(ctx, db, markerCompositionDeleteQuery,
		map[string]any{"marker_id": markerID}); err != nil {
		return fmt.Errorf("clear composition of marker %d: %w", markerID, err)
	}
	for _, c := range composition {
		if _, err := storeutil.ExecNamedRows(ctx, db, markerCompositionInsertQuery, map[string]any{
			"marker_id": markerID, "size_id": c.SizeId, "quantity": c.Quantity,
		}); err != nil {
			return fmt.Errorf("write composition size %d of marker %d: %w", c.SizeId, markerID, err)
		}
	}
	return nil
}

// markerNormScopeSiblingsQuery and markerNormClearScopeQuery share ONE scope predicate, written once
// and reused, because the day the two drifted the read would name a previous norm the write did not
// clear. `bom_item_id IS NULL AND :bom IS NULL` is the «no cloth» scope written out longhand rather
// than folded through IFNULL(...,0): the column's NULL is meaningful, and a sentinel that happens to
// be unreachable today is a sentinel a future migration can reach.
//
// Held as vars for the ':' reason stated above the composition queries.
const markerNormScopePredicate = `
	tech_card_id = :card AND is_norm = TRUE
	AND ((bom_item_id IS NULL AND :bom IS NULL) OR bom_item_id = :bom)`

var (
	markerNormScopeSiblingsQuery = `
		SELECT id, name, updated_at FROM tech_card_marker
		WHERE ` + markerNormScopePredicate + ` AND id <> :id`
	markerNormClearScopeQuery = `
		UPDATE tech_card_marker SET is_norm = FALSE, updated_by = :username
		WHERE ` + markerNormScopePredicate + ` AND id <> :id`
	markerNormSetQuery = `
		UPDATE tech_card_marker SET is_norm = :is_norm, updated_by = :username WHERE id = :id`
)

// SetMarkerNorm designates one раскладка as the НОРМИРОВОЧНАЯ one for its cloth, or clears it, and
// returns the id of the previous norm of the SAME cloth (0 = there was none).
//
// THIS IS THE ONLY WRITER OF is_norm, and that is what holds the invariant. SaveMarker does not list
// the column, so re-saving geometry can neither seize a norm nor lose one; DeleteMarker takes the
// norm away with the row, leaving the card without one, which the gate will see.
//
// EXCLUSIVITY IS HELD HERE AND NOT BY A UNIQUE INDEX. A unique index over (tech_card_id, norm scope)
// does work — until a BOM line is deleted: fk_tcm_bom is ON DELETE SET NULL (0257), so the deletion
// moves that line's norm into the «no cloth» scope, and if one already lives there MySQL refuses the
// delete with ERROR 1761. BOM rows are diffed INSIDE UpdateTechCard, so that refusal would land on
// the save of the whole card, in an error mentioning neither a norm nor a раскладка — the exact trade
// 0257 declined when it chose SET NULL. The residual (a bug could leave two norms in a scope) is
// contained by three things: one writer, in this SERIALIZABLE transaction; a deterministic tiebreak
// in every reader (entity.SelectNorm), so even a corrupted state gives every screen the SAME answer;
// and the conflict being REPORTED on the wire (TechCardMarkerSummary.norm_conflict) instead of
// silently resolved.
//
// It RECOMPUTES NOTHING. There is no server-side «apply marker»: a recipe holds a copied consumption
// figure with provenance consumption_source='marker' and no reference back to the marker, so
// re-designating changes which раскладка is authoritative without moving any number already applied.
// The API layer says so in the response rather than leaving the operator to assume otherwise.
func (s *Store) SetMarkerNorm(ctx context.Context, id int, isNorm bool, username string) (int, error) {
	if id <= 0 {
		return 0, entity.NewFieldViolation("id", "must_be_positive", "",
			"name the раскладка to designate as the norm")
	}
	var previousNormID int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// RESET FIRST. txFunc RETRIES a transaction that hit a deadlock or a lock timeout — and two
		// concurrent designations on one scope are exactly the shape that produces one, since each
		// takes a share lock on the other's rows before upgrading. Without this line an attempt that
		// found a previous norm and then rolled back would leave its id standing, and the retry — which
		// may legitimately find none — would report a раскладка it did not touch.
		previousNormID = 0
		db := rep.DB()
		row, err := storeutil.QueryNamedOne[struct {
			TechCardId int           `db:"tech_card_id"`
			BomItemId  sql.NullInt64 `db:"bom_item_id"`
		}](ctx, db, `SELECT tech_card_id, bom_item_id FROM tech_card_marker WHERE id = :id`,
			map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: marker %d", entity.ErrMarkerNotFound, id)
			}
			return fmt.Errorf("load marker %d: %w", id, err)
		}
		// Designating a norm is a decision about the card's CONTENT, so a released card refuses — like
		// every sibling guard, inside the transaction so a concurrent release cannot slip past it.
		if err := storeutil.RequireMutableTechCard(ctx, db, row.TechCardId); err != nil {
			return err
		}
		scope := map[string]any{
			"card":     row.TechCardId,
			"bom":      row.BomItemId,
			"id":       id,
			"username": username,
		}
		// CLEARING first, and it returns before touching any sibling: «this is no longer the norm» says
		// nothing about who is, and quietly promoting a neighbour would invent a decision nobody made.
		if !isNorm {
			if _, err := storeutil.ExecNamedRows(ctx, db, markerNormSetQuery,
				map[string]any{"id": id, "is_norm": false, "username": username}); err != nil {
				return fmt.Errorf("clear norm on marker %d: %w", id, err)
			}
			return nil
		}
		// The previous norm is read BEFORE it is cleared, and through the same predicate that clears
		// it. The winner is chosen by entity.SelectNorm rather than by an ORDER BY here: if the scope
		// somehow holds several, the id this call REPORTS must be the one every reader would also have
		// been showing as the effective norm — otherwise the response names a раскладка the screen
		// never displayed.
		siblings, err := storeutil.QueryListNamed[struct {
			Id        int          `db:"id"`
			Name      string       `db:"name"`
			UpdatedAt sql.NullTime `db:"updated_at"`
		}](ctx, db, markerNormScopeSiblingsQuery, scope)
		if err != nil {
			return fmt.Errorf("resolve the previous norm of marker %d: %w", id, err)
		}
		markerScope := entity.NormScope{BomItemId: row.BomItemId.Int64, Bound: row.BomItemId.Valid}
		peers := make([]entity.NormPeer, 0, len(siblings))
		for _, sb := range siblings {
			peers = append(peers, entity.NormPeer{
				Id: sb.Id, Name: sb.Name, Scope: markerScope, UpdatedAt: sb.UpdatedAt.Time,
			})
		}
		if winner, _, ok := entity.SelectNorm(peers, markerScope); ok {
			previousNormID = winner.Id
		}
		// Clears ALL of them, not just the one reported: a scope that already held two is repaired by
		// this write rather than carried forward.
		if _, err := storeutil.ExecNamedRows(ctx, db, markerNormClearScopeQuery, scope); err != nil {
			return fmt.Errorf("clear the previous norm of marker %d: %w", id, err)
		}
		if _, err := storeutil.ExecNamedRows(ctx, db, markerNormSetQuery,
			map[string]any{"id": id, "is_norm": true, "username": username}); err != nil {
			return fmt.Errorf("designate marker %d as the norm: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return previousNormID, nil
}

// DeleteMarker removes a saved раскладка. Nothing references markers, so the delete is plain —
// but it is still card content, so a released card refuses.
func (s *Store) DeleteMarker(ctx context.Context, id int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		row, err := storeutil.QueryNamedOne[struct {
			TechCardId int `db:"tech_card_id"`
		}](ctx, db, `SELECT tech_card_id FROM tech_card_marker WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: marker %d", entity.ErrMarkerNotFound, id)
			}
			return fmt.Errorf("load marker %d: %w", id, err)
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, row.TechCardId); err != nil {
			return err
		}
		rows, err := storeutil.ExecNamedRows(ctx, db,
			`DELETE FROM tech_card_marker WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("delete marker %d: %w", id, err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: marker %d", entity.ErrMarkerNotFound, id)
		}
		return nil
	})
}

// storedMarkerFacts hands the stored blob to the API layer's distiller, once, and only if the rule
// asks — which it does only when it is about to consider the exemption. A save that introduces
// nothing forbidden never pays for the parse.
//
// ok=false covers every way the geometry on file cannot be read: no stored row, no distiller wired,
// or a blob that does not parse. The rule treats all three as «cannot forgive», never as «nothing was
// there» — see entity.storedFactsOf for why an unreadable blob must fail closed rather than open.
func storedMarkerFacts(ctx context.Context, id int, layout string,
	distil func(string) (entity.MarkerLayoutFacts, error)) entity.StoredMarkerFacts {
	return func() (entity.MarkerLayoutFacts, bool) {
		if id <= 0 || layout == "" {
			return entity.MarkerLayoutFacts{}, false
		}
		if distil == nil {
			// Fail-closed and therefore harmless — but a caller in this state silently loses EVERY
			// exemption, so it must not be silent. dto sets the field where the payload is converted;
			// anything reaching here without it was built by hand somewhere inside the server.
			slog.Default().WarnContext(ctx, "marker save has no stored-layout distiller; exemptions withheld",
				slog.Int("marker_id", id))
			return entity.MarkerLayoutFacts{}, false
		}
		facts, err := distil(layout)
		if err != nil {
			// Not fatal, by design: the READ path already serves this marker as summary-plus-warning
			// rather than failing, and the save path must not be the stricter of the two. It only
			// costs the row its exemption, which it cannot honestly claim anyway — nobody could have
			// loaded that geometry to send it back.
			slog.Default().WarnContext(ctx, "stored marker layout does not parse; exemption withheld",
				slog.Int("marker_id", id), slog.String("err", err.Error()))
			return entity.MarkerLayoutFacts{}, false
		}
		return facts, true
	}
}
