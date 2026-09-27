package admin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────────────
// T45 round 2 (REVIEW-T45-codex) — the handler half: a palette needs a name (finding 5), and a
// duplicate on either per-style index is an operator's «exists» and never a 500 (findings 1 and
// 6, including the relink). Strict mocks, as in colorway_palette_t45_test.go.
// ─────────────────────────────────────────────────────────────────────────────

// A palette without a name is refused before the store (which refuses it too). MUTATION: drop the
// check in createColorway — the strict mock sees an unexpected CreateColorway.
func TestCreateColorwayRefusesAPaletteWithoutAName(t *testing.T) {
	r := t45Server(t)
	for _, name := range []string{"", "   "} {
		_, err := r.s.CreateColorway(context.Background(), &pb_admin.CreateColorwayRequest{
			StyleId:       3,
			Merchandising: &pb_common.ColorwayMerchandisingInsert{ColorCode: "BLK"},
			Development: &pb_common.ColorwayDevelopmentInsert{
				Name: name, Colours: []*pb_common.ColorwayColour{{Label: "black"}, {Label: "white"}},
			},
		})
		fv := t45Violation(t, err, codes.InvalidArgument)
		require.Equal(t, "development.name", fv.GetField())
		require.Contains(t, fv.GetDescription(), "name_required_with_palette")
	}
}

// t45RawDup is a MySQL 8 duplicate-key error as the driver words it.
func t45RawDup(index string) error {
	return fmt.Errorf("can't insert colourway: Error 1062 (23000): Duplicate entry '12-BKW' for key 'product.%s'", index)
}

// The store names a duplicate; a RAW 1062 on either per-style index that reached the handler
// unnamed is classified the same way — FailedPrecondition, never Internal — while a duplicate on
// any other index stays Internal. MUTATION: drop the classification in colorwayWriteError — the raw
// token duplicate becomes Internal.
func TestColorwayWriteErrorClassifiesTheColourwayIndexes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
		says string
	}{
		{"named token", fmt.Errorf("%w: SKU colour token BKW is already held", entity.ErrColorwaySkuTokenTaken), codes.FailedPrecondition, "BKW"},
		{"named family", fmt.Errorf("%w: style 12 already holds a colourway of the BLK family", entity.ErrColorwayFamilyTaken), codes.FailedPrecondition, "BLK family"},
		{"raw token", t45RawDup(entity.ColorwaySkuTokenUniqueIndex), codes.FailedPrecondition, "SKU colour token"},
		{"raw family", t45RawDup(entity.ColorwayFamilyUniqueIndex), codes.FailedPrecondition, "already exists"},
		{"raw, another index", t45RawDup("sku"), codes.Internal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := colorwayWriteError(ctx, "create", 0, tc.err)
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), tc.says)
		})
	}
}

// The relink: a target holding the token (or, until 0377, the family) is the operator's to fix —
// FailedPrecondition carrying the store's sentence, which names the token, the style and the
// colourway in the way. MUTATION: drop the case in RelinkDraftColorway — the answer is Internal.
func TestRelinkDraftColorwayMapsAHeldToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
		says []string
	}{
		{"the token is held", fmt.Errorf("%w: SKU colour token BKW is already held by colourway 55 in style 9",
			entity.ErrColorwaySkuTokenTaken), codes.FailedPrecondition, []string{"BKW", "colourway 55", "style 9"}},
		{"the family is held, until 0377", fmt.Errorf("relink colourway 41 to style 9: %w",
			fmt.Errorf("%w: style 9 already holds a colourway of the BLK family", entity.ErrColorwayFamilyTaken)),
			codes.FailedPrecondition, []string{"BLK family", "style 9"}},
		{"a raw duplicate on the token index", t45RawDup(entity.ColorwaySkuTokenUniqueIndex), codes.FailedPrecondition, []string{"uniq_product_style_sku_color_token"}},
		{"anything else", errors.New("boom"), codes.Internal, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := t45Server(t)
			r.products.EXPECT().RelinkDraftColorway(mock.Anything, 41, 9, 3, 4).Return(tc.err).Once()
			_, err := r.s.RelinkDraftColorway(context.Background(), &pb_admin.RelinkDraftColorwayRequest{
				ColorwayId: 41, TargetStyleId: 9, ExpectedColorwayVersion: 3, ExpectedTargetStyleVersion: 4,
			})
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			for _, want := range tc.says {
				require.Contains(t, status.Convert(err).Message(), want)
			}
		})
	}
}
