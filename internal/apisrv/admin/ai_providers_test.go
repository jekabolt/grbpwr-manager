package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/probe"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// The handlers run against the generated dependency.AI mock (what they READ and WRITE, asserted call
// by call) and a REAL registry (what the panel says about keys and breakers), the registry reading its
// configuration from aiprovtest's in-memory store with a config of our choosing. No database, no
// network: the probe's http.Client is a recording RoundTripper.
//
// Every test names the mutation that turns it red.

// ───────────────────────── fixtures ─────────────────────────

// aiCfgStore is aiprovtest.Store whose config half serves a fixed AIConfig — the registry's source.
type aiCfgStore struct {
	*aiprovtest.Store
	mu        sync.Mutex
	cfg       entity.AIConfig
	getConfig int
}

func (s *aiCfgStore) GetConfig(context.Context) (*entity.AIConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getConfig++
	c := s.cfg
	return &c, nil
}

func (s *aiCfgStore) ConfigVersion(context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Settings.ConfigVersion, nil
}

func (s *aiCfgStore) reloads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getConfig
}

// set replaces the configuration the registry reads — another instance's write.
func (s *aiCfgStore) set(cfg entity.AIConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

const (
	aiTestVersion   = uint64(7)
	aiTestUser      = "im"
	aiDBKeyOpenAI   = "sk-openai-stored-key-1a2b"
	aiDBAdminAnthro = "sk-ant-admin-stored-9z9z"
	aiEnvOpenRouter = "sk-or-env-key-9999"
	aiEnvFal        = "fal-env-key-5678"
	aiEnvRecraft    = "rc-env-key-4321"
)

var aiKeyStoredAt = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

func aiTestRing(t *testing.T, b byte) *keyring.Ring {
	t.Helper()
	r, err := keyring.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)))
	require.NoError(t, err)
	return r
}

func aiSeal(t *testing.T, r *keyring.Ring, provider string, kind entity.AIKeyKind, plain string) []byte {
	t.Helper()
	blob, err := r.Seal(plain, keyring.AAD(provider, string(kind)))
	require.NoError(t, err)
	return blob
}

// aiTestConfig is 0373's seed plus: an openai api key stored by im, a fal api key sealed under ANOTHER
// master (unreadable → the env key answers), an anthropic admin key, two custom openrouter models (one
// disabled, one duplicating a catalogue slug), and image.generate with a fallback.
func aiTestConfig(t *testing.T, ring *keyring.Ring) entity.AIConfig {
	t.Helper()
	labels := map[string]string{
		"openai": "OpenAI", "anthropic": "Anthropic", "google": "Google Gemini", "openrouter": "OpenRouter",
		"apibost": "apibost", "fal": "fal", "meshy": "Meshy", "runblob": "runblob", "recraft": "Recraft",
	}
	var providers []entity.AIProvider
	for _, k := range entity.AIProviderKeys() {
		p := entity.AIProvider{Key: k, Label: labels[k]}
		switch k {
		case entity.AIProviderOpenRouter, entity.AIProviderFal, entity.AIProviderMeshy, entity.AIProviderRecraft:
			p.Enabled = true
		}
		switch k {
		case entity.AIProviderOpenAI:
			p.Enabled = true
			p.APIKeyEnc = aiSeal(t, ring, k, entity.AIKeyAPI, aiDBKeyOpenAI)
			p.APIKeyLast4, p.APIKeyUpdatedBy, p.APIKeyUpdatedAt = "1a2b", aiTestUser, &aiKeyStoredAt
		case entity.AIProviderFal:
			p.APIKeyEnc = aiSeal(t, aiTestRing(t, 99), k, entity.AIKeyAPI, "fal-stored-under-another-master")
			p.APIKeyLast4, p.APIKeyUpdatedBy, p.APIKeyUpdatedAt = "ster", "someone", &aiKeyStoredAt
		case entity.AIProviderAnthropic:
			p.AdminKeyEnc = aiSeal(t, ring, k, entity.AIKeyAdmin, aiDBAdminAnthro)
			p.AdminKeyLast4 = "9z9z"
		case entity.AIProviderMeshy:
			// A cleared slot still records who cleared it; the panel must not show it as "set by".
			p.APIKeyUpdatedBy, p.APIKeyUpdatedAt = "someone", &aiKeyStoredAt
		}
		providers = append(providers, p)
	}
	var routes []entity.AIRoute
	for _, purpose := range entity.AIPurposes() {
		provider := entity.AIProviderOpenRouter
		switch entity.AIPurposeCapability(purpose) {
		case entity.AICapabilityCutout, entity.AICapabilityEdit, entity.AICapabilityThreed:
			provider = entity.AIProviderFal
		case entity.AICapabilityVector:
			provider = entity.AIProviderRecraft
		}
		rt := entity.AIRoute{Purpose: purpose, Candidates: []entity.AIRouteCandidate{{Position: 1, ProviderKey: provider}}}
		if purpose == entity.AIPurposeImageGenerate {
			rt.Candidates = []entity.AIRouteCandidate{
				{Position: 1, ProviderKey: "", Model: "openai/gpt-image-2"},
				{Position: 2, ProviderKey: entity.AIProviderFal, Model: "fal-ai/flux"},
			}
		}
		routes = append(routes, rt)
	}
	return entity.AIConfig{
		Providers: providers,
		Models: []entity.AIModel{
			{ProviderKey: "openrouter", Model: "x-ai/grok-9", Label: "", Kind: "chat"},
			{ProviderKey: "openrouter", Model: "x-ai/retired", Kind: "chat", Disabled: true},
			{ProviderKey: "openrouter", Model: "openai/gpt-5-mini", Label: "dup of the catalogue", Kind: "chat"},
		},
		Routes: routes,
		Settings: entity.AISettings{
			ConfigVersion: aiTestVersion, DefaultChatProviderKey: "openrouter", DefaultImageProviderKey: "openrouter",
		},
		BudgetTimezone: "Europe/Warsaw",
	}
}

// aiProbeRT records every probe request and answers with one canned response.
type aiProbeRT struct {
	mu     sync.Mutex
	reqs   []*http.Request
	status int
	body   string
}

func (rt *aiProbeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.reqs = append(rt.reqs, r)
	return &http.Response{
		StatusCode: rt.status, Body: io.NopCloser(strings.NewReader(rt.body)),
		Header: http.Header{"Content-Type": {"application/json"}}, Request: r,
	}, nil
}

func (rt *aiProbeRT) count() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.reqs)
}

// aiHarness is one Server wired for the panel.
type aiHarness struct {
	s     *Server
	ai    *mocks.MockAI
	reg   *registry.Registry
	store *aiCfgStore
	cfg   entity.AIConfig
	ring  *keyring.Ring
	probe *aiProbeRT
}

type aiHarnessOpt struct {
	ring             *keyring.Ring // nil = a ring with a master key
	designGeneration bool
	clock            func() time.Time // the registry's clock; nil = time.Now
	reconcile        func(context.Context, string)
}

