package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGetStyleEconomics composes the style business-case card: it wires the sales margin, dev-cost
// roll-up, fitting-round count and production plan/fact, and computes net_after_dev = gross_margin −
// dev_total. Missing authz in context = full access, so cost fields are present.
func TestGetStyleEconomics(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	mtr := mocks.NewMockMetrics(t)
	fit := mocks.NewMockFittings(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().Metrics().Return(mtr)
	repo.EXPECT().Fittings().Return(fit)
	repo.EXPECT().ProductionRuns().Return(pr)

	card := &entity.TechCard{Id: 7}
	card.StyleNumber = sql.NullString{String: "S-1", Valid: true}
	card.Name = "The Coat"
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil)
	tc.EXPECT().ListTechCardDevExpenses(mock.Anything, 7).Return([]entity.TechCardDevExpense{
		{Kind: "sample", AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("50.00"), Valid: true}},
	}, nil)

	mtr.EXPECT().GetStyleMargin(mock.Anything, 7).Return(&entity.MarginByStyleRow{
		TechCardID: 7, StyleNumber: "S-1", Name: "The Coat",
		Revenue: decimal.NewFromInt(200), UnitsSold: 2, ColorwayCount: 2,
		HasCost: true, RevenueCost: decimal.NewFromInt(20), GrossMargin: decimal.NewFromInt(180), GrossMarginPct: 90,
	}, nil)

	// three fitting rounds (only the total is used)
	fit.EXPECT().ListFittings(mock.Anything, 1, 0, entity.Descending, 0, 0, 7).Return(nil, 3, nil)

	pr.EXPECT().ListProductionRuns(mock.Anything, styleEconomicsRunScan, 0, entity.ProductionRunListFilter{TechCardId: 7}).Return([]entity.ProductionRun{
		{ProductionRunInsert: entity.ProductionRunInsert{
			PlannedUnitCost: decimal.NullDecimal{Decimal: decimal.RequireFromString("3.00"), Valid: true},
			Lines:           []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 8, Valid: true}}},
			Costs:           []entity.ProductionRunCost{{Kind: "cmt", AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("25.00"), Valid: true}}},
		}},
	}, 1, nil)

	mtr.EXPECT().GetStyleSampleSummary(mock.Anything, 7).Return(entity.StyleSampleSummary{
		Count: 2, MaterialsCostBase: decimal.RequireFromString("15.00"),
	}, nil)
	mtr.EXPECT().GetStyleMaterialsFromStock(mock.Anything, 7).Return(entity.StyleMaterialsFromStock{
		Base: decimal.RequireFromString("40.00"),
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetStyleEconomics(context.Background(), &pb_admin.GetStyleEconomicsRequest{TechCardId: 7})
	require.NoError(t, err)
	e := resp.Economics
	require.Equal(t, int32(7), e.TechCardId)
	require.Equal(t, "S-1", e.StyleNumber)
	require.Equal(t, "The Coat", e.Name)
	require.EqualValues(t, 3, e.FittingRounds)
	require.NotNil(t, e.Sales)
	require.Equal(t, "180", e.Sales.GrossMargin.GetValue(), "sales gross margin present with full access")
	require.NotNil(t, e.DevCost)
	require.Equal(t, "50", e.DevCost.TotalBase.GetValue())
	require.NotNil(t, e.Production)
	require.EqualValues(t, 1, e.Production.Runs)
	require.EqualValues(t, 10, e.Production.PlannedQtyTotal)
	require.EqualValues(t, 8, e.Production.ReceivedQtyTotal)
	// NF-09: warehouse material (40) folds into the production actual alongside the manual cost (25),
	// same as the run-level actuals — the style card must not read a saving the run detail doesn't
	// (nf09-02). actual = 25 + 40 = 65; materials_from_stock echoed separately.
	require.Equal(t, "65", e.Production.ActualCostBase.GetValue(), "manual 25 + materials 40")
	require.Equal(t, "40", e.Production.MaterialsFromStockBase.GetValue())
	// NF-09 samples.
	require.EqualValues(t, 2, e.SamplesCount)
	require.Equal(t, "15.00", e.SamplesCostBase.GetValue())
	// net_after_dev = 180 − 50 = 130: warehouse materials fold into PRODUCTION actuals only, never into
	// the sales bottom line, which stays gross_margin − dev_total (domain rule holds).
	require.NotNil(t, e.NetAfterDev)
	require.True(t, decimal.RequireFromString(e.NetAfterDev.Value).Equal(decimal.NewFromInt(130)), "net after dev 130, got %s", e.NetAfterDev.Value)
	// samples_cost_base (15) > 0 → the sample-materials caveat is present (nf09-05); dev is fully
	// converted and cost is set, so that is the ONLY caveat.
	require.Contains(t, e.Caveat, "sample materials from stock are not included in net_after_dev")
}

// TestGetStyleEconomicsNoCost: a style without product cost gets a caveat and no net result, but
// still returns revenue/units and dev/production context.
func TestGetStyleEconomicsNoCost(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	mtr := mocks.NewMockMetrics(t)
	fit := mocks.NewMockFittings(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().Metrics().Return(mtr)
	repo.EXPECT().Fittings().Return(fit)
	repo.EXPECT().ProductionRuns().Return(pr)

	card := &entity.TechCard{Id: 9}
	card.StyleNumber = sql.NullString{String: "S-9", Valid: true}
	card.Name = "Uncosted"
	tc.EXPECT().GetTechCardById(mock.Anything, 9).Return(card, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil)
	tc.EXPECT().ListTechCardDevExpenses(mock.Anything, 9).Return(nil, nil)
	// no sales yet → nil row; handler synthesizes identity.
	mtr.EXPECT().GetStyleMargin(mock.Anything, 9).Return(nil, nil)
	fit.EXPECT().ListFittings(mock.Anything, 1, 0, entity.Descending, 0, 0, 9).Return(nil, 0, nil)
	pr.EXPECT().ListProductionRuns(mock.Anything, styleEconomicsRunScan, 0, entity.ProductionRunListFilter{TechCardId: 9}).Return(nil, 0, nil)
	mtr.EXPECT().GetStyleSampleSummary(mock.Anything, 9).Return(entity.StyleSampleSummary{}, nil)
	mtr.EXPECT().GetStyleMaterialsFromStock(mock.Anything, 9).Return(entity.StyleMaterialsFromStock{}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetStyleEconomics(context.Background(), &pb_admin.GetStyleEconomicsRequest{TechCardId: 9})
	require.NoError(t, err)
	e := resp.Economics
	require.Equal(t, "S-9", e.StyleNumber)
	require.Nil(t, e.NetAfterDev, "no net result without product cost")
	require.NotEmpty(t, e.Caveat, "caveat explains missing cost")
	require.NotNil(t, e.Production, "production summary present (all-zero)")
}

func TestGetStyleEconomicsBadRequest(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.GetStyleEconomics(context.Background(), &pb_admin.GetStyleEconomicsRequest{TechCardId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetStyleEconomicsNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardById(mock.Anything, 5).Return(nil, sql.ErrNoRows)
	s := &Server{repo: repo}
	_, err := s.GetStyleEconomics(context.Background(), &pb_admin.GetStyleEconomicsRequest{TechCardId: 5})
	require.Equal(t, codes.NotFound, status.Code(err))
}
