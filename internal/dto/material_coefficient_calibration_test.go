package dto

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// ------------------------------------------------------------------ fixtures

// obs builds one настил's plan/fact pair. An empty string is a NULL column — «не посчитан» / «не
// замерен», never a zero.
func obs(id int, planned, actual string) LayFactObservation {
	o := LayFactObservation{LayId: id, LayKey: fmt.Sprintf("L%d", id)}
	if planned != "" {
		o.PlannedQty = nd(planned)
	}
	if actual != "" {
		o.ActualQty = nd(actual)
	}
	return o
}

func wantDec(t *testing.T, got decimal.NullDecimal, want string, what string) {
	t.Helper()
	if !got.Valid {
		t.Fatalf("%s: INVALID, want %s", what, want)
	}
	if !got.Decimal.Equal(decimal.RequireFromString(want)) {
		t.Fatalf("%s = %s, want %s", what, got.Decimal.String(), want)
	}
}

// ------------------------------------------------------------------ порог трёх

// TestCoefficientSuggestionNeedsThreeLays is acceptance probe §6 п.7 word for word: два настила с
// фактом ⇒ предложения нет («фактов мало»); три ⇒ есть.
//
// МУТАЦИОННЫЙ СТРАЖ ПОРОГА: опустите MinLaysForCoefficientSuggestion до 1 или 2 — и первый подтест
// начнёт получать READY с числом, выведенным из пары замеров.
func TestCoefficientSuggestionNeedsThreeLays(t *testing.T) {
	two := []LayFactObservation{obs(1, "100", "106"), obs(2, "100", "106")}

	t.Run("два настила ⇒ предложения НЕТ, и так и сказано", func(t *testing.T) {
		got := MaterialCoefficientSuggestionOf(two, "m")
		if got.Status != CoefficientSuggestionTooFewFacts {
			t.Fatalf("status = %v, want TOO_FEW_FACTS", got.Status)
		}
		if got.Suggested.Valid {
			t.Errorf("предложение %s выдано на двух замерах — это догадка в одежде числа",
				got.Suggested.Decimal.String())
		}
		if got.MedianDrift.Valid {
			t.Errorf("медиана %s посчитана ниже порога — число без опоры", got.MedianDrift.Decimal.String())
		}
		if got.LayCount != 2 {
			t.Errorf("LayCount = %d, want 2 — сколько фактов есть, говорится и в отказе", got.LayCount)
		}
		if !strings.Contains(got.Detail, "not enough facts") || !strings.Contains(got.Detail, "3") {
			t.Errorf("detail must say «фактов мало» and name the threshold: %q", got.Detail)
		}
	})

	t.Run("три настила ⇒ предложение есть, и это медиана", func(t *testing.T) {
		three := append(append([]LayFactObservation{}, two...), obs(3, "100", "106"))
		got := MaterialCoefficientSuggestionOf(three, "m")
		if got.Status != CoefficientSuggestionReady {
			t.Fatalf("status = %v (%s), want READY", got.Status, got.Detail)
		}
		wantDec(t, got.Suggested, "1.06", "предложение")
		wantDec(t, got.MedianDrift, "0.06", "медиана дрейфов")
		if got.LayCount != 3 {
			t.Fatalf("LayCount = %d, want 3", got.LayCount)
		}
		// §4.2: «Предложение показывается с числом настилов, на которых оно основано.»
		if !strings.Contains(got.Detail, "3") || !strings.Contains(got.Detail, "1.06") {
			t.Errorf("detail must carry BOTH the suggestion and its support: %q", got.Detail)
		}
	})
}

// TestCoefficientSuggestionZeroValueIsNotAnOffer — забытая структура обязана читаться как «нечего
// предложить», а не как коэффициент. Ready стоит последним в перечислении именно поэтому.
func TestCoefficientSuggestionZeroValueIsNotAnOffer(t *testing.T) {
	var zero MaterialCoefficientSuggestion
	if zero.Status != CoefficientSuggestionTooFewFacts {
		t.Errorf("нулевое значение = %v, want TOO_FEW_FACTS", zero.Status)
	}
	if zero.Status == CoefficientSuggestionReady || zero.Suggested.Valid {
		t.Errorf("число нельзя получить, забыв его посчитать")
	}
	if got := MaterialCoefficientSuggestionOf(nil, "m"); got.Status != CoefficientSuggestionTooFewFacts ||
		got.LayCount != 0 || got.Suggested.Valid {
		t.Errorf("пустой вход = %+v, want TOO_FEW_FACTS с нулём настилов", got)
	}
}

