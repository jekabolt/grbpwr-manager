package designgen

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
)

// ═══ THE AI LEDGER, SEEN FROM THE DESIGN WORKER (B-07, 02-PLAN rev.1 A1) ═══
//
// ONE ROW PER PHYSICAL CALL, OPENED BEFORE THE CALL. An attempt is not a call: a per_view flat of
// three views is ONE attempt and THREE paid requests, a recolour of four photographs is four. The
// attempt row keeps being the money truth of the run (recordAttempt → FinishAttempt, price_actual —
// untouched by anything here); the ledger is the per-call view the spend report sums, keyed
// (run_id, attempt_no, call_no).
//
// WHO WRITES WHAT. The worker hands every Job a CallRecorder scoped to the attempt it opened
// (dispatch.go, after StartAttempt and before Execute); the route calls Begin right before each
// transport invocation and Finish right after it; an asynchronous route's collect prices the
// SUBMIT's row with PriceAccepted. A Job without a recorder (every test that predates the ledger, a
// deployment without it) records nothing and behaves exactly as before.
//
// ⚠ NOTHING HERE MAY CHANGE WHAT A RUN DOES. Every method is fire-and-forget: the ledger swallows
// its own failures (aiprov.Ledger), and no route reads anything back from it.

// CallRecorder writes the ledger rows of ONE attempt's physical calls.
type CallRecorder interface {
	// Begin opens the row of one call — provider is the BILLING transport (the key whose account
	// pays: recraft through OpenRouter is "openrouter"), model the requested slug, callNo 1..N
	// inside the attempt — and must be called BEFORE the request leaves.
	Begin(ctx context.Context, provider, model string, callNo int, fallbackFrom string) *aiprov.CallHandle
	// Finish closes that row with the call's outcome.
	Finish(ctx context.Context, h *aiprov.CallHandle, end entity.AICallEnd)
	// PriceAccepted finalises the `accepted` row of call callNo of THIS recorder's attempt — the
	// asynchronous submit — when its collect delivers or finally fails.
	PriceAccepted(ctx context.Context, callNo int, end entity.AICallEnd)
}

// callLedger is what the worker needs of *aiprov.Ledger (an interface so the seam stays testable).
type callLedger interface {
	Begin(ctx context.Context, s entity.AICallStart) *aiprov.CallHandle
	Finish(ctx context.Context, h *aiprov.CallHandle, e entity.AICallEnd)
	PriceAccepted(ctx context.Context, runID, attemptNo, callNo int, e entity.AICallEnd)
	Sweep(ctx context.Context, olderThan time.Duration) (int64, error)
}

// ledgerSweeper is the one verb the reserve sweeper needs of the ledger.
type ledgerSweeper interface {
	Sweep(ctx context.Context, olderThan time.Duration) (int64, error)
}

// ledgerSweepAge — a row still `dispatching` this long after it was opened belongs to a process
// that died between Begin and Finish (02-PLAN A1: fifteen minutes).
const ledgerSweepAge = 15 * time.Minute

// ledgerSweepEvery — how often the worker's tick sweeps the ledger. The worker ticks every few
// seconds; the sweep looks for rows a quarter of an hour old, so once a minute (the reserve
// sweeper's own interval) finds them as fast as they can exist.
const ledgerSweepEvery = sweeperInterval

// ledgerFinishSlack — how long past its pass a live call's Finish can still land. A call cannot
// outlive its pass (RunTimeout bounds the provider's context) and its Finish then waits at most the
// ledger's write bound (aiprov.LedgerWriteTimeout, 5 s) — so this MUST STAY ≥ THAT BOUND, or a sweep
// at RunTimeout + slack could call `unknown` a row whose real outcome is still on its way (Finish
// moves only `dispatching` rows, and the price would be dropped). 30 s is the bound plus room.
const ledgerFinishSlack = 30 * time.Second

// Option configures New and NewSweeper.
type Option func(*options)

type options struct {
	ledger     callLedger
	runTimeout time.Duration
}

