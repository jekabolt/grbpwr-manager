package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ПРОЕКЦИЯ УПАКОВОЧНОГО ЛИСТА — ГОЛОВА ПЛЮС ТЕГИРОВАННЫЕ ПАРНЫЕ ХВОСТЫ (T8a).
//
// До этой правки packagingProjection была плоским позиционным массивом без единого различителя, то
// есть структурой, в которую нельзя дописать поле: одиннадцатый элемент, поставленный безусловно,
// сдвинул бы отпечаток КАЖДОГО упаковочного листа в базе и объявил бы все утверждённые подписи
// PACKAGING устаревшими в момент выкатки. T8b заводит листу пять полей сразу, поэтому механизм
// хвоста обязан появиться раньше самих полей — иначе первое же поле придётся вносить ценой волны.
//
// Здесь заморожены обе половины: ГОЛОВА (десять позиций, байт в байт как до правки) и ФОРМА того,
// что дописывается правее неё.

func packagingGoldCard() *entity.TechCardInsert {
	return &entity.TechCardInsert{Packaging: &entity.TechCardPackaging{
		FoldingMethod:    ns("пополам, рукава внутрь"),
		Polybag:          ns("PE 40 мкм, перфорация"),
		BagSticker:       ns("размер + EAN"),
		Inserts:          ns("картонка A5"),
		UnitsPerBox:      ni32(12),
		BoxMarking:       ns("GRB / артикул / размер"),
		BoxDimensions:    ns("600x400x300"),
		WeightNetGrams:   ni32(420),
		WeightGrossGrams: ni32(470),
		Notes:            ns("не мять воротник"),
	}}
}

func packagingDigest(tc *entity.TechCardInsert) string {
	return TechCardSectionDigests(tc)[entity.SignoffPackaging]
}

// ЗАМОРОЖЕННЫЙ HEX PACKAGING — эталона этой секции не было вовсе, и его отсутствие было дырой того
// же рода, что закрывает opGoldConstructionDigestHex у CONSTRUCTION: правка слоя сборки (кодировка,
// разделители, функция хеша, порядок склейки) оставила бы проекцию байт в байт прежней, а подписи
// всех упаковочных листов протухли бы молча.
//
// Значение снято ПОСЛЕ введения механизма хвоста и обязано совпадать со старым — сегодня ни одно
// семейство не рождается, поэтому дописывать правее головы нечего. Что это именно так, проверяется
// не доверием к литералу, а реконструкцией старой формы прямо в тесте ниже.
const packagingGoldDigestHex = "afe5a44dfc4ec64d8aae5384fd4645eb93d72899a1133a2a09bf115a5577a5e4"

// TestPackagingDigestUnchangedByTheTailMechanism — ГЛАВНОЕ ДОКАЗАТЕЛЬСТВО T8a: заведение механизма
// хвоста не двинуло отпечаток ни одного листа.
//
// Старая форма выписана здесь ДОСЛОВНО, а не скопирована хешем: сравнение идёт со структурой,
// которую проекция имела до правки, поэтому тест ловит и подмену значения в голове, и появление
// одиннадцатого элемента, и смену порядка позиций — а не только сдвиг литерала.
func TestPackagingDigestUnchangedByTheTailMechanism(t *testing.T) {
	tc := packagingGoldCard()
	p := tc.Packaging
	before := []any{
		p.FoldingMethod.String, p.Polybag.String, p.BagSticker.String, p.Inserts.String,
		p.UnitsPerBox.Int32, p.BoxMarking.String, p.BoxDimensions.String,
		p.WeightNetGrams.Int32, p.WeightGrossGrams.Int32, p.Notes.String,
	}
	if got, want := packagingDigest(tc), digestOf(before); got != want {
		t.Fatalf("отпечаток упаковочного листа уехал от заведения механизма хвоста — значит правее "+
			"головы что-то дописалось безусловно, и все подписи PACKAGING протухли в момент "+
			"выкатки.\n--- старая форма ---\n%s\n--- сейчас ---\n%s", want, got)
	}
}

// TestPackagingDigestHexFrozen — тот же эталон литералом, и он ловит то, чего не видит тест выше:
// правку слоя сборки. digestOf кодирует проекцию json.Marshal и берёт sha256; смена кодировки,
// разделителей, функции хеша или порядка склейки оставит ОБЕ формы одинаковыми и тест выше
// зелёным, а hex поедет у всех листов разом.
func TestPackagingDigestHexFrozen(t *testing.T) {
	if got := packagingDigest(packagingGoldCard()); got != packagingGoldDigestHex {
		t.Errorf("отпечаток PACKAGING поехал при неизменной проекции — поехал слой сборки, и "+
			"подписи ВСЕХ упаковочных листов протухли разом.\n--- эталон ---\n%s\n--- сейчас ---\n%s",
			packagingGoldDigestHex, got)
	}
}

// TestPackagingHeadIsTenPositions фиксирует САМО ПРАВИЛО, а не значения: голова ровно десять
// позиций, всё правее неё — тегированный хвост «[имя, пары]». Этот тест обязан упасть, когда T8b
// (или кто угодно после неё) допишет поле БЕЗУСЛОВНО.
func TestPackagingHeadIsTenPositions(t *testing.T) {
	const head = 10
	row, ok := packagingProjection(packagingGoldCard()).([]any)
	if !ok {
		t.Fatalf("проекция упаковочного листа перестала быть []any")
	}
	if len(row) < head {
		t.Fatalf("голова упаковочного листа короче %d позиций (%d) — из неё удалили элемент, "+
			"а это тот же безусловный сдвиг, что и дописка", head, len(row))
	}
	for i, tail := range row[head:] {
		tagged, ok := tail.([]any)
		if !ok || len(tagged) == 0 {
			t.Errorf("хвост %d не тегированный кортеж: %#v", i, tail)
			continue
		}
		if tag, ok := tagged[0].(string); !ok || tag == "" {
			t.Errorf("хвост %d начинается не с имени-тега: %#v — голый хвост запрещён", i, tagged[0])
		}
		if !opKindIsPairTail(tagged) {
			t.Errorf("хвост %d не парной формы: %#v — дописать в него поле нельзя, не сдвинув "+
				"отпечаток каждого листа, который его эмитит", i, tagged)
		}
	}
}

