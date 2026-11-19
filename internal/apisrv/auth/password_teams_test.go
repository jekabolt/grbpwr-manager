package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// planeUser is one platform user of the fake control plane. teamOne=false: a platform user outside
// team #1 that only shares the handle (someone who signed up on the platform).
type planeUser struct {
	hash     string
	gen      int64
	teamOne  bool
	disabled bool
	sids     []string
}

// fakeCreds is the control plane as platformstore.Store answers credentialPlane: every credential
// operation finds the user through team #1 membership, never by bare handle.
type fakeCreds struct {
	users   map[string]*planeUser
	revoked map[string]bool
	err     error
	calls   int
}

func newCreds() *fakeCreds {
	return &fakeCreds{users: map[string]*planeUser{}, revoked: map[string]bool{}}
}

func (f *fakeCreds) change(u *planeUser, hash string) platformstore.CredentialChange {
	u.hash = hash
	u.gen++
	sids := u.sids
	for _, sid := range sids {
		f.revoked[sid] = true
	}
	u.sids = nil
	return platformstore.CredentialChange{Gen: u.gen, SIDs: sids}
}

func (f *fakeCreds) SetPasswordAndRevokeSessions(_ context.Context, handle, hash string, verify func(string) error, _ time.Time) (platformstore.CredentialChange, error) {
	f.calls++
	if f.err != nil {
		return platformstore.CredentialChange{}, f.err
	}
	u := f.users[handle]
	if u == nil || !u.teamOne {
		return platformstore.CredentialChange{}, platformstore.ErrUserNotFound
	}
	if verify != nil {
		if err := verify(u.hash); err != nil {
			return platformstore.CredentialChange{}, fmt.Errorf("%w: %w", platformstore.ErrPasswordMismatch, err)
		}
	}
	return f.change(u, hash), nil
}

func (f *fakeCreds) TeamOneCredentials(_ context.Context, handle string) (platformstore.TeamOneCred, bool, error) {
	f.calls++
	if f.err != nil {
		return platformstore.TeamOneCred{}, false, f.err
	}
	u := f.users[handle]
	if u == nil || !u.teamOne {
		return platformstore.TeamOneCred{}, false, nil
	}
	return platformstore.TeamOneCred{Hash: u.hash, Gen: u.gen, Disabled: u.disabled}, true, nil
}

func (f *fakeCreds) LinkTeamOneAdmin(_ context.Context, handle, hash string, _ bool, _ time.Time) (platformstore.CredentialChange, error) {
	f.calls++
	if f.err != nil {
		return platformstore.CredentialChange{}, f.err
	}
	u := f.users[handle]
	switch {
	case u == nil:
		f.users[handle] = &planeUser{hash: hash, teamOne: true}
		return platformstore.CredentialChange{}, nil
	case !u.teamOne:
		return platformstore.CredentialChange{}, platformstore.ErrHandleTaken
	default:
		return f.change(u, hash), nil
	}
}

// gen is what tenantbind's storePlane.LegacyCredGen answers: the linked user's generation, else 0.
func (f *fakeCreds) gen(handle string) int64 {
	if u := f.users[handle]; u != nil && u.teamOne {
		return u.gen
	}
	return 0
}

// credsEdge caches like tenantbind.Binder: a session it saw live and a handle's generation stay as
// they were until invalidated on this instance (or a token carries a newer generation).
type credsEdge struct {
	*fakeEdge
	creds   *fakeCreds
	cached  map[string]int64
	dropped []string
}

func (e *credsEdge) LegacyCredGen(_ context.Context, handle string, atLeast int64) (int64, error) {
	if g, ok := e.cached[handle]; ok && g >= atLeast {
		return g, nil
	}
	g := e.creds.gen(handle)
	e.cached[handle] = g
	return g, nil
}

func (e *credsEdge) InvalidateSession(sid string) {
	e.dropped = append(e.dropped, sid)
	if e.creds.revoked[sid] {
		e.sessions[sid] = false
	}
}

func (e *credsEdge) InvalidateCredentials(handle string) { delete(e.cached, handle) }

