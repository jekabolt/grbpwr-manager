package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// Devices, password change, the «has a password» rule and Google connect in the store (P0 §3.3,
// §3.4, §3.7, §8).

const (
	s23Hash  = "PBKDF2$sha512$1000$c2FsdHNhbHRzYWx0c2FsdA==$a2V5a2V5a2V5a2V5a2V5a2V5"
	s23Hash2 = "PBKDF2$sha512$1000$b3RoZXJzYWx0b3RoZXJzYQ==$bmV3a2V5bmV3a2V5bmV3a2V5"
)

// sid pads a readable name to a sid (CHAR(32) under a NO PAD collation: zeros, not spaces).
func sid(name string) string { return name + strings.Repeat("0", 32-len(name)) }

func s23Live(now time.Time) platformstore.SessionLiveness {
	return platformstore.SessionLiveness{RootNotBefore: now.Add(-30 * 24 * time.Hour), TeamNotBefore: now.Add(-time.Hour)}
}

func s23Exec(t *testing.T, st *platformstore.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// seedDevices: account 401 with device A (root + two team sessions, one expired), device B (a team
// session on its own), device C (a root whose sessions are all revoked or expired), a console
// session; account 402 has its own device.
func seedDevices(t *testing.T, st *platformstore.Store, now time.Time) {
	t.Helper()
	s23Exec(t, st, `INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES
 (401, 'dev-anna@x.io', 'dev-anna', ?, 'active'), (402, 'dev-boris@x.io', 'dev-boris', ?, 'active')`, s23Hash, s23Hash)
	s23Exec(t, st, `INSERT INTO user_session (sid, user_id, team_id, aud, parent_sid, created_at, last_seen_at, revoked_at, user_agent, ip) VALUES
 (?, 401, NULL, 'root', NULL, ?, ?, NULL, 'mac-chrome', '10.0.0.1'),
 (?, 401, 7, 'team', ?, ?, ?, NULL, NULL, '10.0.0.2'),
 (?, 401, 7, 'team', ?, ?, ?, NULL, NULL, NULL),
 (?, 401, 8, 'team', NULL, ?, ?, NULL, 'iphone-safari', '10.0.0.3'),
 (?, 401, NULL, 'root', NULL, ?, ?, ?, 'old', '10.0.0.4'),
 (?, 401, NULL, 'root', NULL, ?, ?, NULL, 'expired', '10.0.0.5'),
 (?, 401, NULL, 'console', NULL, ?, ?, NULL, NULL, NULL),
 (?, 402, 7, 'team', NULL, ?, ?, NULL, NULL, NULL)`,
		sid("rootA"), now.Add(-2*time.Hour), now.Add(-2*time.Hour),
		sid("teamA1"), sid("rootA"), now.Add(-10*time.Minute), now.Add(-10*time.Minute),
		sid("teamA0"), sid("rootA"), now.Add(-3*time.Hour), now.Add(-3*time.Hour),
		sid("teamB"), now.Add(-5*time.Minute), now.Add(-time.Minute),
		sid("rootC"), now.Add(-24*time.Hour), now.Add(-24*time.Hour), now.Add(-12*time.Hour),
		sid("rootD"), now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour),
		sid("console"), now.Add(-time.Minute), now.Add(-time.Minute),
		sid("borisT"), now.Add(-time.Minute), now.Add(-time.Minute))
	// Hand-off codes: one from device A's root, one from device B's (none: B has no root) — a root
	// code of the expired root D; a reset letter.
	s23Exec(t, st, `INSERT INTO email_token (user_id, team_id, purpose, token_hash, expires_at, parent_sid) VALUES
 (401, 7, 'handoff', ?, ?, ?), (401, 8, 'handoff', ?, ?, ?), (401, NULL, 'reset', ?, ?, NULL), (402, 7, 'handoff', ?, ?, ?)`,
		strings.Repeat("a", 64), now.Add(time.Minute), sid("rootA"),
		strings.Repeat("d", 64), now.Add(time.Minute), sid("rootD"),
		strings.Repeat("e", 64), now.Add(time.Hour),
		strings.Repeat("f", 64), now.Add(time.Minute), sid("borisT"))
}

