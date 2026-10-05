package designgen

import (
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/runblob"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
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
	// errLandingFailed — the provider delivered and the pictures could not be FILED, after a second
	// try (run 148). Terminal: a retry would buy them again.
	errLandingFailed = errors.New("designgen: the pictures were generated but could not be saved — try again")
	// errDuplicateView — TWO PLATES OF THIS RUN CLAIM THE SAME SIDE OF THE GARMENT, and a build
	// has one slot per side. Raised before the request leaves, so nothing is spent; see falViews.
	errDuplicateView = errors.New("designgen: two input plates claim the same view of the garment")
	// errOverDelivery — ПРОВАЙДЕР ПРИСЛАЛ БОЛЬШЕ КАДРОВ, ЧЕМ ПРОГОН КУПИЛ, и лишние не подшиты.
	//
	// ⚠ ЭТО ЖАЛОБА ДОСТАВЛЕННОЙ ПОПЫТКИ, А НЕ ПРОВАЛ. Кадр есть, он оплачен и лежит в карточке;
	// записан ровно тот факт, что ответ был шире заказа, — иначе «в ленте один кадр, а сколько
	// прислала модель» было бы неизвестно никому и никогда. См. narrowToOneOutput.
	errOverDelivery = errors.New("designgen: the provider delivered more pictures than this run bought")
	// errThreedOptionNotRead — the frozen run states a 3D option (texture off, pbr on, detailed, surface
	// words) that the route this pass would pay does not read: the configuration moved between the
	// door and the pickup. Raised before StartAttempt, so nothing is spent; terminal, because the
	// frozen params and the configured route give the same answer on every pass.
	errThreedOptionNotRead = errors.New("designgen: the configured 3D route does not read an option this run states")
	// errThreedReserveShort — every reading candidate of the 3D route would book more than the door
	// reserved for this run: the route was edited (a dearer candidate, a raised price) between the door
	// and the pickup (Codex REVIEW-F1 #1). Before StartAttempt, so free; terminal, because the frozen
	// reservation never grows — the owner starts the run again and the door reserves anew.
	errThreedReserveShort = errors.New("designgen: the 3D route would book more than this run reserved")
	// errAcceptedNotRecorded — an asynchronous route ACCEPTED a paid submit and the attempt row that
	// would carry its id could not be written (G-03, Codex 1a). The id lives only in this pass's
	// memory, so the pass fails closed: terminal, `submit_unconfirmed`, the id in last_error for a
	// person to reconcile — never «carry on and hope», which leaves the next pickup free to buy the
	// job again.
	errAcceptedNotRecorded = errors.New("designgen: a paid submit was accepted and its request id could not be written down")
	// errUnresolvedSubmit — the pickup found an earlier submit of this asynchronous run that never
	// closed (`dispatching`, no finished_at) and no accepted id: the pass that sent it died between
	// the POST and the write. Whether fal queued (and charged) it is unknown, so the run fails closed
	// instead of submitting a second time (G-03, Codex 1a).
	errUnresolvedSubmit = errors.New("designgen: an earlier submit of this run never closed; whether it was bought is unknown")
	// errPaidCollectBlocked — a run whose job is ALREADY BOUGHT (an accepted id on record) cannot be
	// collected on this pass because its route is off right now (FAL_KEY removed, the route
	// unwired). The collect is free and the job is paid: this is weather for the run, retryable,
	// never the terminal «kind not available» that would release the reserve over a paid result
	// (G-03, Codex 2).
	errPaidCollectBlocked = errors.New("designgen: a paid job cannot be collected while its route is off")
	// errSubmitSettling — the pickup found an earlier submit still open (`dispatching`, no accepted
	// id) that is YOUNGER than the longest a live pass could still be inside it (submitSettleGrace):
	// lease expiry proves the old worker lost the claim, not that its paid call has stopped (G-03 r2,
	// Codex 3). Closing it `unknown` now would race the late worker's accepted id; the run instead
	// comes back once the grace has passed, when the row is either closed by its owner (accepted →
	// a free collect) or provably abandoned (→ `submit_unconfirmed`). Retryable, nothing spent.
	errSubmitSettling = errors.New("designgen: an earlier submit of this run may still be settling")
	// errEngineSwitchedOff — the frozen engine is a flagged row (Gemini / Seedream) whose flag is off
	// at the pickup (G-03, Codex 6). Refused before StartAttempt, so nothing is spent; terminal,
	// the door's own word (`unknown_image_model`).
	errEngineSwitchedOff = errors.New("designgen: the frozen engine is switched off on this deployment")

	// ─── the video route (B-32, runblob Kling image-to-video) — its four outcomes after the submit ───
	//
	// errVideoNotReady — the generation is still pending / processing: the collect looks again, for
	// free (retryable; the submit's `accepted` row keeps the id).
	errVideoNotReady = errors.New("designgen: the video is still being generated")
	// errVideoFailed — runblob ended the generation itself (`failed`) and REFUNDS it («Your balance is
	// automatically refunded on any failed task»): terminal, and it cost nothing — `failed`, not
	// `unknown`.
	errVideoFailed = errors.New("designgen: runblob failed the video generation (refunded)")
	// errVideoNoResult — `completed`, and nothing usable came of it: no video_url, a file that is not
	// an mp4, or one past the store's ceiling. The submit's price is REAL (no refund is documented for
	// a completed job), so the collect carries it beside this error — `unknown`, priced.
	errVideoNoResult = errors.New("designgen: the video generation completed with no usable clip")
	// errVideoFetchFailed — the clip's download broke on the wire (a reset, a deadline, a 5xx from the
	// CDN): the generation is done and its url is durable, so looking again is free (retryable).
	errVideoFetchFailed = errors.New("designgen: the finished video could not be downloaded")
	// errVideoSubmitUnconfirmed — the submit LEFT and no usable answer came back (a post-write break,
	// a 408, a 5xx other than a bare 503, a 2xx with no generation id): runblob may have queued and
	// charged the clip, and nothing on record can resume it. D-16: never fall back, never resubmit —
	// `unknown`, terminal, reconciled by a person. fal's ErrSubmitUnconfirmed, for the same reason.
	errVideoSubmitUnconfirmed = errors.New("designgen: the video submit may have been bought and cannot be confirmed")
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

	// CodeOverDelivery — ОТВЕТ ОКАЗАЛСЯ ШИРЕ ЗАКАЗА: провайдер прислал несколько кадров на прогон,
	// который дверь продала как один, и наружу поехал первый.
	//
	// ⚠ ЭТО КОД ДОСТАВЛЕННОЙ ПОПЫТКИ, КАК pattern_not_seamless И cutout_no_alpha. Купленный кадр
	// на месте, прогон закрывается `done`, а строка попытки несёт единственное свидетельство того,
	// что ответ был не такой формы, как заказ, — без него «модель вернула два варианта» нельзя
	// узнать вообще ниоткуда: лишние байты никуда не записаны, и правильно, что не записаны.
	CodeOverDelivery = "over_delivery"

	// CodeJobTooLarge — ЗАДАНИЕ НЕ ВЛЕЗАЕТ В ПАМЯТЬ, И НИ ОДИН ЦЕНТ ЗА НЕГО НЕ УПЛАЧЕН. Производные
	// плейграунда едут base64 внутри тела запроса и живут в процессе, у которого пол-гигабайта;
	// отказ выносится при сборке задания, то есть до StartAttempt. Слово в строке отличает его от
	// `provider_response_too_large` — того же по звучанию отказа с ДРУГОЙ стороны провода и с уже
	// потраченными деньгами.
	CodeJobTooLarge = "job_too_large"

	// CodeSourceTooLarge — ОДНА КАРТИНКА ПРОГОНА НЕ ЧИТАЕТСЯ ВОВСЕ, И НИ ОДИН ЦЕНТ НЕ УПЛАЧЕН.
	// Заголовок объявляет больше пикселей, чем процесс разворачивает, и растр не трогали. Отдельное
	// слово от `job_too_large` потому, что человеку из них следуют РАЗНЫЕ действия: там — снять
	// область или уменьшить набор, здесь — заменить конкретный кадр. См. errFreeformSourceTooLarge.
	CodeSourceTooLarge = "source_too_large"

	// CodeSourceGone — ПРЕДПОСЫЛКА ПРЕСЕТА НЕ ПЕРЕЖИЛА РЕЗОЛВ МЕДИА, И ОТКАЗ ТОЖЕ БЕСПЛАТНЫЙ.
	// Дверь спрашивала пресет у ПАРАМЕТРОВ, а сборка задания собирает его из ВЫЖИВШИХ строк медиа;
	// картинка, удалённая между дверью и проходом, превращала выполнимую просьбу в оплаченную
	// «как получится». См. errFreeformSourceGone в snapshot.go.
	CodeSourceGone = "source_gone"

	// CodeSourceTooSmall — the picture a generation window is cut from is too small to cut (under
	// windowMinSource px on a side). Free and terminal, like its neighbours.
	CodeSourceTooSmall = "source_too_small"

	// CodeOptionNotRead — the configured 3D route would drop an option the run states (see
	// errThreedOptionNotRead). The door's own word for the same fact (entity.DesignErrorCodeOptionNotRead),
	// said again at the pickup because the configuration can move in between. Free and terminal.
	CodeOptionNotRead = entity.DesignErrorCodeOptionNotRead
	// CodeThreedReserveShort — the 3D route would book above the run's reservation (errThreedReserveShort):
	// the door's word for «no reserve number covers this route», reused for «the number is too small».
	CodeThreedReserveShort = entity.DesignErrorCodeThreedReserveUnbounded

	// CodeSubmitUnconfirmed — A PAID SUBMIT WHOSE OUTCOME IS UNKNOWN (G-03, Codex 1): the request may
	// have reached the provider and been charged, and nothing on record says so for sure — a transport
	// failure after the request was written, a 5xx or unreadable 2xx on the submit, an accepted id that
	// could not be written down, or an earlier submit that never closed. Terminal (a retry could buy the
	// job twice against one reservation), attempt state `unknown`; last_error carries whatever id is
	// known. The owner reconciles it with the provider's own dashboard.
	CodeSubmitUnconfirmed = "submit_unconfirmed"

	// CodeSubmitSettling — the run waits for an earlier, possibly still live submit to close (see
	// errSubmitSettling). Retryable; the run comes back at the end of the grace.
	CodeSubmitSettling = "submit_settling"

	// CodePaidCollectWaiting — a BOUGHT job waits for its route to come back (errPaidCollectBlocked).
	// The store reads this word as a wait that spends no round of the ceiling — see
	// entity.DesignErrorCodePaidCollectWaiting (G-03 r2, Codex 4).
	CodePaidCollectWaiting = entity.DesignErrorCodePaidCollectWaiting

	// CodeUnknownImageModel — the frozen engine is not on this deployment's table at the pickup (a
	// B-16 flag went off): the door's own word for the same fact. Free and terminal.
	CodeUnknownImageModel = entity.DesignErrorCodeUnknownImageModel

	// CodeProviderPaused — the route's candidates that would draw the run are held by their open
	// circuit breakers (errRoutePaused, B-13/A5). Free (no attempt row), retryable: the run comes back
	// at the end of the breaker window. Its own word, not kind_not_available — that one tells a person
	// to configure something, and here nothing is wrong with the configuration.
	CodeProviderPaused = "provider_paused"
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
// ⚠ TWO SOURCES, EACH ANSWERING WHAT IT KNOWS (B-14). The SENTINEL names the fault — the Code a
// person reads on the row, and a base State — and it is read first, by classifyBySentinel below. The
// TRANSPORT'S *aiprov.CallError, when the chain carries one, then answers the two MONEY questions
// itself: Retryable is its Retryable (a transport never calls an engaged failure retryable), and the
// State is `unknown` when the request was written (Engaged — the provider may be billing it) and
// `failed` when it provably was not. A sentinel cannot answer those: orimages' ErrProviderFailure was
// both «a 5xx, nothing billed» and «the round trip broke after the write», and the one retry rule it
// could carry paid the second case twice. A `delivered` verdict is never touched: the provider was
// paid and the picture is on file, whatever the call's own error says.
//
// ⚠ THE FOUR TERMINAL-BY-MONEY CASES, NAMED. A rejected key (401/403), an exhausted balance (402),
// a retired model slug and a request we built wrong all produce the SAME answer however many times
// they are repeated. Letting the queue spend five attempts on them buys nothing and hides the real
// cause behind a row that reads "failed after 5 attempts" instead of "the key was rejected".
//
// ⚠ ONE EXCEPTION TO «THE TRANSPORT DECIDES RETRYABLE»: A CANCELLATION BEFORE THE WRITE (B-13/A2,
// Codex B-14 review P2 #3). Every transport calls a caller's cancel not retryable, and it is right to,
// for ITS readers: the registry's breaker must not count it (the provider did nothing wrong) and the
// chat router must not fall through to the next candidate for a caller who has already left. In THIS
// worker the canceller is the worker itself — Stop on a redeploy cancels the pass — and the run's
// owner has not left at all. A cancel that landed after StartAttempt and before the request was
// written moved no money and nobody saw the request; closing the run terminally for it would throw
// away a job the next instance could run. So an unengaged `canceled` is retried here (the attempt
// closes `failed`, the queue picks the run up again); an engaged one — the request was written, the
// provider may be billing it — stays final like every engaged failure.
//
// ⚠ FOR A CallError THE TRANSPORT DECIDES; THE DEFAULT LEANS RETRYABLE ONLY FOR ERRORS NO TRANSPORT
// SPOKE FOR. Before B-14 a transport failure — DNS, a reset connection, a proxy hiccup — reached this
// function as a plain wrapped error, so an unrecognised fault was read as weather and retried, and a
// deadline that expired AFTER the request was written was retried with it: a second payment for one
// picture. Every design transport now says which side of the write it broke on, so the lean survives
// only for the rare error nobody classified. The money is bounded anyway: the attempt cap is the
// store's, and it is a money figure.
func classify(err error) verdict {
	v := classifyBySentinel(err)
	ce, ok := aiprov.AsCallError(err)
	if !ok || v.State == entity.DesignAttemptDelivered {
		return v
	}
	v.Retryable = ce.Retryable || (!ce.Engaged && ce.Code == aiprov.CodeCanceled)
	if ce.Engaged {
		v.State = entity.DesignAttemptUnknown
	} else {
		v.State = entity.DesignAttemptFailed
	}
	return v
}

// classifyBySentinel is the sentinel half of classify: the Code, and the State and Retryable a fault
// had before any transport said whether money moved. classify overrides the money answers from the
// CallError; nothing else calls this.
func classifyBySentinel(err error) verdict {
	switch {
	// ─── ours, G-03: a PAID job waiting for its route to come back. Retryable, before any attempt
	// row (the resume is free). Its own word, because the store reads it as a WAIT that does not
	// spend the ten-round ceiling (G-03 r2, Codex 4): a key gone for longer than ten back-offs must
	// not close a bought job.
	case errors.Is(err, errPaidCollectBlocked):
		return verdict{Retryable: true, Code: CodePaidCollectWaiting, State: entity.DesignAttemptFailed}
	// ─── ours, G-03 r2: an earlier submit may still be live. Retryable, nothing spent.
	case errors.Is(err, errSubmitSettling):
		return verdict{Retryable: true, Code: CodeSubmitSettling, State: entity.DesignAttemptFailed}
	// ─── ours, B-13/A5: every candidate that would draw the run is held by its open breaker.
	// Retryable, before StartAttempt, nothing spent — the breaker heals itself, and the terminal
	// kind_not_available it used to read as closed runs a few minutes of weather would have let through.
	case errors.Is(err, errRoutePaused):
		return verdict{Retryable: true, Code: CodeProviderPaused, State: entity.DesignAttemptFailed}
	// ─── ours + fal, G-03 / B-13/A1: a submit that may have been bought, with nothing on record to
	// resume it by. FIRST among the provider cases: submitLost also wraps ErrUnexpectedResponse, and a
	// 5xx would otherwise fall into the retryable default — both would read as «resubmit».
	case errors.Is(err, errAcceptedNotRecorded), errors.Is(err, errUnresolvedSubmit),
		errors.Is(err, fal.ErrSubmitUnconfirmed),
		errors.Is(err, errVideoSubmitUnconfirmed):
		return verdict{Retryable: false, Code: CodeSubmitUnconfirmed, State: entity.DesignAttemptUnknown}
	// ─── ours: the frozen engine cannot be drawn here — its flag went off (G-03, Codex 6), or no
	// candidate of the image route serves its slug (B-13). Before StartAttempt, free, terminal: the
	// door's own word for the same fact.
	case errors.Is(err, errEngineSwitchedOff), errors.Is(err, errNoCandidateServes):
		return verdict{Retryable: false, Code: CodeUnknownImageModel, State: entity.DesignAttemptFailed}
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
		errors.Is(err, orimages.ErrNotConfigured), errors.Is(err, fal.ErrNotConfigured):
		return verdict{Retryable: false, Code: CodeKindNotAvailable, State: entity.DesignAttemptFailed}
	case errors.Is(err, errSinkUnsupported):
		return verdict{Retryable: false, Code: CodeOutputNotStorable, State: entity.DesignAttemptFailed}
	// ─── ours: the job was refused while it was being BUILT, before StartAttempt and therefore
	// before any money. Terminal because the snapshot is frozen: the next pass assembles the very
	// same pictures out of the very same params and meets the very same ceiling, so a retry buys
	// five identical refusals and hides the one thing a person can act on — the picture is too big.
	case errors.Is(err, errFreeformJobTooLarge):
		return verdict{Retryable: false, Code: CodeJobTooLarge, State: entity.DesignAttemptFailed}
	// ─── ours: the same seam, one step earlier and about ONE picture rather than their sum. A
	// source whose header declares more pixels than this process unpacks is refused before it is
	// decoded — and therefore before any money, since buildJob runs before StartAttempt. Terminal
	// for the same reason as its neighbour: the snapshot is frozen and the media row is immutable,
	// so the next pass meets the very same header.
	case errors.Is(err, errFreeformSourceTooLarge):
		return verdict{Retryable: false, Code: CodeSourceTooLarge, State: entity.DesignAttemptFailed}
	// ─── ours: the preset's own prerequisite did not survive to the pass. Refused while the job was
	// BUILT, so no money moved; terminal because the row that vanished does not come back and the
	// snapshot is frozen. See freeformPrerequisitesSurvived.
	case errors.Is(err, errFreeformSourceGone), errors.Is(err, errFlatUnderdrawingGone):
		return verdict{Retryable: false, Code: CodeSourceGone, State: entity.DesignAttemptFailed}
	case errors.Is(err, errFreeformSourceTooSmall):
		return verdict{Retryable: false, Code: CodeSourceTooSmall, State: entity.DesignAttemptFailed}
	// ─── ours, phase 3: an extend whose frozen target adds no pixels to the picture actually read
	// (the door's second lock, for a media row with no stored dimensions). Built before
	// StartAttempt, so free; terminal because the snapshot and the picture are frozen.
	case errors.Is(err, errExtendNothingToAdd):
		return verdict{Retryable: false, Code: CodeTargetAspectMustExtend, State: entity.DesignAttemptFailed}
	// ─── ours, phase 3: the mask of a retouch fails its second lock at build time (before
	// StartAttempt, so free). Terminal: the mask row is immutable and the snapshot frozen.
	case errors.Is(err, errInpaintMaskGone):
		return verdict{Retryable: false, Code: CodeSourceGone, State: entity.DesignAttemptFailed}
	case errors.Is(err, errInpaintMaskMismatch):
		return verdict{Retryable: false, Code: CodeMaskSizeMismatch, State: entity.DesignAttemptFailed}
	case errors.Is(err, errInpaintMaskEmpty):
		return verdict{Retryable: false, Code: CodeMaskEmpty, State: entity.DesignAttemptFailed}
	case errors.Is(err, errInpaintMaskUnreadable):
		return verdict{Retryable: false, Code: CodeMaskInvalid, State: entity.DesignAttemptFailed}
	case errors.Is(err, errThreedOptionNotRead):
		return verdict{Retryable: false, Code: CodeOptionNotRead, State: entity.DesignAttemptFailed}
	case errors.Is(err, errThreedReserveShort):
		return verdict{Retryable: false, Code: CodeThreedReserveShort, State: entity.DesignAttemptFailed}

	// ─── ours: DELIVERED, AND THE PICTURE IS KEPT. The tile was bought and filed; what failed is a
	// property of the picture, not of the call. Retrying is forbidden for the ordinary reason — it
	// would pay again for the same kind of answer from the same general-purpose model — and the
	// state is `delivered` because that is what happened. See seam.go.
	case errors.Is(err, errPatternNotSeamless):
		return verdict{Retryable: false, Code: CodePatternNotSeamless, State: entity.DesignAttemptDelivered}

	// ─── ours: THE SAME SEAM ONE ROUTE OVER. A cut-out that came back with nothing cut out is a
	// property of the delivered picture, not of the call: the matting model answered, the money is
	// spent, the file is kept and shown. What must NOT happen is a retry — the same picture from the
	// same model gives the same answer, and until this branch existed the sentinel fell into the
	// retryable default below and the run BOUGHT THAT ANSWER AGAIN up to the paid-attempt cap while
	// the history row said `provider_unavailable`, sending a person to the status page of a provider
	// that was working perfectly. See errCutoutNoAlpha in cutoutfal.go.
	case errors.Is(err, errCutoutNoAlpha):
		return verdict{Retryable: false, Code: CodeCutoutNoAlpha, State: entity.DesignAttemptDelivered}

	// ─── ours: DELIVERED, AND DELIBERATELY NOT LOOKED INTO. The header of the bought picture
	// declares more pixels than this process unpacks, so the alpha check refused to decode it. Same
	// state and same non-retryability as the neighbour above, and for the sharper reason: the next
	// pass would buy the same picture from the same model and refuse to read it again. See
	// errCutoutTooLarge.
	case errors.Is(err, errCutoutTooLarge):
		return verdict{Retryable: false, Code: CodeCutoutTooLarge, State: entity.DesignAttemptDelivered}

	// ─── ours: DELIVERED, AND WIDER THAN THE ORDER. The first picture is kept and filed, the rest
	// were never uploaded. Not retryable for the plainest reason of all: the run got what it paid
	// for, and a second pass would buy a second answer to a question already answered.
	case errors.Is(err, errOverDelivery):
		return verdict{Retryable: false, Code: CodeOverDelivery, State: entity.DesignAttemptDelivered}

	// ─── ours: DELIVERED, and the answer could not be fitted back into the frame it was cut from.
	// The crop is kept and filed — it is bought, and it shows how the hardware sat — but it is not
	// the picture that was asked for, and the row says which. Not retryable: whatever stopped the
	// composite (an unreadable original, a frame that no longer matches its frozen bounds) stops it
	// again on the next pass, at the price of a second generation.
	case errors.Is(err, errWindowNotComposited):
		return verdict{Retryable: false, Code: CodeWindowNotComposited, State: entity.DesignAttemptDelivered}
	// ─── ours, phase 3: the same seam for an extend — the canvas is bought and filed as delivered,
	// the source could not be pasted back into it. Not retryable: the next pass would buy a second
	// canvas and meet the same obstacle.
	case errors.Is(err, errExtendNotComposited):
		return verdict{Retryable: false, Code: CodeExtendNotComposited, State: entity.DesignAttemptDelivered}
	// ─── ours, phase 3: the retouch crop is bought and filed; it could not go back through the mask.
	case errors.Is(err, errInpaintNotComposited):
		return verdict{Retryable: false, Code: CodeInpaintNotComposited, State: entity.DesignAttemptDelivered}

	// ─── ours: delivered, then our storage refused. RETRY FORBIDDEN — it pays again for bytes we
	// already had, which is the single most expensive mistake this worker could make.
	case errors.Is(err, errLandingFailed):
		return verdict{Retryable: false, Code: entity.DesignErrorCodeLandingFailed, State: entity.DesignAttemptDelivered}
	case errors.Is(err, errStorageFailed):
		return verdict{Retryable: false, Code: CodeStorageFailed, State: entity.DesignAttemptDelivered}

	// ─── credentials and balance: not weather ───
	case errors.Is(err, orimages.ErrUnauthorized), errors.Is(err, fal.ErrUnauthorized):
		return verdict{Retryable: false, Code: CodeUnauthorized, State: entity.DesignAttemptFailed}
	case errors.Is(err, orimages.ErrOutOfCredit), errors.Is(err, fal.ErrOutOfCredit):
		return verdict{Retryable: false, Code: CodeOutOfCredit, State: entity.DesignAttemptFailed}
	// ⚠ fal.ErrModelUnavailable СТОИТ ИМЕННО ЗДЕСЬ, А НЕ В ПОГОДЕ, И ЭТО ТОТ САМЫЙ ДЕФЕКТ, КОТОРЫЙ
	// УЖЕ РУБИЛ ОБЕ AI-ФУНКЦИИ РАЗОМ: снятый провайдером идентификатор модели маскировался под
	// временный отказ, и по экрану «такой модели нет» было не отличить от «сервис занят». Транспорт
	// различает их по ПУТИ (404 на сабмите — модель, 404 на статусе — задание), а не по английской
	// фразе провайдера, и здесь это различие доезжает до строки истории.
	case errors.Is(err, orimages.ErrModelUnavailable), errors.Is(err, fal.ErrModelUnavailable):
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
	// fal's LOCAL refusals (no front view, an unfetchable reference) are the same verdict for the
	// same reason: they are refused before the request leaves, so nothing was billed, and re-sending
	// the identical request changes nothing.
	//
	// errDuplicateView IS OURS AND SITS HERE FOR THE SAME REASON: the frozen snapshot names one
	// side twice, and it will still name it twice on the fifth pass. Unclassified it would fall
	// into the retryable default and spend the whole cap on a run that cannot become sendable.
	case errors.Is(err, errDuplicateView),
		errors.Is(err, orimages.ErrBadRequest),
		errors.Is(err, fal.ErrBadRequest), errors.Is(err, fal.ErrBadImageURL),
		errors.Is(err, fal.ErrNoFrontView):
		return verdict{Retryable: false, Code: CodeBadRequest, State: entity.DesignAttemptFailed}

	// ─── billed and useless: the money is real, the output is not ───
	case errors.Is(err, orimages.ErrNoImages),
		errors.Is(err, fal.ErrNoModel),
		errors.Is(err, fal.ErrUnexpectedResponse), errors.Is(err, fal.ErrRequestNotFound),
		// B-32: a completed video with nothing usable, and a generation id runblob no longer knows
		// (the status 404) — bought, and nothing to show for it.
		errors.Is(err, errVideoNoResult), errors.Is(err, runblob.ErrGenerationNotFound):
		return verdict{Retryable: false, Code: CodeEmptyResponse, State: entity.DesignAttemptUnknown}
	case errors.Is(err, orimages.ErrResponseTooLarge), errors.Is(err, fal.ErrTooLarge):
		return verdict{Retryable: false, Code: CodeResponseTooLarge, State: entity.DesignAttemptUnknown}

	// ─── the provider ended the task itself.
	// ⚠ У runblob И У fal СОСТОЯНИЯ РАЗНЫЕ. runblob возвращает деньги за упавшую генерацию, поэтому
	// её провал стоил ноль и закрывается как `failed`. Про fal такого обещания нет: задание,
	// упавшее ПОСЛЕ начала исполнения, вполне могло быть списано, а мы этого не узнаем — и
	// `unknown` это ровно то слово схемы, которое значит «деньги, возможно, ушли, показать нечего».
	// ⚠ С B-14 ЭТО БАЗОВЫЙ ОТВЕТ, А НЕ ПОСЛЕДНИЙ: fal.ErrTaskFailed приходит только из ответа 409/410,
	// то есть с CallError транспорта, и classify ставит состояние по ЕГО Engaged. На опросе (GET) оно
	// всегда false — строка сбора закрывается `failed`, она сама ничего не покупала; «деньги,
	// возможно, ушли» остаётся на `accepted`-строке сабмита и в леджере (collectEnd → `unknown`).
	case errors.Is(err, errVideoFailed):
		return verdict{Retryable: false, Code: CodeTaskFailed, State: entity.DesignAttemptFailed}
	case errors.Is(err, fal.ErrTaskFailed):
		return verdict{Retryable: false, Code: CodeTaskFailed, State: entity.DesignAttemptUnknown}

	// ─── retryable ───
	// The request was REFUSED, so it was not billed: the one failure that can be repeated with a
	// clear conscience.
	case errors.Is(err, orimages.ErrRateLimited), errors.Is(err, fal.ErrRateLimited):
		return verdict{Retryable: true, Code: CodeRateLimited, State: entity.DesignAttemptFailed}
	// The wait ran out on a task that is probably still alive. The submit was already closed as
	// `accepted` with its id, so the next pass COLLECTS FOR FREE instead of submitting again.
	case errors.Is(err, fal.ErrTimedOut), errors.Is(err, fal.ErrNotReady),
		errors.Is(err, errVideoNotReady):
		return verdict{Retryable: true, Code: CodeProviderTimeout, State: entity.DesignAttemptUnknown}
	// B-32: the finished clip's download broke — the url is durable, the next collect fetches again.
	case errors.Is(err, errVideoFetchFailed):
		return verdict{Retryable: true, Code: CodeProviderUnavailable, State: entity.DesignAttemptUnknown}
	// «The provider failed»: a 5xx (nothing billed — failed, retryable). The CallError tells the
	// cases apart in classify; the base answer below is what an error no transport classified still
	// gets.
	case errors.Is(err, orimages.ErrProviderFailure):
		return verdict{Retryable: true, Code: CodeProviderUnavailable, State: entity.DesignAttemptUnknown}
	// ─── no sentinel. A transport that spoke (CallError) names the code; its Retryable and Engaged
	// are applied by classify. A fal 503 without a request id arrives here — the one explicit «service
	// unavailable» refusal of a submit, not engaged, retryable; a 502/504 and every other 5xx on a
	// submit arrive wrapped in fal.ErrSubmitUnconfirmed AND engaged, and stop at the terminal case
	// above (G-03 r3, Codex BLOCKER 1).
	default:
		if ce, ok := aiprov.AsCallError(err); ok {
			return verdict{Retryable: ce.Retryable, Code: codeOfCall(ce.Code), State: entity.DesignAttemptFailed}
		}
		// ⚠ RETRYABLE BY DEFAULT, and only here: an error no transport spoke for.
		return verdict{Retryable: true, Code: CodeProviderUnavailable, State: entity.DesignAttemptUnknown}
	}
}

// codeOfCall names an unsentinelled transport failure in design_run.error_code's vocabulary from the
// transport's own word (aiprov.Code*). A deadline, a cut connection and a 5xx are one word for the
// person reading the row — the provider could not be reached or could not answer — and a caller's
// cancellation is too: the row is about the provider, and the CallError has already said «do not
// retry» for it.
func codeOfCall(code string) string {
	switch code {
	case aiprov.CodeKeyRejected:
		return CodeUnauthorized
	case aiprov.CodeOutOfCredits:
		return CodeOutOfCredit
	case aiprov.CodeModelUnknown:
		return CodeModelRetired
	case aiprov.CodeRateLimited:
		return CodeRateLimited
	case aiprov.CodeBadRequest:
		return CodeBadRequest
	case aiprov.CodeEmptyAnswer, aiprov.CodeBudgetExhausted:
		return CodeEmptyResponse
	case aiprov.CodeTooLarge:
		return CodeResponseTooLarge
	case aiprov.CodeNotConfigured:
		return CodeKindNotAvailable
	default: // transport, timeout, provider_error, canceled, and a transport that named nothing
		return CodeProviderUnavailable
	}
}
