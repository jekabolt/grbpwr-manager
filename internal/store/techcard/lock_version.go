package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetTechCardLockVersion is the cheap header-only optimistic-lock read used to revalidate a source
// immediately before a multi-step operation writes its result.
func (s *Store) GetTechCardLockVersion(ctx context.Context, id int) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		LockVersion int `db:"lock_version"`
	}](ctx, s.DB, `SELECT lock_version FROM tech_card WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return 0, fmt.Errorf("load tech card %d lock version: %w", id, err)
	}
	return row.LockVersion, nil
}
