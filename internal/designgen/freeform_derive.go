package designgen

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// ═══════════════ ОБЛАСТЬ — ЭТО НЕ МАСКА, А ДВЕ КАРТИНКИ И НЕСКОЛЬКО СЛОВ ═══════════════
//
// У платного маршрута картинок НЕТ ПОЛЯ МАСКИ ВОВСЕ (orimages.ImageRequest), поэтому «покажи
// модели вот это место» выражается единственным способом, который у нас есть, — ещё картинками:
//
//  1. ОБВЕДЁННАЯ КОПИЯ исходника: тот же кадр, на нём контур каждой области своим цветом. Она
//     отвечает на вопрос «ГДЕ», сохраняя контекст: видно, что обведён именно карман этой куртки, а
//     не абстрактный прямоугольник.
//  2. КРОП ОБЛАСТИ с полями: тот же карман крупно. Он отвечает на вопрос «ЧТО ИМЕННО», потому что
//     на копии 1536 px пуговица занимает двадцать пикселей и модель её не разглядит.
//
// ⚠ И ОБЕ ОБЯЗАНЫ БЫТЬ НАЗВАНЫ СЛОВАМИ, ИНАЧЕ ОНИ ВРЕДЯТ. Копия с красными линиями, приехавшая
// без подписи, читается как фотография вещи, НА КОТОРОЙ НАРИСОВАНЫ КРАСНЫЕ ЛИНИИ, и модель их
// добросовестно перерисует на выход. Подписи собирает эта же функция, в той же итерации, что и
// байты, — по тому же правилу, по которому весь пакет держит «подпись k описывает картинку k».
//
// СТРОКИ МЕДИА НЕ МИНТУЮТСЯ. Производные существуют ровно один платный вызов и едут data-URI:
// минт дал бы сироту при каждом отказе, компенсацию, которую надо писать и тестировать, и полосу
// карточки, засыпанную служебными кадрами. Воспроизводимость от этого не страдает — области
// заморожены в params, исходник адресуется media_id, — а это ровно то, что делает историю
// свидетельством.

// objectFetcher — ЧТЕНИЕ БАЙТОВ ЧУЖОГО ОБЪЕКТА БАКЕТА ПО КЛЮЧУ.
//
// Интерфейс, а не *bucket.Bucket, по той же причине, по которой все зависимости этого пакета
// интерфейсы: проба обязана уметь отдать сто на сто пикселей без сети и без S3.
type objectFetcher interface {
	GetManagedObject(ctx context.Context, objectKey string) (io.ReadCloser, int64, error)
}

// derivedRef — одна производная картинка вместе с её подписью. Пара, а не два списка: см. шапку.
type derivedRef struct {
	dataURI string
	caption string
}

// Потолки и пропорции производных.
const (
	// freeformMaxSourceBytes — сколько байтов исходника вообще будет прочитано. Тот же потолок и
	// тот же довод, что у разреза кадра (designSplitMaxSourceBytes): загрузка ограничивает кадр
	// ~40 MP, это его байтовый близнец, чтобы битый или враждебный объект не втекал в процесс без
	// границы.
	freeformMaxSourceBytes = 64 << 20
	// freeformMaxSide — длинная сторона обведённой копии. 1536 — то, что модели этого семейства
	// читают целиком; больше — это байты base64, за которые платит размер запроса, а не качество
	// ответа.
	freeformMaxSide = 1536
	// freeformOutlineFraction — толщина контура долей ШИРИНЫ кадра. 0.6 % это ~9 px на 1536 —
	// видно человеку и модели, но не закрывает того, что обводит.
	freeformOutlineFraction = 0.006
	// freeformCropPad — поля вокруг области в долях её собственного размера. Приём «generation
	// window»: без полей кроп теряет то, к чему область прилегает, и «пришей пуговицу сюда»
	// превращается в «нарисуй пуговицу на пустом месте».
	freeformCropPad = 0.08
	// freeformJPEGQuality — качество JPEG производных. 90 — предел, за которым растёт вес, а не
	// различимость.
	freeformJPEGQuality = 90
)