// WithLedger books every physical provider call of the worker into the AI ledger, and has the
// running ticker (the worker's, or the reserve sweeper's when generation is off) sweep rows left
// `dispatching`. A nil ledger is no ledger.
func WithLedger(l *aiprov.Ledger) Option {
	return func(o *options) {
		// ⚠ A TYPED NIL MUST NOT BECOME A NON-NIL INTERFACE: the worker tests `w.ledger == nil`.
		if l != nil {
			o.ledger = l
		}
	}
}

// WithRunTimeout hands the RESERVE SWEEPER the RunTimeout an enabled worker would run with, so its
// ledger sweep cuts where the worker's does (ledgerSweepAgeFor; Codex A4 #4). Pass the NORMALISED
// value — designgen.Normalize, the same designCfg app.go builds the worker from; zero/unset is
// normalised here exactly as applyDefaults normalises it (DefaultConfig().RunTimeout). New ignores
// it: the worker reads its own Config.RunTimeout.
func WithRunTimeout(d time.Duration) Option {
	return func(o *options) { o.runTimeout = d }
}

func applyOptions(opts []Option) options {
	var o options
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// runRecorder is a CallRecorder scoped to one (run, attempt).
type runRecorder struct {
	ledger    callLedger
	runID     int
	attemptNo int
	purpose   string
	actor     string
	adminID   *int
}

func (r runRecorder) Begin(ctx context.Context, provider, model string, callNo int, fallbackFrom string) *aiprov.CallHandle {
	runID, attemptNo := r.runID, r.attemptNo
	return r.ledger.Begin(ctx, entity.AICallStart{
		ProviderKey: provider, Model: model, Purpose: r.purpose,
		Actor: r.actor, ActorAdminID: r.adminID,
		RunID: &runID, AttemptNo: &attemptNo, CallNo: callNo, FallbackFrom: fallbackFrom,
	})
}

func (r runRecorder) Finish(ctx context.Context, h *aiprov.CallHandle, end entity.AICallEnd) {
	r.ledger.Finish(ctx, h, end)
}

func (r runRecorder) PriceAccepted(ctx context.Context, callNo int, end entity.AICallEnd) {
	r.ledger.PriceAccepted(ctx, r.runID, r.attemptNo, callNo, end)
}

// recorderFor is the recorder of one attempt of one run, or nil when the worker has no ledger.
//
// The actor is design_run.author — the JWT username of whoever pressed GENERATE, the same string
// the admin interceptor puts in aiprov.Actor. ActorAdminID stays nil here: the ledger's INSERT
// resolves it from that username at write time (store/ai insertAICall), so these rows carry the
// account's id like every other.
func (w *Worker) recorderFor(run entity.DesignRun, attemptNo int) CallRecorder {
	if w.ledger == nil || attemptNo < 1 {
		return nil
	}
	actor := run.Author
	if actor == "" {
		actor = aiprov.ActorUnknown
	}
	return runRecorder{
		ledger: w.ledger, runID: run.Id, attemptNo: attemptNo,
		purpose: entity.AIPurposeOfRunKind(run.Kind), actor: actor,
	}
}

// beginCall / finishCall / priceAccepted — the nil-safe halves a route calls. A Job without a
// recorder records nothing.
func (j *Job) beginCall(ctx context.Context, provider, model string, callNo int) *aiprov.CallHandle {
	if j == nil || j.Recorder == nil {
		return nil
	}
	return j.Recorder.Begin(ctx, provider, model, callNo, "")
}

func (j *Job) finishCall(ctx context.Context, h *aiprov.CallHandle, end entity.AICallEnd) {
	if j == nil || j.Recorder == nil {
		return
	}
	j.Recorder.Finish(ctx, h, end)
}

func (j *Job) priceAccepted(ctx context.Context, callNo int, end entity.AICallEnd) {
	if j == nil || j.Recorder == nil {
		return
	}
	j.Recorder.PriceAccepted(ctx, callNo, end)
}

// sweepLedger is the ledger half of a tick: rows left `dispatching` past age become `unknown`.
// Its failure is logged and nothing else — a sweep is bookkeeping, never a reason to back a
// generation tick off.
func sweepLedger(ctx context.Context, l ledgerSweeper, age time.Duration, who string) {
	if l == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, queueTimeout)
	defer cancel()
	n, err := l.Sweep(sctx, age)
	if err != nil {
		slog.Default().ErrorContext(ctx, "ai ledger sweep failed; rows left dispatching stay so until the next tick",
			slog.String("worker", who), slog.String("err", err.Error()))
		return
	}
	if n > 0 {
		slog.Default().WarnContext(ctx, "ai ledger: calls whose process died before their outcome was written "+
			"are now `unknown` — money may have moved for them",
			slog.String("worker", who), slog.Int64("rows", n), slog.Duration("older_than", age))
	}
}

