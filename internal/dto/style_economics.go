package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
)

// ComputeStyleProductionSummary aggregates a style's production runs into the plan/fact overview on
// the StyleEconomics card (task 15 part C). planned_cost_base = Σ(planned_unit_cost × planned_qty)
// over runs that carry a frozen planned unit cost; actual_cost_base = Σ of every run's recorded
// actual cost articles that folded to base. has_actuals is true once any run recorded an actual
// cost. A style with no runs is all-zero (runs=0, has_actuals=false).
func ComputeStyleProductionSummary(runs []entity.ProductionRun) *pb_admin.StyleProductionSummary {
	out := &pb_admin.StyleProductionSummary{}
	var runs32, plannedQty, receivedQty int64
	planned, actual := decimal.Zero, decimal.Zero
	hasActuals, hasPlan := false, false
	for i := range runs {
		r := &runs[i]
		if r.Status == entity.ProductionRunCancelled {
			continue // an abandoned run is not planned or actual production — don't inflate totals
		}
		runs32++
		var runPlannedQty int64
		for _, ln := range r.Lines {
			runPlannedQty += int64(ln.PlannedQty)
			if ln.ReceivedQty.Valid {
				receivedQty += ln.ReceivedQty.Int64
			}
		}
		plannedQty += runPlannedQty
		if r.PlannedUnitCost.Valid {
			hasPlan = true
			planned = planned.Add(r.PlannedUnitCost.Decimal.Mul(decimal.NewFromInt(runPlannedQty)))
		}
		for _, c := range r.Costs {
			if c.AmountBase.Valid {
				actual = actual.Add(c.AmountBase.Decimal)
				hasActuals = true
			}
		}
	}
	out.Runs = int32(runs32)
	out.PlannedQtyTotal = int32(plannedQty)
	out.ReceivedQtyTotal = int32(receivedQty)
	out.PlannedCostBase = pbDecimalFromDecimal(roundMoney(planned))
	out.ActualCostBase = pbDecimalFromDecimal(roundMoney(actual))
	// Variance only when there is a frozen plan to compare against; otherwise actual−0 would read as
	// a fabricated 100% overrun (mirrors computeProductionRunActuals, which gates on PlannedUnitCost).
	// Left unset (nil) when no run carries a planned unit cost. Note: planned uses planned_qty (full
	// planned spend), so during an in-progress run actual < planned is expected, not a saving.
	if hasPlan {
		out.CostVariance = pbDecimalFromDecimal(roundMoney(actual.Sub(planned)))
	}
	out.HasActuals = hasActuals
	return out
}
