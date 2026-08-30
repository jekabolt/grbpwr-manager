package designgen

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func okOutcome(n int, price float64) *Outcome {
	o := &Outcome{
		Price: decimal.NullDecimal{Decimal: decimal.NewFromFloat(price), Valid: price > 0},
		Model: "openai/gpt-image-1",
	}
	for i := 0; i < n; i++ {
		o.Artifacts = append(o.Artifacts, Artifact{Bytes: []byte{0x89, 'P'}, ContentType: ContentTypePNG})
	}
	return o
}

// TestKindRoutesToItsProvider is the money-routing table. A run sent to the wrong press is paid for
// at the wrong price and comes back in the wrong format.
func TestKindRoutesToItsProvider(t *testing.T) {
	for _, c := range []struct {
		kind string
		want string
	}{
		{entity.DesignRunKindFlat, "image"},
		{entity.DesignRunKindRender, "image"},
		{entity.DesignRunKindVector, "vector"},
		{entity.DesignRunKindThreed, "threed"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
			vec := &fakeProvider{name: "vector", produces: []string{ContentTypePNG}, out: okOutcome(1, 0.08)}
			thd := &fakeProvider{name: "threed", produces: []string{ContentTypePNG}, out: okOutcome(1, 0.6)}
			st := &fakeStore{}
			w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img, Vector: vec, Threed: thd})

			require.NoError(t, w.execute(context.Background(), testRun(1, c.kind), "tok"))
			for name, p := range map[string]*fakeProvider{"image": img, "vector": vec, "threed": thd} {
				if name == c.want {
					require.Len(t, p.calls, 1, "%s should have been called", name)
				} else {
					require.Empty(t, p.calls, "%s must not have been called", name)
				}
			}
		})
	}
}

// TestDraftIdeaNeverReachesAProvider guards the ONE routing mistake that costs money twice: the
// text run is executed synchronously by the handler, and a worker that picked it up would pay a
// second time for an answer the person already has.
func TestDraftIdeaNeverReachesAProvider(t *testing.T) {
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	st := &fakeStore{}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindDraftIdea), "tok"))
	require.Empty(t, img.calls)
	require.Empty(t, st.started, "no attempt may be opened for a run the worker must not run")
	require.Len(t, st.failed, 1)
	require.False(t, st.failed[0].Retryable)
	require.Equal(t, CodeKindNotAvailable, st.failed[0].ErrorCode)
}

// TestUnstorableOutputRefusesBeforeAnyMoney is the guard that is live TODAY: the bucket's picture
// path stores raster only, so the vector (SVG) and 3D (GLB) routes must refuse for free rather
// than buy a file the upload will then reject — five times per run.
func TestUnstorableOutputRefusesBeforeAnyMoney(t *testing.T) {
	vec := &fakeProvider{name: "recraft_vector", produces: []string{ContentTypeSVG}, out: okOutcome(1, 0.08)}
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG) // raster only, exactly like the real one
	w := testWorker(st, nil, sink, Providers{Vector: vec})

	require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindVector), "tok"))
	require.Empty(t, vec.calls, "the provider must not be called at all")
	require.Empty(t, st.started, "no attempt row, therefore no money")
	require.Empty(t, st.finished)
	require.Len(t, st.failed, 1)
	require.False(t, st.failed[0].Retryable)
	require.Equal(t, CodeOutputNotStorable, st.failed[0].ErrorCode)
}

// TestDisabledProviderRefusesBeforeAnyMoney — an unconfigured route is a closed door, not a run
// that burns five paid-looking attempts on nothing.
func TestDisabledProviderRefusesBeforeAnyMoney(t *testing.T) {
	img := &fakeProvider{name: "image", off: true}
	st := &fakeStore{}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindFlat), "tok"))
	require.Empty(t, img.calls)
	require.Empty(t, st.started)
	require.Len(t, st.failed, 1)
	require.Equal(t, CodeKindNotAvailable, st.failed[0].ErrorCode)
	require.False(t, st.failed[0].Retryable)
}

// TestClaimTokenTravelsWithEveryWrite. The token stands in the WHERE clause of the closing writes;
// a worker that forgot to pass it would be silently unable to close anything it started.
func TestClaimTokenTravelsWithEveryWrite(t *testing.T) {
	st := &fakeStore{}
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(9, entity.DesignRunKindFlat), "TOKEN-1"))
	require.Len(t, st.started, 1)
	require.Equal(t, "TOKEN-1", st.started[0].ClaimToken)
	require.Len(t, st.completed, 1)
	require.Equal(t, "TOKEN-1", st.completed[0].ClaimToken)

	// …and on the failing path too.
	st2 := &fakeStore{}
	bad := &fakeProvider{name: "image", err: orimages.ErrRateLimited}
	w2 := testWorker(st2, nil, newFakeSink(ContentTypePNG), Providers{Image: bad})
	require.NoError(t, w2.execute(context.Background(), testRun(9, entity.DesignRunKindFlat), "TOKEN-2"))
	require.Len(t, st2.failed, 1)
	require.Equal(t, "TOKEN-2", st2.failed[0].ClaimToken)
}

