package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ПРОЕКЦИЯ ПРЕДЛОЖЕНИЯ ПРОЦЕНТА РАСКРОЯ НА ПРОВОД (T7 волна 2) — всё, что стоит между настилами в
// базе и ответом GetBomWastageSuggestion. Чистая функция: ни базы, ни часов, ни контекста.
//
// ЗДЕСЬ НЕТ НИ ОДНОГО СОБСТВЕННОГО ЧИСЛА. Правило — BomWastageSuggestionOf; netto — LayNettoOf
// (штамп настила читается раньше живого пересчёта, см. ниже); артикул за настилом —
// LayArticleMaterialId; состав раскладки — entity.TechCardMarkerSummary.CompositionOrLegacy. Файл
// только соединяет их и переводит доли в проценты на самой границе провода.
//
// ЭТО НЕ BuildMaterialCoefficientSuggestion, хотя силуэт тот же. Сосед делит факт на
// ПЛАН-ГЕОМЕТРИЮ (выпады в знаменателе) и калибрует МНОЖИТЕЛЬ артикула; этот файл делит факт на
// NETTO (выпадов в знаменателе нет) и предлагает ПРОЦЕНТ строки BOM. Шапка
// bom_wastage_calibration.go — обязательное чтение до любой правки здесь.

// BomWastageCalibrationInput is everything GetBomWastageSuggestion reads, already loaded.
type BomWastageCalibrationInput struct {
	// MaterialId is the article being suggested for. Настилы, разрешающиеся в ДРУГОЙ артикул,
	// выбывают молча и обязаны выбывать молча: они не «пропущенные замеры этого артикула», они
	// замеры чужого.
	MaterialId int
	// Lays are the CANDIDATES the store returned (ListMeasuredLayCandidates — тот же вход, что у
	// калибровки коэффициента: второго отборщика настилов не заводим). Решает настил за настилом
	// LayArticleMaterialId — единственный резолвер пары (колорвей, слот).
	Lays []entity.ProductionRunLayFact
	// Cards is tech_card_id → карточка, целиком загруженная настоящим загрузчиком (GetTechCardById):
	// резолвер артикула умеет откатываться на позиционный индекс слота, а позиция осмысленна только
	// в порядке настоящего загрузчика. Отсутствие ключа — несобранный вход, вызывающий обязан
	// упасть, а не подать сюда дырявую карту.
	Cards map[int]*entity.TechCard
	// Markers is marker_id → summary С СОСТАВОМ (attachMarkerComposition уже отработал у загрузчика
	// — ListRunMarkers), для ЖИВОГО пересчёта netto у настилов без штампа. Отсутствующий ключ —
	// раскладка удалена: её секция честно скажет «состав не записан», настил выпадет с причиной.
	Markers map[int]entity.TechCardMarkerSummary
	// ConsideredLayCount / TotalMeasuredLayCount — сколько настилов-кандидатов РАССМОТРЕНО (свежее
	// окно отборщика, MAJOR 5) и сколько измеренных кандидатов у артикула ВСЕГО. Оба едут в ответ:
	// «медиана по N» без окна и полного счёта непроверяема — рассмотрено меньше, чем есть, обязано
	// быть видно, а не подразумеваться.
	ConsideredLayCount    int
	TotalMeasuredLayCount int
	// Coefficient — коэффициент раскроя АРТИКУЛА (material.cutting_coefficient через
	// EffectiveCuttingCoefficient), входящий в ЗНАМЕНАТЕЛЬ каждого настила: netto × коэффициент
	// (W3, см. LayWastageDriftOf). INVALID = коэффициента нет, знаменатель — чистое netto.
	//
	// АРТИКУЛ ТОТ, ПО КОТОРОМУ ОТОБРАНЫ НАСТИЛЫ, а не сегодняшний пин какой-то карточки: наблюдения
	// оставляют только те замеры, что разрешаются в MaterialId, поэтому коэффициент здесь
	// принадлежит ровно тому рулону, на котором эти факты и мерились. ЗНАЧЕНИЕ при этом СЕГОДНЯШНЕЕ:
	// исторических коэффициентов система не хранит, и предложение честно говорит, какой линейкой
	// мерило (denominator_cutting_coefficient на проводе).
	Coefficient decimal.NullDecimal
}

