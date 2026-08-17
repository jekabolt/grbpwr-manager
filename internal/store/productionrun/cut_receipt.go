package productionrun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ПРИЁМКА КРОЯ (Ф5б.5, migration 0287). Two numbers per pair (настил, размер): сколько ВЫКРОЕНО off
// the lay, and сколько ПРИНЯТО В ПОШИВ. They are inserted BETWEEN planned_qty and received_qty of
// the run's own grid — see entity/production_cut_receipt.go for the chain, for why re-meaning the
// existing fields would open a double ledger, and for why the run's own numbers are deliberately
// absent from a receipt row rather than joined onto it.
//
// FOUR DECISIONS LIVE IN THIS FILE, and the first one is the one to read before anything else.
//
//  1. A TERMINAL RUN STILL ACCEPTS A CUT RECEIPT. lockRunForLay refuses received/closed/cancelled
//     runs because a настил is a PLAN, and rewriting the plan of a finished run rewrites history. A
//     cut receipt is the OPPOSITE KIND OF OBJECT: it is a report of what physically happened at the
//     cutting table, and cutting always precedes sewing, which precedes receiving. Requiring a
//     non-terminal run would mean the fact can only ever be entered while it is still uncertain and
//     never corrected once it is known — and the ordinary sequence in a workshop is cut → sew →
//     receive → reconcile the paperwork. The cancelled run is the sharpest case of all: the cloth
//     was cut and THEN the run was cancelled, so the cut cloth is exactly the loss somebody has to
//     account for, and a refusal here would mean only successful runs can have their cutting losses
//     recorded. There is no status guard of any kind, in either direction: refusing a receipt on a
//     run still in `planned` would be the same mistake read backwards, since the status moves when a
//     human moves it and the cutting table does not wait for that.
//
//  2. THE WRITE IS AN UPSERT BY PAIR, NOT A REPLACE. A pair the payload does not mention is left
//     alone; only DeleteCutReceipt removes one. Deliberately the opposite of the настил's sections,
//     where the submitted list IS the lay: the cutting room reports the sizes that came off the
//     table today, and reading its silence about the rest as «those sizes were never cut» would
//     delete yesterday's count.
//
//  3. THE NUMBERS ARE NOT CHECKED AGAINST EACH OTHER. Not against each other, not against
//     planned_qty, not against the настил's quantity snapshot. «Заказано 10, выкроено 12, принято
//     10» is legitimate overcutting and «выкроено 12, принято 9» is legitimate cutting scrap; a
//     refusal would leave the operator unable to state what actually happened.
//
//  4. production_run_line IS NEVER READ AND NEVER WRITTEN ON THIS PATH — not in an UPDATE, not even
//     in a JOIN. The chain is assembled by the client from the run lines it already holds, because a
//     colourway legitimately has several настилы and copying its planned_qty onto each of their
//     receipt rows would make any sum over those rows double the order.

// cutReceiptRunContext is what the run must tell a receipt write: which card it makes, and whether
// that card is the kind of thing настилы exist for.
//
// IT DOES NOT CARRY THE STATUS, and the query below does not even select it. A column read and then
// ignored is an invitation for the next reader to «fix» the omission by adding a guard; a column
// never asked for states the decision in the SQL itself. (layRunContext next door carries the status
// because a lay write MUST refuse a terminal run — see decision 1 above for why the two objects part
// ways here.)
type cutReceiptRunContext struct {
	TechCardId int    `db:"tech_card_id"`
	Purpose    string `db:"purpose"`
}

