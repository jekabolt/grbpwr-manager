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
func ni32(v int32) sql.NullInt32       { return sql.NullInt32{Int32: v, Valid: true} }

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
			PlannedCurrency: sql.NullString{String: "EUR", Valid: true}, // base — the plan is comparable
			Lines: []entity.ProductionRunLine{
				{ProductId: ni32(11), SizeId: 1, PlannedQty: 60, ReceivedQty: ni(54), DefectQty: ni(6)},
				{ProductId: ni32(11), SizeId: 2, PlannedQty: 40, ReceivedQty: ni(36), DefectQty: ni(4)},
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

// TestComputeProductionRunActualsByColorway covers the per-colourway material breakdown (gap-07 v2
// C): issues bucket by product_id, returns net against the same product, an uncosted issue flags its
// colourway, and issues with no product_id fall into the unattributed bucket.
func TestComputeProductionRunActualsByColorway(t *testing.T) {
	iss := entity.MaterialMovementIssueProduction
	ret := entity.MaterialMovementReturnProduction
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{
			{ProductId: ni32(101), SizeId: 1, PlannedQty: 10, ReceivedQty: ni(6)},
			{ProductId: ni32(102), SizeId: 1, PlannedQty: 10, ReceivedQty: ni(4)},
		},
	}}
	run.MaterialMovements = []entity.MaterialMovement{
		{MovementType: iss, Quantity: d("20"), UnitCostBase: nd2("5"), ProductId: ni32(101)}, // 100
		{MovementType: ret, Quantity: d("2"), UnitCostBase: nd2("5"), ProductId: ni32(101)},  // −10 → 90
		{MovementType: iss, Quantity: d("10"), UnitCostBase: nd2("5"), ProductId: ni32(102)}, // 50
		{MovementType: iss, Quantity: d("1"), ProductId: ni32(102)},                          // uncosted → flags 102
		{MovementType: iss, Quantity: d("4"), UnitCostBase: nd2("5")},                        // no product → unattributed 20
	}

	a := ConvertEntityProductionRunToPb(run).Actuals
	require.NotNil(t, a)
	require.Equal(t, "160", a.MaterialsFromStockBase.Value, "90 + 50 + 20")
	require.Equal(t, "20", a.UnattributedMaterialsBase.Value)
	require.True(t, a.HasUncostedIssues)
	require.Len(t, a.ByColorway, 2)

	c1 := a.ByColorway[0]
	require.Equal(t, int32(101), c1.ProductId, "sorted by product_id")
	require.Equal(t, "90", c1.MaterialsFromStockBase.Value)
	require.Equal(t, int32(6), c1.ReceivedQty)
	require.Equal(t, "15", c1.MaterialsUnitCost.Value) // 90 / 6
	require.False(t, c1.HasUncosted)

	c2 := a.ByColorway[1]
	require.Equal(t, int32(102), c2.ProductId)
	require.Equal(t, "50", c2.MaterialsFromStockBase.Value)
	require.Equal(t, int32(4), c2.ReceivedQty)
	require.Equal(t, "12.5", c2.MaterialsUnitCost.Value) // 50 / 4
	require.True(t, c2.HasUncosted, "the uncosted issue for 102 flags it")
}

// A legacy single-colour run (no product_id on issues or lines) emits no per-colourway rows.
func TestComputeProductionRunActualsNoColorway(t *testing.T) {
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10, ReceivedQty: ni(10)}},
	}}
	run.MaterialMovements = []entity.MaterialMovement{
		{MovementType: entity.MaterialMovementIssueProduction, Quantity: d("10"), UnitCostBase: nd2("5")},
	}
	a := ConvertEntityProductionRunToPb(run).Actuals
	require.Empty(t, a.ByColorway, "no product_id → no colourway rows")
	require.Equal(t, "50", a.UnattributedMaterialsBase.Value, "all material is unattributed")
}

