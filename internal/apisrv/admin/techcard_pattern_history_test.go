package admin

import (
	"database/sql"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	deletePatternHistoryCardID = 7
	deletePatternHistoryID     = int64(51)
)

func deletePatternHistoryRequest() *pb_admin.DeleteTechCardPatternRevisionRequest {
	return &pb_admin.DeleteTechCardPatternRevisionRequest{
		TechCardId:          deletePatternHistoryCardID,
		HistoryId:           deletePatternHistoryID,
		ExpectedLockVersion: 3,
	}
}

func deletePatternHistoryServer(t *testing.T) (*Server, *mocks.MockTechCards) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	return &Server{repo: repo}, tc
}

func TestDeleteTechCardPatternRevisionNotFound(t *testing.T) {
	srv, tc := deletePatternHistoryServer(t)
	tc.EXPECT().DeleteTechCardPatternRevisionAndListOrphanedPatternURLs(
		mock.Anything, deletePatternHistoryCardID, deletePatternHistoryID, 3, "deleter").
		Return(nil, sql.ErrNoRows)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "deleter")
	_, err := srv.DeleteTechCardPatternRevision(ctx, deletePatternHistoryRequest())
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestDeleteTechCardPatternRevisionStaleLockIsAborted(t *testing.T) {
	srv, tc := deletePatternHistoryServer(t)
	tc.EXPECT().DeleteTechCardPatternRevisionAndListOrphanedPatternURLs(
		mock.Anything, deletePatternHistoryCardID, deletePatternHistoryID, 3, "deleter").
		Return(nil, entity.ErrTechCardConflict)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "deleter")
	_, err := srv.DeleteTechCardPatternRevision(ctx, deletePatternHistoryRequest())
	require.Equal(t, codes.Aborted, status.Code(err))
}

func TestDeleteTechCardPatternRevisionRefusesCurrentVersion(t *testing.T) {
	srv, tc := deletePatternHistoryServer(t)
	tc.EXPECT().DeleteTechCardPatternRevisionAndListOrphanedPatternURLs(
		mock.Anything, deletePatternHistoryCardID, deletePatternHistoryID, 3, "deleter").
		Return(nil, entity.ErrTechCardPatternRevisionCurrent)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "deleter")
	_, err := srv.DeleteTechCardPatternRevision(ctx, deletePatternHistoryRequest())
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "current pattern version")
}
