package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// runMixSnapshotCard: base sample size 4, a fabric graded 2 m at size 4 and 3 m at size 6 (10 EUR/m,
// 10% BOM wastage) plus cmt 5. Unit cost = 27 at size 4 (the STYLE figure) and 38 at size 6. Size 5
// is in the size range but carries NO norm — the "operator forgot to grade it" case.
func runMixSnapshotCard() *entity.TechCard {
	ndd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{4, 5, 6}
	card.BaseSampleSizeId = sql.NullInt32{Int32: 4, Valid: true}
	card.Pieces = []entity.TechCardPiece{{LineKey: "P1", Name: "перед"}}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 1, Name: "Shell", Section: entity.BomSectionFabric,
		Unit:      sql.NullString{String: "m", Valid: true},
		UnitPrice: ndd("10"), Currency: sql.NullString{String: "EUR", Valid: true},
		WastagePercent: ndd("10"),
	}}
	card.Colorways = []entity.TechCardColorway{{Id: 1, Name: "Black", Usages: []entity.TechCardColorwayUsage{{
		BomItemId: sql.NullInt64{Int64: 1, Valid: true},
		SizeConsumptions: []entity.TechCardBomSizeConsumption{
			{SizeId: 4, Consumption: decimal.RequireFromString("2")},
			{SizeId: 6, Consumption: decimal.RequireFromString("3")},
		},
	}}}}
	card.Costing = &entity.TechCardCosting{
		CmtCost:  ndd("5"),
		Currency: sql.NullString{String: "EUR", Valid: true},
	}
	return card
}

