package designgen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/shopspring/decimal"
)

// settleTimeout bounds everything that happens AFTER a provider has answered: the upload, the
// money, the result and the orphan sweep.
//
// IT RUNS ON A CONTEXT THAT CANNOT BE CANCELLED (context.WithoutCancel), and that is the point. A
// redeploy landing in the middle of a paid generation must not throw the picture away — the bytes
// are bought, the charge is real, and the only thing standing between them and the history row is
// a few short writes. App.Stop stops workers BEFORE it closes the database and waits for them, so
// these writes always find a live pool; the bound is what keeps that wait short.
const settleTimeout = 30 * time.Second

// runStore is the slice of the design store this worker turns. It is an interface so the pass can
// be exercised against a fake — this package must never open a database in its own tests, because
// outside CI the store's TestMain reads a production DSN and drops every table.
type runStore interface {
	ClaimRuns(ctx context.Context, n int, lease time.Duration, claimToken string) ([]entity.DesignRun, error)
	ReviveExpiredRuns(ctx context.Context) (int, error)
	GetRun(ctx context.Context, runID int) (*entity.DesignRun, error)
	RecordRunPrompt(ctx context.Context, runID int, claimToken, prompt string) error
	StartAttempt(ctx context.Context, req entity.DesignAttemptStart) (*entity.DesignRunAttempt, error)
	FinishAttempt(ctx context.Context, req entity.DesignAttemptFinish) error
	CompleteRun(ctx context.Context, req entity.DesignRunComplete) (*entity.DesignRun, error)
	FailRun(ctx context.Context, req entity.DesignRunFail) (*entity.DesignRun, error)
}

// mediaResolver is the other half of what a pass reads: input pictures by id.
type mediaResolver interface {
	GetMediaByIds(ctx context.Context, ids []int) (map[int]entity.MediaFull, error)
}

