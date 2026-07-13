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

// TestRefundLineLevelApportionment exercises the task-12 change: when an order carries
// line-level refund detail (refunded_order_item rows), revenue/COGS apportionment shrinks
// ONLY the refunded line. A two-item order (coat + t-shirt, €100 list / €40 cost each)
// where the coat is fully returned must report the coat at zero revenue/cost and the kept
// t-shirt at its FULL €100 revenue / €40 cost — not the old order-level ratio
// (total_price-refunded)/total = 0.5 that bled the refund across both lines and would have
// left each at half. Throwaway harness; cleans up in reverse-dependency order.
func TestRefundLineLevelApportionment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// The apportion SQL reads the global cache (net-revenue status ids, base currency).
	// NewForTest does not populate it (only New() does), so initialize it here.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	var mediaID, coatID, tshirtID, sizeID, statusID int
	var orderID int64
	defer func() {
		if orderID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM refunded_order_item WHERE order_id = ?", orderID)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE order_id = ?", orderID)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", orderID)
		}
		for _, pid := range []int{coatID, tshirtID} {
			if pid != 0 {
				_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", pid)
			}
		}
		if mediaID != 0 {
			_ = s.Media().DeleteMediaById(ctx, mediaID)
		}
	}()

	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.PartiallyRefunded)).Scan(&statusID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	// media (product thumbnail FK target)
	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	mkProduct := func(sku string) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
			VALUES (?, 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, sku, mediaID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}
	coatID = mkProduct("ECO-W4-COAT")
	tshirtID = mkProduct("ECO-W4-TSHIRT")

	// A partially_refunded order (a net-revenue status) placed inside the window.
	// total_settled_base == the reconstructed base (200) so the FX factor is exactly 1;
	// VAT 0; no promo, no shipment. refunded_amount 100 mirrors the returned coat but is
	// only consulted by the ORDER-level fallback — the line detail below overrides it.
	placed := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	res, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, refunded_amount, total_settled_base, placed)
		VALUES ('ECO-W4-REFUND-0001', ?, 'EUR', 200, 100, 200, ?)`, statusID, placed)
	require.NoError(t, err)
	orderID, err = res.LastInsertId()
	require.NoError(t, err)

	// Two lines, €100 base list / €40 base cost each, both snapshots set directly so no
	// product_price / product.cost_price lookup is needed.
	mkItem := func(prodID int) int64 {
		res, err := testDB.ExecContext(ctx, `INSERT INTO order_item
			(order_id, product_id, product_price, product_price_base, cost_price_at_sale, product_sale_percentage, quantity, size_id)
			VALUES (?, ?, 100, 100, 40, 0, 1, ?)`, orderID, prodID, sizeID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}
	coatItemID := mkItem(coatID)
	_ = mkItem(tshirtID)

	// The coat is fully returned (1 of 1 unit); the t-shirt is kept.
	_, err = testDB.ExecContext(ctx, `INSERT INTO refunded_order_item
		(order_id, order_item_id, quantity_refunded) VALUES (?, ?, 1)`, orderID, coatItemID)
	require.NoError(t, err)

	rows, err := s.Metrics().GetSlowMovers(ctx, placed.Add(-24*time.Hour), placed.Add(24*time.Hour), 10000)
	require.NoError(t, err)

	byID := map[int]entity.SlowMoverRow{}
	for _, r := range rows {
		byID[r.ProductID] = r
	}
	coat, ok := byID[coatID]
	require.True(t, ok, "coat present in slow movers")
	tshirt, ok := byID[tshirtID]
	require.True(t, ok, "t-shirt present in slow movers")

	// Coat fully refunded → its line contributes zero net revenue and zero net cost.
	require.True(t, coat.Revenue.IsZero(), "refunded coat revenue should be 0, got %s", coat.Revenue)
	require.True(t, coat.RevenueCost.IsZero(), "refunded coat cost should be 0, got %s", coat.RevenueCost)

	// T-shirt kept → full €100 revenue and €40 cost, NOT halved by order-level proration.
	require.True(t, tshirt.Revenue.Equal(decimal.NewFromInt(100)),
		"kept t-shirt revenue should be 100, got %s", tshirt.Revenue)
	require.True(t, tshirt.RevenueCost.Equal(decimal.NewFromInt(40)),
		"kept t-shirt cost should be 40, got %s", tshirt.RevenueCost)

	// Smoke the other apportion SQL shape — GetProductTrend builds TWO order_factors CTEs
	// (current + previous) and references itemAdj on both aliases, so the per-line refund
	// subquery / has_line_refunds column must resolve there too. Just assert it executes.
	_, err = s.Metrics().GetProductTrend(ctx, placed.Add(-24*time.Hour), placed.Add(24*time.Hour), 100)
	require.NoError(t, err, "product trend (dual order_factors CTE) must run with per-line refund ratio")
}
