package admin

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Read ceilings for CopyLibraryFileToMedia (02-DESIGN §5.3). A picture or an svg is held to the
// bucket's own image ceiling (the raw form of maxImagePayloadBytes, so a copy admits exactly what an
// upload admits); a video to the transport's 50 MiB, which is also the bucket's video ceiling.
const (
	libraryCopyImageLimit = int64(bucket.MaxVerbatimImageBytes)
	libraryCopyVideoLimit = int64(50 << 20)
)

// libraryFileFolderReader is the one read the copy needs that dependency.Library does not carry yet:
// the folder a file sits in. *library.Store implements it (internal/store/library/copy.go). When the
// repository's Library does not (a mock), the copy still happens and is simply not filed.
type libraryFileFolderReader interface {
	FileFolderID(ctx context.Context, fileID int) (int, error)
}

func libraryCopyContentType(ct string) string {
	ct, _, _ = strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// CopyLibraryFileToMedia turns a private library file (a picture, an svg, a video) into a public media
// row by the same bucket doors the upload RPCs use, so a raster gets the normal 3-size re-encode. The
// file row stays where it is (D7). Copies dedupe by the file's sha: a media row that already came from
// it, or that holds the same bytes, is returned with existed=true and nothing is uploaded.
//
// Rights: the edge demands content write and both team features; here files:read is added and the
// file must be visible to the caller (GetFileById applies the viewer predicate), else NotFound.
func (s *Server) CopyLibraryFileToMedia(ctx context.Context, req *pb_admin.CopyLibraryFileToMediaRequest) (*pb_admin.CopyLibraryFileToMediaResponse, error) {
	acc := libraryAccessOf(ctx)
	if !acc.MediaWrite || !acc.FilesRead {
		return nil, status.Error(codes.PermissionDenied, entity.ErrLibraryNoSection.Error())
	}
	if req.GetFileId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	fileID := int(req.GetFileId())

	lf, err := s.repo.Files().GetFileById(ctx, fileID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't load library file for copy", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load file")
	}
	ct := libraryCopyContentType(lf.ContentType)
	if !entity.CopyableToMedia(ct) {
		return nil, status.Error(codes.FailedPrecondition, entity.ErrNotCopyable.Error())
	}

	folderID := s.libraryFileFolder(ctx, fileID)

	existing, err := s.repo.Library().FindMediaBySourceSha(ctx, lf.Sha256)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't look up an earlier copy", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't copy file")
	}
	if existing != nil {
		s.fileCopiedMedia(ctx, existing.Id, folderID)
		return &pb_admin.CopyLibraryFileToMediaResponse{Media: dto.ConvertEntityToCommonMedia(existing), Existed: true}, nil
	}

	limit := libraryCopyImageLimit
	if strings.HasPrefix(ct, "video/") {
		limit = libraryCopyVideoLimit
	}
	if lf.SizeBytes > limit {
		return nil, status.Error(codes.FailedPrecondition, entity.ErrNotCopyable.Error())
	}
	raw, err := s.bucket.GetLibraryObjectWithin(ctx, lf.ObjectKey, limit)
	if err != nil {
		if errors.Is(err, bucket.ErrLibraryObjectTooLarge) {
			return nil, status.Error(codes.FailedPrecondition, entity.ErrNotCopyable.Error())
		}
		slog.Default().ErrorContext(ctx, "can't read library object for copy", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read file")
	}

	var m *pb_common.MediaFull
	folder, name := s.bucket.GetBaseFolder(), bucket.GetMediaName()
	switch {
	case ct == "image/svg+xml":
		m, err = s.bucket.UploadContentNonRaster(ctx, raw, "image/svg+xml", folder, name)
	case strings.HasPrefix(ct, "video/"):
		m, err = s.bucket.UploadContentVideo(ctx, raw, folder, name, ct)
	default: // raster: jpeg, png, webp, heic — the cases bucket.decodeImage handles (sniffed, not declared)
		m, err = s.bucket.UploadContentImage(ctx, "data:"+ct+";base64,"+base64.StdEncoding.EncodeToString(raw), folder, name)
	}
	if err != nil {
		if errors.Is(err, bucket.ErrInvalidNonRaster) {
			return nil, status.Error(codes.FailedPrecondition, entity.ErrNotCopyable.Error())
		}
		slog.Default().ErrorContext(ctx, "can't copy library file to media", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't copy file")
	}

	if id := int(m.GetId()); id > 0 && s.repo != nil {
		meta := entity.MediaLibraryMeta{
			FileName:     sql.NullString{String: lf.FileName, Valid: strings.TrimSpace(lf.FileName) != ""},
			SizeBytes:    sql.NullInt64{Int64: lf.SizeBytes, Valid: lf.SizeBytes > 0},
			SourceSha256: sql.NullString{String: strings.ToLower(lf.Sha256), Valid: strings.TrimSpace(lf.Sha256) != ""},
		}
		if uid := s.callerAdminID(ctx); uid > 0 {
			meta.UploadedById = sql.NullInt64{Int64: int64(uid), Valid: true}
		}
		if err := s.repo.Media().SetMediaLibraryMeta(ctx, id, meta); err != nil {
			slog.Default().WarnContext(ctx, "can't record library meta of a copied file",
				slog.Int("media_id", id), slog.String("err", err.Error()))
		}
		s.fileCopiedMedia(ctx, id, folderID)
	}
	return &pb_admin.CopyLibraryFileToMediaResponse{Media: m, Existed: false}, nil
}

// libraryFileFolder is the folder of the (already proven visible) file, 0 when none or unknown.
func (s *Server) libraryFileFolder(ctx context.Context, fileID int) int {
	r, ok := s.repo.Library().(libraryFileFolderReader)
	if !ok {
		return 0
	}
	id, err := r.FileFolderID(ctx, fileID)
	if err != nil {
		slog.Default().WarnContext(ctx, "can't read library file folder", slog.Int("file_id", fileID), slog.String("err", err.Error()))
		return 0
	}
	return id
}

// fileCopiedMedia puts the media row into the file's folder when it has none. The copy has already
// happened by then, so a failure (the folder deleted meanwhile) is logged, never returned.
func (s *Server) fileCopiedMedia(ctx context.Context, mediaID, folderID int) {
	if folderID <= 0 {
		return
	}
	if err := s.repo.Library().SetMediaFolder(ctx, mediaID, folderID); err != nil {
		slog.Default().WarnContext(ctx, "can't file copied media into the file's folder",
			slog.Int("media_id", mediaID), slog.Int("folder_id", folderID), slog.String("err", err.Error()))
	}
}
