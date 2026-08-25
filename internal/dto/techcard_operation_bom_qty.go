package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// --- количества на связях шага с артикулом (0334) -------------------------------------------------
//
// «Этот шаг ставит 6 пуговиц на изделие» — четвёртое из четырёх чисел «сколько на изделие» и
// единственное, у которого до 0334 не было места вовсе (карта владения — 01-DECISIONS): норма
// закупаемого материала живёт на слоте (0333), число повторов шага — в placement_count,
// количество вспомогательного изделия — в style_assembly.qty.
//
// РАЗБОР ИДЁТ ВСЕГДА, НЕЗАВИСИМО ОТ bom_qty_aware, и это не небрежность, а то же правило, что у
// сборки и у видов операций: флаг объявляет СПОСОБНОСТЬ БАНДЛА, а не выключает разбор.
// «Игнорировать при aware=false» выглядит защитой, а на деле открывает дыру — CloneStyleForSeason
// строит payload сам, транспортных флагов не эмитит и оба гейта обходит, так что клон карточки с
// проставленными количествами вернулся бы без них и без единой ошибки. Защита — ОТКАЗ на входе в
// API (techcard_bom_qty_gate.go), а не тихая фильтрация здесь.

// parseOperationBomQuantities разбирает разрежённый список количеств ОДНОГО шага и закрывает пять
// способов соврать этим списком. Каждый — именованный FieldViolation с путём до записи, потому что
// админка маршрутизирует отказ на конкретный контрол, а форм-левел-баннер «что-то не так с шагом»
// оператору не сообщает ничего.
//
//   - ЧИСЛО НЕ НАЗВАНО. Пустой google.type.Decimal — не «ноль» и не «не сказано»: связь БЕЗ числа
//     в этот список просто не попадает, поэтому запись без числа не выражает ничего. Принять её
//     значило бы завести второе написание отсутствия — запись-пустышку рядом с отсутствием
//     записи, — и первый же читатель, забывший это различить, прочитал бы её нулём.
//   - КЛЮЧ ВНЕ bom_line_keys ЭТОГО ШАГА. Членство в связи определяет ТОЛЬКО список 23, он
//     единственный владелец; здесь навешиваются числа на уже существующие связи. Ссылка на связь,
//     которой у шага нет, — не молчаливый пропуск, а ошибка: молча выброшенное число оператор
//     увидит только тем, что оно не сохранилось.
//   - ДУБЛЬ КЛЮЧА ВНУТРИ ШАГА. В отличие от bom_line_keys, где повтор схлопывается молча (связь —
//     МНОЖЕСТВО, второго факта повтор не несёт), здесь повтор несёт ВТОРОЕ ЧИСЛО о той же паре, и
//     схлопывание выбрало бы одно из двух за человека. Какое — неизвестно никому.
//   - ЧИСЛО НА СВЯЗИ С МЕРНЫМ СЛОТОМ. У нитки, ткани, клеевого и тесьмы норма живёт в рецепте
//     колорвея (и по размерам), поэтому счётчик на шаге был бы ТРЕТЬИМ ответом на один вопрос —
//     рядом со строкой рецепта и с расходом по размерам. Граница держится единственным предикатом
//     проекта, entity.IsCountableSection; второй копии списка мерных семей быть не должно.
//   - ОТРИЦАТЕЛЬНОЕ ЧИСЛО (и не влезающее в DECIMAL(10,3)) — тем же validateDecimalFits, что у
//     нормы слота: MySQL молча округлил бы лишние знаки и отдал бы обратно не то, что набрали.
//
// СЕКЦИЯ БЕРЁТСЯ ИЗ BOM ЭТОГО ЖЕ PAYLOAD'А, а не из сохранённой карточки, и это сильнее: секцию
// слота законно меняют тем же сохранением, и проверять надо ту, которая станет правдой ПОСЛЕ
// записи. Ключ, которого нет в BOM payload'а вовсе, отвергается здесь же: секции у него нет,
// счётность непроверяема, а сама связь всё равно упадёт в сторе на resolveBomRef — только позже и
// без упоминания количества.
//
// placement_count СЮДА НЕ ЗАГЛЯДЫВАЕТ И ОТСЮДА НЕ ВЫВОДИТСЯ. Это разные утверждения: «обметать
// 6 петель» повторяется шесть раз и не тратит ни одной пуговицы.
func parseOperationBomQuantities(
	pbs []*pb_common.TechCardOperationBomQty,
	bomLineKeys []string,
	sectionByKey map[string]entity.TechCardBomSection,
	step string,
) ([]entity.OperationBomQty, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	linked := make(map[string]bool, len(bomLineKeys))
	for _, k := range bomLineKeys {
		linked[k] = true
	}
	out := make([]entity.OperationBomQty, 0, len(pbs))
	seen := make(map[string]bool, len(pbs))
	for j, q := range pbs {
		field := fmt.Sprintf("%s.bom_quantities[%d]", step, j)
		if q == nil {
			return nil, entity.NewFieldViolation(field, "empty_entry", "",
				"remove the blank row — a link with no quantity simply does not appear in this list")
		}
		key := strings.TrimSpace(q.GetLineKey())
		if key == "" {
			return nil, entity.NewFieldViolation(field+".line_key", "required", "",
				"name the BOM line this quantity is about")
		}
		if !linked[key] {
			return nil, entity.NewFieldViolation(field+".line_key", "not_linked_to_this_step", key,
				"add the material to this step first (bom_line_keys owns the link), or drop the quantity")
		}
		if seen[key] {
			return nil, entity.NewFieldViolation(field+".line_key", "duplicate", key,
				"one quantity per material on a step — merge the two rows into the number that is true")
		}
		seen[key] = true

		nd, err := nullDecimalFromPb(q.GetQtyPerGarment())
		if err != nil {
			return nil, fmt.Errorf("%s.qty_per_garment: %w", field, err)
		}
		if !nd.Valid {
			return nil, entity.NewFieldViolation(field+".qty_per_garment", "required", key,
				"enter how many units of this material the step spends per garment, or remove the row — an empty row says nothing")
		}
		if err := validateDecimalFits(field+".qty_per_garment", nd.Decimal, bomQtyMaxFrac, bomQtyLimit, false); err != nil {
			return nil, err
		}
		section, ok := sectionByKey[key]
		if !ok {
			return nil, entity.NewFieldViolation(field+".line_key", "no_such_bom_line", key,
				"reference an existing BOM line by its line_key")
		}
		if !entity.IsCountableSection(section) {
			return nil, entity.NewFieldViolation(field+".qty_per_garment",
				"a measured material is counted by its norm, not by the piece",
				fmt.Sprintf("BOM line %q is section %q", key, section),
				"clear the per-step count, and put the consumption on the colourway recipe row instead")
		}
		out = append(out, entity.OperationBomQty{LineKey: key, QtyPerGarment: nd.Decimal})
	}
	return out, nil
}

