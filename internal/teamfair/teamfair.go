// Package teamfair keeps one busy team from slowing the others (X15, gap 14).
//
// Every team shares one process, one database pool (MYSQL_MAX_OPEN_CONNECTIONS, 5 on lekalo) and
// one vCPU. The Gate is a two-level bulkhead keyed by team:
//
//   - light (every call not listed below): at most Limits.Light calls of one team in flight;
//   - heavy (archive import/export, uploads, image and vector processing, exports): at most
//     Limits.Heavy of one team at a time AND at most Config.GlobalHeavy in the whole process; a
//     heavy call also holds one of its team's light slots;
//   - ai (synchronous AI calls): at most Limits.AI of one team at a time;
//   - rate: a token bucket per team (Limits.Rate per second, Limits.Burst).
//
// A team waits in its own queue first, so it puts at most Limits.Heavy calls into the global heavy
// queue and Limits.Light calls into the pool's queue: the shared queues become round-robin across
// teams without a scheduler. Slots are taken in a fixed order (team heavy → team light → global
// heavy), so there is no deadlock. A wait longer than Config.LightWait / HeavyWait, or an empty
// token bucket, is a BusyError (the edge answers ResourceExhausted "team_busy" / HTTP 429).
//
// Storefront calls (the shared storefront serves every team's shop) take two lanes of their own per
// team, so a shop never queues behind its team's admin nor the admin behind its shop, and browsing
// never refuses a customer paying:
//
//   - shop (browsing): at most Config.Shop.Light calls of one team in flight AND at most
//     Config.GlobalShop of all team shops together, plus a token bucket per team shop
//     (Config.Shop.Rate / Burst); a wait longer than Config.ShopWait or an empty bucket is a
//     BusyError (the edge answers ResourceExhausted "shop_busy");
//   - checkout (placing, paying, order status): the same shape with its own slots, global bound and
//     bucket (Config.Checkout, GlobalCheckout, CheckoutWait). A browsing flood never touches it; a
//     checkout flood is still capped;
//   - the storefront team (grbpwr) never takes a global slot of either lane and has its own,
//     generous shares (Config.StorefrontShop, StorefrontCheckout), so no brand's traffic can refuse
//     it, while its own flood is still bounded.
//
// A call with no team (sign-in, the operator console) is not gated.
package teamfair

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Lane is the kind of work a call is.
type Lane uint8

const (
	Light Lane = iota
	Heavy
	AI
	// Upload: a plain HTTP upload whose body streams for as long as the client's link allows. It
	// takes only its team's heavy slot — never a global one, so a slow upload of one team cannot
	// block every other team's heavy work; the edge bounds the body read (uploadReadTimeout).
	Upload
	// Shop: a storefront call of the team's shop (the shop share, never its admin share).
	Shop
	// Checkout: a checkout call of the team's shop (placing, paying, order status): a share of its
	// own, never the shop's browsing share.
	Checkout
)

func (l Lane) String() string {
	switch l {
	case Heavy:
		return "heavy"
	case AI:
		return "ai"
	case Upload:
		return "upload"
	case Shop:
		return "shop"
	case Checkout:
		return "checkout"
	}
	return "light"
}

// BusyError: the team's share is used up (its queue wait ran out or its rate bucket is empty).
// A type, not a package var: the tenant gate ratchets package-level state (tools/tenantgate G4).
type BusyError struct{ Why string }

func (e BusyError) Error() string { return "teamfair: team is busy (" + e.Why + ")" }

// IsBusy reports whether err is a refusal of the gate.
func IsBusy(err error) bool {
	var b BusyError
	return errors.As(err, &b)
}

// Limits are one team's share. A zero field = the Config default.
type Limits struct {
	Light int     // calls in flight
	Heavy int     // heavy calls at a time
	AI    int     // synchronous AI calls at a time
	Rate  float64 // calls per second, sustained
	Burst int     // calls at once from a full bucket
}

