package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// Labels rework D-02 / I-06: an update_mask of exactly country_code writes the country ALONE. The
// mocks are strict, so the full-replace UpdateColorway being reached fails the case.

// An empty merchandising next to a country-only mask does not blank the merch row: the store is
// asked for the country write only. MUTATION: drop the country branch — UpdateColorway is called
// (unexpected on the strict mock).
func TestUpdateColorwayCountryOnlyMaskWritesTheCountryAlone(t *testing.T) {
	r := t45Server(t)
	r.products.EXPECT().UpdateColorwayCountry(mock.Anything, 41, 7, "PT").Return(8, nil).Once()

	resp, err := r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
		ColorwayId:              41,
		ExpectedColorwayVersion: 7,
		CountryCode:             " pt ",
		Merchandising:           &pb_common.ColorwayMerchandisingInsert{},
		MediaIds:                []int32{5},
		UpdateMask:              &fieldmaskpb.FieldMask{Paths: []string{"country_code"}},
	})
	require.NoError(t, err)
	require.Equal(t, int32(8), resp.GetLockVersion())
}

func TestUpdateColorwayCountryOnlyMaskRefusesAnEmptyCode(t *testing.T) {
	r := t45Server(t)
	_, err := r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
		ColorwayId: 41, ExpectedColorwayVersion: 7,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"countryCode"}},
	})
	fv := t45Violation(t, err, codes.InvalidArgument)
	require.Equal(t, "country_code", fv.GetField())
}

// The store's answers map like every colourway write: stale -> Aborted, released -> FailedPrecondition,
// unknown code -> InvalidArgument on country_code.
func TestUpdateColorwayCountryOnlyMaskMapsTheStoreAnswers(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code codes.Code
	}{
		{"stale", entity.ErrTechCardConflict, codes.Aborted},
		{"released", entity.ErrTechCardReleased, codes.FailedPrecondition},
		{"unknown", entity.NewFieldViolation("country_code", "unknown_country", "ZZ", "pick one"), codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := t45Server(t)
			r.products.EXPECT().UpdateColorwayCountry(mock.Anything, 41, 7, "ZZ").Return(0, tc.err).Once()
			_, err := r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
				ColorwayId: 41, ExpectedColorwayVersion: 7, CountryCode: "ZZ",
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"country_code"}},
			})
			if tc.code == codes.InvalidArgument {
				require.Equal(t, "country_code", t45Violation(t, err, tc.code).GetField())
				return
			}
			require.Equal(t, tc.code, status.Code(err), "%v", err)
		})
	}
}

// A mask naming country_code next to merchandising keeps the full replace.
func TestUpdateColorwayCountryAndMerchandisingMaskKeepsTheFullReplace(t *testing.T) {
	r := t45Server(t)
	var gotPrd *entity.ColorwayInsert
	r.products.EXPECT().UpdateColorway(mock.Anything, 41, 7, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, prd *entity.ColorwayInsert, _ []int, _ []entity.ColorwayTagInsert,
			_ []entity.ColorwayPriceInsert, _ *entity.ColorwayDevelopmentPatch) {
			gotPrd = prd
		}).Return(8, nil).Once()
	_, err := r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
		ColorwayId: 41, ExpectedColorwayVersion: 7, CountryCode: "PT",
		Merchandising: &pb_common.ColorwayMerchandisingInsert{ColorCode: "BLK"},
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"country_code", "merchandising"}},
	})
	require.NoError(t, err)
	require.NotNil(t, gotPrd)
	require.Equal(t, "PT", gotPrd.ProductBodyInsert.CountryOfOrigin)
}
