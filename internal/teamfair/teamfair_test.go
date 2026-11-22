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
	for _, c := range []struct{ pool, light, gh int }{{5, 3, 2}, {15, 8, 7}, {1, 2, 1}, {0, 2, 1}, {2, 2, 1}} {
		d := Defaults(c.pool)
		if d.Team.Light != c.light || d.GlobalHeavy != c.gh || d.Team.Heavy != 1 {
			t.Errorf("pool %d: light %d heavy-all %d heavy %d", c.pool, d.Team.Light, d.GlobalHeavy, d.Team.Heavy)
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

// Storefront calls are never gated: 50 concurrent slow storefront calls of team #1, while its admin
// share is full and the gate is tiny, never get ResourceExhausted.
func TestStorefrontNeverRefused(t *testing.T) {
	g := New(Config{Team: Limits{Light: 1, Heavy: 1, AI: 1, Rate: 1, Burst: 1},
		GlobalHeavy: 1, LightWait: 10 * time.Millisecond, HeavyWait: 10 * time.Millisecond})
	shop := func(m string) bool { return strings.HasPrefix(m, "/frontend.FrontendService/") }
	e := NewEdge(g, shop)
	ctx := tenant.With(context.Background(), tenant.Team{ID: 1})
	r := mustAcquire(t, g, Key{TeamID: 1}, Light) // team #1's admin share is full
	defer r()
	var wg sync.WaitGroup
	var refused atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := unaryCall(e, ctx, "/frontend.FrontendService/SubmitOrder", func(context.Context, any) (any, error) {
				time.Sleep(50 * time.Millisecond) // a Stripe wait
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
	e := NewEdge(g, func(m string) bool { return m == "/frontend.FrontendService/SubmitOrder" })
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
	// The shop of the same team (ungated) and a call with no team pass.
	if _, err := unaryCall(e, ctx, "/frontend.FrontendService/SubmitOrder", func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
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
	e := NewEdge(g, nil)
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
