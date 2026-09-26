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

// СТОРОЖ cut_sheet СЧИТАЕТ ТОЛЬКО ВИДИМЫЕ НЕЗАМЕНЁННЫЕ КРОПЫ ЭТОГО КАДРА.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять фильтр глагола (правка листа — не разрез, и лист, у которого есть
// только флэттены, закрылся бы для перезаписи); снять hidden_at IS NULL (спрятанный кусок держал бы
// лист навсегда); снять replaced_by IS NULL (кусок, уже заменённый своей правкой, держал бы лист).
func TestVisibleCropsCountsOnlyVisibleUnreplacedCrops(t *testing.T) {
	q := designVisibleCropsOf
	require.Contains(t, q, "derived_from = :id")
	require.Contains(t, q, "derivation = :crop", "глагол спрашивается у колонки 0359, а не выводится")
	require.Contains(t, q, "hidden_at IS NULL")
	require.Contains(t, q, "replaced_by IS NULL")

	requireNamedQueryBinds(t, q, map[string]any{"id": 7, "crop": entity.DesignDerivationCrop})
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
