package frontend

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetArchivesPaged(ctx context.Context, req *pb_frontend.GetArchivesPagedRequest) (*pb_frontend.GetArchivesPagedResponse, error) {

	afs, count, err := s.repo.Archive().GetArchivesPaged(ctx,
		int(req.Limit),
		int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get archives paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to list archives")
	}

	pbAfs := make([]*pb_common.ArchiveList, 0, len(afs))

	for _, af := range afs {
		pbAfs = append(pbAfs, dto.ConvertEntityToCommonArchiveList(&af))
	}

	return &pb_frontend.GetArchivesPagedResponse{
		Archives: pbAfs,
		Total:    int32(count),
	}, nil

}

func (s *Server) GetArchive(ctx context.Context, req *pb_frontend.GetArchiveRequest) (*pb_frontend.GetArchiveResponse, error) {

	af, err := s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "archive not found")
		}
		slog.Default().ErrorContext(ctx, "can't get archive by id", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "failed to get archive")
	}

	pbAf := dto.ConvertArchiveFullEntityToPb(af)

	return &pb_frontend.GetArchiveResponse{
		Archive: pbAf,
	}, nil
}
