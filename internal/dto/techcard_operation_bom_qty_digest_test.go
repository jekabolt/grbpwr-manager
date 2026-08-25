package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ХВОСТ "bom_qty" (0334) — КОЛИЧЕСТВА НА СВЯЗЯХ ШАГА В ПОДПИСИ CONSTRUCTION.
//
// ТРИ ЭТАЛОНА ОТВЕЧАЮТ НА ТРИ РАЗНЫХ ВОПРОСА, и ни один не заменяет другой:
//
//  1. шаг СО СВЯЗЯМИ, но БЕЗ количеств — байты не двинулись (волна пере-подписаний нулевая);
//  2. тот же шаг с ОДНИМ количеством — отпечаток другой (число доехало до подписи);
//  3. ДВА количества, присланные в разном порядке, — отпечаток ОДИН (детерминированность хвоста).
//
// ЛИТЕРАЛЫ ПОСЧИТАНЫ НЕЗАВИСИМО ОТ КОДА, А НЕ СПИСАНЫ С ЕГО ВЫВОДА — та же дисциплина, что у
// opGoldWorkDigestHex: кортежи выписаны руками по правилам constructionProjection и сверены
// кортежным тестом ниже, а байты — sha256 от компактного JSON, посчитанный сторонним скриптом.
// Списанный с падения эталон морозит не форму, а ошибку.
//
// ПОЧЕМУ СВОИ ЛИТЕРАЛЫ, А НЕ ДОПИСКА ШАГА В opGoldConstructionDigestHex. Тот hex морозит
// СУЩЕСТВУЮЩИЕ строки, и его неподвижность и есть цитата «волна нулевая»; допиши в ту карточку
// шаг с количеством — литерал пришлось бы переписать, а переписанный эталон нулевой волны не
// доказывает ничего.
//
// КОГДА ПРАВКА ЭТИХ ЛИТЕРАЛОВ ЗАКОННА: ровно тогда же, когда правка соседних, — после подсчёта
// волны пере-утверждения по tech_card_signoff. «Позеленить тест» законным поводом не является.
const (
	// Шаг с двумя связями и БЕЗ единого количества.
	bomQtyGoldNoQtyHex = "e5a22b2780277c69c622a9e9a45abe5bed25009d68dbad4895fe77a5f2a371e5"
	// Тот же шаг, одно количество на первой связи.
	bomQtyGoldOneHex = "a12f7c2a0e3fb33c20598f4273f8e8c23f282901d6dec1491c2b52f329bee229"
	// Тот же шаг, количества на обеих связях.
	bomQtyGoldTwoHex = "3ea6eb3bb0f7d6d5bacef70131f9d37b12e5e1a82b03692420f7e593e638cf12"
)

// bomQtyGoldStep — шаг-основа всех трёх эталонов: MACHINE + lockstitch (компат-позиция несёт
// legacy-токен, машинного хвоста не рождается) и две связи с материалами.
func bomQtyGoldStep(qs ...entity.OperationBomQty) entity.TechCardOperation {
	return entity.TechCardOperation{
		OperationNumber: opGoldI32(30),
		OperationType:   entity.OpTypeMachine,
		MachineType:     opGoldStr("lockstitch"),
		Zone:            entity.ZoneOuter,
		BomLineKeys:     []string{"btn-1", "zip-1"},
		BomQuantities:   qs,
	}
}

func bomQtyGoldDigest(op entity.TechCardOperation) string {
	return digestOf(constructionProjection(&entity.TechCardInsert{
		Operations: []entity.TechCardOperation{op},
	}))
}

func bomQty(key, v string) entity.OperationBomQty {
	return entity.OperationBomQty{LineKey: key, QtyPerGarment: decimal.RequireFromString(v)}
}

