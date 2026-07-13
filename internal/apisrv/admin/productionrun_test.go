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
