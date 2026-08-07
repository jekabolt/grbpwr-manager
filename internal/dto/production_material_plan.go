package dto

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// The metre synonym set that used to live here (`m, м, meter, meters, metre, metres`) moved into
// entity.materialUnitSynonyms VERBATIM as the vocabulary's MaterialUnit "m" row (Ф5а.3). It was a
// private map that only the thread conversion below consulted, so every OTHER unit comparison in
// this file was a raw string compare that treated «м» and "m" as two different units — the number
// kept the slot's meaning while being compared against the article's stock. Unit comparisons here
// now go through entity.SameMaterialUnit / entity.NormalizeMaterialUnit, and an unknown unit still
// falls back to the old raw compare, so nothing that used to work stops working.

// planPercentDivisor turns a wastage percent into a fraction.
var planPercentDivisor = decimal.NewFromInt(100)

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
		required decimal.Decimal
		// requiredBeforeGrossup is Σ(norm × planned_qty) with NO gross-up of ANY kind applied — no
		// BOM wastage %, no cutting coefficient — converted into the same unit as required. It is
		// deliberately not "required before the coefficient": a manual line's number here is also
		// before its wastage, and a line can only ever take ONE of the two factors, so a single
		// "before" number is the only one that stays true for every row (Ф5а.2).
		//
		// The invariant is required >= requiredBeforeGrossup. The ratio equals the coefficient only
		// when every contributing line was marker-sourced; on a manual row it is the wastage factor.
		requiredBeforeGrossup decimal.Decimal
		// coefficient is the ARTICLE's coefficient (a property of the article, reported whenever it
		// has one); coefficientApplied records whether any contribution actually took it.
		coefficient        decimal.NullDecimal
		coefficientApplied bool
		// Which kinds of norm fed this row — read only to word the "the coefficient did not bite"
		// caveat correctly: a manual norm takes the BOM wastage % instead, a counted trim takes
		// nothing at all, and telling an operator their counted buttons are "manual norms" is a
		// wrong explanation of a right number.
		hasManualNorms  bool
		hasCountedNorms bool
		hasSizeNorms    bool
		name            string
		unit            string
	}
	req := make(map[int]*matAcc)
	order := make([]int, 0) // material ids, in first-seen order (then sorted for a stable response)

	type contribKey struct {
		bomID      int
		colorwayID int
		materialID int
	}
	type contribAcc struct {
		required decimal.Decimal
		// Same meaning as matAcc.requiredBeforeGrossup, in the slot's spec unit: before wastage AND
		// before the coefficient, because a line takes at most one of them.
		requiredBeforeGrossup decimal.Decimal
		coefficient           decimal.NullDecimal
		hasSizeNorms          bool
		pinned                bool
	}
	contribs := make(map[contribKey]*contribAcc)

	type blockKey struct {
		bomID      int
		colorwayID int
	}
	type blockAcc struct {
		plannedQty int
		reason     string
		// key is the STABLE MACHINE NAME of the same cause. It rides beside the prose rather than
		// replacing it because the two have different jobs and different lifetimes: `reason` is
		// rewritten whenever the sentence can be made clearer, `key` never changes. The Ф6 readiness
		// gate reads these two facts from HERE instead of recomputing them — one fact, one
		// implementation — and it could not do that safely by matching on the sentence.
		key string
	}
	blocks := make(map[blockKey]*blockAcc)
	blockAdd := func(bomID, colorwayID, qty int, key, reason string) {
		k := blockKey{bomID, colorwayID}
		b := blocks[k]
		if b == nil {
			b = &blockAcc{reason: reason, key: key}
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
	noBomNoted := make(map[string]bool) // by colourway name
	// Unit notes are de-duplicated by the STATEMENT, not by the material. The loop below visits the
	// same slot × colourway once per run line (product × size), so some dedupe is needed or a
	// five-size run repeats every note five times — but a latch keyed on the material id alone was
	// worse than noise: it let the FIRST unit note an article produced silence every later one. An
	// article fed by one slot in metres (converted to kg) and another in pcs then reported only the
	// successful conversion, so a total summed across units read as a precise, converted figure.
	// Keying on the text means every DISTINCT thing that is true about this article gets said once.
	unitNoted := make(map[string]bool)
	noteUnit := func(msg string) {
		if unitNoted[msg] {
			return
		}
		unitNoted[msg] = true
		caveats = append(caveats, msg)
	}
	plannedByColorway := map[int]int{} // product id → Σ planned qty (for uncovered-slot blockers)
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
				blockAdd(bom.Id, pid, ln.PlannedQty, entity.MaterialPlanBlockerNoArticle, entity.MaterialPlanReasonNoArticle)
				continue
			}
			norm, sizeGraded, counted, ok := usageNormForSize(u, ln.SizeId)
			if !ok {
				blockAdd(bom.Id, pid, ln.PlannedQty, entity.MaterialPlanBlockerNoNorm, entity.MaterialPlanReasonNoNorm)
				continue
			}
			// base is what the norm alone asks for; factor is the ONE gross-up this line takes.
			//
			// Which gross-up depends on where the norm came from, and the two worlds stay disjoint
			// so no line is ever grossed twice:
			//
			//   * MANUAL / legacy measured norm — the BOM line's wastage estimate (5% → ×1.05),
			//     overridden by the run's ACTUAL cutting wastage when set. Unchanged by Ф5а.
			//   * MARKER-sourced norm — the ARTICLE's cutting coefficient (Ф5а.2). A marker's
			//     measured length already contains the cutting waste of a clean lay on a nominal
			//     width (PIECES-WASTAGE-DESIGN §2.3), which is why the wastage factor must never
			//     touch it; the coefficient covers what a marker CANNOT contain — усадка, обход
			//     пороков, сращивание, оттеночные полосы. Different losses, so no double count.
			//   * counted trim (4 buttons stay 4 buttons) — nothing, mirroring costing's UnitTotal.
			//
			// An article with no coefficient multiplies by nothing: this path produces exactly the
			// number it produced before the field existed.
			base := norm.Mul(decimal.NewFromInt(int64(ln.PlannedQty)))
			factor := decimal.NewFromInt(1)
			lineCoeff := decimal.NullDecimal{}
			markerSourced := u.ConsumptionSource.String == entity.ConsumptionSourceMarker
			switch {
			case counted:
				// no gross-up
			case markerSourced:
				if m, ok := linked[mid]; ok {
					if c := m.EffectiveCuttingCoefficient(); c.Valid {
						factor = c.Decimal
						lineCoeff = c
					}
				}
			default:
				wastage := bom.WastagePercent
				if run.ActualWastagePercent.Valid {
					wastage = run.ActualWastagePercent
				}
				if wastage.Valid {
					factor = factor.Add(wastage.Decimal.Div(planPercentDivisor))
				}
			}
			add := base.Mul(factor)

			ck := contribKey{bom.Id, pid, mid}
			c := contribs[ck]
			if c == nil {
				c = &contribAcc{hasSizeNorms: true, pinned: pinned}
				contribs[ck] = c
			}
			c.required = c.required.Add(add)
			c.requiredBeforeGrossup = c.requiredBeforeGrossup.Add(base)
			if lineCoeff.Valid {
				c.coefficient = lineCoeff
			}
			if !sizeGraded {
				c.hasSizeNorms = false
			}

			// Rollup unit discipline: the number and its label must always agree. Two conversions
			// are known, both one-directional and both keyed off the CLOSED unit vocabulary rather
			// than raw strings (Ф5а.3) — «м» against "m" is one unit and no longer degrades into a
			// caveat that silently compares a slot-unit number against stock:
			//
			//   metres → cones: a slot in metres against an article stocked in cones divides by
			//     length_per_cone_m. Only that direction — a slot authored in cones against metre
			//     stock would divide the wrong way and report a phantom zero shortage.
			//   metres → kilograms (Ф5а.4): a slot in metres against an article BOUGHT BY WEIGHT
			//     multiplies by the roll's full width and density. Only that direction, for the
			//     same reason.
			//
			// Any other mismatch keeps the number AND the label in the slot's unit (noted once as a
			// caveat) — a slot-unit number under the stock unit's label was a purchase order for
			// 18 000 cones.
			stockAdd, stockBase := add, base
			rowUnit := bom.Unit.String
			if m, ok := linked[mid]; ok {
				slotUnit := strings.TrimSpace(bom.Unit.String)
				stockUnit := strings.TrimSpace(m.Unit.String)
				slotU, slotKnown := entity.NormalizeMaterialUnit(slotUnit)
				stockU, stockKnown := entity.NormalizeMaterialUnit(stockUnit)
				fromMetres := slotKnown && slotU == entity.MaterialUnitM
				switch {
				case stockUnit == "" || slotUnit == "" || entity.SameMaterialUnit(slotUnit, stockUnit):
					rowUnit = matUnit(mid, bom)
				case fromMetres && stockKnown && stockU == entity.MaterialUnitKg:
					// Weight is billed on the FULL roll width, кромка included — the selvedge is
					// bought and it weighs. Using the cutting width here would understate what the
					// supplier invoices by 2–4%. Density comes from the ARTICLE (CTI attr over the
					// flat column), never from the BOM line's own fabric_weight_gsm: that one is a
					// spec snapshot of what the card was drawn against and has no consumer.
					width := m.EffectiveFabricWidthCm()
					gsm := m.EffectiveFabricWeightGsm()
					kg := entity.FabricLengthToKg(add, width, gsm)
					kgBase := entity.FabricLengthToKg(base, width, gsm)
					if kg.Valid && kgBase.Valid {
						stockAdd, stockBase = kg.Decimal, kgBase.Decimal
						rowUnit = stockUnit
						noteUnit(fmt.Sprintf("%s: norm in %s converted to %s by full roll width %s cm (кромка included) × %s g/m²",
							matName(mid, bom), slotUnit, stockUnit,
							width.Decimal.String(), gsm.Decimal.String()))
					} else {
						noteUnit(fmt.Sprintf("%s: stocked in %s but the article has no full width and density — cannot convert the %s norm to weight; the row stays in %q",
							matName(mid, bom), stockUnit, slotUnit, slotUnit))
					}
				case fromMetres &&
					m.ThreadAttr != nil && m.ThreadAttr.LengthPerConeM.Valid && m.ThreadAttr.LengthPerConeM.Decimal.IsPositive():
					stockAdd = add.Div(m.ThreadAttr.LengthPerConeM.Decimal)
					stockBase = base.Div(m.ThreadAttr.LengthPerConeM.Decimal)
					rowUnit = stockUnit
					noteUnit(fmt.Sprintf("%s: norm in %s converted to %s via length per cone (%s m)",
						matName(mid, bom), slotUnit, stockUnit, m.ThreadAttr.LengthPerConeM.Decimal.String()))
				default:
					noteUnit(fmt.Sprintf("%s: slot unit %q vs stock unit %q — no conversion; the row stays in %q and stock figures are compared across units",
						matName(mid, bom), slotUnit, stockUnit, slotUnit))
				}
			}
			a := req[mid]
			if a == nil {
				a = &matAcc{hasSizeNorms: true, name: matName(mid, bom), unit: rowUnit}
				if m, ok := linked[mid]; ok {
					// The article's coefficient is reported whether or not this run's norms let it
					// bite, so the operator can see the dial that exists.
					a.coefficient = m.EffectiveCuttingCoefficient()
				}
				req[mid] = a
				order = append(order, mid)
			} else if a.unit != "" && rowUnit != "" && !entity.SameMaterialUnit(a.unit, rowUnit) {
				// The row is about to add a quantity in one unit to a total kept in another, and it
				// will keep the label of whichever unit was seen FIRST. That summation is an older
				// defect with a wider blast radius than this phase, and it is being scoped separately
				// — but it must never be silent, and above all it must not sit underneath a caveat
				// that reads like a successful conversion. Keyed on the accumulator rather than on
				// the conversion notes above, so no amount of successful converting can hide it.
				noteUnit(fmt.Sprintf("%s: this run needs it in BOTH %q and %q — `required` is a SUM ACROSS UNITS "+
					"labelled with the first unit seen (%q), so that number and its shortage are not usable; "+
					"put the slots on one unit",
					a.name, a.unit, rowUnit, a.unit))
			}
			a.required = a.required.Add(stockAdd)
			a.requiredBeforeGrossup = a.requiredBeforeGrossup.Add(stockBase)
			if lineCoeff.Valid {
				a.coefficientApplied = true
			}
			switch {
			case counted:
				a.hasCountedNorms = true
			case !markerSourced:
				a.hasManualNorms = true
			}
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
			blockAdd(b.Id, pid, qty, entity.MaterialPlanBlockerNoNorm, entity.MaterialPlanReasonNoNorm)
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

	// A coefficient that exists but bit nothing is a silent no-op the operator would otherwise blame
	// on the field being broken: it grosses up MARKER-sourced norms only. Say so once per article
	// rather than letting the dial look dead — and say WHICH kind of norm shut it out, because
	// "manual" is a wrong explanation for an article consumed as a counted quantity (Ф5а.2).
	for _, mid := range order {
		a := req[mid]
		if !a.coefficient.Valid || a.coefficientApplied || !a.required.IsPositive() {
			continue
		}
		var because string
		switch {
		case a.hasManualNorms && a.hasCountedNorms:
			because = "this run's norms for it are manual (their BOM wastage % applies instead) or counted quantities (which take no gross-up at all)"
		case a.hasCountedNorms:
			because = "this article is consumed here as a counted quantity — 4 buttons stay 4 buttons — which takes no gross-up at all"
		default:
			because = "this run's norms for it are manual (their BOM wastage % applies instead)"
		}
		caveats = append(caveats, fmt.Sprintf("%s: cutting coefficient %s not applied — it grosses up MARKER-sourced norms, and %s",
			a.name, a.coefficient.Decimal.String(), because))
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
			UnitCode:       pbMaterialUnit(a.unit),
			Required:       pbDecimalFromDecimal(a.required.Round(3)),
			OnHand:         pbDecimalFromDecimal(on.Round(3)),
			Issued:         pbDecimalFromDecimal(iss.Round(3)),
			Shortage:       pbDecimalFromDecimal(shortage.Round(3)),
			HasSizeNorms:   a.hasSizeNorms,
			IssuedVariance: pbDecimalFromDecimal(iss.Sub(a.required).Round(3)),
			// Ф5а.2 — the audit trail of the gross-up: the norm's own un-grossed sum, and the dial.
			// The two are related BY the dial only on a marker-fed row; see the proto comment.
			RequiredBeforeGrossup: pbDecimalFromDecimal(a.requiredBeforeGrossup.Round(3)),
			CuttingCoefficient:    pbDecimalFromNull(a.coefficient),
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
			BomItemId:             int64(k.bomID),
			SlotName:              slotName,
			Section:               section,
			ColorwayId:            int32(k.colorwayID),
			ColorwayName:          colorwayName(k.colorwayID),
			MaterialId:            int32(k.materialID),
			MaterialName:          matName(k.materialID, bom),
			Pinned:                c.pinned,
			Unit:                  unit,
			Required:              pbDecimalFromDecimal(c.required.Round(3)),
			HasSizeNorms:          c.hasSizeNorms,
			RequiredBeforeGrossup: pbDecimalFromDecimal(c.requiredBeforeGrossup.Round(3)),
			CuttingCoefficient:    pbDecimalFromNull(c.coefficient),
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
			Key:          b.key,
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
