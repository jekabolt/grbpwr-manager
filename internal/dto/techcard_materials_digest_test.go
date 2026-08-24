package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ПРОЕКЦИЯ MATERIALS — ГОЛОВА ПЛЮС ТЕГИРОВАННЫЕ ПАРНЫЕ ХВОСТЫ (задача 02 волны счётных норм).
//
// До этой правки materialsProjection была плоским позиционным массивом из семнадцати элементов без
// единого различителя, то есть структурой, в которую нельзя дописать поле: восемнадцатый элемент,
// поставленный безусловно, сдвинул бы отпечаток КАЖДОЙ строки BOM в базе и объявил бы все
// утверждённые подписи MATERIALS устаревшими в момент выкатки. Задача 03 заводит слоту два поля
// сразу (qty_per_garment, spare_qty), поэтому механизм хвоста обязан появиться РАНЬШЕ самих полей —
// иначе первое же поле придётся вносить ценой волны пере-утверждения на всю базу.
//
// Здесь заморожены обе половины: ГОЛОВА (семнадцать позиций, байт в байт как до правки) и ФОРМА
// того, что дописывается правее неё.

// materialsGoldCard — карточка из двух строк BOM: мерная (все семнадцать позиций головы заполнены,
// на ней идут поимённые мутации) и счётная (на ней «завтра» появится хвост количества).
func materialsGoldCard() *entity.TechCardInsert {
	return &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{
		{
			LineKey:         "01J0MATFABRIC00000000000001",
			MaterialId:      ni(4217),
			Section:         entity.BomSectionFabric,
			Name:            "шерсть меланж 340",
			Supplier:        ns("Lanificio"),
			SupplierRef:     ns("LF-340-MEL"),
			Color:           ns("антрацит"),
			Composition:     ns("80% WO / 20% PA"),
			Spec:            ns("саржа 2/2, валяная"),
			Unit:            ns("m"),
			UnitPrice:       nd("28.40"),
			Currency:        ns("EUR"),
			Comment:         ns("усадка после декатировки 2%"),
			FabricWidth:     nd("150"),
			FabricWeightGsm: nd("340"),
			FabricDirection: ns("nap_one_way"),
			WastagePercent:  nd("8.5"),
		},
		{
			LineKey:     "01J0MATBUTTON00000000000002",
			MaterialId:  ni(9004),
			Section:     entity.BomSectionHardware,
			Name:        "пуговица рог 24L",
			Supplier:    ns("Cornetti"),
			SupplierRef: ns("CR-24L-4H"),
			Color:       ns("тёмный рог"),
			Unit:        ns("pcs"),
			UnitPrice:   nd("0.72"),
			Currency:    ns("EUR"),
		},
	}}
}

func materialsDigest(tc *entity.TechCardInsert) string {
	return TechCardSectionDigests(tc)[entity.SignoffMaterials]
}

// ЗАМОРОЖЕННЫЙ HEX MATERIALS — эталона этой секции не было вовсе, и его отсутствие было дырой того
// же рода, что закрывают opGoldConstructionDigestHex у CONSTRUCTION и packagingGoldDigestHex у
// PACKAGING: правка слоя сборки (кодировка, разделители, функция хеша, порядок склейки) оставила бы
// проекцию байт в байт прежней, а подписи всех карточек протухли бы молча.
//
// Значение снято НА КОММИТЕ ДО введения механизма хвоста и обязано совпадать с сегодняшним —
// сегодня ни одно семейство не рождается, поэтому дописывать правее головы нечего. Что это именно
// так, проверяется не доверием к литералу, а реконструкцией старой формы прямо в тесте ниже.
const materialsGoldDigestHex = "6ca6fd34d03fdbaeeb498f4399010df35a2cdd9fb5a9ba5c3cdba8a6e153b313"