func newAIHarness(t *testing.T, o aiHarnessOpt) *aiHarness {
	t.Helper()
	cfgRing := aiTestRing(t, 7)
	ring := o.ring
	if ring == nil {
		ring = cfgRing
	}
	cfg := aiTestConfig(t, cfgRing)
	store := &aiCfgStore{Store: &aiprovtest.Store{}, cfg: cfg}
	reg := registry.New(store, cfgRing, registry.EnvKeys{
		OpenRouter: aiEnvOpenRouter, OpenRouterImages: aiEnvOpenRouter, Fal: aiEnvFal, Recraft: aiEnvRecraft,
	}, registry.WithClock(o.clock))
	require.NoError(t, reg.Reload(context.Background()))

	repo := mocks.NewMockRepository(t)
	ai := mocks.NewMockAI(t)
	repo.EXPECT().AI().Return(ai).Maybe()

	pr := &aiProbeRT{status: http.StatusOK, body: `{"data":[]}`}
	s := &Server{repo: repo}
	s.SetDesignGenerationEnabled(o.designGeneration)
	s.SetAIProviders(AIProvidersWiring{
		Registry: reg, KeyRing: ring, RecraftViaOpenRouter: true,
		ProbeClient: &http.Client{Transport: pr}, Reconcile: o.reconcile,
	})
	return &aiHarness{s: s, ai: ai, reg: reg, store: store, cfg: cfg, ring: ring, probe: pr}
}

// expectConfigRead is the builder's two reads: the config and the badges.
func (h *aiHarness) expectConfigRead(faults map[string]string) {
	c := h.cfg
	h.ai.EXPECT().GetConfig(mock.Anything).Return(&c, nil)
	h.ai.EXPECT().RecentFaults(mock.Anything, mock.MatchedBy(func(since time.Time) bool {
		age := time.Since(since)
		return age > aiFaultWindow-time.Minute && age < aiFaultWindow+time.Minute
	})).Return(faults, nil)
}

func aiCtx() context.Context { return authsrv.PutAdminUsername(context.Background(), aiTestUser) }

func aiProvider(t *testing.T, cfg *pb_admin.GetAiProvidersConfigResponse, key string) *pb_admin.AiProviderInfo {
	t.Helper()
	for _, p := range cfg.GetProviders() {
		if p.GetKey() == key {
			return p
		}
	}
	t.Fatalf("provider %s missing from the config", key)
	return nil
}

func aiPurpose(t *testing.T, cfg *pb_admin.GetAiProvidersConfigResponse, key string) *pb_admin.AiPurposeInfo {
	t.Helper()
	for _, p := range cfg.GetPurposes() {
		if p.GetKey() == key {
			return p
		}
	}
	t.Fatalf("purpose %s missing from the config", key)
	return nil
}

func aiRequireCode(t *testing.T, err error, want codes.Code) *status.Status {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "not a gRPC status: %v", err)
	require.Equal(t, want, st.Code(), "status %v", st)
	return st
}

func aiViolationField(st *status.Status) string {
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok && len(br.GetFieldViolations()) > 0 {
			return br.GetFieldViolations()[0].GetField()
		}
	}
	return ""
}

// ───────────────────────── the config builder ─────────────────────────

// TestAiConfigJoinsStoreRegistryAndBadges.
//
// MUTATIONS IT CATCHES: key source / last4 read from the store row instead of the registry (fal would
// say "db ···ster" while the env key answers); "set by" shown for a cleared slot (meshy); the admin key
// source not derived (anthropic); the breaker or the badge dropped; a disabled custom model listed, or a
// custom row duplicating a catalogue slug listed twice; the fallback of a single-candidate route
// invented; any version / default / flag not passed through.
func TestAiConfigJoinsStoreRegistryAndBadges(t *testing.T) {
	h := newAIHarness(t, aiHarnessOpt{designGeneration: true})
	for i := 0; i < 3; i++ { // three transient faults open meshy's 3D breaker
		h.reg.RecordFailure(entity.AIProviderMeshy, entity.AICapabilityThreed, registry.Admission{},
			&aiprov.CallError{Provider: "meshy", Retryable: true, Err: fmt.Errorf("503")})
	}
	h.expectConfigRead(map[string]string{"fal": "out_of_credits"})

	cfg, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
	require.NoError(t, err)

	var keys []string
	for _, p := range cfg.GetProviders() {
		keys = append(keys, p.GetKey())
	}
	require.Equal(t, entity.AIProviderKeys(), keys, "providers in the panel's fixed order")

	openai := aiProvider(t, cfg, "openai")
	require.Equal(t, "OpenAI", openai.GetLabel())
	require.True(t, openai.GetEnabled())
	require.Equal(t, []string{"chat", "image"}, openai.GetCapabilities())
	require.Equal(t, registry.KeySourceDB, openai.GetKeySource())
	require.Equal(t, "1a2b", openai.GetKeyLast4())
	require.Equal(t, aiTestUser, openai.GetKeyUpdatedBy())
	require.True(t, openai.GetKeyUpdatedAt().AsTime().Equal(aiKeyStoredAt))
	require.True(t, openai.GetAdminKeySupported())
	require.Equal(t, registry.KeySourceNone, openai.GetAdminKeySource())
	require.Equal(t, registry.BreakerClosed, openai.GetBreaker())
	require.Empty(t, openai.GetFaultCode())

	fal := aiProvider(t, cfg, "fal")
	require.Equal(t, registry.KeySourceUnreadable, fal.GetKeySource(), "sealed under another master")
	require.Equal(t, "5678", fal.GetKeyLast4(), "the last four of the key that ANSWERS: env")
	require.Equal(t, "someone", fal.GetKeyUpdatedBy(), "a key is stored, if unreadable")
	require.Equal(t, "out_of_credits", fal.GetFaultCode())

	meshy := aiProvider(t, cfg, "meshy")
	require.Equal(t, registry.BreakerOpen, meshy.GetBreaker())
	require.Equal(t, registry.KeySourceNone, meshy.GetKeySource())
	require.Empty(t, meshy.GetKeyUpdatedBy(), "a cleared slot is not «set by»")
	require.Nil(t, meshy.GetKeyUpdatedAt())
	require.False(t, meshy.GetAdminKeySupported())

	anthropic := aiProvider(t, cfg, "anthropic")
	require.False(t, anthropic.GetEnabled())
	require.Equal(t, registry.KeySourceDB, anthropic.GetAdminKeySource())
	require.Equal(t, "9z9z", anthropic.GetAdminKeyLast4())

	require.Equal(t, registry.KeySourceEnv, aiProvider(t, cfg, "recraft").GetKeySource())
	require.Equal(t, "via openrouter", aiProvider(t, cfg, "recraft").GetNote())
	require.Empty(t, aiProvider(t, cfg, "openrouter").GetNote())

	// Models: the catalogue (priced where a rate is on file), then custom rows; no disabled row, no dup.
	var slugs []string
	for _, m := range aiProvider(t, cfg, "openrouter").GetModels() {
		slugs = append(slugs, m.GetSlug())
		switch m.GetSlug() {
		case "x-ai/grok-9":
			require.True(t, m.GetCustom())
			require.False(t, m.GetPriced())
			require.Equal(t, "x-ai/grok-9", m.GetLabel(), "a custom row with no label shows its slug")
			require.Equal(t, "chat", m.GetKind())
		case "google/gemini-3.1-flash-lite":
			require.False(t, m.GetPriced(), "an unpriced catalogue row")
		case "openai/gpt-5-mini":
			require.False(t, m.GetCustom())
			require.True(t, m.GetPriced())
		}
	}
	var want []string
	for _, m := range pricing.Catalogue("openrouter") {
		want = append(want, m.Slug)
	}
	want = append(want, "x-ai/grok-9")
	require.Equal(t, want, slugs)
	require.Empty(t, aiProvider(t, cfg, "meshy").GetModels(), "no catalogue, no custom rows")

	// Purposes: every one, in order, with its words and its route.
	var purposes []string
	for _, p := range cfg.GetPurposes() {
		purposes = append(purposes, p.GetKey())
	}
	require.Equal(t, entity.AIPurposes(), purposes)
	gen := aiPurpose(t, cfg, entity.AIPurposeImageGenerate)
	require.Equal(t, "design images", gen.GetLabel())
	require.Equal(t, "images", gen.GetGroup())
	require.Equal(t, "image", gen.GetCapability())
	require.Equal(t, "", gen.GetPrimary().GetProviderKey())
	require.Equal(t, "openai/gpt-image-2", gen.GetPrimary().GetModel())
	require.Equal(t, "fal", gen.GetFallback().GetProviderKey())
	vector := aiPurpose(t, cfg, entity.AIPurposeVector)
	require.Equal(t, "recraft", vector.GetPrimary().GetProviderKey())
	require.Nil(t, vector.GetFallback(), "absent = no fallback")

	require.Equal(t, "openrouter", cfg.GetDefaultChatProviderKey())
	require.Equal(t, "openrouter", cfg.GetDefaultImageProviderKey())
	require.Equal(t, aiTestVersion, cfg.GetConfigVersion())
	require.True(t, cfg.GetMasterKeyPresent())
	require.Equal(t, "Europe/Warsaw", cfg.GetTimezone())
	require.Equal(t, pricing.Version, cfg.GetPriceVersion())
	require.True(t, cfg.GetDesignGenerationEnabled())

	// And no key, anywhere in it.
	wire, err := protojson.Marshal(cfg)
	require.NoError(t, err)
	for _, secret := range []string{aiDBKeyOpenAI, aiDBAdminAnthro, aiEnvOpenRouter, aiEnvFal, aiEnvRecraft} {
		require.NotContains(t, string(wire), secret)
	}
}

