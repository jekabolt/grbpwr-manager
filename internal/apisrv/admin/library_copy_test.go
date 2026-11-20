package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// libraryWithFolders is the mock Library plus the folder read *library.Store has beyond the interface.
type libraryWithFolders struct {
	*mocks.MockLibrary
	folders map[int]int
}

func (l libraryWithFolders) FileFolderID(_ context.Context, fileID int) (int, error) {
	id, ok := l.folders[fileID]
	if !ok {
		return 0, sql.ErrNoRows
	}
	return id, nil
}

var _ dependency.Library = libraryWithFolders{}

type copyFixture struct {
	s     *Server
	lib   *mocks.MockLibrary
	files *mocks.MockFiles
	media *mocks.MockMedia
	adm   *mocks.MockAdmin
	fs    *mocks.MockFileStore
}

func newCopyFixture(t *testing.T, folders map[int]int) *copyFixture {
	f := &copyFixture{
		lib:   mocks.NewMockLibrary(t),
		files: mocks.NewMockFiles(t),
		media: mocks.NewMockMedia(t),
		adm:   mocks.NewMockAdmin(t),
		fs:    mocks.NewMockFileStore(t),
	}
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Library().Return(libraryWithFolders{MockLibrary: f.lib, folders: folders}).Maybe()
	repo.EXPECT().Files().Return(f.files).Maybe()
	repo.EXPECT().Media().Return(f.media).Maybe()
	repo.EXPECT().Admin().Return(f.adm).Maybe()
	f.adm.EXPECT().GetAdminByUsername(mock.Anything, "kirill").Return(&entity.Admin{Id: 7, Username: "kirill"}, nil).Maybe()
	f.s = &Server{repo: repo, bucket: f.fs}
	return f
}

// copierCtx: content write (the edge's demand) plus files read, both features on.
func copierCtx() context.Context {
	return libraryCallerCtx(rbac.SectionContent, string(entity.AccessWrite), rbac.SectionFiles, string(entity.AccessRead))
}

func hexSha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func libFile(id int, ct string, raw []byte) *entity.LibraryFile {
	return &entity.LibraryFile{Id: id, LibraryFileInsert: entity.LibraryFileInsert{
		ObjectKey:   fmt.Sprintf("library/%d", id),
		FileName:    "Look 01.png",
		ContentType: ct,
		SizeBytes:   int64(len(raw)),
		Sha256:      hexSha(raw),
		UploadedBy:  "anna",
	}}
}

func threeURLs(id int32) *pb_common.MediaFull {
	return &pb_common.MediaFull{Id: id, ContentHash: "c0ffee", Media: &pb_common.MediaItem{
		FullSize:   &pb_common.MediaInfo{MediaUrl: "https://cdn/full.webp"},
		Compressed: &pb_common.MediaInfo{MediaUrl: "https://cdn/compressed.webp"},
		Thumbnail:  &pb_common.MediaInfo{MediaUrl: "https://cdn/thumb.webp"},
	}}
}

func TestCopyLibraryFileNeedsMediaWriteAndFilesRead(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t), bucket: mocks.NewMockFileStore(t)}
	for name, ctx := range map[string]context.Context{
		"content write only": contentWriterCtx(),
		"files write only":   filesWriterCtx(),
		"read only":          readOnlyCtx(),
	} {
		_, err := s.CopyLibraryFileToMedia(ctx, &pb_admin.CopyLibraryFileToMediaRequest{FileId: 1})
		require.Equal(t, codes.PermissionDenied, status.Code(err), name)
	}
	_, err := s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// A file the caller cannot see answers exactly like a missing one.