// TestMaterialsDigestUnchangedByTheTailMechanism — ГЛАВНОЕ ДОКАЗАТЕЛЬСТВО задачи 02: заведение
// механизма хвоста не двинуло отпечаток ни одной строки BOM.
//
// Старая форма выписана здесь ДОСЛОВНО, а не скопирована хешем: сравнение идёт со структурой,
// которую проекция имела до правки, поэтому тест ловит и подмену значения в голове, и появление
// восемнадцатого элемента, и смену порядка позиций — а не только сдвиг литерала.
func TestMaterialsDigestUnchangedByTheTailMechanism(t *testing.T) {
	tc := materialsGoldCard()
	before := make([]any, 0, len(tc.BomItems))
	for _, b := range tc.BomItems {
		before = append(before, []any{
			b.LineKey, string(b.Section), b.Name, b.Supplier.String, b.SupplierRef.String,
			b.Color.String, b.Composition.String, b.Spec.String, b.Unit.String,
			digestDecimal(b.UnitPrice), b.Currency.String, b.Comment.String,
			digestDecimal(b.FabricWidth), digestDecimal(b.FabricWeightGsm), b.FabricDirection.String,
			digestDecimal(b.WastagePercent), b.MaterialId.Int64,
		})
	}
	if got, want := materialsDigest(tc), digestOf(before); got != want {
		t.Fatalf("отпечаток MATERIALS уехал от заведения механизма хвоста — значит правее головы "+
			"что-то дописалось безусловно, и все подписи MATERIALS протухли в момент выкатки."+
			"\n--- старая форма ---\n%s\n--- сейчас ---\n%s", want, got)
	}
}

// TestMaterialsDigestHexFrozen — тот же эталон литералом, и он ловит то, чего не видит тест выше:
// правку слоя сборки. digestOf кодирует проекцию json.Marshal и берёт sha256; смена кодировки,
// разделителей, функции хеша или порядка склейки оставит ОБЕ формы одинаковыми и тест выше
// зелёным, а hex поедет у всех карточек разом.
func TestMaterialsDigestHexFrozen(t *testing.T) {
	if got := materialsDigest(materialsGoldCard()); got != materialsGoldDigestHex {
		t.Errorf("отпечаток MATERIALS поехал при неизменной проекции — поехал слой сборки, и "+
			"подписи ВСЕХ карточек протухли разом.\n--- эталон ---\n%s\n--- сейчас ---\n%s",
			materialsGoldDigestHex, got)
	}
}

// TestMaterialsHeadIsSeventeenPositions фиксирует САМО ПРАВИЛО, а не значения: голова ровно
// семнадцать позиций, всё правее неё — тегированный хвост «[имя, пары]». Этот тест обязан упасть,
// когда задача 03 (или кто угодно после неё) допишет поле БЕЗУСЛОВНО.
func TestMaterialsHeadIsSeventeenPositions(t *testing.T) {
	const head = 17
	rows, ok := materialsProjection(materialsGoldCard()).([]any)
	if !ok {
		t.Fatalf("проекция MATERIALS перестала быть []any")
	}
	if len(rows) != 2 {
		t.Fatalf("проекция отдала %d строк на карточке из двух — строка потерялась или задвоилась", len(rows))
	}
	for r, raw := range rows {
		row, ok := raw.([]any)
		if !ok {
			t.Fatalf("строка %d проекции MATERIALS перестала быть []any: %#v", r, raw)
		}
		if len(row) < head {
			t.Fatalf("голова строки %d короче %d позиций (%d) — из неё удалили элемент, а это тот "+
				"же безусловный сдвиг, что и дописка", r, head, len(row))
		}
		for i, tail := range row[head:] {
			tagged, ok := tail.([]any)
			if !ok || len(tagged) == 0 {
				t.Errorf("строка %d, хвост %d не тегированный кортеж: %#v", r, i, tail)
				continue
			}
			if tag, ok := tagged[0].(string); !ok || tag == "" {
				t.Errorf("строка %d, хвост %d начинается не с имени-тега: %#v — голый хвост запрещён",
					r, i, tagged[0])
			}
			if !opKindIsPairTail(tagged) {
				t.Errorf("строка %d, хвост %d не парной формы: %#v — дописать в него поле нельзя, "+
					"не сдвинув отпечаток каждой строки, которая его эмитит", r, i, tagged)
			}
		}
	}
}

// TestMaterialsTailsAreEmptyToday — ОБЕЩАНИЕ НУЛЕВОЙ ВОЛНЫ, отдельной клеткой: сегодня ни одно
// семейство не рождается, значит байты сегодняшних строк BOM не двигаются вовсе.
func TestMaterialsTailsAreEmptyToday(t *testing.T) {
	for i := range materialsGoldCard().BomItems {
		b := &materialsGoldCard().BomItems[i]
		if tails := materialsTails(b); len(tails) != 0 {
			t.Errorf("materialsTails на полностью заполненной строке %d вернул %d хвостов, "+
				"ожидалось 0 — поле уехало из головы в хвост, а это сдвиг отпечатка: %#v",
				i, len(tails), tails)
		}
	}
}

