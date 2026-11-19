package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The workspace screen against a real control plane (CI: MYSQL_HOST). Team 2 (acme) of membersStore:
// ann 101 owner, bob 102 member, carl 103 member; team #1 created by EnsureTeamOne (brand seeded).

type workspaceWorld struct {
	st  *platformstore.Store
	s   *Server
	adm *mocks.MockAdmin
	b   *tenantbind.Binder
	// as is a call by handle in acme, as the edge resolves it (the standard profile's features).
	as func(handle string, uid int64, role string) context.Context
}

func newWorkspaceWorld(t *testing.T) workspaceWorld {
	t.Helper()
	st := membersStore(t)
	cfg := platformstore.Config{BaseDomain: "beta.lekalo.studio", TokenPepper: strings.Repeat("p", 32)}
	b, err := tenantbind.New(tenant.TeamOne("grbpwr_test"))
	require.NoError(t, err)
	b.UsePlatform(st, cfg)
	adm := mocks.NewMockAdmin(t)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Admin().Return(adm).Maybe()
	s := &Server{repo: repo}
	s.SetSessionInvalidator(b.InvalidateSession)
	s.SetPlatform(st, cfg)
	acme := tenant.Team{ID: 2, Slug: "acme", Name: "Acme", DBName: "lk_t_acme0001", Status: tenant.StatusActive}
	as := func(handle string, uid int64, role string) context.Context {
		ctx := authsrv.PutAdminUsername(context.Background(), handle)
		ctx = authsrv.PutWorkspace(ctx, authsrv.Workspace{Team: acme, Role: role, UserID: uid, Features: feature.DefaultOn()})
		return tenant.With(ctx, acme)
	}
	return workspaceWorld{st: st, s: s, adm: adm, b: b, as: as}
}

func (w workspaceWorld) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, w.st.DB().Get(&n, q, args...))
	return n
}

func TestTeamBrandFlow(t *testing.T) {
	w := newWorkspaceWorld(t)
	ann, bob := w.as("ann", 101, "owner"), w.as("bob", 102, "member")

	// Team #1 is seeded with today's papers' data; a new team has an empty brand, not "unavailable".
	one := w.s.teamBrand(context.Background(), tenant.TeamOneID)
	require.Equal(t, "GRBPWR", one.BrandName)
	require.Equal(t, "GRBPWR LIMITED", one.LegalEntity)
	require.Equal(t, []string{"167-169 GREAT PORTLAND STREET", "5TH FLOOR, LONDON, W1W 5PF"}, one.LegalAddress)
	require.Equal(t, "customercare@grbpwr.com", one.ContactEmail)
	require.Equal(t, "grbpwr.com", one.Website)
	require.True(t, one.FirstTeam)
	ws := w.s.workspaceToProto(ann)
	require.NotNil(t, ws.Brand)
	require.False(t, ws.Brand.Unavailable)
	require.False(t, ws.Brand.FirstTeam)
	require.Empty(t, ws.Brand.BrandName)

	// Only an owner writes; the request names no team.
	_, err := w.s.SetTeamBrand(bob, &pb_admin.SetTeamBrandRequest{BrandName: "Bob"})
	require.Equal(t, "PermissionDenied owner_only", refusal(err))
	_, err = w.s.SetTeamBrand(ann, &pb_admin.SetTeamBrandRequest{ContactEmail: "nope"})
	require.Equal(t, "InvalidArgument bad_email", refusal(err))

	res, err := w.s.SetTeamBrand(ann, &pb_admin.SetTeamBrandRequest{
		BrandName: "  Acme  Atelier ", LegalEntity: "Acme Ltd", LegalAddress: []string{"", "1 Main St", "Springfield"},
		ContactEmail: "Hi@Acme.io", Website: "https://Acme.io/",
	})
	require.NoError(t, err)
	require.Equal(t, "Acme Atelier", res.Brand.BrandName)
	require.Equal(t, []string{"1 Main St", "Springfield"}, res.Brand.LegalAddress)
	require.Equal(t, "hi@acme.io", res.Brand.ContactEmail)
	require.Equal(t, "acme.io", res.Brand.Website)
	require.False(t, res.Brand.FirstTeam)
	ws = w.s.workspaceToProto(bob) // a member reads it
	require.Equal(t, "Acme Atelier", ws.Brand.BrandName)
	require.Equal(t, "Acme Ltd", ws.Brand.LegalEntity)
	var by int64
	require.NoError(t, w.st.DB().Get(&by, `SELECT updated_by FROM team_brand WHERE team_id = 2`))
	require.Equal(t, int64(101), by)
	// Isolation: team #1's row is untouched.
	require.Equal(t, "GRBPWR", w.s.teamBrand(context.Background(), tenant.TeamOneID).BrandName)
	// One audit per real change, field names only.
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'team_brand_set'`))
	var detail string
	require.NoError(t, w.st.DB().Get(&detail, `SELECT CAST(detail AS CHAR) FROM platform_audit WHERE team_id = 2 AND action = 'team_brand_set'`))
	require.Contains(t, detail, "brand_name,legal_entity,legal_address,contact_email,website")
	require.NotContains(t, detail, "Acme")
	_, err = w.s.SetTeamBrand(ann, &pb_admin.SetTeamBrandRequest{BrandName: "Acme Atelier", LegalEntity: "Acme Ltd",
		LegalAddress: []string{"1 Main St", "Springfield"}, ContactEmail: "hi@acme.io", Website: "acme.io"})
	require.NoError(t, err)
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'team_brand_set'`), "a same save is no change")

	// A paused team refuses the write inside the store (the edge refuses it first in production).
	_, err = w.st.DB().Exec(`UPDATE team SET status = 'suspended' WHERE id = 2`)
	require.NoError(t, err)
	_, err = w.s.SetTeamBrand(ann, &pb_admin.SetTeamBrandRequest{BrandName: "Paused"})
	require.Equal(t, "FailedPrecondition team_read_only", refusal(err))
	require.Equal(t, "Acme Atelier", w.s.teamBrand(context.Background(), 2).BrandName)
}

