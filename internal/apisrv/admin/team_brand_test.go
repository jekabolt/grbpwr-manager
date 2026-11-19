package admin

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// refusal is "Code key" of a gRPC error, "" for nil.
func refusal(err error) string {
	if err == nil {
		return ""
	}
	return status.Code(err).String() + " " + status.Convert(err).Message()
}

func TestUnitNormalizeBrand(t *testing.T) {
	ok := func(req *pb_admin.SetTeamBrandRequest) platformstore.TeamBrand {
		t.Helper()
		b, err := normalizeBrand(req)
		require.NoError(t, err)
		return b
	}
	// Trim, collapse inner runs, case as typed, any script.
	b := ok(&pb_admin.SetTeamBrandRequest{
		BrandName:    "  Ateliér   Лекало  ",
		LegalEntity:  "ООО «Лекало»   Studio",
		LegalAddress: []string{"  ", "ул. Пушкина,   д. 1", "", "Москва  101000"},
		ContactEmail: "  Hello@Atelier.IO ",
		Website:      " HTTPS://Atelier.IO/ ",
	})
	require.Equal(t, platformstore.TeamBrand{
		BrandName:    "Ateliér Лекало",
		LegalEntity:  "ООО «Лекало» Studio",
		LegalAddress: []string{"ул. Пушкина, д. 1", "Москва 101000"},
		ContactEmail: "hello@atelier.io",
		Website:      "atelier.io",
	}, b)
	// Everything empty is a value: papers print nothing.
	require.Equal(t, platformstore.TeamBrand{}, ok(&pb_admin.SetTeamBrandRequest{LegalAddress: []string{"", " "}}))
	// Limits are runes, not bytes.
	require.Equal(t, strings.Repeat("ж", brandNameMax), ok(&pb_admin.SetTeamBrandRequest{BrandName: strings.Repeat("ж", brandNameMax)}).BrandName)
	require.Equal(t, strings.Repeat("ж", legalEntityMax), ok(&pb_admin.SetTeamBrandRequest{LegalEntity: strings.Repeat("ж", legalEntityMax)}).LegalEntity)
	require.Len(t, ok(&pb_admin.SetTeamBrandRequest{LegalAddress: []string{"a", "", "b", " ", "c"}}).LegalAddress, 3)
	for _, site := range []struct{ in, want string }{
		{"grbpwr.com", "grbpwr.com"},
		{"http://Shop.Example.com/", "shop.example.com"},
		{"https://example.com/brand", "example.com/brand"},
		{"https://example.com/brand/", "example.com/brand"},
		{"лекало.рф", "лекало.рф"},
	} {
		require.Equal(t, site.want, ok(&pb_admin.SetTeamBrandRequest{Website: site.in}).Website, site.in)
	}

	bad := map[string]*pb_admin.SetTeamBrandRequest{
		"name newline":        {BrandName: "a\nb"},
		"name bell":           {BrandName: "a\u0007b"},
		"name tab":            {BrandName: "a\tb"},
		"name line separator": {BrandName: "a b"},
		"name too long":       {BrandName: strings.Repeat("ж", brandNameMax+1)},
		"entity too long":     {LegalEntity: strings.Repeat("a", legalEntityMax+1)},
		"entity control":      {LegalEntity: "x\x00"},
		"address line long":   {LegalAddress: []string{strings.Repeat("a", addressLineMax+1)}},
		"address newline":     {LegalAddress: []string{"a\r\nb"}},
		"name not utf8":       {BrandName: "\xff"},
	}
	for name, req := range bad {
		_, err := normalizeBrand(req)
		require.Equal(t, "InvalidArgument bad_text", refusal(err), name)
	}
	_, err := normalizeBrand(&pb_admin.SetTeamBrandRequest{LegalAddress: []string{"a", "b", "", "c", "d"}})
	require.Equal(t, "InvalidArgument bad_address", refusal(err))
	for _, email := range []string{"nope", "a@b", "Ann <a@b.io>", "a\n@b.io", "почта@b.io"} {
		_, err := normalizeBrand(&pb_admin.SetTeamBrandRequest{ContactEmail: email})
		require.Equal(t, "InvalidArgument bad_email", refusal(err), email)
	}
	for _, site := range []string{"localhost", "exa mple.com", "ftp://example.com", ".example.com", "example.", "https://", "a.b/" + strings.Repeat("x", websiteMax)} {
		_, err := normalizeBrand(&pb_admin.SetTeamBrandRequest{Website: site})
		require.Equal(t, "InvalidArgument bad_website", refusal(err), site)
	}
}