// ------------------------------------------------------------------ медиана, а не среднее

// TestCoefficientSuggestionIsMedianNotMean — §4.2 и главный мутационный страж файла: один настил с
// диким фактом («ввели 500 вместо 50») НЕ сдвигает предложение. Замените медиану средним — и этот
// тест немедленно покажет 2.35 вместо 1.03.
func TestCoefficientSuggestionIsMedianNotMean(t *testing.T) {
	lays := []LayFactObservation{
		obs(1, "100", "102"), // дрейф 0.02
		obs(2, "100", "103"), // дрейф 0.03
		obs(3, "100", "500"), // дрейф 4.00 — опечатка на складе
	}
	got := MaterialCoefficientSuggestionOf(lays, "m")
	if got.Status != CoefficientSuggestionReady {
		t.Fatalf("status = %v (%s), want READY", got.Status, got.Detail)
	}
	wantDec(t, got.Suggested, "1.03", "предложение")

	// И то же самое, сказанное отрицанием: среднее дало бы совсем другое число, которое к тому же
	// прошло бы диапазон и выглядело бы правдоподобно на экране.
	mean := decimal.RequireFromString("0.02").
		Add(decimal.RequireFromString("0.03")).
		Add(decimal.RequireFromString("4")).
		Div(decimal.NewFromInt(3)).Add(decimal.NewFromInt(1)).Round(4)
	if got.Suggested.Decimal.Equal(mean) {
		t.Fatalf("предложение совпало со средним (%s) — медиана заменена средним", mean.String())
	}

	// Выброс обязан ОСТАТЬСЯ ВИДИМЫМ: медиана его пережила, но в базе он всё ещё неверен.
	if len(got.Drifts) != 3 {
		t.Fatalf("дрейфы отданы не все: %d", len(got.Drifts))
	}
	if !got.Drifts[2].Counted() || !got.Drifts[2].Drift.Decimal.Equal(decimal.NewFromInt(4)) {
		t.Errorf("дрейф выброса = %+v, want посчитанный 4 — медиана не прячет строку, она её переживает",
			got.Drifts[2])
	}
}

// TestCoefficientMedianOfEvenSampleIsTheMidPair — чётная выборка: полусумма двух средних, а не
// «нижний из двух» и не «верхний».
func TestCoefficientMedianOfEvenSampleIsTheMidPair(t *testing.T) {
	lays := []LayFactObservation{
		obs(1, "100", "102"), obs(2, "100", "104"), obs(3, "100", "106"), obs(4, "100", "108"),
	}
	got := MaterialCoefficientSuggestionOf(lays, "m")
	if got.Status != CoefficientSuggestionReady {
		t.Fatalf("status = %v (%s), want READY", got.Status, got.Detail)
	}
	wantDec(t, got.Suggested, "1.05", "предложение") // (0.04 + 0.06) / 2 = 0.05
}

// TestCoefficientMedianIgnoresInputOrder — медиана не зависит от того, в каком порядке store вернул
// настилы, а список дрейфов — ЗАВИСИТ и обязан сохранить порядок входа.
func TestCoefficientMedianIgnoresInputOrder(t *testing.T) {
	a := []LayFactObservation{obs(1, "100", "102"), obs(2, "100", "103"), obs(3, "100", "500")}
	b := []LayFactObservation{obs(3, "100", "500"), obs(1, "100", "102"), obs(2, "100", "103")}
	got, rev := MaterialCoefficientSuggestionOf(a, "m"), MaterialCoefficientSuggestionOf(b, "m")
	if !got.Suggested.Decimal.Equal(rev.Suggested.Decimal) {
		t.Fatalf("порядок входа изменил предложение: %s vs %s",
			got.Suggested.Decimal.String(), rev.Suggested.Decimal.String())
	}
	if rev.Drifts[0].LayId != 3 {
		t.Errorf("список дрейфов переставлен (%d) — человек ищет в нём СВОЙ настил", rev.Drifts[0].LayId)
	}
}

// ------------------------------------------------------------------ что входит в расчёт