// TestAiConfigNotesWhenDesignGenerationIsOff.
//
// MUTATIONS IT CATCHES: the note keyed on the wrong flag; a chat-only provider told design generation
// is off; "via openrouter" winning over "off" for recraft (with generation off nothing calls it at all);
// master_key_present hard-wired to true.
func TestAiConfigNotesWhenDesignGenerationIsOff(t *testing.T) {
	disabled, err := keyring.New("")
	require.NoError(t, err)
	h := newAIHarness(t, aiHarnessOpt{designGeneration: false, ring: disabled})
	h.expectConfigRead(nil)

	cfg, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
	require.NoError(t, err)
	for _, k := range []string{"openai", "google", "openrouter", "apibost", "fal", "meshy", "recraft"} {
		require.Equal(t, aiNoteDesignOff, aiProvider(t, cfg, k).GetNote(), k)
	}
	for _, k := range []string{"anthropic", "runblob"} {
		require.Empty(t, aiProvider(t, cfg, k).GetNote(), k)
	}
	require.False(t, cfg.GetDesignGenerationEnabled())
	require.False(t, cfg.GetMasterKeyPresent())
}

// TestAiConfigRetryConvergesOnTheSecondRead — the store rows are version 8, the registry is at 7 and
// its source has since moved to 9 (a second write). The one reload brings the registry to 9, the store
// is read again at 9, and the page is version 9 on both sides: openai's key cleared, and nobody named
// as having set it.
//
// MUTATIONS IT CATCHES: skipping the retry (the first mismatch refused at once — red here, no page);
// re-reading only the registry (version 8 rows beside version 9 key state: «key: none» next to «set by
// im»); retrying past the one reload (a loop against a store that keeps moving); dropping the version
// check (version 8 rows beside the version 7 key state, «db ···1a2b»).
func TestAiConfigRetryConvergesOnTheSecondRead(t *testing.T) {
	h := newAIHarness(t, aiHarnessOpt{})
	v8 := h.cfg
	v8.Settings.ConfigVersion = aiTestVersion + 1
	v9 := aiKeyCleared(v8)
	h.store.set(v9) // the registry's source is already at 9; this registry has not polled
	h.ai.EXPECT().GetConfig(mock.Anything).Return(&v8, nil).Once()
	h.ai.EXPECT().GetConfig(mock.Anything).Return(&v9, nil).Once()
	h.ai.EXPECT().RecentFaults(mock.Anything, mock.Anything).Return(nil, nil).Once()
	before := h.store.reloads()

	cfg, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
	require.NoError(t, err)
	require.Equal(t, before+1, h.store.reloads(), "a registry behind the store is reloaded exactly once")
	require.Equal(t, aiTestVersion+2, cfg.GetConfigVersion(), "the second read's rows")
	openai := aiProvider(t, cfg, "openai")
	require.Equal(t, registry.KeySourceNone, openai.GetKeySource(), "the reloaded registry's key state")
	require.Empty(t, openai.GetKeyLast4())
	require.Empty(t, openai.GetKeyUpdatedBy(), "the cleared slot of the same version")

	h2 := newAIHarness(t, aiHarnessOpt{})
	before = h2.store.reloads()
	h2.expectConfigRead(nil)
	_, err = h2.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
	require.NoError(t, err)
	require.Equal(t, before, h2.store.reloads(), "an up-to-date registry is not reloaded on a read")
}

// TestAiConfigPersistentMismatchIsUnavailable (REVIEW-FIXD P2 #3) — the store rows are version 8 on
// both reads and the registry's source stays at 7, so the reload leaves it at 7: the two sides never
// meet. The read is refused with Unavailable and a WARN naming both versions; no page is built — the
// badge read that precedes the view never happens (the mock has no expectation for it) and nothing is
// returned.
//
// MUTATIONS IT CATCHES: rendering on the persistent mismatch (the pre-fix WARN-and-build: version 8
// rows beside version 7's «db ···1a2b» — the badge read fails the mock and a page comes back); the WARN
// dropped; skipping the retry (no reload).
func TestAiConfigPersistentMismatchIsUnavailable(t *testing.T) {
	logs := aiCaptureLog(t)
	h := newAIHarness(t, aiHarnessOpt{})
	v8 := h.cfg
	v8.Settings.ConfigVersion = aiTestVersion + 1 // the store moved on; the registry's source did not
	h.ai.EXPECT().GetConfig(mock.Anything).Return(&v8, nil).Times(2)
	before := h.store.reloads()

	resp, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
	st := aiRequireCode(t, err, codes.Unavailable)
	require.Equal(t, "the AI configuration changed while it was being read; reload", st.Message())
	require.Nil(t, resp, "no page joins two versions")
	require.Equal(t, before+1, h.store.reloads(), "one reload before giving up")
	_, regVersion := h.reg.ProvidersAt()
	require.Equal(t, aiTestVersion, regVersion, "the registry stayed at 7 — the mismatch was real to the end")
	require.Contains(t, logs.String(), "still describe different config versions after a reload")
	require.Contains(t, logs.String(), `"store_version":8`)
	require.Contains(t, logs.String(), `"registry_version":7`)
}

