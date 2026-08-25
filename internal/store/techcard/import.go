package techcard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
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
// WHAT MAY NOT HAPPEN IN HERE: no bucket calls and no parsing of a payload that arrived from
// outside. The files were moved before the transaction opened (Ф3.1), every payload was parsed
// before it (Ф2.3) and so is the import report — a transaction that talks to the network, or that
// reads somebody else's bytes for the first time, holds SERIALIZABLE locks for the duration of
// somebody else's timeout or for as long as it takes to discover that the payload is rubbish.
//
// The one thing that IS encoded in here is the amended report, at the last statement: what the
// write dropped is only known once the writing is done, and a report stamped outside the
// transaction could describe an attempt that rolled back. It encodes a message this process built,
// never parses one, and it happens after every read this transaction needed.
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

	// style_number is read for the REPORT, not for the write: a dropped assembly line has to name
	// its component the way the resolver's own lines do — by number, which is what is printed on the
	// card — rather than by an id that means nothing outside this database.
	archiveImportComponentFactsQuery = `SELECT id, purpose, style_number FROM tech_card WHERE id IN (:ids)`

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
	// explains them. It is READ HERE, outside the transaction — MySQL would refuse the JSON column
	// with a raw 3140 from the middle of the last statement, after every write above it, and a
	// payload that is not a report at all has to be refused before a single row exists.
	//
	// Parsed rather than merely checked for well-formedness, because the transaction below does not
	// stamp these bytes: it stamps them PLUS its own losses (see importLosses). The report the
	// operator reads has to be true about what was written, and half of what was written is decided
	// after the dry run answered.
	baseReport, err := techcardarchive.ParseReport(in.Report)
	if err != nil {
		return 0, fmt.Errorf("tech card import %s: %w", importID, err)
	}

	card := in.Card
	s.prepareImportedCard(card)

	if err := s.ensureDictionaryFresh(ctx, "archive import"); err != nil {
		return 0, err
	}

	var newID int
	var losses *importLosses
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		newID = 0 // a retried attempt must not inherit the rolled-back one's id
		// ... and must not inherit its losses either: a retry re-runs every drop below, so a
		// collector carried over would report each dropped row once per attempt.
		losses = newImportLosses()

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

		if err := writeImportedStyleFacts(ctx, db, newID, in.Style, rng, losses); err != nil {
			return err
		}
		if err := insertImportedSizeChart(ctx, db, newID, in.SizeChart, rng, losses); err != nil {
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
		if err := insertImportedPieceAreas(ctx, db, newID, importID, in.PieceAreas, rng, losses); err != nil {
			return err
		}
		if err := insertImportedAssembly(ctx, db, newID, in.Assembly, in.Actor, rng, losses); err != nil {
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

		// THE REPORT IS AMENDED HERE AND STAMPED IN THE SAME BREATH. Everything above has finished
		// dropping rows, and nothing below writes one — so this is the first moment at which the
		// report can be true and the last at which it can still be written inside the transaction
		// that made it true.
		report, err := losses.stamp(baseReport)
		if err != nil {
			return err
		}
		return finishTechCardImportRow(ctx, db, importID, newID, report)
	})
	if err != nil {
		return 0, fmt.Errorf("can't import tech card archive %s: %w", importID, err)
	}
	// Logged once, after the transaction survived, and only as a count: the lines themselves are on
	// the card now, where the operator reads them. A per-row warning during the write would say the
	// same thing several times over on a deadlock retry and would still not reach anybody.
	if losses != nil && len(losses.holes) > 0 {
		slog.Default().InfoContext(ctx, "tech card import: the write dropped rows the dry run counted as imported",
			slog.String("import_id", importID), slog.Int("tech_card_id", newID),
			slog.Int("report_lines_added", len(losses.holes)))
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

// ────────────────────────────── what the WRITE itself dropped ──────────────────────────────

// importLosses is the TRANSACTION's half of the import report.
//
// WHY IT EXISTS. in.Report is the dry run's answer — the resolver's holes and the resolver's tally,
// both produced before this transaction opened. Everything below still drops rows: a measurement
// cell filed under a size this card does not make, a grade rule authored from one, a measured area
// that is not a number, an assembly line whose component is not auxiliary in this base. Until this
// type existed those rows went to slog and the report stamped on the card went on counting them as
// imported — so the operator read a clean report about a card with holes in it, once, and believed
// it.
//
// AND THE CASE IS NOT EXOTIC. A size can be perfectly present in this base's DICTIONARY — so the
// resolver mapped it and counted it — and absent from the imported card's own size range, because
// a card makes eight of the dictionary's twenty sizes. Every row filed under it is dropped here.
//
// TWO KINDS OF LOSS, and the difference is whether the dry run counted the row:
//
//   - drop() writes a LINE only. The eight counted entities count sizes, sheets, markers, BOM and
//     assembly lines — not chart cells, not measured areas, not the card's own «model wears» field.
//     Moving a counter for one of those would claim a size or a sheet did not import when it did.
//   - dropCounted() writes a line AND moves one row out of the imported column. It is for rows the
//     resolver planned and tallied: today that is the assembly bill, whose imported count is
//     exactly the number of lines this transaction was handed.
//
// Lines are deduplicated by (entity, ref, reason) the way the resolver deduplicates its own, so one
// out-of-range size does not produce a line per measurement it appears in. COUNTERS ARE NOT: two
// rows lost for one reason are two rows, and the report's own contract says the tally and the lines
// are never derived from each other.
type importLosses struct {
	holes []techcardarchive.ImportHole
	seen  map[string]bool
	// lost is a MOVE, not a tally: see (*ImportReport).Amend. What is counted here leaves the
	// imported column of the stamped report.
	lost techcardarchive.Counters
}

func newImportLosses() *importLosses {
	return &importLosses{seen: make(map[string]bool), lost: techcardarchive.NewCounters()}
}

// stampedReport is the bytes finishTechCardImportRow writes, and the type exists to make the defect
// this file was carrying UNSPELLABLE.
//
// in.Report is a []byte. It fitted the old stamping parameter perfectly, so stamping the dry run's
// answer as a description of the write was not a mistake anybody could see — it was the shortest
// thing to write. Only stamp() below returns this type, so reaching the stamp now means passing the
// write's losses through the report package first; handing it the raw payload instead takes a
// visible conversion that says what it is doing.
type stampedReport []byte

// stamp folds the transaction's losses into the dry run's report and hands back what may be stored.
func (l *importLosses) stamp(base *techcardarchive.ImportReport) (stampedReport, error) {
	b, err := base.Amend(l.holes, l.lost)
	if err != nil {
		return nil, fmt.Errorf("amend the import report with what the write dropped: %w", err)
	}
	return stampedReport(b), nil
}

// drop records one row the write did not keep. The argument order mirrors the resolver's own hole()
// so the two read alike side by side.
func (l *importLosses) drop(entityName, ref, status string, reason techcardarchive.Reason, detail string) {
	key := entityName + "|" + ref + "|" + string(reason)
	if l.seen[key] {
		return
	}
	l.seen[key] = true
	l.holes = append(l.holes, techcardarchive.ImportHole{
		Entity: entityName, Ref: ref, Status: status, Reason: reason, Detail: detail,
	})
}

// dropCounted records a row the DRY RUN counted as imported: the line is deduplicated, the counter
// move is not.
func (l *importLosses) dropCounted(entityName, ref, status string, reason techcardarchive.Reason, detail string) {
	switch status {
	case techcardarchive.StatusDegraded:
		l.lost.AddDegraded(entityName, 1)
	default:
		l.lost.AddSkipped(entityName, 1)
	}
	l.drop(entityName, ref, status, reason, detail)
}

// importedSizeRef names a size the way every other size line of the report names one: by NAME when
// the dictionary has it, because that is what the operator reads on the card, and by id otherwise.
// The dictionary was refreshed before the transaction opened (ensureDictionaryFresh), and a name it
// cannot produce is not worth a second statement inside a SERIALIZABLE transaction.
func importedSizeRef(sizeID int) string {
	if s, ok := cache.GetSizeById(sizeID); ok && strings.TrimSpace(s.Name) != "" {
		return "size_name=" + strings.TrimSpace(s.Name)
	}
	return fmt.Sprintf("size_id=%d", sizeID)
}

// ────────────────────────────── the journal sentence ──────────────────────────────

// Provenance caps, in RUNES. manifest.source is free text out of a file somebody sent us: the
// reader accepts a manifest of up to 16 MiB and neither of these two fields has a length of its
// own, while the sentence they are spelled into lands in change_note — a TEXT column (0067) that
// holds 65 535 BYTES. A manifest that is perfectly valid to the reader, carrying a host of ~70 KiB,
// would therefore fail the LAST statement of the import with «Data too long» and roll back the card
// and everything under it; without strict mode it would be cut in half in silence instead.
//
// Each cap is the longest a REAL value can be, so clamping can only ever hit one that named nothing
// on either side: a style number that does not fit tech_card.style_number (VARCHAR(255), 0067)
// names no card here or there, and a host longer than a fully-qualified DNS name (253 characters,
// RFC 1035 §2.3.4) names no server. Both together, at four bytes per rune, come to about 2 KiB —
// two per cent of what the column holds, with the rest left to the sentence itself.
const (
	importProvenanceStyleRunes = 255
	importProvenanceHostRunes  = 253
)

// importedFromArchiveSummary is the journal sentence. It names the source style and the host it
// came from, which is the whole point of the entry: months later, «why does this card have gaps»
// has an answer that does not depend on the tech_card_import row surviving.
//
// It is also the LAST channel by which a stranger's free text reaches a permanent record of ours —
// everything else the archive carries is either matched against a dictionary here or dropped with a
// line in the report — so both fields are clamped before they are spelled in. See clampProvenance
// for what that means beyond length.
func importedFromArchiveSummary(in entity.TechCardArchiveImport) string {
	style := clampProvenance(in.SourceStyleNumber, importProvenanceStyleRunes)
	if style == "" {
		style = "an unnumbered style"
	}
	host := clampProvenance(in.SourceHost, importProvenanceHostRunes)
	if host == "" {
		host = "an unnamed host"
	}
	return fmt.Sprintf("imported from archive %s of %s", style, host)
}

// clampProvenance makes one archive-supplied string fit to stand in a record a human reads.
//
// FOUR THINGS, and none of them is decoration:
//
//   - INVALID UTF-8 IS DROPPED. MySQL refuses a string with a broken sequence in it (1366) from the
//     middle of the statement, which on this path means the whole import rolls back at its last
//     step over a byte in a file's metadata.
//   - RUNES OUTSIDE THE BMP BECOME U+FFFD. The same 1366 waits for a four-byte rune on a utf8mb3
//     column, and this schema's charset is the database default — utf8mb4 on beta and in the tests,
//     utf8mb3 on production. A replacement character is visible; a failed import at the last
//     statement is a mystery.
//   - CONTROL CHARACTERS BECOME SPACES and runs of whitespace collapse. A journal entry is read on
//     a screen next to other entries, and a host carrying newlines or an escape sequence would
//     rearrange that screen.
//   - THE CUT IS AT A RUNE BOUNDARY AND IT IS SAID. Slicing bytes would leave half a rune, which is
//     the invalid UTF-8 of the first point; the ellipsis is part of the stored sentence, because a
//     truncation nobody can see reads exactly like a short name.
func clampProvenance(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r > 0xFFFF:
			return utf8.RuneError
		case unicode.IsControl(r):
			return ' '
		default:
			return r
		}
	}, s)
	s = strings.Join(strings.Fields(s), " ")

	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	kept := 0
	for i := range s { // ranging a string steps rune by rune, so i is always a boundary
		if kept == maxRunes {
			return s[:i] + "…"
		}
		kept++
	}
	return s
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
func finishTechCardImportRow(ctx context.Context, db dependency.DB, importID string, techCardID int,
	report stampedReport) error {
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

// GetTechCardImportByImportID returns one upload row by its import_id — the dialogue's state
// between the dry run and the commit. sql.ErrNoRows when there is none (NOT_FOUND upstream).
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
	f entity.TechCardArchiveStyleFacts, rng storeutil.TechCardSizeRange, lost *importLosses) error {
	modelWearsSize := importedModelWearsSize(f.ModelWearsSizeId, rng, lost)
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

// importedModelWearsSize decides the card's «model wears» reference and reports it when it goes.
//
// «The model wears a size this style does not make» is either a foreign id worn as a local one or a
// fact about nothing. Cleared rather than refused, on the same principle the season clone applies to
// a grade base outside the range: one display line is not worth failing an import over.
//
// The line it leaves is entity CARD, degraded — the card landed, one fact thinner — which is the
// same shape the resolver gives a card that lost its category. It moves no counter: the size counter
// counts the card's SIZES, and the size itself is imported and fine; it is this reference to it that
// is not.
func importedModelWearsSize(sizeID sql.NullInt32, rng storeutil.TechCardSizeRange, lost *importLosses) sql.NullInt32 {
	if !sizeID.Valid || sizeID.Int32 <= 0 {
		return sql.NullInt32{} // 0 is «unset» across the whole contract, never size zero
	}
	if rng.Has(int(sizeID.Int32)) {
		return sizeID
	}
	lost.drop(techcardarchive.EntityCard, importedSizeRef(int(sizeID.Int32)),
		techcardarchive.StatusDegraded, techcardarchive.ReasonSizeUnknown,
		"the archive says the model wears this size, which the imported card does not make; the card "+
			"imported without the reference")
	return sql.NullInt32{}
}

// ────────────────────────────── size chart ──────────────────────────────

// insertImportedSizeChart writes the measurement grid and the grade rule it was authored from.
//
// Both axes arrive already resolved against THIS base's dictionaries (sizes and measurement names
// travel by name and are looked up by the resolver), so nothing is created here — an import that
// quietly added a measurement name because one archive spelled it differently would corrupt every
// other style's chart with it.
//
// A cell whose size is outside the imported card's own range is DROPPED, not refused, and REPORTED.
// That is the season clone's rule (its carry-over intersects with the clone's size range in SQL)
// and the resolver's principle in one: a missing reference degrades, and one measurement is not
// worth failing an import over. It can only happen on a malformed archive, where the manifest's
// size map and the chart's names disagree — or on a perfectly ordinary one whose size exists in
// this base's dictionary but not in this card's range.
//
// The decisions are two pure functions and the statements are three, because the decisions are what
// can be wrong: everything this transaction drops has to reach the report, and that is testable
// without a database only if choosing and writing are separate.
func insertImportedSizeChart(ctx context.Context, db dependency.DB, id int,
	chart entity.StyleSizeChart, rng storeutil.TechCardSizeRange, lost *importLosses) error {
	if rows := importedChartCellRows(id, chart, rng, lost); len(rows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_size_measurement", rows); err != nil {
			return fmt.Errorf("insert imported size chart of tech card %d: %w", id, err)
		}
	}

	base, stepRows := importedGradeRule(id, chart, rng, lost)
	if len(stepRows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_grade_rule", stepRows); err != nil {
			return fmt.Errorf("insert imported grade rule of tech card %d: %w", id, err)
		}
	}
	if base > 0 {
		if err := storeutil.ExecNamed(ctx, db, archiveImportGradeBaseQuery,
			map[string]any{"id": id, "base": nullableID(base)}); err != nil {
			return fmt.Errorf("write imported grade base of tech card %d: %w", id, err)
		}
	}
	return nil
}

