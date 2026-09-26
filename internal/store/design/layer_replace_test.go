package design

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ «ПЕРЕЗАПИСИ» ПРАВКОЙ (0368, O-53), НЕ ТРЕБУЮЩИЕ БАЗЫ.
//
// Живая половина — переезд слота, штамп, отказы, повтор, цепочка, чтение полосы — лежит в
// replace_db_test.go и ходит в одноразовый контейнер (CI=1). Решение сторожей и их порядок
// проверены без базы в entity (design_replace_test.go). Здесь — то, что живёт в ФОРМЕ кода: два
// оператора, которые держат факт, и отказ, который обязан прозвучать до транзакции.

// ШТАМП ПИШЕТСЯ ОДИН РАЗ И ТОЛЬКО ПОВЕРХ ПУСТОТЫ.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять `replaced_by IS NULL` с WHERE. Тогда вторая перезапись того же
// оригинала, проскочившая чтение (или любой будущий писатель без чтения), молча переписала бы
// указатель на свою правку — и первая правка, уже стоящая в слоте, потеряла бы место в цепочке, не
// потеряв слота.
func TestReplacedByStampIsWrittenOnceOverNothing(t *testing.T) {
	up := strings.ToUpper(designStampReplacedBy)
	require.Equal(t, 1, strings.Count(up, "UPDATE DESIGN_PICTURE"))
	require.Equal(t, 1, strings.Count(up, "WHERE"), "один оператор, один предикат")
	where := designStampReplacedBy[strings.Index(up, "WHERE"):]
	require.Contains(t, where, "id = :id")
	require.Contains(t, where, "replaced_by IS NULL",
		"указатель замены пишется только поверх NULL — второй пояс к чтению в транзакции")
	require.Contains(t, designStampReplacedBy, "SET replaced_by = :edit")

	requireNamedQueryBinds(t, designStampReplacedBy, map[string]any{"id": 7, "edit": 12})
}

// СТОРОЖ cut_sheet СЧИТАЕТ ВСЕ ВИДИМЫЕ КРОПЫ ЭТОГО КАДРА — И ЗАМЕНЁННЫЕ СВОЕЙ ПРАВКОЙ ТОЖЕ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять фильтр глагола (правка листа — не разрез, и лист, у которого есть
// только флэттены, закрылся бы для перезаписи); снять hidden_at IS NULL (спрятанный кусок держал бы
// лист навсегда); ВЕРНУТЬ replaced_by IS NULL (O-53 review: лист, чей кусок перезаписан правкой,
// снова перезаписывается, и на экране остаются две живые ветки одного листа — новый лист и правка
// куска, нарезанного из прежних пикселей). Тот же ответ даёт предикат разреза — см.
// TestSheetCropsAreCropsNotEdits.
func TestVisibleCropsCountsEveryVisibleCrop(t *testing.T) {
	q := designVisibleCropsOf
	require.Contains(t, q, "derived_from = :id")
	require.Contains(t, q, "derivation = :crop", "глагол спрашивается у колонки 0359, а не выводится")
	require.Contains(t, q, "hidden_at IS NULL")
	require.NotContains(t, q, "replaced_by",
		"кусок, заменённый своей правкой, лист держит: правка нарезана из прежних пикселей")

	requireNamedQueryBinds(t, q, map[string]any{"id": 7, "crop": entity.DesignDerivationCrop})
}

// ПОВТОР ИЩЕТСЯ В ПРЕДЕЛАХ КАРТОЧКИ И ПО ТОЧНОМУ КЛЮЧУ (0369).
//
// МУТАЦИИ: потерять карточку в предикате (ключ чужой карточки отвечал бы на повтор этой — и индекс
// (tech_card_id, request_key) перестал бы служить чтению); потерять порядок (при двух кадрах с одним
// ключом — возможных только в обход стора — ответ стал бы броском монеты).
func TestPictureByRequestKeyIsScopedToTheCard(t *testing.T) {
	q := designPictureByRequestKey
	where := q[strings.Index(q, "WHERE"):]
	require.Contains(t, where, "tech_card_id = :card AND request_key = :key")
	require.True(t, strings.HasSuffix(strings.TrimSpace(q), "ORDER BY id LIMIT 1"))
	requireNamedQueryBinds(t, q, map[string]any{"card": 41, "key": "k-1"})
}

// ПУСТОЙ КЛЮЧ НЕ СОВПАДАЕТ НИ С ЧЕМ — И ДАЖЕ НЕ СПРАШИВАЕТ БАЗУ.
//
// МУТАЦИЯ: убрать ранний выход. Тогда пустая строка искалась бы ключом на каждом флэттене без ключа
// (с NULL-колонкой она не совпала бы, и вред казался бы нулевым — до первой строки, где ключ пуст, а
// не NULL). Проба ловит это nil-базой: чтение через неё паникует.
func TestPictureByRequestKeyWithoutAKeyReadsNothing(t *testing.T) {
	_, ok, err := pictureByRequestKey(context.Background(), nil, 41, "")
	require.NoError(t, err)
	require.False(t, ok)
}

