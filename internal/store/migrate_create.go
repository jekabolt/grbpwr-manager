package store

import (
	"context"
	"database/sql"
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
