package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// superGrantRefusal is the exact refusal both handlers give a caller that is not full access.
const superGrantRefusal = "only a super admin may grant super access"

// TestUpdateAccountPermissionsSuperOnlyFromSuper closes security audit P1 #2 on the path it was found
// on: an accounts:write holder sent UpdateAccountPermissions{username: <own name>, is_super: true},
// the interceptor let it through (the method needs exactly accounts:write), and the next login minted
// a super token. ensureNotLastSuper did not help — it only guards DEMOTION.
func TestUpdateAccountPermissionsSuperOnlyFromSuper(t *testing.T) {
	t.Run("accounts:write promoting itself: PermissionDenied after the target check", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill").
			Return(accountOf("kirill", false), nil).Once()
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		_, err := s.UpdateAccountPermissions(ctxAs("kirill", false, accountsWrite),
			&pb_admin.UpdateAccountPermissionsRequest{Username: "kirill", IsSuper: true})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Equal(t, superGrantRefusal, status.Convert(err).Message())
	})

	t.Run("a context that never passed the interceptor is not full access", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill").
			Return(accountOf("kirill", false), nil).Once()
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

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

func TestOnlyFullAccessMayManageSuperAccount(t *testing.T) {
	ph, err := pwhash.New(16, 1000)
	require.NoError(t, err)

	type operation struct {
		name               string
		call               func(*Server, context.Context) error
		allow              func(*mocks.MockAdmin)
		expectInvalidation bool
	}
	operations := []operation{
		{
			name: "reset password",
			call: func(s *Server, ctx context.Context) error {
				_, err := s.ResetAccountPassword(ctx, &pb_admin.ResetAccountPasswordRequest{
					Username: "target-super", NewPassword: "long-enough-password",
				})
				return err
			},
			allow: func(admin *mocks.MockAdmin) {
				admin.EXPECT().ChangePassword(mock.Anything, "target-super", mock.AnythingOfType("string")).Return(nil).Once()
			},
			expectInvalidation: true,
		},
		{
			name: "disable",
			call: func(s *Server, ctx context.Context) error {
				_, err := s.SetAccountDisabled(ctx, &pb_admin.SetAccountDisabledRequest{Username: "target-super", Disabled: true})
				return err
			},
			allow: func(admin *mocks.MockAdmin) {
				admin.EXPECT().CountSuperAdmins(mock.Anything).Return(2, nil).Once()
				admin.EXPECT().SetAccountDisabled(mock.Anything, "target-super", true).Return(nil).Once()
			},
			expectInvalidation: true,
		},
		{
			name: "delete",
			call: func(s *Server, ctx context.Context) error {
				_, err := s.DeleteAccount(ctx, &pb_admin.DeleteAccountRequest{Username: "target-super"})
				return err
			},
			allow: func(admin *mocks.MockAdmin) {
				admin.EXPECT().CountSuperAdmins(mock.Anything).Return(2, nil).Once()
				admin.EXPECT().DeleteAdmin(mock.Anything, "target-super").Return(nil).Once()
			},
			expectInvalidation: true,
		},
		{
			name: "demote",
			call: func(s *Server, ctx context.Context) error {
				_, err := s.UpdateAccountPermissions(ctx, &pb_admin.UpdateAccountPermissionsRequest{Username: "target-super"})
				return err
			},
			allow: func(admin *mocks.MockAdmin) {
				admin.EXPECT().CountSuperAdmins(mock.Anything).Return(2, nil).Once()
				admin.EXPECT().SetAccountPermissions(mock.Anything, "target-super", false, []entity.AdminPermission{}).Return(nil).Once()
				admin.EXPECT().GetAccountWithPermissions(mock.Anything, "target-super").
					Return(accountOf("target-super", false), nil).Once()
			},
		},
	}

	for _, op := range operations {
		t.Run("accounts writer cannot "+op.name+" a super", func(t *testing.T) {
			admin := mocks.NewMockAdmin(t)
			admin.EXPECT().GetAccountWithPermissions(mock.Anything, "target-super").
				Return(accountOf("target-super", true), nil).Once()
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Admin().Return(admin)
			s := &Server{repo: repo, pwhash: ph}

			err := op.call(s, ctxAs("accounts-writer", false, accountsWrite))
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Equal(t, superAccountManageRefusal, status.Convert(err).Message())
		})

		t.Run("full access can "+op.name+" a super", func(t *testing.T) {
			admin := mocks.NewMockAdmin(t)
			admin.EXPECT().GetAccountWithPermissions(mock.Anything, "target-super").
				Return(accountOf("target-super", true), nil).Once()
			op.allow(admin)
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Admin().Return(admin)
			s := &Server{repo: repo, pwhash: ph}
			var invalidated []string
			s.SetAdminAuthInvalidator(func(username string) { invalidated = append(invalidated, username) })

			err := op.call(s, ctxAs("owner", true, nil))
			require.NoError(t, err)
			if op.expectInvalidation {
				require.Equal(t, []string{"target-super"}, invalidated)
			} else {
				require.Empty(t, invalidated)
			}
		})
	}
}

func TestSetAccountDisabledInvalidatesAuthenticatedToken(t *testing.T) {
	adminStore := mocks.NewMockAdmin(t)
	authServer, err := authsrv.New(&authsrv.Config{
		JWTSecret:                "test-jwt-secret-at-least-32-bytes-long!!",
		MasterPassword:           "FJKqDyBvr9pAQMB3f8Uj4s",
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 1000,
		JWTTTL:                   "60m",
	}, adminStore)
	require.NoError(t, err)
	t.Cleanup(authServer.StopRateLimiter)

	token, err := authjwt.NewAdminToken(authServer.JwtAuth, time.Hour, "worker", false, nil, nil)
	require.NoError(t, err)
	adminStore.EXPECT().GetAdminByUsername(mock.Anything, "worker").
		Return(&entity.Admin{Id: 7, Username: "worker"}, nil).Once()
	adminStore.EXPECT().GetAccountWithPermissions(mock.Anything, "worker").
		Return(accountOf("worker", false), nil).Once()
	adminStore.EXPECT().SetAccountDisabled(mock.Anything, "worker", true).Return(nil).Once()
	adminStore.EXPECT().GetAdminByUsername(mock.Anything, "worker").
		Return(&entity.Admin{Id: 7, Username: "worker", Disabled: true}, nil).Once()

	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Admin().Return(adminStore)
	adminServer := &Server{repo: repo}
	adminServer.SetAdminAuthInvalidator(authServer.InvalidateAdminAccount)

	interceptor := authServer.UnaryAdminAuthInterceptor()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		strings.ToLower(authsrv.AuthMetadataKey): "Bearer " + token,
	}))
	call := func() (bool, error) {
		called := false
		_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/admin.AdminService/GetDictionary"},
			func(context.Context, any) (any, error) {
				called = true
				return "ok", nil
			})
		return called, err
	}

	called, err := call()
	require.NoError(t, err)
	require.True(t, called)

	_, err = adminServer.SetAccountDisabled(ctxAs("owner", true, nil),
		&pb_admin.SetAccountDisabledRequest{Username: "worker", Disabled: true})
	require.NoError(t, err)

	called, err = call()
	require.False(t, called)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// TestCreateAccountRefusesReservedAIActors (REVIEW-FIXD P2 #1) — the AI ledger books background and
