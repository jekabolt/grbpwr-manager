package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAcctEventNeedsReviewLifecycle pins the H-1/H-2/B-5 disposition mechanism at the store layer: a
// needs_review event is listed + counted, BLOCKS ClosePeriod for its month, and clears on resolve or
// reprocess. It uses a far-PAST empty month (2024-01) so every other close gate passes and only the
// review gate can fire.
func TestAcctEventNeedsReviewLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	defer s.Close()
	acc := s.Accounting()

	const srcKey = "REVIEW-LIFECYCLE-1"
	occ := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_event WHERE source_key = ?", srcKey)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_period WHERE period = '2024-01-01'")
	})

	require.NoError(t, acc.EnqueueEvent(ctx, entity.AcctEventInsert{
		EventType:  entity.AcctEventOrderPaid,
		SourceKey:  srcKey,
		Payload:    entity.AcctOrderPaidPayload{OrderUUID: srcKey},
		OccurredAt: occ,
	}))
	var id int64
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM acct_event WHERE source_key = ?", srcKey).Scan(&id))

	// Flag it for review (the terminal disposition the worker uses for a non-EUR/degenerate order,
	// an orphan refund, or a dead-letter).
	require.NoError(t, acc.MarkEventNeedsReview(ctx, id, "degenerate amounts, manual entry required"))

	review, err := acc.ListEventsNeedingReview(ctx, 100)
	require.NoError(t, err)
	var found *entity.AcctEvent
	for i := range review {
		if review[i].Id == id {
			found = &review[i]
		}
	}
	require.NotNil(t, found, "event must appear in the review list")
	require.True(t, found.NeedsReview)
	require.True(t, found.ProcessedAt.Valid, "needs_review is terminal (processed, not retried)")

	cnt, err := acc.CountEventsNeedingReviewInPeriod(ctx, "2024-01-01", "2024-02-01")
	require.NoError(t, err)
	require.Equal(t, 1, cnt)

	// ClosePeriod is blocked by the review gate — the month cannot close over an unresolved manual item.
	err = acc.ClosePeriod(ctx, occ, "tester")
	require.ErrorIs(t, err, entity.ErrAcctPeriodNotReady)
	require.Contains(t, err.Error(), "need manual review")

	// Resolve (handled manually) → the review count clears.
	require.NoError(t, acc.ResolveAcctEvent(ctx, id))
	cnt, err = acc.CountEventsNeedingReviewInPeriod(ctx, "2024-01-01", "2024-02-01")
	require.NoError(t, err)
	require.Equal(t, 0, cnt, "resolve clears the review gate")

	// Reprocess a re-flagged event → back to pending (processed_at NULL, needs_review 0, attempts 0).
	require.NoError(t, acc.MarkEventNeedsReview(ctx, id, "again"))
	require.NoError(t, acc.ReprocessAcctEvent(ctx, id))
	var pa sql.NullTime
	var nr, attempts int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT processed_at, needs_review, attempts FROM acct_event WHERE id = ?", id).Scan(&pa, &nr, &attempts))
	require.False(t, pa.Valid, "reprocess clears processed_at (pending again)")
	require.Equal(t, 0, nr, "reprocess clears needs_review")
	require.Equal(t, 0, attempts, "reprocess resets attempts")
}
