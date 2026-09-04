package dto

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
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

// maxCoordinateScale — сколько знаков после запятой имеет право нести координата выноски.
//
// ПРЕДЕЛ НЕ КОСМЕТИЧЕСКИЙ, А ЗАЩИТНЫЙ, и без него диапазона 0..1 недостаточно. На проводе
// координата это СТРОКА (pb_decimal.Decimal), а строка законно записывается показателем степени, и
// одиннадцать байт разворачиваются во что угодно. Замерено на этом же shopspring/decimal:
// «0.5e-500000» — 11 байт с провода и 500 005 байт в JSON-колонке; «1E-10000000» — 1.2 с процессора
// и 44 МиБ ЕЩЁ ДО хранения; «1E+10000000» — 3.3 с и 190 МиБ, причём на пути, который и так
// заканчивается отказом. Дорого именно СРАВНЕНИЕ с границами кадра: оно выравнивает экспоненты и
// материализует все нули. Потолки выносок (30 на снимок × 200 точек следа = 12 000 координат)
// превращают это в часы процессора при теле запроса в двести килобайт — то есть предел размера
// сообщения сюда не достаёт даже близко.
//
// Шесть знаков — с большим запасом: примерка округляет до трёх (toFixed(3)), холст до четырёх, а
// доля кадра точнее шестого знака это доля пикселя на снимке, которого не бывает.
const maxCoordinateScale = 6

var annotationKindFromPb = map[pb_common.TechCardAnnotationKind]entity.TechCardAnnotationKind{
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN:     entity.AnnotationKindPin,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL:   entity.AnnotationKindLabel,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM:     entity.AnnotationKindDim,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET: entity.AnnotationKindBracket,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_MULTI:   entity.AnnotationKindMulti,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC:     entity.AnnotationKindArc,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON: entity.AnnotationKindPolygon,
	pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_INK:     entity.AnnotationKindInk,
}

var annotationKindToPb = map[entity.TechCardAnnotationKind]pb_common.TechCardAnnotationKind{
	entity.AnnotationKindPin:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN,
	entity.AnnotationKindLabel:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL,
	entity.AnnotationKindDim:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
	entity.AnnotationKindBracket: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET,
	entity.AnnotationKindMulti:   pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_MULTI,
	entity.AnnotationKindArc:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC,
	entity.AnnotationKindPolygon: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON,
	entity.AnnotationKindInk:     pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_INK,
}

// annotationCapsFromPb / annotationCapsToPb — наконечник линии в обе стороны. UNSPECIFIED ↔ пусто:
// «не выбран» это НЕ «без наконечников», а «по виду» (dim → засечки, bracket → скобки, arc → без),
// и оба представления обязаны означать ровно это, иначе уже нарисованная мерка, перечитанная и
// записанная обратно, сменила бы байты и объявила подпись секции протухшей.
var annotationCapsFromPb = map[pb_common.TechCardAnnotationCaps]entity.TechCardAnnotationCaps{
	pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_TICK:    entity.AnnotationCapsTick,
	pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_BRACKET: entity.AnnotationCapsBracket,
	pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_BULLET:  entity.AnnotationCapsBullet,
	pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_ARROW:   entity.AnnotationCapsArrow,
}

var annotationCapsToPb = map[entity.TechCardAnnotationCaps]pb_common.TechCardAnnotationCaps{
	entity.AnnotationCapsTick:    pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_TICK,
	entity.AnnotationCapsBracket: pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_BRACKET,
	entity.AnnotationCapsBullet:  pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_BULLET,
	entity.AnnotationCapsArrow:   pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_ARROW,
}

// capsFromPb — наконечник с провода, приведённый к тому, что он может значить у этого вида.
// Неизвестное значение энума ОТВЕРГАЕТСЯ, а не приводится: пришедший из будущего наконечник это
// сведения, которых сервер не понимает, и молча нарисовать вместо стрелки засечку значило бы
// отдать в цех другое указание. Осмысленный, но неуместный (наконечник у зоны) — приводится, ровно
// как Dashed у точки.
func capsFromPb(path string, k entity.TechCardAnnotationKind, c pb_common.TechCardAnnotationCaps) (entity.TechCardAnnotationCaps, error) {
	if c == pb_common.TechCardAnnotationCaps_TECH_CARD_ANNOTATION_CAPS_UNSPECIFIED {
		return "", nil
	}
	caps, ok := annotationCapsFromPb[c]
	if !ok {
		return "", entity.NewFieldViolation(path+".caps", "unknown_value", c.String(),
			"a line cap comes from a closed list: tick, bracket, bullet or arrow")
	}
	return entity.NormalizeAnnotationCaps(k, caps), nil
}

var annotationColorFromPb = map[pb_common.TechCardAnnotationColor]entity.TechCardAnnotationColor{
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED:    entity.AnnotationColorRed,
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_BLUE:   entity.AnnotationColorBlue,
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_GREEN:  entity.AnnotationColorGreen,
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_ORANGE: entity.AnnotationColorOrange,
	pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_WHITE:  entity.AnnotationColorWhite,
}

var annotationColorToPb = map[entity.TechCardAnnotationColor]pb_common.TechCardAnnotationColor{
	entity.AnnotationColorRed:    pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED,
	entity.AnnotationColorBlue:   pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_BLUE,
	entity.AnnotationColorGreen:  pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_GREEN,
	entity.AnnotationColorOrange: pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_ORANGE,
	entity.AnnotationColorWhite:  pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_WHITE,
}

