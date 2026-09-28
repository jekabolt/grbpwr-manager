package designgen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ B-13 — THE IMAGE ROUTE IS REAL: ONE CANDIDATE PER PASS, A FALLBACK ONLY WHERE NO MONEY MOVED ═══
//
// Every test here runs the REAL registry (its snapshot, its version, its breakers) over an in-memory
// config — the router tests' rig — and the real worker over the fake store. Candidate 1 is the real
// orimages client against an httptest stand wherever the failure's shape matters (its CallError is
// the transport's own); candidate 2 is a fake transport keyed `openai`, the provider commit F wires.

// routeCfgStore — aiprovtest.Store with a live config half: the registry reads cfg, a test edits it
// the way a write RPC does (the change and a version bump).
type routeCfgStore struct {
	*aiprovtest.Store
	mu  sync.Mutex
	cfg entity.AIConfig
}

func (s *routeCfgStore) GetConfig(context.Context) (*entity.AIConfig, error) {
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

func (s *routeCfgStore) ConfigVersion(context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Settings.ConfigVersion, nil
}

// routeImagesTo replaces image.generate's route and bumps the version, as SetRoute does.
func (s *routeCfgStore) routeImagesTo(cands ...entity.AIRouteCandidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Routes {
		if s.cfg.Routes[i].Purpose == entity.AIPurposeImageGenerate {
			s.cfg.Routes[i].Candidates = cands
		}
	}
	s.cfg.Settings.ConfigVersion++
}

// switchProvider turns a provider on or off in the panel and bumps the version, as SetProvider does.
func (s *routeCfgStore) switchProvider(pk string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Providers {
		if s.cfg.Providers[i].Key == pk {
			s.cfg.Providers[i].Enabled = on
		}
	}
	s.cfg.Settings.ConfigVersion++
}

// routeClock — the registry's one clock (the breaker window).
type routeClock struct{ ns atomic.Int64 }

func (c *routeClock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *routeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

type imageRouteRig struct {
	store *routeCfgStore
	reg   *registry.Registry
	clk   *routeClock
}

func row(pos int, pk, model string) entity.AIRouteCandidate {
	return entity.AIRouteCandidate{Position: pos, ProviderKey: pk, Model: model}
}

// newImageRouteRig: openrouter, openai and google on with sealed database keys — the only key source
// since B-33 (google has no image transport in any rig — «routed before its adapter exists»); every
// other provider off. image.generate is routed to cands; every other purpose keeps the 0373 seed.
func newImageRouteRig(t *testing.T, cands ...entity.AIRouteCandidate) *imageRouteRig {
	t.Helper()
	ring, err := keyring.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	require.NoError(t, err)
	sealed := map[string]bool{entity.AIProviderOpenRouter: true, entity.AIProviderOpenAI: true, entity.AIProviderGoogle: true}
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		p := entity.AIProvider{Key: k, Label: k, Enabled: sealed[k]}
		if sealed[k] {
			blob, err := ring.Seal("sk-"+k+"-test-1234", keyring.AAD(k, string(entity.AIKeyAPI)))
			require.NoError(t, err)
			p.APIKeyEnc = blob
		}
		cfg.Providers = append(cfg.Providers, p)
	}
	for _, purpose := range entity.AIPurposes() {
		rc := []entity.AIRouteCandidate{row(1, entity.AIProviderOpenRouter, "")}
		if purpose == entity.AIPurposeImageGenerate {
			rc = cands
		}
		cfg.Routes = append(cfg.Routes, entity.AIRoute{Purpose: purpose, Candidates: rc})
	}
	cfg.Settings = entity.AISettings{ConfigVersion: 1,
		DefaultChatProviderKey: entity.AIProviderOpenRouter, DefaultImageProviderKey: entity.AIProviderOpenRouter}
	st := &routeCfgStore{Store: &aiprovtest.Store{}, cfg: cfg}
	clk := &routeClock{}
	clk.ns.Store(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC).UnixNano())
	reg := registry.New(st, ring, registry.EnvKeys{OpenRouter: "env-openrouter-aaaa", OpenRouterImages: "env-images-bbbb"},
		registry.WithClock(clk.now))
	require.NoError(t, reg.Reload(context.Background()))
	return &imageRouteRig{store: st, reg: reg, clk: clk}
}

func (rg *imageRouteRig) reload(t *testing.T) {
	t.Helper()
	require.NoError(t, rg.reg.Reload(context.Background()))
}

// fakeImageTransport — an image transport that records every request and answers with `answer`
// (one PNG at $0.04 when nil).
type fakeImageTransport struct {
	model  string
	off    bool
	only   map[string]bool // the slugs it draws; nil = every non-empty slug
	answer func(orimages.Request) (*orimages.Result, error)

	mu    sync.Mutex
	calls []orimages.Request
}

