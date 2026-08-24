package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// СЧЁТНАЯ НОРМА СЧИТАЕТСЯ ПАРОЙ (КОЛОРВЕЙ × СЛОТ), А НЕ СТРОКОЙ (0333).
//
// Каждое утверждение ниже держит одну границу правила из шапки countable.go. Главный из них —
// TestCountableSlotAppliesOnceAcrossPlacements: он обязан падать на наивной реализации
// `COALESCE(usage.quantity, slot.qty_per_garment)`, потому что она умножила бы и количество, и
// запас на число размещений слота в одном колорвее (0295 такие размещения разрешает дословно).
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ЭТОТ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены) — без них тест доказывал бы
// только то, что он компилируется:
//  1. CountablePairRowTotal отдаёт остаток КАЖДОЙ строке (isCountablePairCarrier → true):
//     TestCountableSlotAppliesOnceAcrossPlacements = 28 вместо 14, TestCountableRowSharesSumToPair
//     назвал расхождение суммы с итогом. Наивный COALESCE ведёт себя ровно так.
//  2. CountablePairTotal перестаёт прибавлять spare_qty: падают три утверждения о закупке при
//     нетронутых утверждениях о пришивании — то есть тесты видят разницу между двумя числами.

func cnDec(v string) decimal.NullDecimal {
	return decimal.NewNullDecimal(decimal.RequireFromString(v))
}

// cnSlot — счётный слот с ценой 2 за штуку; mut правит его до возврата.
func cnSlot(mut func(*TechCardBomItem)) *TechCardBomItem {
	b := &TechCardBomItem{
		Id:        77,
		Section:   BomSectionHardware,
		Name:      "Horn button 18L",
		UnitPrice: cnDec("2"),
	}
	if mut != nil {
		mut(b)
	}
	return b
}

// cnRecipe — рецепт колорвея. Строки создаются В СРЕЗЕ и адресуются указателями в него же: пара
// обязана строиться из того самого среза, по которому идёт читатель (см. CountablePairUsages).
func cnRecipe(rows ...TechCardColorwayUsage) []TechCardColorwayUsage { return rows }

func cnRow(slotID int64, mut func(*TechCardColorwayUsage)) TechCardColorwayUsage {
	u := TechCardColorwayUsage{BomItemId: sql.NullInt64{Int64: slotID, Valid: true}}
	if mut != nil {
		mut(&u)
	}
	return u
}

// TestCountableBasisPicksTheOwner — четыре состояния пары и ни одно из них не ноль.
func TestCountableBasisPicksTheOwner(t *testing.T) {
	slotOnly := cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6") })
	rec := cnRecipe(cnRow(77, nil))
	qty, basis := CountablePairQty(CountablePairUsages(rec, slotOnly), slotOnly)
	require.Equal(t, CountableBasisSlot, basis)
	require.Equal(t, "6", qty.Decimal.String())

	// Явное число строки — переопределение: значение слота не читается вовсе, а не складывается.
	rec = cnRecipe(cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("4") }))
	qty, basis = CountablePairQty(CountablePairUsages(rec, slotOnly), slotOnly)
	require.Equal(t, CountableBasisRows, basis)
	require.Equal(t, "4", qty.Decimal.String(), "6 слота не участвует в арифметике переопределения")

	// Ни там, ни там — INVALID, а не ноль: ноль это утверждение «ни одной».
	bare := cnSlot(nil)
	qty, basis = CountablePairQty(CountablePairUsages(cnRecipe(cnRow(77, nil)), bare), bare)
	require.False(t, qty.Valid)
	require.Equal(t, CountableBasisNone, basis)

	// Слот с числом, который не поминает ни одна строка рецепта, не считается: пары нет.
	qty, basis = CountablePairQty(CountablePairUsages(nil, slotOnly), slotOnly)
	require.False(t, qty.Valid)
	require.Equal(t, CountableBasisNone, basis)
}

// TestCountableSlotIsIgnoredOnAMeasuredSection — граница «счётное/мерное» держится в резолвере,
// а не у заполняющего: число, попавшее на мерный слот мимо валидации store, деньгами не станет.
func TestCountableSlotIsIgnoredOnAMeasuredSection(t *testing.T) {
	for _, s := range MeasuredSectionList {
		b := cnSlot(func(b *TechCardBomItem) { b.Section = s; b.QtyPerGarment = cnDec("6") })
		rec := cnRecipe(cnRow(77, nil))
		qty, basis := CountablePairQty(CountablePairUsages(rec, b), b)
		require.False(t, qty.Valid, "section %q", s)
		require.Equal(t, CountableBasisNone, basis, "section %q", s)

		// Собственное число легаси-строки на мерной секции продолжает считаться, как считалось.
		rec = cnRecipe(cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("3") }))
		row := CountablePairRowTotal(CountablePairUsages(rec, b), &rec[0], b)
		require.True(t, row.Valid, "section %q", s)
		require.Equal(t, "3", row.Decimal.String(), "section %q", s)
	}
}

