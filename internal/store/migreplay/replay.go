// Package migreplay applies the base branch's migrations, seeds hostile legacy
// rows, and then applies only migrations newly introduced by the working tree.
package migreplay

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	migrate "github.com/rubenv/sql-migrate"
	"github.com/rubenv/sql-migrate/sqlparse"
)

const migrationsPath = "internal/store/sql"

type Config struct {
	DSN       string
	BaseRef   string
	RepoDir   string
	Progress  io.Writer
	FullChain bool
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.Progress == nil {
		cfg.Progress = io.Discard
	}
	if cfg.BaseRef == "" {
		cfg.BaseRef = "origin/beta"
	}
	if cfg.RepoDir == "" {
		cfg.RepoDir = "."
	}
	if err := testdbguard.CheckMySQLDSN(cfg.DSN); err != nil {
		return fmt.Errorf("refusing migration replay DSN: %w", err)
	}
	dsnConfig, err := mysql.ParseDSN(cfg.DSN)
	if err != nil {
		return fmt.Errorf("parse replay DSN: %w", err)
	}
	if dsnConfig.MultiStatements {
		return errors.New("refusing replay DSN with multiStatements=true: beta and production execute each sql-migrate statement separately")
	}
	// sql-migrate scans applied_at into time.Time. This is also present in the
	// application's deployed DSN and is required by the library's MySQL dialect.
	dsnConfig.ParseTime = true
	if cfg.FullChain {
		return runFullChain(ctx, cfg, dsnConfig)
	}

	repoRoot, err := gitOutput(ctx, cfg.RepoDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	baseCommit, err := gitOutput(ctx, repoRoot, "merge-base", "HEAD", cfg.BaseRef)
	if err != nil {
		return fmt.Errorf("resolve merge base with %q: %w", cfg.BaseRef, err)
	}
	fmt.Fprintf(cfg.Progress, "migration replay: base %s (%s)\n", shortCommit(baseCommit), cfg.BaseRef)

	baseSource, baseFiles, err := migrationsAtCommit(ctx, repoRoot, baseCommit)
	if err != nil {
		return err
	}
	currentSource := &migrate.FileMigrationSource{Dir: filepath.Join(repoRoot, migrationsPath)}
	currentMigrations, err := currentSource.FindMigrations()
	if err != nil {
		return fmt.Errorf("read working-tree migrations: %w", err)
	}
	currentFiles := make(map[string][]byte, len(currentMigrations))
	for _, migration := range currentMigrations {
		body, err := os.ReadFile(filepath.Join(repoRoot, migrationsPath, migration.Id))
		if err != nil {
			return fmt.Errorf("read working-tree migration %s: %w", migration.Id, err)
		}
		currentFiles[migration.Id] = body
	}
	if err := checkBaseMigrationImmutability(baseFiles, currentFiles); err != nil {
		return err
	}

	newFiles := make(map[string]bool)
	for _, migration := range currentMigrations {
		if _, existed := baseFiles[migration.Id]; !existed {
			newFiles[migration.Id] = true
		}
	}
	fmt.Fprintf(cfg.Progress, "migration replay: %d base migration(s), %d new migration(s)\n", len(baseFiles), len(newFiles))

	db, err := sql.Open("mysql", dsnConfig.FormatDSN())
	if err != nil {
		return fmt.Errorf("open replay database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping replay database: %w", err)
	}
	if err := requireEmptyDatabase(ctx, db, dsnConfig.DBName); err != nil {
		return err
	}

	var sqlMode string
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&sqlMode); err != nil {
		return fmt.Errorf("read migration sql_mode: %w", err)
	}
	fmt.Fprintf(cfg.Progress, "migration replay: applying base with sql_mode=%q and multiStatements=false\n", sqlMode)
	if _, err := migrate.Exec(db, "mysql", baseSource, migrate.Up); err != nil {
		return formatMigrationError("base migration", err)
	}

	stats, err := SeedBoundaryData(ctx, db, dsnConfig.DBName, cfg.Progress)
	if err != nil {
		return fmt.Errorf("seed boundary data: %w", err)
	}
	fmt.Fprintf(cfg.Progress, "migration replay: covered %d/%d table(s) with %d synthetic boundary row(s); %d table(s) were already covered by migration data and %d candidate insert(s) were rejected by the base schema\n",
		stats.TablesCovered, stats.TablesVisited, stats.RowsInserted, stats.TablesAlreadyCovered, stats.CandidatesRejected)
	if len(stats.UnseededTables) > 0 {
		fmt.Fprintf(cfg.Progress, "migration replay: warning: no synthetic boundary row fit %d table(s): %s\n",
			len(stats.UnseededTables), strings.Join(stats.UnseededTables, ", "))
		for _, table := range stats.UnseededTables {
			fmt.Fprintf(cfg.Progress, "migration replay: unseeded %s: %s\n", table, stats.UnseededReasons[table])
		}
	}

	appliedNew := 0
	for {
		plan, _, err := migrate.PlanMigration(db, "mysql", currentSource, migrate.Up, 1)
		if err != nil {
			return fmt.Errorf("plan candidate migrations: %w", err)
		}
		if len(plan) == 0 {
			break
		}
		name := plan[0].Id
		if !newFiles[name] {
			return fmt.Errorf("migration plan unexpectedly includes non-new migration %s", name)
		}
		fmt.Fprintf(cfg.Progress, "migration replay: applying new migration %s\n", name)
		if _, err := migrate.ExecMax(db, "mysql", currentSource, migrate.Up, 1); err != nil {
			return formatMigrationError("new migration", err)
		}
		appliedNew++
	}
	if appliedNew != len(newFiles) {
		return fmt.Errorf("applied %d new migrations, expected %d", appliedNew, len(newFiles))
	}
	fmt.Fprintf(cfg.Progress, "migration replay: PASS (%d new migration(s) applied)\n", appliedNew)
	return nil
}

