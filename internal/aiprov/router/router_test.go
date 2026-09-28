package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Every test here runs against the real registry (its snapshot, its breakers, its Admit contract)
// over an in-memory config, and against the real aiprov.Ledger over aiprovtest.Store — the fake that
// refuses what store/ai refuses (UNIQUE (run_id, attempt_no, call_no) included). The transports are
// fakes: the router's contract with them is aiprov.Chatter and *aiprov.CallError, nothing else.

// ───────────────────────── fakes ─────────────────────────

// cfgStore is aiprovtest.Store (the ledger half, as store/ai behaves) with a live config half: the
// registry reads cfg, a test edits it the way a write RPC does (the change and a version bump).
type cfgStore struct {
	*aiprovtest.Store
	mu  sync.Mutex
	cfg entity.AIConfig
}

func (s *cfgStore) GetConfig(context.Context) (*entity.AIConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.Providers = append([]entity.AIProvider(nil), s.cfg.Providers...)
	c.Routes = nil
	for _, rt := range s.cfg.Routes {
		c.Routes = append(c.Routes, entity.AIRoute{Purpose: rt.Purpose,
			Candidates: append([]entity.AIRouteCandidate(nil), rt.Candidates...)})
	}
	return &c, nil
}

func (s *cfgStore) ConfigVersion(context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Settings.ConfigVersion, nil
}

func (s *cfgStore) edit(mut func(*entity.AIConfig)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mut(&s.cfg)
	s.cfg.Settings.ConfigVersion++
}

// clock is a settable clock (the registry's breaker window and the router's latency).
type clock struct{ ns atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ns.Store(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// seen is one call a fake transport received.
type seen struct {
	model    string
	req      aiprov.ChatRequest
	deadline time.Time
	hasDL    bool
}

// chatter is a fake transport: it records every call and answers with do.
type chatter struct {
	mu    sync.Mutex
	calls []seen
	do    func(ctx context.Context, model string, req aiprov.ChatRequest) (*aiprov.ChatResult, error)
}

func (f *chatter) Chat(ctx context.Context, model string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	dl, ok := ctx.Deadline()
	f.mu.Lock()
	f.calls = append(f.calls, seen{model: model, req: req, deadline: dl, hasDL: ok})
	do := f.do
	f.mu.Unlock()
	if do == nil {
		return &aiprov.ChatResult{Text: "ok"}, nil
	}
	return do(ctx, model, req)
}

func (f *chatter) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *chatter) models() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.model)
	}
	return out
}

func answers(text string) func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	return func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		return &aiprov.ChatResult{Text: text}, nil
	}
}

func fails(err error) func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	return func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) { return nil, err }
}

// The failures the transports return, shaped as oaichat (B-11) shapes them.
func errTransport(pk string) *aiprov.CallError {
	return &aiprov.CallError{Provider: pk, Code: "transport", Retryable: true,
		Err: fmt.Errorf("%s: request failed: dial tcp: connection refused", pk)}
}

func errStatus(pk string, status int) *aiprov.CallError {
	code, retry := "provider_error", true
	inner := fmt.Errorf("%s: API error (HTTP %d): upstream", pk, status)
	switch status {
	case 404:
		code, retry = "model_unknown", false
		inner = fmt.Errorf("%s: %w: API error (HTTP 404): no endpoints", pk, aiprov.ErrModelUnavailable)
	case 429:
		code = "rate_limited"
	case 401:
		code, retry = "key_rejected", false
	}
	return &aiprov.CallError{Provider: pk, Code: code, HTTPStatus: status, Retryable: retry, Err: inner}
}

func errEngaged(pk string) *aiprov.CallError {
	return &aiprov.CallError{Provider: pk, Code: "provider_error", HTTPStatus: 200, Engaged: true,
		Err: fmt.Errorf("%s: could not decode API response envelope: unexpected EOF", pk)}
}

func errEngagedTimeout(pk string) *aiprov.CallError {
	return &aiprov.CallError{Provider: pk, Code: aiprov.CodeTimeout, Engaged: true,
		Err: fmt.Errorf("%s: request failed: %w", pk, context.DeadlineExceeded)}
}

// ───────────────────────── the rig ─────────────────────────

const (
	slugChat     = "anthropic/claude-sonnet-5"
	slugAnalysis = "anthropic/claude-opus-5"
	slugIdeas    = "google/gemini-3.1-flash-lite"
	slugIdeasFB  = "openai/gpt-5-mini"
)

var testDefaults = Defaults{Chat: slugChat, Analysis: slugAnalysis, Ideas: slugIdeas}

type rig struct {
	t      *testing.T
	store  *cfgStore
	reg    *registry.Registry
	clk    *clock
	or     *chatter // openrouter
	oa     *chatter // openai
	ab     *chatter // apibost
	router *Router
	logs   *bytes.Buffer
}

func testRing(t *testing.T) *keyring.Ring {
	t.Helper()
	r, err := keyring.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	require.NoError(t, err)
	return r
}

// at is one route row.
func at(pos int, pk, model string) entity.AIRouteCandidate {
	return entity.AIRouteCandidate{Position: pos, ProviderKey: pk, Model: model}
}

// config: openrouter on with its env key; openai, apibost and anthropic on with sealed database keys
// (anthropic has NO transport in the rig — the "enabled before its adapter exists" provider); every
// other provider off. Routes: every purpose at position 1 on openrouter with no model (the 0373 seed),
// the playground door's position-2 row, then the test's own routes replace whole purposes.
func config(t *testing.T, ring *keyring.Ring, routes map[string][]entity.AIRouteCandidate) entity.AIConfig {
	t.Helper()
	keyed := map[string]bool{entity.AIProviderOpenAI: true, entity.AIProviderApibost: true, entity.AIProviderAnthropic: true}
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		p := entity.AIProvider{Key: k, Label: k, Enabled: k == entity.AIProviderOpenRouter || keyed[k]}
		if keyed[k] {
			blob, err := ring.Seal("sk-"+k+"-test-1234", keyring.AAD(k, string(entity.AIKeyAPI)))
			require.NoError(t, err)
			p.APIKeyEnc = blob
		}
		cfg.Providers = append(cfg.Providers, p)
	}
	for _, p := range entity.AIPurposes() {
		cands := []entity.AIRouteCandidate{at(1, entity.AIProviderOpenRouter, "")}
		if p == entity.AIPurposePlaygroundIdeas {
			cands = append(cands, at(2, entity.AIProviderOpenRouter, slugIdeasFB))
		}
		if own, ok := routes[p]; ok {
			cands = own
		}
		cfg.Routes = append(cfg.Routes, entity.AIRoute{Purpose: p, Candidates: cands})
	}
	cfg.Settings = entity.AISettings{ConfigVersion: 1,
		DefaultChatProviderKey: entity.AIProviderOpenRouter, DefaultImageProviderKey: entity.AIProviderOpenRouter}
	cfg.BudgetTimezone = "Europe/Warsaw"
	return cfg
}

