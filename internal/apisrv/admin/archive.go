package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) AddArchive(ctx context.Context, req *pb_admin.AddArchiveRequest) (*pb_admin.AddArchiveResponse, error) {
	if err := s.validateArchiveEmbeds(req.ArchiveInsert); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	an, err := dto.ConvertPbArchiveInsertToEntity(req.ArchiveInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert pb archive insert to entity archive insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb archive insert to entity archive insert")
	}

	archiveId, err := s.repo.Archive().AddArchive(ctx, an)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add archive")
	}

	s.revalidateAsync(&dto.RevalidationData{
		Archive: archiveId,
	})

	return &pb_admin.AddArchiveResponse{
		Id: int32(archiveId),
	}, nil
}

func (s *Server) UpdateArchive(ctx context.Context, req *pb_admin.UpdateArchiveRequest) (*pb_admin.UpdateArchiveResponse, error) {
	if err := s.validateArchiveEmbeds(req.ArchiveInsert); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	upd, err := dto.ConvertPbArchiveInsertToEntity(req.ArchiveInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert pb archive insert to entity archive insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert pb archive insert to entity archive insert")
	}

	err = s.repo.Archive().UpdateArchive(ctx,
		int(req.Id),
		upd,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "archive not found")
		}
		slog.Default().ErrorContext(ctx, "can't update archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update archive")
	}

	s.revalidateAsync(&dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})

	return &pb_admin.UpdateArchiveResponse{}, nil
}

func (s *Server) DeleteArchiveById(ctx context.Context, req *pb_admin.DeleteArchiveByIdRequest) (*pb_admin.DeleteArchiveByIdResponse, error) {
	err := s.repo.Archive().DeleteArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete archive by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to delete archive: %v", err)
	}

	s.revalidateAsync(&dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})

	return &pb_admin.DeleteArchiveByIdResponse{}, nil
}

func (s *Server) GetArchiveByID(ctx context.Context, req *pb_admin.GetArchiveByIDRequest) (*pb_admin.GetArchiveByIDResponse, error) {

	af, err := s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "archive not found")
		}
		slog.Default().ErrorContext(ctx, "can't get archive by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get archive: %v", err)
	}

	pbAf, err := dto.ConvertArchiveFullEntityToPb(af)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert archive to pb",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to convert archive: %v", err)
	}

	return &pb_admin.GetArchiveByIDResponse{
		Archive: pbAf,
	}, nil
}

// validateArchiveEmbeds enforces the shared iframe embed policy on every EMBED
// timeline block (see validateEmbedURL).
func (s *Server) validateArchiveEmbeds(ai *pb_common.ArchiveInsert) error {
	if ai == nil {
		return nil
	}
	for i, it := range ai.Items {
		if it.Type != pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_EMBED {
			continue
		}
		if err := s.validateEmbedURL(it.EmbedUrl); err != nil {
			return fmt.Errorf("archive item %d: %w", i, err)
		}
	}
	return nil
}