// bomSectionsByLineKey — словарь «ключ строки BOM → секция» этого payload'а, единственный источник
// секции для проверки счётности выше. Строится один раз на карточку: пересобирать его внутри цикла
// по шагам значило бы квадрат на карточке с сотней шагов и сотней строк BOM.
func bomSectionsByLineKey(items []entity.TechCardBomItem) map[string]entity.TechCardBomSection {
	out := make(map[string]entity.TechCardBomSection, len(items))
	for i := range items {
		if k := strings.TrimSpace(items[i].LineKey); k != "" {
			out[k] = items[i].Section
		}
	}
	return out
}

// operationBomQuantitiesToPb — обратная сторона, и она обязана существовать вместе с разбором:
// поле, забытое на выходе, читается клиентом как «количеств нет», и первое же сохранение с
// осведомлённым флагом сотрёт их честно и навсегда.
func operationBomQuantitiesToPb(qs []entity.OperationBomQty) []*pb_common.TechCardOperationBomQty {
	if len(qs) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardOperationBomQty, 0, len(qs))
	for _, q := range qs {
		out = append(out, &pb_common.TechCardOperationBomQty{
			LineKey:       q.LineKey,
			QtyPerGarment: pbDecimalFromDecimal(q.QtyPerGarment),
		})
	}
	return out
}
