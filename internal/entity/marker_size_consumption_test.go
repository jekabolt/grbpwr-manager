package entity

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Одна раскладка на весь файл: 3 × S и 2 × M на одном настиле, детали градуированные плюс один
// безразмерный карман. Числа подобраны так, что деление ТОЧНОЕ — сходимость тогда проверяется как
// тождество, а не как «в пределах эпсилон», и провал теста означает ошибку распределения, а не
// арифметику decimal.
//
//	a_S = 3000 + 2×900  + 2×200 = 5200 см²
//	a_M = 3600 + 2×1100 + 2×200 = 6200 см²
//	A   = 3×5200 + 2×6200 = 28000 см²   (= сумме площадей ВСЕХ экземпляров, см. отдельный тест)
//	L   = 1400 см ⇒ расход(S) = 260, расход(M) = 310, среднее по составу = 280
//
// Разрыв со средним — 20 см на S и 30 см на M, то есть ровно тот перекос «мелкие завышены, крупные
// занижены», ради устранения которого Ф2 и заводилась.
var (
	fixtureComposition = []MarkerCompositionEntry{{SizeId: 10, Quantity: 3}, {SizeId: 20, Quantity: 2}}
	fixturePieces      = []MarkerPieceArea{
		{SizeId: 10, Quantity: 1, AreaCm2: d("3000")},
		{SizeId: 20, Quantity: 1, AreaCm2: d("3600")},
		{SizeId: 10, Quantity: 2, AreaCm2: d("900")},
		{SizeId: 20, Quantity: 2, AreaCm2: d("1100")},
		{SizeId: 0, Quantity: 2, AreaCm2: d("200")},
	}
)

func withAreas(t *testing.T, comp []MarkerCompositionEntry, pieces []MarkerPieceArea) []MarkerCompositionEntry {
	t.Helper()
	out := WithMarkerSizeAreas(comp, pieces)
	for _, c := range out {
		if !c.AreaPerGarmentCm2.Valid {
			t.Fatalf("size %d got no area — the fixture is supposed to yield one", c.SizeId)
		}
	}
	return out
}

// convergence sums the distributed lengths back up: Σ (quantity × расход).
func convergence(rows []MarkerSizeConsumption) decimal.Decimal {
	total := decimal.Zero
	for _, r := range rows {
		if !r.ConsumptionCm.Valid {
			return decimal.Zero
		}
		total = total.Add(r.ConsumptionCm.Decimal.Mul(decimal.NewFromInt(int64(r.Quantity))))
	}
	return total
}

// --- КРИТЕРИЙ ПРИЁМКИ ФАЗЫ ---------------------------------------------------------------------

// «Сумма пер-размерных расходов × количества = длина маркера» (03-composition.md, «Проверка»). This
// is the assertion the whole distribution exists to satisfy, and it is asserted as EQUALITY: if the
// pieces and the состав describe the same раскладка the identity is exact, and a version of this
// test that tolerated a percent would pass on a distribution that had lost a piece.
func TestPerSizeConsumptionConvergesOnUsedLength(t *testing.T) {
	comp := withAreas(t, fixtureComposition, fixturePieces)
	used := d("1400")
	rows := MarkerPerSizeConsumption(comp, used)

	if len(rows) != 2 {
		t.Fatalf("one row per состав line, got %d", len(rows))
	}
	if got := rows[0].ConsumptionCm.Decimal.String(); got != "260" {
		t.Errorf("расход(S) = %s, want 260", got)
	}
	if got := rows[1].ConsumptionCm.Decimal.String(); got != "310" {
		t.Errorf("расход(M) = %s, want 310", got)
	}
	if got := convergence(rows); !got.Equal(used) {
		t.Fatalf("Σ(quantity × расход) = %s, want exactly %s — the distribution is wrong, not the rounding",
			got, used)
	}

	// И ЭТО НЕ СРЕДНЕЕ. The number the server refuses to hand out on this раскладка sits between the
	// two, overstating the small size and understating the large one — the defect being removed.
	mean := used.Div(decimal.NewFromInt(5))
	if rows[0].ConsumptionCm.Decimal.GreaterThanOrEqual(mean) || rows[1].ConsumptionCm.Decimal.LessThanOrEqual(mean) {
		t.Fatalf("the distribution collapsed onto the mean %s: %s / %s",
			mean, rows[0].ConsumptionCm.Decimal, rows[1].ConsumptionCm.Decimal)
	}
}

