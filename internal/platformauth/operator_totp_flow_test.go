package platformauth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"github.com/pquerna/otp/totp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const totpOp, totpOpPW = "delivered+b3op@resend.dev", "Op-Totp-Pass-5521"

// newTotpFlow is the flow with one listed operator and limiters wide enough for a long story.
func newTotpFlow(t *testing.T) *flow {
	t.Helper()
	f := newFlow(t)
	// The story moves the clock forward by up to ~20 minutes; tokens are minted at the clock, so it
	// starts in the past to keep their iat valid against the wall clock.
	f.clock = f.clock.Add(-30 * time.Minute)
	hash, err := f.svc.d.Hasher.HashPassword(totpOpPW)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator, email_verified_at)
VALUES (701, ?, 'op7', ?, 'active', 1, UTC_TIMESTAMP(3))`, totpOp, hash); err != nil {
		t.Fatal(err)
	}
	f.svc.signInLogin = ratelimit.NewLimiter(time.Minute, 1000)
	f.svc.signInIP = ratelimit.NewLimiter(time.Minute, 1000)
	f.svc.codeIP = ratelimit.NewLimiter(time.Hour, 1000)
	f.svc.d.Config.OperatorSecondStep = "on"
	return f
}

func (f *flow) consoleSignIn() *auth.SignInResponse {
	f.t.Helper()
	si, err := f.svc.SignIn(ctxFrom("10.0.7.1", ""), &auth.SignInRequest{Login: totpOp, Password: totpOpPW, Target: auth.SignInTarget_SIGN_IN_TARGET_CONSOLE})
	if err != nil {
		f.t.Fatalf("console sign-in: %v", err)
	}
	if si.GetToken() != "" || si.GetTicket() == "" {
		f.t.Fatalf("console sign-in answered a token or no ticket: token %t ticket %t", si.GetToken() != "", si.GetTicket() != "")
	}
	return si
}

func (f *flow) setupCode() string {
	f.t.Helper()
	var payload []byte
	if err := f.st.DB().Get(&payload, `SELECT payload FROM platform_email WHERE to_email = ? AND template = 'operator_setup' ORDER BY id DESC LIMIT 1`, totpOp); err != nil {
		f.t.Fatalf("no setup letter: %v", err)
	}
	var p struct{ Code string }
	if err := json.Unmarshal(payload, &p); err != nil || p.Code == "" {
		f.t.Fatalf("setup letter without a code: %v", err)
	}
	return p.Code
}

func appCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wantStatus(t *testing.T, err error, c codes.Code, msg, what string) {
	t.Helper()
	if s := status.Convert(err); s.Code() != c || s.Message() != msg {
		t.Fatalf("%s: got %v, want %v %q", what, err, c, msg)
	}
}

// Gap 4 end to end: password → setup (mailed code → key → app code) → recovery codes once → a
// console token with the second step; the next sign-in needs an app code, a code is never taken
// twice, a recovery code works once, the alerts go out.
func TestOperatorTOTPSetupAndVerify(t *testing.T) {
	f := newTotpFlow(t)
	ctx := ctxFrom("10.0.7.2", "")

	si := f.consoleSignIn()
	if si.GetSecondStep() != auth.OperatorSecondStep_OPERATOR_SECOND_STEP_SETUP || !si.GetSetupLetterSent() {
		t.Fatalf("first sign-in: step %v letter %t", si.GetSecondStep(), si.GetSetupLetterSent())
	}
	if n := f.count(`SELECT COUNT(*) FROM user_session WHERE user_id = 701`); n != 0 {
		t.Fatalf("a password alone opened %d sessions", n)
	}
	// The key is never shown before the mailed code (setup hijack with the password alone).
	_, err := f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: "123456"})
	wantStatus(t, err, codes.FailedPrecondition, "sign_in_again", "confirm before the mailed code")

	si = f.consoleSignIn()
	_, err = f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: si.GetTicket(), Code: wrong(f.setupCode())})
	wantCode(t, err, codes.InvalidArgument, "wrong mailed code")
	sm, err := f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: si.GetTicket(), Code: f.setupCode()})
	if err != nil || sm.GetSecret() == "" || sm.GetOtpauthUrl() == "" {
		t.Fatalf("setup mail: %v", err)
	}
	if _, err := f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: si.GetTicket(), Code: f.setupCode()}); err == nil {
		t.Fatal("the key was answered twice")
	}
	secret := sm.GetSecret()
	_, err = f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: wrong(appCode(t, secret, f.clock))})
	wantCode(t, err, codes.InvalidArgument, "wrong app code at setup")
	sc, err := f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: appCode(t, secret, f.clock)})
	if err != nil || len(sc.GetRecoveryCodes()) != 10 {
		t.Fatalf("setup confirm: %v (%d codes)", err, len(sc.GetRecoveryCodes()))
	}
	c, err := jwt.VerifyTeamToken(f.svc.d.JWT, sc.GetToken(), nil)
	if err != nil || !c.SecondStep || c.Audience != jwt.AudConsole || c.UserID != 701 || c.SessionID == "" {
		t.Fatalf("console token %+v %v", c, err)
	}
	if n := f.count(`SELECT COUNT(*) FROM operator_recovery_code WHERE user_id = 701 AND code_hash LIKE '%-%'`); n != 0 {
		t.Fatal("a recovery code is stored in the clear")
	}
	if n := f.count(`SELECT COUNT(*) FROM operator_totp WHERE user_id = 701 AND secret_sealed = ?`, secret); n != 0 {
		t.Fatal("the app secret is stored in the clear")
	}
	if n := f.count(`SELECT COUNT(*) FROM platform_email WHERE template = 'operator_alert' AND JSON_EXTRACT(payload, '$.event') = 'setup'`); n != 1 {
		t.Fatalf("setup alerts %d", n)
	}
	// The setup ticket is spent.
	_, err = f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: appCode(t, secret, f.clock)})
	wantStatus(t, err, codes.FailedPrecondition, "sign_in_again", "setup ticket reused")

	// Next sign-in: an app code. The code that confirmed setup is not taken again (same 30 s).
	si = f.consoleSignIn()
	if si.GetSecondStep() != auth.OperatorSecondStep_OPERATOR_SECOND_STEP_CODE {
		t.Fatalf("second sign-in step %v", si.GetSecondStep())
	}
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, secret, f.clock)})
	wantCode(t, err, codes.InvalidArgument, "replayed code")
	f.clock = f.clock.Add(30 * time.Second)
	v, err := f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, secret, f.clock)})
	if err != nil || v.GetToken() == "" {
		t.Fatalf("verify: %v", err)
	}
	if c, err := jwt.VerifyTeamToken(f.svc.d.JWT, v.GetToken(), nil); err != nil || !c.SecondStep {
		t.Fatalf("verify token %+v %v", c, err)
	}
	// The previous step (inside the ±1 window) is refused too: it is before the accepted one.
	si = f.consoleSignIn()
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, secret, f.clock.Add(-30*time.Second))})
	wantCode(t, err, codes.InvalidArgument, "older step")

	// A recovery code works once, typed loosely; the second use is refused; operators are told.
	rc := sc.GetRecoveryCodes()[0]
	v, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: " " + rc[:4] + " " + rc[5:] + " "})
	if err != nil || v.GetToken() == "" || v.GetRecoveryCodesLeft() != 9 {
		t.Fatalf("recovery: %v left %d", err, v.GetRecoveryCodesLeft())
	}
	si = f.consoleSignIn()
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: rc})
	wantCode(t, err, codes.InvalidArgument, "recovery code reused")
	if n := f.count(`SELECT COUNT(*) FROM platform_email WHERE template = 'operator_alert' AND JSON_EXTRACT(payload, '$.event') = 'recovery'`); n != 1 {
		t.Fatalf("recovery alerts %d", n)
	}
}

// Brute force: five wrong codes burn a ticket, ten in a row lock the account (even a right code),
// a new sign-in burns the older ticket; a password change kills a live ticket.
func TestOperatorTOTPLimits(t *testing.T) {
	f := newTotpFlow(t)
	ctx := ctxFrom("10.0.7.3", "")
	si := f.consoleSignIn()
	sm, err := f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: si.GetTicket(), Code: f.setupCode()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)}); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(time.Minute)
	bad := wrong(appCode(t, sm.GetSecret(), f.clock))

	// One live ticket per account.
	old := f.consoleSignIn()
	si = f.consoleSignIn()
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: old.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)})
	wantStatus(t, err, codes.FailedPrecondition, "sign_in_again", "older ticket")

	for i := range 5 {
		_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: bad})
		wantCode(t, err, codes.InvalidArgument, "wrong code")
		_ = i
	}
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)})
	wantStatus(t, err, codes.FailedPrecondition, "sign_in_again", "ticket after five wrong codes")

	si = f.consoleSignIn()
	for range 4 {
		_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: bad})
		wantCode(t, err, codes.InvalidArgument, "wrong code")
	}
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: bad})
	if err == nil {
		t.Fatal("tenth wrong code accepted")
	}
	si = f.consoleSignIn()
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)})
	wantStatus(t, err, codes.ResourceExhausted, "locked", "right code while locked")
	f.clock = f.clock.Add(16 * time.Minute)
	si = f.consoleSignIn()
	if _, err := f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)}); err != nil {
		t.Fatalf("after the lock: %v", err)
	}

	// A password change (cred_gen) kills a live ticket.
	f.clock = f.clock.Add(time.Minute)
	si = f.consoleSignIn()
	if _, err := f.st.DB().Exec(`UPDATE platform_user SET credential_gen = credential_gen + 1 WHERE id = 701`); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)})
	wantStatus(t, err, codes.FailedPrecondition, "sign_in_again", "ticket after a password change")
}

// Lost phone: the reset removes the app and recovery codes, ends console sessions and tells every
// operator; the next sign-in sets up again through the mailed code.
func TestOperatorTOTPReset(t *testing.T) {
	f := newTotpFlow(t)
	ctx := ctxFrom("10.0.7.4", "")
	si := f.consoleSignIn()
	sm, err := f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: si.GetTicket(), Code: f.setupCode()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)}); err != nil {
		t.Fatal(err)
	}
	// The shape the product writes: the console's EnterTeam issues a hand-off code from the console
	// session (no parent_sid), the team host exchanges it for a team session (no parent either).
	teamTok, spare := f.enterTeam(), f.issueEntry()
	sids, err := f.st.ResetOperatorSecondStep(ctx, totpOp, "other@x.io", f.clock)
	if err != nil || len(sids) != 2 {
		t.Fatalf("reset: %v (%d sessions)", err, len(sids))
	}
	if n := f.count(`SELECT COUNT(*) FROM user_session WHERE user_id = 701 AND revoked_at IS NULL`); n != 0 {
		t.Fatalf("%d operator sessions live after the reset", n)
	}
	f.wantSessionDead(teamTok, "team entry after the reset")
	_, err = f.svc.ExchangeHandoff(ctxFrom("10.0.9.1", "opteam"), &auth.ExchangeHandoffRequest{Code: spare})
	wantCode(t, err, codes.InvalidArgument, "unused entry code after the reset")
	if n := f.count(`SELECT COUNT(*) FROM operator_totp WHERE user_id = 701`) + f.count(`SELECT COUNT(*) FROM operator_recovery_code WHERE user_id = 701`); n != 0 {
		t.Fatal("the app or its recovery codes survived the reset")
	}
	if n := f.count(`SELECT COUNT(*) FROM platform_email WHERE template = 'operator_alert' AND JSON_EXTRACT(payload, '$.event') = 'reset'`); n != 1 {
		t.Fatalf("reset alerts %d", n)
	}
	if si := f.consoleSignIn(); si.GetSecondStep() != auth.OperatorSecondStep_OPERATOR_SECOND_STEP_SETUP {
		t.Fatalf("after the reset: step %v", si.GetSecondStep())
	}
	if _, err := f.st.ResetOperatorSecondStep(ctx, "nobody@x.io", "", f.clock); err == nil {
		t.Fatal("reset of a non-operator")
	}
}

// enterTeam does what the console's EnterTeam and the team host do: a hand-off code from the live
// console session of 701, exchanged on the team host. It returns the team token.
func (f *flow) enterTeam() string {
	f.t.Helper()
	ex, err := f.svc.ExchangeHandoff(ctxFrom("10.0.9.1", "opteam"), &auth.ExchangeHandoffRequest{Code: f.issueEntry()})
	if err != nil {
		f.t.Fatalf("team entry: %v", err)
	}
	return ex.GetToken()
}

// issueEntry issues an operator entry code to team opteam from 701's live console session, as
// EnterTeam (apisrv/platform/teams.go) does.
func (f *flow) issueEntry() string {
	f.t.Helper()
	if _, err := f.st.DB().Exec(`INSERT IGNORE INTO team (id, slug, name, db_name, status) VALUES (50, 'opteam', 'Op Team', 'lkb_t_opteam01', 'active')`); err != nil {
		f.t.Fatal(err)
	}
	var sid string
	if err := f.st.DB().Get(&sid, `SELECT sid FROM user_session WHERE user_id = 701 AND aud = 'console' AND revoked_at IS NULL
