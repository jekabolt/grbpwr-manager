package inventory

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// BatchIssueMaterialStock issues or returns several materials without exposing partial warehouse
// state when one line is refused.
func (s *Store) BatchIssueMaterialStock(ctx context.Context, ins entity.MaterialBatchIssueInsert) ([]entity.MaterialMovement, error) {
	if err := validateBatchIssue(ins); err != nil {
		return nil, err
	}

	var out []entity.MaterialMovement
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		movements := make([]entity.MaterialMovement, 0, len(ins.Lines))
		for i, line := range ins.Lines {
			mv, err := issueInTx(ctx, rep.DB(), entity.MaterialIssueInsert{
				MaterialId:      line.MaterialId,
				Quantity:        line.Quantity,
				ProductionRunId: ins.ProductionRunId,
				SampleId:        ins.SampleId,
				ProductId:       ins.ProductId,
				LotId:           line.LotId,
				IsReturn:        ins.IsReturn,
				OccurredAt:      ins.OccurredAt,
				Comment:         line.Comment,
				AdminUsername:   ins.AdminUsername,
			}, s.Now())
			if err != nil {
				return fmt.Errorf("line %d (material %d): %w", i+1, line.MaterialId, err)
			}
			movements = append(movements, mv)
		}
		out = movements
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateBatchIssue(ins entity.MaterialBatchIssueInsert) error {
	if len(ins.Lines) == 0 {
		return fmt.Errorf("at least one line is required")
	}
	if ins.ProductionRunId.Valid == ins.SampleId.Valid {
		return fmt.Errorf("exactly one of production_run_id / sample_id must be set")
	}
	if ins.ProductionRunId.Valid && ins.ProductionRunId.Int32 <= 0 {
		return fmt.Errorf("production_run_id must be positive")
	}
	if ins.SampleId.Valid && ins.SampleId.Int32 <= 0 {
		return fmt.Errorf("sample_id must be positive")
	}
	if ins.ProductId.Valid && ins.ProductId.Int32 <= 0 {
		return fmt.Errorf("product_id must be positive")
	}

	seen := make(map[int]int, len(ins.Lines))
	for i, line := range ins.Lines {
		if line.MaterialId <= 0 {
			return fmt.Errorf("line %d: material_id is required", i+1)
		}
		if line.Quantity.LessThanOrEqual(decimal.Zero) {
			return fmt.Errorf("line %d (material %d): issue quantity must be positive", i+1, line.MaterialId)
		}
		if line.LotId.Valid && line.LotId.Int32 <= 0 {
			return fmt.Errorf("line %d (material %d): lot_id must be positive", i+1, line.MaterialId)
		}
		if first, ok := seen[line.MaterialId]; ok {
			return fmt.Errorf("line %d (material %d): duplicate material_id (first used on line %d)", i+1, line.MaterialId, first)
		}
		seen[line.MaterialId] = i + 1
	}
	return nil
}
