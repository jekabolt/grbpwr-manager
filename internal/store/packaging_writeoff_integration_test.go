package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
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

// item puts one picked packaging item on the fixture's card, limited to the given colourways.
func (f *packagingFixture) item(ctx context.Context, t *testing.T, key string, qty, materialID int, perOrder bool, colorways ...int) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_packaging_item (tech_card_id, item_key, qty_per_garment, material_id, per_order)
		VALUES (?, ?, ?, ?, ?)`, f.cardID, key, qty, materialID, perOrder)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	for _, cw := range colorways {
		_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_packaging_item_colorway (item_id, colorway_id) VALUES (?, ?)`, id, cw)
		require.NoError(t, err)
	}
	return int(id)
}

// packed sets what the card's packaging sheet says about folding and the shipping box.
func (f *packagingFixture) packed(ctx context.Context, t *testing.T, folding string, unitsPerBox int) {
	t.Helper()
	_, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_packaging (tech_card_id, folding_method, units_per_box) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE folding_method = VALUES(folding_method), units_per_box = VALUES(units_per_box)`,
		f.cardID, sql.NullString{String: folding, Valid: folding != ""}, sql.NullInt32{Int32: int32(unitsPerBox), Valid: unitsPerBox > 0})
	require.NoError(t, err)
}

// ship places and ships an order, reserve then consume, as the real path does.
func (f *packagingFixture) ship(ctx context.Context, t *testing.T, lines ...[2]int) int {
	t.Helper()
	id := f.order(ctx, t, lines...)
	MS := f.s.MaterialStock()
	require.NoError(t, MS.ReservePackagingForOrder(ctx, id, "tester"))
	_, err := MS.ConsumePackagingForOrder(ctx, id, "tester")
	require.NoError(t, err)
	return id
}

func packagingTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The test before the switch (TestPackagingWriteoffBeforeSwitch, commit cdd9e7d) pinned these numbers on
// the old recipe, on the same order: 3 + 2 pieces of two colours. An item for all colours takes what the
// style recipe took; items naming both colours take the same bags, and one box where the two product
// recipes took two: an item counts once on an order, whichever colours it covers.
func TestPackagingWriteoffAfterSwitch(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)

	box := f.material(ctx, t, "After box", 100)
	bag := f.material(ctx, t, "After bag", 100)
	f.item(ctx, t, "other", 1, box, true)
	f.item(ctx, t, "dust_bag", 1, bag, false)
	f.ship(ctx, t, [2]int{f.black, 3}, [2]int{f.white, 2})
	f.requireOnHand(ctx, t, box, "99", "one box for the order")
	f.requireOnHand(ctx, t, bag, "95", "a bag per unit")

	box2 := f.material(ctx, t, "After box 2", 100)
	bag2 := f.material(ctx, t, "After bag 2", 100)
	f.item(ctx, t, "other", 1, box2, true, f.black, f.white)
	f.item(ctx, t, "dust_bag", 1, bag2, false, f.black, f.white)
	f.ship(ctx, t, [2]int{f.black, 3}, [2]int{f.white, 2})
	f.requireOnHand(ctx, t, box2, "99", "the item counts once")
	f.requireOnHand(ctx, t, bag2, "95", "a bag per unit")
}

// A mixed order takes a shared per-order item once and a colour's own item once, whichever line came first.
func TestPackagingWriteoffMixedColoursEitherLineOrder(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	shared := f.material(ctx, t, "Shared mailer", 100)
	blackInsert := f.material(ctx, t, "Black insert", 100)
	whiteBag := f.material(ctx, t, "White bag", 100)
	f.item(ctx, t, "mailer", 1, shared, true)
	f.item(ctx, t, "insert_card", 1, blackInsert, true, f.black)
	f.item(ctx, t, "dust_bag", 1, whiteBag, false, f.white)

	f.ship(ctx, t, [2]int{f.black, 3}, [2]int{f.white, 2})
	f.requireOnHand(ctx, t, shared, "99", "black first: shared once")
	f.requireOnHand(ctx, t, blackInsert, "99", "black first: its insert once")
	f.requireOnHand(ctx, t, whiteBag, "98", "black first: bags on white pieces only")

	f.ship(ctx, t, [2]int{f.white, 2}, [2]int{f.black, 3})
	f.requireOnHand(ctx, t, shared, "98", "white first: shared once")
	f.requireOnHand(ctx, t, blackInsert, "98", "white first: its insert once")
	f.requireOnHand(ctx, t, whiteBag, "96", "white first: bags on white pieces only")

	// An order of the other colour alone leaves the black insert alone.
	f.ship(ctx, t, [2]int{f.white, 1})
	f.requireOnHand(ctx, t, blackInsert, "98", "white alone takes no black insert")
	f.requireOnHand(ctx, t, whiteBag, "95", "white alone takes its bag")
}

// The hanger goes only with a piece shipped on one; a card without items takes nothing.
func TestPackagingWriteoffHangerAndEmptyCard(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	empty := f.ship(ctx, t, [2]int{f.black, 2})
	var skipped int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT skipped_count FROM order_packaging_consumed WHERE order_id = ?`, empty).Scan(&skipped))
	require.Zero(t, skipped)

	hanger := f.material(ctx, t, "Hanger", 100)
	f.item(ctx, t, entity.PackagingItemKeyHanger, 1, hanger, false)
	f.packed(ctx, t, "folded", 0)
	f.ship(ctx, t, [2]int{f.black, 2})
	f.requireOnHand(ctx, t, hanger, "100", "folded: no hanger")
	f.packed(ctx, t, entity.PackagingFoldingOnHanger, 0)
	f.ship(ctx, t, [2]int{f.black, 2})
	f.requireOnHand(ctx, t, hanger, "98", "on a hanger: one each")
}

