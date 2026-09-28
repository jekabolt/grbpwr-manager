package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"slices"
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

// fakeStore is dependency.AI over one in-memory AIConfig. The two reads the registry makes are real,
// and so is the ONE write it may make — SetProviderKey, from the boot import (B-33), recorded in
// keyWrites and applied to the row as the store would; every other write refuses, so a registry
// that started writing elsewhere would fail loudly here.
type fakeStore struct {
	mu             sync.Mutex
	cfg            entity.AIConfig
	getConfigCalls int
	versionCalls   int
	getConfigErr   error
	versionErr     error
	keyWrites      []keyWrite
	keyWriteErr    error
}

// keyWrite is one SetProviderKey call as the fake saw it.
type keyWrite struct {
	provider string
	kind     entity.AIKeyKind
	enc      []byte
	last4    string
	by       string
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
func (f *fakeStore) SetProviderKey(_ context.Context, key string, kind entity.AIKeyKind, enc []byte, last4 string, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyWriteErr != nil {
		return f.keyWriteErr
	}
	f.keyWrites = append(f.keyWrites, keyWrite{provider: key, kind: kind, enc: enc, last4: last4, by: by})
	for i := range f.cfg.Providers {
		if f.cfg.Providers[i].Key != key {
			continue
		}
		if kind == entity.AIKeyAdmin {
			f.cfg.Providers[i].AdminKeyEnc, f.cfg.Providers[i].AdminKeyLast4, f.cfg.Providers[i].AdminKeyUpdatedBy = enc, last4, by
		} else {
			f.cfg.Providers[i].APIKeyEnc, f.cfg.Providers[i].APIKeyLast4, f.cfg.Providers[i].APIKeyUpdatedBy = enc, last4, by
		}
	}
	f.cfg.Settings.ConfigVersion++ // the store bumps the version on every key write
	return nil
}

func (f *fakeStore) writes() []keyWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]keyWrite(nil), f.keyWrites...)
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
func (f *fakeStore) GetUsageSnapshot(context.Context, string) (*entity.AIUsageSnapshot, error) {
	return nil, errNotUsed
}
func (f *fakeStore) PutUsageSnapshot(context.Context, entity.AIUsageSnapshot) error {
	return errNotUsed
}

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

// seedKeys are the api keys seedConfig stores for the four providers that are on — sealed under
// testRing's master, so a registry over testRing opens them and one over another master does not.
// Since B-33 a keyed provider is a provider WITH A STORED KEY: the env values (testEnv) are the
// import's input only, and every test that expects a key to answer expects one of these.
var seedKeys = map[string]string{
	entity.AIProviderOpenRouter: "db-openrouter-1111",
	entity.AIProviderFal:        "db-fal-2222",
	entity.AIProviderMeshy:      "db-meshy-3333",
	entity.AIProviderRecraft:    "db-recraft-4444",
}

