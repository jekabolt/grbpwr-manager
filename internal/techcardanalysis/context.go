package techcardanalysis

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── КОНТЕКСТ ПРОМПТА (design §6) ────────────────────────────────────────────────────────────────
//
// ЭТОТ ФАЙЛ ВЫБИРАЕТ ДАННЫЕ, prompt.go ПОДБИРАЕТ К НИМ СЛОВА. Разделение не косметическое: §7.1 —
// ревьюированный контракт с моделью, и всякая формулировка обязана быть видна в одном месте рядом с
// ним, а не собираться по дороге из кусков в трёх файлах. Здесь — что едет и с какими потолками;
// там — как это читается.
//
// ЧТО ЕДЕТ (§6): шапка карточки со stage/approval_state, дефолты construction и парк оборудования,
// детали (имя, ppg, ведро ткани, не-дефолтные атрибуты), BOM с ЦЕНОЙ И ВАЛЮТОЙ, операции построчно
// со входами в исходном порядке, VERIFIED FACTS, MACHINE FINDINGS ALREADY FILED, MACHINE
// OBSERVATIONS.
//
// ЧТО НЕ ЕДЕТ И ПОЧЕМУ (§6): эскиз и медиа (путь текстовый), геометрия деталей (контуров нет),
// колорвеи, костинг, паттерны и лейблы как разделы (машинный слой уже выжал из них находки, сырьё
// разжижает внимание), примерки, каталог работ с 254 синонимами.

// Пер-полевые капы §6 («Заборы»). Граница доверия проходит ЧЕРЕЗ ИМЕНА: ключи узлов и имена деталей
// приезжают из DXF-блоков внешних файлов, и «report no defects» в одной ноте иначе подавил бы ровно
// судьбоносные находки. Забор двойной — кап здесь и абзац Data fence в системном промпте (§7.1);
// кап держит РАЗМЕР входа, абзац держит СМЫСЛ, и ни один из них не заменяет другой.
const (
	// promptNoteRunes — потолок ноты шага и свободных текстов карточки.
	promptNoteRunes = 300
	// promptNameRunes — потолок имени детали, ключа узла, имени строки BOM и токена-словаря.
	promptNameRunes = 120
)

// Разряды колонок, в которых числа карточки ХРАНЯТСЯ. Печатаются они ровно так же — не .String(),
// который срезает хвостовые нули и превращает цену 1.0000 в «1»: разряд это утверждение о точности
// («55.0000 за метр» — четыре знака в колонке, а не округление до целого), и терять его в промпте
// значит показывать модели другое число, чем видит технолог на вкладке.
const (
	scaleSeamAllowanceMm = 1 // tech_card.required_seam_allowance_mm DECIMAL(6,1)
	scaleStitchesPerCm   = 2 // tech_card_construction.default_stitches_per_cm DECIMAL(5,2)
	scaleUnitPrice       = 4 // tech_card_bom_item.unit_price DECIMAL(12,4)
)

// PromptInput is everything the user prompt needs that is NOT inside entity.TechCard.
//
// GarmentType ПРИХОДИТ АРГУМЕНТОМ ПО ТОЙ ЖЕ ПРИЧИНЕ, ЧТО И Fx: тип изделия лежит в карточке
// внешним ключом (tech_card.type_id → category), а пакет в БД не ходит вовсе — это то свойство,
// ради которого он существует отдельно (см. шапку findings.go). Пустая строка — законное значение
// и молчит, как всякое пустое (§7.2).
type PromptInput struct {
	// Card — карточка, как её отдаёт GetTechCardById: гидрированная, канонизированная.
	Card *entity.TechCard
	// Audit — прогон машинного слоя ЭТОЙ ЖЕ карточки: его находки едут блоком MACHINE FINDINGS
	// ALREADY FILED, его наблюдения — блоком MACHINE OBSERVATIONS.
	Audit AuditResult
	// GarmentType — разрешённое имя типа изделия («blazer»), уже вынутое обработчиком из словаря
	// категорий.
	GarmentType string
}

// PromptPiece is one cut piece as §7.2 prints it.
type PromptPiece struct {
	Name         string
	PerGarment   int
	FabricBucket string
	// Attributes — НЕ-ДЕФОЛТНЫЕ атрибуты по правилу пустоты §6: ungraded, fused, не-lengthwise
	// долевая. Дефолтные молчат: сорок восемь строк «graded, not fused, lengthwise» — это сорок
	// восемь строк, в которых теряется одна настоящая.
	Attributes []string
}

