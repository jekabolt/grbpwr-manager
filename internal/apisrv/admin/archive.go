package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) AddArchive(ctx context.Context, req *pb_admin.AddArchiveRequest) (*pb_admin.AddArchiveResponse, error) {
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

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Archive: archiveId,
	})

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate archive")
	}

	return &pb_admin.AddArchiveResponse{
		Id: int32(archiveId),
	}, nil
}

func (s *Server) UpdateArchive(ctx context.Context, req *pb_admin.UpdateArchiveRequest) (*pb_admin.UpdateArchiveResponse, error) {

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
		slog.Default().ErrorContext(ctx, "can't update archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update archive")
	}

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate archive")
	}

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

	err = s.re.RevalidateAll(ctx, &dto.RevalidationData{
		Archive: int(req.Id),
		Hero:    true,
	})

	if err != nil {
		slog.Default().ErrorContext(ctx, "can't revalidate archive",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't revalidate archive")
	}

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

	return &pb_admin.GetArchiveByIDResponse{
		Archive: dto.ConvertArchiveFullEntityToPb(af),
	}, nil
}
