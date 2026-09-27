package aiprov

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// captureLog swaps the default logger for the test (not parallel-safe, so no test here is parallel).
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// ledgerAt — a ledger over the fake whose clock reads the given instants in turn (the last repeats).
func ledgerAt(st *aiprovtest.Store, tz string, instants ...time.Time) *Ledger {
	l := NewLedger(st, func() string { return tz })
	i := 0
	l.clock = func() time.Time {
		t := instants[i]
		if i < len(instants)-1 {
			i++
		}
		return t
	}
	return l
}

func ip(v int) *int { return &v }

func designStart() entity.AICallStart {
	return entity.AICallStart{
		ProviderKey: entity.AIProviderOpenRouter, Model: "openai/gpt-image-2",
		Purpose: entity.AIPurposeImageGenerate, Actor: "im", RunID: ip(7), AttemptNo: ip(2), CallNo: 2,
	}
}

// TestBeginStampsTheOrganisationsDay — 22:30Z on 27 Sep is already the 28th in Warsaw (UTC+2): the
// row is booked to the day the budget screen shows, not to the server's.
// MUTATION: DayLocal = s.OccurredAt.Format("2006-01-02") (the UTC day) → red on the day.
func TestBeginStampsTheOrganisationsDay(t *testing.T) {
	st := &aiprovtest.Store{}
	at := time.Date(2026, 9, 27, 22, 30, 0, 0, time.UTC)
	l := ledgerAt(st, "Europe/Warsaw", at)

	h := l.Begin(context.Background(), designStart())
	require.NotNil(t, h)
	require.Positive(t, h.ID)

	rows := st.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, "2026-09-28", rows[0].Start.DayLocal)
	require.True(t, rows[0].Start.OccurredAt.Equal(at))
	require.Equal(t, time.UTC, rows[0].Start.OccurredAt.Location(), "occurred_at is stored in UTC")
	require.Equal(t, entity.AICallDispatching, rows[0].Status, "a row is born dispatching, before the call")
	require.Equal(t, 2, rows[0].Start.CallNo)
	require.Equal(t, 7, *rows[0].Start.RunID)
	require.Equal(t, 2, *rows[0].Start.AttemptNo)

	// A blank zone is the organisation's default, never UTC by accident.
	st2 := &aiprovtest.Store{}
	ledgerAt(st2, "  ", at).Begin(context.Background(), designStart())
	require.Equal(t, "2026-09-28", st2.Rows()[0].Start.DayLocal)

	// What the caller stated is kept.
	st3 := &aiprovtest.Store{}
	s := designStart()
	s.OccurredAt, s.DayLocal = at.Add(-48*time.Hour), "2026-09-25"
	ledgerAt(st3, "Europe/Warsaw", at).Begin(context.Background(), s)
	require.Equal(t, "2026-09-25", st3.Rows()[0].Start.DayLocal)
	require.True(t, st3.Rows()[0].Start.OccurredAt.Equal(at.Add(-48*time.Hour)))
}

// TestBeginNeverDropsAMoneyRowForWantOfAName — a blank actor is booked to "unknown" and warned.
// MUTATION: drop the blank-actor branch → the row carries "" (the report cannot tell it from unset).
func TestBeginNeverDropsAMoneyRowForWantOfAName(t *testing.T) {
	logs := captureLog(t)
	st := &aiprovtest.Store{}
	s := designStart()
	s.Actor = " "
	ledgerAt(st, "UTC", time.Now()).Begin(context.Background(), s)
	require.Equal(t, ActorUnknown, st.Rows()[0].Start.Actor)
	require.Contains(t, logs.String(), "carries no actor")
}

