package frontend

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetArchivesPaged(ctx context.Context, req *pb_frontend.GetArchivesPagedRequest) (*pb_frontend.GetArchivesPagedResponse, error) {

	limit, offset := clampPagination(int(req.Limit), int(req.Offset), 30, 100)

	afs, count, err := s.repo.Archive().GetArchivesPaged(ctx,
		limit,
		offset,
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get archives paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to list archives")
	}

	pbAfs := make([]*pb_frontend.StorefrontArchiveList, 0, len(afs))

	for i := range afs {
		pbAfs = append(pbAfs, dto.StorefrontArchiveListFromEntity(&afs[i]))
	}

	return &pb_frontend.GetArchivesPagedResponse{
		Archives: pbAfs,
		Total:    int32(count),
	}, nil

}

func (s *Server) GetArchive(ctx context.Context, req *pb_frontend.GetArchiveRequest) (*pb_frontend.GetArchiveResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "archive lookup is required")
	}

	code := strings.TrimSpace(req.Code)
	hasLegacyFields := req.Heading != "" || req.Tag != "" || req.Id != 0
	if code != "" && hasLegacyFields {
		return nil, status.Error(codes.InvalidArgument, "use either archive code or legacy archive fields, not both")
	}

	var (
		af     *entity.ArchiveFull
		err    error
		lookup string
	)
	switch {
	case code != "":
		lookup = "code"
		af, err = s.repo.Archive().GetArchiveByCode(ctx, code)
	case req.Id > 0:
		// Transitional compatibility for the former /archive/{heading}/{tag}/{id} route. Heading and
		// tag were decorative in the old handler; id was always the actual lookup key.
		lookup = "legacy_id"
		af, err = s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	default:
		return nil, status.Error(codes.InvalidArgument, "archive code or legacy archive id is required")
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "archive not found")
		}
		slog.Default().ErrorContext(ctx, "can't get archive", slog.String("lookup", lookup), slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "failed to get archive")
	}

	// R3: storefront archive projection (StorefrontColorway product blocks, no ArchiveList.id).
	return &pb_frontend.GetArchiveResponse{
		Archive: dto.StorefrontArchiveFullFromEntity(af),
	}, nil
}
