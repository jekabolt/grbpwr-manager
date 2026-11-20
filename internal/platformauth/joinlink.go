package platformauth

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) joinRequestLink(slug string) string {
	return "https://" + slug + "." + s.base + "/workspace#people"
}

func (s *Service) signUpForJoin(ctx context.Context, req *auth.SignUpRequest, email, link string) (*auth.SignUpResponse, error) {
	team, ok := tenant.From(ctx)
	if !ok || team.ID <= 0 {
		return nil, status.Error(codes.NotFound, "link_invalid")
	}
	if !req.GetTermsAccepted() {
		return nil, errTerms()
	}
	first, last, err := SplitFullName(req.GetName())
	if err != nil {
		return nil, errProfileName(err)
	}
	if !s.joinIP.Allow(middleware.GetClientIP(ctx)) {
		return nil, errJoinRateLimited()
	}
	if drop, err := s.guardSignUp(ctx, email); err != nil || drop {
		if err != nil {
			return nil, err
		}
		return &auth.SignUpResponse{}, nil
	}
	hash, err := s.d.Hasher.HashPassword(req.GetPassword())
	if err != nil {
		s.log(ctx, "hash join password", err)
		return nil, errInternal()
	}
	now := s.now()
	res, err := s.d.Store.JoinSignUp(ctx, platformstore.JoinSignUpParams{
		TeamID: team.ID, Link: link, Pepper: s.pepper, Email: email, PasswordHash: hash,
		HandleBase: HandleBase(email), FirstName: first, LastName: last, TermsVersion: s.d.TermsVersion,
		Now: now, MaxLettersPerHour: MaxLettersPerEmailHr,
		PasswordMatches: s.passwordMatcher(req.GetPassword()),
		Letter:          s.letter(PurposeVerifyJoin, email, VerifyTTL, now),
	})
	switch {
	case errors.Is(err, platformstore.ErrJoinLinkInvalid):
		return nil, status.Error(codes.NotFound, "link_invalid")
	case errors.Is(err, platformstore.ErrTeamReadOnly):
		return nil, status.Error(codes.FailedPrecondition, "team_read_only")
	case err != nil:
		s.log(ctx, "join sign-up", err)
		return nil, errInternal()
	}
	if res.Created {
		s.audit(ctx, res.UserID, res.TeamID, "signup", map[string]string{"purpose": "join"})
	} else {
		s.audit(ctx, 0, team.ID, "signup_ignored", map[string]string{"reason": res.Reason})
	}
	return &auth.SignUpResponse{}, nil
}

func (s *Service) JoinLinkInfo(ctx context.Context, teamID int64, req *auth.JoinLinkInfoRequest) (*auth.JoinLinkInfoResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	name, err := s.d.Store.JoinLinkInfo(ctx, teamID, req.GetLink(), s.pepper)
	if errors.Is(err, platformstore.ErrJoinLinkInvalid) {
		return nil, status.Error(codes.NotFound, "link_invalid")
	}
	if err != nil {
		s.log(ctx, "join link info", err)
		return nil, errInternal()
	}
	return &auth.JoinLinkInfoResponse{TeamName: name}, nil
}

func (s *Service) JoinTeam(ctx context.Context, rootToken string, teamID int64, req *auth.JoinTeamRequest) (*auth.JoinTeamResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	u, _, err := s.rootCaller(ctx, rootToken)
	if err != nil {
		return nil, err
	}
	if !s.joinIP.Allow(middleware.GetClientIP(ctx)) {
		return nil, errJoinRateLimited()
	}
	team, err := s.d.Store.Directory().ByID(ctx, teamID)
	if err != nil {
		return nil, status.Error(codes.NotFound, "link_invalid")
	}
	if s.schemaBehind(team) {
		return nil, errUpdating()
	}
	now := s.now()
	res, err := s.d.Store.JoinTeam(ctx, platformstore.JoinDecisionParams{
		TeamID: teamID, UserID: u.ID, Token: strings.TrimSpace(req.GetLink()), Pepper: s.pepper,
		IP: middleware.GetClientIP(ctx), Now: now, RequestLink: s.joinRequestLink(team.Slug),
		Stage: s.stageLinkJoin, Enable: s.enableLinkJoin,
	})
	if err != nil {
		return nil, s.joinError(ctx, "join team", err)
	}
	if res.EnablePending {
		slog.Default().WarnContext(ctx, "platformauth: link join completed, team row not enabled")
	}
	return &auth.JoinTeamResponse{State: string(res.State)}, nil
}

