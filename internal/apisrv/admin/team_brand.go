package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/teamsync"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The workspace screen (teams W): the team's brand data for its papers and each member's section
// rights. Every read and write keys on the team the edge resolved (GetWorkspace), never on a field
// of the request; refusals are fixed keys.

// Refusal keys of the workspace writes. The brand's own keys (bad_text, bad_address, bad_email,
// bad_website) and its one normalisation live in platformauth (NormalizeTeamBrand), shared with a
// team's creation.
const (
	errKeyBadSect   = "bad_section"
	errKeyBadAccess = "bad_access"
)

// brandPB is the brand as the screen and the papers read it. firstTeam: team #1, whose papers keep
// the house vector mark and the build's VAT id.
func brandPB(b platformstore.TeamBrand, firstTeam bool) *pb_admin.TeamBrand {
	return &pb_admin.TeamBrand{
		BrandName:    b.BrandName,
		LegalEntity:  b.LegalEntity,
		LegalAddress: append([]string{}, b.LegalAddress...),
		ContactEmail: b.ContactEmail,
		Website:      b.Website,
		FirstTeam:    firstTeam,
	}
}

// workspaceBrand is Workspace.brand from one control-plane read: a read error is {first_team,
// unavailable} (never nil: nil tells the papers to print the GRBPWR constants); no row is empty values.
func workspaceBrand(b platformstore.TeamBrand, found bool, err error, firstTeam bool) *pb_admin.TeamBrand {
	switch {
	case err != nil:
		return &pb_admin.TeamBrand{FirstTeam: firstTeam, Unavailable: true, LegalAddress: []string{}}
	case !found:
		return brandPB(platformstore.TeamBrand{}, firstTeam)
	default:
		return brandPB(b, firstTeam)
	}
}

// teamBrand reads the calling team's brand for the workspace answer; nil only with teams mode off.
func (s *Server) teamBrand(ctx context.Context, teamID int64) *pb_admin.TeamBrand {
	if !s.teamsOn() {
		return nil
	}
	first := teamID == tenant.TeamOneID
	if teamID <= 0 {
		return workspaceBrand(platformstore.TeamBrand{}, false, errors.New("no team"), first)
	}
	b, found, err := s.platform.store.TeamBrand(ctx, teamID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "team brand", slog.String("err", err.Error()))
	}
	return workspaceBrand(b, found, err, first)
}

// SetTeamBrand replaces the calling team's brand (owner or entered operator) and answers the
// normalised values.
func (s *Server) SetTeamBrand(ctx context.Context, req *pb_admin.SetTeamBrandRequest) (*pb_admin.SetTeamBrandResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	b, err := platformauth.NormalizeTeamBrand(req)
	if err != nil {
		return nil, err
	}
	changed, err := s.platform.store.SetTeamBrand(ctx, platformstore.SetTeamBrandParams{TeamID: p.w.Team.ID, Actor: p.actor(), Brand: b})
	if err != nil {
		return nil, peopleErr(ctx, "set brand", err)
	}
	if len(changed) > 0 {
		s.peopleAudit(ctx, p, "team_brand_set", map[string]string{"fields": strings.Join(changed, ",")})
	}
	return &pb_admin.SetTeamBrandResponse{Brand: brandPB(b, p.w.Team.ID == tenant.TeamOneID)}, nil
}

// sectionRank is the screen order of the grantable set: the workspace mockup's ten, then the rest
// in catalog order (a stable sort keeps it).
func sectionRank(key string) int {
	switch key {
	case rbac.SectionTasks:
		return 0
	case rbac.SectionFiles:
		return 1
	case rbac.SectionContent:
		return 2
	case rbac.SectionModels:
		return 3
	case rbac.SectionFittings:
		return 4
	case rbac.SectionTechCards:
		return 5
	case rbac.SectionInventory:
		return 6
	case rbac.SectionProduction:
		return 7
	case rbac.SectionCosting:
		return 8
	case rbac.SectionDictionaries:
		return 9
	default:
		return 10
	}
}

