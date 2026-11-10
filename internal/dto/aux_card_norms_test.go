package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// Aux card norms (0456): an auxiliary card has no colourways, so its BOM is its recipe and the norm
// per output unit lives on the line. These tests run the card-13 shape (a kit bag: fabric 0.62 m with
// 10% wastage, thread 12 m stocked in cones, rope 1.1 m, two counted stoppers) through the plan, the
// costing, the wire and the release projection.

const (
	auxFabricKey  = "01J0AXFABR1C00000000000001"
	auxThreadKey  = "01J0AXTHREAD00000000000002"
	auxRopeKey    = "01J0AXR0PE0000000000000003"
	auxStopperKey = "01J0AXST0PPER0000000000004"
	auxStripKey   = "01J0AXSTR1P000000000000005"
)

func auxMid(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

func auxPbDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func auxNormsCard() *entity.TechCard {
	card := &entity.TechCard{Id: 13}
	card.StyleNumber = nstr("AUX-13")
	card.Name = "Spare kit bag"
	card.Purpose = entity.TechCardPurposeAuxiliary
	card.AuxMadeBy = nstr(entity.AuxMadeByUs)
	card.BomItems = []entity.TechCardBomItem{
		{Id: 1, LineKey: auxFabricKey, Name: "Shell", Section: entity.BomSectionFabric, Unit: nstr("m"),
			MaterialId: auxMid(100), UnitPrice: nd("10"), Currency: nstr("EUR"), WastagePercent: nd("10"),
			ConsumptionPerUnit: nd("0.62")},
		{Id: 2, LineKey: auxThreadKey, Name: "Thread", Section: entity.BomSectionThread, Unit: nstr("m"),
			MaterialId: auxMid(300), UnitPrice: nd("0.01"), Currency: nstr("EUR"), ConsumptionPerUnit: nd("12")},
		{Id: 3, LineKey: auxRopeKey, Name: "Rope", Section: entity.BomSectionTrim, Unit: nstr("m"),
			MaterialId: auxMid(400), UnitPrice: nd("2"), Currency: nstr("EUR"), ConsumptionPerUnit: nd("1.1")},
		{Id: 4, LineKey: auxStopperKey, Name: "Stopper", Section: entity.BomSectionHardware, Unit: nstr("pcs"),
			MaterialId: auxMid(500), UnitPrice: nd("0.5"), Currency: nstr("EUR"), QtyPerGarment: nd("2")},
	}
	card.Costing = &entity.TechCardCosting{Currency: nstr("EUR"), CmtCost: nd("3")}
	return card
}

func auxPlanLinked() map[int]entity.MaterialWithPrice {
	mat := func(id int, name, unit string) entity.MaterialWithPrice {
		return entity.MaterialWithPrice{Material: entity.Material{Id: id, MaterialInsert: entity.MaterialInsert{Name: name, Unit: nstr(unit)}}}
	}
	thread := mat(300, "Mara 100", "cone")
	thread.ThreadAttr = &entity.MaterialThreadAttr{LengthPerConeM: nd2("5000")}
	return map[int]entity.MaterialWithPrice{
		100: mat(100, "Nylon shell", "m"),
		300: thread,
		400: mat(400, "Rope 4 mm", "m"),
		500: mat(500, "Stopper", "pcs"),
	}
}

// Two lines, one naming an output variant and one naming none: an aux card has one recipe, so both pool.
func auxRun() *entity.ProductionRun {
	return &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 13, Lines: []entity.ProductionRunLine{cwLine(0, 0, 100), cwLine(77, 0, 50)},
	}}
}

func auxPlanRequired(t *testing.T, card *entity.TechCard) (map[int32]string, map[int32]string) {
	t.Helper()
	resp := ComputeProductionRunMaterialPlan(auxRun(), card, nil, nil, auxPlanLinked(), nil)
	req, unit := map[int32]string{}, map[int32]string{}
	for _, r := range resp.Rows {
		req[r.MaterialId] = r.Required.Value
		unit[r.MaterialId] = r.Unit
	}
	return req, unit
}

