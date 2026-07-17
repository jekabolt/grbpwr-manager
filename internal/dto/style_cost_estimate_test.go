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

// helpers local to the estimate tests
func nstr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func bidx(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

// baseEstimateCard: fabric @10 EUR (5% wastage) + trim @2 EUR, one colourway consuming 2 m fabric +
// 3 trims, costing cmt 5 / overhead 3 / defect 10%. All EUR so no FX needed.
func baseEstimateCard() *entity.TechCard {
	c := &entity.TechCard{Id: 7}
	c.Name = "Jacket"
	c.StyleNumber = nstr("S-7")
	c.BomItems = []entity.TechCardBomItem{
		{Id: 100, Name: "Main fabric", Section: entity.BomSectionFabric, Unit: nstr("m"), UnitPrice: nd("10"), Currency: nstr("EUR"), WastagePercent: nd("5")},
		{Id: 101, Name: "Zip", Section: entity.BomSectionHardware, Unit: nstr("pc"), UnitPrice: nd("2"), Currency: nstr("EUR")},
	}
	c.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Black", ProductId: bidx(55), Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: bidx(0), Consumption: nd("2")}, // measured: 2 m → wastage applies
			{BomItemIndex: bidx(1), Quantity: nd("3")},    // countable: 3 zips → no wastage
		}},
	}
	c.Costing = &entity.TechCardCosting{
		CmtCost:       nd("5"),
		OverheadCost:  nd("3"),
		DefectPercent: nd("10"),
		Currency:      nstr("EUR"),
	}
	return c
}

func TestComputeStyleCostEstimateBomSnapshotGolden(t *testing.T) {
	fx := CostingFx{Base: "EUR"} // all-EUR: no rates needed
	est := ComputeStyleCostEstimate(baseEstimateCard(), 0, nil, fx)
	require.NotNil(t, est)

	// fabric: 2 × 10 × 1.05 (5% wastage) = 21.00 ; zip: 3 × 2 = 6.00 ; materials = 27.00
	require.Len(t, est.Materials, 2)
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_BOM_SNAPSHOT, est.Materials[0].PriceSource)
	require.Equal(t, "21.00", est.Materials[0].LineTotalBase.Value)
	require.True(t, est.Materials[0].HasBase)
	require.Equal(t, "6.00", est.Materials[1].LineTotalBase.Value)
	require.Equal(t, "27.00", est.MaterialsPerUnitBase.Value)

	// articles cmt 5 + overhead 3 = 8 ; unit = (27 + 8) × 1.10 = 38.50
	require.Len(t, est.Articles, 2) // only the set articles (cmt, overhead)
	require.Equal(t, "38.50", est.UnitCostBase.Value)
	require.Equal(t, "10", est.DefectPct.Value)
	require.Empty(t, est.Caveat, "no fallback / unpriced / FX gap → no caveat")

	// order_qty 0 (no size run) → order_cost 0
	require.Equal(t, "0.00", est.OrderCostBase.Value)

	// INVARIANT: with every line on a BOM snapshot (no fallback), the transparent estimate must equal
	// the legacy seed math (ComputeTechCardUnitCost) to the cent — the estimate can only diverge on a
	// catalog fallback, and that is always flagged. Guards against drift in either function.
	legacyUnit, _ := ComputeTechCardUnitCost(baseEstimateCard(), fx)
	require.True(t, legacyUnit.Valid)
	require.Equal(t, legacyUnit.Decimal.StringFixed(2), est.UnitCostBase.Value)
}

func TestComputeStyleCostEstimateOrderCost(t *testing.T) {
	c := baseEstimateCard()
	c.SizeQuantities = []entity.TechCardSizeQuantity{{SizeId: 1, OrderQty: 10}, {SizeId: 2, OrderQty: 5}}
	est := ComputeStyleCostEstimate(c, 0, nil, CostingFx{Base: "EUR"})
	require.Equal(t, int32(15), est.OrderQty)
	require.Equal(t, "577.50", est.OrderCostBase.Value) // 38.50 × 15
}

func TestComputeStyleCostEstimateCatalogFallback(t *testing.T) {
	c := baseEstimateCard()
	// fabric loses its snapshot price but keeps its material link → plan-2 catalog fallback.
	c.BomItems[0].UnitPrice = decimal.NullDecimal{}
	c.BomItems[0].Currency = sql.NullString{}
	c.BomItems[0].MaterialId = sql.NullInt64{Int64: 900, Valid: true}
	latest := entity.MaterialPrice{Price: decimal.RequireFromString("12"), Currency: "EUR", ValidFrom: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	catalog := map[int64]*entity.MaterialPrice{900: &latest}

	est := ComputeStyleCostEstimate(c, 0, catalog, CostingFx{Base: "EUR"})
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_CATALOG_LATEST, est.Materials[0].PriceSource)
	require.Equal(t, "12", est.Materials[0].UnitPrice.Value)
	require.NotNil(t, est.Materials[0].PriceDate, "catalog line carries its effective date")
	// fabric now 2 × 12 × 1.05 = 25.20 ; + zip 6 = 31.20 materials
	require.Equal(t, "25.20", est.Materials[0].LineTotalBase.Value)
	require.Equal(t, "31.20", est.MaterialsPerUnitBase.Value)
	require.Contains(t, est.Caveat, "catalog price")
}

func TestComputeStyleCostEstimateUnpriced(t *testing.T) {
	c := baseEstimateCard()
	// zip loses its price AND has no material link → no price at all.
	c.BomItems[1].UnitPrice = decimal.NullDecimal{}
	c.BomItems[1].Currency = sql.NullString{}
	est := ComputeStyleCostEstimate(c, 0, nil, CostingFx{Base: "EUR"})
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_NONE, est.Materials[1].PriceSource)
	require.Nil(t, est.Materials[1].LineTotalBase, "unpriced line contributes nothing")
	require.False(t, est.Materials[1].HasBase)
	require.Equal(t, "21.00", est.MaterialsPerUnitBase.Value, "only the fabric line counts")
	require.Contains(t, est.Caveat, "no price")
}

func TestComputeStyleCostEstimateFxFoldAndGap(t *testing.T) {
	c := baseEstimateCard()
	// fabric priced in USD (rate present) ; zip priced in GBP (no rate → excluded + caveat).
	c.BomItems[0].Currency = nstr("USD")
	c.BomItems[1].Currency = nstr("GBP")
	fx := CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"USD": decimal.RequireFromString("0.90")}}
	est := ComputeStyleCostEstimate(c, 0, nil, fx)

	// fabric: 2 × 10 × 1.05 = 21 USD → × 0.90 = 18.90 EUR ; zip GBP has no rate → has_base false.
	require.True(t, est.Materials[0].HasBase)
	require.Equal(t, "18.90", est.Materials[0].LineTotalBase.Value)
	require.False(t, est.Materials[1].HasBase, "GBP line has no rate")
	require.Equal(t, "18.90", est.MaterialsPerUnitBase.Value, "only the convertible line is in the base total")
	require.Contains(t, est.Caveat, "FX rate")
}

func TestComputeStyleCostEstimateUnknownColorway(t *testing.T) {
	est := ComputeStyleCostEstimate(baseEstimateCard(), 99999, nil, CostingFx{Base: "EUR"})
	require.NotNil(t, est)
	require.Empty(t, est.Materials, "an explicit unknown colourway is not swapped for the primary")
}
