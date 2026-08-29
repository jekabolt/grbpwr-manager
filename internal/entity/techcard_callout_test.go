package entity

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// ОДИН НОМЕР — ДВА УКАЗАНИЯ, И ДВА ПОТРЕБИТЕЛЯ, РЕШАВШИЕ ЭТО В РАЗНЫЕ СТОРОНЫ.
//
// Утверждения ниже держат ровно ту границу, из-за расхождения на которой деталь кроя молча
// объявлялась оторванной. Главное из них — TestCalloutIndexMoodboardTwinDoesNotDetachThePiece: оно
// ОБЯЗАНО падать на прежней реализации, писавшей в карту без условия (последний выигрывает),
// потому что там судьба живой детали зависела от порядка выносок в payload'е.

func ncNull() sql.NullInt32       { return sql.NullInt32{} }
func nc(i int32) sql.NullInt32    { return sql.NullInt32{Int32: i, Valid: true} }
func ncs(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func sketch(id int) TechCardMediaItem {
	return TechCardMediaItem{MediaId: id, Category: TechCardMediaCategoryTechnical}
}
func mood(id int) TechCardMediaItem {
	return TechCardMediaItem{MediaId: id, Category: TechCardMediaCategoryMoodboard}
}

// МУДБОРДНЫЙ ДВОЙНИК НЕ ИМЕЕТ ПРАВА ОТОРВАТЬ ДЕТАЛЬ. Клиент нумерует эскиз и мудборд независимо, и
// «выноска 3» на карточке с мудбордом — это ДВА разных указания. Деталь кроя всегда имеет в виду
// техническое; мудбордная записка о номере детали не знает вовсе.
//
// ПОРЯДОК В PAYLOAD'Е НИЧЕГО НЕ РЕШАЕТ — и это вторая половина утверждения. Прежняя карта писалась
// без условия, поэтому те же самые данные, пересланные в другом порядке, давали ПРОТИВОПОЛОЖНЫЙ
// ответ: деталь то оторвана, то нет, при неизменной карточке.
func TestCalloutIndexMoodboardTwinDoesNotDetachThePiece(t *testing.T) {
	media := []TechCardMediaItem{sketch(10), mood(20)}
	technical := TechCardCallout{Number: 3, Part: ncs("Collar"), MediaId: nc(10)}
	moodboard := TechCardCallout{Number: 3, Part: ncs("Mood ref"), MediaId: nc(20)}

	for _, order := range []struct {
		name     string
		callouts []TechCardCallout
	}{
		{"мудбордный двойник ПОСЛЕ технического", []TechCardCallout{technical, moodboard}},
		{"мудбордный двойник ПЕРЕД техническим", []TechCardCallout{moodboard, technical}},
	} {
		t.Run(order.name, func(t *testing.T) {
			ix := NewTechCardCalloutIndex(media, order.callouts)

			// САМО УТВЕРЖДЕНИЕ ДЕФЕКТА — первым, чтобы краснело именно оно, а не проверка
			// пониже: require обрывает подтест на первом же несовпадении.
			p := TechCardPiece{Name: "old", CalloutNumber: nc(3)}
			ix.ApplyToPiece(&p)
			require.False(t, p.Detached,
				"источник имени детали никуда не делся — отрывать её нечему")
			require.Equal(t, "Collar", p.Name,
				"имя детали живёт один раз, на технической выноске (S8)")

			got, ok := ix.TechnicalCallout(3)
			require.True(t, ok, "номер 3 носит живая техническая выноска")
			require.Equal(t, int32(10), got.MediaId.Int32,
				"номер обязан разрешаться в указание НА ЭСКИЗЕ, а не в мудбордную записку")
		})
	}
}

// ГРАНИЦА «ТЕХНИЧЕСКИЙ ЛИСТ» ЦЕЛИКОМ (S7/S8). Правила те же, что держал прежний apply; они
// перенесены сюда вместе с самим правилом и обязаны остаться неизменными — починка коллизии не
// давала права смягчить хоть одно из них.
func TestCalloutIndexPieceSemanticsBoundary(t *testing.T) {
	media := []TechCardMediaItem{sketch(10), mood(20)}
	ix := NewTechCardCalloutIndex(media, []TechCardCallout{
		{Number: 1, Part: ncs("Collar"), MediaId: nc(10)},     // технический эскиз
		{Number: 2, Part: ncs("Vibe"), MediaId: nc(20)},       // мудборд
		{Number: 3, Part: ncs("Floating"), MediaId: ncNull()}, // ни на чём не запинено
		{Number: 4, Part: ncs("Ghost"), MediaId: nc(77)},      // картинки нет в карточке
		{Number: 5, Part: ncs("   "), MediaId: nc(10)},        // техническая, но без имени
	})

	cases := []struct {
		name         string
		piece        TechCardPiece
		wantName     string
		wantDetached bool
	}{
		{"техническая выноска даёт имя", TechCardPiece{Name: "old", CalloutNumber: nc(1)}, "Collar", false},
		{"мудбордная не несёт смысла детали", TechCardPiece{Name: "keep", CalloutNumber: nc(2)}, "keep", true},
		{"незапиненная не несёт смысла детали", TechCardPiece{Name: "keep", CalloutNumber: nc(3)}, "keep", true},
		{"чужая картинка не эскиз этой карточки", TechCardPiece{Name: "keep", CalloutNumber: nc(4)}, "keep", true},
		{"пустое имя выноски не стирает имя детали", TechCardPiece{Name: "keep", CalloutNumber: nc(5)}, "keep", false},
		{"удалённая выноска отрывает деталь", TechCardPiece{Name: "keep", CalloutNumber: nc(99)}, "keep", true},
		{"деталь без выноски свободна", TechCardPiece{Name: "free"}, "free", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.piece
			ix.ApplyToPiece(&p)
			require.Equal(t, c.wantName, p.Name)
			require.Equal(t, c.wantDetached, p.Detached)
		})
	}
}

