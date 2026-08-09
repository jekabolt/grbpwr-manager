package dto

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"

	"github.com/shopspring/decimal"
)

// ГЕЙТ ГОТОВНОСТИ ПРОГОНА (Ф6) — the assembly. The JUDGEMENTS live in entity/run_readiness.go; what
// is here is the walk over a loaded card and the mapping onto the wire.
//
// THREE INVARIANTS GOVERN HOW THE LIST IS BUILT, and they are the reason this file is a single
// straight-line function rather than a set of independent checkers:
//
//  1. A ROW IS EMITTED ONLY IF ITS SUBJECT EXISTS. When a cloth has no norm at all
//     (norm_provenance = BLOCKER), the five rows that judge a norm's conditions are NOT emitted for
//     that slot, and norm_provenance's own detail says so. Otherwise one fact goes red five times
//     and reads as five problems — the same discipline the advisory checklist already keeps when it
//     leaves `lab_dip` vacuously met on a card with no colourways.
//  2. EVERY ROW CARRIES A TARGET. Without identifiers the client has nothing to build a URL from,
//     and «the link goes there» stops being true.
//  3. ONE FACT IS COMPUTED BY ONE PIECE OF CODE. slot_article/slot_norm are READ BACK from
//     ComputeProductionRunMaterialPlan rather than recomputed; направление comes from Ф1's
//     ScopeFabricDirection; the fabric scope from entity.ResolveFabricScope; the norm from Ф3's
//     SelectNorm. Three copies of a rule drift, and the day they drift the divergence is silent.

// RunReadinessInput is everything the gate reads. It is a struct of LOADED FACTS rather than a
// repository handle on purpose: the whole verdict is then a pure function, testable without a
// database, and re-usable by the create-time re-check without a second code path.
type RunReadinessInput struct {
	// Card must be the ENRICHED single-card read: the gate needs BomItems, Colorways[].Usages,
	// Pieces, Patterns, PieceDxfAliases, Markers (with CardPieceSetFp stamped) and LinkedMaterials.
	Card *entity.TechCard
	// ReleaseId as the request sent it. 0 = «planning against the live card», a legal state.
	ReleaseId int
	// Release is the resolved release when ReleaseId > 0 and it exists; nil otherwise. The pair
	// (ReleaseId, Release) is what tells «not asked for» apart from «asked for and missing».
	Release *entity.TechCardReleaseMeta
	// ColorwayIds are the colourways asked about — NOT derived from Cells, because the modal must be
	// able to show a verdict for a colourway whose quantities have not been typed yet.
	ColorwayIds []int
	Cells       []entity.RunReadinessCell
	Settings    *entity.WorkshopSettings
	OnHand      map[int]decimal.Decimal
	Issued      map[int]decimal.Decimal
	// NarrowestMeasuredLotCm is material id → the narrowest MEASURED ROLL width among lots that
	// still have stock (Ф5а.1). Raw, i.e. кромка NOT yet subtracted — the subtraction is the width
	// rule's job (entity.NormWidthVsArticle) and doing it in the loader would hide it.
	NarrowestMeasuredLotCm map[int]decimal.NullDecimal
	// PatternSizeIndex is scope_key → the stored Ф6.3 row. A missing key is «nobody ran the audit».
	PatternSizeIndex map[string]entity.PatternSizeIndexRow
	// ActualWastagePercent of an EXISTING run being re-checked; invalid for a future run, where the
	// operator has not entered it yet. It changes the metreage, never a verdict — which is why the
	// modal's coverage is labelled «оценка по норме» and systematically differs from the same run's
	// figure a minute after creation.
	ActualWastagePercent decimal.NullDecimal
}

// RunReadinessResult is the verdict plus the two coverage tables. The report is the part the create
// gate acts on; the coverage is display only and no rule reads it.
type RunReadinessResult struct {
	Report       entity.RunReadinessReport
	Coverage     []*pb_admin.ProductionRunReadinessCoverage
	UnitCoverage []*pb_admin.ProductionRunReadinessUnitCoverage
}

// ComputeProductionRunReadiness judges a run that does not exist yet.
func ComputeProductionRunReadiness(in RunReadinessInput) RunReadinessResult {
	res := RunReadinessResult{}
	res.Report.BlockingEnabled = entity.RunReadinessBlocking(in.Settings)
	card := in.Card
	if card == nil {
		return res
	}

	// AUX SHORT CIRCUIT, AND IT IS ON THE SERVER DELIBERATELY.
	//
	// An auxiliary card produces a MATERIAL (a dust bag, a shopper) and receipts into the warehouse;
	// it has no colourways by construction and the cloth-norm vocabulary does not apply to it. The
	// filter has to be here and not in the modal because the create-time re-check is also here: a
	// client-side filter would leave aux runs blockable THROUGH THE API, which is the one thing the
	// acceptance probe explicitly tests.
	if card.Purpose == entity.TechCardPurposeAuxiliary {
		res.Report.Card = []entity.RunReadinessFinding{{
			Key:      entity.RunReadinessKeyCardAuxiliary,
			Severity: entity.RunReadinessOK,
			Label:    "вспомогательная карточка — гейт норм ткани к ней не применяется",
			Target:   entity.RunReadinessTarget{TechCardId: card.Id},
		}}
		return res
	}

	b := &runReadinessBuilder{in: in, card: card}
	b.cardChecks()
	plan := b.materialPlan()
	b.colorwayChecks(plan)
	b.runChecks(plan)
	res.Report.Card = b.cardRows
	res.Report.Colorways = b.colorways
	res.Report.Run = b.runRows
	res.Report.Caveats = plan.GetCaveats()
	res.Coverage = b.coverage(plan)
	res.UnitCoverage = b.unitCoverage()
	return res
}

type runReadinessBuilder struct {
	in   RunReadinessInput
	card *entity.TechCard

	cardRows   []entity.RunReadinessFinding
	colorways  []entity.RunReadinessColorway
	runRows    []entity.RunReadinessFinding
	rollGoods  []entity.RollGoodsLine
	dirLines   []entity.FabricDirectionLine
	normPeers  []entity.NormPeer
	markerByID map[int]*entity.TechCardMarkerSummary
}

func (b *runReadinessBuilder) prepare() {
	if b.markerByID != nil {
		return
	}
	b.rollGoods = entity.RollGoodsLinesOfBom(b.card.BomItems)
	b.dirLines = entity.FabricDirectionLinesOfBom(b.card.BomItems)
	b.normPeers = entity.NormPeersOf(b.card.Markers)
	b.markerByID = make(map[int]*entity.TechCardMarkerSummary, len(b.card.Markers))
	for i := range b.card.Markers {
		b.markerByID[b.card.Markers[i].Id] = &b.card.Markers[i]
	}
}

func (b *runReadinessBuilder) target() entity.RunReadinessTarget {
	return entity.RunReadinessTarget{TechCardId: b.card.Id}
}

// --- КАРТОЧКА -----------------------------------------------------------------------------------

