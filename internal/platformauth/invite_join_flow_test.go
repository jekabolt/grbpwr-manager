package platformauth

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// joinTeam is an active team with an owner, for the invite-join tests.
type joinTeam struct {
	*flow
	teamID, ownerID int64
}

func newJoinTeam(t *testing.T, slug string) *joinTeam {
	t.Helper()
	f := newFlow(t)
	owner := "delivered+b3" + slug + "-owner@resend.dev"
	ctx := ctxFrom("10.6.0.1", "")
	if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: owner, Password: "owner-password-1", TeamName: slug, TeamSlug: slug, TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	_, _, link := f.lastLetter(owner)
	if _, err := f.svc.VerifyEmail(ctx, &auth.VerifyEmailRequest{Token: link}); err != nil {
		t.Fatal(err)
	}
	j := &joinTeam{flow: f}
	if err := f.st.DB().Get(&j.teamID, `SELECT id FROM team WHERE slug = ?`, slug); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().Get(&j.ownerID, `SELECT id FROM platform_user WHERE email = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE team SET status = 'active', db_name = 'lkb_t_jointest' WHERE id = ?`, j.teamID); err != nil {
		t.Fatal(err)
	}
	f.st.Directory().Invalidate(j.teamID)
	return j
}

// invite inserts an invite with the given rights and returns its link token and id.
func (j *joinTeam) invite(email, perms string) (string, int64) {
	j.t.Helper()
	tok, hash, err := NewToken()
	if err != nil {
		j.t.Fatal(err)
	}
	r, err := j.st.DB().Exec(`INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at)
VALUES (?, ?, 'member', ?, ?, ?, ?)`, j.teamID, email, perms, hash, j.ownerID, j.clock.Add(InviteLetterTTL))
	if err != nil {
		j.t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return tok, id
}

func (j *joinTeam) membership(email string) string {
	j.t.Helper()
	var st string
	if err := j.st.DB().Get(&st, `SELECT m.status FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND u.email = ?`, j.teamID, email); err != nil {
		j.t.Fatalf("membership of %s: %v", email, err)
	}
	return st
}

func (j *joinTeam) remove(email string) {
	j.t.Helper()
	if _, err := j.st.DB().Exec(`UPDATE team_member m JOIN platform_user u ON u.id = m.user_id SET m.status = 'removed'
WHERE m.team_id = ? AND u.email = ?`, j.teamID, email); err != nil {
		j.t.Fatal(err)
	}
}

func (j *joinTeam) teamSignIn(ip, email, pw string) (*auth.SignInResponse, error) {
	return j.svc.SignIn(ctxFrom(ip, j.slug()), &auth.SignInRequest{Login: email, Password: pw, Target: auth.SignInTarget_SIGN_IN_TARGET_TEAM})
}

func (j *joinTeam) slug() string {
	var s string
	if err := j.st.DB().Get(&s, `SELECT slug FROM team WHERE id = ?`, j.teamID); err != nil {
		j.t.Fatal(err)
	}
	return s
}

const writeProducts = `[{"section":"products","access":"write"}]`

// TestInviteCrashAfterActivationHealsOnSignIn: the join stops after the control plane made the
// member active but before the team row was enabled. The row is still disabled and staged, and the
// next platform sign-in to the team finishes it and lets the person in. A join that stops before
// activation, with an invite that then dies, is swept back and its row stays disabled.
func TestInviteCrashAfterActivationHealsOnSignIn(t *testing.T) {
	j := newJoinTeam(t, "heal-team")
	const eve = "delivered+b3heal-eve@resend.dev"
	tok, _ := j.invite(eve, writeProducts)
	j.teams.onEnable = func(string) error { return errors.New("team database down") }
	ai, err := j.svc.AcceptInvite(ctxFrom("10.6.0.2", ""), &auth.AcceptInviteRequest{Token: tok, Password: "eve-password-1", Handle: "eve", TermsAccepted: true})
	if err != nil || ai.GetHandoffCode() == "" {
		t.Fatalf("accept with a failing enable = %v, %v", ai, err)
	}
	j.teams.onEnable = nil
	if st := j.membership(eve); st != "active" || !j.teams.disabled["eve"] || !j.teams.pending["eve"] {
		t.Fatalf("after a failed enable: membership %s, row disabled %v staged %v", st, j.teams.disabled["eve"], j.teams.pending["eve"])
	}
	si, err := j.teamSignIn("10.6.0.3", eve, "eve-password-1")
	if err != nil || si.GetToken() == "" {
		t.Fatalf("team sign-in after a failed enable = %v, %v", si, err)
	}
	if j.teams.disabled["eve"] || j.teams.pending["eve"] {
		t.Fatalf("after the heal: row disabled %v staged %v", j.teams.disabled["eve"], j.teams.pending["eve"])
	}

	// Stop after step 2 (the invite is revoked at that point), the invite dies, the sweep removes
	// the reservation; the row stays disabled and nothing signs in.
	const fay = "delivered+b3heal-fay@resend.dev"
	ftok, fid := j.invite(fay, writeProducts)
	j.teams.onEnsure = func(string) error {
		_, err := j.st.DB().Exec(`UPDATE team_invite SET revoked_at = ? WHERE id = ?`, j.clock, fid)
		return err
	}
	_, err = j.svc.AcceptInvite(ctxFrom("10.6.0.5", ""), &auth.AcceptInviteRequest{Token: ftok, Password: "fay-password-1", Handle: "fay", TermsAccepted: true})
	wantCode(t, err, codes.InvalidArgument, "join stopped before activation")
	j.teams.onEnsure = nil
	if st := j.membership(fay); st != "joining" || !j.teams.disabled["fay"] {
		t.Fatalf("after a stop before activation: membership %s, row disabled %v", st, j.teams.disabled["fay"])
	}
	n, err := j.st.SweepJoining(ctxFrom("10.6.0.1", ""), j.clock)
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	var left int
	if err := j.st.DB().Get(&left, `SELECT COUNT(*) FROM team_member m JOIN platform_user u ON u.id = m.user_id WHERE u.email = ?`, fay); err != nil || left != 0 {
		t.Fatalf("reservations of fay after the sweep: %d, %v", left, err)
	}
	_, err = j.teamSignIn("10.6.0.6", fay, "fay-password-1")
	wantCode(t, err, codes.Unauthenticated, "team sign-in after the sweep")
	if !j.teams.disabled["fay"] {
		t.Fatal("a swept reservation's row was enabled")
	}
}

// TestInviteForActiveMemberRefused (finding 1): an invite for someone who is already an active
// member is refused after the password check and changes nothing: no reservation, no re-stage of
// their row with the invite's rights, so a later revoke cannot leave those rights behind.
func TestInviteForActiveMemberRefused(t *testing.T) {
	j := newJoinTeam(t, "active-team")
	const gus = "delivered+b3active-gus@resend.dev"
	tok, _ := j.invite(gus, `[{"section":"products","access":"read"}]`)
	if _, err := j.svc.AcceptInvite(ctxFrom("10.7.0.1", ""), &auth.AcceptInviteRequest{Token: tok, Password: "gus-password-1", Handle: "gus", TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	before := j.teams.ensured["gus"]
	stages := 0
	j.teams.onEnsure = func(string) error { stages++; return nil }
	wtok, wid := j.invite(gus, `[{"section":"accounts","access":"write"}]`)
	_, err := j.svc.AcceptInvite(ctxFrom("10.7.0.2", ""), &auth.AcceptInviteRequest{Token: wtok, Password: "not-gus-password"})
	wantCode(t, err, codes.Unauthenticated, "wrong password on an active member's invite")
	_, err = j.svc.AcceptInvite(ctxFrom("10.7.0.3", ""), &auth.AcceptInviteRequest{Token: wtok, Password: "gus-password-1"})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "already_member" {
		t.Fatalf("invite for an active member = %v", err)
	}
	var accepted int
	if err := j.st.DB().Get(&accepted, `SELECT COUNT(*) FROM team_invite WHERE id = ? AND accepted_at IS NOT NULL`, wid); err != nil || accepted != 0 {
		t.Fatalf("refused invite accepted = %d, %v", accepted, err)
	}
	if after := j.teams.ensured["gus"]; stages != 0 || len(after) != 1 || after[0] != before[0] || j.teams.disabled["gus"] || j.teams.pending["gus"] {
		t.Fatalf("active member's row changed: stages %d, rights %v, disabled %v, staged %v", stages, after, j.teams.disabled["gus"], j.teams.pending["gus"])
	}
	if st := j.membership(gus); st != "active" {
		t.Fatalf("membership after the refused invite = %s", st)
	}
}

// TestInviteRemovalBeforeEnableKeepsRowOff (finding 2): the member is removed (control plane
// removed + team row disabled, which clears the marker) after the finishing path read the active
// membership but before the team-side enable. Neither the join's last step nor the sign-in heal
// re-enables the row, and no team token is issued.
func TestInviteRemovalBeforeEnableKeepsRowOff(t *testing.T) {
	j := newJoinTeam(t, "remove-team")
	removeDuringEnable := func(email, handle string) func(string) error {
		return func(string) error {
			j.remove(email)
			j.teams.adminDisable(handle) // under the fake's lock: the caller holds it
			return nil
		}
	}
	// The join's own step 4.
	const hal = "delivered+b3remove-hal@resend.dev"
	tok, _ := j.invite(hal, writeProducts)
	j.teams.onEnable = removeDuringEnable(hal, "hal")
	if _, err := j.svc.AcceptInvite(ctxFrom("10.8.0.1", ""), &auth.AcceptInviteRequest{Token: tok, Password: "hal-password-1", Handle: "hal", TermsAccepted: true}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	j.teams.onEnable = nil
	if !j.teams.disabled["hal"] {
		t.Fatal("step 4 re-enabled the row of a member removed during it")
	}
	_, err := j.teamSignIn("10.8.0.2", hal, "hal-password-1")
	wantCode(t, err, codes.Unauthenticated, "team sign-in of a member removed during step 4")

	// The sign-in heal: step 4 failed, then the removal lands between the membership re-read and
	// the enable.
	const ivy = "delivered+b3remove-ivy@resend.dev"
	itok, _ := j.invite(ivy, writeProducts)
	j.teams.onEnable = func(string) error { return errors.New("team database down") }
	if _, err := j.svc.AcceptInvite(ctxFrom("10.8.0.3", ""), &auth.AcceptInviteRequest{Token: itok, Password: "ivy-password-1", Handle: "ivy", TermsAccepted: true}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	j.teams.onEnable = removeDuringEnable(ivy, "ivy")
	si, err := j.teamSignIn("10.8.0.4", ivy, "ivy-password-1")
	j.teams.onEnable = nil
	if status.Code(err) != codes.Unauthenticated || si.GetToken() != "" {
		t.Fatalf("heal across a removal = %v, %v", si, err)
	}
	if !j.teams.disabled["ivy"] {
		t.Fatal("the heal re-enabled the row of a removed member")
	}
}

// TestInviteAdminDisableAfterJoinSticks (finding 3): the join's enable commits in the team database
// but its answer is lost, so the control plane never learns the join finished; an admin then
// disables the row. The next platform sign-in does not reverse the admin's decision: the marker
// went with the enable, and the admin's disable would have cleared it anyway.
func TestInviteAdminDisableAfterJoinSticks(t *testing.T) {
	j := newJoinTeam(t, "disable-team")
	const jon = "delivered+b3disable-jon@resend.dev"
	tok, _ := j.invite(jon, writeProducts)
	j.teams.loseEnableResult = true
	if _, err := j.svc.AcceptInvite(ctxFrom("10.9.0.1", ""), &auth.AcceptInviteRequest{Token: tok, Password: "jon-password-1", Handle: "jon", TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	j.teams.loseEnableResult = false
	if j.teams.disabled["jon"] || j.teams.pending["jon"] {
		t.Fatalf("after the join: row disabled %v staged %v", j.teams.disabled["jon"], j.teams.pending["jon"])
	}
	j.teams.mu.Lock()
	j.teams.adminDisable("jon")
	j.teams.mu.Unlock()
	_, err := j.teamSignIn("10.9.0.2", jon, "jon-password-1")
	wantCode(t, err, codes.Unauthenticated, "team sign-in of a member an admin disabled")
	if !j.teams.disabled["jon"] {
		t.Fatal("a sign-in reversed the admin's disable")
	}
}
