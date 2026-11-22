package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// JoinLinkToken is the public token of one team join-link generation.
func JoinLinkToken(pepper string, teamID, gen int64, nonce []byte) string {
	return platformstore.JoinLinkToken(pepper, teamID, gen, nonce)
}

// JoinRequestRef is the opaque handle of a pending team join request.
func JoinRequestRef(pepper string, id int64) string {
	return platformstore.JoinRequestRef(pepper, id)
}

// DefaultInvitePermissions is a new member's grant (teams §3): write on every section of the
// team's grantable set (teamSections: every section whose feature is on for the team or is core,
// except accounts), in screen order. on is the team's switched-on features.
func DefaultInvitePermissions(on []feature.Key) []entity.AdminPermission {
	sections := teamSections(on)
	out := make([]entity.AdminPermission, 0, len(sections))
	for _, sec := range sections {
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

// actor is who the store re-checks inside each change's transaction.
func (p peopleCtx) actor() platformstore.Actor {
	return platformstore.Actor{UserID: p.userID, Operator: p.w.Operator}
}

// changer resolves the caller of a people change: every change (invite, resend, revoke, remove,
// role) is an owner's (or an entered operator's). The store re-checks it under lock.
func (s *Server) changer(ctx context.Context) (peopleCtx, error) {
	p, err := s.people(ctx)
	if err != nil {
		return peopleCtx{}, err
	}
	if !p.owner {
		return peopleCtx{}, status.Error(codes.PermissionDenied, "owner_only")
	}
	return p, nil
}

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

// inviterName is the «from» of an invite letter: the inviter's first + last name, else their
// @username, else the handle. A failed profile read falls back to the handle, never fails the invite.
func (s *Server) inviterName(ctx context.Context, p peopleCtx) string {
	prof, err := s.platform.store.Profile(ctx, p.userID)
	if err != nil {
		return p.handle
	}
	if name := platformstore.DisplayName(prof.FirstName, prof.LastName); name != "" {
		return name
	}
	if prof.PublicUsername != "" {
		return prof.PublicUsername
	}
	return p.handle
}

func peopleErr(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, platformstore.ErrLastOwner):
		return status.Error(codes.FailedPrecondition, "last_owner")
	case errors.Is(err, platformstore.ErrNotOwner):
		return status.Error(codes.PermissionDenied, "owner_only")
	case errors.Is(err, platformstore.ErrNotMember), errors.Is(err, platformstore.ErrUserNotFound):
		return status.Error(codes.NotFound, "not_member")
	case errors.Is(err, platformstore.ErrInviteNotFound):
		return status.Error(codes.NotFound, "invite_not_found")
	case errors.Is(err, platformstore.ErrAlreadyMember):
		return status.Error(codes.AlreadyExists, "already_member")
	case errors.Is(err, platformstore.ErrLetterCap):
		return status.Error(codes.ResourceExhausted, "letter_cap")
	case errors.Is(err, platformstore.ErrTeamLetterCap):
		return status.Error(codes.ResourceExhausted, "team_letter_cap")
	case errors.Is(err, platformstore.ErrRotateCap):
		return status.Error(codes.ResourceExhausted, "rotate_cap")
	case errors.Is(err, platformstore.ErrJoinLinkInvalid):
		return status.Error(codes.NotFound, "request_not_found")
	case errors.Is(err, platformstore.ErrJoinRequestNotFound):
		return status.Error(codes.NotFound, "request_not_found")
	case errors.Is(err, platformstore.ErrJoinConfirmEmail), errors.Is(err, platformstore.ErrJoinDisabled),
		errors.Is(err, platformstore.ErrJoinOperator):
		return status.Error(codes.FailedPrecondition, "request_unavailable")
	case errors.Is(err, platformstore.ErrTeamReadOnly):
		return status.Error(codes.FailedPrecondition, "team_read_only")
	case errors.Is(err, platformstore.ErrOwnerTarget):
		return status.Error(codes.FailedPrecondition, "owner_full")
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
	switch {
	case inv.Bounced: // its letter did not reach the address (X6); the fix is a new invite
		st = "bounced"
	case !inv.ExpiresAt.After(now):
		st = "expired"
	}
	return &pb_admin.TeamInvite{
		InviteRef: InviteRef(pepper, inv.ID),
		Email:     inv.Email,
		Role:      inv.Role,
		SentAt:    timestamppb.New(inv.SentAt),
		ExpiresAt: timestamppb.New(inv.ExpiresAt),
		Status:    st,
		Name:      inv.Name,
	}
}

// accountsReadAccess is the gate ListTeamMembers had at the edge while it was rd(accounts), kept
// for teams mode off: a legacy token, a super, or accounts read. Fails closed without authz.
func accountsReadAccess(ctx context.Context) bool {
	az, ok := authsrv.GetAdminAuthz(ctx)
	if !ok {
		return false
	}
	if az.FullAccess() {
		return true
	}
	lvl, ok := az.Perms[rbac.SectionAccounts]
	return ok && lvl.Covers(entity.AccessRead)
}

// ListTeamMembers: the caller's team's active members (a member's grants inside the grantable set,
// none for an owner), open invites and the grantable set in screen order. It is on the rbac
// allowlist so a member can open the workspace screen. Teams mode off: team #1's admins rows (super
// → owner), no invites, behind the edge's old accounts-read gate with the edge's exact refusal, so
// prod answers as before.
func (s *Server) ListTeamMembers(ctx context.Context, _ *pb_admin.ListTeamMembersRequest) (*pb_admin.ListTeamMembersResponse, error) {
	me := authsrv.GetAdminUsername(ctx)
	if !s.teamsOn() {
		if !accountsReadAccess(ctx) {
			return nil, status.Errorf(codes.PermissionDenied, "insufficient permissions for %s", rbac.MethodPrefix+"ListTeamMembers")
		}
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
	// The list is on the allowlist, so the caller must be an active member of this team by the
	// identity the write handlers use (people: the token's user, or for a legacy team #1 token the
	// platform user of its handle) unless an entered operator. A legacy admin whose handle belongs to
	// a platform user outside the team is no member: the edge ran no membership check for it.
	if !p.w.Operator && !hasMember(members, p.userID) {
		return nil, status.Error(codes.PermissionDenied, "not_member")
	}
	invites, err := s.platform.store.OpenInvites(ctx, p.w.Team.ID)
	if err != nil {
		return nil, peopleErr(ctx, "invites", err)
	}
	requests, err := s.platform.store.PendingJoinRequests(ctx, p.w.Team.ID, time.Now().UTC())
	if err != nil {
		return nil, peopleErr(ctx, "join requests", err)
	}
	// Grants live in the team database's admins rows. A failed read fails the call: rights shown as
	// none would be saved back as none by the screen's next click (a full-set write).
	accounts, err := s.repo.Admin().ListAccounts(ctx)
	if err != nil {
		return nil, peopleErr(ctx, "list accounts", err)
	}
	grants := make(map[string][]entity.AdminPermission, len(accounts))
	for _, a := range accounts {
		grants[strings.ToLower(a.Username)] = a.Permissions
	}
	sections := teamSections(p.w.Features)
	now := time.Now().UTC()
	out := &pb_admin.ListTeamMembersResponse{
		Members:      make([]*pb_admin.TeamMember, 0, len(members)),
		Invites:      make([]*pb_admin.TeamInvite, 0, len(invites)),
		Sections:     sectionsPB(sections),
		JoinRequests: make([]*pb_admin.TeamJoinRequest, 0, len(requests)),
	}
	for _, m := range members {
		tm := &pb_admin.TeamMember{Username: m.Handle, Email: m.Email.String, Role: m.Role, You: m.Handle == me && !p.w.Operator,
			Permissions:    []*pb_admin.AdminPermission{},
			Name:           platformstore.DisplayName(m.FirstName, m.LastName),
			PublicUsername: m.Username(),
			AvatarUrl:      s.avatarURL(m.AvatarSHA.String),
		}
		if m.Role != roleOwner {
			tm.Permissions = grantsPB(sections, grants[strings.ToLower(m.Handle)])
		}
		if m.LastSeenAt.Valid {
			tm.LastSeenAt = timestamppb.New(m.LastSeenAt.Time)
		}
		out.Members = append(out.Members, tm)
	}
	for _, inv := range invites {
		out.Invites = append(out.Invites, invitePB(s.platform.cfg.TokenPepper, inv, now))
	}
	for _, req := range requests {
		out.JoinRequests = append(out.JoinRequests, &pb_admin.TeamJoinRequest{
			RequestRef: JoinRequestRef(s.platform.cfg.TokenPepper, req.ID),
			Email:      req.Email,
			CreatedAt:  timestamppb.New(req.CreatedAt),
		})
	}
	return out, nil
}

func (s *Server) teamJoinLink(slug string, l platformstore.TeamJoinLink) string {
	token := JoinLinkToken(s.platform.cfg.TokenPepper, l.TeamID, l.Gen, l.Nonce)
	return "https://" + slug + "." + strings.ToLower(strings.TrimSpace(s.platform.cfg.BaseDomain)) + "/join#link=" + token
}

func (s *Server) teamHomeLink(slug string) string {
	return "https://" + slug + "." + strings.ToLower(strings.TrimSpace(s.platform.cfg.BaseDomain)) + "/"
}

// GetTeamJoinLink creates the team's single link on first copy and re-derives the same value later.
func (s *Server) GetTeamJoinLink(ctx context.Context, _ *pb_admin.GetTeamJoinLinkRequest) (*pb_admin.GetTeamJoinLinkResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	link, err := s.platform.store.EnsureJoinLink(ctx, platformstore.JoinLinkChange{
		TeamID: p.w.Team.ID, Actor: p.actor(), IP: middleware.GetClientIP(ctx), Now: time.Now().UTC().Truncate(time.Millisecond),
	})
	if err != nil {
		return nil, peopleErr(ctx, "get join link", err)
	}
	return &pb_admin.GetTeamJoinLinkResponse{Link: s.teamJoinLink(p.w.Team.Slug, link)}, nil
}

// RotateTeamJoinLink invalidates the old link and closes pending requests from older generations.
func (s *Server) RotateTeamJoinLink(ctx context.Context, _ *pb_admin.RotateTeamJoinLinkRequest) (*pb_admin.RotateTeamJoinLinkResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	link, err := s.platform.store.RotateJoinLink(ctx, platformstore.JoinLinkChange{
		TeamID: p.w.Team.ID, Actor: p.actor(), IP: middleware.GetClientIP(ctx), Now: time.Now().UTC().Truncate(time.Millisecond),
	})
	if err != nil {
		return nil, peopleErr(ctx, "rotate join link", err)
	}
	return &pb_admin.RotateTeamJoinLinkResponse{Link: s.teamJoinLink(p.w.Team.Slug, link)}, nil
}

func (s *Server) joinRequest(ctx context.Context, p peopleCtx, ref string, resumeApproved bool) (platformstore.PendingJoinRequest, error) {
	ref = strings.TrimSpace(ref)
	if len(ref) != inviteRefLen {
		return platformstore.PendingJoinRequest{}, status.Error(codes.NotFound, "request_not_found")
	}
	now := time.Now().UTC()
	requests, err := s.platform.store.PendingJoinRequests(ctx, p.w.Team.ID, now)
	if resumeApproved {
		requests, err = s.platform.store.JoinRequestsForApproval(ctx, p.w.Team.ID, now)
	}
	if err != nil {
		return platformstore.PendingJoinRequest{}, peopleErr(ctx, "join requests", err)
	}
	for _, req := range requests {
		if hmac.Equal([]byte(JoinRequestRef(s.platform.cfg.TokenPepper, req.ID)), []byte(ref)) {
			return req, nil
		}
	}
	return platformstore.PendingJoinRequest{}, status.Error(codes.NotFound, "request_not_found")
}

type joinRequestAdmins interface {
	StageJoin(context.Context, string, string, bool, []entity.AdminPermission) error
	FinishJoin(context.Context, string) (bool, error)
}

func (s *Server) stageJoinRequest(ctx context.Context, m platformstore.JoinMember, perms []entity.AdminPermission) error {
	// An owner is a super admin with no rows, the way EnsureAdmin stages one for SetTeamMemberRole.
	super := m.Role == roleOwner
	if super {
		perms = nil
	}
	admins, ok := s.repo.Admin().(joinRequestAdmins)
	if !ok {
		return errors.New("admin repository cannot stage team joins")
	}
	hash, err := platformauth.UnusableHash(s.pwhash)()
	if err != nil {
		return fmt.Errorf("join request hash: %w", err)
	}
	if err := admins.StageJoin(ctx, m.Handle, hash, super, perms); err != nil {
		return fmt.Errorf("stage join request: %w", err)
	}
	if s.invalidateAdminAuth != nil {
		s.invalidateAdminAuth(m.Handle)
	}
	return nil
}

func (s *Server) enableJoinRequest(ctx context.Context, m platformstore.JoinMember) (bool, error) {
	if _, ok, err := s.platform.store.Membership(ctx, m.UserID, m.TeamID); err != nil || !ok {
		return false, err
	}
	admins, ok := s.repo.Admin().(joinRequestAdmins)
	if !ok {
		return false, errors.New("admin repository cannot finish team joins")
	}
	finished, err := admins.FinishJoin(ctx, m.Handle)
	if s.invalidateAdminAuth != nil {
		s.invalidateAdminAuth(m.Handle)
	}
	if err != nil {
		return false, fmt.Errorf("finish join request: %w", err)
	}
	return finished, nil
}

// ApproveTeamJoinRequest uses the same four-step join as an invite, with the ordinary invite defaults.
func (s *Server) ApproveTeamJoinRequest(ctx context.Context, req *pb_admin.ApproveTeamJoinRequestRequest) (*pb_admin.ApproveTeamJoinRequestResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	role := strings.ToLower(strings.TrimSpace(req.GetRole()))
	if role == "" {
		role = roleMember
	}
	if role != roleOwner && role != roleMember {
		return nil, status.Error(codes.InvalidArgument, "bad_role")
	}
	pending, err := s.joinRequest(ctx, p, req.GetRequestRef(), true)
	if err != nil {
		return nil, err
	}
	perms := DefaultInvitePermissions(p.w.Features)
	raw, err := permissionsJSON(perms)
	if err != nil {
		return nil, peopleErr(ctx, "join permissions", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	res, err := s.platform.store.ApproveJoinRequest(ctx, platformstore.JoinRequestDecisionParams{
		TeamID: p.w.Team.ID, RequestID: pending.ID, Actor: p.actor(), IP: middleware.GetClientIP(ctx), Now: now,
		Permissions: raw, Role: role, ApprovedLink: s.teamHomeLink(p.w.Team.Slug),
		Stage: func(ctx context.Context, m platformstore.JoinMember) error {
			return s.stageJoinRequest(ctx, m, perms)
		},
		Enable: s.enableJoinRequest,
	})
	if err != nil {
		return nil, peopleErr(ctx, "approve join request", err)
	}
	if res.EnablePending {
		slog.Default().WarnContext(ctx, "team members: approved join row not enabled", slog.String("email", pending.Email))
	}
	return &pb_admin.ApproveTeamJoinRequestResponse{}, nil
}

func (s *Server) DeclineTeamJoinRequest(ctx context.Context, req *pb_admin.DeclineTeamJoinRequestRequest) (*pb_admin.DeclineTeamJoinRequestResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := s.joinRequest(ctx, p, req.GetRequestRef(), false)
	if err != nil {
		return nil, err
	}
	if err := s.platform.store.DeclineJoinRequest(ctx, platformstore.JoinRequestDecisionParams{
		TeamID: p.w.Team.ID, RequestID: pending.ID, Actor: p.actor(), IP: middleware.GetClientIP(ctx),
		Now: time.Now().UTC().Truncate(time.Millisecond),
	}); err != nil {
		return nil, peopleErr(ctx, "decline join request", err)
	}
	return &pb_admin.DeclineTeamJoinRequestResponse{}, nil
}

// InviteTeamMember invites an email into the caller's team. An open invite for the email (an
// expired one included) is re-used (its role updated, its permissions too when the request names
// rights, the old link dead) and re-sent. An active member of this team with that email is
// already_member; three letters an hour to one address is letter_cap.
func (s *Server) InviteTeamMember(ctx context.Context, req *pb_admin.InviteTeamMemberRequest) (*pb_admin.InviteTeamMemberResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
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
	// The name the inviter typed (one box): it prefills the join sheet and seeds a new account's
	// first/last name. Stored as the display form of its split; empty keeps a re-invite's name.
	first, last, err := platformauth.SplitFullName(req.GetName())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "bad_name")
	}
	// Rights named by the request follow SetTeamMemberAccess's rule; none named = the team's default
	// for a new invite, and a re-invite of an open one keeps what it holds.
	grants := DefaultInvitePermissions(p.w.Features)
	named := len(req.GetPermissions()) > 0
	if named {
		if grants, err = teamGrants(teamSections(p.w.Features), req.GetPermissions()); err != nil {
			return nil, err
		}
	}
	perms, err := permissionsJSON(grants)
	if err != nil {
		return nil, peopleErr(ctx, "permissions", err)
	}
	token, hash, err := platformauth.NewToken()
	if err != nil {
		return nil, peopleErr(ctx, "token", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	from := s.inviterName(ctx, p)
	inv, err := s.platform.store.UpsertInvite(ctx, platformstore.InviteParams{
		TeamID: p.w.Team.ID, Email: email, Role: role, Permissions: perms, KeepPermissions: !named, TokenHash: hash,
		Name:      platformstore.DisplayName(first, last),
		ExpiresAt: now.Add(InviteTTL), Actor: p.actor(), Now: now, MaxPerHour: inviteLettersPerHour,
		TeamCap: s.platform.cfg.InviteTeamCap(),
		Letter: platformstore.Letter{
			To: email, Template: "invite", Link: s.inviteLink(p.w.Team.Slug, token),
			TeamName: p.w.Team.Name, Slug: p.w.Team.Slug, From: from,
		},
	})
	if err != nil {
		return nil, peopleErr(ctx, "invite", err)
	}
	detail := map[string]string{"email": email, "role": role}
	if named {
		detail["grants"] = grantsDetail(grants)
	}
	s.peopleAudit(ctx, p, "member_invite", detail)
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
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := s.openInvite(ctx, p, req.GetInviteRef())
	if err != nil {
		return nil, err
	}
	if err := s.platform.store.RevokeInvite(ctx, p.w.Team.ID, inv.ID, p.actor(), time.Now().UTC()); err != nil {
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
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := s.openInvite(ctx, p, req.GetInviteRef())
	if err != nil {
		return nil, err
	}
	token, hash, err := platformauth.NewToken()
	if err != nil {
		return nil, peopleErr(ctx, "token", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	link := s.inviteLink(p.w.Team.Slug, token)
	from := s.inviterName(ctx, p)
	if err := s.platform.store.ResendInvite(ctx, platformstore.ResendParams{
		TeamID: p.w.Team.ID, InviteID: inv.ID, Actor: p.actor(), TokenHash: hash, ExpiresAt: now.Add(InviteTTL),
		Now: now, MaxPerHour: inviteLettersPerHour, TeamCap: s.platform.cfg.InviteTeamCap(),
		Letter: func(platformstore.TeamInvite) platformstore.Letter {
			return platformstore.Letter{Template: "invite", Link: link, TeamName: p.w.Team.Name, Slug: p.w.Team.Slug, From: from}
		},
	}); err != nil {
		return nil, peopleErr(ctx, "resend invite", err)
	}
	s.peopleAudit(ctx, p, "member_invite_resend", map[string]string{"email": inv.Email})
	return &pb_admin.ResendTeamInviteResponse{}, nil
}

// hasMember reports whether the platform user uid is among the team's active members.
func hasMember(members []platformstore.ConsoleMember, uid int64) bool {
	for _, m := range members {
		if uid > 0 && m.UserID == uid {
			return true
		}
	}
	return false
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

// mirrorOwners are the team's active owners (other than target) whose admins row in the team
// database is confirmed super and enabled: only they count as the owner left behind when an owner
// is demoted or removed.
func (s *Server) mirrorOwners(ctx context.Context, p peopleCtx, target string) ([]int64, error) {
	members, err := s.platform.store.TeamMembers(ctx, p.w.Team.ID)
	if err != nil {
		return nil, peopleErr(ctx, "members", err)
	}
	var out []int64
	for _, m := range members {
		if m.Role != roleOwner || strings.EqualFold(m.Handle, target) {
			continue
		}
		acc, err := s.repo.Admin().GetAdminByUsername(ctx, m.Handle)
		if err != nil || acc == nil || !acc.IsSuper || acc.Disabled {
			continue
		}
		out = append(out, m.UserID)
	}
	return out, nil
}

// change builds the store's member change for target; an owner target carries the confirmed owners.
func (s *Server) change(ctx context.Context, p peopleCtx, m platformstore.ConsoleMember) (platformstore.MemberChangeParams, error) {
	ch := platformstore.MemberChangeParams{TeamID: p.w.Team.ID, UserID: m.UserID, Actor: p.actor(), Now: time.Now().UTC()}
	if m.Role == roleOwner {
		mirror, err := s.mirrorOwners(ctx, p, m.Handle)
		if err != nil {
			return ch, err
		}
		ch.MirrorOwners = mirror
	}
	return ch, nil
}

// RemoveTeamMember removes a member (owner only): membership removed and their team sessions
// revoked in one transaction, their admins row disabled, the edge cache dropped. Never the last
// working owner.
func (s *Server) RemoveTeamMember(ctx context.Context, req *pb_admin.RemoveTeamMemberRequest) (*pb_admin.RemoveTeamMemberResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.member(ctx, p, req.GetUsername())
	if err != nil {
		return nil, err
	}
	params, err := s.change(ctx, p, m)
	if err != nil {
		return nil, err
	}
	ch, err := s.platform.store.RemoveMember(ctx, params)
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

// SetTeamMemberRole changes a member's role (owner only). Never leaves the team without a working
// owner. The member's team sessions are revoked so the new claims apply on the next sign-in. The
// team-side admins row is written after the control plane: a call with the role the member
// already has re-writes that row, so a retry after a failed mirror repairs it.
func (s *Server) SetTeamMemberRole(ctx context.Context, req *pb_admin.SetTeamMemberRoleRequest) (*pb_admin.SetTeamMemberRoleResponse, error) {
	if !s.teamsOn() {
		return nil, errTeamsOff()
	}
	role := strings.ToLower(strings.TrimSpace(req.GetRole()))
	if role != roleOwner && role != roleMember {
		return nil, status.Error(codes.InvalidArgument, "bad_role")
	}
	p, err := s.changer(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.member(ctx, p, req.GetUsername())
	if err != nil {
		return nil, err
	}
	params, err := s.change(ctx, p, m)
	if err != nil {
		return nil, err
	}
	ch, err := s.platform.store.SetMemberRole(ctx, params, role)
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
	if m.Role != role {
		s.peopleAudit(ctx, p, "member_role", map[string]string{"handle": ch.Handle, "role": role})
	}
	return &pb_admin.SetTeamMemberRoleResponse{}, nil
}
