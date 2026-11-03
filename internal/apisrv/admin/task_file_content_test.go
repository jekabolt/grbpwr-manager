package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
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

// ---- the read carries the version ----

func TestGetTaskFileContentReturnsTheRowVersion(t *testing.T) {
	stamp := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f := storedTaskFile(4)
	f.Sha256 = "row-sha"
	f.ContentUpdatedBy = "kirill"
	f.ContentUpdatedAt = sql.NullTime{Time: stamp, Valid: true}
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetLibraryObjectWithin(mock.Anything, taskFileKey, int64(taskFileViewLimit)).Return([]byte("xlsx"), nil)
	s := taskFileServer(t, nil, []entity.LibraryFile{f}, fs)

	resp, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.NoError(t, err)
	require.Equal(t, "row-sha", resp.GetSha256())
	require.Equal(t, "kirill", resp.GetLastEditedBy())
	require.True(t, resp.GetLastEditedAt().AsTime().Equal(stamp))
}

// A file over the ceiling still says which version it is, and a never edited one has no stamp.
func TestGetTaskFileContentTooLargeStillCarriesTheVersion(t *testing.T) {
	f := storedTaskFile(taskFileViewLimit + 1)
	f.Sha256 = "row-sha"
	s := taskFileServer(t, nil, []entity.LibraryFile{f}, mocks.NewMockFileStore(t))

	resp, err := s.GetTaskFileContent(context.Background(), &pb_admin.GetTaskFileContentRequest{TaskId: 3, FileId: 7})
	require.NoError(t, err)
	require.True(t, resp.GetTooLarge())
	require.Equal(t, "row-sha", resp.GetSha256())
	require.Nil(t, resp.GetLastEditedAt())
}

// ---- the save ----

const (
	savedTaskFileKey = "files-library/2026/november/new.xlsx"
	savedPreviewKey  = "files-library/previews/old.webp"
	savedSheetType   = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// sheetFile is file 7 under a name: the kind rule reads the name.
func sheetFile(name string) entity.LibraryFile {
	f := storedTaskFile(4)
	f.FileName = name
	f.ContentType = savedSheetType
	f.Sha256 = "base-sha"
	return f
}

// xlsxBytes is the smallest thing the check accepts: a zip that has the workbook part.
func xlsxBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("xl/workbook.xml")
	require.NoError(t, err)
	_, err = w.Write([]byte("<workbook/>"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// saveServer is taskFileServer plus the write. With save nil the store write is undeclared, so
// reaching it fails the test; so does any bucket call the case did not declare.
func saveServer(t *testing.T, row entity.LibraryFile, fs *mocks.MockFileStore, save func(*mocks.MockFiles)) *Server {
	t.Helper()
	tasks := mocks.NewMockTasks(t)
	tasks.EXPECT().GetTaskById(mock.Anything, 3).Return(&entity.Task{Id: 3, TaskInsert: entity.TaskInsert{FileIds: []int{7}}}, nil)
	lib := mocks.NewMockFiles(t)
	lib.EXPECT().ListFilesByIds(mock.Anything, []int{7}).Return([]entity.LibraryFile{row}, nil)
	if save != nil {
		save(lib)
	}
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Tasks().Return(tasks)
	repo.EXPECT().Files().Return(lib)
	return &Server{repo: repo, bucket: fs}
}

func saveReq(content []byte) *pb_admin.SaveTaskFileContentRequest {
	return &pb_admin.SaveTaskFileContentRequest{TaskId: 3, FileId: 7, Content: content, BaseSha256: "base-sha"}
}

// expectUpload declares the one upload a save makes and checks what it was handed.
func expectUpload(fs *mocks.MockFileStore, content []byte, contentType, ext string) *mocks.MockFileStore_UploadLibraryObject_Call {
	return fs.EXPECT().UploadLibraryObject(mock.Anything, mock.MatchedBy(func(r io.Reader) bool {
		got, err := io.ReadAll(r)
		return err == nil && bytes.Equal(got, content)
	}), contentType, ext).Return(savedTaskFileKey, "new-sha", int64(len(content)), nil)
}

func TestSaveTaskFileContentSuccess(t *testing.T) {
	content := xlsxBytes(t)
	stamp := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var order []string

	fs := mocks.NewMockFileStore(t)
	expectUpload(fs, content, savedSheetType, "xlsx").Run(func(context.Context, io.Reader, string, string) { order = append(order, "upload") })
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, taskFileKey).RunAndReturn(func(context.Context, ...string) error {
		order = append(order, "remove old")
		return nil
	})

	s := saveServer(t, sheetFile("План Пошива.XLSX"), fs, func(lib *mocks.MockFiles) {
		lib.EXPECT().SaveFileContent(mock.Anything, entity.LibraryFileContentSave{
			FileId: 7, BaseSha256: "base-sha", ObjectKey: savedTaskFileKey, Sha256: "new-sha", SizeBytes: int64(len(content)), EditedBy: "pasha",
		}).RunAndReturn(func(context.Context, entity.LibraryFileContentSave) (*entity.LibraryFileContentSaveResult, error) {
			order = append(order, "row")
			return &entity.LibraryFileContentSaveResult{
				CurrentSha256: "new-sha", SizeBytes: int64(len(content)), LastEditedBy: "pasha",
				LastEditedAt: sql.NullTime{Time: stamp, Valid: true}, PreviousObjectKey: taskFileKey,
			}, nil
		})
	})

	resp, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq(content))
	require.NoError(t, err)
	require.False(t, resp.GetConflict())
	require.Equal(t, "new-sha", resp.GetCurrentSha256())
	require.Equal(t, "pasha", resp.GetLastEditedBy())
	require.True(t, resp.GetLastEditedAt().AsTime().Equal(stamp))
	require.Equal(t, int64(len(content)), resp.GetSizeBytes())
	require.Equal(t, []string{"upload", "row", "remove old"}, order, "the old object goes only after the row is committed")
}

func TestSaveTaskFileContentCsvKeepsTheRowContentType(t *testing.T) {
	row := sheetFile("plan.csv")
	row.ContentType = "text/csv"
	fs := mocks.NewMockFileStore(t)
	expectUpload(fs, []byte("a;b\n"), "text/csv", "csv")
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, taskFileKey).Return(nil)
	s := saveServer(t, row, fs, func(lib *mocks.MockFiles) {
		lib.EXPECT().SaveFileContent(mock.Anything, mock.Anything).
			Return(&entity.LibraryFileContentSaveResult{CurrentSha256: "new-sha", PreviousObjectKey: taskFileKey}, nil)
	})

	_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq([]byte("a;b\n")))
	require.NoError(t, err)
}

