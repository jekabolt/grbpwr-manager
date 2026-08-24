package techcardanalysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── ВЕРИФИКАТОР ОТВЕТА МОДЕЛИ (design §8) ───────────────────────────────────────────────────────
//
// ФИЛОСОФИЯ, ОДНОЙ СТРОКОЙ: КОЭРЦИРУЙ УЗНАВАЕМЫЙ ДРЕЙФ, ДРОПАЙ ТОЛЬКО НЕРАЗРЕШИМОЕ (§8 п.2).
// Невалидный enum, «op 460» вместо «op:460», пробел после сигила — это дефекты ФОРМЫ, а не ложные
// утверждения о карточке; чинить их молча дешевле, чем выбрасывать находку, ради которой человек
// нажал кнопку. Ссылка, которую невозможно разрешить ОДНОЗНАЧНО, — другое дело: она врёт про
// карточку, и починить её нечем.
//
// ЧТО ЗДЕСЬ НЕ ПРОВЕРЯЕТСЯ И ПОЧЕМУ — `evidence` (§8 п.3). Оно display-only: у находки ОБ
// ОТСУТСТВИИ дословной подстроки в контексте нет по определению, а цитата цены «60 PLN» не обязана
// быть номером операции. Проверка evidence выбрасывала бы ровно те находки, ради которых слой
// существует. НИ ОДНА ветка этого файла не читает Evidence иначе, чем чтобы обрезать её длину.
//
// ЧИСТОТА. Ни БД, ни сети, ни часов, ни логгера. §8 п.9 требует, чтобы КАЖДЫЙ дроп попал в лог
// дословно и КАЖДАЯ коэрция была не молчаливой, — но `slog.Error` там же требует model, base_url и
// usage, которых у чистой функции нет и быть не может. Поэтому верификатор не логирует сам, а
// ОТДАЁТ материал: VerifyStats.Drops несёт текст каждой дропнутой находки и причину дословно,
// VerifyStats.Coercions — каждую применённую коэрцию. Обработчик (T16) печатает их одной записью,
// в которой есть и прогон, и деньги. Второй логгер здесь означал бы двойную запись каждого дропа.

// ErrInvalidOutput is the ONE error this verifier returns: the model half of the run is unusable
// and `ai_status` must become "invalid_output" (§8 п.5, п.8). Обёрнутое сообщение говорит, ЧТО
// именно случилось (нет JSON / битый JSON / обрыв по длине / порог смерти) — оно для лога, а не
// для разбора вызывающим: решение у вызывающего одно, и оно одно для всех четырёх причин.
//
// Ретрая нет намеренно (§8 п.8): повтор за деньги без диагноза — то же самое ещё раз.
var ErrInvalidOutput = errors.New("techcardanalysis: model output rejected")

// FinishReasonLength is the `finish_reason` OpenRouter reports when the answer was cut by the token
// ceiling (§8 п.8). Обрезанный JSON иногда всё-таки парсится — хвостовые скобки модель успевает
// дописать, — и тогда «успешный» разбор отдал бы половину ревью как целое. Признак сильнее парсера.
const FinishReasonLength = "length"

// MaxModelFindings — §8 п.7. Обрезаются ХВОСТОВЫЕ находки в порядке, в котором их выпустила модель:
// правило 9 §7.1 велит ей ставить важное первым, и пересортировка здесь подменила бы её приоритет
// нашим.
const MaxModelFindings = 15

// Порог смерти модельной половины (§8 п.5): дропнуто > max(dropFloor, 30% выпущенных).
//
// ОБА СЛАГАЕМЫХ ОБЯЗАТЕЛЬНЫ. Только доля — и на лаконичном прогоне из шести находок порог
// оказывается 1.8, то есть две неудачные ссылки убивают исправное ревью. Только пол — и прогон из
// сорока находок с двенадцатью дропами считается здоровым.
const (
	dropFloor     = 4
	dropShareNum  = 3  // 30% ...
	dropShareDen  = 10 // ... как целочисленная дробь: 10*dropped > 3*emitted
	dropReasonRef = "no ref resolved"
)

// Причины дропа, дословно в VerifyStats.Drops (§8 п.9).
const (
	// DropBadRef — у находки не разрешилась НИ ОДНА ссылка (§8 п.2). Счётчик — dropped_bad_ref.
	DropBadRef = "bad_ref"
	// DropContradiction — находка противоречит VERIFIED FACTS или повторяет уже поданную машинную
	// находку (§8 п.4). Счётчик — dropped_contradiction.
	DropContradiction = "contradiction"
)

// Капы текстов (§8 п.7). Число title взято из контракта провода (§4: «title <= 90 chars»);
// остальные — забор этого файла, потому что §8 говорит «тексты — aiBoundedText», не называя цифр.
// Колонка, в которую находка в итоге записывается человеком (tech_card_issue.description, TEXT),
// вмещает несравнимо больше — эти капы стоят не против БД, а против модели, решившей ответить
// эссе: пятикилобайтный detail в строке списка это не находка, а стена.
const (
	maxFindingTitleRunes      = 90
	maxFindingDetailRunes     = 1200
	maxFindingSuggestionRunes = 600
	maxEvidenceRunes          = 300
	maxSummaryRunes           = 600
	maxNotCheckedRunes        = 200
	maxNotCheckedItems        = 10
)

// Drop is one discarded finding, kept verbatim for the run log (§8 п.9). Титул и деталь — как их
// написала модель, ДО капов: в логе нужен её текст, а не наш обрезок.
type Drop struct {
	// Reason — DropBadRef | DropContradiction.
	Reason string
	// Title / Detail — дословный текст дропнутой находки.
	Title  string
	Detail string
	// Note — что именно не сошлось: «no ref resolved», «duplicate of filed machine finding
	// (op:470)», «topological claim denied by VERIFIED FACTS: circular dependency».
	Note string
	// Refs — ссылки, как их прислала модель (не разрешённые). Читателю лога нужно видеть ровно то,
	// что модель написала.
	Refs []string
}

// VerifyStats is everything the run needs to log and to put on the wire (§4: dropped_bad_ref,
// dropped_contradiction).
type VerifyStats struct {
	// Emitted — сколько находок модель ВЫПУСТИЛА (объектов в массиве findings), до всякой обработки.
	// Знаменатель порога §8 п.5.
	Emitted int
	// DroppedBadRef / DroppedContradiction — счётчики §4, едут на провод.
	DroppedBadRef        int
	DroppedContradiction int
	// Truncated — сколько находок срезал кап MaxModelFindings. НЕ дроп: находка не отвергнута, она
	// не поместилась, и в порог смерти §8 п.5 не входит.
	Truncated int
	// InvalidOutput — прогон отвергнут целиком. Дублирует err != nil, но переживает передачу
	// структуры отдельно от ошибки (лог, тест, ответ RPC).
	InvalidOutput bool
	// Drops — каждый дроп дословно (§8 п.9).
	Drops []Drop
	// Coercions — каждая применённая коэрция, человеческой строкой: «refs: "op 460" -> "op:460"»,
	// «unit key "BLAZER" -> "blazer" (case-insensitive)». §8 п.2 требует коэрцию ключа «с логом,
	// не молча» — вот этот лог.
	Coercions []string
}

