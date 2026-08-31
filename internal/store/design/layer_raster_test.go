package design

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ ПИКСЕЛЬНОГО КАНАЛА СЛОЯ (0355, X-1/X-9), НЕ ТРЕБУЮЩИЕ БАЗЫ.
//
// Живая половина — резерв, гонка ревизий, граница карточки, реестр медиатеки — лежит в
// layer_raster_db_test.go и ходит в одноразовый контейнер. Здесь то, что живёт в ФОРМЕ кода:
// какой оператор пишется, что значит «слой пуст» и что означает молчание про растр.

// ─────────────────────── X-9: ОДНА РЕВИЗИЯ НА ДВА КАНАЛА ───────────────────────

// TestLayerSaveWritesBothChannelsUnderOneCAS — ЦИТАТА половины X-9.
//
// Пиксели и штрихи — два канала ОДНОГО слоя; сохранение пикселей поверх чужой ревизии это та же
// потеря работы, что и сохранение штрихов. Форма, которая это обеспечивает, ровно одна: присвоение
// растра стоит В ТОМ ЖЕ операторе, что и предикат `rev = :expected`.
//
// МУТАЦИИ, КОТОРЫЕ ЭТА ПРОБА ЛОВИТ:
//   - снять предикат `rev = :expected` с оператора, который пишет растр;
//   - писать `raster_media_id` БЕЗУСЛОВНО: тогда сохранение, тронувшее одни штрихи, стирало бы
//     роспись — исход, ради предотвращения которого на этой строке и заведён CAS.
//
// ⚠ ЧЕГО ОНА НЕ ЛОВИТ, И ЭТО ЗАМЕРЕНО, А НЕ ПРЕДПОЛОЖЕНО: растр, вынесенный во ВТОРОЙ ОПЕРАТОР
// внутри той же транзакции. Такой мутант зелёный ЗАКОННО — промах CAS возвращает ошибку, замыкание
// откатывается, и лишняя запись уходит с ним. Настоящую опасность (растр во втором ГЛАГОЛЕ, со
// своей транзакцией и без expected_rev) ловит живая проба
// TestDesignDBLayerRasterMovesUnderTheSameCAS, и на ней этот мутант краснеет.
func TestLayerSaveWritesBothChannelsUnderOneCAS(t *testing.T) {
	stated := layerSaveUpdate(true)
	up := strings.ToUpper(stated)

	require.Equal(t, 1, strings.Count(up, "UPDATE DESIGN_EDIT_LAYER"),
		"both channels of one layer must move in ONE statement")
	require.Equal(t, 1, strings.Count(up, "WHERE"),
		"one statement, one predicate: a second WHERE would mean a second, unguarded write")
	require.Contains(t, stated, "raster_media_id = :raster",
		"the pixel channel must be assigned by this statement")
	require.Contains(t, stated, "rev = :expected",
		"the statement that writes the pixels must be the one the CAS guards")
	require.Contains(t, stated, "strokes = :strokes")
	require.Contains(t, stated, "rev = rev + 1",
		"one save is one revision, whichever channel moved")

	// ...и присвоение растра стоит ВНУТРИ этого оператора, до его собственного предиката.
	require.Less(t, strings.Index(stated, "raster_media_id = :raster"),
		strings.Index(stated, "WHERE id = :id AND rev = :expected"))

	// МОЛЧАНИЕ НЕ ПИШЕТ КОЛОНКУ ВОВСЕ — так реализовано «не сказано значит оставить».
	silent := layerSaveUpdate(false)
	require.NotContains(t, silent, "raster_media_id",
		"a save that says nothing about the raster must not touch the column at all")
	require.Contains(t, silent, "rev = :expected",
		"silence about the pixels does not weaken the compare-and-set on the strokes")

	// ЛОВУШКА ДВОЕТОЧИЯ: sqlx сканирует имена параметров, не пропуская ни комментариев, ни
	// литералов, поэтому лишнее ':' связалось бы пустым именем и уронило бы весь сейв в рантайме.
	for name, q := range map[string]string{"stated": stated, "silent": silent} {
		require.NotContains(t, q, "--", "%s: SQL comments do not belong in a named query", name)
		expanded, args, err := storeutil.MakeQuery(q, map[string]any{
			"strokes": nil, "raster": nil, "who": "probe", "id": 1, "expected": 2,
		})
		require.NoError(t, err, "%s: named-parameter expansion must succeed", name)
		require.Equal(t, strings.Count(expanded, "?"), len(args), "%s", name)
	}
}

