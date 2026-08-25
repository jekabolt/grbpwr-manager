package dto

import (
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Помощники сборки карточки. Названы с префиксом adv, чтобы не спорить за имена с соседними
// тестами пакета: файл читается вместе с ними одним компилятором.
func advDec(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}
func advI64(v int64) sql.NullInt64   { return sql.NullInt64{Int64: v, Valid: true} }
func advI32(v int32) sql.NullInt32   { return sql.NullInt32{Int32: v, Valid: true} }
func advStr(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

// advKeys — ключи замечаний в порядке выдачи. Сравнивается именно СПИСОК КЛЮЧЕЙ, а не «есть ли
// среди них нужный»: утверждение «проверка молчит» иначе проходило бы на карточке, где вместо
// молчания поднялось другое замечание.
func advKeys(got []TechCardAdvice) []string {
	if len(got) == 0 {
		return nil
	}
	out := make([]string, 0, len(got))
	for _, a := range got {
		out = append(out, a.Key)
	}
	return out
}

// advCard — карточка с одним живым колорвеем: минимум, на котором проверки 3-5 вообще говорят
// (все три спрашивают рецепт, а рецепт живёт на колорвее).
func advCard(bom []entity.TechCardBomItem, usages ...entity.TechCardColorwayUsage) *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		BomItems: bom,
		Colorways: []entity.TechCardColorway{{
			Id: 1, Name: "black", ColorCode: "BLK",
			Status: entity.ColorwayStatusActive, Usages: usages,
		}},
	}}
}

// TestTechCardAdvisoriesSpareKit — две половины одного утверждения «запас едет с изделием»: число
// на слоте говорит СКОЛЬКО, строка spare_kit_bag — ВО ЧТО. Порознь ни одна не исполнима, вместе —
// сказать нечего.
func TestTechCardAdvisoriesSpareKit(t *testing.T) {
	buttons := func(spare string) entity.TechCardBomItem {
		b := entity.TechCardBomItem{Id: 10, LineKey: "k-buttons", Name: "buttons", Section: entity.BomSectionHardware}
		if spare != "" {
			b.SpareQty = advDec(spare)
		}
		return b
	}
	bag := entity.TechCardBomItem{
		Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
		Kind: advStr(string(entity.BomKindSpareKitBag)),
	}

	tests := []struct {
		name string
		bom  []entity.TechCardBomItem
		want []string
	}{
		{
			name: "запас есть, пакетика нет",
			bom:  []entity.TechCardBomItem{buttons("2")},
			want: []string{AdviceSpareKitMissing},
		},
		{
			name: "пакетик есть, класть в него нечего",
			bom:  []entity.TechCardBomItem{buttons(""), bag},
			want: []string{AdviceSpareKitEmpty},
		},
		{
			name: "обе половины на месте — молчание",
			bom:  []entity.TechCardBomItem{buttons("2"), bag},
			want: nil,
		},
		{
			name: "ни запаса, ни пакетика — говорить не о чем",
			bom:  []entity.TechCardBomItem{buttons("")},
			want: nil,
		},
		{
			name: "запас НОЛЬ — это утверждение «запаса нет», а не пропуск",
			bom:  []entity.TechCardBomItem{{Id: 10, Name: "buttons", Section: entity.BomSectionHardware, SpareQty: advDec("0")}},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Строка рецепта на слот пуговиц есть всегда: иначе поднялось бы ещё и замечание 4, и
			// случай перестал бы говорить о пакетике.
			card := advCard(tt.bom, entity.TechCardColorwayUsage{BomItemId: advI64(10), Quantity: advDec("6")})
			require.Equal(t, tt.want, advKeys(TechCardAdvisories(card, nil, nil)))
		})
	}
}

// TestTechCardAdvisoriesSpareKitTextNamesBothHalves — фраза обязана называть ОБЕ половины, иначе
// оператор не знает, чего именно не хватает.
func TestTechCardAdvisoriesSpareKitText(t *testing.T) {
	card := advCard([]entity.TechCardBomItem{{Id: 10, Name: "buttons", Section: entity.BomSectionHardware, SpareQty: advDec("2")}},
		entity.TechCardColorwayUsage{BomItemId: advI64(10), Quantity: advDec("6")})
	got := TechCardAdvisories(card, nil, nil)
	require.Len(t, got, 1)
	require.Equal(t, "spare hardware is packed with the garment, but the card has no spare-kit bag line", got[0].Text)
}

