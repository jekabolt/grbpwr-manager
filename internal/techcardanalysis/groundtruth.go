package techcardanalysis

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── GROUND TRUTH (design §1) ────────────────────────────────────────────────────────────────────
//
// ЭТО ПЕРЕСЧЁТ, А НЕ ПОИСК ДЕФЕКТОВ. Путь записи гоняет canonicalizeAssembly на КАЖДОМ сохранении и
// отвергает payload, нарушающий правила 1–3 и 5–7: циклы, ссылки вперёд, дубли-производители,
// двойное потребление и коллизия пространства имён В СОХРАНЁННОЙ КАРТОЧКЕ НЕПРЕДСТАВИМЫ. Код,
// который ищет их здесь, — мёртвая проверка, которая никогда не сработает и которую никто никогда
// не удалит, потому что «а вдруг».
//
// Тогда зачем пересчёт. Записи не доверяем НА СЛОВО: легаси-строки старше фичи и любая out-of-band
// запись мимо конвертера существуют как класс. Пересчёт стоит копейки и на той же структуре даёт
// три вещи, которые нужны дальше по течению:
//
//  1. VERIFIED FACTS промпта (§7.2): терминал, покрытие деталей, классификация шагов;
//  2. проверку C6 — правило 4 включается только на релизе, поэтому на черновике «терминалов ≠ 1»
//     и «деталь не потреблена» это ЕДИНСТВЕННЫЕ подлинно находимые топологические факты, и они же
//     предсказание отказа релиза;
//  3. честность п.1: список Violations непуст ровно тогда, когда карточка НЕ проходила конвертер,
//     и утверждать «граф чист» в промпте в этот момент нельзя.
//
// Движок — entity.AssemblySweep, тот же самый, которым запись принимает решение. Второй проход
// собственного сочинения разошёлся бы с ним молча, и разошёлся бы именно на поглощении.

// StepKind is §1's vocabulary of what a step does to the table. Оно обязано быть одним и тем же в
// коде и в промпте: модель судит маршрут этими же тремя словами.
type StepKind uint8

const (
	// StepProcessing — ОБРАБОТКА: пустой output_unit_key. Шаг ничего не собирает, его входы
	// остаются на фронтире и достаются следующим шагам. На карточке 8 таких 9 из 48.
	StepProcessing StepKind = iota
	// StepJoin — ДЖОЙН: шаг с непустым выходом, рождающий НОВЫЙ узел; съедает свои входы.
	StepJoin
	// StepAbsorbing — ПОГЛОЩЕНИЕ: джойн, чей выход совпадает с одним из его же входов-узлов.
	// Узел остаётся собой и получает содержимое (оп 260 карточки 8 догружает в «pocket base»
	// деталь PCK_MAIN_INS_S). ЛЕГАЛЬНАЯ ЦЕПОЧКА, а не дубль-производитель — и назвать её дублем
	// значило бы предложить технологу «починить» единственно верную разметку.
	StepAbsorbing
)

// String renders the kind for prompts and messages.
func (k StepKind) String() string {
	switch k {
	case StepJoin:
		return "join"
	case StepAbsorbing:
		return "absorbing"
	default:
		return "processing"
	}
}

// StepFact is one operation as the recomputation sees it.
type StepFact struct {
	// Index — индекс шага В КАНОНИЧЕСКОМ ПОРЯДКЕ (entity.AssemblyOperationOrder), он же индекс, по
	// которому адресуются AssemblyResult.FrontierBefore и AssemblyUnit.ProducedAt.
	Index int
	// CardIndex — индекс той же операции в card.Operations. Канонический порядок и порядок среза
	// совпадают на всякой карточке, записанной после 0307, но легаси-строка с NULL-номером их
	// разводит, и читателю нужны оба.
	CardIndex int
	// OperationNumber — номер шага; NumberValid=false у легаси-строки без номера (якоря у неё нет).
	OperationNumber int32
	NumberValid     bool
	// Kind — джойн / поглощение / обработка.
	Kind StepKind
	// OutputUnitKey — сырой ключ производимого узла; пуст у обработки.
	OutputUnitKey string
}