func TestDevicesInStore(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedDevices(t, st, now)

	// Live sessions: unrevoked root (30 d) and team (1 h) sessions only, oldest first.
	rows, err := st.LiveSessions(ctx, 401, s23Live(now))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, strings.TrimRight(r.SID, "0")+">"+strings.TrimRight(r.Chain, "0"))
	}
	if strings.Join(got, " ") != "rootA>rootA teamA1>rootA teamB>teamB" {
		t.Fatalf("live sessions = %v", got)
	}
	if rows[0].UserAgent != "mac-chrome" || rows[0].IP != "10.0.0.1" || rows[1].UserAgent != "" || rows[1].IP != "10.0.0.2" {
		t.Fatalf("recorded clients = %+v", rows[:2])
	}

	// The chain of a session: its own sid for a root or a lone team session, the parent otherwise;
	// another person's session is not found.
	for s, want := range map[string]string{"teamA1": "rootA", "rootA": "rootA", "teamB": "teamB", "rootC": "rootC"} {
		if c, ok, err := st.SessionChain(ctx, sid(s), 401); err != nil || !ok || c != sid(want) {
			t.Fatalf("chain of %s = %q %v %v", s, c, ok, err)
		}
	}
	if _, ok, _ := st.SessionChain(ctx, sid("borisT"), 401); ok {
		t.Fatal("another person's session has a chain")
	}

	// Touch: a session behind by TouchEvery moves to now; a recent one and a revoked one stay.
	if err := st.TouchSession(ctx, sid("rootA"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, sid("teamB"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, sid("rootC"), now); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE sid = ? AND last_seen_at = ?`, sid("rootA"), now) != 1 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE sid = ? AND last_seen_at = ?`, sid("teamB"), now.Add(-time.Minute)) != 1 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE sid = ? AND last_seen_at = ?`, sid("rootC"), now.Add(-24*time.Hour)) != 1 {
		t.Fatal("touch moved the wrong sessions")
	}

	// Sign out the other devices, keeping A: B and every leftover (expired D, console) end; A, its
	// expired team session and the other person stay. One live device was signed out (B).
	s23Exec(t, st, `INSERT INTO oauth_link_ticket (ticket_hash, user_id, expires_at) VALUES (?, 401, ?)`, strings.Repeat("1", 64), now.Add(time.Minute))
	s23Exec(t, st, `INSERT INTO oauth_state (state_hash, binder_hash, provider, target, pkce_verifier, nonce, link_user_id, expires_at)
 VALUES (?, ?, 'google', 'link', 'v', 'n', 401, ?)`, strings.Repeat("2", 64), strings.Repeat("3", 64), now.Add(10*time.Minute))
	out, err := st.SignOutOtherDevices(ctx, 401, sid("rootA"), s23Live(now), now)
	if err != nil {
		t.Fatal(err)
	}
	if out.Devices != 1 || len(out.SIDs) != 3 {
		t.Fatalf("signed out = %+v, want 1 device and 3 sessions (B, D, console)", out)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE user_id = 401 AND revoked_at IS NULL`) != 3 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE user_id = 401 AND revoked_at IS NULL AND COALESCE(parent_sid, sid) <> ?`, sid("rootA")) != 0 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE user_id = 402 AND revoked_at IS NULL`) != 1 {
		t.Fatal("sign-out of the other devices ended the wrong sessions")
	}
	if countRows(t, st, `SELECT COUNT(*) FROM email_token WHERE used_at IS NULL AND purpose = 'handoff'`) != 2 ||
		countRows(t, st, `SELECT COUNT(*) FROM email_token WHERE token_hash = ? AND used_at IS NOT NULL`, strings.Repeat("d", 64)) != 1 {
		t.Fatal("the hand-off code of an ended root is still usable, or A's / the other person's died")
	}
	if countRows(t, st, `SELECT COUNT(*) FROM oauth_link_ticket WHERE used_at IS NULL`)+
		countRows(t, st, `SELECT COUNT(*) FROM oauth_state WHERE used_at IS NULL`) != 0 {
		t.Fatal("a Google connect in flight survived the sign-out of the other devices")
	}
	// Again: nothing left to end.
	if out, err := st.SignOutOtherDevices(ctx, 401, sid("rootA"), s23Live(now), now); err != nil || out.Devices != 0 || len(out.SIDs) != 0 {
		t.Fatalf("second sign-out = %+v %v", out, err)
	}
	if _, err := st.SignOutOtherDevices(ctx, 401, " ", s23Live(now), now); err == nil {
		t.Fatal("a sign-out without the kept device ran")
	}

	// Single-device connects end too.
	s23Exec(t, st, `INSERT INTO oauth_link_ticket (ticket_hash, user_id, expires_at) VALUES (?, 401, ?)`, strings.Repeat("4", 64), now.Add(time.Minute))
	if err := st.ExpireLinkFlows(ctx, 401, now); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM oauth_link_ticket WHERE used_at IS NULL`) != 0 {
		t.Fatal("ExpireLinkFlows left a ticket")
	}

	// Retention: dead for more than 30 days forgets ip and user agent; the rest keep them.
	s23Exec(t, st, `INSERT INTO user_session (sid, user_id, team_id, aud, created_at, revoked_at, user_agent, ip) VALUES
 (?, 402, NULL, 'root', ?, ?, 'r-old', '1.1.1.1'), (?, 402, NULL, 'root', ?, NULL, 'r-exp', '1.1.1.2'),
 (?, 402, 7, 'team', ?, NULL, 't-old', '1.1.1.3'), (?, 402, NULL, 'root', ?, ?, 'r-recent', '1.1.1.4')`,
		sid("fRevoked"), now.Add(-50*24*time.Hour), now.Add(-31*24*time.Hour),
		sid("fExpired"), now.Add(-61*24*time.Hour),
		sid("fTeam"), now.Add(-31*24*time.Hour),
		sid("fRecent"), now.Add(-50*24*time.Hour), now.Add(-29*24*time.Hour))
	n, err := st.ForgetSessionClients(ctx, now, 30*24*time.Hour)
	if err != nil || n != 3 {
		t.Fatalf("forgot %d, %v; want 3", n, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE sid IN (?, ?, ?) AND ip IS NULL AND user_agent IS NULL`,
		sid("fRevoked"), sid("fExpired"), sid("fTeam")) != 3 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE sid = ? AND ip = '1.1.1.4'`, sid("fRecent")) != 1 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE sid = ? AND ip = '10.0.0.1'`, sid("rootA")) != 1 {
		t.Fatal("retention cleared the wrong sessions")
	}
	if n, err := st.ForgetSessionClients(ctx, now, 30*24*time.Hour); err != nil || n != 0 {
		t.Fatalf("second forget = %d %v", n, err)
	}
}