func runFullChain(ctx context.Context, cfg Config, dsnConfig *mysql.Config) error {
	repoRoot, err := gitOutput(ctx, cfg.RepoDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	source := &migrate.FileMigrationSource{Dir: filepath.Join(repoRoot, migrationsPath)}
	migrations, err := source.FindMigrations()
	if err != nil {
		return fmt.Errorf("read working-tree migrations: %w", err)
	}

	db, err := sql.Open("mysql", dsnConfig.FormatDSN())
	if err != nil {
		return fmt.Errorf("open full-chain database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping full-chain database: %w", err)
	}
	if err := requireEmptyDatabase(ctx, db, dsnConfig.DBName); err != nil {
		return err
	}

	var serverCharset, sqlMode string
	if err := db.QueryRowContext(ctx, "SELECT @@character_set_server, @@SESSION.sql_mode").Scan(&serverCharset, &sqlMode); err != nil {
		return fmt.Errorf("read full-chain MySQL settings: %w", err)
	}
	fmt.Fprintf(cfg.Progress, "migration full-chain: applying %d migration(s) with server charset=%s, sql_mode=%q, and multiStatements=false\n",
		len(migrations), serverCharset, sqlMode)
	applied, err := migrate.Exec(db, "mysql", source, migrate.Up)
	if err != nil {
		return formatMigrationError("full-chain migration", err)
	}
	if applied != len(migrations) {
		return fmt.Errorf("full-chain applied %d migrations, expected %d", applied, len(migrations))
	}
	fmt.Fprintf(cfg.Progress, "migration full-chain: PASS (%d migration(s) applied)\n", applied)
	return nil
}

func checkBaseMigrationImmutability(baseFiles, currentFiles map[string][]byte) error {
	names := make([]string, 0, len(baseFiles))
	for name := range baseFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		baseBody := baseFiles[name]
		currentBody, ok := currentFiles[name]
		if !ok {
			return fmt.Errorf("base migration %s is missing from the working tree; applied migrations may not be removed or renamed", name)
		}
		if !bytes.Equal(baseBody, currentBody) {
			return fmt.Errorf("base migration %s was modified; add a new migration instead of changing an applied file", name)
		}
	}
	return nil
}

func requireEmptyDatabase(ctx context.Context, db *sql.DB, schema string) error {
	var tables int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'`, schema).Scan(&tables)
	if err != nil {
		return fmt.Errorf("inspect replay database: %w", err)
	}
	if tables != 0 {
		return fmt.Errorf("refusing to replay into non-empty database %q (%d table(s)); use a fresh throwaway *_test database", schema, tables)
	}
	return nil
}

func migrationsAtCommit(ctx context.Context, repoRoot, commit string) (*migrate.MemoryMigrationSource, map[string][]byte, error) {
	listing, err := gitOutput(ctx, repoRoot, "ls-tree", "-r", "--name-only", commit, "--", migrationsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("list base migrations: %w", err)
	}
	var paths []string
	for _, path := range strings.Split(listing, "\n") {
		if strings.HasSuffix(path, ".sql") && filepath.Dir(path) == migrationsPath {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	files := make(map[string][]byte, len(paths))
	migrations := make([]*migrate.Migration, 0, len(paths))
	for _, path := range paths {
		body, err := gitBytes(ctx, repoRoot, "show", commit+":"+path)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s at %s: %w", path, shortCommit(commit), err)
		}
		name := filepath.Base(path)
		parsed, err := sqlparse.ParseMigration(bytes.NewReader(body))
		if err != nil {
			return nil, nil, fmt.Errorf("parse base migration %s: %w", name, err)
		}
		files[name] = body
		migrations = append(migrations, &migrate.Migration{
			Id:                     name,
			Up:                     parsed.UpStatements,
			Down:                   parsed.DownStatements,
			DisableTransactionUp:   parsed.DisableTransactionUp,
			DisableTransactionDown: parsed.DisableTransactionDown,
		})
	}
	if len(migrations) == 0 {
		return nil, nil, fmt.Errorf("no base migrations found at %s", shortCommit(commit))
	}
	return &migrate.MemoryMigrationSource{Migrations: migrations}, files, nil
}

func gitOutput(ctx context.Context, repoDir string, args ...string) (string, error) {
	body, err := gitBytes(ctx, repoDir, args...)
	return strings.TrimSpace(string(body)), err
}

func gitBytes(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoDir}, args...)...)
	body, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func formatMigrationError(stage string, err error) error {
	name := "unknown"
	cause := err
	var txErr *migrate.TxError
	if errors.As(err, &txErr) && txErr.Migration != nil {
		name = txErr.Migration.Id
		cause = txErr.Err // TxError in sql-migrate v1.4.0 does not implement Unwrap.
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(cause, &mysqlErr) {
		return fmt.Errorf("%s %s failed: MySQL %d (%s): %w", stage, name, mysqlErr.Number, mysqlErr.Message, err)
	}
	return fmt.Errorf("%s %s failed: %w", stage, name, err)
}
