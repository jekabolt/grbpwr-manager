package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestComputeColorwayUnitCostSelectsPinnedPriceInCostingCurrency(t *testing.T) {
	eur := &entity.MaterialPrice{MaterialId: 2, Price: decimal.RequireFromString("30"), Currency: "EUR"}
	usd := &entity.MaterialPrice{MaterialId: 2, Price: decimal.RequireFromString("20"), Currency: "USD"}
	tc := &entity.TechCard{
		TechCardInsert: entity.TechCardInsert{
			Costing: &entity.TechCardCosting{Currency: sql.NullString{String: "USD", Valid: true}},
			BomItems: []entity.TechCardBomItem{{
				Id: 1, MaterialId: sql.NullInt64{Int64: 1, Valid: true}, Unit: sql.NullString{String: "m", Valid: true},
			}},
			Colorways: []entity.TechCardColorway{{
				ProductId: sql.NullInt32{Int32: 7, Valid: true},
				Usages: []entity.TechCardColorwayUsage{{
					BomItemId:  sql.NullInt64{Int64: 1, Valid: true},
					MaterialId: sql.NullInt64{Int64: 2, Valid: true},
					Consumption: decimal.NullDecimal{
						Decimal: decimal.NewFromInt(1), Valid: true,
					},
				}},
			}},
		},
		LinkedMaterials: map[int]entity.MaterialWithPrice{
			2: {
				Material: entity.Material{MaterialInsert: entity.MaterialInsert{Unit: sql.NullString{String: "m", Valid: true}}},
				// The legacy singular projection is EUR, but costing in USD must use the USD row.
				LatestPrice: eur,
				LatestPrices: map[string]*entity.MaterialPrice{
					"EUR": eur,
					"USD": usd,
				},
			},
		},
	}

	unit, currency := ComputeColorwayUnitCost(tc, 7, CostingFx{
		Base: "EUR", ToBase: map[string]decimal.Decimal{"USD": decimal.RequireFromString("0.5")},
	})
	require.True(t, unit.Valid)
	require.True(t, unit.Decimal.Equal(decimal.RequireFromString("10")), "USD 20 × 0.5 = EUR 10")
	require.Equal(t, "EUR", currency)
}
