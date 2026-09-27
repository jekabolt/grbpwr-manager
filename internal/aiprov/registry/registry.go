// Package registry is the AI providers' live configuration: which provider is enabled, which key
// answers for it, which candidates serve a purpose, and whether a provider is currently breaking.
//
// ONE SNAPSHOT, SWAPPED WHOLE. Reload reads the five config tables (dependency.AI.GetConfig), opens
// the sealed keys with the key ring, and swaps an atomic pointer. Every reader — KeyFunc closures,
// Candidates, Providers — reads the snapshot that is current AT THE MOMENT IT ASKS, so a request
// already on the wire keeps the header it was built with and the next one uses the new key; nobody
// ever sees half a reload.
//
// THE KEY RULE (02-PLAN S-2): the effective key is the database key when one is stored and opens,
// else today's env variable; a provider row with enabled=0 WINS over both — "" is returned, and a
// client whose KeyFunc answers "" reports itself disabled. An env key is never copied into the
// database. A stored key that does not open (wrong master, row swapped, no master at all) never
// stops the registry: the provider is reported as KeySource "unreadable", the env key answers
// meanwhile, and one warning is logged per unreadable blob.
//
// HOT RELOAD ACROSS INSTANCES. The instance that handles a write RPC calls Reload itself; every
// other instance learns from the poller, which compares ai_settings.config_version every 60 s and
// reloads only when it moved.
//
// BREAKERS (breaker.go). A caller's contract, in order: Candidates → Admit(the picked candidate) →
// the physical call → exactly one of RecordSuccess / RecordFailure / Release, WITH THE ADMISSION
// Admit handed out — an end acts only on the admission it completes.
//
// This package imports no client package. The clients (openrouter, orimages, fal, meshy, recraft)
// get a plain `KeyFunc func() string` in their Config and never import aiprov either.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// Name is the worker's name in the health registry and in panic logs.
const Name = "ai-registry"

// DefaultPollInterval — how often the poller asks for ai_settings.config_version.
const DefaultPollInterval = 60 * time.Second

// pollTimeout bounds one poll (the version read and, when it moved, the reload), so a stuck query
// can neither block the loop nor stall a graceful shutdown.
const pollTimeout = 15 * time.Second

// backoffCapFactor — consecutive failed polls back off pollInterval × 2^(n-1), capped at this many
// intervals (60 s → 1, 2, 4, 8, 10 min). The cap is far below fxsync's 30 min on purpose: this loop
// is how a key rotated on another instance reaches this one, and a DB blip must not park that for
// half an hour.
const backoffCapFactor = 10

// Key sources — ProviderState.KeySource.
const (
	KeySourceDB         = "db"         // a stored key opened with the master key
	KeySourceEnv        = "env"        // no stored key; today's env variable answers
	KeySourceNone       = "none"       // neither
	KeySourceUnreadable = "unreadable" // a stored key that does not open; the env key (if any) answers meanwhile
)

// Breaker states — ProviderState.Breaker and BreakerState (breaker.go), in the circuitbreaker
// package's own words.
const (
	BreakerClosed   = "closed"
	BreakerOpen     = "open"
	BreakerHalfOpen = "half-open"
)

// EnvKeys are today's env values — the fallback when the database holds no key for a provider.
//
// OpenRouterImages is the image client's own env key (OPENROUTER_IMAGES_API_KEY, which config/cfg.go
// already falls back to OPENROUTER_API_KEY). ONE provider row serves both OpenRouter clients, so a
// stored openrouter key answers for both; the images env value is consulted only when there is no
// stored key — which keeps a deployment whose two env values differ exactly as it is today.
type EnvKeys struct {
	OpenRouter, OpenRouterImages, Fal, Meshy, Recraft string
}

// Candidate is one provider a purpose may be served by, in route order.
type Candidate struct {
	ProviderKey string
	Model       string // "" = the client's own default
	Position    int
}

