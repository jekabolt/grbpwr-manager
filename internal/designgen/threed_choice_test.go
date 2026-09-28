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
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
)

// ═══ B-24 — THE 3D ROUTE IS THE PANEL'S: ONE CANDIDATE PER PASS, A FALLBACK ONLY WHERE NO MONEY MOVED ═══
//
// The real registry (snapshot, version, breakers) over an in-memory config — the image route's rig —
// and the real fal / Meshy clients against httptest stands, the real worker over the fake store.

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

// newThreedRouteRig: fal and meshy on with their env keys, openrouter on; `threed` routed to cands (none =
// a route with no rows); every other purpose keeps the seed.
func newThreedRouteRig(t *testing.T, cands ...entity.AIRouteCandidate) *imageRouteRig {
	t.Helper()
	ring, err := keyring.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	require.NoError(t, err)
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		on := k == entity.AIProviderOpenRouter || k == entity.AIProviderFal || k == entity.AIProviderMeshy
		cfg.Providers = append(cfg.Providers, entity.AIProvider{Key: k, Label: k, Enabled: on})
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
		Fal: "env-fal-cccc", Meshy: "env-meshy-dddd"}, registry.WithClock(clk.now))
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

// threedFactoriesAt — app.go's factories over a fal config and a Meshy client.
func threedFactoriesAt(falCfg fal.Config, m *meshy.Client) map[string]func(string) Provider {
	return map[string]func(string) Provider{
		ThreedProviderFal: func(model string) Provider {
			cfg := falCfg
			if model != "" {
				cfg.Model3D = model
			}
			return NewFalThreedProvider(fal.New(cfg))
		},
		ThreedProviderMeshy: func(model string) Provider { return NewMeshyThreedProvider(m, model) },
	}
}

// reservedThreed — a reservation wide enough for every candidate of these rigs: since FIX-F2 a fresh 3D
// submit with no reservation is refused before Choose looks at the route.
var reservedThreed = decimal.NullDecimal{Valid: true, Decimal: decimal.RequireFromString("5")}

func meshyAt(baseURL string) *meshy.Client {
	return meshy.New(meshy.Config{APIKey: "k", BaseURL: baseURL})
}

// ─────────────────────────── the chooser ───────────────────────────

// TestTheThreedChooserWALKS_THE_ROUTE_IN_ORDER — fal first by position, `tried` moves to meshy, the
// candidate's name is the provider's (attempt rows keep today's names), the slot's own name never is.
//
// MUTATION (measured red→green): Choose ignoring `tried` (the `if tried[c.Name()]` skip removed) → the
// second pick is fal again.
func TestTheThreedChooserWALKS_THE_ROUTE_IN_ORDER(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), meshyAt("http://127.0.0.1:1")),
		false, ThreedProviderMeshy)
	ch := routed.(Chooser)

	require.Equal(t, "threed", routed.Name(), "the slot's own name — never an attempt row's")
	require.True(t, routed.Enabled())
	require.Equal(t, []string{ContentTypeGLB, ContentTypePNG}, routed.Produces())

	p, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderFal, p.Name(), "position 1 first, whatever DESIGN_THREED_PROVIDER says")

	p, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, map[string]bool{ThreedProviderFal: true})
	require.NoError(t, err)
	require.Equal(t, ThreedProviderMeshy, p.Name(), "tried skips to the fallback")
	_, isCollector := p.(Collector)
	require.True(t, isCollector, "the candidate collects by delegation")
	require.Equal(t, ThreedProviderMeshy, ThreedRouteOf(p, false).Provider, "ThreedRouteOf sees through the wrapper")
}

// TestAnExhaustedThreedRoundSAYS_SO — every candidate that reads the run was tried: errChainExhausted, so
// the worker starts the next round at the top (dispatch.go), exactly as the image route.
//
// MUTATION (measured red→green): the `if served { return nil, errChainExhausted }` line removed → the
// exhausted round reads as option_not_read (terminal) and the seeded one-candidate route dies on its
// first retry.
func TestAnExhaustedThreedRoundSAYS_SO(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), meshyAt("http://127.0.0.1:1")),
		false, ThreedProviderFal).(Chooser)
	_, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed},
		map[string]bool{ThreedProviderFal: true, ThreedProviderMeshy: true})
	require.ErrorIs(t, err, errChainExhausted)
}

