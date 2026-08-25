package dto

import (
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// РАЗБОР КОЛИЧЕСТВ НА СВЯЗЯХ ШАГА (0334) — ПЯТЬ СПОСОБОВ СОВРАТЬ ЭТИМ СПИСКОМ.
//
// Каждый закрыт ИМЕНОВАННЫМ FieldViolation, а не общей ошибкой: админка маршрутизирует отказ на
// конкретный контрол по пути поля, и форм-левел-баннер «что-то не так с шагом» оператору не
// сообщает ничего. Поэтому тесты проверяют не только «упало», но и ЧТО именно названо.

// bomQtyCard собирает минимальный payload: две строки BOM (счётная + мерная) и один шаг, который
// обе связи держит.
func bomQtyCard(qs ...*pb_common.TechCardOperationBomQty) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber: "BOMQTY-1",
		Name:        "Jacket",
		BomQtyAware: true,
		BomItems: []*pb_common.TechCardBomItem{
			{LineKey: "btn-1", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "button"},
			{LineKey: "thr-1", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD, Name: "thread"},
		},
		Operations: []*pb_common.TechCardOperation{{
			OperationNumber: 10,
			OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Zone:            pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_FRONT,
			BomLineKeys:     []string{"btn-1", "thr-1"},
			BomQuantities:   qs,
		}},
	}
}

func bomQtyWire(key, qty string) *pb_common.TechCardOperationBomQty {
	q := &pb_common.TechCardOperationBomQty{LineKey: key}
	if qty != "" {
		q.QtyPerGarment = dec(qty)
	}
	return q
}

// bomQtyViolation прогоняет payload через конверсию и требует именно FieldViolation с ожидаемым
// путём. Проверяется ПУТЬ, а не текст: текст — представление, а путь — контракт с формой.
func bomQtyViolation(t *testing.T, pb *pb_common.TechCardInsert, wantField string) *entity.ValidationError {
	t.Helper()
	_, err := ConvertPbTechCardInsertToEntity(pb)
	if err == nil {
		t.Fatal("конверсия ПРОШЛА — количество, которое сервер обязан отвергнуть, уехало бы в базу")
	}
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("отказ не field-tagged (%T: %v) — админка не сможет показать его на контроле", err, err)
	}
	if ve.Field != wantField {
		t.Fatalf("отказ назвал поле %q, ожидалось %q — оператор увидит баннер вместо подсветки контрола\n%v",
			ve.Field, wantField, ve)
	}
	return ve
}

// TestBomQtyOnMeasuredSlotIsRefused — ТЕСТ 1: число на связи с МЕРНЫМ слотом.
//
// У нитки (как у ткани, клеевого и тесьмы) норма живёт в рецепте колорвея и по размерам, поэтому
// счётчик на шаге был бы ТРЕТЬИМ ответом на один вопрос. Граница держится единственным предикатом
// проекта — entity.IsCountableSection.
func TestBomQtyOnMeasuredSlotIsRefused(t *testing.T) {
	ve := bomQtyViolation(t, bomQtyCard(bomQtyWire("thr-1", "1.5")),
		"operations[0].bom_quantities[0].qty_per_garment")
	if ve.Reason != "measured_section" {
		t.Errorf("причина отказа %q, ожидалось measured_section — админка ветвится по причине, "+
			"а не по тексту, и на чужой причине покажет не тот контрол", ve.Reason)
	}
	if !strings.Contains(ve.Conflicting, "thread") {
		t.Errorf("отказ не назвал секцию, которая сработала (%q) — оператор не поймёт, что чинить",
			ve.Conflicting)
	}
	if ve.HowToFix == "" {
		t.Error("отказ не сказал, как выйти из положения")
	}
}

