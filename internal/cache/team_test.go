package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// resetForTest drops every team but #1, puts team #1 back to the legacy defaults, turns teams mode
// off and unregisters the loader, now and after the test.
func resetForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		r := registry
		r.mu.Lock()
		r.teams = map[int64]*teamSnap{tenant.TeamOneID: newTeamSnap()}
		r.inflight = map[int64]*loadCall{}
		r.settings = map[int64]*teamSettings{}
		r.loader = nil
		r.idleTTL = defaultIdleTTL
		r.maxCached = defaultMaxCached
		r.now = time.Now
		r.mu.Unlock()
		r.strict.Store(false)

		cacheMu.Lock()
		for _, s := range orderStatuses {
			s.Status.Id = 0
		}
		orderStatusesById = map[int]*Status{}
		orderStatusesByName = map[entity.OrderStatusName]*Status{}
		entityOrderStatuses = []entity.OrderStatus{}
		for _, p := range paymentMethods {
			p.Method.Id = 0
			paymentMethodIdByPbId[p.PB] = 0
		}
		paymentMethodsById = map[int]*PaymentMethod{}
		cacheMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func teamCtx(id int64) context.Context {
	return tenant.With(context.Background(), tenant.Team{ID: id, Slug: fmt.Sprintf("t%d", id)})
}

// teamDict is a dictionary whose every per-team value carries the marker.
func teamDict(marker string) TeamData {
	return TeamData{
		Dict: &entity.DictionaryInfo{
			Categories:  []entity.Category{{ID: 1, Name: marker + "-cat", CountMen: len(marker)}},
			Sizes:       []entity.Size{{Id: 1, Name: marker + "-size"}},
			Collections: []entity.Collection{{ID: 1, Name: marker + "-coll"}},
			ProductTags: []string{marker + "-ptag"},
			Tags:        []entity.TagDict{{ID: 1, Code: marker + "-tag", Name: marker}},
			Colors:      []entity.Color{{ID: 1, Code: "AAA", Name: marker + "-colour"}},
			Fibers:      []entity.Fiber{{Code: marker + "-fib"}},
			Promos:      []entity.PromoCode{{Id: 1, PromoCodeInsert: entity.PromoCodeInsert{Code: marker + "-promo", Allowed: true}}},
			ShipmentCarriers: []entity.ShipmentCarrier{{Id: 1, ShipmentCarrierInsert: entity.ShipmentCarrierInsert{
				Carrier: marker + "-carrier", Allowed: true}}},
			PaymentMethods: []entity.PaymentMethod{
				{Id: 1, Name: entity.CARD, Allowed: true},
				{Id: 2, Name: entity.CARD_TEST, Allowed: true},
				{Id: 3, Name: entity.BANK_INVOICE},
				{Id: 4, Name: entity.CASH},
			},
			Announce:                    &entity.AnnounceWithTranslations{Link: marker + "-announce"},
			ComplimentaryShippingPrices: map[string]decimal.Decimal{"EUR": decimal.NewFromInt(int64(len(marker)))},
			BackgroundHeroColor:         marker + "-bg",
			ModelPhotoTechCardID:        len(marker),
		},
		Hero:            &entity.HeroFullWithTranslations{NavFeatured: entity.NavFeaturedWithTranslations{}},
		Revisions:       map[entity.DictionaryNamespace]int64{entity.DictNamespaceColor: int64(len(marker))},
		TargetMarginPct: float64(len(marker)),
	}
}

// markerOf reads back every per-team value of the team in ctx; all must agree with one marker.
func markerOf(t *testing.T, ctx context.Context) string {
	t.Helper()
	cats := GetCategories(ctx)
	require.Len(t, cats, 1)
	m := cats[0].Name[:len(cats[0].Name)-len("-cat")]
	require.Equal(t, m+"-size", GetSizes(ctx)[0].Name)
	require.Equal(t, m+"-coll", GetCollections(ctx)[0].Name)
	require.Equal(t, []string{m + "-ptag"}, GetProductTags(ctx))
	require.Equal(t, m+"-tag", GetTags(ctx)[0].Code)
	c, ok := GetColorByCode(ctx, "AAA")
	require.True(t, ok)
	require.Equal(t, m+"-colour", c.Name)
	require.Equal(t, m+"-fib", GetFibers(ctx)[0].Code)
	_, ok = GetPromoByCode(ctx, m+"-promo")
	require.True(t, ok)
	sc, ok := GetShipmentCarrierById(ctx, 1)
	require.True(t, ok)
	require.Equal(t, m+"-carrier", sc.Carrier)
	require.Equal(t, m+"-announce", GetAnnounce(ctx).Link)
	require.Equal(t, m+"-bg", GetBackgroundHeroColor(ctx))
	require.Equal(t, len(m), GetModelPhotoTechCardID(ctx))
	require.Equal(t, float64(len(m)), GetTargetMarginPct(ctx))
	require.True(t, GetComplimentaryShippingPrices(ctx)["EUR"].Equal(decimal.NewFromInt(int64(len(m)))))
	return m
}

