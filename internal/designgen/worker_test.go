package designgen

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestDisabledWorkerNeverLooksAtTheQueue.
//
// «Off» has to mean the code does not run — not a worker that wakes every few seconds to ask an
// empty table for work it may not do. The queue this drains is the one that spends money, so the
// safe failure mode is silence. app.go does not even construct it; this asserts the second belt.
func TestDisabledWorkerNeverLooksAtTheQueue(t *testing.T) {
	st := &fakeStore{}
	c := DefaultConfig()
	c.Enabled = false
	c.WorkerInterval = time.Millisecond
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})

	require.NoError(t, w.Start(context.Background()))
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, w.Stop())
	require.Empty(t, st.claimedWith, "a disabled worker must not claim anything")
}

// TestEnabledWorkerRevivesThenClaims. Reviving first is what makes «an expired lease is the same
// road» true: ClaimRuns looks only at `pending`, so a run whose worker died stays `running`
// forever unless something puts it back.
func TestEnabledWorkerRevivesThenClaims(t *testing.T) {
	st := &fakeStore{}
	c := DefaultConfig()
	c.Enabled = true
	c.WorkerInterval = 2 * time.Millisecond
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})

	require.NoError(t, w.Start(context.Background()))
	require.Eventually(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.claimedWith) > 0
	}, time.Second, 2*time.Millisecond)
	require.NoError(t, w.Stop())
	require.False(t, w.LastSuccess().IsZero(), "an empty queue is a successful tick")
}

// TestEveryTickClaimsWithAFreshToken. The token is the identity of one batch of claims and it is
// what every closing write is checked against; a constant would let one worker close a run another
// one is running.
func TestEveryTickClaimsWithAFreshToken(t *testing.T) {
	st := &fakeStore{}
	c := DefaultConfig()
	c.Enabled = true
	c.WorkerInterval = 2 * time.Millisecond
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})

	require.NoError(t, w.Start(context.Background()))
	require.Eventually(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.claimedWith) >= 3
	}, 2*time.Second, 2*time.Millisecond)
	require.NoError(t, w.Stop())

	seen := map[string]struct{}{}
	for _, tok := range st.claimedWith {
		require.NotEmpty(t, tok)
		_, dup := seen[tok]
		require.False(t, dup, "a claim token was reused across ticks")
		seen[tok] = struct{}{}
	}
}

// TestTickTakesEachClaimedRunThroughAPass — the loop actually turns, and the token it hands each
// pass is the one it claimed with.
func TestTickTakesEachClaimedRunThroughAPass(t *testing.T) {
	st := &fakeStore{claimReturn: []entity.DesignRun{testRun(1, entity.DesignRunKindFlat), testRun(2, entity.DesignRunKindFlat)}}
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	c := DefaultConfig()
	c.Enabled = true
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.True(t, w.runOnce(context.Background()))
	require.Len(t, img.calls, 2)
	require.Len(t, st.completed, 2)
	require.Equal(t, st.claimedWith[0], st.completed[0].ClaimToken)
}

// TestAFailingQueueBacksTheTickOff rather than hammering a database that is not answering.
func TestAFailingQueueBacksTheTickOff(t *testing.T) {
	st := &fakeStore{claimErr: errBoom}
	c := DefaultConfig()
	c.Enabled = true
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})
	require.False(t, w.runOnce(context.Background()))
	require.True(t, w.LastSuccess().IsZero())
}

// TestRunTimeoutStaysUnderTheClaimLease.
//
// THE ONE CONFIGURATION MISTAKE THAT PRODUCES TWO WORKERS ON ONE PAID JOB. If a pass may outlive
// its own lease, the queue revives a run whose worker is still alive and paying, and both then
// believe they own it. Enforcing it in applyDefaults is what makes it true by construction instead
// of by an operator getting two numbers right.
func TestRunTimeoutStaysUnderTheClaimLease(t *testing.T) {
	for _, c := range []Config{
		{ClaimLease: time.Minute, RunTimeout: time.Hour},
		{ClaimLease: time.Minute, RunTimeout: time.Minute},
		{ClaimLease: 20 * time.Minute, RunTimeout: 0},
		{},
	} {
		applyDefaults(&c)
		require.Less(t, c.RunTimeout, c.ClaimLease,
			"a pass must never be able to outlive the lease that protects it")
		require.Positive(t, c.RunTimeout)
	}
}

// TestConfigFromEnvReadsEveryVariable. AutomaticEnv is off in this repo, so a name nothing reads is
// silently empty — which is also exactly what a correctly-unset override looks like. That is why
// each variable is asserted to ARRIVE rather than merely to exist.
func TestConfigFromEnvReadsEveryVariable(t *testing.T) {
	t.Setenv(EnvEnabled, "true")
	t.Setenv(EnvInterval, "9s")
	t.Setenv(EnvBatchSize, "5")
	t.Setenv(EnvClaimLease, "30m")
	t.Setenv(EnvRunTimeout, "11m")
	t.Setenv(EnvImageQuality, "high")

	c := ConfigFromEnv()
	require.True(t, c.Enabled)
	require.Equal(t, 9*time.Second, c.WorkerInterval)
	require.Equal(t, 5, c.BatchSize)
	require.Equal(t, 30*time.Minute, c.ClaimLease)
	require.Equal(t, 11*time.Minute, c.RunTimeout)
	require.Equal(t, "high", c.ImageQuality)
}

// TestConfigFromEnvDefaultsToOff — an untouched deployment gets exactly the behaviour it had before
// this package existed.
func TestConfigFromEnvDefaultsToOff(t *testing.T) {
	t.Setenv(EnvEnabled, "")
	require.False(t, ConfigFromEnv().Enabled)
	t.Setenv(EnvEnabled, "not-a-bool")
	require.False(t, ConfigFromEnv().Enabled, "a typo must not switch a paid feature on")
}

// TestBucketSinkAcceptsOnlyWhatTheBucketCanStore. The list is a copy of somebody else's rule, and
// the reason it is a copy rather than an inference is that the alternative is finding out after
// paying: the upload sniffs the bytes and refuses an SVG with an error, by which time the provider
// has been billed.
func TestBucketSinkAcceptsOnlyWhatTheBucketCanStore(t *testing.T) {
	s := &bucketSink{}
	for _, ok := range []string{ContentTypePNG, ContentTypeJPEG, ContentTypeWEBP, ContentTypeGIF,
		"IMAGE/PNG", "image/png; charset=binary"} {
		require.True(t, s.Accepts(ok), ok)
	}
	for _, no := range []string{ContentTypeSVG, ContentTypeGLB, "application/pdf", ""} {
		require.False(t, s.Accepts(no), no)
	}
}