func (f *fakeImageTransport) Generate(_ context.Context, req orimages.Request) (*orimages.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	answer := f.answer
	f.mu.Unlock()
	if answer != nil {
		return answer(req)
	}
	png, _ := base64.StdEncoding.DecodeString(pngB64)
	return &orimages.Result{Model: firstNonEmpty(req.Model, f.model),
		Images: []orimages.Image{{Bytes: png, MediaType: ContentTypePNG}},
		Usage:  orimages.Usage{Prompt: 10, Completion: 20, Cost: 0.04}}, nil
}
func (f *fakeImageTransport) Model() string { return f.model }
func (f *fakeImageTransport) Enabled() bool { return !f.off }
func (f *fakeImageTransport) Serves(slug string) bool {
	if f.only == nil {
		return strings.TrimSpace(slug) != ""
	}
	return f.only[slug]
}
func (f *fakeImageTransport) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// statusImageStand is OpenRouter's POST /images answering `status` with `body`, counting calls.
func statusImageStand(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func orClient(baseURL string) *orimages.Client {
	return orimages.New(orimages.Config{APIKey: "k", BaseURL: baseURL, HTTPTimeout: 2 * time.Second})
}

// routedWorker — the worker over the fakes with the routed image slot and a fixed clock.
func routedWorker(st *fakeStore, reg *registry.Registry, transports map[string]ImageTransport) *Worker {
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{
		Image: NewRoutedImageProvider(reg, transports, orimages.DefaultModel),
	})
	at := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return at }
	return w
}

// historyOf — what GetRun hands the next pass: the attempts this fake store has seen opened and closed.
func historyOf(st *fakeStore, run entity.DesignRun) *entity.DesignRun {
	full := run
	for i, s := range st.started {
		a := entity.DesignRunAttempt{RunId: run.Id, AttemptNo: i + 1, Provider: s.Provider}
		if i < len(st.finished) {
			a.State = st.finished[i].State
		}
		full.Attempts = append(full.Attempts, a)
	}
	return &full
}

func startedProviders(st *fakeStore) []string {
	out := []string{}
	for _, s := range st.started {
		out = append(out, s.Provider)
	}
	return out
}

// (a) TestTheImageChooserWALKS_THE_ROUTE_IN_ORDER — position order, `tried` skips, a round that is over
// says so, a transport that is off or does not draw the slug is passed like a missing one, and a
// provider with no transport in this build is skipped with ONE warning per config version.
//
// MUTATIONS (measured red→green): warnNoTransport warning on every call (the `seen && version <= v`
// early return removed) → «one warning per snapshot» reads 5; the memory back to last-seen (`==`) →
// the stale-snapshot row warns twice (FIX-G4, mirrored from the router); Choose ignoring `tried` → the second
// pick is openrouter again; Choose skipping the Serves check → the Gemini run picks openai and the
// lone-openai run is not refused.
func TestTheImageChooserWALKS_THE_ROUTE_IN_ORDER(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, "openai/gpt-image-2"))
	or := &fakeImageTransport{model: orimages.DefaultModel}
	oa := &fakeImageTransport{model: "gpt-image-2", only: map[string]bool{"openai/gpt-image-2": true}}
	routed := NewRoutedImageProvider(rg.reg, map[string]ImageTransport{
		entity.AIProviderOpenRouter: or, entity.AIProviderOpenAI: oa}, orimages.DefaultModel)
	ch := routed.(Chooser)

	require.Equal(t, "image", routed.Name(), "the slot's own name — never an attempt row's")
	require.True(t, routed.Enabled())
	require.Equal(t, []string{ContentTypePNG}, routed.Produces())

	p, err := ch.Choose(Job{Kind: entity.DesignRunKindFlat}, nil)
	require.NoError(t, err)
	require.Equal(t, "openrouter_images", p.Name(), "the primary first; OpenRouter keeps today's row name")

	p, err = ch.Choose(Job{Kind: entity.DesignRunKindFlat}, map[string]bool{"openrouter_images": true})
	require.NoError(t, err)
	require.Equal(t, "openai_images", p.Name(), "tried skips to the fallback")

	_, err = ch.Choose(Job{Kind: entity.DesignRunKindFlat}, map[string]bool{"openrouter_images": true, "openai_images": true})
	require.ErrorIs(t, err, errChainExhausted, "both tried: the round is over")

	t.Run("a transport that is off is passed over", func(t *testing.T) {
		or.off = true
		defer func() { or.off = false }()
		p, err := ch.Choose(Job{Kind: entity.DesignRunKindFlat}, nil)
		require.NoError(t, err)
		require.Equal(t, "openai_images", p.Name())
	})

	t.Run("a candidate that does not draw the frozen slug is passed like a missing transport", func(t *testing.T) {
		p, err := ch.Choose(Job{Kind: entity.DesignRunKindFlat, Model: EngineGemini3Pro}, nil)
		require.NoError(t, err)
		require.Equal(t, "openrouter_images", p.Name())
		_, err = ch.Choose(Job{Kind: entity.DesignRunKindFlat, Model: EngineGemini3Pro}, map[string]bool{"openrouter_images": true})
		require.ErrorIs(t, err, errChainExhausted, "openai does not draw Gemini: nobody left in this round")

		or.off = true
		defer func() { or.off = false }()
		_, err = ch.Choose(Job{Kind: entity.DesignRunKindFlat, Model: EngineGemini3Pro}, nil)
		require.ErrorIs(t, err, errNoCandidateServes)
		v := classify(err)
		require.Equal(t, CodeUnknownImageModel, v.Code, "the door's own word")
		require.False(t, v.Retryable)
	})

	t.Run("a provider with no transport is skipped, one warning per config version", func(t *testing.T) {
		logs := captureSlog(t)
		rg.store.routeImagesTo(row(1, entity.AIProviderGoogle, ""), row(2, entity.AIProviderOpenRouter, ""))
		rg.reload(t)
		for i := 0; i < 5; i++ {
			p, err := ch.Choose(Job{Kind: entity.DesignRunKindFlat}, nil)
			require.NoError(t, err)
			require.Equal(t, "openrouter_images", p.Name())
			require.True(t, routed.Enabled())
		}
		require.Equal(t, 1, strings.Count(logs.String(), "no image transport in this build"), logs.String())
		require.Contains(t, logs.String(), "provider=google")

		rg.store.routeImagesTo(row(1, entity.AIProviderGoogle, ""), row(2, entity.AIProviderOpenRouter, ""))
		rg.reload(t)
		_, _ = ch.Choose(Job{Kind: entity.DesignRunKindFlat}, nil)
		require.Equal(t, 2, strings.Count(logs.String(), "no image transport in this build"), "a new snapshot, a new warning")
	})

	t.Run("the warn-once memory is monotonic: a stale snapshot neither warns nor rolls it back", func(t *testing.T) {
		logs := captureSlog(t)
		rp := routed.(*routedImageProvider)
		rp.warnNoTransport("apibost", 7)
		rp.warnNoTransport("apibost", 6) // a pass that read the older snapshot finishes late
		rp.warnNoTransport("apibost", 7)
		require.Equal(t, 1, strings.Count(logs.String(), "provider=apibost"), logs.String())
		rp.warnNoTransport("apibost", 8)
		require.Equal(t, 2, strings.Count(logs.String(), "provider=apibost"), "a newer snapshot warns again")
	})

	t.Run("a route of providers this build cannot draw with is closed at the door, in words", func(t *testing.T) {
		rg.store.routeImagesTo(row(1, entity.AIProviderGoogle, ""))
		rg.reload(t)
		defer func() {
			rg.store.routeImagesTo(row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, "openai/gpt-image-2"))
			rg.reload(t)
		}()
		require.False(t, routed.Enabled())
		w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{Image: routed})
		err := w.PreflightKind(entity.DesignRunKindFlat)
		require.Error(t, err)
		require.Contains(t, err.Error(), "routed to google")
		require.Contains(t, err.Error(), "no image transport")
	})
}

