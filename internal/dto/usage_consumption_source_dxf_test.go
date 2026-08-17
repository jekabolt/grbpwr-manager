package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// Третий источник нормы (0294): 'dxf' — площадь деталей выкройки ÷ раскройная ширина, NETTO.
//
// Тесты этого файла держат ровно те три утверждения, из-за которых источник вообще стоило заводить
// отдельным значением, а не «ручной нормой с пометкой»:
//   1. на проводе он принимается, но разложения отходов не несёт (оно про измеренную раскладку);
//   2. в деньгах он ГРОССИТСЯ процентом раскроя, побитово как ручной — netto иначе занижено;
//   3. в потребности прогона он берёт ТЕ ЖЕ множители, что и в деньгах (W3): процент раскроя за
//      геометрию настила и коэффициент артикула за реальность рулона.

func TestParseRecipeUsagesAcceptsDxfWithoutDecomposition(t *testing.T) {
	t.Run("dxf is a valid source", func(t *testing.T) {
		out, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("dxf")
		}))
		require.NoError(t, err)
		require.Equal(t, sql.NullString{String: entity.ConsumptionSourceDxf, Valid: true}, out[0].ConsumptionSource)
		require.False(t, out[0].WasteSelvedgePct.Valid)
		require.False(t, out[0].WasteCutPct.Valid)
	})
	t.Run("dxf with a waste decomposition is a provenance mismatch", func(t *testing.T) {
		// Разложение отходов описывает ИЗМЕРЕННУЮ раскладку: кромка и рез. У площади деталей нет ни
		// того, ни другого, и принять их означало бы напечатать на экране разбор отходов раскладки,
		// которой не было.
		_, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("dxf")
			u.WasteCutPct = &pb_decimal.Decimal{Value: "12"}
		}))
		require.Error(t, err)
	})
}

// Норма с выкроек гроссится, и это НЕ упущение: площадь деталей не содержит межлекальных выпадов,
// поэтому процент раскроя слота — единственное, что за них платит. Числа здесь те же, что у ручной
// нормы в TestUsageWastageGrossUpSkip, и это утверждение теста: костинг не различает эти два
// источника ни на копейку.
func TestDxfNormGrossesUpLikeManual(t *testing.T) {
	bom := &entity.TechCardBomItem{
		UnitPrice:      decimal.NewNullDecimal(decimal.RequireFromString("10")),
		WastagePercent: decimal.NewNullDecimal(decimal.RequireFromString("8")),
	}
	dxf := entity.TechCardColorwayUsage{
		Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("2.5")),
		ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceDxf, Valid: true},
	}
	got := dxf.LineTotal(bom)
	require.True(t, got.Valid)
	require.Equal(t, "27", got.Decimal.String(), "dxf netto keeps the 8% gross-up: 2.5×10×1.08")

	perSize := entity.TechCardColorwayUsage{
		ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceDxf, Valid: true},
		SizeConsumptions: []entity.TechCardBomSizeConsumption{
			{SizeId: 1, Consumption: decimal.RequireFromString("2")},
			{SizeId: 2, Consumption: decimal.RequireFromString("3")},
		},
	}
	rt := perSize.SizeRunTotal(bom, map[int]int{1: 10, 2: 10})
	require.True(t, rt.Valid)
	require.Equal(t, "540", rt.Decimal.String(), "dxf per-size run: (2+3)×10×10×1.08")

	// Партионная клетка — норма КОНКРЕТНОГО размера, тоже с гросс-апом.
	bs := perSize.SizeNormTotal(bom, 2)
	require.True(t, bs.Valid)
	require.Equal(t, "32.4", bs.Decimal.String(), "size 2: 3×10×1.08")

	// Себестоимость стиля — среднее по ряду, с тем же гросс-апом: (2+3)/2 × 10 × 1.08.
	avg := perSize.RangeAverageTotal(bom, []int{1, 2})
	require.True(t, avg.Valid)
	require.Equal(t, "27", avg.Decimal.String(), "range average: 2.5×10×1.08")

	// И ни один процент раскроя — ни одного умножения: слот без процента отдаёт чистое netto. Это
	// и есть та дыра, которую гейт готовности закрывает блокером (см. readiness-тесты ниже).
	noPct := &entity.TechCardBomItem{UnitPrice: bom.UnitPrice}
	require.Equal(t, "25", dxf.LineTotal(noPct).Decimal.String(),
		"wastage_percent NULL means applyWastage multiplies by nothing — netto reaches the money as a total")
}

