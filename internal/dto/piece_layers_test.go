package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// СЛОИ ДЕТАЛИ (T4): деталь законно ссылается на несколько тканей ОДНОГО колорвея, если это слои
// (решение владельца); наряд даёт СТРОКУ НА КАЖДЫЙ кроимый слой, блокером остаётся только конфликт
// ОСНОВНЫХ. Здесь же — покрытие настилов «за каждый слой» и три находки гейта.

func t4Purpose(p entity.TechCardBomPurpose) sql.NullString {
	return sql.NullString{String: string(p), Valid: true}
}

// t4LayeredCard: полочка = шелл (fabric, main) + подклад (lining) в одном колорвее.
func t4LayeredCard() *entity.TechCard {
	card := cutPlanSingleFabricCard()
	card.BomItems[0].Purpose = t4Purpose(entity.BomPurposeMain)
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 503, LineKey: "BOM-LINING", Name: "подкладка купра", Section: entity.BomSectionLining, MaterialId: cutMid(400),
	})
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(501), PieceId: cutMid(11), Consumption: nd2("2")},
		{BomItemId: cutMid(503), PieceId: cutMid(11), Consumption: nd2("1")},
	}
	return card
}

// Деталь с двумя слоями даёт ДВЕ строки наряда — по одной на слой, без блокеров. До T4 это был
// блокер «names 2 different slots», прямо противоречивший ответу владельца.
func TestCutPlanLayeredPieceGetsOneRowPerLayer(t *testing.T) {
	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		t4LayeredCard(), nil)

	require.Empty(t, resp.Blockers, "слои — не двусмысленность")
	require.Len(t, resp.Rows, 2, "по строке на каждый кроимый слой")
	require.Equal(t, int64(501), resp.Rows[0].BomItemId, "порядок рецепта: шелл первым")
	require.Equal(t, int64(503), resp.Rows[1].BomItemId)
	for _, row := range resp.Rows {
		require.Equal(t, int32(11), row.PieceId)
		require.False(t, row.SlotInferred, "рецепт назвал слой — выводить нечего")
		require.Equal(t, int32(20), row.PiecesToCutTotal, "спецификация детали повторяется в каждой строке слоя")
	}
	require.Equal(t, int32(40), resp.PiecesToCutTotal, "панели считаются по слоям: полочку кроят и из шелла, и из подклада")
}

// Две ОСНОВНЫЕ на одной цельной детали — блокер (П1), и строки детали не печатаются вовсе.
func TestCutPlanTwoMainsBlock(t *testing.T) {
	card := t4LayeredCard()
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 505, LineKey: "BOM-MAIN2", Name: "поплин", Section: entity.BomSectionFabric,
		Purpose: t4Purpose(entity.BomPurposeMain), MaterialId: cutMid(500),
	})
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: cutMid(505), PieceId: cutMid(11), Consumption: nd2("2")})

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		card, nil)

	require.Empty(t, resp.Rows, "конфликт основных неделим — печатать «хорошие» слои рядом с ним нельзя")
	require.Len(t, resp.Blockers, 1)
	require.Contains(t, resp.Blockers[0].Reason, "main fabrics")
	require.Contains(t, resp.Blockers[0].Reason, "основная ткань")
	require.Contains(t, resp.Blockers[0].Reason, "поплин")
}

// Основная рядом с неразложенной fabric-строкой — тот же блокер, другими словами: не доказать, что
// «не разложено» — это не вторая основная.
func TestCutPlanMainBesideUnsortedFabricBlocks(t *testing.T) {
	card := t4LayeredCard()
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 506, LineKey: "BOM-MYSTERY", Name: "ткань без назначения", Section: entity.BomSectionFabric, MaterialId: cutMid(510),
	})
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: cutMid(506), PieceId: cutMid(11), Consumption: nd2("1")})

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		card, nil)

	require.Empty(t, resp.Rows)
	require.Len(t, resp.Blockers, 1)
	require.Contains(t, resp.Blockers[0].Reason, "unsorted")
	require.Contains(t, resp.Blockers[0].Reason, "ткань без назначения")
}

