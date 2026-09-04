package entity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ═══ D-28: ШЕСТЬ СТОРОН СИЛУЭТА, ЧЕТЫРЕ ИЗ НИХ — ОРТОГОНАЛЬНЫЕ ═══════════════════════════════════
//
// Владелец (круг 18): «добавь еще в слоты три четверти право и лево и как возможность для
// генерации». Словарь сторон живёт здесь и проверяется Go, а не CHECK-ом схемы; пробы ниже держат
// ДВА списка и отношение между ними, потому что расходятся они молча: шестая сторона, попавшая в
// сборку 3D, — это локальный отказ Meshy (потолок в четыре картинки) либо отказ fal (нет слота).
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНИТ: убрать три четверти из DesignSilhouetteViews (первая проба);
// добавить их в DesignCardinalViews (вторая); сделать IsDesignCardinalView синонимом
// IsDesignSilhouetteView (вторая же).

func TestSilhouetteHasSixSidesAndThreeQuartersAreAmongThem(t *testing.T) {
	require.Len(t, DesignSilhouetteViews, 6)
	require.Equal(t, []string{
		DesignViewFront, DesignViewBack, DesignViewSideL, DesignViewSideR,
		DesignViewThreeQuarterL, DesignViewThreeQuarterR,
	}, DesignSilhouetteViews, "порядок — это порядок рядов на экране и сортировки плит")

	require.Equal(t, "three_quarter_l", DesignViewThreeQuarterL,
		"ключ — контракт провода, клиент зеркалит его дословно")
	require.Equal(t, "three_quarter_r", DesignViewThreeQuarterR)

	for _, v := range []string{DesignViewThreeQuarterL, DesignViewThreeQuarterR} {
		require.True(t, IsDesignSilhouetteView(v), "%s адресуема по view_key", v)
		require.True(t, IsDesignGhostView(v), "%s — законная догадка о виде загруженного файла", v)
		require.LessOrEqual(t, len(v), 32, "view_key — VARCHAR(32) в 0341 и 0340")
		require.LessOrEqual(t, len(DesignBenchExclusiveKey(v, 2147483647)), 64,
			"колорвейный ключ слота обязан помещаться в exclusive_key VARCHAR(64)")
	}
	require.False(t, IsDesignSilhouetteView(DesignViewDetail), "деталь адресуется своим id, а не видом")
}

func TestCardinalSidesAreTheFourAThreedBuildCanRead(t *testing.T) {
	require.Equal(t, []string{DesignViewFront, DesignViewBack, DesignViewSideL, DesignViewSideR},
		DesignCardinalViews, "ровно четыре: потолок Meshy и четыре именованных слота fal")

	for _, v := range DesignCardinalViews {
		require.True(t, IsDesignSilhouetteView(v), "кардинальная сторона — подмножество силуэта")
		require.True(t, IsDesignCardinalView(v))
	}
	require.False(t, IsDesignCardinalView(DesignViewThreeQuarterL),
		"три четверти — плита верстака и вход рендера, но не вид для поворотного стола")
	require.False(t, IsDesignCardinalView(DesignViewThreeQuarterR))
	require.False(t, IsDesignCardinalView(DesignViewDetail))
	require.False(t, IsDesignCardinalView(""))

	// ОТНОШЕНИЕ, А НЕ ДВА СПИСКА: всякая сторона силуэта, которая не кардинальна, — одна из двух
	// трёх четвертей; появись третья, эта строка заставит решить, куда ей ехать в 3D.
	var extra []string
	for _, v := range DesignSilhouetteViews {
		if !IsDesignCardinalView(v) {
			extra = append(extra, v)
		}
	}
	require.Equal(t, []string{DesignViewThreeQuarterL, DesignViewThreeQuarterR}, extra)
}