// sectionWord is the screen word of a section: the catalog title lower-cased, except content (media)
// and inventory (materials), which the screen names after what a team keeps there.
func sectionWord(sec rbac.SectionInfo) string {
	switch sec.Key {
	case rbac.SectionContent:
		return "media"
	case rbac.SectionInventory:
		return "materials"
	default:
		return strings.ToLower(sec.Title)
	}
}

// teamSections is the grantable set of a team (one function for the list, the access write and the
// invite): every catalog section except accounts whose feature is on for the team or is core, in
// screen order, Title = the screen word, no description. on is the team's switched-on features.
func teamSections(on []feature.Key) []rbac.SectionInfo {
	enabled := make(map[feature.Key]bool, len(on))
	for _, k := range on {
		enabled[k] = true
	}
	var out []rbac.SectionInfo
	for _, sec := range rbac.Sections() {
		if sec.Key == rbac.SectionAccounts {
			continue
		}
		k, ok := feature.SectionFeature(sec.Key)
		if !ok || (k != feature.Core && !enabled[k]) {
			continue
		}
		out = append(out, rbac.SectionInfo{Key: sec.Key, Title: sectionWord(sec)})
	}
	sort.SliceStable(out, func(i, j int) bool { return sectionRank(out[i].Key) < sectionRank(out[j].Key) })
	return out
}

func sectionsPB(sections []rbac.SectionInfo) []*pb_admin.AdminSectionInfo {
	out := make([]*pb_admin.AdminSectionInfo, 0, len(sections))
	for _, sec := range sections {
		out = append(out, &pb_admin.AdminSectionInfo{Key: sec.Key, Title: sec.Title})
	}
	return out
}

// teamGrants validates a full set of member rights against the grantable set: every section in it
// (bad_section), READ or WRITE (bad_access), no section twice (bad_section). The result is in screen
// order. It is the rule of SetTeamMemberAccess and of an invite that names rights.
func teamGrants(sections []rbac.SectionInfo, in []*pb_admin.AdminPermission) ([]entity.AdminPermission, error) {
	rank := make(map[string]int, len(sections))
	for i, sec := range sections {
		rank[sec.Key] = i
	}
	out := make([]entity.AdminPermission, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, p := range in {
		key := p.GetSection()
		if _, ok := rank[key]; !ok || seen[key] {
			return nil, status.Error(codes.InvalidArgument, errKeyBadSect)
		}
		seen[key] = true
		var access entity.AccessLevel
		switch p.GetAccess() {
		case pb_admin.AccessLevel_ACCESS_LEVEL_READ:
			access = entity.AccessRead
		case pb_admin.AccessLevel_ACCESS_LEVEL_WRITE:
			access = entity.AccessWrite
		default:
			return nil, status.Error(codes.InvalidArgument, errKeyBadAccess)
		}
		out = append(out, entity.AdminPermission{Section: key, Access: access})
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Section] < rank[out[j].Section] })
	return out, nil
}

// grantsPB is a member's stored grants inside the grantable set, in screen order.
func grantsPB(sections []rbac.SectionInfo, perms []entity.AdminPermission) []*pb_admin.AdminPermission {
	have := make(map[string]entity.AccessLevel, len(perms))
	for _, p := range perms {
		have[p.Section] = p.Access
	}
	out := make([]*pb_admin.AdminPermission, 0, len(perms))
	for _, sec := range sections {
		if a, ok := have[sec.Key]; ok && a.Valid() {
			out = append(out, &pb_admin.AdminPermission{Section: sec.Key, Access: toProtoAccess(a)})
		}
	}
	return out
}

// keptGrants are the only stored grants an access write keeps besides the request: a valid level on a
// catalog section that is outside the grantable set solely because its feature is off for the team,
// so it comes back with the feature. Never accounts, never a core section, never a section the
// catalog does not know, never one twice; the write is never super. For every other section the
// owner's request is the whole truth. on is the team's switched-on features.
func keptGrants(on []feature.Key, perms []entity.AdminPermission) []entity.AdminPermission {
	enabled := make(map[feature.Key]bool, len(on))
	for _, k := range on {
		enabled[k] = true
	}
	known := make(map[string]bool)
	for _, sec := range rbac.Sections() {
		known[sec.Key] = true
	}
	seen := make(map[string]bool, len(perms))
	var out []entity.AdminPermission
	for _, p := range perms {
		if p.Section == rbac.SectionAccounts || !known[p.Section] || seen[p.Section] || !p.Access.Valid() {
			continue
		}
		k, ok := feature.SectionFeature(p.Section)
		if !ok || k == feature.Core || enabled[k] {
			continue
		}
		seen[p.Section] = true
		out = append(out, entity.AdminPermission{Section: p.Section, Access: p.Access})
	}
	return out
}