// passwordServer: teams mode, team #1 admin "u" whose tenant admins row holds the hash of password
// and who is linked to a platform user with the same hash (generation 0), sessions "live" + "s2".
func passwordServer(t *testing.T) (*Server, *mocks.MockAdmin, *credsEdge, *fakeCreds) {
	t.Helper()
	as := mocks.NewMockAdmin(t)
	srv, err := New(&Config{
		JWTSecret: jwtSecret, MasterPassword: masterPassword,
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "60m",
	}, as)
	require.NoError(t, err)
	creds := newCreds()
	e := &credsEdge{fakeEdge: newEdge(), creds: creds, cached: map[string]int64{}}
	e.sessions["s2"] = true
	srv.SetEdge(e)
	srv.creds = creds
	hash, err := srv.pwhash.HashPassword(password)
	require.NoError(t, err)
	creds.users["u"] = &planeUser{hash: hash, teamOne: true, sids: []string{"live", "s2"}}
	as.EXPECT().GetAccountWithPermissions(mock.Anything, "u").Return(account("u", hash, true), nil).Maybe()
	as.EXPECT().GetAdminByUsername(mock.Anything, mock.Anything).Return(&entity.Admin{Id: 1}, nil).Maybe()
	return srv, as, e, creds
}

func changePassword(srv *Server) (string, error) {
	resp, err := srv.ChangePassword(context.Background(), &pb_auth.ChangePasswordRequest{
		Username: "u", CurrentPassword: password, NewPassword: newPassword,
	})
	return resp.GetAuthToken(), err
}

func login(srv *Server, pw string) (string, error) {
	resp, err := srv.Login(context.Background(), &pb_auth.LoginRequest{Username: "u", Password: pw})
	return resp.GetAuthToken(), err
}

func requireLegacyOK(t *testing.T, srv *Server, token, msg string) {
	t.Helper()
	_, err := callAdmin(srv, readMethod, token, legacyOrigin)
	require.NoError(t, err, msg)
}

func requireLegacyRefused(t *testing.T, srv *Server, token, msg string) {
	t.Helper()
	_, err := callAdmin(srv, readMethod, token, legacyOrigin)
	require.Equal(t, codes.Unauthenticated, status.Code(err), msg)
}

