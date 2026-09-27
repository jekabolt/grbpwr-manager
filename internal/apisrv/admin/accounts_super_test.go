package admin

import (
	"context"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// superGrantRefusal is the exact refusal both handlers give a caller that is not full access.
const superGrantRefusal = "only a super admin may grant super access"

// TestUpdateAccountPermissionsSuperOnlyFromSuper closes security audit P1 #2 on the path it was found
// on: an accounts:write holder sent UpdateAccountPermissions{username: <own name>, is_super: true},
// the interceptor let it through (the method needs exactly accounts:write), and the next login minted
// a super token. ensureNotLastSuper did not help — it only guards DEMOTION.
func TestUpdateAccountPermissionsSuperOnlyFromSuper(t *testing.T) {
	t.Run("accounts:write promoting itself: PermissionDenied before any store call", func(t *testing.T) {
		// No expectations on repo: mockery fails the test on any call, which is the proof that the
		// refusal came before the account was even read.
		s := &Server{repo: mocks.NewMockRepository(t)}

		_, err := s.UpdateAccountPermissions(ctxAs("kirill", false, accountsWrite),
			&pb_admin.UpdateAccountPermissionsRequest{Username: "kirill", IsSuper: true})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Equal(t, superGrantRefusal, status.Convert(err).Message())
	})

	t.Run("a context that never passed the interceptor is not full access", func(t *testing.T) {
		s := &Server{repo: mocks.NewMockRepository(t)}

		_, err := s.UpdateAccountPermissions(context.Background(),
			&pb_admin.UpdateAccountPermissionsRequest{Username: "kirill", IsSuper: true})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("super promotes someone: the flag reaches the store", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill").
			Return(accountOf("kirill", false), nil).Once()
		// Promotion, not demotion: CountSuperAdmins is NOT expected, so ensureNotLastSuper must not run.
		admin.EXPECT().SetAccountPermissions(mock.Anything, "kirill", true, []entity.AdminPermission(nil)).
			Return(nil).Once()
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill").
			Return(accountOf("kirill", true), nil).Once()
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		resp, err := s.UpdateAccountPermissions(ctxAs("owner", true, nil),
			&pb_admin.UpdateAccountPermissionsRequest{Username: "kirill", IsSuper: true})
		require.NoError(t, err)
		require.True(t, resp.Account.IsSuper)
	})

	t.Run("accounts:write editing scoped grants is untouched by the guard", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").
			Return(accountOf("anna", false), nil).Once()
		admin.EXPECT().SetAccountPermissions(mock.Anything, "anna", false,
			[]entity.AdminPermission{{Section: rbac.SectionOrders, Access: entity.AccessRead}}).
			Return(nil).Once()
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").
			Return(accountOf("anna", false), nil).Once()
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		_, err := s.UpdateAccountPermissions(ctxAs("kirill", false, accountsWrite),
			&pb_admin.UpdateAccountPermissionsRequest{
				Username:    "anna",
				Permissions: []*pb_admin.AdminPermission{{Section: rbac.SectionOrders, Access: pb_admin.AccessLevel_ACCESS_LEVEL_READ}},
			})
		require.NoError(t, err)
	})

	t.Run("ensureNotLastSuper still refuses to demote the last super", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "owner").
			Return(accountOf("owner", true), nil).Once()
		admin.EXPECT().CountSuperAdmins(mock.Anything).Return(1, nil).Once()
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		_, err := s.UpdateAccountPermissions(ctxAs("owner", true, nil),
			&pb_admin.UpdateAccountPermissionsRequest{Username: "owner", IsSuper: false})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
	})
}

// TestCreateAccountSuperOnlyFromSuper — the second door of audit P1 #2: CreateAccount{is_super: true}
// from an accounts:write holder mints a brand-new super account they know the password of.
func TestCreateAccountSuperOnlyFromSuper(t *testing.T) {
	ph, err := pwhash.New(16, 1000)
	require.NoError(t, err)

	t.Run("accounts:write creating a super: PermissionDenied before any store call", func(t *testing.T) {
		s := &Server{repo: mocks.NewMockRepository(t), pwhash: ph}

		_, err := s.CreateAccount(ctxAs("kirill", false, accountsWrite),
			&pb_admin.CreateAccountRequest{Username: "kirill2", Password: "long-enough-pw", IsSuper: true})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Equal(t, superGrantRefusal, status.Convert(err).Message())
	})

	t.Run("a context that never passed the interceptor is not full access", func(t *testing.T) {
		s := &Server{repo: mocks.NewMockRepository(t), pwhash: ph}

		_, err := s.CreateAccount(context.Background(),
			&pb_admin.CreateAccountRequest{Username: "kirill2", Password: "long-enough-pw", IsSuper: true})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	// Full access is Super || Legacy (AdminAuthz.FullAccess). A legacy pre-RBAC token predates
	// permissions, when every admin was de facto super, so it may still grant the flag here — unlike
	// the super-only RPCs, which refuse it.
	for _, caller := range []struct {
		name  string
		authz authsrv.AdminAuthz
	}{
		{"super", authsrv.AdminAuthz{Super: true}},
		{"legacy full-access token", authsrv.AdminAuthz{Legacy: true}},
	} {
		t.Run(caller.name+" creates a super: the flag reaches the store", func(t *testing.T) {
			admin := mocks.NewMockAdmin(t)
			admin.EXPECT().AddAccount(mock.Anything, "kirill2", mock.AnythingOfType("string"), true, []entity.AdminPermission(nil)).
				Return(nil).Once()
			admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill2").
				Return(accountOf("kirill2", true), nil).Once()
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Admin().Return(admin)
			s := &Server{repo: repo, pwhash: ph}

			ctx := authsrv.PutAdminAuthz(authsrv.PutAdminUsername(context.Background(), "owner"), caller.authz)
			resp, err := s.CreateAccount(ctx,
				&pb_admin.CreateAccountRequest{Username: "kirill2", Password: "long-enough-pw", IsSuper: true})
			require.NoError(t, err)
			require.True(t, resp.Account.IsSuper)
		})
	}

	t.Run("accounts:write creating a scoped account is untouched by the guard", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().AddAccount(mock.Anything, "anna", mock.AnythingOfType("string"), false,
			[]entity.AdminPermission{{Section: rbac.SectionOrders, Access: entity.AccessWrite}}).
			Return(nil).Once()
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "anna").
			Return(accountOf("anna", false), nil).Once()
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo, pwhash: ph}

		_, err := s.CreateAccount(ctxAs("kirill", false, accountsWrite),
			&pb_admin.CreateAccountRequest{
				Username:    "anna",
				Password:    "long-enough-pw",
				Permissions: []*pb_admin.AdminPermission{{Section: rbac.SectionOrders, Access: pb_admin.AccessLevel_ACCESS_LEVEL_WRITE}},
			})
		require.NoError(t, err)
	})
}

func accountOf(username string, super bool) *entity.AdminAccount {
	return &entity.AdminAccount{Admin: entity.Admin{Username: username, IsSuper: super}}
}
