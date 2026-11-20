package platformauth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
)

// Delete my account and restore it (P0 §3.5), end to end on the control plane.

// seedAccount is seedProfiles plus two teams of anna: prof-solo (in review, anna its only member:
// it closes with her account) and prof-duo (anna its only owner, boris a member: the delete is
// refused until boris owns it too). The fake team databases hold anna's admins rows enabled.
func seedAccount(t *testing.T, f *flow) (frozen *[]int64) {
	t.Helper()
	seedProfiles(t, f)
	for _, q := range []string{
		`INSERT INTO team (id, slug, name, db_name, status) VALUES
 (32, 'prof-solo', 'Solo', NULL, 'review'), (33, 'prof-duo', 'Duo', 'lk_t_profduo', 'active')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (32, 301, 'owner', 'active'),
 (33, 301, 'owner', 'active'), (33, 302, 'member', 'active')`,
	} {
		if _, err := f.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	f.teams.ensured["prof-anna"] = nil
	frozen = new([]int64)
	f.svc.d.Hooks.FreezeMember = func(_ context.Context, team tenant.Team, handle string) (bool, error) {
		f.teams.mu.Lock()
		defer f.teams.mu.Unlock()
		*frozen = append(*frozen, team.ID)
		if f.teams.disabled[handle] {
			return false, nil
		}
		f.teams.disabled[handle], f.teams.pending[handle] = true, true
		return true, nil
	}
	return frozen
}

func refSlugs(refs []*auth.TeamRef) string {
	var s []string
	for _, r := range refs {
		s = append(s, r.GetSlug())
	}
	sort.Strings(s)
	return strings.Join(s, ",")
}

func (f *flow) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.st.DB().Get(&n, q, args...); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestDeleteAndRestoreAccountFlow(t *testing.T) {
	f := newFlow(t)
	frozen := seedAccount(t, f)
	ctx := ctxFrom("10.9.4.1", "")
	anna := profToken(t, f, 301, "prof-anna", profAnnaRootSID, 0, false)

	// The modal: prof-duo blocks the delete, prof-solo closes with it.
	p, err := f.svc.GetMyProfile(ctx, anna, &auth.GetMyProfileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if refSlugs(p.GetProfile().GetSoleOwnerTeams()) != "prof-duo" || refSlugs(p.GetProfile().GetClosingTeams()) != "prof-solo" {
		t.Fatalf("preview sole=%v closing=%v", p.GetProfile().GetSoleOwnerTeams(), p.GetProfile().GetClosingTeams())
	}
	_, err = f.svc.DeleteMyAccount(ctx, anna, &auth.DeleteMyAccountRequest{ConfirmUsername: "@prof-anna"})
	wantKey(t, err, codes.FailedPrecondition, "sole_owner", "delete as the only owner of a team with members")
	if _, err := f.st.DB().Exec(`UPDATE team_member SET role = 'owner' WHERE team_id = 33 AND user_id = 302`); err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{"", "prof-boris", "@prof-ann", "prof-anna@x.test"} {
		_, err = f.svc.DeleteMyAccount(ctx, anna, &auth.DeleteMyAccountRequest{ConfirmUsername: typed})
		wantKey(t, err, codes.InvalidArgument, "bad_confirm", "confirm "+typed)
	}
	if f.count(`SELECT COUNT(*) FROM platform_user WHERE id = 301 AND status = 'active'`) != 1 {
		t.Fatal("a refused delete wrote")
	}

	del, err := f.svc.DeleteMyAccount(ctx, anna, &auth.DeleteMyAccountRequest{ConfirmUsername: " @Prof-Anna "})
	if err != nil {
		t.Fatal(err)
	}
	purgeAt := f.clock.Add(30 * 24 * time.Hour)
	if !del.GetPurgeAt().AsTime().Equal(purgeAt) {
		t.Fatalf("purge_at %v, want %v", del.GetPurgeAt().AsTime(), purgeAt)
	}
	// Every session ended and handed to the edge; the open teams' admins rows frozen (prof-solo has no
	// database yet); prof-solo closed with the account; the letter names the last day.
	if f.revokedSIDs(profAnnaTeamSID, profAnnaRootSID) != 2 || f.revokedSIDs(profBorisTeamSID) != 0 {
		t.Fatal("delete ended the wrong sessions")
	}
	if got := f.teams.dropped["prof-anna"]; len(got) != 2 {
		t.Fatalf("edge drops %v", got)
	}
	sort.Slice(*frozen, func(i, j int) bool { return (*frozen)[i] < (*frozen)[j] })
	if len(*frozen) != 2 || (*frozen)[0] != 30 || (*frozen)[1] != 33 || !f.teams.disabled["prof-anna"] {
		t.Fatalf("frozen rows %v disabled=%v", *frozen, f.teams.disabled["prof-anna"])
	}
	if f.count(`SELECT COUNT(*) FROM team WHERE id = 32 AND status = 'deleted' AND deleted_from = 'review' AND closed_with_user_id = 301`) != 1 ||
		f.count(`SELECT COUNT(*) FROM team_member WHERE user_id = 301 AND status = 'frozen'`) != 3 {
		t.Fatal("closing team or frozen memberships")
	}
	if kind := f.letterKind("prof-anna@x.test"); kind != "account_deleted" {
		t.Fatalf("letter %s", kind)
	}
	// The old token is dead; the frozen person is nowhere: not a member of prof-alpha for boris, not
	// counted, the console cannot bring prof-solo back without her.
	_, err = f.svc.GetMyProfile(ctx, anna, &auth.GetMyProfileRequest{})
	wantCode(t, err, codes.Unauthenticated, "old token after delete")
	if _, ok, err := f.st.Membership(context.Background(), 301, 30); ok || err != nil {
		t.Fatalf("frozen membership read: %v %v", ok, err)
	}
	if teams, err := f.st.MemberTeams(context.Background(), 301); err != nil || len(teams) != 0 {
		t.Fatalf("frozen teams listed: %v %v", teams, err)
	}
	if _, err := f.st.RestoreTeam(context.Background(), 32, f.clock); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("console restore of a team closed with a deleted account: %v", err)
	}

	// Sign-in: the right password learns the date, a wrong one learns nothing.
	si, err := f.svc.SignIn(ctx, &auth.SignInRequest{Login: "prof-anna", Password: profPassword, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil || si.GetAccountDeletedUntil() == nil || !si.GetAccountDeletedUntil().AsTime().Equal(purgeAt) || si.GetToken() != "" {
		t.Fatalf("sign-in of a deleted account = %v, %v", si, err)
	}
	_, err = f.svc.SignIn(ctx, &auth.SignInRequest{Login: "prof-anna", Password: "wrong-password-1", Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	wantCode(t, err, codes.Unauthenticated, "wrong password of a deleted account")
	// Sign-up with the email: no new account, the restore letter instead.
	if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: "prof-anna@x.test", Password: "another-password-1",
		TeamName: "prof-new", TeamSlug: "prof-new", TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	if f.count(`SELECT COUNT(*) FROM platform_user WHERE email = 'prof-anna@x.test'`) != 1 ||
		f.count(`SELECT COUNT(*) FROM team WHERE slug = 'prof-new'`) != 0 {
		t.Fatal("sign-up made an account or a team over a deleted one")
	}
	if kind, _, _ := f.lastLetter("prof-anna@x.test"); kind != "restore" {
		t.Fatalf("sign-up letter %s", kind)
	}

	// Restore: a letter by the login, then the code. Someone else's login with anna's code, a wrong
	// code and a garbage link all fail.
	if _, err := f.svc.RequestAccountRestore(ctx, &auth.RequestAccountRestoreRequest{Login: "@prof-anna"}); err != nil {
		t.Fatal(err)
	}
	_, code, tok := f.lastLetter("prof-anna@x.test")
	for _, req := range []*auth.RestoreAccountRequest{
		{Login: "prof-boris", Code: code}, {Login: "prof-anna", Code: wrong(code)}, {Login: "nobody", Code: code},
		{Token: "not-a-token"},
	} {
		_, err = f.svc.RestoreAccount(ctx, req)
		wantCode(t, err, codes.InvalidArgument, "restore "+req.String())
	}
	if f.count(`SELECT COUNT(*) FROM platform_user WHERE id = 301 AND status = 'deleted'`) != 1 {
		t.Fatal("a failed restore wrote")
	}
	// Boris asking for anna's account mails anna, never him; his own active account gets nothing.
	before := f.count(`SELECT COUNT(*) FROM platform_email WHERE to_email = 'prof-boris@x.test'`)
	if _, err := f.svc.RequestAccountRestore(ctx, &auth.RequestAccountRestoreRequest{Login: "prof-boris"}); err != nil ||
		f.count(`SELECT COUNT(*) FROM platform_email WHERE to_email = 'prof-boris@x.test'`) != before {
		t.Fatalf("restore letter for an active account: %v", err)
	}

	rs, err := f.svc.RestoreAccount(ctx, &auth.RestoreAccountRequest{Login: "PROF-ANNA@x.test", Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if rs.GetRootToken() == "" || refSlugs(rs.GetTeams()) != "prof-alpha,prof-duo,prof-solo" {
		t.Fatalf("restore = %v", rs)
	}
	// Back with the same roles; prof-solo back in review; the admins rows the delete froze are enabled.
	if f.count(`SELECT COUNT(*) FROM team_member WHERE user_id = 301 AND status = 'active' AND
 ((team_id = 30 AND role = 'member') OR (team_id = 32 AND role = 'owner') OR (team_id = 33 AND role = 'owner'))`) != 3 ||
		f.count(`SELECT COUNT(*) FROM team WHERE id = 32 AND status = 'review' AND closed_with_user_id IS NULL AND purge_at IS NULL`) != 1 ||
		f.count(`SELECT COUNT(*) FROM platform_user WHERE id = 301 AND status = 'active' AND deleted_at IS NULL AND purge_at IS NULL`) != 1 {
		t.Fatal("restored state")
	}
	if f.teams.disabled["prof-anna"] || f.teams.pending["prof-anna"] {
		t.Fatal("admins row still frozen after restore")
	}
	// The letter is spent, and so is the sign-up's one; the new root session works.
	_, err = f.svc.RestoreAccount(ctx, &auth.RestoreAccountRequest{Login: "prof-anna", Code: code})
	wantCode(t, err, codes.InvalidArgument, "restore code reused")
	_, err = f.svc.RestoreAccount(ctx, &auth.RestoreAccountRequest{Token: tok})
	wantCode(t, err, codes.InvalidArgument, "restore link after the code")
	if f.count(`SELECT COUNT(*) FROM email_token WHERE user_id = 301 AND purpose = 'restore' AND used_at IS NULL`) != 0 {
		t.Fatal("restore letters left live")
	}
	if _, err := f.svc.GetMyProfile(ctx, rs.GetRootToken(), &auth.GetMyProfileRequest{}); err != nil {
		t.Fatalf("restored root token: %v", err)
	}
	si, err = f.svc.SignIn(ctx, &auth.SignInRequest{Login: "prof-anna", Password: profPassword, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil || si.GetToken() == "" || si.GetAccountDeletedUntil() != nil {
		t.Fatalf("sign-in after restore = %v, %v", si, err)
	}
}

// After the 30 days nothing brings the account back, and nothing tells the window is over.
func TestRestoreWindowOver(t *testing.T) {
	f := newFlow(t)
	seedAccount(t, f)
	ctx := ctxFrom("10.9.4.2", "")
	anna := profToken(t, f, 301, "prof-anna", profAnnaRootSID, 0, false)
	if _, err := f.st.DB().Exec(`UPDATE team_member SET role = 'owner' WHERE team_id = 33 AND user_id = 302`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RequestAccountRestore(ctx, &auth.RequestAccountRestoreRequest{Login: "prof-anna"}); err != nil {
		t.Fatal(err)
	}
	if f.count(`SELECT COUNT(*) FROM platform_email WHERE template = 'restore'`) != 0 {
		t.Fatal("restore letter for an active account")
	}
	if _, err := f.svc.DeleteMyAccount(ctx, anna, &auth.DeleteMyAccountRequest{ConfirmUsername: "prof-anna"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RequestAccountRestore(ctx, &auth.RequestAccountRestoreRequest{Login: "prof-anna"}); err != nil {
		t.Fatal(err)
	}
	_, code, tok := f.lastLetter("prof-anna@x.test")
	f.clock = f.clock.Add(30*24*time.Hour + time.Second)
	_, err := f.svc.SignIn(ctx, &auth.SignInRequest{Login: "prof-anna", Password: profPassword, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	wantCode(t, err, codes.Unauthenticated, "sign-in after the window")
	for _, req := range []*auth.RestoreAccountRequest{{Login: "prof-anna", Code: code}, {Token: tok}} {
		_, err = f.svc.RestoreAccount(ctx, req)
		wantCode(t, err, codes.InvalidArgument, "restore after the window")
	}
	n := f.count(`SELECT COUNT(*) FROM platform_email WHERE template = 'restore'`)
	if _, err := f.svc.RequestAccountRestore(ctx, &auth.RequestAccountRestoreRequest{Login: "prof-anna"}); err != nil ||
		f.count(`SELECT COUNT(*) FROM platform_email WHERE template = 'restore'`) != n {
		t.Fatalf("restore letter after the window: %v", err)
	}
	if f.count(`SELECT COUNT(*) FROM platform_user WHERE id = 301 AND status = 'deleted'`) != 1 {
		t.Fatal("account changed after the window")
	}
}

func (f *flow) letterKind(to string) string {
	f.t.Helper()
	var kind string
	if err := f.st.DB().Get(&kind, `SELECT template FROM platform_email WHERE to_email = ? ORDER BY id DESC LIMIT 1`, to); err != nil {
		f.t.Fatal(err)
	}
	return kind
}
