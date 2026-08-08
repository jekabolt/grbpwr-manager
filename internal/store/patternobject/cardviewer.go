package patternobject

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Card-viewer access rows (tech_card_pattern_viewer_access, 0288) — the card-level twin
// of the object rows this package already manages. Kept in the same store because the
// mechanics (lazy ensure, epoch revocation, debounced stats) are the same; only the
// identity differs: these rows are keyed by TECH CARD id, and nothing here may be reached
// through an object-scope token (the /api/p handler refuses scope 'c' for exactly that).

const cardViewerColumns = `tech_card_id, epoch, expires_at, revoked_at, last_access_at, access_count, created_at`

// GetCardViewer loads one card's viewer access row. sql.ErrNoRows when absent.
func (s *Store) GetCardViewer(ctx context.Context, techCardID int) (*entity.TechCardPatternViewerAccess, error) {
	row, err := storeutil.QueryNamedOne[entity.TechCardPatternViewerAccess](ctx, s.DB,
		`SELECT `+cardViewerColumns+` FROM tech_card_pattern_viewer_access WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// EnsureCardViewer returns the card's viewer access row, creating it lazily (epoch 1 from
// the column default, no expiry) without touching existing state. SELECT-first for the
// same reason EnsureByKeys is: this runs on every GetTechCard, and the steady state must
// not put a write behind every admin read. INSERT IGNORE keeps a concurrent ensure from
// erroring (the PK guarantees one row) — and also swallows the FK violation of a card
// deleted mid-flight, which the reload below then surfaces as sql.ErrNoRows.
func (s *Store) EnsureCardViewer(ctx context.Context, techCardID int) (*entity.TechCardPatternViewerAccess, error) {
	row, err := s.GetCardViewer(ctx, techCardID)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load tech card pattern viewer access row: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`INSERT IGNORE INTO tech_card_pattern_viewer_access (tech_card_id) VALUES (:id)`,
		map[string]any{"id": techCardID}); err != nil {
		return nil, fmt.Errorf("ensure tech card pattern viewer access row: %w", err)
	}
	return s.GetCardViewer(ctx, techCardID)
}

// RecordCardViewerAccess folds a debounced batch of viewer access stats into the rows.
// Best effort, mirroring RecordAccess: the audit trail is the slog line per request.
func (s *Store) RecordCardViewerAccess(ctx context.Context, counts map[int]int64, last map[int]time.Time) error {
	var firstErr error
	for id, n := range counts {
		if err := storeutil.ExecNamed(ctx, s.DB,
			`UPDATE tech_card_pattern_viewer_access SET access_count = access_count + :n, last_access_at = :last
			 WHERE tech_card_id = :id`,
			map[string]any{"id": id, "n": n, "last": last[id]}); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record tech card pattern viewer access: %w", err)
		}
	}
	return firstErr
}
