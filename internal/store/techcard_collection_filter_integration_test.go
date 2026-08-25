package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// insertCollectionCard кладёт минимальную тех-карту с заданной строкой коллекции.
//
// Коллекция пишется КОЛОНКОЙ `collection` (свободный текст) — единственной, которая у тех-карты
// есть: `collection_id` и её FK, заведённые 0154, дропнула 0240_drop_tech_card_collection_id.sql
// как мёртвую схему. Тест поэтому и написан: он фиксирует, что фильтр отвечает по ИМЕНИ, и следующий
// читатель не «починит» его на колонку-призрак.
func insertCollectionCard(ctx context.Context, t *testing.T, tag, collection string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card
		(style_number, name, brand, target_gender, top_category_id, collection)
		VALUES (CONCAT(?, '-', UUID_SHORT()), ?, 'b', 'unisex', 1, ?)`, tag, tag, collection)
	require.NoError(t, err)
	id64, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM tech_card WHERE id = ?`, id64) })
	return int(id64)
}

// TestListTechCardsCollectionFilter — фильтр листа тех-карт по коллекции.
//
// Утверждений три, и каждое отвечает на вопрос, который мог бы решиться неправильно:
//  1. фильтр СУЖАЕТ выдачу и делает это по точному имени;
//  2. карта с РУКОПИСНЫМ именем, которого нет в словаре коллекций, фильтруется наравне с остальными
//     (такие карты существуют намеренно, и именно ради них имя едет в строку листа — из него клиент
//     собирает пул значений фасета);
//  3. имя коллекции доезжает В СТРОКУ листа — без него пул собрать не из чего.
func TestListTechCardsCollectionFilter(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	named := "COLL-" + suffix           // «словарное» имя
	handwritten := "рукопись-" + suffix // имени нет ни в одном словаре — так тоже пишут

	cardA := insertCollectionCard(ctx, t, "cf-a-"+suffix, named)
	cardB := insertCollectionCard(ctx, t, "cf-b-"+suffix, named)
	cardC := insertCollectionCard(ctx, t, "cf-c-"+suffix, handwritten)

	byName := func(collection string) []entity.TechCard {
		cards, total, err := s.TechCards().ListTechCards(ctx, 100, 0, entity.Descending,
			entity.TechCardListFilter{Collection: collection})
		require.NoError(t, err)
		require.Equal(t, len(cards), total, "total обязан считать по тому же предикату, что и страница")
		return cards
	}

	got := byName(named)
	require.Len(t, got, 2, "фильтр обязан сузить выдачу до карт этой коллекции")
	ids := map[int]bool{}
	for _, c := range got {
		ids[c.Id] = true
		require.Equal(t, named, c.Collection.String, "имя коллекции обязано доехать в строку листа")
	}
	require.True(t, ids[cardA] && ids[cardB])
	require.False(t, ids[cardC], "чужая коллекция не имеет права попасть в выдачу")

	got = byName(handwritten)
	require.Len(t, got, 1, "рукописное имя вне словаря фильтруется наравне со словарным")
	require.Equal(t, cardC, got[0].Id)

	// Имя, которого нет ни у одной карты, — честное пустое состояние, а не молча снятый фильтр.
	require.Empty(t, byName("нет-такой-"+suffix))
}
