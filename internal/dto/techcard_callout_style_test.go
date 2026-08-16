package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// Пунктир, штриховка и список деталей (0310).
//
// Все утверждения этого файла делятся надвое. Первая половина — про то, чего фича НЕ должна
// сделать: ни один существующий отпечаток не двигается, и круглый рейс НОВОГО клиента по СТАРОЙ
// карточке даёт то же число, что и до выката. Вторая — про то, что новые факты в подпись входят,
// в отличие от цвета: пунктир и сплошная на чертеже говорят разное.
//
// Второй хвост опаснее первого. Хвост 0309 был один и открывался одним условием; теперь их два
// подряд, и восьмой элемент кортежа означает то геометрию, то стиль — если не потребовать, что
// второй хвост ВСЕГДА тянет за собой первый. Ровно это здесь и закрепляется.

// --- отпечаток: что не двигается -----------------------------------------------------------

// САМОЕ ВАЖНОЕ УТВЕРЖДЕНИЕ ФИЧИ ДЛЯ БАЗЫ. Новый клиент круглым рейсом вернёт `parts` списком из
// ОДНОГО имени там, где сегодня лежит одиночный `part`, — то есть на каждой живой карточке. Если
// бы список открывал хвост своим существованием, первое же сохранение из нового бандла объявило
// бы устаревшей каждую подписанную DESIGN, ничего в карточке не изменив.
func TestCalloutStyleDigestUnchangedForSinglePart(t *testing.T) {
	single := calloutDigestFixture()
	for i := range single.Callouts {
		single.Callouts[i].Parts = []string{single.Callouts[i].Part.String}
	}
	require.Equal(t, plainCalloutDesignDigest,
		TechCardSectionDigests(single)[entity.SignoffDesign],
		"одна деталь списком — та же одна деталь: хвоста быть не должно")
}

// И явные false у пунктира со штриховкой — тоже не факт: это состояние каждой существующей линии.
func TestCalloutStyleDigestUnchangedForExplicitFalse(t *testing.T) {
	plain := calloutDigestFixture()
	for i := range plain.Callouts {
		plain.Callouts[i].Dashed = false
		plain.Callouts[i].Filled = false
	}
	require.Equal(t, plainCalloutDesignDigest,
		TechCardSectionDigests(plain)[entity.SignoffDesign])
}

// --- отпечаток: что двигается --------------------------------------------------------------

