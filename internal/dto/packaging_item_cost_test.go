package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// packagingCostCard is a card of two colourways: 10 EUR of fabric on the first, and whatever packaging
// the test adds. Material 2 costs 3 EUR, material 3 has no price.
func packagingCostCard(items ...entity.TechCardPackagingItem) *entity.TechCard {
	price := &entity.MaterialPrice{MaterialId: 2, Price: decimal.NewFromInt(3), Currency: "EUR"}
	fabric := &entity.MaterialPrice{MaterialId: 1, Price: decimal.NewFromInt(10), Currency: "EUR"}
	pcs := sql.NullString{String: "pcs", Valid: true}
	one := decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true}
	return &entity.TechCard{
		TechCardInsert: entity.TechCardInsert{
			Costing: &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}},
			BomItems: []entity.TechCardBomItem{
				{Id: 1, Name: "Fabric", MaterialId: sql.NullInt64{Int64: 100, Valid: true}, Unit: pcs},
				{Id: 2, Name: "Old box", Section: entity.BomSectionPackaging, MaterialId: sql.NullInt64{Int64: 2, Valid: true}, Unit: pcs},
			},
			Colorways: []entity.TechCardColorway{
				{ProductId: sql.NullInt32{Int32: 7, Valid: true}, Usages: []entity.TechCardColorwayUsage{
					{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, MaterialId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: one},
				}},
				{ProductId: sql.NullInt32{Int32: 8, Valid: true}},
			},
			PackagingItems: items,
		},
		LinkedMaterials: map[int]entity.MaterialWithPrice{
			1: {Material: entity.Material{MaterialInsert: entity.MaterialInsert{Unit: pcs}}, LatestPrice: fabric},
			2: {Material: entity.Material{MaterialInsert: entity.MaterialInsert{Unit: pcs}}, LatestPrice: price},
			3: {Material: entity.Material{MaterialInsert: entity.MaterialInsert{Unit: pcs}}},
		},
	}
}

func packagingPick(key string, qty, material int, perOrder bool, colorways ...int) entity.TechCardPackagingItem {
	return entity.TechCardPackagingItem{Key: key, QtyPerGarment: qty, PerOrder: perOrder, ColorwayIds: colorways,
		MaterialId: sql.NullInt32{Int32: int32(material), Valid: true}}
}

func requirePackagingUnitCost(t *testing.T, tc *entity.TechCard, productID int, want string) {
	t.Helper()
	got, _ := ComputeColorwayUnitCost(tc, productID, CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"EUR": decimal.NewFromInt(1)}})
	require.True(t, got.Valid, "cost must exist")
	require.True(t, got.Decimal.Equal(decimal.RequireFromString(want)), "cost %s, want %s", got.Decimal, want)
}

func TestPackagingItemsInThePieceCost(t *testing.T) {
	// A per-piece item and, by the owner's rule, a per-order item count their quantity on every piece.
	requirePackagingUnitCost(t, packagingCostCard(packagingPick("dust_bag", 2, 2, false)), 7, "16")
	requirePackagingUnitCost(t, packagingCostCard(packagingPick("mailer", 1, 2, true)), 7, "13")

	// A shipping box with a capacity is a share of a box.
	box := packagingCostCard(packagingPick(entity.PackagingItemKeyBox, 1, 2, true))
	box.Packaging = &entity.TechCardPackaging{UnitsPerBox: sql.NullInt32{Int32: 20, Valid: true}}
	requirePackagingUnitCost(t, box, 7, "10.15")

	// An item limited to the other colour costs this one nothing.
	requirePackagingUnitCost(t, packagingCostCard(packagingPick("insert_card", 1, 2, false, 8)), 7, "10")

	// A hanger only on a piece shipped on one.
	hanger := packagingCostCard(packagingPick(entity.PackagingItemKeyHanger, 1, 2, false))
	requirePackagingUnitCost(t, hanger, 7, "10")
	hanger.Packaging = &entity.TechCardPackaging{FoldingMethod: sql.NullString{String: entity.PackagingFoldingOnHanger, Valid: true}}
	requirePackagingUnitCost(t, hanger, 7, "13")
}

// An old packaging usage next to an item does not pay twice, and the colour holding only that usage
// keeps inheriting the style's figure.
func TestPackagingOldUsageIsNotPaidNorARecipe(t *testing.T) {
	tc := packagingCostCard(packagingPick("mailer", 1, 2, true))
	old := entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: 2, Valid: true}, MaterialId: sql.NullInt64{Int64: 2, Valid: true},
		Consumption: decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true},
	}
	tc.Colorways[0].Usages = append(tc.Colorways[0].Usages, old)
	requirePackagingUnitCost(t, tc, 7, "13")

	tc.Colorways[1].Usages = []entity.TechCardColorwayUsage{old}
	require.False(t, colorwayHasOwnRecipe(tc, &tc.Colorways[1]))
	requirePackagingUnitCost(t, tc, 8, "13")
}