// TestCountableSpareIsBoughtButNotSewn — два разных числа об одной паре, и путать их нельзя.
func TestCountableSpareIsBoughtButNotSewn(t *testing.T) {
	slot := cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6"); b.SpareQty = cnDec("1") })
	rec := cnRecipe(cnRow(77, nil))
	pair := CountablePairUsages(rec, slot)

	sewn, _ := CountablePairQty(pair, slot)
	require.Equal(t, "6", sewn.Decimal.String(), "запас не пришивается")
	bought, _ := CountablePairTotal(pair, slot)
	require.Equal(t, "7", bought.Decimal.String(), "запас закупается")

	// Запас БЕЗ количества — недописанное утверждение, а не закупка одной запасной ни к чему.
	spareOnly := cnSlot(func(b *TechCardBomItem) { b.SpareQty = cnDec("1") })
	bought, basis := CountablePairTotal(CountablePairUsages(cnRecipe(cnRow(77, nil)), spareOnly), spareOnly)
	require.False(t, bought.Valid)
	require.Equal(t, CountableBasisNone, basis)
}

// TestCountableSlotAppliesOnceAcrossPlacements — РЕГРЕССИЯ Д1, главный тест фазы.
//
// 0295 дословно разрешает строкам «на изделие» повторять слот с разными placement («пуговицы —
// планка» / «пуговицы — манжета»). Наивное построчное чтение слотового числа дало бы 12 пуговиц
// и два запаса вместо 6 + 1.
func TestCountableSlotAppliesOnceAcrossPlacements(t *testing.T) {
	slot := cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6"); b.SpareQty = cnDec("1") })
	rec := cnRecipe(
		cnRow(77, func(u *TechCardColorwayUsage) { u.Placement = sql.NullString{String: "planka", Valid: true} }),
		cnRow(77, func(u *TechCardColorwayUsage) { u.Placement = sql.NullString{String: "cuff", Valid: true} }),
	)
	pair := CountablePairUsages(rec, slot)
	require.Len(t, pair, 2)

	bought, basis := CountablePairTotal(pair, slot)
	require.Equal(t, CountableBasisSlot, basis)
	require.Equal(t, "7", bought.Decimal.String(), "6 + 1 ОДИН РАЗ на пару, не 12 + 2")

	// Деньги: 7 × 2 = 14 на всю пару. Носитель — первая строка, вторая вносит валидный НОЛЬ
	// (не INVALID: INVALID означал бы «нормы нет» и поднял бы ложное замечание в смете).
	first := rec[0].LineTotal(slot, pair)
	second := rec[1].LineTotal(slot, pair)
	require.True(t, first.Valid)
	require.True(t, second.Valid)
	require.Equal(t, "14", first.Decimal.String())
	require.Equal(t, "0", second.Decimal.String())
	require.Equal(t, "14", first.Decimal.Add(second.Decimal).String(), "не 28")
}

// TestCountableExplicitRowsSumAndSlotStaysSilent — базис «строки»: Σ явных, значение слота не
// читается, запас прибавляется ОДИН раз.
func TestCountableExplicitRowsSumAndSlotStaysSilent(t *testing.T) {
	slot := cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6"); b.SpareQty = cnDec("1") })

	// 4 + 2 явных + 1 запас = 7 × 2 = 14. Шестёрка слота не участвует.
	rec := cnRecipe(
		cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("4") }),
		cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("2") }),
	)
	pair := CountablePairUsages(rec, slot)
	bought, basis := CountablePairTotal(pair, slot)
	require.Equal(t, CountableBasisRows, basis)
	require.Equal(t, "7", bought.Decimal.String())
	require.Equal(t, "10", rec[0].LineTotal(slot, pair).Decimal.String(), "(4 + запас 1) × 2")
	require.Equal(t, "4", rec[1].LineTotal(slot, pair).Decimal.String(), "2 × 2")

	// Одна явная 8 и одна пустая: итог = Σ ЯВНЫХ (8), а не 8 + 6. Пустая строка пары не
	// «наследует» слот второй раз — иначе одно размещение оплачивалось бы дважды.
	rec = cnRecipe(
		cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("8") }),
		cnRow(77, nil),
	)
	pair = CountablePairUsages(rec, slot)
	bought, basis = CountablePairTotal(pair, slot)
	require.Equal(t, CountableBasisRows, basis)
	require.Equal(t, "9", bought.Decimal.String(), "8 явных + 1 запас")
	require.Equal(t, "18", rec[0].LineTotal(slot, pair).Decimal.String())
	require.Equal(t, "0", rec[1].LineTotal(slot, pair).Decimal.String())
}