var zero = decimal.Zero
var one = decimal.NewFromInt(1)

// unitIntervalNull разбирает нормализованную координату, СОХРАНЯЯ отсутствие: у якоря фигуры пусто
// означает ноль, а у маркера выноски — NULL в колонке, и схлопывать одно в другое нельзя.
//
// ОДНА ОХРАНЯЕМАЯ ПРОВЕРКА НА ОБЕ КООРДИНАТЫ ВЫНОСКИ. Раньше якоря шли сюда, а pos_x/pos_y ТОЙ ЖЕ
// выноски — через безохранный validateUnitInterval, который сравнивает decimal с границами напрямую.
// Защита от показателя степени (см. maxCoordinateScale) обходилась соседним полем: «1e-10000000» в
// координате МАРКЕРА давало ровно тот рескейл, ради которого предел и заводился. Дыра была и на
// карточной выноске; обе координаты обеих выносок теперь читаются здесь.
func unitIntervalNull(field string, d *pb_decimal.Decimal) (decimal.NullDecimal, error) {
	var none decimal.NullDecimal
	nd, err := nullDecimalFromPb(d)
	if err != nil {
		return none, entity.NewFieldViolation(field, "invalid_decimal", "",
			"a callout coordinate is a fraction of the frame from 0 to 1")
	}
	if !nd.Valid {
		return none, nil
	}
	// ПОРЯДОК ЗДЕСЬ — ЧАСТЬ ЗАЩИТЫ. Экспонента проверяется ПОСЛЕ разбора строки, но ДО сравнения с
	// границами кадра: разбор дёшев (он кладёт коэффициент и показатель, ничего не считая), а
	// дорого именно сравнение. Округлить вместо отказа НЕЛЬЗЯ — rescale на показателе -10000000 и
	// есть тот самый взрыв, от которого мы защищаемся.
	if exp := nd.Decimal.Exponent(); exp < -maxCoordinateScale {
		return none, entity.NewFieldViolation(field, "too_precise", coordSample(d.Value),
			fmt.Sprintf("a callout coordinate is a fraction of the frame, at most %d decimal places: the photo resolves nothing finer", maxCoordinateScale))
	} else if exp > 0 {
		// Показатель степени сам по себе не преступление, но координата, записанная им, у нас
		// всегда либо ноль, либо вне кадра — а стоит такое сравнение дороже всей конвертации.
		return none, entity.NewFieldViolation(field, "bad_scale", coordSample(d.Value),
			"a callout coordinate is written as an ordinary fraction from 0 to 1, not in exponent notation")
	}
	if nd.Decimal.LessThan(zero) || nd.Decimal.GreaterThan(one) {
		return none, entity.NewFieldViolation(field, "out_of_frame", nd.Decimal.String(),
			"a callout coordinate is a fraction of the frame from 0 to 1: a point outside the photo points at nothing")
	}
	return nd, nil
}

// unitInterval разбирает нормализованную координату ЯКОРЯ. Пусто = 0: у точки в левом верхнем углу
// координата законно нулевая, и отличать «не прислали» от «прислали ноль» здесь нечем и незачем.
func unitInterval(field string, d *pb_decimal.Decimal) (decimal.Decimal, error) {
	nd, err := unitIntervalNull(field, d)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if !nd.Valid {
		return zero, nil
	}
	return nd.Decimal, nil
}

// coordSample обрезает СЫРОЕ значение координаты для текста отказа.
//
// Печатать его целиком нельзя, и это не забота о читаемости: ровно та строка, из-за которой отказ,
// и раздувается — `String()` у неё развернул бы полмегабайта нулей прямо в сообщение об ошибке,
// то есть отказ стоил бы столько же, сколько атака, от которой он защищает.
func coordSample(raw string) string {
	const maxSample = 32
	if len(raw) <= maxSample {
		return raw
	}
	return raw[:maxSample] + "…"
}

