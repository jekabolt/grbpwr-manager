package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// TestTechCardRestoreBringsBackFoldsOfARestoredPiece: the real path through the admin handler and the
// store. A save that deletes a piece deletes its folds in the same transaction; restoring the snapshot
// taken before that save brings the piece back AND its fold rows (every field, read from the snapshot's
// pieceFolds), so the step that closed those folds gets its links back. The piece that kept its fold is
// not touched, and the card's lock_version moves exactly once.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardRestoreBringsBackFoldsOfARestoredPiece(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	rt := foldArchRig(t, ctx)
	rt.store.ts = time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)
	T := rt.store.TechCards()

	const (
		pieceKeep = "01RFOLDPIECEKEEP0000000001"
		pieceGone = "01RFOLDPIECEGONE0000000002"
		foldKeep  = "01RFXD00000000000000000001"
		foldGone1 = "01RFXD00000000000000000002"
		foldGone2 = "01RFXD00000000000000000003"
		restorer  = "restore.owner@example.com"
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
		Name: "RESTORE FOLDS", Stage: entity.TechCardStageProto,
		StyleNumber:     ns(fmt.Sprintf("RFOLD-%d", time.Now().UnixNano())),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Purpose:    entity.TechCardPurposeSellable,
		Pieces:     []entity.TechCardPiece{piece(pieceKeep, "keep"), piece(pieceGone, "gone")},
		Operations: []entity.TechCardOperation{step("a"), step("b")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })
	rig := opFoldRig{t: t, ctx: ctx, T: T, cardID: cardID}

	gone1 := opFoldInput(foldGone1, pieceGone, 1)
	gone1.EdgeHint, gone1.Note, gone1.AnchoredSize = "hem", "turn twice", "M"
	gone1.Kind, gone1.OffsetMm, gone1.ContourSig = entity.TechCardPieceFoldOutward, 35, "sig-1"
	gone1.EdgeSamples = []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0}, {U: 0.5, V: 0}, {U: 0.75, V: 0}, {U: 1, V: 0}}
	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{TechCardId: cardID, By: "ann",
		Folds: []entity.TechCardPieceFoldInput{
			opFoldInput(foldKeep, pieceKeep, 1),
			gone1,
			opFoldInput(foldGone2, pieceGone, 2),
		}})
	require.NoError(t, err)
	card, _ := rig.read()
	require.NoError(t, rig.save(card, map[string][]string{"a": {foldKeep}, "b": {foldGone1, foldGone2}}))
	before, links := rig.read()
	require.Equal(t, []string{foldKeep}, links["a"])
	require.Equal(t, []string{foldGone1, foldGone2}, links["b"])
	foldsBefore := map[string]entity.TechCardPieceFold{}
	for _, f := range before.PieceFolds {
		foldsBefore[f.FoldKey] = f
	}
	require.Len(t, foldsBefore, 3)

	// ── the client's save without the gone piece, with an explicit checkpoint: its folds go ──
	rt.store.ts = rt.store.ts.Add(time.Hour)
	full := dto.ConvertEntityTechCardToPb(before, dto.CostingFx{})
	in := full.GetTechCard()
	cutsEveryOtherFlag(in)
	in.CutStrips = full.GetCutStrips()
	in.CutStripsAware = true
	in.TopstitchLinesAware = true
	kept := in.GetPieces()[:0]
	for _, p := range in.GetPieces() {
		if p.GetLineKey() != pieceGone {
			kept = append(kept, p)
		}
	}
	in.Pieces = kept
	_, err = rt.srv.UpdateTechCard(rt.adminCtx("ann"), &pb_admin.UpdateTechCardRequest{
		Id: int32(cardID), TechCard: in, ExpectedLockVersion: int32(before.LockVersion), SnapshotReason: "dxf-delete",
	})
	require.NoError(t, err)
	dropped, links := rig.read()
	require.Len(t, dropped.Pieces, 1)
	require.Len(t, dropped.PieceFolds, 1, "the save took the gone piece's folds")
	require.Equal(t, foldKeep, dropped.PieceFolds[0].FoldKey)
	require.Equal(t, []string{foldKeep}, links["a"])
	require.Empty(t, links["b"], "the links to the deleted folds were skipped")

	snaps, err := T.ListTechCardSnapshots(ctx, cardID, 0, 1)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	got, err := T.GetTechCardSnapshot(ctx, cardID, snaps[0].Id)
	require.NoError(t, err)
	require.Contains(t, got.Snapshot, `"pieceFolds"`, "the checkpoint carries the folds it held")

	// ── restore that checkpoint ──
	resp, err := rt.srv.RestoreTechCardSnapshot(rt.adminCtx(restorer), &pb_admin.RestoreTechCardSnapshotRequest{
		TechCardId: int32(cardID), SnapshotId: snaps[0].Id, ExpectedLockVersion: int32(dropped.LockVersion),
	})
	require.NoError(t, err)
	require.EqualValues(t, dropped.LockVersion+1, resp.GetTechCard().GetLockVersion())

	restored, links := rig.read()
	require.Equal(t, dropped.LockVersion+1, restored.LockVersion, "one restore moves the version once")
	require.Len(t, restored.Pieces, 2, "the piece is back")
	require.Equal(t, []string{foldKeep}, links["a"])
	require.Equal(t, []string{foldGone1, foldGone2}, links["b"], "the step closes the re-created folds again")

	require.Len(t, restored.PieceFolds, 3)
	for _, f := range restored.PieceFolds {
		was, ok := foldsBefore[f.FoldKey]
		require.True(t, ok)
		require.Equal(t, was.TechCardPieceFoldInput, f.TechCardPieceFoldInput, "every field comes back as the snapshot has it")
		require.False(t, f.Stale)
		if f.PieceLineKey == pieceKeep {
			require.Equal(t, "ann", f.UpdatedBy, "a piece that holds folds today is not touched")
			require.Equal(t, was.SourceFingerprint, f.SourceFingerprint)
			continue
		}
		require.Equal(t, restorer, f.CreatedBy, "a re-created row is written by the restorer")
		require.Equal(t, restorer, f.UpdatedBy)
	}

	// The response the client renders carries the same rows.
	require.Len(t, resp.GetTechCard().GetPieceFolds(), 3)
	var wireLinks []string
	for _, op := range resp.GetTechCard().GetTechCard().GetOperations() {
		if op.GetNote() == "b" {
			wireLinks = op.GetFoldKeys()
		}
	}
	require.Equal(t, []string{foldGone1, foldGone2}, wireLinks)
}
