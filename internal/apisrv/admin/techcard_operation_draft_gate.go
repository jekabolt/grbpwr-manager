package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- ЩИТ ЧЕРНОВИКА КАРКАСА (0410) ----------------------------------------------------------------
//
// Шестой щит той же породы, что operation_work_aware (techcard_operation_work_gate.go), и устроен
// так же: правило 1 читается с провода, правило 2 — по загруженной карточке.
//
// ФЛАГ ЯВНЫЙ: `draft` — bool без присутствия, и «старый бандл поля не знает» неотличимо от «все
// шаги проверены». ОТКАЗ, А НЕ СЛИЯНИЕ: операции пишутся полной заменой и у строки шага нет
// стабильного ключа — сервер не знает, какой присланной строке принадлежала метка сохранённой.
// Честных исходов два: метки доезжают целиком (aware) либо сохранение отказывает целиком.
//
//	stored нет | draft нет | aware нет  → сохранить (каждая карточка до каркаса)
//	stored —   | draft ЕСТЬ| aware нет  → отказ: старый бандл такого поля не знает, значит это эхо
//	stored ЕСТЬ| —         | aware нет  → FailedPrecondition: устаревшая вкладка стёрла бы метки
//	любое      | любое     | aware есть → сохранить (draft = false на шаге — это «проверено»)

const outdatedOperationDraftClientFix = "this version of the admin panel does not know which steps are unreviewed drafts, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationDraftClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedOperationDraftClientFix)
}

// operationDraftWireGate — правило 1, с провода до конверсии.
func operationDraftWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationDraftAware() || !payloadCarriesOperationDraft(pb) {
		return nil
	}
	slog.Default().Warn("operation draft gate refused an unaware payload that carries draft steps",
		slog.String("gate", "wire"), slog.String("cell", "payload:draft/aware:no"))
	return outdatedOperationDraftClient("the payload marks steps as drafts without declaring support for it")
}

// operationDraftStoredGate — правило 2: только хранилище знает, что полная замена сотрёт метки.
func operationDraftStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationDraftAware() || !storedHasDraftOperations(stored) {
		return nil
	}
	slog.Default().Warn("operation draft gate refused an outdated bundle against a card with draft steps",
		slog.String("gate", "stored"), slog.String("cell", "stored:draft/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationDraftClient("this tech card has steps the assembly skeleton wrote that nobody has reviewed yet")
}

func payloadCarriesOperationDraft(pb *pb_common.TechCardInsert) bool {
	for _, o := range pb.GetOperations() {
		if o.GetDraft() {
			return true
		}
	}
	return false
}

func storedHasDraftOperations(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if stored.Operations[i].Draft {
			return true
		}
	}
	return false
}