// TestTheEnvProviderIsTheCandidateONLY_WITHOUT_ROWS — a route with no rows is DESIGN_THREED_PROVIDER at its
// env model; a route with rows never adds it (meshy is the env default and is NOT a fallback of [fal]).
//
// MUTATION (measured red→green): read() without the `!hasRows` synthetic candidate → the empty route has
// nothing callable (Enabled false, errRouteMissing).
func TestTheEnvProviderIsTheCandidateONLY_WITHOUT_ROWS(t *testing.T) {
	rg := newThreedRouteRig(t) // no rows
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), meshyAt("http://127.0.0.1:1")),
		false, ThreedProviderMeshy)
	ch := routed.(Chooser)
	require.True(t, routed.Enabled())
	p, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderMeshy, p.Name(), "no rows: the env default")

	rg.store.routeTo(entity.AIPurposeThreed, row(1, entity.AIProviderFal, ""))
	rg.reload(t)
	p, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderFal, p.Name(), "rows: the route, not the env")
	_, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, map[string]bool{ThreedProviderFal: true})
	require.ErrorIs(t, err, errChainExhausted, "the env default is not appended to a routed chain")
}

// TestAnUnboundedThreedCandidateIS_SKIPPED — fal with FAL_UNIT_USD and no FAL_UNITS_CEILING_3D has no
// reserve number: skipped with ONE warning per config version, meshy pays instead, and the door's view
// reserves meshy's number. Alone on the route it closes the door in words (Closed, today's sentence) while
// the kind stays «configured» (Enabled — the gate would otherwise answer kind_not_available first), and
// Choose still refuses it free.
//
// MUTATION (measured red→green): bounded() answering true → fal is chosen and paid.
func TestAnUnboundedThreedCandidateIS_SKIPPED(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	cfg := falCfgAt("http://127.0.0.1:1")
	cfg.UnitUSD = 0.5 // a tariff, no units ceiling
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(cfg, meshyAt("http://127.0.0.1:1")), false, ThreedProviderFal)
	ch := routed.(Chooser)
	logs := captureSlog(t)

	for i := 0; i < 3; i++ {
		p, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
		require.NoError(t, err)
		require.Equal(t, ThreedProviderMeshy, p.Name(), "the unbounded primary is never paid")
	}
	require.Equal(t, 1, strings.Count(logs.String(), "3D candidate skipped: no reserve number"), logs.String())
	require.Contains(t, logs.String(), "FAL_UNITS_CEILING_3D")
	v := routed.View()
	require.Equal(t, ThreedProviderMeshy, v.Head.Provider)
	require.Empty(t, v.Closed)

	rg.store.routeTo(entity.AIPurposeThreed, row(1, entity.AIProviderFal, ""))
	rg.reload(t)
	require.True(t, routed.Enabled(), "keyed and unbounded: a closed door, not an unconfigured kind")
	v = routed.View()
	require.Contains(t, v.Closed, "FAL_UNIT_USD is set and FAL_UNITS_CEILING_3D is not")
	require.Equal(t, ThreedProviderFal, v.Head.Provider)
	_, ok := v.CeilingUSD("", "")
	require.False(t, ok, "nothing to reserve on")
	_, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.ErrorIs(t, err, errRouteMissing, "the worker never pays an unbounded route")
	require.Contains(t, err.Error(), "FAL_UNITS_CEILING_3D")
}

// TestAThreedCandidateThatDropsAnOptionIS_SKIPPED — fal at the hitem3d slug reads no build options: a
// detailed run is paid on meshy; with fal alone it is refused free, terminal, in the door's own word.
//
// MUTATION (measured red→green): Choose without the unread check → the detailed run is sent to hitem3d,
// which drops «detailed» (and the lone-fal route is not refused).
func TestAThreedCandidateThatDropsAnOptionIS_SKIPPED(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, hitemSlug), row(2, entity.AIProviderMeshy, ""))
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), meshyAt("http://127.0.0.1:1")),
		false, ThreedProviderFal).(Chooser)
	detailed := Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed, ThreedQuality: fal.QualityDetailed}

	p, err := ch.Choose(detailed, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderMeshy, p.Name())
	p, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderFal, p.Name(), "a run stating nothing is read by the hitem3d row")

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
// view still reserves on it. With a second candidate the chain simply moves on.
//
// MUTATION (measured red→green): read() dropping the held rows → the lone-fal route reads as
// unconfigured (errRouteMissing, terminal).
func TestAPausedThreedRouteWAITS(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), meshyAt("http://127.0.0.1:1")),
		false, ThreedProviderFal)
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

	rg.store.routeTo(entity.AIPurposeThreed, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	rg.reload(t)
	p, err := ch.Choose(Job{Kind: entity.DesignRunKindThreed, ThreedReservedUSD: reservedThreed}, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderMeshy, p.Name(), "the held primary is passed; the fallback pays")
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
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt(stand.srv.URL), meshyAt("http://127.0.0.1:1")),
		false, ThreedProviderFal).(Chooser)
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

