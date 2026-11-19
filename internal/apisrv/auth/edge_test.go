package auth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeEdge is a teams-mode control plane with fixed teams. Its origin policy is the binder's
// (team host https://<slug>.lekalo.studio; the legacy admin origin for team #1 only; a legacy
// token never on a team host) — tenantbind tests pin the real one.
type fakeEdge struct {
	teams    map[int64]tenant.Team
	features map[int64]map[feature.Key]bool
	sessions map[string]bool
	ops      map[int64]bool
	removed  map[int64]bool   // uid → membership removed / user disabled
	roles    map[int64]string // uid → membership role (default owner)
	gens     map[string]int64 // handle → credential generation
}

const (
	legacyOrigin = "https://admin.grbpwr.com"
	acmeOrigin   = "https://acme.lekalo.studio"
)

func (e *fakeEdge) TeamsMode() bool               { return true }
func (e *fakeEdge) TeamOne() tenant.Team          { return e.teams[1] }
func (e *fakeEdge) SchemaBehind(tenant.Team) bool { return false }
func (e *fakeEdge) TeamByID(_ context.Context, id int64) (tenant.Team, error) {
	if t, ok := e.teams[id]; ok {
		return t, nil
	}
	return tenant.Team{}, tenant.ErrNotFound
}
func (e *fakeEdge) OriginAllowed(origin string, t tenant.Team, legacy bool) bool {
	if origin == "" {
		return true
	}
	if t.ID == 1 && origin == legacyOrigin {
		return true
	}
	return !legacy && origin == "https://"+t.Slug+".lekalo.studio"
}
func (e *fakeEdge) SessionActive(_ context.Context, sid string) (bool, error) {
	return e.sessions[sid], nil
}
func (e *fakeEdge) Features(_ context.Context, t tenant.Team) (map[feature.Key]bool, error) {
	if f, ok := e.features[t.ID]; ok {
		return f, nil
	}
	return feature.Effective(t.ID, t.Profile, nil), nil
}
func (e *fakeEdge) TeamSession(_ context.Context, sid string, uid, tid int64) (string, bool, error) {
	if !e.sessions[sid] || e.removed[uid] {
		return "", false, nil
	}
	if _, ok := e.teams[tid]; !ok {
		return "", false, nil
	}
	if r, ok := e.roles[uid]; ok {
		return r, true, nil
	}
	return "owner", true, nil
}

// RequestedTeam mimics the binder: <slug>.lekalo.studio or X-Team names a team; both must agree.
func (e *fakeEdge) RequestedTeam(_ context.Context, host, xteam string) (tenant.Team, bool, error) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	slug := strings.TrimSuffix(strings.ToLower(host), ".lekalo.studio")
	if slug == strings.ToLower(host) || slug == "api" {
		slug = ""
	}
	x := strings.ToLower(strings.TrimSpace(xteam))
	if slug != "" && x != "" && slug != x {
		return tenant.Team{}, false, status.Error(codes.PermissionDenied, msgTeamMismatch)
	}
	if slug == "" {
		slug = x
	}
	if slug == "" {
		return tenant.Team{}, false, nil
	}
	for _, t := range e.teams {
		if t.Slug == slug {
			return t, true, nil
		}
	}
	return tenant.Team{}, false, status.Error(codes.PermissionDenied, msgTeamMismatch)
}

func (e *fakeEdge) Operator(_ context.Context, id int64) (bool, error) { return e.ops[id], nil }
func (e *fakeEdge) LegacyCredGen(_ context.Context, h string, _ int64) (int64, error) {
	return e.gens[h], nil
}

var (
	edgeTeamOne = tenant.TeamOne("grbpwr_beta")
	edgeAcme    = tenant.Team{ID: 7, Slug: "acme", Name: "Acme", DBName: "lkb_t_acme0001",
		Profile: tenant.ProfileAll, Status: tenant.StatusActive}
)

func newEdge() *fakeEdge {
	return &fakeEdge{
		teams:    map[int64]tenant.Team{1: edgeTeamOne, 7: edgeAcme},
		features: map[int64]map[feature.Key]bool{},
		sessions: map[string]bool{"live": true},
		ops:      map[int64]bool{42: true},
		removed:  map[int64]bool{},
		roles:    map[int64]string{},
	}
}

func newEdgeServer(t *testing.T, e Edge) *Server {
	t.Helper()
	as := mocks.NewMockAdmin(t)
	as.EXPECT().GetAdminByUsername(mock.Anything, mock.Anything).Return(&entity.Admin{Id: 1}, nil).Maybe()
	srv, err := New(&Config{
		JWTSecret:                jwtSecret,
		MasterPassword:           masterPassword,
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 100000,
		JWTTTL:                   "60m",
	}, as)
	if err != nil {
		t.Fatal(err)
	}
	if e != nil {
		srv.SetEdge(e)
	}
	return srv
}

