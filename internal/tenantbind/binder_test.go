package tenantbind

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// mapDir is a directory of fixed teams.
type mapDir struct {
	teams       map[int64]tenant.Team
	invalidated []int64
}

func (d *mapDir) ByID(_ context.Context, id int64) (tenant.Team, error) {
	if t, ok := d.teams[id]; ok {
		return t, nil
	}
	return tenant.Team{}, tenant.ErrNotFound
}

func (d *mapDir) BySlug(_ context.Context, slug string) (tenant.Team, error) {
	for _, t := range d.teams {
		if t.Slug == slug {
			return t, nil
		}
	}
	return tenant.Team{}, tenant.ErrNotFound
}

func (d *mapDir) ListActive(context.Context) ([]tenant.Team, error) { return nil, nil }
func (d *mapDir) Invalidate(id int64)                               { d.invalidated = append(d.invalidated, id) }

type fakePlane struct {
	dir       *mapDir
	sessions  map[string]bool
	overrides map[int64]map[string]bool
	operators map[int64]bool
	members   map[string]Membership
	gens      map[string]int64 // handle → credential_gen
	calls     int
	err       error
}

func (p *fakePlane) LegacyCredGen(_ context.Context, handle string) (int64, error) {
	p.calls++
	return p.gens[handle], p.err
}

func (p *fakePlane) Directory() tenant.Directory { return p.dir }
func (p *fakePlane) SessionActive(_ context.Context, sid string) (bool, error) {
	p.calls++
	return p.sessions[sid], p.err
}
func (p *fakePlane) FeatureOverrides(_ context.Context, id int64) (map[string]bool, error) {
	p.calls++
	return p.overrides[id], p.err
}
func (p *fakePlane) TeamSession(_ context.Context, sid string, uid, tid int64) (string, bool, error) {
	p.calls++
	m, ok := p.members[sid]
	if !ok || m.UserID != uid || m.TeamID != tid {
		return "", false, p.err
	}
	return m.Role, true, p.err
}
func (p *fakePlane) OperatorActive(_ context.Context, id int64) (bool, error) {
	p.calls++
	return p.operators[id], p.err
}

var (
	teamOne = tenant.TeamOne("grbpwr_beta")
	acme    = tenant.Team{ID: 7, Slug: "acme", Name: "Acme", DBName: "lkb_t_acme0001", Profile: tenant.ProfileStandard,
		Status: tenant.StatusActive}
)

func newBinder(t *testing.T) *Binder {
	t.Helper()
	b, err := New(teamOne)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func teamsBinder(t *testing.T) (*Binder, *fakePlane) {
	t.Helper()
	b := newBinder(t)
	p := &fakePlane{
		dir:       &mapDir{teams: map[int64]tenant.Team{7: acme}},
		sessions:  map[string]bool{"live": true},
		overrides: map[int64]map[string]bool{},
		operators: map[int64]bool{42: true},
	}
	b.usePlane(p, platformstore.Config{
		BaseDomain:         "Beta.Lekalo.Studio.",
		LegacyAdminOrigins: "https://admin.beta.grbpwr.com, http://localhost:3000",
	})
	return b, p
}

func TestNewNeedsTeamOneWithDB(t *testing.T) {
	if _, err := New(tenant.Team{ID: 2, DBName: "x"}); err == nil {
		t.Fatal("team 2 accepted as team #1")
	}
	if _, err := New(tenant.Team{ID: 1}); err == nil {
		t.Fatal("team #1 without a database accepted")
	}
}

// Teams mode off: everything is team #1, exactly as the single-tenant binary.
func TestModeOffBindsTeamOneEverywhere(t *testing.T) {
	b := newBinder(t)
	if b.TeamsMode() {
		t.Fatal("teams mode on without a control plane")
	}
	for _, c := range []Class{ClassClaim, ClassXTeam, ClassLegacyAuth, ClassStorefront, ClassToken, ClassPlatform,
		ClassHealth, ClassUnknown} {
		ctx, err := b.bindFor(context.Background(), c)
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		got, ok := tenant.From(ctx)
		if !ok || got != teamOne {
			t.Fatalf("%q bound %+v, want team #1", c, got)
		}
	}
	if ok, _ := b.SessionActive(context.Background(), "live"); ok {
		t.Fatal("session active with teams mode off")
	}
	if ok, _ := b.Operator(context.Background(), 42); ok {
		t.Fatal("operator with teams mode off")
	}
	eff, err := b.Features(context.Background(), teamOne)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range feature.Catalog() {
		if !eff[info.Key] {
			t.Fatalf("mode off: feature %s off on team #1", info.Key)
		}
	}
	if _, err := b.TeamByID(context.Background(), 7); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("mode off resolved team 7: %v", err)
	}
	// A nil store keeps teams mode off.
	b.UsePlatform(nil, platformstore.Config{})
	if b.TeamsMode() {
		t.Fatal("UsePlatform(nil) switched teams mode on")
	}
}

