package techcard

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ГРАНИЦА СЕКЦИЙ СЧЁТНОЙ НОРМЫ (0333) — проверяется в Go, а не CHECK'ом, ровно по доводу 0278:
// двухколоночный CHECK (section ↔ qty_per_garment) выстрелил бы сырым 3819 на UPDATE'е, правящем
// одну лишь секцию, и назвал бы оператору колонку, которой тот не касался.

func cqDec(v string) decimal.NullDecimal {
	return decimal.NewNullDecimal(decimal.RequireFromString(v))
}

func TestBomCountableSectionBoundary(t *testing.T) {
	// Счётные секции принимают обе половины пары.
	for _, s := range []entity.TechCardBomSection{
		entity.BomSectionHardware, entity.BomSectionLabel, entity.BomSectionPackaging,
		entity.BomSectionDecoration, entity.BomSectionOther,
	} {
		require.NoError(t, validateBomCountableSection(&entity.TechCardBomItem{
			Section: s, QtyPerGarment: cqDec("6"), SpareQty: cqDec("1"),
		}, 0), "section %q", s)
	}

	// Мерные — отвергают, и отказ называет секцию, которая ФАКТИЧЕСКИ сработала, и способ выйти.
	for _, s := range entity.MeasuredSectionList {
		err := validateBomCountableSection(&entity.TechCardBomItem{Section: s, QtyPerGarment: cqDec("6")}, 3)
		require.Error(t, err, "section %q", s)
		require.Contains(t, err.Error(), string(s), "отказ обязан назвать секцию строки")
		require.Contains(t, err.Error(), "bom_items[3].qty_per_garment", "отказ обязан указать на поле")
	}

	// Запас без количества на мерной секции адресуется СВОИМ полем: указать на пустое
	// qty_per_garment значило бы послать оператора чинить контрол, которого он не трогал.
	err := validateBomCountableSection(&entity.TechCardBomItem{
		Section: entity.BomSectionFabric, SpareQty: cqDec("1"),
	}, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bom_items[2].spare_qty")

	// Пустая пара на мерной секции — это КАЖДАЯ существующая строка ткани: молчание, не отказ.
	require.NoError(t, validateBomCountableSection(&entity.TechCardBomItem{Section: entity.BomSectionFabric}, 0))
}

// TestBomCountableClearedWhenSectionBecomesMeasured — ДЫРА МЕЖДУ ПРИСЛАННЫМ И СОХРАНЁННЫМ.
//
// validateBomCountableSection проверяет то, что ПРИШЛО. Но вкладка со старым бандлом счётных полей
// не шлёт вовсе, флаг присутствия говорит «не трогай», и IF в UPDATE сохраняет число, лежащее в
// базе, — на строке, которая этим же сейвом стала мерной. Проверка при этом молчит: проверять
// нечего.
//
// Итог такого сейва — мерная строка со счётной нормой, состояние, запрещённое по определению.
// Резолвер её игнорирует (граница «счётное/мерное» держится у него), поэтому деньги строки
// исчезают ПОСЛЕ УСПЕШНОГО сохранения, которое оператор считает безобидным.
//
// Здесь проверяется, что параметры записи закрывают это не проверкой, а формой: на мерной секции
// обе колонки уходят в NULL, а флаг присутствия снимается — иначе IF сохранил бы старое.
func TestBomCountableClearedWhenSectionBecomesMeasured(t *testing.T) {
	// Старый бандл: полей нет, флаг «не трогай» стоит, секция сменилась на мерную.
	stale := &entity.TechCardBomItem{
		Section:          entity.BomSectionTrim,
		Name:             "тесьма",
		CountableOmitted: true,
	}
	p := bomItemParams(1, stale, 0, "K0000000000000000000000001")
	if p["countable_omitted"] != false {
		t.Fatalf("countable_omitted=%v на мерной секции: IF в UPDATE сохранит счётную норму, "+
			"которой на этой строке быть не может", p["countable_omitted"])
	}
	if q, ok := p["qty_per_garment"].(decimal.NullDecimal); !ok || q.Valid {
		t.Fatalf("qty_per_garment=%v на мерной секции, ожидался NULL", p["qty_per_garment"])
	}
	if q, ok := p["spare_qty"].(decimal.NullDecimal); !ok || q.Valid {
		t.Fatalf("spare_qty=%v на мерной секции, ожидался NULL", p["spare_qty"])
	}

	// На счётной секции ничего не меняется: «не трогай» остаётся «не трогай».
	keep := &entity.TechCardBomItem{
		Section:          entity.BomSectionHardware,
		Name:             "пуговица",
		CountableOmitted: true,
	}
	if p := bomItemParams(1, keep, 0, "K0000000000000000000000002"); p["countable_omitted"] != true {
		t.Fatalf("countable_omitted=%v на счётной секции: сейв старого бандла обнулил бы счётную "+
			"норму у всех строк карточки", p["countable_omitted"])
	}
}