// TestLostClaimIsNormalAndSweepsWhatItUploaded.
//
// The lost claim is not an incident — somebody else owns the row and is writing the result. What
// this worker must do is take back the files it uploaded, because nothing adopted them and they
// are already publicly addressable.
func TestLostClaimIsNormalAndSweepsWhatItUploaded(t *testing.T) {
	st := &fakeStore{completeEr: entity.ErrDesignClaimLost}
	sink := newFakeSink(ContentTypePNG)
	img := &fakeProvider{name: "image", out: okOutcome(2, 0.08)}
	w := testWorker(st, nil, sink, Providers{Image: img})

	// A lost claim is not a worker failure: the tick must not back off over it.
	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindFlat), "tok"))
	require.ElementsMatch(t, sink.mintedIDs(), sink.dropped, "every uploaded file must be taken back")
	require.Empty(t, st.failed, "the row belongs to somebody else; we do not write its failure")
	require.Len(t, st.finished, 1, "the charge is ours and is recorded regardless")
}

// TestIdempotentRefileSweepsThisPassUploads is THE case the orphan compensation exists for, and
// the one that looks like success: CompleteRun short-circuits and returns the pictures of an
// EARLIER pass, so this pass's fresh uploads were adopted by nothing at all. err == nil.
func TestIdempotentRefileSweepsThisPassUploads(t *testing.T) {
	st := &fakeStore{completeAs: &entity.DesignRun{
		Id: 4, Status: entity.DesignRunDone,
		// media 900/901 are an earlier pass's; this pass minted 1 and 2.
		Pictures: []entity.DesignPicture{{MediaId: 900}, {MediaId: 901}},
	}}
	sink := newFakeSink(ContentTypePNG)
	img := &fakeProvider{name: "image", out: okOutcome(2, 0.08)}
	w := testWorker(st, nil, sink, Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindFlat), "tok"))
	require.Equal(t, []int{1, 2}, sink.mintedIDs())
	require.ElementsMatch(t, []int{1, 2}, sink.dropped,
		"an idempotent re-file adopted nothing of this pass; both files are orphans")
}

// TestAdoptedFilesAreNotSwept — the other half of the same rule. A compensation that took back
// what the store DID adopt would delete the run's own pictures.
func TestAdoptedFilesAreNotSwept(t *testing.T) {
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	img := &fakeProvider{name: "image", out: okOutcome(3, 0.12)}
	w := testWorker(st, nil, sink, Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindFlat), "tok"))
	require.Len(t, sink.mintedIDs(), 3)
	require.Empty(t, sink.dropped)
}

// TestStorageFailureSweepsWhatWasAlreadyMintedAndForbidsRetry.
//
// The provider delivered and our bucket refused halfway. A retry would pay a second time for bytes
// we already had, so the run closes terminally — and the file that DID land is taken back, because
// a half-filed run looks finished.
func TestStorageFailureSweepsWhatWasAlreadyMintedAndForbidsRetry(t *testing.T) {
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	sink.failAfter = 1 // the second Put fails
	img := &fakeProvider{name: "image", out: okOutcome(3, 0.12)}
	w := testWorker(st, nil, sink, Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindFlat), "tok"))
	require.Equal(t, []int{1}, sink.mintedIDs())
	require.Equal(t, []int{1}, sink.dropped)
	require.Empty(t, st.completed)
	require.Len(t, st.failed, 1)
	require.Equal(t, CodeStorageFailed, st.failed[0].ErrorCode)
	require.False(t, st.failed[0].Retryable, "a retry would pay again for bytes already delivered")
	require.Len(t, st.finished, 1)
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[0].State)
	require.True(t, st.finished[0].Price.Valid, "the provider was paid; the ledger must say so")
}

// TestChargedFailureStillRecordsThePrice. «Оплачено, но не доехало» is a real state: the attempt
// closes `unknown` WITH the money on it, because a ledger that records only successes under-reports
// spend in exactly the case where the spend was wasted.
func TestChargedFailureStillRecordsThePrice(t *testing.T) {
	st := &fakeStore{}
	charged := &Outcome{Price: decimal.NullDecimal{Decimal: decimal.NewFromFloat(0.17), Valid: true}}
	img := &fakeProvider{name: "image", out: charged, err: orimages.ErrNoImages}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(5, entity.DesignRunKindFlat), "tok"))
	require.Len(t, st.finished, 1)
	require.True(t, st.finished[0].Price.Valid)
	require.True(t, decimal.NewFromFloat(0.17).Equal(st.finished[0].Price.Decimal))
	require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)
	require.Len(t, st.failed, 1)
	require.False(t, st.failed[0].Retryable)
}

// TestUnknownPriceIsNotZero — "we do not know" and "it was free" must never read the same.
func TestUnknownPriceIsNotZero(t *testing.T) {
	st := &fakeStore{}
	img := &fakeProvider{name: "image", out: okOutcome(1, 0)} // provider reported no cost
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(6, entity.DesignRunKindFlat), "tok"))
	require.Len(t, st.finished, 1)
	require.False(t, st.finished[0].Price.Valid, "an unreported charge is NULL, not 0")
}

