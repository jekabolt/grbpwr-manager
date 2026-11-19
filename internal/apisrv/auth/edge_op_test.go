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

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// withOp re-signs tok with the operator claim added: the console lane will mint such tokens for a
// platform operator who is not a member of the team. Until it lands they must be refused.
func withOp(t *testing.T, srv *Server, tok string) string {
	t.Helper()
	parsed, err := srv.JwtAuth.Decode(tok)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := parsed.AsMap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	claims[claimOperator] = true
	_, out, err := srv.JwtAuth.Encode(claims)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEdgeOperatorTokenRefused(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	team := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	legacy := legacyToken(t, srv)

	// The same tokens without op pass: the refusal is the claim, not a broken re-sign.
	if _, err := callAdmin(srv, readMethod, team, acmeOrigin); err != nil {
		t.Fatalf("team token without op refused: %v", err)
	}
	if _, err := callAdmin(srv, readMethod, legacy, legacyOrigin); err != nil {
		t.Fatalf("legacy token without op refused: %v", err)
	}

	cases := []struct {
		name, method, token, origin string
	}{
		{"team read", readMethod, withOp(t, srv, team), acmeOrigin},
		{"team write", writeMethod, withOp(t, srv, team), acmeOrigin},
		{"legacy", readMethod, withOp(t, srv, legacy), legacyOrigin},
	}
	for _, c := range cases {
		_, err := callAdmin(srv, c.method, c.token, c.origin)
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s with op: %v, want Unauthenticated", c.name, err)
		}
	}
}

func TestEdgeOperatorTokenRefusedOnHTTP(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	team := withOp(t, srv, teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true}))

	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	for name, h := range map[string]http.Handler{
		"upload chain": srv.WithAdminAuthz(next),
		"WithAuth":     srv.WithAuth(next),
	} {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/api/files/upload", nil)
		req.Header.Set(AuthMetadataKey, "Bearer "+team)
		req.Header.Set("Origin", acmeOrigin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || reached {
			t.Errorf("%s with op: %d (reached=%v), want 401", name, rec.Code, reached)
		}
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
