package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

// teamDBName is what a team database may be called: a plain identifier, so it can never smuggle a
// second statement or a schema qualifier into a DSN or a USE.
var teamDBName = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// DSNDatabase returns the database a MySQL DSN names, by the driver's own rule: after the last '/',
// up to the first '?', taken as written (driver v1.6 does not unescape it). It does not call
// mysql.ParseDSN, which also resolves tls=<name> and refuses a name not registered yet: "custom" is
// registered only inside store.New, and config is loaded before that. "" for an empty DSN.
func DSNDatabase(dsn string) (string, error) {
	if dsn == "" {
		return "", nil
	}
	i := strings.LastIndexByte(dsn, '/')
	if i < 0 {
		return "", errors.New("invalid DSN: missing the slash separating the database name")
	}
	name := dsn[i+1:]
	if j := strings.IndexByte(name, '?'); j >= 0 {
		name = name[:j]
	}
	return name, nil
}

// TeamDSN returns baseDSN pointed at dbName: same host, user, TLS and params, another database.
func TeamDSN(baseDSN, dbName string) (string, error) {
	if !teamDBName.MatchString(dbName) {
		return "", fmt.Errorf("team database name %q is not [a-z0-9_]{1,64}", dbName)
	}
	cfg, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		return "", fmt.Errorf("parse base dsn: %w", err)
	}
	cfg.DBName = dbName
	return cfg.FormatDSN(), nil
}

// MigrateTeam applies the embedded team migrations to database dbName on a pool of its own (the
// same bounded lock wait and kill-on-cancel as a boot migration) and returns the id of the latest
// migration, which is then the team's schema_version.
func MigrateTeam(ctx context.Context, baseDSN, dbName string) (version string, err error) {
	dsn, err := TeamDSN(baseDSN, dbName)
	if err != nil {
		return "", err
	}
	n, err := runMigrations(ctx, dsn, migrate.MigrationSet{}, teamMigrationSource(), migrationLockWaitSeconds)
	if err != nil {
		return "", fmt.Errorf("migrate team database %s: %w", dbName, err)
	}
	slog.Default().InfoContext(ctx, "applied team migrations",
		slog.String("db", dbName),
		slog.Int("count", n),
	)
	return LatestMigrationID(), nil
}

// LatestMigrationID is the id (file name) of the newest embedded team migration, in sql-migrate's
// own order (numeric prefix, then name). "" only if the embedded set cannot be read, which a
// build with the embed in place never hits.
func LatestMigrationID() string {
	ms, err := teamMigrationSource().FindMigrations()
	if err != nil || len(ms) == 0 {
		return ""
	}
	return ms[len(ms)-1].Id
}

func teamMigrationSource() migrate.MigrationSource {
	return &migrate.EmbedFileSystemMigrationSource{FileSystem: fs, Root: "sql"}
}
