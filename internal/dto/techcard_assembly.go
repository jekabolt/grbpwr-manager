package dto

import (
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Канонизация сборочного графа — единственный пост-проход, который превращает сырые ключи
// операций в каноническую форму и проверяет правила 1-3, 5-7.
//
// ПОЧЕМУ ЗДЕСЬ, А НЕ В СТОРЕ. Три причины, и каждой хватило бы:
//
//  1. Дайджест штампуется ДО стора. StampTechCardSignoffDigests стоит в конце конвертера и
//     хеширует entity, а позиция 4 кортежа CONSTRUCTION — это PieceLineKeys. Если отделить
//     детали от узлов только в сторе, свежая подпись зафиксирует одно, а следующее чтение
//     вернёт другое: каждая размеченная карточка родилась бы с отпечатком «изменено после
//     подписи» и осталась бы с ним навсегда.
//  2. Конвертер — единственная точка, общая ВСЕМ ТРЁМ входам записи. CreateTechCard и
//     UpdateTechCard проходят capability-гейты, а CloneStyleForSeason не проходит ни одного
//     (единственные вызовы гейтов — apisrv/admin/techcard.go:120/244/270). Проверка, стоящая
//     здесь, клоном не обходится.
//  3. Стор не должен знать семантику. Его работа — резолвить ключи в id и писать; сходимость
//     графа не его слой.
//
// ПОЧЕМУ ОТДЕЛЬНЫЙ ПРОХОД, А НЕ ПРАВКА parseTechCardOperations. Операции разбираются РАНЬШЕ
// деталей (у их разбора свои зависимости — calloutNumbers, парк, число строк BOM), а
// классификация ключа требует множества line_key деталей. Переставлять разборы местами
// рискованнее, чем добавить проход после обоих.

// canonicalizeAssembly приводит входы операций к канонической форме и проверяет правила графа.
//
// МУТИРУЕТ ops: заполняет AssemblyInputs, переписывает InputKeys и PieceLineKeys, нормализует
// имя узла. Это делает конвертер нормализатором, а не только переводчиком, поэтому порядок
// хвоста конвертера (канонизация → релизный гейт → штамп) обязателен и записан там же.
func canonicalizeAssembly(ops []entity.TechCardOperation, pieces []entity.TechCardPiece, aware bool) *entity.ValidationError {
	if len(ops) == 0 {
		return nil
	}

	// Карточка без единого сборочного факта проходит вакуумно и НЕ ТРОГАЕТСЯ вовсе: её
	// entity-форма обязана остаться байт-в-байт такой же, как до фичи. Это состояние каждой
	// сегодняшней карточки, и любая правка здесь — регресс на всей базе.
	marked := false
	for i := range ops {
		// Имя узла считается сборочным фактом наравне с ключом. Иначе payload, несущий ТОЛЬКО
		// имя (без ключа и без входов), проскакивал бы мимо прохода и оседал в колонке
		// output_unit_name — теневое значение, которое контракт обещает отклонять.
		if ops[i].OutputUnitKey.String != "" || ops[i].OutputUnitName.String != "" || len(ops[i].InputKeys) > 0 {
			marked = true
			break
		}
	}
	if !marked {
		return nil
	}

	// --- гигиена полей: длины и теневое имя ---------------------------------------------------
	// Проверяется до графа: сообщение «имя без ключа» полезнее, чем «узел из одного входа»,
	// полученное из того же шага.
	for i := range ops {
		step := stepPath(i)
		key := ops[i].OutputUnitKey.String
		name := ops[i].OutputUnitName.String
		if len(key) > assemblyUnitKeyMaxLen {
			return entity.NewFieldViolation(step+".output_unit_key", "too_long", key,
				"a unit code is up to 64 characters; it is a code to be read on print and in a QR, not a description")
		}
		if len(name) > assemblyUnitNameMaxLen {
			return entity.NewFieldViolation(step+".output_unit_name", "too_long", name,
				"a unit name is up to 255 characters")
		}
		for j, k := range ops[i].InputKeys {
			if len(k) > assemblyUnitKeyMaxLen {
				return entity.NewFieldViolation(inputPath(i, j), "too_long", k,
					"an input key is up to 64 characters")
			}
		}
	}

	// --- классификация -------------------------------------------------------------------------
	pieceKeys := make(map[string]bool, len(pieces))
	for _, p := range pieces {
		if p.LineKey != "" {
			pieceKeys[p.LineKey] = true
		}
	}

	steps := make([]entity.AssemblyStep, len(ops))
	for i := range ops {
		// Источник входов выбирается ПО ФЛАГУ, а не по наполненности поля, и это не педантизм.
		// Осведомлённая запись живёт по объединению (46) целиком; неосведомлённая — по
		// легаси-проекции (21), ровно как сегодня. Пер-операционный фолбэк «46 пусто → возьму
		// 21» смешал бы два источника внутри одной записи: GET-modify-PUT, очистивший 46 у шага,
		// но эхонувший 21 (а она эмитится всегда), молча воскресил бы входы, которые автор
		// только что убрал. Один источник истины на запись.
		var raw []string
		if aware {
			// ЧАСТИЧНО ЗАПОЛНЕННОЕ ОБЪЕДИНЕНИЕ — ОТКАЗ, А НЕ ТИХАЯ ПОТЕРЯ.
			//
			// marked считается по ВСЕЙ карточке: достаточно одного сборочного факта где угодно,
			// и источником входов для всех шагов становится поле 46. Шаг, у которого клиент
			// заполнил только легаси-проекцию (полу-мигрировавшая форма, скрипт, слитый
			// черновик), терял бы входы молча: объединение пусто, проекция пересобирается в nil,
			// стор пишет ноль строк — ни нарушения, ни лога. Бекстоп на это не срабатывает,
			// потому что узлы в payload'е есть, просто на другом шаге.
			//
			// Это ровно сигнатура «тихого стирания», против которой построен весь щит, только
			// пер-операционная. Легитимное «автор очистил входы шага» новый клиент выражает
			// пустым 46 при пустом же 21, и оно проходит.
			if len(ops[i].InputKeys) == 0 && len(ops[i].PieceLineKeys) > 0 {
				return entity.NewFieldViolation(inputPath(i, 0), "assembly_inputs_missing",
					strings.Join(ops[i].PieceLineKeys, ", "),
					"an aware write describes a step's inputs with the input_keys field; this step sent only "+
						"the legacy projection piece_line_keys — refresh the admin tab and save again")
			}
			raw = ops[i].InputKeys
		} else {
			raw = ops[i].PieceLineKeys
		}
		ops[i].AssemblyInputs = entity.ClassifyAssemblyInputs(pieceKeys, raw)
		steps[i] = entity.AssemblyStep{
			Inputs:         ops[i].AssemblyInputs,
			OutputUnitKey:  ops[i].OutputUnitKey.String,
			OutputUnitName: ops[i].OutputUnitName.String,
		}
	}

	// --- правила 1-3, 5-7 ----------------------------------------------------------------------
	// Порядок шагов здесь — позиция в массиве payload'а, и это ЕДИНСТВЕННЫЙ честный порядок на
	// записи: operation_number парсер всё равно присваивает сам как (i+1)*10, так что
	// последовательность массива и есть последовательность карточки. AssemblyOperationOrder
	// нужен читателям, у которых массива нет.
	res := entity.AssemblySweep(assemblyPieces(pieces), steps)
	if len(res.Violations) > 0 {
		v := res.Violations[0]
		// Отказ считается ПО НОМЕРУ ПРАВИЛА и по ветке: «сколько отказов» ничего не говорит, а
		// «правило 1, ветка produced-later, двадцать раз за неделю» говорит, что технолог не
		// понимает порядок шагов, и чинить надо интерфейс, а не правило.
		slog.Default().Info("assembly canonicalisation refused a payload",
			slog.Int("rule", int(v.Rule)), slog.String("branch", string(v.Detail)),
			slog.Int("step", v.Step), slog.Int("violations", len(res.Violations)))
		return assemblyViolationToField(v)
	}

	// --- нормализация после успешной проверки --------------------------------------------------
	for i := range ops {
		// ЧТО НЕСЛА ЛЕГАСИ-ПРОЕКЦИЯ ДО ПЕРЕСБОРКИ. Нужно ровно для одного — сказать вслух, если
		// пересобранная проекция ОКАЗАЛАСЬ УЖЕ присланной.
		incomingPieces := ops[i].PieceLineKeys
		// InputKeys — канонический порядок объединения; PieceLineKeys — его проекция «только
		// детали». Проекция пересобирается ВСЕГДА, даже если клиент прислал её сам: позиция 4
		// кортежа дайджеста обязана быть выводом из одного источника, а не вторым мнением.
		keys := make([]string, 0, len(ops[i].AssemblyInputs))
		var pieceProjection []string
		for _, in := range ops[i].AssemblyInputs {
			keys = append(keys, in.Key)
			if in.Kind == entity.AssemblyInputPiece {
				pieceProjection = append(pieceProjection, in.Key)
			}
		}
		if len(keys) == 0 {
			keys = nil
		}
		ops[i].InputKeys = keys
		// nil, а не пустой срез: json.Marshal их различает, и дайджест записи разошёлся бы с
		// дайджестом чтения на карточке, где у шага нет входов.
		ops[i].PieceLineKeys = pieceProjection

		// ПОТЕРЯ ВХОДА-ДЕТАЛИ СТАНОВИТСЯ СЧИТАЕМОЙ, А НЕ НЕВИДИМОЙ.
		//
		// Отказ выше ловит одну форму частично заполненного объединения — «46 пусто, 21 непусто».
		// Соседнюю форму он пропускает: клиент, реализовавший поле 46 как «только узлы» (прислал
		// input_keys=[SHELL], piece_line_keys=[FR,BK]), проходит проверку, проекция пересобирается
		// из одного узла, и ДЕТАЛИ ИСЧЕЗАЮТ без нарушения и без следа. Строгий отказ «21 ⊆ 46»
		// здесь запрещён: законный круговой рейс GET-правка-PUT возвращает устаревшую 21 после
		// правки 46, и такой отказ ломал бы обычное сохранение.
		//
		// Поэтому предупреждение, а не отказ: невидимый сбой превращается в счётный, и если
		// счётчик начнёт расти, у нас будет и повод, и данные для настоящего правила.
		if aware {
			for _, was := range incomingPieces {
				if was == "" || slices.Contains(pieceProjection, was) {
					continue
				}
				slog.Default().Warn("assembly: rebuilt piece projection dropped an incoming key",
					slog.Int("operation_index", i), slog.String("piece_line_key", was))
			}
		}
	}

	// Имя узла живёт на ПЕРВОМ производителе. Если поглощающий шаг назвал узел, а первый не
	// назвал, имя переносится на первого — иначе удаление или перестановка первого шага молча
	// теряли бы имя, которое технолог набирал один раз.
	normalizeUnitNames(ops, res)

	return nil
}

// assemblyReleaseCheck — правило 4, на переходе в RELEASED: ровно один терминальный узел, и
// каждая объявленная строка детали в него попадает.
//
// ПОЧЕМУ НА РЕЛИЗЕ, А НЕ НА ПОДПИСИ. Первая редакция плана вешала это правило на СВЕЖУЮ подпись
// CONSTRUCTION, и в той точке оно не гейтило ничего: карточку можно зарелизить вовсе без
// подписи (approval_state — свободное поле, единственная жёсткая проверка при RELEASED была про
// открытые high-severity issues), с ПЕРЕНЕСЁННОЙ подписью (reconcileUpdateTechCardSignoffs
// помечает секцию свежей только когда переносить нечего) или разметив граф уже после
// утверждения (устаревание подписи — advisory, оно записи не блокирует).
//
// Условие включения — карточка несёт хотя бы один ПРОИЗВОДЯЩИЙ ШАГ. Неразмеченная карточка
// релизится ровно как раньше, и сегодня это все карточки без исключения.
//
// Гейт живёт в конвертере, потому что конвертер общий всем трём входам записи. Отсюда следствие,
// без которого он ломает штатную операцию: клон сезона обязан становиться черновиком ДО
// конверсии, а не после (см. CloneStyleForSeason).
func assemblyReleaseCheck(ops []entity.TechCardOperation, pieces []entity.TechCardPiece) *entity.ValidationError {
	produces := false
	for i := range ops {
		if ops[i].OutputUnitKey.String != "" {
			produces = true
			break
		}
	}
	// Именно «есть output_unit_key», а не «есть входы-узлы»: узел без производящего шага движок
	// и так не допустит, но условие обязано читаться однозначно.
	if !produces {
		return nil
	}

	steps := make([]entity.AssemblyStep, len(ops))
	for i := range ops {
		steps[i] = entity.AssemblyStep{
			Inputs:         ops[i].AssemblyInputs,
			OutputUnitKey:  ops[i].OutputUnitKey.String,
			OutputUnitName: ops[i].OutputUnitName.String,
		}
	}
	p := assemblyPieces(pieces)
	res := entity.AssemblySweep(p, steps)
	violations := entity.AssemblyReleaseCheck(p, steps, res)
	if len(violations) == 0 {
		return nil
	}
	msg := violations[0].Message
	for _, v := range violations[1:] {
		msg += "; " + v.Message
	}
	return entity.NewFieldViolation("approval_state", string(violations[0].Detail), "released",
		"the assembly doesn't converge: "+msg)
}

// TechCardAssemblyBlocker — совещательная половина правила 4 для чек-листа готовности.
// Возвращает "" когда релизу ничто не мешает.
//
// Существует, чтобы UI не обещал готовность, противоречащую будущему отказу: жёсткий гейт живёт
// в конвертере и срабатывает уже на попытке сохранить RELEASED, а технолог должен видеть причину
// ДО того, как нажмёт. Пока стор не читает сборочные факты, строка выполняется вакуумно — на
// неразмеченной карточке ей нечего блокировать, и это верное поведение, а не заглушка.
func TechCardAssemblyBlocker(tc *entity.TechCard) string {
	if tc == nil {
		return ""
	}
	produces := false
	for i := range tc.Operations {
		if tc.Operations[i].OutputUnitKey.String != "" {
			produces = true
			break
		}
	}
	if !produces {
		return ""
	}

	pieceKeys := make(map[string]bool, len(tc.Pieces))
	for _, p := range tc.Pieces {
		if p.LineKey != "" {
			pieceKeys[p.LineKey] = true
		}
	}
	// Порядок шагов у ПРОЧИТАННОЙ карточки берётся единственной функцией порядка: массива
	// payload'а здесь нет, а сортировка чтения (operation_number, затем display_order) на
	// легаси-строках расходится с позицией в срезе.
	order := entity.AssemblyOperationOrder(tc.Operations)
	steps := make([]entity.AssemblyStep, 0, len(order))
	for _, idx := range order {
		op := tc.Operations[idx]
		inputs := op.AssemblyInputs
		if inputs == nil {
			inputs = entity.ClassifyAssemblyInputs(pieceKeys, op.InputKeys)
		}
		steps = append(steps, entity.AssemblyStep{
			Inputs:         inputs,
			OutputUnitKey:  op.OutputUnitKey.String,
			OutputUnitName: op.OutputUnitName.String,
		})
	}
	p := assemblyPieces(tc.Pieces)
	violations := entity.AssemblyReleaseCheck(p, steps, entity.AssemblySweep(p, steps))
	if len(violations) == 0 {
		return ""
	}
	msg := violations[0].Message
	for _, v := range violations[1:] {
		msg += "; " + v.Message
	}
	return msg
}

// --- вспомогательное ---------------------------------------------------------------------------

const (
	assemblyUnitKeyMaxLen  = 64  // VARCHAR(64) в 0307
	assemblyUnitNameMaxLen = 255 // VARCHAR(255) в 0307
)

func assemblyPieces(pieces []entity.TechCardPiece) []entity.AssemblyPiece {
	out := make([]entity.AssemblyPiece, 0, len(pieces))
	for _, p := range pieces {
		out = append(out, entity.AssemblyPiece{LineKey: p.LineKey, Name: p.Name})
	}
	return out
}

func normalizeUnitNames(ops []entity.TechCardOperation, res entity.AssemblyResult) {
	for key, unit := range res.Units {
		if unit.Name == "" || unit.ProducedAt < 0 || unit.ProducedAt >= len(ops) {
			continue
		}
		if ops[unit.ProducedAt].OutputUnitName.String == "" {
			ops[unit.ProducedAt].OutputUnitName = nullStringFromPb(unit.Name)
		}
		// Поглощающие шаги имя не хранят: на чтении оно разрешается по первому производителю, и
		// повторённое имя было бы вторым мнением о том же факте.
		for _, at := range unit.AbsorbedAt {
			if at >= 0 && at < len(ops) && ops[at].OutputUnitKey.String == key {
				ops[at].OutputUnitName = nullStringFromPb("")
			}
		}
	}
}

func stepPath(i int) string { return "operations[" + strconv.Itoa(i) + "]" }
func inputPath(i, j int) string {
	return "operations[" + strconv.Itoa(i) + "].input_keys[" + strconv.Itoa(j) + "]"
}

// assemblyViolationToField переводит нарушение движка в поле запроса. Движок транспортных
// путей не знает — он возвращает координаты, а путь строится здесь.
func assemblyViolationToField(v entity.AssemblyViolation) *entity.ValidationError {
	field := "operations"
	switch {
	case v.Step >= 0 && v.Input >= 0:
		field = inputPath(v.Step, v.Input)
	case v.Step >= 0 && v.Rule == entity.AssemblyRuleHygiene:
		field = stepPath(v.Step) + ".output_unit_name"
	case v.Step >= 0:
		field = stepPath(v.Step) + ".output_unit_key"
	}
	return entity.NewFieldViolation(field, assemblyReasonOf(v), v.Key, v.Message)
}

// assemblyReasonOf — машинный reason для BadRequest. Берётся код ветки движка, а не
// человеческий текст: клиент разбирает reason, а показывает Message.
func assemblyReasonOf(v entity.AssemblyViolation) string {
	if v.Detail != "" {
		return string(v.Detail)
	}
	return "assembly_rule_violation"
}
