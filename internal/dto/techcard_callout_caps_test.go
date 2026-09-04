package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// Наконечники линии (0362).
//
// Опасность фичи вся в одном: наконечник — ТРЕТИЙ хвост подписи DESIGN, и открывается он на той же
// структуре, где уже лежат два. Восьмой элемент кортежа означает то геометрию, то стиль, девятый —
// то стиль, то наконечник; ошибка в условии не падает, не логируется и не лечится переутверждением
// — она объявляет протухшей каждую подписанную карточку, где кто-то нарисовал мерку.
//
// Поэтому первая половина файла — про то, чего фича НЕ делает.

// capsDigestFixture — карточка, на эскизе которой НАРИСОВАНЫ фигуры с концами: мерка и скобка.
// Пиновая карточка (calloutDigestFixture) третьего хвоста не открыла бы при любой ошибке в
// условии — у пина концов нет, — и зелёный тест на ней ничего бы не доказывал.
func capsDigestFixture() *entity.TechCardInsert {
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
				Kind: entity.AnnotationKindDim,
				Points: []entity.TechCardAnnotationPoint{
					{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.5"), Y: unit("0.5")},
				},
			},
			{
				Number: 2, Part: ns("спинка"), Description: ns("кокетка"),
				MediaId: nullInt32FromPb(11), PosX: calloutPos("0.7"), PosY: calloutPos("0.2"),
				Kind: entity.AnnotationKindBracket,
				Points: []entity.TechCardAnnotationPoint{
					{X: unit("0.6"), Y: unit("0.1")}, {X: unit("0.9"), Y: unit("0.1")},
				},
			},
		},
	}
}

// ЧИСЛО СНЯТО С БАЗОВОГО ДЕРЕВА, а не с этого. Тот же самый набор — мерка и скобка без выбранных
// концов — прогнан под кодом ДО 0362 через `go test -overlay`, то есть без единого записанного в
// дерево байта, и дал ровно это. Значит утверждение ниже проверяет не «мой код согласен сам с
// собой», а «мой код согласен с тем, что уже подписано на проде».
const pre0362DrawnCalloutDesignDigest = "b451b84f20adb436200b40786b57089dc2c1c978c04cc7f8c63cede34a7a1b44"

// САМОЕ ВАЖНОЕ УТВЕРЖДЕНИЕ ФИЧИ. Пустой наконечник — это НЕ «без наконечников», а «по виду»:
// мерка рисуется засечками, скобка скобками, ровно как рисовались до круга 18. Открывать под это
// хвост значило бы сдвинуть отпечаток каждой карточки с нарисованной меркой в момент выката.
func TestCapsDigestUnchangedWhenNoCapChosen(t *testing.T) {
	require.Equal(t, pre0362DrawnCalloutDesignDigest,
		TechCardSectionDigests(capsDigestFixture())[entity.SignoffDesign],
		"мерка и скобка без выбранных концов обязаны хешироваться как до 0362")
}

// И наконечник, приехавший на виде, у которого концов нет, — тоже не факт: сервер приводит его к
// пустому, и отпечаток обязан этого не заметить. Иначе «наконечник у зоны» стал бы способом
// протухнуть подписи, ничего не изменив на чертеже.
func TestCapsDigestUnchangedForCapOnKindWithoutEnds(t *testing.T) {
	silly := capsDigestFixture()
	silly.Callouts = append(silly.Callouts, entity.TechCardCallout{
		Number: 3, Part: ns("подборт"), MediaId: nullInt32FromPb(11),
		PosX: calloutPos("0.1"), PosY: calloutPos("0.1"),
		Kind: entity.AnnotationKindPin, Caps: entity.AnnotationCapsArrow,
	})
	withPin := silly
	plainPin := capsDigestFixture()
	plainPin.Callouts = append(plainPin.Callouts, entity.TechCardCallout{
		Number: 3, Part: ns("подборт"), MediaId: nullInt32FromPb(11),
		PosX: calloutPos("0.1"), PosY: calloutPos("0.1"),
		Kind: entity.AnnotationKindPin,
	})
	require.Equal(t,
		TechCardSectionDigests(plainPin)[entity.SignoffDesign],
		TechCardSectionDigests(withPin)[entity.SignoffDesign],
		"у точки нет концов: наконечник на ней не факт чертежа")
}

