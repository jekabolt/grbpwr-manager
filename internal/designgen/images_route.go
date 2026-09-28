package designgen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ THE ROUTED IMAGE PROVIDER (B-13) ═══
//
// Before B-13 `Providers.Image` was ONE imageProvider over ONE orimages client, so the panel's
// image.generate route (primary + fallback) changed nothing about a generation. Now the image slot
// holds a CHOOSER over the registry's live route: every pass picks ONE candidate, and that
// candidate's concrete provider is what opens the attempt row and pays.
//
// ⚠ EXECUTE STILL NEVER RETRIES (provider.go), AND THE FALLBACK IS NOT A LOOP IN HERE. A candidate
// is tried on its OWN attempt row, in its own pass (02-PLAN S-3): when it fails without having
// engaged, the worker's settle re-queues the run at once (failRunAt now, retryable) and the next pass
// chooses the next candidate. The attempt cap (designMaxPaidAttempts, store-side) therefore counts a
// fallback like any other attempt — which is the point: the cap is a money figure, and a hidden
// second call inside Execute would be a second charge outside it.

// routedImageName is the routed slot's own name. It NEVER lands in an attempt row: the worker asks
// the chooser first and records the concrete candidate's name (`<provider>_images`).
const routedImageName = "image"

// ImageTransport is one provider's paid image call, as the image route needs it.
//
// *orimages.Client satisfies it (OpenRouter's POST /images). Commit F's OpenAI transport maps
// `openai/gpt-image-2 → gpt-image-2` and refuses the Gemini / Seedream slugs — the ALIAS MAP LIVES IN
// THE TRANSPORT, and Serves is how the chooser asks it without a second copy of that map here.
type ImageTransport interface {
	Generate(ctx context.Context, req orimages.Request) (*orimages.Result, error)
	// Model is the transport's own default slug (a run and a route row that name none).
	Model() string
	// Enabled reports whether the transport holds a key right now.
	Enabled() bool
	// Serves reports whether this transport can draw the given slug.
	Serves(slug string) bool
}

// Chooser is a route with more than one candidate: the worker asks it for the provider THIS pass pays.
type Chooser interface {
	// Choose returns the first candidate, in route position order, that is callable (a transport,
	// switched on), serves the run and is not in tried — the provider names this round of the run has
	// already opened attempts with. The image route reads job.Kind and job.Model (the frozen
	// params.image.model, "" = none); the 3D route (B-24) reads the job's stated build options. The
	// whole Job travels so each route reads what it needs off ONE value the worker built.
	//
	// errChainExhausted: every serving candidate is in tried (the round is over — the worker starts
	// the next one with an empty set). errRoutePaused: nothing callable serves the slug, and a candidate
	// that would is held by its open breaker (a wait: the worker re-queues the run with no attempt row).
	// errNoCandidateServes: candidates exist and none serves the slug (terminal, unknown_image_model).
	// errRouteMissing: nothing is callable at all, and nothing is paused either.
	//
	// ⚠ EVERY CANDIDATE IS ITS OWN ATTEMPT, AND THE ATTEMPT CAP IS THE STORE'S. A two-candidate chain
	// spends two of designMaxPaidAttempts on one round; nothing here counts or raises that cap.
	//
	// Two candidates of ONE provider share its name (`openrouter_images`), so once that name is in
	// tried the second is skipped with the first: the attempt row names the provider, not the slug,
	// and the refusals that advance a chain (401, 402, a breaker) are the account's, not the model's.
	Choose(job Job, tried map[string]bool) (Provider, error)
}

var (
	// errChainExhausted — every candidate that serves this run was tried in the current round. Never
	// written on a row: the worker answers it by starting the next round (dispatch.go), and settle reads
	// it as «no fallback left».
	errChainExhausted = errors.New("designgen: every candidate of this route was tried in this round")
	// errNoCandidateServes — the route has callable candidates and not one of them draws the run's
	// slug. Free (before StartAttempt) and terminal: the door's own word, unknown_image_model.
	errNoCandidateServes = errors.New("designgen: no candidate of this route serves the run's image model")
	// errRoutePaused — nothing of the route can be called right now ONLY because the breakers of the
	// candidates that would draw the run are open (B-13/A5, Codex REVIEW-CD P1). Free (before
	// StartAttempt) and retryable: provider_paused. The worker re-queues the run for the end of the
	// breaker window without opening an attempt row; a person has nothing to fix.
	errRoutePaused = errors.New("designgen: the image route is paused after repeated failures")
)