// aiKeyCleared is cfg one version on, with openai's stored api key cleared by somebody else: the
// registry then answers openai with no key at all (openai has no env key here).
func aiKeyCleared(cfg entity.AIConfig) entity.AIConfig {
	next := cfg
	next.Providers = slices.Clone(cfg.Providers)
	for i := range next.Providers {
		if next.Providers[i].Key == entity.AIProviderOpenAI {
			next.Providers[i].APIKeyEnc, next.Providers[i].APIKeyLast4 = nil, ""
			next.Providers[i].APIKeyUpdatedBy = "someone-else"
		}
	}
	next.Settings.ConfigVersion++
	return next
}

// TestAiConfigJoinsOneVersion (Codex B #7) — the panel's store rows and registry key state always
// describe ONE config_version.
//
// MUTATIONS IT CATCHES: the pre-fix shape — Version() compared first, the registry's Providers() read
// after the badges — red in "a reload during the badge read" (the old version's rows beside the new
// version's key state: «key: none» next to «set by im»); Providers() and Version() read side by side
// instead of ProvidersAt — red in "a reload while the key state renders" (the states of one snapshot,
// the number of the next: a needless reload, then the new states beside the old rows); only the
// registry re-read after the reload — red in "rows read just before a write" (the old rows stay).
func TestAiConfigJoinsOneVersion(t *testing.T) {
	t.Run("registry one version behind: one reload, then both sides of the new version", func(t *testing.T) {
		h := newAIHarness(t, aiHarnessOpt{})
		next := aiKeyCleared(h.cfg)
		h.store.set(next) // another instance wrote; this registry has not polled yet
		h.cfg = next
		before := h.store.reloads()
		h.expectConfigRead(nil)
		cfg, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
		require.NoError(t, err)
		require.Equal(t, before+1, h.store.reloads(), "exactly one reload")
		require.Equal(t, aiTestVersion+1, cfg.GetConfigVersion())
		openai := aiProvider(t, cfg, "openai")
		require.Equal(t, registry.KeySourceNone, openai.GetKeySource())
		require.Empty(t, openai.GetKeyLast4())
		require.Empty(t, openai.GetKeyUpdatedBy(), "the cleared slot of the same version")
	})

	t.Run("rows read just before a write the registry already has: both are read again", func(t *testing.T) {
		h := newAIHarness(t, aiHarnessOpt{})
		next := aiKeyCleared(h.cfg)
		old := h.cfg
		h.ai.EXPECT().GetConfig(mock.Anything).Return(&old, nil).Once() // the read before the write
		h.ai.EXPECT().GetConfig(mock.Anything).Return(&next, nil).Once()
		h.ai.EXPECT().RecentFaults(mock.Anything, mock.Anything).Return(nil, nil).Once()
		h.store.set(next)
		require.NoError(t, h.reg.Reload(context.Background())) // this instance wrote it, and reloaded
		before := h.store.reloads()
		cfg, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
		require.NoError(t, err)
		require.Equal(t, before+1, h.store.reloads(), "exactly one reload")
		require.Equal(t, aiTestVersion+1, cfg.GetConfigVersion(), "the store is read again, not only the registry")
		openai := aiProvider(t, cfg, "openai")
		require.Equal(t, registry.KeySourceNone, openai.GetKeySource())
		require.Empty(t, openai.GetKeyUpdatedBy())
	})

	// In the next two the store rows are version 7 (openai's key stored by im) and another writer
	// clears that key and reloads THIS registry to version 8 in the middle of the read. The page must
	// be version 7 throughout: key from the database, ···1a2b, set by im.
	consistent := func(t *testing.T, cfg *pb_admin.GetAiProvidersConfigResponse) {
		t.Helper()
		require.Equal(t, aiTestVersion, cfg.GetConfigVersion())
		openai := aiProvider(t, cfg, "openai")
		require.Equal(t, registry.KeySourceDB, openai.GetKeySource(), "key state of the rows' own version")
		require.Equal(t, "1a2b", openai.GetKeyLast4())
		require.Equal(t, aiTestUser, openai.GetKeyUpdatedBy())
	}
	swap := func(t *testing.T, h *aiHarness) {
		h.store.set(aiKeyCleared(h.cfg))
		require.NoError(t, h.reg.Reload(context.Background()))
	}

	t.Run("a reload during the badge read", func(t *testing.T) {
		h := newAIHarness(t, aiHarnessOpt{})
		c := h.cfg
		h.ai.EXPECT().GetConfig(mock.Anything).Return(&c, nil)
		h.ai.EXPECT().RecentFaults(mock.Anything, mock.Anything).
			Run(func(context.Context, time.Time) { swap(t, h) }).Return(nil, nil).Once()
		cfg, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
		require.NoError(t, err)
		consistent(t, cfg)
	})

	t.Run("a reload while the key state renders", func(t *testing.T) {
		var (
			h     *aiHarness
			armed atomic.Bool
		)
		h = newAIHarness(t, aiHarnessOpt{clock: func() time.Time {
			if armed.CompareAndSwap(true, false) {
				swap(t, h) // the registry reads its clock while it renders the key state
			}
			return time.Now()
		}})
		h.expectConfigRead(nil)
		armed.Store(true)
		cfg, err := h.s.GetAiProvidersConfig(aiCtx(), &pb_admin.GetAiProvidersConfigRequest{})
		require.NoError(t, err)
		require.False(t, armed.Load(), "the swap must have happened inside the read")
		consistent(t, cfg)
	})
}

// ───────────────────────── writes ─────────────────────────

// TestAiProviderSwitchWritesAndAnswersConfig.
//
// MUTATIONS IT CATCHES: the patch, expected_version or actor not passed to the store; the registry not
// reloaded after the write (this instance would keep serving the old switch for up to a minute).
func TestAiProviderSwitchWritesAndAnswersConfig(t *testing.T) {
	h := newAIHarness(t, aiHarnessOpt{})
	before := h.store.reloads()
	h.ai.EXPECT().UpdateProvider(mock.Anything, "google",
		mock.MatchedBy(func(p entity.AIProviderPatch) bool { return p.Enabled != nil && *p.Enabled }),
		aiTestVersion, aiTestUser).Return(nil).Once()
	h.expectConfigRead(nil)

	resp, err := h.s.UpdateAiProvider(aiCtx(), &pb_admin.UpdateAiProviderRequest{
		ProviderKey: " google ", Enabled: true, ExpectedVersion: aiTestVersion,
	})
	require.NoError(t, err)
	require.Equal(t, aiTestVersion, resp.GetConfig().GetConfigVersion())
	require.Greater(t, h.store.reloads(), before, "the registry is reloaded after the write")
}

