package design

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ ПРЕДИКАТА «КУСКИ ЛИСТА» (O-53 follow-up), НЕ ТРЕБУЮЩИЕ БАЗЫ.
//
// Живая половина — правка рядом и правка на месте не закрывают разрез, заменённый кусок держит лист,
// спрятанный не держит, легаси держит, ответ свежего разреза — только его куски — лежит в
// split_crops_db_test.go и ходит в одноразовый контейнер (CI=1). Здесь — форма самого предиката.

// КУСКИ — ЭТО КРОПЫ, А НЕ ЛЮБЫЕ ДЕТИ.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять фильтр глагола (правка листа снова отвечает «уже нарезано» сама
// собой — исходный дефект); выбросить легаси из IN (лист со старыми неклассифицированными кусками
// режется вторым комплектом); снять hidden_at IS NULL (исчезает объявленный выход «спрятать плохие
// куски и резать снова»); добавить фильтр replaced_by (лист, у которого перезаписан каждый кусок,
// режется повторно); потерять порядок (ответ перестаёт идти в порядке кадров).
func TestSheetCropsAreCropsNotEdits(t *testing.T) {
	q := designSheetCropsOf
	where := q[strings.Index(q, "WHERE"):]
	require.Contains(t, where, "derived_from = :id")
	require.Contains(t, where, "derivation IN (:crop, :unclassified)",
		"глагол спрашивается у колонки 0359: правка листа — не кусок")
	require.Contains(t, where, "hidden_at IS NULL", "спрятанный кусок лист не держит")
	require.NotContains(t, where, "replaced_by",
		"заменённый кусок — всё ещё кусок: фильтр по замене режет лист вторым комплектом")
	require.True(t, strings.HasSuffix(strings.TrimSpace(q), "ORDER BY ordinal, id"),
		"ответ разреза идёт в порядке кадров")

	params := designSheetCropsParams(7)
	require.Equal(t, map[string]any{
		"id": 7, "crop": entity.DesignDerivationCrop, "unclassified": entity.DesignDerivationNone,
	}, params)
	require.Equal(t, "crop", params["crop"], "словарь глагола — проводной (0359)")
	require.Equal(t, "", params["unclassified"], "легаси — пустой глагол, а не NULL")
	for k, v := range params {
		require.NotEqual(t, entity.DesignDerivationFlatten, v, "%s: правка в куски не входит", k)
	}

	requireNamedQueryBinds(t, q, params)
}
