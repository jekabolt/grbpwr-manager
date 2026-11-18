package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

// createTimeout bounds CREATE DATABASE: it is a metadata operation and takes milliseconds.
const createTimeout = 30 * time.Second

// identRe is what a TENANT_DB_PREFIX may be: a plain identifier that leaves room for 8 more characters.
var prefixRe = regexp.MustCompile(`^[a-z0-9_]{1,56}$`)

// NewTeamDBName reports whether name is a database the provisioner may create: exactly
// prefix + 8 [a-z0-9] (02-DESIGN §1) and not team #1's database. This is the allowlist every
// database name passes before it reaches CREATE DATABASE, a DSN or a USE.
func NewTeamDBName(prefix, team1DB, name string) error {
	if !prefixRe.MatchString(prefix) {
		return fmt.Errorf("team database prefix %q is not [a-z0-9_]{1,56}", prefix)
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `[a-z0-9]{8}$`)
	if !re.MatchString(name) {
		return fmt.Errorf("team database name %q is not %s + 8 [a-z0-9]", name, prefix)
	}
	if name == team1DB {
		return fmt.Errorf("team database name %q is team #1's database", name)
	}
	return nil
}

// CreateTeamDB creates the empty database dbName (utf8mb4 / utf8mb4_0900_ai_ci) on the server of
// baseDSN, on a short-lived handle of its own. The name must pass NewTeamDBName(prefix, <baseDSN
// database>, dbName). It is CREATE DATABASE without IF NOT EXISTS: an existing database is an error,
// never adopted, so a name collision can never hand a new team someone else's data.
func CreateTeamDB(ctx context.Context, baseDSN, prefix, dbName string) error {
	cfg, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		return fmt.Errorf("parse base dsn: %w", err)
	}
	if err := NewTeamDBName(prefix, cfg.DBName, dbName); err != nil {
		return err
	}
	// No default database on this handle: nothing runs in team #1's database.
	cfg.DBName = ""
	cfg.MultiStatements = false
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return fmt.Errorf("create team database: connector: %w", err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	// dbName matched prefix + [a-z0-9]{8} above: no backtick, quote or dot can reach the statement.
	q := "CREATE DATABASE `" + dbName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"
	if _, err := db.ExecContext(ctx, q); err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1007 {
			return fmt.Errorf("create team database %s: it already exists: %w", dbName, err)
		}
		return fmt.Errorf("create team database %s: %w", dbName, err)
	}
	return nil
}

// ErrTeamLockLost: the named lock on a team database is no longer this holder's (its connection
// died or was killed). Nothing more may run against that database under it.
var ErrTeamLockLost = errors.New("store: team database lock lost")

// TeamDBLock is the fence around every write the provisioner makes to a team database: the MySQL
// named lock TeamLockName(db), held by one session to that database for as long as the holder
// works. Scrub, seed, the owner's admin row and migration all run on that same session — the lock's
// pool has exactly one physical connection and its connector refuses to dial a second — so the lock
// and the statements it fences live and die together: once the session is gone the lock is free
// and every later statement fails instead of running unfenced on a new connection, and a statement
// still running on the server keeps the session (and so the lock) until it ends or is killed. A
// second holder cannot take the lock until then. Control-plane leases only schedule; this is the fence.
type TeamDBLock struct {
	dbName, name string
	db           *sql.DB
	conn         *oneConnector
}

// oneConnector dials one connection for the life of a TeamDBLock and refuses every later Connect,
// so database/sql can never replace a dead lock session with one that does not hold the lock. It
// remembers the session id, so a cancelled migration can be killed on the server.
type oneConnector struct {
	inner driver.Connector

	mu      sync.Mutex
	dialled bool
	id      int64
}

func (c *oneConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dialled {
		return nil, ErrTeamLockLost
	}
	c.dialled = true
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	id, err := driverConnectionID(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read lock connection id: %w", err)
	}
	c.id = id
	return conn, nil
}

func (c *oneConnector) Driver() driver.Driver { return c.inner.Driver() }

func (c *oneConnector) sessionID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

// TeamLockName is the server-wide lock name of a team database (MySQL caps lock names at 64).
func TeamLockName(dbName string) string {
	if n := "provision:" + dbName; len(n) <= 64 {
		return n
	}
	sum := sha256.Sum256([]byte(dbName))
	return "provision:" + hex.EncodeToString(sum[:])[:54]
}

