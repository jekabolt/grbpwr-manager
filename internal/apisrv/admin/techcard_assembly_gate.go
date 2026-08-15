package admin

import (
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- щит совместимости для узлов сборки (0307) ---------------------------------------------------
//
// Устройство зеркалит машинный гейт 0306 намеренно, включая разделение на две функции: правило
// «payload эхоит то, чего не понимает» читается с ПРОВОДА и потому может сработать до конверсии,
// а правило «сохранённая карточка несёт факты» требует загруженной карточки. Слить их в одну
// функцию значило бы опустить и первое правило до поздней точки.
//
// ЧЕГО ЩИТ НЕ ДЕЛАЕТ. Он не фильтрует поля. Разбор 46-48 идёт всегда, независимо от флага:
// «игнорировать при aware=false» выглядит защитой, а на деле открывает дыру — CloneStyleForSeason
// строит payload сам, транспортных флагов не эмитит и оба гейта обходит, так что клон размеченной
// карточки вернулся бы без узлов и без единой ошибки.
//
// ТАБЛИЦА ИСТИННОСТИ ПАРЫ ФЛАГОВ. «Payload несёт узлы» здесь читается УЗКИМ предикатом
// (payloadCarriesAssemblyUnits), а «эхоит поля» — широким (payloadSpeaksAssembly); почему их
// два, написано над самими предикатами.
//
//	stored нет | payload нет | aware нет  | —      → сохранить (сегодняшний путь, ни одной проверки)
//	stored нет | эхо полей   | aware нет  | —      → отказ: бандл эхоит поля, которых не знает
//	stored нет | любой       | aware есть | false  → сохранить
//	stored нет | нет         | aware есть | TRUE   → отказ: «снял разметку» там, где её не было
//	stored есть| —           | aware нет  | —      → FailedPrecondition: устаревшая вкладка
//	stored есть| УЗЛЫ        | aware есть | false  → сохранить (обычное редактирование)
//	stored есть| БЕЗ УЗЛОВ   | aware есть | false  → ОТКАЗ БЕКСТОПОМ: это и есть тихое стирание
//	stored есть| без узлов   | aware есть | TRUE   → сохранить: снятие разметки, намерение объявлено
//	stored есть| УЗЛЫ        | aware есть | TRUE   → отказ: противоречие, «снял» и одновременно прислал
//
// Все девять клеток покрыты тестом, и payload в нём — тот, что реально шлёт НОВЫЙ клиент: все
// входы полем 46, включая чистые детали. На payload'е из легаси-поля таблица зелёная и при
// сломанных предикатах.

const outdatedAssemblyClientFix = "this version of the admin panel cannot edit assembly units, and its save replaces the whole step list — update the admin panel (hard-refresh) and try again"

func outdatedAssemblyClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedAssemblyClientFix)
}

// assemblyCapabilityWireGate — правило 1, читается с провода до конверсии.
func assemblyCapabilityWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetAssemblyAware() {
		// Намерение без предмета — теневой флаг. Ловится здесь, а не в конвертере, потому что это
		// утверждение о ЗАПРОСЕ, а не о карточке.
		if pb.GetAssemblyCleared() && payloadCarriesAssemblyUnits(pb) {
			return status.Error(codes.InvalidArgument,
				"assembly_cleared is set together with assembly units in the same payload: decide whether the card keeps its units or drops them")
		}
		return nil
	}
	if payloadSpeaksAssembly(pb) {
		// Наблюдаемость: без счётчика отказов никто не узнает, бьётся ли старый бандл о щит в
		// проде — а именно это единственный признак, что клиент где-то не обновился.
		slog.Default().Warn("assembly gate refused an unaware payload that echoes units",
			slog.String("gate", "wire"), slog.String("cell", "stored:any/payload:units/aware:no"))
		return outdatedAssemblyClient("the payload carries assembly units it does not declare support for")
	}
	if pb.GetAssemblyCleared() {
		return status.Error(codes.InvalidArgument,
			"assembly_cleared without assembly_aware: a bundle that does not know about assembly units cannot ask to clear them")
	}
	return nil
}

