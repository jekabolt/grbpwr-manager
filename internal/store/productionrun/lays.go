package productionrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// НАСТИЛЫ (Ф4, migration 0281). A lay is the run's cutting plan for ONE pair (colourway, BOM slot):
// an ordered list of sections (раскладка × слои), plus the facing mode, the end losses and the
// snapshot of the quantities it was built for.
//
// THREE STRUCTURAL DECISIONS LIVE IN THIS FILE, and each one is an answer to a failure the
// repository has already survived:
//
//  1. SaveLay addresses EXACTLY ONE lay. A lay the payload does not mention is not touched; only
//     DeleteLay removes one. UpdateProductionRun does not know this file exists. That is the reason
//     a lay written by a direct API call cannot be erased by somebody saving the run from the UI —
//     which is precisely how tech_card's production_run_marker (0119) died (0243:3-9), and how
//     production_run_cost still behaves next door (full replace on every run save).
//
//  2. Sections are DIFFED BY KEY, not replaced. A section whose ply count changed keeps its id; so
//     does one that swapped places with its neighbour; one the payload omits disappears. A full
//     replace would re-mint section ids on every save — including a save that only edited the note
//     — and Ф5б is about to hang the consumption fact and the cutting receipt off those ids. This
//     is verbatim the scenario 0230 was written to prevent for the run's plan lines.
//
//  3. The optimistic lock is checked by PRESENCE, not by magnitude. An existing lay saved without an
//     expected version is refused. The run's own update treats 0 as "skip the check"
//     (productionrun.go, contract named in 0120) — a hole Ф6 ruled a defect, and a new object does
//     not inherit it.

// productionRunLayTerminalStatuses are the run states in which the lay plan is history rather than
// plan. partially_received is deliberately NOT here: such a run is still producing, and the cutting
// plan of what is left is still being worked on.
var productionRunLayTerminalStatuses = map[entity.ProductionRunStatus]bool{
	entity.ProductionRunReceived:  true,
	entity.ProductionRunClosed:    true,
	entity.ProductionRunCancelled: true,
}

// layRollGoodsSectionIn is `bi.section IN (:lay_sec_0, …)` — the only BOM families a lay can be laid
// on, taken from THE list (entity.RollGoodsSectionList) so this file cannot grow a fourth opinion
// about what cloth is. Built as a fragment with its own parameter names, and carrying no ':' inside
// a SQL comment: that combination breaks sqlx's named binding with "could not find name  in map",
// which is why every explanation in this file is a Go comment.
var layRollGoodsSectionIn, layRollGoodsSectionArgs = func() (string, func(map[string]any) map[string]any) {
	names := make([]string, 0, len(entity.RollGoodsSectionList))
	for i := range entity.RollGoodsSectionList {
		names = append(names, fmt.Sprintf(":lay_sec_%d", i))
	}
	frag := "bi.section IN (" + strings.Join(names, ", ") + ")"
	bind := func(args map[string]any) map[string]any {
		for i, s := range entity.RollGoodsSectionList {
			args[fmt.Sprintf("lay_sec_%d", i)] = string(s)
		}
		return args
	}
	return frag, bind
}()

// layRunContext is what the run must tell a lay write before anything else happens: which card it
// makes, and whether it is still a plan.
type layRunContext struct {
	TechCardId int    `db:"tech_card_id"`
	Status     string `db:"status"`
	Purpose    string `db:"purpose"`
}

// lockRunForLay reads the run and its card's purpose FOR UPDATE and applies the two guards every lay
// write shares. FOR UPDATE, and not a plain read, is what serialises a save against a concurrent
// delete of the same lay — and against a concurrent status change that would otherwise let a plan
// edit land on a run that had just been received.
//
// The purpose join is read here rather than in a second statement so an aux card cannot become
// sellable (or the reverse) between the two answers.
func lockRunForLay(ctx context.Context, db dependency.DB, runID int) (layRunContext, error) {
	row, err := storeutil.QueryNamedOne[layRunContext](ctx, db, `
		SELECT r.tech_card_id, r.status, c.purpose
		FROM production_run r
		JOIN tech_card c ON c.id = r.tech_card_id
		WHERE r.id = :id
		FOR UPDATE`, map[string]any{"id": runID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return layRunContext{}, sql.ErrNoRows
		}
		return layRunContext{}, fmt.Errorf("failed to lock production run %d for lay write: %w", runID, err)
	}
	if productionRunLayTerminalStatuses[entity.ProductionRunStatus(row.Status)] {
		return layRunContext{}, fmt.Errorf("%w: run %d is %s", entity.ErrProductionRunLocked, runID, row.Status)
	}
	if entity.TechCardPurpose(row.Purpose) == entity.TechCardPurposeAuxiliary {
		return layRunContext{}, fmt.Errorf("%w: %s", entity.ErrProductionRunLayNotApplicable,
			entity.ProductionRunLayNotApplicableKey)
	}
	return row, nil
}

