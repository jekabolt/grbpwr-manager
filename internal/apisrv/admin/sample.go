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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddSample creates a sample (сэмпл) for a tech card (new-flow NF-04).
func (s *Server) AddSample(ctx context.Context, req *pb_admin.AddSampleRequest) (*pb_admin.AddSampleResponse, error) {
	ins, err := dto.ConvertPbSampleInsertToEntity(req.GetSample())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	actor := authsrv.GetAdminUsername(ctx)
	ins.CreatedBy, ins.UpdatedBy = actor, actor
	id, err := s.repo.Samples().AddSample(ctx, ins)
	if err != nil {
		if code := sampleErrCode(s, err); code != codes.OK {
			return nil, status.Error(code, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't add sample", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add sample")
	}
	return &pb_admin.AddSampleResponse{Id: int32(id)}, nil
}

// sampleErrCode maps a sample write error to a client-facing gRPC code, or codes.OK when it is not a
// recognised client error (the caller then logs it as Internal). A bad tech_card_id/size_id/
// colorway_id is only enforced by FK/ownership checks, so it must surface as InvalidArgument, not 500.
func sampleErrCode(s *Server, err error) codes.Code {
	switch {
	case errors.Is(err, entity.ErrSampleColorwayForeign), errors.Is(err, entity.ErrSampleSizeForeign),
		errors.Is(err, entity.ErrSampleSpecReleaseForeign), errors.Is(err, entity.ErrSamplePreviousForeign),
		errors.Is(err, entity.ErrSampleSubstitutionBomForeign):
		return codes.InvalidArgument
	case s.repo.IsErrForeignKeyViolation(err), s.repo.IsErrUniqueViolation(err):
		return codes.InvalidArgument
	}
	return codes.OK
}

// UpdateSample updates a sample's editable fields.
func (s *Server) UpdateSample(ctx context.Context, req *pb_admin.UpdateSampleRequest) (*pb_admin.UpdateSampleResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	ins, err := dto.ConvertPbSampleInsertToEntity(req.GetSample())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ins.UpdatedBy = authsrv.GetAdminUsername(ctx)
	if err := s.repo.Samples().UpdateSample(ctx, int(req.GetId()), ins, int(req.GetExpectedLockVersion())); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "sample not found")
		}
		if errors.Is(err, entity.ErrSampleConflict) {
			return nil, status.Error(codes.Aborted, "sample was modified concurrently; reload and retry")
		}
		if code := sampleErrCode(s, err); code != codes.OK {
			return nil, status.Error(code, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't update sample", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update sample")
	}
	return &pb_admin.UpdateSampleResponse{}, nil
}

// DeleteSample deletes a sample, refusing when it has material stock movements.
func (s *Server) DeleteSample(ctx context.Context, req *pb_admin.DeleteSampleRequest) (*pb_admin.DeleteSampleResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Samples().DeleteSample(ctx, int(req.GetId())); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Error(codes.NotFound, "sample not found")
		case errors.Is(err, entity.ErrSampleHasMovements):
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't delete sample", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete sample")
	}
	return &pb_admin.DeleteSampleResponse{}, nil
}

// GetSample returns a sample with its composed cost (stripped without costing:read).
func (s *Server) GetSample(ctx context.Context, req *pb_admin.GetSampleRequest) (*pb_admin.GetSampleResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	sm, err := s.repo.Samples().GetSampleById(ctx, int(req.GetId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "sample not found")
		}
		slog.Default().ErrorContext(ctx, "can't get sample", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get sample")
	}
	pb := dto.ConvertEntitySampleToPb(*sm)
	if read, _ := s.costingAccess(ctx); !read {
		pb.Cost = nil // the whole cost block is confidential
	}
	return &pb_admin.GetSampleResponse{Sample: pb}, nil
}

// ListSamples returns samples (cost is not loaded on list). tech_card_id is optional: 0 lists samples
// across every style (the cross-style «sewing queue»), >0 scopes to one style. status/purpose are
// optional string filters (gap-05/B-4).
func (s *Server) ListSamples(ctx context.Context, req *pb_admin.ListSamplesRequest) (*pb_admin.ListSamplesResponse, error) {
	samples, total, err := s.repo.Samples().ListSamples(ctx, int(req.GetLimit()), int(req.GetOffset()),
		dto.ConvertPBCommonOrderFactorToEntity(req.GetOrderFactor()), int(req.GetTechCardId()),
		req.GetStatus(), req.GetPurpose())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list samples", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list samples")
	}
	resp := &pb_admin.ListSamplesResponse{Total: int32(total)}
	for _, sm := range samples {
		resp.Samples = append(resp.Samples, dto.ConvertEntitySampleToPb(sm))
	}
	return resp, nil
}

// AddSampleSubstitution records a dev-time material substitution on a sample (§2.7). Q2: dev-only,
// never COGS.
func (s *Server) AddSampleSubstitution(ctx context.Context, req *pb_admin.AddSampleSubstitutionRequest) (*pb_admin.AddSampleSubstitutionResponse, error) {
	ins, err := dto.ConvertPbSampleSubstitutionInsertToEntity(req.GetSubstitution())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ins.CreatedBy = authsrv.GetAdminUsername(ctx)
	id, err := s.repo.Samples().AddSampleSubstitution(ctx, ins)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "sample not found")
		}
		if code := sampleErrCode(s, err); code != codes.OK {
			return nil, status.Error(code, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't add sample substitution", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add sample substitution")
	}
	return &pb_admin.AddSampleSubstitutionResponse{Id: int32(id)}, nil
}

// ListSampleSubstitutions lists a sample's substitutions.
func (s *Server) ListSampleSubstitutions(ctx context.Context, req *pb_admin.ListSampleSubstitutionsRequest) (*pb_admin.ListSampleSubstitutionsResponse, error) {
	if req.GetSampleId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "sample_id is required")
	}
	subs, err := s.repo.Samples().ListSampleSubstitutions(ctx, int(req.GetSampleId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list sample substitutions", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list sample substitutions")
	}
	resp := &pb_admin.ListSampleSubstitutionsResponse{}
	for _, sub := range subs {
		resp.Substitutions = append(resp.Substitutions, dto.ConvertEntitySampleSubstitutionToPb(sub))
	}
	return resp, nil
}

// DeleteSampleSubstitution deletes a substitution by id.
func (s *Server) DeleteSampleSubstitution(ctx context.Context, req *pb_admin.DeleteSampleSubstitutionRequest) (*pb_admin.DeleteSampleSubstitutionResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Samples().DeleteSampleSubstitution(ctx, int(req.GetId())); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "substitution not found")
		}
		slog.Default().ErrorContext(ctx, "can't delete sample substitution", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete sample substitution")
	}
	return &pb_admin.DeleteSampleSubstitutionResponse{}, nil
}
