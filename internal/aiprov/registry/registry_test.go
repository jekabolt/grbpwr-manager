package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ───────────────────────── fakes ─────────────────────────

// fakeStore is dependency.AI over one in-memory AIConfig. Only the two reads the registry makes are
// real; every write refuses, so a registry that started writing would fail loudly here.
type fakeStore struct {
	mu             sync.Mutex
	cfg            entity.AIConfig
	getConfigCalls int
	versionCalls   int
	getConfigErr   error
	versionErr     error
}

var _ dependency.AI = (*fakeStore)(nil)

var errNotUsed = errors.New("fakeStore: not used by the registry")

func (f *fakeStore) GetConfig(context.Context) (*entity.AIConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getConfigCalls++
	if f.getConfigErr != nil {
		return nil, f.getConfigErr
	}
	c := f.cfg
	c.Providers = append([]entity.AIProvider(nil), f.cfg.Providers...)
	c.Routes = nil
	for _, rt := range f.cfg.Routes {
		c.Routes = append(c.Routes, entity.AIRoute{Purpose: rt.Purpose, Candidates: append([]entity.AIRouteCandidate(nil), rt.Candidates...)})
	}
	return &c, nil
}

func (f *fakeStore) ConfigVersion(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versionCalls++
	if f.versionErr != nil {
		return 0, f.versionErr
	}
	return f.cfg.Settings.ConfigVersion, nil
}

// edit changes the config the way a write RPC does: the change and a version bump.
func (f *fakeStore) edit(mut func(*entity.AIConfig)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mut(&f.cfg)
	f.cfg.Settings.ConfigVersion++
}

func (f *fakeStore) calls() (getConfig, version int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getConfigCalls, f.versionCalls
}

func (f *fakeStore) UpdateProvider(context.Context, string, entity.AIProviderPatch, uint64, string) error {
	return errNotUsed
}
func (f *fakeStore) SetProviderKey(context.Context, string, entity.AIKeyKind, []byte, string, string) error {
	return errNotUsed
}
func (f *fakeStore) SetDefaults(context.Context, entity.AIDefaultsPatch, uint64, string) error {
	return errNotUsed
}
func (f *fakeStore) SetRoute(context.Context, string, []entity.AIRouteCandidate, uint64, string) error {
	return errNotUsed
}
func (f *fakeStore) UpsertModel(context.Context, entity.AIModel, string) error { return errNotUsed }
func (f *fakeStore) RecentFaults(context.Context, time.Time) (map[string]string, error) {
	return nil, errNotUsed
}
func (f *fakeStore) BeginCall(context.Context, entity.AICallStart) (int64, error) {
	return 0, errNotUsed
}
func (f *fakeStore) FinishCall(context.Context, int64, entity.AICallEnd) error { return errNotUsed }
func (f *fakeStore) PriceAcceptedCall(context.Context, int, int, int, entity.AICallEnd) error {
	return errNotUsed
}
func (f *fakeStore) SweepDispatching(context.Context, time.Time) (int64, error) {
	return 0, errNotUsed
}
func (f *fakeStore) SpendReport(context.Context, string, string) (*entity.AISpendReport, error) {
	return nil, errNotUsed
}
func (f *fakeStore) UpsertCostDaily(context.Context, []entity.AICostDaily) error { return errNotUsed }

