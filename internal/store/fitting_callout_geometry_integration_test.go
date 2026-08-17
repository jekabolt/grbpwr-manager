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
	skipUnlessLocalDB(t)

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
	techCardID = newFitGeomTechCard(ctx, t, s, suffix)

	// --- круговой рейс всех видов ---------------------------------------------------------------

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
			fitCallout(5, "подпись у горловины", mediaID, "0.15", "0.05",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL,
				fitPoints("0.15", "0.06"),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN, false, false),
			fitCallout(6, "по этому участку", mediaID, "0.35", "0.65",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET,
				fitPoints("0.30", "0.60", "0.50", "0.60"),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN, false, false),
			fitCallout(7, "закрепки ×3", mediaID, "0.55", "0.45",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_MULTI,
				fitPoints("0.50", "0.40", "0.55", "0.45", "0.60", "0.50"),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN, false, false),
			// СВОБОДНЫЙ СЛЕД НА ПОТОЛКЕ ДЛИНЫ. Это самая крупная JSON-нагрузка, которую колонка
			// вообще может получить, и единственный вид, у которого размер записи — предмет
			// отдельного решения (клиент прореживает след ДО отправки, 200 — потолок, а не норма).
			fitCallout(8, "обвёл рукой", mediaID, "0.80", "0.20",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_INK,
				inkTrail(200),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_WHITE, true, false),
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
	require.Len(t, readBack, 8)
	for _, c := range readBack {
		require.NotNil(t, c.Kind, "вид на чтении обязан присутствовать: иначе круглый рейс молчит про геометрию")
	}
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM, readBack[1].GetKind())
	require.Len(t, readBack[1].GetPoints(), 2)
	require.Equal(t, pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED, readBack[1].GetColor())
	require.True(t, readBack[1].GetDashed())
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON, readBack[3].GetKind())
	require.True(t, readBack[3].GetFilled())
	require.Len(t, readBack[7].GetPoints(), 200, "след обязан вернуться целиком, а не обрезанным колонкой")

	// --- КАК ЭТО ЛЕЖИТ В КОЛОНКЕ ----------------------------------------------------------------
	//
	// require.Empty(points) здесь ничего не проверяет: он одинаково истинен и для NULL, и для «[]».
	// Утверждение про запись делается по СЫРОМУ значению колонки.
	require.Equal(t, "[]", calloutPointsColumn(ctx, t, fittingID, 1),
		"пин пишется пустым массивом: якорей у него нет, и это факт, а не отсутствие сведений")

	// --- строка, записанная ДО 0319 --------------------------------------------------------------
	//
	// Не UPDATE'ом по своим же колонкам (это проверяло бы собственный UPDATE), а INSERT'ом ровно с
	// тем списком колонок, который знал до-0319 бинарь. Так же выглядит и запись после ОТКАТА
	// бинаря: новые колонки берут дефолты, points остаётся NULL.
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO fitting_callout (fitting_id, callout_number, note, media_id, pos_x, pos_y, display_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		fittingID, 9, "написано до 0319", mediaID, "0.5", "0.5", 99)
	require.NoError(t, err)

	var legacyPoints sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT points FROM fitting_callout WHERE fitting_id = ? AND callout_number = 9`,
		fittingID).Scan(&legacyPoints))
	require.False(t, legacyPoints.Valid, "у строки, записанной без колонки points, она обязана быть NULL")

	legacy, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	require.Len(t, legacy.Callouts, 9)
	old := legacy.Callouts[8]
	require.Equal(t, 9, old.Number)
	require.Nil(t, old.Points, "NULL в колонке читается как «якорей никто не ставил», а не как ошибка")
	require.Equal(t, entity.AnnotationKindPin, old.Kind, "дефолт колонки — настоящий вид такой выноски")
	legacyPb := dto.ConvertEntityFittingToPb(legacy).GetFitting().GetCallouts()
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN, legacyPb[8].GetKind(),
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

	// И МАРКЕР защищён тем же пределом, что его собственные якоря: показатель степени в pos_x стоит
	// ровно столько же, сколько в points[0].x, и раньше проверялся слабее.
	_, err = dto.ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
		TechCardId:  int32(techCardID),
		FittingDate: nowPb(),
		Callouts: []*pb_common.FittingCallout{
			{Number: 1, Note: "маркер", PosX: &pb_decimal.Decimal{Value: "0.5e-500000"}},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "callouts[0].pos_x")
}

// Перенос умолчанного: ЧТО он обязан вернуть и, главное, чего не имеет права выдумать.
//
// Стирание — не единственный исход молчания, и не худший. Номера выдаются как max+1, поэтому
// удаление выноски сдвигает нумерацию или освобождает число под новую: перенос по одному лишь
// номеру записал бы на чужую записку чужую фигуру. Подписи у примерки нет, подмена осталась бы
// тихой — поэтому тождество здесь узкое (номер + снимок + маркер), и половина этих утверждений про
// то, что перенос НЕ СРАБОТАЛ.
func TestFittingCalloutGeometryCarry(t *testing.T) {
	skipUnlessLocalDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	mediaA := insertTestMedia(t, "fitcarry-a-"+suffix)
	mediaB := insertTestMedia(t, "fitcarry-b-"+suffix)

	var techCardID, fittingID int
	defer func() {
		bg := context.Background()
		if fittingID != 0 {
			_ = s.Fittings().DeleteFitting(bg, fittingID)
		}
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(bg, techCardID)
		}
		for _, id := range []int{mediaA, mediaB} {
			_, _ = testDB.ExecContext(bg, `DELETE FROM media WHERE id = ?`, id)
		}
	}()
	techCardID = newFitGeomTechCard(ctx, t, s, suffix)

	// ФИГУРА СТОИТ НА МЛАДШЕМ НОМЕРЕ — это не оформление, а условие проверяемости. Будь мерка #2,
	// перенос по одному лишь номеру на сценариях ниже просто НЕ НАХОДИЛ БЫ ничего, и тесты зеленели
	// бы, не различая старое поведение и новое. С меркой на #1 каждое несовпадение тождества — это
	// номер, который сопоставился бы, и фигура, которая приехала бы на чужую записку.
	base := &pb_common.FittingInsert{
		TechCardId: int32(techCardID), FittingDate: nowPb(),
		Status:   pb_common.FittingStatus_FITTING_STATUS_PLANNED,
		Verdict:  pb_common.FittingVerdict_FITTING_VERDICT_PENDING,
		MediaIds: []int32{int32(mediaA)},
		Callouts: []*pb_common.FittingCallout{
			fitCallout(1, "по груди уже", mediaA, "0.20", "0.30",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
				fitPoints("0.20", "0.50", "0.60", "0.50"),
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED, true, false),
			fitCallout(2, "плечо жмёт", mediaA, "0.10", "0.10",
				pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN, nil,
				pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN, false, false),
		},
	}
	fi, err := dto.ConvertPbFittingInsertToEntity(base)
	require.NoError(t, err)
	fittingID, err = s.Fittings().AddFitting(ctx, fi)
	require.NoError(t, err)
	stored, err := s.Fittings().GetFittingById(ctx, fittingID)
	require.NoError(t, err)
	require.Equal(t, entity.AnnotationKindDim, stored.Callouts[0].Kind)

	// carry прогоняет payload через dto и отдаёт результат переноса на ХРАНИМОЕ, прочитанное из
	// MySQL. Читать из базы принципиально: колонка DECIMAL(5,4) возвращает «0.2000», а с провода
	// приезжает «0.2», и сравнение представлений вместо чисел отменило бы перенос вообще всегда.
	carry := func(t *testing.T, cs ...*pb_common.FittingCallout) []entity.FittingCallout {
		t.Helper()
		payload, err := dto.ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
			TechCardId: int32(techCardID), FittingDate: nowPb(), MediaIds: []int32{int32(mediaA)},
			Callouts: cs,
		})
		require.NoError(t, err)
		dto.CarryOmittedFittingCalloutGeometry(stored, payload)
		return payload.Callouts
	}
	// silent — выноска БЕЗ единого слова про фигуру, ровно как её шлёт старый бандл.
	silent := func(number int32, note string, media int, x, y string) *pb_common.FittingCallout {
		return &pb_common.FittingCallout{
			Number: number, Note: note, MediaId: int32(media),
			PosX: fitDec(x), PosY: fitDec(y),
		}
	}

	t.Run("та же выноска — геометрия возвращается", func(t *testing.T) {
		got := carry(t, silent(1, "по груди уже", mediaA, "0.20", "0.30"),
			silent(2, "плечо жмёт", mediaA, "0.10", "0.10"))
		require.Equal(t, entity.AnnotationKindDim, got[0].Kind)
		require.Len(t, got[0].Points, 2)
		require.Equal(t, entity.AnnotationColorRed, got[0].Color)
		require.True(t, got[0].Dashed)
		require.Equal(t, entity.AnnotationKindPin, got[1].Kind)
	})

	t.Run("перенумерация не подменяет фигуру", func(t *testing.T) {
		// Удалили мерку (#1), пин перенумеровался в #1 — со СВОИМИ координатами. По одному номеру
		// на записку «плечо жмёт» записалась бы размерная линия с двумя чужими якорями и пунктиром,
		// и человек увидел бы мерку там, где ничего не рисовал.
		got := carry(t, silent(1, "плечо жмёт", mediaA, "0.10", "0.10"))
		require.Equal(t, entity.AnnotationKindPin, got[0].Kind,
			"номер совпал с чужой выноской, маркер — нет: фигуру выдумывать нельзя")
		require.Empty(t, got[0].Points)
		require.False(t, got[0].Dashed)
	})

	t.Run("номер переиспользован новой выноской", func(t *testing.T) {
		// Мерку удалили, освободившийся номер (max+1 их и переиспользует) достался новой заметке.
		got := carry(t, silent(1, "низ волной", mediaA, "0.90", "0.90"))
		require.Equal(t, entity.AnnotationKindPin, got[0].Kind,
			"новая заметка не наследует фигуру той, чей номер ей достался")
		require.Empty(t, got[0].Points)
	})

	t.Run("тот же номер и маркер, но другой снимок", func(t *testing.T) {
		got := carry(t, silent(1, "по груди уже", mediaB, "0.20", "0.30"))
		require.Equal(t, entity.AnnotationKindPin, got[0].Kind,
			"геометрия принадлежит маркеру на КОНКРЕТНОМ снимке")
		require.Empty(t, got[0].Points)
	})

	t.Run("дубли номеров в присланном", func(t *testing.T) {
		// Уникальности номера нет ни в схеме, ни в проверках. Обе выноски тождественны хранимой
		// мерке, то есть обе на неё претендуют — и без явного правила ОБЕ получили бы её фигуру,
		// хотя нарисована она была один раз.
		got := carry(t,
			silent(1, "по груди уже", mediaA, "0.20", "0.30"),
			silent(1, "по груди уже", mediaA, "0.20", "0.30"))
		for i := range got {
			require.Equal(t, entity.AnnotationKindPin, got[i].Kind,
				"на один номер претендуют двое — фигуру не получает никто")
			require.Empty(t, got[i].Points)
		}
	})

	t.Run("смешанный запрос", func(t *testing.T) {
		// Часть выносок со словом о виде, часть без — обычное состояние во время выката клиента.
		poly := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON
		spoken := &pb_common.FittingCallout{
			Number: 2, Note: "плечо жмёт", MediaId: int32(mediaA),
			PosX: fitDec("0.10"), PosY: fitDec("0.10"), Kind: &poly,
			Points: fitPoints("0.10", "0.10", "0.40", "0.10", "0.40", "0.40"),
			Filled: true,
		}
		got := carry(t, silent(1, "по груди уже", mediaA, "0.20", "0.30"), spoken)
		require.Equal(t, entity.AnnotationKindDim, got[0].Kind, "умолчавший сосед геометрию получает")
		require.Len(t, got[0].Points, 2)
		require.Equal(t, entity.AnnotationKindPolygon, got[1].Kind, "а явное слово переносом не затирается")
		require.True(t, got[1].Filled)
	})

	t.Run("пустой список выносок при непустом хранимом", func(t *testing.T) {
		payload, err := dto.ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
			TechCardId: int32(techCardID), FittingDate: nowPb(),
		})
		require.NoError(t, err)
		dto.CarryOmittedFittingCalloutGeometry(stored, payload)
		require.Empty(t, payload.Callouts,
			"«выносок нет» — это осознанное удаление всех, а не молчание: воскрешать их перенос не имеет права")
	})

	t.Run("явный PIN схлопывает фигуру и уносит пунктир", func(t *testing.T) {
		pin := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN
		payload, err := dto.ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
			TechCardId: int32(techCardID), FittingDate: nowPb(),
			Status:   pb_common.FittingStatus_FITTING_STATUS_DONE,
			Verdict:  pb_common.FittingVerdict_FITTING_VERDICT_NEEDS_REWORK,
			MediaIds: []int32{int32(mediaA)},
			Callouts: []*pb_common.FittingCallout{{
				Number: 1, Note: "по груди уже", MediaId: int32(mediaA),
				PosX: fitDec("0.20"), PosY: fitDec("0.30"), Kind: &pin,
			}},
		})
		require.NoError(t, err)
		require.False(t, payload.Callouts[0].KindOmitted, "явный PIN — не молчание")
		dto.CarryOmittedFittingCalloutGeometry(stored, payload)
		require.Equal(t, entity.AnnotationKindPin, payload.Callouts[0].Kind,
			"перенос не имеет права затереть явное слово человека")

		// И доезжает до колонок: круг замыкается записью, а не только сущностью.
		require.NoError(t, s.Fittings().UpdateFitting(ctx, fittingID, payload, stored.LockVersion))
		flat, err := s.Fittings().GetFittingById(ctx, fittingID)
		require.NoError(t, err)
		require.Len(t, flat.Callouts, 1)
		require.Equal(t, entity.AnnotationKindPin, flat.Callouts[0].Kind)
		require.False(t, flat.Callouts[0].Dashed, "пунктир — часть той же группы, он уходит вместе с фигурой")
		require.Equal(t, "[]", calloutPointsColumn(ctx, t, fittingID, 1),
			"схлопнутая мерка обязана и в колонке остаться без якорей")
	})
}

// skipUnlessLocalDB не даёт интеграционному тесту уехать в настроенную (боевую) базу.
func skipUnlessLocalDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
}

func newFitGeomTechCard(ctx context.Context, t *testing.T, s *MYSQLStore, suffix string) int {
	t.Helper()
	id, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "FIT-GEOM-" + suffix[len(suffix)-8:], Valid: true},
		Name:            "fitting callout geometry",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
	})
	require.NoError(t, err)
	return id
}

// calloutPointsColumn отдаёт СЫРОЕ значение колонки points — «NULL» строкой, когда её нет.
func calloutPointsColumn(ctx context.Context, t *testing.T, fittingID, number int) string {
	t.Helper()
	var raw sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT points FROM fitting_callout WHERE fitting_id = ? AND callout_number = ?`,
		fittingID, number).Scan(&raw))
	if !raw.Valid {
		return "NULL"
	}
	return raw.String
}

