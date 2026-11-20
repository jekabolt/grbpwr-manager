package platformauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

// Password, devices and Google connect from the profile (P0 §3.3, §3.4, §3.7), end to end on the
// control plane.

const (
	secMacUA   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	secPhoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"
	secWinUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0"

	secPhoneSID      = "profannaphone0000000000000000000"
	secLaptopSID     = "profannalaptop000000000000000000"
	secLaptopTeamSID = "profannalaptopteam00000000000000"
	secNewPassword   = "anna-new-password-2"
)

// seedSecurity is seedProfiles with accounts made a day ago (a Google connect within seconds of an
// account reads as a provider sign-up) and anna on three devices: this one (her root session and the
// team session handed off from it), a phone (a team session on its own) and a laptop (root + team).
func seedSecurity(t *testing.T, f *flow) {
	t.Helper()
	seedProfiles(t, f)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE platform_user SET created_at = NOW(3) - INTERVAL 1 DAY WHERE id IN (301, 302)`, nil},
		{`UPDATE user_session SET parent_sid = ?, ip = '10.9.0.1' WHERE sid = ?`, []any{profAnnaRootSID, profAnnaTeamSID}},
		{`UPDATE user_session SET user_agent = ?, ip = '10.9.0.1' WHERE sid = ?`, []any{secMacUA, profAnnaRootSID}},
		{`INSERT INTO user_session (sid, user_id, team_id, aud, parent_sid, user_agent, ip, last_seen_at) VALUES
 (?, 301, 30, 'team', NULL, ?, '10.9.0.7', NOW(3) - INTERVAL 1 MINUTE),
 (?, 301, NULL, 'root', NULL, ?, '10.9.0.8', NOW(3) - INTERVAL 2 MINUTE),
 (?, 301, 30, 'team', ?, NULL, '10.9.0.9', NOW(3) - INTERVAL 2 MINUTE)`,
			[]any{secPhoneSID, secPhoneUA, secLaptopSID, secWinUA, secLaptopTeamSID, secLaptopSID}},
	} {
		if _, err := f.st.DB().Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *flow) revokedSIDs(sids ...string) int {
	f.t.Helper()
	n := 0
	for _, s := range sids {
		var c int
		if err := f.st.DB().Get(&c, `SELECT COUNT(*) FROM user_session WHERE sid = ? AND revoked_at IS NOT NULL`, s); err != nil {
			f.t.Fatal(err)
		}
		n += c
	}
	return n
}

func TestDevicesFlow(t *testing.T) {
	f := newFlow(t)
	seedSecurity(t, f)
	alpha := ctxFrom("10.9.0.1", "prof-alpha")
	anna := profToken(t, f, 301, "prof-anna", profAnnaTeamSID, 30, false)
	annaRoot := profToken(t, f, 301, "prof-anna", profAnnaRootSID, 0, false)
	boris := profToken(t, f, 302, "prof-boris", profBorisTeamSID, 30, false)

	// The list: this device first (browser and system of its root session), then the most recent.
	ls, err := f.svc.ListMySessions(alpha, anna, &auth.ListMySessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	type view struct {
		browser, os, ip string
		this            bool
	}
	want := []view{{"chrome", "macos", "10.9.0.1", true}, {"safari", "ios", "10.9.0.7", false}, {"firefox", "windows", "10.9.0.9", false}}
	if len(ls.GetSessions()) != len(want) {
		t.Fatalf("%d devices, want %d", len(ls.GetSessions()), len(want))
	}
	for i, d := range ls.GetSessions() {
		if v := (view{d.GetBrowser(), d.GetOs(), d.GetIp(), d.GetThisDevice()}); v != want[i] || len(d.GetKey()) != 16 {
			t.Fatalf("device %d = %+v (key %q), want %+v", i, v, d.GetKey(), want[i])
		}
	}
	thisKey, phoneKey := ls.GetSessions()[0].GetKey(), ls.GetSessions()[1].GetKey()
	// The root token sees the same device as this one.
	if lr, err := f.svc.ListMySessions(ctxFrom("10.9.0.1", ""), annaRoot, &auth.ListMySessionsRequest{}); err != nil ||
		len(lr.GetSessions()) != 3 || lr.GetSessions()[0].GetKey() != thisKey || !lr.GetSessions()[0].GetThisDevice() {
		t.Fatalf("root list = %v, %v", lr, err)
	}

	// Sign out one device: not this one, not a key of someone else's device, not garbage.
	_, err = f.svc.RevokeMySession(alpha, anna, &auth.RevokeMySessionRequest{Key: thisKey})
	wantKey(t, err, codes.FailedPrecondition, "this_device", "revoke this device")
	bl, err := f.svc.ListMySessions(alpha, boris, &auth.ListMySessionsRequest{})
	if err != nil || len(bl.GetSessions()) != 1 {
		t.Fatalf("boris list = %v, %v", bl, err)
	}
	for _, key := range []string{bl.GetSessions()[0].GetKey(), "0123456789abcdef", "", "zz"} {
		_, err = f.svc.RevokeMySession(alpha, anna, &auth.RevokeMySessionRequest{Key: key})
		wantKey(t, err, codes.NotFound, "not_found", "revoke "+key)
	}
	if f.revokedSIDs(profBorisTeamSID) != 0 {
		t.Fatal("anna ended boris's session")
	}
	// A connect in flight dies with a single-device sign-out.
	if _, err := f.st.DB().Exec(`INSERT INTO oauth_link_ticket (ticket_hash, user_id, expires_at) VALUES (REPEAT('9', 64), 301, NOW(3) + INTERVAL 1 MINUTE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RevokeMySession(alpha, anna, &auth.RevokeMySessionRequest{Key: phoneKey}); err != nil {
		t.Fatal(err)
	}
	if f.revokedSIDs(secPhoneSID) != 1 || f.revokedSIDs(secLaptopSID, secLaptopTeamSID, profAnnaRootSID, profAnnaTeamSID) != 0 {
		t.Fatal("revoke one ended the wrong sessions")
	}
	var open int
	if err := f.st.DB().Get(&open, `SELECT COUNT(*) FROM oauth_link_ticket WHERE user_id = 301 AND used_at IS NULL`); err != nil || open != 0 {
		t.Fatalf("connect tickets still open after a device sign-out: %d %v", open, err)
	}
	_, err = f.svc.RevokeMySession(alpha, anna, &auth.RevokeMySessionRequest{Key: phoneKey})
	wantKey(t, err, codes.NotFound, "not_found", "revoke the same device again")

	// Sign out the others: the laptop (root and its team session); this device stays.
	ro, err := f.svc.RevokeMyOtherSessions(alpha, anna, &auth.RevokeMyOtherSessionsRequest{})
	if err != nil || ro.GetSignedOut() != 1 {
		t.Fatalf("revoke others = %v, %v", ro, err)
	}
	if f.revokedSIDs(secLaptopSID, secLaptopTeamSID) != 2 || f.revokedSIDs(profAnnaRootSID, profAnnaTeamSID) != 0 {
		t.Fatal("revoke others ended the wrong sessions")
	}
	if ls, err := f.svc.ListMySessions(alpha, anna, &auth.ListMySessionsRequest{}); err != nil || len(ls.GetSessions()) != 1 {
		t.Fatalf("list after revoke others = %v, %v", ls, err)
	}

	// Log out with device: the team token ends its whole chain (the root too); boris is untouched.
	if _, err := f.svc.SignOut(alpha, anna, true); err != nil {
		t.Fatal(err)
	}
	if f.revokedSIDs(profAnnaRootSID, profAnnaTeamSID) != 2 || f.revokedSIDs(profBorisTeamSID) != 0 {
		t.Fatal("log out did not end this device")
	}
	_, err = f.svc.ListMySessions(alpha, anna, &auth.ListMySessionsRequest{})
	wantCode(t, err, codes.Unauthenticated, "list after log out")

	// Log out without device ends the one session only.
	borisRootSID := "profborisroot0000000000000000000"
	if _, err := f.st.DB().Exec(`UPDATE user_session SET parent_sid = ? WHERE sid = ?`, borisRootSID, profBorisTeamSID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`INSERT INTO user_session (sid, user_id, aud) VALUES (?, 302, 'root')`, borisRootSID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SignOut(alpha, boris, false); err != nil {
		t.Fatal(err)
	}
	if f.revokedSIDs(profBorisTeamSID) != 1 || f.revokedSIDs(borisRootSID) != 0 {
		t.Fatal("log out without device ended more than its session")
	}
}

// A new session records the browser and the client IP of the request that opened it.
func TestSessionRecordsDevice(t *testing.T) {
	f := newFlow(t)
	seedProfiles(t, f)
	ctx := metadata.NewIncomingContext(ctxFrom("10.9.3.3", ""), metadata.Pairs(userAgentHeader, secPhoneUA))
	si, err := f.svc.SignIn(ctx, &auth.SignInRequest{Login: "prof-anna", Password: profPassword, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil || si.GetToken() == "" {
		t.Fatalf("sign in = %v, %v", si, err)
	}
	var row struct {
		UA string `db:"user_agent"`
		IP string `db:"ip"`
	}
	if err := f.st.DB().Get(&row, `SELECT COALESCE(user_agent, '') AS user_agent, COALESCE(ip, '') AS ip FROM user_session
WHERE user_id = 301 AND sid NOT IN (?, ?)`, profAnnaTeamSID, profAnnaRootSID); err != nil {
		t.Fatal(err)
	}
	if row.UA != secPhoneUA || row.IP != "10.9.3.3" {
		t.Fatalf("recorded %+v", row)
	}
}

func TestPasswordFlow(t *testing.T) {
	f := newFlow(t)
	seedSecurity(t, f)
	alpha := ctxFrom("10.9.0.1", "prof-alpha")
	anna := profToken(t, f, 301, "prof-anna", profAnnaTeamSID, 30, false)
	boris := profToken(t, f, 302, "prof-boris", profBorisTeamSID, 30, false)

	_, err := f.svc.ChangeMyPassword(alpha, anna, &auth.ChangeMyPasswordRequest{CurrentPassword: profPassword, NewPassword: "short"})
	wantKey(t, err, codes.InvalidArgument, "weak_password", "short new password")
	_, err = f.svc.ChangeMyPassword(alpha, anna, &auth.ChangeMyPasswordRequest{CurrentPassword: "not-the-password", NewPassword: secNewPassword})
	wantKey(t, err, codes.InvalidArgument, "wrong_password", "wrong current password")
	_, err = f.svc.ChangeMyPassword(alpha, anna, &auth.ChangeMyPasswordRequest{NewPassword: secNewPassword})
	wantKey(t, err, codes.InvalidArgument, "wrong_password", "no current password on an account with one")

	// Change with sign out others: two other devices end, this one stays signed in; the edge drops
	// the ended sessions; the old password no longer signs in, the new one does.
	cp, err := f.svc.ChangeMyPassword(alpha, anna, &auth.ChangeMyPasswordRequest{CurrentPassword: profPassword, NewPassword: secNewPassword, SignOutOthers: true})
	if err != nil || cp.GetSignedOut() != 2 || !cp.GetPasswordChangedAt().AsTime().Equal(f.clock) {
		t.Fatalf("change = %v, %v", cp, err)
	}
	if f.revokedSIDs(secPhoneSID, secLaptopSID, secLaptopTeamSID) != 3 || f.revokedSIDs(profAnnaRootSID, profAnnaTeamSID) != 0 {
		t.Fatal("sign out others ended the wrong sessions")
	}
	if len(f.teams.dropped["prof-anna"]) != 3 {
		t.Fatalf("edge dropped %v", f.teams.dropped["prof-anna"])
	}
	p, err := f.svc.GetMyProfile(alpha, anna, &auth.GetMyProfileRequest{})
	if err != nil || !p.GetProfile().GetPasswordSet() || !p.GetProfile().GetPasswordChangedAt().AsTime().Equal(f.clock) {
		t.Fatalf("profile after change = %v, %v", p, err)
	}
	_, err = f.svc.SignIn(ctxFrom("10.9.1.2", ""), &auth.SignInRequest{Login: "prof-anna", Password: profPassword, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	wantCode(t, err, codes.Unauthenticated, "sign in with the old password")
	if si, err := f.svc.SignIn(ctxFrom("10.9.1.2", ""), &auth.SignInRequest{Login: "prof-anna", Password: secNewPassword, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT}); err != nil || si.GetToken() == "" {
		t.Fatalf("sign in with the new password = %v, %v", si, err)
	}

	// An account without a password of its own: password_set false; a current password is refused;
	// an empty one sets it, and from then on it is a change.
	if p, err := f.svc.GetMyProfile(alpha, boris, &auth.GetMyProfileRequest{}); err != nil || p.GetProfile().GetPasswordSet() {
		t.Fatalf("boris profile = %v, %v", p, err)
	}
	_, err = f.svc.ChangeMyPassword(alpha, boris, &auth.ChangeMyPasswordRequest{CurrentPassword: "anything", NewPassword: secNewPassword})
	wantKey(t, err, codes.InvalidArgument, "wrong_password", "current password on an account without one")
	if cp, err := f.svc.ChangeMyPassword(alpha, boris, &auth.ChangeMyPasswordRequest{NewPassword: secNewPassword}); err != nil || cp.GetSignedOut() != 0 {
		t.Fatalf("set = %v, %v", cp, err)
	}
	if p, err := f.svc.GetMyProfile(alpha, boris, &auth.GetMyProfileRequest{}); err != nil || !p.GetProfile().GetPasswordSet() {
		t.Fatalf("boris profile after set = %v, %v", p, err)
	}
	_, err = f.svc.ChangeMyPassword(alpha, boris, &auth.ChangeMyPasswordRequest{NewPassword: "another-password-3"})
	wantKey(t, err, codes.InvalidArgument, "wrong_password", "a second set without the current password")

	// Throttled per account.
	var last error
	for range MaxPasswordChangesPerHr + 1 {
		_, last = f.svc.ChangeMyPassword(alpha, boris, &auth.ChangeMyPasswordRequest{CurrentPassword: "wrong-wrong-1", NewPassword: "another-password-3"})
	}
	wantKey(t, last, codes.ResourceExhausted, "too_many_attempts", "password change flood")
}

func TestGoogleConnectFlow(t *testing.T) {
	f := newFlow(t)
	seedSecurity(t, f)
	alpha := ctxFrom("10.9.0.1", "prof-alpha")
	anna := profToken(t, f, 301, "prof-anna", profAnnaTeamSID, 30, false)
	annaRoot := profToken(t, f, 301, "prof-anna", profAnnaRootSID, 0, false)
	boris := profToken(t, f, 302, "prof-boris", profBorisTeamSID, 30, false)

	_, err := f.svc.StartGoogleLink(alpha, anna, &auth.StartGoogleLinkRequest{})
	wantKey(t, err, codes.FailedPrecondition, "google_off", "connect with Google off")
	f.svc.options = fixedOptions{google: true}

	// A team token's ticket returns to its team; single use.
	sl, err := f.svc.StartGoogleLink(alpha, anna, &auth.StartGoogleLinkRequest{})
	if err != nil || sl.GetTicket() == "" {
		t.Fatalf("start = %v, %v", sl, err)
	}
	uid, team, err := f.svc.ConsumeLinkTicket(context.Background(), sl.GetTicket())
	if err != nil || uid != 301 || team.Slug != "prof-alpha" {
		t.Fatalf("consume = %d %+v %v", uid, team, err)
	}
	if _, _, err := f.svc.ConsumeLinkTicket(context.Background(), sl.GetTicket()); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("second consume = %v", err)
	}
	// A root token's ticket returns to the root host.
	sr, err := f.svc.StartGoogleLink(ctxFrom("10.9.0.1", ""), annaRoot, &auth.StartGoogleLinkRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if uid, team, err := f.svc.ConsumeLinkTicket(context.Background(), sr.GetTicket()); err != nil || uid != 301 || team.Slug != "" {
		t.Fatalf("root consume = %d %+v %v", uid, team, err)
	}

	google := func(subject, email string) platformstore.ProviderSignIn {
		return platformstore.ProviderSignIn{Provider: platformstore.ProviderGoogle, Subject: subject, Email: email,
			EmailVerified: true, EmailAuthoritative: true}
	}
	started := f.clock.Add(-time.Minute)
	if key := f.svc.CompleteGoogleLink(context.Background(), 301, started, google("g-anna", "Anna.Other@gmail.com")); key != KeyLinked {
		t.Fatalf("complete = %q", key)
	}
	p, err := f.svc.GetMyProfile(alpha, anna, &auth.GetMyProfileRequest{})
	if err != nil || p.GetProfile().GetGoogleEmail() != "anna.other@gmail.com" || !p.GetProfile().GetPasswordSet() {
		t.Fatalf("profile after connect = %v, %v", p, err)
	}
	_, err = f.svc.StartGoogleLink(alpha, anna, &auth.StartGoogleLinkRequest{})
	wantKey(t, err, codes.AlreadyExists, "already_linked", "connect twice")
	if key := f.svc.CompleteGoogleLink(context.Background(), 302, started, google("g-anna", "anna.other@gmail.com")); key != KeyTaken {
		t.Fatalf("someone else's Google = %q", key)
	}
	if key := f.svc.CompleteGoogleLink(context.Background(), 302, started, platformstore.ProviderSignIn{
		Provider: platformstore.ProviderGoogle, Subject: "g-boris", Email: "boris@corp.example", EmailVerified: true}); key != KeyUnverified {
		t.Fatalf("unverified = %q", key)
	}

	// Disconnect: anna has a password, so it goes; boris's Google is his only way in.
	if _, err := f.svc.UnlinkMyGoogle(alpha, anna, &auth.UnlinkMyGoogleRequest{}); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.svc.GetMyProfile(alpha, anna, &auth.GetMyProfileRequest{}); p.GetProfile().GetGoogleEmail() != "" {
		t.Fatal("still connected after disconnect")
	}
	if key := f.svc.CompleteGoogleLink(context.Background(), 302, started, google("g-boris", "boris@gmail.com")); key != KeyLinked {
		t.Fatalf("boris connect = %q", key)
	}
	_, err = f.svc.UnlinkMyGoogle(alpha, boris, &auth.UnlinkMyGoogleRequest{})
	wantKey(t, err, codes.FailedPrecondition, "only_sign_in", "disconnect the only way in")
	if p, _ := f.svc.GetMyProfile(alpha, boris, &auth.GetMyProfileRequest{}); p.GetProfile().GetGoogleEmail() != "boris@gmail.com" {
		t.Fatal("a refused disconnect removed Google")
	}

	// A password change ends a connect started before it.
	if _, err := f.svc.ChangeMyPassword(alpha, anna, &auth.ChangeMyPasswordRequest{CurrentPassword: profPassword, NewPassword: secNewPassword}); err != nil {
		t.Fatal(err)
	}
	if key := f.svc.CompleteGoogleLink(context.Background(), 301, started, google("g-anna", "anna.other@gmail.com")); key != KeyExpired {
		t.Fatalf("connect older than the password change = %q", key)
	}
}
