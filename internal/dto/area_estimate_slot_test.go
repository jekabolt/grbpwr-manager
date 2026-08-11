package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// estimateCard builds the exact situation the owner hit on card 38: a priced roll slot, cut pieces
// assigned to it in the colourway, measured areas — and NOT ONE authored norm.
func estimateCard() *entity.TechCard {
	tc := &entity.TechCard{}
	tc.Id = 38
	tc.SizeIds = []int{4}
	tc.Costing = &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}}
	tc.BomItems = []entity.TechCardBomItem{{
		Id:          56,
		LineKey:     "BOMKEY1",
		Section:     entity.BomSectionFabric,
		Unit:        sql.NullString{String: "m", Valid: true},
		UnitPrice:   decimal.NullDecimal{Decimal: decimal.RequireFromString("100"), Valid: true},
		Currency:    sql.NullString{String: "EUR", Valid: true},
		FabricWidth: decimal.NullDecimal{Decimal: decimal.RequireFromString("140"), Valid: true},
	}}
	tc.Pieces = []entity.TechCardPiece{{Id: 14, LineKey: "PIECE1", PiecesPerGarment: 1}}
	tc.PieceAreaScopes = map[string]entity.PieceAreaScope{
		"BOMKEY1": {ScopeKey: "BOMKEY1", Rows: []entity.PieceAreaRow{{
			PieceLineKey: "PIECE1",
			SizeId:       sql.NullInt64{Int64: 4, Valid: true},
			AreaCm2:      decimal.RequireFromString("14000"),
		}}},
	}
	tc.Colorways = []entity.TechCardColorway{{
		Id:        35,
		ProductId: sql.NullInt32{Int32: 35, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{
			Id:        34,
			BomItemId: sql.NullInt64{Int64: 56, Valid: true},
			PieceId:   sql.NullInt64{Int64: 14, Valid: true},
		}},
	}}
	return tc
}

// TestEstimateMakesTheCostNonZero is the user-facing promise of this phase, as an assertion.
//
// Before it, this exact card reported «себестоимость ещё не посчитана» while its BOM, its patterns
// and its recipe were all filled in: money was computed only from an authored norm, and a
// piece-bound row carries none by design.
func TestEstimateMakesTheCostNonZero(t *testing.T) {
	tc := estimateCard()
	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, nil, "EUR", tc.CostingBasis(), CostingFx{})
	// 14000 cm² / 140 cm = 100 cm = 1 m, at 100 EUR/m.
	if !cc.materialsPerUnit.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("materialsPerUnit = %s, want 100 — the estimate did not reach the money", cc.materialsPerUnit)
	}
	if !cc.hasEstimate {
		t.Error("hasEstimate is false; the figure would then be allowed to seed the catalogue")
	}
	if cc.hasUnpriced {
		t.Error("hasUnpriced is true; an estimated line is IN the total, not dropped from it")
	}
}

// TestEstimateNeverSeedsCostPrice is the other half, and the one that must never regress: the
// estimate is a NETTO lower bound (inter-piece waste is absent by construction, ~30% on real
// markers). Letting it reach product.cost_price would push that understatement into the margin
// chain and the accounts, where it is indistinguishable from a fact.
func TestEstimateNeverSeedsCostPrice(t *testing.T) {
	tc := estimateCard()
	cost, _ := ComputeTechCardUnitCost(tc, CostingFx{})
	if cost.Valid {
		t.Fatalf("an estimate-only card seeded cost_price with %s; it must seed nothing", cost.Decimal)
	}
}

// TestAuthoredNormWinsOverEstimate: a number a human typed always beats a number the system derived.
// Otherwise editing a norm DOWN would be silently replaced by an estimate pointing UP.
func TestAuthoredNormWinsOverEstimate(t *testing.T) {
	tc := estimateCard()
	tc.Colorways[0].Usages = append(tc.Colorways[0].Usages, entity.TechCardColorwayUsage{
		Id:          40,
		BomItemId:   sql.NullInt64{Int64: 56, Valid: true},
		Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("2"), Valid: true},
	})
	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, nil, "EUR", tc.CostingBasis(), CostingFx{})
	if !cc.materialsPerUnit.Equal(decimal.RequireFromString("200")) {
		t.Fatalf("materialsPerUnit = %s, want 200 (2 m × 100) — the authored norm must win alone", cc.materialsPerUnit)
	}
	if cc.hasEstimate {
		t.Error("hasEstimate is set although every slot carries an authored norm — the seed would be blocked for nothing")
	}
}

