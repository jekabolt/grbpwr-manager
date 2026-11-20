package platformauth

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// A sign-up through a team join link passes the same proof-of-work gate as a plain sign-up: with
// the check enforced and no proof sent, it is refused and writes no account.
func TestJoinSignUpRunsProofOfWork(t *testing.T) {
	f := newFlow(t)
	f.svc.guard.cfg.BotCheck = BotCheckEnforce
	ctx := tenant.With(context.Background(), tenant.Team{ID: 1})
	_, err := f.svc.SignUp(ctx, &auth.SignUpRequest{
		Email: "bot-join@x.io", Password: "join-password-1", Name: "Bot Join", TermsAccepted: true, JoinLink: "any-link",
	})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "bot_check" {
		t.Fatalf("join sign-up without proof = %v", err)
	}
	var n int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM platform_user WHERE email = 'bot-join@x.io'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("join sign-up without proof wrote %d accounts (%v)", n, err)
	}
}
