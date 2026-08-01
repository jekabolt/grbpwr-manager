package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestUpdateStyleMapsReleasedCardToFailedPrecondition(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	products := mocks.NewMockProducts(t)
	repo.EXPECT().Products().Return(products)
	products.EXPECT().UpdateStyle(mock.Anything, 7, 3, mock.MatchedBy(func(p entity.StylePatch) bool {
		return p.Fit.String == "relaxed"
	}), []string{"fit"}).Return(0, entity.ErrTechCardReleased)

	_, err := (&Server{repo: repo}).UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
		StyleId:             7,
		ExpectedLockVersion: 3,
		Patch:               &pb_admin.StylePatch{Fit: "relaxed"},
		UpdateMask:          &fieldmaskpb.FieldMask{Paths: []string{"fit"}},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), entity.ErrTechCardReleased.Error())
}
