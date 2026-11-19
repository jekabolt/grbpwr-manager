package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// platformHandle is the control plane as the server sees it, and the auth server's control-plane
// side of team #1 credentials (teamOneCreds). Zero = teams mode off.
type platformHandle struct {
	store *platformstore.Store
	cfg   platformstore.Config
	// invalidateSession drops one revoked sid from the edge's session cache (team_members.go).
	invalidateSession func(sid string)
	teamOneCreds      TeamOneCredentials
}

// TeamOneCredentials is the auth server's control-plane side of team #1 admin credentials in teams
// mode (internal/apisrv/auth.Server): the platform user linked through team #1 membership is the
// authoritative password, the tenant admins row a mirror.
type TeamOneCredentials interface {
	// ResetTeamOnePassword: hash + credential generation + session revoke in the control plane,
	// then the tenant mirror. FAILED_PRECONDITION for an account with no linked platform user.
	ResetTeamOnePassword(ctx context.Context, username, hash string) error
	// LinkTeamOneAccount creates or links the control-plane account of a new team #1 admin;
	// ALREADY_EXISTS when the handle belongs to a platform user outside team #1.
	LinkTeamOneAccount(ctx context.Context, username, hash string, super bool) (int64, error)
}

// SetPlatform hands the server the control plane and the platform config (teams mode on). Not
// called with teams mode off.
func (s *Server) SetPlatform(st *platformstore.Store, cfg platformstore.Config) {
	// Keeps the session invalidator and team #1 credentials wired before it.
	s.platform.store, s.platform.cfg = st, cfg
}

// SetTeamOneCredentials wires team #1 account credentials through the control plane (teams mode
// on). Not called with teams mode off: the admins row stays the only password.
func (s *Server) SetTeamOneCredentials(c TeamOneCredentials) {
	s.platform.teamOneCreds = c
}

// teamOneCredentials is the control-plane credential path for a request on team #1 in teams mode;
// nil with teams mode off or on another team (whose members sign in with their platform password).
func (s *Server) teamOneCredentials(ctx context.Context) TeamOneCredentials {
	if s.platform.teamOneCreds == nil {
		return nil
	}
	if t, ok := tenant.From(ctx); ok && t.ID != tenant.TeamOneID {
		return nil
	}
	return s.platform.teamOneCreds
}