// A share of a box, the owner's rule: 1 piece takes 1/capacity, a full box of pieces exactly one, and
// the three decimal places of the stock lose nothing however the shares add up.
func TestPackagingWriteoffBoxShares(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	box := f.material(ctx, t, "Share box", 100)
	f.item(ctx, t, entity.PackagingItemKeyBox, 1, box, true)

	f.packed(ctx, t, "", 12)
	for i := 0; i < 12; i++ {
		f.ship(ctx, t, [2]int{f.black, 1})
	}
	f.requireOnHand(ctx, t, box, "99", "12 orders of 1/12 are exactly one box")

	f.packed(ctx, t, "", 20)
	for i := 0; i < 20; i++ {
		f.ship(ctx, t, [2]int{f.black, 1})
	}
	f.requireOnHand(ctx, t, box, "98", "20 orders of 1/20 are exactly one box")

	f.ship(ctx, t, [2]int{f.black, 20})
	f.requireOnHand(ctx, t, box, "97", "20 pieces on one order are exactly one box")

	f.ship(ctx, t, [2]int{f.black, 3}, [2]int{f.white, 2})
	f.requireOnHand(ctx, t, box, "96.75", "5 of 20 over two colours, a quarter box")

	// Shares below the third place book nothing yet and are not lost.
	tiny := f.material(ctx, t, "Tiny box", 10)
	_, err := testDB.ExecContext(ctx, `DELETE FROM tech_card_packaging_item WHERE material_id = ?`, box)
	require.NoError(t, err)
	f.item(ctx, t, entity.PackagingItemKeyBox, 1, tiny, true)
	f.packed(ctx, t, "", 7000)
	for i := 0; i < 7; i++ {
		f.ship(ctx, t, [2]int{f.black, 1})
	}
	f.requireOnHand(ctx, t, tiny, "9.999", "7 of 7000 add up to the one thousandth they are")
}

