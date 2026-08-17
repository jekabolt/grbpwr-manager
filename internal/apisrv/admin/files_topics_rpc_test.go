package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMergeFileTopicsValidation: a merge is irreversible, so the two requests
// that cannot mean anything — a missing id and a topic merged into itself — are
// refused instead of being answered "done".
func TestMergeFileTopicsValidation(t *testing.T) {
	s := &Server{}
	ctx := context.Background()

	if _, err := s.MergeFileTopics(ctx, &pb_admin.MergeFileTopicsRequest{TargetId: 2}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing source: want InvalidArgument, got %v", err)
	}
	if _, err := s.MergeFileTopics(ctx, &pb_admin.MergeFileTopicsRequest{SourceId: 2, TargetId: 2}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("self merge: want InvalidArgument, got %v", err)
	}

	files := mocks.NewMockFiles(t)
	files.EXPECT().MergeTopics(mock.Anything, 1, 2).Return(0, sql.ErrNoRows)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s2 := &Server{repo: repo}
	if _, err := s2.MergeFileTopics(ctx, &pb_admin.MergeFileTopicsRequest{SourceId: 1, TargetId: 2}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown topic: want NotFound, got %v", err)
	}
}

// TestAssignLibraryFileTopicsPassesDedupedSelection: the bulk write is additive,
// and the ids reach the store deduped — a chip clicked twice must not become two
// identical writes.
func TestAssignLibraryFileTopicsPassesDedupedSelection(t *testing.T) {
	ctx := context.Background()

	s := &Server{}
	if _, err := s.AssignLibraryFileTopics(ctx, &pb_admin.AssignLibraryFileTopicsRequest{TopicIds: []int32{1}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no files: want InvalidArgument, got %v", err)
	}
	if _, err := s.AssignLibraryFileTopics(ctx, &pb_admin.AssignLibraryFileTopicsRequest{FileIds: []int32{1}}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no topics: want InvalidArgument, got %v", err)
	}

	files := mocks.NewMockFiles(t)
	files.EXPECT().AssignTopics(mock.Anything, []int{5, 6}, []int{3}, []string{"фурнитура"}).Return(2, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	s2 := &Server{repo: repo}
	resp, err := s2.AssignLibraryFileTopics(ctx, &pb_admin.AssignLibraryFileTopicsRequest{
		FileIds:   []int32{5, 6, 5},
		TopicIds:  []int32{3, 3},
		NewTopics: []string{"фурнитура", "ФУРНИТУРА"},
	})
	require.NoError(t, err)
	require.Equal(t, int32(2), resp.Assigned)
}

// TestListLibraryFilesTopicFilterBounds: the intersection filter is bounded at
// the API edge, so an oversized set is a 400 rather than 200 EXISTS subqueries.
func TestListLibraryFilesTopicFilterBounds(t *testing.T) {
	many := make([]int32, entity.MaxLibraryTopicFilters+1)
	for i := range many {
		many[i] = int32(i + 1)
	}
	s := &Server{}
	if _, err := s.ListLibraryFiles(context.Background(), &pb_admin.ListLibraryFilesRequest{TopicIds: many}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("oversized filter: want InvalidArgument, got %v", err)
	}
}
