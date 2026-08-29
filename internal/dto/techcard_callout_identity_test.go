package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ОДИН НОМЕР — ДВА УКАЗАНИЯ. Эскиз и мудборд нумеруются независимо (клиент делает это намеренно), а
// схема дубли не запрещает вовсе. Значит «выноска номер 3» на карточке с мудбордом — вопрос без
// ответа, пока не назван эскиз, и оба потребителя этого номера обязаны отвечать на него ОДИНАКОВО.
//
// Пока правило было записано у каждого из них отдельно, они отвечали В РАЗНЫЕ СТОРОНЫ: индекс
// детали кроя брал последнего, перенос геометрии — первого. Ниже обе половины: что перенос не
// пересекает эскизы, и что на одном входе оба потребителя называют ОДНУ выноску.

// twinSketchCard — карточка, на которой номер 3 носят ДВА указания: мерка на техническом эскизе и
// записка на мудборде. Подписи у них нарочно разные, чтобы по результату переноса было видно,
// какая именно выноска ответила. Порядок задаётся снаружи: прежняя реализация выбирала по нему, и
// проверять надо оба.
func twinSketchCard(moodboardFirst bool) *entity.TechCard {
	technical := entity.TechCardCallout{
		Number: 3, Part: ns("полочка"), MediaId: nullInt32FromPb(11),
		PosX: calloutPos("0.3"), PosY: calloutPos("0.4"),
		Kind: entity.AnnotationKindDim,
		Points: []entity.TechCardAnnotationPoint{
			{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.5"), Y: unit("0.5")},
		},
		Color: entity.AnnotationColorBlue,
	}
	moodboard := entity.TechCardCallout{
		Number: 3, Part: ns("настроение"), MediaId: nullInt32FromPb(21),
		PosX: calloutPos("0.7"), PosY: calloutPos("0.2"),
		Kind: entity.AnnotationKindArc,
		Points: []entity.TechCardAnnotationPoint{
			{X: unit("0.1"), Y: unit("0.1")}, {X: unit("0.2"), Y: unit("0.2")}, {X: unit("0.3"), Y: unit("0.1")},
		},
		Color: entity.AnnotationColorRed,
	}
	callouts := []entity.TechCardCallout{technical, moodboard}
	if moodboardFirst {
		callouts = []entity.TechCardCallout{moodboard, technical}
	}
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Concept: ns("плащ на кокетке"),
		Media: []entity.TechCardMediaItem{
			{MediaId: 11, Kind: "front", Category: entity.TechCardMediaCategoryTechnical, Caption: ns("перёд")},
			{MediaId: 21, Kind: "front", Category: entity.TechCardMediaCategoryMoodboard, Caption: ns("референс")},
		},
		Callouts: callouts,
	}}
}

// ПЕРЕНОС ГЕОМЕТРИИ НЕ ПЕРЕСЕКАЕТ ЭСКИЗЫ. Якоря нормированы по КОНКРЕТНОЙ картинке, поэтому
// хранимая мерка с технического эскиза, доставшаяся мудбордной записке, — не потеря фигуры, а
// ПОДМЕНА: человек увидел бы на референсе размерную линию, которой там никто не рисовал.
//
// ОБА ПОРЯДКА, потому что прежняя реализация выбирала по одному номеру и первым вошедшим: одна и
// та же карточка, пересланная в другом порядке, отдавала фигуры крест-накрест.
func TestCarryOmittedCalloutGeometryDoesNotCrossSketches(t *testing.T) {
	for _, moodboardFirst := range []bool{false, true} {
		name := "технический эскиз первым"
		if moodboardFirst {
			name = "мудборд первым"
		}
		t.Run(name, func(t *testing.T) {
			stored := twinSketchCard(moodboardFirst)

			// Вкладка со старым бандлом: содержимое то же, но про геометрию она молчит.
			silent := &entity.TechCardInsert{
				Concept: stored.Concept,
				Media:   stored.Media,
			}
			for _, c := range stored.Callouts {
				silent.Callouts = append(silent.Callouts, entity.TechCardCallout{
					Number: c.Number, Part: c.Part, MediaId: c.MediaId,
					PosX: c.PosX, PosY: c.PosY, KindOmitted: true,
				})
			}

			CarryOmittedCalloutGeometry(stored, silent)

			for i, got := range silent.Callouts {
				want := stored.Callouts[i] // тот же порядок — сравниваем указание с самим собой
				require.Equal(t, want.Kind, got.Kind,
					"выноска на media %d обязана получить СВОЮ фигуру, а не фигуру тёзки с другой картинки", want.MediaId.Int32)
				require.Len(t, got.Points, len(want.Points))
				require.Equal(t, want.Color, got.Color)
			}
		})
	}
}

