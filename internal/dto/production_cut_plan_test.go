package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func cutMid(v int64) sql.NullInt64  { return sql.NullInt64{Int64: v, Valid: true} }
func cutPid(v int32) sql.NullInt32  { return sql.NullInt32{Int32: v, Valid: true} }
func cutNs(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

// cutPlanSingleFabricCard — простейшая живая карточка: один рулонный слот, одна деталь, один
// колорвей. Рецепт НЕ называет деталь (usage.piece_id пуст) — то есть ровно то состояние, в котором
// живые карточки и находятся.
//
// SizeQuantities заданы ЗАВЕДОМО ДРУГИМИ числами (999/777): типовой тираж стиля не должен попасть в
// наряд ни одним путём, и тест обязан это увидеть, а не поверить.
func cutPlanSingleFabricCard() *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{1, 2}
	card.SizeQuantities = []entity.TechCardSizeQuantity{{SizeId: 1, OrderQty: 999}, {SizeId: 2, OrderQty: 777}}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 501, LineKey: "BOM-MAIN", Name: "основная ткань", Section: entity.BomSectionFabric, MaterialId: cutMid(100)},
		{Id: 502, LineKey: "BOM-THREAD", Name: "нить основная", Section: entity.BomSectionThread, MaterialId: cutMid(300)},
	}
	card.Pieces = []entity.TechCardPiece{{
		Id: 11, LineKey: "P-FRONT", Name: "полочка",
		// Зеркальная пара: 0266 свернул удвоение в САМО количество, поэтому 2 — это уже обе полочки.
		PiecesPerGarment: 2,
		CutSymmetry:      cutNs(string(entity.PieceCutSymmetryMirrored)),
		Grainline:        "lengthwise",
	}}
	card.Colorways = []entity.TechCardColorway{{
		Id: 55, Name: "black", ProductId: cutPid(55),
		Usages: []entity.TechCardColorwayUsage{
			{BomItemId: cutMid(501), Consumption: nd2("2")},
			{BomItemId: cutMid(502), Consumption: nd2("180")},
		},
	}}
	return card
}

func cutPlanRun(lines ...entity.ProductionRunLine) *entity.ProductionRun {
	return &entity.ProductionRun{Id: 9, LockVersion: 4, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Lines: lines,
	}}
}

// ГЛАВНЫЙ ИНВАРИАНТ: pieces_to_cut = pieces_per_garment × planned_qty. Зеркальность НИЧЕГО не
// умножает, размеры печатаются в порядке ГРАДАЦИИ КАРТЫ (линии сознательно поданы задом наперёд), а
// типовой тираж стиля не виден в ответе ни одним числом.
func TestComputeProductionRunCutPlan_QuantitiesComeFromTheRunOnly(t *testing.T) {
	card := cutPlanSingleFabricCard()
	run := cutPlanRun(
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 2, PlannedQty: 5},
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10},
	)

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Len(t, resp.Rows, 1, "одна деталь × один колорвей")
	row := resp.Rows[0]
	require.Equal(t, int32(11), row.PieceId)
	require.Equal(t, "P-FRONT", row.PieceLineKey)
	require.Equal(t, int32(2), row.PiecesPerGarment)
	require.Equal(t, pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED, row.CutSymmetry,
		"зеркальность едет словами")

	require.Len(t, row.BySize, 2)
	require.Equal(t, int32(1), row.BySize[0].SizeId, "порядок градации карты, а не порядок линий прогона")
	require.Equal(t, int32(10), row.BySize[0].Garments)
	require.Equal(t, int32(20), row.BySize[0].PiecesToCut, "2 × 10, и НЕ 40 — зеркальность не множитель")
	require.Equal(t, int32(2), row.BySize[1].SizeId)
	require.Equal(t, int32(5), row.BySize[1].Garments)
	require.Equal(t, int32(10), row.BySize[1].PiecesToCut)

	require.Equal(t, int32(15), row.GarmentsTotal)
	require.Equal(t, int32(30), row.PiecesToCutTotal)
	require.Equal(t, int32(15), resp.GarmentsTotal)
	require.Equal(t, int32(30), resp.PiecesToCutTotal)
	require.Equal(t, int32(4), resp.RunLockVersion)
	require.NotNil(t, resp.GeneratedAt)
	require.Equal(t, int32(0), resp.ReleaseId, "прогон без релиза считается по живой карточке")
	require.Equal(t, int32(0), resp.ReleaseNumber)

	// Типовой тираж стиля (999/777) не должен просочиться НИ В ОДНО число ответа.
	for _, bs := range row.BySize {
		require.NotEqual(t, int32(999), bs.Garments)
		require.NotEqual(t, int32(777), bs.Garments)
	}
	require.NotEqual(t, int32(1776), resp.GarmentsTotal, "Σ типового тиража не могла попасть в шапку")
}