// Сходимость обязана держаться и на числах, которые не делятся нацело — иначе критерий проверял бы
// удачно подобранную фикстуру. Точное равенство здесь недостижимо (decimal делит с конечной
// точностью), поэтому остаток ограничен и ограничение НАЗВАНО: оно на десять порядков меньше
// сотой сантиметра, до которой цифра округляется на проводе.
func TestPerSizeConsumptionConvergesOnAwkwardNumbers(t *testing.T) {
	comp := []MarkerCompositionEntry{
		{SizeId: 10, Quantity: 3, AreaPerGarmentCm2: decimal.NullDecimal{Decimal: d("5237.44"), Valid: true}},
		{SizeId: 20, Quantity: 2, AreaPerGarmentCm2: decimal.NullDecimal{Decimal: d("6183.91"), Valid: true}},
		{SizeId: 30, Quantity: 7, AreaPerGarmentCm2: decimal.NullDecimal{Decimal: d("7011.03"), Valid: true}},
	}
	used := d("1387.65")
	rows := MarkerPerSizeConsumption(comp, used)
	got := convergence(rows)
	residue := got.Sub(used).Abs()
	if residue.GreaterThan(d("0.0000000001")) {
		t.Fatalf("Σ(quantity × расход) = %s, want %s (residue %s)", got, used, residue)
	}
	t.Logf("нацело не делится: Σ = %s против L = %s, остаток %s", got, used, residue)

	// На проводе цифра округляется до сотых, и накопленное расхождение обязано остаться в пределах
	// половины последнего разряда на каждое изделие — иначе округление стало бы источником ошибки
	// костинга, а не косметикой отображения.
	rounded := decimal.Zero
	units := 0
	for _, r := range rows {
		rounded = rounded.Add(r.ConsumptionCm.Decimal.Round(2).Mul(decimal.NewFromInt(int64(r.Quantity))))
		units += r.Quantity
	}
	bound := d("0.005").Mul(decimal.NewFromInt(int64(units)))
	if diff := rounded.Sub(used).Abs(); diff.GreaterThan(bound) {
		t.Fatalf("после округления до сотых Σ = %s против %s, расхождение %s > %s", rounded, used, diff, bound)
	}
}

// --- ПРИВЯЗКА БЕЗРАЗМЕРНЫХ ДЕТАЛЕЙ -------------------------------------------------------------

// Тождество, на котором стоит сходимость: Σ_s (q_s × a_s) равно сумме площадей ВСЕХ экземпляров,
// посчитанных по формуле экземпляров из прото. Оно выполняется ровно при выбранной привязке
// безразмерных деталей — «целиком в каждое изделие» — и ломается при любой другой (например при
// раздаче их по долям состава). Поэтому проверяется именно оно, а не сами числа.
func TestSizeAgnosticPiecesFollowTheInstanceFormula(t *testing.T) {
	comp := withAreas(t, fixtureComposition, fixturePieces)
	totalFromSizes := MarkerCompositionAreaCm2(comp)
	if !totalFromSizes.Valid {
		t.Fatal("the fixture must yield a denominator")
	}

	// Формула экземпляров: quantity × (size_id > 0 ? composition[size_id].quantity : total_units).
	qty := map[int]int{}
	for _, c := range fixtureComposition {
		qty[c.SizeId] = c.Quantity
	}
	units := TotalUnitsOf(fixtureComposition)
	fromPieces := decimal.Zero
	for _, p := range fixturePieces {
		n := units
		if p.SizeId > 0 {
			n = qty[p.SizeId]
		}
		fromPieces = fromPieces.Add(p.AreaCm2.Mul(decimal.NewFromInt(int64(p.Quantity * n))))
	}
	if !totalFromSizes.Decimal.Equal(fromPieces) {
		t.Fatalf("Σ(q_s × a_s) = %s but the pieces lay out %s — the size-agnostic attribution disagrees "+
			"with the geometry", totalFromSizes.Decimal, fromPieces)
	}

	// И явно: карман (2 × 200 см²) сидит в КАЖДОМ изделии целиком, одинаково у S и у M.
	bare := WithMarkerSizeAreas(fixtureComposition, fixturePieces[:4])
	for i := range comp {
		delta := comp[i].AreaPerGarmentCm2.Decimal.Sub(bare[i].AreaPerGarmentCm2.Decimal)
		if !delta.Equal(d("400")) {
			t.Errorf("size %d gained %s cm² from the size-agnostic pocket, want 400 for every size",
				comp[i].SizeId, delta)
		}
	}
}

