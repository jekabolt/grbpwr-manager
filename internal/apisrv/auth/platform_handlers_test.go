package auth

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// With teams mode off (no control plane) every platform RPC answers Unimplemented, as before.
func TestPlatformRPCsOffWithoutControlPlane(t *testing.T) {
	s := &Server{}
	ctx := context.Background()
	calls := map[string]func() error{
		"SignUp":               func() error { _, err := s.SignUp(ctx, &auth.SignUpRequest{}); return err },
		"CheckTeamSlug":        func() error { _, err := s.CheckTeamSlug(ctx, &auth.CheckTeamSlugRequest{}); return err },
		"VerifyEmail":          func() error { _, err := s.VerifyEmail(ctx, &auth.VerifyEmailRequest{}); return err },
		"ResendVerification":   func() error { _, err := s.ResendVerification(ctx, &auth.ResendVerificationRequest{}); return err },
		"SignIn":               func() error { _, err := s.SignIn(ctx, &auth.SignInRequest{}); return err },
		"IssueHandoff":         func() error { _, err := s.IssueHandoff(ctx, &auth.IssueHandoffRequest{}); return err },
		"ExchangeHandoff":      func() error { _, err := s.ExchangeHandoff(ctx, &auth.ExchangeHandoffRequest{}); return err },
		"SignOut":              func() error { _, err := s.SignOut(ctx, &auth.SignOutRequest{}); return err },
		"RequestPasswordReset": func() error { _, err := s.RequestPasswordReset(ctx, &auth.RequestPasswordResetRequest{}); return err },
		"ResetPassword":        func() error { _, err := s.ResetPassword(ctx, &auth.ResetPasswordRequest{}); return err },
		"GetInvite":            func() error { _, err := s.GetInvite(ctx, &auth.GetInviteRequest{}); return err },
		"AcceptInvite":         func() error { _, err := s.AcceptInvite(ctx, &auth.AcceptInviteRequest{}); return err },
		"ListMyTeams":          func() error { _, err := s.ListMyTeams(ctx, &auth.ListMyTeamsRequest{}); return err },
		"CreateTeam":           func() error { _, err := s.CreateTeam(ctx, &auth.CreateTeamRequest{}); return err },
		"GetSignInOptions":     func() error { _, err := s.GetSignInOptions(ctx, &auth.GetSignInOptionsRequest{}); return err },
		"ExchangeSignInCode":   func() error { _, err := s.ExchangeSignInCode(ctx, &auth.ExchangeSignInCodeRequest{}); return err },
		"SetTeamLogo":          func() error { _, err := s.SetTeamLogo(ctx, &auth.SetTeamLogoRequest{}); return err },
	}
	for name, call := range calls {
		if c := status.Code(call()); c != codes.Unimplemented {
			t.Errorf("%s with teams mode off = %v, want Unimplemented", name, c)
		}
	}
}