// Прогон без линий даёт ПУСТОЙ наряд, а не наряд по типовому тиражу карточки: партия из нуля
// изделий — это ноль раскроенных панелей.
func TestComputeProductionRunCutPlan_EmptyRunPrintsNothing(t *testing.T) {
	resp := ComputeProductionRunCutPlan(cutPlanRun(), cutPlanSingleFabricCard(), nil)
	require.Empty(t, resp.Rows)
	require.Empty(t, resp.Blockers)
	require.Equal(t, int32(0), resp.GarmentsTotal)
	require.Equal(t, int32(0), resp.PiecesToCutTotal)
}

// Вывод слота: рецепт не называет деталь, но рулонный слот у колорвея ровно один — строка
// печатается и ЧЕСТНО помечается slot_inferred. Нить в рецепте на это не влияет: её не кроят.
func TestComputeProductionRunCutPlan_SingleRollSlotIsInferredAndMarked(t *testing.T) {
	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		cutPlanSingleFabricCard(), nil)

	require.Len(t, resp.Rows, 1)
	require.Empty(t, resp.Blockers)
	row := resp.Rows[0]
	require.True(t, row.SlotInferred, "рецепт эту деталь не называет — слот выведен")
	require.Equal(t, int64(501), row.BomItemId)
	require.Equal(t, "основная ткань", row.SlotName)
	require.Equal(t, pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, row.Section)
	require.Equal(t, int32(100), row.MaterialId)
	require.False(t, row.Pinned)
}

// Пин рецепта побеждает умолчание слота, и наряд говорит, что это пин. Один и тот же слот у двух
// колорвеев — ровно тот случай, ради которого закройщик и смотрит в наряд.
func TestComputeProductionRunCutPlan_ColorwayPinBeatsSlotDefault(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.Colorways = []entity.TechCardColorway{
		{Id: 55, Name: "black", ProductId: cutPid(55), Usages: []entity.TechCardColorwayUsage{
			{BomItemId: cutMid(501), PieceId: cutMid(11), Consumption: nd2("2")}, // наследует умолчание слота
		}},
		{Id: 66, Name: "bone", ProductId: cutPid(66), Usages: []entity.TechCardColorwayUsage{
			{BomItemId: cutMid(501), PieceId: cutMid(11), MaterialId: cutMid(200), Consumption: nd2("2")}, // ПИН
		}},
	}
	run := cutPlanRun(
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10},
		entity.ProductionRunLine{ProductId: cutPid(66), SizeId: 1, PlannedQty: 4},
	)

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Len(t, resp.Rows, 2, "одна деталь × два колорвея")
	black, bone := resp.Rows[0], resp.Rows[1]
	require.Equal(t, int32(55), black.ColorwayId)
	require.Equal(t, "black", black.ColorwayName)
	require.Equal(t, int32(100), black.MaterialId)
	require.False(t, black.Pinned)
	require.False(t, black.SlotInferred, "рецепт назвал деталь — выводить было нечего")

	require.Equal(t, int32(66), bone.ColorwayId)
	require.Equal(t, int32(200), bone.MaterialId, "пин колорвея, а не умолчание слота")
	require.True(t, bone.Pinned)
	require.Equal(t, int32(4), bone.GarmentsTotal)
	require.Equal(t, int32(8), bone.PiecesToCutTotal)
}