func teamToken(t *testing.T, srv *Server, c authjwt.TeamClaims) string {
	t.Helper()
	if c.Handle == "" {
		c.Handle = "u"
	}
	if c.UserID == 0 {
		c.UserID = 5
	}
	if c.SessionID == "" {
		c.SessionID = "live"
	}
	tok, err := authjwt.NewTeamTokenAt(srv.JwtAuth, time.Hour, c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func legacyToken(t *testing.T, srv *Server) string {
	t.Helper()
	tok, err := authjwt.NewAdminToken(srv.JwtAuth, time.Hour, "u", true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// callAdmin runs one gRPC call through the interceptor as the grpc-gateway loopback hop sends it.
func callAdmin(srv *Server, method, token, origin string) (context.Context, error) {
	md := metadata.New(map[string]string{AuthMetadataKey: "Bearer " + token})
	if origin != "" {
		md.Set("grpcgateway-origin", origin)
	}
	ctx := metadata.NewIncomingContext(context.Background(), md)
	var got context.Context
	_, err := srv.UnaryAdminAuthInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, _ any) (any, error) { got = ctx; return nil, nil })
	return got, err
}

const (
	readMethod  = "/admin.AdminService/ListOrders"
	writeMethod = "/admin.AdminService/UpdateColorway"
)

func adminMethods() []string {
	d := pb_admin.AdminService_ServiceDesc
	out := make([]string, 0, len(d.Methods)+len(d.Streams))
	for _, m := range d.Methods {
		out = append(out, "/"+d.ServiceName+"/"+m.MethodName)
	}
	for _, s := range d.Streams {
		out = append(out, "/"+d.ServiceName+"/"+s.StreamName)
	}
	return out
}

func TestEdgeTeamTokenOnItsHost(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	ctx, err := callAdmin(srv, readMethod, tok, acmeOrigin)
	if err != nil {
		t.Fatalf("team call refused: %v", err)
	}
	team, ok := tenant.From(ctx)
	if !ok || team.ID != 7 {
		t.Fatalf("team in ctx = %+v", team)
	}
	w, ok := GetWorkspace(ctx)
	if !ok || w.Team.Slug != "acme" || w.Role != "owner" || w.ReadOnly || len(w.Features) == 0 {
		t.Fatalf("workspace = %+v", w)
	}
}

func TestEdgeRefusals(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	acmeTok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	cases := []struct {
		name   string
		token  string
		origin string
		method string
		code   codes.Code
	}{
		{"wrong origin", acmeTok, "https://other.lekalo.studio", readMethod, codes.Unauthenticated},
		{"team token on the legacy origin", acmeTok, legacyOrigin, readMethod, codes.Unauthenticated},
		{"legacy token on a team host", legacyToken(t, srv), "https://grbpwr.lekalo.studio", readMethod, codes.Unauthenticated},
		{"dead session", teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true, SessionID: "gone"}),
			acmeOrigin, readMethod, codes.Unauthenticated},
		{"console token on the admin API", teamToken(t, srv, authjwt.TeamClaims{Audience: authjwt.AudConsole, Super: true}),
			acmeOrigin, readMethod, codes.Unauthenticated},
		{"unknown team", teamToken(t, srv, authjwt.TeamClaims{TeamID: 99, Audience: authjwt.AudTeam, Super: true}),
			"https://x.lekalo.studio", readMethod, codes.Unauthenticated},
		{"no permission", teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Perms: []string{"content:read"}}),
			acmeOrigin, readMethod, codes.PermissionDenied},
	}
	for _, c := range cases {
		if _, err := callAdmin(srv, c.method, c.token, c.origin); status.Code(err) != c.code {
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}

	// Statuses (teams §7).
	for st, want := range map[tenant.Status]codes.Code{
		tenant.StatusDeleted:      codes.Unauthenticated,
		tenant.StatusPending:      codes.Unavailable,
		tenant.StatusProvisioning: codes.Unavailable,
	} {
		a := edgeAcme
		a.Status = st
		e.teams[7] = a
		if _, err := callAdmin(srv, readMethod, acmeTok, acmeOrigin); status.Code(err) != want {
			t.Errorf("status %s: %v, want %s", st, err, want)
		}
	}
	e.teams[7] = edgeAcme
}

