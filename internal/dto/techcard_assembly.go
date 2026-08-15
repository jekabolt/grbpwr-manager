package dto

import (
	"strconv"

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
func canonicalizeAssembly(ops []entity.TechCardOperation, pieces []entity.TechCardPiece) *entity.ValidationError {
	if len(ops) == 0 {
		return nil
	}

	// Карточка без единого сборочного факта проходит вакуумно и НЕ ТРОГАЕТСЯ вовсе: её
	// entity-форма обязана остаться байт-в-байт такой же, как до фичи. Это состояние каждой
	// сегодняшней карточки, и любая правка здесь — регресс на всей базе.
	marked := false
	for i := range ops {
		if ops[i].OutputUnitKey.String != "" || len(ops[i].InputKeys) > 0 {
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
				"код узла — до 64 символов; это код для чтения на печати и в QR, а не описание")
		}
		if len(name) > assemblyUnitNameMaxLen {
			return entity.NewFieldViolation(step+".output_unit_name", "too_long", name,
				"имя узла — до 255 символов")
		}
		for j, k := range ops[i].InputKeys {
			if len(k) > assemblyUnitKeyMaxLen {
				return entity.NewFieldViolation(inputPath(i, j), "too_long", k,
					"ключ входа — до 64 символов")
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
		// Источник входов. Осведомлённая запись живёт по объединению (46); неосведомлённая — по
		// легаси-проекции (21), ровно как сегодня. Один источник истины на запись: смешивать их
		// значило бы дать двум полям спорить о том, что технолог имел в виду.
		raw := ops[i].InputKeys
		if len(raw) == 0 {
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
		return assemblyViolationToField(res.Violations[0])
	}

	// --- нормализация после успешной проверки --------------------------------------------------
	for i := range ops {
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
	}

	// Имя узла живёт на ПЕРВОМ производителе. Если поглощающий шаг назвал узел, а первый не
	// назвал, имя переносится на первого — иначе удаление или перестановка первого шага молча
	// теряли бы имя, которое технолог набирал один раз.
	normalizeUnitNames(ops, res)

	return nil
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

