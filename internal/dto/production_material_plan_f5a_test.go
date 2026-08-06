package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// planFixture builds a one-slot / one-colourway run: `qty` garments of a fabric slot spelled in
// slotUnit, at `norm` per garment, with the given consumption provenance.
func planFixture(slotUnit, norm, source string, qty int, wastagePct string) (*entity.ProductionRun, *entity.TechCard) {
	bom := entity.TechCardBomItem{
		Id: 501, Name: "Main fabric", Section: entity.BomSectionFabric,
		MaterialId: sql.NullInt64{Int64: 100, Valid: true},
		Unit:       sql.NullString{String: slotUnit, Valid: true},
	}
	if wastagePct != "" {
		bom.WastagePercent = nd2(wastagePct)
	}
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{bom}
	card.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId:         sql.NullInt64{Int64: 501, Valid: true},
			Consumption:       nd2(norm),
			ConsumptionSource: sql.NullString{String: source, Valid: source != ""},
		}},
	}}
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: qty},
		},
	}}
	return run, card
}

func fabricArticle(unit string, coeff decimal.NullDecimal, attr *entity.MaterialFabricAttr) map[int]entity.MaterialWithPrice {
	return map[int]entity.MaterialWithPrice{100: {Material: entity.Material{
		Id: 100, MaterialInsert: entity.MaterialInsert{
			Name: "Cotton twill", Unit: sql.NullString{String: unit, Valid: true},
			CuttingCoefficient: coeff, FabricAttr: attr,
		},
	}}}
}

func hasCaveat(caveats []string, needle string) bool {
	for _, c := range caveats {
		if strings.Contains(c, needle) {
			return true
		}
	}
	return false
}

// Ф5а.2. The cutting coefficient grosses up a MARKER-sourced norm — the losses a marker cannot
// contain (усадка, обход пороков, сращивание, оттеночные полосы) — and the response can say WHY the
// number is bigger than the norm, with the un-grossed sum beside the dial.
func TestPlanAppliesCuttingCoefficientToMarkerNorms(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceMarker, 100, "")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil))

	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	require.Equal(t, "212", row.Required.Value, "200 m of marker norm × 1.06")
	require.Equal(t, "200", row.RequiredBeforeCoefficient.Value, "the norm's own sum, un-grossed")
	require.Equal(t, "1.06", row.CuttingCoefficient.Value)
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_M, row.UnitCode)

	require.Len(t, resp.Contributions, 1)
	require.Equal(t, "212", resp.Contributions[0].Required.Value)
	require.Equal(t, "200", resp.Contributions[0].RequiredBeforeCoefficient.Value)
	require.Equal(t, "1.06", resp.Contributions[0].CuttingCoefficient.Value)
}

// An article with no coefficient plans EXACTLY as it did before the field existed — the reason the
// migration deliberately backfills nothing.
func TestPlanUnsetCuttingCoefficientChangesNothing(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceMarker, 100, "")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", decimal.NullDecimal{}, nil))

	require.Equal(t, "200", resp.Rows[0].Required.Value)
	require.Equal(t, "200", resp.Rows[0].RequiredBeforeCoefficient.Value)
	require.Nil(t, resp.Rows[0].CuttingCoefficient, "no coefficient reported when the article has none")
}

// A MANUAL norm keeps its BOM wastage estimate and does NOT also take the coefficient: the two
// worlds are disjoint, or the line would be grossed twice. The no-op is reported rather than left to
// look like a broken field.
func TestPlanManualNormKeepsWastageAndSaysTheCoefficientDidNotBite(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "5")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil))

	require.Equal(t, "210", resp.Rows[0].Required.Value, "200 × 1.05 wastage — NOT × 1.06 as well")
	require.Equal(t, "200", resp.Rows[0].RequiredBeforeCoefficient.Value)
	require.True(t, hasCaveat(resp.Caveats, "cutting coefficient 1.06 not applied"),
		"a dial that does nothing must say so: %v", resp.Caveats)
}

// A counted trim takes neither factor: 4 buttons stay 4 buttons.
func TestPlanCountedTrimTakesNoCoefficient(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 501, Name: "Button", Section: entity.BomSectionHardware,
		MaterialId: sql.NullInt64{Int64: 100, Valid: true},
		Unit:       sql.NullString{String: "pcs", Valid: true},
	}}
	card.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId:         sql.NullInt64{Int64: 501, Valid: true},
			Quantity:          nd2("4"),
			ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
		}},
	}}
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines:      []entity.ProductionRunLine{{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10}},
	}}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("pcs", nd2("1.5"), nil))
	require.Equal(t, "40", resp.Rows[0].Required.Value, "4 buttons × 10 garments, no gross-up of any kind")
}

