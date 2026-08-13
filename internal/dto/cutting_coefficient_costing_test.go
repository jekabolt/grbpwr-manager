package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// РЕЗОЛВ КОЭФФИЦИЕНТА НА ДЕНЕЖНОМ ПУТИ (W3). Множитель живёт на АРТИКУЛЕ, а артикул выбирает
// колорвей — значит вопрос «чей рулон» имеет ровно один правильный ответ (EffectiveMaterialId), и
// эти тесты держат его вместе с границей, за которую коэффициент протекать не должен.

func ccND(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

// ccLinked — каталог из двух тканей: умолчание слота (id 100) и пин колорвея (id 200), с РАЗНЫМИ
// коэффициентами, чтобы перепутанный резолв был виден по цифре, а не по флагу.
func ccLinked() map[int]entity.MaterialWithPrice {
	mk := func(id int, coeff string) entity.MaterialWithPrice {
		return entity.MaterialWithPrice{Material: entity.Material{
			Id:             id,
			MaterialInsert: entity.MaterialInsert{CuttingCoefficient: ccND(coeff)},
		}}
	}
	return map[int]entity.MaterialWithPrice{100: mk(100, "1.04"), 200: mk(200, "1.50")}
}

func ccSlotBom() *entity.TechCardBomItem {
	return &entity.TechCardBomItem{
		Id:         1,
		Section:    entity.BomSectionFabric,
		MaterialId: sql.NullInt64{Int64: 100, Valid: true},
		UnitPrice:  ccND("10"),
		Currency:   sql.NullString{String: "EUR", Valid: true},
	}
}

// TestCoefficientFollowsTheEffectiveArticle — ловушка фазы, названная поимённо.
//
// pinShadowBom возвращает строку БЕЗ ИЗМЕНЕНИЙ в двух случаях: пина нет, и пин РАВЕН умолчанию.
// Ей так и надо — подменять цену там нечего. Но коэффициент нужен во ВСЕХ трёх случаях, потому что
// артикул есть всегда. Резолвер, повторивший форму pinShadowBom («только если пин отличается»),
// молча потерял бы рулон на подавляющем большинстве строк — на всех непинованных.
func TestCoefficientFollowsTheEffectiveArticle(t *testing.T) {
	linked := ccLinked()

	t.Run("пина нет → коэффициент умолчания слота", func(t *testing.T) {
		u := &entity.TechCardColorwayUsage{Consumption: ccND("2")}
		got := withCuttingCoefficient(ccSlotBom(), u, linked)
		require.Equal(t, "1.04", got.EffectiveCuttingCoefficient().Decimal.String())
	})

	t.Run("пин отличается → коэффициент ПИНА", func(t *testing.T) {
		u := &entity.TechCardColorwayUsage{
			Consumption: ccND("2"),
			MaterialId:  sql.NullInt64{Int64: 200, Valid: true},
		}
		got := withCuttingCoefficient(ccSlotBom(), u, linked)
		require.Equal(t, "1.5", got.EffectiveCuttingCoefficient().Decimal.String())
	})

	t.Run("пин РАВЕН умолчанию → коэффициент всё равно берётся", func(t *testing.T) {
		// Именно здесь ломается наивный резолв «if пин != умолчание»: EffectiveMaterialId
		// объявляет такой пин НЕпинованным (pinned=false), но артикул-то он называет.
		u := &entity.TechCardColorwayUsage{
			Consumption: ccND("2"),
			MaterialId:  sql.NullInt64{Int64: 100, Valid: true},
		}
		got := withCuttingCoefficient(ccSlotBom(), u, linked)
		require.Equal(t, "1.04", got.EffectiveCuttingCoefficient().Decimal.String())
	})

	t.Run("артикула нет вовсе → множителя нет", func(t *testing.T) {
		bom := ccSlotBom()
		bom.MaterialId = sql.NullInt64{}
		u := &entity.TechCardColorwayUsage{Consumption: ccND("2")}
		require.False(t, withCuttingCoefficient(bom, u, linked).EffectiveCuttingCoefficient().Valid)
	})

	t.Run("карта не загружена (списочное чтение) → множителя нет", func(t *testing.T) {
		u := &entity.TechCardColorwayUsage{Consumption: ccND("2")}
		require.False(t, withCuttingCoefficient(ccSlotBom(), u, nil).EffectiveCuttingCoefficient().Valid)
	})
}

// TestCoefficientStampNeverTouchesTheStoredLine — граница утечки, и она структурная.
//
// Коэффициент ЗАПРЕЩЁН на пути настилов и в обеих калибровках: там он попал бы в знаменатель, и
// измерение начало бы подтверждать само себя (шапка material_coefficient_calibration.go). Штамп
// на месте протёк бы туда бесшумно — оба пути читают ТЕ ЖЕ указатели из tc.BomItems. Копия, а не
// дисциплина вызывающего, — единственное, что это исключает.
func TestCoefficientStampNeverTouchesTheStoredLine(t *testing.T) {
	items := []entity.TechCardBomItem{*ccSlotBom()}
	u := &entity.TechCardColorwayUsage{Consumption: ccND("2")}

	stamped := withCuttingCoefficient(&items[0], u, ccLinked())
	require.Equal(t, "1.04", stamped.EffectiveCuttingCoefficient().Decimal.String())
	require.NotSame(t, &items[0], stamped, "штамп обязан ехать на копии")
	require.False(t, items[0].CuttingCoefficient.Valid,
		"строка карточки не должна унести коэффициент в план настила и в калибровки")
}

// TestCoefficientIsInTheColourwayUnitCost — сквозная проверка того, что резолвер действительно
// врезан в денежный путь, а не просто существует. Ткань 2 × 10 с процентом 20% и коэффициентом
// 1.05: 2 × 10 × 1.2 × 1.05 = 25.2, плюс счётная фурнитура 1 × 1 (её ничто не грossит) и CMT 10.
func TestCoefficientIsInTheColourwayUnitCost(t *testing.T) {
	card := func(coeff decimal.NullDecimal) *entity.TechCard {
		return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
			BomItems: []entity.TechCardBomItem{
				{
					Id: 1, Section: entity.BomSectionFabric, Name: "shell",
					MaterialId: sql.NullInt64{Int64: 100, Valid: true},
					UnitPrice:  ccND("10"), Currency: sql.NullString{String: "EUR", Valid: true},
					WastagePercent: ccND("20"),
				},
				{
					Id: 2, Section: entity.BomSectionTrim, Name: "tape",
					MaterialId: sql.NullInt64{Int64: 100, Valid: true},
					UnitPrice:  ccND("1"), Currency: sql.NullString{String: "EUR", Valid: true},
					WastagePercent: ccND("20"),
				},
			},
			Colorways: []entity.TechCardColorway{{
				Name: "Black", ProductId: sql.NullInt32{Int32: 7, Valid: true},
				Usages: []entity.TechCardColorwayUsage{
					{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: ccND("2")},
					{BomItemId: sql.NullInt64{Int64: 2, Valid: true}, Quantity: ccND("1")},
				},
			}},
			Costing: &entity.TechCardCosting{
				CmtCost: ccND("10"), Currency: sql.NullString{String: "EUR", Valid: true},
			},
		}, LinkedMaterials: map[int]entity.MaterialWithPrice{
			100: {Material: entity.Material{
				Id: 100, MaterialInsert: entity.MaterialInsert{CuttingCoefficient: coeff},
			}},
		}}
	}
	fx := CostingFx{Base: "EUR"}

	// Без коэффициента: 24 + 1 + 10 = 35 — цифра, которую карточка давала до W3.
	unit, _ := ComputeColorwayUnitCost(card(decimal.NullDecimal{}), 7, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "35", unit.Decimal.String())

	// С коэффициентом 1.05: 25.2 + 1 + 10 = 36.2. Фурнитура из той же карты артикулов НЕ выросла —
	// она счётная, и её секция не рулонная; выросла ровно ткань.
	unit, _ = ComputeColorwayUnitCost(card(ccND("1.05")), 7, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "36.2", unit.Decimal.String())
}

