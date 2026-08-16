package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// Выноски на фотографиях шага: разбор с провода и обратно.
//
// ВАЛИДАЦИЯ ЗДЕСЬ, А НЕ В SQL, И НЕ В КЛИЕНТЕ. В SQL — потому что CHECK по JSON стреляет сырым
// 3819 и называет не ту колонку; в клиенте — потому что клиент не единственный вход (клон сезона
// строит payload сам). Отказ обязан назвать конкретную выноску на конкретной картинке конкретного
// шага, иначе технолог ищет её глазами среди тридцати.
//
// ЧИСЛО ТОЧЕК ПРОВЕРЯЕТСЯ ПО ВИДУ. Это единственное место, где закрытый словарь `kind` окупается
// целиком: скобка с одной точкой или мерка с тремя — не «странный ввод», а фигура, которую нечем
// нарисовать, и поймать её надо до записи.

const (
	maxOperationMediaPerStep    = 10
	maxAnnotationsPerMedia      = 30
	maxAnnotationTextRunes      = 500
	maxOperationMediaCaptionLen = 255
)

var annotationKindFromPb = map[pb_common.TechCardAnnotationKind]entity.TechCardAnnotationKind{
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN:     entity.AnnotationKindPin,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL:   entity.AnnotationKindLabel,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM:     entity.AnnotationKindDim,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET: entity.AnnotationKindBracket,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_MULTI:   entity.AnnotationKindMulti,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC:     entity.AnnotationKindArc,
}

var annotationKindToPb = map[entity.TechCardAnnotationKind]pb_common.TechCardAnnotationKind{
	entity.AnnotationKindPin:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
	entity.AnnotationKindLabel:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL,
	entity.AnnotationKindDim:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
	entity.AnnotationKindBracket: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET,
	entity.AnnotationKindMulti:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_MULTI,
	entity.AnnotationKindArc:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC,
}

var annotationColorFromPb = map[pb_common.TechCardAnnotationColor]entity.TechCardAnnotationColor{
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED:    entity.AnnotationColorRed,
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_BLUE:   entity.AnnotationColorBlue,
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_GREEN:  entity.AnnotationColorGreen,
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_ORANGE: entity.AnnotationColorOrange,
}

var annotationColorToPb = map[entity.TechCardAnnotationColor]pb_common.TechCardAnnotationColor{
	entity.AnnotationColorRed:    pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED,
	entity.AnnotationColorBlue:   pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_BLUE,
	entity.AnnotationColorGreen:  pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_GREEN,
	entity.AnnotationColorOrange: pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_ORANGE,
}

var zero = decimal.Zero
var one = decimal.NewFromInt(1)

// unitInterval разбирает нормализованную координату. Пусто = 0: у точки в левом верхнем углу
// координата законно нулевая, и отличать «не прислали» от «прислали ноль» здесь нечем и незачем.
func unitInterval(field string, d *pb_decimal.Decimal) (decimal.Decimal, error) {
	nd, err := nullDecimalFromPb(d)
	if err != nil {
		return decimal.Decimal{}, entity.NewFieldViolation(field, "invalid_decimal", "",
			"координата выноски — доля кадра от 0 до 1")
	}
	if !nd.Valid {
		return zero, nil
	}
	if nd.Decimal.LessThan(zero) || nd.Decimal.GreaterThan(one) {
		return decimal.Decimal{}, entity.NewFieldViolation(field, "out_of_frame", nd.Decimal.String(),
			"координата выноски — доля кадра от 0 до 1: точка вне снимка ничего не указывает")
	}
	return nd.Decimal, nil
}

