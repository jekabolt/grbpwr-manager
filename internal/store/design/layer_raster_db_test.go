package design_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/content"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ ПИКСЕЛЬНОГО КАНАЛА СЛОЯ (0355, X-1 и X-9).
//
// ПОЧЕМУ ЖИВАЯ БАЗА, А НЕ МОК. Все четыре предмета — свойства ЗАПРОСОВ и СХЕМЫ: ревизия держится
// предикатом `rev = :expected` внутри SERIALIZABLE-транзакции, «молчание оставляет» держится тем,
// что колонка не попадает в SET, принадлежность считается объединением двух таблиц, а реестр
// медиатеки — это ветка UNION поверх настоящего внешнего ключа. Мок ответил бы ровно то, что ему
// велели, и доказал бы только собственную настройку.
//
// Запуск — тот же одноразовый контейнер, что и у соседних проб (см. шапку wave2_db_test.go); без
// CI=1 каждая проба пропускается ДО открытия соединения.

// probeStrokes — минимальный законный JSON штрихов.
func probeStrokes() json.RawMessage { return json.RawMessage(`[{"k":"pen"}]`) }

// probeLayerRaster читает колонку НАПРЯМУЮ, минуя стор: проба про то, что лежит в базе, не имеет
// права спрашивать об этом ту же функцию, которую проверяет.
func probeLayerRaster(t *testing.T, raw *sql.DB, layerID int) sql.NullInt32 {
	t.Helper()
	var got sql.NullInt32
	require.NoError(t, raw.QueryRow(
		`SELECT raster_media_id FROM design_edit_layer WHERE id = ?`, layerID).Scan(&got))
	return got
}

// ─────────────────── X-9: ОДНА РЕВИЗИЯ НА ДВА КАНАЛА ───────────────────

// ПИКСЕЛИ ХОДЯТ ПОД ТЕМ ЖЕ CAS, ЧТО И ШТРИХИ.
//
// ⚠ ЭТО ГЛАВНАЯ ПРОБА ФАЗЫ. Сохранение пикселей поверх чужой ревизии — та же потеря работы, что и
// сохранение штрихов поверх неё: человек, чью роспись переписали, узнал бы об этом по картинке, а
// не по отказу. Проба проверяет ОБА следствия — отказ и НЕИЗМЕННОСТЬ колонки: отказ без второй
// половины совместим с записью, которая всё-таки состоялась.
//
// МУТАЦИЯ, НА КОТОРОЙ ОНА КРАСНЕЕТ: вынести растр во ВТОРОЙ ГЛАГОЛ — свою транзакцию без
// expected_rev. Тогда пиксели устаревшего писателя коммитятся, штрихи отказываются, и колонка
// внизу этой пробы держит ЧУЖОЕ медиа. Замерено.
//
// ⚠ А ВОТ ВТОРОЙ ОПЕРАТОР ВНУТРИ ТОЙ ЖЕ ТРАНЗАКЦИИ ОНА (законно) НЕ ЛОВИТ: промах CAS возвращает
// ошибку, замыкание откатывается, и лишняя запись уходит с ним. Это записано здесь, чтобы никто не
// вывел из зелёной пробы более сильного обещания, чем она даёт.
func TestDesignDBLayerRasterMovesUnderTheSameCAS(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	mine, other := probeMedia(t, raw), probeMedia(t, raw)

	born, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, 1, born.Rev)
	require.False(t, born.RasterMediaId.Valid, "a fresh layer has painted nothing")

	painted, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, LayerId: born.Id, ExpectedRev: 1,
		Strokes: probeStrokes(), RasterMediaId: mine, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, 2, painted.Rev, "one save is one revision, whichever channel moved")
	require.EqualValues(t, mine, painted.RasterMediaId.Int32)

	// УСТАРЕВШИЙ ПИСАТЕЛЬ. Он видел r1, за это время появился r2, и его пиксели — другие.
	stale, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, LayerId: born.Id, ExpectedRev: 1,
		Strokes: probeStrokes(), RasterMediaId: other, Actor: "stale",
	})
	require.Error(t, err)
	require.Nil(t, stale)
	require.ErrorIs(t, err, entity.ErrDesignLayerRevMismatch)

	require.EqualValues(t, mine, probeLayerRaster(t, raw, born.Id).Int32,
		"the refusal must have changed nothing: a stale writer loses BOTH channels or neither")

	after, err := rep.Design().GetEditLayer(ctx, card, born.Id)
	require.NoError(t, err)
	require.Equal(t, 2, after.Rev, "a refused save must not consume a revision")
}