// advAssemblyLine — одна живая строка сборочной ведомости на aux-карту 77.
func advAssemblyLine() entity.StyleAssembly {
	return entity.StyleAssembly{
		Id: 1, ComponentTechCardId: 77, Active: true, ComponentName: "care label",
		Qty: decimal.NewFromInt(1),
	}
}

// advVariant — цветовой вариант выхода aux-карты.
func advVariant(code string, materialID int, active, archived bool) entity.TechCardOutputVariant {
	return entity.TechCardOutputVariant{
		TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{
			Id: materialID, ColorCode: code, MaterialId: materialID, Active: active,
		},
		ColorName: code, MaterialName: "care label " + code, MaterialArchived: archived,
	}
}

// TestTechCardAdvisoriesAssemblyComponent — самое дорогое из пяти: сборочная ведомость до костинга
// не доходит вовсе, поэтому компонент, которого нет в спецификации, молча стоит ноль.
//
// Отдельно пинится МОЛЧАНИЕ на неразрешённом выходе: ретайренный цвет и архивный материал — это
// «не знаю, какое ведро», а не «ведра нет в спецификации».
func TestTechCardAdvisoriesAssemblyComponent(t *testing.T) {
	// Спецификация карточки: одна строка ткани с артикулом 500 — компонента 900 в ней нет.
	plainBom := []entity.TechCardBomItem{{
		Id: 1, LineKey: "k-fabric", Name: "main fabric",
		Section: entity.BomSectionFabric, MaterialId: advI64(500),
	}}
	tests := []struct {
		name     string
		bom      []entity.TechCardBomItem
		usages   []entity.TechCardColorwayUsage
		assembly []entity.StyleAssembly
		variants map[int][]entity.TechCardOutputVariant
		want     []string
	}{
		{
			name:     "выход разрешился, но в спецификации его нет",
			bom:      plainBom,
			assembly: []entity.StyleAssembly{advAssemblyLine()},
			variants: map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, false)}},
			want:     []string{AdviceAssemblyComponentNotInBom},
		},
		{
			name: "выход aux-карты стоит material_id строки BOM — молчание",
			bom: append(append([]entity.TechCardBomItem{}, plainBom...), entity.TechCardBomItem{
				Id: 2, LineKey: "k-care", Name: "care label",
				Section: entity.BomSectionLabel, MaterialId: advI64(900),
			}),
			assembly: []entity.StyleAssembly{advAssemblyLine()},
			variants: map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, false)}},
			want:     nil,
		},
		{
			name: "выход совпал не со слотом, а с ПИНОМ артикула на строке рецепта — молчание",
			bom: append(append([]entity.TechCardBomItem{}, plainBom...), entity.TechCardBomItem{
				Id: 2, LineKey: "k-care", Name: "care label", Section: entity.BomSectionLabel,
			}),
			usages: []entity.TechCardColorwayUsage{
				{BomItemId: advI64(2), MaterialId: advI64(900), Quantity: advDec("1")},
			},
			assembly: []entity.StyleAssembly{advAssemblyLine()},
			variants: map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, false)}},
			want:     nil,
		},
		{
			name:     "цвет изделия у компонента РЕТАЙРЕН — выход неразрешим, проверка молчит",
			bom:      plainBom,
			assembly: []entity.StyleAssembly{advAssemblyLine()},
			variants: map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, false, false)}},
			want:     nil,
		},
		{
			name:     "ведро компонента АРХИВНОЕ — выход неразрешим, проверка молчит",
			bom:      plainBom,
			assembly: []entity.StyleAssembly{advAssemblyLine()},
			variants: map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, true)}},
			want:     nil,
		},
		{
			name:     "у компонента вообще нет выхода — сказать нечего",
			bom:      plainBom,
			assembly: []entity.StyleAssembly{advAssemblyLine()},
			variants: nil,
			want:     nil,
		},
		{
			name: "выключенная строка ведомости на изделие не идёт",
			bom:  plainBom,
			assembly: []entity.StyleAssembly{func() entity.StyleAssembly {
				a := advAssemblyLine()
				a.Active = false
				return a
			}()},
			variants: map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, false)}},
			want:     nil,
		},
		{
			name: "легаси-выход одноцветной карточки тоже разрешается",
			bom:  plainBom,
			assembly: []entity.StyleAssembly{func() entity.StyleAssembly {
				a := advAssemblyLine()
				a.OutputMaterialId = advI32(900)
				a.OutputMaterialName = advStr("care label")
				return a
			}()},
			variants: nil,
			want:     []string{AdviceAssemblyComponentNotInBom},
		},
		{
			name: "две строки ведомости на один компонент дают ОДНО замечание",
			bom:  plainBom,
			assembly: []entity.StyleAssembly{advAssemblyLine(), func() entity.StyleAssembly {
				a := advAssemblyLine()
				a.Id = 2
				a.SizeId = advI32(3)
				return a
			}()},
			variants: map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, false)}},
			want:     []string{AdviceAssemblyComponentNotInBom},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := advCard(tt.bom, tt.usages...)
			require.Equal(t, tt.want, advKeys(TechCardAdvisories(card, tt.assembly, tt.variants)))
		})
	}
}

