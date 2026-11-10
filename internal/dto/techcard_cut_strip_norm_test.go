package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// What every reader gets for a strip line. The fixture is the golden shape: a measured fabric line, and a
// countable-section line bought by the metre with no recipe row and no typed quantity.
//
// Fabric: 2 m x 10 EUR x 1.08 = 21.6 per garment, 40 m before grossup and 43.2 m after for 20 garments.
// Strip line: 2 EUR/m, 10% wastage. The run is 10 garments of each of the two sizes.

const (
	cutNormFabricKey = "01JABCDEFGHJKMNPQRSTVWXYF1"
	cutNormStripLine = "01JABCDEFGHJKMNPQRSTVWXYL1"
	cutNormStripKey  = "01JABCDEFGHJKMNPQRSTVWXYS1"
)

func cutNormI32(v int32) *int32 { return &v }

func cutNormCard(section entity.TechCardBomSection, kind string) *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{1, 2}
	card.BomItems = []entity.TechCardBomItem{
		{
			Id: 501, LineKey: cutNormFabricKey, Name: "Main fabric", Section: entity.BomSectionFabric,
			MaterialId: sql.NullInt64{Int64: 100, Valid: true}, Unit: nstr("m"),
			UnitPrice: nd("10"), Currency: nstr("EUR"), WastagePercent: nd("8"),
		},
		{
			Id: 502, LineKey: cutNormStripLine, Name: "rubber", Section: section,
			Kind:       sql.NullString{String: kind, Valid: kind != ""},
			MaterialId: sql.NullInt64{Int64: 200, Valid: true}, Unit: nstr("m"),
			UnitPrice: nd("2"), Currency: nstr("EUR"), WastagePercent: nd("10"),
		},
	}
	card.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId: sql.NullInt64{Int64: 501, Valid: true}, Consumption: nd("2"),
			ConsumptionSource: nstr(entity.ConsumptionSourceManual),
		}},
	}}
	card.Costing = &entity.TechCardCosting{Currency: nstr("EUR")}
	return card
}

func cutNormStrip(mode string) entity.TechCardCutStrip {
	return entity.TechCardCutStrip{
		BomItemId: 502, BomLineKey: cutNormStripLine, LineKey: cutNormStripKey, LengthMode: mode,
		FitPct: decimal.RequireFromString("90"), OverlapMm: decimal.RequireFromString("20"),
		WidthMm: decimal.RequireFromString("30"), Qty: 1,
	}
}

func cutNormSeams(s *entity.TechCardCutStrip, seams map[int]string) {
	for _, size := range []int{1, 2} {
		if mm, ok := seams[size]; ok {
			s.Sizes = append(s.Sizes, entity.TechCardCutStripSize{SizeId: size, SeamMm: nd(mm)})
		}
	}
}

func cutNormRun() *entity.ProductionRun {
	return &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 2, PlannedQty: 10},
		},
	}}
}

func cutNormArticles() map[int]entity.MaterialWithPrice {
	article := func(id int) entity.MaterialWithPrice {
		return entity.MaterialWithPrice{Material: entity.Material{Id: id, MaterialInsert: entity.MaterialInsert{
			Name: "article", Unit: nstr("m"),
		}}}
	}
	return map[int]entity.MaterialWithPrice{100: article(100), 200: article(200)}
}

func cutNormApply(card *entity.TechCard, strips ...entity.TechCardCutStrip) {
	card.CutStrips = strips
	entity.ApplyCutStripNorms(card.BomItems, card.CutStrips, card.SizeIds, card.Colorways)
}

func cutPlanRow(t *testing.T, resp *pb_admin.GetProductionRunMaterialPlanResponse, materialID int32) *pb_admin.MaterialPlanRow {
	t.Helper()
	for _, r := range resp.GetRows() {
		if r.GetMaterialId() == materialID {
			return r
		}
	}
	t.Fatalf("no plan row for material %d", materialID)
	return nil
}

func cutNormBlockerKeys(resp *pb_admin.GetProductionRunMaterialPlanResponse) []string {
	out := []string{}
	for _, b := range resp.GetBlockers() {
		out = append(out, b.GetKey())
	}
	return out
}

