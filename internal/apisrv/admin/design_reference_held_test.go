package admin

import (
	"context"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// «REMOVE FROM PROMPT» (109 §4): the door carries the gesture to the store as it was made, and a
// picture that is not in the prompt is a precondition refusal with its own word, not a server fault.
func TestReferenceHeldCarriesTheGestureAndRefusesNothingToHold(t *testing.T) {
	call := func(t *testing.T, held bool, ret *entity.DesignReference, retErr error) (entity.DesignReferenceHold, *pb_admin.SetDesignReferenceHeldResponse, error) {
		t.Helper()
		repo := mocks.NewMockRepository(t)
		dsg := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(dsg).Once()
		var sent entity.DesignReferenceHold
		dsg.EXPECT().SetReferenceHeld(mock.Anything, mock.AnythingOfType("entity.DesignReferenceHold")).
			Run(func(_ context.Context, r entity.DesignReferenceHold) { sent = r }).
			Return(ret, retErr).Once()
		srv := &Server{repo: repo}
		resp, err := srv.SetDesignReferenceHeld(designGuardCtx(), &pb_admin.SetDesignReferenceHeldRequest{
			TechCardId: designGuardCardID, MediaId: 100, Held: held,
		})
		return sent, resp, err
	}

	t.Run("hold", func(t *testing.T) {
		sent, resp, err := call(t, true, &entity.DesignReference{
			TechCardId: designGuardCardID, MediaId: 100, Role: entity.DesignViewFront,
			LabelSource: entity.DesignLabelSourceModelCheap, LabelState: entity.DesignLabelStateHeld,
		}, nil)
		require.NoError(t, err)
		require.Equal(t, designGuardCardID, sent.TechCardId)
		require.Equal(t, 100, sent.MediaId)
		require.True(t, sent.Held)
		require.NotEmpty(t, sent.Actor)
		require.Equal(t, entity.DesignLabelStateHeld, resp.GetReference().GetLabelState())
		require.Equal(t, entity.DesignViewFront, resp.GetReference().GetRole())
	})

	t.Run("put back", func(t *testing.T) {
		sent, _, err := call(t, false, &entity.DesignReference{
			TechCardId: designGuardCardID, MediaId: 100, Role: entity.DesignViewFront,
			LabelState: entity.DesignLabelStateOk,
		}, nil)
		require.NoError(t, err)
		require.False(t, sent.Held, "false is «put back», not «nothing said»")
	})

	t.Run("nothing to hold", func(t *testing.T) {
		_, _, err := call(t, true, nil,
			fmt.Errorf("%w: media 100 has no settled label", entity.ErrDesignNothingToHold))
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Equal(t, "nothing_to_hold", flatReason(t, err))
	})
}

// A put-back re-reads the photo only when the store left the MODEL's label pending (its detail slot
// went with the hold); a person's row, a settled row and a hold never kick the labeller.
func TestHeldPutBackRereadsOnlyAPendingModelLabel(t *testing.T) {
	pending := entity.DesignReference{LabelSource: entity.DesignLabelSourceModelCheap, LabelState: entity.DesignLabelStatePending}
	require.True(t, designHeldPutBackRereads(false, pending))
	require.False(t, designHeldPutBackRereads(true, pending), "a hold never reads")
	ok := pending
	ok.LabelState = entity.DesignLabelStateOk
	require.False(t, designHeldPutBackRereads(false, ok))
	human := pending
	human.LabelSource = entity.DesignLabelSourceHuman
	require.False(t, designHeldPutBackRereads(false, human), "a person's label is never handed to a model")
}
