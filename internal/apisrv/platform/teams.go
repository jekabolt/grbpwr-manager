package platform

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Console statuses (PlatformTeam.status).
const (
	statusActive       = "active"
	statusReadOnly     = "read_only"
	statusProvisioning = "provisioning"
	statusPending      = "pending"
	statusReview       = "review"
	statusDeleted      = "deleted"
)

// SetConfig hands the server the platform config (team addresses for letters). Called once at boot.
func (s *Server) SetConfig(cfg platformstore.Config) {
	s.cfg = cfg
}

// SetBudgetZone sets the owner's budget zone the month of AI spend is counted in ("" or unset =
// entity.DefaultBudgetTimezone).
func (s *Server) SetBudgetZone(fn func() string) {
	s.budgetZone = fn
}

func (s *Server) nowUTC() time.Time {
	if s.now != nil {
		return s.now().UTC().Truncate(time.Millisecond)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

func caller(ctx context.Context) (auth.PlatformCaller, error) {
	c, ok := auth.GetPlatformCaller(ctx)
	if !ok || c.UserID <= 0 {
		return auth.PlatformCaller{}, status.Error(codes.Unauthenticated, "unauthorized")
	}
	return c, nil
}

func (s *Server) audit(ctx context.Context, actor, teamID int64, action string, detail map[string]string) {
	if err := s.st.AuditAuth(ctx, actor, teamID, action, detail, middleware.GetClientIP(ctx)); err != nil {
		slog.Default().ErrorContext(ctx, "platform: audit "+action, slog.String("err", err.Error()))
	}
}

func (s *Server) dropTeam(teamID int64) {
	s.st.Directory().Invalidate(teamID)
	if s.invalidate != nil {
		s.invalidate(teamID)
	}
}

// storeErr maps a control-plane error to the console's answer.
func storeErr(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, platformstore.ErrTeamNotFound):
		return status.Error(codes.NotFound, "team_not_found")
	case errors.Is(err, platformstore.ErrWrongTeamStatus):
		return status.Error(codes.FailedPrecondition, "wrong_status")
	case errors.Is(err, platformstore.ErrSlugTaken):
		return status.Error(codes.AlreadyExists, "slug_taken")
	}
	slog.Default().ErrorContext(ctx, "platform: "+op, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "internal error")
}

// team reads one team by slug for a console action: a deleted team answers wrong_status.
func (s *Server) team(ctx context.Context, slug string) (platformstore.ConsoleTeam, error) {
	t, err := s.st.ConsoleTeam(ctx, slug)
	if err != nil {
		return platformstore.ConsoleTeam{}, storeErr(ctx, "read team", err)
	}
	return t, nil
}

// teamWithDeleted is team that also reads a deleted team (GetTeam, DeleteTeam, RestoreTeam).
func (s *Server) teamWithDeleted(ctx context.Context, slug string) (platformstore.ConsoleTeam, error) {
	t, err := s.st.ConsoleTeamWithDeleted(ctx, slug)
	if err != nil {
		return platformstore.ConsoleTeam{}, storeErr(ctx, "read team", err)
	}
	return t, nil
}

// teamPB builds the console row of a team. spend is this month's AI spend by team id.
func teamPB(t platformstore.ConsoleTeam, spend map[int64]platformstore.AISpend) *pb_admin.PlatformTeam {
	out := &pb_admin.PlatformTeam{
		Slug:            t.Slug,
		Name:            t.Name,
		Status:          consoleStatus(t.Status),
		People:          int32(t.People),
		CreatedAt:       timestamppb.New(t.CreatedAt),
		Features:        []string{},
		AiSpendMonthUsd: decimal.Zero.StringFixed(2),
		SchemaOk:        t.SchemaOK,
	}
	if t.Status == string(tenant.StatusDeleted) && t.PurgeAt.Valid {
		out.PurgeAt = timestamppb.New(t.PurgeAt.Time)
	}
	if t.Status == string(tenant.StatusReview) {
		// A team waiting for approval has no workspace: its creator, nothing switched, no spend.
		out.People, out.SchemaOk = 1, true
		return out
	}
	eff := feature.Effective(t.ID, t.Profile, t.Overrides)
	for _, info := range feature.Catalog() {
		if eff[info.Key] {
			out.Features = append(out.Features, string(info.Key))
		}
	}
	if sp, ok := spend[t.ID]; ok {
		out.AiSpendMonthUsd = sp.CostUSD.StringFixed(2)
		out.AiUnpricedCallsMonth = int32(min(sp.Unpriced, int64(1<<31-1)))
	}
	return out
}

func consoleStatus(st string) string {
	switch tenant.Status(st) {
	case tenant.StatusActive:
		return statusActive
	case tenant.StatusSuspended:
		return statusReadOnly
	case tenant.StatusProvisioning:
		return statusProvisioning
	case tenant.StatusReview:
		return statusReview
	case tenant.StatusDeleted:
		return statusDeleted
	default:
		return statusPending
	}
}