// routePauseRequeue is how far a paused run is put back: the registry's breaker window (registry
// breakerConfig.OpenTimeout — three transient faults open a (provider, image) breaker for five
// minutes, then ONE probe decides). The registry does not export the window or the instant a breaker
// opened, so the number is repeated here and TestTheRoutePauseREQUEUES_FOR_THE_REGISTRY_S_WINDOW pins
// it against the registry's own behaviour: a change there turns that test red, not this run silent.
//
// ⚠ THE WHOLE WINDOW, NOT A MINUTE, BECAUSE A PAUSED PICKUP SPENDS A ROUND. The store's ten-round
// ceiling counts every FailRun that re-queues (only paid_collect_waiting is exempt, and only for a run
// holding an accepted id); a one-minute re-queue would spend up to five rounds of ten per pause and a
// second outage would close the run on the very ceiling this pause exists to keep it off. Re-queued at
// now + the window, the next pickup finds the breaker half-open at the latest — one round per pause —
// at the cost of up to one window of extra latency when the breaker had opened long before.
const routePauseRequeue = 5 * time.Minute

// routedImageProvider — see the section doc.
type routedImageProvider struct {
	reg        *registry.Registry
	transports map[string]ImageTransport
	// envDefault is the deployment's env slug (OPENROUTER_MODEL_IMAGE as the client resolved it): the
	// last word of `requested` for a transport that names no default of its own.
	envDefault string

	warnMu sync.Mutex
	warned map[string]uint64 // provider key → the HIGHEST config version its missing transport was warned at
}

// NewRoutedImageProvider is the image slot over the registry's image.generate route. transports maps a
// provider key to its image transport (openrouter → the orimages client today); a nil transport is
// dropped. envDefaultSlug is the env default slug (see envDefault).
func NewRoutedImageProvider(reg *registry.Registry, transports map[string]ImageTransport, envDefaultSlug string) Provider {
	t := make(map[string]ImageTransport, len(transports))
	for k, tr := range transports {
		if tr != nil {
			t[strings.TrimSpace(k)] = tr
		}
	}
	return &routedImageProvider{
		reg:        reg,
		transports: t,
		envDefault: strings.TrimSpace(envDefaultSlug),
		warned:     map[string]uint64{},
	}
}

// routeCandidate is one route row this build can call: the registry's candidate and its transport.
type routeCandidate struct {
	registry.Candidate
	t ImageTransport
}

// candidates is image.generate's route as ONE registry snapshot, in position order, reduced to what
// this build can call: a row whose provider has no image transport here is skipped with ONE warning
// per (provider, config version) — the owner routed a provider before its adapter exists, and a
// warning per press would drown the log; a transport that reports no key is skipped silently (a
// configuration state the door already names).
func (p *routedImageProvider) candidates() []routeCandidate {
	if p == nil || p.reg == nil {
		return nil
	}
	cands, version := p.reg.CandidatesAt(entity.AIPurposeImageGenerate) // one snapshot: the list and its version
	out := make([]routeCandidate, 0, len(cands))
	for _, c := range cands {
		t := p.transports[c.ProviderKey]
		if t == nil {
			p.warnNoTransport(c.ProviderKey, version)
			continue
		}
		if !t.Enabled() {
			continue
		}
		out = append(out, routeCandidate{Candidate: c, t: t})
	}
	return out
}

// paused is the route's HELD half: the candidates the registry dropped ONLY because their breaker for
// the image capability is open (registry.BreakerHeld: enabled, keyed, able to serve), reduced to the
// ones this build could call once the window ends — a transport here, and that transport switched on.
// A held row with no transport or with its transport off is NOT a pause: the breaker closing would
// leave it just as uncallable, so the door's configuration sentence is the right one for it.
//
// ⚠ READ IT BEFORE candidates(), NOT AFTER. The two lists come from two registry walks, and a breaker
// can change between them. The change time can make on its own is open → half-open (the window runs
// out), which moves a row from held to listed: held first, listed second sees it in at least one of
// the two. The other order could see it in neither and close a self-healing run as not configured —
// the failure this half exists to prevent. (closed → open needs a fault, i.e. a concurrent image call
// in the few instructions between the reads; the worker's runOnce is sequential.)
func (p *routedImageProvider) paused() []routeCandidate {
	if p == nil || p.reg == nil {
		return nil
	}
	held := p.reg.BreakerHeld(entity.AIPurposeImageGenerate)
	out := make([]routeCandidate, 0, len(held))
	for _, c := range held {
		t := p.transports[c.ProviderKey]
		if t == nil || !t.Enabled() {
			continue
		}
		out = append(out, routeCandidate{Candidate: c, t: t})
	}
	return out
}