func TestCopyLibraryFileInvisibleIsNotFound(t *testing.T) {
	f := newCopyFixture(t, nil)
	f.files.EXPECT().GetFileById(mock.Anything, 5).Return(nil, fmt.Errorf("file: %w", sql.ErrNoRows))
	_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// A pdf (and avif, dxf, glb…) stays a document: refused before any lookup, read or upload.
func TestCopyLibraryFilePdfIsNotCopyable(t *testing.T) {
	for _, ct := range []string{"application/pdf", "image/avif", "image/vnd.dxf", "model/gltf-binary"} {
		f := newCopyFixture(t, nil)
		f.files.EXPECT().GetFileById(mock.Anything, 5).Return(libFile(5, ct, []byte("x")), nil)
		_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
		require.Equal(t, codes.FailedPrecondition, status.Code(err), ct)
		require.Equal(t, "not_copyable", status.Convert(err).Message(), ct)
	}
}

// The golden case: a png becomes a media row with three urls, its library meta is the file's, and it
// lands in the file's folder. The Files mock carries no DeleteFile expectation: the file row stays.
func TestCopyLibraryFilePngMakesThreeSizesAndKeepsTheFile(t *testing.T) {
	raw := []byte("\x89PNG\r\n\x1a\nnot-really-a-png")
	lf := libFile(5, "image/png", raw)
	f := newCopyFixture(t, map[int]int{5: 6})
	f.files.EXPECT().GetFileById(mock.Anything, 5).Return(lf, nil)
	f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
	f.fs.EXPECT().GetLibraryObjectWithin(mock.Anything, "library/5", libraryCopyImageLimit).Return(raw, nil)
	f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	f.fs.EXPECT().UploadContentImage(mock.Anything, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw), "grbpwr-com", mock.Anything).
		Return(threeURLs(55), nil)
	f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 55, entity.MediaLibraryMeta{
		FileName:     sql.NullString{String: "Look 01.png", Valid: true},
		SizeBytes:    sql.NullInt64{Int64: int64(len(raw)), Valid: true},
		UploadedById: sql.NullInt64{Int64: 7, Valid: true},
		SourceSha256: sql.NullString{String: lf.Sha256, Valid: true},
	}).Return(nil)
	f.lib.EXPECT().SetMediaFolder(mock.Anything, 55, 6).Return(nil)

	resp, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
	require.NoError(t, err)
	require.False(t, resp.GetExisted())
	require.EqualValues(t, 55, resp.GetMedia().GetId())
	m := resp.GetMedia().GetMedia()
	require.NotEmpty(t, m.GetFullSize().GetMediaUrl())
	require.NotEmpty(t, m.GetCompressed().GetMediaUrl())
	require.NotEmpty(t, m.GetThumbnail().GetMediaUrl())
}

// The second copy finds the first: existed=true, the same id, nothing read or uploaded (the FileStore
// mock has no expectations). It is still filed into the file's folder if it has none.
func TestCopyLibraryFileSecondCopyReturnsTheSameMedia(t *testing.T) {
	raw := []byte("\x89PNG\r\n\x1a\nsame")
	lf := libFile(5, "image/png", raw)
	f := newCopyFixture(t, map[int]int{5: 6})
	f.files.EXPECT().GetFileById(mock.Anything, 5).Return(lf, nil)
	f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(&entity.MediaFull{Id: 55}, nil)
	f.lib.EXPECT().SetMediaFolder(mock.Anything, 55, 6).Return(nil)

	resp, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
	require.NoError(t, err)
	require.True(t, resp.GetExisted())
	require.EqualValues(t, 55, resp.GetMedia().GetId())
}

// svg goes through the non-raster door (svgcheck), video through the video door with its type; a
// file outside every folder is not filed anywhere (no SetMediaFolder expectation).
func TestCopyLibraryFileDispatchesSvgAndVideo(t *testing.T) {
	t.Run("svg", func(t *testing.T) {
		raw := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
		lf := libFile(8, "image/svg+xml", raw)
		f := newCopyFixture(t, map[int]int{8: 0})
		f.files.EXPECT().GetFileById(mock.Anything, 8).Return(lf, nil)
		f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
		f.fs.EXPECT().GetLibraryObjectWithin(mock.Anything, "library/8", libraryCopyImageLimit).Return(raw, nil)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentNonRaster(mock.Anything, raw, "image/svg+xml", "grbpwr-com", mock.Anything).Return(threeURLs(60), nil)
		f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 60, mock.Anything).Return(nil)
		_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 8})
		require.NoError(t, err)
	})
	t.Run("mp4", func(t *testing.T) {
		raw := []byte("....ftypmp42")
		lf := libFile(9, "video/MP4; codecs=avc1", raw)
		f := newCopyFixture(t, map[int]int{9: 3})
		f.files.EXPECT().GetFileById(mock.Anything, 9).Return(lf, nil)
		f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
		f.fs.EXPECT().GetLibraryObjectWithin(mock.Anything, "library/9", libraryCopyVideoLimit).Return(raw, nil)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentVideo(mock.Anything, raw, "grbpwr-com", mock.Anything, "video/mp4").Return(threeURLs(61), nil)
		f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 61, mock.Anything).Return(nil)
		f.lib.EXPECT().SetMediaFolder(mock.Anything, 61, 3).Return(nil)
		_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 9})
		require.NoError(t, err)
	})
}