func TestEdgeFeatureOff(t *testing.T) {
	e := newEdge()
	eff := feature.Effective(7, tenant.ProfileAll, nil)
	eff[feature.Orders] = false
	e.features[7] = eff
	srv := newEdgeServer(t, e)
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	_, err := callAdmin(srv, readMethod, tok, acmeOrigin)
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied || st.Message() != msgFeatureOff {
		t.Fatalf("orders off: %v", err)
	}
	// Core calls (the allowlist) still pass.
	if _, err := callAdmin(srv, "/admin.AdminService/GetCurrentAccount", tok, acmeOrigin); err != nil {
		t.Fatalf("core call refused with orders off: %v", err)
	}
	ctx, err := callAdmin(srv, writeMethod, tok, acmeOrigin)
	if err != nil {
		t.Fatalf("products call refused with orders off: %v", err)
	}
	w, _ := GetWorkspace(ctx)
	for _, k := range w.Features {
		if k == feature.Orders {
			t.Fatal("workspace lists a feature that is off")
		}
	}
}

// Suspended (teams §7): every write method is refused with team_read_only, every read passes.
func TestEdgeSuspendedTeamIsReadOnly(t *testing.T) {
	e := newEdge()
	a := edgeAcme
	a.Status = tenant.StatusSuspended
	e.teams[7] = a
	srv := newEdgeServer(t, e)
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	reads, writes := 0, 0
	for _, m := range adminMethods() {
		access, _ := rbac.Access(m)
		ctx, err := callAdmin(srv, m, tok, acmeOrigin)
		if access == entity.AccessRead {
			reads++
			if err != nil {
				t.Errorf("read %s refused on a suspended team: %v", m, err)
				continue
			}
			if w, _ := GetWorkspace(ctx); !w.ReadOnly {
				t.Errorf("%s: workspace not read-only", m)
			}
			continue
		}
		writes++
		if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != msgTeamReadOnly {
			t.Errorf("write %s on a suspended team: %v", m, err)
		}
	}
	if reads == 0 || writes == 0 {
		t.Fatalf("reads=%d writes=%d: the method walk is empty", reads, writes)
	}
}

// The upload routes go through the same chain on the chi route pattern.
func TestEdgeUploadRoutes(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})

	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusOK) })
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Method(http.MethodPost, "/files/upload", srv.WithAdminAuthz(next))
		r.Method(http.MethodPost, "/files/{id}/preview", srv.WithAdminAuthz(next))
	})
	post := func(path string) (int, string) {
		reached = false
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set(AuthMetadataKey, "Bearer "+tok)
		req.Header.Set("Origin", acmeOrigin)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	for _, p := range []string{"/api/files/upload", "/api/files/abc/preview"} {
		if code, body := post(p); code != http.StatusOK || !reached {
			t.Fatalf("%s on an active team: %d %s", p, code, body)
		}
	}

	a := edgeAcme
	a.Status = tenant.StatusSuspended
	e.teams[7] = a
	for _, p := range []string{"/api/files/upload", "/api/files/abc/preview"} {
		code, body := post(p)
		if code != http.StatusForbidden || !strings.Contains(body, msgTeamReadOnly) || reached {
			t.Fatalf("%s on a suspended team: %d %s", p, code, body)
		}
	}

	e.teams[7] = edgeAcme
	eff := feature.Effective(7, tenant.ProfileAll, nil)
	eff[feature.Files] = false
	e.features[7] = eff
	code, body := post("/api/files/abc/preview")
	if code != http.StatusForbidden || !strings.Contains(body, msgFeatureOff) || reached {
		t.Fatalf("preview with files off: %d %s", code, body)
	}
}

// The file proxy is a read behind the same chain: no token is refused, a foreign team is refused, a
// suspended team still reads (GET), and it is not gated by the files feature (Core).
func TestEdgeFileProxyRoute(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})

	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusOK) })
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Method(http.MethodGet, "/files/proxy", srv.WithAdminAuthz(next))
	})
	get := func(token, xteam string) (int, string) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/api/files/proxy?url=https%3A%2F%2Fcdn.example%2Fa", nil)
		if token != "" {
			req.Header.Set(AuthMetadataKey, "Bearer "+token)
		}
		if xteam != "" {
			req.Header.Set("X-Team", xteam)
		}
		req.Header.Set("Origin", acmeOrigin)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := get("", ""); code != http.StatusUnauthorized || reached {
		t.Fatalf("no token: %d %s", code, body)
	}
	if code, body := get(tok, ""); code != http.StatusOK || !reached {
		t.Fatalf("own team: %d %s", code, body)
	}
	if code, body := get(tok, "grbpwr"); code != http.StatusForbidden || reached {
		t.Fatalf("foreign X-Team: %d %s", code, body)
	}

	a := edgeAcme
	a.Status = tenant.StatusSuspended
	e.teams[7] = a
	if code, body := get(tok, ""); code != http.StatusOK || !reached {
		t.Fatalf("suspended team read: %d %s", code, body)
	}

	e.teams[7] = edgeAcme
	eff := feature.Effective(7, tenant.ProfileAll, nil)
	eff[feature.Files] = false
	e.features[7] = eff
	if code, body := get(tok, ""); code != http.StatusOK || !reached {
		t.Fatalf("files off still reads a url it holds: %d %s", code, body)
	}
}

