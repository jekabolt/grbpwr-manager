package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardPieceDeleteTakesItsFolds: the card save that deletes a piece deletes that piece's folds in
// the same transaction (owner decision 11.11). A step of that same body may still name such a fold: the
// link is skipped, not refused. Other pieces' folds and links stay; an unknown key is still refused.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardPieceDeleteTakesItsFolds(t *testing.T) {
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

	const (
		pieceKeep = "01PDFOLDPIECEKEEP000000001"
		pieceGone = "01PDFOLDPIECEGONE000000002"
		foldKeep  = "01PDFXD0000000000000000001"
		foldGone1 = "01PDFXD0000000000000000002"
		foldGone2 = "01PDFXD0000000000000000003"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	piece := func(key, name string) entity.TechCardPiece {
		return entity.TechCardPiece{LineKey: key, Name: name, PiecesPerGarment: 1, Grainline: "lengthwise"}
	}
	step := func(note string) entity.TechCardOperation {
		return entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
			Zone: entity.ZoneOuter, Note: ns(note)}
	}
	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "PIECE DELETE FOLDS", Stage: entity.TechCardStageProto, StyleNumber: ns("PDF-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Pieces:     []entity.TechCardPiece{piece(pieceKeep, "keep"), piece(pieceGone, "gone")},
		Operations: []entity.TechCardOperation{step("a"), step("b")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })
	rig := opFoldRig{t: t, ctx: ctx, T: T, cardID: cardID}

	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{TechCardId: cardID, By: "ann",
		Folds: []entity.TechCardPieceFoldInput{
			opFoldInput(foldKeep, pieceKeep, 1),
			opFoldInput(foldGone1, pieceGone, 1),
			opFoldInput(foldGone2, pieceGone, 2),
		}})
	require.NoError(t, err)
	card, _ := rig.read()
	require.NoError(t, rig.save(card, map[string][]string{"a": {foldKeep}, "b": {foldGone1, foldGone2}}))

	foldRows := func() map[string]string {
		t.Helper()
		rows, err := testDB.QueryContext(ctx,
			`SELECT fold_key, piece_line_key FROM tech_card_piece_fold WHERE tech_card_id = ?`, cardID)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, p string
			require.NoError(t, rows.Scan(&k, &p))
			out[k] = p
		}
		require.NoError(t, rows.Err())
		return out
	}
	require.Len(t, foldRows(), 3)

	// ── a save that keeps every piece drops no fold ──
	card, links := rig.read()
	require.NoError(t, rig.save(card, links))
	require.Len(t, foldRows(), 3)

	// ── an unknown key on a step is still refused, and the refusal rolls the piece delete back ──
	card, _ = rig.read()
	edit := card
	for _, p := range card.Pieces {
		if p.LineKey == pieceKeep {
			edit.Pieces = []entity.TechCardPiece{p}
		}
	}
	err = rig.save(edit, map[string][]string{"a": {foldKeep, "01PDFXDZ000000000000000009"}, "b": {foldGone1}})
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, string(entity.TechCardFoldLinkUnknown), ve.Reason)
	require.Len(t, foldRows(), 3, "the refused save deleted nothing")

	// ── the piece goes: its folds go with it, the step that still names them saves without them ──
	card, _ = rig.read()
	edit = card
	for _, p := range card.Pieces {
		if p.LineKey == pieceKeep {
			edit.Pieces = []entity.TechCardPiece{p}
		}
	}
	require.NoError(t, rig.save(edit, map[string][]string{"a": {foldKeep}, "b": {foldGone1, foldGone2}}))
	require.Equal(t, map[string]string{foldKeep: pieceKeep}, foldRows())
	card, links = rig.read()
	require.Equal(t, []string{foldKeep}, links["a"])
	require.Empty(t, links["b"])
	require.Len(t, card.PieceFolds, 1)
	require.Equal(t, foldKeep, card.PieceFolds[0].FoldKey)
}
