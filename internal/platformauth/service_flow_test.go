package platformauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The flow tests own this control-plane database only.
const flowDB = "platformauth_b3_test"

func flowDSN(t *testing.T, db string) string {
	t.Helper()
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI set but MYSQL_HOST empty")
		}
		t.Skip("no test MySQL (run through test-db.sh)")
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

type fakeTeams struct {
	mu      sync.Mutex
	ensured map[string][]entity.AdminPermission // handle → perms written into a team
	pwSet   map[string]string
	// disabled: handle → its admins row is disabled; pending: the row carries the join marker.
	disabled map[string]bool
	pending  map[string]bool
	// onEnsure, when set, runs before the admins row is staged; an error fails the write.
	onEnsure func(handle string) error
	// onEnable, when set, runs before the compare-and-set; an error fails the enable.
	onEnable func(handle string) error
	// loseEnableResult: the enable commits in the team database, then its answer is lost.
	loseEnableResult bool
}

// adminDisable is an admin (or a removal) disabling the row: the marker goes in the same write.
func (ft *fakeTeams) adminDisable(handle string) {
	ft.disabled[handle], ft.pending[handle] = true, false
}

type flow struct {
	t     *testing.T
	st    *platformstore.Store
	svc   *Service
	teams *fakeTeams
	clock time.Time
}

func newFlow(t *testing.T) *flow { t.Helper(); return newFlowWith(t, false) }

// newFlowWith builds the flow with sign-up approval on or off.
func newFlowWith(t *testing.T, approval bool) *flow {
	t.Helper()
	d := flowDSN(t, flowDB)
	if err := testdbguard.CheckMySQLDSN(d); err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", flowDSN(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP DATABASE IF EXISTS `" + flowDB + "`", "CREATE DATABASE `" + flowDB + "` CHARACTER SET utf8mb4"} {
		if _, err := root.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	root.Close()
	t.Cleanup(func() {
		if r, err := sql.Open("mysql", flowDSN(t, "")); err == nil {
			_, _ = r.Exec("DROP DATABASE IF EXISTS `" + flowDB + "`")
			r.Close()
		}
	})
	st, err := platformstore.Open(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ph, err := pwhash.New(16, 1000)
	if err != nil {
		t.Fatal(err)
	}
	f := &flow{t: t, st: st, teams: &fakeTeams{ensured: map[string][]entity.AdminPermission{}, pwSet: map[string]string{}, disabled: map[string]bool{}, pending: map[string]bool{}},
		clock: time.Now().UTC().Truncate(time.Second)}
	svc, err := New(Deps{
		Store:   st,
		Config:  platformstore.Config{BaseDomain: "beta.lekalo.studio", TokenPepper: testPepper, OperatorEmails: "delivered+b3op@resend.dev"},
		JWT:     jwtauth.New("HS256", []byte("flow-test-secret-flow-test-secret-0123456789"), nil),
		Hasher:  ph,
		TeamTTL: time.Hour,
		Now:     func() time.Time { return f.clock },
		Hooks: Hooks{
			TeamAccount: func(_ context.Context, _ tenant.Team, handle string) (TeamAccount, bool, error) {
				f.teams.mu.Lock()
				defer f.teams.mu.Unlock()
				perms, ok := f.teams.ensured[handle]
				return TeamAccount{Super: perms == nil, Perms: perms, Disabled: f.teams.disabled[handle], JoinPending: f.teams.pending[handle]}, ok, nil
			},
			StageMember: func(_ context.Context, _ tenant.Team, handle string, super bool, perms []entity.AdminPermission) error {
				f.teams.mu.Lock()
				defer f.teams.mu.Unlock()
				if f.teams.onEnsure != nil {
					if err := f.teams.onEnsure(handle); err != nil {
						return err
					}
				}
				if super {
					perms = nil
				} else if perms == nil {
					perms = []entity.AdminPermission{}
				}
				f.teams.ensured[handle] = perms
				f.teams.disabled[handle], f.teams.pending[handle] = true, true
				return nil
			},
			EnableMember: func(_ context.Context, _ tenant.Team, handle string) (bool, error) {
				f.teams.mu.Lock()
				defer f.teams.mu.Unlock()
				if f.teams.onEnable != nil {
					if err := f.teams.onEnable(handle); err != nil {
						return false, err
					}
				}
				if !f.teams.pending[handle] { // the store's compare-and-set: only a staged row
					return false, nil
				}
				f.teams.disabled[handle], f.teams.pending[handle] = false, false
				if f.teams.loseEnableResult {
					return false, errors.New("connection reset after commit")
				}
				return true, nil
			},
			TeamOnePassword: func(_ context.Context, handle, hash string) error {
				f.teams.mu.Lock()
				defer f.teams.mu.Unlock()
				f.teams.pwSet[handle] = hash
				return nil
			},
		},
		SignupApproval: approval,
	})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Ready() != nil {
		t.Fatal(svc.Ready())
	}
	t.Cleanup(svc.Close)
	f.svc = svc
	return f
}

// ctx is a request from ip, optionally on a team host.
func ctxFrom(ip, teamSlug string) context.Context {
	ctx := context.WithValue(context.Background(), middleware.ClientIPKey, ip)
	md := metadata.MD{}
	if teamSlug != "" {
		md.Set("x-team", teamSlug)
	}
	return metadata.NewIncomingContext(ctx, md)
}

type letterRow struct {
	ID      int64  `db:"id"`
	To      string `db:"to_email"`
	Kind    string `db:"template"`
	Payload []byte `db:"payload"`
}

// lastLetter is the newest outbox letter to the address: its code and link token.
func (f *flow) lastLetter(to string) (kind, code, token string) {
	f.t.Helper()
	var r letterRow
	if err := f.st.DB().Get(&r, `SELECT id, to_email, template, payload FROM platform_email WHERE to_email = ? ORDER BY id DESC LIMIT 1`, to); err != nil {
		f.t.Fatalf("no letter to %s: %v", to, err)
	}
	var p struct {
		Code, Link string
	}
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		f.t.Fatal(err)
	}
	_, tok, ok := strings.Cut(p.Link, "#token=")
	if !ok || p.Code == "" {
		f.t.Fatalf("letter has no code or link: %s", r.Payload)
	}
	return r.Kind, p.Code, tok
}

