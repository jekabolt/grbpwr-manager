package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsV2Task01KPICoreGaps exercises the analytics-v2 task-01 additions to the commerce
// overview: unique_customers (distinct buyer emails over net-revenue orders — a repeated email is
// ONE customer), peak_day (the highest-net-revenue calendar day, with its revenue and order count),
// and that a fully-refunded order is excluded from both. Revenue is seeded via total_settled_base so
// it is deterministic without product/price rows, mirroring the channel-ROAS harness. Throwaway;
// cleans its own rows.
func TestAnalyticsV2Task01KPICoreGaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// GetBusinessMetrics reads the global cache (net-revenue status ids, base currency); NewForTest
	// does not populate it (only New() does), so initialize it here.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// Defensive: clear rows a prior crashed run may have left (shared test DB persists across runs).
	_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T01-%'")

	var confirmedID, refundedID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Refunded)).Scan(&refundedID))

	res, err := testDB.ExecContext(ctx,
		`INSERT INTO address (country, city, address_line_one, postal_code) VALUES ('US','NY','1 st','10001')`)
	require.NoError(t, err)
	addrID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM address WHERE id = ?", addrID) })

	day1 := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	mkOrder := func(uuid, email, settled string, statusID int, placed time.Time) {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, placed)
			VALUES (?, ?, 'EUR', 100, ?, ?)`, uuid, statusID, settled, placed)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
			(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
			VALUES (?, 'a', 'b', ?, '1234567', ?, ?)`, oid, email, addrID, addrID)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid) })
	}

	// Day 1: alice 100 + bob 50 → day revenue 150, 2 orders.
	mkOrder("T01-O1", "t01-alice@example.com", "100", confirmedID, day1)
	mkOrder("T01-O2", "t01-bob@example.com", "50", confirmedID, day1)
	// Day 2: alice again 300 → day revenue 300, 1 order. alice repeats → still ONE unique customer.
	mkOrder("T01-O3", "t01-alice@example.com", "300", confirmedID, day2)
	// Fully-refunded order (status Refunded ∉ net-revenue) must NOT count toward unique_customers,
	// order count, or peak day — even though its settled figure (999) is the largest.
	mkOrder("T01-O4", "t01-carol@example.com", "999", refundedID, day2)

	m, err := s.Metrics().GetBusinessMetrics(ctx,
		entity.TimeRange{From: from, To: to}, entity.TimeRange{}, entity.MetricsGranularityDay)
	require.NoError(t, err)

	// unique_customers = distinct emails over net-revenue orders = {alice, bob} = 2 (carol refunded).
	require.Equal(t, "2", m.UniqueCustomers.Value.String(), "unique customers (alice counted once, carol excluded)")
	// orders_count = 3 net-revenue orders (O1,O2,O3; O4 refunded excluded).
	require.Equal(t, "3", m.OrdersCount.Value.String())

	// peak_day = day 2 (300 > 150), 1 order, revenue 300 (settled, no VAT/refund).
	require.NotNil(t, m.PeakDay, "peak day should be set when the period has revenue")
	require.Equal(t, "300", m.PeakDay.Revenue.String())
	require.Equal(t, 1, m.PeakDay.Orders)
	require.Equal(t, "2026-05-12", m.PeakDay.Date.Format("2006-01-02"))
}
