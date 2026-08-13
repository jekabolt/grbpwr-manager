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
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil), nil)

	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	require.Equal(t, "212", row.Required.Value, "200 m of marker norm × 1.06")
	require.Equal(t, "200", row.RequiredBeforeGrossup.Value, "the norm's own sum, un-grossed")
	require.Equal(t, "1.06", row.CuttingCoefficient.Value)
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_M, row.UnitCode)

	require.Len(t, resp.Contributions, 1)
	require.Equal(t, "212", resp.Contributions[0].Required.Value)
	require.Equal(t, "200", resp.Contributions[0].RequiredBeforeGrossup.Value)
	require.Equal(t, "1.06", resp.Contributions[0].CuttingCoefficient.Value)
}

// An article with no coefficient plans EXACTLY as it did before the field existed — the reason the
// migration deliberately backfills nothing.
func TestPlanUnsetCuttingCoefficientChangesNothing(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceMarker, 100, "")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", decimal.NullDecimal{}, nil), nil)

	require.Equal(t, "200", resp.Rows[0].Required.Value)
	require.Equal(t, "200", resp.Rows[0].RequiredBeforeGrossup.Value)
	require.Nil(t, resp.Rows[0].CuttingCoefficient, "no coefficient reported when the article has none")
}

// W3: A MANUAL norm takes BOTH multipliers — the slot's wastage percent (geometry of the lay) and
// the article's cutting coefficient (the roll's reality). Раньше здесь стоял ровно обратный тест, и
// он был прав при старом смысле процента: тот оплачивал в том числе усадку, поэтому коэффициент
// сверху был бы двойным счётом. W3 сузил процент до геометрии — и исключение исчезло вместе со
// своим основанием.
//
// Эта строка — и есть причина, по которой «до» называется required_before_GROSSUP, а не
// required_before_coefficient: 222.6 / 200 = 1.113, то есть ОБА множителя сразу, и ни один из них
// по отдельности из пары чисел не восстанавливается. Контракт обещает только
// required >= required_before_grossup; коэффициент читается из своего поля.
func TestPlanManualNormTakesBothWastageAndCoefficient(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "5")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil), nil)

	row := resp.Rows[0]
	require.Equal(t, "222.6", row.Required.Value, "200 × 1.05 wastage × 1.06 coefficient")
	require.Equal(t, "200", row.RequiredBeforeGrossup.Value,
		"before ANY gross-up: neither factor is folded in here")
	require.Equal(t, "1.06", row.CuttingCoefficient.Value)
	require.False(t, hasCaveat(resp.Caveats, "cutting coefficient 1.06 not applied"),
		"the dial DID bite — an explanation of a no-op would be a lie: %v", resp.Caveats)
	require.False(t, hasCaveat(resp.Caveats, "applied to PART of this row"),
		"the whole row took it: %v", resp.Caveats)
}

// The other row on which required = before × coefficient is false: no coefficient anywhere, and the
// gap between the two numbers is the plain BOM wastage. The pair still reads honestly as
// "the norm asked for 200, the plan asks for 210".
func TestPlanWastageOnlyRowHasNoCoefficientAndStillDecomposes(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "5")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", decimal.NullDecimal{}, nil), nil)

	row := resp.Rows[0]
	require.Equal(t, "210", row.Required.Value)
	require.Equal(t, "200", row.RequiredBeforeGrossup.Value)
	require.Nil(t, row.CuttingCoefficient, "the article has none, so none is reported")
	require.False(t, hasCaveat(resp.Caveats, "not applied"),
		"nothing to explain when there is no dial at all: %v", resp.Caveats)
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
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("pcs", nd2("1.5"), nil), nil)
	require.Equal(t, "40", resp.Rows[0].Required.Value, "4 buttons × 10 garments, no gross-up of any kind")
	require.Equal(t, "40", resp.Rows[0].RequiredBeforeGrossup.Value, "and the two numbers agree exactly")
	// The caveat must not tell the operator their buttons are "manual norms" — that is the wrong
	// explanation of a right number, and the one they would then go and try to fix.
	require.True(t, hasCaveat(resp.Caveats, "the counted quantities take no gross-up at all"),
		"a counted row must be explained AS counted: %v", resp.Caveats)
	require.False(t, hasCaveat(resp.Caveats, "norms for it are manual"),
		"nothing here is a manual norm: %v", resp.Caveats)
}