// freeformOutlineColours — ЦВЕТ ОБЛАСТИ ПО ЕЁ НОМЕРУ, и словарь тот же, которым карточка красит
// свои выноски (TechCardAnnotationColor: red, blue, green, orange).
//
// ⚠ ЦВЕТОМ, А НЕ БУКВОЙ, И ЭТО РЕШЕНИЕ, А НЕ ЛЕНЬ. Написать «A» на картинке из Go нечем: в
// стандартной библиотеке нет шрифта, а тянуть шрифт ради двух букв — это зависимость, лицензия и
// вес бинаря. Цвет читается и человеком, и моделью, называется словом в подписи («outlined in
// RED») и не требует ничего. Буквы A/B рисует клиент — на своём экране, поверх той же геометрии.
var freeformOutlineColours = []struct {
	name string
	rgba color.RGBA
}{
	{"RED", color.RGBA{R: 229, G: 57, B: 53, A: 255}},
	{"BLUE", color.RGBA{R: 30, G: 136, B: 229, A: 255}},
	{"GREEN", color.RGBA{R: 67, G: 160, B: 71, A: 255}},
	{"ORANGE", color.RGBA{R: 251, G: 140, B: 0, A: 255}},
}

// freeformAreaLetter — имя области НА ЭКРАНЕ ЧЕЛОВЕКА: A, B, C, D. Подпись называет и букву, и
// цвет, потому что на картинке есть только цвет, а в разговоре с человеком — только буква.
func freeformAreaLetter(i int) string {
	if i < 0 || i > 25 {
		return strconv.Itoa(i + 1)
	}
	return string(rune('A' + i))
}

// deriveFreeform строит производные картинки прогона плейграунда.
//
// ВХОДЫ: `attached` — картинки, которые УЖЕ уехали (позиция k здесь = «image k+1» в промпте), и
// `urls` — их адреса в том же порядке. Из них берётся и номер исходника для подписи, и объект для
// чтения байтов.
//
// ⚠ ОТКАЗ ЗДЕСЬ ХОРОНИТ ПРОГОН, И ЭТО ДЕШЁВАЯ ПОЛОВИНА ВЫБОРА. buildJob зовётся ДО StartAttempt,
// то есть до движения денег; прогон падает retryable-ошибкой и не платит. Альтернатива —
// «отправим без производных» — стоила бы полной цены кадра, в котором разметка человека не
// участвовала, и отличить такой кадр от честного в истории было бы нечем.
func deriveFreeform(ctx context.Context, objects objectFetcher, p runParams,
	attached []refCaption, urls []string) ([]derivedRef, error) {
	if p.Freeform == nil {
		return nil, nil
	}
	// ПОЗИЦИЯ КАРТИНКИ В ВЫЗОВЕ — ПО MEDIA ID, А НЕ ПО ИНДЕКСУ ITEM'А. Список `attached` — это
	// картинки, которые ПЕРЕЖИЛИ резолв медиа: строка, исчезнувшая между снимком и проходом,
	// выпадает из него, и все последующие номера сдвигаются. Считать номер по индексу item'а
	// значило бы указывать модели на соседнюю картинку — молча и ровно в том прогоне, где что-то
	// уже пошло не так.
	numberOf := make(map[int]int, len(attached))
	urlOf := make(map[int]string, len(attached))
	for i, rc := range attached {
		if rc.MediaID <= 0 {
			continue
		}
		if _, dup := numberOf[rc.MediaID]; dup {
			continue
		}
		numberOf[rc.MediaID] = i + 1
		if i < len(urls) {
			urlOf[rc.MediaID] = urls[i]
		}
	}

	// ПРОИЗВОДНЫЕ ВСТАЮТ ПОСЛЕ ВСЕХ ОРИГИНАЛОВ, в порядке items: их номера в промпте — это их
	// места в `job.References` после append'а, и другого счёта у этого пакета нет.
	var out []derivedRef
	for _, it := range p.Freeform.Items {
		if len(it.Regions) == 0 || it.MediaID <= 0 {
			continue
		}
		number, ok := numberOf[it.MediaID]
		if !ok {
			// Картинка не доехала (media удалили после снимка). Обводить нечего, и молчание здесь
			// честно: её нет ни в списке, ни в подписях, значит и производных у неё быть не может.
			continue
		}
		if objects == nil {
			return nil, fmt.Errorf("designgen: a freeform run marks areas on image %d but this worker "+
				"has no object store to read the picture from", number)
		}
		src, err := freeformFetchImage(ctx, objects, urlOf[it.MediaID])
		if err != nil {
			return nil, fmt.Errorf("designgen: cannot read image %d of a freeform run: %w", number, err)
		}
		marked, isPNG, err := freeformOutlined(src, it.Regions)
		if err != nil {
			return nil, fmt.Errorf("designgen: cannot outline the areas of image %d: %w", number, err)
		}
		out = append(out, derivedRef{
			dataURI: marked,
			caption: freeformOutlineCaption(number, it.Regions),
		})
		for r := range it.Regions {
			crop, err := freeformCrop(src, it.Regions[r], isPNG)
			if err != nil {
				return nil, fmt.Errorf("designgen: cannot crop area %s of image %d: %w",
					freeformAreaLetter(r), number, err)
			}
			out = append(out, derivedRef{
				dataURI: crop,
				caption: freeformCropCaption(number, r, freeformTextForRegion(it, r)),
			})
		}
	}
	return out, nil
}