func TestCutStripNormEveryReaderGetsTheNumber(t *testing.T) {
	sections := map[string]struct {
		section entity.TechCardBomSection
		kind    string
	}{
		"hardware, the golden line": {entity.BomSectionHardware, string(entity.BomKindChain)},
		"trim":                      {entity.BomSectionTrim, ""},
	}
	type want struct {
		sizeNorms  []string // what the line's card row says per size; empty for a flat norm
		flat       string
		unit       string // whole card cost, range average
		onS1, onS2 string // whole card cost on one size
		before     string // plan, strip article, before wastage
		required   string // plan, strip article, after wastage
	}
	manualS2 := cutNormStrip(entity.CutStripModeFromSeam)
	cutNormSeams(&manualS2, map[int]string{1: "700", 2: "800"})
	manualS2.Sizes[1].ManualCutMm = sql.NullInt32{Int32: 900, Valid: true}

	fromSeam := cutNormStrip(entity.CutStripModeFromSeam)
	cutNormSeams(&fromSeam, map[int]string{1: "700", 2: "800"})

	perSize := cutNormStrip(entity.CutStripModePerSize)
	perSize.Sizes = []entity.TechCardCutStripSize{
		{SizeId: 1, ManualCutMm: sql.NullInt32{Int32: 600, Valid: true}},
		{SizeId: 2, ManualCutMm: sql.NullInt32{Int32: 700, Valid: true}},
	}

	oneLength := cutNormStrip(entity.CutStripModeOneLength)
	oneLength.OneLengthMm = sql.NullInt32{Int32: 500, Valid: true}

	doubled := cutNormStrip(entity.CutStripModeOneLength)
	doubled.OneLengthMm, doubled.Qty = sql.NullInt32{Int32: 500, Valid: true}, 2

	for name, tc := range map[string]struct {
		strip entity.TechCardCutStrip
		want  want
	}{
		// cut = 700*0.9+20 = 650, 800*0.9+20 = 740. Average 0.695 m * 2 EUR * 1.1 = 1.529, on top of the
		// fabric's 21.6 that is 23.129, and the cost is published in whole cents.
		"from seam": {fromSeam, want{
			sizeNorms: []string{"0.65", "0.74"},
			unit:      "23.13", onS1: "23.03", onS2: "23.23",
			before: "13.9", required: "15.29",
		}},
		"per size": {perSize, want{
			sizeNorms: []string{"0.6", "0.7"},
			unit:      "23.03", onS1: "22.92", onS2: "23.14",
			before: "13", required: "14.3",
		}},
		"one length": {oneLength, want{
			flat: "0.5",
			unit: "22.7", onS1: "22.7", onS2: "22.7",
			before: "10", required: "11",
		}},
		"qty multiplies": {doubled, want{
			flat: "1",
			unit: "23.8", onS1: "23.8", onS2: "23.8",
			before: "20", required: "22",
		}},
		"a manual value on one size wins": {manualS2, want{
			sizeNorms: []string{"0.65", "0.9"},
			unit:      "23.3", onS1: "23.03", onS2: "23.58",
			before: "15.5", required: "17.05",
		}},
	} {
		for secName, sec := range sections {
			t.Run(name+"/"+secName, func(t *testing.T) {
				card := cutNormCard(sec.section, sec.kind)
				cutNormApply(card, tc.strip)

				pb := ConvertEntityTechCardToPb(card, CostingFx{Base: "EUR"})
				require.Len(t, pb.GetCutStripNorms(), 1)
				norm := pb.GetCutStripNorms()[0]
				require.True(t, norm.GetComplete())
				if tc.want.flat != "" {
					require.Equal(t, tc.want.flat, norm.GetNorm().GetValue())
				}
				got := []string{}
				for _, row := range norm.GetSizeNorms() {
					got = append(got, row.GetConsumption().GetValue())
				}
				if len(tc.want.sizeNorms) > 0 {
					require.Equal(t, tc.want.sizeNorms, got)
				}

				unit, ccy := ComputeTechCardUnitCost(card, CostingFx{})
				require.True(t, unit.Valid)
				require.Equal(t, "EUR", ccy)
				require.Equal(t, tc.want.unit, unit.Decimal.String(), "style figure")

				cw, _ := ComputeColorwayUnitCost(card, 55, CostingFx{})
				require.True(t, cw.Valid)
				require.Equal(t, tc.want.unit, cw.Decimal.String(), "colourway figure")

				onS1, _ := ComputeTechCardUnitCostOnSize(card, CostingFx{}, decimal.NullDecimal{}, 1)
				onS2, _ := ComputeTechCardUnitCostOnSize(card, CostingFx{}, decimal.NullDecimal{}, 2)
				require.Equal(t, tc.want.onS1, onS1.Decimal.String(), "cost on the first size")
				require.Equal(t, tc.want.onS2, onS2.Decimal.String(), "cost on the second size")

				plan := ComputeProductionRunMaterialPlan(cutNormRun(), card, nil, nil, cutNormArticles(), nil)
				require.Empty(t, cutNormBlockerKeys(plan))
				row := cutPlanRow(t, plan, 200)
				require.Equal(t, tc.want.before, row.GetRequiredBeforeGrossup().GetValue(), "plan before wastage")
				require.Equal(t, tc.want.required, row.GetRequired().GetValue(), "plan with wastage")

				fabric := cutPlanRow(t, plan, 100)
				require.Equal(t, "40", fabric.GetRequiredBeforeGrossup().GetValue())
				require.Equal(t, "43.2", fabric.GetRequired().GetValue())
			})
		}
	}
}