// seedConfig is the 0373 seed as B-33 leaves it after the boot import: nine rows (openrouter, fal,
// meshy, recraft on, each with a stored key — seedKeys), every purpose at position 1 as today,
// version 1, both defaults openrouter.
func seedConfig() entity.AIConfig {
	ring, err := keyring.New(masterB64(7)) // testRing's master, without a *testing.T
	if err != nil {
		panic(err)
	}
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		p := entity.AIProvider{Key: k, Label: k, Enabled: seedKeys[k] != ""}
		if plain := seedKeys[k]; plain != "" {
			blob, err := ring.Seal(plain, keyring.AAD(k, string(entity.AIKeyAPI)))
			if err != nil {
				panic(err)
			}
			p.APIKeyEnc, p.APIKeyLast4 = blob, keyring.Last4(plain)
		}
		cfg.Providers = append(cfg.Providers, p)
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

// TestKeyFunc_StoredKeyOnly_EnvNeverAnswers (B-33) — the stored key answers and the panel says "db"
// with its last four; a provider with NO stored key answers "" and says "none" although its env
// variable is set: the env values are the import's input, never a call's key.
//
// MUTATION (measured red → green): the env fallback restored at the end of effectiveKeyIn
// (`return r.envValue(providerKey)` in place of `return p.dbKey` for an empty dbKey) → meshy answers
// testEnv.Meshy again → red.
func TestKeyFunc_StoredKeyOnly_EnvNeverAnswers(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "db-fal-9z9z")
	provider(&cfg, entity.AIProviderMeshy).APIKeyEnc = nil // on, env set, nothing stored

	r, _, logs := newLoaded(t, ring, cfg)

	require.Equal(t, "db-fal-9z9z", r.KeyFunc(entity.AIProviderFal)())
	st := state(t, r, entity.AIProviderFal)
	require.Equal(t, KeySourceDB, st.KeySource)
	require.Equal(t, "9z9z", st.KeyLast4)
	// No stored key: nothing answers, whatever MESHY_API_KEY says.
	require.Equal(t, "", r.KeyFunc(entity.AIProviderMeshy)())
	require.Equal(t, KeySourceNone, state(t, r, entity.AIProviderMeshy).KeySource)
	require.Equal(t, "", state(t, r, entity.AIProviderMeshy).KeyLast4)
	require.Contains(t, logs.String(), "ai provider meshy: enabled=true key=none")
	// Never the value in a log line — source and last4 only.
	require.NotContains(t, logs.String(), "db-fal-9z9z")
	require.NotContains(t, logs.String(), testEnv.Meshy)
	require.Contains(t, logs.String(), "ai provider fal: enabled=true key=db")
}

// TestKeyFunc_DisabledYieldsEmpty — enabled=0 WINS over a stored key (02-PLAN S-2); the panel still
// shows which key the switched-off provider would use.
//
// MUTATION: drop the `!p.enabled` return in effectiveKeyIn → red (the db key comes back).
func TestKeyFunc_DisabledYieldsEmpty(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	fal := provider(&cfg, entity.AIProviderFal)
	fal.Enabled = false
	fal.APIKeyEnc = seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "db-fal-9z9z")
	provider(&cfg, entity.AIProviderMeshy).Enabled = false // its seeded key stays stored

	r, _, _ := newLoaded(t, ring, cfg)

	require.Equal(t, "", r.KeyFunc(entity.AIProviderFal)())
	require.Equal(t, "", r.KeyFunc(entity.AIProviderMeshy)())
	require.False(t, state(t, r, entity.AIProviderFal).Enabled)
	require.Equal(t, "9z9z", state(t, r, entity.AIProviderFal).KeyLast4, "shown as if enabled")
}

