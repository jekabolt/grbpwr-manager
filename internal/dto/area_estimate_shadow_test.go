package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ТЕНЕВАЯ ОЦЕНКА (Ф6.3-BE, вторая половина). Слот с авторской нормой публикует оценку по площади
// СПРАВОЧНО — чтобы расстояние между вписанным человеком числом и геометрической нижней границей
// было видно хоть где-нибудь.
//
// Опасность этой врезки ровно одна и она денежная: публикуемое множество расширяется, считаемое —
// нет. Тесты ниже стерегут именно этот шов.

// shadowCard: измеренная карточка, у ЕДИНСТВЕННОГО рулонного слота которой есть и авторская норма
// (2 м, вписана человеком), и полная геометрия для теневой оценки.
//
// Числа выбраны так, чтобы утечка была видна с первого взгляда, а не проверялась знаками после
// запятой: авторская норма 2 м × 100 EUR = 200, теневая 1.25 м × 100 EUR = 125. Итог 325 (или
// вообще любой не-200) означает, что тень попала в деньги.
func shadowCard() *entity.TechCard {
	tc := measuredCard()
	// Строка-назначение детали остаётся (она и даёт геометрию), а рядом появляется ГАРНИТУРНАЯ
	// строка с нормой — то самое «норма заявлена», из-за чего слот выпадает из активной ступени.
	tc.Colorways[0].Usages = append(tc.Colorways[0].Usages, entity.TechCardColorwayUsage{
		Id:          40,
		BomItemId:   sql.NullInt64{Int64: 56, Valid: true},
		Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("2"), Valid: true},
	})
	return tc
}

// TestShadowEstimateDoesNotWidenTheMoneySet — ГЛАВНЫЙ тест фазы.
//
// Публикация тени и подсчёт денег ходят в один и тот же slotAreaEstimate, и соблазн посчитать всё
// одним проходом здесь постоянный. Тест прибивает цену карточки, у которой слот несёт авторскую
// норму, к тому же числу, что и до врезки: 200 EUR за 2 м, и НИ ОДНОЙ оценки в итоге. Одновременно
// он требует, чтобы тень при этом действительно существовала и была ДРУГОЙ цифрой (1.25 м) — иначе
// «деньги не поехали» доказывалось бы тем, что тень не посчиталась вовсе.
func TestShadowEstimateDoesNotWidenTheMoneySet(t *testing.T) {
	tc := shadowCard()
	cw := &tc.Colorways[0]

	cc := colorwayCost(tc, cw, tc.BomItems, tc.LinkedMaterials, "EUR", tc.CostingBasis(), CostingFx{})
	require.Equal(t, "200", cc.materialsPerUnit.String(),
		"цена изделия должна остаться авторской (2 м × 100); 325 значит, что тень легла в итог")
	require.False(t, cc.hasEstimate,
		"hasEstimate поднят тенью — а он запрещает посев каталожной себестоимости и обнуляет плановую цену релизного прогона")
	require.False(t, cc.hasUnpriced, "авторская строка посчиталась; ничего не выпало из итога")
	require.Empty(t, cc.estimates,
		"денежный срез должен остаться пустым: у единственного слота есть авторская норма")

	// Посев каталожной себестоимости открыт ровно как и был: тень его не гасит.
	unit, ccy := ComputeTechCardUnitCost(tc, CostingFx{})
	require.True(t, unit.Valid, "тень заблокировала посев product.cost_price")
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "200", unit.Decimal.String())

	// А тень при этом ЕСТЬ и она другая — иначе тест выше проходил бы по причине «нечего было
	// складывать».
	sh := colorwayShadowAreaEstimates(tc, cw, tc.BomItems, tc.LinkedMaterials, tc.CostingBasis(), "")
	require.Len(t, sh, 1)
	require.True(t, sh[0].normDerived)
	require.Equal(t, "1.25", sh[0].perGarment.String(),
		"тень — среднее по объявленному ряду (1 м и 1.5 м), тем же базисом, что и деньги")
}