func TestAuxPlanCard13(t *testing.T) {
	card := auxNormsCard()
	resp := ComputeProductionRunMaterialPlan(auxRun(), card, nil, nil, auxPlanLinked(), nil)
	require.Empty(t, resp.Blockers)
	req, unit := auxPlanRequired(t, card)
	require.Equal(t, map[int32]string{
		100: "102.3", // 0.62 m × 150 × 1.10 wastage
		300: "0.36",  // 12 m × 150 = 1800 m ÷ 5000 m per cone
		400: "165",   // 1.1 m × 150
		500: "300",   // 2 pcs × 150, counted: no gross-up
	}, req)
	require.Equal(t, "cone", unit[300], "thread is planned in its stock unit")

	// The run's actual wastage overrides the line's estimate, as on a colourway.
	run := auxRun()
	run.ActualWastagePercent = nd("20")
	resp = ComputeProductionRunMaterialPlan(run, card, nil, nil, auxPlanLinked(), nil)
	for _, r := range resp.Rows {
		if r.MaterialId == 100 {
			require.Equal(t, "111.6", r.Required.Value, "0.62 × 150 × 1.20")
		}
	}
}

func TestAuxPlanNullNormIsANoNormBlocker(t *testing.T) {
	card := auxNormsCard()
	card.BomItems[2].ConsumptionPerUnit = decimal.NullDecimal{}
	resp := ComputeProductionRunMaterialPlan(auxRun(), card, nil, nil, auxPlanLinked(), nil)
	require.Len(t, resp.Blockers, 1)
	b := resp.Blockers[0]
	require.EqualValues(t, 3, b.BomItemId)
	require.EqualValues(t, 0, b.ColorwayId)
	require.Equal(t, "Spare kit bag", b.ColorwayName, "the blocker names the card")
	require.Equal(t, entity.MaterialPlanBlockerNoNorm, b.Key)
	require.EqualValues(t, 150, b.PlannedQty)
	req, _ := auxPlanRequired(t, card)
	require.NotContains(t, req, int32(400), "no norm is never a zero row")
	require.Len(t, req, 3)
}

func TestAuxPlanSupplierPlansNoMaterials(t *testing.T) {
	card := auxNormsCard()
	card.AuxMadeBy = nstr(entity.AuxMadeBySupplier)
	resp := ComputeProductionRunMaterialPlan(auxRun(), card, nil, nil, auxPlanLinked(), nil)
	require.Empty(t, resp.Rows, "bought finished: nothing reserved even with norms filled")
	require.Empty(t, resp.Blockers)
	require.Contains(t, resp.Caveats, "bought from a supplier — no materials planned")
}

func TestAuxCostingIncludesMaterials(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := auxNormsCard()
	// fabric 0.62 × 1.1 × 10 = 6.82, thread 12 × 0.01 = 0.12, rope 1.1 × 2 = 2.2, stoppers 2 × 0.5 = 1,
	// CMT 3.
	pb := techCardCostingToPb(card, fx)
	require.NotNil(t, pb)
	require.False(t, pb.HasUnpriced)
	require.Equal(t, "13.14", pb.UnitCost.Value)
	unit, ccy := ComputeTechCardUnitCost(card, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "13.14", unit.Decimal.String())
	require.Equal(t, "EUR", ccy)

	br, ok := ComputeTechCardCostBreakdownBase(card, fx)
	require.True(t, ok)
	require.Equal(t, "3", br.Cmt.String())
	sum := br.Materials.Add(br.Cmt).Add(br.Hardware).Add(br.Packaging).Add(br.Logistics).Add(br.Overhead)
	require.Equal(t, "13.14", sum.String(), "the breakdown adds up to the unit cost")

	// A run on the card costs on the same recipe, whichever line it is.
	planned, _ := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{}, auxRun().Lines)
	require.Equal(t, "13.14", planned.Decimal.String())
	planned, _ = ComputeProductionRunPlannedUnitCost(card, fx, nd("20"), auxRun().Lines)
	// The run's wastage replaces every measured line's estimate, as on a colourway: fabric 7.44, thread
	// 0.144, rope 2.64; the stoppers are counted and take none.
	require.Equal(t, "14.22", planned.Decimal.String())
}

func TestAuxCostingNullNormIsUnpriced(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := auxNormsCard()
	card.BomItems[0].ConsumptionPerUnit = decimal.NullDecimal{}
	pb := techCardCostingToPb(card, fx)
	require.NotNil(t, pb)
	require.True(t, pb.HasUnpriced, "a line with no norm is a hole, not a zero")
	unit, _ := ComputeTechCardUnitCost(card, fx)
	require.False(t, unit.Valid)
	_, ok := ComputeTechCardCostBreakdownBase(card, fx)
	require.False(t, ok)
	_, _, reason := ComputeProductionRunPlannedUnitCostWithReason(card, fx, decimal.NullDecimal{}, auxRun().Lines)
	require.Contains(t, reason, "no norm per unit")
}

func TestAuxCostingSupplierIsManualArticlesOnly(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := auxNormsCard()
	card.AuxMadeBy = nstr(entity.AuxMadeBySupplier)
	unit, _ := ComputeTechCardUnitCost(card, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "3", unit.Decimal.String(), "the supplier's price is the cost; norms are not costed")
}

