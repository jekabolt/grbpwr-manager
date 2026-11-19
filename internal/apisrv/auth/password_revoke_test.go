package auth

import (
	"bytes"
	"context"
	"runtime"
	"testing"
	"time"

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// oldCredentials are what the holder had before the change: a team token on the platform session
// "live" and a legacy team #1 token (old /api/auth/login) of generation 0.
type oldCredentials struct{ team, legacy string }

func issueOld(t *testing.T, srv *Server) oldCredentials {
	t.Helper()
	c := oldCredentials{
		team:   teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true}),
		legacy: legacyToken(t, srv),
	}
	_, err := callAdmin(srv, readMethod, c.team, acmeOrigin)
	require.NoError(t, err, "team token before the change")
	requireLegacyOK(t, srv, c.legacy, "legacy token before the change")
	return c
}

// A reset (platformauth.ResetPassword) commits the control-plane write, then runs the
// PasswordChanged hook with the revoked sessions; the flow test pins that ResetPassword calls it.
// On their next call the old platform session, team token and legacy token are refused.
func TestPasswordResetHookKillsOldCredentials(t *testing.T) {
	srv, _, e, creds := passwordServer(t)
	old := issueOld(t, srv)
	c := creds.change(creds.users["u"], "reset-hash")
	srv.platformHooks().PasswordChanged(context.Background(), "u", c.SIDs)

	live, err := e.SessionActive(context.Background(), "live")
	require.NoError(t, err)
	require.False(t, live, "old platform session still live at the edge")
	_, err = callAdmin(srv, readMethod, old.team, acmeOrigin)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "old team token")
	requireLegacyRefused(t, srv, old.legacy, "old legacy team #1 token")

	e.sessions["fresh"] = true
	_, err = callAdmin(srv, readMethod, teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true, SessionID: "fresh"}), acmeOrigin)
	require.NoError(t, err, "team token of a new sign-in")
	tok, err := authjwt.NewAdminToken(srv.JwtAuth, time.Hour, "u", true, nil, &authjwt.IssueOpts{CredGen: c.Gen})
	require.NoError(t, err)
	requireLegacyOK(t, srv, tok, "legacy token of the new generation")
}

// The edge's lookup failing refuses a legacy token (fails closed), never lets it through.
func TestLegacyCredGenLookupFailsClosed(t *testing.T) {
	srv := newEdgeServer(t, &failingCredGen{fakeEdge: newEdge()})
	_, err := callAdmin(srv, readMethod, legacyToken(t, srv), legacyOrigin)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

type failingCredGen struct{ *fakeEdge }

func (failingCredGen) LegacyCredGen(context.Context, string, int64) (int64, error) {
	return 0, context.DeadlineExceeded
}

// StopRateLimiter stops the identity service's limiters too (their cleanup goroutines exit).
func TestStopRateLimiterClosesPlatformService(t *testing.T) {
	srv := newEdgeServer(t, nil)
	srv.platform = platformHandle{store: &platformstore.Store{}, cfg: platformstore.Config{}}
	before := limiterGoroutines()
	srv.platform.svc, srv.platform.svcErr = srv.newPlatformService()
	require.NoError(t, srv.platform.svcErr)
	built := limiterGoroutines()
	require.GreaterOrEqual(t, built-before, 6, "service limiters")

	srv.StopRateLimiter()
	srv.StopRateLimiter() // idempotent
	deadline := time.Now().Add(2 * time.Second)
	for limiterGoroutines() > built-6 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.LessOrEqual(t, limiterGoroutines(), built-6, "service limiters still running")
}

func limiterGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte("ratelimit.(*Limiter).cleanup"))
		}
		buf = make([]byte, 2*len(buf))
	}
}
