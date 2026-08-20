package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- щит совместимости для видов операций (0324) --------------------------------------------------
//
// ЧЕТВЁРТЫЙ ЩИТ ТОЙ ЖЕ ПОРОДЫ, и устройство зеркалит машинный гейт 0306 дословно, включая
// разделение на две функции: правило «payload эхоит то, чего не понимает» читается с ПРОВОДА и
// потому срабатывает до конверсии, а правило «сохранённая карточка несёт факты» требует загруженной
// карточки. Слить их значило бы опустить и первое правило до поздней точки.
//
// ЗАЧЕМ ОН НУЖЕН, ЕСЛИ ГЛАГОЛЫ НОВЫЕ. У девяти новых глаголов (16..24) потери и правда быть не
// должно: старый бандл этих токенов не знает и шага с таким глаголом не построит. Но ТРИНАДЦАТЬ
// полей волны сидят на СТАРЫХ парах (глагол, machine_type), которые живут в проде годами и которые
// сегодняшний бандл шлёт каждый день:
//
//   - блок `stitching` (52) ЦЕЛИКОМ — любой MACHINE: needle_count, needle_gauge_mm, seam_securing,
//     row_spacing_mm, fullness_ratio, плюс binding_style (binding_taping) и label_attach_stitch;
//   - блок `fastening` (63) ЦЕЛИКОМ — MACHINE + buttonhole | bartack | button_attach |
//     zipper_setting: шесть полей петель, закрепок, пуговиц и молний;
//   - часть блоков `hardware` (54) и `placement_layout` (53) — на MACHINE с цикловым machine_type.
//
// Операции пишутся ПОЛНОЙ ЗАМЕНОЙ, у шага НЕТ СТАБИЛЬНОГО КЛЮЧА, и перенести хранимое, как
// переносится разметка детали, невозможно — ровно тот довод, по которому машинный гейт выбрал отказ
// вместо слияния. Значит единственная форма защиты — отказ: технолог заполнил поля новым клиентом,
// кто-то открыл ту же карточку отставшей вкладкой, сохранил — и факты стёрты молча. ПОРЯДКОМ
// ВЫКАТКИ ЭТО НЕ ЗАКРЫВАЕТСЯ: открытая вкладка ест данные и после деплоя клиента.
//
// ЧЕГО ЩИТ НЕ ДЕЛАЕТ. Он не фильтрует поля: разбор блоков 51..61 и 63 идёт всегда, независимо от
// флага. «Игнорировать при aware=false» выглядит защитой, а на деле открывает дыру —
// CloneStyleForSeason строит payload сам, и клон карточки с заполненной волной вернулся бы пустым
// без единой ошибки. Поэтому серверные пути ставят флаг ЯВНО (style.go, betaseed), а не
// обходят гейт молчанием.
//
// ПАРНОГО `*_cleared` У НЕГО НЕТ — как у machine_fields_aware и в отличие от узлов и снимков. Там
// «снять разметку целиком» есть отдельное намерение, выразимое кнопкой, и осведомлённая пустота
// против непустой карточки — почти наверняка авария. Здесь «поле пусто» — РЯДОВАЯ ПРАВКА: технолог
// стёр стиль петли, потому что он больше не нужен. Бекстоп объявил бы такую правку ошибкой и сделал
// бы тринадцать полей НЕСТИРАЕМЫМИ — классический дефект такой защиты. Осведомлённая запись очищает
// поля волны честно, и это покрыто тестом отдельной клеткой.
//
//	stored нет | payload нет | aware нет  → сохранить (сегодняшний путь, ни одной проверки)
//	stored нет | эхо волны   | aware нет  → отказ: бандл эхоит блоки/глаголы, которых не знает
//	stored нет | любой       | aware есть → сохранить
//	stored есть| —           | aware нет  → FailedPrecondition: устаревшая вкладка
//	stored есть| ФАКТЫ       | aware есть → сохранить (обычное редактирование)
//	stored есть| БЕЗ ФАКТОВ  | aware есть → СОХРАНИТЬ И ОЧИСТИТЬ: см. абзац про отсутствие cleared

const outdatedOperationKindsClientFix = "this version of the admin panel cannot edit operation-kind settings, and its save replaces the whole step list — update the admin panel (hard-refresh) and try again"

func outdatedOperationKindsClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedOperationKindsClientFix)
}

// operationKindsWireGate — правило 1, читается с провода до конверсии.
//
// С ПРОВОДА, А НЕ С СУЩНОСТИ, по той же причине, что у машинного гейта: конвертер канонизирует
// девять легаси-токенов в (machine, <machine>) до того, как сущность появится, и сущностная
// проверка прочитала бы совершенно обычный старый шаг как «говорит про волну».
func operationKindsWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationKindsAware() {
		return nil
	}
	if payloadSpeaksOperationKinds(pb) {
		// Наблюдаемость: без счётчика отказов никто не узнает, бьётся ли старый бандл о щит в
		// проде — а это единственный признак, что клиент где-то не обновился.
		slog.Default().Warn("operation kinds gate refused an unaware payload that echoes the wave",
			slog.String("gate", "wire"), slog.String("cell", "stored:any/payload:kinds/aware:no"))
		return outdatedOperationKindsClient("the payload carries operation-kind settings it does not declare support for")
	}
	return nil
}

