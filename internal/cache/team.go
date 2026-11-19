package cache

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/shopspring/decimal"
)

// Per-team snapshots.
//
// Everything a team can change, or that is derived from a team's rows, lives in that team's own
// snapshot (teamSnap): merch dictionaries with their counts, colours, fibres, tags, collections,
// promos, carriers, payment-method allowance, hero, announce, complimentary shipping, the house margin
// target, the dictionary revisions. A getter or setter resolves the team from its ctx; a team never
// reads or writes another team's snapshot. With teams mode off there is exactly one snapshot, team #1,
// filled at boot by InitConsts — the same data, the same order, as the old process-wide cache.
//
// With teams mode on (SetStrict(true)) a ctx without a team reads zero values and writes nothing
// (ErrNoTenant): nothing falls back to team #1. Team #1 is filled at boot; any other team is loaded on
// its first use through the registered LoadFunc from that team's own database (one load per team at a
// time), kept fresh by its own writes and EnsureDictionaryFresh, and dropped after idleTTL without use
// or when more than maxCached teams are held (least recently used first). Team #1 is never dropped.
//
// What is the same for every team (order statuses, payment-method identity, measurement names,
// languages, care symbols, category/size definitions and the category -> size-system mapping,
// genders and sort factors, the base currency) stays process-wide in cache.go: it is seeded by
// migrations, no request writes it, and it is loaded from team #1.

const (
	defaultIdleTTL   = 30 * time.Minute
	defaultMaxCached = 64
	// loadTimeout bounds one team's lazy load; it runs detached from the asking request so a
	// cancelled request never fails the waiters that share the load.
	loadTimeout = 30 * time.Second
)

// TeamData is what a LoadFunc reads from one team's database to build that team's snapshot.
type TeamData struct {
	Dict *entity.DictionaryInfo
	Hero *entity.HeroFullWithTranslations
	// Revisions are the dictionary revisions Dict reflects (read before Dict, so a change made
	// mid-load is seen as stale by the next freshness check, never lost).
	Revisions map[entity.DictionaryNamespace]int64
	// TargetMarginPct <= 0 keeps the built-in default.
	TargetMarginPct float64
}

// LoadFunc loads the snapshot of the team in ctx from that team's database.
type LoadFunc func(ctx context.Context) (TeamData, error)

type teamRegistry struct {
	mu        sync.RWMutex
	strict    atomic.Bool
	loader    LoadFunc
	teams     map[int64]*teamSnap
	inflight  map[int64]*loadCall
	settings  map[int64]*teamSettings
	idleTTL   time.Duration
	maxCached int
	now       func() time.Time
}

type loadCall struct {
	done chan struct{}
	snap *teamSnap
	err  error
}

// registry holds every team's snapshot. Team #1's entry exists from the start with the legacy
// defaults, so a read before InitConsts returns exactly what the old process-wide cache returned.
var registry = newTeamRegistry()

func newTeamRegistry() *teamRegistry {
	r := &teamRegistry{
		teams:     map[int64]*teamSnap{},
		inflight:  map[int64]*loadCall{},
		settings:  map[int64]*teamSettings{},
		idleTTL:   defaultIdleTTL,
		maxCached: defaultMaxCached,
		now:       time.Now,
	}
	r.teams[tenant.TeamOneID] = newTeamSnap()
	return r
}

// SetStrict turns teams mode on for the cache: a ctx without a team no longer means team #1.
func SetStrict(on bool) { registry.strict.Store(on) }

// SetLoader registers how a team's snapshot is loaded from its own database (store.LoadTeamCache).
// nil turns lazy loading off: a team that is not cached reads zero values.
func SetLoader(f LoadFunc) {
	registry.mu.Lock()
	registry.loader = f
	registry.mu.Unlock()
}

type loadingKey struct{}

// teamID resolves the team a call belongs to: the team in ctx; with none, team #1 when teams mode
// is off, else ErrNoTenant.
func (r *teamRegistry) teamID(ctx context.Context) (int64, error) {
	if t, ok := tenant.From(ctx); ok {
		return t.ID, nil
	}
	if r.strict.Load() {
		return 0, tenant.ErrNoTenant
	}
	return tenant.TeamOneID, nil
}

