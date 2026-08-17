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
	// ПОРЯДОК ЗДЕСЬ — ЧАСТЬ ЗАЩИТЫ. Экспонента проверяется ПОСЛЕ разбора строки, но ДО сравнения с
	// границами кадра: разбор дёшев (он кладёт коэффициент и показатель, ничего не считая), а
	// дорого именно сравнение. Округлить вместо отказа НЕЛЬЗЯ — rescale на показателе -10000000 и
	// есть тот самый взрыв, от которого мы защищаемся.
	if exp := nd.Decimal.Exponent(); exp < -maxCoordinateScale {
		return decimal.Decimal{}, entity.NewFieldViolation(field, "too_precise", coordSample(d.Value),
			fmt.Sprintf("координата выноски — доля кадра, не больше %d знаков после запятой: точнее снимок ничего не различает", maxCoordinateScale))
	} else if exp > 0 {
		// Показатель степени сам по себе не преступление, но координата, записанная им, у нас
		// всегда либо ноль, либо вне кадра — а стоит такое сравнение дороже всей конвертации.
		return decimal.Decimal{}, entity.NewFieldViolation(field, "bad_scale", coordSample(d.Value),
			"координата выноски записывается обычной дробью от 0 до 1, а не показателем степени")
	}
	if nd.Decimal.LessThan(zero) || nd.Decimal.GreaterThan(one) {
		return decimal.Decimal{}, entity.NewFieldViolation(field, "out_of_frame", nd.Decimal.String(),
			"координата выноски — доля кадра от 0 до 1: точка вне снимка ничего не указывает")
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
		keys, err := annotationPieceKeys(ap, a.PieceLineKeys, a.PieceLineKey)
		if err != nil {
			return nil, err
		}
		first := ""
		if len(keys) > 0 {
			first = keys[0]
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
			Dashed:        a.Dashed && kind.HasLine(),
			Filled:        a.Filled && kind.HasArea(),
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
			fmt.Sprintf("на одно указание не больше %d деталей: длиннее его не прочесть ни на экране, ни на бумаге", maxAnnotationPieces))
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
}

// techCardCalloutGeometryPb / fittingCalloutGeometryPb — снимок группы с конкретного сообщения.
// Две строчки на переходник вместо второго валидатора.
func techCardCalloutGeometryPb(c *pb_common.TechCardCallout) calloutGeometryPb {
	return calloutGeometryPb{Kind: c.Kind, Points: c.Points, Color: c.Color, Dashed: c.Dashed, Filled: c.Filled}
}

func fittingCalloutGeometryPb(c *pb_common.FittingCallout) calloutGeometryPb {
	return calloutGeometryPb{Kind: c.Kind, Points: c.Points, Color: c.Color, Dashed: c.Dashed, Filled: c.Filled}
}

// calloutGeometryFromPb разбирает вид, якоря и цвет нумерованной выноски — карточной или
// примерочной. `path` — путь для отказов («callouts[3]»). Отсутствие вида читается как PIN: весь
// массив живых карточек и примерок написан до этого поля и приезжает с нулевым энумом, и трактовать
// его отказом значило бы отвергнуть каждую.
func calloutGeometryFromPb(path string, c calloutGeometryPb) (calloutGeometry, error) {
	var zeroGeom calloutGeometry
	kindPb, pointsPb, colorPb := c.Kind, c.Points, c.Color
	kind := entity.AnnotationKindPin
	if kindPb != nil && *kindPb != pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_UNKNOWN {
		k, ok := annotationKindFromPb[*kindPb]
		if !ok {
			return zeroGeom, entity.NewFieldViolation(path+".kind", "unknown_value", kindPb.String(),
				"вид указания — из закрытого списка: вид определяет и число точек, и что рисуется")
		}
		kind = k
	}
	min, max, _ := calloutPointsAllowed(kind)
	if len(pointsPb) < min || len(pointsPb) > max {
		// Слова отказа НЕЙТРАЛЬНЫ к экрану: тот же валидатор отвечает и про эскиз карточки, и про
		// снимок примерки, а путь (`callouts[3].points`) и так называет место точнее любого слова.
		return zeroGeom, entity.NewFieldViolation(path+".points", "wrong_count", fmt.Sprint(len(pointsPb)),
			fmt.Sprintf("«%s» рисуется по %s якорям (нумерованный маркер стоит отдельно)", kind, pointsRangeText(min, max)))
	}
	points := make([]entity.TechCardAnnotationPoint, 0, len(pointsPb))
	for k, p := range pointsPb {
		pp := fmt.Sprintf("%s.points[%d]", path, k)
		if p == nil {
			return zeroGeom, entity.NewFieldViolation(pp, "required", "", "у указания пропущен якорь")
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
				"цвет указания — из закрытого списка: лист печатают и чёрно-белым")
		}
		color = c
	}
	return calloutGeometry{
		Kind:   kind,
		Points: points,
		Color:  color,
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
			fmt.Sprintf("на одно указание не больше %d деталей: длиннее его не прочесть ни на экране, ни на бумаге", maxAnnotationPieces))
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

