package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsV2Task06RevenueForecast exercises the month revenue projection. It seeds a flat
// 10/day of net revenue across the whole trailing 8-week window (so the day-of-week profile is
// perfectly flat and the daily stdev is zero), then forecasts as of the 10th of a 31-day month.
// With a flat history the DOW projection collapses to the naive run-rate and the ±band is
// zero-width, which makes every figure exactly assertable:
//
//	month-to-date = 10 days × 10           = 100
//	forecast      = 100 + 21 remaining ×10 = 310  (== run-rate 100/10 × 31)
//	low == high   = 310                          (flat history → zero variance)
//
// No prior-year rows are seeded, so the seasonal blend is skipped and the method stays "dow".
// Revenue is seeded via total_settled_base (total_price=100, no VAT/refund → revenue == settled),
// mirroring the other analytics-v2 harnesses. Throwaway; cleans its own rows.
func TestAnalyticsV2Task06RevenueForecast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// getRevenueByPeriod reads the global cache (net-revenue status ids, base currency); NewForTest
	// does not populate it (only New() does), so initialize it here.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// Defensive: clear rows a prior crashed run may have left (shared test DB persists across runs).
	_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T06-%'")
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T06-%'") })

	var confirmedID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))

	// asOf = 10th of May 2026 (31-day month) → elapsed 10, remaining 21. Seed a flat 10/day of net
	// revenue from well before the 56-day trailing window (asOf-56 ≈ Mar 15) through asOf, so both the
	// month-to-date span (May 1..10) and the trailing DOW window are fully and uniformly covered.
	asOf := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	seedStart := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	i := 0
	for d := seedStart; !d.After(asOf); d = d.AddDate(0, 0, 1) {
		_, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, placed)
			VALUES (?, ?, 'EUR', 100, 10, ?)`, fmt.Sprintf("T06-%03d", i), confirmedID, d)
		require.NoError(t, err)
		i++
	}

	got, err := s.Metrics().GetRevenueForecast(ctx, asOf)
	require.NoError(t, err)

	require.Equal(t, "dow", got.Method)
	require.Equal(t, 10, got.ElapsedDays)
	require.Equal(t, 21, got.RemainingDays)
	require.Equal(t, "100", got.MtdActual.String())
	require.Equal(t, "310", got.Forecast.String())
	require.Equal(t, "310", got.RunRate.String())
	require.Equal(t, "310", got.ForecastLow.String())  // flat history → zero-width band
	require.Equal(t, "310", got.ForecastHigh.String()) // flat history → zero-width band
	require.Equal(t, "0", got.LastYearMonthTotal.String())
	require.Equal(t, "2026-05-01", got.Month.Format("2006-01-02"))
}