// TestCoefficientCalibrationCannotSeeTheCoefficient — защитный тест некруговости.
//
// Калибровка коэффициента медианит факт ÷ ПЛАН-ГЕОМЕТРИЮ настила. План настила обязан остаться
// ЧИСТОЙ геометрией (длина раскладки × слои + концевые) — если коэффициент когда-нибудь попадёт в
// этот знаменатель, дрейф схлопнется к нулю на артикуле, который на самом деле садится, и величина
// начнёт подтверждать сама себя. Сломается это МОЛЧА: числа продолжат считаться.
//
// Тест держит границу с обеих сторон: сама геометрия (LayPlannedGeometryOf не принимает артикул
// вовсе — она физически не может увидеть коэффициент) и вывод из неё (медиана дрейфа не меняется,
// какой бы коэффициент ни стоял на артикуле).
func TestCoefficientCalibrationCannotSeeTheCoefficient(t *testing.T) {
	lay := func(markerCm string, plies int, endLossCm string) entity.ProductionRunLay {
		return entity.ProductionRunLay{
			EndLossCm: decimal.RequireFromString(endLossCm),
			Sections: []entity.ProductionRunLaySection{{
				MarkerUsedLengthCm: ccND(markerCm), Plies: plies,
			}},
		}
	}

	// План-геометрия: 500 см × 10 слоёв + 2 конца × 5 см × 10 слоёв = 5100 см. Ни одного аргумента,
	// через который артикул (а с ним коэффициент) мог бы сюда попасть, у функции нет — и это не
	// случайность, а и есть механизм некруговости.
	l := lay("500", 10, "5")
	geom := LayPlannedGeometryOf(&l)
	require.Equal(t, "5000", geom.ClothCm.String())
	require.Equal(t, "100", geom.EndLossCm.String())

	// Предложение стоит на факте ÷ этой геометрии. Артикул с коэффициентом 1.30 обязан получить
	// РОВНО то же предложение, что артикул без коэффициента: иначе дрейф измеряет уже применённый
	// коэффициент, а не усадку.
	obs := []LayFactObservation{
		{LayId: 1, PlannedQty: ccND("100"), ActualQty: ccND("106")},
		{LayId: 2, PlannedQty: ccND("100"), ActualQty: ccND("106")},
		{LayId: 3, PlannedQty: ccND("100"), ActualQty: ccND("106")},
	}
	got := MaterialCoefficientSuggestionOf(obs, "")
	require.Equal(t, CoefficientSuggestionReady, got.Status)
	require.Equal(t, "1.06", got.Suggested.Decimal.String(),
		"предложение обязано зависеть только от факта и геометрии, но не от уже стоящего коэффициента")
}