// A csv uploaded as text/plain is a csv. The note check of the library would call it a note.
func TestSaveTaskFileContentCsvAsTextPlainIsAllowed(t *testing.T) {
	row := sheetFile("plan.csv")
	row.ContentType = "text/plain"
	fs := mocks.NewMockFileStore(t)
	expectUpload(fs, []byte("a,b\n"), "text/plain", "csv")
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, taskFileKey).Return(nil)
	s := saveServer(t, row, fs, func(lib *mocks.MockFiles) {
		lib.EXPECT().SaveFileContent(mock.Anything, mock.Anything).
			Return(&entity.LibraryFileContentSaveResult{CurrentSha256: "new-sha", PreviousObjectKey: taskFileKey}, nil)
	})

	_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq([]byte("a,b\n")))
	require.NoError(t, err)
}

// A conflict is data. Only the object just uploaded goes; the stored one stays.
func TestSaveTaskFileContentConflictCleansTheNewObject(t *testing.T) {
	content := xlsxBytes(t)
	stamp := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	fs := mocks.NewMockFileStore(t)
	expectUpload(fs, content, savedSheetType, "xlsx")
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, savedTaskFileKey).Return(nil).Once()
	s := saveServer(t, sheetFile("plan.xlsx"), fs, func(lib *mocks.MockFiles) {
		lib.EXPECT().SaveFileContent(mock.Anything, mock.Anything).Return(&entity.LibraryFileContentSaveResult{
			Conflict: true, CurrentSha256: "their-sha", SizeBytes: 99, LastEditedBy: "kirill",
			LastEditedAt: sql.NullTime{Time: stamp, Valid: true},
		}, nil)
	})

	resp, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq(content))
	require.NoError(t, err)
	require.True(t, resp.GetConflict())
	require.Equal(t, "their-sha", resp.GetCurrentSha256())
	require.Equal(t, "kirill", resp.GetLastEditedBy())
	require.Equal(t, int64(99), resp.GetSizeBytes())
	require.True(t, resp.GetLastEditedAt().AsTime().Equal(stamp))
}

func TestSaveTaskFileContentForceReachesTheStore(t *testing.T) {
	content := xlsxBytes(t)
	fs := mocks.NewMockFileStore(t)
	expectUpload(fs, content, savedSheetType, "xlsx")
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, taskFileKey).Return(nil)
	s := saveServer(t, sheetFile("plan.xlsx"), fs, func(lib *mocks.MockFiles) {
		lib.EXPECT().SaveFileContent(mock.Anything, mock.MatchedBy(func(in entity.LibraryFileContentSave) bool {
			return in.Force && in.BaseSha256 == "stale-sha"
		})).Return(&entity.LibraryFileContentSaveResult{CurrentSha256: "new-sha", PreviousObjectKey: taskFileKey}, nil)
	})

	resp, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), &pb_admin.SaveTaskFileContentRequest{
		TaskId: 3, FileId: 7, Content: content, BaseSha256: "stale-sha", Force: true,
	})
	require.NoError(t, err)
	require.False(t, resp.GetConflict())
}