// storedCalloutGeometryByNumber индексирует ХРАНИМЫЕ указания по номеру — общее ядро переноса для
// обеих выносок. Первый выигрывает: номера уникальны по смыслу, а дубль в хранимом — испорченные
// данные, на которых перенос обязан быть детерминированным (иначе одно и то же сохранение из одной
// и той же вкладки дважды дало бы разные фигуры).
func storedCalloutGeometryByNumber[T any](stored []T, number func(T) int, geom func(T) calloutGeometry) map[int]calloutGeometry {
	out := make(map[int]calloutGeometry, len(stored))
	for _, c := range stored {
		n := number(c)
		if _, seen := out[n]; seen {
			continue
		}
		out[n] = geom(c)
	}
	return out
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
	byNumber := storedCalloutGeometryByNumber(stored.Callouts,
		func(c entity.TechCardCallout) int { return c.Number },
		func(c entity.TechCardCallout) calloutGeometry {
			return calloutGeometry{
				Kind: calloutKindOrPin(c.Kind), Points: c.Points, Color: c.Color,
				Dashed: c.Dashed, Filled: c.Filled,
			}
		})
	for i := range tc.Callouts {
		if !tc.Callouts[i].KindOmitted {
			continue
		}
		prev, ok := byNumber[tc.Callouts[i].Number]
		if !ok {
			continue
		}
		tc.Callouts[i].Kind = prev.Kind
		tc.Callouts[i].Points = prev.Points
		tc.Callouts[i].Color = prev.Color
		// Пунктир и штриховка в той же группе: молчание про вид — молчание про ВСЁ, что описывает
		// фигуру. Перенести якоря дуги и потерять её пунктир значило бы отдать в цех другую линию.
		tc.Callouts[i].Dashed = prev.Dashed
		tc.Callouts[i].Filled = prev.Filled
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

// CarryOmittedFittingCalloutGeometry — то же самое для примерки, и по той же причине. Подписи у
// примерки нет, поэтому цена молчания здесь не «протухшая подпись», а прямая потеря: выноски
// сохраняются ПОЛНОЙ ЗАМЕНОЙ, и вкладка со старым бандлом, изменившая один только вердикт, стёрла
// бы каждую мерку и каждую обведённую зону на всех снимках примерки.
func CarryOmittedFittingCalloutGeometry(stored *entity.Fitting, f *entity.FittingInsert) {
	if stored == nil || f == nil || len(f.Callouts) == 0 {
		return
	}
	byNumber := storedCalloutGeometryByNumber(stored.Callouts,
		func(c entity.FittingCallout) int { return c.Number },
		func(c entity.FittingCallout) calloutGeometry {
			return calloutGeometry{
				Kind: calloutKindOrPin(c.Kind), Points: c.Points, Color: c.Color,
				Dashed: c.Dashed, Filled: c.Filled,
			}
		})
	for i := range f.Callouts {
		if !f.Callouts[i].KindOmitted {
			continue
		}
		prev, ok := byNumber[f.Callouts[i].Number]
		if !ok {
			continue
		}
		f.Callouts[i].Kind = prev.Kind
		f.Callouts[i].Points = prev.Points
		f.Callouts[i].Color = prev.Color
		f.Callouts[i].Dashed = prev.Dashed
		f.Callouts[i].Filled = prev.Filled
	}
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
				Kind:           annotationKindToPb[a.Kind],
				Points:         points,
				Text:           a.Text,
				LabelX:         pbDecimalFromDecimal(a.LabelX),
				LabelY:         pbDecimalFromDecimal(a.LabelY),
				Color:          annotationColorToPb[a.Color],
				PieceLineKey:   first,
				PieceLineKeys:  keys,
				Dashed:         a.Dashed,
				Filled:         a.Filled,
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