// TestTheThreedViewREADS_THE_LIVE_ROUTE — the door's one value, per case: Head = the first callable
// candidate's route; Ceiling = the MAX over the chain (the reserve covers whichever candidate pays);
// a keyless head names its key and closes nothing (the kind gate refuses it, as before).
//
// MUTATION (measured red→green): View's Ceiling returning the head's ceiling only → the [fal, meshy at
// $0.05/credit] chain reserves fal's 1.2 against meshy's 1.5 booking.
func TestTheThreedViewREADS_THE_LIVE_ROUTE(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	// The key through the registry, as app.go wires it: a provider switched off in the panel is keyless.
	m := meshy.New(meshy.Config{KeyFunc: rg.reg.KeyFunc(entity.AIProviderMeshy), BaseURL: "http://127.0.0.1:1",
		CreditUSD: 0.05})
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), m), false, ThreedProviderFal)

	v := routed.View()
	require.Equal(t, ThreedProviderFal, v.Head.Provider, "the first callable candidate")
	require.Equal(t, []string{ThreedOptionTexture, ThreedOptionQuality, ThreedOptionSurfaceHint}, v.Head.Options)
	require.Empty(t, v.Closed)
	c, ok := v.CeilingUSD("", "")
	require.True(t, ok)
	require.Equal(t, "1.5", c.String(), "max(fal 1.20, meshy 30 credits × $0.05)")
	c, _ = v.CeilingUSD("", fal.QualityDetailed)
	require.Equal(t, "1.75", c.String(), "max(fal 1.40, meshy 35 × $0.05)")

	t.Run("a keyless head names its key and closes nothing", func(t *testing.T) {
		rg.store.routeTo(entity.AIPurposeThreed, row(1, entity.AIProviderMeshy, ""))
		rg.store.switchProvider(entity.AIProviderMeshy, false)
		rg.reload(t)
		require.False(t, routed.Enabled())
		v := routed.View()
		require.Equal(t, ThreedProviderMeshy, v.Head.Provider, "the configured head, so the door can name it")
		require.Empty(t, v.Closed)
		_, ok := v.CeilingUSD("", "")
		require.False(t, ok)
		require.Contains(t, missingCredential(routed), "no key for meshy")
	})
}

// ─────────────────────────── the worker ───────────────────────────

// threedWorker — the worker over the fakes with the routed 3D slot, both boot-time providers in Also,
// and a fixed clock.
func threedWorker(st *fakeStore, routed Provider, also ...Provider) *Worker {
	w := testWorker(st, media(21, 22), newFakeSink(ContentTypeGLB, ContentTypePNG),
		Providers{Threed: routed, Also: also})
	at := time.Date(2026, 9, 28, 13, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return at }
	return w
}

