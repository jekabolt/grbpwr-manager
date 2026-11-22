// Package platform implements PlatformService, the operator console of the control plane (teams
// §6). Every call is guarded by the auth interceptor: an aud=console token, a live session and an
// active operator; no team is bound. The methods arrive with B5; until then they answer
// Unimplemented.
package platform

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// Server is the PlatformService implementation.
type Server struct {
	pb_admin.UnimplementedPlatformServiceServer

	st *platformstore.Store
	// invalidate drops a team's cached directory row and feature switches after an operator
	// changes them (tenantbind.Binder.InvalidateTeam). nil = nothing to drop.
	invalidate func(teamID int64)
	// dropSession drops a revoked session from the edge's in-process cache
	// (tenantbind.Binder.InvalidateSession). nil = nothing to drop.
	dropSession func(sid string)
	// dropShop drops a team's cached shop answers after an operator closes or reopens it (X18).
	dropShop func(teamID int64)
	// checkPassword checks the calling operator's own console password under the sign-in limiters
	// (platformauth.Service.CheckOperatorPassword). nil = DeleteTeam answers UNAVAILABLE.
	checkPassword func(ctx context.Context, userID int64, password string) error
	// cfg is the platform config (team addresses); budgetZone the owner's budget zone; now the
	// clock (tests). Set by SetConfig / SetBudgetZone (teams.go).
	cfg        platformstore.Config
	budgetZone func() string
	now        func() time.Time
}

// New returns the server over the control plane.
func New(st *platformstore.Store) *Server {
	return &Server{st: st}
}

// SetInvalidator wires the team cache invalidation (called once at boot).
func (s *Server) SetInvalidator(fn func(teamID int64)) {
	s.invalidate = fn
}

// SetSessionInvalidator wires the edge's session cache drop (called once at boot).
func (s *Server) SetSessionInvalidator(fn func(sid string)) {
	s.dropSession = fn
}

// SetPasswordCheck wires the operator password check DeleteTeam confirms with (called once at boot).
func (s *Server) SetPasswordCheck(fn func(ctx context.Context, userID int64, password string) error) {
	s.checkPassword = fn
}
