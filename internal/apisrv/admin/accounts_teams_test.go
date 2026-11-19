package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeTeamOneCreds stands in for the auth server's control-plane side of team #1 credentials.
type fakeTeamOneCreds struct {
	resets, links []string
	resetErr      error
	linkErr       error
}

func (f *fakeTeamOneCreds) ResetTeamOnePassword(_ context.Context, username, hash string) error {
	f.resets = append(f.resets, username)
	return f.resetErr
}

func (f *fakeTeamOneCreds) LinkTeamOneAccount(_ context.Context, username, hash string, super bool) (int64, error) {
	f.links = append(f.links, username)
	return 1, f.linkErr
}

func teamsServer(t *testing.T, admin *mocks.MockAdmin, creds *fakeTeamOneCreds) (*Server, *[]string) {
	t.Helper()
	ph, err := pwhash.New(16, 1000)
	require.NoError(t, err)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Admin().Return(admin).Maybe()
	s := &Server{repo: repo, pwhash: ph}
	if creds != nil {
		s.SetTeamOneCredentials(creds)
	}
	var invalidated []string
	s.SetAdminAuthInvalidator(func(u string) { invalidated = append(invalidated, u) })
	return s, &invalidated
}

func resetReq(user string) *pb_admin.ResetAccountPasswordRequest {
	return &pb_admin.ResetAccountPasswordRequest{Username: user, NewPassword: "long-enough-password"}
}

func createReq(user string) *pb_admin.CreateAccountRequest {
	return &pb_admin.CreateAccountRequest{Username: user, Password: "long-enough-pw"}
}

// Teams mode, team #1: an admin-panel reset goes through the control plane's transaction (which
// writes the mirror itself), never straight to the admins row.
func TestResetAccountPasswordTeamsMode(t *testing.T) {
	t.Run("control plane, not the admins row", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		creds := &fakeTeamOneCreds{}
		s, inv := teamsServer(t, admin, creds)

		_, err := s.ResetAccountPassword(ctxAs("owner", true, nil), resetReq("anna"))
		require.NoError(t, err)
		require.Equal(t, []string{"anna"}, creds.resets)
		require.Equal(t, []string{"anna"}, *inv)
		admin.AssertNotCalled(t, "ChangePassword", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("on team #1 explicitly", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		creds := &fakeTeamOneCreds{}
		s, _ := teamsServer(t, admin, creds)

		ctx := tenant.With(ctxAs("owner", true, nil), tenant.Team{ID: tenant.TeamOneID})
		_, err := s.ResetAccountPassword(ctx, resetReq("anna"))
		require.NoError(t, err)
		require.Equal(t, []string{"anna"}, creds.resets)
	})

	t.Run("no linked platform account: refused, the admins row untouched", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		creds := &fakeTeamOneCreds{resetErr: status.Error(codes.FailedPrecondition, "account_not_linked")}
		s, inv := teamsServer(t, admin, creds)

		_, err := s.ResetAccountPassword(ctxAs("owner", true, nil), resetReq("anna"))
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Empty(t, *inv)
	})

	t.Run("another team keeps its own admins row", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		admin.EXPECT().ChangePassword(mock.Anything, "anna", mock.AnythingOfType("string")).Return(nil).Once()
		creds := &fakeTeamOneCreds{}
		s, _ := teamsServer(t, admin, creds)

		ctx := tenant.With(ctxAs("owner", true, nil), tenant.Team{ID: 7})
		_, err := s.ResetAccountPassword(ctx, resetReq("anna"))
		require.NoError(t, err)
		require.Empty(t, creds.resets)
	})

	t.Run("teams mode off: the admins row as before", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		admin.EXPECT().ChangePassword(mock.Anything, "anna", mock.AnythingOfType("string")).Return(nil).Once()
		s, inv := teamsServer(t, admin, nil)

		_, err := s.ResetAccountPassword(ctxAs("owner", true, nil), resetReq("anna"))
		require.NoError(t, err)
		require.Equal(t, []string{"anna"}, *inv)
	})
}

// Teams mode, team #1: creating an admin creates or links its control-plane account first; a
// handle owned by a platform user outside team #1 is refused and no admins row appears.
func TestCreateAccountTeamsMode(t *testing.T) {
	t.Run("links the control-plane account, then the admins row", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(nil, sql.ErrNoRows).Once()
		admin.EXPECT().AddAccount(mock.Anything, "anna", mock.AnythingOfType("string"), false, mock.Anything).Return(nil).Once()
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		creds := &fakeTeamOneCreds{}
		s, _ := teamsServer(t, admin, creds)

		_, err := s.CreateAccount(ctxAs("owner", true, nil), createReq("anna"))
		require.NoError(t, err)
		require.Equal(t, []string{"anna"}, creds.links)
	})

	t.Run("handle collision: refused, no admins row", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(nil, sql.ErrNoRows).Once()
		creds := &fakeTeamOneCreds{linkErr: status.Error(codes.AlreadyExists, "username_taken")}
		s, inv := teamsServer(t, admin, creds)

		_, err := s.CreateAccount(ctxAs("owner", true, nil), createReq("anna"))
		require.Equal(t, codes.AlreadyExists, status.Code(err))
		require.Empty(t, *inv)
		admin.AssertNotCalled(t, "AddAccount", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("existing admin: refused before the control plane is touched", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		creds := &fakeTeamOneCreds{}
		s, _ := teamsServer(t, admin, creds)

		_, err := s.CreateAccount(ctxAs("owner", true, nil), createReq("anna"))
		require.Equal(t, codes.AlreadyExists, status.Code(err))
		require.Empty(t, creds.links, "an existing account's password reset through create")
	})

	t.Run("teams mode off: the admins row only", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().AddAccount(mock.Anything, "anna", mock.AnythingOfType("string"), false, mock.Anything).Return(nil).Once()
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").Return(accountOf("anna", false), nil).Once()
		s, _ := teamsServer(t, admin, nil)

		_, err := s.CreateAccount(ctxAs("owner", true, nil), createReq("anna"))
		require.NoError(t, err)
	})
}
