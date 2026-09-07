package design_test

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ЖИВАЯ ПРОБА ВОПРОСА ДЕНЕЖНОЙ ДВЕРИ: «КАКИЕ ИЗ ЭТИХ МЕДИА ЛЕЖАТ ТОЛЬКО СПРЯТАННЫМИ КАДРАМИ».
//
// ЗАЧЕМ ОНА В БАЗЕ, А НЕ НА МОКАХ. Дверь (apisrv/admin, designRefuseHiddenInputs) измерена своими
// пробами: она спрашивает все источники входа и отказывает до резерва. Но ЧТО ИМЕННО значит её
// вопрос, решает ЭТОТ запрос, и решает единственным несущим выбором — предикатом «ВСЕ держатели
// спрятаны» против «хоть один спрятан». Мок отвечает списком, который написал автор пробы, и
// потому не может ошибиться; ошибиться может только SQL.
//
// ⚠ ПОЧЕМУ «ВСЕ», А НЕ «ХОТЬ ОДИН». Флаг стоит на КАДРЕ, а не на файле (в отличие от
// `display_only`, объявленного утверждением о файле), и один media законно лежит несколькими
// кадрами — на своей карточке и на чужой, оригиналом и после разреза. Спрошенный «хоть один», этот
// вопрос отказывал бы оплаченному прогону карточки A за то, что кто-то спрятал ту же картинку на
// карточке B, — то есть был бы сторожем, рубящим законное, а такой сторож хуже дыры.
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*): без CI=1 проба
// пропускается ДО открытия соединения.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНЯТ (и каждая — своя строка):
//   - `HAVING SUM(hidden_at IS NULL) = 0` → `HAVING SUM(hidden_at IS NOT NULL) > 0` («хоть один»)
//     — краснеет медиа с двумя держателями и медиа с чужим видимым кадром;
//   - убрать GROUP BY/HAVING вовсе (вернуть все названные) — краснеет видимое медиа;
//   - отвечать про медиа, которого не держит ни один кадр, — краснеет ничейное.
func TestDesignDBMediaHeldHiddenOnlyMeansEveryHolderIsHidden(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	other := probeCard(t, raw)

	run := outputsProbeRun(t, raw, card, entity.DesignRunKindFreeform, 1, 0)
	otherRun := outputsProbeRun(t, raw, other, entity.DesignRunKindFreeform, 1, 0)

	// ─── ЧЕТЫРЕ СОСТОЯНИЯ ОДНОГО ВОПРОСА ───
	// allHidden — единственный держатель, и он спрятан: отвергнутый кадр.
	allHidden := probeMedia(t, raw)
	outputsProbePicture(t, raw, card, run, allHidden, 0, entity.DesignPictureKindFreeform, 0, 0, true)

	// bothHidden — ДВА держателя, оба спрятаны: файл на полосе больше нигде не показывается.
	bothHidden := probeMedia(t, raw)
	outputsProbePicture(t, raw, card, run, bothHidden, 1, entity.DesignPictureKindFreeform, 0, 0, true)
	outputsProbePicture(t, raw, card, run, bothHidden, 2, entity.DesignPictureKindFreeform, 0, 0, true)

	// mixed — спрятан на этой карточке, ВИДЕН на ней же вторым кадром: человек отверг не файл, а
	// одну его строку. Это и есть случай, ради которого предикат «все», а не «хоть один».
	mixed := probeMedia(t, raw)
	outputsProbePicture(t, raw, card, run, mixed, 3, entity.DesignPictureKindFreeform, 0, 0, true)
	outputsProbePicture(t, raw, card, run, mixed, 4, entity.DesignPictureKindFreeform, 0, 0, false)

	// foreign — спрятан здесь, ВИДЕН на ЧУЖОЙ карточке. Отказ по нему был бы отказом прогону
	// карточки A за решение, принятое на карточке B.
	foreign := probeMedia(t, raw)
	outputsProbePicture(t, raw, card, run, foreign, 5, entity.DesignPictureKindFreeform, 0, 0, true)
	outputsProbePicture(t, raw, other, otherRun, foreign, 0, entity.DesignPictureKindFreeform, 0, 0, false)

	// visible — обычный кадр, ничего не спрятано.
	visible := probeMedia(t, raw)
	outputsProbePicture(t, raw, card, run, visible, 6, entity.DesignPictureKindFreeform, 0, 0, false)

	// ownerless — файл, которого не держит НИ ОДИН кадр: только что загруженный. Граница карточки
	// пропускает такое намеренно (AssertMediaNotForeign), и эта дверь обязана вести себя так же.
	ownerless := probeMedia(t, raw)

	held, err := rep.Design().MediaHeldHiddenOnly(ctx,
		[]int{allHidden, bothHidden, mixed, foreign, visible, ownerless, 0, allHidden})
	require.NoError(t, err)
	require.ElementsMatch(t, []int{allHidden, bothHidden}, held,
		"отвечают ровно те медиа, у которых СПРЯТАНЫ ВСЕ держатели; ноль и повтор во входе "+
			"отсеиваются здесь же, чтобы вызывающему не помнить об этом на каждом источнике")

	// ПУСТОЙ ВХОД — ПУСТОЙ ОТВЕТ, БЕЗ ОБРАЩЕНИЯ К БАЗЕ.
	empty, err := rep.Design().MediaHeldHiddenOnly(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
