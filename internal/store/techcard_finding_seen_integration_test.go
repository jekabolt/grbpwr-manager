package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTechCardFindingSeenMarks pins the store side of the T9 «seen» marks (migration 0433):
// mark → list, an idempotent re-mark that keeps the first who/when, a new fingerprint that
// overwrites them, byte-exact keys, idempotent unmark, the FK on an unknown card and the cascade
// when the card is deleted.
//
// Integration test: runs only against a real MySQL (TestMain connects + migrates).
func TestTechCardFindingSeenMarks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	cardID := seedSpineStyle(ctx, t, "T9-SEEN")
	const key = "smv_missing@opfp:46a915ad"
	const fpOld = "0123456789abcdef"
	const fpNew = "fedcba9876543210"

	// --- mark → list ---
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, key, fpOld, "anna"))
	marks, err := tc.ListTechCardFindingSeen(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, marks, 1)
	require.Equal(t, cardID, marks[0].TechCardId)
	require.Equal(t, key, marks[0].FindingKey)
	require.Equal(t, fpOld, marks[0].Fingerprint)
	require.Equal(t, "anna", marks[0].SeenBy)
	require.False(t, marks[0].SeenAt.IsZero())

	// Age the row so "kept" and "overwritten" are distinguishable without sleeping.
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_finding_seen SET seen_at = ? WHERE tech_card_id = ? AND finding_key = ?", old, cardID, key)
	require.NoError(t, err)

	// --- same fingerprint again: idempotent, the first who/when stays ---
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, key, fpOld, "boris"))
	marks, err = tc.ListTechCardFindingSeen(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, marks, 1)
	require.Equal(t, "anna", marks[0].SeenBy)
	require.True(t, old.Equal(marks[0].SeenAt.UTC()), "seen_at moved: %v", marks[0].SeenAt)

	// --- new fingerprint: the mark is re-taken by whoever saw the new text ---
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, key, fpNew, "boris"))
	marks, err = tc.ListTechCardFindingSeen(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, marks, 1)
	require.Equal(t, fpNew, marks[0].Fingerprint)
	require.Equal(t, "boris", marks[0].SeenBy)
	require.True(t, marks[0].SeenAt.After(old), "seen_at not refreshed: %v", marks[0].SeenAt)

	// --- keys are byte-exact: unit keys «Base» and «base» are two units ---
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, "piece_never_sewn@unit:Base", fpOld, "anna"))
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, "piece_never_sewn@unit:base", fpOld, "anna"))
	marks, err = tc.ListTechCardFindingSeen(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, marks, 3)

	// --- unmark deletes one row; unmarking again is not an error ---
	require.NoError(t, tc.UnmarkTechCardFindingSeen(ctx, cardID, "piece_never_sewn@unit:Base"))
	require.NoError(t, tc.UnmarkTechCardFindingSeen(ctx, cardID, "piece_never_sewn@unit:Base"))
	marks, err = tc.ListTechCardFindingSeen(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, marks, 2)
	for _, m := range marks {
		require.NotEqual(t, "piece_never_sewn@unit:Base", m.FindingKey)
	}

	// --- an unknown card fails on the FK, which the handler maps to NotFound ---
	var maxID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM tech_card").Scan(&maxID))
	err = tc.MarkTechCardFindingSeen(ctx, maxID+1_000_000, key, fpOld, "anna")
	require.Error(t, err)
	require.True(t, s.IsErrForeignKeyViolation(err), "%v", err)

	// --- marks die with their card ---
	other := seedSpineStyle(ctx, t, "T9-SEEN-CASCADE")
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, other, key, fpOld, "anna"))
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", other)
	require.NoError(t, err)
	var left int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_finding_seen WHERE tech_card_id = ?", other).Scan(&left))
	require.Zero(t, left)

	// The first card's marks are untouched by the other card's delete.
	marks, err = tc.ListTechCardFindingSeen(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, marks, 2)
}