// W3: норма с выкроек берёт ОБА множителя. Площадь деталей не содержит ни межлекальных выпадов
// (их оплачивает процент слота), ни усадки с пороками (их оплачивает коэффициент артикула) — то
// есть netto не содержит ВООБЩЕ НИЧЕГО, и оба множителя ложатся на неё без пересечения.
//
// Раньше здесь стоял обратный тест: коэффициент dxf-норму не трогал, а ответ объяснял, почему.
// Основание было в старом смысле процента (он оплачивал и усадку); W3 его снял.
func TestPlanDxfNormTakesBothWastageAndCoefficient(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceDxf, 100, "5")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil), nil)

	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	require.Equal(t, "222.6", row.Required.Value, "200 × 1.05 wastage × 1.06 coefficient")
	require.Equal(t, "200", row.RequiredBeforeGrossup.Value)
	require.False(t, hasCaveat(resp.Caveats, "cutting coefficient 1.06 not applied"),
		"the dial bit — there is no no-op to explain: %v", resp.Caveats)
}

// ПРОВЕНАНС НОРМЫ БОЛЬШЕ НЕ РЕШАЕТ, КУСАЕТ ЛИ КОЭФФИЦИЕНТ. Три источника, одно число: разойтись они
// могут только по ПРОЦЕНТУ (marker его не берёт), но коэффициент берут все три одинаково.
func TestPlanCoefficientBitesRegardlessOfNormProvenance(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		// manual/dxf: 200 × 1.05 × 1.06. marker: процента нет (длина раскладки его уже содержит),
		// значит 200 × 1.06 — но коэффициент на месте во всех трёх.
		{entity.ConsumptionSourceManual, "222.6"},
		{entity.ConsumptionSourceDxf, "222.6"},
		{entity.ConsumptionSourceMarker, "212"},
	} {
		run, card := planFixture("m", "2", tc.src, 100, "5")
		resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil), nil)
		require.Equal(t, tc.want, resp.Rows[0].Required.Value, "source %q", tc.src)
		require.False(t, hasCaveat(resp.Caveats, "cutting coefficient 1.06 not applied"),
			"source %q: the coefficient bit, so nothing is explained away: %v", tc.src, resp.Caveats)
	}
}

// ГЕЙТ ГОТОВНОСТИ. Норма с выкроек — не «введена руками», и текст обязан это говорить: гейт —
// единственное место, где оператор узнаёт, чему верить.
func TestRunReadinessDxfNormIsItsOwnAnswerNotManual(t *testing.T) {
	card := rrHealthyCard()
	card.BomItems[0].WastagePercent = rrNullDec("12")
	card.Colorways[0].Usages[0].ConsumptionSource = sql.NullString{
		String: entity.ConsumptionSourceDxf, Valid: true}

	res := ComputeProductionRunReadiness(rrInput(card))
	f, ok := rrFind(res, entity.RunReadinessKeyNormProvenance)
	require.True(t, ok)
	require.Equal(t, entity.RunReadinessWarning, f.Severity,
		"a pattern-derived norm is accepted — выкройки есть раньше раскладки")
	require.True(t, res.Report.Ready(), "it must not refuse a run; blockers: %v", res.Report.Blockers())
	require.Contains(t, f.Detail, "taken from the patterns")
	require.Contains(t, f.Detail, "12%", "the percentage that pays for the waste must be named")
	require.NotContains(t, f.Detail, "entered by hand")

	// Раскладки за такой нормой нет, поэтому пять условий съёмки не имеют предмета — тот же
	// инвариант 1, что у ручной нормы: один факт краснеет ОДИН раз.
	for _, k := range []string{
		entity.RunReadinessKeyNormConditionsRecorded, entity.RunReadinessKeyNormSeamAllowance,
		entity.RunReadinessKeyNormFlipPolicy, entity.RunReadinessKeyNormPieceSet,
		entity.RunReadinessKeyNormWidthVsArticle, entity.RunReadinessKeyNormMultiple,
	} {
		_, emitted := rrFind(res, k)
		require.False(t, emitted, "%q must not be emitted when there is no раскладка to judge", k)
	}
}

