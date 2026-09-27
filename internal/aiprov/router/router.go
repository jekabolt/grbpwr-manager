// Package router is the provider-neutral chat door: Router.Chat(ctx, purpose, req) answers one chat
// purpose from the candidates the registry routes it to, falls back only where no money moved, and
// books ONE ledger row per physical call. A handler names a purpose and builds an aiprov.ChatRequest;
// it never learns which provider answered unless it reads ChatResult.Provider.
//
// ONE SNAPSHOT PER CALL. Chat asks registry.Candidates once and walks THAT list to the end: a reload
// in the middle of a chain changes the next Chat, never the one in flight, so a chain can neither
// skip a candidate nor try one twice because the owner saved a route while it ran.
//
// CANDIDATES IN ORDER, AND FIVE REASONS TO PASS ONE BY WITHOUT A CALL. The registry already dropped
// the disabled, the keyless and the breaker-open. The router passes over, without a row: a provider
// with no chat transport in this build (the owner enabled it before its adapter exists — one WARN per
// provider per config version, not one per press); a transport that reports no key right now (a
// static route has no registry to drop it); a candidate whose effective model is "" (the
// purpose is switched off for it — OPENROUTER_MODEL_IDEAS=off); one whose breaker refuses Admit
// (half-open, its one probe already out); and, for a purpose under a handler lease, every candidate
// past the chain cap (chainCap — the lease is sized for that many calls and no more).
//
// A ROW BEFORE EVERY PHYSICAL CALL. Ledger.Begin runs BEFORE the transport is called and Finish after
// it, whatever happened: a process that dies between the two leaves a `dispatching` row the sweeper
// turns into `unknown` — the money is never silent (aiprov.Ledger).
//
// THE TRANSPORT DECIDES ENGAGED; THE ROUTER DECIDES "NEXT OR TERMINAL". Only the transport saw whether
// the request reached the wire (httptrace WroteRequest), so the router reads CallError.Engaged and
// never the error's prose. The whole matrix, one physical call per line:
//
//	outcome                                  row             breaker end        then
//	answer                                   ok, priced      RecordSuccess      return the answer
//	failure, not engaged (any status/code)   free, $0        RecordFailure ¹    next candidate
//	failure, engaged, timeout ² (chat.*)     unknown ³       RecordFailure ¹    next candidate (D-16)
//	failure, engaged, anything else          unknown ³       RecordFailure ¹    TERMINAL: return it
//	failure that is not a CallError          unknown ³       RecordFailure ¹    TERMINAL (engaged assumed)
//	every candidate failed or was skipped    —               —                  ErrAllCandidatesFailed wrapping the last
//	nothing called: every provider held by   —               —                  ErrAllCandidatesFailed wrapping ErrPaused
//	  its breaker (open, or probe out)
//	no candidate at all / none callable      —               —                  aiprov.ErrNotConfigured
//
//	¹ the registry's one rule decides what counts: only a Retryable, NOT engaged CallError is a
//	  breaker fault (429, 5xx, a hiccup before the write); a 401/402/404/422 and every engaged or
//	  unclassified failure merely END the admission (a half-open probe is released, never counted).
//	² CallError.Code "timeout", or the router's own call budget expired: a provider that HANGS after
//	  our request was written. D-16 (owner, O-05): for chat purposes the next candidate may try —
//	  chat calls cost cents and a hang is the commonest way to be "down"; never for images or 3D.
//	³ `charged_failed` instead when the transport handed back a partial result that prices (an
//	  empty answer after the model spent its budget: the tokens are known, so the money is booked,
//	  not left unknown).
//
// WHY AN ENGAGED FAILURE IS TERMINAL. Money may have moved; the next candidate would bill a second
// time for the same answer. Over-answering "engaged" costs a fallback, under-answering costs a second
// payment — so a failure that does not say (no CallError) is treated as engaged.
//
// THE CALLER'S CONTEXT IS HONOURED BETWEEN CANDIDATES. Each call runs under the caller's context
// bounded by aiprov.CompletionBudget(budgetBase, req.MaxTokens) — the router's own bound is what lets
// a hang be told apart as THIS CALL's timeout (advance) rather than the caller's (stop). Once the
// caller's context is done no further candidate is tried; the last error is returned.
//
// This package imports the registry and the ledger, and no transport: transports come in as
// aiprov.Chatter values keyed by provider (oaichat for openrouter today; openai and apibost in
// commit E).
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ErrPaused is what Chat wraps (inside aiprov.ErrAllCandidatesFailed) when nothing was called because
// every provider that could serve the purpose is held by its circuit breaker — open after repeated
// failures, or half-open with its one probe already out. It is weather that passes by itself in
// minutes, and a door must not read it as «AI is not configured».
var ErrPaused = errors.New("ai: the providers of this purpose are paused after repeated failures")

