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
// пересчёт по замороженным входам (CostingCard) оставляет пришпиленную строку БЕЗ цены — клетка
// такого колорвея не ценится по размеру, и партия с ним целиком падает на costing.colorway_costs,
// единственное место, где цена пина заморожена.
func TestReleaseFrozenColorwayCostsCarryPinnedPrices(t *testing.T) {
	snap := releaseBlob(t, releaseMixCard())

	costs := ReleaseFrozenColorwayCosts(snap)
	require.NotNil(t, costs)
	require.Equal(t, "EUR", costs.Currency)
	require.Equal(t, "15", costs.UnitCostByColorway[55].String(), "чёрный: средняя норма (1+2)/2 × 10 по умолчанию слота")
	require.Equal(t, "45", costs.UnitCostByColorway[66].String(), "белый: 1.5 м × 30 по ПИНУ, замороженный каталогом релиза")

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

// ГЛАВНОЕ ЧИСЛО РЕЛИЗНОЙ ВЕТКИ: партия из 50 чёрных и 50 белых НЕ снимается по цене первого
// колорвея. Белый пришпилен, а цены пина в блобе нет — размерный проход по такой партии не
// считается, и она ЦЕЛИКОМ взвешивается замороженными стилевыми ценами колорвеев: €30, а не €15.
func TestReleaseRunPlannedCostIsWeightedByColorway(t *testing.T) {
	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, releaseMixCard()))

	unit, ccy := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 4, 50), relLine(66, 4, 50),
	})
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "30", unit.Decimal.String(), "(15×50 + 45×50) ÷ 100")
	require.NotEqual(t, "15", unit.Decimal.String(), "РОВНО ЭТО и снималось раньше — скаляр релиза")

	// Вес — по изделиям, а не по колорвеям.
	unit, _ = ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 4, 90), relLine(66, 4, 10),
	})
	require.Equal(t, "18", unit.Decimal.String(), "(15×90 + 45×10) ÷ 100")

	// Несколько линий на один колорвей — это один колорвей: разбиение не переставляет веса.
	unit, _ = ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 4, 25), relLine(55, 6, 25), relLine(66, 4, 50),
	})
	require.Equal(t, "30", unit.Decimal.String())

	// Партия из одних чёрных пинов не несёт, поэтому считается ПО РАЗМЕРУ из замороженной
	// карточки: норма размера 4 — 1 м × 10 = 10, а не 15 (среднее по ряду). Пин соседа в неё не
	// течёт ни ценой, ни своей неспособностью посчитаться по размеру.
	unit, _ = ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 4, 100)})
	require.Equal(t, "10", unit.Decimal.String())
}

// БЛОКЕР-2 РЕВЬЮ: размерная клетка релизного прогона равна цене того же размера в ЖИВОЙ ветке.
// Стилевая цена стала средним по ряду (T6), и партия обязана этого не заметить: релиз, снятый
// после выката, на прогоне из одних M даёт цену M — ту же цифру, что нерелизный прогон той же
// карточки, — а не среднее по ряду.
func TestReleaseRunSizeCellMatchesLiveBranch(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := releaseMixCard()
	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, card))
	require.NotNil(t, costs)
	require.NotNil(t, costs.CostingCard, "снапшот несёт костинг-проекцию для размерных клеток")

	for name, lines := range map[string][]entity.ProductionRunLine{
		"только размер 4":        {relLine(55, 4, 100)},
		"только размер 6":        {relLine(55, 6, 100)},
		"микс 90/10 по размерам": {relLine(55, 4, 90), relLine(55, 6, 10)},
	} {
		live, liveCcy := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{}, lines)
		frozen, frozenCcy := ComputeReleaseRunPlannedUnitCost(costs, lines)
		require.True(t, live.Valid, "%s: живая ветка ценит", name)
		require.True(t, frozen.Valid, "%s: релизная ветка ценит", name)
		require.Equal(t, live.Decimal.String(), frozen.Decimal.String(),
			"%s: релизная и живая ветка обязаны дать ОДНУ цифру на одном размере", name)
		require.Equal(t, liveCcy, frozenCcy, "%s", name)
	}

	// Конкретные числа, чтобы регресс не спрятался за «обе ветки съехали одинаково»: размер 4 →
	// 10 (норма 1×10), размер 6 → 20 (норма 2×10), и ни то ни другое не 15 (среднее по ряду).
	at4, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 4, 100)})
	require.Equal(t, "10", at4.Decimal.String())
	at6, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(55, 6, 100)})
	require.Equal(t, "20", at6.Decimal.String())
}

