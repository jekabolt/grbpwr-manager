package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func d(v string) decimal.Decimal       { return decimal.RequireFromString(v) }
func nd2(v string) decimal.NullDecimal { return decimal.NullDecimal{Decimal: d(v), Valid: true} }
func ni(v int64) sql.NullInt64         { return sql.NullInt64{Int64: v, Valid: true} }

// FoldProductionRunCostsToBase fills unset amount_base by FX, preserves a manual base, and leaves
// an unfoldable currency unset.
func TestFoldProductionRunCostsToBase(t *testing.T) {
	fx := CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"USD": d("0.9")}}
	costs := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, Amount: d("100"), Currency: "USD"},                  // → 90
		{Kind: entity.ProductionRunCostCMT, Amount: d("50"), Currency: "EUR"},                         // base ccy → 50
		{Kind: entity.ProductionRunCostDuty, Amount: d("30"), Currency: "GBP"},                        // no rate → unset
		{Kind: entity.ProductionRunCostOther, Amount: d("7"), Currency: "USD", AmountBase: nd2("99")}, // manual kept
	}
	FoldProductionRunCostsToBase(costs, fx)
	require.True(t, costs[0].AmountBase.Valid)
	require.True(t, costs[0].AmountBase.Decimal.Equal(d("90")))
	require.True(t, costs[1].AmountBase.Decimal.Equal(d("50")))
	require.False(t, costs[2].AmountBase.Valid, "no rate → base stays unset")
	require.True(t, costs[3].AmountBase.Decimal.Equal(d("99")), "manual base preserved")
}

// computeProductionRunActuals derives totals, unit cost, defect %, by-kind and plan/fact deltas.
func TestComputeProductionRunActuals(t *testing.T) {
	run := &entity.ProductionRun{
		ProductionRunInsert: entity.ProductionRunInsert{
			PlannedUnitCost: nd2("10.00"),
			Sizes: []entity.ProductionRunSize{
				{SizeId: 1, PlannedQty: 60, ReceivedQty: ni(54), DefectQty: ni(6)},
				{SizeId: 2, PlannedQty: 40, ReceivedQty: ni(36), DefectQty: ni(4)},
			},
			Costs: []entity.ProductionRunCost{
				{Kind: entity.ProductionRunCostMaterials, Amount: d("500"), Currency: "EUR", AmountBase: nd2("500")},
				{Kind: entity.ProductionRunCostCMT, Amount: d("400"), Currency: "EUR", AmountBase: nd2("400")},
			},
		},
	}
	a := ConvertEntityProductionRunToPb(run).Actuals
	require.NotNil(t, a)
	require.True(t, a.HasBase)
	require.Equal(t, int32(100), a.PlannedQtyTotal)
	require.Equal(t, int32(90), a.ReceivedQtyTotal)
	require.Equal(t, int32(10), a.DefectQtyTotal)
	require.Equal(t, "900", a.ActualTotalBase.Value)
	require.Equal(t, "10", a.ActualUnitCost.Value)    // 900 / 90
	require.Equal(t, "10", a.DefectPctActual.Value)   // 10 / 100 × 100
	require.Equal(t, "900", a.PlannedTotalBase.Value) // 10 × 90
	require.Equal(t, "0", a.TotalVariance.Value)
	require.Equal(t, "0", a.UnitCostVariance.Value)
	require.Len(t, a.ByKind, 2)
	require.Equal(t, pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_MATERIALS, a.ByKind[0].Kind)
	require.Equal(t, "500", a.ByKind[0].AmountBase.Value)
}

// A cost that could not be folded to base flags has_base=false and is excluded from the total.
func TestComputeProductionRunActualsPartialBase(t *testing.T) {
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Sizes: []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 10, ReceivedQty: ni(10)}},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostMaterials, Amount: d("100"), Currency: "EUR", AmountBase: nd2("100")},
			{Kind: entity.ProductionRunCostDuty, Amount: d("30"), Currency: "GBP"}, // unfoldable → no base
		},
	}}
	a := ConvertEntityProductionRunToPb(run).Actuals
	require.False(t, a.HasBase)
	require.Equal(t, "100", a.ActualTotalBase.Value, "unfoldable cost excluded from total")
	require.Len(t, a.ByKind, 1)
}