// resolveCutReceiptRun reads the run FOR UPDATE and applies the one guard a receipt write shares
// with a lay write.
//
// FOR UPDATE, and not a plain read, even though NO status guard follows it: the X lock on the run
// row is what serialises this write against a concurrent DeleteProductionRun (whose CASCADE would
// otherwise race the INSERT) and against a second writer of the same pair, which is what makes
// last-write-wins on a row without a lock_version an ordering rather than a coin toss.
//
// The auxiliary refusal is kept, unlike the terminal one, because it is a statement about what
// EXISTS rather than about when it may be edited: an aux card has no colourways, no cut pieces and
// no раскладки, therefore no настилы, therefore no pair to report a cut on. Answering «настил not
// found» there would send the operator looking for a lay that could never exist.
func resolveCutReceiptRun(ctx context.Context, db dependency.DB, runID int) (cutReceiptRunContext, error) {
	row, err := storeutil.QueryNamedOne[cutReceiptRunContext](ctx, db, `
		SELECT r.tech_card_id, c.purpose
		FROM production_run r
		JOIN tech_card c ON c.id = r.tech_card_id
		WHERE r.id = :id
		FOR UPDATE`, map[string]any{"id": runID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cutReceiptRunContext{}, sql.ErrNoRows
		}
		return cutReceiptRunContext{}, fmt.Errorf("failed to lock production run %d for cut receipt write: %w", runID, err)
	}
	if entity.TechCardPurpose(row.Purpose) == entity.TechCardPurposeAuxiliary {
		return cutReceiptRunContext{}, fmt.Errorf("%w: %s", entity.ErrProductionRunLayNotApplicable,
			entity.ProductionRunLayNotApplicableKey)
	}
	return row, nil
}

// resolveCutReceiptLayID addresses the настил the way the API addresses it — by (run_id, lay_key) —
// so a receipt can never be written onto another run's lay by id. The refusal reuses the lay's own
// ErrProductionRunLayNotFound: it is the same fact, and one fact deserves one error.
func resolveCutReceiptLayID(ctx context.Context, db dependency.DB, runID int, layKey string) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, db, `
		SELECT id FROM production_run_lay WHERE run_id = :run AND lay_key = :key`,
		map[string]any{"run": runID, "key": layKey})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: lay %q of run %d", entity.ErrProductionRunLayNotFound, layKey, runID)
		}
		return 0, fmt.Errorf("failed to resolve lay %q of run %d for cut receipt: %w", layKey, runID, err)
	}
	return row.Id, nil
}

// SaveCutReceipt upserts the cutting receipt of ONE pair (настил, размер) and returns the stored row
// exactly as ListCutReceipts would return it.
func (s *Store) SaveCutReceipt(ctx context.Context, runID int, layKey string,
	ins entity.ProductionRunCutReceiptInsert, username string) (entity.ProductionRunCutReceipt, error) {

	layKey = strings.TrimSpace(layKey)
	if layKey == "" {
		return entity.ProductionRunCutReceipt{}, entity.NewFieldViolation("lay_key", "required", "",
			"address the lay by the stable key it was created with")
	}
	if err := validateCutReceiptInsert(&ins); err != nil {
		return entity.ProductionRunCutReceipt{}, err
	}

	var saved entity.ProductionRunCutReceipt
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()

		run, err := resolveCutReceiptRun(ctx, db, runID)
		if err != nil {
			return err
		}
		layID, err := resolveCutReceiptLayID(ctx, db, runID, layKey)
		if err != nil {
			return err
		}

		// The size must be one the STYLE grades. This is not a plan check — the run's own planned
		// quantities are deliberately never consulted (decisions 3 and 4) — it is the check every
		// size-scoped child of a style makes: size(id) is the global dictionary, so the FK proves only
		// that the size exists somewhere in the world. A receipt for a size this card has no pattern
		// and no раскладка for could not describe anything that happened at a table. A size the RUN
		// did not plan, by contrast, is accepted: cutting a size nobody ordered is a discrepancy to
		// show, not an input to refuse.
		sizes, err := storeutil.LoadTechCardSizeRange(ctx, db, run.TechCardId)
		if err != nil {
			return err
		}
		if err := sizes.Require("receipt.size_id", ins.SizeId); err != nil {
			return err
		}

		// EXISTENCE BY SELECT, NEVER BY RowsAffected. The DSN carries no clientFoundRows, so the
		// driver counts rows CHANGED: re-saving a byte-identical row reports 0 and would read as a
		// phantom 404 — the trap named in techcard/markers.go:158-162.
		var storedID int
		row, err := storeutil.QueryNamedOne[struct {
			Id int `db:"id"`
		}](ctx, db, `
			SELECT id FROM production_run_cut_receipt WHERE lay_id = :lay AND size_id = :size`,
			map[string]any{"lay": layID, "size": ins.SizeId})
		switch {
		case err == nil:
			storedID = row.Id
		case errors.Is(err, sql.ErrNoRows):
			// A pair the настил has not been counted for yet is a create, not an error.
		default:
			return fmt.Errorf("failed to resolve cut receipt of lay %d size %d: %w", layID, ins.SizeId, err)
		}

		if storedID > 0 {
			// Rewritten unconditionally, even when no number moved. A настил skips a no-op section
			// update to protect its stale badge and to save a write on a note-only save; a receipt has
			// neither concern, and «who last confirmed these counts» is itself a fact worth keeping in
			// updated_by.
			if err := updateCutReceipt(ctx, db, storedID, ins, username); err != nil {
				return err
			}
		} else {
			storedID, err = insertCutReceipt(ctx, db, layID, ins, username)
			if err != nil {
				return err
			}
		}

		// ONE READ PATH: the answer is rebuilt by the loader a list would use, narrowed to this row,
		// so a save and the list that follows it cannot disagree about a single field.
		rows, err := loadCutReceipts(ctx, db, runID, storedID)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("saved cut receipt %d of run %d disappeared from its own transaction", storedID, runID)
		}
		saved = rows[0]
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows),
			errors.Is(err, entity.ErrProductionRunLayNotApplicable),
			errors.Is(err, entity.ErrProductionRunLayNotFound),
			errors.Is(err, entity.ErrProductionRunCutReceiptConflict):
			return entity.ProductionRunCutReceipt{}, err
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return entity.ProductionRunCutReceipt{}, err
		}
		slog.ErrorContext(ctx, "failed to save production run cut receipt",
			slog.Int("run_id", runID), slog.String("lay_key", layKey), slog.Int("size_id", ins.SizeId),
			slog.String("err", err.Error()))
		return entity.ProductionRunCutReceipt{}, fmt.Errorf("can't save production run cut receipt: %w", err)
	}
	return saved, nil
}