func auxNormsPb() *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber: "AUX-13", Name: "Spare kit bag",
		Purpose: pb_common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY, OutputMaterialId: 42,
		AuxMadeBy: pb_common.AuxMadeBy_AUX_MADE_BY_US,
		BomItems: []*pb_common.TechCardBomItem{
			{LineKey: auxRopeKey, Name: "Rope", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_TRIM,
				Unit: "m", ConsumptionPerUnit: auxPbDec("1.1")},
			{LineKey: auxStopperKey, Name: "Stopper", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
				Unit: "pcs", QtyPerGarment: auxPbDec("2")},
		},
	}
}

func TestAuxNormsWire(t *testing.T) {
	got, err := ConvertPbTechCardInsertToEntity(auxNormsPb())
	require.NoError(t, err)
	require.Equal(t, "1.1", got.BomItems[0].ConsumptionPerUnit.Decimal.String())
	require.False(t, got.BomItems[0].ConsumptionPerUnitOmitted)
	require.False(t, got.BomItems[1].ConsumptionPerUnit.Valid)
	require.True(t, got.BomItems[1].ConsumptionPerUnitOmitted, "omitted keeps the stored value")
	require.Equal(t, nstr(entity.AuxMadeByUs), got.AuxMadeBy)
	require.False(t, got.AuxMadeByOmitted)

	t.Run("an empty decimal clears", func(t *testing.T) {
		pb := auxNormsPb()
		pb.BomItems[0].ConsumptionPerUnit = auxPbDec("")
		got, err := ConvertPbTechCardInsertToEntity(pb)
		require.NoError(t, err)
		require.False(t, got.BomItems[0].ConsumptionPerUnit.Valid)
		require.False(t, got.BomItems[0].ConsumptionPerUnitOmitted)
	})
	t.Run("aux_made_by unspecified keeps the stored value", func(t *testing.T) {
		pb := auxNormsPb()
		pb.AuxMadeBy = pb_common.AuxMadeBy_AUX_MADE_BY_UNKNOWN
		got, err := ConvertPbTechCardInsertToEntity(pb)
		require.NoError(t, err)
		require.False(t, got.AuxMadeBy.Valid)
		require.True(t, got.AuxMadeByOmitted)
	})
	t.Run("supplier", func(t *testing.T) {
		pb := auxNormsPb()
		pb.AuxMadeBy = pb_common.AuxMadeBy_AUX_MADE_BY_SUPPLIER
		got, err := ConvertPbTechCardInsertToEntity(pb)
		require.NoError(t, err)
		require.True(t, got.AuxBoughtFinished())
	})
	t.Run("refused on a sellable card", func(t *testing.T) {
		pb := auxNormsPb()
		pb.Purpose, pb.OutputMaterialId, pb.AuxMadeBy = pb_common.TechCardPurpose_TECH_CARD_PURPOSE_UNKNOWN, 0, pb_common.AuxMadeBy_AUX_MADE_BY_UNKNOWN
		_, err := ConvertPbTechCardInsertToEntity(pb)
		cutRequireViolation(t, err, "bom_items[0].consumption_per_unit", "aux_only")

		pb.BomItems[0].ConsumptionPerUnit = nil
		_, err = ConvertPbTechCardInsertToEntity(pb)
		require.NoError(t, err, "a sellable card that sends no norm is fine")

		pb.AuxMadeBy = pb_common.AuxMadeBy_AUX_MADE_BY_US
		_, err = ConvertPbTechCardInsertToEntity(pb)
		require.Error(t, err, "aux_made_by is for auxiliary cards only")
	})
	t.Run("refused on a counted section", func(t *testing.T) {
		pb := auxNormsPb()
		pb.BomItems[1].ConsumptionPerUnit = auxPbDec("2")
		_, err := ConvertPbTechCardInsertToEntity(pb)
		cutRequireViolation(t, err, "bom_items[1].consumption_per_unit", "measured_only")
	})
	t.Run("refused on a line cut to length", func(t *testing.T) {
		pb := auxNormsPb()
		one := int32(550)
		pb.CutStripsAware = true
		pb.CutStrips = []*pb_common.TechCardCutStrip{{
			LineKey: auxStripKey, BomLineKey: auxRopeKey, LengthMode: entity.CutStripModeOneLength, OneLengthMm: &one, Qty: 2,
		}}
		_, err := ConvertPbTechCardInsertToEntity(pb)
		cutRequireViolation(t, err, "bom_items[0].consumption_per_unit", "cut_to_length")

		pb.BomItems[0].ConsumptionPerUnit = nil
		_, err = ConvertPbTechCardInsertToEntity(pb)
		require.NoError(t, err, "the strips give that line its norm")
	})
	for _, bad := range []string{"-1", "1.6001", "10000000"} {
		t.Run("refused "+bad, func(t *testing.T) {
			pb := auxNormsPb()
			pb.BomItems[0].ConsumptionPerUnit = auxPbDec(bad)
			_, err := ConvertPbTechCardInsertToEntity(pb)
			require.Error(t, err)
			require.Contains(t, err.Error(), "bom_items[0].consumption_per_unit")
		})
	}
}

