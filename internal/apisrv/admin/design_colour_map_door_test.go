package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── ДВЕРЬ ПРОГОНА ПРОТИВ РЕЦЕПТА, КОТОРЫЙ ЗАСТАВИЛ БЫ ПРОМПТ СОВРАТЬ ────────────────────────────
//
// Каждый случай ниже адверсарное ревью ЗАМЕРИЛО на собранном промпте: рецепт проходил дверь, деньги
// резервировались, и модель получала утверждение про картинку, которой у неё нет. Промпт с тех пор
// молчит про такую карту (designgen: colourMapsSent), но молчание — это не то, о чём просил
// человек: он просил разложить ткани по покрашенным деталям. Поэтому отказ, словами и ДО денег.

// designProbeMapRecipe — покрашенный рецепт «как из студии»: одна карта переда с двумя ярлыками и
// две ткани, каждая на своём ярлыке.
func designProbeMapRecipe(mut func(*pb_common.DesignColourRecipe)) *pb_common.DesignRunParams {
	c := &pb_common.DesignColourRecipe{
		Code: "RED-01", Hex: "#b1121a",
		ColourMaps: []*pb_common.DesignColourMap{{
			MediaId: 20, View: entity.DesignViewFront, BaseMediaId: 1,
			Palette: []*pb_common.DesignColourSwatch{
				{Hex: "#3a7bd5", Px: 40000}, {Hex: "#ff0000", Px: 900},
			},
		}},
		Fabrics: []*pb_common.DesignFabricUse{
			{Name: "main jersey", MediaId: 9, MapHex: "#3a7bd5"},
			{Name: "contrast rib", MediaId: 10, MapHex: "#ff0000"},
		},
	}
	if mut != nil {
		mut(c)
	}
	return &pb_common.DesignRunParams{Views: []string{entity.DesignViewFront}, Colour: c}
}

// TestTheDoorRefusesAMapLabelThatAddressesNothing — ЗАМЕР №1 И ЗАМЕР №7 НА ОДНОЙ ДВЕРИ.
//
// Проверялась ФОРМА `map_hex` и только она: ни «есть ли карта вообще», ни «есть ли этот ярлык на
// её палитре», ни «не заявили ли его две ткани сразу».
func TestTheDoorRefusesAMapLabelThatAddressesNothing(t *testing.T) {
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ПЕРВЫМ: законный покрашенный рецепт проходит. Без него вся таблица
	// зеленела бы у двери, отказывающей всему, — то есть у закрытой фичи.
	require.NoError(t, designRefuseMalformedColourMaps(designProbeMapRecipe(nil)))

	// ЯРЛЫК БЕЗ ЕДИНОЙ КАРТЫ. Замер ревью: строки тканей говорили «used on the parts painted steel
	// blue (#3a7bd5) on the colour map», а карты в запросе не было вовсе.
	err := designRefuseMalformedColourMaps(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps = nil
	}))
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "this run carries none")

	// ЯРЛЫК, КОТОРОГО НЕТ НИ НА ОДНОЙ ПАЛИТРЕ. Карта уехала, но такого цвета на ней никто не
	// красил: модель отправят искать область, которой нет.
	err = designRefuseMalformedColourMaps(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.Fabrics[1].MapHex = "#00ff00"
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "on the palette of no colour map")

	// ДВЕ ТКАНИ НА ОДИН ЯРЛЫК (находка 7). Обе строки заявили бы «and on no other part» — два
	// взаимно исключающих утверждения об одной области, оба абсолютные. У ПЛАНА этот дубль
	// отвергался с первого дня; у двери прогона эквивалента не было.
	err = designRefuseMalformedColourMaps(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.Fabrics[1].MapHex = "#3a7bd5"
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "already claimed by fabrics.0")

	// ⚠ И МОЛЧАЩАЯ ТКАНЬ ПО-ПРЕЖНЕМУ ЗАКОННА: ткань без `map_hex` — обычный случай, и дверь,
	// потребовавшая ярлык у всех, закрыла бы одноклоточный прогон.
	require.NoError(t, designRefuseMalformedColourMaps(designProbeMapRecipe(
		func(c *pb_common.DesignColourRecipe) {
			c.Fabrics[0].MapHex = ""
			c.Fabrics[1].MapHex = ""
		})))
}

