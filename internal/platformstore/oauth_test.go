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

// Provider sign-in in the store (teams O §1, §2, §5, §12 and the coordinator's EmailAuthoritative
// rule). The callback's provider calls live in lane O3; everything after the verified id_token is here.

func hex64(c byte) string { return strings.Repeat(string(c), 64) }

func TestOAuthStateSpentOnce(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	in := platformstore.OAuthState{StateHash: hex64('a'), BinderHash: hex64('b'), Provider: platformstore.ProviderGoogle,
		Target: platformstore.OAuthTargetSignIn, PKCEVerifier: "verifier", Nonce: "nonce"}
	if err := st.CreateOAuthState(ctx, in, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.ConsumeOAuthState(ctx, hex64('a'), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.BinderHash != hex64('b') || got.PKCEVerifier != "verifier" || got.Nonce != "nonce" || got.Target != platformstore.OAuthTargetSignIn {
		t.Fatalf("consumed state %+v", got)
	}
	if _, err := st.ConsumeOAuthState(ctx, hex64('a'), now.Add(time.Minute)); !errors.Is(err, platformstore.ErrOAuthStateInvalid) {
		t.Fatalf("second consume: %v, want ErrOAuthStateInvalid", err)
	}
	// Expired: created 10 minutes and a moment ago.
	old := in
	old.StateHash = hex64('c')
	if err := st.CreateOAuthState(ctx, old, now.Add(-platformstore.OAuthStateTTL-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConsumeOAuthState(ctx, hex64('c'), now); !errors.Is(err, platformstore.ErrOAuthStateInvalid) {
		t.Fatalf("expired consume: %v, want ErrOAuthStateInvalid", err)
	}
	if _, err := st.ConsumeOAuthState(ctx, hex64('d'), now); !errors.Is(err, platformstore.ErrOAuthStateInvalid) {
		t.Fatalf("unknown consume: %v, want ErrOAuthStateInvalid", err)
	}
	// An invite target carries the invite hash and the terms version back.
	inv := in
	inv.StateHash, inv.Target, inv.InviteHash, inv.TermsVersion = hex64('e'), platformstore.OAuthTargetInvite, hex64('f'), "2026-11"
	if err := st.CreateOAuthState(ctx, inv, now); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ConsumeOAuthState(ctx, hex64('e'), now); err != nil || got.InviteHash != hex64('f') || got.TermsVersion != "2026-11" {
		t.Fatalf("invite state %+v %v", got, err)
	}
}

// oauthWorld adds to seedConsole: dis (disabled), pen (pending, with a pending team that has a
// logo, a live verify letter and a stray session).
func oauthWorld(t *testing.T, st *platformstore.Store) {
	t.Helper()
	seedConsole(t, st)
	for _, q := range []string{
		`INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator) VALUES
 (104, 'dis@x.io', 'dis', 'h', 'disabled', 0), (105, 'pen@x.io', 'pen', 'h-typed-by-someone', 'pending', 0)`,
		`INSERT INTO team (id, slug, name, db_name, status, created_at, created_by, pending_expires_at) VALUES
 (7, 'penteam', 'Pen', NULL, 'pending', '2026-11-18', 105, '2099-01-01')`,
		`INSERT INTO team_logo (team_id, png, sha256) VALUES (7, 'png', REPEAT('7', 64))`,
		`INSERT INTO email_token (user_id, team_id, purpose, token_hash, code_hash, expires_at) VALUES
 (105, 7, 'verify', REPEAT('e', 64), REPEAT('f', 64), '2099-01-01')`,
		`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('spen0000000000000000000000000000', 105, NULL, 'root')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// rowCounts is every table a completion may write, as one comparable string.
func rowCounts(t *testing.T, st *platformstore.Store) string {
	t.Helper()
	var parts []string
	for _, tbl := range []string{"platform_user", "platform_identity", "email_token", "user_session", "team", "team_logo", "platform_audit"} {
		var n int
		if err := st.DB().Get(&n, `SELECT COUNT(*) FROM `+tbl); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("%s=%d", tbl, n))
	}
	var revoked, used int
	if err := st.DB().Get(&revoked, `SELECT COUNT(*) FROM user_session WHERE revoked_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Get(&used, `SELECT COUNT(*) FROM email_token WHERE used_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	var hashes string
	if err := st.DB().Get(&hashes, `SELECT GROUP_CONCAT(CONCAT(handle, ':', status, ':', password_hash) ORDER BY id) FROM platform_user`); err != nil {
		t.Fatal(err)
	}
	return strings.Join(parts, " ") + fmt.Sprintf(" revoked=%d used=%d users=%s", revoked, used, hashes)
}

func providerParams(email string, now time.Time, code byte) platformstore.ProviderParams {
	return platformstore.ProviderParams{
		Now: now, Email: email, HandleBase: strings.Split(email+"@", "@")[0] + "x",
		UnusableHash: func() (string, error) { return "!unusable", nil },
		Code:         platformstore.NewToken{TokenHash: hex64(code), ExpiresAt: now.Add(time.Minute)},
	}
}

func google(sub, email string, verified, authoritative bool) platformstore.ProviderSignIn {
	return platformstore.ProviderSignIn{Provider: platformstore.ProviderGoogle, Subject: sub, Email: email,
		EmailVerified: verified, EmailAuthoritative: authoritative}
}

// The coordinator's rule: without a known identity, an email that is empty, unverified or not
// authoritative refuses with ErrProviderUnverified before any lookup and writes nothing — for an
// unknown address and for an existing active account's address alike.
func TestProviderUnverifiedWritesNothing(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	oauthWorld(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	before := rowCounts(t, st)
	cases := []struct {
		name  string
		in    platformstore.ProviderSignIn
		email string // as the caller normalised it
	}{
		{"unknown email, verified, not authoritative", google("s-new", "new@x.io", true, false), "new@x.io"},
		{"active account's email, verified, not authoritative", google("s-ann", "ann@x.io", true, false), "ann@x.io"},
		{"pending account's email, verified, not authoritative", google("s-pen", "pen@x.io", true, false), "pen@x.io"},
		{"operator's email, verified, not authoritative", google("s-op", "op@x.io", true, false), "op@x.io"},
		{"unknown email, not verified", google("s-new", "new@x.io", false, true), "new@x.io"},
		{"active account's email, not verified", google("s-ann", "ann@x.io", false, true), "ann@x.io"},
		{"no email", google("s-none", "", true, true), ""},
		{"whitespace email", google("s-blank", "   ", true, true), ""},
		{"email the caller could not normalise", google("s-bad", "not-an-address", true, true), ""},
	}
	for i, c := range cases {
		_, err := st.CompleteProviderSignIn(ctx, c.in, providerParams(c.email, now, byte('a'+i)))
		if !errors.Is(err, platformstore.ErrProviderUnverified) {
			t.Errorf("%s: %v, want ErrProviderUnverified", c.name, err)
		}
		if got := rowCounts(t, st); got != before {
			t.Fatalf("%s wrote rows:\n before %s\n after  %s", c.name, before, got)
		}
	}
}

func TestProviderCompletionMatrix(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	oauthWorld(t, st)
	db := st.DB()
	now := time.Now().UTC().Truncate(time.Millisecond)
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.Get(&n, q, args...); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// New address → a new active account, verified, unusable password, no team, the identity and a code.
	res, err := st.CompleteProviderSignIn(ctx, google("s-new", "New@X.io", true, true), providerParams("new@x.io", now, '1'))
	if err != nil || !res.Created || res.Linked || res.UserID <= 0 {
		t.Fatalf("new: %+v %v", res, err)
	}
	u, err := st.UserByID(ctx, res.UserID)
	if err != nil || u.Status != platformstore.UserActive || !u.EmailVerifiedAt.Valid || u.PasswordHash != "!unusable" || u.Email.String != "new@x.io" {
		t.Fatalf("new account %+v %v", u, err)
	}
	if n := count(`SELECT COUNT(*) FROM team_member WHERE user_id = ?`, res.UserID); n != 0 {
		t.Fatalf("new account has %d memberships", n)
	}
	if n := count(`SELECT COUNT(*) FROM platform_identity WHERE user_id = ? AND provider = 'google' AND subject = 's-new' AND email = 'new@x.io'`, res.UserID); n != 1 {
		t.Fatal("new account has no identity")
	}
	if n := count(`SELECT COUNT(*) FROM email_token WHERE user_id = ? AND purpose = 'signin' AND token_hash = ? AND used_at IS NULL`, res.UserID, hex64('1')); n != 1 {
		t.Fatal("new account has no sign-in code")
	}

	// Active account by address → linked, its password untouched.
	res, err = st.CompleteProviderSignIn(ctx, google("s-ann", "ann@x.io", true, true), providerParams("ann@x.io", now, '2'))
	if err != nil || res.UserID != 101 || !res.Linked || res.Created || res.Claimed {
		t.Fatalf("ann: %+v %v", res, err)
	}
	if count(`SELECT COUNT(*) FROM platform_user WHERE id = 101 AND password_hash = 'h'`) != 1 {
		t.Fatal("linking changed ann's password")
	}

	// A known identity signs in whatever the email and its flags say now (not authoritative,
	// not verified, another address, none at all) — no new account, no new identity.
	for i, in := range []platformstore.ProviderSignIn{
		google("s-ann", "ann@x.io", true, false),
		google("s-ann", "someone-else@x.io", false, false),
		google("s-ann", "", false, false),
	} {
		users := count(`SELECT COUNT(*) FROM platform_user`)
		res, err := st.CompleteProviderSignIn(ctx, in, providerParams("", now, byte('3'+i)))
		if err != nil || res.UserID != 101 || res.Created || res.Linked {
			t.Fatalf("known identity #%d: %+v %v", i, res, err)
		}
		if count(`SELECT COUNT(*) FROM platform_user`) != users || count(`SELECT COUNT(*) FROM platform_identity WHERE user_id = 101`) != 1 {
			t.Fatalf("known identity #%d wrote an account or an identity", i)
		}
		if count(`SELECT COUNT(*) FROM email_token WHERE user_id = 101 AND purpose = 'signin' AND token_hash = ?`, hex64(byte('3'+i))) != 1 {
			t.Fatalf("known identity #%d got no code", i)
		}
	}

	// Operator by address → use_password, nothing written; disabled → disabled, nothing written.
	before := rowCounts(t, st)
	if _, err := st.CompleteProviderSignIn(ctx, google("s-op", "op@x.io", true, true), providerParams("op@x.io", now, '7')); !errors.Is(err, platformstore.ErrProviderOperator) {
		t.Fatalf("operator: %v", err)
	}
	if _, err := st.CompleteProviderSignIn(ctx, google("s-dis", "dis@x.io", true, true), providerParams("dis@x.io", now, '8')); !errors.Is(err, platformstore.ErrProviderDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if got := rowCounts(t, st); got != before {
		t.Fatalf("operator / disabled refusals wrote rows:\n before %s\n after  %s", before, got)
	}

	// Pending account by address → the claim: its pending team and logo gone, the typed password
	// unusable, the stray session revoked, the verify letter spent, active and verified, linked.
	res, err = st.CompleteProviderSignIn(ctx, google("s-pen", "pen@x.io", true, true), providerParams("pen@x.io", now, '9'))
	if err != nil || res.UserID != 105 || !res.Claimed || !res.Linked {
		t.Fatalf("pending claim: %+v %v", res, err)
	}
	if len(res.SIDs) != 1 || res.SIDs[0] != "spen0000000000000000000000000000" {
		t.Fatalf("claim revoked %v", res.SIDs)
	}
	if count(`SELECT COUNT(*) FROM team WHERE id = 7`) != 0 || count(`SELECT COUNT(*) FROM team_logo WHERE team_id = 7`) != 0 {
		t.Fatal("claim left the pending team or its logo")
	}
	if count(`SELECT COUNT(*) FROM platform_user WHERE id = 105 AND status = 'active' AND email_verified_at IS NOT NULL AND password_hash = '!unusable'`) != 1 {
		t.Fatal("claimed account not active / verified / password not replaced")
	}
	if count(`SELECT COUNT(*) FROM email_token WHERE user_id = 105 AND purpose = 'verify' AND used_at IS NULL`) != 0 {
		t.Fatal("claim left the verify letter live")
	}
	if count(`SELECT COUNT(*) FROM platform_audit WHERE actor_user_id = 105 AND action = 'oauth_claimed_pending'`) != 1 {
		t.Fatal("claim not audited")
	}

	// A known identity whose account became disabled / an operator's refuses; nothing written.
	if _, err := db.Exec(`UPDATE platform_user SET status = 'disabled' WHERE id = 105`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE platform_user SET is_operator = 1 WHERE id = 101`); err != nil {
		t.Fatal(err)
	}
	before = rowCounts(t, st)
	if _, err := st.CompleteProviderSignIn(ctx, google("s-pen", "pen@x.io", true, true), providerParams("pen@x.io", now, 'a')); !errors.Is(err, platformstore.ErrProviderDisabled) {
		t.Fatalf("known identity, disabled account: %v", err)
	}
	if _, err := st.CompleteProviderSignIn(ctx, google("s-ann", "ann@x.io", true, true), providerParams("ann@x.io", now, 'b')); !errors.Is(err, platformstore.ErrProviderOperator) {
		t.Fatalf("known identity, operator account: %v", err)
	}
	if got := rowCounts(t, st); got != before {
		t.Fatalf("known-identity refusals wrote rows:\n before %s\n after  %s", before, got)
	}
}

// A sign-in code opens one root session, once, within its 60 s.
func TestSignInCodeOnce(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := st.CompleteProviderSignIn(ctx, google("s-ann", "ann@x.io", true, true), providerParams("ann@x.io", now, '1')); err != nil {
		t.Fatal(err)
	}
	ex, err := st.ExchangeSignInCode(ctx, hex64('1'), "sroot000000000000000000000000001", now.Add(10*time.Second))
	if err != nil || ex.User.ID != 101 || ex.EnterTeamID != 0 {
		t.Fatalf("exchange: %+v %v", ex, err)
	}
	if live, err := st.RootSessionLive(ctx, "sroot000000000000000000000000001", 101, now.Add(-time.Hour)); err != nil || !live {
		t.Fatalf("root session not live: %v %v", live, err)
	}
	if _, err := st.ExchangeSignInCode(ctx, hex64('1'), "sroot000000000000000000000000002", now.Add(11*time.Second)); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("second exchange: %v, want ErrTokenInvalid", err)
	}
	// A fresh code, exchanged a moment after its 60 s.
	if _, err := st.CompleteProviderSignIn(ctx, google("s-ann", "ann@x.io", true, true), providerParams("ann@x.io", now, '2')); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExchangeSignInCode(ctx, hex64('2'), "sroot000000000000000000000000003", now.Add(time.Minute+time.Millisecond)); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("late exchange: %v, want ErrTokenInvalid", err)
	}
	var sessions int
	if err := st.DB().Get(&sessions, `SELECT COUNT(*) FROM user_session WHERE sid LIKE 'sroot%'`); err != nil || sessions != 1 {
		t.Fatalf("root sessions = %d %v, want 1", sessions, err)
	}
	// After an invite: the code names the team to enter.
	if err := st.IssueSignInCode(ctx, 101, 2, platformstore.NewToken{TokenHash: hex64('3'), ExpiresAt: now.Add(time.Minute)}, now); err != nil {
		t.Fatal(err)
	}
	if ex, err := st.ExchangeSignInCode(ctx, hex64('3'), "sroot000000000000000000000000004", now); err != nil || ex.EnterTeamID != 2 {
		t.Fatalf("invite exchange: %+v %v", ex, err)
	}
	// An operator gets no code.
	if err := st.IssueSignInCode(ctx, 103, 0, platformstore.NewToken{TokenHash: hex64('4'), ExpiresAt: now.Add(time.Minute)}, now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("operator code: %v, want ErrTokenInvalid", err)
	}
}

// RootSessionLive is absolute: 30 days from the session's creation, never renewed by use.
func TestRootSessionAbsolute(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	ttl := 30 * 24 * time.Hour
	if err := st.CreateSession(ctx, "sold0000000000000000000000000000", 101, 0, platformstore.SessionRoot, 0, now.Add(-ttl-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, "syng0000000000000000000000000000", 101, 0, platformstore.SessionRoot, 0, now.Add(-ttl+time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Recent use does not renew it.
	if _, err := st.DB().Exec(`UPDATE user_session SET last_seen_at = ? WHERE sid = 'sold0000000000000000000000000000'`, now); err != nil {
		t.Fatal(err)
	}
	if live, err := st.RootSessionLive(ctx, "sold0000000000000000000000000000", 101, now.Add(-ttl)); err != nil || live {
		t.Fatalf("31-day-old root session live=%v %v", live, err)
	}
	if live, err := st.RootSessionLive(ctx, "syng0000000000000000000000000000", 101, now.Add(-ttl)); err != nil || !live {
		t.Fatalf("29-day-old root session live=%v %v", live, err)
	}
	// Another account's sid, and a team session's, are not this account's root session.
	if live, _ := st.RootSessionLive(ctx, "syng0000000000000000000000000000", 102, now.Add(-ttl)); live {
		t.Fatal("root session live for another account")
	}
	if live, _ := st.RootSessionLive(ctx, "sann0000000000000000000000000000", 101, now.Add(-ttl)); live {
		t.Fatal("a team session passed as a root session")
	}
}

// A root sign-out ends the root session, every team session opened from it, and its unused
// hand-off codes; a team sign-out leaves the root alone; other sessions are untouched.
func TestRootSignOutTree(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	db := st.DB()
	now := time.Now().UTC().Truncate(time.Millisecond)
	const root = "sroot000000000000000000000000000"
	if err := st.CreateSession(ctx, root, 101, 0, platformstore.SessionRoot, 0, now); err != nil {
		t.Fatal(err)
	}
	handoff := func(c byte) {
		t.Helper()
		if err := st.IssueHandoff(ctx, 101, root, platformstore.NewToken{TeamID: 2, TokenHash: hex64(c), ExpiresAt: now.Add(time.Minute)}, now); err != nil {
			t.Fatal(err)
		}
	}
	handoff('1')
	handoff('2')
	handoff('3') // stays unused
	for i, c := range []byte{'1', '2'} {
		if _, err := st.ExchangeHandoff(ctx, hex64(c), 2, fmt.Sprintf("schild%026d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	// A team sign-out (child 1) leaves the root and the other child.
	if err := st.RevokeSession(ctx, "schild00000000000000000000000001", now); err != nil {
		t.Fatal(err)
	}
	if live, _ := st.RootSessionLive(ctx, root, 101, now.Add(-time.Hour)); !live {
		t.Fatal("a team sign-out ended the root session")
	}

	sids, err := st.SignOutRoot(ctx, root, 101, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sortedStrings(sids), ",") != "schild00000000000000000000000000,"+root {
		t.Fatalf("root sign-out revoked %v", sids)
	}
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM user_session WHERE sid IN (?, 'schild00000000000000000000000000') AND revoked_at IS NULL`, root); err != nil || n != 0 {
		t.Fatalf("root or child still live: %d %v", n, err)
	}
	if err := db.Get(&n, `SELECT COUNT(*) FROM email_token WHERE token_hash = ? AND used_at IS NULL`, hex64('3')); err != nil || n != 0 {
		t.Fatalf("unused hand-off still live after the root sign-out: %d %v", n, err)
	}
	if _, err := st.ExchangeHandoff(ctx, hex64('3'), 2, "schild00000000000000000000000009", now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("hand-off after root sign-out: %v", err)
	}
	// Sessions not opened from this root (ann's own team session, bob's) are untouched.
	if err := db.Get(&n, `SELECT COUNT(*) FROM user_session WHERE sid IN ('sann0000000000000000000000000000', 'sbob0000000000000000000000000000') AND revoked_at IS NULL`); err != nil || n != 2 {
		t.Fatalf("other sessions revoked: %d live %v", n, err)
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
