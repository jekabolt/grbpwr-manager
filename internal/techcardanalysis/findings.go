// Package techcardanalysis is the MACHINE layer of the CONSTRUCTION review (design §3): a
// deterministic audit of a saved tech card's dictionaries, readiness, BOM chains and prices, plus
// the recomputed ground truth of its assembly graph that the LLM layer is handed as closed-world
// facts.
//
// THE PACKAGE IS PURE. No database, no proto, no internal/dto — dto imports THIS package later
// (T6), so importing it back would be a cycle, and everything that lives outside entity.TechCard
// (currency rates, today) arrives as an argument. That purity is what makes the whole layer
// testable against one fixture card instead of against a schema.
//
// WHAT IT DOES NOT DO. It does not hunt topological defects. Cycles, forward references, duplicate
// producers, double-consumed pieces and namespace collisions are UNREPRESENTABLE in a saved card:
// canonicalizeAssembly refuses such a payload on every write (design §1). The topological pass here
// is a RECOMPUTATION for the prompt and for check C6 — never a source of findings.
package techcardanalysis

import (
	"strconv"
	"strings"
)

// MaxAnalysisOperations gates the INPUT of the analysis RPCs (design §4): a card with more
// operations than this is refused with InvalidArgument by the handler rather than analysed.
//
// EXPORTED although design §4 spells it lowercase: the gate itself lives in the handler
// (internal/apisrv/admin, T6), which is a different package, so an unexported constant could not be
// the one the gate reads — and a second copy of the number in the handler is exactly how two
// ceilings come to disagree.
//
// It is NOT openrouter.maxOperations. That one silently slices the GENERATOR's OUTPUT; this one
// refuses an oversized INPUT out loud. Sharing either the constant or the semantics would make a
// change to one silently move the other.
const MaxAnalysisOperations = 200

// Finding source (design §4, TechCardAnalysisFinding.source).
const (
	// SourceMachine — эта находка посчитана детерминированным кодом этого пакета.
	SourceMachine = "machine"
	// SourceModel — эта находка пришла от модели и прошла верификатор §8.
	SourceModel = "model"
)

// Severity — закрытый список design §7.1 п.3. `question` НЕ severity: это категория.
const (
	// SeverityBlocker — как написано, маршрут не производит изделие, либо шаг неисполним как одна
	// операция цеха.
	SeverityBlocker = "blocker"
	// SeverityError — карточка утверждает нечто неверное, за чем фабрика уйдёт в брак.
	SeverityError = "error"
	// SeverityWarning — починить до релиза, но работу не останавливает.
	SeverityWarning = "warning"
)

// Categories. Первые восемь — закрытый список, общий с моделью (design §7.1 п.2, §8 п.1);
// остальные машинные, у модели их не бывает.
const (
	CategoryMissingStep = "missing_step"
	CategoryCoarseStep  = "coarse_step"
	CategoryMethod      = "method"
	CategorySequence    = "sequence"
	CategoryNaming      = "naming"
	CategoryBomMismatch = "bom_mismatch"
	CategoryParameter   = "parameter"
	CategoryQuestion    = "question"

	// CategoryReadiness — «данные готовности не заполнены». ЕДИНСТВЕННЫЙ класс, который
	// схлопывается на черновике (§3.0, CollapseReadiness).
	CategoryReadiness = "readiness"
	// CategoryIntegrity — битая ссылка внутри карточки (мягкая ссылка в никуда). Не readiness:
	// на черновике она такой же дефект, как на релизе, и схлопывать её нельзя.
	CategoryIntegrity = "integrity"
	// CategoryAssembly — предсказание отказа релизного правила 4 (§3.3 C6).
	//
	// РАСХОЖДЕНИЕ СПЕЦИФИКАЦИИ, ЗАФИКСИРОВАННОЕ ЗДЕСЬ: §3.3 называет класс C6 словом «assembly»,
	// а §4 и §3.0 перечисляют машинные категории как ровно «readiness|integrity». Категория едет
	// строкой (§4: «не proto-enum»), поэтому третий член ничего не ломает ни на проводе, ни в
	// клиенте, и назвать вещь её собственным именем честнее, чем перекрасить предсказание отказа
	// релиза в bom_mismatch. В ValidModelCategories он НЕ входит: модель такой категории не знает.
	CategoryAssembly = "assembly"
)

// ValidModelCategories — то, что верификатор §8 п.1 принимает от МОДЕЛИ. Машинные категории сюда
// не входят намеренно: модель, назвавшая находку «readiness», говорит о поле, которого никто не
// заполнил, а это не её работа — коэрция уводит такую находку в `question`.
var ValidModelCategories = map[string]bool{
	CategoryMissingStep: true, CategoryCoarseStep: true, CategoryMethod: true,
	CategorySequence: true, CategoryNaming: true, CategoryBomMismatch: true,
	CategoryParameter: true, CategoryQuestion: true,
}