func newRig(t *testing.T, routes map[string][]entity.AIRouteCandidate, defaults Defaults, budgetBase time.Duration) *rig {
	t.Helper()
	ring := testRing(t)
	st := &cfgStore{Store: &aiprovtest.Store{}, cfg: config(t, ring, routes)}
	clk := newClock()
	reg := registry.New(st, ring, registry.EnvKeys{OpenRouter: "env-openrouter-aaaa", OpenRouterImages: "env-images-bbbb"}, registry.WithClock(clk.now))
	require.NoError(t, reg.Reload(context.Background()))
	rg := &rig{t: t, store: st, reg: reg, clk: clk, or: &chatter{}, oa: &chatter{}, ab: &chatter{}}
	rg.router = New(reg, aiprov.NewLedger(st, nil), map[string]aiprov.Chatter{
		entity.AIProviderOpenRouter: rg.or,
		entity.AIProviderOpenAI:     rg.oa,
		entity.AIProviderApibost:    rg.ab,
	}, defaults, budgetBase, WithClock(clk.now))
	rg.logs = &bytes.Buffer{}
	rg.router.log = slog.New(slog.NewTextHandler(rg.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return rg
}

func (rg *rig) rows() []aiprovtest.Row { return rg.store.Rows() }

// fallbackRoute: openrouter (its default slug) then openai.
func fallbackRoute(purpose string) map[string][]entity.AIRouteCandidate {
	return map[string][]entity.AIRouteCandidate{purpose: {
		at(1, entity.AIProviderOpenRouter, ""), at(2, entity.AIProviderOpenAI, "gpt-5-mini"),
	}}
}

var chatReq = aiprov.ChatRequest{System: "sys", User: "user", MaxTokens: 1200, Effort: "none"}

func dec(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

// ───────────────────────── success ─────────────────────────

// TestChatBooksARowBeforeTheCallAndAnOKRowAfter — the row exists (dispatching, the provider and the
// slug about to be called) at the moment the transport is entered; after the answer it is `ok`,
// engaged, with the tokens, the request id, the reported slug, the provider's own price, and the
// latency of the call as the router's clock measured it.
//
// MUTATION: Begin moved after chatter.Chat → red (no row while the transport runs).
// MUTATION: okEnd drops LatencyMs (the ledger measures on its own wall clock) → red (1234 ms).
// MUTATION: price ignores res.CostUSD → red (source table, not provider).
func TestChatBooksARowBeforeTheCallAndAnOKRowAfter(t *testing.T) {
	rg := newRig(t, nil, testDefaults, 0)
	id := 5
	ctx := aiprov.WithActor(context.Background(), aiprov.Actor{Username: "im", AdminID: &id})

	rg.or.do = func(_ context.Context, model string, _ aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		rows := rg.rows()
		require.Len(t, rows, 1, "the row is opened BEFORE the physical call")
		require.Equal(t, entity.AICallDispatching, rows[0].Status)
		require.Equal(t, entity.AIProviderOpenRouter, rows[0].Start.ProviderKey)
		require.Equal(t, slugAnalysis, rows[0].Start.Model)
		rg.clk.advance(1234 * time.Millisecond)
		return &aiprov.ChatResult{Text: "done", FinishReason: "stop", Model: model + "-20260901",
			RequestID: "gen-1", CostUSD: dec("0.0042"),
			Usage: aiprov.TokenUsage{Prompt: 900, Completion: 300, Cached: 100, Reasoning: 20}}, nil
	}

	res, err := rg.router.Chat(ctx, entity.AIPurposeTechCardEnhance, chatReq)
	require.NoError(t, err)
	require.Equal(t, "done", res.Text)
	require.Equal(t, entity.AIProviderOpenRouter, res.Provider, "the router names the billing provider")
	require.Equal(t, slugAnalysis+"-20260901", res.Model)
	require.True(t, res.Engaged)
	require.Equal(t, []string{slugAnalysis}, rg.or.models(), "enhance runs on the Analysis default")
	require.Equal(t, chatReq, rg.or.calls[0].req, "the request reaches the transport untouched")

	rows := rg.rows()
	require.Len(t, rows, 1)
	r := rows[0]
	require.Equal(t, entity.AICallOK, r.Status)
	require.Equal(t, entity.AIPurposeTechCardEnhance, r.Start.Purpose)
	require.Equal(t, "im", r.Start.Actor)
	require.Equal(t, 5, *r.Start.ActorAdminID)
	require.Equal(t, 1, r.Start.CallNo)
	require.Empty(t, r.Start.FallbackFrom)
	require.Nil(t, r.Start.RunID)
	require.True(t, *r.End.Engaged)
	require.Equal(t, "gen-1", r.End.RequestID)
	require.Equal(t, slugAnalysis+"-20260901", r.End.ModelActual)
	require.Equal(t, 900, *r.End.PromptTokens)
	require.Equal(t, 300, *r.End.CompletionTokens)
	require.Equal(t, 100, *r.End.CachedTokens)
	require.Equal(t, 20, *r.End.ReasoningTokens)
	require.Equal(t, entity.AICostProvider, r.End.CostSource)
	require.True(t, r.End.CostUSD.Valid)
	require.Equal(t, "0.0042", r.End.CostUSD.Decimal.String())
	require.Empty(t, r.End.PriceVersion)
	require.Equal(t, 1234, *r.End.LatencyMs)
}

// TestChatPricesFromTheTableOrLeavesItNull — no provider price: the curated table prices the slug the
// provider REPORTED (price_version stamped); a zero usage.cost is not a price; an unpriced slug stays
// NULL / none, never $0.
//
// MUTATION: price skips pricing.Price (returns none after the provider check) → red on the table row.
// MUTATION: price accepts a Valid zero CostUSD as the provider's price → red (0 provider, not table).
// MUTATION: the last return of price is {Valid: true} (zero) → red on the unpriced row.
func TestChatPricesFromTheTableOrLeavesItNull(t *testing.T) {
	rg := newRig(t, nil, testDefaults, 0)
	usage := aiprov.TokenUsage{Prompt: 1000, Completion: 500}

	rg.or.do = func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		return &aiprov.ChatResult{Text: "a", Model: slugIdeasFB, Usage: usage,
			CostUSD: decimal.NullDecimal{Decimal: decimal.Zero, Valid: true}}, nil
	}
	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)

	rg.or.do = func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		return &aiprov.ChatResult{Text: "b", Model: slugIdeas, Usage: usage}, nil
	}
	_, err = rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)

	rows := rg.rows()
	require.Len(t, rows, 2)
	// gpt-5-mini on OpenRouter: $0.25/M in, $2/M out → (1000×0.25 + 500×2) / 1e6.
	require.Equal(t, entity.AICostTable, rows[0].End.CostSource)
	require.True(t, rows[0].End.CostUSD.Valid)
	require.True(t, decimal.RequireFromString("0.00125").Equal(rows[0].End.CostUSD.Decimal), rows[0].End.CostUSD.Decimal.String())
	require.Equal(t, pricing.Version, rows[0].End.PriceVersion)

	require.Equal(t, entity.AICallOK, rows[1].Status)
	require.Equal(t, entity.AICostNone, rows[1].End.CostSource)
	require.False(t, rows[1].End.CostUSD.Valid, "an unpriced slug is NULL, never $0")
	require.Empty(t, rows[1].End.PriceVersion)
}

// ───────────────────────── not engaged → next ─────────────────────────

// TestChatFallsBackPastAFailureThatNeverLeft — a transport error before the write: the first row is
// `free` ($0, not engaged, its error code), the next candidate answers, and its row says it is the
// fallback from the first provider, call 2.
//
// MUTATION: the not-engaged branch returns (res, err) instead of continuing → red.
// MUTATION: FallbackFrom: "" on every row → red.
// MUTATION: failEnd books a not-engaged failure `unknown` → red.
func TestChatFallsBackPastAFailureThatNeverLeft(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = fails(errTransport(entity.AIProviderOpenRouter))
	rg.oa.do = answers("from openai")

	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, "from openai", res.Text)
	require.Equal(t, entity.AIProviderOpenAI, res.Provider)
	require.Equal(t, "gpt-5-mini", res.Model, "the requested slug when the provider reports none")
	require.Equal(t, []string{slugChat}, rg.or.models())
	require.Equal(t, []string{"gpt-5-mini"}, rg.oa.models())

	rows := rg.rows()
	require.Len(t, rows, 2)
	require.Equal(t, entity.AICallFree, rows[0].Status)
	require.Equal(t, "transport", rows[0].End.ErrorCode)
	require.False(t, *rows[0].End.Engaged)
	require.Equal(t, entity.AICostFree, rows[0].End.CostSource)
	require.True(t, rows[0].End.CostUSD.Decimal.IsZero())
	require.Equal(t, 1, rows[0].Start.CallNo)
	require.Empty(t, rows[0].Start.FallbackFrom)

	require.Equal(t, entity.AICallOK, rows[1].Status)
	require.Equal(t, entity.AIProviderOpenAI, rows[1].Start.ProviderKey)
	require.Equal(t, entity.AIProviderOpenRouter, rows[1].Start.FallbackFrom)
	require.Equal(t, 2, rows[1].Start.CallNo)
	require.Contains(t, rg.logs.String(), "failed before its request was written")
}

// TestChatA404FallsBackAndStaysConfiguration — a 404 moved no money: the row is `free` with
// model_unknown / 404, the next candidate is tried, and the breaker does NOT count it (a dead slug is
// configuration, not weather). With one candidate the error still answers the handlers' door:
// errors.Is(err, ErrModelUnavailable), and it is the exhausted error.
//
// MUTATION: the exhausted error is fmt.Errorf("%w: %v", ErrAllCandidatesFailed, last) → red (not Is ErrModelUnavailable).
// MUTATION: the exhausted error is `last` unwrapped → red (not Is ErrAllCandidatesFailed).
func TestChatA404FallsBackAndStaysConfiguration(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = fails(errStatus(entity.AIProviderOpenRouter, 404))
	for range 4 {
		_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.NoError(t, err)
	}
	require.Equal(t, 4, rg.or.n(), "four 404s never opened the breaker")
	require.Equal(t, registry.BreakerClosed, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	r := rg.rows()[0]
	require.Equal(t, entity.AICallFree, r.Status)
	require.Equal(t, "model_unknown", r.End.ErrorCode)
	require.Equal(t, 404, *r.End.HTTPStatus)

	// One candidate: the refusal reaches the handler as configuration.
	one := newRig(t, nil, testDefaults, 0)
	one.or.do = fails(errStatus(entity.AIProviderOpenRouter, 404))
	_, err := one.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok)
	require.Equal(t, 404, ce.HTTPStatus)
	require.False(t, aiprov.Engaged(err))
}

// TestChatTransientFaultsOpenTheBreaker — 429 / 5xx before the write are the provider being down:
// each is a counted fault, the third opens the breaker, and the next Chat does not call the provider
// at all (the registry no longer lists it) while the fallback keeps answering.
//
// MUTATION: a not-engaged failure ends with reg.Release instead of reg.RecordFailure → red (never opens).
func TestChatTransientFaultsOpenTheBreaker(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	for _, status := range []int{429, 502, 503} {
		rg.or.do = fails(errStatus(entity.AIProviderOpenRouter, status))
		res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.NoError(t, err)
		require.Equal(t, entity.AIProviderOpenAI, res.Provider)
	}
	require.Equal(t, registry.BreakerOpen, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))

	rg.or.do = answers("should not be called")
	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, entity.AIProviderOpenAI, res.Provider)
	require.Equal(t, 3, rg.or.n(), "an open breaker is not called")
	require.Equal(t, 4, rg.oa.n())
	last := rg.rows()[len(rg.rows())-1]
	require.Empty(t, last.Start.FallbackFrom, "no physical call came before it in this chain")
	require.Equal(t, 1, last.Start.CallNo)
}

// ───────────────────────── engaged → terminal ─────────────────────────

// TestChatEngagedFailureIsTerminal — the request was written and then something broke: money may
// have moved, the next candidate is NOT tried, the row is `unknown` (engaged, no price, its code and
// status), and the caller gets the transport's own error — not the exhausted one.
//
// MUTATION: the engaged branch `continue`s like the not-engaged one → red (openai called).
func TestChatEngagedFailureIsTerminal(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	want := errEngaged(entity.AIProviderOpenRouter)
	rg.or.do = fails(want)

	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.Nil(t, res)
	require.Same(t, want, err)
	require.NotErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	require.True(t, aiprov.Engaged(err))
	require.Zero(t, rg.oa.n(), "never a second payment for one answer")

	rows := rg.rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AICallUnknown, rows[0].Status)
	require.True(t, *rows[0].End.Engaged)
	require.False(t, rows[0].End.CostUSD.Valid)
	require.Equal(t, entity.AICostNone, rows[0].End.CostSource)
	require.Equal(t, "provider_error", rows[0].End.ErrorCode)
	require.Equal(t, 200, *rows[0].End.HTTPStatus)
}

