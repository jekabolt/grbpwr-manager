package platformauth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
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

// The join path has its own per-IP budget (MaxJoinsPerIPHr), apart from the workspace sign-up one:
// a studio office is one address, so the sixth join from it in an hour still gets its letter, the
// budget runs out with a visible join_rate_limited (never a silent success without a letter), and a
// workspace sign-up from the same address is not refused because of the joins.
func TestJoinSignUpHasItsOwnIPBudget(t *testing.T) {
	j := newJoinTeam(t, "office-team")
	link, err := j.st.EnsureJoinLink(context.Background(), platformstore.JoinLinkChange{
		TeamID: j.teamID, Actor: platformstore.Actor{UserID: j.ownerID}, IP: "10.6.0.1", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	token := platformstore.JoinLinkToken(j.svc.pepper, link.TeamID, link.Gen, link.Nonce)
	office := tenant.With(ctxFrom("10.9.0.1", ""), tenant.Team{ID: j.teamID})
	join := func(i int) error {
		_, err := j.svc.SignUp(office, &auth.SignUpRequest{
			Email: fmt.Sprintf("person%d@office.io", i), Password: "join-password-1", Name: "Office Person",
			TermsAccepted: true, JoinLink: token,
		})
		return err
	}
	for i := 1; i <= MaxJoinsPerIPHr; i++ {
		if err := join(i); err != nil {
			t.Fatalf("join sign-up %d from one office address = %v", i, err)
		}
		if i == MaxSignUpsPerIPHr+1 || i == MaxJoinsPerIPHr {
			if n := j.count(`SELECT COUNT(*) FROM platform_email WHERE to_email = ? AND template = 'join_code'`,
				fmt.Sprintf("person%d@office.io", i)); n != 1 {
				t.Fatalf("join sign-up %d from one office address sent %d letters", i, n)
			}
		}
	}
	err = join(MaxJoinsPerIPHr + 1)
	if status.Code(err) != codes.ResourceExhausted || status.Convert(err).Message() != "join_rate_limited" {
		t.Fatalf("join sign-up over the office budget = %v", err)
	}
	if n := j.count(`SELECT COUNT(*) FROM platform_user WHERE email = ?`, fmt.Sprintf("person%d@office.io", MaxJoinsPerIPHr+1)); n != 0 {
		t.Fatalf("a refused join sign-up wrote %d accounts", n)
	}
	if _, err := j.svc.SignUp(ctxFrom("10.9.0.1", ""), &auth.SignUpRequest{
		Email: "founder@studio.io", Password: "owner-password-1", TeamName: "own studio", TeamSlug: "own-studio", TermsAccepted: true,
	}); err != nil {
		t.Fatalf("workspace sign-up after the office joins = %v", err)
	}
	if kind, _, _ := j.lastLetter("founder@studio.io"); kind != "verify" {
		t.Fatalf("workspace sign-up after the office joins sent %q, want a verify letter", kind)
	}
}
