package dto

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// A card whose costing row carries no currency (and therefore no manual articles: a money article
// without a currency is refused on write) still has a fully priced EUR recipe. The cost estimate
// shows its base figure, so the costing tab must show the same figure, not «not computed».
//
// Before the fix the rollup bucketed by the costing currency "", every EUR line landed in a foreign
// bucket, materials_per_unit / unit_cost came back 0 and has_unconverted_currencies true, while
// unit_cost_base (the estimate's number) was the real 7.
func TestCostingWithoutCurrencyRollsUpInBaseCurrency(t *testing.T) {
	card := gateCard(gateND("3"), "EUR") // fabric 2 × 3 + trim 1 × 1 = 7
	card.Costing.Currency = sql.NullString{}
	card.Costing.CmtCost = decimal.NullDecimal{}
	fx := CostingFx{Base: "EUR"}

	pb := techCardCostingToPb(card, fx)
	require.NotNil(t, pb)
	require.Equal(t, "", pb.GetCurrency(), "the saved currency stays empty: nothing is written for the user")
	require.Equal(t, "EUR", pb.GetBaseCurrency())
	require.Equal(t, "7", pb.GetUnitCostBase().GetValue())
	require.Equal(t, "7", pb.GetMaterialsPerUnit().GetValue(), "materials must be the base-currency figure, not 0")
	require.Equal(t, "7", pb.GetUnitCost().GetValue())
	require.False(t, pb.GetHasUnconvertedCurrencies(), "EUR lines are not foreign when the card has no currency")
	require.Len(t, pb.GetColorwayCosts(), 1)
	require.Equal(t, "7", pb.GetColorwayCosts()[0].GetUnitCost().GetValue())

	// The seeds read the same rollup and must not move: they already took the base figure.
	unit, ccy := ComputeTechCardUnitCost(card, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "7", unit.Decimal.String())
	unit, ccy = ComputeColorwayUnitCost(card, 7, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "7", unit.Decimal.String())
}

// A line in a currency with no rate stays foreign: the card without a currency is costed in the base
// currency, so only base-currency (and currency-less) lines count, and the gap is still flagged.
func TestCostingWithoutCurrencyStillFlagsUnratedLine(t *testing.T) {
	card := gateCard(gateND("3"), "BDT") // no BDT rate on file
	card.Costing.Currency = sql.NullString{}
	card.Costing.CmtCost = decimal.NullDecimal{}
	fx := CostingFx{Base: "EUR"}

	pb := techCardCostingToPb(card, fx)
	require.True(t, pb.GetHasUnconvertedCurrencies())
	require.Equal(t, "1", pb.GetMaterialsPerUnit().GetValue(), "only the EUR trim is in the base bucket")
	require.Nil(t, pb.GetUnitCostBase())
	require.Equal(t, "", pb.GetBaseCurrency())

	unit, _ := ComputeTechCardUnitCost(card, fx)
	require.False(t, unit.Valid, "an incomplete figure still never seeds")
}

// With no base currency configured there is nothing to fall back to: the old behaviour stands.
func TestCostingWithoutCurrencyAndWithoutBaseUnchanged(t *testing.T) {
	card := gateCard(gateND("3"), "EUR")
	card.Costing.Currency = sql.NullString{}
	card.Costing.CmtCost = decimal.NullDecimal{}

	pb := techCardCostingToPb(card, CostingFx{})
	require.Equal(t, "0", pb.GetMaterialsPerUnit().GetValue())
	require.True(t, pb.GetHasUnconvertedCurrencies())
	require.Equal(t, "", pb.GetBaseCurrency())
}

// A card WITH a currency is untouched: an EUR card next to a PLN-rated base keeps costing in EUR.
func TestCostingWithCurrencyUnchanged(t *testing.T) {
	card := gateCard(gateND("3"), "EUR")
	card.Costing.Currency = sql.NullString{String: "PLN", Valid: true}
	card.Costing.CmtCost = decimal.NullDecimal{}
	fx := CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"PLN": decimal.RequireFromString("0.25")}}

	pb := techCardCostingToPb(card, fx)
	require.Equal(t, "PLN", pb.GetCurrency())
	require.Equal(t, "0", pb.GetMaterialsPerUnit().GetValue(), "EUR lines stay foreign on a PLN card")
	require.True(t, pb.GetHasUnconvertedCurrencies())
	require.Equal(t, "7", pb.GetUnitCostBase().GetValue())
}