// TestChatAnUnclassifiedFailureIsTerminal — a failure that is not a CallError says nothing about the
// wire, so it is read as engaged: terminal, row `unknown`.
//
// MUTATION: `if !isCE || aiprov.Engaged(err)` → `if isCE && aiprov.Engaged(err)` → red (openai called, row free).
// MUTATION: the (nil, nil) guard in call removed → red (nil answer dereferenced).
func TestChatAnUnclassifiedFailureIsTerminal(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	boom := errors.New("boom")
	rg.or.do = fails(boom)

	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.Same(t, boom, err)
	require.Zero(t, rg.oa.n())
	rows := rg.rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AICallUnknown, rows[0].Status)
	require.True(t, *rows[0].End.Engaged)
	require.Empty(t, rows[0].End.ErrorCode)

	// A transport that answers nothing and says nothing broke its contract after the request may have
	// left: the same terminal, never a nil answer handed on as a success.
	rg.or.do = func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) { return nil, nil }
	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.Nil(t, res)
	require.ErrorContains(t, err, "neither an answer nor an error")
	require.Zero(t, rg.oa.n())
	require.Equal(t, entity.AICallUnknown, rg.rows()[1].Status)
}

// TestChatEngagedPartialResultBooksItsTokens — an empty answer after the model spent its budget comes
// back as a partial result WITH the error (oaichat): the chain stops, the caller gets both (the usage
// is what prices the attempt), and the row is `charged_failed` with the tokens and the table price
// instead of an unknown that hides money already known.
//
// MUTATION: failEnd ignores res (no usageEnd / price) → red (row unknown, no tokens).
// MUTATION: the terminal return is (nil, err) → red (the partial result is lost to the caller).
func TestChatEngagedPartialResultBooksItsTokens(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeTechCardAnalysis), testDefaults, 0)
	rg.or.do = func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		return &aiprov.ChatResult{FinishReason: "length", Model: slugChat,
				Usage: aiprov.TokenUsage{Prompt: 2000, Completion: 2500, Reasoning: 2500}},
			&aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: "budget_exhausted", HTTPStatus: 200, Engaged: true,
				Err: fmt.Errorf("openrouter: %w (2500 completion tokens spent, none of them answer)", aiprov.ErrBudgetExhausted)}
	}
	res, err := rg.router.Chat(context.Background(), entity.AIPurposeTechCardAnalysis, chatReq)
	require.ErrorIs(t, err, aiprov.ErrBudgetExhausted)
	require.NotNil(t, res, "the usage rides back with the error")
	require.Equal(t, 2500, res.Usage.Completion)
	require.Equal(t, entity.AIProviderOpenRouter, res.Provider)
	require.Zero(t, rg.oa.n())

	rows := rg.rows()
	require.Len(t, rows, 1)
	r := rows[0]
	require.Equal(t, entity.AICallChargedFailed, r.Status)
	require.Equal(t, 2500, *r.End.CompletionTokens)
	require.Equal(t, entity.AICostTable, r.End.CostSource)
	// Sonnet 5 on OpenRouter: $3/M in, $15/M out → (2000×3 + 2500×15) / 1e6 = 0.0435.
	require.True(t, decimal.RequireFromString("0.0435").Equal(r.End.CostUSD.Decimal), r.End.CostUSD.Decimal.String())
	require.Equal(t, "budget_exhausted", r.End.ErrorCode)
}

// TestChatEngagedFailureReleasesAProbeWithoutCountingIt — the breaker's half of "engaged never
// counts": a half-open provider's ONE probe ends in an engaged failure; the probe must be FREED (the
// next Chat is admitted and may close it) and the failure must NOT re-open the breaker.
//
// MUTATION: the failure path makes no breaker end (RecordFailure dropped) → red (probe stays reserved,
// the next Chat is refused and falls to openai).
// MUTATION: the failure path reports a retryable not-engaged CallError to RecordFailure → red (re-opened).
func TestChatEngagedFailureReleasesAProbeWithoutCountingIt(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = fails(errStatus(entity.AIProviderOpenRouter, 503))
	for range 3 {
		_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.NoError(t, err)
	}
	require.Equal(t, registry.BreakerOpen, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	rg.clk.advance(5*time.Minute + time.Second)

	rg.or.do = fails(errEngaged(entity.AIProviderOpenRouter))
	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.True(t, aiprov.Engaged(err), "the probe's engaged failure is terminal")
	require.Equal(t, 4, rg.or.n())
	require.Equal(t, registry.BreakerHalfOpen, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat),
		"an engaged failure neither re-opens nor closes")

	rg.or.do = answers("probe ok")
	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, entity.AIProviderOpenRouter, res.Provider, "the freed probe was handed to the next Chat")
	require.Equal(t, registry.BreakerClosed, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
}

// ───────────────────────── D-16: an engaged timeout on a chat purpose advances ─────────────────────────

// TestChatEngagedTimeoutAdvances — a provider that hung after the write (Code timeout): the first row
// is `unknown` (money may have moved), and — chat calls cost cents, O-05 — the next candidate answers.
//
// MUTATION: the D-16 branch returns (res, err) (`hung` treated as not hung) → red.
func TestChatEngagedTimeoutAdvances(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = fails(errEngagedTimeout(entity.AIProviderOpenRouter))
	rg.oa.do = answers("after the hang")

	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, "after the hang", res.Text)

	rows := rg.rows()
	require.Len(t, rows, 2)
	require.Equal(t, entity.AICallUnknown, rows[0].Status)
	require.Equal(t, aiprov.CodeTimeout, rows[0].End.ErrorCode)
	require.True(t, *rows[0].End.Engaged)
	require.False(t, rows[0].End.CostUSD.Valid)
	require.Equal(t, entity.AICallOK, rows[1].Status)
	require.Equal(t, entity.AIProviderOpenRouter, rows[1].Start.FallbackFrom)
	require.Equal(t, 2, rows[1].Start.CallNo)
	require.Equal(t, registry.BreakerClosed, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	require.Contains(t, rg.logs.String(), "D-16")
}

// TestChatOwnBudgetExpiryIsAHangWhateverTheCode — the router's OWN bound expiring is what tells a
// hang apart, even when the transport names it something else: the call is bounded by the budget
// (base 40 ms, no ceiling), the transport waits it out and reports an engaged "transport" error, the
// caller is still there → advance.
//
// MUTATION: ownDeadline forced false in call → red (terminal on the mislabelled hang).
// MUTATION: the call context is ctx itself (no WithTimeout) → red (the fake never returns: test timeout
// guard below fails it).
func TestChatOwnBudgetExpiryIsAHangWhateverTheCode(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 40*time.Millisecond)
	rg.or.do = func(ctx context.Context, _ string, _ aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			return nil, errors.New("the router's budget never fired")
		}
		return nil, &aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: "transport", Engaged: true,
			Err: errors.New("openrouter: read response: connection reset")}
	}
	rg.oa.do = answers("after our own deadline")

	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	require.Equal(t, "after our own deadline", res.Text)
	require.Equal(t, entity.AICallUnknown, rg.rows()[0].Status)
}

// TestChatStopsOnTheCallersTimeout — the CALLER's deadline expiring is not the call's: after an
// engaged timeout under a dead caller context nothing more is tried, and the caller gets that error.
//
// MUTATION: the between-candidates ctx.Err() check removed → red (openai called).
func TestChatStopsOnTheCallersTimeout(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = func(ctx context.Context, _ string, _ aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		<-ctx.Done()
		return nil, errEngagedTimeout(entity.AIProviderOpenRouter)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := rg.router.Chat(ctx, entity.AIPurposeNoteMarkdown, chatReq)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok)
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
	require.NotErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	require.Zero(t, rg.oa.n(), "the caller left; no second candidate")
	require.Len(t, rg.rows(), 1)
}

// TestChatStopsWhenTheCallerLeavesBetweenCandidates — the caller cancels while the first candidate
// fails before the write: the next candidate is not tried and the last error comes back as it is.
//
// MUTATION: the between-candidates ctx.Err() check removed → red (openai called).
func TestChatStopsWhenTheCallerLeavesBetweenCandidates(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := &aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: "canceled",
		Err: fmt.Errorf("openrouter: request failed: %w", context.Canceled)}
	rg.or.do = func(context.Context, string, aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		cancel()
		return nil, want
	}

	_, err := rg.router.Chat(ctx, entity.AIPurposeNoteMarkdown, chatReq)
	require.Same(t, want, err)
	require.Zero(t, rg.oa.n())
	rows := rg.rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AICallFree, rows[0].Status)
	require.NotContains(t, rg.logs.String(), "trying the next", "no promise of a next candidate that never comes")

	// Dead before the first call: nothing is called, nothing is booked.
	_, err = rg.router.Chat(ctx, entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, rg.or.n())
	require.Len(t, rg.rows(), 1)
}

// ───────────────────────── exhausted ─────────────────────────

// TestChatExhaustedWrapsTheLast — every candidate failed before engaging: ErrAllCandidatesFailed
// wrapping the LAST failure (AsCallError reads the last provider's status), not engaged.
//
// MUTATION: the exhausted error wraps the FIRST failure instead of the last → red.
func TestChatExhaustedWrapsTheLast(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = fails(errStatus(entity.AIProviderOpenRouter, 503))
	rg.oa.do = fails(errStatus(entity.AIProviderOpenAI, 429))

	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok)
	require.Equal(t, entity.AIProviderOpenAI, ce.Provider)
	require.Equal(t, 429, ce.HTTPStatus)
	require.False(t, aiprov.Engaged(err))
	require.Len(t, rg.rows(), 2)
}

// TestChatKeepsAnEarlierEngagementOnTheExhaustedError — D-16 moved past an engaged hang, and the
// fallback then failed before ITS write: the caller must still read "money may have moved", or it
// books the attempt as free while the first provider may be billing it.
//
// MUTATION: withEarlierEngagement returns err unchanged → red (aiprov.Engaged false).
func TestChatKeepsAnEarlierEngagementOnTheExhaustedError(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = fails(errEngagedTimeout(entity.AIProviderOpenRouter))
	rg.oa.do = fails(errStatus(entity.AIProviderOpenAI, 503))

	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok)
	require.Equal(t, 503, ce.HTTPStatus, "the handler still reads the last failure's fields")
	require.True(t, aiprov.Engaged(err), "…and still learns an earlier call engaged")
}