// TestTechCardAdvisoriesAssemblyComponentText — фраза называет КОМПОНЕНТ: без имени она отправляет
// искать вручную по всей ведомости.
func TestTechCardAdvisoriesAssemblyComponentText(t *testing.T) {
	card := advCard([]entity.TechCardBomItem{{Id: 1, Name: "main fabric", Section: entity.BomSectionFabric, MaterialId: advI64(500)}})
	got := TechCardAdvisories(card, []entity.StyleAssembly{advAssemblyLine()},
		map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, false)}})
	require.Len(t, got, 1)
	require.Equal(t,
		"care label: the assembly list names a component that is not in the BOM, so it will be neither costed nor purchased",
		got[0].Text)
}

// TestTechCardAdvisoriesCountableSlot — два состояния, в которых счётное число слота НЕ действует,
// хотя оператор его написал: слот вне рецепта и слот на по-размерной строке.
func TestTechCardAdvisoriesCountableSlot(t *testing.T) {
	countable := func() entity.TechCardBomItem {
		return entity.TechCardBomItem{
			Id: 10, LineKey: "k-buttons", Name: "buttons",
			Section: entity.BomSectionHardware, QtyPerGarment: advDec("6"),
		}
	}
	tests := []struct {
		name   string
		bom    []entity.TechCardBomItem
		usages []entity.TechCardColorwayUsage
		want   []string
	}{
		{
			name: "количество задано, но слот не поминает ни одна строка рецепта",
			bom:  []entity.TechCardBomItem{countable()},
			want: []string{AdviceCountableSlotUnused},
		},
		{
			name:   "тот же слот, поминаемый рецептом — молчание",
			bom:    []entity.TechCardBomItem{countable()},
			usages: []entity.TechCardColorwayUsage{{BomItemId: advI64(10)}},
			want:   nil,
		},
		{
			// ШОВ С CARVE-OUT'ОМ 0295. Легаси-строка адресует слот ПОЗИЦИОННЫМ индексом, и пара
			// (CountablePairUsages) её намеренно не берёт — в деньгах такие строки не группируются.
			// Но слот она потребляет и платит за него, поэтому обвинение «его не поминает ни один
			// рецепт» было бы ложным. Спрашивать надо оба пути резолва, как это делает костинг.
			name:   "слот поминает ЛЕГАСИ-строка, адресующая его позицией, — молчание",
			bom:    []entity.TechCardBomItem{countable()},
			usages: []entity.TechCardColorwayUsage{{BomItemIndex: advI32(0), Quantity: advDec("6")}},
			want:   nil,
		},
		{
			// Отрицательное утверждение рядом с предыдущим: строка, привязанная к ДЕТАЛИ, слот не
			// «поминает» в том смысле, о котором спрашивают деньги, — нормы она не несёт вовсе.
			name: "строка назначает материал ДЕТАЛИ — слот всё ещё никем не куплен",
			bom:  []entity.TechCardBomItem{countable()},
			usages: []entity.TechCardColorwayUsage{{
				BomItemId: advI64(10), PieceId: advI64(77),
			}},
			want: []string{AdviceCountableSlotUnused},
		},
		{
			name: "строка рецепта считается ПО РАЗМЕРАМ — счётное число не читается",
			bom:  []entity.TechCardBomItem{countable()},
			usages: []entity.TechCardColorwayUsage{{
				BomItemId: advI64(10),
				SizeConsumptions: []entity.TechCardBomSizeConsumption{
					{SizeId: 1, Consumption: decimal.NewFromInt(6)},
				},
			}},
			want: []string{AdviceCountableSlotSized},
		},
		{
			// countable.go отказывается прибавлять запас к отсутствующему основанию и ПРЯМО
			// перекладывает эту фразу на чек-лист. До шестого замечания её не говорил никто:
			// пакетик на карточке есть — обе половины проверки пакетика молчат; слот поминается
			// рецептом — молчит и «не входит ни в один рецепт»; а закуплено ноль.
			name: "запас есть, пришиваемого количества нет ни на слоте, ни на строке",
			bom: []entity.TechCardBomItem{{
				Id: 10, LineKey: "k-buttons", Name: "buttons", Section: entity.BomSectionHardware,
				SpareQty: advDec("2"),
			}, {
				Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
				Kind: advStr(string(entity.BomKindSpareKitBag)),
			}},
			usages: []entity.TechCardColorwayUsage{{BomItemId: advI64(10)}},
			want:   []string{AdviceCountableSpareWithoutQty},
		},
		{
			name: "тот же запас, но основание названо СТРОКОЙ рецепта — молчание",
			bom: []entity.TechCardBomItem{{
				Id: 10, LineKey: "k-buttons", Name: "buttons", Section: entity.BomSectionHardware,
				SpareQty: advDec("2"),
			}, {
				Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
				Kind: advStr(string(entity.BomKindSpareKitBag)),
			}},
			usages: []entity.TechCardColorwayUsage{{BomItemId: advI64(10), Quantity: advDec("6")}},
			want:   nil,
		},
		{
			name: "тот же запас, но основание названо СЛОТОМ — молчание",
			bom: []entity.TechCardBomItem{{
				Id: 10, LineKey: "k-buttons", Name: "buttons", Section: entity.BomSectionHardware,
				QtyPerGarment: advDec("6"), SpareQty: advDec("2"),
			}, {
				Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
				Kind: advStr(string(entity.BomKindSpareKitBag)),
			}},
			usages: []entity.TechCardColorwayUsage{{BomItemId: advI64(10)}},
			want:   nil,
		},
		{
			name: "слот НИЧЕГО счётного не сказал — конструкция пары молчит целиком",
			bom: []entity.TechCardBomItem{{
				Id: 10, LineKey: "k-buttons", Name: "buttons", Section: entity.BomSectionHardware,
			}},
			want: nil,
		},
		{
			name: "МЕРНЫЙ слот (ткань) с количеством не рождает ни одного из двух",
			bom: []entity.TechCardBomItem{{
				Id: 10, LineKey: "k-fabric", Name: "main fabric",
				Section: entity.BomSectionFabric, QtyPerGarment: advDec("6"),
			}},
			want: nil,
		},
		{
			name: "МЕРНЫЙ слот (нитка) на по-размерной строке — тоже ни одного",
			bom: []entity.TechCardBomItem{{
				Id: 10, LineKey: "k-thread", Name: "thread",
				Section: entity.BomSectionThread, QtyPerGarment: advDec("6"),
			}},
			usages: []entity.TechCardColorwayUsage{{
				BomItemId: advI64(10),
				SizeConsumptions: []entity.TechCardBomSizeConsumption{
					{SizeId: 1, Consumption: decimal.NewFromInt(2)},
				},
			}},
			want: nil,
		},
		{
			name: "строка-НАЗНАЧЕНИЕ детали слот не поминает: нормы она не несёт (T8)",
			bom:  []entity.TechCardBomItem{countable()},
			usages: []entity.TechCardColorwayUsage{{
				BomItemId: advI64(10), PieceId: advI64(4),
			}},
			want: []string{AdviceCountableSlotUnused},
		},
		{
			name: "ЗАПАС без пришиваемого числа — слот тоже несёт счётную норму",
			bom: []entity.TechCardBomItem{{
				Id: 10, LineKey: "k-buttons", Name: "buttons",
				Section: entity.BomSectionHardware, SpareQty: advDec("2"),
				Kind: advStr(string(entity.BomKindSpareKitBag)),
			}},
			want: []string{AdviceCountableSlotUnused},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := advCard(tt.bom, tt.usages...)
			require.Equal(t, tt.want, advKeys(TechCardAdvisories(card, nil, nil)))
		})
	}
}