func (s *Service) ConfirmJoinCode(ctx context.Context, req *auth.ConfirmJoinCodeRequest) (*auth.ConfirmJoinCodeResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	email, code, err := s.codeInput(ctx, req.GetEmail(), req.GetCode())
	if err != nil {
		return nil, err
	}
	team, ok := tenant.From(ctx)
	if !ok || team.ID <= 0 {
		return nil, status.Error(codes.NotFound, "link_invalid")
	}
	now := s.now()
	res, err := s.d.Store.ConfirmJoinCode(ctx, platformstore.ConfirmJoinParams{
		Attempt: s.attempt(PurposeVerifyJoin, email, code, now), TeamID: team.ID,
		PasswordMatches: s.passwordMatcher(req.GetPassword()),
		IP:              middleware.GetClientIP(ctx), Now: now, RequestLink: s.joinRequestLink(team.Slug),
		Stage: s.stageLinkJoin, Enable: s.enableLinkJoin,
	})
	if errors.Is(err, platformstore.ErrTokenInvalid) || errors.Is(err, platformstore.ErrWrongPassword) {
		return nil, errCode()
	}
	if err != nil {
		return nil, s.joinError(ctx, "confirm join code", err)
	}
	if res.EnablePending {
		slog.Default().WarnContext(ctx, "platformauth: confirmed link join, team row not enabled")
	}
	return &auth.ConfirmJoinCodeResponse{State: string(res.State)}, nil
}

// errJoinRateLimited is the join path's per-IP refusal. The limit is by IP, so it says nothing about
// any address (unlike a silent success, which hid that no letter went out).
func errJoinRateLimited() error {
	return status.Error(codes.ResourceExhausted, "join_rate_limited")
}

func (s *Service) joinError(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, platformstore.ErrJoinLinkInvalid):
		return status.Error(codes.NotFound, "link_invalid")
	case errors.Is(err, platformstore.ErrTeamReadOnly):
		return status.Error(codes.FailedPrecondition, "team_read_only")
	case errors.Is(err, platformstore.ErrJoinConfirmEmail):
		return status.Error(codes.FailedPrecondition, "confirm_email")
	case errors.Is(err, platformstore.ErrJoinQueueFull):
		return status.Error(codes.ResourceExhausted, "join_queue_full")
	case errors.Is(err, platformstore.ErrJoinRateLimited):
		return errJoinRateLimited()
	case errors.Is(err, platformstore.ErrJoinOperator), errors.Is(err, platformstore.ErrJoinDisabled):
		return status.Error(codes.PermissionDenied, "no_access")
	}
	s.log(ctx, op, err)
	return errInternal()
}

func (s *Service) stageLinkJoin(ctx context.Context, m platformstore.JoinMember) error {
	super, perms, err := InvitePermissions(m.Permissions, m.Role)
	if err != nil {
		return err
	}
	team, err := s.d.Store.Directory().ByID(ctx, m.TeamID)
	if err != nil {
		return err
	}
	return s.d.Hooks.StageMember(ctx, team, m.Handle, super, perms)
}

func (s *Service) enableLinkJoin(ctx context.Context, m platformstore.JoinMember) (bool, error) {
	if _, ok, err := s.d.Store.Membership(ctx, m.UserID, m.TeamID); err != nil || !ok {
		return false, err
	}
	team, err := s.d.Store.Directory().ByID(ctx, m.TeamID)
	if err != nil {
		return false, err
	}
	return s.d.Hooks.EnableMember(ctx, team, m.Handle)
}
