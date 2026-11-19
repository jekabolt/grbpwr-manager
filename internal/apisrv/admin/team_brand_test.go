package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
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
	require.Equal(t, []entity.AdminPermission{{Section: rbac.SectionOrders, Access: entity.AccessWrite}}, outsideGrants(secs, stored),
		"a switched-off feature's grant is kept by an access write")
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