// Ф5а.3. The silently-wrong addition the vocabulary exists to cut: a slot spelled «м» against an
// article spelled "m" used to be "two different units" — the number stayed in the slot's unit while
// being netted against the article's stock, and a caveat was raised about a conflict that never was.
func TestPlanTreatsCyrillicMetreAsMetre(t *testing.T) {
	run, card := planFixture("м", "2", entity.ConsumptionSourceManual, 100, "")
	onHand := map[int]decimal.Decimal{100: d("50")}
	resp := ComputeProductionRunMaterialPlan(run, card, onHand, nil, fabricArticle("m", decimal.NullDecimal{}, nil))

	require.Equal(t, "m", resp.Rows[0].Unit, "the row takes the ARTICLE's unit, as for any agreeing pair")
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_M, resp.Rows[0].UnitCode)
	require.Equal(t, "150", resp.Rows[0].Shortage.Value, "200 needed − 50 on hand")
	require.False(t, hasCaveat(resp.Caveats, "no conversion"), "no conflict to warn about: %v", resp.Caveats)
}

// A genuinely unknown unit is still not guessed: the caveat path stays, and the row keeps the slot's
// unit and reports UNKNOWN rather than pretending to know what it is.
func TestPlanUnknownUnitStillCaveats(t *testing.T) {
	run, card := planFixture("погонный метр", "2", entity.ConsumptionSourceManual, 100, "")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("kg", decimal.NullDecimal{}, nil))

	require.Equal(t, "погонный метр", resp.Rows[0].Unit)
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_UNKNOWN, resp.Rows[0].UnitCode)
	require.True(t, hasCaveat(resp.Caveats, "no conversion"), "a real unit conflict must still be caveated: %v", resp.Caveats)
}

// Ф5а.4. Fabric bought by the kilo: the metre norm converts on the FULL roll width (кромка
// included — the selvedge is paid for) times the ARTICLE's density.
func TestPlanConvertsMetresToKilogramsOnFullWidth(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "")
	attr := &entity.MaterialFabricAttr{WidthCm: nd2("150"), WeightGsm: nd2("220"), SelvedgeCm: d("2")}
	resp := ComputeProductionRunMaterialPlan(run, card, map[int]decimal.Decimal{100: d("10")}, nil,
		fabricArticle("kg", decimal.NullDecimal{}, attr))

	row := resp.Rows[0]
	require.Equal(t, "kg", row.Unit, "the row is in the article's stock unit, so it nets against kg stock")
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_KG, row.UnitCode)
	// 200 m × 1.50 m × 220 g/m² = 66 000 g = 66 kg. On the CUTTING width (146 cm) it would be 64.24 —
	// the 2–4% understatement the spec names.
	require.Equal(t, "66", row.Required.Value)
	require.Equal(t, "56", row.Shortage.Value, "66 kg needed − 10 kg on hand")
	require.True(t, hasCaveat(resp.Caveats, "кромка included"), "the conversion must be stated: %v", resp.Caveats)
	require.Equal(t, "200", resp.Contributions[0].Required.Value, "the contribution stays in the SLOT's unit (metres)")
	require.Equal(t, "m", resp.Contributions[0].Unit)
}

// Kilograms and the coefficient compose: the gross-up happens in metres, the conversion after, and
// required_before_coefficient is converted too so both figures are in the same (stock) unit.
func TestPlanKilogramsAndCoefficientCompose(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceMarker, 100, "")
	attr := &entity.MaterialFabricAttr{WidthCm: nd2("150"), WeightGsm: nd2("220")}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("kg", nd2("1.05"), attr))

	row := resp.Rows[0]
	require.Equal(t, "kg", row.Unit)
	require.Equal(t, "69.3", row.Required.Value, "210 m × 1.5 × 220 / 1000")
	require.Equal(t, "66", row.RequiredBeforeCoefficient.Value, "the same conversion on the un-grossed 200 m")
}

// No width or no density means NO number: a weight computed from a guessed roll geometry is one
// nobody can defend, so the row honestly stays in metres and says why.
func TestPlanKilogramsWithoutRollGeometryDoesNotGuess(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil,
		fabricArticle("kg", decimal.NullDecimal{}, &entity.MaterialFabricAttr{WidthCm: nd2("150")}))

	require.Equal(t, "m", resp.Rows[0].Unit, "no density → no conversion, and the label follows the number")
	require.Equal(t, "200", resp.Rows[0].Required.Value)
	require.True(t, hasCaveat(resp.Caveats, "cannot convert"), "%v", resp.Caveats)
}