// unattributed calls under "system" / "unknown"; an admin account with either name would share their
// spend line. Mixed case and padding are the same name once normalizeUsername has run, so they are
// refused too.
//
// MUTATION IT CATCHES: removing the AIActorIsReserved check from CreateAccount (the reserved names
// reach AddAccount — the mock has no expectation for it and fails the test).
func TestCreateAccountRefusesReservedAIActors(t *testing.T) {
	ph, err := pwhash.New(16, 1000)
	require.NoError(t, err)

	for _, name := range []string{entity.AIActorSystem, entity.AIActorUnknown, "System", " UNKNOWN ", "sYsTeM"} {
		t.Run(name, func(t *testing.T) {
			// No expectations on repo: a refusal that came after the store call fails here.
			s := &Server{repo: mocks.NewMockRepository(t), pwhash: ph}

			_, err := s.CreateAccount(ctxAs("owner", true, nil),
				&pb_admin.CreateAccountRequest{Username: name, Password: "long-enough-pw"})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "this name is reserved for system activity")
			var fields []string
			for _, d := range status.Convert(err).Details() {
				if br, ok := d.(*errdetails.BadRequest); ok {
					for _, fv := range br.GetFieldViolations() {
						fields = append(fields, fv.GetField())
					}
				}
			}
			require.Equal(t, []string{"username"}, fields, "the refusal names the field it is about")
		})
	}

	t.Run("a name that merely contains a reserved word is accepted", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().AddAccount(mock.Anything, "systemadmin", mock.AnythingOfType("string"), false,
			[]entity.AdminPermission{{Section: rbac.SectionOrders, Access: entity.AccessRead}}).
			Return(nil).Once()
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "systemadmin").
			Return(accountOf("systemadmin", false), nil).Once()
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo, pwhash: ph}

		_, err := s.CreateAccount(ctxAs("owner", true, nil),
			&pb_admin.CreateAccountRequest{
				Username:    "SystemAdmin",
				Password:    "long-enough-pw",
				Permissions: []*pb_admin.AdminPermission{{Section: rbac.SectionOrders, Access: pb_admin.AccessLevel_ACCESS_LEVEL_READ}},
			})
		require.NoError(t, err)
	})
}
