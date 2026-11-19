package platformauth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Operator entry (teams §23): a console operator opens a team's workspace with no membership.
const (
	// OperatorCodePrefix marks an operator-entry hand-off code. A member's code is plain base64url
	// and never contains '.', so the two can never be confused; the prefix is part of the hashed code.
	OperatorCodePrefix = "op."
	// OperatorActor is the subject of an operator-entry token: writes in such a session carry it.
	OperatorActor = "lekalo"
	// OperatorTTL bounds an operator-entry token (and never exceeds the team token TTL).
	OperatorTTL = time.Hour
)

// NewOperatorCode returns a fresh operator-entry code and its stored hash.
func NewOperatorCode() (code, hash string, err error) {
	tok, _, err := NewToken()
	if err != nil {
		return "", "", err
	}
	code = OperatorCodePrefix + tok
	return code, HashToken(code), nil
}

// exchangeOperator consumes an operator-entry code on the team host and mints a team token with the
// op claim: super, subject OperatorActor, a short life. The edge re-checks the operator (active +
// is_operator) and the session on every call instead of a membership.
func (s *Service) exchangeOperator(ctx context.Context, team tenant.Team, code string) (*auth.ExchangeHandoffResponse, error) {
	dir := s.d.Store.Directory()
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return nil, errInternal()
	}
	now := s.now()
	res, err := s.d.Store.ExchangeOperatorHandoff(ctx, HashToken(code), team.ID, sid, now)
	switch {
	case errors.Is(err, platformstore.ErrTeamNotReady):
		dir.Invalidate(team.ID)
		return nil, errNotReady()
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return nil, errCode()
	case err != nil:
		s.log(ctx, "exchange operator hand-off", err)
		return nil, errInternal()
	}
	ttl := OperatorTTL
	if s.d.TeamTTL > 0 && s.d.TeamTTL < ttl {
		ttl = s.d.TeamTTL
	}
	tok, err := jwt.NewTeamTokenAt(s.d.JWT, ttl, jwt.TeamClaims{
		Handle: OperatorActor, UserID: res.User.ID, TeamID: team.ID, SessionID: sid,
		Audience: jwt.AudTeam, Super: true, Operator: true,
	}, now)
	if err != nil {
		s.log(ctx, "mint operator token", err)
		if rerr := s.d.Store.RevokeSession(ctx, sid, now); rerr != nil {
			s.log(ctx, "revoke session", rerr)
		}
		return nil, errInternal()
	}
	// platform_audit is the operator's log; the team has no view of it.
	s.audit(ctx, res.User.ID, team.ID, "operator_signin", map[string]string{"slug": team.Slug})
	return &auth.ExchangeHandoffResponse{Token: tok}, nil
}

func isOperatorCode(code string) bool { return strings.HasPrefix(code, OperatorCodePrefix) }