// TestPartialDeliveryIsFiledRatherThanRepaid. Two of three views arrived and the third call failed:
// filing the two is what stops the retry from paying for the first two all over again.
func TestPartialDeliveryIsFiledRatherThanRepaid(t *testing.T) {
	st := &fakeStore{}
	partial := okOutcome(2, 0.08)
	img := &fakeProvider{name: "image", out: partial, err: orimages.ErrProviderFailure}
	r := testRun(7, entity.DesignRunKindFlat)
	r.RequestedOutputs = 3
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), r, "tok"))
	require.Len(t, st.completed, 1)
	require.Len(t, st.completed[0].Outputs, 2)
	require.Empty(t, st.failed)
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[0].State)
	require.Equal(t, CodeProviderUnavailable, st.finished[0].ErrorCode,
		"the row must still say what went wrong beside the two that arrived")
}

// TestAsyncResumeCollectsForFreeInsteadOfPayingAgain.
//
// An attempt already closed as `accepted` carries the provider's task id. Reading it before
// submitting is the difference between resuming a job after a crash and buying it a second time.
func TestAsyncResumeCollectsForFreeInsteadOfPayingAgain(t *testing.T) {
	prior := testRun(8, entity.DesignRunKindThreed)
	prior.Attempts = []entity.DesignRunAttempt{{
		RunId: 8, AttemptNo: 1, Provider: "meshy",
		State:             entity.DesignAttemptAccepted,
		ProviderRequestId: nullString("task-42"),
	}}
	st := &fakeStore{getRun: &prior}
	thd := &fakeAsyncProvider{
		fakeProvider: fakeProvider{name: "meshy", produces: []string{ContentTypePNG}},
		collectOut:   okOutcome(1, 0.6),
	}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: thd})

	require.NoError(t, w.execute(context.Background(), testRun(8, entity.DesignRunKindThreed), "tok"))
	require.Empty(t, thd.calls, "the PAID submit must not run again")
	require.Equal(t, []string{"task-42"}, thd.collectFor)
	require.Len(t, st.completed, 1)
}

// TestAsyncSubmitClosesItsAttemptWithTheTaskIdBeforeCollecting. Without this the id is only in
// memory, and a process that dies during the minutes the provider takes has to buy the model again.
func TestAsyncSubmitClosesItsAttemptWithTheTaskIdBeforeCollecting(t *testing.T) {
	st := &fakeStore{}
	thd := &fakeAsyncProvider{
		fakeProvider: fakeProvider{
			name:     "meshy",
			produces: []string{ContentTypePNG},
			out:      &Outcome{RequestID: "task-7", Pending: true},
		},
		collectOut: okOutcome(1, 0.6),
	}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: thd})

	require.NoError(t, w.execute(context.Background(), testRun(8, entity.DesignRunKindThreed), "tok"))
	require.Len(t, st.finished, 2)
	require.Equal(t, entity.DesignAttemptAccepted, st.finished[0].State)
	require.Equal(t, "task-7", st.finished[0].ProviderRequestId)
	require.False(t, st.finished[0].Price.Valid, "no charge is known at submit; NULL, not zero")
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[1].State)
	require.True(t, st.finished[1].Price.Valid, "the charge arrives with the collect")
	require.Equal(t, []string{"task-7"}, thd.collectFor)
}

// TestClaimLostAtStartAttemptSpendsNothing. The store checks the claim before the money for exactly
// this reason: finding out that the row changed hands is much cheaper before the call than after.
func TestClaimLostAtStartAttemptSpendsNothing(t *testing.T) {
	st := &fakeStore{startErr: entity.ErrDesignClaimLost}
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(3, entity.DesignRunKindFlat), "tok"))
	require.Empty(t, img.calls)
	require.Empty(t, st.finished)
	require.Empty(t, st.failed)
}

// TestTerminalRunIsNotAnIncident — a run somebody cancelled meanwhile closes quietly.
func TestTerminalRunIsNotAnIncident(t *testing.T) {
	st := &fakeStore{completeEr: entity.ErrDesignRunTerminal}
	sink := newFakeSink(ContentTypePNG)
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(st, nil, sink, Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(3, entity.DesignRunKindFlat), "tok"))
	require.Equal(t, sink.mintedIDs(), sink.dropped)
}

// TestDatabaseTroubleIsAWorkerError — as opposed to the two above. It has to reach the tick so the
// worker backs off instead of hammering a database that is not answering.
func TestDatabaseTroubleIsAWorkerError(t *testing.T) {
	st := &fakeStore{completeEr: errBoom}
	sink := newFakeSink(ContentTypePNG)
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(st, nil, sink, Providers{Image: img})

	err := w.execute(context.Background(), testRun(3, entity.DesignRunKindFlat), "tok")
	require.Error(t, err)
	require.True(t, errors.Is(err, errBoom))
	require.Equal(t, sink.mintedIDs(), sink.dropped, "nothing was filed, so nothing was adopted")
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
