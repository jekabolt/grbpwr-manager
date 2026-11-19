package platformauth

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Account profile RPCs of the calling person (P0 design §1.2, §3.1, §3.2, §3.4, §3.7). Contract stubs:
// each answers Unimplemented until its server lane lands (S2a profile + avatar; S2b moves the password and
// Google ones to me_password.go / me_google.go).

// errNotYet is the answer of an account RPC whose server lane has not landed.
var errNotYet = status.Error(codes.Unimplemented, "not implemented yet")

// GetMyProfile returns the calling person's own profile.
func (s *Service) GetMyProfile(ctx context.Context, token string, req *auth.GetMyProfileRequest) (*auth.GetMyProfileResponse, error) {
	return nil, errNotYet
}

// UpdateMyProfile sets first/last name and @username.
func (s *Service) UpdateMyProfile(ctx context.Context, token string, req *auth.UpdateMyProfileRequest) (*auth.UpdateMyProfileResponse, error) {
	return nil, errNotYet
}

// SetMyAvatar sets or removes the avatar.
func (s *Service) SetMyAvatar(ctx context.Context, token string, req *auth.SetMyAvatarRequest) (*auth.SetMyAvatarResponse, error) {
	return nil, errNotYet
}

// ChangeMyPassword changes or sets the password.
func (s *Service) ChangeMyPassword(ctx context.Context, token string, req *auth.ChangeMyPasswordRequest) (*auth.ChangeMyPasswordResponse, error) {
	return nil, errNotYet
}

// StartGoogleLink issues a single-use Google connect ticket.
func (s *Service) StartGoogleLink(ctx context.Context, token string, req *auth.StartGoogleLinkRequest) (*auth.StartGoogleLinkResponse, error) {
	return nil, errNotYet
}

// UnlinkMyGoogle disconnects Google.
func (s *Service) UnlinkMyGoogle(ctx context.Context, token string, req *auth.UnlinkMyGoogleRequest) (*auth.UnlinkMyGoogleResponse, error) {
	return nil, errNotYet
}
