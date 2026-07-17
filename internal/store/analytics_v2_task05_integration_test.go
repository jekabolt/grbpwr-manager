package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsV2Task05PromoSnapshot verifies the promo snapshot makes historical promo reporting
// immutable: an order carries promo_code_snapshot / promo_discount_pct captured at apply time, and
// editing the promo_code row afterwards does NOT change the order's reported code or discount (the
// snapshot wins over the live join). Seeded via total_settled_base so no product rows are needed.
func TestAnalyticsV2Task05PromoSnapshot(t *testing.T) {
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

	_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T05-%'")
	_, _ = testDB.ExecContext(ctx, "DELETE FROM promo_code WHERE code IN ('T05-SAVE20','T05-CHANGED')")

	var confirmedID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))

	pr, err := testDB.ExecContext(ctx, `INSERT INTO promo_code
		(code, free_shipping, discount, expiration, start, voucher, allowed)
		VALUES ('T05-SAVE20', 0, 20.00, DATE_ADD(NOW(), INTERVAL 1 YEAR), NOW(), 0, 1)`)
	require.NoError(t, err)
	promoID, err := pr.LastInsertId()
	require.NoError(t, err)
	// Fresh context: the test's ctx is already cancelled by its `defer cancel()` (defers run before
	// Cleanups), which would make these DELETEs no-ops and leak rows into later tests sharing this
	// date window.
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM promo_code WHERE id = ?", promoID) })

	res, err := testDB.ExecContext(ctx,
		`INSERT INTO address (country, city, address_line_one, postal_code) VALUES ('US','NY','1 st','10001')`)
	require.NoError(t, err)
	addrID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM address WHERE id = ?", addrID) })

	inWindow := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	// Order with the promo applied: snapshot columns captured at apply time (as updateOrderTotalPromo
	// writes them). Revenue via settled base.
	r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, total_settled_base, promo_id,
		 promo_discount_pct, promo_free_shipping, promo_code_snapshot, placed)
		VALUES ('T05-O1', ?, 'EUR', 80, 80, ?, 20.00, 0, 'T05-SAVE20', ?)`, confirmedID, promoID, inWindow)
	require.NoError(t, err)
	oid, err := r.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM customer_order WHERE id = ?", oid) })
	_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
		(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
		VALUES (?, 'a', 'b', 't05@example.com', '1234567', ?, ?)`, oid, addrID, addrID)
	require.NoError(t, err)

	promoRow := func() *entity.PromoMetric {
		m, err := s.Metrics().GetBusinessMetrics(ctx,
			entity.TimeRange{From: from, To: to}, entity.TimeRange{}, entity.MetricsGranularityDay)
		require.NoError(t, err)
		for i := range m.RevenueByPromo {
			if m.RevenueByPromo[i].PromoCode == "T05-SAVE20" {
				return &m.RevenueByPromo[i]
			}
		}
		return nil
	}

	// Before edit: the order is attributed to SAVE20 at 20% discount.
	before := promoRow()
	require.NotNil(t, before, "order should appear under its promo snapshot code")
	require.Equal(t, "20", before.AvgDiscount.Round(0).String())

	// Edit the promo_code row: change both its discount and its code. The snapshot must shield the
	// order's history — the report still shows SAVE20 @ 20%, not CHANGED @ 50%.
	_, err = testDB.ExecContext(ctx,
		"UPDATE promo_code SET discount = 50.00, code = 'T05-CHANGED' WHERE id = ?", promoID)
	require.NoError(t, err)

	after := promoRow()
	require.NotNil(t, after, "snapshot keeps the order under the original code after the promo is edited")
	require.Equal(t, "20", after.AvgDiscount.Round(0).String(), "discount stays snapshotted at 20, not 50")
	require.Nil(t, promoRowByCode(ctx, t, s, from, to, "T05-CHANGED"), "edited code must not appear for a historical order")
}

func promoRowByCode(ctx context.Context, t *testing.T, s *MYSQLStore, from, to time.Time, code string) *entity.PromoMetric {
	t.Helper()
	m, err := s.Metrics().GetBusinessMetrics(ctx,
		entity.TimeRange{From: from, To: to}, entity.TimeRange{}, entity.MetricsGranularityDay)
	require.NoError(t, err)
	for i := range m.RevenueByPromo {
		if m.RevenueByPromo[i].PromoCode == code {
			return &m.RevenueByPromo[i]
		}
	}
	return nil
}