func TestCalloutStyleDigestMovesOnEachNewFact(t *testing.T) {
	base := TechCardSectionDigests(calloutDigestFixture())[entity.SignoffDesign]

	dashed := calloutDigestFixture()
	dashed.Callouts[0].Kind = entity.AnnotationKindDim
	dashed.Callouts[0].Points = []entity.TechCardAnnotationPoint{
		{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.5"), Y: unit("0.5")},
	}
	solid := TechCardSectionDigests(dashed)[entity.SignoffDesign]
	dashed.Callouts[0].Dashed = true
	require.NotEqual(t, solid, TechCardSectionDigests(dashed)[entity.SignoffDesign],
		"пунктир вместо сплошной — другое указание цеху: линия построения против линии шва")

	hatched := calloutDigestFixture()
	hatched.Callouts[0].Kind = entity.AnnotationKindPolygon
	hatched.Callouts[0].Points = []entity.TechCardAnnotationPoint{
		{X: unit("0.1"), Y: unit("0.1")}, {X: unit("0.4"), Y: unit("0.1")}, {X: unit("0.4"), Y: unit("0.5")},
	}
	outline := TechCardSectionDigests(hatched)[entity.SignoffDesign]
	hatched.Callouts[0].Filled = true
	require.NotEqual(t, outline, TechCardSectionDigests(hatched)[entity.SignoffDesign],
		"штриховка — «эта площадь», контур — «эта граница»")

	second := calloutDigestFixture()
	second.Callouts[0].Parts = []string{second.Callouts[0].Part.String, "подборт"}
	require.NotEqual(t, base, TechCardSectionDigests(second)[entity.SignoffDesign],
		"вторая деталь на указании — то, о чём оно, и в подпись входит")
}

// ВТОРОЙ ХВОСТ ТЯНЕТ ПЕРВЫЙ. Пин с пунктиром невозможен через dto (флаг приводится к false), но
// проекция обязана быть однозначной и на данных, пришедших мимо неё — из архива, из клона сезона.
// Без этого правила восьмой элемент кортежа означал бы то геометрию, то стиль.
func TestCalloutStyleTailAlwaysCarriesGeometryTail(t *testing.T) {
	styleOnly := calloutDigestFixture()
	styleOnly.Callouts[0].Parts = []string{"полочка", "подборт"}

	shapeOnly := calloutDigestFixture()
	shapeOnly.Callouts[0].Kind = entity.AnnotationKindDim
	shapeOnly.Callouts[0].Points = []entity.TechCardAnnotationPoint{
		{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.5"), Y: unit("0.5")},
	}

	require.NotEqual(t,
		TechCardSectionDigests(styleOnly)[entity.SignoffDesign],
		TechCardSectionDigests(shapeOnly)[entity.SignoffDesign],
		"две детали на пине и мерка без деталей — разные указания, и кортеж обязан их различать")
}

// ЗАПИСЬ И ЧТЕНИЕ ОДНОЙ И ТОЙ ЖЕ ВЫНОСКИ ОБЯЗАНЫ ДАТЬ ОДИН ОТПЕЧАТОК — и это утверждение,
// которого не хватало, когда 0310 писался.
//
// Стор не пишет колонку `parts`, пока деталь одна: она уже лежит в `part`, и второй экземпляр той
// же строки был бы вторым местом, откуда её однажды прочтут по-разному. Значит одна и та же
// выноска выглядит по-разному на записи (Parts=["полочка"]) и на чтении (Parts=nil) — и пока
// проекция брала поле СЫРЫМ, второй хвост кодировался двумя способами.
//
// Цена: подпись DESIGN рождается протухшей при ПЕРВОМ ЖЕ осмысленном применении фичи (пунктирная
// мерка, заштрихованная зона) и НЕ ЛЕЧИТСЯ переутверждением — повторный штамп берёт то же
// расхождение. Прошлые тесты этого не ловили: они сравнивали запись с записью.
func TestCalloutStyleDigestSurvivesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shape   func(*entity.TechCardCallout)
	}{
		{"пунктирная мерка", func(c *entity.TechCardCallout) {
			c.Kind = entity.AnnotationKindDim
			c.Points = []entity.TechCardAnnotationPoint{
				{X: unit("0.2"), Y: unit("0.5")}, {X: unit("0.5"), Y: unit("0.5")},
			}
			c.Dashed = true
		}},
		{"заштрихованная зона", func(c *entity.TechCardCallout) {
			c.Kind = entity.AnnotationKindPolygon
			c.Points = []entity.TechCardAnnotationPoint{
				{X: unit("0.1"), Y: unit("0.1")}, {X: unit("0.4"), Y: unit("0.1")}, {X: unit("0.4"), Y: unit("0.5")},
			}
			c.Filled = true
		}},
		{"пин с пунктиром из архива", func(c *entity.TechCardCallout) { c.Dashed = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// КАК ВЫГЛЯДИТ НА ЗАПИСИ: dto нормализовал список, он непустой.
			write := calloutDigestFixture()
			tc.shape(&write.Callouts[0])
			write.Callouts[0].Parts = []string{write.Callouts[0].Part.String}

			// КАК ВЫГЛЯДИТ НА ЧТЕНИИ: колонка `parts` не заполнялась, деталь только в `part`.
			read := calloutDigestFixture()
			tc.shape(&read.Callouts[0])
			read.Callouts[0].Parts = nil

			require.Equal(t,
				TechCardSectionDigests(write)[entity.SignoffDesign],
				TechCardSectionDigests(read)[entity.SignoffDesign],
				"одна и та же выноска на записи и на чтении: расхождение делает подпись вечно протухшей")
		})
	}
}

// Повторы и пустые строки в колонке — испорченная строка, а не второй смысл: круглый рейс через
// dto их снимает, и проекция обязана снимать тоже, иначе отпечаток чтения не сойдётся с записью.
func TestCalloutStyleDigestNormalizesStoredParts(t *testing.T) {
	clean := calloutDigestFixture()
	clean.Callouts[0].Dashed = true
	clean.Callouts[0].Parts = []string{"полочка"}

	dirty := calloutDigestFixture()
	dirty.Callouts[0].Dashed = true
	dirty.Callouts[0].Parts = []string{"полочка", "полочка", "  "}

	require.Equal(t,
		TechCardSectionDigests(clean)[entity.SignoffDesign],
		TechCardSectionDigests(dirty)[entity.SignoffDesign],
		"деталь, названная дважды, — одно указание; отпечаток обязан это видеть")
}

