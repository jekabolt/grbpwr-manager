package dto

import (
	"errors"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ПОКРЫТИЕ ПРОГОНА НАСТИЛАМИ (Ф4.5) — шаги 0 и 4 формулы §6.2 плюс сборка целого.
//
// production_lay_yield.go отвечает на вопрос «что кроит ОДИН маркер» (§6.1, шаги 1, 2, 3) и умеет
// свести ответы по деталям в клетку (CoverageCell, шаг 5). ЗДЕСЬ живёт то, что требует КАРТОЧКУ и
// ПЛАН, и чего тот файл сознательно не взял:
//
//	шаг 0 — какой слот BOM кроит эту деталь у этого колорвея      (pieceSlotBomLine)
//	шаг 4 — какие детали для этого колорвея вообще ОБЯЗАТЕЛЬНЫ    (requiredPiecesForColorway)
//	сборка — обход (колорвей × размер) по строкам прогона          (ComputeLayCoverage)
//	отдача — контракт §6.4, дословно                              (CoverageCellsPb / ApplyToUnitCoverage)
//
// ОДНО ОПРЕДЕЛЕНИЕ ПОКРЫТИЯ, И ОНО ЗДЕСЬ. Ф6 уже описала форму ответа
// (ProductionRunReadinessUnitCoverage), Ф4 — свою клетку (ProductionRunLayCoverageCell). Оба
// заполняются из ОДНОГО расчёта: ApplyToUnitCoverage накладывает те же числа на строки гейта, а не
// считает их второй раз. Второе определение покрытия — это способ получить два разных ответа на
// один вопрос и узнать об этом от цеха.
//
// МЕЖТКАНЕВОСТЬ НИГДЕ НЕ ЗАПРОГРАММИРОВАНА. «22 полочки и 20 подкладок дают 20 изделий» не
// обрабатывается ни одной веткой этого файла: полочка и подкладка — просто две обязательные детали
// одного колорвея, обе попадают в ОДНУ клетку, и 20 выпадает из минимума
// (CoverageCell.KnownMin). Ветки «а если разные ткани дали разное» здесь нет и быть не должно —
// она означала бы, что минимум считается где-то ещё.

// LaySection is one section of a настил as coverage reads it: a marker laid `Plies` deep.
//
// The blob arrives ALREADY DISTILLED. §6.1 asks for one parse per marker per request (memoised by
// marker_id), and a struct that carried the raw blob would invite one parse per section — the exact
// hot-path cost §14 п.16 names. The caller also owns WithSummaryComposition: a legacy blob's состав
// lives on the marker SUMMARY, which is a row this file never sees.
type LaySection struct {
	MarkerID int
	Plies    int
	Yield    MarkerYield
	// YieldErr is the parse's refusal, carried rather than swallowed. A section that could not be
	// read proves NOTHING about what it cut, so every piece of its pair comes back UNKNOWN. Dropping
	// the section instead would silently answer «эта секция не выкроила ничего» — a shortage invented
	// out of a parse failure.
	YieldErr error
}

// Lay is one настил of the run in the terms coverage needs. Deliberately NOT the store's row type:
// like LayFaceMode in production_lay_yield.go, this file stays a pure function of its inputs and
// does not wait on the schema.
type Lay struct {
	LayKey string
	Name   string
	// ColorwayID is the colourway's product id — the same identity production_run_line.product_id
	// carries, so a lay and a run line can be keyed against each other without a translation table.
	ColorwayID int
	// BomItemID is the slot this настил cuts. 0 = the slot is gone from the BOM (fk_tcm_bom is SET
	// NULL, 0257:63): the настил is BROKEN and its contribution is UNATTRIBUTABLE — see §6.3 and
	// brokenLayColorways.
	BomItemID int64
	Mode      LayFaceMode
	Sections  []LaySection
}

// LayCoverageInput is everything the computation reads. Card supplies the pieces, their per-colourway
// slot bindings and the BOM sections; Lines supply the grid (колорвей × размер × planned_qty); Lays
// supply what was actually laid.
type LayCoverageInput struct {
	Card  *entity.TechCard
	Lines []entity.ProductionRunLine
	Lays  []Lay
}

// LayCoverageFinding is a fact about a RUN LINE that coverage could not fold into a cell. It is a
// finding and not a silently skipped row on purpose (§14 п.4): a line that cannot be covered by any
// настил must SAY SO, because dropping it quietly shrinks the denominator and the grid then reads
// «всё покрыто» while a line of the run is uncovered by construction.
type LayCoverageFinding struct {
	Key     string
	LineKey string
	SizeID  int
	Detail  string
}

// Finding keys. Named with the LayCoverage prefix rather than the shorter lay_* spelling the checks
// of §8 use, because those live in another file of this package and a key vocabulary shared by two
// files is a collision waiting for the second author.
const (
	// LayCoverageFindingKeyLineWithoutColorway — production_run_line.product_id IS NULL (0110:20).
	// Настил принадлежит паре (колорвей, слот); строка без колорвея не принадлежит ни одной паре.
	LayCoverageFindingKeyLineWithoutColorway = "run_line_without_colorway"
	// LayCoverageFindingKeyLineWithoutSize — production_run_line.size_id IS NULL (0236), read as 0.
	// The same sentence of §14 п.4 names both columns, and the same reasoning applies: a marker's
	// состав is keyed by size, so a sizeless line matches no состав and no настил can cover it.
	LayCoverageFindingKeyLineWithoutSize = "run_line_without_size"
)

// LayCoveragePieceYield is the per-piece diagnostic behind one cell — «почему покрытие такое»,
// read by a human in the cell's popover (ProductionRunLayPieceYield).
type LayCoveragePieceYield struct {
	ColorwayID         int
	SizeID             int
	PieceLineKey       string
	PieceName          string
	BomItemID          int64
	RequiredPerGarment int
	CutAsDrawn         int
	CutMirrored        int
	GarmentYield       int
	OvercutQty         int
	Status             CoverageStatus
	Detail             string
}

// LayCoverageCell is one (колорвей, размер) cell before it becomes a wire message.
type LayCoverageCell struct {
	ColorwayID int
	SizeID     int
	PlannedQty int
	// CoveredQty is the minimum over the required pieces, capped at PlannedQty. WHEN Status IS
	// UNKNOWN IT IS A LOWER BOUND — see CoverageCell.CoveredQty; UnknownPieceCount travels beside it
	// so the client can render «не меньше чем».
	CoveredQty         int
	Status             CoverageStatus
	BlockingBomItemIDs []int64
	BlockingPieceNames []string
	UnknownPieceCount  int
	// Source is §6.4's signature: LAYS when every required slot of this colourway has a настил, NORM
	// when none has, MIXED when some do. A MIXED cell is the honest state of a run whose основная
	// ткань is laid and whose подкладка is not.
	Source pb_admin.ProductionRunCoverageSource
	// SlotsWithoutLays are the required slots with no настил at all. They are the reason a cell is
	// MIXED, and they are what a caller falls back to the NORM estimate for.
	SlotsWithoutLays []int64
}

// LayCoverage is the whole answer for one run.
type LayCoverage struct {
	// Applicable is false for an aux card — §6.3's last row. Отдаётся ЯВНО, не пустым списком:
	// пустой список читается как «настилов пока нет», то есть как приглашение их построить.
	Applicable          bool
	NotApplicableReason string
	Cells               []LayCoverageCell
	PieceYields         []LayCoveragePieceYield
	Findings            []LayCoverageFinding
	// Caveats are the irreducible «этот ответ — пол, а не факт» sentences, deduplicated and sorted.
	Caveats []string
	// UnknownCount counts what COVERAGE could not answer: cells that came out UNKNOWN plus findings.
	// It is not the whole response's unknown_count — the fitness checks of §8 add their own — and a
	// caller must add, never replace.
	UnknownCount int
}

// ComputeLayCoverage is §6.2 end to end for one run: every (колорвей, размер) of the run's lines,
// judged against what the run's настилы actually cut.
//
// PURE. It touches no database and no clock; everything it knows arrives in LayCoverageInput.
func ComputeLayCoverage(in LayCoverageInput) LayCoverage {
	out := LayCoverage{Applicable: true}
	card := in.Card
	if card == nil {
		out.Applicable = false
		out.NotApplicableReason = "карточка не загружена"
		return out
	}
	if card.Purpose == entity.TechCardPurposeAuxiliary {
		// §6.3, последняя строка: у aux-карточки нет ни колорвеев, ни деталей кроя. Ноль означал бы
		// «не покрыто», а покрывать нечего.
		out.Applicable = false
		out.NotApplicableReason = "вспомогательная карточка — настилов и покрытия у неё не бывает"
		return out
	}

	b := &layCoverageBuilder{
		card:       card,
		laysByPair: make(map[layPairKey][]Lay),
		slotsWith:  make(map[int]map[int64]bool),
		brokenLay:  make(map[int]string),
		required:   make(map[int][]requiredPiece),
		seenCaveat: make(map[string]bool),
	}
	b.indexLays(in.Lays)

	for _, cell := range b.grid(in.Lines, &out) {
		c, yields := b.cell(cell.colorwayID, cell.sizeID, cell.plannedQty)
		out.Cells = append(out.Cells, c)
		out.PieceYields = append(out.PieceYields, yields...)
		if c.Status == CoverageStatusUnknown {
			out.UnknownCount++
		}
	}
	out.UnknownCount += len(out.Findings)
	out.Caveats = b.sortedCaveats()
	return out
}

// Status folds the run's cells into one verdict, and it folds THROUGH THE LADDER
// (WorstCoverageStatus): BLOCKER бьёт UNKNOWN, UNKNOWN бьёт OK.
//
// НЕ `max` ПО КОНСТАНТАМ. CoverageStatus is ordered so that its ZERO VALUE is UNKNOWN, which puts
// OK numerically ABOVE it; a fold written as max would therefore answer OK for a run holding an
// UNKNOWN cell — a green badge over a run nobody proved anything about. The two orders are
// deliberately different and production_lay_yield.go pins the divergence with a test.
//
// A run with no cells at all is UNKNOWN, not OK: «нечего покрывать» is a statement about the card
// (an aux card sets Applicable=false), not a verdict about a run.
func (c LayCoverage) Status() CoverageStatus {
	ss := make([]CoverageStatus, 0, len(c.Cells))
	for _, cell := range c.Cells {
		ss = append(ss, cell.Status)
	}
	return WorstCoverageStatus(ss...)
}

// ColorwayStatus is the same fold over one colourway — «зелено только когда раскроены ВСЕ пары
// этого цвета», which is what greys a colourway's badge.
func (c LayCoverage) ColorwayStatus(colorwayID int) CoverageStatus {
	ss := make([]CoverageStatus, 0, len(c.Cells))
	for _, cell := range c.Cells {
		if cell.ColorwayID == colorwayID {
			ss = append(ss, cell.Status)
		}
	}
	return WorstCoverageStatus(ss...)
}

// --- ШАГ 0: ДЕТАЛЬ → СЛОТ -----------------------------------------------------------------------

// pieceSlotBomLine resolves ONE cut-piece's fabric slot for ONE colourway — §6.2 step 0.
//
// IT CALLS THE RECIPE'S RESOLVER; it does not reproduce its priority. tech_card_piece_material
// carries the SAME reference pair as a colourway usage — the durable FK bom_item_id plus the LEGACY
// POSITIONAL bom_item_index (0109:39) — into the SAME []entity.TechCardBomItem, and planBomLine
// reads nothing else off the value it is handed. Понимание ТОЛЬКО позиционного индекса уже давало
// ПУСТОЙ материал-план на бете (живой баг, зафиксированный комментарием на planBomLine); второй
// резолвер здесь означал бы, что покрытие и рецепт могут разойтись в том, какой слот кроит деталь —
// и разойдутся они молча, потому что обе стороны вернут какой-то слот.
//
// The synthetic usage is the price of keeping ONE implementation while planBomLine's signature is
// typed to the usage. Расщепить planBomLine на общее ядро (id, index, items) — правка ЧУЖОГО файла;
// она предложена оркестратору отдельно. Пока её нет, вызов идёт через ту самую функцию, и любое
// изменение её приоритета покрытие подхватывает автоматически — что и есть требуемый инвариант.
func pieceSlotBomLine(m *entity.TechCardPieceMaterial, items []entity.TechCardBomItem) *entity.TechCardBomItem {
	if m == nil {
		return nil
	}
	return planBomLine(&entity.TechCardColorwayUsage{
		BomItemId:    m.BomItemId,
		BomItemIndex: m.BomItemIndex,
		// BomLineKey is passed through even though planBomLine does not read it TODAY. Both types
		// carry the same wire reference by stable line_key, and the day the shared resolver learns
		// about it, a synthetic usage that silently dropped it would resolve pieces by an older rule
		// than the recipe — the divergence this whole function exists to make impossible.
		BomLineKey: m.BomLineKey,
	}, items)
}

// pieceMaterialForColorway finds the piece's binding for one colourway. TechCardPieceMaterial.
// ColorwayID is the explicit colourway id = product.id, the same identity a run line names.
func pieceMaterialForColorway(p *entity.TechCardPiece, colorwayID int) *entity.TechCardPieceMaterial {
	for i := range p.Materials {
		if p.Materials[i].ColorwayID == colorwayID {
			return &p.Materials[i]
		}
	}
	return nil
}

// --- ШАГ 4: ОБЯЗАТЕЛЬНЫЕ ДЕТАЛИ КОЛОРВЕЯ ---------------------------------------------------------

// requiredPiece is one card cut-piece as the cell sees it: either bound to a slot, or unresolved and
// therefore UNKNOWN — never absent. §6.2 step 0 says it in one line: «деталь, чей слот не
// резолвится, даёт UNKNOWN, а не выпадает».
type requiredPiece struct {
	piece     *entity.TechCardPiece
	bomItemID int64
	bomName   string
	resolved  bool
	// reason is why the slot did not resolve, for the diagnostic row. Empty on a resolved piece.
	reason string
}

// key is the identity the cell's minimum is keyed by. tech_card_piece.line_key is the stable one; a
// legacy keyless piece gets a synthetic id-based key so that TWO keyless pieces cannot collapse into
// one entry and quietly leave the minimum.
func (r requiredPiece) key() string {
	if r.piece.LineKey != "" {
		return r.piece.LineKey
	}
	return fmt.Sprintf("piece#%d", r.piece.Id)
}

func (r requiredPiece) label() string {
	if r.piece.Name != "" {
		return r.piece.Name
	}
	return r.key()
}

// requiredPiecesForColorway is §6.2 step 4's `required(C)`: every cut-piece of the card whose slot
// for C falls into planSlotSections — the EXISTING definition from production_material_plan.go, not
// a second one.
//
// A piece is dropped from the list in EXACTLY ONE case: its slot resolved and that slot is outside
// planSlotSections (label/packaging/other — those ride the assembly and packaging recipes, and a cut
// piece bound to one is not a garment part the cloth plan counts). Every other outcome — no binding
// for this colourway, a binding that does not resolve to a line — keeps the piece IN the list as
// unresolved, i.e. as an UNKNOWN the cell has to carry.
func requiredPiecesForColorway(card *entity.TechCard, colorwayID int) []requiredPiece {
	out := make([]requiredPiece, 0, len(card.Pieces))
	for i := range card.Pieces {
		p := &card.Pieces[i]
		m := pieceMaterialForColorway(p, colorwayID)
		if m == nil {
			out = append(out, requiredPiece{
				piece:  p,
				reason: "деталь не привязана к слоту BOM у этого колорвея — неизвестно, какой настил обязан её кроить",
			})
			continue
		}
		bom := pieceSlotBomLine(m, card.BomItems)
		if bom == nil {
			out = append(out, requiredPiece{
				piece:  p,
				reason: "ссылка детали на слот BOM не резолвится — неизвестно, какой настил обязан её кроить",
			})
			continue
		}
		if !planSlotSections[bom.Section] {
			continue
		}
		out = append(out, requiredPiece{
			piece:     p,
			bomItemID: int64(bom.Id),
			bomName:   bom.Name,
			resolved:  true,
		})
	}
	return out
}

// --- КЛЕТКА: НИ ОДНА ОБЯЗАТЕЛЬНАЯ ДЕТАЛЬ НЕ ТЕРЯЕТСЯ ---------------------------------------------

// coverageCellBuilder ties the REQUIRED SET to the cell so that a piece the fill loop forgets cannot
// disappear from the minimum.
//
// ЭТО ГЛАВНАЯ ГАРАНТИЯ ЭТОГО ФАЙЛА, и она структурная, а не дисциплинарная. CoverageCell.Add
// вызывается НЕ в цикле заполнения, а в finish() — по списку обязательных деталей. Деталь, за
// которую никто не ответил, попадает в клетку как UNKNOWN (нулевое значение PieceYield) и уносит с
// собой оговорку с именем. Молча выпасть она не может: цикл заполнения физически не тот цикл,
// который кормит клетку. Тип бы этого не поймал — автор production_lay_yield.go назвал это
// оставшейся дырой прямо в комментарии к CoverageCell.
type coverageCellBuilder struct {
	required   []requiredPiece
	answers    []cellAnswer
	answered   []bool
	cell       *CoverageCell
	plannedQty int
	// missing names the pieces nobody answered for. Non-empty means a bug in the fill loop, and it
	// travels into the caveats rather than into a log line nobody reads.
	missing []string
}

// cellAnswer is one piece's verdict plus the numbers behind it, kept for the diagnostic row.
type cellAnswer struct {
	yield  PieceYield
	cut    MarkerPieceCounts
	detail string
}

func newCoverageCellBuilder(plannedQty int, required []requiredPiece) *coverageCellBuilder {
	return &coverageCellBuilder{
		required:   required,
		answers:    make([]cellAnswer, len(required)),
		answered:   make([]bool, len(required)),
		cell:       NewCoverageCell(plannedQty),
		plannedQty: plannedQty,
	}
}

// answer records the verdict for the i-th required piece. Indexed rather than keyed: two pieces of
// one card can share a name and a legacy row can share an empty line_key, and a map would silently
// merge them into one voice in the minimum.
func (b *coverageCellBuilder) answer(i int, a cellAnswer) {
	if i < 0 || i >= len(b.answers) {
		return
	}
	b.answers[i] = a
	b.answered[i] = true
}

// finish feeds EVERY required piece into the cell — answered or not — and reads the verdict off it.
func (b *coverageCellBuilder) finish() *CoverageCell {
	for i := range b.required {
		if !b.answered[i] {
			b.answers[i] = cellAnswer{detail: "деталь не была посчитана при наполнении клетки"}
			b.missing = append(b.missing, b.required[i].label())
		}
		b.cell.Add(b.required[i].key(), b.answers[i].yield)
	}
	return b.cell
}

// --- СБОРКА -------------------------------------------------------------------------------------

type layPairKey struct {
	colorwayID int
	bomItemID  int64
}

type gridCell struct {
	colorwayID int
	sizeID     int
	plannedQty int
}

type layCoverageBuilder struct {
	card       *entity.TechCard
	laysByPair map[layPairKey][]Lay
	// slotsWith is colourway → the slots that have at least one настил. It is what §6.4's `source`
	// reads, and it is kept separately from laysByPair so a colourway with a BROKEN lay does not
	// count as «слот настелен».
	slotsWith map[int]map[int64]bool
	// brokenLay is colourway → the name of a настил whose slot is gone from the BOM. Its cut cloth
	// exists and cannot be attributed to any slot, which is why it makes shortages UNPROVABLE rather
	// than proving them (see pieceAnswer).
	brokenLay  map[int]string
	required   map[int][]requiredPiece
	caveats    []string
	seenCaveat map[string]bool
}

func (b *layCoverageBuilder) addCaveat(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	if b.seenCaveat[s] {
		// The same caveat is reached once per size and once per cell; a run with six sizes would
		// otherwise repeat one sentence six times.
		return
	}
	b.seenCaveat[s] = true
	b.caveats = append(b.caveats, s)
}

func (b *layCoverageBuilder) sortedCaveats() []string {
	out := append([]string(nil), b.caveats...)
	sort.Strings(out)
	return out
}

func (b *layCoverageBuilder) indexLays(lays []Lay) {
	for _, l := range lays {
		if l.BomItemID <= 0 {
			// §6.3: слот удалён ⇒ вклад настила НЕВЫРАЗИМ. Он не кладётся ни в одну пару, и его
			// колорвей помечается — иначе ткань, которую он раскроил, просто исчезнет из счёта.
			name := l.Name
			if name == "" {
				name = l.LayKey
			}
			if _, seen := b.brokenLay[l.ColorwayID]; !seen {
				b.brokenLay[l.ColorwayID] = name
			}
			b.addCaveat("настил %q потерял слот BOM: его раскрой не привязывается ни к одной ткани, покрытие этого колорвея не доказуемо", name)
			continue
		}
		k := layPairKey{colorwayID: l.ColorwayID, bomItemID: l.BomItemID}
		b.laysByPair[k] = append(b.laysByPair[k], l)
		if b.slotsWith[l.ColorwayID] == nil {
			b.slotsWith[l.ColorwayID] = make(map[int64]bool)
		}
		b.slotsWith[l.ColorwayID][l.BomItemID] = true
	}
}

// grid turns the run's lines into the cells to judge, and REFUSES OUT LOUD the ones no настил can
// ever cover (§14 п.4). Duplicate (колорвей, размер) lines are summed: the cell is keyed by the
// pair, and two lines of one pair are one quantity to cover.
func (b *layCoverageBuilder) grid(lines []entity.ProductionRunLine, out *LayCoverage) []gridCell {
	byPair := make(map[gridCell]int)
	var order []gridCell
	for _, l := range lines {
		if !l.ProductId.Valid || l.ProductId.Int32 <= 0 {
			out.Findings = append(out.Findings, LayCoverageFinding{
				Key:     LayCoverageFindingKeyLineWithoutColorway,
				LineKey: l.LineKey,
				SizeID:  l.SizeId,
				Detail:  "строка прогона без колорвея — настил принадлежит паре (колорвей, слот), поэтому покрытие такой строки не считается",
			})
			continue
		}
		if l.SizeId <= 0 {
			out.Findings = append(out.Findings, LayCoverageFinding{
				Key:     LayCoverageFindingKeyLineWithoutSize,
				LineKey: l.LineKey,
				SizeID:  l.SizeId,
				Detail:  "строка прогона без размера — состав маркера ключуется размером, поэтому покрытие такой строки не считается",
			})
			continue
		}
		if l.PlannedQty <= 0 {
			continue
		}
		k := gridCell{colorwayID: int(l.ProductId.Int32), sizeID: l.SizeId}
		if _, seen := byPair[k]; !seen {
			order = append(order, k)
		}
		byPair[k] += l.PlannedQty
	}
	cells := make([]gridCell, 0, len(order))
	for _, k := range order {
		cells = append(cells, gridCell{colorwayID: k.colorwayID, sizeID: k.sizeID, plannedQty: byPair[k]})
	}
	sort.SliceStable(cells, func(i, j int) bool {
		if cells[i].colorwayID != cells[j].colorwayID {
			return cells[i].colorwayID < cells[j].colorwayID
		}
		return cells[i].sizeID < cells[j].sizeID
	})
	return cells
}

func (b *layCoverageBuilder) requiredFor(colorwayID int) []requiredPiece {
	if r, ok := b.required[colorwayID]; ok {
		return r
	}
	r := requiredPiecesForColorway(b.card, colorwayID)
	b.required[colorwayID] = r
	return r
}

// cell computes one (колорвей, размер) cell and its diagnostic rows.
func (b *layCoverageBuilder) cell(colorwayID, sizeID, plannedQty int) (LayCoverageCell, []LayCoveragePieceYield) {
	required := b.requiredFor(colorwayID)
	cb := newCoverageCellBuilder(plannedQty, required)
	for i := range required {
		cb.answer(i, b.pieceAnswer(required[i], colorwayID, sizeID, plannedQty))
	}
	cell := cb.finish()
	for _, name := range cb.missing {
		b.addCaveat("деталь %q не была посчитана при наполнении клетки — покрытие считается неизвестным по этой детали", name)
	}

	out := LayCoverageCell{
		ColorwayID:        colorwayID,
		SizeID:            sizeID,
		PlannedQty:        plannedQty,
		CoveredQty:        cell.CoveredQty(),
		Status:            cell.Status(),
		UnknownPieceCount: cell.UnknownPieceCount(),
	}
	out.Source, out.SlotsWithoutLays = b.source(colorwayID, required)

	byKey := make(map[string]requiredPiece, len(required))
	for _, rp := range required {
		byKey[rp.key()] = rp
	}
	seenSlot := make(map[int64]bool)
	seenName := make(map[string]bool)
	for _, k := range cell.BlockingPieceKeys() {
		rp, ok := byKey[k]
		if !ok {
			continue
		}
		if rp.bomItemID > 0 && !seenSlot[rp.bomItemID] {
			seenSlot[rp.bomItemID] = true
			out.BlockingBomItemIDs = append(out.BlockingBomItemIDs, rp.bomItemID)
		}
		if n := rp.label(); !seenName[n] {
			seenName[n] = true
			out.BlockingPieceNames = append(out.BlockingPieceNames, n)
		}
	}
	sort.Slice(out.BlockingBomItemIDs, func(i, j int) bool { return out.BlockingBomItemIDs[i] < out.BlockingBomItemIDs[j] })
	sort.Strings(out.BlockingPieceNames)

	yields := make([]LayCoveragePieceYield, 0, len(required))
	for i := range required {
		rp := required[i]
		a := cb.answers[i]
		y := LayCoveragePieceYield{
			ColorwayID:         colorwayID,
			SizeID:             sizeID,
			PieceLineKey:       rp.piece.LineKey,
			PieceName:          rp.label(),
			BomItemID:          rp.bomItemID,
			RequiredPerGarment: rp.piece.PiecesPerGarment,
			CutAsDrawn:         a.cut.AsDrawn,
			CutMirrored:        a.cut.Mirrored,
			Detail:             a.detail,
		}
		switch {
		case !a.yield.Known:
			y.Status = CoverageStatusUnknown
		case a.yield.Garments < plannedQty:
			y.Status = CoverageStatusBlocker
			y.GarmentYield = a.yield.Garments
		default:
			y.Status = CoverageStatusOK
			y.GarmentYield = a.yield.Garments
		}
		// «Выкроено сверх нужного на covered_qty изделий» — ровно как написано в проте. Зеркальные
		// экземпляры `identical`-детали (PieceYield.Overcut) входят сюда как частный случай: они
		// выкроены и не стали ни одним изделием.
		if a.yield.Known && rp.piece.PiecesPerGarment > 0 {
			if over := a.cut.Total() - out.CoveredQty*rp.piece.PiecesPerGarment; over > 0 {
				y.OvercutQty = over
			}
		}
		yields = append(yields, y)
	}
	sort.SliceStable(yields, func(i, j int) bool { return yields[i].PieceName < yields[j].PieceName })
	return out, yields
}

// source is §6.4's signature for one cell, plus the slots that have to fall back to the norm.
func (b *layCoverageBuilder) source(colorwayID int, required []requiredPiece) (pb_admin.ProductionRunCoverageSource, []int64) {
	slots := make(map[int64]bool)
	for _, rp := range required {
		if rp.resolved && rp.bomItemID > 0 {
			slots[rp.bomItemID] = true
		}
	}
	var without []int64
	with := 0
	for id := range slots {
		if b.slotsWith[colorwayID][id] {
			with++
			continue
		}
		without = append(without, id)
	}
	sort.Slice(without, func(i, j int) bool { return without[i] < without[j] })
	switch {
	case with == 0:
		// Ни одного настила: настильного ответа нет вовсе, и источник остаётся нормой. Клетка при
		// этом честно BLOCKER (§6.3, первая строка) — «ткань не раскроена» это факт, а не пробел.
		return pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, without
	case len(without) == 0:
		return pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, nil
	default:
		return pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED, without
	}
}

// pieceAnswer is §6.2 steps 1-3 for ONE required piece at ONE size, summed over the настилы of its
// pair. Every «не могу сказать» on the way returns an UNKNOWN yield rather than a smaller number.
func (b *layCoverageBuilder) pieceAnswer(rp requiredPiece, colorwayID, sizeID, plannedQty int) cellAnswer {
	if !rp.resolved {
		// §6.3: слот детали не резолвится для C ⇒ UNKNOWN.
		return cellAnswer{detail: rp.reason}
	}

	lays := b.laysByPair[layPairKey{colorwayID: colorwayID, bomItemID: rp.bomItemID}]
	var cut MarkerPieceCounts
	// chiralityKnown is ANDed over every section CONSULTED, and starts true so that a pair with no
	// настилы at all answers an honest zero rather than an UNKNOWN: nothing was cut, and no evidence
	// about `flipped` is needed to say that both hands are zero.
	chiralityKnown := true
	unknown := ""
	for _, l := range lays {
		for _, s := range l.Sections {
			if s.YieldErr != nil {
				unknown = fmt.Sprintf("раскладка маркера %d не читается (%v) — что она выкроила, сказать нечем", s.MarkerID, s.YieldErr)
				continue
			}
			chiralityKnown = chiralityKnown && s.Yield.ChiralityKnown()
			inst := s.Yield.PerLayerInstances(rp.piece.LineKey, sizeID)
			if !inst.Known {
				// Неатрибутируемый блоб или блоб без состава. Сумма стала бы полом, а пол,
				// прочитанный как факт, — это выдуманная нехватка.
				unknown = fmt.Sprintf("маркер %d не может отнести свои детали к карточке или не знает своего состава", s.MarkerID)
				continue
			}
			for _, c := range inst.Caveats {
				b.addCaveat("%s", c)
			}
			cutBySection, err := LayerCutInstances(inst.Counts, l.Mode, s.Plies)
			if err != nil {
				if errors.Is(err, ErrLayModeParity) {
					// §6.2 шаг 2: секция не вносит НИЧЕГО. Это доказанный ноль, а не пробел —
					// последний непарный слой отдаёт только одну хиральность. Сам вердикт «настил
					// негоден» ставит проверка lay_mode_parity (§8), здесь остаётся арифметика.
					b.addCaveat("настил %q: секция маркера %d нечётная в режиме «лицом к лицу» и не вносит в покрытие ничего", layLabel(l), s.MarkerID)
					continue
				}
				unknown = fmt.Sprintf("секция маркера %d: %v", s.MarkerID, err)
				continue
			}
			cut.AsDrawn += cutBySection.AsDrawn
			cut.Mirrored += cutBySection.Mirrored
		}
	}
	if unknown != "" {
		return cellAnswer{cut: cut, detail: unknown}
	}

	y := PieceYieldGarments(cut, rp.piece.CutSymmetry, rp.piece.PiecesPerGarment, chiralityKnown)
	detail := ""
	if !y.Known {
		switch {
		case !rp.piece.CutSymmetry.Valid:
			detail = "у детали не размечено, как она кроится (cut_symmetry) — сколько изделий из неё выйдет, сказать нечем"
		case !chiralityKnown:
			detail = "раскладка старше схемы 3: зеркальность размещений в ней не записана, и «нет зеркальных» не доказательство"
		default:
			detail = "деталь не даёт вычислимого выхода изделий"
		}
	}

	// НЕАТРИБУТИРУЕМЫЙ РАСКРОЙ. Настил, потерявший слот, кроил ЧТО-ТО этого колорвея, и что именно —
	// неизвестно. Покрытие монотонно по числу выкроенных экземпляров, поэтому доказанная ДОСТАТОЧНОСТЬ
	// остаётся достаточностью (лишняя ткань не может уменьшить выход), а доказанная НЕХВАТКА перестаёт
	// быть доказанной: недостающие экземпляры могли лежать именно на том настиле. §6.3 называет
	// результат UNKNOWN, и это ровно он.
	if name, broken := b.brokenLay[colorwayID]; broken && y.Known && y.Garments < plannedQty {
		return cellAnswer{
			cut:    cut,
			detail: fmt.Sprintf("нехватка не доказуема: у колорвея есть настил %q, потерявший слот BOM, и его раскрой мог включать эту деталь", name),
		}
	}
	return cellAnswer{yield: y, cut: cut, detail: detail}
}

func layLabel(l Lay) string {
	if l.Name != "" {
		return l.Name
	}
	return l.LayKey
}

// --- ОТДАЧА (§6.4) ------------------------------------------------------------------------------

// pbLayCheckStatus maps a coverage verdict onto the wire's four-valued status. WARNING is
// unreachable by construction and the proto says so: «OK | BLOCKER | UNKNOWN — WARNING здесь не
// бывает».
func pbLayCheckStatus(s CoverageStatus) pb_common.ProductionLayCheckStatus {
	switch s {
	case CoverageStatusOK:
		return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK
	case CoverageStatusBlocker:
		return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_BLOCKER
	}
	return pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_UNKNOWN
}

// CoverageCellsPb is the Ф4 half of §6.4: the run detail page's grid.
func (c LayCoverage) CoverageCellsPb() []*pb_common.ProductionRunLayCoverageCell {
	out := make([]*pb_common.ProductionRunLayCoverageCell, 0, len(c.Cells))
	for _, cell := range c.Cells {
		out = append(out, &pb_common.ProductionRunLayCoverageCell{
			ColorwayId:         int32(cell.ColorwayID),
			SizeId:             int32(cell.SizeID),
			PlannedQty:         int32(cell.PlannedQty),
			CoveredQty:         int32(cell.CoveredQty),
			Status:             pbLayCheckStatus(cell.Status),
			BlockingBomItemIds: append([]int64(nil), cell.BlockingBomItemIDs...),
			BlockingPieceNames: append([]string(nil), cell.BlockingPieceNames...),
			UnknownPieceCount:  int32(cell.UnknownPieceCount),
		})
	}
	return out
}

// PieceYieldsPb is the cell popover's diagnostics.
func (c LayCoverage) PieceYieldsPb() []*pb_common.ProductionRunLayPieceYield {
	out := make([]*pb_common.ProductionRunLayPieceYield, 0, len(c.PieceYields))
	for _, y := range c.PieceYields {
		out = append(out, &pb_common.ProductionRunLayPieceYield{
			ColorwayId:         int32(y.ColorwayID),
			SizeId:             int32(y.SizeID),
			PieceLineKey:       y.PieceLineKey,
			PieceName:          y.PieceName,
			BomItemId:          y.BomItemID,
			RequiredPerGarment: int32(y.RequiredPerGarment),
			CutAsDrawn:         int32(y.CutAsDrawn),
			CutMirrored:        int32(y.CutMirrored),
			GarmentYield:       int32(y.GarmentYield),
			OvercutQty:         int32(y.OvercutQty),
			Status:             pbLayCheckStatus(y.Status),
			Detail:             y.Detail,
		})
	}
	return out
}

// ApplyToUnitCoverage is the Ф6 half of §6.4, executed to the letter and WITHOUT touching the shape
// of the message:
//
//	colorway_id, size_id, planned_qty  — as they are (the gate already keyed the row)
//	provisioned_qty                    ← covered_qty
//	units_from_stock                   — UNTOUCHED; it stays the stock estimate against the norm
//	source                             ← LAYS для пар с настилами, NORM иначе (MIXED когда часть)
//	blocking_bom_item_ids              ← слоты деталей, давших минимум
//
// A row whose colourway has NO настил at all is left exactly as the gate computed it: «источник
// NORM» is not a number this file may overwrite, and overwriting it would turn the creation modal's
// estimate into a zero the moment one lay appeared anywhere in the run.
//
// Returns how many rows were switched to the настильный ответ — the number a test pins and the
// caller logs.
func (c LayCoverage) ApplyToUnitCoverage(rows []*pb_admin.ProductionRunReadinessUnitCoverage) int {
	if !c.Applicable || len(rows) == 0 {
		return 0
	}
	type k struct{ colorway, size int32 }
	byCell := make(map[k]LayCoverageCell, len(c.Cells))
	for _, cell := range c.Cells {
		byCell[k{colorway: int32(cell.ColorwayID), size: int32(cell.SizeID)}] = cell
	}
	switched := 0
	for _, row := range rows {
		if row == nil {
			continue
		}
		cell, ok := byCell[k{colorway: row.GetColorwayId(), size: row.GetSizeId()}]
		if !ok {
			continue
		}
		if cell.Source != pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS &&
			cell.Source != pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED {
			continue
		}
		row.ProvisionedQty = int32(cell.CoveredQty)
		row.Source = cell.Source
		row.BlockingBomItemIds = append([]int64(nil), cell.BlockingBomItemIDs...)
		switched++
	}
	return switched
}
