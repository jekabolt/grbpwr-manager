package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// Brand limits (runes), as the team_brand columns.
const (
	brandNameMax    = 64
	legalEntityMax  = 96
	addressLineMax  = 64
	websiteMax      = 128
	errKeyBadText   = "bad_text"
	errKeyBadAddr   = "bad_address"
	errKeyBadEmail  = "bad_email"
	errKeyBadSite   = "bad_website"
	errKeyBadSect   = "bad_section"
	errKeyBadAccess = "bad_access"
)

// cleanBrandText trims, refuses control characters and line breaks inside, collapses whitespace
// runs into one space and checks the rune limit. Any script is accepted; case is kept as typed.
func cleanBrandText(s string, max int) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", false
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		return "", false
	}
	return s, true
}

// cleanWebsite: empty, or the address without its http(s) scheme and one trailing slash, lower-case,
// with a dot and nothing that is a space or a control character.
func cleanWebsite(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", true
	}
	if rest, ok := strings.CutPrefix(s, "https://"); ok {
		s = rest
	} else if rest, ok := strings.CutPrefix(s, "http://"); ok {
		s = rest
	}
	s = strings.TrimSuffix(s, "/")
	host, _, _ := strings.Cut(s, "/")
	if !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") ||
		strings.Contains(s, "://") || utf8.RuneCountInString(s) > websiteMax {
		return "", false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

// normalizeBrand is the one normalisation of a brand write (teams W §1). Refusals (InvalidArgument):
// bad_text (a control character or line break, or over the limit: name 64, entity 96, address line 64),
// bad_address (more than three non-blank lines; blank lines are dropped and the rest move up),
// bad_email (not empty and not an address platformauth.NormalizeEmail accepts), bad_website.
func normalizeBrand(req *pb_admin.SetTeamBrandRequest) (platformstore.TeamBrand, error) {
	bad := func(key string) (platformstore.TeamBrand, error) {
		return platformstore.TeamBrand{}, status.Error(codes.InvalidArgument, key)
	}
	var b platformstore.TeamBrand
	var ok bool
	if b.BrandName, ok = cleanBrandText(req.GetBrandName(), brandNameMax); !ok {
		return bad(errKeyBadText)
	}
	if b.LegalEntity, ok = cleanBrandText(req.GetLegalEntity(), legalEntityMax); !ok {
		return bad(errKeyBadText)
	}
	for _, raw := range req.GetLegalAddress() {
		line, ok := cleanBrandText(raw, addressLineMax)
		if !ok {
			return bad(errKeyBadText)
		}
		if line == "" {
			continue
		}
		if len(b.LegalAddress) == platformstore.TeamBrandAddressLines {
			return bad(errKeyBadAddr)
		}
		b.LegalAddress = append(b.LegalAddress, line)
	}
	if email := strings.TrimSpace(req.GetContactEmail()); email != "" {
		norm, err := platformauth.NormalizeEmail(email)
		if err != nil {
			return bad(errKeyBadEmail)
		}
		b.ContactEmail = norm
	}
	if b.Website, ok = cleanWebsite(req.GetWebsite()); !ok {
		return bad(errKeyBadSite)
	}
	return b, nil
}

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
	b, err := normalizeBrand(req)
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

// outsideGrants are the stored grants on sections outside the grantable set (a switched-off feature's
// section): an access write keeps them, so they come back with the feature.
func outsideGrants(sections []rbac.SectionInfo, perms []entity.AdminPermission) []entity.AdminPermission {
	in := make(map[string]bool, len(sections))
	for _, sec := range sections {
		in[sec.Key] = true
	}
	var out []entity.AdminPermission
	for _, p := range perms {
		if !in[p.Section] {
			out = append(out, p)
		}
	}
	return out
}

// storedGrants reads a member's admins row: its grants, and whether the row exists.
func (s *Server) storedGrants(ctx context.Context, handle string) ([]entity.AdminPermission, bool, error) {
	acc, err := s.repo.Admin().GetAccountWithPermissions(ctx, handle)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && acc == nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return acc.Permissions, true, nil
}

func grantsDetail(perms []entity.AdminPermission) string {
	parts := make([]string, 0, len(perms))
	for _, p := range perms {
		parts = append(parts, p.Section+":"+string(p.Access))
	}
	return strings.Join(parts, ",")
}

// SetTeamMemberAccess replaces a member's section rights (owner or entered operator; an owner target
// is refused, an owner has everything). The request is the full set inside the grantable set; grants
// on sections outside it are kept.
//
// Order, on purpose the reverse of SetTeamMemberRole: (1) the team database's admins row gets the
// new rights, (2) the control plane revokes the member's team sessions, (3) the edge cache drops
// them. Claims are minted from the admins row at sign-in. Revoking first would leave the whole admins
// write (a call to another database) as a window in which a re-entry mints the OLD rights into a
// token that then lives its full term; written first, a re-entry after (1) reads the new rights, and
// a session opened before (2) commits is revoked by (2). What stays is a sign-in already in flight
// that read the row before (1) and opens its session after (2) — milliseconds, as for a role change.
// A retry repeats both steps (idempotent). A refusal at (2) puts the admins row back.
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
	prior, hadRow, err := s.storedGrants(ctx, m.Handle)
	if err != nil {
		return nil, peopleErr(ctx, "read grants", err)
	}
	perms := append(append([]entity.AdminPermission{}, grants...), outsideGrants(sections, prior)...)
	// (1) the admins row.
	if err := teamsync.EnsureAdmin(ctx, s.repo.Admin(), m.Handle, false, perms); err != nil {
		slog.Default().ErrorContext(ctx, "team members: ensure admin", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "internal error")
	}
	// (2) the sessions, re-checking team status, actor and target under lock.
	ch, err := s.platform.store.RevokeMemberSessions(ctx, platformstore.MemberChangeParams{
		TeamID: p.w.Team.ID, UserID: m.UserID, Actor: p.actor(), Now: time.Now().UTC(),
	})
	if err != nil {
		s.undoAccess(ctx, m.Handle, err, prior, hadRow)
		return nil, peopleErr(ctx, "revoke sessions", err)
	}
	// (3) the edge cache.
	s.dropSessions(ch.RevokedSessions)
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

// undoAccess puts the admins row back after step (2) refused: a target who became an owner meanwhile
// is super again, one who left is disabled, otherwise the rights it held before return. Best effort,
// logged: the refusal is the answer either way.
func (s *Server) undoAccess(ctx context.Context, handle string, cause error, prior []entity.AdminPermission, hadRow bool) {
	var err error
	switch {
	case errors.Is(cause, platformstore.ErrOwnerTarget):
		err = teamsync.EnsureAdmin(ctx, s.repo.Admin(), handle, true, nil)
	case errors.Is(cause, platformstore.ErrNotMember) || !hadRow:
		err = teamsync.DisableAdmin(ctx, s.repo.Admin(), handle)
	default:
		err = teamsync.EnsureAdmin(ctx, s.repo.Admin(), handle, false, prior)
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "team members: undo access", slog.String("err", err.Error()))
	}
}