// Team #1 with teams mode off is today's admin: legacy token, every feature, no origin lock.
func TestEdgeTeamOneUnchangedModeOff(t *testing.T) {
	// edge_ext_test.go runs the same with the real binder (tenantbind imports store, which imports
	// this package, so it cannot be imported here).
	for name, e := range map[string]Edge{"no edge": nil} {
		srv := newEdgeServer(t, e)
		ctx, err := callAdmin(srv, writeMethod, legacyToken(t, srv), "https://anything.example")
		if err != nil {
			t.Fatalf("%s: legacy call refused: %v", name, err)
		}
		w, ok := GetWorkspace(ctx)
		if !ok || w.Team.ID != 1 || w.Team.Slug != "grbpwr" || w.Role != "owner" || w.ReadOnly {
			t.Fatalf("%s: workspace %+v", name, w)
		}
		if len(w.Features) != len(feature.Catalog()) {
			t.Fatalf("%s: %d features on, want all %d", name, len(w.Features), len(feature.Catalog()))
		}
		// A platform token is refused while teams mode is off.
		tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 1, Audience: authjwt.AudTeam, Super: true})
		if _, err := callAdmin(srv, readMethod, tok, ""); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("%s: team token with teams mode off: %v", name, err)
		}
		if _, err := callAdmin(srv, "/admin.PlatformService/ListTeams", tok, ""); status.Code(err) != codes.Unimplemented {
			t.Fatalf("%s: platform call with teams mode off: %v", name, err)
		}
	}
}

// Team #1 in teams mode: the legacy token still works from the legacy origin.
func TestEdgeTeamOneLegacyInTeamsMode(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	ctx, err := callAdmin(srv, writeMethod, legacyToken(t, srv), legacyOrigin)
	if err != nil {
		t.Fatalf("legacy call from the legacy origin refused: %v", err)
	}
	if team, _ := tenant.From(ctx); team.ID != 1 || team.DBName != "grbpwr_beta" {
		t.Fatalf("team = %+v", team)
	}
}

// Team #1 with a stored profile standard (TENANT_TEAM1_PROFILE=production): the edge gates it on
// the set the binder computes from that row, never on the boot-time profile all.
func TestEdgeTeamOneStandardProfileGatesPromo(t *testing.T) {
	e := newEdge()
	e.features[1] = feature.Effective(1, tenant.ProfileStandard, map[string]bool{
		string(feature.Products): true, string(feature.AIProviders): true, string(feature.LocalAccounts): true,
	})
	srv := newEdgeServer(t, e)
	tok := legacyToken(t, srv)
	_, err := callAdmin(srv, "/admin.AdminService/ListPromos", tok, legacyOrigin)
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied || st.Message() != msgFeatureOff {
		t.Fatalf("promo for team #1/standard: %v", err)
	}
	ctx, err := callAdmin(srv, writeMethod, tok, legacyOrigin)
	if err != nil {
		t.Fatalf("products call refused for team #1/standard: %v", err)
	}
	w, _ := GetWorkspace(ctx)
	for _, k := range w.Features {
		if k == feature.Promo || k == feature.Orders {
			t.Fatalf("workspace of team #1/standard lists %s", k)
		}
	}
}

func TestEdgePlatformChain(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	const m = "/admin.PlatformService/ListTeams"
	op := teamToken(t, srv, authjwt.TeamClaims{Audience: authjwt.AudConsole, UserID: 42})
	ctx, err := callAdmin(srv, m, op, "")
	if err != nil {
		t.Fatalf("operator refused: %v", err)
	}
	if c, ok := GetPlatformCaller(ctx); !ok || c.UserID != 42 {
		t.Fatalf("platform caller = %+v", c)
	}
	if _, ok := tenant.From(ctx); ok {
		t.Fatal("a team is bound on a platform call")
	}
	for name, tok := range map[string]string{
		"non-operator": teamToken(t, srv, authjwt.TeamClaims{Audience: authjwt.AudConsole, UserID: 43}),
		"team token":   teamToken(t, srv, authjwt.TeamClaims{Audience: authjwt.AudTeam, TeamID: 7, UserID: 42}),
		"legacy token": legacyToken(t, srv),
		"dead session": teamToken(t, srv, authjwt.TeamClaims{Audience: authjwt.AudConsole, UserID: 42, SessionID: "gone"}),
	} {
		if _, err := callAdmin(srv, m, tok, ""); err == nil {
			t.Errorf("%s passed the platform chain", name)
		}
	}
}