func TestChangePasswordInStore(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedDevices(t, st, now)
	s23Exec(t, st, `INSERT INTO email_token (user_id, purpose, token_hash, expires_at) VALUES (401, 'verify', ?, ?), (401, 'signin', ?, ?)`,
		strings.Repeat("7", 64), now.Add(time.Hour), strings.Repeat("8", 64), now.Add(time.Minute))
	s23Exec(t, st, `INSERT INTO oauth_link_ticket (ticket_hash, user_id, expires_at) VALUES (?, 401, ?)`, strings.Repeat("1", 64), now.Add(time.Minute))
	var gen0 int64
	if err := st.DB().Get(&gen0, `SELECT credential_gen FROM platform_user WHERE id = 401`); err != nil {
		t.Fatal(err)
	}
	change := func(signOut bool, verify func(string, bool) error) (platformstore.PasswordChanged, error) {
		return st.ChangePassword(ctx, platformstore.PasswordChange{
			UserID: 401, Hash: s23Hash2, KeepChain: sid("rootA"), SignOutOthers: signOut, Live: s23Live(now), Verify: verify, Now: now,
		})
	}

	// The check sees the stored hash and «has a password» under the lock; a refusal changes nothing.
	var seen string
	var seenSet bool
	if _, err := change(true, func(stored string, set bool) error {
		seen, seenSet = stored, set
		return errors.New("no")
	}); !errors.Is(err, platformstore.ErrPasswordMismatch) {
		t.Fatalf("refused check = %v, want ErrPasswordMismatch", err)
	}
	if seen != s23Hash || !seenSet {
		t.Fatalf("check saw %q %v", seen, seenSet)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE id = 401 AND password_hash = ? AND password_changed_at IS NULL AND credential_gen = ?`, s23Hash, gen0) != 1 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE user_id = 401 AND revoked_at IS NULL`) != 6 {
		t.Fatal("a refused change wrote")
	}

	// Change without signing out: the hash, password_changed_at, gen + 1; every code and letter in
	// flight and the connect in flight end; every session stays.
	res, err := change(false, func(string, bool) error { return nil })
	if err != nil || res.Gen != gen0+1 || res.Devices != 0 || len(res.SIDs) != 0 || res.Handle != "dev-anna" || res.TeamOne {
		t.Fatalf("change = %+v %v", res, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE id = 401 AND password_hash = ? AND password_changed_at = ?`, s23Hash2, now) != 1 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE user_id = 401 AND revoked_at IS NULL`) != 6 ||
		countRows(t, st, `SELECT COUNT(*) FROM email_token WHERE user_id = 401 AND used_at IS NULL`) != 0 ||
		countRows(t, st, `SELECT COUNT(*) FROM email_token WHERE user_id = 402 AND used_at IS NULL`) != 1 ||
		countRows(t, st, `SELECT COUNT(*) FROM oauth_link_ticket WHERE used_at IS NULL`) != 0 {
		t.Fatal("change without sign-out wrote the wrong rows")
	}

	// Change with sign_out_others: every chain but A ends; B was the one live device.
	res, err = change(true, func(string, bool) error { return nil })
	if err != nil || res.Gen != gen0+2 || res.Devices != 1 || len(res.SIDs) != 3 {
		t.Fatalf("change with sign-out = %+v %v", res, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE user_id = 401 AND revoked_at IS NULL AND COALESCE(parent_sid, sid) <> ?`, sid("rootA")) != 0 ||
		countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE user_id = 401 AND revoked_at IS NULL`) != 3 {
		t.Fatal("sign-out with the change ended the wrong sessions")
	}

	// A team #1 member is reported for the admins mirror; a disabled account is refused.
	s23Exec(t, st, `INSERT INTO team (id, slug, name, db_name, status) VALUES (1, 'grbpwr', 'grbpwr', 'grbpwr', 'active')`)
	s23Exec(t, st, `INSERT INTO team_member (team_id, user_id, role, status) VALUES (1, 401, 'member', 'active')`)
	if res, err := change(false, func(string, bool) error { return nil }); err != nil || !res.TeamOne {
		t.Fatalf("team #1 change = %+v %v", res, err)
	}
	s23Exec(t, st, `UPDATE platform_user SET status = 'disabled' WHERE id = 401`)
	if _, err := change(false, func(string, bool) error { return nil }); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("disabled account = %v", err)
	}
	// An unusable hash is never written as a password.
	unusable, err := platformstore.MarkUnusable(s23Hash2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChangePassword(ctx, platformstore.PasswordChange{UserID: 402, Hash: unusable, KeepChain: sid("borisT"),
		Verify: func(string, bool) error { return nil }, Now: now}); err == nil {
		t.Fatal("an unusable hash was accepted as a new password")
	}
}

func TestPasswordSetRule(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	unusable, err := platformstore.MarkUnusable(s23Hash)
	if err != nil {
		t.Fatal(err)
	}
	if !platformstore.IsUnusableHash(unusable) || platformstore.IsUnusableHash(s23Hash) || platformstore.IsUnusableHash("!x") {
		t.Fatal("IsUnusableHash does not tell the unusable mark")
	}
	if _, err := platformstore.MarkUnusable("!x"); err == nil {
		t.Fatal("MarkUnusable took a non-PBKDF2 hash")
	}
	s23Exec(t, st, `INSERT INTO platform_user (id, email, handle, password_hash, status, created_at, password_changed_at) VALUES
 (501, 'a@x.io', 'pw-a', ?, 'active', NOW(3), NULL),
 (502, 'b@x.io', 'pw-b', ?, 'active', NOW(3), NULL),
 (503, 'c@x.io', 'pw-c', '!x', 'active', NOW(3), NULL),
 (504, 'd@x.io', 'pw-d', ?, 'active', NOW(3) - INTERVAL 1 DAY, NULL),
 (505, 'e@x.io', 'pw-e', ?, 'active', NOW(3) - INTERVAL 1 DAY, NOW(3)),
 (506, 'f@x.io', 'pw-f', ?, 'active', NOW(3) - INTERVAL 1 DAY, NULL)`, s23Hash, unusable, s23Hash, s23Hash, s23Hash)
	// 504: a provider sign-up from before the mark (identity made with the account, no password
	// written since); 505: the same, then a password was set; 506: Google linked a day later.
	s23Exec(t, st, `INSERT INTO platform_identity (user_id, provider, subject, created_at) VALUES
 (504, 'google', 'g-504', NOW(3) - INTERVAL 1 DAY), (505, 'google', 'g-505', NOW(3) - INTERVAL 1 DAY), (506, 'google', 'g-506', NOW(3))`)
	for uid, want := range map[int64]bool{501: true, 502: false, 503: false, 504: false, 505: true, 506: true} {
		p, err := st.Profile(ctx, uid)
		if err != nil || p.PasswordSet != want {
			t.Errorf("password_set of %d = %v (%v), want %v", uid, p.PasswordSet, err, want)
		}
	}
}

func TestGoogleLinkInStore(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	unusable, err := platformstore.MarkUnusable(s23Hash)
	if err != nil {
		t.Fatal(err)
	}
	// Accounts made a day ago: a connect within seconds of the account reads as a provider sign-up.
	s23Exec(t, st, `INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator, created_at) VALUES
 (601, 'pw@x.io', 'gl-pw', ?, 'active', 0, NOW(3) - INTERVAL 1 DAY), (602, 'g@x.io', 'gl-g', ?, 'active', 0, NOW(3) - INTERVAL 1 DAY),
 (603, 'op@x.io', 'gl-op', ?, 'active', 1, NOW(3) - INTERVAL 1 DAY), (604, 'pen@x.io', 'gl-pen', ?, 'pending', 0, NOW(3) - INTERVAL 1 DAY)`,
		s23Hash, unusable, s23Hash, s23Hash)
	s23Exec(t, st, `INSERT INTO platform_identity (user_id, provider, subject, email) VALUES (602, 'google', 'g-602', 'g@gmail.com')`)
	h := func(c byte) string { return strings.Repeat(string(c), 64) }

	// Tickets: an account with Google, an operator and a pending account get none.
	if err := st.CreateLinkTicket(ctx, h('a'), 602, "", now); !errors.Is(err, platformstore.ErrAlreadyLinked) {
		t.Fatalf("ticket for a linked account = %v", err)
	}
	for _, uid := range []int64{603, 604, 699} {
		if err := st.CreateLinkTicket(ctx, h('a'), uid, "", now); !errors.Is(err, platformstore.ErrUserNotFound) {
			t.Fatalf("ticket for %d = %v, want ErrUserNotFound", uid, err)
		}
	}
	if err := st.CreateLinkTicket(ctx, h('a'), 601, "acme", now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLinkTicket(ctx, h('b'), 601, "", now.Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Single use; an expired one and an unknown one are the same refusal.
	uid, slug, err := st.ConsumeLinkTicket(ctx, h('a'), now)
	if err != nil || uid != 601 || slug != "acme" {
		t.Fatalf("consume = %d %q %v", uid, slug, err)
	}
	for _, hash := range []string{h('a'), h('b'), h('c'), "short"} {
		if _, _, err := st.ConsumeLinkTicket(ctx, hash, now); !errors.Is(err, platformstore.ErrTokenInvalid) {
			t.Fatalf("consume %q = %v, want ErrTokenInvalid", hash[:5], err)
		}
	}

	g := func(subject, email string, authoritative bool) platformstore.ProviderSignIn {
		return platformstore.ProviderSignIn{Provider: platformstore.ProviderGoogle, Subject: subject, Email: email,
			EmailVerified: true, EmailAuthoritative: authoritative}
	}
	started := now.Add(-time.Minute)
	// Another account's subject: taken. An unverified address: unverified. The account's own subject
	// again: fine, nothing written.
	if err := st.CompleteGoogleLink(ctx, 601, g("g-602", "g@gmail.com", true), started, now); !errors.Is(err, platformstore.ErrLinkTaken) {
		t.Fatalf("someone else's subject = %v", err)
	}
	if err := st.CompleteGoogleLink(ctx, 601, g("g-601", "x@corp.example", false), started, now); !errors.Is(err, platformstore.ErrProviderUnverified) {
		t.Fatalf("non-authoritative address = %v", err)
	}
	if err := st.CompleteGoogleLink(ctx, 601, g("g-601", "", true), started, now); !errors.Is(err, platformstore.ErrProviderUnverified) {
		t.Fatalf("no address = %v", err)
	}
	if err := st.CompleteGoogleLink(ctx, 602, g("g-602", "g@gmail.com", true), started, now); err != nil {
		t.Fatalf("own subject again = %v", err)
	}
	// Connected: the identity is written with the Google address (it may differ from the account's).
	if err := st.CompleteGoogleLink(ctx, 601, g("g-601", "pw.other@gmail.com", true), started, now); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_identity WHERE user_id = 601 AND provider = 'google' AND subject = 'g-601' AND email = 'pw.other@gmail.com'`) != 1 {
		t.Fatal("identity not written")
	}
	if err := st.CompleteGoogleLink(ctx, 601, g("g-601b", "pw2@gmail.com", true), started, now); !errors.Is(err, platformstore.ErrAlreadyLinked) {
		t.Fatalf("a second Google = %v", err)
	}
	// A flow started before the password changed is dead; an operator's or a pending account too.
	s23Exec(t, st, `UPDATE platform_user SET password_changed_at = ? WHERE id = 602`, now.Add(-30*time.Second))
	s23Exec(t, st, `DELETE FROM platform_identity WHERE user_id = 602`)
	if err := st.CompleteGoogleLink(ctx, 602, g("g-602", "g@gmail.com", true), started, now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("flow older than the password = %v", err)
	}
	for _, uid := range []int64{603, 604, 699} {
		if err := st.CompleteGoogleLink(ctx, uid, g(fmt.Sprint("g-", uid), "o@gmail.com", true), started, now); !errors.Is(err, platformstore.ErrTokenInvalid) {
			t.Fatalf("link to %d = %v, want ErrTokenInvalid", uid, err)
		}
	}

	// Disconnect: refused while Google is the only way in; allowed with a password; nothing to remove
	// is not an error.
	s23Exec(t, st, `INSERT INTO platform_identity (user_id, provider, subject, email) VALUES (602, 'google', 'g-602', 'g@gmail.com')`)
	if _, err := st.UnlinkGoogle(ctx, 602); !errors.Is(err, platformstore.ErrOnlySignIn) {
		t.Fatalf("unlink of the only way in = %v", err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_identity WHERE user_id = 602`) != 1 {
		t.Fatal("a refused unlink removed the identity")
	}
	s23Exec(t, st, `INSERT INTO platform_identity (user_id, provider, subject) VALUES (602, 'apple', 'a-602')`)
	if n, err := st.UnlinkGoogle(ctx, 602); err != nil || n != 1 {
		t.Fatalf("unlink with apple left = %d %v", n, err)
	}
	if n, err := st.UnlinkGoogle(ctx, 601); err != nil || n != 1 {
		t.Fatalf("unlink with a password = %d %v", n, err)
	}
	if n, err := st.UnlinkGoogle(ctx, 601); err != nil || n != 0 {
		t.Fatalf("unlink of nothing = %d %v", n, err)
	}
	if _, err := st.UnlinkGoogle(ctx, 604); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("unlink of a pending account = %v", err)
	}
}