// Одна детальная строка на fabric без назначения — НЕ блокер (половина живых строк беты такая):
// роль «не разложено» неоднозначна только рядом со вторым слоем.
func TestCutPlanSingleUnsortedLayerStillPrints(t *testing.T) {
	card := cutPlanSingleFabricCard() // 501 без purpose
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: cutMid(501), PieceId: cutMid(11), Consumption: nd2("2")},
	}
	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		card, nil)
	require.Empty(t, resp.Blockers)
	require.Len(t, resp.Rows, 1)
}

// Слой без артикула — блокер ЭТОГО слоя: шелл печатается, подклад останавливается и называется.
func TestCutPlanLayerWithoutArticleBlocksThatLayerOnly(t *testing.T) {
	card := t4LayeredCard()
	card.BomItems[2].MaterialId = sql.NullInt64{} // подкладка: ни пина, ни умолчания

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		card, nil)

	require.Len(t, resp.Rows, 1, "шелл кроится")
	require.Equal(t, int64(501), resp.Rows[0].BomItemId)
	require.Len(t, resp.Blockers, 1, "подклад стоит и называется")
	require.Contains(t, resp.Blockers[0].Reason, "подкладка купра")
}

// Клеевая (T4-усиление): interlining-строка, ПРИВЯЗАННАЯ к детали, бьёт правило «единственный
// клеевой слот колорвея» — и разрешает пару там, где два слота колорвея раньше давали пустоту.
// Пара едет на строке ОСНОВНОГО слоя.
func TestCutPlanPieceBoundFusingBeatsColorwayWide(t *testing.T) {
	card := t4LayeredCard()
	card.Pieces[0].Fused = true
	card.BomItems = append(card.BomItems,
		entity.TechCardBomItem{Id: 504, LineKey: "BOM-FUSE", Name: "дублерин 30г", Section: entity.BomSectionInterlining, MaterialId: cutMid(600)},
		entity.TechCardBomItem{Id: 507, LineKey: "BOM-FUSE2", Name: "дублерин 50г", Section: entity.BomSectionInterlining, MaterialId: cutMid(601)},
	)
	// Два клеевых слота у колорвея (раньше — неоднозначность, пары нет), но один привязан к детали.
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: cutMid(504), PieceId: cutMid(11), Consumption: nd2("1")},
		entity.TechCardColorwayUsage{BomItemId: cutMid(507), Consumption: nd2("1")},
	)

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		card, nil)

	require.Len(t, resp.Rows, 2)
	require.Empty(t, resp.Blockers)
	shell, lining := resp.Rows[0], resp.Rows[1]
	require.Equal(t, int64(501), shell.BomItemId)
	require.Equal(t, int64(504), shell.FusingBomItemId, "привязанная клеевая, на строке основного слоя")
	require.Equal(t, "дублерин 30г", shell.FusingMaterialName)
	require.Equal(t, int64(0), lining.FusingBomItemId, "подклад не дублируют")
}

// Покрытие настилов: деталь с двумя слоями обязана присутствовать в настиле КАЖДОГО слоя. Шелл
// настелен, подклад нет — клетка BLOCKER с виноватым слотом подклада, и полочка приходит в
// диагностику двумя голосами.
func TestLayCoverageRequiresPiecePerLayer(t *testing.T) {
	card := twoClothCard()
	card.Pieces = card.Pieces[:1] // одна полочка
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: slotRef(fixtureFabricSlot), PieceId: slotRef(1)},
		{BomItemId: slotRef(fixtureLiningSlot), PieceId: slotRef(1)},
	}

	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
		Lays:  []Lay{lay("основная", fixtureCw1, fixtureFabricSlot, 20, frontMarker(t))},
	})
	c := cellOf(t, cov, fixtureCw1, fixtureSize)
	if c.Status != CoverageStatusBlocker {
		t.Fatalf("status = %s, want BLOCKER — слой подклада не настелен", c.Status)
	}
	if c.CoveredQty != 0 {
		t.Errorf("covered = %d, want 0 — изделий без подкладных полочек нет", c.CoveredQty)
	}
	foundLining := false
	for _, id := range c.BlockingBomItemIDs {
		if id == fixtureLiningSlot {
			foundLining = true
		}
	}
	if !foundLining {
		t.Errorf("blocking slots = %v, want to contain the lining slot %d", c.BlockingBomItemIDs, fixtureLiningSlot)
	}
	layers := 0
	for _, y := range cov.PieceYields {
		if y.PieceLineKey == "K_FRONT" {
			layers++
		}
	}
	if layers != 2 {
		t.Errorf("piece yields for K_FRONT = %d, want 2 — по голосу на слой", layers)
	}
}