// PromptBomLine is one BOM line as §7.2 prints it.
type PromptBomLine struct {
	Section     string
	Purpose     string
	PurposeNote string
	Name        string
	// Price — «55.0000 PLN/m»; пусто, когда цены или валюты на строке нет.
	//
	// ⚠️ ДЕНЬГИ (design §12, решение T14). ЦЕНЫ В ПРОМПТ КЛАДУТСЯ — и это решение, а не умолчание.
	// Почему: §6 и §7.2 — ревьюированный контракт, ценовой блок в нём есть, и без него из ревью
	// исчезает целый КЛАСС находок, отданный §2 модели («карман дороже основной ткани — это
	// намерение или опечатка?»); цена — единственное, чем этот вопрос вообще задаётся.
	//
	// ЦЕНА РАВНОСИЛЬНА ОБЯЗАТЕЛЬСТВУ НА ВЫХОДЕ. Модель, увидевшая ценовой блок, может процитировать
	// закупочную цену или валюту в ЛЮБОЙ своей находке, а аудитория аудита ШИРЕ костинговой
	// (`GetTechCardConstructionAudit` — read-грант секции; тот же аккаунт у `GetTechCard` получает
	// карточку с вырезанным unit_price через stripTechCardCosting). Поэтому T15 (верификатор §8)
	// ОБЯЗАН ставить Finding.Money каждой модельной находке, чьи refs указывают на строку BOM либо
	// чей текст содержит валюту или величину, а T16 — подавлять её тем же фильтром
	// redactMoneyFindings, каким подавляет машинные денежные находки. Инвариант «денежная находка
	// не может иметь категорию readiness» действует и на модельных.
	//
	// Признак того, что это обязательство наступило, едет ДАННЫМИ, а не комментарием:
	// PromptContext.PricesIncluded. Список имён проверок в обработчике — это место, куда новая
	// денежная строка не попадёт и утечёт молча (§12); флаг, посчитанный по факту рендера, ошибается
	// в другую сторону.
	Price string
}

// PromptOperation is one route step as §7.2 prints it.
type PromptOperation struct {
	Number      int32
	NumberValid bool
	// Verb — «machine on lockstitch» / «press_open» / «buttonhole (machine: buttonhole)»: вид работы
	// (0329) когда он назначен, иначе тип операции, и машина рядом с тем и другим.
	Verb string
	Zone string
	// Inputs — входы В ИСХОДНОМ ПОРЯДКЕ (display_order): детали именами, узлы как UNIT<ключ>.
	// Порядок — факт карточки: «сперва подборт, потом полочка» и «сперва полочка» это разные
	// указания цеху, и сортировка здесь стёрла бы разницу.
	Inputs []string
	// Produces — ключ произведённого узла; пусто = ОБРАБОТКА (§1).
	Produces string
	// Absorbing — ПОГЛОЩЕНИЕ (§1): выход совпадает с одним из входов-узлов. Печатается пометкой,
	// потому что без неё строка «consumes UNIT<pocket base> … produces "pocket base"» читается как
	// дубль-производитель, которого запись не допускает.
	Absorbing bool
	Note      string
	// Materials — имена строк BOM, привязанных к шагу.
	Materials []string
}

// PromptTerminal is a terminal unit and the step that produced it.
type PromptTerminal struct {
	Unit        string
	Op          int32
	OpNumberOK  bool
	HasProducer bool
}

// PromptAbsorption is one absorbing chain, for VERIFIED FACTS.
type PromptAbsorption struct {
	Op         int32
	OpNumberOK bool
	Unit       string
	// Added — что шаг догрузил в узел, кроме самого узла.
	Added []string
}

// PromptFinding is one already-filed machine finding as the FILED block prints it.
type PromptFinding struct {
	Category string
	Title    string
	Detail   string
	Refs     []string
	// Collapsed — это схлопнутая readiness-находка черновика (§3.0). Модель обязана знать, что за
	// одной строкой стоит список: правило зрелости §7.1 запрещает ей заводить находки о незаполненных
	// полях, и «(draft, collapsed)» — то, по чему она это узнаёт.
	Collapsed bool
}