// TestBomQtyOnUnlinkedKeyIsRefused — ТЕСТ 2: ключ вне bom_line_keys ЭТОГО шага.
//
// Членство в связи определяет ТОЛЬКО bom_line_keys, единственный владелец. Ссылка на связь,
// которой у шага нет, — не молчаливый пропуск: молча выброшенное число оператор обнаружит лишь
// тем, что оно не сохранилось.
func TestBomQtyOnUnlinkedKeyIsRefused(t *testing.T) {
	card := bomQtyCard(bomQtyWire("btn-1", "6"))
	// Материал СУЩЕСТВУЕТ в BOM карточки — со связью его развязывает только этот шаг.
	card.Operations[0].BomLineKeys = []string{"thr-1"}
	bomQtyViolation(t, card, "operations[0].bom_quantities[0].line_key")
}

// TestBomQtyDuplicateKeyIsRefused — ТЕСТ 3: дубль ключа внутри одного шага.
//
// В отличие от bom_line_keys, где повтор схлопывается молча (связь — МНОЖЕСТВО, второго факта
// повтор не несёт), здесь повтор несёт ВТОРОЕ ЧИСЛО о той же паре. Схлопывание выбрало бы одно из
// двух за человека, а какое — не знает никто.
func TestBomQtyDuplicateKeyIsRefused(t *testing.T) {
	ve := bomQtyViolation(t,
		bomQtyCard(bomQtyWire("btn-1", "6"), bomQtyWire("btn-1", "2")),
		"operations[0].bom_quantities[1].line_key")
	if ve.Reason != "duplicate" {
		t.Errorf("причина отказа %q, ожидалось duplicate", ve.Reason)
	}
}

// TestBomQtyWithoutNumberIsRefused — ТЕСТ 4а: запись без числа.
//
// Пустой google.type.Decimal — не «ноль» и не «не сказано»: связь БЕЗ числа в список не попадает
// вовсе, поэтому запись-пустышка не выражает ничего и завела бы ВТОРОЕ написание отсутствия.
func TestBomQtyWithoutNumberIsRefused(t *testing.T) {
	bomQtyViolation(t, bomQtyCard(bomQtyWire("btn-1", "")),
		"operations[0].bom_quantities[0].qty_per_garment")
}

// TestBomQtyNegativeIsRefused — ТЕСТ 4б: отрицательное число.
//
// Схема закрывает знак CHECK'ом, но сырой MySQL 3819 назвал бы оператору колонку, которой тот не
// касался. Отказ обязан прийти отсюда, с путём до записи.
func TestBomQtyNegativeIsRefused(t *testing.T) {
	bomQtyViolation(t, bomQtyCard(bomQtyWire("btn-1", "-1")),
		"operations[0].bom_quantities[0].qty_per_garment")
}

// TestBomQtyZeroIsARealStatement — НОЛЬ ПРОХОДИТ, и это не поблажка.
//
// «Этот шаг артикула не тратит» — реальное утверждение, отличное от «не сказано» (у которого нет
// записи вовсе). Отвергни ноль — и утверждение стало бы невыразимым, а его место занял бы пробел,
// который следующий читатель принял бы за «неизвестно».
func TestBomQtyZeroIsARealStatement(t *testing.T) {
	tc, err := ConvertPbTechCardInsertToEntity(bomQtyCard(bomQtyWire("btn-1", "0")))
	if err != nil {
		t.Fatalf("ноль отвергнут: %v — «шаг этого артикула не тратит» стало невыразимым", err)
	}
	qs := tc.Operations[0].BomQuantities
	if len(qs) != 1 || !qs[0].QtyPerGarment.IsZero() {
		t.Fatalf("ноль не доехал до сущности: %#v", qs)
	}
}