// materialsTomorrow — «завтрашняя» карточка задачи 03: та же самая, но у СЧЁТНОЙ строки (индекс 1)
// правее головы стоит хвост количества. Поля qty_per_garment / spare_qty на сущности ещё нет, так
// что хвост подаётся сюда искусственно — проверяется МЕХАНИЗМ, а не поля, которых нет.
func materialsTomorrow(t *testing.T, tail []any) string {
	t.Helper()
	rows, ok := materialsProjection(materialsGoldCard()).([]any)
	if !ok {
		t.Fatalf("проекция MATERIALS перестала быть []any")
	}
	next := append([]any(nil), rows...)
	if tail != nil {
		row, ok := next[1].([]any)
		if !ok {
			t.Fatalf("строка BOM перестала быть []any")
		}
		next[1] = append(append([]any(nil), row...), tail)
	}
	return digestOf(next)
}

// TestMaterialsEmptyCountDoesNotMoveTheDigest — СВОЙСТВО, РАДИ КОТОРОГО МЕХАНИЗМ И ЗАВОДИТСЯ,
// проверенное ПРЯМО на hex, а не рассуждением.
//
// Пока оба поля задачи 03 пусты, отпечаток обязан совпадать побайтно с сегодняшним; как только одно
// из них заполнено — обязан разойтись, иначе хвост врал бы о содержании и подпись под карточкой
// «по две пуговицы» читалась бы как действительная под карточкой «по шесть».
func TestMaterialsEmptyCountDoesNotMoveTheDigest(t *testing.T) {
	today := materialsDigest(materialsGoldCard())

	empty := operationKindTail("count",
		opKindDec("qty_per_garment", decimal.NullDecimal{}),
		opKindDec("spare_qty", decimal.NullDecimal{}),
	)
	if empty != nil {
		t.Fatalf("хвост из одних незаполненных полей всё-таки родился: %#v", empty)
	}
	if got := materialsTomorrow(t, empty); got != today {
		t.Errorf("дописка ПУСТЫХ полей количества сдвинула отпечаток — механизм хвоста не защищает "+
			"подписи, и волна задачи 03 протухнет всю базу.\nсегодня: %s\nзавтра:  %s", today, got)
	}

	filled := operationKindTail("count", opKindDec("qty_per_garment", nd("6")))
	if filled == nil {
		t.Fatalf("хвост с заполненным количеством не родился")
	}
	if got := materialsTomorrow(t, filled); got == today {
		t.Errorf("заполненное количество на изделие не сдвинуло отпечаток — подпись под шестью " +
			"пуговицами читалась бы как действительная под любым другим числом")
	}
}

// TestMaterialsCountTailIsOrderedAndDistinct — ПОРЯДОК ПАР ЕСТЬ ЧАСТЬ ОТПЕЧАТКА, поэтому он обязан
// определяться ключом, а не порядком объявления в коде: иначе первая же дописка поля в середину
// списка увела бы отпечаток каждой строки, которая хвост эмитит, без единого изменения данных.
func TestMaterialsCountTailIsOrderedAndDistinct(t *testing.T) {
	asDeclared := operationKindTail("count",
		opKindDec("qty_per_garment", nd("6")),
		opKindDec("spare_qty", nd("2")),
	)
	reversed := operationKindTail("count",
		opKindDec("spare_qty", nd("2")),
		opKindDec("qty_per_garment", nd("6")),
	)
	if digestOf(asDeclared) != digestOf(reversed) {
		t.Fatalf("порядок объявления пар протёк в отпечаток — дописка поля в середину списка "+
			"протухнет все подписи.\n--- как объявлено ---\n%#v\n--- наоборот ---\n%#v",
			asDeclared, reversed)
	}
	pairs, ok := asDeclared[1].([]any)
	if !ok || len(pairs) != 2 {
		t.Fatalf("хвост количества перестал быть парой пар: %#v", asDeclared)
	}
	first, _ := pairs[0].([]any)
	second, _ := pairs[1].([]any)
	if len(first) != 2 || len(second) != 2 || first[0] != "qty_per_garment" || second[0] != "spare_qty" {
		t.Fatalf("порядок пар не qty_per_garment → spare_qty: %#v", pairs)
	}

	// Обе половины хвоста обязаны говорить по отдельности: карточка «шесть пришитых» и карточка
	// «шесть пришитых плюс две в пакетик» — разные закупки, а значит и разные подписи.
	onlyQty := materialsTomorrow(t, operationKindTail("count", opKindDec("qty_per_garment", nd("6"))))
	both := materialsTomorrow(t, asDeclared)
	if onlyQty == both {
		t.Errorf("запас не сдвинул отпечаток — подпись под закупкой без запаски читалась бы как " +
			"действительная под закупкой с запаской")
	}
}

