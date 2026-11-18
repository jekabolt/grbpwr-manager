package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeCreds is the control plane's password write: hash + session revocation in one tx.
type fakeCreds struct {
	err     error
	calls   int
	hash    string
	revoked []string
}

func (f *fakeCreds) SetPasswordAndRevokeSessions(_ context.Context, handle, hash string, _ time.Time) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	f.hash = hash
	return f.revoked, nil
}

type invalidatingEdge struct {
	*fakeEdge
	dropped []string
}

func (e *invalidatingEdge) InvalidateSession(sid string) { e.dropped = append(e.dropped, sid) }

func passwordServer(t *testing.T, creds *fakeCreds) (*Server, *mocks.MockAdmin, *invalidatingEdge) {
	t.Helper()
	as := mocks.NewMockAdmin(t)
	srv, err := New(&Config{
		JWTSecret: jwtSecret, MasterPassword: masterPassword,
		PasswordHasherSaltSize: 16, PasswordHasherIterations: 1000, JWTTTL: "60m",
	}, as)
	require.NoError(t, err)
	e := &invalidatingEdge{fakeEdge: newEdge()}
	srv.SetEdge(e)
	srv.creds = creds
	hash, err := srv.pwhash.HashPassword(password)
	require.NoError(t, err)
	as.EXPECT().GetAccountWithPermissions(mock.Anything, "u").Return(account("u", hash, true), nil).Maybe()
	return srv, as, e
}

func changePassword(srv *Server) error {
	_, err := srv.ChangePassword(context.Background(), &pb_auth.ChangePasswordRequest{
		Username: "u", CurrentPassword: password, NewPassword: newPassword,
	})
	return err
}

// Review item 8: in teams mode the control plane is authoritative (hash + session revocation in
// one tx), the tenant admins row is mirrored with a retry, and success is never answered while a
// sign-in path still takes the old password.
func TestChangePasswordTeamsMode(t *testing.T) {
	t.Run("both stores updated, sessions revoked and dropped from the cache", func(t *testing.T) {
		creds := &fakeCreds{revoked: []string{"s1", "s2"}}
		srv, as, e := passwordServer(t, creds)
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
		require.NoError(t, changePassword(srv))
		require.Equal(t, 1, creds.calls)
		require.NotEmpty(t, creds.hash)
		require.Equal(t, []string{"s1", "s2"}, e.dropped)
	})

	t.Run("control plane down: nothing changes, no success", func(t *testing.T) {
		srv, as, _ := passwordServer(t, &fakeCreds{err: errors.New("down")})
		err := changePassword(srv)
		require.Equal(t, codes.Unavailable, status.Code(err))
		as.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("tenant mirror retried until it lands", func(t *testing.T) {
		srv, as, _ := passwordServer(t, &fakeCreds{})
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(errors.New("blip")).Twice()
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
		require.NoError(t, changePassword(srv))
	})

	t.Run("tenant mirror never lands: an error, not success", func(t *testing.T) {
		srv, as, _ := passwordServer(t, &fakeCreds{})
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(errors.New("down")).Times(passwordMirrorAttempts)
		require.Equal(t, codes.Unavailable, status.Code(changePassword(srv)))
	})

	t.Run("no platform user: tenant only, as before", func(t *testing.T) {
		srv, as, _ := passwordServer(t, &fakeCreds{err: platformstore.ErrUserNotFound})
		as.EXPECT().ChangePassword(mock.Anything, "u", mock.Anything).Return(nil).Once()
		require.NoError(t, changePassword(srv))
	})
}
