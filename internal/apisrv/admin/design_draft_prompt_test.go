package admin

// ПРОБЫ ПРИВЯЗКИ ВЫНОСОК К КАРТИНКАМ В ПРОМПТЕ ЧЕРНОВИКА (V-19).
//
// Требование владельца дословно: «важно что модель принимала картинку и знала какой пин из
// колаутов как размечен а не что он просто есть (к какой картинке и какой части картинки)».
// До этой волны выноски уезжали плоским списком текстов; эти пробы прибивают три вещи, которые
// ломаются молча:
//
//   1. НОМЕР — ПО ФАКТИЧЕСКИ ПРИЛОЖЕННЫМ КАРТИНКАМ, а не по списку желаний доски: пропавшая
//      строка медиа не смеет сдвигать номера соседей на чужие картинки.
//   2. МЕСТО — В ДОЛЯХ КАДРА: у пина — его точка, у фигуры — центр якорей, потому что плашка
//      фигуры намеренно отводится ОТ отмеченного места.
//   3. ЗАПИСКА — ЭТО `concept` (V-16), с легаси-`mood_note` вторым абзацем, и обе видимы модели.
//
// МУТАЦИИ, КОТОРЫМИ ПРОВЕРЕНО (по ЧИСЛУ ИСПОЛНЕННЫХ ИСХОДОВ, не по коду возврата; каждая
// компилируется): нумерация по boardIDs вместо attached — см. отчёт задачи.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// draftBindCard — карточка с ГРОМКОЙ доской из трёх картинок и выносками на двух из них.
// Тексты выносок носят маркеры, которых нет ни в одном другом поле, чтобы Contains не зеленел
// на случайном совпадении.
func draftBindCard() *entity.TechCard {
	d := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	card := &entity.TechCard{}
	card.Name = "bind subject"
	card.Fit = sql.NullString{String: "boxy", Valid: true}
	card.Concept = sql.NullString{String: "CONCEPT-the-one-note", Valid: true}
	card.MoodNote = sql.NullString{String: "LEGACY-board-note", Valid: true}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 11, Category: entity.TechCardMediaCategoryMoodboard},
		{MediaId: 22, Category: entity.TechCardMediaCategoryMoodboard},
		{MediaId: 33, Category: entity.TechCardMediaCategoryMoodboard},
		{MediaId: 44, Category: entity.TechCardMediaCategoryTechnical},
	}
	card.Callouts = []entity.TechCardCallout{
		{
			// Пин на ВТОРОЙ картинке доски: точка пина и есть его pos_x/pos_y.
			Part:        sql.NullString{String: "collar", Valid: true},
			Description: sql.NullString{String: "PINWORDS-collar-roll", Valid: true},
			MediaId:     sql.NullInt32{Int32: 22, Valid: true},
			Kind:        entity.AnnotationKindPin,
			PosX:        d("0.31"),
			PosY:        d("0.62"),
		},
		{
			// Фигура на ТРЕТЬЕЙ: место — центр якорей (0.3, 0.7), а плашка нарочно в углу
			// (0.9, 0.1) — если в промпт уедет плашка, проба обязана это увидеть.
			Description: sql.NullString{String: "MULTIWORDS-seam-run", Valid: true},
			MediaId:     sql.NullInt32{Int32: 33, Valid: true},
			Kind:        entity.AnnotationKindMulti,
			PosX:        d("0.9"),
			PosY:        d("0.1"),
			Points: []entity.TechCardAnnotationPoint{
				{X: decimal.RequireFromString("0.2"), Y: decimal.RequireFromString("0.6")},
				{X: decimal.RequireFromString("0.4"), Y: decimal.RequireFromString("0.8")},
			},
		},
		{
			// Безсловесная отметка: designFrozenCallout её не замораживает — читать нечего.
			MediaId: sql.NullInt32{Int32: 22, Valid: true},
			Kind:    entity.AnnotationKindPin,
			PosX:    d("0.5"),
			PosY:    d("0.5"),
		},
		{
			// Выноска ТЕХНИЧЕСКОГО эскиза — не доска, в снимок не входит.
			Description: sql.NullString{String: "TECHWORDS-not-mood", Valid: true},
			MediaId:     sql.NullInt32{Int32: 44, Valid: true},
		},
	}
	return card
}

