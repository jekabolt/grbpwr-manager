package aiprov

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// ═══ THE ONE STATUS MATRIX (B-14) ═══
//
// Every HTTP transport of the stack — the chat transport (oaichat) and the design transports
// (orimages, fal, runblob) — names a failed call's Code and Retryable with THESE two
// functions and no copy of them. Moved verbatim out of oaichat, where they were born, so that a 408
// cannot be weather on the chat path and a terminal refusal on the image path: the router's fallback,
// the breaker and the design worker's retry all read CallError fields, and those fields are only as
// comparable as the table that filled them.
//
// A transport keeps its OWN SENTINELS beside the matrix (orimages.ErrProviderFailure, fal.ErrBadRequest,
// …): the sentinel names the fault for errors.Is and for the person reading the row; the CallError's
// Code and Retryable are the matrix's. Where the two disagree (fal folds a 408
// into its «bad request» sentinel, the matrix calls it weather) the transport says so at its wrap site.

// ClassifyStatus is the status → (Code, Retryable) table, one row per case on purpose (each row has a
// test and a measured mutation in oaichat). Retryable = the SAME request may succeed later and nobody
// paid for this one: 408, 429, 5xx. Every other 4xx is the provider refusing the request WE built, or
// our key or balance — re-sending it cannot end differently (orimages.classifyStatus learnt that the
// expensive way).
func ClassifyStatus(status int) (code string, retryable bool) {
	switch {
	case status == http.StatusUnauthorized:
		return CodeKeyRejected, false
	case status == http.StatusForbidden:
		return CodeKeyRejected, false
	case status == http.StatusPaymentRequired:
		return CodeOutOfCredits, false
	case status == http.StatusNotFound:
		return CodeModelUnknown, false
	case status == http.StatusRequestTimeout:
		// Named before the generic 4xx row, or that row takes it and a transient timeout becomes a
		// terminal refusal from the first attempt.
		return CodeProviderError, true
	case status == http.StatusTooManyRequests:
		return CodeRateLimited, true
	case status >= 500:
		return CodeProviderError, true
	case status >= 400:
		// 400, 422 and any 4xx not named above: the request we built.
		return CodeBadRequest, false
	default:
		// A 1xx/3xx that reached us unfollowed: not a refusal we can name, not weather we can bet on.
		return CodeProviderError, false
	}
}

// Interruption names a round trip that did not complete: our budget or the caller's deadline
// (timeout), the caller leaving (canceled — never the provider's fault, never fed to the breaker), or
// anything else on the wire (transport). Decided by typed errors and the context, not by prose.
func Interruption(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return CodeTimeout
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return CodeCanceled
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return CodeTimeout
	}
	return CodeTransport
}
