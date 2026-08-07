package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The marker writes refuse for reasons an operator can act on — an incomplete раскладка, a released
// card, a name the size already carries. Every one is a fact about current state, not a malformed
// request, so it lands as FailedPrecondition; a gone marker/card is NotFound; only an unrecognised
// error may read as Internal.
func TestTechCardMarkerErrorClassification(t *testing.T) {
	ctx := context.Background()
	s := &Server{repo: driverErrorRepo(t)}

	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"incomplete layout", fmt.Errorf("%w: 11 of 12 pieces placed", entity.ErrMarkerIncomplete), codes.FailedPrecondition},
		{"released card", entity.ErrTechCardReleased, codes.FailedPrecondition},
		{"marker gone", fmt.Errorf("%w: marker 4", entity.ErrMarkerNotFound), codes.NotFound},
		{"tech card absent", sql.ErrNoRows, codes.NotFound},
		{"size off the card", entity.NewFieldViolation("size_id", "not_on_card", "size 9", "the marker's size must be one of the card's sizes"), codes.InvalidArgument},
		{"unknown bom line", entity.NewFieldViolation("bom_line_key", "not_found", "01X…", "pick a BOM fabric line of this card"), codes.InvalidArgument},
		{"infrastructure failure", errors.New("dial tcp: connection reset"), codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := status.FromError(s.techCardMarkerError(ctx, "save", 7, tc.err))
			require.True(t, ok, "must be a gRPC status")
			require.Equal(t, tc.want, st.Code())
		})
	}
}

// The incomplete-layout refusal carries the counts (11 of 12) — the actionable half of the message —
// so the handler must surface the wrapped text, not the bare sentinel.
func TestTechCardMarkerErrorKeepsTheDetail(t *testing.T) {
	s := &Server{repo: driverErrorRepo(t)}
	err := s.techCardMarkerError(context.Background(), "save", 7,
		fmt.Errorf("%w: 11 of 12 pieces placed", entity.ErrMarkerIncomplete))
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Contains(t, st.Message(), "11 of 12")
}
