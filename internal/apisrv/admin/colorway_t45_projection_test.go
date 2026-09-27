package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// T45 round 2 (REVIEW-T45-codex, finding 4) — the full projection (dto.ConvertToPbProductFull)
// carries the SKU colour token beside the family on the detail read and on every lifecycle
// transition, as the paged list already did. Strict mocks, as in colorway_palette_t45_test.go.

// t45Full is a stored colourway of the black family whose SKU token is BKW.
func t45Full() *entity.ColorwayFull {
	full := &entity.ColorwayFull{Product: &entity.Colorway{Id: 41, StyleId: 9}}
	body := &full.Product.ProductDisplay.ProductBody.ProductBodyInsert
	body.ColorCode, body.SkuColorToken = "BLK", "BKW"
	return full
}

// The detail read answers with the token beside the family (finding 4). MUTATION: drop
// SkuColorToken from dto.ConvertToPbProductFull — the case reads "".
func TestGetColorwayByIDCarriesTheSkuColorToken(t *testing.T) {
	r := t45Server(t)
	r.products.EXPECT().GetProductByIdShowHidden(mock.Anything, 41, true).Return(t45Full(), nil).Once()
	r.cards.EXPECT().GetColorwayRecipe(mock.Anything, 41).Return(nil, nil).Once()
	resp, err := r.s.GetColorwayByID(context.Background(), &pb_admin.GetColorwayByIDRequest{ColorwayId: 41})
	require.NoError(t, err)
	require.Equal(t, "BLK", resp.GetColorway().GetColorway().GetColorCode())
	require.Equal(t, "BKW", resp.GetColorway().GetColorway().GetSkuColorToken())
}

// …and so does a lifecycle transition, which reloads through the same projection.
func TestTransitionColorwayStatusCarriesTheSkuColorToken(t *testing.T) {
	r := t45Server(t)
	r.products.EXPECT().TransitionColorwayToHidden(mock.Anything, 41).Return(nil).Once()
	r.products.EXPECT().GetProductByIdShowHidden(mock.Anything, 41, false).Return(t45Full(), nil).Once()
	resp, err := r.s.TransitionColorwayStatus(context.Background(), &pb_admin.TransitionColorwayStatusRequest{
		ColorwayId: 41, Target: pb_common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_HIDDEN,
	})
	require.NoError(t, err)
	require.Equal(t, "BKW", resp.GetColorway().GetSkuColorToken())
	r.s.revalWG.Wait()
}