// ListTeams is the console matrix: every team that is not deleted, then the deleted ones whose
// data is not wiped yet (status deleted, purge_at), with people, switched-on features, this month's
// AI spend and schema health. A fixed number of control-plane queries.
func (s *Server) ListTeams(ctx context.Context, req *pb_admin.ListTeamsRequest) (*pb_admin.ListTeamsResponse, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	teams, err := s.st.ConsoleTeams(ctx, req.GetQuery())
	if err != nil {
		return nil, storeErr(ctx, "list teams", err)
	}
	ids := make([]int64, 0, len(teams))
	for _, t := range teams {
		ids = append(ids, t.ID)
	}
	spend, err := s.monthSpend(ctx, ids...)
	if err != nil {
		return nil, storeErr(ctx, "month spend", err)
	}
	total := decimal.Zero
	out := &pb_admin.ListTeamsResponse{Teams: make([]*pb_admin.PlatformTeam, 0, len(teams))}
	for _, t := range teams {
		out.Teams = append(out.Teams, teamPB(t, spend))
		if t.Status != string(tenant.StatusReview) {
			total = total.Add(spend[t.ID].CostUSD)
		}
	}
	out.AiSpendMonthTotalUsd = total.StringFixed(2)
	return out, nil
}

// GetTeam is one team with its active members; a deleted team whose data is not wiped yet too.
func (s *Server) GetTeam(ctx context.Context, req *pb_admin.GetTeamRequest) (*pb_admin.GetTeamResponse, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	t, err := s.teamWithDeleted(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	spend, err := s.monthSpend(ctx, t.ID)
	if err != nil {
		return nil, storeErr(ctx, "month spend", err)
	}
	members, err := s.st.TeamMembers(ctx, t.ID)
	if err != nil {
		return nil, storeErr(ctx, "team members", err)
	}
	out := &pb_admin.GetTeamResponse{Team: teamPB(t, spend), Members: make([]*pb_admin.PlatformMember, 0, len(members))}
	for _, m := range members {
		pm := &pb_admin.PlatformMember{Handle: m.Handle, Email: m.Email.String, Role: m.Role}
		if m.LastSeenAt.Valid {
			pm.LastSeenAt = timestamppb.New(m.LastSeenAt.Time)
		}
		out.Members = append(out.Members, pm)
	}
	return out, nil
}

// refreshed re-reads a team after a change and builds its console row (a deleted team included).
func (s *Server) refreshed(ctx context.Context, slug string) (*pb_admin.PlatformTeam, error) {
	t, err := s.teamWithDeleted(ctx, slug)
	if err != nil {
		return nil, err
	}
	spend, err := s.monthSpend(ctx, t.ID)
	if err != nil {
		return nil, storeErr(ctx, "month spend", err)
	}
	return teamPB(t, spend), nil
}

// ApproveTeam opens a waiting sign-up: review → provisioning (the provisioner claims it) and the
// team_approved letter to its owner, in one transaction.
func (s *Server) ApproveTeam(ctx context.Context, req *pb_admin.ApproveTeamRequest) (*pb_admin.ApproveTeamResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if t.Status != string(tenant.StatusReview) {
		return nil, status.Error(codes.FailedPrecondition, "wrong_status")
	}
	link := "https://" + t.Slug + "." + strings.ToLower(strings.TrimSpace(s.cfg.BaseDomain)) + "/"
	if err := s.st.ApproveTeam(ctx, t.ID, link, s.nowUTC()); err != nil {
		return nil, storeErr(ctx, "approve team", err)
	}
	s.dropTeam(t.ID)
	s.audit(ctx, c.UserID, t.ID, "team_approve", map[string]string{"slug": t.Slug})
	team, err := s.refreshed(ctx, t.Slug)
	if err != nil {
		return nil, err
	}
	return &pb_admin.ApproveTeamResponse{Team: team}, nil
}

// DeclineTeam deletes a waiting sign-up's team; the slug is free again, no letter is sent.
func (s *Server) DeclineTeam(ctx context.Context, req *pb_admin.DeclineTeamRequest) (*pb_admin.DeclineTeamResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if t.Status != string(tenant.StatusReview) {
		return nil, status.Error(codes.FailedPrecondition, "wrong_status")
	}
	if err := s.st.DeclineTeam(ctx, t.ID); err != nil {
		return nil, storeErr(ctx, "decline team", err)
	}
	s.dropTeam(t.ID)
	// The team row is gone: the audit keeps the slug in its detail, not a dangling team id.
	s.audit(ctx, c.UserID, 0, "team_decline", map[string]string{"slug": t.Slug, "name": t.Name})
	return &pb_admin.DeclineTeamResponse{}, nil
}

// EnterTeam is operator entry (teams §23): a single-use 10-minute hand-off code the team host's
// /auth page exchanges for a team token with the op claim. No membership, nothing the team sees.
func (s *Server) EnterTeam(ctx context.Context, req *pb_admin.EnterTeamRequest) (*pb_admin.EnterTeamResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	switch tenant.Status(t.Status) {
	case tenant.StatusActive, tenant.StatusSuspended:
	default:
		return nil, status.Error(codes.FailedPrecondition, "wrong_status")
	}
	code, hash, err := platformauth.NewOperatorCode()
	if err != nil {
		return nil, storeErr(ctx, "operator code", err)
	}
	now := s.nowUTC()
	// Issued only while the operator's console session is live (a password change revokes it and
	// expires unused hand-offs in one transaction).
	err = s.st.IssueHandoff(ctx, c.UserID, c.SessionID, platformstore.NewToken{
		TeamID: t.ID, TokenHash: hash, ExpiresAt: now.Add(platformauth.HandoffLongTTL),
	}, now)
	if errors.Is(err, platformstore.ErrTokenInvalid) {
		return nil, status.Error(codes.Unauthenticated, "unauthorized")
	}
	if err != nil {
		return nil, storeErr(ctx, "operator hand-off", err)
	}
	s.audit(ctx, c.UserID, t.ID, "operator_enter", map[string]string{"slug": t.Slug})
	return &pb_admin.EnterTeamResponse{HandoffCode: code}, nil
}
