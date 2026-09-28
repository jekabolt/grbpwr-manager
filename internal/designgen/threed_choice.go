package designgen

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ═══ THE ROUTED 3D PROVIDER (B-24) ═══
//
// Before B-24 the panel's `threed` route (fal | meshy, primary + fallback, an optional model) changed
// nothing about a build: DESIGN_THREED_PROVIDER picked ONE provider at boot, at FAL_MODEL_3D. The panel
// was lying. Now the Threed slot is a CHOOSER over the registry's live route, built like the image route
// (images_route.go): every pass pays ONE candidate — the first callable one, in position order, whose
// route reads what the run states — and that candidate's concrete provider opens the attempt row and
// pays. A candidate that fails WITHOUT engaging hands the run to the next one on a fresh attempt, through
// the worker's re-queue (settle → candidateChain.next); an open breaker PAUSES the route.
//
// ⚠ DESIGN_THREED_PROVIDER IS THE CANDIDATE ONLY WHEN THE ROUTE HAS NO ROWS. The seeded route (0373) is
// `threed → fal`, so a deployment that set DESIGN_THREED_PROVIDER=meshy pays fal from this commit on
// unless the route is edited: the panel is the truth, the env is the fallback — the boot log names both.
//
// ⚠ THE DOOR READS THE SAME ROUTE, LIVE (View): which options it reads, what one build may book — the
// MAX over the chain, because the reservation must cover whichever candidate ends up paying — and
// whether the door is closed. The worker asks ThreedUnread again of the candidate it is about to pay
// (dispatch.go threedUnreadAtSubmit), so a route edit between the door and the pickup is caught free.

// routedThreedName is the slot's own name. It NEVER lands in an attempt row: the worker chooses first
// and records the concrete candidate's name (`fal` | `meshy`, the names every row before B-24 carries).
const routedThreedName = "threed"

// RoutedThreed is the 3D slot as app.go wires it: the Provider the worker routes kind threed to (a
// Chooser), and the View the door reads.
type RoutedThreed interface {
	Provider
	View() ThreedRouteView
}

// ThreedRouteView — the 3D route as the door and the band read it, from ONE registry snapshot.
type ThreedRouteView struct {
	// Head — the route of the first CALLABLE candidate, else of the first PAUSED one, else (nothing can
	// be called) of the configured head at its row's model, so the door can name the missing key or the
	// unbounded setting; nil when not even that exists. Its Options are what the band advertises and
	// what the door refuses a stated option against.
	Head *ThreedRoute
	// Ceiling — the most one build with these options may book on ANY callable or paused candidate (ok =
	// false when there is none): the reserve must cover whichever candidate ends up paying.
	Ceiling func(texture, quality string) (decimal.Decimal, bool)
	// Closed — the sentence that closes the door: "" when a candidate can be called or is paused, else
	// the configured head's Unbounded() sentence when it has no reserve number (threed_reserve_unbounded),
	// else "" (the kind gate then refuses as it always did: kind_not_available).
	Closed string
}

// CeilingUSD is Ceiling, nil-safe.
func (v ThreedRouteView) CeilingUSD(texture, quality string) (decimal.Decimal, bool) {
	if v.Ceiling == nil {
		return decimal.Zero, false
	}
	return v.Ceiling(texture, quality)
}

// ThreedViewOf is the view of a route of ONE candidate that is callable (or, when r has no reserve
// number, the view of a door that r closes). The admin tests pin the door with it.
func ThreedViewOf(r ThreedRoute) ThreedRouteView {
	v := ThreedRouteView{Head: &r}
	if why := r.Unbounded(); why != "" {
		v.Closed = why
		return v
	}
	v.Ceiling = r.CeilingUSD
	return v
}

// routedThreedProvider — see the section doc.
type routedThreedProvider struct {
	reg *registry.Registry
	// factories builds a candidate's concrete provider per provider key: fal → the fal route at the row's
	// model (FAL_MODEL_3D when ""), meshy → the Meshy route asking for the row's ai_model. A struct build,
	// cheap enough per choice.
	factories map[string]func(model string) Provider
	// pbr is DESIGN_THREED_PBR, which every candidate's ThreedRoute is read at.
	pbr bool
	// envDefault is DESIGN_THREED_PROVIDER as designgen normalised it: the ONE candidate of a route with
	// no rows.
	envDefault string

	warnMu sync.Mutex
	warned map[string]uint64 // "<why>\x00<provider>" → the HIGHEST config version it was warned at
}