// СОГЛАСИЕ ДВУХ ПОТРЕБИТЕЛЕЙ ОДНОГО НОМЕРА. Индекс детали кроя (entity.TechCardCalloutIndex, им
// пользуется store/techcard.buildCalloutSync) и перенос геометрии на ОДНОМ И ТОМ ЖЕ входе обязаны
// называть ОДНУ И ТУ ЖЕ выноску.
//
// Проверяется не по внутренностям, а по результату: подписи двух тёзок нарочно разные, поэтому
// фигура, доехавшая до присланного указания, НАЗЫВАЕТ выноску, которая ответила переносу. Если
// сломать любую из двух половин правила по отдельности, стороны разойдутся и это утверждение
// покраснеет.
func TestCalloutNumberAnswersTheSameForPiecesAndGeometry(t *testing.T) {
	for _, moodboardFirst := range []bool{false, true} {
		stored := twinSketchCard(moodboardFirst)

		// Сторона ДЕТАЛИ КРОЯ: какую выноску носит номер 3.
		pieceSide, ok := entity.NewTechCardCalloutIndex(stored.Media, stored.Callouts).TechnicalCallout(3)
		require.True(t, ok, "номер 3 носит живая техническая выноска")

		// Сторона ГЕОМЕТРИИ: то же указание, присланное вкладкой, которая про фигуру молчит.
		silent := &entity.TechCardInsert{
			Concept:  stored.Concept,
			Media:    stored.Media,
			Callouts: []entity.TechCardCallout{{Number: 3, Part: pieceSide.Part, MediaId: pieceSide.MediaId, KindOmitted: true}},
		}
		CarryOmittedCalloutGeometry(stored, silent)
		geometrySide := silent.Callouts[0]

		require.Equal(t, pieceSide.CalloutKey(), geometrySide.CalloutKey())
		require.Equal(t, pieceSide.Kind, geometrySide.Kind,
			"переносу ответила НЕ та выноска, которую номер называет детали кроя")
		require.Len(t, geometrySide.Points, len(pieceSide.Points))
		require.Equal(t, pieceSide.Color, geometrySide.Color)
	}
}

// А ХРАНИМОЕ, КОТОРОГО НЕТ НА ЭТОЙ КАРТИНКЕ, НЕ ПЕРЕЕЗЖАЕТ. Указание, перенесённое человеком на
// другой эскиз, своей прежней фигуры не получает: якоря принадлежали ТОЙ картинке. Потеря честнее
// подмены — стёртую фигуру человек видит и рисует заново, выдуманную принимает за свою.
func TestCarryOmittedCalloutGeometrySkipsACalloutMovedToAnotherSketch(t *testing.T) {
	stored := twinSketchCard(false)
	moved := &entity.TechCardInsert{
		Concept: stored.Concept,
		Media:   stored.Media,
		// тот же номер 3, но человек перевесил указание с эскиза 11 на референс 21… которого под
		// этим номером там нет — на 21 висит СВОЯ выноска 3, и её фигура принадлежит ей.
		Callouts: []entity.TechCardCallout{{Number: 3, MediaId: nullInt32FromPb(31), KindOmitted: true}},
	}
	CarryOmittedCalloutGeometry(stored, moved)
	require.Equal(t, entity.TechCardAnnotationKind(""), moved.Callouts[0].Kind)
	require.Empty(t, moved.Callouts[0].Points)
}