// ValidSeverities — то, что верификатор §8 п.1 принимает в поле severity.
var ValidSeverities = map[string]bool{
	SeverityBlocker: true, SeverityError: true, SeverityWarning: true,
}

// Confidence values. Модельные три (§7.1 п.4) и машинные два: пусто = детерминированный факт,
// `heuristic` = догадка парователя (§3.4), которую модели РАЗРЕШЕНО опровергнуть.
const (
	ConfidenceCertain    = "certain"
	ConfidenceLikely     = "likely"
	ConfidenceNeedsOwner = "needs_owner"
	ConfidenceHeuristic  = "heuristic"
)

// ValidModelConfidences — то, что верификатор §8 п.1 принимает от модели.
var ValidModelConfidences = map[string]bool{
	ConfidenceCertain: true, ConfidenceLikely: true, ConfidenceNeedsOwner: true,
}

// Anchor sigils (design §9, §7.1 п.5). Якорь — валюта перехода в клиенте, поэтому строится он
// одними и теми же функциями, а не конкатенацией по месту.
const (
	// RefCard — якорь на карточку целиком: находка не про конкретный шаг.
	RefCard = "card"

	refOpPrefix    = "op:"
	refUnitPrefix  = "unit:"
	refPiecePrefix = "piece:"
	refBomPrefix   = "bom:"
)

// RefOp builds the "op:<number>" sigil.
func RefOp(operationNumber int32) string { return refOpPrefix + itoa32(operationNumber) }

// RefUnit builds the "unit:<key>" sigil. Ключ БАЙТ-В-БАЙТ: «Base» и «base» — разные узлы, и
// приведение регистра здесь стёрло бы ровно ту находку, ради которой A1 существует.
func RefUnit(unitKey string) string { return refUnitPrefix + unitKey }

// RefPiece builds the "piece:<name>" sigil. Имя детали, а не line_key: технолог не знает своих
// ULID'ов (та же причина, по которой AssemblyPiece возит имя).
func RefPiece(pieceName string) string { return refPiecePrefix + pieceName }

// RefBom builds the "bom:<name>" sigil.
func RefBom(lineName string) string { return refBomPrefix + lineName }

// Finding is one machine or model finding — the Go mirror of proto TechCardAnalysisFinding (§4).
//
// Все поля, кроме Clause, едут на провод один в один; конвертацию делает dto (T6), поэтому здесь
// нет ни одного proto-типа.
type Finding struct {
	// Source — SourceMachine или SourceModel. Два ответа RPC не смешивают источники (§4), но поле
	// живёт на находке: клиент рисует их в одном списке и обязан различать бейджем.
	Source string
	// Category — одна из констант выше. Строка, не enum: таксономия будет расти, а незнакомого
	// члена enum protojson молча выбросил бы (§4).
	Category string
	// Severity — SeverityBlocker|SeverityError|SeverityWarning.
	Severity string
	// Title — <= 90 символов, по-английски.
	Title string
	// Detail — развёрнутое объяснение.
	Detail string
	// Evidence — display-only. Верификатор §8 п.3 их НЕ проверяет и из-за них ничего не дропает.
	Evidence []string
	// Refs — сигилы-якоря (RefOp/RefUnit/RefPiece/RefBom/RefCard). Находка без единого
	// разрешившегося якоря дропается (§8 п.2), поэтому пустой список у машинной находки — дефект.
	Refs []string
	// InsertAfter — только у CategoryMissingStep: "op:<int>" | "start" | "".
	InsertAfter string
	// Suggestion — что предлагается сделать.
	Suggestion string
	// Confidence — ConfidenceHeuristic у эвристик §3.4, пусто у детерминированных машинных находок,
	// одна из трёх модельных у модели.
	Confidence string

	// Clause — КОРОТКАЯ фраза этой находки для draft-схлопывания §3.0 («SMV 0/48», «no labels»).
	// НЕ ЕДЕТ НА ПРОВОД и в proto-сообщении §4 её нет: она существует только затем, чтобы
	// схлопнутая находка читалась перечислением, а не склейкой шести заголовков-предложений.
	// Заполняется ТОЛЬКО находками класса CategoryReadiness; у остальных пуста и не читается.
	Clause string
}

// CoverageMiss is one gap of a coverage check: its anchors and the per-operation finding that
// would be filed if the gap were reported on its own.
type CoverageMiss struct {
	// Refs — якоря этого пропуска. Первые три из них (в порядке пропусков) становятся
	// якорями-образцами агрегированной находки.
	Refs []string
	// Finding — пер-операционная находка. Читается только на ветке 0 < |M| <= 3.
	Finding Finding
}

