package tenantbind

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type shopFakePlane struct {
	*fakePlane
	hosts map[string]int64
	shops map[int64]platformstore.TeamShop
	looks int
}

func (p *shopFakePlane) ShopHostTeam(_ context.Context, host string) (int64, bool, error) {
	p.looks++
	id, ok := p.hosts[host]
	return id, ok, nil
}

func (p *shopFakePlane) TeamShop(_ context.Context, id int64) (platformstore.TeamShop, error) {
	return p.shops[id], nil
}

var bolt = tenant.Team{ID: 9, Slug: "bolt", Name: "Bolt", DBName: "lkb_t_bolt0001", Profile: tenant.ProfileAll,
	Status: tenant.StatusActive}

func shopBinder(t *testing.T) (*Binder, *shopFakePlane) {
	t.Helper()
	b := newBinder(t)
	p := &shopFakePlane{
		fakePlane: &fakePlane{
			dir:       &mapDir{teams: map[int64]tenant.Team{1: teamOne, 7: acme, 9: bolt}},
			overrides: map[int64]map[string]bool{7: {"products": true, "orders": true}},
		},
		hosts: map[string]int64{"acme.shop.lekalo.studio": 7, "acme.com": 7, "bolt.shop.lekalo.studio": 9},
		shops: map[int64]platformstore.TeamShop{7: {TeamID: 7, Enabled: true}, 9: {TeamID: 9, Enabled: true}},
	}
	b.usePlane(p, platformstore.Config{BaseDomain: "lekalo.studio"})
	b.SetStorefrontOrigins([]string{"https://grbpwr.com", "http://localhost:3000"})
	return b, p
}

func TestShopTeamResolution(t *testing.T) {
	b, _ := shopBinder(t)
	ctx := context.Background()
	cases := []struct {
		name, host, origin string
		want               int64
		legacy             bool
		code               codes.Code
	}{
		{"no header no origin = storefront team", "", "", 1, true, codes.OK},
		{"grbpwr origin = storefront team", "", "https://grbpwr.com", 1, true, codes.OK},
		{"unknown origin = storefront team", "", "https://elsewhere.example", 1, true, codes.OK},
		{"header names a shop", "acme.shop.lekalo.studio", "", 7, false, codes.OK},
		{"header with port and case", "ACME.com:443", "", 7, false, codes.OK},
		{"origin names a shop", "", "https://acme.com", 7, false, codes.OK},
		{"header and origin agree", "acme.com", "https://acme.shop.lekalo.studio", 7, false, codes.OK},
		{"dev origin, header wins", "bolt.shop.lekalo.studio", "http://localhost:3000", 9, false, codes.OK},
		{"header names the storefront's own host", "grbpwr.com", "", 1, true, codes.OK},
		{"unknown header", "nope.shop.lekalo.studio", "", 0, false, codes.NotFound},
		{"header and origin disagree", "acme.com", "https://bolt.shop.lekalo.studio", 0, false, codes.PermissionDenied},
		{"malformed header", "acme_com", "", 0, false, codes.NotFound},
		{"malformed header with a path", "acme.com/x", "", 0, false, codes.NotFound},
		{"malformed origin names no shop", "", "https://acme_com", 1, true, codes.OK},
	}
	for _, c := range cases {
		got, legacy, err := b.ShopTeam(ctx, c.host, c.origin)
		if status.Code(err) != c.code {
			t.Fatalf("%s: err %v, want %v", c.name, err, c.code)
		}
		if c.code != codes.OK {
			continue
		}
		if got.ID != c.want || legacy != c.legacy {
			t.Fatalf("%s: team %d legacy %v, want %d %v", c.name, got.ID, legacy, c.want, c.legacy)
		}
	}
}

func TestShopTeamModeOffIsTeamOne(t *testing.T) {
	b := newBinder(t)
	got, legacy, err := b.ShopTeam(context.Background(), "acme.com", "https://acme.com")
	if err != nil || got.ID != tenant.TeamOneID || !legacy {
		t.Fatalf("mode off: %+v %v %v", got, legacy, err)
	}
}

func TestShopOpen(t *testing.T) {
	b, p := shopBinder(t)
	ctx := context.Background()
	if ok, _ := b.ShopOpen(ctx, acme); !ok {
		t.Fatal("acme: on, features on — want open")
	}
	// Bolt's profile is all; acme's standard profile needs the overrides above.
	p.overrides[7] = map[string]bool{"products": true, "orders": false}
	b.InvalidateTeam(7)
	if ok, _ := b.ShopOpen(ctx, acme); ok {
		t.Fatal("acme with orders off is open")
	}
	p.shops[9] = platformstore.TeamShop{TeamID: 9, Enabled: true, ClosedByOperator: true}
	b.InvalidateShop(9)
	if ok, _ := b.ShopOpen(ctx, bolt); ok {
		t.Fatal("bolt closed by the operator is open")
	}
	p.shops[9] = platformstore.TeamShop{TeamID: 9}
	b.InvalidateShop(9)
	if ok, _ := b.ShopOpen(ctx, bolt); ok {
		t.Fatal("bolt switched off is open")
	}
	susp := bolt
	susp.Status = tenant.StatusSuspended
	p.shops[9] = platformstore.TeamShop{TeamID: 9, Enabled: true}
	b.InvalidateShop(9)
	if ok, _ := b.ShopOpen(ctx, susp); ok {
		t.Fatal("a suspended team's shop is open")
	}
	if ok, _ := b.ShopOpen(ctx, teamOne); !ok {
		t.Fatal("the storefront team is always open")
	}
}