// ledgerSweepAgeFor — THE cut-off of the ledger sweep, for BOTH tickers (the worker's and the reserve
// sweeper's; Codex A4 #4): fifteen minutes OR the longest a live pass can hold a row open, whichever
// is later. A call cannot outlive its pass (RunTimeout bounds its context) by more than its Finish;
// sweeping a row sooner than that could turn a live call `unknown`, after which its Finish — which
// only moves `dispatching` rows — would silently drop the real price. One function, because two
// tickers with two cut-offs is how an enabled→disabled rolling deploy swept the old instance's live
// rows from the new one. With the defaults (RunTimeout 15 min) this is 15 min 30 s.
func ledgerSweepAgeFor(runTimeout time.Duration) time.Duration {
	if age := runTimeout + ledgerFinishSlack; age > ledgerSweepAge {
		return age
	}
	return ledgerSweepAge
}

// normalisedRunTimeout — an unset RunTimeout as applyDefaults reads it: the default's. A set one is
// taken as given (the caller normalised it, and only ever downwards — a larger value only sweeps later).
func normalisedRunTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultConfig().RunTimeout
	}
	return d
}

// workerLedgerSweepAge — the worker's cut-off: ledgerSweepAgeFor its own RunTimeout.
func (w *Worker) workerLedgerSweepAge() time.Duration {
	var runTimeout time.Duration
	if w.c != nil {
		runTimeout = w.c.RunTimeout
	}
	return ledgerSweepAgeFor(runTimeout)
}

// maybeSweepLedger runs the ledger sweep on the worker's tick at most once per ledgerSweepEvery.
// The reserve sweeper — the other ticker — does not run while generation is on (app.go builds one
// or the other), so this is where the sweep lives when the worker is the one running.
func (w *Worker) maybeSweepLedger(ctx context.Context) {
	if w.ledger == nil {
		return
	}
	now := w.clock()
	if !w.ledgerSweptAt.IsZero() && now.Sub(w.ledgerSweptAt) < ledgerSweepEvery {
		return
	}
	w.ledgerSweptAt = now
	sweepLedger(ctx, w.ledger, w.workerLedgerSweepAge(), workerName)
}

// ─────────────────────────── outcomes: transport → ledger row ───────────────────────────
//
// ⚠ ONE FUNCTION PER TRANSPORT, BECAUSE «WAS THE REQUEST WRITTEN, AND DID MONEY MOVE» IS EACH
// TRANSPORT'S OWN FACT — and since B-14 each transport STATES it: every failed call of orimages, recraft
// direct, fal and Meshy carries an aiprov.CallError whose Engaged is read here, never the sentinel. Not
// engaged (a refusal before the wire, any non-2xx) is `free`; engaged (a post-write break, a 2xx that
// did not become an answer) is `unknown` — or what the transport's own money rule says beside it (a
// charge riding the error, fal's unconfirmed submit). An error that carries NO CallError is one no
// transport spoke for: where a route's own pre-wire refusals are bare sentinels (recraft's adapter,
// fal's and Meshy's local checks) they stay `free`; anything else is `unknown` — over-reporting
// possible spend costs a line in the report, under-reporting it costs the owner's trust in it.

// engaged / notEngaged — the two known answers to «was the request written»; nil is «nobody knows».
func engaged() *bool    { t := true; return &t }
func notEngaged() *bool { f := false; return &f }

func usdOf(v float64) decimal.NullDecimal {
	if v <= 0 {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: decimal.NewFromFloat(v), Valid: true}
}

