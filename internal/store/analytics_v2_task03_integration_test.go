package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsV2Task03OrderValueBands exercises the fixed order-value histogram: net-revenue orders
// are bucketed into stable EUR bands with per-band order/revenue shares and in-band AOV, empty bands
// are still returned (stable axis), and a refunded order is excluded. Seeded via total_settled_base.
func TestAnalyticsV2Task03OrderValueBands(t *testing.T) {
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

	_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T03-%'")

	var confirmedID, refundedID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Refunded)).Scan(&refundedID))

	inWindow := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	mkOrder := func(uuid, settled string, statusID int) {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, placed)
			VALUES (?, ?, 'EUR', 100, ?, ?)`, uuid, statusID, settled, inWindow)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid) })
	}

	// Band €0–50: 30 + 40 (2 orders, 70). Band €50–100: 75. Band €500+: 600.
	mkOrder("T03-O1", "30", confirmedID)
	mkOrder("T03-O2", "40", confirmedID)
	mkOrder("T03-O3", "75", confirmedID)
	mkOrder("T03-O4", "600", confirmedID)
	// Refunded order (∉ net-revenue) must be excluded from the histogram.
	mkOrder("T03-O5", "250", refundedID)

	rows, err := s.Metrics().GetOrderValueBands(ctx, from, to)
	require.NoError(t, err)
	require.Len(t, rows, 7, "all fixed bands returned even when empty")

	// Band 0 (€0–50): 2 orders, revenue 70, AOV 35, 50% of 4 orders.
	require.Equal(t, "€0–50", rows[0].Label)
	require.Equal(t, 2, rows[0].Orders)
	require.Equal(t, "70", rows[0].Revenue.String())
	require.Equal(t, "35", rows[0].AvgOrderValue.String())
	require.InDelta(t, 50.0, rows[0].OrdersSharePct, 0.01)

	// Band 1 (€50–100): 1 order, 75.
	require.Equal(t, 1, rows[1].Orders)
	require.Equal(t, "75", rows[1].Revenue.String())

	// Band 6 (€500+): 1 order, 600 → 80.54% of total revenue 745.
	require.Equal(t, "€500+", rows[6].Label)
	require.Equal(t, 1, rows[6].Orders)
	require.Equal(t, "600", rows[6].Revenue.String())
	require.InDelta(t, 80.54, rows[6].RevenueSharePct, 0.05)

	// Bands 2–5 empty (stable axis), refunded 250-order excluded.
	for _, i := range []int{2, 3, 4, 5} {
		require.Equal(t, 0, rows[i].Orders, "band %d should be empty", i)
	}

	// Order shares sum to 100 across the 4 net-revenue orders.
	var orderShare float64
	for _, r := range rows {
		orderShare += r.OrdersSharePct
	}
	require.InDelta(t, 100.0, orderShare, 0.01)
}
