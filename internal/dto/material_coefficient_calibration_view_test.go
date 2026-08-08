package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Карточка на два слота и два колорвея. Колорвей 100 ПРИКАЛЫВАЕТ артикул 77 на основной слот;
// колорвей 200 пина не имеет и берёт умолчание слота — артикул 42. Оба настилают ОДИН И ТОТ ЖЕ слот
// (id 5), поэтому любой отбор по `tech_card_bom_item.material_id` увидел бы у обоих артикул 42.
func calibCard() *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 5, LineKey: "B1", Section: entity.BomSectionFabric, Name: "основная",
			MaterialId: sql.NullInt64{Int64: 42, Valid: true}},
		{Id: 6, LineKey: "B2", Section: entity.BomSectionLining, Name: "подкладка",
			MaterialId: sql.NullInt64{Int64: 43, Valid: true}},
	}
	card.Colorways = []entity.TechCardColorway{
		{
			ProductId: sql.NullInt32{Int32: 100, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, MaterialId: sql.NullInt64{Int64: 77, Valid: true}},
			},
		},
		{ProductId: sql.NullInt32{Int32: 200, Valid: true}},
	}
	return card
}

// calibLay — настил на одну секцию: длина раскладки × слои даёт план в САНТИМЕТРАХ, факт вводится в
// единице замера. end_loss = 0, чтобы план был ровно тем числом, которое проверяет тест.
func calibLay(id, colorwayID int, markerLenCm string, plies int, actualQty, actualUom string) entity.ProductionRunLayFact {
	l := entity.ProductionRunLayFact{TechCardId: 7, TechCardName: "худи"}
	l.Id = id
	l.RunId = 900 + id
	l.LayKey = "L" + decimal.NewFromInt(int64(id)).String()
	l.Name = "настил " + decimal.NewFromInt(int64(id)).String()
	l.ColorwayId = colorwayID
	l.BomItemId = sql.NullInt64{Int64: 5, Valid: true}
	l.EndLossCm = decimal.Zero
	l.ActualAt = sql.NullTime{Time: time.Unix(1700000000, 0), Valid: true}
	l.Sections = []entity.ProductionRunLaySection{{
		Id: id, LayId: id, MarkerId: 1, Plies: plies,
		MarkerUsedLengthCm: decimal.NullDecimal{Decimal: decimal.RequireFromString(markerLenCm), Valid: true},
	}}
	if actualQty != "" {
		l.ActualQty = decimal.NullDecimal{Decimal: decimal.RequireFromString(actualQty), Valid: true}
		l.ActualUom = sql.NullString{String: actualUom, Valid: true}
	}
	return l
}

func calibIn(materialID int, uom string, lays ...entity.ProductionRunLayFact) MaterialCoefficientCalibrationInput {
	return MaterialCoefficientCalibrationInput{
		MaterialId: materialID, ArticleUom: uom, Lays: lays,
		Cards: map[int]*entity.TechCard{7: calibCard()},
	}
}