// TestBomQtyRoundTripsThroughTheConverter — ТЕСТ 10: симметрия запись↔чтение на уровне конвертера.
//
// Поле, забытое на ВЫХОДЕ, читается клиентом как «количеств нет», и первое же осведомлённое
// сохранение сотрёт их честно и навсегда — молча, потому что клиент сам же и прислал пустоту.
func TestBomQtyRoundTripsThroughTheConverter(t *testing.T) {
	tc, err := ConvertPbTechCardInsertToEntity(bomQtyCard(bomQtyWire("btn-1", "6")))
	if err != nil {
		t.Fatalf("конверсия не прошла: %v", err)
	}
	if !tc.BomQtyAware {
		t.Error("транспортный флаг не доехал до сущности")
	}
	qs := tc.Operations[0].BomQuantities
	if len(qs) != 1 || qs[0].LineKey != "btn-1" || qs[0].QtyPerGarment.String() != "6" {
		t.Fatalf("количество не доехало с провода в сущность: %#v", qs)
	}

	back := techCardOperationsToPb(tc.Operations)
	if len(back) != 1 {
		t.Fatalf("ожидался один шаг на выходе, получено %d", len(back))
	}
	out := back[0].GetBomQuantities()
	if len(out) != 1 {
		t.Fatalf("количества НЕ ВЕРНУЛИСЬ на провод (%d записей) — клиент прочтёт «количеств нет» "+
			"и сотрёт их следующим же сохранением", len(out))
	}
	if out[0].GetLineKey() != "btn-1" || out[0].GetQtyPerGarment().GetValue() != "6" {
		t.Fatalf("количество вернулось искажённым: %#v", out[0])
	}
	// Связь БЕЗ числа на провод записью не выезжает: разрежённость — часть контракта, а не
	// оптимизация. Пустая запись означала бы «ноль» у любого читателя, который забыл различить.
	if len(back[0].GetBomLineKeys()) != 2 {
		t.Fatalf("список связей поехал: %#v", back[0].GetBomLineKeys())
	}
}

// TestBomQtyStaysWithItsStepWhenStepsAreReordered — ТЕСТ 8: перестановка шагов не перепутывает
// количества.
//
// Это ровно тот дефект, ради которого щит выбрал ОТКАЗ вместо восстановления хранимого по паре
// (display_order, bom_item_id): перестановка шагов в том же сохранении — рядовая правка, и
// позиционная связь перепутала бы числа между шагами ТИХО, оставив их валидными, но не на своих
// местах. Здесь проверяется, что ключи ездят ВНУТРИ шага.
func TestBomQtyStaysWithItsStepWhenStepsAreReordered(t *testing.T) {
	step := func(key, qty string) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Zone:          pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_FRONT,
			BomLineKeys:   []string{key},
			BomQuantities: []*pb_common.TechCardOperationBomQty{bomQtyWire(key, qty)},
		}
	}
	build := func(ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
		card := bomQtyCard()
		card.BomItems = append(card.BomItems, &pb_common.TechCardBomItem{
			LineKey: "btn-2", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, Name: "snap",
		})
		card.Operations = ops
		return card
	}
	got := func(pb *pb_common.TechCardInsert) map[string]string {
		t.Helper()
		tc, err := ConvertPbTechCardInsertToEntity(pb)
		if err != nil {
			t.Fatalf("конверсия не прошла: %v", err)
		}
		out := map[string]string{}
		for i := range tc.Operations {
			for _, q := range tc.Operations[i].BomQuantities {
				out[q.LineKey] = q.QtyPerGarment.String()
			}
		}
		return out
	}
	straight := got(build(step("btn-1", "6"), step("btn-2", "2")))
	swapped := got(build(step("btn-2", "2"), step("btn-1", "6")))
	for _, want := range []struct{ key, qty string }{{"btn-1", "6"}, {"btn-2", "2"}} {
		if straight[want.key] != want.qty || swapped[want.key] != want.qty {
			t.Fatalf("перестановка шагов перепутала количества: %q → прямой %q, переставленный %q "+
				"(ожидалось %q)", want.key, straight[want.key], swapped[want.key], want.qty)
		}
	}
}

