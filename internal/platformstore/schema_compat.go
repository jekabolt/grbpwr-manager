package platformstore

import (
	"context"
	"fmt"

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