// NewRoutedThreedProvider is the 3D slot over the registry's `threed` route. A nil factory is dropped.
func NewRoutedThreedProvider(reg *registry.Registry, factories map[string]func(model string) Provider,
	pbr bool, envDefault string) RoutedThreed {
	f := make(map[string]func(string) Provider, len(factories))
	for k, fn := range factories {
		if fn != nil {
			f[strings.TrimSpace(k)] = fn
		}
	}
	return &routedThreedProvider{
		reg:        reg,
		factories:  f,
		pbr:        pbr,
		envDefault: strings.TrimSpace(envDefault),
		warned:     map[string]uint64{},
	}
}

// threedCandidate is one route row's concrete provider (Provider + Collector by delegation), with the
// registry's breaker around its paid half. Its Name is the inner provider's (`fal` | `meshy`), so attempt
// rows keep today's names and a resume by name (Providers.byName over Also) is unchanged.
type threedCandidate struct {
	inner Provider
	key   string // the provider key: the breaker's and the billing's
	reg   *registry.Registry
	route *ThreedRoute // ThreedRouteOf(inner, pbr), read once when the candidate was built
}

func (c threedCandidate) Name() string       { return c.inner.Name() }
func (c threedCandidate) Enabled() bool      { return c.inner.Enabled() }
func (c threedCandidate) Produces() []string { return c.inner.Produces() }

// MissingCredential and SentPrompt pass through: the door's sentence and the history row's words are
// the inner route's own — a wrapper that dropped SentPrompt would record the whole composed prompt for
// a 3D run again (see PromptCarrier).
func (c threedCandidate) MissingCredential() string { return missingCredential(c.inner) }
func (c threedCandidate) SentPrompt(job Job) string { return recordedPrompt(c.inner, job) }

// Collect passes through. A collect is a free lookup and NEVER touches the breaker: its weather is
// not the submit's, and a slow build must not pause the route for every fresh run.
func (c threedCandidate) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	col, ok := c.inner.(Collector)
	if !ok {
		return nil, fmt.Errorf("%w: %s accepted task %s but cannot collect it", errRouteMissing, c.Name(), requestID)
	}
	return col.Collect(ctx, job, requestID)
}

// Execute is the inner submit inside the registry's breaker bookkeeping, exactly as the image route asks
// it (images.go admit / endCall): one admission right before the paid call, exactly one end after it —
// RecordSuccess, or RecordFailure, which counts only a retryable fault nobody paid for and releases the
// admission for everything else. A refusal is a call that never left: not engaged, retryable — the
// worker's settle advances the chain past this candidate.
func (c threedCandidate) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if c.reg == nil {
		return c.inner.Execute(ctx, job)
	}
	adm, ok := c.reg.Admit(c.key, entity.AICapabilityThreed)
	if !ok {
		return nil, &aiprov.CallError{
			Provider:  c.key,
			Code:      aiprov.CodeRateLimited,
			Engaged:   false,
			Retryable: true,
			Err:       fmt.Errorf("%s refused the call (its circuit breaker is probing or open)", c.key),
		}
	}
	out, err := c.inner.Execute(ctx, job)
	if err == nil {
		c.reg.RecordSuccess(c.key, entity.AICapabilityThreed, adm)
	} else {
		c.reg.RecordFailure(c.key, entity.AICapabilityThreed, adm, err)
	}
	return out, err
}

// threedRouteRead is the route as ONE read: the callable candidates, the paused ones, and the configured
// head (as a provider, keyed or not) for the door's sentences.
type threedRouteRead struct {
	listed, held []threedCandidate
	// head — RouteHeadAt's row, or the env default when the route has no rows; nil when this build has
	// no provider of that key (headKey still names it).
	head    *threedCandidate
	headKey string
}

