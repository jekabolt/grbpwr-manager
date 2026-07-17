package frontend

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetArchiveByPublicCode(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	archives := mocks.NewMockArchive(t)
	repo.EXPECT().Archive().Return(archives).Once()
	archives.EXPECT().GetArchiveByCode(mock.Anything, "AR000001").Return(&entity.ArchiveFull{
		ArchiveList: entity.ArchiveList{Id: 7, Code: "AR000001"},
	}, nil).Once()

	resp, err := (&Server{repo: repo}).GetArchive(context.Background(), &pb_frontend.GetArchiveRequest{
		Code: " AR000001 ",
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Archive)
	// R3: the storefront archive projection carries only the public code (no ArchiveList.id).
	assert.Equal(t, "AR000001", resp.Archive.ArchiveList.Code)
}

func TestGetArchiveByLegacyID(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	archives := mocks.NewMockArchive(t)
	repo.EXPECT().Archive().Return(archives).Once()
	archives.EXPECT().GetArchiveById(mock.Anything, 7).Return(&entity.ArchiveFull{
		ArchiveList: entity.ArchiveList{Id: 7, Code: "AR000001"},
	}, nil).Once()

	resp, err := (&Server{repo: repo}).GetArchive(context.Background(), &pb_frontend.GetArchiveRequest{
		Heading: "drop-one",
		Tag:     "summer",
		Id:      7,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Archive)
	assert.Equal(t, "AR000001", resp.Archive.ArchiveList.Code)
}

func TestGetArchiveRejectsMixedLookupVersions(t *testing.T) {
	_, err := (&Server{}).GetArchive(context.Background(), &pb_frontend.GetArchiveRequest{
		Code: "AR000001",
		Id:   7,
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetArchiveRejectsEmptyLookup(t *testing.T) {
	_, err := (&Server{}).GetArchive(context.Background(), &pb_frontend.GetArchiveRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
