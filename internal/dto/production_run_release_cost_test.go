package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
)

// releaseMixCard — та же карточка, что и colorwayMixCard (один слот ткани, чёрный по умолчанию
// слота 10 EUR/м, белый по пину 30 EUR/м, норма 1 м на размере 4 и 2 м на размере 6), но с
// колорвеями, чей id РАВЕН product_id. В проде это одно и то же (колорвей карточки И ЕСТЬ продукт,
// materials.go `c.id AS product_id`), а замороженные цены лежат под colorway_id — линия прогона
// приходит с product_id, и вся релизная ветка стоит на этой идентичности.
func releaseMixCard() *entity.TechCard {
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
		{Id: 55, Name: "Black", ProductId: bidx(55), Usages: []entity.TechCardColorwayUsage{
			{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, SizeConsumptions: graded},
		}},
		{Id: 66, Name: "White", ProductId: bidx(66), Usages: []entity.TechCardColorwayUsage{
			{BomItemId: sql.NullInt64{Int64: 1, Valid: true},
				MaterialId:       sql.NullInt64{Int64: 200, Valid: true}, // ПИН: другой артикул, другая цена
				SizeConsumptions: graded},
		}},
	}
	// Каталог на момент релиза: цена пина живёт ЗДЕСЬ, и в контракте TechCard этой карты нет.
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{
		200: {
			Material:    entity.Material{MaterialInsert: entity.MaterialInsert{Unit: nstr("m")}},
			LatestPrice: &entity.MaterialPrice{MaterialId: 200, Price: decimal.RequireFromString("30"), Currency: "EUR"},
		},
	}
	card.Costing = &entity.TechCardCosting{Currency: nstr("EUR")}
	return card
}

// releaseBlob замораживает карточку РОВНО ТАК ЖЕ, как это делает релиз (snapshotReleaseIfReleased):
// ConvertEntityTechCardToPb → protojson. Тест, который собрал бы pb руками, доказывал бы только то,
// что автор теста и автор кода одинаково представляют себе блоб.
func releaseBlob(t *testing.T, card *entity.TechCard) *pb_common.TechCard {
	t.Helper()
	raw, err := protojson.Marshal(ConvertEntityTechCardToPb(card, CostingFx{Base: "EUR"}))
	require.NoError(t, err)
	var snap pb_common.TechCard
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &snap))
	return &snap
}

// ГЛАВНОЕ ПРО СНАПШОТ: цена ПИНА переживает заморозку — но только как посчитанная цена колорвея.
// Сам артикул 200 в блобе есть (пин), а его цены нет нигде: она в каталоге материалов. Поэтому
// релизная ветка читает costing.colorway_costs, а не пересчитывает рецепт по замороженным входам —
// пересчёт оставил бы белый колорвей БЕЗ цены и обнулил бы всю партию.
func TestReleaseFrozenColorwayCostsCarryPinnedPrices(t *testing.T) {
	snap := releaseBlob(t, releaseMixCard())

	costs := ReleaseFrozenColorwayCosts(snap)
	require.NotNil(t, costs)
	require.Equal(t, "EUR", costs.Currency)
	require.Equal(t, "10", costs.UnitCostByColorway[55].String(), "чёрный: 1 м × 10 по умолчанию слота")
	require.Equal(t, "30", costs.UnitCostByColorway[66].String(), "белый: 1 м × 30 по ПИНУ, замороженный каталогом релиза")

	// И одновременно — почему пересчёт по блобу невозможен: цены артикула 200 в замороженных
	// входах нет, есть только сам пин.
	var pinned bool
	for _, cw := range snap.GetColorways() {
		for _, u := range cw.GetUsages() {
			if u.GetMaterialId() == 200 {
				pinned = true
			}
		}
	}
	require.True(t, pinned, "пин в блобе есть")
	for _, b := range snap.GetTechCard().GetBomItems() {
		require.NotEqual(t, int64(200), b.GetMaterialId(),
			"а цены пришпиленного артикула в блобе нет ни в одной строке BOM — это и есть причина читать colorway_costs")
	}
}

func relLine(productID, sizeID, qty int) entity.ProductionRunLine {
	ln := entity.ProductionRunLine{SizeId: sizeID, PlannedQty: qty}
	if productID > 0 {
		ln.ProductId = bidx(int32(productID))
	}
	return ln
}

