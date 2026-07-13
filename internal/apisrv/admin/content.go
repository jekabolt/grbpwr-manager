package admin

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxPatternFilename bounds the echoed/stored original pattern filename (mirrors the
// tech_card_size_pattern.filename / fitting_pattern.filename VARCHAR(255) columns).
const maxPatternFilename = 255

// UploadContentImage
func (s *Server) UploadContentImage(ctx context.Context, req *pb_admin.UploadContentImageRequest) (*pb_admin.UploadContentImageResponse, error) {
	m, err := s.bucket.UploadContentImage(ctx, req.RawB64Image, s.bucket.GetBaseFolder(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content image",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to upload image: %v", err)
	}
	return &pb_admin.UploadContentImageResponse{
		Media: m,
	}, nil
}

// UploadContentVideo
func (s *Server) UploadContentVideo(ctx context.Context, req *pb_admin.UploadContentVideoRequest) (*pb_admin.UploadContentVideoResponse, error) {
	media, err := s.bucket.UploadContentVideo(ctx, req.GetRaw(), s.bucket.GetBaseFolder(), bucket.GetMediaName(), req.ContentType)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload content video",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to upload video: %v", err)
	}
	return &pb_admin.UploadContentVideoResponse{
		Media: media,
	}, nil
}

// UploadPattern uploads a raw PDF cut pattern (выкройка) and returns its url. The file is
// stored in object storage (not the media library) and referenced by tech-card per-size
// patterns and fitting iteration patterns.
func (s *Server) UploadPattern(ctx context.Context, req *pb_admin.UploadPatternRequest) (*pb_admin.UploadPatternResponse, error) {
	if len(req.GetFilename()) > maxPatternFilename {
		return nil, status.Errorf(codes.InvalidArgument, "filename must be at most %d characters", maxPatternFilename)
	}
	url, sizeBytes, err := s.bucket.UploadPatternPDF(ctx, req.GetRaw(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload pattern pdf",
			slog.String("err", err.Error()),
		)
		// A rejected payload (empty / too large / not a PDF) is the client's fault;
		// anything else (e.g. an S3 PutObject failure) is an internal error.
		code := codes.Internal
		if errors.Is(err, bucket.ErrInvalidPattern) {
			code = codes.InvalidArgument
		}
		return nil, status.Errorf(code, "failed to upload pattern: %v", err)
	}
	return &pb_admin.UploadPatternResponse{
		Url:       url,
		Filename:  req.GetFilename(),
		SizeBytes: sizeBytes,
	}, nil
}

// DeleteFromBucket
func (s *Server) DeleteFromBucket(ctx context.Context, req *pb_admin.DeleteFromBucketRequest) (*pb_admin.DeleteFromBucketResponse, error) {
	resp := &pb_admin.DeleteFromBucketResponse{}

	// Capture the media's object URLs before the row is gone so the backing S3
	// objects can be removed afterwards; deleting only the row leaves orphaned,
	// still-public CDN files (cost + data-leak). Best effort: a load failure only
	// means we can't clean S3, not that we should block the delete.
	media, mediaErr := s.repo.Media().GetMediaById(ctx, int(req.Id))
	if mediaErr != nil {
		slog.Default().WarnContext(ctx, "can't load media before delete; S3 objects may be orphaned",
			slog.Int("id", int(req.Id)), slog.String("err", mediaErr.Error()))
	}

	err := s.repo.Media().DeleteMediaById(ctx, int(req.Id))
	if err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.FailedPrecondition,
				"media is still referenced (product, archive, model, fitting or tech card) and cannot be deleted")
		}
		slog.Default().ErrorContext(ctx, "can't delete object from bucket",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to delete media: %v", err)
	}

	// The row is gone; remove the S3 objects it referenced. Failures here only leak
	// bytes (already de-referenced), so log and continue rather than fail the RPC.
	if media != nil {
		if delErr := s.bucket.DeleteObjects(ctx, media.FullSizeMediaURL, media.CompressedMediaURL, media.ThumbnailMediaURL); delErr != nil {
			slog.Default().ErrorContext(ctx, "media row deleted but S3 objects may be orphaned",
				slog.Int("id", int(req.Id)), slog.String("err", delErr.Error()))
		}
	}

	err = s.repo.Hero().RefreshHero(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refresh hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "media deleted but failed to refresh hero: %v", err)
	}
	return resp, nil
}

// ListObjects
func (s *Server) ListObjectsPaged(ctx context.Context, req *pb_admin.ListObjectsPagedRequest) (*pb_admin.ListObjectsPagedResponse, error) {
	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)
	limit, offset := clampPagination(int(req.Limit), int(req.Offset))
	list, err := s.repo.Media().ListMediaPaged(ctx, limit, offset, of)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list objects from bucket",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to list media: %v", err)
	}

	entities := make([]*pb_common.MediaFull, 0, len(list))
	for _, m := range list {
		entities = append(entities, dto.ConvertEntityToCommonMedia(&m))
	}

	return &pb_admin.ListObjectsPagedResponse{
		List: entities,
	}, nil
}
