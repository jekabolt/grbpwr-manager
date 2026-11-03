package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// libraryFileServer wires GetFileById for file 7. Mockery fails the test on any undeclared call, so
// a case that declares no bucket read proves the bucket is never reached. The people level itself is
// proved against the real predicate in internal/store (library_file_content_integration_test.go).
func libraryFileServer(t *testing.T, f *entity.LibraryFile, fileErr error, fs *mocks.MockFileStore) *Server {
	t.Helper()
	lib := mocks.NewMockFiles(t)
	lib.EXPECT().GetFileById(mock.Anything, 7).Return(f, fileErr)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(lib)
	return &Server{repo: repo, bucket: fs}
}

func TestGetLibraryFileContentReturnsBytes(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, taskFileKey, int64(taskFileViewLimit)).Return([]byte("xlsx"), nil)
	f := storedTaskFile(4)
	s := libraryFileServer(t, &f, nil, fs)

	resp, err := s.GetLibraryFileContent(context.Background(), &pb_admin.GetLibraryFileContentRequest{Id: 7})
	require.NoError(t, err)
	require.Equal(t, []byte("xlsx"), resp.GetContent())
	require.False(t, resp.GetTooLarge())
}

// Hidden, deleted and unknown all arrive from the store as ErrNoRows, and nothing reaches the bucket.
func TestGetLibraryFileContentNoRowIsNotFound(t *testing.T) {
	s := libraryFileServer(t, nil, sql.ErrNoRows, mocks.NewMockFileStore(t))

	_, err := s.GetLibraryFileContent(context.Background(), &pb_admin.GetLibraryFileContentRequest{Id: 7})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetLibraryFileContentStoreFailureIsInternal(t *testing.T) {
	s := libraryFileServer(t, nil, errors.New("boom"), mocks.NewMockFileStore(t))

	_, err := s.GetLibraryFileContent(context.Background(), &pb_admin.GetLibraryFileContentRequest{Id: 7})
	require.Equal(t, codes.Internal, status.Code(err))
}

// Over the ceiling by the row: no bucket read at all, and data rather than an error.
func TestGetLibraryFileContentTooLargeByRowSkipsTheBucket(t *testing.T) {
	f := storedTaskFile(taskFileViewLimit + 1)
	s := libraryFileServer(t, &f, nil, mocks.NewMockFileStore(t))

	resp, err := s.GetLibraryFileContent(context.Background(), &pb_admin.GetLibraryFileContentRequest{Id: 7})
	require.NoError(t, err)
	require.True(t, resp.GetTooLarge())
	require.Empty(t, resp.GetContent())
}

// The row can understate the object, then the bucket read is what says too large.
func TestGetLibraryFileContentTooLargeByBucket(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, taskFileKey, int64(taskFileViewLimit)).Return(nil, bucket.ErrLibraryObjectTooLarge)
	f := storedTaskFile(4)
	s := libraryFileServer(t, &f, nil, fs)

	resp, err := s.GetLibraryFileContent(context.Background(), &pb_admin.GetLibraryFileContentRequest{Id: 7})
	require.NoError(t, err)
	require.True(t, resp.GetTooLarge())
	require.Empty(t, resp.GetContent())
}

func TestGetLibraryFileContentBucketFailureIsInternal(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, taskFileKey, int64(taskFileViewLimit)).Return(nil, errors.New("boom"))
	f := storedTaskFile(4)
	s := libraryFileServer(t, &f, nil, fs)

	_, err := s.GetLibraryFileContent(context.Background(), &pb_admin.GetLibraryFileContentRequest{Id: 7})
	require.Equal(t, codes.Internal, status.Code(err))
}

func TestGetLibraryFileContentRequiresId(t *testing.T) {
	s := &Server{}
	_, err := s.GetLibraryFileContent(context.Background(), &pb_admin.GetLibraryFileContentRequest{Id: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