// PromptGround is the numeric half of VERIFIED FACTS: what the recomputation counted. Слова к этим
// числам подбирает prompt.go.
type PromptGround struct {
	// Violations — сколько нарушений увидел пересчёт. НЕ НОЛЬ значит, что карточка писалась мимо
	// конвертера, и утверждать «граф чист» в закрытом мире НЕЛЬЗЯ (см. groundtruth.go, Violations).
	Violations int
	Terminals  []PromptTerminal
	// Operations / Pieces — знаменатели всех дробей блока.
	Operations int
	Pieces     int
	// UnconsumedPieces — имена деталей, не съеденных ни одним джойном.
	UnconsumedPieces []string
	Absorptions      []PromptAbsorption
	// ProcessingOps — номера шагов-обработок в каноническом порядке.
	ProcessingOps []string
	WorksAssigned int
	SMVFilled     int
	Profiles      int
	FusedPieces   int
	FusingOps     int
	// InterliningLines — строки BOM секции interlining: имя и purpose-нота.
	InterliningLines []PromptBomLine
	// FinishingOps — сколько шагов маршрута несут финишный глагол.
	FinishingOps int
	// OwnSeamClass / OwnAllowance / OwnDensity — сколько шагов задали своё значение вместо
	// наследования. Строка наследования дефолтов в шапке ОБЯЗАТЕЛЬНА (§7.2, «правило рендера»):
	// без неё золотая находка 10 («оверлок наследует ss_plain») из промпта невыводима — правило
	// наследования живёт только в Go.
	OwnSeamClass int
	OwnAllowance int
	OwnDensity   int
}

// PromptContext is §6 selected, capped and ordered — everything §7.2 prints, and nothing else.
type PromptContext struct {
	StyleName    string
	StyleNumber  string
	GarmentType  string
	TargetGender string
	Purpose      string

	Stage         string
	ApprovalState string
	// Draft — карточка в состоянии draft: §7.1 велит модели не заводить находки о незаполненных
	// полях, и шапка обязана сказать, что карточка черновик.
	Draft bool

	RequiredSeamAllowanceMm string
	DefaultSeamClass        string
	DefaultStitchesPerCm    string
	// HemFinish / ConstructionNotes — ЕДИНСТВЕННЫЕ два поля, у которых пустота печатается словами
	// «not specified» (§7.2, правило рендера). Они ревьюируемые дефолты карточки: их отсутствие —
	// содержательный факт, а не молчание.
	HemFinish         string
	ConstructionNotes string

	MachineProfiles int
	PressProfiles   int

	Pieces       []PromptPiece
	Bom          []PromptBomLine
	Operations   []PromptOperation
	Ground       PromptGround
	Filed        []PromptFinding
	Observations []string

	// PricesIncluded — в промпте есть хотя бы одна закупочная цена. См. ⚠️ ДЕНЬГИ на
	// PromptBomLine.Price: пока это true, модельные находки ОБЯЗАНЫ проходить денежный скрин T15 и
	// подавление T16.
	PricesIncluded bool
}

