package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
)

// stripScope — один скоуп с одной деталью: площадь 4200 см², периметр 260 см (реальные пропорции
// полочки). На них и держится вся арифметика ниже.
func stripScope(key string, withPerimeter bool) PieceAreaScope {
	row := PieceAreaRow{
		PieceLineKey: "PIECE1",
		SizeId:       sql.NullInt64{Int64: 4, Valid: true},
		AreaCm2:      decimal.RequireFromString("4200"),
	}
	if withPerimeter {
		row.PerimeterCm = decimal.NullDecimal{Decimal: decimal.RequireFromString("260"), Valid: true}
	}
	return PieceAreaScope{ScopeKey: key, Rows: []PieceAreaRow{row}}
}

func cm(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

// ПОЛОСА СЧИТАЕТСЯ ПЕРИМЕТРОМ, А НЕ ПЛОЩАДЬЮ — то самое, ради чего заведена вся фаза.
//
// 260 см × 2.5 см = 650 см² против 4200 см² у полного дублирования: в 6.5 раза меньше. Оба числа
// делятся на одну раскройную ширину, поэтому разница доезжает до нормы один в один.
func TestStripNormUsesPerimeterNotArea(t *testing.T) {
	areas := map[string]PieceAreaScope{"FUSE": stripScope("FUSE", true)}
	pieces := []AreaEstimatePiece{{LineKey: "PIECE1", PerGarment: 1, StripWidthCm: cm("2.5")}}

	got, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4)
	if refusal != "" {
		t.Fatalf("отказ %q там, где всё измерено", refusal)
	}
	// 650 см² / 100 см = 6.5 см.
	if !got.Equal(decimal.RequireFromString("6.5")) {
		t.Fatalf("норма полосы = %s, ожидалось 6.5 (260 × 2.5 ÷ 100)", got)
	}

	// Та же деталь без режима считается площадью — 4200 / 100 = 42 см.
	whole := []AreaEstimatePiece{{LineKey: "PIECE1", PerGarment: 1}}
	got, refusal = AreaEstimateNorm("FUSE", whole, areas, cm("100"), "cm", 4)
	if refusal != "" || !got.Equal(decimal.RequireFromString("42")) {
		t.Fatalf("дублирование целиком = %s (отказ %q), ожидалось 42", got, refusal)
	}
}

// КРАТНОСТЬ УМНОЖАЕТ И ПОЛОСУ. Деталь, которую кроят дважды, несёт две полосы; забыть здесь
// множитель значит недосчитать клеевую ровно вдвое на каждой парной детали.
func TestStripNormMultipliesByPiecesPerGarment(t *testing.T) {
	areas := map[string]PieceAreaScope{"FUSE": stripScope("FUSE", true)}
	pieces := []AreaEstimatePiece{{LineKey: "PIECE1", PerGarment: 2, StripWidthCm: cm("2.5")}}
	got, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4)
	if refusal != "" || !got.Equal(decimal.RequireFromString("13")) {
		t.Fatalf("норма = %s (отказ %q), ожидалось 13 = 2 × 6.5", got, refusal)
	}
}

// ЗАМЕР БЕЗ ПЕРИМЕТРА — ОТКАЗ, А НЕ ВЫВОД ИЗ ПЛОЩАДИ. И отказ ИМЕННО СВОЙ: «комплект неполон»
// отправил бы оператора искать пропавшую деталь, которой нет, — все детали на месте и все размеры
// покрыты, не хватает второй меры того же контура.
func TestStripNormRefusesWithoutPerimeter(t *testing.T) {
	areas := map[string]PieceAreaScope{"FUSE": stripScope("FUSE", false)}
	pieces := []AreaEstimatePiece{{LineKey: "PIECE1", PerGarment: 1, StripWidthCm: cm("2.5")}}
	_, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4)
	if refusal != AreaEstimateNoPerimeter {
		t.Fatalf("отказ %q, ожидался %q", refusal, AreaEstimateNoPerimeter)
	}
	// Та же строка полным контуром считается прекрасно: периметр нужен ТОЛЬКО полосе, и старые
	// замеры не должны переставать работать для того, ради чего снимались.
	whole := []AreaEstimatePiece{{LineKey: "PIECE1", PerGarment: 1}}
	if _, refusal := AreaEstimateNorm("FUSE", whole, areas, cm("100"), "cm", 4); refusal != "" {
		t.Fatalf("замер без периметра перестал годиться для площади: %q", refusal)
	}
}