// Dropped is the numerator of the death threshold: everything the verifier THREW AWAY.
func (s VerifyStats) Dropped() int { return s.DroppedBadRef + s.DroppedContradiction }

// VerifyModelOutput is the verifier of design §8, end to end and pure.
//
// Возвращает находки source="model", строки not_checked модели, её summary и статистику прогона.
// Ошибка — ВСЕГДА ErrInvalidOutput (обёрнутая): вызывающий ставит ai_status="invalid_output",
// выбрасывает модельную половину целиком и НЕ ТРОГАЕТ машинную секцию (§8 п.5).
//
// `machineFindings` — находки, УЖЕ поданные пользователю этим же прогоном. Они здесь не ради
// формы: дедуп §8 п.4 идёт по ПЕРЕСЕЧЕНИЮ множеств якорей с ними.
func VerifyModelOutput(raw string, card *cardView, machineFindings []Finding) (
	findings []Finding, notChecked []string, summary string, stats VerifyStats, err error,
) {
	out, perr := parseModelOutput(raw)
	if perr != nil {
		stats.InvalidOutput = true
		return nil, nil, "", stats, perr
	}

	res := newRefResolver(card)
	machineAnchors := anchorSets(machineFindings)
	money := newMoneyScreen(card, machineFindings)
	emitted := *out.Findings
	stats.Emitted = len(emitted)

	kept := make([]Finding, 0, len(emitted))
	for i := range emitted {
		mf := &emitted[i]

		category, severity, confidence := coerceEnums(mf, &stats)

		refs, refNotes, bad := res.resolveAll(mf.Refs)
		stats.Coercions = append(stats.Coercions, refNotes...)
		if len(refs) == 0 {
			stats.DroppedBadRef++
			stats.Drops = append(stats.Drops, Drop{
				Reason: DropBadRef, Title: mf.Title, Detail: mf.Detail,
				Note: dropReasonRef + joinNotes(bad), Refs: mf.Refs,
			})
			continue
		}

		if note, contradicts := contradiction(mf, refs, card, machineAnchors); contradicts {
			stats.DroppedContradiction++
			stats.Drops = append(stats.Drops, Drop{
				Reason: DropContradiction, Title: mf.Title, Detail: mf.Detail,
				Note: note, Refs: mf.Refs,
			})
			continue
		}

		f := Finding{
			Source:      SourceModel,
			Category:    category,
			Severity:    severity,
			Title:       aiBoundedText(mf.Title, maxFindingTitleRunes),
			Detail:      aiBoundedText(mf.Detail, maxFindingDetailRunes),
			Evidence:    boundedList(mf.Evidence, maxEvidenceRunes, 0),
			Refs:        refs,
			InsertAfter: coerceInsertAfter(mf.InsertAfter, category, res, &stats),
			Suggestion:  aiBoundedText(mf.Suggestion, maxFindingSuggestionRunes),
			Confidence:  confidence,
		}
		// ДЕНЕЖНЫЙ СКРИН — ПО ГОТОВОЙ НАХОДКЕ И ПОСЛЕДНИМ ДЕЙСТВИЕМ. Скрин обязан читать ровно то,
		// что уедет читателю: категорию ПОСЛЕ коэрции и тексты ПОСЛЕ капов §8 п.7. Поставить флаг
		// по сырому modelFinding значило бы судить о раскрытии по строке, которой на экране не
		// будет.
		f.Money = money.flags(&f)
		kept = append(kept, f)
	}

	// Порог смерти (§8 п.5) — ДО обрезки: знаменатель это выпущенное моделью, а не то, что
	// поместилось на экран.
	if dead := stats.Dropped(); dead > dropFloor && dropShareDen*dead > dropShareNum*stats.Emitted {
		stats.InvalidOutput = true
		return nil, nil, "", stats, fmt.Errorf(
			"%w: %d of %d findings dropped (%d bad ref, %d contradiction), over the max(%d, 30%%) threshold",
			ErrInvalidOutput, dead, stats.Emitted, stats.DroppedBadRef, stats.DroppedContradiction, dropFloor)
	}

	if len(kept) > MaxModelFindings {
		stats.Truncated = len(kept) - MaxModelFindings
		kept = kept[:MaxModelFindings]
	}

	return kept, boundedList(out.NotChecked, maxNotCheckedRunes, maxNotCheckedItems),
		aiBoundedText(out.Summary, maxSummaryRunes), stats, nil
}

// VerifyModelRun is the entry point the RPC handler calls (T16), and it exists for two reasons the
// signature of VerifyModelOutput cannot carry.
//
//  1. `finish_reason` (§8 п.8) не является частью текста ответа: он приезжает рядом с ним из
//     CompleteWithMeta. Проверять его ВНУТРИ верификатора нельзя — верификатор видит только текст;
//     оставить его обработчику значило бы, что пункт §8 живёт вне того файла, где его тестируют.
//  2. `cardView` НЕЭКСПОРТИРУЕМ — это внутренний разбор карточки, и правильно, что он такой. Но
//     обработчик живёт в другом пакете и построить его не может; без этой обёртки экспортированный
//     VerifyModelOutput был бы недостижим извне.
//
// Форма аргументов — та же, что у RunAudit (карточка + курсы), чтобы обработчик не держал двух
// разных представлений одного прогона.
func VerifyModelRun(raw, finishReason string, card *entity.TechCard, fx Fx, machineFindings []Finding) (
	findings []Finding, notChecked []string, summary string, stats VerifyStats, err error,
) {
	if strings.EqualFold(strings.TrimSpace(finishReason), FinishReasonLength) {
		stats.InvalidOutput = true
		return nil, nil, "", stats, fmt.Errorf(
			"%w: the answer was cut by the token ceiling (finish_reason=%q)", ErrInvalidOutput, finishReason)
	}
	return VerifyModelOutput(raw, newCardView(card, fx), machineFindings)
}

// ── РАЗБОР ─────────────────────────────────────────────────────────────────────────────────────

// modelOutput is the JSON object §7.1 asks the model for.
type modelOutput struct {
	// Findings — УКАЗАТЕЛЬ намеренно: «findings отсутствует» и «findings пуст» — разные ответы.
	// Пустой список это ЗАКОННЫЙ и полный ответ (§7.1: «An empty findings list is a correct and
	// complete answer»); отсутствие ключа — ответ не по схеме, и принять его за «модель ничего не
	// нашла» значило бы нарисовать чистую карточку там, где модель ответила что-то другое.
	Findings   *[]modelFinding `json:"findings"`
	NotChecked stringList      `json:"not_checked"`
	Summary    string          `json:"summary"`
}