// TestTheRouteSlugFILLS_ONLY_A_RUN_THAT_NAMED_NONE — the seeded route (openrouter, no model) sends
// today's request byte for byte (no model on the wire: the client's own default); a route row's model
// is what an unnamed run is drawn by; a frozen params.image.model wins over the route's.
//
// MUTATION (measured red→green): the request's Model back to `job.Model` → the route-slug case sends
// "" (the client's default instead of the owner's route).
func TestTheRouteSlugFILLS_ONLY_A_RUN_THAT_NAMED_NONE(t *testing.T) {
	for _, c := range []struct {
		name, routeModel, frozen, wantWire, wantOutcome string
	}{
		{"the seeded route: today's request", "", "", "", orimages.DefaultModel},
		{"a route slug draws an unnamed run", EngineGPTImage25, "", EngineGPTImage25, EngineGPTImage25},
		{"a frozen slug wins over the route's", EngineGPTImage25, EngineGPTImage2, EngineGPTImage2, EngineGPTImage2},
	} {
		t.Run(c.name, func(t *testing.T) {
			rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, c.routeModel))
			or := &fakeImageTransport{model: orimages.DefaultModel}
			routed := NewRoutedImageProvider(rg.reg, map[string]ImageTransport{entity.AIProviderOpenRouter: or}, orimages.DefaultModel)
			p, err := routed.(Chooser).Choose(Job{Kind: entity.DesignRunKindFlat, Model: c.frozen}, nil)
			require.NoError(t, err)
			out, err := p.Execute(context.Background(), Job{Kind: entity.DesignRunKindFlat, Prompt: "a flat", Model: c.frozen})
			require.NoError(t, err)
			require.Len(t, or.calls, 1)
			require.Equal(t, c.wantWire, or.calls[0].Model)
			require.Equal(t, c.wantOutcome, out.Model)
			require.Equal(t, entity.AIProviderOpenRouter, out.Provider)
		})
	}
}