// DeleteCutReceipt removes the receipt of ONE pair (настил, размер) — the way to say «this size was
// never on this настил», which zeroing the two numbers cannot say: a row of zeroes is a count of
// zero, and an absent row is the absence of a count.
//
// Existence is established by a SELECT for the same reason SaveCutReceipt does it: an answer derived
// from rows affected cannot tell «there was nothing there» from «there was nothing to change».
func (s *Store) DeleteCutReceipt(ctx context.Context, runID int, layKey string, sizeID int) error {
	layKey = strings.TrimSpace(layKey)
	if layKey == "" {
		return entity.NewFieldViolation("lay_key", "required", "",
			"address the lay by the stable key it was created with")
	}
	if sizeID <= 0 {
		return entity.NewFieldViolation("size_id", "required", "",
			"a cut receipt is addressed by the pair (lay, size)")
	}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if _, err := resolveCutReceiptRun(ctx, db, runID); err != nil {
			return err
		}
		layID, err := resolveCutReceiptLayID(ctx, db, runID, layKey)
		if err != nil {
			return err
		}
		row, err := storeutil.QueryNamedOne[struct {
			Id int `db:"id"`
		}](ctx, db, `
			SELECT id FROM production_run_cut_receipt WHERE lay_id = :lay AND size_id = :size`,
			map[string]any{"lay": layID, "size": sizeID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: size %d on lay %q of run %d",
					entity.ErrProductionRunCutReceiptNotFound, sizeID, layKey, runID)
			}
			return fmt.Errorf("failed to resolve cut receipt of lay %d size %d: %w", layID, sizeID, err)
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM production_run_cut_receipt WHERE id = :id`, map[string]any{"id": row.Id}); err != nil {
			return fmt.Errorf("failed to delete cut receipt %d: %w", row.Id, err)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows),
			errors.Is(err, entity.ErrProductionRunLayNotApplicable),
			errors.Is(err, entity.ErrProductionRunLayNotFound),
			errors.Is(err, entity.ErrProductionRunCutReceiptNotFound):
			return err
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return err
		}
		slog.ErrorContext(ctx, "failed to delete production run cut receipt",
			slog.Int("run_id", runID), slog.String("lay_key", layKey), slog.Int("size_id", sizeID),
			slog.String("err", err.Error()))
		return fmt.Errorf("can't delete production run cut receipt: %w", err)
	}
	return nil
}

// ListCutReceipts returns every cutting receipt of a run, across all of its настилы.
//
// BY RUN AND NOT BY НАСТИЛ, because the cutting room reconciles заказанное against выкроенное for
// the whole run at once, and a per-настил list would make the client assemble that reconciliation
// out of N answers.
//
// An auxiliary card is answered with an empty list rather than an explicit «not applicable». That is
// the opposite of ListLays, and the difference is real: a lay plan's emptiness reads as an
// invitation to build one, so it has to be denied in words, whereas the cutting receipt screen is
// reached THROUGH the lay plan, which has already said the word. There is no wire field for it here
// either, and inventing one the API cannot carry would be a column with no reader.
func (s *Store) ListCutReceipts(ctx context.Context, runID int) ([]entity.ProductionRunCutReceipt, error) {
	exists, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM production_run WHERE id = :id`, map[string]any{"id": runID})
	if err != nil {
		return nil, fmt.Errorf("failed to check production run %d for cut receipts: %w", runID, err)
	}
	if exists == 0 {
		// Answered explicitly rather than as an empty list: «this run does not exist» and «this run
		// has no receipts» are different sentences, and only the first one is a 404.
		return nil, sql.ErrNoRows
	}
	rows, err := loadCutReceipts(ctx, s.DB, runID, 0)
	if err != nil {
		return nil, fmt.Errorf("can't list production run cut receipts: %w", err)
	}
	return rows, nil
}

