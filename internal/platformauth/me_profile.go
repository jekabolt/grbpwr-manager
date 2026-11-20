package platformauth

import (
	"context"
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/avatar"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/teamlogo"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Account profile RPCs of the calling person (P0 design §1.2, §3.1, §3.2). The password RPC is in
// me_password.go, the Google ones in me_google.go, the devices in me_sessions.go.

// errNotYet is the answer of an account RPC whose server lane has not landed.
func errNotYet() error { return status.Error(codes.Unimplemented, "not implemented yet") }

// Profile answers (P0 §1.2). Built per call: the gate counts package-level values.
func errBadUsernameStatus() error { return status.Error(codes.InvalidArgument, "bad_username") }
func errUsernameTaken() error     { return status.Error(codes.AlreadyExists, "username_taken") }
func errBadAvatar() error         { return status.Error(codes.InvalidArgument, "bad_avatar") }

// errProfileName maps a name rule's refusal to its answer key (name_too_long or bad_name).
func errProfileName(err error) error {
	if errors.Is(err, errNameTooLong) {
		return status.Error(codes.InvalidArgument, "name_too_long")
	}
	return status.Error(codes.InvalidArgument, "bad_name")
}

// AvatarURL is the public address of the avatar with this sha256; "" when there is none.
func (s *Service) AvatarURL(sha string) string { return avatar.URL(s.d.Config.APIBase(), sha) }

func (s *Service) profilePB(p platformstore.Profile) *auth.MyProfile {
	out := &auth.MyProfile{
		FirstName:      p.FirstName,
		LastName:       p.LastName,
		PublicUsername: p.PublicUsername,
		Email:          p.Email,
		AvatarUrl:      s.AvatarURL(p.AvatarSHA),
		// false for an account made or claimed through a provider: ChangeMyPassword then sets one.
		PasswordSet: p.PasswordSet,
		GoogleEmail: p.GoogleEmail,
		// SoleOwnerTeams and ClosingTeams come with delete account (lane S4b).
	}
	if p.PasswordChangedAt.Valid {
		out.PasswordChangedAt = timestamppb.New(p.PasswordChangedAt.Time)
	}
	return out
}

// profile reads the caller's profile; a missing or inactive account is errAuth.
func (s *Service) profile(ctx context.Context, userID int64) (*auth.MyProfile, error) {
	p, err := s.d.Store.Profile(ctx, userID)
	if errors.Is(err, platformstore.ErrUserNotFound) {
		return nil, errAuth()
	}
	if err != nil {
		s.log(ctx, "read profile", err)
		return nil, errInternal()
	}
	return s.profilePB(p), nil
}

// GetMyProfile returns the calling person's own profile (team token of any role, or root token).
func (s *Service) GetMyProfile(ctx context.Context, token string, _ *auth.GetMyProfileRequest) (*auth.GetMyProfileResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if !s.profileRead.Allow(c.key()) {
		return nil, errBusy()
	}
	p, err := s.profile(ctx, c.UserID)
	if err != nil {
		return nil, err
	}
	return &auth.GetMyProfileResponse{Profile: p}, nil
}

// UpdateMyProfile sets first and last name (each trimmed, at most 60 characters: name_too_long) and
// the @username (bad_username, username_taken; the own handle clears the choice, empty keeps it).
func (s *Service) UpdateMyProfile(ctx context.Context, token string, req *auth.UpdateMyProfileRequest) (*auth.UpdateMyProfileResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	first, err := CleanPersonName(req.GetFirstName())
	if err != nil {
		return nil, errProfileName(err)
	}
	last, err := CleanPersonName(req.GetLastName())
	if err != nil {
		return nil, errProfileName(err)
	}
	var username string
	if raw := req.GetPublicUsername(); raw != "" {
		if username, err = NormalizePublicUsername(raw); err != nil {
			return nil, errBadUsernameStatus()
		}
	}
	if !s.profileWrite.Allow(c.key()) {
		return nil, errBusy()
	}
	err = s.d.Store.UpdateProfile(ctx, platformstore.UpdateProfileParams{
		UserID: c.UserID, FirstName: first, LastName: last, PublicUsername: username,
	})
	switch {
	case errors.Is(err, platformstore.ErrUsernameTaken):
		return nil, errUsernameTaken()
	case errors.Is(err, platformstore.ErrUserNotFound):
		return nil, errAuth()
	case errors.Is(err, platformstore.ErrTryAgain):
		return nil, errTryAgain()
	case err != nil:
		s.log(ctx, "update profile", err)
		return nil, errInternal()
	}
	s.audit(ctx, c.UserID, c.TeamID, "profile_update", nil)
	p, err := s.profile(ctx, c.UserID)
	if err != nil {
		return nil, err
	}
	return &auth.UpdateMyProfileResponse{Profile: p}, nil
}

// processAvatar checks and fits a photo as a team logo is (teamlogo.Process, sharing its decode
// slots); nil for empty bytes (a removal), bad_avatar for anything it refuses.
func (s *Service) processAvatar(ctx context.Context, raw []byte) (*platformstore.Avatar, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > teamlogo.MaxRawBytes {
		return nil, errBadAvatar()
	}
	select {
	case s.logoSlots <- struct{}{}:
		defer func() { <-s.logoSlots }()
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	l, err := teamlogo.Process(raw)
	if err != nil {
		return nil, errBadAvatar()
	}
	return &platformstore.Avatar{PNG: l.PNG, SHA256: l.SHA256}, nil
}

// SetMyAvatar sets the avatar (PNG or JPEG up to 1 MB, fitted into 256x256; bad_avatar otherwise) or
// removes it (empty bytes; its old URL then answers 404). One avatar per account, in every team.
func (s *Service) SetMyAvatar(ctx context.Context, token string, req *auth.SetMyAvatarRequest) (*auth.SetMyAvatarResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if !s.avatarSet.Allow(c.key()) {
		return nil, errBusy()
	}
	a, err := s.processAvatar(ctx, req.GetAvatar())
	if err != nil {
		return nil, err
	}
	err = s.d.Store.SetAvatar(ctx, c.UserID, a)
	switch {
	case errors.Is(err, platformstore.ErrUserNotFound):
		return nil, errAuth()
	case err != nil:
		s.log(ctx, "set avatar", err)
		return nil, errInternal()
	}
	if a == nil {
		s.audit(ctx, c.UserID, c.TeamID, "avatar_remove", nil)
		return &auth.SetMyAvatarResponse{}, nil
	}
	s.audit(ctx, c.UserID, c.TeamID, "avatar_set", nil)
	return &auth.SetMyAvatarResponse{AvatarUrl: s.AvatarURL(a.SHA256)}, nil
}