func TestUnitWorkspaceBrand(t *testing.T) {
	// A read error: never nil (nil tells the papers to print the GRBPWR constants).
	got := workspaceBrand(platformstore.TeamBrand{BrandName: "X"}, true, errors.New("down"), false)
	require.NotNil(t, got)
	require.True(t, got.Unavailable)
	require.Empty(t, got.BrandName)
	require.False(t, got.FirstTeam)
	require.True(t, workspaceBrand(platformstore.TeamBrand{}, false, errors.New("down"), true).FirstTeam)
	// No row: empty values, available.
	got = workspaceBrand(platformstore.TeamBrand{}, false, nil, false)
	require.False(t, got.Unavailable)
	require.Empty(t, got.BrandName)
	require.NotNil(t, got.LegalAddress)
	// A row.
	got = workspaceBrand(platformstore.TeamBrand{BrandName: "GRBPWR", LegalAddress: []string{"a", "b"}, Website: "grbpwr.com"}, true, nil, true)
	require.Equal(t, "GRBPWR", got.BrandName)
	require.Equal(t, []string{"a", "b"}, got.LegalAddress)
	require.True(t, got.FirstTeam)
	require.False(t, got.Unavailable)
}

// TestUnitTeamsOffWorkspace: with teams mode off (prod today) the workspace carries no brand and
// every workspace write answers teams_off before touching anything (strict mocks, no calls).
func TestUnitTeamsOffWorkspace(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	ctx := authsrv.PutAdminUsername(context.Background(), "boss")
	ctx = authsrv.PutAdminAuthz(ctx, authsrv.AdminAuthz{Super: true})
	require.Nil(t, s.teamBrand(ctx, 1))
	_, err := s.SetTeamBrand(ctx, &pb_admin.SetTeamBrandRequest{BrandName: "x"})
	require.Equal(t, "FailedPrecondition teams_off", refusal(err))
	_, err = s.SetTeamMemberAccess(ctx, &pb_admin.SetTeamMemberAccessRequest{Username: "kim"})
	require.Equal(t, "FailedPrecondition teams_off", refusal(err))
}

func TestUnitTeamSections(t *testing.T) {
	words := func(secs []rbac.SectionInfo) []string {
		out := make([]string, 0, len(secs))
		for _, s := range secs {
			out = append(out, s.Title)
		}
		return out
	}
	// An ordinary team (the standard profile): the workspace screen's ten, in its order.
	std := teamSections(feature.DefaultOn())
	require.Equal(t, []string{"tasks", "files", "media", "models", "fittings", "tech cards", "materials",
		"production", "costing", "dictionaries"}, words(std))
	require.Equal(t, rbac.SectionContent, std[2].Key)
	require.Equal(t, rbac.SectionInventory, std[6].Key)
	// Every feature on: the ten first, then the rest; accounts never.
	var all []feature.Key
	for _, info := range feature.Catalog() {
		all = append(all, info.Key)
	}
	full := teamSections(all)
	require.Len(t, full, len(rbac.Sections())-1)
	require.Equal(t, words(std), words(full[:10]))
	for _, s := range full {
		require.NotEqual(t, rbac.SectionAccounts, s.Key)
		require.True(t, rbac.ValidSection(s.Key))
		require.Empty(t, s.Description)
	}
	// The core section stays with no feature on.
	require.Equal(t, []string{"dictionaries"}, words(teamSections(nil)))
	// The default invite grant is the same set, write.
	def := DefaultInvitePermissions(feature.DefaultOn())
	require.Len(t, def, len(std))
	for i, p := range def {
		require.Equal(t, std[i].Key, p.Section)
		require.Equal(t, entity.AccessWrite, p.Access)
	}
}

