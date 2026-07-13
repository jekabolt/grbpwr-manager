package dto

import (
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
)

// ComputeProductionRunMaterialPlan estimates a run's material requirement (NF-06 §6.2). For each
// line (product colourway × size × planned_qty) it resolves the colourway's usage norms to catalog
// materials via bom_item_index → bom_item.material_id, applies the BOM wastage, and sums the need
// per material. It compares that plan against on-hand and already-issued stock. It is a plan
// estimate BEFORE the marker — the real consumption comes from stock issues (issued column). It
// writes nothing.
//
// onHand and issued are keyed by material_id (issued = net issue_production − return_production
// already booked to this run). Both may be nil/partial; a missing key reads as zero.
func ComputeProductionRunMaterialPlan(run *entity.ProductionRun, card *entity.TechCard, onHand, issued map[int]decimal.Decimal) *pb_admin.GetProductionRunMaterialPlanResponse {
	out := &pb_admin.GetProductionRunMaterialPlanResponse{}
	if run == nil || card == nil {
		return out
	}

	// product_id → colourway (a colourway is the recipe whose usages give the per-material norms).
	colorwayByProduct := make(map[int]*entity.TechCardColorway, len(card.Colorways))
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		if cw.ProductId.Valid {
			colorwayByProduct[int(cw.ProductId.Int32)] = cw
		}
	}

	type acc struct {
		required     decimal.Decimal
		hasSizeNorms bool
		name         string
		unit         string
	}
	req := make(map[int]*acc)
	order := make([]int, 0) // material ids, in first-seen order (then sorted for a stable response)

	var caveats []string
	noProductNoted, noColorwayNoted := false, false
	noMaterialNoted := make(map[int]bool) // by bom index
	noNormNoted := make(map[string]bool)  // by colourway name

	for i := range run.Lines {
		ln := &run.Lines[i]
		if ln.PlannedQty <= 0 {
			continue
		}
		if !ln.ProductId.Valid {
			if !noProductNoted {
				caveats = append(caveats, "a planned line has no product — not counted")
				noProductNoted = true
			}
			continue
		}
		cw := colorwayByProduct[int(ln.ProductId.Int32)]
		if cw == nil {
			if !noColorwayNoted {
				caveats = append(caveats, fmt.Sprintf("product %d has no matching colourway in the card — not counted", ln.ProductId.Int32))
				noColorwayNoted = true
			}
			continue
		}
		for j := range cw.Usages {
			u := &cw.Usages[j]
			if !u.BomItemIndex.Valid {
				continue
			}
			bi := int(u.BomItemIndex.Int32)
			if bi < 0 || bi >= len(card.BomItems) {
				continue
			}
			bom := &card.BomItems[bi]
			if !bom.MaterialId.Valid {
				if !noMaterialNoted[bi] {
					caveats = append(caveats, fmt.Sprintf("BOM line %q has no linked material — not counted", bom.Name))
					noMaterialNoted[bi] = true
				}
				continue
			}
			norm, sizeGraded, ok := usageNormForSize(u, ln.SizeId)
			if !ok {
				if !noNormNoted[cw.Name] {
					caveats = append(caveats, fmt.Sprintf("colourway %q has a usage with no consumption norm — not counted", cw.Name))
					noNormNoted[cw.Name] = true
				}
				continue
			}
			// wastage grosses the norm up (e.g. 5% → ×1.05); missing → no wastage.
			factor := decimal.NewFromInt(1)
			if bom.WastagePercent.Valid {
				factor = factor.Add(bom.WastagePercent.Decimal.Div(decimal.NewFromInt(100)))
			}
			add := norm.Mul(decimal.NewFromInt(int64(ln.PlannedQty))).Mul(factor)

			mid := int(bom.MaterialId.Int64)
			a := req[mid]
			if a == nil {
				a = &acc{required: decimal.Zero, hasSizeNorms: true, name: bom.Name, unit: bom.Unit.String}
				req[mid] = a
				order = append(order, mid)
			}
			a.required = a.required.Add(add)
			if !sizeGraded {
				a.hasSizeNorms = false
			}
		}
	}

	sort.Ints(order)
	for _, mid := range order {
		a := req[mid]
		on := onHand[mid]
		iss := issued[mid]
		shortage := a.required.Sub(iss).Sub(on)
		if shortage.IsNegative() {
			shortage = decimal.Zero
		}
		out.Rows = append(out.Rows, &pb_admin.MaterialPlanRow{
			MaterialId:   int32(mid),
			MaterialName: a.name,
			Unit:         a.unit,
			Required:     pbDecimalFromDecimal(a.required.Round(3)),
			OnHand:       pbDecimalFromDecimal(on.Round(3)),
			Issued:       pbDecimalFromDecimal(iss.Round(3)),
			Shortage:     pbDecimalFromDecimal(shortage.Round(3)),
			HasSizeNorms: a.hasSizeNorms,
		})
	}
	out.Caveats = caveats
	return out
}

// usageNormForSize returns the per-garment material norm of a usage for a given size: the per-size
// consumption when graded for that size (sizeGraded=true), else the flat per-garment consumption,
// else the countable quantity (a trim count). ok=false when the usage carries no norm at all.
func usageNormForSize(u *entity.TechCardColorwayUsage, sizeID int) (norm decimal.Decimal, sizeGraded, ok bool) {
	for _, sc := range u.SizeConsumptions {
		if sc.SizeId == sizeID {
			return sc.Consumption, true, true
		}
	}
	if u.Consumption.Valid {
		return u.Consumption.Decimal, false, true
	}
	if u.Quantity.Valid {
		return u.Quantity.Decimal, false, true
	}
	return decimal.Zero, false, false
}

// AggregateRunMaterialIssues sums a run's material movements into net issued-per-material (base
// quantity): issue_production adds, return_production subtracts. Used for the material plan's issued
// column.
func AggregateRunMaterialIssues(movements []entity.MaterialMovement) map[int]decimal.Decimal {
	out := make(map[int]decimal.Decimal)
	for _, m := range movements {
		switch m.MovementType {
		case entity.MaterialMovementIssueProduction:
			out[m.MaterialId] = out[m.MaterialId].Add(m.Quantity)
		case entity.MaterialMovementReturnProduction:
			out[m.MaterialId] = out[m.MaterialId].Sub(m.Quantity)
		}
	}
	return out
}
