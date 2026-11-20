package platformstore_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// serverSQLMode is the global sql_mode of the DO managed MySQL the control plane runs on (beta and
// prod read 2026-10-25, lekalo beta 2026-11-19); the app sets none, so migrations run under it.
const serverSQLMode = "REAL_AS_FLOAT,PIPES_AS_CONCAT,ANSI_QUOTES,IGNORE_SPACE,ONLY_FULL_GROUP_BY,ANSI," +
	"STRICT_ALL_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION"

// profileMigrationColumns: every column 0009 adds or widens, as COLUMN_TYPE:IS_NULLABLE:COLUMN_DEFAULT.
var profileMigrationColumns = map[string]string{
	"platform_user.status":              "enum('pending','active','disabled','deleted'):NO:NULL",
	"platform_user.first_name":          "varchar(60):NO:",
	"platform_user.last_name":           "varchar(60):NO:",
	"platform_user.public_username":     "varchar(30):YES:NULL",
	"platform_user.password_changed_at": "datetime(3):YES:NULL",
	"platform_user.deleted_at":          "datetime(3):YES:NULL",
	"platform_user.purge_at":            "datetime(3):YES:NULL",
	"platform_user.purged_at":           "datetime(3):YES:NULL",
	"user_session.user_agent":           "varchar(255):YES:NULL",
	"user_session.ip":                   "varchar(45):YES:NULL",
	"team_member.status":                "enum('active','removed','joining','frozen'):NO:NULL",
	"team_member.entry_pending":         "tinyint(1):NO:0",
	"team_invite.name":                  "varchar(120):NO:",
	"oauth_state.target":                "enum('signin','invite','link'):NO:NULL",
	"oauth_state.link_user_id":          "bigint:YES:NULL",
	"oauth_state.return_slug":           "varchar(63):YES:NULL",
	"team.deleted_from":                 "enum('active','suspended','review'):YES:NULL",
	"team.closed_with_user_id":          "bigint:YES:NULL",
	"email_token.purpose":               "enum('verify','reset','handoff','signin','restore'):NO:NULL",
	"platform_email.template": "enum('verify','invite','reset','team_request','team_approved','restore'," +
		"'account_deleted'):NO:NULL",
	"account_avatar.user_id":        "bigint:NO:NULL",
	"account_avatar.png":            "mediumblob:NO:NULL",
	"account_avatar.sha256":         "char(64):NO:NULL",
	"account_avatar.updated_at":     "datetime(3):NO:CURRENT_TIMESTAMP(3)",
	"oauth_link_ticket.ticket_hash": "char(64):NO:NULL",
	"oauth_link_ticket.user_id":     "bigint:NO:NULL",
	"oauth_link_ticket.return_slug": "varchar(63):YES:NULL",
	"oauth_link_ticket.expires_at":  "datetime(3):NO:NULL",
	"oauth_link_ticket.used_at":     "datetime(3):YES:NULL",
	"oauth_link_ticket.created_at":  "datetime(3):NO:CURRENT_TIMESTAMP(3)",
}

// profileMigrationKeys: every key 0009 adds, as NON_UNIQUE:columns.
var profileMigrationKeys = map[string]string{
	"platform_user.uq_platform_user_public_username": "0:public_username",
	"platform_user.idx_platform_user_purge":          "1:purge_at",
	"team.idx_team_closed_with":                      "1:closed_with_user_id",
	"account_avatar.idx_account_avatar_sha":          "1:sha256",
}