// Гейт готовности: три находки слоёв.
func TestRunReadinessPieceLayerFindings(t *testing.T) {
	bind := func(card *entity.TechCard, bomID int64) {
		card.Colorways[0].Usages = append(card.Colorways[0].Usages, entity.TechCardColorwayUsage{
			BomItemId: sql.NullInt64{Int64: bomID, Valid: true},
			PieceId:   sql.NullInt64{Int64: 1, Valid: true},
		})
	}

	t.Run("здоровая карточка без детальных строк — все три OK", func(t *testing.T) {
		res := ComputeProductionRunReadiness(rrInput(rrHealthyCard()))
		got := rrKeys(res)
		for _, k := range []string{entity.RunReadinessKeyPieceRoleConflict,
			entity.RunReadinessKeyPieceMainFabric, entity.RunReadinessKeyPieceFabricSorted} {
			if got[k] != entity.RunReadinessOK {
				t.Errorf("key %q = %s, want ok", k, got[k])
			}
		}
	})

	t.Run("две основные на детали — piece_role_conflict BLOCKER", func(t *testing.T) {
		card := rrHealthyCard()
		card.BomItems[0].Purpose = t4Purpose(entity.BomPurposeMain)
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{
			Id: 77, LineKey: "BOM2", Section: entity.BomSectionFabric, Name: "поплин",
			Purpose: t4Purpose(entity.BomPurposeMain),
		})
		bind(card, int64(card.BomItems[0].Id))
		bind(card, 77)
		res := ComputeProductionRunReadiness(rrInput(card))
		f, ok := rrFind(res, entity.RunReadinessKeyPieceRoleConflict)
		if !ok || f.Severity != entity.RunReadinessBlocker {
			t.Fatalf("piece_role_conflict = %+v, want BLOCKER", f)
		}
		if !strings.Contains(f.Detail, "перед") || !strings.Contains(f.Detail, "поплин") {
			t.Errorf("detail must name the piece and the fabrics: %q", f.Detail)
		}
	})

	t.Run("подклад без основной — piece_main_fabric WARNING", func(t *testing.T) {
		card := rrHealthyCard()
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{
			Id: 78, LineKey: "BOM-LIN", Section: entity.BomSectionLining, Name: "купра",
		})
		bind(card, 78)
		res := ComputeProductionRunReadiness(rrInput(card))
		f, ok := rrFind(res, entity.RunReadinessKeyPieceMainFabric)
		if !ok || f.Severity != entity.RunReadinessWarning {
			t.Fatalf("piece_main_fabric = %+v, want WARNING", f)
		}
		if !strings.Contains(f.Detail, "lining") {
			t.Errorf("detail must name the roles the piece HAS: %q", f.Detail)
		}
		if got, _ := rrFind(res, entity.RunReadinessKeyPieceRoleConflict); got.Severity != entity.RunReadinessOK {
			t.Errorf("piece_role_conflict must stay OK, got %s", got.Severity)
		}
	})

	t.Run("единственная неразложенная ткань — piece_fabric_sorted WARNING, конфликта нет", func(t *testing.T) {
		card := rrHealthyCard()
		bind(card, int64(card.BomItems[0].Id)) // fabric без назначения
		res := ComputeProductionRunReadiness(rrInput(card))
		f, ok := rrFind(res, entity.RunReadinessKeyPieceFabricSorted)
		if !ok || f.Severity != entity.RunReadinessWarning {
			t.Fatalf("piece_fabric_sorted = %+v, want WARNING", f)
		}
		if got, _ := rrFind(res, entity.RunReadinessKeyPieceRoleConflict); got.Severity != entity.RunReadinessOK {
			t.Errorf("одна неразложенная строка — ещё не конфликт, got %s", got.Severity)
		}
		if got, _ := rrFind(res, entity.RunReadinessKeyPieceMainFabric); got.Severity != entity.RunReadinessOK {
			t.Errorf("неразложенная может оказаться основной — П2 молчит, got %s", got.Severity)
		}
	})

	t.Run("двойной подклад — piece_role_conflict WARNING, не блокер", func(t *testing.T) {
		card := rrHealthyCard()
		card.BomItems[0].Purpose = t4Purpose(entity.BomPurposeMain)
		card.BomItems = append(card.BomItems,
			entity.TechCardBomItem{Id: 78, LineKey: "BOM-LIN", Section: entity.BomSectionLining, Name: "купра"},
			entity.TechCardBomItem{Id: 79, LineKey: "BOM-LIN2", Section: entity.BomSectionLining, Name: "вискоза"},
		)
		bind(card, int64(card.BomItems[0].Id))
		bind(card, 78)
		bind(card, 79)
		res := ComputeProductionRunReadiness(rrInput(card))
		f, ok := rrFind(res, entity.RunReadinessKeyPieceRoleConflict)
		if !ok || f.Severity != entity.RunReadinessWarning {
			t.Fatalf("piece_role_conflict = %+v, want WARNING на дубль не-основной роли", f)
		}
		if !strings.Contains(f.Detail, "купра") || !strings.Contains(f.Detail, "вискоза") {
			t.Errorf("detail must name both linings: %q", f.Detail)
		}
	})
}

