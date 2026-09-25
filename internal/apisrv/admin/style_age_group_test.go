package admin

import (
	"context"
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// UpdateStyle and the age group (0366). The mask decides everything:
//   - naming age_group writes it, and then UNKNOWN (or an undeclared number) is InvalidArgument,
//     field-tagged, before the store is reached;
//   - NOT naming it — an unmasked full replace included — means "keep the stored value": the patch
//     reaches the store with an empty age group, which the store's SQL leaves untouched. That is the
//     shape of every request an old admin bundle sends, which knows nothing of the field.

// ageGroupStyleServer wires a Server whose store expects exactly one UpdateStyle call matching want.
func ageGroupStyleServer(t *testing.T, wantFields []string, want func(entity.StylePatch) bool) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	products := mocks.NewMockProducts(t)
	repo.EXPECT().Products().Return(products)
	products.EXPECT().UpdateStyle(mock.Anything, 7, 3, mock.MatchedBy(want), wantFields).Return(4, nil)
	// After a successful write the handler refreshes the dictionary cache. It gets an error here on
	// purpose: the handler only logs it, and a returned dictionary would be installed into the
	// process-global cache that other tests of this package read.
	dict := mocks.NewMockCache(t)
	repo.EXPECT().Cache().Return(dict).Maybe()
	dict.EXPECT().GetDictionaryInfo(mock.Anything).Return(nil, errors.New("no dictionary in this test")).Maybe()
	re := mocks.NewMockRevalidationService(t)
	re.EXPECT().RevalidateAll(mock.Anything, mock.Anything).Return(nil).Maybe()
	return &Server{repo: repo, re: re, revalidateSem: make(chan struct{}, 1), revalCtx: context.Background()}
}

// ageGroupRefusingServer has no store expectations at all: any store call fails the test, which is
// how "refused before the store is reached" is proven.
func ageGroupRefusingServer(t *testing.T) *Server {
	t.Helper()
	return &Server{repo: mocks.NewMockRepository(t)}
}

func requireAgeGroupViolation(t *testing.T, err error, wantReason string) {
	t.Helper()
	require.Equal(t, codes.InvalidArgument, status.Code(err), "err = %v", err)
	var violation *errdetails.BadRequest_FieldViolation
	for _, detail := range status.Convert(err).Details() {
		if br, ok := detail.(*errdetails.BadRequest); ok && len(br.FieldViolations) > 0 {
			violation = br.FieldViolations[0]
			break
		}
	}
	require.NotNil(t, violation, "the refusal must be field-tagged so the client can bind it: %v", err)
	require.Equal(t, "age_group", violation.Field)
	require.Contains(t, violation.Description, wantReason)
}

// fullStylePatch is what a full-replace caller sends: every required fact present and valid.
func fullStylePatch(ag pb_common.AgeGroupEnum) *pb_admin.StylePatch {
	return &pb_admin.StylePatch{
		Brand:         "grbpwr",
		Season:        pb_common.SeasonEnum_SEASON_ENUM_SS,
		Collection:    "core",
		TargetGender:  pb_common.GenderEnum_GENDER_ENUM_UNISEX,
		AgeGroup:      ag,
		Fit:           "regular",
		TopCategoryId: 1,
	}
}

func TestUpdateStyleWritesAMaskedAgeGroup(t *testing.T) {
	for _, path := range []string{"age_group", "ageGroup"} {
		t.Run(path, func(t *testing.T) {
			s := ageGroupStyleServer(t, []string{path}, func(p entity.StylePatch) bool {
				return p.AgeGroup == entity.AgeGroupKids
			})
			resp, err := s.UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
				StyleId:             7,
				ExpectedLockVersion: 3,
				Patch:               &pb_admin.StylePatch{AgeGroup: pb_common.AgeGroupEnum_AGE_GROUP_ENUM_KIDS},
				UpdateMask:          &fieldmaskpb.FieldMask{Paths: []string{path}},
			})
			require.NoError(t, err)
			require.EqualValues(t, 4, resp.GetLockVersion())
		})
	}
}

func TestUpdateStyleRefusesUnknownAgeGroupWhenMasked(t *testing.T) {
	_, err := ageGroupRefusingServer(t).UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
		StyleId:             7,
		ExpectedLockVersion: 3,
		Patch:               &pb_admin.StylePatch{}, // age_group UNKNOWN
		UpdateMask:          &fieldmaskpb.FieldMask{Paths: []string{"age_group"}},
	})
	requireAgeGroupViolation(t, err, "required")
}

func TestUpdateStyleRefusesAnUndeclaredAgeGroupNumber(t *testing.T) {
	t.Run("masked", func(t *testing.T) {
		_, err := ageGroupRefusingServer(t).UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
			StyleId:             7,
			ExpectedLockVersion: 3,
			Patch:               &pb_admin.StylePatch{AgeGroup: pb_common.AgeGroupEnum(99)},
			UpdateMask:          &fieldmaskpb.FieldMask{Paths: []string{"age_group"}},
		})
		requireAgeGroupViolation(t, err, "unknown_age_group")
	})
	t.Run("unmasked full replace", func(t *testing.T) {
		_, err := ageGroupRefusingServer(t).UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
			StyleId:             7,
			ExpectedLockVersion: 3,
			Patch:               fullStylePatch(pb_common.AgeGroupEnum(99)),
		})
		requireAgeGroupViolation(t, err, "unknown_age_group")
	})
}

// The compatibility case: an old client sends a full StylePatch with no age_group at all (UNKNOWN on
// the wire) and no mask. The write must go through, and the store must receive an EMPTY age group —
// the value its unmasked SQL keeps as stored — never a guessed member.
func TestUpdateStyleOldClientFullReplaceKeepsTheStoredAgeGroup(t *testing.T) {
	s := ageGroupStyleServer(t, nil, func(p entity.StylePatch) bool {
		return p.AgeGroup == "" && p.Fit.String == "regular"
	})
	_, err := s.UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
		StyleId:             7,
		ExpectedLockVersion: 3,
		Patch:               fullStylePatch(pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN),
	})
	require.NoError(t, err)
}

// A full replace that DOES carry an age group writes it.
func TestUpdateStyleFullReplaceWritesAStatedAgeGroup(t *testing.T) {
	s := ageGroupStyleServer(t, nil, func(p entity.StylePatch) bool {
		return p.AgeGroup == entity.AgeGroupToddler
	})
	_, err := s.UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
		StyleId:             7,
		ExpectedLockVersion: 3,
		Patch:               fullStylePatch(pb_common.AgeGroupEnum_AGE_GROUP_ENUM_TODDLER),
	})
	require.NoError(t, err)
}

// A mask that does not name age_group excludes it from the write, so whatever the patch carries in
// that field — even an undeclared number — is neither checked nor passed on.
func TestUpdateStyleMaskWithoutAgeGroupIgnoresTheField(t *testing.T) {
	s := ageGroupStyleServer(t, []string{"fit"}, func(p entity.StylePatch) bool {
		return p.AgeGroup == "" && p.Fit.String == "relaxed"
	})
	_, err := s.UpdateStyle(context.Background(), &pb_admin.UpdateStyleRequest{
		StyleId:             7,
		ExpectedLockVersion: 3,
		Patch:               &pb_admin.StylePatch{Fit: "relaxed", AgeGroup: pb_common.AgeGroupEnum(99)},
		UpdateMask:          &fieldmaskpb.FieldMask{Paths: []string{"fit"}},
	})
	require.NoError(t, err)
}
