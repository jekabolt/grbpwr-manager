package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// T8 — СТРОКА РЕЦЕПТА, ПРИВЯЗАННАЯ К ДЕТАЛИ, НЕ НЕСЁТ НОРМУ (решение владельца: расход ткани —
// свойство ИЗДЕЛИЯ; пер-детальная строка лишь назначает детали материал).
//
// Два симметричных следствия, и оба здесь проверяются на КАЖДОМ потребителе рецепта:
//   1. легаси-ЧИСЛО на строке детали не прибавляется ни к себестоимости, ни к смете, ни к
//      потребности прогона;
//   2. строка детали БЕЗ числа — не «нормы нет»: ни блокера, ни каветы, ни hasUnpriced, когда
//      строка изделия того же слота норму НЕСЁТ.
//
// Ключевая пара из спеки — «строка детали без нормы + строка изделия с нормой на том же слоте» —
// до правки давала И лишний вклад, И ложный блокер одновременно; здесь она встроена в каждую
// фикстуру. Единственное правило — entity.TechCardColorwayUsage.IsPieceMaterialAssignment.

func pieceRow(bomID int64, pieceID int64, consumption string) entity.TechCardColorwayUsage {
	u := entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: bomID, Valid: true},
		PieceId:   sql.NullInt64{Int64: pieceID, Valid: true},
	}
	if consumption != "" {
		u.Consumption = nd2(consumption)
	}
	return u
}

// t8Card: один слот ткани по 10 EUR/м без отхода, колорвей 55 — строка изделия на 2 м, рядом две
// пер-детальные строки: одна с легаси-числом 1.4 (девять таких живут на бете), одна пустая
// (то, что массово произведёт клиент T1).
func t8Card() *entity.TechCard {
	c := &entity.TechCard{Id: 7}
	c.BomItems = []entity.TechCardBomItem{{
		Id: 1, Name: "Main fabric", Section: entity.BomSectionFabric,
		Unit: nstr("m"), UnitPrice: nd("10"), Currency: nstr("EUR"),
		MaterialId: sql.NullInt64{Int64: 100, Valid: true},
	}}
	c.Pieces = []entity.TechCardPiece{
		{Id: 1, LineKey: "PIECE1", Name: "перед", PiecesPerGarment: 1},
		{Id: 2, LineKey: "PIECE2", Name: "спинка", PiecesPerGarment: 1},
	}
	c.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "Black", ProductId: bidx(55), Usages: []entity.TechCardColorwayUsage{
			pieceRow(1, 1, "1.4"), // легаси-число на детали — НЕ прибавляется
			{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: nd2("2")}, // норма изделия
			pieceRow(1, 2, ""), // пустая строка детали — НЕ «нормы нет»
		},
	}}
	c.Costing = &entity.TechCardCosting{Currency: nstr("EUR")}
	return c
}

// Себестоимость колорвея (colorwayCost через посевную точку ComputeColorwayUnitCost): 2 м × 10 =
// 20, а не 34 (сумма с легаси-числом детали) и не «непосчитано» (пустая строка детали ставила
// hasUnpriced и глушила посев целиком — вместе с плановой ценой прогона).
func TestT8ColorwayCostSkipsPieceRows(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	unit, ccy := ComputeColorwayUnitCost(t8Card(), 55, fx)
	require.True(t, unit.Valid, "пустая строка детали не имеет права глушить посев (hasUnpriced)")
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "20", unit.Decimal.String(), "только норма изделия: 2 м × 10; 1.4 детали не прибавляется")
}