// execute takes ONE claimed run through ONE pass.
//
// The error it returns is about THE WORKER, not about the run: a run that failed for a reason of
// its own has already been written down by failRun and comes back as nil. A non-nil error means
// the pass could not even record what happened, which is the only thing worth backing the whole
// tick off for.
func (w *Worker) execute(ctx context.Context, run entity.DesignRun, token string) error {
	// ─── THE ATTEMPT HISTORY FIRST, FOR EVERY KIND (B-13). Two decisions below are read off it before
	// any provider is picked: WHICH PROVIDER a paid, accepted job belongs to (its collect goes there,
	// whatever is wired for the kind today), and which candidates of a routed kind this run's current
	// round has already paid (the chooser moves past them). It used to be read by the asynchronous
	// kinds only; a sync route has nothing to resume, but it now has a chain to walk.
	//
	// ⚠ FAIL CLOSED (G-02 r3, Codex 1). When the attempt history cannot be read, this pass does NOT
	// know whether the run was already paid for, so it must neither submit nor refuse: "fresh" would
	// either buy the build a second time or, through the route guard below, fail an already-paid
	// request terminally and release its reservation with the result never collected. The pass is
	// abandoned through the same door as a failed RecordRunPrompt / StartAttempt: nothing is written,
	// the tick backs off, the row keeps its claim until the lease dies and ReviveExpiredRuns hands it
	// back to the queue, where the next pass reads the history again.
	full, gerr := w.store.GetRun(ctx, run.Id)
	if gerr != nil {
		return w.abandon(ctx, run, fmt.Errorf("read attempts before choosing a route: %w", gerr))
	}
	var attempts []entity.DesignRunAttempt
	if full != nil {
		attempts = full.Attempts
	}

	// ─── RESUME. An asynchronous route may already have been paid: an attempt closed as `accepted`
	// carries the provider's task id, and looking that task up is FREE. Reading it before submitting
	// is the difference between resuming a job after a crash and buying it twice.
	//
	// pendingAttempt — the attempt whose submit was accepted: its ledger row is the one the collect
	// prices (B-07). 0 = not known (no ledger row to price). pendingNamed — that attempt NAMES its
	// provider, and the collector below is the provider of that name (resumeRoute): only then may the
	// collect price its row.
	var (
		prov           Provider
		err            error
		pendingID      string
		pendingAttempt int
		pendingNamed   bool
	)
	if a, ok := acceptedAttempt(attempts); ok {
		// ⚠ THE COLLECTOR IS THE PROVIDER THAT ACCEPTED THE JOB, NOT THE ONE WIRED FOR THE KIND NOW
		// (B-13; REVIEW-A4 #2). After a switch of DESIGN_THREED_PROVIDER the wired route has never seen
		// this locator: its lookup failed for good and the run sat `accepted` for ever. resumeRoute
		// looks the stored name up over every provider the worker constructed (Providers.byName) and
		// pre-flights THAT one. A refusal is a WAIT, never a re-submit (G-03, Codex 2): FAL_KEY removed
		// for an hour must not throw away a job that is bought and collectable for free — the accepted
		// id turns the refusal into `paid_collect_waiting`, which the store re-queues at the capped
		// back-off WITHOUT spending the ten-round ceiling (G-03 r2, Codex 4); a person ends the wait by
		// cancelling the run.
		if prov, err = w.resumeRoute(run, a); err != nil {
			return w.failRun(ctx, run, token, err)
		}
		pendingID, pendingAttempt = a.ProviderRequestId.String, a.AttemptNo
		pendingNamed = strings.TrimSpace(a.Provider) != ""
	} else {
		// ─── PRE-FLIGHT. Everything here happens BEFORE an attempt row exists, therefore before any
		// money can move. Each of these refusals is permanent by nature: no number of retries wires a
		// route, hands over an API key or teaches the bucket a new file type.
		//
		// IT IS THE SAME CALL THE HANDLER ALREADY MADE AT THE DOOR (PreflightKind), and it is repeated
		// here rather than trusted: the door answered when the run was created, this answers when it is
		// executed, and between the two lie a redeploy, a rotated key and a changed configuration. One
		// expression, asked twice — never two expressions.
		if prov, err = w.providers.preflight(w.sink, run.Kind); err != nil {
			return w.failRun(ctx, run, token, err)
		}
	}

	// The SAME table the door prices against and the band advertises (app.go hands EngineTableFunc to
	// both, B-13): the route's default slug + the B-16 flags. A frozen flagged slug the flags no longer
	// list is read off the catalogue (applyImageOptions) for its dial — and then REFUSED before any
	// money below (engineOffAtSubmit): the flag is what the owner turns off to stop spending on that
	// engine.
	engines := w.engines()
	job, err := buildJobWith(ctx, w.media, w.objects, run, w.c.QualityFor(run.Kind), engines)
	if err != nil {
		// A database hiccup while resolving input media. Retryable, and nothing has been spent.
		return w.failRun(ctx, run, token, err)
	}

	// chain is the routed kind's fallback state for settle; nil for every route that is not a Chooser
	// and for a resume (a collect never falls back: the job is bought).
	var chain *candidateChain
	if pendingID == "" {
		// ─── THE CANDIDATE (B-13). A routed kind (Providers.Image since B-13) pays ONE candidate per
		// pass: the first of the live route, in position order, that this run's current round has not
		// opened an attempt with (roundTried). A round that is over — every serving candidate paid once,
		// the store's back-off since — starts again at the top. The concrete candidate is what the
		// attempt row, the prompt record, the pre-flights below and the payment all see; the routed slot
		// itself never pays.
		if ch, ok := prov.(Chooser); ok {
			tried := roundTried(attempts)
			next, cerr := ch.Choose(run.Kind, job.Model, tried)
			if errors.Is(cerr, errChainExhausted) {
				tried = nil
				next, cerr = ch.Choose(run.Kind, job.Model, nil)
			}
			if cerr != nil {
				// Nothing callable, or nothing that draws the frozen slug: before StartAttempt, so free,
				// and terminal like every other pre-flight refusal.
				return w.failRun(ctx, run, token, cerr)
			}
			prov = next
			chain = newCandidateChain(ch, run.Kind, job.Model, tried, prov.Name())
		}
	}

	collector, async := prov.(Collector)

	if async && pendingID == "" {
		// ⚠ AN EARLIER SUBMIT THAT NEVER CLOSED IS A POSSIBLE PURCHASE (G-03, Codex 1a). A
		// `dispatching` attempt with no finished_at and no accepted id before it means a pass died
		// between StartAttempt and the write that closes it — before the POST, during it, or after
		// fal accepted it. Nobody can say which, and fal takes no idempotency key, so a fresh
		// submit here could buy the job a second time against the same reservation. Fail closed.
		if open, ok := unresolvedSubmit(attempts); ok {
			// ⚠ BUT NOT WHILE ITS OWNER MAY STILL BE INSIDE IT (G-03 r2, Codex 3). This pass
			// holds the claim because the previous one's LEASE expired — which proves it lost
			// the row, not that its paid call has stopped. Closing the attempt `unknown` while
			// that call can still answer races the late worker's accepted id. So a young open
			// attempt is left alone and the run comes back when the grace is over; by then
			// the row is either closed by its owner (an accepted id → a free collect) or
			// provably abandoned (closed below). The store's late-finish rule
			// (entity.DesignLateAcceptedFinish) is the second lock for a pause longer than
			// any grace.
			if settleBy := open.StartedAt.Add(w.submitSettleGrace()); w.clock().Before(settleBy) {
				cause := fmt.Errorf("%w: attempt %d on %s opened at %s and may still be answering; "+
					"looking again after %s", errSubmitSettling, open.AttemptNo, open.Provider,
					open.StartedAt.UTC().Format(time.RFC3339), settleBy.UTC().Format(time.RFC3339))
				return w.failRunAt(ctx, run, token, cause, settleBy)
			}
			cause := fmt.Errorf("%w: attempt %d on %s opened at %s and never closed — the job may "+
				"have been queued and billed; reconcile it with the provider before starting it again",
				errUnresolvedSubmit, open.AttemptNo, open.Provider,
				open.StartedAt.UTC().Format(time.RFC3339))
			w.finishAttempt(ctx, run, open.AttemptNo, nil, cause, entity.DesignAttemptUnknown)
			return w.failRun(ctx, run, token, cause)
		}
	}

	// ─── THE PROMPT GOES INTO THE HISTORY ROW BEFORE IT GOES TO A PROVIDER — И ТОЛЬКО ТОГДА,
	// КОГДА ЭТОТ ПРОХОД ДЕЙСТВИТЕЛЬНО ОТПРАВЛЯЕТ ТЕКСТ.
	//
	// ⚠ ПОРЯДОК ЗДЕСЬ ИСПРАВЛЕН ПО РЕВЬЮ, И ПРЕЖНИЙ БЫЛ НЕВЕРЕН ДВАЖДЫ. Запись стояла ВЫШЕ поиска
	// принятой попытки, поэтому на ВОЗОБНОВЛЕНИИ уже оплаченного асинхронного задания она:
	//   · переписывала колонку заново собранным текстом, который поставщику НЕ отправлялся ни
	//     разу (состав входов мог измениться между проходами — удалили медиа, переехал сборщик), —
	//     то есть история начинала утверждать про деньги неправду;
	//   · своим отказом отменяла БЕСПЛАТНЫЙ сбор результата: submit был оплачен раньше, а проход
	//     обрывался до Collect, и оплаченное задание ждало истечения аренды. Дорогая ошибка ради
	//     дешёвой записи.
	// Возобновление текст не отправляет вовсе, значит и писать ему нечего: в колонке уже лежит то,
	// что ушло на самом деле.
	//
	// СТОРОНА ХРАНЕНИЯ ВЫБРАНА НАМЕРЕННО, а не глагол предпросмотра: предпросмотр — вторая сборка
	// другим кодом в другое время, и «что показала модалка» разошлось бы с «что услышала модель»
	// молча. Здесь колонка пишется ИЗ ТОГО ЖЕ Job, который отправляют следующие строки, и ИЗ ТОГО
	// ЖЕ `prov`, который его отправляет: `recordedPrompt` спрашивает у маршрута его собственный
	// текст. На маршрутах картинок это по-прежнему `Job.Prompt` буквально (PromptCarrier они не
	// реализуют); на 3D — стир, потому что `Job.Prompt` туда не уезжает вовсе.
	//
	// RECORD-THEN-SPEND: запись стоит до `StartAttempt`, то есть до любого движения денег; её
	// отказ останавливает проход, ничего не потратив. Токен захвата сторожит её как и всякую
	// другую запись результата.
	//
	// ЧТО В КОЛОНКЕ — БАЗОВАЯ ИНСТРУКЦИЯ ЭТОГО МАРШРУТА, И СПРАШИВАЮТ ЕЁ У САМОГО МАРШРУТА
	// (PromptCarrier). На маршруте картинок это `Job.Prompt`, и на `per_view` отправленный текст
	// длиннее хранимого ровно на «view:\n<view>» (viewPrompt) — надстройка над той же базой, и
	// слово «базовая» её честно покрывает.
	//
	// ⚠ У 3D БАЗОЙ БЫЛ ЧУЖОЙ ТЕКСТ, И ЭТО БЫЛО ИЗМЕРЕНО, А НЕ ЗАПОДОЗРЕНО. Оба 3D-прогона на бете
	// ушли к поставщику ЧЕТЫРЬМЯ ССЫЛКАМИ И БЕЗ ЕДИНОГО СЛОВА (у hitem3d в теле нет текстового
	// поля вовсе), а в колонке лежал полный composePrompt — и панель истории показывала владельцу
	// «промпт прогона», которого поставщик не видел, рядом с настоящей ценой. Приписанные деньгам
	// слова хуже пустой колонки: пустая колонка ничего не утверждает, а эта выглядела уликой.
	// Теперь маршрут отвечает за свой текст сам: стир — или пусто, если слов не несёт.
	if pendingID == "" {
		// ─── THE ROUTE MUST STILL READ WHAT THE RUN PAID FOR (G-02 r2, Codex 1 + 2). The door asked
		// designgen.ThreedUnread of the route configured WHEN THE RUN WAS CREATED; this asks it of the
		// route this pass would PAY — after a redeploy to the hitem3d override or with
		// DESIGN_THREED_PBR turned off, a detailed / untextured / pbr / worded run would be bought
		// with its options silently dropped (or, for pbr, sent to the unmeasured size cap). Only a
		// FRESH submit is refused: an accepted request above is already paid and is collected.
		// Before RecordRunPrompt and StartAttempt, so nothing is spent; terminal, and failRun's
		// terminal transition releases the reservation like every other pre-call refusal.
		if err := w.threedUnreadAtSubmit(run, prov); err != nil {
			return w.failRun(ctx, run, token, err)
		}
		// …and a flagged engine whose flag went off after the door froze the run is not paid for.
		if err := engineOffAtSubmit(job, engines); err != nil {
			return w.failRun(ctx, run, token, err)
		}
		if err := w.store.RecordRunPrompt(ctx, run.Id, token, recordedPrompt(prov, job)); err != nil {
			return w.abandon(ctx, run, err)
		}
	}

	// ─── THE PAID CALL. Outside every transaction, by construction: the store verbs above and
	// below are each their own short transaction, and nothing here holds one open across a
	// network call that takes tens of seconds.
	if pendingID == "" {
		att, err := w.store.StartAttempt(ctx, entity.DesignAttemptStart{
			// THE CONCRETE NAME (B-13): the chosen candidate's (`<provider>_images`), never the routed
			// slot's — the next pass reads it back as «tried», a collect as «who accepted this».
			RunId: run.Id, ClaimToken: token, Provider: prov.Name(),
		})
		if err != nil {
			// Includes ErrDesignClaimLost: somebody else holds the row, and finding that out
			// BEFORE the money is exactly why the store checks the claim here too.
			return w.abandon(ctx, run, err)
		}
		// THE LEDGER'S SCOPE IS THE ATTEMPT JUST OPENED (B-07): every physical call Execute makes
		// books its own row under (run, this attempt, call_no). Set on the local copy the route is
		// handed; nil without a ledger, which records nothing.
		job.Recorder = w.recorderFor(run, att.AttemptNo)
		out, callErr := prov.Execute(ctx, job)

		if async && callErr == nil && out != nil && out.Pending {
			// Submitted, not delivered. Close the attempt with the task id so a worker that dies
			// during the build resumes for free, then fall through to collect in this same pass.
			//
			// BEYOND CANCELLATION, like every other write that follows a payment: this id IS the
			// resume, and losing it to an expired pass deadline means buying the model again.
			//
			// ⚠ AND IT IS A REQUIRED HANDOFF, NOT A BEST-EFFORT LOG LINE (G-03, Codex 1a). Were the
			// write to fail and the pass carry on with the id in memory, a collect that then timed out
			// (retryable) would hand the next pickup a run with no accepted id — and it would submit,
			// that is BUY, again. So a failed write fails the pass CLOSED: terminal, the id in
			// last_error for reconciliation. Should FailRun fail too, the attempt row stays open and
			// the next pickup refuses it (unresolvedSubmit) — closed either way.
			actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
			defer acancel()
			out.RequestID = fitRequestLocator(actx, run.Id, out.RequestID)
			if ferr := w.recordAttempt(actx, run, att.AttemptNo, out, nil, entity.DesignAttemptAccepted); ferr != nil {
				return w.failRun(actx, run, token, fmt.Errorf("%w: %s accepted request %s and the attempt could "+
					"not record it (%v) — the job may be running and billed; reconcile it with the provider",
					errAcceptedNotRecorded, prov.Name(), out.RequestID, ferr))
			}
			pendingID, pendingAttempt, pendingNamed = out.RequestID, att.AttemptNo, true
		} else {
			return w.settle(ctx, job, run, token, att.AttemptNo, out, callErr, chain)
		}
	}

	// ─── THE FREE COLLECT. Its own attempt row, because it is where the price of an asynchronous
	// job finally becomes known: the submit closed with a NULL price (nobody could say yet), and
	// FinishAttempt is idempotent, so the charge could never be written onto that row afterwards.
	//
	// ⚠ TWO ROWS, ONE PAYMENT — AND THE STORE IS WHAT HOLDS THAT SECOND HALF UP, in two separate
	// places, because this loop breaks both of the store's older assumptions:
	//
	//   * the ATTEMPT CAP is a money cap, so it counts payments rather than attempt rows: an
	//     attempt that follows an `accepted` one is a free lookup and does not spend a round of it
	//     (designPaidAttemptsSQL). Counted the other way, a turntable paid for once died terminally
	//     after three windows of waiting;
	//   * a REPEATED collect of the same task answers with the same consumed_credits, on a fresh,
	//     not-yet-closed attempt row. `spent` and price_actual move on the FIRST of them only —
	//     the charge is keyed by provider_request_id, not by the row that reports it
	//     (chargeAlreadyBooked).
	//
	// So `accepted` + the task id is not bookkeeping: it is the token both of those decisions read.
	if collector == nil {
		// Unreachable today: pendingID is only ever set on a route that implements Collector (a
		// fresh Pending answer, or resumeRoute, which refuses a stored provider that cannot collect).
		// It is written down anyway because the alternative to a refusal here is a nil dereference
		// on a run that has ALREADY BEEN PAID FOR, and a panic leaves it claimed until its lease dies.
		return w.failRun(ctx, run, token,
			fmt.Errorf("%w: %s accepted task %s but cannot collect it", errRouteMissing, prov.Name(), pendingID))
	}
	att, err := w.store.StartAttempt(ctx, entity.DesignAttemptStart{
		RunId: run.Id, ClaimToken: token, Provider: prov.Name(),
	})
	if err != nil {
		return w.abandon(ctx, run, err)
	}
	// ⚠ THE COLLECT PRICES THE SUBMIT'S ROW, NOT ITS OWN (B-07). A collect is a free lookup and
	// opens no ledger row; the money of the job sits on the row the submit opened, keyed by the
	// SUBMIT's attempt — so the recorder is scoped to that attempt, not to `att`. A repeated collect
	// finds the row already priced and changes nothing (PriceAcceptedCall moves only `accepted`).
	//
	// ⚠ AND ONLY THE ROUTE THAT ACCEPTED THE JOB MAY PRICE ITS ROW (Codex A4 #2) — which, since B-13,
	// is the route collecting it BY CONSTRUCTION: a resume collects with the provider the accepted
	// attempt names (resumeRoute), and a fresh submit collects with the provider that just submitted.
	// A collector that had never seen the locator used to fail its lookup for good and turn another
	// provider's `accepted` row `unknown` — IRREVERSIBLY, since PriceAcceptedCall moves only `accepted`
	// rows. The one row still withheld is a NAMELESS accepted attempt (written before the column was
	// filled): the kind's route collects it, and nothing proves that route accepted it, so its row
	// waits `accepted` rather than be priced by a guess.
	job.Recorder = nil
	if pendingNamed {
		job.Recorder = w.recorderFor(run, pendingAttempt)
	}
	out, callErr := collector.Collect(ctx, job, pendingID)
	if out != nil && out.RequestID == "" {
		out.RequestID = pendingID
	}
	return w.settle(ctx, job, run, token, att.AttemptNo, out, callErr, nil)
}

