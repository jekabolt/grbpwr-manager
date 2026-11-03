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

const taskFileKey = "files-library/2026/november/plan.xlsx"

func storedTaskFile(size int64) entity.LibraryFile {
	return entity.LibraryFile{Id: 7, LibraryFileInsert: entity.LibraryFileInsert{ObjectKey: taskFileKey, SizeBytes: size}}
}

// taskFileServer wires a task that has file 7 attached. Mockery fails the test on any undeclared
// call, so a case that declares no bucket read proves the bucket is never reached.
func taskFileServer(t *testing.T, taskErr error, files []entity.LibraryFile, fs *mocks.MockFileStore) *Server {
	t.Helper()
	tasks := mocks.NewMockTasks(t)
	tasks.EXPECT().GetTaskById(mock.Anything, 3).Return(&entity.Task{Id: 3, TaskInsert: entity.TaskInsert{FileIds: []int{7}}}, taskErr)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Tasks().Return(tasks)
	if files != nil {
		lib := mocks.NewMockFiles(t)
		lib.EXPECT().ListFilesByIds(mock.Anything, []int{7}).Return(files, nil)
		repo.EXPECT().Files().Return(lib)
	}
	return &Server{repo: repo, bucket: fs}
}

func TestGetTaskFileContentReturnsBytes(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, taskFileKey, int64(taskFileViewLimit)).Return([]byte("xlsx"), nil)
	s := taskFileServer(t, nil, []entity.LibraryFile{storedTaskFile(4)}, fs)

	resp, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.NoError(t, err)
	require.Equal(t, []byte("xlsx"), resp.GetContent())
	require.False(t, resp.GetTooLarge())
}

// tasks:read must not open the library: an id that is not attached to this task gets nothing, even
// though the file exists.
func TestGetTaskFileContentRefusesAFileOfAnotherTask(t *testing.T) {
	s := taskFileServer(t, nil, nil, mocks.NewMockFileStore(t))

	_, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 8})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetTaskFileContentMissingTaskIsNotFound(t *testing.T) {
	s := taskFileServer(t, sql.ErrNoRows, nil, mocks.NewMockFileStore(t))

	_, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// A restricted file drops out of ListFilesByIds: attached, yet the viewer may not see it.
func TestGetTaskFileContentHiddenFileIsNotFound(t *testing.T) {
	s := taskFileServer(t, nil, []entity.LibraryFile{}, mocks.NewMockFileStore(t))

	_, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// Over the ceiling by the row: no bucket read at all, and data rather than an error.
func TestGetTaskFileContentTooLargeByRowSkipsTheBucket(t *testing.T) {
	s := taskFileServer(t, nil, []entity.LibraryFile{storedTaskFile(taskFileViewLimit + 1)}, mocks.NewMockFileStore(t))

	resp, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.NoError(t, err)
	require.True(t, resp.GetTooLarge())
	require.Empty(t, resp.GetContent())
}

// The row can understate the object, then the bucket read is what says too large.
func TestGetTaskFileContentTooLargeByBucket(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, taskFileKey, int64(taskFileViewLimit)).Return(nil, bucket.ErrLibraryObjectTooLarge)
	s := taskFileServer(t, nil, []entity.LibraryFile{storedTaskFile(4)}, fs)

	resp, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.NoError(t, err)
	require.True(t, resp.GetTooLarge())
}

func TestGetTaskFileContentBucketFailureIsInternal(t *testing.T) {
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, taskFileKey, int64(taskFileViewLimit)).Return(nil, errors.New("boom"))
	s := taskFileServer(t, nil, []entity.LibraryFile{storedTaskFile(4)}, fs)

	_, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.Equal(t, codes.Internal, status.Code(err))
}

func TestGetTaskFileContentRequiresIds(t *testing.T) {
	s := &Server{}
	_, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 0, FileId: 7})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