// Unknown is never 0. The size that has a length still gets its number, the one that has none blocks the
// plan by name, and the cost refuses to be a number anyone can seed, the way any uncostable line does.
func TestCutStripUnknownLengthIsNoNumber(t *testing.T) {
	for _, section := range []entity.TechCardBomSection{entity.BomSectionHardware, entity.BomSectionTrim} {
		t.Run(string(section), func(t *testing.T) {
			strip := cutNormStrip(entity.CutStripModeFromSeam)
			cutNormSeams(&strip, map[int]string{1: "700"})
			card := cutNormCard(section, "")
			cutNormApply(card, strip)

			norm := ConvertEntityTechCardToPb(card, CostingFx{Base: "EUR"}).GetCutStripNorms()[0]
			require.False(t, norm.GetComplete())
			require.Len(t, norm.GetSizeNorms(), 1)
			require.Equal(t, int32(1), norm.GetSizeNorms()[0].GetSizeId())

			unit, _ := ComputeTechCardUnitCost(card, CostingFx{})
			require.False(t, unit.Valid, "a range average over a missing size is not a cost")
			onS1, _ := ComputeTechCardUnitCostOnSize(card, CostingFx{}, decimal.NullDecimal{}, 1)
			require.Equal(t, "23.03", onS1.Decimal.String(), "the size with a length is still priced")
			onS2, _ := ComputeTechCardUnitCostOnSize(card, CostingFx{}, decimal.NullDecimal{}, 2)
			require.False(t, onS2.Valid, "the size with no length is not priced at zero")

			plan := ComputeProductionRunMaterialPlan(cutNormRun(), card, nil, nil, cutNormArticles(), nil)
			require.Contains(t, cutNormBlockerKeys(plan), entity.MaterialPlanBlockerNoNorm)
			require.Equal(t, "6.5", cutPlanRow(t, plan, 200).GetRequiredBeforeGrossup().GetValue(),
				"only the size that has a length is in the sum")
			require.Equal(t, "43.2", cutPlanRow(t, plan, 100).GetRequired().GetValue())
		})
	}
	// A stored row that says nothing is the same as no row: the size is in the range, the client has not
	// sent its seam yet, and a reader must not see a length of 0 and call the size cheap.
	t.Run("a size row with no seam", func(t *testing.T) {
		strip := cutNormStrip(entity.CutStripModeFromSeam)
		cutNormSeams(&strip, map[int]string{1: "700"})
		strip.Sizes = append(strip.Sizes, entity.TechCardCutStripSize{SizeId: 2})
		card := cutNormCard(entity.BomSectionHardware, "")
		cutNormApply(card, strip)

		norm := ConvertEntityTechCardToPb(card, CostingFx{Base: "EUR"}).GetCutStripNorms()[0]
		require.False(t, norm.GetComplete())
		require.Len(t, norm.GetSizeNorms(), 1)
		onS2, _ := ComputeTechCardUnitCostOnSize(card, CostingFx{}, decimal.NullDecimal{}, 2)
		require.False(t, onS2.Valid, "the size with no seam is not priced at zero")
		plan := ComputeProductionRunMaterialPlan(cutNormRun(), card, nil, nil, cutNormArticles(), nil)
		require.Contains(t, cutNormBlockerKeys(plan), entity.MaterialPlanBlockerNoNorm)
	})
	t.Run("nothing known at all", func(t *testing.T) {
		card := cutNormCard(entity.BomSectionHardware, "")
		cutNormApply(card, cutNormStrip(entity.CutStripModeFromSeam))
		unit, _ := ComputeTechCardUnitCost(card, CostingFx{})
		require.False(t, unit.Valid)
		plan := ComputeProductionRunMaterialPlan(cutNormRun(), card, nil, nil, cutNormArticles(), nil)
		require.Contains(t, cutNormBlockerKeys(plan), entity.MaterialPlanBlockerNoNorm)
	})
}