// TestPackagingTailsAreEmptyToday — ОБЕЩАНИЕ НУЛЕВОЙ ВОЛНЫ, отдельной клеткой: сегодня ни одно
// семейство не рождается, значит байты сегодняшних листов не двигаются вовсе.
func TestPackagingTailsAreEmptyToday(t *testing.T) {
	if tails := packagingTails(packagingGoldCard().Packaging); len(tails) != 0 {
		t.Errorf("packagingTails на полностью заполненном листе вернул %d хвостов, ожидалось 0 — "+
			"поле уехало из головы в хвост, а это сдвиг отпечатка: %#v", len(tails), tails)
	}
}

// TestPackagingEmptyFieldDoesNotMoveTheDigest — СВОЙСТВО, РАДИ КОТОРОГО МЕХАНИЗМ И ЗАВОДИТСЯ,
// проверенное ПРЯМО на hex, а не рассуждением.
//
// «Завтра» — это лист T8b: тот же лист, но набор ключей семейства "fold" расширен полями, которых
// сегодня нет. Пока они пусты, отпечаток обязан совпадать побайтно; как только одно из них
// заполнено — обязан разойтись, иначе хвост врал бы о содержании.
func TestPackagingEmptyFieldDoesNotMoveTheDigest(t *testing.T) {
	tomorrow := func(tail []any) string {
		row, ok := packagingProjection(packagingGoldCard()).([]any)
		if !ok {
			t.Fatalf("проекция упаковочного листа перестала быть []any")
		}
		next := append([]any(nil), row...)
		if tail != nil {
			next = append(next, tail)
		}
		return digestOf(next)
	}
	today := packagingDigest(packagingGoldCard())

	// Все пять полей T8b пусты ⇒ пар нет ⇒ хвоста нет ⇒ байты те же.
	empty := operationKindTail("fold",
		opKindStr("fold_scheme", sql.NullString{}),
		opKindDec("folded_w_mm", decimal.NullDecimal{}),
		opKindDec("folded_h_mm", decimal.NullDecimal{}),
		opKindBool("face_out", sql.NullBool{}),
		opKindBool("metal_pins_prohibited", sql.NullBool{}),
	)
	if empty != nil {
		t.Fatalf("хвост из одних пустых полей всё-таки родился: %#v", empty)
	}
	if got := tomorrow(empty); got != today {
		t.Errorf("дописка ПУСТЫХ полей в упаковочный лист сдвинула отпечаток — механизм хвоста не "+
			"защищает подписи, и волна T8b протухнет всю базу.\nсегодня: %s\nзавтра:  %s", today, got)
	}

	// Обратная половина: заполненное поле обязано говорить.
	filled := operationKindTail("fold",
		opKindStr("fold_scheme", ns("bifold_sleeves_in")),
		opKindDec("folded_w_mm", decimal.NullDecimal{}),
		opKindBool("face_out", sql.NullBool{}),
	)
	if filled == nil {
		t.Fatalf("хвост с заполненным полем не родился")
	}
	if got := tomorrow(filled); got == today {
		t.Errorf("заполненная схема укладки не сдвинула отпечаток — подпись под одной укладкой " +
			"читалась бы как действительная под другой")
	}
}

// TestEveryPackagingFieldMovesTheDigest — десять полей головы поимённо. Поле, попавшее в чужую
// позицию (или не попавшее никуда), проявится здесь совпадением двух отпечатков, а в цехе —
// подписью под упаковкой, которой никто не видел.
func TestEveryPackagingFieldMovesTheDigest(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*entity.TechCardPackaging)
	}{
		{"folding_method", func(p *entity.TechCardPackaging) { p.FoldingMethod = ns("втрое") }},
		{"polybag", func(p *entity.TechCardPackaging) { p.Polybag = ns("LDPE 50 мкм") }},
		{"bag_sticker", func(p *entity.TechCardPackaging) { p.BagSticker = ns("только EAN") }},
		{"inserts", func(p *entity.TechCardPackaging) { p.Inserts = ns("шёлковая бумага") }},
		{"units_per_box", func(p *entity.TechCardPackaging) { p.UnitsPerBox = ni32(24) }},
		{"box_marking", func(p *entity.TechCardPackaging) { p.BoxMarking = ns("без маркировки") }},
		{"box_dimensions", func(p *entity.TechCardPackaging) { p.BoxDimensions = ns("800x400x300") }},
		{"weight_net_grams", func(p *entity.TechCardPackaging) { p.WeightNetGrams = ni32(421) }},
		{"weight_gross_grams", func(p *entity.TechCardPackaging) { p.WeightGrossGrams = ni32(471) }},
		{"notes", func(p *entity.TechCardPackaging) { p.Notes = ns("не мять манжету") }},
	}
	seen := map[string]string{packagingGoldDigestHex: "лист без правок"}
	for _, m := range mutations {
		tc := packagingGoldCard()
		m.mutate(tc.Packaging)
		d := packagingDigest(tc)
		if prev, dup := seen[d]; dup {
			t.Fatalf("%q и %q дают ОДИН отпечаток PACKAGING: поле не хешируется или заняло чужую позицию",
				prev, m.name)
		}
		seen[d] = m.name
	}
}
