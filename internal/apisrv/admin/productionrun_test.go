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
			Sizes:      []*pb_common.ProductionRunSize{{SizeId: 1, PlannedQty: 50}},
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

// ReceiveProductionRun increments stock, sets cost_price when asked, and passes the per-size
// received map + base actual unit cost to the store.
func TestReceiveProductionRunHappyPath(t *testing.T) {
	run := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Status: entity.ProductionRunInProgress,
		Sizes: []entity.ProductionRunSize{
			{SizeId: 1, PlannedQty: 60, ReceivedQty: sql.NullInt64{Int64: 58, Valid: true}},
			{SizeId: 2, PlannedQty: 40, ReceivedQty: sql.NullInt64{Int64: 40, Valid: true}},
		},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostMaterials, Amount: decimal.RequireFromString("980"), Currency: "EUR", AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("980"), Valid: true}},
		},
	}}
	card := &entity.TechCard{Id: 7}
	card.ProductIds = []int{55}

	repo, pr, _ := receiveMocks(t, run, card)
	var gotPerSize map[int]int
	var gotCost decimal.NullDecimal
	pr.EXPECT().ReceiveProductionRun(mock.Anything, 4, 55, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ int, perSize map[int]int, _ string, cost decimal.NullDecimal) error {
			gotPerSize, gotCost = perSize, cost
			return nil
		})

	s := &Server{repo: repo}
	resp, err := s.ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4, ProductId: 55, UpdateCostPrice: true})
	require.NoError(t, err)
	require.True(t, resp.CostPriceUpdated)
	require.Equal(t, map[int]int{1: 58, 2: 40}, gotPerSize)
	require.True(t, gotCost.Valid)
	require.True(t, gotCost.Decimal.Equal(decimal.RequireFromString("10")), "980 / 98 received")
}

func TestReceiveProductionRunGuards(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.ProductIds = []int{55}
	recvSizes := []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 10, Valid: true}}}

	// already received → FailedPrecondition (no store receive call)
	run1 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunReceived, Sizes: recvSizes}}
	repo1, _, _ := receiveMocks(t, run1, card)
	_, err := (&Server{repo: repo1}).ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4, ProductId: 55})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	// product not linked to the card → InvalidArgument
	run2 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress, Sizes: recvSizes}}
	repo2, _, _ := receiveMocks(t, run2, card)
	_, err = (&Server{repo: repo2}).ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4, ProductId: 999})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// no received quantities → FailedPrecondition
	run3 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress,
		Sizes: []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 10}}}}
	repo3, _, _ := receiveMocks(t, run3, card)
	_, err = (&Server{repo: repo3}).ReceiveProductionRun(context.Background(), &pb_admin.ReceiveProductionRunRequest{RunId: 4, ProductId: 55})
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
