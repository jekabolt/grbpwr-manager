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
	out := &pb_admin.StyleProductionSummary{Runs: int32(len(runs))}
	var plannedQty, receivedQty int64
	planned, actual := decimal.Zero, decimal.Zero
	hasActuals := false
	for i := range runs {
		r := &runs[i]
		var runPlannedQty int64
		for _, sz := range r.Sizes {
			runPlannedQty += int64(sz.PlannedQty)
			if sz.ReceivedQty.Valid {
				receivedQty += sz.ReceivedQty.Int64
			}
		}
		plannedQty += runPlannedQty
		if r.PlannedUnitCost.Valid {
			planned = planned.Add(r.PlannedUnitCost.Decimal.Mul(decimal.NewFromInt(runPlannedQty)))
		}
		for _, c := range r.Costs {
			if c.AmountBase.Valid {
				actual = actual.Add(c.AmountBase.Decimal)
				hasActuals = true
			}
		}
	}
	out.PlannedQtyTotal = int32(plannedQty)
	out.ReceivedQtyTotal = int32(receivedQty)
	out.PlannedCostBase = pbDecimalFromDecimal(roundMoney(planned))
	out.ActualCostBase = pbDecimalFromDecimal(roundMoney(actual))
	out.CostVariance = pbDecimalFromDecimal(roundMoney(actual.Sub(planned)))
	out.HasActuals = hasActuals
	return out
}