// TestABeginFailureLosesTheRowNotTheCall — the store refuses the INSERT: Begin hands back a handle
// with ID 0 (the caller proceeds with the paid call) and logs ERROR with every field of the row.
// Finish on that handle writes nothing and logs the outcome at ERROR — its only record.
// MUTATION: return nil from Begin on a store error → the handle is nil and Finish logs nothing (red).
func TestABeginFailureLosesTheRowNotTheCall(t *testing.T) {
	logs := captureLog(t)
	st := &aiprovtest.Store{BeginErr: errors.New("db down")}
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	l := ledgerAt(st, "Europe/Warsaw", at, at.Add(1500*time.Millisecond))

	h := l.Begin(context.Background(), designStart())
	require.NotNil(t, h, "the call proceeds: a handle, not a refusal")
	require.Zero(t, h.ID)
	out := logs.String()
	require.Contains(t, out, "level=ERROR")
	require.Contains(t, out, "its row is LOST")
	for _, field := range []string{"run_id=7", "attempt_no=2", "call_no=2", "provider_key=openrouter",
		"model=openai/gpt-image-2", "purpose=image.generate", "actor=im", "day_local=2026-09-27", "err=\"db down\""} {
		require.Contains(t, out, field)
	}

	logs.Reset()
	l.Finish(context.Background(), h, entity.AICallEnd{
		Status: entity.AICallOK, CostUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.053"), Valid: true},
		CostSource: entity.AICostProvider, RequestID: "gen-1",
	})
	for _, w := range st.Writes() {
		require.NotEqual(t, "finish", w.Verb, "no row, no UPDATE")
	}
	out = logs.String()
	require.Contains(t, out, "level=ERROR")
	require.Contains(t, out, "cost_usd=0.053")
	require.Contains(t, out, "request_id=gen-1")
	require.Contains(t, out, "run_id=7")
}

// TestFinishRunsBeyondCancellationAndMeasuresTheCall — the pass's context is already cancelled when
// the answer arrives (the provider was slow): the outcome is still written, under a bounded deadline,
// with the latency of the call (not of our INSERT).
// MUTATION: FinishCall on ctx instead of writeContext(ctx) → the write sees context.Canceled (red).
func TestFinishRunsBeyondCancellationAndMeasuresTheCall(t *testing.T) {
	st := &aiprovtest.Store{}
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	// clock reads: OccurredAt, Started (after the INSERT), the Finish instant.
	l := ledgerAt(st, "UTC", t0, t0.Add(40*time.Millisecond), t0.Add(2040*time.Millisecond))
	h := l.Begin(context.Background(), designStart())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.Finish(ctx, h, entity.AICallEnd{Status: entity.AICallOK, ModelActual: "openai/gpt-image-2"})

	rows := st.Rows()
	require.Equal(t, entity.AICallOK, rows[0].Status)
	require.NotNil(t, rows[0].End.LatencyMs)
	require.Equal(t, 2000, *rows[0].End.LatencyMs, "latency is the call's, measured from after the INSERT")
	w := st.Writes()
	require.Equal(t, "finish", w[len(w)-1].Verb)
	require.NoError(t, w[len(w)-1].CtxErr, "the write must not inherit the pass's cancellation")
	require.True(t, w[len(w)-1].HasDL, "and it must be bounded")
	require.LessOrEqual(t, time.Until(w[len(w)-1].Deadline), ledgerWriteTimeout)
	require.NoError(t, w[0].CtxErr, "Begin is bounded and uncancelled too")
	require.True(t, w[0].HasDL)
}

// TestAFreeCallCostsZeroNotNull — a `free` outcome (nothing was sent) is booked 0 / free / not
// engaged by the ledger itself, whatever the caller forgot; a paid outcome keeps NULL when unknown.
// MUTATION: drop normaliseEnd → cost NULL, source 'none' on a free row (red).
func TestAFreeCallCostsZeroNotNull(t *testing.T) {
	st := &aiprovtest.Store{}
	l := ledgerAt(st, "UTC", time.Now())
	h := l.Begin(context.Background(), designStart())
	l.Finish(context.Background(), h, entity.AICallEnd{Status: entity.AICallFree, ErrorCode: "provider_unauthorized"})
	r := st.Rows()[0]
	require.True(t, r.End.CostUSD.Valid)
	require.True(t, r.End.CostUSD.Decimal.IsZero())
	require.Equal(t, entity.AICostFree, r.End.CostSource)
	require.NotNil(t, r.End.Engaged)
	require.False(t, *r.End.Engaged)

	s := designStart()
	s.CallNo = 3
	h = l.Begin(context.Background(), s)
	l.Finish(context.Background(), h, entity.AICallEnd{Status: entity.AICallUnknown})
	require.False(t, st.Rows()[1].End.CostUSD.Valid, "unknown stays NULL: never zero")
}

