package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestConvertPbIssueMaterialStockLot covers the optional structured-lot draw on an issue (gap-07 v2 D):
// a positive lot_id is carried onto the insert for either target; a zero one is left unset.
func TestConvertPbIssueMaterialStockLot(t *testing.T) {
	// run target with a lot.
	ins, err := ConvertPbIssueMaterialStock(&pb_admin.IssueMaterialStockRequest{
		MaterialId: 3, Quantity: dec("5"), ProductionRunId: 7, LotId: 42,
	})
	require.NoError(t, err)
	require.True(t, ins.LotId.Valid)
	require.Equal(t, int32(42), ins.LotId.Int32)

	// sample target with a lot (lots are valid for either target).
	ins, err = ConvertPbIssueMaterialStock(&pb_admin.IssueMaterialStockRequest{
		MaterialId: 3, Quantity: dec("5"), SampleId: 9, LotId: 42,
	})
	require.NoError(t, err)
	require.True(t, ins.LotId.Valid)

	// no lot_id → unset.
	ins, err = ConvertPbIssueMaterialStock(&pb_admin.IssueMaterialStockRequest{
		MaterialId: 3, Quantity: dec("5"), ProductionRunId: 7,
	})
	require.NoError(t, err)
	require.False(t, ins.LotId.Valid)
}

// TestMaterialLotToPb round-trips every field of a stored lot, including the null-guarded ones.
func TestMaterialLotToPb(t *testing.T) {
	when := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	l := entity.MaterialLot{
		Id: 5, MaterialId: 8, LotCode: "DYE-A",
		SupplierDoc:  sql.NullString{String: "INV-1", Valid: true},
		ReceivedQty:  decimal.NewFromInt(100),
		RemainingQty: decimal.NewFromInt(75),
		UnitCost:     decimal.NullDecimal{Decimal: decimal.RequireFromString("5.5"), Valid: true},
		Currency:     sql.NullString{String: "EUR", Valid: true},
		ReceivedAt:   sql.NullTime{Time: when, Valid: true},
		Note:         sql.NullString{String: "roll 3", Valid: true},
		Archived:     true,
	}
	pb := MaterialLotToPb(l)
	require.Equal(t, int32(5), pb.Id)
	require.Equal(t, int32(8), pb.MaterialId)
	require.Equal(t, "DYE-A", pb.LotCode)
	require.Equal(t, "INV-1", pb.SupplierDoc)
	require.Equal(t, "100", pb.ReceivedQty.GetValue())
	require.Equal(t, "75", pb.RemainingQty.GetValue())
	require.Equal(t, "5.5", pb.UnitCost.GetValue())
	require.Equal(t, "EUR", pb.Currency)
	require.Equal(t, "roll 3", pb.Note)
	require.True(t, pb.Archived)
	require.NotNil(t, pb.ReceivedAt)

	// null-safe: an empty lot carries no cost / timestamp.
	pb = MaterialLotToPb(entity.MaterialLot{Id: 1, MaterialId: 2, LotCode: "X", ReceivedQty: decimal.Zero, RemainingQty: decimal.Zero})
	require.Nil(t, pb.UnitCost)
	require.Nil(t, pb.ReceivedAt)
	require.Empty(t, pb.SupplierDoc)

	require.Len(t, MaterialLotListToPb([]entity.MaterialLot{l, l}), 2)
}
