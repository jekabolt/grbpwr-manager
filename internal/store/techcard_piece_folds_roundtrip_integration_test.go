package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardPieceFoldsRoundTrip drives tech_card_piece_fold (0443) through the real store: keyed
// upsert, echo, the card read, staleness, the seq rule over the stored list (a swap passes, a clash
// rolls back), piece membership, delete, released card — and lock_version never moves.
//
// SAFE ONLY against a local container DSN or CI (see the seams round trip).
func TestTechCardPieceFoldsRoundTrip(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	rig := newRTRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()

	const key1 = "01FDRT00000000000000000001"
	const key2 = "01FDRT00000000000000000002"
	const key3 = "01FDRT00000000000000000003"
	hem := entity.TechCardPieceFoldInput{
		FoldKey: key1, PieceLineKey: rtPieceFront, Seq: 1,
		Kind: entity.TechCardPieceFoldInward, Source: entity.TechCardPieceFoldSourceAcceptedHint,
		Line:        []entity.TechCardSeamSample{{U: 0, V: 0.1}, {U: 1, V: 0.1}},
		FlipAt:      entity.TechCardSeamSample{U: 0.5, V: 0.05},
		EdgeHint:    "FRONT#4",
		EdgeSamples: []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0}, {U: 0.5, V: 0}, {U: 0.75, V: 0}, {U: 1, V: 0}},
		OffsetMm:    30.25, ContourSig: "abcdef0123456789", AnchoredSize: "M", Note: "hem",
	}
	second := hem
	second.FoldKey, second.Seq, second.Kind, second.Source = key2, 2, entity.TechCardPieceFoldOutward, entity.TechCardPieceFoldSourceOwner
	second.EdgeHint, second.EdgeSamples, second.OffsetMm = "", nil, 0 // a (u, v)-only line

	before, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Empty(t, before.PieceFolds, "no folds is an empty list, not an error")

	// ── write → echo ──
	list, written, err := T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: "ann", Folds: []entity.TechCardPieceFoldInput{second, hem},
	})
	require.NoError(t, err)
	require.Equal(t, 2, written)
	require.Len(t, list, 2)
	require.Equal(t, key1, list[0].FoldKey, "(piece, seq) order, not call order")
	require.Equal(t, key2, list[1].FoldKey)
	got := list[0]
	require.False(t, got.Stale)
	require.Len(t, got.SourceFingerprint, 64)
	require.Equal(t, 1, got.Seq)
	require.Equal(t, entity.TechCardPieceFoldInward, got.Kind)
	require.Equal(t, entity.TechCardPieceFoldSourceAcceptedHint, got.Source)
	foldRTSamePoints(t, hem.Line, got.Line)
	foldRTSamePoints(t, hem.EdgeSamples, got.EdgeSamples)
	require.InDelta(t, 0.5, got.FlipAt.U, 1e-9)
	require.InDelta(t, 0.05, got.FlipAt.V, 1e-9)
	require.InDelta(t, 30.25, got.OffsetMm, 1e-9)
	require.Equal(t, "FRONT#4", got.EdgeHint)
	require.Equal(t, "M", got.AnchoredSize)
	require.Equal(t, "ann", got.CreatedBy)
	require.False(t, got.SizeId.Valid)
	require.Empty(t, list[1].EdgeSamples, "no locator reads back as no samples")
	require.Zero(t, list[1].OffsetMm)

	// ── the card read carries them; lock_version and digests did not move ──
	after, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Len(t, after.PieceFolds, 2)
	require.Equal(t, before.LockVersion, after.LockVersion, "fold writes never bump lock_version")
	require.Equal(t,
		dto.ConvertEntityTechCardToPb(before, dto.CostingFx{}).GetSectionDigests(),
		dto.ConvertEntityTechCardToPb(after, dto.CostingFx{}).GetSectionDigests(),
		"folds enter no section digest")
	require.Len(t, dto.ConvertEntityTechCardToPb(after, dto.CostingFx{}).GetPieceFolds(), 2)

	// ── a reorder that swaps two seqs in one call passes (the rule is judged after the write) ──
	swapA, swapB := hem, second
	swapA.Seq, swapB.Seq = 2, 1
	list, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: "bob", Folds: []entity.TechCardPieceFoldInput{swapA, swapB},
	})
	require.NoError(t, err)
	require.Equal(t, key2, list[0].FoldKey)
	require.Equal(t, key1, list[1].FoldKey)
	require.Equal(t, "ann", list[1].CreatedBy)
	require.Equal(t, "bob", list[1].UpdatedBy)

	// ── a new fold on a seq already stored for that piece is refused, and nothing is written ──
	clash := hem
	clash.FoldKey, clash.Seq = key3, 1
	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: "ann", Folds: []entity.TechCardPieceFoldInput{clash},
	})
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "folds[0].seq", ve.Field)
	list, err = T.GetTechCardPieceFolds(ctx, fx.cardID)
	require.NoError(t, err)
	require.Len(t, list, 2, "the refused write rolled back")

	// the same seq on ANOTHER piece is fine
	cuff := clash
	cuff.PieceLineKey = strings.ToLower(rtPieceCuff)
	list, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: "ann", Folds: []entity.TechCardPieceFoldInput{cuff},
	})
	require.NoError(t, err)
	require.Len(t, list, 3)

	// ── a piece not on this card is refused with the field named ──
	foreign := hem
	foreign.PieceLineKey = "01NOTAPIECEOFTHISCARD00000"
	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: "ann", Folds: []entity.TechCardPieceFoldInput{foreign},
	})
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "folds[0].piece_line_key", ve.Field)

	// ── staleness: re-upload a sheet → stale; a re-write clears that row only ──
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_size_pattern SET version = version + 1 WHERE tech_card_id = ? AND line_key = ?",
		fx.cardID, rtSheetGraded)
	require.NoError(t, err)
	list, err = T.GetTechCardPieceFolds(ctx, fx.cardID)
	require.NoError(t, err)
	staleFront := 0
	for _, f := range list {
		if strings.EqualFold(f.PieceLineKey, rtPieceFront) && f.Stale {
			staleFront++
		}
	}
	require.Equal(t, 2, staleFront, "the sheet moved under both front folds")
	list, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: "ann", Folds: []entity.TechCardPieceFoldInput{swapA},
	})
	require.NoError(t, err)
	for _, f := range list {
		switch f.FoldKey {
		case key1:
			require.False(t, f.Stale, "re-written on today's sources")
		case key2:
			require.True(t, f.Stale, "nobody re-wrote it")
		}
	}

	// ── a corrupted JSON column reads stale instead of failing the card read ──
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_piece_fold SET line_uv = JSON_OBJECT('u', 1) WHERE tech_card_id = ? AND fold_key = ?",
		fx.cardID, key3)
	require.NoError(t, err)
	_, err = T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	list, err = T.GetTechCardPieceFolds(ctx, fx.cardID)
	require.NoError(t, err)
	for _, f := range list {
		if f.FoldKey == key3 {
			require.True(t, f.Stale)
		}
	}

	// ── delete: known keys removed, an unknown key is not an error ──
	list, deleted, err := T.DeleteTechCardPieceFolds(ctx, fx.cardID, []string{strings.ToLower(key3), "01FDRT0000000000000000ZZZZ"})
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.Len(t, list, 2)

	// ── a released card refuses both writes ──
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'released' WHERE id = ?", fx.cardID)
	require.NoError(t, err)
	_, _, err = T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: "ann", Folds: []entity.TechCardPieceFoldInput{swapA},
	})
	require.ErrorIs(t, err, entity.ErrTechCardReleased)
	_, _, err = T.DeleteTechCardPieceFolds(ctx, fx.cardID, []string{key1})
	require.ErrorIs(t, err, entity.ErrTechCardReleased)
}

func foldRTSamePoints(t *testing.T, want, got []entity.TechCardSeamSample) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		require.InDelta(t, want[i].U, got[i].U, 1e-9)
		require.InDelta(t, want[i].V, got[i].V, 1e-9)
	}
}

// TestTechCardPieceFoldMigrationIsIdempotent: the boot applied 0443 on a fresh schema; re-running its
// Up body (as a mid-file crash would on the next boot) under ANSI_QUOTES — prod's global sql_mode —
// is a no-op, and the table keeps its UNIQUE key and cascade FK.
func TestTechCardPieceFoldMigrationIsIdempotent(t *testing.T) {
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
	up := migrationUpBody(t, "0443_tech_card_piece_fold.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "re-run %d", i+1)
	}
	var n int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_schema = DATABASE() AND table_name = 'tech_card_piece_fold'
		  AND constraint_name IN ('uniq_tcpf_card_key', 'fk_tcpf_tech_card')`).Scan(&n))
	require.Equal(t, 2, n)
}