// freeformTextForRegion — слова про эту область, если они есть. Пара позиционная (см. freeformItem).
func freeformTextForRegion(it freeformItem, r int) string {
	if r < 0 || r >= len(it.Texts) {
		return ""
	}
	return strings.TrimSpace(it.Texts[r])
}

// freeformOutlineCaption — подпись обведённой копии.
//
// ⚠ ПОСЛЕДНЯЯ ПОЛОВИНА ФРАЗЫ — САМАЯ ВАЖНАЯ. Без неё модель дорисовывает контуры на выход: они
// нарисованы на картинке, значит они часть вещи. Это не гипотеза о вкусе модели, а прямое
// следствие того, что мы ей показали.
func freeformOutlineCaption(number int, regions []freeformRegion) string {
	var parts []string
	for i := range regions {
		colour := freeformOutlineColours[i%len(freeformOutlineColours)]
		parts = append(parts, "area "+freeformAreaLetter(i)+" in "+colour.name)
	}
	return "image " + strconv.Itoa(number) + " with " + joinWithAnd(parts) +
		" outlined — the outlines are markers for you, not part of the garment; never draw them"
}

// freeformCropCaption — подпись кропа области. Слова человека приезжают ВНУТРЬ подписи, в кавычках:
// «что это за место» и «что с ним делать» — одно предложение, а не два в разных концах промпта.
func freeformCropCaption(number, region int, text string) string {
	c := "a close crop of area " + freeformAreaLetter(region) + " of image " + strconv.Itoa(number)
	if text != "" {
		c += " — «" + oneLine(text) + "»"
	}
	return c
}

func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return "no areas"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// freeformFetchImage читает объект бакета по СОХРАНЁННОМУ url и декодирует его.
//
// Ключ берётся из строки медиа и только из неё; сегментный гард стоит в бакете (GetManagedObject
// отказывает на ключе вне разрешённых папок ДО обращения к S3). Разбор url — общая
// bucket.ObjectKeyFromStoredURL, та же, которой пользуются экспорт архива и кроп кадра.
func freeformFetchImage(ctx context.Context, objects objectFetcher, rawURL string) (image.Image, error) {
	key, err := bucket.ObjectKeyFromStoredURL(rawURL)
	if err != nil {
		return nil, err
	}
	rc, size, err := objects.GetManagedObject(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("object %q: %w", key, err)
	}
	defer rc.Close()
	if size > freeformMaxSourceBytes {
		return nil, fmt.Errorf("object %q is %d bytes, over the %d ceiling", key, size, freeformMaxSourceBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, freeformMaxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read object %q: %w", key, err)
	}
	if len(raw) > freeformMaxSourceBytes {
		return nil, fmt.Errorf("object %q is over the %d byte ceiling", key, freeformMaxSourceBytes)
	}
	return freeformDecode(raw)
}

// freeformDecode ОПОЗНАЁТ ФОРМАТ ПО БАЙТАМ, а не по расширению или заголовку: полный размер медиа
// бывает и WebP (перекодирующая загрузка), и чем угодно, что положили побайтово (verbatim).
func freeformDecode(raw []byte) (image.Image, error) {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return png.Decode(bytes.NewReader(raw))
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return jpeg.Decode(bytes.NewReader(raw))
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return webp.Decode(bytes.NewReader(raw))
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return gif.Decode(bytes.NewReader(raw))
	default:
		return nil, fmt.Errorf("unrecognised image format")
	}
}

