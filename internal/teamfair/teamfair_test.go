package teamfair

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	teamA = Key{TeamID: 2}
	teamB = Key{TeamID: 3}
)

func testGate(light, heavy, globalHeavy int) *Gate {
	return New(Config{
		Team:        Limits{Light: light, Heavy: heavy, AI: 1, Rate: 1e6, Burst: 1e6},
		GlobalHeavy: globalHeavy,
		LightWait:   200 * time.Millisecond,
		HeavyWait:   200 * time.Millisecond,
	})
}

func mustAcquire(t *testing.T, g *Gate, k Key, l Lane) func() {
	t.Helper()
	rel, err := g.Acquire(context.Background(), k, l)
	if err != nil {
		t.Fatalf("acquire %s %s: %v", k, l, err)
	}
	return rel
}

func TestDefaults(t *testing.T) {
	for _, c := range []struct{ pool, light, gh, gs int }{{5, 3, 2, 4}, {15, 8, 7, 10}, {1, 2, 1, 2}, {0, 2, 1, 2}, {2, 2, 1, 2}} {
		d := Defaults(c.pool)
		if d.Team.Light != c.light || d.GlobalHeavy != c.gh || d.Team.Heavy != 1 {
			t.Errorf("pool %d: light %d heavy-all %d heavy %d", c.pool, d.Team.Light, d.GlobalHeavy, d.Team.Heavy)
		}
		if d.Shop.Light != c.light || d.GlobalShop != c.gs || d.ShopWait <= 0 || d.Shop.Rate <= 0 {
			t.Errorf("pool %d: shop %d shop-all %d wait %s rate %v", c.pool, d.Shop.Light, d.GlobalShop, d.ShopWait, d.Shop.Rate)
		}
		if d.Checkout.Light != c.light || d.GlobalCheckout != c.gs || d.CheckoutWait <= d.ShopWait || d.Checkout.Rate <= 0 || d.Checkout.Burst <= 0 {
			t.Errorf("pool %d: checkout %+v all %d wait %s", c.pool, d.Checkout, d.GlobalCheckout, d.CheckoutWait)
		}
		// The storefront team: bounded, never unlimited, and far above a team shop.
		sfs, sfc := d.StorefrontShop, d.StorefrontCheckout
		if sfs.Light < 64 || sfs.Rate <= d.Shop.Rate || sfs.Burst <= d.Shop.Burst || sfc.Light < 32 || sfc.Rate <= d.Checkout.Rate || sfc.Burst <= d.Checkout.Burst || d.ShopOff {
			t.Errorf("pool %d: storefront shop %+v checkout %+v", c.pool, sfs, sfc)
		}
	}
}