// TestKeyFunc_UnreadableAnswersNothing — a blob that does not open (sealed for another row, under
// another master, or with no master at all) is reported "unreadable", NOTHING answers for the
// provider (B-33: the env key is not a fallback any more), the warning says where to re-enter the
// key and is logged ONCE per blob, not on every reload.
//
// MUTATION: return (plain, false) on an Open error in open() → red (KeySource "none").
// MUTATION: drop the `already` check → red (two warnings).
// MUTATION (B-33): effectiveKeyIn answers envValue for an empty dbKey → red (fal answers testEnv.Fal).
func TestKeyFunc_UnreadableAnswersNothing(t *testing.T) {
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
	// Unreadable on a provider that never had an env variable.
	provider(&cfg, entity.AIProviderOpenAI).Enabled = true
	provider(&cfg, entity.AIProviderOpenAI).APIKeyEnc = seal(t, other, entity.AIProviderOpenAI, entity.AIKeyAPI, "sk-openai-3333")

	r, fs, logs := newLoaded(t, ring, cfg)

	require.Equal(t, "", r.KeyFunc(entity.AIProviderFal)(), "FAL_KEY is set and must not answer")
	require.Equal(t, KeySourceUnreadable, state(t, r, entity.AIProviderFal).KeySource)
	require.Equal(t, "", state(t, r, entity.AIProviderFal).KeyLast4, "no key answers: no last4")
	require.Equal(t, "", r.KeyFunc(entity.AIProviderMeshy)())
	require.Equal(t, KeySourceUnreadable, state(t, r, entity.AIProviderMeshy).KeySource)
	require.Equal(t, "", r.KeyFunc(entity.AIProviderOpenAI)())
	require.Equal(t, KeySourceUnreadable, state(t, r, entity.AIProviderOpenAI).KeySource)
	require.Contains(t, logs.String(), "does not open — re-enter it in admin → AI providers")
	require.NotContains(t, logs.String(), "answers meanwhile")
	require.NotContains(t, logs.String(), "fallback=")

	// Reload again (a version bump that touches nothing here): no second warning for the same blobs.
	fs.edit(func(c *entity.AIConfig) {})
	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, 1, strings.Count(logs.String(), "provider=fal kind=api"), logs.String())
	require.Equal(t, 1, strings.Count(logs.String(), "provider=meshy kind=api"), logs.String())
	for _, secret := range []string{"moved-key-1111", "other-master-2222", "sk-openai-3333", testEnv.Fal, testEnv.Meshy} {
		require.NotContains(t, logs.String(), secret)
	}

	// A registry with no master key cannot open anything: stored keys are unreadable, nothing answers.
	cfg2 := seedConfig()
	provider(&cfg2, entity.AIProviderFal).APIKeyEnc = seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "db-fal-9z9z")
	r2, _, _ := newLoaded(t, noMaster, cfg2)
	require.Equal(t, "", r2.KeyFunc(entity.AIProviderFal)())
	require.Equal(t, KeySourceUnreadable, state(t, r2, entity.AIProviderFal).KeySource)
}

// TestKeyFunc_BeforeReloadAndMissingRow — never loaded, or a provider with no row: "" (B-33: with no
// snapshot there is no key, and a missing row is a broken deploy, not a reason to read env).
//
// MUTATION: effectiveKeyIn returns r.envValue(providerKey) for a nil snapshot / a missing row → red.
func TestKeyFunc_BeforeReloadAndMissingRow(t *testing.T) {
	r := New(&fakeStore{cfg: seedConfig()}, testRing(t), testEnv)
	require.Equal(t, "", r.KeyFunc(entity.AIProviderFal)())
	require.Equal(t, "", state(t, r, entity.AIProviderFal).KeyLast4)
	require.Equal(t, KeySourceNone, state(t, r, entity.AIProviderFal).KeySource)
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
	require.Equal(t, "", loaded.KeyFunc(entity.AIProviderMeshy)())
	require.Equal(t, KeySourceNone, state(t, loaded, entity.AIProviderMeshy).KeySource)
	require.Equal(t, uint64(1), loaded.Version())
}

// TestOpenRouterImagesKeyFunc — one row serves both OpenRouter clients: a stored key answers for
// both; with none, BOTH answer "" — OPENROUTER_IMAGES_API_KEY is no fallback any more (B-33);
// enabled=0 silences both.
//
// MUTATION: OpenRouterImagesKeyFunc answers r.env.OpenRouterImages for an empty dbKey → red.
func TestOpenRouterImagesKeyFunc(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderOpenRouter).APIKeyEnc = nil
	r, fs, _ := newLoaded(t, ring, cfg)
	chat, images := r.KeyFunc(entity.AIProviderOpenRouter), r.OpenRouterImagesKeyFunc()
	require.Equal(t, "", chat())
	require.Equal(t, "", images())

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

// ───────────────────────── the env import (B-33) ─────────────────────────