// storedGrants reads a member's admins row's grants (none when there is no row).
func (s *Server) storedGrants(ctx context.Context, handle string) ([]entity.AdminPermission, error) {
	acc, err := s.repo.Admin().GetAccountWithPermissions(ctx, handle)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && acc == nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return acc.Permissions, nil
}

func grantsDetail(perms []entity.AdminPermission) string {
	parts := make([]string, 0, len(perms))
	for _, p := range perms {
		parts = append(parts, p.Section+":"+string(p.Access))
	}
	return strings.Join(parts, ",")
}

// SetTeamMemberAccess replaces a member's section rights (owner or entered operator; an owner target
// is refused, an owner has everything). The request is the full set inside the grantable set; the
// only stored grants kept besides it are those keptGrants names.
//
// Claims are minted from the admins row at sign-in and the edge refuses a disabled row on every
// call, so the write is staged fail-closed (applyAccess): the row is disabled with the new rights,
// the control plane revokes the member's team sessions, then the row is enabled. A failure at any
// step leaves the row disabled and returns the error: the member is locked out until a retry, never
// left with live tokens of the old rights. A retry repeats every step and ends enabled. Nothing is
// ever put back from a value read before the write: two concurrent writes end last-writer-wins.
func (s *Server) SetTeamMemberAccess(ctx context.Context, req *pb_admin.SetTeamMemberAccessRequest) (*pb_admin.SetTeamMemberAccessResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	// The edge refuses a suspended team's writes already; checked again here because step (1) writes
	// another database before the store's own re-check at (2).
	if p.w.ReadOnly {
		return nil, status.Error(codes.FailedPrecondition, "team_read_only")
	}
	m, err := s.member(ctx, p, req.GetUsername())
	if err != nil {
		return nil, err
	}
	if m.Role == roleOwner {
		return nil, status.Error(codes.FailedPrecondition, "owner_full")
	}
	sections := teamSections(p.w.Features)
	grants, err := teamGrants(sections, req.GetPermissions())
	if err != nil {
		return nil, err
	}
	prior, err := s.storedGrants(ctx, m.Handle)
	if err != nil {
		return nil, peopleErr(ctx, "read grants", err)
	}
	perms := append(append([]entity.AdminPermission{}, grants...), keptGrants(p.w.Features, prior)...)
	ch, err := s.applyAccess(ctx, m.Handle, perms, accessPlane{
		revoke: func() (platformstore.MemberChange, error) {
			return s.platform.store.RevokeMemberSessions(ctx, platformstore.MemberChangeParams{
				TeamID: p.w.Team.ID, UserID: m.UserID, Actor: p.actor(), Now: time.Now().UTC(),
			})
		},
		role: func() (string, bool, error) {
			members, err := s.platform.store.TeamMembers(ctx, p.w.Team.ID)
			if err != nil {
				return "", false, err
			}
			for _, x := range members {
				if x.UserID == m.UserID {
					return x.Role, true, nil
				}
			}
			return "", false, nil
		},
	})
	if err != nil {
		return nil, err
	}
	s.peopleAudit(ctx, p, "member_access", map[string]string{"handle": ch.Handle, "grants": grantsDetail(grants)})
	tm := &pb_admin.TeamMember{
		Username:    m.Handle,
		Email:       m.Email.String,
		Role:        roleMember,
		You:         m.Handle == authsrv.GetAdminUsername(ctx) && !p.w.Operator,
		Permissions: grantsPB(sections, grants),
	}
	if m.LastSeenAt.Valid {
		tm.LastSeenAt = timestamppb.New(m.LastSeenAt.Time)
	}
	return &pb_admin.SetTeamMemberAccessResponse{Member: tm}, nil
}

