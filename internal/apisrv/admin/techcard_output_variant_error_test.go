package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The colour-variant writes refuse for reasons an operator can act on — a sellable card, a released
// card, a bucket another card already owns, a unit that contradicts the card's other colours. Every
// one of those is a fact about current state, not a malformed request, so it must land as
// FailedPrecondition (HTTP 400). Only an unrecognised error may be Internal (500): the client renders
// a 500 as "something broke, try later", which for these is a lie that hides the fix.
func TestTechCardOutputVariantErrorClassification(t *testing.T) {
	ctx := context.Background()
	s := &Server{repo: driverErrorRepo(t)}

	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"sellable card", fmt.Errorf("%w: tech card 7 is \"sellable\"", entity.ErrTechCardNotAuxiliary), codes.FailedPrecondition},
		{"bucket claimed elsewhere", fmt.Errorf("%w: material 12 is the BLK variant of \"dust bag\" (tech card 9)", entity.ErrOutputVariantMaterialClaimed), codes.FailedPrecondition},
		{"unit disagrees with the card", fmt.Errorf("%w: this card's colours are measured in \"pcs\", the chosen material in \"m\"", entity.ErrOutputVariantUnitMismatch), codes.FailedPrecondition},
		{"released card", entity.ErrTechCardReleased, codes.FailedPrecondition},
		{"variant already gone", fmt.Errorf("%w: colour variant 4", entity.ErrOutputVariantNotFound), codes.NotFound},
		{"tech card absent", sql.ErrNoRows, codes.NotFound},
		{"duplicate colour on the card", entity.NewFieldViolation("color_code", "duplicate", "black (BLK)", "edit the existing colour variant instead"), codes.InvalidArgument},
		{"infrastructure failure", errors.New("dial tcp: connection reset"), codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := status.FromError(s.techCardOutputVariantError(ctx, "upsert", 7, tc.err))
			require.True(t, ok, "must be a gRPC status")
			require.Equal(t, tc.want, st.Code())
		})
	}
}

// The store appends the fact that actually blocks the write — which card holds the bucket, which unit
// the card is measured in. That detail is the only actionable half of the message, so the handler must
// return the wrapped error's text and not the bare sentinel.
func TestTechCardOutputVariantErrorKeepsTheStoreDetail(t *testing.T) {
	s := &Server{repo: driverErrorRepo(t)}
	err := s.techCardOutputVariantError(context.Background(), "upsert", 7,
		fmt.Errorf("%w: material 12 is the BLK variant of \"dust bag\" (tech card 9)", entity.ErrOutputVariantMaterialClaimed))
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Contains(t, st.Message(), "dust bag")
	require.Contains(t, st.Message(), "BLK")
}

// A colourway under an AUXILIARY style is a dead end: it can never publish, and meanwhile a DRAFT
// counts as live and pins the card's purpose. CreateColorway now refuses it (requireSellableStyle),
// and the refusal has to reach the operator as the same FailedPrecondition every other sellability
// gate uses — an Internal here would read as "the server broke" rather than "flip the card back".
//
// The store-side guard itself needs a database and is covered by the container store run.
func TestCreateColorwayOnAuxiliaryStyleIsFailedPrecondition(t *testing.T) {
	err := colorwayWriteError(context.Background(), "create", 0,
		fmt.Errorf("%w: style 3 is auxiliary, which produces a warehouse material rather than a product; register a colour variant on the card instead, or make the style sellable first",
			entity.ErrColorwayNotSellable))
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Contains(t, st.Message(), "colour variant")
}

// driverErrorRepo is a repository whose only job is to answer the two driver-classification
// questions the mapper asks last (is this a UNIQUE / FK violation). Everything under test here is a
// typed domain error, so both answers are "no" — the mapper must reach its own decisions, not the
// driver's.
func driverErrorRepo(t *testing.T) *mocks.MockRepository {
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().IsErrUniqueViolation(mock.Anything).Return(false).Maybe()
	repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(false).Maybe()
	return repo
}