// ───────────────────────── skipped without a call ─────────────────────────

// TestChatSkipsAProviderItsBreakerRefuses — a half-open provider whose one probe is already out:
// Admit refuses, and the candidate is passed over without a call and without a row. Alone on its
// route, the refusal is the exhausted error naming the breaker — nothing was called.
//
// MUTATION: `if !admitted` branch removed (the call goes ahead) → red.
func TestChatSkipsAProviderItsBreakerRefuses(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeNoteMarkdown), testDefaults, 0)
	rg.or.do = fails(errStatus(entity.AIProviderOpenRouter, 503))
	for range 3 {
		_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.NoError(t, err)
	}
	rg.clk.advance(5*time.Minute + time.Second)
	probe, ok := rg.reg.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat) // somebody else holds the probe
	require.True(t, ok)
	defer rg.reg.Release(entity.AIProviderOpenRouter, entity.AICapabilityChat, probe)
	calls, rows := rg.or.n(), len(rg.rows())

	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, entity.AIProviderOpenAI, res.Provider)
	require.Equal(t, calls, rg.or.n(), "refused: no call")
	all := rg.rows()
	require.Len(t, all, rows+1, "refused: no row")
	require.Empty(t, all[len(all)-1].Start.FallbackFrom)
	require.Equal(t, 1, all[len(all)-1].Start.CallNo)

	// Alone on its route.
	rg.store.edit(func(c *entity.AIConfig) {
		for i := range c.Routes {
			if c.Routes[i].Purpose == entity.AIPurposeNoteMarkdown {
				c.Routes[i].Candidates = []entity.AIRouteCandidate{at(1, entity.AIProviderOpenRouter, "")}
			}
		}
	})
	require.NoError(t, rg.reg.Reload(context.Background()))
	_, err = rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	require.ErrorIs(t, err, ErrPaused, "a probe held elsewhere is a pause, not a missing setting")
	require.Contains(t, err.Error(), "circuit breaker")
	require.Equal(t, calls, rg.or.n())
}

// TestChatSkipsAProviderWithNoTransport — the owner enabled a provider this build has no adapter for:
// it is passed over, the next candidate answers as the FIRST call, and the log says so once per
// provider per config version — not once per press.
//
// SINCE B-23 PRODUCTION HAS NO SUCH PROVIDER: app.go wires a chat transport for every provider that
// serves chat (openrouter, openai, apibost, anthropic, google), so the branch guards the day a
// provider joins the vocabulary before its adapter. It is exercised two ways. The registry half keeps
// the rig as it is — the rig's router holds no anthropic transport, so anthropic stands in for that
// provider (a key the vocabulary does not know cannot reach the router through the registry: walk
// drops any provider that does not serve chat, runblob included). The static half uses a provider key
// that exists nowhere, "acme", with no transport at all.
//
// MUTATION: the no-transport branch returns ErrNotConfigured instead of continuing → red (both halves).
// MUTATION: warnNoTransport without its seen/version check → red (two lines for one version).
func TestChatSkipsAProviderWithNoTransport(t *testing.T) {
	t.Run("a static route naming a provider key that exists nowhere", func(t *testing.T) {
		or := &chatter{}
		st := NewStatic([]StaticCandidate{
			{ProviderKey: "acme", Model: "acme-1"}, // no Chatter: no transport for this key
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: or, Model: "m"},
		})
		logs := &bytes.Buffer{}
		st.log = slog.New(slog.NewTextHandler(logs, nil))
		require.Equal(t, entity.AIProviderOpenRouter, st.PrimaryProvider(entity.AIPurposeNoteMarkdown))
		res, err := st.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.NoError(t, err)
		require.Equal(t, entity.AIProviderOpenRouter, res.Provider)
		require.Equal(t, []string{"m"}, or.models())
		require.Contains(t, logs.String(), "provider=acme")
		require.Equal(t, 1, strings.Count(logs.String(), "no chat transport"))
	})

	rg := newRig(t, map[string][]entity.AIRouteCandidate{entity.AIPurposeNoteMarkdown: {
		at(1, entity.AIProviderAnthropic, "claude-sonnet-5"), at(2, entity.AIProviderOpenRouter, ""),
	}}, testDefaults, 0)

	require.Equal(t, entity.AIProviderOpenRouter, rg.router.PrimaryProvider(entity.AIPurposeNoteMarkdown))
	require.Equal(t, slugChat, rg.router.PrimaryModel(entity.AIPurposeNoteMarkdown))
	for range 2 {
		res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.NoError(t, err)
		require.Equal(t, entity.AIProviderOpenRouter, res.Provider)
	}
	rows := rg.rows()
	require.Len(t, rows, 2)
	require.Equal(t, 1, rows[0].Start.CallNo)
	require.Empty(t, rows[0].Start.FallbackFrom)
	require.Equal(t, 1, strings.Count(rg.logs.String(), "no chat transport"))

	// A new config version is a new chance to be told.
	rg.store.edit(func(*entity.AIConfig) {})
	require.NoError(t, rg.reg.Reload(context.Background()))
	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(rg.logs.String(), "no chat transport"))
	require.Contains(t, rg.logs.String(), "provider=anthropic")
	require.NotContains(t, rg.logs.String(), "sk-anthropic", "never a key in the log")
}

// TestChatSkipsACandidateWithNoModel — a candidate whose effective model is "" is switched off for
// the purpose: apibost with no model has no default here (testDefaults carries no ByProvider table —
// production's does, TestADirectProviderRowWithNoModelCallsItsDefault), so the openrouter candidate
// after it answers with its default; with no default either, the purpose is not configured and
// nothing is called.
//
// MUTATION: the `model == ""` skip removed → red (apibost called with "").
func TestChatSkipsACandidateWithNoModel(t *testing.T) {
	rg := newRig(t, map[string][]entity.AIRouteCandidate{entity.AIPurposeNoteMarkdown: {
		at(1, entity.AIProviderApibost, ""), at(2, entity.AIProviderOpenRouter, ""),
	}}, testDefaults, 0)
	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, entity.AIProviderOpenRouter, res.Provider)
	require.Zero(t, rg.ab.n())
	require.Equal(t, []string{slugChat}, rg.or.models())

	off := newRig(t, nil, Defaults{}, 0)
	require.False(t, off.router.Enabled(entity.AIPurposeNoteMarkdown))
	_, err = off.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Zero(t, off.or.n())
	require.Empty(t, off.rows())
}

// TestEffectiveModelKeepsTodaysEnvSemantics — the route row's own slug wins; an empty one reads the
// env default of ITS purpose (enhance and analysis → Analysis, the playground door → Ideas, the rest
// → Chat) for openrouter only; OPENROUTER_MODEL_IDEAS=off switches the whole door off — the seeded
// position-2 row with its own slug included — so nothing is called.
//
// MUTATION: Defaults.slug maps chat.techcard_enhance to Chat → red.
// MUTATION: the IdeasOff check removed from EffectiveModel (off only empties Ideas) → red (gpt-5-mini called).
func TestEffectiveModelKeepsTodaysEnvSemantics(t *testing.T) {
	r := New(nil, nil, nil, testDefaults, 0)
	or := func(m string) registry.Candidate {
		return registry.Candidate{ProviderKey: entity.AIProviderOpenRouter, Model: m}
	}
	for purpose, want := range map[string]string{
		entity.AIPurposeTechCardEnhance:  slugAnalysis,
		entity.AIPurposeTechCardAnalysis: slugAnalysis,
		entity.AIPurposeNoteMarkdown:     slugChat,
		entity.AIPurposeEmailTranslate:   slugChat,
		entity.AIPurposeDesignDraftIdea:  slugChat,
		entity.AIPurposePlaygroundIdeas:  slugIdeas,
		entity.AIPurposeImageGenerate:    "",
	} {
		require.Equal(t, want, r.EffectiveModel(purpose, or("")), purpose)
		require.Equal(t, "x/y", r.EffectiveModel(purpose, or(" x/y ")), purpose)
	}
	require.Empty(t, r.EffectiveModel(entity.AIPurposeNoteMarkdown,
		registry.Candidate{ProviderKey: entity.AIProviderOpenAI}), "an OpenRouter slug is never sent to OpenAI")

	// The kill switch, against the seeded two-row route.
	offDefaults := testDefaults
	offDefaults.Ideas, offDefaults.IdeasOff = "", true
	rg := newRig(t, nil, offDefaults, 0)
	require.False(t, rg.router.Enabled(entity.AIPurposePlaygroundIdeas))
	require.Empty(t, rg.router.PrimaryModel(entity.AIPurposePlaygroundIdeas))
	_, err := rg.router.Chat(context.Background(), entity.AIPurposePlaygroundIdeas, chatReq)
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Zero(t, rg.or.n(), "off means neither the slug nor its fallback is called")
	require.True(t, rg.router.Enabled(entity.AIPurposeNoteMarkdown), "the switch is the door's, not the provider's")

	// On: the door's default first, the seeded fallback row second.
	on := newRig(t, nil, testDefaults, 0)
	on.or.do = func(_ context.Context, model string, _ aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		if model == slugIdeas {
			return nil, errStatus(entity.AIProviderOpenRouter, 404)
		}
		return &aiprov.ChatResult{Text: "ideas"}, nil
	}
	res, err := on.router.Chat(context.Background(), entity.AIPurposePlaygroundIdeas, chatReq)
	require.NoError(t, err)
	require.Equal(t, slugIdeasFB, res.Model)
	require.Equal(t, []string{slugIdeas, slugIdeasFB}, on.or.models())
	require.Equal(t, entity.AIProviderOpenRouter, on.rows()[1].Start.FallbackFrom)
}

