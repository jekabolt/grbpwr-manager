package designgen

import (
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
)

// Faults this package raises itself, before or around a provider call.
var (
	// errRouteMissing — no route is wired for this run kind. A configuration fact, not weather.
	errRouteMissing = errors.New("designgen: no provider route for this run kind")
	// errProviderDisabled — the route exists but holds no credentials.
	errProviderDisabled = errors.New("designgen: the provider for this run kind is not configured")
	// errSinkUnsupported — the sink cannot store what this route produces. Raised BEFORE any
	// money moves; see the pre-flight in dispatch.go.
	errSinkUnsupported = errors.New("designgen: this route's output has nowhere to be stored")
	// errStorageFailed — the provider delivered and OUR storage refused. Money spent, nothing to
	// show, and A RETRY IS FORBIDDEN: it would pay a second time for bytes we already had.
	errStorageFailed = errors.New("designgen: the delivered bytes could not be stored")
)

// Stable machine tokens for design_run.error_code. The client renders `failed · <token>`, so they
// are a vocabulary rather than prose: a reworded sentence must not change what a row says.
const (
	CodeKindNotAvailable    = "kind_not_available"
	CodeOutputNotStorable   = "output_not_storable"
	CodeUnauthorized        = "provider_unauthorized"
	CodeOutOfCredit         = "provider_out_of_credit"
	CodeModelRetired        = "provider_model_retired"
	CodeBadRequest          = "provider_bad_request"
	CodeEmptyResponse       = "provider_empty_response"
	CodeResponseTooLarge    = "provider_response_too_large"
	CodeWrongFormat         = "provider_wrong_format"
	CodeRateLimited         = "provider_rate_limited"
	CodeProviderUnavailable = "provider_unavailable"
	CodeProviderTimeout     = "provider_timeout"
	CodeTaskFailed          = "provider_task_failed"
	CodeStorageFailed       = "storage_failed"
)

// verdict is the three separate answers a failure has to give.
//
// THEY ARE THREE BECAUSE THEY DISAGREE. A rate limit is retryable, cheap and honest. A rejected
// key is not retryable and cost nothing. A 200 that carried no picture is not retryable and cost
// money — and only the third answer, the attempt STATE, can say that: `unknown` is the schema's
// word for "the money may be gone and there is nothing to show", and a person reading the history
// must find it written down rather than infer it from a blank.
type verdict struct {
	// Retryable lets the queue schedule another paid attempt.
	Retryable bool
	// Code is the stable token for design_run.error_code.
	Code string
	// State is the design_run_attempt.state this failure closes in.
	State string
}

