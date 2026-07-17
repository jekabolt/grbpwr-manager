package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetStyleSizeChart returns a style's full size chart (R5): every measurement cell plus the shared
// tech_card.lock_version the caller echoes back on a full-replace. sql.ErrNoRows when the style is
// absent (NOT_FOUND upstream).
func (s *Store) GetStyleSizeChart(ctx context.Context, styleID int) (entity.StyleSizeChart, error) {
	return loadStyleSizeChart(ctx, s.DB, styleID)
}

// UpdateStyleSizeChart replaces a style's ENTIRE size chart in one versioned request (R5, full-replace):
// it clears every cell of the style and re-inserts the supplied set, under the shared tech_card
// optimistic lock. A stale expected_lock_version is entity.ErrTechCardConflict (ABORTED upstream); an
// absent style is sql.ErrNoRows. The write bumps the shared lock_version, so a concurrent UpdateStyle /
// UpdateTechCard holding the old version is correctly rejected. Colourway saves never touch the chart.
func (s *Store) UpdateStyleSizeChart(ctx context.Context, styleID, expectedLockVersion int, cells []entity.StyleSizeChartCell) (entity.StyleSizeChart, error) {
	for _, c := range cells {
		if c.SizeID == 0 || c.MeasurementNameID == 0 {
			return entity.StyleSizeChart{}, fmt.Errorf("invalid size chart cell: size_id and measurement_name_id are required")
		}
	}
	var out entity.StyleSizeChart
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion int `db:"lock_version"`
		}](ctx, rep.DB(), `SELECT lock_version FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
		if err != nil {
			return err // sql.ErrNoRows -> NOT_FOUND upstream
		}
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrTechCardConflict
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM tech_card_size_measurement WHERE tech_card_id = :id`, map[string]any{"id": styleID}); err != nil {
			return fmt.Errorf("clear style %d size chart: %w", styleID, err)
		}
		rows := make([]map[string]any, 0, len(cells))
		for _, c := range cells {
			rows = append(rows, map[string]any{
				"tech_card_id":        styleID,
				"size_id":             c.SizeID,
				"measurement_name_id": c.MeasurementNameID,
				"measurement_value":   c.Value,
			})
		}
		if len(rows) > 0 {
			if err := storeutil.BulkInsert(ctx, rep.DB(), "tech_card_size_measurement", rows); err != nil {
				return fmt.Errorf("insert style %d size chart: %w", styleID, err)
			}
		}
		// Bump the shared optimistic lock under the guard (a full-replace is a style mutation).
		affected, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = :id AND lock_version = :expected`,
			map[string]any{"id": styleID, "expected": expectedLockVersion})
		if err != nil {
			return fmt.Errorf("bump style %d lock: %w", styleID, err)
		}
		if affected == 0 {
			return entity.ErrTechCardConflict
		}
		out, err = loadStyleSizeChart(ctx, rep.DB(), styleID)
		return err
	})
	return out, err
}

func loadStyleSizeChart(ctx context.Context, db dependency.DB, styleID int) (entity.StyleSizeChart, error) {
	cur, err := storeutil.QueryNamedOne[struct {
		LockVersion int `db:"lock_version"`
	}](ctx, db, `SELECT lock_version FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, err
	}
	cells, err := storeutil.QueryListNamed[entity.StyleSizeChartCell](ctx, db,
		`SELECT size_id, measurement_name_id, measurement_value FROM tech_card_size_measurement
		 WHERE tech_card_id = :id ORDER BY size_id, measurement_name_id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, fmt.Errorf("load style %d size chart cells: %w", styleID, err)
	}
	return entity.StyleSizeChart{StyleID: styleID, LockVersion: cur.LockVersion, Cells: cells}, nil
}
