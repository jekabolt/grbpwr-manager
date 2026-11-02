package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// opFoldRig is a small card with its own pieces and steps. Not rtBuildMaximalCard: the rule is about
// the order of plain steps, and a card with fusing lines brings a reconciliation the test does not need.
type opFoldRig struct {
	t      *testing.T
	ctx    context.Context
	T      dependency.TechCards
	cardID int
}

const (
	opFoldPieceFlap = "01OPFOLDPIECEFLAP000000001"
	opFoldPieceBack = "01OPFOLDPIECEBACK000000002"
	opFoldFirst     = "01FDSTEP000000000000000001" // flap, seq 1
	opFoldSecond    = "01FDSTEP000000000000000002" // flap, seq 2
	opFoldBack      = "01FDSTEP000000000000000003" // back, seq 1
)

func opFoldInput(key, piece string, seq int) entity.TechCardPieceFoldInput {
	return entity.TechCardPieceFoldInput{
		FoldKey: key, PieceLineKey: piece, Seq: seq,
		Kind: entity.TechCardPieceFoldInward, Source: entity.TechCardPieceFoldSourceOwner,
		Line:   []entity.TechCardSeamSample{{U: 0, V: 0.1}, {U: 1, V: 0.1}},
		FlipAt: entity.TechCardSeamSample{U: 0.5, V: 0.05},
	}
}

// read returns the card and each step's fold keys by the step's note.
func (r opFoldRig) read() (*entity.TechCard, map[string][]string) {
	r.t.Helper()
	card, err := r.T.GetTechCardById(r.ctx, r.cardID)
	require.NoError(r.t, err)
	byNote := make(map[string][]string, len(card.Operations))
	for _, op := range card.Operations {
		byNote[op.Note.String] = op.FoldKeys
	}
	return card, byNote
}

// save writes the read card back with each step's keys set by note; nil clears every other step.
func (r opFoldRig) save(card *entity.TechCard, links map[string][]string) error {
	r.t.Helper()
	edit := card.TechCardInsert
	edit.Operations = append([]entity.TechCardOperation(nil), card.Operations...)
	for i := range edit.Operations {
		edit.Operations[i].FoldKeys = links[edit.Operations[i].Note.String]
	}
	return r.T.UpdateTechCard(r.ctx, r.cardID, &edit, card.LockVersion)
}

// opFoldInOrder is the card's steps in a new order, numbered by position as the wire converter numbers
// every body it lets through (dto.ConvertPbTechCardInsertToEntity): the read sorts by that number.
func opFoldInOrder(card *entity.TechCard, order ...int) []entity.TechCardOperation {
	out := make([]entity.TechCardOperation, 0, len(order))
	for pos, i := range order {
		op := card.Operations[i]
		op.OperationNumber = sql.NullInt32{Int32: int32((pos + 1) * 10), Valid: true}
		out = append(out, op)
	}
	return out
}

