package designgen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
)

// ═══ B-24 — THE 3D ROUTE IS THE PANEL'S: ONE CANDIDATE PER PASS, A FALLBACK ONLY WHERE NO MONEY MOVED ═══
//
// The real registry (snapshot, version, breakers) over an in-memory config — the image route's rig —
// and the real fal client against httptest stands, the real worker over the fake store. Since 2026-09-29
// fal is the only 3D provider (the direct Meshy provider left), so a chain of TWO providers — the
// cross-provider fallback these tests used to drive — is not constructible any more; the chooser's
// mechanics are driven through fal rows at different slugs.

// routeTo replaces one purpose's route and bumps the version, as SetRoute does.
func (s *routeCfgStore) routeTo(purpose string, cands ...entity.AIRouteCandidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Routes {
		if s.cfg.Routes[i].Purpose == purpose {
			s.cfg.Routes[i].Candidates = cands
		}
	}
	s.cfg.Settings.ConfigVersion++
}

// newThreedRouteRig: fal and openrouter on with STORED keys (the only key source since B-33;
// the EnvKeys below are the boot import's input, never read at call time); `threed` routed to cands
// (none = a route with no rows); every other purpose keeps the seed.
func newThreedRouteRig(t *testing.T, cands ...entity.AIRouteCandidate) *imageRouteRig {
	t.Helper()
	ring, err := keyring.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	require.NoError(t, err)
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		on := k == entity.AIProviderOpenRouter || k == entity.AIProviderFal
		p := entity.AIProvider{Key: k, Label: k, Enabled: on}
		if on {
			blob, err := ring.Seal("db-"+k+"-test-1234", keyring.AAD(k, string(entity.AIKeyAPI)))
			require.NoError(t, err)
			p.APIKeyEnc = blob
		}
		cfg.Providers = append(cfg.Providers, p)
	}
	for _, purpose := range entity.AIPurposes() {
		rc := []entity.AIRouteCandidate{row(1, entity.AIProviderOpenRouter, "")}
		if purpose == entity.AIPurposeThreed {
			rc = cands
		}
		cfg.Routes = append(cfg.Routes, entity.AIRoute{Purpose: purpose, Candidates: rc})
	}
	cfg.Settings = entity.AISettings{ConfigVersion: 1,
		DefaultChatProviderKey: entity.AIProviderOpenRouter, DefaultImageProviderKey: entity.AIProviderOpenRouter}
	st := &routeCfgStore{Store: &aiprovtest.Store{}, cfg: cfg}
	clk := &routeClock{}
	clk.ns.Store(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).UnixNano())
	reg := registry.New(st, ring, registry.EnvKeys{OpenRouter: "env-openrouter-aaaa",
		Fal: "env-fal-cccc"}, registry.WithClock(clk.now))
	require.NoError(t, reg.Reload(context.Background()))
	return &imageRouteRig{store: st, reg: reg, clk: clk}
}

// recStand records every request (method, path, body) and answers with `answer`.
type recStand struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []string // "METHOD path"
	raw []string // bodies
}