// TestTechCardAdvisoriesSpareIsCountedPerPairNotPerCard — БАЗИС ЕСТЬ СВОЙСТВО ПАРЫ.
//
// Слот несёт только запас. Чёрный колорвей число сказал, белый — нет. Флаг «где-то на карточке
// число есть» подавил бы замечание ровно там, где оно нужно: белое изделие уезжает и без пуговиц,
// и без пакетика, а карточка отвечает «всё в порядке». Однопалитровые тесты этого не видят вовсе.
func TestTechCardAdvisoriesSpareIsCountedPerPairNotPerCard(t *testing.T) {
	bom := []entity.TechCardBomItem{{
		Id: 10, LineKey: "k-buttons", Name: "buttons",
		Section: entity.BomSectionHardware, SpareQty: advDec("2"),
	}, {
		Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
		Kind: advStr(string(entity.BomKindSpareKitBag)),
	}}
	card := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		BomItems: bom,
		Colorways: []entity.TechCardColorway{
			{Id: 1, Name: "black", ColorCode: "BLK", Status: entity.ColorwayStatusActive,
				Usages: []entity.TechCardColorwayUsage{{BomItemId: advI64(10), Quantity: advDec("6")}}},
			{Id: 2, Name: "white", ColorCode: "WHT", Status: entity.ColorwayStatusActive,
				Usages: []entity.TechCardColorwayUsage{{BomItemId: advI64(10)}}},
		},
	}}
	got := TechCardAdvisories(card, nil, nil)
	require.Equal(t, []string{AdviceCountableSpareWithoutQty}, advKeys(got))
	require.Contains(t, got[0].Text, "neither its units nor its spares",
		"колорвей без числа не покупает НИЧЕГО — фраза обязана сказать именно это")
}

