package techcardanalysis

import (
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Fx is the currency channel of the audit: the rates the money checks need, and the base they fold
// to (design §3.2 B5в).
//
// ПОЧЕМУ АРГУМЕНТОМ, А НЕ ПОЛЕМ КАРТОЧКИ. Курсов в entity.TechCard нет и не будет: они живут в
// costing_fx_rate и достаются GetCostingFxRatesToBase на уровне ОБРАБОТЧИКА (образец —
// internal/apisrv/admin/techcard.go, s.costingFx), а база приходит из cache.GetBaseCurrency().
// Пакет в БД не ходит вовсе — это то свойство, ради которого он существует отдельно, — поэтому
// единственная честная форма связи с курсами это аргумент.
//
// dto.CostingFx сюда не импортируется НИКОГДА: dto импортирует ЭТОТ пакет (T6), и обратный импорт
// был бы циклом.
type Fx struct {
	// ToBase — курс валюты К БАЗЕ, по коду валюты. Отсутствие ключа значит «курса нет», а не «курс
	// единица»: подставить единицу значило бы посчитать 60 PLN как 60 EUR и объявить это фактом.
	ToBase map[string]decimal.Decimal
	// Base — код базовой валюты контура. Дефолт системы — EUR (cache.defaultCurrency); проверки
	// обязаны читать его отсюда, а не хардкодить, иначе на другом контуре они начнут находить
	// «строки без курса» там, где вся карточка и есть база.
	Base string
}

// Rate returns the rate of `currency` to the base, and whether it is known at all. Валюта, РАВНАЯ
// базе, всегда известна и равна единице — иначе всякая проверка повторяла бы это условие сама, и
// одна из них однажды забыла бы.
func (f Fx) Rate(currency string) (decimal.Decimal, bool) {
	c := strings.ToUpper(strings.TrimSpace(currency))
	if c == "" {
		return decimal.Decimal{}, false
	}
	if c == strings.ToUpper(strings.TrimSpace(f.Base)) {
		return decimal.NewFromInt(1), true
	}
	r, ok := f.ToBase[c]
	return r, ok
}

// AuditResult is one machine-layer run (design §4, GetTechCardConstructionAuditResponse).
type AuditResult struct {
	// Findings — машинные находки, отсортированные и (на черновике) со схлопнутым readiness.
	Findings []Finding
	// Fingerprints — operation_number → fp8 (§9). Клиент сравнивает их со своими, чтобы отличить
	// «эта операция изменилась с момента прогона» от «номер переехал».
	Fingerprints map[int32]string
	// NotChecked — что этот прогон НЕ проверял и почему. Модель обязана говорить это вслух (§6), а
	// машинный слой обязан не делать вид, что молчание проверки означает «всё в порядке».
	NotChecked []string
	// Observations — блок MACHINE OBSERVATIONS для промпта (§3.4): эвристики, которым явно
	// разрешено ошибаться. Находками они становятся только с бейджем ConfidenceHeuristic; сюда
	// едут СТРОКАМИ, потому что модель их опровергает прозой, а не по якорям.
	Observations []string
}

// checkFn is one check of the machine layer. Она получает готовый разбор карточки и возвращает
// свои находки — ни логировать, ни ходить наружу ей нечем и незачем.
type checkFn func(v *cardView) []Finding

// checks is the registry. Пуст после T2: проверки регистрируют себя САМИ, каждая в своём файле.
var checks []checkFn

// register adds checks to the registry and returns a struct{} so the call fits a file-scope
// `var _ = register(...)`.
//
// ЗАЧЕМ ИМЕННО ТАК. Проверки маршрута (route.go), BOM (bom.go) и готовности (readiness.go) пишутся
// РАЗНЫМИ параллельными задачами. Если бы реестр наполнялся списком в analysis.go, у «параллельных»
// задач оказался бы общий файл — генератор дефектов на швах, где обе правки выглядят верными по
// отдельности. Регистрация в собственном файле снимает шов целиком: analysis.go после T2 не трогает
// никто.
//
// Порядок регистрации значения не имеет: RunAudit сортирует результат детерминированно.
func register(fns ...checkFn) struct{} {
	checks = append(checks, fns...)
	return struct{}{}
}

// cardView is the parsed card every check reads. Собирается один раз на прогон: сорок восемь
// проверок, каждая из которых сама строит карту деталей по ключу, — это сорок восемь мест, где
// «ключ пустой» обрабатывается чуть по-разному.
type cardView struct {
	card *entity.TechCard
	fx   Fx
	gt   GroundTruth

	// ops — операции в КАНОНИЧЕСКОМ порядке (entity.AssemblyOperationOrder), указателями на строки
	// card.Operations. Проверки читают их отсюда и НЕ сортируют сами.
	ops []*entity.TechCardOperation
	// pieceByKey / pieceByID — детали по line_key и по id.
	pieceByKey map[string]*entity.TechCardPiece
	pieceByID  map[int]*entity.TechCardPiece
	// bomByKey / bomByID — строки BOM по line_key и по id.
	bomByKey map[string]*entity.TechCardBomItem
	bomByID  map[int]*entity.TechCardBomItem
	// pieceScope — line_key детали → ключ ведра ткани (§7.2 «fabric bucket»).
	pieceScope map[string]string
	// draft — карточка в состоянии draft: класс readiness схлопывается (§3.0).
	draft bool

	notChecked   []string
	observations []string
}

// notCheck records something this run did not verify. Проверка, которая молчит потому, что данных
// нет, ОБЯЗАНА сказать это здесь: молчание, неотличимое от «проверено и чисто», — самый дорогой вид
// лжи, который может себе позволить аудит.
func (v *cardView) notCheck(line string) {
	if line = strings.TrimSpace(line); line != "" {
		v.notChecked = append(v.notChecked, line)
	}
}

// observe records one MACHINE OBSERVATIONS line (§3.4).
func (v *cardView) observe(line string) {
	if line = strings.TrimSpace(line); line != "" {
		v.observations = append(v.observations, line)
	}
}

// construction returns the card's construction defaults, never nil — a card with no row inherits
// nothing, and that is a value, not a crash.
func (v *cardView) construction() *entity.TechCardConstruction {
	if v.card == nil || v.card.Construction == nil {
		return &entity.TechCardConstruction{}
	}
	return v.card.Construction
}

// equipment returns the card's equipment park, never nil. NIL НА ЧТЕНИИ ЗНАЧИТ «профилей нет» (так
// его оставляет стор), а не «payload промолчал» — это смысл только на записи.
func (v *cardView) equipment() *entity.TechCardEquipmentDefaults {
	if c := v.construction(); c.EquipmentDefaults != nil {
		return c.EquipmentDefaults
	}
	return &entity.TechCardEquipmentDefaults{}
}

// pieceName resolves a piece line_key to the name a human anchors on; falls back to the key so an
// anchor never comes out empty.
func (v *cardView) pieceName(lineKey string) string {
	if p := v.pieceByKey[lineKey]; p != nil && p.Name != "" {
		return p.Name
	}
	return lineKey
}

// staticNotChecked is what the machine layer NEVER checks, on any card, and therefore says out loud
// on every run (design §6). Не условные строки: путь анализа текстовый, и геометрии деталей в нём
// нет даже когда площади замерены — площади это площади, а не контуры.
var staticNotChecked = []string{
	"sketch (not reviewed: the analysis path is text-only)",
	"piece geometry (contours are not stored; measured areas are areas, not outlines)",
}

// RunAudit is the machine layer, end to end: recompute the ground truth, run every registered
// check against it, then sort and (on a draft) collapse the result.
//
// СИГНАТУРА ЗАМОРОЖЕНА (T2). Курсы приходят аргументом, потому что в карточке их нет, а в БД этот
// пакет не ходит; менять форму под уже пишущими параллельными задачами нельзя.
func RunAudit(card *entity.TechCard, fx Fx) AuditResult {
	v := newCardView(card, fx)

	var findings []Finding
	for _, check := range checks {
		for _, f := range check(v) {
			if f.Source == "" {
				// Слой машинный целиком: находка без источника — забытое поле, а не модельная
				// находка, случайно попавшая в реестр.
				f.Source = SourceMachine
			}
			findings = append(findings, f)
		}
	}

	sortFindings(findings)
	findings = CollapseReadiness(findings, v.draft)

	notChecked := make([]string, 0, len(staticNotChecked)+len(v.notChecked))
	notChecked = append(notChecked, staticNotChecked...)
	notChecked = append(notChecked, v.notChecked...)

	return AuditResult{
		Findings:     findings,
		Fingerprints: Fingerprints(card),
		NotChecked:   notChecked,
		Observations: v.observations,
	}
}

// newCardView parses the card once for every check of the run.
func newCardView(card *entity.TechCard, fx Fx) *cardView {
	v := &cardView{
		card:       card,
		fx:         fx,
		gt:         ComputeGroundTruth(card),
		pieceByKey: map[string]*entity.TechCardPiece{},
		pieceByID:  map[int]*entity.TechCardPiece{},
		bomByKey:   map[string]*entity.TechCardBomItem{},
		bomByID:    map[int]*entity.TechCardBomItem{},
		pieceScope: PieceScopeKeys(card),
	}
	if card == nil {
		return v
	}
	v.draft = card.ApprovalState == entity.TechCardApprovalDraft

	for i := range card.Pieces {
		p := &card.Pieces[i]
		if p.LineKey != "" {
			v.pieceByKey[p.LineKey] = p
		}
		if p.Id != 0 {
			v.pieceByID[p.Id] = p
		}
	}
	for i := range card.BomItems {
		b := &card.BomItems[i]
		if b.LineKey != "" {
			v.bomByKey[b.LineKey] = b
		}
		if b.Id != 0 {
			v.bomByID[b.Id] = b
		}
	}
	v.ops = make([]*entity.TechCardOperation, 0, len(card.Operations))
	for _, idx := range entity.AssemblyOperationOrder(card.Operations) {
		v.ops = append(v.ops, &card.Operations[idx])
	}
	return v
}

// sortFindings orders the section the way a human reads it: worst first, then along the route.
//
// Порядок ДЕТЕРМИНИРОВАН до последнего разряда, и это не эстетика: слой always-on, он пересчитывается
// при каждом открытии вкладки, и список, который тасуется от прогона к прогону на одних и тех же
// данных, читается как «что-то изменилось» там, где не изменилось ничего. По той же причине
// сортировка стабильная: две находки, неразличимые всеми четырьмя ключами, остаются в порядке
// реестра.
func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := &findings[i], &findings[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if oa, ob := firstOpAnchor(a.Refs), firstOpAnchor(b.Refs); oa != ob {
			return oa < ob
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return strings.Join(a.Refs, "\x00") < strings.Join(b.Refs, "\x00")
	})
}

// noOpAnchor sorts findings that name no operation after those that do, within one severity: a
// finding about the card as a whole is context for the route, not a step of it.
const noOpAnchor = int64(1) << 40

// firstOpAnchor is the smallest operation number among a finding's refs, or noOpAnchor.
func firstOpAnchor(refs []string) int64 {
	best := noOpAnchor
	for _, r := range refs {
		if !strings.HasPrefix(r, refOpPrefix) {
			continue
		}
		n, err := strconv.ParseInt(r[len(refOpPrefix):], 10, 64)
		if err != nil {
			continue
		}
		if n < best {
			best = n
		}
	}
	return best
}
