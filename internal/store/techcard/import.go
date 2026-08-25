package techcard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.2 — THE WRITE SIDE OF AN IMPORT: one transaction, one new card, never an update.
//
// AN IMPORT ALWAYS CREATES (owner decision). It never overwrites a card, not even when the style
// number matches — merging two authoring histories is a decision nobody delegated to a ZIP, and
// «the same style number» is the weakest possible evidence that two cards are the same garment.
// A collision on the number is resolved by the handler (Ф3.3) by minting a new one; nothing here
// knows about it.
//
// AND IT ALWAYS ARRIVES A DRAFT. Three things stand between an archive and a card that LOOKS
// SIGNED, and they are three because each one alone is a habit rather than a guarantee:
//
//	1. the export writes no sign-offs and no release into the archive at all (FORMAT.md §4);
//	2. the sanitiser forces draft on the way in, against a hand-made archive (sanitize.go);
//	3. THIS FILE forces it again, right here, on the entity payload — because between (2) and the
//	   INSERT sits a whole handler, a converter and a wire contract, and the create pipeline
//	   COERCES supplied sign-offs into fresh ones stamped with a fresh digest and the importing
//	   operator's name rather than refusing them. Handing it none is the only defence that does not
//	   depend on somebody else's care, so the adversarial gate (Ф3.6) passes because it cannot fail,
//	   not because the exporter was polite.
//
// EVERYTHING IS ONE s.txFunc, which is SERIALIZABLE with deadlock retries (the same envelope
// SaveTechCardRelease uses). That is not tidiness: an import writes a card, its children, its
// chart, its markers, its measured areas, its assembly bill AND the journal row that says the card
// came from an archive. Any split would make a half-import reachable — a card in the catalogue
// with no chart and no explanation of why — and the compensation for it would have to delete a
// card, which no other path in this service does.
//
// THE CLOSURE IS RE-ENTERED ON A DEADLOCK RETRY, so every value it computes is reset at the top of
// it. A newID left over from a rolled-back attempt is the kind of bug that only shows up under
// load, and only as a wrong number in a response.
//
// WHAT MAY NOT HAPPEN IN HERE: no bucket calls and no protojson parsing. The files were moved
// before the transaction opened (Ф3.1) and every payload was parsed before it (Ф2.3) — a
// transaction that talks to the network holds SERIALIZABLE locks for the duration of somebody
// else's timeout.
// ─────────────────────────────────────────────────────────────────────────────

// EVERY NAMED QUERY OF THIS FILE IS A PACKAGE CONSTANT, and not for tidiness: sqlx reads EVERY ':'
// in a query as a named parameter — including one inside a `--` comment — so a bind error is a
// runtime failure on a path that only a database-backed test would reach. Held here, they are bound
// by TestArchiveImportQueriesBind without a database, which is the only test this file can have
// locally (the store's own suite talks to a real MySQL). None of them carries a SQL comment; the
// rationale lives in Go above each call site.
const (
	archiveImportClaimQuery = `
		UPDATE tech_card_import
		SET status = :committed, committed_at = NOW()
		WHERE import_id = :import_id AND status = :uploaded`

	archiveImportStatusQuery = `SELECT status FROM tech_card_import WHERE import_id = :import_id`

	archiveImportStampResultQuery = `
		UPDATE tech_card_import
		SET tech_card_id = :tech_card_id, report = :report
		WHERE import_id = :import_id`

	archiveImportRowColumns = `id, import_id, tech_card_id, object_key, status, imported_by,
		created_at, committed_at, acknowledged_at, archive_manifest, colorways_payload, report`

	archiveImportRowByIDQuery = `SELECT ` + archiveImportRowColumns + `
		FROM tech_card_import WHERE import_id = :import_id`

	archiveImportLatestByCardQuery = `SELECT ` + archiveImportRowColumns + `
		FROM tech_card_import WHERE tech_card_id = :tech_card_id ORDER BY id DESC LIMIT 1`

	archiveImportAckQuery = `
		UPDATE tech_card_import
		SET acknowledged_at = NOW()
		WHERE tech_card_id = :tech_card_id AND acknowledged_at IS NULL`

	archiveImportStyleFactsQuery = `
		UPDATE tech_card
		SET fit = :fit,
		    composition = JSON_QUOTE(:composition),
		    care_instructions = :care_instructions,
		    model_wears_height_cm = :model_wears_height_cm,
		    model_wears_size_id = :model_wears_size_id
		WHERE id = :id`

	archiveImportGradeBaseQuery = `UPDATE tech_card SET grade_base_size_id = :base WHERE id = :id`

	archiveImportBomLinesQuery = `SELECT id, line_key FROM tech_card_bom_item WHERE tech_card_id = :card`

	archiveImportLabelsQuery = `SELECT id, display_order FROM tech_card_label WHERE tech_card_id = :card`

	archiveImportLabelRelinkQuery = `
		UPDATE tech_card_label SET bom_item_id = :bom WHERE id = :id AND tech_card_id = :card`

	// colorway_id and run_id are written as literal NULL, with no parameter to carry anything else:
	// a colourway is a product an import does not create, and only card markers travel. is_norm is
	// absent from the statement entirely — designation is SetMarkerNorm's alone on every path.
	archiveImportMarkerInsertQuery = `
		INSERT INTO tech_card_marker
			(tech_card_id, size_id, bom_item_id, colorway_id, run_id, name, source, fabric_width_cm,
			 gap_cm, edge_margin_cm, selvedge_cm, allow_cross_grain, sets, total_units,
			 used_length_cm, efficiency_pct, placed_count, total_count, is_draft, layout,
			 layout_schema_version, seam_allowance_mm, contour_allowance_mm, contour_layer,
			 grain_layer, allow_flip, piece_set_fp, created_by, updated_by)
		VALUES (:tech_card_id, :size_id, :bom_item_id, NULL, NULL, :name, :source, :fabric_width_cm,
			 :gap_cm, :edge_margin_cm, :selvedge_cm, :allow_cross_grain, :sets, :total_units,
			 :used_length_cm, :efficiency_pct, :placed_count, :total_count, :is_draft, :layout,
			 :schema_version, :seam_allowance_mm, :contour_allowance_mm, :contour_layer,
			 :grain_layer, :allow_flip, :piece_set_fp, :username, :username)`

	archiveImportPieceAreaInsertQuery = `
		INSERT INTO tech_card_piece_area
			(tech_card_id, scope_key, piece_line_key, size_id, area_cm2, perimeter_cm,
			 contour_layer, seam_allowance_mm, hulled, ambiguous_pick,
			 sheet_fingerprint, parsed_by, parsed_at)
		VALUES (:card, :scope, :piece, :size, :area, :perimeter,
			 :layer, :seam, :hulled, :ambiguous,
			 :fingerprint, :parsed_by, :parsed_at)`

	archiveImportComponentPurposeQuery = `SELECT id, purpose FROM tech_card WHERE id IN (:ids)`

	archiveImportAssemblyInsertQuery = `
		INSERT INTO style_assembly
			(style_id, component_tech_card_id, size_id, qty, print_note, position_note,
			 active, created_by, updated_by)
		VALUES (:style_id, :component_tech_card_id, :size_id, :qty, :print_note, :position_note,
			 :active, :actor, :actor)`
)