func newRecStand(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *recStand {
	t.Helper()
	st := &recStand{}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		st.mu.Lock()
		st.got = append(st.got, r.Method+" "+r.URL.Path)
		st.raw = append(st.raw, string(raw))
		st.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func (s *recStand) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func (s *recStand) posts() int {
	n := 0
	for _, r := range s.requests() {
		if strings.HasPrefix(r, http.MethodPost) {
			n++
		}
	}
	return n
}

func jsonAnswer(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// falCfgAt is the fal config the 3D factory builds from (as app.go's a.c.Fal), against a stand.
func falCfgAt(baseURL string) fal.Config {
	return fal.Config{APIKey: "k", BaseURL: baseURL, HTTPTimeout: 2 * time.Second,
		PollInterval: 5 * time.Millisecond, PollTimeout: 100 * time.Millisecond, DownloadTimeout: 2 * time.Second}
}

// threedFactoriesAt — app.go's factories over a fal config.
func threedFactoriesAt(falCfg fal.Config) map[string]func(string) Provider {
	return map[string]func(string) Provider{
		ThreedProviderFal: func(model string) Provider {
			cfg := falCfg
			if model != "" {
				cfg.Model3D = model
			}
			return NewFalThreedProvider(fal.New(cfg))
		},
	}
}

// reservedThreed — a reservation wide enough for every candidate of these rigs: since FIX-F2 a fresh 3D
// submit with no reservation is refused before Choose looks at the route.
var reservedThreed = decimal.NullDecimal{Valid: true, Decimal: decimal.RequireFromString("5")}

// ─────────────────────────── the chooser ───────────────────────────

// TestTheThreedChooserWALKS_THE_ROUTE_IN_ORDER — position 1 first; the candidate's name is the provider's
// (attempt rows keep today's names), the slot's own name never is. `tried` is keyed by that name, so a
// second fal row is not a fallback after fal was tried in this round: the round is exhausted.
//
// MUTATION (measured red→green): Choose ignoring `tried` (the `if tried[c.Name()]` skip removed) → the
// second pick is fal again instead of errChainExhausted.
func TestTheThreedChooserWALKS_THE_ROUTE_IN_ORDER(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, hitemSlug), row(2, entity.AIProviderFal, ""))
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1")), false)
	ch := routed.(Chooser)

	require.Equal(t, "threed", routed.Name(), "the slot's own name — never an attempt row's")
	require.True(t, routed.Enabled())
	require.Equal(t, []string{ContentTypeGLB, ContentTypePNG}, routed.Produces())

	p, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderFal, p.Name())
	require.Empty(t, ThreedRouteOf(p, false).Options, "position 1 first: the hitem3d row, which reads no options")
	_, isCollector := p.(Collector)
	require.True(t, isCollector, "the candidate collects by delegation")
	require.Equal(t, ThreedProviderFal, ThreedRouteOf(p, false).Provider, "ThreedRouteOf sees through the wrapper")

	_, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, map[string]bool{ThreedProviderFal: true})
	require.ErrorIs(t, err, errChainExhausted, "tried is per provider: fal was tried, the round is over")
}

// TestAnExhaustedThreedRoundSAYS_SO — every candidate that reads the run was tried: errChainExhausted, so
// the worker starts the next round at the top (dispatch.go), exactly as the image route.
//
// MUTATION (measured red→green): the `if served { return nil, errChainExhausted }` line removed → the
// exhausted round reads as option_not_read (terminal) and the seeded one-candidate route dies on its
// first retry.
func TestAnExhaustedThreedRoundSAYS_SO(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1")), false).(Chooser)
	_, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed},
		map[string]bool{ThreedProviderFal: true})
	require.ErrorIs(t, err, errChainExhausted)
}

// TestFalIsTheCandidateOfARouteWITHOUT_ROWS — a route with no rows is fal at its env model (FAL_MODEL_3D,
// here the default meshy slug, which reads build options); a route with rows is the rows and nothing is
// appended to them.
//
// MUTATION (measured red→green): read() without the `!hasRows` synthetic candidate → the empty route has
// nothing callable (Enabled false, errRouteMissing).
func TestFalIsTheCandidateOfARouteWITHOUT_ROWS(t *testing.T) {
	rg := newThreedRouteRig(t) // no rows
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1")), false)
	ch := routed.(Chooser)
	require.True(t, routed.Enabled())
	p, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderFal, p.Name(), "no rows: fal")
	require.NotEmpty(t, ThreedRouteOf(p, false).Options, "at the env model (the meshy family on fal)")

	rg.store.routeTo(entity.AIPurposeThreed, row(1, entity.AIProviderFal, hitemSlug))
	rg.reload(t)
	p, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Empty(t, ThreedRouteOf(p, false).Options, "rows: the route's row at its model, not the env's")
	_, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, map[string]bool{ThreedProviderFal: true})
	require.ErrorIs(t, err, errChainExhausted, "the env default is not appended to a routed chain")
}

