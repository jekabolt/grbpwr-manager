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
	return &aiprov.CallError{Provider: pk, Code: codeTimeout, Engaged: true,
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
	require.Equal(t, codeTimeout, rows[0].End.ErrorCode)
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
	require.Equal(t, codeTimeout, ce.Code)
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
	require.Contains(t, err.Error(), "circuit breaker")
	require.Equal(t, calls, rg.or.n())
}

// TestChatSkipsAProviderWithNoTransport — the owner enabled a provider this build has no adapter for
// (anthropic, first on the route): it is passed over, the next candidate answers as the FIRST call,
// and the log says so once per provider per config version — not once per press.
//
// MUTATION: the no-transport branch returns ErrNotConfigured instead of continuing → red.
// MUTATION: warnNoTransport without its seen/version check → red (two lines for one version).
func TestChatSkipsAProviderWithNoTransport(t *testing.T) {
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
// the purpose: apibost with no model has no default (the Defaults are OpenRouter's slugs), so the
// openrouter candidate after it answers with its default; with no default either, the purpose is
// not configured and nothing is called.
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
		entity.AIPurposeTechCardOperationsDraft: slugChat,
		entity.AIPurposeTechCardEnhance:         slugAnalysis,
		entity.AIPurposeTechCardAnalysis:        slugAnalysis,
		entity.AIPurposeNoteMarkdown:            slugChat,
		entity.AIPurposeEmailTranslate:          slugChat,
		entity.AIPurposeDesignDraftIdea:         slugChat,
		entity.AIPurposePlaygroundIdeas:         slugIdeas,
		entity.AIPurposeImageGenerate:           "",
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
// MUTATION: ChainBudget without the chainCap clamp → red (3× on draft-idea).
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
