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

// maxMediaUsageIds bounds one usage lookup. The batch fans out to one placeholder per id per
// registry entry (seventeen tables), so an unbounded request would build a statement with tens
// of thousands of binds. A library page is ~50 tiles; 500 leaves generous headroom while keeping
// the widest statement near 8.5k placeholders, well under MySQL's limit.
const maxMediaUsageIds = 500

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

// UploadPattern uploads a raw cut pattern (выкройка) file — PDF or DXF, sniffed from the
// bytes — and returns its url. The file is stored in object storage (not the media
// library) and referenced by tech-card per-size patterns and fitting iteration patterns;
// the object extension (.pdf / .dxf) carries the file type.
func (s *Server) UploadPattern(ctx context.Context, req *pb_admin.UploadPatternRequest) (*pb_admin.UploadPatternResponse, error) {
	if len(req.GetFilename()) > maxPatternFilename {
		return nil, status.Errorf(codes.InvalidArgument, "filename must be at most %d characters", maxPatternFilename)
	}
	url, sizeBytes, err := s.bucket.UploadPatternFile(ctx, req.GetRaw(), bucket.GetMediaName())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload pattern file",
			slog.String("err", err.Error()),
		)
		// A rejected payload (empty / too large / not a PDF or DXF) is the client's
		// fault; anything else (e.g. an S3 PutObject failure) is an internal error.
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

// GetMediaUsage reports where each requested media item is still referenced.
//
// Without it the library cannot distinguish a free file from one on the storefront, and
// DeleteFromBucket's FailedPrecondition is the operator's only (useless) feedback: the FK
// refuses, and nothing says which of the seventeen referencing columns held the file.
//
// Every requested id is answered, including unreferenced and non-existent ones, which come
// back with an empty refs list. Omitting them would make "unused" indistinguishable from a
// response the client failed to match up.
func (s *Server) GetMediaUsage(ctx context.Context, req *pb_admin.GetMediaUsageRequest) (*pb_admin.GetMediaUsageResponse, error) {
	// Deduplicate before counting against the cap: a client repeating an id must not be able
	// to burn the budget, and the store would only fold the duplicates away anyway.
	seen := make(map[int]struct{}, len(req.GetMediaIds()))
	ids := make([]int, 0, len(req.GetMediaIds()))
	for _, id := range req.GetMediaIds() {
		if id <= 0 {
			return nil, status.Errorf(codes.InvalidArgument, "media id must be positive, got %d", id)
		}
		if _, dup := seen[int(id)]; dup {
			continue
		}
		seen[int(id)] = struct{}{}
		ids = append(ids, int(id))
	}
	if len(ids) > maxMediaUsageIds {
		return nil, status.Errorf(codes.InvalidArgument,
			"at most %d media ids per request, got %d", maxMediaUsageIds, len(ids))
	}
	if len(ids) == 0 {
		return &pb_admin.GetMediaUsageResponse{}, nil
	}

	usage, err := s.repo.Media().GetMediaUsage(ctx, ids)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get media usage",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get media usage: %v", err)
	}

	usages := make([]*pb_admin.MediaUsage, 0, len(ids))
	for _, id := range ids {
		refs := make([]*pb_admin.MediaUsageRef, 0, len(usage[id]))
		for _, r := range usage[id] {
			refs = append(refs, &pb_admin.MediaUsageRef{
				Kind:     r.Kind,
				EntityId: int32(r.EntityId),
				Label:    r.Label,
				Slot:     r.Slot,
			})
		}
		usages = append(usages, &pb_admin.MediaUsage{
			MediaId: int32(id),
			Refs:    refs,
		})
	}

	return &pb_admin.GetMediaUsageResponse{
		Usages: usages,
	}, nil
}