// TestAiProviderStalePageIsFailedPrecondition — every checked write.
//
// MUTATIONS IT CATCHES: mapping ErrAIVersionConflict to Internal / Aborted, or losing the "reload"
// words; building (or reloading) the config after a lost compare-and-swap — the mock has no read
// expectations; a slug recorded by the handler instead of inside the store's route transaction (no
// UpsertModel expectation either).
func TestAiProviderStalePageIsFailedPrecondition(t *testing.T) {
	conflict := fmt.Errorf("tx: %w", entity.ErrAIVersionConflict)
	cases := map[string]struct {
		expect func(h *aiHarness)
		call   func(s *Server) error
	}{
		"UpdateAiProvider": {
			expect: func(h *aiHarness) {
				h.ai.EXPECT().UpdateProvider(mock.Anything, "fal", mock.Anything, uint64(3), aiTestUser).Return(conflict).Once()
			},
			call: func(s *Server) error {
				_, err := s.UpdateAiProvider(aiCtx(), &pb_admin.UpdateAiProviderRequest{ProviderKey: "fal", ExpectedVersion: 3})
				return err
			},
		},
		"SetAiDefaults": {
			expect: func(h *aiHarness) {
				h.ai.EXPECT().SetDefaults(mock.Anything, mock.Anything, uint64(3), aiTestUser).Return(conflict).Once()
			},
			call: func(s *Server) error {
				_, err := s.SetAiDefaults(aiCtx(), &pb_admin.SetAiDefaultsRequest{ChatProviderKey: "anthropic", ExpectedVersion: 3})
				return err
			},
		},
		"SetAiRoute": {
			expect: func(h *aiHarness) {
				h.ai.EXPECT().SetRoute(mock.Anything, entity.AIPurposeNoteMarkdown, mock.Anything, uint64(3), aiTestUser).Return(conflict).Once()
			},
			call: func(s *Server) error {
				_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{
					Purpose:         entity.AIPurposeNoteMarkdown,
					Primary:         &pb_admin.AiRouteCandidate{ProviderKey: "openrouter", Model: "x-ai/never-recorded"},
					ExpectedVersion: 3,
				})
				return err
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newAIHarness(t, aiHarnessOpt{})
			before := h.store.reloads()
			c.expect(h)
			st := aiRequireCode(t, c.call(h.s), codes.FailedPrecondition)
			require.Equal(t, aiStaleMessage, st.Message())
			require.Equal(t, before, h.store.reloads(), "a lost compare-and-swap reloads nothing")
		})
	}
}

// TestAiProviderUnknownRefusedBeforeAnyWrite — every refusal here runs against a dependency.AI mock
// with NO expectations: a single store call fails the test.
//
// MUTATIONS IT CATCHES: any validation moved after the store call (or dropped: the store would then be
// called and the mock fails); the capability check in SetAiRoute dropped (threed → openrouter would be
// written); "" accepted for a capability with no default; the model bound not checked; an unknown
// provider echoed back in the error.
func TestAiProviderUnknownRefusedBeforeAnyWrite(t *testing.T) {
	long := strings.Repeat("m", aiModelMaxRunes+1)
	cases := []struct {
		name  string
		call  func(s *Server) error
		code  codes.Code
		field string
	}{
		{"switch: unknown provider", func(s *Server) error {
			_, err := s.UpdateAiProvider(aiCtx(), &pb_admin.UpdateAiProviderRequest{ProviderKey: "sk-looks-like-a-key-0000", Enabled: true})
			return err
		}, codes.NotFound, ""},
		{"key: unknown provider", func(s *Server) error {
			_, err := s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{ProviderKey: "sk-looks-like-a-key-0000", Kind: "api", Value: "sk-looks-like-a-key-0000"})
			return err
		}, codes.NotFound, ""},
		{"key: unknown kind", func(s *Server) error {
			_, err := s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{ProviderKey: "fal", Kind: "root", Value: "x"})
			return err
		}, codes.InvalidArgument, "kind"},
		{"key: admin kind on a provider without a cost API", func(s *Server) error {
			_, err := s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{ProviderKey: "meshy", Kind: "admin", Value: "x"})
			return err
		}, codes.InvalidArgument, "kind"},
		{"key: a key with a line break", func(s *Server) error {
			_, err := s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{ProviderKey: "fal", Kind: "api", Value: "abc\ndef"})
			return err
		}, codes.InvalidArgument, "value"},
		{"defaults: unknown provider", func(s *Server) error {
			_, err := s.SetAiDefaults(aiCtx(), &pb_admin.SetAiDefaultsRequest{ChatProviderKey: "nope"})
			return err
		}, codes.InvalidArgument, "chat_provider_key"},
		{"defaults: a provider that cannot serve images", func(s *Server) error {
			_, err := s.SetAiDefaults(aiCtx(), &pb_admin.SetAiDefaultsRequest{ChatProviderKey: "openrouter", ImageProviderKey: "anthropic"})
			return err
		}, codes.InvalidArgument, "image_provider_key"},
		{"defaults: nothing named", func(s *Server) error {
			_, err := s.SetAiDefaults(aiCtx(), &pb_admin.SetAiDefaultsRequest{})
			return err
		}, codes.InvalidArgument, "chat_provider_key"},
		{"route: unknown purpose", func(s *Server) error {
			_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: "chat.nothing", Primary: &pb_admin.AiRouteCandidate{}})
			return err
		}, codes.NotFound, ""},
		{"route: no primary", func(s *Server) error {
			_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeVector})
			return err
		}, codes.InvalidArgument, "primary"},
		{"route: capability mismatch on the primary", func(s *Server) error {
			_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeThreed,
				Primary: &pb_admin.AiRouteCandidate{ProviderKey: "openrouter"}})
			return err
		}, codes.InvalidArgument, "primary.provider_key"},
		{"route: capability mismatch on the fallback", func(s *Server) error {
			_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeImageGenerate,
				Primary:  &pb_admin.AiRouteCandidate{ProviderKey: "openrouter"},
				Fallback: &pb_admin.AiRouteCandidate{ProviderKey: "anthropic"}})
			return err
		}, codes.InvalidArgument, "fallback.provider_key"},
		{"route: unknown provider", func(s *Server) error {
			_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeNoteMarkdown,
				Primary: &pb_admin.AiRouteCandidate{ProviderKey: "nope"}})
			return err
		}, codes.InvalidArgument, "primary.provider_key"},
		{"route: the default provider for a capability that has none", func(s *Server) error {
			_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeImageCutout,
				Primary: &pb_admin.AiRouteCandidate{ProviderKey: ""}})
			return err
		}, codes.InvalidArgument, "primary.provider_key"},
		{"route: model over 128 characters", func(s *Server) error {
			_, err := s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeNoteMarkdown,
				Primary: &pb_admin.AiRouteCandidate{ProviderKey: "openrouter", Model: long}})
			return err
		}, codes.InvalidArgument, "primary.model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAIHarness(t, aiHarnessOpt{})
			before := h.store.reloads()
			st := aiRequireCode(t, c.call(h.s), c.code)
			require.Equal(t, c.field, aiViolationField(st))
			require.NotContains(t, st.Message(), "sk-looks-like-a-key", "the request's strings are not echoed")
			require.Equal(t, before, h.store.reloads())
		})
	}
}

// TestAiDefaultsEmptyLeavesUnchanged.
//
// MUTATION IT CATCHES: "" sent to the store as a value (it would write an empty key over the stored default)
// instead of an omitted field.
func TestAiDefaultsEmptyLeavesUnchanged(t *testing.T) {
	h := newAIHarness(t, aiHarnessOpt{})
	h.ai.EXPECT().SetDefaults(mock.Anything, mock.MatchedBy(func(p entity.AIDefaultsPatch) bool {
		return p.ChatProviderKey != nil && *p.ChatProviderKey == "anthropic" && p.ImageProviderKey == nil
	}), aiTestVersion, aiTestUser).Return(nil).Once()
	h.expectConfigRead(nil)
	_, err := h.s.SetAiDefaults(aiCtx(), &pb_admin.SetAiDefaultsRequest{ChatProviderKey: "anthropic", ExpectedVersion: aiTestVersion})
	require.NoError(t, err)
}