// TestAnUnboundedThreedCandidateIS_SKIPPED — fal with FAL_UNIT_USD and no FAL_UNITS_CEILING_3D has no
// reserve number: never paid, with ONE warning per config version. Alone on the route it closes the door in
// words (Closed, today's sentence) while the kind stays «configured» (Enabled — the gate would otherwise
// answer kind_not_available first), and Choose refuses it free.
//
// MUTATION (measured red→green): bounded() answering true → fal is chosen and paid.
func TestAnUnboundedThreedCandidateIS_SKIPPED(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	cfg := falCfgAt("http://127.0.0.1:1")
	cfg.UnitUSD = 0.5 // a tariff, no units ceiling
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(cfg), false)
	ch := routed.(Chooser)
	logs := captureSlog(t)

	for i := 0; i < 3; i++ {
		_, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
		require.ErrorIs(t, err, errRouteMissing, "the worker never pays an unbounded route")
		require.Contains(t, err.Error(), "FAL_UNITS_CEILING_3D")
	}
	require.Equal(t, 1, strings.Count(logs.String(), "3D candidate skipped: no reserve number"), logs.String())
	require.True(t, routed.Enabled(), "keyed and unbounded: a closed door, not an unconfigured kind")
	v := routed.View()
	require.Contains(t, v.Closed, "FAL_UNIT_USD is set and FAL_UNITS_CEILING_3D is not")
	require.Equal(t, ThreedProviderFal, v.Head.Provider)
	_, ok := v.CeilingUSD("", "")
	require.False(t, ok, "nothing to reserve on")
}

// TestAThreedCandidateThatDropsAnOptionIS_SKIPPED — fal at the hitem3d slug reads no build options: a
// detailed run is paid on the next row (fal at the meshy slug, which reads them); with the hitem3d row
// alone it is refused free, terminal, in the door's own word.
//
// MUTATION (measured red→green): Choose without the unread check → the detailed run is sent to hitem3d,
// which drops «detailed» (and the lone-hitem3d route is not refused).
func TestAThreedCandidateThatDropsAnOptionIS_SKIPPED(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, hitemSlug), row(2, entity.AIProviderFal, ""))
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1")), false).(Chooser)
	detailed := Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed, ThreedQuality: fal.QualityDetailed}

	p, err := ch.Choose(detailed, nil)
	require.NoError(t, err)
	require.Contains(t, ThreedRouteOf(p, false).Options, ThreedOptionQuality, "the row that reads «detailed» pays")
	p, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Empty(t, ThreedRouteOf(p, false).Options, "a run stating nothing is read by the hitem3d row")

	rg.store.routeTo(entity.AIPurposeThreed, row(1, entity.AIProviderFal, hitemSlug))
	rg.reload(t)
	_, err = ch.Choose(detailed, nil)
	require.ErrorIs(t, err, errThreedOptionNotRead)
	require.Contains(t, err.Error(), "no candidate of the threed route reads params.threed.quality — ")
	v := classify(err)
	require.Equal(t, CodeOptionNotRead, v.Code, "the door's own word for the same fact")
	require.False(t, v.Retryable)
}

// openThreedBreaker — three retryable, unengaged faults open (provider, threed) for the window.
func openThreedBreaker(t *testing.T, reg *registry.Registry, pk string) {
	t.Helper()
	weather := &aiprov.CallError{Provider: pk, Code: aiprov.CodeProviderError, HTTPStatus: 503, Retryable: true,
		Err: errors.New("down")}
	for i := 0; i < 3; i++ {
		a, ok := reg.Admit(pk, entity.AICapabilityThreed)
		require.True(t, ok)
		reg.RecordFailure(pk, entity.AICapabilityThreed, a, weather)
	}
	require.Equal(t, registry.BreakerOpen, reg.BreakerState(pk, entity.AICapabilityThreed))
}