// TestSaveEditLayerRefusesARasterAndAClearTogether — противоречие, а не задача о приоритете.
//
// Запрос, который И называет медиа, И просит очистить растр, описывает два несовместимых
// намерения. Выбрать победителя значило бы молча не прочесть одну половину запроса — и проигравшей
// оказалась бы та половина, которую редактор считал отправленной.
//
// МУТАЦИЯ: убрать проверку. Тогда вызов доходит до транзакции — проба это ловит обоими способами
// (ошибки нет И замыкание достигнуто), не открывая ни одного соединения.
func TestSaveEditLayerRefusesARasterAndAClearTogether(t *testing.T) {
	reached := false
	s := &Store{txFunc: func(context.Context, func(context.Context, dependency.Repository) error) error {
		reached = true
		return nil
	}}
	_, err := s.SaveEditLayer(context.Background(), entity.DesignEditLayerSave{
		TechCardId: 1, LayerId: 7, ExpectedRev: 3, RasterMediaId: 42, ClearRaster: true,
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
	require.False(t, reached,
		"a contradictory save must be refused before a transaction is opened")
}

// TestSaveEditLayerLetsEachSideOfTheRasterThrough — контроль на ложную зелень предыдущей пробы:
// отказ обязан быть про ПРОТИВОРЕЧИЕ, а не про сам факт упоминания растра.
func TestSaveEditLayerLetsEachSideOfTheRasterThrough(t *testing.T) {
	for _, c := range []struct {
		name string
		req  entity.DesignEditLayerSave
	}{
		{"a raster alone", entity.DesignEditLayerSave{TechCardId: 1, LayerId: 7, RasterMediaId: 42}},
		{"a clear alone", entity.DesignEditLayerSave{TechCardId: 1, LayerId: 7, ClearRaster: true}},
		{"silence", entity.DesignEditLayerSave{TechCardId: 1, LayerId: 7}},
	} {
		t.Run(c.name, func(t *testing.T) {
			reached := false
			s := &Store{txFunc: func(context.Context, func(context.Context, dependency.Repository) error) error {
				reached = true
				return nil
			}}
			_, err := s.SaveEditLayer(context.Background(), c.req)
			require.NoError(t, err)
			require.True(t, reached, "a legal save must reach the transaction")
		})
	}
}

// ─────────────────────── X-9: «ПУСТОЙ СЛОЙ» СТАЛ ВОПРОСОМ ПРО ДВА КАНАЛА ───────────────────────

// TestFlattenEmptinessCountsPixelsAsWork — ПРОБА ДЕФЕКТА, КОТОРЫЙ БЫЛ ЖИВЫМ ДО ЭТОЙ ПРАВКИ.
//
// Гейт флэттена звучал «нет штрихов — сплющивать нечего», и до пиксельного канала это было верно
// тождественно: штрихи были единственным каналом слоя. С появлением растра ТО ЖЕ условие стало
// ложью ровно про тот случай, ради которого круг 6 и затевался: человек взял кисть, закрасил, стёр
// ластиком дырку в фотографии, пера не касался — и получил бы FailedPrecondition «у слоя нет
// штрихов» на законченной работе. Отказ не косметический: сплющивание — единственная дверь, через
// которую правка попадает в полосу как картинка.
//
// МУТАЦИЯ, ВОСПРОИЗВОДЯЩАЯ ПРЕЖНЕЕ ПРАВИЛО ДОСЛОВНО (компилируется):
//
//	func designLayerIsEmpty(l entity.DesignEditLayer) bool {
//	    s := string(l.Strokes)
//	    return len(l.Strokes) == 0 || s == "null" || s == "[]"
//	}
//
// краснеют три случая «одни пиксели».
func TestFlattenEmptinessCountsPixelsAsWork(t *testing.T) {
	raster := func(id int32) sql.NullInt32 { return sql.NullInt32{Int32: id, Valid: true} }

	for _, c := range []struct {
		name  string
		layer entity.DesignEditLayer
		empty bool
	}{
		{"nothing at all", entity.DesignEditLayer{}, true},
		{"the JSON column reads back as the literal null", entity.DesignEditLayer{
			Strokes: entity.RawJSON("null")}, true},
		{"an empty stroke array is empty too", entity.DesignEditLayer{
			Strokes: entity.RawJSON("[]")}, true},
		{"strokes and no pixels flatten, exactly as before 0355", entity.DesignEditLayer{
			Strokes: entity.RawJSON(`[{"k":"pen"}]`)}, false},

		// ⚠ ТРИ СЛУЧАЯ, РАДИ КОТОРЫХ ПРОБА СУЩЕСТВУЕТ.
		{"pixels and no strokes at all", entity.DesignEditLayer{
			RasterMediaId: raster(91)}, false},
		{"pixels while the strokes column reads null", entity.DesignEditLayer{
			Strokes: entity.RawJSON("null"), RasterMediaId: raster(91)}, false},
		{"pixels while the stroke array is empty", entity.DesignEditLayer{
			Strokes: entity.RawJSON("[]"), RasterMediaId: raster(91)}, false},

		{"both channels", entity.DesignEditLayer{
			Strokes: entity.RawJSON(`[{"k":"pen"}]`), RasterMediaId: raster(91)}, false},

		// НОЛЬ И NULL В КОЛОНКЕ — ЭТО «НЕ ЗАКРАШИВАЛИ», а не «закрасили картинкой номер ноль».
		{"an invalid raster column is not a painting", entity.DesignEditLayer{
			RasterMediaId: sql.NullInt32{Int32: 91, Valid: false}}, true},
		{"a zero raster id is not a painting", entity.DesignEditLayer{
			RasterMediaId: raster(0)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.empty, designLayerIsEmpty(c.layer))
		})
	}
}

// ─────────────────────── ПОЛОСА ВИДИТ, ЧТО СЛОЙ ЗАКРАШЕН ───────────────────────

// TestBandLayerProjectionNamesThePixelChannel — именованная проекция, поэтому каждая новая колонка
// попадает в неё РУКАМИ, а пропуск не падает и ничего не логирует: полоса просто сервирует поле
// нулём. Для растра это значит «закрашенный слой неотличим от пустого до открытия редактора».
//
// МУТАЦИЯ: убрать `raster_media_id` из designListLayers.
func TestBandLayerProjectionNamesThePixelChannel(t *testing.T) {
	require.Contains(t, designListLayers, "raster_media_id",
		"a band that omits the pixel channel serves every painted layer as unpainted")
	// ...вместе со всем, что уже обязано там быть — проба не должна разрешать обмен одной потери
	// на другую.
	for _, col := range []string{"id", "tech_card_id", "base_media_id", "rev", "origin",
		"source_media_id", "source_picture_id", "updated_by", "updated_at"} {
		require.Contains(t, designListLayers, col)
	}
	require.NotContains(t, designListLayers, "strokes",
		"strokes stay out of the list: 512 KB per layer, several layers per card")
	require.Contains(t, designListLayers, "tech_card_id = :card",
		"the projection must stay scoped to the card")
}