// TestCalibrationKeepsColourwayPinnedArticles — ЦЕНТРАЛЬНЫЙ ТЕСТ ЭТОЙ СВЯЗКИ.
//
// Настил не хранит material_id: он хранит пару (колорвей, слот BOM). Оба настила ниже стоят на ОДНОМ
// слоте, чей артикул по умолчанию — 42, но колорвей 100 приколол на этот слот артикул 77. Отбор,
// который «просто возьмёт material_id слота» (джойн по tech_card_bom_item.material_id), отдал бы обе
// строки артикулу 42 и НИ ОДНОЙ артикулу 77 — молча, и с виду правдоподобно.
//
// Поэтому здесь проверяется не «работает ли», а ИМЕННО РАЗВЕДЕНИЕ: калибровка 77 обязана взять
// только приколотые настилы, калибровка 42 — только неприколотые.
func TestCalibrationKeepsColourwayPinnedArticles(t *testing.T) {
	// Приколотые к 77: план 100 см каждый, факт 1.05 м = 105 см ⇒ дрейф +5%.
	pinned := []entity.ProductionRunLayFact{
		calibLay(1, 100, "100", 1, "1.05", "m"),
		calibLay(2, 100, "100", 1, "1.05", "m"),
		calibLay(3, 100, "100", 1, "1.05", "m"),
	}
	// Неприколотые (артикул слота 42): факт 1.10 м ⇒ дрейф +10%.
	fallback := []entity.ProductionRunLayFact{
		calibLay(4, 200, "100", 1, "1.10", "m"),
		calibLay(5, 200, "100", 1, "1.10", "m"),
		calibLay(6, 200, "100", 1, "1.10", "m"),
	}
	all := append(append([]entity.ProductionRunLayFact{}, pinned...), fallback...)

	t.Run("приколотый колорвеем артикул не потерян", func(t *testing.T) {
		got := BuildMaterialCoefficientSuggestion(calibIn(77, "m", all...))
		require.Equal(t, pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_READY,
			got.GetStatus(), got.GetDetail())
		require.EqualValues(t, 3, got.GetLayCount(),
			"под медианой обязаны стоять ТРИ приколотых настила: артикул 77 не назван ни на одном слоте")
		require.Equal(t, "1.05", got.GetSuggestedCoefficient().GetValue())
		require.Len(t, got.GetDrifts(), 3, "чужие настилы не «пропущены», их здесь нет вовсе")
		for _, d := range got.GetDrifts() {
			require.Contains(t, []int32{1, 2, 3}, d.GetLayId())
		}
	})

	t.Run("артикул слота по умолчанию берёт ТОЛЬКО неприколотые настилы", func(t *testing.T) {
		got := BuildMaterialCoefficientSuggestion(calibIn(42, "m", all...))
		require.EqualValues(t, 3, got.GetLayCount())
		require.Equal(t, "1.1", got.GetSuggestedCoefficient().GetValue(),
			"если бы приколотые настилы просочились в 42, медиана шести дрейфов дала бы 1.075")
		for _, d := range got.GetDrifts() {
			require.Contains(t, []int32{4, 5, 6}, d.GetLayId())
		}
	})

	t.Run("карточка не загружена — настил не разрешается, а не разрешается «как-нибудь»", func(t *testing.T) {
		in := calibIn(77, "m", pinned...)
		in.Cards = map[int]*entity.TechCard{}
		got := BuildMaterialCoefficientSuggestion(in)
		require.EqualValues(t, 0, got.GetLayCount())
		require.Empty(t, got.GetDrifts(),
			"без карточки артикул неизвестен; приписать настил артикулу из запроса значило бы угадать")
	})
}

// TestCalibrationTakesPlanFromTheOneConversion — план настила лежит в САНТИМЕТРАХ, факт вводится в
// метрах, и перевод между ними написан РОВНО ОДИН РАЗ: entity.ProductionRunLayDrift возвращает
// PlannedInFactUnit. Проекция обязана брать план оттуда, а не делить сырые числа.
//
// Проверяется тем, что второе определение здесь дало бы АБСУРД, а не соседнее число: 47.25 / 4500
// это −98.95%, а не +5%. Дрейф, посчитанный без перевода, невозможно принять за правдоподобный —
// в этом и смысл выбранных величин.
func TestCalibrationTakesPlanFromTheOneConversion(t *testing.T) {
	// План: 900 см × 5 слоёв = 4500 см = 45 м. Факт: 47.25 м ⇒ +5%.
	lays := []entity.ProductionRunLayFact{
		calibLay(1, 100, "900", 5, "47.25", "m"),
		calibLay(2, 100, "900", 5, "47.25", "m"),
		calibLay(3, 100, "900", 5, "47.25", "m"),
	}
	got := BuildMaterialCoefficientSuggestion(calibIn(77, "m", lays...))

	require.Equal(t, pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_READY,
		got.GetStatus(), got.GetDetail())
	require.Equal(t, "5", got.GetMedianDriftPercent().GetValue())
	require.Equal(t, "1.05", got.GetSuggestedCoefficient().GetValue())

	d := got.GetDrifts()[0]
	require.Equal(t, "45", d.GetPlannedQty().GetValue(),
		"план едет НА ПРОВОД уже в единице факта — иначе клиент делал бы перевод третий раз")
	require.Equal(t, "47.25", d.GetActualQty().GetValue())
	require.Equal(t, "5", d.GetDriftPercent().GetValue())
	require.Equal(t, pb_common.MaterialUnit_MATERIAL_UNIT_M, d.GetActualUom())
	require.Empty(t, d.GetSkipped())
	require.EqualValues(t, 900+1, d.GetRunId())
	require.Equal(t, "худи", d.GetTechCardName())
}

