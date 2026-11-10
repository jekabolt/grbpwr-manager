package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func threadConversionMaterial(id int, unit, length, price string) entity.MaterialWithPrice {
	m := entity.MaterialWithPrice{Material: entity.Material{Id: id, MaterialInsert: entity.MaterialInsert{
		Unit: sql.NullString{String: unit, Valid: unit != ""},
	}}}
	if length != "" {
		m.ThreadAttr = &entity.MaterialThreadAttr{
			LengthPerConeM: decimal.NewNullDecimal(decimal.RequireFromString(length)),
		}
	}
	if price != "" {
		m.LatestPrice = &entity.MaterialPrice{
			MaterialId: id, Price: decimal.RequireFromString(price), Currency: "EUR",
			ValidFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	return m
}

func threadConversionBom() *entity.TechCardBomItem {
	return &entity.TechCardBomItem{
		Id: 10, Section: entity.BomSectionThread,
		MaterialId: sql.NullInt64{Int64: 1, Valid: true},
		Unit:       sql.NullString{String: "m", Valid: true},
	}
}

func TestPinShadowBomConvertsPinnedThreadPurchaseUnitPrice(t *testing.T) {
	bom := threadConversionBom()
	usage := &entity.TechCardColorwayUsage{MaterialId: sql.NullInt64{Int64: 2, Valid: true}}

	for _, tc := range []struct {
		name, unit, length, price, want string
		priced                          bool
	}{
		{"5000 metre cone", "cone", "5000", "5", "0.001", true},
		{"pin uses its own 3000 metre length", "cone", "3000", "5", "0.0017", true},
		{"mass never uses thread length", "kg", "5000", "5", "", false},
		{"package without length", "cone", "", "5", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shadow := pinShadowBom(bom, usage, map[int]entity.MaterialWithPrice{
				2: threadConversionMaterial(2, tc.unit, tc.length, tc.price),
			}, "EUR", "EUR")
			require.NotSame(t, bom, shadow)
			require.Equal(t, tc.priced, shadow.UnitPrice.Valid)
			if tc.priced {
				require.Equal(t, tc.want, shadow.UnitPrice.Decimal.String())
			}
		})
	}
}

func TestResolvePlanUnitPriceConvertsThreadPinAndFallback(t *testing.T) {
	bom := threadConversionBom()
	material := threadConversionMaterial(2, "cone", "5000", "5")
	catalog := map[int64]entity.MaterialWithPrice{2: material}

	pin := &entity.TechCardColorwayUsage{MaterialId: sql.NullInt64{Int64: 2, Valid: true}}
	price, _, source, _ := resolvePlanUnitPrice(pin, bom, catalog)
	require.True(t, price.Valid)
	require.Equal(t, "0.001", price.Decimal.String())
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_CATALOG_LATEST, source)

	bom.MaterialId = sql.NullInt64{Int64: 2, Valid: true}
	price, _, source, _ = resolvePlanUnitPrice(nil, bom, catalog)
	require.True(t, price.Valid)
	require.Equal(t, "0.001", price.Decimal.String())
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_CATALOG_LATEST, source)

	missing := threadConversionMaterial(2, "cone", "", "5")
	price, _, source, _ = resolvePlanUnitPrice(nil, bom, map[int64]entity.MaterialWithPrice{2: missing})
	require.False(t, price.Valid)
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_NONE, source)
}

func threadPlanFixture(article entity.MaterialWithPrice) (*entity.ProductionRun, *entity.TechCard, map[int]entity.MaterialWithPrice) {
	bom := *threadConversionBom()
	bom.MaterialId = sql.NullInt64{Int64: int64(article.Id), Valid: true}
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{bom}
	card.Colorways = []entity.TechCardColorway{{
		Id: 20, ProductId: sql.NullInt32{Int32: 30, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId:   sql.NullInt64{Int64: int64(bom.Id), Valid: true},
			Consumption: decimal.NewNullDecimal(decimal.NewFromInt(2)),
		}},
	}}
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{{
			ProductId: sql.NullInt32{Int32: 30, Valid: true}, SizeId: 1, PlannedQty: 1,
		}},
	}}
	return run, card, map[int]entity.MaterialWithPrice{article.Id: article}
}

func TestMaterialPlanConvertsOnlyConvertibleThreadUnits(t *testing.T) {
	t.Run("fractional cone keeps six-decimal precision", func(t *testing.T) {
		article := threadConversionMaterial(2, "cone", "5000", "")
		run, card, linked := threadPlanFixture(article)
		plan := ComputeProductionRunMaterialPlan(run, card, map[int]decimal.Decimal{2: decimal.NewFromInt(1)}, nil, linked, nil)
		require.Len(t, plan.Rows, 1)
		require.Equal(t, "cone", plan.Rows[0].Unit)
		require.Equal(t, "0.0004", plan.Rows[0].Required.Value)
		require.NotNil(t, plan.Rows[0].OnHand)
		require.NotNil(t, plan.Rows[0].Shortage)
	})

	for _, tc := range []struct{ name, unit, length string }{
		{"missing package length", "cone", ""},
		{"mass with stale length", "kg", "5000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			article := threadConversionMaterial(2, tc.unit, tc.length, "")
			run, card, linked := threadPlanFixture(article)
			plan := ComputeProductionRunMaterialPlan(run, card, map[int]decimal.Decimal{2: decimal.NewFromInt(100)}, nil, linked, nil)
			require.Len(t, plan.Rows, 1)
			require.Empty(t, plan.Rows[0].Unit)
			require.Equal(t, "2", plan.Rows[0].Required.Value, "the metre requirement is not divided by an invalid factor")
			require.Nil(t, plan.Rows[0].OnHand)
			require.Nil(t, plan.Rows[0].Shortage)
		})
	}
}

func TestRunReadinessUnitCoverageConvertsThreadNormToStockUnit(t *testing.T) {
	bom := *threadConversionBom()
	card := &entity.TechCard{}
	card.BomItems = []entity.TechCardBomItem{bom}
	card.Colorways = []entity.TechCardColorway{{
		ProductId: sql.NullInt32{Int32: 30, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId:   sql.NullInt64{Int64: int64(bom.Id), Valid: true},
			Consumption: decimal.NewNullDecimal(decimal.NewFromInt(2)),
		}},
	}}
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{2: threadConversionMaterial(2, "cone", "5000", "")}
	card.BomItems[0].MaterialId = sql.NullInt64{Int64: 2, Valid: true}
	b := runReadinessBuilder{card: card, in: RunReadinessInput{
		Card:   card,
		Cells:  []entity.RunReadinessCell{{ColorwayId: 30, SizeId: 1, PlannedQty: 30000}},
		OnHand: map[int]decimal.Decimal{2: decimal.NewFromInt(10)},
	}}
	require.Equal(t, int32(25000), b.unitCoverage()[0].GetUnitsFromStock())

	missing := card.LinkedMaterials[2]
	missing.ThreadAttr = nil
	card.LinkedMaterials[2] = missing
	require.Zero(t, b.unitCoverage()[0].GetUnitsFromStock())
}
