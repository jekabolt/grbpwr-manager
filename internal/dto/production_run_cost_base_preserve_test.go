package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestPreserveProductionRunCostBasesKeepsUnchangedArticles: an article whose kind, amount and
// currency survive an edit keeps the euro figure it was folded at when the money was spent.
func TestPreserveProductionRunCostBasesKeepsUnchangedArticles(t *testing.T) {
	stored := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, Amount: d("1000"), Currency: "USD", AmountBase: nd2("920")},
		{Kind: entity.ProductionRunCostCMT, Amount: d("500"), Currency: "EUR", AmountBase: nd2("500")},
	}
	incoming := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, Amount: d("1000.00"), Currency: "usd"}, // re-sent, base dropped
		{Kind: entity.ProductionRunCostCMT, Amount: d("500"), Currency: "EUR"},
	}
	PreserveProductionRunCostBases(incoming, stored)

	require.True(t, incoming[0].AmountBase.Valid)
	require.Equal(t, "920", incoming[0].AmountBase.Decimal.String(), "March's rate, not today's")
	require.Equal(t, "500", incoming[1].AmountBase.Decimal.String())

	// The fold then leaves them alone even though a very different rate is on file today.
	FoldProductionRunCostsToBase(incoming, CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"USD": d("0.5")}})
	require.Equal(t, "920", incoming[0].AmountBase.Decimal.String())
}

// TestPreserveProductionRunCostBasesRefoldsChangedArticles: a changed amount or currency is a
// different payment — it must be re-folded, and a manual override must not be touched.
func TestPreserveProductionRunCostBasesRefoldsChangedArticles(t *testing.T) {
	stored := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, Amount: d("1000"), Currency: "USD", AmountBase: nd2("920")},
		{Kind: entity.ProductionRunCostDuty, Amount: d("10"), Currency: "USD", AmountBase: nd2("9.2")},
		{Kind: entity.ProductionRunCostOther, Amount: d("7"), Currency: "USD"}, // never folded
	}
	incoming := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, Amount: d("1200"), Currency: "USD"},               // amount changed
		{Kind: entity.ProductionRunCostDuty, Amount: d("10"), Currency: "GBP"},                      // currency changed
		{Kind: entity.ProductionRunCostOther, Amount: d("7"), Currency: "USD"},                      // no stored base
		{Kind: entity.ProductionRunCostCMT, Amount: d("1"), Currency: "USD", AmountBase: nd2("99")}, // manual override
	}
	PreserveProductionRunCostBases(incoming, stored)

	require.False(t, incoming[0].AmountBase.Valid, "a different amount is a different payment")
	require.False(t, incoming[1].AmountBase.Valid, "a different currency is a different payment")
	require.False(t, incoming[2].AmountBase.Valid, "nothing to preserve")
	require.Equal(t, "99", incoming[3].AmountBase.Decimal.String(), "manual override untouched")
}

// TestPreserveProductionRunCostBasesMatchesAsMultiset: production_run_cost has no natural key, so
// two identical articles must consume two stored bases, not the same one twice.
func TestPreserveProductionRunCostBasesMatchesAsMultiset(t *testing.T) {
	stored := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostLogistics, Amount: d("100"), Currency: "USD", AmountBase: nd2("92")},
		{Kind: entity.ProductionRunCostLogistics, Amount: d("100"), Currency: "USD", AmountBase: nd2("91")},
	}
	incoming := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostLogistics, Amount: d("100"), Currency: "USD"},
		{Kind: entity.ProductionRunCostLogistics, Amount: d("100"), Currency: "USD"},
		{Kind: entity.ProductionRunCostLogistics, Amount: d("100"), Currency: "USD"}, // no third stored row
	}
	PreserveProductionRunCostBases(incoming, stored)

	require.Equal(t, "92", incoming[0].AmountBase.Decimal.String())
	require.Equal(t, "91", incoming[1].AmountBase.Decimal.String())
	require.False(t, incoming[2].AmountBase.Valid, "each stored base is handed out once")
}