// planRunOnLiveCard drives CreateProductionRun down the LIVE-CARD snapshot path (no release id) and
// hands back what the service actually froze on the run.
func planRunOnLiveCard(t *testing.T, lines []*pb_common.ProductionRunLine) *entity.ProductionRunInsert {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	pr := mocks.NewMockProductionRuns(t)
	ws := mocks.NewMockWorkshop(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().ProductionRuns().Return(pr)
	repo.EXPECT().Workshop().Return(ws).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()
	// Readiness gate in its default report-only mode — this test is about the cost snapshot.
	ws.EXPECT().GetSettings(mock.Anything).Return(&entity.WorkshopSettings{}, nil).Maybe()
	ms.EXPECT().NarrowestMeasuredLotWidths(mock.Anything, mock.Anything).
		Return(map[int]decimal.NullDecimal{}, nil).Maybe()
	tc.EXPECT().GetTechCardPatternSizeIndex(mock.Anything, 7).
		Return(map[string]entity.PatternSizeIndexRow{}, nil).Maybe()
	// One card serves both the gate and the snapshot — they read the same row.
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(runMixSnapshotCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil)

	var captured *entity.ProductionRunInsert
	pr.EXPECT().CreateProductionRun(mock.Anything, mock.MatchedBy(func(r *entity.ProductionRunInsert) bool {
		captured = r
		return true
	})).Return(11, nil)
	expectRunReservationReconcileStandsDown(t, pr, 11)

	s := &Server{repo: repo}
	_, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{
			TechCardId: 7,
			Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines:      lines,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, captured)
	return captured
}

// TestCreateProductionRunSnapshotsPlanFromItsOwnSizeMix is the service-level proof of the run-mix
// phase: the frozen plan is the batch's own price, and the three edges reach the stored row as
// documented — a value, the style figure, or NULL, never a plausible-looking average.
func TestCreateProductionRunSnapshotsPlanFromItsOwnSizeMix(t *testing.T) {
	// A batch of nothing but size 6 → 3×10×1.1 + 5 = 38, NOT the style's 27 on the base size.
	run := planRunOnLiveCard(t, []*pb_common.ProductionRunLine{{SizeId: 6, PlannedQty: 40}})
	require.True(t, run.PlannedUnitCost.Valid)
	require.Equal(t, "38", run.PlannedUnitCost.Decimal.String(),
		"the run is planned at the price of the size it is actually cutting")
	require.Equal(t, "EUR", run.PlannedCurrency.String)

	// A mixed batch is weighted by its own quantities: (27×30 + 38×10) ÷ 40 = 29.75.
	run = planRunOnLiveCard(t, []*pb_common.ProductionRunLine{
		{SizeId: 4, PlannedQty: 30}, {SizeId: 6, PlannedQty: 10},
	})
	require.Equal(t, "29.75", run.PlannedUnitCost.Decimal.String())

	// A header planned before the grid exists falls back to the style figure on the base size.
	run = planRunOnLiveCard(t, nil)
	require.True(t, run.PlannedUnitCost.Valid)
	require.Equal(t, "27", run.PlannedUnitCost.Decimal.String(),
		"no lines → the style's standard cost, the conscious default")

	// A size with no norm leaves the WHOLE run unpriced: the run saves, and it reports no variance
	// rather than a variance measured against the sizes that happened to be graded (which would have
	// frozen 27 here and made every later plan/fact number a comparison with a batch never planned).
	run = planRunOnLiveCard(t, []*pb_common.ProductionRunLine{
		{SizeId: 4, PlannedQty: 30}, {SizeId: 5, PlannedQty: 10},
	})
	require.False(t, run.PlannedUnitCost.Valid, "an ungraded size on the grid → no plan cost at all")
	require.False(t, run.PlannedCurrency.Valid)
}

// TestListTechCardDevExpensesAmortisesOverRuns covers the wiring the dto tests cannot: the command
// loads the style's production runs and amortises over them, and a failure to load them degrades to
// "not amortised" instead of failing the journal or dividing by the card's phantom run.
func TestListTechCardDevExpensesAmortisesOverRuns(t *testing.T) {
	list := func(t *testing.T, runs []entity.ProductionRun, runsErr error) *pb_common.TechCardDevCostSummary {
		t.Helper()
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		fit := mocks.NewMockFittings(t)
		pr := mocks.NewMockProductionRuns(t)
		repo.EXPECT().TechCards().Return(tc)
		repo.EXPECT().Fittings().Return(fit)
		repo.EXPECT().ProductionRuns().Return(pr)

		tc.EXPECT().ListTechCardDevExpenses(mock.Anything, 7).Return([]entity.TechCardDevExpense{
			{Kind: "sample", AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("270"), Valid: true}},
		}, nil)
		tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(runMixSnapshotCard(), nil)
		tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil)
		fit.EXPECT().ListFittings(mock.Anything, devExpenseFittingScan, 0, entity.Descending, 0, 0, 7).Return(nil, 0, nil)
		pr.EXPECT().ListProductionRuns(mock.Anything, productionRunPageSize, 0,
			entity.ProductionRunListFilter{TechCardId: 7}).Return(runs, len(runs), runsErr)

		s := &Server{repo: repo}
		resp, err := s.ListTechCardDevExpenses(fullAccessCtx(), &pb_admin.ListTechCardDevExpensesRequest{TechCardId: 7})
		require.NoError(t, err)
		return resp.Summary
	}

	// 30 garments planned across two runs → 270/30 = 9 per unit on the style's 27 → 36.
	sum := list(t, []entity.ProductionRun{
		{ProductionRunInsert: entity.ProductionRunInsert{Lines: []entity.ProductionRunLine{{SizeId: 4, PlannedQty: 20}}}},
		{ProductionRunInsert: entity.ProductionRunInsert{Lines: []entity.ProductionRunLine{{SizeId: 6, PlannedQty: 10}}}},
	}, nil)
	require.EqualValues(t, 30, sum.OrderQty, "order_qty is Σ planned_qty over the runs")
	require.NotNil(t, sum.UnitCostWithDev)
	require.Equal(t, "36", sum.UnitCostWithDev.Value)

	// No runs planned yet: the journal still lists, the per-unit figure is simply withheld.
	sum = list(t, nil, nil)
	require.EqualValues(t, 0, sum.OrderQty)
	require.Nil(t, sum.UnitCostWithDev)
	require.Equal(t, "270", sum.TotalBase.Value, "the total is not affected by the missing denominator")

	// The run load failing is the same outcome, not a 500 and not a fallback denominator.
	sum = list(t, nil, context.DeadlineExceeded)
	require.EqualValues(t, 0, sum.OrderQty)
	require.Nil(t, sum.UnitCostWithDev)
	require.Equal(t, "270", sum.TotalBase.Value)
}