// modelFinding is one raw finding as it came off the wire — every field a string or a list of them,
// nothing coerced yet.
type modelFinding struct {
	Category    string     `json:"category"`
	Severity    string     `json:"severity"`
	Title       string     `json:"title"`
	Detail      string     `json:"detail"`
	Evidence    stringList `json:"evidence"`
	Refs        stringList `json:"refs"`
	InsertAfter string     `json:"insert_after"`
	Suggestion  string     `json:"suggestion"`
	Confidence  string     `json:"confidence"`
}

// stringList accepts the three shapes a model actually writes where the schema says «array of
// strings»: the array, a BARE STRING (`"refs": "op:460"`), and an array with numbers in it
// (`"refs": [460]`). Все три — дрейф формы, а не ложь о карточке, и ронять из-за них ВЕСЬ прогон
// в invalid_output значило бы применить к форме наказание, придуманное для содержания.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*l = nil
		return nil
	}
	if b[0] != '[' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = stringList{s}
		return nil
	}
	var out []string
	if err := json.Unmarshal(b, &out); err == nil {
		*l = out
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out = make([]string, 0, len(raw))
	for _, r := range raw {
		var s string
		if json.Unmarshal(r, &s) == nil {
			out = append(out, s)
			continue
		}
		out = append(out, strings.Trim(strings.TrimSpace(string(r)), `"`))
	}
	*l = out
	return nil
}

// parseModelOutput extracts the JSON object and unmarshals it (§8 п.8).
func parseModelOutput(raw string) (modelOutput, error) {
	js := extractAnalysisJSON(raw)
	if js == "" {
		return modelOutput{}, fmt.Errorf("%w: no JSON object in the model output (%q)",
			ErrInvalidOutput, aiBoundedText(raw, 200))
	}
	var out modelOutput
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return modelOutput{}, fmt.Errorf("%w: the model output is not valid analysis JSON: %v",
			ErrInvalidOutput, err)
	}
	if out.Findings == nil {
		return modelOutput{}, fmt.Errorf("%w: the model output has no \"findings\" key", ErrInvalidOutput)
	}
	return out, nil
}

// extractAnalysisJSON returns the outermost {...} object in s, first stripping a Markdown code
// fence — fences and surrounding prose are tolerated (§8, «фенсы и проза терпимы»).
//
// ПОВТОР ЛОГИКИ openrouter.extractJSON, А НЕ ВЫЗОВ ЕЁ. Та функция неэкспортируема, а
// экспортировать её ради этого пакета значило бы поменять чужой файл, который прямо сейчас правят
// параллельные задачи (§6 «Заборы» — та же причина, по которой aiBoundedText здесь копия).
// Поведение обязано совпадать: расхождение парсеров означало бы, что один и тот же ответ модели
// принимается генератором и отвергается анализом.
func extractAnalysisJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:] // строка с языковым тегом («json»)
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// ── КОЭРЦИЯ ENUM'ОВ (§8 п.1) ───────────────────────────────────────────────────────────────────

// coerceEnums maps category/severity/confidence onto the closed lists, recording every correction.
//
// РЕГИСТР КОЭРЦИРУЕТСЯ МОЛЧА («Blocker» → «blocker»): это не дрейф смысла, а дрейф написания, и
// строка лога на каждую заглавную букву утопила бы настоящие коэрции.
func coerceEnums(mf *modelFinding, stats *VerifyStats) (category, severity, confidence string) {
	category = strings.ToLower(strings.TrimSpace(mf.Category))
	severity = strings.ToLower(strings.TrimSpace(mf.Severity))
	confidence = strings.ToLower(strings.TrimSpace(mf.Confidence))

	// `question` — КАТЕГОРИЯ, НЕ SEVERITY (§7.1 п.3, §8 п.1). Модель, поставившая его в severity,
	// сказала «это вопрос», а не «это неважно», и перевести её надо в то, что она имела в виду:
	// severity становится warning, категория — question. Проверяется ПЕРВОЙ, иначе общая коэрция
	// severity уже стёрла бы слово, по которому это видно.
	if severity == CategoryQuestion {
		stats.Coercions = append(stats.Coercions,
			fmt.Sprintf("severity %q is a category, not a severity: severity=%s, category=%s (%q)",
				mf.Severity, SeverityWarning, CategoryQuestion, mf.Title))
		return CategoryQuestion, SeverityWarning, coerceConfidence(confidence, mf, stats)
	}

	if !ValidModelCategories[category] {
		stats.Coercions = append(stats.Coercions,
			fmt.Sprintf("category %q is not one of the eight: coerced to %s (%q)",
				mf.Category, CategoryQuestion, mf.Title))
		category = CategoryQuestion
	}
	if !ValidSeverities[severity] {
		stats.Coercions = append(stats.Coercions,
			fmt.Sprintf("severity %q is not one of the three: coerced to %s (%q)",
				mf.Severity, SeverityWarning, mf.Title))
		severity = SeverityWarning
	}
	return category, severity, coerceConfidence(confidence, mf, stats)
}

func coerceConfidence(confidence string, mf *modelFinding, stats *VerifyStats) string {
	if ValidModelConfidences[confidence] {
		return confidence
	}
	stats.Coercions = append(stats.Coercions,
		fmt.Sprintf("confidence %q is not one of the three: coerced to %s (%q)",
			mf.Confidence, ConfidenceLikely, mf.Title))
	return ConfidenceLikely
}

// ── ССЫЛКИ: ГРАММАТИКА, КОЭРЦИЯ, РАЗРЕШЕНИЕ (§8 п.2) ───────────────────────────────────────────

// Сигилы, которые модель пишет вместо канонических. Множественное число и слово «operation»
// целиком — ровно тот узнаваемый дрейф, который §8 велит коэрцировать.
var refSigilAliases = map[string]string{
	"op": refOpPrefix, "ops": refOpPrefix, "operation": refOpPrefix, "operations": refOpPrefix,
	"unit": refUnitPrefix, "units": refUnitPrefix,
	"piece": refPiecePrefix, "pieces": refPiecePrefix,
	"bom": refBomPrefix,
}

// refResolver answers the only question §8 п.2 asks about a ref: does it name something that is
// ACTUALLY ON THIS CARD, and does it name it unambiguously.
type refResolver struct {
	ops   map[int32]bool
	units *nameIndex
	piece *nameIndex
	bom   *nameIndex
}