// --- приведение бессмысленных флагов ---------------------------------------------------------

func TestCalloutGeometryNormalizesMeaninglessFlags(t *testing.T) {
	pin := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN
	geom, err := calloutGeometryFromPb("callouts[0]", &pb_common.TechCardCallout{
		Kind: &pin, Dashed: true, Filled: true,
	})
	require.NoError(t, err, "бессмысленный флаг — не порча данных: отказ здесь стоил бы дороже")
	require.False(t, geom.Dashed, "у точки нет линии, которую можно сделать пунктирной")
	require.False(t, geom.Filled, "у точки нет площади, которую можно заштриховать")

	dim := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM
	geom, err = calloutGeometryFromPb("callouts[0]", &pb_common.TechCardCallout{
		Kind:   &dim,
		Points: []*pb_common.TechCardAnnotationPoint{unitPointPb("0.2", "0.5"), unitPointPb("0.5", "0.5")},
		Dashed: true, Filled: true,
	})
	require.NoError(t, err)
	require.True(t, geom.Dashed, "у мерки линия есть")
	require.False(t, geom.Filled, "а площади нет")

	poly := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON
	geom, err = calloutGeometryFromPb("callouts[0]", &pb_common.TechCardCallout{
		Kind: &poly,
		Points: []*pb_common.TechCardAnnotationPoint{
			unitPointPb("0.1", "0.1"), unitPointPb("0.4", "0.1"), unitPointPb("0.4", "0.5"),
		},
		Dashed: true, Filled: true,
	})
	require.NoError(t, err)
	require.True(t, geom.Dashed)
	require.True(t, geom.Filled, "площадь есть только у полигона — и там флаг обязан выжить")
}

func unitPointPb(x, y string) *pb_common.TechCardAnnotationPoint {
	return &pb_common.TechCardAnnotationPoint{
		X: pbDecimalFromDecimal(unit(x)),
		Y: pbDecimalFromDecimal(unit(y)),
	}
}

// --- число точек новых видов ------------------------------------------------------------------