// fakeClock is a settable clock for the breaker window.
type fakeClock struct{ ns atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// ───────────────────────── helpers ─────────────────────────

func masterB64(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func testRing(t *testing.T) *keyring.Ring {
	t.Helper()
	r, err := keyring.New(masterB64(7))
	require.NoError(t, err)
	return r
}

func seal(t *testing.T, ring *keyring.Ring, provider string, kind entity.AIKeyKind, plain string) []byte {
	t.Helper()
	blob, err := ring.Seal(plain, keyring.AAD(provider, string(kind)))
	require.NoError(t, err)
	return blob
}

// seedConfig is the 0373 seed: nine rows (openrouter, fal, meshy, recraft on), every purpose at
// position 1 as today, version 1, both defaults openrouter.
func seedConfig() entity.AIConfig {
	on := map[string]bool{
		entity.AIProviderOpenRouter: true, entity.AIProviderFal: true,
		entity.AIProviderMeshy: true, entity.AIProviderRecraft: true,
	}
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		cfg.Providers = append(cfg.Providers, entity.AIProvider{Key: k, Label: k, Enabled: on[k]})
	}
	for _, p := range entity.AIPurposes() {
		pk := entity.AIProviderOpenRouter
		switch p {
		case entity.AIPurposeImageCutout, entity.AIPurposeImageExtend, entity.AIPurposeImageInpaint, entity.AIPurposeThreed:
			pk = entity.AIProviderFal
		case entity.AIPurposeVector:
			pk = entity.AIProviderRecraft
		}
		cfg.Routes = append(cfg.Routes, entity.AIRoute{Purpose: p, Candidates: []entity.AIRouteCandidate{{Position: 1, ProviderKey: pk}}})
	}
	cfg.Settings = entity.AISettings{ConfigVersion: 1, DefaultChatProviderKey: entity.AIProviderOpenRouter, DefaultImageProviderKey: entity.AIProviderOpenRouter}
	cfg.BudgetTimezone = "Europe/Warsaw"
	return cfg
}

func provider(cfg *entity.AIConfig, key string) *entity.AIProvider {
	for i := range cfg.Providers {
		if cfg.Providers[i].Key == key {
			return &cfg.Providers[i]
		}
	}
	panic("no provider " + key)
}

func setRoute(cfg *entity.AIConfig, purpose string, cands ...entity.AIRouteCandidate) {
	for i := range cfg.Routes {
		if cfg.Routes[i].Purpose == purpose {
			cfg.Routes[i].Candidates = cands
			return
		}
	}
	cfg.Routes = append(cfg.Routes, entity.AIRoute{Purpose: purpose, Candidates: cands})
}

var testEnv = EnvKeys{
	OpenRouter:       "env-openrouter-aaaa",
	OpenRouterImages: "env-images-bbbb",
	Fal:              "env-fal-cccc",
	Meshy:            "env-meshy-dddd",
	Recraft:          "env-recraft-eeee",
}

// newLoaded builds a registry over cfg, captures its log, and reloads once.
func newLoaded(t *testing.T, ring *keyring.Ring, cfg entity.AIConfig, opts ...Option) (*Registry, *fakeStore, *bytes.Buffer) {
	t.Helper()
	fs := &fakeStore{cfg: cfg}
	r := New(fs, ring, testEnv, opts...)
	var buf bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&lockedWriter{w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	require.NoError(t, r.Reload(context.Background()))
	return r, fs, &buf
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func state(t *testing.T, r *Registry, key string) ProviderState {
	t.Helper()
	for _, s := range r.Providers() {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no provider state for %s", key)
	return ProviderState{}
}

// call is one caller's whole contract: Admit, then end with THAT admission — RecordSuccess when err
// is nil, else RecordFailure. The caller must be admitted.
func call(t *testing.T, r *Registry, providerKey, capability string, err error) {
	t.Helper()
	a, ok := r.Admit(providerKey, capability)
	require.True(t, ok, "%s/%s refused the call", providerKey, capability)
	if err == nil {
		r.RecordSuccess(providerKey, capability, a)
		return
	}
	r.RecordFailure(providerKey, capability, a, err)
}

// admitOK is Admit's verdict alone: admitOK(r.Admit(p, c)).
func admitOK(_ Admission, ok bool) bool { return ok }

// ───────────────────────── keys ─────────────────────────

// TestKeyFunc_DBKeyBeatsEnv — a stored key answers even though the env variable is set; the panel
// says "db" and shows the stored key's last four.
//
// MUTATION: effectiveKeyIn returns envKey before checking dbKey → red.
func TestKeyFunc_DBKeyBeatsEnv(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "db-fal-9z9z")

	r, _, logs := newLoaded(t, ring, cfg)

	require.Equal(t, "db-fal-9z9z", r.KeyFunc(entity.AIProviderFal)())
	st := state(t, r, entity.AIProviderFal)
	require.Equal(t, KeySourceDB, st.KeySource)
	require.Equal(t, "9z9z", st.KeyLast4)
	// A provider with no stored key keeps its env key.
	require.Equal(t, testEnv.Meshy, r.KeyFunc(entity.AIProviderMeshy)())
	require.Equal(t, KeySourceEnv, state(t, r, entity.AIProviderMeshy).KeySource)
	// Never the value in a log line — source and last4 only.
	require.NotContains(t, logs.String(), "db-fal-9z9z")
	require.NotContains(t, logs.String(), testEnv.Meshy)
	require.Contains(t, logs.String(), "ai provider fal: enabled=true key=db")
}

// TestKeyFunc_DisabledYieldsEmpty — enabled=0 WINS over a stored key and over env (02-PLAN S-2).
//
// MUTATION: drop the `!p.enabled` return in effectiveKeyIn → red (the db key comes back).
func TestKeyFunc_DisabledYieldsEmpty(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	fal := provider(&cfg, entity.AIProviderFal)
	fal.Enabled = false
	fal.APIKeyEnc = seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "db-fal-9z9z")
	provider(&cfg, entity.AIProviderMeshy).Enabled = false // env only

	r, _, _ := newLoaded(t, ring, cfg)

	require.Equal(t, "", r.KeyFunc(entity.AIProviderFal)())
	require.Equal(t, "", r.KeyFunc(entity.AIProviderMeshy)(), "enabled=0 must win over the env key too")
	require.False(t, state(t, r, entity.AIProviderFal).Enabled)
}

// TestKeyFunc_UnreadableFallsBackToEnv — a blob that does not open (sealed for another row, under
// another master, or with no master at all) is reported "unreadable", the env key answers, and the
// warning is logged ONCE per blob, not on every reload.
//
// MUTATION: return (plain, false) on an Open error in open() → red (KeySource "env"/"none").
// MUTATION: drop the `already` check → red (two warnings).
func TestKeyFunc_UnreadableFallsBackToEnv(t *testing.T) {
	ring := testRing(t)
	other, err := keyring.New(masterB64(9))
	require.NoError(t, err)
	noMaster, err := keyring.New("")
	require.NoError(t, err)

	cfg := seedConfig()
	// The openrouter ciphertext moved onto the fal row: the AAD binds it to openrouter:api.
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = seal(t, ring, entity.AIProviderOpenRouter, entity.AIKeyAPI, "moved-key-1111")
	// Sealed under another master.
	provider(&cfg, entity.AIProviderMeshy).APIKeyEnc = seal(t, other, entity.AIProviderMeshy, entity.AIKeyAPI, "other-master-2222")
	// Unreadable and nothing in env to fall back to.
	provider(&cfg, entity.AIProviderOpenAI).Enabled = true
	provider(&cfg, entity.AIProviderOpenAI).APIKeyEnc = seal(t, other, entity.AIProviderOpenAI, entity.AIKeyAPI, "sk-openai-3333")

	r, fs, logs := newLoaded(t, ring, cfg)

	require.Equal(t, testEnv.Fal, r.KeyFunc(entity.AIProviderFal)())
	require.Equal(t, KeySourceUnreadable, state(t, r, entity.AIProviderFal).KeySource)
	require.Equal(t, "cccc", state(t, r, entity.AIProviderFal).KeyLast4, "last4 of the key that answers: env")
	require.Equal(t, testEnv.Meshy, r.KeyFunc(entity.AIProviderMeshy)())
	require.Equal(t, KeySourceUnreadable, state(t, r, entity.AIProviderMeshy).KeySource)
	require.Equal(t, "", r.KeyFunc(entity.AIProviderOpenAI)())
	require.Equal(t, KeySourceUnreadable, state(t, r, entity.AIProviderOpenAI).KeySource)

	// Reload again (a version bump that touches nothing here): no second warning for the same blobs.
	fs.edit(func(c *entity.AIConfig) {})
	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, 1, strings.Count(logs.String(), "provider=fal kind=api"), logs.String())
	require.Equal(t, 1, strings.Count(logs.String(), "provider=meshy kind=api"), logs.String())
	for _, secret := range []string{"moved-key-1111", "other-master-2222", "sk-openai-3333"} {
		require.NotContains(t, logs.String(), secret)
	}

	// A registry with no master key cannot open anything: stored keys are unreadable, env answers.
	cfg2 := seedConfig()
	provider(&cfg2, entity.AIProviderFal).APIKeyEnc = seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "db-fal-9z9z")
	r2, _, _ := newLoaded(t, noMaster, cfg2)
	require.Equal(t, testEnv.Fal, r2.KeyFunc(entity.AIProviderFal)())
	require.Equal(t, KeySourceUnreadable, state(t, r2, entity.AIProviderFal).KeySource)
}

