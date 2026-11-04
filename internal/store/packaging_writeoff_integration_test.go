package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// packagingFixture is one card with two colourways, packaging materials with stock, and a helper that
// places an order over the colourways. Everything it makes is removed by t.Cleanup.
type packagingFixture struct {
	s      *MYSQLStore
	cardID int
	black  int
	white  int
	sizeID int
}

func newPackagingFixture(ctx context.Context, t *testing.T) *packagingFixture {
	t.Helper()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	exec := func(q string, args ...any) int {
		t.Helper()
		res, err := testDB.ExecContext(ctx, q, args...)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	f := &packagingFixture{s: s}
	f.cardID = exec(`INSERT INTO tech_card (style_number, name) VALUES (CONCAT('PKG-', UUID_SHORT()), 'packaging')`)
	colorway := func(code string) int {
		return exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
			VALUES (CONCAT('PKG-', LEFT(MD5(RAND()), 8)), ?, ?, '#000000', 'IT', ?, ?)`, code, code, mediaID, f.cardID)
	}
	f.black = colorway("BLK")
	f.white = colorway("WHT")
	f.sizeID = exec(`INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('PKG-', LEFT(MD5(RAND()), 4)), 42, 'apparel')`)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, `DELETE FROM packaging_recipe`)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM order_item WHERE product_id IN (?, ?)`, f.black, f.white)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM product_size WHERE product_id IN (?, ?)`, f.black, f.white)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM product WHERE id IN (?, ?)`, f.black, f.white)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM size WHERE id = ?`, f.sizeID)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM tech_card WHERE id = ?`, f.cardID)
	})
	return f
}

// material makes a packaging material holding onHand units.
func (f *packagingFixture) material(ctx context.Context, t *testing.T, name string, onHand int64) int {
	t.Helper()
	id, err := f.s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: name, Section: "packaging", Unit: sql.NullString{String: "pc", Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, `DELETE FROM material_stock_movement WHERE material_id = ?`, id)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM material WHERE id = ?`, id)
	})
	if onHand > 0 {
		_, err = f.s.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
			MaterialId: id, Quantity: decimal.NewFromInt(onHand),
			UnitCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true}, Currency: "EUR",
		})
		require.NoError(t, err)
	}
	return id
}

// order places an order holding the given colourway → units lines, in the given order.
func (f *packagingFixture) order(ctx context.Context, t *testing.T, lines ...[2]int) int {
	t.Helper()
	orderID := seedOrder(ctx, t)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, `DELETE FROM order_packaging_consumed WHERE order_id = ?`, orderID)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM order_item WHERE order_id = ?`, orderID)
		_, _ = testDB.ExecContext(cctx, `DELETE FROM customer_order WHERE id = ?`, orderID)
	})
	for _, ln := range lines {
		var variantID int64
		err := testDB.QueryRowContext(ctx, `SELECT id FROM product_size WHERE product_id = ? AND size_id = ?`, ln[0], f.sizeID).Scan(&variantID)
		if err == sql.ErrNoRows {
			res, ierr := testDB.ExecContext(ctx, `INSERT INTO product_size (product_id, size_id, quantity) VALUES (?, ?, 100)`, ln[0], f.sizeID)
			require.NoError(t, ierr)
			variantID, err = res.LastInsertId()
		}
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO order_item
			(order_id, product_id, variant_id, product_price, product_price_base, quantity, size_id, variant_sku_snapshot)
			VALUES (?, ?, ?, 100, 100, ?, ?, 'PKG')`, orderID, ln[0], variantID, ln[1], f.sizeID)
		require.NoError(t, err)
	}
	return orderID
}

func (f *packagingFixture) requireOnHand(ctx context.Context, t *testing.T, materialID int, want string, msg string) {
	t.Helper()
	st, err := f.s.MaterialStock().GetMaterialStock(ctx, materialID)
	require.NoError(t, err)
	require.True(t, st.OnHand.Equal(decimal.RequireFromString(want)), "%s: on hand %s, want %s", msg, st.OnHand, want)
}

// TestPackagingWriteoffBeforeSwitch pins what an order of two colourways writes off through the old
// recipe, reserve then ship: per-item scales with units, per-order counts once per resolved recipe.
func TestPackagingWriteoffBeforeSwitch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	f := newPackagingFixture(ctx, t)
	MS := f.s.MaterialStock()
	one := decimal.NewFromInt(1)
	style := sql.NullInt32{Int32: int32(f.cardID), Valid: true}

	box := f.material(ctx, t, "Before box", 100)
	bag := f.material(ctx, t, "Before bag", 100)
	require.NoError(t, MS.UpsertPackagingRecipe(ctx, entity.PackagingScopeStyle, style, sql.NullInt32{}, []entity.PackagingRecipeInsert{
		{MaterialId: box, QtyPerOrder: one, Active: true},
		{MaterialId: bag, QtyPerItem: one, Active: true},
	}, "tester"))
	styleOrder := f.order(ctx, t, [2]int{f.black, 3}, [2]int{f.white, 2})
	require.NoError(t, MS.ReservePackagingForOrder(ctx, styleOrder, "tester"))
	_, err := MS.ConsumePackagingForOrder(ctx, styleOrder, 5, "tester")
	require.NoError(t, err)
	f.requireOnHand(ctx, t, box, "99", "style recipe: one box for the order")
	f.requireOnHand(ctx, t, bag, "95", "style recipe: a bag per unit")

	// Product scope: each colourway is its own recipe, so its box counts apart.
	box2 := f.material(ctx, t, "Before box 2", 100)
	bag2 := f.material(ctx, t, "Before bag 2", 100)
	for _, cw := range []int{f.black, f.white} {
		require.NoError(t, MS.UpsertPackagingRecipe(ctx, entity.PackagingScopeProduct, sql.NullInt32{}, sql.NullInt32{Int32: int32(cw), Valid: true}, []entity.PackagingRecipeInsert{
			{MaterialId: box2, QtyPerOrder: one, Active: true},
			{MaterialId: bag2, QtyPerItem: one, Active: true},
		}, "tester"))
	}
	productOrder := f.order(ctx, t, [2]int{f.black, 3}, [2]int{f.white, 2})
	require.NoError(t, MS.ReservePackagingForOrder(ctx, productOrder, "tester"))
	_, err = MS.ConsumePackagingForOrder(ctx, productOrder, 5, "tester")
	require.NoError(t, err)
	f.requireOnHand(ctx, t, box2, "98", "product recipes: a box per colourway")
	f.requireOnHand(ctx, t, bag2, "95", "product recipes: a bag per unit")
}