// TestAPausedThreedRouteWAITS — the only candidate's breaker is open: Choose answers errRoutePaused (a
// free re-queue, provider_paused), the slot stays Enabled (never kind_not_available for weather), and the
// view still reserves on it.
//
// MUTATION (measured red→green): read() dropping the held rows → the lone-fal route reads as
// unconfigured (errRouteMissing, terminal).
func TestAPausedThreedRouteWAITS(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1")), false)
	ch := routed.(Chooser)
	openThreedBreaker(t, rg.reg, entity.AIProviderFal)

	_, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.ErrorIs(t, err, errRoutePaused)
	require.Contains(t, err.Error(), "threed is paused after repeated failures (fal: circuit breaker open)")
	v := classify(err)
	require.Equal(t, CodeProviderPaused, v.Code)
	require.True(t, v.Retryable)
	require.True(t, routed.Enabled(), "a paused route is configured")
	view := routed.View()
	require.Equal(t, ThreedProviderFal, view.Head.Provider)
	_, ok := view.CeilingUSD("", "")
	require.True(t, ok, "the reserve covers the paused candidate it waits for")
}

// TestTheThreedCandidateKEEPS_THE_BREAKER_BOOKS — the submit is asked of the registry's breaker exactly as
// an image call is: three unengaged 503s open it; a half-open probe held by somebody else refuses the
// submit before it leaves (not engaged, retryable); the probe's success closes it. The COLLECT is free
// and never touches it — any number of failed lookups leave it closed.
//
// MUTATION (measured red→green): threedCandidate.Execute calling inner.Execute with no Admit / Record →
// three 503s leave the breaker closed.
func TestTheThreedCandidateKEEPS_THE_BREAKER_BOOKS(t *testing.T) {
	var mu sync.Mutex
	status := http.StatusServiceUnavailable
	stand := newRecStand(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		s := status
		mu.Unlock()
		if r.Method == http.MethodGet {
			jsonAnswer(http.StatusServiceUnavailable, `{"detail":"status down"}`)(w, r)
			return
		}
		if s == http.StatusOK {
			jsonAnswer(http.StatusOK, `{"request_id":"req-1"}`)(w, r)
			return
		}
		jsonAnswer(s, `{"detail":"queue down"}`)(w, r)
	})
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt(stand.srv.URL)), false).(Chooser)
	job := Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed, References: []string{"https://cdn.example/m/21.png"},
		ReferenceViews: []string{entity.DesignViewFront}}
	cand, err := ch.Choose(job, nil)
	require.NoError(t, err)

	t.Run("collects never touch the breaker", func(t *testing.T) {
		for i := 0; i < 4; i++ {
			_, err := cand.(Collector).Collect(context.Background(), job, falLocator(fal.DefaultModel3D, "req-x"))
			require.Error(t, err)
		}
		require.Equal(t, registry.BreakerClosed, rg.reg.BreakerState(entity.AIProviderFal, entity.AICapabilityThreed))
	})

	for i := 0; i < 3; i++ {
		_, err := cand.Execute(context.Background(), job)
		require.Error(t, err)
		require.False(t, aiprov.Engaged(err), "a bare 503 is the queue refusing work")
	}
	require.Equal(t, registry.BreakerOpen, rg.reg.BreakerState(entity.AIProviderFal, entity.AICapabilityThreed),
		"three unengaged 503s on the submit open (fal, threed)")

	rg.clk.advance(6 * time.Minute) // half-open
	probe, ok := rg.reg.Admit(entity.AIProviderFal, entity.AICapabilityThreed)
	require.True(t, ok, "somebody else takes the one probe")
	before := stand.posts()
	_, err = cand.Execute(context.Background(), job)
	require.Error(t, err)
	require.Equal(t, before, stand.posts(), "a refused submit never leaves")
	ce, spoke := aiprov.AsCallError(err)
	require.True(t, spoke)
	require.False(t, ce.Engaged)
	require.True(t, ce.Retryable, "retryable and free: the chain advances past it")
	rg.reg.Release(entity.AIProviderFal, entity.AICapabilityThreed, probe)

	mu.Lock()
	status = http.StatusOK
	mu.Unlock()
	out, err := cand.Execute(context.Background(), job)
	require.NoError(t, err)
	require.True(t, out.Pending)
	require.Equal(t, registry.BreakerClosed, rg.reg.BreakerState(entity.AIProviderFal, entity.AICapabilityThreed),
		"the probe's success closes the breaker")
}