// (b) TestA402OnTheFirstCandidateIS_A_NEW_ATTEMPT_ON_THE_SECOND — G-04's live scenario: the primary's
// account is empty. Pass 1 pays nothing (402, not engaged) and re-queues the run at once; pass 2 opens
// a NEW attempt on the fallback and delivers. Two attempt rows, two concrete names, two ledger rows
// (`free`, `ok`) — the fallback is never a second call hidden inside one Execute.
//
// MUTATION (measured red→green): settle without the chain (the advance branch removed) → pass 1 closes
// the run terminally as provider_out_of_credit and pass 2 never happens.
func TestA402OnTheFirstCandidateIS_A_NEW_ATTEMPT_ON_THE_SECOND(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, "openai/gpt-image-2"))
	srv, orCalls := statusImageStand(t, http.StatusPaymentRequired, `{"error":{"message":"insufficient credits"}}`)
	oa := &fakeImageTransport{model: "gpt-image-2"}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderOpenRouter: orClient(srv.URL), entity.AIProviderOpenAI: oa})
	ai := withLedger(w)
	run := testRun(80, entity.DesignRunKindFlat)
	run.Author = "im"
	logs := captureSlog(t)

	// ─── pass 1: the primary refuses — nothing bought, straight back to the queue.
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.EqualValues(t, 1, orCalls.Load())
	require.Zero(t, oa.n(), "the fallback is the NEXT pass's, never a second call in this one")
	require.Equal(t, []string{"openrouter_images"}, startedProviders(st))
	require.Equal(t, entity.DesignAttemptFailed, st.finished[0].State)
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable, "a terminal code on one provider says nothing about the next")
	require.Equal(t, w.now(), st.failed[0].NextAttempt, "failRunAt(now): the re-queue is immediate")
	require.Equal(t, CodeOutOfCredit, st.failed[0].ErrorCode, "the history row says what happened to candidate 1")
	require.Contains(t, logs.String(), "designgen: candidate advanced")
	require.Contains(t, logs.String(), "from=openrouter_images")
	require.Contains(t, logs.String(), "to=openai_images")
	require.Contains(t, logs.String(), "code="+CodeOutOfCredit)

	// ─── pass 2: the history says openrouter was tried; the fallback pays on its own attempt.
	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.EqualValues(t, 1, orCalls.Load(), "the primary is not asked again in this round")
	require.Equal(t, 1, oa.n())
	require.Equal(t, []string{"openrouter_images", "openai_images"}, startedProviders(st))
	require.Len(t, st.completed, 1)
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[1].State)
	require.Equal(t, "0.04", st.finished[1].Price.Decimal.String())

	rows := ai.Rows()
	require.Len(t, rows, 2, "one ledger row per physical call")
	require.Equal(t, entity.AIProviderOpenRouter, rows[0].Start.ProviderKey)
	require.Equal(t, entity.AICallFree, rows[0].Status)
	require.Equal(t, intp(http.StatusPaymentRequired), rows[0].End.HTTPStatus)
	require.Equal(t, 1, *rows[0].Start.AttemptNo)
	require.Equal(t, entity.AIProviderOpenAI, rows[1].Start.ProviderKey)
	require.Equal(t, entity.AICallOK, rows[1].Status)
	require.Equal(t, 2, *rows[1].Start.AttemptNo)
	require.Equal(t, "openai/gpt-image-2", rows[1].Start.Model, "the fallback's own route slug")
}

// (c) TestAnEngagedFailureNEVER_FALLS_BACK — the primary may have BOUGHT the picture (a 2xx whose
// envelope broke): terminal `unknown`, and the fallback is never called — it would buy it again. The
// same for a failure no transport spoke for (no CallError): read as engaged.
//
// MUTATIONS (measured red→green): chain.next without `|| aiprov.Engaged(callErr)` → the engaged row
// advances (a second call on the next pass, the run re-queued retryable); chain.next without the
// `!spoke` half → the unclassified row advances.
func TestAnEngagedFailureNEVER_FALLS_BACK(t *testing.T) {
	t.Run("engaged: a 2xx whose envelope broke", func(t *testing.T) {
		rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, ""))
		srv, orCalls := statusImageStand(t, http.StatusOK, `{"data":`)
		oa := &fakeImageTransport{model: "gpt-image-2"}
		st := &fakeStore{}
		w := routedWorker(st, rg.reg, map[string]ImageTransport{
			entity.AIProviderOpenRouter: orClient(srv.URL), entity.AIProviderOpenAI: oa})
		run := testRun(81, entity.DesignRunKindFlat)

		require.NoError(t, w.execute(context.Background(), run, "tok"))
		require.EqualValues(t, 1, orCalls.Load())
		require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)
		require.Len(t, st.failed, 1)
		// The queue: a run that went back to it comes round again; a closed one does not.
		if st.failed[0].Retryable {
			st.getRun = historyOf(st, run)
			require.NoError(t, w.execute(context.Background(), run, "tok"))
		}
		require.Zero(t, oa.n(), "candidate 2 is never called: it would buy the picture a second time")
		require.Equal(t, []string{"openrouter_images"}, startedProviders(st))
		require.False(t, st.failed[0].Retryable, "money may have moved: terminal")
		require.True(t, st.failed[0].NextAttempt.IsZero())
	})

	t.Run("unclassified: an error no transport spoke for is read as engaged", func(t *testing.T) {
		rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, ""))
		or := &fakeImageTransport{model: orimages.DefaultModel, answer: func(orimages.Request) (*orimages.Result, error) {
			return nil, errors.New("connection reset by peer")
		}}
		oa := &fakeImageTransport{model: "gpt-image-2"}
		st := &fakeStore{}
		w := routedWorker(st, rg.reg, map[string]ImageTransport{
			entity.AIProviderOpenRouter: or, entity.AIProviderOpenAI: oa})

		require.NoError(t, w.execute(context.Background(), testRun(82, entity.DesignRunKindFlat), "tok"))
		require.Len(t, st.failed, 1)
		require.True(t, st.failed[0].NextAttempt.IsZero(), "today's path: the store's back-off, not an advance")
		require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)
		require.Zero(t, oa.n())
	})
}