// TestKeyFunc_BeforeReloadAndMissingRow — never loaded, or a provider with no row: today's env key.
//
// MUTATION: effectiveKeyIn returns "" for a nil snapshot → red.
func TestKeyFunc_BeforeReloadAndMissingRow(t *testing.T) {
	r := New(&fakeStore{cfg: seedConfig()}, testRing(t), testEnv)
	require.Equal(t, testEnv.Fal, r.KeyFunc(entity.AIProviderFal)())
	require.Equal(t, uint64(0), r.Version())
	require.Nil(t, r.Candidates(entity.AIPurposeThreed))

	cfg := seedConfig()
	var kept []entity.AIProvider
	for _, p := range cfg.Providers {
		if p.Key != entity.AIProviderMeshy {
			kept = append(kept, p)
		}
	}
	cfg.Providers = kept
	loaded, _, _ := newLoaded(t, testRing(t), cfg)
	require.Equal(t, testEnv.Meshy, loaded.KeyFunc(entity.AIProviderMeshy)())
	require.Equal(t, uint64(1), loaded.Version())
}

// TestOpenRouterImagesKeyFunc — one row serves both OpenRouter clients: a stored key answers for
// both; with none, each keeps its own env variable; enabled=0 silences both.
//
// MUTATION: OpenRouterImagesKeyFunc passes capability "" → red (the chat env key answers images).
func TestOpenRouterImagesKeyFunc(t *testing.T) {
	ring := testRing(t)
	r, fs, _ := newLoaded(t, ring, seedConfig())
	chat, images := r.KeyFunc(entity.AIProviderOpenRouter), r.OpenRouterImagesKeyFunc()
	require.Equal(t, testEnv.OpenRouter, chat())
	require.Equal(t, testEnv.OpenRouterImages, images())

	blob := seal(t, ring, entity.AIProviderOpenRouter, entity.AIKeyAPI, "db-or-7777")
	fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderOpenRouter).APIKeyEnc = blob })
	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, "db-or-7777", chat())
	require.Equal(t, "db-or-7777", images())

	fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderOpenRouter).Enabled = false })
	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, "", chat())
	require.Equal(t, "", images())
}

// TestAdminKey — the reconciliation key opens with its own AAD and is not gated by enabled.
//
// MUTATION: open the admin blob with AAD kind "api" → red.
func TestAdminKey(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	oa := provider(&cfg, entity.AIProviderOpenAI) // seeded disabled
	oa.AdminKeyEnc = seal(t, ring, entity.AIProviderOpenAI, entity.AIKeyAdmin, "sk-admin-4444")
	r, _, _ := newLoaded(t, ring, cfg)
	require.Equal(t, "sk-admin-4444", r.AdminKey(entity.AIProviderOpenAI))
	require.True(t, state(t, r, entity.AIProviderOpenAI).AdminKeySet)
	require.Equal(t, "", r.AdminKey(entity.AIProviderFal))
	require.False(t, state(t, r, entity.AIProviderFal).AdminKeySet)
}

// ───────────────────────── candidates ─────────────────────────

// TestCandidates_OrderSkipsAndDefaultProvider — route order; "" = the default provider; disabled,
// keyless, capability-less and duplicate rows skipped; the model passes through, "" included.
//
// MUTATION: return candidates without the enabled/keyless check → red (anthropic and google appear).
// MUTATION: defaultProvider ignores Settings.DefaultChatProviderKey → red (openrouter first, not apibost).
func TestCandidates_OrderSkipsAndDefaultProvider(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	ap := provider(&cfg, entity.AIProviderApibost)
	ap.Enabled = true
	ap.APIKeyEnc = seal(t, ring, entity.AIProviderApibost, entity.AIKeyAPI, "sk-apibost-5555")
	provider(&cfg, entity.AIProviderAnthropic).APIKeyEnc = seal(t, ring, entity.AIProviderAnthropic, entity.AIKeyAPI, "sk-ant-6666") // disabled
	provider(&cfg, entity.AIProviderGoogle).Enabled = true                                                                           // keyless
	cfg.Settings.DefaultChatProviderKey = entity.AIProviderApibost
	setRoute(&cfg, entity.AIPurposeTechCardAnalysis,
		entity.AIRouteCandidate{Position: 4, ProviderKey: entity.AIProviderAnthropic, Model: "claude-sonnet-5"},
		entity.AIRouteCandidate{Position: 1, ProviderKey: "", Model: "claude-fable-5-1"},
		entity.AIRouteCandidate{Position: 2, ProviderKey: entity.AIProviderOpenRouter, Model: ""},
		entity.AIRouteCandidate{Position: 3, ProviderKey: entity.AIProviderFal, Model: "x"}, // fal serves no chat
		entity.AIRouteCandidate{Position: 5, ProviderKey: entity.AIProviderGoogle, Model: "gemini-2.5-pro"},
		entity.AIRouteCandidate{Position: 6, ProviderKey: entity.AIProviderApibost, Model: "claude-fable-5-1"}, // repeat of position 1
	)

	r, _, _ := newLoaded(t, ring, cfg)

	require.Equal(t, []Candidate{
		{ProviderKey: entity.AIProviderApibost, Model: "claude-fable-5-1", Position: 1},
		{ProviderKey: entity.AIProviderOpenRouter, Model: "", Position: 2},
	}, r.Candidates(entity.AIPurposeTechCardAnalysis))

	// The seeded routes answer exactly today's wiring.
	require.Equal(t, []Candidate{{ProviderKey: entity.AIProviderFal, Position: 1}}, r.Candidates(entity.AIPurposeThreed))
	require.Equal(t, []Candidate{{ProviderKey: entity.AIProviderRecraft, Position: 1}}, r.Candidates(entity.AIPurposeVector))
	require.Equal(t, []Candidate{{ProviderKey: entity.AIProviderOpenRouter, Position: 1}}, r.Candidates(entity.AIPurposeImageGenerate))
	require.Nil(t, r.Candidates("no.such.purpose"))
}