// Пара (норма с выкроек + процент раскроя не задан) — BLOCKER. Netto без процента уходит в закупку
// заведомо занижённым, и это знаемая величина, а не неизвестная: отсутствие здесь и есть ответ.
func TestRunReadinessDxfNormWithoutWastagePercentBlocks(t *testing.T) {
	card := rrHealthyCard()
	card.BomItems[0].WastagePercent = decimal.NullDecimal{}
	card.Colorways[0].Usages[0].ConsumptionSource = sql.NullString{
		String: entity.ConsumptionSourceDxf, Valid: true}

	res := ComputeProductionRunReadiness(rrInput(card))
	f, ok := rrFind(res, entity.RunReadinessKeyNormProvenance)
	require.True(t, ok)
	require.Equal(t, entity.RunReadinessBlocker, f.Severity)
	require.Contains(t, f.Detail, "NOT SET")
	blockers := res.Report.Blockers()
	require.Len(t, blockers, 1, "one fact goes red once, got %v", blockers)
	require.Equal(t, entity.RunReadinessKeyNormProvenance, blockers[0].Key)
}

// Ноль — это ЗАЯВЛЕННЫЙ ноль, а не молчание: слот, где кто-то сознательно написал 0% раскроя,
// прогон не останавливает. Разница между NULL и 0 здесь единственная, что отделяет «никто не
// сказал» от «сказали, что отходов нет».
func TestRunReadinessDxfNormWithZeroWastagePercentIsDeclaredNotMissing(t *testing.T) {
	card := rrHealthyCard()
	card.BomItems[0].WastagePercent = rrNullDec("0")
	card.Colorways[0].Usages[0].ConsumptionSource = sql.NullString{
		String: entity.ConsumptionSourceDxf, Valid: true}

	res := ComputeProductionRunReadiness(rrInput(card))
	f, _ := rrFind(res, entity.RunReadinessKeyNormProvenance)
	require.Equal(t, entity.RunReadinessWarning, f.Severity)
	require.True(t, res.Report.Ready(), "blockers: %v", res.Report.Blockers())
}