// (d) TestAnExhaustedRoundTAKES_TODAYS_PATH — both candidates answer 503 (not engaged, weather): the
// primary advances at once, the fallback's failure has nobody left in the round and takes today's path
// (the store's back-off, the LAST transport's sentence on the row). The next pass starts a new round at
// the top, and the round after continues where it stood. A one-candidate route — the seeded one — keeps
// retrying its only candidate exactly as before B-13.
//
// MUTATIONS (measured red→green): execute failing the run on errChainExhausted instead of starting a
// new round → pass 3 closes the run `kind_not_available` and the one-candidate route dies on its first
// retry; roundTried without the reset on a repeat (every name ever tried) → pass 4 picks openrouter
// again instead of openai.
func TestAnExhaustedRoundTAKES_TODAYS_PATH(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, ""))
	srv, orCalls := statusImageStand(t, http.StatusServiceUnavailable, `{"error":{"message":"upstream A is down"}}`)
	oa := &fakeImageTransport{model: "gpt-image-2", answer: func(orimages.Request) (*orimages.Result, error) {
		return nil, &aiprov.CallError{Provider: entity.AIProviderOpenAI, Code: aiprov.CodeProviderError,
			HTTPStatus: 503, Retryable: true, Err: errors.New("openai: HTTP 503: upstream B is down")}
	}}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderOpenRouter: orClient(srv.URL), entity.AIProviderOpenAI: oa})
	run := testRun(83, entity.DesignRunKindFlat)

	// pass 1: the primary's weather advances the chain at once.
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, w.now(), st.failed[0].NextAttempt)

	// pass 2: the fallback's weather — nobody left in this round: the store's back-off.
	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, []string{"openrouter_images", "openai_images"}, startedProviders(st))
	require.Len(t, st.failed, 2)
	require.True(t, st.failed[1].Retryable, "503 is weather")
	require.True(t, st.failed[1].NextAttempt.IsZero(), "today's path: the back-off is the store's")
	require.Contains(t, st.failed[1].LastError, "upstream B is down", "the last transport's sentence")

	// pass 3: a new round, from the top.
	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.EqualValues(t, 2, orCalls.Load())
	require.Equal(t, "openrouter_images", st.started[2].Provider)

	// pass 4: the round goes on where it stood.
	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, "openai_images", st.started[3].Provider)

	t.Run("the seeded one-candidate route retries its only candidate, as before", func(t *testing.T) {
		one := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""))
		srv, calls := statusImageStand(t, http.StatusServiceUnavailable, `{"error":{"message":"down"}}`)
		st := &fakeStore{}
		w := routedWorker(st, one.reg, map[string]ImageTransport{entity.AIProviderOpenRouter: orClient(srv.URL)})
		run := testRun(84, entity.DesignRunKindFlat)
		for i := 0; i < 3; i++ {
			st.getRun = historyOf(st, run)
			require.NoError(t, w.execute(context.Background(), run, "tok"))
		}
		require.EqualValues(t, 3, calls.Load())
		for _, f := range st.failed {
			require.True(t, f.Retryable)
			require.Equal(t, CodeProviderUnavailable, f.ErrorCode)
			require.True(t, f.NextAttempt.IsZero(), "the store's back-off every time — no chain to advance")
		}
	})
}

// TestABreakerRefusalADVANCES_THE_CHAIN — the primary's image breaker is half-open with its one probe
// already out: this pass's admission is refused, nothing leaves (no ledger row, the stand untouched),
// and the refusal — not engaged, retryable — moves the run to the fallback at once. The router's
// contract, asked per physical call.
//
// MUTATION (measured red→green): imageProvider.admit always admitting → the stand is called while its
// breaker is probing.
func TestABreakerRefusalADVANCES_THE_CHAIN(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, ""))
	srv, orCalls := statusImageStand(t, http.StatusOK, `{"data":[]}`)
	weather := &aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: aiprov.CodeProviderError,
		HTTPStatus: 503, Retryable: true, Err: errors.New("down")}
	for i := 0; i < 3; i++ {
		a, ok := rg.reg.Admit(entity.AIProviderOpenRouter, entity.AICapabilityImage)
		require.True(t, ok)
		rg.reg.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityImage, a, weather)
	}
	rg.clk.advance(6 * time.Minute) // past the window: half-open, listed again
	_, probe := rg.reg.Admit(entity.AIProviderOpenRouter, entity.AICapabilityImage)
	require.True(t, probe, "somebody else holds the one probe")

	oa := &fakeImageTransport{model: "gpt-image-2"}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderOpenRouter: orClient(srv.URL), entity.AIProviderOpenAI: oa})
	ai := withLedger(w)
	run := testRun(85, entity.DesignRunKindFlat)

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Zero(t, orCalls.Load(), "a refused admission is a call that never left")
	require.Empty(t, ai.Rows(), "no call, no ledger row")
	require.Equal(t, []string{"openrouter_images"}, startedProviders(st))
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable)
	require.Equal(t, w.now(), st.failed[0].NextAttempt, "advanced at once")
	require.Equal(t, CodeRateLimited, st.failed[0].ErrorCode)

	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, oa.n())
	require.Len(t, st.completed, 1)
}