func TestShopHostCache(t *testing.T) {
	b, p := shopBinder(t)
	ctx := context.Background()
	for range 3 {
		if _, _, err := b.ShopTeam(ctx, "acme.com", ""); err != nil {
			t.Fatal(err)
		}
	}
	if p.looks != 1 {
		t.Fatalf("lookups = %d, want 1 (cached)", p.looks)
	}
	b.InvalidateShop(7)
	if _, _, err := b.ShopTeam(ctx, "acme.com", ""); err != nil {
		t.Fatal(err)
	}
	if p.looks != 2 {
		t.Fatalf("lookups after invalidate = %d, want 2", p.looks)
	}
}

func TestUnaryStorefrontBindsShop(t *testing.T) {
	b, p := shopBinder(t)
	var got tenant.Team
	h := func(ctx context.Context, _ any) (any, error) {
		got, _ = tenant.From(ctx)
		return nil, nil
	}
	call := func(method, host string) error {
		md := metadata.Pairs(ShopHostMD, host, "grpcgateway-origin", "https://"+host)
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := b.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, h)
		return err
	}
	if err := call("/frontend.FrontendService/GetHero", "acme.com"); err != nil || got.ID != 7 {
		t.Fatalf("acme hero: %v, team %d", err, got.ID)
	}
	p.shops[7] = platformstore.TeamShop{TeamID: 7}
	b.InvalidateShop(7)
	err := call("/frontend.FrontendService/GetHero", "acme.com")
	if status.Code(err) != codes.NotFound || status.Convert(err).Message() != MsgShopClosed {
		t.Fatalf("closed shop hero: %v", err)
	}
	if err := call(shopProfileMethod, "acme.com"); err != nil || got.ID != 7 {
		t.Fatalf("closed shop profile: %v, team %d", err, got.ID)
	}
	// No shop named: today's storefront.
	_, err = b.Unary()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/frontend.FrontendService/GetHero"}, h)
	if err != nil || got.ID != tenant.TeamOneID {
		t.Fatalf("legacy hero: %v, team %d", err, got.ID)
	}
}

func TestMiddlewareStorefrontRefusals(t *testing.T) {
	b, _ := shopBinder(t)
	var got tenant.Team
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got, _ = tenant.From(r.Context()) })
	do := func(path, host, origin string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if host != "" {
			r.Header.Set(ShopHostHeader, host)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		b.Middleware(next).ServeHTTP(w, r)
		return w.Code
	}
	if c := do("/api/frontend/hero", "acme.com", ""); c != http.StatusOK || got.ID != 7 {
		t.Fatalf("acme: %d team %d", c, got.ID)
	}
	if c := do("/api/frontend/hero", "nope.example", ""); c != http.StatusNotFound {
		t.Fatalf("unknown shop: %d", c)
	}
	if c := do("/api/frontend/hero", "acme.com", "https://bolt.shop.lekalo.studio"); c != http.StatusForbidden {
		t.Fatalf("mismatch: %d", c)
	}
	// Webhooks stay the storefront team's whatever they carry.
	if c := do("/api/webhooks/stripe", "acme.com", ""); c != http.StatusOK || got.ID != tenant.TeamOneID {
		t.Fatalf("webhook: %d team %d", c, got.ID)
	}
}

// Malformed hosts never reach the control plane; concurrent lookups of one host fold into one.
func TestShopHostLookupGuards(t *testing.T) {
	b, p := shopBinder(t)
	ctx := context.Background()
	for _, h := range []string{"a_b.com", "x..com", "1.2.3.4", strings.Repeat("a", 254)} {
		_, _, _ = b.ShopTeam(ctx, h, "")
	}
	if p.looks != 0 {
		t.Fatalf("malformed hosts looked up %d times", p.looks)
	}
	slow := &slowShopPlane{shopFakePlane: p, release: make(chan struct{})}
	b.usePlane(slow, platformstore.Config{BaseDomain: "lekalo.studio"})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, _, err := b.ShopTeam(ctx, "acme.com", ""); err != nil || got.ID != 7 {
				t.Errorf("team %d %v", got.ID, err)
			}
		}()
	}
	for slow.n.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(slow.release)
	wg.Wait()
	if n := slow.n.Load(); n != 1 {
		t.Fatalf("concurrent lookups = %d, want 1", n)
	}
}

type slowShopPlane struct {
	*shopFakePlane
	n       atomic.Int32
	release chan struct{}
}

func (p *slowShopPlane) ShopHostTeam(_ context.Context, host string) (int64, bool, error) {
	p.n.Add(1)
	<-p.release
	id, ok := p.hosts[host]
	return id, ok, nil
}