// pausedNames — the providers of held, once each, in route order (the sentence a row and the door say).
func pausedNames(held []routeCandidate) []string {
	var names []string
	for _, c := range held {
		names = appendUnique(names, c.ProviderKey)
	}
	return names
}

// pausedSentence is what a paused route says, on the row and at the door: which providers wait, that
// nothing needs configuring, and when the next pickup comes — never «no key», which sends the owner to
// re-type a key that is fine.
func pausedSentence(held []routeCandidate) string {
	return routePausedSentence(entity.AIPurposeImageGenerate, pausedNames(held))
}

// routePausedSentence — pausedSentence for any routed purpose (the 3D route says it too, B-24): one
// wording, so a person reading a paused image run and a paused 3D run reads one fact.
func routePausedSentence(purpose string, providers []string) string {
	return fmt.Sprintf("%s is paused after repeated failures (%s: circuit breaker open); retrying "+
		"by itself within %s — no key is missing", purpose, strings.Join(providers, ", "), routePauseRequeue)
}

// warnNoTransport — once per provider per config version, as the router warns (router.warnNoTransport).
//
// THE MEMORY IS MONOTONIC, as the router's is (FIX-G4): passes and door checks read snapshots
// concurrently, so versions arrive out of order — a read of v1 finishing after one of v2 warned must
// neither warn again nor pull the memory back to v1 (the next v2 read would then warn a third time).
func (p *routedImageProvider) warnNoTransport(providerKey string, version uint64) {
	p.warnMu.Lock()
	v, seen := p.warned[providerKey]
	if seen && version <= v {
		p.warnMu.Unlock()
		return
	}
	p.warned[providerKey] = version
	p.warnMu.Unlock()
	slog.Default().Warn("design generation: image.generate routes a provider with no image transport in this build; "+
		"its candidate is skipped",
		slog.String("provider", providerKey), slog.String("purpose", entity.AIPurposeImageGenerate),
		slog.Uint64("config_version", version))
}

// candidate is the concrete provider of one route row: the ordinary image route over that row's
// transport, billed to its provider, with its route slug and the registry's breaker.
func (p *routedImageProvider) candidate(c routeCandidate) imageProvider {
	return imageProvider{t: c.t, providerKey: c.ProviderKey, routeModel: c.Model, breakers: p.reg}
}

// Name — see routedImageName: the slot's name, never an attempt row's.
func (p *routedImageProvider) Name() string { return routedImageName }

// Enabled — some candidate of the route is callable right now, OR PAUSED (held by its open breaker,
// with a transport here that is on). False before the registry has a snapshot (app.go refuses to boot
// without one), with no route rows, or when every routed provider is switched off, keyless or without
// a transport here.
//
// ⚠ A PAUSED ROUTE IS ENABLED (B-13/A5, Codex REVIEW-CD P1). Enabled is the pre-flight's question
// «is this route configured», and a false answer is terminal: kind_not_available, the run closed
// before an attempt row, the reserve released. Three bare 503s open the image breaker for five
// minutes; before A5 the next pickup inside that window found no candidate, read the route as
// unconfigured and killed a run the breaker would have let through minutes later. The pause is
// answered by Choose (errRoutePaused → a re-queue), not here.
func (p *routedImageProvider) Enabled() bool {
	return len(p.paused()) > 0 || len(p.candidates()) > 0
}

// Produces is the union of the content types of the callable and the paused candidates (all PNG
// today: every candidate is the image route over a transport that is asked for png). The paused half
// counts: the pre-flight's sink check must judge the route a paused run is waiting for, not pass
// vacuously over an empty list.
func (p *routedImageProvider) Produces() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range append(p.paused(), p.candidates()...) {
		for _, ct := range p.candidate(c).Produces() {
			if !seen[ct] {
				seen[ct] = true
				out = append(out, ct)
			}
		}
	}
	return out
}

