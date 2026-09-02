package designgen

import (
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
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
	// errDuplicateView — TWO PLATES OF THIS RUN CLAIM THE SAME SIDE OF THE GARMENT, and a build
	// has one slot per side. Raised before the request leaves, so nothing is spent; see falViews.
	errDuplicateView = errors.New("designgen: two input plates claim the same view of the garment")
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
	// CodePatternNotSeamless — ПЛИТКА КУПЛЕНА И НЕ СТЫКУЕТСЯ САМА С СОБОЙ (K-13).
	//
	// ⚠ ЭТО КОД ДОСТАВЛЕННОЙ ПОПЫТКИ, А НЕ ПРОВАЛЕННОЙ, и в этом весь его смысл. Картинка получена
	// и оплачена, её кладут в карточку, прогон закрывается `done` — а строка попытки говорит, чем
	// именно результат может не быть тем, что просили. Полный ответ на вопрос «стыкуется ли»
	// по-прежнему за глазом человека (см. seam.go), но обычный провал — рамка, виньетка, просто
	// незаворачивающийся квадрат — виден отсюда в момент покупки, а не через две недели.
	CodePatternNotSeamless = "pattern_not_seamless"

	// CodeOutputRefused — СТОР ОТКАЗАЛСЯ ПОДШИТЬ ВЫДАЧУ, и это НЕ погода. Сюда попадают
	// детерминированные ошибки ВОРКЕРА: род кадра, который не может нести колорвей задания
	// (0356), два выхода с одним ординалом, неизвестный ghost_view, выход без медиа. Все они
	// дадут ТОТ ЖЕ ответ на том же задании сколько ни повторяй, поэтому единственное, что
	// покупает повтор, — ещё один платный вызов поставщика.
	CodeOutputRefused = "output_refused"
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
	// ─── ours: DELIVERED, and then the STORE refused to file it. RETRY FORBIDDEN, and this one is
	// the most expensive of the family to get wrong. The attempt is already recorded as delivered
	// (the provider was paid before CompleteRun is ever called), so an unclassified refusal here
	// falls through to `abandon`, which does NOT fail the run — the lease simply expires and the
	// queue hands the same job back out while paid attempts are under the cap. A deterministic
	// routing bug would therefore BUY THE SAME BAD OUTPUT FIVE TIMES and finish as a generic
	// `lease_expired`, with the real cause nowhere in the row.
	case errors.Is(err, entity.ErrDesignColorwayForbidden),
		errors.Is(err, entity.ErrDesignInvalidArgument):
		return verdict{Retryable: false, Code: CodeOutputRefused, State: entity.DesignAttemptDelivered}
	// ─── ours: settled before any payment ───
	case errors.Is(err, errRouteMissing), errors.Is(err, errProviderDisabled),
		errors.Is(err, orimages.ErrNotConfigured), errors.Is(err, recraft.ErrNotConfigured),
		errors.Is(err, meshy.ErrNotConfigured), errors.Is(err, fal.ErrNotConfigured):
		return verdict{Retryable: false, Code: CodeKindNotAvailable, State: entity.DesignAttemptFailed}
	case errors.Is(err, errSinkUnsupported):
		return verdict{Retryable: false, Code: CodeOutputNotStorable, State: entity.DesignAttemptFailed}

	// ─── ours: DELIVERED, AND THE PICTURE IS KEPT. The tile was bought and filed; what failed is a
	// property of the picture, not of the call. Retrying is forbidden for the ordinary reason — it
	// would pay again for the same kind of answer from the same general-purpose model — and the
	// state is `delivered` because that is what happened. See seam.go.
	case errors.Is(err, errPatternNotSeamless):
		return verdict{Retryable: false, Code: CodePatternNotSeamless, State: entity.DesignAttemptDelivered}

	// ─── ours: delivered, then our storage refused. RETRY FORBIDDEN — it pays again for bytes we
	// already had, which is the single most expensive mistake this worker could make.
	case errors.Is(err, errStorageFailed):
		return verdict{Retryable: false, Code: CodeStorageFailed, State: entity.DesignAttemptDelivered}

	// ─── credentials and balance: not weather ───
	case errors.Is(err, orimages.ErrUnauthorized), errors.Is(err, recraft.ErrUnauthorized),
		errors.Is(err, meshy.ErrUnauthorized), errors.Is(err, fal.ErrUnauthorized):
		return verdict{Retryable: false, Code: CodeUnauthorized, State: entity.DesignAttemptFailed}
	case errors.Is(err, orimages.ErrOutOfCredit), errors.Is(err, recraft.ErrInsufficientCredits),
		errors.Is(err, meshy.ErrOutOfCredit), errors.Is(err, fal.ErrOutOfCredit):
		return verdict{Retryable: false, Code: CodeOutOfCredit, State: entity.DesignAttemptFailed}
	// ⚠ fal.ErrModelUnavailable СТОИТ ИМЕННО ЗДЕСЬ, А НЕ В ПОГОДЕ, И ЭТО ТОТ САМЫЙ ДЕФЕКТ, КОТОРЫЙ
	// УЖЕ РУБИЛ ОБЕ AI-ФУНКЦИИ РАЗОМ: снятый провайдером идентификатор модели маскировался под
	// временный отказ, и по экрану «такой модели нет» было не отличить от «сервис занят». Транспорт
	// различает их по ПУТИ (404 на сабмите — модель, 404 на статусе — задание), а не по английской
	// фразе провайдера, и здесь это различие доезжает до строки истории.
	case errors.Is(err, orimages.ErrModelUnavailable), errors.Is(err, recraft.ErrModelUnavailable),
		errors.Is(err, fal.ErrModelUnavailable):
		return verdict{Retryable: false, Code: CodeModelRetired, State: entity.DesignAttemptFailed}

	// ─── we sent something unacceptable; a retry repeats it exactly ───
	//
	// ⚠ THE 4xx CASES BELONG HERE AND NOT IN THE DEFAULT, and the difference is five paid rounds.
	// The default leans retryable because an unrecognised fault is usually weather — but a
	// provider's own "this request is wrong" is the one fault a retry provably cannot fix, and it
	// used to land in that default and burn the whole attempt cap. Worse, the row then read
	// `failed · provider_unavailable`, which sends a person to look at the provider's status page
	// for a request that was never acceptable in the first place.
	//
	// The two LOCAL ceilings (input picture count, texture prompt length) are the same verdict for
	// the same reason: they are refused before the request leaves, so nothing was billed, and
	// re-sending the identical too-long list changes nothing.
	//
	// errDuplicateView IS OURS AND SITS HERE FOR THE SAME REASON: the frozen snapshot names one
	// side twice, and it will still name it twice on the fifth pass. Unclassified it would fall
	// into the retryable default and spend the whole cap on a run that cannot become sendable.
	case errors.Is(err, errDuplicateView),
		errors.Is(err, recraft.ErrBadRequest), errors.Is(err, meshy.ErrImageCount),
		errors.Is(err, meshy.ErrBadImageURL), errors.Is(err, meshy.ErrPromptTooLong),
		errors.Is(err, meshy.ErrBadRequest), errors.Is(err, orimages.ErrBadRequest),
		errors.Is(err, fal.ErrBadRequest), errors.Is(err, fal.ErrBadImageURL),
		errors.Is(err, fal.ErrNoFrontView):
		return verdict{Retryable: false, Code: CodeBadRequest, State: entity.DesignAttemptFailed}

	// ─── billed and useless: the money is real, the output is not ───
	case errors.Is(err, orimages.ErrNoImages), errors.Is(err, recraft.ErrInvalidResponse),
		errors.Is(err, meshy.ErrNoGLB), errors.Is(err, meshy.ErrUnexpectedResponse),
		errors.Is(err, meshy.ErrTaskNotFound), errors.Is(err, fal.ErrNoModel),
		errors.Is(err, fal.ErrUnexpectedResponse), errors.Is(err, fal.ErrRequestNotFound):
		return verdict{Retryable: false, Code: CodeEmptyResponse, State: entity.DesignAttemptUnknown}
	case errors.Is(err, orimages.ErrResponseTooLarge), errors.Is(err, meshy.ErrTooLarge),
		errors.Is(err, fal.ErrTooLarge):
		return verdict{Retryable: false, Code: CodeResponseTooLarge, State: entity.DesignAttemptUnknown}
	case errors.Is(err, recraft.ErrNotVector), errors.Is(err, recraft.ErrUnsafeSVG):
		return verdict{Retryable: false, Code: CodeWrongFormat, State: entity.DesignAttemptUnknown}

	// ─── the provider ended the task itself. Meshy returns the credits on FAILED, so this is a
	// failure that cost nothing — `failed`, not `unknown`.
	// ⚠ fal СТОИТ РЯДОМ, НО СОСТОЯНИЕ У НЕГО ДРУГОЕ. Meshy возвращает кредиты на FAILED, поэтому
	// его провал стоил ноль и закрывается как `failed`. Про fal такого обещания нет: задание,
	// упавшее ПОСЛЕ начала исполнения, вполне могло быть списано, а мы этого не узнаем — и
	// `unknown` это ровно то слово схемы, которое значит «деньги, возможно, ушли, показать нечего».
	case errors.Is(err, meshy.ErrTaskFailed):
		return verdict{Retryable: false, Code: CodeTaskFailed, State: entity.DesignAttemptFailed}
	case errors.Is(err, fal.ErrTaskFailed):
		return verdict{Retryable: false, Code: CodeTaskFailed, State: entity.DesignAttemptUnknown}

	// ─── retryable ───
	// The request was REFUSED, so it was not billed: the one failure that can be repeated with a
	// clear conscience.
	case errors.Is(err, orimages.ErrRateLimited), errors.Is(err, recraft.ErrRateLimited),
		errors.Is(err, meshy.ErrRateLimited), errors.Is(err, fal.ErrRateLimited):
		return verdict{Retryable: true, Code: CodeRateLimited, State: entity.DesignAttemptFailed}
	// The wait ran out on a task that is probably still alive. The submit was already closed as
	// `accepted` with its id, so the next pass COLLECTS FOR FREE instead of submitting again.
	case errors.Is(err, meshy.ErrTimedOut), errors.Is(err, meshy.ErrNotReady),
		errors.Is(err, fal.ErrTimedOut), errors.Is(err, fal.ErrNotReady):
		return verdict{Retryable: true, Code: CodeProviderTimeout, State: entity.DesignAttemptUnknown}
	case errors.Is(err, orimages.ErrProviderFailure), errors.Is(err, recraft.ErrProviderFailure):
		return verdict{Retryable: true, Code: CodeProviderUnavailable, State: entity.DesignAttemptUnknown}
	default:
		return verdict{Retryable: true, Code: CodeProviderUnavailable, State: entity.DesignAttemptUnknown}
	}
}
