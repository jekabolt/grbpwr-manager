package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ПРАВИЛО ПРЕДЛОЖЕНИЯ ПРОЦЕНТА РАСКРОЯ (T7 волна 2) — §9 спеки: порог, медиана чёт/нечёт, каждая
// skip-причина, OUT_OF_RANGE без прижатия, netto-композиция секции × состав × пер-размерная норма.

// wastageCard — карточка с одним рулонным слотом (id 5, единица «м» кириллицей НАРОЧНО: сверка
// единиц обязана идти через перечень, не строкой) и тремя колорвеями: у 100 dxf-норма с
// пер-размерными значениями И строка детали с завышенным расходом (T8: строку детали netto обязан
// игнорировать), у 200 — manual-норма, у 300 — только плоская dxf-норма.
func wastageCard() *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 5, LineKey: "B1", Section: entity.BomSectionFabric, Name: "основная",
			Unit:       sql.NullString{String: "м", Valid: true},
			MaterialId: sql.NullInt64{Int64: 42, Valid: true}},
	}
	dxf := sql.NullString{String: entity.ConsumptionSourceDxf, Valid: true}
	card.Colorways = []entity.TechCardColorway{
		{
			ProductId: sql.NullInt32{Int32: 100, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				// Строка ДЕТАЛИ стоит ПЕРВОЙ и несёт абсурдный расход: возьми её netto — тест
				// взорвётся числом, а не разойдётся на проценты.
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, PieceId: sql.NullInt64{Int64: 9, Valid: true},
					ConsumptionSource: dxf,
					Consumption:       decimal.NullDecimal{Decimal: decimal.RequireFromString("999"), Valid: true}},
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, ConsumptionSource: dxf,
					SizeConsumptions: []entity.TechCardBomSizeConsumption{
						{SizeId: 1, Consumption: decimal.RequireFromString("1.20")},
						{SizeId: 2, Consumption: decimal.RequireFromString("1.40")},
					}},
			},
		},
		{
			ProductId: sql.NullInt32{Int32: 200, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true},
					ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
					Consumption:       decimal.NullDecimal{Decimal: decimal.RequireFromString("1.30"), Valid: true}},
			},
		},
		{
			ProductId: sql.NullInt32{Int32: 300, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, ConsumptionSource: dxf,
					Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("1.50"), Valid: true}},
			},
		},
		// 400: ДВЕ dxf-строки «на изделие» на ОДИН слот (легально; пример ревью MAJOR 2 — 0.6 и
		// 0.4). 500: dxf + manual на одном слоте — знаменатель неполон по построению.
		{
			ProductId: sql.NullInt32{Int32: 400, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, ConsumptionSource: dxf,
					Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.60"), Valid: true}},
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, ConsumptionSource: dxf,
					Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.40"), Valid: true}},
			},
		},
		{
			ProductId: sql.NullInt32{Int32: 500, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true}, ConsumptionSource: dxf,
					Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.60"), Valid: true}},
				{BomItemId: sql.NullInt64{Int64: 5, Valid: true},
					ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceManual, Valid: true},
					Consumption:       decimal.NullDecimal{Decimal: decimal.RequireFromString("0.40"), Valid: true}},
			},
		},
	}
	return card
}

func TestLayNettoOfComposesSectionsCompositionAndPerSizeNorm(t *testing.T) {
	card := wastageCard()
	// 10 слоёв × (2 изделия р.1 × 1.20 + 1 изделие р.2 × 1.40) = 10 × 3.80 = 38.000.
	res := LayNettoOf(card, 100, 5, "m", []LayNettoSection{{
		Plies:       10,
		MarkerLabel: "раскладка А",
		Composition: []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 2}, {SizeId: 2, Quantity: 1}},
	}})
	require.Empty(t, res.Reason)
	require.True(t, res.Qty.Valid)
	require.Equal(t, "38", res.Qty.Decimal.String(),
		"netto = Σ по секциям (слои × Σ по составу (кол-во × норма размера)); строка ДЕТАЛИ с расходом 999 не участвует (T8)")

	t.Run("две секции складываются", func(t *testing.T) {
		res := LayNettoOf(card, 100, 5, "m", []LayNettoSection{
			{Plies: 10, MarkerLabel: "А", Composition: []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 2}, {SizeId: 2, Quantity: 1}}},
			{Plies: 2, MarkerLabel: "Б", Composition: []entity.MarkerCompositionEntry{{SizeId: 2, Quantity: 1}}},
		})
		require.Empty(t, res.Reason)
		require.Equal(t, "40.8", res.Qty.Decimal.String(), "38 + 2×1.40")
	})

	t.Run("плоская dxf-норма покрывает все размеры (лестница usageNormForSize)", func(t *testing.T) {
		res := LayNettoOf(card, 300, 5, "m", []LayNettoSection{{
			Plies: 2, MarkerLabel: "А",
			Composition: []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 1}, {SizeId: 9, Quantity: 1}},
		}})
		require.Empty(t, res.Reason)
		require.Equal(t, "6", res.Qty.Decimal.String(), "2 слоя × (1.50 + 1.50)")
	})

	t.Run("единицы сверяются перечнем: «м» слота и \"m\" факта — одна единица", func(t *testing.T) {
		res := LayNettoOf(card, 100, 5, "m", []LayNettoSection{{
			Plies: 1, MarkerLabel: "А", Composition: []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 1}}}})
		require.Empty(t, res.Reason, "сырое сравнение строк объявило бы «м» и \"m\" разными и выбросило бы каждый настил")
	})
}

