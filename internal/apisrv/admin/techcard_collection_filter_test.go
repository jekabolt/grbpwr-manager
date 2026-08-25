package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestListTechCardsCollectionFilterIsPassedVerbatim — фильтр по коллекции обязан доехать до стора
// ДОСЛОВНО, без обрезки пробелов.
//
// ПОЧЕМУ ЭТО НЕ ПРИДИРКА. Пул значений фасета клиент собирает из тех же хранимых строк, которые
// сервер отдаёт в строке листа (TechCardListItem.collection). Карта с рукописным именем " SS25"
// кладёт в пул " SS25" дословно, и клик по этому пункту шлёт " SS25". Обрежь сервер запрос — и
// карта пропадёт при фильтре по СВОЕМУ ЖЕ значению: на utf8mb3 сравнение PAD SPACE добивает
// хвостовые пробелы, но ведущий не трогает никогда.
//
// Обрезка была бы уместна, будь на входе НАБРАННЫЙ текст (как у name/brand, где идёт LIKE по
// подстроке). Здесь на входе ВЫБРАННОЕ значение из готового списка, и «поправить» его значит
// разойтись с тем, что сам же сервер и выдал.
func TestListTechCardsCollectionFilterIsPassedVerbatim(t *testing.T) {
	const padded = " SS25"

	tc := mocks.NewMockTechCards(t)
	var got entity.TechCardListFilter
	tc.EXPECT().ListTechCards(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, _ entity.OrderFactor, f entity.TechCardListFilter) {
			got = f
		}).Return(nil, 0, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().TechCards().Return(tc)
	s := &Server{repo: repo}

	_, err := s.ListTechCards(context.Background(), &pb_admin.ListTechCardsRequest{Collection: padded})
	require.NoError(t, err)
	require.Equal(t, padded, got.Collection,
		"значение выбрано из пула, собранного из ответов этого же сервера, — обрезать его значит разойтись с самим собой")
}