// MissingCredential is the door's sentence when no candidate is callable (see CredentialNamer). Three
// different situations, so three sentences: a route PAUSED by its breakers is fixed by waiting (no key
// is missing — «set a key» would send the owner to re-type one that works); a route that names only
// providers this build cannot draw with is fixed in the route; a route whose providers hold no key is
// fixed by the key.
func (p *routedImageProvider) MissingCredential() string {
	if held := p.paused(); len(held) > 0 {
		return pausedSentence(held)
	}
	if p.reg != nil {
		cands, _ := p.reg.CandidatesAt(entity.AIPurposeImageGenerate)
		var without []string
		transported := false
		for _, c := range cands {
			if p.transports[c.ProviderKey] == nil {
				without = appendUnique(without, c.ProviderKey)
			} else {
				transported = true
			}
		}
		if len(without) > 0 && !transported {
			return fmt.Sprintf("image.generate is routed to %s, and this build has no image transport for it — "+
				"route it to %s in admin → AI providers", strings.Join(without, ", "), strings.Join(p.transportKeys(), ", "))
		}
	}
	keys := p.transportKeys()
	sentences := make([]string, 0, len(keys))
	for _, k := range keys {
		sentences = append(sentences, imageProvider{providerKey: k}.MissingCredential())
	}
	if len(sentences) == 0 {
		return "no image transport is wired"
	}
	return strings.Join(sentences, "; ")
}