// TestCoefficientCountsOnlyLaysWithBothNumbers — §4.2: «настилов этого артикула, где есть И план, И
// факт». Настил с планом, но без факта в расчёт не входит; настил с фактом, но без плана — тоже; и
// ни один из них НЕ добивает выборку до порога.
func TestCoefficientCountsOnlyLaysWithBothNumbers(t *testing.T) {
	t.Run("половинчатые настилы не считаются и названы", func(t *testing.T) {
		lays := []LayFactObservation{
			obs(1, "100", "106"),
			obs(2, "100", ""),  // план есть, факта нет — обычное состояние цеха
			obs(3, "", "106"),  // факт есть, плана нет — слот BOM удалён
			obs(4, "100", "0"), // ноль — незаполненная форма, а не «ушло нисколько»
			obs(5, "0", "106"), // план не положителен — делить не на что
			obs(6, "100", "106"),
			obs(7, "100", "106"),
		}
		got := MaterialCoefficientSuggestionOf(lays, "m")
		if got.LayCount != 3 {
			t.Fatalf("LayCount = %d, want 3 — считаются только настилы с обоими числами", got.LayCount)
		}
		if got.Status != CoefficientSuggestionReady {
			t.Fatalf("status = %v (%s), want READY", got.Status, got.Detail)
		}
		wantDec(t, got.Suggested, "1.06", "предложение")

		for _, i := range []int{1, 2, 3, 4} { // индексы настилов 2..5
			d := got.Drifts[i]
			if d.Counted() {
				t.Errorf("настил %d вошёл в медиану, хотя не должен: %+v", d.LayId, d)
			}
			if d.Skipped == "" {
				t.Errorf("настил %d выброшен молча — строка без числа и без объяснения неотличима от бага", d.LayId)
			}
		}
		for _, i := range []int{0, 5, 6} {
			if !got.Drifts[i].Counted() || got.Drifts[i].Skipped != "" {
				t.Errorf("настил %d обязан считаться и не нести причину: %+v", got.Drifts[i].LayId, got.Drifts[i])
			}
		}
	})

	t.Run("невошедшие не добивают выборку до порога", func(t *testing.T) {
		// Два полных настила и три половинчатых: пять строк на экране, а фактов по-прежнему два.
		lays := []LayFactObservation{
			obs(1, "100", "106"), obs(2, "100", "106"),
			obs(3, "100", ""), obs(4, "100", ""), obs(5, "", "106"),
		}
		got := MaterialCoefficientSuggestionOf(lays, "m")
		if got.Status != CoefficientSuggestionTooFewFacts || got.LayCount != 2 {
			t.Fatalf("status = %v, LayCount = %d, want TOO_FEW_FACTS / 2", got.Status, got.LayCount)
		}
		if got.Suggested.Valid {
			t.Errorf("предложение выдано на двух фактах, разбавленных пустыми строками")
		}
	})

	t.Run("настил без факта — это НЕ дрейф ноль", func(t *testing.T) {
		// Если бы «нет факта» читалось как «сошлось», медиана притягивалась бы к нулю тем сильнее,
		// чем реже в цехе меряют. Здесь: один настил с дрейфом 0.06 и два без факта. Прочитай их
		// нулями — получишь три «факта» и медиану 0.
		lays := []LayFactObservation{obs(1, "100", "106"), obs(2, "100", ""), obs(3, "100", "")}
		got := MaterialCoefficientSuggestionOf(lays, "m")
		if got.Status != CoefficientSuggestionTooFewFacts {
			t.Fatalf("status = %v, want TOO_FEW_FACTS", got.Status)
		}
		if got.MedianDrift.Valid && got.MedianDrift.Decimal.IsZero() {
			t.Errorf("настилы без факта прочитаны как «сошлось» — медиана поехала к нулю")
		}
	})
}

