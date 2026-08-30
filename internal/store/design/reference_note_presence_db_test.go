package design_test

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ЗАПИСКА РЕФЕРЕНСА ПЕРЕЖИВАЕТ СОХРАНЕНИЕ, КОТОРОЕ О НЕЙ НЕ УПОМИНАЕТ.
//
// ЧТО ЭТО ЗА ДЕФЕКТ. У поля `note` на проводе было ДВА состояния — «вот текст» и «пустая строка», —
// а глаголу нужно ТРИ. Недостающее — «про записку ничего не сказано». Пока его не было, вкладка с
// более старым JS, которая поля не шлёт, была НЕОТЛИЧИМА от человека, нажавшего «стереть»: proto3
// декодирует отсутствие в "", апсерт читал "" как «очистить», и слова человека исчезали без
// единого жеста с чьей-либо стороны. Соседнее поле `garment_description` от этого защищено
// `optional` с самого начала — и потому этой беды не знало.
//
// ⚠ ПОЧЕМУ ПРОБА ОБЯЗАНА ХОДИТЬ В БАЗУ. Третья нога живёт в SQL — `IF(:note_omitted, note,
// VALUES(note))`. Сущность про неё ничего не знает, sqlx свяжет параметр молча в любом случае, а
// хендл стора — `d.Unsafe()`, поэтому расхождение читается в ничто без единой ошибки. Проверить
// это можно ТОЛЬКО исполнив тот самый оператор.
//
// АСИММЕТРИЯ СОХРАНЕНА И ПРОВЕРЯЕТСЯ ЗДЕСЬ ЖЕ: «сказано и пусто» по-прежнему ОЧИЩАЕТ. Иначе
// починка отняла бы у человека законный жест — стереть свои слова, — и это было бы не лечением, а
// заменой одной потери на другую.
func TestDesignDBReferenceNoteSurvivesASaveThatDoesNotMentionIt(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	mediaID := probeMedia(t, raw)
	ctx := context.Background()
	_, err := raw.Exec(`INSERT INTO tech_card_media (tech_card_id, media_id, category, display_order)
		VALUES (?, ?, 'moodboard', 0)`, card, mediaID)
	require.NoError(t, err)

	base := entity.DesignReferenceRole{
		TechCardId: card, MediaId: mediaID, Role: entity.DesignViewFront, Actor: "probe",
	}

	// Человек написал записку.
	written := base
	written.Note = "the fabric, not the cut"
	written.Ordinal = 1
	ref, err := rep.Design().SetReferenceRole(ctx, written)
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.Equal(t, "the fabric, not the cut", ref.Note.String)

	// СТАРАЯ ВКЛАДКА: шлёт роль и порядок, про записку молчит. Это и есть тот самый вызов, который
	// раньше стирал чужой текст. Ordinal меняем, чтобы апсерт ТОЧНО дошёл до ветки обновления —
	// иначе проба могла бы зеленеть просто потому, что писать было нечего.
	silent := base
	silent.NoteOmitted = true
	silent.Ordinal = 2
	ref, err = rep.Design().SetReferenceRole(ctx, silent)
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.True(t, ref.Note.Valid,
		"сохранение, не упомянувшее записку, стёрло её: у поля снова два состояния вместо трёх")
	require.Equal(t, "the fabric, not the cut", ref.Note.String,
		"молчание о записке обязано оставить её КАК БЫЛА, а не как угодно")
	require.Equal(t, 2, ref.Ordinal, "при этом остальное сохранение прошло — апсерт дошёл до строки")

	// И тем же значением её видит ЧИТАТЕЛЬ ПОЛОСЫ: снимок входов прогона собирается отсюда, и
	// потеря, не дошедшая до этого списка, всё равно замёрзла бы в истории.
	band, err := rep.Design().GetBand(ctx, card, 1)
	require.NoError(t, err)
	require.Len(t, band.References, 1)
	require.Equal(t, "the fabric, not the cut", band.References[0].Note.String)

	// СКАЗАНО И ПУСТО — по-прежнему ОЧИСТИТЬ. Законный жест человека не отнят.
	cleared := base
	cleared.NoteOmitted = false
	cleared.Ordinal = 3
	ref, err = rep.Design().SetReferenceRole(ctx, cleared)
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.False(t, ref.Note.Valid,
		"пустой текст при СКАЗАННОМ поле — настоящий ответ «сотри», и он обязан работать")
}
