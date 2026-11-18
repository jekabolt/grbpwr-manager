package auth

import (
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// platformHandle is the control plane as the server sees it. Wave 1 only stores it; nothing reads
// it yet (wave 2 wires sign-in, membership and features through it).
type platformHandle struct {
	store *platformstore.Store
	cfg   platformstore.Config
}

// SetPlatform hands the server the control plane and the platform config (teams mode on). Not
// called with teams mode off.
func (s *Server) SetPlatform(st *platformstore.Store, cfg platformstore.Config) {
	s.platform = platformHandle{store: st, cfg: cfg}
}