// Раскладка, в которой НИЧЕГО не градуируется, — законный случай (в DXF нет размерных суффиксов).
// Каждое изделие тогда действительно кроит одни и те же контуры, и одинаковая цифра на всех размерах
// — верный ответ, а не вырождение: сходимость на ней держится, и она совпадает со средним честно, а
// не по умолчанию.
func TestNothingGradesGivesEverySizeTheSameHonestNumber(t *testing.T) {
	comp := withAreas(t, fixtureComposition, []MarkerPieceArea{
		{SizeId: 0, Quantity: 1, AreaCm2: d("3200")},
		{SizeId: 0, Quantity: 2, AreaCm2: d("500")},
	})
	used := d("1400")
	rows := MarkerPerSizeConsumption(comp, used)
	if !rows[0].ConsumptionCm.Decimal.Equal(rows[1].ConsumptionCm.Decimal) {
		t.Fatalf("ungraded pieces must cost every size the same: %s vs %s",
			rows[0].ConsumptionCm.Decimal, rows[1].ConsumptionCm.Decimal)
	}
	if got := convergence(rows); !got.Equal(used) {
		t.Fatalf("Σ = %s, want %s", got, used)
	}
	if got := rows[0].ConsumptionCm.Decimal; !got.Equal(used.Div(decimal.NewFromInt(5))) {
		t.Fatalf("расход = %s, want the honest mean %s", got, used.Div(decimal.NewFromInt(5)))
	}
}

// --- ОДНОРОДНЫЙ И ЛЕГАСИ ------------------------------------------------------------------------

// Однородной раскладке площади не нужны вовсе: весь настил принадлежит одному размеру, и норма —
// L / q, та же цифра, что скалярный путь выдавал всегда. Это то, что оставляет пер-размерный ответ у
// КАЖДОГО маркера, включая снятые до Ф2.4 и легаси (size_id, sets).
func TestHomogeneousNeedsNoAreas(t *testing.T) {
	rows := MarkerPerSizeConsumption([]MarkerCompositionEntry{{SizeId: 10, Quantity: 4}}, d("512.4"))
	if len(rows) != 1 || !rows[0].ConsumptionCm.Valid {
		t.Fatalf("a homogeneous раскладка must state its norm without areas: %+v", rows)
	}
	if got := rows[0].ConsumptionCm.Decimal.String(); got != "128.1" {
		t.Fatalf("расход = %s, want 128.1", got)
	}
	if got := convergence(rows); !got.Equal(d("512.4")) {
		t.Fatalf("Σ = %s, want 512.4", got)
	}
}

// --- ОТКАЗЫ: ЧЕСТНОЕ «НЕ ЗНАЮ» ВМЕСТО СРЕДНЕГО --------------------------------------------------

func TestMixedWithoutAreasWithholdsRatherThanAveraging(t *testing.T) {
	cases := map[string][]MarkerCompositionEntry{
		"площади не записаны вовсе (маркер до Ф2.4)": fixtureComposition,
		"площадь записана только одному размеру": {
			{SizeId: 10, Quantity: 3, AreaPerGarmentCm2: decimal.NullDecimal{Decimal: d("5200"), Valid: true}},
			{SizeId: 20, Quantity: 2},
		},
	}
	for name, comp := range cases {
		rows := MarkerPerSizeConsumption(comp, d("1400"))
		if len(rows) != len(comp) {
			t.Fatalf("%s: the состав must still ride, got %d rows", name, len(rows))
		}
		for _, r := range rows {
			if r.ConsumptionCm.Valid {
				t.Errorf("%s: size %d was handed %s — a partial distribution is the mean wearing a "+
					"per-size label", name, r.SizeId, r.ConsumptionCm.Decimal)
			}
		}
		if MarkerPerSizeConsumptionComplete(rows) {
			t.Errorf("%s: must not read as applicable by size", name)
		}
	}
}

func TestAreasAreWithheldWholesaleOnUnusableGeometry(t *testing.T) {
	cases := map[string][]MarkerPieceArea{
		"нулевые площади": {
			{SizeId: 10, Quantity: 1, AreaCm2: decimal.Zero},
			{SizeId: 20, Quantity: 1, AreaCm2: decimal.Zero},
		},
		"отрицательная площадь": {
			{SizeId: 10, Quantity: 1, AreaCm2: d("3000")},
			{SizeId: 20, Quantity: 1, AreaCm2: d("-1")},
		},
		"у одного размера нет ни одной детали": {
			{SizeId: 10, Quantity: 1, AreaCm2: d("3000")},
		},
		"деталь указывает на размер вне состава": {
			{SizeId: 10, Quantity: 1, AreaCm2: d("3000")},
			{SizeId: 20, Quantity: 1, AreaCm2: d("3600")},
			{SizeId: 99, Quantity: 1, AreaCm2: d("1000")},
		},
		"деталь с нулевым количеством": {
			{SizeId: 10, Quantity: 0, AreaCm2: d("3000")},
			{SizeId: 20, Quantity: 1, AreaCm2: d("3600")},
		},
	}
	for name, pieces := range cases {
		if _, ok := MarkerSizeAreasPerGarment(fixtureComposition, pieces); ok {
			t.Errorf("%s: must withhold the whole distribution", name)
		}
		got := WithMarkerSizeAreas(fixtureComposition, pieces)
		for _, c := range got {
			if c.AreaPerGarmentCm2.Valid {
				t.Errorf("%s: size %d kept an area (%s) — all lines or none", name, c.SizeId,
					c.AreaPerGarmentCm2.Decimal)
			}
		}
	}
}

