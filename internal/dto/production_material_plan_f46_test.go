package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Ф4.6 — ПОТРЕБНОСТЬ ПЕРЕКЛЮЧАЕТСЯ С НОРМЫ НА НАСТИЛЫ, С ПОДПИСАННЫМ ИСТОЧНИКОМ.
//
// Every test here pins one sentence of §7 and each is named after the sentence rather than after the
// function, because the thing under test is the RULE: mutate the rule in the source and exactly one
// named test must go red.

const (
	f46Fabric  = 501 // slot: основная ткань, article 100
	f46Lining  = 502 // slot: подкладка, article 200
	f46Colorwy = 55  // colourway = product id, the identity a run line and a настил share
)

// f46Card is a two-cloth card: основная ткань and подкладка, one colourway, both consumed by a
// manual norm with a 5% BOM wastage estimate.
func f46Card() *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{
			Id: f46Fabric, Name: "Основная ткань", Section: entity.BomSectionFabric,
			MaterialId:     sql.NullInt64{Int64: 100, Valid: true},
			Unit:           sql.NullString{String: "m", Valid: true},
			WastagePercent: nd2("5"),
		},
		{
			Id: f46Lining, Name: "Подкладка", Section: entity.BomSectionLining,
			MaterialId:     sql.NullInt64{Int64: 200, Valid: true},
			Unit:           sql.NullString{String: "m", Valid: true},
			WastagePercent: nd2("5"),
		},
	}
	card.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: f46Colorwy, Valid: true},
		Usages: []entity.TechCardColorwayUsage{
			{
				BomItemId:         sql.NullInt64{Int64: f46Fabric, Valid: true},
				Consumption:       nd2("2"),
				ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
			},
			{
				BomItemId:         sql.NullInt64{Int64: f46Lining, Valid: true},
				Consumption:       nd2("1.5"),
				ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
			},
		},
	}}
	return card
}

func f46Run(qty int) *entity.ProductionRun {
	return &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: f46Colorwy, Valid: true}, SizeId: 1, PlannedQty: qty},
		},
	}}
}

func f46Section(markerID, plies int, usedLengthCm string) entity.ProductionRunLaySection {
	s := entity.ProductionRunLaySection{
		MarkerId: markerID, Plies: plies, MarkerName: "раскладка", SectionKey: "SEC",
	}
	if usedLengthCm != "" {
		s.MarkerUsedLengthCm = nd2(usedLengthCm)
	}
	return s
}

func f46Lay(bomItemID int64, name, endLossCm string, secs ...entity.ProductionRunLaySection) entity.ProductionRunLay {
	return entity.ProductionRunLay{
		Id: 1, RunId: 9, LayKey: "LAY" + name, ColorwayId: f46Colorwy,
		BomItemId:  sql.NullInt64{Int64: bomItemID, Valid: bomItemID > 0},
		BomLineKey: "BOMKEY" + name, Name: name,
		Mode: entity.ProductionLayModeFaceUp, EndLossCm: d(endLossCm),
		Sections: secs,
	}
}

func f46Articles() map[int]entity.MaterialWithPrice {
	return map[int]entity.MaterialWithPrice{
		100: {Material: entity.Material{Id: 100, MaterialInsert: entity.MaterialInsert{
			Name: "Cotton twill", Unit: sql.NullString{String: "m", Valid: true},
		}}},
		200: {Material: entity.Material{Id: 200, MaterialInsert: entity.MaterialInsert{
			Name: "Viscose lining", Unit: sql.NullString{String: "m", Valid: true},
		}}},
	}
}

func f46RowByMaterial(t *testing.T, resp *pb_admin.GetProductionRunMaterialPlanResponse, mid int32) *pb_admin.MaterialPlanRow {
	t.Helper()
	for _, r := range resp.Rows {
		if r.MaterialId == mid {
			return r
		}
	}
	t.Fatalf("no row for material %d", mid)
	return nil
}

func f46ContribBySlot(t *testing.T, resp *pb_admin.GetProductionRunMaterialPlanResponse, slot int64) *pb_admin.MaterialPlanContribution {
	t.Helper()
	for _, c := range resp.Contributions {
		if c.BomItemId == slot {
			return c
		}
	}
	t.Fatalf("no contribution for slot %d", slot)
	return nil
}