func (b *runReadinessBuilder) cardChecks() {
	b.prepare()
	card := b.card
	add := func(f entity.RunReadinessFinding) { b.cardRows = append(b.cardRows, f) }

	// release_frozen — WARNING when planning against the live card, and the WARNING must name the
	// PRICE rather than the fact. A yellow row that states a condition without a consequence teaches
	// an operator to stop reading yellow rows, and the consequence here is concrete: no frozen
	// snapshot means the planned unit cost is whatever the card says at the moment of creation.
	switch {
	case b.in.ReleaseId <= 0:
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyReleaseFrozen, Severity: entity.RunReadinessWarning,
			Label:  "прогон планируется от замороженного релиза",
			Detail: "релиз не выбран — прогон планируется от ЖИВОЙ карточки: норма не заморожена, и плановая цена прогона снимется с карточки такой, какой она окажется в момент создания. Пересъёмка нормы или правка рецепта до этого момента молча изменит плановую себестоимость этого прогона",
			Target: b.target(),
		})
	case b.in.Release == nil:
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyReleaseFrozen, Severity: entity.RunReadinessBlocker,
			Label:  "прогон планируется от замороженного релиза",
			Detail: fmt.Sprintf("релиза #%d не существует", b.in.ReleaseId),
			Target: b.target(),
		})
	case b.in.Release.TechCardId != card.Id:
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyReleaseFrozen, Severity: entity.RunReadinessBlocker,
			Label: "прогон планируется от замороженного релиза",
			Detail: fmt.Sprintf("релиз #%d принадлежит карточке #%d, а не этой",
				b.in.ReleaseId, b.in.Release.TechCardId),
			Target: b.target(),
		})
	default:
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyReleaseFrozen, Severity: entity.RunReadinessOK,
			Label:  "прогон планируется от замороженного релиза",
			Target: b.target(),
		})
	}

	// card_size_range
	if len(card.SizeIds) > 0 {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyCardSizeRange, Severity: entity.RunReadinessOK,
			Label: "у карточки есть размерный ряд", Target: b.target(),
		})
	} else {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyCardSizeRange, Severity: entity.RunReadinessBlocker,
			Label: "у карточки есть размерный ряд", Detail: "размерный ряд пуст — планировать нечего",
			Target: b.target(),
		})
	}

	// card_pieces
	if len(card.Pieces) > 0 {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyCardPieces, Severity: entity.RunReadinessOK,
			Label: "на карточке заведены детали кроя", Target: b.target(),
		})
	} else {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyCardPieces, Severity: entity.RunReadinessBlocker,
			Label:  "на карточке заведены детали кроя",
			Detail: "ни одной детали кроя — раскроить изделие не по чему",
			Target: b.target(),
		})
	}

	// card_pieces_dxf_matched — a piece with no DXF block alias cannot be recognised in a раскладка.
	// WARNING, not BLOCKER: aliases are what makes a saved layout survive a piece rename, and a card
	// can still be cut from a раскладка whose blocks nobody mapped.
	aliased := make(map[string]bool, len(card.PieceDxfAliases))
	aliasedByID := make(map[int]bool, len(card.PieceDxfAliases))
	for _, a := range card.PieceDxfAliases {
		if k := strings.TrimSpace(a.PieceLineKey); k != "" {
			aliased[strings.ToUpper(k)] = true
		}
		if a.PieceId > 0 {
			aliasedByID[a.PieceId] = true
		}
	}
	var unaliased []string
	for i := range card.Pieces {
		p := &card.Pieces[i]
		if aliased[strings.ToUpper(strings.TrimSpace(p.LineKey))] || aliasedByID[p.Id] {
			continue
		}
		unaliased = append(unaliased, pieceLabel(p))
	}
	if len(unaliased) == 0 {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyCardPiecesDxfMatched, Severity: entity.RunReadinessOK,
			Label: "каждая деталь кроя сопоставлена с блоком DXF", Target: b.target(),
		})
	} else {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyCardPiecesDxfMatched, Severity: entity.RunReadinessWarning,
			Label: "каждая деталь кроя сопоставлена с блоком DXF",
			Detail: fmt.Sprintf("без блока DXF: %s (%d из %d) — раскладка не узнает эти детали в файле",
				strings.Join(unaliased, ", "), len(unaliased), len(card.Pieces)),
			Target: b.target(),
		})
	}

	// pattern_binding_resolved — WARNING, and the degree is not a judgement call: fabric_scope.go
	// already declares a dangling binding to be a UI state («слот удалён»), and the store refuses one
	// only at the moment it is first written. Promoting it here would overrule a decision the
	// neighbouring layer has taken.
	var dangling []string
	for i := range card.Patterns {
		p := &card.Patterns[i]
		if entity.ResolveFabricScope(p.FabricPurpose.String, p.BomLineKey.String, b.rollGoods).Live() {
			continue
		}
		dangling = append(dangling, fmt.Sprintf("лист %q", patternLabel(p)))
	}
	for _, a := range card.PieceDxfAliases {
		if a.Scope(b.rollGoods).Live() {
			continue
		}
		dangling = append(dangling, fmt.Sprintf("алиас %q", a.BlockName))
	}
	if len(dangling) == 0 {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyPatternBindingResolved, Severity: entity.RunReadinessOK,
			Label: "каждый лист выкройки и алиас привязан к живой рулонной строке", Target: b.target(),
		})
	} else {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyPatternBindingResolved, Severity: entity.RunReadinessWarning,
			Label: "каждый лист выкройки и алиас привязан к живой рулонной строке",
			Detail: fmt.Sprintf("привязка висит в пустоте у %d: %s — строку BOM удалили или увели из рулонных",
				len(dangling), strings.Join(dangling, ", ")),
			Target: b.target(),
		})
	}
}

func pieceLabel(p *entity.TechCardPiece) string {
	if n := strings.TrimSpace(p.Name); n != "" {
		return n
	}
	return p.LineKey
}

func patternLabel(p *entity.TechCardSizePattern) string {
	if n := strings.TrimSpace(p.Name.String); n != "" {
		return n
	}
	if f := strings.TrimSpace(p.Filename.String); f != "" {
		return f
	}
	return p.LineKey
}

// --- МАТЕРИАЛ-ПЛАН НАД СИНТЕТИЧЕСКИМ ПРОГОНОМ ---------------------------------------------------

// materialPlan runs the EXISTING plan engine over a run built out of the request.
//
// The plan already resolves the article (pin, else slot default), already picks the per-size norm,
// already reports the two blockers the gate needs, and already knows not to gross a marker-sourced
// norm twice. Recomputing any of that here would be a second definition of the same facts, and the
// two would drift silently — the plan's own header is the argument.
func (b *runReadinessBuilder) materialPlan() *pb_admin.GetProductionRunMaterialPlanResponse {
	run := &entity.ProductionRun{
		ProductionRunInsert: entity.ProductionRunInsert{
			TechCardId: b.card.Id,
			// ActualWastagePercent is INVALID for a future run — the operator enters it later, on the
			// detail page — so the metreage here is the BOM's own estimate. That is why the modal's
			// heading says «оценка по норме» and why the same run's figure differs a minute after it
			// is created. Not a defect; a labelled difference.
			ActualWastagePercent: b.in.ActualWastagePercent,
			Lines:                runReadinessLines(b.in.Cells),
		},
	}
	// NO НАСТИЛЫ HERE, AND THAT IS NOT AN OMISSION (Ф4.6 §3.5). The run this gate judges is
	// SYNTHETIC — assembled out of the request, with no id — so there is no lay plan that could
	// belong to it: настилы are built afterwards, on the detail page. The источник is therefore NORM
	// always, which is exactly what the modal's heading «оценка по норме» claims. Switching an
	// existing run's gate onto its настилы needs `optional production_run_id` on the REQUEST, and
	// that is Ф6's edit, not a nil this file may quietly fill in.
	return ComputeProductionRunMaterialPlan(run, b.card, b.in.OnHand, b.in.Issued, b.card.LinkedMaterials, nil)
}

// runReadinessLines turns the request grid into plan lines. Zero and negative quantities are kept
// out: the plan skips them anyway, and a line carrying 0 would make «this colourway is planned»
// true for a colourway nobody is producing.
func runReadinessLines(cells []entity.RunReadinessCell) []entity.ProductionRunLine {
	out := make([]entity.ProductionRunLine, 0, len(cells))
	for _, c := range entity.SortedCells(cells) {
		if c.PlannedQty <= 0 {
			continue
		}
		ln := entity.ProductionRunLine{SizeId: c.SizeId, PlannedQty: c.PlannedQty}
		if c.ColorwayId > 0 {
			ln.ProductId = sql.NullInt32{Int32: int32(c.ColorwayId), Valid: true}
		}
		out = append(out, ln)
	}
	return out
}

// --- КОЛОРВЕЙ -----------------------------------------------------------------------------------