// accessPlane is the control-plane half of an access write, cut out so the staging order is tested
// without a database.
type accessPlane struct {
	// revoke kills the target's team sessions, re-checking team status, actor and target under lock.
	revoke func() (platformstore.MemberChange, error)
	// role reads the target's membership afresh: member=false when it is no longer active.
	role func() (role string, member bool, err error)
}

// applyAccess writes a member's new rights to the ctx team's admins row, fail-closed:
//
//  1. StageAdmin: the row DISABLED with the new rights. From here every live token of the member is
//     refused (this instance at once, other instances within the edge's account-state TTL) and so
//     is every sign-in.
//  2. The control plane revokes the member's team sessions (team status, actor, target re-checked).
//  3. The row is enabled: a sign-in now mints the new rights only.
//  4. The membership is read again: a removal that committed after (2) may have disabled the row
//     before (3) enabled it, so a member gone by now is disabled again.
//
// Any failure returns its refusal with the row left disabled; a retry repeats 1–4 (each idempotent)
// and ends enabled. The one write that is not «leave it disabled»: (2) found the target an owner — a
// promotion committed after the caller read the member and its own mirror write (super, enabled) may
// have landed before (1) — so the row is written as SetTeamMemberRole writes an owner's, from a
// control-plane read made after (1). No path writes back a value read before (1).
//
// What stays: a sign-in already in flight that read the row before (1) and opens its session after
// (2) — milliseconds, as for a role change.
func (s *Server) applyAccess(ctx context.Context, handle string, perms []entity.AdminPermission, plane accessPlane) (platformstore.MemberChange, error) {
	admins := s.repo.Admin()
	internal := func(op string, err error) error {
		slog.Default().ErrorContext(ctx, "team members: access "+op, slog.String("err", err.Error()))
		return status.Error(codes.Internal, "internal error")
	}
	// (1) A partial stage may have disabled the row already: drop the cached state either way.
	err := teamsync.StageAdmin(ctx, admins, handle, perms)
	s.invalidateAccountAuth(handle)
	if err != nil {
		return platformstore.MemberChange{}, internal("stage", err)
	}
	// (2)
	ch, err := plane.revoke()
	if err != nil {
		if errors.Is(err, platformstore.ErrOwnerTarget) {
			s.restoreOwner(ctx, handle, plane)
		}
		return platformstore.MemberChange{}, peopleErr(ctx, "revoke sessions", err)
	}
	s.dropSessions(ch.RevokedSessions)
	// (3)
	err = teamsync.EnableAdmin(ctx, admins, handle)
	s.invalidateAccountAuth(handle)
	if err != nil {
		return platformstore.MemberChange{}, internal("enable", err)
	}
	// (4)
	_, member, err := plane.role()
	if err != nil || !member {
		if derr := teamsync.DisableAdmin(ctx, admins, handle); derr != nil {
			slog.Default().ErrorContext(ctx, "team members: access re-disable", slog.String("err", derr.Error()))
		}
		s.invalidateAccountAuth(handle)
		if err != nil {
			return platformstore.MemberChange{}, internal("membership", err)
		}
		return platformstore.MemberChange{}, status.Error(codes.NotFound, "not_member")
	}
	return ch, nil
}

// restoreOwner writes an owner's admins row (super, enabled) when the control plane, read now, still
// has the target as an owner; otherwise the staged row stays disabled. Best effort, logged: the
// refusal (owner_full) is the answer either way.
func (s *Server) restoreOwner(ctx context.Context, handle string, plane accessPlane) {
	role, member, err := plane.role()
	if err != nil {
		slog.Default().ErrorContext(ctx, "team members: access owner re-read", slog.String("err", err.Error()))
		return
	}
	if !member || role != roleOwner {
		return
	}
	if err := teamsync.EnsureAdmin(ctx, s.repo.Admin(), handle, true, nil); err != nil {
		slog.Default().ErrorContext(ctx, "team members: access owner mirror", slog.String("err", err.Error()))
	}
	s.invalidateAccountAuth(handle)
}
