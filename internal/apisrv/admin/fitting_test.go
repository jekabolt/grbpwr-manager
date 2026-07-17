package admin

import (
	"context"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestUpdateFittingRejectsChangeRequests is the M3 fix's contract test. Review finding M3: the store's
// UpdateFitting never wrote change_requests (S26 manages structured remarks through their own
// dedicated Add/Update/DeleteFittingChangeRequest CRUD so item ids stay stable for
// carried_from_id/carry-over) — but a non-empty change_requests on the wire was accepted without
// error, so a client that round-trips a GetFitting read straight back into UpdateFitting (or appends
// to the embedded list) got a silent no-op: no error, no write. It must now fail closed with a
// field-tagged InvalidArgument instead. The store is never reached (no mock expectation set on
// Fittings()), proving the rejection happens before any write attempt.
func TestUpdateFittingRejectsChangeRequests(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.UpdateFitting(context.Background(), &pb_admin.UpdateFittingRequest{
		Id: 1,
		Fitting: &pb_common.FittingInsert{
			ChangeRequests: []*pb_common.FittingChangeRequest{{Note: "smuggled in via update"}},
		},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "change_requests")
}

// TestUpdateFittingAllowsEmptyChangeRequests confirms the M3 fix only rejects a NON-EMPTY
// change_requests: an update that doesn't touch them (the common case — UpdateFitting never reads or
// writes that child table either way) still reaches the store as before.
func TestUpdateFittingAllowsEmptyChangeRequests(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	f := mocks.NewMockFittings(t)
	repo.EXPECT().Fittings().Return(f)
	f.EXPECT().UpdateFitting(mock.Anything, 1, mock.Anything, 0).Return(nil)

	s := &Server{repo: repo}
	_, err := s.UpdateFitting(context.Background(), &pb_admin.UpdateFittingRequest{
		Id:      1,
		Fitting: &pb_common.FittingInsert{TechCardId: 7, FittingDate: timestamppb.New(time.Now())},
	})
	require.NoError(t, err)
}

// TestAddFittingKeepsChangeRequestBatchSemantics confirms the M3 fix scopes to UpdateFitting only:
// AddFitting still accepts an initial batch of change_requests (S26's create-time batch write), the
// path insertFittingChangeRequests persists.
func TestAddFittingKeepsChangeRequestBatchSemantics(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	f := mocks.NewMockFittings(t)
	repo.EXPECT().Fittings().Return(f)
	f.EXPECT().AddFitting(mock.Anything, mock.Anything).Return(7, nil)

	s := &Server{repo: repo}
	resp, err := s.AddFitting(context.Background(), &pb_admin.AddFittingRequest{
		Fitting: &pb_common.FittingInsert{
			TechCardId:     7,
			FittingDate:    timestamppb.New(time.Now()),
			ChangeRequests: []*pb_common.FittingChangeRequest{{Target: "pattern", Note: "initial remark"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(7), resp.Id)
}
