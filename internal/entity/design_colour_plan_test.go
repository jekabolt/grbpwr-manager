package entity

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ФОРМА ЦВЕТОВОГО ПЛАНА ПРОВЕРЯЕТСЯ ЗДЕСЬ, ПОТОМУ ЧТО В СХЕМЕ ЕЙ МЕСТА НЕТ. CHECK-ограничение
// копирует таблицу целиком и проверяется против всей истории — в этом проекте это однажды
// остановило старт прода (0364 записывает довод). Значит форму держит Go, а раз так, у неё обязана
// быть проба: молча принятый кривой документ переживёт запуск и уедет в промпт.

func planSave(mut func(*DesignColourPlanSave)) DesignColourPlanSave {
	s := DesignColourPlanSave{
		TechCardId: 7,
		Maps: []DesignColourMap{{
			MediaId: 20, View: DesignViewFront, BaseMediaId: 1,
			Palette: []DesignColourSwatch{{Hex: "#3a7bd5", Px: 40000}, {Hex: "#ff0000", Px: 900}},
		}},
		Cloths: []DesignColourCloth{
			{Hex: "#3a7bd5", AssetId: 4},
			{Hex: "#ff0000", Words: "2x2 rib"},
		},
		Actor: "probe",
	}
	if mut != nil {
		mut(&s)
	}
	return s
}

// TestDesignColourPlanValidate — таблица отказов, и КАЖДАЯ строка называет свой ущерб.
//
// ⚠ ПЕРВАЯ СТРОКА — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, и без неё вся таблица зеленела бы у функции, которая
// отказывает ВСЕМУ. Это не украшение: «проверка формы» — ровно тот класс кода, который ломается в
// сторону избыточной строгости, и заметить это по списку красных нельзя.
func TestDesignColourPlanValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		in   DesignColourPlanSave
		ok   bool
	}{
		{"a whole plan", planSave(nil), true},
		{"an empty plan — painted, then cleared, and that is a state a person made",
			planSave(func(s *DesignColourPlanSave) { s.Maps, s.Cloths = nil, nil }), true},
		{"a map nobody has assigned yet — the ordinary half-finished screen",
			planSave(func(s *DesignColourPlanSave) { s.Cloths = nil }), true},
		{"six maps, the ceiling", planSave(func(s *DesignColourPlanSave) {
			s.Maps = nil
			s.Cloths = nil
			for _, v := range DesignSilhouetteViews {
				s.Maps = append(s.Maps, DesignColourMap{MediaId: 20, View: v, BaseMediaId: 1})
			}
		}), true},

		{"seven maps — the silhouette has six sides, so a seventh is painted on nothing",
			planSave(func(s *DesignColourPlanSave) {
				for i := 0; i < 7; i++ {
					s.Maps = append(s.Maps, DesignColourMap{MediaId: 30 + i, View: DesignViewBack, BaseMediaId: 2})
				}
			}), false},
		{"a view the silhouette does not have — the prompt would print a word it does not know",
			planSave(func(s *DesignColourPlanSave) { s.Maps[0].View = "front-ish" }), false},
		{"two maps for one view — two disagreeing answers to one question",
			planSave(func(s *DesignColourPlanSave) {
				s.Maps = append(s.Maps, DesignColourMap{MediaId: 21, View: DesignViewFront, BaseMediaId: 1})
			}), false},
		{"a map with no picture — a map IS a picture",
			planSave(func(s *DesignColourPlanSave) { s.Maps[0].MediaId = 0 }), false},
		{"a map that cannot name its base — un-stale-able for ever after",
			planSave(func(s *DesignColourPlanSave) { s.Maps[0].BaseMediaId = 0 }), false},
		{"an upper-case label — the hex is the KEY a cloth finds its parts by",
			planSave(func(s *DesignColourPlanSave) {
				s.Maps[0].Palette[0].Hex = "#3A7BD5"
				s.Cloths[0].Hex = "#3A7BD5"
			}), false},
		{"a three-digit label", planSave(func(s *DesignColourPlanSave) {
			s.Maps[0].Palette[0].Hex = "#abc"
			s.Cloths[0].Hex = "#abc"
		}), false},
		{"black as a label — that is the line ink, not a cloth",
			planSave(func(s *DesignColourPlanSave) {
				s.Maps[0].Palette[0].Hex = "#000000"
				s.Cloths[0].Hex = "#000000"
			}), false},
		{"white as a label — that is the paper",
			planSave(func(s *DesignColourPlanSave) {
				s.Maps[0].Palette[0].Hex = "#ffffff"
				s.Cloths[0].Hex = "#ffffff"
			}), false},
		{"an assignment to a colour nobody painted — it would outlive the repaint that removed it",
			planSave(func(s *DesignColourPlanSave) {
				s.Cloths = append(s.Cloths, DesignColourCloth{Hex: "#00ff00", AssetId: 9})
			}), false},
		{"the same hex assigned twice — the hex is the key",
			planSave(func(s *DesignColourPlanSave) {
				s.Cloths = append(s.Cloths, DesignColourCloth{Hex: "#3a7bd5", Words: "and also this"})
			}), false},
		{"an assignment that says nothing — a label pointing at silence",
			planSave(func(s *DesignColourPlanSave) { s.Cloths[0] = DesignColourCloth{Hex: "#3a7bd5"} }), false},
		{"a negative pixel count", planSave(func(s *DesignColourPlanSave) { s.Maps[0].Palette[0].Px = -1 }), false},
		{"no card at all", planSave(func(s *DesignColourPlanSave) { s.TechCardId = 0 }), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.in.Validate()
			if c.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrDesignInvalidArgument),
				"a malformed plan is InvalidArgument — the request is fixed by editing the request")
		})
	}
}

// TestDesignColourClothStated — «сказала ли строка хоть что-нибудь», и каждая из четырёх половин
// считается сама по себе.
//
// ⚠ МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: считать строку сказанной по одному лишь `asset_id`, либо наоборот
// — забыть один из четырёх способов ответить. Первое пускает пустую строку, второе ОТКАЗЫВАЕТ
// человеку, который назвал цвет одними словами, — а это полный и законный ответ на вопрос «из чего
// эта деталь».
func TestDesignColourClothStated(t *testing.T) {
	require.False(t, DesignColourCloth{Hex: "#3a7bd5"}.Stated())
	require.False(t, DesignColourCloth{Hex: "#3a7bd5", Words: "   "}.Stated(),
		"пробелы — это не слова")
	require.True(t, DesignColourCloth{Hex: "#3a7bd5", AssetId: 4}.Stated())
	require.True(t, DesignColourCloth{Hex: "#3a7bd5", ColourHex: "#112233"}.Stated())
	require.True(t, DesignColourCloth{Hex: "#3a7bd5", Words: "2x2 rib"}.Stated())
	require.True(t, DesignColourCloth{Hex: "#3a7bd5", Parts: "cuffs"}.Stated())
}

// TestIsDesignColourMapHex — ОДИН читатель формата ярлыка на всю полосу, поэтому у него одна проба.
func TestIsDesignColourMapHex(t *testing.T) {
	for _, v := range []string{"#3a7bd5", "#000001", "#fffffe", "#abcdef", "#012345"} {
		require.Truef(t, IsDesignColourMapHex(v), "%q is a label", v)
	}
	for _, v := range []string{
		"", "#", "3a7bd5", "#3a7bd", "#3a7bd55", "#3A7BD5", "#3a7bdg", "# 3a7bd", "#000000", "#ffffff",
	} {
		require.Falsef(t, IsDesignColourMapHex(v), "%q is not a label", v)
	}
}