// TestProfileMigration: 0009 applied under the servers' sql_mode over a control plane at 0008 that
// holds rows using the last ENUM member of every widened list keeps each row exactly as it was and
// gives the new columns their defaults; the new members, columns, keys and tables work; re-runs,
// also after an interrupted run, are no-ops; the store then opens with nothing to apply.
func TestProfileMigration(t *testing.T) {
	d := freshDSN(t)
	ctx := context.Background()
	db, err := sql.Open("mysql", d+"?parseTime=true&sql_mode="+url.QueryEscape("'"+serverSQLMode+"'"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return n
	}

	set := migrate.MigrationSet{TableName: "platform_migrations"}
	src := migrate.FileMigrationSource{Dir: "sql"}
	if n, err := set.ExecMax(db, "mysql", src, migrate.Up, 8); err != nil || n != 8 {
		t.Fatalf("0001-0008: applied %d, %v", n, err)
	}
	for _, q := range []string{
		`INSERT INTO platform_user (id, email, handle, password_hash, status, credential_gen) VALUES
 (1, 'ann@x.io', 'ann', 'h1', 'active', 3), (2, 'bob@x.io', 'bob', 'h2', 'disabled', 0), (3, NULL, 'cy', 'h3', 'pending', 0)`,
		`INSERT INTO team (id, slug, name, status, deleted_at, purge_at, deleted_from) VALUES
 (2, 'two', 'Two', 'deleted', '2026-11-01 00:00:00', '2026-12-01 00:00:00', 'suspended'),
 (3, 'three', 'Three', 'review', NULL, NULL, NULL)`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES
 (2, 1, 'owner', 'active'), (2, 2, 'member', 'removed'), (3, 3, 'owner', 'joining')`,
		`INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at) VALUES
 (2, 'dee@x.io', 'member', '[]', REPEAT('a', 64), 1, '2030-01-01 00:00:00')`,
		`INSERT INTO email_token (user_id, purpose, token_hash, expires_at) VALUES (1, 'signin', REPEAT('b', 64), '2030-01-01 00:00:00')`,
		`INSERT INTO user_session (sid, user_id, team_id, aud, parent_sid) VALUES (REPEAT('c', 32), 1, 2, 'team', REPEAT('9', 32))`,
		`INSERT INTO oauth_state (state_hash, binder_hash, provider, target, pkce_verifier, nonce, expires_at) VALUES
 (REPEAT('d', 64), REPEAT('e', 64), 'google', 'invite', 'v', 'n', '2030-01-01 00:00:00')`,
		`INSERT INTO platform_email (to_email, template, payload, status) VALUES ('ann@x.io', 'team_approved', '{}', 'pending')`,
	} {
		exec(q)
	}

	// Every row of every touched table, over the columns it had at 0008.
	touched := []string{"platform_user", "team", "team_member", "team_invite", "email_token", "user_session", "oauth_state", "platform_email"}
	oldCols := map[string][]string{}
	for _, tbl := range touched {
		rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			oldCols[tbl] = append(oldCols[tbl], c)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		t.Helper()
		var b strings.Builder
		for _, tbl := range touched {
			cols := oldCols[tbl]
			rows, err := db.QueryContext(ctx, "SELECT `"+strings.Join(cols, "`, `")+"` FROM `"+tbl+"` ORDER BY 1, 2")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				vals := make([]sql.NullString, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					t.Fatal(err)
				}
				b.WriteString(tbl)
				for i, v := range vals {
					b.WriteString(" " + cols[i] + "=")
					if v.Valid {
						b.WriteString("'" + v.String + "'")
					} else {
						b.WriteString("NULL")
					}
				}
				b.WriteString("\n")
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return b.String()
	}
	before := snapshot()

	if n, err := set.ExecMax(db, "mysql", src, migrate.Up, 1); err != nil || n != 1 {
		t.Fatalf("0009: applied %d, %v", n, err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("0009 changed existing rows:\nbefore\n%s\nafter\n%s", before, after)
	}
	// The new columns read as nothing set on every existing row.
	if n := count(`SELECT COUNT(*) FROM platform_user WHERE first_name = '' AND last_name = '' AND public_username IS NULL
 AND password_changed_at IS NULL AND deleted_at IS NULL AND purge_at IS NULL AND purged_at IS NULL`); n != 3 {
		t.Fatalf("platform_user defaults on %d of 3 rows", n)
	}
	if n := count(`SELECT COUNT(*) FROM team_member WHERE entry_pending = 0`); n != 3 {
		t.Fatalf("entry_pending 0 on %d of 3 memberships", n)
	}
	if n := count(`SELECT
 (SELECT COUNT(*) FROM team WHERE closed_with_user_id IS NULL)
 + (SELECT COUNT(*) FROM team_invite WHERE name = '')
 + (SELECT COUNT(*) FROM user_session WHERE user_agent IS NULL AND ip IS NULL)
 + (SELECT COUNT(*) FROM oauth_state WHERE link_user_id IS NULL AND return_slug IS NULL)`); n != 5 {
		t.Fatalf("defaults on %d of 5 team / invite / session / state rows", n)
	}

	checkSchema := func(when string) {
		t.Helper()
		cols, keys := profileSchema(ctx, t, db)
		for k, want := range profileMigrationColumns {
			if cols[k] != want {
				t.Errorf("%s: column %s = %q, want %q", when, k, cols[k], want)
			}
		}
		for k, want := range profileMigrationKeys {
			if keys[k] != want {
				t.Errorf("%s: key %s = %q, want %q", when, k, keys[k], want)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	checkSchema("first apply")

	// The new members and columns take values; the @username key refuses a second holder, never NULLs.
	for _, q := range []string{
		`UPDATE platform_user SET status = 'deleted', deleted_at = UTC_TIMESTAMP(3), purge_at = UTC_TIMESTAMP(3) + INTERVAL 30 DAY WHERE id = 2`,
		`UPDATE platform_user SET public_username = 'ann.k', first_name = 'Ann', last_name = 'Lund', password_changed_at = UTC_TIMESTAMP(3) WHERE id = 1`,
		`UPDATE team_member SET status = 'frozen' WHERE team_id = 2 AND user_id = 1`,
		`UPDATE team_member SET entry_pending = 1 WHERE team_id = 3 AND user_id = 3`,
		`UPDATE team SET deleted_from = 'review', closed_with_user_id = 3 WHERE id = 3`,
		`UPDATE team_invite SET name = 'Dee Moss'`,
		`UPDATE user_session SET user_agent = 'Mozilla/5.0', ip = '2001:db8::1'`,
		`INSERT INTO email_token (user_id, purpose, token_hash, expires_at) VALUES (2, 'restore', REPEAT('f', 64), '2030-01-01 00:00:00')`,
		`INSERT INTO platform_email (to_email, template, payload, status) VALUES
 ('bob@x.io', 'account_deleted', '{}', 'pending'), ('bob@x.io', 'restore', '{}', 'pending')`,
		`INSERT INTO oauth_state (state_hash, binder_hash, provider, target, pkce_verifier, nonce, expires_at, link_user_id, return_slug) VALUES
 (REPEAT('g', 64), REPEAT('h', 64), 'google', 'link', 'v', 'n', '2030-01-01 00:00:00', 1, 'two')`,
		`INSERT INTO account_avatar (user_id, png, sha256) VALUES (1, 'png', REPEAT('0', 64))`,
		`INSERT INTO oauth_link_ticket (ticket_hash, user_id, return_slug, expires_at) VALUES (REPEAT('1', 64), 1, 'two', '2030-01-01 00:00:00')`,
	} {
		exec(q)
	}
	_, err = db.ExecContext(ctx, `UPDATE platform_user SET public_username = 'ann.k' WHERE id = 3`)
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		t.Fatalf("a second holder of @ann.k: %v, want a duplicate-key error", err)
	}
	const filled = `SELECT
 (SELECT COUNT(*) FROM platform_user WHERE id = 2 AND status = 'deleted' AND purge_at > deleted_at)
 + (SELECT COUNT(*) FROM platform_user WHERE id = 1 AND public_username = 'ann.k' AND first_name = 'Ann')
 + (SELECT COUNT(*) FROM team_member WHERE status = 'frozen')
 + (SELECT COUNT(*) FROM team_member WHERE entry_pending = 1)
 + (SELECT COUNT(*) FROM team WHERE deleted_from = 'review' AND closed_with_user_id = 3)
 + (SELECT COUNT(*) FROM email_token WHERE purpose = 'restore')
 + (SELECT COUNT(*) FROM platform_email WHERE template IN ('account_deleted', 'restore'))
 + (SELECT COUNT(*) FROM oauth_state WHERE target = 'link' AND link_user_id = 1)
 + (SELECT COUNT(*) FROM account_avatar)
 + (SELECT COUNT(*) FROM oauth_link_ticket)`
	if n := count(filled); n != 11 {
		t.Fatalf("new values: %d of 11", n)
	}

	// Re-runs on one session in the servers' sql_mode: twice, then after an interrupted run that left
	// a key, a column and a table missing.
	raw, err := os.ReadFile("sql/0009_profile.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	up = up[strings.Index(up, "-- +migrate Up"):strings.Index(up, "-- +migrate Down")]
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	apply := func(round string) {
		t.Helper()
		for _, q := range strings.Split(up, ";\n") {
			if strings.TrimSpace(stripComments(q)) == "" {
				continue
			}
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v\n%s", round, err, q)
			}
		}
	}
	apply("second run")
	apply("third run")
	checkSchema("re-runs")
	for _, q := range []string{
		`ALTER TABLE platform_user DROP KEY uq_platform_user_public_username`,
		`ALTER TABLE platform_user DROP COLUMN purged_at`,
		`DROP TABLE oauth_link_ticket`,
	} {
		exec(q)
	}
	apply("after an interrupted run")
	checkSchema("after the interrupted run")
	if n := count(filled); n != 10 { // oauth_link_ticket came back empty
		t.Fatalf("new values after the re-runs: %d of 10", n)
	}

	st, err := platformstore.Open(ctx, d)
	if err != nil {
		t.Fatalf("open after 0009: %v", err)
	}
	defer st.Close()
	if n := count(`SELECT COUNT(*) FROM platform_migrations`); n != 10 { // Open applies 0010 (X6) too
		t.Fatalf("platform_migrations rows = %d, want 10", n)
	}
}

// profileSchema reads COLUMN_TYPE:IS_NULLABLE:COLUMN_DEFAULT of every column and NON_UNIQUE:columns
// of every key of the tables 0009 touches, keyed table.column and table.key.
func profileSchema(ctx context.Context, t *testing.T, db *sql.DB) (map[string]string, map[string]string) {
	t.Helper()
	cols := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tbl, col, typ, null string
		var def sql.NullString
		if err := rows.Scan(&tbl, &col, &typ, &null, &def); err != nil {
			t.Fatal(err)
		}
		v := "NULL"
		if def.Valid {
			v = def.String
		}
		cols[tbl+"."+col] = typ + ":" + null + ":" + v
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	rows, err = db.QueryContext(ctx, `SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME
FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`)
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string][]string{}
	unique := map[string]string{}
	for rows.Next() {
		var tbl, idx, nonUnique, col string
		if err := rows.Scan(&tbl, &idx, &nonUnique, &col); err != nil {
			t.Fatal(err)
		}
		k := tbl + "." + idx
		parts[k] = append(parts[k], col)
		unique[k] = nonUnique
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(parts))
	for k := range parts {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		keys[k] = unique[k] + ":" + strings.Join(parts[k], ",")
	}
	return cols, keys
}