// §7.3 — ПЕРЕКЛЮЧЕНИЕ ПО ПАРЕ, А НЕ ПО ПРОГОНУ. Это приёмочный тест фазы.
//
// Прогон на две ткани: основная настелена, подкладка ещё нет. Подкладка ОБЯЗАНА продолжать считаться
// по норме — переключение «всё или ничего» обнулило бы её, и это была бы та же ложь наоборот.
func TestPlanLaysSwitchPerPairAndTheLiningKeepsItsNorm(t *testing.T) {
	card, run := f46Card(), f46Run(100)
	// 20 слоёв × 3 м под лекалами + 2 см на каждый конец каждого слоя.
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, f46Articles(), lays)

	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED,
		resp.PlanSource, "одна пара по настилам, другая по норме — ответ MIXED, а не молчаливый выбор одного источника")

	fabric := f46RowByMaterial(t, resp, 100)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, fabric.Source)
	// 20 × 300 = 6000 см ткани + 2 × 2 × 20 = 80 см концевых = 6080 см = 60.8 м.
	require.Equal(t, "60.8", fabric.Required.Value)

	lining := f46RowByMaterial(t, resp, 200)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, lining.Source)
	require.Equal(t, "157.5", lining.Required.Value, "1.5 × 100 × 1.05 — подкладка считается по норме, как и раньше")
	require.True(t, d(lining.Required.Value).IsPositive(), "подкладка НЕ обнуляется настилом соседнего слота")

	fc := f46ContribBySlot(t, resp, f46Fabric)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, fc.Source)
	require.Equal(t, "6000", fc.LayClothLengthCm.Value)
	require.Equal(t, "80", fc.LayEndLossCm.Value)
	require.Equal(t, "60.8", fc.Required.Value)
	// §7.3: RequiredBeforeGrossup на пути LAYS = raw(C,B), то есть план настила ДО наценки. Это то
	// самое число, против которого Ф5б.2 сравнивает ФАКТ, и ноль вместо него оставил бы ту фазу без
	// базы сравнения — тихо, потому что строка артикула своё «до» всё равно показывает.
	require.Equal(t, "60.8", fc.RequiredBeforeGrossup.Value)

	lc := f46ContribBySlot(t, resp, f46Lining)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, lc.Source)
	require.Equal(t, "0", lc.LayClothLengthCm.Value, "у нормы настильного разложения нет, и это ноль, а не тишина")
	require.Equal(t, "0", lc.LayEndLossCm.Value)
}

// §7.3 — plan_source читается по ПАРАМ, а не по наличию настилов где-нибудь в прогоне: когда
// настелены ОБЕ ткани, ответ целиком LAYS; когда ни одной — NORM.
func TestPlanSourceIsLaysOnlyWhenEveryCountedPairIsLaid(t *testing.T) {
	card := f46Card()

	t.Run("ни одного настила — NORM", func(t *testing.T) {
		resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(), nil)
		require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, resp.PlanSource)
		for _, c := range resp.Contributions {
			require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, c.Source)
		}
	})

	t.Run("обе пары настелены — LAYS", func(t *testing.T) {
		lays := []entity.ProductionRunLay{
			f46Lay(f46Fabric, "основная", "2", f46Section(9001, 20, "300")),
			f46Lay(f46Lining, "подкладка", "0", f46Section(9002, 10, "250")),
		}
		resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(), lays)
		require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, resp.PlanSource)
		require.Equal(t, "25", f46RowByMaterial(t, resp, 200).Required.Value, "10 × 250 см = 25 м, концевых потерь нет")
	})
}