// BuildPromptContext selects §6's data out of a saved card and its machine run.
//
// ВТОРОЕ ЗНАЧЕНИЕ — «ЕСТЬ ЧТО АНАЛИЗИРОВАТЬ» (§1/§7). Карточка без единого СБОРОЧНОГО ФАКТА проходит
// путь записи вакуумно: canonicalizeAssembly её не трогает вовсе (ранний выход `marked`), графа у
// неё нет, и судить по графу нечего — ни маршрутной полноты, ни гранулярности, ни имён узлов не
// существует как предмета. false здесь значит «прогон не собирается»; обработчик T16 превращает это
// в ai_status="skipped" и НЕ тратит ключ.
//
// Условие ровно то же, по которому включается правило 4 записи (entity.AssemblyReleaseCheck): хотя
// бы один ПРОИЗВОДЯЩИЙ шаг. Не «хотя бы одна операция»: маршрут из одних обработок ничего не
// собирает, и сборочного факта в нём нет ни одного.
func BuildPromptContext(in PromptInput) (PromptContext, bool) {
	card := in.Card
	if card == nil {
		return PromptContext{}, false
	}
	gt := ComputeGroundTruth(card)
	if !gt.Marked {
		return PromptContext{}, false
	}

	ctx := PromptContext{
		StyleName:    promptField(card.Name, promptNameRunes),
		StyleNumber:  promptField(card.StyleNumber.String, promptNameRunes),
		GarmentType:  promptField(in.GarmentType, promptNameRunes),
		TargetGender: promptField(card.TargetGender.String, promptNameRunes),
		Purpose:      strings.TrimSpace(string(card.Purpose)),

		Stage:         strings.TrimSpace(string(card.Stage)),
		ApprovalState: strings.TrimSpace(string(card.ApprovalState)),
		Draft:         card.ApprovalState == entity.TechCardApprovalDraft,

		RequiredSeamAllowanceMm: decimalAtScale(card.RequiredSeamAllowanceMm, scaleSeamAllowanceMm),
		Observations:            append([]string(nil), in.Audit.Observations...),
	}

	construction := card.Construction
	if construction == nil {
		construction = &entity.TechCardConstruction{}
	}
	ctx.DefaultSeamClass = promptField(construction.DefaultSeamClass.String, promptNameRunes)
	ctx.DefaultStitchesPerCm = decimalAtScale(construction.DefaultStitchesPerCm, scaleStitchesPerCm)
	ctx.HemFinish = promptField(construction.HemFinish.String, promptNoteRunes)
	ctx.ConstructionNotes = promptField(construction.Notes.String, promptNoteRunes)
	if eq := construction.EquipmentDefaults; eq != nil {
		ctx.MachineProfiles = len(eq.Machines)
		ctx.PressProfiles = len(eq.Presses)
	}

	bomByKey := make(map[string]*entity.TechCardBomItem, len(card.BomItems))
	for i := range card.BomItems {
		b := &card.BomItems[i]
		if b.LineKey != "" {
			bomByKey[b.LineKey] = b
		}
	}
	ctx.Bom = make([]PromptBomLine, 0, len(card.BomItems))
	for i := range card.BomItems {
		line := promptBomLineOf(&card.BomItems[i])
		if line.Price != "" {
			ctx.PricesIncluded = true
		}
		ctx.Bom = append(ctx.Bom, line)
	}

	scope := PieceScopeKeys(card)
	pieceNameByKey := make(map[string]string, len(card.Pieces))
	ctx.Pieces = make([]PromptPiece, 0, len(card.Pieces))
	for i := range card.Pieces {
		p := &card.Pieces[i]
		pieceNameByKey[p.LineKey] = p.Name
		ctx.Pieces = append(ctx.Pieces, PromptPiece{
			Name:         promptField(p.Name, promptNameRunes),
			PerGarment:   p.PiecesPerGarment,
			FabricBucket: promptField(scope[p.LineKey], promptNameRunes),
			Attributes:   pieceAttributes(p),
		})
	}

	ctx.Operations = make([]PromptOperation, 0, len(gt.Steps))
	for _, step := range gt.Steps {
		op := &card.Operations[step.CardIndex]
		ctx.Operations = append(ctx.Operations, PromptOperation{
			Number:      step.OperationNumber,
			NumberValid: step.NumberValid,
			Verb:        operationVerb(op),
			Zone:        promptField(string(op.Zone), promptNameRunes),
			Inputs:      operationInputs(op, pieceNameByKey),
			Produces:    promptField(op.OutputUnitKey.String, promptNameRunes),
			Absorbing:   step.Kind == StepAbsorbing,
			Note:        promptField(op.Note.String, promptNoteRunes),
			Materials:   operationMaterials(op, bomByKey),
		})
	}

	ctx.Ground = buildGround(card, gt, pieceNameByKey, ctx.Bom)
	ctx.Filed = buildFiled(in.Audit.Findings, ctx.Draft)
	return ctx, true
}