// ГЛАВНОЕ ЧИСЛО РЕЛИЗНОЙ ВЕТКИ: партия из 50 чёрных по €10 и 50 белых по €30 стоит €20 за изделие.
// Скаляр релиза — цена ПЕРВОГО колорвея, то есть €10, и именно он снимался с такой партии.
func TestReleaseRunPlannedCostIsWeightedByColorway(t *testing.T) {
	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, releaseMixCard()))

	unit, ccy := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 4, 50), relLine(66, 4, 50),
	})
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "20", unit.Decimal.String(), "(10×50 + 30×50) ÷ 100")
	require.NotEqual(t, "10", unit.Decimal.String(), "РОВНО ЭТО и снималось раньше — скаляр релиза")

	// Вес — по изделиям, а не по колорвеям.
	unit, _ = ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 4, 90), relLine(66, 4, 10),
	})
	require.Equal(t, "12", unit.Decimal.String(), "(10×90 + 30×10) ÷ 100")

	// Несколько линий на один колорвей — это один колорвей: разбиение не переставляет веса.
	unit, _ = ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 4, 25), relLine(55, 6, 25), relLine(66, 4, 50),
	})
	require.Equal(t, "20", unit.Decimal.String())

	// Партия из одних чёрных стоит ровно столько, сколько стоила: пин соседа в неё не течёт.
	unit, _ = ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 4, 100)})
	require.Equal(t, "10", unit.Decimal.String())
}

// РАЗМЕРА В РЕЛИЗНОЙ ВЕТКЕ НЕТ, и это записано тестом, а не только комментарием: замороженная цена
// существует лишь на базовом размере карточки. Партия из одних размеров 6 стоит столько же, сколько
// такая же партия размера 4 — осознанный остаток (пересчитать норму на размер нечем: цены пина в
// блобе нет).
func TestReleaseRunPlannedCostIgnoresSizeByDesign(t *testing.T) {
	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, releaseMixCard()))

	base, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 4, 100)})
	graded, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 6, 100)})
	require.Equal(t, base.Decimal.String(), graded.Decimal.String(),
		"живая ветка дала бы здесь 20 (норма размера 6 вдвое больше); релизная остаётся на базовом размере")
}

// Колорвей без рецепта наследует цену СТИЛЯ — то же правило, что у ComputeColorwayUnitCost.
// Замороженная цифра такого колорвея — одни ручные статьи (пустой рецепт проекция костинга считает
// нулём материалов), и принять её за цену цвета значит уронить план на всю ткань.
func TestReleaseFrozenColorwayCostsInheritStyleForRecipelessColorway(t *testing.T) {
	card := releaseMixCard()
	card.Colorways[1].Usages = nil
	card.Costing.CmtCost = nd("5")

	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, card))
	require.NotNil(t, costs)
	require.Equal(t, "15", costs.UnitCostByColorway[55].String(), "чёрный: 1 м × 10 + CMT 5")
	require.Equal(t, "15", costs.UnitCostByColorway[66].String(),
		"белый без рецепта берёт цену стиля, а не свои €5 из одного CMT")

	// Наследовать можно только полную цену стиля: у корня с флагом неполноты брать нечего.
	broken := &pb_common.TechCard{TechCard: &pb_common.TechCardInsert{
		Costing: &pb_common.TechCardCosting{
			Currency: "EUR", UnitCost: &pb_decimal.Decimal{Value: "15"}, HasUnpriced: true,
			ColorwayCosts: []*pb_common.TechCardColorwayCost{{ColorwayId: 66, UnitCost: &pb_decimal.Decimal{Value: "5"}}},
		},
	}}
	require.Nil(t, ReleaseFrozenColorwayCosts(broken),
		"неполная цена стиля не наследуется — прогон остаётся на скаляре")
}

// Края, каждый из которых оставляет прогон на скаляре релиза.
func TestReleaseRunPlannedCostEdges(t *testing.T) {
	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, releaseMixCard()))

	for name, lines := range map[string][]entity.ProductionRunLine{
		"линий нет": nil,
		"количества неположительны": {relLine(55, 4, 0)},
		"линия без колорвея":        {relLine(0, 4, 50)},
		"колорвея нет в релизе":     {relLine(55, 4, 50), relLine(77, 4, 50)},
	} {
		t.Run(name, func(t *testing.T) {
			unit, ccy := ComputeReleaseRunPlannedUnitCost(costs, lines)
			require.False(t, unit.Valid, "непосчитанная партия возвращается к замороженному скаляру, а не к живой карточке")
			require.Empty(t, ccy)
		})
	}

	unit, _ := ComputeReleaseRunPlannedUnitCost(nil, []entity.ProductionRunLine{relLine(55, 4, 50)})
	require.False(t, unit.Valid, "снапшот без денег — тоже скаляр")
}