// Stock exactly at the share books it; a thousandth short does not fail and is taken on a re-ship.
func TestPackagingWriteoffShareOnTheStockBoundary(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	MS := f.s.MaterialStock()
	receive := func(mat int, qty string) {
		t.Helper()
		_, err := MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
			MaterialId: mat, Quantity: decimal.RequireFromString(qty),
			UnitCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true}, Currency: "EUR",
		})
		require.NoError(t, err)
	}
	exact := f.material(ctx, t, "Exact box", 0)
	receive(exact, "0.125")
	item := f.item(ctx, t, entity.PackagingItemKeyBox, 1, exact, true)
	f.packed(ctx, t, "", 8)
	f.ship(ctx, t, [2]int{f.black, 1})
	f.requireOnHand(ctx, t, exact, "0", "exactly the share")

	short := f.material(ctx, t, "Short box", 0)
	receive(short, "0.124")
	_, err := testDB.ExecContext(ctx, `UPDATE tech_card_packaging_item SET material_id = ? WHERE id = ?`, short, item)
	require.NoError(t, err)
	orderID := f.ship(ctx, t, [2]int{f.black, 1})
	f.requireOnHand(ctx, t, short, "0.124", "short: nothing taken")
	var booked, skipped int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT movement_count, skipped_count FROM order_packaging_consumed WHERE order_id = ?`, orderID).Scan(&booked, &skipped))
	require.Zero(t, booked)
	require.Equal(t, 1, skipped)

	// Still short: the debt stays. Received: one re-ship takes it, the next takes nothing.
	mvs, err := MS.ConsumePackagingForOrder(ctx, orderID, "tester")
	require.NoError(t, err)
	require.Empty(t, mvs)
	receive(short, "1")
	mvs, err = MS.ConsumePackagingForOrder(ctx, orderID, "tester")
	require.NoError(t, err)
	require.Len(t, mvs, 1)
	f.requireOnHand(ctx, t, short, "0.999", "the debt is taken once stock came")
	mvs, err = MS.ConsumePackagingForOrder(ctx, orderID, "tester")
	require.NoError(t, err)
	require.Empty(t, mvs, "a settled debt is not taken twice")
	f.requireOnHand(ctx, t, short, "0.999", "and the stock stays")
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT movement_count, skipped_count FROM order_packaging_consumed WHERE order_id = ?`, orderID).Scan(&booked, &skipped))
	require.Equal(t, 1, booked)
	require.Zero(t, skipped)
}

// What placement claimed ships, whatever the card says by then; a second ship takes nothing.
func TestPackagingWriteoffOpenClaimWinsAndReshipIsIdempotent(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	MS := f.s.MaterialStock()
	bag := f.material(ctx, t, "Claimed bag", 100)
	other := f.material(ctx, t, "New bag", 100)
	f.item(ctx, t, "dust_bag", 1, bag, false)

	orderID := f.order(ctx, t, [2]int{f.black, 3})
	require.NoError(t, MS.ReservePackagingForOrder(ctx, orderID, "tester"))
	require.NoError(t, MS.ReservePackagingForOrder(ctx, orderID, "tester"), "a repeat claims nothing more")
	_, err := testDB.ExecContext(ctx, `DELETE FROM tech_card_packaging_item WHERE tech_card_id = ?`, f.cardID)
	require.NoError(t, err)
	f.item(ctx, t, "dust_bag", 2, other, false)

	mvs, err := MS.ConsumePackagingForOrder(ctx, orderID, "tester")
	require.NoError(t, err)
	require.Len(t, mvs, 1)
	f.requireOnHand(ctx, t, bag, "97", "the claim ships")
	f.requireOnHand(ctx, t, other, "100", "the edit does not")
	mvs, err = MS.ConsumePackagingForOrder(ctx, orderID, "tester")
	require.NoError(t, err)
	require.Empty(t, mvs)
	f.requireOnHand(ctx, t, bag, "97", "a re-ship takes nothing")

	// An order that was never reserved resolves fresh from the card as it is.
	fresh := f.order(ctx, t, [2]int{f.black, 1})
	_, err = MS.ConsumePackagingForOrder(ctx, fresh, "tester")
	require.NoError(t, err)
	f.requireOnHand(ctx, t, other, "98", "fresh path")
}

