package dto

import (
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// planSlotSections are the BOM sections a colourway recipe is expected to cover — the garment's
// own materials. A slot in one of these with NO norm for a produced colourway is a blocker (the
// plan literally cannot count it). label/packaging/other are deliberately outside: labels ride
// the assembly recipe and packaging the packaging recipe, so their BOM lines carrying no
// colourway usage is normal, not a planning hole.
var planSlotSections = map[entity.TechCardBomSection]bool{
	entity.BomSectionFabric:      true,
	entity.BomSectionLining:      true,
	entity.BomSectionInterlining: true,
	entity.BomSectionInsulation:  true,
	entity.BomSectionThread:      true,
	entity.BomSectionHardware:    true,
	entity.BomSectionTrim:        true,
	entity.BomSectionDecoration:  true,
}

// ComputeProductionRunMaterialPlan estimates a run's material requirement (NF-06 §6.2). For each
// line (product colourway × size × planned_qty) it resolves the colourway's usage norms to catalog
// materials — the colourway's PIN first (usage.material_id), else the slot default
// (bom_item.material_id) — applies wastage to measured norms, and sums the need per material.
//
// The response has three layers with distinct jobs:
//   - rows: one per MATERIAL, in the material's stock unit — the only carrier of on-hand /
//     issued / shortage, so an article shared by two slots or two colourways is compared against
//     its ONE pile of stock exactly once;
//   - contributions: one per slot × colourway × article — the factory-spec breakdown of rows'
//     required, in the slot's spec unit, carrying no stock figures;
//   - blockers: slot × colourway the plan could NOT count (no article, or no norm at all —
//     including a slot the recipe never references, which previously vanished without a trace).
//
// linked resolves article identities (name/unit/thread cone length) for labelling and unit
// conversion; nil degrades to the BOM line's own snapshot fields. onHand and issued are keyed by
// material_id (issued = net issue_production − return_production). All may be nil/partial.
func ComputeProductionRunMaterialPlan(run *entity.ProductionRun, card *entity.TechCard, onHand, issued map[int]decimal.Decimal, linked map[int]entity.MaterialWithPrice) *pb_admin.GetProductionRunMaterialPlanResponse {
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
	// bom item id → its index in card.BomItems (the BOM's display order, used for stable output).
	bomOrder := make(map[int]int, len(card.BomItems))
	for i := range card.BomItems {
		bomOrder[card.BomItems[i].Id] = i
	}

	type matAcc struct {
		required     decimal.Decimal
		hasSizeNorms bool
		name         string
		unit         string
		converted    bool // metres → cones conversion applied
	}
	req := make(map[int]*matAcc)
	order := make([]int, 0) // material ids, in first-seen order (then sorted for a stable response)

	type contribKey struct {
		bomID      int
		colorwayID int
		materialID int
	}
	type contribAcc struct {
		required     decimal.Decimal
		hasSizeNorms bool
		pinned       bool
	}
	contribs := make(map[contribKey]*contribAcc)

	type blockKey struct {
		bomID      int
		colorwayID int
	}
	type blockAcc struct {
		plannedQty int
		reason     string
	}
	blocks := make(map[blockKey]*blockAcc)
	blockAdd := func(bomID, colorwayID, qty int, reason string) {
		k := blockKey{bomID, colorwayID}
		b := blocks[k]
		if b == nil {
			b = &blockAcc{reason: reason}
			blocks[k] = b
		}
		b.plannedQty += qty
	}

	// matName/matUnit label a material by its catalog identity when linked knows it, else by the
	// BOM line's own snapshot — under slots the line's name is the ROLE («основная молния»), so
	// the catalog name must win whenever it is available.
	matName := func(mid int, bom *entity.TechCardBomItem) string {
		if m, ok := linked[mid]; ok && m.Name != "" {
			return m.Name
		}
		if bom != nil {
			return bom.Name
		}
		return fmt.Sprintf("material #%d", mid)
	}
	matUnit := func(mid int, bom *entity.TechCardBomItem) string {
		if m, ok := linked[mid]; ok && m.Unit.Valid && m.Unit.String != "" {
			return m.Unit.String
		}
		if bom != nil {
			return bom.Unit.String
		}
		return ""
	}

	var caveats []string
	noProductNoted, noColorwayNoted := false, false
	noBomNoted := make(map[string]bool)  // by colourway name
	unitNoted := make(map[int]bool)      // by material id — unit mismatch / conversion notes
	plannedByColorway := map[int]int{}   // product id → Σ planned qty (for uncovered-slot blockers)
	usedSlotsByColorway := map[int]map[int]bool{}

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
		pid := int(ln.ProductId.Int32)
		cw := colorwayByProduct[pid]
		if cw == nil {
			if !noColorwayNoted {
				caveats = append(caveats, fmt.Sprintf("product %d has no matching colourway in the card — not counted", pid))
				noColorwayNoted = true
			}
			continue
		}
		plannedByColorway[pid] += ln.PlannedQty
		if usedSlotsByColorway[pid] == nil {
			usedSlotsByColorway[pid] = map[int]bool{}
		}
		for j := range cw.Usages {
			u := &cw.Usages[j]
			bom := planBomLine(u, card.BomItems)
			if bom == nil {
				if !noBomNoted[cw.Name] {
					caveats = append(caveats, fmt.Sprintf("colourway %q has a usage with no resolvable BOM line — not counted", cw.Name))
					noBomNoted[cw.Name] = true
				}
				continue
			}
			usedSlotsByColorway[pid][bom.Id] = true
			mid, pinned := u.EffectiveMaterialId(bom)
			if mid == 0 {
				blockAdd(bom.Id, pid, ln.PlannedQty, "no article (no pin, no slot default)")
				continue
			}
			norm, sizeGraded, counted, ok := usageNormForSize(u, ln.SizeId)
			if !ok {
				blockAdd(bom.Id, pid, ln.PlannedQty, "no consumption norm")
				continue
			}
			// Wastage grosses MEASURED norms up (e.g. 5% → ×1.05) — cutting loss. A counted trim
			// (4 buttons stay 4 buttons) takes none, mirroring the costing's UnitTotal. The run's
			// ACTUAL cutting wastage overrides the BOM line's estimate when set.
			factor := decimal.NewFromInt(1)
			if !counted {
				wastage := bom.WastagePercent
				if run.ActualWastagePercent.Valid {
					wastage = run.ActualWastagePercent
				}
				if wastage.Valid {
					factor = factor.Add(wastage.Decimal.Div(decimal.NewFromInt(100)))
				}
			}
			add := norm.Mul(decimal.NewFromInt(int64(ln.PlannedQty))).Mul(factor)

			ck := contribKey{bom.Id, pid, mid}
			c := contribs[ck]
			if c == nil {
				c = &contribAcc{hasSizeNorms: true, pinned: pinned}
				contribs[ck] = c
			}
			c.required = c.required.Add(add)
			if !sizeGraded {
				c.hasSizeNorms = false
			}

			// Rollup in the material's STOCK unit. The one conversion we know is thread: a slot
			// specified in metres against an article stocked in cones (length_per_cone_m). Any
			// other unit mismatch is surfaced once and compared as-is — a wrong-looking number
			// beats a silently wrong one.
			stockAdd := add
			if m, ok := linked[mid]; ok {
				slotUnit := bom.Unit.String
				stockUnit := m.Unit.String
				if m.Unit.Valid && stockUnit != "" && slotUnit != "" && stockUnit != slotUnit {
					if m.ThreadAttr != nil && m.ThreadAttr.LengthPerConeM.Valid && m.ThreadAttr.LengthPerConeM.Decimal.IsPositive() {
						stockAdd = add.Div(m.ThreadAttr.LengthPerConeM.Decimal)
						if !unitNoted[mid] {
							caveats = append(caveats, fmt.Sprintf("%s: norm in %s converted to %s via length per cone (%s m)",
								matName(mid, bom), slotUnit, stockUnit, m.ThreadAttr.LengthPerConeM.Decimal.String()))
							unitNoted[mid] = true
						}
					} else if !unitNoted[mid] {
						caveats = append(caveats, fmt.Sprintf("%s: slot unit %q vs stock unit %q — compared without conversion",
							matName(mid, bom), slotUnit, stockUnit))
						unitNoted[mid] = true
					}
				}
			}
			a := req[mid]
			if a == nil {
				a = &matAcc{hasSizeNorms: true, name: matName(mid, bom), unit: matUnit(mid, bom)}
				req[mid] = a
				order = append(order, mid)
			}
			a.required = a.required.Add(stockAdd)
			a.converted = a.converted || !stockAdd.Equal(add)
			if !sizeGraded {
				a.hasSizeNorms = false
			}
		}
	}

	// Uncovered slots: a recipe-section BOM line the colourway's recipe never references. Without
	// this, adding «основная молния» to the BOM after the colourways were authored changes NOTHING
	// in any plan — the exact silent hole slots exist to close.
	for pid, qty := range plannedByColorway {
		for i := range card.BomItems {
			b := &card.BomItems[i]
			if !planSlotSections[entity.TechCardBomSection(b.Section)] {
				continue
			}
			if usedSlotsByColorway[pid][b.Id] {
				continue
			}
			blockAdd(b.Id, pid, qty, "no consumption norm")
		}
	}

	// Issued-but-not-required union: material already issued to the run whose requirement is no
	// longer in the plan (recipe changed, line removed). Without a row it would just vanish —
	// the variance column is where the operator sees it.
	issuedOnly := make([]int, 0)
	for mid, q := range issued {
		if _, ok := req[mid]; !ok && !q.IsZero() {
			issuedOnly = append(issuedOnly, mid)
		}
	}
	sort.Ints(issuedOnly)
	for _, mid := range issuedOnly {
		req[mid] = &matAcc{hasSizeNorms: true, name: matName(mid, nil), unit: matUnit(mid, nil)}
		order = append(order, mid)
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
		// issued − required: after the marker is cut and material issued, this is the real over/under
		// vs the theoretical plan — positive means more fabric went out than the norm predicted (marker
		// inefficiency / scrap), negative means the run under-issued or has leftover (gap-04).
		out.Rows = append(out.Rows, &pb_admin.MaterialPlanRow{
			MaterialId:     int32(mid),
			MaterialName:   a.name,
			Unit:           a.unit,
			Required:       pbDecimalFromDecimal(a.required.Round(3)),
			OnHand:         pbDecimalFromDecimal(on.Round(3)),
			Issued:         pbDecimalFromDecimal(iss.Round(3)),
			Shortage:       pbDecimalFromDecimal(shortage.Round(3)),
			HasSizeNorms:   a.hasSizeNorms,
			IssuedVariance: pbDecimalFromDecimal(iss.Sub(a.required).Round(3)),
		})
	}

	// Contributions and blockers in BOM display order, then colourway id — stable and mirroring
	// the spec sheet top-to-bottom.
	colorwayName := func(pid int) string {
		if cw := colorwayByProduct[pid]; cw != nil {
			return cw.Name
		}
		return fmt.Sprintf("#%d", pid)
	}
	bomByID := func(id int) *entity.TechCardBomItem {
		if i, ok := bomOrder[id]; ok {
			return &card.BomItems[i]
		}
		return nil
	}

	ckeys := make([]contribKey, 0, len(contribs))
	for k := range contribs {
		ckeys = append(ckeys, k)
	}
	sort.Slice(ckeys, func(i, j int) bool {
		oi, oj := bomOrder[ckeys[i].bomID], bomOrder[ckeys[j].bomID]
		if oi != oj {
			return oi < oj
		}
		if ckeys[i].colorwayID != ckeys[j].colorwayID {
			return ckeys[i].colorwayID < ckeys[j].colorwayID
		}
		return ckeys[i].materialID < ckeys[j].materialID
	})
	for _, k := range ckeys {
		c := contribs[k]
		bom := bomByID(k.bomID)
		slotName, unit := "", ""
		section := pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN
		if bom != nil {
			slotName = bom.Name
			unit = bom.Unit.String
			section = pbBomSection(entity.TechCardBomSection(bom.Section))
		}
		out.Contributions = append(out.Contributions, &pb_admin.MaterialPlanContribution{
			BomItemId:    int64(k.bomID),
			SlotName:     slotName,
			Section:      section,
			ColorwayId:   int32(k.colorwayID),
			ColorwayName: colorwayName(k.colorwayID),
			MaterialId:   int32(k.materialID),
			MaterialName: matName(k.materialID, bom),
			Pinned:       c.pinned,
			Unit:         unit,
			Required:     pbDecimalFromDecimal(c.required.Round(3)),
			HasSizeNorms: c.hasSizeNorms,
		})
	}

	bkeys := make([]blockKey, 0, len(blocks))
	for k := range blocks {
		bkeys = append(bkeys, k)
	}
	sort.Slice(bkeys, func(i, j int) bool {
		oi, oj := bomOrder[bkeys[i].bomID], bomOrder[bkeys[j].bomID]
		if oi != oj {
			return oi < oj
		}
		return bkeys[i].colorwayID < bkeys[j].colorwayID
	})
	for _, k := range bkeys {
		b := blocks[k]
		bom := bomByID(k.bomID)
		slotName := ""
		if bom != nil {
			slotName = bom.Name
		}
		out.Blockers = append(out.Blockers, &pb_admin.MaterialPlanBlocker{
			BomItemId:    int64(k.bomID),
			SlotName:     slotName,
			ColorwayId:   int32(k.colorwayID),
			ColorwayName: colorwayName(k.colorwayID),
			PlannedQty:   int32(b.plannedQty),
			Reason:       b.reason,
		})
	}

	out.Caveats = caveats
	return out
}