// TestBomQtyAwareIsNotHashed — ТРАНСПОРТНЫЙ ФЛАГ О ОТПРАВИТЕЛЕ, А НЕ О КАРТОЧКЕ.
//
// bom_qty_aware говорит, знает ли бандл про количества; содержание карточки от него не зависит.
// Захешируй его — и подпись CONSTRUCTION протухала бы у КАЖДОЙ карточки в день выкатки клиента,
// причём с формулировкой «секция отредактирована после подписания», которую никто не связал бы с
// причиной. Тот же довод, по которому в проекции нет machine_fields_aware, assembly_aware,
// media_aware и operation_kinds_aware.
func TestBomQtyAwareIsNotHashed(t *testing.T) {
	build := func(aware bool) *entity.TechCardInsert {
		pb := bomQtyCard(bomQtyWire("btn-1", "6"))
		pb.BomQtyAware = aware
		tc, err := ConvertPbTechCardInsertToEntity(pb)
		if err != nil {
			t.Fatalf("конверсия payload'а (aware=%v) не должна падать: %v", aware, err)
		}
		return tc
	}
	// Разбор идёт ВСЕГДА, независимо от флага: без этого клон сезона (payload строит сервер,
	// флагов не эмитит) вернулся бы без количеств.
	if qs := build(false).Operations[0].BomQuantities; len(qs) != 1 {
		t.Fatalf("при aware=false количества выброшены разбором (%d записей) — клон сезона вернул бы "+
			"карточку без них и без единой ошибки", len(qs))
	}
	awareDigest := constructionDigest(build(true))
	unawareDigest := constructionDigest(build(false))
	if awareDigest != unawareDigest {
		t.Fatalf("транспортный флаг попал в отпечаток CONSTRUCTION: aware=%s, unaware=%s",
			awareDigest, unawareDigest)
	}
	if awareDigest == "" {
		t.Fatal("отпечаток CONSTRUCTION пуст — тест не проверил бы ничего")
	}
}

// TestBomQtyWriteAndReadNameTheSameColumn — ШЕСТОЙ СПИСОК, ОШИБАЮЩИЙСЯ МОЛЧА.
//
// Колонка qty_per_garment живёт в трёх местах пути хранения: ALTER миграции, ключи вставки связей
// (insertTechCardOperationBoms) и СПИСОК КОЛОНОК SELECT'а, читающего связи. Первые два ошибаются
// громко; SELECT — молча: запись проходит целиком, а чтение возвращает NULL, шаг открывается без
// числа, отпечаток ЧТЕНИЯ расходится с отпечатком ЗАПИСИ, и подпись, поставленная после такого
// чтения, рождается протухшей навсегда и без единой ошибки в логе.
//
// ПОЧЕМУ ПО ИСХОДНИКУ, А НЕ ПО БАЗЕ: запрос лежит в другом пакете, а тесты internal/store бить
// нельзя вовсе (они идут в живую базу и дропают таблицы). Текст запроса — то же самое утверждение,
// что и запрос: колонки, которой нет в тексте, не появится в результате ни при какой базе.
func TestBomQtyWriteAndReadNameTheSameColumn(t *testing.T) {
	src := productionSource(t)

	insert := bomQtyFuncBody(t, src, "func insertTechCardOperationBoms")
	if !strings.Contains(insert, `"qty_per_garment"`) {
		t.Error("insertTechCardOperationBoms не кладёт ключ \"qty_per_garment\" в строку связи — " +
			"количество никуда не пишется")
	}
	// Ключ обязан лежать в КАЖДОЙ строке, а не только там, где число есть: storeutil.BulkInsert
	// строит список колонок по набору ключей ПЕРВОЙ строки, и строка с лишним ключом уехала бы не в
	// свою колонку либо уронила бы вставку целиком. Форма «var qty any + безусловный ключ» и есть
	// это утверждение; условная вставка ключа его нарушает.
	if strings.Contains(insert, `if ok {`+"\n") && !strings.Contains(insert, "var qty any") {
		t.Error("ключ qty_per_garment кладётся условно — BulkInsert возьмёт колонки по первой строке")
	}

	// ДВЕ ПОЛОВИНЫ ЧТЕНИЯ ПРОВЕРЯЮТСЯ ПОРОЗНЬ, И ЭТО НЕ ПЕДАНТИЗМ — ЭТО НАЙДЕНО МУТАЦИЕЙ. Первая
	// версия теста резала весь блок QueryListNamed целиком, вместе с объявлением строки результата;
	// убранная из SQL колонка оставляла в куске тег `db:"qty_per_garment"`, и проверка ЗЕЛЕНЕЛА при
	// сломанном чтении. Текст запроса и объявление строки — два разных утверждения, и совпадать они
	// обязаны оба.
	sql, decl := bomQtyLinkSelect(t, src)
	if !strings.Contains(sql, "qty_per_garment") {
		t.Errorf("SELECT связей шага не тянет qty_per_garment — количество читается как NULL, "+
			"и подпись CONSTRUCTION протухает после первого же чтения:\n%s", sql)
	}
	if !strings.Contains(decl, `db:"qty_per_garment"`) {
		t.Errorf("у строки результата нет поля с тегом db:\"qty_per_garment\" — колонка в запросе "+
			"есть, а разложить её некуда:\n%s", decl)
	}
}