// A cost that could not be folded to base flags has_base=false and is excluded from the total.
func TestComputeProductionRunActualsPartialBase(t *testing.T) {
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{{ProductId: ni32(11), SizeId: 1, PlannedQty: 10, ReceivedQty: ni(10)}},
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
	base := func(costs []entity.ProductionRunCost, lines []entity.ProductionRunLine) decimal.NullDecimal {
		return ProductionRunActualUnitCostBase(&entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{Costs: costs, Lines: lines}})
	}
	okCosts := []entity.ProductionRunCost{
		{Kind: entity.ProductionRunCostMaterials, AmountBase: nd2("500")},
		{Kind: entity.ProductionRunCostCMT, AmountBase: nd2("400")},
	}
	recv := []entity.ProductionRunLine{{ProductId: ni32(11), SizeId: 1, PlannedQty: 90, ReceivedQty: ni(90)}}

	v := base(okCosts, recv)
	require.True(t, v.Valid)
	require.True(t, v.Decimal.Equal(d("10")), "900 / 90")

	require.False(t, base(nil, recv).Valid, "no costs → invalid")
	require.False(t, base(okCosts, []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 90}}).Valid, "0 received → invalid")

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
		Lines: []*pb_common.ProductionRunLine{
			{ProductId: 11, SizeId: 1, PlannedQty: 60, ReceivedQty: &rq, DefectQty: &dq},
			{ProductId: 11, SizeId: 2, PlannedQty: 40}, // received/defect unset
		},
	})
	require.NoError(t, err)
	require.Equal(t, 7, e.TechCardId)
	require.True(t, e.ReleaseId.Valid)
	require.EqualValues(t, 3, e.ReleaseId.Int64)
	require.Equal(t, entity.ProductionRunInProgress, e.Status)
	require.False(t, e.PlannedUnitCost.Valid, "plan cost is never taken from the client")
	require.Len(t, e.Lines, 2)
	require.True(t, e.Lines[0].ReceivedQty.Valid)
	require.EqualValues(t, 58, e.Lines[0].ReceivedQty.Int64)
	require.EqualValues(t, 11, e.Lines[0].ProductId.Int32)
	require.False(t, e.Lines[1].ReceivedQty.Valid, "unset received stays NULL")

	// round-trip back to pb preserves presence
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: *e}
	pb := ConvertEntityProductionRunToPb(run)
	require.Equal(t, int32(9), pb.Id)
	require.Equal(t, pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS, pb.Run.Status)
	require.EqualValues(t, 3, pb.Run.ReleaseId)
	require.Len(t, pb.Run.Lines, 2)
	require.NotNil(t, pb.Run.Lines[0].ReceivedQty)
	require.EqualValues(t, 58, *pb.Run.Lines[0].ReceivedQty)
	require.EqualValues(t, 11, pb.Run.Lines[0].ProductId)
	require.Nil(t, pb.Run.Lines[1].ReceivedQty, "unset received stays absent")
}

func TestConvertPbProductionRunInsertValidation(t *testing.T) {
	cases := map[string]*pb_common.ProductionRunInsert{
		"missing tech_card_id": {Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED},
		"unknown status":       {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_UNKNOWN},
		"duplicate product/size": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines: []*pb_common.ProductionRunLine{{ProductId: 11, SizeId: 1, PlannedQty: 1}, {ProductId: 11, SizeId: 1, PlannedQty: 2}}},
		// Phase 4: a size is required exactly when the line names a product (a garment line books
		// into product_size). A product-less size-less line is the AUX output line and is legal —
		// requiring a size there is what made aux planning unsavable since it shipped.
		"zero size_id with product": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines: []*pb_common.ProductionRunLine{{ProductId: 11, SizeId: 0, PlannedQty: 1}}},
		// NOTE: a received line without a product is NOT a dto error — an auxiliary run's output has
		// no product (it goes to the material warehouse). Product-presence is enforced per card
		// purpose in the receive handlers, not here.
		//
		// Phase 3 (0253): a line produces EITHER a sellable product or an aux colour, never both, and
		// two lines never name the same colour at the same size. The legacy "one unvarianted
		// product-less line" rule is the same triple key seen from another angle and is asserted here
		// too, because it is the one aux planning behaviour that must NOT change.
		"product and colour variant on one line": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines: []*pb_common.ProductionRunLine{{ProductId: 11, SizeId: 1, OutputVariantId: 4, PlannedQty: 1}}},
		"negative output_variant_id": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines: []*pb_common.ProductionRunLine{{OutputVariantId: -1, PlannedQty: 1}}},
		"duplicate colour variant": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines: []*pb_common.ProductionRunLine{{OutputVariantId: 4, PlannedQty: 1}, {OutputVariantId: 4, PlannedQty: 2}}},
		// A colour line is sizeless (review F5): what it produces is a warehouse bucket, not a size
		// grid, and a size would split one colour across lines the receipt has to blend back together.
		"size on a colour variant line": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines: []*pb_common.ProductionRunLine{{OutputVariantId: 4, SizeId: 2, PlannedQty: 1}}},
		"two unvarianted product-less lines": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines: []*pb_common.ProductionRunLine{{PlannedQty: 1}, {PlannedQty: 2}}},
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