// TestCoefficientDriftDoesNotDivideAcrossUnits — метры, делённые на килограммы, дают число, которое
// выглядит как дрейф. Р3 объявляет факт в единице артикула; настил, нарушающий это, НАЗЫВАЕТСЯ и не
// считается. Пустая единица с любой стороны — отсутствие улики, а не улика: такой настил считается,
// иначе артикул молча провалился бы ниже порога при полном журнале замеров.
func TestCoefficientDriftDoesNotDivideAcrossUnits(t *testing.T) {
	kg := func(id int) LayFactObservation {
		o := obs(id, "100", "106")
		o.ActualUom = "kg"
		return o
	}
	t.Run("замер в чужой единице выброшен и назван", func(t *testing.T) {
		got := MaterialCoefficientSuggestionOf([]LayFactObservation{kg(1), kg(2), kg(3)}, "m")
		if got.Status != CoefficientSuggestionTooFewFacts || got.LayCount != 0 {
			t.Fatalf("status = %v, LayCount = %d, want TOO_FEW_FACTS / 0", got.Status, got.LayCount)
		}
		if !strings.Contains(got.Drifts[0].Skipped, "kg") || !strings.Contains(got.Drifts[0].Skipped, "m") {
			t.Errorf("причина обязана назвать ОБЕ единицы: %q", got.Drifts[0].Skipped)
		}
	})
	t.Run("единица не заполнена — настил считается", func(t *testing.T) {
		got := MaterialCoefficientSuggestionOf(
			[]LayFactObservation{obs(1, "100", "106"), obs(2, "100", "106"), obs(3, "100", "106")}, "m")
		if got.LayCount != 3 {
			t.Fatalf("LayCount = %d, want 3 — отсутствие единицы не является расхождением", got.LayCount)
		}
	})
	t.Run("единица артикула неизвестна — сверки нет", func(t *testing.T) {
		got := MaterialCoefficientSuggestionOf([]LayFactObservation{kg(1), kg(2), kg(3)}, "")
		if got.LayCount != 3 {
			t.Fatalf("LayCount = %d, want 3 — расхождение нельзя придумать из отсутствующих данных", got.LayCount)
		}
	})
	t.Run("регистр и пробелы единицу не меняют", func(t *testing.T) {
		o := obs(1, "100", "106")
		o.ActualUom = " M "
		if d := LayFactDriftOf(o, "m"); !d.Counted() {
			t.Errorf(" M  и m — одна единица: %+v", d)
		}
	})
}

// ------------------------------------------------------ множитель, а не дрейф; и его диапазон

// TestCoefficientSuggestionIsAStorableMultiplier — 0270 хранит коэффициент МНОЖИТЕЛЕМ (1.0300) в
// DECIMAL(6,4). Спека формулирует предложение как медиану ДРЕЙФОВ, поэтому наружу едут оба числа, и
// они обязаны быть согласованы арифметически, а не дисциплиной: Suggested = MedianDrift + 1 ТОЧНО.
func TestCoefficientSuggestionIsAStorableMultiplier(t *testing.T) {
	// Дрейф с бесконечным делением: 100 / 3 не представимо конечной дробью.
	lays := []LayFactObservation{obs(1, "3", "4"), obs(2, "3", "4"), obs(3, "3", "4")}
	got := MaterialCoefficientSuggestionOf(lays, "m")
	if got.Status != CoefficientSuggestionReady {
		t.Fatalf("status = %v (%s), want READY", got.Status, got.Detail)
	}
	if got.Suggested.Decimal.Exponent() < -coefficientScale {
		t.Errorf("предложение %s точнее, чем DECIMAL(6,4) — поле его не примет",
			got.Suggested.Decimal.String())
	}
	if !got.Suggested.Decimal.Sub(got.MedianDrift.Decimal).Equal(decimal.NewFromInt(1)) {
		t.Errorf("предложение %s и дрейф %s разошлись — на экране это два числа, которые не сходятся",
			got.Suggested.Decimal.String(), got.MedianDrift.Decimal.String())
	}
	if got.Suggested.Decimal.LessThan(decimal.NewFromInt(1)) {
		t.Errorf("предложение %s меньше единицы — EffectiveCuttingCoefficient прочитает его как «не задано»",
			got.Suggested.Decimal.String())
	}
	// И отрицание: предложение НЕ РАВНО голому дрейфу. Тот, кто отдаст наружу медиану дрейфов вместо
	// множителя, получит артикул с коэффициентом 0.33, то есть с «не задано».
	if got.Suggested.Decimal.Equal(got.MedianDrift.Decimal) {
		t.Errorf("предложение равно дрейфу — забыт +1, которого 0270 требует по построению")
	}
}

