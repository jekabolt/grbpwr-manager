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
//
// lays (Ф4.6) are the run's настилы. THEY SWITCH THE REQUIREMENT PER PAIR (колорвей, слот) AND
// NEVER PER RUN: a run whose основная ткань is laid and whose подкладка is not counts the основная
// by measurement and the подкладка by the norm, and every number says which it is
// (MaterialPlanRow.source, MaterialPlanContribution.source, plan_source). A whole-run switch would
// zero the подкладка — the same lie as the norm, only in the other direction. nil = no настилы, and
// the plan is byte-for-byte what it was before this parameter existed.
func ComputeProductionRunMaterialPlan(run *entity.ProductionRun, card *entity.TechCard, onHand, issued map[int]decimal.Decimal, linked map[int]entity.MaterialWithPrice, lays []entity.ProductionRunLay) *pb_admin.GetProductionRunMaterialPlanResponse {
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
	bomByID := func(id int) *entity.TechCardBomItem {
		if i, ok := bomOrder[id]; ok {
			return &card.BomItems[i]
		}
		return nil
	}
	colorwayName := func(pid int) string {
		if cw := colorwayByProduct[pid]; cw != nil {
			return cw.Name
		}
		return fmt.Sprintf("#%d", pid)
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
		// Which kinds of contribution fed this row — read only to word the "the coefficient did not
		// bite" caveat correctly. С W3 таких видов ровно два, и ИСТОЧНИК НОРМЫ СРЕДИ НИХ БОЛЬШЕ НЕ
		// ЧИСЛИТСЯ: manual, dxf и marker берут коэффициент одинаково, поэтому «ваши нормы ручные»
		// объясняло бы молчание дела, которого нет.
		//
		// hasCountedNorms — счётная фурнитура: 4 пуговицы остаются 4 пуговицами, гросс-апа нет
		// вовсе. hasNonRollNorms — мерная строка НЕ рулонной секции: усадка и пороки суть свойства
		// полотна в рулоне, и на нитке коэффициент бессмыслен. Сказать оператору «нормы ручные» про
		// его пуговицы (или про нитку) — ровно тот неверный ответ на верное число, ради которого
		// этот разбор и существует.
		hasCountedNorms bool
		hasNonRollNorms bool
		hasSizeNorms    bool
		// hasNormSource / hasLaySource are the row's SIGNATURE (Ф4.6). A row is keyed by ARTICLE and
		// an article can be fed by two slots at once — one laid, one not — so the two are counted
		// rather than collapsed into one value on the way in. MIXED is the honest answer for that row
		// and it is computed from these two flags, never guessed from the magnitude of the number.
		hasNormSource bool
		hasLaySource  bool
		name          string
		unit          string
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
		// source is ALWAYS PURE here — NORM or LAYS, never MIXED. A contribution is ONE slot of ONE
		// colourway, and §7.3 switches exactly that pair, so there is nothing for it to be mixed
		// between. MIXED exists only one level up, on the article-keyed row.
		source pb_admin.ProductionRunCoverageSource
		// layClothCm / layEndLossCm are the LAYS decomposition, zero on the norm path. Two facts, not
		// one sum: cloth under the markers is a different physical thing from the cloth lost at the
		// two ends of every ply, and Ф5б compares the fact against each of them separately.
		layClothCm   decimal.Decimal
		layEndLossCm decimal.Decimal
		// unit is the unit `required` is actually IN. It used to be read off the BOM line at emit
		// time, which was true only because the norm path never leaves the slot's unit. A настил
		// measures cloth in METRES whatever the slot's spec unit says, so the label has to travel with
		// the number instead of being re-derived from the slot.
		unit string
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

	// Ф4.6 — WHICH PAIRS THE НАСТИЛЫ ANSWER FOR. Built BEFORE the norm walk because the walk has to
	// know, at each (колорвей, слот), whether the norm is still the source of truth for that pair.
	// A настил that lost its slot contributes to NOTHING and says so by name (§7.3, §14 п.6).
	layDemand, layNotes := planLayDemandByPair(lays)
	caveats = append(caveats, layNotes...)
	// layArticle remembers the article the RECIPE resolves for a laid pair, so the lay's metres are
	// attributed to the SAME article every other lay reader uses — ResolveLayArticle, the one
	// resolver (garment pin → sole piece pin → slot default), never the positional index alone and
	// never a private second rule that would let the plan count a different roll than the lay view
	// shows and the cutting room lays.
	type layArticleRef struct {
		mid    int
		pinned bool
		// pieceClash — ResolveLayArticle's contest, to be named ONCE per pair in the lays pass.
		pieceClash []int
		// rowMid is the FIRST visited garment row's own resolution, kept solely so the clash check
		// below can still notice a pair whose rows resolve to two different articles.
		rowMid int
	}
	layArticle := make(map[planLayPairKey]layArticleRef, len(layDemand))
	// The loop below visits each (колорвей, слот) once per RUN LINE, i.e. once per size, so a pair
	// that resolves to two articles would otherwise say so five times on a five-size run.
	layArticleClash := make(map[planLayPairKey]bool)
	// laidSlots feeds the «отход не применён» caveat (§7.1): a wastage percent that silently stops
	// biting is the same species of no-op as a cutting coefficient that never applies, and the
	// existing machinery for that is copied rather than reinvented.
	laidSlots := make(map[int]bool, len(layDemand))

	// planConvert turns a quantity pair expressed in the SLOT's spec unit into the ARTICLE's stock
	// unit. Extracted so the NORM path and the LAYS path convert through ONE implementation: two
	// copies of this table would eventually disagree about what a metre is, and the disagreement
	// would be a number under the wrong label rather than an error.
	//
	// Rollup unit discipline: the number and its label must always agree. Two conversions are known,
	// both one-directional and both keyed off the CLOSED unit vocabulary rather than raw strings
	// (Ф5а.3) — «м» against "m" is one unit and no longer degrades into a caveat that silently
	// compares a slot-unit number against stock:
	//
	//	metres → cones: a slot in metres against an article stocked in cones divides by
	//	  length_per_cone_m. Only that direction — a slot authored in cones against metre stock would
	//	  divide the wrong way and report a phantom zero shortage.
	//	metres → kilograms (Ф5а.4): a slot in metres against an article BOUGHT BY WEIGHT multiplies
	//	  by the roll's full width and density. Only that direction, for the same reason.
	//
	// Any other mismatch keeps the number AND the label in the slot's unit (noted once as a caveat) —
	// a slot-unit number under the stock unit's label was a purchase order for 18 000 cones.
	planConvert := func(mid int, bom *entity.TechCardBomItem, slotUnitRaw string, add, base decimal.Decimal) (decimal.Decimal, decimal.Decimal, string) {
		stockAdd, stockBase := add, base
		rowUnit := slotUnitRaw
		m, ok := linked[mid]
		if !ok {
			return stockAdd, stockBase, rowUnit
		}
		slotUnit := strings.TrimSpace(slotUnitRaw)
		stockUnit := strings.TrimSpace(m.Unit.String)
		slotU, slotKnown := entity.NormalizeMaterialUnit(slotUnit)
		stockU, stockKnown := entity.NormalizeMaterialUnit(stockUnit)
		fromMetres := slotKnown && slotU == entity.MaterialUnitM
		switch {
		case stockUnit == "" || slotUnit == "" || entity.SameMaterialUnit(slotUnit, stockUnit):
			rowUnit = matUnit(mid, bom)
		case fromMetres && stockKnown && stockU == entity.MaterialUnitKg:
			// Weight is billed on the FULL roll width, кромка included — the selvedge is bought and it
			// weighs. Using the cutting width here would understate what the supplier invoices by
			// 2–4%. Density comes from the ARTICLE (CTI attr over the flat column), never from the BOM
			// line's own fabric_weight_gsm: that one is a spec snapshot of what the card was drawn
			// against and has no consumer.
			width := m.EffectiveFabricWidthCm()
			gsm := m.EffectiveFabricWeightGsm()
			kg := entity.FabricLengthToKg(add, width, gsm)
			kgBase := entity.FabricLengthToKg(base, width, gsm)
			if kg.Valid && kgBase.Valid {
				stockAdd, stockBase = kg.Decimal, kgBase.Decimal
				rowUnit = stockUnit
				noteUnit(fmt.Sprintf("%s: norm in %s converted to %s by full roll width %s cm (selvedge included) × %s g/m²",
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
		return stockAdd, stockBase, rowUnit
	}

	// accUnitConflict is the row-level «this article arrives in two units» alarm, kept beside the
	// accumulator it guards. See its message: the sum it describes is an older defect being scoped
	// separately, and it must never sit underneath a caveat that reads like a successful conversion.
	accUnitConflict := func(a *matAcc, rowUnit string) {
		if a.unit != "" && rowUnit != "" && !entity.SameMaterialUnit(a.unit, rowUnit) {
			noteUnit(fmt.Sprintf("%s: this run needs it in BOTH %q and %q — `required` is a SUM ACROSS UNITS "+
				"labelled with the first unit seen (%q), so that number and its shortage are not usable; "+
				"put the slots on one unit",
				a.name, a.unit, rowUnit, a.unit))
		}
	}

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
			// A piece-bound row (entity.IsPieceMaterialAssignment) assigns a material to a
			// cut-piece and carries no norm: no contribution, no blocker, and no «slot covered»
			// mark — the requirement of the slot is counted from its GARMENT-level row. A slot
			// referenced ONLY by piece rows therefore falls into the uncovered-slot blocker
			// below, which is the honest verdict: it has no garment norm to plan by.
			if u.IsPieceMaterialAssignment() {
				continue
			}
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
			// Ф4.6 — THE SWITCH, AND IT IS HERE, ON THE PAIR. This slot of this colourway has настилы:
			// its requirement is a MEASUREMENT (length × plies + end losses), so the norm walk stops
			// for this pair and ONLY for this pair. The sibling slot two lines down — подкладка with no
			// настил yet — falls through to the norm below and keeps its non-zero requirement.
			//
			// The article for the pair is resolved ONCE, through ResolveLayArticle — the same
			// resolver the lay view and the calibration read — and remembered; the row-by-row walk
			// only checks that no OTHER garment row of the pair resolves elsewhere, and names the
			// pair once when one does.
			if _, laid := layDemand[planLayPairKey{colorwayID: pid, bomItemID: bom.Id}]; laid {
				k := planLayPairKey{colorwayID: pid, bomItemID: bom.Id}
				if prev, seen := layArticle[k]; !seen {
					res := ResolveLayArticle(card, pid, int64(bom.Id))
					layArticle[k] = layArticleRef{mid: res.MaterialId, pinned: res.Pinned, pieceClash: res.PieceClash, rowMid: mid}
				} else if prev.rowMid != mid && mid != 0 && !layArticleClash[k] {
					layArticleClash[k] = true
					caveats = append(caveats, fmt.Sprintf(
						"slot %q of colourway %q resolves to two different articles — the lay-based requirement is attributed to #%d",
						bom.Name, colorwayName(pid), prev.mid))
				}
				continue
			}
			if mid == 0 {
				blockAdd(bom.Id, pid, ln.PlannedQty, entity.MaterialPlanBlockerNoArticle, entity.MaterialPlanReasonNoArticle)
				continue
			}
			// Пара (колорвей × слот) — из ТОГО ЖЕ среза cw.Usages, по которому идёт этот цикл:
			// счётная норма слота и запас применяются ОДИН РАЗ на пару (0333), иначе слот с двумя
			// размещениями заказал бы двойное количество и двойной запас.
			norm, sizeGraded, counted, ok := usageNormForSize(u, ln.SizeId, entity.CountablePairUsages(cw.Usages, bom), bom)
			if !ok {
				blockAdd(bom.Id, pid, ln.PlannedQty, entity.MaterialPlanBlockerNoNorm, entity.MaterialPlanReasonNoNorm)
				continue
			}
			// base is what the norm alone asks for; factor is the gross-up this line takes — И ЭТО
			// ТОТ ЖЕ ФАКТОР, ЧТО БЕРЁТ СЕБЕСТОИМОСТЬ ТОЙ ЖЕ СТРОКИ (entity.NormGrossUp).
			//
			// ДВА МНОЖИТЕЛЯ С РАЗНЫМИ БАЗАМИ, а не «один из двух» (это правило W3 и заменило):
			//
			//   * ГЕОМЕТРИЯ НАСТИЛА — процент слота (5% → ×1.05), переопределяемый фактическим
			//     процентом отхода ПРОГОНА, когда он задан. Платит за межлекальные выпады и концы
			//     настила. MARKER-строке не начисляется никогда: измеренная длина раскладки эти
			//     выпады уже содержит (PIECES-WASTAGE-DESIGN §2.3), и второй раз — двойной счёт.
			//   * РЕАЛЬНОСТЬ РУЛОНА — коэффициент АРТИКУЛА (Ф5а.2), и он начисляется ЛЮБОЙ мерной
			//     роликовой строке независимо от источника нормы: усадку, обход пороков, сращивание
			//     и оттеночные полосы не содержит НИ ОДНА раскладка — ни измеренная, ни netto.
			//   * СЧЁТНАЯ фурнитура — ничего вовсе (4 пуговицы остаются 4 пуговицами), зеркально
			//     раннему возврату счётной ветки в костинге.
			//
			// ПОЧЕМУ ЭТО ИЗМЕНИЛОСЬ (W3). Раньше коэффициент кусал ТОЛЬКО marker-строку, и основание
			// у исключения было ровно одно: процент тогда оплачивал в том числе усадку и пороки,
			// так что начислить сверх него коэффициент значило бы оплатить их дважды. W3 сузил смысл
			// процента до чистой геометрии настила — и вместе с основанием исчезло исключение.
			// Оставить его значило бы просить у закупки на 2–6% меньше ткани, чем оплатила
			// себестоимость: линейно, молча и ровно на тех строках, где коэффициент задан.
			//
			// Артикул без коэффициента умножает на ничто: строка считается ровно так же, как до
			// появления поля.
			base := norm.Mul(decimal.NewFromInt(int64(ln.PlannedQty)))
			factor := decimal.NewFromInt(1)
			lineCoeff := decimal.NullDecimal{}
			// ИСТОЧНИК НОРМЫ ЗДЕСЬ БОЛЬШЕ НЕ РАЗБИРАЕТСЯ. До W3 на нём стояла развилка гросс-апа
			// (marker → коэффициент, остальные → процент); теперь разницу между провенансами знает
			// ровно одно место — entity.wastageApplies внутри NormGrossUp, — и второе чтение
			// consumption_source в этом файле было бы началом следующего расхождения.
			nonRoll := !entity.IsRollGoodsSection(bom.Section)
			if !counted {
				// ТОТ ЖЕ БАЗОВЫЙ ОБЪЕКТ И ТО ЖЕ ПРАВИЛО, ЧТО У КОСТИНГА. Коэффициент проставляется
				// тем же резолвером (withCuttingCoefficient → EffectiveMaterialId — артикул уже
				// разрешён в mid двадцатью строками выше, это он же), а множители складывает
				// entity.NormGrossUp — функция, которую зовёт и grossNorm. Переписать здесь
				// арифметику заново нельзя: именно так эти две стороны и разъехались до W3.
				planBom := withCuttingCoefficient(bom, u, linked)
				factor = u.NormGrossUp(planBom, run.ActualWastagePercent)
				lineCoeff = planBom.EffectiveCuttingCoefficient()
			}
			add := base.Mul(factor)

			ck := contribKey{bom.Id, pid, mid}
			c := contribs[ck]
			if c == nil {
				c = &contribAcc{hasSizeNorms: true, pinned: pinned, unit: bom.Unit.String}
				contribs[ck] = c
			}
			c.required = c.required.Add(add)
			c.requiredBeforeGrossup = c.requiredBeforeGrossup.Add(base)
			c.source = pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM
			if lineCoeff.Valid {
				c.coefficient = lineCoeff
			}
			if !sizeGraded {
				c.hasSizeNorms = false
			}

			stockAdd, stockBase, rowUnit := planConvert(mid, bom, bom.Unit.String, add, base)
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
			} else {
				accUnitConflict(a, rowUnit)
			}
			a.required = a.required.Add(stockAdd)
			a.requiredBeforeGrossup = a.requiredBeforeGrossup.Add(stockBase)
			a.hasNormSource = true
			if lineCoeff.Valid {
				a.coefficientApplied = true
			}
			// Виды норм, из-за которых коэффициент МОЖЕТ не укусить, — только они и записываются.
			// Источник нормы (manual / dxf / marker) с W3 в этот список НЕ входит: коэффициент
			// начисляется любой мерной роликовой норме независимо от провенанса, поэтому «ваши
			// нормы ручные» перестало быть объяснением чего бы то ни было.
			switch {
			case counted:
				a.hasCountedNorms = true
			case nonRoll:
				a.hasNonRollNorms = true
			}
			if !sizeGraded {
				a.hasSizeNorms = false
			}
		}
	}

	// --- Ф4.6: ПОТРЕБНОСТЬ ПАР, КОТОРЫЕ УШЛИ НА НАСТИЛЫ ------------------------------------------
	//
	// Driven by the НАСТИЛ, not by the run lines: the lay's length does not depend on the size grid
	// (it already contains it — the marker's состав is what was laid), so walking the lines here
	// would multiply one measurement by the number of sizes.
	//
	// NO COEFFICIENT AND NO WASTAGE ON THIS PATH (решение Р4). Both exist to correct an ESTIMATE
	// derived from a norm; a настил is not an estimate, it is a measurement — length × plies plus end
	// losses, with the losses already named separately. Laying a coefficient calibrated against
	// normative estimates on top of it counts the same thing twice by an unknown amount. required
	// therefore EQUALS required_before_grossup here, which is exactly what §7.3 asks for: the number
	// Ф5б.2 compares the FACT against.
	laidKeys := make([]planLayPairKey, 0, len(layDemand))
	for k := range layDemand {
		laidKeys = append(laidKeys, k)
	}
	sort.Slice(laidKeys, func(i, j int) bool {
		oi, oj := bomOrder[laidKeys[i].bomItemID], bomOrder[laidKeys[j].bomItemID]
		if oi != oj {
			return oi < oj
		}
		return laidKeys[i].colorwayID < laidKeys[j].colorwayID
	})
	for _, k := range laidKeys {
		d := layDemand[k]
		bom := bomByID(k.bomItemID)
		if bom == nil {
			// The slot is live in the настил but absent from the card this plan loaded. Nothing to
			// attribute the cloth to, and silence would drop a measured requirement on the floor.
			caveats = append(caveats, fmt.Sprintf(
				"lays %s: slot #%d is not found in this card — their requirement is not counted",
				d.label(), k.bomItemID))
			continue
		}
		// The lay PROVES the slot is used by this colourway, whatever the recipe says. Marking it
		// here keeps the uncovered-slot blocker below from firing on a slot the cutting room has
		// already planned to lay.
		if usedSlotsByColorway[k.colorwayID] == nil {
			usedSlotsByColorway[k.colorwayID] = map[int]bool{}
		}
		usedSlotsByColorway[k.colorwayID][bom.Id] = true

		ref, ok := layArticle[k]
		if !ok {
			// The norm walk never visited this pair (the colourway has no planned line, or the
			// slot's recipe rows are all piece assignments) — resolve it HERE through the same
			// resolver: garment pin → sole piece pin → slot default. Falling back to the slot
			// default alone would be a second resolver, and it would count a different roll than
			// the one the cut plan names and the lot validation accepted.
			res := ResolveLayArticle(card, k.colorwayID, int64(k.bomItemID))
			ref = layArticleRef{mid: res.MaterialId, pinned: res.Pinned, pieceClash: res.PieceClash}
		}
		mid, pinned := ref.mid, ref.pinned
		if len(ref.pieceClash) > 1 {
			// Той же интонацией, что layArticleClash выше: выбор назван, а не сделан молча.
			caveats = append(caveats, fmt.Sprintf(
				"slot %q of colourway %q: piece rows pin %d different articles and no garment-level row pins one — the lay-based requirement is attributed to the first pin (#%d)",
				bom.Name, colorwayName(k.colorwayID), len(ref.pieceClash), mid))
		}
		if mid == 0 {
			blockAdd(bom.Id, k.colorwayID, plannedByColorway[k.colorwayID], entity.MaterialPlanBlockerNoArticle, entity.MaterialPlanReasonNoArticle)
			continue
		}
		// Marked only AFTER the article resolved: a pair that ended as a blocker produced no number at
		// all, and «процент отхода не применён, потребность посчитана по настилам» would then describe
		// a requirement that was never computed.
		laidSlots[bom.Id] = true
		if _, planned := plannedByColorway[k.colorwayID]; !planned {
			caveats = append(caveats, fmt.Sprintf(
				"lays %s are built for colourway %q, which the run does not plan — their metreage still counts towards the requirement",
				d.label(), colorwayName(k.colorwayID)))
		}
		if d.totalCm().IsZero() {
			caveats = append(caveats, fmt.Sprintf(
				"lays %s (slot %q, colourway %q) yield no length — this pair's lay-based requirement is zero, and the norm no longer applies to it",
				d.label(), bom.Name, colorwayName(k.colorwayID)))
		}
		caveats = append(caveats, d.unmeasured...)

		// The настил measures CLOTH, and cloth is measured in metres — whatever the slot's spec unit
		// happens to say. When the slot agrees, its own spelling is kept («м» stays «м») so the
		// contribution reads like every other row; when it does not, the number stays metres and the
		// mismatch is stated rather than laundered through the slot's label.
		clothM := d.clothCm.Div(planCmPerMetre)
		endLossM := d.endLossCm.Div(planCmPerMetre)
		geom := clothM.Add(endLossM)

		// КОЭФФИЦИЕНТ ЛОЖИТСЯ И НА НАСТИЛ (W3) — ПОСЛЕ ГЕОМЕТРИИ, НЕ ВНУТРЬ НЕЁ.
		//
		// Настил — это ПЛАН того, что положат на стол: длина раскладки × слои + концы. Усадка,
		// обход пороков, сращивание и оттеночные полосы случаются с ПОЛОТНОМ и не зависят от того,
		// чем посчитали требование — нормой или измеренной раскладкой. Оставить настильную часть без
		// коэффициента значило бы на MIXED-строке грossить нормовую половину и не грossить
		// настеленную: одна строка, два разных правила, и цех недополучает ровно на настеленной.
		//
		// ГЕОМЕТРИЯ ОСТАЁТСЯ ЧИСТОЙ, И ЭТО УСЛОВИЕ НЕКРУГОВОСТИ. LayPlannedGeometryOf — знаменатель
		// калибровки САМОГО коэффициента (факт ÷ план-геометрия): попади коэффициент внутрь неё,
		// дрейф схлопнулся бы к нулю на артикуле, который на самом деле садится, и величина стала бы
		// подтверждать сама себя. Поэтому умножение стоит ЗДЕСЬ, на закупочном требовании, а не в
		// LayPlannedGeometryOf — и калибровка продолжает читать геометрию напрямую.
		layBom := withArticleCuttingCoefficient(bom, mid, linked)
		layCoeff := layBom.EffectiveCuttingCoefficient()
		add := geom
		if layCoeff.Valid {
			add = add.Mul(layCoeff.Decimal)
		}
		contribUnit := bom.Unit.String
		if u, ok := entity.NormalizeMaterialUnit(bom.Unit.String); !ok || u != entity.MaterialUnitM {
			contribUnit = string(entity.MaterialUnitM)
			spec := strings.TrimSpace(bom.Unit.String)
			if spec == "" {
				spec = "not set"
			}
			noteUnit(fmt.Sprintf("%s: slot %q is specified in unit %q, but a lay measures cloth in metres — the lay contribution is given in metres",
				matName(mid, bom), bom.Name, spec))
		}

		ck := contribKey{bom.Id, k.colorwayID, mid}
		c := contribs[ck]
		if c == nil {
			// hasSizeNorms is TRUE on a lay, and that is not a claim about norms. The flag warns that
			// a requirement was extrapolated from a per-garment average instead of a per-size one; a
			// настил extrapolates nothing — the sizes are already inside the раскладки it stacks.
			// Marking it false would label the most precise number on the page a rougher estimate.
			c = &contribAcc{hasSizeNorms: true, pinned: pinned, unit: contribUnit}
			contribs[ck] = c
		}
		c.required = c.required.Add(add)
		// §7.3: before-grossup on the LAYS path IS the lay plan — ЧИСТАЯ ГЕОМЕТРИЯ, без коэффициента.
		// До W3 два числа здесь совпадали (наценки на этом пути не было вовсе); теперь они расходятся
		// ровно на коэффициент, и это делает пару читаемой: «раскладки просят 60.8, закупка — 64.448».
		// Инвариант required >= required_before_grossup держится.
		c.requiredBeforeGrossup = c.requiredBeforeGrossup.Add(geom)
		c.source = pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS
		c.layClothCm = c.layClothCm.Add(d.clothCm)
		c.layEndLossCm = c.layEndLossCm.Add(d.endLossCm)

		stockAdd, stockBase, rowUnit := planConvert(mid, bom, contribUnit, add, geom)
		a := req[mid]
		if a == nil {
			a = &matAcc{hasSizeNorms: true, name: matName(mid, bom), unit: rowUnit}
			if m, ok := linked[mid]; ok {
				a.coefficient = m.EffectiveCuttingCoefficient()
			}
			req[mid] = a
			order = append(order, mid)
		} else {
			accUnitConflict(a, rowUnit)
		}
		a.required = a.required.Add(stockAdd)
		a.requiredBeforeGrossup = a.requiredBeforeGrossup.Add(stockBase)
		a.hasLaySource = true
		if layCoeff.Valid {
			a.coefficientApplied = true
		}
		if !entity.IsRollGoodsSection(bom.Section) {
			// Нерулонный слот, посчитанный настилом, — экзотика, но объяснение у него то же, что у
			// нерулонной нормы, и оно обязано доехать до кавеата: иначе коэффициент на таком артикуле
			// молча «не сработал» без единого слова.
			a.hasNonRollNorms = true
		}
	}

	// §7.1 — a wastage percent that stopped biting must SAY so. The operator typed «фактический
	// процент отхода» on the run and would otherwise watch it change nothing on the slot the cutting
	// room has already laid; the silence would read as a broken field rather than as the deliberate
	// no-op it is. Same species as the «coefficient did not apply» machinery below, same treatment.
	if len(laidSlots) > 0 {
		laidIDs := make([]int, 0, len(laidSlots))
		for id := range laidSlots {
			laidIDs = append(laidIDs, id)
		}
		sort.Slice(laidIDs, func(i, j int) bool { return bomOrder[laidIDs[i]] < bomOrder[laidIDs[j]] })
		for _, id := range laidIDs {
			bom := bomByID(id)
			if bom == nil {
				continue
			}
			switch {
			case run.ActualWastagePercent.Valid:
				caveats = append(caveats, fmt.Sprintf(
					"the run's wastage percent %s%% is not applied to slot %q: the requirement is computed from lays (a measurement), not from the norm",
					run.ActualWastagePercent.Decimal.String(), bom.Name))
			case bom.WastagePercent.Valid:
				caveats = append(caveats, fmt.Sprintf(
					"the wastage percent %s%% of slot %q is not applied: the requirement is computed from lays (a measurement), not from the norm",
					bom.WastagePercent.Decimal.String(), bom.Name))
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
			// ОЦЕНКА ПО ПЛОЩАДИ (Ф1) ЗДЕСЬ НЕ СТАНОВИТСЯ ПОТРЕБНОСТЬЮ — И ЭТО РЕШЕНИЕ, А НЕ ПРОБЕЛ.
			//
			// В костинге netto-оценка полезна: она честная нижняя граница и снимает «ноль» с
			// карточки, у которой всё заполнено. В ЗАКУПКЕ та же нижняя граница вредна ровно
			// настолько же, насколько там полезна: по ней закупят на все межлекальные выпады меньше,
			// чем нужно (по замерам беты — около трети), и обнаружится это посреди настила.
			//
			// Поэтому слот, у которого есть только оценка, остаётся БЛОКЕРОМ — «остановись и сними
			// раскладку», а не «закупим по нижней границе». Меняется лишь формулировка: она называет
			// следующий шаг, потому что «нормы нет» на карточке с посчитанной оценкой звучит как
			// неправда и отправляет вписывать число руками.
			// СПРАШИВАЕМ ПРО ГЕОМЕТРИЮ (normDerived), А НЕ ПРО ДЕНЬГИ (ok). План выбирает
			// ФОРМУЛИРОВКУ: «есть только оценка по площади» против «нормы расхода нет», — и это
			// вопрос о выкройках, а не о прайсе. Через ok ответ зависел бы от валютной лестницы
			// (пустая базовая валюта ниже: у артикула с ценой в EUR при костинге в USD цена не
			// резолвится), и слот с полностью посчитанной геометрией получал бы «нормы нет» —
			// неверное предписание оператору при неизменной ткани. Пустая база поэтому здесь
			// безвредна ПО ПОСТРОЕНИЮ, а не по совпадению: цена в этот ответ не входит.
			reason := entity.MaterialPlanReasonNoNorm
			if cw := colorwayByProduct[pid]; cw != nil && card != nil {
				if slotAreaEstimate(card, cw, b, linked, card.CostingBasis(), "").normDerived {
					reason = entity.MaterialPlanReasonEstimateOnly
				}
			}
			blockAdd(b.Id, pid, qty, entity.MaterialPlanBlockerNoNorm, reason)
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
	// on the field being broken. Say so once per article rather than letting the dial look dead —
	// and say WHAT shut it out, because a wrong explanation of a right number sends people to fix
	// the wrong thing.
	//
	// С W3 ЭТОТ КАВЕАТ СТАЛ РЕДКИМ, И ЭТО ЕГО ГЛАВНОЕ ИЗМЕНЕНИЕ. Раньше он срабатывал на КАЖДОМ
	// артикуле с ручной или dxf-нормой — то есть в самом обычном случае, — потому что коэффициент
	// кусал только marker-строки. Теперь он кусает любую мерную роликовую норму, и остаться без
	// него можно ровно тремя способами: требование посчитано от НАСТИЛОВ (это измерение, а не
	// норма), артикул расходуется СЧЁТНО, или он стоит на НЕРУЛОННОЙ секции.
	for _, mid := range order {
		a := req[mid]
		if !a.coefficient.Valid || !a.required.IsPositive() {
			continue
		}
		// Собирается из ПРИСУТСТВУЮЩИХ причин, а не перечисляется сочетаниями: рукописный набор
		// арок — ровно тот код, где через одну ветку начинают говорить «ваши нормы ручные» про
		// счётную фурнитуру. Порядок (настилы → счётные → нерулонные) фиксирован.
		// НАСТИЛОВ В ЭТОМ ПЕРЕЧНЕ БОЛЬШЕ НЕТ (W3): коэффициент ложится и на настильное требование —
		// после геометрии, поверх измерения, — потому что усадка и пороки случаются с полотном
		// независимо от того, чем посчитали требование. Осталось ровно две причины не укусить, и обе
		// про природу самого расхода, а не про способ его подсчёта.
		reasons := make([]string, 0, 2)
		if a.hasCountedNorms {
			reasons = append(reasons, "the counted quantities take no gross-up at all — 4 buttons stay 4 buttons")
		}
		if a.hasNonRollNorms {
			reasons = append(reasons, "the non-roll-goods slots have no roll shrinkage or flaws to pay for")
		}
		if len(reasons) == 0 {
			continue // взяло всё, что могло взять — объяснять нечего
		}
		// ЧАСТИЧНОЕ ПРИМЕНЕНИЕ НАЗЫВАЕТСЯ ЧАСТИЧНЫМ. Строка артикула может собрать И настил, И
		// норму (Ф4.6 MIXED): с W3 норма коэффициент берёт, настил — нет, и обе фразы неверны
		// поодиночке. «Не применён» о строке, где он применён к половине, отправил бы чинить
		// работающее поле; молчание о той половине, где он не применён, скрыло бы, почему число
		// меньше ожидаемого. Prefix несёт «вся или часть», reasons — «что именно не взяло».
		if a.coefficientApplied {
			caveats = append(caveats, fmt.Sprintf(
				"%s: cutting coefficient %s applied to PART of this row only — %s",
				a.name, a.coefficient.Decimal.String(), strings.Join(reasons, "; and ")))
			continue
		}
		caveats = append(caveats, fmt.Sprintf(
			"%s: cutting coefficient %s not applied — it grosses up every MEASURED norm of a roll-goods slot, and here %s",
			a.name, a.coefficient.Decimal.String(), strings.Join(reasons, "; and ")))
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
			// Ф4.6 — the signature. A number nobody can say the provenance of is the thing this whole
			// phase walks away from, so it rides on the ROW and on every CONTRIBUTION, not only on the
			// response as a whole.
			Source: planRowSource(a.hasNormSource, a.hasLaySource),
		})
	}

	// Contributions and blockers in BOM display order, then colourway id — stable and mirroring
	// the spec sheet top-to-bottom.
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
		slotName := ""
		unit := c.unit
		section := pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN
		if bom != nil {
			slotName = bom.Name
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
			// Ф4.6. Never MIXED here by construction — a contribution is ONE slot of ONE colourway,
			// which is exactly the unit §7.3 switches. The two lengths are zero on the norm path and
			// are stated as zeros rather than left absent, because the proto says they ARE zero there.
			Source:           c.source,
			LayClothLengthCm: pbDecimalFromDecimal(c.layClothCm.Round(3)),
			LayEndLossCm:     pbDecimalFromDecimal(c.layEndLossCm.Round(3)),
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

	// plan_source is the WHOLE ANSWER's signature, and it is folded from the CONTRIBUTIONS — the only
	// places a source is a fact rather than an aggregate. LAYS when every counted pair went to
	// настилы, NORM when none did, MIXED in between. It is a field and not something the client
	// infers from the rows, because the client writes the table heading with it and may not be wrong.
	//
	// No contributions at all ⇒ NORM: nothing was counted, and «нормой» is what an empty plan has
	// always been. The blockers say why it is empty.
	planLays, planNorm := 0, 0
	for _, c := range contribs {
		if c.source == pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS {
			planLays++
			continue
		}
		planNorm++
	}
	out.PlanSource = planRowSource(planNorm > 0, planLays > 0)

	out.Caveats = caveats
	return out
}

// planRowSource folds «this number was fed by norms» and «this number was fed by настилы» into the
// wire's signature. Both ⇒ MIXED, and MIXED is emitted rather than one of the two being picked: an
// article shared by a laid slot and an unlaid one is genuinely both, and silently choosing would lie
// in the very field that exists to stop the number being guessed at.
//
// Neither ⇒ UNSPECIFIED, which is the honest answer for the issued-but-not-required row: it has no
// requirement at all, so there is no source that produced one.
func planRowSource(hasNorm, hasLays bool) pb_admin.ProductionRunCoverageSource {
	switch {
	case hasNorm && hasLays:
		return pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED
	case hasLays:
		return pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS
	case hasNorm:
		return pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM
	}
	return pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_UNSPECIFIED
}

// --- Ф4.6: НАСТИЛ → ДЛИНА ------------------------------------------------------------------------

// planCmPerMetre converts the настил's centimetres into the metres the slot and the article speak.
// Здесь жил довод «это НЕ делитель процентов, хоть и та же сотня»; сам делитель уехал в
// entity.applyWastage вместе с арифметикой гросс-апа (W3), и довод остаётся в силе на будущее:
// сотня в переводе сантиметров и сотня в проценте — разные сотни, и общей константы у них быть
// не должно.
var planCmPerMetre = decimal.NewFromInt(100)

// planLayEndsPerPly is the 2 in `2 × end_loss_cm × Σ слоёв`: every ply is trimmed at BOTH ends.
//
// THE DEFINITION IS PINNED BY ARITHMETIC, not by prose (§7.2). The model gave two calibrations for
// «2–5 см на конец каждого слоя» and they are mutually inconsistent: 20 слоёв × 3 м ⇒ ~1.3% works out
// exactly (20 × 2 × 2 см = 80 см on 6000 см = 1.33%), while 1 слой × 30 м ⇒ 0.02% does not (2 × 2 см
// = 4 см on 3000 см = 0.13%). The first carries the whole point of the paragraph — short markers laid
// deep are punished — so it wins and the second is read as a typo. The column comment in migration
// 0281 says the same thing, and this constant is the executable half of it.
var planLayEndsPerPly = decimal.NewFromInt(2)

// planLayPairKey is the pair a настил belongs to, and it is the UNIT OF THE SWITCH (§7.3): ONE
// colourway × ONE BOM slot. Never the run. A run whose основная ткань is laid and whose подкладка is
// not must keep counting the подкладка by its norm; an all-or-nothing switch would zero it, which is
// the same lie as the norm, only pointing the other way.
type planLayPairKey struct {
	colorwayID int
	bomItemID  int
}

// planLayDemand is Σ over the настилы of ONE pair, in CENTIMETRES, kept as the two physically
// distinct facts the wire carries separately: cloth under the раскладки, and cloth lost at the two
// ends of every ply.
type planLayDemand struct {
	clothCm   decimal.Decimal
	endLossCm decimal.Decimal
	// names are the настилы that fed this pair, for the sentences that have to name them.
	names []string
	// unmeasured are the ready-made caveats for sections whose раскладка has no usable length. They
	// travel with the demand instead of being logged: a section that could not be measured makes the
	// pair's number SMALLER, and a requirement that quietly shrank is the one failure mode this whole
	// phase is built against.
	unmeasured []string
}

func (d *planLayDemand) totalCm() decimal.Decimal { return d.clothCm.Add(d.endLossCm) }

func (d *planLayDemand) label() string {
	if len(d.names) == 0 {
		return "(unnamed)"
	}
	return strings.Join(d.names, ", ")
}

// planLayDemandByPair is §7.1 for a whole run: every настил distilled into the length of cloth its
// pair needs.
//
//	cloth_length_cm(L)   = Σ по секциям (marker.used_length_cm × plies)
//	end_loss_total_cm(L) = 2 × end_loss_cm × Σ по секциям plies
//	planned_length_cm(L) = cloth_length_cm + end_loss_total_cm
//	raw(C,B)             = Σ по настилам пары planned_length_cm
//
// A НАСТИЛ THAT LOST ITS SLOT IS NEVER SILENT. fk_prlay_bom is SET NULL (0281), so a BOM edit can
// leave a настил pointing at nothing; such a lay drops out of the requirement WITH A FINDING that
// names the slot it lost through the bom_line_key snapshot the row keeps exactly for this. Counting
// it against some other slot would be worse than dropping it, and dropping it quietly would remove
// cloth from the plan that the cutting room is still going to lay.
//
// Returns the caveats rather than taking a sink so the function stays pure and directly testable.
func planLayDemandByPair(lays []entity.ProductionRunLay) (map[planLayPairKey]*planLayDemand, []string) {
	out := make(map[planLayPairKey]*planLayDemand, len(lays))
	var notes []string
	for i := range lays {
		l := &lays[i]
		name := strings.TrimSpace(l.Name)
		if name == "" {
			name = l.LayKey
		}
		if l.Broken() {
			notes = append(notes, fmt.Sprintf(
				"lay %q lost its BOM slot (slot key snapshot %q) — its metreage did not enter any article's requirement",
				name, l.BomLineKey))
			continue
		}
		k := planLayPairKey{colorwayID: l.ColorwayId, bomItemID: int(l.BomItemId.Int64)}
		d := out[k]
		if d == nil {
			d = &planLayDemand{}
			out[k] = d
		}
		d.names = append(d.names, fmt.Sprintf("%q", name))
		g := LayPlannedGeometryOf(l)
		for _, idx := range g.UnmeasuredSections {
			// A раскладка with no measured length cannot say how much cloth its section eats. The
			// section is skipped and NAMED: the alternative is a requirement that is quietly too
			// small, which reads as «ткани хватает» in the one place that must never be optimistic.
			s := l.Sections[idx]
			d.unmeasured = append(d.unmeasured, fmt.Sprintf(
				"lay %q: marker %q (#%d) has no marker length set — %d plies of this section did not enter the requirement",
				name, s.MarkerName, s.MarkerId, s.Plies))
		}
		d.clothCm = d.clothCm.Add(g.ClothCm)
		d.endLossCm = d.endLossCm.Add(g.EndLossCm)
	}
	return out, notes
}

// LayPlannedGeometry is ONE настил's planned cloth, kept as the two physically distinct facts the
// wire carries separately: cloth under the раскладки, and cloth lost at the two ends of every ply.
type LayPlannedGeometry struct {
	ClothCm   decimal.Decimal
	EndLossCm decimal.Decimal
	// UnmeasuredSections are INDICES into the lay's Sections whose раскладка has no usable length —
	// плies that are physically laid but cannot be turned into centimetres. Indices and not ready
	// sentences, because the two callers name the same fact differently: the material plan says «не
	// вошли в потребность», the calibration says «план настила неполон». The arithmetic is shared;
	// the wording is not.
	UnmeasuredSections []int
}

// TotalCm is planned_length_cm(L) — what a настил's plan actually claims.
func (g LayPlannedGeometry) TotalCm() decimal.Decimal { return g.ClothCm.Add(g.EndLossCm) }

// LayPlannedGeometryOf is §7.1 for ONE настил, and it is the ONLY definition of that sum:
//
//	cloth_length_cm(L)   = Σ по секциям (marker.used_length_cm × plies)
//	end_loss_total_cm(L) = 2 × end_loss_cm × Σ по секциям plies
//	planned_length_cm(L) = cloth_length_cm + end_loss_total_cm
//
// ОДНО ОПРЕДЕЛЕНИЕ НА ДВУХ ЧИТАТЕЛЕЙ, и это не вкус. Потребность прогона (planLayDemandByPair) и
// калибровка коэффициента (Ф5б.3) делят одно и то же число: первая складывает его по парам, вторая
// делит на него факт. Две копии этой суммы разошлись бы при первой же правке — и разошлись бы
// МОЛЧА, потому что обе продолжали бы выдавать правдоподобные сантиметры, а расхождение проявилось
// бы как «дрейф −4%» на артикуле, у которого всё в порядке.
//
// КОЭФФИЦИЕНТ РАСКРОЯ ЗДЕСЬ НЕ ПРИМЕНЯЕТСЯ (решение Р4 фазы Ф4), и это load-bearing: план настила
// обязан остаться ЧИСТОЙ ГЕОМЕТРИЕЙ, иначе калибровка становится круговой — коэффициент правил бы
// план, план правил бы коэффициент. Тот, кто соберётся «для единообразия» домножить здесь на
// material.cutting_coefficient, сломает не потребность, а калибровку, и сломает её молча.
func LayPlannedGeometryOf(l *entity.ProductionRunLay) LayPlannedGeometry {
	out := LayPlannedGeometry{ClothCm: decimal.Zero, EndLossCm: decimal.Zero}
	if l == nil {
		return out
	}
	for i := range l.Sections {
		s := &l.Sections[i]
		if !s.MarkerUsedLengthCm.Valid || !s.MarkerUsedLengthCm.Decimal.IsPositive() {
			out.UnmeasuredSections = append(out.UnmeasuredSections, i)
			continue
		}
		out.ClothCm = out.ClothCm.Add(s.MarkerUsedLengthCm.Decimal.Mul(decimal.NewFromInt(int64(s.Plies))))
	}
	// End losses ride on Σ plies of the WHOLE lay, including the sections whose length is unknown:
	// those plies are physically laid and physically trimmed at both ends whatever the раскладка's
	// records say. Ступенчатый настил is counted by SECTION plies, i.e. conservatively — a ply
	// crossing two sections has two ends and not four, but the difference is centimetres and
	// understating a loss costs more than overstating it (§7.2).
	out.EndLossCm = planLayEndsPerPly.Mul(l.EndLossCm).Mul(decimal.NewFromInt(int64(l.TotalPlies())))
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
//
// СЧЁТНОЕ ЧИСЛО ЧИТАЕТСЯ ЧЕРЕЗ ТОТ ЖЕ РЕЗОЛВЕР ПАРЫ, ЧТО И ДЕНЬГИ (0333), и это не симметрия ради
// симметрии. Потребность и себестоимость обязаны читать ОДНО определение — расхождение здесь
// означает, что цех получает не то количество, которое оплачено (класс дефекта описан дословно в
// шапке entity.NormGrossUp). Не сделав эту правку, получили бы цех, которому выдали 6 пуговиц там,
// где нужно 7: ошибка линейна по запасу и молчалива.
//
// pair — все строки пары (колорвей × слот), построенные из ТОГО ЖЕ среза, по которому идёт
// вызывающий: итог слота и запас лежат на первой строке пары, остальные её строки вносят 0.
//
// ПОРЯДОК ВЕТВЕЙ. Счётная ветка стоит ПЕРЕД мерной ТОЛЬКО на счётной секции — там она и есть
// определение нормы, и там же костинг читает её первой (LineTotal), так что оба пути видят одно
// число. На МЕРНОЙ секции порядок остался прежним (расход, потом легаси-quantity): историческое
// расхождение с костингом, который на мерной строке с обоими заполненными полями берёт quantity, —
// ДОСУЩЕСТВУЮЩЕЕ, и менять его этой волной значило бы молча передвинуть числа на строках, которых
// счётная норма не касается вовсе.
func usageNormForSize(u *entity.TechCardColorwayUsage, sizeID int, pair []*entity.TechCardColorwayUsage, bom *entity.TechCardBomItem) (norm decimal.Decimal, sizeGraded, counted, ok bool) {
	for _, sc := range u.SizeConsumptions {
		if sc.SizeId == sizeID {
			return sc.Consumption, true, false, true
		}
	}
	// СЧЁТНАЯ ВЕТВЬ ВКЛЮЧАЕТСЯ, ТОЛЬКО КОГДА СЛОТ ДЕЙСТВИТЕЛЬНО НЕСЁТ СЧЁТНУЮ НОРМУ. Проверять
	// одну лишь валидность ответа резолвера здесь НЕЛЬЗЯ: на пустом слоте он честно возвращает
	// собственное quantity строки, и по одному NullDecimal «резолвер сказал» неотличимо от
	// «резолвер вернул то, что было». А ветвь эта стоит ПЕРЕД Consumption — то есть на строке,
	// несущей ОБА числа (состояние легальное и в истории встречается), включённая впустую она
	// молча превращала 0.5 расхода в 6 штук, вшестеро завышая требование цеха на данных, которых
	// волна не касается. В деньгах этого видно не было: там quantity и до 0333 стоял впереди.
	if bom != nil && entity.IsCountableSection(bom.Section) && entity.SlotCarriesCountableNorm(bom) {
		if q := entity.CountablePairRowTotal(pair, u, bom); q.Valid {
			return q.Decimal, false, true, true
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
