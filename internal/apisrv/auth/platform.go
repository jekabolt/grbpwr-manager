package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	if st != nil {
		s.creds = st
	}
}

// credentialPlane is the control plane's password write (platformstore.Store in teams mode): the
// authoritative store for a team-1 admin who has a platform user. nil = teams mode off.
type credentialPlane interface {
	SetPasswordAndRevokeSessions(ctx context.Context, handle, hash string, now time.Time) ([]string, error)
}

// passwordMirrorAttempts bounds the retry of the tenant admins mirror after the control plane took
// the new hash.
const passwordMirrorAttempts = 3

// changePasswordTeams writes a new hash in teams mode. Control plane first (hash + revoke every
// session, one tx); then the tenant admins row (the legacy Login reads it) with a retry. It never
// returns nil while a sign-in path still accepts the old hash. ok=false: the user has no platform
// user, so the caller keeps the tenant-only path of mode OFF.
func (s *Server) changePasswordTeams(ctx context.Context, username, hash string) (ok bool, err error) {
	revoked, err := s.creds.SetPasswordAndRevokeSessions(ctx, username, hash, time.Now().UTC())
	if errors.Is(err, platformstore.ErrUserNotFound) {
		return false, nil
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "change password: control plane write failed, nothing changed",
			slog.String("username", username), slog.String("err", err.Error()))
		return true, status.Error(codes.Unavailable, "password not changed, try again")
	}
	if inv, ok := s.edge.(interface{ InvalidateSession(sid string) }); ok {
		for _, sid := range revoked {
			inv.InvalidateSession(sid)
		}
	}
	for attempt := 1; attempt <= passwordMirrorAttempts; attempt++ {
		if err = s.adminRepository.ChangePassword(ctx, username, hash); err == nil {
			s.InvalidateAdminAccount(username)
			return true, nil
		}
		if attempt == passwordMirrorAttempts {
			break
		}
		if werr := sleepCtx(ctx, time.Duration(attempt)*100*time.Millisecond); werr != nil {
			err = werr
			break
		}
	}
	// The control plane has the new hash; the legacy Login still takes the old one. Not success:
	// a retry of ChangePassword with the old password passes validation and finishes the mirror.
	slog.Default().ErrorContext(ctx, "change password: tenant mirror failed after retries",
		slog.String("username", username), slog.String("err", err.Error()))
	return true, status.Error(codes.Unavailable, "password change incomplete, try again")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
