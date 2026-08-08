package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ПРОЕКЦИЯ КАЛИБРОВКИ КОЭФФИЦИЕНТА НА ПРОВОД (Ф5б.3, §4.2) — всё, что стоит между настилами в базе
// и ответом GetMaterialCuttingCoefficientSuggestion. Чистая функция: ни базы, ни часов, ни контекста.
//
// ЗДЕСЬ НЕТ НИ ОДНОГО СОБСТВЕННОГО ЧИСЛА, и это главное свойство файла. Правило калибровки —
// MaterialCoefficientSuggestionOf; план настила — LayPlannedGeometryOf; перевод плана в единицу
// факта — entity.ProductionRunLayDrift; артикул за настилом — LayArticleMaterialId. Файл ТОЛЬКО
// соединяет их и переводит доли в проценты на самой границе провода. Всякий раз, когда сюда
// захочется дописать арифметику, это будет второе определение чего-то, у чего уже есть владелец.

// MaterialCoefficientCalibrationInput is everything GetMaterialCuttingCoefficientSuggestion reads,
// already loaded.
type MaterialCoefficientCalibrationInput struct {
	// MaterialId is the article being calibrated. Настилы, разрешающиеся В ДРУГОЙ артикул, выбывают
	// молча и обязаны выбывать молча: они не «пропущенные замеры этого артикула», они замеры чужого.
	MaterialId int
	// ArticleUom is material.unit AS STORED — свободный текст, не канон. Сверка с единицей замера
	// идёт через перечень (entity.SameMaterialUnit внутри LayFactDriftOf), поэтому «м» и "m" здесь
	// одно и то же, а канонизировать колонку на чтении нечем и незачем.
	//
	// ПУСТО = «единица артикула неизвестна», и тогда сверки единиц не будет вовсе. Это НЕ дыра:
	// придумать расхождение из отсутствующих данных значило бы молча выбросить настилы из медианы и
	// опустить артикул ниже порога трёх — на экране это выглядело бы как «фактов мало» при полном
	// журнале замеров.
	ArticleUom string
	// Lays are the CANDIDATES the store returned: настилы с фактом, чья карточка НАЗЫВАЕТ этот
	// артикул хоть где-то. Отбор среди них — работа этой функции, а не запроса; см. ниже.
	Lays []entity.ProductionRunLayFact
	// Cards is tech_card_id → карточка, ЦЕЛИКОМ ЗАГРУЖЕННАЯ настоящим загрузчиком (GetTechCardById).
	// Отсутствие ключа — это не «карточка без слотов», а несобранный вход: резолвер ответит 0, настил
	// выпадет, и медиана тихо станет короче. Вызывающий обязан упасть, а не подать сюда дырявую карту.
	Cards map[int]*entity.TechCard
}

