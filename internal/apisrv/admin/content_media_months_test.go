package admin

import (
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListMediaMonths(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media).Once()
	media.EXPECT().ListMediaMonths(mock.Anything).Return([]entity.MonthCount{
		{Year: 2026, Month: 10, Count: 17},
		{Year: 2026, Month: 8, Count: 4},
		{Year: 2025, Month: 12, Count: 9},
	}, nil).Once()

	got, err := (&Server{repo: repo}).ListMediaMonths(t.Context(), &pb_admin.ListMediaMonthsRequest{})
	require.NoError(t, err)
	require.Equal(t, []*pb_admin.MonthCount{
		{Year: 2026, Month: 10, Count: 17},
		{Year: 2026, Month: 8, Count: 4},
		{Year: 2025, Month: 12, Count: 9},
	}, got.Months)
}

func TestListMediaMonthsStoreFailure(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media).Once()
	media.EXPECT().ListMediaMonths(mock.Anything).Return(nil, errors.New("database unavailable")).Once()

	got, err := (&Server{repo: repo}).ListMediaMonths(t.Context(), &pb_admin.ListMediaMonthsRequest{})
	require.Nil(t, got)
	require.Equal(t, codes.Internal, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "failed to list media months")
}