// ImportTechCardArchive writes ONE imported archive: a new tech card, everything the archive
// carried about it, and the tech_card_import row that says so. It returns the new card's id.
//
// Errors worth naming to a caller: entity.ErrImportAlreadyCommitted when the import_id has already
// produced a card (the double-click race, closed inside the transaction), sql.ErrNoRows when there
// is no such import_id, and a UNIQUE violation on style_number, which is deliberately NOT retried
// here — a retry inside a SERIALIZABLE transaction would re-run every write above it. The handler
// picks a new number and calls again.
func (s *Store) ImportTechCardArchive(ctx context.Context, in entity.TechCardArchiveImport) (int, error) {
	importID := strings.TrimSpace(in.ImportID)
	if importID == "" {
		return 0, fmt.Errorf("tech card import: import_id is required")
	}
	if in.Card == nil {
		return 0, fmt.Errorf("tech card import %s: no card payload", importID)
	}
	// The report is stored in the SAME transaction as the card and is not optional: a committed
	// import whose report went missing is a card full of unexplained gaps with nothing left that
	// explains them. Validity is checked HERE, outside the transaction — MySQL would refuse the
	// JSON column with a raw 3140 from the middle of the last statement, after every write above it.
	if !json.Valid(in.Report) {
		return 0, fmt.Errorf("tech card import %s: the report is not valid JSON", importID)
	}

	card := in.Card
	s.prepareImportedCard(card)

	if err := s.ensureDictionaryFresh(ctx, "archive import"); err != nil {
		return 0, err
	}

	var newID int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		newID = 0 // a retried attempt must not inherit the rolled-back one's id

		// CLAIM FIRST, WRITE SECOND. The row is taken exclusively before a single byte of the card
		// exists, so a concurrent twin of this call blocks here and then reads its own refusal —
		// rather than both transactions doing the whole import and one losing it at the very end.
		if err := claimTechCardImportRow(ctx, db, importID); err != nil {
			return err
		}

		params, err := techCardInsertHeaderParams(card)
		if err != nil {
			return err
		}
		newID, err = storeutil.ExecNamedLastId(ctx, db,
			fmt.Sprintf(`INSERT INTO tech_card (%s) VALUES (%s)`, techCardHeaderColumns, techCardHeaderValues),
			params)
		if err != nil {
			return fmt.Errorf("insert imported tech card: %w", err)
		}
		// The triple goes in BEFORE the children: insertTechCardChildren validates the size range
		// against the card's category, which it reads back off this row. Same order as AddTechCard
		// and CloneTechCardForSeason, and for the same reason.
		if err := syncStyleCategoryTriple(ctx, db, newID, card.CategoryId); err != nil {
			return err
		}
		if err := insertTechCardChildren(ctx, db, newID, card); err != nil {
			return err
		}

		// The imported card's OWN size range, read back after the children landed. It is the one
		// authority every size below is held against: the resolver mapped names to this base's
		// dictionary, but the dictionary is instance-wide and this card makes a handful of its
		// sizes. A row filed under a size the card does not make measures nothing.
		rng, err := storeutil.LoadTechCardSizeRange(ctx, db, newID)
		if err != nil {
			return err
		}

		if err := writeImportedStyleFacts(ctx, db, newID, in.Style, rng); err != nil {
			return err
		}
		if err := insertImportedSizeChart(ctx, db, newID, in.SizeChart, rng); err != nil {
			return err
		}

		// ONE map, read once, used by BOTH the labels and the markers: each of them points at a BOM
		// line by its stable key and needs the id the upsert just minted for it.
		bomIDs, err := importedBomLineIDs(ctx, db, newID)
		if err != nil {
			return err
		}
		if err := resewImportedLabels(ctx, db, newID, in.Labels, bomIDs); err != nil {
			return err
		}
		if err := insertImportedMarkers(ctx, db, newID, in.Markers, bomIDs, in.Actor, rng); err != nil {
			return err
		}
		if err := insertImportedPieceAreas(ctx, db, newID, importID, in.PieceAreas, rng); err != nil {
			return err
		}
		if err := insertImportedAssembly(ctx, db, newID, in.Assembly, in.Actor, rng); err != nil {
			return err
		}
		// A new card links no products of its own (colourways are created separately and an import
		// creates none), so this is a no-op today — kept because AddTechCard and the season clone
		// both run it, and «the create paths agree» is worth more than one saved statement.
		if err := remintCardProducts(ctx, db, newID, nil); err != nil {
			return err
		}
		if err := appendTechCardRevision(ctx, db, newID, in.Actor, "header", "created",
			importedFromArchiveSummary(in)); err != nil {
			return err
		}
		return finishTechCardImportRow(ctx, db, importID, newID, in.Report)
	})
	if err != nil {
		return 0, fmt.Errorf("can't import tech card archive %s: %w", importID, err)
	}
	return newID, nil
}