func newRefResolver(card *cardView) *refResolver {
	r := &refResolver{
		ops:   map[int32]bool{},
		units: newNameIndex(),
		piece: newNameIndex(),
		bom:   newNameIndex(),
	}
	if card == nil {
		return r
	}
	for _, s := range card.gt.Steps {
		if s.NumberValid {
			r.ops[s.OperationNumber] = true
		}
	}
	for key := range card.gt.Units {
		r.units.add(key)
	}
	if card.card != nil {
		for i := range card.card.Pieces {
			r.piece.add(card.card.Pieces[i].Name)
		}
		for i := range card.card.BomItems {
			r.bom.add(card.card.BomItems[i].Name)
		}
	}
	return r
}

// resolveAll turns the model's ref list into canonical sigils, in the model's own order and without
// duplicates. `notes` are everything the verifier DID to the list — every coercion AND every dropped
// ref; `bad` are just the dead ones, for the drop record of a finding that loses all of them.
//
// ПОЧЕМУ ДРОП ССЫЛКИ ПОПАДАЕТ В `notes` ВСЕГДА, А НЕ ТОЛЬКО КОГДА УМИРАЕТ НАХОДКА. Дроп ОДНОЙ
// ссылки у выжившей находки нигде больше не виден: счётчики §4 считают находки, а не ссылки, и
// находка приезжает на экран с тремя якорями вместо четырёх, ничем не показывая, что четвёртый был.
// Именно этого случая касается требование §8 п.2 «коэрция с логом, не молча»: неуникальный «BASE»
// на карточке с «Base» и «base» — это исчезнувший якорь, и узнать о нём можно только отсюда.
func (r *refResolver) resolveAll(refs []string) (out []string, notes []string, bad []string) {
	seen := make(map[string]bool, len(refs))
	for _, raw := range refs {
		canon, note, ok := r.resolve(raw)
		switch {
		case ok && note != "":
			notes = append(notes, "ref "+strconv.Quote(raw)+" -> "+strconv.Quote(canon)+" ("+note+")")
		case !ok:
			if note == "" {
				note = "names nothing on this card"
			}
			reason := strconv.Quote(raw) + ": " + note
			bad = append(bad, reason)
			notes = append(notes, "ref dropped — "+reason)
		}
		if !ok || seen[canon] {
			continue
		}
		seen[canon] = true
		out = append(out, canon)
	}
	return out, notes, bad
}

// resolve applies the coercion table of §8 п.2 to ONE ref.
//
//	"op:460" / "op 460" / "operation:460" / "460" / "op #460"  → "op:460", если такая операция есть
//	"unit: base"                                               → "unit:base" (трим после сигила)
//	"unit:BLAZER" при единственном «blazer»                    → "unit:blazer" + запись коэрции
//	"unit:BASE" на карточке с «Base» И «base»                  → НЕ разрешается: дроп ссылки
//	"card" / "CARD"                                            → "card"
//
// Голое число разрешается ТОЛЬКО как номер существующей операции: пространство номеров — единственное
// в карточке, где голая цифра имеет однозначный смысл. Голое СЛОВО не коэрцируется ни во что: «base»
// без сигила это законное имя и узла, и детали, и строки BOM одновременно, и угадывать здесь значило
// бы поставить якорь на чужой объект — ровно тот отказ, от которого §8 велит защищаться дропом.
func (r *refResolver) resolve(raw string) (canon, note string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	if strings.EqualFold(s, RefCard) {
		return RefCard, coercionNote(s != RefCard, "case"), true
	}

	head, rest, split := splitSigil(s)
	if split {
		if prefix, known := refSigilAliases[head]; known {
			return r.resolveTyped(prefix, rest, s)
		}
		return "", "", false
	}

	// Голое число — номер операции.
	if n, err := parseOpNumber(s); err == nil {
		if r.ops[n] {
			return refOpPrefix + itoa32(n), "bare operation number", true
		}
		return "", fmt.Sprintf("operation %d is not on this card", n), false
	}
	return "", "", false
}

// resolveTyped resolves the part after a recognised sigil.
func (r *refResolver) resolveTyped(prefix, rest, whole string) (canon, note string, ok bool) {
	switch prefix {
	case refOpPrefix:
		n, err := parseOpNumber(rest)
		if err != nil {
			return "", "not an operation number", false
		}
		if !r.ops[n] {
			return "", fmt.Sprintf("operation %d is not on this card", n), false
		}
		canon = refOpPrefix + itoa32(n)
		return canon, coercionNote(canon != whole, "operation sigil"), true
	case refUnitPrefix:
		return resolveName(r.units, refUnitPrefix, rest, whole, "unit key")
	case refPiecePrefix:
		return resolveName(r.piece, refPiecePrefix, rest, whole, "piece name")
	case refBomPrefix:
		return resolveName(r.bom, refBomPrefix, rest, whole, "BOM line name")
	}
	return "", "", false
}

func resolveName(idx *nameIndex, prefix, rest, whole, what string) (canon, note string, ok bool) {
	key, how, ok := idx.resolve(rest)
	if !ok {
		if how != "" {
			return "", what + " " + strconv.Quote(strings.TrimSpace(rest)) + " " + how, false
		}
		return "", what + " " + strconv.Quote(strings.TrimSpace(rest)) + " is not on this card", false
	}
	canon = prefix + key
	if how == "" {
		return canon, coercionNote(canon != whole, "sigil"), true
	}
	return canon, what + " " + how, true
}

// splitSigil cuts a ref into its sigil head and the rest, accepting both the colon of the grammar
// and the space of a model writing prose («op 460»).
//
// Двоеточие ищется ПЕРВЫМ и по ПЕРВОМУ вхождению: ключ узла может содержать что угодно, включая
// пробелы («left lining with pocket»), и резать по пробелу раньше двоеточия означало бы разрубить
// такой ключ пополам.
func splitSigil(s string) (head, rest string, ok bool) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(s[:i])), s[i+1:], true
	}
	if i := strings.IndexFunc(s, unicode.IsSpace); i > 0 {
		return strings.ToLower(strings.TrimSpace(s[:i])), s[i:], true
	}
	return "", "", false
}