func wrong(code string) string {
	b := []byte(code)
	b[5] = '0' + (b[5]-'0'+1)%10
	return string(b)
}

func wantCode(t *testing.T, err error, c codes.Code, what string) {
	t.Helper()
	if status.Code(err) != c {
		t.Fatalf("%s: got %v, want %v", what, err, c)
	}
}

func TestIdentityFlow(t *testing.T) {
	f := newFlow(t)
	const anna = "delivered+b3anna@resend.dev"
	const pw = "anna-password-1"
	ctx := ctxFrom("10.0.0.1", "")

	// Without the terms the sheet is refused before anything is written or mailed.
	_, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: anna, Password: pw, TeamName: "Atelier Anna", TeamSlug: "atelier-anna"})
	wantCode(t, err, codes.InvalidArgument, "sign-up without terms")
	if status.Convert(err).Message() != "terms_required" {
		t.Fatalf("sign-up without terms = %v", err)
	}
	var written int
	if err := f.st.DB().Get(&written, `SELECT (SELECT COUNT(*) FROM platform_user) + (SELECT COUNT(*) FROM team) + (SELECT COUNT(*) FROM platform_email)`); err != nil || written != 0 {
		t.Fatalf("a refused sign-up wrote %d rows, %v", written, err)
	}

	// Sign-up: {} and a letter with a code and a link, written to the outbox.
	if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: anna, Password: pw, TeamName: "Atelier Anna", TeamSlug: "atelier-anna", TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	var terms struct {
		Version sql.NullString `db:"terms_version"`
		At      sql.NullTime   `db:"terms_accepted_at"`
	}
	if err := f.st.DB().Get(&terms, `SELECT terms_version, terms_accepted_at FROM platform_user WHERE email = ?`, anna); err != nil ||
		terms.Version.String != DefaultTermsVersion || !terms.At.Valid || terms.At.Time.Sub(f.clock).Abs() > time.Second {
		t.Fatalf("terms of anna = %+v, %v", terms, err)
	}
	kind, code, link := f.lastLetter(anna)
	if kind != "verify" || len(code) != 6 {
		t.Fatalf("letter %s %q", kind, code)
	}
	// The stored token row holds hashes only.
	var stored int
	if err := f.st.DB().Get(&stored, `SELECT COUNT(*) FROM email_token WHERE token_hash = ? OR code_hash = ?`, link, code); err != nil || stored != 0 {
		t.Fatalf("a secret is stored in plain text (%d, %v)", stored, err)
	}
	// The address is taken now, for a stranger and for anna alike: the same answer.
	_, e1 := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: "delivered+b3x@resend.dev", Password: pw, TeamName: "X", TeamSlug: "atelier-anna", TermsAccepted: true})
	_, e2 := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: anna, Password: "other-password", TeamName: "X", TeamSlug: "atelier-anna", TermsAccepted: true})
	wantCode(t, e1, codes.AlreadyExists, "stranger on a taken address")
	if e2 == nil || e1.Error() != e2.Error() {
		t.Fatalf("taken-address answers differ: %v / %v", e1, e2)
	}
	// A stranger cannot re-key anna's sign-up in progress: {} and nothing changes, no letter.
	var hashBefore string
	if err := f.st.DB().Get(&hashBefore, `SELECT password_hash FROM platform_user WHERE email = ?`, anna); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: anna, Password: "attacker-password", TeamName: "Evil", TeamSlug: "evil-anna", TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	var hashAfter string
	var evil int
	if err := f.st.DB().Get(&hashAfter, `SELECT password_hash FROM platform_user WHERE email = ?`, anna); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().Get(&evil, `SELECT COUNT(*) FROM team WHERE slug = 'evil-anna'`); err != nil {
		t.Fatal(err)
	}
	if hashAfter != hashBefore || evil != 0 {
		t.Fatal("a pending sign-up was re-keyed by another password")
	}
	if _, c2, l2 := f.lastLetter(anna); c2 != code || l2 != link {
		t.Fatal("a letter went out for the re-key attempt")
	}

	// Wrong codes: an unknown email and a known one answer identically.
	cctx := ctxFrom("10.0.0.2", "")
	_, eu := f.svc.VerifyEmail(cctx, &auth.VerifyEmailRequest{Email: "delivered+b3nobody@resend.dev", Code: code})
	_, ek := f.svc.VerifyEmail(cctx, &auth.VerifyEmailRequest{Email: anna, Code: wrong(code)})
	wantCode(t, eu, codes.InvalidArgument, "unknown email")
	if eu.Error() != ek.Error() {
		t.Fatalf("code answers differ: %v / %v", eu, ek)
	}
	// Four more wrong codes (five in all) kill the code: the right one is refused…
	for range 4 {
		_, err := f.svc.VerifyEmail(cctx, &auth.VerifyEmailRequest{Email: anna, Code: wrong(code)})
		wantCode(t, err, codes.InvalidArgument, "wrong code")
	}
	_, err = f.svc.VerifyEmail(cctx, &auth.VerifyEmailRequest{Email: anna, Code: code})
	wantCode(t, err, codes.InvalidArgument, "right code after five wrong")
	// …while the link still works, once.
	vr, err := f.svc.VerifyEmail(cctx, &auth.VerifyEmailRequest{Token: link})
	if err != nil || vr.GetTeamSlug() != "atelier-anna" || vr.GetHandoffCode() == "" {
		t.Fatalf("verify by link = %v, %v", vr, err)
	}
	_, err = f.svc.VerifyEmail(cctx, &auth.VerifyEmailRequest{Token: link})
	wantCode(t, err, codes.InvalidArgument, "link reused")

	// Sign-in on the root host lists the team, not ready while provisioning; a wrong password and an
	// unknown login answer the same.
	sctx := ctxFrom("10.0.0.3", "")
	_, ew := f.svc.SignIn(sctx, &auth.SignInRequest{Login: anna, Password: "nope-nope-nope", Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	_, en := f.svc.SignIn(sctx, &auth.SignInRequest{Login: "delivered+b3nobody@resend.dev", Password: "nope-nope-nope", Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	wantCode(t, ew, codes.Unauthenticated, "wrong password")
	if ew.Error() != en.Error() {
		t.Fatalf("sign-in answers differ: %v / %v", ew, en)
	}
	si, err := f.svc.SignIn(sctx, &auth.SignInRequest{Login: anna, Password: pw, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil || len(si.GetTeams()) != 1 || si.GetTeams()[0].GetReady() || si.GetTeams()[0].GetRole() != "owner" {
		t.Fatalf("root sign-in = %v, %v", si, err)
	}
	// The hand-off waits while the workspace is prepared, then works once the team is active.
	_, err = f.svc.ExchangeHandoff(ctxFrom("10.0.0.3", "atelier-anna"), &auth.ExchangeHandoffRequest{Code: vr.GetHandoffCode()})
	wantCode(t, err, codes.Unavailable, "hand-off while provisioning")
	var teamID int64
	if err := f.st.DB().Get(&teamID, `SELECT id FROM team WHERE slug = 'atelier-anna'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE team SET status = 'active', db_name = 'lkb_t_flowtest' WHERE id = ?`, teamID); err != nil {
		t.Fatal(err)
	}
	f.st.Directory().Invalidate(teamID)
	var annaHandle string
	if err := f.st.DB().Get(&annaHandle, `SELECT handle FROM platform_user WHERE email = ?`, anna); err != nil {
		t.Fatal(err)
	}
	f.teams.ensured[annaHandle] = nil // the provisioner wrote her as the team's super
	ex, err := f.svc.ExchangeHandoff(ctxFrom("10.0.0.3", "atelier-anna"), &auth.ExchangeHandoffRequest{Code: vr.GetHandoffCode()})
	if err != nil || ex.GetToken() == "" {
		t.Fatalf("exchange = %v, %v", ex, err)
	}
	_, err = f.svc.ExchangeHandoff(ctxFrom("10.0.0.3", "atelier-anna"), &auth.ExchangeHandoffRequest{Code: vr.GetHandoffCode()})
	wantCode(t, err, codes.InvalidArgument, "hand-off reused")
	// A root token issues a fresh hand-off into the team.
	ih, err := f.svc.IssueHandoff(sctx, si.GetToken(), &auth.IssueHandoffRequest{TeamSlug: "atelier-anna"})
	if err != nil || !strings.HasPrefix(ih.GetUrl(), "https://atelier-anna.beta.lekalo.studio/auth#code=") {
		t.Fatalf("issue hand-off = %v, %v", ih, err)
	}

	// Reset: 10 wrong codes in 24 h lock the code path; the link still resets and revokes sessions.
	rctx := ctxFrom("10.0.0.4", "")
	if _, err := f.svc.RequestPasswordReset(rctx, &auth.RequestPasswordResetRequest{Email: anna}); err != nil {
		t.Fatal(err)
	}
	_, rcode, _ := f.lastLetter(anna)
	for range 5 {
		_, err := f.svc.ResetPassword(rctx, &auth.ResetPasswordRequest{Email: anna, Code: wrong(rcode), NewPassword: "new-password-1"})
		wantCode(t, err, codes.InvalidArgument, "wrong reset code")
	}
	f.clock = f.clock.Add(61 * time.Minute) // past the hourly letter cap
	if _, err := f.svc.RequestPasswordReset(rctx, &auth.RequestPasswordResetRequest{Email: anna}); err != nil {
		t.Fatal(err)
	}
	_, rcode, _ = f.lastLetter(anna)
	for range 5 { // failures 6–10: the user's code path locks
		_, err := f.svc.ResetPassword(rctx, &auth.ResetPasswordRequest{Email: anna, Code: wrong(rcode), NewPassword: "new-password-1"})
		wantCode(t, err, codes.InvalidArgument, "wrong reset code")
	}
	f.clock = f.clock.Add(61 * time.Minute)
	if _, err := f.svc.RequestPasswordReset(rctx, &auth.RequestPasswordResetRequest{Email: anna}); err != nil {
		t.Fatal(err)
	}
	_, rcode, rlink := f.lastLetter(anna)
	_, err = f.svc.ResetPassword(rctx, &auth.ResetPasswordRequest{Email: anna, Code: rcode, NewPassword: "new-password-1"})
	wantCode(t, err, codes.InvalidArgument, "right code while locked")
	// A wrong link is refused before any hashing, and the link bucket closes a flood from one IP.
	lctx := ctxFrom("10.0.0.40", "")
	for i := range 10 {
		_, err := f.svc.ResetPassword(lctx, &auth.ResetPasswordRequest{Token: fmt.Sprintf("not-a-link-%d", i), NewPassword: "new-password-1"})
		wantCode(t, err, codes.InvalidArgument, "wrong reset link")
	}
	_, err = f.svc.ResetPassword(lctx, &auth.ResetPasswordRequest{Token: rlink, NewPassword: "new-password-1"})
	wantCode(t, err, codes.ResourceExhausted, "reset link flood")
	if _, err := f.svc.ResetPassword(rctx, &auth.ResetPasswordRequest{Token: rlink, NewPassword: "new-password-1"}); err != nil {
		t.Fatalf("reset by link: %v", err)
	}
	_, err = f.svc.IssueHandoff(sctx, si.GetToken(), &auth.IssueHandoffRequest{TeamSlug: "atelier-anna"})
	wantCode(t, err, codes.Unauthenticated, "root token after reset")
	if _, err := f.svc.SignIn(sctx, &auth.SignInRequest{Login: anna, Password: "new-password-1", Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT}); err != nil {
		t.Fatalf("sign-in with the new password: %v", err)
	}

	// Invite: a new account joins with its handle; the team gets its admins row with the invite's rights.
	var annaID int64
	if err := f.st.DB().Get(&annaID, `SELECT id FROM platform_user WHERE email = ?`, anna); err != nil {
		t.Fatal(err)
	}
	invite := func(email string) string {
		tok, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.DB().Exec(`INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at)
VALUES (?, ?, 'member', '[{"section":"products","access":"write"}]', ?, ?, ?)`, teamID, email, hash, annaID, f.clock.Add(InviteLetterTTL)); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	const bob = "delivered+b3bob@resend.dev"
	btok := invite(bob)
	gi, err := f.svc.GetInvite(ctx, &auth.GetInviteRequest{Token: btok})
	if err != nil || gi.GetTeamName() != "Atelier Anna" || gi.GetAccountExists() {
		t.Fatalf("get invite = %v, %v", gi, err)
	}
	// A new account needs the terms; the invite stays usable after the refusal.
	_, err = f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: btok, Password: "bob-password-1", Handle: "bob.k"})
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "terms_required" {
		t.Fatalf("invite for a new account without terms = %v", err)
	}
	ai, err := f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: btok, Password: "bob-password-1", Handle: "bob.k", TermsAccepted: true})
	if err != nil || ai.GetTeamSlug() != "atelier-anna" || ai.GetHandoffCode() == "" {
		t.Fatalf("accept = %v, %v", ai, err)
	}
	if p := f.teams.ensured["bob.k"]; len(p) != 1 || p[0].Section != "products" || p[0].Access != entity.AccessWrite || f.teams.disabled["bob.k"] {
		t.Fatalf("team admins row of bob = %v, disabled %v", p, f.teams.disabled["bob.k"])
	}
	if err := f.st.DB().Get(&terms, `SELECT terms_version, terms_accepted_at FROM platform_user WHERE email = ?`, bob); err != nil ||
		terms.Version.String != DefaultTermsVersion || !terms.At.Valid {
		t.Fatalf("terms of bob = %+v, %v", terms, err)
	}
	_, err = f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: btok, Password: "bob-password-1", Handle: "bob.k", TermsAccepted: true})
	wantCode(t, err, codes.InvalidArgument, "invite reused")
	// An existing account (removed from the team meanwhile) is not asked for the terms again.
	if _, err := f.st.DB().Exec(`UPDATE team_member m JOIN platform_user u ON u.id = m.user_id SET m.status = 'removed'
WHERE m.team_id = ? AND u.email = ?`, teamID, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: invite(bob), Password: "bob-password-1"}); err != nil {
		t.Fatalf("invite for an existing account without terms: %v", err)
	}
	// Password guesses through an invite share the address's sign-in bucket, whatever the IP.
	gtok := invite(bob)
	_, err = f.svc.AcceptInvite(ctxFrom("10.2.0.1", ""), &auth.AcceptInviteRequest{Token: gtok, Password: "not-bobs-password"})
	wantCode(t, err, codes.Unauthenticated, "invite with a wrong password")
	for i := 2; i <= 6; i++ {
		_, err = f.svc.AcceptInvite(ctxFrom(fmt.Sprintf("10.2.0.%d", i), ""), &auth.AcceptInviteRequest{Token: gtok, Password: "not-bobs-password"})
	}
	wantCode(t, err, codes.ResourceExhausted, "invite guesses from rotating IPs")
	// Two steps around the team write: a failed write leaves a reserved, inactive membership and a
	// live invite; nothing signs into the team; the same link then finishes the join.
	const dan = "delivered+b3dan@resend.dev"
	dtok := invite(dan)
	f.teams.onEnsure = func(string) error { return errors.New("team database down") }
	_, err = f.svc.AcceptInvite(ctxFrom("10.4.0.1", ""), &auth.AcceptInviteRequest{Token: dtok, Password: "dan-password-1", Handle: "dan", TermsAccepted: true})
	wantCode(t, err, codes.Internal, "invite while the team write fails")
	memberStatus := func(email string) string {
		var st string
		if err := f.st.DB().Get(&st, `SELECT m.status FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND u.email = ?`, teamID, email); err != nil {
			t.Fatalf("membership of %s: %v", email, err)
		}
		return st
	}
	var accepted, handoffs int
	if err := f.st.DB().Get(&accepted, `SELECT COUNT(*) FROM team_invite WHERE email = ? AND accepted_at IS NOT NULL`, dan); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().Get(&handoffs, `SELECT COUNT(*) FROM email_token e JOIN platform_user u ON u.id = e.user_id
WHERE u.email = ? AND e.purpose = 'handoff'`, dan); err != nil {
		t.Fatal(err)
	}
	if st := memberStatus(dan); st != "joining" || accepted != 0 || handoffs != 0 {
		t.Fatalf("after a failed team write: membership %s, accepted %d, hand-offs %d", st, accepted, handoffs)
	}
	dctx := ctxFrom("10.4.0.2", "")
	dsi, err := f.svc.SignIn(dctx, &auth.SignInRequest{Login: dan, Password: "dan-password-1", Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil || len(dsi.GetTeams()) != 0 {
		t.Fatalf("root sign-in of a reserved member = %v, %v", dsi, err)
	}
	// (The test clock runs ahead of the JWT clock here, so the hand-off is checked on the store.)
	var danID int64
	if err := f.st.DB().Get(&danID, `SELECT id FROM platform_user WHERE email = ?`, dan); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.st.Membership(ctx, danID, teamID); err != nil || ok {
		t.Fatalf("membership of a reserved member = %v, %v", ok, err)
	}
	_, err = f.svc.SignIn(ctxFrom("10.4.0.3", "atelier-anna"), &auth.SignInRequest{Login: dan, Password: "dan-password-1", Target: auth.SignInTarget_SIGN_IN_TARGET_TEAM})
	wantCode(t, err, codes.Unauthenticated, "team sign-in of a reserved member")
	f.teams.onEnsure = nil
	if gi, err := f.svc.GetInvite(ctx, &auth.GetInviteRequest{Token: dtok}); err != nil || !gi.GetAccountExists() {
		t.Fatalf("invite after the failed write = %v, %v", gi, err)
	}
	if _, err := f.svc.AcceptInvite(ctxFrom("10.4.0.4", ""), &auth.AcceptInviteRequest{Token: dtok, Password: "dan-password-1"}); err != nil {
		t.Fatalf("retry of the invite: %v", err)
	}
	if st := memberStatus(dan); st != "active" {
		t.Fatalf("membership after the retry = %s", st)
	}
	// A member removed while the team write runs stays removed; the invite is not consumed.
	etok := invite(dan)
	if _, err := f.st.DB().Exec(`UPDATE team_member m JOIN platform_user u ON u.id = m.user_id SET m.status = 'removed'
WHERE m.team_id = ? AND u.email = ?`, teamID, dan); err != nil {
		t.Fatal(err)
	}
	f.teams.onEnsure = func(string) error {
		_, err := f.st.DB().Exec(`UPDATE team_member m JOIN platform_user u ON u.id = m.user_id SET m.status = 'removed'
WHERE m.team_id = ? AND u.email = ?`, teamID, dan)
		return err
	}
	_, err = f.svc.AcceptInvite(ctxFrom("10.4.0.5", ""), &auth.AcceptInviteRequest{Token: etok, Password: "dan-password-1"})
	wantCode(t, err, codes.InvalidArgument, "member removed during the join")
	f.teams.onEnsure = nil
	if st := memberStatus(dan); st != "removed" {
		t.Fatalf("membership of a member removed during the join = %s", st)
	}
	// A suspended team refuses new members.
	ctok := invite("delivered+b3cara@resend.dev")
	if _, err := f.st.DB().Exec(`UPDATE team SET status = 'suspended' WHERE id = ?`, teamID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: ctok, Password: "cara-password-1", Handle: "cara", TermsAccepted: true})
	wantCode(t, err, codes.FailedPrecondition, "suspended team")

	// Operators: the env list creates the operator with an unusable password.
	if err := f.svc.SyncOperators(ctx); err != nil {
		t.Fatal(err)
	}
	var op struct {
		Op    bool           `db:"is_operator"`
		Hash  string         `db:"password_hash"`
		Terms sql.NullString `db:"terms_version"`
	}
	if err := f.st.DB().Get(&op, `SELECT is_operator, password_hash, terms_version FROM platform_user WHERE email = 'delivered+b3op@resend.dev'`); err != nil ||
		!op.Op || !wellFormedHash(op.Hash) || op.Terms.Valid {
		t.Fatalf("operator = %+v, %v", op, err)
	}
	_, err = f.svc.SignIn(ctxFrom("10.3.0.1", ""), &auth.SignInRequest{Login: "delivered+b3op@resend.dev", Password: "anything-at-all",
		Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	wantCode(t, err, codes.Unauthenticated, "operator before a reset")

	// Every letter went to the test inbox only.
	var stray int
	if err := f.st.DB().Get(&stray, `SELECT COUNT(*) FROM platform_email WHERE to_email NOT LIKE 'delivered+%@resend.dev'`); err != nil || stray != 0 {
		t.Fatalf("letters outside the test inbox: %d, %v", stray, err)
	}
}

// teamLetters are the outbox letters of a kind, oldest first.
func (f *flow) teamLetters(kind string) []letterRow {
	f.t.Helper()
	var rows []letterRow
	if err := f.st.DB().Select(&rows, `SELECT id, to_email, template, payload FROM platform_email WHERE template = ? ORDER BY id`, kind); err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func TestSignupApproval(t *testing.T) {
	f := newFlowWith(t, true)
	const (
		anna = "delivered+b3anna@resend.dev"
		bob  = "delivered+b3bob@resend.dev"
		op   = "delivered+b3op@resend.dev"
		pw   = "anna-password-1"
	)
	ctx := ctxFrom("10.0.1.1", "")
	if err := f.svc.SyncOperators(ctx); err != nil {
		t.Fatal(err)
	}
	signUp := func(email, name, slug string) (code, link string) {
		t.Helper()
		if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: email, Password: pw, TeamName: name, TeamSlug: slug, TermsAccepted: true}); err != nil {
			t.Fatal(err)
		}
		_, code, link = f.lastLetter(email)
		return code, link
	}

	// Verify by link: the account is active, the team waits in review, no hand-off and no session.
	_, link := signUp(anna, "Atelier Anna", "atelier-anna")
	vr, err := f.svc.VerifyEmail(ctx, &auth.VerifyEmailRequest{Token: link})
	if err != nil || vr.GetTeamSlug() != "atelier-anna" || vr.GetHandoffCode() != "" || !vr.GetAwaitingApproval() {
		t.Fatalf("verify under approval = %v, %v", vr, err)
	}
	var team struct {
		ID      int64        `db:"id"`
		Status  string       `db:"status"`
		Expires sql.NullTime `db:"pending_expires_at"`
	}
	if err := f.st.DB().Get(&team, `SELECT id, status, pending_expires_at FROM team WHERE slug = 'atelier-anna'`); err != nil ||
		team.Status != "review" || team.Expires.Valid {
		t.Fatalf("team after verify = %+v, %v", team, err)
	}
	var handoffs, sessions int
	if err := f.st.DB().Get(&handoffs, `SELECT COUNT(*) FROM email_token WHERE purpose = 'handoff'`); err != nil || handoffs != 0 {
		t.Fatalf("hand-offs = %d, %v", handoffs, err)
	}
	if err := f.st.DB().Get(&sessions, `SELECT COUNT(*) FROM user_session`); err != nil || sessions != 0 {
		t.Fatalf("sessions = %d, %v", sessions, err)
	}
	// One team_request letter per active operator: team name, slug, the person's email, the console.
	reqs := f.teamLetters("team_request")
	if len(reqs) != 1 || reqs[0].To != op {
		t.Fatalf("team_request letters = %+v", reqs)
	}
	var p struct {
		Link     string `json:"link"`
		TeamName string `json:"team_name"`
		Slug     string `json:"slug"`
		Email    string `json:"email"`
	}
	if err := json.Unmarshal(reqs[0].Payload, &p); err != nil || p.Link != "https://console.beta.lekalo.studio/" ||
		p.TeamName != "Atelier Anna" || p.Slug != "atelier-anna" || p.Email != anna {
		t.Fatalf("team_request payload = %s, %v", reqs[0].Payload, err)
	}
	// A second click on the link answers the same and writes no second operator letter.
	again, err := f.svc.VerifyEmail(ctx, &auth.VerifyEmailRequest{Token: link})
	if err != nil || again.GetTeamSlug() != "atelier-anna" || again.GetHandoffCode() != "" || !again.GetAwaitingApproval() {
		t.Fatalf("second verify = %v, %v", again, err)
	}
	if n := len(f.teamLetters("team_request")); n != 1 {
		t.Fatalf("team_request letters after a second click = %d", n)
	}

	// Verify by code goes the same way.
	bcode, _ := signUp(bob, "Bob Studio", "bob-studio")
	bv, err := f.svc.VerifyEmail(ctx, &auth.VerifyEmailRequest{Email: bob, Code: bcode})
	if err != nil || bv.GetHandoffCode() != "" || !bv.GetAwaitingApproval() {
		t.Fatalf("verify by code under approval = %v, %v", bv, err)
	}
	if n := len(f.teamLetters("team_request")); n != 2 {
		t.Fatalf("team_request letters after bob = %d", n)
	}

	// The root list marks the team; a hand-off into it is refused with the reason.
	sctx := ctxFrom("10.0.1.2", "")
	sr, err := f.svc.SignIn(sctx, &auth.SignInRequest{Login: anna, Password: pw, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil || len(sr.GetTeams()) != 1 {
		t.Fatalf("root sign-in = %v, %v", sr, err)
	}
	if ref := sr.GetTeams()[0]; ref.GetSlug() != "atelier-anna" || ref.GetReady() || !ref.GetAwaitingApproval() {
		t.Fatalf("team ref = %v", ref)
	}
	_, err = f.svc.IssueHandoff(sctx, sr.GetToken(), &auth.IssueHandoffRequest{TeamSlug: "atelier-anna"})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "team_awaiting_approval" {
		t.Fatalf("hand-off into a team in review = %v", err)
	}
	// Someone else hears the generic no-access, as for any team that is not theirs.
	br, err := f.svc.SignIn(sctx, &auth.SignInRequest{Login: bob, Password: pw, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil {
		t.Fatal(err)
	}
	_, e1 := f.svc.IssueHandoff(sctx, br.GetToken(), &auth.IssueHandoffRequest{TeamSlug: "atelier-anna"})
	_, e2 := f.svc.IssueHandoff(sctx, br.GetToken(), &auth.IssueHandoffRequest{TeamSlug: "no-such-team"})
	wantCode(t, e1, codes.PermissionDenied, "hand-off into another's team in review")
	if e2 == nil || e1.Error() != e2.Error() {
		t.Fatalf("no-access answers differ: %v / %v", e1, e2)
	}
	// On the team's own host the review slug is an unknown team.
	_, eh := f.svc.SignIn(ctxFrom("10.0.1.3", "atelier-anna"), &auth.SignInRequest{Login: anna, Password: pw, Target: auth.SignInTarget_SIGN_IN_TARGET_TEAM})
	_, eu := f.svc.SignIn(ctxFrom("10.0.1.3", "no-such-team"), &auth.SignInRequest{Login: anna, Password: pw, Target: auth.SignInTarget_SIGN_IN_TARGET_TEAM})
	wantCode(t, eh, codes.Unauthenticated, "team sign-in on a review slug")
	if eu == nil || eh.Error() != eu.Error() {
		t.Fatalf("team-host answers differ: %v / %v", eh, eu)
	}
	// The address stays held while the team waits.
	_, err = f.svc.SignUp(ctx, &auth.SignUpRequest{Email: "delivered+b3x@resend.dev", Password: pw, TeamName: "X", TeamSlug: "atelier-anna", TermsAccepted: true})
	wantCode(t, err, codes.AlreadyExists, "sign-up on an address in review")

	// Approval (the console's transaction): review → provisioning plus the team_approved letter to the owner.
	tx, err := f.st.DB().Beginx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE team SET status = 'provisioning' WHERE id = ? AND status = 'review'`, team.ID); err != nil {
		t.Fatal(err)
	}
	if err := platformstore.TeamApprovedLetterTx(context.Background(), tx, team.ID, "https://atelier-anna.beta.lekalo.studio/", f.clock); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ok := f.teamLetters("team_approved")
	if len(ok) != 1 || ok[0].To != anna {
		t.Fatalf("team_approved letters = %+v", ok)
	}
	p.Email = ""
	if err := json.Unmarshal(ok[0].Payload, &p); err != nil || p.Link != "https://atelier-anna.beta.lekalo.studio/" ||
		p.Slug != "atelier-anna" || p.TeamName != "Atelier Anna" || p.Email != "" {
		t.Fatalf("team_approved payload = %s, %v", ok[0].Payload, err)
	}
	// After approval a second click on the old link is an ordinary used link.
	_, err = f.svc.VerifyEmail(ctx, &auth.VerifyEmailRequest{Token: link})
	wantCode(t, err, codes.InvalidArgument, "old link after approval")

	var stray int
	if err := f.st.DB().Get(&stray, `SELECT COUNT(*) FROM platform_email WHERE to_email NOT LIKE 'delivered+%@resend.dev'`); err != nil || stray != 0 {
		t.Fatalf("letters outside the test inbox: %d, %v", stray, err)
	}
}
