package platformstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// The live tuple behind a team token, its revocation, and the authoritative password write
// (review items 3 and 8). Runs against the test MySQL (CI); skipped without one.
func TestTeamSessionRevokeAndPassword(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if err := st.EnsureTeamOne(ctx, "grbpwr_test", []platformstore.TeamOneAdmin{
		{Username: "boss", PasswordHash: "h-boss", Super: true},
		{Username: "helper", PasswordHash: "h-helper"},
	}); err != nil {
		t.Fatal(err)
	}
	var boss, helper int64
	if err := st.DB().Get(&boss, `SELECT id FROM platform_user WHERE handle = 'boss'`); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Get(&helper, `SELECT id FROM platform_user WHERE handle = 'helper'`); err != nil {
		t.Fatal(err)
	}
	const s1, s2, s3 = "s1000000000000000000000000000000", "s2000000000000000000000000000000", "s3000000000000000000000000000000"
	if _, err := st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES
 (?, ?, 1, 'team'), (?, ?, NULL, 'console'), (?, ?, 1, 'team')`, s1, boss, s2, boss, s3, helper); err != nil {
		t.Fatal(err)
	}

	if role, ok, err := st.TeamSession(ctx, s1, boss, 1); err != nil || !ok || role != "owner" {
		t.Fatalf("live owner session: %q %v %v", role, ok, err)
	}
	for _, c := range []struct {
		sid      string
		uid, tid int64
	}{{s1, helper, 1}, {s1, boss, 2}, {s2, boss, 1}, {"nope", boss, 1}} {
		if _, ok, err := st.TeamSession(ctx, c.sid, c.uid, c.tid); err != nil || ok {
			t.Fatalf("TeamSession(%v) = %v %v, want inactive", c, ok, err)
		}
	}
	if _, err := st.DB().Exec(`UPDATE team_member SET status = 'removed' WHERE user_id = ? AND team_id = 1`, helper); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.TeamSession(ctx, s3, helper, 1); ok {
		t.Fatal("removed member's session still live")
	}
	if sids, err := st.RevokeTeamSessions(ctx, helper, 1, time.Now()); err != nil || len(sids) != 1 || sids[0] != s3 {
		t.Fatalf("RevokeTeamSessions: %v %v", sids, err)
	}

	// A hand-off issued from boss's live root session before the change.
	const root = "r1000000000000000000000000000000"
	if _, err := st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES (?, ?, NULL, 'root')`, root, boss); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.IssueHandoff(ctx, boss, root, platformstore.NewToken{TeamID: 1, TokenHash: "handoff-before", ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatalf("hand-off from a live root session: %v", err)
	}

	c, err := st.SetPasswordAndRevokeSessions(ctx, "boss", "h-boss-2", sameHash("h-boss"), now)
	if err != nil || len(c.SIDs) != 3 || c.Gen != 1 {
		t.Fatalf("SetPasswordAndRevokeSessions: %+v %v", c, err)
	}
	var hash string
	if err := st.DB().Get(&hash, `SELECT password_hash FROM platform_user WHERE id = ?`, boss); err != nil || hash != "h-boss-2" {
		t.Fatalf("hash %q %v", hash, err)
	}
	var live int
	if err := st.DB().Get(&live, `SELECT COUNT(*) FROM user_session WHERE user_id = ? AND revoked_at IS NULL`, boss); err != nil || live != 0 {
		t.Fatalf("%d live sessions after a password change (%v)", live, err)
	}
	// The issued hand-off died with the password; a new one from the revoked root session is refused.
	if _, err := st.ExchangeHandoff(ctx, "handoff-before", 1, "s4000000000000000000000000000000", now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("hand-off issued before the change: %v", err)
	}
	if err := st.IssueHandoff(ctx, boss, root, platformstore.NewToken{TeamID: 1, TokenHash: "handoff-after", ExpiresAt: now.Add(time.Hour)}, now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("hand-off from a revoked root session: %v", err)
	}
	// A sign-in that checked the old password (generation 0) opens no session; the new one does.
	if err := st.CreateSession(ctx, "s5000000000000000000000000000000", boss, 1, platformstore.SessionTeam, 0, now); !errors.Is(err, platformstore.ErrCredentialsChanged) {
		t.Fatalf("session on the old generation: %v", err)
	}
	if err := st.CreateSession(ctx, "s6000000000000000000000000000000", boss, 1, platformstore.SessionTeam, c.Gen, now); err != nil {
		t.Fatalf("session on the new generation: %v", err)
	}
	if gen, err := st.LegacyCredGen(ctx, "boss"); err != nil || gen != 1 {
		t.Fatalf("LegacyCredGen = %d, %v", gen, err)
	}
	if cred, linked, err := st.TeamOneCredentials(ctx, "boss"); err != nil || !linked || cred.Hash != "h-boss-2" || cred.Gen != 1 || cred.Disabled {
		t.Fatalf("TeamOneCredentials = %+v %v %v", cred, linked, err)
	}
	// An admin reset (verify nil) bumps the generation again.
	if c, err := st.SetPasswordAndRevokeSessions(ctx, "boss", "h-boss-r", nil, now); err != nil || c.Gen != 2 || len(c.SIDs) != 1 {
		t.Fatalf("admin reset: %+v %v", c, err)
	}

	if _, err := st.SetPasswordAndRevokeSessions(ctx, "nobody", "h", sameHash("h"), now); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("unknown handle: %v", err)
	}
	// verify runs against the locked row's hash: the old one is refused and nothing changes.
	if _, err := st.SetPasswordAndRevokeSessions(ctx, "boss", "h-boss-3", sameHash("h-boss"), now); !errors.Is(err, platformstore.ErrPasswordMismatch) {
		t.Fatalf("stale current hash: %v", err)
	}
	if err := st.DB().Get(&hash, `SELECT password_hash FROM platform_user WHERE id = ?`, boss); err != nil || hash != "h-boss-r" {
		t.Fatalf("hash after a refused change %q %v", hash, err)
	}
}