func TestUnitTeamGrants(t *testing.T) {
	secs := teamSections(feature.DefaultOn())
	p := func(section string, a pb_admin.AccessLevel) *pb_admin.AdminPermission {
		return &pb_admin.AdminPermission{Section: section, Access: a}
	}
	read, write := pb_admin.AccessLevel_ACCESS_LEVEL_READ, pb_admin.AccessLevel_ACCESS_LEVEL_WRITE
	got, err := teamGrants(secs, []*pb_admin.AdminPermission{p(rbac.SectionDictionaries, read), p(rbac.SectionTasks, write)})
	require.NoError(t, err)
	require.Equal(t, []entity.AdminPermission{{Section: rbac.SectionTasks, Access: entity.AccessWrite},
		{Section: rbac.SectionDictionaries, Access: entity.AccessRead}}, got, "screen order")
	got, err = teamGrants(secs, nil)
	require.NoError(t, err)
	require.Empty(t, got, "no rights is a valid set")

	for name, in := range map[string][]*pb_admin.AdminPermission{
		"accounts":          {p(rbac.SectionAccounts, read)},
		"feature off":       {p(rbac.SectionOrders, write)},
		"unknown":           {p("nope", read)},
		"twice":             {p(rbac.SectionTasks, read), p(rbac.SectionTasks, write)},
		"empty section key": {p("", read)},
	} {
		_, err := teamGrants(secs, in)
		require.Equal(t, "InvalidArgument bad_section", refusal(err), name)
	}
	_, err = teamGrants(secs, []*pb_admin.AdminPermission{p(rbac.SectionTasks, pb_admin.AccessLevel_ACCESS_LEVEL_UNSPECIFIED)})
	require.Equal(t, "InvalidArgument bad_access", refusal(err))

	// What a member holds, as the list shows it: only the grantable set, in screen order.
	stored := []entity.AdminPermission{{Section: rbac.SectionDictionaries, Access: entity.AccessRead},
		{Section: rbac.SectionOrders, Access: entity.AccessWrite}, {Section: rbac.SectionTasks, Access: entity.AccessWrite}}
	shown := grantsPB(secs, stored)
	require.Len(t, shown, 2)
	require.Equal(t, rbac.SectionTasks, shown[0].Section)
	require.Equal(t, write, shown[0].Access)
	require.Equal(t, rbac.SectionDictionaries, shown[1].Section)
	require.Equal(t, []entity.AdminPermission{{Section: rbac.SectionOrders, Access: entity.AccessWrite}}, keptGrants(feature.DefaultOn(), stored),
		"a switched-off feature's grant is kept by an access write")
}

// TestUnitKeptGrants: an access write keeps only grants on sections hidden because their feature is
// off. accounts (an adopted team #1 admin may hold it), core, unknown, visible, invalid and repeated
// grants all go: the owner's request is the whole truth for them.
func TestUnitKeptGrants(t *testing.T) {
	on := feature.DefaultOn()
	stored := []entity.AdminPermission{
		{Section: rbac.SectionAccounts, Access: entity.AccessWrite},
		{Section: rbac.SectionOrders, Access: entity.AccessWrite},
		{Section: rbac.SectionOrders, Access: entity.AccessRead},
		{Section: rbac.SectionTasks, Access: entity.AccessWrite},
		{Section: rbac.SectionDictionaries, Access: entity.AccessRead},
		{Section: "nope", Access: entity.AccessWrite},
		{Section: rbac.SectionPromo, Access: "admin"},
		{Section: rbac.SectionAnalytics, Access: entity.AccessRead},
	}
	require.Equal(t, []entity.AdminPermission{
		{Section: rbac.SectionOrders, Access: entity.AccessWrite},
		{Section: rbac.SectionAnalytics, Access: entity.AccessRead},
	}, keptGrants(on, stored))
	// The feature back on: its section is in the request's hands again.
	require.Equal(t, []entity.AdminPermission{{Section: rbac.SectionAnalytics, Access: entity.AccessRead}},
		keptGrants(append(append([]feature.Key{}, on...), feature.Orders), stored))
	require.Empty(t, keptGrants(on, []entity.AdminPermission{{Section: rbac.SectionAccounts, Access: entity.AccessWrite}}),
		"accounts is never kept")
}

// adminRow is one admins row behind a MockAdmin, so the staging order is checked against state.
type adminRow struct {
	exists, disabled, joinPending, super bool
	perms                                []entity.AdminPermission
	log                                  []string
	failPerms, failEnable                error
}

