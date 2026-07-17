package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
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
	actor := authsrv.GetAdminUsername(ctx)
	fi.CreatedBy, fi.UpdatedBy = actor, actor

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
	fi.UpdatedBy = authsrv.GetAdminUsername(ctx)
	if err := s.repo.Fittings().UpdateFitting(ctx, int(req.Id), fi, int(req.GetExpectedLockVersion())); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "fitting not found")
		}
		if errors.Is(err, entity.ErrFittingConflict) {
			return nil, status.Error(codes.Aborted, "fitting was modified concurrently; reload and retry")
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

// AddFittingChangeRequest adds one structured remark item to a fitting (S26). Managed separately from
// the fitting so its id is stable (carried_from_id / carry-over depend on it).
func (s *Server) AddFittingChangeRequest(ctx context.Context, req *pb_admin.AddFittingChangeRequestRequest) (*pb_admin.AddFittingChangeRequestResponse, error) {
	cr, err := dto.ConvertPbFittingChangeRequestInsertToEntity(req.GetChangeRequest())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if cr.FittingId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "fitting_id is required")
	}
	cr.CreatedBy = authsrv.GetAdminUsername(ctx)
	id, err := s.repo.Fittings().AddFittingChangeRequest(ctx, cr)
	if err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "fitting_id, piece_id or carried_from_id does not reference an existing record")
		}
		slog.Default().ErrorContext(ctx, "can't add fitting change request", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add fitting change request")
	}
	return &pb_admin.AddFittingChangeRequestResponse{Id: int32(id)}, nil
}

// UpdateFittingChangeRequest edits one remark item in place (S26) — its id stays stable.
func (s *Server) UpdateFittingChangeRequest(ctx context.Context, req *pb_admin.UpdateFittingChangeRequestRequest) (*pb_admin.UpdateFittingChangeRequestResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	cr, err := dto.ConvertPbFittingChangeRequestInsertToEntity(req.GetChangeRequest())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.repo.Fittings().UpdateFittingChangeRequest(ctx, int(req.GetId()), cr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "change request not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "piece_id or carried_from_id does not reference an existing record")
		}
		slog.Default().ErrorContext(ctx, "can't update fitting change request", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update fitting change request")
	}
	return &pb_admin.UpdateFittingChangeRequestResponse{}, nil
}

// DeleteFittingChangeRequest deletes one remark item (S26).
func (s *Server) DeleteFittingChangeRequest(ctx context.Context, req *pb_admin.DeleteFittingChangeRequestRequest) (*pb_admin.DeleteFittingChangeRequestResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Fittings().DeleteFittingChangeRequest(ctx, int(req.GetId())); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "change request not found")
		}
		slog.Default().ErrorContext(ctx, "can't delete fitting change request", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete fitting change request")
	}
	return &pb_admin.DeleteFittingChangeRequestResponse{}, nil
}

// ListOpenFittingChangeRequests returns a style's OPEN remark tips from earlier rounds — the carry-over
// view shown when opening the next round (task 2, acceptance E.15).
func (s *Server) ListOpenFittingChangeRequests(ctx context.Context, req *pb_admin.ListOpenFittingChangeRequestsRequest) (*pb_admin.ListOpenFittingChangeRequestsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	crs, err := s.repo.Fittings().ListOpenFittingChangeRequests(ctx, int(req.GetTechCardId()), int(req.GetBeforeRound()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list open fitting change requests", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list open fitting change requests")
	}
	resp := &pb_admin.ListOpenFittingChangeRequestsResponse{}
	for _, cr := range crs {
		resp.ChangeRequests = append(resp.ChangeRequests, dto.ConvertEntityFittingChangeRequestToPb(cr))
	}
	return resp, nil
}