// validateCutReceiptInsert enforces in Go what the schema enforces in SQL, so a bad payload is
// refused with the offending field named instead of surfacing MySQL error 3819. It also collapses an
// all-blank note to NULL, so «no note» has ONE representation in the column rather than two.
//
// It checks NO relation between the numbers, and none against the run: see decisions 3 and 4 in the
// file header. There is no upper bound either — an invented ceiling would eventually refuse a real
// count, and the run's own planned_qty carries no such bound.
func validateCutReceiptInsert(ins *entity.ProductionRunCutReceiptInsert) error {
	if ins.SizeId <= 0 {
		return entity.NewFieldViolation("receipt.size_id", "required", "",
			"a cut receipt describes the pair (lay, size)")
	}
	if ins.CutQty < 0 {
		return entity.NewFieldViolation("receipt.cut_qty", "out_of_range", fmt.Sprintf("%d", ins.CutQty),
			"cut qty is a count of what came off the lay; it cannot be negative")
	}
	if ins.AcceptedQty < 0 {
		return entity.NewFieldViolation("receipt.accepted_qty", "out_of_range", fmt.Sprintf("%d", ins.AcceptedQty),
			"accepted into sewing is a count of what reached the sewing floor; it cannot be negative")
	}
	if ins.Note.Valid {
		if trimmed := strings.TrimSpace(ins.Note.String); trimmed == "" {
			ins.Note = sql.NullString{}
		} else {
			ins.Note.String = trimmed
		}
	}
	if ins.Note.Valid && len([]rune(ins.Note.String)) > entity.ProductionRunCutReceiptNoteMaxLen {
		return entity.NewFieldViolation("receipt.note", "too_long",
			fmt.Sprintf("%d characters", len([]rune(ins.Note.String))),
			fmt.Sprintf("keep the note under %d characters", entity.ProductionRunCutReceiptNoteMaxLen))
	}
	return nil
}

