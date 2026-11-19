package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/teamsync"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// People of a team (teams §3 §8): members and invites live in the control plane; the team's own
// admins row follows each change (teamsync). Nothing here ever returns a row id.
const (
	roleOwner  = "owner"
	roleMember = "member"
	// InviteTTL: an invite link is good for 7 days.
	InviteTTL = 7 * 24 * time.Hour
	// inviteLettersPerHour caps letters to one address (verify, invite and reset together).
	inviteLettersPerHour = 3
	inviteRefLen         = 22
)

// SetSessionInvalidator wires the edge's session cache: a sid revoked by a member change is
// refused on the very next call. Called once at boot (teams mode only).
func (s *Server) SetSessionInvalidator(fn func(sid string)) {
	s.platform.invalidateSession = fn
}

// InviteRef is the opaque handle of an invite: first 22 chars of base64url(HMAC-SHA256(pepper,
// "invite|"+id)). Computed on read; resolved by matching the team's open invites.
func InviteRef(pepper string, id int64) string {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte("invite|"))
	m.Write([]byte(strconv.FormatInt(id, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:inviteRefLen]
}

// DefaultInvitePermissions is a new member's grant (teams §3): write on every section whose
// feature is on for the team or is core, except accounts. on is the team's switched-on features.
func DefaultInvitePermissions(on []feature.Key) []entity.AdminPermission {
	enabled := make(map[feature.Key]bool, len(on))
	for _, k := range on {
		enabled[k] = true
	}
	var out []entity.AdminPermission
	for _, sec := range rbac.Sections() {
		if sec.Key == rbac.SectionAccounts {
			continue
		}
		k, ok := feature.SectionFeature(sec.Key)
		if !ok || (k != feature.Core && !enabled[k]) {
			continue
		}
		out = append(out, entity.AdminPermission{Section: sec.Key, Access: entity.AccessWrite})
	}
	return out
}

func permissionsJSON(perms []entity.AdminPermission) ([]byte, error) {
	type row struct {
		Section string `json:"section"`
		Access  string `json:"access"`
	}
	rows := make([]row, 0, len(perms))
	for _, p := range perms {
		rows = append(rows, row{Section: p.Section, Access: string(p.Access)})
	}
	return json.Marshal(rows)
}

// peopleCtx is the caller of a members RPC in teams mode.
type peopleCtx struct {
	w      authsrv.Workspace
	userID int64
	handle string
	owner  bool
}

func (s *Server) teamsOn() bool { return s.platform.store != nil }

// people resolves the caller (teams mode on). An entered operator counts as an owner.
func (s *Server) people(ctx context.Context) (peopleCtx, error) {
	w, ok := authsrv.GetWorkspace(ctx)
	if !ok || w.Team.ID <= 0 {
		return peopleCtx{}, status.Error(codes.Unauthenticated, "unauthorized")
	}
	p := peopleCtx{w: w, userID: w.UserID, handle: authsrv.GetAdminUsername(ctx), owner: w.Role == roleOwner || w.Operator}
	if p.userID <= 0 {
		// A legacy team #1 token carries no platform user id: its handle is the platform handle.
		u, err := s.platform.store.UserByHandle(ctx, p.handle)
		if err != nil {
			if errors.Is(err, platformstore.ErrUserNotFound) {
				return peopleCtx{}, status.Error(codes.FailedPrecondition, "no_platform_account")
			}
			return peopleCtx{}, peopleErr(ctx, "caller", err)
		}
		p.userID = u.ID
	}
	return p, nil
}

func peopleErr(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, platformstore.ErrLastOwner):
		return status.Error(codes.FailedPrecondition, "last_owner")
	case errors.Is(err, platformstore.ErrNotMember), errors.Is(err, platformstore.ErrUserNotFound):
		return status.Error(codes.NotFound, "not_member")
	case errors.Is(err, platformstore.ErrInviteNotFound):
		return status.Error(codes.NotFound, "invite_not_found")
	case errors.Is(err, platformstore.ErrAlreadyMember):
		return status.Error(codes.AlreadyExists, "already_member")
	case errors.Is(err, platformstore.ErrLetterCap):
		return status.Error(codes.ResourceExhausted, "letter_cap")
	case errors.Is(err, platformstore.ErrWrongTeamStatus):
		return status.Error(codes.FailedPrecondition, "wrong_status")
	case errors.Is(err, platformstore.ErrTeamNotFound):
		return status.Error(codes.NotFound, "team_not_found")
	}
	slog.Default().ErrorContext(ctx, "team members: "+op, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "internal error")
}