// Р4 — КОЭФФИЦИЕНТ РАСКРОЯ НА ПУТИ НАСТИЛОВ НЕ ПРИМЕНЯЕТСЯ. Настил — измерение (длина × слои +
// концевые потери), а коэффициент откалиброван против нормативных ОЦЕНОК: наложить его сверху значит
// дважды учесть одно и то же на неизвестную величину.
func TestPlanLaysDoNotTakeTheCuttingCoefficient(t *testing.T) {
	card, run := f46Card(), f46Run(100)
	articles := f46Articles()
	m := articles[100]
	m.CuttingCoefficient = nd2("1.06")
	articles[100] = m
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, articles, lays)

	row := f46RowByMaterial(t, resp, 100)
	require.Equal(t, "60.8", row.Required.Value, "60.8 × 1.06 = 64.448 — коэффициент к измерению не применяется")
	require.Equal(t, "60.8", row.RequiredBeforeGrossup.Value, "на пути настилов наценки нет вовсе, значит до и после совпадают")
	require.Equal(t, "1.06", row.CuttingCoefficient.Value, "коэффициент артикула всё равно показан — он существует")

	// И он не молчит о том, ПОЧЕМУ не сработал, и не врёт ни про «ручные нормы», ни про то, что
	// какая-то часть строки посчитана иначе: у этой строки ВСЯ потребность из настилов.
	require.True(t, hasCaveat(resp.Caveats, "потребность этой строки посчитана по НАСТИЛАМ"),
		"оговорка обязана назвать настилы причиной, а не выдумать ручные нормы")
	require.True(t, hasCaveat(resp.Caveats, "к измерению он не применяется"))
	require.False(t, hasCaveat(resp.Caveats, "norms for it are manual"),
		"«ваши нормы ручные» — неверное объяснение верного числа")
	require.False(t, hasCaveat(resp.Caveats, "часть потребности"),
		"«часть по настилам» — тоже неверное объяснение: по настилам посчитана вся строка")
}

// Та же оговорка на строке, собравшей ОБА источника, обязана звучать иначе: здесь по настилам
// действительно только ЧАСТЬ, и сказать «вся строка измерена» было бы вторым неверным объяснением
// верного числа.
func TestPlanMixedRowExplainsTheCoefficientDifferently(t *testing.T) {
	card := f46Card()
	card.BomItems[1].MaterialId = sql.NullInt64{Int64: 100, Valid: true} // один артикул на оба слота
	articles := f46Articles()
	m := articles[100]
	m.CuttingCoefficient = nd2("1.06")
	articles[100] = m

	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, articles,
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))})

	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED, resp.Rows[0].Source)
	require.True(t, hasCaveat(resp.Caveats, "часть потребности посчитана по НАСТИЛАМ"))
	require.False(t, hasCaveat(resp.Caveats, "потребность этой строки посчитана по НАСТИЛАМ"))
}

// Обратная половина Р4: на пути НОРМЫ коэффициент применяется как и раньше. Один прогон, два
// артикула — один настелен, другой считается по маркерной норме, — и наценка достаётся ровно
// второму.
func TestPlanNormPathStillTakesTheCoefficientWhileLaysDoNot(t *testing.T) {
	card, run := f46Card(), f46Run(100)
	// Подкладка считается по МАРКЕРНОЙ норме, значит её наценка — коэффициент артикула.
	card.Colorways[0].Usages[1].ConsumptionSource = sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true}
	articles := f46Articles()
	for _, id := range []int{100, 200} {
		m := articles[id]
		m.CuttingCoefficient = nd2("1.10")
		articles[id] = m
	}
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, articles, lays)

	require.Equal(t, "60.8", f46RowByMaterial(t, resp, 100).Required.Value, "настил: без коэффициента")
	require.Equal(t, "165", f46RowByMaterial(t, resp, 200).Required.Value, "1.5 × 100 × 1.10 — норма: с коэффициентом")
}

// §7.2 — КОНЦЕВЫЕ ПОТЕРИ: на ОДИН конец ОДНОГО слоя, полные = 2 × end_loss_cm × Σ слоёв.
//
// Определение выведено АРИФМЕТИКОЙ, а не текстом плана. Из двух калибровок модели («20 слоёв × 3 м ⇒
// ~1.3%» и «1 слой × 30 м ⇒ 0.02%») одновременно выполнима только первая, и она пиннится здесь:
// 20 × 2 × 2 = 80 см на 6000 см = 1.333%. Вторая калибровка при том же определении даёт 0.13%, и
// признана опечаткой — этот тест фиксирует, какая из двух победила.
func TestPlanEndLossIsTwoEndsOfEveryPly(t *testing.T) {
	card := f46Card()

	twentyPlies := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "короткий", "2", f46Section(9001, 20, "300"))})
	cloth, total := d("60"), d(f46RowByMaterial(t, twentyPlies, 100).Required.Value)
	require.Equal(t, "60.8", total.String())
	ratio := total.Sub(cloth).Div(cloth).Mul(decimal.NewFromInt(100))
	require.Equal(t, "1.3333", ratio.Round(4).String(), "калибровка «20 слоёв × 3 м ⇒ ~1.3%» — та, что выиграла")

	onePly := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "длинный", "2", f46Section(9001, 1, "3000"))})
	require.Equal(t, "30.04", f46RowByMaterial(t, onePly, 100).Required.Value,
		"1 слой × 30 м даёт 0.13%, а не 0.02% — вторая калибровка модели арифметически невозможна")

	// Потери НА КАЖДЫЙ слой, а не на настил: удвоение слоёв удваивает и потери.
	forty := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "глубокий", "2", f46Section(9001, 40, "300"))})
	fc := f46ContribBySlot(t, forty, f46Fabric)
	require.Equal(t, "160", fc.LayEndLossCm.Value, "2 × 2 × 40")
}