// TestCalibrationCarriesBothNumbersInTheirOwnUnits — MedianDrift и Suggested суть РАЗНЫЕ ЕДИНИЦЫ, и
// оба обязаны доехать. 0270 хранит коэффициент МНОЖИТЕЛЕМ, потому что путь потребности на него
// умножает; отдай сервер одно измерение (0.03), забытый «+1» дал бы артикулу коэффициент 0.03,
// который EffectiveCuttingCoefficient читает как «НЕ ЗАДАНО».
func TestCalibrationCarriesBothNumbersInTheirOwnUnits(t *testing.T) {
	lays := []entity.ProductionRunLayFact{
		calibLay(1, 100, "100", 1, "1.03", "m"),
		calibLay(2, 100, "100", 1, "1.03", "m"),
		calibLay(3, 100, "100", 1, "1.03", "m"),
	}
	got := BuildMaterialCoefficientSuggestion(calibIn(77, "m", lays...))
	require.Equal(t, "3", got.GetMedianDriftPercent().GetValue(), "ИЗМЕРЕНИЕ — в процентах")
	require.Equal(t, "1.03", got.GetSuggestedCoefficient().GetValue(), "ПРЕДЛОЖЕНИЕ — множитель")
	require.NotEqual(t, got.GetMedianDriftPercent().GetValue(), got.GetSuggestedCoefficient().GetValue())
}

// TestCalibrationOutOfRangeShowsTheMeasurementAndWithholdsTheNumber — §4.2: за границами поля
// измерение ОТДАЁТСЯ, предложение — НЕТ, и НЕ прижимается к границе. Зажать −12% в 1.0 значило бы
// сказать владельцу «поставьте единицу» там, где измерение говорит «ваш план систематически завышен».
func TestCalibrationOutOfRangeShowsTheMeasurementAndWithholdsTheNumber(t *testing.T) {
	// Факт вдвое меньше плана ⇒ дрейф −50% ⇒ множитель 0.5, вне [1, 3].
	lays := []entity.ProductionRunLayFact{
		calibLay(1, 100, "100", 1, "0.50", "m"),
		calibLay(2, 100, "100", 1, "0.50", "m"),
		calibLay(3, 100, "100", 1, "0.50", "m"),
	}
	got := BuildMaterialCoefficientSuggestion(calibIn(77, "m", lays...))

	require.Equal(t, pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_OUT_OF_RANGE,
		got.GetStatus())
	require.Equal(t, "-50", got.GetMedianDriftPercent().GetValue(), "измерение остаётся фактом")
	require.Nil(t, got.GetSuggestedCoefficient(), "предложения нет — и это не 1.0")
	require.NotEmpty(t, got.GetDetail())
	require.EqualValues(t, 3, got.GetLayCount())
}

// TestCalibrationNamesEveryLayItDidNotCount — медиана ПРЯЧЕТ выброс, а выброс это, скорее всего,
// опечатка. Невошедшие настилы едут наружу С ПРИЧИНОЙ, и причина непуста ровно у них.
func TestCalibrationNamesEveryLayItDidNotCount(t *testing.T) {
	weighed := calibLay(4, 100, "100", 1, "12", "kg") // взвешивание: кг с планом в см не связать
	noFact := calibLay(5, 100, "100", 1, "", "")      // настил спланирован, ткань ещё не мерили
	lays := []entity.ProductionRunLayFact{
		calibLay(1, 100, "100", 1, "1.05", "m"),
		calibLay(2, 100, "100", 1, "1.05", "m"),
		calibLay(3, 100, "100", 1, "1.05", "m"),
		weighed, noFact,
	}
	got := BuildMaterialCoefficientSuggestion(calibIn(77, "m", lays...))

	require.EqualValues(t, 3, got.GetLayCount(), "непосчитанные настилы медиану не двигают")
	require.Equal(t, "1.05", got.GetSuggestedCoefficient().GetValue())
	require.Len(t, got.GetDrifts(), 5, "разбор везёт ВСЕ настилы, вошедшие и нет")

	for _, d := range got.GetDrifts() {
		counted := d.GetLayId() <= 3
		if counted {
			require.NotNil(t, d.GetDriftPercent(), "у вошедшего есть число")
			require.Empty(t, d.GetSkipped(), "у вошедшего причины нет")
			continue
		}
		require.Nil(t, d.GetDriftPercent(), "у невошедшего числа нет — «нечего сравнить» ≠ «сошлось»")
		require.NotEmpty(t, d.GetSkipped(), "строка без числа и без объяснения неотличима от бага")
	}
}