func errTeamsOff() error { return status.Error(codes.FailedPrecondition, "teams_off") }

func (s *Server) peopleAudit(ctx context.Context, p peopleCtx, action string, detail map[string]string) {
	if p.w.Operator {
		detail["operator"] = "true"
	}
	if err := s.platform.store.AuditAuth(ctx, p.userID, p.w.Team.ID, action, detail, middleware.GetClientIP(ctx)); err != nil {
		slog.Default().ErrorContext(ctx, "team members: audit "+action, slog.String("err", err.Error()))
	}
}

func (s *Server) inviteLink(slug, token string) string {
	return "https://" + slug + "." + strings.ToLower(strings.TrimSpace(s.platform.cfg.BaseDomain)) + "/join#token=" + token
}

func invitePB(pepper string, inv platformstore.TeamInvite, now time.Time) *pb_admin.TeamInvite {
	st := "pending"
	if !inv.ExpiresAt.After(now) {
		st = "expired"
	}
	return &pb_admin.TeamInvite{
		InviteRef: InviteRef(pepper, inv.ID),
		Email:     inv.Email,
		Role:      inv.Role,
		SentAt:    timestamppb.New(inv.SentAt),
		ExpiresAt: timestamppb.New(inv.ExpiresAt),
		Status:    st,
	}
}

// ListTeamMembers: the caller's team's active members and open invites. Teams mode off: team #1's
// admins rows (super → owner), no invites.
func (s *Server) ListTeamMembers(ctx context.Context, _ *pb_admin.ListTeamMembersRequest) (*pb_admin.ListTeamMembersResponse, error) {
	me := authsrv.GetAdminUsername(ctx)
	if !s.teamsOn() {
		accounts, err := s.repo.Admin().ListAccounts(ctx)
		if err != nil {
			return nil, peopleErr(ctx, "list accounts", err)
		}
		out := &pb_admin.ListTeamMembersResponse{Members: []*pb_admin.TeamMember{}, Invites: []*pb_admin.TeamInvite{}}
		for _, a := range accounts {
			if a.Disabled {
				continue
			}
			role := roleMember
			if a.IsSuper {
				role = roleOwner
			}
			out.Members = append(out.Members, &pb_admin.TeamMember{Username: a.Username, Role: role, You: a.Username == me})
		}
		return out, nil
	}
	p, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.platform.store.TeamMembers(ctx, p.w.Team.ID)
	if err != nil {
		return nil, peopleErr(ctx, "members", err)
	}
	invites, err := s.platform.store.OpenInvites(ctx, p.w.Team.ID)
	if err != nil {
		return nil, peopleErr(ctx, "invites", err)
	}
	now := time.Now().UTC()
	out := &pb_admin.ListTeamMembersResponse{
		Members: make([]*pb_admin.TeamMember, 0, len(members)),
		Invites: make([]*pb_admin.TeamInvite, 0, len(invites)),
	}
	for _, m := range members {
		tm := &pb_admin.TeamMember{Username: m.Handle, Email: m.Email.String, Role: m.Role, You: m.Handle == me && !p.w.Operator}
		if m.LastSeenAt.Valid {
			tm.LastSeenAt = timestamppb.New(m.LastSeenAt.Time)
		}
		out.Members = append(out.Members, tm)
	}
	for _, inv := range invites {
		out.Invites = append(out.Invites, invitePB(s.platform.cfg.TokenPepper, inv, now))
	}
	return out, nil
}