// ДУБЛЬ ВНУТРИ ОДНОГО ЭСКИЗА — уже испорченные данные, но ответ на них обязан быть один и тот же у
// всех, кто спрашивает. Первый выигрывает — ровно та же развязка, что у TechCardCalloutsByKey,
// которым пользуется перенос геометрии.
func TestCalloutIndexDuplicateOnOneSketchFirstWins(t *testing.T) {
	media := []TechCardMediaItem{sketch(10)}
	callouts := []TechCardCallout{
		{Number: 7, Part: ncs("First"), MediaId: nc(10)},
		{Number: 7, Part: ncs("Second"), MediaId: nc(10)},
	}
	got, ok := NewTechCardCalloutIndex(media, callouts).TechnicalCallout(7)
	require.True(t, ok)
	require.Equal(t, "First", got.Part.String)

	byKey := TechCardCalloutsByKey(callouts)
	require.Equal(t, "First", byKey[callouts[0].CalloutKey()].Part.String,
		"обе половины правила обязаны развязывать дубль ОДИНАКОВО")
}

// ИДЕНТИЧНОСТЬ УКАЗАНИЯ — ПАРА, А НЕ ЧИСЛО. Один номер на двух картинках даёт два разных ключа;
// незапиненное указание держит собственный ключ и ни с чем не сливается.
func TestCalloutKeyIsSketchPlusNumber(t *testing.T) {
	onSketch := TechCardCallout{Number: 3, MediaId: nc(10)}
	onMoodboard := TechCardCallout{Number: 3, MediaId: nc(20)}
	unpinned := TechCardCallout{Number: 3, MediaId: ncNull()}

	require.NotEqual(t, onSketch.CalloutKey(), onMoodboard.CalloutKey())
	require.NotEqual(t, onSketch.CalloutKey(), unpinned.CalloutKey())
	require.Equal(t, TechCardCalloutKey{MediaId: 0, Number: 3}, unpinned.CalloutKey())

	byKey := TechCardCalloutsByKey([]TechCardCallout{onSketch, onMoodboard, unpinned})
	require.Len(t, byKey, 3, "три указания с одним номером — три разных указания")
}