func (r *adminRow) wire(adm *mocks.MockAdmin) {
	adm.EXPECT().GetAdminByUsername(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, h string) (*entity.Admin, error) {
		r.log = append(r.log, "read")
		if !r.exists {
			return nil, sql.ErrNoRows
		}
		return &entity.Admin{Username: h, Disabled: r.disabled, JoinPending: r.joinPending, IsSuper: r.super}, nil
	}).Maybe()
	adm.EXPECT().SetAccountDisabled(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, _ string, d bool) error {
		if d {
			r.log = append(r.log, "disable")
		} else {
			r.log = append(r.log, "enable")
			if r.failEnable != nil {
				return r.failEnable
			}
		}
		r.disabled, r.joinPending = d, false
		return nil
	}).Maybe()
	adm.EXPECT().SetAccountPermissions(mock.Anything, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, super bool, perms []entity.AdminPermission) error {
			r.log = append(r.log, "rights")
			if r.failPerms != nil {
				return r.failPerms
			}
			r.super, r.perms = super, perms
			return nil
		}).Maybe()
	adm.EXPECT().AddAccount(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, _ string, super bool, perms []entity.AdminPermission) error {
			r.log = append(r.log, "add")
			r.exists, r.super, r.perms = true, super, perms
			return nil
		}).Maybe()
}