// importRig is a registry over cfg with testEnv as its env, its log captured, NOT yet reloaded — the
// import runs before the boot Reload, and the tests reload afterwards to see what the snapshot says.
func importRig(t *testing.T, ring *keyring.Ring, cfg entity.AIConfig) (*Registry, *fakeStore, *bytes.Buffer) {
	t.Helper()
	fs := &fakeStore{cfg: cfg}
	r := New(fs, ring, testEnv)
	var buf bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&lockedWriter{w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return r, fs, &buf
}

// TestImportEnvKeys_EmptySlotIsFilled — an env value whose panel slot is EMPTY is sealed (the
// handler's AAD, so the registry opens it) and stored with updated_by "env-import" and the right
// last4; one INFO line per import names the VARIABLE to delete and never the value; the next Reload
// says "db" and the key answers — from the store, not from env.
//
// MUTATION (measured red → green): the seal's AAD changed to AAD(provider, "admin") → the blob does
// not open under "api" → KeySource unreadable, KeyFunc "" → red.
func TestImportEnvKeys_EmptySlotIsFilled(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	for _, k := range []string{entity.AIProviderOpenRouter, entity.AIProviderFal, entity.AIProviderMeshy, entity.AIProviderRecraft} {
		provider(&cfg, k).APIKeyEnc = nil
	}
	r, fs, logs := importRig(t, ring, cfg)

	imported, err := r.ImportEnvKeys(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{entity.AIProviderOpenRouter, entity.AIProviderFal, entity.AIProviderMeshy, entity.AIProviderRecraft}, imported)

	want := map[string]string{
		entity.AIProviderOpenRouter: testEnv.OpenRouter, entity.AIProviderFal: testEnv.Fal,
		entity.AIProviderMeshy: testEnv.Meshy, entity.AIProviderRecraft: testEnv.Recraft,
	}
	writes := fs.writes()
	require.Len(t, writes, 4)
	for _, w := range writes {
		require.Equal(t, entity.AIKeyAPI, w.kind)
		require.Equal(t, EnvImportedBy, w.by)
		require.Equal(t, keyring.Last4(want[w.provider]), w.last4)
		plain, err := ring.Open(w.enc, keyring.AAD(w.provider, string(entity.AIKeyAPI)))
		require.NoError(t, err, "sealed with the handler's AAD")
		require.Equal(t, want[w.provider], plain)
	}
	require.Contains(t, logs.String(),
		"ai provider fal: env key imported into the panel (last4 cccc) — delete FAL_KEY from the app spec")
	require.Contains(t, logs.String(), "delete OPENROUTER_API_KEY from the app spec")
	require.Contains(t, logs.String(), "delete MESHY_API_KEY from the app spec")
	require.Contains(t, logs.String(), "delete RECRAFT_API_KEY from the app spec")
	for _, secret := range []string{testEnv.OpenRouter, testEnv.Fal, testEnv.Meshy, testEnv.Recraft, testEnv.OpenRouterImages} {
		require.NotContains(t, logs.String(), secret)
	}
	require.NotContains(t, logs.String(), "level=ERROR")

	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, testEnv.Fal, r.KeyFunc(entity.AIProviderFal)(), "answers now — from the stored blob")
	st := state(t, r, entity.AIProviderFal)
	require.Equal(t, KeySourceDB, st.KeySource)
	require.Equal(t, "cccc", st.KeyLast4)
	require.Equal(t, testEnv.OpenRouter, r.OpenRouterImagesKeyFunc()(), "one row serves both OpenRouter clients")
}

// TestImportEnvKeys_FilledSlotUntouched — a slot that already holds a blob keeps it: the panel's
// key wins over the env value, and no write happens for it. Only the empty slots are filled.
//
// MUTATION (measured red → green): the `len(row.APIKeyEnc) > 0` skip dropped → openrouter's stored
// key is overwritten with testEnv.OpenRouter → red on the first assertion (five writes, not three)
// and on the KeyFunc answer.
func TestImportEnvKeys_FilledSlotUntouched(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig() // openrouter, fal, meshy, recraft all stored
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = nil
	r, fs, _ := importRig(t, ring, cfg)

	imported, err := r.ImportEnvKeys(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{entity.AIProviderFal}, imported)
	require.Len(t, fs.writes(), 1)
	require.Equal(t, entity.AIProviderFal, fs.writes()[0].provider)

	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, seedKeys[entity.AIProviderOpenRouter], r.KeyFunc(entity.AIProviderOpenRouter)(), "the stored key, not OPENROUTER_API_KEY")
	require.Equal(t, testEnv.Fal, r.KeyFunc(entity.AIProviderFal)())
}

