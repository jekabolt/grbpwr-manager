package auth

import (
	"bytes"
	"context"
	"runtime"
	"testing"
	"time"

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cachingEdge caches like tenantbind.Binder: a session it saw live, and a handle's «not before»,
// stay as they were until invalidated, however the control plane moved since.
type cachingEdge struct {
	*fakeEdge
	planeRevoked map[string]bool
	planeChanged map[string]time.Time
}

func (e *cachingEdge) InvalidateSession(sid string) {
	if e.planeRevoked[sid] {
		e.sessions[sid] = false
	}
}

func (e *cachingEdge) InvalidateCredentials(handle string) {
	if at, ok := e.planeChanged[handle]; ok {
		e.changed[handle] = at
	}
}

// revokePlane is the control plane's password write: new hash, every session of the user revoked,
// password_changed_at = now (one tx).
type revokePlane struct {
	e    *cachingEdge
	sids []string
}

func (p *revokePlane) SetPasswordAndRevokeSessions(_ context.Context, handle, _ string, _ func(string) error, now time.Time) ([]string, error) {
	p.reset(handle, now)
	return p.sids, nil
}

func (p *revokePlane) reset(handle string, now time.Time) {
	for _, sid := range p.sids {
		p.e.planeRevoked[sid] = true
	}
	p.e.planeChanged[handle] = now
}

func revokeServer(t *testing.T) (*Server, *mocks.MockAdmin, *cachingEdge, *revokePlane) {
	t.Helper()
	as := mocks.NewMockAdmin(t)
	srv, err := New(&Config{
		JWTSecret: jwtSecret, MasterPassword: masterPassword,
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "60m",
	}, as)
	require.NoError(t, err)
	fe := newEdge()
	fe.changed = map[string]time.Time{}
	e := &cachingEdge{fakeEdge: fe, planeRevoked: map[string]bool{}, planeChanged: map[string]time.Time{}}
	srv.SetEdge(e)
	plane := &revokePlane{e: e, sids: []string{"live"}}
	srv.creds = plane
	hash, err := srv.pwhash.HashPassword(password)
	require.NoError(t, err)
	as.EXPECT().GetAccountWithPermissions(mock.Anything, "u").Return(account("u", hash, true), nil).Maybe()
	as.EXPECT().GetAdminByUsername(mock.Anything, mock.Anything).Return(&entity.Admin{Id: 1}, nil).Maybe()
	return srv, as, e, plane
}

// oldCredentials are what the holder had before the change: a platform session, a team token on
// it, and a legacy team #1 token (old /api/auth/login) issued a minute earlier.
type oldCredentials struct{ team, legacy string }

func issueOld(t *testing.T, srv *Server) oldCredentials {
	t.Helper()
	legacy, err := authjwt.NewAdminTokenAt(srv.JwtAuth, time.Hour, "u", true, nil, nil, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	c := oldCredentials{
		team:   teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true}),
		legacy: legacy,
	}
	_, err = callAdmin(srv, readMethod, c.team, acmeOrigin)
	require.NoError(t, err, "team token before the change")
	_, err = callAdmin(srv, readMethod, c.legacy, legacyOrigin)
	require.NoError(t, err, "legacy token before the change")
	return c
}

// requireKilled: on their next call the old platform session, team token and legacy token are
// refused, while a fresh sign-in (new session, new legacy token) works.
func requireKilled(t *testing.T, srv *Server, e *cachingEdge, old oldCredentials) {
	t.Helper()
	live, err := e.SessionActive(context.Background(), "live")
	require.NoError(t, err)
	require.False(t, live, "old platform session still live at the edge")
	_, err = callAdmin(srv, readMethod, old.team, acmeOrigin)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "old team token")
	_, err = callAdmin(srv, readMethod, old.legacy, legacyOrigin)
	require.Equal(t, codes.Unauthenticated, status.Code(err), "old legacy team #1 token")

	e.sessions["fresh"] = true
	_, err = callAdmin(srv, readMethod, teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true, SessionID: "fresh"}), acmeOrigin)
	require.NoError(t, err, "team token of a new sign-in")
	_, err = callAdmin(srv, readMethod, legacyToken(t, srv), legacyOrigin)
	require.NoError(t, err, "legacy token issued after the change")
}

func TestAdminPasswordChangeKillsOldCredentials(t *testing.T) {
	srv, as, e, _ := revokeServer(t)
	old := issueOld(t, srv)
	as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
	_, err := srv.ChangePassword(context.Background(), &pb_auth.ChangePasswordRequest{
		Username: "u", CurrentPassword: password, NewPassword: newPassword,
	})
	require.NoError(t, err)
	requireKilled(t, srv, e, old)
}

// A reset (platformauth.ResetPassword) commits the same control-plane write, then runs the
// PasswordChanged hook with the revoked sessions; the flow test pins that ResetPassword calls it.
func TestPasswordResetHookKillsOldCredentials(t *testing.T) {
	srv, _, e, plane := revokeServer(t)
	old := issueOld(t, srv)
	plane.reset("u", time.Now().UTC())
	srv.platformHooks().PasswordChanged(context.Background(), "u", plane.sids)
	requireKilled(t, srv, e, old)
}

// The edge's lookup failing refuses a legacy token (fails closed), never lets it through.
func TestLegacyNotBeforeLookupFailsClosed(t *testing.T) {
	srv := newEdgeServer(t, &failingNotBefore{fakeEdge: newEdge()})
	_, err := callAdmin(srv, readMethod, legacyToken(t, srv), legacyOrigin)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

type failingNotBefore struct{ *fakeEdge }

func (failingNotBefore) LegacyNotBefore(context.Context, string) (time.Time, error) {
	return time.Time{}, context.DeadlineExceeded
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