func TestTeamsModeBinding(t *testing.T) {
	b, _ := teamsBinder(t)
	ctx := context.Background()
	for _, c := range []Class{ClassClaim, ClassXTeam, ClassPlatform} {
		bound, err := b.bindFor(ctx, c)
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if _, ok := tenant.From(bound); ok {
			t.Fatalf("%q bound a team in teams mode; the chain or handler binds it", c)
		}
	}
	for _, c := range []Class{ClassStorefront, ClassToken, ClassLegacyAuth, ClassHealth} {
		bound, err := b.bindFor(ctx, c)
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if got, _ := tenant.From(bound); got.ID != tenant.TeamOneID {
			t.Fatalf("%q bound team %d, want #1", c, got.ID)
		}
	}
	if _, err := b.bindFor(ctx, ClassUnknown); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unknown class in teams mode: %v", err)
	}
	// Old AuthService calls naming another team are refused.
	md := metadata.NewIncomingContext(ctx, metadata.Pairs("x-team", "acme"))
	if _, err := b.bindFor(md, ClassLegacyAuth); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("legacy auth for acme: %v", err)
	}
	md = metadata.NewIncomingContext(ctx, metadata.Pairs("x-team", "GRBPWR"))
	if _, err := b.bindFor(md, ClassLegacyAuth); err != nil {
		t.Fatalf("legacy auth for team #1: %v", err)
	}
}

func TestTeamOneIsAlwaysTheBootTeam(t *testing.T) {
	b, p := teamsBinder(t)
	// Even when the control plane holds a different row for id 1, team #1 stays the boot team.
	p.dir.teams[1] = tenant.Team{ID: 1, Slug: "grbpwr", DBName: "other", Status: tenant.StatusSuspended}
	got, err := b.TeamByID(context.Background(), 1)
	if err != nil || got != teamOne {
		t.Fatalf("team #1 = %+v, %v", got, err)
	}
	got, err = b.TeamByID(context.Background(), 7)
	if err != nil || got.Slug != "acme" {
		t.Fatalf("team 7 = %+v, %v", got, err)
	}
}

func TestOriginAllowed(t *testing.T) {
	b, _ := teamsBinder(t)
	cases := []struct {
		name   string
		origin string
		team   tenant.Team
		legacy bool
		want   bool
	}{
		{"no browser", "", acme, false, true},
		{"own host", "https://acme.beta.lekalo.studio", acme, false, true},
		{"own host upper case and slash", "HTTPS://ACME.beta.lekalo.studio/", acme, false, true},
		{"another team host", "https://other.beta.lekalo.studio", acme, false, false},
		{"plain http", "http://acme.beta.lekalo.studio", acme, false, false},
		{"suffix attack", "https://acme.beta.lekalo.studio.evil.com", acme, false, false},
		{"legacy origin for another team", "https://admin.beta.grbpwr.com", acme, false, false},
		{"legacy origin team #1", "https://admin.beta.grbpwr.com", teamOne, false, true},
		{"legacy token on legacy origin", "https://admin.beta.grbpwr.com", teamOne, true, true},
		{"dev legacy origin", "http://localhost:3000", teamOne, true, true},
		{"legacy token on team host", "https://grbpwr.beta.lekalo.studio", teamOne, true, false},
		{"new token team #1 on its team host", "https://grbpwr.beta.lekalo.studio", teamOne, false, true},
		{"not an origin", "https://acme.beta.lekalo.studio/path", acme, false, false},
		{"garbage", "::", acme, false, false},
	}
	for _, c := range cases {
		if got := b.OriginAllowed(c.origin, c.team, c.legacy); got != c.want {
			t.Errorf("%s: OriginAllowed(%q) = %v, want %v", c.name, c.origin, got, c.want)
		}
	}
}