// Неоднозначность уходит в БЛОКЕРЫ ЦЕЛИКОМ: два рулонных слота и рецепт, который не говорит, какой
// из них идёт на деталь, — это остановка, а не строка с угаданной тканью.
func TestComputeProductionRunCutPlan_AmbiguousSlotBlocksInsteadOfGuessing(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 503, LineKey: "BOM-LINING", Name: "подкладка", Section: entity.BomSectionLining, MaterialId: cutMid(400),
	})
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: cutMid(503), Consumption: nd2("1")})
	run := cutPlanRun(
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10},
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 2, PlannedQty: 5},
	)

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Empty(t, resp.Rows, "угадывать между основной тканью и подкладкой наряд не имеет права")
	require.Len(t, resp.Blockers, 1)
	b := resp.Blockers[0]
	require.Equal(t, int32(11), b.PieceId)
	require.Equal(t, "полочка", b.PieceName)
	require.Equal(t, int32(55), b.ColorwayId)
	require.Equal(t, "black", b.ColorwayName)
	require.Equal(t, int32(15), b.Garments, "изделия, чей крой не попал в наряд")
	require.NotEmpty(t, b.Reason)
	require.Equal(t, int32(0), resp.PiecesToCutTotal, "заблокированная деталь не даёт панелей")
	require.Equal(t, int32(15), resp.GarmentsTotal, "партия при этом остаётся партией")
}

// Слот без артикула — тоже блокер: «кроить из роли, у которой нет ткани» невыполнимо, а строка с
// пустым артикулом об этом молчала бы.
func TestComputeProductionRunCutPlan_SlotWithoutArticleBlocks(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.BomItems[0].MaterialId = sql.NullInt64{} // свободный текст: ни пина, ни умолчания
	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}), card, nil)

	require.Empty(t, resp.Rows)
	require.Len(t, resp.Blockers, 1)
	require.Contains(t, resp.Blockers[0].Reason, "основная ткань")
}

// AUX-прогон: линия несёт цвет вместо продукта и НЕ несёт размера. Строка печатается с size_id = 0
// и именем цвета, а слот выводится из BOM карточки — у вспомогательной карточки колорвеев нет
// вовсе, и рецепту взяться неоткуда.
func TestComputeProductionRunCutPlan_AuxLineHasNoSize(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 501, LineKey: "BOM-MAIN", Name: "хлопок 200", Section: entity.BomSectionFabric, MaterialId: cutMid(100)},
	}
	card.Pieces = []entity.TechCardPiece{{Id: 11, LineKey: "P-BODY", Name: "корпус пыльника", PiecesPerGarment: 1, Grainline: "lengthwise"}}
	card.OutputVariants = []entity.TechCardOutputVariant{{
		TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{Id: 9, ColorCode: "BLK", MaterialId: 700, Active: true},
		ColorName:                   "чёрный",
	}}
	run := cutPlanRun(entity.ProductionRunLine{OutputVariantId: cutPid(9), SizeId: 0, PlannedQty: 200})

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Len(t, resp.Rows, 1)
	require.Empty(t, resp.Blockers)
	row := resp.Rows[0]
	require.Equal(t, int32(0), row.ColorwayId, "у aux-линии продукта нет")
	require.Empty(t, row.ColorwayName)
	require.Equal(t, int32(9), row.OutputVariantId)
	require.Equal(t, "чёрный", row.OutputVariantName)
	require.Len(t, row.BySize, 1)
	require.Equal(t, int32(0), row.BySize[0].SizeId, "безразмерная линия")
	require.Empty(t, row.BySize[0].SizeName)
	require.Equal(t, int32(200), row.BySize[0].Garments)
	require.Equal(t, int32(200), row.BySize[0].PiecesToCut)
	require.True(t, row.SlotInferred)
	require.Equal(t, int32(100), row.MaterialId)
	require.Empty(t, resp.Caveats, "безразмерность aux-линии — норма, а не оговорка")
}

