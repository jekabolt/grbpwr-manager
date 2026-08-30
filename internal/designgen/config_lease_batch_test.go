package designgen

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// THE DEFECT THESE TESTS STAND ON.
//
// A lease is granted ONCE, to every row of a batch, at the instant of the claim: ClaimRuns stamps
// claim_expires_at = now + ClaimLease on all N rows and nothing renews it. runOnce then executes
// those rows ONE AT A TIME, each under its own RunTimeout. So "RunTimeout < ClaimLease" — the
// invariant applyDefaults used to enforce — is an invariant about the FIRST run of a batch and
// about nothing else.
//
// With the numbers this package shipped (batch 2, run 15m, lease 20m) the second run of every batch
// executed between minute 15 and minute 30 against a lease that died at minute 20. A second
// instance sweeps it back to `pending`, claims it and pays for it a second time, and the first
// worker's paid bytes are discarded as a lost claim. Two charges, one result kept.

// TestTheLeaseOutlivesTheWholeBatch is the invariant, stated for the batch rather than for one run.
//
// The multiplication is the whole point: run number k of a batch can still be inside a paid
// provider call k × RunTimeout after the claim that leased it.
func TestTheLeaseOutlivesTheWholeBatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Config
	}{
		{"the numbers this package used to ship", Config{BatchSize: 2, ClaimLease: 20 * time.Minute, RunTimeout: 15 * time.Minute}},
		{"the old batch ceiling", Config{BatchSize: 16, ClaimLease: 20 * time.Minute, RunTimeout: 15 * time.Minute}},
		{"the env test's numbers", Config{BatchSize: 5, ClaimLease: 30 * time.Minute, RunTimeout: 11 * time.Minute}},
		{"a batch bought with a long lease", Config{BatchSize: 8, ClaimLease: 4 * time.Hour, RunTimeout: 15 * time.Minute}},
		{"a run timeout longer than the lease", Config{BatchSize: 4, ClaimLease: time.Minute, RunTimeout: time.Hour}},
		{"a lease too short to round to anything", Config{BatchSize: 4, ClaimLease: time.Nanosecond, RunTimeout: time.Hour}},
		{"nothing configured at all", Config{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.in
			applyDefaults(&c)

			require.Positive(t, c.BatchSize, "a batch of nothing drains nothing")
			require.Positive(t, c.RunTimeout, "a pass with no time expires before it starts")
			require.Positive(t, c.ClaimLease)

			batchWall := time.Duration(c.BatchSize) * c.RunTimeout
			require.Less(t, batchWall, c.ClaimLease,
				"the LAST run of the batch must still hold its lease: batch %d × run %s = %s, lease %s",
				c.BatchSize, c.RunTimeout, batchWall, c.ClaimLease)
			require.LessOrEqual(t, batchWall, c.ClaimLease/4*3,
				"the last run also needs room to write down the result of the call it already paid for")
		})
	}
}

// TestDefaultConfigNeedsNoCorrection. A default that applyDefaults has to repair is a default that
// lies to whoever reads DefaultConfig — and the value the worker logs at startup would not be the
// value written here.
func TestDefaultConfigNeedsNoCorrection(t *testing.T) {
	d := DefaultConfig()
	c := d
	applyDefaults(&c)
	require.Equal(t, d, c, "DefaultConfig must be a fixed point of applyDefaults")
}

// TestTheBatchIsWhatGives pins WHICH of the three numbers is sacrificed, because the choice is the
// argument.
//
// RunTimeout bounds a PAID call — cutting it to fit a batch would hang up on a provider mid-charge.
// ClaimLease is how long a genuinely dead worker's run stays stuck holding its budget reservation —
// stretching it by up to the batch ceiling turns one redeploy into hours of frozen runs. BatchSize
// buys no parallelism at all in a sequential worker, so it is the only one of the three with
// nothing to lose.
func TestTheBatchIsWhatGives(t *testing.T) {
	c := Config{BatchSize: 8, ClaimLease: 20 * time.Minute, RunTimeout: 15 * time.Minute}
	applyDefaults(&c)

	require.Equal(t, 20*time.Minute, c.ClaimLease, "the lease the operator named must survive")
	require.Equal(t, 15*time.Minute, c.RunTimeout, "the paid call's budget must survive")
	require.Equal(t, 1, c.BatchSize, "the batch is what a lease of this length can carry")

	// And the escape hatch: a bigger batch is BOUGHT, by naming a lease that covers it.
	big := Config{BatchSize: 8, ClaimLease: 4 * time.Hour, RunTimeout: 15 * time.Minute}
	applyDefaults(&big)
	require.Equal(t, 8, big.BatchSize, "a lease that covers the batch must leave the batch alone")
}

// claimSpy records the arguments that actually reach the store. The clamped number is worth nothing
// in the Config struct: what leases rows is the n and the lease HANDED TO ClaimRuns, and that is
// what this asserts.
type claimSpy struct {
	*fakeStore
	askedFor []int
	leases   []time.Duration
}

func (s *claimSpy) ClaimRuns(ctx context.Context, n int, lease time.Duration, token string) ([]entity.DesignRun, error) {
	s.askedFor = append(s.askedFor, n)
	s.leases = append(s.leases, lease)
	return s.fakeStore.ClaimRuns(ctx, n, lease, token)
}

// TestTheClaimAsksForTheBatchTheLeaseCanCarry follows the number to the wire. ClaimRuns is where a
// lease is stamped on every claimed row at once, so the size of that claim is the size of the
// exposure.
func TestTheClaimAsksForTheBatchTheLeaseCanCarry(t *testing.T) {
	st := &claimSpy{fakeStore: &fakeStore{}}
	c := DefaultConfig()
	c.Enabled = true
	c.BatchSize = 16 // an operator reaching for throughput on the default lease
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})

	require.True(t, w.runOnce(context.Background()))
	require.Len(t, st.askedFor, 1)
	require.Equal(t, 1, st.askedFor[0],
		"the claim must not lease more rows than one pass of this lease can finish")
	require.Equal(t, c.ClaimLease, st.leases[0])
	require.LessOrEqual(t, time.Duration(st.askedFor[0])*c.RunTimeout, st.leases[0],
		"every row this claim leases must be reachable before that lease expires")
}
