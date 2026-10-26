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

// TestTechCardOperationSeams drives tech_card_operation_seam (0434) through the real store: a step's
// ordered seam picks survive the card save and the read, an unknown key is refused field-addressed,
// deleting a seam drops its key from the steps in the delete's own transaction, and an anchor's hand
// placement plus an inner-line edge hint ("i3") round-trip through the seam JSON.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardOperationSeams(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()

	// Own small card, not rtBuildMaximalCard: that fixture is known-red since one-file-per-slot.
	const pieceFront, pieceBack, pieceCuff = "01OPSEAMPIECEFRONT00000001", "01OPSEAMPIECEBACK000000002", "01OPSEAMPIECECUFF000000003"
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	piece := func(key, name string) entity.TechCardPiece {
		return entity.TechCardPiece{LineKey: key, Name: name, PiecesPerGarment: 1, Grainline: "lengthwise"}
	}
	step := func(note string) entity.TechCardOperation {
		return entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
			Zone: entity.ZoneOuter, Note: ns(note)}
	}
	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "OP SEAMS", Stage: entity.TechCardStageProto, StyleNumber: ns("OPS-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Pieces:     []entity.TechCardPiece{piece(pieceFront, "front"), piece(pieceBack, "back"), piece(pieceCuff, "cuff")},
		Operations: []entity.TechCardOperation{step("join shoulders"), step("set cuff")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })
	fx := struct{ cardID int }{cardID}

	const k1 = "01QPSEAM000000000000000001"
	const k2 = "01QPSEAM000000000000000002"
	anchor := func(piece, hint string) entity.TechCardSeamAnchor {
		return entity.TechCardSeamAnchor{
			PieceLineKey: piece,
			Samples:      []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4}},
			PerimShare:   0.125, LenMm: 142.5, Notches: 1, TurnDeg: 0, EdgeHint: hint,
		}
	}
	placed := anchor(pieceCuff, "i3")
	placed.Placement = &entity.TechCardSeamPlacement{XMm: -12.5, YMm: 40.25, RotDeg: 270, Flip: true}
	seam := func(key string, a, b entity.TechCardSeamAnchor) entity.TechCardSeamInput {
		return entity.TechCardSeamInput{
			SeamKey: key, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
			Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceManual,
			SideA: []entity.TechCardSeamAnchor{a}, SideB: []entity.TechCardSeamAnchor{b},
		}
	}
	_, _, err = T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{TechCardId: fx.cardID, By: "ann",
		Seams: []entity.TechCardSeamInput{
			seam(k1, anchor(pieceFront, "FRONT#2"), anchor(pieceBack, "BACK#8")),
			seam(k2, anchor(pieceFront, "FRONT#5"), placed),
		}})
	require.NoError(t, err)

	// ── placement + inner-line hint round-trip through the seam JSON ──
	seams, err := T.GetTechCardSeams(ctx, fx.cardID)
	require.NoError(t, err)
	require.Len(t, seams, 2)
	require.Equal(t, k2, seams[1].SeamKey)
	gotB := seams[1].SideB[0]
	require.Equal(t, "i3", gotB.EdgeHint, "an inner-line hint is stored as sent")
	require.NotNil(t, gotB.Placement)
	require.InDelta(t, -12.5, gotB.Placement.XMm, 1e-9)
	require.InDelta(t, 40.25, gotB.Placement.YMm, 1e-9)
	require.InDelta(t, 270, gotB.Placement.RotDeg, 1e-9)
	require.True(t, gotB.Placement.Flip)
	require.Nil(t, seams[0].SideA[0].Placement, "no placement stays absent")

	// ── save a step with [k2, k1] → the read returns that order ──
	stored, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Len(t, stored.Operations, 2)
	edit := stored.TechCardInsert
	edit.Operations = append([]entity.TechCardOperation(nil), stored.Operations...)
	edit.Operations[0].SeamKeys = []string{k2, k1}
	require.NoError(t, T.UpdateTechCard(ctx, fx.cardID, &edit, stored.LockVersion))

	saved, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Equal(t, []string{k2, k1}, saved.Operations[0].SeamKeys, "sewing order survives")
	for i := 1; i < len(saved.Operations); i++ {
		require.Empty(t, saved.Operations[i].SeamKeys, "step %d names no seams", i)
	}
	require.Equal(t, stored.LockVersion+1, saved.LockVersion, "a card save bumps lock_version as before")

	// ── the stored card saved back unchanged keeps the picks (full replace rewrites them) ──
	again := saved.TechCardInsert
	require.NoError(t, T.UpdateTechCard(ctx, fx.cardID, &again, saved.LockVersion))
	reread, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Equal(t, []string{k2, k1}, reread.Operations[0].SeamKeys)

	// ── an unknown key is refused with the field named; nothing is written ──
	bad := reread.TechCardInsert
	bad.Operations = append([]entity.TechCardOperation(nil), reread.Operations...)
	bad.Operations[0].SeamKeys = []string{k1, "01QPSEAM0000000000000000ZZ"}
	err = T.UpdateTechCard(ctx, fx.cardID, &bad, reread.LockVersion)
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "operations[0].seam_keys[1]", ve.Field)
	untouched, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Equal(t, reread.LockVersion, untouched.LockVersion, "the refused save rolled back")
	require.Equal(t, []string{k2, k1}, untouched.Operations[0].SeamKeys)

	// ── delete seam k1 → the step keeps [k2]; the seam delete does not move lock_version ──
	_, deleted, err := T.DeleteTechCardSeams(ctx, fx.cardID, []string{k1})
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	afterDelete, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Equal(t, []string{k2}, afterDelete.Operations[0].SeamKeys)
	require.Equal(t, untouched.LockVersion, afterDelete.LockVersion, "seam writes never bump lock_version")

	// ── and the card stays savable as read ──
	final := afterDelete.TechCardInsert
	require.NoError(t, T.UpdateTechCard(ctx, fx.cardID, &final, afterDelete.LockVersion))
}
