package platformstore

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
)

// featureKey is a feature id as F1 names them: lowercase words joined by '_' or '.'.
var featureKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_.]{0,39}$`)

// FeatureOverrides returns the team's explicit feature switches (team_feature). A feature absent
// from the map follows the team's profile.
func (s *Store) FeatureOverrides(ctx context.Context, teamID int64) (map[string]bool, error) {
	var rows []struct {
		Feature string `db:"feature"`
		Enabled bool   `db:"enabled"`
	}
	if err := s.db.SelectContext(ctx, &rows,
		`SELECT feature, enabled FROM team_feature WHERE team_id = ?`, teamID); err != nil {
		return nil, fmt.Errorf("platformstore: feature overrides: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.Feature] = r.Enabled
	}
	return out, nil
}

// SetFeature records an explicit switch of one feature for one team. byUserID <= 0 = the system.
func (s *Store) SetFeature(ctx context.Context, teamID int64, key string, enabled bool, byUserID int64) error {
	if teamID <= 0 {
		return fmt.Errorf("platformstore: set feature: bad team %d", teamID)
	}
	if !featureKey.MatchString(key) {
		return fmt.Errorf("platformstore: set feature: bad key %q", key)
	}
	by := sql.NullInt64{Int64: byUserID, Valid: byUserID > 0}
	_, err := s.db.ExecContext(ctx, `INSERT INTO team_feature (team_id, feature, enabled, updated_by, updated_at)
VALUES (?, ?, ?, ?, UTC_TIMESTAMP(3)) AS nv
ON DUPLICATE KEY UPDATE enabled = nv.enabled, updated_by = nv.updated_by, updated_at = nv.updated_at`,
		teamID, key, enabled, by)
	if err != nil {
		return fmt.Errorf("platformstore: set feature: %w", err)
	}
	s.dir.Invalidate(teamID)
	return nil
}