// parseOpNumber reads an operation number, tolerating the surrounding whitespace and the '#' a
// model writes when it is quoting a step the way a human does («op #460»).
func parseOpNumber(s string) (int32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

func coercionNote(applied bool, what string) string {
	if applied {
		return what
	}
	return ""
}

func joinNotes(bad []string) string {
	if len(bad) == 0 {
		return ""
	}
	return " (" + strings.Join(bad, "; ") + ")"
}

// nameIndex resolves a key the way §8 п.2 requires: byte-exact first, and only then case-insensitive
// AND ONLY WHEN THE FOLD IS UNIQUE.
//
// ПОЧЕМУ УНИКАЛЬНОСТЬ — НЕСУЩЕЕ УСЛОВИЕ, А НЕ ПРЕДОСТОРОЖНОСТЬ. Карточка 8 несёт узлы «Base» (оп
// 270) и «base» (оп 450) одновременно — это НАСТОЯЩАЯ находка A1, ради которой машинный слой
// существует. Модельный «unit:BASE» на такой карточке указывает на два разных узла; поставить его
// на любой из них значило бы стереть саму находку, о которой идёт речь. Поэтому — дроп ссылки, с
// записью в лог.
type nameIndex struct {
	exact map[string]bool
	fold  map[string]map[string]bool
}

func newNameIndex() *nameIndex {
	return &nameIndex{exact: map[string]bool{}, fold: map[string]map[string]bool{}}
}

func (n *nameIndex) add(key string) {
	if key == "" {
		return
	}
	n.exact[key] = true
	lower := strings.ToLower(key)
	if n.fold[lower] == nil {
		n.fold[lower] = map[string]bool{}
	}
	n.fold[lower][key] = true
}

// resolve returns the byte-exact key this ref names. `how` is the coercion applied when ok, and the
// reason it failed when not ok.
func (n *nameIndex) resolve(raw string) (key, how string, ok bool) {
	if n.exact[raw] {
		return raw, "", true
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", false
	}
	if n.exact[trimmed] {
		return trimmed, "", true
	}
	cands := n.fold[strings.ToLower(trimmed)]
	switch len(cands) {
	case 0:
		return "", "", false
	case 1:
		for k := range cands {
			return k, "resolved case-insensitively to " + strconv.Quote(k), true
		}
	}
	return "", "matches " + strconv.Itoa(len(cands)) + " keys case-insensitively (" +
		strings.Join(quotedSortedKeys(cands), ", ") + ") and is therefore not resolvable", false
}

func quotedSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, strconv.Quote(k))
	}
	sort.Strings(out)
	return out
}

// ── insert_after (§8 п.6) ──────────────────────────────────────────────────────────────────────

// insertAfterStart is the one non-operation value of the field: the missing step belongs before the
// first operation of the route.
const insertAfterStart = "start"

// coerceInsertAfter narrows the field to its grammar: "op:<int>" of an EXISTING operation, or
// "start", or empty. Всё прочее — пустая строка: НАХОДКА ЖИВЁТ, не рисуется только вставка (§8 п.6).
// Место вставки — подсказка, а не содержание находки, и убивать находку из-за неверного номера
// значило бы поменять их местами.
//
// Поле существует ТОЛЬКО у missing_step (§7.1 п.7, §4). У находки, чья категория после коэрции —
// не missing_step, оно очищается: клиент рисует по нему стрелку вставки, и стрелка «вставить после
// оп 120» на находке о названии узла — это указание сделать то, чего находка не предлагает.
func coerceInsertAfter(raw, category string, res *refResolver, stats *VerifyStats) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if category != CategoryMissingStep {
		stats.Coercions = append(stats.Coercions,
			fmt.Sprintf("insert_after %q dropped: it belongs to %s only, this finding is %s",
				raw, CategoryMissingStep, category))
		return ""
	}
	if strings.EqualFold(s, insertAfterStart) {
		return insertAfterStart
	}
	canon, _, ok := res.resolve(s)
	if ok && strings.HasPrefix(canon, refOpPrefix) {
		if canon != raw {
			stats.Coercions = append(stats.Coercions,
				fmt.Sprintf("insert_after %q -> %q", raw, canon))
		}
		return canon
	}
	stats.Coercions = append(stats.Coercions,
		fmt.Sprintf("insert_after %q is outside the grammar (op:<existing> | start): cleared", raw))
	return ""
}

// ── ПРОТИВОРЕЧИЯ VERIFIED FACTS И ДЕДУП (§8 п.4) ───────────────────────────────────────────────

// topologyClaims are the assertions the WRITE PATH makes unrepresentable (design §1: cycles,
// forward references, duplicate producers, double consumption, dangling inputs are rejected by
// canonicalizeAssembly on every save). Промпт подаёт это как закрытый мир — «граф ацикличен, ссылок
// вперёд и висячих ссылок нет», — и находка, утверждающая обратное, противоречит VERIFIED FACTS.
//
// СПИСОК УЗКИЙ НАМЕРЕННО, И ЭТО НЕ ЛЕНЬ. Ложный дроп здесь дороже пропуска: он молча стирает
// настоящую находку, и человек об этом не узнает. Фразы взяты из словаря, которым сам промпт
// описывает топологию, и совпадать обязана ЦЕЛАЯ фраза, а не слово: «loop» в «loop the thread» —
// не заявление о графе.
var topologyClaims = []string{
	"circular dependency",
	"circular reference",
	"cycle in the assembly",
	"cyclic dependency",
	"forward reference",
	"consumed twice",
	"consumed by two operations",
	"consumed by two different operations",
	"double-consumed",
	"produced twice",
	"produced by two operations",
	"duplicate producer",
	"dangling input",
	"dangling reference",
}

// anchorSets turns findings into their anchor sets, `card` EXCLUDED.
//
// ПОЧЕМУ `card` НЕ ЯКОРЬ ДЛЯ ДЕДУПА. Он означает «находка не про конкретный шаг» — это отсутствие
// адреса, а не адрес. Машинный слой ставит его на схлопнутую readiness-находку КАЖДОГО черновика
// (§3.0), поэтому засчитывать его в пересечение значило бы объявить дублем любую модельную находку
// о карточке целиком — включая блокер «во всём маршруте нет клеевого блока», который §14 требует от
// каждого приёмочного прогона именно на якоре `card`. Дедуп, срезающий обязательную находку
// приёмки, — не дедуп.
func anchorSets(findings []Finding) []map[string]bool {
	out := make([]map[string]bool, 0, len(findings))
	for i := range findings {
		set := make(map[string]bool, len(findings[i].Refs))
		for _, r := range findings[i].Refs {
			if r == RefCard {
				continue
			}
			set[r] = true
		}
		if len(set) > 0 {
			out = append(out, set)
		}
	}
	return out
}

// contradiction is §8 п.4: a topological claim the recomputation denies, or a re-filing of a
// finding the machine layer has already put on the user's screen.
func contradiction(mf *modelFinding, refs []string, card *cardView, machine []map[string]bool) (string, bool) {
	if claim, found := topologyClaim(mf, card); found {
		return "topological claim denied by VERIFIED FACTS: " + strconv.Quote(claim), true
	}
	// Дедуп по ПЕРЕСЕЧЕНИЮ множеств якорей, а не по заголовку и не по паре якорь+класс (§8 п.4):
	// машинная пара 70/100 и модельный method-вердикт о ней — ОДИН факт, названный двумя словами,
	// и сравнение заголовков не поймало бы этого никогда.
	// `card` отсутствует в множествах machine по построению (см. anchorSets) — и это ЕДИНСТВЕННОЕ
	// место, где он оттуда убран. Второй такой же фильтр здесь выглядел бы страховкой, а на деле
	// сделал бы правило непроверяемым: сломав одну половину, тест остался бы зелёным на второй.
	for _, set := range machine {
		for _, r := range refs {
			if !set[r] {
				continue
			}
			return "re-files a machine finding already on screen (shared anchor " + strconv.Quote(r) + ")", true
		}
	}
	return "", false
}