// GroundTruth is the recomputed state of the card's assembly graph.
type GroundTruth struct {
	// Steps — шаги в каноническом порядке.
	Steps []StepFact
	// Frontier — что осталось на столе после последнего шага, в порядке появления: и узлы, и не
	// съеденные детали.
	Frontier []string
	// Terminals — ключи УЗЛОВ, оставшихся на фронтире. Ровно один — то, чего требует релиз.
	Terminals []string
	// Units — узлы по ключу, как их увидел проход (имя, первый производитель, поглощающие шаги,
	// замыкание по деталям).
	Units map[string]entity.AssemblyUnit
	// ProducerOf — ключ узла → индекс шага, ПЕРВЫМ его произведшего (карта «узлы → производитель»).
	// Поглощающие шаги эту точку не двигают: имя узла разрешается по первому производителю.
	ProducerOf map[string]int
	// ConsumedBy — ключ (детали ИЛИ узла) → индекс шага, который его съел. Покрытие деталей — это
	// он, суженный на детали (см. PieceConsumedBy).
	ConsumedBy map[string]int
	// PieceConsumedBy — line_key детали → индекс съевшего её шага. Отдельной картой, потому что
	// «какая деталь кем потреблена» — вопрос, который задают именно про детали.
	PieceConsumedBy map[string]int
	// UnconsumedPieces — line_key объявленных деталей, которых не съел ни один джойн, в порядке
	// объявления деталей (в том же, в каком они лежат на вкладке).
	UnconsumedPieces []string
	// UnreachedPieces — line_key деталей, не попавших в ЗАМЫКАНИЕ единственного терминала. Это НЕ
	// то же, что UnconsumedPieces: деталь может быть съедена джойном, который никуда не сходится.
	// Считается ТОЛЬКО при ровно одном терминале — ровно как в entity.AssemblyReleaseCheck, чей
	// отказ этот список предсказывает. При другом числе терминалов nil, и это не «их нет», а «на
	// этот вопрос ещё не отвечают»: при двух терминалах ни одна деталь формально не достигает
	// изделия, и список из сорока имён утопил бы настоящую причину.
	UnreachedPieces []string
	// Marked — карточка несёт хотя бы один ПРОИЗВОДЯЩИЙ шаг. Условие включения правила 4 (и,
	// значит, C6): неразмеченная карточка проходит вакуумно и релизится ровно как раньше.
	Marked bool
	// ProcessingCount / JoinCount / AbsorbingCount — счётчики классификации для VERIFIED FACTS.
	ProcessingCount int
	JoinCount       int
	AbsorbingCount  int
	// Violations — нарушения, которые проход всё-таки увидел. НЕ ИСТОЧНИК НАХОДОК (§2: «циклы,
	// ссылки вперёд, дубли-производители, двойное потребление — никто»): в сохранённой карточке их
	// не бывает, потому что запись их не принимает.
	//
	// Список существует ради ЧЕСТНОСТИ ПРОМПТА. VERIFIED FACTS утверждают «граф ацикличен, ссылок
	// вперёд и висячих ссылок нет» — утверждение закрытого мира, которое модели запрещено
	// оспаривать. На карточке, записанной мимо конвертера, оно было бы ЛОЖЬЮ, поданной как факт.
	// Читатель, собирающий блок фактов, обязан посмотреть сюда прежде, чем это напечатать.
	Violations []entity.AssemblyViolation
}

// TerminalCount is the number of terminal units — the number release rule 4 requires to be one.
func (g GroundTruth) TerminalCount() int { return len(g.Terminals) }

// StepByNumber finds a step fact by its operation number.
func (g GroundTruth) StepByNumber(number int32) (StepFact, bool) {
	for _, s := range g.Steps {
		if s.NumberValid && s.OperationNumber == number {
			return s, true
		}
	}
	return StepFact{}, false
}