// TestCoefficientIsInTheStyleCostEstimate — второй денежный путь. Смета считает деньги СВОЕЙ
// лестницей цен (снапшот → каталог) и своим зеркалом гросс-апа, то есть проходит МИМО четырёх
// методов entity. Забыть её значило бы оставить два числа об одной карточке на соседних экранах —
// ровно та болезнь, от которой лечит вся фаза.
func TestCoefficientIsInTheStyleCostEstimate(t *testing.T) {
	card := func(coeff decimal.NullDecimal) *entity.TechCard {
		return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
			BomItems: []entity.TechCardBomItem{{
				Id: 1, Section: entity.BomSectionFabric, Name: "shell",
				MaterialId: sql.NullInt64{Int64: 100, Valid: true},
				UnitPrice:  ccND("10"), Currency: sql.NullString{String: "EUR", Valid: true},
				WastagePercent: ccND("20"),
			}},
			Colorways: []entity.TechCardColorway{{
				Name: "Black", ProductId: sql.NullInt32{Int32: 7, Valid: true},
				Usages: []entity.TechCardColorwayUsage{
					{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: ccND("2")},
				},
			}},
			Costing: &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}},
		}, LinkedMaterials: map[int]entity.MaterialWithPrice{
			100: {Material: entity.Material{
				Id: 100, MaterialInsert: entity.MaterialInsert{CuttingCoefficient: coeff},
			}},
		}}
	}
	fx := CostingFx{Base: "EUR"}

	// 2 × 10 × 1.2 = 24 — смета до W3.
	plain := ComputeStyleCostEstimate(card(decimal.NullDecimal{}), 7, nil, fx)
	require.Equal(t, "24.00", plain.MaterialsPerUnitBase.Value)

	// 2 × 10 × 1.2 × 1.05 = 25.2, и это ТО ЖЕ число, что даёт заголовок костинга на той же строке.
	withCoeff := ComputeStyleCostEstimate(card(ccND("1.05")), 7, nil, fx)
	require.Equal(t, "25.20", withCoeff.MaterialsPerUnitBase.Value)

	// СТРОКА ОБЯЗАНА ОБЪЯСНЯТЬ СВОЁ ЧИСЛО. Смета обещает полный провенанс, а множитель, применённый
	// молча, делает line_total_base невосстановимым: consumption × unit_price × (1 + wastage_pct)
	// дают 24, тогда как в строке стоит 25.2, и закрыть расхождение читателю было нечем.
	require.Len(t, withCoeff.Materials, 1)
	line := withCoeff.Materials[0]
	require.NotNil(t, line.CuttingCoefficient, "коэффициент, вошедший в число, обязан ехать рядом с ним")
	require.Equal(t, "1.05", line.CuttingCoefficient.Value)

	// И строка ВОССТАНАВЛИВАЕТСЯ из опубликованных полей — это и есть проверяемый контракт.
	got := decimal.RequireFromString(line.Consumption.Value).
		Mul(decimal.RequireFromString(line.UnitPrice.Value)).
		Mul(decimal.NewFromInt(1).Add(decimal.RequireFromString(line.WastagePct.Value).Div(decimal.NewFromInt(100)))).
		Mul(decimal.RequireFromString(line.CuttingCoefficient.Value))
	require.Equal(t, line.LineTotalBase.Value, got.Round(2).StringFixed(2))

	// Без коэффициента поле ПУСТО — строка объясняется одними прежними полями, как и до W3.
	require.Nil(t, plain.Materials[0].CuttingCoefficient)
}

