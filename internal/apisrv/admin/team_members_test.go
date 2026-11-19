package admin

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnitDefaultInvitePermissions(t *testing.T) {
	perms := DefaultInvitePermissions([]feature.Key{feature.Products, feature.Orders})
	got := map[string]entity.AccessLevel{}
	for _, p := range perms {
		got[p.Section] = p.Access
	}
	require.Equal(t, entity.AccessWrite, got[rbac.SectionProducts])
	require.Equal(t, entity.AccessWrite, got[rbac.SectionOrders])
	require.Equal(t, entity.AccessWrite, got[rbac.SectionDictionaries], "core sections are always granted")
	require.NotContains(t, got, rbac.SectionAccounts, "accounts is never a default grant")
	require.NotContains(t, got, rbac.SectionPromo, "a switched-off feature's section is not granted")
	for s := range got {
		require.True(t, rbac.ValidSection(s))
	}
}

func TestUnitInviteRef(t *testing.T) {
	pep := strings.Repeat("p", 32)
	a, b := InviteRef(pep, 41), InviteRef(pep, 42)
	require.Len(t, a, inviteRefLen)
	require.NotEqual(t, a, b)
	require.Equal(t, a, InviteRef(pep, 41), "stable")
	require.NotEqual(t, a, InviteRef(strings.Repeat("q", 32), 41), "keyed by the pepper")
	require.NotContains(t, a, "41")
}

func TestTeamMembersOff(t *testing.T) {
	adm := mocks.NewMockAdmin(t)
	adm.EXPECT().ListAccounts(mock.Anything).Return([]entity.AdminAccount{
		{Admin: entity.Admin{Username: "boss", IsSuper: true}}, {Admin: entity.Admin{Username: "kim"}},
		{Admin: entity.Admin{Username: "gone", Disabled: true}},
	}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Admin().Return(adm)
	s := &Server{repo: repo}
	ctx := authsrv.PutAdminUsername(context.Background(), "kim")
	// The edge's old rd(accounts) gate now lives in the handler (TestTeamMembersOffGate).
	ctx = authsrv.PutAdminAuthz(ctx, authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{rbac.SectionAccounts: entity.AccessRead}})
	res, err := s.ListTeamMembers(ctx, &pb_admin.ListTeamMembersRequest{})
	require.NoError(t, err)
	require.Len(t, res.Members, 2)
	require.Equal(t, "owner", res.Members[0].Role)
	require.True(t, res.Members[1].You)
	require.Empty(t, res.Invites)
	_, err = s.InviteTeamMember(ctx, &pb_admin.InviteTeamMemberRequest{Email: "a@x.io"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, "teams_off", status.Convert(err).Message())
}

const membersDB = "admin_members_cp_test"

func membersDSN(t *testing.T, db string) string {
	t.Helper()
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI set but MYSQL_HOST empty")
		}
		t.Skip("no test MySQL (run through test-db.sh)")
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

func membersStore(t *testing.T) *platformstore.Store {
	t.Helper()
	d := membersDSN(t, membersDB)
	require.NoError(t, testdbguard.CheckMySQLDSN(d))
	root, err := sql.Open("mysql", membersDSN(t, ""))
	require.NoError(t, err)
	defer root.Close()
	for _, q := range []string{"DROP DATABASE IF EXISTS `" + membersDB + "`", "CREATE DATABASE `" + membersDB + "` CHARACTER SET utf8mb4"} {
		_, err := root.Exec(q)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		if r, err := sql.Open("mysql", membersDSN(t, "")); err == nil {
			_, _ = r.Exec("DROP DATABASE IF EXISTS `" + membersDB + "`")
			r.Close()
		}
	})
	ctx := context.Background()
	st, err := platformstore.Open(ctx, d)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	require.NoError(t, st.EnsureTeamOne(ctx, "grbpwr_test", []platformstore.TeamOneAdmin{{Username: "boss", PasswordHash: "h", Super: true}}))
	for _, q := range []string{
		`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES
 (101, 'ann@x.io', 'ann', 'h', 'active'), (102, 'bob@x.io', 'bob', 'h', 'active'), (103, 'carl@x.io', 'carl', 'h', 'active')`,
		`INSERT INTO team (id, slug, name, db_name, status) VALUES (2, 'acme', 'Acme', 'lk_t_acme0001', 'active')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (2, 101, 'owner', 'active'), (2, 102, 'member', 'active'), (2, 103, 'member', 'active')`,
		`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('sbob0000000000000000000000000000', 102, 2, 'team'), ('scarl000000000000000000000000000', 103, 2, 'team')`,
	} {
		_, err := st.DB().Exec(q)
		require.NoError(t, err)
	}
	return st
}