// resumeRoute is the provider that collects an ACCEPTED job (B-13): the one whose name the accepted
// attempt stored, looked up over every provider this worker constructed (Providers.byName — the six
// slots and Also), and pre-flighted like any route this pass is about to use (routeReady).
//
//   - not ready (keyless, switched off, output the sink cannot keep) → errPaidCollectBlocked: a paid
//     job WAITS for its provider (`paid_collect_waiting`), it is never re-submitted elsewhere;
//   - no provider of that name → errRouteMissing, the name in the sentence: this build no longer
//     constructs the provider that holds the job, and a person has to reconcile it with the provider;
//   - a NAMELESS row (written before the attempt stored its provider) → the kind's route, as before
//     B-13 — the only reading such a row ever had.
func (w *Worker) resumeRoute(run entity.DesignRun, a entity.DesignRunAttempt) (Provider, error) {
	id := a.ProviderRequestId.String
	var prov Provider
	if name := strings.TrimSpace(a.Provider); name == "" {
		p, err := w.providers.preflight(w.sink, run.Kind)
		if err != nil {
			return nil, fmt.Errorf("%w: request %s is paid and waits: %v", errPaidCollectBlocked, id, err)
		}
		prov = p
	} else {
		p, ok := w.providers.byName(name)
		if !ok {
			return nil, fmt.Errorf("%w: %s accepted request %s, and this deployment constructs no route named %s — "+
				"reconcile the job with the provider", errRouteMissing, name, id, name)
		}
		if err := routeReady(w.sink, p); err != nil {
			return nil, fmt.Errorf("%w: request %s is paid and waits: %v", errPaidCollectBlocked, id, err)
		}
		prov = p
	}
	if _, ok := prov.(Collector); !ok {
		return nil, fmt.Errorf("%w: %s accepted task %s but cannot collect it", errRouteMissing, prov.Name(), id)
	}
	return prov, nil
}

