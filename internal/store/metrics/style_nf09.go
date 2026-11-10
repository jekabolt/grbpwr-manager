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
	var summary entity.StyleSampleSummary
	count, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM sample WHERE tech_card_id = :tc`, map[string]any{"tc": techCardID})
	if err != nil {
		return entity.StyleSampleSummary{}, fmt.Errorf("get style sample count %d: %w", techCardID, err)
	}
	summary.Count = count
	movements, err := storeutil.QueryListNamed[entity.MaterialMovement](ctx, s.DB, `
		SELECT mv.movement_type, mv.quantity, mv.unit_cost_base
		FROM sample sp
		JOIN material_stock_movement mv
			ON mv.sample_id = sp.id AND mv.movement_type IN (:issue, :return)
		WHERE sp.tech_card_id = :tc`, map[string]any{
		"tc":     techCardID,
		"issue":  string(entity.MaterialMovementIssueSample),
		"return": string(entity.MaterialMovementReturnSample),
	})
	if err != nil {
		return entity.StyleSampleSummary{}, fmt.Errorf("get style sample summary %d: %w", techCardID, err)
	}
	for _, movement := range movements {
		if !movement.UnitCostBase.Valid {
			summary.HasUncosted = true
			continue
		}
		value := entity.MovementValueCents(movement.Quantity, movement.UnitCostBase.Decimal)
		if movement.MovementType == entity.MaterialMovementIssueSample {
			summary.MaterialsCostBase = summary.MaterialsCostBase.Add(value)
		} else {
			summary.MaterialsCostBase = summary.MaterialsCostBase.Sub(value)
		}
	}
	return summary, nil
}

// GetStyleMaterialsFromStock returns the net warehouse-material cost issued into a style's production
// runs (issue_production − return_production, base EUR), plus whether any issue was uncosted (NF-09).
// This is the material actuals feeding the style production summary, so it is scoped to the SAME runs
// the summary counts — cancelled runs are excluded (nf09-04): otherwise the block would exclude a
// cancelled run from runs/planned/actual yet still sum its issued material here, contradicting itself.
func (s *Store) GetStyleMaterialsFromStock(ctx context.Context, techCardID int) (entity.StyleMaterialsFromStock, error) {
	movements, err := storeutil.QueryListNamed[entity.MaterialMovement](ctx, s.DB, `
		SELECT mv.movement_type, mv.quantity, mv.unit_cost_base
		FROM material_stock_movement mv
		JOIN production_run pr ON pr.id = mv.production_run_id
		WHERE pr.tech_card_id = :tc AND pr.status <> :cancelled AND mv.movement_type IN (:issue, :return)`, map[string]any{
		"tc":        techCardID,
		"issue":     string(entity.MaterialMovementIssueProduction),
		"return":    string(entity.MaterialMovementReturnProduction),
		"cancelled": string(entity.ProductionRunCancelled),
	})
	if err != nil {
		return entity.StyleMaterialsFromStock{}, fmt.Errorf("get style materials from stock %d: %w", techCardID, err)
	}
	var out entity.StyleMaterialsFromStock
	for _, movement := range movements {
		if !movement.UnitCostBase.Valid {
			out.HasUncosted = true
			continue
		}
		value := entity.MovementValueCents(movement.Quantity, movement.UnitCostBase.Decimal)
		if movement.MovementType == entity.MaterialMovementIssueProduction {
			out.Base = out.Base.Add(value)
		} else {
			out.Base = out.Base.Sub(value)
		}
	}
	return out, nil
}