// Кат-лист стиля читает рецептную половину проекции: запись на каждый слой, клеевая парой на
// записи основного слоя. Второй источник — замороженная tech_card_piece_material — проверяется
// ниже, в TestStyleCutFabricIndexReadsFrozenPieceMaterialTable.
func TestStyleCutFabricIndexProjectsRecipeLayers(t *testing.T) {
	card := t4LayeredCard()
	card.Pieces[0].Fused = true
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 504, LineKey: "BOM-FUSE", Name: "дублерин 30г", Section: entity.BomSectionInterlining, MaterialId: cutMid(600),
	})
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: cutMid(504), PieceId: cutMid(11), Consumption: nd2("1")})

	idx := NewStyleCutFabricIndex(card)
	fabrics := idx.FabricsFor(&card.Pieces[0])
	require.Len(t, fabrics, 2, "по записи на кроимый слой")
	require.Equal(t, int64(501), fabrics[0].BomItemId)
	require.Equal(t, int64(55), fabrics[0].ColorwayId, "идентичность колонки — product id колорвея")
	require.Equal(t, int64(504), fabrics[0].FusingBomItemId, "клеевая на записи основного слоя")
	require.Equal(t, int64(503), fabrics[1].BomItemId)
	require.Equal(t, int64(0), fabrics[1].FusingBomItemId)
}