// Every team #1 credential operation goes through team #1 membership: a platform user who merely
// shares an admin's handle is never read, written or linked (handle collision).
func TestTeamOneCredentialsHandleCollision(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if err := st.EnsureTeamOne(ctx, "grbpwr_test", []platformstore.TeamOneAdmin{
		{Username: "boss", PasswordHash: "h-boss", Super: true},
		{Username: "gone", PasswordHash: "h-gone", Disabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	// A stranger signed up on the platform as "x", outside team #1, with a live session.
	if _, err := st.DB().Exec(`INSERT INTO platform_user (email, handle, password_hash, status, created_at)
VALUES ('x@example.test', 'x', 'h-stranger', 'active', UTC_TIMESTAMP(3))`); err != nil {
		t.Fatal(err)
	}
	var x int64
	if err := st.DB().Get(&x, `SELECT id FROM platform_user WHERE handle = 'x'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('sx00000000000000000000000000000x', ?, NULL, 'root')`, x); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := st.SetPasswordAndRevokeSessions(ctx, "x", "h-evil", nil, now); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("reset by bare handle: %v", err)
	}
	if err := st.SetPasswordHashByHandle(ctx, "x", "h-evil"); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("mirror write by bare handle: %v", err)
	}
	if gen, err := st.LegacyCredGen(ctx, "x"); err != nil || gen != 0 {
		t.Fatalf("LegacyCredGen(stranger) = %d, %v", gen, err)
	}
	if _, linked, err := st.TeamOneCredentials(ctx, "x"); err != nil || linked {
		t.Fatalf("TeamOneCredentials(stranger) linked=%v, %v", linked, err)
	}
	if _, err := st.LinkTeamOneAdmin(ctx, "x", "h-admin-x", true, now); !errors.Is(err, platformstore.ErrHandleTaken) {
		t.Fatalf("link onto a stranger: %v", err)
	}
	var got struct {
		Hash    string `db:"password_hash"`
		Gen     int64  `db:"credential_gen"`
		Members int    `db:"members"`
		Live    int    `db:"live"`
	}
	if err := st.DB().Get(&got, `SELECT u.password_hash, u.credential_gen,
  (SELECT COUNT(*) FROM team_member m WHERE m.user_id = u.id) AS members,
  (SELECT COUNT(*) FROM user_session s WHERE s.user_id = u.id AND s.revoked_at IS NULL) AS live
FROM platform_user u WHERE u.id = ?`, x); err != nil {
		t.Fatal(err)
	}
	if got.Hash != "h-stranger" || got.Gen != 0 || got.Members != 0 || got.Live != 1 {
		t.Fatalf("the stranger was touched: %+v", got)
	}

	// A free handle: a new platform user, a team #1 member (owner for a super).
	if c, err := st.LinkTeamOneAdmin(ctx, "newbie", "h-new", false, now); err != nil || c.Gen != 0 {
		t.Fatalf("link a new admin: %+v %v", c, err)
	}
	if c, err := st.LinkTeamOneAdmin(ctx, "chief", "h-chief", true, now); err != nil || c.Gen != 0 {
		t.Fatalf("link a new super: %+v %v", c, err)
	}
	for handle, role := range map[string]string{"newbie": "member", "chief": "owner"} {
		cred, linked, err := st.TeamOneCredentials(ctx, handle)
		if err != nil || !linked || cred.Disabled {
			t.Fatalf("%s: %+v %v %v", handle, cred, linked, err)
		}
		var r string
		if err := st.DB().Get(&r, `SELECT m.role FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE u.handle = ? AND m.team_id = 1`, handle); err != nil || r != role {
			t.Fatalf("%s role %q %v", handle, r, err)
		}
	}
	// Already linked: the password is set through the credential change (generation bumped);
	// a disabled account stays disabled.
	if c, err := st.LinkTeamOneAdmin(ctx, "gone", "h-gone-2", false, now); err != nil || c.Gen != 1 {
		t.Fatalf("link an existing team #1 user: %+v %v", c, err)
	}
	if cred, linked, err := st.TeamOneCredentials(ctx, "gone"); err != nil || !linked || cred.Hash != "h-gone-2" || !cred.Disabled {
		t.Fatalf("relinked disabled user: %+v %v %v", cred, linked, err)
	}
}

// sameHash is a verify that accepts only the given stored hash.
func sameHash(want string) func(string) error {
	return func(cur string) error {
		if cur != want {
			return errors.New("mismatch")
		}
		return nil
	}
}