// One team at its light cap waits (then is refused); another team is not affected at all.
func TestLightCapIsPerTeam(t *testing.T) {
	g := testGate(2, 1, 1)
	r1 := mustAcquire(t, g, teamA, Light)
	r2 := mustAcquire(t, g, teamA, Light)
	start := time.Now()
	if _, err := g.Acquire(context.Background(), teamA, Light); !IsBusy(err) {
		t.Fatalf("third light call of A: %v, want busy", err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("the refusal did not wait for the queue")
	}
	rb := mustAcquire(t, g, teamB, Light) // B is never in A's queue
	rb()
	r1()
	r3 := mustAcquire(t, g, teamA, Light) // a released slot is reused
	r2()
	r3()
	if s := g.Stats(); s.Busy != 1 || s.Waited != 1 {
		t.Fatalf("stats %+v", s)
	}
}

// The storefront team's storefront calls get a generous share by default: 50 concurrent slow
// storefront calls of team #1, while its admin share is full, the shop shares are tiny and another
// team's shop holds the only global shop slot, never get ResourceExhausted. Its share is still
// bounded (TestStorefrontShopLimits refuses over it).
func TestStorefrontGenerousByDefault(t *testing.T) {
	g := New(Config{Team: Limits{Light: 1, Heavy: 1, AI: 1, Rate: 1, Burst: 1},
		GlobalHeavy: 1, LightWait: 10 * time.Millisecond, HeavyWait: 10 * time.Millisecond,
		Shop: Limits{Light: 1, Rate: 1, Burst: 1}, GlobalShop: 1, ShopWait: 10 * time.Millisecond,
		StorefrontTeamID: 1})
	brand := mustAcquire(t, g, teamA, Shop) // the only global shop slot
	defer brand()
	shop := func(m string) bool { return strings.HasPrefix(m, "/frontend.FrontendService/") }
	if sf := g.Config().StorefrontShop; sf.Light <= 0 || sf.Rate <= 0 || sf.Burst <= 0 {
		t.Fatalf("the storefront shop is unbounded: %+v", sf)
	}
	e := NewEdge(g, shop, nil)
	ctx := tenant.With(context.Background(), tenant.Team{ID: 1})
	r := mustAcquire(t, g, Key{TeamID: 1}, Light) // team #1's admin share is full
	defer r()
	var wg sync.WaitGroup
	var refused atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := unaryCall(e, ctx, "/frontend.FrontendService/GetProduct", func(context.Context, any) (any, error) {
				time.Sleep(50 * time.Millisecond) // a slow page
				return nil, nil
			})
			if status.Code(err) == codes.ResourceExhausted {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := refused.Load(); n != 0 {
		t.Fatalf("%d of 50 storefront calls refused", n)
	}
}

// An upload takes only its team's heavy slot: another team's heavy work runs past it even with a
// global heavy bound of 1, and the uploading team's light calls are not touched.
func TestUploadTakesOnlyTeamHeavy(t *testing.T) {
	g := testGate(1, 1, 1)
	ru := mustAcquire(t, g, teamA, Upload)
	mustAcquire(t, g, teamB, Heavy)()
	mustAcquire(t, g, teamA, Light)()
	if _, err := g.Acquire(context.Background(), teamA, Heavy); !IsBusy(err) {
		t.Fatalf("A's heavy during its upload: %v", err)
	}
	if _, err := g.Acquire(context.Background(), teamA, Upload); !IsBusy(err) {
		t.Fatalf("A's second upload: %v", err)
	}
	ru()
	mustAcquire(t, g, teamA, Upload)()
}

// Heavy: one per team, a global bound, and a heavy call takes a light slot of its team.
func TestHeavyPerTeamAndGlobal(t *testing.T) {
	g := testGate(2, 1, 2)
	ra := mustAcquire(t, g, teamA, Heavy)
	if _, err := g.Acquire(context.Background(), teamA, Heavy); !IsBusy(err) {
		t.Fatalf("second heavy of A: %v", err)
	}
	rb := mustAcquire(t, g, teamB, Heavy)
	c := Key{TeamID: 4}
	if _, err := g.Acquire(context.Background(), c, Heavy); !IsBusy(err) {
		t.Fatalf("third team's heavy above the global bound: %v", err)
	}
	rl := mustAcquire(t, g, teamA, Light) // A: 1 heavy + 1 light = its 2 slots
	if _, err := g.Acquire(context.Background(), teamA, Light); !IsBusy(err) {
		t.Fatalf("A's light above its cap while a heavy runs: %v", err)
	}
	// C can still do light work while heavy work is full.
	mustAcquire(t, g, c, Light)()
	ra()
	rb()
	rl()
	mustAcquire(t, g, c, Heavy)()
}

// A refused heavy call gives back everything it took on the way (no slot leaks).
func TestRefusalReleasesPartialSlots(t *testing.T) {
	g := testGate(1, 1, 1)
	rb := mustAcquire(t, g, teamB, Heavy) // global heavy is now full
	if _, err := g.Acquire(context.Background(), teamA, Heavy); !IsBusy(err) {
		t.Fatalf("want busy, got %v", err)
	}
	// A's own heavy slot must have been released: after B finishes, A gets in at once.
	rb()
	mustAcquire(t, g, teamA, Heavy)()
	mustAcquire(t, g, teamA, Light)()
}

// Two-level queueing is round-robin: B's one heavy call does not wait behind A's whole flood.
func TestHeavyFloodDoesNotQueueOtherTeam(t *testing.T) {
	g := New(Config{
		Team:        Limits{Light: 10, Heavy: 1, AI: 1, Rate: 1e6, Burst: 1e6},
		GlobalHeavy: 1, LightWait: time.Second, HeavyWait: 10 * time.Second,
	})
	const job = 30 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ { // A: 20 heavy jobs = 600 ms of serial work
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rel, err := g.Acquire(context.Background(), teamA, Heavy); err == nil {
				time.Sleep(job)
				rel()
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	start := time.Now()
	rel, err := g.Acquire(context.Background(), teamB, Heavy)
	if err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	rel()
	wg.Wait()
	// B waits at most for the A job running now plus the one A job already in the global queue.
	if waited > 3*job {
		t.Fatalf("B's heavy call waited %v behind A's flood, want ≤ %v", waited, 3*job)
	}
}

func TestRateLimit(t *testing.T) {
	g := New(Config{Team: Limits{Light: 100, Heavy: 1, AI: 1, Rate: 1, Burst: 3}})
	for i := 0; i < 3; i++ {
		mustAcquire(t, g, teamA, Light)()
	}
	_, err := g.Acquire(context.Background(), teamA, Light)
	if !IsBusy(err) {
		t.Fatalf("4th call in a burst of 3: %v", err)
	}
	mustAcquire(t, g, teamB, Light)()
	if g.Stats().Limited != 1 {
		t.Fatalf("stats %+v", g.Stats())
	}
}

func TestCanceledWaitIsNotBusy(t *testing.T) {
	g := testGate(1, 1, 1)
	r := mustAcquire(t, g, teamA, Light)
	defer r()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := g.Acquire(ctx, teamA, Light)
	if !errors.Is(err, context.Canceled) || IsBusy(err) {
		t.Fatalf("got %v", err)
	}
}

func TestOverrideAndOff(t *testing.T) {
	g := testGate(1, 1, 1)
	g.SetOverride(func(id int64) (Limits, bool) { return Limits{Light: 3}, id == teamA.TeamID })
	r1 := mustAcquire(t, g, teamA, Light)
	r2 := mustAcquire(t, g, teamA, Light)
	r3 := mustAcquire(t, g, teamA, Light)
	r1()
	r2()
	r3()
	rb := mustAcquire(t, g, teamB, Light)
	if _, err := g.Acquire(context.Background(), teamB, Light); !IsBusy(err) {
		t.Fatalf("B keeps the default of 1: %v", err)
	}
	rb()

	off := New(Config{Off: true, Team: Limits{Light: 1}})
	for i := 0; i < 5; i++ {
		if _, err := off.Acquire(context.Background(), teamA, Heavy); err != nil {
			t.Fatal(err)
		}
	}
	var nilGate *Gate
	if _, err := nilGate.Acquire(context.Background(), teamA, Heavy); err != nil {
		t.Fatal(err)
	}
}

func TestLaneOf(t *testing.T) {
	for m, want := range map[string]Lane{
		"/admin.AdminService/ExportTechCardArchive": Heavy,
		"/admin.AdminService/UploadContentImage":    Heavy,
		"/admin.AdminService/TranslateFields":       AI,
		"/admin.AdminService/SuggestPatternPieces":  AI,
		"/admin.AdminService/GetTechCard":           Light,
		"/frontend.FrontendService/GetHero":         Light,
		"":                                          Light,
	} {
		if got := LaneOf(m); got != want {
			t.Errorf("%s: %s, want %s", m, got, want)
		}
	}
}

func unaryCall(e *Edge, ctx context.Context, method string, h grpc.UnaryHandler) (any, error) {
	return e.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, h)
}

func TestUnaryEdge(t *testing.T) {
	g := testGate(1, 1, 1)
	g.cfg.Shop = Limits{Light: 1, Rate: 1e6, Burst: 1e6}
	g.cfg.ShopWait = 50 * time.Millisecond
	g.cfg.Checkout = Limits{Light: 1, Rate: 1e6, Burst: 1e6}
	g.cfg.CheckoutWait = 50 * time.Millisecond
	isShop := func(m string) bool { return strings.HasPrefix(m, "/frontend.FrontendService/") }
	e := NewEdge(g, isShop, func(m string) bool { return m == "/frontend.FrontendService/SubmitOrder" })
	team := tenant.Team{ID: 2, Slug: "a"}
	ctx := tenant.With(context.Background(), team)

	hold := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _ = unaryCall(e, ctx, "/admin.AdminService/GetTechCard", func(context.Context, any) (any, error) {
			close(started)
			<-hold
			return nil, nil
		})
	}()
	<-started
	// Same team, same share: refused with the machine word.
	_, err := unaryCall(e, ctx, "/admin.AdminService/ListTasks", func(context.Context, any) (any, error) { return "ok", nil })
	if st, _ := status.FromError(err); st.Code() != codes.ResourceExhausted || st.Message() != BusyCode {
		t.Fatalf("got %v", err)
	}
	// The shop of the same team (its own share) and a call with no team pass.
	if _, err := unaryCall(e, ctx, "/frontend.FrontendService/GetHero", func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	// The shop's own share used up: refused with the shop's machine word, the admin share untouched.
	shopHeld := mustAcquire(t, g, Key{TeamID: 2}, Shop)
	_, err = unaryCall(e, ctx, "/frontend.FrontendService/GetHero", func(context.Context, any) (any, error) { return nil, nil })
	if st, _ := status.FromError(err); st.Code() != codes.ResourceExhausted || st.Message() != ShopBusyCode {
		t.Fatalf("shop busy: %v", err)
	}
	// A checkout method takes its own lane: it runs with the shop's share and the admin share both
	// used up, and is refused (shop_busy) only once the checkout share itself is used up.
	ran := false
	if _, err := unaryCall(e, ctx, "/frontend.FrontendService/SubmitOrder", func(context.Context, any) (any, error) { ran = true; return nil, nil }); err != nil || !ran {
		t.Fatalf("checkout refused: %v ran=%v", err, ran)
	}
	checkoutHeld := mustAcquire(t, g, Key{TeamID: 2}, Checkout)
	_, err = unaryCall(e, ctx, "/frontend.FrontendService/SubmitOrder", func(context.Context, any) (any, error) { return nil, nil })
	if st, _ := status.FromError(err); st.Code() != codes.ResourceExhausted || st.Message() != ShopBusyCode {
		t.Fatalf("checkout busy: %v", err)
	}
	checkoutHeld()
	shopHeld()
	if _, err := unaryCall(e, context.Background(), "/auth.AuthService/Login", func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	close(hold)
	var nilEdge *Edge
	if _, err := unaryCall(nilEdge, ctx, "/x/Y", func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPEdge(t *testing.T) {
	g := testGate(1, 1, 1)
	e := NewEdge(g, nil, nil)
	ctx := tenant.With(context.Background(), tenant.Team{ID: 2})
	rel := mustAcquire(t, g, Key{TeamID: 2}, Heavy)
	h := e.HTTP(Upload, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/upload", nil).WithContext(ctx))
	if rec.Code != http.StatusTooManyRequests || rec.Body.String() != `{"error":"team_busy"}` {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body.String())
	}
	rel()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/files/upload", nil).WithContext(ctx))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("free: %d", rec.Code)
	}
}

func shopGate(teamShop, allShops int) *Gate {
	return New(Config{
		Team:        Limits{Light: 1, Heavy: 1, AI: 1, Rate: 1e6, Burst: 1e6},
		GlobalHeavy: 1,
		LightWait:   50 * time.Millisecond,
		HeavyWait:   50 * time.Millisecond,
		Shop:        Limits{Light: teamShop, Rate: 1e6, Burst: 1e6},
		GlobalShop:  allShops,
		ShopWait:    50 * time.Millisecond,

		StorefrontTeamID: 1,
	})
}

// A team's shop has a share of its own: its admin share full does not touch it, its shop at its cap
// is refused while another team's shop still runs, and all team shops together stop at GlobalShop.
func TestShopLanePerTeam(t *testing.T) {
	g := shopGate(2, 3)
	admin := mustAcquire(t, g, teamA, Light) // A's admin share is full
	defer admin()
	a1 := mustAcquire(t, g, teamA, Shop)
	a2 := mustAcquire(t, g, teamA, Shop)
	if _, err := g.Acquire(context.Background(), teamA, Shop); !IsBusy(err) {
		t.Fatalf("third shop call of A: %v, want busy", err)
	}
	b1 := mustAcquire(t, g, teamB, Shop) // the third global shop slot
	if _, err := g.Acquire(context.Background(), teamB, Shop); !IsBusy(err) {
		t.Fatalf("all shops full: %v, want busy", err)
	}
	// The storefront team never takes a global shop slot; its default cap is far above a team's.
	for i := 0; i < 5; i++ {
		defer mustAcquire(t, g, Key{TeamID: 1}, Shop)()
	}
	// A refused shop call left no slot behind: freeing one of A's lets B in.
	a1()
	b2 := mustAcquire(t, g, teamB, Shop)
	a2()
	b1()
	b2()
	// The admin share of B is untouched by all of this.
	mustAcquire(t, g, teamB, Light)()
}

// The storefront team's limits apply only when set: then its cap and bucket refuse it too, still
// without any global shop slot.
func TestStorefrontShopLimits(t *testing.T) {
	g := shopGate(1, 1)
	g.cfg.StorefrontShop = Limits{Light: 2}
	sf := Key{TeamID: 1}
	brand := mustAcquire(t, g, teamA, Shop) // the only global shop slot
	defer brand()
	r1 := mustAcquire(t, g, sf, Shop)
	r2 := mustAcquire(t, g, sf, Shop)
	if _, err := g.Acquire(context.Background(), sf, Shop); !IsBusy(err) {
		t.Fatalf("storefront over its cap: %v, want busy", err)
	}
	r1()
	r2()

	g = shopGate(1, 1)
	g.cfg.StorefrontShop = Limits{Rate: 1, Burst: 2}
	mustAcquire(t, g, sf, Shop)()
	mustAcquire(t, g, sf, Shop)()
	if _, err := g.Acquire(context.Background(), sf, Shop); !IsBusy(err) {
		t.Fatalf("storefront over its bucket: %v, want busy", err)
	}
}

func TestShopRateAndOff(t *testing.T) {
	g := shopGate(5, 5)
	g.cfg.Shop = Limits{Light: 5, Rate: 1, Burst: 2}
	mustAcquire(t, g, teamA, Shop)()
	mustAcquire(t, g, teamA, Shop)()
	_, err := g.Acquire(context.Background(), teamA, Shop)
	var be BusyError
	if !errors.As(err, &be) || be.Why != "shop rate" {
		t.Fatalf("over the shop bucket: %v", err)
	}
	mustAcquire(t, g, teamB, Shop)() // another team's bucket is its own

	g = shopGate(1, 1)
	g.cfg.ShopOff = true
	for i := 0; i < 5; i++ {
		defer mustAcquire(t, g, teamA, Shop)()
	}
}

func checkoutGate(teamCheckout, allCheckouts int) *Gate {
	g := shopGate(1, 1)
	g.cfg.Checkout = Limits{Light: teamCheckout, Rate: 1e6, Burst: 1e6}
	g.cfg.CheckoutWait = 50 * time.Millisecond
	g.cfg.StorefrontCheckout = Limits{Light: 2, Rate: 1e6, Burst: 1e6}
	g.checkout = make(chan struct{}, allCheckouts)
	return g
}

// Checkout is a lane of its own: browsing full (the team's shop slot and every global shop slot)
// never refuses it; a team's checkout stops at its own cap, all team shops' checkouts at
// GlobalCheckout; the storefront team never takes a global checkout slot and stops at its own cap.
func TestCheckoutLane(t *testing.T) {
	g := checkoutGate(2, 3)
	browsing := mustAcquire(t, g, teamA, Shop) // A's shop slot and the only global shop slot
	defer browsing()
	if _, err := g.Acquire(context.Background(), teamB, Shop); !IsBusy(err) {
		t.Fatalf("browsing full: %v, want busy", err)
	}
	a1 := mustAcquire(t, g, teamA, Checkout)
	a2 := mustAcquire(t, g, teamA, Checkout)
	_, err := g.Acquire(context.Background(), teamA, Checkout)
	var be BusyError
	if !errors.As(err, &be) || be.Why != "checkout queue" {
		t.Fatalf("third checkout of A: %v, want busy", err)
	}
	b1 := mustAcquire(t, g, teamB, Checkout) // the third global checkout slot
	if _, err := g.Acquire(context.Background(), Key{TeamID: 4}, Checkout); !IsBusy(err) {
		t.Fatalf("all checkouts full: %v, want busy", err)
	}
	sf := Key{TeamID: 1}
	s1 := mustAcquire(t, g, sf, Checkout) // no global slot left, the storefront still checks out
	s2 := mustAcquire(t, g, sf, Checkout)
	if _, err := g.Acquire(context.Background(), sf, Checkout); !IsBusy(err) {
		t.Fatalf("storefront over its checkout cap: %v, want busy", err)
	}
	// A refused checkout left no slot behind: freeing one of A's lets team 4 in.
	a1()
	mustAcquire(t, g, Key{TeamID: 4}, Checkout)()
	a2()
	b1()
	s1()
	s2()

	// The checkout bucket refuses with its own word; TEAM_FAIR_SHOP=off ungates checkout too.
	g = checkoutGate(5, 5)
	g.cfg.Checkout = Limits{Light: 5, Rate: 1, Burst: 1}
	mustAcquire(t, g, teamA, Checkout)()
	if _, err := g.Acquire(context.Background(), teamA, Checkout); !errors.As(err, &be) || be.Why != "checkout rate" {
		t.Fatalf("over the checkout bucket: %v", err)
	}
	g = checkoutGate(1, 1)
	g.cfg.ShopOff = true
	for i := 0; i < 5; i++ {
		defer mustAcquire(t, g, teamA, Checkout)()
	}
}

// New never leaves the storefront team unbounded: a Config without storefront shares gets the
// defaults, and a set field is kept.
func TestStorefrontSharesFilled(t *testing.T) {
	g := New(Config{StorefrontTeamID: 1, StorefrontCheckout: Limits{Light: 7}})
	c, d := g.Config(), Defaults(5)
	if c.StorefrontShop != d.StorefrontShop {
		t.Fatalf("storefront shop %+v, want %+v", c.StorefrontShop, d.StorefrontShop)
	}
	if c.StorefrontCheckout != (Limits{Light: 7, Rate: d.StorefrontCheckout.Rate, Burst: d.StorefrontCheckout.Burst}) {
		t.Fatalf("storefront checkout %+v", c.StorefrontCheckout)
	}
	if c.Checkout != d.Checkout || c.GlobalCheckout != d.GlobalCheckout || c.CheckoutWait != d.CheckoutWait {
		t.Fatalf("checkout %+v %d %s", c.Checkout, c.GlobalCheckout, c.CheckoutWait)
	}
}