// TestA402OnFalPAYS_MESHY_ON_THE_NEXT_PASS — the fal account is empty: pass 1 is refused (402, not
// engaged), nothing is bought and the run goes straight back to the queue; pass 2 opens a NEW attempt on
// meshy and submits there. Two attempt rows, two concrete names — the fallback is never a second call
// inside one Execute.
//
// MUTATION (measured red→green): settle without the chain (the `chain.next` advance branch removed) →
// pass 1 closes the run terminally as provider_out_of_credit and meshy is never paid.
func TestA402OnFalPAYS_MESHY_ON_THE_NEXT_PASS(t *testing.T) {
	falStand := newRecStand(t, jsonAnswer(http.StatusPaymentRequired, `{"detail":"insufficient balance"}`))
	meshyStand := newRecStand(t, jsonAnswer(http.StatusOK, `{"result":"task-777"}`))
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	m := meshyAt(meshyStand.srv.URL)
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt(falStand.srv.URL), m), false, ThreedProviderFal)
	st := &fakeStore{}
	w := threedWorker(st, routed)
	run := reservedSteerRun(90)
	logs := captureSlog(t)

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, falStand.posts())
	require.Zero(t, meshyStand.posts(), "the fallback is the NEXT pass's")
	require.Equal(t, []string{ThreedProviderFal}, startedProviders(st))
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable, "a 402 on fal says nothing about meshy")
	require.Equal(t, w.now(), st.failed[0].NextAttempt, "failRunAt(now): the re-queue is immediate")
	require.Equal(t, CodeOutOfCredit, st.failed[0].ErrorCode)
	require.Contains(t, logs.String(), "from=fal")
	require.Contains(t, logs.String(), "to=meshy")

	st.getRun = historyOf(st, run)
	_ = w.execute(context.Background(), run, "tok")
	require.Equal(t, 1, falStand.posts(), "fal is not asked again in this round")
	require.Equal(t, 1, meshyStand.posts())
	require.Equal(t, []string{ThreedProviderFal, ThreedProviderMeshy, ThreedProviderMeshy}, startedProviders(st),
		"attempt 2 submits on meshy; attempt 3 is its free collect")
	require.Equal(t, entity.DesignAttemptAccepted, st.finished[1].State)
	require.Equal(t, "task-777", st.finished[1].ProviderRequestId)
}

// TestAnEngagedFalTimeoutNEVER_FALLS_BACK — fal read the whole submit and never answered: the build may
// be queued and billed (engaged). Terminal `unknown`, and meshy is never called — it would buy the model
// a second time (D-16: image/3D never fall back after money may have moved).
//
// MUTATION (measured red→green): candidateChain.next without `|| aiprov.Engaged(callErr)` → the run is
// re-queued retryable for meshy.
func TestAnEngagedFalTimeoutNEVER_FALLS_BACK(t *testing.T) {
	falStand := newRecStand(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // the body is read; the answer never comes
	})
	meshyStand := newRecStand(t, jsonAnswer(http.StatusOK, `{"result":"task-777"}`))
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	cfg := falCfgAt(falStand.srv.URL)
	cfg.HTTPTimeout = 150 * time.Millisecond
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(cfg, meshyAt(meshyStand.srv.URL)), false, ThreedProviderFal)
	st := &fakeStore{}
	w := threedWorker(st, routed)
	run := reservedSteerRun(91)

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, falStand.posts())
	require.Len(t, st.failed, 1)
	require.False(t, st.failed[0].Retryable, "money may have moved: terminal")
	require.Equal(t, CodeSubmitUnconfirmed, st.failed[0].ErrorCode)
	require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)
	require.Zero(t, meshyStand.posts(), "meshy is never called")
}

// TestAResumeAfterARouteEditCOLLECTS_WITH_THE_STORED_PROVIDER — meshy accepted a build (attempt 1,
// `meshy`, task-9); the owner then routed 3D to fal only. The next pass collects task-9 from MESHY for
// free — through Providers.Also — and never asks fal (which has never seen that id).
//
// MUTATION (measured red→green): resumeRoute collecting with the kind's route (preflight(run.Kind))
// instead of the stored name → the routed slot is asked to collect, fails, and meshy is never polled.
func TestAResumeAfterARouteEditCOLLECTS_WITH_THE_STORED_PROVIDER(t *testing.T) {
	falStand := newRecStand(t, jsonAnswer(http.StatusOK, `{"request_id":"req-1"}`))
	meshyStand := newRecStand(t, jsonAnswer(http.StatusOK, `{"result":"task-9"}`))
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""))
	falCfg := falCfgAt(falStand.srv.URL)
	m := meshyAt(meshyStand.srv.URL)
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfg, m), false, ThreedProviderFal)
	st := &fakeStore{}
	w := threedWorker(st, routed, NewFalThreedProvider(fal.New(falCfg)), NewThreedProvider(m))
	run := reservedSteerRun(92)
	full := run
	full.Attempts = []entity.DesignRunAttempt{{
		AttemptNo: 1, Provider: ThreedProviderMeshy, State: entity.DesignAttemptAccepted,
		ProviderRequestId: sql.NullString{String: "task-9", Valid: true},
	}}
	st.getRun = &full

	_ = w.execute(context.Background(), run, "tok")
	require.Equal(t, []string{ThreedProviderMeshy}, startedProviders(st), "the collect's attempt names meshy")
	require.Contains(t, meshyStand.requests(), "GET /openapi/v1/multi-image-to-3d/task-9", "meshy is polled")
	require.Empty(t, falStand.requests(), "fal is never asked about a meshy task")
	for _, f := range st.failed {
		require.NotEqual(t, CodeKindNotAvailable, f.ErrorCode, "a paid build is collected, not refused")
	}
}