// Колорвей, у которого ВСЕ строки — назначения деталей, для расчёта — колорвей БЕЗ рецепта: он
// наследует цифру стиля (первичного колорвея), а не «материалы = 0 + ручные статьи».
func TestT8AllPieceRowsColorwayInheritsStyleFigure(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := t8Card()
	card.Colorways = append(card.Colorways, entity.TechCardColorway{
		Id: 2, Name: "White", ProductId: bidx(66),
		Usages: []entity.TechCardColorwayUsage{pieceRow(1, 1, "10")},
	})
	unit, _ := ComputeColorwayUnitCost(card, 66, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "20", unit.Decimal.String(),
		"рецепт из одних строк деталей = пустой рецепт: цифра стиля, а не 100 (10 м × 10 с детали)")
}

// Смета стиля: строка детали не даёт ни строки материалов, ни вклада в базовую сумму, ни каветы
// «no consumption» — пустая пер-детальная строка не «недосчитанная позиция».
func TestT8StyleCostEstimateSkipsPieceRows(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	out := ComputeStyleCostEstimate(t8Card(), 0, nil, fx)
	require.NotNil(t, out)
	require.Len(t, out.Materials, 1, "одна строка сметы — норма изделия; строки деталей не печатаются")
	require.Equal(t, "20.00", out.MaterialsPerUnitBase.Value)
	require.Empty(t, out.Caveat, "пустая строка детали не порождает каветы «no consumption or quantity»")
}

// Потребность прогона: ключевая пара на одном слоте. До правки строка детали с числом прибавляла
// 10 м/изделие к потребности, а пустая — вешала блокер slot_norm на слот, у которого норма
// изделия ЕСТЬ (вклад и блокер жили на одном ключе независимо). Теперь: ровно один вклад, ноль
// блокеров, потребность — только по норме изделия.
func TestT8MaterialPlanPieceRowsNeitherContributeNorBlock(t *testing.T) {
	card := t8Card()
	// Утяжелим пару: пер-детальная строка с числом 10 (максимальный из живущих на бете хвостов).
	card.Colorways[0].Usages[0].Consumption = nd2("10")
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
		},
	}}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, nil, nil)
	require.Empty(t, resp.Blockers, "пустая строка детали — не «нормы нет»: норма изделия на слоте есть")
	require.Len(t, resp.Contributions, 1, "строки деталей вкладов не дают")
	require.Len(t, resp.Rows, 1)
	require.Equal(t, "20", resp.Rows[0].Required.Value, "2 м × 10 изделий; 10 м/изделие с детали не прибавились")
}

// Слот, на который ссылаются ТОЛЬКО строки деталей, честно падает в блокер «нормы нет»: нормы
// изделия у него действительно нет, и его легаси-число не считается потребностью.
func TestT8MaterialPlanPieceOnlySlotIsAnHonestNoNormBlocker(t *testing.T) {
	card := t8Card()
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 2, Name: "Lining", Section: entity.BomSectionLining,
		Unit: nstr("m"), MaterialId: sql.NullInt64{Int64: 200, Valid: true},
	})
	card.Colorways[0].Usages = append(card.Colorways[0].Usages, pieceRow(2, 2, "1.1"))
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
		},
	}}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, nil, nil)
	require.Len(t, resp.Rows, 1, "1.1 м/изделие с детали не становится потребностью подклада")
	require.Equal(t, int32(100), resp.Rows[0].MaterialId)
	require.Len(t, resp.Blockers, 1)
	require.Equal(t, entity.MaterialPlanBlockerNoNorm, resp.Blockers[0].Key)
	require.Equal(t, "Lining", resp.Blockers[0].SlotName)
}