// httpStatusOf is the provider's HTTP status carried by a failed call — CallError.HTTPStatus, the
// transport's own field — or nil.
//
// ⚠ THE TEXT IS NOT READ ANY MORE, AND THAT IS THE FIX (Codex FIX P3, B-14). Until the transports
// carried the status as a field, it was parsed out of the sentence OUR clients format ("(HTTP 402)",
// "HTTP 502:"). A sentence is not a contract: a local refusal whose words happened to quote a 408, or a
// provider message echoing one, would have turned a free row `unknown` through timeoutIsNotFree. The
// field is set only from a response that actually arrived.
func httpStatusOf(err error) *int {
	if ce, ok := aiprov.AsCallError(err); ok && ce.HTTPStatus != 0 {
		s := ce.HTTPStatus
		return &s
	}
	return nil
}

func isAnyOf(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}

// timeoutIsNotFree — ONE RULE OVER EVERY TRANSPORT'S OWN MAPPING (Codex A4 #3): an HTTP 408 is not
// proof that nothing was bought.
//
// ⚠ SINCE B-13/A3 IT IS A NET, NOT THE RULE. The four design transports now raise a 408 on their paid
// POST as ENGAGED themselves (orimages, recraft direct; fal and Meshy as an unconfirmed submit), so the
// mappings book it `unknown` by the transport's word — and, which is the half this rule could never
// reach, the worker stops retrying it: the row used to say «money may have moved» while the next pass
// bought again. What still arrives here as a not-engaged 408 is a 408 no transport vouched for — a
// lookup never reaches these mappings, so today nothing does; the net stays for the next transport.
//
// The original reading, still true of such a 408: a non-2xx CallError is NOT engaged (D-09) and the
// mappings below read it as `free`, as they read the refusal sentinel the clients fold it into
// (orimages ErrProviderFailure, recraft direct / fal / Meshy ErrBadRequest). But a 408 is a server or
// a gateway giving up on a request whose body it may already have taken: the generation may have run
// and been billed, and on fal or Meshy a task may have been queued that nobody will ever collect. No
// transport documents a 408 as unbilled, so a `free` outcome carrying one becomes `unknown` — engaged
// nobody-knows, cost NULL, source none. Over-reporting possible spend costs a line in the report;
// under-reporting it costs the owner's trust in it. Applied by imageCallEnd, vectorCallEnd,
// falSubmitEnd and meshySubmitEnd AFTER their own mapping; every other outcome passes through
// untouched. The 408 is read from CallError.HTTPStatus — a response that arrived — never from text.
func timeoutIsNotFree(end entity.AICallEnd, err error) entity.AICallEnd {
	if end.Status != entity.AICallFree {
		return end
	}
	if s := httpStatusOf(err); s == nil || *s != http.StatusRequestTimeout {
		return end
	}
	end.Status, end.Engaged = entity.AICallUnknown, nil
	end.CostUSD, end.CostSource = decimal.NullDecimal{}, entity.AICostNone
	return end
}

// withFailure stamps the machine word and the provider status of a failed call.
func withFailure(end entity.AICallEnd, err error) entity.AICallEnd {
	if err != nil {
		end.ErrorCode = classify(err).Code
		end.HTTPStatus = httpStatusOf(err)
	}
	return end
}

// imageCallEnd — one orimages.Generate (images.go). The transport's CallError decides (B-14):
//   - charged_failed: the call failed and the provider reported a cost (Result rides with the error);
//   - free: the CallError is NOT engaged, whatever its sentinel — a refusal before the wire, or any
//     non-2xx (OpenRouter documents image generation as all-or-nothing, «fails and is not billed»),
//     except a 408, which is `unknown` (timeoutIsNotFree);
//   - failed: engaged, and a 2xx came back with usage and no cost (billed-shaped, zero charge reported);
//   - unknown: engaged otherwise — a deadline or a reset AFTER the request was written, an unreadable
//     2xx — and, last resort, an error that carries no CallError at all.
func imageCallEnd(res *orimages.Result, err error) entity.AICallEnd {
	var end entity.AICallEnd
	if res != nil {
		end.ModelActual = res.Model
		if res.Usage.Prompt > 0 || res.Usage.Completion > 0 {
			p, c := res.Usage.Prompt, res.Usage.Completion
			end.PromptTokens, end.CompletionTokens = &p, &c
		}
		if cost := usdOf(res.Usage.Cost); cost.Valid {
			end.CostUSD, end.CostSource = cost, entity.AICostProvider
		}
	}
	ce, spoke := aiprov.AsCallError(err)
	switch {
	case err == nil:
		end.Status, end.Engaged = entity.AICallOK, engaged()
	case end.CostUSD.Valid:
		end.Status, end.Engaged = entity.AICallChargedFailed, engaged()
	case spoke && !ce.Engaged:
		end.Status, end.Engaged = entity.AICallFree, notEngaged()
	case spoke && res != nil:
		end.Status, end.Engaged = entity.AICallFailed, engaged()
	case spoke:
		end.Status, end.Engaged = entity.AICallUnknown, engaged()
	default:
		end.Status = entity.AICallUnknown
	}
	if !end.CostUSD.Valid && end.Status != entity.AICallFree {
		end.CostSource = entity.AICostNone
	}
	return timeoutIsNotFree(withFailure(end, err), err)
}

