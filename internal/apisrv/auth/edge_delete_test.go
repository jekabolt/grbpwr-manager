package auth

import (
	"testing"

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A deleted team's token is refused at the edge; after restore the team serves again. The delete
// also revokes the team's sessions, so in production the old token stays dead and people sign in
// again: the fresh token works.
func TestEdgeDeletedTeamAndRestore(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); err != nil {
		t.Fatalf("active team: %v", err)
	}

	deleted := edgeAcme
	deleted.Status = tenant.StatusDeleted
	e.teams[7] = deleted
	for _, m := range []string{readMethod, writeMethod, "/admin.AdminService/GetCurrentAccount"} {
		if _, err := callAdmin(srv, m, tok, acmeOrigin); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("deleted team, %s: %v", m, err)
		}
	}

	// Restore (the status gate alone): the same token passes again.
	e.teams[7] = edgeAcme
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); err != nil {
		t.Fatalf("restored team: %v", err)
	}

	// With the session revoked by the delete, the old token stays refused; a new sign-in works.
	e.sessions["live"] = false
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked session after restore: %v", err)
	}
	e.sessions["fresh"] = true
	fresh := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true, SessionID: "fresh"})
	if _, err := callAdmin(srv, readMethod, fresh, acmeOrigin); err != nil {
		t.Fatalf("fresh sign-in after restore: %v", err)
	}
}