// Ф5а.3. The silently-wrong addition the vocabulary exists to cut: a slot spelled «м» against an
// article spelled "m" used to be "two different units" — the number stayed in the slot's unit while
// being netted against the article's stock, and a caveat was raised about a conflict that never was.
func TestPlanTreatsCyrillicMetreAsMetre(t *testing.T) {
	run, card := planFixture("м", "2", entity.ConsumptionSourceManual, 100, "")
	onHand := map[int]decimal.Decimal{100: d("50")}
	resp := ComputeProductionRunMaterialPlan(run, card, onHand, nil, fabricArticle("m", decimal.NullDecimal{}, nil), nil)

	require.Equal(t, "m", resp.Rows[0].Unit, "the row takes the ARTICLE's unit, as for any agreeing pair")
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_M, resp.Rows[0].UnitCode)
	require.Equal(t, "150", resp.Rows[0].Shortage.Value, "200 needed − 50 on hand")
	require.False(t, hasCaveat(resp.Caveats, "no conversion"), "no conflict to warn about: %v", resp.Caveats)
}

// A genuinely unknown unit is still not guessed: the caveat path stays, and the row keeps the slot's
// unit and reports UNKNOWN rather than pretending to know what it is.
func TestPlanUnknownUnitStillCaveats(t *testing.T) {
	run, card := planFixture("погонный метр", "2", entity.ConsumptionSourceManual, 100, "")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("kg", decimal.NullDecimal{}, nil), nil)

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
		fabricArticle("kg", decimal.NullDecimal{}, attr), nil)

	row := resp.Rows[0]
	require.Equal(t, "kg", row.Unit, "the row is in the article's stock unit, so it nets against kg stock")
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_KG, row.UnitCode)
	// 200 m × 1.50 m × 220 g/m² = 66 000 g = 66 kg. On the CUTTING width (146 cm) it would be 64.24 —
	// the 2–4% understatement the spec names.
	require.Equal(t, "66", row.Required.Value)
	require.Equal(t, "56", row.Shortage.Value, "66 kg needed − 10 kg on hand")
	require.True(t, hasCaveat(resp.Caveats, "selvedge included"), "the conversion must be stated: %v", resp.Caveats)
	require.Equal(t, "200", resp.Contributions[0].Required.Value, "the contribution stays in the SLOT's unit (metres)")
	require.Equal(t, "m", resp.Contributions[0].Unit)
}

// Kilograms and the coefficient compose: the gross-up happens in metres, the conversion after, and
// required_before_grossup is converted too so both figures are in the same (stock) unit.
func TestPlanKilogramsAndCoefficientCompose(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceMarker, 100, "")
	attr := &entity.MaterialFabricAttr{WidthCm: nd2("150"), WeightGsm: nd2("220")}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("kg", nd2("1.05"), attr), nil)

	row := resp.Rows[0]
	require.Equal(t, "kg", row.Unit)
	require.Equal(t, "69.3", row.Required.Value, "210 m × 1.5 × 220 / 1000")
	require.Equal(t, "66", row.RequiredBeforeGrossup.Value, "the same conversion on the un-grossed 200 m")
}

// No width or no density means NO number: a weight computed from a guessed roll geometry is one
// nobody can defend, so the row honestly stays in metres and says why.
func TestPlanKilogramsWithoutRollGeometryDoesNotGuess(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil,
		fabricArticle("kg", decimal.NullDecimal{}, &entity.MaterialFabricAttr{WidthCm: nd2("150")}), nil)

	require.Equal(t, "m", resp.Rows[0].Unit, "no density → no conversion, and the label follows the number")
	require.Equal(t, "200", resp.Rows[0].Required.Value)
	require.True(t, hasCaveat(resp.Caveats, "cannot convert"), "%v", resp.Caveats)
}