// topologyClaim reports whether the finding asserts something the closed world denies.
//
// ЗАМОК: если пересчёт САМ нашёл нарушения (карточка записана мимо конвертера — легаси-строка,
// out-of-band запись), то VERIFIED FACTS чистоту НЕ УТВЕРЖДАЮТ, и противоречить тут нечему. В этот
// момент модель, заметившая цикл, вероятно права, и дроп её находки был бы худшим из возможных:
// молчаливым удалением правды на единственной карточке, где она редкая.
func topologyClaim(mf *modelFinding, card *cardView) (string, bool) {
	if card == nil || len(card.gt.Violations) > 0 {
		return "", false
	}
	hay := strings.ToLower(mf.Title + "\n" + mf.Detail + "\n" + mf.Suggestion)
	for _, claim := range topologyClaims {
		if strings.Contains(hay, claim) {
			return claim, true
		}
	}
	return "", false
}

// ── ДЕНЕЖНЫЙ СКРИН МОДЕЛЬНЫХ НАХОДОК (design §12, T15) ─────────────────────────────────────────
//
// ЗАЧЕМ ОН СУЩЕСТВУЕТ. Аудит классифицирован rd(SectionTechCards), и эта аудитория ШИРЕ костинговой:
// аккаунт с tech_cards:read и без costing:read до RPC доходит, а GetTechCard тому же аккаунту отдаёт
// карточку с вырезанными unit_price и currency (stripTechCardCosting). Машинная половина границу уже
// держит — Finding.Money ставится РЯДОМ С ПРОВЕРКОЙ (bom.go, три места), обработчик режет по флагу
// (redactMoneyFindings). Модельная не держала её ничем, а промпт кладёт закупочные цены в контекст
// НАМЕРЕННО (⚠️ ДЕНЬГИ на PromptBomLine.Price): без них из ревью исчезает целый класс находок,
// отданный §2 модели. Значит, модель может процитировать цену В ЛЮБОЙ своей находке, и без скрина
// такая находка проезжает ровно тот фильтр, который ради неё и построен.
//
// ГРАНИЦА — ТА ЖЕ, ЧТО У stripTechCardCosting: ВЕЛИЧИНА, ВАЛЮТА И ОТНОШЕНИЕ ВЕЛИЧИН — ДЕНЬГИ; ИМЯ
// НЕДОСТАЮЩЕГО ФАКТА — НЕ ДЕНЬГИ. Тот файл осознанно оставляет видимыми `price_source` и причину
// `no_price`, и по тому же правилу «у подкладочной строки не проставлена цена» деньгами здесь НЕ
// является: это не цена, а её отсутствие, и технолог, которому карточку чинить, обязан это видеть.
//
// ЦЕНА ОБЕИХ ОШИБОК, И ГДЕ ПРОВЕДЕНА ЧЕРТА (ограничение 1). Недофлаг публикует закупочную цену
// аккаунту, у которого её вырезают из самой карточки: утечка тихая и необратимая. Перефлаг прячет
// настоящую находку от технолога, который на неё имеет право, и прячет НЕОТЛИЧИМО ОТ «находки нет»
// — обработчик дропает находку целиком, оставляя одну и ту же неизменную строку not_checked.
// Поэтому черта проведена НЕ по «есть ли в тексте цифра» (в находке о маршруте цифра есть почти
// всегда — это номер шага) и НЕ по «есть ли якорь bom:» (это адрес, а не деньги), а по трём
// узнаваемым ФОРМАМ РАСКРЫТИЯ. Каждая требует либо одного однозначного признака, либо ДВУХ
// совпавших:
//
//	1. НАЗВАНА ВАЛЮТА, КОТОРОЙ КАРТОЧКА МОГЛА НАУЧИТЬ, ЛИБО ЗНАК ВАЛЮТЫ — единица величины;
//	2. ЦЕНОВОЕ СЛОВО + ЧИСЛО ПРИ НЁМ                                    — сама величина;
//	3. ЦЕНОВОЕ СЛОВО + СРАВНЕНИЕ                                        — ОТНОШЕНИЕ, БЕЗ ЦИФР.
//
// ПУНКТ 3 — ЭТО ОТВЕТ НА КЛАСС «ОТНОШЕНИЕ БЕЗ ВЕЛИЧИН» (ограничение 3). «The pocketing costs more
// per metre than the main fabric» не содержит ни числа, ни валюты, но сообщает ПОРЯДОК закупочных
// цен двух строк. Обработчик уже отказался публиковать это половинчато — «показать находку без
// цифр» рассмотрено и отвергнуто в redactMoneyFindings теми же словами и на этом же примере, — и
// если бы скрин ловил только цифры, запрет держался бы ровно на машинной половине, а на модельной
// не держался бы вовсе.
//
// ЯКОРЯ НЕ ЧИТАЮТСЯ ВОВСЕ, И ЭТО РЕШЕНИЕ (ограничение 2). Ссылка `bom:<имя>` — АДРЕС. «The lining
// BOM line has no material linked» цитирует имя строки, которое тот же аккаунт видит в GetTechCard,
// и денег в ней нет ни одной формы; спрятать её значило бы перефлагнуть ровно тот случай, ради
// которого граница проведена по СОДЕРЖАНИЮ, а не по разделу карточки.

const (
	// moneyBindRunes — сколько рун между ценовым словом и числом ещё считается «число ПРИ этом
	// слове». Двенадцать — это «priced at 1.00», «costs 60 per metre», «at a price of 55», и это
	// НЕ «4 fabric lines are unpriced» (восемнадцать рун между «4» и «unpriced»), то есть счёт
	// НЕДОСТАЮЩИХ фактов остаётся видимым.
	moneyBindRunes = 12
	// minCurrencyCodeRunes — короче этого строка валюты матчером не становится (см. addCurrency).
	minCurrencyCodeRunes = 3
)