// A colour with no recipe takes the primary's materials and keeps its own packaging: in the live price, in
// the breakdown and in the frozen release price, one figure.
func TestPackagingIsNotInherited(t *testing.T) {
	fx := CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"EUR": decimal.NewFromInt(1)}}
	requireAll := func(tc *entity.TechCard, productID int, want string) {
		t.Helper()
		requirePackagingUnitCost(t, tc, productID, want)
		bd, ok := ComputeColorwayCostBreakdownBase(tc, productID, fx)
		require.True(t, ok)
		require.True(t, bd.Materials.Equal(decimal.RequireFromString(want)), "breakdown materials %s, want %s", bd.Materials, want)
	}
	// Its own insert on top of the inherited fabric, the primary pays for neither.
	own := packagingCostCard(packagingPick("insert_card", 1, 2, false, 8))
	requireAll(own, 8, "13")
	requireAll(own, 7, "10")

	// The primary's own insert stays the primary's.
	primary := packagingCostCard(packagingPick("insert_card", 1, 2, false, 7))
	requireAll(primary, 7, "13")
	requireAll(primary, 8, "10")

	// An item with no price on this colour refuses this colour alone.
	unpriced := packagingCostCard(packagingPick("insert_card", 1, 3, false, 8))
	got, _ := ComputeColorwayUnitCost(unpriced, 8, fx)
	require.False(t, got.Valid)
	requireAll(unpriced, 7, "10")
}

func TestPackagingIsNotInheritedInTheFrozenPrice(t *testing.T) {
	frozen := func(items ...entity.TechCardPackagingItem) *ReleaseFrozenCosts {
		t.Helper()
		tc := packagingCostCard(items...)
		tc.Colorways[0].Id, tc.Colorways[1].Id = 7, 8
		// A colour priced at zero on its own recipe is left out, so both carry a manual article.
		tc.Costing.CmtCost = decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true}
		costs := ReleaseFrozenColorwayCosts(releaseBlob(t, tc))
		require.NotNil(t, costs)
		return costs
	}
	requireUnit := func(costs *ReleaseFrozenCosts, id int, want string) {
		t.Helper()
		require.True(t, costs.UnitCostByColorway[id].Equal(decimal.RequireFromString(want)), "colour %d: %s, want %s", id, costs.UnitCostByColorway[id], want)
	}
	own := frozen(packagingPick("insert_card", 1, 2, false, 8))
	requireUnit(own, 7, "11")
	requireUnit(own, 8, "14")
	primary := frozen(packagingPick("insert_card", 1, 2, false, 7))
	requireUnit(primary, 7, "14")
	requireUnit(primary, 8, "11")

	// A release frozen before the figure existed had no packaging in its cost: the style's figure it is.
	snap := releaseBlob(t, func() *entity.TechCard {
		tc := packagingCostCard()
		tc.Colorways[0].Id, tc.Colorways[1].Id = 7, 8
		tc.Costing.CmtCost = decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true}
		return tc
	}())
	for _, cc := range snap.GetTechCard().GetCosting().GetColorwayCosts() {
		cc.InheritedUnitCost = nil
	}
	requireUnit(ReleaseFrozenColorwayCosts(snap), 8, "11")
}

// An item with no price refuses the seed instead of leaving the figure quietly short.
func TestPackagingUnpricedItemRefusesTheSeed(t *testing.T) {
	tc := packagingCostCard(packagingPick("dust_bag", 1, 3, false))
	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, tc.LinkedMaterials, "EUR", tc.CostingBasis(), CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"EUR": decimal.NewFromInt(1)}})
	require.True(t, cc.hasUnpriced)
}

// The run plan never issues packaging: it leaves at shipment, not at the cutting table.
func TestPackagingStaysOutOfTheRunPlan(t *testing.T) {
	tc := packagingCostCard(packagingPick("mailer", 1, 2, true))
	tc.Colorways[0].Usages = append(tc.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: 2, Valid: true},
		Quantity:  decimal.NullDecimal{Decimal: decimal.NewFromInt(1), Valid: true},
	})
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{Lines: []entity.ProductionRunLine{
		{ProductId: sql.NullInt32{Int32: 7, Valid: true}, SizeId: 1, PlannedQty: 10},
	}}}
	resp := ComputeProductionRunMaterialPlan(run, tc, nil, nil, nil, nil)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, int32(1), resp.Rows[0].MaterialId)
}

// A frozen card with picks keeps the figure frozen at release: a recount from the blob has no prices for
// the picks and would come out short by the packaging.
func TestPackagingPickKeepsTheReleaseFigureFrozen(t *testing.T) {
	require.NotNil(t, ReleaseFrozenColorwayCosts(releaseBlob(t, releaseMixCard())), "the plain card still costs")

	card := releaseMixCard()
	card.PackagingItems = []entity.TechCardPackagingItem{packagingPick("mailer", 1, 2, true)}
	if card.LinkedMaterials == nil {
		card.LinkedMaterials = map[int]entity.MaterialWithPrice{}
	}
	card.LinkedMaterials[2] = packagingCostCard().LinkedMaterials[2]
	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, card))
	require.NotNil(t, costs)
	require.Nil(t, costs.CostingCard)
}