// freeformSourceIsPNG — держит ли исходник альфу. Спрашивается у декодированной картинки, а не у
// расширения: WebP с альфой декодируется в NRGBA ровно так же, как PNG, и терять её на кропе было
// бы потерей ровно того, ради чего вырез существует.
func freeformSourceIsPNG(src image.Image) bool {
	switch src.(type) {
	case *image.NRGBA, *image.RGBA, *image.NRGBA64, *image.RGBA64, *image.Paletted:
		return true
	}
	return false
}

// freeformOutlined — ОБВЕДЁННАЯ КОПИЯ: исходник, уменьшенный до freeformMaxSide, с контуром каждой
// области своим цветом. Возвращает data-URI и признак «исходник держал альфу».
//
// ⚠ КОПИЯ ВСЕГДА JPEG, ДАЖЕ ЕСЛИ ИСХОДНИК PNG, И ЭТО НЕ ПРОТИВОРЕЧИТ ПРАВИЛУ ПРО АЛЬФУ У КРОПА.
// Это служебная картинка «посмотри сюда», а не материал, из которого что-то делают: её никто не
// вклеивает и не сохраняет. Прозрачность на ней всё равно потерялась бы при отрисовке контуров, а
// JPEG q90 против PNG — это втрое меньше base64 в теле запроса, у которого свой потолок.
func freeformOutlined(src image.Image, regions []freeformRegion) (string, bool, error) {
	hadAlpha := freeformSourceIsPNG(src)
	scaled := freeformFit(src, freeformMaxSide)
	b := scaled.Bounds()
	canvas := image.NewRGBA(b)
	// БЕЛАЯ ПОДЛОЖКА ПОД ПРОЗРАЧНЫМ, а не чёрная: прозрачный PNG, слитый в JPEG без подложки,
	// становится чёрным прямоугольником, и модель видит вещь на угольном фоне вместо пустого.
	draw.Draw(canvas, b, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, b, scaled, b.Min, draw.Over)

	width := float64(b.Dx())
	thickness := int(math.Round(width * freeformOutlineFraction))
	if thickness < 2 {
		thickness = 2
	}
	for i, region := range regions {
		colour := freeformOutlineColours[i%len(freeformOutlineColours)].rgba
		freeformStroke(canvas, region, colour, thickness)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: freeformJPEGQuality}); err != nil {
		return "", hadAlpha, err
	}
	return freeformDataURI("image/jpeg", buf.Bytes()), hadAlpha, nil
}

// freeformStroke обводит один многоугольник ЗАМКНУТОЙ ломаной.
//
// Толстая линия рисуется квадратной «кистью» вдоль отрезка — приём, у которого нет ни зависимости,
// ни сглаживания, и оба недостатка здесь не стоят ничего: это метка «вот эта граница», а не
// графика. Точки, вышедшие за кадр, обрезаются самим Set'ом по bounds.
func freeformStroke(dst *image.RGBA, region freeformRegion, c color.RGBA, thickness int) {
	pts := region.Points
	if len(pts) < 2 {
		return
	}
	b := dst.Bounds()
	at := func(p freeformPoint) (int, int) {
		x := b.Min.X + int(math.Round(clamp01(p.X.f())*float64(b.Dx()-1)))
		y := b.Min.Y + int(math.Round(clamp01(p.Y.f())*float64(b.Dy()-1)))
		return x, y
	}
	for i := range pts {
		x0, y0 := at(pts[i])
		x1, y1 := at(pts[(i+1)%len(pts)])
		freeformThickLine(dst, x0, y0, x1, y1, c, thickness)
	}
}

