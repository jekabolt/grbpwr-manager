package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ДВА МНОЖИТЕЛЯ ДЕНЕГ НОРМЫ (W3), закреплённые в том единственном месте, где они умножаются.
//
//	расход = ГЕОМЕТРИЯ(набор деталей, ширина, настил) × РЕАЛЬНОСТЬ_РУЛОНА(артикул)
//
// Процент раскроя платит за настил (выпады + концы) и не бьёт по marker-норме, чья измеренная
// длина эти выпады уже содержит. Коэффициент артикула платит за то, чего НИ ОДНА раскладка
// содержать не может (усадка, пороки, сращивание, оттеночные полосы), и потому бьёт по ОБОИМ
// путям. Каждое утверждение ниже держит одну из границ, за которой начисление становится неверным.

func ccBom(mut func(*TechCardBomItem)) *TechCardBomItem {
	b := &TechCardBomItem{
		Section:        BomSectionFabric,
		UnitPrice:      decimal.NewNullDecimal(decimal.RequireFromString("10")),
		WastagePercent: decimal.NewNullDecimal(decimal.RequireFromString("20")),
	}
	if mut != nil {
		mut(b)
	}
	return b
}

func ccCoeff(v string) decimal.NullDecimal {
	return decimal.NewNullDecimal(decimal.RequireFromString(v))
}

func ccSource(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// TestMarkerNormTakesTheCoefficientButNeverThePercent — ловушка №1 фазы: коэффициент НЕ является
// заменой процента и не подчиняется правилу провенанса. Измеренная раскладка уже содержит выпады
// (поэтому 20% не начисляются), но не содержит усадки (поэтому 1.05 начисляются).
func TestMarkerNormTakesTheCoefficientButNeverThePercent(t *testing.T) {
	bom := ccBom(func(b *TechCardBomItem) { b.CuttingCoefficient = ccCoeff("1.05") })
	u := TechCardColorwayUsage{
		Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("2")),
		ConsumptionSource: ccSource(ConsumptionSourceMarker),
	}

	// 2 × 10 × 1.05 = 21. НЕ 25.2 (это была бы marker-строка, гроssнутая ещё и процентом) и не 20
	// (это была бы marker-строка до врезки коэффициента).
	got := u.LineTotal(bom)
	require.True(t, got.Valid)
	require.Equal(t, "21", got.Decimal.String())

	// Тот же артикул без коэффициента даёт ровно ту цифру, что и до W3, — доказательство того, что
	// разница выше пришла именно от коэффициента, а не от смены правила процента.
	require.Equal(t, "20", u.LineTotal(ccBom(nil)).Decimal.String())
}

// TestNettoNormTakesBothMultipliers — netto-путь (manual и dxf одинаково): процент за настил,
// коэффициент за рулон, и оба сразу.
func TestNettoNormTakesBothMultipliers(t *testing.T) {
	bom := ccBom(func(b *TechCardBomItem) { b.CuttingCoefficient = ccCoeff("1.05") })

	for _, src := range []string{ConsumptionSourceManual, ConsumptionSourceDxf, ""} {
		u := TechCardColorwayUsage{
			Consumption:       decimal.NewNullDecimal(decimal.RequireFromString("2")),
			ConsumptionSource: ccSource(src),
		}
		// 2 × 10 × 1.20 × 1.05 = 25.2.
		got := u.LineTotal(bom)
		require.True(t, got.Valid, "source %q", src)
		require.Equal(t, "25.2", got.Decimal.String(), "source %q", src)
	}
}

// TestCountedTrimIsNeverGrossed — граница «только мерные строки». Счётная строка выходит из
// LineTotal ДО любого гросс-апа, и коэффициент не должен был этот порядок изменить: 4 пуговицы
// остаются 4 пуговицами, сколько бы ни садилась ткань рядом с ними.
func TestCountedTrimIsNeverGrossed(t *testing.T) {
	// Секция намеренно рулонная: проверяется НЕ фильтр секции, а именно счётность строки.
	bom := ccBom(func(b *TechCardBomItem) { b.CuttingCoefficient = ccCoeff("1.5") })
	u := TechCardColorwayUsage{Quantity: decimal.NewNullDecimal(decimal.RequireFromString("4"))}

	got := u.LineTotal(bom)
	require.True(t, got.Valid)
	require.Equal(t, "40", got.Decimal.String())
}

// TestNonRollSectionIgnoresTheCoefficient — граница секций. Коэффициент оплачивает усадку и пороки
// ПОЛОТНА В РУЛОНЕ; у нитки, пуговицы, этикетки и упаковки таких потерь нет, и начислить их значило
// бы поднять цену фурнитуры на процент, которого никто не наблюдал.
func TestNonRollSectionIgnoresTheCoefficient(t *testing.T) {
	u := TechCardColorwayUsage{Consumption: decimal.NewNullDecimal(decimal.RequireFromString("2"))}

	for _, s := range []TechCardBomSection{
		BomSectionThread, BomSectionHardware, BomSectionLabel,
		BomSectionPackaging, BomSectionTrim, BomSectionDecoration, BomSectionOther,
	} {
		bom := ccBom(func(b *TechCardBomItem) {
			b.Section = s
			b.CuttingCoefficient = ccCoeff("1.5")
		})
		require.False(t, bom.EffectiveCuttingCoefficient().Valid, "section %q", s)
		// 2 × 10 × 1.20 = 24 — процент как был, рулона нет.
		require.Equal(t, "24", u.LineTotal(bom).Decimal.String(), "section %q", s)
	}

	// Все четыре рулонные секции, наоборот, коэффициент берут.
	for _, s := range []TechCardBomSection{
		BomSectionFabric, BomSectionLining, BomSectionInterlining, BomSectionInsulation,
	} {
		bom := ccBom(func(b *TechCardBomItem) {
			b.Section = s
			b.CuttingCoefficient = ccCoeff("1.5")
		})
		require.Equal(t, "36", u.LineTotal(bom).Decimal.String(), "section %q", s)
	}
}