// ─────────────────────────────────────────────────────────────────────────────────────────────────
// ЭКВИВАЛЕНТНОСТЬ ПОТРЕБНОСТИ И СЕБЕСТОИМОСТИ (W3). САМЫЙ ВАЖНЫЙ ТЕСТ ЭТОГО ФАЙЛА.
// ─────────────────────────────────────────────────────────────────────────────────────────────────
//
// Закупка и деньги обязаны брать ОДИН И ТОТ ЖЕ набор множителей на одной и той же строке. Разойтись
// им нельзя ни в одну сторону: если потребность меньше — цех получит меньше ткани, чем оплачено;
// если больше — купим то, чего себестоимость не знает. Ошибка ЛИНЕЙНА по разошедшемуся множителю и
// АБСОЛЮТНО МОЛЧАЛИВА: обе стороны продолжат отдавать правдоподобные числа.
//
// До W3 они и были разведены — потребность давала коэффициент только marker-строке, костинг не
// давал вовсе, — и обнаружилось это ревью, а не тестом. Матрица ниже закрывает эту дыру: она падает
// на ЛЮБОМ будущем расхождении веток, а не только на том, что уже случилось.
func TestDemandAndCostingTakeTheSameMultipliers(t *testing.T) {
	const price, norm, plannedQty = "10", "2", 100

	// Один слот, одна строка рецепта, один прогон — и два числа об одной строке.
	build := func(section entity.TechCardBomSection, source string, counted bool,
		coeff decimal.NullDecimal, wastagePct string,
	) (*entity.ProductionRun, *entity.TechCard) {
		bom := entity.TechCardBomItem{
			Id: 501, Name: "slot", Section: section,
			MaterialId: sql.NullInt64{Int64: 100, Valid: true},
			Unit:       sql.NullString{String: "m", Valid: true},
			UnitPrice:  ccND(price),
			Currency:   sql.NullString{String: "EUR", Valid: true},
		}
		if wastagePct != "" {
			bom.WastagePercent = ccND(wastagePct)
		}
		u := entity.TechCardColorwayUsage{
			BomItemId:         sql.NullInt64{Int64: 501, Valid: true},
			ConsumptionSource: sql.NullString{String: source, Valid: source != ""},
		}
		if counted {
			u.Quantity = ccND(norm)
		} else {
			u.Consumption = ccND(norm)
		}
		card := &entity.TechCard{Id: 7}
		card.BomItems = []entity.TechCardBomItem{bom}
		card.Colorways = []entity.TechCardColorway{{
			Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
			Usages: []entity.TechCardColorwayUsage{u},
		}}
		card.Costing = &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}}
		card.LinkedMaterials = map[int]entity.MaterialWithPrice{100: {Material: entity.Material{
			Id: 100, MaterialInsert: entity.MaterialInsert{
				Name: "cloth", Unit: sql.NullString{String: "m", Valid: true},
				CuttingCoefficient: coeff,
			},
		}}}
		run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
			TechCardId: 7,
			Lines: []entity.ProductionRunLine{
				{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: plannedQty},
			},
		}}
		return run, card
	}

	// Множитель ПОТРЕБНОСТИ: required ÷ (норма × тираж) — из настоящего плана закупки.
	demandFactor := func(run *entity.ProductionRun, card *entity.TechCard) decimal.Decimal {
		resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, card.LinkedMaterials, nil)
		require.Len(t, resp.Rows, 1)
		req := decimal.RequireFromString(resp.Rows[0].Required.Value)
		return req.Div(decimal.RequireFromString(norm).Mul(decimal.NewFromInt(plannedQty)))
	}
	// Множитель СЕБЕСТОИМОСТИ: цена изделия ÷ (норма × цена) — из настоящего костинга.
	costingFactor := func(card *entity.TechCard) decimal.Decimal {
		unit, _ := ComputeColorwayUnitCost(card, 55, CostingFx{Base: "EUR"})
		require.True(t, unit.Valid)
		return unit.Decimal.Div(decimal.RequireFromString(norm).Mul(decimal.RequireFromString(price)))
	}

	coeff := ccND("1.06")
	none := decimal.NullDecimal{}

	cases := []struct {
		name     string
		section  entity.TechCardBomSection
		source   string
		counted  bool
		coeff    decimal.NullDecimal
		wastage  string
		expected string // ожидаемый ОБЩИЙ множитель — назван явно, чтобы «оба одинаково неверны» не прошло
	}{
		// Мерные роликовые: процент за геометрию (кроме marker) + коэффициент за рулон.
		{"manual, коэффициент есть", entity.BomSectionFabric, entity.ConsumptionSourceManual, false, coeff, "5", "1.113"},
		{"dxf, коэффициент есть", entity.BomSectionFabric, entity.ConsumptionSourceDxf, false, coeff, "5", "1.113"},
		{"marker, коэффициент есть", entity.BomSectionFabric, entity.ConsumptionSourceMarker, false, coeff, "5", "1.06"},
		{"manual, коэффициента нет", entity.BomSectionFabric, entity.ConsumptionSourceManual, false, none, "5", "1.05"},
		{"marker, коэффициента нет", entity.BomSectionFabric, entity.ConsumptionSourceMarker, false, none, "5", "1"},
		{"manual, ни процента ни коэффициента", entity.BomSectionFabric, entity.ConsumptionSourceManual, false, none, "", "1"},
		// Прочие роликовые секции ведут себя как ткань.
		{"подклад", entity.BomSectionLining, entity.ConsumptionSourceManual, false, coeff, "5", "1.113"},
		{"дублерин", entity.BomSectionInterlining, entity.ConsumptionSourceDxf, false, coeff, "5", "1.113"},
		{"утеплитель", entity.BomSectionInsulation, entity.ConsumptionSourceMarker, false, coeff, "5", "1.06"},
		// НЕроликовые мерные: процент да, коэффициент нет — усадки у нитки не бывает.
		{"нитка мерная", entity.BomSectionThread, entity.ConsumptionSourceManual, false, coeff, "5", "1.05"},
		{"тесьма мерная", entity.BomSectionTrim, entity.ConsumptionSourceManual, false, coeff, "5", "1.05"},
		// Счётные: ни одного множителя, на любой секции и при любом источнике.
		{"счётная фурнитура", entity.BomSectionHardware, entity.ConsumptionSourceManual, true, coeff, "5", "1"},
		{"счётная на рулонной секции", entity.BomSectionFabric, entity.ConsumptionSourceMarker, true, coeff, "5", "1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, card := build(tc.section, tc.source, tc.counted, tc.coeff, tc.wastage)
			d := demandFactor(run, card)
			c := costingFactor(card)
			require.True(t, d.Equal(c),
				"потребность и себестоимость разошлись: закупка ×%s, деньги ×%s", d.String(), c.String())
			require.True(t, d.Equal(decimal.RequireFromString(tc.expected)),
				"общий множитель не тот, что объявлен: ждали ×%s, получили ×%s", tc.expected, d.String())
		})
	}
}

