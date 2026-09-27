package entity

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestALateAcceptedIdIsNEVER_SWALLOWED — G-03 r2, Codex 3. The store's FinishAttempt asks this rule
// when its row is already closed (the store package's own tests open a production database outside
// CI and are never run from here, so the decision lives here, where it can be measured).
//
// The race: a pickup closed attempt 1 `unknown` while its expired owner was still inside the paid
// call; the owner then brings fal's accepted id. It must be written (upgrade), never acknowledged
// and dropped.
// MUTATION (measured red): return (false, nil) for DesignAttemptUnknown → the «unknown, no id» row.
func TestALateAcceptedIdIsNEVER_SWALLOWED(t *testing.T) {
	row := func(state, prid string) DesignRunAttempt {
		return DesignRunAttempt{RunId: 4, AttemptNo: 1, State: state,
			ProviderRequestId: sql.NullString{String: prid, Valid: prid != ""}}
	}
	accepted := func(id string) DesignAttemptFinish {
		return DesignAttemptFinish{RunId: 4, AttemptNo: 1, State: DesignAttemptAccepted, ProviderRequestId: id}
	}
	for _, tc := range []struct {
		name     string
		existing DesignRunAttempt
		req      DesignAttemptFinish
		upgrade  bool
		conflict bool
	}{
		{"a pickup closed it unknown; the owner brings the paid id", row(DesignAttemptUnknown, ""), accepted("slug#r1"), true, false},
		{"accepted without an id; the id fills the gap", row(DesignAttemptAccepted, ""), accepted("slug#r1"), true, false},
		{"a genuine repeat after a lost answer", row(DesignAttemptAccepted, "slug#r1"), accepted("slug#r1"), false, false},
		{"unknown already holding this id", row(DesignAttemptUnknown, "slug#r1"), accepted("slug#r1"), true, false},
		{"a different id already on the row", row(DesignAttemptAccepted, "slug#r0"), accepted("slug#r1"), false, true},
		{"closed failed, and fal says it accepted", row(DesignAttemptFailed, ""), accepted("slug#r1"), false, true},
		{"closed delivered, and a late accepted arrives", row(DesignAttemptDelivered, ""), accepted("slug#r1"), false, true},
		{"a late finish with no id loses nothing", row(DesignAttemptUnknown, ""), accepted(""), false, false},
		{"a late failure loses nothing", row(DesignAttemptUnknown, ""),
			DesignAttemptFinish{RunId: 4, AttemptNo: 1, State: DesignAttemptFailed, ProviderRequestId: "x"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upgrade, err := DesignLateAcceptedFinish(tc.existing, tc.req)
			require.Equal(t, tc.upgrade, upgrade)
			if tc.conflict {
				require.ErrorIs(t, err, ErrDesignAttemptConflict)
				require.Contains(t, err.Error(), tc.req.ProviderRequestId, "the refusal carries the paid id")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestAPaidCollectWaitIsNARROW — G-03 r2, Codex 4: only a retryable, un-cancelled failure named
// paid_collect_waiting on a run that really holds an accepted id escapes the round ceiling; the
// database is asked only then.
// MUTATION (measured red): drop the `req.ErrorCode != DesignErrorCodePaidCollectWaiting` guard →
// the «other code» row waits.
func TestAPaidCollectWaitIsNARROW(t *testing.T) {
	asked := 0
	has := func(v bool) func() (bool, error) {
		return func() (bool, error) { asked++; return v, nil }
	}
	wait := DesignRunFail{Retryable: true, ErrorCode: DesignErrorCodePaidCollectWaiting}

	ok, err := DesignPaidCollectWaits(wait, false, has(true))
	require.NoError(t, err)
	require.True(t, ok, "a bought job waits")

	ok, _ = DesignPaidCollectWaits(wait, false, has(false))
	require.False(t, ok, "second lock: no accepted id on record → the ordinary ceilings")

	asked = 0
	for name, tc := range map[string]struct {
		req       DesignRunFail
		cancelled bool
	}{
		"other code":    {DesignRunFail{Retryable: true, ErrorCode: "kind_not_available"}, false},
		"not retryable": {DesignRunFail{Retryable: false, ErrorCode: DesignErrorCodePaidCollectWaiting}, false},
		"cancelled":     {wait, true},
	} {
		ok, err := DesignPaidCollectWaits(tc.req, tc.cancelled, has(true))
		require.NoError(t, err)
		require.Falsef(t, ok, name)
	}
	require.Zero(t, asked, "the query runs only when the rest already says yes")

	boom := errors.New("db down")
	_, err = DesignPaidCollectWaits(wait, false, func() (bool, error) { return false, boom })
	require.ErrorIs(t, err, boom)
}