// SaveLay creates or updates ONE lay of a run and diffs its sections by key, returning the stored
// lay exactly as ListLays would return it.
//
// expectedLockVersion carries PRESENCE, not magnitude (entity.LockGuard, Ф6.5). An ABSENT token on
// an EXISTING lay is a REFUSAL — and here it is a refusal outright, not the legacy last-write-wins
// opt-out the run still grants: the run's opt-out exists because clients predating the lock echo a
// bare 0, and a lay has no such clients. A new object starts closed.
//
// reaffirm is the operator saying "I checked, the quantities are still covered" — the only way to
// clear the stale badge without touching a section, because otherwise editing the note would
// launder the very signal the snapshot exists to raise.
func (s *Store) SaveLay(ctx context.Context, runID int, ins entity.ProductionRunLayInsert,
	expectedLockVersion entity.LockGuard, reaffirm bool, username string) (entity.ProductionRunLay, error) {

	if err := validateLayInsert(&ins); err != nil {
		return entity.ProductionRunLay{}, err
	}
	sectionKeys, err := resolveLaySectionKeys(ins.Sections)
	if err != nil {
		return entity.ProductionRunLay{}, err
	}

	var saved entity.ProductionRunLay
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()

		// 1. The run under lock: it exists, it is still a plan, and its card is not auxiliary.
		run, err := lockRunForLay(ctx, db, runID)
		if err != nil {
			return err
		}

		// 2. Existence by (run_id, lay_key), resolved with a SELECT and never with RowsAffected. The
		// driver counts rows CHANGED (the DSN carries no clientFoundRows), so a byte-identical
		// re-save would report 0 rows and read as a phantom 404 — the trap named in
		// techcard/markers.go.
		var stored *layIdentity
		if ins.LayKey != "" {
			row, err := storeutil.QueryNamedOne[layIdentity](ctx, db, `
				SELECT id, lock_version, qty_snapshot
				FROM production_run_lay
				WHERE run_id = :run_id AND lay_key = :lay_key`,
				map[string]any{"run_id": runID, "lay_key": ins.LayKey})
			switch {
			case err == nil:
				stored = &row
			case errors.Is(err, sql.ErrNoRows):
				// A key the run does not hold yet is a create, not an error: the client mints the
				// identity before the first save, which is what makes a retry idempotent.
			default:
				return fmt.Errorf("failed to resolve lay %q of run %d: %w", ins.LayKey, runID, err)
			}
		}

		// 3. Optimistic lock BY PRESENCE.
		if stored != nil {
			if !expectedLockVersion.Present() {
				return fmt.Errorf("%w: lay %q was saved without an expected version", entity.ErrProductionRunLayConflict, ins.LayKey)
			}
			if expectedLockVersion.Conflicts(stored.LockVersion) {
				return fmt.Errorf("%w: lay %q is at version %d, caller expected %d",
					entity.ErrProductionRunLayConflict, ins.LayKey, stored.LockVersion, expectedLockVersion.Version())
			}
		}

		// 4. The cloth slot, addressed by its stable key and resolved against THIS run's card. Only
		// roll goods: a lay is a length of cloth, and one laid on a thread or a button would be a
		// consumption norm for something that is counted, not laid out.
		bomItemID, err := resolveLayBomItem(ctx, db, run.TechCardId, ins.BomLineKey)
		if err != nil {
			return err
		}

		// 5. The colourway: it must be one of this card's, and the run must actually plan it. A lay
		// on a colour the run does not make has nothing to be laid on.
		if err := requireLayColorway(ctx, db, runID, run.TechCardId, ins.ColorwayId); err != nil {
			return err
		}

		// 6. Every section's раскладка must belong to THIS run and to THIS slot. A card marker —
		// including the norm — can never be a section: that is what makes the cascade total and the
		// sentence "a run's markers die with the run" true rather than aspirational.
		if err := requireLaySectionMarkers(ctx, db, runID, run.TechCardId, bomItemID, ins.Sections); err != nil {
			return err
		}

		// 7. Header upsert. lock_version is bumped on EVERY update, whether or not a column changed:
		// the version is a statement about "somebody saved this", which is what the other tab needs
		// to hear.
		layID := 0
		creating := stored == nil
		if creating {
			layID, err = insertLayHeader(ctx, db, runID, ins, bomItemID, username)
			if err != nil {
				return err
			}
		} else {
			layID = stored.Id
			if err := updateLayHeader(ctx, db, layID, ins, bomItemID, username); err != nil {
				return err
			}
		}

		// 8. The section diff.
		sectionsChanged, err := upsertLaySections(ctx, db, layID, ins.Sections, sectionKeys)
		if err != nil {
			return err
		}

		// 9. The quantity snapshot is recomputed IF AND ONLY IF the sections changed, the caller
		// reaffirmed, or the lay is new. A note edit leaves it alone — otherwise the stale badge
		// would wash itself clean by accident.
		if creating || sectionsChanged || reaffirm {
			if err := refreshLayQtySnapshot(ctx, db, layID, runID, ins.ColorwayId); err != nil {
				return err
			}
		}

		lays, err := loadLays(ctx, db, runID, layID)
		if err != nil {
			return err
		}
		if len(lays) != 1 {
			return fmt.Errorf("saved lay %d of run %d disappeared from its own transaction", layID, runID)
		}
		saved = lays[0]
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows),
			errors.Is(err, entity.ErrProductionRunLocked),
			errors.Is(err, entity.ErrProductionRunLayNotApplicable),
			errors.Is(err, entity.ErrProductionRunLayConflict):
			return entity.ProductionRunLay{}, err
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return entity.ProductionRunLay{}, err
		}
		slog.ErrorContext(ctx, "failed to save production run lay",
			slog.Int("run_id", runID), slog.String("lay_key", ins.LayKey), slog.String("err", err.Error()))
		return entity.ProductionRunLay{}, fmt.Errorf("can't save production run lay: %w", err)
	}
	return saved, nil
}

