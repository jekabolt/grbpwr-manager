package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"
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

// teamLockWatch is how often a held team-database lock is re-checked while a migration runs under it.
const teamLockWatch = 2 * time.Second

// ErrTeamLockLost: the named lock on a team database is no longer this holder's (its connection
// died or was killed). Nothing more may run against that database under it.
var ErrTeamLockLost = errors.New("store: team database lock lost")

// TeamDBLock is the fence around every write the provisioner makes to a team database: the MySQL
// named lock TeamLockName(db), held by one dedicated connection to that database for as long as
// the holder works. Scrub, seed and migration run only while it is held; a second holder cannot
// take it until the first releases it or its connection dies (a crashed worker frees it). The
// statements of ExecContext run on the lock's own connection, so once that connection is gone they
// fail instead of running unfenced. Control-plane leases only schedule; this lock is the fence.
type TeamDBLock struct {
	baseDSN, dbName, name string
	db                    *sql.DB
	conn                  *sql.Conn
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
// nothing held) when another connection holds it. The name must pass NewTeamDBName(prefix,
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
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, false, fmt.Errorf("lock team database: connector: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, false, fmt.Errorf("lock team database %s: connect: %w", dbName, err)
	}
	name := TeamLockName(dbName)
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 0)`, name).Scan(&got); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, false, fmt.Errorf("lock team database %s: %w", dbName, err)
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		_ = db.Close()
		return nil, false, nil
	}
	return &TeamDBLock{baseDSN: baseDSN, dbName: dbName, name: name, db: db, conn: conn}, true, nil
}

// ExecContext runs one statement in the team database on the lock's connection.
func (l *TeamDBLock) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return l.conn.ExecContext(ctx, query, args...)
}

// Alive reports ErrTeamLockLost unless the lock is still held by this lock's connection.
func (l *TeamDBLock) Alive(ctx context.Context) error {
	var mine sql.NullInt64
	if err := l.conn.QueryRowContext(ctx, `SELECT IS_USED_LOCK(?) = CONNECTION_ID()`, l.name).Scan(&mine); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTeamLockLost, l.dbName, err)
	}
	if !mine.Valid || mine.Int64 != 1 {
		return fmt.Errorf("%w: %s", ErrTeamLockLost, l.dbName)
	}
	return nil
}

// Migrate runs MigrateTeam while the lock is held. The lock is checked before the run, every
// teamLockWatch during it — a lost lock cancels the run, and MigrateTeam then kills its statement on
// the server — and after it; a run that ended without the lock reports ErrTeamLockLost.
func (l *TeamDBLock) Migrate(ctx context.Context) (version string, err error) {
	if err := l.Alive(ctx); err != nil {
		return "", err
	}
	mctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		t := time.NewTicker(teamLockWatch)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-mctx.Done():
				return
			case <-t.C:
			}
			if err := l.Alive(mctx); err != nil {
				if mctx.Err() == nil {
					cancel(err)
				}
				return
			}
		}
	}()
	version, err = MigrateTeam(mctx, l.baseDSN, l.dbName)
	close(stop)
	<-watched
	if cause := context.Cause(mctx); errors.Is(cause, ErrTeamLockLost) {
		if err == nil {
			err = errors.New("migration finished after the lock was lost")
		}
		return "", fmt.Errorf("%w: %w", cause, err)
	}
	if err != nil {
		return "", err
	}
	if err := l.Alive(ctx); err != nil {
		return "", err
	}
	return version, nil
}

// Release frees the lock (RELEASE_LOCK, even when ctx is already cancelled, so the next holder can
// take it at once) and closes its connection; if the statement fails, closing the session frees it.
func (l *TeamDBLock) Release(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = l.conn.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, l.name)
	_ = l.conn.Close()
	_ = l.db.Close()
}