// §7.2 — ступенчатый настил считается по слоям СЕКЦИЙ, то есть консервативно (верхняя оценка), и
// длины секций складываются каждая со своим числом слоёв.
func TestPlanStepLayCountsSectionPlies(t *testing.T) {
	card := f46Card()
	lay := f46Lay(f46Fabric, "ступенчатый", "2",
		f46Section(9001, 20, "300"),
		f46Section(9002, 5, "120"))

	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{lay})

	fc := f46ContribBySlot(t, resp, f46Fabric)
	require.Equal(t, "6600", fc.LayClothLengthCm.Value, "20 × 300 + 5 × 120")
	require.Equal(t, "100", fc.LayEndLossCm.Value, "2 × 2 × (20 + 5) — по слоям секций, консервативно")
	require.Equal(t, "67", f46RowByMaterial(t, resp, 100).Required.Value)
}

// §7.1 — потребность пары это СУММА по её настилам: два настила на одну пару складываются, а не
// побеждает последний.
func TestPlanSumsEveryLayOfThePair(t *testing.T) {
	card := f46Card()
	lays := []entity.ProductionRunLay{
		f46Lay(f46Fabric, "первый", "2", f46Section(9001, 20, "300")),
		f46Lay(f46Fabric, "второй", "0", f46Section(9002, 10, "200")),
	}
	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(), lays)
	require.Equal(t, "80.8", f46RowByMaterial(t, resp, 100).Required.Value, "60.8 + 20")
}

// §14 п.6 / правило 5 — НАСТИЛ, ПОТЕРЯВШИЙ СЛОТ (fk_prlay_bom = SET NULL), выпадает из потребности
// С ЯВНОЙ НАХОДКОЙ и никогда не молчит: он умеет назвать пропавший слот через снимок bom_line_key.
// Пара при этом НЕ считается настеленной, то есть продолжает считаться по норме.
func TestPlanBrokenLayIsNamedAndNeverSilent(t *testing.T) {
	card := f46Card()
	broken := f46Lay(0, "осиротевший", "2", f46Section(9001, 20, "300"))
	broken.BomLineKey = "01J0SLOTKEY0000000000000AA"

	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{broken})

	require.True(t, hasCaveat(resp.Caveats, "01J0SLOTKEY0000000000000AA"),
		"находка обязана НАЗВАТЬ пропавший слот снимком bom_line_key")
	require.True(t, hasCaveat(resp.Caveats, "осиротевший"), "и назвать сам настил")
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, resp.PlanSource,
		"сломанный настил не делает пару настеленной")
	require.Equal(t, "210", f46RowByMaterial(t, resp, 100).Required.Value,
		"пара продолжает считаться по норме: 2 × 100 × 1.05")
}

// Правило 4 / §14 п.5 — РЕЗОЛВ СЛОТА ТОЛЬКО ЧЕРЕЗ ОБЩИЙ РЕЗОЛВЕР, НИКОГДА ПО ПОЗИЦИИ.
// `tech_card_piece_material.bom_item_index` и `usage.bom_item_index` позиционны (0109:39), и резолв
// только по позиции УЖЕ давал ПУСТОЙ материал-план на бете.
//
// Здесь у рецепта FK смотрит на ткань (id 501), а СТАРЫЙ позиционный индекс — на подкладку (позиция
// 1). Если бы настильный путь искал рецепт по позиции, он не нашёл бы пин ткани и отнёс бы метраж на
// слотовый артикул по умолчанию.
func TestPlanLayArticleComesFromTheSharedResolverNotThePosition(t *testing.T) {
	card, run := f46Card(), f46Run(100)
	u := &card.Colorways[0].Usages[0]
	u.BomItemIndex = sql.NullInt32{Int32: 1, Valid: true} // ← позиция подкладки, устаревшая
	u.MaterialId = sql.NullInt64{Int64: 777, Valid: true} // ← пин колорвея на другой артикул
	articles := f46Articles()
	articles[777] = entity.MaterialWithPrice{Material: entity.Material{Id: 777, MaterialInsert: entity.MaterialInsert{
		Name: "Pinned twill", Unit: sql.NullString{String: "m", Valid: true},
	}}}
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, articles, lays)

	fc := f46ContribBySlot(t, resp, f46Fabric)
	require.Equal(t, int32(777), fc.MaterialId, "метраж настила лёг на ПИН колорвея, найденный по FK")
	require.True(t, fc.Pinned, "и он помечен как пин, а не как слотовый умолчальный артикул")
	require.Equal(t, "60.8", f46RowByMaterial(t, resp, 777).Required.Value)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS,
		f46RowByMaterial(t, resp, 777).Source)
}