// prepareImportedCard is defence (3) of the three named at the top of this file: the payload is
// coerced to a DRAFT WITH NO SIGN-OFFS before a single row exists.
//
// It runs on the ENTITY payload, after every converter and wire gate has had its say, because that
// is the last point at which anything can still be removed. What it takes off, and why each one:
//
//   - APPROVAL STATE → draft. A card arriving `released` would be released HERE, on evidence that
//     is nothing but a file somebody sent.
//   - SIGN-OFFS → gone. Not «refused»: the create pipeline COERCES supplied sign-offs into fresh
//     ones, stamped with a digest computed here and attributed to whoever ran the import. Handing
//     it a signature is therefore how you MINT one, and the only safe input is none.
//   - approved_at / released_at → cleared, by stampApprovalTimes for the draft state — the same
//     call AddTechCard and the season clone make, because the server is authoritative for both
//     stamps and an archive's are its own instance's.
//   - BASE MODEL → cleared. It names a row in the SOURCE's model table and no model dictionary
//     travels beside it (FORMAT.md §4). The export blanks it and the resolver clears it; this is
//     the same statement made where the write happens, so a payload that arrived by some other
//     route still cannot point at a stranger's fit model.
//
// Separated from the transaction so it can be tested for exactly this — see
// TestPreparedImportedCardCannotLookSigned, which is the DB-free half of the adversarial gate.
func (s *Store) prepareImportedCard(card *entity.TechCardInsert) {
	if card == nil {
		return
	}
	card.ApprovalState = entity.TechCardApprovalDraft
	card.Signoffs = nil
	card.BaseModelId = sql.NullInt32{}
	s.stampApprovalTimes(card, "", sql.NullTime{}, sql.NullTime{})
}

// importedFromArchiveSummary is the journal sentence. It names the source style and the host it
// came from, which is the whole point of the entry: months later, «why does this card have gaps»
// has an answer that does not depend on the tech_card_import row surviving.
func importedFromArchiveSummary(in entity.TechCardArchiveImport) string {
	style := strings.TrimSpace(in.SourceStyleNumber)
	if style == "" {
		style = "an unnumbered style"
	}
	host := strings.TrimSpace(in.SourceHost)
	if host == "" {
		host = "an unnamed host"
	}
	return fmt.Sprintf("imported from archive %s of %s", style, host)
}

// ────────────────────────────── the tech_card_import row ──────────────────────────────

// claimTechCardImportRow takes the upload row exclusively and refuses anything but an `uploaded`
// one.
//
// It is an UPDATE rather than a SELECT ... FOR UPDATE because the guard and the claim are the same
// act: two concurrent commits of one import_id serialise on this statement, the loser sees zero
// rows once the winner commits, and neither of them can have created a card in between. A plain
// SELECT would take a shared lock in SERIALIZABLE and turn the same race into a deadlock retry that
// re-runs the whole import.
//
// Zero rows is not ambiguous here: the statement moves both `status` and `committed_at`, so a
// matching row always counts as CHANGED even on a driver without clientFoundRows.
func claimTechCardImportRow(ctx context.Context, db dependency.DB, importID string) error {
	rows, err := storeutil.ExecNamedRows(ctx, db, archiveImportClaimQuery,
		map[string]any{
			"import_id": importID,
			"committed": entity.TechCardImportStatusCommitted,
			"uploaded":  entity.TechCardImportStatusUploaded,
		})
	if err != nil {
		return fmt.Errorf("claim import %s: %w", importID, err)
	}
	if rows > 0 {
		return nil
	}
	// Nothing was claimed: say WHY, because the two reasons lead to different answers on screen.
	st, err := storeutil.QueryNamedOne[struct {
		Status string `db:"status"`
	}](ctx, db, archiveImportStatusQuery, map[string]any{"import_id": importID})
	if err != nil {
		return err // sql.ErrNoRows -> NOT_FOUND upstream
	}
	if st.Status == entity.TechCardImportStatusCommitted {
		return fmt.Errorf("%w: import %s", entity.ErrImportAlreadyCommitted, importID)
	}
	return fmt.Errorf("import %s is %q and can no longer be committed", importID, st.Status)
}

// finishTechCardImportRow stamps the claimed row with what the transaction produced.
//
// The report lands here rather than in a later call for the same reason the whole write is one
// transaction: an import that committed and then failed to record what it skipped would leave a
// card whose gaps nobody can explain.
func finishTechCardImportRow(ctx context.Context, db dependency.DB, importID string, techCardID int, report []byte) error {
	if err := storeutil.ExecNamed(ctx, db, archiveImportStampResultQuery,
		map[string]any{
			"import_id":    importID,
			"tech_card_id": techCardID,
			"report":       string(report),
		}); err != nil {
		return fmt.Errorf("record import %s result: %w", importID, err)
	}
	return nil
}

// GetTechCardImportByImportID returns one upload row by its ULID — the dialogue's state between the
// dry run and the commit. sql.ErrNoRows when there is none (NOT_FOUND upstream).
func (s *Store) GetTechCardImportByImportID(ctx context.Context, importID string) (entity.TechCardArchiveImportRecord, error) {
	return storeutil.QueryNamedOne[entity.TechCardArchiveImportRecord](ctx, s.DB,
		archiveImportRowByIDQuery, map[string]any{"import_id": strings.TrimSpace(importID)})
}

