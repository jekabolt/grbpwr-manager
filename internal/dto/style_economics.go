package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
)

// ComputeStyleProductionSummary aggregates a style's production runs into the plan/fact overview on
// the StyleEconomics card (task 15 part C). planned_cost_base = Σ(planned_unit_cost × planned_qty)
// over runs that carry a frozen planned unit cost; actual_cost_base = Σ of every run's recorded manual
// actual-cost articles that folded to base PLUS `materialsFromStockBase` — the net warehouse material
// issued into the style's (non-cancelled) runs, exactly as the run-level actuals do
// (computeProductionRunActuals: totalBase = manual + materials_from_stock). Keeping the style roll-up
// and the run detail on the same rule stops the style card reading a "saving" (actual excl. materials)
// while the run detail shows the full cost (nf09-02). This is a PRODUCTION cost figure only — it is
// NOT folded into sales net_after_dev, which stays warehouse-free per the domain rule.
// has_actuals is true once any run recorded a manual actual OR any warehouse material was issued.
// A style with no runs and no stock materials is all-zero (runs=0, has_actuals=false). The caller
// passes materialsFromStockBase from GetStyleMaterialsFromStock, which is scoped to the SAME
// non-cancelled runs this summary counts (nf09-04).
func ComputeStyleProductionSummary(runs []entity.ProductionRun, materialsFromStockBase decimal.Decimal, hasStockMaterials bool) *pb_admin.StyleProductionSummary {
	out := &pb_admin.StyleProductionSummary{}
	var runs32, plannedQty, receivedQty int64
	planned, actual := decimal.Zero, decimal.Zero
	hasActuals, hasPlan := false, false
	for i := range runs {
		r := &runs[i]
		if !runCountsAsProduction(r) {
			continue // an abandoned run is not planned or actual production — don't inflate totals
		}
		runs32++
		plannedThisRun := runPlannedQty(r)
		for _, ln := range r.Lines {
			if ln.ReceivedQty.Valid {
				receivedQty += ln.ReceivedQty.Int64
			}
		}
		plannedQty += plannedThisRun
		// Same currency guard as the run-level actuals: actual is always base, a planned snapshot in
		// the costing currency would make the roll-up's variance an FX artefact (plannedCostInBase).
		if plannedCostInBase(r) {
			hasPlan = true
			planned = planned.Add(r.PlannedUnitCost.Decimal.Mul(decimal.NewFromInt(plannedThisRun)))
		}
		for _, c := range r.Costs {
			if c.AmountBase.Valid {
				actual = actual.Add(c.AmountBase.Decimal)
				hasActuals = true
			}
		}
	}
	// Fold warehouse materials into the actual, same as run-level. hasStockMaterials records that
	// material was issued even if the net rounds to zero (issues == returns), so the caveat about
	// uncosted issues still applies.
	actual = actual.Add(materialsFromStockBase)
	hasActuals = hasActuals || hasStockMaterials

	out.Runs = int32(runs32)
	out.PlannedQtyTotal = int32(plannedQty)
	out.ReceivedQtyTotal = int32(receivedQty)
	out.PlannedCostBase = pbDecimalFromDecimal(roundMoney(planned))
	out.ActualCostBase = pbDecimalFromDecimal(roundMoney(actual))
	out.MaterialsFromStockBase = pbDecimalFromDecimal(roundMoney(materialsFromStockBase))
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

// runCountsAsProduction reports whether a run belongs in a style's production picture at all. A
// cancelled run is abandoned production: it never made a garment and never will, so it inflates
// neither the quantity a style is planned to produce nor the money spent on it.
func runCountsAsProduction(r *entity.ProductionRun) bool {
	return r.Status != entity.ProductionRunCancelled
}

// runPlannedQty is Σ planned_qty over one run's grid lines — the garments that run intends to make.
func runPlannedQty(r *entity.ProductionRun) int64 {
	total := int64(0)
	for _, ln := range r.Lines {
		total += int64(ln.PlannedQty)
	}
	return total
}

// PlannedRunQtyTotal is Σ planned_qty over a style's NON-CANCELLED production runs: how many
// garments of this style are actually planned to exist. It is the same number
// ComputeStyleProductionSummary reports as planned_qty_total, by construction rather than by
// coincidence — both go through runCountsAsProduction + runPlannedQty.
//
// That sharing is the point. This is now also the denominator R&D amortisation divides by
// (ComputeTechCardDevCostSummary), and the two figures appear on the SAME style card: a second
// query with its own idea of which runs count would put two different production quantities a few
// pixels apart, and whichever one the reader trusted would be a coin toss. 0 means the style has no
// planned production yet — a real answer, not a missing one, and the caller must treat it as "do not
// divide" rather than substitute something.
func PlannedRunQtyTotal(runs []entity.ProductionRun) int64 {
	total := int64(0)
	for i := range runs {
		r := &runs[i]
		if !runCountsAsProduction(r) {
			continue
		}
		total += runPlannedQty(r)
	}
	return total
}
