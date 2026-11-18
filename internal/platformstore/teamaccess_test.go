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

	sids, err := st.SetPasswordAndRevokeSessions(ctx, "boss", "h-boss-2", time.Now())
	if err != nil || len(sids) != 2 {
		t.Fatalf("SetPasswordAndRevokeSessions: %v %v", sids, err)
	}
	var hash string
	if err := st.DB().Get(&hash, `SELECT password_hash FROM platform_user WHERE id = ?`, boss); err != nil || hash != "h-boss-2" {
		t.Fatalf("hash %q %v", hash, err)
	}
	var live int
	if err := st.DB().Get(&live, `SELECT COUNT(*) FROM user_session WHERE user_id = ? AND revoked_at IS NULL`, boss); err != nil || live != 0 {
		t.Fatalf("%d live sessions after a password change (%v)", live, err)
	}
	if _, err := st.SetPasswordAndRevokeSessions(ctx, "nobody", "h", time.Now()); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("unknown handle: %v", err)
	}
}