// Колорвей с флагом неполноты в замороженные цены не попадает: его число ЗАНИЖЕНО на целый
// материал, и плановая цена партии — не место, где такую цифру впервые принимают за правду.
func TestReleaseFrozenColorwayCostsSkipIncompleteFigures(t *testing.T) {
	// Рецепты у всех четырёх авторские: иначе сработало бы наследование цены стиля, и тест проверял
	// бы не то правило, которое назван проверять.
	snap := &pb_common.TechCard{
		TechCard: &pb_common.TechCardInsert{
			Costing: &pb_common.TechCardCosting{
				Currency: "EUR",
				ColorwayCosts: []*pb_common.TechCardColorwayCost{
					{ColorwayId: 55, UnitCost: &pb_decimal.Decimal{Value: "10"}},
					{ColorwayId: 66, UnitCost: &pb_decimal.Decimal{Value: "30"}, HasUnpriced: true},
					{ColorwayId: 77, UnitCost: &pb_decimal.Decimal{Value: "40"}, HasUnconvertedCurrencies: true},
					{ColorwayId: 88, UnitCost: &pb_decimal.Decimal{Value: "0"}},
				},
			},
		},
		Colorways: []*pb_common.AdminColorwayRef{
			{ColorwayId: 55, Usages: []*pb_common.TechCardColorwayUsage{{}}},
			{ColorwayId: 66, Usages: []*pb_common.TechCardColorwayUsage{{}}},
			{ColorwayId: 77, Usages: []*pb_common.TechCardColorwayUsage{{}}},
			{ColorwayId: 88, Usages: []*pb_common.TechCardColorwayUsage{{}}},
		},
	}

	costs := ReleaseFrozenColorwayCosts(snap)
	require.NotNil(t, costs)
	require.Len(t, costs.UnitCostByColorway, 1)
	require.Contains(t, costs.UnitCostByColorway, 55)

	unit, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 4, 50), relLine(66, 4, 50),
	})
	require.False(t, unit.Valid,
		"партия с колорвеем, чья замороженная цена неполна, целиком остаётся на скаляре — а не считается по половине цветов")

	// Блоб, где цены колорвеев есть, а списка колорвеев нет (легаси/битая форма): рецептов не видно
	// ни у кого, значит все наследуют цену стиля — и микс схлопывается в неё же, то есть ровно в
	// прежний скаляр. Тихого занижения на всю ткань здесь не случается.
	legacy := &pb_common.TechCard{TechCard: &pb_common.TechCardInsert{
		Costing: &pb_common.TechCardCosting{
			Currency: "EUR", UnitCost: &pb_decimal.Decimal{Value: "15"},
			ColorwayCosts: []*pb_common.TechCardColorwayCost{
				{ColorwayId: 55, UnitCost: &pb_decimal.Decimal{Value: "15"}},
				{ColorwayId: 66, UnitCost: &pb_decimal.Decimal{Value: "5"}},
			},
		},
	}}
	legacyCosts := ReleaseFrozenColorwayCosts(legacy)
	require.NotNil(t, legacyCosts)
	legacyUnit, _ := ComputeReleaseRunPlannedUnitCost(legacyCosts, []entity.ProductionRunLine{
		relLine(55, 4, 50), relLine(66, 4, 50),
	})
	require.Equal(t, "15", legacyUnit.Decimal.String(), "цена стиля на обоих цветах, а не (15+5)/2")

	// Ни костинга, ни валюты, ни одного пригодного колорвея — денег в снапшоте нет.
	require.Nil(t, ReleaseFrozenColorwayCosts(&pb_common.TechCard{}))
	require.Nil(t, ReleaseFrozenColorwayCosts(&pb_common.TechCard{TechCard: &pb_common.TechCardInsert{
		Costing: &pb_common.TechCardCosting{ColorwayCosts: []*pb_common.TechCardColorwayCost{
			{ColorwayId: 55, UnitCost: &pb_decimal.Decimal{Value: "10"}},
		}},
	}}), "без валюты число не деньги")
}
