package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func gateND(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

// gateCard builds an EUR-costing card whose recipe is one fabric line (2 units, price/currency under
// the test's control) plus one EUR trim (1 × 1) and EUR CMT 10. Every truncation of the fabric still
// leaves a POSITIVE figure in the base currency — exactly the shape that used to sail through the
// seed's base-currency guard into product.cost_price.
func gateCard(fabricPrice decimal.NullDecimal, fabricCcy string) *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		SizeQuantities: []entity.TechCardSizeQuantity{{SizeId: 4, OrderQty: 100}},
		BomItems: []entity.TechCardBomItem{
			{
				Id: 1, Section: entity.BomSectionFabric, Name: "shell",
				UnitPrice: fabricPrice, Currency: sql.NullString{String: fabricCcy, Valid: fabricCcy != ""},
			},
			{
				Id: 2, Section: entity.BomSectionTrim, Name: "tape",
				UnitPrice: gateND("1"), Currency: sql.NullString{String: "EUR", Valid: true},
			},
		},
		Colorways: []entity.TechCardColorway{{
			Name: "Black", ProductId: sql.NullInt32{Int32: 7, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: gateND("2")},
				{BomItemId: sql.NullInt64{Int64: 2, Valid: true}, Quantity: gateND("1")},
			},
		}},
		Costing: &entity.TechCardCosting{
			CmtCost:  gateND("10"),
			Currency: sql.NullString{String: "EUR", Valid: true},
		},
	}}
}

// TestUnitCostGateHappyPathUnchanged pins the figure a complete EUR recipe must keep producing:
// fabric 2 × 3 + trim 1 × 1 + cmt 10 = 17.
func TestUnitCostGateHappyPathUnchanged(t *testing.T) {
	card := gateCard(gateND("3"), "EUR")
	fx := CostingFx{Base: "EUR"}

	unit, ccy := ComputeColorwayUnitCost(card, 7, fx)
	require.True(t, unit.Valid, "a fully priced base-currency recipe must still seed")
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "17", unit.Decimal.String())

	unit, ccy = ComputeTechCardUnitCost(card, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "17", unit.Decimal.String())

	bd, ok := ComputeColorwayCostBreakdownBase(card, 7, fx)
	require.True(t, ok)
	require.Equal(t, "7", bd.Materials.String())
}

// TestUnitCostGateRejectsForeignCurrencyLine is the headline defect: a fabric quoted in a currency
// with no FX rate is excluded from the costing-currency bucket, and the fallback used to return
// trims+CMT — labelled EUR, because the costing currency IS the base currency. The seed's
// "base currency only" guard could not tell that apart from a complete figure.
func TestUnitCostGateRejectsForeignCurrencyLine(t *testing.T) {
	card := gateCard(gateND("3"), "BDT") // no BDT rate on file
	fx := CostingFx{Base: "EUR"}

	unit, ccy := ComputeColorwayUnitCost(card, 7, fx)
	require.False(t, unit.Valid, "a recipe missing its fabric must not produce a cost (was 11 = trim+cmt)")
	require.Equal(t, "", ccy)

	unit, _ = ComputeTechCardUnitCost(card, fx)
	require.False(t, unit.Valid)

	_, ok := ComputeColorwayCostBreakdownBase(card, 7, fx)
	require.False(t, ok, "no cost_price ⇒ no cost_breakdown")

	// With a rate on file the same card is complete again, in base currency:
	// fabric 2 × 3 × 0.01 = 0.06, + trim 1 + cmt 10 = 11.06.
	rated := CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"BDT": decimal.RequireFromString("0.01")}}
	unit, ccy = ComputeColorwayUnitCost(card, 7, rated)
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "11.06", unit.Decimal.String())
}