// engines is the engine table of this pickup: Config.Engines (app.go: the live route's default, the
// door's own function), else EngineTable over orimages.DefaultModel and this Config's flags — the
// table every worker built without app.go (the tests) resolves against.
func (w *Worker) engines() []Engine {
	if w.c != nil && w.c.Engines != nil {
		return w.c.Engines()
	}
	var flags EngineFlags
	if w.c != nil {
		flags = w.c.EngineFlags()
	}
	return EngineTable("", flags)
}

// threedUnreadAtSubmit — the worker's half of the route check: the frozen params of a threed run
// against the route this pass is about to pay (ThreedRouteOf the wired provider, at this deployment's
// DESIGN_THREED_PBR). nil for every other kind and for a run the route reads in full.
func (w *Worker) threedUnreadAtSubmit(run entity.DesignRun, prov Provider) error {
	if run.Kind != entity.DesignRunKindThreed {
		return nil
	}
	p := parseParams(run.Params)
	o := threedOptionsOf(p)
	hint := ""
	if p.Threed != nil {
		hint = p.Threed.SurfaceHint
	}
	pbr := w.c != nil && w.c.ThreedPBR
	if opt, why := ThreedUnread(ThreedRouteOf(prov, pbr), o.Texture, o.PBR, o.Quality, hint); opt != "" {
		return fmt.Errorf("%w: params.threed.%s: %s. Nothing was submitted and nothing was charged",
			errThreedOptionNotRead, opt, why)
	}
	return nil
}

