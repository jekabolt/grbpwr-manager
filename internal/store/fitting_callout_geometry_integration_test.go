package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Указания на снимках примерки (0319): весь путь, который проходит нарисованная фигура —
// провод → dto → колонки fitting_callout → ОБА пути чтения → и, главное, обратно на запись.
//
// Гонится через dto.ConvertPbFittingInsertToEntity, а не собранной руками сущностью, потому что
// проверять здесь надо именно то, что примерка пользуется ОБЩИМ сводом карточной выноски: вид из
// закрытого словаря, число якорей по виду, приведение бессмысленных пунктира и штриховки.
// Собранная руками сущность обошла бы ровно этот шов.
//
// Оба пути чтения утверждаются нарочно. Экран примерки читает GetFitting, но список примерок —
// вторая проекция того же содержимого, и поле, молча пропавшее из одной из них, это ровно то, как
// случается «клиент сохранил прочитанное и стёр геометрию».
//
// Интеграционный: только против живого MySQL (TestMain подключает и мигрирует). Всё вставленное
// убирается за собой, в порядке FK.
func TestFittingCalloutGeometry(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	mediaID := insertTestMedia(t, "fitgeom-"+suffix)

	var techCardID, fittingID int
	defer func() {
		bg := context.Background()
		if fittingID != 0 {
			_ = s.Fittings().DeleteFitting(bg, fittingID)
		}
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(bg, techCardID)
		}
		_, _ = testDB.ExecContext(bg, `DELETE FROM media WHERE id = ?`, mediaID)
	}()

	// Карточка нужна не сама по себе: она пришпиливает примерку в собственный однострочный список,
	// иначе ListFittings пришлось бы просить целую страницу, и на засеянной базе примерка выпала бы
	// из неё по причине, к геометрии отношения не имеющей.
	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "FIT-GEOM-" + suffix[len(suffix)-8:], Valid: true},
		Name:            "fitting callout geometry",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
	})
	require.NoError(t, err)

	// --- круговой рейс четырёх видов ------------------------------------------------------------

	insert := &pb_common.FittingInsert{
		TechCardId:  int32(techCardID),
		FittingDate: nowPb(),
		Status:      pb_common.FittingStatus_FITTING_STATUS_PLANNED,
		Verdict:     pb_common.FittingVerdict_FITTING_VERDICT_PENDING,
		MediaIds:    []int32{int32(mediaID)},
		Callouts: []*pb_common.FittingCallout{
			fitCallout(1, "плечо жмёт", mediaID, "0.10", "0.10",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN, nil,
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN, false, false),
			// Мерка: две точки и пунктир — «здесь замеряли», а не «здесь шов».
			fitCallout(2, "по груди на 2 см уже", mediaID, "0.20", "0.30",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
				fitPoints("0.20", "0.50", "0.60", "0.50"),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED, true, true),
			// Дуга: три точки, средняя ЛЕЖИТ НА КРИВОЙ.
			fitCallout(3, "залом по окату", mediaID, "0.40", "0.20",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC,
				fitPoints("0.30", "0.20", "0.40", "0.30", "0.50", "0.20"),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN, false, false),
			// Зона: замкнутый контур со штриховкой — «вот эта площадь», а не «вот эта граница».
			fitCallout(4, "зона заломов", mediaID, "0.70", "0.70",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON,
				fitPoints("0.60", "0.60", "0.90", "0.60", "0.90", "0.90"),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_BLUE, false, true),
		},
	}
	fi, err := dto.ConvertPbFittingInsertToEntity(insert)
	require.NoError(t, err)
	// Флаги приводятся ОБЩИМ сводом, ещё до стора: у мерки линия есть, площади нет.
	require.True(t, fi.Callouts[1].Dashed, "у мерки линия есть — пунктир обязан выжить")
	require.False(t, fi.Callouts[1].Filled, "площади у мерки нет: бессмысленная штриховка гасится, а не отвергается")

	fittingID, err = s.Fittings().AddFitting(ctx, fi)
	require.NoError(t, err)

	stored, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	assertFittingGeometry(t, stored.Callouts)

	// Вторая проекция того же содержимого.
	listed, _, err := s.Fittings().ListFittings(ctx, 10, 0, entity.Descending, 0, 0, techCardID)
	require.NoError(t, err)
	require.Len(t, listed, 1, "примерка обязана найтись в списке своей карточки")
	assertFittingGeometry(t, listed[0].Callouts)

	// Обратное преобразование отдаёт вид ВСЕГДА — чтение не бывает «умолчавшим».
	readBack := dto.ConvertEntityFittingToPb(stored).GetFitting().GetCallouts()
	require.Len(t, readBack, 4)
	for _, c := range readBack {
		require.NotNil(t, c.Kind, "вид на чтении обязан присутствовать: иначе круглый рейс молчит про геометрию")
	}
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM, readBack[1].GetKind())
	require.Len(t, readBack[1].GetPoints(), 2)
	require.Equal(t, pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED, readBack[1].GetColor())
	require.True(t, readBack[1].GetDashed())
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON, readBack[3].GetKind())
	require.True(t, readBack[3].GetFilled())

	// --- вкладка со старым бандлом не стирает нарисованное ---------------------------------------
	//
	// Клиент, который про геометрию не знает, шлёт номер, записку и координаты маркера — и НИЧЕГО
	// про фигуру. Выноски сохраняются полной заменой, так что без переноса такое сохранение (правка
	// одного только вердикта) снесло бы и мерку, и дугу, и зону.

	silent := &pb_common.FittingInsert{
		TechCardId:  int32(techCardID),
		FittingDate: nowPb(),
		Status:      pb_common.FittingStatus_FITTING_STATUS_DONE,
		Verdict:     pb_common.FittingVerdict_FITTING_VERDICT_NEEDS_REWORK,
		MediaIds:    []int32{int32(mediaID)},
		Callouts: []*pb_common.FittingCallout{
			{Number: 1, Note: "плечо жмёт", MediaId: int32(mediaID), PosX: fitDec("0.10"), PosY: fitDec("0.10")},
			{Number: 2, Note: "по груди на 2 см уже", MediaId: int32(mediaID), PosX: fitDec("0.20"), PosY: fitDec("0.30")},
			{Number: 3, Note: "залом по окату", MediaId: int32(mediaID), PosX: fitDec("0.40"), PosY: fitDec("0.20")},
			{Number: 4, Note: "зона заломов", MediaId: int32(mediaID), PosX: fitDec("0.70"), PosY: fitDec("0.70")},
		},
	}
	silentFi, err := dto.ConvertPbFittingInsertToEntity(silent)
	require.NoError(t, err)
	for _, c := range silentFi.Callouts {
		require.True(t, c.KindOmitted, "молчание про вид обязано быть видно серверу, иначе переносить нечего")
	}
	require.True(t, dto.FittingCalloutGeometryOmitted(silentFi))
	require.Empty(t, silentFi.Callouts[1].Points, "без переноса умолчавшая вкладка несёт «просто точки» — иначе тест не про то")

	dto.CarryOmittedFittingCalloutGeometry(stored, silentFi)
	require.NoError(t, s.Fittings().UpdateFitting(ctx, fittingID, silentFi, stored.LockVersion))

	after, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	assertFittingGeometry(t, after.Callouts)
	require.Equal(t, entity.FittingNeedsRework, after.Verdict, "правка вердикта обязана доехать: перенос трогает только геометрию")

	// --- явный PIN превращает фигуру обратно в точку ---------------------------------------------
	//
	// Это осознанное действие человека, и хранимое не имеет права вернуться поверх него.

	pin := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN
	flattened := &pb_common.FittingInsert{
		TechCardId:  int32(techCardID),
		FittingDate: nowPb(),
		Status:      pb_common.FittingStatus_FITTING_STATUS_DONE,
		Verdict:     pb_common.FittingVerdict_FITTING_VERDICT_NEEDS_REWORK,
		MediaIds:    []int32{int32(mediaID)},
		Callouts: []*pb_common.FittingCallout{
			{Number: 2, Note: "по груди на 2 см уже", MediaId: int32(mediaID),
				PosX: fitDec("0.20"), PosY: fitDec("0.30"), Kind: &pin},
		},
	}
	flatFi, err := dto.ConvertPbFittingInsertToEntity(flattened)
	require.NoError(t, err)
	require.False(t, flatFi.Callouts[0].KindOmitted, "явный PIN — не молчание")
	require.False(t, dto.FittingCalloutGeometryOmitted(flatFi))
	dto.CarryOmittedFittingCalloutGeometry(after, flatFi)
	require.Equal(t, entity.AnnotationKindPin, flatFi.Callouts[0].Kind, "перенос не имеет права затереть явное слово")
	require.Empty(t, flatFi.Callouts[0].Points)
	require.NoError(t, s.Fittings().UpdateFitting(ctx, fittingID, flatFi, after.LockVersion))

	flat, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	require.Len(t, flat.Callouts, 1)
	require.Equal(t, entity.AnnotationKindPin, flat.Callouts[0].Kind)
	require.Empty(t, flat.Callouts[0].Points, "мерка, превращённая в точку, якорей не хранит")
	require.False(t, flat.Callouts[0].Dashed, "пунктир — часть той же группы, он уходит вместе с фигурой")

	// --- строка, записанная ДО 0319 --------------------------------------------------------------
	//
	// У неё kind пуст (сущность приезжает в обход колонки на архивных путях), а points — NULL:
	// «якорей никто не ставил». Читается она как точка, и никакой ошибки в этом нет.

	_, err = testDB.ExecContext(ctx,
		`UPDATE fitting_callout SET kind = '', color = '', points = NULL, dashed = 0, filled = 0
		 WHERE fitting_id = ?`, fittingID)
	require.NoError(t, err)

	legacy, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	require.Len(t, legacy.Callouts, 1)
	require.Empty(t, legacy.Callouts[0].Points)
	legacyPb := dto.ConvertEntityFittingToPb(legacy).GetFitting().GetCallouts()
	require.Len(t, legacyPb, 1)
	require.NotNil(t, legacyPb[0].Kind)
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN, legacyPb[0].GetKind(),
		"выноска, записанная до 0319, читается как то, чем она была — нумерованной точкой")

	// --- отказ на неверном числе якорей называет поле --------------------------------------------

	arc := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC
	_, err = dto.ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
		TechCardId:  int32(techCardID),
		FittingDate: nowPb(),
		Callouts: []*pb_common.FittingCallout{
			{Number: 1, Note: "залом", Kind: &arc, Points: fitPoints("0.10", "0.10", "0.40", "0.40")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "callouts[0].points",
		"отказ обязан назвать выноску и поле: иначе технолог ищет её глазами среди тридцати")
	require.Contains(t, err.Error(), "arc")

	// Координата вне кадра ловится тем же сводом, что и на карточке.
	dim := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM
	_, err = dto.ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
		TechCardId:  int32(techCardID),
		FittingDate: nowPb(),
		Callouts: []*pb_common.FittingCallout{
			{Number: 1, Note: "мерка", Kind: &dim, Points: fitPoints("0.10", "0.10", "1.40", "0.40")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "callouts[0].points[1].x")
}

// assertFittingGeometry — то, что обязано вернуться из ЛЮБОГО пути чтения.
func assertFittingGeometry(t *testing.T, cs []entity.FittingCallout) {
	t.Helper()
	require.Len(t, cs, 4)

	require.Equal(t, entity.AnnotationKindPin, cs[0].Kind)
	require.Empty(t, cs[0].Points, "у пина якорей нет: его единственная точка — маркер в pos_x/pos_y")

	require.Equal(t, entity.AnnotationKindDim, cs[1].Kind)
	require.Len(t, cs[1].Points, 2)
	require.Equal(t, "0.2", cs[1].Points[0].X.String())
	require.Equal(t, "0.6", cs[1].Points[1].X.String())
	require.Equal(t, entity.AnnotationColorRed, cs[1].Color)
	require.True(t, cs[1].Dashed)
	require.False(t, cs[1].Filled)
	require.Equal(t, "0.2", cs[1].PosX.Decimal.String(), "маркер остался на своём месте, отдельно от якорей")

	require.Equal(t, entity.AnnotationKindArc, cs[2].Kind)
	require.Len(t, cs[2].Points, 3)

	require.Equal(t, entity.AnnotationKindPolygon, cs[3].Kind)
	require.Len(t, cs[3].Points, 3)
	require.Equal(t, entity.AnnotationColorBlue, cs[3].Color)
	require.True(t, cs[3].Filled)
}

func fitDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func nowPb() *timestamppb.Timestamp { return timestamppb.New(time.Now().UTC()) }

// fitPoints строит якоря парами «x, y».
func fitPoints(xy ...string) []*pb_common.TechCardAnnotationPoint {
	out := make([]*pb_common.TechCardAnnotationPoint, 0, len(xy)/2)
	for i := 0; i+1 < len(xy); i += 2 {
		out = append(out, &pb_common.TechCardAnnotationPoint{X: fitDec(xy[i]), Y: fitDec(xy[i+1])})
	}
	return out
}

func fitCallout(number int32, note string, mediaID int, posX, posY string,
	kind pb_common.TechCardAnnotationKind, points []*pb_common.TechCardAnnotationPoint,
	color pb_common.TechCardAnnotationColor, dashed, filled bool) *pb_common.FittingCallout {
	k := kind
	return &pb_common.FittingCallout{
		Number: number, Note: note, MediaId: int32(mediaID),
		PosX: fitDec(posX), PosY: fitDec(posY),
		Kind: &k, Points: points, Color: color, Dashed: dashed, Filled: filled,
	}
}