// TestImportEnvKeys_BrokenBlobUntouched — a blob that does not open is still "a key is stored": the
// import leaves it (a person re-enters it), the provider stays unreadable and answers "".
//
// MUTATION (measured red → green): the skip made `r.open(...) != ""` (readable blobs only) → the
// broken fal blob is overwritten with FAL_KEY → KeySource db → red.
func TestImportEnvKeys_BrokenBlobUntouched(t *testing.T) {
	ring := testRing(t)
	other, err := keyring.New(masterB64(9))
	require.NoError(t, err)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = seal(t, other, entity.AIProviderFal, entity.AIKeyAPI, "other-master-2222")
	r, fs, _ := importRig(t, ring, cfg)

	imported, err := r.ImportEnvKeys(context.Background())
	require.NoError(t, err)
	require.Empty(t, imported)
	require.Empty(t, fs.writes())

	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, KeySourceUnreadable, state(t, r, entity.AIProviderFal).KeySource)
	require.Equal(t, "", r.KeyFunc(entity.AIProviderFal)())
}

// TestImportEnvKeys_NoMasterKey — without AI_KEYS_MASTER_KEY nothing can be sealed: one ERROR line
// says so, nothing is stored, and the env values are not used either — the provider answers "".
//
// MUTATION (measured red → green): the `!r.ring.Enabled()` early return dropped → SealProviderKey
// fails with ErrNoMasterKey → ImportEnvKeys returns an error → red.
func TestImportEnvKeys_NoMasterKey(t *testing.T) {
	noMaster, err := keyring.New("")
	require.NoError(t, err)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = nil
	r, fs, logs := importRig(t, noMaster, cfg)

	imported, err := r.ImportEnvKeys(context.Background())
	require.NoError(t, err)
	require.Empty(t, imported)
	require.Empty(t, fs.writes())
	require.Contains(t, logs.String(), "level=ERROR")
	require.Contains(t, logs.String(),
		"AI_KEYS_MASTER_KEY is not set: env keys are no longer read; set it and save the keys in admin → AI providers")

	require.NoError(t, r.Reload(context.Background()))
	require.Equal(t, "", r.KeyFunc(entity.AIProviderFal)(), "FAL_KEY is set and is not used")
	require.Equal(t, KeySourceNone, state(t, r, entity.AIProviderFal).KeySource)
}

// TestImportEnvKeys_ImagesEnvDiffersWarns — OPENROUTER_IMAGES_API_KEY is never imported (one
// openrouter row serves both clients); when it differs from OPENROUTER_API_KEY one WARN names the
// variable and not the value; when it is the same (config's own fallback) there is no line.
//
// MUTATION (measured red → green): the WARN's condition inverted (`==`) → the differing case logs
// nothing → red.
func TestImportEnvKeys_ImagesEnvDiffersWarns(t *testing.T) {
	ring := testRing(t)
	r, fs, logs := importRig(t, ring, seedConfig()) // every slot filled: nothing to import
	_, err := r.ImportEnvKeys(context.Background())
	require.NoError(t, err)
	require.Empty(t, fs.writes())
	require.Contains(t, logs.String(), "level=WARN")
	require.Contains(t, logs.String(), "OPENROUTER_IMAGES_API_KEY differs from OPENROUTER_API_KEY and is not imported")
	require.NotContains(t, logs.String(), testEnv.OpenRouterImages)

	same := testEnv
	same.OpenRouterImages = same.OpenRouter
	r2 := New(&fakeStore{cfg: seedConfig()}, ring, same)
	var buf bytes.Buffer
	r2.log = slog.New(slog.NewTextHandler(&buf, nil))
	_, err = r2.ImportEnvKeys(context.Background())
	require.NoError(t, err)
	require.NotContains(t, buf.String(), "OPENROUTER_IMAGES_API_KEY")
}

