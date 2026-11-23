package platformstore_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

const wipeFK = "0123456789abcdef0123456789abcdef"

// seedWipe: seedConsole plus a due deleted team "xgone" (8) with a database, files, a claimed spare
// row and rows in the per-team tables; a deleted team "soon" (7) still in its window; a closing team
// "xclose" (9) closed with a deleted, not yet purged account (104).
func seedWipe(t *testing.T, st *platformstore.Store) {
	t.Helper()
	seedConsole(t, st)
	for _, q := range []string{
		`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES (104, 'cid@x.io', 'cid', 'h', 'active')`,
		`UPDATE platform_user SET status = 'deleted', deleted_at = '2026-01-01', purge_at = '2026-01-31' WHERE id = 104`,
		`INSERT INTO team (id, slug, name, db_name, file_key, status, purge_at, deleted_from) VALUES
 (7, 'soon', 'Soon', 'lk_t_soon0001', NULL, 'deleted', '2099-01-01', 'active'),
 (8, 'xgone', 'Gone', 'lk_t_xgon0001', '` + wipeFK + `', 'deleted', '2026-01-01', 'active'),
 (9, 'xclose', 'Close', NULL, NULL, 'deleted', '2026-01-01', 'active')`,
		`UPDATE team SET closed_with_user_id = 104 WHERE id = 9`,
		`INSERT INTO spare_db (db_name, status, claimed_team_id, claimed_at) VALUES ('lk_t_xgon0001', 'claimed', 8, '2025-12-01'),
 ('lk_t_acme0001', 'claimed', 2, '2025-12-01')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (8, 102, 'owner', 'removed'), (9, 104, 'owner', 'frozen')`,
		`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('sgon0000000000000000000000000000', 102, 8, 'team')`,
		`INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at) VALUES
 (8, 'z@x.io', 'member', '[]', REPEAT('e', 64), 102, '2099-01-01')`,
		`INSERT INTO team_feature (team_id, feature, enabled) VALUES (8, 'ai', 1), (2, 'ai', 1)`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func teamRows(t *testing.T, st *platformstore.Store, teamID int64) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"team_member", "user_session", "team_invite", "team_feature", "email_token", "team_logo", "team_brand"} {
		var n int
		if err := st.DB().Get(&n, `SELECT COUNT(*) FROM `+table+` WHERE team_id = ?`, teamID); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func TestTeamWipeNeverTeamOne(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedWipe(t, st)
	// Even forced into a due deleted state by hand, team #1 is never listed, previewed, claimed or finished.
	if _, err := st.DB().Exec(`UPDATE team SET status = 'deleted', purge_at = '2026-01-01' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	ids, err := st.DueTeamWipes(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == 1 {
			t.Fatal("team #1 listed as due")
		}
	}
	if _, _, err := st.ClaimTeamWipe(ctx, 1, time.Now()); !errors.Is(err, platformstore.ErrTeamOne) {
		t.Fatalf("claim team #1: %v", err)
	}
	if _, err := st.PreviewTeamWipe(ctx, 1); !errors.Is(err, platformstore.ErrTeamOne) {
		t.Fatalf("preview team #1: %v", err)
	}
	if _, err := st.FinishTeamWipe(ctx, 1, time.Now()); !errors.Is(err, platformstore.ErrTeamOne) {
		t.Fatalf("finish team #1: %v", err)
	}
	if err := st.MarkWipeDBDropped(ctx, 1, time.Now()); !errors.Is(err, platformstore.ErrTeamOne) {
		t.Fatalf("mark team #1: %v", err)
	}
	var db string
	var purge *time.Time
	if err := st.DB().QueryRow(`SELECT db_name, purge_at FROM team WHERE id = 1`).Scan(&db, &purge); err != nil || db != "grbpwr_test" || purge == nil {
		t.Fatalf("team #1 row changed: %q %v %v", db, purge, err)
	}
	var n int
	_ = st.DB().Get(&n, `SELECT COUNT(*) FROM team_wipe WHERE team_id = 1`)
	if n != 0 {
		t.Fatal("team #1 has a wipe row")
	}
}

func TestTeamWipeClaimAndFinish(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedWipe(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	acmeBefore := teamRows(t, st, 2)

	ids, err := st.DueTeamWipes(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 8 || ids[1] != 9 {
		t.Fatalf("due %v, want [8 9] (never live, review, provisioning or in-window teams)", ids)
	}
	for _, id := range []int64{2, 3, 4, 7} {
		if _, ok, err := st.ClaimTeamWipe(ctx, id, now); err != nil || ok {
			t.Fatalf("team %d claimed (%v %v)", id, ok, err)
		}
	}
	p, err := st.PreviewTeamWipe(ctx, 8)
	if err != nil || p.DBName != "lk_t_xgon0001" || p.FileKey != wipeFK || p.Rows["team_member"] != 1 {
		t.Fatalf("preview %+v %v", p, err)
	}

	w, ok, err := st.ClaimTeamWipe(ctx, 8, now)
	if err != nil || !ok || !w.DBPending() || !w.FilesPending() {
		t.Fatalf("claim %+v %v %v", w, ok, err)
	}
	var (
		purge, db, fk *string
		status        string
	)
	if err := st.DB().QueryRow(`SELECT status, purge_at, db_name, file_key FROM team WHERE id = 8`).Scan(&status, &purge, &db, &fk); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || purge != nil || db != nil || fk != nil {
		t.Fatalf("team row after claim: %s %v %v %v", status, purge, db, fk)
	}
	var n int
	_ = st.DB().Get(&n, `SELECT COUNT(*) FROM spare_db WHERE db_name = 'lk_t_xgon0001'`)
	if n != 0 {
		t.Fatal("claimed spare row kept")
	}
	_ = st.DB().Get(&n, `SELECT COUNT(*) FROM spare_db WHERE db_name = 'lk_t_acme0001'`)
	if n != 1 {
		t.Fatal("another team's spare row deleted")
	}
	if ref, err := st.DBNameReferenced(ctx, "lk_t_xgon0001"); err != nil || ref {
		t.Fatalf("db still referenced %v %v", ref, err)
	}
	if ref, _ := st.DBNameReferenced(ctx, "lk_t_acme0001"); !ref {
		t.Fatal("acme database not referenced")
	}
	if ref, err := st.FileKeyReferenced(ctx, wipeFK); err != nil || ref {
		t.Fatalf("file key still referenced %v %v", ref, err)
	}
	if tomb, err := st.TeamWipeTombstone(ctx, 8); err != nil || !tomb {
		t.Fatalf("claimed team not a tombstone %v %v", tomb, err)
	}
	for _, id := range []int64{1, 2, 7, 404} {
		if tomb, err := st.TeamWipeTombstone(ctx, id); err != nil || tomb {
			t.Fatalf("team %d read as a tombstone %v %v", id, tomb, err)
		}
	}
	// A file key a team row names again (set by hand) is referenced, and that row is no tombstone.
	if _, err := st.DB().Exec(`UPDATE team SET file_key = ? WHERE id = 8`, wipeFK); err != nil {
		t.Fatal(err)
	}
	if ref, _ := st.FileKeyReferenced(ctx, wipeFK); !ref {
		t.Fatal("file key named by a team row not referenced")
	}
	if tomb, _ := st.TeamWipeTombstone(ctx, 8); tomb {
		t.Fatal("row with a file key read as a tombstone")
	}
	if _, err := st.DB().Exec(`UPDATE team SET file_key = NULL WHERE id = 8`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimTeamWipe(ctx, 8, now); err != nil || ok {
		t.Fatalf("second claim %v %v", ok, err)
	}
	if _, err := st.RestoreTeam(ctx, 8, now); err == nil {
		t.Fatal("restore after claim")
	}
	if ids, _ := st.DueTeamWipes(ctx, 50); len(ids) != 1 || ids[0] != 9 {
		t.Fatalf("due after claim %v", ids)
	}

	if _, err := st.FinishTeamWipe(ctx, 8, now); !errors.Is(err, platformstore.ErrWipeNotReady) {
		t.Fatalf("finish before steps: %v", err)
	}
	if _, err := st.MarkWipeFailed(ctx, 8, "files: boom", true, now); err != nil {
		t.Fatal(err)
	}
	if open, _ := st.OpenTeamWipes(ctx, 10); len(open) != 0 {
		t.Fatal("failed wipe retried before its backoff")
	}
	if err := st.MarkWipeFilesDone(ctx, 8, 3, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkWipeDBDropped(ctx, 8, now); err != nil {
		t.Fatal(err)
	}
	res, err := st.FinishTeamWipe(ctx, 8, now)
	if err != nil || !res.Finished || res.Slug != "xgone" {
		t.Fatalf("finish %+v %v", res, err)
	}
	for table, c := range teamRows(t, st, 8) {
		if c != 0 {
			t.Fatalf("%s keeps %d rows of the wiped team", table, c)
		}
	}
	if got := teamRows(t, st, 2); len(got) != len(acmeBefore) || got["team_member"] != acmeBefore["team_member"] ||
		got["user_session"] != acmeBefore["user_session"] || got["team_feature"] != acmeBefore["team_feature"] {
		t.Fatalf("acme rows changed %v → %v", acmeBefore, got)
	}
	var slug string
	_ = st.DB().Get(&slug, `SELECT slug FROM team WHERE id = 8`)
	if slug != "_w8" {
		t.Fatalf("tombstone slug %q", slug)
	}
	if taken, err := st.SlugTaken(ctx, "xgone", now.Add(89*24*time.Hour)); err != nil || !taken {
		t.Fatalf("address free inside 90 days (%v %v)", taken, err)
	}
	if taken, _ := st.SlugTaken(ctx, "xgone", now.Add(91*24*time.Hour)); taken {
		t.Fatal("address still held after 90 days")
	}
	_ = st.DB().Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 8 AND action IN ('team_wipe_start', 'team_wipe')`)
	if n != 2 {
		t.Fatalf("audit rows %d", n)
	}
	if again, err := st.FinishTeamWipe(ctx, 8, now); err != nil || again.Finished {
		t.Fatalf("second finish %+v %v", again, err)
	}
}

// A team that closed with an account waits until the account is purged.
func TestTeamWipeWaitsForAccount(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedWipe(t, st)
	now := time.Now().UTC()
	if p, err := st.PreviewTeamWipe(ctx, 9); err != nil || !p.ClosedWithPending {
		t.Fatalf("preview %+v %v", p, err)
	}
	if _, ok, err := st.ClaimTeamWipe(ctx, 9, now); err != nil || ok {
		t.Fatalf("claimed before the account purge (%v %v)", ok, err)
	}
	purged, due, err := st.PurgeDeletedAccounts(ctx, 50, func() (string, error) { return "!unusable", nil })
	if err != nil || purged != 1 || due != 1 {
		t.Fatalf("purge %d/%d %v", purged, due, err)
	}
	w, ok, err := st.ClaimTeamWipe(ctx, 9, now)
	if err != nil || !ok || w.DBPending() || w.FilesPending() {
		t.Fatalf("claim after purge %+v %v %v", w, ok, err)
	}
	if res, err := st.FinishTeamWipe(ctx, 9, now); err != nil || !res.Finished {
		t.Fatalf("finish %+v %v", res, err)
	}
}

// Restore and claim racing: exactly one wins; a restored team is never claimed, a claimed one never restored.
func TestTeamWipeRestoreRace(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedWipe(t, st)
	// purge_at just passed on the database clock (the claim's check); the restore runs with a "now"
	// an hour earlier, so both are allowed on their own and the row lock decides.
	if _, err := st.DB().Exec(`UPDATE team SET purge_at = UTC_TIMESTAMP(3) - INTERVAL 1 SECOND WHERE id = 8`); err != nil {
		t.Fatal(err)
	}
	restoreAt := time.Now().UTC().Add(-time.Hour)
	var (
		wg              sync.WaitGroup
		claimed         bool
		claimErr, rsErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); _, claimed, claimErr = st.ClaimTeamWipe(ctx, 8, time.Now()) }()
	go func() { defer wg.Done(); _, rsErr = st.RestoreTeam(ctx, 8, restoreAt) }()
	wg.Wait()
	if claimErr != nil && !errors.Is(claimErr, platformstore.ErrTryAgain) {
		t.Fatalf("claim: %v", claimErr)
	}
	restored := rsErr == nil
	if claimed == restored {
		t.Fatalf("claimed %v restored %v (claim err %v, restore err %v)", claimed, restored, claimErr, rsErr)
	}
	var status string
	_ = st.DB().Get(&status, `SELECT status FROM team WHERE id = 8`)
	if restored && status == "deleted" || claimed && status != "deleted" {
		t.Fatalf("status %s after claimed=%v restored=%v", status, claimed, restored)
	}
}

func TestAccountPurge(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedWipe(t, st)
	db := st.DB()
	for _, q := range []string{
		// 105: deleted, still in its window; 106: deleted operator, due; bob (102) active.
		`INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator) VALUES
 (105, 'dee@x.io', 'dee', 'h', 'active', 0), (106, 'opx@x.io', 'opx', 'h', 'active', 1)`,
		`UPDATE platform_user SET status = 'deleted', deleted_at = UTC_TIMESTAMP(3), purge_at = '2099-01-01' WHERE id = 105`,
		`UPDATE platform_user SET status = 'deleted', deleted_at = '2026-01-01', purge_at = '2026-01-31' WHERE id = 106`,
		`UPDATE platform_user SET first_name = 'Cid', last_name = 'C', public_username = 'cidc', email_verified_at = '2026-01-01' WHERE id = 104`,
		`INSERT INTO user_session (sid, user_id, team_id, aud, ip, user_agent) VALUES ('scid0000000000000000000000000000', 104, NULL, 'root', '1.2.3.4', 'UA')`,
		`INSERT INTO email_token (user_id, team_id, purpose, token_hash, expires_at) VALUES (104, NULL, 'restore', REPEAT('f', 64), '2099-01-01')`,
		`INSERT INTO platform_audit (actor_user_id, team_id, action, ip, created_at) VALUES (104, NULL, 'sign_in', '1.2.3.4', UTC_TIMESTAMP(3))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	due, err := st.DueAccountPurges(ctx, 50)
	if err != nil || len(due) != 1 || due[0].Handle != "cid" {
		t.Fatalf("due %+v %v", due, err)
	}
	hashes := 0
	gen := func() (string, error) { hashes++; return "!unusable", nil }
	if purged, _, err := st.PurgeDeletedAccounts(ctx, 50, gen); err != nil || purged != 1 || hashes != 1 {
		t.Fatalf("purge %d %v (hashes %d)", purged, err, hashes)
	}
	var u struct {
		Email    *string    `db:"email"`
		First    string     `db:"first_name"`
		Last     string     `db:"last_name"`
		Public   *string    `db:"public_username"`
		Hash     string     `db:"password_hash"`
		Verified *time.Time `db:"email_verified_at"`
		Handle   string     `db:"handle"`
		Status   string     `db:"status"`
		PurgeAt  *time.Time `db:"purge_at"`
		PurgedAt *time.Time `db:"purged_at"`
		Deleted  *time.Time `db:"deleted_at"`
		Gen      int64      `db:"credential_gen"`
	}
	if err := db.Get(&u, `SELECT email, first_name, last_name, public_username, password_hash, email_verified_at, handle, status,
purge_at, purged_at, deleted_at, credential_gen FROM platform_user WHERE id = 104`); err != nil {
		t.Fatal(err)
	}
	if u.Email != nil || u.First != "" || u.Last != "" || u.Public != nil || u.Hash != "!unusable" || u.Verified != nil ||
		u.Handle != "cid" || u.Status != "deleted" || u.PurgeAt != nil || u.PurgedAt == nil || u.Deleted == nil || u.Gen != 1 {
		t.Fatalf("purged row %+v", u)
	}
	var n int
	_ = db.Get(&n, `SELECT COUNT(*) FROM email_token WHERE user_id = 104`)
	if n != 0 {
		t.Fatal("tokens kept")
	}
	_ = db.Get(&n, `SELECT COUNT(*) FROM user_session WHERE user_id = 104 AND (ip IS NOT NULL OR user_agent IS NOT NULL)`)
	if n != 0 {
		t.Fatal("session device kept")
	}
	_ = db.Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE actor_user_id = 104 AND ip IS NOT NULL`)
	if n != 0 {
		t.Fatal("audit ip kept")
	}
	_ = db.Get(&n, `SELECT COUNT(*) FROM team_member WHERE user_id = 104 AND status = 'frozen'`)
	if n != 0 {
		t.Fatal("frozen membership kept")
	}
	_ = db.Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE action = 'account_purge'`)
	if n != 1 {
		t.Fatalf("purge audit rows %d", n)
	}
	// In-window, operator and active accounts untouched; a rerun is a no-op.
	var left int
	_ = db.Get(&left, `SELECT COUNT(*) FROM platform_user WHERE id IN (102, 105, 106) AND email IS NOT NULL AND purged_at IS NULL`)
	if left != 3 {
		t.Fatalf("untouched accounts %d of 3", left)
	}
	if ok, err := st.PurgeAccount(ctx, 104, "!again"); err != nil || ok {
		t.Fatalf("second purge %v %v", ok, err)
	}
	if ok, err := st.PurgeAccount(ctx, 106, "!x"); err != nil || ok {
		t.Fatalf("operator purged %v %v", ok, err)
	}
	// The address is free for a new account.
	if _, err := db.Exec(`INSERT INTO platform_user (email, handle, password_hash, status) VALUES ('cid@x.io', 'cid2', 'h', 'active')`); err != nil {
		t.Fatalf("email not free after purge: %v", err)
	}
}
