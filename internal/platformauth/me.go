package platformauth

import (
	"context"
	"errors"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Limits of the account RPCs (P0 §1.4: a per-account limiter on each).
const (
	// MaxProfileReadsPerMin bounds GetMyProfile per account.
	MaxProfileReadsPerMin = 60
	// MaxProfileWritesPerHr bounds UpdateMyProfile and FinishTeamEntry per account.
	MaxProfileWritesPerHr = 60
	// MaxAvatarSetsPerHr bounds SetMyAvatar per account (decoding shares the logo slots).
	MaxAvatarSetsPerHr = 30
)

// caller is the person an account RPC acts for (P0 §1.4).
type caller struct {
	UserID int64
	TeamID int64 // 0 = a root token
	SID    string
	// Chain is the device chain: the root session the team session was opened from, else the session.
	Chain string
}

func (c caller) key() string { return "u:" + strconv.FormatInt(c.UserID, 10) }

// errSignInAgain refuses a token the account RPCs never take (legacy, console, operator entry).
func errSignInAgain() error { return status.Error(codes.Unauthenticated, "sign_in_again") }

// me resolves the caller of an account RPC (P0 §1.4). A team token: the X-Team slug names the token's
// team (any status but deleted, so a paused team's member still edits their own account), and its
// session is live with an active account and an active membership of any role. A root token: its
// session is live within RootSessionTTL and the account active. Console tokens, operator-entry team
// tokens and legacy tokens without a session are sign_in_again. No rbac and no feature gate: the
// account is not team data.
func (s *Service) me(ctx context.Context, token string) (caller, error) {
	if err := s.usable(); err != nil {
		return caller{}, err
	}
	claims, err := jwt.VerifyTeamToken(s.d.JWT, token, nil)
	if err != nil {
		return caller{}, errAuth()
	}
	if claims.Legacy || claims.Operator || claims.SessionID == "" || claims.UserID <= 0 {
		return caller{}, errSignInAgain()
	}
	c := caller{UserID: claims.UserID, SID: claims.SessionID}
	switch claims.Audience {
	case jwt.AudTeam:
		if claims.TeamID <= 0 {
			return caller{}, errAuth()
		}
		slug := tenant.HeaderSlug(ctx)
		if slug == "" {
			return caller{}, errNoAccess()
		}
		team, err := s.d.Store.Directory().BySlug(ctx, slug)
		if err != nil || team.ID != claims.TeamID {
			return caller{}, errNoAccess()
		}
		chain, ok, err := s.d.Store.TeamSessionChain(ctx, claims.SessionID, claims.UserID, claims.TeamID)
		if err != nil {
			s.log(ctx, "team session", err)
			return caller{}, errInternal()
		}
		if !ok {
			return caller{}, errAuth()
		}
		c.TeamID, c.Chain = team.ID, chain
	case jwt.AudRoot:
		live, err := s.d.Store.RootSessionLive(ctx, claims.SessionID, claims.UserID, s.now().Add(-RootSessionTTL))
		if err != nil {
			s.log(ctx, "root session", err)
			return caller{}, errInternal()
		}
		if !live {
			return caller{}, errAuth()
		}
		u, err := s.d.Store.UserByID(ctx, claims.UserID)
		if errors.Is(err, platformstore.ErrUserNotFound) || (err == nil && u.Status != platformstore.UserActive) {
			return caller{}, errAuth()
		}
		if err != nil {
			s.log(ctx, "root user", err)
			return caller{}, errInternal()
		}
		c.Chain = claims.SessionID
	default:
		return caller{}, errSignInAgain()
	}
	s.touch(ctx, c.SID)
	return c, nil
}
