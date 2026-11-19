package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// withOp re-signs tok with the operator claim set to v (true is what operator entry mints).
func withOp(t *testing.T, srv *Server, tok string, v any) string {
	t.Helper()
	parsed, err := srv.JwtAuth.Decode(tok)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := parsed.AsMap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	claims[claimOperator] = v
	_, out, err := srv.JwtAuth.Encode(claims)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// opToken is what ExchangeHandoff mints for an operator: subject lekalo, super, op, uid 42 (an
// operator in the fake edge), session "live", team acme.
func opToken(t *testing.T, srv *Server, sid string, uid int64) string {
	t.Helper()
	tok, err := authjwt.NewTeamTokenAt(srv.JwtAuth, time.Hour, authjwt.TeamClaims{
		Handle: "lekalo", UserID: uid, TeamID: 7, SessionID: sid, Audience: authjwt.AudTeam, Super: true, Operator: true,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestEdgeOperatorEntryAccepted(t *testing.T) {
	e := newEdge()
	// The operator is no member of acme: the membership tuple must not be what lets them in.
	e.removed[42] = true
	srv := newEdgeServer(t, e)
	tok := opToken(t, srv, "live", 42)
	for _, m := range []string{readMethod, writeMethod, "/admin.AdminService/ListTeamMembers"} {
		ctx, err := callAdmin(srv, m, tok, acmeOrigin)
		if err != nil {
			t.Fatalf("%s: operator entry refused: %v", m, err)
		}
		w, ok := GetWorkspace(ctx)
		if !ok || !w.Operator || w.Role != "owner" || w.Team.ID != 7 {
			t.Fatalf("%s: workspace %+v", m, w)
		}
		if u := GetAdminUsername(ctx); u != "lekalo" {
			t.Fatalf("%s: actor %q, want lekalo", m, u)
		}
	}
	// A member's token is not marked as an operator.
	ctx, err := callAdmin(srv, readMethod, teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true}), acmeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := GetWorkspace(ctx); w.Operator {
		t.Fatal("member token marked as operator")
	}
}

func TestEdgeOperatorEntryRefused(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	team := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	cases := []struct {
		name, token, origin string
	}{
		{"not an operator", opToken(t, srv, "live", 5), acmeOrigin},
		{"session revoked", opToken(t, srv, "dead", 42), acmeOrigin},
		{"op claim not true", withOp(t, srv, team, "yes"), acmeOrigin},
		{"op claim false", withOp(t, srv, team, false), acmeOrigin},
		{"legacy token with op", withOp(t, srv, legacyToken(t, srv), true), legacyOrigin},
	}
	for _, c := range cases {
		for _, m := range []string{readMethod, writeMethod} {
			if _, err := callAdmin(srv, m, c.token, c.origin); status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s %s: %v, want Unauthenticated", c.name, m, err)
			}
		}
	}
	// An operator who lost the flag is refused on the next call.
	tok := opToken(t, srv, "live", 42)
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); err != nil {
		t.Fatal(err)
	}
	e.ops[42] = false
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("ex-operator: %v, want Unauthenticated", err)
	}
}

func TestEdgeOperatorEntrySuspendedReadOnly(t *testing.T) {
	e := newEdge()
	acme := e.teams[7]
	acme.Status = tenant.StatusSuspended
	e.teams[7] = acme
	srv := newEdgeServer(t, e)
	tok := opToken(t, srv, "live", 42)
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); err != nil {
		t.Fatalf("read on a suspended team: %v", err)
	}
	if _, err := callAdmin(srv, writeMethod, tok, acmeOrigin); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("write on a suspended team: %v, want FailedPrecondition", err)
	}
}

func TestEdgeOperatorEntryOnHTTP(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	run := func(tok string) map[string]int {
		out := map[string]int{}
		for name, h := range map[string]http.Handler{
			"upload chain": srv.WithAdminAuthz(next),
			"WithAuth":     srv.WithAuth(next),
		} {
			reached = false
			req := httptest.NewRequest(http.MethodPost, "/api/files/upload", nil)
			req.Header.Set(AuthMetadataKey, "Bearer "+tok)
			req.Header.Set("Origin", acmeOrigin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if !reached {
				out[name] = rec.Code
			}
		}
		return out
	}
	if refused := run(opToken(t, srv, "live", 42)); len(refused) != 0 {
		t.Errorf("live operator refused on HTTP: %v", refused)
	}
	if refused := run(opToken(t, srv, "live", 5)); len(refused) != 2 || refused["upload chain"] != http.StatusUnauthorized || refused["WithAuth"] != http.StatusUnauthorized {
		t.Errorf("non-operator on HTTP: %v, want 401 on both", refused)
	}
}

// The membership / admins-row re-check is called from teamAccess only: a second caller would be a
// second, divergent answer to «may this token still act on this team».
func TestTeamAccessIsTheOnlyMembershipCheck(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "checkAdminAccountState" && fn.Name.Name != "teamAccess" {
					t.Errorf("%s: %s calls checkAdminAccountState; route it through teamAccess", fset.Position(call.Pos()), fn.Name.Name)
				}
				return true
			})
		}
	}
}
