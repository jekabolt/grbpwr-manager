package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acctDate builds a UTC date for a distinctive far-past/future month used only by this suite.
func acctDate(year int, m time.Month, day int) time.Time {
	return time.Date(year, m, day, 0, 0, 0, 0, time.UTC)
}

// balancedManualEntry builds a two-line balanced manual entry (Dr drCode / Cr crCode, amount).
func balancedManualEntry(sourceKey string, occurred time.Time, drCode, crCode, amount string) entity.AcctJournalEntryInsert {
	a := decimal.RequireFromString(amount)
	return entity.AcctJournalEntryInsert{
		OccurredAt:  occurred,
		Description: "acct core test " + sourceKey,
		SourceType:  entity.AcctSourceManual,
		SourceKey:   sourceKey,
		CreatedBy:   "tester",
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: drCode, Side: entity.AcctSideDebit, Amount: a},
			{AccountCode: crCode, Side: entity.AcctSideCredit, Amount: a},
		},
	}
}

// TestAccountingCore exercises the accounting store layer (docs/plan-accounting/02): balanced/
// unbalanced entries, (source_type, source_key) idempotency, the closed-period gate + reopen,
// reversal with its guards, account archive/system guards, ClosePeriod readiness, the trial-balance
// invariant, and that rep.Accounting() is non-nil inside a Tx. Uses distinctive months
// (2020/2021/2035) and cleans its own rows so it does not disturb other suites.
func TestAccountingCore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	acc := s.Accounting()

	// The store enforces its repo.Tx contract (CreateJournalEntry / ReverseJournalEntry refuse to
	// run on the pool — the InTx guard), so this suite posts the way production does: through
	// s.Tx. Domain errors pass through Tx unwrapped, so the ErrorIs assertions below still hold.
	createEntry := func(in entity.AcctJournalEntryInsert) (int, bool, error) {
		var id int
		var existed bool
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e error
			id, existed, e = rep.Accounting().CreateJournalEntry(ctx, in)
			return e
		})
		return id, existed, err
	}
	reverseEntry := func(entryID int, reason string) (int, error) {
		var id int
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e error
			id, e = rep.Accounting().ReverseJournalEntry(ctx, entryID, reason, "tester")
			return e
		})
		return id, err
	}

	t.Cleanup(func() {
		cctx := context.Background()
		// Break the self-referential reversal links (ON DELETE RESTRICT) before deleting.
		_, _ = testDB.ExecContext(cctx, "UPDATE acct_journal_entry SET reversed_by = NULL, reversal_of = NULL WHERE source_key LIKE 'ACCT-TEST%' OR source_key LIKE 'rev:%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_entry WHERE source_key LIKE 'ACCT-TEST%' OR source_key LIKE 'rev:%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_event WHERE source_key LIKE 'ACCT-TEST%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_period WHERE period IN ('2020-01-01','2021-02-01','2035-03-01','2035-06-01')")
		_, _ = testDB.ExecContext(cctx, "UPDATE acct_account SET archived = FALSE WHERE code = '1210'")
	})

	// --- balanced entry created; unbalanced rejected ---
	t.Run("balanced_and_unbalanced", func(t *testing.T) {
		id, existed, err := createEntry(balancedManualEntry("ACCT-TEST-balanced", acctDate(2035, 3, 15), "1030", "4020", "100.00"))
		require.NoError(t, err)
		require.False(t, existed)
		require.Positive(t, id)

		full, err := acc.GetJournalEntry(ctx, id)
		require.NoError(t, err)
		require.Len(t, full.Lines, 2)

		unbalanced := balancedManualEntry("ACCT-TEST-unbalanced", acctDate(2035, 3, 15), "1030", "4020", "100.00")
		unbalanced.Lines[1].Amount = decimal.RequireFromString("90.00") // credit != debit
		_, _, err = createEntry(unbalanced)
		require.ErrorIs(t, err, entity.ErrAcctUnbalanced)
	})

	// --- duplicate (source_type, source_key) is idempotent: same id, alreadyExists, no doubled lines ---
	t.Run("idempotent_duplicate", func(t *testing.T) {
		in := balancedManualEntry("ACCT-TEST-dup", acctDate(2035, 3, 15), "1030", "4020", "42.50")
		id1, existed1, err := createEntry(in)
		require.NoError(t, err)
		require.False(t, existed1)

		id2, existed2, err := createEntry(in)
		require.NoError(t, err)
		require.True(t, existed2)
		require.Equal(t, id1, id2)

		var lineCount int
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM acct_journal_line WHERE entry_id = ?", id1).Scan(&lineCount))
		require.Equal(t, 2, lineCount, "duplicate must not double the lines")
	})

	// --- unknown / archived account, and system-account guard ---
	t.Run("account_guards", func(t *testing.T) {
		_, _, err := createEntry(balancedManualEntry("ACCT-TEST-unknown", acctDate(2035, 3, 15), "9999", "1010", "10.00"))
		require.ErrorIs(t, err, entity.ErrAcctUnknownAccount)

		require.NoError(t, acc.SetAccountArchived(ctx, "1210", true)) // 1210 Prepaid Expenses is non-system
		_, _, err = createEntry(balancedManualEntry("ACCT-TEST-archived", acctDate(2035, 3, 15), "1210", "1010", "10.00"))
		require.ErrorIs(t, err, entity.ErrAcctArchivedAccount)
		require.NoError(t, acc.SetAccountArchived(ctx, "1210", false))

		err = acc.SetAccountArchived(ctx, "1030", true) // 1030 is a system account
		require.ErrorIs(t, err, entity.ErrAcctSystemAccount)
		err = acc.UpdateAccountName(ctx, "1030", "nope")
		require.ErrorIs(t, err, entity.ErrAcctSystemAccount)
	})

	// --- closed-period gate then reopen ---
	t.Run("closed_period_gate_and_reopen", func(t *testing.T) {
		_, err := testDB.ExecContext(ctx, "INSERT INTO acct_period (period, status) VALUES ('2020-01-01','closed') ON DUPLICATE KEY UPDATE status='closed'")
		require.NoError(t, err)

		_, _, err = createEntry(balancedManualEntry("ACCT-TEST-closed", acctDate(2020, 1, 15), "1030", "4020", "10.00"))
		require.ErrorIs(t, err, entity.ErrAcctPeriodClosed)

		require.NoError(t, acc.ReopenPeriod(ctx, acctDate(2020, 1, 1), "tester"))
		id, _, err := createEntry(balancedManualEntry("ACCT-TEST-reopened", acctDate(2020, 1, 15), "1030", "4020", "10.00"))
		require.NoError(t, err)
		require.Positive(t, id)
	})

	// --- reversal: mirror lines, reversed_by set, second reversal + reversing a reversal guarded ---
	t.Run("reversal", func(t *testing.T) {
		origID, _, err := createEntry(balancedManualEntry("ACCT-TEST-reversal", acctDate(2035, 3, 15), "1030", "4020", "77.00"))
		require.NoError(t, err)

		revID, err := reverseEntry(origID, "wrong amount")
		require.NoError(t, err)
		require.Positive(t, revID)

		rev, err := acc.GetJournalEntry(ctx, revID)
		require.NoError(t, err)
		require.Equal(t, entity.AcctSourceReversal, rev.Entry.SourceType)
		require.Equal(t, fmt.Sprintf("rev:%d", origID), rev.Entry.SourceKey)
		require.True(t, rev.Entry.ReversalOf.Valid)
		require.Equal(t, int32(origID), rev.Entry.ReversalOf.Int32)
		// Mirror: the 1030 line was debit on the original, must be credit on the reversal.
		for _, l := range rev.Lines {
			if l.AccountCode == "1030" {
				require.Equal(t, entity.AcctSideCredit, l.Side)
			}
			if l.AccountCode == "4020" {
				require.Equal(t, entity.AcctSideDebit, l.Side)
			}
		}

		orig, err := acc.GetJournalEntry(ctx, origID)
		require.NoError(t, err)
		require.True(t, orig.Entry.ReversedBy.Valid)
		require.Equal(t, int32(revID), orig.Entry.ReversedBy.Int32)

		// second reversal of the original -> already reversed
		_, err = reverseEntry(origID, "again")
		require.ErrorIs(t, err, entity.ErrAcctAlreadyReversed)

		// reversing a reversal is forbidden
		_, err = reverseEntry(revID, "no")
		require.ErrorIs(t, err, entity.ErrAcctCannotReverseReversal)
	})

	// --- ClosePeriod refuses a month with a pending outbox event ---
	t.Run("close_period_not_ready", func(t *testing.T) {
		require.NoError(t, acc.EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType:  entity.AcctEventOrderPaid,
			SourceKey:  "ACCT-TEST-pending-order",
			Payload:    entity.AcctOrderPaidPayload{OrderUUID: "ACCT-TEST-pending-order"},
			OccurredAt: acctDate(2021, 2, 15),
		}))
		err := acc.ClosePeriod(ctx, acctDate(2021, 2, 1), "tester")
		require.ErrorIs(t, err, entity.ErrAcctPeriodNotReady)
	})

	// --- trial-balance invariant: after several balanced entries, Σdebit == Σcredit for the month ---
	t.Run("trial_balance_invariant", func(t *testing.T) {
		amounts := []string{"12.34", "0.05", "1000.00", "3.33", "58.10"}
		pairs := [][2]string{{"1030", "4020"}, {"5010", "1130"}, {"6050", "1030"}, {"1030", "4010"}, {"5010", "1130"}}
		for i, amt := range amounts {
			_, _, err := createEntry(balancedManualEntry(
				fmt.Sprintf("ACCT-TEST-tb-%d", i), acctDate(2035, 6, 15), pairs[i][0], pairs[i][1], amt))
			require.NoError(t, err)
		}
		var dr, cr string
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(CASE WHEN l.side='debit'  THEN l.amount ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN l.side='credit' THEN l.amount ELSE 0 END), 0)
			FROM acct_journal_line l
			JOIN acct_journal_entry e ON e.id = l.entry_id
			WHERE e.occurred_at >= '2035-06-01' AND e.occurred_at < '2035-07-01'`).Scan(&dr, &cr))
		require.Equal(t, decimal.RequireFromString(dr).String(), decimal.RequireFromString(cr).String(), "trial balance must be balanced")
	})

	// --- DoD: rep.Accounting() is wired inside a Tx (initSubStoresForTx branch) ---
	t.Run("accounting_available_in_tx", func(t *testing.T) {
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			require.NotNil(t, rep.Accounting())
			accts, err := rep.Accounting().ListAccounts(ctx, false)
			if err != nil {
				return err
			}
			assert.NotEmpty(t, accts, "seeded chart of accounts should be visible in tx")
			return nil
		})
		require.NoError(t, err)
	})
}