// recraftBillingKey — the provider whose account a vector call spends: through OpenRouter's image
// endpoint by default (02-PLAN rev.1 Opus #7), Recraft's own only on RECRAFT_ROUTE=direct.
func recraftBillingKey(route recraft.Route) string {
	if route == recraft.RouteDirect {
		return entity.AIProviderRecraft
	}
	return entity.AIProviderOpenRouter
}

// vectorPrice books a vector charge the way its transport reports it: OpenRouter says USD
// (`provider`); the direct route reports credits and the client converts them at RECRAFT_CREDIT_USD
// (`units`, unit "credit").
func vectorPrice(end *entity.AICallEnd, route recraft.Route, usd, credits float64) {
	if route == recraft.RouteDirect {
		if credits > 0 {
			u := decimal.NewFromFloat(credits)
			end.Units, end.Unit = &u, "credit"
		}
		if c := usdOf(usd); c.Valid {
			end.CostUSD, end.CostSource = c, entity.AICostUnits
		}
		return
	}
	if c := usdOf(usd); c.Valid {
		end.CostUSD, end.CostSource = c, entity.AICostProvider
	}
}

// vectorCallEnd — one recraft ImageToImage (vector.go). Both routes carry a CallError since B-14: the
// OpenRouter route keeps orimages' own through translateORError (which wraps, never flattens), the
// direct route raises recraft's. Not engaged → `free` (a 408 → `unknown`, timeoutIsNotFree); engaged →
// `unknown` — a post-write break, or a 2xx whose picture never became a usable SVG. An error with NO
// CallError is one no transport spoke for: the adapter's own pre-wire refusals (ErrNotConfigured,
// ErrBadRequest — a missing prompt, a strength the route has no dial for) stay `free`; anything else
// — a delivered answer the SVG inspector then refused (ErrNotVector, ErrUnsafeSVG) — was billed at a
// price nobody passed back: `unknown`.
func vectorCallEnd(route recraft.Route, res *recraft.VectorResult, err error) entity.AICallEnd {
	var end entity.AICallEnd
	ce, spoke := aiprov.AsCallError(err)
	switch {
	case err == nil:
		end.Status, end.Engaged = entity.AICallOK, engaged()
		if res != nil {
			end.ModelActual = res.Model
			vectorPrice(&end, route, res.CostUSD, res.Credits)
		}
	default:
		if usd, credits, ok := recraft.Charge(err); ok && (usd > 0 || credits > 0) {
			end.Status, end.Engaged = entity.AICallChargedFailed, engaged()
			vectorPrice(&end, route, usd, credits)
			var ce *recraft.ChargedError
			if errors.As(err, &ce) {
				end.ModelActual = ce.Model
			}
			break
		}
		switch {
		case spoke && !ce.Engaged:
			end.Status, end.Engaged = entity.AICallFree, notEngaged()
		case spoke:
			end.Status, end.Engaged = entity.AICallUnknown, engaged()
		case isAnyOf(err, recraft.ErrNotConfigured, recraft.ErrBadRequest):
			end.Status, end.Engaged = entity.AICallFree, notEngaged()
		default:
			end.Status = entity.AICallUnknown
		}
	}
	if !end.CostUSD.Valid && end.Status != entity.AICallFree {
		end.CostSource = entity.AICostNone
	}
	return timeoutIsNotFree(withFailure(end, err), err)
}