// TestAiRouteLeavesModelRecordingToTheStore (Codex B #6) — the handler hands the route's candidates,
// trimmed and positioned, to the store's ONE checked SetRoute and records nothing itself: the store
// files every named slug in ai_model inside that transaction. The mock has no UpsertModel expectation,
// so a call fails the test; AssertNotCalled says so in words.
//
// MUTATIONS IT CATCHES: the handler's own after-the-fact UpsertModel coming back (a second, unchecked
// transaction whose "" provider was resolved in a third snapshot — a default changed in between files
// the slug under a provider the route no longer follows); the candidates not reaching the store as
// entered (a "" provider replaced by a guess, the fallback dropped, the model untrimmed).
func TestAiRouteLeavesModelRecordingToTheStore(t *testing.T) {
	h := newAIHarness(t, aiHarnessOpt{})
	h.ai.EXPECT().SetRoute(mock.Anything, entity.AIPurposeNoteMarkdown, []entity.AIRouteCandidate{
		{Position: 1, ProviderKey: "", Model: "custom/chat-1"},
		{Position: 2, ProviderKey: "openrouter", Model: "x-ai/grok-9"},
	}, aiTestVersion, aiTestUser).Return(nil).Once()
	h.expectConfigRead(nil)

	_, err := h.s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{
		Purpose:         " " + entity.AIPurposeNoteMarkdown + " ",
		Primary:         &pb_admin.AiRouteCandidate{ProviderKey: "", Model: " custom/chat-1 "},
		Fallback:        &pb_admin.AiRouteCandidate{ProviderKey: "openrouter", Model: "x-ai/grok-9"},
		ExpectedVersion: aiTestVersion,
	})
	require.NoError(t, err)
	h.ai.AssertNotCalled(t, "UpsertModel", mock.Anything, mock.Anything, mock.Anything)

	// A slug the catalogue names, and a model-less route: the same single write, nothing else.
	h2 := newAIHarness(t, aiHarnessOpt{})
	h2.ai.EXPECT().SetRoute(mock.Anything, entity.AIPurposeThreed, []entity.AIRouteCandidate{
		{Position: 1, ProviderKey: "meshy", Model: ""},
	}, aiTestVersion, aiTestUser).Return(nil).Once()
	h2.ai.EXPECT().SetRoute(mock.Anything, entity.AIPurposeImageGenerate, []entity.AIRouteCandidate{
		{Position: 1, ProviderKey: "openrouter", Model: "openai/gpt-image-2"},
	}, aiTestVersion, aiTestUser).Return(nil).Once()
	h2.expectConfigRead(nil)
	_, err = h2.s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeThreed,
		Primary: &pb_admin.AiRouteCandidate{ProviderKey: "meshy"}, ExpectedVersion: aiTestVersion})
	require.NoError(t, err)
	_, err = h2.s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{Purpose: entity.AIPurposeImageGenerate,
		Primary: &pb_admin.AiRouteCandidate{ProviderKey: "openrouter", Model: "openai/gpt-image-2"}, ExpectedVersion: aiTestVersion})
	require.NoError(t, err)
	h2.ai.AssertNotCalled(t, "UpsertModel", mock.Anything, mock.Anything, mock.Anything)
}

// TestAiRouteFallbackSameAsPrimaryInEffectRefused (FIX-D P2) — "" as a model is the purpose's env
// default on an openrouter row, resolved the way the router resolves it (router.EffectiveModel), so
// «openrouter / ""» against «openrouter / <that very slug>» is the primary twice: the router would skip
// the repeat and the route would show a fallback the runtime does not have. The same slug named for a
// purpose whose default is ANOTHER slug is a real fallback.
//
// MUTATION IT CATCHES (measured red): aiSameCandidate compares the raw models (aiEffectiveSlug returns
// its model argument) → "" and the default slug pass as different and the write goes through.
func TestAiRouteFallbackSameAsPrimaryInEffectRefused(t *testing.T) {
	const chatSlug, analysisSlug = "anthropic/claude-sonnet-5", "anthropic/claude-opus-5"
	cand := func(p, m string) *pb_admin.AiRouteCandidate {
		return &pb_admin.AiRouteCandidate{ProviderKey: p, Model: m}
	}
	withRouter := func(h *aiHarness) {
		h.s.SetAIRouter(router.NewSingle(entity.AIProviderOpenRouter, nil, "",
			router.WithDefaults(router.Defaults{Chat: chatSlug, Analysis: analysisSlug, Ideas: "google/x"})))
	}
	for _, c := range []struct {
		name              string
		primary, fallback *pb_admin.AiRouteCandidate
		readsConfig       bool
	}{
		{"openrouter's default slug and that slug by name", cand("openrouter", ""), cand("openrouter", chatSlug), false},
		{"that slug by name and openrouter's default slug", cand("openrouter", chatSlug), cand(" openrouter ", " "), false},
		{"the default provider's default slug and the slug by name", cand("", ""), cand("openrouter", chatSlug), true},
		{"the default provider twice, once by slug", cand("", chatSlug), cand("", ""), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newAIHarness(t, aiHarnessOpt{})
			withRouter(h)
			if c.readsConfig {
				cfg := h.cfg // version 7, default chat provider openrouter
				h.ai.EXPECT().GetConfig(mock.Anything).Return(&cfg, nil).Once()
			}
			_, err := h.s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{
				Purpose: entity.AIPurposeNoteMarkdown, Primary: c.primary, Fallback: c.fallback, ExpectedVersion: aiTestVersion,
			})
			st := aiRequireCode(t, err, codes.InvalidArgument)
			require.Equal(t, "fallback", aiViolationField(st))
			require.Contains(t, st.Message(), "same_as_primary")
		})
	}

	// Not the same: the chat slug is a real fallback for a purpose whose "" is the ANALYSIS slug.
	h := newAIHarness(t, aiHarnessOpt{})
	withRouter(h)
	h.ai.EXPECT().SetRoute(mock.Anything, entity.AIPurposeTechCardEnhance, mock.Anything, aiTestVersion, aiTestUser).
		Return(nil).Once()
	h.expectConfigRead(nil)
	_, err := h.s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{
		Purpose: entity.AIPurposeTechCardEnhance, Primary: cand("openrouter", ""), Fallback: cand("openrouter", chatSlug),
		ExpectedVersion: aiTestVersion,
	})
	require.NoError(t, err)
}

