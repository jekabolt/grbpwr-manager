// Package platform implements PlatformService, the operator console of the control plane (teams
// §6). Every call is guarded by the auth interceptor: an aud=console token, a live session and an
// active operator; no team is bound. The methods arrive with B5; until then they answer
// Unimplemented.
package platform

import (
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
}

// New returns the server over the control plane.
func New(st *platformstore.Store) *Server {
	return &Server{st: st}
}

// SetInvalidator wires the team cache invalidation (called once at boot).
func (s *Server) SetInvalidator(fn func(teamID int64)) {
	s.invalidate = fn
}