// TestCandidates_DefaultFallsBackToOpenRouter — an empty default (never saved) is openrouter.
func TestCandidates_DefaultFallsBackToOpenRouter(t *testing.T) {
	cfg := seedConfig()
	cfg.Settings.DefaultImageProviderKey = ""
	setRoute(&cfg, entity.AIPurposeImageGenerate, entity.AIRouteCandidate{Position: 1, Model: "openai/gpt-image-2"})
	r, _, _ := newLoaded(t, testRing(t), cfg)
	require.Equal(t, []Candidate{{ProviderKey: entity.AIProviderOpenRouter, Model: "openai/gpt-image-2", Position: 1}},
		r.Candidates(entity.AIPurposeImageGenerate))
	require.Equal(t, "openai/gpt-image-2", r.DefaultImageSlug())
}

// TestCandidates_OpenBreakerSkippedUntilItsWindowPasses — an open (provider, capability) breaker
// takes the provider out of THAT capability only; five minutes later it is a candidate again (the
// probe), and a success closes it.
//
// MUTATION: probeBreaker.State ignores the open window (open stays open) → red at the window step
// (the provider never comes back).
// MUTATION: RecordSuccess returns without calling Success → red at the last step (still half-open).
func TestCandidates_OpenBreakerSkippedUntilItsWindowPasses(t *testing.T) {
	clk := newFakeClock()
	cfg := seedConfig()
	setRoute(&cfg, entity.AIPurposeTechCardEnhance,
		entity.AIRouteCandidate{Position: 1, ProviderKey: entity.AIProviderOpenRouter},
	)
	r, _, _ := newLoaded(t, testRing(t), cfg, WithClock(clk.now))

	transient := &aiprov.CallError{Provider: entity.AIProviderOpenRouter, HTTPStatus: 503, Retryable: true}
	for range 3 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transient)
	}
	require.Equal(t, BreakerOpen, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	require.Empty(t, r.Candidates(entity.AIPurposeTechCardEnhance))
	require.Equal(t, BreakerOpen, state(t, r, entity.AIProviderOpenRouter).Breaker)
	// The image capability of the same provider is untouched.
	require.Len(t, r.Candidates(entity.AIPurposeImageGenerate), 1)

	clk.advance(breakerConfig.OpenTimeout + time.Second)
	require.Len(t, r.Candidates(entity.AIPurposeTechCardEnhance), 1, "past the window the provider is the probe")
	require.Equal(t, BreakerHalfOpen, state(t, r, entity.AIProviderOpenRouter).Breaker)

	// The probe goes through: a success closes it.
	call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, nil)
	require.Equal(t, BreakerClosed, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	require.Equal(t, BreakerClosed, state(t, r, entity.AIProviderOpenRouter).Breaker)
	require.Len(t, r.Candidates(entity.AIPurposeTechCardEnhance), 1)
}

// ───────────────────────── reload + poller ─────────────────────────

// TestReload_SwapsAtomically — a KeyFunc captured BEFORE a reload answers the NEW key after it; a
// failed reload leaves the previous snapshot in force.
//
// MUTATION: KeyFunc captures r.snap.Load() once outside the closure → red.
func TestReload_SwapsAtomically(t *testing.T) {
	ring := testRing(t)
	r, fs, _ := newLoaded(t, ring, seedConfig())
	falKey := r.KeyFunc(entity.AIProviderFal)
	require.Equal(t, testEnv.Fal, falKey())

	blob := seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "rotated-fal-8888")
	fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderFal).APIKeyEnc = blob })
	require.Equal(t, testEnv.Fal, falKey(), "nothing changes until the reload")
	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, "rotated-fal-8888", falKey())
	require.Equal(t, uint64(2), r.Version())

	fs.mu.Lock()
	fs.getConfigErr = errors.New("db down")
	fs.mu.Unlock()
	require.Error(t, r.Reload(context.Background()))
	require.Equal(t, "rotated-fal-8888", falKey())
	require.Equal(t, uint64(2), r.Version())
}

// TestReload_KeyChangeResetsThatProvidersBreakers — a rotated key is a new chance.
//
// MUTATION: drop the ResetBreakers call in Reload → red.
func TestReload_KeyChangeResetsThatProvidersBreakers(t *testing.T) {
	ring := testRing(t)
	r, fs, _ := newLoaded(t, ring, seedConfig())
	transient := &aiprov.CallError{HTTPStatus: 502, Retryable: true}
	for range 3 {
		call(t, r, entity.AIProviderFal, entity.AICapabilityThreed, transient)
		call(t, r, entity.AIProviderMeshy, entity.AICapabilityThreed, transient)
	}
	blob := seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "rotated-fal-8888")
	fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderFal).APIKeyEnc = blob })
	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, BreakerClosed, r.BreakerState(entity.AIProviderFal, entity.AICapabilityThreed))
	require.Equal(t, BreakerOpen, r.BreakerState(entity.AIProviderMeshy, entity.AICapabilityThreed),
		"another provider's breaker is not touched")

	r.ResetBreakers(entity.AIProviderMeshy)
	require.Equal(t, BreakerClosed, r.BreakerState(entity.AIProviderMeshy, entity.AICapabilityThreed))
}

// TestPoller_ReloadsOnVersionChangeOnly — a poll with an unchanged version reads the version and
// nothing else; a moved version reloads once.
//
// MUTATION: pollOnce reloads unconditionally → red (getConfig climbs on an unchanged version).
func TestPoller_ReloadsOnVersionChangeOnly(t *testing.T) {
	ring := testRing(t)
	r, fs, _ := newLoaded(t, ring, seedConfig())
	g0, _ := fs.calls()
	require.False(t, r.LastSuccess().IsZero(), "the boot reload makes the worker fresh before its first poll")

	for range 3 {
		require.True(t, r.pollOnce(context.Background()))
	}
	g, v := fs.calls()
	require.Equal(t, g0, g, "no reload while the version stands")
	require.Equal(t, 3, v)
	require.False(t, r.LastSuccess().IsZero())

	blob := seal(t, ring, entity.AIProviderMeshy, entity.AIKeyAPI, "rotated-meshy-9999")
	fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderMeshy).APIKeyEnc = blob })
	require.True(t, r.pollOnce(context.Background()))
	g, _ = fs.calls()
	require.Equal(t, g0+1, g)
	require.Equal(t, "rotated-meshy-9999", r.KeyFunc(entity.AIProviderMeshy)())

	fs.mu.Lock()
	fs.versionErr = errors.New("db down")
	fs.mu.Unlock()
	require.False(t, r.pollOnce(context.Background()))
}