// operationMediaFromPb разбирает картинки одного шага. `step` — путь для отказов («operations.3»).
func operationMediaFromPb(step string, in []*pb_common.TechCardOperationMedia) ([]entity.TechCardOperationMedia, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxOperationMediaPerStep {
		return nil, entity.NewFieldViolation(step+".media", "too_many", fmt.Sprint(len(in)),
			fmt.Sprintf("at most %d photos per step: a long filmstrip stops being scrolled through", maxOperationMediaPerStep))
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
				"a step picture with no media means nothing — pick a file")
		}
		// Один и тот же снимок дважды на шаге — это два филмстрип-кадра, неразличимых глазом, и
		// два места, куда можно поставить противоречащие выноски.
		if seenMedia[mediaID] {
			return nil, entity.NewFieldViolation(path+".media_id", "duplicate", fmt.Sprint(mediaID),
				"this photo is already attached to the step: put the callouts on that same one")
		}
		seenMedia[mediaID] = true

		caption := strings.TrimSpace(m.Caption)
		if len([]rune(caption)) > maxOperationMediaCaptionLen {
			return nil, entity.NewFieldViolation(path+".caption", "too_long", "",
				fmt.Sprintf("a photo caption is at most %d characters", maxOperationMediaCaptionLen))
		}
		anns, err := annotationsFromPb(path, m.Annotations)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardOperationMedia{
			MediaId: mediaID,
			Caption: nullStringFromPb(caption),
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
			fmt.Sprintf("at most %d callouts per photo: beyond that they can't be read", maxAnnotationsPerMedia))
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
				"the callout kind determines both the number of points and what gets drawn — without it there is no shape")
		}
		min, max, _ := kind.PointsAllowed()
		if len(a.Points) < min || len(a.Points) > max {
			return nil, entity.NewFieldViolation(ap+".points", "wrong_count", fmt.Sprint(len(a.Points)),
				fmt.Sprintf("“%s” is drawn from %s points", kind, pointsRangeText(min, max)))
		}
		points := make([]entity.TechCardAnnotationPoint, 0, len(a.Points))
		for k, p := range a.Points {
			pp := fmt.Sprintf("%s.points[%d]", ap, k)
			if p == nil {
				return nil, entity.NewFieldViolation(pp, "required", "", "the callout has a missing point")
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
				fmt.Sprintf("callout text is at most %d characters; a long explanation lives in the step note", maxAnnotationTextRunes))
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
					"a callout colour comes from a closed list: the seamstress's sheet gets printed in black and white too")
			}
			color = c
		}
		// ССЫЛКА НА ДЕТАЛЬ — СОВЕТУЮЩАЯ. Проверяется только ФОРМА ключа, не разрешимость: указание
		// ставят на снимок, пришедший с примерки, раньше, чем детали кроя родятся из чертежа, и
		// отказ сохранения всей карточки за неразрешённый ключ стоил бы дороже висящей ссылки.
		// Клиент показывает «деталь удалена» и даёт перевыбрать — ровно как у входов операции.
		keys, err := annotationPieceKeys(ap, a.PieceLineKeys, a.PieceLineKey)
		if err != nil {
			return nil, err
		}
		first := ""
		if len(keys) > 0 {
			first = keys[0]
		}
		caps, err := capsFromPb(ap, kind, a.Caps)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardAnnotation{
			Kind:          kind,
			Points:        points,
			Text:          text,
			LabelX:        lx,
			LabelY:        ly,
			Color:         color,
			PieceLineKey:  first,
			PieceLineKeys: keys,
			// ПРИВЕДЕНИЕ К false МОЛЧИТ ЗАКОННО, обоснование — у карточного близнеца
			// (calloutGeometryFromPb ниже, «Пунктир у точки и заливка у линии…»): флаг это не
			// измеренный факт, а признак примитива, у которого рисовать нечего. Асимметрии
			// запись/чтение здесь нет, но НЕ потому, что чтение повторяет kind.HasLine/HasArea —
			// эмиссия отдаёт хранимое дословно (operationMediaToPb ниже, task.go, fitting.go), —
			// а потому, что в хранилище флаг попадает ТОЛЬКО через это приведение: каждый пишущий
			// путь (карточка, задача, примерка) идёт через annotationsFromPb / calloutGeometryFromPb,
			// и хранимое уже приведено. Значит и отпечаток, считающий Dashed/Filled из entity
			// (techcard_section_digest.go), на записи и на перечтении видит одно и то же. Отказ
			// вместо приведения объявлял бы подпись протухшей за нажатие, ничего не изменившее.
			Dashed: a.Dashed && kind.HasLine(),
			Filled: a.Filled && kind.HasArea(),
			Caps:   caps,
		})
	}
	return out, nil
}