// settle records the money, stores the bytes and closes the run.
//
// ORDER IS THE ARGUMENT. The charge is written FIRST, from the provider's answer alone, because it
// is already real and nothing that happens afterwards can make it less so — a bucket that refuses
// the bytes does not refund the generation. Only then are the bytes uploaded and the run closed.
//
// chain is the routed kind's fallback (B-13): nil for every other route and for a collect.
func (w *Worker) settle(ctx context.Context, job Job, run entity.DesignRun, token string, attemptNo int,
	out *Outcome, callErr error, chain *candidateChain) error {
	// The pass may be running on a context whose deadline has already passed — a long provider
	// call is exactly the case. Everything from here on is short, and losing it would lose the
	// paid result, so it runs beyond cancellation.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()

	// ─── НАРУЖУ ВЫХОДИТ РОВНО ОДИН КАДР — ТАМ, ГДЕ ДВЕРЬ ПРОДАЛА ОДИН. Стоит ДО подсчёта, потому
	// что всё ниже — состояние попытки, минт, строки выдачи — считается по этому числу.
	if dropped := narrowToOneOutput(run.Kind, out); dropped > 0 {
		if callErr == nil {
			callErr = fmt.Errorf("%w: %d pictures came back for a run that bought one; the first was "+
				"kept and the other %d were not filed", errOverDelivery, dropped+1, dropped)
		} else {
			// ⚠ ДВЕ ЖАЛОБЫ, ОДНА КОЛОНКА, И ПЕРВАЯ ВАЖНЕЕ. error_code у попытки один, а жалоба,
			// приехавшая С МАРШРУТА, — про саму купленную картинку (`cutout_no_alpha`,
			// `pattern_not_seamless`); наша — про лишние, которых человек всё равно не увидит.
			// Перетереть первую второй значило бы обменять свидетельство о товаре на служебную
			// заметку, поэтому вторая уходит в лог с теми же числами.
			slog.Default().WarnContext(sctx, "design run over-delivered beside another complaint; "+
				"the extra pictures were dropped and only the first complaint reached the attempt row",
				slog.Int("run_id", run.Id), slog.String("kind", run.Kind),
				slog.Int("dropped", dropped), slog.String("err", callErr.Error()))
		}
	}

	// ─── ОКНО ГЕНЕРАЦИИ: ОТВЕТ ВКЛЕИВАЕТСЯ ОБРАТНО В КАДР. Стоит ПОСЛЕ обрезки до одного кадра
	// (композитить есть смысл ровно тот, который поедет наружу) и ДО publish: позже кроп уже
	// заминчен строкой медиа и лежит в ленте карточки вместо кадра, который просили. Жалоба, а не
	// отказ — см. postProcess: деньги уже ушли, и кроп полезнее выброшенного прогона.
	if perr := w.postProcess(sctx, job, out); perr != nil {
		if callErr == nil {
			callErr = perr
		} else {
			slog.Default().WarnContext(sctx, "a windowed run could not be fitted back into its frame "+
				"and already carried another complaint",
				slog.Int("run_id", run.Id), slog.String("window_err", perr.Error()),
				slog.String("err", callErr.Error()))
		}
	}

	artifacts := 0
	if out != nil {
		artifacts = len(out.Artifacts)
	}

	// Success with nothing attached is a failure, and it is settled here rather than below so the
	// attempt state and the run's fate are decided by the same fact.
	if artifacts == 0 && callErr == nil {
		callErr = fmt.Errorf("%w: the provider reported success with nothing attached", errStorageFailed)
	}
	// A pass that produced pictures DELIVERED, whatever else went wrong beside them: three views
	// asked for, two arrived, the third call failed. The attempt still carries the error code, so
	// the history says both halves.
	state := entity.DesignAttemptDelivered
	if callErr != nil && artifacts == 0 {
		state = classify(callErr).State
	}
	w.finishAttempt(sctx, run, attemptNo, out, callErr, state)

	if artifacts == 0 {
		// ─── THE FALLBACK (B-13). The transport PROVED nothing was bought — a CallError, not engaged
		// (chain.next is the one place that decides which failures qualify) — and the route has a
		// candidate this round has not tried: the run goes straight back to the queue, and the next
		// pass pays that candidate on a fresh attempt row. For terminal codes too: a 401 or a 402 on
		// one provider says nothing about the next (D-09). Everything else — an engaged failure, an
		// error no transport spoke for, a round with nobody left — takes today's path exactly: the
		// store's back-off for weather, a closed run for the terminal.
		if next, ok := chain.next(callErr); ok {
			return w.advanceCandidate(sctx, run, token, callErr, chain.from, next.Name())
		}
		return w.failRun(sctx, run, token, callErr)
	}
	if callErr != nil {
		// ⚠ СТРОКА НЕ ГОВОРИТ «МЕНЬШЕ, ЧЕМ ПРОСИЛИ», ХОТЯ ГОВОРИЛА. Сюда приходят ТРИ разных
		// исхода с картинками на руках — недобор вызовов, жалоба на сам кадр
		// (`pattern_not_seamless`, `cutout_no_alpha`) и перебор (`over_delivery`), — и из них
		// «меньше, чем просили» верно только для первого. Код называется словом, оба числа рядом.
		slog.Default().WarnContext(sctx, "design run closed with a complaint beside its pictures",
			slog.Int("run_id", run.Id), slog.String("code", classify(callErr).Code),
			slog.Int("delivered", artifacts), slog.Int("requested", run.RequestedOutputs),
			slog.String("err", callErr.Error()))
	}

	// ─── BYTES INTO THE BUCKET, BEFORE THE TRANSACTION. Whatever nobody adopts is swept below.
	minted, outputs, perr := w.publish(sctx, run, out)
	if perr != nil {
		w.sweep(sctx, minted)
		return w.failRun(sctx, run, token, perr)
	}

	filed, err := w.store.CompleteRun(sctx, entity.DesignRunComplete{
		RunId:      run.Id,
		ClaimToken: token,
		Outputs:    outputs,
	})
	if err != nil {
		// NOTHING WAS FILED, SO EVERYTHING MINTED IS AN ORPHAN. Sweeping is not optional here: the
		// objects are already publicly addressable and the media rows already exist, and the only
		// list of them is the one in this stack frame.
		w.sweep(sctx, minted)
		// ⚠ «СТРОКА БОЛЬШЕ НЕ НАША» И «СТОР ОТВЕРГ ВЫДАЧУ» — ДВА РАЗНЫХ ИСХОДА, И РАНЬШЕ ОБА
		// УХОДИЛИ В abandon. Разница — в деньгах. Потерянный захват действительно не инцидент:
		// работу доделает тот, кто её перехватил. А отвергнутая выдача — детерминированный баг
		// ВОРКЕРА: платный вызов уже записан как delivered, abandon строку НЕ проваливает, лизинг
		// истекает, очередь выдаёт то же задание снова — и так до потолка платных попыток, после
		// чего строка закрывается безымянным `lease_expired`. То есть ошибка маршрутизации
		// покупала один и тот же плохой ответ пять раз и стирала собственную причину.
		//
		// Дорога «не повторять» в этом воркере уже есть — failRun с Retryable=false, — и
		// классификатор теперь называет этот класс своим кодом (CodeOutputRefused). Прогон
		// проваливается СРАЗУ и НАЗВАННО.
		if designResultRefused(err) {
			return w.failRun(sctx, run, token, err)
		}
		return w.abandon(sctx, run, err)
	}

	// ─── THE SWEEP THAT MATTERS ON SUCCESS. An idempotent re-file returns the pictures of an
	// EARLIER pass, so this pass's fresh uploads were adopted by nothing at all. "It returned no
	// error" is not "what I uploaded was taken", which is why adoption is read off the rows the
	// store actually filed.
	mintedIDs := make([]int, 0, len(minted))
	byID := make(map[int]MintedMedia, len(minted))
	for _, m := range minted {
		mintedIDs = append(mintedIDs, m.ID)
		byID[m.ID] = m
	}
	adopted := make([]int, 0, len(filed.Pictures))
	for _, p := range filed.Pictures {
		adopted = append(adopted, p.MediaId)
	}
	for _, id := range design.OrphanedMedia(mintedIDs, adopted) {
		w.sink.Drop(sctx, byID[id])
	}
	return nil
}