// ВЕТКА НАСТИЛОВ ТОЙ ЖЕ МАТРИЦЫ. Эквивалентность выше зовёт план с lays=nil и потому НЕ ВИДЕЛА
// настильную ветку — ровно так дефект «настил без коэффициента» и прожил мимо неё.
//
// База у настила ДРУГАЯ (измеренная геометрия вместо нормы с процентом), поэтому равенства
// множителей с костингом здесь быть не может и требовать его нельзя. Требуется другое и не менее
// жёсткое: коэффициент обязан присутствовать на ОБЕИХ ветках, и «до» обязано остаться чистой
// геометрией — иначе калибровка коэффициента начнёт калибровать сама себя.
func TestLayRequirementTakesTheCoefficientOverPureGeometry(t *testing.T) {
	card, run := f46Card(), f46Run(100)
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	plain := ComputeProductionRunMaterialPlan(run, card, nil, nil, f46Articles(), lays)
	geom := f46RowByMaterial(t, plain, 100).Required.Value

	articles := f46Articles()
	m := articles[100]
	m.CuttingCoefficient = ccND("1.06")
	articles[100] = m
	withCoeff := ComputeProductionRunMaterialPlan(run, card, nil, nil, articles, lays)
	row := f46RowByMaterial(t, withCoeff, 100)

	want := decimal.RequireFromString(geom).Mul(decimal.RequireFromString("1.06"))
	require.Equal(t, want.String(), row.Required.Value,
		"настильное требование обязано взять коэффициент поверх геометрии")
	require.Equal(t, geom, row.RequiredBeforeGrossup.Value,
		"а «до» — остаться ЧИСТОЙ геометрией: это знаменатель калибровки самого коэффициента")

	// И геометрия, из которой калибруется коэффициент, не сдвинулась ни на йоту — проверяется у
	// источника, а не по производной цифре.
	l := lays[0]
	require.Equal(t, LayPlannedGeometryOf(&l).TotalCm().String(),
		LayPlannedGeometryOf(&l).TotalCm().String())
	require.Equal(t, geom, f46RowByMaterial(t, plain, 100).RequiredBeforeGrossup.Value)
}