// ProviderState is one provider as the panel shows it. Never carries a key.
type ProviderState struct {
	Key         string
	Enabled     bool
	KeySource   string // KeySourceDB | KeySourceEnv | KeySourceNone | KeySourceUnreadable
	KeyLast4    string // last four characters of the key that ANSWERS (env on "unreadable"); "" when none
	AdminKeySet bool   // a reconciliation key is stored and opens
	Breaker     string // the worst of the provider's breakers: open > half-open > closed
}

// Option configures a Registry.
type Option func(*Registry)

// WithPollInterval sets the poller's period; <= 0 keeps DefaultPollInterval.
func WithPollInterval(d time.Duration) Option {
	return func(r *Registry) {
		if d > 0 {
			r.poll = d
		}
	}
}

// WithClock replaces time.Now as the breakers' one clock (breaker.go). Tests only.
func WithClock(f func() time.Time) Option {
	return func(r *Registry) {
		if f != nil {
			r.clock = f
		}
	}
}

// providerSnap is one provider inside a snapshot. The keys are plaintext and live in memory only.
type providerSnap struct {
	enabled   bool
	dbKey     string // the opened stored api key; "" when none or unreadable
	keySource string
	adminKey  string // the opened stored admin key; "" when none or unreadable
}

// snapshot is the whole configuration as of one config_version. Immutable once stored.
type snapshot struct {
	version        uint64
	providers      map[string]providerSnap
	routes         map[string][]entity.AIRouteCandidate // by purpose, ordered by Position
	defaultChat    string
	defaultImage   string
	budgetTimezone string
}

// Registry — see the package doc.
type Registry struct {
	store dependency.AI
	ring  *keyring.Ring
	env   EnvKeys
	log   *slog.Logger

	snap     atomic.Pointer[snapshot]
	reloadMu sync.Mutex // serialises Reload: two reloads finishing out of order must not swap an older snapshot over a newer one

	bmu      sync.Mutex
	breakers map[string]*probeBreaker // key = providerKey + "/" + capability

	warnMu sync.Mutex
	warned map[string]string // providerKey:kind → fingerprint of the unreadable blob already warned about

	poll    time.Duration
	clock   func() time.Time
	tracker health.Tracker

	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// New builds a registry. Nothing is read until Reload; before it, every KeyFunc answers the env key
// (today's behaviour) and Candidates answers nothing.
func New(store dependency.AI, ring *keyring.Ring, env EnvKeys, opts ...Option) *Registry {
	r := &Registry{
		store:    store,
		ring:     ring,
		env:      trimEnv(env),
		log:      slog.Default(),
		breakers: map[string]*probeBreaker{},
		warned:   map[string]string{},
		poll:     DefaultPollInterval,
		clock:    time.Now,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

func trimEnv(e EnvKeys) EnvKeys {
	return EnvKeys{
		OpenRouter:       strings.TrimSpace(e.OpenRouter),
		OpenRouterImages: strings.TrimSpace(e.OpenRouterImages),
		Fal:              strings.TrimSpace(e.Fal),
		Meshy:            strings.TrimSpace(e.Meshy),
		Recraft:          strings.TrimSpace(e.Recraft),
	}
}

// ───────────────────────── reload ─────────────────────────

// Reload reads the configuration, opens the stored keys, and swaps the snapshot. On error the
// previous snapshot stays in force. Providers whose effective key changed get their breakers reset
// (a rotated key is a new chance), and every provider whose visible state changed gets one log line
// — at boot that is one line per provider. Keys are never logged; source and last4 are. A reload is
// a health tick of its own: the boot reload makes the worker fresh on /statusz before its first poll.
func (r *Registry) Reload(ctx context.Context) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	cfg, err := r.store.GetConfig(ctx)
	if err == nil && cfg == nil {
		err = errors.New("the store returned no configuration")
	}
	if err != nil {
		err = fmt.Errorf("ai registry: reload: %w", err)
		r.tracker.MarkError(err)
		return err
	}
	next := r.build(ctx, cfg)
	prev := r.snap.Swap(next)

	for _, key := range entity.AIProviderKeys() {
		before, after := r.stateIn(prev, key), r.stateIn(next, key)
		if prev != nil && r.effectiveKeyIn(prev, key, "") != r.effectiveKeyIn(next, key, "") {
			r.ResetBreakers(key)
		}
		if prev == nil || before != after {
			r.log.InfoContext(ctx, fmt.Sprintf("ai provider %s: enabled=%t key=%s", key, after.Enabled, after.KeySource),
				slog.String("provider", key),
				slog.Bool("enabled", after.Enabled),
				slog.String("key_source", after.KeySource),
				slog.String("key_last4", after.KeyLast4),
				slog.Bool("admin_key_set", after.AdminKeySet),
				slog.Uint64("config_version", next.version),
			)
		}
	}
	r.tracker.MarkSuccess()
	return nil
}

func (r *Registry) build(ctx context.Context, cfg *entity.AIConfig) *snapshot {
	s := &snapshot{
		version:        cfg.Settings.ConfigVersion,
		providers:      make(map[string]providerSnap, len(cfg.Providers)),
		routes:         make(map[string][]entity.AIRouteCandidate, len(cfg.Routes)),
		defaultChat:    strings.TrimSpace(cfg.Settings.DefaultChatProviderKey),
		defaultImage:   strings.TrimSpace(cfg.Settings.DefaultImageProviderKey),
		budgetTimezone: cfg.BudgetTimezone,
	}
	for _, p := range cfg.Providers {
		ps := providerSnap{enabled: p.Enabled}
		dbKey, unreadable := r.open(ctx, p.Key, entity.AIKeyAPI, p.APIKeyEnc)
		switch {
		case dbKey != "":
			ps.dbKey, ps.keySource = dbKey, KeySourceDB
		case unreadable:
			ps.keySource = KeySourceUnreadable
		case r.envKey(p.Key, "") != "":
			ps.keySource = KeySourceEnv
		default:
			ps.keySource = KeySourceNone
		}
		ps.adminKey, _ = r.open(ctx, p.Key, entity.AIKeyAdmin, p.AdminKeyEnc)
		s.providers[p.Key] = ps
	}
	for _, rt := range cfg.Routes {
		cands := append([]entity.AIRouteCandidate(nil), rt.Candidates...)
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].Position < cands[j].Position })
		for i := range cands {
			cands[i].ProviderKey = strings.TrimSpace(cands[i].ProviderKey)
			cands[i].Model = strings.TrimSpace(cands[i].Model)
		}
		s.routes[rt.Purpose] = cands
	}
	return s
}

