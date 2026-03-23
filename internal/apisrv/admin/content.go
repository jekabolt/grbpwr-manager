package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

// DeleteFromBucket
func (s *Server) DeleteFromBucket(ctx context.Context, req *pb_admin.DeleteFromBucketRequest) (*pb_admin.DeleteFromBucketResponse, error) {
	resp := &pb_admin.DeleteFromBucketResponse{}
	err := s.repo.Media().DeleteMediaById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete object from bucket",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to delete media: %v", err)
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
	list, err := s.repo.Media().ListMediaPaged(ctx, int(req.Limit), int(req.Offset), of)
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
