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

// TestAnalyticsV2Task09LogisticsDemand exercises the DB side of per-country logistics and demand. Two
// DE orders (one shipped+delivered with a €10 carrier cost and an ETA, one 50%-refunded) plus one US
// order let the fulfilment durations, on-time rate, shipping cost, refund rate and the demand mix be
// asserted exactly:
//
//	DE logistics: placed→shipped 2d, placed→delivered 5d (sample 1), on-time 100%, ship €10,
//	              refund rate 25% (refunded 50 / gross 200), 1 refund order
//	DE demand:    2 orders, 2 new customers (100% new), AOV 75, one category at 100% share
//	US:           1 order, 1 new customer, no refunds, no shipping signal
//
// The GA4-session merge and conversion live in the admin package (unit-tested separately); this covers
// the SQL. Throwaway; cleans its own rows.
func TestAnalyticsV2Task09LogisticsDemand(t *testing.T) {
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

	// clean is also registered via t.Cleanup below, which runs after the test's own `defer cancel()`
	// has already cancelled ctx (defers run before Cleanups) — so it must use a fresh context, or the
	// deferred DELETEs would silently no-op and leak rows into later tests.
	clean := func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE uuid LIKE 'T09-%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE sku = 'T09-P'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM shipment_carrier WHERE carrier = 'T09-carrier'")
	}
	clean()
	t.Cleanup(clean)

	statusID := func(n entity.OrderStatusName) int {
		var id int
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(n)).Scan(&id))
		return id
	}
	confirmedID := statusID(entity.Confirmed)
	shippedStatus := statusID(entity.Shipped)
	deliveredStatus := statusID(entity.Delivered)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	styleID := seedSpineStyle(ctx, t, "T09")
	pr, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES ('T09-P', 'c', 'BLK', '#000000', 'US', ?, ?)`, mediaID, styleID)
	require.NoError(t, err)
	productID, err := pr.LastInsertId()
	require.NoError(t, err)
	// order_item.variant_id is a NOT NULL FK RESTRICT to product_size(id) as of migration 0153 — every
	// order line needs a live variant row to anchor to.
	vr, err := testDB.ExecContext(ctx, `INSERT INTO product_size (product_id, size_id, quantity, sku)
		VALUES (?, ?, 1, 'T09-P-V')`, productID, sizeID)
	require.NoError(t, err)
	variantID, err := vr.LastInsertId()
	require.NoError(t, err)

	// shipment_carrier lost its own `price` column in migration 0016 (multi-currency prices moved to
	// shipment_carrier_price); this test only needs a valid FK target for shipment.carrier_id.
	cr, err := testDB.ExecContext(ctx, `INSERT INTO shipment_carrier (carrier, tracking_url, allowed)
		VALUES ('T09-carrier', 'http://x', 1)`)
	require.NoError(t, err)
	carrierID, err := cr.LastInsertId()
	require.NoError(t, err)

	addr := func(country string) int64 {
		r, err := testDB.ExecContext(ctx,
			`INSERT INTO address (country, city, address_line_one, postal_code) VALUES (?, 'x', '1 st', '00000')`, country)
		require.NoError(t, err)
		id, err := r.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM address WHERE id = ?", id) })
		return id
	}
	deAddr, usAddr := addr("DE"), addr("US")

	placed := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	// mkOrder inserts a net-revenue order with one line item; refunded>0 marks a partial refund.
	mkOrder := func(uuid, email string, addrID int64, totalPrice, refunded int) int64 {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, refunded_amount, total_settled_base, placed)
			VALUES (?, ?, 'EUR', ?, ?, ?, ?)`, uuid, confirmedID, totalPrice, refunded, totalPrice, placed)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
			(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
			VALUES (?, 'a', 'b', ?, '1234567', ?, ?)`, oid, email, addrID, addrID)
		require.NoError(t, err)
		// variant_sku_snapshot is NOT NULL and variant_id is a NOT NULL FK RESTRICT to product_size(id)
		// as of migration 0153 (immutable variant identity of a sold line).
		_, err = testDB.ExecContext(ctx, `INSERT INTO order_item
			(order_id, product_id, variant_id, product_price, product_price_base, product_sale_percentage, quantity, size_id, variant_sku_snapshot)
			VALUES (?, ?, ?, ?, ?, 0, 1, ?, 'T09-P')`, oid, productID, variantID, totalPrice, totalPrice, sizeID)
		require.NoError(t, err)
		return oid
	}
	mkHistory := func(orderID int64, st int, at time.Time) {
		_, err := testDB.ExecContext(ctx, `INSERT INTO order_status_history
			(order_id, order_status_id, changed_at, changed_by) VALUES (?, ?, ?, 'test')`, orderID, st, at)
		require.NoError(t, err)
	}

	// DE1: shipped +2, delivered +5, ETA +6, €10 carrier cost.
	de1 := mkOrder("T09-DE1", "t09-alice@example.com", deAddr, 100, 0)
	mkHistory(de1, shippedStatus, placed.AddDate(0, 0, 2))
	mkHistory(de1, deliveredStatus, placed.AddDate(0, 0, 5))
	_, err = testDB.ExecContext(ctx, `INSERT INTO shipment (order_id, cost, carrier_id, actual_cost, estimated_arrival_date)
		VALUES (?, 0, ?, 10, ?)`, de1, carrierID, placed.AddDate(0, 0, 6))
	require.NoError(t, err)
	// DE2: 50% refund, no fulfilment history.
	_ = mkOrder("T09-DE2", "t09-bob@example.com", deAddr, 100, 50)
	// US1: plain order.
	_ = mkOrder("T09-US1", "t09-carol@example.com", usAddr, 100, 0)

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// --- Logistics ---
	logRows, err := s.Metrics().GetCountryLogistics(ctx, from, to)
	require.NoError(t, err)
	logByC := map[string]entity.CountryLogisticsRow{}
	for _, r := range logRows {
		logByC[r.Country] = r
	}
	de := logByC["DE"]
	require.Equal(t, 2.0, de.AvgDaysPlacedToShipped, "DE placed→shipped")
	require.Equal(t, 5.0, de.AvgDaysPlacedToDelivered, "DE placed→delivered")
	require.Equal(t, 1, de.DeliveredSample, "DE delivered sample")
	require.Equal(t, 100.0, de.OnTimeRatePct, "DE on-time (delivered +5 ≤ ETA +6)")
	require.Truef(t, de.AvgShippingCost.Equal(decimal.NewFromInt(10)), "DE avg shipping: got %s", de.AvgShippingCost)
	require.Equal(t, 25.0, de.RefundRatePct, "DE refund rate = 50 / (150 net + 50 refunded)")
	require.Equal(t, 1, de.RefundOrders, "DE refund orders")
	us := logByC["US"]
	require.Equal(t, 0, us.RefundOrders, "US no refunds")
	require.Truef(t, us.AvgShippingCost.IsZero(), "US no shipping signal: got %s", us.AvgShippingCost)

	// --- Demand (DB side) ---
	demand, err := s.Metrics().GetCountryDemand(ctx, from, to)
	require.NoError(t, err)
	demByC := map[string]entity.CountryDemandRow{}
	for _, r := range demand {
		demByC[r.Country] = r
	}
	deD := demByC["DE"]
	require.Equal(t, 2, deD.Orders, "DE orders")
	require.Equal(t, 2, deD.NewCustomers, "DE new customers (both first-time)")
	require.Equal(t, 0, deD.ReturningCustomers, "DE returning")
	require.Equal(t, 100.0, deD.NewSharePct, "DE new share")
	require.Truef(t, deD.AOV.Equal(decimal.NewFromInt(75)), "DE AOV = 150/2: got %s", deD.AOV)
	require.Len(t, deD.TopCategories, 1, "DE has one category")
	require.Equal(t, 1, deD.TopCategories[0].CategoryId, "DE top category id")
	require.Equal(t, 100.0, deD.TopCategories[0].SharePct, "single category = 100% share")
	usD := demByC["US"]
	require.Equal(t, 1, usD.Orders, "US orders")
	require.Equal(t, 1, usD.NewCustomers, "US new customer")
}
