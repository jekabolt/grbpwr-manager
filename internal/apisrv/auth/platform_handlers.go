package auth

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The platform identity RPCs (teams mode). With teams mode off they answer Unimplemented, exactly as
// the embedded UnimplementedAuthServiceServer did before; the legacy Login is untouched either way.

func (s *Server) identity() (*platformauth.Service, error) {
	svc, err := s.platformService()
	if err != nil {
		return nil, status.Error(codes.Unavailable, "sign-up is not available")
	}
	if svc == nil {
		return nil, status.Error(codes.Unimplemented, "teams mode is off")
	}
	return svc, nil
}

// bearer is the calling token; "" when the header is missing.
func bearer(ctx context.Context) string {
	t, err := GetTokenMetadata(ctx)
	if err != nil {
		return ""
	}
	return t
}

func (s *Server) SignUp(ctx context.Context, req *auth.SignUpRequest) (*auth.SignUpResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.SignUp(ctx, req)
}

func (s *Server) CheckTeamSlug(ctx context.Context, req *auth.CheckTeamSlugRequest) (*auth.CheckTeamSlugResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.CheckTeamSlug(ctx, req)
}

func (s *Server) VerifyEmail(ctx context.Context, req *auth.VerifyEmailRequest) (*auth.VerifyEmailResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.VerifyEmail(ctx, req)
}

func (s *Server) ResendVerification(ctx context.Context, req *auth.ResendVerificationRequest) (*auth.ResendVerificationResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.ResendVerification(ctx, req)
}

func (s *Server) SignIn(ctx context.Context, req *auth.SignInRequest) (*auth.SignInResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.SignIn(ctx, req)
}

func (s *Server) IssueHandoff(ctx context.Context, req *auth.IssueHandoffRequest) (*auth.IssueHandoffResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.IssueHandoff(ctx, bearer(ctx), req)
}

func (s *Server) ExchangeHandoff(ctx context.Context, req *auth.ExchangeHandoffRequest) (*auth.ExchangeHandoffResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.ExchangeHandoff(ctx, req)
}

func (s *Server) SignOut(ctx context.Context, req *auth.SignOutRequest) (*auth.SignOutResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.SignOut(ctx, bearer(ctx))
}

func (s *Server) RequestPasswordReset(ctx context.Context, req *auth.RequestPasswordResetRequest) (*auth.RequestPasswordResetResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.RequestPasswordReset(ctx, req)
}

func (s *Server) ResetPassword(ctx context.Context, req *auth.ResetPasswordRequest) (*auth.ResetPasswordResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.ResetPassword(ctx, req)
}

func (s *Server) GetInvite(ctx context.Context, req *auth.GetInviteRequest) (*auth.GetInviteResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.GetInvite(ctx, req)
}

func (s *Server) AcceptInvite(ctx context.Context, req *auth.AcceptInviteRequest) (*auth.AcceptInviteResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.AcceptInvite(ctx, req)
}

func (s *Server) ListMyTeams(ctx context.Context, req *auth.ListMyTeamsRequest) (*auth.ListMyTeamsResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.ListMyTeams(ctx, bearer(ctx), req)
}

func (s *Server) CreateTeam(ctx context.Context, req *auth.CreateTeamRequest) (*auth.CreateTeamResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.CreateTeam(ctx, bearer(ctx), req)
}

func (s *Server) GetSignInOptions(ctx context.Context, req *auth.GetSignInOptionsRequest) (*auth.GetSignInOptionsResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.GetSignInOptions(ctx, req)
}

func (s *Server) ExchangeSignInCode(ctx context.Context, req *auth.ExchangeSignInCodeRequest) (*auth.ExchangeSignInCodeResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.ExchangeSignInCode(ctx, req)
}

func (s *Server) SetTeamLogo(ctx context.Context, req *auth.SetTeamLogoRequest) (*auth.SetTeamLogoResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.SetTeamLogo(ctx, bearer(ctx), req)
}
