package designgen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// ─── PRE-FLIGHT. Everything here happens BEFORE an attempt row exists, therefore before any
	// money can move. Each of these refusals is permanent by nature: no number of retries wires a
	// route, hands over an API key or teaches the bucket a new file type.
	//
	// IT IS THE SAME CALL THE HANDLER ALREADY MADE AT THE DOOR (PreflightKind), and it is repeated
	// here rather than trusted: the door answered when the run was created, this answers when it is
	// executed, and between the two lie a redeploy, a rotated key and a changed configuration. One
	// expression, asked twice — never two expressions.
	prov, err := w.providers.preflight(w.sink, run.Kind)
	if err != nil {
		return w.failRun(ctx, run, token, err)
	}

	job, err := buildJob(ctx, w.media, run, w.c.QualityFor(run.Kind))
	if err != nil {
		// A database hiccup while resolving input media. Retryable, and nothing has been spent.
		return w.failRun(ctx, run, token, err)
	}

	collector, async := prov.(Collector)

	// ─── RESUME. An asynchronous route may already have been paid: an attempt closed as
	// `accepted` carries the provider's task id, and looking that task up is FREE. Reading it
	// before submitting is the difference between resuming a job after a crash and buying it
	// twice.
	pendingID := ""
	if async {
		if full, gerr := w.store.GetRun(ctx, run.Id); gerr != nil {
			slog.Default().WarnContext(ctx, "could not read the attempts of a design run; treating it as fresh",
				slog.Int("run_id", run.Id), slog.String("err", gerr.Error()))
		} else if full != nil {
			pendingID = acceptedRequestID(full.Attempts)
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
	// молча. Здесь колонка пишется ИЗ ТОГО ЖЕ `Job.Prompt`, который отправляют следующие строки.
	//
	// RECORD-THEN-SPEND: запись стоит до `StartAttempt`, то есть до любого движения денег; её
	// отказ останавливает проход, ничего не потратив. Токен захвата сторожит её как и всякую
	// другую запись результата.
	//
	// ЧТО В КОЛОНКЕ — БАЗОВАЯ ИНСТРУКЦИЯ, и это НЕ ПРИДИРКА: на маршруте `per_view` каждый платный
	// вызов получает сверху «view:\n<view>» (viewPrompt), а 3D режет текст по потолку текстуры
	// (textureSteer). Значит на этих двух маршрутах отправленный текст СТРОГО ДЛИННЕЕ или короче
	// хранимого, и контракт обязан говорить «базовая инструкция», а не «то, что ушло».
	if pendingID == "" {
		if err := w.store.RecordRunPrompt(ctx, run.Id, token, job.Prompt); err != nil {
			return w.abandon(ctx, run, err)
		}
	}

	// ─── THE PAID CALL. Outside every transaction, by construction: the store verbs above and
	// below are each their own short transaction, and nothing here holds one open across a
	// network call that takes tens of seconds.
	if pendingID == "" {
		att, err := w.store.StartAttempt(ctx, entity.DesignAttemptStart{
			RunId: run.Id, ClaimToken: token, Provider: prov.Name(),
		})
		if err != nil {
			// Includes ErrDesignClaimLost: somebody else holds the row, and finding that out
			// BEFORE the money is exactly why the store checks the claim here too.
			return w.abandon(ctx, run, err)
		}
		out, callErr := prov.Execute(ctx, job)

		if async && callErr == nil && out != nil && out.Pending {
			// Submitted, not delivered. Close the attempt with the task id so a worker that dies
			// during the build resumes for free, then fall through to collect in this same pass.
			//
			// BEYOND CANCELLATION, like every other write that follows a payment: this id IS the
			// resume, and losing it to an expired pass deadline means buying the model again.
			actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
			w.finishAttempt(actx, run, att.AttemptNo, out, nil, entity.DesignAttemptAccepted)
			acancel()
			pendingID = out.RequestID
		} else {
			return w.settle(ctx, run, token, att.AttemptNo, out, callErr)
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
		// Unreachable today: pendingID is only ever set on a route that implements Collector. It is
		// written down anyway because the alternative to a refusal here is a nil dereference on a
		// run that has ALREADY BEEN PAID FOR, and a panic leaves it claimed until its lease dies.
		return w.failRun(ctx, run, token,
			fmt.Errorf("%w: %s accepted task %s but cannot collect it", errRouteMissing, prov.Name(), pendingID))
	}
	att, err := w.store.StartAttempt(ctx, entity.DesignAttemptStart{
		RunId: run.Id, ClaimToken: token, Provider: prov.Name(),
	})
	if err != nil {
		return w.abandon(ctx, run, err)
	}
	out, callErr := collector.Collect(ctx, job, pendingID)
	if out != nil && out.RequestID == "" {
		out.RequestID = pendingID
	}
	return w.settle(ctx, run, token, att.AttemptNo, out, callErr)
}

// settle records the money, stores the bytes and closes the run.
//
// ORDER IS THE ARGUMENT. The charge is written FIRST, from the provider's answer alone, because it
// is already real and nothing that happens afterwards can make it less so — a bucket that refuses
// the bytes does not refund the generation. Only then are the bytes uploaded and the run closed.
func (w *Worker) settle(ctx context.Context, run entity.DesignRun, token string, attemptNo int, out *Outcome, callErr error) error {
	// The pass may be running on a context whose deadline has already passed — a long provider
	// call is exactly the case. Everything from here on is short, and losing it would lose the
	// paid result, so it runs beyond cancellation.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()

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
		return w.failRun(sctx, run, token, callErr)
	}
	if callErr != nil {
		slog.Default().WarnContext(sctx, "design run delivered fewer outputs than it asked for",
			slog.Int("run_id", run.Id), slog.Int("delivered", artifacts),
			slog.Int("requested", run.RequestedOutputs), slog.String("err", callErr.Error()))
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
// lost number into one lost generation.
func (w *Worker) finishAttempt(ctx context.Context, run entity.DesignRun, attemptNo int, out *Outcome, callErr error, state string) {
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
		slog.Default().ErrorContext(ctx, "failed to record the money of a design attempt",
			slog.Int("run_id", run.Id), slog.Int("attempt_no", attemptNo),
			slog.String("state", state), slog.String("err", err.Error()))
	}
}

// failRun writes the failure down, letting the store decide the backoff.
//
// NEXT ATTEMPT TIME IS NOT SET HERE ON PURPOSE. The exponent (30 s × 2ⁿ, capped at fifteen minutes)
// and the two ceilings it runs into — FIVE PAID CALLS and ten rounds, paid or free — are a MONEY
// policy and they live in exactly one place, in the store. A second copy of them in the worker
// would be a second policy the day either one is edited.
func (w *Worker) failRun(ctx context.Context, run entity.DesignRun, token string, cause error) error {
	v := classify(cause)
	if _, err := w.store.FailRun(ctx, entity.DesignRunFail{
		RunId:      run.Id,
		ClaimToken: token,
		ErrorCode:  v.Code,
		LastError:  cause.Error(),
		Retryable:  v.Retryable,
	}); err != nil {
		return w.abandon(ctx, run, err)
	}
	slog.Default().WarnContext(ctx, "design run failed",
		slog.Int("run_id", run.Id), slog.String("kind", run.Kind),
		slog.String("code", v.Code), slog.Bool("retryable", v.Retryable),
		slog.String("err", cause.Error()))
	return nil
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

// acceptedRequestID finds the newest attempt that was ACCEPTED by an asynchronous provider and
// carries its task id — the id that makes the next lookup free.
func acceptedRequestID(attempts []entity.DesignRunAttempt) string {
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.State == entity.DesignAttemptAccepted && a.ProviderRequestId.Valid &&
			a.ProviderRequestId.String != "" {
			return a.ProviderRequestId.String
		}
	}
	return ""
}