// InviteTeamMember invites an email into the caller's team. An open invite for the email is
// re-used (its role and permissions updated, the old link dead) and re-sent.
func (s *Server) InviteTeamMember(ctx context.Context, req *pb_admin.InviteTeamMemberRequest) (*pb_admin.InviteTeamMemberResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	email, err := platformauth.NormalizeEmail(req.GetEmail())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad_email")
	}
	role := strings.ToLower(strings.TrimSpace(req.GetRole()))
	if role == "" {
		role = roleMember
	}
	if role != roleOwner && role != roleMember {
		return nil, status.Error(codes.InvalidArgument, "bad_role")
	}
	if role == roleOwner && !p.owner {
		return nil, status.Error(codes.PermissionDenied, "owner_only")
	}
	perms, err := permissionsJSON(DefaultInvitePermissions(p.w.Features))
	if err != nil {
		return nil, peopleErr(ctx, "permissions", err)
	}
	token, hash, err := platformauth.NewToken()
	if err != nil {
		return nil, peopleErr(ctx, "token", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	inv, err := s.platform.store.UpsertInvite(ctx, platformstore.InviteParams{
		TeamID: p.w.Team.ID, Email: email, Role: role, Permissions: perms, TokenHash: hash,
		ExpiresAt: now.Add(InviteTTL), InvitedBy: p.userID, Now: now, MaxPerHour: inviteLettersPerHour,
		Letter: platformstore.Letter{
			To: email, Template: "invite", Link: s.inviteLink(p.w.Team.Slug, token),
			TeamName: p.w.Team.Name, Slug: p.w.Team.Slug, From: p.handle,
		},
	})
	if err != nil {
		return nil, peopleErr(ctx, "invite", err)
	}
	s.peopleAudit(ctx, p, "member_invite", map[string]string{"email": email, "role": role})
	return &pb_admin.InviteTeamMemberResponse{Invite: invitePB(s.platform.cfg.TokenPepper, inv, now)}, nil
}

// openInvite resolves an invite_ref against the team's open invites.
func (s *Server) openInvite(ctx context.Context, p peopleCtx, ref string) (platformstore.TeamInvite, error) {
	ref = strings.TrimSpace(ref)
	if len(ref) != inviteRefLen {
		return platformstore.TeamInvite{}, status.Error(codes.NotFound, "invite_not_found")
	}
	invites, err := s.platform.store.OpenInvites(ctx, p.w.Team.ID)
	if err != nil {
		return platformstore.TeamInvite{}, peopleErr(ctx, "invites", err)
	}
	for _, inv := range invites {
		if hmac.Equal([]byte(InviteRef(s.platform.cfg.TokenPepper, inv.ID)), []byte(ref)) {
			return inv, nil
		}
	}
	return platformstore.TeamInvite{}, status.Error(codes.NotFound, "invite_not_found")
}

// RevokeTeamInvite cancels an open invite: its link dies.
func (s *Server) RevokeTeamInvite(ctx context.Context, req *pb_admin.RevokeTeamInviteRequest) (*pb_admin.RevokeTeamInviteResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := s.openInvite(ctx, p, req.GetInviteRef())
	if err != nil {
		return nil, err
	}
	if inv.Role == roleOwner && !p.owner {
		return nil, status.Error(codes.PermissionDenied, "owner_only")
	}
	if err := s.platform.store.RevokeInvite(ctx, p.w.Team.ID, inv.ID, time.Now().UTC()); err != nil {
		return nil, peopleErr(ctx, "revoke invite", err)
	}
	s.peopleAudit(ctx, p, "member_invite_revoke", map[string]string{"email": inv.Email})
	return &pb_admin.RevokeTeamInviteResponse{}, nil
}

// ResendTeamInvite sends an open invite again with a new link (the old one dies) and a new expiry.
func (s *Server) ResendTeamInvite(ctx context.Context, req *pb_admin.ResendTeamInviteRequest) (*pb_admin.ResendTeamInviteResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := s.openInvite(ctx, p, req.GetInviteRef())
	if err != nil {
		return nil, err
	}
	if inv.Role == roleOwner && !p.owner {
		return nil, status.Error(codes.PermissionDenied, "owner_only")
	}
	token, hash, err := platformauth.NewToken()
	if err != nil {
		return nil, peopleErr(ctx, "token", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	link := s.inviteLink(p.w.Team.Slug, token)
	if err := s.platform.store.ResendInvite(ctx, platformstore.ResendParams{
		TeamID: p.w.Team.ID, InviteID: inv.ID, TokenHash: hash, ExpiresAt: now.Add(InviteTTL),
		Now: now, MaxPerHour: inviteLettersPerHour,
		Letter: func(platformstore.TeamInvite) platformstore.Letter {
			return platformstore.Letter{Template: "invite", Link: link, TeamName: p.w.Team.Name, Slug: p.w.Team.Slug, From: p.handle}
		},
	}); err != nil {
		return nil, peopleErr(ctx, "resend invite", err)
	}
	s.peopleAudit(ctx, p, "member_invite_resend", map[string]string{"email": inv.Email})
	return &pb_admin.ResendTeamInviteResponse{}, nil
}

// member finds an active member of the caller's team by handle.
func (s *Server) member(ctx context.Context, p peopleCtx, username string) (platformstore.ConsoleMember, error) {
	username = strings.TrimSpace(username)
	members, err := s.platform.store.TeamMembers(ctx, p.w.Team.ID)
	if err != nil {
		return platformstore.ConsoleMember{}, peopleErr(ctx, "members", err)
	}
	for _, m := range members {
		if strings.EqualFold(m.Handle, username) {
			return m, nil
		}
	}
	return platformstore.ConsoleMember{}, status.Error(codes.NotFound, "not_member")
}

// dropSessions refuses the revoked sids at the edge on the next call (the control plane already
// revoked them in the member change's transaction).
func (s *Server) dropSessions(sids []string) {
	if s.platform.invalidateSession == nil {
		return
	}
	for _, sid := range sids {
		s.platform.invalidateSession(sid)
	}
}

// RemoveTeamMember removes a member: membership removed and their team sessions revoked in one
// transaction, their admins row disabled, the edge cache dropped. Never the last owner; only an
// owner removes an owner.
func (s *Server) RemoveTeamMember(ctx context.Context, req *pb_admin.RemoveTeamMemberRequest) (*pb_admin.RemoveTeamMemberResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.member(ctx, p, req.GetUsername())
	if err != nil {
		return nil, err
	}
	if m.Role == roleOwner && !p.owner {
		return nil, status.Error(codes.PermissionDenied, "owner_only")
	}
	ch, err := s.platform.store.RemoveMember(ctx, p.w.Team.ID, m.UserID, time.Now().UTC())
	if err != nil {
		return nil, peopleErr(ctx, "remove member", err)
	}
	s.dropSessions(ch.RevokedSessions)
	// Membership and sessions are already gone (the edge refuses them); the admins row is the
	// team database's mirror, so a failure here is logged, not returned.
	if err := teamsync.DisableAdmin(ctx, s.repo.Admin(), ch.Handle); err != nil {
		slog.Default().ErrorContext(ctx, "team members: disable admin", slog.String("err", err.Error()))
	}
	s.peopleAudit(ctx, p, "member_remove", map[string]string{"handle": ch.Handle})
	return &pb_admin.RemoveTeamMemberResponse{}, nil
}

// SetTeamMemberRole changes a member's role (owner only). Never leaves the team without an owner.
// The member's team sessions are revoked so the new claims apply on the next sign-in.
func (s *Server) SetTeamMemberRole(ctx context.Context, req *pb_admin.SetTeamMemberRoleRequest) (*pb_admin.SetTeamMemberRoleResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.people(ctx)
	if err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(req.GetRole()))
	if role != roleOwner && role != roleMember {
		return nil, status.Error(codes.InvalidArgument, "bad_role")
	}
	if !p.owner {
		return nil, status.Error(codes.PermissionDenied, "owner_only")
	}
	m, err := s.member(ctx, p, req.GetUsername())
	if err != nil {
		return nil, err
	}
	if m.Role == role {
		return &pb_admin.SetTeamMemberRoleResponse{}, nil
	}
	ch, err := s.platform.store.SetMemberRole(ctx, p.w.Team.ID, m.UserID, role, time.Now().UTC())
	if err != nil {
		return nil, peopleErr(ctx, "set role", err)
	}
	s.dropSessions(ch.RevokedSessions)
	var perms []entity.AdminPermission
	if role == roleMember {
		// The admins row keeps the grants it had; a former owner (super, no rows) gets the default.
		if acc, err := s.repo.Admin().GetAccountWithPermissions(ctx, ch.Handle); err == nil && acc != nil {
			perms = acc.Permissions
		}
		if len(perms) == 0 {
			perms = DefaultInvitePermissions(p.w.Features)
		}
	}
	if err := teamsync.EnsureAdmin(ctx, s.repo.Admin(), ch.Handle, role == roleOwner, perms); err != nil {
		slog.Default().ErrorContext(ctx, "team members: ensure admin", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "internal error")
	}
	s.peopleAudit(ctx, p, "member_role", map[string]string{"handle": ch.Handle, "role": role})
	return &pb_admin.SetTeamMemberRoleResponse{}, nil
}
