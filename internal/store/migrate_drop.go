package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// DROP OF A WIPED TEAM'S DATABASE (teams D3, lanes/D3-design.md §2.3). The only DROP DATABASE in the
// codebase. The name passes the provisioner's allow-list (prefix + 8 [a-z0-9], never team #1's
// database) and a second one here (never the control plane, never a system schema); the caller has
// checked that no team or spare row still names it. The drop runs on the named-lock session the
// provisioner and the fleet migrator hold (LockTeamDB), so it can never run under a migration.

// dropTimeout bounds one drop: a metadata operation, but it waits for open metadata locks.
const dropTimeout = 2 * time.Minute

// systemSchema: never dropped, whatever the prefix.
func systemSchema(name string) bool {
	switch strings.ToLower(name) {
	case "mysql", "sys", "information_schema", "performance_schema", "defaultdb":
		return true
	}
	return false
}

// DropResult is what DropTeamDB did.
type DropResult int

const (
	// DropDone: the database was dropped now.
	DropDone DropResult = iota
	// DropGone: the database did not exist (dropped before; a resume after a lost db_dropped_at).
	DropGone
	// DropBusy: another session holds the database's named lock (a migration or seed); try later.
	DropBusy
)

func (r DropResult) String() string {
	switch r {
	case DropDone:
		return "dropped"
	case DropGone:
		return "gone"
	case DropBusy:
		return "busy"
	}
	return "unknown"
}

// CheckDropName is the allow-list a database name passes before DROP: NewTeamDBName(prefix, <the
// base DSN's database>, name), not platformDB (the control plane), not a system schema.
func CheckDropName(prefix, team1DB, platformDB, name string) error {
	if err := NewTeamDBName(prefix, team1DB, name); err != nil {
		return err
	}
	if strings.TrimSpace(team1DB) == "" {
		return errors.New("drop team database: team #1's database is unknown")
	}
	if strings.TrimSpace(platformDB) == "" {
		return errors.New("drop team database: the control plane's database is unknown")
	}
	if strings.EqualFold(name, platformDB) || strings.EqualFold(name, team1DB) {
		return fmt.Errorf("drop team database: %q is a platform database", name)
	}
	if systemSchema(name) {
		return fmt.Errorf("drop team database: %q is a system schema", name)
	}
	return nil
}

// DropTeamDB drops the wiped team database dbName on the server of baseDSN (whose database is team
// #1's). It first looks the name up in information_schema on a handle with no default database
// (missing: DropGone), then takes the named lock without waiting (held elsewhere: DropBusy) and runs
// DROP DATABASE IF EXISTS on the lock's own session; dropping the session's database leaves the lock
// held until Release.
func DropTeamDB(ctx context.Context, baseDSN, prefix, platformDB, dbName string) (DropResult, error) {
	cfg, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		return 0, fmt.Errorf("parse base dsn: %w", err)
	}
	if err := CheckDropName(prefix, cfg.DBName, platformDB, dbName); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, dropTimeout)
	defer cancel()
	exists, err := teamDBExists(ctx, cfg, dbName)
	if err != nil {
		return 0, err
	}
	if !exists {
		return DropGone, nil
	}
	lock, ok, err := LockTeamDB(ctx, baseDSN, prefix, dbName)
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1049 { // unknown database: dropped meanwhile
			return DropGone, nil
		}
		return 0, err
	}
	if !ok {
		return DropBusy, nil
	}
	defer lock.Release(ctx)
	// dbName passed CheckDropName: prefix + [a-z0-9]{8}, no backtick, quote or dot can reach it.
	if _, err := lock.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+dbName+"`"); err != nil {
		return 0, fmt.Errorf("drop team database %s: %w", dbName, err)
	}
	return DropDone, nil
}

// TeamDBSize reports whether dbName exists and the bytes its tables take (information_schema; the
// dry run's plan). The name passes the same allow-list as DropTeamDB.
func TeamDBSize(ctx context.Context, baseDSN, prefix, platformDB, dbName string) (exists bool, bytes int64, err error) {
	cfg, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		return false, 0, fmt.Errorf("parse base dsn: %w", err)
	}
	if err := CheckDropName(prefix, cfg.DBName, platformDB, dbName); err != nil {
		return false, 0, err
	}
	db, err := noDBHandle(cfg)
	if err != nil {
		return false, 0, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, dbName).Scan(&n); err != nil {
		return false, 0, fmt.Errorf("team database %s: %w", dbName, err)
	}
	if n == 0 {
		return false, 0, nil
	}
	var size sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT SUM(DATA_LENGTH + INDEX_LENGTH) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?`,
		dbName).Scan(&size); err != nil {
		return true, 0, fmt.Errorf("team database %s size: %w", dbName, err)
	}
	return true, size.Int64, nil
}

func teamDBExists(ctx context.Context, cfg *mysql.Config, dbName string) (bool, error) {
	db, err := noDBHandle(cfg)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, dbName).Scan(&n); err != nil {
		return false, fmt.Errorf("team database %s: %w", dbName, err)
	}
	return n > 0, nil
}

// noDBHandle is a one-connection handle on the server of cfg with no default database.
func noDBHandle(cfg *mysql.Config) (*sql.DB, error) {
	c := cfg.Clone()
	c.DBName = ""
	c.MultiStatements = false
	connector, err := mysql.NewConnector(c)
	if err != nil {
		return nil, fmt.Errorf("team database handle: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	return db, nil
}