// mixedUnitFixture is the shape the caveat latch used to hide: ONE article reached by two slots of
// the same colourway measured in different units — slot A in metres (converted to the article's kg),
// slot B in pieces. The rollup adds them anyway and labels the total with whichever unit it saw
// first; that summation is an older defect being scoped separately, but it must never be silent.
func mixedUnitFixture(qty int, sizes int) (*entity.ProductionRun, *entity.TechCard) {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{
			Id: 501, Name: "Main fabric", Section: entity.BomSectionFabric,
			MaterialId: sql.NullInt64{Int64: 100, Valid: true},
			Unit:       sql.NullString{String: "m", Valid: true},
		},
		{
			Id: 502, Name: "Patch", Section: entity.BomSectionTrim,
			MaterialId: sql.NullInt64{Int64: 100, Valid: true},
			Unit:       sql.NullString{String: "pcs", Valid: true},
		},
	}
	card.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
		Usages: []entity.TechCardColorwayUsage{
			{
				BomItemId:         sql.NullInt64{Int64: 501, Valid: true},
				Consumption:       nd2("2"),
				ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
			},
			{
				BomItemId:         sql.NullInt64{Int64: 502, Valid: true},
				Quantity:          nd2("3"),
				ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
			},
		},
	}}
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7}}
	for s := 1; s <= sizes; s++ {
		run.Lines = append(run.Lines, entity.ProductionRunLine{
			ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: s, PlannedQty: qty,
		})
	}
	return run, card
}

// The caveat latch was keyed on the material id, so the FIRST unit note an article produced silenced
// every later one. Here the winner was the kg conversion — which reads like a precise, successful
// conversion — while the number underneath it was metres-converted-to-kg plus a raw count of pieces.
// Making a wrong number look right is worse than leaving it obviously wrong.
func TestPlanMixedSlotUnitsAreAlwaysCaveated(t *testing.T) {
	run, card := mixedUnitFixture(100, 1)
	attr := &entity.MaterialFabricAttr{WidthCm: nd2("150"), WeightGsm: nd2("220")}
	resp := ComputeProductionRunMaterialPlan(run, card, map[int]decimal.Decimal{100: d("70")}, nil,
		fabricArticle("kg", decimal.NullDecimal{}, attr), nil)

	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	// The sum across units is untouched by this fix — it is a separate, wider defect. What must NOT
	// happen is that it goes unremarked.
	require.Equal(t, "366", row.Required.Value, "66 kg + 300 pcs — the pre-existing mixed sum, unchanged")
	require.Equal(t, "kg", row.Unit)

	require.True(t, hasCaveat(resp.Caveats, "SUM ACROSS UNITS"),
		"a total added across units must say so: %v", resp.Caveats)
	require.True(t, hasCaveat(resp.Caveats, `"kg"`) && hasCaveat(resp.Caveats, `"pcs"`),
		"the caveat must name BOTH units: %v", resp.Caveats)
	// And the conversion note must still be there — the point is that neither hides the other.
	require.True(t, hasCaveat(resp.Caveats, "selvedge included"),
		"the successful conversion is still reported: %v", resp.Caveats)
}

// The latch exists because the loop visits each slot once per run LINE. Dedupe is by the statement
// now, not by the material, so a five-size run says each true thing once — and still says all of them.
func TestPlanUnitCaveatsAreDedupedByStatementNotByMaterial(t *testing.T) {
	run, card := mixedUnitFixture(10, 5)
	attr := &entity.MaterialFabricAttr{WidthCm: nd2("150"), WeightGsm: nd2("220")}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil,
		fabricArticle("kg", decimal.NullDecimal{}, attr), nil)

	counts := map[string]int{}
	for _, c := range resp.Caveats {
		counts[c]++
	}
	for c, n := range counts {
		require.Equal(t, 1, n, "caveat repeated %d times across 5 sizes: %q", n, c)
	}
	require.True(t, hasCaveat(resp.Caveats, "SUM ACROSS UNITS"), "%v", resp.Caveats)
	require.True(t, hasCaveat(resp.Caveats, "selvedge included"), "%v", resp.Caveats)
}

// A single-unit article must stay quiet: the new alarm keys on a real disagreement, not on the mere
// presence of two slots.
func TestPlanAgreeingSlotUnitsRaiseNoMixedUnitCaveat(t *testing.T) {
	run, card := mixedUnitFixture(100, 1)
	card.BomItems[1].Unit = sql.NullString{String: "м", Valid: true} // Cyrillic: the SAME unit as "m"
	card.Colorways[0].Usages[1].Quantity = decimal.NullDecimal{}
	card.Colorways[0].Usages[1].Consumption = nd2("3")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil,
		fabricArticle("m", decimal.NullDecimal{}, nil), nil)

	require.Equal(t, "500", resp.Rows[0].Required.Value, "200 m + 300 m, one unit throughout")
	require.False(t, hasCaveat(resp.Caveats, "SUM ACROSS UNITS"),
		"«м» and \"m\" are one unit — no disagreement to report: %v", resp.Caveats)
}