// insertCutReceipt creates the row for one pair. A duplicate key here is the create/create race —
// two writers reporting the same pair at the same instant — and it is a conflict, not an internal
// error: a reload lands the loser on an update.
func insertCutReceipt(ctx context.Context, db dependency.DB, layID int,
	ins entity.ProductionRunCutReceiptInsert, username string) (int, error) {

	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO production_run_cut_receipt
			(lay_id, size_id, cut_qty, accepted_qty, note, created_by, updated_by)
		VALUES
			(:lay_id, :size_id, :cut_qty, :accepted_qty, :note, :created_by, :updated_by)`,
		map[string]any{
			"lay_id":       layID,
			"size_id":      ins.SizeId,
			"cut_qty":      ins.CutQty,
			"accepted_qty": ins.AcceptedQty,
			"note":         ins.Note,
			"created_by":   username,
			"updated_by":   username,
		})
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1062 {
			return 0, fmt.Errorf("%w: size %d of lay %d was reported concurrently",
				entity.ErrProductionRunCutReceiptConflict, ins.SizeId, layID)
		}
		return 0, fmt.Errorf("failed to insert cut receipt of lay %d size %d: %w", layID, ins.SizeId, err)
	}
	return id, nil
}

// updateCutReceipt rewrites the two numbers and the note of an existing pair. lay_id and size_id are
// absent from the SET list because they are the identity, not attributes: moving a receipt to
// another size is deleting one count and entering another, and doing it in place would rewrite the
// history of a size nobody re-counted.
func updateCutReceipt(ctx context.Context, db dependency.DB, id int,
	ins entity.ProductionRunCutReceiptInsert, username string) error {

	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE production_run_cut_receipt SET
			cut_qty = :cut_qty, accepted_qty = :accepted_qty, note = :note, updated_by = :updated_by
		WHERE id = :id`, map[string]any{
		"cut_qty":      ins.CutQty,
		"accepted_qty": ins.AcceptedQty,
		"note":         ins.Note,
		"updated_by":   username,
		"id":           id,
	}); err != nil {
		return fmt.Errorf("failed to update cut receipt %d: %w", id, err)
	}
	return nil
}

// cutReceiptColumns is the explicit projection every receipt read uses.
//
// production_run_line IS NOT IN THIS QUERY — not as a column, not as a join. «Заказано» and «сдано
// готовым» are per (колорвей, размер) while a receipt is per (настил, размер), and one colourway
// legitimately has several настилы; joining the order quantity onto each of their rows would make
// any sum over receipts double the order (decision 4). The client already holds the run's lines.
//
// The joins that ARE here carry no CAST, so nothing on this path can trip the utf8mb3/utf8mb4
// collation difference between prod and the test container.
const cutReceiptColumns = `
	r.id, r.lay_id, l.lay_key, r.size_id, COALESCE(sz.name, '') AS size_name,
	r.cut_qty, r.accepted_qty, r.note,
	r.created_by, r.updated_by, r.created_at, r.updated_at`

// loadCutReceipts reads a run's receipts, optionally narrowed to ONE row (receiptID > 0) so a save
// returns exactly what a subsequent list returns.
//
// The lay join is scoped by l.run_id even when a single row is addressed by id: that is what makes
// a receipt of another run unreachable through this run's RPC, rather than merely unlikely.
//
// ORDER BY makes the emitted order a function of the data rather than of InnoDB's mood: настилы in
// their display order, and sizes in dictionary order (the size table is seeded xxs → up, so its id
// IS the size order).
func loadCutReceipts(ctx context.Context, db dependency.DB, runID, receiptID int) ([]entity.ProductionRunCutReceipt, error) {
	narrow := ""
	params := map[string]any{"run": runID}
	if receiptID > 0 {
		narrow = " AND r.id = :receipt_id"
		params["receipt_id"] = receiptID
	}
	rows, err := storeutil.QueryListNamed[entity.ProductionRunCutReceipt](ctx, db, `
		SELECT `+cutReceiptColumns+`
		FROM production_run_cut_receipt r
		JOIN production_run_lay l ON l.id = r.lay_id
		LEFT JOIN size sz ON sz.id = r.size_id
		WHERE l.run_id = :run`+narrow+`
		ORDER BY l.display_order, l.id, r.size_id`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to load cut receipts of run %d: %w", runID, err)
	}
	if rows == nil {
		return []entity.ProductionRunCutReceipt{}, nil
	}
	return rows, nil
}
