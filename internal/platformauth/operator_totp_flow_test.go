package platformauth

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
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
	// An operator team session opened from the console session dies with it.
	if _, err := f.st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud, parent_sid)
SELECT 'sopchild000000000000000000000000', 701, NULL, 'team', sid FROM user_session WHERE user_id = 701 AND aud = 'console'`); err != nil {
		t.Fatal(err)
	}
	sids, err := f.st.ResetOperatorSecondStep(ctx, totpOp, "other@x.io", f.clock)
	if err != nil || len(sids) != 2 {
		t.Fatalf("reset: %v (%d sessions)", err, len(sids))
	}
	if n := f.count(`SELECT COUNT(*) FROM user_session WHERE user_id = 701 AND revoked_at IS NULL`); n != 0 {
		t.Fatalf("%d console sessions live after the reset", n)
	}
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