// TestUnitApplyAccess: the access write is staged fail-closed — disabled with the new rights, sessions
// revoked, enabled — and a failure anywhere leaves the row disabled; nothing is ever put back from a
// value read before the write, and a retry ends enabled.
func TestUnitApplyAccess(t *testing.T) {
	newRights := []entity.AdminPermission{{Section: rbac.SectionTasks, Access: entity.AccessRead}}
	broad := []entity.AdminPermission{{Section: rbac.SectionAccounts, Access: entity.AccessWrite}, {Section: rbac.SectionTasks, Access: entity.AccessWrite}}
	type world struct {
		s       *Server
		row     *adminRow
		ctx     context.Context
		dropped []string
		evicted int
	}
	newWorld := func(t *testing.T, row *adminRow) *world {
		adm := mocks.NewMockAdmin(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(adm).Maybe()
		row.wire(adm)
		w := &world{s: &Server{repo: repo}, row: row}
		w.s.SetSessionInvalidator(func(sid string) { w.dropped = append(w.dropped, sid) })
		w.s.SetAdminAuthInvalidator(func(string) { w.evicted++ })
		team := tenant.Team{ID: 2, Slug: "acme", DBName: "lk_t_acme0001", Status: tenant.StatusActive}
		w.ctx = tenant.With(context.Background(), team)
		return w
	}
	revoked := func(log *[]string) func() (platformstore.MemberChange, error) {
		return func() (platformstore.MemberChange, error) {
			*log = append(*log, "revoke")
			return platformstore.MemberChange{Handle: "bob", RevokedSessions: []string{"s1"}}, nil
		}
	}
	refused := func(log *[]string, err error) func() (platformstore.MemberChange, error) {
		return func() (platformstore.MemberChange, error) {
			*log = append(*log, "revoke")
			return platformstore.MemberChange{}, err
		}
	}
	isMember := func(role string) func() (string, bool, error) {
		return func() (string, bool, error) { return role, true, nil }
	}

	t.Run("staged order, the old rights gone", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, perms: broad})
		ch, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: revoked(&w.row.log), role: isMember(roleMember)})
		require.NoError(t, err)
		require.Equal(t, "bob", ch.Handle)
		require.Equal(t, []string{"read", "disable", "rights", "revoke", "read", "enable"}, w.row.log)
		require.False(t, w.row.disabled)
		require.False(t, w.row.super)
		require.Equal(t, newRights, w.row.perms, "no accounts grant survives")
		require.Equal(t, []string{"s1"}, w.dropped)
		require.GreaterOrEqual(t, w.evicted, 2, "the staged disable and the enable are seen here at once")
	})

	t.Run("revoke refused: left disabled, then a retry ends enabled", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, perms: broad})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: refused(&w.row.log, platformstore.ErrTeamReadOnly), role: isMember(roleMember)})
		require.Equal(t, "FailedPrecondition team_read_only", refusal(err))
		require.Equal(t, []string{"read", "disable", "rights", "revoke"}, w.row.log)
		require.True(t, w.row.disabled, "fail closed: no live token of the old rights")
		require.Equal(t, newRights, w.row.perms, "never put back")
		require.Empty(t, w.dropped)

		w.row.log = nil
		_, err = w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: revoked(&w.row.log), role: isMember(roleMember)})
		require.NoError(t, err)
		require.Equal(t, []string{"read", "rights", "revoke", "read", "enable"}, w.row.log)
		require.False(t, w.row.disabled)
		require.Equal(t, newRights, w.row.perms)
	})

	t.Run("an internal revoke error is the same", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, perms: broad})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: refused(&w.row.log, errors.New("boom")), role: isMember(roleMember)})
		require.Equal(t, "Internal internal error", refusal(err))
		require.True(t, w.row.disabled)
	})

	t.Run("stage fails: no revoke, left disabled", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, perms: broad, failPerms: errors.New("boom")})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: revoked(&w.row.log), role: isMember(roleMember)})
		require.Equal(t, "Internal internal error", refusal(err))
		require.Equal(t, []string{"read", "disable", "rights"}, w.row.log)
		require.True(t, w.row.disabled)
		require.Equal(t, broad, w.row.perms)
		require.GreaterOrEqual(t, w.evicted, 1, "the disable that landed is seen at once")
	})

	t.Run("enable fails: left disabled, sessions already gone", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, perms: broad, failEnable: errors.New("boom")})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: revoked(&w.row.log), role: isMember(roleMember)})
		require.Equal(t, "Internal internal error", refusal(err))
		require.True(t, w.row.disabled)
		require.Equal(t, []string{"s1"}, w.dropped)
	})

	t.Run("removed after the revoke: disabled again", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, perms: broad})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: revoked(&w.row.log),
			role: func() (string, bool, error) { return "", false, nil }})
		require.Equal(t, "NotFound not_member", refusal(err))
		require.True(t, w.row.disabled)
		w2 := newWorld(t, &adminRow{exists: true, perms: broad})
		_, err = w2.s.applyAccess(w2.ctx, "bob", newRights, accessPlane{revoke: revoked(&w2.row.log),
			role: func() (string, bool, error) { return "", false, errors.New("boom") }})
		require.Equal(t, "Internal internal error", refusal(err))
		require.True(t, w2.row.disabled, "membership unconfirmed: fail closed")
	})

	t.Run("target became an owner: written as an owner from a fresh read", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, super: true})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: refused(&w.row.log, platformstore.ErrOwnerTarget), role: isMember(roleOwner)})
		require.Equal(t, "FailedPrecondition owner_full", refusal(err))
		require.True(t, w.row.super)
		require.False(t, w.row.disabled)
		w2 := newWorld(t, &adminRow{exists: true, super: true})
		_, err = w2.s.applyAccess(w2.ctx, "bob", newRights, accessPlane{revoke: refused(&w2.row.log, platformstore.ErrOwnerTarget),
			role: func() (string, bool, error) { return "", false, errors.New("boom") }})
		require.Equal(t, "FailedPrecondition owner_full", refusal(err))
		require.True(t, w2.row.disabled, "not confirmed an owner: stays disabled")
	})

	t.Run("no row: created with the rights, enabled only at the end", func(t *testing.T) {
		w := newWorld(t, &adminRow{})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: revoked(&w.row.log), role: isMember(roleMember)})
		require.NoError(t, err)
		require.Equal(t, []string{"read", "add", "disable", "revoke", "read", "enable"}, w.row.log)
		require.False(t, w.row.disabled)
		require.Equal(t, newRights, w.row.perms)
	})

	t.Run("an unfinished join cannot enable the row mid-change", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, disabled: true, joinPending: true})
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: func() (platformstore.MemberChange, error) {
			require.False(t, w.row.joinPending, "join_pending cleared before the revoke")
			return platformstore.MemberChange{Handle: "bob"}, nil
		}, role: isMember(roleMember)})
		require.NoError(t, err)
		require.False(t, w.row.disabled)
	})

	t.Run("concurrent edits: last writer wins, no older state restored", func(t *testing.T) {
		w := newWorld(t, &adminRow{exists: true, perms: broad})
		narrow := []entity.AdminPermission{}
		// A stages; B runs whole while A waits on the control plane; then A's revoke fails.
		_, err := w.s.applyAccess(w.ctx, "bob", newRights, accessPlane{revoke: func() (platformstore.MemberChange, error) {
			_, berr := w.s.applyAccess(w.ctx, "bob", narrow, accessPlane{revoke: revoked(&w.row.log), role: isMember(roleMember)})
			require.NoError(t, berr)
			return platformstore.MemberChange{}, errors.New("boom")
		}, role: isMember(roleMember)})
		require.Error(t, err)
		require.Equal(t, narrow, w.row.perms, "B's downgrade stands")
		require.NotContains(t, w.row.perms, broad[0])
		require.False(t, w.row.disabled)
	})
}

