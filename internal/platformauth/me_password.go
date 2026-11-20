package platformauth

import (
	"context"
	"errors"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Change or set the password from the profile (P0 §3.4). «forgot password» is RequestPasswordReset
// with the profile email: no code of its own.

// MaxPasswordChangesPerHr bounds ChangeMyPassword per account (each try runs PBKDF2 once or twice).
const MaxPasswordChangesPerHr = 10

// Password answers. Built per call: the gate counts package-level values.
func errWrongPassword() error   { return status.Error(codes.InvalidArgument, "wrong_password") }
func errWeakPassword() error    { return status.Error(codes.InvalidArgument, "weak_password") }
func errTooManyAttempts() error { return status.Error(codes.ResourceExhausted, "too_many_attempts") }

// errPasswordMoved: the stored password changed between the check and the write.
var errPasswordMoved = errors.New("platformauth: password changed meanwhile")

// ChangeMyPassword changes the password (current_password checked) or, on an account without one
// (password_set false: made through Google), sets it (current_password empty). One transaction writes
// the hash, password_changed_at and a new credential generation (legacy tokens, sign-ins and codes in
// flight die); the caller's device stays signed in, and sign_out_others ends every other device.
// signed_out counts the live devices ended.
func (s *Service) ChangeMyPassword(ctx context.Context, token string, req *auth.ChangeMyPasswordRequest) (*auth.ChangeMyPasswordResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if CheckPassword(req.GetNewPassword()) != nil {
		return nil, errWeakPassword()
	}
	current := req.GetCurrentPassword()
	if len(current) > MaxPasswordLen {
		return nil, errWrongPassword()
	}
	// PBKDF2 is the expensive step: an account bucket and the sign-in IP bucket come first.
	if !s.passwordChange.Allow(c.key()) || !s.signInIP.Allow(middleware.GetClientIP(ctx)) {
		return nil, errTooManyAttempts()
	}
	p, err := s.d.Store.Profile(ctx, c.UserID)
	if errors.Is(err, platformstore.ErrUserNotFound) {
		return nil, errAuth()
	}
	if err != nil {
		s.log(ctx, "read profile", err)
		return nil, errInternal()
	}
	if p.PasswordSet {
		if !s.passwordOK(current, p.PasswordHash) {
			s.audit(ctx, c.UserID, c.TeamID, "password_change_failed", nil)
			return nil, errWrongPassword()
		}
	} else if current != "" {
		return nil, errWrongPassword()
	}
	hash, err := s.d.Hasher.HashPassword(req.GetNewPassword())
	if err != nil {
		s.log(ctx, "hash password", err)
		return nil, errInternal()
	}
	now := s.now()
	res, err := s.d.Store.ChangePassword(ctx, platformstore.PasswordChange{
		UserID:        c.UserID,
		Hash:          hash,
		KeepChain:     c.Chain,
		SignOutOthers: req.GetSignOutOthers(),
		Live:          s.liveness(now),
		// The row lock makes the check above binding: the hash and the «has a password» answer it
		// was made against must still be the stored ones.
		Verify: func(stored string, set bool) error {
			if stored != p.PasswordHash || set != p.PasswordSet {
				return errPasswordMoved
			}
			return nil
		},
		Now: now,
	})
	switch {
	case errors.Is(err, platformstore.ErrPasswordMismatch):
		return nil, errWrongPassword()
	case errors.Is(err, platformstore.ErrUserNotFound):
		return nil, errAuth()
	case err != nil:
		s.log(ctx, "change password", err)
		return nil, errInternal()
	}
	if s.d.Hooks.PasswordChanged != nil {
		s.d.Hooks.PasswordChanged(ctx, res.Handle, res.SIDs)
	}
	if res.TeamOne && s.d.Hooks.TeamOnePassword != nil {
		if err := s.d.Hooks.TeamOnePassword(ctx, res.Handle, hash); err != nil {
			// The platform password is set; the old team #1 Login keeps the old one until fixed.
			s.log(ctx, "team #1 password", err)
		}
	}
	action := "password_change"
	if !p.PasswordSet {
		action = "password_set"
	}
	s.audit(ctx, c.UserID, c.TeamID, action, map[string]string{"signed_out": strconv.Itoa(res.Devices)})
	return &auth.ChangeMyPasswordResponse{PasswordChangedAt: timestamppb.New(now), SignedOut: int32(res.Devices)}, nil
}
