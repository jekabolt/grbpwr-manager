package entity

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// ПРОБЫ СЛОВАРЯ ОСИ КОЛОРВЕЯ (0356, L-2/L-3) — свойства, на которых держится схема.

// КЛЮЧ ЭКСКЛЮЗИВНОСТИ: colorway 0 возвращает ГОЛЫЙ ВИД — байт в байт легаси-адрес, поэтому каждый
// слот, рождённый до оси, остаётся достижим тем же ключом, каким рождался. Именованные колорвеи
// дробят домен: front колорвея A и front колорвея B — разные ключи, то есть UNIQUE
// (tech_card_id, kind, exclusive_key) держит их ОДНОВРЕМЕННО — ровно требование L-2.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ ПЕРВАЯ ПОЛОВИНА: вернуть суффикс и при colorwayID == 0 («для
// единообразия») — каждый существующий слот беты стал бы недостижим своим адресом, и первая же
// постановка родила бы ВТОРОЙ слот того же вида.
func TestDesignBenchExclusiveKeyProperties(t *testing.T) {
	require.Equal(t, DesignViewFront, DesignBenchExclusiveKey(DesignViewFront, 0),
		"легаси-адрес обязан остаться байт в байт")
	require.Equal(t, DesignViewFront, DesignBenchExclusiveKey(DesignViewFront, -1),
		"отрицательное читается как «не назван», а не рождает мусорный ключ")

	a := DesignBenchExclusiveKey(DesignViewFront, 5)
	b := DesignBenchExclusiveKey(DesignViewFront, 6)
	require.NotEqual(t, a, b, "два колорвея держат один вид ОДНОВРЕМЕННО — ключи обязаны различаться")
	require.NotEqual(t, DesignViewFront, a, "колорвейный ключ не коллидирует с легаси-адресом")
	require.Equal(t, a, DesignBenchExclusiveKey(DesignViewFront, 5), "адрес стабилен")

	// Домены не пересекаются ни с одним легаси-ключом: четыре стороны и детальный префикс.
	for _, v := range DesignSilhouetteViews {
		require.NotContains(t, v, "@cw:", "разделитель не встречается в видах")
	}

	// VARCHAR(64): самый длинный вид + максимальный int32 обязаны помещаться.
	long := DesignBenchExclusiveKey(DesignViewSideL, 2147483647)
	require.LessOrEqual(t, len(long), 64, "ключ обязан помещаться в колонку exclusive_key")
}

// ОСЬ ЕСТЬ НЕ У ВСЯКОГО РОДА. Флэт — одна разметка на карточку (L-4), паттерн — плитка ткани;
// обоим колорвей ОТКАЗЫВАЮТ, а не молча обнуляют. Рендер и 3D-кадр — изображения изделия в цвете,
// их колорвей и есть предмет L-2.
//
// МУТАЦИЯ: включить flat в DesignPictureKindTakesColorway — «флэт колорвея 5» стал бы выразим, и
// граница L-4 (флэтовый верстак один на карточку) умерла бы без единого красного теста.
func TestColorwayAxisVocabulary(t *testing.T) {
	require.False(t, DesignPictureKindTakesColorway(DesignPictureKindFlat))
	require.False(t, DesignPictureKindTakesColorway(""), "пустое читается как flat одним правилом")
	require.False(t, DesignPictureKindTakesColorway(DesignPictureKindPattern))
	require.True(t, DesignPictureKindTakesColorway(DesignPictureKindRender))
	require.True(t, DesignPictureKindTakesColorway(DesignPictureKindThreed))

	require.True(t, DesignRunKindTakesColorway(DesignRunKindRender))
	require.True(t, DesignRunKindTakesColorway(DesignRunKindThreed))
	require.True(t, DesignRunKindTakesColorway(DesignRunKindRecolor),
		"перекрас рождает рендер — его колорвей осмыслен")
	for _, k := range []string{DesignRunKindFlat, DesignRunKindVector, DesignRunKindPattern, DesignRunKindDraftIdea} {
		require.Falsef(t, DesignRunKindTakesColorway(k), "род %s оси колорвея не имеет", k)
	}
}

// NULL колонки читается нулём одним правилом на всех ярусах — парная половина DesignKindOrFlat.
func TestDesignColorwayOrNone(t *testing.T) {
	require.Zero(t, DesignColorwayOrNone(sql.NullInt32{}))
	require.Equal(t, 5, DesignColorwayOrNone(sql.NullInt32{Int32: 5, Valid: true}))
}