func TestCopyLibraryFileRefusals(t *testing.T) {
	t.Run("recorded size over the ceiling is refused before the read", func(t *testing.T) {
		lf := libFile(5, "image/jpeg", []byte("x"))
		lf.SizeBytes = libraryCopyImageLimit + 1
		f := newCopyFixture(t, nil)
		f.files.EXPECT().GetFileById(mock.Anything, 5).Return(lf, nil)
		f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
		_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Equal(t, "not_copyable", status.Convert(err).Message())
	})
	t.Run("object over the read ceiling", func(t *testing.T) {
		lf := libFile(5, "image/jpeg", []byte("x"))
		f := newCopyFixture(t, nil)
		f.files.EXPECT().GetFileById(mock.Anything, 5).Return(lf, nil)
		f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
		f.fs.EXPECT().GetLibraryObjectWithin(mock.Anything, "library/5", libraryCopyImageLimit).
			Return(nil, fmt.Errorf("%w: over", bucket.ErrLibraryObjectTooLarge))
		_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
	})
	t.Run("unsafe svg", func(t *testing.T) {
		raw := []byte("<svg><script/></svg>")
		lf := libFile(5, "image/svg+xml", raw)
		f := newCopyFixture(t, nil)
		f.files.EXPECT().GetFileById(mock.Anything, 5).Return(lf, nil)
		f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
		f.fs.EXPECT().GetLibraryObjectWithin(mock.Anything, "library/5", libraryCopyImageLimit).Return(raw, nil)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentNonRaster(mock.Anything, raw, "image/svg+xml", "grbpwr-com", mock.Anything).
			Return(nil, fmt.Errorf("%w: script", bucket.ErrInvalidNonRaster))
		_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
	})
	t.Run("s3 failure is internal", func(t *testing.T) {
		lf := libFile(5, "image/jpeg", []byte("x"))
		f := newCopyFixture(t, nil)
		f.files.EXPECT().GetFileById(mock.Anything, 5).Return(lf, nil)
		f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
		f.fs.EXPECT().GetLibraryObjectWithin(mock.Anything, "library/5", libraryCopyImageLimit).Return(nil, errors.New("timeout"))
		_, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
		require.Equal(t, codes.Internal, status.Code(err))
	})
}

// Once the copy exists, a meta or folder failure is logged and the copy is still answered.
func TestCopyLibraryFileMetaFailureDoesNotFailTheCopy(t *testing.T) {
	raw := []byte("\xff\xd8\xffjpeg")
	lf := libFile(5, "image/jpeg", raw)
	f := newCopyFixture(t, map[int]int{5: 6})
	f.files.EXPECT().GetFileById(mock.Anything, 5).Return(lf, nil)
	f.lib.EXPECT().FindMediaBySourceSha(mock.Anything, lf.Sha256).Return(nil, nil)
	f.fs.EXPECT().GetLibraryObjectWithin(mock.Anything, "library/5", libraryCopyImageLimit).Return(raw, nil)
	f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	f.fs.EXPECT().UploadContentImage(mock.Anything, mock.Anything, "grbpwr-com", mock.Anything).Return(threeURLs(55), nil)
	f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 55, mock.Anything).Return(errors.New("db down"))
	f.lib.EXPECT().SetMediaFolder(mock.Anything, 55, 6).Return(entity.ErrFolderNotFound)

	resp, err := f.s.CopyLibraryFileToMedia(copierCtx(), &pb_admin.CopyLibraryFileToMediaRequest{FileId: 5})
	require.NoError(t, err)
	require.EqualValues(t, 55, resp.GetMedia().GetId())
}

// ---- upload doors record library meta (02-DESIGN §2.5) ----

type uploadFixture struct {
	s     *Server
	media *mocks.MockMedia
	fs    *mocks.MockFileStore
}

func newUploadFixture(t *testing.T) *uploadFixture {
	f := &uploadFixture{media: mocks.NewMockMedia(t), fs: mocks.NewMockFileStore(t)}
	adm := mocks.NewMockAdmin(t)
	adm.EXPECT().GetAdminByUsername(mock.Anything, "kirill").Return(&entity.Admin{Id: 7}, nil).Maybe()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Media().Return(f.media).Maybe()
	repo.EXPECT().Admin().Return(adm).Maybe()
	f.s = &Server{repo: repo, bucket: f.fs}
	return f
}

func wantUploadMeta(name string, raw []byte) entity.MediaLibraryMeta {
	return entity.MediaLibraryMeta{
		FileName:     sql.NullString{String: name, Valid: name != ""},
		SizeBytes:    sql.NullInt64{Int64: int64(len(raw)), Valid: true},
		UploadedById: sql.NullInt64{Int64: 7, Valid: true},
		SourceSha256: sql.NullString{String: hexSha(raw), Valid: true},
	}
}

