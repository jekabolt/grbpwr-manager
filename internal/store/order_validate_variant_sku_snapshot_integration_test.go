package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestValidateOrderItemsInsertSnapshotsVariantSKU is the acceptance test for the fix to
// validateOrderItemsStockAvailabilityWithLock (the storefront ValidateOrderItemsInsert RPC's backing
// function): it used to snapshot the colourway's BASE sku (product.sku) onto entity.OrderItem.SKU
// (db:"variant_sku_snapshot"), which per dto.ConvertEntityOrderItemToPb's documented invariant must
// hold the VARIANT sku (product_size.sku) instead. The actual persisted order was always correct —
// insertOrderItems does its own independent fetchVariantSnapshots query at insert time, so it never
// reused this wrong value — only the live pre-order preview response was wrong. Seeded via raw SQL
// (mirrors the fixture pattern in the neighboring order_sku_snapshot_integration_test.go), deliberately
// using two DIFFERENT literal SKUs for base vs. variant so the test would silently pass-for-the-wrong-
// reason if they happened to match.
func TestValidateOrderItemsInsertSnapshotsVariantSKU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	exec := func(q string, args ...any) int64 {
		res, err := testDB.ExecContext(ctx, q, args...)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
		// BlurHash must be set (not left NULL) or the later GetProductsByIds call (inside
		// ValidateOrderItemsInsert) NULL-scan-fails on blur_hash — a pre-existing quirk of hand-built
		// media fixtures, not something to fix here.
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)

	styleID := exec(`INSERT INTO tech_card (style_number, name, brand, collection, target_gender, top_category_id)
		VALUES (CONCAT('AUTO-', UUID_SHORT()), 'VSKU', 'ACME', '', 'unisex', 1)`)
	prodID := exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
		VALUES ('BASE-SKU-HERE', 'c', 'BLK', '#000000', 'US', ?, ?, 2)`, mediaID, styleID) // lifecycle_status 2 = ACTIVE, required by GetProductsByIds' WHERE p.lifecycle_status = 2
	exec(`INSERT INTO product_price (product_id, currency, price) VALUES (?, 'EUR', 100.00)`, prodID)
	sizeID := exec(`INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('VS-', LEFT(MD5(RAND()),4)), 42, 'apparel')`)
	psID := exec(`INSERT INTO product_size (product_id, size_id, quantity, sku) VALUES (?, ?, 5, 'VARIANT-SKU-HERE')`, prodID, sizeID)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_size WHERE id = ?", psID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_price WHERE product_id = ?", prodID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM size WHERE id = ?", sizeID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id = ?", prodID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	oiv, err := s.Order().ValidateOrderItemsInsert(ctx, []entity.OrderItemInsert{
		{VariantSKU: "VARIANT-SKU-HERE", Quantity: decimal.NewFromInt(1)},
	}, "EUR")
	require.NoError(t, err)
	require.Len(t, oiv.ValidItems, 1)
	require.Equal(t, "VARIANT-SKU-HERE", oiv.ValidItems[0].SKU,
		"ValidateOrderItemsInsert must snapshot the variant SKU onto OrderItem.SKU, not the colourway's base SKU")
}
