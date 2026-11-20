package platformstore_test

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestAIOffByDefaultMigration: 0015 gives every team that exists an explicit ai row, never touches
// a row an operator wrote, and is a no-op on a re-run under the servers' ANSI sql_mode. A team made
// after it has no row, so it follows the new default (ai off).
func TestAIOffByDefaultMigration(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO team (id, slug, name, db_name, status, created_at) VALUES
  (41, 'old-a', 'old a', 'lkb_t_olda', 'active', '2026-11-01'),
  (42, 'old-b', 'old b', 'lkb_t_oldb', 'active', '2026-11-01')`,
		`INSERT INTO team_feature (team_id, feature, enabled, updated_by) VALUES (42, 'ai', 0, 7)`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("sql/0015_ai_off_by_default.sql")
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
	for round := 0; round < 2; round++ {
		for _, q := range strings.Split(up, ";\n") {
			if strings.TrimSpace(stripComments(q)) == "" {
				continue
			}
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("round %d: %v\n%s", round, err, q)
			}
		}
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_feature WHERE team_id = 41 AND feature = 'ai' AND enabled = 1 AND updated_by IS NULL`); n != 1 {
		t.Fatalf("an existing team without a row: %d ai rows", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_feature WHERE team_id = 42 AND feature = 'ai' AND enabled = 0 AND updated_by = 7`); n != 1 {
		t.Fatal("the operator's ai switch was overwritten")
	}
	if _, err := st.DB().Exec(`INSERT INTO team (id, slug, name, db_name, status, created_at) VALUES (43, 'new-c', 'new c', NULL, 'review', '2026-11-20')`); err != nil {
		t.Fatal(err)
	}
	ov, err := st.FeatureOverrides(ctx, 43)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := ov["ai"]; has {
		t.Fatal("a team made after 0015 got an ai row")
	}
}