// leasedChainCap is how many physical calls ONE Chat may make for a purpose whose handler holds a
// lease over the call (draft-idea: design_run's HandlerLease). The lease is sized by ChainBudget
// BEFORE the call, and a lease that expires while a paid call is still running lets a retry of the
// same client_request_id pay a second time — so the chain the lease was sized for is also the most
// the chain may run: primary + one fallback (02-PLAN A2).
const leasedChainCap = 2

// chainCap is the most physical calls one Chat may make for purpose; 0 = no cap.
func chainCap(purpose string) int {
	if purpose == entity.AIPurposeDesignDraftIdea {
		return leasedChainCap
	}
	return 0
}

// isChatPurpose — the router serves chat purposes only; an image purpose's candidates are image
// providers and must never receive a chat request.
func isChatPurpose(purpose string) bool {
	return entity.AIPurposeCapability(purpose) == entity.AICapabilityChat
}

// advancesAfterEngagedTimeout is D-16: an engaged timeout moves on to the next candidate for chat
// purposes only. Written as its own rule so the day this router serves a paid image purpose the
// answer for it is already "no".
func advancesAfterEngagedTimeout(purpose string) bool { return isChatPurpose(purpose) }

// Defaults is the per-purpose default slug table for a candidate whose route row names no model
// (an empty ai_route.model): today's env semantics, built in app.go from cfg.OpenRouter — Chat =
// Model(), Analysis = AnalysisModel(), Ideas = IdeasModel(), IdeasOff = OPENROUTER_MODEL_IDEAS is
// `off`. They are OPENROUTER slugs and answer only for openrouter candidates; another provider with
// no model has no default yet (commit E) and is passed over.
//
// IdeasOff is the kill switch of the whole `Ideas ▾` door, not only of its default: with it set no
// candidate of chat.playground_ideas is called, the seeded position-2 row with its own slug included
// — today `off` means neither the slug nor its fallback is ever called, and that must survive the
// fallback becoming a route row.
type Defaults struct {
	Chat, Analysis, Ideas string
	IdeasOff              bool
}

// slug is the default for purpose: Analysis for the two tech-card analysis purposes, Ideas for the
// playground door, Chat for every other chat purpose, "" for anything that is not a chat purpose.
func (d Defaults) slug(purpose string) string {
	switch purpose {
	case entity.AIPurposeTechCardEnhance, entity.AIPurposeTechCardAnalysis:
		return strings.TrimSpace(d.Analysis)
	case entity.AIPurposePlaygroundIdeas:
		return strings.TrimSpace(d.Ideas)
	}
	if isChatPurpose(purpose) {
		return strings.TrimSpace(d.Chat)
	}
	return ""
}

// StaticCandidate is one entry of a fixed route (NewStatic): a provider key, the transport that
// serves it and the slug it is called with.
type StaticCandidate struct {
	ProviderKey string
	Chatter     aiprov.Chatter
	Model       string
}

// Option configures a Router.
type Option func(*Router)

// WithClock replaces time.Now as the clock the router measures a call's latency with. Tests only.
func WithClock(f func() time.Time) Option {
	return func(r *Router) {
		if f != nil {
			r.clock = f
		}
	}
}

// WithDefaults sets the default slug table — the seam a static router (NewSingle / NewStatic) needs
// to answer a candidate with no model the way the registry-backed one does in production.
func WithDefaults(d Defaults) Option {
	return func(r *Router) { r.defaults = d }
}

// budgetBaser is a transport that knows the base of its own call budget (oaichat.Client.CompletionBase:
// the OPENROUTER_HTTP_TIMEOUT it bounds every request with).
type budgetBaser interface{ CompletionBase() time.Duration }

// enabler is a transport that knows whether it has a key right now (oaichat.Client.Enabled).
type enabler interface{ Enabled() bool }