// MAJOR 2 (правки ревью T7в2): несколько строк «на изделие» одного слота СУММИРУЮТСЯ — сервер
// всюду их складывает, и netto обязано так же; смесь источников — отказ, не молчаливое смешение.
func TestLayNettoOfSumsSlotLines(t *testing.T) {
	card := wastageCard()
	sections := []LayNettoSection{{Plies: 1, MarkerLabel: "А",
		Composition: []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 10}}}}

	t.Run("две dxf-строки 0.6 и 0.4 — netto 10, не 6 и не 4", func(t *testing.T) {
		res := LayNettoOf(card, 400, 5, "m", sections)
		require.Empty(t, res.Reason)
		require.Equal(t, "10", res.Qty.Decimal.String(),
			"первая строка вместо суммы занизила бы знаменатель и показала бы +100% там, где отход 20%")
	})

	t.Run("dxf + manual на одном слоте — знаменатель неполон, отказ с названным источником", func(t *testing.T) {
		res := LayNettoOf(card, 500, 5, "m", sections)
		require.False(t, res.Qty.Valid)
		require.Contains(t, res.Reason, "manual")
		require.Contains(t, res.Reason, "incomplete")
	})

	t.Run("отказ не зависит от порядка строк рецепта", func(t *testing.T) {
		reversed := wastageCard()
		cw := &reversed.Colorways[4] // 500
		cw.Usages[0], cw.Usages[1] = cw.Usages[1], cw.Usages[0]
		res := LayNettoOf(reversed, 500, 5, "m", sections)
		require.False(t, res.Qty.Valid,
			"manual первой строкой раньше отбрасывал настил, dxf первой — молча брал её одну: результат зависел от порядка")
		require.Contains(t, res.Reason, "manual")
	})
}

// BLOCKER 2 (правки ревью T7в2): пустая или неизвестная перечню единица — отказ с причиной, ровно
// как явное несовпадение. Дробь факт/netto при неопределённой размерности — число ни о чём.
func TestLayNettoOfRefusesUndefinedUnits(t *testing.T) {
	sections := []LayNettoSection{{Plies: 1, MarkerLabel: "А",
		Composition: []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 1}}}}

	t.Run("пустая единица слота", func(t *testing.T) {
		card := wastageCard()
		card.BomItems[0].Unit = sql.NullString{}
		res := LayNettoOf(card, 100, 5, "kg", sections)
		require.False(t, res.Qty.Valid,
			"пример ревью: dxf 1.2 × 10 изделий при пустой единице слота записал бы netto 12 под фактом 14.4 kg — «+20%» неизвестной размерности")
		require.Contains(t, res.Reason, "names no unit")
	})

	t.Run("единица слота вне перечня", func(t *testing.T) {
		card := wastageCard()
		card.BomItems[0].Unit = sql.NullString{String: "погонаж", Valid: true}
		res := LayNettoOf(card, 100, 5, "m", sections)
		require.False(t, res.Qty.Valid)
		require.Contains(t, res.Reason, "погонаж", "причина называет строку, которую надо чинить")
	})

	t.Run("пустая единица факта", func(t *testing.T) {
		res := LayNettoOf(wastageCard(), 100, 5, "", sections)
		require.False(t, res.Qty.Valid)
		require.Contains(t, res.Reason, "the actual names no unit")
	})

	t.Run("единица факта вне перечня", func(t *testing.T) {
		res := LayNettoOf(wastageCard(), 100, 5, "bushel", sections)
		require.False(t, res.Qty.Valid)
		require.Contains(t, res.Reason, "bushel")
	})
}