// ComputeGroundTruth recomputes the card's assembly facts. A nil card yields an empty, usable value
// — every map is non-nil, so a caller never has to nil-check before a lookup.
func ComputeGroundTruth(card *entity.TechCard) GroundTruth {
	gt := GroundTruth{
		Units:           map[string]entity.AssemblyUnit{},
		ProducerOf:      map[string]int{},
		ConsumedBy:      map[string]int{},
		PieceConsumedBy: map[string]int{},
	}
	if card == nil || len(card.Operations) == 0 {
		return gt
	}

	// Порядок шагов берётся ТОЛЬКО у entity.AssemblyOperationOrder — единственной функции порядка.
	// Своя сортировка здесь разошлась бы с тем, что утвердила запись, на первой же легаси-карточке
	// с неканоническим номером.
	order := entity.AssemblyOperationOrder(card.Operations)

	pieces := make([]entity.AssemblyPiece, 0, len(card.Pieces))
	pieceOrder := make(map[string]int, len(card.Pieces))
	for i := range card.Pieces {
		p := &card.Pieces[i]
		if p.LineKey == "" {
			continue
		}
		if _, dup := pieceOrder[p.LineKey]; dup {
			continue
		}
		pieceOrder[p.LineKey] = len(pieces)
		pieces = append(pieces, entity.AssemblyPiece{LineKey: p.LineKey, Name: p.Name})
	}

	steps := make([]entity.AssemblyStep, len(order))
	gt.Steps = make([]StepFact, len(order))
	for i, cardIdx := range order {
		op := &card.Operations[cardIdx]
		steps[i] = entity.AssemblyStep{
			Inputs:         op.AssemblyInputs,
			OutputUnitKey:  op.OutputUnitKey.String,
			OutputUnitName: op.OutputUnitName.String,
		}
		gt.Steps[i] = StepFact{
			Index:           i,
			CardIndex:       cardIdx,
			OperationNumber: op.OperationNumber.Int32,
			NumberValid:     op.OperationNumber.Valid,
			OutputUnitKey:   op.OutputUnitKey.String,
		}
		if steps[i].OutputUnitKey != "" {
			gt.Marked = true
		}
	}

	res := entity.AssemblySweep(pieces, steps)
	gt.Frontier = res.Frontier
	gt.Units = res.Units
	gt.Violations = res.Violations
	gt.ConsumedBy = res.ConsumedBy

	// Поглощение опознаётся ПО ПРОХОДУ, а не по «выход есть среди входов»: проход учитывает ещё и
	// то, что узел к этому моменту существует, и повторить это условие второй формулой значило бы
	// завести второе определение поглощения — то самое, которым прототип съедал вход молча.
	absorbing := make(map[int]bool, len(res.Units))
	for key, u := range res.Units {
		gt.ProducerOf[key] = u.ProducedAt
		for _, at := range u.AbsorbedAt {
			absorbing[at] = true
		}
	}
	for i := range gt.Steps {
		switch {
		case gt.Steps[i].OutputUnitKey == "":
			gt.Steps[i].Kind = StepProcessing
			gt.ProcessingCount++
		case absorbing[i]:
			gt.Steps[i].Kind = StepAbsorbing
			gt.AbsorbingCount++
		default:
			gt.Steps[i].Kind = StepJoin
			gt.JoinCount++
		}
	}

	for _, key := range res.Frontier {
		if _, isUnit := res.Units[key]; isUnit {
			gt.Terminals = append(gt.Terminals, key)
		}
	}

	for _, p := range pieces {
		if at, eaten := res.ConsumedBy[p.LineKey]; eaten {
			gt.PieceConsumedBy[p.LineKey] = at
			continue
		}
		gt.UnconsumedPieces = append(gt.UnconsumedPieces, p.LineKey)
	}

	if len(gt.Terminals) == 1 {
		reached := make(map[string]bool, len(pieces))
		for _, leaf := range res.Units[gt.Terminals[0]].Leaves {
			reached[leaf] = true
		}
		for _, p := range pieces {
			if !reached[p.LineKey] {
				gt.UnreachedPieces = append(gt.UnreachedPieces, p.LineKey)
			}
		}
	}

	return gt
}