// TestAiRouteFallbackSameAsPrimaryRefused (Codex B #8) — a fallback that is the primary itself is
// refused on the fallback field before anything is written; "" is the capability's default on both
// sides. The registry drops an exact repeat, so such a route would show a fallback the runtime does
// not have. The refusals run against a mock with no write expectation (and, where no default is
// needed, no read expectation either: the verdict is reached without one).
//
// MUTATIONS IT CATCHES: no check (the write goes through: the mock fails on SetRoute); providers
// compared raw, without resolving "" ("" vs openrouter, openrouter being the chat default, passes); ""
// resolved against a configuration of another version than the page's (a stale page is refused as a
// duplicate on a default it never saw, instead of being told to reload).
func TestAiRouteFallbackSameAsPrimaryRefused(t *testing.T) {
	cand := func(p, m string) *pb_admin.AiRouteCandidate {
		return &pb_admin.AiRouteCandidate{ProviderKey: p, Model: m}
	}
	for _, c := range []struct {
		name              string
		primary, fallback *pb_admin.AiRouteCandidate
		readsConfig       bool
	}{
		{"the same provider and model", cand("openrouter", "x-ai/grok-9"), cand(" openrouter ", " x-ai/grok-9 "), false},
		{"the default twice, same model", cand("", "x-ai/grok-9"), cand("", "x-ai/grok-9"), false},
		{"the default twice, no model", cand("", ""), cand("", ""), false},
		{"the default and the default by name", cand("", "x-ai/grok-9"), cand("openrouter", "x-ai/grok-9"), true},
		{"the default by name and the default", cand("openrouter", ""), cand("", ""), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newAIHarness(t, aiHarnessOpt{})
			if c.readsConfig {
				cfg := h.cfg // version 7, default chat provider openrouter
				h.ai.EXPECT().GetConfig(mock.Anything).Return(&cfg, nil).Once()
			}
			before := h.store.reloads()
			_, err := h.s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{
				Purpose: entity.AIPurposeNoteMarkdown, Primary: c.primary, Fallback: c.fallback, ExpectedVersion: aiTestVersion,
			})
			st := aiRequireCode(t, err, codes.InvalidArgument)
			require.Equal(t, "fallback", aiViolationField(st))
			require.Contains(t, st.Message(), "same_as_primary")
			require.Equal(t, before, h.store.reloads(), "nothing written, nothing reloaded")
		})
	}

	// Not the same: a different model; "" against a provider that is not the default; and "" against
	// the default by name on a page that is stale — the configuration read is version 8, the page saved
	// against 7, so no verdict is drawn from a default the page never saw and the store's
	// compare-and-swap answers "reload".
	for _, c := range []struct {
		name              string
		primary, fallback *pb_admin.AiRouteCandidate
		storeVersion      uint64
		want              codes.Code
	}{
		{"another model", cand("openrouter", "x-ai/grok-9"), cand("openrouter", "x-ai/grok-8"), 0, codes.OK},
		// Two named slugs that differ need no default to tell them apart: no configuration read.
		{"another model on the default", cand("", "x-ai/grok-9"), cand("openrouter", "x-ai/grok-8"), 0, codes.OK},
		{"the default and another provider", cand("", "x-ai/grok-9"), cand("apibost", "x-ai/grok-9"), aiTestVersion, codes.OK},
		{"a stale page", cand("", "x-ai/grok-9"), cand("openrouter", "x-ai/grok-9"), aiTestVersion + 1, codes.FailedPrecondition},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newAIHarness(t, aiHarnessOpt{})
			if c.storeVersion != 0 {
				cfg := h.cfg
				cfg.Settings.ConfigVersion = c.storeVersion
				h.ai.EXPECT().GetConfig(mock.Anything).Return(&cfg, nil).Once()
			}
			var storeErr error
			if c.want == codes.FailedPrecondition {
				storeErr = entity.ErrAIVersionConflict
			}
			h.ai.EXPECT().SetRoute(mock.Anything, entity.AIPurposeNoteMarkdown, mock.Anything, aiTestVersion, aiTestUser).
				Return(storeErr).Once()
			if c.want == codes.OK {
				h.expectConfigRead(nil)
			}
			_, err := h.s.SetAiRoute(aiCtx(), &pb_admin.SetAiRouteRequest{
				Purpose: entity.AIPurposeNoteMarkdown, Primary: c.primary, Fallback: c.fallback, ExpectedVersion: aiTestVersion,
			})
			if c.want == codes.OK {
				require.NoError(t, err)
				if c.storeVersion == 0 {
					// The verdict needed no default: the one read is the page rebuilt after the write.
					h.ai.AssertNumberOfCalls(t, "GetConfig", 1)
				}
				return
			}
			aiRequireCode(t, err, c.want)
		})
	}
}

// ───────────────────────── keys ─────────────────────────

// aiCaptureLog routes slog.Default into a buffer for the test (the registry built afterwards logs
// there too).
func aiCaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestAiKeyNeverEchoedAndProbedAsSaved.
//
// MUTATIONS IT CATCHES: the value in any log line (e.g. `slog.String("value", value)` on the saved
// line), in the response (the probe message, a config field) or in the store's arguments as plaintext;
// the probe run with anything but the just-saved key (the registry's answer, the env key); last4 not
// logged; the breakers not reset after the save (openai's chat breaker stays open).
func TestAiKeyNeverEchoedAndProbedAsSaved(t *testing.T) {
	const secret = "sk-proj-THIS-IS-THE-SECRET-VALUE-7h3k"
	logs := aiCaptureLog(t)
	h := newAIHarness(t, aiHarnessOpt{})
	for i := 0; i < 3; i++ {
		h.reg.RecordFailure(entity.AIProviderOpenAI, entity.AICapabilityChat, registry.Admission{},
			&aiprov.CallError{Provider: "openai", Retryable: true, Err: fmt.Errorf("503")})
	}
	require.Equal(t, registry.BreakerOpen, h.reg.BreakerState(entity.AIProviderOpenAI, entity.AICapabilityChat))

	var stored []byte
	h.ai.EXPECT().SetProviderKey(mock.Anything, "openai", entity.AIKeyAPI, mock.Anything, "7h3k", aiTestUser).
		Run(func(_ context.Context, _ string, _ entity.AIKeyKind, enc []byte, _ string, _ string) { stored = enc }).
		Return(nil).Once()
	h.expectConfigRead(nil)

	resp, err := h.s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{
		ProviderKey: "openai", Kind: "api", Value: "  " + secret + "\n",
	})
	require.NoError(t, err)

	// Stored sealed, bound to its row and column.
	require.NotContains(t, string(stored), secret)
	opened, err := h.ring.Open(stored, keyring.AAD("openai", "api"))
	require.NoError(t, err)
	require.Equal(t, secret, opened)

	// Probed with the key just saved, at the provider's constant host.
	require.Equal(t, 1, h.probe.count())
	req := h.probe.reqs[0]
	require.Equal(t, "Bearer "+secret, req.Header.Get("Authorization"))
	require.Equal(t, "api.openai.com", req.URL.Host)
	require.True(t, resp.GetProbe().GetOk())
	require.Equal(t, "key accepted", resp.GetProbe().GetMessage())

	// The breakers are a new chance.
	require.Equal(t, registry.BreakerClosed, h.reg.BreakerState(entity.AIProviderOpenAI, entity.AICapabilityChat))

	// Nowhere out: not in the response, not in any log line.
	wire, err := protojson.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(wire), secret)
	require.NotContains(t, logs.String(), secret)
	require.NotContains(t, logs.String(), "THIS-IS-THE-SECRET")
	require.Contains(t, logs.String(), `"msg":"ai provider key saved"`)
	require.Contains(t, logs.String(), `"last4":"7h3k"`)
	require.Contains(t, logs.String(), `"by":"im"`)
}

