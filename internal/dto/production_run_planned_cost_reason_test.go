package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// TestPlannedCostReasonNamesTheEmptyGrid: a batch with no quantities is the most common empty-price
// case (the header is created before the grid is filled), and «цена не посчитана» told the operator
// nothing about it.
func TestPlannedCostReasonNamesTheEmptyGrid(t *testing.T) {
	tc := estimateCard()
	unit, _, reason := ComputeProductionRunPlannedUnitCostWithReason(tc, CostingFx{}, decimal.NullDecimal{}, nil)
	if unit.Valid {
		t.Fatalf("an empty grid produced a price: %s", unit.Decimal)
	}
	if reason == "" {
		t.Fatal("no reason for an empty grid — the screen would print «не посчитана» and leave the person guessing")
	}
}

// TestPlannedCostReasonNamesTheCell: on a twenty-line batch, «партия не считается» is an invitation
// to check the lines by hand. The refusal must name the cell.
func TestPlannedCostReasonNamesTheCell(t *testing.T) {
	tc := estimateCard()
	// A line naming a colourway the card does not have: edge 4 of the computation.
	lines := []entity.ProductionRunLine{{
		ProductId:  sql.NullInt32{Int32: 999, Valid: true},
		SizeId:     4,
		PlannedQty: 10,
	}}
	unit, _, reason := ComputeProductionRunPlannedUnitCostWithReason(tc, CostingFx{}, decimal.NullDecimal{}, lines)
	if unit.Valid {
		t.Fatalf("a batch naming an unknown colourway produced a price: %s", unit.Decimal)
	}
	if reason == "" {
		t.Fatal("no reason for an uncomputable cell")
	}
	if !contains(reason, "999") {
		t.Errorf("reason %q does not name the offending cell", reason)
	}
}

// TestPlannedCostSuccessCarriesNoReason: a reason next to a good number would read as a warning
// about a figure that is fine.
func TestPlannedCostSuccessCarriesNoReason(t *testing.T) {
	tc := estimateCard()
	tc.Colorways[0].Usages = append(tc.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId:   sql.NullInt64{Int64: 56, Valid: true},
		Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("2"), Valid: true},
	})
	lines := []entity.ProductionRunLine{{
		ProductId:  sql.NullInt32{Int32: 35, Valid: true},
		SizeId:     4,
		PlannedQty: 10,
	}}
	unit, _, reason := ComputeProductionRunPlannedUnitCostWithReason(tc, CostingFx{}, decimal.NullDecimal{}, lines)
	if !unit.Valid {
		t.Fatalf("a costable batch produced no price (reason %q)", reason)
	}
	if reason != "" {
		t.Errorf("a successful computation carried a reason: %q", reason)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