func TestSaveTaskFileContentRemovesThePreviewToo(t *testing.T) {
	content := xlsxBytes(t)
	fs := mocks.NewMockFileStore(t)
	expectUpload(fs, content, savedSheetType, "xlsx")
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, taskFileKey, savedPreviewKey).Return(nil).Once()
	s := saveServer(t, sheetFile("plan.xlsx"), fs, func(lib *mocks.MockFiles) {
		lib.EXPECT().SaveFileContent(mock.Anything, mock.Anything).Return(&entity.LibraryFileContentSaveResult{
			CurrentSha256: "new-sha", PreviousObjectKey: taskFileKey, PreviousPreviewKey: savedPreviewKey,
		}, nil)
	})

	_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq(content))
	require.NoError(t, err)
}

// A failed store write cleans the new object and leaves the old one alone: only the new key is
// declared, so removing anything else fails the test.
func TestSaveTaskFileContentStoreFailureCleansTheNewObjectOnly(t *testing.T) {
	cases := map[string]struct {
		err  error
		code codes.Code
	}{
		"failure":        {errors.New("boom"), codes.Internal},
		"gone meanwhile": {sql.ErrNoRows, codes.NotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			content := xlsxBytes(t)
			fs := mocks.NewMockFileStore(t)
			expectUpload(fs, content, savedSheetType, "xlsx")
			fs.EXPECT().RemoveObjectsByKeys(mock.Anything, savedTaskFileKey).Return(nil).Once()
			s := saveServer(t, sheetFile("plan.xlsx"), fs, func(lib *mocks.MockFiles) {
				lib.EXPECT().SaveFileContent(mock.Anything, mock.Anything).Return(nil, tc.err)
			})

			_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq(content))
			require.Equal(t, tc.code, status.Code(err))
		})
	}
}

func TestSaveTaskFileContentUploadFailureNeverReachesTheRow(t *testing.T) {
	content := xlsxBytes(t)
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().UploadLibraryObject(mock.Anything, mock.Anything, savedSheetType, "xlsx").Return("", "", 0, errors.New("boom"))
	s := saveServer(t, sheetFile("plan.xlsx"), fs, nil)

	_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq(content))
	require.Equal(t, codes.Internal, status.Code(err))
}

// All of these answer before the bucket: no upload is declared, so one would fail the test.
func TestSaveTaskFileContentNotAttachedIsNotFound(t *testing.T) {
	s := taskFileServer(t, nil, nil, mocks.NewMockFileStore(t))
	_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), &pb_admin.SaveTaskFileContentRequest{TaskId: 3, FileId: 8, Content: []byte("a")})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSaveTaskFileContentHiddenFileIsNotFound(t *testing.T) {
	s := taskFileServer(t, nil, []entity.LibraryFile{}, mocks.NewMockFileStore(t))
	_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq([]byte("a")))
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSaveTaskFileContentMissingTaskIsNotFound(t *testing.T) {
	s := taskFileServer(t, sql.ErrNoRows, nil, mocks.NewMockFileStore(t))
	_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq([]byte("a")))
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSaveTaskFileContentRequiresIds(t *testing.T) {
	s := &Server{}
	_, err := s.SaveTaskFileContent(context.Background(), &pb_admin.SaveTaskFileContentRequest{FileId: 7})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.SaveTaskFileContent(context.Background(), &pb_admin.SaveTaskFileContentRequest{TaskId: 3})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestSaveTaskFileContentRefusals(t *testing.T) {
	over := make([]byte, taskFileViewLimit+1)
	cases := map[string]struct {
		name    string
		content []byte
		code    codes.Code
	}{
		"old xls":       {"plan.xls", []byte("a"), codes.FailedPrecondition},
		"note":          {"notes.md", []byte("a"), codes.FailedPrecondition},
		"pdf":           {"plan.pdf", []byte("a"), codes.FailedPrecondition},
		"no extension":  {"plan", []byte("a"), codes.FailedPrecondition},
		"garbage xlsx":  {"plan.xlsx", []byte("this is not a zip"), codes.InvalidArgument},
		"empty xlsx":    {"plan.xlsx", nil, codes.InvalidArgument},
		"empty csv":     {"plan.csv", nil, codes.InvalidArgument},
		"over ceiling":  {"plan.csv", over, codes.InvalidArgument},
		"zip, no sheet": {"plan.xlsx", zipWithout(t), codes.InvalidArgument},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := saveServer(t, sheetFile(tc.name), mocks.NewMockFileStore(t), nil)
			_, err := s.SaveTaskFileContent(ctxAs("pasha", false, nil), saveReq(tc.content))
			require.Equal(t, tc.code, status.Code(err))
		})
	}
}

// zipWithout is a valid zip that is not a workbook, like a .docx under a wrong name.
func zipWithout(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	require.NoError(t, err)
	_, err = w.Write([]byte("<document/>"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestSaveTaskFileContentRbac(t *testing.T) {
	const method = "/admin.AdminService/SaveTaskFileContent"
	require.True(t, rbac.Authorize(method, false, false, map[string]entity.AccessLevel{rbac.SectionTasks: entity.AccessWrite}))
	require.False(t, rbac.Authorize(method, false, false, map[string]entity.AccessLevel{rbac.SectionTasks: entity.AccessRead}))
	require.False(t, rbac.Authorize(method, false, false, map[string]entity.AccessLevel{rbac.SectionFiles: entity.AccessWrite}))
}
