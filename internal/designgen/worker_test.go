package designgen

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
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
	t.Setenv(EnvRunTimeout, "4m")
	t.Setenv(EnvImageQuality, "high")

	c := ConfigFromEnv()
	require.True(t, c.Enabled)
	require.Equal(t, 9*time.Second, c.WorkerInterval)
	require.Equal(t, 5, c.BatchSize)
	require.Equal(t, 30*time.Minute, c.ClaimLease)
	require.Equal(t, 4*time.Minute, c.RunTimeout)
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

// TestBucketSinkAcceptsOnlyWhatTheBucketCanStore states a RELATION, not a list.
//
// ⚠ WHY IT MUST NOT BE A LIST. This test used to name four raster types and assert that SVG and GLB
// were refused — and in doing so it froze the defect as an intention: for as long as it stood, the
// vector and 3D routes were "correctly" unable to store their own output, while the door happily
// accepted those runs and reserved money for them. A list-shaped probe cannot tell a limitation
// from a decision, so it defends whichever one it was written next to.
//
// The relation is one-directional, and the direction is the one that costs money: THE SINK MUST
// NEVER SAY YES TO SOMETHING THE BUCKET CANNOT KEEP. That way round is a paid generation refused by
// our own storage on the way in. The other way round — the bucket able to keep a type this sink
// does not use — costs nothing and is nobody's bug, so it is not asserted and this probe will not
// become a brake on the next type that is added.
func TestBucketSinkAcceptsOnlyWhatTheBucketCanStore(t *testing.T) {
	s := &bucketSink{}
	// The corpus is deliberately wider than the truth: the bucket's own set, plus types nothing
	// here should ever take. It is a source of QUESTIONS; every ANSWER comes from the two
	// implementations being compared.
	corpus := append(bucket.StorableMediaTypes(),
		"application/pdf", "video/mp4", "text/html", "image/heic", "IMAGE/PNG",
		"image/png; charset=binary", "image/svg+xml; charset=utf-8", "")
	accepted := 0
	for _, ct := range corpus {
		if !s.Accepts(ct) {
			continue
		}
		accepted++
		require.Truef(t, bucket.CanStoreMediaType(ct),
			"the sink accepts %q but the bucket has no storage path for it: that file would be "+
				"bought and then refused on the way in", ct)
	}
	// A positive control. Without it every assertion above is vacuously true for a sink that
	// accepts nothing at all — which is exactly the state this whole task existed to end.
	require.GreaterOrEqual(t, accepted, len(bucket.StorableMediaTypes()),
		"the sink must accept at least everything the bucket can store; it accepted %d", accepted)
	require.True(t, s.Accepts("image/png; charset=binary"), "a parameter must not read as a new type")
	require.True(t, s.Accepts("IMAGE/PNG"), "casing must not read as a new type")
	require.False(t, s.Accepts(""), "an unlabelled artifact has no storage path")
}

// TestEveryAcceptedTypeHasADoorBehindIt is the other half of the relation above, and it is the half
// that catches the mistake a predicate alone cannot: a type the sink says yes to but hands to the
// WRONG upload — a vector posted through the picture door is refused by the sniffing, after the
// provider has been paid.
//
// It asserts nothing about which types those are; it iterates the bucket's own set.
func TestEveryAcceptedTypeHasADoorBehindIt(t *testing.T) {
	for _, ct := range bucket.StorableMediaTypes() {
		t.Run(ct, func(t *testing.T) {
			s := &bucketSink{}
			require.True(t, s.Accepts(ct), "the bucket can store %s; the sink must not refuse it", ct)

			fs := mocks.NewMockFileStore(t)
			minted := &pb_common.MediaFull{
				Id:    41,
				Media: &pb_common.MediaItem{FullSize: &pb_common.MediaInfo{MediaUrl: "https://cdn/x"}},
			}
			if _, nonRaster := nonRasterTypes[ct]; nonRaster {
				// The type travels WITH the bytes on this route: the storage path picks the object's
				// content type — the one the browser will obey — from it.
				fs.EXPECT().UploadContentNonRaster(mock.Anything, mock.Anything, ct,
					designMediaFolder, mock.Anything).Return(minted, nil).Once()
			} else {
				fs.EXPECT().UploadContentImageVerbatim(mock.Anything, mock.Anything,
					designMediaFolder, mock.Anything).Return(minted, nil).Once()
			}

			got, err := (&bucketSink{files: fs}).Put(context.Background(), []byte("bytes"), ct, "run-1-0")
			require.NoError(t, err)
			require.Equal(t, 41, got.ID)
		})
	}
}

// TestARefusedTypeNeverReachesTheBucket. The refusal has to happen HERE, in front of the file
// store: reaching it means the bytes were bought first.
func TestARefusedTypeNeverReachesTheBucket(t *testing.T) {
	for _, ct := range []string{"application/pdf", "video/mp4", "text/html", ""} {
		// A mock with no expectations at all: any call to it fails this test by name.
		fs := mocks.NewMockFileStore(t)
		_, err := (&bucketSink{files: fs}).Put(context.Background(), []byte("bytes"), ct, "run-1-0")
		require.ErrorIs(t, err, errSinkUnsupported, ct)
	}
}