func (b *runReadinessBuilder) colorwayChecks(plan *pb_admin.GetProductionRunMaterialPlanResponse) {
	b.prepare()
	byProduct := make(map[int]*entity.TechCardColorway, len(b.card.Colorways))
	for i := range b.card.Colorways {
		cw := &b.card.Colorways[i]
		if cw.ProductId.Valid {
			byProduct[int(cw.ProductId.Int32)] = cw
		}
	}
	// Blockers of the plan, grouped by colourway and by key, so slot_article/slot_norm are read back
	// rather than recomputed.
	type slotBlock struct {
		bomID int64
		slot  string
	}
	blocked := map[int]map[string][]slotBlock{}
	for _, bl := range plan.GetBlockers() {
		cid := int(bl.GetColorwayId())
		if blocked[cid] == nil {
			blocked[cid] = map[string][]slotBlock{}
		}
		key := entity.RunReadinessKeySlotNorm
		if bl.GetKey() == entity.MaterialPlanBlockerNoArticle {
			key = entity.RunReadinessKeySlotArticle
		}
		blocked[cid][key] = append(blocked[cid][key], slotBlock{bomID: bl.GetBomItemId(), slot: bl.GetSlotName()})
	}

	for _, cid := range b.in.ColorwayIds {
		cw := byProduct[cid]
		out := entity.RunReadinessColorway{ColorwayId: cid}
		if cw != nil {
			out.Name = cw.Name
		}
		tgt := entity.RunReadinessTarget{TechCardId: b.card.Id, ColorwayId: cid}
		add := func(f entity.RunReadinessFinding) { out.Findings = append(out.Findings, f) }

		// colorway_live. An ARCHIVED colourway has no product id at all on the enriched read
		// (LinkedProductIDs' contract), so «somebody else's» and «archived» arrive here as the same
		// miss — hence one row whose detail names both possibilities rather than a confident guess.
		// Nothing on the plan path checks this today: validateRunLineVariants only looks at aux
		// colours, so a run planned against a foreign product id is accepted by the store.
		if cw == nil {
			add(entity.RunReadinessFinding{
				Key: entity.RunReadinessKeyColorwayLive, Severity: entity.RunReadinessBlocker,
				Label:  "колорвей принадлежит этой карточке и не архивирован",
				Detail: fmt.Sprintf("продукта #%d нет среди живых колорвеев этой карточки — он либо чужой, либо архивирован", cid),
				Target: tgt,
			})
			// Everything below is about THIS card's recipe, and there is none for a product that is
			// not this card's colourway. Emitting the rest would print a dozen «нет нормы» rows about
			// a colourway that does not exist here (invariant 1).
			b.colorways = append(b.colorways, out)
			continue
		}
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyColorwayLive, Severity: entity.RunReadinessOK,
			Label: "колорвей принадлежит этой карточке и не архивирован", Target: tgt,
		})

		// slot_article / slot_norm — ONE row per key per colourway, listing every offending slot in
		// the detail. One row per slot would red the same fact once per slot and make a
		// three-fabric card read as three problems; the target points at the first offender so the
		// link still lands somewhere concrete.
		for _, spec := range []struct {
			key   string
			label string
			lead  string
		}{
			{entity.RunReadinessKeySlotArticle, "у каждого обязательного слота есть артикул", "без артикула (ни пина колорвея, ни дефолта слота)"},
			{entity.RunReadinessKeySlotNorm, "у каждого обязательного слота есть норма расхода на каждый планируемый размер", "без нормы расхода"},
		} {
			hits := blocked[cid][spec.key]
			if len(hits) == 0 {
				add(entity.RunReadinessFinding{
					Key: spec.key, Severity: entity.RunReadinessOK, Label: spec.label, Target: tgt,
				})
				continue
			}
			sort.SliceStable(hits, func(i, j int) bool { return hits[i].bomID < hits[j].bomID })
			names := make([]string, 0, len(hits))
			for _, h := range hits {
				names = append(names, h.slot)
			}
			t := tgt
			t.BomItemId = hits[0].bomID
			add(entity.RunReadinessFinding{
				Key: spec.key, Severity: entity.RunReadinessBlocker, Label: spec.label,
				Detail: fmt.Sprintf("%s: %s", spec.lead, strings.Join(names, ", ")),
				Target: t,
			})
		}

		b.normChecks(cw, tgt, &out)
		b.colorways = append(b.colorways, out)
	}
}

// normChecks emits the norm rows for every ROLL-GOODS slot of this colourway's recipe.
//
// Roll goods only, and that is narrower than the plan's own planSlotSections deliberately: the plan
// is about the RECIPE (a thread and a button have norms too), while these rows are about a
// РАСКЛАДКА, and thread has none to have. Letting a button through would put five nonsense rows on
// the screen and one of them could refuse a run.
func (b *runReadinessBuilder) normChecks(cw *entity.TechCardColorway, base entity.RunReadinessTarget, out *entity.RunReadinessColorway) {
	add := func(f entity.RunReadinessFinding) { out.Findings = append(out.Findings, f) }
	standard := entity.RequiredSeamAllowanceMm(b.card.RequiredSeamAllowanceMm, b.workshopSeamAllowance())

	for j := range cw.Usages {
		u := &cw.Usages[j]
		bom := planBomLine(u, b.card.BomItems)
		if bom == nil || !entity.IsRollGoodsSection(bom.Section) {
			continue
		}
		tgt := base
		tgt.BomItemId = int64(bom.Id)
		tgt.BomLineKey = bom.LineKey
		mid, _ := u.EffectiveMaterialId(bom)
		tgt.MaterialId = int64(mid)

		// norm_provenance decides whether the five condition rows below have a subject at all.
		//
		// A nil marker means there is no раскладка to judge — either the norm was entered by hand (a
		// legitimate escape hatch, WARNING) or there is none at all (BLOCKER). Invariant 1: the five
		// condition rows are then NOT emitted, and norm_provenance's own detail carries the fact.
		// Emitting them anyway would print one problem five times.
		marker := b.normProvenance(u, bom, tgt, add)
		if marker == nil {
			continue
		}
		tgt.MarkerId = marker.Id

		// norm_conditions_recorded — «старая норма». A BLOCKER, and this is the UNKNOWN/BLOCKER
		// boundary in its sharpest form: the absence is not a missing instrument, it IS the answer.
		// Fitness under «не хуже» cannot be established for a раскладка that states nothing about the
		// rules it was measured under, and a norm whose fitness cannot be established is not fit.
		if marker.IsLegacyNorm() {
			add(entity.RunReadinessFinding{
				Key: entity.RunReadinessKeyNormConditionsRecorded, Severity: entity.RunReadinessBlocker,
				Label: "у нормы записаны условия съёмки",
				Detail: fmt.Sprintf("раскладка %q снята до Ф3 и не говорит, по какому контуру и с каким припуском её мерили — «старая норма». Пересними её, чтобы норма стала проверяемой",
					marker.Name),
				Target: tgt,
			})
		} else {
			add(entity.RunReadinessFinding{
				Key: entity.RunReadinessKeyNormConditionsRecorded, Severity: entity.RunReadinessOK,
				Label: "у нормы записаны условия съёмки", Target: tgt,
			})
		}

		b.seamAllowanceRow(marker, standard, tgt, add)
		b.flipPolicyRow(marker, bom, tgt, add)
		b.pieceSetRow(marker, tgt, add)
		b.widthRow(marker, mid, tgt, add)
	}
}

func (b *runReadinessBuilder) workshopSeamAllowance() decimal.NullDecimal {
	if b.in.Settings == nil {
		return decimal.NullDecimal{}
	}
	return b.in.Settings.DefaultSeamAllowanceMm
}

