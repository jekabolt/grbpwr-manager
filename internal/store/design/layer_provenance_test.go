package design

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ПРОВЕНАНС ФЛЭТТЕНА ЧИТАЕТ origin СЛОЯ, А НЕ ТОЛЬКО НАЛИЧИЕ БАЗЫ.
//
// ЧТО БЫЛО. Решение принималось единственным признаком — есть базовая картинка или нет. Значит
// слой, чей вектор построила МОДЕЛЬ (origin=vectorised, платная перерисовка), и слой, чей вектор
// принесён ЧУЖИМ ФАЙЛОМ (origin=imported), оба записывались «нарисован от руки». ИИ-происхождение
// отмывалось в человеческое молча и навсегда: строка design_picture замерзает.
//
// ПОЧЕМУ ЭТО НЕ КОСМЕТИКА. Предупреждение о смеси провенансов считается ровно по
// design_picture.source_class (runInputsAreMixed → designMixedInput). Кадр, у которого написано
// `drawn` вместо `ai`, делает смесь «ИИ + рука» НЕОТЛИЧИМОЙ от чистой руки — то есть согласие
// человека на смесь перестаёт требоваться там, где оно и заведено.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ КАЖДЫЙ СЛУЧАЙ: вернуть прежнее правило (hasParent ? ai_edits : drawn) —
// два верхних случая краснеют.
func TestFlattenSourceClassReadsTheLayerOrigin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		origin    string
		hasParent bool
		want      string
	}{
		{"machine vector on a blank sheet is AI, never a drawing",
			entity.DesignLayerOriginVectorised, false, entity.DesignSourceAI},
		{"an imported foreign file stays imported even over our own raster",
			entity.DesignLayerOriginImported, true, entity.DesignSourceImportedSVG},
		{"an imported foreign file on a blank sheet is imported too",
			entity.DesignLayerOriginImported, false, entity.DesignSourceImportedSVG},
		{"machine vector over a picture is an AI edit of that picture",
			entity.DesignLayerOriginVectorised, true, entity.DesignSourceAIEdits},
		{"a hand drawing over a picture is an edit",
			entity.DesignLayerOriginDrawn, true, entity.DesignSourceAIEdits},
		{"a hand drawing on a blank sheet is a drawing",
			entity.DesignLayerOriginDrawn, false, entity.DesignSourceDrawn},
		{"an empty origin column reads as drawn, exactly as DEFAULT 'drawn' in 0350",
			"", false, entity.DesignSourceDrawn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, designFlattenSourceClass(tc.origin, tc.hasParent))
		})
	}
}

// entity.DesignSourceImportedSVG ПОЛУЧАЕТ ПИСАТЕЛЯ. До этой правки значение стояло в словаре
// провода и не записывалось НИ ОДНОЙ строкой кода — то есть клиент рисовал ярлык, который сервер
// не мог произвести.
func TestImportedSVGProvenanceHasAWriter(t *testing.T) {
	require.Equal(t, entity.DesignSourceImportedSVG,
		designFlattenSourceClass(entity.DesignLayerOriginImported, true))
}