// TestTheThreedCandidateRECORDS_WHAT_IT_SENDS — the history row of a routed 3D run carries the SURFACE
// STEER the chosen candidate sends, never the run's whole composed prompt (the garment note about the back
// reaches no texturing stage): the wrapper passes PromptCarrier through, and the ledger row of Meshy
// names the route row's ai_model.
//
// MUTATION (measured red→green): threedCandidate without SentPrompt → design_run.prompt records the
// composed prompt, «crossed straps on the back» included.
func TestTheThreedCandidateRECORDS_WHAT_IT_SENDS(t *testing.T) {
	meshyStand := newRecStand(t, jsonAnswer(http.StatusOK, `{"result":"task-777"}`))
	rg := newThreedRouteRig(t, row(1, entity.AIProviderMeshy, "meshy-6"))
	routed := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), meshyAt(meshyStand.srv.URL)),
		false, ThreedProviderFal)
	st := &fakeStore{}
	w := threedWorker(st, routed)
	ai := withLedger(w)

	_ = w.execute(context.Background(), reservedSteerRun(93), "tok")
	require.Len(t, st.recordedPrompts, 1)
	require.NotEmpty(t, st.recordedPrompts[0])
	require.NotContains(t, st.recordedPrompts[0], "crossed straps", "the surface steer, not the composed prompt")
	require.Equal(t, 1, meshyStand.posts())
	require.Contains(t, meshyStand.raw[0], `"ai_model":"meshy-6"`, "the route row's model reaches Meshy's body")
	require.Contains(t, st.recordedPrompts[0], "matte heavy jersey")
	rows := ai.Rows()
	require.NotEmpty(t, rows)
	require.Equal(t, "meshy-6", rows[0].Start.Model, "the ledger row names what was asked")
}

// TestADearerCandidateNEVER_EXCEEDS_THE_RESERVATION — Codex REVIEW-F1 #1: the door reserved 1.20 (fal alone);
// meshy (30 credits × $0.05 = 1.50) joined the route since. fal is still chosen; meshy is skipped as over the
// reservation; a route of meshy alone refuses free and terminal (threed_reserve_unbounded), never pays 1.50
// against 1.20.
//
// MUTATION (measured red→green): the `over` skip in Choose removed → meshy is chosen for the 1.20 run.
func TestADearerCandidateNEVER_EXCEEDS_THE_RESERVATION(t *testing.T) {
	rg := newThreedRouteRig(t, row(1, entity.AIProviderFal, ""), row(2, entity.AIProviderMeshy, ""))
	m := meshy.New(meshy.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1", CreditUSD: 0.05})
	ch := NewRoutedThreedProvider(rg.reg, threedFactoriesAt(falCfgAt("http://127.0.0.1:1"), m), false, ThreedProviderFal).(Chooser)
	reserved := Job{Kind: entity.DesignRunKindThreed,
		ThreedReservedUSD: decimal.NullDecimal{Valid: true, Decimal: decimal.RequireFromString("1.20")}}

	p, err := ch.Choose(reserved, nil)
	require.NoError(t, err)
	require.Equal(t, ThreedProviderFal, p.Name())
	_, err = ch.Choose(reserved, map[string]bool{ThreedProviderFal: true})
	require.ErrorIs(t, err, errChainExhausted, "meshy at 1.50 is not a fallback for a 1.20 reservation")

	rg.store.routeTo(entity.AIPurposeThreed, row(1, entity.AIProviderMeshy, ""))
	rg.reload(t)
	_, err = ch.Choose(reserved, nil)
	require.ErrorIs(t, err, errThreedReserveShort)
	require.Contains(t, err.Error(), "more than the 1.2 USD reserved")
	v := classify(err)
	require.Equal(t, CodeThreedReserveShort, v.Code)
	require.False(t, v.Retryable)
	_, err = ch.Choose(Job{Kind: entity.DesignRunKindThreed}, nil)
	require.ErrorIs(t, err, errThreedReserveShort, "a run with no reservation is never paid (Codex REVIEW-F2 P1-3)")
	require.Contains(t, err.Error(), "carries no reservation")
}