// TestTechCardOperationFolds drives tech_card_operation_fold (0444) through the real store: the round
// trip and full replace, the event riding its step through a reorder, the shared step, every refusal
// (each field-addressed and rolled back), the fold write that would reverse stored links, the fold
// delete clearing the steps, and the store clone holding no fold.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardOperationFolds(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	piece := func(key, name string) entity.TechCardPiece {
		return entity.TechCardPiece{LineKey: key, Name: name, PiecesPerGarment: 1, Grainline: "lengthwise"}
	}
	step := func(note string) entity.TechCardOperation {
		return entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
			Zone: entity.ZoneOuter, Note: ns(note)}
	}
	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "OP FOLDS", Stage: entity.TechCardStageProto, StyleNumber: ns("OPF-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Pieces:     []entity.TechCardPiece{piece(opFoldPieceFlap, "flap"), piece(opFoldPieceBack, "back")},
		Operations: []entity.TechCardOperation{step("a"), step("b"), step("c")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })
	rig := opFoldRig{t: t, ctx: ctx, T: T, cardID: cardID}

	refused := func(err error, field, reason string) {
		t.Helper()
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, field, ve.Field)
		require.Equal(t, reason, ve.Reason)
	}

	// ── fold-less: the save writes no row, the read names nothing ──
	card, links := rig.read()
	require.NoError(t, rig.save(card, nil))
	var rows int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_operation_fold l
		JOIN tech_card_operation o ON o.id = l.operation_id WHERE o.tech_card_id = ?`, cardID).Scan(&rows))
	require.Zero(t, rows)
	for note, keys := range links {
		require.Empty(t, keys, "step %s", note)
	}

	_, written, err := T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{TechCardId: cardID, By: "ann",
		Folds: []entity.TechCardPieceFoldInput{
			opFoldInput(opFoldFirst, opFoldPieceFlap, 1),
			opFoldInput(opFoldSecond, opFoldPieceFlap, 2),
			opFoldInput(opFoldBack, opFoldPieceBack, 1),
		}})
	require.NoError(t, err)
	require.Equal(t, 3, written)

	// ── a fold no step names leaves the steps as they were ──
	card, links = rig.read()
	for note, keys := range links {
		require.Empty(t, keys, "step %s: a fold on the piece is not an event on a step", note)
	}

	// ── round trip ──
	require.NoError(t, rig.save(card, map[string][]string{"a": {opFoldFirst}, "b": {opFoldSecond}, "c": {opFoldBack}}))
	saved, links := rig.read()
	require.Equal(t, map[string][]string{"a": {opFoldFirst}, "b": {opFoldSecond}, "c": {opFoldBack}}, links)
	require.Equal(t, card.LockVersion+1, saved.LockVersion, "a card save bumps lock_version as before")

	// ── saved back unchanged keeps the links (full replace rewrites them) ──
	again := saved.TechCardInsert
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &again, saved.LockVersion))
	saved, links = rig.read()
	require.Equal(t, map[string][]string{"a": {opFoldFirst}, "b": {opFoldSecond}, "c": {opFoldBack}}, links)

	// ── a reorder: the event rides its step, no code of its own ──
	reorder := saved.TechCardInsert
	reorder.Operations = opFoldInOrder(saved, 2, 0, 1)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &reorder, saved.LockVersion))
	saved, links = rig.read()
	require.Equal(t, "c", saved.Operations[0].Note.String)
	require.Equal(t, []string{opFoldBack}, saved.Operations[0].FoldKeys, "the fold rode its step to the front")
	require.Equal(t, map[string][]string{"a": {opFoldFirst}, "b": {opFoldSecond}, "c": {opFoldBack}}, links)

	// ── a reorder that reverses one piece's folds is refused by the same rule ──
	backwards := saved.TechCardInsert
	backwards.Operations = opFoldInOrder(saved, 0, 2, 1)
	refused(T.UpdateTechCard(ctx, cardID, &backwards, saved.LockVersion), "operations[2].fold_keys[0]", "fold_order")
	unchanged, _ := rig.read()
	require.Equal(t, saved.LockVersion, unchanged.LockVersion, "the refused save rolled back")
	require.Equal(t, "a", unchanged.Operations[1].Note.String)

	// ── two folds of one piece share a step, in either order, and the order is kept ──
	require.NoError(t, rig.save(unchanged, map[string][]string{"a": {opFoldSecond, opFoldFirst}}))
	saved, links = rig.read()
	require.Equal(t, []string{opFoldSecond, opFoldFirst}, links["a"])
	require.Empty(t, links["b"])
	require.Empty(t, links["c"], "the back fold went back to no step: its piece reads flat")

	// ── a fold with no step takes no part: the second alone is fine, whatever the first does later ──
	require.NoError(t, rig.save(saved, map[string][]string{"a": {opFoldSecond}}))
	saved, links = rig.read()
	require.Equal(t, []string{opFoldSecond}, links["a"])

	// ── refusals: the field is named, nothing is written ──
	aIdx := -1
	for i, op := range saved.Operations {
		if op.Note.String == "a" {
			aIdx = i
		}
	}
	require.Equal(t, 1, aIdx, "c, a, b")
	refused(rig.save(saved, map[string][]string{"c": {opFoldSecond}, "a": {opFoldFirst}}),
		"operations[1].fold_keys[0]", "fold_order")
	refused(rig.save(saved, map[string][]string{"a": {"01FDSTEP00000000000000000Z"}}),
		"operations[1].fold_keys[0]", "unknown_fold")
	refused(rig.save(saved, map[string][]string{"a": {opFoldBack}, "b": {opFoldBack}}),
		"operations[2].fold_keys[0]", "fold_on_two_steps")
	refused(rig.save(saved, map[string][]string{"a": {opFoldBack, opFoldBack}}),
		"operations[1].fold_keys[1]", "duplicate")
	unchanged, links = rig.read()
	require.Equal(t, saved.LockVersion, unchanged.LockVersion, "every refused save rolled back")
	require.Equal(t, []string{opFoldSecond}, links["a"])

	// ── a fold write that would reverse the stored links is refused and rolled back ──
	require.NoError(t, rig.save(unchanged, map[string][]string{"a": {opFoldFirst}, "b": {opFoldSecond}}))
	saved, _ = rig.read()
	swap := []entity.TechCardPieceFoldInput{opFoldInput(opFoldFirst, opFoldPieceFlap, 2), opFoldInput(opFoldSecond, opFoldPieceFlap, 1)}
	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{TechCardId: cardID, By: "ann", Folds: swap})
	refused(err, "folds[0].seq", "fold_order")
	stillFirst, _ := rig.read()
	for _, f := range stillFirst.PieceFolds {
		if f.FoldKey == opFoldFirst {
			require.Equal(t, 1, f.Seq, "the refused swap rolled back")
		}
	}
	// A write that touches neither fold of the reversal still lands, and neither moved lock_version.
	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{TechCardId: cardID, By: "ann",
		Folds: []entity.TechCardPieceFoldInput{opFoldInput(opFoldBack, opFoldPieceBack, 4)}})
	require.NoError(t, err)
	require.Equal(t, saved.LockVersion, stillFirst.LockVersion)

	// ── the same swap on one shared step passes: folds of a step do not order each other ──
	require.NoError(t, rig.save(stillFirst, map[string][]string{"a": {opFoldFirst, opFoldSecond}}))
	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{TechCardId: cardID, By: "ann", Folds: swap})
	require.NoError(t, err)

	// ── deleting a fold clears its key from the steps, no lock_version move, the card stays savable ──
	beforeDelete, _ := rig.read()
	_, deleted, err := T.DeleteTechCardPieceFolds(ctx, cardID, []string{opFoldFirst})
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	afterDelete, links := rig.read()
	require.Equal(t, []string{opFoldSecond}, links["a"], "only the deleted fold left the step")
	require.Equal(t, beforeDelete.LockVersion, afterDelete.LockVersion, "fold writes never bump lock_version")
	final := afterDelete.TechCardInsert
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &final, afterDelete.LockVersion))

	// ── the clone holds no fold; a key left on its payload would name nothing there ──
	source, _ := rig.read()
	clone := source.TechCardInsert
	clone.StyleNumber = ns("OPF-2")
	clone.Operations = append([]entity.TechCardOperation(nil), source.Operations...)
	refused(func() error {
		_, err := T.CloneTechCardForSeason(ctx, cardID, source.LockVersion, &clone)
		return err
	}(), "operations[1].fold_keys[0]", "unknown_fold")
	for i := range clone.Operations {
		clone.Operations[i].FoldKeys = nil
	}
	cloneID, err := T.CloneTechCardForSeason(ctx, cardID, source.LockVersion, &clone)
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cloneID) })
	cloned, err := T.GetTechCardById(ctx, cloneID)
	require.NoError(t, err)
	require.Empty(t, cloned.PieceFolds, "a clone starts flat, as it starts with no seams")
	for _, op := range cloned.Operations {
		require.Empty(t, op.FoldKeys)
	}
}

// TestTechCardOperationFoldMigrationIsIdempotent: the boot applied 0444 on a fresh schema; re-running
// its Up body (as a mid-file crash would on the next boot) under ANSI_QUOTES — prod's global sql_mode —
// is a no-op, and the table keeps its UNIQUE keys and cascade FK.
func TestTechCardOperationFoldMigrationIsIdempotent(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')")
	require.NoError(t, err)
	up := migrationUpBody(t, "0444_tech_card_operation_fold.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "re-run %d", i+1)
	}
	var n int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_schema = DATABASE() AND table_name = 'tech_card_operation_fold'
		  AND constraint_name IN ('uniq_op_fold_order', 'uniq_op_fold_key', 'fk_op_fold_operation')`).Scan(&n))
	require.Equal(t, 3, n)
}