// ПРЕДЕЛ СНАПШОТА, записанный тестом: у пришпиленного колорвея цены пина в блобе нет, размерная
// клетка по нему не считается, и партия ЦЕЛИКОМ (всё-или-ничего, не по клеткам) падает на
// замороженную стилевую цену колорвея. Живая ветка дала бы 60 (2 м × 30 по пину); релизная даёт
// замороженные 45 — це́ны здесь заморожены сильнее, чем размерны, и это документированный выбор.
func TestReleaseRunPinnedCellFallsBackToFrozenColorwayMix(t *testing.T) {
	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, releaseMixCard()))

	unit, ccy := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{relLine(66, 6, 100)})
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "45", unit.Decimal.String(),
		"замороженная стилевая цена пина (1.5 м × 30), не размерная и не сегодняшний каталог")

	// Смешанная партия с пином тоже целиком на стилевых ценах: половина клеток по размеру, половина
	// по среднему — запрещённая смесь, её не должно быть даже там, где часть клеток посчиталась бы.
	mixed, _ := ComputeReleaseRunPlannedUnitCost(costs, []entity.ProductionRunLine{
		relLine(55, 6, 50), relLine(66, 6, 50),
	})
	require.Equal(t, "30", mixed.Decimal.String(),
		"(15+45)/2 по замороженным стилевым ценам; НЕ (20+45)/2 — чёрная клетка не смеет быть размерной, когда белая не может")
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
	require.Equal(t, "20", costs.UnitCostByColorway[55].String(), "чёрный: 1.5 м (средняя по ряду) × 10 + CMT 5")
	require.Equal(t, "20", costs.UnitCostByColorway[66].String(),
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

// T8: колорвей, у которого в снапшоте ОДНИ строки-назначения деталей, — для замороженной проекции
// колорвей БЕЗ рецепта: он наследует цену стиля, а не свою замороженную цифру без ткани. До правки
// len(usages) > 0 считал его «авторским», и релизная партия тихо занижалась на весь материал.
func TestT8ReleaseFrozenAllPieceRowsColorwayInheritsStyle(t *testing.T) {
	card := releaseMixCard()
	card.Costing.CmtCost = nd("5")
	card.Pieces = []entity.TechCardPiece{{Id: 1, LineKey: "PIECE1", Name: "перед", PiecesPerGarment: 1}}
	// Белый: только назначение детали (с легаси-числом — оно тоже не должно ничего менять).
	card.Colorways[1].Usages = []entity.TechCardColorwayUsage{pieceRow(1, 1, "10")}

	costs := ReleaseFrozenColorwayCosts(releaseBlob(t, card))
	require.NotNil(t, costs)
	require.Equal(t, "20", costs.UnitCostByColorway[55].String(), "чёрный: 1.5 м (средняя по ряду) × 10 + CMT 5")
	require.Equal(t, "20", costs.UnitCostByColorway[66].String(),
		"белый из одних строк деталей наследует цену стиля, а не свои €5 из одного CMT")
}

// T8: привязка к детали узнаётся во ВСЕХ трёх проводных формах — piece_id, только piece_line_key
// (у снапшота id детали нет вовсе), только легаси piece_index (включая индекс 0, где ноль —
// настоящая деталь, а не «не задано»). Строка любой из этих форм рецептом не считается.
func TestT8ReleaseFrozenPieceBindingSurvivesEveryWireForm(t *testing.T) {
	zero := int32(0)
	snap := &pb_common.TechCard{
		TechCard: &pb_common.TechCardInsert{
			Costing: &pb_common.TechCardCosting{
				Currency: "EUR", UnitCost: &pb_decimal.Decimal{Value: "15"},
				ColorwayCosts: []*pb_common.TechCardColorwayCost{
					{ColorwayId: 55, UnitCost: &pb_decimal.Decimal{Value: "15"}},
					{ColorwayId: 66, UnitCost: &pb_decimal.Decimal{Value: "5"}},
					{ColorwayId: 77, UnitCost: &pb_decimal.Decimal{Value: "5"}},
					{ColorwayId: 88, UnitCost: &pb_decimal.Decimal{Value: "5"}},
				},
			},
		},
		Colorways: []*pb_common.AdminColorwayRef{
			{ColorwayId: 55, Usages: []*pb_common.TechCardColorwayUsage{{}}}, // авторская строка изделия
			{ColorwayId: 66, Usages: []*pb_common.TechCardColorwayUsage{{PieceId: 1}}},
			{ColorwayId: 77, Usages: []*pb_common.TechCardColorwayUsage{{PieceLineKey: "PIECE1"}}},
			{ColorwayId: 88, Usages: []*pb_common.TechCardColorwayUsage{{PieceIndex: &zero}}},
		},
	}

	costs := ReleaseFrozenColorwayCosts(snap)
	require.NotNil(t, costs)
	require.Equal(t, "15", costs.UnitCostByColorway[55].String(), "авторский колорвей стоит своей цифрой")
	for _, id := range []int{66, 77, 88} {
		require.Equal(t, "15", costs.UnitCostByColorway[id].String(),
			"колорвей %d из одних строк деталей наследует цену стиля, а не свои €5 без ткани", id)
	}
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
