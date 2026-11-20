package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardRecent pins the store side of the home «recent techcards» (migration 0485): a touch
// upserts, the list is the account's own cards latest first, obsolete and system cards are skipped,
// the limit is clamped, each account keeps only its latest entity.RecentTechCardsKeep rows, and
// both FKs cascade.
//
// Integration test: runs only against a real MySQL (TestMain connects + migrates).
func TestTechCardRecent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	anna, _ := insertAdminFixture(ctx, t, "recent-anna")
	boris, _ := insertAdminFixture(ctx, t, "recent-boris")
	c1 := seedSpineStyle(ctx, t, "RECENT-1")
	c2 := seedSpineStyle(ctx, t, "RECENT-2")
	c3 := seedSpineStyle(ctx, t, "RECENT-3")

	ids := func(account, limit int) []int {
		t.Helper()
		rows, err := tc.ListRecentTechCards(ctx, account, limit)
		require.NoError(t, err)
		out := make([]int, len(rows))
		for i, r := range rows {
			out[i] = r.Card.Id
			require.False(t, r.TouchedAt.IsZero())
		}
		return out
	}
	age := func(account, card int, at time.Time) {
		t.Helper()
		_, err := testDB.ExecContext(ctx,
			`UPDATE tech_card_recent SET touched_at = ? WHERE account_id = ? AND tech_card_id = ?`, at, account, card)
		require.NoError(t, err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// --- nothing touched: an empty list, not nil-vs-error ---
	empty, err := tc.ListRecentTechCards(ctx, anna, 0)
	require.NoError(t, err)
	require.Empty(t, empty)

	// --- touch → list, latest first; another account never shows up ---
	require.NoError(t, tc.TouchTechCardRecent(ctx, anna, c1))
	require.NoError(t, tc.TouchTechCardRecent(ctx, anna, c2))
	require.NoError(t, tc.TouchTechCardRecent(ctx, boris, c3))
	age(anna, c1, base)
	age(anna, c2, base.Add(time.Minute))
	age(boris, c3, base)
	require.Equal(t, []int{c2, c1}, ids(anna, 0))
	require.Equal(t, []int{c3}, ids(boris, 0))

	// --- a re-touch moves the card to the top (upsert, one row per pair) ---
	require.NoError(t, tc.TouchTechCardRecent(ctx, anna, c1))
	require.Equal(t, []int{c1, c2}, ids(anna, 0))
	var pairs int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_recent WHERE account_id = ?`, anna).Scan(&pairs))
	require.Equal(t, 2, pairs)

	// --- the list carries the header, decorated like ListTechCards ---
	rows, err := tc.ListRecentTechCards(ctx, anna, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, c1, rows[0].Card.Id)
	require.Equal(t, "RECENT-1", rows[0].Card.Name)

	// --- archived (obsolete) and system cards are skipped, their rows kept ---
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card SET approval_state = 'obsolete' WHERE id = ?`, c2)
	require.NoError(t, err)
	require.Equal(t, []int{c1}, ids(anna, 0))
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card SET approval_state = 'draft' WHERE id = ?`, c2)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card SET create_request_id = ? WHERE id = ?`,
		fmt.Sprintf("%srecent-test-%d", entity.TechCardSystemKeyPrefix, c1), c1)
	require.NoError(t, err)
	require.Equal(t, []int{c2}, ids(anna, 0))
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card SET create_request_id = NULL WHERE id = ?`, c1)
	require.NoError(t, err)
	require.Equal(t, []int{c1, c2}, ids(anna, 0))

	// --- an unknown card fails the FK (the handler treats it as best-effort) ---
	require.Error(t, tc.TouchTechCardRecent(ctx, anna, 2_000_000_000))

	// --- keep the latest RecentTechCardsKeep per account; the limit is clamped ---
	many := make([]int, 0, entity.RecentTechCardsKeep+2)
	for i := 0; i < entity.RecentTechCardsKeep+2; i++ {
		many = append(many, seedSpineStyle(ctx, t, fmt.Sprintf("RECENT-K%02d", i)))
	}
	for i, id := range many {
		require.NoError(t, tc.TouchTechCardRecent(ctx, boris, id))
		age(boris, id, base.Add(time.Duration(i+1)*time.Minute))
	}
	// One more touch runs the trim with the aged order in place.
	last := many[len(many)-1]
	require.NoError(t, tc.TouchTechCardRecent(ctx, boris, last))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_recent WHERE account_id = ?`, boris).Scan(&pairs))
	require.Equal(t, entity.RecentTechCardsKeep, pairs)
	var kept int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_recent WHERE account_id = ? AND tech_card_id IN (?, ?, ?)`,
		boris, c3, many[0], many[1]).Scan(&kept))
	require.Zero(t, kept, "the three oldest touches must be trimmed")
	require.Len(t, ids(boris, 0), entity.RecentTechCardsDefault)
	require.Len(t, ids(boris, 999), entity.RecentTechCardsMax)
	require.Equal(t, last, ids(boris, 1)[0])
	// The trim is per account: anna's rows are untouched.
	require.Equal(t, []int{c1, c2}, ids(anna, 0))

	// --- deleting a card or an account takes its rows ---
	_, err = testDB.ExecContext(ctx, `DELETE FROM tech_card WHERE id = ?`, c2)
	require.NoError(t, err)
	require.Equal(t, []int{c1}, ids(anna, 0))
	_, err = testDB.ExecContext(ctx, `DELETE FROM admins WHERE id = ?`, anna)
	require.NoError(t, err)
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_recent WHERE account_id = ?`, anna).Scan(&pairs))
	require.Zero(t, pairs)
}