// AggregateFn builds the single aggregated finding of a coverage check: how many are missing, how
// many were applicable, and up to three sample anchors — the three numbers §3.0 requires the text
// to quote («press parameters missing on 4 of 4 pressing operations»).
type AggregateFn func(missing, applicable int, sampleRefs []string) Finding

// Aggregate is THE law of coverage checks (design §3.0), and every coverage check is OBLIGED to go
// through it:
//
//	|M| = 0          — молчание;
//	0 < |M| <= 3     — пер-операционные находки;
//	|M| > 3          — ОДНА находка с дробью и <= 3 якорями-образцами.
//
// Никогда 48 находок. Закон живёт одной функцией, а не соглашением, потому что соглашение
// нарушается ровно в той проверке, где применимое множество внезапно оказалось размером с карточку.
func Aggregate(applicable int, missing []CoverageMiss, agg AggregateFn) []Finding {
	if len(missing) == 0 {
		return nil
	}
	if len(missing) <= 3 {
		out := make([]Finding, 0, len(missing))
		for _, m := range missing {
			out = append(out, m.Finding)
		}
		return out
	}
	return []Finding{agg(len(missing), applicable, sampleRefs(missing))}
}

// sampleRefs takes the anchors of the first misses, deduplicated in order, capped at three.
func sampleRefs(missing []CoverageMiss) []string {
	out := make([]string, 0, 3)
	seen := make(map[string]bool, 3)
	for _, m := range missing {
		for _, r := range m.Refs {
			if seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
			if len(out) == 3 {
				return out
			}
		}
	}
	return out
}

// collapsedReadinessTitle is the title of the one finding a draft card's readiness class collapses
// into. Kept as a constant because the client's «draft» badge and the prompt renderer both key off
// this exact finding.
const collapsedReadinessTitle = "Not yet ready for release"

// CollapseReadiness folds every CategoryReadiness finding into ONE anchored on `card` when the card
// is a draft (design §3.0), leaving every other finding untouched and in place.
//
// Почему только на черновике. На черновике незаполненные SMV, работы, профили и лейблы — это НЕ
// дефекты, а ещё не сделанная работа, и шесть отдельных строк про них вытесняют с экрана
// единственную настоящую находку. На не-draft карточке тот же список — чек-лист выпуска, и он
// разворачивается целиком.
//
// Severity схлопнутой — МАКСИМУМ из схлопнутых: занизить его значило бы спрятать «операций ноль»
// (C1) за словом warning. Позиция — позиция ПЕРВОЙ readiness-находки, чтобы порядок остальных не
// поехал.
func CollapseReadiness(findings []Finding, draft bool) []Finding {
	if !draft {
		return findings
	}
	first := -1
	clauses := make([]string, 0, 8)
	worst, worstRank := "", severityRank("")+1
	for i := range findings {
		if findings[i].Category != CategoryReadiness {
			continue
		}
		if first < 0 {
			first = i
		}
		if c := strings.TrimSpace(findings[i].Clause); c != "" {
			clauses = append(clauses, c)
		}
		if r := severityRank(findings[i].Severity); r < worstRank {
			worst, worstRank = findings[i].Severity, r
		}
	}
	if first < 0 {
		return findings
	}

	detail := collapsedReadinessTitle
	if len(clauses) > 0 {
		// Разделитель — « · », как в образце §3.0. Не запятая: клаузы сами содержат дроби и
		// запятые, и перечисление через запятую читалось бы одним предложением.
		detail += ": " + strings.Join(clauses, " · ")
	}
	collapsed := Finding{
		Source:   SourceMachine,
		Category: CategoryReadiness,
		Severity: worst,
		Title:    collapsedReadinessTitle,
		Detail:   detail,
		Refs:     []string{RefCard},
	}

	out := make([]Finding, 0, len(findings))
	for i := range findings {
		if findings[i].Category == CategoryReadiness {
			if i == first {
				out = append(out, collapsed)
			}
			continue
		}
		out = append(out, findings[i])
	}
	return out
}

// severityRank orders severities for sorting and for picking the worst of a collapsed set:
// blocker (0) is the most severe. An unknown string sorts last — it can only be a bug, and a bug
// must not push itself to the top of the user's list.
func severityRank(s string) int {
	switch s {
	case SeverityBlocker:
		return 0
	case SeverityError:
		return 1
	case SeverityWarning:
		return 2
	default:
		return 3
	}
}

func itoa32(v int32) string { return strconv.Itoa(int(v)) }
