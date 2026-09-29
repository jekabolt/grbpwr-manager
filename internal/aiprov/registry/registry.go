// Package registry is the AI providers' live configuration: which provider is enabled, which key
// answers for it, which candidates serve a purpose, and whether a provider is currently breaking.
//
// ONE SNAPSHOT, SWAPPED WHOLE. Reload reads the five config tables (dependency.AI.GetConfig), opens
// the sealed keys with the key ring, and swaps an atomic pointer. Every reader — KeyFunc closures,
// Candidates, Providers — reads the snapshot that is current AT THE MOMENT IT ASKS, so a request
// already on the wire keeps the header it was built with and the next one uses the new key; nobody
// ever sees half a reload.
//
// THE KEY RULE (02-PLAN S-2, tightened by B-33): the effective key is the database key — the one an
// admin saved in admin → AI providers — when it is stored and opens; nothing else ever answers. A
// provider row with enabled=0 WINS over it — "" is returned, and a client whose KeyFunc answers ""
// reports itself disabled. The env variables (EnvKeys) are read ONCE, at boot, by ImportEnvKeys: a
// value whose panel slot is empty is sealed and stored as if the admin had pasted it, and from that
// boot on the variables are dead weight the operator deletes. A stored key that does not open
// (wrong master, row swapped, no master at all) never stops the registry: the provider is reported
// as KeySource "unreadable", it answers "" until a person re-enters the key, and one warning is
// logged per unreadable blob.
//
// HOT RELOAD ACROSS INSTANCES. The instance that handles a write RPC calls Reload itself; every
// other instance learns from the poller, which compares ai_settings.config_version every 60 s and
// reloads only when it moved.
//
// BREAKERS (breaker.go). A caller's contract, in order: Candidates → Admit(the picked candidate) →
// the physical call → exactly one of RecordSuccess / RecordFailure / Release, WITH THE ADMISSION
// Admit handed out — an end acts only on the admission it completes.
//
// This package imports no client package. The clients (openrouter, orimages, fal, meshy)
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
	KeySourceDB   = "db"   // a stored key opened with the master key
	KeySourceNone = "none" // no stored key: nothing answers
	// KeySourceEnv is LEGACY (B-33): no snapshot produces it any more — the env variables are
	// imported at boot, never read at call time. The constant stays for the wire and the client's
	// switch, which still name it.
	KeySourceEnv        = "env"
	KeySourceUnreadable = "unreadable" // a stored key that does not open; nothing answers until it is re-entered
)

// Breaker states — ProviderState.Breaker and BreakerState (breaker.go), in the circuitbreaker
// package's own words.
const (
	BreakerClosed   = "closed"
	BreakerOpen     = "open"
	BreakerHalfOpen = "half-open"
)