// LockTeamDB takes the named lock of team database dbName without waiting. ok is false (no lock,
// nothing held) when another session holds it. The name must pass NewTeamDBName(prefix,
// <baseDSN database>, dbName); team #1's database is refused.
func LockTeamDB(ctx context.Context, baseDSN, prefix, dbName string) (lock *TeamDBLock, ok bool, err error) {
	cfg, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		return nil, false, fmt.Errorf("parse base dsn: %w", err)
	}
	if err := NewTeamDBName(prefix, cfg.DBName, dbName); err != nil {
		return nil, false, err
	}
	cfg.DBName = dbName
	cfg.MultiStatements = false
	if cfg.Params == nil {
		cfg.Params = make(map[string]string, 1)
	}
	// The same bounded metadata-lock wait as a boot migration: the migration runs on this session.
	cfg.Params["lock_wait_timeout"] = strconv.Itoa(migrationLockWaitSeconds)
	inner, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("lock team database: connector: %w", err)
	}
	one := &oneConnector{inner: inner}
	db := sql.OpenDB(one)
	// One connection, kept idle between statements and never retired: closing it would free the lock.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	name := TeamLockName(dbName)
	var got sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT GET_LOCK(?, 0)`, name).Scan(&got); err != nil {
		_ = db.Close()
		return nil, false, fmt.Errorf("lock team database %s: %w", dbName, err)
	}
	if !got.Valid || got.Int64 != 1 {
		_ = db.Close()
		return nil, false, nil
	}
	return &TeamDBLock{dbName: dbName, name: name, db: db, conn: one}, true, nil
}

// ExecContext runs one statement in the team database on the lock's session.
func (l *TeamDBLock) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return l.db.ExecContext(ctx, query, args...)
}

// Alive reports ErrTeamLockLost unless the lock is still held by this lock's session.
func (l *TeamDBLock) Alive(ctx context.Context) error {
	var mine sql.NullInt64
	if err := l.db.QueryRowContext(ctx, `SELECT IS_USED_LOCK(?) = CONNECTION_ID()`, l.name).Scan(&mine); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTeamLockLost, l.dbName, err)
	}
	if !mine.Valid || mine.Int64 != 1 {
		return fmt.Errorf("%w: %s", ErrTeamLockLost, l.dbName)
	}
	return nil
}

// Migrate applies the embedded team migrations on the lock's own session and returns the latest
// migration id (the team's schema_version). The DDL and the lock share one session, so the lock
// cannot be lost while a migration statement still runs, and a session killed mid-run ends the
// run with an error (nothing re-dials). A cancelled ctx kills the session on the server, which
// also frees the lock. A run that failed without the lock reports ErrTeamLockLost.
func (l *TeamDBLock) Migrate(ctx context.Context) (version string, err error) {
	if err := l.Alive(ctx); err != nil {
		return "", err
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := migrate.MigrationSet{}.Exec(l.db, "mysql", teamMigrationSource(), migrate.Up)
		done <- result{n: n, err: err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			err := describeMigrationError(res.err, migrationLockWaitSeconds)
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if lost := l.Alive(actx); lost != nil {
				return "", fmt.Errorf("migrate team database %s: %w: %w", l.dbName, lost, err)
			}
			return "", fmt.Errorf("migrate team database %s: %w", l.dbName, err)
		}
		slog.Default().InfoContext(ctx, "applied team migrations",
			slog.String("db", l.dbName),
			slog.Int("count", res.n),
		)
	case <-ctx.Done():
		stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), migrationStopTimeout)
		defer stopCancel()
		errs := []error{fmt.Errorf("migrate team database %s: %w", l.dbName, ctx.Err())}
		killer := &migrationConnector{ctx: stopCtx, inner: l.conn.inner, ids: []int64{l.conn.sessionID()}}
		if err := killer.killAll(stopCtx); err != nil {
			errs = append(errs, err)
		}
		select {
		case <-done:
		case <-stopCtx.Done():
			errs = append(errs, errors.New("migration statement may still be running on the server"))
		}
		return "", errors.Join(errs...)
	}
	if err := l.Alive(ctx); err != nil {
		return "", err
	}
	return LatestMigrationID(), nil
}

// Release frees the lock (RELEASE_LOCK, even when ctx is already cancelled, so the next holder can
// take it at once) and closes its session; if the statement fails, closing the session frees it.
func (l *TeamDBLock) Release(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = l.db.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, l.name)
	_ = l.db.Close()
}