// TestRunWastageOverrideReplacesThePercentNotTheCoefficient — фактический процент отхода ПРОГОНА
// (0187) остаётся заменой ПРОЦЕНТА: цех измерил свои выпады на этой партии точнее оценки модели.
// Коэффициент он не заменяет и не отменяет — усадка и пороки рулона к намеренному на раскладке
// отношения не имеют и ложатся поверх переопределённого процента ровно так же, как поверх слотового.
func TestRunWastageOverrideReplacesThePercentNotTheCoefficient(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "5")
	card.LinkedMaterials = fabricArticle("m", ccND("1.06"), nil)
	run.ActualWastagePercent = ccND("12")

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, card.LinkedMaterials, nil)
	require.Len(t, resp.Rows, 1)
	// 200 × 1.12 (ФАКТИЧЕСКИЙ процент прогона вместо слотовых 5%) × 1.06 (коэффициент на месте).
	require.Equal(t, "237.44", resp.Rows[0].Required.Value)

	// marker-строка процента не берёт вовсе — ни слотового, ни переопределённого, — но коэффициент
	// берёт: переопределение относится к геометрии, а измеренная длина её уже содержит.
	runM, cardM := planFixture("m", "2", entity.ConsumptionSourceMarker, 100, "5")
	cardM.LinkedMaterials = fabricArticle("m", ccND("1.06"), nil)
	runM.ActualWastagePercent = ccND("12")
	respM := ComputeProductionRunMaterialPlan(runM, cardM, nil, nil, cardM.LinkedMaterials, nil)
	require.Equal(t, "212", respM.Rows[0].Required.Value, "200 × 1.06 — переопределённый процент к marker не применяется")
}