// open decrypts one stored key. An empty slot is ("", false); a blob that does not open is
// ("", true) and is warned about once per blob (the fingerprint, never the bytes, is remembered).
func (r *Registry) open(ctx context.Context, providerKey string, kind entity.AIKeyKind, blob []byte) (string, bool) {
	slot := providerKey + ":" + string(kind)
	if len(blob) == 0 {
		r.forgetWarning(slot)
		return "", false
	}
	plain, err := r.ring.Open(blob, keyring.AAD(providerKey, string(kind)))
	if err == nil {
		r.forgetWarning(slot)
		// A sealed empty string is not a key; it reads as "no stored key", not as unreadable.
		return strings.TrimSpace(plain), false
	}
	sum := sha256.Sum256(blob)
	fp := hex.EncodeToString(sum[:8])
	r.warnMu.Lock()
	already := r.warned[slot] == fp
	r.warned[slot] = fp
	r.warnMu.Unlock()
	if !already {
		fallback := KeySourceNone
		if kind == entity.AIKeyAPI && r.envKey(providerKey, "") != "" {
			fallback = KeySourceEnv
		}
		// err names the AAD ("fal:api") or the missing master; it carries no byte of the blob.
		r.log.WarnContext(ctx, "ai provider key is stored but does not open — re-enter it; the env key answers meanwhile",
			slog.String("provider", providerKey),
			slog.String("kind", string(kind)),
			slog.String("fallback", fallback),
			slog.String("err", err.Error()),
		)
	}
	return "", true
}

