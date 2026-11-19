package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// platformHandle is the control plane as the server sees it, and the identity service built on it
// (the platform RPCs). Zero = teams mode off.
type platformHandle struct {
	store *platformstore.Store
	cfg   platformstore.Config
	// svc is built once in SetPlatform; svcErr is why it could not be (the platform RPCs then
	// answer with it). StopRateLimiter closes svc.
	svc    *platformauth.Service
	svcErr error
}

// SetPlatform hands the server the control plane and the platform config (teams mode on) and builds
// the identity service on them. Not called with teams mode off. Call it after New, before serving.
func (s *Server) SetPlatform(st *platformstore.Store, cfg platformstore.Config) {
	s.platform = platformHandle{store: st, cfg: cfg}
	if st == nil {
		return
	}
	s.creds = st
	s.platform.svc, s.platform.svcErr = s.newPlatformService()
}

// credentialPlane is the control plane's password write (platformstore.Store in teams mode): the
// authoritative store for a team-1 admin who has a platform user. nil = teams mode off.
type credentialPlane interface {
	SetPasswordAndRevokeSessions(ctx context.Context, handle, hash string, verify func(current string) error, now time.Time) ([]string, error)
}

// passwordMirrorAttempts bounds the retry of the tenant admins mirror after the control plane took
// the new hash.
const passwordMirrorAttempts = 3

// changePasswordTeams writes a new hash in teams mode. The control plane is the only judge of the
// current password: its row is locked, the current password (or the master password) is checked
// against its hash, then hash + revoke every session, one tx. The tenant admins hash is a mirror
// that may lag after a failed retry, so it never validates here. Then the tenant admins row (the
// legacy Login reads it) with a retry. It never returns nil while a sign-in path still accepts the
// old hash. ok=false: the user has no platform user, so the caller keeps the tenant-only path of
// mode OFF.
func (s *Server) changePasswordTeams(ctx context.Context, username, current, hash string) (ok bool, err error) {
	verify := func(stored string) error {
		if s.pwhash.Validate(current, s.masterHash) == nil {
			return nil
		}
		return s.pwhash.Validate(current, stored)
	}
	revoked, err := s.creds.SetPasswordAndRevokeSessions(ctx, username, hash, verify, time.Now().UTC())
	if errors.Is(err, platformstore.ErrUserNotFound) {
		return false, nil
	}
	if errors.Is(err, platformstore.ErrPasswordMismatch) {
		slog.Default().ErrorContext(ctx, "failed to validate current password",
			slog.String("err", err.Error()))
		return true, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "change password: control plane write failed, nothing changed",
			slog.String("username", username), slog.String("err", err.Error()))
		return true, status.Error(codes.Unavailable, "password not changed, try again")
	}
	s.dropRevokedCredentials(ctx, username, revoked)
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
	// a retry of ChangePassword with the NEW password (the control plane's) finishes the mirror.
	slog.Default().ErrorContext(ctx, "change password: tenant mirror failed after retries",
		slog.String("username", username), slog.String("err", err.Error()))
	return true, status.Error(codes.Unavailable, "password change incomplete, retry with the new password")
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
