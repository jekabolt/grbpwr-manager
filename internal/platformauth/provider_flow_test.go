package platformauth

import (
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Provider sign-in → one-time code → root token → your teams → new team, the root sign-out, then
// the 60-s code and the 30-day absolute limit of the root session (teams O §2, §5, §7, §12).
// Tokens are verified against the real clock (iat must not be in the future), so every token is
// minted before the test clock moves; the hourly limiter also runs on the real clock.
func TestProviderSignInToNewTeam(t *testing.T) {
	f := newFlow(t)
	const email = "delivered+o1new@resend.dev"
	ctx := ctxFrom("10.7.0.1", "")
	google := func(sub, email string, authoritative bool) platformstore.ProviderSignIn {
		return platformstore.ProviderSignIn{Provider: platformstore.ProviderGoogle, Subject: sub, Email: email,
			EmailVerified: true, EmailAuthoritative: authoritative}
	}
	exchange := func(code string) string {
		t.Helper()
		ex, err := f.svc.ExchangeSignInCode(ctx, &auth.ExchangeSignInCodeRequest{Code: code})
		if err != nil || ex.GetToken() == "" {
			t.Fatalf("exchange = %v, %v", ex, err)
		}
		return ex.GetToken()
	}

	// Not authoritative and no identity yet: unverified, nothing written.
	if code, key := f.svc.CompleteProviderSignIn(ctx, google("g-o1", email, false)); code != "" || key != KeyUnverified {
		t.Fatalf("not authoritative: code %d bytes, key %q", len(code), key)
	}
	var users int
	if err := f.st.DB().Get(&users, `SELECT COUNT(*) FROM platform_user`); err != nil || users != 0 {
		t.Fatalf("unverified completion wrote %d accounts, %v", users, err)
	}

	code, key := f.svc.CompleteProviderSignIn(ctx, google("g-o1", "Delivered+O1new@Resend.dev", true))
	if code == "" || key != "" {
		t.Fatalf("new account: key %q", key)
	}
	ex, err := f.svc.ExchangeSignInCode(ctx, &auth.ExchangeSignInCodeRequest{Code: code})
	if err != nil || ex.GetToken() == "" || len(ex.GetTeams()) != 0 || ex.GetEnterTeamSlug() != "" {
		t.Fatalf("exchange = %v, %v", ex, err)
	}
	_, err = f.svc.ExchangeSignInCode(ctx, &auth.ExchangeSignInCodeRequest{Code: code})
	wantCode(t, err, codes.InvalidArgument, "code exchanged twice")
	root := ex.GetToken()

	// A known identity signs in whatever the flag says.
	again, key := f.svc.CompleteProviderSignIn(ctx, google("g-o1", email, false))
	if again == "" || key != "" {
		t.Fatalf("known identity, not authoritative: key %q", key)
	}
	second := exchange(again)
	late, _ := f.svc.CompleteProviderSignIn(ctx, google("g-o1", email, true)) // exchanged after 60 s below

	my, err := f.svc.ListMyTeams(ctx, root, &auth.ListMyTeamsRequest{})
	if err != nil || my.GetEmail() != email || !my.GetCanCreateTeam() || !my.GetTermsNeeded() || len(my.GetTeams()) != 0 {
		t.Fatalf("your teams = %v, %v", my, err)
	}

	// New team: terms first; then provisioning (approval off), not ready, owner, no logo.
	_, err = f.svc.CreateTeam(ctx, root, &auth.CreateTeamRequest{TeamName: "Studio O1", TeamSlug: "studio-o1"})
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "terms_required" {
		t.Fatalf("new team without terms = %v", err)
	}
	ct, err := f.svc.CreateTeam(ctx, root, &auth.CreateTeamRequest{TeamName: "Studio O1", TeamSlug: "studio-o1", TermsAccepted: true})
	if err != nil || ct.GetTeam().GetSlug() != "studio-o1" || ct.GetTeam().GetReady() || ct.GetTeam().GetAwaitingApproval() ||
		ct.GetTeam().GetRole() != "owner" || ct.GetTeam().GetLogoUrl() != "" {
		t.Fatalf("new team = %v, %v", ct, err)
	}
	// No cap on own teams: four of them; the hourly limit is 5 requests (the refused one counts).
	for i := 2; i <= 4; i++ {
		if _, err := f.svc.CreateTeam(ctx, root, &auth.CreateTeamRequest{TeamName: fmt.Sprintf("Studio %d", i), TeamSlug: fmt.Sprintf("studio-o1-%d", i)}); err != nil {
			t.Fatalf("own team %d: %v", i, err)
		}
	}
	_, err = f.svc.CreateTeam(ctx, root, &auth.CreateTeamRequest{TeamName: "Studio 5", TeamSlug: "studio-o1-5"})
	wantCode(t, err, codes.ResourceExhausted, "sixth new team in an hour")
	my, err = f.svc.ListMyTeams(ctx, root, &auth.ListMyTeamsRequest{})
	if err != nil || len(my.GetTeams()) != 4 || my.GetTermsNeeded() {
		t.Fatalf("your teams after four = %v, %v", my, err)
	}
	for _, r := range my.GetTeams() {
		if r.GetRole() != "owner" || r.GetReady() || r.GetAwaitingApproval() {
			t.Fatalf("own team %v", r)
		}
	}

	// The root sign-out ends that root session only.
	if _, err := f.svc.SignOut(ctx, second); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.ListMyTeams(ctx, second, &auth.ListMyTeamsRequest{})
	wantCode(t, err, codes.Unauthenticated, "your teams after the root sign-out")
	if _, err := f.svc.ListMyTeams(ctx, root, &auth.ListMyTeamsRequest{}); err != nil {
		t.Fatalf("the other root session after a sign-out: %v", err)
	}

	// A code dies after 60 s.
	f.clock = f.clock.Add(SignInCodeTTL + time.Second)
	_, err = f.svc.ExchangeSignInCode(ctx, &auth.ExchangeSignInCodeRequest{Code: late})
	wantCode(t, err, codes.InvalidArgument, "code after 60 s")

	// 30 days after the sign-in the root session is over, though the token itself is still valid.
	f.clock = f.clock.Add(RootSessionTTL)
	_, err = f.svc.ListMyTeams(ctx, root, &auth.ListMyTeamsRequest{})
	wantCode(t, err, codes.Unauthenticated, "your teams after 30 days")
	_, err = f.svc.CreateTeam(ctx, root, &auth.CreateTeamRequest{TeamName: "Late", TeamSlug: "late-o1", TermsAccepted: true})
	wantCode(t, err, codes.Unauthenticated, "new team after 30 days")
	_, err = f.svc.IssueHandoff(ctx, root, &auth.IssueHandoffRequest{TeamSlug: "studio-o1"})
	wantCode(t, err, codes.Unauthenticated, "hand-off after 30 days")
}

// With approval on, a new team waits in review and the operators get the request; an operator's
// account is refused a provider sign-in.
func TestCreateTeamUnderApproval(t *testing.T) {
	f := newFlowWith(t, true)
	ctx := ctxFrom("10.7.0.2", "")
	if _, err := f.st.DB().Exec(`INSERT INTO platform_user (email, handle, password_hash, status, is_operator, email_verified_at)
VALUES ('delivered+b3op@resend.dev', 'o1op', '!x', 'active', 1, UTC_TIMESTAMP(3))`); err != nil {
		t.Fatal(err)
	}
	code, key := f.svc.CompleteProviderSignIn(ctx, platformstore.ProviderSignIn{Provider: platformstore.ProviderApple, Subject: "a-o1",
		Email: "delivered+o1apple@resend.dev", EmailVerified: true, EmailAuthoritative: true})
	if key != "" {
		t.Fatalf("apple sign-in: %q", key)
	}
	ex, err := f.svc.ExchangeSignInCode(ctx, &auth.ExchangeSignInCodeRequest{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	ct, err := f.svc.CreateTeam(ctx, ex.GetToken(), &auth.CreateTeamRequest{TeamName: "Review O1", TeamSlug: "review-o1", TermsAccepted: true})
	if err != nil || !ct.GetTeam().GetAwaitingApproval() || ct.GetTeam().GetReady() {
		t.Fatalf("new team under approval = %v, %v", ct, err)
	}
	_, err = f.svc.CreateTeam(ctx, ex.GetToken(), &auth.CreateTeamRequest{TeamName: "Again", TeamSlug: "review-o1"})
	wantCode(t, err, codes.AlreadyExists, "address held by a team in review")
	if got := f.teamLetters("team_request"); len(got) != 1 || got[0].To != "delivered+b3op@resend.dev" {
		t.Fatalf("team_request letters = %+v", got)
	}
	my, err := f.svc.ListMyTeams(ctx, ex.GetToken(), &auth.ListMyTeamsRequest{})
	if err != nil || len(my.GetTeams()) != 1 || !my.GetTeams()[0].GetAwaitingApproval() {
		t.Fatalf("your teams under approval = %v, %v", my, err)
	}
	if code, key := f.svc.CompleteProviderSignIn(ctx, platformstore.ProviderSignIn{Provider: platformstore.ProviderGoogle, Subject: "g-op",
		Email: "delivered+b3op@resend.dev", EmailVerified: true, EmailAuthoritative: true}); code != "" || key != KeyUsePassword {
		t.Fatalf("operator: code %d bytes, key %q", len(code), key)
	}
}