// get returns the snapshot of the team in ctx for a read, loading it first when it is not cached
// and a loader is registered. nil when there is no team (teams mode), no loader, or the load failed:
// the getter then returns its zero value.
func (r *teamRegistry) get(ctx context.Context) *teamSnap {
	tid, err := r.teamID(ctx)
	if err != nil {
		return nil
	}
	s, _ := r.ensure(ctx, tid, false)
	return s
}

// getForWrite is get for a setter: with no loader a missing snapshot is created empty (unit tests,
// tools), so the write is not lost; an error means nothing was written (the database stays the truth
// and the next load reads it).
func (r *teamRegistry) getForWrite(ctx context.Context) (*teamSnap, int64, error) {
	tid, err := r.teamID(ctx)
	if err != nil {
		return nil, 0, err
	}
	s, err := r.ensure(ctx, tid, true)
	if err == nil && s == nil {
		err = fmt.Errorf("cache: team %d not cached", tid)
	}
	return s, tid, err
}

func (r *teamRegistry) ensure(ctx context.Context, tid int64, create bool) (*teamSnap, error) {
	r.mu.RLock()
	s := r.teams[tid]
	loader := r.loader
	r.mu.RUnlock()
	if s != nil && (s.loaded.Load() || loader == nil) {
		s.touch(r.now())
		return s, nil
	}
	// A cache call made by the loader itself (a store read that consults the cache) must not wait
	// for the load it is part of.
	if v, ok := ctx.Value(loadingKey{}).(int64); ok && v == tid {
		if s != nil {
			return s, nil
		}
		return nil, fmt.Errorf("cache: team %d is loading", tid)
	}
	if loader == nil {
		if !create {
			return nil, nil
		}
		r.mu.Lock()
		if s = r.teams[tid]; s == nil {
			s = newTeamSnap()
			s.loaded.Store(true)
			r.teams[tid] = s
			r.sweepLocked()
		}
		r.mu.Unlock()
		s.touch(r.now())
		return s, nil
	}
	return r.load(ctx, tid)
}

// load runs one load per team at a time: the first caller loads, the others wait for it (or for
// their own ctx). A failed load is not cached; the next call tries again.
func (r *teamRegistry) load(ctx context.Context, tid int64) (*teamSnap, error) {
	r.mu.Lock()
	if s := r.teams[tid]; s != nil && s.loaded.Load() {
		r.mu.Unlock()
		s.touch(r.now())
		return s, nil
	}
	if c, ok := r.inflight[tid]; ok {
		r.mu.Unlock()
		select {
		case <-c.done:
			return c.snap, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := &loadCall{done: make(chan struct{})}
	r.inflight[tid] = c
	loader := r.loader
	r.mu.Unlock()

	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
	lctx = context.WithValue(lctx, loadingKey{}, tid)
	data, err := callLoader(lctx, loader)
	cancel()

	r.mu.Lock()
	delete(r.inflight, tid)
	if err != nil {
		c.err = fmt.Errorf("cache: load team %d: %w", tid, err)
	} else {
		s := r.teams[tid]
		if s == nil {
			s = newTeamSnap()
		}
		s.applyFull(data)
		s.loaded.Store(true)
		s.touch(r.now())
		r.teams[tid] = s
		c.snap = s
		r.sweepLocked()
	}
	r.mu.Unlock()
	if err == nil && data.Dict != nil {
		// A lazy load (production fills team #1 in InitConsts): the process-wide definitions and
		// identity come from team #1, or from the first team loaded while they are still empty.
		publishDefinitions(tid, data.Dict)
		setIdentityOnce(data.Dict)
	}
	close(c.done)
	if c.err != nil {
		slog.Default().ErrorContext(ctx, "team cache load failed",
			slog.Int64("tid", tid), slog.String("err", c.err.Error()))
	}
	return c.snap, c.err
}

func callLoader(ctx context.Context, f LoadFunc) (data TeamData, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("loader panic: %v", p)
		}
	}()
	data, err = f(ctx)
	if err == nil && data.Dict == nil {
		err = fmt.Errorf("loader returned no dictionary")
	}
	return data, err
}

// install replaces the snapshot of team tid with data and marks it loaded (InitConsts).
func (r *teamRegistry) install(tid int64, data TeamData) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.teams[tid]
	if s == nil {
		s = newTeamSnap()
		r.teams[tid] = s
	}
	s.applyFull(data)
	s.loaded.Store(true)
	s.touch(r.now())
}

