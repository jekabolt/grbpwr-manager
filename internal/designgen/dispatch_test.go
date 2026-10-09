package designgen

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

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
		{entity.DesignRunKindThreed, "threed"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
			thd := &fakeProvider{name: "threed", produces: []string{ContentTypePNG}, out: okOutcome(1, 0.6)}
			st := &fakeStore{}
			w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img, Threed: thd})

			require.NoError(t, w.execute(context.Background(), testRun(1, c.kind), "tok"))
			for name, p := range map[string]*fakeProvider{"image": img, "threed": thd} {
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
// path stores raster only, so a route that produces something else (here a fake that claims SVG)
// must refuse for free rather than buy a file the upload will then reject — five times per run.
func TestUnstorableOutputRefusesBeforeAnyMoney(t *testing.T) {
	vec := &fakeProvider{name: "image", produces: []string{ContentTypeSVG}, out: okOutcome(1, 0.08)}
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG) // raster only, exactly like the real one
	w := testWorker(st, nil, sink, Providers{Image: vec})

	require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindFlat), "tok"))
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
	// the landing is tried twice (run 148), then the run closes NAMED — never re-queued
	require.Equal(t, entity.DesignErrorCodeLandingFailed, st.failed[0].ErrorCode)
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

// TestDatabaseTroubleClosesTheRunNamed — a delivered result the store cannot file (twice) closes the
// run `landing_failed` on a fresh context instead of leaving it `running` on its lease (run 148);
// only when even FailRun fails is it a worker error the tick backs off on.
func TestDatabaseTroubleClosesTheRunNamed(t *testing.T) {
	st := &fakeStore{completeEr: errBoom}
	sink := newFakeSink(ContentTypePNG)
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(st, nil, sink, Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(3, entity.DesignRunKindFlat), "tok"))
	require.Len(t, st.completed, 2, "filing is tried twice")
	require.Len(t, st.failed, 1)
	require.Equal(t, entity.DesignErrorCodeLandingFailed, st.failed[0].ErrorCode)
	require.False(t, st.failed[0].Retryable)
	require.Equal(t, sink.mintedIDs(), sink.dropped, "nothing was filed, so nothing was adopted")

	down := &fakeStore{completeEr: errBoom, failErr: errBoom}
	w2 := testWorker(down, nil, newFakeSink(ContentTypePNG), Providers{Image: &fakeProvider{name: "image", out: okOutcome(1, 0.04)}})
	err := w2.execute(context.Background(), testRun(3, entity.DesignRunKindFlat), "tok")
	require.Error(t, err)
	require.True(t, errors.Is(err, errBoom))
}

// TestSettleRunsOnAFreshBudget — the pass's context is already spent when the provider answers
// (run 148): the landing and the close still happen.
func TestSettleRunsOnAFreshBudget(t *testing.T) {
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	img := &fakeProvider{name: "image", out: okOutcome(4, 0.29)}
	w := testWorker(st, nil, sink, Providers{Image: img})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := testRun(3, entity.DesignRunKindFlat)
	require.NoError(t, w.settle(ctx, Job{Kind: run.Kind}, run, "tok", 1, okOutcome(4, 0.29), nil, &candidateChain{}))
	require.Len(t, st.completed, 1)
	require.Len(t, st.completed[0].Outputs, 4)
	for i, o := range st.completed[0].Outputs {
		require.Equal(t, i, o.Ordinal, "parallel landing keeps the ordinals")
	}
	require.Equal(t, 30*time.Second+4*settlePerArtifact, settleBudget(4))
	require.Equal(t, settleMax, settleBudget(40))
}