// TestADirectProviderRowWithNoModelCallsItsDefault (B-23) — a route row on a direct provider with no
// model resolves to that provider's default chat slug (Defaults.ByProvider), so it is callable: the
// rig's (openai, "") calls gpt-5-mini, RouteHead names it, and the answer — reported as the dated
// snapshot OpenAI echoes — is priced from the table with the price version. The row's own model still
// wins; openrouter never reads ByProvider (an openrouter entry planted in the map is ignored); a
// provider absent from the map stays "" and is passed over; a non-chat purpose gets no chat default;
// the Ideas kill switch closes a defaulted row too.
//
// MUTATIONS (each measured red → restored green): EffectiveModel without the ByProvider branch
// (returns "" for every non-openrouter row, the pre-B-23 code) → red (openai not callable); ByProvider
// read before the openrouter branch → red (the planted openrouter entry answers instead of the env
// slug); the isChatPurpose guard removed → red (image.generate gets a chat slug).
func TestADirectProviderRowWithNoModelCallsItsDefault(t *testing.T) {
	d := testDefaults
	d.ByProvider = map[string]string{
		entity.AIProviderOpenAI:     "gpt-5-mini",
		entity.AIProviderApibost:    " claude-sonnet-5 ",
		entity.AIProviderOpenRouter: "must/not-be-read",
	}
	r := New(nil, nil, nil, d, 0)
	cand := func(pk, m string) registry.Candidate { return registry.Candidate{ProviderKey: pk, Model: m} }
	require.Equal(t, "gpt-5-mini", r.EffectiveModel(entity.AIPurposeNoteMarkdown, cand(entity.AIProviderOpenAI, "")))
	require.Equal(t, "gpt-5-mini", r.EffectiveModel(entity.AIPurposeTechCardAnalysis, cand(entity.AIProviderOpenAI, " ")),
		"one default for every chat purpose")
	require.Equal(t, "claude-sonnet-5", r.EffectiveModel(entity.AIPurposeEmailTranslate, cand(entity.AIProviderApibost, "")))
	require.Equal(t, "gpt-5.2", r.EffectiveModel(entity.AIPurposeNoteMarkdown, cand(entity.AIProviderOpenAI, "gpt-5.2")),
		"the row's own model wins")
	require.Equal(t, slugChat, r.EffectiveModel(entity.AIPurposeNoteMarkdown, cand(entity.AIProviderOpenRouter, "")),
		"openrouter answers with its env slug, never with ByProvider")
	require.Equal(t, slugAnalysis, r.EffectiveModel(entity.AIPurposeTechCardEnhance, cand(entity.AIProviderOpenRouter, "")))
	require.Empty(t, r.EffectiveModel(entity.AIPurposeNoteMarkdown, cand(entity.AIProviderAnthropic, "")),
		"a provider with no default stays uncallable")
	require.Empty(t, r.EffectiveModel(entity.AIPurposeImageGenerate, cand(entity.AIProviderOpenAI, "")),
		"a chat default is never an image slug")
	off := d
	off.IdeasOff = true
	require.Empty(t, New(nil, nil, nil, off, 0).EffectiveModel(entity.AIPurposePlaygroundIdeas, cand(entity.AIProviderOpenAI, "")),
		"the kill switch closes a defaulted row too")

	// Through the registry: (openai, "") is callable, called with the default, and priced.
	rg := newRig(t, map[string][]entity.AIRouteCandidate{
		entity.AIPurposeNoteMarkdown: {at(1, entity.AIProviderOpenAI, "")},
	}, d, 0)
	require.True(t, rg.router.Enabled(entity.AIPurposeNoteMarkdown))
	p, m := rg.router.RouteHead(entity.AIPurposeNoteMarkdown)
	require.Equal(t, entity.AIProviderOpenAI, p)
	require.Equal(t, "gpt-5-mini", m)
	rg.oa.do = func(_ context.Context, model string, _ aiprov.ChatRequest) (*aiprov.ChatResult, error) {
		return &aiprov.ChatResult{Text: "ok", Model: model + "-2025-08-07",
			Usage: aiprov.TokenUsage{Prompt: 1000, Completion: 500}}, nil
	}
	res, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, entity.AIProviderOpenAI, res.Provider)
	require.Equal(t, []string{"gpt-5-mini"}, rg.oa.models())
	require.Zero(t, rg.or.n())
	rows := rg.rows()
	require.Len(t, rows, 1)
	require.Equal(t, "gpt-5-mini", rows[0].Start.Model)
	require.Equal(t, "gpt-5-mini-2025-08-07", rows[0].End.ModelActual)
	require.Equal(t, entity.AICostTable, rows[0].End.CostSource, "the dated snapshot prices as its alias")
	require.True(t, decimal.RequireFromString("0.00125").Equal(rows[0].End.CostUSD.Decimal), rows[0].End.CostUSD.Decimal.String())
	require.Equal(t, pricing.Version, rows[0].End.PriceVersion)
}

// TestChatRefusesAPurposeThatIsNotChat — an image purpose's candidates are image providers; a chat
// request must never reach them.
//
// MUTATION: candidates drops the isChatPurpose guard → red (openrouter called for image.generate).
func TestChatRefusesAPurposeThatIsNotChat(t *testing.T) {
	// A route with its own slug, so nothing but the purpose check stands between it and a call.
	rg := newRig(t, map[string][]entity.AIRouteCandidate{
		entity.AIPurposeImageGenerate: {at(1, entity.AIProviderOpenRouter, "openai/gpt-image-2")},
	}, testDefaults, 0)
	_, err := rg.router.Chat(context.Background(), entity.AIPurposeImageGenerate, chatReq)
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Zero(t, rg.or.n())
	require.False(t, rg.router.Enabled(entity.AIPurposeImageGenerate))
	require.False(t, rg.router.Enabled("chat.nonsense"))
}

// ───────────────────────── run keys, budget, cap ─────────────────────────

// TestChatWithRunKeysEveryRow — under WithRun every row of the chain carries (run_id, attempt_no),
// numbered 1..N: the fallback is call 2, which the store's UNIQUE (run_id, attempt_no, call_no) needs.
//
// MUTATION: CallNo: 1 on every Begin → red (the store refuses the fallback's row; it is lost).
// MUTATION: RunID/AttemptNo not read from ctx → red.
func TestChatWithRunKeysEveryRow(t *testing.T) {
	rg := newRig(t, fallbackRoute(entity.AIPurposeDesignDraftIdea), testDefaults, 0)
	rg.or.do = fails(errTransport(entity.AIProviderOpenRouter))
	ctx := aiprov.WithRun(aiprov.WithActor(context.Background(), aiprov.Actor{Username: "im"}), 41, 3)

	_, err := rg.router.Chat(ctx, entity.AIPurposeDesignDraftIdea, chatReq)
	require.NoError(t, err)
	rows := rg.rows()
	require.Len(t, rows, 2, "both calls of the chain are booked")
	for i, r := range rows {
		require.Equal(t, 41, *r.Start.RunID)
		require.Equal(t, 3, *r.Start.AttemptNo)
		require.Equal(t, i+1, r.Start.CallNo)
		require.Equal(t, "im", r.Start.Actor)
	}
}

// TestChainBudgetIsTheSumOfTheCallableChain — Σ CompletionBudget over the candidates Chat may call
// (a provider with no transport is not one), capped at two for the draft-idea handler lease, and
// never below one call.
//
// MUTATION: ChainBudget counts r.candidates instead of r.callable → red (4× with anthropic).
// MUTATION: ChainBudget without the n < 1 floor → red (0 on an empty route).
func TestChainBudgetIsTheSumOfTheCallableChain(t *testing.T) {
	three := []entity.AIRouteCandidate{
		at(1, entity.AIProviderOpenRouter, ""), at(2, entity.AIProviderAnthropic, "claude-sonnet-5"),
		at(3, entity.AIProviderOpenAI, "gpt-5-mini"), at(4, entity.AIProviderApibost, "claude-sonnet-5"),
	}
	rg := newRig(t, map[string][]entity.AIRouteCandidate{
		entity.AIPurposeNoteMarkdown: three, entity.AIPurposeDesignDraftIdea: three,
		entity.AIPurposeEmailTranslate: {at(1, entity.AIProviderApibost, "")},
	}, testDefaults, 90*time.Second)

	one := aiprov.CompletionBudget(90*time.Second, 8000) // 90 s + 8000/30 s
	require.Equal(t, 90*time.Second+8000*time.Second/30, one)
	require.Equal(t, 3*one, rg.router.ChainBudget(entity.AIPurposeNoteMarkdown, 8000))
	require.Equal(t, 2*one, rg.router.ChainBudget(entity.AIPurposeDesignDraftIdea, 8000))
	require.Equal(t, one, rg.router.ChainBudget(entity.AIPurposeEmailTranslate, 8000), "nothing callable: one call's worth")
	require.Equal(t, aiprov.CompletionBudget(0, 300), (*Router)(nil).ChainBudget(entity.AIPurposeNoteMarkdown, 300))
}

// TestChatStopsAtTheLeasedChainCap — the draft-idea lease is sized for two calls, so a chain of three
// candidates stops after two failures; the third is never called.
//
// MUTATION: the `limit > 0 && calls >= limit` break removed → red (apibost called).
func TestChatStopsAtTheLeasedChainCap(t *testing.T) {
	rg := newRig(t, map[string][]entity.AIRouteCandidate{entity.AIPurposeDesignDraftIdea: {
		at(1, entity.AIProviderOpenRouter, ""), at(2, entity.AIProviderOpenAI, "gpt-5-mini"),
		at(3, entity.AIProviderApibost, "claude-sonnet-5"),
	}}, testDefaults, 0)
	rg.or.do = fails(errTransport(entity.AIProviderOpenRouter))
	rg.oa.do = fails(errStatus(entity.AIProviderOpenAI, 502))

	_, err := rg.router.Chat(context.Background(), entity.AIPurposeDesignDraftIdea, chatReq)
	require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	require.Zero(t, rg.ab.n(), "the lease covers two calls, the chain makes two")
	ce, _ := aiprov.AsCallError(err)
	require.Equal(t, 502, ce.HTTPStatus)

	// The same route on an unleased purpose goes all the way.
	rg2 := newRig(t, map[string][]entity.AIRouteCandidate{entity.AIPurposeNoteMarkdown: {
		at(1, entity.AIProviderOpenRouter, ""), at(2, entity.AIProviderOpenAI, "gpt-5-mini"),
		at(3, entity.AIProviderApibost, "claude-sonnet-5"),
	}}, testDefaults, 0)
	rg2.or.do = fails(errTransport(entity.AIProviderOpenRouter))
	rg2.oa.do = fails(errStatus(entity.AIProviderOpenAI, 502))
	_, err = rg2.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, 1, rg2.ab.n())
}