// An auxiliary card's pick takes the colour's variant, or the plain output, or is counted as skipped.
func TestPackagingWriteoffAuxiliaryOutputs(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	MS := f.s.MaterialStock()
	aux := func(name string, output sql.NullInt32) int {
		t.Helper()
		res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card (style_number, name, output_material_id) VALUES (CONCAT('PKGX-', UUID_SHORT()), ?, ?)`, name, output)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() {
			cctx := context.Background()
			_, _ = testDB.ExecContext(cctx, `DELETE FROM tech_card_output_variant WHERE tech_card_id = ?`, id)
			_, _ = testDB.ExecContext(cctx, `DELETE FROM tech_card WHERE id = ?`, id)
		})
		return int(id)
	}
	pick := func(key string, card int) {
		t.Helper()
		_, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_packaging_item (tech_card_id, item_key, qty_per_garment, aux_tech_card_id, per_order)
			VALUES (?, ?, 1, ?, 0)`, f.cardID, key, card)
		require.NoError(t, err)
	}
	blackTag := f.material(ctx, t, "Black tag", 100)
	whiteTag := f.material(ctx, t, "White tag", 100)
	plainTag := f.material(ctx, t, "Plain tag", 100)
	variantCard := aux("variants", sql.NullInt32{})
	for code, mat := range map[string]int{"BLK": blackTag, "WHT": whiteTag} {
		_, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_output_variant (tech_card_id, color_code, material_id, active) VALUES (?, ?, ?, TRUE)`, variantCard, code, mat)
		require.NoError(t, err)
	}
	pick("hangtag", variantCard)
	pick("sticker", aux("plain", sql.NullInt32{Int32: int32(plainTag), Valid: true}))
	pick("insert_card", aux("nothing", sql.NullInt32{}))

	orderID := f.order(ctx, t, [2]int{f.black, 3}, [2]int{f.white, 2})
	_, err := MS.ConsumePackagingForOrder(ctx, orderID, "tester")
	require.NoError(t, err)
	f.requireOnHand(ctx, t, blackTag, "97", "black pieces, black variant")
	f.requireOnHand(ctx, t, whiteTag, "98", "white pieces, white variant")
	f.requireOnHand(ctx, t, plainTag, "95", "plain output on every piece")
	var skipped int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT skipped_count FROM order_packaging_consumed WHERE order_id = ?`, orderID).Scan(&skipped))
	require.Equal(t, 2, skipped, "the pick with no output, on each of two lines")
}