// TestNoEstimateWithoutAreas: assignments alone are not enough. Without measured geometry there is
// nothing to derive from, and the honest answer stays «not computed».
func TestNoEstimateWithoutAreas(t *testing.T) {
	tc := estimateCard()
	tc.PieceAreaScopes = nil
	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, nil, "EUR", tc.CostingBasis(), CostingFx{})
	if !cc.materialsPerUnit.IsZero() || cc.hasEstimate {
		t.Fatalf("estimated without areas: materials=%s hasEstimate=%v", cc.materialsPerUnit, cc.hasEstimate)
	}
}

// TestMeasuredButUncomputableSlotBlocksTheSeed is the loud half of the refusal rule.
//
// A card that HAS been measured, whose slot carries assignments and yet cannot be costed — stale
// patterns, no width, no price, arguing pins — has a fabric that goes into the garment and into no
// total. Staying silent there is how a cost gets published with a whole material missing. (A card
// that has NOT been measured is the normal pre-Ф1 state and stays silent: that is T8's empty
// recipe, not a failure.)
func TestMeasuredButUncomputableSlotBlocksTheSeed(t *testing.T) {
	tc := estimateCard()
	// Measured, but the patterns moved since — the areas describe files that no longer exist.
	sc := tc.PieceAreaScopes["BOMKEY1"]
	sc.Stale = true
	tc.PieceAreaScopes["BOMKEY1"] = sc

	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, nil, "EUR", tc.CostingBasis(), CostingFx{})
	if !cc.hasUnpriced {
		t.Fatal("a measured card whose slot could not be costed stayed silent; the seed would publish a cost missing that fabric")
	}
	if cost, _ := ComputeTechCardUnitCost(tc, CostingFx{}); cost.Valid {
		t.Fatalf("seeded %s despite an uncostable measured slot", cost.Decimal)
	}
}

// TestUnmeasuredCardStaysSilent pins the other side: every card in the database today has piece
// assignments and no measured areas. Raising a flag there would have stopped the cost seed for all
// of them at deploy — the regression the T8 release test caught.
func TestUnmeasuredCardStaysSilent(t *testing.T) {
	tc := estimateCard()
	tc.PieceAreaScopes = nil
	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, nil, "EUR", tc.CostingBasis(), CostingFx{})
	if cc.hasUnpriced {
		t.Fatal("an unmeasured card raised hasUnpriced; that is T8's empty recipe, not a failure")
	}
}

// TestMeasuredColorwayIsPricedFromItselfNotFromItsNeighbour closes С1.
//
// The old rule read «nothing but piece assignments» as «empty recipe» and handed such a colourway
// the STYLE figure — i.e. the primary colourway's price. That was defensible while a piece-only
// recipe produced no number of its own. Since Ф1 it produces one, and inheriting would give a
// colourway pinned to a different article somebody else's cost while its own sat computed and
// unused.
func TestMeasuredColorwayIsPricedFromItselfNotFromItsNeighbour(t *testing.T) {
	tc := estimateCard()
	if !colorwayHasOwnRecipe(tc, &tc.Colorways[0]) {
		t.Fatal("a measured piece-only colourway reads as having no recipe; it would inherit the style figure")
	}
	// And the unmeasured case keeps the old behaviour, because every card in the database is in it.
	tc.PieceAreaScopes = nil
	if colorwayHasOwnRecipe(tc, &tc.Colorways[0]) {
		t.Fatal("an unmeasured piece-only colourway claims a recipe; it would be costed as manual articles only — a silent price drop")
	}
}

// TestEstimateReachesTheStyleEstimateToo closes С4's visible half: the смета and the costing
// headline must not disagree about the same card. Before this, the headline counted an estimated
// slot and the смета did not list it at all.
func TestEstimateReachesTheStyleEstimateToo(t *testing.T) {
	tc := estimateCard()
	est := ComputeStyleCostEstimate(tc, 35, nil, CostingFx{Base: "EUR"})
	if est == nil {
		t.Fatal("no estimate produced")
	}
	var found bool
	for _, l := range est.Materials {
		if l.GetBomItemId() == 56 {
			found = true
		}
	}
	if !found {
		t.Fatal("the estimated slot is missing from the смета while the headline counts it — two numbers for one card")
	}
	if est.GetCaveat() == "" {
		t.Error("an area-estimated смета carries no caveat; the lower bound would read as a norm")
	}
}