// normProvenance emits norm_provenance (and norm_multiple when a norm exists) and returns the
// раскладка whose conditions the caller may then judge — nil when there is none to judge.
func (b *runReadinessBuilder) normProvenance(u *entity.TechCardColorwayUsage, bom *entity.TechCardBomItem,
	tgt entity.RunReadinessTarget, add func(entity.RunReadinessFinding)) *entity.TechCardMarkerSummary {
	const label = "норма расхода взята из нормировочной раскладки"

	// НОРМА С ВЫКРОЕК (0294) — свой ответ, а не «введена руками».
	//
	// Раскладки за ней нет и быть не может, поэтому пять условий съёмки ниже так же не имеют
	// предмета, как у ручной нормы, и строка остаётся WARNING. Но текст обязан отличаться: сказать
	// про netto с выкроек «введено руками» — это соврать оператору в единственном месте, где он
	// узнаёт, чему верить.
	//
	// А ВОТ ПАРА (dxf + wastage_percent NULL) — BLOCKER, и это не строгость ради строгости.
	// Площадь деталей не содержит межлекальных выпадов, кромки и концов настила; процент раскроя
	// слота — ЕДИНСТВЕННОЕ, что за них платит (applyWastage при NULL умножает на единицу). Пустой
	// процент здесь означает не «отходов нет», а «никто не сказал сколько», и потребность уходит в
	// закупку заведомо занижённой. Это тот же UNKNOWN/BLOCKER-порог, что у «старой нормы» ниже:
	// отсутствие не инструмента, а ОТВЕТА. Клиент не даёт применить выкройки на слот без процента —
	// эта строка ловит данные, пришедшие другой дорогой.
	if u.ConsumptionSource.String == entity.ConsumptionSourceDxf {
		if !bom.WastagePercent.Valid {
			add(entity.RunReadinessFinding{
				Key: entity.RunReadinessKeyNormProvenance, Severity: entity.RunReadinessBlocker,
				Label: label,
				Detail: fmt.Sprintf("расход по слоту %q снят с выкроек — это NETTO площадь деталей, поделённая на раскройную ширину, без межлекальных выпадов, кромки и концов настила. Процент раскроя у слота НЕ ЗАДАН, поэтому потребность и себестоимость идут по чистой площади и занижены. Задайте процент раскроя слота или снимите норму с раскладки",
					bom.Name),
				Target: tgt,
			})
			return nil
		}
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyNormProvenance, Severity: entity.RunReadinessWarning,
			Label: label,
			Detail: fmt.Sprintf("расход по слоту %q снят с выкроек (NETTO площадь деталей ÷ раскройная ширина), отходы доначисляет процент раскроя слота — %s%%. Гейт это принимает: выкройки есть раньше раскладки, и норма по ним проверяема, в отличие от набранной руками. Но условия съёмки проверить не по чему — раскладки за такой нормой нет, и коэффициент раскроя артикула её не трогает",
				bom.Name, bom.WastagePercent.Decimal.String()),
			Target: tgt,
		})
		return nil
	}

	// An absent consumption_source is 'manual': that is the column's DEFAULT (0261) and what every
	// row predating it carries. Reading absence as 'marker' would demand a норма-раскладка of every
	// legacy recipe on the card.
	if u.ConsumptionSource.String != entity.ConsumptionSourceMarker {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyNormProvenance, Severity: entity.RunReadinessWarning,
			Label: label,
			Detail: fmt.Sprintf("расход по слоту %q введён руками (consumption_source='manual'), а не снят с раскладки. Гейт это принимает — без ручного ввода первый же странный DXF останавливал бы производство — но проверить условия съёмки не по чему, и костинг помечает такую норму ручной",
				bom.Name),
			Target: tgt,
		})
		return nil
	}

	scope := entity.NormScope{BomItemId: int64(bom.Id), Bound: true}
	winner, contenders, ok := entity.SelectNorm(b.normPeers, scope)
	if !ok {
		detail := fmt.Sprintf("расход по слоту %q записан как снятый с раскладки, но ни одна раскладка этой ткани не отмечена нормой", bom.Name)
		// The orphaned-norm hint. fk_tcm_bom is ON DELETE SET NULL (0257), so deleting a BOM line
		// moves that line's norm into the «no cloth» scope instead of destroying it. Without this
		// sentence the operator cannot find their раскладка and re-designates a new one over the top.
		if orphan, _, orphaned := entity.SelectNorm(b.normPeers, entity.NormScope{}); orphaned {
			detail += fmt.Sprintf(". На карточке есть норма БЕЗ ТКАНИ — раскладка %q: похоже, слот BOM удаляли, и привязка обнулилась (ON DELETE SET NULL). Привяжите её к слоту заново", orphan.Name)
		}
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyNormProvenance, Severity: entity.RunReadinessBlocker,
			Label: label, Detail: detail, Target: tgt,
		})
		return nil
	}

	marker := b.markerByID[winner.Id]
	t := tgt
	t.MarkerId = winner.Id
	add(entity.RunReadinessFinding{
		Key: entity.RunReadinessKeyNormProvenance, Severity: entity.RunReadinessOK,
		Label: label, Target: t,
	})

	// norm_multiple — WARNING, never BLOCKER. The tiebreak (SelectNorm) is deterministic, so the run
	// is physically possible and the number is not random; it may simply not be the number a human
	// meant. REPORTING it is a direct condition of the Ф3 decision to hold exclusivity in a
	// transaction rather than a UNIQUE index: silently taking the first is forbidden, because the
	// only thing the missing index would otherwise have bought is that nobody ever finds out.
	if len(contenders) > 1 {
		names := make([]string, 0, len(contenders))
		for _, c := range contenders {
			names = append(names, fmt.Sprintf("#%d %q", c.Id, c.Name))
		}
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyNormMultiple, Severity: entity.RunReadinessWarning,
			Label: "на ткани отмечена ровно одна норма",
			Detail: fmt.Sprintf("норм отмечено %d: %s. Действует #%d %q (последняя изменённая) — выбор одинаков у всех читателей, но это может быть не та раскладка, которую имели в виду",
				len(contenders), strings.Join(names, ", "), winner.Id, winner.Name),
			Target: t,
		})
	} else {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyNormMultiple, Severity: entity.RunReadinessOK,
			Label: "на ткани отмечена ровно одна норма", Target: t,
		})
	}
	if marker == nil {
		// Unreachable: peers are built out of the same slice markerByID indexes. Fail closed rather
		// than judging conditions of a раскладка we cannot see.
		return nil
	}
	return marker
}

func (b *runReadinessBuilder) seamAllowanceRow(m *entity.TechCardMarkerSummary, standard decimal.NullDecimal,
	tgt entity.RunReadinessTarget, add func(entity.RunReadinessFinding)) {
	const label = "припуск нормы не меньше требуемого"
	a := m.Allowance()
	sev := entity.NormSeamAllowance(a, standard)
	detail := ""
	switch {
	case sev == entity.RunReadinessOK:
	case !a.Recorded:
		detail = "припуск съёмки не записан — сравнивать не с чем (см. строку об условиях съёмки)"
	case !standard.Valid:
		detail = "эталон припуска не задан ни на карточке, ни в цехе — вердикта нет. Ноль здесь ЗАКОННАЯ настройка («выкройки несут линию кроя»), поэтому подставить его вместо «не задано» нельзя"
	case sev == entity.RunReadinessBlocker:
		detail = fmt.Sprintf("на раскладке %s мм припуска, требуется %s мм", a.Mm.String(), standard.Decimal.String())
	default:
		detail = fmt.Sprintf("на раскладке не меньше %s мм припуска при требуемых %s мм, но контурная половина не замерена — известна только НИЖНЯЯ ГРАНИЦА, и она ниже эталона. Утверждать нарушение по нижней границе нельзя",
			a.Mm.String(), standard.Decimal.String())
	}
	add(entity.RunReadinessFinding{Key: entity.RunReadinessKeyNormSeamAllowance, Severity: sev,
		Label: label, Detail: detail, Target: tgt})
}