func (p *routedImageProvider) transportKeys() []string {
	keys := make([]string, 0, len(p.transports))
	for k := range p.transports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// Choose — see Chooser. The image route reads the run's kind and its frozen slug off the job.
func (p *routedImageProvider) Choose(job Job, tried map[string]bool) (Provider, error) {
	kind, model := job.Kind, job.Model
	held := p.paused() // FIRST — see paused
	cands := p.candidates()
	if len(cands) == 0 && len(held) == 0 {
		return nil, fmt.Errorf("%w: the image.generate route has no callable candidate for a %s run — %s",
			errRouteMissing, kind, p.MissingCredential())
	}
	served := false
	for _, c := range cands {
		prov := p.candidate(c)
		// ⚠ A CANDIDATE THAT CANNOT DRAW THE SLUG IS SKIPPED LIKE A MISSING TRANSPORT, before any row is
		// opened: sending it would be a paid-looking refusal (a 400/404 at best) for a slug we already
		// know it does not take. Its own route model counts for a run that froze none.
		if !c.t.Serves(firstNonEmpty(prov.requested(model), p.envDefault)) {
			continue
		}
		served = true
		if tried[prov.Name()] {
			continue
		}
		return prov, nil
	}
	if served {
		return nil, errChainExhausted
	}
	// ⚠ NOTHING CALLABLE DRAWS THE SLUG — BUT A PAUSED CANDIDATE THAT WOULD IS A WAIT, NOT A REFUSAL.
	// Checked with the same Serves question as the callable ones: a Gemini run whose only drawing
	// candidate is held must come back after the window, and a run no candidate draws even when every
	// breaker closes is refused now, whatever is paused.
	var waiting []routeCandidate
	for _, c := range held {
		if c.t.Serves(firstNonEmpty(p.candidate(c).requested(model), p.envDefault)) {
			waiting = append(waiting, c)
		}
	}
	if len(waiting) > 0 {
		return nil, fmt.Errorf("%w: %s. Nothing was sent and nothing was charged", errRoutePaused, pausedSentence(waiting))
	}
	return nil, fmt.Errorf("%w: %q (a %s run) — no candidate of image.generate draws it. Nothing was sent and "+
		"nothing was charged", errNoCandidateServes, strings.TrimSpace(model), kind)
}

// Execute is the chooser's pick with nothing tried, for a caller that did not Choose first. The
// worker always chooses (dispatch.go) and pays the concrete candidate itself, so its attempt row names
// the provider; this exists so the slot is still a whole Provider. ONE candidate, ONE pass — never a
// walk down the chain.
func (p *routedImageProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	prov, err := p.Choose(job, nil)
	if err != nil {
		return nil, err
	}
	return prov.Execute(ctx, job)
}

// roundTried is the set of providers the CURRENT ROUND of a run has opened attempts with: the attempts
// in attempt_no order, a round closing whenever a provider repeats — the chain wrapped. A nameless
// attempt (a row from before the column was filled) says nothing and is skipped.
//
// ⚠ A ROUND, NOT THE WHOLE HISTORY, AND THE DIFFERENCE IS THE SEEDED ONE-CANDIDATE ROUTE. «Every
// provider ever tried» would leave a one-candidate route with nothing to choose after its first
// weather failure, and the run would close terminally on the pass that should have retried it — a
// behaviour change on the one route every deployment has. With rounds, candidate 1 → 2 → … → back to
// 1 after the store's back-off, exactly as far as the attempt cap lets it.
func roundTried(attempts []entity.DesignRunAttempt) map[string]bool {
	tried := map[string]bool{}
	for _, a := range attempts {
		name := strings.TrimSpace(a.Provider)
		if name == "" {
			continue
		}
		if tried[name] {
			tried = map[string]bool{}
		}
		tried[name] = true
	}
	return tried
}

// candidateChain is what settle needs to decide «fall back now»: the chooser the pass chose with, the
// job it chose for (the image route reads its kind and frozen slug, the 3D route its stated options),
// and the round's tried set INCLUDING the candidate this pass paid.
type candidateChain struct {
	chooser Chooser
	job     Job
	tried   map[string]bool
	from    string
}

// newCandidateChain — the chain after a pass that paid `from`, in a round that had tried `tried`.
func newCandidateChain(ch Chooser, job Job, tried map[string]bool, from string) *candidateChain {
	next := make(map[string]bool, len(tried)+1)
	for k, v := range tried {
		next[k] = v
	}
	next[from] = true
	return &candidateChain{chooser: ch, job: job, tried: next, from: from}
}

// next is the candidate the run falls back to after callErr, or false.
//
// ⚠ ONLY A FAILURE THE TRANSPORT PROVED FREE ADVANCES. The chain carries on only when the error holds
// a CallError and nothing in it is engaged (D-09: no money moved) — for terminal codes too: a 401, a
// 402, a 404 or a 400 on one provider says nothing about the next. An engaged failure may have bought
// the picture, and the next candidate would buy it again; an error NO transport spoke for is read as
// engaged for the same reason (the router's rule). A pass that produced pictures never reaches here.
func (c *candidateChain) next(callErr error) (Provider, bool) {
	if c == nil || callErr == nil {
		return nil, false
	}
	if _, spoke := aiprov.AsCallError(callErr); !spoke || aiprov.Engaged(callErr) {
		return nil, false
	}
	prov, err := c.chooser.Choose(c.job, c.tried)
	if err != nil {
		return nil, false
	}
	return prov, true
}

// ─────────────────────────── the engine table off the live route ───────────────────────────

// EngineTableFunc is the engine table as a function of the LIVE route (B-13): EngineTable over the
// image.generate route's first model when that slug is a row this deployment can offer as its
// default, else over envDefaultSlug. app.go hands the same function to the worker (Config.Engines)
// and to the door (SetDesignEngines), so the door prices and the worker resolves against one table.
//
// ⚠ A SLUG THE TABLE CANNOT DEFAULT TO FALLS BACK TO ENV, WITH ONE WARNING PER DISTINCT SLUG. The
// route's model is free text in the panel, and EngineTable EMPTIES the table for a default it has no
// row for — a typo would blank the playground's picker and refuse every params.image at the door. The
// test is «does EngineTable(slug) come back non-empty», which is stricter than «is it a catalogue
// row»: a Gemini / Seedream slug IS in the catalogue and still cannot be the default (defaultCapable),
// and a flagged row whose flag is off is not listed — both would blank the table the same way.
//
// Flags stay env (the panel does not own them yet).
func EngineTableFunc(reg *registry.Registry, envDefaultSlug string, flags EngineFlags) func() []Engine {
	var (
		mu     sync.Mutex
		warned = map[string]bool{}
	)
	return func() []Engine {
		if reg != nil {
			if slug := strings.TrimSpace(reg.DefaultImageSlug()); slug != "" {
				if table := EngineTable(slug, flags); len(table) > 0 {
					return table
				}
				mu.Lock()
				first := !warned[slug]
				warned[slug] = true
				mu.Unlock()
				if first {
					slog.Default().Warn("design generation: the image.generate route's model is not an engine this "+
						"deployment can offer as its default; the engine table keeps the env default",
						slog.String("route_model", slug), slog.String("env_default", envDefaultSlug))
				}
			}
		}
		return EngineTable(envDefaultSlug, flags)
	}
}