// Гейт готовности, сквозной прогон: здоровая карточка остаётся READY после появления
// пер-детальных строк (пустой и с числом), slot_norm не краснеет, norm_provenance печатается
// ОДИН раз (а не по разу на деталь), и покрытие в изделиях не зависит от порядка строк — пустая
// строка детали, отсортированная ПОСЛЕ строки изделия, раньше затеняла её в usageByBom и
// обнуляла provisioned_qty.
func TestT8RunReadinessPieceRowsNeitherBlockNorDuplicate(t *testing.T) {
	card := rrHealthyCard()
	cw := &card.Colorways[0]
	empty := entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: rrBom, Valid: true},
		PieceId:   sql.NullInt64{Int64: 1, Valid: true},
	}
	withNumber := empty
	withNumber.Consumption = nd2("1.4")
	// С числом — ПЕРЕД строкой изделия, пустая — ПОСЛЕ: атака на обе порядковые случайности разом.
	cw.Usages = append([]entity.TechCardColorwayUsage{withNumber}, append(cw.Usages, empty)...)

	res := ComputeProductionRunReadiness(rrInput(card))
	require.True(t, res.Report.Ready(), "ложный отказ: %v", res.Report.Blockers())

	keys := rrKeys(res)
	require.Equal(t, entity.RunReadinessOK, keys[entity.RunReadinessKeySlotNorm],
		"строка детали без числа — не «нормы нет»")

	provenanceRows := 0
	for _, c := range res.Report.Colorways {
		for _, f := range c.Findings {
			if f.Key == entity.RunReadinessKeyNormProvenance {
				provenanceRows++
			}
		}
	}
	require.Equal(t, 1, provenanceRows, "провенанс нормы судится по строке изделия, не по разу на деталь")

	require.Len(t, res.UnitCoverage, 1)
	require.Equal(t, int32(10), res.UnitCoverage[0].ProvisionedQty,
		"пустая строка детали, стоящая последней, не затеняет норму изделия в покрытии")
	require.Empty(t, res.UnitCoverage[0].BlockingBomItemIds)
}

// АТРИБУЦИЯ НАСТИЛА — ПРАВИЛО НАЗВАНО, А НЕ УГАДАНО (ревью T8, блокер 1): пин строки ИЗДЕЛИЯ
// имеет приоритет; при его отсутствии — пин строк ДЕТАЛЕЙ, если он единственный (назначение
// артикула строкой детали ЗАКОННО, и терять его нельзя: кат-лист кроит из него, валидация лота
// его принимает); при расхождении пинов деталей — первый пин плюс названный вслух PieceClash;
// иначе умолчание слота.
func TestT8LayArticleRule(t *testing.T) {
	// Вход ревью: слот по умолчанию несёт 100; строка изделия норму имеет, но пина не несёт;
	// строка детали пинует 999. Настил обязан атрибутироваться к 999 — тому же рулону, который
	// выбирает кат-лист и допускает валидация лота, — а не к 100 «по умолчанию».
	card := t8Card()
	card.Colorways[0].Usages[0].MaterialId = sql.NullInt64{Int64: 999, Valid: true}
	res := ResolveLayArticle(card, 55, 1)
	require.Equal(t, 999, res.MaterialId, "единственный пин детали и есть артикул слота: цех кроит из этого рулона")
	require.True(t, res.Pinned)
	require.Empty(t, res.PieceClash, "один пин — не расхождение")

	// Пин строки ИЗДЕЛИЯ приоритетнее пина детали, в любом порядке строк.
	card.Colorways[0].Usages[1].MaterialId = sql.NullInt64{Int64: 500, Valid: true}
	require.Equal(t, 500, LayArticleMaterialId(card, 55, 1), "пин строки изделия имеет приоритет")

	// Расхождение пинов деталей без пина изделия: не угадывать молча — первый пин, и расхождение
	// возвращено вслух (та же интонация, что layArticleClash материального плана).
	card = t8Card()
	card.Colorways[0].Usages[0].MaterialId = sql.NullInt64{Int64: 999, Valid: true}
	card.Colorways[0].Usages[2].MaterialId = sql.NullInt64{Int64: 888, Valid: true}
	res = ResolveLayArticle(card, 55, 1)
	require.Equal(t, 999, res.MaterialId, "детерминированно первый пин по порядку строк")
	require.Equal(t, []int{999, 888}, res.PieceClash, "оба артикула названы — потребитель обязан сказать это каветой")

	// Ни одного пина нигде → умолчание слота; пин детали, РАВНЫЙ умолчанию, ведёт себя как
	// умолчание (правило EffectiveMaterialId — одно, не два).
	card = t8Card()
	require.Equal(t, 100, LayArticleMaterialId(card, 55, 1))
	card.Colorways[0].Usages[0].MaterialId = sql.NullInt64{Int64: 100, Valid: true}
	res = ResolveLayArticle(card, 55, 1)
	require.Equal(t, 100, res.MaterialId)
	require.False(t, res.Pinned, "пин, равный умолчанию, — не пин")
}

