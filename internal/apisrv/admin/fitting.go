package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddFitting creates a new fitting session.
func (s *Server) AddFitting(ctx context.Context, req *pb_admin.AddFittingRequest) (*pb_admin.AddFittingResponse, error) {
	fi, err := dto.ConvertPbFittingInsertToEntity(req.Fitting)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	id, err := s.repo.Fittings().AddFitting(ctx, fi)
	if err != nil {
		if errors.Is(err, entity.ErrSampleForeignToCard) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "tech_card_id, product_id, model_id, size_id, or media_id does not reference an existing record")
		}
		slog.Default().ErrorContext(ctx, "can't add fitting",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add fitting")
	}
	return &pb_admin.AddFittingResponse{Id: int32(id)}, nil
}

// ListFittings returns a paged list of fitting sessions, optionally filtered by
// product and/or model.
func (s *Server) ListFittings(ctx context.Context, req *pb_admin.ListFittingsRequest) (*pb_admin.ListFittingsResponse, error) {
	fittings, total, err := s.repo.Fittings().ListFittings(ctx, int(req.Limit), int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor), int(req.ProductId), int(req.ModelId), int(req.TechCardId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list fittings",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't list fittings")
	}

	pbFittings := make([]*pb_common.Fitting, 0, len(fittings))
	for i := range fittings {
		pbFittings = append(pbFittings, dto.ConvertEntityFittingToPb(&fittings[i]))
	}
	return &pb_admin.ListFittingsResponse{Fittings: pbFittings, Total: int32(total)}, nil
}

// GetFitting returns a fitting session by id.
func (s *Server) GetFitting(ctx context.Context, req *pb_admin.GetFittingRequest) (*pb_admin.GetFittingResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "fitting id is required")
	}
	f, err := s.repo.Fittings().GetFittingById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "fitting not found")
		}
		slog.Default().ErrorContext(ctx, "can't get fitting by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get fitting")
	}
	return &pb_admin.GetFittingResponse{Fitting: dto.ConvertEntityFittingToPb(f)}, nil
}

// UpdateFitting updates a fitting session.
func (s *Server) UpdateFitting(ctx context.Context, req *pb_admin.UpdateFittingRequest) (*pb_admin.UpdateFittingResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "fitting id is required")
	}
	fi, err := dto.ConvertPbFittingInsertToEntity(req.Fitting)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.repo.Fittings().UpdateFitting(ctx, int(req.Id), fi); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "fitting not found")
		}
		if errors.Is(err, entity.ErrSampleForeignToCard) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "tech_card_id, product_id, model_id, size_id, or media_id does not reference an existing record")
		}
		slog.Default().ErrorContext(ctx, "can't update fitting",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update fitting")
	}
	return &pb_admin.UpdateFittingResponse{}, nil
}

// DeleteFitting deletes a fitting session by id.
func (s *Server) DeleteFitting(ctx context.Context, req *pb_admin.DeleteFittingRequest) (*pb_admin.DeleteFittingResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "fitting id is required")
	}
	if err := s.repo.Fittings().DeleteFitting(ctx, int(req.Id)); err != nil {
		slog.Default().ErrorContext(ctx, "can't delete fitting",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete fitting")
	}
	return &pb_admin.DeleteFittingResponse{}, nil
}