// importedChartCellRows keeps the cells this card can actually hold and reports the rest.
//
// ONE LINE PER OFFENDING SIZE, not per cell: a chart is sizes × measurements, so a single size the
// card does not make would otherwise put twenty identical lines in front of the operator. The count
// goes into the detail, where it is a fact rather than a repetition.
func importedChartCellRows(id int, chart entity.StyleSizeChart,
	rng storeutil.TechCardSizeRange, lost *importLosses) []map[string]any {
	rows := make([]map[string]any, 0, len(chart.Cells))
	outOfRange := make(map[int]int, 4)
	var noSize, noMeasurement int
	for _, c := range chart.Cells {
		switch {
		case c.MeasurementNameID <= 0:
			noMeasurement++
		case c.SizeID <= 0:
			noSize++
		case !rng.Has(c.SizeID):
			outOfRange[c.SizeID]++
		default:
			rows = append(rows, map[string]any{
				"tech_card_id":        id,
				"size_id":             c.SizeID,
				"measurement_name_id": c.MeasurementNameID,
				"measurement_value":   c.Value,
			})
		}
	}

	for _, sizeID := range sortedIntKeys(outOfRange) {
		lost.drop(techcardarchive.EntitySize, importedSizeRef(sizeID),
			techcardarchive.StatusSkipped, techcardarchive.ReasonSizeUnknown,
			fmt.Sprintf("%s filed under a size the imported card does not make; %s dropped and the rest "+
				"of the chart imported", rowsPhrase(outOfRange[sizeID], "size chart row"),
				theyOrIt(outOfRange[sizeID])))
	}
	if noMeasurement > 0 {
		lost.drop(techcardarchive.EntityMeasurement, "size_chart.measurement_name_id=0",
			techcardarchive.StatusSkipped, techcardarchive.ReasonMeasurementUnknown,
			fmt.Sprintf("%s carrying no measurement at all, which addresses nothing; %s dropped",
				rowsPhrase(noMeasurement, "size chart row"), theyOrIt(noMeasurement)))
	}
	if noSize > 0 {
		lost.drop(techcardarchive.EntitySize, "size_chart.size_id=0",
			techcardarchive.StatusSkipped, techcardarchive.ReasonSizeUnknown,
			fmt.Sprintf("%s carrying no size at all, which addresses nothing; %s dropped",
				rowsPhrase(noSize, "size chart row"), theyOrIt(noSize)))
	}
	return rows
}