// TestTheDoorRefusesTwoMapsOnOnePicture — ЗАМЕР №2: `colour_maps:[{20,"front"},{20,"back"}]`.
//
// Дубль ВИДА отвергался, дубль КАРТИНКИ — нет. Список вложений дедуплицирует по media id и
// СКЛЕИВАЕТ подписи, поэтому одна картинка объявлялась картой двух разных видов, а предложение
// читалось «Images 3 and 3» — на платном вызове.
func TestTheDoorRefusesTwoMapsOnOnePicture(t *testing.T) {
	err := designRefuseMalformedColourMaps(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps = append(c.ColourMaps, &pb_common.DesignColourMap{
			MediaId: 20, View: entity.DesignViewBack, BaseMediaId: 2,
			Palette: []*pb_common.DesignColourSwatch{{Hex: "#3a7bd5", Px: 10}},
		})
	}))
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "one picture is one map")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: ДВЕ карты на две картинки — обычный покрашенный прогон.
	require.NoError(t, designRefuseMalformedColourMaps(designProbeMapRecipe(
		func(c *pb_common.DesignColourRecipe) {
			c.ColourMaps = append(c.ColourMaps, &pb_common.DesignColourMap{
				MediaId: 21, View: entity.DesignViewBack, BaseMediaId: 2,
				Palette: []*pb_common.DesignColourSwatch{{Hex: "#3a7bd5", Px: 10}},
			})
		})))
}

// TestTheDoorRefusesAColourMapThatIsAlsoAnInput — ЗАМЕР №3.
//
// `media_id` карты и `base_media_id` карты — соседи на одном сообщении, и база это И ЕСТЬ флэт,
// поэтому опечатка в ОДНО поле объявляет картой ПЛИТУ ВЕРСТАКА. Замер: одна картинка, подписанная
// одновременно «current state of the garment — front view» и «colour map … those colours LABEL
// which cloth covers which part» — ровно тот провал, ради предотвращения которого блок подписей и
// написан. У соседнего случая сторож был (designClothAlsoAPhotograph), у карт — нет.
func TestTheDoorRefusesAColourMapThatIsAlsoAnInput(t *testing.T) {
	inputs := &pb_common.DesignInputSnapshot{
		Slots: []*pb_common.DesignInputSlot{{ViewKey: entity.DesignViewFront, MediaId: 1}},
		Refs:  []*pb_common.DesignInputRef{{MediaId: 7, Role: "mood"}},
	}

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: карта своей собственной картинкой проходит.
	require.NoError(t, designRefuseColourMapAlsoAnInput(designProbeMapRecipe(nil), inputs))

	// ПЛИТА ВЕРСТАКА, НАЗВАННАЯ КАРТОЙ — та самая опечатка в одно поле.
	err := designRefuseColourMapAlsoAnInput(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps[0].MediaId = 1
	}), inputs)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "the bench plate on front")
	require.Contains(t, err.Error(), "base_media_id")
	require.Contains(t, err.Error(), "nothing was charged")

	// РЕФЕРЕНС КАРТОЧКИ, НАЗВАННЫЙ КАРТОЙ — тот же класс, другой источник.
	err = designRefuseColourMapAlsoAnInput(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps[0].MediaId = 7
	}), inputs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "«mood»")

	// И ЛОСКУТ ТКАНИ ТОЖЕ: карта, оказавшаяся текстурой, уехала бы в вызов дважды под двумя
	// взаимно исключающими подписями.
	err = designRefuseColourMapAlsoAnInput(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps[0].MediaId = 9
	}), inputs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "params.colour.fabrics.0.media_id")

	// ⚠ ПРОГОН БЕЗ КАРТ НЕ ЗАДЕТ ВОВСЕ: сторож обязан молчать там, где карты нет.
	require.NoError(t, designRefuseColourMapAlsoAnInput(
		&pb_common.DesignRunParams{ExtraInputMediaIds: []int32{1, 7}}, inputs))
}
