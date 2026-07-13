package metrics

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetStyleSampleSummary returns how many samples a style (tech card) has and the net warehouse-
// material cost they consumed (issue_sample − return_sample, valued at the frozen movement cost, base
// EUR), plus whether any of those issues was uncosted (NF-09). This is informational: sample material
// is R&D spend, so it is NOT folded into the style's sales net_after_dev — it just tells the operator
// what sampling drew from the warehouse. Manual sample dev-expenses are excluded here (they are
// already counted in the style's dev-cost).
func (s *Store) GetStyleSampleSummary(ctx context.Context, techCardID int) (entity.StyleSampleSummary, error) {
	summary, err := storeutil.QueryNamedOne[entity.StyleSampleSummary](ctx, s.DB, `
		SELECT
			(SELECT COUNT(*) FROM sample WHERE tech_card_id = :tc) AS count,
			COALESCE(SUM(
				CASE mv.movement_type
					WHEN :issue  THEN mv.quantity * COALESCE(mv.unit_cost_base, 0)
					WHEN :return THEN -mv.quantity * COALESCE(mv.unit_cost_base, 0)
					ELSE 0 END), 0) AS materials_cost_base,
			CASE WHEN SUM(
				CASE WHEN mv.movement_type IN (:issue, :return) AND mv.unit_cost_base IS NULL
					THEN 1 ELSE 0 END) > 0 THEN 1 ELSE 0 END AS has_uncosted
		FROM sample sp
		LEFT JOIN material_stock_movement mv
			ON mv.sample_id = sp.id AND mv.movement_type IN (:issue, :return)
		WHERE sp.tech_card_id = :tc`, map[string]any{
		"tc":     techCardID,
		"issue":  string(entity.MaterialMovementIssueSample),
		"return": string(entity.MaterialMovementReturnSample),
	})
	if err != nil {
		return entity.StyleSampleSummary{}, fmt.Errorf("get style sample summary %d: %w", techCardID, err)
	}
	summary.MaterialsCostBase = summary.MaterialsCostBase.Round(2)
	return summary, nil
}

// GetStyleMaterialsFromStock returns the net warehouse-material cost issued into a style's production
// runs (issue_production − return_production, base EUR), across all its runs, plus whether any issue
// was uncosted (NF-09). This is the material actuals feeding the style production summary.
func (s *Store) GetStyleMaterialsFromStock(ctx context.Context, techCardID int) (entity.StyleMaterialsFromStock, error) {
	out, err := storeutil.QueryNamedOne[entity.StyleMaterialsFromStock](ctx, s.DB, `
		SELECT
			COALESCE(SUM(
				CASE mv.movement_type
					WHEN :issue  THEN mv.quantity * COALESCE(mv.unit_cost_base, 0)
					WHEN :return THEN -mv.quantity * COALESCE(mv.unit_cost_base, 0)
					ELSE 0 END), 0) AS base,
			CASE WHEN SUM(
				CASE WHEN mv.movement_type IN (:issue, :return) AND mv.unit_cost_base IS NULL
					THEN 1 ELSE 0 END) > 0 THEN 1 ELSE 0 END AS has_uncosted
		FROM material_stock_movement mv
		JOIN production_run pr ON pr.id = mv.production_run_id
		WHERE pr.tech_card_id = :tc AND mv.movement_type IN (:issue, :return)`, map[string]any{
		"tc":     techCardID,
		"issue":  string(entity.MaterialMovementIssueProduction),
		"return": string(entity.MaterialMovementReturnProduction),
	})
	if err != nil {
		return entity.StyleMaterialsFromStock{}, fmt.Errorf("get style materials from stock %d: %w", techCardID, err)
	}
	out.Base = out.Base.Round(2)
	return out, nil
}