// TestImportEnvKeys_StoreFailureIsReturned — a store that cannot read or write is a boot error for
// the import exactly as it is for the Reload; the import never swallows it.
func TestImportEnvKeys_StoreFailureIsReturned(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderFal).APIKeyEnc = nil
	r, fs, _ := importRig(t, ring, cfg)
	fs.mu.Lock()
	fs.keyWriteErr = errors.New("db down")
	fs.mu.Unlock()
	_, err := r.ImportEnvKeys(context.Background())
	require.ErrorContains(t, err, "env import: store fal key: db down")

	fs.mu.Lock()
	fs.keyWriteErr, fs.getConfigErr = nil, errors.New("db down")
	fs.mu.Unlock()
	_, err = r.ImportEnvKeys(context.Background())
	require.ErrorContains(t, err, "env import: db down")
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

// TestBreakerHeld_ListsOnlyWhatTheOpenBreakerTookOut — a door that finds no candidate asks this to
// tell «paused after repeated failures» from «not configured»: while the breaker is open the provider
// is held (and only it — a disabled or keyless provider is not), past the window it is listed again
// and held no more.
//
// MUTATION: walk appends a breaker-open candidate to neither list → red (held empty while open).
// MUTATION: walk puts a disabled provider's candidate in held → red (fal listed as held).
func TestBreakerHeld_ListsOnlyWhatTheOpenBreakerTookOut(t *testing.T) {
	clk := newFakeClock()
	cfg := seedConfig()
	setRoute(&cfg, entity.AIPurposeTechCardEnhance,
		entity.AIRouteCandidate{Position: 1, ProviderKey: entity.AIProviderOpenRouter, Model: "a/b"},
		entity.AIRouteCandidate{Position: 2, ProviderKey: entity.AIProviderOpenAI, Model: "gpt"}, // disabled in the seed
	)
	r, _, _ := newLoaded(t, testRing(t), cfg, WithClock(clk.now))
	require.Empty(t, r.BreakerHeld(entity.AIPurposeTechCardEnhance), "nothing is held while every breaker is closed")

	transient := &aiprov.CallError{Provider: entity.AIProviderOpenRouter, HTTPStatus: 503, Retryable: true}
	for range 3 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transient)
	}
	require.Empty(t, r.Candidates(entity.AIPurposeTechCardEnhance))
	require.Equal(t, []Candidate{{ProviderKey: entity.AIProviderOpenRouter, Model: "a/b", Position: 1}},
		r.BreakerHeld(entity.AIPurposeTechCardEnhance), "held: the open breaker, not the disabled provider")

	clk.advance(breakerConfig.OpenTimeout + time.Second)
	require.Len(t, r.Candidates(entity.AIPurposeTechCardEnhance), 1)
	require.Empty(t, r.BreakerHeld(entity.AIPurposeTechCardEnhance), "past the window it is listed, not held")
	require.Nil(t, New(&fakeStore{}, testRing(t), testEnv).BreakerHeld(entity.AIPurposeTechCardEnhance), "nil before the first Reload")
}