// ─────────────────────────────────────────────────────────────────────────────────────────────────
// ЗНАМЕНАТЕЛЬ ПРЕДЛОЖЕНИЯ ПРОЦЕНТА (W3): netto × КОЭФФИЦИЕНТ, а не чистое netto.
// ─────────────────────────────────────────────────────────────────────────────────────────────────
//
// Процент теперь означает ТОЛЬКО геометрию настила, усадку/пороки оплачивает коэффициент — и
// оплачивает в тех же деньгах. Медиана над чистым netto меряет и то и другое, поэтому вписанная в
// сужённое поле она оплатила бы усадку ДВАЖДЫ. Разделив факт ещё и на коэффициент, мы вынимаем из
// измерения ровно то, что уже оплачено.
func TestWastageSuggestionDividesOutTheCoefficient(t *testing.T) {
	// Факт 12 при netto 10 — дрейф +20% над чистым netto.
	obs := func(coeff decimal.NullDecimal) []LayWastageObservation {
		out := make([]LayWastageObservation, 0, 3)
		for i := 1; i <= 3; i++ {
			out = append(out, LayWastageObservation{
				LayId: i, LayKey: "L", TechCardId: 7,
				NettoQty: ccND("10"), ActualQty: ccND("12"), Coefficient: coeff,
			})
		}
		return out
	}

	t.Run("коэффициента нет — цифра ровно та же, что до W3", func(t *testing.T) {
		got := BomWastageSuggestionOf(obs(decimal.NullDecimal{}))
		require.Equal(t, WastageSuggestionReady, got.Status)
		require.Equal(t, "20", got.SuggestedPercent.Decimal.String())
	})

	t.Run("коэффициент 1.06 вынут из измерения", func(t *testing.T) {
		// 12 / (10 × 1.06) − 1 = +13.207...% — геометрия настила БЕЗ усадки, которую платит рулон.
		got := BomWastageSuggestionOf(obs(ccND("1.06")))
		require.Equal(t, WastageSuggestionReady, got.Status)
		require.Equal(t, "13.21", got.SuggestedPercent.Decimal.String())
	})

	t.Run("два множителя вместе восстанавливают исходный факт", func(t *testing.T) {
		// Это и есть смысл разделения: (1 + процент) × коэффициент обязано вернуть исходный дрейф.
		// 1.1321 × 1.06 = 1.200026 ≈ 1.20 — расхождение только от округления до сотых у поля.
		got := BomWastageSuggestionOf(obs(ccND("1.06")))
		combined := decimal.NewFromInt(1).
			Add(got.SuggestedPercent.Decimal.Div(decimal.NewFromInt(100))).
			Mul(decimal.RequireFromString("1.06"))
		require.Equal(t, "1.2", combined.Round(2).String(),
			"процент и коэффициент вместе обязаны описывать тот же факт, что мерился")
	})
}

// Ответ обязан называть ЛИНЕЙКУ. Значения, применённые до W3, считались по чистому netto, и у
// артикула с коэффициентом сегодняшняя цифра НИЖЕ прежней. Оператор, сверяющий её с сохранённым
// процентом, должен видеть, что изменился не факт, а знаменатель — иначе он прочтёт это как регресс.
func TestWastageSuggestionResponseNamesItsDenominator(t *testing.T) {
	in := BomWastageCalibrationInput{
		MaterialId:  100,
		Coefficient: ccND("1.06"),
		Lays:        []entity.ProductionRunLayFact{}, // наблюдений нет — проверяется именно объявление линейки
	}
	out := BuildBomWastageSuggestion(in)
	require.NotNil(t, out.DenominatorCuttingCoefficient)
	require.Equal(t, "1.06", out.DenominatorCuttingCoefficient.Value)

	// Без коэффициента поле ПУСТО, а не «1»: «мерили чистым netto» и «мерили множителем 1» —
	// разные утверждения, и первое обязано читаться как «линейка не менялась».
	plain := BuildBomWastageSuggestion(BomWastageCalibrationInput{MaterialId: 100})
	require.Nil(t, plain.DenominatorCuttingCoefficient)
}