// СВЕРКА «АРТИКУЛ НАСТИЛА ↔ АРТИКУЛ КАТ-ЛИСТА» при пине на строке детали: обе проекции обязаны
// назвать ОДИН рулон. До правки кат-лист кроил из 999, а настил атрибутировался к 100 — физическое
// расхождение: цех кроит из одного рулона, план считает другой.
func TestT8LayArticleAgreesWithCutPlan(t *testing.T) {
	card := t8Card()
	// Обе детали назначены строками рецепта на пин 999; строка изделия несёт норму без пина.
	card.Colorways[0].Usages[0].MaterialId = sql.NullInt64{Int64: 999, Valid: true}
	card.Colorways[0].Usages[2].MaterialId = sql.NullInt64{Int64: 999, Valid: true}
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
		},
	}}

	plan := ComputeProductionRunCutPlan(run, card, nil)
	require.NotEmpty(t, plan.Rows)
	layArticle := LayArticleMaterialId(card, 55, 1)
	for _, row := range plan.Rows {
		require.Equal(t, int32(layArticle), row.MaterialId,
			"деталь %q: наряд и настил обязаны назвать один рулон", row.PieceName)
	}
	require.Equal(t, 999, layArticle, "и этот рулон — пин строк деталей, а не умолчание слота")
}

// Материальный план, настил на паре с пином детали: метраж настила атрибутируется тому же
// артикулу, что настил-вью и кат-лист (один резолвер), а расхождение пинов деталей называется
// каветой — той же интонацией, что layArticleClash.
func TestT8MaterialPlanLayAttributionFollowsPiecePin(t *testing.T) {
	card := t8Card()
	card.Colorways[0].Usages[0].MaterialId = sql.NullInt64{Int64: 999, Valid: true}
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
		},
	}}
	lay := entity.ProductionRunLay{
		Id: 1, RunId: 9, LayKey: "LAY1", ColorwayId: 55, Name: "настил-1",
		BomItemId: sql.NullInt64{Int64: 1, Valid: true},
		Mode:      entity.ProductionLayModeFaceUp, EndLossCm: d("0"),
		Sections: []entity.ProductionRunLaySection{{
			MarkerId: 9001, Plies: 10, MarkerName: "раскладка", SectionKey: "SEC",
			MarkerUsedLengthCm: nd2("300"),
		}},
	}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, nil, []entity.ProductionRunLay{lay})
	require.Len(t, resp.Rows, 1)
	require.Equal(t, int32(999), resp.Rows[0].MaterialId,
		"метраж настила — на рулон пина детали, тот же ответ, что у настил-вью и кат-листа")
	require.False(t, hasCaveat(resp.Caveats, "different articles"), "единственный пин — не расхождение")

	// Теперь пины деталей расходятся: атрибуция — первому пину, и это НАЗВАНО.
	card.Colorways[0].Usages[2].MaterialId = sql.NullInt64{Int64: 888, Valid: true}
	resp = ComputeProductionRunMaterialPlan(run, card, nil, nil, nil, []entity.ProductionRunLay{lay})
	require.Len(t, resp.Rows, 1)
	require.Equal(t, int32(999), resp.Rows[0].MaterialId)
	require.True(t, hasCaveat(resp.Caveats, "piece rows pin 2 different articles"),
		"расхождение пинов деталей обязано быть названо, а не сглотнуто: %v", resp.Caveats)
}

