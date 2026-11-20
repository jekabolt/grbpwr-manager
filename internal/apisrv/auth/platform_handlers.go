package auth

import (
	"context"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
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
	if strings.TrimSpace(req.GetJoinLink()) != "" {
		team, err := s.requestedJoinTeam(ctx)
		if err != nil {
			return nil, err
		}
		ctx = tenant.With(ctx, team)
	}
	return svc.SignUp(ctx, req)
}

func (s *Server) requestedJoinTeam(ctx context.Context) (tenant.Team, error) {
	if s.edge == nil {
		return tenant.Team{}, status.Error(codes.NotFound, "link_invalid")
	}
	team, named, err := s.edge.RequestedTeam(ctx, grpcHost(ctx), tenant.HeaderSlug(ctx))
	if err != nil || !named || team.ID <= 0 {
		return tenant.Team{}, status.Error(codes.NotFound, "link_invalid")
	}
	return team, nil
}

func (s *Server) JoinLinkInfo(ctx context.Context, req *auth.JoinLinkInfoRequest) (*auth.JoinLinkInfoResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	team, err := s.requestedJoinTeam(ctx)
	if err != nil {
		return nil, err
	}
	return svc.JoinLinkInfo(ctx, team.ID, req)
}

func (s *Server) JoinTeam(ctx context.Context, req *auth.JoinTeamRequest) (*auth.JoinTeamResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	team, err := s.requestedJoinTeam(ctx)
	if err != nil {
		return nil, err
	}
	return svc.JoinTeam(ctx, bearer(ctx), team.ID, req)
}

func (s *Server) ConfirmJoinCode(ctx context.Context, req *auth.ConfirmJoinCodeRequest) (*auth.ConfirmJoinCodeResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	team, err := s.requestedJoinTeam(ctx)
	if err != nil {
		return nil, err
	}
	return svc.ConfirmJoinCode(tenant.With(ctx, team), req)
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
	return svc.SignOut(ctx, bearer(ctx), req.GetDevice())
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

// The account RPCs of the calling person (team token or root token; no rbac entry, no feature gate).

func (s *Server) GetMyProfile(ctx context.Context, req *auth.GetMyProfileRequest) (*auth.GetMyProfileResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.GetMyProfile(ctx, bearer(ctx), req)
}

func (s *Server) UpdateMyProfile(ctx context.Context, req *auth.UpdateMyProfileRequest) (*auth.UpdateMyProfileResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.UpdateMyProfile(ctx, bearer(ctx), req)
}

func (s *Server) SetMyAvatar(ctx context.Context, req *auth.SetMyAvatarRequest) (*auth.SetMyAvatarResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.SetMyAvatar(ctx, bearer(ctx), req)
}

func (s *Server) ChangeMyPassword(ctx context.Context, req *auth.ChangeMyPasswordRequest) (*auth.ChangeMyPasswordResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.ChangeMyPassword(ctx, bearer(ctx), req)
}

func (s *Server) ListMySessions(ctx context.Context, req *auth.ListMySessionsRequest) (*auth.ListMySessionsResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.ListMySessions(ctx, bearer(ctx), req)
}

func (s *Server) RevokeMySession(ctx context.Context, req *auth.RevokeMySessionRequest) (*auth.RevokeMySessionResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.RevokeMySession(ctx, bearer(ctx), req)
}

func (s *Server) RevokeMyOtherSessions(ctx context.Context, req *auth.RevokeMyOtherSessionsRequest) (*auth.RevokeMyOtherSessionsResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.RevokeMyOtherSessions(ctx, bearer(ctx), req)
}

func (s *Server) StartGoogleLink(ctx context.Context, req *auth.StartGoogleLinkRequest) (*auth.StartGoogleLinkResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.StartGoogleLink(ctx, bearer(ctx), req)
}

func (s *Server) UnlinkMyGoogle(ctx context.Context, req *auth.UnlinkMyGoogleRequest) (*auth.UnlinkMyGoogleResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.UnlinkMyGoogle(ctx, bearer(ctx), req)
}

func (s *Server) DeleteMyAccount(ctx context.Context, req *auth.DeleteMyAccountRequest) (*auth.DeleteMyAccountResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.DeleteMyAccount(ctx, bearer(ctx), req)
}

func (s *Server) FinishTeamEntry(ctx context.Context, req *auth.FinishTeamEntryRequest) (*auth.FinishTeamEntryResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.FinishTeamEntry(ctx, bearer(ctx), req)
}

// The account restore RPCs are public (no session: the deleted account has none).

func (s *Server) RequestAccountRestore(ctx context.Context, req *auth.RequestAccountRestoreRequest) (*auth.RequestAccountRestoreResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.RequestAccountRestore(ctx, req)
}

func (s *Server) RestoreAccount(ctx context.Context, req *auth.RestoreAccountRequest) (*auth.RestoreAccountResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.RestoreAccount(ctx, req)
}

// The operator second-step RPCs are public: the ticket from SignIn is the credential.

func (s *Server) OperatorSetupMail(ctx context.Context, req *auth.OperatorSetupMailRequest) (*auth.OperatorSetupMailResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.OperatorSetupMail(ctx, req)
}

func (s *Server) OperatorSetupConfirm(ctx context.Context, req *auth.OperatorSetupConfirmRequest) (*auth.OperatorSetupConfirmResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.OperatorSetupConfirm(ctx, req)
}

func (s *Server) OperatorVerify(ctx context.Context, req *auth.OperatorVerifyRequest) (*auth.OperatorVerifyResponse, error) {
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	return svc.OperatorVerify(ctx, req)
}