func freeformThickLine(dst *image.RGBA, x0, y0, x1, y1 int, c color.RGBA, thickness int) {
	dx, dy := float64(x1-x0), float64(y1-y0)
	steps := int(math.Max(math.Abs(dx), math.Abs(dy)))
	if steps == 0 {
		freeformDot(dst, x0, y0, c, thickness)
		return
	}
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		freeformDot(dst, x0+int(math.Round(dx*t)), y0+int(math.Round(dy*t)), c, thickness)
	}
}

func freeformDot(dst *image.RGBA, x, y int, c color.RGBA, thickness int) {
	half := thickness / 2
	for iy := y - half; iy <= y+half; iy++ {
		for ix := x - half; ix <= x+half; ix++ {
			dst.SetRGBA(ix, iy, c)
		}
	}
}

// freeformCrop — КРОП ОБЛАСТИ С ПОЛЯМИ. PNG, если исходник держал альфу, иначе JPEG.
func freeformCrop(src image.Image, region freeformRegion, keepAlpha bool) (string, error) {
	b := src.Bounds()
	minX, minY, maxX, maxY := freeformBBox(region)
	// ПОЛЯ СЧИТАЮТСЯ ОТ РАЗМЕРА ОБЛАСТИ, а не от кадра: у пуговицы поле должно быть с пуговицу, а
	// не с четверть куртки.
	padX := (maxX - minX) * freeformCropPad
	padY := (maxY - minY) * freeformCropPad
	x0 := b.Min.X + int(math.Floor(clamp01(minX-padX)*float64(b.Dx())))
	y0 := b.Min.Y + int(math.Floor(clamp01(minY-padY)*float64(b.Dy())))
	x1 := b.Min.X + int(math.Ceil(clamp01(maxX+padX)*float64(b.Dx())))
	y1 := b.Min.Y + int(math.Ceil(clamp01(maxY+padY)*float64(b.Dy())))
	// НУЛЕВАЯ ПЛОЩАДЬ — ЭТО НЕ КАРТИНКА. Область в один пиксель законна на проводе (три точки в
	// одной), и кроп по ней дал бы пустой прямоугольник, который провайдер отвергнет уже за
	// деньги: расширяем до минимума вокруг того же места.
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	rect := image.Rect(x0, y0, x1, y1).Intersect(b)
	if rect.Empty() {
		return "", fmt.Errorf("the area lies outside the picture")
	}
	crop := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(crop, crop.Bounds(), src, rect.Min, draw.Src)
	scaled := freeformFit(crop, freeformMaxSide)

	var buf bytes.Buffer
	if keepAlpha {
		if err := png.Encode(&buf, scaled); err != nil {
			return "", err
		}
		return freeformDataURI("image/png", buf.Bytes()), nil
	}
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: freeformJPEGQuality}); err != nil {
		return "", err
	}
	return freeformDataURI("image/jpeg", buf.Bytes()), nil
}

// freeformBBox — охватывающий прямоугольник многоугольника, в долях 0..1.
func freeformBBox(region freeformRegion) (minX, minY, maxX, maxY float64) {
	minX, minY, maxX, maxY = 1, 1, 0, 0
	if len(region.Points) == 0 {
		return 0, 0, 1, 1
	}
	for _, p := range region.Points {
		x, y := clamp01(p.X.f()), clamp01(p.Y.f())
		minX = math.Min(minX, x)
		minY = math.Min(minY, y)
		maxX = math.Max(maxX, x)
		maxY = math.Max(maxY, y)
	}
	return minX, minY, maxX, maxY
}

// freeformFit уменьшает картинку до длинной стороны `max`. Меньшую НЕ увеличивает: растянутый
// кроп не несёт ни одной новой детали, а весит вчетверо.
func freeformFit(src image.Image, max int) image.Image {
	b := src.Bounds()
	long := b.Dx()
	if b.Dy() > long {
		long = b.Dy()
	}
	if long <= max || long == 0 {
		return src
	}
	scale := float64(max) / float64(long)
	w := int(math.Max(1, math.Round(float64(b.Dx())*scale)))
	h := int(math.Max(1, math.Round(float64(b.Dy())*scale)))
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst
}

func freeformDataURI(mediaType string, raw []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