// narrowToOneOutput ДЕРЖИТ ПОСТ-ИНВАРИАНТ «НАРУЖУ МАКСИМУМ ОДИН КАДР» и возвращает, сколько
// артефактов не поехало дальше.
//
// ⚠ ЗАЧЕМ ЭТО ВООБЩЕ НУЖНО, ЕСЛИ МАРШРУТ ПРОСИТ n=1. Потому что «попросили один» и «пришёл один» —
// разные утверждения, и между ними стоит чужой сервер. Клиент картинок принимает ВЕСЬ `data[]`
// ответа (orimages), провайдер превращает КАЖДУЮ картинку в артефакт (images.go), а CompleteRun
// не сверяет их число с requested_outputs вовсе. То есть модель, ответившая двумя вариантами на
// `n=1`, сегодня положила бы в карточку два кадра на прогон, который дверь оценила и продала как
// ОДИН: лента карточки перестаёт совпадать с историей и со счётом, а человек не может сказать,
// какой из двух кадров он просил.
//
// ПОЧЕМУ РОД, А НЕ requested_outputs. Число выходов честно равно числу артефактов далеко не везде:
// 3D отдаёт МОДЕЛЬ И МИНИАТЮРУ на один запрошенный выход, а `per_view` — по кадру на вызов. Общая
// сверка с requested_outputs выбросила бы миниатюру турнтейбла молча. Здесь названы ровно те два
// рода, у которых дверь и сборка вызовов договорились об одном кадре и обе это утверждают
// (designRequestedOutputs, imageCalls): плейграунд и вырез.
//
// ПОЧЕМУ ПЕРВЫЙ, А НЕ «ЛУЧШИЙ». Выбор обязан быть ДЕТЕРМИНИРОВАННЫМ, иначе реран того же снимка
// даёт другой кадр по причине, которой нет в params; «лучший» же требует меры качества, которой у
// нас нет ни для выреза, ни для плейграунда. Порядок `data[]` — это порядок провайдера, он
// стабилен внутри ответа, и первый элемент — единственный, про который можно сказать, почему он.
//
// ПОЧЕМУ ЛИШНИЕ НЕ ФАЙЛЯТСЯ ВОВСЕ, А НЕ «ФАЙЛЯТСЯ, НО НЕ ПУБЛИКУЮТСЯ». Всё, что уходит в sink.Put,
// уже минтит строку медиа и публично адресуемый объект; не подшитое CompleteRun'ом становится
// сиротой, которую надо подметать (см. sweep). Обрезка ДО publish не создаёт ни объекта, ни строки:
// нечего подметать и нечему протечь.
func narrowToOneOutput(kind string, out *Outcome) int {
	if out == nil || len(out.Artifacts) <= 1 || !designKindBuysOnePicture(kind) {
		return 0
	}
	dropped := len(out.Artifacts) - 1
	out.Artifacts = out.Artifacts[:1]
	return dropped
}

