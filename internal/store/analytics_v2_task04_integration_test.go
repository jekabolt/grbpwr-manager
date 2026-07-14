package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsV2Task04Delivery exercises fulfilment-speed metrics derived from order_status_history
// timestamps and the shipment ETA: placed→shipped→delivered durations, on-time rate vs ETA, and the
// delivered-coverage gate when a shipped order is not yet marked delivered.
func TestAnalyticsV2Task04Delivery(t *testing.T) {
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

	_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T04-%'")
	_, _ = testDB.ExecContext(ctx, "DELETE FROM shipment_carrier WHERE carrier = 'T04-carrier'")

	statusID := func(n entity.OrderStatusName) int {
		var id int
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(n)).Scan(&id))
		return id
	}
	shippedStatus := statusID(entity.Shipped)
	deliveredStatus := statusID(entity.Delivered)

	cr, err := testDB.ExecContext(ctx, `INSERT INTO shipment_carrier (carrier, price, tracking_url, allowed)
		VALUES ('T04-carrier', 5.00, 'http://x', 1)`)
	require.NoError(t, err)
	carrierID, err := cr.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM shipment_carrier WHERE id = ?", carrierID) })

	placed := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	d := func(n int) time.Time { return placed.AddDate(0, 0, n) }

	mkOrder := func(uuid string, curStatus int) int {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, placed)
			VALUES (?, ?, 'EUR', 100, 100, ?)`, uuid, curStatus, placed)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid) })
		return int(oid)
	}
	mkHistory := func(orderID, statusID int, at time.Time) {
		_, err := testDB.ExecContext(ctx, `INSERT INTO order_status_history
			(order_id, order_status_id, changed_at, changed_by) VALUES (?, ?, ?, 'test')`, orderID, statusID, at)
		require.NoError(t, err)
	}

	// O1 delivered: shipped +2, delivered +5, ETA +6 → on-time.
	o1 := mkOrder("T04-O1", deliveredStatus)
	mkHistory(o1, shippedStatus, d(2))
	mkHistory(o1, deliveredStatus, d(5))
	_, err = testDB.ExecContext(ctx, `INSERT INTO shipment (order_id, cost, carrier_id, estimated_arrival_date)
		VALUES (?, 5.00, ?, ?)`, o1, carrierID, d(6))
	require.NoError(t, err)

	// O2 shipped but not delivered: shipped +3 → counts toward shipped + delivered-coverage denominator.
	o2 := mkOrder("T04-O2", shippedStatus)
	mkHistory(o2, shippedStatus, d(3))

	got, err := s.Metrics().GetDeliveryMetrics(ctx, from, to)
	require.NoError(t, err)

	require.Equal(t, 2.5, got.AvgDaysPlacedToShipped)     // (2+3)/2
	require.Equal(t, 3.0, got.AvgDaysShippedToDelivered)  // O1: 5-2
	require.Equal(t, 5.0, got.AvgDaysPlacedToDelivered)   // O1
	require.Equal(t, 5.0, got.MedianDaysPlacedToDelivered)
	require.Equal(t, 2, got.ShippedSample)
	require.Equal(t, 1, got.DeliveredSample)
	require.Equal(t, 1, got.OnTimeSample)
	require.Equal(t, 100.0, got.OnTimeRatePct)           // delivered +5 <= ETA +6
	require.Equal(t, 100.0, got.EtaCoveragePct)          // the one delivered order had an ETA
	require.Equal(t, 50.0, got.DeliveredCoveragePct)     // 1 of 2 shipped delivered
	require.NotEmpty(t, got.Caveat)                       // coverage 50% < 80%
}
