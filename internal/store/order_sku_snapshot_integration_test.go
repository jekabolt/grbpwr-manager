package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestOrderItemSKUSnapshotImmutable is the acceptance test for problem 023: order_item.product_sku is a
// mandatory, immutable snapshot. After a sale, a catalogue remint (variant or base SKU) must not change
// what the order response or the label/customs read back (no live/base fallback), and the column is
// NOT NULL so a line can never be persisted without an identity. Seeded via SQL (AddProduct is
// unrelated-broken at this base by the 0146 season CHECK).
func TestOrderItemSKUSnapshotImmutable(t *testing.T) {
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
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)
	styleID := exec(`INSERT INTO tech_card (style_number, name, brand, collection, target_gender, top_category_id)
		VALUES (CONCAT('AUTO-', UUID_SHORT()), 'S23', 'ACME', '', 'unisex', 1)`)
	prodID := exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES ('BASE-ORIG', 'c', 'BLK', '#000000', 'US', ?, ?)`, mediaID, styleID)
	sizeID := exec(`INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('T23-', LEFT(MD5(RAND()),4)), 42, 'apparel')`)
	psID := exec(`INSERT INTO product_size (product_id, size_id, quantity, sku) VALUES (?, ?, 5, 'LIVE-ORIG')`, prodID, sizeID)

	uuid := fmt.Sprintf("T23-%d", psID)
	orderID := exec(`INSERT INTO customer_order (uuid, order_status_id, currency, total_price)
		VALUES (?, (SELECT MIN(id) FROM order_status), 'EUR', 100)`, uuid)
	exec(`INSERT INTO order_item (order_id, product_id, product_price, product_price_base, quantity, size_id, product_sku)
		VALUES (?, ?, 100, 100, 1, ?, 'SNAP-FROZEN')`, orderID, prodID, sizeID)
	// a buyer + addresses so GetOrderFullByUUID's inner joins resolve.
	addrID := exec(`INSERT INTO address (country, city, address_line_one, postal_code) VALUES ('US', 'X', 'L1', '00000')`)
	exec(`INSERT INTO buyer (order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
		VALUES (?, 'A', 'B', 'a@b.cc', '1234567', ?, ?)`, orderID, addrID, addrID)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM order_item WHERE order_id = ?", orderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM buyer WHERE order_id = ?", orderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE id = ?", orderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM address WHERE id = ?", addrID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_size WHERE id = ?", psID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM size WHERE id = ?", sizeID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id = ?", prodID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	orderSKU := func() string {
		full, err := s.Order().GetOrderFullByUUID(ctx, uuid)
		require.NoError(t, err)
		require.Len(t, full.OrderItems, 1)
		return full.OrderItems[0].SKU
	}
	parcelSKU := func() string {
		items, err := s.Order().GetOrderParcelItems(ctx, int(orderID))
		require.NoError(t, err)
		require.Len(t, items, 1)
		return items[0].SKU
	}

	// before any catalogue change: reads reflect the frozen snapshot.
	require.Equal(t, "SNAP-FROZEN", orderSKU(), "order response uses the frozen snapshot")
	require.Equal(t, "SNAP-FROZEN", parcelSKU(), "label/customs uses the frozen snapshot")

	// catalogue remint of both the live variant SKU and the base SKU.
	_, err = testDB.ExecContext(ctx, "UPDATE product_size SET sku = 'LIVE-CHANGED' WHERE id = ?", psID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "UPDATE product SET sku = 'BASE-CHANGED' WHERE id = ?", prodID)
	require.NoError(t, err)

	// the snapshot is immutable: neither read follows the live/base SKU (no COALESCE fallback).
	require.Equal(t, "SNAP-FROZEN", orderSKU(), "order response must not follow a catalogue remint")
	require.Equal(t, "SNAP-FROZEN", parcelSKU(), "label/customs must not follow a catalogue remint")

	// NOT NULL: a line can never be persisted without an identity snapshot.
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO order_item (order_id, product_id, product_price, product_price_base, quantity, size_id, product_sku)
		 VALUES (?, ?, 100, 100, 1, ?, NULL)`, orderID, prodID, sizeID)
	require.Error(t, err, "a NULL product_sku snapshot must be rejected (NOT NULL)")
}