// §7.1 — процент отхода на пути LAYS НЕ ПРИМЕНЯЕТСЯ, и молчаливый no-op недопустим: оператор ввёл
// «фактический процент отхода» на прогоне и обязан прочитать, почему он ничего не изменил.
func TestPlanLaysDisplaceTheWastagePercentOutLoud(t *testing.T) {
	card := f46Card()
	run := f46Run(100)
	run.ActualWastagePercent = nd2("8")
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, f46Articles(), lays)

	require.Equal(t, "60.8", f46RowByMaterial(t, resp, 100).Required.Value, "8% к измерению не применяются")
	require.True(t, hasCaveat(resp.Caveats, "процент отхода прогона 8% не применён к слоту \"Основная ткань\""),
		"вытесненный процент отхода обязан быть назван по слоту")
	require.False(t, hasCaveat(resp.Caveats, "\"Подкладка\""),
		"подкладка считается по норме и её отход применён — про неё говорить нечего")
	require.Equal(t, "162", f46RowByMaterial(t, resp, 200).Required.Value, "1.5 × 100 × 1.08 — отход прогона перебивает BOM")
}

// Правило 6 — ИСТОЧНИК ПОДПИСАН НА КАЖДОМ ВКЛАДЕ, и вклад НИКОГДА не MIXED: вклад — это один слот
// одного колорвея, то есть ровно та пара, которую переключает §7.3. MIXED живёт только на строке
// артикула, собравшей два слота.
func TestPlanContributionSourceIsAlwaysPure(t *testing.T) {
	card := f46Card()
	// Один артикул на обоих слотах: строка соберёт настил и норму сразу.
	card.BomItems[1].MaterialId = sql.NullInt64{Int64: 100, Valid: true}
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(), lays)

	require.Len(t, resp.Rows, 1, "один артикул — одна строка, один запас")
	row := resp.Rows[0]
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED, row.Source,
		"артикул собрал слот с настилами и слот без них — это MIXED, а не молчаливый выбор одного")
	require.Equal(t, "218.3", row.Required.Value, "60.8 (настил) + 157.5 (норма)")

	for _, c := range resp.Contributions {
		require.NotEqual(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_MIXED, c.Source,
			"вклад — это один слот, ему нечем быть смешанным")
		require.NotEqual(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_UNSPECIFIED, c.Source,
			"вклад без подписи — то самое число, про которое нельзя сказать, откуда оно")
	}
}

// Секция, чья раскладка не знает своей длины, не может назвать метраж — и её слои НЕ исчезают молча:
// потребность, которая тихо стала меньше, читается как «ткани хватает» ровно там, где оптимизм
// недопустим.
func TestPlanUnmeasuredSectionIsNamed(t *testing.T) {
	card := f46Card()
	lay := f46Lay(f46Fabric, "настил-1", "2",
		f46Section(9001, 20, "300"),
		f46Section(9002, 5, "")) // длина раскладки не задана

	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{lay})

	require.True(t, hasCaveat(resp.Caveats, "не задана длина раскладки"),
		"неизмеримая секция обязана быть названа")
	fc := f46ContribBySlot(t, resp, f46Fabric)
	require.Equal(t, "6000", fc.LayClothLengthCm.Value, "в ткань вошла только измеренная секция")
	require.Equal(t, "100", fc.LayEndLossCm.Value,
		"а слои этой секции физически лежат и физически обрезаются с двух концов: 2 × 2 × (20 + 5)")
}