func TestCutStripCardWithNoColourwaysStillHasItsNorm(t *testing.T) {
	strip := cutNormStrip(entity.CutStripModeFromSeam)
	cutNormSeams(&strip, map[int]string{1: "700", 2: "800"})
	card := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	card.Colorways = nil
	cutNormApply(card, strip)

	require.True(t, card.BomItems[1].CutToLength)
	require.Empty(t, card.Colorways)
	pb := ConvertEntityTechCardToPb(card, CostingFx{Base: "EUR"})
	require.Len(t, pb.GetCutStripNorms(), 1)
	require.Equal(t, cutNormStripLine, pb.GetCutStripNorms()[0].GetBomLineKey())
	require.Len(t, pb.GetCutStripNorms()[0].GetSizeNorms(), 2)
	require.Equal(t, int32(650), pb.GetCutStrips()[0].GetSizes()[0].GetCutMm())

	unit, _ := ComputeTechCardUnitCost(card, CostingFx{})
	require.False(t, unit.Valid, "no colourway, nothing to cost: the same as any card with no colourways")
	require.NotNil(t, ComputeProductionRunMaterialPlan(cutNormRun(), card, nil, nil, cutNormArticles(), nil))
}

// A number typed on the line or on its row is the trap on a hardware line: the countable path would multiply it.
func TestCutStripLineIgnoresTypedNumbers(t *testing.T) {
	strip := cutNormStrip(entity.CutStripModeOneLength)
	strip.OneLengthMm = sql.NullInt32{Int32: 500, Valid: true}

	plain := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	cutNormApply(plain, strip)
	want, _ := ComputeTechCardUnitCost(plain, CostingFx{})
	require.Equal(t, "22.7", want.Decimal.String())

	typed := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	typed.BomItems[1].QtyPerGarment = nd("5")
	typed.BomItems[1].SpareQty = nd("1")
	typed.Colorways[0].Usages = append(typed.Colorways[0].Usages,
		entity.TechCardColorwayUsage{
			BomItemId: sql.NullInt64{Int64: 502, Valid: true}, Consumption: nd("12"), Quantity: nd("7"),
			ConsumptionSource: nstr(entity.ConsumptionSourceManual),
		},
		entity.TechCardColorwayUsage{BomItemId: sql.NullInt64{Int64: 502, Valid: true}, Quantity: nd("3")},
	)
	cutNormApply(typed, strip)
	got, _ := ComputeTechCardUnitCost(typed, CostingFx{})
	require.Equal(t, want.Decimal.String(), got.Decimal.String())

	plan := ComputeProductionRunMaterialPlan(cutNormRun(), typed, nil, nil, cutNormArticles(), nil)
	require.Equal(t, "10", cutPlanRow(t, plan, 200).GetRequiredBeforeGrossup().GetValue())
}

// The same hardware line with no strip stays on the countable path, so nothing moved for old cards.
func TestLineWithNoStripKeepsItsOldNumbers(t *testing.T) {
	card := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	card.BomItems[1].QtyPerGarment = nd("5")
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: sql.NullInt64{Int64: 502, Valid: true}})
	cutNormApply(card)
	require.False(t, card.BomItems[1].CutToLength)
	unit, _ := ComputeTechCardUnitCost(card, CostingFx{})
	require.Equal(t, "31.6", unit.Decimal.String(), "21.6 + 5 x 2: a count takes no wastage")
}

