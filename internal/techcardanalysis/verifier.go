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

		kept = append(kept, Finding{
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
		})
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