// (f) TestTheEngineTableFOLLOWS_THE_ROUTE_BUT_NEVER_BLANKS — EngineTableFunc: a route slug the table
// can default to is the default; a typo — and a catalogue slug that cannot be a default (Gemini) —
// keeps the env default, with ONE warning per distinct slug; no route slug is the env default.
//
// MUTATION (measured red→green): the `first` guard removed (warn on every read) → the typo's three
// reads log three warnings.
func TestTheEngineTableFOLLOWS_THE_ROUTE_BUT_NEVER_BLANKS(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""))
	tables := EngineTableFunc(rg.reg, orimages.DefaultModel, EngineFlags{Gemini: true})
	defaultOf := func() string {
		tb := tables()
		require.NotEmpty(t, tb, "the table is never blanked by the route")
		require.True(t, tb[0].IsDefault)
		return tb[0].Slug
	}
	logs := captureSlog(t)

	require.Equal(t, EngineGPTImage2, defaultOf(), "no route slug: the env default")

	rg.store.routeImagesTo(row(1, entity.AIProviderOpenRouter, EngineGPTImage25))
	rg.reload(t)
	require.Equal(t, EngineGPTImage25, defaultOf(), "the panel's slug is the default")

	rg.store.routeImagesTo(row(1, entity.AIProviderOpenRouter, "openai/gpt-image-3-typo"))
	rg.reload(t)
	for i := 0; i < 3; i++ {
		require.Equal(t, EngineGPTImage2, defaultOf(), "a typo keeps the env default")
	}
	require.Equal(t, 1, strings.Count(logs.String(), "route_model=openai/gpt-image-3-typo"), logs.String())

	rg.store.routeImagesTo(row(1, entity.AIProviderOpenRouter, EngineGemini3Pro))
	rg.reload(t)
	require.Equal(t, EngineGPTImage2, defaultOf(), "a catalogue row that cannot be the default does not blank it")
	require.Equal(t, 1, strings.Count(logs.String(), "route_model="+EngineGemini3Pro), "one warning per distinct slug")
	require.Equal(t, 2, strings.Count(logs.String(), "can offer as its default"))
}

// TestTheWorkerRESOLVES_AGAINST_THE_DOOR_S_TABLE — Config.Engines is what the worker reads at every
// pickup (the same function app.go hands the door): a frozen flagged engine whose row the function no
// longer lists is refused before the money, exactly as with the env-built table.
func TestTheWorkerRESOLVES_AGAINST_THE_DOOR_S_TABLE(t *testing.T) {
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.1)}
	st := &fakeStore{}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: img})
	calls := 0
	w.c.Engines = func() []Engine { calls++; return EngineTable("", EngineFlags{}) }
	run := testRun(86, entity.DesignRunKindFlat)
	run.Params = entity.RawJSON(fmt.Sprintf(`{"image":{"model":%q}}`, EngineGemini3Pro))

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, calls, "read once per pickup")
	require.Empty(t, img.calls, "the flag is off in the worker's table: refused before any money")
	require.Equal(t, []string{CodeUnknownImageModel + " retry=false"}, failedCodes(st))
}

// ═══ B-13/A5 — AN OPEN BREAKER PAUSES THE ROUTE; IT NEVER CLOSES THE RUN (Codex REVIEW-CD P1) ═══

// openImageBreaker records three transient, unengaged faults for pk's image capability — the third
// opens its breaker for the registry's window, at the registry clock's now.
func openImageBreaker(t *testing.T, rg *imageRouteRig, pk string) {
	t.Helper()
	weather := &aiprov.CallError{Provider: pk, Code: aiprov.CodeProviderError, HTTPStatus: 503, Retryable: true,
		Err: errors.New("down")}
	for i := 0; i < 3; i++ {
		a, ok := rg.reg.Admit(pk, entity.AICapabilityImage)
		require.True(t, ok)
		rg.reg.RecordFailure(pk, entity.AICapabilityImage, a, weather)
	}
	require.Equal(t, registry.BreakerOpen, rg.reg.BreakerState(pk, entity.AICapabilityImage))
}