// DeleteLay removes ONE lay of a run (its sections cascade). Existence is established by a SELECT,
// for the same reason SaveLay does it: an answer derived from rows-affected cannot distinguish
// "there was nothing there" from "there was nothing to change".
func (s *Store) DeleteLay(ctx context.Context, runID int, layKey string) error {
	if strings.TrimSpace(layKey) == "" {
		return entity.NewFieldViolation("lay_key", "required", "",
			"address the lay by the stable key it was created with")
	}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if _, err := lockRunForLay(ctx, db, runID); err != nil {
			return err
		}
		row, err := storeutil.QueryNamedOne[struct {
			Id int `db:"id"`
		}](ctx, db, `SELECT id FROM production_run_lay WHERE run_id = :run_id AND lay_key = :lay_key`,
			map[string]any{"run_id": runID, "lay_key": layKey})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: lay %q of run %d", entity.ErrProductionRunLayNotFound, layKey, runID)
			}
			return fmt.Errorf("failed to resolve lay %q of run %d for delete: %w", layKey, runID, err)
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM production_run_lay WHERE id = :id`, map[string]any{"id": row.Id}); err != nil {
			return fmt.Errorf("failed to delete production run lay %d: %w", row.Id, err)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows),
			errors.Is(err, entity.ErrProductionRunLocked),
			errors.Is(err, entity.ErrProductionRunLayNotApplicable),
			errors.Is(err, entity.ErrProductionRunLayNotFound):
			return err
		}
		slog.ErrorContext(ctx, "failed to delete production run lay",
			slog.Int("run_id", runID), slog.String("lay_key", layKey), slog.String("err", err.Error()))
		return fmt.Errorf("can't delete production run lay: %w", err)
	}
	return nil
}

// ListLays returns the run's whole lay plan, with each lay's sections, its quantity snapshot and
// today's quantities. Applicability is stated explicitly: an auxiliary card answers
// Applicable=false with a reason, never an empty list — an empty list reads as "none built yet",
// which is an invitation to build one.
func (s *Store) ListLays(ctx context.Context, runID int) (entity.ProductionRunLayList, error) {
	run, err := storeutil.QueryNamedOne[layRunContext](ctx, s.DB, `
		SELECT r.tech_card_id, r.status, c.purpose
		FROM production_run r
		JOIN tech_card c ON c.id = r.tech_card_id
		WHERE r.id = :id`, map[string]any{"id": runID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.ProductionRunLayList{}, sql.ErrNoRows
		}
		return entity.ProductionRunLayList{}, fmt.Errorf("failed to load production run %d for lays: %w", runID, err)
	}
	if entity.TechCardPurpose(run.Purpose) == entity.TechCardPurposeAuxiliary {
		return entity.ProductionRunLayList{
			Applicable:          false,
			NotApplicableReason: entity.ProductionRunLayNotApplicableKey,
		}, nil
	}
	lays, err := loadLays(ctx, s.DB, runID, 0)
	if err != nil {
		return entity.ProductionRunLayList{}, fmt.Errorf("can't list production run lays: %w", err)
	}
	return entity.ProductionRunLayList{Applicable: true, Lays: lays}, nil
}

// layIdentity is the stored half of a lay a save needs before it writes anything: the id it must
// keep, the version it must match, and the snapshot it must not disturb.
type layIdentity struct {
	Id          int    `db:"id"`
	LockVersion int    `db:"lock_version"`
	QtySnapshot []byte `db:"qty_snapshot"`
}

// validateLayInsert enforces in Go everything the schema's CHECKs enforce in SQL, so a bad payload
// is refused with the offending field named instead of surfacing MySQL error 3819. It also mints
// the lay key when the client did not, which is what makes a create addressable afterwards.
func validateLayInsert(ins *entity.ProductionRunLayInsert) error {
	ins.LayKey = strings.TrimSpace(ins.LayKey)
	if ins.LayKey == "" {
		key, err := entity.MintProductionLayKey()
		if err != nil {
			return fmt.Errorf("mint production run lay key: %w", err)
		}
		ins.LayKey = key
	} else if !entity.IsValidProductionLayKey(ins.LayKey) {
		return entity.NewFieldViolation("lay.lay_key", "malformed", ins.LayKey,
			"a lay key is 26 characters of [0-9A-Z]; leave it empty to create a new lay")
	}
	if !entity.IsValidProductionLayMode(ins.Mode) {
		return entity.NewFieldViolation("lay.mode", "unknown_mode", string(ins.Mode),
			"pick face_up or face_to_face")
	}
	if ins.ColorwayId <= 0 {
		return entity.NewFieldViolation("lay.colorway_id", "required", "",
			"a lay belongs to one colourway of the run")
	}
	if strings.TrimSpace(ins.BomLineKey) == "" {
		return entity.NewFieldViolation("lay.bom_line_key", "required", "",
			"a lay belongs to one cloth slot of the tech card")
	}
	ins.BomLineKey = strings.TrimSpace(ins.BomLineKey)
	if ins.EndLossCm.LessThan(entity.ProductionLayEndLossMinCm) ||
		ins.EndLossCm.GreaterThan(entity.ProductionLayEndLossMaxCm) {
		return entity.NewFieldViolation("lay.end_loss_cm", "out_of_range", ins.EndLossCm.String(),
			"end loss is measured per ONE end of ONE ply, 0..100 cm")
	}
	for i := range ins.Sections {
		sec := &ins.Sections[i]
		if sec.MarkerId <= 0 {
			return entity.NewFieldViolation(fmt.Sprintf("lay.sections[%d].marker_id", i), "required", "",
				"every section lays one раскладка of this run")
		}
		if sec.Plies < entity.ProductionLayPliesMin || sec.Plies > entity.ProductionLayPliesMax {
			return entity.NewFieldViolation(fmt.Sprintf("lay.sections[%d].plies", i), "out_of_range",
				fmt.Sprintf("%d", sec.Plies),
				fmt.Sprintf("a section lays between %d and %d plies", entity.ProductionLayPliesMin, entity.ProductionLayPliesMax))
		}
		// Parity is refused at SAVE time, not reported afterwards. In face-to-face the last unpaired
		// ply yields one hand only, and a stored odd section would make every downstream count claim
		// pairs that the cutting room will not produce.
		if ins.Mode.RequiresEvenPlies() && sec.Plies%2 != 0 {
			return entity.NewFieldViolation(fmt.Sprintf("lay.sections[%d].plies", i), "odd_plies_in_face_to_face",
				fmt.Sprintf("%d", sec.Plies),
				"face-to-face pairs its plies: use an even count, or lay the section face up")
		}
	}
	return nil
}

// resolveLaySectionKeys mints the missing section keys and refuses duplicates. A duplicate key would
// send two payload rows at one stored row, and the diff would silently apply whichever came last.
func resolveLaySectionKeys(sections []entity.ProductionRunLaySectionInsert) ([]string, error) {
	keys := make([]string, len(sections))
	seen := make(map[string]int, len(sections))
	for i := range sections {
		key := strings.TrimSpace(sections[i].SectionKey)
		if key == "" {
			minted, err := entity.MintProductionLayKey()
			if err != nil {
				return nil, fmt.Errorf("mint lay section key: %w", err)
			}
			key = minted
		} else if !entity.IsValidProductionLayKey(key) {
			return nil, entity.NewFieldViolation(fmt.Sprintf("lay.sections[%d].section_key", i), "malformed", key,
				"a section key is 26 characters of [0-9A-Z]; leave it empty to create a new section")
		}
		if first, dup := seen[key]; dup {
			return nil, entity.NewFieldViolation(fmt.Sprintf("lay.sections[%d].section_key", i), "duplicate", key,
				fmt.Sprintf("section %d already carries this key; every section needs its own", first))
		}
		seen[key] = i
		keys[i] = key
	}
	return keys, nil
}

// resolveLayBomItem resolves the cloth slot by its stable key, restricted to this card and to roll
// goods. A slot the payload names but the card no longer has is a refusal that NAMES it — a lay
// whose slot vanished is BROKEN and must say so, never quietly re-bind to something else.
func resolveLayBomItem(ctx context.Context, db dependency.DB, techCardID int, bomLineKey string) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, db, `
		SELECT bi.id FROM tech_card_bom_item bi
		WHERE bi.tech_card_id = :card AND bi.line_key = :key AND `+layRollGoodsSectionIn,
		layRollGoodsSectionArgs(map[string]any{"card": techCardID, "key": bomLineKey}))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, entity.NewFieldViolation("lay.bom_line_key", "not_found", bomLineKey,
				"a lay lays a fabric, lining, interlining or insulation line of this run's tech card")
		}
		return 0, fmt.Errorf("resolve bom line %q of tech card %d: %w", bomLineKey, techCardID, err)
	}
	return row.Id, nil
}

// requireLayColorway checks the two facts that make a colourway layable: it is one of this card's
// (a colourway is a product whose style is the card, 0151), and this run actually plans it. The two
// refusals are separate because they are different problems with different fixes.
func requireLayColorway(ctx context.Context, db dependency.DB, runID, techCardID, colorwayID int) error {
	onCard, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM product WHERE id = :cw AND style_id = :card`,
		map[string]any{"cw": colorwayID, "card": techCardID})
	if err != nil {
		return fmt.Errorf("check colorway %d on tech card %d: %w", colorwayID, techCardID, err)
	}
	if onCard == 0 {
		return entity.NewFieldViolation("lay.colorway_id", "not_on_card", fmt.Sprintf("colorway %d", colorwayID),
			"a lay's colourway must be a colourway of this run's tech card")
	}
	planned, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM production_run_line WHERE run_id = :run AND product_id = :cw`,
		map[string]any{"run": runID, "cw": colorwayID})
	if err != nil {
		return fmt.Errorf("check colorway %d on run %d: %w", colorwayID, runID, err)
	}
	if planned == 0 {
		return entity.NewFieldViolation("lay.colorway_id", "not_planned_by_run", fmt.Sprintf("colorway %d", colorwayID),
			"plan a quantity for this colourway before laying it")
	}
	return nil
}

// requireLaySectionMarkers checks every section's раскладка in ONE round trip. A marker qualifies
// only if it belongs to this card, to THIS RUN (run_id, 0282) and to the lay's own slot. A card
// marker — the norm included — can never be a section: the run's markers cascade with the run, and
// letting a section point at a card marker would make "a lay dies with its run" false and would put
// a delete of a run in a position to take a card asset with it.
func requireLaySectionMarkers(ctx context.Context, db dependency.DB, runID, techCardID, bomItemID int,
	sections []entity.ProductionRunLaySectionInsert) error {

	if len(sections) == 0 {
		return nil
	}
	ids := make([]int, 0, len(sections))
	for i := range sections {
		ids = append(ids, sections[i].MarkerId)
	}
	rows, err := storeutil.QueryListNamed[struct {
		Id int `db:"id"`
	}](ctx, db, `
		SELECT id FROM tech_card_marker
		WHERE id IN (:ids) AND tech_card_id = :card AND run_id = :run AND bom_item_id = :bom`,
		map[string]any{"ids": ids, "card": techCardID, "run": runID, "bom": bomItemID})
	if err != nil {
		return fmt.Errorf("resolve lay section markers of run %d: %w", runID, err)
	}
	ok := make(map[int]bool, len(rows))
	for _, r := range rows {
		ok[r.Id] = true
	}
	for i := range sections {
		if ok[sections[i].MarkerId] {
			continue
		}
		return entity.NewFieldViolation(fmt.Sprintf("lay.sections[%d].marker_id", i), "not_a_run_marker",
			fmt.Sprintf("marker %d", sections[i].MarkerId),
			"a section lays a раскладка taken FOR THIS RUN on this lay's cloth slot; copy the card's раскладка into the run first")
	}
	return nil
}

// insertLayHeader creates the lay row. A duplicate key here is the create/create race — two tabs
// minting the same lay_key at once — and it is a conflict, not an internal error.
func insertLayHeader(ctx context.Context, db dependency.DB, runID int, ins entity.ProductionRunLayInsert,
	bomItemID int, username string) (int, error) {

	params := layHeaderParams(ins, bomItemID, username)
	params["run_id"] = runID
	params["created_by"] = username
	// An empty snapshot is written here and replaced by refreshLayQtySnapshot in the same
	// transaction; the column is NOT NULL and no lay ever leaves this function without one.
	params["qty_snapshot"] = "[]"
	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO production_run_lay
			(run_id, lay_key, colorway_id, bom_item_id, bom_line_key, name, mode, end_loss_cm,
			 qty_snapshot, note, display_order, created_by, updated_by)
		VALUES
			(:run_id, :lay_key, :colorway_id, :bom_item_id, :bom_line_key, :name, :mode, :end_loss_cm,
			 :qty_snapshot, :note, :display_order, :created_by, :updated_by)`, params)
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1062 {
			return 0, fmt.Errorf("%w: lay %q was created concurrently", entity.ErrProductionRunLayConflict, ins.LayKey)
		}
		return 0, fmt.Errorf("failed to insert production run lay: %w", err)
	}
	return id, nil
}