// TestUnsetCoefficientChangesNothing — «не задан» это ОТСУТСТВИЕ множителя, а не 1.0 с претензией.
// Карточка без коэффициента обязана считать ровно те же деньги, что до врезки, — иначе W3 молча
// сдвинул бы себестоимость всего каталога.
func TestUnsetCoefficientChangesNothing(t *testing.T) {
	u := TechCardColorwayUsage{Consumption: decimal.NewNullDecimal(decimal.RequireFromString("2"))}

	for name, bom := range map[string]*TechCardBomItem{
		"NULL":          ccBom(nil),
		"ниже единицы":  ccBom(func(b *TechCardBomItem) { b.CuttingCoefficient = ccCoeff("0.9") }),
		"ровно единица": ccBom(func(b *TechCardBomItem) { b.CuttingCoefficient = ccCoeff("1") }),
	} {
		// 2 × 10 × 1.20 = 24 во всех трёх случаях.
		require.Equal(t, "24", u.LineTotal(bom).Decimal.String(), name)
	}

	// Значение ниже единицы читается как «не задано», а не как скидка: коэффициент может только
	// добавить к норме. Слово в слово правило Material.EffectiveCuttingCoefficient.
	require.False(t, ccBom(func(b *TechCardBomItem) {
		b.CuttingCoefficient = ccCoeff("0.9")
	}).EffectiveCuttingCoefficient().Valid)
}

// TestCoefficientReachesEveryOneOfTheFourMoneyMethods — деньги нормы определены в четырёх методах,
// и множитель обязан быть во всех четырёх. Метод, забытый здесь, дал бы разную себестоимость
// одной строки в зависимости от того, кто её спросил.
func TestCoefficientReachesEveryOneOfTheFourMoneyMethods(t *testing.T) {
	bom := ccBom(func(b *TechCardBomItem) { b.CuttingCoefficient = ccCoeff("1.05") })
	plain := ccBom(nil)

	flat := TechCardColorwayUsage{Consumption: decimal.NewNullDecimal(decimal.RequireFromString("2"))}
	graded := TechCardColorwayUsage{SizeConsumptions: []TechCardBomSizeConsumption{
		{SizeId: 4, Consumption: decimal.RequireFromString("2")},
		{SizeId: 5, Consumption: decimal.RequireFromString("3")},
	}}

	// LineTotal: 2 × 10 × 1.2 × 1.05 = 25.2 (против 24 без коэффициента).
	require.Equal(t, "25.2", flat.LineTotal(bom).Decimal.String())
	require.Equal(t, "24", flat.LineTotal(plain).Decimal.String())

	// SizeRunTotal: (2×10 + 3×5) × 10 × 1.2 × 1.05 = 441 (против 420).
	qty := map[int]int{4: 10, 5: 5}
	require.Equal(t, "441", graded.SizeRunTotal(bom, qty).Decimal.String())
	require.Equal(t, "420", graded.SizeRunTotal(plain, qty).Decimal.String())

	// SizeNormTotal: 3 × 10 × 1.2 × 1.05 = 37.8 (против 36).
	require.Equal(t, "37.8", graded.SizeNormTotal(bom, 5).Decimal.String())
	require.Equal(t, "36", graded.SizeNormTotal(plain, 5).Decimal.String())

	// RangeAverageTotal: ((2+3)/2) × 10 × 1.2 × 1.05 = 31.5 (против 30).
	require.Equal(t, "31.5", graded.RangeAverageTotal(bom, []int{4, 5}).Decimal.String())
	require.Equal(t, "30", graded.RangeAverageTotal(plain, []int{4, 5}).Decimal.String())

	// UnitTotal композирует их, а не считает заново: базис ряда обязан совпасть с RangeAverageTotal.
	unit := graded.UnitTotal(bom, CostingBasis{Mode: CostingBasisRangeAverage, RangeSizeIds: []int{4, 5}})
	require.Equal(t, "31.5", unit.Decimal.String())
}

// TestPieceBoundRowStillHasNoMoneyWithACoefficient — строка детали не несёт нормы (T8), и
// коэффициент не должен был подарить ей деньги: множитель применяется к норме, а нормы здесь нет.
func TestPieceBoundRowStillHasNoMoneyWithACoefficient(t *testing.T) {
	bom := ccBom(func(b *TechCardBomItem) { b.CuttingCoefficient = ccCoeff("1.05") })
	u := TechCardColorwayUsage{
		PieceId:     sql.NullInt64{Int64: 7, Valid: true},
		Consumption: decimal.NewNullDecimal(decimal.RequireFromString("2")),
	}
	require.False(t, u.LineTotal(bom).Valid)
	require.False(t, u.UnitTotal(bom, CostingBasis{}).Valid)
}