// planBomLine resolves a usage's BOM line for the plan: by the read-resolved FK first (bom_item_id —
// the stable line_key world, S2/S3), else the legacy positional index. Mirrors the recipe read's
// resolveUsageBom priority; found live by the beta A–L run (H.22b: a line_key-keyed recipe produced
// an empty material plan because this compute only understood positional indices).
func planBomLine(u *entity.TechCardColorwayUsage, items []entity.TechCardBomItem) *entity.TechCardBomItem {
	if u.BomItemId.Valid {
		for i := range items {
			if int64(items[i].Id) == u.BomItemId.Int64 {
				return &items[i]
			}
		}
	}
	if u.BomItemIndex.Valid {
		if bi := int(u.BomItemIndex.Int32); bi >= 0 && bi < len(items) {
			return &items[bi]
		}
	}
	return nil
}

// usageNormForSize returns the per-garment material norm of a usage for a given size: the per-size
// consumption when graded for that size (sizeGraded=true), else the flat per-garment consumption,
// else the countable quantity (counted=true — a trim count, which never takes cutting wastage).
// ok=false when the usage carries no norm at all.
func usageNormForSize(u *entity.TechCardColorwayUsage, sizeID int) (norm decimal.Decimal, sizeGraded, counted, ok bool) {
	for _, sc := range u.SizeConsumptions {
		if sc.SizeId == sizeID {
			return sc.Consumption, true, false, true
		}
	}
	if u.Consumption.Valid {
		return u.Consumption.Decimal, false, false, true
	}
	if u.Quantity.Valid {
		return u.Quantity.Decimal, false, true, true
	}
	return decimal.Zero, false, false, false
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
