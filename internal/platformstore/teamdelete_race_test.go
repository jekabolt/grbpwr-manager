package platformstore_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// deletePaths are the transactions that create a team session, a hand-off code or a membership
// from an invite. Each runs against team 2 (acme) of seedDelete and returns nil only when it
// created what it creates; tag makes every hash and sid of one run unique.
func deletePaths(st *platformstore.Store, now time.Time) map[string]func(ctx context.Context, tag string) error {
	h := func(tag, c string) string { return strings.Repeat(c, 64-len(tag)) + tag }
	sid := func(tag, who string) string { return who + strings.Repeat("0", 32-len(who)-len(tag)) + tag }
	return map[string]func(context.Context, string) error{
		"session": func(ctx context.Context, tag string) error {
			ok, err := st.CreateTeamSession(ctx, sid(tag, "sann"), 101, 2, 0, now)
			if err == nil && !ok {
				return errors.New("refused")
			}
			return err
		},
		"issue hand-off": func(ctx context.Context, tag string) error {
			return st.IssueHandoff(ctx, 102, "sbob1000000000000000000000000000",
				platformstore.NewToken{TeamID: 2, TokenHash: h(tag, "e"), ExpiresAt: now.Add(time.Hour)}, now)
		},
		"exchange hand-off": func(ctx context.Context, tag string) error {
			_, err := st.ExchangeHandoff(ctx, h(tag, "f"), 2, sid(tag, "sbobx"), now)
			return err
		},
		"operator enter": func(ctx context.Context, tag string) error {
			_, err := st.ExchangeOperatorHandoff(ctx, h(tag, "g"), 2, sid(tag, "sopx"), now)
			return err
		},
		"accept invite": func(ctx context.Context, tag string) error {
			_, err := st.AcceptInvite(ctx, platformstore.AcceptParams{TokenHash: h(tag, "i"), Now: now,
				NewPasswordHash: "h", HandleBase: "joiner", TermsVersion: "2026-11",
				Handoff: platformstore.NewToken{TokenHash: h(tag, "j"), ExpiresAt: now.Add(time.Minute)}})
			return err
		},
	}
}