// importedGradeRule decides the grade rule AS A WHOLE — the base size and the per-measurement steps
// authored against it — and returns nothing to write when it cannot have both.
//
// THE INVARIANT IS «BOTH HALVES OR NEITHER», and it used to break exactly here. The steps were
// inserted first and the base was range-checked afterwards, so a base outside the imported card's
// range left the steps standing on their own: a step is «this measurement grows by 2 cm per size
// away from the base», and away from WHICH size is then unanswerable. Half a rule is not a thinner
// rule — it reads on the card exactly like one somebody authored, and nothing downstream can tell
// the difference. So the range check happens before a single step row exists.
//
// A rule that arrives with steps and NO base at all is left as it is: the resolver holds the same
// invariant on the way in, and dropping data the archive carried on the strength of a state it says
// cannot arrive would be the more expensive mistake.
func importedGradeRule(id int, chart entity.StyleSizeChart,
	rng storeutil.TechCardSizeRange, lost *importLosses) (int, []map[string]any) {
	stepRows := make([]map[string]any, 0, len(chart.GradeSteps))
	unaddressed := 0
	for _, g := range chart.GradeSteps {
		if g.MeasurementNameID <= 0 {
			unaddressed++
			continue
		}
		stepRows = append(stepRows, map[string]any{
			"tech_card_id":        id,
			"measurement_name_id": g.MeasurementNameID,
			"step":                g.Step,
		})
	}
	if unaddressed > 0 {
		lost.drop(techcardarchive.EntityMeasurement, "grade_step.measurement_name_id=0",
			techcardarchive.StatusSkipped, techcardarchive.ReasonMeasurementUnknown,
			fmt.Sprintf("%s naming no measurement, which addresses nothing; %s dropped",
				rowsPhrase(unaddressed, "grade step"), theyOrIt(unaddressed)))
	}

	base := chart.GradeBaseSizeID
	if base > 0 && !rng.Has(base) {
		detail := "the size chart's grade rule is authored from a size the imported card does not make, " +
			"so the rule was dropped whole"
		if len(stepRows) > 0 {
			detail += fmt.Sprintf(" — the base and the %s with it, because a step without its base "+
				"reads as an authored rule", rowsPhrase(len(stepRows), "step"))
		}
		lost.drop(techcardarchive.EntitySize, importedSizeRef(base),
			techcardarchive.StatusSkipped, techcardarchive.ReasonSizeUnknown, detail)
		return 0, nil
	}
	return base, stepRows
}

