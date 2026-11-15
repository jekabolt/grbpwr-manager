package admin

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func adminMannequinPoseRequest(t *testing.T, modelID int) *pb_admin.PutModelMannequinPoseRequest {
	t.Helper()
	pose := entity.ModelMannequinPoseV2{
		ArmMode: entity.ModelMannequinArmModeLinked,
		LegMode: entity.ModelMannequinLegModeLinked,
	}
	hash, err := pose.CanonicalSHA256()
	require.NoError(t, err)
	return &pb_admin.PutModelMannequinPoseRequest{
		ModelId: int32(modelID),
		Pose:    dto.ConvertEntityModelMannequinPoseToPb(pose, hash),
	}
}

func TestGetModelMannequinPoseDistinguishesAbsenceAndFailure(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	models := mocks.NewMockModels(t)
	repo.EXPECT().Models().Return(models).Twice()
	models.EXPECT().GetModelMannequinPose(mock.Anything, 7).Return(nil, nil).Once()
	models.EXPECT().GetModelMannequinPose(mock.Anything, 8).Return(nil, entity.ErrModelMannequinPoseCorrupt).Once()
	s := &Server{repo: repo}

	response, err := s.GetModelMannequinPose(context.Background(), &pb_admin.GetModelMannequinPoseRequest{ModelId: 7})
	require.NoError(t, err)
	require.Nil(t, response.GetStoredPose())
	_, err = s.GetModelMannequinPose(context.Background(), &pb_admin.GetModelMannequinPoseRequest{ModelId: 8})
	require.Equal(t, codes.DataLoss, status.Code(err))
}

func TestPutModelMannequinPoseValidatesHashAndStampsActor(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	models := mocks.NewMockModels(t)
	repo.EXPECT().Models().Return(models).Once()
	req := adminMannequinPoseRequest(t, 7)
	ctx := authsrv.PutAdminUsername(context.Background(), "pose-editor@example.com")
	stored := &entity.StoredModelMannequinPose{
		ModelID: 7, Revision: 1, UpdatedAt: time.Now().UTC(), UpdatedBy: "pose-editor@example.com",
		CanonicalSHA256: req.Pose.CanonicalSha256,
		Pose:            entity.ModelMannequinPoseV2{ArmMode: entity.ModelMannequinArmModeLinked, LegMode: entity.ModelMannequinLegModeLinked},
	}
	models.EXPECT().PutModelMannequinPose(mock.Anything, 7, stored.Pose, mock.MatchedBy(func(g entity.ModelMannequinPoseRevisionGuard) bool {
		return !g.Present()
	}), "pose-editor@example.com").Return(stored, nil)

	response, err := (&Server{repo: repo}).PutModelMannequinPose(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "1", response.GetStoredPose().GetRevision())
	require.Equal(t, "pose-editor@example.com", response.GetStoredPose().GetUpdatedBy())

	bad := adminMannequinPoseRequest(t, 9)
	bad.Pose.CanonicalSha256 = "bad"
	_, err = (&Server{repo: mocks.NewMockRepository(t)}).PutModelMannequinPose(ctx, bad)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestPutModelMannequinPoseConflictCarriesCurrentRevision(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	models := mocks.NewMockModels(t)
	repo.EXPECT().Models().Return(models).Once()
	req := adminMannequinPoseRequest(t, 7)
	expected := "3"
	req.ExpectedRevision = &expected
	models.EXPECT().PutModelMannequinPose(mock.Anything, 7, mock.Anything, mock.MatchedBy(func(g entity.ModelMannequinPoseRevisionGuard) bool {
		return g.Present() && g.Revision() == 3
	}), "editor").Return(nil, &entity.ModelMannequinPoseConflictError{CurrentRevision: 9})

	ctx := authsrv.PutAdminUsername(context.Background(), "editor")
	_, err := (&Server{repo: repo}).PutModelMannequinPose(ctx, req)
	require.Equal(t, codes.Aborted, status.Code(err))
	st, ok := status.FromError(err)
	require.True(t, ok)
	var info *errdetails.ErrorInfo
	for _, detail := range st.Details() {
		if candidate, is := detail.(*errdetails.ErrorInfo); is {
			info = candidate
		}
	}
	require.NotNil(t, info)
	require.Equal(t, modelMannequinPoseConflictReason, info.GetReason())
	require.Equal(t, "7", info.GetMetadata()["model_id"])
	require.Equal(t, "9", info.GetMetadata()["current_revision"])
}

func TestPutModelMannequinPoseRequiresAuditIdentity(t *testing.T) {
	req := adminMannequinPoseRequest(t, 7)
	_, err := (&Server{repo: mocks.NewMockRepository(t)}).PutModelMannequinPose(context.Background(), req)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestPutModelMannequinPoseRejectsNonCanonicalRevision(t *testing.T) {
	ctx := authsrv.PutAdminUsername(context.Background(), "editor")
	for _, value := range []string{"0", "01", "18446744073709551616", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			req := adminMannequinPoseRequest(t, 7)
			req.ExpectedRevision = &value
			_, err := (&Server{repo: mocks.NewMockRepository(t)}).PutModelMannequinPose(ctx, req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, "expected_revision")
		})
	}
}

func TestPutModelMannequinPoseRejectsUnknownRequestWireFields(t *testing.T) {
	req := adminMannequinPoseRequest(t, 7)
	// Field 99, varint value 1; direct gRPC must fail as closed as the JSON gateway.
	req.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
	ctx := authsrv.PutAdminUsername(context.Background(), "editor")
	_, err := (&Server{repo: mocks.NewMockRepository(t)}).PutModelMannequinPose(ctx, req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "unknown protobuf fields")
}

func TestModelMannequinPoseStoreErrorMappings(t *testing.T) {
	for err, code := range map[error]codes.Code{
		context.Canceled: codes.Canceled,
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded): codes.DeadlineExceeded,
		entity.ErrModelMannequinPoseRevisionExhausted:       codes.FailedPrecondition,
		entity.ErrModelMannequinPoseCorrupt:                 codes.DataLoss,
	} {
		t.Run(code.String(), func(t *testing.T) {
			require.Equal(t, code, status.Code(modelMannequinPoseStoreError(context.Background(), "get", 7, err)))
		})
	}
	require.True(t, errors.Is(&entity.ModelMannequinPoseConflictError{CurrentRevision: 1}, entity.ErrModelMannequinPoseConflict))
}