func TestAuxNormsToPb(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := auxNormsCard()
	card.BomItems[2].ConsumptionPerUnit = decimal.NullDecimal{}
	pb := ConvertEntityTechCardToPb(card, fx).GetTechCard()
	require.Equal(t, pb_common.AuxMadeBy_AUX_MADE_BY_US, pb.GetAuxMadeBy())
	require.Equal(t, "0.62", pb.GetBomItems()[0].GetConsumptionPerUnit().GetValue())
	require.Nil(t, pb.GetBomItems()[2].GetConsumptionPerUnit(), "NULL stays absent")

	back, err := ConvertPbTechCardInsertToEntity(pb)
	require.NoError(t, err)
	require.Equal(t, "0.62", back.BomItems[0].ConsumptionPerUnit.Decimal.String())
	require.True(t, back.BomItems[2].ConsumptionPerUnitOmitted)
	require.Equal(t, nstr(entity.AuxMadeByUs), back.AuxMadeBy)

	card.AuxMadeBy = nstr(entity.AuxMadeBySupplier)
	require.Equal(t, pb_common.AuxMadeBy_AUX_MADE_BY_SUPPLIER, ConvertEntityTechCardToPb(card, fx).GetTechCard().GetAuxMadeBy())
	card.AuxMadeBy = sql.NullString{}
	require.Equal(t, pb_common.AuxMadeBy_AUX_MADE_BY_US, ConvertEntityTechCardToPb(card, fx).GetTechCard().GetAuxMadeBy(),
		"NULL on an aux card reads as made by us")
	card.Purpose = entity.TechCardPurposeSellable
	require.Equal(t, pb_common.AuxMadeBy_AUX_MADE_BY_UNKNOWN, ConvertEntityTechCardToPb(card, fx).GetTechCard().GetAuxMadeBy())
}

func TestAuxNormDigest(t *testing.T) {
	gold := materialsGoldCard()
	require.Equal(t, materialsGoldDigestHex, materialsDigest(gold), "NULL norms leave every fingerprint where it was")
	gold.BomItems[0].ConsumptionPerUnitOmitted = true
	require.Equal(t, materialsGoldDigestHex, materialsDigest(gold))

	set := materialsGoldCard()
	set.BomItems[0].ConsumptionPerUnit = nd("0.62")
	require.NotEqual(t, materialsGoldDigestHex, materialsDigest(set))
	moved := materialsGoldCard()
	moved.BomItems[0].ConsumptionPerUnit = nd("0.63")
	require.NotEqual(t, materialsDigest(set), materialsDigest(moved))
	same := materialsGoldCard()
	same.BomItems[0].ConsumptionPerUnit = nd("0.620")
	require.Equal(t, materialsDigest(set), materialsDigest(same), "scale is not a change")
}

func auxReleaseCard() *entity.TechCard {
	card := auxNormsCard()
	// The release loader links every slot's article; the blob freezes the fabric's coefficient from it.
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{
		100: {Material: entity.Material{Id: 100, MaterialInsert: entity.MaterialInsert{Unit: nstr("m")}}},
	}
	return card
}