// TestTechCardAdvisoriesLegacyRowNeverGetsTheSpare — ЛЕГАСИ-СТРОКА ПЛАТИТ ЗА СЕБЯ, НО ЗАПАС К НЕЙ
// НЕ ПРИБАВЛЯЕТСЯ НИКОГДА.
//
// Строка адресует слот позиционным индексом, поэтому в пару (carve-out 0295) не входит, а запас
// прибавляется РОВНО К ПАРЕ. Её собственные шесть куплены, запас — нет, и текст обязан различать
// эти два убытка: «не покупается ничего» здесь было бы ложью про деньги оператора.
func TestTechCardAdvisoriesLegacyRowNeverGetsTheSpare(t *testing.T) {
	card := advCard([]entity.TechCardBomItem{{
		Id: 10, LineKey: "k-buttons", Name: "buttons",
		Section: entity.BomSectionHardware, SpareQty: advDec("2"),
	}, {
		Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
		Kind: advStr(string(entity.BomKindSpareKitBag)),
	}}, entity.TechCardColorwayUsage{BomItemIndex: advI32(0), Quantity: advDec("6")})
	got := TechCardAdvisories(card, nil, nil)
	require.Equal(t, []string{AdviceCountableSpareWithoutQty}, advKeys(got))
	require.Contains(t, got[0].Text, "pays for its own units",
		"строка своё число платит — терять надо ровно запас, а не всё")
}