func (r *Registry) forgetWarning(slot string) {
	r.warnMu.Lock()
	delete(r.warned, slot)
	r.warnMu.Unlock()
}

// envKey is today's env value for a provider. capability picks the OpenRouter image client's own
// variable; every other provider has one.
func (r *Registry) envKey(providerKey, capability string) string {
	switch providerKey {
	case entity.AIProviderOpenRouter:
		if capability == entity.AICapabilityImage {
			return r.env.OpenRouterImages
		}
		return r.env.OpenRouter
	case entity.AIProviderFal:
		return r.env.Fal
	case entity.AIProviderMeshy:
		return r.env.Meshy
	case entity.AIProviderRecraft:
		return r.env.Recraft
	}
	return ""
}

// effectiveKeyIn is THE key rule, on one snapshot:
//   - no snapshot yet (never reloaded) → the env key, exactly today's behaviour;
//   - a provider with no row → the env key (the migration seeds all nine rows; a missing one is not
//     an enabled=0 and must not silently switch a working feature off);
//   - enabled=0 → "" — it WINS over the stored key and over env;
//   - else the stored key when it opened, else env.
func (r *Registry) effectiveKeyIn(s *snapshot, providerKey, capability string) string {
	if s == nil {
		return r.envKey(providerKey, capability)
	}
	p, ok := s.providers[providerKey]
	if !ok {
		return r.envKey(providerKey, capability)
	}
	if !p.enabled {
		return ""
	}
	if p.dbKey != "" {
		return p.dbKey
	}
	return r.envKey(providerKey, capability)
}

// stateIn renders a provider's ProviderState from one snapshot, the breaker column aside.
func (r *Registry) stateIn(s *snapshot, providerKey string) ProviderState {
	st := ProviderState{Key: providerKey, Enabled: true}
	var p providerSnap
	var ok bool
	if s != nil {
		p, ok = s.providers[providerKey]
	}
	switch {
	case ok:
		st.Enabled = p.enabled
		st.KeySource = p.keySource
		st.AdminKeySet = p.adminKey != ""
	case r.envKey(providerKey, "") != "":
		st.KeySource = KeySourceEnv
	default:
		st.KeySource = KeySourceNone
	}
	// The last four of the key that ANSWERS when the provider is on — computed as if enabled, so a
	// switched-off provider still shows which key it would use.
	answering := r.envKey(providerKey, "")
	if ok && p.dbKey != "" {
		answering = p.dbKey
	}
	st.KeyLast4 = keyring.Last4(answering)
	return st
}

// ───────────────────────── reads ─────────────────────────

// Version is the config_version of the current snapshot; 0 before the first Reload.
func (r *Registry) Version() uint64 {
	if s := r.snap.Load(); s != nil {
		return s.version
	}
	return 0
}

// BudgetTimezone is design_settings.budget_timezone as of the current snapshot; "" before the first
// Reload.
func (r *Registry) BudgetTimezone() string {
	if s := r.snap.Load(); s != nil {
		return s.budgetTimezone
	}
	return ""
}

// KeyFunc returns the hook a client's Config.KeyFunc takes. It reads the CURRENT snapshot on every
// call: "" when the provider is disabled (enabled=0 wins over env), else the stored key, else the
// env key. Capture it once at wiring time; a Reload changes what it answers, not the func.
func (r *Registry) KeyFunc(providerKey string) func() string {
	return func() string { return r.effectiveKeyIn(r.snap.Load(), providerKey, "") }
}

// OpenRouterImagesKeyFunc is KeyFunc("openrouter") for the OpenRouter IMAGE client: the same row,
// the same stored key and the same enabled switch, but with OPENROUTER_IMAGES_API_KEY (already
// falling back to OPENROUTER_API_KEY in config) as its env fallback — so a deployment whose two env
// values differ keeps paying for pictures with the key it pays with today.
func (r *Registry) OpenRouterImagesKeyFunc() func() string {
	return func() string {
		return r.effectiveKeyIn(r.snap.Load(), entity.AIProviderOpenRouter, entity.AICapabilityImage)
	}
}