// bomQtyFuncBody вырезает тело функции по её заголовку. Отсутствие функции — ПАДЕНИЕ, а не пустая
// строка: пустой разбор молча зеленит любую проверку, построенную на нём.
func bomQtyFuncBody(t *testing.T, src, header string) string {
	t.Helper()
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("в %s не найдена %q — её переименовали, и проверка ниже проверяла бы пустоту",
			productionStoreSource, header)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("не найден конец %q — разбор сломан", header)
	}
	return src[start : start+end]
}

// bomQtyLinkSelect вырезает чтение связей шага ДВУМЯ КУСКАМИ: сырой текст SQL (между бэктиками) и
// объявление строки результата (то, что стоит до него). Одним куском их резать нельзя — см. мутацию
// в теле теста выше.
func bomQtyLinkSelect(t *testing.T, src string) (sqlText, decl string) {
	t.Helper()
	start := strings.Index(src, "bomLinkRows, err := storeutil.QueryListNamed")
	if start < 0 {
		t.Fatalf("в %s не найдено чтение связей шага (bomLinkRows) — разбор сломан",
			productionStoreSource)
	}
	rest := src[start:]
	// Якорь — бэктик ПОСЛЕ аргументов QueryListNamed: бэктики есть и в тегах структуры результата,
	// и первый попавшийся увёл бы извлекатель в объявление строки.
	const queryOpen = "](ctx, s.DB, `"
	open := strings.Index(rest, queryOpen)
	if open < 0 {
		t.Fatalf("у запроса связей шага не найден открывающий бэктик — разбор сломан")
	}
	open += len(queryOpen)
	close := strings.Index(rest[open:], "`")
	if close < 0 {
		t.Fatalf("у запроса связей шага не найден закрывающий бэктик — разбор сломан")
	}
	sqlText = rest[open : open+close]
	decl = rest[:open]
	if !strings.Contains(sqlText, "FROM tech_card_operation_bom") {
		t.Fatalf("вырезанный SQL не содержит FROM tech_card_operation_bom — извлекатель смотрит "+
			"не туда, а сломанный извлекатель зеленит тест на любой ошибке:\n%s", sqlText)
	}
	if !strings.Contains(decl, "db:\"line_key\"") {
		t.Fatalf("вырезанное объявление строки не содержит даже db:\"line_key\" — извлекатель "+
			"смотрит не туда:\n%s", decl)
	}
	return sqlText, decl
}