// The aux output line (no product, no size) must convert cleanly — its rejection was the bug that
// made auxiliary run planning unsavable from the day it shipped (Phase 4 fix, migration 0236).
func TestConvertPbProductionRunInsertAllowsSizelessProductlessLine(t *testing.T) {
	e, err := ConvertPbProductionRunInsertToEntity(&pb_common.ProductionRunInsert{
		TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
		Lines: []*pb_common.ProductionRunLine{{SizeId: 0, PlannedQty: 100}},
	})
	require.NoError(t, err)
	require.Len(t, e.Lines, 1)
	require.Equal(t, 0, e.Lines[0].SizeId, "0 size = NULL at the store boundary")
	require.False(t, e.Lines[0].ProductId.Valid)
	require.False(t, e.Lines[0].OutputVariantId.Valid,
		"an aux card in legacy single-output mode plans one colourless line, exactly as before 0253")
}

// A variant-mode aux run plans ONE PRODUCT-LESS LINE PER COLOUR (0253). The colour is the third axis
// of the uniqueness key, so several such lines coexist where the old {product, size} key collapsed
// them all into "duplicate product_id 0 / size_id 0" — and each stays sizeless, like the aux line
// always was.
func TestConvertPbProductionRunInsertAcceptsOneLinePerColourVariant(t *testing.T) {
	e, err := ConvertPbProductionRunInsertToEntity(&pb_common.ProductionRunInsert{
		TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
		Lines: []*pb_common.ProductionRunLine{
			{OutputVariantId: 4, PlannedQty: 60},
			{OutputVariantId: 9, PlannedQty: 40},
		},
	})
	require.NoError(t, err)
	require.Len(t, e.Lines, 2)
	for i, want := range []int32{4, 9} {
		require.True(t, e.Lines[i].OutputVariantId.Valid)
		require.Equal(t, want, e.Lines[i].OutputVariantId.Int32)
		require.False(t, e.Lines[i].ProductId.Valid, "a colour line names no product")
		require.Equal(t, 0, e.Lines[i].SizeId, "a colour line needs no size, like the aux line before it")
		require.NotEmpty(t, e.Lines[i].LineKey, "every line still gets a stable identity")
	}

	// The colour survives the round trip back to pb, so the client's next save diffs against it
	// rather than silently dropping every line's colour.
	pb := ConvertEntityProductionRunToPb(&entity.ProductionRun{Id: 3, ProductionRunInsert: *e})
	require.Len(t, pb.Run.Lines, 2)
	require.EqualValues(t, 4, pb.Run.Lines[0].OutputVariantId)
	require.EqualValues(t, 9, pb.Run.Lines[1].OutputVariantId)
	require.Zero(t, pb.Run.Lines[0].ProductId)
}

// A HALF-COLOURED grid (the legacy colourless aux line beside colour lines) converts cleanly here
// and is refused one layer down, by the production-run store's plan-time validator — which is the
// single home of that rule (review F3). The dto deliberately does not duplicate it: the two shapes
// are distinct keys as far as uniqueness goes, and the reason the mix is illegal is a fact about how
// an auxiliary receipt books (one bucket per colour, nowhere to put a colourless line), which is the
// store's business. The refusal itself is pinned in
// TestOutputVariantProductionRuns/a_half_coloured_grid_is_refused_at_plan_time.
func TestConvertPbProductionRunInsertLeavesTheMixedGridToTheStore(t *testing.T) {
	e, err := ConvertPbProductionRunInsertToEntity(&pb_common.ProductionRunInsert{
		TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
		Lines: []*pb_common.ProductionRunLine{
			{PlannedQty: 10},
			{OutputVariantId: 4, PlannedQty: 20},
		},
	})
	require.NoError(t, err, "the dto converts the shape; the store decides it is unreceivable")
	require.Len(t, e.Lines, 2)
	require.False(t, e.Lines[0].OutputVariantId.Valid)
	require.True(t, e.Lines[1].OutputVariantId.Valid)
}