// BomWastageObservationsOf resolves the candidates to THIS article's observations — the pure half
// shared by the RPC projection below and by the write-side claim verification
// (verifyBomWastageClaims): оба обязаны видеть ОДНУ медиану, и второй сборщик наблюдений разъехался
// бы с проекцией молча. Возвращает наблюдения и их настилы параллельно (наблюдение i — настил i).
//
// ЗНАМЕНАТЕЛЬ КАЖДОГО НАСТИЛА: сначала ШТАМП — но только ЧЕРЕЗ САМОПРОВЕРКУ
// (entity.TrustedNettoStamp, MAJOR 4): штамп, чей базис разошёлся с сегодняшним фактом, ОТБРОШЕН с
// причиной, а не принят. Дальше живая dxf-норма через LayNettoOf; отказ обеих — skip, называющий
// обе причины.
func BomWastageObservationsOf(in BomWastageCalibrationInput) ([]LayWastageObservation, []*entity.ProductionRunLayFact) {
	obs := make([]LayWastageObservation, 0, len(in.Lays))
	rows := make([]*entity.ProductionRunLayFact, 0, len(in.Lays))

	for i := range in.Lays {
		l := &in.Lays[i]
		if LayArticleMaterialId(in.Cards[l.TechCardId], l.ColorwayId, bomItemIdOf(&l.ProductionRunLay)) != in.MaterialId {
			continue
		}
		o := LayWastageObservation{
			LayId:      l.Id,
			LayKey:     l.LayKey,
			TechCardId: l.TechCardId,
			ActualQty:  l.ActualQty,
			// Настил дошёл сюда только потому, что разрешился в in.MaterialId (проверка строкой
			// выше) — значит это коэффициент ЕГО артикула, а не чужого.
			Coefficient: in.Coefficient,
		}
		stamp, discarded := l.TrustedNettoStamp()
		if stamp.Valid {
			o.NettoQty = stamp
			o.NettoStamped = true
		} else {
			sections := make([]LayNettoSection, 0, len(l.Sections))
			for j := range l.Sections {
				s := &l.Sections[j]
				sec := LayNettoSection{Plies: s.Plies, MarkerLabel: s.MarkerName}
				if sec.MarkerLabel == "" {
					sec.MarkerLabel = fmt.Sprintf("раскладка #%d", s.MarkerId)
				}
				if m, ok := in.Markers[s.MarkerId]; ok {
					// ОДИН владелец состава: CompositionOrLegacy (легаси-пара побеждает проекцию —
					// см. его шапку). Отсутствующий маркер оставляет состав пустым, и netto называет
					// секцию, а не молчит.
					sec.Composition = m.CompositionOrLegacy()
				}
				sections = append(sections, sec)
			}
			res := LayNettoOf(in.Cards[l.TechCardId], l.ColorwayId, bomItemIdOf(&l.ProductionRunLay),
				l.ActualUom.String, sections)
			o.NettoQty = res.Qty
			o.NettoReason = res.Reason
			if discarded != "" && !res.Qty.Valid {
				// Отброшенный штамп называется вместе с отказом живого пересчёта: настил, выпавший
				// из-за отката DO, обязан говорить об этом, а не выглядеть «без нормы».
				o.NettoReason = discarded + "; " + res.Reason
			}
		}
		obs = append(obs, o)
		rows = append(rows, l)
	}
	return obs, rows
}