// moneyPriceWords — вокабуляр ДЕНЕГ, и это ЕДИНСТВЕННЫЙ список констант в этом разделе.
//
// ПОЧЕМУ ИМЕННО ОН — СПИСОК, А ВАЛЮТЫ — НЕТ (ограничение 4). Множество валют растёт с каждым новым
// поставщиком, и список валют в этом файле был бы местом, куда новая валюта не попадёт: карточка в
// CZK утекла бы целиком и молча. Множество английских слов о цене не растёт ни от одного изменения
// карточки, вывести его из карточки неоткуда, и заменить его на «любое слово» нельзя — «no price on
// the lining line» это ИМЯ НЕДОСТАЮЩЕГО ФАКТА и по границе stripTechCardCosting деньгами не
// является.
//
// Формы перечислены явно, а не стеммингом по «cost»/«price»: «costume» — законное слово этой
// предметной области, и префиксный матч пометил бы деньгами находку про костюм.
var moneyPriceWords = []string{
	"price", "prices", "priced", "unpriced", "pricing", "pricey", "pricier", "priciest",
	"cost", "costs", "costed", "costing", "costly",
	"cheap", "cheaper", "cheapest", "expensive", "dear", "dearer", "dearest",
	"margin", "markup",
}

// moneyComparators — слова, которыми ОТНОШЕНИЕ величин выражается без самих величин.
//
// Голые «more», «over», «above» сюда НЕ входят: они несут сравнение слишком часто и ни разу
// однозначно («more passes than needed» — не про деньги), и вместе с ценовым словом дали бы
// перефлаг на ровном месте. «than» несёт ровно сравнение и ничего больше; сравнительные и
// превосходные степени самих ценовых прилагательных стоят в обоих списках намеренно — «the dearest
// fabric line» называет ПОРЯДОК, не назвав ни одной цифры.
var moneyComparators = []string{
	"than", "twice", "double", "doubled", "half", "percent", "ratio", "exceed", "exceeds",
	"cheaper", "cheapest", "dearer", "dearest", "pricier", "priciest",
}

// moneyOpAnchors — слова, после которых число это АДРЕС ШАГА, а не величина.
//
// Грамматика та же, которой ссылки читают splitSigil и parseOpNumber, и это не совпадение: «the
// lining line at op 470 has no price» ОБЯЗАНО остаться видимым (имя недостающего факта), а без
// этой оговорки число 470 стояло бы в восьми рунах от слова «price» и находка ушла бы в деньги.
var moneyOpAnchors = map[string]bool{
	"op": true, "ops": true, "operation": true, "operations": true, "step": true, "steps": true,
}

// moneyScreen is the per-run screen: what this card could have taught the model about money, and
// whether it taught it anything at all.
type moneyScreen struct {
	// armed — промпт ЭТОГО прогона мог научить модель денежному факту. Пока false, скрин молчит
	// (ограничение 5): ценового блока в контексте не было, и цитировать модели нечего.
	armed bool
	// currencies — коды валют ЭТОЙ карточки плюс база контура, заглавными.
	currencies map[string]bool
}

// newMoneyScreen derives the screen from the card and from the run's own machine findings.
//
// ВЗВОД СЧИТАЕТСЯ ПО ФАКТУ РЕНДЕРА И ТОЙ ЖЕ ФУНКЦИЕЙ, КОТОРАЯ РИСУЕТ ПРОМПТ (promptBomLineOf).
// Правило «цена печатается, только когда есть И величина, И валюта» написанное здесь заново — это
// два правила, которые однажды разойдутся, и разойдутся МОЛЧА: скрин решит, что цен в промпте не
// было, ровно тогда, когда они там были. Тот же довод, по которому PromptContext.PricesIncluded
// считается по факту рендера, а не по «есть ли в карточке цены».
//
// ВТОРОЕ СЛАГАЕМОЕ ВЗВОДА — ДЕНЕЖНЫЕ МАШИННЫЕ НАХОДКИ ЭТОГО ЖЕ ПРОГОНА, и оно закрывает ВТОРОЙ
// канал, которого PricesIncluded не видит. Блок FILED промпта (buildFiled) кладёт в контекст ВСЕ
// поданные машинные находки дословно, включая B5а/б/в с ценами в заголовке и детали. Карточка, где
// у строк BOM есть величина, но нет валюты, ценового блока не печатает вовсе — и всё равно печатает
// в FILED находку «„подкладка“ is priced at zero». Оба слагаемых — данные, уже лежащие у
// верификатора в руках: machineFindings это ровно то, из чего собран FILED.
func newMoneyScreen(card *cardView, machineFindings []Finding) *moneyScreen {
	m := &moneyScreen{currencies: map[string]bool{}}
	for i := range machineFindings {
		if machineFindings[i].Money {
			m.armed = true
			break
		}
	}
	if card == nil {
		return m
	}
	// База контура — валюта, в которой машинные проверки называют итог («PLN has no rate to EUR»),
	// и модель видит её в FILED даже на карточке, где ни одна строка в базе не номинирована.
	m.addCurrency(card.fx.Base)
	if card.card == nil {
		return m
	}
	for i := range card.card.BomItems {
		b := &card.card.BomItems[i]
		m.addCurrency(b.Currency.String)
		if promptBomLineOf(b).Price != "" {
			m.armed = true
		}
	}
	return m
}

// addCurrency keeps a code only if it can be matched as a word at all.
//
// Однобуквенный огрызок или строка с цифрой внутри стали бы матчером, срабатывающим на прозе («e» в
// каждом втором слове), и такой «фильтр» глушил бы ревью целиком вместо того, чтобы прятать деньги
// — то есть ровно перефлаг, только тотальный. Поле currency в этой схеме несёт код ISO; всё, что на
// код не похоже, матчить нечем.
func (m *moneyScreen) addCurrency(code string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len([]rune(code)) < minCurrencyCodeRunes {
		return
	}
	for _, r := range code {
		if !unicode.IsLetter(r) {
			return
		}
	}
	m.currencies[code] = true
}

// flags reports whether this model finding discloses money, by the three forms of the section head.
func (m *moneyScreen) flags(f *Finding) bool {
	if m == nil || !m.armed || f == nil {
		return false
	}
	text := moneyScreenedText(f)
	if m.namesCurrency(text) || hasCurrencySymbol(text) {
		return true
	}
	lower := []rune(strings.ToLower(text))
	words := wordSpans(lower, moneyPriceWords)
	if len(words) == 0 {
		// ЦЕНОВОГО СЛОВА НЕТ — ЗНАЧИТ, НЕТ НИ ФОРМЫ 2, НИ ФОРМЫ 3. Обе требуют его вторым
		// признаком, и это единственное, что удерживает скрин от того, чтобы пометить деньгами
		// каждую находку с числом.
		return false
	}
	if strings.ContainsRune(text, '%') || len(wordSpans(lower, moneyComparators)) > 0 {
		return true
	}
	return hasBoundMagnitude(lower, words)
}