// nonBlank — непустые значения без окружающих пробелов. Общий для обоих сводов: «список пуст» и
// «список из пустых строк» обязаны означать одно и то же, иначе клиент, приславший слоты вместо
// значений, стирает деталь, которую сам же прислал старым полем.
func nonBlank(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// maxAnnotationPieces — потолок числа деталей на одном указании. Узел, собирающий больше дюжины
// деталей сразу, — это не узел, а вся вещь; такое указание не читается ни на экране, ни на бумаге.
const maxAnnotationPieces = 12

// annotationPieceKeys сводит СПИСОК деталей и старое одиночное поле к одному списку.
//
// ПРАВИЛО БЕЗ ФЛАГА ПРИСУТСТВИЯ: непустой список вытесняет старое поле целиком, пустой читается
// как [legacy]. Клиенту, который про список не знает, менять нечего, а новый шлёт оба и обязан
// держать legacy равным первому элементу — сервер этого не требует, потому что список у него
// главный, и расхождение просто теряется.
//
// Дубли снимаются молча: «эта строчка на подборте и на подборте» — не два указания, а одно,
// названное дважды, и отказ здесь был бы отказом за опечатку в интерфейсе, а не за порчу данных.
func annotationPieceKeys(path string, list []string, legacy string) ([]string, error) {
	// ВЕТКА ВЫБИРАЕТСЯ ПО НЕПУСТЫМ ЭЛЕМЕНТАМ, а не по длине списка. Список из одних пустых строк
	// (клиент послал слоты, а не значения) — это «сказать нечего», и трактовать его как
	// «вытеснить старое поле» значило бы молча стереть деталь, которую прислали legacy-полем.
	src := nonBlank(list)
	field := path + ".piece_line_keys"
	if len(src) == 0 {
		src = nonBlank([]string{legacy})
		field = path + ".piece_line_key"
	}
	out := make([]string, 0, len(src))
	seen := make(map[string]bool, len(src))
	for i, key := range src {
		// Индекс в пути отказа: без него сообщение «ключ должен быть из 26 знаков» не говорит,
		// какой из двенадцати. Соседние отказы этого файла пишут `points[2]`.
		if err := validatePatternLineKey(key, fmt.Sprintf("%s[%d]", field, i)); err != nil {
			return nil, err
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	if len(out) > maxAnnotationPieces {
		return nil, entity.NewFieldViolation(field, "too_many", fmt.Sprint(len(out)),
			fmt.Sprintf("at most %d pieces per callout: any longer and it can't be read, on screen or on paper", maxAnnotationPieces))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// --- геометрия НУМЕРОВАННОЙ выноски -------------------------------------------------------------
//
// ОДИН свод на ОБЕ выноски — карточную (0309/0310) и примерочную (0319). Тот же словарь видов и те
// же правила числа точек, что у выноски на снимке шага, — с одним отличием, и оно принципиально: у
// нумерованной выноски МАРКЕР живёт отдельно, в pos_x/pos_y, потому что на него ссылаются НОМЕРОМ
// (на карточке — деталь, операция и дефект; на примерке — замечание change-request). Поэтому
// `points` здесь держит ТОЛЬКО якоря фигуры, и у пина их ноль, а не один: единственная точка пина —
// это и есть маркер, и дублировать её в якорях значило бы завести два места для одной координаты,
// которые однажды разойдутся.
//
// ВТОРОГО ВАЛИДАТОРА НЕТ И БЫТЬ НЕ ДОЛЖНО. Правил тут пять (вид из закрытого списка, число точек по
// виду, координата в кадре и не длиннее шести знаков, цвет из закрытого списка, согласованность
// пунктира со штриховкой), и разойдясь хоть в одном, два экрана начали бы принимать разные фигуры
// под одним именем — притом молча, потому что расхождение видно только на переносе замечания
// примерки в тех-карту.

// calloutKindOrPin читает вид ХРАНИМОЙ выноски. Пусто = pin: колонка появилась в 0309 (у примерки —
// в 0319) с этим дефолтом, но ряд читателей (архивные снапшоты релизов, клон сезона) отдают сущность
// в обход колонки, и там поле остаётся нулевой строкой.
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

// calloutGeometry — разобранная фигура нумерованного указания. Структурой, а не пятью возвратами:
// группа атомарна (см. proto), и пять значений подряд в сигнатуре — приглашение перепутать их
// местами на следующем добавленном поле.
type calloutGeometry struct {
	Kind   entity.TechCardAnnotationKind
	Points []entity.TechCardAnnotationPoint
	Color  entity.TechCardAnnotationColor
	Dashed bool
	Filled bool
	Caps   entity.TechCardAnnotationCaps
}

// calloutGeometryPb — та же группа, как она приезжает С ПРОВОДА. Аргументом-структурой, а не
// конкретным сообщением: полей у карточной выноски и у выноски примерки разное число, а ФИГУРА у
// них одна, и валидатор обязан видеть ровно фигуру. Так же он не сможет случайно опереться на
// поле, которого у второй выноски нет.
type calloutGeometryPb struct {
	Kind   *pb_common.TechCardAnnotationKind
	Points []*pb_common.TechCardAnnotationPoint
	Color  pb_common.TechCardAnnotationColor
	Dashed bool
	Filled bool
	Caps   pb_common.TechCardAnnotationCaps
}

// techCardCalloutGeometryPb / fittingCalloutGeometryPb — снимок группы с конкретного сообщения.
// Две строчки на переходник вместо второго валидатора.
func techCardCalloutGeometryPb(c *pb_common.TechCardCallout) calloutGeometryPb {
	return calloutGeometryPb{Kind: c.Kind, Points: c.Points, Color: c.Color, Dashed: c.Dashed, Filled: c.Filled, Caps: c.Caps}
}

func fittingCalloutGeometryPb(c *pb_common.FittingCallout) calloutGeometryPb {
	return calloutGeometryPb{Kind: c.Kind, Points: c.Points, Color: c.Color, Dashed: c.Dashed, Filled: c.Filled, Caps: c.Caps}
}

// calloutGeometryFromPb разбирает вид, якоря и цвет нумерованной выноски — карточной или
// примерочной. `path` — путь для отказов («callouts[3]»). Отсутствие вида читается как PIN: весь
// массив живых карточек и примерок написан до этого поля и приезжает с нулевым энумом, и трактовать
// его отказом значило бы отвергнуть каждую.
//
// ДВЕ ИЗВЕСТНЫЕ ЩЕЛИ, ОСТАВЛЕННЫЕ НАМЕРЕННО — обе одинаковы у карточки и у примерки, и лечить их
// на одном экране значило бы развести поведение двух половин одного примитива:
//
//   - REST-гейтвей позволяет прислать `kind` ЯВНЫМ нулевым значением. Для proto3 optional это
//     присутствие, то есть контракт «молчание ⇒ перенос» такой запрос обходит и схлопывает фигуру в
//     точку. Живёт на проде у тех-карты с 0309; расхождение было бы хуже самой щели.
//   - Сущность, собранная в обход dto (клон сезона, архивный снапшот), может нести вид без
//     согласованного числа якорей: стор форму не сверяет и запишет как есть, а следующее сохранение
//     через dto такую выноску отвергнет. Симметрично тех-карте.
func calloutGeometryFromPb(path string, c calloutGeometryPb) (calloutGeometry, error) {
	var zeroGeom calloutGeometry
	kindPb, pointsPb, colorPb := c.Kind, c.Points, c.Color
	kind := entity.AnnotationKindPin
	if kindPb != nil && *kindPb != pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_UNKNOWN {
		k, ok := annotationKindFromPb[*kindPb]
		if !ok {
			return zeroGeom, entity.NewFieldViolation(path+".kind", "unknown_value", kindPb.String(),
				"the callout kind comes from a closed list: the kind determines both the number of points and what gets drawn")
		}
		kind = k
	}
	min, max, _ := calloutPointsAllowed(kind)
	if len(pointsPb) < min || len(pointsPb) > max {
		// Слова отказа НЕЙТРАЛЬНЫ к экрану: тот же валидатор отвечает и про эскиз карточки, и про
		// снимок примерки, а путь (`callouts[3].points`) и так называет место точнее любого слова.
		return zeroGeom, entity.NewFieldViolation(path+".points", "wrong_count", fmt.Sprint(len(pointsPb)),
			fmt.Sprintf("“%s” is drawn from %s anchors (the numbered marker stands apart)", kind, pointsRangeText(min, max)))
	}
	points := make([]entity.TechCardAnnotationPoint, 0, len(pointsPb))
	for k, p := range pointsPb {
		pp := fmt.Sprintf("%s.points[%d]", path, k)
		if p == nil {
			return zeroGeom, entity.NewFieldViolation(pp, "required", "", "the callout has a missing anchor")
		}
		x, err := unitInterval(pp+".x", p.X)
		if err != nil {
			return zeroGeom, err
		}
		y, err := unitInterval(pp+".y", p.Y)
		if err != nil {
			return zeroGeom, err
		}
		points = append(points, entity.TechCardAnnotationPoint{X: x, Y: y})
	}
	color := entity.TechCardAnnotationColor("")
	if colorPb != pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_UNKNOWN {
		c, ok := annotationColorFromPb[colorPb]
		if !ok {
			return zeroGeom, entity.NewFieldViolation(path+".color", "unknown_value", colorPb.String(),
				"a callout colour comes from a closed list: the sheet gets printed in black and white too")
		}
		color = c
	}
	caps, err := capsFromPb(path, kind, c.Caps)
	if err != nil {
		return zeroGeom, err
	}
	return calloutGeometry{
		Kind:   kind,
		Points: points,
		Color:  color,
		Caps:   caps,
		// Пунктир у точки и заливка у линии приводятся к false, а не отвергаются: бессмысленный
		// флаг это не порча данных, а два способа записать «нечего рисовать» разошлись бы в
		// отпечатке секции и объявили бы подпись протухшей за нажатие, ничего не изменившее.
		Dashed: c.Dashed && kind.HasLine(),
		Filled: c.Filled && kind.HasArea(),
	}, nil
}

// calloutParts сводит СПИСОК деталей карточного указания и старое одиночное `part` к одному
// списку — теми же правилами, что annotationPieceKeys, и по той же причине: пустой список
// читается как [part], непустой вытесняет его целиком.
//
// Имена, а не ключи: на именах стоит связь «деталь ↔ выноска», и второй способ адресовать деталь
// развёл бы две половины одной связи. Форма имени не проверяется — `part` всегда был свободным
// текстом, и указание законно называет узел, которого среди деталей нет вовсе.
func calloutParts(path string, list []string, legacy string) ([]string, error) {
	src := nonBlank(list)
	field := path + ".parts"
	if len(src) == 0 {
		src = nonBlank([]string{legacy})
		field = path + ".part"
	}
	out := make([]string, 0, len(src))
	seen := make(map[string]bool, len(src))
	for _, name := range src {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) > maxAnnotationPieces {
		return nil, entity.NewFieldViolation(field, "too_many", fmt.Sprint(len(out)),
			fmt.Sprintf("at most %d pieces per callout: any longer and it can't be read, on screen or on paper", maxAnnotationPieces))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// calloutKindPbPtr отдаёт вид ХРАНИМОЙ выноски присутствующим полем. Присутствует всегда: чтение
// не бывает «умолчавшим», а круглый рейс нового клиента обязан вернуть то, что прочитал.
func calloutKindPbPtr(k entity.TechCardAnnotationKind) *pb_common.TechCardAnnotationKind {
	v := annotationKindToPb[calloutKindOrPin(k)]
	return &v
}

// CarryOmittedCalloutGeometry переносит хранимую геометрию указаний в payload, который про неё не
// говорил. Сопоставление по ЭСКИЗУ И НОМЕРУ (entity.TechCardCalloutKey).
//
// РАНЬШЕ ЗДЕСЬ СТОЯЛО «сопоставление по НОМЕРУ: номер — та самая идентичность, которой на выноску
// ссылаются деталь, операция и дефект, и другой у неё нет». Первая половина этого довода верна —
// ссылаются действительно номером, — а вторая неверна, и на ней перенос и ломался: номер не
// уникален по карточке, потому что эскиз и мудборд нумеруются независимо. «Другой идентичности
// нет» у ССЫЛКИ, а не у самого указания; у указания она есть — картинка, на которой оно стоит.
//
// Третья нога контракта присутствия, ровно как carryOmittedPieceCutSymmetryFrom: без неё подпись
// DESIGN, поставленная из вкладки со старым бандлом, хешировала бы «просто точки» поверх карточки,
// где нарисованы мерки, — и рождалась бы устаревшей навсегда.
func CarryOmittedCalloutGeometry(stored *entity.TechCard, tc *entity.TechCardInsert) {
	if stored == nil || tc == nil || len(tc.Callouts) == 0 {
		return
	}
	// СОПОСТАВЛЕНИЕ ПО ИДЕНТИЧНОСТИ УКАЗАНИЯ, А НЕ ПО ОДНОМУ НОМЕРУ. Номер не уникален по карточке:
	// эскиз и мудборд нумеруются независимо, и «выноска номер 3» бывает сразу двумя разными
	// указаниями на двух разных картинках. Перенос по одному номеру тогда не теряет фигуру, а
	// ПОДМЕНЯЕТ её — мудбордной записке досталась бы мерка с технического эскиза, нарисованная в
	// координатах ЧУЖОЙ картинки. Правило и доводы целиком — entity.TechCardCalloutKey.
	//
	// ДУБЛИ РАЗВЯЗЫВАЮТСЯ ПОЗИЦИОННО, а не «первым победившим» и не выбросом ключа. Идентичность
	// делят несколько строк ЗАКОННО: заметка мудборда номера листа не берёт вовсе, поэтому две
	// заметки на одной картинке несут один ключ; на проде есть и легаси-нули с нарисованными
	// мерками на одном эскизе (геометрия 0309/0310 старше ключа строки 0345). «Первый выигрывает»
	// подменяло бы второму чужую фигуру; выброс ключа стирал бы фигуру ОБОИМ — включая первого,
	// который раньше сопоставлялся сам с собой и своё сохранял. n-я входящая берёт у n-й хранимой:
	// при полной замене клиент пересылает строки в исходном порядке, поэтому каждая получает своё.
	//
	// Индекс детали кроя развязывает дубли ИНАЧЕ (первый выигрывает) — там разрешается ССЫЛКА, и
	// выброс объявил бы живую деталь оторванной. Расхождение намеренное и утверждается пробой.
	pos := entity.NewTechCardCalloutPositional(stored.Callouts, tc.Callouts)
	for i := range tc.Callouts {
		// Счётчик позиции двигается на КАЖДОЙ входящей строке с этим ключом, а не только на тех,
		// что просят перенос: иначе вторая просящая получила бы содержание ПЕРВОЙ хранимой.
		prev, ok := pos.Next(tc.Callouts[i].CalloutKey())
		if !tc.Callouts[i].KindOmitted {
			continue
		}
		if !ok {
			continue
		}
		tc.Callouts[i].Kind = calloutKindOrPin(prev.Kind)
		tc.Callouts[i].Points = prev.Points
		tc.Callouts[i].Color = prev.Color
		// Пунктир и штриховка в той же группе: молчание про вид — молчание про ВСЁ, что описывает
		// фигуру. Перенести якоря дуги и потерять её пунктир значило бы отдать в цех другую линию.
		tc.Callouts[i].Dashed = prev.Dashed
		tc.Callouts[i].Filled = prev.Filled
		// Наконечник — та же группа: перенести якоря и потерять стрелку значило бы отдать в цех
		// другую линию, ровно как с пунктиром.
		tc.Callouts[i].Caps = prev.Caps
	}
}

// FittingCalloutGeometryOmitted — говорит ли payload про геометрию хоть одной выноски. Существует
// затем, чтобы UpdateFitting не платил лишним чтением всей примерки за каждое сохранение НОВОГО
// клиента: тот шлёт вид всегда, и переносить ему нечего.
func FittingCalloutGeometryOmitted(f *entity.FittingInsert) bool {
	if f == nil {
		return false
	}
	for _, c := range f.Callouts {
		if c.KindOmitted {
			return true
		}
	}
	return false
}

// CarryOmittedFittingCalloutGeometry — то же самое для примерки, и по той же причине: выноски
// сохраняются ПОЛНОЙ ЗАМЕНОЙ, и вкладка со старым бандлом, изменившая один только вердикт, стёрла бы
// каждую мерку и каждую обведённую зону на всех снимках.
//
// НО ТОЖДЕСТВО ЗДЕСЬ СТРОЖЕ, ЧЕМ У ТЕХ-КАРТЫ, И ЭТО НЕ РАЗНОБОЙ, КОТОРЫЙ НАДО «ПРИВЕСТИ К
// ЕДИНООБРАЗИЮ». Номер выноску не опознаёт: он выдаётся как max+1, поэтому удаление ПОСЛЕДНЕЙ
// заметки и заведение новой переиспользует освободившееся число, а удаление первой сдвигает
// нумерацию остальных. Перенос по одному лишь номеру тогда не теряет фигуру, а ПОДМЕНЯЕТ её:
// на записку «плечо жмёт» записался бы вид dim с двумя чужими якорями и пунктиром. Строка выходит
// внутренне согласованной — стор нормализует только пустой вид и число точек не сверяет, — поэтому
// новый клиент нарисует размерную линию там, где человек ничего не рисовал.
//
// У тех-карты этот же промах ловится протухшей подписью DESIGN: отпечаток секции сдвигается, и
// карточка кричит «изменено с момента утверждения». У примерки подписи нет и журнала нет — подмена
// осталась бы тихой и навсегда. Поэтому геометрия принадлежит КОНКРЕТНОМУ МАРКЕРУ НА КОНКРЕТНОМ
// СНИМКЕ: сходятся номер, media_id и обе координаты — переносим; не сходятся — переноса нет.
//
// ПОТЕРЯ ЧЕСТНЕЕ ПОДМЕНЫ: стёртую фигуру человек видит и рисует заново, выдуманную — принимает за
// свою.
func CarryOmittedFittingCalloutGeometry(stored *entity.Fitting, f *entity.FittingInsert) {
	if stored == nil || f == nil || len(f.Callouts) == 0 {
		return
	}
	// Повторяющиеся номера В ПРИСЛАННОМ выбывают из переноса целиком. Уникальности номера нет ни в
	// схеме, ни в проверках (ноль законен), а старый бандл легко шлёт несколько выносок без номера
	// вовсе: все они претендовали бы на одну и ту же хранимую фигуру, и как минимум одна получила
	// бы чужую. Отказом на это отвечать нельзя — отказ на сохранении примерки, которую нечем
	// починить из интерфейса, дороже потерянной фигуры.
	seen := make(map[int]int, len(f.Callouts))
	for _, c := range f.Callouts {
		seen[c.Number]++
	}
	for i := range f.Callouts {
		if !f.Callouts[i].KindOmitted || seen[f.Callouts[i].Number] > 1 {
			continue
		}
		prev, ok := matchStoredFittingCallout(stored.Callouts, f.Callouts[i])
		if !ok {
			continue
		}
		f.Callouts[i].Kind = calloutKindOrPin(prev.Kind)
		f.Callouts[i].Points = prev.Points
		f.Callouts[i].Color = prev.Color
		// Пунктир и штриховка в той же группе: молчание про вид — молчание про ВСЁ, что описывает
		// фигуру.
		f.Callouts[i].Dashed = prev.Dashed
		f.Callouts[i].Filled = prev.Filled
		f.Callouts[i].Caps = prev.Caps
	}
}

// matchStoredFittingCallout ищет ХРАНИМУЮ выноску, тождественную присланной: тот же номер, тот же
// снимок и тот же маркер на нём. Первая подошедшая выигрывает — на четырёх совпавших полях две
// хранимые строки различить нечем, и выбор обязан быть детерминированным, иначе одно и то же
// сохранение дважды дало бы разные фигуры.
//
// Координаты сравниваются ЧИСЛЕННО, а не строками: колонка DECIMAL(5,4) возвращает «0.2000», а с
// провода приезжает «0.2», и сравнение представлений отменило бы перенос вообще всегда — то есть
// фича молча превратилась бы в «геометрия теряется при любом сохранении старым клиентом».
func matchStoredFittingCallout(stored []entity.FittingCallout, want entity.FittingCallout) (entity.FittingCallout, bool) {
	for _, c := range stored {
		if c.Number == want.Number &&
			sameNullInt32(c.MediaId, want.MediaId) &&
			sameNullDecimal(c.PosX, want.PosX) &&
			sameNullDecimal(c.PosY, want.PosY) {
			return c, true
		}
	}
	return entity.FittingCallout{}, false
}

// sameNullInt32 / sameNullDecimal — равенство С УЧЁТОМ отсутствия: «не привязана к снимку» и
// «привязана к снимку 7» это разные выноски, а два «не привязана» — одна и та же.
func sameNullInt32(a, b sql.NullInt32) bool {
	if a.Valid != b.Valid {
		return false
	}
	return !a.Valid || a.Int32 == b.Int32
}

func sameNullDecimal(a, b decimal.NullDecimal) bool {
	if a.Valid != b.Valid {
		return false
	}
	return !a.Valid || a.Decimal.Equal(b.Decimal)
}

// calloutPartsToPb — список деталей ХРАНИМОГО указания. Пустой список у записанного до 0310
// собирается из `part`: круглый рейс нового клиента иначе вернул бы пустоту и стёр бы деталь.
func calloutPartsToPb(c entity.TechCardCallout) []string {
	if len(c.Parts) > 0 {
		return c.Parts
	}
	if name := strings.TrimSpace(c.Part.String); name != "" {
		return []string{name}
	}
	return nil
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
			// Список деталей отдаётся ВСЕГДА, а старое поле — первым его элементом. Уже
			// записанные выноски несут только `piece` в JSON-колонке, поэтому список берётся
			// тем же правилом, что и на чтении с провода: непустой главнее, пустой читается
			// как [legacy].
			keys := a.PieceLineKeys
			if len(keys) == 0 && a.PieceLineKey != "" {
				keys = []string{a.PieceLineKey}
			}
			first := a.PieceLineKey
			if len(keys) > 0 {
				first = keys[0]
			}
			anns = append(anns, &pb_common.TechCardAnnotation{
				Kind:          annotationKindToPb[a.Kind],
				Points:        points,
				Text:          a.Text,
				LabelX:        pbDecimalFromDecimal(a.LabelX),
				LabelY:        pbDecimalFromDecimal(a.LabelY),
				Color:         annotationColorToPb[a.Color],
				PieceLineKey:  first,
				PieceLineKeys: keys,
				Dashed:        a.Dashed,
				Filled:        a.Filled,
				Caps:          annotationCapsToPb[a.Caps],
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

// TechCardCalloutAnnotationJSON — ГЕОМЕТРИЯ УКАЗАНИЯ В ТОЙ ФОРМЕ, В КАКОЙ ЕЁ ЧИТАЕТ ПРОВОД.
//
// Существует ради потребителя, которому нужна ФОРМА выноски отдельно от строки её текста: снимок
// мудбордной выноски в прогоне (DesignMoodCallout.annotation) собирается из этой же карточкиной
// выноски и разбирается обратно в то же самое сообщение common.TechCardAnnotation.
//
// ⚠ ПОЧЕМУ НЕ «СЛОЖИТЬ JSON РУКАМИ». Потому что это уже было сделано и МОЛЧА ТЕРЯЛО ВСЁ. Читатель
// разбирает колонку с DiscardUnknown, а вид и цвет на проводе — ЭНУМЫ: protojson ждёт их
// объявленные имена (TECH_CARD_ANNOTATION_KIND_DIM), а не хранимые строки («dim»). Самодельный
// объект с хранимыми строками разбирается БЕЗ ОШИБКИ в ПУСТОЕ сообщение — то есть бумага теряет
// каждую мерку и каждую скобку, и ни одна ошибка нигде не появляется. Единственный способ не
// разойтись — собрать то же сообщение теми же картами, что и весь остальной провод.
//
// Точка привязки берётся из pos_x/pos_y выноски: у указания на эскизе это положение плашки с
// номером, то есть ровно то, что label_x/label_y и означают.
func TechCardCalloutAnnotationJSON(c entity.TechCardCallout) ([]byte, error) {
	ann := &pb_common.TechCardAnnotation{
		Kind:   annotationKindToPb[calloutKindOrPin(c.Kind)],
		Points: calloutPointsToPb(c.Points),
		// TEXT ОСТАЁТСЯ ПУСТЫМ, И ЭТО НЕ УПУЩЕНИЕ. Геометрия несёт ФОРМУ, а не слова: заполнив
		// её `text`, мы получили бы одну и ту же заметку дважды — у фигуры и в списке, — и два
		// написания разошлись бы на первой же правке. Читаемая строка собирается ОДНИМ местом,
		// `entity.TechCardCalloutPrintedLine`, и едет полем рядом с геометрией.
		Text:   "",
		LabelX: pbDecimalFromNull(c.PosX),
		LabelY: pbDecimalFromNull(c.PosY),
		Color:  annotationColorToPb[c.Color],
		Dashed: c.Dashed,
		Filled: c.Filled,
		Caps:   annotationCapsToPb[c.Caps],
	}
	return protojson.Marshal(ann)
}

// TechCardAnnotationFromPb проверяет ОДНО указание и отдаёт его доменную форму.
//
// ⚠ ЭТО ТОНКАЯ ОБЁРТКА, А НЕ ВТОРОЙ ВАЛИДАТОР, и обёрткой она обязана остаться. Правил у фигуры
// пять (вид из закрытого списка, число точек по виду, координата в кадре и не длиннее шести знаков,
// цвет из закрытого списка, согласованность пунктира со штриховкой); разойдясь хоть в одном, два
// экрана начали бы принимать разные фигуры под одним именем — притом молча. Поэтому тело здесь —
// вызов annotationsFromPb, того же самого, которым идут тех-карта, задача и примерка.
//
// Заведена ради писателя, у которого указание ОДНО, а не список на снимок: метка ассета на флэте
// (design_asset_placement, 0354) — это ровно одна фигура на одной картинке, и разворачивать её в
// список из одного элемента у каждого вызывающего значило бы разложить nil-случай по трём местам.
//
// nil ОТКАЗЫВАЕТСЯ ЯВНО. annotationsFromPb пропускает nil-элементы молча (в списке на снимок это
// верно: дыра в массиве — не фигура), и без этой ветки одиночный nil вернул бы пустой результат с
// nil-ошибкой, то есть «указание принято» про указание, которого не прислали.
func TechCardAnnotationFromPb(path string, a *pb_common.TechCardAnnotation) (entity.TechCardAnnotation, error) {
	if a == nil {
		return entity.TechCardAnnotation{}, entity.NewFieldViolation(path, "required", "",
			"a mark on a drawing is a shape: without one there is nothing to draw and nothing to point at")
	}
	out, err := annotationsFromPb(path, []*pb_common.TechCardAnnotation{a})
	if err != nil {
		return entity.TechCardAnnotation{}, singleAnnotationViolation(path, err)
	}
	if len(out) != 1 {
		return entity.TechCardAnnotation{}, entity.NewFieldViolation(path, "required", "",
			"a mark on a drawing is a shape: without one there is nothing to draw and nothing to point at")
	}
	return out[0], nil
}

// singleAnnotationViolation убирает из имени поля индекс, которого у одиночного указания нет.
//
// annotationsFromPb называет поля как «<path>.annotations[0].points», потому что у неё их СПИСОК.
// Отдать это имя клиенту, приславшему одно поле `annotation`, значило бы указать на путь, которого
// в его запросе не существует, — а весь смысл BadRequest.field_violations в том, чтобы экран
// подсветил ровно то место, куда человек ткнул. Переписывается ПРЕФИКС и только он: причина,
// конфликтующее значение и совет остаются теми же, что у общего свода.
func singleAnnotationViolation(path string, err error) error {
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	prefix := path + ".annotations[0]"
	if !strings.HasPrefix(ve.Field, prefix) {
		return err
	}
	return entity.NewFieldViolation(path+strings.TrimPrefix(ve.Field, prefix),
		ve.Reason, ve.Conflicting, ve.HowToFix)
}