// sortedIntKeys puts the report's lines in a fixed order: a report that shuffles between two runs of
// the same archive cannot be diffed by the person reading it.
func sortedIntKeys(m map[int]int) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// rowsPhrase and theyOrIt keep a detail line a sentence rather than a template with a «(s)» in it.
func rowsPhrase(n int, noun string) string {
	if n == 1 {
		return "one " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func theyOrIt(n int) string {
	if n == 1 {
		return "it was"
	}
	return "they were"
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
// A DROPPED AREA IS A REPORTED AREA, under entity `pattern`. The vocabulary of FORMAT.md §7 is
// closed and has no word for a measured contour, and `pattern` is the true one of the twelve: the
// areas describe pattern geometry, and the button that fixes a hole in them is on the patterns tab.
// It moves NO counter — the pattern counter counts SHEETS, and a sheet whose areas were dropped
// still imported.
func insertImportedPieceAreas(ctx context.Context, db dependency.DB, techCardID int, importID string,
	areas []entity.TechCardArchivePieceArea, rng storeutil.TechCardSizeRange, lost *importLosses) error {
	rows, err := importedPieceAreaRows(techCardID, importID, areas, rng, lost)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := storeutil.ExecNamed(ctx, db, archiveImportPieceAreaInsertQuery, row); err != nil {
			return fmt.Errorf("insert imported piece area %q of tech card %d: %w", row["piece"], techCardID, err)
		}
	}
	return nil
}

// importedPieceAreaRows keeps the measurements this card can hold and reports the rest. It refuses
// only on a DUPLICATE, which is the archive contradicting itself rather than the target missing a
// reference — the row it would collide with is in the same file.
func importedPieceAreaRows(techCardID int, importID string, areas []entity.TechCardArchivePieceArea,
	rng storeutil.TechCardSizeRange, lost *importLosses) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(areas))
	// UNIQUE (tech_card_id, scope_key, piece_line_key, size_key) — a duplicate in the archive would
	// otherwise arrive as a bare 1062 with no hint of which row it was.
	seen := make(map[[3]string]bool, len(areas))
	for _, a := range areas {
		scope := strings.TrimSpace(a.ScopeKey)
		piece := importedLineKey(a.PieceLineKey)
		if scope == "" || piece == "" {
			lost.drop(techcardarchive.EntityPattern, importedPieceAreaRef(a.ScopeKey, a.PieceLineKey),
				techcardarchive.StatusSkipped, techcardarchive.ReasonPatternInvalid,
				"the archive measures an area that names no fabric scope or no cut piece, so it "+
					"addresses nothing; the row was dropped. Recount the areas from the sheets")
			continue
		}
		// chk_tcpa_area_positive refuses a non-positive area at the schema level; caught here so a
		// corrupt row costs one report line rather than the whole import.
		if !a.AreaCm2.IsPositive() {
			lost.drop(techcardarchive.EntityPattern, importedPieceAreaRef(scope, a.PieceLineKey),
				techcardarchive.StatusSkipped, techcardarchive.ReasonPatternInvalid,
				fmt.Sprintf("the archive's measured area for this piece is %s, which is not an area; "+
					"the row was dropped and the piece has no measured geometry here. Recount the "+
					"areas from the sheets", a.AreaCm2.String()))
			continue
		}
		// THE MEASUREMENT'S DATE HAS TO BE STORABLE. parsed_at is a TIMESTAMP NOT NULL (0297) and
		// MySQL's TIMESTAMP range starts one second AFTER the Unix epoch — while the zero value of
		// a protobuf Timestamp IS the epoch. An archive that simply left the field unset would
		// therefore reach the driver as 1292 from the middle of this loop, with the card and every
		// child already written and nothing on screen but a MySQL error code.
		//
		// DROPPED RATHER THAN RE-DATED, and that is this file's own rule rather than a preference:
		// parsed_by and parsed_at are the SOURCE's and are stored as they stand, because who
		// measured this geometry and when is a fact about the measurement. Stamping today's date on
		// it would claim a measurement nobody took, and stamping 1970 would claim one nobody could
		// have. The resolver has already tried the one honest substitute it has (manifest's export
		// date), so a value that still does not fit has no replacement left that is true.
		if !fitsMySQLTimestamp(a.ParsedAt) {
			lost.drop(techcardarchive.EntityPattern, importedPieceAreaRef(scope, a.PieceLineKey),
				techcardarchive.StatusSkipped, techcardarchive.ReasonPatternInvalid,
				fmt.Sprintf("the archive dates this measurement %s, which is not a date this base can "+
					"store, and re-dating it would claim a measurement nobody took; the row was "+
					"dropped. Recount the areas from the sheets",
					a.ParsedAt.UTC().Format("2006-01-02 15:04:05")))
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
			lost.drop(techcardarchive.EntityPattern, importedSizeRef(int(sizeID.Int64)),
				techcardarchive.StatusSkipped, techcardarchive.ReasonSizeUnknown,
				"the archive measures piece areas for a size the imported card does not make; those "+
					"rows were dropped and the card states no cloth norm for that size")
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
			return nil, entity.NewFieldViolation("piece_areas", "duplicate",
				fmt.Sprintf("%s / %s / size %s", scope, piece, areaSizeKey),
				"the archive measures the same piece twice in one fabric scope and size")
		}
		seen[dedupe] = true

		rows = append(rows, map[string]any{
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
		})
	}
	return rows, nil
}