func TestLayNettoOfNamesEveryRefusal(t *testing.T) {
	card := wastageCard()
	sections := []LayNettoSection{{Plies: 1, MarkerLabel: "А",
		Composition: []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 1}}}}

	cases := map[string]struct {
		card      *entity.TechCard
		colorway  int
		bomItemID int64
		uom       string
		sections  []LayNettoSection
		contains  string
	}{
		"карточка не загружена":                                      {nil, 100, 5, "m", sections, "isn't loaded"},
		"настил потерял слот":                                        {card, 100, 0, "m", sections, "lost its BOM slot"},
		"слота нет в карточке":                                       {card, 100, 999, "m", sections, "isn't in the card"},
		"manual-норма не годится в netto (это оценка, не измерение)": {card, 200, 5, "m", sections, "manual"},
		"нет нормы вовсе":                                            {card, 999, 5, "m", sections, "no dxf norm"},
		"факт в килограммах против нормы в метрах":                   {card, 100, 5, "kg", sections, "can't be reconciled"},
		"нет секций":                                                 {card, 100, 5, "m", nil, "has no sections"},
		"раскладка без состава": {card, 100, 5, "m",
			[]LayNettoSection{{Plies: 1, MarkerLabel: "раскладка Б"}}, "no composition recorded"},
		"размер состава не покрыт нормой": {card, 100, 5, "m",
			[]LayNettoSection{{Plies: 1, MarkerLabel: "А",
				Composition: []entity.MarkerCompositionEntry{{SizeId: 3, Quantity: 1}}}}, "size #3"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := LayNettoOf(tc.card, tc.colorway, tc.bomItemID, tc.uom, tc.sections)
			require.False(t, res.Qty.Valid)
			require.Contains(t, res.Reason, tc.contains,
				"невычислимое netto обязано НАЗВАТЬ причину: молчаливый пропуск выглядит как «фактов мало» при полном журнале")
		})
	}
}

// obsOf — наблюдение с готовым netto (как со штампа): дрейф теста читается глазами.
func obsOf(id int, cardID int, netto, actual string) LayWastageObservation {
	o := LayWastageObservation{LayId: id, LayKey: "L", TechCardId: cardID}
	if netto != "" {
		o.NettoQty = decimal.NullDecimal{Decimal: decimal.RequireFromString(netto), Valid: true}
		o.NettoStamped = true
	}
	if actual != "" {
		o.ActualQty = decimal.NullDecimal{Decimal: decimal.RequireFromString(actual), Valid: true}
	}
	return o
}

func TestWastageSuggestionThresholdIsAStateNotAnError(t *testing.T) {
	// Порог ПРИВЯЗАН к порогу коэффициента одной константой — решение владельца, не совпадение.
	require.Equal(t, MinLaysForCoefficientSuggestion, MinLaysForWastageSuggestion)

	got := BomWastageSuggestionOf([]LayWastageObservation{
		obsOf(1, 7, "10", "12"),
		obsOf(2, 7, "10", "12"),
	})
	require.Equal(t, WastageSuggestionTooFewFacts, got.Status)
	require.Equal(t, 2, got.LayCount)
	require.False(t, got.MedianPercent.Valid, "медиана из двух замеров не отдаётся даже как измерение")
	require.False(t, got.SuggestedPercent.Valid)
	require.NotEmpty(t, got.Detail)
	require.Len(t, got.Drifts, 2, "вошедшие дрейфы показываются и до порога — человек видит, как копится корпус")

	t.Run("пустой вход — основной путь после выката, а не исключение", func(t *testing.T) {
		got := BomWastageSuggestionOf(nil)
		require.Equal(t, WastageSuggestionTooFewFacts, got.Status)
		require.Equal(t, 0, got.LayCount)
		require.NotEmpty(t, got.Detail)
	})
}

func TestWastageSuggestionMedianAndModelCount(t *testing.T) {
	t.Run("нечётная выборка — середина", func(t *testing.T) {
		got := BomWastageSuggestionOf([]LayWastageObservation{
			obsOf(1, 7, "10", "11"),   // +10%
			obsOf(2, 7, "10", "12"),   // +20%
			obsOf(3, 8, "10", "14.5"), // +45% — выброс, медиана его переживает
		})
		require.Equal(t, WastageSuggestionReady, got.Status)
		require.Equal(t, "20", got.MedianPercent.Decimal.String())
		require.Equal(t, "20", got.SuggestedPercent.Decimal.String(),
			"предложение и измерение — ОДНО число в ОДНОЙ единице: второй единицы (множителя) тут нет нарочно")
		require.Equal(t, 3, got.LayCount)
		require.Equal(t, 2, got.TechCardCount, "по каким моделям посчитано — часть ответа: число без состава непроверяемо")
	})

	t.Run("чётная выборка — полусумма средних", func(t *testing.T) {
		got := BomWastageSuggestionOf([]LayWastageObservation{
			obsOf(1, 7, "10", "11"), // +10%
			obsOf(2, 7, "10", "12"), // +20%
			obsOf(3, 7, "10", "13"), // +30%
			obsOf(4, 7, "10", "14"), // +40%
		})
		require.Equal(t, "25", got.MedianPercent.Decimal.String())
	})

	t.Run("округление один раз, до масштаба поля", func(t *testing.T) {
		got := BomWastageSuggestionOf([]LayWastageObservation{
			obsOf(1, 7, "3", "4"), // +33.333...%
			obsOf(2, 7, "3", "4"),
			obsOf(3, 7, "3", "4"),
		})
		require.Equal(t, "33.33", got.SuggestedPercent.Decimal.String(),
			"DECIMAL(5,2) поля не примет шестнадцать знаков деления — предложение обязано сохраняться как есть")
	})
}

