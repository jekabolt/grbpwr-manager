package platformauth

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/teamlogo"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The root list (`your teams`), `new team` and the team logo (teams O §5, §7, §11, §12).

func errBadLogo() error  { return status.Error(codes.InvalidArgument, "bad_logo") }
func errTryAgain() error { return status.Error(codes.Aborted, "try_again") }
func errSlugTaken() error {
	return status.Error(codes.AlreadyExists, "address taken")
}

// rootCaller checks a root token: audience root, not legacy, its session live and opened less than
// RootSessionTTL ago (absolute: a root session is never renewed), the account active. Every refusal
// is the generic one.
func (s *Service) rootCaller(ctx context.Context, token string) (platformstore.User, string, error) {
	claims, err := jwt.VerifyTeamToken(s.d.JWT, token, &jwt.Expectations{Audience: jwt.AudRoot})
	if err != nil || claims.Legacy || claims.Audience != jwt.AudRoot || claims.SessionID == "" || claims.UserID <= 0 {
		return platformstore.User{}, "", errAuth()
	}
	live, err := s.d.Store.RootSessionLive(ctx, claims.SessionID, claims.UserID, s.now().Add(-RootSessionTTL))
	if err != nil {
		s.log(ctx, "root session", err)
		return platformstore.User{}, "", errInternal()
	}
	if !live {
		return platformstore.User{}, "", errAuth()
	}
	u, err := s.d.Store.UserByID(ctx, claims.UserID)
	if err != nil || u.Status != platformstore.UserActive {
		return platformstore.User{}, "", errAuth()
	}
	s.touch(ctx, claims.SessionID)
	return u, claims.SessionID, nil
}

// LogoURL is the public address of a team's logo with its version (?v=<sha prefix>); "" when the
// team has no logo.
func (s *Service) LogoURL(slug, sha string) string {
	return teamlogo.URL(s.d.Config.APIBase(), slug, sha)
}

// teamRefs is the list a root answer carries: review shown awaiting approval (not ready),
// provisioning not ready, suspended read-only, each with its role and logo.
func (s *Service) teamRefs(teams []platformstore.MemberTeam) []*auth.TeamRef {
	out := make([]*auth.TeamRef, 0, len(teams))
	for _, t := range teams {
		out = append(out, &auth.TeamRef{
			Slug:             t.Slug,
			Name:             t.Name,
			Role:             t.Role,
			ReadOnly:         t.Status == string(tenant.StatusSuspended),
			Ready:            t.Status != string(tenant.StatusProvisioning) && t.Status != string(tenant.StatusReview),
			AwaitingApproval: t.Status == string(tenant.StatusReview),
			LogoUrl:          s.LogoURL(t.Slug, t.LogoSHA),
		})
	}
	return out
}