// TestTechCardAdvisoriesSizedRowStillLosesTheSpare — ПО-РАЗМЕРНАЯ СТРОКА ПОКУПАЕТ СВОИ ЕДИНИЦЫ.
//
// Единицы куплены по-размерной нормой (usagePerGarmentQty выходит на неё раньше резолвера пары), и
// теряется только запас. Общая фраза «не покупается ничего» соврала бы про уже посчитанные деньги.
func TestTechCardAdvisoriesSizedRowStillLosesTheSpare(t *testing.T) {
	card := advCard([]entity.TechCardBomItem{{
		Id: 10, LineKey: "k-buttons", Name: "buttons",
		Section: entity.BomSectionHardware, SpareQty: advDec("2"),
	}, {
		Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
		Kind: advStr(string(entity.BomKindSpareKitBag)),
	}}, entity.TechCardColorwayUsage{
		BomItemId: advI64(10),
		SizeConsumptions: []entity.TechCardBomSizeConsumption{
			{SizeId: 1, Consumption: decimal.NewFromInt(6)},
		},
	})
	got := TechCardAdvisories(card, nil, nil)
	require.Equal(t, []string{AdviceCountableSpareWithoutQty}, advKeys(got))
	require.Contains(t, got[0].Text, "pays for its own units",
		"по-размерная норма действует — теряется только запас")
}

// TestTechCardAdvisoriesCountableSlotText — фраза называет СЛОТ и говорит, чем это кончится.
func TestTechCardAdvisoriesCountableSlotText(t *testing.T) {
	card := advCard([]entity.TechCardBomItem{{
		Id: 10, LineKey: "k-buttons", Name: "buttons",
		Section: entity.BomSectionHardware, QtyPerGarment: advDec("6"),
	}})
	got := TechCardAdvisories(card, nil, nil)
	require.Len(t, got, 1)
	require.Equal(t,
		"buttons: the slot carries a quantity, but no colourway recipe uses it, so it will be neither costed nor purchased",
		got[0].Text)

	card.Colorways[0].Usages = []entity.TechCardColorwayUsage{{
		BomItemId:        advI64(10),
		SizeConsumptions: []entity.TechCardBomSizeConsumption{{SizeId: 1, Consumption: decimal.NewFromInt(6)}},
	}}
	got = TechCardAdvisories(card, nil, nil)
	require.Len(t, got, 1)
	require.Equal(t,
		"buttons: the slot carries a quantity, but its recipe line is graded per size, so the quantity does not apply",
		got[0].Text)
}

// TestTechCardAdvisoriesNoColorways — на карточке без колорвеев проверки 3-5 молчат: рецепта нет
// ни одного, сравнивать не с чем, а «нет ни одного колорвея» уже сказано строкой colorway_linked.
// Проверка пакетика при этом работает — она смотрит только в спецификацию.
func TestTechCardAdvisoriesNoColorways(t *testing.T) {
	card := &entity.TechCard{TechCardInsert: entity.TechCardInsert{BomItems: []entity.TechCardBomItem{{
		Id: 10, LineKey: "k-buttons", Name: "buttons",
		Section: entity.BomSectionHardware, QtyPerGarment: advDec("6"), SpareQty: advDec("2"),
	}}}}
	require.Equal(t, []string{AdviceSpareKitMissing},
		advKeys(TechCardAdvisories(card, []entity.StyleAssembly{advAssemblyLine()},
			map[int][]entity.TechCardOutputVariant{77: {advVariant("BLK", 900, true, false)}})))
}

// TestTechCardAdvisoriesNilCard — читатель зовёт функцию с тем, что вернул стор; nil там законен.
func TestTechCardAdvisoriesNilCard(t *testing.T) {
	require.Nil(t, TechCardAdvisories(nil, []entity.StyleAssembly{advAssemblyLine()}, nil))
}