// AdminKey is the stored reconciliation key of a provider ("" when none or unreadable). It is NOT
// gated by enabled: switching a provider off stops new spend, not the need to reconcile the spend
// already made. There is no env fallback — no admin key ever lived in env.
func (r *Registry) AdminKey(providerKey string) string {
	if s := r.snap.Load(); s != nil {
		return s.providers[providerKey].adminKey
	}
	return ""
}

// Candidates lists the providers that may serve purpose right now, in route order: a "" provider is
// the capability's default provider (Settings.DefaultChat/ImageProviderKey, falling back to
// openrouter; the other capabilities have no default and such a row is skipped); a provider that
// cannot serve the purpose's capability, is disabled, has no effective key, or whose breaker for
// that capability is open (inside its window) is skipped; an exact (provider, model) repeat is
// dropped. A candidate's Model "" passes through — the client's own default. Nil before the first
// Reload.
//
// A HALF-OPEN PROVIDER IS LISTED, NOT ADMITTED. Listing reserves nothing: the caller asks Admit right
// before the physical call to the candidate it picked, and only one caller gets the probe.
func (r *Registry) Candidates(purpose string) []Candidate {
	s := r.snap.Load()
	if s == nil {
		return nil
	}
	capability := entity.AIPurposeCapability(purpose)
	if capability == "" {
		return nil
	}
	now := r.clock()
	var out []Candidate
	seen := map[string]bool{}
	for _, c := range s.routes[purpose] {
		pk := c.ProviderKey
		if pk == "" {
			pk = s.defaultProvider(capability)
		}
		if pk == "" || !entity.AIProviderServes(pk, capability) {
			continue
		}
		if r.effectiveKeyIn(s, pk, capability) == "" { // disabled or keyless
			continue
		}
		if b := r.lookupBreaker(pk, capability); b != nil && b.State(now) == BreakerOpen {
			continue
		}
		dup := pk + "\x00" + c.Model
		if seen[dup] {
			continue
		}
		seen[dup] = true
		out = append(out, Candidate{ProviderKey: pk, Model: c.Model, Position: c.Position})
	}
	return out
}

func (s *snapshot) defaultProvider(capability string) string {
	pick := func(k string) string {
		if k != "" {
			return k
		}
		return entity.AIProviderOpenRouter
	}
	switch capability {
	case entity.AICapabilityChat:
		return pick(s.defaultChat)
	case entity.AICapabilityImage:
		return pick(s.defaultImage)
	}
	return ""
}

// DefaultImageSlug is the model of image.generate's first route row, "" when that row names none
// (the env/client default stays the caller's fallback). "" before the first Reload.
func (r *Registry) DefaultImageSlug() string {
	s := r.snap.Load()
	if s == nil {
		return ""
	}
	if cands := s.routes[entity.AIPurposeImageGenerate]; len(cands) > 0 {
		return cands[0].Model
	}
	return ""
}

// Providers is every known provider in the panel's order, as of the current snapshot.
func (r *Registry) Providers() []ProviderState {
	states, _ := r.ProvidersAt()
	return states
}

// ProvidersAt is Providers together with the config_version of the snapshot those states were rendered
// from — both from ONE snapshot load (Codex B #7). Version() and Providers() called one after the other
// load the snapshot twice, and a Reload between the two pairs one version's number with another
// version's key state; a caller that joins the states with store rows of a known version (the panel)
// needs the version the states really describe. 0 before the first Reload. The breaker column is live,
// not part of any snapshot.
func (r *Registry) ProvidersAt() ([]ProviderState, uint64) {
	s := r.snap.Load()
	keys := entity.AIProviderKeys()
	out := make([]ProviderState, 0, len(keys))
	for _, k := range keys {
		st := r.stateIn(s, k)
		st.Breaker = r.breakerState(k)
		out = append(out, st)
	}
	var version uint64
	if s != nil {
		version = s.version
	}
	return out, version
}

