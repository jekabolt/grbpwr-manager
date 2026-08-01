package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func cloneSourceCard() *entity.TechCard {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	return &entity.TechCard{
		Id:          7,
		LockVersion: 4,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber:       sql.NullString{String: "SS25-0042", Valid: true},
			StyleNumberSource: entity.StyleNumberSourceManual,
			Name:              "Source Style",
			SeasonCode:        sql.NullString{String: "SS", Valid: true},
			SeasonYear:        sql.NullInt32{Int32: 2025, Valid: true},
			Stage:             entity.TechCardStageProto,
			ApprovalState:     entity.TechCardApprovalReleased,
			MeasurementUnit:   entity.TechCardUnitMm,
			Purpose:           entity.TechCardPurposeSellable,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func cloneRequest() *pb_admin.CloneStyleForSeasonRequest {
	return &pb_admin.CloneStyleForSeasonRequest{
		SourceStyleId:         7,
		ExpectedSourceVersion: 4,
		SkuSeason: &pb_common.SkuSeason{
			Code: pb_common.SeasonEnum_SEASON_ENUM_FW,
			Year: 2026,
		},
	}
}

func TestCloneStyleForSeasonUsesGeneratedNumberAndRechecksSource(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(cloneSourceCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	tc.EXPECT().GetTechCardLockVersion(mock.Anything, 7).Return(4, nil)
	tc.EXPECT().AddTechCard(mock.Anything, mock.AnythingOfType("*entity.TechCardInsert")).
		Run(func(_ context.Context, insert *entity.TechCardInsert) {
			require.Equal(t, "FW26-0007", insert.StyleNumber.String)
			require.True(t, insert.StyleNumber.Valid)
			require.Equal(t, entity.StyleNumberSourceGenerated, insert.StyleNumberSource)
			require.Equal(t, entity.TechCardApprovalDraft, insert.ApprovalState)
			require.Equal(t, entity.TechCardStageProto, insert.Stage, "clone preserves the source stage")
			require.Equal(t, "FW", insert.SeasonCode.String)
			require.Equal(t, int32(2026), insert.SeasonYear.Int32)
		}).Return(11, nil)

	resp, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.NoError(t, err)
	require.Equal(t, int32(11), resp.NewStyleId)
}

func TestCloneStyleForSeasonRejectsSourceChangedBeforeInsert(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(cloneSourceCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	tc.EXPECT().GetTechCardLockVersion(mock.Anything, 7).Return(5, nil)

	_, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.Equal(t, codes.Aborted, status.Code(err))
}
