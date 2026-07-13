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

// TestChannelRoasSettled exercises task-20 step-2 server-side attribution: orders are attributed to
// marketing channels via bq_order_channel (ga_client_id → last non-direct UTM), settled revenue is
// summed per channel, an unmapped client falls to '(direct)', and new-customer counts are DISTINCT
// first-time buyers (a buyer with an earlier order is NOT new). Throwaway harness; cleans its rows.
func TestChannelRoasSettled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// The attribution SQL reads the global cache (net-revenue status ids, base currency);
	// NewForTest does not populate it (only New() does), so initialize it here.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// Defensive: clear any ROAS-* rows a prior crashed run may have left (the shared test DB
	// persists across runs; a panic skips t.Cleanup).
	_, _ = testDB.ExecContext(ctx, "DELETE FROM bq_order_channel WHERE client_id LIKE 'ROAS-%'")
	_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'ROAS-%'")

	var statusID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&statusID))

	res, err := testDB.ExecContext(ctx,
		`INSERT INTO address (country, city, address_line_one, postal_code) VALUES ('US','NY','1 st','10001')`)
	require.NoError(t, err)
	addrID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM address WHERE id = ?", addrID) })

	inWindow := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	mkOrder := func(uuid, clientID, email, settled string, placed time.Time) {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, ga_client_id, placed)
			VALUES (?, ?, 'EUR', 100, ?, ?, ?)`, uuid, statusID, settled, clientID, placed)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
			(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
			VALUES (?, 'a', 'b', ?, '1234567', ?, ?)`, oid, email, addrID, addrID)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid) })
	}

	mkChannel := func(clientID, src, med, camp string) {
		_, err := testDB.ExecContext(ctx, `INSERT INTO bq_order_channel
			(client_id, date, utm_source, utm_medium, utm_campaign) VALUES (?, '2026-05-11', ?, ?, ?)`,
			clientID, src, med, camp)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM bq_order_channel WHERE client_id = ?", clientID) })
	}

	mkChannel("ROAS-C1", "ig", "social", "camp_a")
	mkChannel("ROAS-C2", "google", "cpc", "camp_b")
	// ROAS-C3 has no mapping → the join attributes it to '(direct)'.

	// A prior (pre-window) order makes new1 a RETURNING customer for the in-window order.
	mkOrder("ROAS-O0", "ROAS-C1", "roas-new1@example.com", "10", inWindow.AddDate(0, -1, 0))
	mkOrder("ROAS-O1", "ROAS-C1", "roas-new1@example.com", "200", inWindow) // returning
	mkOrder("ROAS-O2", "ROAS-C1", "roas-new2@example.com", "100", inWindow) // new
	mkOrder("ROAS-O3", "ROAS-C2", "roas-new3@example.com", "300", inWindow) // new
	mkOrder("ROAS-O4", "ROAS-C3", "roas-new4@example.com", "50", inWindow)  // unmapped → direct, new

	rows, err := s.Metrics().GetChannelRoasSettled(ctx, from, to)
	require.NoError(t, err)
	byCh := map[string]entity.ChannelSettledRow{}
	for _, r := range rows {
		byCh[r.UTMSource+"/"+r.UTMMedium+"/"+r.UTMCampaign] = r
	}

	ig := byCh["ig/social/camp_a"]
	require.True(t, ig.SettledRevenue.Equal(decimal.NewFromInt(300)), "ig settled 200+100, got %s", ig.SettledRevenue)
	require.EqualValues(t, 2, ig.Orders, "two in-window orders on ig")
	require.EqualValues(t, 1, ig.NewCustomers, "only new2 is first-time; new1 had a prior order")

	g := byCh["google/cpc/camp_b"]
	require.True(t, g.SettledRevenue.Equal(decimal.NewFromInt(300)), "google settled 300, got %s", g.SettledRevenue)
	require.EqualValues(t, 1, g.Orders)
	require.EqualValues(t, 1, g.NewCustomers)

	d := byCh["(direct)/(none)/(not set)"]
	require.True(t, d.SettledRevenue.Equal(decimal.NewFromInt(50)), "unmapped client attributes to direct, got %s", d.SettledRevenue)
	require.EqualValues(t, 1, d.Orders)
	require.EqualValues(t, 1, d.NewCustomers)
}
