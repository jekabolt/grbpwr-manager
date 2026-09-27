package aiprov

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ledgerWriteTimeout bounds every ledger write. The writes are one-row statements; a database that
// cannot take one in ten seconds is sick, and the paid call next to it must not wait longer than
// that for a bookkeeping line.
const ledgerWriteTimeout = 10 * time.Second

// Ledger writes ai_usage_event: ONE ROW PER PHYSICAL PROVIDER CALL, OPENED BEFORE THE CALL
// (02-PLAN rev.1 A1).
//
// WHY BEFORE. A row written only after the call is lost exactly when it matters: the process that
// paid dies between the answer and the write, and the money leaves no trace. Begin inserts the row
// as `dispatching` BEFORE the request leaves; Finish moves it on (ok · free · failed · charged_failed
// · accepted · unknown); a row still `dispatching` fifteen minutes later belongs to a process that
// died holding it, and Sweep calls it `unknown` — money may have moved, nobody can say how much.
//
// ⚠ THE LEDGER NEVER STOPS PRODUCTION. Every method swallows its store errors: a failed Begin logs
// ERROR with every field of the row and hands back a handle with ID 0 — the paid call proceeds, the
// row is lost, and the log line is its only record; a failed Finish logs ERROR with every field of
// the outcome. The ledger is an observer of spend, never a gate on it: refusing a generation
// because a bookkeeping INSERT failed would turn a missing line into a missing picture.
//
// ⚠ AND IT IS NOT THE MONEY TRUTH OF A DESIGN RUN. design_run_attempt.price and price_actual stay
// exactly what they were (Worker.recordAttempt → FinishAttempt); nothing here reads or writes them.
// The ledger is the per-call view the spend report sums; the attempt row is what the run is billed.
type Ledger struct {
	store dependency.AI
	// tz is design_settings.budget_timezone, cached by the caller; day_local is stamped in it.
	tz func() string
	// clock is "now"; a field so a test can stand at a chosen instant.
	clock func() time.Time
}

// NewLedger builds the writer. tz may be nil (the default zone); store may be nil, and then every
// method is a no-op — a deployment without the ledger tables must still generate.
func NewLedger(store dependency.AI, tz func() string) *Ledger {
	return &Ledger{store: store, tz: tz, clock: time.Now}
}

// CallHandle is one opened row. ID 0 means the row could not be opened (the call still happened);
// Started is the moment the physical call began, for LatencyMs; Start is what Begin wrote, kept so
// a failed Finish can log the row it could not close.
type CallHandle struct {
	ID      int64
	Started time.Time
	Start   entity.AICallStart
}

func (l *Ledger) off() bool { return l == nil || l.store == nil }

func (l *Ledger) now() time.Time {
	if l.clock != nil {
		return l.clock()
	}
	return time.Now()
}

func (l *Ledger) timezone() string {
	if l.tz != nil {
		if tz := strings.TrimSpace(l.tz()); tz != "" {
			return tz
		}
	}
	return entity.DefaultBudgetTimezone
}

// writeContext is the context every ledger write runs on: BEYOND the caller's cancellation (the
// write follows or precedes real money, and a pass whose deadline just passed must still record
// it — the settle pattern of designgen), and bounded, so a sick database costs a paid call at most
// ledgerWriteTimeout.
func writeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
}

// Begin opens the row of ONE physical call, status `dispatching`, and must be called BEFORE the
// request leaves. OccurredAt (UTC) and DayLocal (the organisation's day, entity.BudgetDayKey) are
// filled when empty; a blank Actor becomes ActorUnknown with a warning — a money row is never
// dropped for want of a name. A nil ledger returns nil (and Finish(nil) is a no-op).
func (l *Ledger) Begin(ctx context.Context, s entity.AICallStart) *CallHandle {
	if l.off() {
		return nil
	}
	if s.OccurredAt.IsZero() {
		s.OccurredAt = l.now().UTC()
	}
	if strings.TrimSpace(s.DayLocal) == "" {
		s.DayLocal = entity.BudgetDayKey(s.OccurredAt, l.timezone())
	}
	if s.CallNo == 0 {
		s.CallNo = 1
	}
	if strings.TrimSpace(s.Actor) == "" {
		slog.Default().WarnContext(ctx, "ai ledger: a paid call carries no actor; its row is booked to "+
			"'"+ActorUnknown+"' — the path that made it forgot to name who asked", startAttrs(s)...)
		s.Actor = ActorUnknown
	}
	h := &CallHandle{Start: s}
	wctx, cancel := writeContext(ctx)
	id, err := l.store.BeginCall(wctx, s)
	cancel()
	// The call starts AFTER its row exists: latency measures the provider, not our INSERT.
	h.Started = l.now()
	if err != nil {
		slog.Default().ErrorContext(ctx, "ai ledger: the row of a paid call could not be opened; the call "+
			"proceeds and its row is LOST — this line is its only record",
			append(startAttrs(s), slog.String("err", err.Error()))...)
		return h
	}
	h.ID = id
	return h
}