func TestTeamMemberAccessFlow(t *testing.T) {
	w := newWorkspaceWorld(t)
	ann, bob := w.as("ann", 101, "owner"), w.as("bob", 102, "member")
	read, write := pb_admin.AccessLevel_ACCESS_LEVEL_READ, pb_admin.AccessLevel_ACCESS_LEVEL_WRITE
	grant := func(section string, a pb_admin.AccessLevel) *pb_admin.AdminPermission {
		return &pb_admin.AdminPermission{Section: section, Access: a}
	}

	// The list: the grantable set in screen order, a member's grants inside it, none for an owner.
	w.adm.EXPECT().ListAccounts(mock.Anything).Return([]entity.AdminAccount{
		{Admin: entity.Admin{Username: "ann", IsSuper: true}},
		{Admin: entity.Admin{Username: "Bob"}, Permissions: []entity.AdminPermission{
			{Section: rbac.SectionOrders, Access: entity.AccessWrite}, {Section: rbac.SectionTasks, Access: entity.AccessWrite}}},
	}, nil).Once()
	list, err := w.s.ListTeamMembers(bob, &pb_admin.ListTeamMembersRequest{})
	require.NoError(t, err, "a member opens the screen")
	require.Len(t, list.Sections, 10)
	require.Equal(t, "tasks", list.Sections[0].Title)
	require.Equal(t, "dictionaries", list.Sections[9].Title)
	for _, m := range list.Members {
		switch m.Username {
		case "ann":
			require.NotNil(t, m.Permissions)
			require.Empty(t, m.Permissions)
		case "bob":
			require.Len(t, m.Permissions, 1)
			require.Equal(t, rbac.SectionTasks, m.Permissions[0].Section)
		case "carl":
			require.Empty(t, m.Permissions)
		}
	}

	// Refusals before anything is written (strict mocks: no admins call).
	_, err = w.s.SetTeamMemberAccess(bob, &pb_admin.SetTeamMemberAccessRequest{Username: "carl"})
	require.Equal(t, "PermissionDenied owner_only", refusal(err))
	_, err = w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "ann"})
	require.Equal(t, "FailedPrecondition owner_full", refusal(err))
	_, err = w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "nobody"})
	require.Equal(t, "NotFound not_member", refusal(err))
	_, err = w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "bob",
		Permissions: []*pb_admin.AdminPermission{grant(rbac.SectionAccounts, read)}})
	require.Equal(t, "InvalidArgument bad_section", refusal(err))
	_, err = w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "bob",
		Permissions: []*pb_admin.AdminPermission{grant(rbac.SectionTasks, pb_admin.AccessLevel_ACCESS_LEVEL_UNSPECIFIED)}})
	require.Equal(t, "InvalidArgument bad_access", refusal(err))

	// bob holds accounts write (an adopted team #1 admin can): staged — disabled with the new rights,
	// session revoked, enabled. The switched-off orders grant is kept, the accounts grant is not.
	ctx := context.Background()
	_, live, err := w.b.TeamSession(ctx, "sbob0000000000000000000000000000", 102, 2)
	require.NoError(t, err)
	require.True(t, live)
	w.adm.EXPECT().GetAccountWithPermissions(mock.Anything, "bob").Return(&entity.AdminAccount{Admin: entity.Admin{Username: "bob"},
		Permissions: []entity.AdminPermission{{Section: rbac.SectionAccounts, Access: entity.AccessWrite},
			{Section: rbac.SectionOrders, Access: entity.AccessWrite}, {Section: rbac.SectionTasks, Access: entity.AccessRead}}}, nil).Once()
	w.adm.EXPECT().GetAdminByUsername(mock.Anything, "bob").Return(&entity.Admin{Username: "bob"}, nil).Once()
	w.adm.EXPECT().SetAccountDisabled(mock.Anything, "bob", true).Return(nil).Once()
	w.adm.EXPECT().SetAccountPermissions(mock.Anything, "bob", false, []entity.AdminPermission{
		{Section: rbac.SectionTasks, Access: entity.AccessWrite}, {Section: rbac.SectionDictionaries, Access: entity.AccessRead},
		{Section: rbac.SectionOrders, Access: entity.AccessWrite},
	}).Return(nil).Once()
	w.adm.EXPECT().GetAdminByUsername(mock.Anything, "bob").Return(&entity.Admin{Username: "bob", Disabled: true}, nil).Once()
	w.adm.EXPECT().SetAccountDisabled(mock.Anything, "bob", false).Return(nil).Once()
	res, err := w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "bob",
		Permissions: []*pb_admin.AdminPermission{grant(rbac.SectionDictionaries, read), grant(rbac.SectionTasks, write)}})
	require.NoError(t, err)
	require.Equal(t, "member", res.Member.Role)
	require.Len(t, res.Member.Permissions, 2)
	require.Equal(t, rbac.SectionTasks, res.Member.Permissions[0].Section)
	_, live, err = w.b.TeamSession(ctx, "sbob0000000000000000000000000000", 102, 2)
	require.NoError(t, err)
	require.False(t, live, "the member's token must be refused at once, re-entry mints the new rights")
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'member_access'`))

	// The owner sets nothing: the row holds no accounts grant, only the switched-off orders one.
	w.adm.EXPECT().GetAccountWithPermissions(mock.Anything, "bob").Return(&entity.AdminAccount{Admin: entity.Admin{Username: "bob"},
		Permissions: []entity.AdminPermission{{Section: rbac.SectionAccounts, Access: entity.AccessWrite},
			{Section: rbac.SectionOrders, Access: entity.AccessWrite}}}, nil).Once()
	w.adm.EXPECT().GetAdminByUsername(mock.Anything, "bob").Return(&entity.Admin{Username: "bob"}, nil).Once()
	w.adm.EXPECT().SetAccountDisabled(mock.Anything, "bob", true).Return(nil).Once()
	w.adm.EXPECT().SetAccountPermissions(mock.Anything, "bob", false, []entity.AdminPermission{
		{Section: rbac.SectionOrders, Access: entity.AccessWrite},
	}).Return(nil).Once()
	w.adm.EXPECT().GetAdminByUsername(mock.Anything, "bob").Return(&entity.Admin{Username: "bob", Disabled: true}, nil).Once()
	w.adm.EXPECT().SetAccountDisabled(mock.Anything, "bob", false).Return(nil).Once()
	res, err = w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "bob"})
	require.NoError(t, err)
	require.Empty(t, res.Member.Permissions)

	// A paused workspace (as the edge resolved it) refuses before any write.
	paused := authsrv.PutWorkspace(ann, authsrv.Workspace{Team: tenant.Team{ID: 2, Slug: "acme", Status: tenant.StatusSuspended},
		Role: "owner", UserID: 101, Features: feature.DefaultOn(), ReadOnly: true})
	_, err = w.s.SetTeamMemberAccess(paused, &pb_admin.SetTeamMemberAccessRequest{Username: "carl"})
	require.Equal(t, "FailedPrecondition team_read_only", refusal(err))

	// Paused between the edge and the store: step (2) refuses, the row stays disabled with the new
	// rights (nothing put back), carl's session lives on but his token is refused by the disabled row.
	_, err = w.st.DB().Exec(`UPDATE team SET status = 'suspended' WHERE id = 2`)
	require.NoError(t, err)
	newRights := []entity.AdminPermission{{Section: rbac.SectionTasks, Access: entity.AccessRead}}
	w.adm.EXPECT().GetAccountWithPermissions(mock.Anything, "carl").Return(&entity.AdminAccount{Admin: entity.Admin{Username: "carl"},
		Permissions: []entity.AdminPermission{{Section: rbac.SectionFiles, Access: entity.AccessWrite}}}, nil).Once()
	w.adm.EXPECT().GetAdminByUsername(mock.Anything, "carl").Return(&entity.Admin{Username: "carl"}, nil).Once()
	w.adm.EXPECT().SetAccountDisabled(mock.Anything, "carl", true).Return(nil).Once()
	w.adm.EXPECT().SetAccountPermissions(mock.Anything, "carl", false, newRights).Return(nil).Once()
	_, err = w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "carl",
		Permissions: []*pb_admin.AdminPermission{grant(rbac.SectionTasks, read)}})
	require.Equal(t, "FailedPrecondition team_read_only", refusal(err))
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM user_session WHERE sid = 'scarl000000000000000000000000000' AND revoked_at IS NULL`))
	require.Equal(t, 2, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'member_access'`))

	// The retry once the team is active again: same rights, the session revoked, the row enabled.
	_, err = w.st.DB().Exec(`UPDATE team SET status = 'active' WHERE id = 2`)
	require.NoError(t, err)
	w.adm.EXPECT().GetAccountWithPermissions(mock.Anything, "carl").Return(&entity.AdminAccount{Admin: entity.Admin{Username: "carl", Disabled: true},
		Permissions: newRights}, nil).Once()
	w.adm.EXPECT().GetAdminByUsername(mock.Anything, "carl").Return(&entity.Admin{Username: "carl", Disabled: true}, nil).Once()
	w.adm.EXPECT().SetAccountPermissions(mock.Anything, "carl", false, newRights).Return(nil).Once()
	w.adm.EXPECT().GetAdminByUsername(mock.Anything, "carl").Return(&entity.Admin{Username: "carl", Disabled: true}, nil).Once()
	w.adm.EXPECT().SetAccountDisabled(mock.Anything, "carl", false).Return(nil).Once()
	_, err = w.s.SetTeamMemberAccess(ann, &pb_admin.SetTeamMemberAccessRequest{Username: "carl",
		Permissions: []*pb_admin.AdminPermission{grant(rbac.SectionTasks, read)}})
	require.NoError(t, err)
	require.Equal(t, 0, w.count(t, `SELECT COUNT(*) FROM user_session WHERE sid = 'scarl000000000000000000000000000' AND revoked_at IS NULL`))
	require.Equal(t, 3, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'member_access'`))
}