// GetTechCardImportReport returns the LATEST import a card came from, so the card can say «this
// arrived as an archive, here is what did not fit».
//
// The latest and not the only one: tech_card_import.tech_card_id is ON DELETE SET NULL and the row
// outlives its card deliberately, so a card id can in principle be reached by more than one row
// over time. Newest by id — the same order the journal reads in. sql.ErrNoRows when the card came
// from no archive at all, which is the ordinary case for every card anybody typed by hand.
func (s *Store) GetTechCardImportReport(ctx context.Context, techCardID int) (entity.TechCardArchiveImportRecord, error) {
	return storeutil.QueryNamedOne[entity.TechCardArchiveImportRecord](ctx, s.DB,
		archiveImportLatestByCardQuery, map[string]any{"tech_card_id": techCardID})
}

// AcknowledgeTechCardImport closes the «imported» banner on a card: it stamps every unacknowledged
// import row of that card as read.
//
// IDEMPOTENT BY CONSTRUCTION — the guard is `acknowledged_at IS NULL`, so a second click writes
// nothing and is not an error, and the stamp keeps the moment the operator actually read the report
// rather than the moment they last clicked anything.
func (s *Store) AcknowledgeTechCardImport(ctx context.Context, techCardID int) error {
	if err := storeutil.ExecNamed(ctx, s.DB, archiveImportAckQuery,
		map[string]any{"tech_card_id": techCardID}); err != nil {
		return fmt.Errorf("acknowledge import report of tech card %d: %w", techCardID, err)
	}
	return nil
}

// ────────────────────────────── style facts ──────────────────────────────

// writeImportedStyleFacts writes the CATALOGUE half of the card — fit, composition, care and the
// model-wears reference.
//
// They are written here, by hand, because no other create path writes them at all: those columns
// belong to UpdateStyle (the sole writer of a style's catalogue facts), the tech-card converter
// does not carry them and techCardHeaderColumns does not list them. An import that only ran the
// create pipeline would land a card whose fit and care were silently blank — facts the archive
// carried and that a receiving constructor expects to read.
//
// The fragments mirror UpdateStyle's own, including JSON_QUOTE on `composition`: that column is
// JSON and holds a quoted string (the legacy free-text composition), while the STRUCTURAL fibre
// breakdown lives in style_composition and is derived from the BOM.
//
// THAT DERIVATION IS DELIBERATELY NOT RUN HERE, though UpdateStyle ends with it. It reads the
// TARGET catalogue's own material compositions and refuses with a field-tagged error when one of
// them does not sum to 100 — so running it would let a pre-existing flaw in somebody else's article
// abort a whole import, over a value that is derived and can be re-derived by the card's first
// save. AddTechCard does not run it either, and an imported card is a created card.
func writeImportedStyleFacts(ctx context.Context, db dependency.DB, id int,
	f entity.TechCardArchiveStyleFacts, rng storeutil.TechCardSizeRange) error {
	// «The model wears a size this style does not make» is either a foreign id worn as a local one
	// or a fact about nothing. Cleared rather than refused, on the same principle the season clone
	// applies to a grade base outside the range: one display line is not worth failing an import.
	modelWearsSize := f.ModelWearsSizeId
	if modelWearsSize.Valid && modelWearsSize.Int32 <= 0 {
		modelWearsSize = sql.NullInt32{} // 0 is «unset» across the whole contract, never size zero
	}
	if modelWearsSize.Valid && !rng.Has(int(modelWearsSize.Int32)) {
		slog.Default().Warn("tech card import: dropped a model-wears size outside the imported card's range",
			slog.Int("tech_card_id", id), slog.Int("size_id", int(modelWearsSize.Int32)))
		modelWearsSize = sql.NullInt32{}
	}
	if err := storeutil.ExecNamed(ctx, db, archiveImportStyleFactsQuery,
		map[string]any{
			"id":                    id,
			"fit":                   f.Fit,
			"composition":           f.Composition,
			"care_instructions":     f.CareInstructions,
			"model_wears_height_cm": f.ModelWearsHeightCm,
			"model_wears_size_id":   modelWearsSize,
		}); err != nil {
		return fmt.Errorf("write imported style facts of tech card %d: %w", id, err)
	}
	return nil
}

// ────────────────────────────── size chart ──────────────────────────────