// designKindBuysOnePicture — роды, у которых «сколько кадров наружу» решено ДВЕРЬЮ и равно одному.
//
// Список положительный НАМЕРЕННО, как и соседние предикаты словаря родов: новый род получает
// честное false и не наследует чужого потолка, пока кто-нибудь не напишет его сюда руками.
func designKindBuysOnePicture(kind string) bool {
	switch kind {
	case entity.DesignRunKindFreeform, entity.DesignRunKindCutout,
		entity.DesignRunKindExtend, entity.DesignRunKindInpaint:
		return true
	default:
		return false
	}
}

// publish uploads every artifact and describes it as an output row. On the first failure it stops
// and hands back what it had already minted, so the caller can sweep all of it: a half-filed run
// is worse than a failed one, because it looks finished.
func (w *Worker) publish(ctx context.Context, run entity.DesignRun, out *Outcome) ([]MintedMedia, []entity.DesignPictureInsert, error) {
	minted := make([]MintedMedia, 0, len(out.Artifacts))
	outputs := make([]entity.DesignPictureInsert, 0, len(out.Artifacts))
	for i, a := range out.Artifacts {
		m, err := w.sink.Put(ctx, a.Bytes, a.ContentType, fmt.Sprintf("run-%d-%d", run.Id, i))
		if err != nil {
			return minted, nil, err
		}
		minted = append(minted, m)
		outputs = append(outputs, entity.DesignPictureInsert{
			MediaId: m.ID,
			Ordinal: i,
			Kind:    a.Kind,
			// Empty leaves the store's own guess in force: requested views handed out by ordinal,
			// and no guess at all for a composite. The worker fills it only where it KNOWS, i.e.
			// on the per-view route where each call was made for a named side.
			GhostView:   a.GhostView,
			SourceClass: entity.DesignSourceAI,
		})
	}
	return minted, outputs, nil
}

// finishAttempt writes the money. Its failure is LOUD BUT NOT FATAL: the picture is already bought
// and, further down, filed, and refusing to file it because the ledger write failed would turn one
// lost number into one lost generation. (The ONE write that is fatal — an accepted id — calls
// recordAttempt itself; see execute.)
func (w *Worker) finishAttempt(ctx context.Context, run entity.DesignRun, attemptNo int, out *Outcome, callErr error, state string) {
	_ = w.recordAttempt(ctx, run, attemptNo, out, callErr, state)
}

// recordAttempt closes one attempt row and returns the store's answer (logged as well).
func (w *Worker) recordAttempt(ctx context.Context, run entity.DesignRun, attemptNo int, out *Outcome, callErr error, state string) error {
	req := entity.DesignAttemptFinish{
		RunId:     run.Id,
		AttemptNo: attemptNo,
		State:     state,
		Price:     decimal.NullDecimal{},
	}
	if out != nil {
		req.ProviderRequestId = out.RequestID
		req.Price = out.Price
	}
	if callErr != nil {
		req.ErrorCode = classify(callErr).Code
	}
	if err := w.store.FinishAttempt(ctx, req); err != nil {
		// The request id rides the line: when this write is the ACCEPTED one, the log is the only
		// place a paid id still exists if the run cannot be failed either (a lost claim).
		slog.Default().ErrorContext(ctx, "failed to record the money of a design attempt",
			slog.Int("run_id", run.Id), slog.Int("attempt_no", attemptNo),
			slog.String("state", state), slog.String("provider_request_id", req.ProviderRequestId),
			slog.String("err", err.Error()))
		return err
	}
	return nil
}

// failRun writes the failure down, letting the store decide the backoff.
//
// NEXT ATTEMPT TIME IS NOT SET HERE ON PURPOSE. The exponent (30 s × 2ⁿ, capped at fifteen minutes)
// and the two ceilings it runs into — FIVE PAID CALLS and ten rounds, paid or free — are a MONEY
// policy and they live in exactly one place, in the store. A second copy of them in the worker
// would be a second policy the day either one is edited.
func (w *Worker) failRun(ctx context.Context, run entity.DesignRun, token string, cause error) error {
	return w.failRunAt(ctx, run, token, cause, time.Time{})
}

// failRunAt is failRun with the next pickup named. Used ONLY where the moment is a fact rather than
// a policy — the end of an open submit's settle grace — so the back-off exponent stays the store's.
// Zero = the store's own back-off.
func (w *Worker) failRunAt(ctx context.Context, run entity.DesignRun, token string, cause error, next time.Time) error {
	v := classify(cause)
	if _, err := w.store.FailRun(ctx, entity.DesignRunFail{
		RunId:       run.Id,
		ClaimToken:  token,
		ErrorCode:   v.Code,
		LastError:   cause.Error(),
		Retryable:   v.Retryable,
		NextAttempt: next,
	}); err != nil {
		return w.abandon(ctx, run, err)
	}
	slog.Default().WarnContext(ctx, "design run failed",
		slog.Int("run_id", run.Id), slog.String("kind", run.Kind),
		slog.String("code", v.Code), slog.Bool("retryable", v.Retryable),
		slog.String("err", cause.Error()))
	return nil
}

// advanceCandidate re-queues a run for the NEXT candidate of its route, now (B-13): retryable
// whatever the failure's own verdict — the failure was the candidate's, and it moved no money — with
// the next pickup named as this instant, because the wait of a back-off is for weather, and the next
// candidate has had none. The code and the sentence are the failed candidate's (the history row says
// what happened to it); the log line names both ends of the step.
//
// The store's two ceilings still apply unchanged: a fallback is an attempt like any other.
func (w *Worker) advanceCandidate(ctx context.Context, run entity.DesignRun, token string, cause error, from, to string) error {
	v := classify(cause)
	if _, err := w.store.FailRun(ctx, entity.DesignRunFail{
		RunId:       run.Id,
		ClaimToken:  token,
		ErrorCode:   v.Code,
		LastError:   cause.Error(),
		Retryable:   true,
		NextAttempt: w.clock(),
	}); err != nil {
		return w.abandon(ctx, run, err)
	}
	slog.Default().InfoContext(ctx, "designgen: candidate advanced",
		slog.Int("run_id", run.Id), slog.String("from", from), slog.String("to", to),
		slog.String("code", v.Code))
	return nil
}

