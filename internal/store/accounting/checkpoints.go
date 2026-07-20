package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetCheckpoint returns the pull-source cursor for source. A missing row is NOT an error: it returns
// the zero AcctCheckpoint (LastId / LastTs invalid), which the worker treats as the first run
// (last_id = 0 / last_ts = accounting.start_date).
func (s *Store) GetCheckpoint(ctx context.Context, source string) (entity.AcctCheckpoint, error) {
	cp, err := storeutil.QueryNamedOne[entity.AcctCheckpoint](ctx, s.DB, `
		SELECT source, last_id, last_ts, updated_at
		FROM acct_checkpoint WHERE source = :source`,
		map[string]any{"source": source})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.AcctCheckpoint{Source: source}, nil
		}
		return entity.AcctCheckpoint{}, fmt.Errorf("accounting: get checkpoint %s: %w", source, err)
	}
	return cp, nil
}

// SetCheckpoint upserts the pull-source cursor. lastID / lastTS may be NULL (a source that scans by
// only one of id / timestamp leaves the other invalid).
func (s *Store) SetCheckpoint(ctx context.Context, source string, lastID sql.NullInt64, lastTS sql.NullTime) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO acct_checkpoint (source, last_id, last_ts)
		VALUES (:source, :last_id, :last_ts)
		ON DUPLICATE KEY UPDATE last_id = VALUES(last_id), last_ts = VALUES(last_ts)`,
		map[string]any{"source": source, "last_id": lastID, "last_ts": lastTS}); err != nil {
		return fmt.Errorf("accounting: set checkpoint %s: %w", source, err)
	}
	return nil
}
