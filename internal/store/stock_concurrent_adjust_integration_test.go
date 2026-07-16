package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestConcurrentStockAdjustNoLostUpdate is the acceptance test for problem 025: N concurrent +1 stock
// adjustments must compose exactly (final = start + N) and record N history rows with a consistent,
// gap-free before/after chain — no lost update. Seeded via SQL (AddProduct is unrelated-broken at this
// base by the 0146 season CHECK).
func TestConcurrentStockAdjustNoLostUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	exec := func(q string, args ...any) int64 {
		res, err := testDB.ExecContext(ctx, q, args...)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	styleID := exec(`INSERT INTO tech_card (style_number, name) VALUES (CONCAT('AUTO-', UUID_SHORT()), 'S25')`)
	prodID := int(exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (CONCAT('SS26-00099-', LEFT(MD5(RAND()),3)), 'c', 'BLK', '#000000', 'US', ?, ?)`, mediaID, styleID))
	const start = 10
	exec(`INSERT INTO product_size (product_id, size_id, quantity) VALUES (?, ?, ?)`, prodID, sizeID, start)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_stock_change_history WHERE product_id = ?", prodID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_size WHERE product_id = ?", prodID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id = ?", prodID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	// N goroutines each apply a +1 adjustment, released together by a barrier.
	const n = 10
	var wg sync.WaitGroup
	start2 := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start2
			_, _, e := s.Products().UpdateProductSizeStockWithHistory(ctx, prodID, sizeID, entity.StockUpdateModeAdjust, 1, "correction", "")
			errs[i] = e
		}(i)
	}
	close(start2)
	wg.Wait()
	for i, e := range errs {
		require.NoError(t, e, "adjustment %d", i)
	}

	// final quantity is exactly start + N — no lost update.
	var final int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?`, prodID, sizeID).Scan(&final))
	require.Equal(t, start+n, final, "final stock must equal start + N (no lost update)")

	// exactly N history rows, each a +1 delta, with a gap-free after chain start+1 .. start+N.
	var rowCount, deltaSum, distinctAfter, minAfter, maxAfter int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*), CAST(COALESCE(SUM(quantity_delta),0) AS SIGNED), COUNT(DISTINCT quantity_after),
		        CAST(COALESCE(MIN(quantity_after),0) AS SIGNED), CAST(COALESCE(MAX(quantity_after),0) AS SIGNED)
		 FROM product_stock_change_history WHERE product_id = ? AND size_id = ? AND source = 'manual_adjustment'`,
		prodID, sizeID).Scan(&rowCount, &deltaSum, &distinctAfter, &minAfter, &maxAfter))
	require.Equal(t, n, rowCount, "exactly N history rows")
	require.Equal(t, n, deltaSum, "each row is a +1 delta")
	require.Equal(t, n, distinctAfter, "every after value is distinct (no two txns saw the same before)")
	require.Equal(t, start+1, minAfter, "after chain starts at start+1")
	require.Equal(t, start+n, maxAfter, "after chain ends at start+N")
}
