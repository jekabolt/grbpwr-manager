package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddModel creates a new fit-model profile.
func (s *Server) AddModel(ctx context.Context, req *pb_admin.AddModelRequest) (*pb_admin.AddModelResponse, error) {
	mi, err := dto.ConvertPbModelInsertToEntity(req.Model)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	id, err := s.repo.Models().AddModel(ctx, mi)
	if err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "default_sample_size_id does not reference an existing size")
		}
		slog.Default().ErrorContext(ctx, "can't add model",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add model")
	}
	return &pb_admin.AddModelResponse{Id: int32(id)}, nil
}

// ListModels returns a paged list of fit-model profiles, optionally filtered by
// gender and a substring search on name.
func (s *Server) ListModels(ctx context.Context, req *pb_admin.ListModelsRequest) (*pb_admin.ListModelsResponse, error) {
	gender := ""
	if req.Gender != pb_common.GenderEnum_GENDER_ENUM_UNKNOWN {
		g, err := dto.ConvertPbGenderEnumToEntityGenderEnum(req.Gender)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid gender filter: %v", err)
		}
		gender = string(g)
	}

	models, total, err := s.repo.Models().ListModels(ctx, int(req.Limit), int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor), gender, strings.TrimSpace(req.Name))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list models",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't list models")
	}

	pbModels := make([]*pb_common.Model, 0, len(models))
	for i := range models {
		pbModels = append(pbModels, dto.ConvertEntityModelToPb(&models[i]))
	}
	return &pb_admin.ListModelsResponse{Models: pbModels, Total: int32(total)}, nil
}

// GetModel returns a fit-model profile by id.
func (s *Server) GetModel(ctx context.Context, req *pb_admin.GetModelRequest) (*pb_admin.GetModelResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "model id is required")
	}
	m, err := s.repo.Models().GetModelById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "model not found")
		}
		slog.Default().ErrorContext(ctx, "can't get model by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get model")
	}
	return &pb_admin.GetModelResponse{Model: dto.ConvertEntityModelToPb(m)}, nil
}

// UpdateModel updates a fit-model profile.
func (s *Server) UpdateModel(ctx context.Context, req *pb_admin.UpdateModelRequest) (*pb_admin.UpdateModelResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "model id is required")
	}
	mi, err := dto.ConvertPbModelInsertToEntity(req.Model)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.repo.Models().UpdateModel(ctx, int(req.Id), mi); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "model not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "default_sample_size_id does not reference an existing size")
		}
		slog.Default().ErrorContext(ctx, "can't update model",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update model")
	}
	return &pb_admin.UpdateModelResponse{}, nil
}

// DeleteModel deletes a fit-model profile by id.
func (s *Server) DeleteModel(ctx context.Context, req *pb_admin.DeleteModelRequest) (*pb_admin.DeleteModelResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "model id is required")
	}
	if err := s.repo.Models().DeleteModel(ctx, int(req.Id)); err != nil {
		slog.Default().ErrorContext(ctx, "can't delete model",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete model")
	}
	return &pb_admin.DeleteModelResponse{}, nil
}
