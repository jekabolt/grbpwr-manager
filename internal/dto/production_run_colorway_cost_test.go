package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// colorwayMixCard: ОДИН слот ткани, ДВА колорвея с разной ценой на нём — чёрный по умолчанию слота
// (10 EUR/м), белый по пину (30 EUR/м из каталога). Норма градуирована: 1 м на размере 4 и 2 м на
// размере 6. Отсюда цена изделия:
//
//	чёрный: 10 на размере 4, 20 на размере 6
//	белый:  30 на размере 4, 60 на размере 6
//
// Ровно та карточка, ради которой пины и существуют: пока прогон считался «первым колорвеем», вся
// эта разница в снапшот не попадала.
func colorwayMixCard() *entity.TechCard {
	graded := []entity.TechCardBomSizeConsumption{
		{SizeId: 4, Consumption: decimal.RequireFromString("1")},
		{SizeId: 6, Consumption: decimal.RequireFromString("2")},
	}
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{4, 6}
	card.BaseSampleSizeId = sql.NullInt32{Int32: 4, Valid: true}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 1, Name: "Shell", Section: entity.BomSectionFabric,
		Unit: nstr("m"), MaterialId: sql.NullInt64{Int64: 100, Valid: true},
		UnitPrice: nd("10"), Currency: nstr("EUR"),
	}}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Black", ProductId: bidx(55), Usages: []entity.TechCardColorwayUsage{
			{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, SizeConsumptions: graded},
		}},
		{Id: 2, Name: "White", ProductId: bidx(66), Usages: []entity.TechCardColorwayUsage{
			{BomItemId: sql.NullInt64{Int64: 1, Valid: true},
				MaterialId:       sql.NullInt64{Int64: 200, Valid: true}, // ПИН: другой артикул, другая цена
				SizeConsumptions: graded},
		}},
	}
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{
		200: {
			Material:    entity.Material{MaterialInsert: entity.MaterialInsert{Unit: nstr("m")}},
			LatestPrice: &entity.MaterialPrice{MaterialId: 200, Price: decimal.RequireFromString("30"), Currency: "EUR"},
		},
	}
	card.Costing = &entity.TechCardCosting{Currency: nstr("EUR")}
	return card
}

func cwLine(productID, sizeID, qty int) entity.ProductionRunLine {
	ln := entity.ProductionRunLine{SizeId: sizeID, PlannedQty: qty}
	if productID > 0 {
		ln.ProductId = bidx(int32(productID))
	}
	return ln
}

// ГЛАВНОЕ ЧИСЛО: партия из 50 чёрных по €10 и 50 белых по €30 стоит €20 за изделие. Пока прогон
// считался по первому колорвею карточки, он снимал €10 — и вся plan/fact вариация партии ехала от
// этого на 100%.
func TestRunPlannedCostIsWeightedByColorway(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := colorwayMixCard()

	// Сначала — сами СТИЛЕВЫЕ цены колорвеев (среднее по ряду {4,6}: (1+2)/2 = 1.5 м), чтобы ниже
	// нечего было списать на арифметику. Клетки прогона считаются на СВОЁМ размере и этих цифр не
	// трогают.
	black, ccy := ComputeColorwayUnitCost(card, 55, fx)
	require.True(t, black.Valid)
	require.Equal(t, "15", black.Decimal.String(), "чёрный, среднее по ряду: 1.5 м × 10")
	white, _ := ComputeColorwayUnitCost(card, 66, fx)
	require.True(t, white.Valid)
	require.Equal(t, "45", white.Decimal.String(), "белый, среднее по ряду: 1.5 м × 30 (пин)")

	unit, gotCcy := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 50), cwLine(66, 4, 50)})
	require.True(t, unit.Valid)
	require.Equal(t, ccy, gotCcy)
	require.Equal(t, "20", unit.Decimal.String(), "(10×50 + 30×50) ÷ 100")
	require.NotEqual(t, "10", unit.Decimal.String(),
		"РОВНО ЭТО и снималось раньше — цена первого колорвея карточки вместо цены партии")

	// Вес — по изделиям, а не по колорвеям: 90 чёрных и 10 белых стоят 12, а не 20.
	unit, _ = ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 90), cwLine(66, 4, 10)})
	require.Equal(t, "12", unit.Decimal.String(), "(10×90 + 30×10) ÷ 100")

	// Цвет и размер взвешиваются ВМЕСТЕ, по одной клетке (колорвей, размер) на цену:
	// (10 + 20 + 30 + 60) × 10 ÷ 40 = 30.
	unit, _ = ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 10), cwLine(55, 6, 10), cwLine(66, 4, 10), cwLine(66, 6, 10)})
	require.Equal(t, "30", unit.Decimal.String())

	// Несколько линий на одну клетку — это одна клетка: грид законно держит их (разные line_key), и
	// разбиение не имеет права переставлять веса.
	split, _ := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 25), cwLine(55, 4, 25), cwLine(66, 4, 50)})
	require.Equal(t, "20", split.Decimal.String())

	// Пин соседнего колорвея не имеет права протечь в чужую цену: партия из одних чёрных стоит 10.
	only, _ := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 100)})
	require.Equal(t, "10", only.Decimal.String())
}