// Config is the whole gate.
type Config struct {
	// Off turns the gate into a pass-through (TEAM_FAIR=off).
	Off bool
	// Team is every team's default share.
	Team Limits
	// GlobalHeavy bounds heavy calls of all teams together.
	GlobalHeavy int
	// LightWait / HeavyWait bound the queue wait of a light / heavy-or-ai call.
	LightWait, HeavyWait time.Duration

	// ShopOff leaves storefront calls, browsing and checkout, ungated (TEAM_FAIR_SHOP=off).
	ShopOff bool
	// Shop is every team shop's share of storefront calls: Light = calls in flight, Rate / Burst =
	// its token bucket (Heavy and AI are not used).
	Shop Limits
	// GlobalShop bounds storefront calls of all team shops together (the storefront team aside).
	GlobalShop int
	// ShopWait bounds the queue wait of a storefront call.
	ShopWait time.Duration
	// Checkout is every team shop's share of checkout calls, apart from its browsing share: Light =
	// calls in flight, Rate / Burst = its token bucket.
	Checkout Limits
	// GlobalCheckout bounds checkout calls of all team shops together (the storefront team aside).
	GlobalCheckout int
	// CheckoutWait bounds the queue wait of a checkout call.
	CheckoutWait time.Duration
	// StorefrontTeamID is the storefront team (grbpwr); 0 = none. Its storefront calls take
	// StorefrontShop / StorefrontCheckout and never a global slot.
	StorefrontTeamID int64
	// StorefrontShop / StorefrontCheckout are the storefront team's shares of browsing and checkout:
	// generous, never unlimited (a zero field = the default).
	StorefrontShop     Limits
	StorefrontCheckout Limits
}

// Defaults derives the shares from the database pool size: a team holds at most half the pool
// (rounded up), heavy work of all teams at most half the pool (rounded down, at least one); a team
// shop as much as a team's admin for browsing and again for checkout, all team shops together two
// thirds of the pool per lane (rounded up, at least two), so the storefront team and the admin
// always find room. The storefront team: 64 browsing and 32 checkout calls in flight at least (8 and
// 4 per pool connection), buckets of 500/s and 100/s.
// pool 5 → light 3, global heavy 2, shop 3, all shops 4, checkout 3, all checkouts 4; pool 15 →
// light 8, global heavy 7, shop 8, all shops 10, checkout 8, all checkouts 10.
func Defaults(pool int) Config {
	if pool < 1 {
		pool = 1
	}
	light := (pool + 1) / 2
	if light < 2 {
		light = 2
	}
	gh := pool / 2
	if gh < 1 {
		gh = 1
	}
	gs := (2*pool + 2) / 3
	if gs < 2 {
		gs = 2
	}
	return Config{
		Team:        Limits{Light: light, Heavy: 1, AI: 2, Rate: 50, Burst: 200},
		GlobalHeavy: gh,
		LightWait:   10 * time.Second,
		HeavyWait:   5 * time.Minute,
		Shop:        Limits{Light: light, Rate: 50, Burst: 200},
		GlobalShop:  gs,
		ShopWait:    5 * time.Second,

		Checkout:       Limits{Light: light, Rate: 20, Burst: 60},
		GlobalCheckout: gs,
		CheckoutWait:   15 * time.Second,

		StorefrontShop:     Limits{Light: max(64, 8*pool), Rate: 500, Burst: 1000},
		StorefrontCheckout: Limits{Light: max(32, 4*pool), Rate: 100, Burst: 300},
	}
}

// fillShop replaces zero fields of a shop share (Light, Rate, Burst) with the defaults d.
func (l Limits) fillShop(d Limits) Limits {
	if l.Light <= 0 {
		l.Light = d.Light
	}
	if l.Rate <= 0 {
		l.Rate = d.Rate
	}
	if l.Burst <= 0 {
		l.Burst = d.Burst
	}
	return l
}

// fill replaces zero fields of l with the defaults d.
func (l Limits) fill(d Limits) Limits {
	if l.Light <= 0 {
		l.Light = d.Light
	}
	if l.Heavy <= 0 {
		l.Heavy = d.Heavy
	}
	if l.AI <= 0 {
		l.AI = d.AI
	}
	if l.Rate <= 0 {
		l.Rate = d.Rate
	}
	if l.Burst <= 0 {
		l.Burst = d.Burst
	}
	return l
}