// TestPoller_Loop — Start / Stop drive the same poll on a ticker: a version bump reaches a KeyFunc
// without anybody calling Reload, and ticks with an unchanged version never reload.
func TestPoller_Loop(t *testing.T) {
	ring := testRing(t)
	r, fs, _ := newLoaded(t, ring, seedConfig(), WithPollInterval(2*time.Millisecond))
	require.Equal(t, Name, r.Name())
	require.NoError(t, r.Start(context.Background()))
	require.Error(t, r.Start(context.Background()), "a second Start is refused")

	require.Eventually(t, func() bool { _, v := fs.calls(); return v >= 3 }, 2*time.Second, time.Millisecond)
	g, _ := fs.calls()
	require.Equal(t, 1, g, "only the boot reload so far")

	blob := seal(t, ring, entity.AIProviderRecraft, entity.AIKeyAPI, "rotated-recraft-1234")
	fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderRecraft).APIKeyEnc = blob })
	fn := r.KeyFunc(entity.AIProviderRecraft)
	require.Eventually(t, func() bool { return fn() == "rotated-recraft-1234" }, 2*time.Second, time.Millisecond)

	require.NoError(t, r.Stop())
	require.Error(t, r.Stop())
}

func TestBackoffDelay(t *testing.T) {
	base := time.Minute
	require.Equal(t, time.Minute, backoffDelay(base, 1))
	require.Equal(t, 2*time.Minute, backoffDelay(base, 2))
	require.Equal(t, 8*time.Minute, backoffDelay(base, 4))
	require.Equal(t, 10*time.Minute, backoffDelay(base, 5))
	require.Equal(t, 10*time.Minute, backoffDelay(base, 50))
}

// ───────────────────────── breaker feed ─────────────────────────

// TestRecordFailure_IgnoresConfigAndEngagedErrors — THE BREAKER HEARS ONLY TRANSIENT, FREE FAULTS.
// Three configuration refusals (a dead slug, a rejected key), three engaged failures, three errors
// with no verdict: the breaker stays closed. Three retryable, not-engaged faults: it opens.
//
// MUTATION: RecordFailure feeds every CallError (drop `!ce.Retryable`) → red at the first
// assertion: 3 config errors open the breaker.
// MUTATION: drop `aiprov.Engaged(err)` → red at the second.
func TestRecordFailure_IgnoresConfigAndEngagedErrors(t *testing.T) {
	r, _, _ := newLoaded(t, testRing(t), seedConfig())
	orChat := func() string { return r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat) }

	for _, status := range []int{404, 401, 422} {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat,
			&aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: "model_unknown", HTTPStatus: status})
	}
	require.Equal(t, BreakerClosed, orChat(), "configuration errors must not open the breaker")
	require.Nil(t, r.lookupBreaker(entity.AIProviderOpenRouter, entity.AICapabilityChat), "nor even count")

	engaged := &aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: "timeout", Engaged: true, Retryable: true}
	for range 3 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, engaged)
		// Engaged deeper in the chain counts too.
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat,
			&aiprov.CallError{Retryable: true, Err: engaged})
	}
	require.Equal(t, BreakerClosed, orChat(), "an engaged failure must not open the breaker")

	for range 3 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, errors.New("no verdict"))
	}
	a, ok := r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)
	require.True(t, ok)
	r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, a, nil)
	require.Equal(t, BreakerClosed, orChat())

	for range 3 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat,
			&aiprov.CallError{Provider: entity.AIProviderOpenRouter, HTTPStatus: 503, Retryable: true})
	}
	require.Equal(t, BreakerOpen, orChat())
}

// TestRecordSuccess_ClearsTheCount — two faults, a success, two faults: still closed.
//
// MUTATION: RecordSuccess returns without calling through → red.
func TestRecordSuccess_ClearsTheCount(t *testing.T) {
	r, _, _ := newLoaded(t, testRing(t), seedConfig())
	transient := &aiprov.CallError{HTTPStatus: 429, Retryable: true}
	call(t, r, entity.AIProviderFal, entity.AICapabilityCutout, nil) // no breaker yet: none is created
	require.Nil(t, r.lookupBreaker(entity.AIProviderFal, entity.AICapabilityCutout))
	for range 2 {
		call(t, r, entity.AIProviderFal, entity.AICapabilityCutout, transient)
	}
	call(t, r, entity.AIProviderFal, entity.AICapabilityCutout, nil)
	for range 2 {
		call(t, r, entity.AIProviderFal, entity.AICapabilityCutout, transient)
	}
	require.Equal(t, BreakerClosed, r.BreakerState(entity.AIProviderFal, entity.AICapabilityCutout))
}

// TestProviders_PanelOrderAndNoKeys — nine rows in the panel's order, and no field ever holds a key.
func TestProviders_PanelOrderAndNoKeys(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "db-fal-9z9z")
	r, _, _ := newLoaded(t, ring, cfg)
	states := r.Providers()
	require.Len(t, states, len(entity.AIProviderKeys()))
	for i, k := range entity.AIProviderKeys() {
		st := states[i]
		require.Equal(t, k, st.Key)
		require.Equal(t, BreakerClosed, st.Breaker)
		for _, v := range []string{st.KeySource, st.KeyLast4} {
			require.NotContains(t, []string{"db-fal-9z9z", testEnv.Fal, testEnv.OpenRouter, testEnv.Meshy, testEnv.Recraft}, v)
		}
	}
	require.Equal(t, KeySourceNone, state(t, r, entity.AIProviderRunblob).KeySource)
	require.Equal(t, "Europe/Warsaw", r.BudgetTimezone())
}

// ───────────────────────── one clock, one probe (Codex A1 #1) ─────────────────────────

var (
	transientFault = &aiprov.CallError{Provider: entity.AIProviderOpenRouter, HTTPStatus: 503, Retryable: true}
	keyRejected    = &aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: "key_rejected", HTTPStatus: 401}
	engagedFault   = &aiprov.CallError{Provider: entity.AIProviderOpenRouter, Code: "timeout", Engaged: true, Retryable: true}
)