// BuildBomWastageSuggestion assembles the whole GetBomWastageSuggestionResponse.
//
// ЗНАМЕНАТЕЛЬ КАЖДОГО НАСТИЛА — см. BomWastageObservationsOf (штамп через самопроверку, иначе живая
// dxf-норма, иначе skip с причиной). Какой из двух взят — видно наружу (netto_stamped): живая норма
// могла уйти от той, при которой мерили, и человек, спорящий с дрейфом, обязан знать, с чем именно
// он спорит.
func BuildBomWastageSuggestion(in BomWastageCalibrationInput) *pb_admin.GetBomWastageSuggestionResponse {
	obs, rows := BomWastageObservationsOf(in)
	byLay := make(map[int]*entity.ProductionRunLayFact, len(obs))
	stamped := make(map[int]bool, len(obs))
	netto := make(map[int]decimal.NullDecimal, len(obs))
	for i := range obs {
		byLay[obs[i].LayId] = rows[i]
		stamped[obs[i].LayId] = obs[i].NettoStamped
		netto[obs[i].LayId] = obs[i].NettoQty
	}

	sug := BomWastageSuggestionOf(obs)

	// ОКНО ОТБОРЩИКА НАЗЫВАЕТСЯ (MAJOR 5): когда рассмотрены не все измеренные настилы, «медиана по
	// N» обязана говорить, из какого окна N взят, — иначе она непроверяема.
	detail := sug.Detail
	if in.TotalMeasuredLayCount > in.ConsideredLayCount {
		detail = fmt.Sprintf("%s (рассмотрены %d свежих настилов-кандидатов из %d измеренных)",
			detail, in.ConsideredLayCount, in.TotalMeasuredLayCount)
	}

	out := &pb_admin.GetBomWastageSuggestionResponse{
		Status:                bomWastageSuggestionStatusPb(sug.Status),
		LayCount:              int32(sug.LayCount),
		TechCardCount:         int32(sug.TechCardCount),
		ConsideredLayCount:    int32(in.ConsideredLayCount),
		TotalMeasuredLayCount: int32(in.TotalMeasuredLayCount),
		Detail:                detail,
		Drifts:                make([]*pb_common.BomWastageLayDrift, 0, len(sug.Drifts)),
		// ОДНА ЕДИНИЦА — ПРОЦЕНТ, И ЭТО РЕШЕНИЕ. Сосед (коэффициент) отдаёт измерение-процент И
		// множитель, потому что его поле хранит множитель и забытый «+1» обнулил бы калибровку.
		// Здесь поле хранит процент — вторая единица в ответе повторила бы путаницу «процент против
		// коэффициента» в обратную сторону. Правило уже отдаёт проценты; проекция ничего не умножает.
		MedianOverNettoPercent:  pbDecimalFromNull(sug.MedianPercent),
		SuggestedWastagePercent: pbDecimalFromNull(sug.SuggestedPercent),
		// ЛИНЕЙКА НАЗЫВАЕТСЯ ВМЕСТЕ С ЧИСЛОМ (W3). Значения, применённые до W3, мерились чистым
		// netto, поэтому у артикула с коэффициентом сегодняшняя цифра ниже прежней — и оператор,
		// сверяющий её с сохранённым процентом, обязан видеть, что изменился не факт, а знаменатель.
		DenominatorCuttingCoefficient: pbDecimalFromNull(in.Coefficient),
	}

	for _, d := range sug.Drifts {
		l := byLay[d.LayId]
		if l == nil {
			// Недостижимо: правило возвращает по наблюдению на вход и в том же порядке. Пропуск, а
			// не паника — но и не пустая строка с одним id.
			continue
		}
		out.Drifts = append(out.Drifts, &pb_common.BomWastageLayDrift{
			LayId:        int32(l.Id),
			LayKey:       l.LayKey,
			LayName:      l.Name,
			RunId:        int32(l.RunId),
			TechCardId:   int32(l.TechCardId),
			TechCardName: l.TechCardName,
			NettoQty:     pbDecimalFromNull(netto[l.Id]),
			NettoStamped: stamped[l.Id],
			ActualQty:    pbDecimalFromNull(l.ActualQty),
			ActualUom:    pbMaterialUnit(l.ActualUom.String),
			// Дрейф ЭТОГО настила — тот самый, что стоит под медианой: медиана обязана быть
			// медианой показанных чисел. Пусто ровно у невошедших, причина в Skipped.
			DriftPercent: wastageDriftPercentPb(d.Drift),
			Skipped:      d.Skipped,
			ActualAt:     layActualAtPb(l.ActualAt),
		})
	}
	return out
}

// wastageDriftPercentPb turns a drift fraction (0.22) into THIS response's percent (22.00), or into
// ABSENCE. The multiplication lives here and only here for this response — the same ownership rule
// pbPercentFromNullFraction states for the coefficient response: the field name is the only place
// the unit is named, and the two must not drift.
func wastageDriftPercentPb(v decimal.NullDecimal) *pb_decimal.Decimal {
	if !v.Valid {
		return nil
	}
	return pbDecimalFromDecimal(v.Decimal.Mul(decimal.NewFromInt(100)).Round(2))
}

// bomWastageSuggestionStatusPb maps the rule's three answers onto the wire's. UNSPECIFIED is NEVER
// produced: every branch of BomWastageSuggestionOf sets one of the three, and the default arm
// exists so a status added to the rule without a wire value fails loudly as «не заполнено» rather
// than quietly as READY.
func bomWastageSuggestionStatusPb(s WastageSuggestionStatus) pb_common.BomWastageSuggestionStatus {
	switch s {
	case WastageSuggestionTooFewFacts:
		return pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_TOO_FEW_FACTS
	case WastageSuggestionOutOfRange:
		return pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_OUT_OF_RANGE
	case WastageSuggestionReady:
		return pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_READY
	}
	return pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_UNSPECIFIED
}