// TestMaterialsZeroCountIsAStatementNotSilence — НОЛЬ И NULL ОБЯЗАНЫ РАЗЛИЧАТЬСЯ, и это не
// придирка: «пуговиц на изделии ноль» — утверждение технолога, «не сказано» — незакрытый вопрос.
// Схлопнись они в один отпечаток, подпись под неотвеченным вопросом читалась бы как подпись под
// ответом. Правило рождения пары стоит на Valid, а digestDecimal пишет NULL как "" и ноль как "0",
// поэтому различие держится by construction — здесь оно и проверяется.
func TestMaterialsZeroCountIsAStatementNotSilence(t *testing.T) {
	silence := materialsTomorrow(t, operationKindTail("count",
		opKindDec("qty_per_garment", decimal.NullDecimal{}),
	))
	zero := materialsTomorrow(t, operationKindTail("count",
		opKindDec("qty_per_garment", nd("0")),
	))
	six := materialsTomorrow(t, operationKindTail("count",
		opKindDec("qty_per_garment", nd("6")),
	))
	if silence == zero {
		t.Errorf("«количество не задано» и «количество ноль» дали ОДИН отпечаток MATERIALS — " +
			"подпись под неотвеченным вопросом читается как подпись под ответом «ноль»")
	}
	if zero == six {
		t.Errorf("ноль и шесть дали один отпечаток MATERIALS — значение количества не в подписи")
	}
}

// TestEveryMaterialsFieldMovesTheDigest — семнадцать позиций головы поимённо. Поле, попавшее в чужую
// позицию (или не попавшее никуда), проявится здесь совпадением двух отпечатков, а в цехе — подписью
// под закупкой, которой никто не согласовывал.
func TestEveryMaterialsFieldMovesTheDigest(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*entity.TechCardBomItem)
	}{
		{"line_key", func(b *entity.TechCardBomItem) { b.LineKey = "01J0MATFABRIC00000000000009" }},
		{"section", func(b *entity.TechCardBomItem) { b.Section = entity.BomSectionLining }},
		{"name", func(b *entity.TechCardBomItem) { b.Name = "шерсть меланж 380" }},
		{"supplier", func(b *entity.TechCardBomItem) { b.Supplier = ns("Marzotto") }},
		{"supplier_ref", func(b *entity.TechCardBomItem) { b.SupplierRef = ns("MZ-380-MEL") }},
		{"color", func(b *entity.TechCardBomItem) { b.Color = ns("графит") }},
		{"composition", func(b *entity.TechCardBomItem) { b.Composition = ns("100% WO") }},
		{"spec", func(b *entity.TechCardBomItem) { b.Spec = ns("саржа 3/1") }},
		{"unit", func(b *entity.TechCardBomItem) { b.Unit = ns("kg") }},
		{"unit_price", func(b *entity.TechCardBomItem) { b.UnitPrice = nd("28.41") }},
		{"currency", func(b *entity.TechCardBomItem) { b.Currency = ns("GBP") }},
		{"comment", func(b *entity.TechCardBomItem) { b.Comment = ns("декатировать до раскроя") }},
		{"fabric_width", func(b *entity.TechCardBomItem) { b.FabricWidth = nd("140") }},
		{"fabric_weight_gsm", func(b *entity.TechCardBomItem) { b.FabricWeightGsm = nd("380") }},
		{"fabric_direction", func(b *entity.TechCardBomItem) { b.FabricDirection = ns("two_way") }},
		{"wastage_percent", func(b *entity.TechCardBomItem) { b.WastagePercent = nd("9") }},
		{"material_id", func(b *entity.TechCardBomItem) { b.MaterialId = ni(4218) }},
	}
	seen := map[string]string{materialsDigest(materialsGoldCard()): "карточка без правок"}
	for _, m := range mutations {
		tc := materialsGoldCard()
		m.mutate(&tc.BomItems[0])
		d := materialsDigest(tc)
		if prev, dup := seen[d]; dup {
			t.Fatalf("%q и %q дают ОДИН отпечаток MATERIALS: поле не хешируется или заняло чужую позицию",
				prev, m.name)
		}
		seen[d] = m.name
	}
}