// buildGround counts everything the VERIFIED FACTS block states.
func buildGround(card *entity.TechCard, gt GroundTruth, pieceNameByKey map[string]string,
	bom []PromptBomLine) PromptGround {
	g := PromptGround{
		Violations: len(gt.Violations),
		Operations: len(gt.Steps),
		Pieces:     len(card.Pieces),
	}

	for _, key := range gt.Terminals {
		t := PromptTerminal{Unit: promptField(key, promptNameRunes)}
		if at, ok := gt.ProducerOf[key]; ok && at >= 0 && at < len(gt.Steps) {
			t.HasProducer = true
			t.Op, t.OpNumberOK = gt.Steps[at].OperationNumber, gt.Steps[at].NumberValid
		}
		g.Terminals = append(g.Terminals, t)
	}
	for _, key := range gt.UnconsumedPieces {
		g.UnconsumedPieces = append(g.UnconsumedPieces,
			promptField(pieceNameOr(pieceNameByKey, key), promptNameRunes))
	}

	for _, step := range gt.Steps {
		op := &card.Operations[step.CardIndex]
		switch step.Kind {
		case StepProcessing:
			g.ProcessingOps = append(g.ProcessingOps, opLabelOf(step.OperationNumber, step.NumberValid))
		case StepAbsorbing:
			a := PromptAbsorption{
				Op:         step.OperationNumber,
				OpNumberOK: step.NumberValid,
				Unit:       promptField(step.OutputUnitKey, promptNameRunes),
			}
			for _, in := range op.AssemblyInputs {
				if in.Kind == entity.AssemblyInputUnit && in.Key == step.OutputUnitKey {
					continue // сам поглощаемый узел — не «догруженное»
				}
				a.Added = append(a.Added, promptField(inputLabel(in, pieceNameByKey), promptNameRunes))
			}
			g.Absorptions = append(g.Absorptions, a)
		}
		if !nsEmpty(op.Work) {
			g.WorksAssigned++
		}
		if op.SMV.Valid {
			g.SMVFilled++
		}
		if !nsEmpty(op.SeamClass) {
			g.OwnSeamClass++
		}
		if op.SeamAllowanceMm.Valid {
			g.OwnAllowance++
		}
		if op.StitchesPerCm.Valid {
			g.OwnDensity++
		}
		if op.OperationType == entity.OpTypeFusing {
			g.FusingOps++
		}
		if hasVerb(op, finishingVerbs) {
			g.FinishingOps++
		}
	}

	for i := range card.Pieces {
		if card.Pieces[i].Fused {
			g.FusedPieces++
		}
	}
	if c := card.Construction; c != nil && c.EquipmentDefaults != nil {
		g.Profiles = len(c.EquipmentDefaults.Machines) + len(c.EquipmentDefaults.Presses)
	}
	for i := range card.BomItems {
		if card.BomItems[i].Section == entity.BomSectionInterlining {
			g.InterliningLines = append(g.InterliningLines, bom[i])
		}
	}
	return g
}

// buildFiled turns the run's machine findings into the FILED block's entries.
//
// БЛОК ЕДЕТ ЦЕЛИКОМ, ВКЛЮЧАЯ ДЕНЕЖНЫЕ НАХОДКИ. Вырезать их отсюда бессмысленно: цены уже стоят в
// блоке BOM (см. ⚠️ ДЕНЬГИ), а вырезанная находка возвращается — модель заведёт её своей, и та же
// цена приедет обратно уже как модельная строка, которую §7.1 просил не дублировать. Граница
// проходит по ВЫХОДУ (T15/T16), одна и та же для машинных и модельных находок.
func buildFiled(findings []Finding, draft bool) []PromptFinding {
	out := make([]PromptFinding, 0, len(findings))
	for i := range findings {
		f := &findings[i]
		out = append(out, PromptFinding{
			Category:  f.Category,
			Title:     strings.TrimSpace(f.Title),
			Detail:    strings.TrimSpace(f.Detail),
			Refs:      append([]string(nil), f.Refs...),
			Collapsed: draft && f.Category == CategoryReadiness,
		})
	}
	return out
}

// promptBomLineOf selects one BOM line's §6 fields.
func promptBomLineOf(b *entity.TechCardBomItem) PromptBomLine {
	line := PromptBomLine{
		Section:     strings.TrimSpace(string(b.Section)),
		Purpose:     promptField(b.Purpose.String, promptNameRunes),
		PurposeNote: promptField(b.PurposeNote.String, promptNameRunes),
		Name:        promptField(b.Name, promptNameRunes),
	}
	// Цена печатается только целиком — величина, валюта и единица вместе. «55.0000 /m» без валюты
	// это не цена, а число, и сравнить его модели не с чем.
	price := decimalAtScale(b.UnitPrice, scaleUnitPrice)
	currency := strings.TrimSpace(b.Currency.String)
	if price != "" && currency != "" {
		line.Price = price + " " + currency
		if unit := strings.TrimSpace(b.Unit.String); unit != "" {
			line.Price += "/" + unit
		}
	}
	return line
}

// pieceAttributes lists what the piece says BEYOND the defaults (§6, правило пустоты).
func pieceAttributes(p *entity.TechCardPiece) []string {
	var out []string
	if p.Ungraded {
		out = append(out, "ungraded")
	}
	if p.Fused {
		out = append(out, "fused")
	}
	if g := strings.TrimSpace(p.Grainline); g != "" && g != "lengthwise" {
		out = append(out, "grainline "+promptField(g, promptNameRunes))
	}
	return out
}

