package admin

import (
	"context"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The hidden model-photo system card (0417): a client can never claim its key namespace, and it can
// be neither edited (which is also archiving: approval_state=obsolete) nor deleted.

const testModelPhotoCardID = 990417

func armModelPhotoCard(t *testing.T) {
	t.Helper()
	prev := cache.GetModelPhotoTechCardID()
	cache.SetModelPhotoTechCardID(testModelPhotoCardID)
	t.Cleanup(func() { cache.SetModelPhotoTechCardID(prev) })
}

func TestCreateTechCardRefusesASystemKeyBeforeTheStore(t *testing.T) {
	repo := mocks.NewMockRepository(t) // strict: any store call fails the test
	_, err := (&Server{repo: repo}).CreateTechCard(fullAccessCtx(), guidedCreateReq(entity.TechCardModelPhotosKey))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "client_request_id")
}

func TestUpdateTechCardRefusesTheModelPhotoCardBeforeTheStore(t *testing.T) {
	armModelPhotoCard(t)
	repo := mocks.NewMockRepository(t) // strict: any store call fails the test
	_, err := (&Server{repo: repo}).UpdateTechCard(fullAccessCtx(), &pb_admin.UpdateTechCardRequest{
		Id: testModelPhotoCardID,
		TechCard: &pb_common.TechCardInsert{
			Name:          "Model photos",
			ApprovalState: pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_OBSOLETE,
		},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// The store refusal (a cache that has not loaded the id) maps to the same FailedPrecondition.
func TestTechCardWriteErrorMapsTheSystemCardRefusal(t *testing.T) {
	err := (&Server{}).techCardWriteError(context.Background(), fmt.Errorf("update: %w", entity.ErrTechCardSystem))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestDeleteTechCardRefusesTheSystemCard(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().DeleteTechCardAndListOrphanedPatternURLs(mock.Anything, testModelPhotoCardID).
		Return(nil, entity.ErrTechCardSystem)

	_, err := (&Server{repo: repo}).DeleteTechCard(context.Background(), &pb_admin.DeleteTechCardRequest{Id: testModelPhotoCardID})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