// TestAdviceKeysAreAllListedInTheProtoContract — третий список, который волна чуть не завела.
//
// Ключей два места: константы Advice* здесь и перечисление в комментарии к TechCardReadinessAdvice,
// по которому ветвится клиент. Шестой ключ уже один раз доехал до сервера, но не до перечисления —
// и на клиенте это выглядело бы не как «контракт разошёлся», а как «замечание не поднимается».
// Тест не заводит ТРЕТИЙ список: он вынимает ключи из исходника констант и требует каждый в proto.
func TestAdviceKeysAreAllListedInTheProtoContract(t *testing.T) {
	src, err := os.ReadFile("techcard_advisories.go")
	if err != nil {
		t.Fatalf("не читается источник констант: %v", err)
	}
	keys := regexp.MustCompile(`(?m)^\tAdvice\w+ = "([a-z_]+)"$`).FindAllStringSubmatch(string(src), -1)
	if len(keys) < 6 {
		t.Fatalf("из источника вынулось %d ключей — регулярка отстала от кода, и тест "+
			"проверяет пустоту", len(keys))
	}

	body, err := os.ReadFile("../../proto/admin/admin/admin.proto")
	if err != nil {
		t.Fatalf("не читается контракт: %v", err)
	}
	proto := string(body)
	from := strings.Index(proto, "message TechCardReadinessAdvice {")
	to := strings.Index(proto[max(from, 0):], "string key = 1;")
	if from < 0 || to < 0 {
		t.Fatalf("блок TechCardReadinessAdvice не найден (%d, %d) — тест ничего не измеряет", from, to)
	}
	block := proto[from : from+to]
	for _, k := range keys {
		if !strings.Contains(block, k[1]) {
			t.Errorf("ключ %q поднимается сервером, но не перечислен в контракте: клиент по нему "+
				"не ветвится, и замечание выглядит непоявившимся", k[1])
		}
	}
}

// TestTechCardAdvisoriesRowWithoutAnyBomReferenceIsNotAUse — строка рецепта, не называющая слот
// НИКАК: ни bom_item_id, ни позиционного bom_item_index.
//
// Такие строки в базе есть — их родил импорт, у которого ссылка не собралась, — и все три чтения
// слота обязаны считать их немыми. Опасен здесь не отказ, а щедрость: прочитай такую строку как
// «слот кем-то используется» — и countable_slot_unused замолчит ровно на карточке, где норма
// действительно никуда не доехала.
func TestTechCardAdvisoriesRowWithoutAnyBomReferenceIsNotAUse(t *testing.T) {
	slot := entity.TechCardBomItem{
		Id: 10, LineKey: "k-buttons", Name: "buttons", Section: entity.BomSectionHardware,
		QtyPerGarment: advDec("6"),
	}
	orphan := entity.TechCardColorwayUsage{Quantity: advDec("6")} // ссылки нет ни одной

	got := advKeys(TechCardAdvisories(advCard([]entity.TechCardBomItem{slot}, orphan), nil, nil))
	require.Equal(t, []string{AdviceCountableSlotUnused}, got,
		"немая строка прочитана как использование слота — замечание молчит там, где норма никуда "+
			"не доехала")
}

// TestTechCardAdvisoriesOrphanRowDoesNotSilenceTheSpare — та же немая строка против шестого
// замечания. Отдельный тест, потому что путь другой: там слот ищется по ссылке, здесь — считается
// корзина пары, и «щедрое» чтение гасит уже не одно замечание, а два.
func TestTechCardAdvisoriesOrphanRowDoesNotSilenceTheSpare(t *testing.T) {
	slot := entity.TechCardBomItem{
		Id: 10, LineKey: "k-buttons", Name: "buttons", Section: entity.BomSectionHardware,
		SpareQty: advDec("2"),
	}
	bag := entity.TechCardBomItem{
		Id: 11, LineKey: "k-bag", Name: "spare kit bag", Section: entity.BomSectionPackaging,
		Kind: advStr(string(entity.BomKindSpareKitBag)),
	}
	// Строка ЕСТЬ и число несёт, но слот не называет: пара остаётся без пришиваемого количества.
	orphan := entity.TechCardColorwayUsage{Quantity: advDec("6")}

	got := advKeys(TechCardAdvisories(advCard([]entity.TechCardBomItem{slot, bag}, orphan), nil, nil))
	require.Equal(t, []string{AdviceCountableSlotUnused}, got,
		"немая строка сошла за использование слота: запас объявлен закупаемым, хотя закупать его "+
			"не с чем")
}