// TestCalibrationTooFewFactsSaysSoAndOffersNothing — §4.2: меньше трёх настилов ⇒ предложения нет
// вовсе, а число настилов ЕДЕТ, потому что оно и есть содержание ответа.
func TestCalibrationTooFewFactsSaysSoAndOffersNothing(t *testing.T) {
	got := BuildMaterialCoefficientSuggestion(calibIn(77, "m",
		calibLay(1, 100, "100", 1, "1.05", "m"),
		calibLay(2, 100, "100", 1, "1.05", "m")))

	require.Equal(t, pb_common.MaterialCoefficientSuggestionStatus_MATERIAL_COEFFICIENT_SUGGESTION_STATUS_TOO_FEW_FACTS,
		got.GetStatus())
	require.Nil(t, got.GetSuggestedCoefficient())
	require.Nil(t, got.GetMedianDriftPercent(), "медианы нет, когда её не из чего считать")
	require.EqualValues(t, 2, got.GetLayCount(), "«фактов пока 2 из 3» — это ответ, а не его отсутствие")
	require.NotEmpty(t, got.GetDetail())
}

// TestCalibrationUnitVocabularyIsUsedNotStringEquality — единица ФАКТА канонизируется на записи
// («m»), а material.unit остаётся свободным текстом, в котором законно лежит «м». Сырое сравнение
// строк объявило бы их разными единицами и выбросило бы КАЖДЫЙ настил такого артикула — на экране
// это «фактов пока мало» при полном журнале замеров.
func TestCalibrationUnitVocabularyIsUsedNotStringEquality(t *testing.T) {
	lays := []entity.ProductionRunLayFact{
		calibLay(1, 100, "100", 1, "1.05", "m"),
		calibLay(2, 100, "100", 1, "1.05", "m"),
		calibLay(3, 100, "100", 1, "1.05", "m"),
	}
	got := BuildMaterialCoefficientSuggestion(calibIn(77, "м", lays...)) // кириллическая «м» на артикуле
	require.EqualValues(t, 3, got.GetLayCount(), "«м» и \"m\" — одна единица (перечень Ф5а.3)")
	require.Equal(t, "1.05", got.GetSuggestedCoefficient().GetValue())
}

// TestLayPlannedGeometryIsTheOnlyDefinition — план настила складывается РОВНО в одном месте, и
// потребность прогона читает то же число. Секция без измеренной раскладки не считается, но её слои
// всё равно теряют концы: ткань физически стелется и физически обрезается.
func TestLayPlannedGeometryIsTheOnlyDefinition(t *testing.T) {
	l := entity.ProductionRunLay{EndLossCm: decimal.RequireFromString("2")}
	l.Sections = []entity.ProductionRunLaySection{
		{Plies: 5, MarkerUsedLengthCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("900"), Valid: true}},
		{Plies: 3}, // раскладка без длины
	}
	g := LayPlannedGeometryOf(&l)
	require.Equal(t, "4500", g.ClothCm.String())
	require.Equal(t, []int{1}, g.UnmeasuredSections, "неизмеренная секция НАЗЫВАЕТСЯ, а не исчезает")
	require.Equal(t, "32", g.EndLossCm.String(), "2 × 2 см × 8 слоёв — включая слои неизмеренной секции")
	require.Equal(t, "4532", g.TotalCm().String())

	require.Equal(t, "0", LayPlannedGeometryOf(nil).TotalCm().String())
}
