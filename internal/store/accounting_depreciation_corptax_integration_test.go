package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestDepreciationCorpTaxEndToEnd exercises the fixed-asset depreciation + corporation-tax accrual
// feature (migration 0197) and the three adversarial-review fixes, against a real MySQL. TestMain
// connects + migrates (which seeds the chart of accounts incl. 6370/1225 from 0195 and 8010/2050 from
// 0196), so this reproduces the exact SQL path the beta backend runs.
//
// SAFE ONLY against a local container DSN: the store TestMain drops all tables on cleanup
// (mysql_test.go), so a prod/beta DSN would be destructive. The guard below refuses to run unless the
// DSN targets 127.0.0.1.
func TestDepreciationCorpTaxEndToEnd(t *testing.T) {
	// Only run in CI (which points MYSQL_* at a container) or when the DSN explicitly targets a local
	// container. Otherwise skip — a bare local `go test ./internal/store/...` uses config.toml's prod
	// DSN, and this suite's TestMain drops all tables on cleanup (see mysql_test.go / project memory).
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	dec := decimal.RequireFromString
	acct := s.Accounting()
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	var entryIDs []int
	var assetIDs []int
	closedPeriod := "2025-06-01"

	post := func(e entity.AcctJournalEntryInsert) {
		var id int
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e2 error
			id, _, e2 = rep.Accounting().CreateJournalEntry(ctx, e)
			return e2
		})
		require.NoError(t, err, "post %s", e.SourceKey)
		entryIDs = append(entryIDs, id)
	}
	createAsset := func(name string, cost decimal.Decimal, acquired time.Time, life int) int {
		var id int
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e2 error
			id, e2 = rep.Accounting().CreateFixedAsset(ctx, entity.FixedAssetInsert{
				Name: name, CostBase: cost, AcquiredOn: acquired, UsefulLifeMonths: life,
			})
			return e2
		})
		require.NoError(t, err, "create asset %s", name)
		assetIDs = append(assetIDs, id)
		return id
	}
	postDepr := func(upTo time.Time) (int, int) {
		var posted, skipped int
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e2 error
			posted, skipped, e2 = rep.Accounting().PostDepreciationDue(ctx, upTo)
			return e2
		})
		require.NoError(t, err)
		return posted, skipped
	}
	accrueCT := func(from, to time.Time, rate string) (decimal.Decimal, bool) {
		var amt decimal.Decimal
		var dup bool
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e2 error
			amt, dup, e2 = rep.Accounting().AccrueCorporationTax(ctx, from, to, dec(rate))
			return e2
		})
		require.NoError(t, err)
		return amt, dup
	}
	// Ledger balance for an account/side, summed across all this test's entries.
	sumAccount := func(code, side string) decimal.Decimal {
		var v string
		qerr := testDB.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(l.amount), 0)
			FROM acct_journal_line l
			JOIN acct_journal_entry e ON e.id = l.entry_id
			JOIN acct_account a ON a.id = l.account_id
			WHERE a.code = ? AND l.side = ?`, code, side).Scan(&v)
		require.NoError(t, qerr)
		return dec(v)
	}

	defer func() {
		for _, id := range entryIDs {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_journal_entry WHERE id = ?", id)
		}
		for _, id := range assetIDs {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM fixed_asset WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_period WHERE period = ?", closedPeriod)
	}()

	from := day(2026, time.January, 1)
	to := day(2027, time.January, 1) // exclusive

	// --- Profit scenario: revenue 3000, opex 200 ---
	post(entity.AcctJournalEntryInsert{
		OccurredAt: day(2026, time.January, 15), Description: "test revenue",
		SourceType: entity.AcctSourceManual, SourceKey: "manual:deprct-revenue", CreatedBy: "test",
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: "1010", Side: entity.AcctSideDebit, Amount: dec("3000")},
			{AccountCode: "4020", Side: entity.AcctSideCredit, Amount: dec("3000")},
		},
	})
	post(entity.AcctJournalEntryInsert{
		OccurredAt: day(2026, time.January, 20), Description: "test opex",
		SourceType: entity.AcctSourceManual, SourceKey: "manual:deprct-opex", CreatedBy: "test",
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: "6050", Side: entity.AcctSideDebit, Amount: dec("200")},
			{AccountCode: "1010", Side: entity.AcctSideCredit, Amount: dec("200")},
		},
	})

	// --- Depreciation: cost 1200, life 12m from 2026-01 → 100.00/month, telescoping to cost ---
	createAsset("Test sewing machine", dec("1200"), from, 12)
	posted, skipped := postDepr(day(2026, time.December, 31))
	require.Equal(t, 12, posted, "12 monthly charges posted")
	require.Equal(t, 0, skipped, "no closed periods yet")
	require.Equal(t, "1200.00", sumAccount("6370", "debit").StringFixed(2), "Σ depreciation charge == cost exactly")
	require.Equal(t, "1200.00", sumAccount("1225", "credit").StringFixed(2), "accumulated depreciation == cost")

	// Idempotent: re-running posts nothing new.
	posted2, _ := postDepr(day(2026, time.December, 31))
	require.Equal(t, 0, posted2, "re-run posts nothing (idempotent)")

	// --- Corporation tax: pre-tax profit = 3000 - 200 - 1200 = 1600; @19% = 304.00 ---
	amt, dup := accrueCT(from, to, "19")
	require.False(t, dup, "first accrual is not a duplicate")
	require.Equal(t, "304.00", amt.StringFixed(2), "CT = 19% of 1600 pre-tax profit")
	require.Equal(t, "304.00", sumAccount("8010", "debit").StringFixed(2), "8010 charge")
	require.Equal(t, "304.00", sumAccount("2050", "credit").StringFixed(2), "2050 income tax payable")

	// --- FRS 105: depreciation + tax lines populated, their caveats cleared ---
	frs, err := acct.GetFrs105Accounts(ctx, from, to)
	require.NoError(t, err)
	require.Equal(t, "1200.00", frs.Depreciation.StringFixed(2), "FRS105 depreciation line")
	require.Equal(t, "304.00", frs.Tax.StringFixed(2), "FRS105 tax line reads the 'tax' section (8010)")
	joined := strings.ToLower(strings.Join(frs.Caveats, " | "))
	require.NotContains(t, joined, "no corporation tax", "CT caveat cleared once accrued")
	require.NotContains(t, joined, "no depreciation", "depreciation caveat cleared once charged")

	// --- Finding 2: duplicate accrual returns the LEDGER amount, not a recompute ---
	// Add more revenue (profit would now imply a higher CT), then re-accrue the same period.
	post(entity.AcctJournalEntryInsert{
		OccurredAt: day(2026, time.February, 10), Description: "extra revenue",
		SourceType: entity.AcctSourceManual, SourceKey: "manual:deprct-revenue2", CreatedBy: "test",
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: "1010", Side: entity.AcctSideDebit, Amount: dec("1000")},
			{AccountCode: "4020", Side: entity.AcctSideCredit, Amount: dec("1000")},
		},
	})
	amtDup, isDup := accrueCT(from, to, "19")
	require.True(t, isDup, "second accrual for the same period is a duplicate")
	require.Equal(t, "304.00", amtDup.StringFixed(2), "returns the posted ledger amount (304), not the recomputed 494")
	require.Equal(t, "304.00", sumAccount("8010", "debit").StringFixed(2), "no second CT entry posted")

	// --- Finding 3: a period that flipped to a loss still surfaces the stale accrual ---
	post(entity.AcctJournalEntryInsert{
		OccurredAt: day(2026, time.February, 15), Description: "large expense -> loss",
		SourceType: entity.AcctSourceManual, SourceKey: "manual:deprct-bigexpense", CreatedBy: "test",
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: "6050", Side: entity.AcctSideDebit, Amount: dec("6000")},
			{AccountCode: "1010", Side: entity.AcctSideCredit, Amount: dec("6000")},
		},
	})
	amtLoss, isDupLoss := accrueCT(from, to, "19")
	require.True(t, isDupLoss, "loss period still reports the stale accrual as already-posted")
	require.Equal(t, "304.00", amtLoss.StringFixed(2), "stale 304 accrual surfaced, not a misleading 0")

	// --- Finding 1: a charge whose period is closed is counted as skipped, not silently dropped ---
	_, err = testDB.ExecContext(ctx, "INSERT INTO acct_period (period, status) VALUES (?, 'closed')", closedPeriod)
	require.NoError(t, err)
	createAsset("Back-dated asset", dec("100"), day(2025, time.June, 1), 1) // single charge in the closed month
	postedC, skippedC := postDepr(day(2025, time.June, 30))
	require.Equal(t, 0, postedC, "nothing posts into the closed period")
	require.Equal(t, 1, skippedC, "the closed-period charge is surfaced as skipped, not dropped silently")
}