// Совсем старый замороженный релиз: из трёх форм привязки «строка → деталь» в снапшоте выжил один
// лишь позиционный piece_index (piece_id релиз не несёт по контракту, line_key ещё не существовал),
// у строк BOM нет ни id, ни line_key. Предикат T8 такие строки ПРИЗНАЁТ назначениями — разбор слоёв
// обязан узнавать их теми же тремя формами: деталь с индексом 0 и двумя слоями даёт ДВЕ строки
// наряда, а не ложную неоднозначность «2 roll slots» (pieceUsageIndex, игнорировавший позиционную
// форму, ронял резолюцию в правило (б) ровно на самой надёжной — замороженной — спецификации).
func TestCutPlanReleaseLegacyPieceIndexBindsLayers(t *testing.T) {
	zero, one := int32(0), int32(1)
	snap := &pb_common.TechCard{
		Id: 7,
		TechCard: &pb_common.TechCardInsert{
			SizeIds: []int32{1},
			BomItems: []*pb_common.TechCardBomItem{
				{Name: "основная ткань", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, MaterialId: 100},
				{Name: "подкладка купра", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LINING, MaterialId: 400},
			},
			Pieces: []*pb_common.TechCardPiece{{Name: "полочка", PiecesPerGarment: 2}},
		},
		Colorways: []*pb_common.AdminColorwayRef{{
			ColorwayId: 55, DevName: "black",
			Usages: []*pb_common.TechCardColorwayUsage{
				{BomItemIndex: &zero, PieceIndex: &zero},
				{BomItemIndex: &one, PieceIndex: &zero},
			},
		}},
	}
	card := CutSpecCardFromReleaseSnapshot(snap)
	require.NotNil(t, card)

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}),
		card, &entity.TechCardReleaseMeta{Id: 31, ReleaseNumber: 2})

	require.Empty(t, resp.Blockers, "легаси-привязка узнана — двусмысленности «2 roll slots» нет")
	require.Len(t, resp.Rows, 2, "по строке на каждый слой, как у живой карточки")
	require.Equal(t, int32(100), resp.Rows[0].MaterialId, "порядок рецепта: шелл первым")
	require.Equal(t, int32(400), resp.Rows[1].MaterialId)
	for _, row := range resp.Rows {
		require.False(t, row.SlotInferred, "рецепт назвал слои — это правило (а), не вывод")
		require.Equal(t, int32(20), row.PiecesToCutTotal)
	}
}

// РЕГРЕССИЯ ПРОДА: у живой карточки прода связь «деталь → ткань» живёт ТОЛЬКО в замороженной
// tech_card_piece_material (пять деталей одного колорвея на одном слоте), а рецепт того же
// колорвея весь «на изделие» (piece_id NULL). Источника связи ДВА, и деталь привязана, если её
// называет любой из них (шапка piece_layers.go): кат-лист стиля обязан дать ткань КАЖДОЙ детали,
// и наряд обязан видеть ту же связь — правило одно, в общем pieceUsageIndex.
func TestStyleCutFabricIndexReadsFrozenPieceMaterialTable(t *testing.T) {
	card := cutPlanSingleFabricCard() // рецепт: только строки «на изделие», деталей не называет
	card.BomItems = append(card.BomItems,
		entity.TechCardBomItem{Id: 503, LineKey: "BOM-LINING", Name: "подкладка купра", Section: entity.BomSectionLining, MaterialId: cutMid(400)},
		entity.TechCardBomItem{Id: 504, LineKey: "BOM-FUSE", Name: "дублерин 30г", Section: entity.BomSectionInterlining, MaterialId: cutMid(600)},
	)
	names := []string{"FP", "BP", "RP", "LP", "BLT"}
	card.Pieces = card.Pieces[:0]
	for i, n := range names {
		card.Pieces = append(card.Pieces, entity.TechCardPiece{
			Id: 11 + i, LineKey: "P-" + n, Name: n, PiecesPerGarment: 1, Grainline: "lengthwise",
			Materials: []entity.TechCardPieceMaterial{{ColorwayID: 55, BomItemId: cutMid(501)}},
		})
	}
	// Строка ЧУЖОГО колорвея не подмешивается: фильтр по colorway_id, а не «всё, что есть у детали».
	card.Pieces[0].Materials = append(card.Pieces[0].Materials,
		entity.TechCardPieceMaterial{ColorwayID: 99, BomItemId: cutMid(503)})
	// Клеевая старой таблицы едет парой fusing_* — тем же правилом секции, что у рецепта.
	card.Pieces[4].Fused = true
	card.Pieces[4].Materials[0].FusingBomItemId = cutMid(504)

	idx := NewStyleCutFabricIndex(card)
	for i := range card.Pieces {
		fabrics := idx.FabricsFor(&card.Pieces[i])
		require.Len(t, fabrics, 1, "деталь %s: ткань названа старой таблицей", card.Pieces[i].Name)
		require.Equal(t, int64(501), fabrics[0].BomItemId)
		require.Equal(t, "основная ткань", fabrics[0].FabricName)
		require.Equal(t, int64(55), fabrics[0].ColorwayId)
	}
	blt := idx.FabricsFor(&card.Pieces[4])
	require.Equal(t, int64(504), blt[0].FusingBomItemId, "клеевая из старой таблицы — парой на записи слоя")
	require.Equal(t, "дублерин 30г", blt[0].FusingName)

	// Наряд видит ТУ ЖЕ связь: правило (а), а не вывод единственного слота и не блокер.
	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}), card, nil)
	require.Empty(t, resp.Blockers)
	require.Len(t, resp.Rows, len(names), "по строке на каждую привязанную деталь")
	for _, row := range resp.Rows {
		require.Equal(t, int64(501), row.BomItemId)
		require.False(t, row.SlotInferred, "связь НАЗВАНА старой таблицей, а не выведена")
	}
}