// --- отпечаток: что двигается --------------------------------------------------------------

// Наконечник входит в подпись, в отличие от цвета: засечка говорит «этот участок измерен»,
// стрелка — «смотри сюда», точка — «вот здесь». Это разные указания цеху, а не разное оформление.
func TestCapsDigestMovesOnEachCap(t *testing.T) {
	base := TechCardSectionDigests(capsDigestFixture())[entity.SignoffDesign]
	seen := map[string]entity.TechCardAnnotationCaps{base: ""}
	for _, c := range []entity.TechCardAnnotationCaps{
		entity.AnnotationCapsTick, entity.AnnotationCapsBracket,
		entity.AnnotationCapsBullet, entity.AnnotationCapsArrow,
	} {
		tc := capsDigestFixture()
		tc.Callouts[0].Caps = c
		d := TechCardSectionDigests(tc)[entity.SignoffDesign]
		if prev, ok := seen[d]; ok {
			t.Fatalf("наконечник %q дал тот же отпечаток, что %q — концы не входят в подпись", c, prev)
		}
		seen[d] = c
	}
}

// ТРЕТИЙ ХВОСТ ОБЯЗАН ТЯНУТЬ ЗА СОБОЙ ОБА ПРЕДЫДУЩИХ. Если бы он открывался один, кортеж мерки со
// стрелкой и кортеж мерки с пунктиром совпали бы по длине, и девятый элемент означал бы то стиль,
// то наконечник. Здесь это закрепляется наблюдаемо: наконечник без пунктира и пунктир без
// наконечника обязаны быть РАЗНЫМИ числами, и оба — отличными от базового.
func TestCapsTailDragsTheStyleTail(t *testing.T) {
	base := TechCardSectionDigests(capsDigestFixture())[entity.SignoffDesign]

	capOnly := capsDigestFixture()
	capOnly.Callouts[0].Caps = entity.AnnotationCapsArrow

	dashOnly := capsDigestFixture()
	dashOnly.Callouts[0].Dashed = true

	both := capsDigestFixture()
	both.Callouts[0].Caps = entity.AnnotationCapsArrow
	both.Callouts[0].Dashed = true

	a := TechCardSectionDigests(capOnly)[entity.SignoffDesign]
	b := TechCardSectionDigests(dashOnly)[entity.SignoffDesign]
	c := TechCardSectionDigests(both)[entity.SignoffDesign]

	require.NotEqual(t, base, a)
	require.NotEqual(t, base, b)
	require.NotEqual(t, a, b, "стрелка и пунктир — разные факты, а не один хвост на двоих")
	require.NotEqual(t, a, c)
	require.NotEqual(t, b, c)
}

// --- провод ---------------------------------------------------------------------------------

// Неизвестный наконечник ОТВЕРГАЕТСЯ, а не приводится к засечке: значение из будущего это
// сведения, которых сервер не понимает, и нарисовать вместо стрелки засечку значило бы молча
// отдать в цех другое указание.
func TestCapsFromPbRefusesUnknownValue(t *testing.T) {
	_, err := capsFromPb("callouts[0]", entity.AnnotationKindDim, pb_common.TechCardAnnotationCaps(77))
	require.Error(t, err)
}

// А осмысленный, но неуместный — приводится молча, ровно как пунктир у точки: бессмысленное
// значение это не порча данных, а отказ здесь стоил бы дороже.
func TestCapsFromPbCoercesOnKindWithoutEnds(t *testing.T) {
	got, err := capsFromPb("callouts[0]", entity.AnnotationKindPolygon,
		pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_ARROW)
	require.NoError(t, err)
	require.Equal(t, entity.TechCardAnnotationCaps(""), got)
}

// Круглый рейс: наконечник, приехавший с провода, уезжает обратно тем же. Без этого клиент,
// вернувший прочитанное, стирал бы концы каждым сохранением.
func TestCapsRoundTripsThroughTheWire(t *testing.T) {
	for entIn, pbIn := range annotationCapsToPb {
		got, err := capsFromPb("a", entity.AnnotationKindDim, pbIn)
		require.NoError(t, err)
		require.Equal(t, entIn, got)
		require.Equal(t, pbIn, annotationCapsToPb[got])
	}
}
