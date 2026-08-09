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
//   3. в потребности прогона его не трогает коэффициент раскроя артикула, и объяснение этому в
//      ответе не называет норму ручной.

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

	// Себестоимость стиля — норма БАЗОВОГО размера, тоже с гросс-апом.
	bs := perSize.BaseSizeTotal(bom, 2)
	require.True(t, bs.Valid)
	require.Equal(t, "32.4", bs.Decimal.String(), "base size 2: 3×10×1.08")

	// И ни один процент раскроя — ни одного умножения: слот без процента отдаёт чистое netto. Это
	// и есть та дыра, которую гейт готовности закрывает блокером (см. readiness-тесты ниже).
	noPct := &entity.TechCardBomItem{UnitPrice: bom.UnitPrice}
	require.Equal(t, "25", dxf.LineTotal(noPct).Decimal.String(),
		"wastage_percent NULL means applyWastage multiplies by nothing — netto reaches the money as a total")
}

// Ф5а.2 остаётся неприкосновенной: коэффициент раскроя артикула калиброван по ИЗМЕРЕННЫМ длинам
// раскладок, поэтому норму с выкроек он не трогает. Но объяснение в ответе обязано называть верную
// причину — «ваши нормы ручные» про площадь деталей было бы правильным числом с ложным поводом.
func TestPlanDxfNormTakesWastageNotCoefficientAndSaysWhy(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceDxf, 100, "5")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil), nil)

	require.Len(t, resp.Rows, 1)
	row := resp.Rows[0]
	require.Equal(t, "210", row.Required.Value, "200 × 1.05 wastage — the coefficient must NOT also bite")
	require.Equal(t, "200", row.RequiredBeforeGrossup.Value)
	require.True(t, hasCaveat(resp.Caveats, "cutting coefficient 1.06 not applied"),
		"a dial that does nothing must say so: %v", resp.Caveats)
	require.True(t, hasCaveat(resp.Caveats, "taken from the patterns"),
		"and it must name the RIGHT reason, not «manual»: %v", resp.Caveats)
	for _, c := range resp.Caveats {
		if strings.Contains(c, "cutting coefficient") {
			require.NotContains(t, c, "norms for it are manual",
				"a pattern-derived norm is not a manual one: %q", c)
		}
	}
}

// The pre-0294 wordings must be byte-identical, because the composed phrase replaced hand-written
// arms: a caveat whose text drifts silently is a caveat nobody trusts twice.
func TestPlanCoefficientCaveatWordingUnchangedForManualNorms(t *testing.T) {
	run, card := planFixture("m", "2", entity.ConsumptionSourceManual, 100, "5")
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, fabricArticle("m", nd2("1.06"), nil), nil)
	require.True(t, hasCaveat(resp.Caveats,
		"cutting coefficient 1.06 not applied — it grosses up MARKER-sourced norms, and this run's norms for it are manual (their BOM wastage % applies instead)"),
		"manual-only wording must not drift: %v", resp.Caveats)
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
	require.Contains(t, f.Detail, "снят с выкроек")
	require.Contains(t, f.Detail, "12%", "the percentage that pays for the waste must be named")
	require.NotContains(t, f.Detail, "введён руками")

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
	require.Contains(t, f.Detail, "НЕ ЗАДАН")
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
