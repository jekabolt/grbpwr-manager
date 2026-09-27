package designgen

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
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
//
// B-14: the bare-sentinel rows below are the SENTINEL half — what an error no transport spoke for
// still gets. The CallError rows at the end are how the design transports' failures actually arrive
// now, and on those the transport answers the money questions: a 5xx is `failed` and retryable (was
// `unknown`), an engaged envelope error and a post-write deadline are `unknown` and NOT retryable (the
// deadline was retryable — a second payment), a pre-write failure is `failed` and retryable.
//
// MUTATIONS (measured red→green): classify returning classifyBySentinel(err) unchanged → every
// CallError row but the delivered one goes red; the Engaged → `unknown` arm inverted → the engaged
// rows read `failed`; the delivered guard dropped → the storage row reads `failed`, retryable.
func TestClassifyIsAMoneyDecision(t *testing.T) {
	type callErr = aiprov.CallError
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
		// An error NO TRANSPORT SPOKE FOR keeps the old lean: most often weather. Since B-14 the design
		// transports never produce one; the row pins the fallback, not a live path.
		{"unclassified", errors.New("connection reset by peer"), true, CodeProviderUnavailable, entity.DesignAttemptUnknown},

		// ─── B-14: the transport's CallError answers the money questions ───
		{"image 5xx: refused at the gate, nothing billed",
			&callErr{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeProviderError, HTTPStatus: 503, Retryable: true,
				Err: fmt.Errorf("%w: API error (HTTP 503): down", orimages.ErrProviderFailure)},
			true, CodeProviderUnavailable, entity.DesignAttemptFailed},
		{"image envelope broken after a 2xx: billed, never again",
			&callErr{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeProviderError, HTTPStatus: 200, Engaged: true,
				Err: errors.New("orimages: could not decode the image response envelope: unexpected EOF")},
			false, CodeProviderUnavailable, entity.DesignAttemptUnknown},
		{"image deadline AFTER the write: maybe billed, never again",
			&callErr{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeTimeout, Engaged: true,
				Err: fmt.Errorf("orimages: request failed: %w", context.DeadlineExceeded)},
			false, CodeProviderUnavailable, entity.DesignAttemptUnknown},
		{"image dial refused BEFORE the write: free to try again",
			&callErr{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeTransport, Retryable: true,
				Err: errors.New("orimages: request failed: dial tcp: connection refused")},
			true, CodeProviderUnavailable, entity.DesignAttemptFailed},
		{"image empty after a 2xx keeps its sentinel's word",
			&callErr{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeEmptyAnswer, HTTPStatus: 200, Engaged: true, Err: orimages.ErrNoImages},
			false, CodeEmptyResponse, entity.DesignAttemptUnknown},
		{"recraft direct reset after the write keeps ErrProviderFailure, loses the retry",
			&callErr{Provider: entity.AIProviderRecraft, Code: aiprov.CodeTransport, Engaged: true,
				Err: fmt.Errorf("%w: connection reset", recraft.ErrProviderFailure)},
			false, CodeProviderUnavailable, entity.DesignAttemptUnknown},
		{"a 408: the sentinel names the code, the matrix decides the retry",
			&callErr{Provider: entity.AIProviderRecraft, Code: aiprov.CodeProviderError, HTTPStatus: 408, Retryable: true,
				Err: fmt.Errorf("%w (HTTP 408): timeout", recraft.ErrBadRequest)},
			true, CodeBadRequest, entity.DesignAttemptFailed},
		{"fal bare 503 on a submit: no sentinel, the transport's word",
			&callErr{Provider: entity.AIProviderFal, Code: aiprov.CodeProviderError, HTTPStatus: 503, Retryable: true,
				Err: errors.New("fal: POST /meshy/v7/multi-image-to-3d: HTTP 503: busy")},
			true, CodeProviderUnavailable, entity.DesignAttemptFailed},
		{"fal 502 on a submit: unconfirmed and engaged",
			&callErr{Provider: entity.AIProviderFal, Code: aiprov.CodeProviderError, HTTPStatus: 502, Engaged: true,
				Err: fmt.Errorf("%w: fal: POST /x: HTTP 502: bad gateway", fal.ErrSubmitUnconfirmed)},
			false, CodeSubmitUnconfirmed, entity.DesignAttemptUnknown},
		{"fal status poll 502: a lookup, never engaged, retried for free",
			&callErr{Provider: entity.AIProviderFal, Code: aiprov.CodeProviderError, HTTPStatus: 502, Retryable: true,
				Err: errors.New("fal: GET /meshy/v7/requests/r/status: HTTP 502: bad gateway")},
			true, CodeProviderUnavailable, entity.DesignAttemptFailed},
		{"meshy 401 on a submit: the setting, not the weather",
			&callErr{Provider: entity.AIProviderMeshy, Code: aiprov.CodeKeyRejected, HTTPStatus: 401,
				Err: fmt.Errorf("%w (HTTP 401): nope", meshy.ErrUnauthorized)},
			false, CodeUnauthorized, entity.DesignAttemptFailed},
		{"the caller left before the write: not the provider's fault, not retried",
			&callErr{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeCanceled,
				Err: fmt.Errorf("orimages: request failed: %w", context.Canceled)},
			false, CodeProviderUnavailable, entity.DesignAttemptFailed},
		{"an unsentinelled 429 is named by the transport's code",
			&callErr{Provider: entity.AIProviderFal, Code: aiprov.CodeRateLimited, HTTPStatus: 429, Retryable: true,
				Err: errors.New("fal: something the switch does not know")},
			true, CodeRateLimited, entity.DesignAttemptFailed},
		{"a delivered verdict is never touched",
			fmt.Errorf("%w: %w", errStorageFailed, &callErr{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeTransport, Retryable: true,
				Err: errors.New("bucket unreachable")}),
			false, CodeStorageFailed, entity.DesignAttemptDelivered},
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