// Клеевая: ровно один слот дублерина — заполняем; несколько — оставляем пустым, но деталь ВСЁ РАВНО
// кроится (это не блокер).
func TestComputeProductionRunCutPlan_FusingOnlyWhenUnambiguous(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.Pieces[0].Fused = true
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 504, LineKey: "BOM-FUSE", Name: "дублерин 30г", Section: entity.BomSectionInterlining, MaterialId: cutMid(600),
	})
	// Рецепт называет деталь, иначе два рулонных слота (ткань + дублерин) сами по себе дали бы блокер.
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(501), PieceId: cutMid(11), Consumption: nd2("2")},
		{BomItemId: cutMid(504), Consumption: nd2("1")},
	}
	run := cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10})

	resp := ComputeProductionRunCutPlan(run, card, nil)
	require.Len(t, resp.Rows, 1)
	require.True(t, resp.Rows[0].Fused)
	require.Equal(t, int64(504), resp.Rows[0].FusingBomItemId)
	require.Equal(t, "дублерин 30г", resp.Rows[0].FusingMaterialName)

	// Второй дублерин — двусмысленность: печатать нечего, но кроить деталь всё равно надо.
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 505, LineKey: "BOM-FUSE2", Name: "дублерин 50г", Section: entity.BomSectionInterlining, MaterialId: cutMid(601),
	})
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: cutMid(505), Consumption: nd2("1")})

	resp = ComputeProductionRunCutPlan(run, card, nil)
	require.Len(t, resp.Rows, 1, "неоднозначная клеевая — не блокер")
	require.Equal(t, int64(0), resp.Rows[0].FusingBomItemId)
	require.Empty(t, resp.Rows[0].FusingMaterialName)
}

// Оговорки говорят только о том, что РЕАЛЬНО произошло, и называют количество: наряд, чья шапка не
// сходится со строками, обязан объяснить разницу сам.
func TestComputeProductionRunCutPlan_Caveats(t *testing.T) {
	card := cutPlanSingleFabricCard()
	run := cutPlanRun(
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10},
		entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 42, PlannedQty: 3}, // размер вне градации
		entity.ProductionRunLine{ProductId: cutPid(99), SizeId: 1, PlannedQty: 7},  // продукт не колорвей карточки
		entity.ProductionRunLine{SizeId: 1, PlannedQty: 2},                         // ни продукта, ни цвета
	)

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Len(t, resp.Caveats, 3)
	require.Contains(t, resp.Caveats[0], "2 изделий", "линия без продукта названа количеством")
	require.Contains(t, resp.Caveats[1], "99")
	require.Contains(t, resp.Caveats[2], "градаци")
	// Шапка называет размер ПАРТИИ (10+3+7+2), а не сумму привязанного: ужать её значило бы спрятать
	// девять изделий в правдоподобное число. Разницу объясняют оговорки выше.
	require.Equal(t, int32(22), resp.GarmentsTotal)
	require.Equal(t, int32(26), resp.Rows[0].PiecesToCutTotal, "в строке — только её 13 изделий × 2")
	require.Len(t, resp.Rows[0].BySize, 2, "размер вне градации всё равно посчитан")
	require.Equal(t, int32(42), resp.Rows[0].BySize[1].SizeId, "и печатается ПОСЛЕ градации карты")
}