// TestBomQtyTailAbsentWhenNoQuantities — ПЕРВЫЙ вопрос: нулевая волна.
//
// Шаг С привязанными материалами и БЕЗ количеств — сегодняшнее состояние всех до единой строк
// (3 на проде, 14 на бете). Его отпечаток обязан быть тем же, каким был до 0334: хвост не
// рождается вовсе, и ни одна подписанная CONSTRUCTION не протухает в момент выкатки.
//
// Позиция 5 кортежа (голый digestList(o.BomLineKeys)) заморожена именно этим: преврати её в пары
// «ключ, число» — и этот hex поедет, объявив устаревшей подпись КАЖДОГО шага с материалом.
func TestBomQtyTailAbsentWhenNoQuantities(t *testing.T) {
	got := bomQtyGoldDigest(bomQtyGoldStep())
	if got != bomQtyGoldNoQtyHex {
		t.Errorf("отпечаток шага СО СВЯЗЯМИ, но БЕЗ количеств поехал — значит волна 0334 не нулевая "+
			"и подписи всех карточек с привязанным материалом протухли разом."+
			"\n--- эталон ---\n%s\n--- сейчас ---\n%s", bomQtyGoldNoQtyHex, got)
	}
	for _, tag := range opKindTagsOf(t, bomQtyGoldStep()) {
		if tag == "bom_qty" {
			t.Fatal("у шага без количеств родился ПУСТОЙ хвост \"bom_qty\" — он сдвигает байты " +
				"ровно так же, как непустой")
		}
	}
}

// TestBomQtyTailChangesTheDigest — ВТОРОЙ вопрос: число доезжает до подписи.
//
// «Шаг ставит сюда 6 пуговиц» — инструкция цеху той же природы, что placement_count, который в
// хвосте уже хешируется: она меняет физическое изделие. Проставить количество на утверждённой
// карточке и НЕ сдвинуть подпись значило бы подписать одно, а отдать в цех другое.
func TestBomQtyTailChangesTheDigest(t *testing.T) {
	got := bomQtyGoldDigest(bomQtyGoldStep(bomQty("btn-1", "6")))
	if got == bomQtyGoldNoQtyHex {
		t.Fatal("количество на связи НЕ ВОШЛО в отпечаток CONSTRUCTION: подпись под карточкой " +
			"перестала удостоверять, сколько пуговиц ставит шаг")
	}
	if got != bomQtyGoldOneHex {
		t.Errorf("отпечаток шага с количеством поехал: либо изменилась форма хвоста \"bom_qty\", "+
			"либо его место среди хвостов, либо слой сборки отпечатка."+
			"\n--- эталон ---\n%s\n--- сейчас ---\n%s", bomQtyGoldOneHex, got)
	}
}

// TestBomQtyTailIsOrderIndependent — ТРЕТИЙ вопрос: детерминированность.
//
// Клиент волен прислать количества в любом порядке — это множество, а не последовательность. Если
// бы порядок доезжал до байт, одно и то же содержание карточки давало бы разные отпечатки при
// каждом сохранении, и «изменено после подписи» загоралось бы от перетасовки строк в форме.
//
// Сортировку делает operationKindTail (побайтно по ключу, стабильно) — своей второй здесь нет
// намеренно: второй список того же правила разъехался бы с первым при первой же правке.
func TestBomQtyTailIsOrderIndependent(t *testing.T) {
	straight := bomQtyGoldDigest(bomQtyGoldStep(bomQty("btn-1", "6"), bomQty("zip-1", "1")))
	reversed := bomQtyGoldDigest(bomQtyGoldStep(bomQty("zip-1", "1"), bomQty("btn-1", "6")))
	if straight != reversed {
		t.Fatalf("порядок присылки количеств доехал до отпечатка: прямой %s, обратный %s — "+
			"одна и та же карточка получила бы две разные подписи", straight, reversed)
	}
	if straight != bomQtyGoldTwoHex {
		t.Errorf("отпечаток шага с двумя количествами поехал:"+
			"\n--- эталон ---\n%s\n--- сейчас ---\n%s", bomQtyGoldTwoHex, straight)
	}
}

