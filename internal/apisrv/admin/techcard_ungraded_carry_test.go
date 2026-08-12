package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// Третья нога контракта UNI (0302), близнец теста про КАК КРОИТСЯ рядом: `optional` в прото и
// IF(:ungraded_omitted, …) в сторе чинят ЗАПИСЬ и сами по себе ломают ДАЙДЖЕСТ. constructionProjection
// хеширует ungraded, а у неговорящей вкладки поле приезжает как false — значит подпись, поставленная
// именно из того клиента, ради которого поле сделано optional, сравнивалась бы с колонкой, где
// пометка стоит, и читалась бы как «изменено с момента утверждения» сразу и навсегда.
func TestCarryOmittedPieceUngradedKeepsTheConstructionDigestStable(t *testing.T) {
	const (
		pocket = "01DGSTPIECEPOCKET000000P1"
		front  = "01DGSTPIECEFRONT0000000F1"
	)
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Pieces: []entity.TechCardPiece{
			{LineKey: pocket, Name: "карман", PiecesPerGarment: 2, Grainline: "lengthwise", Ungraded: true},
			{LineKey: front, Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise"},
		},
	}}
	want := dto.TechCardSectionDigests(&stored.TechCardInsert)[entity.SignoffConstruction]

	staleTab := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: pocket, Name: "карман", PiecesPerGarment: 2, Grainline: "lengthwise", UngradedOmitted: true},
		{LineKey: front, Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise", UngradedOmitted: true},
	}}
	require.NotEqual(t, want, dto.TechCardSectionDigests(staleTab)[entity.SignoffConstruction],
		"guard: without the carry the digests must differ, or this test proves nothing")

	carryOmittedPieceUngradedFrom(stored, staleTab)
	require.Equal(t, want, dto.TechCardSectionDigests(staleTab)[entity.SignoffConstruction],
		"an approval from a tab that cannot speak the field must not be born stale")

	// Перенос — не безусловное копирование. Вкладка, которая ПОСКАЗАЛА, владеет значением, включая
	// явное снятие пометки: иначе UNI стало бы состоянием, в которое можно войти и нельзя выйти.
	current := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: pocket, Name: "карман", PiecesPerGarment: 2, Grainline: "lengthwise"},
		{LineKey: front, Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise", Ungraded: true},
	}}
	carryOmittedPieceUngradedFrom(stored, current)
	require.False(t, current.Pieces[0].Ungraded, "an explicit clear survives the carry")
	require.True(t, current.Pieces[1].Ungraded, "an explicit mark survives the carry")

	// Новая деталь переносить нечего, и по позиции соседа брать нельзя: градация — свойство ЭТОЙ
	// детали, а сосед в списке про неё ничего не знает.
	fresh := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: "01DGSTPIECENEW000000000N1", Name: "шлёвка", PiecesPerGarment: 5, UngradedOmitted: true},
	}}
	carryOmittedPieceUngradedFrom(stored, fresh)
	require.False(t, fresh.Pieces[0].Ungraded, "a new piece has nothing stored to carry")

	// Пустые концы — no-op, а не паника: на создании карточки хранимой стороны просто нет.
	carryOmittedPieceUngradedFrom(nil, fresh)
	carryOmittedPieceUngradedFrom(stored, nil)
}

// Ключи сравниваются так же, как их сравнивает СТОР: trim, дальше побайтово. Регистр-вариант ключа
// для upsertTechCardPieces — другая (новая) строка, и перенос обязан держать то же представление о
// личности детали, а не полагаться на то, что расхождение потом починит ER_DUP_ENTRY.
func TestCarryOmittedPieceUngradedMatchesTheStoresKeying(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Pieces: []entity.TechCardPiece{
			{LineKey: "01ABCDEF0000000000000001", Name: "карман", PiecesPerGarment: 2, Ungraded: true},
		},
	}}

	sameKeyPadded := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: "  01ABCDEF0000000000000001  ", Name: "карман", PiecesPerGarment: 2, UngradedOmitted: true},
	}}
	carryOmittedPieceUngradedFrom(stored, sameKeyPadded)
	require.True(t, sameKeyPadded.Pieces[0].Ungraded,
		"the store trims the incoming key, so the carry must trim it too")

	differentCase := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		{LineKey: "01abcdef0000000000000001", Name: "карман", PiecesPerGarment: 2, UngradedOmitted: true},
	}}
	carryOmittedPieceUngradedFrom(stored, differentCase)
	require.False(t, differentCase.Pieces[0].Ungraded,
		"the store treats a differently-cased key as a NEW row; the carry must not hand that row somebody else's marking")
}
