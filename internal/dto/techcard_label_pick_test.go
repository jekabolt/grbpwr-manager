package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestGarmentLabelPickParseAndCarry(t *testing.T) {
	strOf := func(s string) *string { return &s }
	out, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{
		{Key: "loop", Source: strOf("make"), Kind: "loop", MadeBy: "printed", PrintMethod: "screen", LengthCm: dec("6.5")},
		{Key: "stock", Source: strOf("stock"), MadeBy: "woven", PrintMethod: "foil"},
		{Key: "old"},
	})
	require.NoError(t, err)
	require.Equal(t, entity.GarmentLabelSourceMake, out[0].Source)
	require.Equal(t, "6.5", out[0].LengthCm.String())
	require.Empty(t, out[1].MadeBy, "a stock label is not made")
	require.Empty(t, out[1].PrintMethod)
	require.True(t, out[2].PickOmitted)

	for _, bad := range []*pb_common.TechCardGarmentLabel{
		{Key: "x", Source: strOf("bought")},
		{Key: "x", LengthCm: dec("-1")},
		{Key: "x", LengthCm: dec("6.55")},
		{Key: "x", LengthCm: dec("abc")},
	} {
		_, err := parseTechCardGarmentLabels([]*pb_common.TechCardGarmentLabel{bad})
		require.Error(t, err)
	}

	// An older client's save keeps the pick; a client that knows the field may clear it.
	stored := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "loop", Source: entity.GarmentLabelSourceMake, Kind: "loop", MadeBy: "printed"},
	}}
	tc := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "loop", PickOmitted: true}}}
	CarryOmittedGarmentLabelArtwork(stored, tc)
	require.Equal(t, entity.GarmentLabelSourceMake, tc.GarmentLabels[0].Source)
	require.Equal(t, "printed", tc.GarmentLabels[0].MadeBy)
	tc = &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{{Key: "loop"}}}
	CarryOmittedGarmentLabelArtwork(stored, tc)
	require.Empty(t, tc.GarmentLabels[0].Source)
}

func TestGarmentLabelStockNeedsNoArtwork(t *testing.T) {
	tc := &entity.TechCardInsert{GarmentLabels: []entity.TechCardGarmentLabel{
		{Key: "stock", Source: entity.GarmentLabelSourceStock},
		{Key: "made", Source: entity.GarmentLabelSourceMake},
		{Key: "old"},
	}}
	require.Equal(t, []string{"made (#2)", "old (#3)"}, GarmentLabelsWithoutMockup(tc))
}

// A row the server wrote for a picked label is not an authored recipe: a colourway with nothing else
// must keep inheriting the style figure, not be costed as one label.
func TestPickedLabelRowIsNotOwnRecipe(t *testing.T) {
	bom := func(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
	tc := &entity.TechCard{}
	tc.GarmentLabels = []entity.TechCardGarmentLabel{
		{Key: "woven", Source: entity.GarmentLabelSourceStock, BomItemId: sql.NullInt32{Int32: 7, Valid: true}},
		{Key: "old", BomItemId: sql.NullInt32{Int32: 8, Valid: true}},
	}
	only := &entity.TechCardColorway{Usages: []entity.TechCardColorwayUsage{{BomItemId: bom(7)}}}
	require.False(t, colorwayHasOwnRecipe(tc, only))
	authored := &entity.TechCardColorway{Usages: []entity.TechCardColorwayUsage{{BomItemId: bom(7)}, {BomItemId: bom(9)}}}
	require.True(t, colorwayHasOwnRecipe(tc, authored))
	for name, row := range map[string]entity.TechCardColorwayUsage{
		"quantity":  {BomItemId: bom(7), Quantity: decimal.NullDecimal{Decimal: decimal.NewFromInt(2), Valid: true}},
		"pin":       {BomItemId: bom(7), MaterialId: bom(3)},
		"placement": {BomItemId: bom(7), Placement: sql.NullString{String: "hem", Valid: true}},
	} {
		typed := &entity.TechCardColorway{Usages: []entity.TechCardColorwayUsage{row}}
		require.True(t, colorwayHasOwnRecipe(tc, typed), name)
	}
	manual := entity.TechCardColorwayUsage{BomItemId: bom(7), ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true}}
	require.False(t, colorwayHasOwnRecipe(tc, &entity.TechCardColorway{Usages: []entity.TechCardColorwayUsage{manual}}))
	older := &entity.TechCardColorway{Usages: []entity.TechCardColorwayUsage{{BomItemId: bom(8)}}}
	require.True(t, colorwayHasOwnRecipe(tc, older), "a label with no source changes nothing")
}

// The frozen price reads the same rule: a secondary with only the server's row keeps the style figure.
func TestPickedLabelRowFrozenInherits(t *testing.T) {
	source := "stock"
	snap := &pb_common.TechCard{
		TechCard: &pb_common.TechCardInsert{
			GarmentLabels: []*pb_common.TechCardGarmentLabel{{Key: "woven", Source: &source, BomItemId: 7}},
			Costing: &pb_common.TechCardCosting{
				Currency: "EUR", UnitCost: dec("20"),
				ColorwayCosts: []*pb_common.TechCardColorwayCost{
					{ColorwayId: 1, UnitCost: dec("20")},
					{ColorwayId: 2, UnitCost: dec("1")},
					{ColorwayId: 3, UnitCost: dec("5")},
				},
			},
		},
		Colorways: []*pb_common.AdminColorwayRef{
			{ColorwayId: 1, Usages: []*pb_common.TechCardColorwayUsage{{BomItemId: 9}, {BomItemId: 7}}},
			{ColorwayId: 2, Usages: []*pb_common.TechCardColorwayUsage{{BomItemId: 7}}},
			{ColorwayId: 3, Usages: []*pb_common.TechCardColorwayUsage{{BomItemId: 7, Quantity: dec("2")}}},
		},
	}
	got := ReleaseFrozenColorwayCosts(snap)
	require.NotNil(t, got)
	require.Equal(t, "20", got.UnitCostByColorway[2].String(), "the server's row is not a recipe")
	require.Equal(t, "5", got.UnitCostByColorway[3].String(), "a typed number is")

	card := releaseCostingCardFromSnapshot(snap)
	require.False(t, colorwayHasOwnRecipe(card, &card.Colorways[1]))
	require.True(t, colorwayHasOwnRecipe(card, &card.Colorways[2]))
}