func TestUploadContentRecordsMediaLibraryMeta(t *testing.T) {
	ctx := contentWriterCtx()

	t.Run("image re-encode: size and sha of the decoded original", func(t *testing.T) {
		raw := []byte("\x89PNG\r\n\x1a\noriginal bytes")
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
		f := newUploadFixture(t)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentImage(mock.Anything, dataURL, "grbpwr-com", mock.Anything).Return(threeURLs(70), nil)
		f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 70, wantUploadMeta("look.png", raw)).Return(nil)
		resp, err := f.s.UploadContentImage(ctx, &pb_admin.UploadContentImageRequest{RawB64Image: dataURL, FileName: "  look.png "})
		require.NoError(t, err)
		require.EqualValues(t, 70, resp.GetMedia().GetId())
	})
	t.Run("image verbatim", func(t *testing.T) {
		raw := []byte("\x89PNG\r\n\x1a\nverbatim")
		f := newUploadFixture(t)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentImageVerbatim(mock.Anything, raw, "grbpwr-com", mock.Anything).Return(threeURLs(71), nil)
		f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 71, wantUploadMeta("flat.png", raw)).Return(nil)
		_, err := f.s.UploadContentImage(ctx, &pb_admin.UploadContentImageRequest{
			RawB64Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw), PreserveOriginal: true, FileName: "flat.png"})
		require.NoError(t, err)
	})
	t.Run("video", func(t *testing.T) {
		raw := []byte("....ftypmp42")
		f := newUploadFixture(t)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentVideo(mock.Anything, raw, "grbpwr-com", mock.Anything, "video/mp4").Return(threeURLs(72), nil)
		f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 72, wantUploadMeta("run.mp4", raw)).Return(nil)
		_, err := f.s.UploadContentVideo(ctx, &pb_admin.UploadContentVideoRequest{Raw: raw, ContentType: "video/mp4", FileName: "run.mp4"})
		require.NoError(t, err)
	})
	t.Run("vector", func(t *testing.T) {
		raw := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
		f := newUploadFixture(t)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentNonRaster(mock.Anything, raw, "image/svg+xml", "grbpwr-com", mock.Anything).Return(threeURLs(73), nil)
		f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 73, wantUploadMeta("", raw)).Return(nil)
		_, err := f.s.UploadContentVector(ctx, &pb_admin.UploadContentVectorRequest{Raw: raw})
		require.NoError(t, err)
	})
	t.Run("model: the model's bytes, not the preview's", func(t *testing.T) {
		raw := []byte("glTF\x02\x00\x00\x00")
		preview := []byte("\x89PNG\r\n\x1a\npreview")
		f := newUploadFixture(t)
		f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
		f.fs.EXPECT().UploadContentModel(mock.Anything, raw, preview, "grbpwr-com", mock.Anything).Return(threeURLs(74), nil)
		f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 74, wantUploadMeta("coat.glb", raw)).Return(nil)
		_, err := f.s.UploadContentModel(ctx, &pb_admin.UploadContentModelRequest{Raw: raw, Preview: preview, FileName: "coat.glb"})
		require.NoError(t, err)
	})
}

// A meta failure is logged; the upload answers as before.
func TestUploadContentMediaLibraryMetaFailureIsNotReturned(t *testing.T) {
	raw := []byte("....ftypmp42")
	f := newUploadFixture(t)
	f.fs.EXPECT().GetBaseFolder().Return("grbpwr-com")
	f.fs.EXPECT().UploadContentVideo(mock.Anything, raw, "grbpwr-com", mock.Anything, "video/mp4").Return(threeURLs(72), nil)
	f.media.EXPECT().SetMediaLibraryMeta(mock.Anything, 72, mock.Anything).Return(errors.New("db down"))
	resp, err := f.s.UploadContentVideo(contentWriterCtx(), &pb_admin.UploadContentVideoRequest{Raw: raw, ContentType: "video/mp4"})
	require.NoError(t, err)
	require.EqualValues(t, 72, resp.GetMedia().GetId())
}

// A bad file_name is refused before anything is uploaded (no bucket expectations).
func TestUploadContentFileNameValidation(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t), bucket: mocks.NewMockFileStore(t)}
	ctx := contentWriterCtx()
	for _, name := range []string{"a/b.png", `a\b.png`, strings.Repeat("я", maxUploadFileName+1)} {
		_, err := s.UploadContentImage(ctx, &pb_admin.UploadContentImageRequest{RawB64Image: "data:image/png;base64,AA==", FileName: name})
		require.Equal(t, codes.InvalidArgument, status.Code(err), name)
		_, err = s.UploadContentVideo(ctx, &pb_admin.UploadContentVideoRequest{Raw: []byte("x"), FileName: name})
		require.Equal(t, codes.InvalidArgument, status.Code(err), name)
		_, err = s.UploadContentVector(ctx, &pb_admin.UploadContentVectorRequest{Raw: []byte("x"), FileName: name})
		require.Equal(t, codes.InvalidArgument, status.Code(err), name)
		_, err = s.UploadContentModel(ctx, &pb_admin.UploadContentModelRequest{Raw: []byte("x"), FileName: name})
		require.Equal(t, codes.InvalidArgument, status.Code(err), name)
	}
	got, err := uploadFileName(strings.Repeat("я", maxUploadFileName))
	require.NoError(t, err)
	require.Equal(t, maxUploadFileName, len([]rune(got)))
}