ORDER BY created_at DESC LIMIT 1`); err != nil {
		f.t.Fatalf("no live console session: %v", err)
	}
	code, hash, err := NewOperatorCode()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.IssueHandoff(context.Background(), 701, sid, platformstore.NewToken{
		TeamID: 50, TokenHash: hash, ExpiresAt: f.clock.Add(HandoffLongTTL),
	}, f.clock); err != nil {
		f.t.Fatalf("entry code: %v", err)
	}
	return code
}

// wantSessionDead: the token's session is revoked, so the edge (SessionActive) refuses the token.
func (f *flow) wantSessionDead(tok, what string) {
	f.t.Helper()
	c, err := jwt.VerifyTeamToken(f.svc.d.JWT, tok, nil)
	if err != nil {
		f.t.Fatalf("%s: token: %v", what, err)
	}
	ok, err := f.st.SessionActive(context.Background(), c.SessionID)
	if err != nil || ok {
		f.t.Fatalf("%s: session still live (%v)", what, err)
	}
}

// PLATFORM_OPERATOR_SECOND_STEP off: the console answers a token on the password as before, and the
// second-step RPCs refuse. Turning it on (boot) ends every console session, team entry and unused
// entry code issued meanwhile; a second boot with it on ends nothing; off then on ends them again.
func TestOperatorSecondStepSwitch(t *testing.T) {
	f := newTotpFlow(t)
	f.svc.d.Config.OperatorSecondStep = ""
	ctx := ctxFrom("10.0.7.4", "")
	si, err := f.svc.SignIn(ctxFrom("10.0.7.1", ""), &auth.SignInRequest{Login: totpOp, Password: totpOpPW, Target: auth.SignInTarget_SIGN_IN_TARGET_CONSOLE})
	if err != nil || si.GetToken() == "" || si.GetTicket() != "" {
		t.Fatalf("switch off: console sign-in %v (token %t ticket %t)", err, si.GetToken() != "", si.GetTicket() != "")
	}
	if c, err := jwt.VerifyTeamToken(f.svc.d.JWT, si.GetToken(), nil); err != nil || c.Audience != jwt.AudConsole || c.SecondStep {
		t.Fatalf("switch off: console claims %+v %v", c, err)
	}
	_, err = f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: "x", Code: "123456"})
	wantStatus(t, err, codes.FailedPrecondition, "second_step_off", "verify with the switch off")
	_, err = f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: "x", Code: "123456"})
	wantStatus(t, err, codes.FailedPrecondition, "second_step_off", "setup mail with the switch off")

	if _, on, err := f.st.ApplyOperatorSecondStepSwitch(ctx, false, f.clock); err != nil || on {
		t.Fatalf("boot with the switch off: %v %t", err, on)
	}
	teamTok, spare := f.enterTeam(), f.issueEntry()
	// A member's own team session is not an operator's and survives.
	if _, err := f.st.DB().Exec(`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES (702, 'mem@x.io', 'mem7', 'h', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('smember0000000000000000000000000', 702, 50, 'team')`); err != nil {
		t.Fatal(err)
	}

	sids, on, err := f.st.ApplyOperatorSecondStepSwitch(ctx, true, f.clock)
	if err != nil || !on || len(sids) != 2 {
		t.Fatalf("switch on: %v %t (%d sessions)", err, on, len(sids))
	}
	if n := f.count(`SELECT COUNT(*) FROM user_session WHERE user_id = 701 AND revoked_at IS NULL`); n != 0 {
		t.Fatalf("%d operator sessions live after the switch", n)
	}
	f.wantSessionDead(si.GetToken(), "console session after the switch")
	f.wantSessionDead(teamTok, "team entry after the switch")
	_, err = f.svc.ExchangeHandoff(ctxFrom("10.0.9.1", "opteam"), &auth.ExchangeHandoffRequest{Code: spare})
	wantCode(t, err, codes.InvalidArgument, "unused entry code after the switch")
	if ok, err := f.st.SessionActive(ctx, "smember0000000000000000000000000"); err != nil || !ok {
		t.Fatalf("a member's team session ended: %v", err)
	}

	// Again with it on: nothing more ends.
	f.svc.d.Config.OperatorSecondStep = "on"
	verifyApp(t, f)
	if sids, on, err := f.st.ApplyOperatorSecondStepSwitch(ctx, true, f.clock); err != nil || on || len(sids) != 0 {
		t.Fatalf("second boot with it on: %v %t %d", err, on, len(sids))
	}
	if n := f.count(`SELECT COUNT(*) FROM user_session WHERE user_id = 701 AND revoked_at IS NULL`); n != 1 {
		t.Fatalf("second boot ended sessions: %d live", n)
	}
	// Off, then on again: ends them again.
	if _, on, err := f.st.ApplyOperatorSecondStepSwitch(ctx, false, f.clock); err != nil || on {
		t.Fatalf("off: %v %t", err, on)
	}
	if sids, on, err := f.st.ApplyOperatorSecondStepSwitch(ctx, true, f.clock); err != nil || !on || len(sids) != 1 {
		t.Fatalf("on again: %v %t %d", err, on, len(sids))
	}
}

// verifyApp sets up 701's app and signs in through it (switch on): one live console session.
func verifyApp(t *testing.T, f *flow) string {
	t.Helper()
	ctx := ctxFrom("10.0.7.4", "")
	si := f.consoleSignIn()
	sm, err := f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: si.GetTicket(), Code: f.setupCode()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)}); err != nil {
		t.Fatal(err)
	}
	return sm.GetSecret()
}

// A rotated pepper: the sealed app no longer opens. The app and its recovery codes are dropped, the
// operator is told to sign in again, and the next sign-in is a first setup behind a mailed code.
func TestOperatorTOTPPepperRotated(t *testing.T) {
	f := newTotpFlow(t)
	ctx := ctxFrom("10.0.7.4", "")
	secret := verifyApp(t, f)
	f.svc.pepper += "-rotated"
	f.clock = f.clock.Add(time.Minute)
	si := f.consoleSignIn()
	if si.GetSecondStep() != auth.OperatorSecondStep_OPERATOR_SECOND_STEP_CODE {
		t.Fatalf("before: step %v", si.GetSecondStep())
	}
	_, err := f.svc.OperatorVerify(ctx, &auth.OperatorVerifyRequest{Ticket: si.GetTicket(), Code: appCode(t, secret, f.clock)})
	wantStatus(t, err, codes.FailedPrecondition, "sign_in_again", "app sealed under the old pepper")
	if n := f.count(`SELECT COUNT(*) FROM operator_totp WHERE user_id = 701`) + f.count(`SELECT COUNT(*) FROM operator_recovery_code WHERE user_id = 701`); n != 0 {
		t.Fatal("the unreadable app or its recovery codes survived")
	}
	si = f.consoleSignIn()
	if si.GetSecondStep() != auth.OperatorSecondStep_OPERATOR_SECOND_STEP_SETUP {
		t.Fatalf("after: step %v", si.GetSecondStep())
	}
	sm, err := f.svc.OperatorSetupMail(ctx, &auth.OperatorSetupMailRequest{Ticket: si.GetTicket(), Code: f.setupCode()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OperatorSetupConfirm(ctx, &auth.OperatorSetupConfirmRequest{Ticket: si.GetTicket(), Code: appCode(t, sm.GetSecret(), f.clock)}); err != nil {
		t.Fatalf("setup under the new pepper: %v", err)
	}
}

// An address removed from PLATFORM_OPERATOR_EMAILS loses its app: re-listed later, it sets one up anew.
func TestOperatorTOTPRemovedOperator(t *testing.T) {
	f := newTotpFlow(t)
	verifyApp(t, f)
	unusable := func() (string, error) { return "h", nil }
	if _, err := f.st.SyncOperators(context.Background(), []string{"someone@x.io"}, f.clock, unusable, HandleBase); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM operator_totp WHERE user_id = 701`) + f.count(`SELECT COUNT(*) FROM operator_recovery_code WHERE user_id = 701`); n != 0 {
		t.Fatal("a removed operator kept the app")
	}
	if _, err := f.st.SyncOperators(context.Background(), []string{totpOp}, f.clock, unusable, HandleBase); err != nil {
		t.Fatal(err)
	}
	if si := f.consoleSignIn(); si.GetSecondStep() != auth.OperatorSecondStep_OPERATOR_SECOND_STEP_SETUP {
		t.Fatalf("re-listed: step %v", si.GetSecondStep())
	}
}
