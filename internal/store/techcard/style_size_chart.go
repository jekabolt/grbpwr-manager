package techcard

import (
	"context"
	"database/sql"
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
//
// The grade rule (base size + per-measurement step) is replaced in the same transaction and under the
// same lock: it is the authoring rule the cells were expanded from, so persisting one without the other
// would let them drift.
func (s *Store) UpdateStyleSizeChart(ctx context.Context, styleID, expectedLockVersion int, cells []entity.StyleSizeChartCell, gradeBaseSizeID int, gradeSteps []entity.StyleSizeChartGradeStep) (entity.StyleSizeChart, error) {
	for _, c := range cells {
		if c.SizeID == 0 || c.MeasurementNameID == 0 {
			return entity.StyleSizeChart{}, fmt.Errorf("invalid size chart cell: size_id and measurement_name_id are required")
		}
	}
	for _, g := range gradeSteps {
		if g.MeasurementNameID == 0 {
			return entity.StyleSizeChart{}, fmt.Errorf("invalid grade step: measurement_name_id is required")
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
		if err := storeutil.RequireMutableTechCard(ctx, rep.DB(), styleID); err != nil {
			return err
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
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM tech_card_grade_rule WHERE tech_card_id = :id`, map[string]any{"id": styleID}); err != nil {
			return fmt.Errorf("clear style %d grade rule: %w", styleID, err)
		}
		stepRows := make([]map[string]any, 0, len(gradeSteps))
		for _, g := range gradeSteps {
			stepRows = append(stepRows, map[string]any{
				"tech_card_id":        styleID,
				"measurement_name_id": g.MeasurementNameID,
				"step":                g.Step,
			})
		}
		if len(stepRows) > 0 {
			if err := storeutil.BulkInsert(ctx, rep.DB(), "tech_card_grade_rule", stepRows); err != nil {
				return fmt.Errorf("insert style %d grade rule: %w", styleID, err)
			}
		}
		// Bump the shared optimistic lock under the guard (a full-replace is a style mutation), and
		// carry the grade base size on the same statement — it is one column of the same rule.
		affected, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET lock_version = lock_version + 1, grade_base_size_id = :gradeBase
			 WHERE id = :id AND lock_version = :expected`,
			map[string]any{"id": styleID, "expected": expectedLockVersion, "gradeBase": nullableID(gradeBaseSizeID)})
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

// nullableID maps a 0 "unset" id onto SQL NULL, so clearing the grade base writes NULL rather than a
// 0 that no size row can satisfy under the FK.
func nullableID(id int) sql.NullInt32 {
	if id <= 0 {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(id), Valid: true}
}

func loadStyleSizeChart(ctx context.Context, db dependency.DB, styleID int) (entity.StyleSizeChart, error) {
	cur, err := storeutil.QueryNamedOne[struct {
		LockVersion     int           `db:"lock_version"`
		GradeBaseSizeID sql.NullInt32 `db:"grade_base_size_id"`
	}](ctx, db, `SELECT lock_version, grade_base_size_id FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, err
	}
	// Cells for sizes the style no longer makes are not returned — same rule as the storefront read.
	// A save is a full replace of what it is handed, so serving a stranded cell would have the editor
	// write it straight back (and, with the size-range check on the write, be refused for a column it
	// never showed the author). A style with no declared range keeps every cell: the grid is simply
	// not picked yet.
	cells, err := storeutil.QueryListNamed[entity.StyleSizeChartCell](ctx, db,
		`SELECT size_id, measurement_name_id, measurement_value FROM tech_card_size_measurement m
		 WHERE m.tech_card_id = :id
		   AND (EXISTS (SELECT 1 FROM tech_card_size z
		                WHERE z.tech_card_id = m.tech_card_id AND z.size_id = m.size_id)
		        OR NOT EXISTS (SELECT 1 FROM tech_card_size z WHERE z.tech_card_id = m.tech_card_id))
		 ORDER BY size_id, measurement_name_id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, fmt.Errorf("load style %d size chart cells: %w", styleID, err)
	}
	steps, err := storeutil.QueryListNamed[entity.StyleSizeChartGradeStep](ctx, db,
		`SELECT measurement_name_id, step FROM tech_card_grade_rule
		 WHERE tech_card_id = :id ORDER BY measurement_name_id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, fmt.Errorf("load style %d grade rule: %w", styleID, err)
	}
	return entity.StyleSizeChart{
		StyleID:         styleID,
		LockVersion:     cur.LockVersion,
		Cells:           cells,
		GradeBaseSizeID: int(cur.GradeBaseSizeID.Int32),
		GradeSteps:      steps,
	}, nil
}