// TestCountableRowSharesSumToPair — ИНВАРИАНТ, ради которого CountablePairRowTotal существует:
// сумма долей строк равна итогу пары ровно, без остатка и без повтора. Именно он позволяет
// читателям остаться построчными.
func TestCountableRowSharesSumToPair(t *testing.T) {
	cases := map[string]struct {
		slot *TechCardBomItem
		rec  []TechCardColorwayUsage
	}{
		"слот, одно размещение": {
			cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6"); b.SpareQty = cnDec("1") }),
			cnRecipe(cnRow(77, nil)),
		},
		"слот, три размещения": {
			cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6"); b.SpareQty = cnDec("1") }),
			cnRecipe(cnRow(77, nil), cnRow(77, nil), cnRow(77, nil)),
		},
		"строки, две явных": {
			cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6"); b.SpareQty = cnDec("2") }),
			cnRecipe(
				cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("4") }),
				cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("2.5") }),
			),
		},
		"строки, явная и пустая": {
			cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6") }),
			cnRecipe(cnRow(77, func(u *TechCardColorwayUsage) { u.Quantity = cnDec("8") }), cnRow(77, nil)),
		},
	}
	for name, c := range cases {
		pair := CountablePairUsages(c.rec, c.slot)
		total, _ := CountablePairTotal(pair, c.slot)
		require.True(t, total.Valid, name)
		sum := decimal.Zero
		for i := range c.rec {
			share := CountablePairRowTotal(pair, &c.rec[i], c.slot)
			require.True(t, share.Valid, "%s: строка %d", name, i)
			sum = sum.Add(share.Decimal)
		}
		require.Equal(t, total.Decimal.String(), sum.String(), name)
	}
}

// TestCountablePairExcludesPieceRowsAndLegacyRows — два carve-out'а, каждый по своей причине.
func TestCountablePairExcludesPieceRowsAndLegacyRows(t *testing.T) {
	slot := cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6"); b.SpareQty = cnDec("1") })

	// Строка, привязанная к детали, нормы не несёт (T8) — ни своей, ни доли пары.
	rec := cnRecipe(
		cnRow(77, func(u *TechCardColorwayUsage) { u.PieceId = sql.NullInt64{Int64: 3, Valid: true} }),
		cnRow(77, nil),
	)
	pair := CountablePairUsages(rec, slot)
	require.Len(t, pair, 1, "детальная строка в пару не входит")
	require.False(t, CountablePairRowTotal(pair, &rec[0], slot).Valid, "детальная строка денег нормы не несёт")
	require.Equal(t, "7", CountablePairRowTotal(pair, &rec[1], slot).Decimal.String())

	// Легаси-строка адресует слот позиционным индексом: в пару не группируется (carve-out 0295),
	// поэтому слотовое число к ней не применяется и она считается ровно как считалась.
	legacy := cnRecipe(TechCardColorwayUsage{
		BomItemIndex: sql.NullInt32{Int32: 0, Valid: true},
		Quantity:     cnDec("3"),
	})
	pair = CountablePairUsages(legacy, slot)
	require.Empty(t, pair, "строка без bom_item_id в пару не группируется")
	require.Equal(t, "3", CountablePairRowTotal(pair, &legacy[0], slot).Decimal.String())
	require.Equal(t, "6", legacy[0].LineTotal(slot, pair).Decimal.String(), "3 × 2, без слота и без запаса")
}

// TestCountableMoneyTakesNoGrossUp — граница обоих множителей. Процент раскроя и коэффициент
// рулона на счётной строке не начисляются НИКОГДА: 6 пуговиц остаются 6 пуговицами, а запасная
// тем более не кроится.
func TestCountableMoneyTakesNoGrossUp(t *testing.T) {
	slot := cnSlot(func(b *TechCardBomItem) {
		b.QtyPerGarment = cnDec("6")
		b.SpareQty = cnDec("1")
		b.WastagePercent = cnDec("50")
		b.CuttingCoefficient = cnDec("1.5")
	})
	rec := cnRecipe(cnRow(77, nil))
	pair := CountablePairUsages(rec, slot)
	require.Equal(t, "14", rec[0].LineTotal(slot, pair).Decimal.String(), "7 × 2, без 1.5 и без +50%")
}

// TestCountableSlotDoesNotShadowPerSizeConsumption — порядок ветвей в деньгах не изменился:
// пер-размерная норма по-прежнему уводит деньги в SizeRunTotal, что бы ни лежало на слоте.
func TestCountableSlotDoesNotShadowPerSizeConsumption(t *testing.T) {
	slot := cnSlot(func(b *TechCardBomItem) { b.QtyPerGarment = cnDec("6") })
	rec := cnRecipe(cnRow(77, func(u *TechCardColorwayUsage) {
		u.SizeConsumptions = []TechCardBomSizeConsumption{{SizeId: 4, Consumption: decimal.RequireFromString("2")}}
	}))
	pair := CountablePairUsages(rec, slot)
	require.False(t, rec[0].LineTotal(slot, pair).Valid, "деньги пер-размерной строки живут в SizeRunTotal")
}
