package entity

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func auxNd(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

func auxNs(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// auxCard13 is the shape of card 13: a fabric, a thread and a rope, all measured, plus a counted
// stopper. Made by us.
func auxCard13() *TechCardInsert {
	return &TechCardInsert{
		Name:      "Spare kit bag",
		Purpose:   TechCardPurposeAuxiliary,
		AuxMadeBy: auxNs(AuxMadeByUs),
		BomItems: []TechCardBomItem{
			{Id: 1, LineKey: "01JAUXFABR1C0000000000000A", Section: BomSectionFabric, Unit: auxNs("m"), ConsumptionPerUnit: auxNd("0.62")},
			{Id: 2, LineKey: "01JAUXTHREAD000000000000AB", Section: BomSectionThread, Unit: auxNs("m"), ConsumptionPerUnit: auxNd("12")},
			{Id: 3, LineKey: "01JAUXR0PE00000000000000AC", Section: BomSectionTrim, Unit: auxNs("m"), ConsumptionPerUnit: auxNd("1.1")},
			{Id: 4, LineKey: "01JAUXST0PPER0000000000AD1", Section: BomSectionHardware, Unit: auxNs("pcs"),
				QtyPerGarment: auxNd("2"), ConsumptionPerUnit: auxNd("9")},
		},
	}
}

func TestAuxRecipeMeasuredTakesTheNormCountedTakesTheSlot(t *testing.T) {
	tc := auxCard13()
	r := AuxRecipe(tc)
	require.NotNil(t, r)
	require.Equal(t, "Spare kit bag", r.Name)
	require.Len(t, r.Usages, 4, "one garment-level usage per BOM line")
	for i, u := range r.Usages {
		require.True(t, u.BomItemId.Valid)
		require.Equal(t, int64(tc.BomItems[i].Id), u.BomItemId.Int64)
		require.Equal(t, int32(i), u.BomItemIndex.Int32)
		require.Equal(t, tc.BomItems[i].LineKey, u.BomLineKey)
		require.Zero(t, u.Id, "the recipe is never written")
	}
	require.Equal(t, "0.62", r.Usages[0].Consumption.Decimal.String())
	require.Equal(t, "12", r.Usages[1].Consumption.Decimal.String())
	require.Equal(t, "1.1", r.Usages[2].Consumption.Decimal.String())
	// A counted line carries no number of its own: its slot's qty answers through the pair resolver,
	// and a stray consumption_per_unit on it is never read.
	require.False(t, r.Usages[3].Consumption.Valid)
	bom := &tc.BomItems[3]
	qty := CountablePairRowTotal(CountablePairUsages(r.Usages, bom), &r.Usages[3], bom)
	require.True(t, qty.Valid)
	require.Equal(t, "2", qty.Decimal.String())

	// NULL norm stays NULL: unpriced and a plan blocker downstream, never zero.
	tc.BomItems[2].ConsumptionPerUnit = decimal.NullDecimal{}
	require.False(t, AuxRecipe(tc).Usages[2].Consumption.Valid)
	require.Len(t, AuxRecipe(tc).Usages, 4, "a line with no norm keeps its row")
}

func TestAuxRecipeSellableIsNil(t *testing.T) {
	tc := auxCard13()
	tc.Purpose = TechCardPurposeSellable
	require.Nil(t, AuxRecipe(tc))
	require.Nil(t, AuxRecipe(nil))
}

func TestAuxRecipeSupplierHasNoUsages(t *testing.T) {
	tc := auxCard13()
	tc.AuxMadeBy = auxNs(AuxMadeBySupplier)
	r := AuxRecipe(tc)
	require.NotNil(t, r)
	require.Empty(t, r.Usages, "bought finished: nothing is costed, planned or issued even with norms filled")

	// NULL on an aux card reads as made by us.
	tc.AuxMadeBy = sql.NullString{}
	require.Len(t, AuxRecipe(tc).Usages, 4)
}

func TestAuxRecipeStripsOneLengthFlatGradedNone(t *testing.T) {
	tc := auxCard13()
	rope := &tc.BomItems[2]
	rope.ConsumptionPerUnit = decimal.NullDecimal{}
	tc.CutStrips = []TechCardCutStrip{{
		LineKey: "01JAUXSTR1P0000000000000S1", BomLineKey: rope.LineKey,
		LengthMode: CutStripModeOneLength, OneLengthMm: sql.NullInt32{Int32: 550, Valid: true}, Qty: 2,
	}}
	r := AuxRecipe(tc)
	require.True(t, r.Usages[2].Consumption.Valid, "a one-length strip gives a flat norm")
	require.Equal(t, "1.1", r.Usages[2].Consumption.Decimal.String(), "550 mm × 2")
	require.Equal(t, ConsumptionSourceCutLength, r.Usages[2].ConsumptionSource.String)

	// A graded strip answers per size, and aux has no size: no norm.
	tc.CutStrips = []TechCardCutStrip{{
		LineKey: "01JAUXSTR1P0000000000000S1", BomLineKey: rope.LineKey, LengthMode: CutStripModePerSize, Qty: 1,
		Sizes: []TechCardCutStripSize{{SizeId: 4, ManualCutMm: sql.NullInt32{Int32: 1000, Valid: true}}},
	}}
	r = AuxRecipe(tc)
	require.False(t, r.Usages[2].Consumption.Valid)
	require.Empty(t, r.Usages[2].SizeConsumptions)
	require.Equal(t, "0.62", r.Usages[0].Consumption.Decimal.String(), "other lines keep their norms")
}

func TestValidateAuxBomNorms(t *testing.T) {
	reason := func(t *testing.T, err error, want string) {
		t.Helper()
		var ve *ValidationError
		require.True(t, errors.As(err, &ve), "got %v", err)
		require.Contains(t, err.Error(), want)
	}
	tc := auxCard13()
	tc.BomItems[3].ConsumptionPerUnit = decimal.NullDecimal{}
	require.NoError(t, ValidateAuxBomNorms(tc))

	sellable := auxCard13()
	sellable.Purpose = TechCardPurposeSellable
	sellable.BomItems[3].ConsumptionPerUnit = decimal.NullDecimal{}
	reason(t, ValidateAuxBomNorms(sellable), "bom_items[0].consumption_per_unit")

	counted := auxCard13()
	reason(t, ValidateAuxBomNorms(counted), "bom_items[3].consumption_per_unit")

	cut := auxCard13()
	cut.BomItems[3].ConsumptionPerUnit = decimal.NullDecimal{}
	cut.CutStrips = []TechCardCutStrip{{BomLineKey: " 01jauxr0pe00000000000000ac "}}
	reason(t, ValidateAuxBomNorms(cut), "bom_items[2].consumption_per_unit")

	// Omitted keeps the stored value and is not judged; a clear is always allowed.
	omitted := auxCard13()
	omitted.Purpose = TechCardPurposeSellable
	for i := range omitted.BomItems {
		omitted.BomItems[i].ConsumptionPerUnitOmitted = true
	}
	require.NoError(t, ValidateAuxBomNorms(omitted))
	cleared := auxCard13()
	cleared.Purpose = TechCardPurposeSellable
	for i := range cleared.BomItems {
		cleared.BomItems[i].ConsumptionPerUnit = decimal.NullDecimal{}
	}
	require.NoError(t, ValidateAuxBomNorms(cleared))
}

func TestAuxBomLinesMissingNorm(t *testing.T) {
	tc := &TechCardInsert{
		Purpose: TechCardPurposeAuxiliary,
		BomItems: []TechCardBomItem{
			{LineKey: "A", Section: BomSectionFabric},                                   // missing
			{LineKey: "B", Section: BomSectionFabric, ConsumptionPerUnit: auxNd("0.3")}, // normed
			{LineKey: "C", Section: BomSectionHardware},                                 // counted
			{LineKey: "D", Section: BomSectionTrim},                                     // cut by strip
			{LineKey: "E", Section: BomSectionThread, CutToLength: true},                // cut, read mark
			{LineKey: "F", Section: BomSectionLining},                                   // missing
		},
		CutStrips: []TechCardCutStrip{{BomLineKey: "D"}},
	}
	require.Equal(t, []int{0, 5}, AuxBomLinesMissingNorm(tc))

	tc.AuxMadeBy = sql.NullString{String: AuxMadeBySupplier, Valid: true}
	require.Nil(t, AuxBomLinesMissingNorm(tc), "a bought card's norms are not needed")

	tc.AuxMadeBy = sql.NullString{}
	tc.Purpose = TechCardPurposeSellable
	require.Nil(t, AuxBomLinesMissingNorm(tc), "a sellable card's norms live on its colourways")
}