// assemblyCapabilityStoredGate — правило 2 и контентный бекстоп; работает только с загруженной
// карточкой.
//
// Бекстоп — не тот же самый щит. Щит закрывает СТАРЫЙ бандл; бекстоп закрывает запись, которая
// осведомлена, но пуста: параллельная вкладка нового клиента, открытая до разметки; применение
// AI-черновика поверх размеченной карточки; восстановление до-фичевого локального черновика;
// сидер или скрипт. Все они шлют assembly_aware=true и ноль узлов, и без бекстопа самый дорогой
// ручной ввод карточки исчезал бы молча.
func assemblyCapabilityStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if !storedHasAssemblyFacts(stored) {
		// Намерение без предмета. Клиент, у которого cleared протекает в каждое сохранение, иначе
		// оставался бы невидимым до первой размеченной карточки — а там уже стирал бы её законно.
		if pb.GetAssemblyAware() && pb.GetAssemblyCleared() {
			slog.Default().Warn("assembly gate refused assembly_cleared on a card with no markup",
				slog.String("gate", "stored"), slog.String("cell", "stored:none/aware:yes/cleared:yes"),
				slog.Int("tech_card_id", storedCardID(stored)))
			return status.Error(codes.InvalidArgument,
				"assembly_cleared is set but this tech card has no assembly units to clear")
		}
		return nil
	}
	if !pb.GetAssemblyAware() {
		slog.Default().Warn("assembly gate refused an outdated bundle against a marked-up card",
			slog.String("gate", "stored"), slog.String("cell", "stored:units/aware:no"),
			slog.Int("tech_card_id", storedCardID(stored)))
		return outdatedAssemblyClient("this tech card is marked up with assembly units (what each step produces and takes)")
	}
	if payloadCarriesAssemblyUnits(pb) {
		return nil
	}
	if pb.GetAssemblyCleared() {
		// Намерение объявлено — это единственный законный путь снять разметку.
		return nil
	}
	slog.Default().Warn("assembly backstop refused an aware but empty save",
		slog.String("gate", "backstop"), slog.String("cell", "stored:units/payload:none/aware:yes/cleared:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return status.Error(codes.FailedPrecondition,
		"this save would erase the assembly units on this tech card and does not carry any: "+
			"if you meant to remove them, use «снять разметку узлов»; otherwise reload the card — "+
			"another tab or a restored draft is about to overwrite it")
}

// ДВА ПРЕДИКАТА, ПОТОМУ ЧТО ВОПРОСА ДВА, и слить их — значит сломать и защиту, и путь
// отступления одной функцией.
//
// storedCardID — id для лог-строки; 0, когда карточки ещё нет (путь создания). Без id строка
// отвечает «кто-то бился о щит», а нужно «эта карточка», иначе разбор инцидента начинается с
// поиска по времени.
func storedCardID(stored *entity.TechCard) int {
	if stored == nil {
		return 0
	}
	return stored.Id
}

// payloadSpeaksAssembly отвечает «трогает ли payload сборочные ПОЛЯ вообще». Это вопрос про
// БАНДЛ: неосведомлённый клиент, эхоящий поле 46, эхоит то, чего не понимает. Предикат широкий
// намеренно — любой ключ в 46 считается разговором про сборку.
func payloadSpeaksAssembly(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, o := range pb.GetOperations() {
		if o == nil {
			continue
		}
		if strings.TrimSpace(o.GetOutputUnitKey()) != "" || strings.TrimSpace(o.GetOutputUnitName()) != "" {
			return true
		}
		for _, k := range o.GetInputKeys() {
			if strings.TrimSpace(k) != "" {
				return true
			}
		}
	}
	return false
}

// payloadCarriesAssemblyUnits отвечает на СОВСЕМ ДРУГОЙ вопрос: несёт ли payload УЗЛЫ.
//
// Это вопрос про СОДЕРЖАНИЕ, и широкий предикат выше на него отвечать не может. Осведомлённый
// клиент по контракту шлёт ВСЕ входы полем 46, включая чистые детали, — то есть «говорит про
// сборку» на каждом сохранении любой карточки. Спроси у широкого предиката про содержание, и:
//   - бекстоп умрёт для нового клиента (параллельная вкладка, открытая до разметки, шлёт 46 с
//     деталями, предикат отвечает «узлы есть», гейт пропускает, разметка стёрта молча) — то
//     есть первый же кейс, ради которого бекстоп заведён;
//   - кнопка «снять разметку» станет невыразимой: она обязана прислать 46 с деталями и флаг
//     cleared, а это прочиталось бы как противоречие «снял и одновременно прислал узлы».
//
// Узел — это непустой выходной ключ (или имя) ЛИБО вход, не совпадающий ни с одной line_key
// деталей ЭТОГО ЖЕ payload'а. Сравнение по своему же payload'у, а не по сохранённой карточке:
// детали приходят в той же записи, и спрашивать базу здесь незачем.
func payloadCarriesAssemblyUnits(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	pieceKeys := make(map[string]bool, len(pb.GetPieces()))
	for _, p := range pb.GetPieces() {
		if p == nil {
			continue
		}
		if k := strings.TrimSpace(p.GetLineKey()); k != "" {
			pieceKeys[k] = true
		}
	}
	for _, o := range pb.GetOperations() {
		if o == nil {
			continue
		}
		if strings.TrimSpace(o.GetOutputUnitKey()) != "" || strings.TrimSpace(o.GetOutputUnitName()) != "" {
			return true
		}
		for _, k := range o.GetInputKeys() {
			if k = strings.TrimSpace(k); k != "" && !pieceKeys[k] {
				return true
			}
		}
	}
	return false
}

// storedHasAssemblyFacts — несёт ли СОХРАНЁННАЯ карточка разметку.
//
// Только выходной ключ и входы-узлы. Строки входов-деталей фактом сборки не являются: они есть у
// карточек, где никто ничего не размечал, и считать их разметкой значило бы объявить устаревшими
// вкладки, редактирующие сегодняшние карточки.
func storedHasAssemblyFacts(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		o := &stored.Operations[i]
		if o.OutputUnitKey.Valid && o.OutputUnitKey.String != "" {
			return true
		}
		for _, in := range o.AssemblyInputs {
			if in.Kind == entity.AssemblyInputUnit {
				return true
			}
		}
	}
	return false
}