// Finish closes the row Begin opened. LatencyMs is measured from the handle when the caller left it
// nil; a `free` outcome is booked as cost 0 / source free (02-PLAN A1). The UPDATE runs beyond
// cancellation, bounded. A handle with ID 0 (the row was never opened) is logged with every field —
// start and outcome — at ERROR, because that line is the only place this call's money now exists.
func (l *Ledger) Finish(ctx context.Context, h *CallHandle, e entity.AICallEnd) {
	if l.off() || h == nil {
		return
	}
	if e.LatencyMs == nil && !h.Started.IsZero() {
		ms := int(l.now().Sub(h.Started) / time.Millisecond)
		if ms < 0 {
			ms = 0
		}
		e.LatencyMs = &ms
	}
	e = normaliseEnd(e)
	if h.ID == 0 {
		slog.Default().ErrorContext(ctx, "ai ledger: a paid call finished whose row was never opened; its "+
			"outcome exists only in this line",
			append(startAttrs(h.Start), endAttrs(e)...)...)
		return
	}
	wctx, cancel := writeContext(ctx)
	defer cancel()
	if err := l.store.FinishCall(wctx, h.ID, e); err != nil {
		attrs := append([]any{slog.Int64("ledger_id", h.ID)}, startAttrs(h.Start)...)
		attrs = append(attrs, endAttrs(e)...)
		slog.Default().ErrorContext(ctx, "ai ledger: the outcome of a paid call could not be written; the "+
			"row stays dispatching until the sweeper calls it unknown — this line is the outcome's record",
			append(attrs, slog.String("err", err.Error()))...)
	}
}

// PriceAccepted finalises the `accepted` row of an asynchronous submit — (runID, attemptNo, callNo)
// is the SUBMIT's attempt, not the collect's — when its collect delivers or finally fails. A row
// that is no longer `accepted` (a repeated, de-duplicated collect) is left alone by the store, so
// calling this twice is harmless. Beyond cancellation, bounded; a store error logs ERROR with every
// field.
func (l *Ledger) PriceAccepted(ctx context.Context, runID, attemptNo, callNo int, e entity.AICallEnd) {
	if l.off() {
		return
	}
	e = normaliseEnd(e)
	wctx, cancel := writeContext(ctx)
	defer cancel()
	if err := l.store.PriceAcceptedCall(wctx, runID, attemptNo, callNo, e); err != nil {
		attrs := []any{slog.Int("run_id", runID), slog.Int("attempt_no", attemptNo), slog.Int("call_no", callNo)}
		attrs = append(attrs, endAttrs(e)...)
		slog.Default().ErrorContext(ctx, "ai ledger: the price of an accepted call could not be written; the "+
			"row stays accepted and unpriced — this line is the price's record",
			append(attrs, slog.String("err", err.Error()))...)
	}
}

// Sweep turns rows still `dispatching` after olderThan into `unknown` (error_code `sweeper`) and
// reports how many: their process died between Begin and Finish, and money may have moved. It
// returns the store's error for the caller to log — a sweeper's failure is not a call's.
func (l *Ledger) Sweep(ctx context.Context, olderThan time.Duration) (int64, error) {
	if l.off() {
		return 0, nil
	}
	return l.store.SweepDispatching(ctx, l.now().Add(-olderThan))
}

// normaliseEnd applies the one rule of the vocabulary the caller must not have to remember: a
// `free` call sent nothing and owes nothing — cost 0, source free, not engaged (02-PLAN A1).
func normaliseEnd(e entity.AICallEnd) entity.AICallEnd {
	if e.Status == entity.AICallFree {
		e.CostUSD = decimal.NullDecimal{Decimal: decimal.Zero, Valid: true}
		e.CostSource = entity.AICostFree
		if e.Engaged == nil {
			f := false
			e.Engaged = &f
		}
	}
	return e
}

// ───────────────────────── log fields ─────────────────────────

func intPtrAttr(key string, v *int) slog.Attr {
	if v == nil {
		return slog.Any(key, nil)
	}
	return slog.Int(key, *v)
}

// startAttrs — every field of a row's opening, for the line that replaces a lost row.
func startAttrs(s entity.AICallStart) []any {
	return []any{
		slog.Time("occurred_at", s.OccurredAt), slog.String("day_local", s.DayLocal),
		slog.String("provider_key", s.ProviderKey), slog.String("model", s.Model),
		slog.String("purpose", s.Purpose), slog.String("actor", s.Actor),
		intPtrAttr("actor_admin_id", s.ActorAdminID), intPtrAttr("run_id", s.RunID),
		intPtrAttr("attempt_no", s.AttemptNo), slog.Int("call_no", s.CallNo),
		slog.String("fallback_from", s.FallbackFrom),
	}
}

// endAttrs — every field of a row's outcome. The cost is written as a decimal string, and as "NULL"
// when unknown: an unknown price must never read as $0 in a log either.
func endAttrs(e entity.AICallEnd) []any {
	cost := "NULL"
	if e.CostUSD.Valid {
		cost = e.CostUSD.Decimal.String()
	}
	units := "NULL"
	if e.Units != nil {
		units = e.Units.String()
	}
	engaged := slog.Any("engaged", nil)
	if e.Engaged != nil {
		engaged = slog.Bool("engaged", *e.Engaged)
	}
	return []any{
		slog.String("status", e.Status), slog.String("error_code", e.ErrorCode),
		intPtrAttr("http_status", e.HTTPStatus), engaged,
		slog.String("request_id", e.RequestID), slog.String("model_actual", e.ModelActual),
		intPtrAttr("prompt_tokens", e.PromptTokens), intPtrAttr("completion_tokens", e.CompletionTokens),
		intPtrAttr("cached_tokens", e.CachedTokens), intPtrAttr("reasoning_tokens", e.ReasoningTokens),
		slog.String("units", units), slog.String("unit", e.Unit),
		slog.String("cost_usd", cost), slog.String("cost_source", e.CostSource),
		slog.String("price_version", e.PriceVersion), intPtrAttr("latency_ms", e.LatencyMs),
	}
}