func TestPolygonAndInkPointCounts(t *testing.T) {
	for _, tc := range []struct {
		kind     entity.TechCardAnnotationKind
		min, max int
	}{
		{entity.AnnotationKindPolygon, 3, 40},
		{entity.AnnotationKindInk, 2, 200},
	} {
		min, max, ok := tc.kind.PointsAllowed()
		require.True(t, ok, "вид %s обязан быть известен: неизвестный отвергает сохранение всей карточки", tc.kind)
		require.Equal(t, tc.min, min, "вид %s", tc.kind)
		require.Equal(t, tc.max, max, "вид %s", tc.kind)
	}

	// Два угла — не область: «замкнуть» отрезок нечем, и отказ обязан назвать это словами.
	poly := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON
	_, err := calloutGeometryFromPb("callouts[0]", &pb_common.TechCardCallout{
		Kind:   &poly,
		Points: []*pb_common.TechCardAnnotationPoint{unitPointPb("0.1", "0.1"), unitPointPb("0.4", "0.1")},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "polygon")
}

// --- список деталей ---------------------------------------------------------------------------

// lineKey строит валидный 26-символьный ключ детали — тот же формат, которым карточка адресует
// деталь во входах операции и в назначениях материала.
func lineKey(tag string) string {
	k := tag + strings.Repeat("0", 26-len(tag))
	return k
}

func TestAnnotationPieceKeysFallback(t *testing.T) {
	fr, bk, sl := lineKey("FR"), lineKey("BK"), lineKey("SL")
	t.Run("старое поле читается списком", func(t *testing.T) {
		got, err := annotationPieceKeys("a", nil, fr)
		require.NoError(t, err)
		require.Equal(t, []string{fr}, got)
	})
	t.Run("список вытесняет старое поле целиком", func(t *testing.T) {
		got, err := annotationPieceKeys("a", []string{bk, sl}, fr)
		require.NoError(t, err)
		require.Equal(t, []string{bk, sl}, got,
			"непустой список — то, что человек выбрал; старое поле у нового клиента лишь эхо первого элемента")
	})
	t.Run("дубли снимаются молча", func(t *testing.T) {
		got, err := annotationPieceKeys("a", []string{fr, fr, "  ", bk}, "")
		require.NoError(t, err)
		require.Equal(t, []string{fr, bk}, got,
			"названная дважды деталь — одно указание, а не порча данных: отказ был бы отказом за опечатку")
	})
	t.Run("пусто остаётся пустым", func(t *testing.T) {
		got, err := annotationPieceKeys("a", nil, "   ")
		require.NoError(t, err)
		require.Nil(t, got, "указание про узел, а не про деталь, — законное состояние")
	})
	t.Run("кривой ключ отвергается формой", func(t *testing.T) {
		_, err := annotationPieceKeys("a", []string{"FR_1"}, "")
		require.Error(t, err, "ссылка советующая по РАЗРЕШИМОСТИ, но не по форме")
	})
	t.Run("предел назван словами", func(t *testing.T) {
		many := make([]string, 0, maxAnnotationPieces+1)
		for i := 0; i <= maxAnnotationPieces; i++ {
			many = append(many, lineKey(string(rune('A'+i))))
		}
		_, err := annotationPieceKeys("a", many, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "не больше")
	})
}

// Отпечаток CONSTRUCTION у выноски снимка шага: одна деталь списком — то же самое, что одна деталь
// старым полем. Это тот же довод, что у карточного указания, и та же цена ошибки.
func TestAnnotationDigestUnchangedForSingleKey(t *testing.T) {
	card := func(a entity.TechCardAnnotation) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Construction: &entity.TechCardConstruction{HemFinish: ns("подгибка 2 см")},
			Operations: []entity.TechCardOperation{{
				OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
				Media: []entity.TechCardOperationMedia{{
					MediaId: 7, Caption: ns("узел"), Annotations: []entity.TechCardAnnotation{a},
				}},
			}},
		}
	}
	legacy := entity.TechCardAnnotation{
		Kind: entity.AnnotationKindPin, Text: "закрепка",
		Points:       []entity.TechCardAnnotationPoint{{X: unit("0.3"), Y: unit("0.3")}},
		PieceLineKey: lineKey("FR"),
	}
	roundTripped := legacy
	roundTripped.PieceLineKeys = []string{lineKey("FR")}

	require.Equal(t,
		TechCardSectionDigests(card(legacy))[entity.SignoffConstruction],
		TechCardSectionDigests(card(roundTripped))[entity.SignoffConstruction],
		"круглый рейс нового клиента по старой выноске не должен двигать отпечаток")

	twoPieces := legacy
	twoPieces.PieceLineKeys = []string{lineKey("FR"), lineKey("BK")}
	require.NotEqual(t,
		TechCardSectionDigests(card(legacy))[entity.SignoffConstruction],
		TechCardSectionDigests(card(twoPieces))[entity.SignoffConstruction],
		"вторая деталь — то, о чём указание")
}

// --- перенос умолчанного ----------------------------------------------------------------------

// Пунктир и штриховка живут в той же атомарной группе, что вид и якоря: вкладка со старым бандлом
// молчит про всю фигуру целиком, и перенести дугу, потеряв её пунктир, значило бы отдать в цех
// другую линию — молча.
func TestCarryOmittedCalloutGeometryCarriesStyle(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: *calloutDigestFixture()}
	stored.Callouts[0].Kind = entity.AnnotationKindArc
	stored.Callouts[0].Points = []entity.TechCardAnnotationPoint{
		{X: unit("0.1"), Y: unit("0.5")}, {X: unit("0.3"), Y: unit("0.3")}, {X: unit("0.5"), Y: unit("0.5")},
	}
	stored.Callouts[0].Dashed = true

	silent := calloutDigestFixture()
	silent.Callouts[0].KindOmitted = true

	CarryOmittedCalloutGeometry(stored, silent)

	require.Equal(t, entity.AnnotationKindArc, silent.Callouts[0].Kind)
	require.Len(t, silent.Callouts[0].Points, 3)
	require.True(t, silent.Callouts[0].Dashed,
		"перенести дугу и потерять её пунктир — отдать в цех другую линию")
}