// TestAFinishFailureIsLoggedWithEveryField.
// MUTATION: ignore FinishCall's error → no ERROR line (red).
func TestAFinishFailureIsLoggedWithEveryField(t *testing.T) {
	logs := captureLog(t)
	st := &aiprovtest.Store{FinishErr: errors.New("deadlock")}
	l := ledgerAt(st, "UTC", time.Now())
	h := l.Begin(context.Background(), designStart())
	l.Finish(context.Background(), h, entity.AICallEnd{
		Status: entity.AICallChargedFailed, CostUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.04"), Valid: true},
		CostSource: entity.AICostProvider, HTTPStatus: ip(200),
	})
	out := logs.String()
	require.Contains(t, out, "level=ERROR")
	for _, field := range []string{"ledger_id=1", "status=charged_failed", "cost_usd=0.04", "http_status=200",
		"run_id=7", "err=deadlock"} {
		require.Contains(t, out, field)
	}
}

// TestPriceAcceptedPricesTheSubmitsRowOnce — the collect of an async call prices the SUBMIT's row
// (run, attempt, call) beyond cancellation; a second collect (de-duplicated) changes nothing.
// MUTATION: PriceAccepted passing the collect's attempt number → the accepted row is never found.
func TestPriceAcceptedPricesTheSubmitsRowOnce(t *testing.T) {
	st := &aiprovtest.Store{}
	l := ledgerAt(st, "UTC", time.Now())
	h := l.Begin(context.Background(), entity.AICallStart{ProviderKey: entity.AIProviderFal, Model: "meshy/v7",
		Purpose: entity.AIPurposeThreed, Actor: "im", RunID: ip(9), AttemptNo: ip(1), CallNo: 1})
	l.Finish(context.Background(), h, entity.AICallEnd{Status: entity.AICallAccepted, RequestID: "meshy/v7#req-1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	units := decimal.NewFromInt(1)
	l.PriceAccepted(ctx, 9, 1, 1, entity.AICallEnd{Status: entity.AICallOK, Units: &units, Unit: "unit",
		CostUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("1.2"), Valid: true}, CostSource: entity.AICostUnits})
	l.PriceAccepted(context.Background(), 9, 1, 1, entity.AICallEnd{Status: entity.AICallOK,
		CostUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("9.99"), Valid: true}, CostSource: entity.AICostUnits})

	r := st.Rows()[0]
	require.Equal(t, entity.AICallOK, r.Status)
	require.Equal(t, "1.2", r.End.CostUSD.Decimal.String(), "the repeated collect changed nothing")
	require.Equal(t, "meshy/v7#req-1", r.End.RequestID, "the submit's request id is kept")
	w := st.Writes()
	require.Equal(t, "price", w[2].Verb)
	require.NoError(t, w[2].CtxErr)
}

// TestSweepCutsAtTheAge — Sweep asks the store for rows older than now − age.
// MUTATION: SweepDispatching(ctx, l.now()) (no age) → the fresh row is swept too (red).
func TestSweepCutsAtTheAge(t *testing.T) {
	st := &aiprovtest.Store{}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	old := st.Seed(aiprovtest.Row{Start: entity.AICallStart{OccurredAt: now.Add(-16 * time.Minute)}, Status: entity.AICallDispatching})
	fresh := st.Seed(aiprovtest.Row{Start: entity.AICallStart{OccurredAt: now.Add(-14 * time.Minute)}, Status: entity.AICallDispatching})
	l := ledgerAt(st, "UTC", now)

	n, err := l.Sweep(context.Background(), 15*time.Minute)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	for _, r := range st.Rows() {
		switch r.ID {
		case old:
			require.Equal(t, entity.AICallUnknown, r.Status)
			require.Equal(t, entity.AICallErrorSweeper, r.End.ErrorCode)
		case fresh:
			require.Equal(t, entity.AICallDispatching, r.Status)
		}
	}
}

// TestANilLedgerIsANoOp — a deployment without the ledger keeps generating.
func TestANilLedgerIsANoOp(t *testing.T) {
	var l *Ledger
	h := l.Begin(context.Background(), designStart())
	require.Nil(t, h)
	l.Finish(context.Background(), h, entity.AICallEnd{Status: entity.AICallOK})
	l.PriceAccepted(context.Background(), 1, 1, 1, entity.AICallEnd{Status: entity.AICallOK})
	n, err := l.Sweep(context.Background(), time.Minute)
	require.NoError(t, err)
	require.Zero(t, n)

	l2 := NewLedger(nil, nil)
	require.Nil(t, l2.Begin(context.Background(), designStart()))
}