// acceptedEnd — an asynchronous submit the provider accepted: the row waits, unpriced, for the
// collect that delivers (PriceAccepted). The request id is the one the attempt row stores.
func acceptedEnd(requestID string) entity.AICallEnd {
	return entity.AICallEnd{Status: entity.AICallAccepted, RequestID: requestID, Engaged: engaged()}
}

// unitsOf — a fal / Meshy unit count as the ledger stores it, or nil when there is none.
func unitsOf(v float64) *decimal.Decimal {
	if v <= 0 {
		return nil
	}
	u := decimal.NewFromFloat(v)
	return &u
}

// reportedUnits — fal's billable units as the ledger's `units` column holds them: the provider's
// number, or nil when the header was absent and the one unit is only this client's assumption.
func reportedUnits(units float64, assumed bool) *decimal.Decimal {
	if assumed {
		return nil
	}
	return unitsOf(units)
}

// chargedUnits — the billable units a failed fal call carries (fal.ChargedError), or nil. An
// ASSUMED unit is not a reported one and is not written as one.
func chargedUnits(err error) *decimal.Decimal {
	var ce *fal.ChargedError
	if !errors.As(err, &ce) {
		return nil
	}
	return reportedUnits(ce.Units, ce.Assumed)
}

// falSubmitEnd — a failed fal submit (3D, cutout, outpaint, fill). fal's transport decides (B-14): a
// charge riding the error → `charged_failed`, priced with the SAME number the attempt books (booked;
// invalid when the route books none); an ENGAGED CallError → `unknown` — the whole request left and no
// usable answer came back (ErrSubmitUnconfirmed: a post-write break, a 5xx other than a bare 503, a 2xx
// without an id), fal may have queued and billed it; a CallError NOT engaged → `free` — explicitly
// refused (4xx, a bare 503) or never completely written — except a 408, which is `unknown`
// (timeoutIsNotFree). No CallError at all is the client's own pre-wire refusal (no key, no front view,
// an unfetchable reference, an option this route cannot send) → `free`; an ErrSubmitUnconfirmed that
// somehow arrived without one still reads `unknown`.
func falSubmitEnd(err error, booked decimal.NullDecimal) entity.AICallEnd {
	var end entity.AICallEnd
	if _, ok := fal.Charge(err); ok {
		end.Status, end.Engaged = entity.AICallChargedFailed, engaged()
		end.Units, end.Unit = chargedUnits(err), "unit"
		end.ModelActual = fal.ChargedModel(err)
		if booked.Valid {
			end.CostUSD, end.CostSource = booked, entity.AICostUnits
		} else {
			end.CostSource = entity.AICostNone
		}
		return withFailure(end, err)
	}
	ce, spoke := aiprov.AsCallError(err)
	if (spoke && ce.Engaged) || (!spoke && errors.Is(err, fal.ErrSubmitUnconfirmed)) {
		end.Status, end.Engaged, end.CostSource = entity.AICallUnknown, engaged(), entity.AICostNone
		return withFailure(end, err)
	}
	end.Status, end.Engaged = entity.AICallFree, notEngaged()
	return timeoutIsNotFree(withFailure(end, err), err)
}

// meshySubmitEnd — a failed direct-Meshy submit. Meshy's transport decides (B-14): a charge →
// `charged_failed`; an ENGAGED CallError (a post-write break, a 2xx that named no task, a 5xx other
// than a bare 503 — meshy.ErrSubmitUnconfirmed, B-13/A1) → `unknown`; NOT engaged (any other non-2xx,
// a request never completely written) → `free`, except a 408 → `unknown`
// (timeoutIsNotFree). No CallError: the client's own pre-wire refusals (no key, the image count, the
// prompt ceiling, an option or a reference it cannot send) → `free`; anything else → `unknown`.
func meshySubmitEnd(err error) entity.AICallEnd {
	var end entity.AICallEnd
	if credits, ok := meshy.Charge(err); ok {
		end.Status, end.Engaged = entity.AICallChargedFailed, engaged()
		end.Units, end.Unit, end.CostSource = unitsOf(float64(credits)), "credit", entity.AICostNone
		return withFailure(end, err)
	}
	ce, spoke := aiprov.AsCallError(err)
	switch {
	case spoke && !ce.Engaged,
		!spoke && isAnyOf(err, meshy.ErrNotConfigured, meshy.ErrImageCount, meshy.ErrPromptTooLong,
			meshy.ErrBadRequest, meshy.ErrBadImageURL):
		end.Status, end.Engaged = entity.AICallFree, notEngaged()
		return timeoutIsNotFree(withFailure(end, err), err)
	case spoke:
		end.Status, end.Engaged, end.CostSource = entity.AICallUnknown, engaged(), entity.AICostNone
		return withFailure(end, err)
	}
	end.Status, end.CostSource = entity.AICallUnknown, entity.AICostNone
	return withFailure(end, err)
}

