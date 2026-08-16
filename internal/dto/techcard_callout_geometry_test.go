package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Геометрия указаний на карточном эскизе (0309): отпечаток DESIGN и перенос умолчанного.
//
// Первое утверждение — про то, чего фича НЕ должна сделать. Хвост в позиционном кортеже выноски
// опасен ровно одним: безусловный элемент сдвинул бы отпечаток КАЖДОЙ карточки в базе и объявил бы
// все подписанные DESIGN устаревшими в момент выката — до того, как кто-нибудь нарисовал первую
// мерку. Против ЗАКРЕПЛЁННОГО числа, а не против самой себя: сравнение «до/после» внутри одного
// кода зелено ровно в том случае, ради которого тест написан.

// Отпечаток DESIGN этой фикстуры, снятый ДО 0309 (когда у выноски не было ни вида, ни якорей).
// Он не пересчитывается — он закрепляет прошлое.
const plainCalloutDesignDigest = "436b61ebf33bd4064dc72ae6c0473d468ae41705576bcb3051381ea6281c75bf"

func calloutDigestFixture() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		Concept: ns("плащ на кокетке"),
		Media: []entity.TechCardMediaItem{
			{MediaId: 11, Kind: "front", Category: "technical", Caption: ns("перёд")},
		},
		Callouts: []entity.TechCardCallout{
			{
				Number: 1, Part: ns("полочка"), Description: ns("отделочная строчка"),
				Dimensions: ns("6"), MediaId: nullInt32FromPb(11),
				PosX: calloutPos("0.3"), PosY: calloutPos("0.4"),
			},
			{
				Number: 2, Part: ns("спинка"), Description: ns("кокетка"),
				MediaId: nullInt32FromPb(11), PosX: calloutPos("0.7"), PosY: calloutPos("0.2"),
			},
		},
	}
}

func calloutPos(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: unit(v), Valid: true}
}

// САМОЕ ВАЖНОЕ УТВЕРЖДЕНИЕ ФИЧИ ДЛЯ БАЗЫ: карточка, на эскизе которой нарисованы только пины, —
// то есть КАЖДАЯ живая карточка — обязана хешировать байт-в-байт так же, как до правки.
func TestCalloutGeometryDigestUnchangedForPlainPins(t *testing.T) {
	require.Equal(t, plainCalloutDesignDigest,
		TechCardSectionDigests(calloutDigestFixture())[entity.SignoffDesign],
		"безусловный хвост геометрии сдвинул бы отпечаток каждой карточки и объявил устаревшими все подписанные DESIGN")

	// И отдельно: явный PIN с пустыми якорями — то же самое, что отсутствие полей.
	explicit := calloutDigestFixture()
	for i := range explicit.Callouts {
		explicit.Callouts[i].Kind = entity.AnnotationKindPin
		explicit.Callouts[i].Points = []entity.TechCardAnnotationPoint{}
	}
	require.Equal(t, plainCalloutDesignDigest,
		TechCardSectionDigests(explicit)[entity.SignoffDesign],
		"явный пин — не новое указание: хвоста быть не должно")
}

