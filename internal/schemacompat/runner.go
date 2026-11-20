package schemacompat

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

const mysqlErrNoSuchTable = 1146

// TolerateAhead is the one rule both migration runners (team databases with team #1, and the
// control plane) apply before sql-migrate runs: when every migration recorded in table that this
// binary does not embed sorts after its newest embedded one and none is breaking, the database is
// only AHEAD of the code (a rollback over additive releases) and set.IgnoreUnknown is turned on, so
// the binary starts instead of refusing with «unknown migration in database». A diverged history
// or a newer breaking migration keeps sql-migrate's refusal. A missing table is a fresh database.
// It warns once per call; db is read once (a newer instance applying a file between this read and
// the run brings the refusal back for that one start).
func TolerateAhead(ctx context.Context, db *sql.DB, table string, set *migrate.MigrationSet, src migrate.MigrationSource) error {
	if table == "" {
		table = "gorp_migrations"
	}
	ms, err := src.FindMigrations()
	if err != nil {
		return err
	}
	known := make([]string, 0, len(ms))
	for _, m := range ms {
		known = append(known, m.Id)
	}
	rows, err := db.QueryContext(ctx, "SELECT id FROM `"+table+"`")
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == mysqlErrNoSuchTable {
			return nil
		}
		return err
	}
	defer rows.Close()
	var applied []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		applied = append(applied, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if unknown, ok := AheadOnly(known, applied); ok {
		set.IgnoreUnknown = true
		slog.Default().WarnContext(ctx, "schema is ahead of this binary; newer migrations left as they are",
			slog.String("table", table), slog.Any("unknown", unknown))
	}
	return nil
}