// Старый снапшот релиза несёт строки BOM БЕЗ id (поле появилось на проводе позже), и рецепт в нём
// ссылается позиционно. Двусмысленность обязана остаться двусмысленностью: дедуп слотов по id
// схлопнул бы основную ткань с подкладкой в одну «единственную» и напечатал бы строку ровно там,
// где контракт требует блокер.
func TestComputeProductionRunCutPlan_IdlessBomLinesDoNotCollapse(t *testing.T) {
	idx := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{1}
	card.BomItems = []entity.TechCardBomItem{
		{Name: "основная ткань", Section: entity.BomSectionFabric, MaterialId: cutMid(100)},
		{Name: "подкладка", Section: entity.BomSectionLining, MaterialId: cutMid(400)},
	}
	card.Pieces = []entity.TechCardPiece{{LineKey: "P-FRONT", Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise"}}
	card.Colorways = []entity.TechCardColorway{{Id: 55, Name: "black", ProductId: cutPid(55), Usages: []entity.TechCardColorwayUsage{
		{BomItemIndex: idx(0), Consumption: nd2("2")},
		{BomItemIndex: idx(1), Consumption: nd2("1")},
	}}}
	run := cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10})

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Empty(t, resp.Rows, "два рулонных слота без id — по-прежнему два")
	require.Len(t, resp.Blockers, 1)
	require.Equal(t, int32(10), resp.Blockers[0].Garments)
}

// Единственный рулонный слот без id всё равно даёт строку: отсутствие id — это свойство снапшота,
// а не повод остановить цех.
func TestComputeProductionRunCutPlan_IdlessSingleSlotStillPrints(t *testing.T) {
	idx := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{1}
	card.BomItems = []entity.TechCardBomItem{
		{Name: "основная ткань", Section: entity.BomSectionFabric, MaterialId: cutMid(100)},
	}
	card.Pieces = []entity.TechCardPiece{{LineKey: "P-FRONT", Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise"}}
	card.Colorways = []entity.TechCardColorway{{Id: 55, Name: "black", ProductId: cutPid(55), Usages: []entity.TechCardColorwayUsage{
		{BomItemIndex: idx(0), Consumption: nd2("2")},
	}}}

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}), card, nil)

	require.Len(t, resp.Rows, 1)
	require.Equal(t, int64(0), resp.Rows[0].BomItemId, "id нет — и наряд его не выдумывает")
	require.Equal(t, int32(100), resp.Rows[0].MaterialId)
	require.Equal(t, int32(20), resp.Rows[0].PiecesToCutTotal)
}

// Наряд обязан назвать ревизию, по которой кроят: это поле ответа, а не вывод клиента из наличия
// release_id.
func TestComputeProductionRunCutPlan_NamesTheRelease(t *testing.T) {
	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		cutPlanSingleFabricCard(),
		&entity.TechCardReleaseMeta{Id: 31, ReleaseNumber: 4},
	)
	require.Equal(t, int32(31), resp.ReleaseId)
	require.Equal(t, int32(4), resp.ReleaseNumber)
}

// Карточка из релиза не несёт tech_card_piece.id (в контракте у детали есть только line_key),
// поэтому связь «рецепт → деталь» обязана держаться на ULID. Иначе наряд по ЗАМОРОЖЕННОЙ
// спецификации — то есть самый надёжный — состоял бы из одних блокеров.
func TestComputeProductionRunCutPlan_RecipeBindsPieceByLineKeyWhenIdIsAbsent(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 503, LineKey: "BOM-LINING", Name: "подкладка", Section: entity.BomSectionLining, MaterialId: cutMid(400),
	})
	card.Pieces[0].Id = 0 // как в снапшоте релиза
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(503), PieceLineKey: "P-FRONT", Consumption: nd2("1")},
		{BomItemId: cutMid(501), Consumption: nd2("2")},
	}
	run := cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10})

	resp := ComputeProductionRunCutPlan(run, card, nil)

	require.Len(t, resp.Rows, 1)
	require.Empty(t, resp.Blockers)
	require.Equal(t, int64(503), resp.Rows[0].BomItemId, "связь нашлась по line_key")
	require.False(t, resp.Rows[0].SlotInferred, "это не вывод — рецепт назвал деталь")
}