// КАЖДАЯ ВЫНОСКА НАЗЫВАЕТ СВОЮ КАРТИНКУ И СВОЁ МЕСТО.
func TestDraftIdeaPromptBindsEachCalloutToItsPictureAndSpot(t *testing.T) {
	card := draftBindCard()
	mood := designMoodSnapshot(card)
	require.NotNil(t, mood)
	require.Len(t, mood.GetCallouts(), 2, "две говорящие выноски доски — и только они")

	prompt := designDraftIdeaPrompt(card, mood, []int{11, 22, 33})

	// Привязка к картинке: пин живёт на второй приложенной, фигура — на третьей.
	require.Contains(t, prompt,
		"- picture 2 — pin at 31% from the left, 62% from the top: collar: PINWORDS-collar-roll",
		"пин обязан назвать СВОЮ картинку и точку в долях кадра")
	require.Contains(t, prompt,
		"- picture 3 — line at 30% from the left, 70% from the top: MULTIWORDS-seam-run",
		"у фигуры место — ЦЕНТР ЯКОРЕЙ, а не плашка с текстом")
	// Плашка фигуры (0.9, 0.1) в промпт уехать не смеет: она отводится ОТ отмеченного места.
	require.NotContains(t, prompt, "90% from the left",
		"место фигуры — якоря, а не отведённая плашка")
	// Нумерация объяснена модели словами — без вводной «picture 1 = первая приложенная» номер
	// это просто число.
	require.Contains(t, prompt, "attached in order")
	// Выноска эскиза и безсловесная отметка не оставляют строк.
	require.NotContains(t, prompt, "TECHWORDS-not-mood")
	require.Equal(t, 2, strings.Count(prompt, "- picture "),
		"строк привязки ровно столько, сколько говорящих выносок доски")
}

// НОМЕР СЧИТАЕТСЯ ПО ВЫЖИВШИМ: пропавшая картинка уносит свою выноску и НЕ сдвигает соседей.
func TestDraftIdeaPromptNumbersByAttachedSurvivors(t *testing.T) {
	card := draftBindCard()
	mood := designMoodSnapshot(card)
	require.NotNil(t, mood)

	// Медиа 22 не разрешилось (строка удалена из библиотеки): приложены только 11 и 33.
	prompt := designDraftIdeaPrompt(card, mood, []int{11, 33})

	require.NotContains(t, prompt, "PINWORDS-collar-roll",
		"слова выноски пропавшей картинки едут вместе с ней — то есть никуда")
	require.Contains(t, prompt,
		"- picture 2 — line at 30% from the left, 70% from the top: MULTIWORDS-seam-run",
		"фигура на 33-й теперь ВТОРАЯ приложенная — номер обязан считаться по выжившим, не по доске")
	require.Equal(t, 1, strings.Count(prompt, "- picture "))
}

// ЗАПИСКА ДОСКИ — ЭТО КОНЦЕПТ (V-16), И ЛЕГАСИ-ЗАМЕТКА НЕ ТЕРЯЕТСЯ.
//
// Обе видимы модели, концепт первым: он — принятое утверждение о стиле, легаси-абзац — хвост
// дословного слияния. Совпадающие тексты не дублируются, а карточка, у которой заполнен ТОЛЬКО
// концепт, проходит сторожа пустой доски — иначе слияние V-16 закрывало бы дверь, которую
// раньше открывала moodNote.
func TestDraftIdeaMoodNoteIsConceptPlusLegacy(t *testing.T) {
	card := draftBindCard()
	mood := designMoodSnapshot(card)
	require.NotNil(t, mood)
	require.Equal(t, "CONCEPT-the-one-note\nLEGACY-board-note", mood.GetNote(),
		"концепт первым, легаси-заметка вторым абзацем")

	// Совпадающие тексты не дублируются.
	card.MoodNote = card.Concept
	require.Equal(t, "CONCEPT-the-one-note", designMoodSnapshot(card).GetNote())

	// Только концепт, ни выносок, ни легаси: доска ГОВОРИТ и сторож обязан пропустить.
	bare := &entity.TechCard{}
	bare.Name = "concept only"
	bare.Concept = sql.NullString{String: "CONCEPT-alone", Valid: true}
	moodBare := designMoodSnapshot(bare)
	require.NotNil(t, moodBare, "заполненный концепт — это непустая доска после V-16")
	require.Equal(t, "CONCEPT-alone", moodBare.GetNote())

	// Ни слова нигде — доске нечего сказать, платный вызов не покупается.
	require.Nil(t, designMoodSnapshot(&entity.TechCard{}))
}

// СИСТЕМНЫЙ ПРОМПТ ПРОСИТ РОВНО ТРИ СЕКЦИИ, И ИХ ЗАГОЛОВКИ — КОНТРАКТ С КЛИЕНТОМ.
//
// parseDraftSections на клиенте различает судьбы ответа ПО ЭТИМ СТРОКАМ: описание предлагается
// в концепт, аспекты и недостающие выноски — совет. Переименованный здесь заголовок молча
// понизил бы свою секцию до «предлагать всё в концепт».
func TestDraftIdeaSystemPromptNamesTheThreeSections(t *testing.T) {
	for _, title := range []string{"DESCRIPTION", "DESIGN ASPECTS", "MISSING CALLOUTS"} {
		require.Contains(t, draftIdeaSystemPrompt, title)
	}
	require.Contains(t, draftIdeaSystemPrompt, "names its picture by number",
		"роль обязана сказать модели, что привязка пинов к картинкам ЕСТЬ и что ею надо пользоваться")
}