// loaderOf serves teams[tid] and counts calls per team.
type fakeLoader struct {
	mu    sync.Mutex
	data  map[int64]TeamData
	calls map[int64]int
	err   map[int64]error
	gate  chan struct{} // when set, every load waits for it
}

func newFakeLoader() *fakeLoader {
	return &fakeLoader{data: map[int64]TeamData{}, calls: map[int64]int{}, err: map[int64]error{}}
}

func (f *fakeLoader) load(ctx context.Context) (TeamData, error) {
	tm, ok := tenant.From(ctx)
	if !ok {
		return TeamData{}, tenant.ErrNoTenant
	}
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[tm.ID]++
	if err := f.err[tm.ID]; err != nil {
		return TeamData{}, err
	}
	d, ok := f.data[tm.ID]
	if !ok {
		return TeamData{}, fmt.Errorf("no team %d", tm.ID)
	}
	return d, nil
}

func (f *fakeLoader) callsOf(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

// Legacy (teams mode off): a ctx with no team is team #1, and InitConsts fills it with the same
// values the old process-wide cache held.
func TestLegacyNoTeamIsTeamOne(t *testing.T) {
	resetForTest(t)
	bg := context.Background()

	// Before InitConsts: the legacy defaults.
	require.Equal(t, []entity.Category{}, GetCategories(bg))
	require.True(t, GetSiteAvailability(bg))
	require.Equal(t, 3, GetMaxOrderItems(bg))
	require.True(t, GetPaymentIsProd(bg))
	require.False(t, GetBigMenu(bg))
	require.Equal(t, 0, GetOrderExpirationSeconds(bg))
	require.Equal(t, entity.DefaultAlertThresholds().TargetMarginPct, GetTargetMarginPct(bg))
	require.NotNil(t, GetHero(bg))
	require.NotNil(t, GetAnnounce(bg))
	pm, ok := GetPaymentMethodByName(bg, entity.CARD)
	require.True(t, ok, "the known methods resolve by name before the load, as before")
	require.Equal(t, 0, pm.Method.Id)

	d := teamDict("one")
	d.Dict.OrderStatuses = allOrderStatuses()
	require.NoError(t, InitConsts(bg, d.Dict, d.Hero))
	SetTargetMarginPct(bg, d.TargetMarginPct) // app.go loads it right after the boot
	require.Equal(t, "one", markerOf(t, bg))
	require.Equal(t, "one", markerOf(t, teamCtx(tenant.TeamOneID)), "no team == team #1 with teams mode off")
	require.Empty(t, GetDictionaryRevisions(bg), "the boot load leaves revisions empty: the first poll reloads, as before")

	// Process-wide identity and definitions come from the boot load.
	name, ok := PaymentMethodNameByID(2)
	require.True(t, ok)
	require.Equal(t, entity.CARD_TEST, name)
	sz, ok := GetSizeById(1)
	require.True(t, ok)
	require.Equal(t, "one-size", sz.Name)

	pm, ok = GetPaymentMethodById(bg, 1)
	require.True(t, ok)
	require.True(t, pm.Method.Allowed)
	require.Equal(t, []entity.PaymentMethod{{Id: 1, Name: entity.CARD, Allowed: true}},
		GetPaymentMethodsFilteredByIsProd(bg))
	require.NoError(t, SetPaymentIsProd(bg, false))
	require.Equal(t, []entity.PaymentMethod{{Id: 2, Name: entity.CARD_TEST, Allowed: true}},
		GetPaymentMethodsFilteredByIsProd(bg))
}

func allOrderStatuses() []entity.OrderStatus {
	out := make([]entity.OrderStatus, 0, len(orderStatuses))
	for i, s := range orderStatuses {
		out = append(out, entity.OrderStatus{Id: i + 1, Name: s.Status.Name})
	}
	return out
}

// InitConsts still refuses a dictionary without every order status / payment method.
func TestInitConstsMissingIdentity(t *testing.T) {
	resetForTest(t)
	d := teamDict("one")
	err := InitConsts(context.Background(), d.Dict, d.Hero)
	require.ErrorContains(t, err, "order status")
}

// Two teams: each reads only its own snapshot, and no write crosses over.
func TestTwoTeamsIsolation(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	fl := newFakeLoader()
	fl.data[1] = teamDict("alpha")
	fl.data[2] = teamDict("bravoo")
	SetLoader(fl.load)
	a, b := teamCtx(1), teamCtx(2)

	require.Equal(t, "alpha", markerOf(t, a))
	require.Equal(t, "bravoo", markerOf(t, b))

	// Writes in B stay in B.
	RefreshDictionary(b, &entity.DictionaryInfo{
		Categories: []entity.Category{{ID: 1, Name: "bravoo-cat"}},
		Sizes:      []entity.Size{{Id: 1, Name: "bravoo-size"}},
		Colors:     []entity.Color{{ID: 1, Code: "AAA", Name: "bravoo-colour"}, {ID: 2, Code: "BBB", Name: "new"}},
	})
	_, ok := GetColorByCode(a, "BBB")
	require.False(t, ok, "B's new colour must not reach A")
	_, ok = GetColorByCode(b, "BBB")
	require.True(t, ok)

	AddPromo(b, entity.PromoCode{Id: 9, PromoCodeInsert: entity.PromoCodeInsert{Code: "bonly", Allowed: true}})
	_, ok = GetPromoByCode(a, "bonly")
	require.False(t, ok)
	DisablePromo(b, "alpha-promo") // A's code does not exist in B: nothing changes anywhere
	p, _ := GetPromoByCode(a, "alpha-promo")
	require.True(t, p.Allowed)

	UpdatePaymentMethodAllowance(b, entity.CARD, false)
	RefreshEntityPaymentMethods(b)
	pmA, _ := GetPaymentMethodByName(a, entity.CARD)
	pmB, _ := GetPaymentMethodByName(b, entity.CARD)
	require.True(t, pmA.Method.Allowed)
	require.False(t, pmB.Method.Allowed)
	require.Len(t, GetPaymentMethodsFilteredByIsProd(a), 1)
	require.Empty(t, GetPaymentMethodsFilteredByIsProd(b))

	UpdateShipmentCarrierAllowance(b, "bravoo-carrier", false)
	scA, _ := GetShipmentCarrierById(a, 1)
	require.True(t, scA.Allowed)

	SetAnnounce(b, "b-link", nil)
	UpdateHero(b, &entity.HeroFullWithTranslations{})
	SetBackgroundHeroColor(b, "#b")
	UpdateComplimentaryShippingPrices(b, map[string]decimal.Decimal{"USD": decimal.NewFromInt(1)})
	SetTargetMarginPct(b, 77)
	SetDictionaryRevisions(b, map[entity.DictionaryNamespace]int64{entity.DictNamespaceTag: 99})
	require.NoError(t, SetMaxOrderItems(b, 9))
	require.NoError(t, SetSiteAvailability(b, false))
	require.NoError(t, SetBigMenu(b, true))
	require.NoError(t, SetOrderExpirationSeconds(b, 60))
	require.NoError(t, SetPaymentIsProd(b, false))

	require.Equal(t, "alpha", markerOf(t, a), "A after every B write")
	require.Equal(t, 3, GetMaxOrderItems(a))
	require.True(t, GetSiteAvailability(a))
	require.False(t, GetBigMenu(a))
	require.Equal(t, 0, GetOrderExpirationSeconds(a))
	require.True(t, GetPaymentIsProd(a))
	require.Equal(t, map[entity.DictionaryNamespace]int64{entity.DictNamespaceColor: 5}, GetDictionaryRevisions(a))

	require.Equal(t, 9, GetMaxOrderItems(b))
	require.False(t, GetSiteAvailability(b))
	require.Equal(t, "b-link", GetAnnounce(b).Link)
	require.Equal(t, float64(77), GetTargetMarginPct(b))
	require.Equal(t, map[entity.DictionaryNamespace]int64{entity.DictNamespaceTag: 99}, GetDictionaryRevisions(b))

	// And writes in A stay in A.
	DeletePromo(a, "alpha-promo")
	_, ok = GetPromoByCode(b, "bravoo-promo")
	require.True(t, ok)
	require.Equal(t, 1, fl.callsOf(1))
	require.Equal(t, 1, fl.callsOf(2))
}

// Teams mode: a ctx without a team reads zero values and writes nothing — never team #1's data.
func TestStrictNoTeamNeverFallsBackToTeamOne(t *testing.T) {
	resetForTest(t)
	d := teamDict("one")
	d.Dict.OrderStatuses = allOrderStatuses()
	require.NoError(t, InitConsts(teamCtx(1), d.Dict, d.Hero))
	SetTargetMarginPct(teamCtx(1), d.TargetMarginPct)
	SetStrict(true)
	bg := context.Background()

	require.Empty(t, GetCategories(bg))
	require.Empty(t, GetColors(bg))
	_, ok := GetColorByCode(bg, "AAA")
	require.False(t, ok)
	_, ok = GetPromoByCode(bg, "one-promo")
	require.False(t, ok)
	_, ok = GetShipmentCarrierById(bg, 1)
	require.False(t, ok)
	_, ok = GetPaymentMethodById(bg, 1)
	require.False(t, ok)
	require.Empty(t, GetPaymentMethodsFilteredByIsProd(bg))
	require.Empty(t, GetAnnounce(bg).Link)
	require.Empty(t, GetBackgroundHeroColor(bg))
	require.Zero(t, GetModelPhotoTechCardID(bg))
	require.Empty(t, GetComplimentaryShippingPrices(bg))
	require.False(t, GetSiteAvailability(bg))
	require.Zero(t, GetMaxOrderItems(bg))
	require.Equal(t, entity.DefaultAlertThresholds().TargetMarginPct, GetTargetMarginPct(bg))
	require.True(t, DictionaryRevisionsStale(bg, map[entity.DictionaryNamespace]int64{}))

	require.ErrorIs(t, SetMaxOrderItems(bg, 1), tenant.ErrNoTenant)
	require.ErrorIs(t, SetSiteAvailability(bg, false), tenant.ErrNoTenant)
	AddPromo(bg, entity.PromoCode{PromoCodeInsert: entity.PromoCodeInsert{Code: "x"}})
	RefreshDictionary(bg, &entity.DictionaryInfo{})
	_, err := EnsureDictionaryFresh(bg, &fakeRevSrc{}, &fakeInfoSrc{})
	require.ErrorIs(t, err, tenant.ErrNoTenant)

	require.Equal(t, "one", markerOf(t, teamCtx(1)), "team #1 untouched by no-team writes")
	require.True(t, GetSiteAvailability(teamCtx(1)))
	_, ok = GetPromoByCode(teamCtx(1), "x")
	require.False(t, ok)
}

// One load per team at a time, whatever the number of concurrent first readers.
func TestLazyLoadSingleflight(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	fl := newFakeLoader()
	fl.data[2] = teamDict("bravoo")
	fl.data[3] = teamDict("charlie")
	fl.gate = make(chan struct{})
	SetLoader(fl.load)

	var wg sync.WaitGroup
	var seen atomic.Int32
	for i := 0; i < 32; i++ {
		tid := int64(2 + i%2)
		wg.Go(func() {
			if len(GetColors(teamCtx(tid))) == 1 {
				seen.Add(1)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(fl.gate)
	wg.Wait()
	require.Equal(t, int32(32), seen.Load())
	require.Equal(t, 1, fl.callsOf(2))
	require.Equal(t, 1, fl.callsOf(3))
}

// A failed load is not cached: the next call loads again. A waiter whose ctx ends stops waiting.
func TestLoadFailureRetries(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	fl := newFakeLoader()
	fl.data[2] = teamDict("bravoo")
	fl.err[2] = errors.New("db down")
	SetLoader(fl.load)
	b := teamCtx(2)

	require.Empty(t, GetColors(b))
	require.Equal(t, []int64{1}, CachedTeams())
	AddPromo(b, entity.PromoCode{PromoCodeInsert: entity.PromoCodeInsert{Code: "lost"}}) // skipped: no snapshot

	fl.mu.Lock()
	delete(fl.err, 2)
	fl.mu.Unlock()
	require.Equal(t, "bravoo", markerOf(t, b))
	_, ok := GetPromoByCode(b, "lost")
	require.False(t, ok, "a write that found no snapshot is not replayed; the DB is the truth")
	require.Equal(t, 3, fl.callsOf(2), "two failed loads (read, write), then one that worked")
}

// A store read inside the loader that consults the cache of the same team must not deadlock.
func TestLoaderReentryDoesNotDeadlock(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	SetLoader(func(ctx context.Context) (TeamData, error) {
		_ = GetColors(ctx) // the team is loading: returns at once
		_, _ = EnsureDictionaryFresh(ctx, &fakeRevSrc{}, &fakeInfoSrc{})
		return teamDict("bravoo"), nil
	})
	done := make(chan string)
	go func() { done <- markerOf(t, teamCtx(2)) }()
	select {
	case m := <-done:
		require.Equal(t, "bravoo", m)
	case <-time.After(5 * time.Second):
		t.Fatal("loader re-entry deadlocked")
	}
}

// Idle snapshots are dropped after idleTTL, the least recently used above maxCached; team #1 never.
func TestEviction(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	fl := newFakeLoader()
	for id := int64(1); id <= 6; id++ {
		fl.data[id] = teamDict(fmt.Sprintf("team%d", id))
	}
	SetLoader(fl.load)
	clock := time.Unix(1_000_000, 0)
	registry.mu.Lock()
	registry.now = func() time.Time { return clock }
	registry.idleTTL = time.Minute
	registry.maxCached = 3
	registry.mu.Unlock()
	tick := func(d time.Duration) {
		registry.mu.Lock()
		clock = clock.Add(d)
		registry.mu.Unlock()
	}

	for id := int64(2); id <= 5; id++ {
		_ = GetColors(teamCtx(id))
		tick(time.Second)
	}
	// Four teams loaded, at most three kept besides team #1: team 2 (oldest) is gone.
	require.Equal(t, []int64{1, 3, 4, 5}, CachedTeams())

	_ = GetColors(teamCtx(3)) // touch 3
	tick(30 * time.Second)
	_ = GetColors(teamCtx(6)) // insert 6: evicts the LRU (4)
	require.Equal(t, []int64{1, 3, 5, 6}, CachedTeams())

	tick(2 * time.Minute)
	Sweep()
	require.Equal(t, []int64{1}, CachedTeams(), "idle teams are dropped, team #1 stays")

	// An evicted team reloads on its next use, with its own data.
	require.Equal(t, "team4", markerOf(t, teamCtx(4)))
	require.Equal(t, 2, fl.callsOf(4))
	require.Zero(t, fl.callsOf(1), "team #1 is never loaded lazily here: it was never evicted")
}

// EnsureDictionaryFresh compares each team against its own revisions and reloads only that team.
func TestEnsureDictionaryFreshPerTeam(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	fl := newFakeLoader()
	fl.data[1] = teamDict("alpha")
	fl.data[2] = teamDict("bravoo")
	SetLoader(fl.load)
	a, b := teamCtx(1), teamCtx(2)
	require.Equal(t, "alpha", markerOf(t, a))
	require.Equal(t, "bravoo", markerOf(t, b))

	revs := map[int64]map[entity.DictionaryNamespace]int64{
		1: {entity.DictNamespaceColor: 5},
		2: {entity.DictNamespaceColor: 6},
	}
	src := perTeamSrc{revs: revs, dict: map[int64]*entity.DictionaryInfo{
		1: {Colors: []entity.Color{{Code: "AAA", Name: "alpha-colour"}}},
		2: {Colors: []entity.Color{{Code: "ZZZ", Name: "reloaded"}}},
	}}
	reloaded, err := EnsureDictionaryFresh(a, src, src)
	require.NoError(t, err)
	require.False(t, reloaded, "A is at its own revision")
	reloaded, err = EnsureDictionaryFresh(b, src, src)
	require.NoError(t, err)
	require.False(t, reloaded, "B is at its own revision")

	revs[2][entity.DictNamespaceColor] = 7
	reloaded, err = EnsureDictionaryFresh(a, src, src)
	require.NoError(t, err)
	require.False(t, reloaded, "B's bump does not reload A")
	reloaded, err = EnsureDictionaryFresh(b, src, src)
	require.NoError(t, err)
	require.True(t, reloaded)
	_, ok := GetColorByCode(b, "ZZZ")
	require.True(t, ok)
	_, ok = GetColorByCode(a, "ZZZ")
	require.False(t, ok)
}

type perTeamSrc struct {
	revs map[int64]map[entity.DictionaryNamespace]int64
	dict map[int64]*entity.DictionaryInfo
}

func (p perTeamSrc) GetDictionaryRevisions(ctx context.Context) (map[entity.DictionaryNamespace]int64, error) {
	tm, _ := tenant.From(ctx)
	return copyRevisions(p.revs[tm.ID]), nil
}

func (p perTeamSrc) GetDictionaryInfo(ctx context.Context) (*entity.DictionaryInfo, error) {
	tm, _ := tenant.From(ctx)
	return p.dict[tm.ID], nil
}

// Many goroutines read and write two teams at once (run with -race); every read sees its own
// team's values, and the eviction sweep runs concurrently.
func TestTwoTeamsConcurrent(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	fl := newFakeLoader()
	fl.data[1] = teamDict("alpha")
	fl.data[2] = teamDict("bravoo")
	SetLoader(fl.load)

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 16; g++ {
		tid, marker := int64(1), "alpha"
		if g%2 == 1 {
			tid, marker = 2, "bravoo"
		}
		ctx := teamCtx(tid)
		wg.Go(func() {
			for i := 0; i < 300; i++ {
				switch i % 6 {
				case 0:
					RefreshDictionary(ctx, teamDict(marker).Dict)
				case 1:
					AddPromo(ctx, entity.PromoCode{PromoCodeInsert: entity.PromoCodeInsert{Code: marker + "-promo", Allowed: true}})
				case 2:
					UpdatePaymentMethodAllowance(ctx, entity.CARD, i%2 == 0)
					RefreshEntityPaymentMethods(ctx)
				case 3:
					_ = SetMaxOrderItems(ctx, int(tid))
				case 4:
					SetTargetMarginPct(ctx, float64(len(marker)))
				case 5:
					Sweep()
				}
				cats := GetCategories(ctx)
				if len(cats) != 1 || cats[0].Name != marker+"-cat" {
					errs <- fmt.Errorf("team %d read %v", tid, cats)
					return
				}
				if c, ok := GetColorByCode(ctx, "AAA"); !ok || c.Name != marker+"-colour" {
					errs <- fmt.Errorf("team %d colour %v", tid, c)
					return
				}
				if n := GetMaxOrderItems(ctx); n != 3 && n != int(tid) {
					errs <- fmt.Errorf("team %d max items %d", tid, n)
					return
				}
				if GetTargetMarginPct(ctx) != float64(len(marker)) {
					errs <- fmt.Errorf("team %d margin %v", tid, GetTargetMarginPct(ctx))
					return
				}
				_ = GetPaymentMethodsFilteredByIsProd(ctx)
				_ = GetShipmentCarriers(ctx)
				_ = GetHero(ctx)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// The colour dictionary handed to ctx-less code is the team's own.
func TestColorDictFor(t *testing.T) {
	resetForTest(t)
	SetStrict(true)
	fl := newFakeLoader()
	fl.data[2] = teamDict("bravoo")
	SetLoader(fl.load)
	d := ColorDictFor(teamCtx(2))
	c, ok := d.ByCode("AAA")
	require.True(t, ok)
	require.Equal(t, "bravoo-colour", c.Name)
	require.Len(t, d.All(), 1)
	_, ok = ColorDict{}.ByCode("AAA")
	require.False(t, ok, "the zero dictionary resolves nothing")
	_, ok = ShipmentCarrierLookup(teamCtx(2))(1)
	require.True(t, ok)
	_, ok = ShipmentCarrierLookup(context.Background())(1)
	require.False(t, ok)
}
