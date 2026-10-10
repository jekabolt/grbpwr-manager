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

// TestTechCardSeamsRoundTrip drives tech_card_seam (0411) through the real store: keyed upsert,
// echo, the card read, server-side staleness, re-confirm, piece membership, delete, released card.
// What it proves is what no unit test can: the migration, the INSERT … ON DUPLICATE KEY, the JSON
// sides and the SELECT agree, and the seam writes never move lock_version.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go / project memory
// (store-tests-drop-prod-db: the non-CI TestMain talks to the configured DB and DROPs tables).
func TestTechCardSeamsRoundTrip(t *testing.T) {
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

	const key1 = "01SEAMRT000000000000000001"
	const key2 = "01SEAMRT000000000000000002"
	anchor := func(piece, hint string) entity.TechCardSeamAnchor {
		return entity.TechCardSeamAnchor{
			PieceLineKey: piece,
			Samples:      []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4}},
			PerimShare:   0.125, LenMm: 142.5, Notches: 2, TurnDeg: -3.5,
			RangeFrom: 0, RangeTo: 0, EdgeHint: hint, ContourSig: "abcdef0123456789",
		}
	}
	shoulder := entity.TechCardSeamInput{
		SeamKey: key1, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
		Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceGraph,
		SideA:        []entity.TechCardSeamAnchor{anchor(rtPieceFront, "FRONT#2")},
		SideB:        []entity.TechCardSeamAnchor{anchor(rtPieceBack, "BACK#8")},
		AnchoredSize: "M", Note: "shoulder",
	}

	before, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Empty(t, before.Seams, "a card nobody reviewed has no seams — an empty list, not an error")

	// ── write → echo ──
	list, written, err := T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: fx.cardID, By: "ann", Seams: []entity.TechCardSeamInput{shoulder},
	})
	require.NoError(t, err)
	require.Equal(t, 1, written)
	require.Len(t, list, 1)
	got := list[0]
	require.Equal(t, key1, got.SeamKey)
	require.False(t, got.Stale, "freshly written on today's sheets")
	require.Len(t, got.SourceFingerprint, 64)
	// MySQL's JSON type re-serialises numbers, so the anchors are compared with a tolerance far below
	// the client's 3-decimal rounding rather than byte for byte.
	seamRTSameSide(t, shoulder.SideA, got.SideA)
	seamRTSameSide(t, shoulder.SideB, got.SideB)
	require.Equal(t, "ann", got.CreatedBy)
	require.Equal(t, "ann", got.UpdatedBy)
	require.False(t, got.SizeId.Valid, "v1 writes no per-size rows")

	// ── the card read carries it; lock_version did not move; digests did not move ──
	after, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Len(t, after.Seams, 1)
	require.Equal(t, before.LockVersion, after.LockVersion, "seam writes never bump lock_version")
	require.Equal(t,
		dto.ConvertEntityTechCardToPb(before, dto.CostingFx{}).GetSectionDigests(),
		dto.ConvertEntityTechCardToPb(after, dto.CostingFx{}).GetSectionDigests(),
		"seams enter no section digest")
	require.Len(t, dto.ConvertEntityTechCardToPb(after, dto.CostingFx{}).GetSeams(), 1)

	// ── keyed replace: same key, new decision; created_* kept, updated_* moved; a second row added ──
	rejected := shoulder
	rejected.Status = entity.TechCardSeamRejected
	rejected.Note = "wrong shoulder"
	cuff := shoulder
	cuff.SeamKey = key2
	cuff.SideB = []entity.TechCardSeamAnchor{anchor(rtPieceCuff, "CUFF#1")}
	list, written, err = T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: fx.cardID, By: "bob", Seams: []entity.TechCardSeamInput{rejected, cuff},
	})
	require.NoError(t, err)
	require.Equal(t, 2, written)
	require.Len(t, list, 2)
	require.Equal(t, key1, list[0].SeamKey, "insertion order: the replaced row keeps its place")
	require.Equal(t, entity.TechCardSeamRejected, list[0].Status)
	require.Equal(t, "wrong shoulder", list[0].Note)
	require.Equal(t, "ann", list[0].CreatedBy)
	require.Equal(t, "bob", list[0].UpdatedBy)

	// ── a piece not on this card is refused with the field named ──
	foreign := shoulder
	foreign.SideB = []entity.TechCardSeamAnchor{anchor("01NOTAPIECEOFTHISCARD00000", "X#1")}
	_, _, err = T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: fx.cardID, By: "ann", Seams: []entity.TechCardSeamInput{foreign},
	})
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "seams[0].side_b.parts[0].piece_line_key", ve.Field)

	// ── server staleness: re-upload a sheet → every read says stale; re-confirm clears it ──
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_size_pattern SET version = version + 1 WHERE tech_card_id = ? AND line_key = ?",
		fx.cardID, rtSheetGraded)
	require.NoError(t, err)
	list, err = T.GetTechCardSeams(ctx, fx.cardID)
	require.NoError(t, err)
	require.True(t, list[0].Stale, "the sheet moved under the decision")
	require.True(t, list[1].Stale)
	list, _, err = T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: fx.cardID, By: "ann", Seams: []entity.TechCardSeamInput{rejected},
	})
	require.NoError(t, err)
	require.False(t, list[0].Stale, "re-confirm rewrites the fingerprint from today's sources")
	require.True(t, list[1].Stale, "a row nobody re-confirmed stays stale")

	// ── delete: known key removed, unknown key is not an error ──
	list, deleted, err := T.DeleteTechCardSeams(ctx, fx.cardID, []string{strings.ToLower(key2), "01SEAMRT0000000000000000ZZ"})
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.Len(t, list, 1)
	require.Equal(t, key1, list[0].SeamKey)

	// ── a released card refuses both writes ──
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'released' WHERE id = ?", fx.cardID)
	require.NoError(t, err)
	_, _, err = T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: fx.cardID, By: "ann", Seams: []entity.TechCardSeamInput{shoulder},
	})
	require.ErrorIs(t, err, entity.ErrTechCardReleased)
	_, _, err = T.DeleteTechCardSeams(ctx, fx.cardID, []string{key1})
	require.ErrorIs(t, err, entity.ErrTechCardReleased)
}

func seamRTSameSide(t *testing.T, want, got []entity.TechCardSeamAnchor) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		w, g := want[i], got[i]
		require.Equal(t, w.PieceLineKey, g.PieceLineKey)
		require.Equal(t, w.EdgeHint, g.EdgeHint)
		require.Equal(t, w.ContourSig, g.ContourSig)
		require.Equal(t, w.Notches, g.Notches)
		require.Len(t, g.Samples, len(w.Samples))
		for k := range w.Samples {
			require.InDelta(t, w.Samples[k].U, g.Samples[k].U, 1e-9)
			require.InDelta(t, w.Samples[k].V, g.Samples[k].V, 1e-9)
		}
		for _, p := range [][2]float64{{w.PerimShare, g.PerimShare}, {w.LenMm, g.LenMm}, {w.TurnDeg, g.TurnDeg},
			{w.RangeFrom, g.RangeFrom}, {w.RangeTo, g.RangeTo}} {
			require.InDelta(t, p[0], p[1], 1e-9)
		}
	}
}
