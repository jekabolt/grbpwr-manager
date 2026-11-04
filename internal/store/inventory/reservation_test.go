package inventory

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
)

func d(i int64) decimal.Decimal { return decimal.NewFromInt(i) }

func matPick(id int) sql.NullInt32 { return sql.NullInt32{Int32: int32(id), Valid: true} }

func noAux(packagingItemRow) (int, bool) { return 0, false }

func perOrder() sql.NullBool { return sql.NullBool{Bool: true, Valid: true} }

func TestAggregatePackagingItems_PerGarmentScalesPerOrderOnce(t *testing.T) {
	// An item covering both colourways of the order: the box once, the bag on 2+3 pieces.
	rows := []packagingItemRow{
		{ItemId: 1, ProductId: 1, Pieces: d(2), ItemKey: "box", MaterialId: matPick(10), QtyPerGarment: 1, PerOrder: perOrder()},
		{ItemId: 2, ProductId: 1, Pieces: d(2), ItemKey: "dust_bag", MaterialId: matPick(20), QtyPerGarment: 1},
		{ItemId: 1, ProductId: 2, Pieces: d(3), ItemKey: "box", MaterialId: matPick(10), QtyPerGarment: 1, PerOrder: perOrder()},
		{ItemId: 2, ProductId: 2, Pieces: d(3), ItemKey: "dust_bag", MaterialId: matPick(20), QtyPerGarment: 1},
	}
	got, unresolved := aggregatePackagingItems(rows, noAux)
	assertReq(t, got, map[int]decimal.Decimal{10: d(1), 20: d(5)})
	if unresolved != 0 {
		t.Fatalf("unresolved %d", unresolved)
	}
}

func TestAggregatePackagingItems_TwoPerOrderItemsOfOneMaterialAdd(t *testing.T) {
	// Item keys are not unique, the id is what says «the same item».
	rows := []packagingItemRow{
		{ItemId: 1, ProductId: 1, Pieces: d(1), ItemKey: "other", MaterialId: matPick(10), QtyPerGarment: 1, PerOrder: perOrder()},
		{ItemId: 2, ProductId: 1, Pieces: d(1), ItemKey: "other", MaterialId: matPick(10), QtyPerGarment: 2, PerOrder: perOrder()},
	}
	got, _ := aggregatePackagingItems(rows, noAux)
	assertReq(t, got, map[int]decimal.Decimal{10: d(3)})
}

func TestAggregatePackagingItems_HangerOnlyOnHanger(t *testing.T) {
	row := packagingItemRow{ItemId: 1, ProductId: 1, Pieces: d(4), ItemKey: "hanger", MaterialId: matPick(10), QtyPerGarment: 1}
	got, _ := aggregatePackagingItems([]packagingItemRow{row}, noAux)
	if len(got) != 0 {
		t.Fatalf("a folded piece takes no hanger, got %v", got)
	}
	row.FoldingMethod = sql.NullString{String: "on a hanger", Valid: true}
	got, _ = aggregatePackagingItems([]packagingItemRow{row}, noAux)
	assertReq(t, got, map[int]decimal.Decimal{10: d(4)})
}

func TestAggregatePackagingItems_ShippingBoxGoesInShares(t *testing.T) {
	// The capacity makes a box a share of the pieces, per-order flag or not: 5 of 20 is a quarter.
	row := packagingItemRow{ItemId: 1, ProductId: 1, Pieces: d(5), ItemKey: "box", MaterialId: matPick(10), QtyPerGarment: 1,
		PerOrder: perOrder(), UnitsPerBox: sql.NullInt32{Int32: 20, Valid: true}}
	got, _ := aggregatePackagingItems([]packagingItemRow{row}, noAux)
	assertReq(t, got, map[int]decimal.Decimal{10: decimal.RequireFromString("0.25")})

	row.Pieces = d(20)
	got, _ = aggregatePackagingItems([]packagingItemRow{row}, noAux)
	assertReq(t, got, map[int]decimal.Decimal{10: d(1)})
}

func TestAggregatePackagingItems_AuxResolvesOrCounts(t *testing.T) {
	rows := []packagingItemRow{
		{ItemId: 1, ProductId: 1, ColorCode: "BLK", Pieces: d(2), ItemKey: "hangtag", AuxTechCardId: matPick(7), QtyPerGarment: 1},
		{ItemId: 2, ProductId: 1, ColorCode: "BLK", Pieces: d(2), ItemKey: "hangtag", AuxTechCardId: matPick(8), QtyPerGarment: 1},
	}
	got, unresolved := aggregatePackagingItems(rows, func(r packagingItemRow) (int, bool) {
		if r.AuxTechCardId.Int32 == 7 {
			return 70, true
		}
		return 0, false
	})
	assertReq(t, got, map[int]decimal.Decimal{70: d(2)})
	if unresolved != 1 {
		t.Fatalf("unresolved %d, want 1", unresolved)
	}
}

func assertReq(t *testing.T, got, want map[int]decimal.Decimal) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("materials: got %v want %v", got, want)
	}
	for m, w := range want {
		g, ok := got[m]
		if !ok {
			t.Fatalf("material %d missing in %v", m, got)
		}
		if !g.Equal(w) {
			t.Errorf("material %d: got %s want %s", m, g, w)
		}
	}
}
