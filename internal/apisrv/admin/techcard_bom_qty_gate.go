package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- щит совместимости для количеств на связях шага (0334) ----------------------------------------
//
// ШЕСТОЙ ЩИТ ТОЙ ЖЕ ПОРОДЫ, и устройство зеркалит щит видов операций дословно, включая разделение на
// две функции: правило «payload эхоит то, чего не понимает» читается с ПРОВОДА и потому срабатывает
// до конверсии, а правило «сохранённая карточка несёт факты» требует загруженной карточки. Слить их
// значило бы опустить и первое правило до поздней точки, где отставшая вкладка услышала бы отказ про
// контрол, которого у неё нет.
//
// ПОЧЕМУ ЗДЕСЬ ВОЗМОЖЕН ТОЛЬКО ОТКАЗ, А НЕ ЗАЩИТА ПОЛЯ. Операции пишутся ПОЛНОЙ ЗАМЕНОЙ
// (insertTechCardOperationBoms — delete+reinsert на каждом сохранении карточки), и стабильного ключа
// у шага НЕТ. Значит:
//
//   - `IF(:omitted, …)`-защита, которой хватает колонке BOM (строка живёт по line_key и переживает
//     сохранение), здесь защищать нечего: строки связи не обновляются, они пересоздаются;
//   - восстановление хранимого по паре (display_order, bom_item_id) ЗАПРЕЩЕНО ОТДЕЛЬНО: перестановка
//     шагов в том же сохранении — рядовая правка, и она перепутала бы количества между шагами.
//     Тихо: числа остались бы валидными, только не на своих шагах.
//
// Честны ровно два исхода: значение доезжает целиком либо сохранение отказывает целиком. Порядком
// выкатки это НЕ закрывается — открытая вкладка ест данные и после деплоя клиента.
//
// ЧЕГО ЩИТ НЕ ДЕЛАЕТ. Он не фильтрует поля: разбор bom_quantities идёт всегда, независимо от флага.
// «Игнорировать при aware=false» выглядит защитой, а на деле открывает дыру — CloneStyleForSeason
// строит payload сам, транспортных флагов не эмитит и все capability-гейты обходит, так что клон
// карточки с проставленными количествами вернулся бы пустым без единой ошибки. Поэтому серверные
// пути ставят флаг ЯВНО (style.go, betaseed), а не обходят гейт молчанием.
//
// ПАРНОГО `*_cleared` У НЕГО НЕТ — как у machine_fields_aware, operation_kinds_aware и
// operation_work_aware, и в отличие от узлов и снимков. Там «снять разметку целиком» есть отдельное
// намерение, выразимое кнопкой, и осведомлённая пустота против непустой карточки — почти наверняка
// авария. Здесь «количество стёрли» — РЯДОВАЯ ПРАВКА: технолог убрал число, потому что оно оказалось
// неверным или потому что материал со связи ушёл. Бекстоп объявил бы такую правку ошибкой и сделал
// бы количество НЕСТИРАЕМЫМ — классический дефект такой защиты. Осведомлённая запись очищает
// количества честно, и это покрыто тестом отдельной клеткой.
//
//	stored нет  | payload нет   | aware нет  → сохранить (сегодняшний путь, ни одной проверки)
//	stored нет  | эхо количеств | aware нет  → отказ: бандл эхоит поле, которого не знает
//	stored нет  | любой         | aware есть → сохранить
//	stored есть | —             | aware нет  → FailedPrecondition: устаревшая вкладка
//	stored есть | КОЛИЧЕСТВА    | aware есть → сохранить (обычное редактирование)
//	stored есть | БЕЗ КОЛИЧЕСТВ | aware есть → СОХРАНИТЬ И ОЧИСТИТЬ: см. абзац про отсутствие cleared

const outdatedBomQtyClientFix = "this version of the admin panel cannot edit per-step material quantities, and its save replaces the whole step list — update the admin panel (hard-refresh) and try again"

func outdatedBomQtyClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedBomQtyClientFix)
}

// bomQtyWireGate — правило 1, читается с провода до конверсии.
//
// ПРЕДИКАТ ПО ПРИСУТСТВИЮ ЗАПИСИ, А НЕ ПО ЗАПОЛНЕННОСТИ ЧИСЛА, и это здесь единственно верно, в
// отличие от расширенных словарей волны 0324: поле bom_quantities заведено ровно затем, чтобы его
// ОТСУТСТВИЕ значило «бандл про количества молчит». Присланная запись и есть факт — даже пустая
// (её конверсия потом отвергнет своим именем, но услышать про устаревшую вкладку оператор должен
// РАНЬШЕ, чем про пустую строку).
func bomQtyWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetBomQtyAware() {
		return nil
	}
	if payloadSpeaksBomQty(pb) {
		// Наблюдаемость: без счётчика отказов никто не узнает, бьётся ли старый бандл о щит в
		// проде — а это единственный признак, что клиент где-то не обновился.
		slog.Default().Warn("bom qty gate refused an unaware payload that echoes per-step quantities",
			slog.String("gate", "wire"), slog.String("cell", "stored:any/payload:bom_qty/aware:no"))
		return outdatedBomQtyClient("the payload carries per-step material quantities it does not declare support for")
	}
	return nil
}

// bomQtyStoredGate — правило 2, требует загруженной карточки. Именно оно и срабатывает на практике:
// payload отставшей вкладки, которая про поле не знает вовсе, выглядит невинно, и только хранилище
// знает, что полная замена операций сотрёт числа.
func bomQtyStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetBomQtyAware() {
		return nil
	}
	if storedHasBomQty(stored) {
		slog.Default().Warn("bom qty gate refused an outdated bundle against a card with per-step quantities",
			slog.String("gate", "stored"), slog.String("cell", "stored:bom_qty/aware:no"),
			slog.Int("tech_card_id", storedCardID(stored)))
		return outdatedBomQtyClient("this tech card holds per-step material quantities on its steps, and this version of the admin panel would erase them")
	}
	return nil
}

// payloadSpeaksBomQty — предикат правила 1.
func payloadSpeaksBomQty(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, o := range pb.GetOperations() {
		if len(o.GetBomQuantities()) > 0 {
			return true
		}
	}
	return false
}

// storedHasBomQty — предикат правила 2: несёт ли СОХРАНЁННАЯ карточка хоть одно количество на связи.
//
// По СПИСКУ, а не по «есть ли вообще связи»: связь без числа — сегодняшнее состояние всех трёх
// прод-строк и всех четырнадцати бета-строк, и предикат по связям заблокировал бы каждую карточку с
// привязанным материалом — ровно тот отказ, от которого щит обязан воздержаться.
func storedHasBomQty(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if len(stored.Operations[i].BomQuantities) > 0 {
			return true
		}
	}
	return false
}
