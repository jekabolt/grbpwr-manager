package admin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestASecondFlatRunIsRefusedWhileOneDraws — M8 (07.10): the store refuses a flat run on a card whose
// flat run is pending or running (two tabs, one card), and the door answers FailedPrecondition
// `flat_run_in_flight` naming the run that holds the card, with «nothing reserved, nothing charged».
// MUTATIONS IT CATCHES: the refusal mapped through designError (Internal, no reason); the typed error
// lost by a wrapper (errors.As); the run id or status missing from the metadata.
func TestASecondFlatRunIsRefusedWhileOneDraws(t *testing.T) {
	rig := designFormatRig(t, designBandWith(false), false, nil)
	held := &entity.DesignFlatRunInFlightError{RunID: 182, Status: entity.DesignRunPending}
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Run(func(_ context.Context, req entity.DesignRunStart) {
			require.Equal(t, entity.DesignRunKindFlat, req.Kind)
		}).
		// the store wraps on its way out (txFunc's retry cap wraps with %w): the door must still see it
		Return(nil, fmt.Errorf("transaction: %w", held)).Once()

	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindFlat))
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "flat_run_in_flight", md["reason"])
	require.Equal(t, "182", md["run_id"])
	require.Equal(t, entity.DesignRunPending, md["status"])
	st, _ := status.FromError(err)
	require.Contains(t, st.Message(), "flat run 182")
	require.Contains(t, st.Message(), "Nothing was reserved and nothing was charged")
}

// TestFlatInFlightRefusalOnlyForItsError — any other store error keeps the general translation.
func TestFlatInFlightRefusalOnlyForItsError(t *testing.T) {
	require.Nil(t, designFlatInFlightRefusal(errors.New("boom")))
	require.Nil(t, designFlatInFlightRefusal(entity.ErrDesignRunTerminal))
	require.NotNil(t, designFlatInFlightRefusal(&entity.DesignFlatRunInFlightError{RunID: 1, Status: "running"}))
	require.True(t, errors.Is(&entity.DesignFlatRunInFlightError{RunID: 1}, entity.ErrDesignFlatRunInFlight))
}