// read walks the `threed` route.
//
// ⚠ THREE REGISTRY READS, RE-READ UNTIL THE LIST AND THE HEAD COME FROM ONE VERSION. «Has the route any
// rows» (RouteHeadAt) decides whether the env default is a candidate at all; a panel save landing
// between that read and CandidatesAt would otherwise pair an empty list with a head that no longer
// exists and read a configured route as unconfigured (a terminal kind_not_available). BreakerHeld is read
// FIRST for the reason routedImageProvider.paused gives.
func (p *routedThreedProvider) read() threedRouteRead {
	var out threedRouteRead
	if p == nil || p.reg == nil {
		return out
	}
	var (
		heldRows, rows []registry.Candidate
		headRow        registry.Candidate
		hasRows        bool
		version        uint64
	)
	for i := 0; i < 3; i++ {
		heldRows = p.reg.BreakerHeld(entity.AIPurposeThreed)
		rows, version = p.reg.CandidatesAt(entity.AIPurposeThreed)
		var headVersion uint64
		headRow, headVersion, hasRows = p.reg.RouteHeadAt(entity.AIPurposeThreed)
		if headVersion == version {
			break
		}
	}
	if !hasRows {
		// THE ENV IS THE FALLBACK: a route with no row able to serve 3D is DESIGN_THREED_PROVIDER at its
		// env model, under the same key, bound and breaker rules as a row.
		out.headKey = p.envDefault
		c, ok := p.candidate(registry.Candidate{ProviderKey: p.envDefault}, version)
		if !ok {
			return out
		}
		out.head = &c
		if !c.inner.Enabled() || !p.bounded(c, version) {
			return out
		}
		if p.reg.BreakerState(p.envDefault, entity.AICapabilityThreed) == registry.BreakerOpen {
			out.held = []threedCandidate{c}
		} else {
			out.listed = []threedCandidate{c}
		}
		return out
	}
	out.headKey = headRow.ProviderKey
	if c, ok := p.candidate(headRow, version); ok {
		out.head = &c
	}
	// A held row that has no provider here, no key or no reserve number is NOT a pause: the breaker
	// closing would leave it just as uncallable, so the door's configuration sentence is the right one.
	for _, r := range heldRows {
		if c, ok := p.candidate(r, version); ok && c.inner.Enabled() && p.bounded(c, version) {
			out.held = append(out.held, c)
		}
	}
	// A row with no provider in this build is skipped with one warning per (provider, version); a
	// keyless one silently (the door names it); an UNBOUNDED one with one warning — see bounded.
	for _, r := range rows {
		if c, ok := p.candidate(r, version); ok && c.inner.Enabled() && p.bounded(c, version) {
			out.listed = append(out.listed, c)
		}
	}
	return out
}

// candidate builds a row's concrete provider; false (with a warning) when this build has none for it.
func (p *routedThreedProvider) candidate(r registry.Candidate, version uint64) (threedCandidate, bool) {
	f := p.factories[r.ProviderKey]
	if f == nil {
		p.warnOnce("transport", r.ProviderKey, version, "design generation: the threed route names a provider "+
			"with no 3D transport in this build; its candidate is skipped")
		return threedCandidate{}, false
	}
	inner := f(r.Model)
	return threedCandidate{inner: inner, key: r.ProviderKey, reg: p.reg, route: ThreedRouteOf(inner, p.pbr)}, true
}

// bounded — the candidate's route has a reserve number. ⚠ AN UNBOUNDED CANDIDATE IS SKIPPED, NEVER PAID:
// fal with FAL_UNIT_USD and no FAL_UNITS_CEILING_3D books tariff × whatever units fal reports, and no
// reservation covers that — the door used to refuse such a route, and a fallback onto it would pay what
// the door never reserved. A provider with no known route (nothing is known about what it reads or
// books) is skipped the same way.
func (p *routedThreedProvider) bounded(c threedCandidate, version uint64) bool {
	why := "this provider has no known 3D route"
	if c.route != nil {
		if why = c.route.Unbounded(); why == "" {
			return true
		}
	}
	p.warnOnce("unbounded", c.key, version, "design generation: 3D candidate skipped: no reserve number — "+why)
	return false
}

