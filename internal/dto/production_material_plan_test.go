package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ComputeProductionRunMaterialPlan resolves per-colourway norms × planned qty × (1+wastage) to a
// per-material requirement, nets it against on-hand + issued, and surfaces caveats.
func TestComputeProductionRunMaterialPlan(t *testing.T) {
	mid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	bomIdx := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Name: "Main fabric", MaterialId: mid(100), Unit: sql.NullString{String: "m", Valid: true}, WastagePercent: nd2("5")}, // 5% wastage
		{Name: "Free-text trim", Unit: sql.NullString{String: "pc", Valid: true}},                                             // no material_id → caveat
		{Id: 503, Name: "Zipper (FK-keyed)", MaterialId: mid(200), Unit: sql.NullString{String: "pcs", Valid: true}},          // referenced by FK, not index
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Black", ProductId: sql.NullInt32{Int32: 55, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: bomIdx(0), Consumption: nd2("2")}, // 2 m per garment
			{BomItemIndex: bomIdx(1), Quantity: nd2("3")},    // trim, no material_id
		}},
		{Id: 2, Name: "Navy", ProductId: sql.NullInt32{Int32: 66, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: bomIdx(0), Consumption: nd2("2")},
		}},
		// A line_key-world recipe: the usage carries ONLY the resolved bom_item_id FK, no positional
		// index (what UpdateColorwayRecipe writes since S2/S3). The beta A–L run (H.22b) caught the
		// plan skipping these entirely and returning zero rows.
		{Id: 3, Name: "Red", ProductId: sql.NullInt32{Int32: 77, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemId: mid(503), Quantity: nd2("1")},
		}},
	}

	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
			{ProductId: sql.NullInt32{Int32: 66, Valid: true}, SizeId: 1, PlannedQty: 20},
			{ProductId: sql.NullInt32{Int32: 77, Valid: true}, SizeId: 1, PlannedQty: 10}, // FK-keyed recipe
			{ProductId: sql.NullInt32{Int32: 99, Valid: true}, SizeId: 1, PlannedQty: 5},  // no matching colourway → caveat
		},
	}}

	onHand := map[int]decimal.Decimal{100: d("5")}
	issued := map[int]decimal.Decimal{100: d("10")}

	resp := ComputeProductionRunMaterialPlan(run, card, onHand, issued, nil)
	require.Len(t, resp.Rows, 2, "materials 100 (index-keyed) and 200 (FK-keyed) are countable")
	fkRow := resp.Rows[1]
	require.Equal(t, int32(200), fkRow.MaterialId, "the FK-only usage resolves via bom_item_id")
	require.Equal(t, "10", fkRow.Required.Value, "1 pc × 10 garments, no wastage")
	row := resp.Rows[0]
	require.Equal(t, int32(100), row.MaterialId)
	require.Equal(t, "Main fabric", row.MaterialName)
	require.Equal(t, "m", row.Unit)
	// (2×10 + 2×20) × 1.05 = 60 × 1.05 = 63
	require.Equal(t, "63", row.Required.Value)
	require.Equal(t, "5", row.OnHand.Value)
	require.Equal(t, "10", row.Issued.Value)
	require.Equal(t, "48", row.Shortage.Value, "63 − 10 − 5")
	require.Equal(t, "-53", row.IssuedVariance.Value, "issued 10 − required 63 (under-issued so far)")
	require.False(t, row.HasSizeNorms, "per-garment norm, not size-graded")

	// The free-text BOM line (no pin, no slot default) is now a BLOCKER — slot × colourway with
	// the uncounted garment qty — not a caveat lost in prose. The product with no colourway
	// stays a caveat (it has no slot to anchor a blocker to).
	require.GreaterOrEqual(t, len(resp.Caveats), 1)
	require.Len(t, resp.Blockers, 1)
	require.Equal(t, "no article (no pin, no slot default)", resp.Blockers[0].Reason)
	require.Equal(t, int32(55), resp.Blockers[0].ColorwayId)
	require.Equal(t, int32(10), resp.Blockers[0].PlannedQty)
	require.Equal(t, "Free-text trim", resp.Blockers[0].SlotName)
}