// TestAiKeyAdminKindProbesTheCostAPI.
//
// MUTATIONS IT CATCHES: an admin key probed as an api key (the probe would hit /v1/models and say the
// reconciliation key works when the cost API refuses it); an admin key save resetting the breakers of
// the calls it never serves.
func TestAiKeyAdminKindProbesTheCostAPI(t *testing.T) {
	h := newAIHarness(t, aiHarnessOpt{})
	for i := 0; i < 3; i++ {
		h.reg.RecordFailure(entity.AIProviderOpenAI, entity.AICapabilityChat, registry.Admission{},
			&aiprov.CallError{Provider: "openai", Retryable: true, Err: fmt.Errorf("503")})
	}
	h.probe.status = http.StatusForbidden
	h.ai.EXPECT().SetProviderKey(mock.Anything, "openai", entity.AIKeyAdmin, mock.Anything, "cost", aiTestUser).Return(nil).Once()
	h.expectConfigRead(nil)

	resp, err := h.s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{ProviderKey: "openai", Kind: "admin", Value: "sk-admin-key-cost"})
	require.NoError(t, err)
	require.Equal(t, 1, h.probe.count())
	require.Equal(t, "/v1/organization/costs", h.probe.reqs[0].URL.Path)
	require.False(t, resp.GetProbe().GetOk())
	require.Equal(t, probe.CodeKeyRejected, resp.GetProbe().GetCode())
	require.Equal(t, registry.BreakerOpen, h.reg.BreakerState(entity.AIProviderOpenAI, entity.AICapabilityChat))
}

// TestAiAdminKeyReconcilesOnlyAfterAnAcceptedProbe — the saved reconciliation key gets an immediate
// detached fetch only when the provider accepted it. API keys use the hourly worker; a refused admin
// key is kept (so the admin can see and replace it) but is never used for a cost fetch.
//
// MUTATION IT CATCHES: drop `res.OK` from the trigger condition → the refused-probe case records a
// call and turns red.
func TestAiAdminKeyReconcilesOnlyAfterAnAcceptedProbe(t *testing.T) {
	t.Run("accepted admin key", func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		type record struct {
			provider    string
			err         error
			deadline    time.Time
			hasDeadline bool
		}
		recorded := make(chan record, 1)
		h := newAIHarness(t, aiHarnessOpt{reconcile: func(ctx context.Context, provider string) {
			calls.Add(1)
			<-release
			deadline, ok := ctx.Deadline()
			recorded <- record{provider: provider, err: ctx.Err(), deadline: deadline, hasDeadline: ok}
		}})
		h.ai.EXPECT().SetProviderKey(mock.Anything, "openai", entity.AIKeyAdmin, mock.Anything, "cost", aiTestUser).Return(nil).Once()
		h.expectConfigRead(nil)

		ctx, cancel := context.WithCancel(aiCtx())
		_, err := h.s.SetAiProviderKey(ctx, &pb_admin.SetAiProviderKeyRequest{
			ProviderKey: "openai", Kind: "admin", Value: "sk-admin-key-cost",
		})
		require.NoError(t, err)
		cancel() // the detached run must survive the request ending
		close(release)

		select {
		case got := <-recorded:
			require.Equal(t, "openai", got.provider)
			require.NoError(t, got.err)
			require.True(t, got.hasDeadline)
			require.WithinDuration(t, time.Now().Add(time.Minute), got.deadline, 5*time.Second)
		case <-time.After(5 * time.Second):
			t.Fatal("the accepted admin key did not trigger reconciliation")
		}
		require.Equal(t, int32(1), calls.Load())
	})

	for _, tc := range []struct {
		name, kind string
		probe      int
	}{
		{name: "refused admin key", kind: "admin", probe: http.StatusForbidden},
		{name: "accepted api key", kind: "api", probe: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := make(chan string, 1)
			h := newAIHarness(t, aiHarnessOpt{reconcile: func(_ context.Context, provider string) { called <- provider }})
			h.probe.status = tc.probe
			h.ai.EXPECT().SetProviderKey(mock.Anything, "openai", entity.AIKeyKind(tc.kind), mock.Anything, "cost", aiTestUser).Return(nil).Once()
			h.expectConfigRead(nil)

			_, err := h.s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{
				ProviderKey: "openai", Kind: tc.kind, Value: "sk-admin-key-cost",
			})
			require.NoError(t, err)
			select {
			case provider := <-called:
				t.Fatalf("unexpected reconciliation for %s", provider)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// TestAiKeyClearDoesNotProbe.
//
// MUTATIONS IT CATCHES: a probe of the cleared slot (it would send "" — or worse, the env key — to the
// provider and report a verdict about a key nobody saved); a clear refused for want of a master key
// (clearing seals nothing); a clear sent to the store with a ciphertext or a last4.
func TestAiKeyClearDoesNotProbe(t *testing.T) {
	disabled, err := keyring.New("")
	require.NoError(t, err)
	h := newAIHarness(t, aiHarnessOpt{ring: disabled})
	h.ai.EXPECT().SetProviderKey(mock.Anything, "fal", entity.AIKeyAPI,
		mock.MatchedBy(func(enc []byte) bool { return len(enc) == 0 }), "", aiTestUser).Return(nil).Once()
	h.expectConfigRead(nil)

	resp, err := h.s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{ProviderKey: "fal", Kind: "api", Value: "   "})
	require.NoError(t, err)
	require.Nil(t, resp.GetProbe(), "unset when the slot was cleared")
	require.NotNil(t, resp.GetConfig())
	require.Zero(t, h.probe.count())
}

// TestAiKeyRingDisabledRefusesToStore.
//
// MUTATIONS IT CATCHES: storing a key with no master key (the registry could never open it; the mock
// has no SetProviderKey expectation); the refusal worded without the variable's name, or echoing the
// value.
func TestAiKeyRingDisabledRefusesToStore(t *testing.T) {
	disabled, err := keyring.New("")
	require.NoError(t, err)
	h := newAIHarness(t, aiHarnessOpt{ring: disabled})
	st := aiRequireCode(t, func() error {
		_, err := h.s.SetAiProviderKey(aiCtx(), &pb_admin.SetAiProviderKeyRequest{ProviderKey: "fal", Kind: "api", Value: "fal-key-SECRET-1234"})
		return err
	}(), codes.FailedPrecondition)
	require.Equal(t, "AI_KEYS_MASTER_KEY is not set on this server", st.Message())
	require.Zero(t, h.probe.count())
}

// TestAiKeyAdminKindMatchesTheProbeTable — the panel offers a reconciliation key exactly where the
// probe package has an admin-kind probe (openai, anthropic, fal). No request is made: an empty key is
// answered before any, and a pair with no probe says so.
//
// MUTATION IT CATCHES: aiAdminKeySupported and the probe table drifting apart (a field the panel shows
// whose save the probe cannot check, or a cost-API provider with no field).
func TestAiKeyAdminKindMatchesTheProbeTable(t *testing.T) {
	var supported []string
	for _, p := range entity.AIProviderKeys() {
		hasProbe := probe.Probe(context.Background(), p, entity.AIKeyAdmin, "", nil).Code == probe.CodeKeyRejected
		require.Equal(t, hasProbe, aiAdminKeySupported(p), p)
		if hasProbe {
			supported = append(supported, p)
		}
	}
	require.True(t, slices.Equal([]string{"openai", "anthropic", "fal"}, supported), "%v", supported)
}
