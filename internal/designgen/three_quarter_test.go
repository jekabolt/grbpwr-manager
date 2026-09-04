package designgen

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/stretchr/testify/require"
)

// ═══ D-28, ВОРКЕР: ШЕСТЬ СТОРОН НА ВЕРСТАКЕ, ЧЕТЫРЕ В СБОРКЕ 3D, ВСЕ ШЕСТЬ В СЛОВАХ ═════════════
//
// Три четверти слева и справа — пятая и шестая стороны силуэта. Для флэта и рендера это ещё два
// чекбокса и ещё две плиты, для 3D — ловушка: Meshy принимает 1..4 картинки одного предмета
// (meshy.MaxImages), у fal четыре ИМЕНОВАННЫХ слота (front/back/left/right). Отбор плит 3D поэтому
// идёт по entity.IsDesignCardinalView, а не по «стороне силуэта».
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ ПЕРВАЯ ПРОБА: вернуть в threedPictures `IsDesignSilhouetteView` — в
// сборку уедут шесть адресов, и провайдер откажет локально на каждой карточке, где человек
// поставил хотя бы одну три-четверти-плиту. Ровно тот же класс дефекта, что V-14 у референсов.

// threedRunSixSides — прогон 3D на верстаке, где заняты ВСЕ шесть сторон рендера.
func threedRunSixSides() entity.DesignRun {
	r := testRun(1, entity.DesignRunKindThreed)
	r.Params = entity.RawJSON(`{
	  "views": ["front","back","side_l","side_r","three_quarter_l","three_quarter_r"]
	}`)
	r.Inputs = entity.RawJSON(`{
	  "slots": [
	    {"view_key": "three_quarter_r", "media_id": 6},
	    {"view_key": "side_r",          "media_id": 4},
	    {"view_key": "three_quarter_l", "media_id": 5},
	    {"view_key": "back",            "media_id": 2},
	    {"view_key": "front",           "media_id": 1},
	    {"view_key": "side_l",          "media_id": 3}
	  ]
	}`)
	return r
}

func TestThreedDropsTheThreeQuarterPlatesAndKeepsTheCardinalFour(t *testing.T) {
	job, err := buildJob(context.Background(), media(1, 2, 3, 4, 5, 6), threedRunSixSides(), "medium")
	require.NoError(t, err)

	require.Equal(t, []string{
		"https://cdn.example/m/1.png",
		"https://cdn.example/m/2.png",
		"https://cdn.example/m/3.png",
		"https://cdn.example/m/4.png",
	}, job.References, "front первым, затем back, side_l, side_r; три четверти столу не вид")
	require.Equal(t, []string{
		entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL, entity.DesignViewSideR,
	}, job.ReferenceViews, "и именной маршрут получает ровно те четыре стороны, для которых у него есть слоты")
	for _, u := range job.References {
		require.NotContains(t, u, "/5.", "три четверти слева уехали в сборку 3D")
		require.NotContains(t, u, "/6.", "три четверти справа уехали в сборку 3D")
	}
	require.LessOrEqual(t, len(job.References), meshy.MaxImages,
		"meshy.Submit отказывает локально выше этого числа — прогон не начнётся вовсе")
}

// TestAThreeQuarterFrontlessBenchStillHasNoFront — «без переда ничего» не ослабло от новых
// сторон: две три-четверти-плиты не заменяют перёд.
func TestAThreeQuarterFrontlessBenchStillHasNoFront(t *testing.T) {
	r := threedRunSixSides()
	r.Inputs = entity.RawJSON(`{
	  "slots": [
	    {"view_key": "three_quarter_l", "media_id": 5},
	    {"view_key": "three_quarter_r", "media_id": 6}
	  ]
	}`)
	job, err := buildJob(context.Background(), media(5, 6), r, "medium")
	require.NoError(t, err)
	require.Empty(t, job.References, "прогон без переда — прогон, которому нечего поставить лицом")
}

// TestFalRouteHasNoSlotForAThreeQuarterPlate — ПОЯС именного маршрута: даже если отбор выше
// пропустит три четверти, fal-сборка откажет, а не подставит её в чужой слот.
func TestFalRouteHasNoSlotForAThreeQuarterPlate(t *testing.T) {
	_, err := falViews(Job{
		References:     []string{"https://cdn/front.png", "https://cdn/tq.png"},
		ReferenceViews: []string{entity.DesignViewFront, entity.DesignViewThreeQuarterL},
	})
	require.ErrorIs(t, err, fal.ErrNoFrontView)
	require.Contains(t, err.Error(), entity.DesignViewThreeQuarterL,
		"отказ называет сторону, которой негде лежать")
}

// TestThreeQuarterViewsAreSpelledOnEveryRoute — у новых сторон есть СЛОВА на каждом маршруте, а не
// сырой ключ: подпись референса (captionView), инструкция раскладки (displayView) и место в
// порядке плит (viewRank) — после четырёх кардинальных, до деталей.
func TestThreeQuarterViewsAreSpelledOnEveryRoute(t *testing.T) {
	require.Equal(t, "three-quarter view from the left", captionView(entity.DesignViewThreeQuarterL))
	require.Equal(t, "three-quarter view from the right", captionView(entity.DesignViewThreeQuarterR))
	require.Equal(t, "THREE-QUARTER LEFT", displayView(entity.DesignViewThreeQuarterL))
	require.Equal(t, "THREE-QUARTER RIGHT", displayView(entity.DesignViewThreeQuarterR))

	require.Greater(t, viewRank(entity.DesignViewThreeQuarterL), viewRank(entity.DesignViewSideR),
		"три четверти сортируются ПОСЛЕ четырёх кардинальных: на маршруте 3D порядок — смысл")
	require.Less(t, viewRank(entity.DesignViewThreeQuarterL), viewRank(entity.DesignViewThreeQuarterR))
	require.Less(t, viewRank(entity.DesignViewThreeQuarterR), viewRank(entity.DesignViewDetail),
		"…и ДО деталей: сторона силуэта раньше крупного плана")
	require.Less(t, viewRank(entity.DesignViewDetail), viewRank("nonsense"),
		"неназванное по-прежнему последнее")
}

// TestFlatSheetNamesThreeQuarterViewsInParamsOrder — раскладка листа называет новые стороны
// словами и в порядке params, как и остальные (см. TestFlatCompositeNamesTheChosenViewsInParamsOrder).
func TestFlatSheetNamesThreeQuarterViewsInParamsOrder(t *testing.T) {
	got := flatPrompt(t, `{"views":["three_quarter_r","front","three_quarter_l"],"layout":"one"}`, oneRef)
	require.Contains(t, got,
		"Layout: three views on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced — left to right: THREE-QUARTER RIGHT, FRONT, THREE-QUARTER LEFT.")
	require.NotContains(t, got, "three_quarter", "в промпт не должен просочиться сырой ключ")
}

// TestSixSidesOnOneSheetAreCountedInWords — countWord знает шесть и семь: полный силуэт на одном
// листе (шесть сторон) и лист с деталью (семь кадров) не должны падать в цифры посреди фразы.
func TestSixSidesOnOneSheetAreCountedInWords(t *testing.T) {
	got := flatPrompt(t,
		`{"views":["front","back","side_l","side_r","three_quarter_l","three_quarter_r"],"layout":"one"}`, oneRef)
	require.Contains(t, got, "Layout: six views on one horizontal canvas")
	require.Contains(t, got,
		"left to right: FRONT, BACK, SIDE LEFT, SIDE RIGHT, THREE-QUARTER LEFT, THREE-QUARTER RIGHT.")
	require.Equal(t, "seven", countWord(7))
}
