package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsV2Task02NewVsReturning exercises the new-vs-returning revenue split: a buyer whose
// first-ever order predates the window is "returning" in-window; a buyer whose first order is
// in-window is "new" (revenue and AOV split accordingly), and new+returning revenue reconciles with
// headline revenue. Seeded via total_settled_base for deterministic revenue. Throwaway harness.
func TestAnalyticsV2Task02NewVsReturning(t *testing.T) {
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

	_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T02-%'")

	var confirmedID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))

	res, err := testDB.ExecContext(ctx,
		`INSERT INTO address (country, city, address_line_one, postal_code) VALUES ('US','NY','1 st','10001')`)
	require.NoError(t, err)
	addrID, err := res.LastInsertId()
	require.NoError(t, err)
	// Fresh context: the test's ctx is already cancelled by its `defer cancel()` (defers run before
	// Cleanups), which would make this DELETE a no-op and leak the row into later tests sharing this
	// date window.
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM address WHERE id = ?", addrID) })

	preWindow := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	inWindow := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	mkOrder := func(uuid, email, settled string, placed time.Time) {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, placed)
			VALUES (?, ?, 'EUR', 100, ?, ?)`, uuid, confirmedID, settled, placed)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
			(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
			VALUES (?, 'a', 'b', ?, '1234567', ?, ?)`, oid, email, addrID, addrID)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM customer_order WHERE id = ?", oid) })
	}

	// alice's FIRST order predates the window → she is a returning customer in-window.
	mkOrder("T02-A0", "t02-alice@example.com", "100", preWindow)
	// In-window: alice returning (200), bob & carol new (300 + 100).
	mkOrder("T02-A1", "t02-alice@example.com", "200", inWindow)
	mkOrder("T02-B1", "t02-bob@example.com", "300", inWindow)
	mkOrder("T02-C1", "t02-carol@example.com", "100", inWindow)

	m, err := s.Metrics().GetBusinessMetrics(ctx,
		entity.TimeRange{From: from, To: to}, entity.TimeRange{}, entity.MetricsGranularityDay)
	require.NoError(t, err)
	require.NotNil(t, m.NewVsReturning)
	nvr := m.NewVsReturning

	// New = bob + carol: 2 orders, 400 revenue, AOV 200.
	require.Equal(t, "2", nvr.NewOrders.Value.String())
	require.Equal(t, "400", nvr.NewRevenue.Value.String())
	require.Equal(t, "200", nvr.NewAOV.Value.String())
	// Returning = alice's in-window order: 1 order, 200 revenue, AOV 200.
	require.Equal(t, "1", nvr.ReturningOrders.Value.String())
	require.Equal(t, "200", nvr.ReturningRevenue.Value.String())
	require.Equal(t, "200", nvr.ReturningAOV.Value.String())
	// Share: new 400 / total 600 = 66.67%.
	require.InDelta(t, 66.67, nvr.NewRevenueSharePct, 0.01)

	// Reconciliation: new + returning revenue == headline net revenue (600).
	require.Equal(t, "600", m.Revenue.Value.String())
	require.Equal(t, m.Revenue.Value.String(),
		nvr.NewRevenue.Value.Add(nvr.ReturningRevenue.Value).String())
}
