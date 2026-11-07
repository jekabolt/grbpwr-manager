package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// GetStyleSizeChart returns the full grid, row metadata and applied shrinkage.
func (s *Store) GetStyleSizeChart(ctx context.Context, styleID int) (entity.StyleSizeChart, error) {
	return loadStyleSizeChart(ctx, s.DB, styleID)
}

// UpdateStyleSizeChart replaces the expanded grid and grade rule under tech_card.lock_version.
// pointsSet distinguishes a new client doing a full point-row replace from an old client that has
// never seen point metadata: the latter keeps authored rows and only reconciles default typed rows.
func (s *Store) UpdateStyleSizeChart(
	ctx context.Context,
	styleID, expectedLockVersion int,
	cells []entity.StyleSizeChartCell,
	gradeBaseSizeID int,
	gradeSteps []entity.StyleSizeChartGradeStep,
	points []entity.StyleSizeChartPoint,
	pointsSet bool,
	shrinkage *decimal.Decimal,
) (entity.StyleSizeChart, error) {
	for i := range cells {
		cell := &cells[i]
		if cell.SizeID == 0 || cell.MeasurementNameID == 0 {
			return entity.StyleSizeChart{}, fmt.Errorf("invalid size chart cell: size_id and measurement_name_id are required")
		}
		if cell.Source == "" {
			cell.Source = entity.StyleSizeChartCellSourceManual
		}
		if cell.Source != entity.StyleSizeChartCellSourceManual && cell.Source != entity.StyleSizeChartCellSourcePattern {
			return entity.StyleSizeChart{}, entity.NewFieldViolation(fmt.Sprintf("cells[%d].source", i), "invalid", cell.Source, "manual or pattern")
		}
	}
	for _, step := range gradeSteps {
		if step.MeasurementNameID == 0 {
			return entity.StyleSizeChart{}, fmt.Errorf("invalid grade step: measurement_name_id is required")
		}
	}
	if shrinkage != nil && (shrinkage.IsNegative() || shrinkage.GreaterThan(decimal.NewFromInt(100))) {
		return entity.StyleSizeChart{}, entity.NewFieldViolation("shrinkage_pct", "out_of_range", shrinkage.String(), "between 0 and 100")
	}
	if pointsSet {
		if err := entity.ValidateStyleSizeChartPoints(points); err != nil {
			return entity.StyleSizeChart{}, err
		}
	}

	var out entity.StyleSizeChart
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion int `db:"lock_version"`
		}](ctx, db, `SELECT lock_version FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
		if err != nil {
			return err
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, styleID); err != nil {
			return err
		}
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrTechCardConflict
		}
		rng, err := storeutil.LoadTechCardSizeRange(ctx, db, styleID)
		if err != nil {
			return err
		}
		for i, cell := range cells {
			if err := rng.Require(fmt.Sprintf("cells[%d].size_id", i), cell.SizeID); err != nil {
				return err
			}
		}
		if err := rng.Require("grade_base_size_id", gradeBaseSizeID); err != nil {
			return err
		}

		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_size_measurement WHERE tech_card_id = :id`, map[string]any{"id": styleID}); err != nil {
			return fmt.Errorf("clear style %d size chart: %w", styleID, err)
		}
		cellRows := make([]map[string]any, 0, len(cells))
		for _, cell := range cells {
			cellRows = append(cellRows, map[string]any{
				"tech_card_id": styleID, "size_id": cell.SizeID,
				"measurement_name_id": cell.MeasurementNameID,
				"measurement_value":   cell.Value, "source": cell.Source,
			})
		}
		if len(cellRows) > 0 {
			if err := storeutil.BulkInsert(ctx, db, "tech_card_size_measurement", cellRows); err != nil {
				return fmt.Errorf("insert style %d size chart: %w", styleID, err)
			}
		}

		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_grade_rule WHERE tech_card_id = :id`, map[string]any{"id": styleID}); err != nil {
			return fmt.Errorf("clear style %d grade rule: %w", styleID, err)
		}
		stepRows := make([]map[string]any, 0, len(gradeSteps))
		for _, step := range gradeSteps {
			stepRows = append(stepRows, map[string]any{
				"tech_card_id": styleID, "measurement_name_id": step.MeasurementNameID, "step": step.Step,
			})
		}
		if len(stepRows) > 0 {
			if err := storeutil.BulkInsert(ctx, db, "tech_card_grade_rule", stepRows); err != nil {
				return fmt.Errorf("insert style %d grade rule: %w", styleID, err)
			}
		}

		finalPoints, err := reconcileStyleSizeChartPoints(ctx, db, styleID, cells, gradeSteps, points, pointsSet)
		if err != nil {
			return err
		}
		shrinkageValue := decimal.NullDecimal{}
		if shrinkage != nil {
			shrinkageValue = decimal.NullDecimal{Decimal: *shrinkage, Valid: true}
		} else if !pointsSet {
			// An old client never sends the override: keep the stored one instead of clearing it.
			kept, err := storeutil.QueryNamedOne[struct {
				ShrinkagePct decimal.NullDecimal `db:"size_chart_shrinkage_pct"`
			}](ctx, db, `SELECT size_chart_shrinkage_pct FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
			if err != nil {
				return fmt.Errorf("load style %d size chart shrinkage: %w", styleID, err)
			}
			shrinkageValue = kept.ShrinkagePct
		}
		if err := resolveStyleSizeChartPointFingerprints(ctx, db, styleID, finalPoints, shrinkageValue, true); err != nil {
			return err
		}
		if err := replaceStyleSizeChartPoints(ctx, db, styleID, finalPoints); err != nil {
			return err
		}

		affected, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE tech_card
			SET lock_version = lock_version + 1,
			    grade_base_size_id = :grade_base,
			    size_chart_shrinkage_pct = :shrinkage
			WHERE id = :id AND lock_version = :expected`, map[string]any{
			"id": styleID, "expected": expectedLockVersion,
			"grade_base": nullableID(gradeBaseSizeID), "shrinkage": shrinkageValue,
		})
		if err != nil {
			return fmt.Errorf("bump style %d lock: %w", styleID, err)
		}
		if affected == 0 {
			return entity.ErrTechCardConflict
		}
		out, err = loadStyleSizeChart(ctx, db, styleID)
		return err
	})
	return out, err
}

func reconcileStyleSizeChartPoints(
	ctx context.Context,
	db dependency.DB,
	styleID int,
	cells []entity.StyleSizeChartCell,
	steps []entity.StyleSizeChartGradeStep,
	supplied []entity.StyleSizeChartPoint,
	pointsSet bool,
) ([]entity.StyleSizeChartPoint, error) {
	points := append([]entity.StyleSizeChartPoint(nil), supplied...)
	if !pointsSet {
		var err error
		points, err = loadStyleSizeChartPoints(ctx, db, styleID)
		if err != nil {
			return nil, err
		}
	}
	needed := make(map[int]bool, len(cells)+len(steps))
	for _, cell := range cells {
		needed[cell.MeasurementNameID] = true
	}
	for _, step := range steps {
		needed[step.MeasurementNameID] = true
	}
	if !pointsSet {
		kept := points[:0]
		for _, point := range points {
			if needed[point.MeasurementNameID] || point.PomCode != "" {
				kept = append(kept, point)
			}
		}
		points = kept
	}
	present := make(map[int]bool, len(points))
	maxPosition := 0
	for _, point := range points {
		present[point.MeasurementNameID] = true
		if point.Position > maxPosition {
			maxPosition = point.Position
		}
	}
	missing := make([]int, 0)
	for measurementID := range needed {
		if !present[measurementID] {
			missing = append(missing, measurementID)
		}
	}
	sort.Ints(missing)
	for _, measurementID := range missing {
		maxPosition++
		points = append(points, entity.StyleSizeChartPoint{
			MeasurementNameID: measurementID, Position: maxPosition,
			TolMinusCm: decimal.NewFromInt(1), TolPlusCm: decimal.NewFromInt(1), InShop: true,
		})
	}
	if err := entity.ValidateStyleSizeChartPoints(points); err != nil {
		return nil, err
	}
	return points, nil
}

type styleSizeChartPointRow struct {
	MeasurementNameID int             `db:"measurement_name_id"`
	PomCode           string          `db:"pom_code"`
	Position          int             `db:"position"`
	TolMinusCm        decimal.Decimal `db:"tol_minus_cm"`
	TolPlusCm         decimal.Decimal `db:"tol_plus_cm"`
	InShop            bool            `db:"in_shop"`
	BasisPieces       []byte          `db:"basis_pieces"`
	SourceFingerprint string          `db:"source_fingerprint"`
}

func loadStyleSizeChartPoints(ctx context.Context, db dependency.DB, styleID int) ([]entity.StyleSizeChartPoint, error) {
	rows, err := storeutil.QueryListNamed[styleSizeChartPointRow](ctx, db, `
		SELECT measurement_name_id, pom_code, position, tol_minus_cm, tol_plus_cm,
		       in_shop, basis_pieces, source_fingerprint
		FROM tech_card_size_chart_point
		WHERE tech_card_id = :id
		ORDER BY position, measurement_name_id`, map[string]any{"id": styleID})
	if err != nil {
		return nil, fmt.Errorf("load style %d size chart points: %w", styleID, err)
	}
	out := make([]entity.StyleSizeChartPoint, 0, len(rows))
	for _, row := range rows {
		point := entity.StyleSizeChartPoint{
			MeasurementNameID: row.MeasurementNameID, PomCode: row.PomCode, Position: row.Position,
			TolMinusCm: row.TolMinusCm, TolPlusCm: row.TolPlusCm, InShop: row.InShop,
			SourceFingerprint: row.SourceFingerprint,
		}
		if err := json.Unmarshal(row.BasisPieces, &point.BasisPieceLineKeys); err != nil {
			return nil, fmt.Errorf("decode style %d point %d basis pieces: %w", styleID, point.MeasurementNameID, err)
		}
		out = append(out, point)
	}
	return out, nil
}

func replaceStyleSizeChartPoints(ctx context.Context, db dependency.DB, styleID int, points []entity.StyleSizeChartPoint) error {
	if err := storeutil.ExecNamed(ctx, db,
		`DELETE FROM tech_card_size_chart_point WHERE tech_card_id = :id`, map[string]any{"id": styleID}); err != nil {
		return fmt.Errorf("clear style %d size chart points: %w", styleID, err)
	}
	rows := make([]map[string]any, 0, len(points))
	for _, point := range points {
		basis, err := json.Marshal(point.BasisPieceLineKeys)
		if err != nil {
			return fmt.Errorf("encode style %d point %d basis pieces: %w", styleID, point.MeasurementNameID, err)
		}
		rows = append(rows, map[string]any{
			"tech_card_id": styleID, "measurement_name_id": point.MeasurementNameID,
			"pom_code": point.PomCode, "position": point.Position,
			"tol_minus_cm": point.TolMinusCm, "tol_plus_cm": point.TolPlusCm,
			"in_shop": point.InShop, "basis_pieces": string(basis),
			"source_fingerprint": point.SourceFingerprint,
		})
	}
	if len(rows) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "tech_card_size_chart_point", rows); err != nil {
			return fmt.Errorf("insert style %d size chart points: %w", styleID, err)
		}
	}
	return nil
}

// resolveStyleSizeChartPointFingerprints is the one provenance implementation for writes and reads.
// stamp=true records the current projection; stamp=false compares it with what was stored.
func resolveStyleSizeChartPointFingerprints(
	ctx context.Context,
	db dependency.DB,
	styleID int,
	points []entity.StyleSizeChartPoint,
	shrinkage decimal.NullDecimal,
	stamp bool,
) error {
	needsSources := false
	for _, point := range points {
		if len(point.BasisPieceLineKeys) > 0 {
			needsSources = true
			break
		}
	}
	if !needsSources {
		for i := range points {
			if stamp {
				points[i].SourceFingerprint = ""
			} else {
				points[i].Stale = false
			}
		}
		return nil
	}
	sources, err := loadSeamSources(ctx, db, styleID)
	if err != nil {
		return err
	}
	for i := range points {
		basis := entity.NormalizedPieceLineKeys(points[i].BasisPieceLineKeys)
		points[i].BasisPieceLineKeys = basis
		current := entity.StyleSizeChartPointFingerprint(basis, sources.fingerprint(basis), shrinkage)
		if stamp {
			points[i].SourceFingerprint = current
		} else {
			points[i].Stale = points[i].SourceFingerprint != "" && points[i].SourceFingerprint != current
		}
	}
	return nil
}

func nullableID(id int) sql.NullInt32 {
	if id <= 0 {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(id), Valid: true}
}

func loadStyleSizeChart(ctx context.Context, db dependency.DB, styleID int) (entity.StyleSizeChart, error) {
	cur, err := storeutil.QueryNamedOne[struct {
		LockVersion     int                 `db:"lock_version"`
		GradeBaseSizeID sql.NullInt32       `db:"grade_base_size_id"`
		ShrinkagePct    decimal.NullDecimal `db:"size_chart_shrinkage_pct"`
	}](ctx, db, `
		SELECT lock_version, grade_base_size_id, size_chart_shrinkage_pct
		FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, err
	}
	cells, err := storeutil.QueryListNamed[entity.StyleSizeChartCell](ctx, db, `
		SELECT size_id, measurement_name_id, measurement_value, source
		FROM tech_card_size_measurement m
		WHERE m.tech_card_id = :id
		  AND (EXISTS (SELECT 1 FROM tech_card_size z
		               WHERE z.tech_card_id = m.tech_card_id AND z.size_id = m.size_id)
		       OR NOT EXISTS (SELECT 1 FROM tech_card_size z WHERE z.tech_card_id = m.tech_card_id))
		ORDER BY size_id, measurement_name_id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, fmt.Errorf("load style %d size chart cells: %w", styleID, err)
	}
	steps, err := storeutil.QueryListNamed[entity.StyleSizeChartGradeStep](ctx, db, `
		SELECT measurement_name_id, step FROM tech_card_grade_rule
		WHERE tech_card_id = :id ORDER BY measurement_name_id`, map[string]any{"id": styleID})
	if err != nil {
		return entity.StyleSizeChart{}, fmt.Errorf("load style %d grade rule: %w", styleID, err)
	}
	points, err := loadStyleSizeChartPoints(ctx, db, styleID)
	if err != nil {
		return entity.StyleSizeChart{}, err
	}
	if err := resolveStyleSizeChartPointFingerprints(ctx, db, styleID, points, cur.ShrinkagePct, false); err != nil {
		return entity.StyleSizeChart{}, err
	}
	return entity.StyleSizeChart{
		StyleID: styleID, LockVersion: cur.LockVersion, Cells: cells,
		GradeBaseSizeID: int(cur.GradeBaseSizeID.Int32), GradeSteps: steps,
		Points: points, ShrinkagePct: cur.ShrinkagePct,
	}, nil
}