// TestTheThreedViewREADS_THE_LIVE_ROUTE — the door's one value: Head = the first callable candidate's
// route; Ceiling = the MAX over the chain (the reserve covers whichever candidate pays); a keyless head
// names its key and closes nothing (the kind gate refuses it, as before).
//
// MUTATION (measured red→green): View's Ceiling returning the head's ceiling only → the [fal@hitem3d,
// fal@meshy] chain reserves hitem3d's number against the meshy row's 1.40 detailed booking.
func TestTheThreedViewREADS_THE_LIVE_ROUTE(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	// The key through the registry, as app.go wires it: a provider switched off in the panel is keyless.
	cfg := falCfgAt("http://127.0.0.1:1")
	cfg.APIKey, cfg.KeyFunc = "", rg.reg.KeyFunc(entity.AIProviderFal)
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(cfg), false)

	v := routed.View()
	require.Equal(t, ThreedProviderFal, v.Head.Provider, "the first callable candidate")
	require.Equal(t, []string{ThreedOptionTexture, ThreedOptionQuality, ThreedOptionSurfaceHint}, v.Head.Options)
	require.Empty(t, v.Closed)
	c, ok := v.CeilingUSD("", "")
	require.True(t, ok)
	require.Equal(t, "1.2", c.String(), "fal's published per-build price")
	c, _ = v.CeilingUSD("", fal.QualityDetailed)
	require.Equal(t, "1.4", c.String(), "fal's «ultra mode» price")

	t.Run("a keyless head names its key and closes nothing", func(t *testing.T) {
		rg.store.switchProvider(entity.AIProviderFal, false)
		rg.reload(t)
		require.False(t, routed.Enabled())
		v := routed.View()
		require.Equal(t, ThreedProviderFal, v.Head.Provider, "the configured head, so the door can name it")
		require.Empty(t, v.Closed)
		_, ok := v.CeilingUSD("", "")
		require.False(t, ok)
		require.Contains(t, missingCredential(routed), "no key for fal")
	})
}

// ─────────────────────────── the worker ───────────────────────────

// threedWorker — the worker over the fakes with the routed 3D slot, the boot-time provider(s) in Also,
// and a fixed clock.
func threedWorker(st *fakeStore, routed Provider, also ...Provider) *Worker {
	w := testWorker(st, media(21, 22), newFakeSink(ContentTypeGLB, ContentTypePNG),
		Providers{Threed: routed, Also: also})
	at := time.Date(2026, 9, 28, 13, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return at }
	return w
}

// TestAnEngagedFalTimeoutIsTERMINAL — fal read the whole submit and never answered: the build may be
// queued and billed (engaged). Terminal `unknown` — never a second submit (D-16: image/3D never fall back
// or retry after money may have moved).
//
// MUTATION (measured red→green): candidateChain.next without `|| aiprov.Engaged(callErr)` → the run is
// re-queued retryable.
func TestAnEngagedFalTimeoutIsTERMINAL(t *testing.T) {
	falStand := newRecStand(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // the body is read; the answer never comes
	})
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	cfg := falCfgAt(falStand.srv.URL)
	cfg.HTTPTimeout = 150 * time.Millisecond
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(cfg), false)
	st := &fakeStore{}
	w := threedWorker(st, routed)
	run := reservedSteerRun(91)

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, falStand.posts())
	require.Len(t, st.failed, 1)
	require.False(t, st.failed[0].Retryable, "money may have moved: terminal")
	require.Equal(t, CodeSubmitUnconfirmed, st.failed[0].ErrorCode)
	require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)
}