// TestChatBoundsEachCallByItsBudget — the transport receives a context whose deadline is the call's
// budget: base + ceiling / 30 tok/s (60 s + 1200/30 s = 100 s).
//
// MUTATION: the call runs on ctx (no WithTimeout) → red (no deadline).
func TestChatBoundsEachCallByItsBudget(t *testing.T) {
	rg := newRig(t, nil, testDefaults, 0)
	before := time.Now()
	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	c := rg.or.calls[0]
	require.True(t, c.hasDL, "every call is bounded")
	want := aiprov.CompletionBudget(0, 1200)
	require.Equal(t, 100*time.Second, want)
	got := c.deadline.Sub(before)
	require.InDelta(t, want.Seconds(), got.Seconds(), 1.0, got.String())
}

// ───────────────────────── seams and doors ─────────────────────────

// TestNewSingleAndNewStatic — the test seams: one fixed candidate (no registry, no ledger, no
// breaker), and a fixed ordered list whose entries may share a provider (B-18's two-slug suggest
// route: a 404 on the first slug, the second answers).
//
// MUTATION: NewStatic keeps only cands[:1] → red (the 404 is exhausted instead of answered).
func TestNewSingleAndNewStatic(t *testing.T) {
	c := &chatter{do: answers("single")}
	s := NewSingle(entity.AIProviderOpenRouter, c, " some/slug ")
	res, err := s.Chat(context.Background(), entity.AIPurposeEmailTranslate, chatReq)
	require.NoError(t, err)
	require.Equal(t, "single", res.Text)
	require.Equal(t, entity.AIProviderOpenRouter, res.Provider)
	require.Equal(t, []string{"some/slug"}, c.models())
	require.True(t, s.Enabled(entity.AIPurposeTechCardAnalysis))
	require.False(t, s.Enabled(entity.AIPurposeImageGenerate))
	require.Equal(t, entity.AIProviderOpenRouter, s.PrimaryProvider(entity.AIPurposeNoteMarkdown))
	require.Equal(t, "some/slug", s.PrimaryModel(entity.AIPurposeNoteMarkdown))
	require.Equal(t, []string{"some/slug"}, s.OpenRouterSlugs())

	first := &chatter{do: fails(errStatus(entity.AIProviderOpenRouter, 404))}
	second := &chatter{do: answers("fallback slug")}
	st := NewStatic([]StaticCandidate{
		{ProviderKey: entity.AIProviderOpenRouter, Chatter: first, Model: slugIdeas},
		{ProviderKey: entity.AIProviderOpenRouter, Chatter: second, Model: slugIdeasFB},
	})
	res, err = st.Chat(context.Background(), entity.AIPurposePlaygroundIdeas, chatReq)
	require.NoError(t, err)
	require.Equal(t, "fallback slug", res.Text)
	require.Equal(t, slugIdeasFB, res.Model)
	require.Equal(t, []string{slugIdeas}, first.models())
	require.Equal(t, 2*aiprov.CompletionBudget(0, 300), st.ChainBudget(entity.AIPurposePlaygroundIdeas, 300))
}

// translateCompleter is internal/translate's `completer`, verbatim — what Completer must satisfy.
type translateCompleter interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool) (string, error)
	Enabled() bool
}

var _ translateCompleter = (*PurposeCompleter)(nil)

// TestCompleterIsTranslateShaped — the translate door is one Chat for its purpose: system, user and
// jsonMode reach the transport, nothing else is set, the text comes back.
//
// MUTATION: Complete drops jsonMode from the request → red.
func TestCompleterIsTranslateShaped(t *testing.T) {
	c := &chatter{do: answers(`{"items":[]}`)}
	var tc translateCompleter = NewSingle(entity.AIProviderOpenRouter, c, "m").Completer(entity.AIPurposeEmailTranslate)
	require.True(t, tc.Enabled())
	text, err := tc.Complete(context.Background(), "sys", "usr", true)
	require.NoError(t, err)
	require.Equal(t, `{"items":[]}`, text)
	require.Equal(t, aiprov.ChatRequest{System: "sys", User: "usr", JSONMode: true}, c.calls[0].req)

	var nilRouter *Router
	require.False(t, nilRouter.Completer(entity.AIPurposeEmailTranslate).Enabled())
	_, err = nilRouter.Completer(entity.AIPurposeEmailTranslate).Complete(context.Background(), "s", "u", false)
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
}

// TestOpenRouterSlugs — the slugs WarnIfRetired probes: every openrouter chat candidate's effective
// slug, sorted, once — today's four (Chat, Analysis, Ideas + the seeded fallback), no image slug, no
// other provider's slug.
//
// MUTATION: the ProviderKey != openrouter filter removed → red (apibost's claude-sonnet-5 is listed).
func TestOpenRouterSlugs(t *testing.T) {
	rg := newRig(t, map[string][]entity.AIRouteCandidate{
		entity.AIPurposeNoteMarkdown:  {at(1, entity.AIProviderOpenRouter, ""), at(2, entity.AIProviderApibost, "claude-sonnet-5")},
		entity.AIPurposeImageGenerate: {at(1, entity.AIProviderOpenRouter, "openai/gpt-image-2")},
	}, testDefaults, 0)
	require.Equal(t, []string{slugAnalysis, slugChat, slugIdeas, slugIdeasFB}, rg.router.OpenRouterSlugs())
}

// TestNilLedgerAndNilRouter — a router built without a ledger records nothing and still answers; a
// nil router is a disabled one, never a panic.
func TestNilLedgerAndNilRouter(t *testing.T) {
	rg := newRig(t, nil, testDefaults, 0)
	bare := New(rg.reg, nil, map[string]aiprov.Chatter{entity.AIProviderOpenRouter: rg.or}, testDefaults, 0)
	_, err := bare.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Empty(t, rg.rows())

	var r *Router
	_, err = r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.False(t, r.Enabled(entity.AIPurposeNoteMarkdown))
	require.Empty(t, r.PrimaryProvider(entity.AIPurposeNoteMarkdown))
	require.Empty(t, r.OpenRouterSlugs())
}

// ───────────────────────── B-18 additions ─────────────────────────

// keyedChatter is a fake transport that also answers the two questions oaichat answers: is there a
// key right now, and what is the base of its call budget.
type keyedChatter struct {
	chatter
	up   bool
	base time.Duration
}

func (k *keyedChatter) Enabled() bool                 { return k.up }
func (k *keyedChatter) CompletionBase() time.Duration { return k.base }

// TestAStaticRouterReadsTheDefaults — the handler rigs build a static router whose one openrouter
// candidate names no slug, exactly like the seeded routes; WithDefaults gives it today's env slugs
// per purpose (and the Ideas kill switch), as the registry-backed router has them.
//
// MUTATION: WithDefaults returns an option that does nothing → red (nothing callable).
// MUTATION: SwitchedOff ignores the purpose (any purpose is off under IdeasOff) → red.
func TestAStaticRouterReadsTheDefaults(t *testing.T) {
	c := &chatter{}
	r := NewSingle(entity.AIProviderOpenRouter, c, "", WithDefaults(testDefaults))
	_, err := r.Chat(context.Background(), entity.AIPurposeTechCardEnhance, chatReq)
	require.NoError(t, err)
	_, err = r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, []string{slugAnalysis, slugChat}, c.models())
	require.Equal(t, slugIdeas, r.PrimaryModel(entity.AIPurposePlaygroundIdeas))

	off := testDefaults
	off.IdeasOff = true
	switchedOff := NewSingle(entity.AIProviderOpenRouter, c, "", WithDefaults(off))
	require.False(t, switchedOff.Enabled(entity.AIPurposePlaygroundIdeas))
	require.True(t, switchedOff.SwitchedOff(entity.AIPurposePlaygroundIdeas), "the door names the switch")
	require.False(t, switchedOff.SwitchedOff(entity.AIPurposeNoteMarkdown), "the switch is the Ideas door's only")
	require.False(t, r.SwitchedOff(entity.AIPurposePlaygroundIdeas))
	require.False(t, (*Router)(nil).SwitchedOff(entity.AIPurposePlaygroundIdeas))
}

// TestAKeylessTransportIsPassedOver — a transport that has no key right now (oaichat.Enabled false:
// no OPENROUTER_API_KEY, or the provider switched off in the panel) is passed over like a keyless
// provider: the purpose is not enabled, nothing is called, and the log does not call it a missing
// adapter. On a longer route the next candidate answers as the first call.
//
// MUTATION: transportUp always true → red (the keyless transport is called).
func TestAKeylessTransportIsPassedOver(t *testing.T) {
	keyless := &keyedChatter{up: false}
	r := NewSingle(entity.AIProviderOpenRouter, keyless, "m")
	var logs bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&logs, nil))
	require.False(t, r.Enabled(entity.AIPurposeNoteMarkdown))
	require.Empty(t, r.PrimaryProvider(entity.AIPurposeNoteMarkdown))
	_, err := r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Zero(t, keyless.n())
	require.NotContains(t, logs.String(), "no chat transport")

	next := &keyedChatter{up: true}
	two := NewStatic([]StaticCandidate{
		{ProviderKey: entity.AIProviderOpenRouter, Chatter: keyless, Model: "m"},
		{ProviderKey: entity.AIProviderOpenAI, Chatter: next, Model: "gpt"},
	})
	res, err := two.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, entity.AIProviderOpenAI, res.Provider)
	require.Zero(t, keyless.n())
	require.Equal(t, entity.AIProviderOpenAI, two.PrimaryProvider(entity.AIPurposeNoteMarkdown))
}