// TestUnitWorkspaceRefusalsCarryNoIDs: every refusal of the workspace handlers is a fixed key.
func TestUnitWorkspaceRefusalsCarryNoIDs(t *testing.T) {
	for _, key := range []string{errKeyBadText, errKeyBadAddr, errKeyBadEmail, errKeyBadSite, errKeyBadSect, errKeyBadAccess} {
		require.NotRegexp(t, `[0-9]`, key)
	}
	for _, err := range []error{platformstore.ErrTeamReadOnly, platformstore.ErrOwnerTarget, platformstore.ErrNotOwner,
		platformstore.ErrNotMember, platformstore.ErrWrongTeamStatus, platformstore.ErrTeamNotFound} {
		msg := status.Convert(peopleErr(context.Background(), "op", err)).Message()
		require.Regexp(t, `^[a-z_]+$`, msg, err.Error())
	}
	require.Equal(t, "FailedPrecondition team_read_only", refusal(peopleErr(context.Background(), "op", platformstore.ErrTeamReadOnly)))
	require.Equal(t, "FailedPrecondition owner_full", refusal(peopleErr(context.Background(), "op", platformstore.ErrOwnerTarget)))
}

// TestUnitWorkspaceRBAC: the writes are owner rights at the edge (accounts write, which no member
// holds) and refused on a suspended team (write class); the list is open to every member.
func TestUnitWorkspaceRBAC(t *testing.T) {
	member := map[string]entity.AccessLevel{rbac.SectionTasks: entity.AccessWrite}
	for _, m := range []string{"SetTeamBrand", "SetTeamMemberAccess", "InviteTeamMember", "SetTeamMemberRole"} {
		lvl, ok := rbac.Access(rbac.MethodPrefix + m)
		require.True(t, ok, m)
		require.Equal(t, entity.AccessWrite, lvl, m)
		require.False(t, rbac.Allow(rbac.MethodPrefix+m, false, member), m)
	}
	full := rbac.MethodPrefix + "ListTeamMembers"
	_, allowlisted, _ := rbac.Lookup(full)
	require.True(t, allowlisted)
	lvl, _ := rbac.Access(full)
	require.Equal(t, entity.AccessRead, lvl)
	require.True(t, rbac.Allow(full, false, member))
	require.True(t, rbac.Allow(full, false, nil))
}

// TestTeamMembersOffGate: with teams mode off ListTeamMembers keeps the edge gate it had as
// rd(accounts) — a legacy token, a super or accounts read — and refuses everyone else with the
// edge's exact text, so prod answers byte for byte as before the allowlist move.
func TestTeamMembersOffGate(t *testing.T) {
	const want = "PermissionDenied insufficient permissions for /admin.AdminService/ListTeamMembers"
	cases := []struct {
		name  string
		authz *authsrv.AdminAuthz
		allow bool
	}{
		{"no authorization", nil, false},
		{"no rights", &authsrv.AdminAuthz{}, false},
		{"other sections", &authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{rbac.SectionOrders: entity.AccessWrite}}, false},
		{"accounts read", &authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{rbac.SectionAccounts: entity.AccessRead}}, true},
		{"accounts write", &authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{rbac.SectionAccounts: entity.AccessWrite}}, true},
		{"super", &authsrv.AdminAuthz{Super: true}, true},
		{"legacy token", &authsrv.AdminAuthz{Legacy: true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t) // strict: a refusal reads nothing
			if c.allow {
				adm := mocks.NewMockAdmin(t)
				adm.EXPECT().ListAccounts(mock.Anything).Return([]entity.AdminAccount{
					{Admin: entity.Admin{Username: "boss", IsSuper: true}}, {Admin: entity.Admin{Username: "kim"}},
				}, nil).Once()
				repo.EXPECT().Admin().Return(adm).Once()
			}
			s := &Server{repo: repo}
			ctx := authsrv.PutAdminUsername(context.Background(), "kim")
			if c.authz != nil {
				ctx = authsrv.PutAdminAuthz(ctx, *c.authz)
			}
			res, err := s.ListTeamMembers(ctx, &pb_admin.ListTeamMembersRequest{})
			if !c.allow {
				require.Equal(t, want, refusal(err))
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				return
			}
			require.NoError(t, err)
			require.Len(t, res.Members, 2)
			require.Empty(t, res.Sections, "teams mode off: the answer is as before")
			require.Empty(t, res.Members[1].Permissions)
		})
	}
}