func (b *runReadinessBuilder) flipPolicyRow(m *entity.TechCardMarkerSummary, bom *entity.TechCardBomItem,
	tgt entity.RunReadinessTarget, add func(entity.RunReadinessFinding)) {
	const label = "политика переворота нормы не мягче, чем требует ткань"
	scope := entity.MarkerFabricScope(bom.LineKey, b.dirLines)
	dir, unknownLines, known := entity.ScopeFabricDirection(scope, b.dirLines)
	sev := entity.NormFlipPolicy(dir, known, m.AllowFlip)
	detail := ""
	switch {
	case sev == entity.RunReadinessOK:
	case !known:
		// ALL the lines, never the first: the fix is a mass fill (кампания Д1), and naming one row
		// per call would make a three-cloth card three round trips. Ф1 already made this choice in
		// ScopeFabricDirection itself; printing only the head of its answer would undo it.
		names := make([]string, 0, len(unknownLines))
		for _, l := range unknownLines {
			names = append(names, fabricLineLabel(l))
		}
		detail = fmt.Sprintf("направление ткани не заполнено: %s — пока оно неизвестно, сказать, законен ли переворот, нельзя", strings.Join(names, ", "))
	case !m.AllowFlip.Valid:
		detail = fmt.Sprintf("ткань односторонняя, а раскладка %q не записала, разрешался ли при съёмке переворот детали — вердикта нет", m.Name)
	default:
		detail = fmt.Sprintf("ткань односторонняя, а раскладка %q снята С РАЗРЕШЁННЫМ переворотом: измеренную длину на этой ткани не воспроизвести", m.Name)
	}
	add(entity.RunReadinessFinding{Key: entity.RunReadinessKeyNormFlipPolicy, Severity: sev,
		Label: label, Detail: detail, Target: tgt})
}

func fabricLineLabel(l entity.FabricDirectionLine) string {
	if n := strings.TrimSpace(l.Name); n != "" {
		return fmt.Sprintf("%q (%s)", n, l.LineKey)
	}
	return l.LineKey
}

func (b *runReadinessBuilder) pieceSetRow(m *entity.TechCardMarkerSummary,
	tgt entity.RunReadinessTarget, add func(entity.RunReadinessFinding)) {
	const label = "набор деталей карточки не менялся после съёмки нормы"
	status := m.PieceSetStatus()
	sev := entity.NormPieceSet(status)
	detail := ""
	switch sev {
	case entity.RunReadinessOK:
	case entity.RunReadinessBlocker:
		detail = fmt.Sprintf("набор деталей кроя изменился после того, как была снята раскладка %q — норма посчитана по другому изделию", m.Name)
	default:
		detail = fmt.Sprintf("отпечаток набора деталей у раскладки %q не записан (снята до Ф3) либо не вычисляется сегодня — «не проверено», а НЕ «изменился»", m.Name)
	}
	add(entity.RunReadinessFinding{Key: entity.RunReadinessKeyNormPieceSet, Severity: sev,
		Label: label, Detail: detail, Target: tgt})
}

func (b *runReadinessBuilder) widthRow(m *entity.TechCardMarkerSummary, materialID int,
	tgt entity.RunReadinessTarget, add func(entity.RunReadinessFinding)) {
	const label = "ширина съёмки нормы не больше сегодняшней полезной ширины артикула"
	var (
		selvedge = decimal.Zero
		nominal  decimal.NullDecimal
	)
	if art, ok := b.card.LinkedMaterials[materialID]; ok {
		selvedge = art.FabricSelvedgeCm()
		nominal = art.UsableFabricWidthCm()
	}
	v := entity.NormWidthVsArticle(m.FabricWidthCm, b.in.NarrowestMeasuredLotCm[materialID], selvedge, nominal)
	detail := ""
	// The detail MUST say which of the two branches answered, and with the numbers: «the marker was
	// taken at 150 and the narrowest available roll measures 148» and «…and the article's nominal
	// less the кромка is 146» send an operator to two different actions (re-measure the lot vs fix
	// the catalogue), and only one of them is about buying cloth.
	switch v.Basis {
	case entity.NormWidthBasisNone:
		detail = "у артикула нет ни измеренной ширины лота, ни ширины в каталоге — сравнивать не с чем"
	case entity.NormWidthBasisLot:
		if v.Severity != entity.RunReadinessOK {
			detail = fmt.Sprintf("раскладка %q снята на %s см кроя, а самый узкий доступный лот меряется %s см (минус кромка %s см с каждого края = %s см кроя)",
				m.Name, m.FabricWidthCm.String(), v.MeasuredRollCm.Decimal.String(),
				selvedge.String(), v.TodayCuttingCm.Decimal.String())
		}
	case entity.NormWidthBasisNominal:
		if v.Severity != entity.RunReadinessOK {
			detail = fmt.Sprintf("раскладка %q снята на %s см кроя, а номинал артикула за вычетом кромки даёт %s см. Измеренных лотов у артикула нет",
				m.Name, m.FabricWidthCm.String(), v.TodayCuttingCm.Decimal.String())
		}
	}
	add(entity.RunReadinessFinding{Key: entity.RunReadinessKeyNormWidthVsArticle, Severity: v.Severity,
		Label: label, Detail: detail, Target: tgt})
}

// --- ПРОГОН -------------------------------------------------------------------------------------

func (b *runReadinessBuilder) runChecks(plan *pb_admin.GetProductionRunMaterialPlanResponse) {
	b.prepare()
	add := func(f entity.RunReadinessFinding) { b.runRows = append(b.runRows, f) }

	// sizes_in_range
	inRange := make(map[int]bool, len(b.card.SizeIds))
	for _, id := range b.card.SizeIds {
		inRange[id] = true
	}
	var stray []int
	seenStray := map[int]bool{}
	for _, c := range entity.SortedCells(b.in.Cells) {
		// A cell with NO size (size_id 0) is skipped rather than reported out of range. It names no
		// size, so «размер 0 вне ряда» would be a sentence about nothing; and the shape it belongs to
		// — a colourless aux line, which carries an output variant instead of a product and no size —
		// is one this gate never judges anyway, because an auxiliary card short-circuits above. A
		// sellable line arriving without a size is refused by the store's own linkage check, which is
		// where that fact already has an owner.
		if c.PlannedQty <= 0 || c.SizeId <= 0 || inRange[c.SizeId] || seenStray[c.SizeId] {
			continue
		}
		seenStray[c.SizeId] = true
		stray = append(stray, c.SizeId)
	}
	if len(stray) == 0 {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeySizesInRange, Severity: entity.RunReadinessOK,
			Label: "каждый планируемый размер входит в размерный ряд карточки", Target: b.target(),
		})
	} else {
		t := b.target()
		t.SizeId = stray[0]
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeySizesInRange, Severity: entity.RunReadinessBlocker,
			Label:  "каждый планируемый размер входит в размерный ряд карточки",
			Detail: fmt.Sprintf("вне ряда: %s", joinInts(stray)),
			Target: t,
		})
	}

	// quantities_present — WARNING: a colourway ticked with no quantities produces nothing, which is
	// a mistake worth naming and not a reason to refuse the whole run (the other colourways are real).
	planned := map[int]bool{}
	for _, c := range b.in.Cells {
		if c.PlannedQty > 0 {
			planned[c.ColorwayId] = true
		}
	}
	var empty []int
	for _, cid := range b.in.ColorwayIds {
		if !planned[cid] {
			empty = append(empty, cid)
		}
	}
	if len(empty) == 0 {
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyQuantitiesPresent, Severity: entity.RunReadinessOK,
			Label: "у каждого выбранного колорвея есть хотя бы одно положительное количество", Target: b.target(),
		})
	} else {
		t := b.target()
		t.ColorwayId = empty[0]
		add(entity.RunReadinessFinding{
			Key: entity.RunReadinessKeyQuantitiesPresent, Severity: entity.RunReadinessWarning,
			Label:  "у каждого выбранного колорвея есть хотя бы одно положительное количество",
			Detail: fmt.Sprintf("выбраны, но без количеств: %s — эти колорвеи не произведут ничего", joinInts(empty)),
			Target: t,
		})
	}

	b.sizesInDxfRow(add)
	b.stockShortageRow(plan, add)
}

