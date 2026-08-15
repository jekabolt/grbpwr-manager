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
}

var annotationKindToPb = map[entity.TechCardAnnotationKind]pb_common.TechCardAnnotationKind{
	entity.AnnotationKindPin:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
	entity.AnnotationKindLabel:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL,
	entity.AnnotationKindDim:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
	entity.AnnotationKindBracket: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET,
	entity.AnnotationKindMulti:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_MULTI,
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
		path := fmt.Sprintf("%s.media.%d", step, i)
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
			DisplayOrder: i,
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
		ap := fmt.Sprintf("%s.annotations.%d", path, j)
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
			pp := fmt.Sprintf("%s.points.%d", ap, k)
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
		color := annotationColorFromPb[a.Color] // отсутствие в словаре = чернильный
		out = append(out, entity.TechCardAnnotation{
			Kind:   kind,
			Points: points,
			Text:   text,
			LabelX: lx,
			LabelY: ly,
			Color:  color,
		})
	}
	return out, nil
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
				Kind:   annotationKindToPb[a.Kind],
				Points: points,
				Text:   a.Text,
				LabelX: pbDecimalFromDecimal(a.LabelX),
				LabelY: pbDecimalFromDecimal(a.LabelY),
				Color:  annotationColorToPb[a.Color],
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
