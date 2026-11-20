package auth

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// With teams mode off (no control plane) every platform RPC answers Unimplemented, as before.
func TestPlatformRPCsOffWithoutControlPlane(t *testing.T) {
	s := &Server{}
	ctx := context.Background()
	calls := map[string]func() error{
		"SignUp":               func() error { _, err := s.SignUp(ctx, &auth.SignUpRequest{}); return err },
		"JoinLinkInfo":         func() error { _, err := s.JoinLinkInfo(ctx, &auth.JoinLinkInfoRequest{}); return err },
		"JoinTeam":             func() error { _, err := s.JoinTeam(ctx, &auth.JoinTeamRequest{}); return err },
		"ConfirmJoinCode":      func() error { _, err := s.ConfirmJoinCode(ctx, &auth.ConfirmJoinCodeRequest{}); return err },
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

// TestTeamsOffEntryAnswersByteForByte: with teams mode off (prod) the entry RPCs E1 changed answer
// exactly as before, whatever the request carries (a brand, a bad brand, a code, a link): no
// response, and the error's wire status is the same bytes {code 12, "teams mode is off"}.
func TestTeamsOffEntryAnswersByteForByte(t *testing.T) {
	want := []byte("\x08\x0c\x12\x11teams mode is off")
	s := &Server{}
	ctx := context.Background()
	brand := &pb_admin.TeamBrand{BrandName: "Acme", LegalAddress: []string{"1 Road"}, ContactEmail: "a@acme.io", Website: "acme.io"}
	badBrand := &pb_admin.TeamBrand{BrandName: "a\nb", Website: "localhost"}
	type answer struct {
		resp proto.Message
		err  error
	}
	calls := map[string]func() answer{
		"SignUp": func() answer {
			r, err := s.SignUp(ctx, &auth.SignUpRequest{})
			return answer{r, err}
		},
		"SignUp with a brand": func() answer {
			r, err := s.SignUp(ctx, &auth.SignUpRequest{Email: "a@acme.io", Password: "pw-123456789", TeamName: "Acme",
				TeamSlug: "acme", TermsAccepted: true, Brand: brand})
			return answer{r, err}
		},
		"SignUp with a bad brand": func() answer {
			r, err := s.SignUp(ctx, &auth.SignUpRequest{TeamSlug: "acme", Brand: badBrand})
			return answer{r, err}
		},
		"VerifyEmail": func() answer {
			r, err := s.VerifyEmail(ctx, &auth.VerifyEmailRequest{})
			return answer{r, err}
		},
		"VerifyEmail by code": func() answer {
			r, err := s.VerifyEmail(ctx, &auth.VerifyEmailRequest{Email: "a@acme.io", Code: "123456"})
			return answer{r, err}
		},
		"VerifyEmail by link": func() answer {
			r, err := s.VerifyEmail(ctx, &auth.VerifyEmailRequest{Token: "abc"})
			return answer{r, err}
		},
		"CreateTeam with a brand": func() answer {
			r, err := s.CreateTeam(ctx, &auth.CreateTeamRequest{TeamName: "Acme", TeamSlug: "acme", Brand: brand})
			return answer{r, err}
		},
		"CreateTeam with a bad brand": func() answer {
			r, err := s.CreateTeam(ctx, &auth.CreateTeamRequest{Brand: badBrand})
			return answer{r, err}
		},
	}
	for name, call := range calls {
		a := call()
		if a.resp != nil && !reflect.ValueOf(a.resp).IsNil() {
			t.Errorf("%s: response %v, want none", name, a.resp)
		}
		st, ok := status.FromError(a.err)
		if !ok {
			t.Fatalf("%s: %v is not a status error", name, a.err)
		}
		got, err := proto.MarshalOptions{Deterministic: true}.Marshal(st.Proto())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: wire status %q, want %q", name, got, want)
		}
		if a.err.Error() != "rpc error: code = Unimplemented desc = teams mode is off" {
			t.Errorf("%s: error text %q", name, a.err.Error())
		}
	}
}