// TestUnitCostGateRejectsUnpricedLine covers the currency-blind half of the same hole: a line with
// no resolvable price contributes to NO bucket, so no currency flag catches it and both the base
// rollup and the fallback silently omit that material.
func TestUnitCostGateRejectsUnpricedLine(t *testing.T) {
	card := gateCard(decimal.NullDecimal{}, "EUR") // fabric has no price yet
	fx := CostingFx{Base: "EUR"}

	unit, ccy := ComputeColorwayUnitCost(card, 7, fx)
	require.False(t, unit.Valid, "an unpriced fabric must not seed trim+cmt as the garment cost")
	require.Equal(t, "", ccy)

	unit, _ = ComputeTechCardUnitCost(card, fx)
	require.False(t, unit.Valid)

	_, ok := ComputeColorwayCostBreakdownBase(card, 7, fx)
	require.False(t, ok)
}

// TestUnitCostGateRejectsUnresolvablePin covers the pin variant: pinShadowBom deliberately strips the
// price when the pinned article is unknown (or its unit disagrees with the slot's), which lands in the
// same "line contributes nothing" state.
func TestUnitCostGateRejectsUnresolvablePin(t *testing.T) {
	card := gateCard(gateND("3"), "EUR")
	card.Colorways[0].Usages[0].MaterialId = sql.NullInt64{Int64: 99, Valid: true} // not in LinkedMaterials

	unit, _ := ComputeColorwayUnitCost(card, 7, CostingFx{Base: "EUR"})
	require.False(t, unit.Valid, "an unresolvable pin must not fall back to the slot default's price")
}

// TestUnitCostFlagsTravelOnTheWire pins the read side of the same gate: the admin cannot explain WHY a
// cost stopped seeding unless the reason reaches it, so has_unpriced must be set on BOTH the root
// rollup (primary colourway) and the colourway's own entry — and stay false for a complete recipe.
func TestUnitCostFlagsTravelOnTheWire(t *testing.T) {
	fx := CostingFx{Base: "EUR"}

	complete := techCardCostingToPb(gateCard(gateND("3"), "EUR"), fx)
	require.NotNil(t, complete)
	require.False(t, complete.HasUnpriced, "a fully priced recipe must not raise the blocking flag")
	require.False(t, complete.HasUnconvertedCurrencies)
	require.Len(t, complete.ColorwayCosts, 1)
	require.False(t, complete.ColorwayCosts[0].HasUnpriced)

	// An unpriced fabric contributes to no bucket: the figures still render (11 = trim+cmt), which is
	// exactly why the flag has to travel — the number looks plausible and is silently incomplete.
	unpriced := techCardCostingToPb(gateCard(decimal.NullDecimal{}, "EUR"), fx)
	require.NotNil(t, unpriced)
	require.True(t, unpriced.HasUnpriced, "an uncostable line must be visible on the root rollup")
	require.False(t, unpriced.HasUnconvertedCurrencies, "no currency flag catches an unpriced line")
	require.Len(t, unpriced.ColorwayCosts, 1)
	require.True(t, unpriced.ColorwayCosts[0].HasUnpriced)
	require.Equal(t, "11", unpriced.UnitCost.Value)

	// An unresolvable pin lands in the same state, via pinShadowBom stripping the price.
	pinned := gateCard(gateND("3"), "EUR")
	pinned.Colorways[0].Usages[0].MaterialId = sql.NullInt64{Int64: 99, Valid: true}
	pinnedPb := techCardCostingToPb(pinned, fx)
	require.True(t, pinnedPb.HasUnpriced, "an unresolvable pin must raise the flag too")
	require.True(t, pinnedPb.ColorwayCosts[0].HasUnpriced)
}

// TestUnitCostGateKeepsMissingColorwayDistinguishable proves the seed can still tell "this product is
// not one of the card's colourways" (fall back to the style figure) from "this colourway cannot be
// costed" (seed nothing) — both return an invalid cost.
func TestUnitCostGateKeepsMissingColorwayDistinguishable(t *testing.T) {
	card := gateCard(gateND("3"), "BDT")
	require.True(t, HasColorwayForProduct(card, 7), "colourway 7 exists but cannot be costed")
	require.False(t, HasColorwayForProduct(card, 8), "no colourway is bound to product 8")
	require.False(t, HasColorwayForProduct(nil, 7))
}