func TestWastageSuggestionSkipsWithNamedReasons(t *testing.T) {
	noNetto := LayWastageObservation{LayId: 1, LayKey: "L1", TechCardId: 7,
		NettoReason: "the lay's tech card has no dxf norm for the slot — there is nothing to take netto from",
		ActualQty:   decimal.NullDecimal{Decimal: decimal.RequireFromString("12"), Valid: true}}
	noFact := obsOf(2, 7, "10", "")
	zeroFact := obsOf(3, 7, "10", "0")
	silentNoNetto := LayWastageObservation{LayId: 4, LayKey: "L4", TechCardId: 7,
		ActualQty: decimal.NullDecimal{Decimal: decimal.RequireFromString("12"), Valid: true}}

	got := BomWastageSuggestionOf([]LayWastageObservation{noNetto, noFact, zeroFact, silentNoNetto})
	require.Equal(t, WastageSuggestionTooFewFacts, got.Status)
	require.Equal(t, 0, got.LayCount)
	require.Len(t, got.Drifts, 4, "невошедшие не исчезают — они называются")
	for i, want := range []string{"no dxf norm", "isn't entered", "isn't positive", "isn't computed"} {
		require.False(t, got.Drifts[i].Counted())
		require.Contains(t, got.Drifts[i].Skipped, want, "строка без числа и без объяснения неотличима от бага")
	}
}

func TestWastageSuggestionOutOfRangeShowsMeasurementWithoutClamping(t *testing.T) {
	t.Run("выше 100 — не прижимается к потолку", func(t *testing.T) {
		got := BomWastageSuggestionOf([]LayWastageObservation{
			obsOf(1, 7, "10", "25"), // +150%
			obsOf(2, 7, "10", "26"),
			obsOf(3, 7, "10", "27"),
		})
		require.Equal(t, WastageSuggestionOutOfRange, got.Status)
		require.Equal(t, "160", got.MedianPercent.Decimal.String(), "измерение остаётся фактом, когда поле его не принимает")
		require.False(t, got.SuggestedPercent.Valid, "число-предложение НЕ отдаётся и НЕ прижимается к 100")
		require.True(t, strings.Contains(got.Detail, "160"), got.Detail)
	})

	t.Run("ниже нуля — ткани ушло меньше безотходной нормы, это разговор о замерах", func(t *testing.T) {
		got := BomWastageSuggestionOf([]LayWastageObservation{
			obsOf(1, 7, "10", "9.5"), // −5%
			obsOf(2, 7, "10", "9.5"),
			obsOf(3, 7, "10", "9.5"),
		})
		require.Equal(t, WastageSuggestionOutOfRange, got.Status)
		require.Equal(t, "-5", got.MedianPercent.Decimal.String())
		require.False(t, got.SuggestedPercent.Valid, "зажать −5% в 0 значило бы сказать «выпадов нет» вместо «netto-норма завышена»")
	})

	t.Run("границы поля включительно", func(t *testing.T) {
		got := BomWastageSuggestionOf([]LayWastageObservation{
			obsOf(1, 7, "10", "20"), // ровно +100%
			obsOf(2, 7, "10", "20"),
			obsOf(3, 7, "10", "20"),
		})
		require.Equal(t, WastageSuggestionReady, got.Status)
		require.Equal(t, "100", got.SuggestedPercent.Decimal.String())

		got = BomWastageSuggestionOf([]LayWastageObservation{
			obsOf(1, 7, "10", "10"), // ровно 0%
			obsOf(2, 7, "10", "10"),
			obsOf(3, 7, "10", "10"),
		})
		require.Equal(t, WastageSuggestionReady, got.Status)
		require.Equal(t, "0", got.SuggestedPercent.Decimal.String())
	})
}
