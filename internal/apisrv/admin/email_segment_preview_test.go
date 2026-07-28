package admin

import (
	"context"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/segment"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPreviewEmailSegmentCachesSavedCount(t *testing.T) {
	ctx := context.Background()
	repo := mocks.NewMockRepository(t)
	campaigns := mocks.NewMockCampaigns(t)
	repo.EXPECT().Campaigns().Return(campaigns).Once()
	campaigns.EXPECT().
		PreviewSegmentCount(ctx, entity.SegmentPredicate{}).
		Return(17, nil).
		Once()
	campaigns.EXPECT().SaveSegmentCount(ctx, 9, 17).Return(nil).Once()

	resp, err := (&Server{repo: repo}).PreviewEmailSegment(ctx, &pb_admin.PreviewEmailSegmentRequest{
		Segment: &pb_common.EmailSegment{Id: 9},
	})
	require.NoError(t, err)
	require.Equal(t, int32(17), resp.Count)
}

func TestPreviewEmailSegmentDoesNotCacheUnsavedCount(t *testing.T) {
	ctx := context.Background()
	repo := mocks.NewMockRepository(t)
	campaigns := mocks.NewMockCampaigns(t)
	repo.EXPECT().Campaigns().Return(campaigns).Once()
	campaigns.EXPECT().
		PreviewSegmentCount(ctx, entity.SegmentPredicate{}).
		Return(23, nil).
		Once()

	resp, err := (&Server{repo: repo}).PreviewEmailSegment(ctx, &pb_admin.PreviewEmailSegmentRequest{
		Segment: &pb_common.EmailSegment{},
	})
	require.NoError(t, err)
	require.Equal(t, int32(23), resp.Count)
}

func TestPreviewEmailSegmentMapsCompilerErrorsToInvalidArgument(t *testing.T) {
	ctx := context.Background()
	repo := mocks.NewMockRepository(t)
	campaigns := mocks.NewMockCampaigns(t)
	repo.EXPECT().Campaigns().Return(campaigns).Once()
	campaigns.EXPECT().
		PreviewSegmentCount(ctx, entity.SegmentPredicate{}).
		Return(0, fmt.Errorf("compile predicate: %w", segment.ErrUnknownField)).
		Once()

	_, err := (&Server{repo: repo}).PreviewEmailSegment(ctx, &pb_admin.PreviewEmailSegmentRequest{
		Segment: &pb_common.EmailSegment{},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