// MySQL's TIMESTAMP range as UTC instants: '1970-01-01 00:00:01' through '2038-01-19 03:14:07'. It
// begins ONE SECOND after the Unix epoch, and that second is what makes an unset protobuf Timestamp
// — which is exactly the epoch — a value the column refuses rather than a value it stores oddly.
var (
	mysqlTimestampMin = time.Date(1970, 1, 1, 0, 0, 1, 0, time.UTC)
	mysqlTimestampMax = time.Date(2038, 1, 19, 3, 14, 7, 0, time.UTC)
)

// fitsMySQLTimestamp reports whether an instant the ARCHIVE supplied can go into a TIMESTAMP NOT
// NULL column at all.
//
// It is a predicate rather than three lines at the one call site because it is a property of the
// COLUMN TYPE, not of piece areas: every instant this file takes from an archive and writes into a
// TIMESTAMP passes through here, so the next such column cannot be written without the check. Today
// there is exactly one — tech_card_piece_area.parsed_at (0297).
func fitsMySQLTimestamp(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	u := t.UTC()
	return !u.Before(mysqlTimestampMin) && !u.After(mysqlTimestampMax)
}

// importedPieceAreaRef names a measured row in the report the way its own file names it: by the
// piece's stable key, with the fabric scope beside it, because one piece is measured once per scope.
func importedPieceAreaRef(scope, pieceLineKey string) string {
	piece := strings.TrimSpace(pieceLineKey)
	if piece == "" {
		piece = "?"
	}
	if s := strings.TrimSpace(scope); s != "" {
		return fmt.Sprintf("piece_line_key=%s scope_key=%s", piece, s)
	}
	return "piece_line_key=" + piece
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
// size-scoped line must name a size this card makes. A line failing either is dropped rather than
// refused, the season clone's rule for exactly this data.
//
// THIS IS THE ONE PLACE A COUNTER MOVES. The resolver counted every line it planned as imported —
// `assembly.imported` is exactly the length of the slice handed in here — so a line dropped below
// was counted as landed by the report the operator will read. It leaves the imported column and
// enters skipped, one row at a time, next to the line that says why.
func insertImportedAssembly(ctx context.Context, db dependency.DB, techCardID int,
	items []entity.StyleAssemblyInsert, actor string, rng storeutil.TechCardSizeRange,
	lost *importLosses) error {
	if len(items) == 0 {
		return nil
	}
	componentIDs := make([]int, 0, len(items))
	for _, it := range items {
		if it.ComponentTechCardId > 0 {
			componentIDs = append(componentIDs, it.ComponentTechCardId)
		}
	}
	facts := make(map[int]importedComponentFacts, len(componentIDs))
	if len(componentIDs) > 0 {
		rows, err := storeutil.QueryListNamed[importedComponentFacts](ctx, db,
			archiveImportComponentFactsQuery, map[string]any{"ids": componentIDs})
		if err != nil {
			return fmt.Errorf("load imported assembly components: %w", err)
		}
		for _, r := range rows {
			facts[r.Id] = r
		}
	}

	for _, it := range importedAssemblyLines(techCardID, items, facts, rng, lost) {
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

// importedComponentFacts is what the TARGET knows about a component the archive named: whether it
// is an auxiliary card here, and the number an operator recognises it by.
//
// The style number is read for the REPORT and for nothing else. The resolver's own assembly lines
// say `component_style_number=…`, and a line from this side saying `component_tech_card_id=41`
// about the same bill would send the operator looking up an id that means nothing on the card.
type importedComponentFacts struct {
	Id          int    `db:"id"`
	Purpose     string `db:"purpose"`
	StyleNumber string `db:"style_number"`
}

// importedAssemblyLines keeps the assembly lines this base will accept and reports the rest.
//
// Every refusal is the TARGET's, which is why none of them is a corrupt archive and none of them
// takes the import down: the component may be a perfectly good card here that simply is not an
// auxiliary one, or the line may name a size this particular card does not make.
func importedAssemblyLines(techCardID int, items []entity.StyleAssemblyInsert,
	facts map[int]importedComponentFacts, rng storeutil.TechCardSizeRange,
	lost *importLosses) []entity.StyleAssemblyInsert {
	out := make([]entity.StyleAssemblyInsert, 0, len(items))
	seen := make(map[[2]int]bool, len(items))
	for _, it := range items {
		f, known := facts[it.ComponentTechCardId]
		detail, reason := "", techcardarchive.ReasonAssemblyComponentNotFound
		switch {
		case it.ComponentTechCardId <= 0:
			detail = "the assembly line names no component at all, so there is nothing to attach"
		case it.ComponentTechCardId == techCardID:
			detail = "the assembly line names the imported card itself, and a style cannot be its own " +
				"assembly component"
		case !it.Qty.IsPositive():
			detail = fmt.Sprintf("the assembly line asks for a quantity of %s, which is not a quantity",
				it.Qty.String())
		case !known:
			// The resolver matched this component against THIS base, so its absence now means the
			// card went away between the dry run and the commit. Said as it is rather than folded
			// into the purpose check below, which would tell the operator to change the purpose of
			// a card that is not there.
			detail = "the component the archive named is not in this base any more; it was matched " +
				"when the archive was read and gone by the time the card was written"
		case entity.TechCardPurpose(f.Purpose) != entity.TechCardPurposeAuxiliary:
			detail = "a card with this number exists here but is not an AUXILIARY one, and only " +
				"auxiliary cards can be assembly components"
		// A non-positive size key means «all sizes» — not size zero — and is always in range.
		case sizeKey(it) > 0 && !rng.Has(sizeKey(it)):
			// The size itself is fine — the resolver found it in this base's dictionary — it is the
			// imported CARD that does not make it, so the line describes labels for a garment that
			// does not exist. size_unknown is the closest the closed dictionary comes; the detail
			// says which of the two dictionaries actually disagreed.
			reason = techcardarchive.ReasonSizeUnknown
			detail = fmt.Sprintf("the assembly line is filed under %s, which the imported card does not "+
				"make", importedSizeRef(sizeKey(it)))
		case seen[[2]int{it.ComponentTechCardId, sizeKey(it)}]:
			detail = "the archive lists this component twice for the same size; the second line was " +
				"dropped and the first imported"
		}
		if detail != "" {
			lost.dropCounted(techcardarchive.EntityAssembly,
				importedAssemblyRef(f, it.ComponentTechCardId),
				techcardarchive.StatusSkipped, reason, detail)
			continue
		}
		seen[[2]int{it.ComponentTechCardId, sizeKey(it)}] = true
		out = append(out, it)
	}
	return out
}

// importedAssemblyRef names a component the way the resolver's own assembly lines name one.
func importedAssemblyRef(f importedComponentFacts, componentID int) string {
	if n := strings.TrimSpace(f.StyleNumber); n != "" {
		return "component_style_number=" + n
	}
	return fmt.Sprintf("component_tech_card_id=%d", componentID)
}