// designResultRefused — отверг ли СТОР саму выдачу (в отличие от «строка уже не наша»).
//
// Обе половины детерминированы и обе куплены: род кадра, который не может нести колорвей задания
// (0356, наш собственный сторож), и старая четвёрка InvalidArgument в CompleteRun — выход без
// медиа, отрицательный ординал, два выхода с одним ординалом, неизвестный ghost_view. Ни одна из
// них не изменит ответа от повтора, поэтому повтор покупает ровно ещё один платный вызов.
//
// ⚠ ГРАНИЦА НАРОЧНО НЕ ШИРЕ. ErrDesignClaimLost, ErrDesignRunTerminal и всё, что говорит «строка
// уже не наша», обязаны и дальше уходить в abandon: провалить их значило бы затереть результат
// того, кто перехватил задание, — ровно то, ради чего в WHERE стоит токен.
func designResultRefused(err error) bool {
	return errors.Is(err, entity.ErrDesignColorwayForbidden) ||
		errors.Is(err, entity.ErrDesignInvalidArgument)
}

// abandon turns "this row is no longer ours" into a normal, quiet outcome.
//
// ⚠ A LOST CLAIM IS NOT AN INCIDENT. The token stands in the WHERE clause of every closing write,
// so a worker whose lease expired is REFUSED rather than allowed to overwrite the result of the
// worker that took the job over — which is the entire reason the token is there. The same goes for
// a run somebody cancelled or that is already closed. Neither is worth an error that backs off the
// whole tick.
func (w *Worker) abandon(ctx context.Context, run entity.DesignRun, err error) error {
	switch {
	case errors.Is(err, entity.ErrDesignClaimLost):
		slog.Default().InfoContext(ctx, "design run changed hands; leaving its result to whoever holds it",
			slog.Int("run_id", run.Id))
		return nil
	case errors.Is(err, entity.ErrDesignRunTerminal):
		slog.Default().InfoContext(ctx, "design run is already closed",
			slog.Int("run_id", run.Id))
		return nil
	default:
		return fmt.Errorf("design run %d: %w", run.Id, err)
	}
}

// sweep drops every minted file. Best-effort by contract: the caller's own failure is the one a
// person has to see.
func (w *Worker) sweep(ctx context.Context, minted []MintedMedia) {
	for _, m := range minted {
		w.sink.Drop(ctx, m)
	}
}

// submitSettleGrace — how long after an attempt was OPENED its pass could still be inside the paid
// call or the write that closes it. A cooperative pass is bounded by RunTimeout (its context) plus
// settleTimeout (the accepted write runs beyond cancellation); the claim lease is longer than
// RunTimeout by construction (applyDefaults keeps BatchSize × RunTimeout ≤ ¾ × ClaimLease), so
// lease + settle covers it with room for a short stall. A crashed worker's run therefore waits at
// most one extra round before its open attempt is closed `unknown`.
func (w *Worker) submitSettleGrace() time.Duration {
	lease := 20 * time.Minute
	if w.c != nil && w.c.ClaimLease > 0 {
		lease = w.c.ClaimLease
	}
	return lease + settleTimeout
}

// clock is the worker's «now»; a field so a probe can stand at a chosen moment.
func (w *Worker) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// unresolvedSubmit finds an attempt that was opened and never closed: `dispatching`, no finished_at.
// Read only when no accepted id exists — after an accepted submit, an open row is a collect that died
// (free, resumed by the id), never a purchase.
func unresolvedSubmit(attempts []entity.DesignRunAttempt) (entity.DesignRunAttempt, bool) {
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.State == entity.DesignAttemptDispatching && !a.FinishedAt.Valid {
			return a, true
		}
	}
	return entity.DesignRunAttempt{}, false
}

// engineOffAtSubmit — the frozen engine is a flagged row (Gemini / Seedream) that this deployment's
// table no longer lists: its flag went off between the door and the pickup (G-03, Codex 6). The
// flag is the owner's spend switch, so a queued run is refused here, before RecordRunPrompt and
// StartAttempt — free and terminal — rather than paid for on a row nobody may start any more. A GPT
// row, or a slug the catalogue does not know, is not this function's business (the door froze it and
// applyImageOptions sends it verbatim).
func engineOffAtSubmit(job Job, table []Engine) error {
	slug := strings.TrimSpace(job.Model)
	if slug == "" {
		return nil
	}
	if _, listed := FindEngine(table, slug); listed {
		return nil
	}
	e, known := catalogueEngine(slug)
	if !known {
		return nil
	}
	flag := ""
	switch e.Slug {
	case EngineGemini3Pro:
		flag = EnvEngineGemini
	case EngineSeedream5Pro:
		flag = EnvEngineSeedream
	default:
		return nil
	}
	return fmt.Errorf("%w: %s (%s) was accepted while %s was on, and it is off now. Nothing was sent and "+
		"nothing was charged", errEngineSwitchedOff, e.Label, e.Slug, flag)
}

// acceptedAttempt finds the newest attempt that was ACCEPTED by an asynchronous provider and carries
// its task id — the id that makes the next lookup free (the store's comments still call this fact
// «acceptedRequestID», the helper it replaced in B-13). The whole attempt, because a resume needs
// three of its fields: the id, the number (it keys the ledger row the collect prices) and the provider
// name (it picks the collector — resumeRoute).
func acceptedAttempt(attempts []entity.DesignRunAttempt) (entity.DesignRunAttempt, bool) {
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.State == entity.DesignAttemptAccepted && a.ProviderRequestId.Valid &&
			a.ProviderRequestId.String != "" {
			return a, true
		}
	}
	return entity.DesignRunAttempt{}, false
}