// Slots: a colourway's PIN (usage.material_id) outranks the slot default; the rollup is keyed by
// the RESOLVED article (each article nets against its own one pile of stock), the contribution is
// marked pinned and labelled by the article, and a thread norm in metres converts to the stock's
// cones via length_per_cone_m.
func TestComputeProductionRunMaterialPlan_PinResolution(t *testing.T) {
	mid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 501, Name: "основная молния", Section: entity.BomSectionHardware, MaterialId: mid(100), Unit: ns("pcs")},
		{Id: 502, Name: "нить основная", Section: entity.BomSectionThread, MaterialId: mid(300), Unit: ns("m")},
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: 55, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemId: mid(501), Quantity: nd2("1")}, // inherits default article 100
			{BomItemId: mid(502), Consumption: nd2("180")},
		}},
		{Id: 2, Name: "bone", ProductId: sql.NullInt32{Int32: 66, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemId: mid(501), Quantity: nd2("1"), MaterialId: mid(200)}, // PIN: silver zip
			{BomItemId: mid(502), Consumption: nd2("180")},
		}},
	}
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 60},
			{ProductId: sql.NullInt32{Int32: 66, Valid: true}, SizeId: 1, PlannedQty: 40},
		},
	}}
	linked := map[int]entity.MaterialWithPrice{
		100: {Material: entity.Material{Id: 100, MaterialInsert: entity.MaterialInsert{Name: "YKK5VS antique brass", Unit: ns("pcs")}}},
		200: {Material: entity.Material{Id: 200, MaterialInsert: entity.MaterialInsert{Name: "YKK5VS silver", Unit: ns("pcs")}}},
		300: {Material: entity.Material{Id: 300, MaterialInsert: entity.MaterialInsert{
			Name: "Gütermann Mara 100", Unit: ns("cone"),
			ThreadAttr: &entity.MaterialThreadAttr{LengthPerConeM: nd2("5000")},
		}}},
	}
	onHand := map[int]decimal.Decimal{100: d("240"), 200: d("10")}

	resp := ComputeProductionRunMaterialPlan(run, card, onHand, nil, linked)

	byID := map[int32]*int{}
	for i, r := range resp.Rows {
		byID[r.MaterialId] = &i
		_ = i
	}
	require.Len(t, resp.Rows, 3, "AB zip + silver zip + thread — never 100 zips of one article")
	var ab, silver, thread int
	for i, r := range resp.Rows {
		switch r.MaterialId {
		case 100:
			ab = i
		case 200:
			silver = i
		case 300:
			thread = i
		}
	}
	require.Equal(t, "60", resp.Rows[ab].Required.Value, "black keeps the default article")
	require.Equal(t, "YKK5VS antique brass", resp.Rows[ab].MaterialName, "rollup labelled by ARTICLE, not the slot role")
	require.Equal(t, "40", resp.Rows[silver].Required.Value, "bone's pin diverts its 40 garments")
	require.Equal(t, "30", resp.Rows[silver].Shortage.Value, "40 needed − 10 on hand")
	// thread: (60+40) × 180 m = 18000 m → / 5000 m per cone = 3.6 cones, in the STOCK unit
	require.Equal(t, "3.6", resp.Rows[thread].Required.Value)
	require.Equal(t, "cone", resp.Rows[thread].Unit)

	// Contributions: slot × colourway, spec units, pinned flag on bone's zip line.
	var bonePinned bool
	for _, c := range resp.Contributions {
		if c.ColorwayId == 66 && c.MaterialId == 200 {
			bonePinned = c.Pinned
			require.Equal(t, "основная молния", c.SlotName)
			require.Equal(t, "YKK5VS silver", c.MaterialName)
		}
	}
	require.True(t, bonePinned, "bone's zip contribution must be marked pinned")
	require.Empty(t, resp.Blockers, "every slot has an article and a norm here")
}

// materials-from-stock is folded into the run actuals; a manual materials cost alongside stock
// issues raises the mixed-sources caveat, and an uncosted issue raises has_uncosted_issues.
func TestProductionRunActualsMaterialsFromStock(t *testing.T) {
	run := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10, ReceivedQty: ni(10)}},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostCMT, Amount: d("400"), Currency: "EUR", AmountBase: nd2("400")},
		},
	}}
	run.MaterialMovements = []entity.MaterialMovement{
		{MaterialId: 100, MovementType: entity.MaterialMovementIssueProduction, Quantity: d("21"), UnitCostBase: nd2("10")}, // 210
		{MaterialId: 100, MovementType: entity.MaterialMovementReturnProduction, Quantity: d("1"), UnitCostBase: nd2("10")}, // −10
	}
	a := ConvertEntityProductionRunToPb(run).Actuals
	require.Equal(t, "200", a.MaterialsFromStockBase.Value, "210 issued − 10 returned")
	require.Equal(t, "600", a.ActualTotalBase.Value, "400 CMT + 200 materials-from-stock")
	require.False(t, a.MixedMaterialsSources, "no manual kind=materials, so no double-count risk")
	require.False(t, a.HasUncostedIssues)
	require.Equal(t, "60", a.ActualUnitCost.Value, "600 / 10 received")

	// add a manual materials cost + an uncosted issue → both caveats fire.
	run.Costs = append(run.Costs, entity.ProductionRunCost{Kind: entity.ProductionRunCostMaterials, Amount: d("50"), Currency: "EUR", AmountBase: nd2("50")})
	run.MaterialMovements = append(run.MaterialMovements, entity.MaterialMovement{MaterialId: 200, MovementType: entity.MaterialMovementIssueProduction, Quantity: d("5")}) // no unit_cost_base
	a = ConvertEntityProductionRunToPb(run).Actuals
	require.True(t, a.MixedMaterialsSources, "manual materials + stock issues")
	require.True(t, a.HasUncostedIssues, "an issue had no average cost")

	// cost_price figure is not trustworthy while an issue is uncosted.
	require.False(t, ProductionRunActualUnitCostBase(run).Valid, "uncosted issue → no cost_price seed")
}