// sizesInDxfRow is ONE aggregated row over (scope × planned size), and the aggregation rule is where
// the UNKNOWN discipline has to hold: the worst severity wins, but UNKNOWN can only ever beat OK,
// never turn into BLOCKER and never be absorbed by it into a claim that everything was checked.
func (b *runReadinessBuilder) sizesInDxfRow(add func(entity.RunReadinessFinding)) {
	const label = "планируемые размеры есть в файлах выкроек"
	tgt := b.target()

	// Which sizes: the planned ones, and the whole range before quantities are typed — the modal
	// shows this row at step 3, when `cells` is still empty.
	sizes := make([]int, 0, len(b.card.SizeIds))
	seen := map[int]bool{}
	for _, c := range entity.SortedCells(b.in.Cells) {
		if c.PlannedQty > 0 && !seen[c.SizeId] {
			seen[c.SizeId] = true
			sizes = append(sizes, c.SizeId)
		}
	}
	if len(sizes) == 0 {
		sizes = append(sizes, b.card.SizeIds...)
	}
	if len(sizes) == 0 {
		add(entity.RunReadinessFinding{Key: entity.RunReadinessKeySizesInDxf, Severity: entity.RunReadinessUnknown,
			Label: label, Detail: "у карточки нет размерного ряда — проверять нечего", Target: tgt})
		return
	}

	scopes := b.patternScopes()
	if len(scopes) == 0 {
		add(entity.RunReadinessFinding{Key: entity.RunReadinessKeySizesInDxf, Severity: entity.RunReadinessUnknown,
			Label: label, Detail: "на карточке нет ни одного файла выкроек — проверить размеры не по чему", Target: tgt})
		return
	}

	worst := entity.RunReadinessOK
	var notes []string
	for _, sc := range scopes {
		var rp *entity.PatternSizeIndexRow
		if r, ok := b.in.PatternSizeIndex[sc.key]; ok {
			rp = &r
		}
		state, tokens := entity.PatternSizeIndexStatus(rp, entity.PatternSheetFingerprint(sc.sheets))
		switch state {
		case entity.PatternSizeIndexMissing:
			notes = append(notes, fmt.Sprintf("%s: размеры в файлах не проверялись — нажмите «⌕ размеры в файлах» на вкладке выкроек", sc.key))
			worst = worseReadiness(worst, entity.RunReadinessUnknown)
			continue
		case entity.PatternSizeIndexStale:
			notes = append(notes, fmt.Sprintf("%s: файлы менялись после проверки — разбор устарел", sc.key))
			worst = worseReadiness(worst, entity.RunReadinessUnknown)
			continue
		case entity.PatternSizeIndexUngraded:
			notes = append(notes, fmt.Sprintf("%s: файлы не несут размерного кодирования (похоже, один размер на файл) — вердикта нет", sc.key))
			worst = worseReadiness(worst, entity.RunReadinessUnknown)
			continue
		}
		var missing []string
		for _, sid := range sizes {
			if entity.SizesInDxf(state, tokens, sizeDictionaryName(sid)) == entity.RunReadinessBlocker {
				missing = append(missing, sizeLabelOf(sid))
			}
		}
		if len(missing) > 0 {
			notes = append(notes, fmt.Sprintf("%s: в файлах нет %s", sc.key, strings.Join(missing, ", ")))
			worst = worseReadiness(worst, entity.RunReadinessBlocker)
		}
	}
	detail := ""
	if worst != entity.RunReadinessOK {
		detail = strings.Join(notes, "; ")
	}
	add(entity.RunReadinessFinding{Key: entity.RunReadinessKeySizesInDxf, Severity: worst,
		Label: label, Detail: detail, Target: tgt})
}