// TestShadowAndActiveTiersAreDisjoint: одна пара (колорвей, слот) не может попасть в обе ступени.
// Активная — это в точности «нормы нет», теневая — в точности «норма есть»; пересечение означало бы,
// что один слот и оценивается, и стоит денег дважды.
func TestShadowAndActiveTiersAreDisjoint(t *testing.T) {
	tc := shadowCard()
	// Второй рулонный слот, у которого нормы НЕТ: карточка теперь несёт обе ступени разом.
	tc.BomItems = append(tc.BomItems, entity.TechCardBomItem{
		Id:      57,
		LineKey: "BOMKEY2",
		Section: entity.BomSectionLining,
		Unit:    sql.NullString{String: "m", Valid: true},
	})
	cw := &tc.Colorways[0]

	active := colorwayAreaEstimates(tc, cw, tc.BomItems, tc.LinkedMaterials, tc.CostingBasis(), "")
	shadow := colorwayShadowAreaEstimates(tc, cw, tc.BomItems, tc.LinkedMaterials, tc.CostingBasis(), "")
	require.Len(t, active, 1)
	require.Len(t, shadow, 1)

	seen := map[string]bool{}
	for _, e := range append(append([]slotEstimate{}, active...), shadow...) {
		require.False(t, seen[e.bomLineKey], "слот %q попал в обе ступени", e.bomLineKey)
		seen[e.bomLineKey] = true
	}
	require.Equal(t, "BOMKEY2", active[0].bomLineKey)
	require.Equal(t, "BOMKEY1", shadow[0].bomLineKey)
}

// TestShadowRidesItsOwnWireField: старый клиент не должен получить тень ВООБЩЕ.
//
// На проводе стоит правило «у одной ткани либо строка рецепта, либо оценка», и клиент сшивает по
// нему два списка в один. Дослав тень в area_estimates, сервер дал бы слоту с нормой вторую строку —
// и какая из двух цифр окажется на экране, зависело бы от порядка склейки у КАЖДОГО сегодняшнего
// клиента. Отдельное поле снимает вопрос: кто про него не знает, тень не видит.
func TestShadowRidesItsOwnWireField(t *testing.T) {
	refs := techCardColorwayRefsToPb(shadowCard(), nil, CostingFx{})
	require.Len(t, refs, 1)

	require.Empty(t, refs[0].GetAreaEstimates(),
		"активный список обязан остаться пустым: у слота есть авторская норма")

	shadow := refs[0].GetShadowAreaEstimates()
	require.Len(t, shadow, 1)
	require.Equal(t, "BOMKEY1", shadow[0].GetBomLineKey(), "ключ склейки с авторской строкой рецепта")
	require.Equal(t, "m", shadow[0].GetUnit())
	require.Empty(t, shadow[0].GetRefusal())
	per, err := nullDecimalFromPb(shadow[0].GetPerGarment())
	require.NoError(t, err)
	require.True(t, per.Valid)
	require.Equal(t, "1.25", per.Decimal.String())
	// Провенанс замера едет ровно как у активной ступени — это то же сообщение, а не урезанное.
	require.Equal(t, "kate", shadow[0].GetParsedBy())
	require.Equal(t, "1", shadow[0].GetContourLayer())
	require.EqualValues(t, 1, shadow[0].GetPieceCount())
}

// TestShadowRefusesWithTheSameVocabulary: у тени нет своего словаря причин.
//
// Устаревшие площади — та же areas_stale, что и на активной ступени, и с тем же следствием: числа
// нет. Одновременно тест требует, чтобы этот отказ НИЧЕГО не сломал: цена карточки прежняя, чек-лист
// релиза молчит (слот стоит по авторской норме, а не по геометрии), посев открыт.
func TestShadowRefusesWithTheSameVocabulary(t *testing.T) {
	tc := shadowCard()
	sc := tc.PieceAreaScopes["BOMKEY1"]
	sc.Stale = true
	tc.PieceAreaScopes["BOMKEY1"] = sc

	refs := techCardColorwayRefsToPb(tc, nil, CostingFx{})
	require.Len(t, refs, 1)
	shadow := refs[0].GetShadowAreaEstimates()
	require.Len(t, shadow, 1)
	require.Equal(t, string(entity.AreaEstimateStale), shadow[0].GetRefusal())
	require.Equal(t, entity.AreaEstimateRefusalText(entity.AreaEstimateStale), shadow[0].GetRefusalText())
	require.Nil(t, shadow[0].GetPerGarment(), "отказ и число вместе не едут")
	require.True(t, shadow[0].GetStale())

	cc := colorwayCost(tc, &tc.Colorways[0], tc.BomItems, tc.LinkedMaterials, "EUR", tc.CostingBasis(), CostingFx{})
	require.Equal(t, "200", cc.materialsPerUnit.String())
	require.False(t, cc.hasUnpriced,
		"отказ ТЕНИ не имеет права объявлять итог неполным: материал в нём есть, по авторской норме")
	require.False(t, cc.hasEstimate)

	require.Empty(t, TechCardCostBlockers(tc, CostingFx{}),
		"чек-лист готовности к релизу называет слоты, из-за которых цена НЕ считается; этот считается")

	unit, _ := ComputeTechCardUnitCost(tc, CostingFx{})
	require.True(t, unit.Valid, "отказ тени заблокировал посев product.cost_price")
}