// Fabric numbers do not move when a strip appears, in any reader. The strip's own cost is the only delta.
func TestFabricNumbersUnchangedByAStrip(t *testing.T) {
	before := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	before.BomItems = before.BomItems[:1]
	beforeUnit, _ := ComputeTechCardUnitCost(before, CostingFx{})
	beforePlan := ComputeProductionRunMaterialPlan(cutNormRun(), before, nil, nil, cutNormArticles(), nil)
	require.Equal(t, "21.6", beforeUnit.Decimal.String())

	strip := cutNormStrip(entity.CutStripModeFromSeam)
	cutNormSeams(&strip, map[int]string{1: "700", 2: "800"})
	after := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	cutNormApply(after, strip)

	afterUnit, _ := ComputeTechCardUnitCost(after, CostingFx{})
	require.Equal(t, "23.13", afterUnit.Decimal.String(), "21.6 + 1.529, in whole cents")

	afterPlan := ComputeProductionRunMaterialPlan(cutNormRun(), after, nil, nil, cutNormArticles(), nil)
	b, a := cutPlanRow(t, beforePlan, 100), cutPlanRow(t, afterPlan, 100)
	require.Equal(t, b.GetRequired().GetValue(), a.GetRequired().GetValue())
	require.Equal(t, b.GetRequiredBeforeGrossup().GetValue(), a.GetRequiredBeforeGrossup().GetValue())

	fabricRow := after.Colorways[0].Usages[0]
	require.Equal(t, "2", fabricRow.Consumption.Decimal.String())
	require.Equal(t, entity.ConsumptionSourceManual, fabricRow.ConsumptionSource.String)
	require.False(t, fabricRow.IsCutLengthNorm())

	beforeEstimate := ComputeStyleCostEstimate(before, 1, nil, CostingFx{Base: "EUR"})
	afterEstimate := ComputeStyleCostEstimate(after, 1, nil, CostingFx{Base: "EUR"})
	require.Equal(t, "21.60", beforeEstimate.GetUnitCostBase().GetValue())
	require.Equal(t, "23.13", afterEstimate.GetUnitCostBase().GetValue())
}

// A colourway with no recipe inherits the style's price. The made-up norm row must not read as a recipe of
// its own, or the colourway would lose the fabric and be priced at the strip alone.
func TestCutStripRowIsNotARecipeOfItsOwn(t *testing.T) {
	strip := cutNormStrip(entity.CutStripModeOneLength)
	strip.OneLengthMm = sql.NullInt32{Int32: 500, Valid: true}
	card := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	card.Colorways = append(card.Colorways, entity.TechCardColorway{
		Id: 2, Name: "white", ProductId: sql.NullInt32{Int32: 66, Valid: true},
	})
	cutNormApply(card, strip)

	require.Len(t, card.Colorways[1].Usages, 1, "every colourway gets the norm")
	black, _ := ComputeColorwayUnitCost(card, 55, CostingFx{})
	white, _ := ComputeColorwayUnitCost(card, 66, CostingFx{})
	require.Equal(t, "22.7", black.Decimal.String())
	require.Equal(t, "22.7", white.Decimal.String(), "inherited, not 1.1")
}

// A release freezes the card as read. The frozen reader has no strips to resolve from, so the row's source
// has to keep a hardware line off the countable path, or a release would price the line by its typed qty.
func TestCutStripFrozenInARelease(t *testing.T) {
	strip := cutNormStrip(entity.CutStripModeFromSeam)
	cutNormSeams(&strip, map[int]string{1: "700", 2: "800"})
	card := cutNormCard(entity.BomSectionHardware, string(entity.BomKindChain))
	card.BomItems[1].QtyPerGarment = nd("5")
	card.Colorways[0].Id = 55                // a live colourway's id is its product id, and the frozen figures are keyed by it
	card.LinkedMaterials = cutNormArticles() // the fabric's frozen coefficient is read from its article
	cutNormApply(card, strip)

	live, _ := ComputeTechCardUnitCost(card, CostingFx{})
	require.Equal(t, "23.13", live.Decimal.String())

	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, card))
	require.NotNil(t, costs)
	require.Equal(t, "23.13", costs.UnitCostByColorway[55].String(), "the frozen figure")
	require.NotNil(t, costs.CostingCard)

	// A run line is priced on its own size from the frozen inputs, not 21.6 + 5 x 2.
	onS1, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 1, 10)})
	onS2, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 2, 10)})
	require.Equal(t, "23.03", onS1.Decimal.String())
	require.Equal(t, "23.23", onS2.Decimal.String())
}

