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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateProductionRun freezes the planned unit cost from a linked tech_card_release (task 11).
func TestCreateProductionRunSnapshotsPlanFromRelease(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().ProductionRuns().Return(pr)

	tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			Id: 5, TechCardId: 7,
			UnitCost: decimal.NullDecimal{Decimal: decimal.RequireFromString("33.00"), Valid: true},
			Currency: sql.NullString{String: "EUR", Valid: true},
		},
	}, nil)

	var captured *entity.ProductionRunInsert
	pr.EXPECT().CreateProductionRun(mock.Anything, mock.MatchedBy(func(r *entity.ProductionRunInsert) bool {
		captured = r
		return true
	})).Return(11, nil)

	s := &Server{repo: repo}
	resp, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{
			TechCardId: 7,
			ReleaseId:  5,
			Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines:      []*pb_common.ProductionRunLine{{SizeId: 1, PlannedQty: 50}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(11), resp.Id)
	require.NotNil(t, captured)
	require.True(t, captured.PlannedUnitCost.Decimal.Equal(decimal.RequireFromString("33.00")), "plan cost snapshotted from release")
	require.Equal(t, "EUR", captured.PlannedCurrency.String)
}

func TestCreateProductionRunReleaseNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(nil, sql.ErrNoRows)

	s := &Server{repo: repo}
	_, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{
			TechCardId: 7, ReleaseId: 5,
			Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
		},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateProductionRunValidation(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "missing tech_card_id")
}

// receiveRun builds a mock repo whose run/card are fixed, wiring the ProductionRuns + TechCards
// mocks. run and card are returned so tests can assert on captured store args.
func receiveMocks(t *testing.T, run *entity.ProductionRun, card *entity.TechCard) (*mocks.MockRepository, *mocks.MockProductionRuns, *mocks.MockTechCards) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().ProductionRuns().Return(pr).Maybe()
	repo.EXPECT().TechCards().Return(tc).Maybe()
	pr.EXPECT().GetProductionRun(mock.Anything, run.Id).Return(run, nil).Maybe()
	if card != nil {
		tc.EXPECT().GetTechCardById(mock.Anything, run.TechCardId).Return(card, nil).Maybe()
	}
	return repo, pr, tc
}

// ReceiveProductionRun books each line's received qty into its product, sets cost_price when asked,
// and passes the product→size→qty map + base actual unit cost to the store.
func TestReceiveProductionRunHappyPath(t *testing.T) {
	run := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 60, ReceivedQty: sql.NullInt64{Int64: 58, Valid: true}},
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 2, PlannedQty: 40, ReceivedQty: sql.NullInt64{Int64: 40, Valid: true}},
			{ProductId: sql.NullInt32{Int32: 66, Valid: true}, SizeId: 1, PlannedQty: 20, ReceivedQty: sql.NullInt64{Int64: 20, Valid: true}},
		},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostMaterials, Amount: decimal.RequireFromString("1180"), Currency: "EUR", AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("1180"), Valid: true}},
		},
	}}
	card := &entity.TechCard{Id: 7}
	card.ProductIds = []int{55, 66}
	card.SizeIds = []int{1, 2}

	repo, pr, _ := receiveMocks(t, run, card)
	var gotPerProduct map[int]map[int]int
	var gotUpdateCostPrice bool
	// The store computes cost_price itself now (inside its tx); the handler passes the validated
	// per-product grid + updateCostPrice flag and returns whether the store seeded cost_price.
	pr.EXPECT().ReceiveProductionRun(mock.Anything, 4, mock.Anything, true, mock.Anything).
		RunAndReturn(func(_ context.Context, _ int, perProduct map[int]map[int]int, updateCostPrice bool, _ string) (bool, error) {
			gotPerProduct, gotUpdateCostPrice = perProduct, updateCostPrice
			return updateCostPrice, nil
		})

	s := &Server{repo: repo}
	resp, err := s.ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4, UpdateCostPrice: true})
	require.NoError(t, err)
	require.True(t, resp.CostPriceUpdated)
	require.True(t, gotUpdateCostPrice, "update-cost-price flag passed through")
	// each colour-model booked into its own product; 118 units received total across both.
	require.Equal(t, map[int]map[int]int{55: {1: 58, 2: 40}, 66: {1: 20}}, gotPerProduct)
}

// TestProductionRunActualUnitCostBase covers the trusted actual-unit-cost math (moved to the entity
// so the store can seed cost_price inside its transaction). 1180 base cost / 118 received = 10.
func TestProductionRunActualUnitCostBase(t *testing.T) {
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{
			{SizeId: 1, ReceivedQty: sql.NullInt64{Int64: 58, Valid: true}},
			{SizeId: 2, ReceivedQty: sql.NullInt64{Int64: 60, Valid: true}},
		},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostMaterials, AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("1180"), Valid: true}},
		},
	}}
	c := run.ActualUnitCostBase()
	require.True(t, c.Valid)
	require.True(t, c.Decimal.Equal(decimal.RequireFromString("10")), "1180 / 118 received, got %s", c.Decimal)

	// an uncosted manual article makes the figure untrustworthy → invalid.
	run.Costs = append(run.Costs, entity.ProductionRunCost{Kind: entity.ProductionRunCostCMT})
	require.False(t, run.ActualUnitCostBase().Valid, "partial fold → not trustworthy")
}

func TestReceiveProductionRunGuards(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.ProductIds = []int{55}
	recvLines := func(pid int32) []entity.ProductionRunLine {
		return []entity.ProductionRunLine{{ProductId: sql.NullInt32{Int32: pid, Valid: true}, SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 10, Valid: true}}}
	}

	// already received → FailedPrecondition (no store receive call)
	run1 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunReceived, Lines: recvLines(55)}}
	repo1, _, _ := receiveMocks(t, run1, card)
	_, err := (&Server{repo: repo1}).ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	// a received line's product not linked to the card → InvalidArgument
	run2 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress, Lines: recvLines(999)}}
	repo2, _, _ := receiveMocks(t, run2, card)
	_, err = (&Server{repo: repo2}).ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// a received line with no product → FailedPrecondition
	run3 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 10, Valid: true}}}}}
	repo3, _, _ := receiveMocks(t, run3, card)
	_, err = (&Server{repo: repo3}).ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	// no received quantities at all → FailedPrecondition
	run4 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10}}}}
	repo4, _, _ := receiveMocks(t, run4, card)
	_, err = (&Server{repo: repo4}).ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestGetProductionRunNotFoundAndList(t *testing.T) {
	// NotFound
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	pr.EXPECT().GetProductionRun(mock.Anything, 404).Return(nil, sql.ErrNoRows)
	s := &Server{repo: repo}
	_, err := s.GetProductionRun(context.Background(), &pb_admin.GetProductionRunRequest{Id: 404})
	require.Equal(t, codes.NotFound, status.Code(err))

	// invalid status filter → InvalidArgument (no store call)
	s2 := &Server{repo: mocks.NewMockRepository(t)}
	_, err = s2.ListProductionRuns(context.Background(), &pb_admin.ListProductionRunsRequest{Status: "bogus"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