// TestShadowIsNotANormInAFrozenRelease: слепок релиза морозит ВЕСЬ ответ чтения, значит и тень.
// Через год кто-то откроет этот блоб — и обязан получить те же деньги, что и без тени.
//
// Держится это не на осторожности читателей, а на построении: костинг-проекция снапшота
// (releaseCostingCardFromSnapshot) собирает нормы ТОЛЬКО из usages и не переносит piece_area_scopes
// вовсе, поэтому по замороженной карточке оценка — ни активная, ни теневая — не считается в принципе.
func TestShadowIsNotANormInAFrozenRelease(t *testing.T) {
	tc := shadowCard()
	tc.Costing.CmtCost = decimal.NullDecimal{Decimal: decimal.RequireFromString("10"), Valid: true}

	snap := releaseBlob(t, tc)
	require.Len(t, snap.GetColorways(), 1)
	require.Len(t, snap.GetColorways()[0].GetShadowAreaEstimates(), 1,
		"тень действительно вморожена — иначе тест ниже доказывает пустоту")

	costs := ReleaseFrozenColorwayCosts(snap)
	require.NotNil(t, costs)
	require.Equal(t, "EUR", costs.Currency)
	require.Equal(t, "210", costs.UnitCostByColorway[35].String(),
		"замороженная цена — авторская норма плюс ручные статьи; тень в неё не входит")

	frozen := releaseCostingCardFromSnapshot(snap)
	require.NotNil(t, frozen)
	require.Empty(t, frozen.PieceAreaScopes,
		"костинг-проекция снапшота не несёт площадей: по ней оценка не воспроизводима ни в какой ступени")
	// Ни одна тень по замороженной карточке НЕ ВЫВОДИТСЯ — и слава богу: это была бы оценка по
	// СЕГОДНЯШНЕЙ геометрии под датой релиза. Проверяется именно это, а не пустота списка: с тех пор
	// как проекция снапшота стала переносить section (W3, без неё не работала граница рулонных
	// секций), рулонный слот попадает в перебор и возвращает ОТКАЗ. Отказ — это запись без нормы,
	// без денег и с названной причиной; раньше список был пуст лишь потому, что секция терялась, то
	// есть тест держался за симптом чужого дефекта.
	for _, sh := range colorwayShadowAreaEstimates(frozen, &frozen.Colorways[0], frozen.BomItems,
		frozen.LinkedMaterials, frozen.CostingBasis(), "EUR") {
		require.False(t, sh.ok, "тень посчиталась по замороженной карточке: %+v", sh)
		require.True(t, sh.money.IsZero(), "у отказа не может быть денег: %+v", sh)
		require.NotEmpty(t, sh.refusal, "отказ обязан называть причину, иначе он неотличим от бага")
	}
}

// TestShadowNeverReachesTheMaterialPlan: закупка не видит тень ни при каком раскладе.
//
// План материалов зовёт slotAreaEstimate РОВНО в одном месте — на слоте, которого рецепт колорвея
// не касается вовсе (там оценка выбирает ФОРМУЛИРОВКУ блокера, а не потребность). Слот с авторской
// нормой — это в точности слот, который рецепт касается, поэтому теневая популяция и популяция того
// вызова не пересекаются по построению. Тест фиксирует следствие: строка потребности считается по
// авторской норме, а блокера «есть только оценка» на этом слоте нет.
func TestShadowNeverReachesTheMaterialPlan(t *testing.T) {
	tc := shadowCard()
	tc.BomItems[0].MaterialId = sql.NullInt64{Int64: 800, Valid: true}
	tc.LinkedMaterials = map[int]entity.MaterialWithPrice{800: {}}

	run := &entity.ProductionRun{Id: 1, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: tc.Id,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 35, Valid: true}, SizeId: 4, PlannedQty: 10},
		},
	}}
	plan := ComputeProductionRunMaterialPlan(run, tc, nil, nil, tc.LinkedMaterials, nil)
	require.NotNil(t, plan)

	for _, bl := range plan.GetBlockers() {
		require.NotEqual(t, entity.MaterialPlanReasonEstimateOnly, bl.GetReason(),
			"слот с авторской нормой не имеет права объявляться «есть только оценка по площади»")
	}
	var required string
	for _, r := range plan.GetRows() {
		if r.GetMaterialId() == 800 {
			required = r.GetRequired().GetValue()
		}
	}
	require.Equal(t, "20", required, "10 изделий × авторские 2 м — тень (1.25) на закупку не влияет")
}
