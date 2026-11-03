package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lockTestLockWaitSeconds = 2

var lockTestSet = migrate.MigrationSet{TableName: "lock_test_migrations"}

func guardedAddColumn(table, column string) []string {
	return []string{
		fmt.Sprintf(`SET @ddl = (SELECT IF(COUNT(*) = 0, 'ALTER TABLE %[1]s ADD COLUMN %[2]s INT NULL, ALGORITHM=INSTANT', 'SELECT 1') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%[1]s' AND COLUMN_NAME = '%[2]s')`, table, column),
		`PREPARE stmt FROM @ddl`,
		`EXECUTE stmt`,
		`DEALLOCATE PREPARE stmt`,
	}
}

func setupLockTestTables(t *testing.T, tables ...string) {
	t.Helper()
	drop := func() {
		for _, table := range append([]string{"lock_test_migrations"}, tables...) {
			_, err := testDB.Exec("DROP TABLE IF EXISTS " + table)
			require.NoError(t, err)
		}
	}
	drop()
	t.Cleanup(drop)
	for _, table := range tables {
		_, err := testDB.Exec("CREATE TABLE " + table + " (id INT PRIMARY KEY)")
		require.NoError(t, err)
	}
}

// holdTable keeps a transaction open over table, the way a long report or a stuck request does.
func holdTable(t *testing.T, table string) (release func()) {
	t.Helper()
	tx, err := testDB.Begin()
	require.NoError(t, err)
	rows, err := tx.Query("SELECT id FROM " + table)
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	released := false
	release = func() {
		if !released {
			released = true
			require.NoError(t, tx.Rollback())
		}
	}
	t.Cleanup(release)
	return release
}

func columnExists(t *testing.T, table, column string) bool {
	t.Helper()
	var n int
	require.NoError(t, testDB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&n))
	return n == 1
}

func appliedLockTestMigrations(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, testDB.QueryRow(`SELECT COUNT(*) FROM lock_test_migrations`).Scan(&n))
	return n
}