// Хвост появляется ровно тогда, когда точка становится меркой, — и подпись протухает по делу.
func TestCalloutGeometryDigestMovesWhenShapeDrawn(t *testing.T) {
	rich := calloutDigestFixture()
	rich.Callouts[0].Kind = entity.AnnotationKindDim
	rich.Callouts[0].Points = []entity.TechCardAnnotationPoint{
		{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.5"), Y: unit("0.5")},
	}

	require.NotEqual(t, plainCalloutDesignDigest,
		TechCardSectionDigests(rich)[entity.SignoffDesign],
		"мерка на эскизе — новое указание цеху, подпись обязана протухнуть")

	// Сдвинули якорь — сменилось указание.
	moved := calloutDigestFixture()
	moved.Callouts[0].Kind = entity.AnnotationKindDim
	moved.Callouts[0].Points = []entity.TechCardAnnotationPoint{
		{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.9"), Y: unit("0.5")},
	}
	require.NotEqual(t,
		TechCardSectionDigests(rich)[entity.SignoffDesign],
		TechCardSectionDigests(moved)[entity.SignoffDesign],
		"якорь мерки — сама мерка, его правка обязана двигать отпечаток")
}

// А ЦВЕТ — НЕ ДВИГАЕТ, ровно как у выносок на снимке шага: он различает пересекающиеся указания и
// смысла не несёт.
func TestCalloutGeometryDigestIgnoresColor(t *testing.T) {
	painted := calloutDigestFixture()
	painted.Callouts[0].Color = entity.AnnotationColorRed

	require.Equal(t, plainCalloutDesignDigest,
		TechCardSectionDigests(painted)[entity.SignoffDesign],
		"цвет указания не факт цеха: перекраска не должна протухать подпись")
}

// ПЕРЕНОС УМОЛЧАННОГО. Вкладка со старым бандлом шлёт выноски без вида и якорей; без переноса
// сохранение стёрло бы каждую мерку, а подпись, поставленная из такой вкладки, родилась бы
// устаревшей — и оставалась бы такой навсегда, потому что повторное подписание из того же клиента
// хеширует то же самое отсутствие.
func TestCarryOmittedCalloutGeometry(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: *calloutDigestFixture()}
	stored.Callouts[0].Kind = entity.AnnotationKindDim
	stored.Callouts[0].Points = []entity.TechCardAnnotationPoint{
		{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.5"), Y: unit("0.5")},
	}
	stored.Callouts[0].Color = entity.AnnotationColorBlue
	want := TechCardSectionDigests(&stored.TechCardInsert)[entity.SignoffDesign]

	// Вкладка со старым бандлом: содержимое то же, но про геометрию она не говорит вовсе.
	stale := calloutDigestFixture()
	for i := range stale.Callouts {
		stale.Callouts[i].KindOmitted = true
	}
	require.NotEqual(t, want, TechCardSectionDigests(stale)[entity.SignoffDesign],
		"без переноса умолчавшая вкладка хеширует «просто точки» — иначе тест не про то")

	CarryOmittedCalloutGeometry(stored, stale)
	require.Equal(t, want, TechCardSectionDigests(stale)[entity.SignoffDesign],
		"перенос обязан вернуть отпечаток к хранимому, иначе подпись рождается устаревшей")
	require.Equal(t, entity.AnnotationKindDim, stale.Callouts[0].Kind)
	require.Len(t, stale.Callouts[0].Points, 2)
	require.Equal(t, entity.AnnotationColorBlue, stale.Callouts[0].Color)

	// А ЯВНОЕ СЛОВО ПЕРЕНОСОМ НЕ ЗАТИРАЕТСЯ: человек, превративший мерку обратно в точку, сделал
	// это осознанно, и хранимое не имеет права вернуться поверх его решения.
	spoken := calloutDigestFixture()
	spoken.Callouts[0].Kind = entity.AnnotationKindPin
	CarryOmittedCalloutGeometry(stored, spoken)
	require.Equal(t, entity.AnnotationKindPin, spoken.Callouts[0].Kind)
	require.Empty(t, spoken.Callouts[0].Points)
}

// Выноска с номером, которого в хранимом нет (только что нарисована), переносом не трогается и
// падать на ней нечему.
func TestCarryOmittedCalloutGeometryIgnoresUnknownNumber(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: *calloutDigestFixture()}
	fresh := calloutDigestFixture()
	fresh.Callouts = append(fresh.Callouts, entity.TechCardCallout{
		Number: 9, KindOmitted: true, PosX: calloutPos("0.1"), PosY: calloutPos("0.1"),
	})
	CarryOmittedCalloutGeometry(stored, fresh)
	require.Equal(t, entity.TechCardAnnotationKind(""), fresh.Callouts[2].Kind)
}