// classify maps a provider fault onto its verdict.
//
// ⚠ THE FOUR TERMINAL-BY-MONEY CASES, NAMED. A rejected key (401/403), an exhausted balance (402),
// a retired model slug and a request we built wrong all produce the SAME answer however many times
// they are repeated. Letting the queue spend five attempts on them buys nothing and hides the real
// cause behind a row that reads "failed after 5 attempts" instead of "the key was rejected".
//
// ⚠ THE DEFAULT LEANS RETRYABLE, ON PURPOSE. A transport failure — DNS, a reset connection, a
// proxy hiccup — is reported by orimages as a plain wrapped error and by nobody as a sentinel, so
// an unrecognised fault is most often weather. The money is bounded anyway: the attempt cap is the
// store's, and it is a money figure.
func classify(err error) verdict {
	switch {
	// ─── ours: settled before any payment ───
	case errors.Is(err, errRouteMissing), errors.Is(err, errProviderDisabled),
		errors.Is(err, orimages.ErrNotConfigured), errors.Is(err, recraft.ErrNotConfigured),
		errors.Is(err, meshy.ErrNotConfigured):
		return verdict{Retryable: false, Code: CodeKindNotAvailable, State: entity.DesignAttemptFailed}
	case errors.Is(err, errSinkUnsupported):
		return verdict{Retryable: false, Code: CodeOutputNotStorable, State: entity.DesignAttemptFailed}

	// ─── ours: delivered, then our storage refused. RETRY FORBIDDEN — it pays again for bytes we
	// already had, which is the single most expensive mistake this worker could make.
	case errors.Is(err, errStorageFailed):
		return verdict{Retryable: false, Code: CodeStorageFailed, State: entity.DesignAttemptDelivered}

	// ─── credentials and balance: not weather ───
	case errors.Is(err, orimages.ErrUnauthorized), errors.Is(err, recraft.ErrUnauthorized),
		errors.Is(err, meshy.ErrUnauthorized):
		return verdict{Retryable: false, Code: CodeUnauthorized, State: entity.DesignAttemptFailed}
	case errors.Is(err, orimages.ErrOutOfCredit), errors.Is(err, recraft.ErrInsufficientCredits):
		return verdict{Retryable: false, Code: CodeOutOfCredit, State: entity.DesignAttemptFailed}
	case errors.Is(err, orimages.ErrModelUnavailable), errors.Is(err, recraft.ErrModelUnavailable):
		return verdict{Retryable: false, Code: CodeModelRetired, State: entity.DesignAttemptFailed}

	// ─── we sent something unacceptable; a retry repeats it exactly ───
	case errors.Is(err, recraft.ErrBadRequest), errors.Is(err, meshy.ErrImageCount),
		errors.Is(err, meshy.ErrBadImageURL):
		return verdict{Retryable: false, Code: CodeBadRequest, State: entity.DesignAttemptFailed}

	// ─── billed and useless: the money is real, the output is not ───
	case errors.Is(err, orimages.ErrNoImages), errors.Is(err, recraft.ErrInvalidResponse),
		errors.Is(err, meshy.ErrNoGLB), errors.Is(err, meshy.ErrUnexpectedResponse),
		errors.Is(err, meshy.ErrTaskNotFound):
		return verdict{Retryable: false, Code: CodeEmptyResponse, State: entity.DesignAttemptUnknown}
	case errors.Is(err, orimages.ErrResponseTooLarge), errors.Is(err, meshy.ErrTooLarge):
		return verdict{Retryable: false, Code: CodeResponseTooLarge, State: entity.DesignAttemptUnknown}
	case errors.Is(err, recraft.ErrNotVector), errors.Is(err, recraft.ErrUnsafeSVG):
		return verdict{Retryable: false, Code: CodeWrongFormat, State: entity.DesignAttemptUnknown}

	// ─── the provider ended the task itself. Meshy returns the credits on FAILED, so this is a
	// failure that cost nothing — `failed`, not `unknown`.
	case errors.Is(err, meshy.ErrTaskFailed):
		return verdict{Retryable: false, Code: CodeTaskFailed, State: entity.DesignAttemptFailed}

	// ─── retryable ───
	// The request was REFUSED, so it was not billed: the one failure that can be repeated with a
	// clear conscience.
	case errors.Is(err, orimages.ErrRateLimited), errors.Is(err, recraft.ErrRateLimited),
		errors.Is(err, meshy.ErrRateLimited):
		return verdict{Retryable: true, Code: CodeRateLimited, State: entity.DesignAttemptFailed}
	// The wait ran out on a task that is probably still alive. The submit was already closed as
	// `accepted` with its id, so the next pass COLLECTS FOR FREE instead of submitting again.
	case errors.Is(err, meshy.ErrTimedOut), errors.Is(err, meshy.ErrNotReady):
		return verdict{Retryable: true, Code: CodeProviderTimeout, State: entity.DesignAttemptUnknown}
	case errors.Is(err, orimages.ErrProviderFailure), errors.Is(err, recraft.ErrProviderFailure):
		return verdict{Retryable: true, Code: CodeProviderUnavailable, State: entity.DesignAttemptUnknown}
	default:
		return verdict{Retryable: true, Code: CodeProviderUnavailable, State: entity.DesignAttemptUnknown}
	}
}