// TestCoefficientSuggestionOutOfRangeShowsTheMeasurementAndWithholdsTheNumber — измерение может
// вывалиться за [1, 3] (систематически завышенный план вниз, опечатки вверх). Число-предложение
// тогда НЕ ОТДАЁТСЯ и НЕ ПРИЖИМАЕТСЯ к границе, а измерение остаётся видимым.
func TestCoefficientSuggestionOutOfRangeShowsTheMeasurementAndWithholdsTheNumber(t *testing.T) {
	t.Run("ниже единицы: план систематически завышен", func(t *testing.T) {
		lays := []LayFactObservation{obs(1, "100", "90"), obs(2, "100", "90"), obs(3, "100", "90")}
		got := MaterialCoefficientSuggestionOf(lays, "m")
		if got.Status != CoefficientSuggestionOutOfRange {
			t.Fatalf("status = %v (%s), want OUT_OF_RANGE", got.Status, got.Detail)
		}
		if got.Suggested.Valid {
			t.Errorf("отдано несохраняемое предложение %s", got.Suggested.Decimal.String())
		}
		wantDec(t, got.MedianDrift, "-0.1", "медиана дрейфов")
		if got.LayCount != 3 || got.Detail == "" {
			t.Errorf("измерение обязано остаться видимым с его опорой: %+v", got)
		}
	})
	t.Run("выше трёх: очевидная ошибка ввода", func(t *testing.T) {
		lays := []LayFactObservation{obs(1, "100", "500"), obs(2, "100", "500"), obs(3, "100", "500")}
		got := MaterialCoefficientSuggestionOf(lays, "m")
		if got.Status != CoefficientSuggestionOutOfRange || got.Suggested.Valid {
			t.Fatalf("status = %v, suggested = %+v, want OUT_OF_RANGE без числа", got.Status, got.Suggested)
		}
		if !strings.Contains(got.Detail, "measurements") {
			t.Errorf("detail обязан отправить человека в замеры: %q", got.Detail)
		}
	})
	t.Run("ровно на границе — предложение есть", func(t *testing.T) {
		lays := []LayFactObservation{obs(1, "100", "100"), obs(2, "100", "100"), obs(3, "100", "100")}
		got := MaterialCoefficientSuggestionOf(lays, "m")
		if got.Status != CoefficientSuggestionReady {
			t.Fatalf("status = %v (%s), want READY: 1.0 — законное значение поля", got.Status, got.Detail)
		}
		wantDec(t, got.Suggested, "1", "предложение")
	})
}

// ----------------------------------------------------------------- некруговость, в числах

// TestCalibrationIsCircularIfThePlanCarriesTheCoefficient makes Р4's most important sentence
// MEASURABLE instead of merely stated.
//
// План настила — чистая геометрия, БЕЗ коэффициента (решение Р4 фазы Ф4), поэтому факт/план — честная
// оценка того, что коэффициент обязан покрыть. Этот тест показывает, ЧТО именно ломается, если
// кто-нибудь «для единообразия» умножит план настила на коэффициент артикула: дрейф схлопывается в
// ноль, предложение становится 1.0, и калибровка начинает подтверждать сама себя — молча, продолжая
// выдавать числа. Поймать такую правку в ЭТОМ файле нечем (план приходит значением), поэтому
// поймано хотя бы её следствие, с точными числами.
func TestCalibrationIsCircularIfThePlanCarriesTheCoefficient(t *testing.T) {
	// Артикул садится на 6%: геометрия говорит 100, цех расходует 106.
	honest := []LayFactObservation{obs(1, "100", "106"), obs(2, "100", "106"), obs(3, "100", "106")}
	// Та же реальность, но план уже умножен на действующий коэффициент 1.06.
	circular := []LayFactObservation{obs(1, "106", "106"), obs(2, "106", "106"), obs(3, "106", "106")}

	h := MaterialCoefficientSuggestionOf(honest, "m")
	c := MaterialCoefficientSuggestionOf(circular, "m")
	wantDec(t, h.Suggested, "1.06", "предложение по чистой геометрии")
	wantDec(t, c.Suggested, "1", "предложение по плану, в который коэффициент уже зашит")

	if h.Suggested.Decimal.Equal(c.Suggested.Decimal) {
		t.Fatalf("два входа неразличимы — тест перестал показывать разницу")
	}
	// Именно так выглядит круг: коэффициент 1.06 «подтверждается» предложением 1.0, то есть
	// предложением снять его — после чего дрейф вернётся к 0.06, и так до бесконечности.
	if !c.Suggested.Decimal.Equal(decimal.NewFromInt(1)) {
		t.Errorf("круговой вход дал %s; ожидалась ровно единица — величина, зависящая от числа "+
			"пересчётов, а не от того, сколько ткани ушло", c.Suggested.Decimal.String())
	}
}