// The root host rule of a provider start: exactly https://<base>, and no browser is no pass.
func TestRootOriginAllowed(t *testing.T) {
	b, _ := teamsBinder(t)
	for origin, want := range map[string]bool{
		"https://beta.lekalo.studio":          true,
		"HTTPS://Beta.Lekalo.Studio/":         true,
		"":                                    false,
		"http://beta.lekalo.studio":           false,
		"https://acme.beta.lekalo.studio":     false,
		"https://beta.lekalo.studio.evil.com": false,
		"https://beta.lekalo.studio/auth":     false,
		"https://admin.beta.grbpwr.com":       false,
		"null":                                false,
	} {
		if got := b.RootOriginAllowed(origin); got != want {
			t.Errorf("RootOriginAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
	if newBinder(t).RootOriginAllowed("https://beta.lekalo.studio") {
		t.Error("a binder with no base domain admitted a root origin")
	}
}

func TestCachesAndInvalidate(t *testing.T) {
	b, p := teamsBinder(t)
	ctx := context.Background()
	for range 3 {
		if ok, err := b.SessionActive(ctx, "live"); !ok || err != nil {
			t.Fatalf("live session: %v %v", ok, err)
		}
	}
	if ok, _ := b.SessionActive(ctx, "gone"); ok {
		t.Fatal("unknown session active")
	}
	if ok, _ := b.Operator(ctx, 42); !ok {
		t.Fatal("operator 42 refused")
	}
	if ok, _ := b.Operator(ctx, 43); ok {
		t.Fatal("non-operator accepted")
	}
	if p.calls != 4 {
		t.Fatalf("control plane calls = %d, want 4 (cached)", p.calls)
	}

	p.overrides[7] = map[string]bool{string(feature.Orders): false}
	eff, err := b.Features(ctx, acme)
	if err != nil {
		t.Fatal(err)
	}
	if eff[feature.Orders] {
		t.Fatal("override off ignored")
	}
	p.overrides[7] = map[string]bool{string(feature.Orders): true}
	if eff, _ := b.Features(ctx, acme); eff[feature.Orders] {
		t.Fatal("feature cache not used")
	}
	b.InvalidateTeam(7)
	if eff, _ := b.Features(ctx, acme); !eff[feature.Orders] {
		t.Fatal("InvalidateTeam did not drop the feature cache")
	}
	if len(p.dir.invalidated) == 0 || p.dir.invalidated[len(p.dir.invalidated)-1] != 7 {
		t.Fatalf("directory not invalidated: %v", p.dir.invalidated)
	}

	p.err = errors.New("down")
	if _, err := b.SessionActive(ctx, "other"); err == nil {
		t.Fatal("control-plane error swallowed")
	}
}

func TestStorefrontTeam(t *testing.T) {
	b := newBinder(t)
	p := &fakePlane{dir: &mapDir{teams: map[int64]tenant.Team{7: acme}}}
	b.usePlane(p, platformstore.Config{StorefrontTeamID: 7})
	ctx, err := b.bindFor(context.Background(), ClassStorefront)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := tenant.From(ctx); got.ID != 7 {
		t.Fatalf("storefront team = %d, want 7", got.ID)
	}
	b.usePlane(p, platformstore.Config{StorefrontTeamID: 9})
	if _, err := b.bindFor(context.Background(), ClassStorefront); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing storefront team: %v", err)
	}
}

func TestInterceptorsAndMiddleware(t *testing.T) {
	b, _ := teamsBinder(t)
	var seen tenant.Team
	_, err := b.Unary()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/frontend.FrontendService/GetHero"},
		func(ctx context.Context, _ any) (any, error) { seen, _ = tenant.From(ctx); return nil, nil })
	if err != nil || seen.ID != 1 {
		t.Fatalf("storefront unary: team %d, %v", seen.ID, err)
	}
	_, err = b.Unary()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x.Unknown/Do"},
		func(context.Context, any) (any, error) { t.Fatal("unknown method ran"); return nil, nil })
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("unknown unary: %v", err)
	}

	h := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := tenant.From(r.Context()); ok {
			w.WriteHeader(299)
			return
		}
		w.WriteHeader(204)
	}))
	for path, want := range map[string]int{
		"/api/frontend/hero": 299,
		"/api/p/abc":         299,
		"/api/admin/x":       204,
		"/livez":             299, // health class: team #1 (the /readyz ping needs it)
		"/api/nothing":       http.StatusForbidden,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
}