// ───────────────────────── breakers ─────────────────────────
//
// One probeBreaker (breaker.go) per (provider, capability), created by the first transient fault. The
// caller's side of the contract, in order: Candidates → Admit(the picked candidate), which hands out
// an Admission → the physical call → exactly one of RecordSuccess / RecordFailure — or Release when
// it was admitted and did not call after all — each WITH THAT ADMISSION: an end only acts on the
// admission it completes (a straggler from before an opening or a reset is ignored). Every instant
// comes from r.clock(), the one clock.

func breakerKey(providerKey, capability string) string { return providerKey + "/" + capability }

// breakerFor returns the breaker of (provider, capability), creating it on first use. Only a fault
// creates one: a provider that never failed has no entry and is closed by definition.
func (r *Registry) breakerFor(providerKey, capability string) *probeBreaker {
	key := breakerKey(providerKey, capability)
	r.bmu.Lock()
	defer r.bmu.Unlock()
	if b, ok := r.breakers[key]; ok {
		return b
	}
	b := &probeBreaker{state: BreakerClosed}
	r.breakers[key] = b
	return b
}

func (r *Registry) lookupBreaker(providerKey, capability string) *probeBreaker {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	return r.breakers[breakerKey(providerKey, capability)]
}

// Admit asks for the physical call to (provider, capability) right now, right before making it: true
// = go; false = its breaker is open, or half-open with its one probe already handed out — try the
// next candidate. A provider with no breaker is admitted with the zero Admission (and none is
// created); breaker.go says why a fault made with it still counts. An admitted caller MUST end with
// RecordSuccess, RecordFailure or Release, passing back the Admission it got here: a half-open
// breaker stays reserved until its probe does.
func (r *Registry) Admit(providerKey, capability string) (Admission, bool) {
	b := r.lookupBreaker(providerKey, capability)
	if b == nil {
		return Admission{}, true
	}
	return b.Admit(r.clock())
}

// Release ends admission a without a verdict: the caller was admitted and made no call, or its call
// ended in something RecordFailure would not count anyway. A half-open breaker frees its probe when
// a is the admission holding it.
func (r *Registry) Release(providerKey, capability string, a Admission) {
	if b := r.lookupBreaker(providerKey, capability); b != nil {
		b.Release(a)
	}
}

// BreakerState is the state of (provider, capability) at the registry clock: closed | open |
// half-open. It creates no breaker.
func (r *Registry) BreakerState(providerKey, capability string) string {
	b := r.lookupBreaker(providerKey, capability)
	if b == nil {
		return BreakerClosed
	}
	return b.State(r.clock())
}

// breakerState is the worst of a provider's breakers.
func (r *Registry) breakerState(providerKey string) string {
	now := r.clock()
	prefix := providerKey + "/"
	r.bmu.Lock()
	entries := make([]*probeBreaker, 0, 2)
	for k, b := range r.breakers {
		if strings.HasPrefix(k, prefix) {
			entries = append(entries, b)
		}
	}
	r.bmu.Unlock()
	worst := BreakerClosed
	for _, b := range entries {
		switch b.State(now) {
		case BreakerOpen:
			return BreakerOpen
		case BreakerHalfOpen:
			worst = BreakerHalfOpen
		}
	}
	return worst
}

// RecordFailure ends an admitted call that failed. It COUNTS ONLY a transient fault nobody paid for:
// a *aiprov.CallError that is Retryable and not Engaged. A configuration refusal (401/402/404/422 —
// not retryable) says nothing about the provider being down and must not open it for every purpose;
// an engaged error may have cost money and is the ledger's business, not the breaker's; an error that
// is not a CallError carries no verdict. None of those three counts — but each still ENDS the call,
// so a probe it was is released rather than left reserved. a is the Admission Admit handed out (the
// zero one when there was no breaker); a counted fault creates the breaker when there is none yet.
func (r *Registry) RecordFailure(providerKey, capability string, a Admission, err error) {
	ce, ok := aiprov.AsCallError(err)
	if !ok || !ce.Retryable || aiprov.Engaged(err) {
		r.Release(providerKey, capability, a)
		return
	}
	r.breakerFor(providerKey, capability).Fault(a, r.clock())
}