// processLogo checks and fits an uploaded logo; empty bytes = none. At most a few images are
// decoded at once (a 4096-px image costs tens of MB while it is decoded).
func (s *Service) processLogo(ctx context.Context, raw []byte) (*platformstore.TeamLogo, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > teamlogo.MaxRawBytes {
		return nil, errBadLogo()
	}
	select {
	case s.logoSlots <- struct{}{}:
		defer func() { <-s.logoSlots }()
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	l, err := teamlogo.Process(raw)
	if err != nil {
		return nil, errBadLogo()
	}
	return &platformstore.TeamLogo{PNG: l.PNG, SHA256: l.SHA256}, nil
}

// ListMyTeams lists the root session's account's teams: review shown awaiting approval, deleted,
// pending and removed memberships absent; can_create_team is true for an active account (no cap on
// own teams); terms_needed while the account has not accepted the current terms.
func (s *Service) ListMyTeams(ctx context.Context, rootToken string, _ *auth.ListMyTeamsRequest) (*auth.ListMyTeamsResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	u, _, err := s.rootCaller(ctx, rootToken)
	if err != nil {
		return nil, err
	}
	my, err := s.d.Store.ListMyTeams(ctx, u.ID)
	if errors.Is(err, platformstore.ErrUserNotFound) {
		return nil, errAuth()
	}
	if err != nil {
		s.log(ctx, "list my teams", err)
		return nil, errInternal()
	}
	return &auth.ListMyTeamsResponse{
		Teams:         s.teamRefs(my.Teams),
		Email:         my.Email,
		CanCreateTeam: my.Status == platformstore.UserActive,
		TermsNeeded:   my.TermsVersion != s.d.TermsVersion,
		Name:          platformstore.DisplayName(my.FirstName, my.LastName),
	}, nil
}

// CreateTeam creates one more team owned by the root session's account. The team follows the
// verified sign-up's transition: review (and the operators' letters) while approval is on, else
// provisioning; no letter to the person. terms_accepted is required while terms are needed. The
// brand, when the request carries one, is written with the team (NormalizeTeamBrand: a bad value
// refuses the request with its key before anything is written).
func (s *Service) CreateTeam(ctx context.Context, rootToken string, req *auth.CreateTeamRequest) (*auth.CreateTeamResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	u, rootSID, err := s.rootCaller(ctx, rootToken)
	if err != nil {
		return nil, err
	}
	name, err := CleanTeamName(req.GetTeamName())
	if err != nil {
		return nil, errBad(err.Error())
	}
	slug := strings.ToLower(strings.TrimSpace(req.GetTeamSlug()))
	if err := tenant.ValidSlug(slug); err != nil {
		return nil, errBad(err.Error())
	}
	brand, err := creationBrand(req.GetBrand())
	if err != nil {
		return nil, err
	}
	if !s.createTeam.Allow("u:" + strconv.FormatInt(u.ID, 10)) {
		return nil, errBusy()
	}
	logo, err := s.processLogo(ctx, req.GetLogo())
	if err != nil {
		return nil, err
	}
	now := s.now()
	res, err := s.d.Store.CreateTeam(ctx, platformstore.CreateTeamParams{
		UserID: u.ID, RootSID: rootSID, RootNotBefore: now.Add(-RootSessionTTL),
		Name: name, Slug: slug, Now: now, Review: s.review(),
		TermsVersion: s.d.TermsVersion, TermsAccepted: req.GetTermsAccepted(), Logo: logo, Brand: brand,
	})
	switch {
	case errors.Is(err, platformstore.ErrSlugTaken):
		return nil, errSlugTaken()
	case errors.Is(err, platformstore.ErrTermsRequired):
		return nil, errTerms()
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return nil, errAuth()
	case errors.Is(err, platformstore.ErrTryAgain):
		return nil, errTryAgain()
	case err != nil:
		s.log(ctx, "create team", err)
		return nil, errInternal()
	}
	s.audit(ctx, u.ID, res.TeamID, "team_create", map[string]string{"slug": res.Slug, "status": res.Status})
	sha := ""
	if logo != nil {
		sha = logo.SHA256
	}
	return &auth.CreateTeamResponse{Team: &auth.TeamRef{
		Slug:             res.Slug,
		Name:             res.Name,
		Role:             "owner",
		Ready:            false,
		AwaitingApproval: res.Status == string(tenant.StatusReview),
		LogoUrl:          s.LogoURL(res.Slug, sha),
	}}, nil
}

// SetTeamLogo sets (non-empty bytes) or removes (empty) the logo of the calling team: a team token
// of an owner whose session and membership are live, on that team's host (x-team). A suspended
// team is read-only.
func (s *Service) SetTeamLogo(ctx context.Context, teamToken string, req *auth.SetTeamLogoRequest) (*auth.SetTeamLogoResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	claims, err := jwt.VerifyTeamToken(s.d.JWT, teamToken, &jwt.Expectations{Audience: jwt.AudTeam})
	if err != nil || claims.Legacy || claims.Audience != jwt.AudTeam || claims.SessionID == "" || claims.TeamID <= 0 {
		return nil, errAuth()
	}
	slug := tenant.HeaderSlug(ctx)
	if slug == "" {
		return nil, errNoAccess()
	}
	team, err := s.d.Store.Directory().BySlug(ctx, slug)
	if err != nil || team.ID != claims.TeamID {
		return nil, errNoAccess()
	}
	role, ok, err := s.d.Store.TeamSession(ctx, claims.SessionID, claims.UserID, claims.TeamID)
	if err != nil {
		s.log(ctx, "team session", err)
		return nil, errInternal()
	}
	if !ok {
		return nil, errAuth()
	}
	if role != "owner" {
		return nil, errNoAccess()
	}
	if team.Status == tenant.StatusSuspended {
		return nil, status.Error(codes.FailedPrecondition, "team is read-only")
	}
	if !s.logoSet.Allow("u:" + strconv.FormatInt(claims.UserID, 10)) {
		return nil, errBusy()
	}
	logo, err := s.processLogo(ctx, req.GetLogo())
	if err != nil {
		return nil, err
	}
	err = s.d.Store.SetTeamLogo(ctx, platformstore.SetTeamLogoParams{
		TeamID: claims.TeamID, UserID: claims.UserID, SID: claims.SessionID, Logo: logo,
	})
	switch {
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return nil, errAuth()
	case errors.Is(err, platformstore.ErrNotOwner):
		return nil, errNoAccess()
	case errors.Is(err, platformstore.ErrTeamReadOnly):
		return nil, status.Error(codes.FailedPrecondition, "team is read-only")
	case errors.Is(err, platformstore.ErrTeamNotFound), errors.Is(err, platformstore.ErrWrongTeamStatus):
		return nil, errNoAccess()
	case err != nil:
		s.log(ctx, "set team logo", err)
		return nil, errInternal()
	}
	if logo == nil {
		s.audit(ctx, claims.UserID, team.ID, "team_logo_remove", nil)
		return &auth.SetTeamLogoResponse{}, nil
	}
	s.audit(ctx, claims.UserID, team.ID, "team_logo_set", nil)
	return &auth.SetTeamLogoResponse{LogoUrl: s.LogoURL(team.Slug, logo.SHA256)}, nil
}