// operationMediaFromPb разбирает картинки одного шага. `step` — путь для отказов («operations.3»).
func operationMediaFromPb(step string, in []*pb_common.TechCardOperationMedia) ([]entity.TechCardOperationMedia, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxOperationMediaPerStep {
		return nil, entity.NewFieldViolation(step+".media", "too_many", fmt.Sprint(len(in)),
			fmt.Sprintf("на шаг не больше %d фотографий: длинный филмстрип перестают листать", maxOperationMediaPerStep))
	}
	out := make([]entity.TechCardOperationMedia, 0, len(in))
	seenMedia := make(map[int]bool, len(in))
	for i, m := range in {
		path := fmt.Sprintf("%s.media[%d]", step, i)
		if m == nil {
			continue
		}
		mediaID := int(m.MediaId)
		if mediaID <= 0 {
			return nil, entity.NewFieldViolation(path+".media_id", "required", "",
				"картинка шага без медиа не значит ничего — выберите файл")
		}
		// Один и тот же снимок дважды на шаге — это два филмстрип-кадра, неразличимых глазом, и
		// два места, куда можно поставить противоречащие выноски.
		if seenMedia[mediaID] {
			return nil, entity.NewFieldViolation(path+".media_id", "duplicate", fmt.Sprint(mediaID),
				"эта фотография уже прикреплена к шагу: выноски ставятся на неё же")
		}
		seenMedia[mediaID] = true

		caption := strings.TrimSpace(m.Caption)
		if len([]rune(caption)) > maxOperationMediaCaptionLen {
			return nil, entity.NewFieldViolation(path+".caption", "too_long", "",
				fmt.Sprintf("подпись к фотографии — не длиннее %d знаков", maxOperationMediaCaptionLen))
		}
		anns, err := annotationsFromPb(path, m.Annotations)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardOperationMedia{
			MediaId:      mediaID,
			Caption:      nullStringFromPb(caption),
			// Позиция в РЕЗУЛЬТАТЕ, а не индекс входа: nil-элемент посреди списка оставил бы
			// дыру, и порядок в сущности разошёлся бы с тем, что запишет стор.
			DisplayOrder: len(out),
			Annotations:  anns,
		})
	}
	return out, nil
}

func annotationsFromPb(path string, in []*pb_common.TechCardAnnotation) ([]entity.TechCardAnnotation, error) {
	if len(in) == 0 {
		// Пустой список, а не nil: «выносок нет» — это факт картинки, и в JSON он обязан быть
		// массивом. nil заехал бы в колонку как `null` и заставил бы каждого читателя различать
		// два способа сказать одно.
		return []entity.TechCardAnnotation{}, nil
	}
	if len(in) > maxAnnotationsPerMedia {
		return nil, entity.NewFieldViolation(path+".annotations", "too_many", fmt.Sprint(len(in)),
			fmt.Sprintf("на снимок не больше %d выносок: дальше их не прочесть", maxAnnotationsPerMedia))
	}
	out := make([]entity.TechCardAnnotation, 0, len(in))
	for j, a := range in {
		ap := fmt.Sprintf("%s.annotations[%d]", path, j)
		if a == nil {
			continue
		}
		kind, ok := annotationKindFromPb[a.Kind]
		if !ok {
			return nil, entity.NewFieldViolation(ap+".kind", "required", a.Kind.String(),
				"вид выноски определяет и число точек, и что рисуется — без него фигуры нет")
		}
		min, max, _ := kind.PointsAllowed()
		if len(a.Points) < min || len(a.Points) > max {
			return nil, entity.NewFieldViolation(ap+".points", "wrong_count", fmt.Sprint(len(a.Points)),
				fmt.Sprintf("«%s» рисуется по %s точкам", kind, pointsRangeText(min, max)))
		}
		points := make([]entity.TechCardAnnotationPoint, 0, len(a.Points))
		for k, p := range a.Points {
			pp := fmt.Sprintf("%s.points[%d]", ap, k)
			if p == nil {
				return nil, entity.NewFieldViolation(pp, "required", "", "у выноски пропущена точка")
			}
			x, err := unitInterval(pp+".x", p.X)
			if err != nil {
				return nil, err
			}
			y, err := unitInterval(pp+".y", p.Y)
			if err != nil {
				return nil, err
			}
			points = append(points, entity.TechCardAnnotationPoint{X: x, Y: y})
		}
		text := strings.TrimSpace(a.Text)
		if len([]rune(text)) > maxAnnotationTextRunes {
			return nil, entity.NewFieldViolation(ap+".text", "too_long", "",
				fmt.Sprintf("текст выноски — не длиннее %d знаков; длинное объяснение живёт в заметке шага", maxAnnotationTextRunes))
		}
		lx, err := unitInterval(ap+".label_x", a.LabelX)
		if err != nil {
			return nil, err
		}
		ly, err := unitInterval(ap+".label_y", a.LabelY)
		if err != nil {
			return nil, err
		}
		// Неизвестный ненулевой цвет — ОТКАЗ, как и неизвестный вид: клиент новее сервера иначе
		// потерял бы различие молча. UNKNOWN — законное «чернильный», это не неизвестность.
		color := entity.TechCardAnnotationColor("")
		if a.Color != pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN {
			c, ok := annotationColorFromPb[a.Color]
			if !ok {
				return nil, entity.NewFieldViolation(ap+".color", "unknown_value", a.Color.String(),
					"цвет выноски — из закрытого списка: лист швеи печатают и чёрно-белым")
			}
			color = c
		}
		// ССЫЛКА НА ДЕТАЛЬ — СОВЕТУЮЩАЯ. Проверяется только ФОРМА ключа, не разрешимость: указание
		// ставят на снимок, пришедший с примерки, раньше, чем детали кроя родятся из чертежа, и
		// отказ сохранения всей карточки за неразрешённый ключ стоил бы дороже висящей ссылки.
		// Клиент показывает «деталь удалена» и даёт перевыбрать — ровно как у входов операции.
		pieceKey := strings.TrimSpace(a.PieceLineKey)
		if err := validatePatternLineKey(pieceKey, ap+".piece_line_key"); err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardAnnotation{
			Kind:         kind,
			Points:       points,
			Text:         text,
			LabelX:       lx,
			LabelY:       ly,
			Color:        color,
			PieceLineKey: pieceKey,
		})
	}
	return out, nil
}