// Key names whose share a call takes.
type Key struct {
	TeamID int64
}

func (k Key) String() string {
	return "team:" + strconv.FormatInt(k.TeamID, 10)
}

// Override returns a team's own limits (a plan or the operator console, later); ok=false = default.
type Override func(teamID int64) (Limits, bool)

// Stats are counters since start.
type Stats struct {
	Busy    int64 // refusals: queue wait ran out
	Limited int64 // refusals: rate bucket empty
	Waited  int64 // calls that had to queue at all
}

type share struct {
	limits Limits
	light  chan struct{} // nil = no cap (a shop share with Light 0 only)
	heavy  chan struct{}
	ai     chan struct{}
	bucket *rate.Limiter // nil = no bucket (a shop share with Rate 0 only)
}

func newShare(l Limits) *share {
	return &share{
		limits: l,
		light:  make(chan struct{}, l.Light),
		heavy:  make(chan struct{}, l.Heavy),
		ai:     make(chan struct{}, l.AI),
		bucket: rate.NewLimiter(rate.Limit(l.Rate), l.Burst),
	}
}

// newShopShare is a shop or checkout share: a light cap and a bucket only where l sets them (New
// fills both; only a Config edited after New can leave one out).
func newShopShare(l Limits) *share {
	s := &share{limits: l}
	if l.Light > 0 {
		s.light = make(chan struct{}, l.Light)
	}
	if l.Rate > 0 {
		burst := l.Burst
		if burst < 1 {
			burst = int(l.Rate) + 1
		}
		s.bucket = rate.NewLimiter(rate.Limit(l.Rate), burst)
	}
	return s
}

// shareKey names a share: a team's admin share (lane Light), its shop share or its checkout share.
type shareKey struct {
	team int64
	lane Lane
}

// Gate is the bulkhead. The zero value is not usable; a nil *Gate is a pass-through.
type Gate struct {
	cfg      Config
	heavy    chan struct{}
	shop     chan struct{}
	checkout chan struct{}
	mu       sync.Mutex
	shares   map[shareKey]*share
	over     atomic.Pointer[Override]

	busy, limited, waited atomic.Int64
}

// New builds a gate. Zero fields of cfg take Defaults(5) values.
func New(cfg Config) *Gate {
	d := Defaults(5)
	cfg.Team = cfg.Team.fill(d.Team)
	if cfg.GlobalHeavy <= 0 {
		cfg.GlobalHeavy = d.GlobalHeavy
	}
	if cfg.LightWait <= 0 {
		cfg.LightWait = d.LightWait
	}
	if cfg.HeavyWait <= 0 {
		cfg.HeavyWait = d.HeavyWait
	}
	cfg.Shop = cfg.Shop.fillShop(d.Shop)
	cfg.Checkout = cfg.Checkout.fillShop(d.Checkout)
	cfg.StorefrontShop = cfg.StorefrontShop.fillShop(d.StorefrontShop)
	cfg.StorefrontCheckout = cfg.StorefrontCheckout.fillShop(d.StorefrontCheckout)
	if cfg.GlobalShop <= 0 {
		cfg.GlobalShop = d.GlobalShop
	}
	if cfg.GlobalCheckout <= 0 {
		cfg.GlobalCheckout = d.GlobalCheckout
	}
	if cfg.ShopWait <= 0 {
		cfg.ShopWait = d.ShopWait
	}
	if cfg.CheckoutWait <= 0 {
		cfg.CheckoutWait = d.CheckoutWait
	}
	return &Gate{cfg: cfg, heavy: make(chan struct{}, cfg.GlobalHeavy), shop: make(chan struct{}, cfg.GlobalShop),
		checkout: make(chan struct{}, cfg.GlobalCheckout), shares: map[shareKey]*share{}}
}

// Config returns the effective configuration.
func (g *Gate) Config() Config {
	if g == nil {
		return Config{Off: true}
	}
	return g.cfg
}

// SetOverride installs the per-team limits source (nil = everyone on the default). A team's new
// limits apply to its next call; calls in flight finish on the old share.
func (g *Gate) SetOverride(o Override) {
	if o == nil {
		g.over.Store(nil)
		return
	}
	g.over.Store(&o)
}