func TestTeamMembersFlow(t *testing.T) {
	st := membersStore(t)
	cfg := platformstore.Config{BaseDomain: "beta.lekalo.studio", TokenPepper: strings.Repeat("p", 32)}
	b, err := tenantbind.New(tenant.TeamOne("grbpwr_test"))
	require.NoError(t, err)
	b.UsePlatform(st, cfg)

	adm := mocks.NewMockAdmin(t)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Admin().Return(adm).Maybe()
	// The list reads each member's grants from the team database (teams W); none here.
	adm.EXPECT().ListAccounts(mock.Anything).Return(nil, nil).Maybe()
	s := &Server{repo: repo}
	s.SetSessionInvalidator(b.InvalidateSession)
	s.SetPlatform(st, cfg) // after the invalidator: SetPlatform keeps it
	require.NotNil(t, s.platform.invalidateSession)

	acme := tenant.Team{ID: 2, Slug: "acme", Name: "Acme", DBName: "lk_t_acme0001", Status: tenant.StatusActive}
	as := func(handle string, uid int64, role string) context.Context {
		ctx := authsrv.PutAdminUsername(context.Background(), handle)
		ctx = authsrv.PutWorkspace(ctx, authsrv.Workspace{Team: acme, Role: role, UserID: uid, Features: []feature.Key{feature.Products}})
		return tenant.With(ctx, acme)
	}
	ann, bob := as("ann", 101, "owner"), as("bob", 102, "member")
	msg := func(err error) string { return status.Code(err).String() + " " + status.Convert(err).Message() }

	// A member cannot make an owner, by invite or by role.
	_, err = s.InviteTeamMember(bob, &pb_admin.InviteTeamMemberRequest{Email: "x@x.io", Role: "owner"})
	require.Equal(t, "PermissionDenied owner_only", msg(err))
	_, err = s.SetTeamMemberRole(bob, &pb_admin.SetTeamMemberRoleRequest{Username: "bob", Role: "owner"})
	require.Equal(t, "PermissionDenied owner_only", msg(err))

	inv, err := s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: " New@X.io ", Role: "member"})
	require.NoError(t, err)
	require.Equal(t, "new@x.io", inv.Invite.Email)
	require.Len(t, inv.Invite.InviteRef, inviteRefLen)
	require.Equal(t, "pending", inv.Invite.Status)
	var payload string
	require.NoError(t, st.DB().Get(&payload, `SELECT payload FROM platform_email WHERE to_email = 'new@x.io' ORDER BY id DESC LIMIT 1`))
	require.Contains(t, payload, "https://acme.beta.lekalo.studio/join#token=")
	require.NotContains(t, payload, `"code"`)
	var perms string
	require.NoError(t, st.DB().Get(&perms, `SELECT permissions FROM team_invite WHERE email = 'new@x.io'`))
	require.Contains(t, perms, `"products"`)
	require.NotContains(t, perms, `"accounts"`)

	list, err := s.ListTeamMembers(ann, &pb_admin.ListTeamMembersRequest{})
	require.NoError(t, err)
	require.Len(t, list.Members, 3)
	require.Len(t, list.Invites, 1)
	require.Equal(t, inv.Invite.InviteRef, list.Invites[0].InviteRef)
	for _, m := range list.Members {
		require.Equal(t, m.Username == "ann", m.You)
	}

	var before, after string
	require.NoError(t, st.DB().Get(&before, `SELECT token_hash FROM team_invite WHERE email = 'new@x.io'`))
	_, err = s.ResendTeamInvite(ann, &pb_admin.ResendTeamInviteRequest{InviteRef: inv.Invite.InviteRef})
	require.NoError(t, err)
	require.NoError(t, st.DB().Get(&after, `SELECT token_hash FROM team_invite WHERE email = 'new@x.io'`))
	require.NotEqual(t, before, after, "resend kills the old token")
	// Every people change is owner only: a member with accounts rights still cannot invite, resend,
	// revoke or remove.
	_, err = s.InviteTeamMember(bob, &pb_admin.InviteTeamMemberRequest{Email: "y@x.io", Role: "member"})
	require.Equal(t, "PermissionDenied owner_only", msg(err))
	_, err = s.ResendTeamInvite(bob, &pb_admin.ResendTeamInviteRequest{InviteRef: inv.Invite.InviteRef})
	require.Equal(t, "PermissionDenied owner_only", msg(err))
	_, err = s.RevokeTeamInvite(bob, &pb_admin.RevokeTeamInviteRequest{InviteRef: inv.Invite.InviteRef})
	require.Equal(t, "PermissionDenied owner_only", msg(err))
	_, err = s.RemoveTeamMember(bob, &pb_admin.RemoveTeamMemberRequest{Username: "carl"})
	require.Equal(t, "PermissionDenied owner_only", msg(err))
	// Listing stays open to the member.
	_, err = s.ListTeamMembers(bob, &pb_admin.ListTeamMembersRequest{})
	require.NoError(t, err)

	_, err = s.RevokeTeamInvite(ann, &pb_admin.RevokeTeamInviteRequest{InviteRef: inv.Invite.InviteRef})
	require.NoError(t, err)
	_, err = s.RevokeTeamInvite(ann, &pb_admin.RevokeTeamInviteRequest{InviteRef: inv.Invite.InviteRef})
	require.Equal(t, codes.NotFound, status.Code(err))

	// The last owner can never be removed or demoted.
	_, err = s.RemoveTeamMember(ann, &pb_admin.RemoveTeamMemberRequest{Username: "ann"})
	require.Equal(t, "FailedPrecondition last_owner", msg(err))
	_, err = s.SetTeamMemberRole(ann, &pb_admin.SetTeamMemberRoleRequest{Username: "ann", Role: "member"})
	require.Equal(t, "FailedPrecondition last_owner", msg(err))

	// Removal: the old token's session is refused on the very next check (edge cache dropped).
	ctx := context.Background()
	role, live, err := b.TeamSession(ctx, "sbob0000000000000000000000000000", 102, 2)
	require.NoError(t, err)
	require.True(t, live)
	require.Equal(t, "member", role)
	adm.EXPECT().GetAdminByUsername(mock.Anything, "bob").Return(&entity.Admin{Username: "bob"}, nil).Once()
	adm.EXPECT().SetAccountDisabled(mock.Anything, "bob", true).Return(nil).Once()
	_, err = s.RemoveTeamMember(ann, &pb_admin.RemoveTeamMemberRequest{Username: "bob"})
	require.NoError(t, err)
	_, live, err = b.TeamSession(ctx, "sbob0000000000000000000000000000", 102, 2)
	require.NoError(t, err)
	require.False(t, live, "removed member's token must be refused at once")

	// Role change: carl becomes owner, his session dies so the new claims apply.
	_, live, _ = b.TeamSession(ctx, "scarl000000000000000000000000000", 103, 2)
	require.True(t, live)
	adm.EXPECT().GetAdminByUsername(mock.Anything, "carl").Return(&entity.Admin{Username: "carl"}, nil).Once()
	adm.EXPECT().SetAccountPermissions(mock.Anything, "carl", true, []entity.AdminPermission(nil)).Return(nil).Once()
	_, err = s.SetTeamMemberRole(ann, &pb_admin.SetTeamMemberRoleRequest{Username: "carl", Role: "owner"})
	require.NoError(t, err)
	_, live, _ = b.TeamSession(ctx, "scarl000000000000000000000000000", 103, 2)
	require.False(t, live, "re-roled member's token must be refused at once")

	// carl's team-side row did not become super (the mirror write failed): he is no working owner,
	// so ann cannot step down yet.
	adm.EXPECT().GetAdminByUsername(mock.Anything, "carl").Return(&entity.Admin{Username: "carl"}, nil).Once()
	_, err = s.SetTeamMemberRole(ann, &pb_admin.SetTeamMemberRoleRequest{Username: "ann", Role: "member"})
	require.Equal(t, "FailedPrecondition last_owner", msg(err))
	// A retry with the role carl already has repairs his row instead of answering "same role".
	adm.EXPECT().GetAdminByUsername(mock.Anything, "ann").Return(&entity.Admin{Username: "ann", IsSuper: true}, nil).Once()
	adm.EXPECT().GetAdminByUsername(mock.Anything, "carl").Return(&entity.Admin{Username: "carl"}, nil).Once()
	adm.EXPECT().SetAccountPermissions(mock.Anything, "carl", true, []entity.AdminPermission(nil)).Return(nil).Once()
	_, err = s.SetTeamMemberRole(ann, &pb_admin.SetTeamMemberRoleRequest{Username: "carl", Role: "owner"})
	require.NoError(t, err)

	// With carl a confirmed owner, ann may step down: a former super gets the default grant.
	adm.EXPECT().GetAdminByUsername(mock.Anything, "carl").Return(&entity.Admin{Username: "carl", IsSuper: true}, nil).Once()
	adm.EXPECT().GetAccountWithPermissions(mock.Anything, "ann").Return(&entity.AdminAccount{Admin: entity.Admin{Username: "ann", IsSuper: true}}, nil).Once()
	adm.EXPECT().GetAdminByUsername(mock.Anything, "ann").Return(&entity.Admin{Username: "ann", IsSuper: true}, nil).Once()
	adm.EXPECT().SetAccountPermissions(mock.Anything, "ann", false, mock.MatchedBy(func(p []entity.AdminPermission) bool { return len(p) > 0 })).Return(nil).Once()
	_, err = s.SetTeamMemberRole(ann, &pb_admin.SetTeamMemberRoleRequest{Username: "ann", Role: "member"})
	require.NoError(t, err)

	var audits int
	require.NoError(t, st.DB().Get(&audits, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action LIKE 'member_%'`))
	require.Equal(t, 6, audits) // invite, resend, revoke, remove, role ×2 (the repair is not a change)
}
