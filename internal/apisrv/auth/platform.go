package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
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

// credentialPlane is the control plane's side of team #1 admin credentials (platformstore.Store in
// teams mode): authoritative for every team #1 admin linked to a platform user through team #1
// membership. nil = teams mode off (no control-plane call anywhere).
type credentialPlane interface {
	SetPasswordAndRevokeSessions(ctx context.Context, handle, hash string, verify func(current string) error, now time.Time) (platformstore.CredentialChange, error)
	TeamOneCredentials(ctx context.Context, handle string) (platformstore.TeamOneCred, bool, error)
	LinkTeamOneAdmin(ctx context.Context, handle, hash string, super bool, now time.Time) (platformstore.CredentialChange, error)
}

// passwordMirrorAttempts bounds the retry of the tenant admins mirror after the control plane took
// the new hash.
const passwordMirrorAttempts = 3

// errNotLinked: a team #1 admin with no platform user linked through team #1 (its handle belongs to
// someone else on the platform). Its password cannot change in teams mode: the change could not
// reach its old tokens.
func errNotLinked() error {
	return status.Error(codes.FailedPrecondition, "account_not_linked")
}

// legacyCredentials is what the old /api/auth/login checks in teams mode: a linked team #1 admin's
// control-plane hash and generation (the tenant admins row is only a mirror and may lag); an
// unlinked one keeps the tenant hash and generation 0. Mode off: the tenant hash, no lookup.
func (s *Server) legacyCredentials(ctx context.Context, username, tenantHash string) (hash string, gen int64, err error) {
	if s.creds == nil {
		return tenantHash, 0, nil
	}
	c, linked, err := s.creds.TeamOneCredentials(ctx, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "login: control plane lookup failed", slog.String("err", err.Error()))
		return "", 0, status.Error(codes.Unavailable, "sign-in unavailable, try again")
	}
	if !linked {
		return tenantHash, 0, nil
	}
	if c.Disabled {
		slog.Default().WarnContext(ctx, "login attempt for an account disabled on the platform",
			slog.String("username", username))
		return "", 0, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	return c.Hash, c.Gen, nil
}

// issueOpts is jwtIssueOpts plus the credential generation of a legacy team #1 token (claim cg,
// written only above 0, so mode off mints the same token as before).
func (s *Server) issueOpts(gen int64) *jwt.IssueOpts {
	o := s.jwtIssueOpts()
	if gen <= 0 {
		return o
	}
	if o == nil {
		return &jwt.IssueOpts{CredGen: gen}
	}
	c := *o
	c.CredGen = gen
	return &c
}

// writeTeamOnePassword is the one password write of a team #1 admin in teams mode. The control
// plane's row (found through team #1 membership, never by bare handle) is locked; verify (nil = an
// admin resetting another account) checks the current password or the master password against
// ITS hash; then hash, credential_gen + 1, sessions revoked, hand-offs expired, one tx. The edge
// drops the revoked sessions and the cached generation; then the tenant admins mirror with a retry.
// The mirror is no longer a way in (Login checks the control plane), so a mirror that keeps failing
// is logged, not an error. Returns the new generation: a token handed back carries it.
func (s *Server) writeTeamOnePassword(ctx context.Context, username, hash string, verify func(string) error) (int64, error) {
	c, err := s.creds.SetPasswordAndRevokeSessions(ctx, username, hash, verify, time.Now().UTC())
	if errors.Is(err, platformstore.ErrUserNotFound) {
		slog.Default().WarnContext(ctx, "password change refused: team #1 account has no linked platform user",
			slog.String("username", username))
		return 0, errNotLinked()
	}
	if errors.Is(err, platformstore.ErrPasswordMismatch) {
		slog.Default().ErrorContext(ctx, "failed to validate current password",
			slog.String("err", err.Error()))
		return 0, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "change password: control plane write failed, nothing changed",
			slog.String("username", username), slog.String("err", err.Error()))
		return 0, status.Error(codes.Unavailable, "password not changed, try again")
	}
	s.dropRevokedCredentials(ctx, username, c.SIDs)
	s.mirrorTeamOnePassword(ctx, username, hash)
	return c.Gen, nil
}

// mirrorTeamOnePassword copies a committed hash onto the tenant admins row (3 attempts).
func (s *Server) mirrorTeamOnePassword(ctx context.Context, username, hash string) {
	var err error
	for attempt := 1; attempt <= passwordMirrorAttempts; attempt++ {
		if err = s.adminRepository.ChangePassword(ctx, username, hash); err == nil {
			s.InvalidateAdminAccount(username)
			return
		}
		if attempt == passwordMirrorAttempts {
			break
		}
		if werr := sleepCtx(ctx, time.Duration(attempt)*100*time.Millisecond); werr != nil {
			err = werr
			break
		}
	}
	s.InvalidateAdminAccount(username)
	slog.Default().ErrorContext(ctx, "change password: tenant mirror failed after retries; the control plane has the new hash",
		slog.String("username", username), slog.String("err", err.Error()))
}

// changePasswordTeams is ChangePassword in teams mode: the current password (or the master
// password) is judged by the control plane only.
func (s *Server) changePasswordTeams(ctx context.Context, username, current, hash string) (int64, error) {
	return s.writeTeamOnePassword(ctx, username, hash, func(stored string) error {
		if s.pwhash.Validate(current, s.masterHash) == nil {
			return nil
		}
		return s.pwhash.Validate(current, stored)
	})
}

// ResetTeamOnePassword is an admin resetting another team #1 account's password in teams mode
// (AdminService.ResetAccountPassword; the caller checked the accounts permission): the same
// control-plane write as a change, without a current password. Only wired with teams mode on.
func (s *Server) ResetTeamOnePassword(ctx context.Context, username, hash string) error {
	if s.creds == nil {
		return status.Error(codes.FailedPrecondition, "teams mode is off")
	}
	_, err := s.writeTeamOnePassword(ctx, strings.ToLower(username), hash, nil)
	return err
}

// LinkTeamOneAccount gives a team #1 admin created in teams mode its control-plane account before
// the tenant row is written (AdminService.CreateAccount, the master-password bootstrap): a free
// handle gets a new platform user in team #1; a handle already linked to team #1 gets the new
// password like a reset (old sessions and tokens die); a handle that belongs to a platform user
// outside team #1 is refused with ALREADY_EXISTS and nothing changes. Returns the generation.
func (s *Server) LinkTeamOneAccount(ctx context.Context, username, hash string, super bool) (int64, error) {
	if s.creds == nil {
		return 0, status.Error(codes.FailedPrecondition, "teams mode is off")
	}
	username = strings.ToLower(username)
	c, err := s.creds.LinkTeamOneAdmin(ctx, username, hash, super, time.Now().UTC())
	if errors.Is(err, platformstore.ErrHandleTaken) {
		slog.Default().WarnContext(ctx, "team #1 account refused: the handle belongs to a platform user outside team #1",
			slog.String("username", username))
		return 0, status.Error(codes.AlreadyExists, "username_taken")
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "team #1 account: control plane write failed",
			slog.String("username", username), slog.String("err", err.Error()))
		return 0, status.Error(codes.Unavailable, "account not created, try again")
	}
	s.dropRevokedCredentials(ctx, username, c.SIDs)
	return c.Gen, nil
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