// --- геометрия КАРТОЧНОЙ выноски ---------------------------------------------------------------
//
// Тот же словарь видов и те же правила числа точек, что у выноски на снимке шага, — с одним
// отличием, и оно принципиально: у карточной выноски НУМЕРОВАННЫЙ МАРКЕР живёт отдельно, в
// pos_x/pos_y, потому что на него ссылаются номером деталь, операция и дефект. Поэтому `points`
// здесь держит ТОЛЬКО якоря фигуры, и у пина их ноль, а не один: единственная точка пина — это и
// есть маркер, и дублировать её в якорях значило бы завести два места для одной координаты,
// которые однажды разойдутся.

// calloutKindOrPin читает вид ХРАНИМОЙ выноски. Пусто = pin: колонка появилась в 0309 с этим
// дефолтом, но ряд читателей (архивные снапшоты релизов, клон сезона) отдают сущность в обход
// колонки, и там поле остаётся нулевой строкой.
func calloutKindOrPin(k entity.TechCardAnnotationKind) entity.TechCardAnnotationKind {
	if k == "" {
		return entity.AnnotationKindPin
	}
	return k
}

// calloutPointsAllowed — сколько ЯКОРЕЙ у карточной выноски этого вида (маркер не в счёт).
func calloutPointsAllowed(k entity.TechCardAnnotationKind) (min, max int, ok bool) {
	if k == entity.AnnotationKindPin {
		return 0, 0, true
	}
	return k.PointsAllowed()
}

// calloutGeometryFromPb разбирает вид, якоря и цвет карточной выноски. `path` — путь для отказов
// («callouts[3]»). Отсутствие вида читается как PIN: весь массив живых карточек написан до этого
// поля и приезжает с нулевым энумом, и трактовать его отказом значило бы отвергнуть каждую.
func calloutGeometryFromPb(
	path string,
	kindPb *pb_common.TechCardAnnotationKind,
	pointsPb []*pb_common.TechCardAnnotationPoint,
	colorPb pb_common.TechCardAnnotationColor,
) (entity.TechCardAnnotationKind, []entity.TechCardAnnotationPoint, entity.TechCardAnnotationColor, error) {
	kind := entity.AnnotationKindPin
	if kindPb != nil && *kindPb != pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_UNKNOWN {
		k, ok := annotationKindFromPb[*kindPb]
		if !ok {
			return "", nil, "", entity.NewFieldViolation(path+".kind", "unknown_value", kindPb.String(),
				"вид указания — из закрытого списка: вид определяет и число точек, и что рисуется")
		}
		kind = k
	}
	min, max, _ := calloutPointsAllowed(kind)
	if len(pointsPb) < min || len(pointsPb) > max {
		return "", nil, "", entity.NewFieldViolation(path+".points", "wrong_count", fmt.Sprint(len(pointsPb)),
			fmt.Sprintf("«%s» на эскизе рисуется по %s якорям (номерной маркер стоит отдельно)", kind, pointsRangeText(min, max)))
	}
	points := make([]entity.TechCardAnnotationPoint, 0, len(pointsPb))
	for k, p := range pointsPb {
		pp := fmt.Sprintf("%s.points[%d]", path, k)
		if p == nil {
			return "", nil, "", entity.NewFieldViolation(pp, "required", "", "у указания пропущен якорь")
		}
		x, err := unitInterval(pp+".x", p.X)
		if err != nil {
			return "", nil, "", err
		}
		y, err := unitInterval(pp+".y", p.Y)
		if err != nil {
			return "", nil, "", err
		}
		points = append(points, entity.TechCardAnnotationPoint{X: x, Y: y})
	}
	color := entity.TechCardAnnotationColor("")
	if colorPb != pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN {
		c, ok := annotationColorFromPb[colorPb]
		if !ok {
			return "", nil, "", entity.NewFieldViolation(path+".color", "unknown_value", colorPb.String(),
				"цвет указания — из закрытого списка: лист печатают и чёрно-белым")
		}
		color = c
	}
	return kind, points, color, nil
}