// operationKindsStoredGate — правило 2, требует загруженной карточки. Именно оно и срабатывает на
// практике: payload бандла, который выбросил непонятные ему блоки, выглядит невинно, и только
// хранилище знает, что запись собирается стереть.
func operationKindsStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationKindsAware() {
		return nil
	}
	if storedHasOperationKindFacts(stored) {
		slog.Default().Warn("operation kinds gate refused an outdated bundle against a card with wave facts",
			slog.String("gate", "stored"), slog.String("cell", "stored:kinds/aware:no"),
			slog.Int("tech_card_id", storedCardID(stored)))
		return outdatedOperationKindsClient("this tech card holds operation-kind settings on its steps (stitching, placement, hardware, print, welding, trimming, cleaning, inspection or fastening parameters)")
	}
	return nil
}

// payloadSpeaksOperationKinds — предикат правила 1.
//
// ОДИН ПРЕДИКАТ, А НЕ ДВА, в отличие от щита узлов: там поле 46 несёт и чистые детали, то есть его
// наличие ещё не факт сборки. Здесь всё наоборот — блок-сообщение существует ровно затем, чтобы его
// ОТСУТСТВИЕ значило «бандл про это семейство молчит». Присланный блок и есть факт.
//
// Девять новых глаголов считаются наравне с блоками: шаг PACK полей не несёт вовсе, и без глагола
// в предикате бандл, эхоящий его сырым номером енума, прошёл бы щит насквозь.
func payloadSpeaksOperationKinds(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, o := range pb.GetOperations() {
		if o == nil {
			continue
		}
		switch o.GetOperationType() {
		case pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HARDWARE_SET,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRINT,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_TRIM,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_THREAD_TRIM,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_CLEAN,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_INSPECT,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FOLD,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PACK,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_WET_PROCESS:
			return true
		}
		// Десять блоков и два плоских поля волны — те самые 32 колонки.
		if o.GetStitching() != nil ||
			o.GetPlacementLayout() != nil ||
			o.GetHardware() != nil ||
			o.GetPrint() != nil ||
			o.GetWeld() != nil ||
			o.GetTrim() != nil ||
			o.GetThreadTrim() != nil ||
			o.GetClean() != nil ||
			o.GetInspect() != nil ||
			o.GetFastening() != nil {
			return true
		}
		if o.GetPrintMethod() != pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_UNKNOWN ||
			o.GetWetProcessKind() != pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_UNKNOWN {
			return true
		}
	}
	return false
}

// storedHasOperationKindFacts — предикат правила 2: несёт ли СОХРАНЁННАЯ карточка факты волны.
//
// ВСЕ 32 КОЛОНКИ, А НЕ ТРИНАДЦАТЬ «ДЕЛЬТОВЫХ», и это решение, а не небрежность. Довод «у остальных
// глагол сам доказывает осведомлённость» верен ровно наполовину: он доказывает, что старый бандл не
// СОЗДАСТ такой шаг, но ничего не говорит о том, что он его не УДАЛИТ. Шаг PACK или FOLD не несёт ни
// одной из 32 колонок; бандл, выбросивший непонятный ему шаг из списка, стёр бы его при полной
// замене, а предикат по одним лишь колонкам ответил бы «фактов нет» и пропустил запись. Поэтому
// глагол здесь считается наравне с колонкой.
//
// И обратный довод к «только дельта»: список дельты сам оказался неполон. Пять полей блока
// `stitching` (needle_count, needle_gauge_mm, seam_securing, row_spacing_mm, fullness_ratio) сидят на
// ЛЮБОМ MACHINE — то есть на паре, которую сегодняшний бандл шлёт каждый день, — и в перечень
// «восьми дельтовых» не попали. Правило «только дельта» оставило бы ровно ту дыру, ради которой щит
// написан, и разъезжалось бы дальше при каждом добавлении колонки. Одно правило: любая колонка волны
// и любой новый глагол — факт.
func storedHasOperationKindFacts(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		o := &stored.Operations[i]
		switch o.OperationType {
		case entity.OpTypeHardwareSet, entity.OpTypePrint, entity.OpTypeTrim,
			entity.OpTypeThreadTrim, entity.OpTypeClean, entity.OpTypeInspect,
			entity.OpTypeFold, entity.OpTypePack, entity.OpTypeWetProcess:
			return true
		}
		// Порядок — КАНОН волны (§1 entity): тот же, что у ALTER'ов миграции, списка колонок
		// INSERT'а и SELECT'а операций. Пятый список, обязанный совпасть с четырьмя.
		if o.NeedleCount.Valid || o.NeedleGaugeMm.Valid || o.SeamSecuring.Valid ||
			o.RowSpacingMm.Valid || o.FullnessRatio.Valid {
			return true
		}
		if o.PlacementCount.Valid || o.PitchMm.Valid {
			return true
		}
		if o.AttachMethod.Valid || o.HolePrep.Valid || o.Reinforcement.Valid ||
			o.FoldbackMm.Valid || o.CycleStitchCount.Valid {
			return true
		}
		if o.PrintMethod.Valid || o.PeelMode.Valid || o.SecondPressSec.Valid || o.PressureScale.Valid {
			return true
		}
		if o.AirTemperatureC.Valid || o.FeedSpeedMMin.Valid {
			return true
		}
		if o.TrimAction.Valid || o.ResidualAllowanceMm.Valid {
			return true
		}
		if o.ResidualTailMaxMm.Valid {
			return true
		}
		if o.CleaningKind.Valid || o.CoverageMode.Valid || o.WetProcessKind.Valid {
			return true
		}
		if o.ButtonholeStyle.Valid || o.CutLengthMm.Valid || o.ButtonholeOrientation.Valid ||
			o.BartackLengthMm.Valid || o.AttachPattern.Valid || o.ZipperApplication.Valid ||
			o.BindingStyle.Valid || o.LabelAttachStitch.Valid {
			return true
		}
	}
	return false
}
