package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// Тесты этого файла держат ДВА числа наряда, каждое из которых уезжает на бумагу в цех: из чего
// кроят деталь и сколько изделий вообще получили инструкцию.

// Размерная этикетка, привязанная к детали, — НЕ артикул её кроя. Живая авторская форма: расход
// основной ткани задан на весь колорвей (без piece_id), а этикетка привязана к P-FRONT. Правило
// «рецепт называет деталь → его слот и есть ткань» без фильтра по секции печатало цеху «кроить
// полочку из размерной этикетки» — и делало это тихо, строкой обычного вида.
func TestComputeProductionRunCutPlan_PieceBoundLabelIsNotTheCutArticle(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 506, LineKey: "BOM-LABEL", Name: "этикетка размерная", Section: entity.BomSectionLabel, MaterialId: cutMid(900),
	})
	// Ткань — на весь колорвей; этикетка — на деталь. Нитка (502) уже есть в карточке и тоже не крой.
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(501), Consumption: nd2("2")},
		{BomItemId: cutMid(506), PieceId: cutMid(11), Quantity: nd2("1")},
	}

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}), card, nil)

	require.Empty(t, resp.Blockers, "нерулонная привязка — не повод останавливать цех")
	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	require.Equal(t, int64(501), row.BomItemId, "кроим из основной ткани")
	require.Equal(t, int32(100), row.MaterialId)
	require.NotEqual(t, int64(506), row.BomItemId, "артикул этикетки не может стать тканью детали")
	require.NotEqual(t, int32(900), row.MaterialId)
	require.True(t, row.SlotInferred,
		"про крой рецепт не сказал ничего — слот выведен как единственный рулонный, и наряд это признаёт")
	require.Equal(t, int32(20), row.PiecesToCutTotal, "2 × 10")
}

// Симметричная половина того же дефекта: рецепт честно называет на детали И ткань, И клеевую, а
// наряд отвечал блокером «для этой детали 2 разных слота» — остановкой цеха на спецификации, в
// которой всё сказано. Клеевая при этом не теряется: у неё свои два поля строки.
func TestComputeProductionRunCutPlan_PieceBoundFusingDoesNotBlockTheCut(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.Pieces[0].Fused = true
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 504, LineKey: "BOM-FUSE", Name: "дублерин 30г", Section: entity.BomSectionInterlining, MaterialId: cutMid(600),
	})
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(501), PieceId: cutMid(11), Consumption: nd2("2")},
		{BomItemId: cutMid(504), PieceId: cutMid(11), Consumption: nd2("1")}, // клеевая ТОЖЕ названа на детали
	}

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}), card, nil)

	require.Empty(t, resp.Blockers, "ткань + клеевая на одной детали — это полная спецификация, а не двусмысленность")
	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	require.Equal(t, int64(501), row.BomItemId, "основной артикул — ткань")
	require.False(t, row.SlotInferred, "рецепт назвал ткань явно — выводить было нечего")
	require.Equal(t, int64(504), row.FusingBomItemId, "клеевая не потеряна — она в своём поле")
	require.Equal(t, "дублерин 30г", row.FusingMaterialName)
	require.Equal(t, int32(20), row.PiecesToCutTotal)
}

// Сломанная ссылка рецепта остаётся блокером: фильтр по секции не имеет права проглотить «строка
// BOM, на которую ссылается рецепт, в карточке не найдена» — иначе наряд молча выведет слот там,
// где спецификация испорчена.
func TestComputeProductionRunCutPlan_BrokenRecipeReferenceStillBlocks(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(501), Consumption: nd2("2")},
		{BomItemId: cutMid(777), PieceId: cutMid(11), Consumption: nd2("1")}, // такой строки в BOM нет
	}

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}), card, nil)

	require.Empty(t, resp.Rows)
	require.Len(t, resp.Blockers, 1)
	require.Contains(t, resp.Blockers[0].Reason, "is not found in the card")
	require.Equal(t, int32(10), resp.Blockers[0].Garments)
}

