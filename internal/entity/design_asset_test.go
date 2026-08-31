package entity

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ ЧИСТЫХ ПРАВИЛ ПОЛКИ АССЕТОВ (0354).
//
// ПОЧЕМУ ОНИ ЗДЕСЬ, А НЕ В СТОРЕ. Правила апсерта делятся надвое: тем, что ниже, не нужно ничего,
// кроме запроса, а тем, что осталось в транзакции (родитель той же карточки, медиа не чужое, полка
// не переполнена), нужна живая строка. Первую половину вынесли в entity.DesignAssetUpsert.Validate
// ровно затем, чтобы её было чем проверить: правило, которое исполняется только против базы, — это
// правило, которое на практике не исполняет никто.

// СЛОВАРЬ ПОЛОК: три члена и ни одного больше.
//
// МУТАЦИЯ: добавить в DesignAssetKinds четвёртое значение либо вернуть true из IsDesignAssetKind
// безусловно — «trim» краснеет первым. CHECK в схеме намеренно нет, поэтому этот словарь и есть
// единственный сторож: пропусти он «Fabric» с большой буквы, и на полке появилась бы вторая
// ткань-которая-не-ткань, невидимая ни одному фильтру по kind.
func TestDesignAssetKindDictionaryIsClosed(t *testing.T) {
	for _, ok := range []string{DesignAssetKindFabric, DesignAssetKindPattern, DesignAssetKindHardware} {
		assert.True(t, IsDesignAssetKind(ok), "%q — объявленная полка", ok)
	}
	for _, bad := range []string{"", "trim", "Fabric", "FABRIC", "fabric ", "patterns"} {
		assert.False(t, IsDesignAssetKind(bad), "%q полкой не объявлен", bad)
	}
	require.Len(t, DesignAssetKinds, 3, "полок три: ткани, паттерны, фурнитура")
}

// ВСЕ ЧИСТЫЕ ОТКАЗЫ АПСЕРТА, ПО ОДНОМУ НА СТРОКУ.
//
// КАЖДЫЙ СЛУЧАЙ НАЗЫВАЕТ СВОЙ SENTINEL, а не просто «ошибка»: у четырёх отказов полки четыре
// разных машинных токена на проводе и четыре разных починки на экране, и проба, сверяющая лишь
// «err != nil», разрешила бы им схлопнуться в один.
func TestDesignAssetUpsertValidate(t *testing.T) {
	base := DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric, Name: "main jersey"}
	pattern := func(mut func(*DesignAssetUpsert)) DesignAssetUpsert {
		r := DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindPattern, Name: "stripe"}
		mut(&r)
		return r
	}

	for _, tc := range []struct {
		name string
		req  DesignAssetUpsert
		want error // nil = запрос законен
	}{
		{"ткань как есть", base, nil},
		{"фурнитура как есть",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindHardware, Name: "gunmetal snap"}, nil},
		{"паттерн с родителем и раппортом",
			pattern(func(r *DesignAssetUpsert) { r.DerivedFromAssetId = 7; r.RepeatMm = 120 }), nil},

		{"неизвестная полка",
			DesignAssetUpsert{TechCardId: 41, Kind: "trim", Name: "x"}, ErrDesignAssetKindUnknown},
		{"полка не названа вовсе",
			DesignAssetUpsert{TechCardId: 41, Name: "x"}, ErrDesignAssetKindUnknown},

		{"имя пустое",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric}, ErrDesignAssetNameRequired},
		{"имя из одних пробелов",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric, Name: "  \t "}, ErrDesignAssetNameRequired},
		{"имя ровно по границе",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric,
				Name: strings.Repeat("ф", MaxDesignAssetNameRunes)}, nil},
		{"имя на руну длиннее",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric,
				Name: strings.Repeat("ф", MaxDesignAssetNameRunes+1)}, ErrDesignInvalidArgument},
		{"записка ровно по границе",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric, Name: "x",
				Note: strings.Repeat("я", MaxDesignAssetNoteRunes)}, nil},
		{"записка на руну длиннее",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric, Name: "x",
				Note: strings.Repeat("я", MaxDesignAssetNoteRunes+1)}, ErrDesignInvalidArgument},

		{"раппорт по нижней границе", pattern(func(r *DesignAssetUpsert) { r.RepeatMm = 0 }), nil},
		{"раппорт по верхней границе",
			pattern(func(r *DesignAssetUpsert) { r.RepeatMm = MaxDesignAssetRepeatMm }), nil},
		{"раппорт за верхней границей",
			pattern(func(r *DesignAssetUpsert) { r.RepeatMm = MaxDesignAssetRepeatMm + 1 }), ErrDesignInvalidArgument},
		{"раппорт отрицательный",
			pattern(func(r *DesignAssetUpsert) { r.RepeatMm = -1 }), ErrDesignInvalidArgument},

		{"поворот по нижней границе", pattern(func(r *DesignAssetUpsert) { r.RotationDeg = 0 }), nil},
		{"поворот по верхней границе",
			pattern(func(r *DesignAssetUpsert) { r.RotationDeg = MaxDesignAssetRotationDeg }), nil},
		{"поворот 360 это 0, записанный вторым способом",
			pattern(func(r *DesignAssetUpsert) { r.RotationDeg = 360 }), ErrDesignInvalidArgument},
		{"поворот отрицательный",
			pattern(func(r *DesignAssetUpsert) { r.RotationDeg = -1 }), ErrDesignInvalidArgument},

		{"ткань заявила раппорт",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric, Name: "x", RepeatMm: 120},
			ErrDesignAssetNotAPattern},
		{"ткань заявила родителя",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric, Name: "x", DerivedFromAssetId: 7},
			ErrDesignAssetNotAPattern},
		{"фурнитура заявила раппорт",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindHardware, Name: "x", RepeatMm: 15},
			ErrDesignAssetNotAPattern},
		{"поворот НЕ делает не-паттерн паттерном",
			DesignAssetUpsert{TechCardId: 41, Kind: DesignAssetKindFabric, Name: "x", RotationDeg: 90}, nil},

		{"паттерн сам себе родитель",
			pattern(func(r *DesignAssetUpsert) { r.AssetId = 9; r.DerivedFromAssetId = 9 }),
			ErrDesignInvalidArgument},
		{"родитель отрицательный",
			pattern(func(r *DesignAssetUpsert) { r.DerivedFromAssetId = -3 }), ErrDesignInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want),
				"ожидался %v, получено %v — у каждого отказа полки свой машинный токен", tc.want, err)
		})
	}
}