// worseReadiness is the ONE place two severities are combined, and it exists so the combination
// cannot be written wrong somewhere else. The order is OK < UNKNOWN < WARNING < BLOCKER, which
// preserves both halves of the UNKNOWN contract: an UNKNOWN component can never be absorbed into an
// OK verdict, and it can never on its own produce a BLOCKER.
func worseReadiness(a, b entity.RunReadinessSeverity) entity.RunReadinessSeverity {
	rank := func(s entity.RunReadinessSeverity) int {
		switch s {
		case entity.RunReadinessOK:
			return 0
		case entity.RunReadinessUnknown:
			return 1
		case entity.RunReadinessWarning:
			return 2
		case entity.RunReadinessBlocker:
			return 3
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

type patternScope struct {
	key    string
	sheets []entity.PatternSheetRef
}

// patternScopes groups the card's pattern sheets by the ONE binding rule's uniqueness bucket. The
// fingerprint is computed HERE, from the server's own rows, which is what makes the index
// unforgeable in the dangerous direction and what makes a re-upload stale it by itself.
func (b *runReadinessBuilder) patternScopes() []patternScope {
	return patternScopesOfCard(b.card)
}

func patternScopesOfCard(card *entity.TechCard) []patternScope {
	byKey := map[string][]entity.PatternSheetRef{}
	var order []string
	for i := range card.Patterns {
		p := &card.Patterns[i]
		key := entity.FabricScopeKey(p.FabricPurpose.String, p.BomLineKey.String)
		if key == "" {
			// A sheet bound to nothing at all (uploaded before 0260). pattern_binding_resolved already
			// names it; indexing it under "" would let one unbound sheet answer for every cloth.
			continue
		}
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], entity.PatternSheetRef{
			LineKey: p.LineKey, URL: p.URL, Version: p.Version,
		})
	}
	sort.Strings(order)
	out := make([]patternScope, 0, len(order))
	for _, k := range order {
		out = append(out, patternScope{key: k, sheets: byKey[k]})
	}
	return out
}

// stockShortageRow — WARNING at worst, never a BLOCKER: a shortage is a purchasing fact, and
// refusing to PLAN a run because the cloth has not arrived yet would invert the order in which a
// factory works. UNKNOWN when the plan could not compare quantities at all, which it signals through
// unit_code = UNKNOWN — a machine signal, deliberately not the prose of a caveat.
func (b *runReadinessBuilder) stockShortageRow(plan *pb_admin.GetProductionRunMaterialPlanResponse, add func(entity.RunReadinessFinding)) {
	const label = "на складе хватает материалов (по голому остатку, без учёта резервов)"
	tgt := b.target()
	var short, unresolved []string
	shortMaterial := int64(0)
	for _, r := range plan.GetRows() {
		// MATERIAL_UNIT_UNKNOWN is the vocabulary's zero value and means the row's unit is free text
		// this server does not know. That is a MACHINE signal, deliberately used instead of matching on
		// the plan's prose caveat: branching on a sentence is exactly what MaterialPlanBlocker.key was
		// added to stop, and the same discipline applies here.
		if r.GetUnitCode() == pb_common.MaterialUnit_MATERIAL_UNIT_UNKNOWN {
			unresolved = append(unresolved, r.GetMaterialName())
			continue
		}
		if v, err := decimal.NewFromString(r.GetShortage().GetValue()); err == nil && v.IsPositive() {
			if shortMaterial == 0 {
				shortMaterial = int64(r.GetMaterialId())
			}
			short = append(short, fmt.Sprintf("%s — не хватает %s %s", r.GetMaterialName(), v.String(), r.GetUnit()))
		}
	}
	switch {
	case len(short) > 0:
		t := tgt
		t.MaterialId = shortMaterial
		detail := strings.Join(short, "; ")
		if len(unresolved) > 0 {
			detail += fmt.Sprintf(". Кроме того, единицы не сводятся у: %s — по ним сравнения нет вовсе", strings.Join(unresolved, ", "))
		}
		add(entity.RunReadinessFinding{Key: entity.RunReadinessKeyStockShortage, Severity: entity.RunReadinessWarning,
			Label: label, Detail: detail, Target: t})
	case len(unresolved) > 0:
		add(entity.RunReadinessFinding{Key: entity.RunReadinessKeyStockShortage, Severity: entity.RunReadinessUnknown,
			Label: label,
			Detail: fmt.Sprintf("единица измерения не сводится у: %s — число слота под меткой складской единицы сравнивать нельзя, поэтому вердикта по остатку нет",
				strings.Join(unresolved, ", ")),
			Target: tgt})
	default:
		add(entity.RunReadinessFinding{Key: entity.RunReadinessKeyStockShortage, Severity: entity.RunReadinessOK,
			Label: label, Target: tgt})
	}
}

// --- ПОКРЫТИЕ -----------------------------------------------------------------------------------

// coverage turns the plan's contributions into the modal's «оценка по норме» table.
//
// on_hand and shortage are the ARTICLE's, not the row's, and they are repeated on every row naming
// that article rather than shown once: an operator reading one line wants the pile of cloth beside
// the requirement. THEY MUST NEVER BE SUMMED — two colourways drawing on one article share ONE pile,
// and adding two identical on_hand figures would invent stock. The proto field says so too.
//
// They are omitted entirely when the slot's unit and the stock unit are not the same unit: the plan
// keeps the contribution in the SLOT's unit while the rollup row is in the STOCK's, and printing one
// under the other's label is the failure the plan's own comment calls «a purchase order for 18 000
// cones».
func (b *runReadinessBuilder) coverage(plan *pb_admin.GetProductionRunMaterialPlanResponse) []*pb_admin.ProductionRunReadinessCoverage {
	type stockFact struct {
		unit     string
		onHand   string
		shortage string
	}
	byMaterial := map[int32]stockFact{}
	for _, r := range plan.GetRows() {
		byMaterial[r.GetMaterialId()] = stockFact{
			unit: r.GetUnit(), onHand: r.GetOnHand().GetValue(), shortage: r.GetShortage().GetValue(),
		}
	}
	out := make([]*pb_admin.ProductionRunReadinessCoverage, 0, len(plan.GetContributions()))
	for _, c := range plan.GetContributions() {
		row := &pb_admin.ProductionRunReadinessCoverage{
			ColorwayId:   c.GetColorwayId(),
			BomItemId:    c.GetBomItemId(),
			SlotName:     c.GetSlotName(),
			MaterialId:   int64(c.GetMaterialId()),
			MaterialName: c.GetMaterialName(),
			Unit:         c.GetUnit(),
			UnitCode:     pbMaterialUnit(c.GetUnit()),
			Required:     c.GetRequired(),
			// NORM today, always. Ф4.6 switches the requirement to the lays and flips this field; the
			// message does not change, which is the whole reason the field exists.
			Source: pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM,
		}
		if s, ok := byMaterial[c.GetMaterialId()]; ok && entity.SameMaterialUnit(s.unit, c.GetUnit()) {
			row.OnHand = pbDecimalString(s.onHand)
			row.Shortage = pbDecimalString(s.shortage)
		}
		out = append(out, row)
	}
	return out
}

// unitCoverage answers «how many GARMENTS is this cell provisioned for», which is a different
// question from «how much cloth». A garment is not cut until ALL of its cloths are: 22 fronts and
// 20 linings are 20 garments.
//
// provisioned_qty is binary today — 0 or the whole cell — and that is not a shortcut, it is what the
// NORM can see: a slot either has an article and a norm for this size or it does not. A fraction
// would need placement counts in a lay, which is Ф4.5 and needs data that does not exist (a run has
// no link to a marker at all). units_from_stock is the fraction that DOES exist, and it comes from
// the shelf rather than from the recipe.
func (b *runReadinessBuilder) unitCoverage() []*pb_admin.ProductionRunReadinessUnitCoverage {
	byProduct := make(map[int]*entity.TechCardColorway, len(b.card.Colorways))
	for i := range b.card.Colorways {
		cw := &b.card.Colorways[i]
		if cw.ProductId.Valid {
			byProduct[int(cw.ProductId.Int32)] = cw
		}
	}
	var out []*pb_admin.ProductionRunReadinessUnitCoverage
	for _, cell := range entity.SortedCells(b.in.Cells) {
		if cell.PlannedQty <= 0 {
			continue
		}
		cw := byProduct[cell.ColorwayId]
		row := &pb_admin.ProductionRunReadinessUnitCoverage{
			ColorwayId: int32(cell.ColorwayId),
			SizeId:     int32(cell.SizeId),
			PlannedQty: int32(cell.PlannedQty),
			Source:     pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM,
		}
		if cw == nil {
			out = append(out, row) // provisioned 0; colorway_live already said why
			continue
		}
		usageByBom := map[int]*entity.TechCardColorwayUsage{}
		for j := range cw.Usages {
			u := &cw.Usages[j]
			if bom := planBomLine(u, b.card.BomItems); bom != nil {
				usageByBom[bom.Id] = u
			}
		}
		ok := true
		fromStock := -1
		for i := range b.card.BomItems {
			bom := &b.card.BomItems[i]
			if !planSlotSections[bom.Section] {
				continue
			}
			u := usageByBom[bom.Id]
			if u == nil {
				// A recipe-section slot the recipe never references. The plan already counts this as a
				// blocker; the same rule has to hold here or a slot added to the BOM after the
				// colourways were authored would change nothing in the count.
				ok = false
				row.BlockingBomItemIds = append(row.BlockingBomItemIds, int64(bom.Id))
				continue
			}
			mid, _ := u.EffectiveMaterialId(bom)
			norm, _, _, hasNorm := usageNormForSize(u, cell.SizeId)
			if mid == 0 || !hasNorm {
				ok = false
				row.BlockingBomItemIds = append(row.BlockingBomItemIds, int64(bom.Id))
				continue
			}
			if norm.IsPositive() {
				units := b.in.OnHand[mid].Div(norm).IntPart()
				if units < 0 {
					units = 0
				}
				if fromStock < 0 || int(units) < fromStock {
					fromStock = int(units)
				}
			}
		}
		if ok {
			row.ProvisionedQty = int32(cell.PlannedQty)
		}
		if fromStock < 0 {
			fromStock = 0
		}
		if fromStock > cell.PlannedQty {
			fromStock = cell.PlannedQty
		}
		row.UnitsFromStock = int32(fromStock)
		out = append(out, row)
	}
	return out
}

// --- НА ПРОВОД ----------------------------------------------------------------------------------

// RunReadinessToPb maps the verdict onto the wire.
func RunReadinessToPb(res RunReadinessResult) *pb_admin.CheckProductionRunReadinessResponse {
	r := res.Report
	out := &pb_admin.CheckProductionRunReadinessResponse{
		Ready:           r.Ready(),
		BlockingEnabled: r.BlockingEnabled,
		WouldBlock:      r.WouldBlock(),
		UnknownCount:    int32(r.UnknownCount()),
		Card:            runReadinessFindingsToPb(r.Card),
		Run:             runReadinessFindingsToPb(r.Run),
		Coverage:        res.Coverage,
		UnitCoverage:    res.UnitCoverage,
		Caveats:         r.Caveats,
	}
	for _, c := range r.Colorways {
		out.Colorways = append(out.Colorways, &pb_admin.ProductionRunReadinessColorway{
			ColorwayId:   int32(c.ColorwayId),
			ColorwayName: c.Name,
			Ready:        c.Ready(),
			Findings:     runReadinessFindingsToPb(c.Findings),
		})
	}
	return out
}

func runReadinessFindingsToPb(in []entity.RunReadinessFinding) []*pb_admin.ProductionRunReadinessFinding {
	out := make([]*pb_admin.ProductionRunReadinessFinding, 0, len(in))
	for _, f := range in {
		out = append(out, &pb_admin.ProductionRunReadinessFinding{
			Key:      f.Key,
			Severity: runReadinessSeverityToPb(f.Severity),
			Label:    f.Label,
			// A passing row carries no detail — the same rule readinessReq keeps for the advisory
			// checklist, enforced HERE rather than trusted to every branch above, because a detail on
			// an OK row reads as a failure that was let through.
			Detail: detailUnlessOK(f),
			Target: &pb_admin.ProductionRunReadinessTarget{
				TechCardId: int32(f.Target.TechCardId),
				ColorwayId: int32(f.Target.ColorwayId),
				BomLineKey: f.Target.BomLineKey,
				BomItemId:  f.Target.BomItemId,
				MarkerId:   int32(f.Target.MarkerId),
				MaterialId: f.Target.MaterialId,
				SizeId:     int32(f.Target.SizeId),
			},
		})
	}
	return out
}

func detailUnlessOK(f entity.RunReadinessFinding) string {
	if f.Severity == entity.RunReadinessOK {
		return ""
	}
	return f.Detail
}

func runReadinessSeverityToPb(s entity.RunReadinessSeverity) pb_admin.ProductionRunReadinessSeverity {
	switch s {
	case entity.RunReadinessOK:
		return pb_admin.ProductionRunReadinessSeverity_PRODUCTION_RUN_READINESS_SEVERITY_OK
	case entity.RunReadinessUnknown:
		return pb_admin.ProductionRunReadinessSeverity_PRODUCTION_RUN_READINESS_SEVERITY_UNKNOWN
	case entity.RunReadinessWarning:
		return pb_admin.ProductionRunReadinessSeverity_PRODUCTION_RUN_READINESS_SEVERITY_WARNING
	case entity.RunReadinessBlocker:
		return pb_admin.ProductionRunReadinessSeverity_PRODUCTION_RUN_READINESS_SEVERITY_BLOCKER
	}
	return pb_admin.ProductionRunReadinessSeverity_PRODUCTION_RUN_READINESS_SEVERITY_UNSPECIFIED
}

// RunReadinessCellsFromPb reads the request grid.
func RunReadinessCellsFromPb(in []*pb_admin.ProductionRunReadinessCell) []entity.RunReadinessCell {
	out := make([]entity.RunReadinessCell, 0, len(in))
	for _, c := range in {
		out = append(out, entity.RunReadinessCell{
			ColorwayId: int(c.GetColorwayId()),
			SizeId:     int(c.GetSizeId()),
			PlannedQty: int(c.GetPlannedQty()),
		})
	}
	return out
}

// RunReadinessCellsFromRunLines reads the grid off a run that already exists — the create-time
// re-check and the detail page both judge THE SAME SHAPE the modal does, through one function, so a
// stored run and a proposed one can never be judged by two different rules.
func RunReadinessCellsFromRunLines(lines []entity.ProductionRunLine) []entity.RunReadinessCell {
	out := make([]entity.RunReadinessCell, 0, len(lines))
	for i := range lines {
		ln := &lines[i]
		c := entity.RunReadinessCell{SizeId: ln.SizeId, PlannedQty: ln.PlannedQty}
		if ln.ProductId.Valid {
			c.ColorwayId = int(ln.ProductId.Int32)
		}
		out = append(out, c)
	}
	return out
}

// RunReadinessColorwayIdsFromCells derives the colourway set from a grid, for the paths that have no
// separate list — a stored run's lines ARE its colourways.
func RunReadinessColorwayIdsFromCells(cells []entity.RunReadinessCell) []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range entity.SortedCells(cells) {
		if c.ColorwayId <= 0 || seen[c.ColorwayId] {
			continue
		}
		seen[c.ColorwayId] = true
		out = append(out, c.ColorwayId)
	}
	return out
}