// insertImportedSizeChart writes the measurement grid and the grade rule it was authored from.
//
// Both axes arrive already resolved against THIS base's dictionaries (sizes and measurement names
// travel by name and are looked up by the resolver), so nothing is created here — an import that
// quietly added a measurement name because one archive spelled it differently would corrupt every
// other style's chart with it.
//
// A cell whose size is outside the imported card's own range is DROPPED, not refused, and logged.
// That is the season clone's rule (its carry-over intersects with the clone's size range in SQL)
// and the resolver's principle in one: a missing reference degrades, and one measurement is not
// worth failing an import over. It can only happen on a malformed archive, where the manifest's
// size map and the chart's names disagree.
func insertImportedSizeChart(ctx context.Context, db dependency.DB, id int,
	chart entity.StyleSizeChart, rng storeutil.TechCardSizeRange) error {
	rows := make([]map[string]any, 0, len(chart.Cells))
	for _, c := range chart.Cells {
		if c.SizeID <= 0 || c.MeasurementNameID <= 0 {
			slog.Default().Warn("tech card import: dropped an unaddressed size chart cell",
				slog.Int("tech_card_id", id), slog.Int("size_id", c.SizeID),
				slog.Int("measurement_name_id", c.MeasurementNameID))
			continue
		}
		if !rng.Has(c.SizeID) {
			slog.Default().Warn("tech card import: dropped a size chart cell outside the imported card's size range",
				slog.Int("tech_card_id", id), slog.Int("size_id", c.SizeID))
			continue
		}
		rows = append(rows, map[string]any{
			"tech_card_id":        id,
			"size_id":             c.SizeID,
			"measurement_name_id": c.MeasurementNameID,
			"measurement_value":   c.Value,
		})
	}
	if len(rows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_size_measurement", rows); err != nil {
			return fmt.Errorf("insert imported size chart of tech card %d: %w", id, err)
		}
	}

	stepRows := make([]map[string]any, 0, len(chart.GradeSteps))
	for _, g := range chart.GradeSteps {
		if g.MeasurementNameID <= 0 {
			continue
		}
		stepRows = append(stepRows, map[string]any{
			"tech_card_id":        id,
			"measurement_name_id": g.MeasurementNameID,
			"step":                g.Step,
		})
	}
	if len(stepRows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_grade_rule", stepRows); err != nil {
			return fmt.Errorf("insert imported grade rule of tech card %d: %w", id, err)
		}
	}

	// The grade base is one column of the same rule and moves with it. Outside the range it is
	// cleared, which is exactly what the season clone does with a base its clone does not make.
	base := chart.GradeBaseSizeID
	if base > 0 && !rng.Has(base) {
		slog.Default().Warn("tech card import: dropped a grade base size outside the imported card's size range",
			slog.Int("tech_card_id", id), slog.Int("size_id", base))
		base = 0
	}
	if base > 0 {
		if err := storeutil.ExecNamed(ctx, db, archiveImportGradeBaseQuery,
			map[string]any{"id": id, "base": nullableID(base)}); err != nil {
			return fmt.Errorf("write imported grade base of tech card %d: %w", id, err)
		}
	}
	return nil
}

// ────────────────────────────── BOM keys → new ids ──────────────────────────────

// importedBomLineIDs reads back the ids the BOM upsert just minted, keyed by the stable line_key
// the archive addresses them with.
//
// Read back rather than threaded out of insertTechCardChildren: that function owns the resolver it
// builds internally, and reaching into it would mean changing a signature the whole create path
// shares. The rows are this transaction's own, so the read costs one statement and cannot see
// anybody else's card.
//
// Keys are compared case-insensitively for the same reason the marker path does it: line_key is a
// ULID stored in a CHAR column under a case-insensitive collation, and two spellings of one key
// must not resolve to two different lines.
func importedBomLineIDs(ctx context.Context, db dependency.DB, techCardID int) (map[string]int64, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Id      int64  `db:"id"`
		LineKey string `db:"line_key"`
	}](ctx, db, archiveImportBomLinesQuery, map[string]any{"card": techCardID})
	if err != nil {
		return nil, fmt.Errorf("read back BOM lines of imported tech card %d: %w", techCardID, err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		if key := importedLineKey(r.LineKey); key != "" {
			out[key] = r.Id
		}
	}
	return out, nil
}

