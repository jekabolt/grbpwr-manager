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

// TestPackagingBomConsumeOnShip exercises gap-07 v2 B: the packaging recipe CRUD (full-replace + the
// material-name join) and ConsumePackagingForOrder — the per-order/per-item write-off, the
// idempotency guard (a re-ship consumes nothing more), and the best-effort skip of a material short
// on stock (which must not fail the whole consume).
func TestPackagingBomConsumeOnShip(t *testing.T) {
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

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	MS := s.MaterialStock()

	// three packaging materials.
	mk := func(name string) int {
		id, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: name, Section: "packaging", Unit: sql.NullString{String: "pc", Valid: true}})
		require.NoError(t, err)
		return id
	}
	box := mk("NF Pkg Box")
	bag := mk("NF Pkg DustBag")
	shortMat := mk("NF Pkg Short")

	var orderA, orderB int
	t.Cleanup(func() {
		cctx := context.Background() // test ctx is cancelled before cleanups run
		for _, oid := range []int{orderA, orderB} {
			if oid != 0 {
				_, _ = testDB.ExecContext(cctx, "DELETE FROM order_packaging_consumed WHERE order_id = ?", oid)
				_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE id = ?", oid)
			}
		}
		// The global recipe now lives in packaging_recipe (scope='global'); packaging_bom is vestigial.
		_, _ = testDB.ExecContext(cctx, "DELETE FROM packaging_recipe")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM packaging_bom")
		for _, id := range []int{box, bag, shortMat} {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
		}
	})

	// stock: plenty of box + bag, only 1 of shortMat.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: box, Quantity: decimal.NewFromInt(100), UnitCost: nd("2"), Currency: "EUR"})
	require.NoError(t, err)
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: bag, Quantity: decimal.NewFromInt(100), UnitCost: nd("1"), Currency: "EUR"})
	require.NoError(t, err)
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: shortMat, Quantity: decimal.NewFromInt(1), UnitCost: nd("1"), Currency: "EUR"})
	require.NoError(t, err)

	// --- recipe CRUD: box 1/order, bag 1/item ---
	require.NoError(t, MS.UpsertPackagingBom(ctx, []entity.PackagingBomItem{
		{MaterialId: box, QtyPerOrder: decimal.NewFromInt(1), Active: true},
		{MaterialId: bag, QtyPerItem: decimal.NewFromInt(1), Active: true},
	}))
	list, err := MS.ListPackagingBom(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	byMat := map[int]entity.PackagingBomItem{}
	for _, it := range list {
		byMat[it.MaterialId] = it
	}
	require.Equal(t, "NF Pkg Box", byMat[box].MaterialName, "recipe joins the material name")
	require.Equal(t, "pc", byMat[box].MaterialUnit.String)
	require.True(t, byMat[box].QtyPerOrder.Equal(decimal.NewFromInt(1)))
	require.True(t, byMat[bag].QtyPerItem.Equal(decimal.NewFromInt(1)))

	// full-replace: re-upsert with only the box → bag drops out.
	require.NoError(t, MS.UpsertPackagingBom(ctx, []entity.PackagingBomItem{{MaterialId: box, QtyPerOrder: decimal.NewFromInt(1), Active: true}}))
	one, err := MS.ListPackagingBom(ctx)
	require.NoError(t, err)
	require.Len(t, one, 1, "full-replace drops the omitted material")
	// restore the two-line recipe for the consume test.
	require.NoError(t, MS.UpsertPackagingBom(ctx, []entity.PackagingBomItem{
		{MaterialId: box, QtyPerOrder: decimal.NewFromInt(1), Active: true},
		{MaterialId: bag, QtyPerItem: decimal.NewFromInt(1), Active: true},
	}))

	// --- consume for a real order (3 units) ---
	orderA = seedOrder(ctx, t)
	mvs, err := MS.ConsumePackagingForOrder(ctx, orderA, 3, "tester")
	require.NoError(t, err)
	require.Len(t, mvs, 2, "box + bag written off")
	for _, m := range mvs {
		require.Equal(t, entity.MaterialMovementWriteoff, m.MovementType)
		require.Equal(t, entity.MaterialAdjustReasonPackaging, m.Reason.String)
	}
	// box: 1 per order → 100-1=99; bag: 1 per item × 3 → 100-3=97.
	boxSt, err := MS.GetMaterialStock(ctx, box)
	require.NoError(t, err)
	require.True(t, boxSt.OnHand.Equal(decimal.NewFromInt(99)), "box on-hand 99, got %s", boxSt.OnHand)
	bagSt, err := MS.GetMaterialStock(ctx, bag)
	require.NoError(t, err)
	require.True(t, bagSt.OnHand.Equal(decimal.NewFromInt(97)), "bag on-hand 97, got %s", bagSt.OnHand)

	// --- idempotent: a re-ship of the same order consumes nothing more ---
	again, err := MS.ConsumePackagingForOrder(ctx, orderA, 3, "tester")
	require.NoError(t, err)
	require.Empty(t, again, "re-ship is a no-op")
	boxSt, err = MS.GetMaterialStock(ctx, box)
	require.NoError(t, err)
	require.True(t, boxSt.OnHand.Equal(decimal.NewFromInt(99)), "no double consume")

	// --- best-effort: a material short on stock is skipped, the rest still consume ---
	require.NoError(t, MS.UpsertPackagingBom(ctx, []entity.PackagingBomItem{
		{MaterialId: box, QtyPerOrder: decimal.NewFromInt(1), Active: true},
		{MaterialId: shortMat, QtyPerItem: decimal.NewFromInt(5), Active: true}, // needs 10, only 1 in stock
	}))
	orderB = seedOrder(ctx, t)
	mvs, err = MS.ConsumePackagingForOrder(ctx, orderB, 2, "tester")
	require.NoError(t, err, "a short material must not fail the whole consume")
	require.Len(t, mvs, 1, "only the box succeeds; the short material is skipped")
	require.Equal(t, box, mvs[0].MaterialId)
	shortSt, err := MS.GetMaterialStock(ctx, shortMat)
	require.NoError(t, err)
	require.True(t, shortSt.OnHand.Equal(decimal.NewFromInt(1)), "short material untouched, got %s", shortSt.OnHand)

	// g25-02: the claim row records both what was booked and what was skipped (reconciliation trail).
	var booked, skipped int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT movement_count, skipped_count FROM order_packaging_consumed WHERE order_id = ?", orderB).
		Scan(&booked, &skipped))
	require.Equal(t, 1, booked, "one writeoff booked")
	require.Equal(t, 1, skipped, "the short material counted as skipped")
}

// seedOrder inserts a minimal customer_order (uuid + total_price + any valid status) and returns its
// id, so the order_packaging_consumed FK is satisfied without building a whole order.
func seedOrder(ctx context.Context, t *testing.T) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `
		INSERT INTO customer_order (uuid, total_price, order_status_id)
		SELECT UUID(), 100.00, id FROM order_status LIMIT 1`)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}