// warnOnce — once per (why, provider) per config version, MONOTONIC as routedImageProvider.warnNoTransport
// is (FIX-G4): passes and door reads see snapshots out of order, and a stale one must neither warn again
// nor pull the memory back.
func (p *routedThreedProvider) warnOnce(why, providerKey string, version uint64, msg string) {
	key := why + "\x00" + providerKey
	p.warnMu.Lock()
	v, seen := p.warned[key]
	if seen && version <= v {
		p.warnMu.Unlock()
		return
	}
	p.warned[key] = version
	p.warnMu.Unlock()
	slog.Default().Warn(msg, slog.String("provider", providerKey), slog.String("purpose", entity.AIPurposeThreed),
		slog.Uint64("config_version", version))
}

// closedHead — the configured head HOLDS A KEY and has no reserve number: the door is closed in words
// (threed_reserve_unbounded), not as unconfigured. "" otherwise.
func closedHead(r threedRouteRead) string {
	if r.head == nil || r.head.route == nil || !r.head.inner.Enabled() {
		return ""
	}
	return r.head.route.Unbounded()
}

// unread — the first option the job states that c's route would not read ("" = it reads them all).
func (c threedCandidate) unread(job Job) (option, why string) {
	return ThreedUnread(c.route, job.ThreedTexture, job.ThreedPBR, job.ThreedQuality, job.ThreedSurfaceHint)
}

// Name — see routedThreedName.
func (p *routedThreedProvider) Name() string { return routedThreedName }

// Enabled — a candidate is callable or PAUSED (the image route's rule, B-13/A5: a paused route is
// answered by Choose with a re-queue, never here with a terminal refusal) — or the configured head holds
// a key and has no reserve number: that door is CLOSED in words by the admin side (View.Closed →
// threed_reserve_unbounded) exactly as before B-24, and reading it as «not configured» here would have the
// kind gate, which runs first, answer kind_not_available instead. The worker's Choose still refuses such a
// route free (errRouteMissing): nothing unbounded is ever paid.
func (p *routedThreedProvider) Enabled() bool {
	r := p.read()
	return len(r.listed) > 0 || len(r.held) > 0 || closedHead(r) != ""
}

// Produces is the union of the callable and the paused candidates' content types (GLB + PNG for both
// routes). The paused half counts: the pre-flight's sink check must judge the route a paused run waits for.
func (p *routedThreedProvider) Produces() []string {
	r := p.read()
	seen := map[string]bool{}
	var out []string
	for _, c := range append(r.held, r.listed...) {
		for _, ct := range c.Produces() {
			if !seen[ct] {
				seen[ct] = true
				out = append(out, ct)
			}
		}
	}
	return out
}

// MissingCredential is the door's sentence when no candidate is callable (see CredentialNamer).
func (p *routedThreedProvider) MissingCredential() string { return p.missingIn(p.read()) }

func (p *routedThreedProvider) missingIn(r threedRouteRead) string {
	if len(r.held) > 0 {
		return routePausedSentence(entity.AIPurposeThreed, threedNames(r.held))
	}
	if why := closedHead(r); why != "" {
		return why
	}
	if r.head != nil {
		if !r.head.inner.Enabled() {
			return missingCredential(r.head.inner)
		}
		return "no candidate of the threed route can be called right now — check the route in admin → AI providers"
	}
	if r.headKey != "" {
		return fmt.Sprintf("threed is routed to %s, and this build has no 3D transport for it — route it to %s "+
			"in admin → AI providers", r.headKey, strings.Join(p.factoryKeys(), ", "))
	}
	return "no 3D transport is wired"
}