// TestADirectMeshyBuildInFlightIsREFUSED_BY_NAME — the direct Meshy provider left on 2026-09-29. A build
// it had ACCEPTED before the deploy (attempt 1, `meshy`, task-9) has no provider in this binary to be
// collected by: the pass refuses it by name — kind_not_available, the provider and the task in the
// sentence, so a person can reconcile it at Meshy — and never asks fal, which has never seen that id.
func TestADirectMeshyBuildInFlightIsREFUSED_BY_NAME(t *testing.T) {
	falStand := newRecStand(t, jsonAnswer(http.StatusOK, `{"request_id":"req-1"}`))
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	falCfg := falCfgAt(falStand.srv.URL)
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfg), false)
	st := &fakeStore{}
	w := threedWorker(st, routed, NewFalThreedProvider(fal.New(falCfg)))
	run := reservedSteerRun(92)
	full := run
	full.Attempts = []entity.DesignRunAttempt{{
		AttemptNo: 1, Provider: "meshy", State: entity.DesignAttemptAccepted,
		ProviderRequestId: sql.NullString{String: "task-9", Valid: true},
	}}
	st.getRun = &full

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Empty(t, falStand.requests(), "fal is never asked about a meshy task")
	require.Empty(t, st.started, "nothing is called: no attempt row")
	require.Equal(t, []string{CodeKindNotAvailable + " retry=false"}, failedCodes(st))
	require.Contains(t, st.failed[0].LastError, "meshy", "the sentence names the provider that holds the job")
	require.Contains(t, st.failed[0].LastError, "task-9", "and the task to reconcile")
}

// TestTheThreedCandidateRECORDS_WHAT_IT_SENDS — the history row of a routed 3D run carries the SURFACE
// STEER the chosen candidate sends, never the run's whole composed prompt (the garment note about the back
// reaches no texturing stage): the wrapper passes PromptCarrier through, and the ledger row names the
// route row's model.
//
// MUTATION (measured red→green): threedCandidate without SentPrompt → design_run.prompt records the
// composed prompt, «crossed straps on the back» included.
func TestTheThreedCandidateRECORDS_WHAT_IT_SENDS(t *testing.T) {
	falStand := newRecStand(t, jsonAnswer(http.StatusOK, `{"request_id":"req-777"}`))
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, falMeshySlug))
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt(falStand.srv.URL)), false)
	st := &fakeStore{}
	w := threedWorker(st, routed)
	ai := withLedger(w)

	_ = w.execute(context.Background(), reservedSteerRun(93), "tok")
	require.Len(t, st.recordedPrompts, 1)
	require.NotEmpty(t, st.recordedPrompts[0])
	require.NotContains(t, st.recordedPrompts[0], "crossed straps", "the surface steer, not the composed prompt")
	require.Equal(t, 1, falStand.posts())
	require.Contains(t, falStand.raw[0], `"texture_prompt"`, "the steer reaches the body")
	require.Contains(t, st.recordedPrompts[0], "matte heavy jersey")
	rows := ai.Rows()
	require.NotEmpty(t, rows)
	require.Equal(t, falMeshySlug, rows[0].Start.Model, "the ledger row names what was asked")
}

// TestADearerBuildNEVER_EXCEEDS_THE_RESERVATION — Codex REVIEW-F1 #1: the door reserved 1.20 (a standard
// build); a candidate that would book more for this run (a detailed build at 1.40) is skipped free, and a
// route with nothing within the reservation refuses free and terminal (threed_reserve_short), never pays
// 1.40 against 1.20. A run with no reservation at all is never paid.
//
// MUTATION (measured red→green): the `over` skip in Choose removed → the detailed build is chosen for the
// 1.20 run.
func TestADearerBuildNEVER_EXCEEDS_THE_RESERVATION(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1")), false).(Chooser)
	reserved := decimal.NullDecimal{Valid: true, Decimal: decimal.RequireFromString("1.20")}

	p, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reserved}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderFal, p.Name())

	_, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reserved,
		ThreedQuality: fal.QualityDetailed}, nil)
	require.ErrorIs(t, err, errThreedReserveShort)
	require.Contains(t, err.Error(), "more than the 1.2 USD reserved")
	v := classify(err)
	require.Equal(t, CodeThreedReserveShort, v.Code)
	require.False(t, v.Retryable)
	_, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed}, nil)
	require.ErrorIs(t, err, errThreedReserveShort, "a run with no reservation is never paid (Codex REVIEW-F2 P1-3)")
	require.Contains(t, err.Error(), "carries no reservation")
}