// ProductionRunActualUnitCostBase is valid only with costs present, all folded to base, and some
// received quantity — the trustworthy figure for setting cost_price.
func TestProductionRunActualUnitCostBase(t *testing.T) {
	base := func(costs []entity.ProductionRunCost, sizes []entity.ProductionRunSize) decimal.NullDecimal {
		return ProductionRunActualUnitCostBase(&entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{Costs: costs, Sizes: sizes}})
	}
	okCosts := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, AmountBase: nd2("500")},
		{Kind: entity.ProductionRunCostCMT, AmountBase: nd2("400")},
	}
	recv := []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 90, ReceivedQty: ni(90)}}

	v := base(okCosts, recv)
	require.True(t, v.Valid)
	require.True(t, v.Decimal.Equal(d("10")), "900 / 90")

	require.False(t, base(nil, recv).Valid, "no costs → invalid")
	require.False(t, base(okCosts, []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 90}}).Valid, "0 received → invalid")

	partial := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, AmountBase: nd2("500")},
		{Kind: entity.ProductionRunCostDuty}, // AmountBase unset → not foldable
	}
	require.False(t, base(partial, recv).Valid, "partial fold → invalid")
}

func TestConvertPbProductionRunInsertToEntity(t *testing.T) {
	rq := int32(58)
	dq := int32(2)
	e, err := ConvertPbProductionRunInsertToEntity(&pb_common.ProductionRunInsert{
		TechCardId: 7,
		ReleaseId:  3,
		Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS,
		Notes:      "batch A",
		Sizes: []*pb_common.ProductionRunSize{
			{SizeId: 1, PlannedQty: 60, ReceivedQty: &rq, DefectQty: &dq},
			{SizeId: 2, PlannedQty: 40}, // received/defect unset
		},
	})
	require.NoError(t, err)
	require.Equal(t, 7, e.TechCardId)
	require.True(t, e.ReleaseId.Valid)
	require.EqualValues(t, 3, e.ReleaseId.Int64)
	require.Equal(t, entity.ProductionRunInProgress, e.Status)
	require.False(t, e.PlannedUnitCost.Valid, "plan cost is never taken from the client")
	require.Len(t, e.Sizes, 2)
	require.True(t, e.Sizes[0].ReceivedQty.Valid)
	require.EqualValues(t, 58, e.Sizes[0].ReceivedQty.Int64)
	require.False(t, e.Sizes[1].ReceivedQty.Valid, "unset received stays NULL")

	// round-trip back to pb preserves presence
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: *e}
	pb := ConvertEntityProductionRunToPb(run)
	require.Equal(t, int32(9), pb.Id)
	require.Equal(t, pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS, pb.Run.Status)
	require.EqualValues(t, 3, pb.Run.ReleaseId)
	require.Len(t, pb.Run.Sizes, 2)
	require.NotNil(t, pb.Run.Sizes[0].ReceivedQty)
	require.EqualValues(t, 58, *pb.Run.Sizes[0].ReceivedQty)
	require.Nil(t, pb.Run.Sizes[1].ReceivedQty, "unset received stays absent")
}

func TestConvertPbProductionRunInsertValidation(t *testing.T) {
	cases := map[string]*pb_common.ProductionRunInsert{
		"missing tech_card_id": {Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED},
		"unknown status":       {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_UNKNOWN},
		"duplicate size": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Sizes: []*pb_common.ProductionRunSize{{SizeId: 1, PlannedQty: 1}, {SizeId: 1, PlannedQty: 2}}},
		"zero size_id": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Sizes: []*pb_common.ProductionRunSize{{SizeId: 0, PlannedQty: 1}}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ConvertPbProductionRunInsertToEntity(in)
			require.Error(t, err)
		})
	}
}

func TestNormalizeProductionRunStatusFilter(t *testing.T) {
	st, err := NormalizeProductionRunStatusFilter(" Received ")
	require.NoError(t, err)
	require.Equal(t, entity.ProductionRunReceived, st)

	st, err = NormalizeProductionRunStatusFilter("")
	require.NoError(t, err)
	require.Equal(t, entity.ProductionRunStatus(""), st)

	_, err = NormalizeProductionRunStatusFilter("bogus")
	require.Error(t, err)
}