// Длина настила не записана (или записана нулём) — распределять нечего. Состав при этом всё равно
// едет: строка обязана уметь сказать, ЧТО она кроит, даже когда не может сказать, во что это встаёт.
func TestNoLengthMeansNoNumbersButStillAComposition(t *testing.T) {
	comp := withAreas(t, fixtureComposition, fixturePieces)
	for _, used := range []decimal.Decimal{decimal.Zero, d("-5")} {
		rows := MarkerPerSizeConsumption(comp, used)
		if len(rows) != 2 {
			t.Fatalf("used_length %s: the состав must still ride", used)
		}
		for _, r := range rows {
			if r.ConsumptionCm.Valid {
				t.Errorf("used_length %s: size %d was handed %s", used, r.SizeId, r.ConsumptionCm.Decimal)
			}
			if !r.AreaPerGarmentCm2.Valid {
				t.Errorf("used_length %s: size %d lost its area basis", used, r.SizeId)
			}
		}
	}
}

// --- ОТКАЗ ОТ СКАЛЯРА ПОСЛЕ Ф2.4 ---------------------------------------------------------------

// Ф2.4 не отменяет отказ — она даёт ему СРЕДСТВО. Проверяются обе половины: отказ на смешанном
// составе остаётся (иначе среднее уехало бы в рецепт как персистентный факт), а текст называет то
// действие, которое у ЭТОЙ раскладки действительно есть.
func TestScalarRefusalNamesTheRemedyThatExists(t *testing.T) {
	t.Run("однородная — отказа нет", func(t *testing.T) {
		rows := MarkerPerSizeConsumption([]MarkerCompositionEntry{{SizeId: 10, Quantity: 4}}, d("512.4"))
		if got := MarkerScalarNormRefusal("однородная", rows); got != "" {
			t.Fatalf("one size means the scalar is honest, got %q", got)
		}
	})

	t.Run("смешанная с пер-размерным расходом — «по размерам»", func(t *testing.T) {
		comp := withAreas(t, fixtureComposition, fixturePieces)
		got := MarkerScalarNormRefusal("смешанная", MarkerPerSizeConsumption(comp, d("1400")))
		if got == "" {
			t.Fatal("Ф2.4 does not repeal the refusal: a mixed настил still has no sizeless number")
		}
		if !strings.Contains(got, "PER SIZE") {
			t.Errorf("the refusal must name the remedy that now exists: %q", got)
		}
		if strings.Contains(got, "Ф2.4") {
			t.Errorf("the remedy exists — the text must stop pointing at a future phase: %q", got)
		}
	})

	t.Run("смешанная без площадей — «пересохраните»", func(t *testing.T) {
		got := MarkerScalarNormRefusal("старая смешанная", MarkerPerSizeConsumption(fixtureComposition, d("1400")))
		if got == "" {
			t.Fatal("a mixed раскладка must still refuse the scalar")
		}
		if strings.Contains(got, "PER SIZE") {
			t.Errorf("this раскладка has no per-size figures — promising the apply would be a lie: %q", got)
		}
		if !strings.Contains(got, "re-save the marker") {
			t.Errorf("the text must name the action that produces them: %q", got)
		}
	})

	t.Run("состава нет вовсе — прежний отказ", func(t *testing.T) {
		got := MarkerScalarNormRefusal("испорченная", MarkerPerSizeConsumption(nil, d("1400")))
		if !strings.Contains(got, "has no composition") {
			t.Fatalf("an empty состав keeps its own refusal, got %q", got)
		}
	})
}

// --- ЗНАМЕНАТЕЛЬ --------------------------------------------------------------------------------

// Частичная сумма — меньший знаменатель, а меньший знаменатель завышает норму ВСЕМ. Поэтому
// знаменатель либо полон, либо его нет.
func TestCompositionAreaIsAllOrNothing(t *testing.T) {
	comp := withAreas(t, fixtureComposition, fixturePieces)
	if got := MarkerCompositionAreaCm2(comp); !got.Valid || !got.Decimal.Equal(d("28000")) {
		t.Fatalf("A = %v, want 28000", got)
	}
	partial := []MarkerCompositionEntry{comp[0], {SizeId: 20, Quantity: 2}}
	if got := MarkerCompositionAreaCm2(partial); got.Valid {
		t.Fatalf("a partial denominator must not be reported, got %s", got.Decimal)
	}
	if got := MarkerCompositionAreaCm2(nil); got.Valid {
		t.Fatal("an empty состав has no denominator")
	}
}