// ФОРМА СТРОКИ, КОТОРУЮ 'dxf' ОБЯЗАН ОТКАЗАТЬ. Источник утверждает «это измеренная норма по
// площадям», и утверждение опровержимо самой строкой. Обе формы ниже РАСКАЛЫВАЮТ костинг и закупку:
// LineTotal читает Quantity первым (штук × цена, без гросс-апа), а usageNormForSize в плане берёт
// расход — одна строка замораживает дешёвую себестоимость и резервирует ткань по норме.
func TestParseRecipeUsagesRefusesDxfWithCountableOrNoNorm(t *testing.T) {
	t.Run("dxf with a countable quantity is refused", func(t *testing.T) {
		_, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("dxf")
			u.Quantity = &pb_decimal.Decimal{Value: "4"}
		}))
		require.Error(t, err)
	})
	t.Run("dxf without any norm is refused", func(t *testing.T) {
		_, err := ParseRecipeUsages([]*pb_common.TechCardColorwayUsage{{
			BomLineKey:        "RK1",
			ConsumptionSource: strPtr("dxf"),
		}})
		require.Error(t, err)
	})
	t.Run("dxf on a per-size norm alone is fine", func(t *testing.T) {
		out, err := ParseRecipeUsages([]*pb_common.TechCardColorwayUsage{{
			BomLineKey:        "RK1",
			ConsumptionSource: strPtr("dxf"),
			SizeConsumptions: []*pb_common.TechCardBomSizeConsumption{
				{SizeId: 1, Consumption: &pb_decimal.Decimal{Value: "1.4"}},
			},
		}})
		require.NoError(t, err)
		require.Equal(t, entity.ConsumptionSourceDxf, out[0].ConsumptionSource.String)
	})
	t.Run("the same shapes stay ACCEPTED for marker and manual", func(t *testing.T) {
		// Запрет нарочно не распространён на прежние источники: строки со счётным количеством И
		// расходом лежат в базе с 0079, и отказ ударил бы по сохранению карточки, которую сегодня
		// открывают без правок.
		for _, src := range []string{"marker", "manual"} {
			_, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
				u.ConsumptionSource = strPtr(src)
				u.Quantity = &pb_decimal.Decimal{Value: "4"}
			}))
			require.NoError(t, err, "source %q must keep its legacy tolerance", src)
		}
	})
}

// Строка с источником dxf, но БЕЗ числа, не имеет права краснеть здесь: «нет нормы» уже краснеет в
// slot_norm, который считает материальный план. Один факт — один красный.
func TestRunReadinessDxfWithoutNormDoesNotDoubleReport(t *testing.T) {
	card := rrHealthyCard()
	card.BomItems[0].WastagePercent = decimal.NullDecimal{}
	card.Colorways[0].Usages[0].Consumption = decimal.NullDecimal{}
	card.Colorways[0].Usages[0].SizeConsumptions = nil
	card.Colorways[0].Usages[0].ConsumptionSource = sql.NullString{
		String: entity.ConsumptionSourceDxf, Valid: true}

	res := ComputeProductionRunReadiness(rrInput(card))
	f, ok := rrFind(res, entity.RunReadinessKeyNormProvenance)
	require.True(t, ok)
	require.Equal(t, entity.RunReadinessWarning, f.Severity,
		"the missing norm is slot_norm's red, not this row's")
	require.NotContains(t, f.Detail, "not set%", "the percentage must not be printed as a bare zero")
	blockers := res.Report.Blockers()
	require.Len(t, blockers, 1, "exactly one red about one missing norm, got %v", blockers)
	require.Equal(t, entity.RunReadinessKeySlotNorm, blockers[0].Key)
}

// ФАКТИЧЕСКИЙ ПРОЦЕНТ ПРОГОНА — ТОЖЕ ЗАЯВЛЕННЫЙ ПРОЦЕНТ. План на перепроверке живого прогона
// подставляет его ВМЕСТО слотового, даже когда слотовый NULL, — значит потребность там гроссится
// полностью, и блокер «занижены» был бы про этот прогон ложью.
func TestRunReadinessDxfNormAcceptsTheRunsActualWastagePercent(t *testing.T) {
	card := rrHealthyCard()
	card.BomItems[0].WastagePercent = decimal.NullDecimal{}
	card.Colorways[0].Usages[0].ConsumptionSource = sql.NullString{
		String: entity.ConsumptionSourceDxf, Valid: true}

	in := rrInput(card)
	in.ActualWastagePercent = rrNullDec("9")
	res := ComputeProductionRunReadiness(in)
	f, _ := rrFind(res, entity.RunReadinessKeyNormProvenance)
	require.Equal(t, entity.RunReadinessWarning, f.Severity,
		"the run declares 9%% — nothing is understated, so nothing is blocked")
	require.True(t, res.Report.Ready(), "blockers: %v", res.Report.Blockers())
	require.Contains(t, f.Detail, "9%")
	require.Contains(t, f.Detail, "the run's actual percent")
}