// RecordSuccess ends admission a, whose call went through: it clears the closed breaker's failure
// count, and closes a probing one when a holds the probe. A provider with no breaker yet is left
// without one.
func (r *Registry) RecordSuccess(providerKey, capability string, a Admission) {
	if b := r.lookupBreaker(providerKey, capability); b != nil {
		b.Success(a)
	}
}

// ResetBreakers closes every breaker of a provider and drops any probe reservation — after its key
// was written, and by Reload when the effective key changed. It retires every Admission handed out
// before it, so a call still running on the old key cannot count against the new one.
func (r *Registry) ResetBreakers(providerKey string) {
	// EVERY capability the provider serves, CREATED when absent (Codex FIX-C P2). A call admitted with
	// the zero Admission — no breaker existed yet — before the rotation must not count its fault
	// against the FRESH key: three old-key 429s in flight at the rotation would otherwise create a
	// generation-0 breaker and open it for five minutes. Creating the entry here moves its generation
	// past 0, so every zero admission is stale by the time it ends.
	for _, capability := range entity.AIProviderCapabilities(providerKey) {
		r.breakerFor(providerKey, capability).Reset()
	}
	prefix := providerKey + "/"
	r.bmu.Lock()
	entries := make([]*probeBreaker, 0, 2)
	for k, b := range r.breakers {
		if strings.HasPrefix(k, prefix) {
			entries = append(entries, b)
		}
	}
	r.bmu.Unlock()
	for _, b := range entries {
		b.Reset()
	}
}

// ───────────────────────── worker (fxsync shape) ─────────────────────────

// Name implements health.Reporter.
func (r *Registry) Name() string { return Name }

// LastSuccess implements health.Reporter (zero until the first clean poll).
func (r *Registry) LastSuccess() time.Time { return r.tracker.LastSuccess() }

// Start launches the poller.
func (r *Registry) Start(ctx context.Context) error {
	if r.ctx != nil && r.stop != nil {
		return fmt.Errorf("%s worker already started", Name)
	}
	r.ctx, r.stop = context.WithCancel(ctx)
	r.wg.Go(func() {
		r.run(r.ctx)
	})
	return nil
}

// Stop signals the poller to exit and waits for it.
func (r *Registry) Stop() error {
	if r.stop == nil {
		return fmt.Errorf("%s worker already stopped or not started", Name)
	}
	r.stop()
	r.stop = nil
	r.wg.Wait()
	return nil
}

func (r *Registry) run(ctx context.Context) {
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()

	var consecutiveFailures int
	for {
		select {
		case <-ticker.C:
			if r.pollOnce(ctx) {
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			delay := backoffDelay(r.poll, consecutiveFailures)
			r.log.WarnContext(ctx, "ai registry: backing off after a failed poll",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// backoffDelay returns base × 2^(n-1), capped at base × backoffCapFactor.
func backoffDelay(base time.Duration, consecutiveFailures int) time.Duration {
	limit := base * backoffCapFactor
	delay := base
	for i := 1; i < consecutiveFailures; i++ {
		delay *= 2
		if delay >= limit {
			return limit
		}
	}
	return delay
}

// pollOnce reloads when config_version moved (or nothing was ever loaded). It reports whether the
// tick succeeded; a panic is recovered and counts as a failed tick.
func (r *Registry) pollOnce(ctx context.Context) (ok bool) {
	defer saferun.Recover(ctx, Name)

	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	v, err := r.store.ConfigVersion(ctx)
	if err != nil {
		r.tracker.MarkError(err)
		r.log.ErrorContext(ctx, "ai registry: config version read failed", slog.String("err", err.Error()))
		return false
	}
	if s := r.snap.Load(); s != nil && s.version == v {
		r.tracker.MarkSuccess()
		return true
	}
	if err := r.Reload(ctx); err != nil { // Reload marks the tracker itself
		r.log.ErrorContext(ctx, "ai registry: reload failed; the previous snapshot stays in force",
			slog.String("err", err.Error()))
		return false
	}
	return true
}