// Пересечение источников безопасно: деталь, названную на одном слоте И рецептом, И старой таблицей,
// кат-лист считает ОДИН раз, а слот достаётся строке РЕЦЕПТА — её пин артикула не теряется.
func TestPieceLayersBothSourcesCountOnceRecipePinWins(t *testing.T) {
	card := cutPlanSingleFabricCard()
	card.Colorways[0].Usages = append(card.Colorways[0].Usages,
		entity.TechCardColorwayUsage{BomItemId: cutMid(501), PieceId: cutMid(11), MaterialId: cutMid(777)})
	card.Pieces[0].Materials = []entity.TechCardPieceMaterial{{ColorwayID: 55, BomItemId: cutMid(501)}}

	idx := NewStyleCutFabricIndex(card)
	fabrics := idx.FabricsFor(&card.Pieces[0])
	require.Len(t, fabrics, 1, "оба источника называют один слот — деталь входит один раз")
	require.Equal(t, int64(501), fabrics[0].BomItemId)

	resp := ComputeProductionRunCutPlan(
		cutPlanRun(entity.ProductionRunLine{ProductId: cutPid(55), SizeId: 1, PlannedQty: 10}), card, nil)
	require.Empty(t, resp.Blockers)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, int32(777), resp.Rows[0].MaterialId, "слот выиграла строка рецепта — пин жив")
	require.True(t, resp.Rows[0].Pinned)
}

// Покрытие настилов: привязка детали к НЕРУЛОННОЙ строке (швейная нитка) — законное назначение
// материала (T8), но НЕ слой кроя (T4). Настил для нитки невозможен в принципе — стор разрешает
// настилы только для рулонных материалов, — и обязательная «деталь-слой» из неё навсегда блокировала
// бы полностью покрытую клетку и делала источник MIXED. Правило слоёв одно на всех —
// collectPieceLayers, как у наряда и кат-листа, а не широкий planSlotSections материального плана.
func TestLayCoverageThreadBoundPieceIsNotARequiredLayer(t *testing.T) {
	card := twoClothCard()
	card.Pieces = card.Pieces[:1] // одна полочка
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 14, LineKey: "BOM_THREAD", Name: "НИТКА ШВЕЙНАЯ", Section: entity.BomSectionThread,
	})
	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{
		{BomItemId: slotRef(fixtureFabricSlot), PieceId: slotRef(1)},
		{BomItemId: slotRef(14), PieceId: slotRef(1)},
	}

	required := requiredPiecesForColorway(card, fixtureCw1)
	require.Len(t, required, 1, "нитка не слой — у полочки ровно один обязательный слой, ткань")
	require.True(t, required[0].resolved)
	require.Equal(t, int64(fixtureFabricSlot), required[0].bomItemID)

	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  card,
		Lines: []entity.ProductionRunLine{runLine("L1", fixtureCw1, fixtureSize, 20)},
		Lays:  []Lay{lay("основная", fixtureCw1, fixtureFabricSlot, 20, frontMarker(t))},
	})
	c := cellOf(t, cov, fixtureCw1, fixtureSize)
	require.Equal(t, CoverageStatusOK, c.Status, "настил ткани полностью покрывает тираж")
	require.Equal(t, 20, c.CoveredQty)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, c.Source,
		"ниточный слот не делает источник MIXED")
	require.Empty(t, c.SlotsWithoutLays)
	require.Zero(t, c.UnknownPieceCount)
}