// КЛЮЧ МЕРЯЕТСЯ ШИРИНОЙ КОЛОНКИ, В СИМВОЛАХ, И ОТКАЗ ЗВУЧИТ ДО ТРАНЗАКЦИИ.
//
// VARCHAR(64) в utf8mb4 — 64 СИМВОЛА, а не байта: 64 кириллических буквы (128 байт) законны, 65 —
// нет. Пробелы по краям срезаются до меры (колонка _bin хвостовых пробелов не различает).
//
// МУТАЦИИ: мерить байтами (64 «ж» отказываются); не мерить вовсе (65 доходят до транзакции и падают
// там сырым 1406 «Data too long»); мерить до среза пробелов (ключ с отступом отказывается).
func TestFlattenMeasuresTheKeyBeforeTheTransaction(t *testing.T) {
	reached := false
	s := &Store{txFunc: func(context.Context, func(context.Context, dependency.Repository) error) error {
		reached = true
		return nil
	}}
	flatten := func(key string) error {
		reached = false
		_, err := s.FlattenEditLayer(context.Background(), entity.DesignEditLayerFlatten{
			TechCardId: 1, LayerId: 5, ExpectedRev: 2, MediaId: 9, ClientRequestId: key,
		})
		return err
	}

	require.ErrorIs(t, flatten(strings.Repeat("ж", entity.DesignRequestKeyMaxRunes+1)), entity.ErrDesignInvalidArgument)
	require.False(t, reached, "длинный ключ обязан отказаться до транзакции")

	for _, key := range []string{
		"",
		strings.Repeat("ж", entity.DesignRequestKeyMaxRunes),
		"  " + strings.Repeat("k", entity.DesignRequestKeyMaxRunes) + "\t",
	} {
		require.NoError(t, flatten(key))
		require.True(t, reached, "ключ %q обязан дойти до транзакции", key)
	}
}

// ОТРИЦАТЕЛЬНОЕ МЕСТО ОТКАЗЫВАЕТСЯ ДО ТРАНЗАКЦИИ, А НЕ ЧИТАЕТСЯ НУЛЁМ.
//
// МУТАЦИЯ: убрать проверку. Тогда `replace_picture_id: -1` доходит до транзакции и подаётся РЯДОМ —
// то есть OK на просьбу поставить правку на место, которую никто не исполнил. Проба ловит это обоими
// способами: ошибки нет И замыкание достигнуто, не открывая ни одного соединения.
//
// ВТОРАЯ ПОЛОВИНА — контроль на ложную зелень: ноль («рядом») и положительный id доходят до
// транзакции, то есть отказ про ЗНАК, а не про само упоминание поля.
func TestFlattenRefusesANegativeReplaceTargetBeforeTheTransaction(t *testing.T) {
	reached := false
	s := &Store{txFunc: func(context.Context, func(context.Context, dependency.Repository) error) error {
		reached = true
		return nil
	}}
	_, err := s.FlattenEditLayer(context.Background(), entity.DesignEditLayerFlatten{
		TechCardId: 1, LayerId: 5, ExpectedRev: 2, MediaId: 9, ReplacePictureId: -1,
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
	require.False(t, reached, "отрицательное место обязано отказаться до транзакции")

	for _, target := range []int{0, 7} {
		reached = false
		_, err := s.FlattenEditLayer(context.Background(), entity.DesignEditLayerFlatten{
			TechCardId: 1, LayerId: 5, ExpectedRev: 2, MediaId: 9, ReplacePictureId: target,
		})
		require.NoError(t, err)
		require.True(t, reached, "replace_picture_id %d обязан дойти до транзакции", target)
	}
}

// requireNamedQueryBinds — ЛОВУШКА ДВОЕТОЧИЯ: sqlx сканирует имена параметров, не пропуская ни
// комментариев, ни литералов, поэтому лишнее ':' связалось бы пустым именем и уронило бы запрос в
// рантайме, а не здесь.
func requireNamedQueryBinds(t *testing.T, q string, params map[string]any) {
	t.Helper()
	require.NotContains(t, q, "--", "SQL comments do not belong in a named query")
	expanded, args, err := storeutil.MakeQuery(q, params)
	require.NoError(t, err, "named-parameter expansion must succeed")
	require.Equal(t, strings.Count(expanded, "?"), len(args))
	require.Len(t, args, len(params), "every parameter is bound exactly once")
}
