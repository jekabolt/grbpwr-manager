package admin

import (
	"log/slog"
	"strings"

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
// должно: старый бандл этих токенов не знает и шага с таким глаголом не построит. Но ВОСЕМНАДЦАТЬ
// полей волны сидят на СТАРЫХ парах (глагол, machine_type), которые живут в проде годами и которые
// сегодняшний бандл шлёт каждый день:
//
//   - блок `stitching` (52) ЦЕЛИКОМ — любой MACHINE, СЕМЬ полей: needle_count, needle_gauge_mm,
//     seam_securing, row_spacing_mm, fullness_ratio, плюс binding_style (binding_taping) и
//     label_attach_stitch;
//   - блок `fastening` (63) ЦЕЛИКОМ — MACHINE + buttonhole | bartack | button_attach |
//     zipper_setting: ШЕСТЬ полей петель, закрепок, пуговиц и молний;
//   - ТРИ поля блока `hardware` (54) — hole_prep, reinforcement, cycle_stitch_count — на MACHINE с
//     ЦИКЛОВЫМ machine_type (buttonhole | button_attach | bartack). Остальные два поля блока,
//     attach_method и foldback_mm, сюда не входят: они живут только на HARDWARE_SET, глаголе
//     новом;
//   - блок `placement_layout` (53) ЦЕЛИКОМ — ДВА поля, placement_count и pitch_mm, — на ЛЮБОМ
//     MACHINE и БЕЗ ВСЯКОЙ проверки машинки: parseOperationKinds пускает его на
//     isMachine || isHardware || isPrint (techcard_operation_kinds.go, блок PL). Здесь раньше
//     стояло «на MACHINE с цикловым machine_type» — текст расходился с кодом и как раз этим
//     занижал счёт: 7 + 6 + 3 + 2 = 18, а не 13.
//
// И ЭТО НЕ ВЕСЬ УБЫТОК. 0324 расширяет ещё и СЛОВАРИ КОЛОНОК, КОТОРЫЕ ЖИВУТ ГОДАМИ (шаги 4..9
// миграции): machine_type шага +2 (seam_taping, ultrasonic_welder), press_cloth шага +1
// (silicone_paper), topstitch_mode +2 (in_ditch, parallel_to_seam), equipment профиля парка +2
// (те же две машинки), press_cloth профиля +1 и bom_item.kind +2 (seam_sealing_tape,
// embroidery_stabilizer). Шаг MACHINE + ultrasonic_welder без единого поля weld-блока, шаг с
// topstitch_mode = in_ditch, шаг с press_cloth = silicone_paper, строка BOM с
// kind = embroidery_stabilizer — все законны и НЕ НЕСУТ НИ ОДНОЙ из 32 колонок и ни одного из
// девяти глаголов. Теряются они так же молча: parseTopstitch при Mode = UNKNOWN отдаёт пустые
// mode/width/rows, то есть режим отстрочки уносит с собой и ширину, и число рядов; press_cloth и
// bom.kind опциональны и стираются без единого слова (machine_type спасён случайно — щит 0306
// требует его при aware=true и отвечает шумным отказом).
//
// ПОЭТОМУ ОБА ПРЕДИКАТА СЧИТАЮТ ТОКЕНЫ, А НЕ ЗАПОЛНЕННОСТЬ ПОЛЯ. Проверка вида
// `machine_type != UNKNOWN` объявила бы фактом волны КАЖДЫЙ обычный lockstitch-шаг и заблокировала
// бы сегодняшнюю админку целиком — ровно тот отказ, от которого щит обязан воздержаться.
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
// бы восемнадцать полей НЕСТИРАЕМЫМИ — классический дефект такой защиты. Осведомлённая запись
// очищает поля волны честно, и это покрыто тестом отдельной клеткой.
//
//	stored нет | payload нет | aware нет  → сохранить (сегодняшний путь, ни одной проверки)
//	stored нет | эхо волны   | aware нет  → отказ: бандл эхоит блоки/глаголы, которых не знает
//	stored нет | любой       | aware есть → сохранить
//	stored есть| —           | aware нет  → FailedPrecondition: устаревшая вкладка
//	stored есть| ФАКТЫ       | aware есть → сохранить (обычное редактирование)
//	stored есть| БЕЗ ФАКТОВ  | aware есть → СОХРАНИТЬ И ОЧИСТИТЬ: см. абзац про отсутствие cleared

// --- РАСШИРЕННЫЕ СЛОВАРИ ЖИВЫХ КОЛОНОК -----------------------------------------------------------
//
// Шаги 4..9 миграции 0324 дописывают токены в шесть словарей, каждый из которых стоит на колонке,
// существующей задолго до волны. Токен и есть факт: старый бандл этих строк не знает и написать их
// не мог, поэтому их присутствие однозначно говорит «карточку правил новый клиент», тогда как сама
// колонка ничего не доказывает — она заполнена на каждом сегодняшнем шаге.
//
// ОДИН СПИСОК НА ОБА ПРЕДИКАТА: проводные множества СТРОЯТСЯ из токенных (waveEnums), а не
// выписываются вторым списком руками. Словарь, записанный дважды, разъезжается, и разъезжается
// всегда та половина, которую никто не читает.

// waveTokens сверяет каждый токен расширения со словарём entity и возвращает множество для
// предиката. Опечатка в такой строке сборку не ломает — она просто НИКОГДА не совпадает, и правило
// тихо перестаёт существовать; сверка ловит её в первую секунду жизни процесса (приём dto).
func waveTokens[T ~string](vocab map[T]bool, tokens ...string) map[string]bool {
	out := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		if !vocab[T(t)] {
			panic("operation kinds gate names a token that is not in the vocabulary: " + t)
		}
		out[t] = true
	}
	return out
}

