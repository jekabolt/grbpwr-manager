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
