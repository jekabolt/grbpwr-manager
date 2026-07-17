package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetStyleSizeChart returns a style's full size chart (R5). The admin UI loads it before editing
// because the update is a full-replace of the whole chart.
func (s *Server) GetStyleSizeChart(ctx context.Context, req *pb_admin.GetStyleSizeChartRequest) (*pb_admin.GetStyleSizeChartResponse, error) {
	if req.StyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	chart, err := s.repo.TechCards().GetStyleSizeChart(ctx, int(req.StyleId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.StyleId)
		}
		slog.Default().ErrorContext(ctx, "can't get style size chart", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get style size chart: %v", err)
	}
	return &pb_admin.GetStyleSizeChartResponse{Chart: dto.StyleSizeChartToPb(chart)}, nil
}

// UpdateStyleSizeChart replaces a style's ENTIRE size chart in one versioned request (R5). A stale
// expected_lock_version is ABORTED; an unknown measurement/size is InvalidArgument (FK).
func (s *Server) UpdateStyleSizeChart(ctx context.Context, req *pb_admin.UpdateStyleSizeChartRequest) (*pb_admin.UpdateStyleSizeChartResponse, error) {
	if req.StyleId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	cells, err := dto.StyleSizeChartCellsFromPb(req.Cells)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	chart, err := s.repo.TechCards().UpdateStyleSizeChart(ctx, int(req.StyleId), int(req.ExpectedLockVersion), cells)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Errorf(codes.NotFound, "style %d not found", req.StyleId)
		case errors.Is(err, entity.ErrTechCardConflict):
			return nil, status.Error(codes.Aborted, "style was modified concurrently; reload the chart and retry")
		case s.repo.IsErrForeignKeyViolation(err):
			return nil, status.Error(codes.InvalidArgument, "size chart references an unknown size or measurement name")
		default:
			slog.Default().ErrorContext(ctx, "can't update style size chart", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't update style size chart: %v", err)
		}
	}
	return &pb_admin.UpdateStyleSizeChartResponse{Chart: dto.StyleSizeChartToPb(chart)}, nil
}
