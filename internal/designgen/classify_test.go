package designgen

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	"github.com/stretchr/testify/require"
)

// TestClassifyIsAMoneyDecision.
//
// Every row here is an amount of money. A fault marked retryable that is not costs up to five
// charges for a result that could never arrive; a fault marked terminal that is weather throws away
// a job that would have worked on the next tick. The two are asserted separately from the attempt
// STATE, because a billed failure and an unbilled one are the same verdict and different rows in
// the ledger.
func TestClassifyIsAMoneyDecision(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		retry bool
		code  string
		state string
	}{
		// Not weather: repeating them changes nothing, and each sends a person somewhere different.
		{"image key rejected", orimages.ErrUnauthorized, false, CodeUnauthorized, entity.DesignAttemptFailed},
		{"image out of credit", orimages.ErrOutOfCredit, false, CodeOutOfCredit, entity.DesignAttemptFailed},
		{"image slug retired", orimages.ErrModelUnavailable, false, CodeModelRetired, entity.DesignAttemptFailed},
		{"vector key rejected", recraft.ErrUnauthorized, false, CodeUnauthorized, entity.DesignAttemptFailed},
		{"vector no credits", recraft.ErrInsufficientCredits, false, CodeOutOfCredit, entity.DesignAttemptFailed},
		{"meshy key rejected", meshy.ErrUnauthorized, false, CodeUnauthorized, entity.DesignAttemptFailed},
		// We sent something unacceptable; a retry repeats it exactly.
		{"vector bad request", recraft.ErrBadRequest, false, CodeBadRequest, entity.DesignAttemptFailed},
		{"meshy image count", meshy.ErrImageCount, false, CodeBadRequest, entity.DesignAttemptFailed},
		// ЭТИХ ТРЁХ ЗДЕСЬ НЕ БЫЛО, И КАЖДЫЙ УХОДИЛ В ДЕФОЛТНУЮ ВЕТКУ — то есть читался как ПОГОДА
		// и жёг все пять попыток на запросе, который провайдер уже отверг. Строка истории при этом
		// говорила `provider_unavailable`: человек шёл смотреть статус поставщика вместо того,
		// чтобы починить свой запрос.
		{"meshy prompt over the ceiling", meshy.ErrPromptTooLong, false, CodeBadRequest, entity.DesignAttemptFailed},
		{"meshy refused the request (4xx)", meshy.ErrBadRequest, false, CodeBadRequest, entity.DesignAttemptFailed},
		{"image request we built wrong", orimages.ErrBadRequest, false, CodeBadRequest, entity.DesignAttemptFailed},
		// Billed and useless: `unknown` is the schema's word for it and a person has to read it.
		{"image returned nothing", orimages.ErrNoImages, false, CodeEmptyResponse, entity.DesignAttemptUnknown},
		{"vector malformed", recraft.ErrInvalidResponse, false, CodeEmptyResponse, entity.DesignAttemptUnknown},
		{"raster under a vector name", recraft.ErrNotVector, false, CodeWrongFormat, entity.DesignAttemptUnknown},
		{"unsafe svg", recraft.ErrUnsafeSVG, false, CodeWrongFormat, entity.DesignAttemptUnknown},
		{"response over the ceiling", orimages.ErrResponseTooLarge, false, CodeResponseTooLarge, entity.DesignAttemptUnknown},
		// The provider ended the task itself and returned the credits.
		{"meshy task failed", meshy.ErrTaskFailed, false, CodeTaskFailed, entity.DesignAttemptFailed},
		// Refused, therefore not billed: the one fault that may be repeated with a clear conscience.
		{"image rate limited", orimages.ErrRateLimited, true, CodeRateLimited, entity.DesignAttemptFailed},
		{"vector rate limited", recraft.ErrRateLimited, true, CodeRateLimited, entity.DesignAttemptFailed},
		{"meshy rate limited", meshy.ErrRateLimited, true, CodeRateLimited, entity.DesignAttemptFailed},
		// Still baking; the next pass collects it for free off the accepted attempt.
		{"meshy not ready", meshy.ErrNotReady, true, CodeProviderTimeout, entity.DesignAttemptUnknown},
		{"meshy timed out", meshy.ErrTimedOut, true, CodeProviderTimeout, entity.DesignAttemptUnknown},
		{"provider 5xx", orimages.ErrProviderFailure, true, CodeProviderUnavailable, entity.DesignAttemptUnknown},
		// Ours.
		{"no route", errRouteMissing, false, CodeKindNotAvailable, entity.DesignAttemptFailed},
		{"no key", errProviderDisabled, false, CodeKindNotAvailable, entity.DesignAttemptFailed},
		{"nowhere to store it", errSinkUnsupported, false, CodeOutputNotStorable, entity.DesignAttemptFailed},
		{"delivered then storage refused", errStorageFailed, false, CodeStorageFailed, entity.DesignAttemptDelivered},
		// An unrecognised fault is most often transport weather — orimages reports a dead
		// connection as a plain wrapped error and as no sentinel at all.
		{"unclassified", errors.New("connection reset by peer"), true, CodeProviderUnavailable, entity.DesignAttemptUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Wrapped, because that is how every one of them actually arrives.
			got := classify(fmt.Errorf("designgen: run 1: %w", c.err))
			require.Equal(t, c.retry, got.Retryable, "retryability")
			require.Equal(t, c.code, got.Code, "error code")
			require.Equal(t, c.state, got.State, "attempt state")
		})
	}
}

// TestTerminalFaultsCloseTheRunInOnePass is the running half of the table above: a rejected key
// must not consume five paid-looking attempts before the history admits what happened.
func TestTerminalFaultsCloseTheRunInOnePass(t *testing.T) {
	for _, e := range []error{orimages.ErrUnauthorized, orimages.ErrOutOfCredit, orimages.ErrModelUnavailable} {
		st := &fakeStore{}
		img := &fakeProvider{name: "image", err: e}
		w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

		require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindFlat), "tok"))
		require.Len(t, st.failed, 1)
		require.False(t, st.failed[0].Retryable, "%v must not be retried", e)
	}
}

// TestRateLimitIsRetried — the mirror of the test above. A worker that treated every fault as
// terminal would throw away jobs over a provider's ordinary back-pressure.
func TestRateLimitIsRetried(t *testing.T) {
	st := &fakeStore{}
	img := &fakeProvider{name: "image", err: orimages.ErrRateLimited}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})

	require.NoError(t, w.execute(context.Background(), testRun(1, entity.DesignRunKindFlat), "tok"))
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable)
	require.True(t, st.failed[0].NextAttempt.IsZero(),
		"the backoff is the store's policy and must not be duplicated here")
}