// newOpenBreaker loads a registry on a fake clock and opens openrouter/chat with three transient
// faults.
func newOpenBreaker(t *testing.T) (*Registry, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	r, _, _ := newLoaded(t, testRing(t), seedConfig(), WithClock(clk.now))
	for range breakerConfig.MaxFailures {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transientFault)
	}
	require.Equal(t, BreakerOpen, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	return r, clk
}

// admitAll releases n callers at once on openrouter/chat's Admit and returns the admissions of the
// ones let through.
func admitAll(r *Registry, n int) []Admission {
	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		yes   []Admission
	)
	start.Add(1)
	for range n {
		done.Go(func() {
			start.Wait()
			if a, ok := r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat); ok {
				mu.Lock()
				yes = append(yes, a)
				mu.Unlock()
			}
		})
	}
	start.Done()
	done.Wait()
	return yes
}

// admitted is admitAll's head count.
func admitted(r *Registry, n int) int { return len(admitAll(r, n)) }

// TestBreaker_PastTheWindowExactlyOneProbe — open: nobody is admitted; past the window sixteen
// concurrent callers ask and exactly ONE is let through (the probe); the rest keep being refused
// while it is out. A provider that never failed is admitted and gets no breaker.
//
// MUTATION: Admit never sets probeInFlight (drop `b.probeInFlight = true`) → red: 16 admitted.
// MUTATION: Admit judges the window with time.Since instead of the instant passed in (the old two
// clocks) → red whatever the wall clock says: either the wall clock is past the window and the first
// caller is let through while the registry clock says open, or it is not and nobody is ever the probe.
// MUTATION: registry Admit uses breakerFor (creating) instead of lookupBreaker → red at the last line.
func TestBreaker_PastTheWindowExactlyOneProbe(t *testing.T) {
	r, clk := newOpenBreaker(t)
	require.False(t, admitOK(r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)), "open: nobody")
	require.Equal(t, 0, admitted(r, 16))

	clk.advance(breakerConfig.OpenTimeout - time.Second)
	require.Equal(t, 0, admitted(r, 16), "one second before the window ends: still nobody")

	clk.advance(2 * time.Second)
	require.Equal(t, BreakerHalfOpen, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	require.Len(t, r.Candidates(entity.AIPurposeTechCardEnhance), 1, "half-open is still LISTED")
	require.Equal(t, 1, admitted(r, 16), "past the window exactly one caller is the probe")
	require.Equal(t, 0, admitted(r, 16), "while the probe is out, nobody else")

	a, ok := r.Admit(entity.AIProviderFal, entity.AICapabilityThreed)
	require.True(t, ok, "no breaker: admitted")
	require.Equal(t, Admission{}, a, "with the zero Admission")
	require.Nil(t, r.lookupBreaker(entity.AIProviderFal, entity.AICapabilityThreed), "and none created")
}

// TestBreaker_ProbeSuccessClosesAndReservesNothing — the probe's RecordSuccess closes the breaker:
// listed, closed, every caller admitted; and the NEXT outage gets a probe of its own — a reservation
// left over from the last one would lock the provider out for good.
//
// MUTATION: Success does not set state closed → red (still half-open, the second caller refused).
// MUTATION: Success does not clear probeInFlight → red at the second outage (nobody is ever the
// probe again).
func TestBreaker_ProbeSuccessClosesAndReservesNothing(t *testing.T) {
	r, clk := newOpenBreaker(t)
	clk.advance(breakerConfig.OpenTimeout)
	p, ok := r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)
	require.True(t, ok)

	r.RecordSuccess(entity.AIProviderOpenRouter, entity.AICapabilityChat, p)
	require.Equal(t, BreakerClosed, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	require.Len(t, r.Candidates(entity.AIPurposeTechCardEnhance), 1)
	require.Equal(t, 2, admitted(r, 2), "closed: both callers")

	for range breakerConfig.MaxFailures {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transientFault)
	}
	require.Equal(t, 0, admitted(r, 4))
	clk.advance(breakerConfig.OpenTimeout)
	require.Equal(t, 1, admitted(r, 16), "the next outage has its own probe")
}

// TestBreaker_ProbeFaultReopensForAFullWindow — a transient fault on the probe re-opens the breaker
// for a FULL window counted from the probe's fault, not from the first opening; after it, again one
// probe.
//
// MUTATION: Fault on half-open does not refresh openedAt → red: right after the probe failed the
// breaker already reads half-open again (the old window had long passed).
// MUTATION: Fault on half-open does not clear probeInFlight → red at the end (nobody is ever the
// probe again).
func TestBreaker_ProbeFaultReopensForAFullWindow(t *testing.T) {
	r, clk := newOpenBreaker(t)
	clk.advance(breakerConfig.OpenTimeout + time.Minute)
	p, ok := r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)
	require.True(t, ok)

	r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, p, transientFault)
	require.Equal(t, BreakerOpen, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	require.Empty(t, r.Candidates(entity.AIPurposeTechCardEnhance))

	clk.advance(time.Second)
	require.False(t, admitOK(r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)), "a fresh window, not the old one")
	clk.advance(breakerConfig.OpenTimeout - 2*time.Second)
	require.False(t, admitOK(r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)), "one second before the new window ends")

	clk.advance(time.Second)
	require.Equal(t, 1, admitted(r, 16))
}

// TestBreaker_VerdictlessEndsReleaseTheProbe — a probe that ends in a configuration refusal (401),
// in an error that is not a CallError, or with no call at all (Release) says nothing about the
// provider: the breaker stays half-open, and the probe is FREED for the next caller — exactly one.
//
// MUTATION: RecordFailure returns early for a non-counting error instead of calling Release → red:
// the provider stays reserved by a probe nobody runs, the next Admit is false.
// MUTATION: probeBreaker.Release does nothing → red.
func TestBreaker_VerdictlessEndsReleaseTheProbe(t *testing.T) {
	r, clk := newOpenBreaker(t)
	clk.advance(breakerConfig.OpenTimeout)

	for _, end := range []struct {
		name string
		do   func(Admission)
	}{
		{"401", func(a Admission) {
			r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, a, keyRejected)
		}},
		{"no verdict", func(a Admission) {
			r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, a, errors.New("dial: no route"))
		}},
		{"no call", func(a Admission) { r.Release(entity.AIProviderOpenRouter, entity.AICapabilityChat, a) }},
	} {
		probes := admitAll(r, 16)
		require.Len(t, probes, 1, "before %s", end.name)
		end.do(probes[0])
		require.Equal(t, BreakerHalfOpen, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat),
			"%s neither closes nor re-opens it", end.name)
	}
	require.Equal(t, 1, admitted(r, 16), "freed each time, one probe each time")
}