// importedLineKey normalises a line_key for lookup: trimmed and case-folded, matching the column's
// own collation.
func importedLineKey(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// ────────────────────────────── R2-2: labels → BOM lines ──────────────────────────────

// resewImportedLabels restores each label's link to the BOM line it prints on.
//
// WHY THIS EXISTS AT ALL. TechCardLabel.bom_item_id is a REAL input FK carrying the SOURCE base's
// row id. Written as it stands it would either break the foreign key — killing the whole import —
// or, on the day that number happens to name a live row here, bind the label to ANOTHER CARD's BOM
// line. So the resolver translates it into the line's stable key and clears the id off the payload,
// and the card lands with every label unlinked. This function is the second half of that transfer.
//
// WHY IT IS THE STEP MOST EASILY LOST. There is no hole in the report for a label whose link went
// missing, and there cannot be: the resolver deliberately wrote none, because the link is not lost
// at that point — it is in the plan, waiting for the new ids to exist. Skip this and the label
// imports with a NULL link, in silence, with a clean report saying everything is fine. Nothing
// downstream fails, because a NULL bom_item_id is legal: it means «this label names no article»,
// which is a legitimate state a great many labels are actually in. That is exactly why the failure
// is invisible and why the miss below is LOUD.
//
// A key that resolves to nothing is therefore an error rather than a silent unlink: the BOM travels
// verbatim in the same card.json the label came from, so the line the label names is either in the
// imported BOM or the archive is corrupt.
func resewImportedLabels(ctx context.Context, db dependency.DB, techCardID int,
	links []entity.TechCardArchiveLabelLink, bomIDs map[string]int64) error {
	if len(links) == 0 {
		return nil
	}
	// display_order is the label's position in the payload, written by insertTechCardLabels from
	// the very slice the resolver indexed — that position IS the label's identity (labels are a
	// full-replace child with no key of their own). Addressed by id after this read rather than by
	// `WHERE display_order = ...`, so «no such label» is distinguishable from «the row was already
	// correct»: this driver counts rows CHANGED, not matched.
	rows, err := storeutil.QueryListNamed[struct {
		Id           int64 `db:"id"`
		DisplayOrder int   `db:"display_order"`
	}](ctx, db, archiveImportLabelsQuery, map[string]any{"card": techCardID})
	if err != nil {
		return fmt.Errorf("read back labels of imported tech card %d: %w", techCardID, err)
	}
	idByOrder := make(map[int]int64, len(rows))
	for _, r := range rows {
		idByOrder[r.DisplayOrder] = r.Id
	}

	for _, link := range links {
		key := importedLineKey(link.BomLineKey)
		if key == "" {
			continue
		}
		labelID, ok := idByOrder[link.LabelIndex]
		if !ok {
			return fmt.Errorf("re-sew imported label %d of tech card %d: the card has no label at that position",
				link.LabelIndex, techCardID)
		}
		bomID, ok := bomIDs[key]
		if !ok {
			return entity.NewFieldViolation(fmt.Sprintf("labels[%d].bom_item_id", link.LabelIndex),
				"not_in_tech_card", fmt.Sprintf("BOM line %s", link.BomLineKey),
				"the archive's label names a BOM line the archive did not carry")
		}
		if err := storeutil.ExecNamed(ctx, db, archiveImportLabelRelinkQuery,
			map[string]any{"id": labelID, "card": techCardID, "bom": bomID}); err != nil {
			return fmt.Errorf("re-sew imported label %d of tech card %d: %w", link.LabelIndex, techCardID, err)
		}
	}
	return nil
}

// ────────────────────────────── markers ──────────────────────────────

// insertImportedMarkers writes the card's раскладки: the row, its состав and the layout blob.
//
// It writes them directly instead of calling SaveMarker, and the reason is not convenience:
// SaveMarker opens its OWN transaction, so calling it from inside this one would be a second
// SERIALIZABLE transaction waiting on locks the first one holds. What it does that this does not,
// and why:
//
//   - THE FABRIC-DIRECTION VERDICT is skipped. Judging geometry means reading the blob, and the
//     store deliberately cannot read a layout (0257/0268 — the bytes are opaque to storage, the
//     distiller is injected by the API layer). The verdict's only durable effect is the generation
//     stamp below, and this path takes the STRICTEST value, so a marker imported today gains no
//     pass it did not earn.
//   - IS_NORM is not written and cannot be: the designation is SetMarkerNorm's alone on every path,
//     and entity.TechCardMarkerInsert has no member for it. An imported раскладка therefore lands
//     as ordinary geometry and somebody designates the norm deliberately.
//   - THE NORM'S MARKER STAMP IS NOT RE-SEWN, AND IN v1 THERE IS NOTHING TO RE-SEW. The stamp
//     `norm_marker_id` is a column of tech_card_colorway_usage and of nothing else (0291, checked
//     against the schema and every reader) — it belongs to a COLOURWAY's recipe row, and colourways
//     are products an import does not create (FORMAT.md §5.3). So no row of an imported card can
//     hold a stamp pointing anywhere, and the resolver's norm_marker_lost line is the whole of the
//     story until Ф6 applies colourways from the archive; that is where the re-sew will belong, on
//     rows that exist by then.
//   - THE COLOURWAY is not written: a colourway is a product, an import creates none, and the
//     resolver has already zeroed the field and reported it.
//   - RUN_ID is NULL by construction. Only card markers travel (FORMAT.md §5.7); a run's раскладка
//     belongs to its run and dies with it.
//
// A состав naming a size outside the card's range REFUSES the whole import, through the same
// requireCardSizes every marker save uses. That is deliberately harsher than the size chart's
// dropped cell above: a раскладка is a claim about cloth, and one whose состав lost a size no
// longer describes the lay that was measured — while its total_units, the divisor of every cost
// read downstream, would silently shrink.
func insertImportedMarkers(ctx context.Context, db dependency.DB, techCardID int,
	markers []entity.TechCardMarkerInsert, bomIDs map[string]int64, actor string,
	rng storeutil.TechCardSizeRange) error {
	if len(markers) == 0 {
		return nil
	}
	// The set the fingerprint saw IS the set this transaction committed against — computed from
	// this card's own just-inserted pieces, exactly as the save path computes it from the rows its
	// own transaction sees, and never taken from a payload. One read for every marker of the card:
	// the piece set does not move inside this transaction.
	pieceSetFp, err := cardPieceSetFingerprint(ctx, db, techCardID)
	if err != nil {
		return err
	}
	// (size_key, name) is UNIQUE per card. Two archive markers colliding on it would surface as a
	// bare MySQL 1062 from the middle of the import; named here, it says which two.
	seen := make(map[[2]string]bool, len(markers))
	for i, m := range markers {
		field := fmt.Sprintf("markers[%d]", i)
		if strings.TrimSpace(m.Name) == "" {
			return entity.NewFieldViolation(field+".name", "required", "", "the archive's marker has no name")
		}
		// The generated column the UNIQUE key uses folds «no size» into 0; mirrored here so the
		// dedupe below sees exactly what the index will. Named to avoid shadowing sizeKey(), the
		// assembly helper of this package.
		markerSizeKey := "0"
		if m.SizeId.Valid {
			markerSizeKey = fmt.Sprintf("%d", m.SizeId.Int64)
		}
		dedupe := [2]string{markerSizeKey, strings.ToLower(strings.TrimSpace(m.Name))}
		if seen[dedupe] {
			return entity.NewFieldViolation(field+".name", "duplicate", m.Name,
				"the archive carries two markers with the same name for the same size")
		}
		seen[dedupe] = true

		// A layout counting more placements than pieces is a WRONG DENOMINATOR, not an incomplete
		// result: there is no state in which a count contradicting itself describes cloth.
		if m.PlacedCount > m.TotalCount {
			return entity.NewFieldViolation(field+".placed_count", "over_total",
				fmt.Sprintf("%d of %d pieces placed", m.PlacedCount, m.TotalCount),
				"the archive's marker counts more placements than it has pieces")
		}
		// The legacy single size, when the row still carries one.
		if m.SizeId.Valid {
			if err := rng.Require(field+".size_id", int(m.SizeId.Int64)); err != nil {
				return err
			}
		}
		if err := requireCardSizes(ctx, db, techCardID, m.Composition); err != nil {
			return err
		}

		bomItemID := sql.NullInt64{}
		if key := importedLineKey(m.BomLineKey); key != "" {
			id, ok := bomIDs[key]
			if !ok {
				return entity.NewFieldViolation(field+".bom_line_key", "not_in_tech_card", m.BomLineKey,
					"the archive's marker names a BOM line the archive did not carry")
			}
			bomItemID = sql.NullInt64{Int64: id, Valid: true}
		}

		markerID, err := storeutil.ExecNamedLastId(ctx, db, archiveImportMarkerInsertQuery,
			map[string]any{
				"tech_card_id":    techCardID,
				"size_id":         m.SizeId,
				"bom_item_id":     bomItemID,
				"name":            m.Name,
				"source":          string(m.Source),
				"fabric_width_cm": m.FabricWidthCm,
				"gap_cm":          m.GapCm,
				"edge_margin_cm":  m.EdgeMarginCm,
				"selvedge_cm":     m.SelvedgeCm,
				// Derived from the very slice the child rows are written from, so the divisor of
				// money and its own состав cannot come apart.
				"total_units":       entity.TotalUnitsOf(m.Composition),
				"allow_cross_grain": m.AllowCrossGrain,
				"sets":              m.Sets,
				"used_length_cm":    m.UsedLengthCm,
				"efficiency_pct":    m.EfficiencyPct,
				"placed_count":      m.PlacedCount,
				"total_count":       m.TotalCount,
				// Черновик is a state of the COUNTERS, never a copy of a flag: a раскладка that
				// laid out fewer pieces than it has is a draft, on the source and here alike. The
				// consent flag the live save path reads is a client's, and an import has no client
				// — refusing a legitimately unfinished раскладка would abort an import over
				// geometry the source keeps quite happily.
				"is_draft": m.PlacedCount < m.TotalCount,
				"layout":   m.Layout,
				// THE STRICTEST generation on purpose. The column answers «what is the newest
				// policy this geometry has been judged under», and a LOWER value buys a legacy
				// exemption. Nothing here judged anything, so claiming the current generation is
				// the conservative direction: the first real save judges it under today's rules.
				"schema_version":       entity.MarkerLayoutSchemaWithFlip,
				"seam_allowance_mm":    m.SeamAllowanceMm,
				"contour_allowance_mm": m.ContourAllowanceMm,
				"contour_layer":        m.ContourLayer,
				"grain_layer":          m.GrainLayer,
				"allow_flip":           m.AllowFlip,
				"piece_set_fp":         pieceSetFp,
				"username":             actor,
			})
		if err != nil {
			return fmt.Errorf("insert imported marker %q on tech card %d: %w", m.Name, techCardID, err)
		}
		if err := replaceMarkerComposition(ctx, db, markerID, m.Composition); err != nil {
			return err
		}
	}
	return nil
}

// ────────────────────────────── measured piece areas ──────────────────────────────

// insertImportedPieceAreas carries the measured contour areas across, provenance and all.
//
// WHY THEY TRAVEL: they are what the server derives a cloth norm from when nobody typed one. Left
// behind, an imported card cannot cost itself until somebody re-parses every DXF — and the archive
// carried the numbers.
//
// WHOSE MEASUREMENT IT STAYS: parsed_by and parsed_at are written AS THEY STAND. Who measured this
// geometry and when is a fact about the measurement, not about the import; re-stamping it with
// today's date and the importing operator's name would claim a measurement nobody took.
//
// AND WHY EVERY IMPORTED SCOPE READS «STALE». Staleness is derived, never stored: the reader
// recomputes the scope's sheet fingerprint from today's sheets and block links and compares it with
// the one the areas were measured under. The archive does not carry that fingerprint at all — it is
// not on the wire, because on a live card it is recomputed on every read — and the sheets on this
// side are freshly uploaded objects with their own identity, so no honest value could match. This
// path therefore writes a fingerprint that is DOMAIN-SEPARATED from any real one: same CHAR(64) hex
// shape, derived from the import's own id, and equal to no sheet set that ever existed. The scope
// consequently reads «measured on {date}, patterns changed since», which is the honest verdict —
// the areas describe contours measured somewhere else, and a recount is one button away.
func insertImportedPieceAreas(ctx context.Context, db dependency.DB, techCardID int, importID string,
	areas []entity.TechCardArchivePieceArea, rng storeutil.TechCardSizeRange) error {
	// UNIQUE (tech_card_id, scope_key, piece_line_key, size_key) — a duplicate in the archive would
	// otherwise arrive as a bare 1062 with no hint of which row it was.
	seen := make(map[[3]string]bool, len(areas))
	for _, a := range areas {
		scope := strings.TrimSpace(a.ScopeKey)
		piece := importedLineKey(a.PieceLineKey)
		if scope == "" || piece == "" {
			slog.Default().Warn("tech card import: dropped an unaddressed piece area",
				slog.Int("tech_card_id", techCardID), slog.String("scope_key", a.ScopeKey),
				slog.String("piece_line_key", a.PieceLineKey))
			continue
		}
		// chk_tcpa_area_positive refuses a non-positive area at the schema level; caught here so a
		// corrupt row costs one line of the log rather than the whole import.
		if !a.AreaCm2.IsPositive() {
			slog.Default().Warn("tech card import: dropped a piece area that is not a positive number",
				slog.Int("tech_card_id", techCardID), slog.String("piece_line_key", a.PieceLineKey),
				slog.String("area_cm2", a.AreaCm2.String()))
			continue
		}
		// chk_tcpa_size_positive: the size is either UNSET (the piece does not grade and enters
		// every size's set whole) or a real one. A zero worn as a size would, through the generated
		// size_key, read as «ungraded» and merge two different rows into one.
		sizeID := a.SizeId
		if sizeID.Valid && sizeID.Int64 <= 0 {
			sizeID = sql.NullInt64{}
		}
		if sizeID.Valid && !rng.Has(int(sizeID.Int64)) {
			slog.Default().Warn("tech card import: dropped a piece area outside the imported card's size range",
				slog.Int("tech_card_id", techCardID), slog.String("piece_line_key", a.PieceLineKey),
				slog.Int64("size_id", sizeID.Int64))
			continue
		}
		// Mirrors the generated size_key the UNIQUE index is built on (NULL folds to 0). Named to
		// avoid shadowing sizeKey(), the assembly helper of this package.
		areaSizeKey := "0"
		if sizeID.Valid {
			areaSizeKey = fmt.Sprintf("%d", sizeID.Int64)
		}
		dedupe := [3]string{strings.ToUpper(scope), piece, areaSizeKey}
		if seen[dedupe] {
			return entity.NewFieldViolation("piece_areas", "duplicate",
				fmt.Sprintf("%s / %s / size %s", scope, piece, areaSizeKey),
				"the archive measures the same piece twice in one fabric scope and size")
		}
		seen[dedupe] = true

		if err := storeutil.ExecNamed(ctx, db, archiveImportPieceAreaInsertQuery,
			map[string]any{
				"card":        techCardID,
				"scope":       scope,
				"piece":       piece,
				"size":        sizeID,
				"area":        a.AreaCm2,
				"perimeter":   a.PerimeterCm,
				"layer":       a.ContourLayer,
				"seam":        a.SeamAllowanceMm,
				"hulled":      a.Hulled,
				"ambiguous":   a.AmbiguousPick,
				"fingerprint": importedPieceAreaFingerprint(importID, scope),
				"parsed_by":   a.ParsedBy,
				"parsed_at":   a.ParsedAt,
			}); err != nil {
			return fmt.Errorf("insert imported piece area %q of tech card %d: %w", a.PieceLineKey, techCardID, err)
		}
	}
	return nil
}

// importedPieceAreaFingerprint mints the provenance token an imported scope carries.
//
// It is a sha256 of a DOMAIN-SEPARATED string, so it has the column's shape (CHAR(64) hex) and can
// equal no fingerprint computed from a real sheet set — the reader's comparison is equality, and
// «not equal» is precisely the verdict wanted: these areas were measured against files that live in
// another base. Deterministic per (import, scope) so a scope's rows agree with each other, which is
// what the reader's "one fingerprint per scope" expectation rests on.
func importedPieceAreaFingerprint(importID, scopeKey string) string {
	sum := sha256.Sum256([]byte("techcard-archive-import|" + importID + "|" + scopeKey))
	return hex.EncodeToString(sum[:])
}

// ────────────────────────────── assembly bill ──────────────────────────────

// insertImportedAssembly writes the auxiliary bill — the labels, tags and packaging attached to the
// garment.
//
// Components arrive already resolved: they travel by STYLE NUMBER (never by id) and the resolver
// matched them against this base, dropping the ones it has no card for with a hole. What is checked
// here is what the target alone can know, and it is the same pair UpsertStyleAssembly checks: the
// component must be an AUXILIARY card (the FK proves the row exists, not what it is), and a
// size-scoped line must name a size this card makes. A line failing either is dropped with a log
// rather than refused, the season clone's rule for exactly this data.
func insertImportedAssembly(ctx context.Context, db dependency.DB, techCardID int,
	items []entity.StyleAssemblyInsert, actor string, rng storeutil.TechCardSizeRange) error {
	if len(items) == 0 {
		return nil
	}
	componentIDs := make([]int, 0, len(items))
	for _, it := range items {
		if it.ComponentTechCardId > 0 {
			componentIDs = append(componentIDs, it.ComponentTechCardId)
		}
	}
	if len(componentIDs) == 0 {
		return nil // every line names nothing; the loop below would drop them all anyway
	}
	purposeRows, err := storeutil.QueryListNamed[struct {
		Id      int    `db:"id"`
		Purpose string `db:"purpose"`
	}](ctx, db, archiveImportComponentPurposeQuery, map[string]any{"ids": componentIDs})
	if err != nil {
		return fmt.Errorf("load imported assembly component purposes: %w", err)
	}
	purposeByID := make(map[int]string, len(purposeRows))
	for _, r := range purposeRows {
		purposeByID[r.Id] = r.Purpose
	}

	seen := make(map[[2]int]bool, len(items))
	for _, it := range items {
		reason := ""
		switch {
		case it.ComponentTechCardId <= 0:
			reason = "the line names no component"
		case it.ComponentTechCardId == techCardID:
			reason = "a style cannot be its own assembly component"
		case !it.Qty.IsPositive():
			reason = "the quantity is not positive"
		case entity.TechCardPurpose(purposeByID[it.ComponentTechCardId]) != entity.TechCardPurposeAuxiliary:
			reason = "the component is not an auxiliary card here"
		// A non-positive size key means «all sizes» — not size zero — and is always in range.
		case sizeKey(it) > 0 && !rng.Has(sizeKey(it)):
			reason = "the line is filed under a size this card does not make"
		case seen[[2]int{it.ComponentTechCardId, sizeKey(it)}]:
			reason = "the same component and size is listed twice"
		}
		if reason != "" {
			slog.Default().Warn("tech card import: dropped an assembly line",
				slog.Int("tech_card_id", techCardID),
				slog.Int("component_tech_card_id", it.ComponentTechCardId),
				slog.String("reason", reason))
			continue
		}
		seen[[2]int{it.ComponentTechCardId, sizeKey(it)}] = true
		if err := storeutil.ExecNamed(ctx, db, archiveImportAssemblyInsertQuery,
			map[string]any{
				"style_id":               techCardID,
				"component_tech_card_id": it.ComponentTechCardId,
				"size_id":                it.SizeId,
				"qty":                    it.Qty,
				"print_note":             it.PrintNote,
				"position_note":          it.PositionNote,
				"active":                 it.Active,
				"actor":                  actor,
			}); err != nil {
			return fmt.Errorf("insert imported assembly component %d of tech card %d: %w",
				it.ComponentTechCardId, techCardID, err)
		}
	}
	return nil
}