// The old doors are shut: a recipe or a global list cannot come back and do nothing.
func TestPackagingOldWritesRefused(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	MS := f.s.MaterialStock()
	mat := f.material(ctx, t, "Refused", 10)
	err := MS.UpsertPackagingRecipe(ctx, entity.PackagingScopeStyle, sql.NullInt32{Int32: int32(f.cardID), Valid: true}, sql.NullInt32{},
		[]entity.PackagingRecipeInsert{{MaterialId: mat, QtyPerItem: decimal.NewFromInt(1), Active: true}}, "tester")
	require.ErrorIs(t, err, entity.ErrPackagingRecipeInvalid)
	require.Error(t, MS.UpsertPackagingBom(ctx, []entity.PackagingBomItem{{MaterialId: mat, QtyPerItem: decimal.NewFromInt(1), Active: true}}))
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM packaging_recipe WHERE material_id = ?`, mat).Scan(&n))
	require.Zero(t, n)
}

// runPackagingMigrationUp replays the Up half of 0450 on one connection (its @variables need one).
func runPackagingMigrationUp(ctx context.Context, t *testing.T) error {
	t.Helper()
	raw, err := os.ReadFile("sql/0450_tech_card_packaging_one_source.sql")
	require.NoError(t, err)
	up, _, ok := strings.Cut(string(raw), "-- +migrate Down")
	require.True(t, ok)
	var kept []string
	for _, line := range strings.Split(up, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			kept = append(kept, line)
		}
	}
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	for _, stmt := range strings.Split(strings.Join(kept, "\n"), ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// The transfer: recipes become items with the same meaning, go dark, and running it twice changes nothing.
func TestPackagingMigrationMovesRecipes(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	_, err := testDB.ExecContext(ctx, `DELETE FROM packaging_recipe`)
	require.NoError(t, err)

	both := f.material(ctx, t, "Migr dust bag", 100)
	blackOnly := f.material(ctx, t, "Migr insert", 100)
	twoWays := f.material(ctx, t, "Migr thing", 100)
	kept := f.material(ctx, t, "Migr kept", 100)
	ignored := f.material(ctx, t, "Migr global", 100)
	f.item(ctx, t, "other", 3, kept, false)
	recipe := func(scope string, cw, mat int, perOrder, perItem string) {
		t.Helper()
		_, err := testDB.ExecContext(ctx, `INSERT INTO packaging_recipe (scope, product_id, material_id, qty_per_order, qty_per_item, active)
			VALUES (?, ?, ?, ?, ?, TRUE)`, scope, sql.NullInt32{Int32: int32(cw), Valid: cw != 0}, mat, perOrder, perItem)
		require.NoError(t, err)
	}
	recipe("product", f.black, both, "0", "1")
	recipe("product", f.white, both, "0", "1")
	recipe("product", f.black, blackOnly, "0", "2")
	recipe("product", f.black, twoWays, "1", "1")
	recipe("product", f.black, kept, "0", "1")
	recipe("global", 0, ignored, "1", "1")

	require.NoError(t, runPackagingMigrationUp(ctx, t))
	require.NoError(t, runPackagingMigrationUp(ctx, t), "a second run finds nothing to move")

	var active int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM packaging_recipe WHERE active = TRUE`).Scan(&active))
	require.Zero(t, active, "every recipe is dark")

	type row struct {
		Key       string
		Qty       int
		PerOrder  bool
		Colorways int
	}
	read := func(mat int) []row {
		t.Helper()
		q, err := testDB.QueryContext(ctx, `
			SELECT i.item_key, i.qty_per_garment, COALESCE(i.per_order, 0) AS po,
			       (SELECT COUNT(*) FROM tech_card_packaging_item_colorway c WHERE c.item_id = i.id) AS cw
			FROM tech_card_packaging_item i WHERE i.material_id = ? ORDER BY po`, mat)
		require.NoError(t, err)
		defer q.Close()
		var rows []row
		for q.Next() {
			var r row
			require.NoError(t, q.Scan(&r.Key, &r.Qty, &r.PerOrder, &r.Colorways))
			rows = append(rows, r)
		}
		require.NoError(t, q.Err())
		return rows
	}
	require.Equal(t, []row{{"dust_bag", 1, false, 0}}, read(both), "both colours named means no limit")
	require.Equal(t, []row{{"insert_card", 2, false, 1}}, read(blackOnly), "one colour keeps its limit")
	require.Equal(t, []row{{"other", 1, false, 1}, {"other", 1, true, 1}}, read(twoWays), "both quantities make two items")
	require.Equal(t, []row{{"other", 3, false, 0}}, read(kept), "a card's own item stays as it is")
	require.Empty(t, read(ignored), "the global recipe moves nowhere")

	// A fractional quantity stops the file and moves nothing.
	frac := f.material(ctx, t, "Migr fraction", 100)
	recipe("product", f.black, frac, "0", "0.5")
	require.Error(t, runPackagingMigrationUp(ctx, t))
	require.Empty(t, read(frac))
	_, err = testDB.ExecContext(ctx, `DELETE FROM packaging_recipe WHERE material_id = ?`, frac)
	require.NoError(t, err)
}

// Deleting a colourway named alone by an item takes the item: left without colours it would mean «all».
func TestPackagingDeleteColorwayTakesItsOwnItems(t *testing.T) {
	ctx := packagingTestCtx(t)
	f := newPackagingFixture(ctx, t)
	alone := f.material(ctx, t, "Del alone", 10)
	shared := f.material(ctx, t, "Del shared", 10)
	f.item(ctx, t, "insert_card", 1, alone, false, f.white)
	sharedID := f.item(ctx, t, "dust_bag", 1, shared, false, f.white, f.black)

	require.NoError(t, product.DeleteColorwayOnlyPackagingItems(ctx, f.s.DB(), f.white))
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_packaging_item WHERE material_id = ?`, alone).Scan(&n))
	require.Zero(t, n, "named only that colour")
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_packaging_item_colorway WHERE item_id = ? AND colorway_id = ?`, sharedID, f.black).Scan(&n))
	require.Equal(t, 1, n, "the other colour keeps its item")
}