// runningStatements counts server threads still executing a statement that contains pattern,
// leaving out this very query.
func runningStatements(t *testing.T, pattern string) int {
	t.Helper()
	var n int
	require.NoError(t, testDB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE INFO LIKE ? AND INFO NOT LIKE '%PROCESSLIST%'`,
		"%"+pattern+"%").Scan(&n))
	return n
}

func TestMigrationFailsFastOnHeldTableAndRetrySucceeds(t *testing.T) {
	setupLockTestTables(t, "lock_test_a")
	source := &migrate.MemoryMigrationSource{Migrations: []*migrate.Migration{
		{Id: "1_add.sql", Up: guardedAddColumn("lock_test_a", "extra")},
	}}
	release := holdTable(t, "lock_test_a")

	started := time.Now()
	_, err := runMigrations(context.Background(), testCfg.DSN, lockTestSet, source, lockTestLockWaitSeconds)
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1_add.sql could not get a table lock in 2s")
	assert.Less(t, elapsed, 15*time.Second, "the wait must end near the bound")
	assert.False(t, columnExists(t, "lock_test_a", "extra"))
	assert.Zero(t, runningStatements(t, "ALTER TABLE lock_test_a"), "nothing may stay queued on the server")

	// New reads of the table are free again even though the long transaction is still open.
	var count int
	require.NoError(t, testDB.QueryRow("SELECT COUNT(*) FROM lock_test_a").Scan(&count))

	release()
	n, err := runMigrations(context.Background(), testCfg.DSN, lockTestSet, source, lockTestLockWaitSeconds)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.True(t, columnExists(t, "lock_test_a", "extra"))
	assert.Equal(t, 1, appliedLockTestMigrations(t))
}

// DDL auto-commits, so a file that dies on its second ALTER keeps the first one and has no
// gorp_migrations row: the next start re-runs the file from the top.
func TestMigrationRetryResumesFileWithSeveralDDL(t *testing.T) {
	setupLockTestTables(t, "lock_test_a", "lock_test_b")
	up := append(guardedAddColumn("lock_test_a", "extra"), guardedAddColumn("lock_test_b", "extra")...)
	source := &migrate.MemoryMigrationSource{Migrations: []*migrate.Migration{
		{Id: "1_two_tables.sql", Up: up},
	}}
	release := holdTable(t, "lock_test_b")

	_, err := runMigrations(context.Background(), testCfg.DSN, lockTestSet, source, lockTestLockWaitSeconds)
	require.Error(t, err)
	assert.True(t, columnExists(t, "lock_test_a", "extra"), "the first ALTER has already auto-committed")
	assert.False(t, columnExists(t, "lock_test_b", "extra"))
	assert.Zero(t, appliedLockTestMigrations(t), "a half-applied file must not be recorded")

	release()
	n, err := runMigrations(context.Background(), testCfg.DSN, lockTestSet, source, lockTestLockWaitSeconds)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.True(t, columnExists(t, "lock_test_b", "extra"))
	assert.Equal(t, 1, appliedLockTestMigrations(t))
}

func TestMigrationTimeoutKillsTheRunningStatement(t *testing.T) {
	setupLockTestTables(t, "lock_test_after_kill")
	source := &migrate.MemoryMigrationSource{Migrations: []*migrate.Migration{
		{Id: "1_slow.sql", DisableTransactionUp: true, Up: []string{
			`DO SLEEP(120) /* lock_test_slow */`,
			`ALTER TABLE lock_test_after_kill ADD COLUMN extra INT NULL`,
		}},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	started := time.Now()
	_, err := runMigrations(ctx, testCfg.DSN, lockTestSet, source, lockTestLockWaitSeconds)
	elapsed := time.Since(started)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 15*time.Second, "the caller must not wait for the statement")
	assert.Zero(t, runningStatements(t, "lock_test_slow"), "the statement must be gone from the server")
	assert.False(t, columnExists(t, "lock_test_after_kill", "extra"), "nothing may run after the timeout")
}

func TestMigrationKillFailureIsReported(t *testing.T) {
	cfg, err := mysql.ParseDSN(testCfg.DSN)
	require.NoError(t, err)
	cfg.Addr = "127.0.0.1:1" // nothing listens here
	inner, err := mysql.NewConnector(cfg)
	require.NoError(t, err)
	connector := &migrationConnector{ctx: context.Background(), inner: inner, ids: []int64{1}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = connector.killAll(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can't kill 1 migration connection")
}

func TestMigrationConnectorRefusesConnectionsAfterCancel(t *testing.T) {
	cfg, err := mysql.ParseDSN(testCfg.DSN)
	require.NoError(t, err)
	inner, err := mysql.NewConnector(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	connector := &migrationConnector{ctx: ctx, inner: inner}

	conn, err := connector.Connect(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	assert.Len(t, connector.ids, 1)

	cancel()
	_, err = connector.Connect(context.Background())
	require.ErrorIs(t, err, context.Canceled)
}

func TestMigrationLockWaitStaysOffApplicationConnections(t *testing.T) {
	setupLockTestTables(t, "lock_test_a")
	source := &migrate.MemoryMigrationSource{Migrations: []*migrate.Migration{
		{Id: "1_add.sql", Up: guardedAddColumn("lock_test_a", "extra")},
	}}
	_, err := runMigrations(context.Background(), testCfg.DSN, lockTestSet, source, lockTestLockWaitSeconds)
	require.NoError(t, err)

	var global, session int
	require.NoError(t, testDB.QueryRow("SELECT @@GLOBAL.lock_wait_timeout, @@SESSION.lock_wait_timeout").Scan(&global, &session))
	assert.Equal(t, global, session)
	assert.NotEqual(t, lockTestLockWaitSeconds, session)
}

func TestMigrateDSNWithoutContentionKeepsAppliedMigrations(t *testing.T) {
	var before int
	require.NoError(t, testDB.QueryRow("SELECT COUNT(*) FROM gorp_migrations").Scan(&before))
	require.NotZero(t, before)

	require.NoError(t, MigrateDSN(context.Background(), testCfg.DSN))

	var after int
	require.NoError(t, testDB.QueryRow("SELECT COUNT(*) FROM gorp_migrations").Scan(&after))
	assert.Equal(t, before, after)
}