// TestTheBudgetIsTheTransportsOwnBase — a transport that reports its base (OPENROUTER_HTTP_TIMEOUT
// through oaichat.CompletionBase) bounds its calls and the lease by THAT base, not the router's: the
// lease and the wire read one field of one object, and cannot drift when the env variable is set.
//
// MUTATION: budget ignores budgetBaser (always r.budgetBase) → red (60 s base instead of 240 s).
func TestTheBudgetIsTheTransportsOwnBase(t *testing.T) {
	tr := &keyedChatter{up: true, base: 240 * time.Second}
	r := NewSingle(entity.AIProviderOpenRouter, tr, "m")
	want := aiprov.CompletionBudget(240*time.Second, 8000)
	require.Equal(t, 240*time.Second+8000*time.Second/30, want)
	require.Equal(t, want, r.ChainBudget(entity.AIPurposeNoteMarkdown, 8000))
	require.Equal(t, 2*want, r.ChainBudget(entity.AIPurposeDesignDraftIdea, 8000), "the lease: the cap's worth of THIS base")

	before := time.Now()
	_, err := r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, aiprov.ChatRequest{User: "u", MaxTokens: 8000})
	require.NoError(t, err)
	require.True(t, tr.calls[0].hasDL)
	require.InDelta(t, want.Seconds(), tr.calls[0].deadline.Sub(before).Seconds(), 1.0)
}

// TestPausedTellsTheBreakerFromMissingConfig — three transient faults open the only provider's
// breaker: the purpose is not enabled, but it is PAUSED, and Chat says so (ErrPaused inside the
// exhausted error, not ErrNotConfigured) without calling anybody. A purpose with no model is not
// paused, it is off; past the window the provider is back and nothing is paused.
//
// MUTATION: Paused always false → red.
// MUTATION: Chat's empty-route branch returns ErrNotConfigured without asking Paused → red.
func TestPausedTellsTheBreakerFromMissingConfig(t *testing.T) {
	rg := newRig(t, nil, testDefaults, 0)
	rg.or.do = fails(errStatus(entity.AIProviderOpenRouter, 503))
	for range 3 {
		_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	}
	calls := rg.or.n()
	require.False(t, rg.router.Enabled(entity.AIPurposeNoteMarkdown))
	require.True(t, rg.router.Paused(entity.AIPurposeNoteMarkdown))

	_, err := rg.router.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, ErrPaused)
	require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	require.NotErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Equal(t, calls, rg.or.n(), "a paused provider is not called")

	// Off is not paused: the same open breaker, a purpose with no slug for it.
	noSlug := testDefaults
	noSlug.Chat = ""
	off := New(rg.reg, nil, map[string]aiprov.Chatter{entity.AIProviderOpenRouter: rg.or}, noSlug, 0)
	require.False(t, off.Paused(entity.AIPurposeNoteMarkdown))
	_, err = off.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)

	rg.clk.advance(5*time.Minute + time.Second)
	require.True(t, rg.router.Enabled(entity.AIPurposeNoteMarkdown))
	require.False(t, rg.router.Paused(entity.AIPurposeNoteMarkdown))
	require.False(t, (*Router)(nil).Paused(entity.AIPurposeNoteMarkdown))
	require.False(t, NewSingle(entity.AIProviderOpenRouter, &keyedChatter{}, "m").Paused(entity.AIPurposeNoteMarkdown),
		"a static router has no breakers")
}

// TestTheNoTransportWarningIsKeyedByTheListsOwnVersion — the warn-once memory is keyed by the version
// of the snapshot the candidates came from (registry.CandidatesAt). A reload that lands while the route
// is being walked must not make the old list warn again under the new version; the next Chat, on the
// new snapshot, warns once for it.
//
// MUTATION: router.candidates reads `r.reg.Candidates(purpose)` and then `r.reg.Version()` separately →
// red (a second warning on the old list).
func TestTheNoTransportWarningIsKeyedByTheListsOwnVersion(t *testing.T) {
	ring := testRing(t)
	st := &cfgStore{Store: &aiprovtest.Store{}, cfg: config(t, ring, map[string][]entity.AIRouteCandidate{
		entity.AIPurposeNoteMarkdown: {at(1, entity.AIProviderAnthropic, "claude-sonnet-5"), at(2, entity.AIProviderOpenRouter, "")},
	})}
	clk := newClock()
	var (
		reg    *registry.Registry
		reload atomic.Bool
	)
	hooked := func() time.Time {
		if reload.CompareAndSwap(true, false) {
			st.edit(func(*entity.AIConfig) {}) // a version bump, the same routes
			require.NoError(t, reg.Reload(context.Background()))
		}
		return clk.now()
	}
	reg = registry.New(st, ring, registry.EnvKeys{OpenRouter: "env-openrouter-aaaa"}, registry.WithClock(hooked))
	require.NoError(t, reg.Reload(context.Background()))
	or := &chatter{}
	r := New(reg, nil, map[string]aiprov.Chatter{entity.AIProviderOpenRouter: or}, testDefaults, 0)
	var logs bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&logs, nil))
	warnings := func() int { return strings.Count(logs.String(), "no chat transport") }

	_, err := r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, 1, warnings())

	reload.Store(true) // the reload lands inside the next Chat's walk of the route
	_, err = r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, uint64(2), reg.Version(), "the reload did land")
	require.Equal(t, 1, warnings(), "the old list was already warned about under its own version")

	_, err = r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
	require.NoError(t, err)
	require.Equal(t, 2, warnings(), "the new version is a new chance to be told")
	require.Contains(t, logs.String(), "config_version=2")
}

// urlChatter is a transport that also names its API root, as oaichat does.
type urlChatter struct {
	keyedChatter
	url string
}

func (u *urlChatter) BaseURL() string { return u.url }

// TestRouteHeadNamesTheWouldBeCallee — a door that could not call (no key right now) still logs the
// provider and slug it WOULD have called; while something is callable, RouteHead is exactly
// PrimaryProvider / PrimaryModel, never an earlier dead row.
//
// MUTATION: RouteHead reads only the callable list → red (keyless: "", "").
// MUTATION: RouteHead reads the listed rows first → red (it names the keyless row over the live one).
func TestRouteHeadNamesTheWouldBeCallee(t *testing.T) {
	keyless := &keyedChatter{up: false}
	one := NewSingle(entity.AIProviderOpenRouter, keyless, "", WithDefaults(testDefaults))
	require.False(t, one.Enabled(entity.AIPurposeTechCardAnalysis))
	require.Empty(t, one.PrimaryModel(entity.AIPurposeTechCardAnalysis), "precondition: nothing is callable")
	p, m := one.RouteHead(entity.AIPurposeTechCardAnalysis)
	require.Equal(t, entity.AIProviderOpenRouter, p)
	require.Equal(t, slugAnalysis, m, "the default slug of the purpose is the one that would be called")

	two := NewStatic([]StaticCandidate{
		{ProviderKey: entity.AIProviderOpenRouter, Chatter: keyless, Model: "dead/row"},
		{ProviderKey: entity.AIProviderOpenAI, Chatter: &keyedChatter{up: true}, Model: "gpt-5-mini"},
	})
	p, m = two.RouteHead(entity.AIPurposeNoteMarkdown)
	require.Equal(t, two.PrimaryProvider(entity.AIPurposeNoteMarkdown), p)
	require.Equal(t, "gpt-5-mini", m)

	off := testDefaults
	off.Ideas, off.IdeasOff = "", true
	p, m = NewSingle(entity.AIProviderOpenRouter, keyless, "", WithDefaults(off)).RouteHead(entity.AIPurposePlaygroundIdeas)
	require.Empty(t, p+m, "the kill switch names nothing")
	p, m = (*Router)(nil).RouteHead(entity.AIPurposeNoteMarkdown)
	require.Empty(t, p+m)
}

// TestBaseURLNamesTheTransportsRoot — the analysis log's base_url comes from the transport of the
// provider that was (or would be) called, on a static route and on the registry's.
//
// MUTATION: BaseURL returns "" → red. MUTATION: the static branch reads r.transports → red.
func TestBaseURLNamesTheTransportsRoot(t *testing.T) {
	st := NewSingle(entity.AIProviderOpenRouter, &urlChatter{url: "https://static.example/v1"}, "m")
	require.Equal(t, "https://static.example/v1", st.BaseURL(entity.AIProviderOpenRouter))
	require.Empty(t, st.BaseURL(entity.AIProviderOpenAI), "no candidate of that provider")

	rg := newRig(t, nil, testDefaults, 0)
	reg := New(rg.reg, nil, map[string]aiprov.Chatter{
		entity.AIProviderOpenRouter: &urlChatter{url: "https://or.example/api/v1"},
		entity.AIProviderOpenAI:     &chatter{},
	}, testDefaults, 0)
	require.Equal(t, "https://or.example/api/v1", reg.BaseURL(" openrouter "))
	require.Empty(t, reg.BaseURL(entity.AIProviderOpenAI), "a transport that does not say")
	require.Empty(t, (*Router)(nil).BaseURL(entity.AIProviderOpenRouter))
}