// ГЕОМЕТРИЯ ЗАИМСТВУЕТСЯ У ТКАНИ. У клеевой своих выкроек не бывает, поэтому её слот читает контур
// из скоупа основной ткани — иначе полностью измеренная карточка вечно отказывала бы «площади не
// измерены» на слоте, чертежа которого не существует.
func TestStripNormBorrowsGeometryFromTheFabricScope(t *testing.T) {
	areas := map[string]PieceAreaScope{"MAIN": stripScope("MAIN", true)}
	pieces := []AreaEstimatePiece{{
		LineKey: "PIECE1", PerGarment: 1, StripWidthCm: cm("2.5"), ScopeOverride: "MAIN",
	}}
	// Скоупа "FUSE" в карте НЕТ вовсе — ровно так выглядит клеевая на живой карточке.
	got, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4)
	if refusal != "" {
		t.Fatalf("отказ %q при заимствованной геометрии", refusal)
	}
	if !got.Equal(decimal.RequireFromString("6.5")) {
		t.Fatalf("норма = %s, ожидалось 6.5", got)
	}
}

// ЗАИМСТВУЯ КОНТУР, СЛОТ НАСЛЕДУЕТ И УСТАРЕВАНИЕ. Перерисовали полочку — устарела и клеевая на неё;
// смолчать здесь значило бы посчитать по лекалу, которого в файлах уже нет, ровно там, где основная
// ткань честно скажет «пересчитайте».
func TestStripNormInheritsStalenessOfTheBorrowedScope(t *testing.T) {
	sc := stripScope("MAIN", true)
	sc.Stale = true
	areas := map[string]PieceAreaScope{"MAIN": sc}
	pieces := []AreaEstimatePiece{{
		LineKey: "PIECE1", PerGarment: 1, StripWidthCm: cm("2.5"), ScopeOverride: "MAIN",
	}}
	if _, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4); refusal != AreaEstimateStale {
		t.Fatalf("отказ %q, ожидался %q", refusal, AreaEstimateStale)
	}
}

// СМЕШАННЫЙ СЛОТ: часть деталей своя, часть заимствованная. Проверяет, что слотовый скоуп всё ещё
// требуется, когда на него кто-то опирается, и что суммируются оба вклада.
func TestNormMixesOwnAndBorrowedScopes(t *testing.T) {
	own := stripScope("FUSE", true)
	own.Rows[0].PieceLineKey = "PIECE2"
	areas := map[string]PieceAreaScope{
		"FUSE": own,
		"MAIN": stripScope("MAIN", true),
	}
	pieces := []AreaEstimatePiece{
		{LineKey: "PIECE1", PerGarment: 1, StripWidthCm: cm("2.5"), ScopeOverride: "MAIN"},
		{LineKey: "PIECE2", PerGarment: 1, StripWidthCm: cm("2.5")},
	}
	got, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4)
	if refusal != "" || !got.Equal(decimal.RequireFromString("13")) {
		t.Fatalf("норма = %s (отказ %q), ожидалось 13 = 6.5 + 6.5", got, refusal)
	}
}

// ПОЛНОТА КОМПЛЕКТА СТАРШЕ НЕХВАТКИ ПЕРИМЕТРА. У первой детали есть площадь, но нет периметра; у
// второй нет площади на этот размер вовсе. Отказ обязан назвать НЕПОЛНЫЙ КОМПЛЕКТ: деталь, которой
// в замере нет, надо домерить, и жалоба на вторую меру ПЕРВОЙ детали отправляет чинить не то.
//
// В один проход побеждала бы та причина, чья деталь попалась раньше, — то есть указание оператору
// зависело бы от порядка строк рецепта.
func TestIncompleteSetOutranksMissingPerimeter(t *testing.T) {
	sc := stripScope("FUSE", false) // PIECE1: площадь есть, периметра нет
	areas := map[string]PieceAreaScope{"FUSE": sc}
	pieces := []AreaEstimatePiece{
		{LineKey: "PIECE1", PerGarment: 1, StripWidthCm: cm("2.5")},
		{LineKey: "PIECE2", PerGarment: 1, StripWidthCm: cm("2.5")}, // в замере её нет вовсе
	}
	if _, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4); refusal != AreaEstimateIncomplete {
		t.Fatalf("отказ %q, ожидался %q", refusal, AreaEstimateIncomplete)
	}
}

// ПОЛОСА БЕЗ ШИРИНЫ — ОТКАЗ, А НЕ ПОЛНЫЙ КОНТУР. Подстановка площади дала бы 42 см вместо отказа:
// на экране «по припуску», в деньгах «вся деталь», и разница в разы ничем не помечена.
func TestMissingStripWidthRefusesInsteadOfFallingBackToArea(t *testing.T) {
	areas := map[string]PieceAreaScope{"FUSE": stripScope("FUSE", true)}
	pieces := []AreaEstimatePiece{{LineKey: "PIECE1", PerGarment: 1, StripWidthMissing: true}}
	got, refusal := AreaEstimateNorm("FUSE", pieces, areas, cm("100"), "cm", 4)
	if refusal != AreaEstimateNoStripWidth {
		t.Fatalf("отказ %q (норма %s), ожидался %q", refusal, got, AreaEstimateNoStripWidth)
	}
}