func joinInts(v []int) string {
	parts := make([]string, 0, len(v))
	for _, x := range v {
		parts = append(parts, fmt.Sprintf("%d", x))
	}
	return strings.Join(parts, ", ")
}

// pbDecimalString re-wraps a decimal string the plan already produced. It exists so the coverage
// table carries the SAME rounded figure the material plan printed rather than a second rounding of
// the same quantity — two numbers that differ in the last digit read as two different facts.
func pbDecimalString(v string) *pb_decimal.Decimal {
	if v == "" {
		return nil
	}
	return &pb_decimal.Decimal{Value: v}
}

// --- Р4: ЧИНИМ ВРУЩУЮ СТРОКУ `patterns` В СОВЕТУЮЩЕМ ЧЕКЛИСТЕ --------------------------------

// TechCardPatternSizeVerdict answers «does every size of the range have a pattern» for the ADVISORY
// checklist (GetTechCardReadiness), from the Ф6.3 index rather than from a storage artefact.
//
// WHY THE OLD ROW HAD TO GO. It counted DISTINCT tech_card_size_pattern.size_id against the size
// range — but the client files EVERY sheet of a card under the smallest size of the range and calls
// that a storage artefact in its own comment. So a card with five sizes and one graded DXF read as
// «1 of 5» and could never satisfy the prod checklist, while a card with one flat sheet per size read
// as fully covered whether or not those files contain those sizes. Both directions were wrong, and
// the FALSE PASS is the worse one.
//
// The replacement never claims «verified» without evidence. It returns three states:
//
//	ok=true                → every size resolved in every scope that has a usable index
//	ok=false, unknown!=""  → no verdict, and the sentence says which of the three reasons
//	ok=false, missing!=nil → a real, evidenced miss: these sizes are in no scope's files
//
// The unknown case is what makes this deployable: an UNKNOWN row does not count as unmet, so NOT ONE
// card becomes newly blocked on the day this ships. The real check switches itself on card by card
// as the index fills.
func TechCardPatternSizeVerdict(card *entity.TechCard, index map[string]entity.PatternSizeIndexRow) (ok bool, unknown string, missing []string) {
	if card == nil || len(card.SizeIds) == 0 {
		return false, "размерный ряд пуст — покрытие выкройками считать не по чему", nil
	}
	scopes := patternScopesOfCard(card)
	if len(scopes) == 0 {
		return false, "на карточке нет ни одного файла выкроек с привязкой к ткани", nil
	}
	var reasons []string
	missingSet := map[int]bool{}
	usable := 0
	for _, sc := range scopes {
		var rp *entity.PatternSizeIndexRow
		if r, found := index[sc.key]; found {
			rp = &r
		}
		state, tokens := entity.PatternSizeIndexStatus(rp, entity.PatternSheetFingerprint(sc.sheets))
		switch state {
		case entity.PatternSizeIndexMissing:
			reasons = append(reasons, fmt.Sprintf("%s: размеры в файлах не проверялись", sc.key))
			continue
		case entity.PatternSizeIndexStale:
			reasons = append(reasons, fmt.Sprintf("%s: файлы менялись после проверки", sc.key))
			continue
		case entity.PatternSizeIndexUngraded:
			reasons = append(reasons, fmt.Sprintf("%s: файлы не несут размерного кодирования", sc.key))
			continue
		}
		usable++
		for _, sid := range card.SizeIds {
			if !entity.SizeCoveredByTokens(sizeDictionaryName(sid), tokens) {
				missingSet[sid] = true
			}
		}
	}
	if usable == 0 {
		return false, strings.Join(reasons, "; ") + " — нажмите «⌕ размеры в файлах» на вкладке выкроек", nil
	}
	if len(missingSet) == 0 {
		// A partially indexed card still reads as UNKNOWN rather than as met: claiming coverage while
		// one scope was never parsed is the same false pass this row is being fixed for.
		if len(reasons) > 0 {
			return false, strings.Join(reasons, "; "), nil
		}
		return true, "", nil
	}
	for _, sid := range card.SizeIds {
		if missingSet[sid] {
			missing = append(missing, sizeLabelOf(sid))
		}
	}
	return false, "", missing
}

func sizeDictionaryName(id int) string {
	if s, ok := cache.GetSizeById(id); ok {
		return s.Name
	}
	return ""
}

// sizeLabelOf is what an operator reads. Falls back to the id so a size missing from the dictionary
// cache still produces an actionable sentence instead of an empty one.
func sizeLabelOf(id int) string {
	if s, ok := cache.GetSizeById(id); ok && strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	return fmt.Sprintf("размер #%d", id)
}