// Настил доказывает, что слот кроится этим колорвеем, даже если рецепт его не упоминает: блокер
// «нет нормы» на такой слот не ставится, а метраж ложится на слотовый артикул по умолчанию.
func TestPlanLaidSlotOutsideTheRecipeIsCountedNotBlocked(t *testing.T) {
	card, run := f46Card(), f46Run(100)
	card.Colorways[0].Usages = card.Colorways[0].Usages[:1] // рецепт знает только основную ткань
	lays := []entity.ProductionRunLay{f46Lay(f46Lining, "подкладка", "0", f46Section(9002, 10, "250"))}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, f46Articles(), lays)

	for _, b := range resp.Blockers {
		require.NotEqual(t, int64(f46Lining), b.BomItemId, "настеленный слот не «без нормы» — он измерен")
	}
	row := f46RowByMaterial(t, resp, 200)
	require.Equal(t, "25", row.Required.Value)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, row.Source)
	require.False(t, f46ContribBySlot(t, resp, f46Lining).Pinned, "артикул взят слотовым умолчанием, а не пином")
}

// Настил на слоте без артикула (ни пина, ни умолчания) — это блокер, а не тихий ноль: метраж есть,
// а положить его не на что.
func TestPlanLaidSlotWithoutArticleBlocks(t *testing.T) {
	card, run := f46Card(), f46Run(100)
	card.BomItems[0].MaterialId = sql.NullInt64{}
	card.Colorways[0].Usages[0].MaterialId = sql.NullInt64{}
	run.ActualWastagePercent = nd2("8")
	lays := []entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))}

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, f46Articles(), lays)

	require.False(t, hasCaveat(resp.Caveats, "не применён к слоту \"Основная ткань\""),
		"у заблокированной пары потребности нет вовсе — говорить, что к ней «не применён отход, потому что посчитано по настилам», значит описывать несуществующее число")

	found := false
	for _, b := range resp.Blockers {
		if b.BomItemId == f46Fabric && b.Key == entity.MaterialPlanBlockerNoArticle {
			found = true
		}
	}
	require.True(t, found, "настил без артикула обязан стать блокером")
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, resp.PlanSource,
		"ни один вклад не посчитан по настилам — подписывать ответ как LAYS не на чем")
}

// Настил, у которого нет ни одной секции, даёт НОЛЬ метража — и это опасное число, потому что норма
// к паре уже не применяется. Оно обязано быть названо.
func TestPlanEmptyLayZeroIsSpokenAloud(t *testing.T) {
	card := f46Card()
	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "пустой", "2")})

	require.Equal(t, "0", f46RowByMaterial(t, resp, 100).Required.Value)
	require.True(t, hasCaveat(resp.Caveats, "не дают длины"),
		"нулевая потребность по настилам — самое опасное число на странице, и оно обязано говорить")
}

// Настил измеряет ткань в МЕТРАХ, чем бы ни была специфицирована единица слота. Число под чужой
// подписью — это заказ на 18 000 бобин; здесь оно остаётся в метрах, а расхождение названо.
func TestPlanLayStaysInMetresWhenTheSlotUnitDisagrees(t *testing.T) {
	card := f46Card()
	card.BomItems[0].Unit = sql.NullString{String: "шт", Valid: true}
	articles := f46Articles()
	m := articles[100]
	m.Unit = sql.NullString{String: "шт", Valid: true}
	articles[100] = m

	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, articles,
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))})

	require.Equal(t, "m", f46ContribBySlot(t, resp, f46Fabric).Unit,
		"вклад подписан той единицей, в которой он ДЕЙСТВИТЕЛЬНО посчитан")
	require.True(t, hasCaveat(resp.Caveats, "настил измеряет ткань в метрах"))
}

// Конверсия в складскую единицу — ОДНА на оба пути: метры настила уходят в килограммы по полной
// ширине рулона ровно так же, как метры нормы.
func TestPlanLayMetresConvertToStockKilograms(t *testing.T) {
	card := f46Card()
	articles := f46Articles()
	m := articles[100]
	m.Unit = sql.NullString{String: "kg", Valid: true}
	m.FabricAttr = &entity.MaterialFabricAttr{WidthCm: nd2("150"), WeightGsm: nd2("220")}
	articles[100] = m

	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, nil, articles,
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))})

	row := f46RowByMaterial(t, resp, 100)
	require.Equal(t, "kg", row.Unit)
	// 60.8 м × 1.5 м × 220 г/м² = 20064 г = 20.064 кг
	require.Equal(t, "20.064", row.Required.Value)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, row.Source)
	require.Equal(t, "6000", f46ContribBySlot(t, resp, f46Fabric).LayClothLengthCm.Value,
		"разложение настила остаётся в САНТИМЕТРАХ ткани — это факт цеха, а не складская величина")
}

