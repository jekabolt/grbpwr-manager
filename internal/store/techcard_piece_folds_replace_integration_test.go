package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

const (
	replaceFoldA        = "01FDSTEP000000000000000001"
	replaceFoldB        = "01FDSTEP000000000000000002"
	replaceFoldDelete   = "01FDSTEP000000000000000003"
	replaceFoldInserted = "01FDSTEP000000000000000004"
)

// TestTechCardPieceFoldsReplace drives the atomic delete+upsert path through the real store.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardPieceFoldsReplace(t *testing.T) {
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

	cardNo := 0
	newRig := func(t *testing.T) opFoldRig {
		t.Helper()
		cardNo++
		ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
		piece := func(key, name string) entity.TechCardPiece {
			return entity.TechCardPiece{LineKey: key, Name: name, PiecesPerGarment: 1, Grainline: "lengthwise"}
		}
		step := func(note string) entity.TechCardOperation {
			return entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
				Zone: entity.ZoneOuter, Note: ns(note)}
		}
		cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "REPLACE FOLDS", Stage: entity.TechCardStageProto,
			StyleNumber: ns(fmt.Sprintf("RPF-%d", cardNo)), MeasurementUnit: entity.TechCardUnitMm,
			ApprovalState: entity.TechCardApprovalDraft,
			Pieces: []entity.TechCardPiece{
				piece(opFoldPieceFlap, "flap"), piece(opFoldPieceBack, "back"),
			},
			Operations: []entity.TechCardOperation{step("a"), step("b"), step("c")},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })
		return opFoldRig{t: t, ctx: ctx, T: T, cardID: cardID}
	}
	write := func(t *testing.T, cardID int, folds ...entity.TechCardPieceFoldInput) {
		t.Helper()
		_, written, err := T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
			TechCardId: cardID, By: "ann", Folds: folds,
		})
		require.NoError(t, err)
		require.Equal(t, len(folds), written)
	}
	refused := func(t *testing.T, err error, field, reason string) {
		t.Helper()
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, field, ve.Field)
		require.Equal(t, reason, ve.Reason)
	}

	t.Run("double to single also unlinks the step", func(t *testing.T) {
		rig := newRig(t)
		write(t, rig.cardID,
			opFoldInput(replaceFoldA, opFoldPieceFlap, 1),
			opFoldInput(replaceFoldB, opFoldPieceFlap, 2))
		card, _ := rig.read()
		require.NoError(t, rig.save(card, map[string][]string{"a": {replaceFoldA, replaceFoldB}}))

		list, written, deleted, err := T.ReplaceTechCardPieceFolds(ctx, entity.TechCardPieceFoldsReplace{
			TechCardId:     rig.cardID,
			DeleteFoldKeys: []string{replaceFoldB},
			Folds:          []entity.TechCardPieceFoldInput{opFoldInput(replaceFoldA, opFoldPieceFlap, 1)},
			By:             "bob",
		})
		require.NoError(t, err)
		require.Equal(t, 1, written)
		require.Equal(t, 1, deleted)
		require.Len(t, list, 1)
		require.Equal(t, replaceFoldA, list[0].FoldKey)
		_, links := rig.read()
		require.Equal(t, []string{replaceFoldA}, links["a"])
	})

	t.Run("step order refusal rolls back deletes and inserts", func(t *testing.T) {
		rig := newRig(t)
		write(t, rig.cardID,
			opFoldInput(replaceFoldA, opFoldPieceFlap, 1),
			opFoldInput(replaceFoldB, opFoldPieceFlap, 2),
			opFoldInput(replaceFoldDelete, opFoldPieceFlap, 3))
		card, _ := rig.read()
		require.NoError(t, rig.save(card, map[string][]string{"a": {replaceFoldA}, "b": {replaceFoldB}}))

		_, _, _, err := T.ReplaceTechCardPieceFolds(ctx, entity.TechCardPieceFoldsReplace{
			TechCardId:     rig.cardID,
			DeleteFoldKeys: []string{replaceFoldDelete},
			Folds: []entity.TechCardPieceFoldInput{
				opFoldInput(replaceFoldA, opFoldPieceFlap, 2),
				opFoldInput(replaceFoldB, opFoldPieceFlap, 1),
				opFoldInput(replaceFoldInserted, opFoldPieceBack, 1),
			},
			By: "bob",
		})
		refused(t, err, "folds[0].seq", string(entity.TechCardFoldLinkOrder))

		list, err := T.GetTechCardPieceFolds(ctx, rig.cardID)
		require.NoError(t, err)
		require.Len(t, list, 3)
		byKey := make(map[string]int, len(list))
		for _, fold := range list {
			byKey[fold.FoldKey] = fold.Seq
		}
		require.Equal(t, 1, byKey[replaceFoldA])
		require.Equal(t, 2, byKey[replaceFoldB])
		require.Equal(t, 3, byKey[replaceFoldDelete], "the delete rolled back")
		_, inserted := byKey[replaceFoldInserted]
		require.False(t, inserted, "the insert rolled back")
	})

	t.Run("key conflict is refused", func(t *testing.T) {
		rig := newRig(t)
		write(t, rig.cardID, opFoldInput(replaceFoldA, opFoldPieceFlap, 1))

		_, _, _, err := T.ReplaceTechCardPieceFolds(ctx, entity.TechCardPieceFoldsReplace{
			TechCardId:     rig.cardID,
			DeleteFoldKeys: []string{replaceFoldA},
			Folds:          []entity.TechCardPieceFoldInput{opFoldInput(replaceFoldA, opFoldPieceFlap, 1)},
			By:             "bob",
		})
		refused(t, err, "delete_fold_keys[0]", "conflict")
		list, err := T.GetTechCardPieceFolds(ctx, rig.cardID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, replaceFoldA, list[0].FoldKey)
	})

	t.Run("two saved rows swap seq", func(t *testing.T) {
		rig := newRig(t)
		write(t, rig.cardID,
			opFoldInput(replaceFoldA, opFoldPieceFlap, 1),
			opFoldInput(replaceFoldB, opFoldPieceFlap, 2))

		list, written, deleted, err := T.ReplaceTechCardPieceFolds(ctx, entity.TechCardPieceFoldsReplace{
			TechCardId: rig.cardID,
			Folds: []entity.TechCardPieceFoldInput{
				opFoldInput(replaceFoldA, opFoldPieceFlap, 2),
				opFoldInput(replaceFoldB, opFoldPieceFlap, 1),
			},
			By: "bob",
		})
		require.NoError(t, err)
		require.Equal(t, 2, written)
		require.Zero(t, deleted)
		require.Len(t, list, 2)
		require.Equal(t, replaceFoldB, list[0].FoldKey)
		require.Equal(t, 1, list[0].Seq)
		require.Equal(t, replaceFoldA, list[1].FoldKey)
		require.Equal(t, 2, list[1].Seq)
	})

	t.Run("released card writes nothing", func(t *testing.T) {
		rig := newRig(t)
		write(t, rig.cardID, opFoldInput(replaceFoldA, opFoldPieceFlap, 1))
		_, err := testDB.ExecContext(ctx,
			"UPDATE tech_card SET approval_state = 'released' WHERE id = ?", rig.cardID)
		require.NoError(t, err)

		_, _, _, err = T.ReplaceTechCardPieceFolds(ctx, entity.TechCardPieceFoldsReplace{
			TechCardId:     rig.cardID,
			DeleteFoldKeys: []string{replaceFoldA},
			Folds:          []entity.TechCardPieceFoldInput{opFoldInput(replaceFoldB, opFoldPieceFlap, 1)},
			By:             "bob",
		})
		require.ErrorIs(t, err, entity.ErrTechCardReleased)
		list, err := T.GetTechCardPieceFolds(ctx, rig.cardID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, replaceFoldA, list[0].FoldKey)
	})
}