// assertFittingGeometry — то, что обязано вернуться из ЛЮБОГО пути чтения.
func assertFittingGeometry(t *testing.T, cs []entity.FittingCallout) {
	t.Helper()
	require.Len(t, cs, 8)

	require.Equal(t, entity.AnnotationKindPin, cs[0].Kind)
	require.Empty(t, cs[0].Points, "у пина якорей нет: его единственная точка — маркер в pos_x/pos_y")

	require.Equal(t, entity.AnnotationKindDim, cs[1].Kind)
	require.Len(t, cs[1].Points, 2)
	require.Equal(t, "0.2", cs[1].Points[0].X.String())
	require.Equal(t, "0.6", cs[1].Points[1].X.String())
	require.Equal(t, entity.AnnotationColorRed, cs[1].Color)
	require.True(t, cs[1].Dashed)
	require.False(t, cs[1].Filled)
	require.True(t, cs[1].PosX.Decimal.Equal(cs[1].Points[0].X),
		"маркер остался на своём месте, отдельно от якорей")

	require.Equal(t, entity.AnnotationKindArc, cs[2].Kind)
	require.Len(t, cs[2].Points, 3)

	require.Equal(t, entity.AnnotationKindPolygon, cs[3].Kind)
	require.Len(t, cs[3].Points, 3)
	require.Equal(t, entity.AnnotationColorBlue, cs[3].Color)
	require.True(t, cs[3].Filled)

	require.Equal(t, entity.AnnotationKindLabel, cs[4].Kind)
	require.Len(t, cs[4].Points, 1)

	require.Equal(t, entity.AnnotationKindBracket, cs[5].Kind)
	require.Len(t, cs[5].Points, 2)

	require.Equal(t, entity.AnnotationKindMulti, cs[6].Kind)
	require.Len(t, cs[6].Points, 3)

	require.Equal(t, entity.AnnotationKindInk, cs[7].Kind)
	require.Len(t, cs[7].Points, 200, "след на потолке длины обязан вернуться целиком")
	require.Equal(t, entity.AnnotationColorWhite, cs[7].Color)
	require.True(t, cs[7].Dashed)
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

// inkTrail — свободный след из n различимых точек (клиент прореживает до трёх знаков).
func inkTrail(n int) []*pb_common.TechCardAnnotationPoint {
	out := make([]*pb_common.TechCardAnnotationPoint, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &pb_common.TechCardAnnotationPoint{
			X: fitDec(fmt.Sprintf("0.%03d", i)),
			Y: fitDec(fmt.Sprintf("0.%03d", (i*7)%1000)),
		})
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