// Stats returns the counters.
func (g *Gate) Stats() Stats {
	if g == nil {
		return Stats{}
	}
	return Stats{Busy: g.busy.Load(), Limited: g.limited.Load(), Waited: g.waited.Load()}
}

func (g *Gate) shareOf(k Key) *share {
	want := g.cfg.Team
	if p := g.over.Load(); p != nil {
		if l, ok := (*p)(k.TeamID); ok {
			want = l.fill(g.cfg.Team)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sk := shareKey{team: k.TeamID}
	s := g.shares[sk]
	if s == nil || s.limits != want {
		s = newShare(want)
		g.shares[sk] = s
	}
	return s
}

// storefront reports whether k is the storefront team.
func (g *Gate) storefront(k Key) bool {
	return g.cfg.StorefrontTeamID > 0 && k.TeamID == g.cfg.StorefrontTeamID
}

// shopShareOf is k's share of lane Shop or Checkout (the operator's per-team override does not
// apply to shops).
func (g *Gate) shopShareOf(k Key, lane Lane) *share {
	want := g.cfg.Shop
	switch {
	case lane == Checkout && g.storefront(k):
		want = g.cfg.StorefrontCheckout
	case lane == Checkout:
		want = g.cfg.Checkout
	case g.storefront(k):
		want = g.cfg.StorefrontShop
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sk := shareKey{team: k.TeamID, lane: lane}
	s := g.shares[sk]
	if s == nil || s.limits != want {
		s = newShopShare(want)
		g.shares[sk] = s
	}
	return s
}

// Acquire takes the slots of one call of lane for key. The returned release must be called once
// the call is over. BusyError = refused (rate or wait); a ctx error = the caller gave up waiting.
func (g *Gate) Acquire(ctx context.Context, k Key, lane Lane) (release func(), err error) {
	if g == nil || g.cfg.Off {
		return func() {}, nil
	}
	var s *share
	wait := g.cfg.LightWait
	switch lane {
	case Shop, Checkout:
		if g.cfg.ShopOff {
			return func() {}, nil
		}
		s, wait = g.shopShareOf(k, lane), g.cfg.ShopWait
		if lane == Checkout {
			wait = g.cfg.CheckoutWait
		}
	case Light:
		s = g.shareOf(k)
	default:
		s, wait = g.shareOf(k), g.cfg.HeavyWait
	}
	if s.bucket != nil && !s.bucket.Allow() {
		g.limited.Add(1)
		why := "rate"
		if lane == Shop || lane == Checkout {
			why = lane.String() + " rate"
		}
		return nil, BusyError{Why: why}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	var held []chan struct{}
	undo := func() {
		for i := len(held) - 1; i >= 0; i-- {
			<-held[i]
		}
	}
	queued := false
	take := func(c chan struct{}) error {
		select {
		case c <- struct{}{}:
			held = append(held, c)
			return nil
		default:
		}
		if !queued {
			queued = true
			g.waited.Add(1)
		}
		select {
		case c <- struct{}{}:
			held = append(held, c)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			g.busy.Add(1)
			return BusyError{Why: lane.String() + " queue"}
		}
	}
	var chain []chan struct{}
	switch lane {
	case Heavy:
		// The global slot last: a heavy call waiting for its own team's light slot must not sit
		// on a global slot another team could use.
		chain = []chan struct{}{s.heavy, s.light, g.heavy}
	case AI:
		chain = []chan struct{}{s.ai}
	case Upload:
		chain = []chan struct{}{s.heavy}
	case Shop, Checkout:
		// The team shop's own slot first, the global one of its lane last (as heavy); the storefront
		// team never takes a global slot.
		if s.light != nil {
			chain = append(chain, s.light)
		}
		if !g.storefront(k) {
			global := g.shop
			if lane == Checkout {
				global = g.checkout
			}
			chain = append(chain, global)
		}
	default:
		chain = []chan struct{}{s.light}
	}
	for _, c := range chain {
		if err := take(c); err != nil {
			undo()
			return nil, err
		}
	}
	var once sync.Once
	return func() { once.Do(undo) }, nil
}
