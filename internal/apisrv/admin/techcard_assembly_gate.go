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
// ТАБЛИЦА ИСТИННОСТИ ПАРЫ ФЛАГОВ (stored несёт узлы × payload несёт узлы × aware × cleared):
//
//	stored нет | payload нет | aware нет  | —      → сохранить (сегодняшний путь, ни одной проверки)
//	stored нет | payload есть| aware нет  | —      → отказ: бандл эхоит поля, которых не знает
//	stored нет | любой       | aware есть | false  → сохранить
//	stored нет | нет         | aware есть | TRUE   → отказ: «снял разметку» там, где её не было
//	stored есть| —           | aware нет  | —      → FailedPrecondition: устаревшая вкладка
//	stored есть| есть        | aware есть | false  → сохранить (обычное редактирование)
//	stored есть| НЕТ         | aware есть | false  → ОТКАЗ БЕКСТОПОМ: это и есть тихое стирание
//	stored есть| нет         | aware есть | TRUE   → сохранить: снятие разметки, намерение объявлено
//	stored есть| есть        | aware есть | TRUE   → отказ: противоречие, «снял» и одновременно прислал

const outdatedAssemblyClientFix = "this version of the admin panel cannot edit assembly units, and its save replaces the whole step list — update the admin panel (hard-refresh) and try again"

func outdatedAssemblyClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedAssemblyClientFix)
}

// assemblyCapabilityWireGate — правило 1, читается с провода до конверсии.
func assemblyCapabilityWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetAssemblyAware() {
		// Намерение без предмета — теневой флаг. Ловится здесь, а не в конвертере, потому что это
		// утверждение о ЗАПРОСЕ, а не о карточке.
		if pb.GetAssemblyCleared() && payloadSpeaksAssembly(pb) {
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
		return nil
	}
	if !pb.GetAssemblyAware() {
		slog.Default().Warn("assembly gate refused an outdated bundle against a marked-up card",
			slog.String("gate", "stored"), slog.String("cell", "stored:units/aware:no"))
		return outdatedAssemblyClient("this tech card is marked up with assembly units (what each step produces and takes)")
	}
	if payloadSpeaksAssembly(pb) {
		return nil
	}
	if pb.GetAssemblyCleared() {
		// Намерение объявлено — это единственный законный путь снять разметку.
		return nil
	}
	slog.Default().Warn("assembly backstop refused an aware but empty save",
		slog.String("gate", "backstop"), slog.String("cell", "stored:units/payload:none/aware:yes/cleared:no"))
	return status.Error(codes.FailedPrecondition,
		"this save would erase the assembly units on this tech card and does not carry any: "+
			"if you meant to remove them, use «снять разметку узлов»; otherwise reload the card — "+
			"another tab or a restored draft is about to overwrite it")
}

// payloadSpeaksAssembly — несёт ли payload хоть один сборочный факт.
//
// Читается с ПРОВОДА, как и машинный аналог: у сырых полей нет канонизации, которая могла бы
// превратить обычную старую операцию в «говорящую про сборку».
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
