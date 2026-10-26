package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardFindingSeenMarks pins the store side of the T9 «seen» marks (migration 0435):
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

	// --- an unknown card fails the card-row lock, which the handler maps to NotFound ---
	var maxID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM tech_card").Scan(&maxID))
	err = tc.MarkTechCardFindingSeen(ctx, maxID+1_000_000, key, fpOld, "anna")
	require.Error(t, err)
	require.True(t, errors.Is(err, sql.ErrNoRows), "%v", err)

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

// TestTechCardFindingSeenCap pins the per-card ceiling (entity.TechCardFindingSeenMaxPerCard): the
// last free slot is taken, a NEW key past it is refused with ErrTechCardFindingSeenLimit, a key the
// card already keeps is re-marked at the ceiling, an unmark frees a slot — and two concurrent new
// marks racing for the last slot get exactly one row.
func TestTechCardFindingSeenCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()
	const fp = "0123456789abcdef"
	const limit = entity.TechCardFindingSeenMaxPerCard

	// fill seeds n marks pdf_fill@card#0…n-1 in one statement.
	fill := func(cardID, n int) {
		t.Helper()
		values := make([]string, 0, n)
		args := make([]any, 0, n*2)
		for i := 0; i < n; i++ {
			values = append(values, "(?, ?, '"+fp+"', 'seed', CURRENT_TIMESTAMP(6))")
			args = append(args, cardID, fmt.Sprintf("pdf_fill@card#%d", i))
		}
		_, err := testDB.ExecContext(ctx, "INSERT INTO tech_card_finding_seen "+
			"(tech_card_id, finding_key, fingerprint, seen_by, seen_at) VALUES "+strings.Join(values, ", "), args...)
		require.NoError(t, err)
	}
	count := func(cardID int) int {
		t.Helper()
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_finding_seen WHERE tech_card_id = ?", cardID).Scan(&n))
		return n
	}

	cardID := seedSpineStyle(ctx, t, "T9-SEEN-CAP")
	fill(cardID, limit-1)

	// The last free slot is taken.
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, "pdf_last@card", fp, "anna"))
	require.Equal(t, limit, count(cardID))

	// A new key past the ceiling is refused, and nothing is written.
	err = tc.MarkTechCardFindingSeen(ctx, cardID, "pdf_over@card", fp, "anna")
	require.True(t, errors.Is(err, entity.ErrTechCardFindingSeenLimit), "%v", err)
	require.Equal(t, limit, count(cardID))

	// A key the card keeps is re-marked at the ceiling — same fingerprint and a new one.
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, "pdf_last@card", fp, "boris"))
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, "pdf_fill@card#7", "fedcba9876543210", "boris"))
	require.Equal(t, limit, count(cardID))

	// An unmark frees a slot for a new key.
	require.NoError(t, tc.UnmarkTechCardFindingSeen(ctx, cardID, "pdf_fill@card#0"))
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, cardID, "pdf_over@card", fp, "anna"))
	require.Equal(t, limit, count(cardID))

	// The ceiling is per card: another card is not affected.
	other := seedSpineStyle(ctx, t, "T9-SEEN-CAP-2")
	require.NoError(t, tc.MarkTechCardFindingSeen(ctx, other, "pdf_over@card", fp, "anna"))

	// Two new keys race for the last slot: the card-row lock lets exactly one in.
	race := seedSpineStyle(ctx, t, "T9-SEEN-CAP-RACE")
	fill(race, limit-1)
	errs := make([]error, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = tc.MarkTechCardFindingSeen(ctx, race, fmt.Sprintf("asm_race@card#%d", i), fp, "anna")
		}(i)
	}
	close(start)
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		require.True(t, errors.Is(err, entity.ErrTechCardFindingSeenLimit), "%v", err)
	}
	require.Equal(t, 1, won)
	require.Equal(t, limit, count(race))
}