// BuildMaterialCoefficientSuggestion assembles the whole GetMaterialCuttingCoefficientSuggestion
// response.
//
// АРТИКУЛ ЗА НАСТИЛОМ РЕЗОЛВИТСЯ ЗДЕСЬ, И РОВНО ТЕМ ЖЕ РЕЗОЛВЕРОМ, ЧТО И ВЕЗДЕ.
// production_run_lay не хранит material_id — он хранит пару (колорвей, слот BOM), и артикул за ней
// даёт LayArticleMaterialId: пин колорвея, если он есть, иначе умолчание слота. Второго резолвера
// быть не должно, и SQL-джойн `bi.material_id = :material` был бы именно им — он видит только
// умолчание слота и потому теряет КАЖДЫЙ настил, у которого колорвей приколол другую ткань. Запрос
// поэтому лишь ОГРАНИЧИВАЕТ работу (карточки, вовсе не называющие артикул), а решает — эта строка.
func BuildMaterialCoefficientSuggestion(in MaterialCoefficientCalibrationInput) *pb_admin.GetMaterialCuttingCoefficientSuggestionResponse {
	obs := make([]LayFactObservation, 0, len(in.Lays))
	// planned keeps the plan RESTATED IN THE FACT'S UNIT per lay, so the wire can put it beside the
	// fact without a second conversion — and so the drift shown is visibly the ratio of the two
	// numbers shown.
	planned := make(map[int]decimal.NullDecimal, len(in.Lays))
	byLay := make(map[int]*entity.ProductionRunLayFact, len(in.Lays))

	for i := range in.Lays {
		l := &in.Lays[i]
		if LayArticleMaterialId(in.Cards[l.TechCardId], l.ColorwayId, bomItemIdOf(&l.ProductionRunLay)) != in.MaterialId {
			continue
		}
		// ПЛАН БЕРЁТСЯ ИЗ entity.ProductionRunLayDrift, А НЕ ПЕРЕСЧИТЫВАЕТСЯ. План настила хранится в
		// САНТИМЕТРАХ (геометрия), а факт — в единице замера, и перевод одного в другое уже написан и
		// имеет владельца: PlannedInFactUnit. Второй перевод см→метры разошёлся бы с первым при первой
		// же правке, и разошёлся бы МОЛЧА — обе стороны продолжили бы выдавать правдоподобные числа, а
		// проявилось бы это как ложный дрейф на артикуле, у которого всё в порядке.
		//
		// Невычислимый перевод (факт в килограммах против плана в сантиметрах — их не связать без
		// плотности и ширины) оставляет план ПУСТЫМ, и правило калибровки называет такой настил само.
		v := entity.ProductionRunLayDrift(LayPlannedGeometryOf(&l.ProductionRunLay).TotalCm(), l.ProductionRunLay)
		p := decimal.NullDecimal{}
		if v.Known {
			p = decimal.NullDecimal{Decimal: v.PlannedInFactUnit, Valid: true}
		}
		planned[l.Id] = p
		byLay[l.Id] = l
		obs = append(obs, LayFactObservation{
			LayId:      l.Id,
			LayKey:     l.LayKey,
			PlannedQty: p,
			ActualQty:  l.ActualQty,
			ActualUom:  l.ActualUom.String,
		})
	}

	sug := MaterialCoefficientSuggestionOf(obs, in.ArticleUom)

	out := &pb_admin.GetMaterialCuttingCoefficientSuggestionResponse{
		Status:   coefficientSuggestionStatusPb(sug.Status),
		LayCount: int32(sug.LayCount),
		Detail:   sug.Detail,
		Drifts:   make([]*pb_common.MaterialCoefficientLayDrift, 0, len(sug.Drifts)),
		// ДВА ЧИСЛА, РАЗНЫЕ ЕДИНИЦЫ, И ОБА ОБЯЗАТЕЛЬНЫ. Дрейф — ИЗМЕРЕНИЕ и едет в ПРОЦЕНТАХ (единица
		// названа в имени поля, как у actual_drift_percent на настиле — единственная дисциплина, при
		// которой процент и доля не могут разъехаться). Предложение — МНОЖИТЕЛЬ, как его хранит 0270,
		// и едет как есть. Отдай мы одно измерение, забытый «+1» дал бы артикулу коэффициент 0.03, а
		// EffectiveCuttingCoefficient читает всё, что меньше единицы, как «не задано» — калибровка
		// молча обнулила бы саму себя.
		MedianDriftPercent:   pbPercentFromNullFraction(sug.MedianDrift),
		SuggestedCoefficient: pbDecimalFromNull(sug.Suggested),
	}

	for _, d := range sug.Drifts {
		l := byLay[d.LayId]
		if l == nil {
			// Недостижимо: правило возвращает по наблюдению на вход и в том же порядке. Пропуск, а не
			// паника — но и не пустая строка с одним id: настил, который нечем назвать, показать нечем.
			continue
		}
		out.Drifts = append(out.Drifts, &pb_common.MaterialCoefficientLayDrift{
			LayId:        int32(l.Id),
			LayKey:       l.LayKey,
			LayName:      l.Name,
			RunId:        int32(l.RunId),
			TechCardId:   int32(l.TechCardId),
			TechCardName: l.TechCardName,
			PlannedQty:   pbDecimalFromNull(planned[l.Id]),
			ActualQty:    pbDecimalFromNull(l.ActualQty),
			ActualUom:    pbMaterialUnit(l.ActualUom.String),
			// Дрейф ЭТОГО настила — тот самый, что стоит под медианой, а не пересчитанный заново:
			// медиана обязана быть медианой ПОКАЗАННЫХ чисел, иначе человек, ищущий выброс, ищет его в
			// другом наборе. Пусто ровно у невошедших, и тогда причина названа в Skipped.
			DriftPercent: pbPercentFromNullFraction(d.Drift),
			Skipped:      d.Skipped,
			ActualAt:     layActualAtPb(l.ActualAt),
		})
	}
	return out
}

// pbPercentFromNullFraction turns a fraction (0.06) into the wire's percent (6.00), or into ABSENCE.
// The multiplication lives HERE and only here for this response, exactly as layDriftPercentPb owns
// it for the lay: the field name is the only place the unit is stated, and the two must not drift.
func pbPercentFromNullFraction(v decimal.NullDecimal) *pb_decimal.Decimal {
	if !v.Valid {
		return nil
	}
	return pbDecimalFromDecimal(v.Decimal.Mul(decimal.NewFromInt(100)).Round(2))
}

// coefficientSuggestionStatusPb maps the rule's three answers onto the wire's. UNSPECIFIED is NEVER
// produced: every branch of MaterialCoefficientSuggestionOf sets one of the three, and the default
// arm exists so a status added to the rule without a wire value fails loudly as «не заполнено»
// rather than quietly as READY.
func coefficientSuggestionStatusPb(s CoefficientSuggestionStatus) pb_common.MaterialCoefficientSuggestionStatus {
	switch s {
	case CoefficientSuggestionTooFewFacts:
		return pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_TOO_FEW_FACTS
	case CoefficientSuggestionOutOfRange:
		return pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_OUT_OF_RANGE
	case CoefficientSuggestionReady:
		return pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_READY
	}
	return pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_UNSPECIFIED
}