// plantLive writes, straight into the tables, the live codes and invite a path of run tag consumes:
// they are there although the team is deleted, as rows written by a racing transaction would be.
func plantLive(t *testing.T, st *platformstore.Store, tag string) {
	t.Helper()
	h := func(c string) string { return strings.Repeat(c, 64-len(tag)) + tag }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO email_token (user_id, team_id, purpose, token_hash, expires_at) VALUES (102, 2, 'handoff', ?, '2099-01-01'), (103, 2, 'handoff', ?, '2099-01-01')`,
			[]any{h("f"), h("g")}},
		{`INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at) VALUES (2, ?, 'member', '[]', ?, 101, '2099-01-01')`,
			[]any{"joiner" + tag + "@x.io", h("i")}},
	} {
		if _, err := st.DB().Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

// noTrace: run tag of a refused path left no session, no hand-off and no membership behind.
func noTrace(t *testing.T, st *platformstore.Store, name, tag string) {
	t.Helper()
	var n int
	if err := st.DB().Get(&n, `SELECT
 (SELECT COUNT(*) FROM user_session WHERE team_id = 2 AND RIGHT(sid, ?) = ? AND revoked_at IS NULL) * 100
 + (SELECT COUNT(*) FROM email_token WHERE purpose = 'handoff' AND RIGHT(token_hash, ?) = ? AND used_at IS NULL AND token_hash LIKE 'e%') * 10
 + (SELECT COUNT(*) FROM team_member m JOIN platform_user u ON u.id = m.user_id WHERE m.team_id = 2 AND u.email = ?)`,
		len(tag), tag, len(tag), tag, "joiner"+tag+"@x.io"); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%s on a deleted team left sessions/codes/members = %d", name, n)
	}
}

// TestDeletedTeamRefusesAtStore: on a deleted team every creating path refuses inside the store,
// not only at the edge — also with a live code or invite that escaped the delete's revocation.
func TestDeletedTeamRefusesAtStore(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedDelete(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention); err != nil {
		t.Fatal(err)
	}
	plantLive(t, st, "d1")
	want := map[string]error{
		"issue hand-off":    platformstore.ErrWrongTeamStatus,
		"exchange hand-off": platformstore.ErrTokenInvalid,
		"operator enter":    platformstore.ErrTokenInvalid,
		"accept invite":     platformstore.ErrTokenInvalid,
	}
	for name, run := range deletePaths(st, now) {
		err := run(ctx, "d1")
		if name == "session" {
			if err == nil || err.Error() != "refused" {
				t.Fatalf("session on a deleted team: %v", err)
			}
		} else if !errors.Is(err, want[name]) {
			t.Fatalf("%s on a deleted team: %v (want %v)", name, err, want[name])
		}
		noTrace(t, st, name, "d1")
	}
}

// TestCreatingPathWaitsForDelete orders the race deterministically: a transaction holds the team
// row FOR UPDATE and flips it to deleted (only the status, nothing revoked, so nothing but the team
// row can refuse), each creating path starts and must wait on the team row, and after the commit it
// sees the delete and refuses. Before D1b these paths never read the team under a lock and created
// their session / code right after the commit.
func TestCreatingPathWaitsForDelete(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedDelete(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	paths := deletePaths(st, now)
	i := 0
	for name, run := range paths {
		i++
		tag := "r" + string(rune('0'+i))
		plantLive(t, st, tag)

		tx, err := st.DB().BeginTxx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var cur string
		if err := tx.Get(&cur, `SELECT status FROM team WHERE id = 2 FOR UPDATE`); err != nil || cur != "active" {
			t.Fatalf("%s: lock %q %v", name, cur, err)
		}
		if _, err := tx.Exec(`UPDATE team SET status = 'deleted', deleted_at = ?, purge_at = ?, deleted_from = 'active' WHERE id = 2`,
			now, now.Add(platformstore.DeleteRetention)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- run(ctx, tag) }()
		select {
		case err := <-done:
			_ = tx.Rollback()
			t.Fatalf("%s did not wait for the delete holding the team row: %v", name, err)
		case <-time.After(400 * time.Millisecond):
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("%s still waits after the delete committed", name)
		}
		if err == nil {
			t.Fatalf("%s created its row after the delete committed", name)
		}
		noTrace(t, st, name, tag)

		if _, err := st.DB().Exec(`UPDATE team SET status = 'active', deleted_at = NULL, purge_at = NULL, deleted_from = NULL WHERE id = 2`); err != nil {
			t.Fatal(err)
		}
	}
	// Back to active, every path works again (the refusals above came from the status alone).
	plantLive(t, st, "ok")
	for name, run := range paths {
		if err := run(ctx, "ok"); err != nil {
			t.Fatalf("%s on an active team: %v", name, err)
		}
	}
}

// TestDeleteRestoreReturnIdentity: both answer with the team as their transaction locked it.
func TestDeleteRestoreReturnIdentity(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedDelete(t, st)
	now := time.Now().UTC()
	if err := st.RenameTeam(ctx, 2, "acme-two", "Acme Two", now); err != nil {
		t.Fatal(err)
	}
	del, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention)
	if err != nil || del.Slug != "acme-two" || del.Name != "Acme Two" {
		t.Fatalf("delete identity %+v %v", del, err)
	}
	rs, err := st.RestoreTeam(ctx, 2, now)
	if err != nil || rs.Slug != "acme-two" || rs.Name != "Acme Two" || rs.Status != "active" {
		t.Fatalf("restore identity %+v %v", rs, err)
	}
	if tm, err := st.ConsoleTeamByID(ctx, 2); err != nil || tm.Slug != "acme-two" {
		t.Fatalf("by id %+v %v", tm, err)
	}
}

// TestTeamDeleteMigrationRerun: 0005 applied again over a migrated schema, and after a run that
// stopped half-way (one column and one key missing), under the servers' ANSI sql_mode, is a no-op
// in the first case and completes the schema in the second.
func TestTeamDeleteMigrationRerun(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	raw, err := os.ReadFile("sql/0005_team_delete.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	up = up[strings.Index(up, "-- +migrate Up"):strings.Index(up, "-- +migrate Down")]
	conn, err := st.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET SESSION sql_mode = 'ANSI'`); err != nil {
		t.Fatal(err)
	}
	apply := func(round string) {
		t.Helper()
		// The migrator sends one statement at a time on one connection; so does this.
		for _, q := range strings.Split(up, ";\n") {
			if strings.TrimSpace(stripComments(q)) == "" {
				continue
			}
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v\n%s", round, err, q)
			}
		}
	}
	present := func() int {
		t.Helper()
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team'
   AND COLUMN_NAME IN ('deleted_at', 'purge_at', 'deleted_from'))
 + (SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE()
   AND INDEX_NAME IN ('idx_user_session_team', 'idx_email_token_team'))`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	apply("twice")
	apply("three times")
	if n := present(); n != 5 {
		t.Fatalf("after re-runs: %d of 5 columns and keys", n)
	}
	for _, q := range []string{`ALTER TABLE team DROP COLUMN deleted_from`, `ALTER TABLE email_token DROP KEY idx_email_token_team`} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	apply("after an interrupted run")
	if n := present(); n != 5 {
		t.Fatalf("after the interrupted run: %d of 5 columns and keys", n)
	}
}

func stripComments(q string) string {
	var b strings.Builder
	for _, line := range strings.Split(q, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			b.WriteString(line)
		}
	}
	return b.String()
}