// МОЛЧАНИЕ ПРО РАСТР ОСТАВЛЯЕТ ПИКСЕЛИ НА МЕСТЕ.
//
// ⚠ ИМЕННО ЭТОТ ИСХОД ЛОВИТ ДЕФЕКТ «ЧЕРНОВИК СТИРАЕТ ОТСУТСТВУЮЩИЕ ПОЛЯ», уже оплаченный этим
// репозиторием: незаполненное поле уезжает нулём и читается как команда «очисти». У ссылки на
// медиа цена такой ошибки — вся роспись человека, стёртая автосейвом, тронувшим одни штрихи.
//
// МУТАЦИЯ: писать `raster_media_id` безусловно — красная.
func TestDesignDBLayerSilenceKeepsThePixels(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	born, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, RasterMediaId: media, Actor: "probe",
	})
	require.NoError(t, err)
	require.EqualValues(t, media, born.RasterMediaId.Int32, "a layer may be born painted")

	// САМ СЕЙВ, КОТОРЫЙ ПРО РАСТР МОЛЧИТ: ни id, ни явной очистки.
	quiet, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, LayerId: born.Id, ExpectedRev: 1,
		Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, 2, quiet.Rev)
	require.EqualValues(t, media, quiet.RasterMediaId.Int32,
		"a stroke-only save must not delete the painting")
	require.EqualValues(t, media, probeLayerRaster(t, raw, born.Id).Int32)
}

// ОЧИСТКА СКАЗАНА ВСЛУХ И РАБОТАЕТ.
//
// Контроль на ложную зелень предыдущей пробы: «молчание оставляет» было бы бесполезной half-truth,
// если бы стереть пиксели было нельзя вовсе — колонка держит файл ключом RESTRICT, и слой,
// неспособный её отпустить, держал бы медиа в библиотеке навсегда.
func TestDesignDBLayerClearRasterReleasesTheFile(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	born, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, RasterMediaId: media, Actor: "probe",
	})
	require.NoError(t, err)

	cleared, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, LayerId: born.Id, ExpectedRev: 1,
		Strokes: probeStrokes(), ClearRaster: true, Actor: "probe",
	})
	require.NoError(t, err)
	require.False(t, cleared.RasterMediaId.Valid, "an explicit clear must drop the pixel channel")
	require.False(t, probeLayerRaster(t, raw, born.Id).Valid)

	// ФАЙЛ ДЕЙСТВИТЕЛЬНО ОТПУЩЕН, а не только обнулён в проекции: RESTRICT — единственный
	// беспристрастный свидетель, и до очистки этот же DELETE отказывал (см. пробу ниже).
	_, err = raw.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, media)
	require.NoError(t, err, "a released file must go back to being deletable")
}

// ПРОТИВОРЕЧИЕ ОТКАЗЫВАЕТСЯ И НИЧЕГО НЕ ПИШЕТ.
func TestDesignDBLayerRefusesARasterAndAClearTogether(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	first, second := probeMedia(t, raw), probeMedia(t, raw)

	born, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, RasterMediaId: first, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, LayerId: born.Id, ExpectedRev: 1,
		RasterMediaId: second, ClearRaster: true, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
	require.EqualValues(t, first, probeLayerRaster(t, raw, born.Id).Int32)
}

// ─────────────────── X-1: ГРАНИЦА КАРТОЧКИ ДЛЯ ПИКСЕЛЕЙ ───────────────────