// TestBreaker_EngagedEndReleasesAndNeverCounts — an engaged error on the probe may have cost money:
// it is the ledger's business, never a breaker fault — it releases the probe like any verdict-less
// end, and the breaker stays half-open (not re-opened). While closed it never opens it either.
//
// MUTATION: drop `aiprov.Engaged(err)` in RecordFailure → red: the engaged probe re-opens it.
func TestBreaker_EngagedEndReleasesAndNeverCounts(t *testing.T) {
	r, clk := newOpenBreaker(t)
	clk.advance(breakerConfig.OpenTimeout)
	p, ok := r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)
	require.True(t, ok)

	r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, p, engagedFault)
	require.Equal(t, BreakerHalfOpen, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	next := admitAll(r, 16)
	require.Len(t, next, 1, "released, not re-opened")

	r.RecordSuccess(entity.AIProviderOpenRouter, entity.AICapabilityChat, next[0])
	for range 2 * breakerConfig.MaxFailures {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, engagedFault)
	}
	require.Equal(t, BreakerClosed, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
}

// TestBreaker_ResetClosesAndUnreserves — ResetBreakers with the probe out: closed, everybody admitted,
// and the next outage has its own probe (the old reservation is gone).
//
// MUTATION: probeBreaker.Reset does not clear probeInFlight → red at the last line.
// MUTATION: probeBreaker.Reset does not set state closed → red (still half-open, one admitted).
func TestBreaker_ResetClosesAndUnreserves(t *testing.T) {
	r, clk := newOpenBreaker(t)
	clk.advance(breakerConfig.OpenTimeout)
	require.True(t, admitOK(r.Admit(entity.AIProviderOpenRouter, entity.AICapabilityChat)))

	r.ResetBreakers(entity.AIProviderOpenRouter)
	require.Equal(t, BreakerClosed, r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat))
	require.Equal(t, 3, admitted(r, 3))

	for range breakerConfig.MaxFailures {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transientFault)
	}
	clk.advance(breakerConfig.OpenTimeout)
	require.Equal(t, 1, admitted(r, 16))
}

// ───────────────────────── an end acts only on its own admission (Codex second review, P1) ─────────────────────────

// orChatState is openrouter/chat's breaker state — the breaker every test below works on.
func orChatState(r *Registry) string {
	return r.BreakerState(entity.AIProviderOpenRouter, entity.AICapabilityChat)
}

// TestBreaker_StragglersCannotTouchTheProbe — six calls are admitted while the breaker is CLOSED and
// are still out when three other calls open it and its whole window passes; the probe P is admitted.
// Three stragglers end now — a Release, a Success, a transient fault — and each is ignored: still
// half-open, still exactly one probe out, nobody else admitted. P's success closes the breaker; the
// other three stragglers then fail transiently, and that does not count either — it is news from
// before the opening, not three faults of the closed breaker.
//
// MUTATION: probeBreaker.current always true (drop the gen check) → red at the last assertion: the
// late stragglers' three faults re-open the breaker P has just closed.
// MUTATION: open() does not bump gen AND the half-open `!a.probe` checks are dropped → red at the
// first straggler: its Release frees P's reservation and a second caller is admitted. (Either one
// alone stays green: each retires the closed-time admissions by itself.)
func TestBreaker_StragglersCannotTouchTheProbe(t *testing.T) {
	clk := newFakeClock()
	r, _, _ := newLoaded(t, testRing(t), seedConfig(), WithClock(clk.now))
	early, late := admitAll(r, 3), admitAll(r, 3)
	require.Len(t, early, 3, "closed: everybody")
	require.Len(t, late, 3, "closed: everybody")
	for range breakerConfig.MaxFailures {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transientFault)
	}
	require.Equal(t, BreakerOpen, orChatState(r))
	clk.advance(breakerConfig.OpenTimeout)
	probes := admitAll(r, 16)
	require.Len(t, probes, 1)

	for _, end := range []struct {
		name string
		do   func(Admission)
	}{
		{"Release", func(a Admission) { r.Release(entity.AIProviderOpenRouter, entity.AICapabilityChat, a) }},
		{"Success", func(a Admission) { r.RecordSuccess(entity.AIProviderOpenRouter, entity.AICapabilityChat, a) }},
		{"Fault", func(a Admission) {
			r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, a, transientFault)
		}},
	} {
		end.do(early[0])
		early = early[1:]
		require.Equal(t, BreakerHalfOpen, orChatState(r), "a straggler's %s judged somebody else's probe", end.name)
		require.Equal(t, 0, admitted(r, 16), "a straggler's %s freed somebody else's probe", end.name)
	}

	r.RecordSuccess(entity.AIProviderOpenRouter, entity.AICapabilityChat, probes[0])
	require.Equal(t, BreakerClosed, orChatState(r), "the probe's own success closes it")
	for _, s := range late {
		r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, s, transientFault)
	}
	require.Equal(t, BreakerClosed, orChatState(r), "faults of calls admitted before the opening must not count")
}

