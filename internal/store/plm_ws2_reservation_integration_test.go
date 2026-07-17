package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestPackagingReservationLedger exercises the packaging reservation ledger (PLM rework §2.8, S22):
// the soft available = on_hand − Σ open, the reservation-aware manual-adjust guard, release closing a
// claim, and consume fulfilling a claim (drift-proof tier-1 path) idempotently. Reserve's resolution
// from order lines is covered by the pure aggregatePackaging unit test; here we insert claims directly
// to exercise the ledger mechanics without a full product/variant fixture.
func TestPackagingReservationLedger(t *testing.T) {
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

	MS := s.MaterialStock()

	mk := func(name string) int {
		id, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: name, Section: "packaging", Unit: sql.NullString{String: "pc", Valid: true}})
		require.NoError(t, err)
		return id
	}
	matA, matB, matC := mk("WS2 Res A"), mk("WS2 Res B"), mk("WS2 Res C")

	orders := []int{}
	t.Cleanup(func() {
		cctx := context.Background()
		for _, oid := range orders {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM order_packaging_consumed WHERE order_id = ?", oid)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE id = ?", oid) // cascades material_reservation_ledger
		}
		for _, id := range []int{matA, matB, matC} {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
		}
	})

	recv := func(mat int, qty int64) {
		_, err := MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
			MaterialId: mat, Quantity: decimal.NewFromInt(qty),
			UnitCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true}, Currency: "EUR",
		})
		require.NoError(t, err)
	}
	// reserveClaim inserts an open 'reserve' row keyed like ReservePackagingForOrder would.
	reserveClaim := func(mat, order int, qty int64) {
		_, err := testDB.ExecContext(ctx, `
			INSERT INTO material_reservation_ledger (material_id, order_id, qty, event, claim_key, created_by)
			VALUES (?, ?, ?, 'reserve', ?, 'tester')`,
			mat, order, qty, fmt.Sprintf("%d:%d", order, mat))
		require.NoError(t, err)
	}
	newOrder := func() int {
		o := seedOrder(ctx, t)
		orders = append(orders, o)
		return o
	}
	eq := func(d decimal.Decimal, v int64) bool { return d.Equal(decimal.NewFromInt(v)) }

	// --- available + reservation-aware manual adjust guard (matA) ---
	recv(matA, 10)
	o1 := newOrder()
	reserveClaim(matA, o1, 4)

	av, err := MS.MaterialAvailable(ctx, matA)
	require.NoError(t, err)
	require.True(t, eq(av.OnHand, 10) && eq(av.Reserved, 4) && eq(av.Available, 6), "10 − 4 = 6, got %+v", av)

	// A write-off that would drop on_hand below the 4 reserved is refused (would deepen an oversell).
	_, err = MS.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{
		MaterialId: matA, Mode: entity.MaterialAdjustModeWriteoff, Quantity: decimal.NewFromInt(7),
		Reason: entity.MaterialAdjustReasonDamage, AdminUsername: "tester",
	})
	require.ErrorIs(t, err, entity.ErrMaterialReserved, "writeoff 7 (→3 < 4 reserved) must be refused")
	// A write-off down to exactly the reserved quantity is allowed.
	_, err = MS.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{
		MaterialId: matA, Mode: entity.MaterialAdjustModeWriteoff, Quantity: decimal.NewFromInt(6),
		Reason: entity.MaterialAdjustReasonDamage, AdminUsername: "tester",
	})
	require.NoError(t, err, "writeoff 6 (→4 == 4 reserved) is allowed")

	// --- release returns the soft hold without any physical writeoff (matB) ---
	recv(matB, 10)
	o2 := newOrder()
	reserveClaim(matB, o2, 3)
	require.NoError(t, MS.ReleasePackagingForOrder(ctx, o2, "tester"))
	av, err = MS.MaterialAvailable(ctx, matB)
	require.NoError(t, err)
	require.True(t, eq(av.OnHand, 10) && eq(av.Reserved, 0), "release frees the hold, on_hand untouched, got %+v", av)
	// idempotent: a second release is a no-op.
	require.NoError(t, MS.ReleasePackagingForOrder(ctx, o2, "tester"))

	// --- consume fulfils an open claim (drift-proof tier-1) and is idempotent (matC) ---
	recv(matC, 10)
	o3 := newOrder()
	reserveClaim(matC, o3, 5)
	mvs, err := MS.ConsumePackagingForOrder(ctx, o3, 0, "tester") // itemCount ignored — claim drives it
	require.NoError(t, err)
	require.Len(t, mvs, 1)
	require.Equal(t, matC, mvs[0].MaterialId)
	require.True(t, eq(mvs[0].Quantity, 5), "consumes the reserved 5")
	st, err := MS.GetMaterialStock(ctx, matC)
	require.NoError(t, err)
	require.True(t, eq(st.OnHand, 5), "10 − 5 consumed = 5, got %s", st.OnHand)
	av, err = MS.MaterialAvailable(ctx, matC)
	require.NoError(t, err)
	require.True(t, eq(av.Reserved, 0), "claim closed by consume, got reserved %s", av.Reserved)
	again, err := MS.ConsumePackagingForOrder(ctx, o3, 0, "tester")
	require.NoError(t, err)
	require.Empty(t, again, "re-ship consumes nothing more")
}