// ЧУЖАЯ КАРТИНКА НЕ СТАНОВИТСЯ ПИКСЕЛЯМИ ЭТОГО СЛОЯ.
//
// Растр отдаётся клиенту ссылкой и рисуется им на холсте, поэтому непроверенное поле означает, что
// слой карточки A показывает и сплющивает картинку карточки B — дословно та беда, которую
// ImportVector закрыл для source_media_id и base_media_id.
//
// МУТАЦИЯ: убрать вызов refuseForeignMedia из SaveEditLayer — красная.
func TestDesignDBLayerRefusesForeignRaster(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine, foreign := probeCard(t, raw), probeCard(t, raw)
	media := probeMedia(t, raw)
	probeBandPicture(t, raw, foreign, media)

	_, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: mine, RasterMediaId: media, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignMedia)

	var rows int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM design_edit_layer WHERE tech_card_id = ?`, mine).Scan(&rows))
	require.Equal(t, 0, rows, "a refused save must not leave a layer behind")
}

// ...А НИЧЕЙНЫЙ СВЕЖЕЗАГРУЖЕННЫЙ ФАЙЛ ПРОХОДИТ, И ЭТО ОБЫЧНЫЙ СЛУЧАЙ.
//
// Контроль на ложную зелень: правило ОТРИЦАТЕЛЬНОЕ («не принадлежит чужой карточке»), и
// положительное («обязано лежать в tech_card_media») отказало бы ровно на законном жесте — растр
// приходит из UploadContentImage и не принадлежит ещё никому.
func TestDesignDBLayerAcceptsAFreshlyUploadedRaster(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)

	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, RasterMediaId: probeMedia(t, raw), Actor: "probe",
	})
	require.NoError(t, err)
	require.True(t, layer.RasterMediaId.Valid)
}

// НЕСУЩЕСТВУЮЩЕЕ МЕДИА — ОТКАЗ, НАЗЫВАЮЩИЙ ПРИСЛАННЫЙ ID, А НЕ СЫРОЙ 1452 С ИМЕНЕМ ОГРАНИЧЕНИЯ.
func TestDesignDBLayerRefusesAnUnknownRaster(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)

	_, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, RasterMediaId: 2147483000, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
}

// ─────────────────── X-1: РЕЕСТР ИСПОЛЬЗОВАНИЯ МЕДИА ───────────────────

// БИБЛИОТЕКА ЗНАЕТ ПРО КОЛОНКУ, КОТОРУЮ ЗАВЕЛА СХЕМА.
//
// ⚠ БЕЗ СТРОКИ В РЕЕСТРЕ `GetMediaUsage` — а значит и `DeleteMediaByIdIfUnused`, который её
// спрашивает, — объявил бы файл свободным, человек нажал бы удалить и получил бы сырой внешний ключ
// с именем таблицы, о которой никогда не слышал. Для растра это чья-то незавершённая роспись.
//
// ⚠ ПОЧЕМУ ЗДЕСЬ ДИФФ СХЕМЫ, А НЕ ВЫЗОВ `GetMediaUsage`. Вызов на ЭТОМ стенде падает 1271
// («Illegal mix of collations for operation UNION») и падает ОДИНАКОВО без этой правки: таблицы
// волны DESIGN создавались с явным `COLLATE=utf8mb4_unicode_ci`, а `tech_card` и `media` унаследовали
// серверный `utf8mb4_0900_ai_ci` контейнера, и UNION мешает их метки. Замерено: запрос из ДВАДЦАТИ
// ОДНОЙ ветки (без растровой) даёт ту же 1271. Проба, которая ждала бы там зелени, доказывала бы
// свойство контейнера, а не реестра, — поэтому она спрашивает ровно то, что зависит от этой правки:
// живой внешний ключ есть, и реестр его называет. Тот же диффер целиком — в
// TestMediaUsageRegistryCoversSchema (internal/store), и он тоже исполняется на этом стенде.
//
// МУТАЦИЯ: убрать запись `del.raster_media_id` из mediaRefRegistry — красная.
func TestDesignDBLayerRasterIsRegisteredAsAHolder(t *testing.T) {
	_, raw := probeRepository(t)
	ctx := context.Background()

	var refs int
	require.NoError(t, raw.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
		  AND COLUMN_NAME = 'raster_media_id' AND REFERENCED_TABLE_NAME = 'media'`).Scan(&refs))
	require.Equal(t, 1, refs, "0355 must have created the owning foreign key")

	var rule string
	require.NoError(t, raw.QueryRowContext(ctx, `
		SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA = DATABASE()
		  AND CONSTRAINT_NAME = 'fk_design_edit_layer_raster_media'`).Scan(&rule))
	require.Equal(t, "RESTRICT", rule,
		"SET NULL would turn a painted layer back into a bare vector one without a word")

	registered := false
	for _, target := range content.MediaRefRegistryTargets() {
		if target == "design_edit_layer.raster_media_id" {
			registered = true
		}
	}
	require.True(t, registered,
		"an unregistered RESTRICT makes the library call somebody's unfinished artwork free")

	// И КОЛОНКА ОБЯЗАНА БЫТЬ ВЕДУЩЕЙ В КАКОМ-НИБУДЬ ИНДЕКСЕ: реестр фильтрует по ней на каждой
	// странице медиатеки, и без индекса это полное сканирование таблицы слоёв.
	var idx int
	require.NoError(t, raw.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
		  AND COLUMN_NAME = 'raster_media_id' AND SEQ_IN_INDEX = 1`).Scan(&idx))
	require.Greater(t, idx, 0, "the registry filters on this column on every library page")
}

// ...И УДАЛЕНИЕ ТАКОГО ФАЙЛА ОТКАЗЫВАЕТСЯ, А НЕ МОЛЧА СНОСИТ РОСПИСЬ. Род ключа — RESTRICT — это и
// есть сообщение «файл держит незавершённая правка», и проба исполняет его, а не выводит из схемы.
func TestDesignDBLayerRasterCannotBeDeletedFromUnderTheLayer(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	_, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, RasterMediaId: media, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = raw.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, media)
	require.Error(t, err, "RESTRICT must refuse: the file is held by an unfinished edit")
}

// ─────────────────── X-9: СПЛЮЩИВАНИЕ НЕСЁТ РАСТР ───────────────────

// СЛОЙ, У КОТОРОГО ЕСТЬ ТОЛЬКО ПИКСЕЛИ, СПЛЮЩИВАЕТСЯ.
//
// ⚠ ЭТО ЖИВОЙ ДЕФЕКТ ДО ЭТОЙ ПРАВКИ. Гейт флэттена звучал «нет штрихов — сплющивать нечего», и с
// появлением пиксельного канала то же условие стало ложью ровно про тот случай, ради которого круг
// 6 и затевался: закрасил кистью, прогрыз ластиком дырку, пера не касался. Сплющивание —
// единственная дверь, через которую правка попадает в полосу как картинка, поэтому отказ здесь
// закрывал бы весь растровый редактор.
//
// МУТАЦИЯ (компилируется): вернуть designLayerIsEmpty к чтению одних штрихов — красная.
func TestDesignDBFlattenAcceptsALayerPaintedWithoutStrokes(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)

	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, RasterMediaId: probeMedia(t, raw), Actor: "probe",
	})
	require.NoError(t, err)
	require.Empty(t, layer.Strokes, "this layer was never touched with the pen")

	pic, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: card, LayerId: layer.Id, ExpectedRev: layer.Rev,
		MediaId: probeMedia(t, raw), Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, layer.Rev, pic.LayerRev,
		"the flatten records the revision it materialised")
}

// ...А СЛОЙ, У КОТОРОГО НЕТ НИ ОДНОГО КАНАЛА, ПО-ПРЕЖНЕМУ ОТКАЗЫВАЕТСЯ.
//
// Контроль на ложную зелень предыдущей пробы: гейт обязан ослабнуть ровно на пиксели, а не исчезнуть.
func TestDesignDBFlattenStillRefusesAnEmptyLayer(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)

	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: card, LayerId: layer.Id, ExpectedRev: layer.Rev,
		MediaId: probeMedia(t, raw), Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignEmptyLayer)
}

// ПОЛОСА ОТДАЁТ ПИКСЕЛЬНЫЙ КАНАЛ В СПИСКЕ СЛОЁВ.
//
// Проекция полосы ИМЕНОВАННАЯ, поэтому пропуск колонки не падает и ничего не логирует: полоса
// просто сервирует каждый закрашенный слой как пустой. Штрихи в списке по-прежнему не едут.
func TestDesignDBBandServesTheLayerPixelChannel(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, Strokes: probeStrokes(), RasterMediaId: media, Actor: "probe",
	})
	require.NoError(t, err)

	band, err := rep.Design().GetBand(ctx, card, 12)
	require.NoError(t, err)
	var seen bool
	for _, l := range band.Layers {
		if l.Id != layer.Id {
			continue
		}
		seen = true
		require.EqualValues(t, media, l.RasterMediaId.Int32,
			"the band must be able to tell a painted canvas from an empty one")
		require.Empty(t, l.Strokes, "strokes stay out of the list")
	}
	require.True(t, seen, "the card's layer must be in its own band")
}