func TestCutStripFrozenRollUsageIsAuthoritative(t *testing.T) {
	strip := cutNormStrip(entity.CutStripModeOneLength)
	strip.OneLengthMm = sql.NullInt32{Int32: 1200, Valid: true}
	card := cutNormCard(entity.BomSectionLining, "")
	card.BomItems[1].FabricWidth = nd("140")
	card.Colorways[0].Id = 55
	card.LinkedMaterials = cutNormArticles()
	cutNormApply(card, strip)
	require.Equal(t, "0.026", card.Colorways[0].Usages[1].Consumption.Decimal.String())

	snap := releaseBlob(t, card)
	for _, raw := range snap.GetCutStrips() {
		raw.WidthMm = &pb_decimal.Decimal{Value: "700"}
		raw.OneLengthMm = cutNormI32(9999)
	}
	for _, line := range snap.GetTechCard().GetBomItems() {
		if line.GetLineKey() == cutNormStripLine {
			line.FabricWidth = &pb_decimal.Decimal{Value: "70"}
		}
	}
	frozen := releaseCostingCardFromSnapshot(snap)
	require.Len(t, frozen.Colorways, 1)
	require.Len(t, frozen.Colorways[0].Usages, 2)
	usage := frozen.Colorways[0].Usages[1]
	require.True(t, usage.IsCutLengthNorm())
	require.Equal(t, "0.026", usage.Consumption.Decimal.String(),
		"release cost keeps the frozen cut_length row instead of rebuilding from geometry")
}

// Readiness asks about roll goods. A hardware or trim strip line gets no row there at all, and a strip on a
// roll line says where the number came from instead of sending someone after a marker.
func TestCutStripReadiness(t *testing.T) {
	findings := func(res RunReadinessResult, bomKey string) []entity.RunReadinessFinding {
		out := []entity.RunReadinessFinding{}
		for _, cw := range res.Report.Colorways {
			for _, f := range cw.Findings {
				if f.Target.BomLineKey == bomKey {
					out = append(out, f)
				}
			}
		}
		return out
	}
	build := func(section entity.TechCardBomSection, purpose string, strip entity.TechCardCutStrip) *entity.TechCard {
		card := rrHealthyCard()
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{
			Id: 502, LineKey: cutNormStripLine, Section: section, Name: "rubber",
			Purpose:    sql.NullString{String: purpose, Valid: purpose != ""},
			MaterialId: sql.NullInt64{Int64: rrMaterial, Valid: true}, Unit: nstr("m"), FabricWidth: nd("140"),
		})
		cutNormApply(card, strip)
		return card
	}
	known := cutNormStrip(entity.CutStripModeOneLength)
	known.OneLengthMm = sql.NullInt32{Int32: 500, Valid: true}
	unknown := cutNormStrip(entity.CutStripModeFromSeam)

	for name, section := range map[string]entity.TechCardBomSection{"hardware": entity.BomSectionHardware, "trim": entity.BomSectionTrim} {
		t.Run(name+" has no norm rows", func(t *testing.T) {
			res := ComputeProductionRunReadiness(rrInput(build(section, "", known)))
			for _, f := range findings(res, cutNormStripLine) {
				require.NotEqual(t, entity.RunReadinessKeyNormProvenance, f.Key)
			}
		})
	}
	t.Run("a roll line says the norm is computed", func(t *testing.T) {
		res := ComputeProductionRunReadiness(rrInput(build(entity.BomSectionLining, "binding", known)))
		got := findings(res, cutNormStripLine)
		require.NotEmpty(t, got)
		provenance := 0
		for _, f := range got {
			if f.Key == entity.RunReadinessKeyNormProvenance {
				provenance++
				require.Equal(t, entity.RunReadinessOK, f.Severity)
			}
			require.NotEqual(t, entity.RunReadinessKeyNormConditionsRecorded, f.Key, "no marker, no conditions")
		}
		require.Equal(t, 1, provenance)
	})
	t.Run("an unknown length is not reported as manual", func(t *testing.T) {
		res := ComputeProductionRunReadiness(rrInput(build(entity.BomSectionLining, "binding", unknown)))
		for _, f := range findings(res, cutNormStripLine) {
			require.NotEqual(t, entity.RunReadinessKeyNormProvenance, f.Key)
		}
	})
}