// TestBomQtyTailStandsLastInTheTuple — КОРТЕЖНАЯ половина заморозки: она говорит, ЧТО поехало,
// тогда как hex'и выше говорят только «поехало».
//
// Место хвоста заморожено САМЫМ ПОСЛЕДНИМ, после всех двенадцати хвостов волны видов операций:
// позиция хвоста в списке значима, и втиснуть новый между существующими уже нельзя.
func TestBomQtyTailStandsLastInTheTuple(t *testing.T) {
	op := bomQtyGoldStep(bomQty("btn-1", "6"))
	op.ZipperApplication = opGoldStr("invisible") // хвост "fastening"
	op.Work = opGoldStr("topstitch")              // хвост "work", двенадцатый
	tags := opKindTagsOf(t, op)
	if len(tags) < 3 {
		t.Fatalf("ожидались хвосты fastening, work и bom_qty, получено: %v", tags)
	}
	if last, prev := tags[len(tags)-1], tags[len(tags)-2]; last != "bom_qty" || prev != "work" {
		t.Fatalf("порядок хвостов %v: ожидалось …, work, bom_qty — место \"bom_qty\" заморожено "+
			"последним, после всех хвостов волны видов операций", tags)
	}
	want := []any{
		30, "lockstitch", "outer", nil, []string{"btn-1", "zip-1"}, "", 0,
		"", "", "", "", "", 0, "", "", "",
		[]any{"fastening", []any{[]any{"zipper_application", "invisible"}}},
		[]any{"work", []any{[]any{"work", "topstitch"}}},
		[]any{"bom_qty", []any{[]any{"btn-1", "6"}}},
	}
	if got := opGoldJSON(t, opGoldProject(t, op)); got != opGoldJSON(t, want) {
		t.Errorf("кортеж шага с количеством сдвинулся:\n--- эталон ---\n%s\n--- сейчас ---\n%s",
			opGoldJSON(t, want), got)
	}
}

// TestBomQtyDigestSurvivesTheColumnScale — ЗАПИСЬ И ЧТЕНИЕ ОБЯЗАНЫ ДАТЬ ОДИН ОТПЕЧАТОК.
//
// Клиент присылает «6», а DECIMAL(10,3) возвращает из MySQL «6.000» — одно и то же число двумя
// записями. Если бы они хешировались по-разному, подпись CONSTRUCTION рождалась бы протухшей в
// момент штампа и оставалась такой навсегда: сохранили по одной записи, прочитали по другой,
// «секция отредактирована после подписания» без единой правки человеком. Держится это тем, что
// количество едет в хвост СТРОКОЙ через digestDecimal, а decimal.String() масштаб нормализует.
//
// Вторая половина — что нормализация не съедает СОДЕРЖАНИЕ: 6 и 60 обязаны различаться.
func TestBomQtyDigestSurvivesTheColumnScale(t *testing.T) {
	wire := bomQtyGoldDigest(bomQtyGoldStep(bomQty("btn-1", "6")))
	fromDB := bomQtyGoldDigest(bomQtyGoldStep(bomQty("btn-1", "6.000")))
	if wire != fromDB {
		t.Fatalf("«6» с провода и «6.000» из колонки дали РАЗНЫЕ отпечатки (%s против %s): подпись "+
			"будет протухать между записью и чтением одной и той же карточки", wire, fromDB)
	}
	if other := bomQtyGoldDigest(bomQtyGoldStep(bomQty("btn-1", "60"))); other == wire {
		t.Fatal("6 и 60 дали один отпечаток — значит число до подписи не доезжает вовсе")
	}
}

// TestPlacementCountNeverBecomesBomQty — ГРАНИЦА С placement_count, названная вслух.
//
// Повторы шага и потраченные штуки — РАЗНЫЕ утверждения, и вывод одного из другого врёт на живых
// примерах: «обметать 6 петель» повторяется шесть раз и не тратит НИ ОДНОЙ пуговицы (петля тратит
// нитку). Шаг с placement_count = 6 и связью БЕЗ количества обязан хешироваться ровно как шаг без
// количеств — то есть шестёрка не должна нигде прочитаться как «6 пуговиц».
func TestPlacementCountNeverBecomesBomQty(t *testing.T) {
	withPlacement := bomQtyGoldStep()
	withPlacement.PlacementCount = opGoldI32(6)

	for _, tag := range opKindTagsOf(t, withPlacement) {
		if tag == "bom_qty" {
			t.Fatal("placement_count родил хвост \"bom_qty\": число повторов шага прочиталось как " +
				"количество потраченного артикула")
		}
	}
	// И то же самое с другой стороны: шаг, где количество названо ЯВНО и равно шести, обязан
	// отличаться от шага, где шесть — это повторы. Совпадение отпечатков означало бы, что два
	// разных утверждения о цехе неразличимы под одной подписью.
	explicit := bomQtyGoldStep(bomQty("btn-1", "6"))
	explicit.PlacementCount = opGoldI32(6)
	if bomQtyGoldDigest(withPlacement) == bomQtyGoldDigest(explicit) {
		t.Fatal("«шаг повторяется 6 раз» и «шаг ставит 6 пуговиц» дали один отпечаток")
	}
}