// TestBreaker_AProbeAdmissionEndsOnce — a probe's admission is spent by its end, whatever the end:
// a second end of it (a deferred Release behind an explicit RecordFailure, say) must not act on the
// NEXT probe. P1 re-opens on a transient fault; past the new full window P2 is the probe, and P1's
// second end frees nothing. P2 ends without a verdict (Release); P3 is the probe, and P2's second
// end neither frees nor closes. P3 succeeds and closes; two fresh faults; P3's second end, a fault,
// does not make it three.
//
// MUTATION: open() does not bump gen → red at P1's second end (P2's reservation freed).
// MUTATION: probeBreaker.Release does not bump gen → red at P2's second end.
// MUTATION: probeBreaker.Success does not bump gen → red at the end (P3's stale fault opens it).
func TestBreaker_AProbeAdmissionEndsOnce(t *testing.T) {
	r, clk := newOpenBreaker(t)
	clk.advance(breakerConfig.OpenTimeout)
	p1 := admitAll(r, 16)
	require.Len(t, p1, 1)
	r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, p1[0], transientFault)
	require.Equal(t, BreakerOpen, orChatState(r), "the probe's fault re-opens it")
	clk.advance(breakerConfig.OpenTimeout - time.Second)
	require.Equal(t, 0, admitted(r, 16), "for a full window")
	clk.advance(time.Second)

	p2 := admitAll(r, 16)
	require.Len(t, p2, 1)
	r.Release(entity.AIProviderOpenRouter, entity.AICapabilityChat, p1[0])
	require.Equal(t, 0, admitted(r, 16), "P1's second end freed P2's probe")

	r.Release(entity.AIProviderOpenRouter, entity.AICapabilityChat, p2[0])
	p3 := admitAll(r, 16)
	require.Len(t, p3, 1, "P2's release frees the probe once")
	r.Release(entity.AIProviderOpenRouter, entity.AICapabilityChat, p2[0])
	require.Equal(t, 0, admitted(r, 16), "P2's second Release freed P3's probe")
	r.RecordSuccess(entity.AIProviderOpenRouter, entity.AICapabilityChat, p2[0])
	require.Equal(t, BreakerHalfOpen, orChatState(r), "P2's second end closed the breaker under P3")

	r.RecordSuccess(entity.AIProviderOpenRouter, entity.AICapabilityChat, p3[0])
	require.Equal(t, BreakerClosed, orChatState(r))
	for range breakerConfig.MaxFailures - 1 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transientFault)
	}
	r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, p3[0], transientFault)
	require.Equal(t, BreakerClosed, orChatState(r), "P3's second end counted as a third fault")
}

// TestBreaker_ResetRetiresEveryAdmission — the probe P is out on the old key when the key rotates
// (ResetBreakers). The new key's calls fail twice; P then ends in a success, which says nothing about
// the new key and must not wipe those two; the third new fault opens the breaker.
//
// MUTATION: probeBreaker.Reset does not bump gen → red: P's success clears the count, the third fault
// leaves it closed.
// MUTATION: probeBreaker.current always true (drop the gen check) → red, the same way.
func TestBreaker_ResetRetiresEveryAdmission(t *testing.T) {
	r, clk := newOpenBreaker(t)
	clk.advance(breakerConfig.OpenTimeout)
	old := admitAll(r, 16)
	require.Len(t, old, 1)

	r.ResetBreakers(entity.AIProviderOpenRouter)
	for range breakerConfig.MaxFailures - 1 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transientFault)
	}
	r.RecordSuccess(entity.AIProviderOpenRouter, entity.AICapabilityChat, old[0])
	call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transientFault)
	require.Equal(t, BreakerOpen, orChatState(r), "a success on the old key wiped the new key's faults")
}

// TestBreaker_RotationRetiresZeroAdmissions — no breaker exists; three calls are admitted with the
// zero Admission on the OLD key; the key rotates (ResetBreakers); their transient faults arrive
// afterwards and must NOT open the breaker for the NEW key — while the new key's own three faults
// still do (Codex FIX-C review, P2).
//
// MUTATION: ResetBreakers resets only the entries that exist → red: the three old faults create a
// generation-0 breaker and open it.
func TestBreaker_RotationRetiresZeroAdmissions(t *testing.T) {
	r, _, _ := newLoaded(t, testRing(t), seedConfig(), WithClock(newFakeClock().now))
	old := admitAll(r, breakerConfig.MaxFailures)
	require.Len(t, old, breakerConfig.MaxFailures)
	r.ResetBreakers(entity.AIProviderOpenRouter)
	for _, a := range old {
		r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, a, transientFault)
	}
	require.Equal(t, BreakerClosed, orChatState(r), "faults of calls admitted before the rotation opened the new key's breaker")
	fresh := admitAll(r, breakerConfig.MaxFailures)
	require.Len(t, fresh, breakerConfig.MaxFailures)
	for _, a := range fresh {
		r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, a, transientFault)
	}
	require.Equal(t, BreakerOpen, orChatState(r), "the new key's own faults must still open it")
}

// TestBreaker_ZeroAdmissionCountsOnANeverOpenedBreaker — the first outage ever: no breaker exists, so
// three callers are admitted with the zero Admission; their three transient faults must create the
// breaker and open it. The zero Admission is accepted exactly while the breaker has never opened nor
// been reset (gen 0).
//
// MUTATION: probeBreaker.current rejects the zero Admission → red: the first outage never opens.
func TestBreaker_ZeroAdmissionCountsOnANeverOpenedBreaker(t *testing.T) {
	r, _, _ := newLoaded(t, testRing(t), seedConfig(), WithClock(newFakeClock().now))
	first := admitAll(r, breakerConfig.MaxFailures)
	require.Len(t, first, breakerConfig.MaxFailures)
	for _, a := range first {
		require.Equal(t, Admission{}, a, "no breaker yet: the zero Admission")
	}
	require.Nil(t, r.lookupBreaker(entity.AIProviderOpenRouter, entity.AICapabilityChat), "Admit creates none")
	for _, a := range first {
		r.RecordFailure(entity.AIProviderOpenRouter, entity.AICapabilityChat, a, transientFault)
	}
	require.Equal(t, BreakerOpen, orChatState(r), "the first three faults ever open it")
}

// TestProbeBreaker_OnlyTheProbeJudgesHalfOpen — the ownership rule on its own, below the registry: an
// admission of the half-open breaker's CURRENT generation that does not hold the probe neither
// closes, re-opens nor frees it. (Through the registry no such admission can be handed out — the
// opening's gen bump retires every closed-time one — so this is the rule's only direct test.)
//
// MUTATION: drop the `!a.probe` check in Success / Fault / Release → red at that step.
func TestProbeBreaker_OnlyTheProbeJudgesHalfOpen(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	b := &probeBreaker{state: BreakerHalfOpen, gen: 7, probeInFlight: true, openedAt: now.Add(-time.Hour)}
	bystander := Admission{gen: 7}

	b.Success(bystander)
	require.Equal(t, BreakerHalfOpen, b.State(now), "a non-probe success closed it")
	b.Fault(bystander, now)
	require.Equal(t, BreakerHalfOpen, b.State(now), "a non-probe fault re-opened it")
	b.Release(bystander)
	_, ok := b.Admit(now)
	require.False(t, ok, "a non-probe release freed the probe")

	b.Release(Admission{gen: 7, probe: true})
	_, ok = b.Admit(now)
	require.True(t, ok, "the probe's own release frees it")
}