// moneyScreenedText is EXACTLY the four fields the client draws, joined.
//
// ЧИТАЕТСЯ ТЕКСТ ПОСЛЕ КАПОВ §8 п.7, потому что раскрыто читателю то, что до него доехало, а не то,
// что модель написала до обрезки. EVIDENCE ВХОДИТ НАРАВНЕ С ОСТАЛЬНЫМИ: она display-only для
// ВЕРИФИКАЦИИ (§8 п.3 — из-за неё ничего не дропается), но на экране она такой же текст, и валюта в
// ней раскрыта ровно настолько же, насколько в detail. Refs не читаются намеренно (см. шапку).
func moneyScreenedText(f *Finding) string {
	parts := make([]string, 0, 3+len(f.Evidence))
	parts = append(parts, f.Title, f.Detail, f.Suggestion)
	parts = append(parts, f.Evidence...)
	return strings.Join(parts, "\n")
}

// namesCurrency matches a card currency as an UPPERCASE whole word, and the case-sensitivity is the
// load-bearing part of the rule.
//
// Коды ISO — сплошь и рядом английские слова: TRY (турецкая лира — поставщик ровно того профиля, с
// которым работает этот бизнес), ALL, TOP, CUP, GEL, BAM. Сопоставление БЕЗ УЧЁТА РЕГИСТРА на
// карточке в TRY пометило бы деньгами каждую находку со словом «try», то есть почти каждую, и
// денежный фильтр превратился бы в глушилку ревью. В промпте код печатается заглавными («55.0000
// PLN/m»), заглавными его модель и повторяет.
//
// ЧТО ИЗ ЭТОГО ОСТАЁТСЯ ОТКРЫТЫМ, СКАЗАНО ПРЯМО: «60 pln» строчными этим правилом не ловится.
// Обычно его ловит форма 2 — ценовое слово при числе, — но находка, написавшая цену строчной
// валютой и не сказавшая ни одного ценового слова, проедет. Это известная дыра, а не недосмотр:
// закрыть её регистронезависимостью значит открыть дыру шире, на карточке в TRY.
func (m *moneyScreen) namesCurrency(text string) bool {
	if len(m.currencies) == 0 {
		return false
	}
	rs := []rune(text)
	for code := range m.currencies {
		if len(wordSpans(rs, []string{code})) > 0 {
			return true
		}
	}
	return false
}

// hasCurrencySymbol reports whether the text carries a currency SIGN.
//
// Категория Unicode Sc, а не список глифов: список — снова то место, куда новый знак не попадёт, но
// в отличие от валют карточки, знак валюты вывести ИЗ КАРТОЧКИ неоткуда, а из Unicode — можно.
// Знак сам по себе достаточен без второго признака: «€» в находке о конструкции не бывает случайной
// буквой, в отличие от цифры.
func hasCurrencySymbol(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Sc, r) {
			return true
		}
	}
	return false
}

// hasBoundMagnitude — форма 2: число ПРИ ценовом слове.
//
// ПОЧЕМУ ОКНО, А НЕ «ЕСТЬ ЛИ ЦИФРА ВООБЩЕ». Находка о маршруте почти всегда содержит номер шага, а
// находка о готовности — дробь («2 of 4»). Правило «любая цифра рядом со словом price» пометило бы
// деньгами «2 of 4 fabric lines are unpriced», то есть СЧЁТ НЕДОСТАЮЩИХ ФАКТОВ — ровно то, что
// граница stripTechCardCosting оставляет видимым.
func hasBoundMagnitude(rs []rune, words []runeSpan) bool {
	for i := 0; i < len(rs); {
		if !unicode.IsDigit(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && unicode.IsDigit(rs[j]) {
			j++
		}
		if !isOperationAddress(rs, i) {
			for _, w := range words {
				if runeGap(w, runeSpan{from: i, to: j}) <= moneyBindRunes {
					return true
				}
			}
		}
		i = j
	}
	return false
}

// isOperationAddress reports whether the digit run starting at `at` is the number of an OPERATION —
// «op:470», «op 470», «operation 470», «step 470», «#470» — and therefore an address, not a
// magnitude. rs is expected lowercased.
func isOperationAddress(rs []rune, at int) bool {
	i := at - 1
	for i >= 0 && (rs[i] == ' ' || rs[i] == '\t' || rs[i] == ':' || rs[i] == '#') {
		if rs[i] == '#' {
			return true
		}
		i--
	}
	end := i + 1
	for i >= 0 && unicode.IsLetter(rs[i]) {
		i--
	}
	if i+1 >= end {
		return false
	}
	return moneyOpAnchors[string(rs[i+1:end])]
}

// runeSpan is a half-open [from, to) range of rune indices.
type runeSpan struct{ from, to int }

// runeGap is the number of runes between two spans, 0 when they touch or overlap.
func runeGap(a, b runeSpan) int {
	if a.to <= b.from {
		return b.from - a.to
	}
	if b.to <= a.from {
		return a.from - b.to
	}
	return 0
}

// wordSpans finds every WHOLE-WORD occurrence of any needle in rs, in rs's own casing.
//
// Граница слова — «сосед не буква и не цифра». ПОДЧЁРКИВАНИЕ СЧИТАЕТСЯ ГРАНИЦЕЙ НАМЕРЕННО:
// «unit_price» — это слово «price», и модель, процитировавшая имя поля, сказала то же самое.
func wordSpans(rs []rune, needles []string) []runeSpan {
	var out []runeSpan
	for _, needle := range needles {
		nr := []rune(needle)
		if len(nr) == 0 {
			continue
		}
		for i := 0; i+len(nr) <= len(rs); i++ {
			if i > 0 && isWordRune(rs[i-1]) {
				continue
			}
			if end := i + len(nr); end < len(rs) && isWordRune(rs[end]) {
				continue
			}
			if !runesEqualAt(rs, i, nr) {
				continue
			}
			out = append(out, runeSpan{from: i, to: i + len(nr)})
		}
	}
	return out
}

func runesEqualAt(rs []rune, at int, needle []rune) bool {
	for k, r := range needle {
		if rs[at+k] != r {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// ── КАПЫ ТЕКСТОВ (§8 п.7) ──────────────────────────────────────────────────────────────────────

// boundedList caps every entry of a list and drops the entries that were nothing but whitespace.
// `maxItems` == 0 means «не ограничивать число».
//
// EVIDENCE ПРОХОДИТ ЗДЕСЬ И БОЛЬШЕ НИГДЕ (§8 п.3): длина — единственное, что этому файлу позволено
// сделать с ней. Число элементов evidence НЕ ограничивается: §8 говорит про капы ТЕКСТОВ, а лишняя
// строка доказательства — потеря для читателя без выигрыша для кого бы то ни было.
func boundedList(in []string, maxRunes, maxItems int) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = aiBoundedText(s, maxRunes); s == "" {
			continue
		}
		out = append(out, s)
		if maxItems > 0 && len(out) == maxItems {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