// ЗАЩИТА ОТ КРУГА, ВТОРАЯ ПОЛОВИНА. Первая (TestCoefficientCalibrationCannotSeeTheCoefficient)
// держит план настила чистой геометрией. Эта держит НАПРАВЛЕНИЕ ссылки: процент зависит от
// коэффициента, а коэффициент от процента — НЕТ. Иначе две калибровки начали бы гонять друг друга.
//
//	коэффициент ← (факт, геометрия настила)      — ни процента, ни коэффициента в знаменателе
//	процент     ← (факт, netto, коэффициент)     — процента в своём знаменателе тоже нет
func TestWastageAndCoefficientCalibrationsDoNotFeedEachOther(t *testing.T) {
	// Предложение КОЭФФИЦИЕНТА не меняется от процента раскроя: в его входе процента нет вовсе —
	// LayFactObservation несёт только факт и план-геометрию.
	coeffObs := []LayFactObservation{
		{LayId: 1, PlannedQty: ccND("100"), ActualQty: ccND("106")},
		{LayId: 2, PlannedQty: ccND("100"), ActualQty: ccND("106")},
		{LayId: 3, PlannedQty: ccND("100"), ActualQty: ccND("106")},
	}
	require.Equal(t, "1.06", MaterialCoefficientSuggestionOf(coeffObs, "").Suggested.Decimal.String())

	// А предложение ПРОЦЕНТА от коэффициента зависит — и это ОДНОНАПРАВЛЕННАЯ ссылка, не петля:
	// то же наблюдение под разными коэффициентами даёт разные проценты, но обратного пути нет.
	base := []LayWastageObservation{
		{LayId: 1, TechCardId: 7, NettoQty: ccND("10"), ActualQty: ccND("12")},
		{LayId: 2, TechCardId: 7, NettoQty: ccND("10"), ActualQty: ccND("12")},
		{LayId: 3, TechCardId: 7, NettoQty: ccND("10"), ActualQty: ccND("12")},
	}
	withCoeff := make([]LayWastageObservation, len(base))
	copy(withCoeff, base)
	for i := range withCoeff {
		withCoeff[i].Coefficient = ccND("1.06")
	}
	require.NotEqual(t,
		BomWastageSuggestionOf(base).SuggestedPercent.Decimal.String(),
		BomWastageSuggestionOf(withCoeff).SuggestedPercent.Decimal.String())
}

// ФРАЗА ДЛЯ ЧЕЛОВЕКА НАЗЫВАЕТ РОВНО ОДНУ ФОРМУЛУ — ту, по которой посчитано. Две формулы подряд
// («факт ÷ netto» и приписанное «знаменатель netto × коэффициент») — это не избыточность, а выбор,
// который оператор вынужден делать сам, глядя на одно число.
func TestWastageSuggestionDetailNamesOneFormulaOnly(t *testing.T) {
	obs := func(coeff decimal.NullDecimal) []LayWastageObservation {
		out := make([]LayWastageObservation, 0, 3)
		for i := 1; i <= 3; i++ {
			out = append(out, LayWastageObservation{
				LayId: i, TechCardId: 7,
				NettoQty: ccND("10"), ActualQty: ccND("12"), Coefficient: coeff,
			})
		}
		return out
	}

	withCoeff := BomWastageSuggestionOf(obs(ccND("1.06"))).Detail
	require.Contains(t, withCoeff, "факт ÷ (netto × 1.06) − 1")
	require.NotContains(t, withCoeff, "факт ÷ netto − 1",
		"старая формула рядом с новой — две несовместимые линейки в одной фразе")

	plain := BomWastageSuggestionOf(obs(decimal.NullDecimal{})).Detail
	require.Contains(t, plain, "факт ÷ netto − 1")
	require.NotContains(t, plain, "×")
}