// TestTheSameSlugTwiceIsNotAFallback — two rows naming the same provider and slug in effect (a ""
// model resolved through the purpose's default on one side; the seeded Ideas route when
// OPENROUTER_MODEL_IDEAS names the fallback slug itself) make ONE call: on a refusal the second would
// repeat it word for word, after a D-16 engaged timeout it would pay twice. The lease counts it once.
//
// MUTATION: Chat without the tried-set → red (two calls). MUTATION: callable without the listed-set →
// red (ChainBudget counts two calls).
func TestTheSameSlugTwiceIsNotAFallback(t *testing.T) {
	t.Run("a refusal: the default resolved to the fallback slug", func(t *testing.T) {
		c := &keyedChatter{up: true, base: time.Minute}
		c.do = fails(errStatus(entity.AIProviderOpenRouter, 404))
		d := testDefaults
		d.Ideas = slugIdeasFB
		r := NewStatic([]StaticCandidate{
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: c},                     // '' → the default = slugIdeasFB
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: c, Model: slugIdeasFB}, // the seeded position-2 row
		}, WithDefaults(d))
		_, err := r.Chat(context.Background(), entity.AIPurposePlaygroundIdeas, chatReq)
		require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
		require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
		require.Equal(t, []string{slugIdeasFB}, c.models(), "the same slug is called once")
		require.Equal(t, aiprov.CompletionBudget(time.Minute, 300), r.ChainBudget(entity.AIPurposePlaygroundIdeas, 300),
			"the lease counts the repeat once")
	})
	t.Run("a D-16 engaged timeout: the same pair is not paid twice", func(t *testing.T) {
		hung := &keyedChatter{up: true, base: time.Minute}
		hung.do = fails(errEngagedTimeout(entity.AIProviderOpenRouter))
		r := NewStatic([]StaticCandidate{
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: hung},                  // '' → the default Chat slug
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: hung, Model: slugChat}, // that very slug, by name
		}, WithDefaults(testDefaults))
		_, err := r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, chatReq)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, []string{slugChat}, hung.models(), "a hung call is not repeated on the same provider and slug")
	})
	t.Run("a different slug on the same provider IS a fallback", func(t *testing.T) {
		c := &keyedChatter{up: true, base: time.Minute}
		c.do = fails(errStatus(entity.AIProviderOpenRouter, 404))
		r := NewStatic([]StaticCandidate{
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: c},
			{ProviderKey: entity.AIProviderOpenRouter, Chatter: c, Model: slugIdeasFB},
		}, WithDefaults(testDefaults))
		_, err := r.Chat(context.Background(), entity.AIPurposePlaygroundIdeas, chatReq)
		require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
		require.Equal(t, []string{slugIdeas, slugIdeasFB}, c.models())
		require.Equal(t, 2*aiprov.CompletionBudget(time.Minute, 300), r.ChainBudget(entity.AIPurposePlaygroundIdeas, 300))
	})
}

// TestALeasedPurposeIsSizedForTheCap (FIX-G1) — the draft-idea lease covers the chain that CAN run,
// not the chain callable when the lease is sized: cap (two) × the longest single call any candidate
// could make — a candidate callable now, any transport the router holds (a route edit can add a
// candidate on it between the lease and the call), or one call on the router's own base.
//
// MUTATION: the leased branch returns one call's worth (no cap multiplication) → red.
// MUTATION: the leased branch ignores heldTransports → red (the 240 s transport not on the route).
func TestALeasedPurposeIsSizedForTheCap(t *testing.T) {
	const draft = entity.AIPurposeDesignDraftIdea
	// One callable candidate, one held transport: two calls' worth anyway.
	single := NewSingle(entity.AIProviderOpenRouter, &keyedChatter{up: true, base: time.Minute}, "m")
	one := aiprov.CompletionBudget(time.Minute, 8000)
	require.Equal(t, 2*one, single.ChainBudget(draft, 8000), "one callable candidate still leases the cap")
	require.Equal(t, one, single.ChainBudget(entity.AIPurposeNoteMarkdown, 8000), "an unleased purpose: the chain seen")

	// Registry-backed: only openrouter (60 s) is on the draft route, but the router also holds an
	// openai transport whose calls run on 240 s — an owner can put it on the route before Chat runs.
	rg := newRig(t, map[string][]entity.AIRouteCandidate{
		draft: {at(1, entity.AIProviderOpenRouter, "")},
	}, testDefaults, time.Minute)
	held := New(rg.reg, nil, map[string]aiprov.Chatter{
		entity.AIProviderOpenRouter: &keyedChatter{up: true, base: time.Minute},
		entity.AIProviderOpenAI:     &keyedChatter{up: true, base: 240 * time.Second},
	}, testDefaults, time.Minute)
	require.Equal(t, 2*aiprov.CompletionBudget(240*time.Second, 8000), held.ChainBudget(draft, 8000),
		"the longest call is the one on the slowest transport the route could gain")

	// Nothing callable now (keyless): still the cap's worth on the router's own base, never zero.
	keyless := NewSingle(entity.AIProviderOpenRouter, &keyedChatter{up: false}, "m")
	require.Equal(t, 2*aiprov.CompletionBudget(0, 8000), keyless.ChainBudget(draft, 8000))
}

// TestRouteHeadNamesAKeylessConfiguredRoute (FIX-G3) — on the REGISTRY-backed router (the one app.go
// builds), a provider with no key is dropped before the router sees the candidates; RouteHead still
// names the configured head (registry.RouteHeadAt) resolved through the defaults, and BaseURL names
// its transport's root — what the analysis' no-key answer and log report.
//
// MUTATION (measured red): RouteHead without the RouteHeadAt fallback (the filtered list only) → "", "".
func TestRouteHeadNamesAKeylessConfiguredRoute(t *testing.T) {
	ring := testRing(t)
	st := &cfgStore{Store: &aiprovtest.Store{}, cfg: config(t, ring, nil)}
	reg := registry.New(st, ring, registry.EnvKeys{}) // no OPENROUTER_API_KEY, no key saved in the panel
	require.NoError(t, reg.Reload(context.Background()))
	r := New(reg, nil, map[string]aiprov.Chatter{
		entity.AIProviderOpenRouter: &urlChatter{keyedChatter: keyedChatter{up: false}, url: "https://or.example/api/v1"},
	}, testDefaults, 0)

	require.Empty(t, reg.Candidates(entity.AIPurposeTechCardAnalysis), "precondition: the registry drops the keyless provider")
	require.False(t, r.Enabled(entity.AIPurposeTechCardAnalysis))
	require.Empty(t, r.PrimaryModel(entity.AIPurposeTechCardAnalysis))

	p, m := r.RouteHead(entity.AIPurposeTechCardAnalysis)
	require.Equal(t, entity.AIProviderOpenRouter, p)
	require.Equal(t, slugAnalysis, m, "the seeded row names no model: the analysis default is the would-be slug")
	require.Equal(t, "https://or.example/api/v1", r.BaseURL(p))

	off := testDefaults
	off.Ideas, off.IdeasOff = "", true
	p, m = New(reg, nil, nil, off, 0).RouteHead(entity.AIPurposePlaygroundIdeas)
	require.Empty(t, p+m, "the kill switch still names nothing")
}

// TestTheNoTransportWarningIsMonotonic (FIX-G4) — concurrent Chats bring their snapshots' versions out
// of order: B warns at v2, then A (which read v1 before the reload and paused) resumes. A must not
// warn, and must not pull the memory back to v1 — the next v2 Chat would warn a second time. A newer
// version still warns.
//
// MUTATION (measured red): plain last-seen (`seen && v == version`, then overwrite) → A warns at v1 and
// the next v2 warns again.
func TestTheNoTransportWarningIsMonotonic(t *testing.T) {
	r := NewSingle(entity.AIProviderOpenRouter, &chatter{}, "m")
	var logs bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&logs, nil))
	warnings := func() int { return strings.Count(logs.String(), "no chat transport") }
	ctx := context.Background()

	r.warnNoTransport(ctx, entity.AIProviderAnthropic, entity.AIPurposeNoteMarkdown, 2) // B, after the reload
	require.Equal(t, 1, warnings())
	r.warnNoTransport(ctx, entity.AIProviderAnthropic, entity.AIPurposeNoteMarkdown, 1) // A resumes on its old list
	require.Equal(t, 1, warnings(), "an older version than the one warned at is not news")
	r.warnNoTransport(ctx, entity.AIProviderAnthropic, entity.AIPurposeNoteMarkdown, 2) // the next v2 Chat
	require.Equal(t, 1, warnings(), "the memory was not pulled back to v1")
	r.warnNoTransport(ctx, entity.AIProviderAnthropic, entity.AIPurposeNoteMarkdown, 3)
	require.Equal(t, 2, warnings(), "a newer version is a new chance to be told")
	r.warnNoTransport(ctx, entity.AIProviderApibost, entity.AIPurposeNoteMarkdown, 1)
	require.Equal(t, 3, warnings(), "the memory is per provider")
}

// wireChatter is a transport that fills the ceiling itself when the caller names none (anthropic).
type wireChatter struct {
	keyedChatter
	wire int
}

func (w *wireChatter) WireCeiling(maxTokens int) int {
	if maxTokens <= 0 {
		return w.wire
	}
	return maxTokens
}

// TestTheBudgetIsTheWireCeiling (Codex REVIEW-E #2) — a transport that puts DefaultMaxTokens on the
// wire when the caller names no ceiling (anthropic) is budgeted and leased for THAT ceiling: the
// deadline the router grants is the one the wire buys. A caller's own ceiling is unchanged, and a
// transport that fills none is budgeted from the caller's number as before.
//
// MUTATION: chatterBudget ignores wireCeilinger → red (the base alone for a caller ceiling of 0).
func TestTheBudgetIsTheWireCeiling(t *testing.T) {
	tr := &wireChatter{keyedChatter: keyedChatter{up: true, base: time.Minute}, wire: 4096}
	r := NewSingle(entity.AIProviderAnthropic, tr, "m")
	want := aiprov.CompletionBudget(time.Minute, 4096)
	require.Equal(t, time.Minute+4096*time.Second/30, want)
	require.Equal(t, want, r.ChainBudget(entity.AIPurposeNoteMarkdown, 0), "no caller ceiling: the wire's")
	require.Equal(t, 2*want, r.ChainBudget(entity.AIPurposeDesignDraftIdea, 0), "the lease: the cap's worth of the wire's ceiling")
	require.Equal(t, aiprov.CompletionBudget(time.Minute, 8000), r.ChainBudget(entity.AIPurposeNoteMarkdown, 8000),
		"the caller's own ceiling wins")

	before := time.Now()
	_, err := r.Chat(context.Background(), entity.AIPurposeNoteMarkdown, aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	require.True(t, tr.calls[0].hasDL)
	require.InDelta(t, want.Seconds(), tr.calls[0].deadline.Sub(before).Seconds(), 1.0)

	plain := NewSingle(entity.AIProviderOpenRouter, &keyedChatter{up: true, base: time.Minute}, "m")
	require.Equal(t, aiprov.CompletionBudget(time.Minute, 0), plain.ChainBudget(entity.AIPurposeNoteMarkdown, 0),
		"a transport that fills no ceiling: the caller's zero, the base alone")
}
