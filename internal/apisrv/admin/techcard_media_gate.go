package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- щит совместимости для операционных фотографий (0308) ----------------------------------------
//
// ТРЕТИЙ ЩИТ ТОЙ ЖЕ ПОРОДЫ, и он существует не «за компанию»: операции пишутся ПОЛНОЙ ЗАМЕНОЙ, а
// значит payload без поля `media` неотличим от «удалили все снимки со всех шагов». Отставшая
// вкладка, восстановленный до-фичевый черновик, сидер или скрипт стёрли бы десятки выносок молча,
// и протухшую подпись CONSTRUCTION потом никто бы не связал с причиной.
//
// Устройство зеркалит гейт узлов сборки (0307) дословно, включая разделение на две функции:
// правило «бандл эхоит то, чего не понимает» читается с ПРОВОДА и срабатывает до конверсии, а
// правило «сохранённая карточка несёт снимки» требует загруженной карточки.
//
// ЧЕГО ЩИТ НЕ ДЕЛАЕТ. Он не фильтрует поля: разбор `media` идёт всегда, независимо от флага.
// «Игнорировать при aware=false» выглядит защитой, а на деле открывает дыру — CloneStyleForSeason
// строит payload сам, транспортных флагов не эмитит и оба гейта обходит, так что клон карточки со
// снимками вернулся бы без них и без единой ошибки.
//
//	stored нет | payload нет | aware нет  | —      → сохранить (сегодняшний путь)
//	stored нет | эхо поля    | aware нет  | —      → отказ: бандл эхоит поле, которого не знает
//	stored нет | любой       | aware есть | false  → сохранить
//	stored нет | нет         | aware есть | TRUE   → отказ: «снял» там, где нечего снимать
//	stored есть| —           | aware нет  | —      → FailedPrecondition: устаревшая вкладка
//	stored есть| СНИМКИ      | aware есть | false  → сохранить (обычное редактирование)
//	stored есть| БЕЗ СНИМКОВ | aware есть | false  → ОТКАЗ БЕКСТОПОМ: это и есть тихое стирание
//	stored есть| без снимков | aware есть | TRUE   → сохранить: снятие объявлено намеренно
//	stored есть| СНИМКИ      | aware есть | TRUE   → отказ: противоречие

const outdatedMediaClientFix = "this version of the admin panel cannot edit operation photos, and its save replaces the whole step list — update the admin panel (hard-refresh) and try again"

func outdatedMediaClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedMediaClientFix)
}

// mediaCapabilityWireGate — правило 1, читается с провода до конверсии.
func mediaCapabilityWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetMediaAware() {
		if pb.GetMediaCleared() && payloadCarriesOperationMedia(pb) {
			return status.Error(codes.InvalidArgument,
				"media_cleared is set together with operation photos in the same payload: decide whether the card keeps its photos or drops them")
		}
		return nil
	}
	if payloadCarriesOperationMedia(pb) {
		slog.Default().Warn("media gate refused an unaware bundle echoing operation photos",
			slog.String("gate", "wire"), slog.String("cell", "aware:no/payload:media"))
		return outdatedMediaClient("this save carries operation photos but does not declare it can edit them")
	}
	if pb.GetMediaCleared() {
		return status.Error(codes.InvalidArgument,
			"media_cleared is set by a bundle that does not declare it can edit operation photos")
	}
	return nil
}

// mediaCapabilityStoredGate — правило 2, требует загруженной карточки.
func mediaCapabilityStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if !storedHasOperationMedia(stored) {
		if pb.GetMediaAware() && pb.GetMediaCleared() {
			slog.Default().Warn("media gate refused media_cleared on a card with no photos",
				slog.String("gate", "stored"), slog.String("cell", "stored:none/aware:yes/cleared:yes"),
				slog.Int("tech_card_id", storedCardID(stored)))
			return status.Error(codes.InvalidArgument,
				"media_cleared is set but this tech card has no operation photos to clear")
		}
		return nil
	}
	if !pb.GetMediaAware() {
		slog.Default().Warn("media gate refused an outdated bundle against a card with photos",
			slog.String("gate", "stored"), slog.String("cell", "stored:media/aware:no"),
			slog.Int("tech_card_id", storedCardID(stored)))
		return outdatedMediaClient("this tech card carries operation photos with callouts")
	}
	if payloadCarriesOperationMedia(pb) {
		return nil
	}
	if pb.GetMediaCleared() {
		return nil
	}
	slog.Default().Warn("media backstop refused an aware but empty save",
		slog.String("gate", "backstop"), slog.String("cell", "stored:media/payload:none/aware:yes/cleared:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return status.Error(codes.FailedPrecondition,
		"this save would erase the operation photos on this tech card and does not carry any: "+
			"if you meant to remove them, use “clear the step photos”; otherwise reload the card — "+
			"another tab or a restored draft is about to overwrite it")
}

// ОДИН ПРЕДИКАТ, А НЕ ДВА, в отличие от щита узлов. Там пришлось разделить «трогает поля» и
// «несёт узлы», потому что поле 46 несёт и чистые детали — то есть его наличие ещё не говорит о
// сборочных фактах. Здесь поле `media` не несёт ничего, кроме снимков: его наличие и есть факт.
func payloadCarriesOperationMedia(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, o := range pb.GetOperations() {
		if len(o.GetMedia()) > 0 {
			return true
		}
	}
	return false
}

func storedHasOperationMedia(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for _, o := range stored.Operations {
		if len(o.Media) > 0 {
			return true
		}
	}
	return false
}
