package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestSizeDeleteRestricted is the acceptance test for problem 018: a size referenced by a variant, an
// order-history line or a style size-chart can no longer be physically deleted (ON DELETE RESTRICT,
// migration 0149), and the rejected DELETE mutates no dependent row. It seeds via SQL because
// AddProduct is unrelated-broken at this base by the 0146 season CHECK.
func TestSizeDeleteRestricted(t *testing.T) {
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
	count := func(q string, args ...any) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, q, args...).Scan(&n))
		return n
	}

	// media + style + product (colourway) — minimal rows to satisfy FKs.
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	styleID := exec(`INSERT INTO tech_card (style_number, name) VALUES (CONCAT('AUTO-', UUID_SHORT()), 'S18')`)
	prodID := exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (CONCAT('SS26-00001-', LEFT(MD5(RAND()),3)), 'c', 'BLK', '#000000', 'US', ?, ?)`, mediaID, styleID)
	// a dedicated, otherwise-unused size so only our own rows reference it.
	sizeID := exec(`INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('T18-', LEFT(MD5(RAND()),4)), 42, 'apparel')`)
	nameID := exec(`INSERT INTO measurement_name (name) VALUES (CONCAT('MN18-', LEFT(MD5(RAND()),6)))`)

	// dependents that reference the size: a variant, an order line, a style-chart row.
	psID := exec(`INSERT INTO product_size (product_id, size_id, quantity) VALUES (?, ?, 5)`, prodID, sizeID)
	orderID := exec(`INSERT INTO customer_order (uuid, order_status_id, currency, total_price)
		VALUES (CONCAT('T18-', UUID_SHORT()), (SELECT MIN(id) FROM order_status), 'EUR', 100)`)
	oiID := exec(`INSERT INTO order_item (order_id, product_id, variant_id, product_price, product_price_base, quantity, size_id, variant_sku_snapshot)
		VALUES (?, ?, ?, 100, 100, 1, ?, 'SS26-00001-BLK')`, orderID, prodID, psID, sizeID)
	tcsmID := exec(`INSERT INTO tech_card_size_measurement (tech_card_id, size_id, measurement_name_id, measurement_value)
		VALUES (?, ?, ?, 10.0)`, styleID, sizeID, nameID)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card_size_measurement WHERE id = ?", tcsmID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM order_item WHERE id = ?", oiID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE id = ?", orderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_size WHERE id = ?", psID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM size WHERE id = ?", sizeID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM measurement_name WHERE id = ?", nameID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id = ?", prodID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	// the used size cannot be deleted (RESTRICT).
	_, err = testDB.ExecContext(ctx, "DELETE FROM size WHERE id = ?", sizeID)
	require.Error(t, err, "deleting a used size must be rejected by ON DELETE RESTRICT")

	// no dependent row was touched by the rejected DELETE.
	require.Equal(t, 1, count("SELECT COUNT(*) FROM product_size WHERE id = ?", psID), "variant survives")
	require.Equal(t, 1, count("SELECT COUNT(*) FROM order_item WHERE id = ?", oiID), "order-history line survives")
	require.Equal(t, 1, count("SELECT COUNT(*) FROM tech_card_size_measurement WHERE id = ?", tcsmID), "style-chart row survives")
	require.Equal(t, 1, count("SELECT COUNT(*) FROM size WHERE id = ?", sizeID), "size itself survives")

	// removing every reference lets the size be deleted again — proving RESTRICT (not something else)
	// was the blocker, and that each of the three FKs enforces it.
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_size_measurement WHERE id = ?", tcsmID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "DELETE FROM size WHERE id = ?", sizeID)
	require.Error(t, err, "still referenced by product_size + order_item")
	_, err = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE id = ?", oiID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "DELETE FROM size WHERE id = ?", sizeID)
	require.Error(t, err, "still referenced by product_size")
	_, err = testDB.ExecContext(ctx, "DELETE FROM product_size WHERE id = ?", psID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "DELETE FROM size WHERE id = ?", sizeID)
	require.NoError(t, err, "with no references, the size deletes cleanly")
}