// transportUp — a transport that says it has no key is passed over like a keyless provider: the
// registry has already dropped those for a registry-backed route (the transport reads the same key
// func), and a static route has no registry to do it.
func transportUp(c aiprov.Chatter) bool {
	if e, ok := c.(enabler); ok {
		return e.Enabled()
	}
	return true
}

// Router — see the package doc. Safe for concurrent use; everything but the warn-once memory is
// fixed at construction.
type Router struct {
	reg        *registry.Registry
	ledger     *aiprov.Ledger
	transports map[string]aiprov.Chatter
	// static is the fixed route of every chat purpose (NewSingle / NewStatic); nil = the registry's.
	static     []StaticCandidate
	defaults   Defaults
	budgetBase time.Duration
	clock      func() time.Time
	log        *slog.Logger

	warnMu sync.Mutex
	warned map[string]uint64 // provider key → the config version its missing transport was warned at
}

// New builds the router over the registry's routes. ledger may be nil (nothing is recorded);
// transports maps a provider key to its chat transport; budgetBase is the BASE of a call's time
// budget (aiprov.CompletionBudget; <= 0 = the default base) for a transport that does not report its
// own — one that does (oaichat: CompletionBase) is bounded by ITS base, so the router's bound, the
// transport's bound and ChainBudget's lease read one field of one object.
func New(reg *registry.Registry, ledger *aiprov.Ledger, transports map[string]aiprov.Chatter,
	defaults Defaults, budgetBase time.Duration, opts ...Option) *Router {
	t := make(map[string]aiprov.Chatter, len(transports))
	for k, c := range transports {
		if c != nil {
			t[strings.TrimSpace(k)] = c
		}
	}
	r := &Router{
		reg:        reg,
		ledger:     ledger,
		transports: t,
		defaults:   defaults,
		budgetBase: budgetBase,
		clock:      time.Now,
		log:        slog.Default(),
		warned:     map[string]uint64{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// NewSingle is a router with exactly one fixed candidate for every chat purpose — no registry, no
// ledger, no breaker. The seam the admin handler test rigs use in place of a registry.
func NewSingle(providerKey string, chatter aiprov.Chatter, model string, opts ...Option) *Router {
	return NewStatic([]StaticCandidate{{ProviderKey: providerKey, Chatter: chatter, Model: model}}, opts...)
}

// NewStatic is a router over a fixed, ordered candidate list shared by every chat purpose — no
// registry, no ledger, no breaker; each candidate brings its own transport (two entries may share a
// provider key with different slugs, as the playground door's seeded route does).
func NewStatic(cands []StaticCandidate, opts ...Option) *Router {
	r := New(nil, nil, nil, Defaults{}, 0, opts...)
	r.static = make([]StaticCandidate, 0, len(cands))
	for _, c := range cands {
		c.ProviderKey = strings.TrimSpace(c.ProviderKey)
		c.Model = strings.TrimSpace(c.Model)
		r.static = append(r.static, c)
	}
	return r
}

func (r *Router) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Now()
}

// candidate is one route entry as this router will try it: the registry's candidate and its
// transport (nil = no transport for that provider in this build).
type candidate struct {
	registry.Candidate
	chatter aiprov.Chatter
}

// candidates is the purpose's route as ONE snapshot, in order, with the config version of THAT
// snapshot (registry.CandidatesAt — one load for both), 0 for a static router.
func (r *Router) candidates(purpose string) ([]candidate, uint64) {
	if r == nil || !isChatPurpose(purpose) {
		return nil, 0
	}
	if r.static != nil {
		out := make([]candidate, 0, len(r.static))
		for i, s := range r.static {
			out = append(out, candidate{
				Candidate: registry.Candidate{ProviderKey: s.ProviderKey, Model: s.Model, Position: i + 1},
				chatter:   s.Chatter,
			})
		}
		return out, 0
	}
	if r.reg == nil {
		return nil, 0
	}
	regCands, version := r.reg.CandidatesAt(purpose) // one snapshot: the list and the version it came from
	out := make([]candidate, 0, len(regCands))
	for _, c := range regCands {
		out = append(out, candidate{Candidate: c, chatter: r.transports[c.ProviderKey]})
	}
	return out, version
}

// callable is the part of the route Chat would actually call, before the breaker has its say: a
// transport that is up and a model. What Enabled, PrimaryProvider, ChainBudget and OpenRouterSlugs read.
//
// A provider and slug already listed higher up is not listed again (callKey): Chat never calls the
// same pair twice in one chain, so the lease and the band must not count it either.
func (r *Router) callable(purpose string) []candidate {
	cands, _ := r.candidates(purpose)
	out := cands[:0]
	listed := map[string]bool{}
	for _, c := range cands {
		if !r.canCall(purpose, c) {
			continue
		}
		key := callKey(c.ProviderKey, r.EffectiveModel(purpose, c.Candidate))
		if listed[key] {
			continue
		}
		listed[key] = true
		out = append(out, c)
	}
	return out
}

// callKey is what makes two route rows THE SAME CALL: the provider and the slug. The seeded Ideas
// route is openrouter + its default slug, then openrouter + openai/gpt-5-mini — and when
// OPENROUTER_MODEL_IDEAS names gpt-5-mini itself, the second row would repeat the first call's
// refusal word for word (the handler's retry before B-18 skipped exactly that case).
func callKey(providerKey, model string) string { return providerKey + "\x00" + model }

func (r *Router) canCall(purpose string, c candidate) bool {
	return c.chatter != nil && transportUp(c.chatter) && r.EffectiveModel(purpose, c.Candidate) != ""
}

// budget is ONE call's time budget on candidate c: aiprov.CompletionBudget over the transport's own
// base when it reports one, else over the router's.
func (r *Router) budget(c candidate, maxTokens int) time.Duration {
	base := r.budgetBase
	if b, ok := c.chatter.(budgetBaser); ok {
		base = b.CompletionBase()
	}
	return aiprov.CompletionBudget(base, maxTokens)
}

// Paused reports that purpose has nothing to call ONLY because its breakers hold it: Enabled is
// false, and at least one provider that WOULD be called (a transport that is up, a model) is dropped
// by an open breaker. The door that refuses then says «paused after repeated failures, try again in a
// few minutes» instead of «not configured». Always false for a static router (no breakers). Nil-safe.
func (r *Router) Paused(purpose string) bool {
	if r == nil || r.reg == nil || !isChatPurpose(purpose) || r.Enabled(purpose) {
		return false
	}
	for _, h := range r.reg.BreakerHeld(purpose) {
		if r.canCall(purpose, candidate{Candidate: h, chatter: r.transports[h.ProviderKey]}) {
			return true
		}
	}
	return false
}

// EffectiveModel is the slug candidate is called with for purpose: the route row's own model when it
// names one, else the Defaults slug for the purpose (openrouter candidates only — see Defaults); ""
// means the purpose is switched off for this candidate. Every candidate of chat.playground_ideas
// answers "" while Defaults.IdeasOff is set.
func (r *Router) EffectiveModel(purpose string, c registry.Candidate) string {
	var d Defaults
	if r != nil {
		d = r.defaults
	}
	if purpose == entity.AIPurposePlaygroundIdeas && d.IdeasOff {
		return ""
	}
	if m := strings.TrimSpace(c.Model); m != "" {
		return m
	}
	if strings.TrimSpace(c.ProviderKey) != entity.AIProviderOpenRouter {
		return ""
	}
	return d.slug(purpose)
}

// SwitchedOff reports that purpose is closed by an env kill switch — today only
// OPENROUTER_MODEL_IDEAS=off (Defaults.IdeasOff) for chat.playground_ideas — so the door can name the
// switch instead of calling the purpose unconfigured or paused. Nil-safe.
func (r *Router) SwitchedOff(purpose string) bool {
	return r != nil && purpose == entity.AIPurposePlaygroundIdeas && r.defaults.IdeasOff
}

// Enabled reports whether purpose has at least one candidate with a transport and a model — the
// door every handler's "AI is not configured" refusal asks. Nil-safe.
func (r *Router) Enabled(purpose string) bool { return len(r.callable(purpose)) > 0 }

// PrimaryProvider is the provider key Chat would call first for purpose (the first candidate with a
// transport and a model), "" when none — what the draft-idea attempt row names before the call.
func (r *Router) PrimaryProvider(purpose string) string {
	if c := r.callable(purpose); len(c) > 0 {
		return c[0].ProviderKey
	}
	return ""
}

// PrimaryModel is the slug Chat would call first for purpose, "" when none: EffectiveModel of the
// candidate PrimaryProvider names. For the doors that show or name the model before any answer
// exists (the suggest band's model, a refusal sentence when no ChatResult came back).
func (r *Router) PrimaryModel(purpose string) string {
	if c := r.callable(purpose); len(c) > 0 {
		return r.EffectiveModel(purpose, c[0].Candidate)
	}
	return ""
}

// RouteHead names the candidate a door would call first: PrimaryProvider / PrimaryModel while
// something is callable, else the first candidate of the route that has a model, callable or not (no
// key right now) — so the line of a door that could not call still names the provider and the slug it
// WOULD have called. "", "" when the route names nothing (a registry drops a keyless provider before
// the router sees it; the Ideas kill switch empties every slug). Nil-safe.
func (r *Router) RouteHead(purpose string) (providerKey, model string) {
	if c := r.callable(purpose); len(c) > 0 {
		return c[0].ProviderKey, r.EffectiveModel(purpose, c[0].Candidate)
	}
	cands, _ := r.candidates(purpose)
	for _, c := range cands {
		if m := r.EffectiveModel(purpose, c.Candidate); m != "" {
			return c.ProviderKey, m
		}
	}
	return "", ""
}

// baseURLer is a transport that knows its API root (oaichat.Client.BaseURL).
type baseURLer interface{ BaseURL() string }

// BaseURL is the API root providerKey's transport calls, "" when it does not say. For LOG LINES only:
// a 404 "model not found" can mean a dead slug OR a base URL pointing nowhere, and a line that names
// only the slug sends the reader to the wrong knob. Nil-safe.
func (r *Router) BaseURL(providerKey string) string {
	if r == nil {
		return ""
	}
	providerKey = strings.TrimSpace(providerKey)
	var c aiprov.Chatter
	if r.static != nil {
		for _, s := range r.static {
			if s.ProviderKey == providerKey {
				c = s.Chatter
				break
			}
		}
	} else {
		c = r.transports[providerKey]
	}
	if b, ok := c.(baseURLer); ok {
		return b.BaseURL()
	}
	return ""
}

// ChainBudget is the longest a Chat for purpose may run with an answer ceiling of maxTokens: the sum
// of each candidate's call budget (budget: its transport's base) over the candidates it may call —
// capped at leasedChainCap for a purpose under a handler lease (Chat stops there too, so the lease
// and the chain are the same number), and never less than ONE call: a route that gains a candidate
// between the lease and the call must not find a lease of zero. design.HandlerLeaseFor takes it.
//
// ⚠ ONE SNAPSHOT EACH, AND THE GAP BETWEEN THEM IS KNOWN. The lease is sized from the route as it
// stands now and Chat reads the route again when it runs; a callable candidate that APPEARS in
// between (a route edit, or a breaker whose open window ends, in the handler's pre-call seconds) can
// make the chain one call longer than the lease — up to the cap. Sizing every leased purpose at the
// cap would close it, and would also double today's one-candidate draft-idea lease (a dead handler's
// row blocks the honest retry that much longer): a behaviour change commit C does not make.
func (r *Router) ChainBudget(purpose string, maxTokens int) time.Duration {
	chain := r.callable(purpose)
	if limit := chainCap(purpose); limit > 0 && len(chain) > limit {
		chain = chain[:limit]
	}
	if len(chain) == 0 {
		// Nothing callable now: one call's worth on the router's own base, never zero.
		var base time.Duration
		if r != nil {
			base = r.budgetBase
		}
		return aiprov.CompletionBudget(base, maxTokens)
	}
	var sum time.Duration
	for _, c := range chain {
		sum += r.budget(c, maxTokens)
	}
	return sum
}

// OpenRouterSlugs lists every slug an openrouter candidate of a chat purpose is called with, sorted
// and without repeats — what openrouter.WarnIfRetired probes at boot.
func (r *Router) OpenRouterSlugs() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range entity.AIPurposes() {
		for _, c := range r.callable(p) {
			if c.ProviderKey != entity.AIProviderOpenRouter {
				continue
			}
			if m := r.EffectiveModel(p, c.Candidate); m != "" && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ───────────────────────── Chat ─────────────────────────

// Chat answers purpose with req from the purpose's candidates, in route order — the package doc has
// the whole matrix. It returns:
//   - the answer (Provider, Model, Engaged always set) and nil;
//   - on a terminal engaged failure, the transport's error as it came — and the partial result the
//     transport handed back with it, if any (an empty answer still carries its usage);
//   - when every candidate failed before engaging (or was passed over after one did), an error that
//     Is aiprov.ErrAllCandidatesFailed and wraps the LAST call's error, so errors.Is(err,
//     aiprov.ErrModelUnavailable) and AsCallError still answer the handlers' doors; if an earlier call
//     of the chain engaged (D-16) that error is wrapped too, so aiprov.Engaged(err) stays true;
//   - aiprov.ErrNotConfigured when the purpose has no callable candidate; ErrAllCandidatesFailed
//     wrapping ErrPaused when the only ones there are held by their breakers (open, or refused
//     admission while a probe is out);
//   - the caller's context error when it was done before the first call.
func (r *Router) Chat(ctx context.Context, purpose string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cands, version := r.candidates(purpose)
	if len(cands) == 0 {
		if r.Paused(purpose) {
			return nil, fmt.Errorf("%w: %w", aiprov.ErrAllCandidatesFailed, ErrPaused)
		}
		return nil, aiprov.ErrNotConfigured
	}
	limit := chainCap(purpose)

	var (
		calls        int    // physical calls made so far; the next one's call_no is calls+1
		prev         string // provider key of the previous physical call: the next row's fallback_from
		last         error  // the previous physical call's error
		firstEngaged error  // the first engaged failure the chain moved past (D-16)
		refused      string // a provider whose breaker refused admission, when nothing else was called
	)
	tried := map[string]bool{} // callKey of every physical call so far
	for _, c := range cands {
		// The caller's context, between candidates: once it is done nothing more is tried.
		if err := ctx.Err(); err != nil {
			if last == nil {
				return nil, err
			}
			return nil, withEarlierEngagement(last, firstEngaged)
		}
		if limit > 0 && calls >= limit {
			break
		}
		if c.chatter == nil {
			r.warnNoTransport(ctx, c.ProviderKey, purpose, version)
			continue
		}
		if !transportUp(c.chatter) {
			continue // keyless: a configuration state, not a missing adapter — no warning
		}
		model := r.EffectiveModel(purpose, c.Candidate)
		if model == "" || tried[callKey(c.ProviderKey, model)] {
			continue // no slug, or the same provider and slug again: a repeat, not a fallback
		}
		adm, admitted := r.admit(c.ProviderKey)
		if !admitted {
			refused = c.ProviderKey
			continue
		}

		tried[callKey(c.ProviderKey, model)] = true
		calls++
		res, ownDeadline, err := r.call(ctx, purpose, c, model, adm, calls, prev, r.budget(c, req.MaxTokens), req)
		prev = c.ProviderKey
		if err == nil {
			return res, nil
		}
		last = err
		ce, isCE := aiprov.AsCallError(err)
		if !isCE || aiprov.Engaged(err) {
			// Engaged (or unclassified, which is read as engaged): money may have moved.
			hung := ownDeadline || (isCE && ce.Code == aiprov.CodeTimeout)
			if !hung || !advancesAfterEngagedTimeout(purpose) {
				return res, err // TERMINAL
			}
			if firstEngaged == nil {
				firstEngaged = err
			}
			r.log.WarnContext(ctx, "ai router: a candidate hung after its request was written (engaged timeout); "+
				"its row is unknown and the next candidate is tried (D-16)",
				callAttrs(purpose, c.ProviderKey, model, calls, ce)...)
			continue
		}
		if ctx.Err() == nil {
			r.log.WarnContext(ctx, "ai router: a candidate failed before its request was written; trying the next",
				callAttrs(purpose, c.ProviderKey, model, calls, ce)...)
		}
	}
	if last == nil {
		if refused != "" {
			return nil, fmt.Errorf("%w: %w (%s: its circuit breaker is probing or open)",
				aiprov.ErrAllCandidatesFailed, ErrPaused, refused)
		}
		return nil, aiprov.ErrNotConfigured
	}
	return nil, withEarlierEngagement(fmt.Errorf("%w: %w", aiprov.ErrAllCandidatesFailed, last), firstEngaged)
}

// withEarlierEngagement keeps "money may have moved" on the error the caller gets: when an earlier
// call of the chain engaged (D-16 moved past it) and err itself does not say engaged, the earlier
// error is wrapped in too — AsCallError still finds err's CallError first, aiprov.Engaged walks both.
func withEarlierEngagement(err, earlier error) error {
	if earlier == nil || aiprov.Engaged(err) {
		return err
	}
	return fmt.Errorf("%w [after an engaged call: %w]", err, earlier)
}

// admit asks the registry to let the call go; a static router has no breakers.
func (r *Router) admit(providerKey string) (registry.Admission, bool) {
	if r.reg == nil {
		return registry.Admission{}, true
	}
	return r.reg.Admit(providerKey, entity.AICapabilityChat)
}

// call is ONE physical call: the row, the bounded call, the row's outcome and the breaker's end.
// ownDeadline reports that the call ran out of the ROUTER's budget while the caller's context was
// still alive — the hang D-16 is about, whatever Code the transport chose for it.
func (r *Router) call(ctx context.Context, purpose string, c candidate, model string, adm registry.Admission,
	callNo int, fallbackFrom string, budget time.Duration, req aiprov.ChatRequest,
) (res *aiprov.ChatResult, ownDeadline bool, err error) {
	actor := aiprov.ActorFrom(ctx)
	runID, attemptNo := aiprov.RunFrom(ctx)
	// THE ROW BEFORE THE CALL.
	h := r.ledger.Begin(ctx, entity.AICallStart{
		ProviderKey: c.ProviderKey, Model: model, Purpose: purpose,
		Actor: actor.Username, ActorAdminID: actor.AdminID,
		RunID: runID, AttemptNo: attemptNo, CallNo: callNo, FallbackFrom: fallbackFrom,
	})

	callCtx, cancel := context.WithTimeout(ctx, budget)
	started := r.now()
	res, err = c.chatter.Chat(callCtx, model, req)
	latency := int(r.now().Sub(started) / time.Millisecond)
	// Read before cancel(): after it, callCtx.Err() is Canceled whatever happened.
	ownDeadline = errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	if latency < 0 {
		latency = 0
	}

	if res != nil {
		res = fill(res, c.ProviderKey, model)
	}
	if err == nil && res == nil {
		// A transport that answered nothing and said nothing broke its contract after the request
		// may have left: the row says unknown and the chain stops.
		err = fmt.Errorf("%s: the chat transport returned neither an answer nor an error", c.ProviderKey)
	}
	if err == nil {
		r.ledger.Finish(ctx, h, okEnd(c.ProviderKey, res, latency))
		if r.reg != nil {
			r.reg.RecordSuccess(c.ProviderKey, entity.AICapabilityChat, adm)
		}
		return res, false, nil
	}

	r.ledger.Finish(ctx, h, failEnd(c.ProviderKey, res, err, latency))
	if r.reg != nil {
		// The registry's one rule decides what counts; every other failure ends the admission.
		r.reg.RecordFailure(c.ProviderKey, entity.AICapabilityChat, adm, err)
	}
	return res, ownDeadline, err
}

// fill makes the provenance whole: the billing provider, the slug (the requested one when the
// provider reported none), Engaged (an answer — even a partial one — was served).
func fill(res *aiprov.ChatResult, providerKey, model string) *aiprov.ChatResult {
	if strings.TrimSpace(res.Provider) == "" {
		res.Provider = providerKey
	}
	if strings.TrimSpace(res.Model) == "" {
		res.Model = model
	}
	res.Engaged = true
	return res
}

// price is the ledger's rank for one served call: the provider's own charge (a positive usage.cost —
// zero is not a known price), else the curated table by the slug the provider reported, else NULL.
func price(providerKey string, res *aiprov.ChatResult) (usd decimal.NullDecimal, source, version string) {
	if res.CostUSD.Valid && res.CostUSD.Decimal.IsPositive() {
		return res.CostUSD, entity.AICostProvider, ""
	}
	table, src := pricing.Price(providerKey, res.Model, pricing.Usage{
		Prompt: res.Usage.Prompt, Completion: res.Usage.Completion,
		Cached: res.Usage.Cached, Reasoning: res.Usage.Reasoning,
	})
	if table.Valid && src == entity.AICostTable {
		return table, entity.AICostTable, pricing.Version
	}
	return decimal.NullDecimal{}, entity.AICostNone, ""
}

func ptr[T any](v T) *T { return &v }

// usageEnd copies what the provider reported about the call onto the row.
func usageEnd(e *entity.AICallEnd, res *aiprov.ChatResult) {
	e.RequestID = res.RequestID
	e.ModelActual = res.Model
	e.PromptTokens = ptr(res.Usage.Prompt)
	e.CompletionTokens = ptr(res.Usage.Completion)
	e.CachedTokens = ptr(res.Usage.Cached)
	e.ReasoningTokens = ptr(res.Usage.Reasoning)
}

func okEnd(providerKey string, res *aiprov.ChatResult, latency int) entity.AICallEnd {
	e := entity.AICallEnd{Status: entity.AICallOK, Engaged: ptr(true), LatencyMs: ptr(latency)}
	usageEnd(&e, res)
	e.CostUSD, e.CostSource, e.PriceVersion = price(providerKey, res)
	return e
}

// failEnd is a failed call's row: `free` when the request never left (the ledger books it $0),
// else `unknown` with no price — or `charged_failed` when the transport handed back a partial result
// that prices.
func failEnd(providerKey string, res *aiprov.ChatResult, err error, latency int) entity.AICallEnd {
	ce, isCE := aiprov.AsCallError(err)
	engaged := !isCE || aiprov.Engaged(err)
	e := entity.AICallEnd{Engaged: ptr(engaged), LatencyMs: ptr(latency), CostSource: entity.AICostNone}
	if isCE {
		e.ErrorCode = ce.Code
		if ce.HTTPStatus != 0 {
			e.HTTPStatus = ptr(ce.HTTPStatus)
		}
	}
	if !engaged {
		e.Status = entity.AICallFree // normaliseEnd books cost 0 / free
		return e
	}
	e.Status = entity.AICallUnknown
	if res != nil {
		usageEnd(&e, res)
		if usd, src, ver := price(providerKey, res); usd.Valid {
			e.Status = entity.AICallChargedFailed
			e.CostUSD, e.CostSource, e.PriceVersion = usd, src, ver
		}
	}
	return e
}

// warnNoTransport — once per provider per config version: the owner enabled a provider this build
// has no chat adapter for. Its candidate is passed over; the log says so without flooding.
func (r *Router) warnNoTransport(ctx context.Context, providerKey, purpose string, version uint64) {
	r.warnMu.Lock()
	v, seen := r.warned[providerKey]
	if seen && v == version {
		r.warnMu.Unlock()
		return
	}
	r.warned[providerKey] = version
	r.warnMu.Unlock()
	r.log.WarnContext(ctx, "ai router: a route names a provider with no chat transport in this build; its candidate is skipped",
		slog.String("provider", providerKey), slog.String("purpose", purpose),
		slog.Uint64("config_version", version))
}

// callAttrs — the fields of a fallback line. NEVER the error's text: a provider's error body may echo
// the prompt (an author's text), and the router logs only what the fields say.
func callAttrs(purpose, providerKey, model string, callNo int, ce *aiprov.CallError) []any {
	attrs := []any{
		slog.String("purpose", purpose), slog.String("provider", providerKey),
		slog.String("model", model), slog.Int("call_no", callNo),
	}
	if ce != nil {
		attrs = append(attrs, slog.String("code", ce.Code), slog.Int("http_status", ce.HTTPStatus),
			slog.Bool("engaged", ce.Engaged), slog.Bool("retryable", ce.Retryable))
	}
	return attrs
}

// ───────────────────────── the translate-shaped door ─────────────────────────

// PurposeCompleter is Router.Chat narrowed to one purpose and to the text-in, text-out shape
// internal/translate needs: Complete(ctx, system, user, jsonMode) and Enabled().
type PurposeCompleter struct {
	r       *Router
	purpose string
}

// Completer returns the translate-shaped door of purpose.
func (r *Router) Completer(purpose string) *PurposeCompleter {
	return &PurposeCompleter{r: r, purpose: purpose}
}

// Complete is one Chat with no ceiling and the transport's default effort — the request
// openrouter.Client.Complete made — answering the text.
func (c *PurposeCompleter) Complete(ctx context.Context, system, user string, jsonMode bool) (string, error) {
	if c == nil {
		return "", aiprov.ErrNotConfigured
	}
	res, err := c.r.Chat(ctx, c.purpose, aiprov.ChatRequest{System: system, User: user, JSONMode: jsonMode})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// Enabled reports whether the purpose has a callable candidate. Nil-safe.
func (c *PurposeCompleter) Enabled() bool { return c != nil && c.r.Enabled(c.purpose) }
