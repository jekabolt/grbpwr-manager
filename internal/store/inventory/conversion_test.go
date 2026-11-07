package inventory

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cd(v string) decimal.Decimal { return decimal.RequireFromString(v) }

func TestComputeConversionCost(t *testing.T) {
	t.Run("E1", func(t *testing.T) {
		got, err := computeConversionCost([]conversionCostInput{{Quantity: cd("10"), Avg: cd("4.2000")}}, cd("50"), cd("200"))
		require.NoError(t, err)
		assert.True(t, got.InputsCostBase.Equal(cd("42")))
		assert.True(t, got.InputsValueBase.Equal(cd("42")))
		assert.True(t, got.TotalCostBase.Equal(cd("92")))
		assert.True(t, got.OutputUnitCostBase.Equal(cd("0.4600")))
	})

	t.Run("E1b blend", func(t *testing.T) {
		cost, err := computeConversionCost([]conversionCostInput{{Quantity: cd("10"), Avg: cd("4.2000")}}, cd("50"), cd("200"))
		require.NoError(t, err)
		avg := blendIntoAvg(stockState{OnHand: cd("100"), Avg: decimal.NullDecimal{Decimal: cd("0.50"), Valid: true}},
			cd("200"), decimal.NullDecimal{Decimal: cost.OutputUnitCostBase, Valid: true})
		require.True(t, avg.Valid)
		assert.True(t, avg.Decimal.Equal(cd("0.4733")), avg.Decimal.String())
	})

	t.Run("residual", func(t *testing.T) {
		got, err := computeConversionCost([]conversionCostInput{{Quantity: cd("7"), Avg: cd("4.2345")}}, cd("19.99"), cd("333"))
		require.NoError(t, err)
		assert.True(t, got.InputsCostBase.Equal(cd("29.6415")))
		assert.True(t, got.InputsValueBase.Equal(cd("29.64")))
		assert.True(t, got.TotalCostBase.Equal(cd("49.6315")))
		assert.True(t, got.OutputUnitCostBase.Equal(cd("0.1490")))
		assert.True(t, cd("333").Mul(got.OutputUnitCostBase).Round(2).Sub(got.InputsValueBase).Sub(cd("19.99")).Equal(cd("-0.01")))
	})

	t.Run("precision guard", func(t *testing.T) {
		_, err := computeConversionCost([]conversionCostInput{{Quantity: cd("10"), Avg: cd("1")}}, decimal.Zero, cd("30000"))
		assert.ErrorIs(t, err, entity.ErrMaterialConversionPrecision)
	})

	t.Run("multi input", func(t *testing.T) {
		got, err := computeConversionCost([]conversionCostInput{
			{Quantity: cd("2"), Avg: cd("4.20")},
			{Quantity: cd("50"), Avg: cd("0.10")},
		}, decimal.Zero, cd("100"))
		require.NoError(t, err)
		assert.True(t, got.InputsValueBase.Equal(cd("13.40")))
		assert.True(t, got.TotalCostBase.Equal(cd("13.40")))
		assert.True(t, got.OutputUnitCostBase.Equal(cd("0.1340")))
	})
}

func TestCompoundedShrinkage(t *testing.T) {
	rows := []shrinkageRatio{
		{LotId: 1, LengthBefore: cd("10"), LengthAfter: cd("9.5")},
		{LotId: 2, LengthBefore: cd("20"), LengthAfter: cd("19")},
		{LotId: 3, LengthBefore: cd("30"), LengthAfter: cd("29.1")},
	}
	lot, material := compoundedShrinkage(rows, 1)
	assert.True(t, lot.Equal(cd("5.00")))
	assert.True(t, material.Equal(cd("4.00")))
	rows = append(rows, shrinkageRatio{LotId: 1, LengthBefore: cd("9.5"), LengthAfter: cd("9.31")})
	lot, material = compoundedShrinkage(rows, 1)
	assert.True(t, lot.Equal(cd("6.90")))
	assert.True(t, material.Equal(cd("4.32")))
}