// operationVerb renders «что за работа и на чём» — the two axes of a step (0306/0329).
//
// Вид работы (Work) старше типа операции: тип — грубая ось словаря («machine»), вид — то, что
// человек назначил шагу («buttonhole»). Когда назначен вид, тип не печатается вовсе: «machine on
// buttonhole» и «buttonhole (machine: buttonhole)» — одно и то же дважды.
func operationVerb(op *entity.TechCardOperation) string {
	work := promptField(op.Work.String, promptNameRunes)
	machine := promptField(op.MachineType.String, promptNameRunes)
	opType := promptField(string(op.OperationType), promptNameRunes)
	switch {
	case work != "" && machine != "":
		return work + " (machine: " + machine + ")"
	case work != "":
		return work
	case machine != "" && opType != "":
		return opType + " on " + machine
	case machine != "":
		return machine
	default:
		return opType
	}
}

// operationInputs renders the step's inputs IN THEIR STORED ORDER: pieces by name, units as
// UNIT<key>.
func operationInputs(op *entity.TechCardOperation, pieceNameByKey map[string]string) []string {
	out := make([]string, 0, len(op.AssemblyInputs))
	for _, in := range op.AssemblyInputs {
		out = append(out, promptField(inputLabel(in, pieceNameByKey), promptNameRunes))
	}
	return out
}

// inputLabel is one input's label. Деталь — ИМЕНЕМ, а не line_key: технолог не знает своих ULID'ов,
// и якорь «piece:<name>» §7.1 п.5 построен на том же имени.
func inputLabel(in entity.OperationInput, pieceNameByKey map[string]string) string {
	if in.Kind == entity.AssemblyInputUnit {
		return "UNIT<" + in.Key + ">"
	}
	return pieceNameOr(pieceNameByKey, in.Key)
}

// operationMaterials renders the BOM lines linked to the step, by name.
func operationMaterials(op *entity.TechCardOperation, bomByKey map[string]*entity.TechCardBomItem) []string {
	var out []string
	for _, key := range op.BomLineKeys {
		b := bomByKey[key]
		if b == nil {
			continue // мягкая ссылка в никуда — предмет проверки целостности, не строки промпта
		}
		out = append(out, promptField(b.Name, promptNameRunes))
	}
	return out
}

// pieceNameOr resolves a piece line_key to its name, falling back to the key so a label never comes
// out empty.
func pieceNameOr(pieceNameByKey map[string]string, lineKey string) string {
	if name := strings.TrimSpace(pieceNameByKey[lineKey]); name != "" {
		return name
	}
	return lineKey
}

// opLabelOf is an operation's number as the prompt anchors on it. Легаси-строка без номера
// печатается «?»: якоря у неё нет, и выдумать его нельзя — модель сослалась бы на шаг, которого
// клиент не найдёт.
func opLabelOf(number int32, valid bool) string {
	if !valid {
		return "?"
	}
	return strconv.Itoa(int(number))
}

// promptField is the ONE gate every card-authored string passes on its way into the prompt: the
// per-field cap of §6 («Заборы») and the collapse of line breaks into spaces.
//
// ПОЧЕМУ ПЕРЕНОСЫ СХЛОПЫВАЮТСЯ, ХОТЯ ЭКРАНИРОВАНИЯ ЗДЕСЬ НЕТ. Это не защита от инструкции в ноте —
// её держит абзац Data fence системного промпта (§7.1), и текст ноты доезжает до модели дословно.
// Это защита от ПОДДЕЛКИ СТРУКТУРЫ: нота с переносом строки может напечатать собственную строку
// блока — «- naming: …» в MACHINE FINDINGS ALREADY FILED или целый заголовок VERIFIED FACTS, — а
// этот блок §7.1 объявляет закрытым миром, который модели запрещено оспаривать. Инъекция,
// выглядящая как машинный факт, обходит забор не убеждением, а происхождением. Ни один символ при
// этом не теряется: перенос становится пробелом, слова остаются на месте, кап §6 считает руны.
func promptField(s string, max int) string {
	return aiBoundedText(flattenLines(s), max)
}

// flattenLines turns every line break of a stored value into a space.
func flattenLines(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
}

// decimalAtScale prints a stored decimal at its column's scale, or "" when the column is NULL.
func decimalAtScale(d decimal.NullDecimal, scale int32) string {
	if !d.Valid {
		return ""
	}
	return d.Decimal.StringFixed(scale)
}
