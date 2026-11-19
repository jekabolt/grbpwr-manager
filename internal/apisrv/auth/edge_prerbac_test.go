package auth

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// preRBACToken is an admin token minted before the super / perms claims existed: sub, iat, exp only.
// Production tokens live for weeks, so such tokens are still in open panels at a rollout.
func preRBACToken(t *testing.T, srv *Server) string {
	t.Helper()
	now := time.Now()
	_, tok, err := srv.JwtAuth.Encode(map[string]any{"sub": "u", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

const superOnlyMethod = "/admin.AdminService/UpdateAiProvider"

// Teams mode OFF is an identity: a pre-RBAC token keeps full non-super access, as on beta today.
func TestEdgePreRBACTokenModeOff(t *testing.T) {
	srv := newEdgeServer(t, nil)
	tok := preRBACToken(t, srv)
	for _, m := range []string{readMethod, writeMethod} {
		ctx, err := callAdmin(srv, m, tok, "")
		if err != nil {
			t.Fatalf("%s with a pre-RBAC token, mode off: %v", m, err)
		}
		az, ok := GetAdminAuthz(ctx)
		if !ok || !az.FullAccess() || az.Super {
			t.Fatalf("%s: authz %+v, want full non-super access", m, az)
		}
	}
	if _, err := callAdmin(srv, superOnlyMethod, tok, ""); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("super-only method with a pre-RBAC token: %v, want PermissionDenied", err)
	}
}

// Teams mode ON keeps the strict rule: no super claim, no perms → nothing beyond the allowlist.
func TestEdgePreRBACTokenTeamsMode(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	tok := preRBACToken(t, srv)
	if _, err := callAdmin(srv, readMethod, tok, legacyOrigin); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("pre-RBAC token in teams mode: %v, want PermissionDenied", err)
	}
	ctx, err := callAdmin(srv, "/admin.AdminService/GetCurrentAccount", tok, legacyOrigin)
	if err != nil {
		t.Fatalf("allowlisted method: %v", err)
	}
	if az, _ := GetAdminAuthz(ctx); az.FullAccess() {
		t.Fatalf("teams mode authz %+v: a pre-RBAC token must not be full access", az)
	}
}