// TestTeamMembersListNeedsMembership: in teams mode the list answers only an active member of the
// calling team (or an entered operator). A legacy team #1 admin whose handle belongs to a platform
// user outside team #1 (EnsureTeamOne does not adopt it) gets no membership from the edge; the
// handler refuses it with a fixed key and reads nothing.
func TestTeamMembersListNeedsMembership(t *testing.T) {
	w := newWorkspaceWorld(t)
	one := tenant.Team{ID: tenant.TeamOneID, Slug: tenant.TeamOneSlug, Name: tenant.TeamOneName, DBName: "grbpwr_test", Status: tenant.StatusActive}
	call := func(handle string, uid int64, role string, operator bool) context.Context {
		ctx := authsrv.PutAdminUsername(context.Background(), handle)
		ctx = authsrv.PutWorkspace(ctx, authsrv.Workspace{Team: one, Role: role, UserID: uid, Operator: operator, Features: feature.DefaultOn()})
		return tenant.With(ctx, one)
	}
	// ann (platform user 101, acme only) as a legacy team #1 token, member and super alike.
	for _, role := range []string{"member", "owner"} {
		_, err := w.s.ListTeamMembers(call("ann", 0, role, false), &pb_admin.ListTeamMembersRequest{})
		require.Equal(t, "PermissionDenied not_member", refusal(err), role)
	}
	// A team token of a non-member is refused the same way.
	_, err := w.s.ListTeamMembers(call("ann", 101, "member", false), &pb_admin.ListTeamMembersRequest{})
	require.Equal(t, "PermissionDenied not_member", refusal(err))

	// boss (adopted, owner of team #1) by a legacy token, and an entered operator, are answered.
	w.adm.EXPECT().ListAccounts(mock.Anything).Return([]entity.AdminAccount{{Admin: entity.Admin{Username: "boss", IsSuper: true}}}, nil).Twice()
	list, err := w.s.ListTeamMembers(call("boss", 0, "owner", false), &pb_admin.ListTeamMembersRequest{})
	require.NoError(t, err)
	require.Len(t, list.Members, 1)
	_, err = w.s.ListTeamMembers(call("op", 101, "owner", true), &pb_admin.ListTeamMembersRequest{})
	require.NoError(t, err)
}

