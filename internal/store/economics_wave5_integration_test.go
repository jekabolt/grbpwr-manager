package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestSettledBaseLoyaltyReconcile exercises the task-18 change: at payment capture
// UpdateSettledBaseAndFee collapses the order-time loyalty snapshot (total_price_eur) onto the
// settled fact (total_settled_base) when the two EUR figures agree within tolerance, but leaves
// the snapshot untouched when they diverge beyond it (so qualifying spend — and thus loyalty
// tiers — never silently move as a side effect of capture). A NULL snapshot stays NULL. In all
// cases total_settled_base and payment_fee are written. Throwaway harness; cleans up its rows.
func TestSettledBaseLoyaltyReconcile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var statusID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM order_status").Scan(&statusID))

	placed := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	// mkOrder inserts a customer_order with the given uuid and (optional) loyalty snapshot,
	// registers cleanup, and returns the uuid. priceEUR.Valid=false inserts a NULL snapshot.
	mkOrder := func(uuid string, priceEUR decimal.NullDecimal) string {
		var eur any
		if priceEUR.Valid {
			eur = priceEUR.Decimal
		}
		_, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_price_eur, placed)
			VALUES (?, ?, 'EUR', 100, ?, ?)`, uuid, statusID, eur, placed)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid = ?", uuid) })
		return uuid
	}

	readEUR := func(uuid string) decimal.NullDecimal {
		var got decimal.NullDecimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT total_price_eur FROM customer_order WHERE uuid = ?", uuid).Scan(&got))
		return got
	}
	readSettled := func(uuid string) (decimal.NullDecimal, decimal.Decimal) {
		var settled decimal.NullDecimal
		var fee decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT total_settled_base, payment_fee FROM customer_order WHERE uuid = ?", uuid).Scan(&settled, &fee))
		return settled, fee
	}

	dec := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}

	// Case 1 — within tolerance (snapshot 101.00, settled 100.00 → 1% gap ≤ 2%): the snapshot
	// is pulled onto the settled fact.
	within := mkOrder("ECO-W5-EUR-WITHIN", dec("101.00"))
	require.NoError(t, s.Order().UpdateSettledBaseAndFee(ctx, within,
		decimal.RequireFromString("100.00"), decimal.RequireFromString("3.20")))
	gotWithin := readEUR(within)
	require.True(t, gotWithin.Valid)
	require.True(t, gotWithin.Decimal.Equal(decimal.RequireFromString("100.00")),
		"within-tolerance snapshot should collapse onto settled, got %s", gotWithin.Decimal)

	// Case 2 — beyond tolerance (snapshot 130.00, settled 100.00 → 30% gap > 2%): the snapshot
	// is left at its order-time value (and an anomaly is logged).
	beyond := mkOrder("ECO-W5-EUR-BEYOND", dec("130.00"))
	require.NoError(t, s.Order().UpdateSettledBaseAndFee(ctx, beyond,
		decimal.RequireFromString("100.00"), decimal.RequireFromString("3.20")))
	gotBeyond := readEUR(beyond)
	require.True(t, gotBeyond.Valid)
	require.True(t, gotBeyond.Decimal.Equal(decimal.RequireFromString("130.00")),
		"beyond-tolerance snapshot must be preserved, got %s", gotBeyond.Decimal)

	// Case 3 — NULL snapshot: nothing to reconcile, stays NULL.
	null := mkOrder("ECO-W5-EUR-NULL", decimal.NullDecimal{})
	require.NoError(t, s.Order().UpdateSettledBaseAndFee(ctx, null,
		decimal.RequireFromString("100.00"), decimal.RequireFromString("3.20")))
	require.False(t, readEUR(null).Valid, "NULL snapshot must stay NULL")

	// In every case the settled base and fee are recorded regardless of the reconcile decision.
	for _, uuid := range []string{within, beyond, null} {
		settled, fee := readSettled(uuid)
		require.True(t, settled.Valid && settled.Decimal.Equal(decimal.RequireFromString("100.00")),
			"settled base must be written for %s", uuid)
		require.True(t, fee.Equal(decimal.RequireFromString("3.20")),
			"payment fee must be written for %s", uuid)
	}
}

// TestGA4RevenueCoverageOnDashboard exercises the task-20 GA4-vs-DB reconciliation: GetDashboard
// sums GA4-reported revenue from ga4_ecommerce_metrics over the period (inclusive DATE bounds,
// excluding days on either side) and surfaces it as Dashboard.GA4Revenue. With no orders in the
// window the DB gross revenue is 0, so coverage stays 0 — the coverage % / alert arithmetic is
// covered by the metrics-package unit tests; this pins the SQL bounds and entity wiring.
func TestGA4RevenueCoverageOnDashboard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// A distinctive future window; delete any pre-existing rows on these dates (UNIQUE(date))
	// so the test is isolated on the shared DB, and clean them up afterwards.
	dates := []string{"2027-02-28", "2027-03-01", "2027-03-02", "2027-03-05"}
	for _, d := range dates {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM ga4_ecommerce_metrics WHERE date = ?", d)
	}
	defer func() {
		for _, d := range dates {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM ga4_ecommerce_metrics WHERE date = ?", d)
		}
	}()
	// In-window (2027-03-01..02): 300 + 200 = 500. Out-of-window on both sides must be excluded.
	seed := []struct {
		date string
		rev  string
	}{
		{"2027-02-28", "888.00"}, // below DATE(from) → excluded
		{"2027-03-01", "300.00"},
		{"2027-03-02", "200.00"},
		{"2027-03-05", "999.00"}, // above DATE(to) → excluded
	}
	for _, r := range seed {
		_, err := testDB.ExecContext(ctx,
			"INSERT INTO ga4_ecommerce_metrics (date, revenue) VALUES (?, ?)", r.date, r.rev)
		require.NoError(t, err)
	}

	from := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 3, 3, 0, 0, 0, 0, time.UTC) // DATE(to) = 2027-03-03, so 03-02 is included, 03-05 is not

	d, err := s.Metrics().GetDashboard(ctx, from, to, 10)
	require.NoError(t, err)
	require.True(t, d.GA4Revenue.Equal(decimal.RequireFromString("500.00")),
		"GA4 revenue should sum only the in-window days, got %s", d.GA4Revenue)
	require.Zero(t, d.TrackingCoveragePct, "no orders in window → DB gross revenue 0 → coverage 0")
}

// TestAlertThresholdsGA4Key confirms the new GA4 coverage threshold (task 20) persists through
// the alert_setting key-value table alongside the existing thresholds.
func TestAlertThresholdsGA4Key(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	want := entity.AlertThresholds{
		CoverageWarnPct:      65,
		RefundRateWarnPct:    12,
		RateFloorN:           25,
		ContributionTrustPct: 55,
		GA4CoverageWarnPct:   75,
	}
	require.NoError(t, s.Metrics().UpsertAlertThresholds(ctx, want))
	defer func() {
		// restore built-in defaults so a shared-DB neighbour test isn't perturbed.
		_ = s.Metrics().UpsertAlertThresholds(ctx, entity.DefaultAlertThresholds())
	}()

	got, err := s.Metrics().GetAlertThresholds(ctx)
	require.NoError(t, err)
	require.Equal(t, want.GA4CoverageWarnPct, got.GA4CoverageWarnPct, "GA4 coverage threshold persisted")
	require.Equal(t, want.ContributionTrustPct, got.ContributionTrustPct, "existing thresholds still persisted")
}

// TestOpexOperatingResult exercises task 22: UpsertOpexEntries persists fixed costs, and
// GetDashboard subtracts the day-pro-rated OPEX (and marketing spend) from the contribution
// margin to form the operating result, flagging a missing-OPEX period with a caveat. Uses a
// distinctive future window with no orders (contribution 0) so the result is exactly
// −(opex + marketing) and the pro-rata is isolated.
func TestOpexOperatingResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// April 2027: a distinctive, order-free window. Clean the opex rows we own, then upsert.
	aprStart := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	mayStart := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _ = testDB.ExecContext(ctx, "DELETE FROM opex_entry WHERE month = ?", aprStart.Format("2006-01-02"))
	defer func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM opex_entry WHERE month = ?", aprStart.Format("2006-01-02"))
	}()

	require.NoError(t, s.Metrics().UpsertOpexEntries(ctx, []entity.OpexEntry{
		{Month: aprStart, Category: "salaries", Amount: decimal.RequireFromString("1000.00")},
		{Month: aprStart, Category: "rent", Amount: decimal.RequireFromString("500.00")},
	}))
	// Upsert on (month, category): re-writing salaries changes the amount, not the row count.
	require.NoError(t, s.Metrics().UpsertOpexEntries(ctx, []entity.OpexEntry{
		{Month: aprStart, Category: "salaries", Amount: decimal.RequireFromString("1200.00")},
	}))
	// April total = 1200 + 500 = 1700.

	// Full April → OPEX fully attributed; no orders → contribution 0; operating result = −1700 − marketing.
	full, err := s.Metrics().GetDashboard(ctx, aprStart, mayStart, 10)
	require.NoError(t, err)
	require.True(t, full.OpexTotal.Equal(decimal.RequireFromString("1700.00")),
		"full-month OPEX should be the sum of categories, got %s", full.OpexTotal)
	require.Empty(t, full.OpexCaveat, "OPEX present → no caveat")
	wantFull := full.ContributionMargin.Sub(full.OpexTotal).Sub(full.MarketingSpend).Round(2)
	require.True(t, full.OperatingResult.Equal(wantFull),
		"operating result must be contribution − opex − marketing (%s), got %s", wantFull, full.OperatingResult)

	// First half of April (15 of 30 days) → OPEX pro-rated to ~half.
	aprMid := time.Date(2027, 4, 16, 0, 0, 0, 0, time.UTC)
	half, err := s.Metrics().GetDashboard(ctx, aprStart, aprMid, 10)
	require.NoError(t, err)
	require.True(t, half.OpexTotal.Equal(decimal.RequireFromString("850.00")),
		"half-month OPEX should be pro-rated to 15/30 of 1700 = 850, got %s", half.OpexTotal)

	// A month with no OPEX recorded → caveat set, zero OPEX.
	julStart := time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)
	augStart := time.Date(2027, 8, 1, 0, 0, 0, 0, time.UTC)
	_, _ = testDB.ExecContext(ctx, "DELETE FROM opex_entry WHERE month = ?", julStart.Format("2006-01-02"))
	none, err := s.Metrics().GetDashboard(ctx, julStart, augStart, 10)
	require.NoError(t, err)
	require.True(t, none.OpexTotal.IsZero(), "no OPEX rows → zero total")
	require.NotEmpty(t, none.OpexCaveat, "missing OPEX → caveat set")
}