// switchableImageStand is OpenRouter's POST /images answering `status` (an error body) until the test
// stores 200, then one PNG at $0.04.
func switchableImageStand(t *testing.T, status int) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var calls, answer atomic.Int32
	answer.Store(int32(status))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if s := int(answer.Load()); s != http.StatusOK {
			w.WriteHeader(s)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream is down"}}`))
			return
		}
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"media_type":"image/png"}],"usage":{"cost":0.04}}`, pngB64)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &answer
}

// TestAnOpenBreakerPAUSES_THE_ROUTE_IT_NEVER_CLOSES_IT — the seeded one-candidate route, three bare
// 503s: each is its own attempt (free, retryable, the store's back-off), and the third opens the image
// breaker. The next pickup INSIDE the window finds nothing callable — and before A5 read that as «not
// configured»: kind_not_available, terminal, the run closed before the breaker could let a probe
// through. Now the route is PAUSED: the door still accepts (the route exists), the pickup re-queues
// the run for the end of the window as provider_paused with NO attempt row (the paid ceiling, counted
// from attempt rows, is untouched) and no call, and after the window the probe delivers.
//
// MUTATIONS (measured red→green): paused() answering nothing (held read as disabled) → pass 4 closes
// kind_not_available retry=false; execute failing a paused run with the store's back-off (failRun)
// instead of the window → NextAttempt is zero; classify without the errRoutePaused arm → the row reads
// provider_unavailable, not provider_paused.
func TestAnOpenBreakerPAUSES_THE_ROUTE_IT_NEVER_CLOSES_IT(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""))
	srv, orCalls, answer := switchableImageStand(t, http.StatusServiceUnavailable)
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{entity.AIProviderOpenRouter: orClient(srv.URL)})
	ai := withLedger(w)
	run := testRun(90, entity.DesignRunKindFlat)

	// ─── passes 1–3: weather, one attempt each; the third opens the breaker.
	for i := 0; i < 3; i++ {
		st.getRun = historyOf(st, run)
		require.NoError(t, w.execute(context.Background(), run, "tok"))
	}
	require.EqualValues(t, 3, orCalls.Load())
	require.Equal(t, []string{"openrouter_images", "openrouter_images", "openrouter_images"}, startedProviders(st))
	require.Equal(t, []string{CodeProviderUnavailable + " retry=true", CodeProviderUnavailable + " retry=true",
		CodeProviderUnavailable + " retry=true"}, failedCodes(st))
	require.Equal(t, registry.BreakerOpen, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityImage))
	require.Len(t, ai.Rows(), 3)

	// ─── inside the window: the route exists, it is paused — never «not configured».
	require.Empty(t, rg.reg.Candidates(entity.AIPurposeImageGenerate), "the registry lists nothing callable")
	require.NoError(t, w.PreflightKind(entity.DesignRunKindFlat), "the door accepts: the route is configured")
	msg := w.providers.Image.(CredentialNamer).MissingCredential()
	require.Contains(t, msg, "paused after repeated failures")
	require.Contains(t, msg, "openrouter")
	require.NotContains(t, msg, "set it in admin", "a paused route does not send the owner to re-type a key")

	// ─── pass 4: re-queued for the end of the window; no attempt row, no call, no ledger row.
	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.EqualValues(t, 3, orCalls.Load(), "nothing is sent while the breaker is open")
	require.Len(t, st.started, 3, "no attempt row: the paid ceiling is not spent on a wait")
	require.Len(t, ai.Rows(), 3, "no call, no ledger row")
	require.Len(t, st.failed, 4)
	require.Equal(t, CodeProviderPaused+" retry=true", failedCodes(st)[3])
	require.Equal(t, w.now().Add(routePauseRequeue), st.failed[3].NextAttempt, "back at the end of the breaker window")
	require.Contains(t, st.failed[3].LastError, "paused after repeated failures")
	require.Contains(t, st.failed[3].LastError, "openrouter")

	// ─── after the window: half-open, listed again; the probe goes out and delivers.
	rg.clk.advance(routePauseRequeue)
	answer.Store(http.StatusOK)
	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.EqualValues(t, 4, orCalls.Load())
	require.Len(t, st.started, 4)
	require.Len(t, st.completed, 1, "the run the breaker paused is delivered, not lost")
	require.Equal(t, registry.BreakerClosed, rg.reg.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityImage),
		"the probe's success closed the breaker")
	rows := ai.Rows()
	require.Len(t, rows, 4)
	require.Equal(t, entity.AICallOK, rows[3].Status)
}

// TestAKeylessRouteSTILL_CLOSES_AS_NOT_CONFIGURED — the pause is ONLY «held by an open breaker, and
// callable once it closes». A route whose provider is switched off in the panel (the registry drops
// it from both lists, breaker or not), and a held provider whose transport has no key here, are
// configuration: kind_not_available, terminal, before any attempt row — as before A5.
//
// MUTATIONS (measured red→green): paused() built from the configured route head (RouteHeadAt) instead
// of BreakerHeld — «everything that has a row is paused» → the switched-off route re-queues as
// provider_paused; paused() without the transport's Enabled check → the keyless transport re-queues.
func TestAKeylessRouteSTILL_CLOSES_AS_NOT_CONFIGURED(t *testing.T) {
	t.Run("the provider is switched off in the panel", func(t *testing.T) {
		rg := newImageRouteRig(t, row(1, entity.AIProviderOpenAI, ""))
		openImageBreaker(t, rg, entity.AIProviderOpenAI) // held first, then switched off: no longer a pause
		rg.store.switchProvider(entity.AIProviderOpenAI, false)
		rg.reload(t)
		oa := &fakeImageTransport{model: "gpt-image-2"}
		st := &fakeStore{}
		w := routedWorker(st, rg.reg, map[string]ImageTransport{entity.AIProviderOpenAI: oa})

		require.Error(t, w.PreflightKind(entity.DesignRunKindFlat), "the door refuses: nothing to wait for")
		require.NoError(t, w.execute(context.Background(), testRun(91, entity.DesignRunKindFlat), "tok"))
		require.Equal(t, []string{CodeKindNotAvailable + " retry=false"}, failedCodes(st))
		require.Empty(t, st.started)
		require.Zero(t, oa.n())
		require.NotContains(t, st.failed[0].LastError, "paused")
	})

	t.Run("the held provider's transport has no key here", func(t *testing.T) {
		rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""))
		openImageBreaker(t, rg, entity.AIProviderOpenRouter)
		or := &fakeImageTransport{model: orimages.DefaultModel, off: true}
		st := &fakeStore{}
		w := routedWorker(st, rg.reg, map[string]ImageTransport{entity.AIProviderOpenRouter: or})

		require.NoError(t, w.execute(context.Background(), testRun(92, entity.DesignRunKindFlat), "tok"))
		require.Equal(t, []string{CodeKindNotAvailable + " retry=false"}, failedCodes(st))
		require.Empty(t, st.started)
		msg := w.providers.Image.(CredentialNamer).MissingCredential()
		require.Contains(t, msg, "no key for openrouter", "a keyless transport is named for its key, not paused")
		require.NotContains(t, msg, "paused")
	})
}

// TestAPausedCandidateWAITS_ONLY_FOR_A_SLUG_IT_DRAWS — the pause asks the held candidates the same
// Serves question the callable ones answer: a run whose only drawing candidate is held waits; a run no
// candidate would draw even with every breaker closed is refused now, paused or not; a callable
// candidate that draws the slug is simply chosen.
//
// MUTATION (measured red→green): the held half without its Serves check → the slug nobody draws is
// «paused» instead of unknown_image_model.
func TestAPausedCandidateWAITS_ONLY_FOR_A_SLUG_IT_DRAWS(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderOpenAI, ""))
	or := &fakeImageTransport{model: EngineGPTImage2, only: map[string]bool{EngineGPTImage2: true}}
	oa := &fakeImageTransport{model: "gpt-image-2", only: map[string]bool{EngineGemini3Pro: true}}
	openImageBreaker(t, rg, entity.AIProviderOpenAI)
	ch := NewRoutedImageProvider(rg.reg, map[string]ImageTransport{
		entity.AIProviderOpenRouter: or, entity.AIProviderOpenAI: oa}, EngineGPTImage2).(Chooser)

	_, err := ch.Choose(Job{Kind: entity.DesignRunKindFlat, Model: EngineGemini3Pro}, nil)
	require.ErrorIs(t, err, errRoutePaused, "the only candidate that draws it is held: a wait")
	require.Contains(t, err.Error(), "openai")
	require.Equal(t, CodeProviderPaused, classify(err).Code)
	require.True(t, classify(err).Retryable)

	_, err = ch.Choose(Job{Kind: entity.DesignRunKindFlat, Model: "vendor/nobody-draws-this"}, nil)
	require.ErrorIs(t, err, errNoCandidateServes, "no candidate draws it with every breaker closed either")

	got, err := ch.Choose(Job{Kind: entity.DesignRunKindFlat, Model: EngineGPTImage2}, nil)
	require.NoError(t, err)
	require.Equal(t, "openrouter_images", got.Name())
}

// TestTheRoutePauseREQUEUES_FOR_THE_REGISTRY_S_WINDOW — routePauseRequeue repeats the registry's
// breaker window (unexported there). This pins the copy to the registry's behaviour: held one instant
// before routePauseRequeue has passed since the opening, listed (half-open, the probe's turn) at it.
//
// MUTATION (measured red→green): routePauseRequeue = time.Minute → still held at the re-queue moment,
// i.e. the paused run would come back into another pause and spend another round.
func TestTheRoutePauseREQUEUES_FOR_THE_REGISTRY_S_WINDOW(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""))
	openImageBreaker(t, rg, entity.AIProviderOpenRouter)

	rg.clk.advance(routePauseRequeue - time.Nanosecond)
	require.Len(t, rg.reg.BreakerHeld(entity.AIPurposeImageGenerate), 1, "inside the window: held")
	rg.clk.advance(time.Nanosecond)
	require.Empty(t, rg.reg.BreakerHeld(entity.AIPurposeImageGenerate))
	require.Len(t, rg.reg.Candidates(entity.AIPurposeImageGenerate), 1, "at the re-queue moment: listed, the probe's turn")
}
