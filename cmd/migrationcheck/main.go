// Command migrationcheck provides the two isolated CI entry points for SQL
// migrations: a database-free PR safety lint and a direct sql-migrate runner.
// It intentionally does not import internal/store, whose test package has a
// destructive TestMain intended only for dedicated integration databases.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/store/migrationlint/safety"
	migrate "github.com/rubenv/sql-migrate"
)

const usage = `usage:
  migrationcheck lint --base-dir <dir> --head-dir <dir>
  migrationcheck migrate --dsn <dsn> --dir <dir> [--create-database]
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "lint":
		return runLint(args[1:])
	case "migrate":
		return runMigrate(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runLint(args []string) error {
	flags := flag.NewFlagSet("lint", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	baseDir := flags.String("base-dir", "", "migration directory from the PR base revision")
	headDir := flags.String("head-dir", "", "migration directory from the PR head revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *baseDir == "" || *headDir == "" {
		return errors.New("lint requires --base-dir and --head-dir")
	}

	report, err := safety.LintDirs(*baseDir, *headDir)
	if err != nil {
		return fmt.Errorf("lint migrations: %w", err)
	}
	if len(report.Findings) == 0 {
		fmt.Printf("migration safety lint passed (%d new migration file(s))\n", len(report.NewFiles))
		return nil
	}

	fmt.Fprintf(os.Stderr, "migration safety lint found %d history-validating change(s):\n", len(report.Findings))
	for _, finding := range report.Findings {
		fmt.Fprintf(os.Stderr, "- %s: %s on pre-existing table %s\n  %s\n",
			finding.File, finding.Risk, finding.Table, finding.Statement)
	}
	return errors.New("add a non-empty '-- data-checked: <how it was verified against existing data>' comment to each intentional migration")
}

func runMigrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dsn := flags.String("dsn", "", "MySQL DSN")
	dir := flags.String("dir", "", "directory containing sql-migrate files")
	createDatabase := flags.Bool("create-database", false, "create the DSN database using MySQL server defaults")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dsn == "" || *dir == "" {
		return errors.New("migrate requires --dsn and --dir")
	}

	if *createDatabase {
		if err := ensureDatabase(*dsn); err != nil {
			return err
		}
	}

	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping migration database: %w", err)
	}

	source := &migrate.FileMigrationSource{Dir: *dir}
	count, err := migrate.Exec(db, "mysql", source, migrate.Up)
	if err != nil {
		return fmt.Errorf("apply migrations from %s: %w", *dir, err)
	}
	fmt.Printf("applied %d migration(s) from %s\n", count, *dir)
	return nil
}

var safeDatabaseName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func ensureDatabase(dsn string) error {
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse migration DSN: %w", err)
	}
	databaseName := config.DBName
	if !safeDatabaseName.MatchString(databaseName) {
		return fmt.Errorf("database name %q is empty or unsafe; use only letters, numbers, and underscore", databaseName)
	}

	config.DBName = ""
	admin, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return fmt.Errorf("open MySQL server connection: %w", err)
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		return fmt.Errorf("ping MySQL server: %w", err)
	}

	// The name is strictly allowlisted above. Deliberately omit CHARACTER SET
	// and COLLATE: CI must exercise the mysql:8.0 defaults, since forcing
	// utf8mb4_unicode_ci masks/breaks migrations that depend on 8.0 defaults.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+databaseName+"`"); err != nil {
		return fmt.Errorf("create database %s: %w", databaseName, err)
	}
	return nil
}
