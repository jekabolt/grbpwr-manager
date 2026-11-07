package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func pd(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func TestConvertPbMaterialConversion(t *testing.T) {
	got, err := ConvertPbMaterialConversion(&pb_admin.ConvertMaterialStockRequest{
		Inputs:   []*pb_common.MaterialConversionInput{{MaterialId: 1, Quantity: pd("10"), LossQty: pd("0.5"), LotId: 7}},
		Output:   &pb_common.MaterialConversionOutput{MaterialId: 2, Quantity: pd("200"), LotCode: "DYE-1", MeasuredWidthCm: pd("146"), ShadeCode: "BLK"},
		WorkCost: pd("50"), WorkCurrency: "eur", SupplierId: 3, ClientRequestId: "conv-1",
	})
	require.NoError(t, err)
	require.Len(t, got.Inputs, 1)
	assert.True(t, got.Inputs[0].Quantity.Equal(decimal.RequireFromString("10")))
	assert.True(t, got.Inputs[0].LossQty.Decimal.Equal(decimal.RequireFromString("0.5")))
	assert.Equal(t, "EUR", got.WorkCurrency)
	assert.Equal(t, 2, got.Output.MaterialId)
	assert.Equal(t, "DYE-1", got.Output.LotCode.String)
}

func TestConvertPbMaterialConversionGuards(t *testing.T) {
	base := func() *pb_admin.ConvertMaterialStockRequest {
		return &pb_admin.ConvertMaterialStockRequest{
			Inputs: []*pb_common.MaterialConversionInput{{MaterialId: 1, Quantity: pd("1")}},
			Output: &pb_common.MaterialConversionOutput{MaterialId: 2, Quantity: pd("1")},
		}
	}
	t.Run("same output", func(t *testing.T) {
		r := base()
		r.Output.MaterialId = 1
		_, err := ConvertPbMaterialConversion(r)
		assert.Error(t, err)
	})
	t.Run("loss over quantity", func(t *testing.T) {
		r := base()
		r.Inputs[0].LossQty = pd("2")
		_, err := ConvertPbMaterialConversion(r)
		assert.Error(t, err)
	})
	t.Run("work needs currency", func(t *testing.T) {
		r := base()
		r.WorkCost = pd("1")
		_, err := ConvertPbMaterialConversion(r)
		assert.Error(t, err)
	})
}

func TestMaterialMovementAndLotConversionFields(t *testing.T) {
	m := ConvertEntityMaterialMovementToPb(entity.MaterialMovement{
		MovementType: entity.MaterialMovementConvertOut,
		ConversionId: sqlNullInt32(9), LossQty: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.5"), Valid: true},
	})
	assert.Equal(t, pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_CONVERT_OUT, m.MovementType)
	assert.Equal(t, int32(9), m.ConversionId)
	assert.Equal(t, "0.5", m.LossQty.Value)

	l := MaterialLotToPb(entity.MaterialLot{
		ShrunkQty:     decimal.RequireFromString("0.5"),
		WashedWidthCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("146"), Valid: true},
		ShrinkagePct:  decimal.NullDecimal{Decimal: decimal.RequireFromString("5"), Valid: true},
	})
	assert.Equal(t, "0.5", l.ShrunkQty.Value)
	assert.Equal(t, "146", l.WashedWidthCm.Value)
	assert.Equal(t, "5", l.ShrinkagePct.Value)
}

func TestEveryMaterialMovementTypeHasWireMapping(t *testing.T) {
	assert.Len(t, materialMovementTypeToPb, len(entity.ValidMaterialMovementTypes))
	for movementType := range entity.ValidMaterialMovementTypes {
		wire, ok := materialMovementTypeToPb[movementType]
		require.True(t, ok, "missing protobuf mapping for %q", movementType)
		assert.Equal(t, movementType, MaterialMovementTypeFromPb(wire))
	}
}

func sqlNullInt32(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