// updateLayHeader rewrites the lay's attributes and bumps its version. qty_snapshot is absent from
// the SET list on purpose: it is the server's own fact and is refreshed only under the rule in
// SaveLay. lay_key and run_id are absent because they are the identity, not an attribute.
func updateLayHeader(ctx context.Context, db dependency.DB, layID int, ins entity.ProductionRunLayInsert,
	bomItemID int, username string) error {

	params := layHeaderParams(ins, bomItemID, username)
	params["id"] = layID
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE production_run_lay SET
			lock_version = lock_version + 1,
			colorway_id = :colorway_id, bom_item_id = :bom_item_id, bom_line_key = :bom_line_key,
			name = :name, mode = :mode, end_loss_cm = :end_loss_cm, note = :note,
			display_order = :display_order, updated_by = :updated_by
		WHERE id = :id`, params); err != nil {
		return fmt.Errorf("failed to update production run lay %d: %w", layID, err)
	}
	return nil
}

func layHeaderParams(ins entity.ProductionRunLayInsert, bomItemID int, username string) map[string]any {
	return map[string]any{
		"lay_key":       ins.LayKey,
		"colorway_id":   ins.ColorwayId,
		"bom_item_id":   bomItemID,
		"bom_line_key":  ins.BomLineKey,
		"name":          ins.Name,
		"mode":          string(ins.Mode),
		"end_loss_cm":   ins.EndLossCm,
		"note":          ins.Note,
		"display_order": ins.DisplayOrder,
		"updated_by":    username,
	}
}

// laySectionIdentity is a stored section as the diff sees it: its id, its key, and the three values
// that decide whether the payload actually changed anything.
type laySectionIdentity struct {
	Id         int    `db:"id"`
	SectionKey string `db:"section_key"`
	MarkerId   int    `db:"marker_id"`
	Plies      int    `db:"plies"`
	Position   int    `db:"position"`
}

// laySectionUpdate is one matched section: the payload index that keeps the stored row's id, and
// whether any of its values actually moved.
type laySectionUpdate struct {
	index   int
	id      int
	changed bool
}

// laySectionDiff is the whole write plan of upsertLaySections, decided before a single statement
// runs so the rules can be unit-tested without a database.
type laySectionDiff struct {
	deletes []int              // stored ids whose key vanished from the payload, ascending
	updates []laySectionUpdate // matched rows, in payload order
	inserts []int              // payload indexes with no stored row, in payload order
}

// Changed reports whether the plan alters the SET or the CONTENT of the lay's sections. It is the
// trigger for recomputing the quantity snapshot: a save that only edited the note must leave the
// stale badge exactly where it was.
func (d laySectionDiff) Changed() bool {
	if len(d.deletes) > 0 || len(d.inserts) > 0 {
		return true
	}
	for _, u := range d.updates {
		if u.changed {
			return true
		}
	}
	return false
}

// planLaySectionDiff decides delete/update/insert for one save. Pure: no DB, no ordering surprises.
//
// Three shapes and nothing else:
//   - a key present on both sides is an UPDATE IN PLACE — the id survives, which is the whole point.
//     That covers "only the ply count changed" and "two sections swapped places" alike, because
//     position is an attribute here and not an identity;
//   - a key only in the payload is an INSERT;
//   - a key only in the store is a DELETE. Inside ONE lay, the submitted list IS the lay — leaving
//     unmentioned sections alone would leave no way to remove one. (Between lays the rule is the
//     opposite, and that asymmetry is deliberate: see the file header.)
//
// Unlike upsertRunLines this needs no PARKING step. Parking exists there because run lines carry a
// SECOND unique key — uniq_prl (run_id, product_id, size_id) — so a row moving to a slot another row
// is vacating collides mid-diff. Sections have exactly one unique key, uniq_prlays_key (lay_id,
// section_key), and the diff matches on precisely that key: a matched row keeps its key by
// construction, so it cannot collide with anything, and every other row is either leaving or new.
// Position is not unique and never was — two sections may legally share one.
func planLaySectionDiff(stored []laySectionIdentity, sections []entity.ProductionRunLaySectionInsert,
	keys []string) laySectionDiff {

	existing := make(map[string]laySectionIdentity, len(stored))
	for _, row := range stored {
		existing[row.SectionKey] = row
	}
	submitted := make(map[string]bool, len(keys))
	for _, k := range keys {
		submitted[k] = true
	}

	plan := laySectionDiff{}
	for _, row := range stored {
		if submitted[row.SectionKey] {
			continue
		}
		plan.deletes = append(plan.deletes, row.Id)
	}
	sort.Ints(plan.deletes)

	for i := range sections {
		row, ok := existing[keys[i]]
		if !ok {
			plan.inserts = append(plan.inserts, i)
			continue
		}
		changed := row.MarkerId != sections[i].MarkerId ||
			row.Plies != sections[i].Plies ||
			row.Position != sections[i].Position
		plan.updates = append(plan.updates, laySectionUpdate{index: i, id: row.Id, changed: changed})
	}
	return plan
}

// upsertLaySections applies the diff and reports whether anything about the sections actually
// changed. The order is delete → update → insert, which needs no further argument: see
// planLaySectionDiff on why no parking step is required.
func upsertLaySections(ctx context.Context, db dependency.DB, layID int,
	sections []entity.ProductionRunLaySectionInsert, keys []string) (bool, error) {

	stored, err := storeutil.QueryListNamed[laySectionIdentity](ctx, db, `
		SELECT id, section_key, marker_id, plies, position
		FROM production_run_lay_section WHERE lay_id = :lay_id`, map[string]any{"lay_id": layID})
	if err != nil {
		return false, fmt.Errorf("failed to load stored sections of lay %d: %w", layID, err)
	}
	plan := planLaySectionDiff(stored, sections, keys)

	if len(plan.deletes) > 0 {
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM production_run_lay_section WHERE id IN (:ids)`,
			map[string]any{"ids": plan.deletes}); err != nil {
			return false, fmt.Errorf("failed to delete lay sections: %w", err)
		}
	}
	for _, u := range plan.updates {
		if !u.changed {
			// Nothing to write. Skipped rather than executed for its own sake: an UPDATE that changes
			// nothing is not free, and this branch is the common case of a note-only save.
			continue
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE production_run_lay_section SET marker_id = :marker_id, plies = :plies, position = :position
			WHERE id = :id`, map[string]any{
			"marker_id": sections[u.index].MarkerId,
			"plies":     sections[u.index].Plies,
			"position":  sections[u.index].Position,
			"id":        u.id,
		}); err != nil {
			return false, fmt.Errorf("failed to update lay section %d: %w", u.id, err)
		}
	}
	if len(plan.inserts) > 0 {
		rows := make([]map[string]any, 0, len(plan.inserts))
		for _, i := range plan.inserts {
			rows = append(rows, map[string]any{
				"lay_id":      layID,
				"section_key": keys[i],
				"marker_id":   sections[i].MarkerId,
				"plies":       sections[i].Plies,
				"position":    sections[i].Position,
			})
		}
		if err := storeutil.BulkInsert(ctx, db, "production_run_lay_section", rows); err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1062 {
				return false, fmt.Errorf("%w: a section of this lay was created concurrently", entity.ErrProductionRunLayConflict)
			}
			return false, fmt.Errorf("failed to insert lay sections: %w", err)
		}
	}
	return plan.Changed(), nil
}

// refreshLayQtySnapshot rewrites the lay's snapshot from the run's CURRENT lines for its colourway.
// The server reads its own rows: a snapshot accepted from the client could be forged, and a forged
// snapshot clears the stale badge, which is the one thing the snapshot exists to raise.
func refreshLayQtySnapshot(ctx context.Context, db dependency.DB, layID, runID, colorwayID int) error {
	current, err := loadRunColorwayQuantities(ctx, db, runID, colorwayID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(entity.NormalizeProductionRunLayQty(current))
	if err != nil {
		return fmt.Errorf("marshal qty snapshot of lay %d: %w", layID, err)
	}
	if err := storeutil.ExecNamed(ctx, db,
		`UPDATE production_run_lay SET qty_snapshot = :snapshot WHERE id = :id`,
		map[string]any{"snapshot": string(payload), "id": layID}); err != nil {
		return fmt.Errorf("failed to write qty snapshot of lay %d: %w", layID, err)
	}
	return nil
}

// loadRunColorwayQuantities is the run's planned grid for one colourway, summed per size. A line
// without a size lands under size 0 rather than being dropped: the snapshot has to describe the
// whole plan, including the part of it that is not yet sized.
func loadRunColorwayQuantities(ctx context.Context, db dependency.DB, runID, colorwayID int) ([]entity.ProductionRunLayQtyEntry, error) {
	rows, err := storeutil.QueryListNamed[entity.ProductionRunLayQtyEntry](ctx, db, `
		SELECT COALESCE(size_id, 0) AS size_id, SUM(planned_qty) AS qty
		FROM production_run_line
		WHERE run_id = :run AND product_id = :cw
		GROUP BY COALESCE(size_id, 0)`, map[string]any{"run": runID, "cw": colorwayID})
	if err != nil {
		return nil, fmt.Errorf("failed to load planned quantities of colorway %d on run %d: %w", colorwayID, runID, err)
	}
	return rows, nil
}

// layColumns is the explicit column list every lay read uses. Explicit and not SELECT *: the JSON
// snapshot must be read by name (a `*` read is how the quoted-JSON-scalar bug resurfaces), and the
// joined names must not collide with the row's own.
const layColumns = `
	l.id, l.run_id, l.lay_key, l.colorway_id, COALESCE(p.color, '') AS colorway_name,
	l.bom_item_id, l.bom_line_key, bi.name AS bom_item_name,
	l.mode, l.end_loss_cm, l.name, l.note, l.display_order, l.lock_version,
	l.qty_snapshot, l.created_by, l.updated_by, l.created_at, l.updated_at`

// layRow is the stored lay plus its raw snapshot bytes, which only this file decodes.
type layRow struct {
	entity.ProductionRunLay
	QtySnapshotRaw []byte `db:"qty_snapshot"`
}

// loadLays reads a run's lays with their sections and both quantity sets. layID > 0 narrows to one
// lay, so a save returns EXACTLY what a subsequent list would return — one read path, so the two can
// never disagree about a field.
func loadLays(ctx context.Context, db dependency.DB, runID, layID int) ([]entity.ProductionRunLay, error) {
	narrow := ""
	params := map[string]any{"run": runID}
	if layID > 0 {
		narrow = " AND l.id = :lay_id"
		params["lay_id"] = layID
	}
	rows, err := storeutil.QueryListNamed[layRow](ctx, db, `
		SELECT `+layColumns+`
		FROM production_run_lay l
		LEFT JOIN product p ON p.id = l.colorway_id
		LEFT JOIN tech_card_bom_item bi ON bi.id = l.bom_item_id
		WHERE l.run_id = :run`+narrow+`
		ORDER BY l.display_order, l.id`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to load lays of run %d: %w", runID, err)
	}
	if len(rows) == 0 {
		return []entity.ProductionRunLay{}, nil
	}

	lays := make([]entity.ProductionRunLay, 0, len(rows))
	ids := make([]int, 0, len(rows))
	for i := range rows {
		lay := rows[i].ProductionRunLay
		snapshot, err := decodeLayQtySnapshot(rows[i].QtySnapshotRaw)
		if err != nil {
			return nil, fmt.Errorf("lay %d of run %d: %w", lay.Id, runID, err)
		}
		lay.QtySnapshot = snapshot
		lays = append(lays, lay)
		ids = append(ids, lay.Id)
	}

	sections, err := loadLaySections(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	for i := range lays {
		lays[i].Sections = sections[lays[i].Id]
	}

	// Today's quantities, one query for every colourway the lays name. Attached here rather than
	// left to the caller because "stale" is a comparison, and a reader that has to fetch the second
	// half itself is a reader that will eventually skip it.
	current, err := loadRunQuantitiesByColorway(ctx, db, runID)
	if err != nil {
		return nil, err
	}
	for i := range lays {
		lays[i].QtyCurrent = entity.NormalizeProductionRunLayQty(current[lays[i].ColorwayId])
		lays[i].QuantitiesStale = entity.ProductionRunLayQuantitiesStale(lays[i].QtySnapshot, lays[i].QtyCurrent)
	}
	return lays, nil
}

// decodeLayQtySnapshot turns the stored JSON into entries. An empty column is an empty set, not an
// error: the row is written with '[]' before the snapshot lands, inside the same transaction.
func decodeLayQtySnapshot(raw []byte) ([]entity.ProductionRunLayQtyEntry, error) {
	if len(raw) == 0 {
		return []entity.ProductionRunLayQtyEntry{}, nil
	}
	var entries []entity.ProductionRunLayQtyEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode qty snapshot: %w", err)
	}
	return entity.NormalizeProductionRunLayQty(entries), nil
}

// loadLaySections reads every section of a batch of lays in ONE round trip, with the раскладка facts
// a reader turns into a length. ORDER BY position, id makes the emitted order a function of the data
// rather than of InnoDB's mood — a list that reshuffles between two reads of an unchanged lay reads
// as the lay having changed.
func loadLaySections(ctx context.Context, db dependency.DB, layIDs []int) (map[int][]entity.ProductionRunLaySection, error) {
	out := make(map[int][]entity.ProductionRunLaySection, len(layIDs))
	if len(layIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[entity.ProductionRunLaySection](ctx, db, `
		SELECT s.id, s.lay_id, s.section_key, s.marker_id, s.plies, s.position,
		       COALESCE(m.name, '') AS marker_name,
		       m.used_length_cm AS marker_used_length_cm,
		       m.fabric_width_cm AS marker_fabric_width_cm,
		       m.total_units AS marker_total_units,
		       m.bom_item_id AS marker_bom_item_id
		FROM production_run_lay_section s
		LEFT JOIN tech_card_marker m ON m.id = s.marker_id
		WHERE s.lay_id IN (:ids)
		ORDER BY s.lay_id, s.position, s.id`, map[string]any{"ids": layIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to load lay sections: %w", err)
	}
	for i := range rows {
		out[rows[i].LayId] = append(out[rows[i].LayId], rows[i])
	}
	return out, nil
}

// loadRunQuantitiesByColorway is the run's planned grid grouped by colourway and size — the "today"
// half of the staleness comparison. Product-less lines are skipped: a line without a colourway
// cannot be covered by any lay, and folding it into some colourway's total would be an invention.
func loadRunQuantitiesByColorway(ctx context.Context, db dependency.DB, runID int) (map[int][]entity.ProductionRunLayQtyEntry, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ColorwayId int `db:"colorway_id"`
		SizeId     int `db:"size_id"`
		Qty        int `db:"qty"`
	}](ctx, db, `
		SELECT product_id AS colorway_id, COALESCE(size_id, 0) AS size_id, SUM(planned_qty) AS qty
		FROM production_run_line
		WHERE run_id = :run AND product_id IS NOT NULL
		GROUP BY product_id, COALESCE(size_id, 0)`, map[string]any{"run": runID})
	if err != nil {
		return nil, fmt.Errorf("failed to load planned quantities of run %d: %w", runID, err)
	}
	out := make(map[int][]entity.ProductionRunLayQtyEntry)
	for _, r := range rows {
		out[r.ColorwayId] = append(out[r.ColorwayId], entity.ProductionRunLayQtyEntry{SizeId: r.SizeId, Qty: r.Qty})
	}
	return out, nil
}