// waveEnums переводит токенное множество в проводное по имени члена enum'а. Отсутствие члена —
// паника на init по той же причине: молчаливо пустое множество означало бы щит без правила.
func waveEnums[T ~int32](prefix string, values map[string]int32, tokens map[string]bool) map[T]bool {
	out := make(map[T]bool, len(tokens))
	for t := range tokens {
		name := prefix + strings.ToUpper(t)
		v, ok := values[name]
		if !ok {
			panic("operation kinds gate: no enum member " + name + " for token " + t)
		}
		out[T(v)] = true
	}
	return out
}

var (
	// Шаг 4 (tech_card_operation.machine_type) и шаг 7 (tech_card_equipment_profile.equipment) —
	// ОДИН И ТОТ ЖЕ список из двух безыгольных машин, ровно как в миграции.
	waveMachineTypeTokens = waveTokens(entity.ValidMachineTypes, "seam_taping", "ultrasonic_welder")
	// Шаг 6 (topstitch_mode). Терять его дороже всего: parseTopstitch при UNKNOWN обнуляет заодно
	// ширину и число рядов.
	waveTopstitchModeTokens = waveTokens(entity.ValidTopstitchModes,
		string(entity.TopstitchInDitch), string(entity.TopstitchParallelToSeam))
	// Шаги 5 и 8 — press_cloth шага и профиля; словари обязаны совпадать, шаг наследует профиль.
	wavePressClothTokens = waveTokens(entity.ValidPressCloths, "silicone_paper")
	// Шаг 9 (tech_card_bom_item.kind).
	waveBomKindTokens = waveTokens(entity.ValidTechCardBomKinds,
		string(entity.BomKindSeamSealingTape), string(entity.BomKindEmbroideryStabilizer))
)

var (
	waveMachineTypesPb = waveEnums[pb_common.TechCardMachineType](
		"TECH_CARD_MACHINE_TYPE_", pb_common.TechCardMachineType_value, waveMachineTypeTokens)
	waveTopstitchModesPb = waveEnums[pb_common.TechCardTopstitchMode](
		"TECH_CARD_TOPSTITCH_MODE_", pb_common.TechCardTopstitchMode_value, waveTopstitchModeTokens)
	wavePressClothsPb = waveEnums[pb_common.TechCardPressCloth](
		"TECH_CARD_PRESS_CLOTH_", pb_common.TechCardPressCloth_value, wavePressClothTokens)
	waveBomKindsPb = waveEnums[pb_common.TechCardBomKind](
		"TECH_CARD_BOM_KIND_", pb_common.TechCardBomKind_value, waveBomKindTokens)
)

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
	// Два расширенных словаря живут НЕ НА ШАГЕ, поэтому и проверяются вне цикла: парк оборудования
	// карточки (шаги 7 и 8 миграции) и строки BOM (шаг 9).
	if ed := pb.GetConstruction().GetEquipmentDefaults(); ed != nil {
		for _, m := range ed.GetMachines() {
			if waveMachineTypesPb[m.GetMachineType()] {
				return true
			}
		}
		for _, pp := range ed.GetPresses() {
			if wavePressClothsPb[pp.GetPressCloth()] {
				return true
			}
		}
	}
	for _, b := range pb.GetBomItems() {
		if waveBomKindsPb[b.GetKind()] {
			return true
		}
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
			o.GetFastening() != nil ||
			// ВТО-под-глагол и направление припуска (0325) — ОДИННАДЦАТЫЙ блок и тот же довод, что
			// у десяти выше: присланный блок и есть факт. И довод отдельный, свой: press_action
			// сидит на ГЛАГОЛЕ PRESS, который живёт в проде годами и который сегодняшний бандл
			// шлёт каждый день, — то есть глагол осведомлённости не доказывает, доказывает только
			// блок.
			o.GetPress() != nil {
			return true
		}
		if o.GetPrintMethod() != pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_UNKNOWN ||
			o.GetWetProcessKind() != pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_UNKNOWN {
			return true
		}
		// Три расширенных словаря шага — ПО ТОКЕНУ, а не по заполненности. `machine_type != UNKNOWN`
		// отверг бы каждый обычный MACHINE-шаг, который сегодняшняя админка шлёт непрерывно.
		if waveMachineTypesPb[o.GetMachineType()] ||
			waveTopstitchModesPb[o.GetTopstitch().GetMode()] ||
			wavePressClothsPb[o.GetPressCloth()] {
			return true
		}
	}
	return false
}