// The acceptance of the spec: the cut sheet does not change. Rule (b) infers the fabric of a piece when the
// colourway has exactly one roll slot, and a strip on a roll line must not make that two.
func TestCutStripLeavesTheCutPlanAlone(t *testing.T) {
	run := func() *entity.ProductionRun {
		return cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10})
	}
	withStrip := func(section entity.TechCardBomSection) *entity.TechCard {
		card := cutPlanSingleFabricCard()
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{
			Id: 503, LineKey: cutNormStripLine, Name: "binding", Section: section,
			MaterialId: cutMid(200), Unit: nstr("m"),
		})
		strip := cutNormStrip(entity.CutStripModeOneLength)
		strip.BomItemId = 503
		strip.OneLengthMm = sql.NullInt32{Int32: 500, Valid: true}
		cutNormApply(card, strip)
		return card
	}
	want := ComputeProductionRunCutPlan(run(), cutPlanSingleFabricCard(), nil)
	require.Len(t, want.Rows, 1)
	require.True(t, want.Rows[0].SlotInferred)

	for name, section := range map[string]entity.TechCardBomSection{
		"lining":      entity.BomSectionLining,
		"interlining": entity.BomSectionInterlining,
		"insulation":  entity.BomSectionInsulation,
		"hardware":    entity.BomSectionHardware,
	} {
		t.Run(name, func(t *testing.T) {
			got := ComputeProductionRunCutPlan(run(), withStrip(section), nil)
			require.Empty(t, got.Blockers)
			require.Len(t, got.Rows, 1)
			require.True(t, got.Rows[0].SlotInferred)
			require.Equal(t, want.Rows[0].BomItemId, got.Rows[0].BomItemId)
			require.Equal(t, want.Rows[0].MaterialId, got.Rows[0].MaterialId)
			require.Equal(t, want.Rows[0].GetSlotName(), got.Rows[0].GetSlotName())
		})
	}
	t.Run("a frozen release reads the same", func(t *testing.T) {
		frozen := CutSpecCardFromReleaseSnapshot(releaseBlob(t, withStrip(entity.BomSectionLining)))
		got := ComputeProductionRunCutPlan(run(), frozen, nil)
		require.Empty(t, got.Blockers)
		require.Len(t, got.Rows, 1)
		require.True(t, got.Rows[0].SlotInferred)
	})
	// A card with no colourways infers its slot from the BOM itself, by a walk of its own.
	t.Run("a card with no colourways infers from the BOM", func(t *testing.T) {
		auxCard := func(withLine bool) *entity.TechCard {
			card := &entity.TechCard{Id: 7}
			card.BomItems = []entity.TechCardBomItem{{
				Id: 501, LineKey: "BOM-MAIN", Name: "cotton", Section: entity.BomSectionFabric, MaterialId: cutMid(100),
			}}
			card.Pieces = []entity.TechCardPiece{{Id: 11, LineKey: "P-BODY", Name: "body", PiecesPerGarment: 1}}
			card.OutputVariants = []entity.TechCardOutputVariant{{
				TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{Id: 9, ColorCode: "BLK", MaterialId: 700, Active: true},
				ColorName:                   "black",
			}}
			if !withLine {
				return card
			}
			card.BomItems = append(card.BomItems, entity.TechCardBomItem{
				Id: 503, LineKey: cutNormStripLine, Name: "binding", Section: entity.BomSectionLining,
				MaterialId: cutMid(200), Unit: nstr("m"),
			})
			strip := cutNormStrip(entity.CutStripModeOneLength)
			strip.BomItemId = 503
			strip.OneLengthMm = sql.NullInt32{Int32: 500, Valid: true}
			cutNormApply(card, strip)
			return card
		}
		auxRun := func() *entity.ProductionRun {
			return cutPlanRun(entity.ProductionRunLine{OutputVariantId: cutPid(9), SizeId: 0, PlannedQty: 200})
		}
		base := ComputeProductionRunCutPlan(auxRun(), auxCard(false), nil)
		require.Len(t, base.Rows, 1)
		require.True(t, base.Rows[0].SlotInferred)

		got := ComputeProductionRunCutPlan(auxRun(), auxCard(true), nil)
		require.Empty(t, got.Blockers)
		require.Len(t, got.Rows, 1)
		require.True(t, got.Rows[0].SlotInferred)
		require.Equal(t, int32(100), got.Rows[0].MaterialId, "the piece is cut from the fabric, not from the strip's line")
	})
}
