package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	acctrules "github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/metrics"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestAcctDashboardAlertQueries is the wave-0 techdebt item (docs/plan-accounting-phase2/
// 06-wave0-techdebt.md #2) integration coverage for the four accounting-module dashboard-alert
// queries in internal/store/metrics/dashboard.go: GetAcctModuleActive, GetAcctPostingLag,
// GetAcctManualEntryRequiredCount, GetAcctRevenueReconForMonth. It follows the
// TestDashboardAlertQueries (new_flow_alerts_integration_test.go) pattern: these queries live on
// *metrics.Store but read acct_* tables the metrics package does not otherwise own, and the metrics
// package itself has no live-DB test harness — so this store-level integration suite is their only
// direct exercise against real SQL (dashboard_test.go only unit-tests the pure
// buildAcctDashboardAlerts function against pre-computed inputs, never these four queries).
//
// Every fixture uses the 'ACCT-ALERT-' source_key/uuid prefix (swept in one t.Cleanup) and, for the
// ledger-posting scenario, the 2031-06 period — a month grepped clean of any other suite in this
// package, so GetAcctRevenueReconForMonth's aggregate sums are exact, not "baseline + delta". The
// counter-style assertions (posting lag / manual entry) instead compare a before/after snapshot, the
// same idiom TestDashboardAlertQueries uses for GetStaleOpenRunCount: acct_event and
// acct_journal_entry are shared, cumulative tables across this whole package's test run, so a bare
// "count == 1" assertion would be one bad neighbour away from flaking.
func TestAcctDashboardAlertQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Warm the order-status cache: GetAcctRevenueReconForMonth's operational half reads
	// cache.OrderStatusIDsForNetRevenue(), which NewForTest alone does not populate (see its own doc
	// comment "creates a new store instance for testing without initializing cache") — the same
	// warm-up TestAcctEventProducers / TestAcctPostingWorker perform before touching accounting.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	mtr, ok := s.Metrics().(*metrics.Store)
	require.True(t, ok, "metrics store is the concrete *metrics.Store")

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_event WHERE source_key LIKE 'ACCT-ALERT-%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_entry WHERE source_key LIKE 'ACCT-ALERT-%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE uuid LIKE 'ACCT-ALERT-%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_period WHERE period = '2031-06-01'")
	})

	// --- GetAcctModuleActive: false when acct_journal_entry is empty --------------------------------
	t.Run("module_inactive_when_ledger_empty", func(t *testing.T) {
		// acct_journal_entry is a shared, cumulative table across this whole package's test run (other
		// suites post real entries and clean most of them up in their own defers/t.Cleanup, but the
		// table is never GUARANTEED empty when this subtest happens to run) — so the only reliable way
		// to exercise GetAcctModuleActive's false branch is to wipe the table and prove the answer, then
		// discard the wipe. This runs inside a Tx whose callback always returns a sentinel error, the
		// same rollback-only technique TestAcctEventProducers' "event_rolls_back_with_tx" subtest uses:
		// the DELETE is visible to the SELECT within the same Tx (read-your-own-writes under
		// SERIALIZABLE), and is fully undone when the Tx rolls back — this probe leaves the shared DB
		// exactly as it found it, no cleanup needed. The self-referencing reversal_of/reversed_by
		// columns are nulled first so the bulk DELETE cannot trip the FK RESTRICT on any reversal pair
		// another suite may have posted (order-dependent otherwise: InnoDB does not delete
		// self-referencing rows parent-before-child within one statement).
		sentinel := errors.New("acct_alerts_test: probe-only rollback, must not persist")
		var active bool
		txErr := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			m, ok := rep.Metrics().(*metrics.Store)
			if !ok {
				return errors.New("tx-scoped metrics store is not *metrics.Store")
			}
			if _, e := m.DB.ExecContext(ctx, "UPDATE acct_journal_entry SET reversal_of = NULL, reversed_by = NULL"); e != nil {
				return e
			}
			if _, e := m.DB.ExecContext(ctx, "DELETE FROM acct_journal_entry"); e != nil {
				return e
			}
			var gErr error
			active, gErr = m.GetAcctModuleActive(ctx)
			if gErr != nil {
				return gErr
			}
			return sentinel
		})
		require.ErrorIs(t, txErr, sentinel, "the probe tx must roll back, leaving the shared ledger untouched")
		require.False(t, active, "GetAcctModuleActive is false when acct_journal_entry is empty")
	})

	// --- GetAcctPostingLag: an unprocessed acct_event older than the threshold is counted ------------
	t.Run("posting_lag_counts_a_stale_event", func(t *testing.T) {
		lagHours := entity.DefaultAlertThresholds().AcctPostingLagHours // 24h, per 06-wave0-techdebt.md #2
		beforeCount, _, err := mtr.GetAcctPostingLag(ctx, lagHours)
		require.NoError(t, err)

		const uuid = "ACCT-ALERT-LAG-0001"
		old := time.Now().UTC().Add(-time.Duration(lagHours+6) * time.Hour) // safely past the threshold
		require.NoError(t, s.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType:  entity.AcctEventOrderPaid,
			SourceKey:  uuid,
			Payload:    entity.AcctOrderPaidPayload{OrderUUID: uuid},
			OccurredAt: old,
		}))

		afterCount, maxAgeHours, err := mtr.GetAcctPostingLag(ctx, lagHours)
		require.NoError(t, err)
		require.Equal(t, beforeCount+1, afterCount, "one stale unprocessed event joins the backlog count")
		require.GreaterOrEqual(t, maxAgeHours, float64(lagHours+6),
			"the oldest-event age reflects this stale fixture (or an even older pre-existing backlog item)")
	})

	// --- GetAcctManualEntryRequiredCount: a processed event skipped for manual entry, within 30 days -
	t.Run("manual_entry_required_counts_a_recent_skip", func(t *testing.T) {
		before, err := mtr.GetAcctManualEntryRequiredCount(ctx)
		require.NoError(t, err)

		const uuid = "ACCT-ALERT-MANUAL-0001"
		require.NoError(t, s.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType:  entity.AcctEventOrderPaid,
			SourceKey:  uuid,
			Payload:    entity.AcctOrderPaidPayload{OrderUUID: uuid},
			OccurredAt: time.Now().UTC().Add(-24 * time.Hour), // well inside the 30-day lookback
		}))
		var evID int64
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM acct_event WHERE event_type = 'order_paid' AND source_key = ?", uuid).Scan(&evID))
		// Mirrors acctposting/outbox.go's skipEvent exactly (MarkEventFailed records the reason, then
		// MarkEventProcessed sets processed_at WITHOUT clearing last_error) — the disposition
		// GetAcctManualEntryRequiredCount scans for via `last_error LIKE '%manual entry required%'`.
		require.NoError(t, s.Accounting().MarkEventFailed(ctx, evID, "non-eur non-stripe order, manual entry required", 0))
		require.NoError(t, s.Accounting().MarkEventProcessed(ctx, evID))

		after, err := mtr.GetAcctManualEntryRequiredCount(ctx)
		require.NoError(t, err)
		require.Equal(t, before+1, after, "one recently-skipped manual-entry event joins the count")
	})

	// --- GetAcctRevenueReconForMonth: an order_sale NET+SHIP posting vs a drifted customer_order -----
	t.Run("revenue_recon_reports_ledger_vs_operational_drift", func(t *testing.T) {
		month := time.Date(2031, 6, 15, 0, 0, 0, 0, time.UTC)
		dec := decimal.RequireFromString

		// Ledger side: a clean order_sale entry with both a NET and a SHIP line (G=100, VAT 0, shipping
		// 10, not free) posted straight through the builder, mirroring
		// TestAccountingReportsEndToEnd's S1 construction. No customer_order row backs this UUID, so it
		// cannot accidentally agree with the independent operational fixture below.
		const ledgerKey = "ACCT-ALERT-DRIFT-LEDGER-0001"
		entry, err := acctrules.BuildOrderSaleEntry(entity.AcctOrderFacts{
			Id: 1, UUID: ledgerKey, Placed: month,
			TotalPrice:        dec("100"),
			Currency:          "EUR",
			TotalSettledBase:  decimal.NullDecimal{Decimal: dec("100"), Valid: true},
			VatAmount:         decimal.NullDecimal{Decimal: decimal.Zero, Valid: true},
			PaymentMethodId:   1,
			PaymentMethodName: entity.CARD,
			ShipmentCost:      decimal.NullDecimal{Decimal: dec("10"), Valid: true},
			FreeShipping:      sql.NullBool{Bool: false, Valid: true},
			// Phase 2: an export decision posts no VAT line (snapshot VAT is 0), reproducing NET 90 + SHIP 10.
		}, acctrules.VatDecision{Regime: entity.VatRegimeExport}, month)
		require.NoError(t, err)
		require.False(t, entry.HasCaveat, "a fully-specified sale (zero VAT, priced shipping) carries no caveat")

		require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			_, _, e := rep.Accounting().CreateJournalEntry(ctx, entry)
			return e
		}))

		// Operational side: an independent Confirmed order in the SAME month whose settled figure (250,
		// VAT 0) does NOT match the ledger entry above — e.g. a settlement correction recorded on the
		// order after the sale had already been posted. Deliberately not derived from the entry, so the
		// drift below is real, not a copy of the same number under two names.
		var confirmedID int
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))
		_, err = testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, vat_amount, placed)
			VALUES ('ACCT-ALERT-DRIFT-OP-0001', ?, 'EUR', 100, 250, 0, ?)`, confirmedID, month)
		require.NoError(t, err)

		ledger, operational, err := mtr.GetAcctRevenueReconForMonth(ctx, month)
		require.NoError(t, err)
		require.True(t, dec("100.00").Equal(ledger), "ledger = NET(90)+SHIP(10) credited by the order_sale entry, got %s", ledger)
		require.True(t, dec("250.00").Equal(operational), "operational = settled (VAT 0) on the Confirmed order, got %s", operational)
		require.False(t, ledger.Equal(operational), "the fixture deliberately drifts: ledger != operational")
	})
}
