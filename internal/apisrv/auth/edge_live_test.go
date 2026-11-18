package auth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// callAdminAs is callAdmin as the grpc-gateway loopback hop sends it: x-forwarded-host and x-team
// from the HTTP request, peer 127.0.0.1.
func callAdminAs(srv *Server, method, token, origin, host, xteam string) (context.Context, error) {
	return callAdminFrom(srv, method, token, origin, xteam, loopbackPeer, map[string]string{"x-forwarded-host": host})
}

var (
	loopbackPeer = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000}
	remotePeer   = &net.TCPAddr{IP: net.IPv4(10, 244, 0, 7), Port: 50000}
)

func callAdminFrom(srv *Server, method, token, origin, xteam string, from net.Addr, hosts map[string]string) (context.Context, error) {
	md := metadata.New(map[string]string{AuthMetadataKey: "Bearer " + token, "grpcgateway-origin": origin})
	for k, v := range hosts {
		if v != "" {
			md.Set(k, v)
		}
	}
	if xteam != "" {
		md.Set("x-team", xteam)
	}
	ctx := peer.NewContext(metadata.NewIncomingContext(context.Background(), md), &peer.Peer{Addr: from})
	var got context.Context
	_, err := srv.UnaryAdminAuthInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, _ any) (any, error) { got = ctx; return nil, nil })
	return got, err
}

// Review item 3: a removed member, a disabled user or a revoked session loses access at once, and a
// demoted owner loses super, although the token is still valid.
func TestEdgeLiveControlTuple(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	if _, err := callAdmin(srv, writeMethod, tok, acmeOrigin); err != nil {
		t.Fatalf("live member refused: %v", err)
	}

	e.roles[5] = "member"
	if _, err := callAdmin(srv, writeMethod, tok, acmeOrigin); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("demoted owner with a super token: %v, want PermissionDenied", err)
	}
	delete(e.roles, 5)

	e.removed[5] = true
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("removed member: %v, want Unauthenticated", err)
	}
	// The same decision guards WithAuth (/api/admin mount).
	req := httptest.NewRequest(http.MethodGet, "/api/admin/x", nil)
	req.Header.Set(AuthMetadataKey, "Bearer "+tok)
	rec := httptest.NewRecorder()
	srv.WithAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("removed member reached the handler") })).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("WithAuth for a removed member: %d, want 401", rec.Code)
	}
	delete(e.removed, 5)

	e.sessions["live"] = false
	if _, err := callAdmin(srv, readMethod, tok, acmeOrigin); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked session: %v, want Unauthenticated", err)
	}
}

// Review item 4: the team a request is presented as must be the token's team.
func TestEdgeRequestedTeamMustBeTheTokenTeam(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	acme := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	legacy := legacyToken(t, srv)

	ok := []struct{ name, tok, origin, host, xteam string }{
		{"api host, own X-Team", acme, acmeOrigin, "api.lekalo.studio", "acme"},
		{"own team host", acme, acmeOrigin, "acme.lekalo.studio", ""},
		{"no host, no header", acme, acmeOrigin, "", ""},
		{"legacy, team one header", legacy, legacyOrigin, "", "grbpwr"},
	}
	for _, c := range ok {
		if _, err := callAdminAs(srv, readMethod, c.tok, c.origin, c.host, c.xteam); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	bad := []struct{ name, tok, origin, host, xteam string }{
		{"team-A token, X-Team of B", acme, "", "", "grbpwr"},
		{"team-A token on B's host", acme, "", "grbpwr.lekalo.studio", ""},
		{"host and header disagree", acme, "", "acme.lekalo.studio", "grbpwr"},
		{"unknown team", acme, "", "", "nobody"},
		{"legacy token, X-Team of a lekalo team", legacy, "", "", "acme"},
	}
	for _, c := range bad {
		_, err := callAdminAs(srv, writeMethod, c.tok, c.origin, c.host, c.xteam)
		if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != msgTeamMismatch {
			t.Errorf("%s: %v, want PermissionDenied team_mismatch", c.name, err)
		}
	}
}

// The upload routes read Host and X-Team from the HTTP request.
func TestEdgeRequestedTeamOnUploadRoutes(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	tok := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	h := srv.WithAdminAuthz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, c := range []struct {
		host, xteam string
		want        int
	}{
		{"acme.lekalo.studio", "", http.StatusOK},
		{"api.lekalo.studio", "acme", http.StatusOK},
		{"grbpwr.lekalo.studio", "", http.StatusForbidden},
		{"api.lekalo.studio", "grbpwr", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/files/upload", nil)
		req.Host = c.host
		req.Header.Set(AuthMetadataKey, "Bearer "+tok)
		req.Header.Set("Origin", acmeOrigin)
		if c.xteam != "" {
			req.Header.Set("Grpc-Metadata-X-Team", c.xteam)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("host %q x-team %q: %d, want %d (%s)", c.host, c.xteam, rec.Code, c.want, rec.Body.String())
		}
	}
}

// Fix-round item 2: x-forwarded-host is the gateway's annotation, trusted only on its loopback hop.
// A direct gRPC call to team B's address cannot claim team A's host with it.
func TestEdgeForwardedHostTrustedOnlyFromTheGateway(t *testing.T) {
	srv := newEdgeServer(t, newEdge())
	acme := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	hosts := map[string]string{"x-forwarded-host": "acme.lekalo.studio", ":authority": "grbpwr.lekalo.studio"}

	_, err := callAdminFrom(srv, readMethod, acme, "", "acme", remotePeer, hosts)
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != msgTeamMismatch {
		t.Fatalf("direct call with a forged x-forwarded-host: %v, want team_mismatch", err)
	}
	if _, err := callAdminFrom(srv, readMethod, acme, "", "acme", nil, hosts); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("no peer: %v, want team_mismatch", err)
	}
	// The gateway hop: its annotation is the public Host.
	if _, err := callAdminFrom(srv, readMethod, acme, acmeOrigin, "acme", loopbackPeer, hosts); err != nil {
		t.Fatalf("gateway hop on the own host: %v", err)
	}
	// A direct call on its own team's address is fine.
	own := map[string]string{":authority": "acme.lekalo.studio:443"}
	if _, err := callAdminFrom(srv, readMethod, acme, "", "", remotePeer, own); err != nil {
		t.Fatalf("direct call on the own host: %v", err)
	}
}