// PieceScopeKeys resolves every cut piece of the card to the ТКАНЬ it is cut from — the fabric
// bucket §7.2 prints beside the piece — keyed by piece line_key. Пустого значения в карте не бывает:
// деталь, у которой ткани не назначено, в карту просто не попадает, и «не назначено» читается как
// отсутствие ключа, а не как пустая строка, неотличимая от пустого назначения.
//
// ДВА ИСТОЧНИКА, И ОБА НАСТОЯЩИЕ. Привязка «деталь → ткань» живёт в двух местах, и они не
// эквивалентны по употреблению: tech_card_piece_material (вкладка деталей кроя) и строка рецепта
// колорвея с piece_id (кнопка «добавить материал к детали») — на практике основной путь. Читать
// только одно значит на половине живых карточек ответить «ткани нет» там, где она назначена всем
// деталям; ровно этим болел первый расчёт «по выкройкам».
//
// Ключ ведра считает entity.FabricScopeIdentity — ЛИЧНОСТЬ ТКАНИ (назначение той строки BOM, на
// которую деталь смотрит сегодня), а не ведро записи. Материализовать её нельзя: она функция
// сегодняшнего BOM.
func PieceScopeKeys(card *entity.TechCard) map[string]string {
	out := map[string]string{}
	if card == nil {
		return out
	}
	lines := entity.RollGoodsLinesOfBom(card.BomItems)

	bomByID := make(map[int64]*entity.TechCardBomItem, len(card.BomItems))
	bomByKey := make(map[string]*entity.TechCardBomItem, len(card.BomItems))
	for i := range card.BomItems {
		b := &card.BomItems[i]
		if b.Id != 0 {
			bomByID[int64(b.Id)] = b
		}
		if b.LineKey != "" {
			bomByKey[b.LineKey] = b
		}
	}
	pieceKeyByID := make(map[int64]string, len(card.Pieces))
	for i := range card.Pieces {
		p := &card.Pieces[i]
		if p.Id != 0 && p.LineKey != "" {
			pieceKeyByID[int64(p.Id)] = p.LineKey
		}
	}

	assign := func(pieceKey string, bom *entity.TechCardBomItem) {
		if pieceKey == "" || bom == nil || !entity.IsRollGoodsSection(bom.Section) {
			return
		}
		if _, taken := out[pieceKey]; taken {
			return // первая назвавшая ткань выигрывает: две ткани у детали — вопрос не к этой карте
		}
		if key := entity.FabricScopeIdentity(bom.Purpose.String, bom.LineKey, lines); key != "" {
			out[pieceKey] = key
		}
	}

	// 1. Заявление самой детали (tech_card_piece_material).
	for i := range card.Pieces {
		p := &card.Pieces[i]
		for j := range p.Materials {
			m := &p.Materials[j]
			switch {
			case m.BomItemId.Valid:
				assign(p.LineKey, bomByID[m.BomItemId.Int64])
			case m.BomLineKey != "":
				assign(p.LineKey, bomByKey[m.BomLineKey])
			}
		}
	}
	// 2. Строка рецепта колорвея с piece_id — на практике основной путь.
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		for j := range cw.Usages {
			u := &cw.Usages[j]
			pieceKey := u.PieceLineKey
			if pieceKey == "" && u.PieceId.Valid {
				pieceKey = pieceKeyByID[u.PieceId.Int64]
			}
			switch {
			case u.BomItemId.Valid:
				assign(pieceKey, bomByID[u.BomItemId.Int64])
			case u.BomLineKey != "":
				assign(pieceKey, bomByKey[u.BomLineKey])
			}
		}
	}
	return out
}
