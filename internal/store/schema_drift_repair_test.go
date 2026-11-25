package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrationRepairGrbpwrSchemaDrift builds the shape of the databases inherited from the old
// grbpwr prod (address.country_code + index missing, review ratings NOT NULL, order_item FK indexes
// named product_id / size_id) on a fully migrated team database, runs the repair file under
// ANSI_QUOTES and requires the schema fingerprint lines of a clean database. A second Up is a no-op,
// Down is a no-op, and the backfill follows 0053.
func TestMigrationRepairGrbpwrSchemaDrift(t *testing.T) {
	requireGarmentLabelGeometryDB(t) // local container or CI only
	const db = "drift_repair_test"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	drop := func() { _, _ = testDB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+db+"`") }
	drop()
	t.Cleanup(drop)
	_, err := testDB.ExecContext(ctx, "CREATE DATABASE `"+db+"` CHARACTER SET utf8mb4")
	require.NoError(t, err)
	_, err = MigrateTeam(ctx, testCfg.DSN, db)
	require.NoError(t, err)

	clean, err := DSNDatabase(testCfg.DSN)
	require.NoError(t, err)
	want, err := SchemaLines(ctx, testCfg.DSN, clean)
	require.NoError(t, err)
	got, err := SchemaLines(ctx, testCfg.DSN, db)
	require.NoError(t, err)
	require.Equal(t, want, got, "a freshly migrated team database equals the suite database")

	files, err := filepath.Glob("sql/*_repair_grbpwr_schema_drift.sql")
	require.NoError(t, err)
	require.Len(t, files, 1)
	f, err := os.Open(files[0])
	require.NoError(t, err)
	parsed, err := sqlparse.ParseMigration(f)
	require.NoError(t, f.Close())
	require.NoError(t, err)

	dsn, err := TeamDSN(testCfg.DSN, db)
	require.NoError(t, err)
	raw, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer raw.Close()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')`)
	require.NoError(t, err)
	run := func(name string, statements []string) {
		t.Helper()
		for i, statement := range statements {
			_, err := conn.ExecContext(ctx, statement)
			require.NoErrorf(t, err, "%s statement %d", name, i)
		}
	}
	lines := func() []string {
		t.Helper()
		got, err := SchemaLines(ctx, testCfg.DSN, db)
		require.NoError(t, err)
		return got
	}

	run("up on a chain-built database", parsed.UpStatements)
	require.Equal(t, want, lines(), "no-op on a database that already matches the chain")

	// The inherited drift, as information_schema showed it on lk_grbpwr (25.11).
	run("drift", []string{
		`DROP INDEX idx_address_country_code ON address`,
		`ALTER TABLE address DROP COLUMN country_code`,
		`ALTER TABLE order_review
			MODIFY COLUMN delivery_rating ENUM('much_faster_than_expected', 'faster_than_expected', 'as_expected', 'slower_than_expected', 'much_slower_than_expected') NOT NULL,
			MODIFY COLUMN packaging_rating ENUM('damaged', 'acceptable', 'good', 'excellent') NOT NULL`,
		`ALTER TABLE order_item_review
			MODIFY COLUMN rating ENUM('poor', 'fair', 'good', 'very_good', 'excellent') NOT NULL,
			MODIFY COLUMN fit_rating ENUM('runs_small', 'slightly_small', 'true_to_size', 'slightly_large', 'runs_large') NOT NULL,
			MODIFY COLUMN recommend BOOLEAN NOT NULL DEFAULT 1`,
		`ALTER TABLE order_item RENAME INDEX fk_order_item_product TO product_id`,
		`ALTER TABLE order_item RENAME INDEX fk_order_item_size TO size_id`,
		`INSERT INTO address (country, city, address_line_one, postal_code) VALUES
			('de', 'berlin', 'a 1', '10115'), ('string', 'x', 'a 2', '1'), ('Germany', 'y', 'a 3', '2')`,
	})
	drifted := lines()
	require.NotEqual(t, want, drifted)
	t.Logf("drift before repair: %v", FirstSchemaDiff(want, drifted, 20))

	run("up on the drifted database", parsed.UpStatements)
	got = lines()
	assert.Equal(t, want, got, "repaired database must equal a clean one: %v", FirstSchemaDiff(want, got, 20))

	rows, err := conn.QueryContext(ctx, `SELECT country, COALESCE(country_code, '-') FROM address ORDER BY id`)
	require.NoError(t, err)
	backfill := map[string]string{}
	for rows.Next() {
		var country, code string
		require.NoError(t, rows.Scan(&country, &code))
		backfill[country] = code
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Equal(t, map[string]string{"de": "DE", "string": "-", "Germany": "-"}, backfill, "backfill as 0053")

	// review.go writes NULL ratings: they must be accepted now.
	_, err = conn.ExecContext(ctx, `INSERT INTO order_item_review (order_item_id) VALUES (999999)`)
	require.Error(t, err, "FK still holds (no such order_item)")
	assert.NotContains(t, err.Error(), "doesn't have a default value")

	run("up retry", parsed.UpStatements)
	require.Equal(t, want, lines())
	run("down", parsed.DownStatements)
	require.Equal(t, want, lines(), "down is a no-op")
	run("up after down", parsed.UpStatements)
	require.Equal(t, want, lines())
}