// Правило «размер без нормы → вся партия непосчитана» сохраняется и в разрезе колорвеев: непосчитанной
// становится КЛЕТКА, а не строка, и одна клетка обнуляет весь снапшот. Усреднение по тем клеткам,
// которые посчитались, оценило бы остальные изделия в ноль.
func TestRunPlannedCostUnpricedCellLeavesWholeBatchUnpriced(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := colorwayMixCard()
	card.SizeIds = append(card.SizeIds, 7) // размер есть в градации, нормы на него нет

	unit, ccy := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 90), cwLine(66, 7, 10)})
	require.False(t, unit.Valid, "неградуированная клетка оставляет непосчитанной всю партию")
	require.Empty(t, ccy)

	// Линия на продукт, которого среди колорвеев карточки нет: считать её по чужому цвету —
	// тот же дефект уровнем выше, поэтому непосчитана вся партия.
	unit, _ = ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 90), cwLine(99, 4, 10)})
	require.False(t, unit.Valid, "продукт без колорвея в карточке не имеет цены — и партия тоже")
}

// Линия без продукта на карточке С НЕСКОЛЬКИМИ колорвеями непосчитана осознанно: выбрать за неё
// цвет — значит выдумать цену. На карточке с единственным рецептом (aux-карта, легаси-выход)
// выбирать не из чего, и она считается как считалась.
func TestRunPlannedCostProductlessLineDependsOnHowManyRecipesExist(t *testing.T) {
	fx := CostingFx{Base: "EUR"}

	twoRecipes := colorwayMixCard()
	unit, ccy := ComputeProductionRunPlannedUnitCost(twoRecipes, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(0, 4, 200)})
	require.False(t, unit.Valid, "цвет линии неопределим, а колорвеи расходятся в цене — считать нечем")
	require.Empty(t, ccy)
	// И одна такая линия рядом с нормальными обнуляет партию целиком, а не тихо выпадает из среднего.
	unit, _ = ComputeProductionRunPlannedUnitCost(twoRecipes, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(55, 4, 100), cwLine(0, 4, 100)})
	require.False(t, unit.Valid)

	// Aux-карточка: один рецепт, безразмерная линия, норма на изделие (не по размерам) — считается,
	// и ровно в ту же цену, что и стилевая.
	aux := colorwayMixCard()
	aux.Colorways = aux.Colorways[:1]
	aux.Colorways[0].ProductId = sql.NullInt32{}
	aux.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: nd("2")},
	}
	style, styleCcy := ComputeTechCardUnitCost(aux, fx)
	require.True(t, style.Valid)
	unit, ccy = ComputeProductionRunPlannedUnitCost(aux, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{cwLine(0, 0, 200)})
	require.True(t, unit.Valid, "единственный рецепт карточки — не чужой цвет")
	require.Equal(t, style.Decimal.String(), unit.Decimal.String())
	require.Equal(t, styleCcy, ccy)
}

// Фактическая раскройная потеря прогона остаётся общей для всех клеток и применяется к цене КАЖДОГО
// колорвея, а не только первого.
func TestRunPlannedCostWastageOverrideAppliesPerColorway(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := colorwayMixCard()

	// 20% потери: чёрный 1×10×1.2 = 12, белый 1×30×1.2 = 36 → (12×50 + 36×50) ÷ 100 = 24.
	unit, _ := ComputeProductionRunPlannedUnitCost(card, fx, nd("20"),
		[]entity.ProductionRunLine{cwLine(55, 4, 50), cwLine(66, 4, 50)})
	require.True(t, unit.Valid)
	require.Equal(t, "24", unit.Decimal.String())
	require.False(t, card.BomItems[0].WastagePercent.Valid, "подмена потери не имеет права трогать карточку вызывающего")
}
