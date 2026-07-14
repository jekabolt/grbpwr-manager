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

// TestAnalyticsV2Task07Profitability exercises the assembled profitability tab, focusing on the NEW
// acquisition-economics figures (CPO, blended CAC, LTV·CAC) and the operating-result roll-up. Orders
// are seeded via total_settled_base with no order_item / product cost rows, so gross margin,
// shipping and fees are all zero — which makes contribution zero and lets the operating-result and
// CAC arithmetic be asserted exactly:
//
//	orders 2, new customers 2, LTV (CLV mean) = (200+100)/2 = 150
//	spend 60 → CPO = 60/2 = 30, blended CAC = 60/2 = 30, LTV·CAC = 150/30 = 5
//	opex 90, marketing 60 → operating = contribution(0) − 90 − 60 = −150
//
// A second period with orders but NO channel_spend asserts the has_spend=false gate (CPO/CAC/ratio
// are 0 = N/A, not a misleading "free"). Throwaway; cleans its own rows.
func TestAnalyticsV2Task07Profitability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	// Defensive + cleanup: this test's rows are namespaced so a crashed run can't leak into another.
	clean := func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T07-%'")
		_, _ = testDB.ExecContext(ctx, "DELETE FROM channel_spend WHERE utm_campaign = 'T07'")
		_, _ = testDB.ExecContext(ctx, "DELETE FROM opex_line WHERE label = 'T07-rent'")
	}
	clean()
	t.Cleanup(clean)

	var confirmedID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))

	res, err := testDB.ExecContext(ctx,
		`INSERT INTO address (country, city, address_line_one, postal_code) VALUES ('US','NY','1 st','10001')`)
	require.NoError(t, err)
	addrID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM address WHERE id = ?", addrID) })

	mkOrder := func(uuid, email string, total int, placed time.Time) {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, placed)
			VALUES (?, ?, 'EUR', ?, ?, ?)`, uuid, confirmedID, total, total, placed)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
			(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
			VALUES (?, 'a', 'b', ?, '1234567', ?, ?)`, oid, email, addrID, addrID)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid) })
	}

	// --- Scenario A: May 2026, spend + opex present ---
	mkOrder("T07-A1", "t07-alice@example.com", 200, time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))
	mkOrder("T07-A2", "t07-bob@example.com", 100, time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC))
	_, err = testDB.ExecContext(ctx, `INSERT INTO channel_spend (date, utm_source, utm_medium, utm_campaign, amount, currency)
		VALUES ('2026-05-15', 'ig', 'cpc', 'T07', 60, 'EUR')`)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO opex_line (month, category, label, amount, currency, amount_base)
		VALUES ('2026-05-01', 'rent', 'T07-rent', 90, 'EUR', 90)`)
	require.NoError(t, err)

	mayFrom := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	mayTo := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	sec, err := s.Metrics().GetProfitability(ctx,
		entity.TimeRange{From: mayFrom, To: mayTo}, entity.TimeRange{})
	require.NoError(t, err)

	require.True(t, sec.HasSpend, "spend was entered for May")
	eq := func(got decimal.Decimal, want int64, msg string) {
		require.Truef(t, got.Equal(decimal.NewFromInt(want)), "%s: got %s want %d", msg, got.String(), want)
	}
	eq(sec.MarketingSpend, 60, "marketing spend")
	eq(sec.CPO.Value, 30, "CPO = 60/2 orders")
	eq(sec.BlendedCAC.Value, 30, "blended CAC = 60/2 new customers")
	eq(sec.LTV, 150, "LTV = CLV mean (200+100)/2")
	require.Equal(t, 5.0, sec.LTVCACRatio, "LTV·CAC = 150/30")
	eq(sec.OpexTotal, 90, "opex full-month proration")
	eq(sec.ContributionMargin.Value, 0, "contribution (no product cost/shipping/fees)")
	eq(sec.OperatingResult, -150, "operating = 0 − 90 opex − 60 spend")
	require.Equal(t, 2, sec.CPO.SampleSize, "CPO sample = orders")
	require.Equal(t, 2, sec.BlendedCAC.SampleSize, "CAC sample = new customers")
	require.Nil(t, sec.CPO.CompareValue, "no compare period requested")

	// --- Scenario B: April 2026, an order but NO spend → has_spend=false gate ---
	mkOrder("T07-B1", "t07-carol@example.com", 100, time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC))
	aprFrom := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	aprTo := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	secB, err := s.Metrics().GetProfitability(ctx,
		entity.TimeRange{From: aprFrom, To: aprTo}, entity.TimeRange{})
	require.NoError(t, err)

	require.False(t, secB.HasSpend, "no spend entered for April")
	require.True(t, secB.CPO.Value.IsZero(), "CPO N/A without spend")
	require.True(t, secB.BlendedCAC.Value.IsZero(), "blended CAC N/A without spend")
	require.Zero(t, secB.LTVCACRatio, "LTV·CAC N/A without spend")
	eq(secB.LTV, 100, "LTV still computed from realized revenue")
	require.Equal(t, 1, secB.CPO.SampleSize, "sample size still populated (n=1 order)")
	require.NotEmpty(t, secB.Caveat, "caveat present")
}