// TestCandidatesAt_OneSnapshotForTheListAndItsVersion — the list and the version come from ONE load:
// a reload that lands while the route is being walked (here: inside the walk's own clock read) does
// not stamp the old list with the new version. The next call reads the new snapshot, both halves.
//
// MUTATION: CandidatesAt returns `listed, r.Version()` (a second read) → red (old list, version 2).
func TestCandidatesAt_OneSnapshotForTheListAndItsVersion(t *testing.T) {
	clk := newFakeClock()
	cfg := seedConfig()
	setRoute(&cfg, entity.AIPurposeTechCardEnhance,
		entity.AIRouteCandidate{Position: 1, ProviderKey: entity.AIProviderOpenRouter, Model: "old/slug"})
	var (
		r      *Registry
		fs     *fakeStore
		reload atomic.Bool
	)
	hooked := func() time.Time {
		if reload.CompareAndSwap(true, false) {
			fs.edit(func(c *entity.AIConfig) {
				setRoute(c, entity.AIPurposeTechCardEnhance,
					entity.AIRouteCandidate{Position: 1, ProviderKey: entity.AIProviderOpenRouter, Model: "new/slug"})
			})
			require.NoError(t, r.Reload(context.Background()))
		}
		return clk.now()
	}
	r, fs, _ = newLoaded(t, testRing(t), cfg, WithClock(hooked))
	require.Equal(t, uint64(1), r.Version())

	reload.Store(true)
	got, v := r.CandidatesAt(entity.AIPurposeTechCardEnhance)
	require.Equal(t, uint64(2), r.Version(), "the reload did land during the walk")
	require.Equal(t, "old/slug", got[0].Model)
	require.Equal(t, uint64(1), v, "the version is the one the list was read from, not the one that landed meanwhile")

	got, v = r.CandidatesAt(entity.AIPurposeTechCardEnhance)
	require.Equal(t, "new/slug", got[0].Model)
	require.Equal(t, uint64(2), v)
	require.Equal(t, got, r.Candidates(entity.AIPurposeTechCardEnhance))
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
	require.Equal(t, seedKeys[entity.AIProviderFal], falKey())

	blob := seal(t, ring, entity.AIProviderFal, entity.AIKeyAPI, "rotated-fal-8888")
	fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderFal).APIKeyEnc = blob })
	require.Equal(t, seedKeys[entity.AIProviderFal], falKey(), "nothing changes until the reload")
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