// EnvKeys are the env values as the process booted — the INPUT of the one-time import
// (ImportEnvKeys) and nothing else: no KeyFunc reads them (B-33).
//
// OpenRouterImages (OPENROUTER_IMAGES_API_KEY, which config/cfg.go already falls back to
// OPENROUTER_API_KEY) is never imported: ONE provider row serves both OpenRouter clients, so the
// chat variable is the one that lands in the slot. A differing images value is only warned about.
type EnvKeys struct {
	OpenRouter, OpenRouterImages, Fal, Meshy string
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
	KeyLast4    string // last four characters of the stored key that opened; "" when none or unreadable
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

// New builds a registry. Nothing is read until Reload; before it, every KeyFunc answers "" (no
// snapshot = no key; the env values are never a fallback, B-33) and Candidates answers nothing.
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
		if prev != nil && r.effectiveKeyIn(prev, key) != r.effectiveKeyIn(next, key) {
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
		// err names the AAD ("fal:api") or the missing master; it carries no byte of the blob.
		// Since B-33 nothing answers for the provider until a person re-enters the key — there is
		// no env fallback to soften this — so the line says exactly where to go.
		r.log.WarnContext(ctx, "ai provider key is stored but does not open — re-enter it in admin → AI providers",
			slog.String("provider", providerKey),
			slog.String("kind", string(kind)),
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

// ───────────────────────── the one-time env import (B-33) ─────────────────────────

// envImports are the three variables the import reads, in the panel's order, each with the provider
// row it lands in. OPENROUTER_IMAGES_API_KEY is deliberately absent — one openrouter row serves both
// OpenRouter clients (EnvKeys) — and no other provider ever had an env variable.
var envImports = []struct{ provider, variable string }{
	{entity.AIProviderOpenRouter, "OPENROUTER_API_KEY"},
	{entity.AIProviderFal, "FAL_KEY"},
	{entity.AIProviderMeshy, "MESHY_API_KEY"},
}

// EnvImportedBy is the updated_by the panel shows for a key the import stored — a name no admin
// has, so «set by env-import» reads as what it is.
const EnvImportedBy = "env-import"

// envValue is the booted env value of one importable provider; "" for every other provider.
func (r *Registry) envValue(providerKey string) string {
	switch providerKey {
	case entity.AIProviderOpenRouter:
		return r.env.OpenRouter
	case entity.AIProviderFal:
		return r.env.Fal
	case entity.AIProviderMeshy:
		return r.env.Meshy
	}
	return ""
}

// ImportEnvKeys is the boot-time bridge from the old key source to the only one (B-33): for each
// provider whose env variable is set and whose panel api slot is EMPTY (no blob at all), the value is
// sealed exactly as the admin handler seals a pasted key (keyring.SealProviderKey, the same AAD) and
// stored with updated_by EnvImportedBy. Returns the providers it stored.
//
// Runs ONCE per boot, BEFORE the first Reload — so the boot snapshot already carries the imported
// keys and a deployment that had its keys in env never serves a keyless request — and after the DB is
// up. A slot that already holds a blob, readable or not, is left alone in silence: the panel's key
// wins, even a broken one (a person re-enters it; overwriting it with the env value would resurrect
// a key the admin may have rotated away). Without a master key nothing can be sealed: one ERROR line
// says so and nothing is imported or used — the env values are not a fallback any more.
// OPENROUTER_IMAGES_API_KEY is never imported; when it differs from OPENROUTER_API_KEY one WARN names
// the variable. No line ever carries a key: last4 only.
func (r *Registry) ImportEnvKeys(ctx context.Context) (imported []string, err error) {
	if !r.ring.Enabled() {
		r.log.ErrorContext(ctx, "AI_KEYS_MASTER_KEY is not set: env keys are no longer read; "+
			"set it and save the keys in admin → AI providers")
		return nil, nil
	}
	cfg, err := r.store.GetConfig(ctx)
	if err == nil && cfg == nil {
		err = errors.New("the store returned no configuration")
	}
	if err != nil {
		return nil, fmt.Errorf("ai registry: env import: %w", err)
	}
	rows := make(map[string]entity.AIProvider, len(cfg.Providers))
	for _, p := range cfg.Providers {
		rows[p.Key] = p
	}
	for _, imp := range envImports {
		value := r.envValue(imp.provider)
		if value == "" {
			continue
		}
		row, ok := rows[imp.provider]
		if !ok {
			// The migration seeds every row; a missing one is a broken deploy, not a slot to create.
			r.log.WarnContext(ctx, "ai provider has no row; its env key is not imported",
				slog.String("provider", imp.provider), slog.String("variable", imp.variable))
			continue
		}
		if len(row.APIKeyEnc) > 0 {
			continue // the panel's key wins, readable or not
		}
		enc, last4, err := r.ring.SealProviderKey(imp.provider, string(entity.AIKeyAPI), value)
		if err != nil {
			return imported, fmt.Errorf("ai registry: env import: seal %s key: %w", imp.provider, err)
		}
		if err := r.store.SetProviderKey(ctx, imp.provider, entity.AIKeyAPI, enc, last4, EnvImportedBy); err != nil {
			return imported, fmt.Errorf("ai registry: env import: store %s key: %w", imp.provider, err)
		}
		r.log.InfoContext(ctx, fmt.Sprintf("ai provider %s: env key imported into the panel (last4 %s) — delete %s from the app spec",
			imp.provider, last4, imp.variable),
			slog.String("provider", imp.provider),
			slog.String("variable", imp.variable),
			slog.String("key_last4", last4),
		)
		imported = append(imported, imp.provider)
	}
	if r.env.OpenRouterImages != "" && r.env.OpenRouterImages != r.env.OpenRouter {
		r.log.WarnContext(ctx, "OPENROUTER_IMAGES_API_KEY differs from OPENROUTER_API_KEY and is not imported: "+
			"one openrouter row serves both clients — save the key pictures should use in admin → AI providers, "+
			"then delete the variable from the app spec",
			slog.String("provider", entity.AIProviderOpenRouter),
			slog.String("variable", "OPENROUTER_IMAGES_API_KEY"),
		)
	}
	return imported, nil
}

// effectiveKeyIn is THE key rule, on one snapshot (B-33: the database is the only source):
//   - no snapshot yet (never reloaded) → "" — a call before the boot Reload has no key; the boot
//     order (import → Reload → clients wired) makes that window unreachable in the app;
//   - a provider with no row → "" (the migration seeds all nine rows; a missing one is a broken
//     deploy, and inventing a key for it would hide that);
//   - enabled=0 → "" — it WINS over the stored key;
//   - else the stored key when it opened, else "" (unreadable or empty: re-enter it in the panel).
func (r *Registry) effectiveKeyIn(s *snapshot, providerKey string) string {
	if s == nil {
		return ""
	}
	p, ok := s.providers[providerKey]
	if !ok || !p.enabled {
		return ""
	}
	return p.dbKey
}

// stateIn renders a provider's ProviderState from one snapshot, the breaker column aside.
func (r *Registry) stateIn(s *snapshot, providerKey string) ProviderState {
	st := ProviderState{Key: providerKey, Enabled: true}
	var p providerSnap
	var ok bool
	if s != nil {
		p, ok = s.providers[providerKey]
	}
	if ok {
		st.Enabled = p.enabled
		st.KeySource = p.keySource
		st.AdminKeySet = p.adminKey != ""
		// The last four of the stored key that opened — computed as if enabled, so a switched-off
		// provider still shows which key it would use. "" for an unreadable blob: nothing answers.
		st.KeyLast4 = keyring.Last4(p.dbKey)
	} else {
		st.KeySource = KeySourceNone
	}
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
// call: "" when the provider is disabled (enabled=0 wins over the stored key), else the stored key,
// else "" — never an env variable (B-33). Capture it once at wiring time; a Reload changes what it
// answers, not the func.
func (r *Registry) KeyFunc(providerKey string) func() string {
	return func() string { return r.effectiveKeyIn(r.snap.Load(), providerKey) }
}

// OpenRouterImagesKeyFunc is KeyFunc("openrouter") for the OpenRouter IMAGE client. Since B-33 it is
// the chat client's answer exactly — one row, one stored key, one switch; the separate env fallback
// (OPENROUTER_IMAGES_API_KEY) it once carried is gone. It stays a func of its own so the wiring in
// app.go keeps naming which client it hands the key to.
func (r *Registry) OpenRouterImagesKeyFunc() func() string {
	return r.KeyFunc(entity.AIProviderOpenRouter)
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
	listed, _ := r.CandidatesAt(purpose)
	return listed
}

// CandidatesAt is Candidates together with the config_version of the SAME snapshot the list was read
// from — one atomic load for both. A caller that keys anything by version (the router warns once per
// provider per version) must not read Version() separately: a reload between the two reads stamps the
// list with a version it did not come from.
func (r *Registry) CandidatesAt(purpose string) ([]Candidate, uint64) {
	listed, _, version := r.walk(purpose)
	return listed, version
}

// RouteHeadAt is the purpose's CONFIGURED head — the first route row, as the owner saved it, read
// BEFORE the key and breaker filtering Candidates applies — with the config_version of the same
// snapshot. A "" provider is the capability's default provider, and a row whose provider cannot serve
// the capability (or has no default) is not a head, so the next row is taken. ok=false for an unknown
// purpose, an empty route and before the first Reload.
//
// It exists for NAMING, never for calling: a door that could not call (no key saved, env key unset)
// still says which slug and which API root it WOULD have used — the knob a person has to turn.
func (r *Registry) RouteHeadAt(purpose string) (Candidate, uint64, bool) {
	s := r.snap.Load()
	if s == nil {
		return Candidate{}, 0, false
	}
	capability := entity.AIPurposeCapability(purpose)
	if capability == "" {
		return Candidate{}, s.version, false
	}
	for _, c := range s.routes[purpose] {
		pk := c.ProviderKey
		if pk == "" {
			pk = s.defaultProvider(capability)
		}
		if pk == "" || !entity.AIProviderServes(pk, capability) {
			continue
		}
		return Candidate{ProviderKey: pk, Model: c.Model, Position: c.Position}, s.version, true
	}
	return Candidate{}, s.version, false
}

// BreakerHeld lists the candidates Candidates dropped for ONE reason only: their breaker is open,
// inside its window — enabled, keyed, able to serve the purpose, and paused. It exists so a door
// that finds nothing to call can tell «the provider is paused after repeated failures» from «AI is
// not configured» (the first passes by itself in minutes, the second needs a person). Same order and
// the same repeat rule as Candidates; nil before the first Reload.
func (r *Registry) BreakerHeld(purpose string) []Candidate {
	_, held, _ := r.walk(purpose)
	return held
}

// walk is the one pass over a purpose's route both lists come from, on ONE snapshot load: listed =
// what Candidates answers, held = what it dropped only because the breaker is open, version = that
// snapshot's config_version (0 before the first Reload).
func (r *Registry) walk(purpose string) (listed, held []Candidate, version uint64) {
	s := r.snap.Load()
	if s == nil {
		return nil, nil, 0
	}
	capability := entity.AIPurposeCapability(purpose)
	if capability == "" {
		return nil, nil, s.version
	}
	now := r.clock()
	seen := map[string]bool{}
	for _, c := range s.routes[purpose] {
		pk := c.ProviderKey
		if pk == "" {
			pk = s.defaultProvider(capability)
		}
		if pk == "" || !entity.AIProviderServes(pk, capability) {
			continue
		}
		if r.effectiveKeyIn(s, pk) == "" { // disabled or keyless
			continue
		}
		dup := pk + "\x00" + c.Model
		if seen[dup] {
			continue
		}
		seen[dup] = true
		cand := Candidate{ProviderKey: pk, Model: c.Model, Position: c.Position}
		if b := r.lookupBreaker(pk, capability); b != nil && b.State(now) == BreakerOpen {
			held = append(held, cand)
			continue
		}
		listed = append(listed, cand)
	}
	return listed, held, s.version
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