// In teams mode the control plane is authoritative for a linked team #1 admin: hash, credential
// generation, session revoke in one tx, then the tenant mirror. The token handed back is minted
// after the commit with the new generation; every older legacy token dies.
func TestChangePasswordTeamsMode(t *testing.T) {
	t.Run("control plane, mirror, sessions dropped, old token dead, new token live", func(t *testing.T) {
		srv, as, e, creds := passwordServer(t)
		old, err := login(srv, password)
		require.NoError(t, err)
		requireLegacyOK(t, srv, old, "token before the change")
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
		tok, err := changePassword(srv)
		require.NoError(t, err)
		require.EqualValues(t, 1, creds.users["u"].gen)
		require.Equal(t, []string{"live", "s2"}, e.dropped)
		requireLegacyRefused(t, srv, old, "token of the old generation")
		requireLegacyOK(t, srv, tok, "token handed back by the change")
	})

	t.Run("control plane down: nothing changes, no success", func(t *testing.T) {
		srv, as, _, creds := passwordServer(t)
		creds.err = errors.New("down")
		_, err := changePassword(srv)
		require.Equal(t, codes.Unavailable, status.Code(err))
		as.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("tenant mirror retried until it lands", func(t *testing.T) {
		srv, as, _, _ := passwordServer(t)
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(errors.New("blip")).Twice()
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
		_, err := changePassword(srv)
		require.NoError(t, err)
	})

	t.Run("tenant mirror never lands: the old password still opens nothing", func(t *testing.T) {
		srv, as, _, _ := passwordServer(t)
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(errors.New("down")).Times(passwordMirrorAttempts)
		_, err := changePassword(srv)
		require.NoError(t, err, "the control plane committed: the change happened")
		// admins still holds the old hash (passwordServer's mock), the control plane the new one.
		_, err = login(srv, password)
		require.Equal(t, codes.Unauthenticated, status.Code(err), "old password through /api/auth/login")
		tok, err := login(srv, newPassword)
		require.NoError(t, err)
		requireLegacyOK(t, srv, tok, "new password's token")
	})

	t.Run("mirror lagging: the old password no longer changes the master hash", func(t *testing.T) {
		srv, as, _, creds := passwordServer(t)
		cur, err := srv.pwhash.HashPassword(newPassword)
		require.NoError(t, err)
		creds.users["u"].hash = cur
		_, err = changePassword(srv)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		require.Equal(t, cur, creds.users["u"].hash, "old password overwrote the control-plane hash")
		as.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything)

		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
		_, err = srv.ChangePassword(context.Background(), &pb_auth.ChangePasswordRequest{
			Username: "u", CurrentPassword: newPassword, NewPassword: "third-password-1",
		})
		require.NoError(t, err)
	})

	t.Run("master password still accepted in teams mode", func(t *testing.T) {
		srv, as, _, _ := passwordServer(t)
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
		_, err := srv.ChangePassword(context.Background(), &pb_auth.ChangePasswordRequest{
			Username: "u", CurrentPassword: masterPassword, NewPassword: "third-password-1",
		})
		require.NoError(t, err)
	})

	t.Run("no linked platform user: refused, no tenant-only change", func(t *testing.T) {
		srv, as, _, creds := passwordServer(t)
		delete(creds.users, "u")
		_, err := changePassword(srv)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		as.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("handle collision: the stranger's platform account is never touched", func(t *testing.T) {
		srv, as, e, creds := passwordServer(t)
		stranger := &planeUser{hash: "stranger-hash", gen: 4, sids: []string{"theirs"}}
		creds.users["u"] = stranger
		e.sessions["theirs"] = true
		_, err := changePassword(srv)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Equal(t, "stranger-hash", stranger.hash)
		require.EqualValues(t, 4, stranger.gen)
		require.Equal(t, []string{"theirs"}, stranger.sids)
		require.Empty(t, e.dropped)
		as.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything)
	})
}

// The old /api/auth/login in teams mode checks a linked team #1 admin's password against the
// control plane (the tenant admins row is a mirror and may lag) and mints the token with the
// account's credential generation.
func TestLegacyLoginTeamsMode(t *testing.T) {
	t.Run("linked: control-plane hash wins over a lagging mirror", func(t *testing.T) {
		srv, _, _, creds := passwordServer(t)
		cur, err := srv.pwhash.HashPassword(newPassword)
		require.NoError(t, err)
		creds.users["u"].hash, creds.users["u"].gen = cur, 3
		_, err = login(srv, password)
		require.Equal(t, codes.Unauthenticated, status.Code(err), "old password from the stale mirror")
		tok, err := login(srv, newPassword)
		require.NoError(t, err)
		claims, err := authjwt.VerifyTeamToken(srv.JwtAuth, tok, nil)
		require.NoError(t, err)
		require.EqualValues(t, 3, claims.CredGen)
		requireLegacyOK(t, srv, tok, "token of the current generation")
	})

	t.Run("disabled on the platform: refused", func(t *testing.T) {
		srv, _, _, creds := passwordServer(t)
		creds.users["u"].disabled = true
		_, err := login(srv, password)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("control plane down: unavailable, not the mirror", func(t *testing.T) {
		srv, _, _, creds := passwordServer(t)
		creds.err = errors.New("down")
		_, err := login(srv, password)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})

	t.Run("handle collision: the stranger's password opens nothing, the admin's own works", func(t *testing.T) {
		srv, _, _, creds := passwordServer(t)
		strangerHash, err := srv.pwhash.HashPassword("strangers-password")
		require.NoError(t, err)
		creds.users["u"] = &planeUser{hash: strangerHash, gen: 9}
		_, err = login(srv, "strangers-password")
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		tok, err := login(srv, password)
		require.NoError(t, err, "unlinked admin keeps its tenant password")
		claims, err := authjwt.VerifyTeamToken(srv.JwtAuth, tok, nil)
		require.NoError(t, err)
		require.Zero(t, claims.CredGen)
		requireLegacyOK(t, srv, tok, "unlinked admin's token (generation 0)")
	})
}

// Generations are compared for equality with no clock: a legacy token minted in the same second as
// the change is dead if it carries the old generation, and the one handed back is live at once.
func TestLegacyTokenGeneration(t *testing.T) {
	srv, as, _, creds := passwordServer(t)
	now := time.Now()
	mint := func(gen int64) string {
		tok, err := authjwt.NewAdminTokenAt(srv.JwtAuth, time.Hour, "u", true, nil, &authjwt.IssueOpts{CredGen: gen}, now)
		require.NoError(t, err)
		return tok
	}
	sameSecond := mint(0)
	requireLegacyOK(t, srv, sameSecond, "generation 0 before any change")
	as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
	tok, err := changePassword(srv)
	require.NoError(t, err)
	requireLegacyRefused(t, srv, sameSecond, "same-second token of the old generation")
	requireLegacyOK(t, srv, tok, "token handed back")
	requireLegacyOK(t, srv, mint(1), "generation 1")
	requireLegacyRefused(t, srv, mint(2), "a generation from the future")

	// Another instance changed the password: this edge's cache still says 1, a token of 2 makes
	// it read again instead of refusing a fresh token for the TTL.
	creds.users["u"].gen = 2
	requireLegacyOK(t, srv, mint(2), "fresh token minted elsewhere")
	requireLegacyRefused(t, srv, mint(1), "generation 1 after the cache caught up")
}

// Teams mode off: no control plane is wired, and Login / ChangePassword / Create mint the same
// claims as before (no cg claim), whatever the tenant row says.
func TestCredentialsModeOffNoControlPlane(t *testing.T) {
	as := mocks.NewMockAdmin(t)
	srv, err := New(&Config{
		JWTSecret: jwtSecret, MasterPassword: masterPassword,
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "60m",
	}, as)
	require.NoError(t, err)
	require.Nil(t, srv.creds)
	hash, err := srv.pwhash.HashPassword(password)
	require.NoError(t, err)
	as.EXPECT().GetAccountWithPermissions(mock.Anything, "u").Return(account("u", hash, true), nil)
	as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
	as.EXPECT().AddAccount(mock.Anything, "v", mock.Anything, true, mock.Anything).Return(nil).Once()

	tok, err := login(srv, password)
	require.NoError(t, err)
	changed, err := changePassword(srv)
	require.NoError(t, err)
	created, err := srv.Create(context.Background(), &pb_auth.CreateRequest{
		MasterPassword: masterPassword, User: &pb_auth.User{Username: "v", Password: "pw-of-v-1234"},
	})
	require.NoError(t, err)
	for _, tk := range []string{tok, changed, created.GetAuthToken()} {
		require.False(t, hasClaim(srv.JwtAuth, tk, "cg"), "mode off token carries cg")
	}
}

// The master-password bootstrap in teams mode links the control-plane account first and refuses a
// handle that belongs to a platform user outside team #1.
func TestCreateTeamsModeLinks(t *testing.T) {
	srv, as, _, creds := passwordServer(t)
	creds.users["taken"] = &planeUser{hash: "stranger-hash"}
	_, err := srv.Create(context.Background(), &pb_auth.CreateRequest{
		MasterPassword: masterPassword, User: &pb_auth.User{Username: "taken", Password: "pw-of-v-1234"},
	})
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Equal(t, "stranger-hash", creds.users["taken"].hash)
	as.AssertNotCalled(t, "AddAccount", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)

	as.EXPECT().AddAccount(mock.Anything, "v", mock.Anything, true, mock.Anything).Return(nil).Once()
	_, err = srv.Create(context.Background(), &pb_auth.CreateRequest{
		MasterPassword: masterPassword, User: &pb_auth.User{Username: "v", Password: "pw-of-v-1234"},
	})
	require.NoError(t, err)
	require.True(t, creds.users["v"].teamOne)
}