func (p *routedThreedProvider) factoryKeys() []string {
	keys := make([]string, 0, len(p.factories))
	for k := range p.factories {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// threedNames — the providers of cands, once each, in route order.
func threedNames(cands []threedCandidate) []string {
	var names []string
	for _, c := range cands {
		names = appendUnique(names, c.key)
	}
	return names
}

// Choose — see Chooser. The first callable candidate, in position order, that is not in tried and whose
// route READS every option the run states (ThreedUnread over job.Threed*): a candidate that would drop
// one is skipped before any row is opened, as the image route skips one that cannot draw the slug.
//
//   - every reading candidate tried → errChainExhausted (the worker starts the next round);
//   - no callable candidate reads the run, a paused one would → errRoutePaused (a free re-queue);
//   - candidates exist and none reads the run → errThreedOptionNotRead, terminal and free — the door's
//     own word for the fact (option_not_read), not the image route's unknown_image_model;
//   - nothing callable, nothing paused → errRouteMissing with the door's sentence.
func (p *routedThreedProvider) Choose(job Job, tried map[string]bool) (Provider, error) {
	r := p.read()
	if len(r.listed) == 0 && len(r.held) == 0 {
		return nil, fmt.Errorf("%w: the threed route has no callable candidate — %s", errRouteMissing, p.missingIn(r))
	}
	served := false
	var opt, why string
	over := 0 // candidates skipped because one build would book more than the run's reservation
	for _, c := range r.listed {
		if o, w := c.unread(job); o != "" {
			if opt == "" {
				opt, why = o, w
			}
			continue
		}
		// ⚠ THE RESERVATION IS THE CEILING (Codex REVIEW-F1 #1). The door reserved the MAX over the chain
		// it saw; a route edited since (a dearer candidate added, a price raised) must not pay above that
		// number on this run: such a candidate is skipped free, before any row is opened.
		if job.ThreedReservedUSD.Valid && c.route != nil {
			if top, ok := c.route.CeilingUSD(job.ThreedTexture, job.ThreedQuality); ok && top.GreaterThan(job.ThreedReservedUSD.Decimal) {
				over++
				continue
			}
		}
		served = true
		if tried[c.Name()] {
			continue
		}
		return c, nil
	}
	if served {
		return nil, errChainExhausted
	}
	if over > 0 && opt == "" {
		return nil, fmt.Errorf("%w: every candidate of the threed route that reads this run would book more than the "+
			"%s USD reserved for it — the route was edited after the run was priced; start the run again. Nothing was "+
			"submitted and nothing was charged", errThreedReserveShort, job.ThreedReservedUSD.Decimal.String())
	}
	var waiting []threedCandidate
	for _, c := range r.held {
		o, w := c.unread(job)
		if o == "" {
			waiting = append(waiting, c)
		} else if opt == "" {
			opt, why = o, w
		}
	}
	if len(waiting) > 0 {
		return nil, fmt.Errorf("%w: %s. Nothing was sent and nothing was charged", errRoutePaused,
			routePausedSentence(entity.AIPurposeThreed, threedNames(waiting)))
	}
	return nil, fmt.Errorf("%w: no candidate of the threed route reads params.threed.%s — %s. Nothing was "+
		"submitted and nothing was charged", errThreedOptionNotRead, opt, why)
}

// Execute is the chooser's pick with nothing tried, for a caller that did not Choose first. The worker
// always chooses (dispatch.go) and pays the concrete candidate itself; ONE candidate, ONE pass.
func (p *routedThreedProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	prov, err := p.Choose(job, nil)
	if err != nil {
		return nil, err
	}
	return prov.Execute(ctx, job)
}

// View — see ThreedRouteView. ONE read of the route per call; a door that calls it once per question
// may see two versions within one request, and the worker's pre-submit re-check is what catches that.
func (p *routedThreedProvider) View() ThreedRouteView {
	r := p.read()
	live := append(append([]threedCandidate(nil), r.listed...), r.held...)
	var v ThreedRouteView
	if len(live) > 0 {
		v.Head = live[0].route
		routes := make([]*ThreedRoute, 0, len(live))
		for _, c := range live {
			routes = append(routes, c.route)
		}
		v.Ceiling = func(texture, quality string) (decimal.Decimal, bool) {
			top, any := decimal.Zero, false
			for _, rt := range routes {
				if c, ok := rt.CeilingUSD(texture, quality); ok && (!any || c.GreaterThan(top)) {
					top, any = c, true
				}
			}
			return top, any
		}
		return v
	}
	if r.head != nil && r.head.route != nil {
		v.Head = r.head.route
		v.Closed = r.head.route.Unbounded()
	}
	return v
}