// ЛЕГАСИ-AUX: линия без продукта и без aux-цвета (оба NULL — валидная форма единственного выхода
// aux-карточки, см. ProductionRunLine.OutputVariantId) обязана давать нормальные строки кроя.
// Раньше она давала оговорку: шапка наряда говорила «200 изделий», а инструкции ровно на эти 200
// штук в бумаге не было.
func TestComputeProductionRunCutPlan_LegacyAuxLineGetsItsOwnRows(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 501, LineKey: "BOM-MAIN", Name: "хлопок 200", Section: entity.BomSectionFabric, MaterialId: cutMid(100)},
		{Id: 502, LineKey: "BOM-THREAD", Name: "нить", Section: entity.BomSectionThread, MaterialId: cutMid(300)},
	}
	card.Pieces = []entity.TechCardPiece{{Id: 11, LineKey: "P-BODY", Name: "корпус чехла", PiecesPerGarment: 2, Grainline: "lengthwise"}}
	// Ни колорвеев, ни aux-цветов: карточка в легаси-режиме единственного выхода.
	run := cutPlanRun(entity.ProductionRunLine{SizeId: 0, PlannedQty: 200})

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Equal(t, int32(200), resp.GarmentsTotal)
	require.Empty(t, resp.Blockers)
	require.Empty(t, resp.Caveats, "оговорке здесь взяться неоткуда — линия разложена целиком")
	require.Len(t, resp.Rows, 1, "безымянный выход — такая же колонка наряда, как колорвей")
	row := resp.Rows[0]
	require.Equal(t, int32(0), row.ColorwayId)
	require.Equal(t, int32(0), row.OutputVariantId, "варианта нет — и наряд его не выдумывает")
	require.NotEmpty(t, row.OutputVariantName, "колонка обязана называться, иначе блокер по ней неисполним")
	require.Equal(t, int32(200), row.GarmentsTotal)
	require.Equal(t, int32(400), row.PiecesToCutTotal, "2 × 200 — инструкция на ВСЕ заявленные изделия")
	require.Len(t, row.BySize, 1)
	require.Equal(t, int32(0), row.BySize[0].SizeId, "безразмерная линия")
	require.Equal(t, int32(200), row.BySize[0].Garments)
	require.Equal(t, int32(100), row.MaterialId, "слот выведен из BOM карточки — рецептов у неё нет")
	require.True(t, row.SlotInferred)
	require.Equal(t, int32(400), resp.PiecesToCutTotal)
}

// Обратная граница того же правила: на ПРОДАВАЕМОЙ карточке линия без продукта — не легаси-выход, а
// дыра в плане, и колонки по голому BOM она не получает. Иначе наряд напечатал бы умолчание слота
// (100) вместо пришпиленного колорвеем артикула (200) — то есть выдал бы цеху не ту ткань, и выдал
// бы её строкой обычного вида.
func TestComputeProductionRunCutPlan_ProductlessLineOnSellableCardStaysACaveat(t *testing.T) {
	card := cutPlanSingleFabricCard()
	// Единственный колорвей карточки шпилит на слот другой артикул, чем умолчание слота.
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(501), MaterialId: cutMid(200), Consumption: nd2("2")},
	}
	run := cutPlanRun(
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10},
		entity.ProductionRunLine{SizeId: 1, PlannedQty: 2}, // ни продукта, ни цвета
	)

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Len(t, resp.Rows, 1, "колонка только у названного колорвея")
	require.Equal(t, int32(200), resp.Rows[0].MaterialId, "пин колорвея")
	require.True(t, resp.Rows[0].Pinned)
	require.Len(t, resp.Caveats, 1)
	require.Contains(t, resp.Caveats[0], "(2 units)",
		"изделия без цвета названы количеством, а не разложены по умолчанию слота")
	require.Equal(t, int32(12), resp.GarmentsTotal, "шапка при этом называет всю партию")
}

// У безымянного выхода блокер обязан оставаться исполнимым: имя колонки — единственное, чем он
// отличает свою партию от чужой.
func TestComputeProductionRunCutPlan_LegacyAuxBlockerIsNamed(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 501, Name: "хлопок 200", Section: entity.BomSectionFabric, MaterialId: cutMid(100)},
		{Id: 503, Name: "подкладка", Section: entity.BomSectionLining, MaterialId: cutMid(400)},
	}
	card.Pieces = []entity.TechCardPiece{{Id: 11, LineKey: "P-BODY", Name: "корпус чехла", PiecesPerGarment: 1}}

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{SizeId: 0, PlannedQty: 200}), card, nil)

	require.Empty(t, resp.Rows, "два рулонных слота — наряд не угадывает")
	require.Len(t, resp.Blockers, 1)
	require.Equal(t, int32(200), resp.Blockers[0].Garments)
	require.NotEmpty(t, resp.Blockers[0].ColorwayName, "безымянный блокер неисполним")
}