// calloutKindPbPtr отдаёт вид ХРАНИМОЙ выноски присутствующим полем. Присутствует всегда: чтение
// не бывает «умолчавшим», а круглый рейс нового клиента обязан вернуть то, что прочитал.
func calloutKindPbPtr(k entity.TechCardAnnotationKind) *pb_common.TechCardAnnotationKind {
	v := annotationKindToPb[calloutKindOrPin(k)]
	return &v
}

// CarryOmittedCalloutGeometry переносит хранимую геометрию указаний в payload, который про неё не
// говорил. Сопоставление по НОМЕРУ выноски: номер — та самая идентичность, которой на выноску
// ссылаются деталь, операция и дефект, и другой у неё нет.
//
// Третья нога контракта присутствия, ровно как carryOmittedPieceCutSymmetryFrom: без неё подпись
// DESIGN, поставленная из вкладки со старым бандлом, хешировала бы «просто точки» поверх карточки,
// где нарисованы мерки, — и рождалась бы устаревшей навсегда.
func CarryOmittedCalloutGeometry(stored *entity.TechCard, tc *entity.TechCardInsert) {
	if stored == nil || tc == nil || len(tc.Callouts) == 0 {
		return
	}
	byNumber := make(map[int]entity.TechCardCallout, len(stored.Callouts))
	for _, c := range stored.Callouts {
		// Первый выигрывает: номера уникальны по смыслу, а дубль в хранимом — испорченные данные,
		// на которых перенос обязан быть детерминированным.
		if _, seen := byNumber[c.Number]; !seen {
			byNumber[c.Number] = c
		}
	}
	for i := range tc.Callouts {
		if !tc.Callouts[i].KindOmitted {
			continue
		}
		prev, ok := byNumber[tc.Callouts[i].Number]
		if !ok {
			continue
		}
		tc.Callouts[i].Kind = calloutKindOrPin(prev.Kind)
		tc.Callouts[i].Points = prev.Points
		tc.Callouts[i].Color = prev.Color
	}
}

// calloutPointsToPb — обратный ход якорей.
func calloutPointsToPb(in []entity.TechCardAnnotationPoint) []*pb_common.TechCardAnnotationPoint {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardAnnotationPoint, 0, len(in))
	for _, p := range in {
		out = append(out, &pb_common.TechCardAnnotationPoint{
			X: pbDecimalFromDecimal(p.X),
			Y: pbDecimalFromDecimal(p.Y),
		})
	}
	return out
}

func pointsRangeText(min, max int) string {
	if min == max {
		return fmt.Sprintf("%d", min)
	}
	return fmt.Sprintf("%d–%d", min, max)
}

// operationMediaToPb — обратный ход. Порядок списка это порядок показа: сохраняем как есть.
func operationMediaToPb(in []entity.TechCardOperationMedia) []*pb_common.TechCardOperationMedia {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardOperationMedia, 0, len(in))
	for _, m := range in {
		anns := make([]*pb_common.TechCardAnnotation, 0, len(m.Annotations))
		for _, a := range m.Annotations {
			points := make([]*pb_common.TechCardAnnotationPoint, 0, len(a.Points))
			for _, p := range a.Points {
				points = append(points, &pb_common.TechCardAnnotationPoint{
					X: pbDecimalFromDecimal(p.X),
					Y: pbDecimalFromDecimal(p.Y),
				})
			}
			anns = append(anns, &pb_common.TechCardAnnotation{
				Kind:         annotationKindToPb[a.Kind],
				Points:       points,
				Text:         a.Text,
				LabelX:       pbDecimalFromDecimal(a.LabelX),
				LabelY:       pbDecimalFromDecimal(a.LabelY),
				Color:        annotationColorToPb[a.Color],
				PieceLineKey: a.PieceLineKey,
			})
		}
		out = append(out, &pb_common.TechCardOperationMedia{
			MediaId:     int32(m.MediaId),
			Caption:     m.Caption.String,
			Annotations: anns,
		})
	}
	return out
}

// resolvedOperationMedia отдаёт словарь операционных снимков карточки. Kind и caption здесь пусты
// намеренно: у операционного снимка своя подпись живёт на строке операции, а вид эскиза
// (front/back/detail) к нему неприменим — это фотография узла, а не проекция изделия.
func resolvedOperationMedia(tc *entity.TechCard) []*pb_common.TechCardMediaFull {
	if len(tc.ResolvedOperationMedia) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardMediaFull, 0, len(tc.ResolvedOperationMedia))
	for i := range tc.ResolvedOperationMedia {
		out = append(out, &pb_common.TechCardMediaFull{
			Media: ConvertEntityToCommonMedia(&tc.ResolvedOperationMedia[i].Media),
		})
	}
	return out
}
