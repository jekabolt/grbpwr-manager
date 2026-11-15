package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	modelMannequinPoseErrorDomain    = "models.grbpwr.com"
	modelMannequinPoseConflictReason = "MODEL_MANNEQUIN_POSE_REVISION_CONFLICT"
)

func modelMannequinPoseConflict(modelID int, currentRevision uint64) error {
	st := status.New(codes.Aborted, "model mannequin pose was modified concurrently; reload and retry")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: modelMannequinPoseConflictReason,
		Domain: modelMannequinPoseErrorDomain,
		Metadata: map[string]string{
			"model_id":         strconv.Itoa(modelID),
			"current_revision": strconv.FormatUint(currentRevision, 10),
		},
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

func modelMannequinPoseStoreError(ctx context.Context, operation string, modelID int, err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "model not found")
	case errors.Is(err, entity.ErrModelMannequinPoseCorrupt):
		slog.Default().ErrorContext(ctx, "stored model mannequin pose failed integrity validation",
			slog.Int("model_id", modelID), slog.String("operation", operation), slog.String("err", err.Error()))
		return status.Error(codes.DataLoss, "stored model mannequin pose failed integrity validation")
	case errors.Is(err, entity.ErrModelMannequinPoseRevisionExhausted):
		return status.Error(codes.FailedPrecondition, "model mannequin pose revision is exhausted")
	case errors.Is(err, entity.ErrModelMannequinPoseConflict):
		var conflict *entity.ModelMannequinPoseConflictError
		if errors.As(err, &conflict) {
			return modelMannequinPoseConflict(modelID, conflict.CurrentRevision)
		}
		return modelMannequinPoseConflict(modelID, 0)
	default:
		slog.Default().ErrorContext(ctx, "model mannequin pose operation failed",
			slog.Int("model_id", modelID), slog.String("operation", operation), slog.String("err", err.Error()))
		return status.Errorf(codes.Internal, "%s model mannequin pose", operation)
	}
}

func (s *Server) GetModelMannequinPose(ctx context.Context, req *pb_admin.GetModelMannequinPoseRequest) (*pb_admin.GetModelMannequinPoseResponse, error) {
	modelID := int(req.GetModelId())
	if modelID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "model_id must be positive")
	}
	stored, err := s.repo.Models().GetModelMannequinPose(ctx, modelID)
	if err != nil {
		return nil, modelMannequinPoseStoreError(ctx, "get", modelID, err)
	}
	return &pb_admin.GetModelMannequinPoseResponse{
		StoredPose: dto.ConvertEntityStoredModelMannequinPoseToPb(stored),
	}, nil
}

func (s *Server) PutModelMannequinPose(ctx context.Context, req *pb_admin.PutModelMannequinPoseRequest) (*pb_admin.PutModelMannequinPoseResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if len(req.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "request contains unknown protobuf fields")
	}
	modelID := int(req.GetModelId())
	if modelID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "model_id must be positive")
	}
	pose, err := dto.ConvertPbModelMannequinPoseV2ToEntity(req.GetPose())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	guard, err := dto.ParseModelMannequinPoseRevisionGuard(req.ExpectedRevision)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	actor := strings.TrimSpace(authsrv.GetAdminUsername(ctx))
	if actor == "" {
		return nil, status.Error(codes.PermissionDenied, "admin identity is required to save a mannequin pose")
	}
	stored, err := s.repo.Models().PutModelMannequinPose(ctx, modelID, pose, guard, actor)
	if err != nil {
		return nil, modelMannequinPoseStoreError(ctx, "put", modelID, err)
	}
	if stored == nil {
		return nil, modelMannequinPoseStoreError(ctx, "put", modelID, fmt.Errorf("store returned no saved pose"))
	}
	return &pb_admin.PutModelMannequinPoseResponse{
		StoredPose: dto.ConvertEntityStoredModelMannequinPoseToPb(stored),
	}, nil
}