// Провод: строка-назначение детали не отдаёт line_total/size_run_total — денег, которых
// себестоимость больше не содержит, на проводе нет, и просуммировать карточку заново нельзя.
// Правило живёт в методах entity (LineTotal и сёстры), конвертер лишь один из читателей.
func TestT8ConvertRecipeUsagesToPbPieceRowHasNoMoney(t *testing.T) {
	card := t8Card()
	usages := card.Colorways[0].Usages
	pb := ConvertRecipeUsagesToPb(usages, card.BomItems, card.Pieces, nil)
	require.Len(t, pb, 3)
	require.Nil(t, pb[0].LineTotal, "легаси-число 1.4 на строке детали не превращается в line_total")
	require.NotNil(t, pb[1].LineTotal, "строка изделия деньги несёт")
	require.Equal(t, "20", pb[1].LineTotal.Value, "2 м × 10")
	require.Nil(t, pb[2].LineTotal)

	// И per-size ветка тоже: строка детали с посайзовой сеткой не отдаёт size_run_total.
	graded := entity.TechCardColorwayUsage{
		BomItemId:        sql.NullInt64{Int64: 1, Valid: true},
		PieceLineKey:     "PIECE1", // привязка только ключом, без PieceId — форма снапшота/провода
		SizeConsumptions: []entity.TechCardBomSizeConsumption{{SizeId: 1, Consumption: d("2")}},
	}
	pb = ConvertRecipeUsagesToPb([]entity.TechCardColorwayUsage{graded}, card.BomItems, card.Pieces, map[int]int{1: 5})
	require.Nil(t, pb[0].SizeRunTotal, "и посайзовые деньги строки детали не едут на провод")
}

// Строка детали, привязанная ТОЛЬКО piece_line_key (без PieceId) — форма провода и снапшота —
// подчиняется тому же правилу: её число не прибавляется, её пустота не глушит посев.
func TestT8PieceLineKeyOnlyRowIsAnAssignment(t *testing.T) {
	card := t8Card()
	card.Colorways[0].Usages = append(card.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId:    sql.NullInt64{Int64: 1, Valid: true},
		PieceLineKey: "PIECE2",
		Consumption:  nd2("5"),
	})
	unit, _ := ComputeColorwayUnitCost(card, 55, CostingFx{Base: "EUR"})
	require.True(t, unit.Valid)
	require.Equal(t, "20", unit.Decimal.String(), "5 м «на деталь по ключу» не прибавились к 2 м изделия")
}

// Разложение себестоимости: колорвей из одних назначений деталей наследует РАЗЛОЖЕНИЕ стиля, как
// его цена наследует цифру стиля. Собственное разложение дало бы materials = 0 рядом с ценой, в
// которой ткань есть, и метрики структуры COGS раскладывали бы реальную цифру по неверным долям.
func TestT8BreakdownAllPieceRowsColorwayInheritsStyleShares(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := t8Card()
	card.Costing.CmtCost = nd("5")
	card.Colorways = append(card.Colorways, entity.TechCardColorway{
		Id: 2, Name: "White", ProductId: bidx(66),
		Usages: []entity.TechCardColorwayUsage{pieceRow(1, 1, "10")},
	})

	own, ok := ComputeColorwayCostBreakdownBase(card, 66, fx)
	require.True(t, ok)
	style, ok := ComputeTechCardCostBreakdownBase(card, fx)
	require.True(t, ok)
	require.Equal(t, style, own, "разложение колорвея без рецепта = разложение стиля, той же проекцией")
	require.True(t, own.Materials.IsPositive(),
		"materials наследуется от стиля (2 м × 10), а не обнуляется пустым собственным рецептом")

	// Пара (цена, разложение) согласована: цена тоже стилевая.
	unit, _ := ComputeColorwayUnitCost(card, 66, fx)
	require.Equal(t, "25", unit.Decimal.String(), "20 материалов + 5 CMT — цифра стиля")
}