// TestPastTheCapARetryableFaultClosesTimedOut — a capped run that fails past its wall-clock cap is
// closed timed_out, not re-queued; an uncapped kind keeps its retry.
func TestPastTheCapARetryableFaultClosesTimedOut(t *testing.T) {
	st := &fakeStore{}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{})
	now := time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	run := testRun(3, entity.DesignRunKindFlat)
	run.StartedAt = sql.NullTime{Time: now.Add(-7 * time.Minute), Valid: true}
	require.NoError(t, w.failRun(context.Background(), run, "tok", errBoom))
	require.Equal(t, entity.DesignErrorCodeTimedOut, st.failed[0].ErrorCode)
	require.False(t, st.failed[0].Retryable)
	require.Contains(t, st.failed[0].LastError, "took longer than 6 min")

	fresh := testRun(4, entity.DesignRunKindFlat)
	fresh.StartedAt = sql.NullTime{Time: now.Add(-time.Minute), Valid: true}
	require.NoError(t, w.failRun(context.Background(), fresh, "tok", errBoom))
	require.NotEqual(t, entity.DesignErrorCodeTimedOut, st.failed[1].ErrorCode)

	threed := testRun(5, entity.DesignRunKindThreed)
	threed.StartedAt = run.StartedAt
	require.NoError(t, w.failRun(context.Background(), threed, "tok", errBoom))
	require.NotEqual(t, entity.DesignErrorCodeTimedOut, st.failed[2].ErrorCode, "3D is not capped")
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// ═══════════ НАРУЖУ РОВНО ОДИН КАДР ТАМ, ГДЕ ДВЕРЬ ПРОДАЛА ОДИН ═══════════

// TestOverDeliveryFilesOnePictureAndRecordsTheRest.
//
// ⚠ «ПОПРОСИЛИ ОДИН» И «ПРИШЁЛ ОДИН» — РАЗНЫЕ УТВЕРЖДЕНИЯ, И МЕЖДУ НИМИ ЧУЖОЙ СЕРВЕР. Плейграунд
// строит РОВНО ОДИН вызов с `n=1` (imageCalls), и дверь оценила прогон по этому же числу — но
// клиент картинок принимает ВЕСЬ `data[]` ответа, провайдер превращает каждую картинку в артефакт,
// а CompleteRun число выходов ни с чем не сверяет. Модель, ответившая двумя вариантами, положила бы
// в ленту карточки два кадра на прогон, проданный как один.
//
// Проба держит все три половины этого инварианта разом: в бакет ушёл ОДИН объект (лишние не
// заминчены и потому не могут стать сиротами), в CompleteRun уехала ОДНА строка выдачи, а факт
// перебора записан кодом попытки — при этом попытка ДОСТАВЛЕНА, а прогон НЕ провален: кадр куплен и
// человек его видит.
func TestOverDeliveryFilesOnePictureAndRecordsTheRest(t *testing.T) {
	for _, c := range []struct {
		kind  string
		wire  func(p Provider) Providers
		count int
	}{
		{entity.DesignRunKindFreeform, func(p Provider) Providers { return Providers{Image: p} }, 3},
		{entity.DesignRunKindCutout, func(p Provider) Providers { return Providers{Cutout: p} }, 2},
	} {
		t.Run(c.kind, func(t *testing.T) {
			prov := &fakeProvider{name: "prov", out: okOutcome(c.count, 0.04)}
			st := &fakeStore{}
			sink := newFakeSink(ContentTypePNG)
			w := testWorker(st, nil, sink, c.wire(prov))

			require.NoError(t, w.execute(context.Background(), testRun(1, c.kind), "tok"))

			require.Len(t, sink.put, 1, "лишние кадры не должны попадать в бакет вовсе: заминченный "+
				"объект, который CompleteRun не подшил, — сирота, которую надо подметать")
			require.Empty(t, sink.dropped, "подметать нечего, когда лишнее не минтили")
			require.Len(t, st.completed, 1)
			require.Len(t, st.completed[0].Outputs, 1, "в ленту карточки едет один кадр")
			require.Equal(t, 0, st.completed[0].Outputs[0].Ordinal)
			require.Empty(t, st.failed, "прогон не провален: кадр куплен, сохранён и виден")

			require.Len(t, st.finished, 1)
			require.Equal(t, CodeOverDelivery, st.finished[0].ErrorCode,
				"единственное место, где вообще записано, что модель прислала больше одного")
			require.Equal(t, entity.DesignAttemptDelivered, st.finished[0].State)
		})
	}
}