// Строка «выдано, но больше не требуется» не имеет потребности вовсе, значит и источника у неё нет:
// подписать её нормой значило бы утверждать, что какая-то норма её произвела.
func TestPlanIssuedOnlyRowHasNoSource(t *testing.T) {
	card := f46Card()
	issued := map[int]decimal.Decimal{999: d("3")}
	resp := ComputeProductionRunMaterialPlan(f46Run(100), card, nil, issued, f46Articles(), nil)

	row := f46RowByMaterial(t, resp, 999)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_UNSPECIFIED, row.Source)
	require.Equal(t, "0", row.Required.Value)
}

// Настил, построенный на колорвей, которого прогон больше не планирует (строку удалили), всё равно
// физически ляжет — его метраж считается, НО факт называется. Молча выкинуть его значило бы убрать
// из плана ткань, которую цех собирается стелить.
func TestPlanLayOnUnplannedColourwayIsCountedAndNamed(t *testing.T) {
	card := f46Card()
	run := f46Run(100)
	run.Lines[0].ProductId = sql.NullInt32{Int32: 66, Valid: true} // прогон планирует ДРУГОЙ цвет

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))})

	require.Equal(t, "60.8", f46RowByMaterial(t, resp, 100).Required.Value)
	require.True(t, hasCaveat(resp.Caveats, "которого прогон не планирует"),
		"расхождение настила с планом обязано быть названо, а не тихо посчитано или тихо выброшено")
}

// Пара, чей рецепт резолвится в ДВА разных артикула, называет это ОДИН раз, а не по разу на каждый
// размер прогона: цикл нормы обходит пару once per run line, и незаглушённая оговорка превратилась бы
// в пять одинаковых строк на пятиразмерном прогоне.
func TestPlanAmbiguousLaidPairArticleIsNamedOnce(t *testing.T) {
	card := f46Card()
	run := f46Run(50)
	run.Lines = append(run.Lines,
		entity.ProductionRunLine{ProductId: sql.NullInt32{Int32: f46Colorwy, Valid: true}, SizeId: 2, PlannedQty: 50},
		entity.ProductionRunLine{ProductId: sql.NullInt32{Int32: f46Colorwy, Valid: true}, SizeId: 3, PlannedQty: 50})
	// Второе использование того же слота с другим пином.
	card.Colorways[0].Usages = append(card.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId:   sql.NullInt64{Int64: f46Fabric, Valid: true},
		MaterialId:  sql.NullInt64{Int64: 777, Valid: true},
		Consumption: nd2("1"),
	})

	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, f46Articles(),
		[]entity.ProductionRunLay{f46Lay(f46Fabric, "настил-1", "2", f46Section(9001, 20, "300"))})

	n := 0
	for _, c := range resp.Caveats {
		if len(c) > 0 && hasCaveat([]string{c}, "резолвится в два разных артикула") {
			n++
		}
	}
	require.Equal(t, 1, n, "три размера — одна оговорка")
}

// planLayDemandByPair сама по себе: ключ — ПАРА, и два настила разных пар не сливаются.
func TestPlanLayDemandByPairKeysOnColourwayAndSlot(t *testing.T) {
	other := f46Lay(f46Fabric, "другой цвет", "0", f46Section(9003, 4, "100"))
	other.ColorwayId = 66
	demand, notes := planLayDemandByPair([]entity.ProductionRunLay{
		f46Lay(f46Fabric, "чёрный", "0", f46Section(9001, 10, "300")),
		other,
	})
	require.Empty(t, notes)
	require.Len(t, demand, 2, "один слот, два колорвея — две пары")
	require.Equal(t, "3000", demand[planLayPairKey{colorwayID: f46Colorwy, bomItemID: f46Fabric}].clothCm.String())
	require.Equal(t, "400", demand[planLayPairKey{colorwayID: 66, bomItemID: f46Fabric}].clothCm.String())
}