// collectEnd is what a collect writes onto the submit's `accepted` row, and whether it writes at
// all. out is the Outcome the collect hands the attempt (its Price is the booked charge — the ledger
// books the SAME number, so the two can never disagree); units is the provider's unit count when it
// named one (nil when it did not, or when the unit was only assumed).
//
//   - delivered (err == nil)             → ok, priced (NULL/none when nothing was booked);
//   - failed WITH a charge               → charged_failed, priced;
//   - still running / a transient lookup → nothing: the row stays `accepted`, the next collect decides;
//   - failed for good, no charge         → `unknown`, with ONE exception: Meshy's FAILED task, which
//     Meshy refunds (`failed`, a KNOWN zero — see below).
//
// ⚠ A REFUND IS A PRICE, AND IT IS WRITTEN AS ONE (REVIEW-FIXD P2 #2). The spend report counts every
// non-free row whose cost is NULL as an unknown liability — «money possibly gone» — so a refunded task
// booked with a NULL cost would read as possible spend for ever. The refund is the provider's own
// statement (meshy.ErrTaskFailed: «Meshy refunds the credits of a failed task»), so the row carries
// cost 0, source `provider`. It is the ONLY outcome treated as refunded: fal documents no refund for a
// job it ended (fal.ErrTaskFailed stays `unknown`), and a known zero is never inferred from silence.
//
// ⚠ NOT classify()'s attempt state, and the difference is money. The submit was ACCEPTED: the provider
// took the job and bills it when it finishes, whether or not we ever look. A terminal lookup failure
// on OUR side — a key rejected on the status GET, a request id the queue no longer knows, an
// unreadable result — says nothing about whether the job was billed; classify calls several of those
// `failed` for the ATTEMPT (it made no payment), but for the CALL they are `unknown`. Only the
// provider's own «this task failed, the credits are back» is a failure that cost nothing.
func collectEnd(out *Outcome, err error, units *decimal.Decimal, unit string, charged bool) (entity.AICallEnd, bool) {
	end := entity.AICallEnd{Engaged: engaged()}
	if units != nil {
		end.Units, end.Unit = units, unit
	}
	if out != nil {
		end.ModelActual = out.Model
		if out.Price.Valid {
			end.CostUSD, end.CostSource = out.Price, entity.AICostUnits
		}
	}
	switch {
	case err == nil:
		end.Status = entity.AICallOK
	case charged:
		end.Status = entity.AICallChargedFailed
	case classify(err).Retryable:
		return entity.AICallEnd{}, false
	case errors.Is(err, meshy.ErrTaskFailed):
		end.Status = entity.AICallFailed
		if !end.CostUSD.Valid {
			end.CostUSD, end.CostSource = decimal.NewNullDecimal(decimal.Zero), entity.AICostProvider
		}
	default:
		end.Status = entity.AICallUnknown
	}
	if !end.CostUSD.Valid {
		end.CostSource = entity.AICostNone
	}
	return withFailure(end, err), true
}

// recordCollect prices the submit's row (call 1 of the recorder's attempt) from a collect's result.
func (j *Job) recordCollect(ctx context.Context, out *Outcome, err error, units *decimal.Decimal, unit string, charged bool) {
	if j == nil || j.Recorder == nil {
		return
	}
	if end, write := collectEnd(out, err, units, unit, charged); write {
		j.Recorder.PriceAccepted(ctx, 1, end)
	}
}