// storedHasOperationKindFacts — предикат правила 2: несёт ли СОХРАНЁННАЯ карточка факты волны.
//
// ВСЕ 32 КОЛОНКИ, А НЕ ВОСЕМНАДЦАТЬ «ДЕЛЬТОВЫХ», и это решение, а не небрежность. Довод «у
// остальных глагол сам доказывает осведомлённость» верен ровно наполовину: он доказывает, что
// старый бандл не СОЗДАСТ такой шаг, но ничего не говорит о том, что он его не УДАЛИТ. Шаг PACK
// или FOLD не несёт ни одной из 32 колонок; бандл, выбросивший непонятный ему шаг из списка,
// стёр бы его при полной замене, а предикат по одним лишь колонкам ответил бы «фактов нет» и
// пропустил запись. Поэтому глагол здесь считается наравне с колонкой.
//
// И обратный довод к «только дельта»: СПИСОК ДЕЛЬТЫ САМ ПЕРЕСЧИТЫВАЛСЯ ДВАЖДЫ, и оба раза вверх.
// Сперва в перечень «восьми дельтовых» не попали пять полей блока `stitching` (needle_count,
// needle_gauge_mm, seam_securing, row_spacing_mm, fullness_ratio) — они сидят на ЛЮБОМ MACHINE, то
// есть на паре, которую сегодняшний бандл шлёт каждый день. Потом оказалось, что и тринадцать —
// занижение: hole_prep, reinforcement и cycle_stitch_count живут на цикловом MACHINE, а
// placement_count и pitch_mm — на любом. Восемнадцать. Правило «только дельта» оставило бы ровно ту
// дыру, ради которой щит написан, и разъезжалось бы дальше при каждом добавлении колонки. Одно
// правило: любая колонка волны и любой новый глагол — факт.
//
// И ОТДЕЛЬНО — ТОКЕНЫ, ДОПИСАННЫЕ В СЛОВАРИ ЖИВЫХ КОЛОНОК (шаги 4..9 миграции). Колонок они не
// добавляют вовсе, поэтому предикат «по 32 колонкам и девяти глаголам» отвечал бы на такой
// карточке «фактов нет»: шаг MACHINE + ultrasonic_welder, шаг с in_ditch, шаг с silicone_paper и
// BOM-строка с embroidery_stabilizer не несут ни одной из 32. Считаются они ПО ТОКЕНУ и только по
// нему — см. шапку файла.
func storedHasOperationKindFacts(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	// Расширенные словари вне шага: парк оборудования (шаги 7, 8) и строки BOM (шаг 9). Здесь тоже
	// ПО ТОКЕНУ — сам факт профиля в парке доказывает только осведомлённость о волне 0306, которую
	// бандл между волнами объявляет честным machine_fields_aware = true.
	if c := stored.Construction; c != nil && c.EquipmentDefaults != nil {
		for i := range c.EquipmentDefaults.Machines {
			if waveMachineTypeTokens[c.EquipmentDefaults.Machines[i].MachineType] {
				return true
			}
		}
		for i := range c.EquipmentDefaults.Presses {
			if wavePressClothTokens[c.EquipmentDefaults.Presses[i].PressCloth.String] {
				return true
			}
		}
	}
	for i := range stored.BomItems {
		if waveBomKindTokens[stored.BomItems[i].Kind.String] {
			return true
		}
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
		// ВТО (0325). Две колонки сверх тридцати двух, и считаются они ровно так же — ПО КОЛОНКЕ:
		// глагол press осведомлённости не доказывает (он в проде годами), доказывает заполненность.
		if o.PressAction.Valid || o.PressToward.Valid {
			return true
		}
		// И ТРИ РАСШИРЕННЫХ СЛОВАРЯ ЖИВЫХ КОЛОНОК ШАГА — по токену. Незаполненная NullString даёт
		// пустую строку, которой ни в одном из множеств нет, поэтому .Valid проверять незачем.
		if waveMachineTypeTokens[o.MachineType.String] ||
			waveTopstitchModeTokens[o.TopstitchMode.String] ||
			wavePressClothTokens[o.PressCloth.String] {
			return true
		}
	}
	return false
}