func TestTeamInviteRights(t *testing.T) {
	w := newWorkspaceWorld(t)
	ann := w.as("ann", 101, "owner")
	type grant struct {
		Section string `json:"section"`
		Access  string `json:"access"`
	}
	stored := func(email string) []grant {
		t.Helper()
		var raw string
		require.NoError(t, w.st.DB().Get(&raw, `SELECT CAST(permissions AS CHAR) FROM team_invite WHERE email = ?`, email))
		var out []grant
		require.NoError(t, json.Unmarshal([]byte(raw), &out))
		return out
	}
	p := func(section string, a pb_admin.AccessLevel) *pb_admin.AdminPermission {
		return &pb_admin.AdminPermission{Section: section, Access: a}
	}

	// No rights named: the team's default (write on the grantable set, screen order).
	_, err := w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "d@x.io", Role: "member"})
	require.NoError(t, err)
	def := stored("d@x.io")
	require.Len(t, def, 10)
	require.Equal(t, grant{rbac.SectionTasks, "write"}, def[0])

	// Named rights are stored as named; a re-invite without rights keeps them, one with rights replaces.
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "r@x.io", Role: "member",
		Permissions: []*pb_admin.AdminPermission{p(rbac.SectionFiles, pb_admin.AccessLevel_ACCESS_LEVEL_READ)}})
	require.NoError(t, err)
	require.Equal(t, []grant{{rbac.SectionFiles, "read"}}, stored("r@x.io"))
	var tok1, tok2 string
	require.NoError(t, w.st.DB().Get(&tok1, `SELECT token_hash FROM team_invite WHERE email = 'r@x.io'`))
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "R@x.io", Role: "member"})
	require.NoError(t, err)
	require.Equal(t, []grant{{rbac.SectionFiles, "read"}}, stored("r@x.io"), "a re-invite without rights keeps them")
	require.NoError(t, w.st.DB().Get(&tok2, `SELECT token_hash FROM team_invite WHERE email = 'r@x.io'`))
	require.NotEqual(t, tok1, tok2, "the old link dies")
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "r@x.io", Role: "member",
		Permissions: []*pb_admin.AdminPermission{p(rbac.SectionModels, pb_admin.AccessLevel_ACCESS_LEVEL_WRITE)}})
	require.NoError(t, err)
	require.Equal(t, []grant{{rbac.SectionModels, "write"}}, stored("r@x.io"))
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM team_invite WHERE email = 'r@x.io'`))
	// A fourth letter within the hour to that address: letter_cap, nothing changed.
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "r@x.io", Role: "member"})
	require.Equal(t, "ResourceExhausted letter_cap", refusal(err))

	// Bad rights refuse before anything is stored.
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "b@x.io", Role: "member",
		Permissions: []*pb_admin.AdminPermission{p(rbac.SectionAccounts, pb_admin.AccessLevel_ACCESS_LEVEL_READ)}})
	require.Equal(t, "InvalidArgument bad_section", refusal(err))
	require.Equal(t, 0, w.count(t, `SELECT COUNT(*) FROM team_invite WHERE email = 'b@x.io'`))

	// Point 8: an expired invite is re-used and re-sent (pending again); already_member only for an
	// active member of this team — a platform user of no team here is invited like anyone.
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "e@x.io", Role: "member"})
	require.NoError(t, err)
	_, err = w.st.DB().Exec(`UPDATE team_invite SET expires_at = '2000-01-01' WHERE email = 'e@x.io'`)
	require.NoError(t, err)
	inv, err := w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "e@x.io", Role: "member"})
	require.NoError(t, err)
	require.Equal(t, "pending", inv.Invite.Status)
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM team_invite WHERE email = 'e@x.io' AND expires_at > ?`, time.Now().UTC()))
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "bob@x.io", Role: "member"})
	require.Equal(t, "AlreadyExists already_member", refusal(err))
	_, err = w.st.DB().Exec(`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES (104, 'dan@x.io', 'dan', 'h', 'active')`)
	require.NoError(t, err)
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "dan@x.io", Role: "member"})
	require.NoError(t, err)
	_, err = w.st.DB().Exec(`UPDATE team_member SET status = 'removed' WHERE team_id = 2 AND user_id = 103`)
	require.NoError(t, err)
	_, err = w.s.InviteTeamMember(ann, &pb_admin.InviteTeamMemberRequest{Email: "carl@x.io", Role: "member"})
	require.NoError(t, err, "a removed member is no member")
}