// Фраза кавеата собирается из ПРИСУТСТВУЮЩИХ причин. С W3 причин ровно три — настилы, счётность,
// нерулонная секция, — и источник нормы среди них не числится. Пинятся все сочетания, а не одно:
// ошибка в порядке списка или в join прошла бы зелёной на единственном.
func TestPlanCoefficientCaveatNamesEveryReasonCombination(t *testing.T) {
	// Два слота ОДНОГО артикула: статистика причин складывается на артикуле, а кавеат печатается
	// по нему. Второй слот получает секцию и вид расхода по параметрам.
	twoSlots := func(section entity.TechCardBomSection, countedB bool) (*entity.ProductionRun, *entity.TechCard) {
		run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "5")
		// Первый слот делаем счётным-или-нерулонным через сам вызов; здесь — только второй.
		second := card.BomItems[0]
		second.Id = 502
		second.Name = "Second slot"
		second.Section = section
		card.BomItems = append(card.BomItems, second)
		u := entity.TechCardColorwayUsage{
			BomItemId: sql.NullInt64{Int64: 502, Valid: true},
			ConsumptionSource: sql.NullString{
				String: entity.ConsumptionSourceManual, Valid: true},
		}
		if countedB {
			u.Quantity = nd2("3")
		} else {
			u.Consumption = nd2("1")
		}
		card.Colorways[0].Usages = append(card.Colorways[0].Usages, u)
		return run, card
	}
	caveatOf := func(run *entity.ProductionRun, card *entity.TechCard) string {
		resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil), nil)
		for _, c := range resp.Caveats {
			if strings.Contains(c, "cutting coefficient") {
				return c
			}
		}
		return ""
	}

	t.Run("рулонная норма + счётный слот — частичное применение", func(t *testing.T) {
		got := caveatOf(twoSlots(entity.BomSectionFabric, true))
		require.Contains(t, got, "applied to PART of this row only")
		require.Contains(t, got, "the counted quantities take no gross-up at all")
	})
	t.Run("рулонная норма + нерулонный слот — частичное применение", func(t *testing.T) {
		got := caveatOf(twoSlots(entity.BomSectionThread, false))
		require.Contains(t, got, "applied to PART of this row only")
		require.Contains(t, got, "the non-roll-goods slots have no roll shrinkage or flaws to pay for")
	})
	t.Run("обе причины разом — обе и названы, в фиксированном порядке", func(t *testing.T) {
		// Второй слот — МЕРНЫЙ на нерулонной секции (счётность победила бы нерулонность: у
		// счётной строки гросс-апа нет вовсе, и это более сильная причина — см. порядок switch).
		run, card := twoSlots(entity.BomSectionThread, false)
		// Третий слот: счётный на рулонной секции, чтобы к нерулонной причине добавилась счётная.
		third := card.BomItems[0]
		third.Id = 503
		third.Name = "Third slot"
		card.BomItems = append(card.BomItems, third)
		card.Colorways[0].Usages = append(card.Colorways[0].Usages, entity.TechCardColorwayUsage{
			BomItemId: sql.NullInt64{Int64: 503, Valid: true},
			Quantity:  nd2("2"),
			ConsumptionSource: sql.NullString{
				String: entity.ConsumptionSourceManual, Valid: true},
		})
		got := caveatOf(run, card)
		require.Contains(t, got, "the counted quantities take no gross-up at all")
		require.Contains(t, got, "the non-roll-goods slots have no roll shrinkage or flaws to pay for")
		require.Less(t,
			strings.Index(got, "the counted quantities"),
			strings.Index(got, "the non-roll-goods slots"),
			"порядок причин фиксирован: настилы → счётные → нерулонные")
	})
	t.Run("НЕТ причин — нет и кавеата", func(t *testing.T) {
		// Обе строки мерные и рулонные: коэффициент взял всё, объяснять нечего.
		require.Equal(t, "", caveatOf(twoSlots(entity.BomSectionLining, false)))
	})
}