// TestProvidersAt_OneSnapshot (Codex B #7) — the states and the version ProvidersAt returns describe
// the SAME snapshot, even when a Reload swaps the snapshot while the states are being rendered (here
// from inside the breaker clock, which rendering reads once per provider); and the pair returned is a
// value — the swap after it changes neither.
//
// MUTATION IT CATCHES: the version read by a second load after the states (`return out, r.Version()`,
// i.e. Providers() + Version() side by side) — the old key state paired with the new number.
func TestProvidersAt_OneSnapshot(t *testing.T) {
	ring := testRing(t)
	cfg := seedConfig()
	provider(&cfg, entity.AIProviderOpenAI).APIKeyEnc = seal(t, ring, entity.AIProviderOpenAI, entity.AIKeyAPI, "db-openai-1111")
	var (
		armed atomic.Bool
		r     *Registry
		fs    *fakeStore
	)
	clk := newFakeClock()
	r, fs, _ = newLoaded(t, ring, cfg, WithClock(func() time.Time {
		if armed.CompareAndSwap(true, false) {
			// another writer clears the stored key and this instance reloads, mid-render
			fs.edit(func(c *entity.AIConfig) { provider(c, entity.AIProviderOpenAI).APIKeyEnc = nil })
			require.NoError(t, r.Reload(context.Background()))
		}
		return clk.now()
	}))
	openaiOf := func(states []ProviderState) ProviderState {
		for _, st := range states {
			if st.Key == entity.AIProviderOpenAI {
				return st
			}
		}
		t.Fatal("no openai state")
		return ProviderState{}
	}

	armed.Store(true)
	states, v := r.ProvidersAt()
	require.False(t, armed.Load(), "the swap must have happened inside the call")
	require.Equal(t, uint64(1), v, "the version of the snapshot the states were rendered from")
	require.Equal(t, KeySourceDB, openaiOf(states).KeySource)
	require.Equal(t, "1111", openaiOf(states).KeyLast4)

	// The registry has moved on; the pair already returned has not.
	require.Equal(t, uint64(2), r.Version())
	require.Equal(t, uint64(1), v)
	require.Equal(t, KeySourceDB, openaiOf(states).KeySource)
	states, v = r.ProvidersAt()
	require.Equal(t, uint64(2), v)
	require.Equal(t, KeySourceNone, openaiOf(states).KeySource)
	require.Empty(t, openaiOf(states).KeyLast4)
	require.Equal(t, states, r.Providers(), "Providers is ProvidersAt's states")

	// Before the first Reload: version 0 and no key anywhere (B-33: env is not a picture any more).
	states, v = New(&fakeStore{cfg: seedConfig()}, ring, testEnv).ProvidersAt()
	require.Zero(t, v)
	require.Len(t, states, len(entity.AIProviderKeys()))
	require.Equal(t, KeySourceNone, states[slices.Index(entity.AIProviderKeys(), entity.AIProviderFal)].KeySource)
	require.Empty(t, states[slices.Index(entity.AIProviderKeys(), entity.AIProviderFal)].KeyLast4)
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

// TestRouteHeadAt_IsTheConfiguredHeadBeforeKeyAndBreaker (FIX-G3) — the head is the first row as
// saved: a disabled/keyless provider and a provider held by its breaker are still the head (Candidates
// drops both); "" is the capability's default provider; a row whose provider cannot serve the purpose
// is not a head; the version is the snapshot's own.
//
// MUTATION (measured red): RouteHeadAt reads the filtered list (walk's listed[0]) → the keyless head
// is lost.
func TestRouteHeadAt_IsTheConfiguredHeadBeforeKeyAndBreaker(t *testing.T) {
	clk := newFakeClock()
	cfg := seedConfig()
	setRoute(&cfg, entity.AIPurposeTechCardAnalysis,
		entity.AIRouteCandidate{Position: 1, ProviderKey: entity.AIProviderFal, Model: "not/chat"}, // cannot serve chat
		entity.AIRouteCandidate{Position: 2, ProviderKey: entity.AIProviderOpenAI, Model: "gpt"},   // disabled in the seed
		entity.AIRouteCandidate{Position: 3, ProviderKey: "", Model: ""},                           // the default: openrouter
	)
	setRoute(&cfg, entity.AIPurposeNoteMarkdown, entity.AIRouteCandidate{Position: 1, ProviderKey: "", Model: "x/y"})
	r, _, _ := newLoaded(t, testRing(t), cfg, WithClock(clk.now))

	require.Equal(t, []Candidate{{ProviderKey: entity.AIProviderOpenRouter, Position: 3}},
		r.Candidates(entity.AIPurposeTechCardAnalysis), "precondition: the filtered list starts at the default")
	head, version, ok := r.RouteHeadAt(entity.AIPurposeTechCardAnalysis)
	require.True(t, ok)
	require.Equal(t, Candidate{ProviderKey: entity.AIProviderOpenAI, Model: "gpt", Position: 2}, head,
		"the configured head, keyless or not")
	require.Equal(t, r.Version(), version)

	head, _, ok = r.RouteHeadAt(entity.AIPurposeNoteMarkdown)
	require.True(t, ok)
	require.Equal(t, Candidate{ProviderKey: entity.AIProviderOpenRouter, Model: "x/y", Position: 1}, head, `"" is the default provider`)

	transient := &aiprov.CallError{Provider: entity.AIProviderOpenRouter, HTTPStatus: 503, Retryable: true}
	for range 3 {
		call(t, r, entity.AIProviderOpenRouter, entity.AICapabilityChat, transient)
	}
	require.Empty(t, r.Candidates(entity.AIPurposeNoteMarkdown), "precondition: the breaker holds it")
	head, _, ok = r.RouteHeadAt(entity.AIPurposeNoteMarkdown)
	require.True(t, ok)
	require.Equal(t, entity.AIProviderOpenRouter, head.ProviderKey, "a held provider is still the head")

	_, _, ok = r.RouteHeadAt("chat.nothing")
	require.False(t, ok, "an unknown purpose has no head")
	_, _, ok = New(&fakeStore{}, testRing(t), testEnv).RouteHeadAt(entity.AIPurposeNoteMarkdown)
	require.False(t, ok, "no head before the first Reload")
}