func TestAuxReleaseRestorerCarriesTheNorms(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := auxReleaseCard()
	live, _ := ComputeTechCardUnitCost(card, fx)
	require.Equal(t, "13.14", live.Decimal.String())

	snap := releaseBlob(t, card)
	require.Equal(t, "13.14", snap.GetTechCard().GetCosting().GetUnitCost().GetValue(),
		"the frozen scalar carries the materials")
	require.Nil(t, ReleaseFrozenColorwayCosts(snap), "no colourway figures: an aux run on a release prices on that scalar")

	restored := releaseCostingCardFromSnapshot(snap)
	require.True(t, restored.IsAuxiliary())
	require.Equal(t, nstr(entity.AuxMadeByUs), restored.AuxMadeBy)
	require.Equal(t, "0.62", restored.BomItems[0].ConsumptionPerUnit.Decimal.String())
	require.Equal(t, "1.1", restored.BomItems[2].ConsumptionPerUnit.Decimal.String())
	frozen, _ := ComputeTechCardUnitCost(restored, CostingFx{})
	require.Equal(t, live.Decimal.String(), frozen.Decimal.String())
	cell, _ := runCellUnitCost(restored, CostingFx{}, decimal.NullDecimal{}, runMixKey{productID: 77})
	require.Equal(t, live.Decimal.String(), cell.Decimal.String())

	t.Run("a blob older than 0456 has no norm and does not price", func(t *testing.T) {
		old := releaseBlob(t, card)
		for _, b := range old.GetTechCard().GetBomItems() {
			b.ConsumptionPerUnit = nil
		}
		old.GetTechCard().AuxMadeBy = pb_common.AuxMadeBy_AUX_MADE_BY_UNKNOWN
		restored := releaseCostingCardFromSnapshot(old)
		require.False(t, restored.AuxMadeBy.Valid, "NULL reads as made by us")
		unit, _ := ComputeTechCardUnitCost(restored, CostingFx{})
		require.False(t, unit.Valid)
	})
	t.Run("supplier", func(t *testing.T) {
		sup := auxReleaseCard()
		sup.AuxMadeBy = nstr(entity.AuxMadeBySupplier)
		restored := releaseCostingCardFromSnapshot(releaseBlob(t, sup))
		require.True(t, restored.AuxBoughtFinished())
		unit, _ := ComputeTechCardUnitCost(restored, CostingFx{})
		require.Equal(t, "3", unit.Decimal.String())
	})
	t.Run("a one-length strip", func(t *testing.T) {
		strip := auxReleaseCard()
		strip.BomItems[2].ConsumptionPerUnit = decimal.NullDecimal{}
		strip.CutStrips = []entity.TechCardCutStrip{{
			LineKey: auxStripKey, BomLineKey: auxRopeKey, LengthMode: entity.CutStripModeOneLength,
			OneLengthMm: sql.NullInt32{Int32: 550, Valid: true}, Qty: 2,
		}}
		live, _ := ComputeTechCardUnitCost(strip, fx)
		require.Equal(t, "13.14", live.Decimal.String(), "550 mm × 2 = the rope's 1.1 m")
		restored := releaseCostingCardFromSnapshot(releaseBlob(t, strip))
		frozen, _ := ComputeTechCardUnitCost(restored, CostingFx{})
		require.Equal(t, live.Decimal.String(), frozen.Decimal.String())
	})
	t.Run("a roll strip uses the frozen outer norm", func(t *testing.T) {
		strip := auxReleaseCard()
		strip.BomItems[2].Section = entity.BomSectionLining
		strip.BomItems[2].FabricWidth = nd("140")
		strip.BomItems[2].ConsumptionPerUnit = decimal.NullDecimal{}
		strip.CutStrips = []entity.TechCardCutStrip{{
			LineKey: auxStripKey, BomLineKey: auxRopeKey, LengthMode: entity.CutStripModeOneLength,
			OneLengthMm: sql.NullInt32{Int32: 1200, Valid: true}, WidthMm: decimal.RequireFromString("30"), Qty: 1,
		}}
		snap := releaseBlob(t, strip)
		require.Equal(t, "0.026", snap.GetCutStripNorms()[0].GetNorm().GetValue())
		for _, raw := range snap.GetCutStrips() {
			raw.WidthMm = auxPbDec("700")
			raw.OneLengthMm = cutNormI32(9999)
		}
		for _, line := range snap.GetTechCard().GetBomItems() {
			if line.GetLineKey() == auxRopeKey {
				line.FabricWidth = auxPbDec("70")
			}
		}
		restored := releaseCostingCardFromSnapshot(snap)
		require.True(t, restored.BomItems[2].CutToLength)
		require.Equal(t, "0.026", restored.BomItems[2].ConsumptionPerUnit.Decimal.String())
		recipe := entity.AuxRecipe(&restored.TechCardInsert)
		require.Equal(t, "0.026", recipe.Usages[2].Consumption.Decimal.String())
	})
	t.Run("a sellable blob carries no aux fields", func(t *testing.T) {
		sell := releaseBlob(t, auxReleaseCard())
		sell.GetTechCard().Purpose = pb_common.TechCardPurpose_TECH_CARD_PURPOSE_SELLABLE
		restored := releaseCostingCardFromSnapshot(sell)
		require.False(t, restored.IsAuxiliary())
		require.False(t, restored.AuxMadeBy.Valid)
	})
}
