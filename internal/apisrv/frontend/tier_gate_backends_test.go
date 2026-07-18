package frontend

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
)

// The two frontend tier-gate tests (hero hidden-product leak, pre-checkout gate) run against a real
// store so they exercise the exact tier-blind product reads the leak stems from. They require the
// ephemeral MySQL container the task provisions; without CI=1 + MYSQL_* they SKIP (never fall back to
// a config file, so they can never touch a real database).
var (
	tierBackendsOnce sync.Once
	tierBackendStore *store.MYSQLStore
	tierBackendDB    *sql.DB
	tierBackendErr   error
)

func tierGateBackends(t *testing.T) (*store.MYSQLStore, *sql.DB) {
	t.Helper()
	if os.Getenv("CI") == "" || os.Getenv("MYSQL_HOST") == "" {
		t.Skip("requires ephemeral MySQL container: set CI=1 and MYSQL_USER/PASSWORD/HOST/PORT/DATABASE")
	}
	tierBackendsOnce.Do(func() {
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
			os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"),
			os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_PORT"), os.Getenv("MYSQL_DATABASE"))
		// Build the store with a non-cancelled context: MYSQLStore closes its DB when the ctx it was
		// created with is done (store.go), and this store must live for the whole package test run.
		// Migrations carry their own internal timeout inside NewForTest.
		st, err := store.NewForTest(context.Background(), store.Config{
			DSN: dsn, Automigrate: true, MaxOpenConnections: 10, MaxIdleConnections: 5,
		})
		if err != nil {
			tierBackendErr = fmt.Errorf("NewForTest: %w", err)
			return
		}
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			tierBackendErr = fmt.Errorf("sql.Open: %w", err)
			return
		}
		tierBackendStore, tierBackendDB = st, db
	})
	if tierBackendErr != nil {
		t.Fatalf("tier gate backends init failed: %v", tierBackendErr)
	}
	return tierBackendStore, tierBackendDB
}

// seedTestMedia inserts a minimal media row via the store API and returns its id.
func seedTestMedia(ctx context.Context, t *testing.T, st *store.MYSQLStore) int {
	t.Helper()
	id, err := st.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	t.Cleanup(func() { _, _ = tierBackendDB.ExecContext(context.Background(), "DELETE FROM media WHERE id = ?", id) })
	return id
}

// seedTestSize inserts a minimal size row and returns its id.
func seedTestSize(ctx context.Context, t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.ExecContext(ctx, `INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('FT-', LEFT(MD5(RAND()),6)), 42, 'apparel')`)
	if err != nil {
		t.Fatalf("seed size: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed size id: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM size WHERE id = ?", id) })
	return id
}

// seedTierProduct inserts an ACTIVE product (lifecycle_status=2) with the given min_tier + hidden flag,
// optionally tagged `tag`, plus one buyable variant. It returns (productID, baseSKU, variantSKU). Its
// own style (distinct tech_card) keeps (style_id, color_code) unique so every product can share BLK.
func seedTierProduct(ctx context.Context, t *testing.T, db *sql.DB, mediaID int, sizeID int64, baseSKU string, minTier int16, hidden bool, tag string) (int, string, string) {
	t.Helper()
	mustExec := func(q string, args ...any) sql.Result {
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return res
	}
	styleRes := mustExec(`INSERT INTO tech_card (style_number, name, brand, collection, season_code, season_year, season, target_gender, top_category_id)
		VALUES (CONCAT('FT-', UUID_SHORT()), 'FT', 'ACME', '', 'SS', 2026, 'SS26', 'unisex', 1)`)
	styleID, _ := styleRes.LastInsertId()
	prodRes := mustExec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status, min_tier, hidden_for_non_qualified)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?, 2, ?, ?)`, baseSKU, mediaID, styleID, minTier, hidden)
	pid, _ := prodRes.LastInsertId()
	mustExec(`INSERT INTO product_price (product_id, currency, price) VALUES (?, 'EUR', 100.00)`, pid)
	variantSKU := "V" + baseSKU
	mustExec(`INSERT INTO product_size (product_id, size_id, quantity, sku) VALUES (?, ?, 10, ?)`, pid, sizeID, variantSKU)
	if tag != "" {
		mustExec(`INSERT INTO product_tag (product_id, tag) VALUES (?, ?)`, pid, tag)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = db.ExecContext(cctx, "DELETE FROM product_tag WHERE product_id = ?", pid)
		_, _ = db.ExecContext(cctx, "DELETE FROM product_size WHERE product_id = ?", pid)
		_, _ = db.ExecContext(cctx, "DELETE FROM product_price WHERE product_id = ?", pid)
		_, _ = db.ExecContext(cctx, "DELETE FROM product WHERE id = ?", pid)
		_, _ = db.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
	})
	return int(pid), baseSKU, variantSKU
}
