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

// TestMaterialLotMeasuredWidthAndShade covers Ф5а.1 (migration 0269): a receipt records the width
// that ARRIVED and the dye lot on the lot it opens, and the top-up rule that makes those fields
// safe — a later receipt into the same lot that omits them must NOT erase what the first one
// measured, while one that carries a new measurement corrects it (re-measuring a roll is a
// correction, not a second roll).
func TestMaterialLotMeasuredWidthAndShade(t *testing.T) {
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
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	MS := s.MaterialStock()

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "F5A Lot Fabric", Section: "fabric", Unit: ns("m"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_lot WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	lotOf := func(code string) entity.MaterialLot {
		t.Helper()
		lots, err := MS.ListMaterialLots(ctx, matID, false)
		require.NoError(t, err)
		for _, l := range lots {
			if l.LotCode == code {
				return l
			}
		}
		t.Fatalf("lot %q not found", code)
		return entity.MaterialLot{}
	}

	// Supplier prints 150; the roll measures 148 — that gap is the whole point of the field.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(60), Lot: ns("ROLL-1"),
		MeasuredWidthCm: nd("148"), ShadeCode: ns("SH-7"),
	})
	require.NoError(t, err)

	l := lotOf("ROLL-1")
	require.True(t, l.MeasuredWidthCm.Valid && l.MeasuredWidthCm.Decimal.Equal(decimal.RequireFromString("148")),
		"the measured width is stored, got %v", l.MeasuredWidthCm)
	require.Equal(t, "SH-7", l.ShadeCode.String)

	// A top-up that says nothing about the roll must not blank the measurement.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(40), Lot: ns("ROLL-1"),
	})
	require.NoError(t, err)
	l = lotOf("ROLL-1")
	require.True(t, l.MeasuredWidthCm.Valid && l.MeasuredWidthCm.Decimal.Equal(decimal.RequireFromString("148")),
		"an omitted measurement must not erase the recorded one, got %v", l.MeasuredWidthCm)
	require.Equal(t, "SH-7", l.ShadeCode.String)
	require.True(t, l.ReceivedQty.Equal(decimal.NewFromInt(100)), "quantities still accumulate normally")

	// A re-measure corrects it.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(10), Lot: ns("ROLL-1"),
		MeasuredWidthCm: nd("147.5"),
	})
	require.NoError(t, err)
	l = lotOf("ROLL-1")
	require.True(t, l.MeasuredWidthCm.Decimal.Equal(decimal.RequireFromString("147.5")),
		"a new measurement corrects the old one, got %v", l.MeasuredWidthCm)

	// A lot nobody measured stays NULL — "unmeasured" must not read as "agrees with the nominal".
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(20), Lot: ns("ROLL-2"),
	})
	require.NoError(t, err)
	l = lotOf("ROLL-2")
	require.False(t, l.MeasuredWidthCm.Valid, "unmeasured stays NULL, got %v", l.MeasuredWidthCm)
	require.False(t, l.ShadeCode.Valid, "unrecorded shade stays NULL, got %v", l.ShadeCode)
}
