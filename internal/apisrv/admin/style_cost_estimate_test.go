package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ndDec(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}
func nsStr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// TestGetStyleCostEstimateHappyPath: full access, one all-EUR BOM line + a cmt article, no production
// actuals yet, a cost_price snapshot on the colourway. Verifies the estimate math and the plan-vs-fact
// comparison (estimate vs snapshot; actual absent).
func TestGetStyleCostEstimateHappyPath(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	pr := mocks.NewMockProductionRuns(t)
	mtr := mocks.NewMockMetrics(t)
	prod := mocks.NewMockProducts(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().ProductionRuns().Return(pr)
	repo.EXPECT().Metrics().Return(mtr)
	repo.EXPECT().Products().Return(prod)

	card := &entity.TechCard{Id: 7}
	card.StyleNumber = nsStr("S-7")
	card.Name = "Coat"
	card.BomItems = []entity.TechCardBomItem{
		{Id: 100, Name: "Fabric", Section: entity.BomSectionFabric, Unit: nsStr("m"), UnitPrice: ndDec("10"), Currency: nsStr("EUR")},
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Black", ProductId: sql.NullInt32{Int32: 55, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: sql.NullInt32{Int32: 0, Valid: true}, Consumption: ndDec("2")},
		}},
	}
	card.Costing = &entity.TechCardCosting{CmtCost: ndDec("5"), Currency: nsStr("EUR")}

	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil)
	pr.EXPECT().ListProductionRuns(mock.Anything, styleCostRunScan, 0, entity.ProductionRunListFilter{TechCardId: 7}).Return(nil, 0, nil)
	mtr.EXPECT().GetStyleMaterialsFromStock(mock.Anything, 7).Return(entity.StyleMaterialsFromStock{}, nil)
	prod.EXPECT().GetProductCostInfo(mock.Anything, 55).Return(&entity.ColorwayCostInfo{
		CostPrice:       ndDec("24"),
		CostPriceSource: nsStr("production_run"),
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetStyleCostEstimate(context.Background(), &pb_admin.GetStyleCostEstimateRequest{TechCardId: 7})
	require.NoError(t, err)
	e := resp.Estimate
	// materials 2×10 = 20 ; + cmt 5 ; defect 0 → unit 25.00
	require.Equal(t, "20.00", e.MaterialsPerUnitBase.Value)
	require.Equal(t, "25.00", e.UnitCostBase.Value)
	require.Equal(t, int64(55), e.ColorwayId)
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_BOM_SNAPSHOT, e.Materials[0].PriceSource)

	c := e.Comparison
	require.NotNil(t, c)
	require.Equal(t, "25.00", c.EstimateUnitCostBase.Value)
	require.False(t, c.HasActual, "no production runs → no actual")
	require.True(t, c.HasSnapshot)
	require.Equal(t, "24.00", c.SnapshotCostBase.Value)
	require.Equal(t, "production_run", c.SnapshotSource)
	require.Equal(t, "-1.00", c.EstimateVsSnapshot.Value) // 24 − 25
}

func TestGetStyleCostEstimateNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(nil, sql.ErrNoRows)

	s := &Server{repo: repo}
	_, err := s.GetStyleCostEstimate(context.Background(), &pb_admin.GetStyleCostEstimateRequest{TechCardId: 7})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetStyleCostEstimateBadRequest(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.GetStyleCostEstimate(context.Background(), &pb_admin.GetStyleCostEstimateRequest{TechCardId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestStripStyleCostEstimate: without costing:read every money figure is cleared while the material/
// article STRUCTURE (identity, section, consumption, provenance label, article kind) survives.
func TestStripStyleCostEstimate(t *testing.T) {
	resp := &pb_admin.GetStyleCostEstimateResponse{Estimate: &pb_admin.StyleCostEstimate{
		TechCardId:           7,
		Name:                 "Coat",
		MaterialsPerUnitBase: &pb_decimal.Decimal{Value: "27.00"},
		UnitCostBase:         &pb_decimal.Decimal{Value: "38.50"},
		OrderCostBase:        &pb_decimal.Decimal{Value: "577.50"},
		DefectPct:            &pb_decimal.Decimal{Value: "10"},
		Materials: []*pb_admin.StyleCostMaterialLine{{
			BomItemId: 100, MaterialName: "Fabric", Section: "fabric", Unit: "m",
			Consumption:   &pb_decimal.Decimal{Value: "2"},
			UnitPrice:     &pb_decimal.Decimal{Value: "10"},
			Currency:      "EUR",
			PriceSource:   pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_BOM_SNAPSHOT,
			LineTotalBase: &pb_decimal.Decimal{Value: "21.00"},
			HasBase:       true,
		}},
		Articles: []*pb_admin.StyleCostArticleLine{{
			Kind: "cmt", Amount: &pb_decimal.Decimal{Value: "5"}, Currency: "EUR",
			AmountBase: &pb_decimal.Decimal{Value: "5.00"}, HasBase: true,
		}},
		Comparison: &pb_admin.StyleCostComparison{
			EstimateUnitCostBase: &pb_decimal.Decimal{Value: "38.50"},
			SnapshotCostBase:     &pb_decimal.Decimal{Value: "24.00"},
			HasSnapshot:          true,
		},
	}}

	stripStyleCostEstimate(resp)
	e := resp.Estimate
	// structure kept
	require.Equal(t, "Coat", e.Name)
	require.Equal(t, "Fabric", e.Materials[0].MaterialName)
	require.Equal(t, "fabric", e.Materials[0].Section)
	require.Equal(t, "2", e.Materials[0].Consumption.Value)
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_BOM_SNAPSHOT, e.Materials[0].PriceSource)
	require.Equal(t, "cmt", e.Articles[0].Kind)
	// money gone
	require.Nil(t, e.MaterialsPerUnitBase)
	require.Nil(t, e.UnitCostBase)
	require.Nil(t, e.OrderCostBase)
	require.Nil(t, e.DefectPct)
	require.Nil(t, e.Materials[0].UnitPrice)
	require.Nil(t, e.Materials[0].LineTotalBase)
	require.Empty(t, e.Materials[0].Currency)
	require.False(t, e.Materials[0].HasBase)
	require.Nil(t, e.Articles[0].Amount)
	require.Nil(t, e.Articles[0].AmountBase)
	require.Nil(t, e.Comparison, "the whole plan/fact comparison is money")
}