// TestOverDeliveryLeavesTheOtherKindsAlone — ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ, без которого проба выше
// зелена и у обрезки, снесённой в ноль условий.
//
// Числу выходов и числу артефактов НЕ ВЕЗДЕ положено совпадать: 3D отдаёт МОДЕЛЬ И МИНИАТЮРУ на
// один запрошенный выход, `per_view` — по кадру на вызов. Сверка, написанная против
// requested_outputs вместо рода, выбросила бы миниатюру турнтейбла молча — то есть починка одного
// дефекта завела бы второй, потише.
func TestOverDeliveryLeavesTheOtherKindsAlone(t *testing.T) {
	img := &fakeProvider{name: "image", out: okOutcome(3, 0.04)}
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	w := testWorker(st, nil, sink, Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindRender), "tok"))
	require.Len(t, sink.put, 3, "рендер отдаёт столько кадров, сколько сделал вызовов")
	require.Len(t, st.completed[0].Outputs, 3)
	require.Empty(t, st.finished[0].ErrorCode, "жаловаться не на что")
}

// TestOverDeliveryDoesNotOverwriteTheComplaintAboutThePicture.
//
// У попытки ОДНА колонка error_code, и в неё претендуют две жалобы: маршрутная — про сам купленный
// кадр (`cutout_no_alpha`) — и наша, про лишние, которых человек всё равно не увидит. Первая
// описывает товар, вторая — форму ответа; перетереть первую второй значит обменять свидетельство о
// товаре на служебную заметку. Обрезка при этом происходит в обоих случаях.
func TestOverDeliveryDoesNotOverwriteTheComplaintAboutThePicture(t *testing.T) {
	out := okOutcome(2, 0.02)
	prov := &fakeProvider{name: "fal_cutout", out: out, err: errCutoutNoAlpha}
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	w := testWorker(st, nil, sink, Providers{Cutout: prov})

	require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindCutout), "tok"))
	require.Len(t, sink.put, 1, "обрезка работает и рядом с чужой жалобой")
	require.Equal(t, CodeCutoutNoAlpha, st.finished[0].ErrorCode)
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[0].State)
}

// TestALandingRetryNeverReusesObjectKeys — the second landing gets its own object names, so no two
// media rows ever point at the same bucket objects (Codex critical 2).
func TestALandingRetryNeverReusesObjectKeys(t *testing.T) {
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	sink.failAfter = 1
	w := testWorker(st, nil, sink, Providers{Image: &fakeProvider{name: "image", out: okOutcome(3, 0.12)}})
	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindFlat), "tok"))
	require.Len(t, sink.names, 6, "two landings of three")
	seen := map[string]bool{}
	for _, n := range sink.names {
		require.False(t, seen[n], "object name %q handed out twice", n)
		seen[n] = true
	}
}

// TestACappedRunsClaimEndsAfterItsCap — at pickup a capped run's claim is shortened to its deadline
// plus the live worker's worst tail (Codex critical 1); an uncapped kind keeps the full lease.
func TestACappedRunsClaimEndsAfterItsCap(t *testing.T) {
	now := time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)
	flat := testRun(3, entity.DesignRunKindFlat)
	flat.StartedAt = sql.NullTime{Time: now.Add(-time.Minute), Valid: true}
	threed := testRun(4, entity.DesignRunKindThreed)
	threed.StartedAt = flat.StartedAt
	st := &fakeStore{claimReturn: []entity.DesignRun{flat, threed}}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: &fakeProvider{name: "image", out: okOutcome(1, 0.04)}})
	w.now = func() time.Time { return now }
	w.runOnce(context.Background())
	require.Equal(t, entity.DesignImageRunCapDefault-time.Minute+claimTailAfterCap, st.capped[3])
	_, ok := st.capped[4]
	require.False(t, ok, "3D keeps its lease")
	require.Less(t, entity.DesignImageRunCapDefault+claimTailAfterCap, 20*time.Minute)
	require.Equal(t, 2*closeTimeout+time.Minute, w.capClaimWithin(now.Add(-time.Hour)), "a run past its cap still gets time to close")
}
