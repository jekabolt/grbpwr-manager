package platformauth

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
)

// TestInviteCrashAfterActivationHealsOnSignIn: the join stops after the control plane made the
// member active but before the team row was enabled. The person is a member, their row is still
// disabled, and the next platform sign-in to the team enables it and lets them in. A join that
// stops before activation, with an invite that then dies, is swept back and its row stays disabled.
func TestInviteCrashAfterActivationHealsOnSignIn(t *testing.T) {
	f := newFlow(t)
	const owner = "delivered+b3heal-owner@resend.dev"
	ctx := ctxFrom("10.6.0.1", "")
	if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: owner, Password: "owner-password-1", TeamName: "Heal", TeamSlug: "heal-team", TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	_, _, link := f.lastLetter(owner)
	if _, err := f.svc.VerifyEmail(ctx, &auth.VerifyEmailRequest{Token: link}); err != nil {
		t.Fatal(err)
	}
	var teamID, ownerID int64
	if err := f.st.DB().Get(&teamID, `SELECT id FROM team WHERE slug = 'heal-team'`); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().Get(&ownerID, `SELECT id FROM platform_user WHERE email = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE team SET status = 'active', db_name = 'lkb_t_healtest' WHERE id = ?`, teamID); err != nil {
		t.Fatal(err)
	}
	f.st.Directory().Invalidate(teamID)
	invite := func(email string) (tok string, id int64) {
		tok, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		r, err := f.st.DB().Exec(`INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at)
VALUES (?, ?, 'member', '[{"section":"products","access":"write"}]', ?, ?, ?)`, teamID, email, hash, ownerID, f.clock.Add(InviteLetterTTL))
		if err != nil {
			t.Fatal(err)
		}
		id, _ = r.LastInsertId()
		return tok, id
	}
	pending := func(email string) (status string, flag bool) {
		var m struct {
			Status string `db:"status"`
			Flag   bool   `db:"enable_pending"`
		}
		if err := f.st.DB().Get(&m, `SELECT m.status, m.enable_pending FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND u.email = ?`, teamID, email); err != nil {
			t.Fatalf("membership of %s: %v", email, err)
		}
		return m.Status, m.Flag
	}

	// Crash after step 3: the enable fails, the acceptance still answers with the hand-off.
	const eve = "delivered+b3heal-eve@resend.dev"
	tok, _ := invite(eve)
	f.teams.onEnable = func(string) error { return errors.New("team database down") }
	ai, err := f.svc.AcceptInvite(ctxFrom("10.6.0.2", ""), &auth.AcceptInviteRequest{Token: tok, Password: "eve-password-1", Handle: "eve", TermsAccepted: true})
	if err != nil || ai.GetHandoffCode() == "" {
		t.Fatalf("accept with a failing enable = %v, %v", ai, err)
	}
	f.teams.onEnable = nil
	if st, flag := pending(eve); st != "active" || !flag || !f.teams.disabled["eve"] {
		t.Fatalf("after a failed enable: membership %s, pending %v, row disabled %v", st, flag, f.teams.disabled["eve"])
	}
	// The next team sign-in enables the row, clears the flag and gets in.
	si, err := f.svc.SignIn(ctxFrom("10.6.0.3", "heal-team"), &auth.SignInRequest{Login: eve, Password: "eve-password-1", Target: auth.SignInTarget_SIGN_IN_TARGET_TEAM})
	if err != nil || si.GetToken() == "" {
		t.Fatalf("team sign-in after a failed enable = %v, %v", si, err)
	}
	if st, flag := pending(eve); st != "active" || flag || f.teams.disabled["eve"] {
		t.Fatalf("after the heal: membership %s, pending %v, row disabled %v", st, flag, f.teams.disabled["eve"])
	}
	// A row disabled later by an admin, without the flag, stays disabled: never the reverse heal.
	f.teams.disabled["eve"] = true
	_, err = f.svc.SignIn(ctxFrom("10.6.0.4", "heal-team"), &auth.SignInRequest{Login: eve, Password: "eve-password-1", Target: auth.SignInTarget_SIGN_IN_TARGET_TEAM})
	wantCode(t, err, codes.Unauthenticated, "team sign-in of a member disabled by an admin")

	// Crash after step 2: the row is staged disabled, the process dies before activation (the
	// invite is revoked here to stop it at the same point), the invite dies; the sweep removes the
	// reservation and the row stays disabled.
	const fay = "delivered+b3heal-fay@resend.dev"
	ftok, fid := invite(fay)
	f.teams.onEnsure = func(string) error {
		_, err := f.st.DB().Exec(`UPDATE team_invite SET revoked_at = ? WHERE id = ?`, f.clock, fid)
		return err
	}
	_, err = f.svc.AcceptInvite(ctxFrom("10.6.0.5", ""), &auth.AcceptInviteRequest{Token: ftok, Password: "fay-password-1", Handle: "fay", TermsAccepted: true})
	wantCode(t, err, codes.InvalidArgument, "join stopped before activation")
	f.teams.onEnsure = nil
	if st, flag := pending(fay); st != "joining" || !flag || !f.teams.disabled["fay"] {
		t.Fatalf("after a stop before activation: membership %s, pending %v, row disabled %v", st, flag, f.teams.disabled["fay"])
	}
	n, err := f.st.SweepJoining(ctx, f.clock)
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	var left int
	if err := f.st.DB().Get(&left, `SELECT COUNT(*) FROM team_member m JOIN platform_user u ON u.id = m.user_id WHERE u.email = ?`, fay); err != nil || left != 0 {
		t.Fatalf("reservations of fay after the sweep: %d, %v", left, err)
	}
	if !f.teams.disabled["fay"] {
		t.Fatal("the sweep enabled a staged row")
	}
	_, err = f.svc.SignIn(ctxFrom("10.6.0.6", "heal-team"), &auth.SignInRequest{Login: fay, Password: "fay-password-1", Target: auth.SignInTarget_SIGN_IN_TARGET_TEAM})
	wantCode(t, err, codes.Unauthenticated, "team sign-in after the sweep")
}
