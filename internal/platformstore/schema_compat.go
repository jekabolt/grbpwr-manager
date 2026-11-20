package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"

	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
)

// RaiseTeamMinCode records that team teamID's database has (or is about to get) the breaking team
// migration minCode (X3): only a binary that embeds it may serve the team. It only ever raises the
// value, in one statement (numeric prefix first, then the full id, as sql-migrate orders them), so
// an older worker racing a newer one can never lower it; no lease is needed. "" is a no-op.
func (s *Store) RaiseTeamMinCode(ctx context.Context, teamID int64, minCode string) error {
	if minCode == "" {
		return nil
	}
	n := schemacompat.Number(minCode)
	if _, err := s.db.ExecContext(ctx, `UPDATE team SET schema_min_code = ?
WHERE id = ? AND (schema_min_code IS NULL
  OR CAST(SUBSTRING_INDEX(schema_min_code, '_', 1) AS UNSIGNED) < ?
  OR (CAST(SUBSTRING_INDEX(schema_min_code, '_', 1) AS UNSIGNED) = ? AND schema_min_code < ?))`,
		minCode, teamID, n, n, minCode); err != nil {
		return fmt.Errorf("platformstore: team %d min code: %w", teamID, err)
	}
	s.dir.Invalidate(teamID)
	return nil
}

// tolerateAhead sets set.IgnoreUnknown when the control plane is only AHEAD of this binary
// (X3): every recorded migration this binary does not embed is newer than its newest one and none
// is breaking — a rollback after an additive control-plane migration. Anything else (a diverged
// history, a breaking one) keeps sql-migrate's refusal. Control-plane migrations are additive only.
func tolerateAhead(ctx context.Context, db *sql.DB, set *migrate.MigrationSet, src migrate.MigrationSource) error {
	ms, err := src.FindMigrations()
	if err != nil {
		return err
	}
	known := make([]string, 0, len(ms))
	for _, m := range ms {
		known = append(known, m.Id)
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM `+migrationsTable)
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1146 { // no table yet: a fresh control plane
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
	if unknown, ok := schemacompat.AheadOnly(known, applied); ok {
		set.IgnoreUnknown = true
		slog.Default().WarnContext(ctx, "platformstore: control plane is ahead of this binary; newer migrations left as they are",
			slog.Any("unknown", unknown))
	}
	return nil
}
