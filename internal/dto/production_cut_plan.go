package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// ComputeProductionRunCutPlan projects a run's lines onto the style's cut pieces: деталь × колорвей
// × размер → сколько панелей выкроить и из какого артикула. Сестра ComputeProductionRunMaterialPlan
// и её антипод по вопросу: тот отвечает «сколько ткани заказать и хватает ли её», этот — «что из
// этой ткани выкроить». Обе проекции ЧИТАЮТ прогон и карту и ничего не хранят.
//
// `card` — это спецификация, по которой печатается наряд: снапшот РЕЛИЗА, если прогон к нему
// привязан, иначе живая карта. Решение принимает вызывающий (у него репозиторий), а `release`
// нужен только чтобы ответ мог назвать ревизию, по которой кроят.
//
// ИНВАРИАНТ, который нельзя нарушить: pieces_to_cut = pieces_per_garment × garments. cut_symmetry
// НЕ множитель (0266 свернул зеркальное удвоение в само количество, 0275 вернул одну лишь
// классификацию) — она едет в ответ словами для закройщика.
//
// Денег в ответе нет по построению: он уходит на бумагу в цех и в публичный манифест наряда, где
// RBAC-стрипа костинга не существует.
func ComputeProductionRunCutPlan(
	run *entity.ProductionRun,
	card *entity.TechCard,
	release *entity.TechCardReleaseMeta,
) *pb_admin.GetProductionRunCutPlanResponse {
	// TODO(Ф2-бэк): реализация. Контракт и инварианты — в admin.proto (CutPlanRow/CutPlanBlocker)
	// и в комментарии выше.
	return &pb_admin.GetProductionRunCutPlanResponse{}
}