// sweepLocked drops snapshots unused for idleTTL, then the least recently used ones above
// maxCached. Team #1 is never dropped. Caller holds r.mu.
func (r *teamRegistry) sweepLocked() {
	now := r.now().UnixNano()
	type entry struct {
		id   int64
		used int64
	}
	var rest []entry
	for id, s := range r.teams {
		if id == tenant.TeamOneID {
			continue
		}
		used := s.lastUsed.Load()
		if r.idleTTL > 0 && now-used > int64(r.idleTTL) {
			delete(r.teams, id)
			continue
		}
		rest = append(rest, entry{id, used})
	}
	if r.maxCached <= 0 || len(rest) <= r.maxCached {
		return
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].used < rest[j].used })
	for _, e := range rest[:len(rest)-r.maxCached] {
		delete(r.teams, e.id)
	}
}

// Sweep drops idle snapshots now (the dictionary poller calls it every tick).
func Sweep() {
	registry.mu.Lock()
	registry.sweepLocked()
	registry.mu.Unlock()
}

// CachedTeams returns the ids of the teams whose snapshot is held, ascending (tests, diagnostics).
func CachedTeams() []int64 {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]int64, 0, len(registry.teams))
	for id := range registry.teams {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// teamSnap is one team's cached data. Every field is guarded by mu.
type teamSnap struct {
	mu sync.RWMutex

	categories           []entity.Category
	sizes                []entity.Size
	collections          []entity.Collection
	productTags          []string
	tags                 []entity.TagDict
	colors               ColorDict
	fibers               []entity.Fiber
	modelPhotoTechCardID int

	promos        map[string]entity.PromoCode
	carriers      map[int]entity.ShipmentCarrier
	payByName     map[entity.PaymentMethodName]PaymentMethod
	payByID       map[int]entity.PaymentMethodName
	payList       []entity.PaymentMethod
	hero          *entity.HeroFullWithTranslations
	announce      *entity.AnnounceWithTranslations
	heroBg        string
	complimentary map[string]decimal.Decimal
	targetMargin  float64
	revisions     map[entity.DictionaryNamespace]int64

	loaded   atomic.Bool
	lastUsed atomic.Int64
}

// newTeamSnap returns a snapshot holding the legacy defaults of the old process-wide cache.
func newTeamSnap() *teamSnap {
	return &teamSnap{
		categories:    []entity.Category{},
		sizes:         []entity.Size{},
		collections:   []entity.Collection{},
		productTags:   []string{},
		tags:          []entity.TagDict{},
		colors:        NewColorDict(nil),
		fibers:        []entity.Fiber{},
		promos:        map[string]entity.PromoCode{},
		carriers:      map[int]entity.ShipmentCarrier{},
		payByName:     paymentTemplates(),
		payByID:       map[int]entity.PaymentMethodName{},
		payList:       []entity.PaymentMethod{},
		hero:          &entity.HeroFullWithTranslations{},
		announce:      &entity.AnnounceWithTranslations{},
		complimentary: map[string]decimal.Decimal{},
		targetMargin:  entity.DefaultAlertThresholds().TargetMarginPct,
		revisions:     map[entity.DictionaryNamespace]int64{},
	}
}

// paymentTemplates is the per-team payment-method table before a load: the four known methods,
// no id, not allowed (the legacy byName map was pre-populated the same way).
func paymentTemplates() map[entity.PaymentMethodName]PaymentMethod {
	out := make(map[entity.PaymentMethodName]PaymentMethod, len(paymentMethods))
	for _, p := range paymentMethods {
		out[p.Method.Name] = PaymentMethod{
			Method: entity.PaymentMethod{Name: p.Method.Name},
			PB:     p.PB,
		}
	}
	return out
}

func (s *teamSnap) touch(now time.Time) { s.lastUsed.Store(now.UnixNano()) }

// applyFull replaces the whole snapshot with a fresh load (InitConsts for team #1, the LoadFunc for
// any team). It mirrors what the old InitConsts put into the process-wide cache.
func (s *teamSnap) applyFull(d TeamData) {
	di := d.Dict
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setDictLocked(di)
	s.modelPhotoTechCardID = di.ModelPhotoTechCardID

	s.promos = make(map[string]entity.PromoCode, len(di.Promos))
	for _, p := range di.Promos {
		s.promos[p.Code] = p
	}
	s.carriers = make(map[int]entity.ShipmentCarrier, len(di.ShipmentCarriers))
	for _, sc := range di.ShipmentCarriers {
		s.carriers[sc.Id] = sc
	}

	s.payByName = paymentTemplates()
	s.payByID = map[int]entity.PaymentMethodName{}
	s.payList = []entity.PaymentMethod{}
	for _, pm := range di.PaymentMethods {
		p, ok := s.payByName[pm.Name]
		if !ok {
			continue
		}
		p.Method.Id = pm.Id
		p.Method.Allowed = pm.Allowed
		s.payByName[pm.Name] = p
		s.payByID[pm.Id] = pm.Name
		s.payList = append(s.payList, pm)
	}

	s.complimentary = di.ComplimentaryShippingPrices
	s.heroBg = di.BackgroundHeroColor
	if di.Announce != nil {
		s.announce = di.Announce
	}
	if d.Hero != nil {
		s.hero = d.Hero
	}
	if d.TargetMarginPct > 0 {
		s.targetMargin = d.TargetMarginPct
	}
	s.revisions = copyRevisions(d.Revisions)
}

// setDictLocked replaces the merch dictionaries RefreshDictionary refreshes. Caller holds s.mu.
func (s *teamSnap) setDictLocked(di *entity.DictionaryInfo) {
	s.categories = di.Categories
	s.sizes = di.Sizes
	s.collections = di.Collections
	s.productTags = di.ProductTags
	s.tags = di.Tags
	s.fibers = di.Fibers
	s.colors = NewColorDict(di.Colors)
}

func copyRevisions(in map[entity.DictionaryNamespace]int64) map[entity.DictionaryNamespace]int64 {
	out := make(map[entity.DictionaryNamespace]int64, len(in))
	for ns, r := range in {
		out[ns] = r
	}
	return out
}

// teamSettings are the per-team storefront settings that live only in memory (the admin sets them,
// nothing persists them): they are never evicted, and a team that never set one reads the legacy
// default.
type teamSettings struct {
	mu                     sync.RWMutex
	siteEnabled            bool
	maxOrderItems          int
	bigMenu                bool
	orderExpirationSeconds int
	paymentIsProd          bool
}

func defaultTeamSettings() teamSettings {
	return teamSettings{
		siteEnabled:   true,
		maxOrderItems: 3,
		paymentIsProd: true, // true = prod Stripe (CARD), false = test Stripe (CARD_TEST)
		// orderExpirationSeconds 0 = use the payment handler default
	}
}

// readSettings returns the settings of the team in ctx; ok is false with no team in teams mode.
func (r *teamRegistry) readSettings(ctx context.Context) (teamSettings, bool) {
	tid, err := r.teamID(ctx)
	if err != nil {
		return teamSettings{}, false
	}
	r.mu.RLock()
	st := r.settings[tid]
	r.mu.RUnlock()
	if st == nil {
		return defaultTeamSettings(), true
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	return teamSettings{
		siteEnabled:            st.siteEnabled,
		maxOrderItems:          st.maxOrderItems,
		bigMenu:                st.bigMenu,
		orderExpirationSeconds: st.orderExpirationSeconds,
		paymentIsProd:          st.paymentIsProd,
	}, true
}

// writeSettings applies f to the settings of the team in ctx.
func (r *teamRegistry) writeSettings(ctx context.Context, f func(*teamSettings)) error {
	tid, err := r.teamID(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	st := r.settings[tid]
	if st == nil {
		d := defaultTeamSettings()
		st = &d
		r.settings[tid] = st
	}
	r.mu.Unlock()
	st.mu.Lock()
	f(st)
	st.mu.Unlock()
	return nil
}

// publishDefinitions keeps the process-wide definitions in step with a team's dictionary: team #1's
// always, another team's only while none are known yet (team #1 was never loaded: a harness, or an
// installation whose team #1 database is empty). The rows are migration-seeded and the same in every
// team database, so whichever team fills them first gives the same answer.
func publishDefinitions(tid int64, d *entity.DictionaryInfo) {
	if d == nil {
		return
	}
	if tid == tenant.TeamOneID {
		setDefinitions(d)
		return
	}
	cacheMu.RLock()
	known := len(sizeById) > 0 || len(categoryById) > 0
	cacheMu.RUnlock()
	if !known && (len(d.Sizes) > 0 || len(d.Categories) > 0) {
		setDefinitions(d)
	}
}
