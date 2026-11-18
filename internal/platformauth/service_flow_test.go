package platformauth

import (
	"context"
	"database/sql"
	"encoding/json"
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
}

type flow struct {
	t     *testing.T
	st    *platformstore.Store
	svc   *Service
	teams *fakeTeams
	clock time.Time
}

func newFlow(t *testing.T) *flow {
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
	f := &flow{t: t, st: st, teams: &fakeTeams{ensured: map[string][]entity.AdminPermission{}, pwSet: map[string]string{}},
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
				return TeamAccount{Super: perms == nil, Perms: perms}, ok, nil
			},
			EnsureMember: func(_ context.Context, _ tenant.Team, handle string, super bool, perms []entity.AdminPermission) error {
				f.teams.mu.Lock()
				defer f.teams.mu.Unlock()
				if super {
					perms = nil
				} else if perms == nil {
					perms = []entity.AdminPermission{}
				}
				f.teams.ensured[handle] = perms
				return nil
			},
			TeamOnePassword: func(_ context.Context, handle, hash string) error {
				f.teams.mu.Lock()
				defer f.teams.mu.Unlock()
				f.teams.pwSet[handle] = hash
				return nil
			},
		},
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

	// Sign-up: {} and a letter with a code and a link, written to the outbox.
	if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: anna, Password: pw, TeamName: "Atelier Anna", TeamSlug: "atelier-anna"}); err != nil {
		t.Fatal(err)
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
	_, e1 := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: "delivered+b3x@resend.dev", Password: pw, TeamName: "X", TeamSlug: "atelier-anna"})
	_, e2 := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: anna, Password: "other-password", TeamName: "X", TeamSlug: "atelier-anna"})
	wantCode(t, e1, codes.AlreadyExists, "stranger on a taken address")
	if e2 == nil || e1.Error() != e2.Error() {
		t.Fatalf("taken-address answers differ: %v / %v", e1, e2)
	}
	// A stranger cannot re-key anna's sign-up in progress: {} and nothing changes, no letter.
	var hashBefore string
	if err := f.st.DB().Get(&hashBefore, `SELECT password_hash FROM platform_user WHERE email = ?`, anna); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SignUp(ctx, &auth.SignUpRequest{Email: anna, Password: "attacker-password", TeamName: "Evil", TeamSlug: "evil-anna"}); err != nil {
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
	_, err := f.svc.VerifyEmail(cctx, &auth.VerifyEmailRequest{Email: anna, Code: code})
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
	ai, err := f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: btok, Password: "bob-password-1", Handle: "bob.k"})
	if err != nil || ai.GetTeamSlug() != "atelier-anna" || ai.GetHandoffCode() == "" {
		t.Fatalf("accept = %v, %v", ai, err)
	}
	if p := f.teams.ensured["bob.k"]; len(p) != 1 || p[0].Section != "products" || p[0].Access != entity.AccessWrite {
		t.Fatalf("team admins row of bob = %v", p)
	}
	_, err = f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: btok, Password: "bob-password-1", Handle: "bob.k"})
	wantCode(t, err, codes.InvalidArgument, "invite reused")
	// A suspended team refuses new members.
	ctok := invite("delivered+b3cara@resend.dev")
	if _, err := f.st.DB().Exec(`UPDATE team SET status = 'suspended' WHERE id = ?`, teamID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.AcceptInvite(ctx, &auth.AcceptInviteRequest{Token: ctok, Password: "cara-password-1", Handle: "cara"})
	wantCode(t, err, codes.FailedPrecondition, "suspended team")

	// Operators: the env list creates the operator with an unusable password.
	if err := f.svc.SyncOperators(ctx); err != nil {
		t.Fatal(err)
	}
	var op struct {
		Op   bool   `db:"is_operator"`
		Hash string `db:"password_hash"`
	}
	if err := f.st.DB().Get(&op, `SELECT is_operator, password_hash FROM platform_user WHERE email = 'delivered+b3op@resend.dev'`); err != nil || !op.Op || !strings.HasPrefix(op.Hash, "!unusable$") {
		t.Fatalf("operator = %+v, %v", op, err)
	}

	// Every letter went to the test inbox only.
	var stray int
	if err := f.st.DB().Get(&stray, `SELECT COUNT(*) FROM platform_email WHERE to_email NOT LIKE 'delivered+%@resend.dev'`); err != nil || stray != 0 {
		t.Fatalf("letters outside the test inbox: %d, %v", stray, err)
	}
}
