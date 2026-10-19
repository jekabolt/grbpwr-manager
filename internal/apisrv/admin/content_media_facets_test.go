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

func TestListMediaFacets(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media).Once()
	media.EXPECT().ListMediaFacets(mock.Anything).Return([]entity.MediaFacetRow{
		{IsVideo: false, Width: 200, Height: 200, InUse: false, Count: 7},
		{IsVideo: true, Width: 320, Height: 180, InUse: true, Count: 3},
		{IsVideo: true, Width: 0, Height: 0, InUse: false, Count: 2},
	}, 12, nil).Once()

	got, err := (&Server{repo: repo}).ListMediaFacets(t.Context(), &pb_admin.ListMediaFacetsRequest{})
	require.NoError(t, err)
	require.Equal(t, int32(12), got.Total)
	require.Equal(t, []*pb_admin.MediaFacetRow{
		{IsVideo: false, Width: 200, Height: 200, InUse: false, Count: 7},
		{IsVideo: true, Width: 320, Height: 180, InUse: true, Count: 3},
		{IsVideo: true, Width: 0, Height: 0, InUse: false, Count: 2},
	}, got.Rows)
}

func TestListMediaFacetsStoreFailure(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media).Once()
	media.EXPECT().ListMediaFacets(mock.Anything).Return(nil, 0, errors.New("database unavailable")).Once()

	got, err := (&Server{repo: repo}).ListMediaFacets(t.Context(), &pb_admin.ListMediaFacetsRequest{})
	require.Nil(t, got)
	require.Equal(t, codes.Internal, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "failed to list media facets")
}
