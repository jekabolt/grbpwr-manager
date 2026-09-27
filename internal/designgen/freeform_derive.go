package designgen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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
	// freeformFallbackSide — ПЕРВАЯ СТУПЕНЬ ВНИЗ, а не второе умолчание. Производная, не влезшая в
	// свой потолок байтов на 1536, пересобирается на 1024 — и это честный обмен: 1024 всё ещё
	// больше, чем модель видит после собственного ресайза, а байтов в ней вдвое с лишним меньше.
	// Ступень одна: вторая, третья и четвёртая превратили бы отказ в бесшумную деградацию, при
	// которой человек платит полную цену за кадр, собранный из миниатюр.
	freeformFallbackSide = 1024
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

	// ═══ ПОТОЛКИ БАЙТОВ, И ОНИ СТОЯТ ДО ДЕНЕГ ═══
	//
	// ⚠ ПОТОЛОК В 16 ССЫЛОК НЕ ОГРАНИЧИВАЕТ РАЗМЕР. Производная едет data-URI, то есть base64 ВНУТРИ
	// JSON-тела запроса, и живёт в памяти процесса, у которого пол-гигабайта на всё: тело собирается
	// целиком, кодируется целиком и держится до ответа. Шестнадцать ссылок по паре мегабайт — это
	// не «чуть больше трафика», это OOM всего воркера, который унесёт с собой чужой оплаченный
	// прогон в соседней горутине. Сам клиент картинок прямо называет base64 главным риском памяти.
	//
	// ОБА ЧИСЛА — БАЙТЫ ПОСЛЕ base64, то есть длина того, что реально уедет, а не размер картинки до
	// кодирования: считать «в пикселях» здесь значит считать не то, что расходуется.

	// freeformMaxDerivedBytes — потолок ОДНОЙ производной. 1.5 MiB это JPEG q90 полного кадра или
	// PNG настоящей вырезки; всё, что толще, — либо шум, либо фотография, зря сохранённая PNG'ом
	// (см. freeformSourceHasAlpha).
	freeformMaxDerivedBytes = 1536 << 10
	// freeformMaxJobBytes — потолок ВСЕХ производных одного задания. Шестнадцать ссылок в потолке
	// формы × полтора мегабайта дали бы двадцать четыре: число здесь — не сумма потолков, а то,
	// что процесс переживает, держа в себе тело запроса и ответ на него.
	//
	// Оригиналы в эту сумму не входят и входить не должны: они едут ССЫЛКАМИ по несколько десятков
	// байт, а картинку по ним забирает провайдер, а не мы.
	freeformMaxJobBytes = 12 << 20
)

// freeformSides — ЛЕСТНИЦА РАЗМЕРОВ ПРОИЗВОДНОЙ, сверху вниз. Порядок несущий: производная
// собирается на первом размере, и следующий берётся, только если она не влезла в свой потолок.
var freeformSides = []int{freeformMaxSide, freeformFallbackSide}

// errFreeformJobTooLarge — ЗАДАНИЕ НЕ ВЛЕЗАЕТ В ПАМЯТЬ, И ОТКАЗ БЕСПЛАТНЫЙ.
//
// ⚠ ОН ТЕРМИНАЛЬНЫЙ, И ЭТО ГЛАВНОЕ ЕГО СВОЙСТВО. buildJob зовётся ДО StartAttempt, то есть до
// движения денег; повтор соберёт РОВНО ТО ЖЕ задание из ровно того же замороженного снимка и упрётся
// в тот же потолок — то есть купит пять одинаковых отказов, если счесть его погодой. Человеку
// нужно другое: уменьшить картинку или снять с неё пару областей, и об этом говорит текст.
var errFreeformJobTooLarge = errors.New("designgen: this playground run does not fit in memory")

// errFreeformSourceTooLarge — ИСХОДНИК ОБЪЯВЛЯЕТ БОЛЬШЕ ПИКСЕЛЕЙ, ЧЕМ ЭТОТ ПРОЦЕСС РАЗВОРАЧИВАЕТ,
// И ОТКАЗ БЕСПЛАТНЫЙ.
//
// ⚠ ЭТО НЕ ВТОРОЕ ИМЯ job_too_large, И РАЗЛИЧЕНИЕ ЧЕЛОВЕК ЧИТАЕТ ГЛАЗАМИ. `job_too_large` говорит
// «картинок вместе слишком много байтов — снимите область или уменьшите кадры», то есть про СУММУ
// уже собранного; здесь не собрано ничего и собирать нечего — ОДНА картинка не может быть прочитана
// вовсе. Совет у них разный, поэтому и слово разное.
//
// ТЕРМИНАЛЬНЫЙ ПО ТОМУ ЖЕ ДОВОДУ, ЧТО СОСЕД: снимок заморожен, следующий проход возьмёт ту же
// строку медиа с тем же заголовком и упрётся в тот же потолок — то есть купит пять одинаковых
// отказов, если счесть его погодой.
var errFreeformSourceTooLarge = errors.New("designgen: a picture of this playground run is too large to read")

// freeformOutlineColours — ЦВЕТ ОБЛАСТИ ПО ЕЁ НОМЕРУ, и словарь тот же, которым карточка красит
// свои выноски (TechCardAnnotationColor: red, blue, green, orange).
//
// ⚠ ЦВЕТ — ПОЛОВИНА МЕТКИ, ВТОРАЯ ПОЛОВИНА — БУКВА, И ОНА ТОЖЕ ВПЕЧАТАНА В ПИКСЕЛИ. Приём
// называется Set-of-Mark (arXiv 2310.11441), и у него ровно два органа: видимая граница и видимый
// ЯРЛЫК рядом с ней. Без ярлыка «сделай это в области B» указывает в никуда — модель видит два
// одинаково обведённых места и слово, которого на картинке нет; сопоставить «B» с синим ей неоткуда,
// кроме нашей же подписи, а подпись — это текст, спорящий с изображением.
//
// БУКВЫ РИСУЕТ СЕРВЕР, А НЕ КЛИЕНТ, И ЭТО НЕ ДУБЛИРОВАНИЕ. Клиентские буквы живут на ЭКРАНЕ
// ЧЕЛОВЕКА, поверх канваса; в байты, уехавшие модели, они не попадают вовсе. Раньше здесь стоял
// довод «шрифта в стандартной библиотеке нет, а тянуть шрифт ради двух букв — зависимость,
// лицензия и вес бинаря»: он верен про ШРИФТ и не верен про БУКВУ. Восемь глифов 5×7 — это восемь
// строковых литералов ниже, у которых нет ни лицензии, ни веса, ни начертания.
var freeformOutlineColours = []struct {
	name string
	rgba color.RGBA
}{
	{"RED", color.RGBA{R: 229, G: 57, B: 53, A: 255}},
	{"BLUE", color.RGBA{R: 30, G: 136, B: 229, A: 255}},
	{"GREEN", color.RGBA{R: 67, G: 160, B: 71, A: 255}},
	{"ORANGE", color.RGBA{R: 251, G: 140, B: 0, A: 255}},
}

// ═══════════ БУКВА ОБЛАСТИ, ВПЕЧАТАННАЯ В ПИКСЕЛИ ═══════════

// freeformGlyphs — восемь букв растром 5×7, по строке на ряд: '#' рисуется, всё прочее пусто.
//
// ПОЧЕМУ ВОСЕМЬ, А НЕ ВЕСЬ АЛФАВИТ. Дверь держит потолок в четыре области НА КАРТИНКУ, а картинок в
// прогоне до восьми; буквы сквозные по прогону, поэтому предел, до которого метка обязана быть
// нарисуемой, — H. Область за пределом остаётся обведённой и НАЗВАННОЙ В ПОДПИСИ, но плашки не
// получает: нарисовать не ту букву было бы хуже, чем не рисовать никакой.
//
// ПОЧЕМУ 5×7. Это минимальная сетка, на которой все восемь букв различимы по форме, а не по
// плотности: у A, B, D и G есть ЗАМКНУТАЯ дыра, у C, E, F и H её нет — и это единственная разница,
// которую надо удержать при любом масштабе.
var freeformGlyphs = map[rune][7]string{
	'A': {".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'B': {"####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."},
	'C': {".###.", "#...#", "#....", "#....", "#....", "#...#", ".###."},
	'D': {"####.", "#...#", "#...#", "#...#", "#...#", "#...#", "####."},
	'E': {"#####", "#....", "#....", "####.", "#....", "#....", "#####"},
	'F': {"#####", "#....", "#....", "####.", "#....", "#....", "#...."},
	'G': {".###.", "#...#", "#....", "#.###", "#...#", "#...#", ".###."},
	'H': {"#...#", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
}

const (
	// freeformGlyphCols / freeformGlyphRows — сетка глифа. Названы, потому что по ним считается и
	// плашка, и масштаб, и три числа подряд в арифметике — это три места, где можно ошибиться.
	freeformGlyphCols = 5
	freeformGlyphRows = 7
	// freeformLetterFraction — высота буквы долей ШИРИНЫ кадра. 2 % это ~30 px на 1536: столько же,
	// сколько занимает средняя подпись на скриншоте, который эти модели читают уверенно.
	freeformLetterFraction = 0.02
	// freeformLetterMinPx — пол высоты. Ниже двенадцати пикселей различие A и H перестаёт быть
	// формой и становится шумом — а метка, которую нельзя прочесть, хуже отсутствующей: она
	// занимает место в кадре и заставляет модель гадать.
	freeformLetterMinPx = 12
)

// freeformLetterPlate впечатывает букву области в левый верхний угол её bbox: БЕЛЫЙ ГЛИФ НА ПЛАШКЕ
// ЦВЕТА КОНТУРА.
//
// ПОЧЕМУ НА ПЛАШКЕ, А НЕ ПРОСТО ЦВЕТНЫМИ ПИКСЕЛЯМИ. Буква ложится на ФОТОГРАФИЮ, то есть на любой
// цвет и любую фактуру; тонкие цветные штрихи по джинсовой ткани не читаются вовсе. Плашка даёт
// контраст, который не зависит от того, что под ней, и заодно связывает букву с цветом контура —
// одним взглядом, без легенды.
//
// ПОЧЕМУ В УГЛУ BBOX, А НЕ В ЦЕНТРЕ ОБЛАСТИ. Центр — это то, ПРО ЧТО просьба: пуговица, карман,
// вышивка. Метка, накрывшая предмет собой, отнимает у модели ровно ту деталь, ради которой область
// и обведена. Угол bbox всегда снаружи или на границе того, что обведено.
func freeformLetterPlate(dst *image.RGBA, region freeformRegion, index int, c color.RGBA) {
	rows, ok := freeformGlyphs[rune(freeformAreaLetter(index)[0])]
	if !ok || len(freeformAreaLetter(index)) != 1 {
		return
	}
	b := dst.Bounds()
	scale := int(math.Round(float64(b.Dx()) * freeformLetterFraction / freeformGlyphRows))
	if h := scale * freeformGlyphRows; h < freeformLetterMinPx {
		scale = (freeformLetterMinPx + freeformGlyphRows - 1) / freeformGlyphRows
	}
	if scale < 1 {
		scale = 1
	}
	pad := scale
	glyphW, glyphH := freeformGlyphCols*scale, freeformGlyphRows*scale
	plateW, plateH := glyphW+2*pad, glyphH+2*pad

	minX, minY, _, _ := freeformBBox(region)
	x0 := b.Min.X + int(math.Round(minX*float64(b.Dx()-1)))
	y0 := b.Min.Y + int(math.Round(minY*float64(b.Dy()-1)))
	// ПЛАШКА ЦЕЛИКОМ ВНУТРИ КАДРА. Область, прижатая к правому или нижнему краю, иначе получила бы
	// половину буквы — а половина «B» это «E», и указание уезжает на соседнюю область.
	if x0+plateW > b.Max.X {
		x0 = b.Max.X - plateW
	}
	if y0+plateH > b.Max.Y {
		y0 = b.Max.Y - plateH
	}
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	draw.Draw(dst, image.Rect(x0, y0, x0+plateW, y0+plateH), image.NewUniform(c), image.Point{}, draw.Src)
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for row := 0; row < freeformGlyphRows; row++ {
		line := rows[row]
		for col := 0; col < freeformGlyphCols && col < len(line); col++ {
			if line[col] != '#' {
				continue
			}
			draw.Draw(dst, image.Rect(
				x0+pad+col*scale, y0+pad+row*scale,
				x0+pad+(col+1)*scale, y0+pad+(row+1)*scale,
			), image.NewUniform(white), image.Point{}, draw.Src)
		}
	}
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
	var budget jobBudget
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
		hasAlpha := freeformSourceHasAlpha(src)
		marked, err := freeformWithinBudget(
			fmt.Sprintf("the outlined copy of image %d", number),
			func(side int) (string, error) { return freeformOutlined(src, it.Regions, side) })
		if err != nil {
			return nil, fmt.Errorf("designgen: cannot outline the areas of image %d: %w", number, err)
		}
		if err := budget.add(marked); err != nil {
			return nil, err
		}
		out = append(out, derivedRef{
			dataURI: marked,
			caption: freeformOutlineCaption(number, it.Regions),
		})
		for r := range it.Regions {
			crop, err := freeformWithinBudget(
				fmt.Sprintf("the crop of area %s of image %d", freeformAreaLetter(r), number),
				func(side int) (string, error) { return freeformCrop(src, it.Regions[r], hasAlpha, side) })
			if err != nil {
				return nil, fmt.Errorf("designgen: cannot crop area %s of image %d: %w",
					freeformAreaLetter(r), number, err)
			}
			if err := budget.add(crop); err != nil {
				return nil, err
			}
			out = append(out, derivedRef{
				dataURI: crop,
				caption: freeformCropCaption(number, r, freeformTextForRegion(it, r)),
			})
		}
	}
	return out, nil
}

// jobBudget — СКОЛЬКО БАЙТОВ ПРОИЗВОДНЫХ УЖЕ НАБРАНО. Считается на ходу и отказывает на первой же
// производной, которая переполняет сумму: собирать остальные — это ещё несколько мегабайт в память
// процесса, который и так решено не грузить.
type jobBudget struct{ spent int }

func (b *jobBudget) add(uri string) error {
	b.spent += len(uri)
	if b.spent > freeformMaxJobBytes {
		return fmt.Errorf("%w: its pictures come to %d bytes of inline data against a ceiling of %d — "+
			"use smaller pictures, or mark fewer areas on them",
			errFreeformJobTooLarge, b.spent, freeformMaxJobBytes)
	}
	return nil
}

// freeformWithinBudget собирает производную НА ПЕРВОМ РАЗМЕРЕ, КОТОРЫЙ ВЛЕЗАЕТ В ПОТОЛОК.
//
// ⚠ ЛЕСТНИЦА ИДЁТ ПЕРЕД ОТКАЗОМ, А НЕ ВМЕСТО НЕГО, И ОБА КОНЦА ОБЯЗАТЕЛЬНЫ. Без ступени вниз
// честный кадр 4000×4000 упирался бы в потолок и убивал прогон там, где хватило бы 1024. Без отказа
// в конце лестница молча уводила бы качество вниз до любой глубины — и человек платил бы полную
// цену за кадр, собранный из миниатюр, не зная об этом.
func freeformWithinBudget(what string, build func(side int) (string, error)) (string, error) {
	widest := 0
	for _, side := range freeformSides {
		uri, err := build(side)
		if err != nil {
			return "", err
		}
		if len(uri) <= freeformMaxDerivedBytes {
			return uri, nil
		}
		if len(uri) > widest {
			widest = len(uri)
		}
	}
	return "", fmt.Errorf("%w: %s is %d bytes of inline data even at %d px, against a ceiling of %d — "+
		"this picture has to get smaller before it can be sent",
		errFreeformJobTooLarge, what, widest, freeformSides[len(freeformSides)-1], freeformMaxDerivedBytes)
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
		parts = append(parts, "area "+freeformAreaLetter(i)+" outlined in "+colour.name+
			" and lettered "+freeformAreaLetter(i))
	}
	return "image " + strconv.Itoa(number) + " with " + joinWithAnd(parts) +
		" — the outlines and the letters are markers for you, not part of the garment; never draw them"
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
//
// ⚠ ЗАГОЛОВОК ЧИТАЕТСЯ РАНЬШЕ РАСТРА, И ПОТОЛОК БАЙТОВ ЭТОГО НЕ ЗАМЕНЯЕТ. Байтовый потолок в 64 MiB
// (freeformMaxSourceBytes) ограничивает СЖАТОЕ, а в память процесса ложится РАЗВЁРНУТОЕ: PNG,
// WebP, GIF и JPEG все умеют объявить канву в гигапиксели несколькими килобайтами — это классическая
// бомба разжатия, и её цена здесь не «прогон подороже», а OOM всего воркера вместе с чужими
// оплаченными прогонами в соседних горутинах. Отказ стоит одного вызова DecodeConfig и приходит ДО
// денег: сборка задания зовётся до StartAttempt.
//
// ПОТОЛОК ТОТ ЖЕ, ЧТО У БАКЕТА И У ПРОВЕРКИ АЛЬФЫ ВЫРЕЗА (bucket.ImageWithinBudget). Одно число на
// весь процесс: копия разошлась бы молча.
func freeformDecode(raw []byte) (image.Image, error) {
	cfg, err := freeformDecodeConfig(raw)
	if err != nil {
		return nil, err
	}
	if !bucket.ImageWithinBudget(cfg.Width, cfg.Height) {
		side, pixels := bucket.ImageBudgetCeilings()
		return nil, fmt.Errorf("%w: its header declares %d×%d pixels, and this deployment unpacks at "+
			"most %d px on a side and %d pixels in all — %d bytes of file would have become tens of "+
			"gigabytes of memory. Use a smaller picture",
			errFreeformSourceTooLarge, cfg.Width, cfg.Height, side, pixels, len(raw))
	}
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

// freeformDecodeConfig — ТОЛЬКО ЗАГОЛОВОК, ни одного пикселя.
//
// ⚠ РАЗБИЕНИЕ ПО ФОРМАТАМ ЗДЕСЬ ДОСЛОВНО ТО ЖЕ, ЧТО У ДЕКОДЕРА ВЫШЕ, И ЭТО НЕСУЩЕЕ СВОЙСТВО.
// Общая image.DecodeConfig отвечала бы по ЗАРЕГИСТРИРОВАННЫМ форматам — то есть по тому, какие
// пакеты кто-то импортировал ради других надобностей, — и разошлась бы с настоящим декодером в обе
// стороны: формат, который она прочитает, а он не возьмёт (тихий отказ вместо картинки), и формат,
// который возьмёт он, а она нет (бомба мимо потолка). Одна развилка, повторённая дважды подряд,
// проверяется глазом; две разные — не проверяется никак.
func freeformDecodeConfig(raw []byte) (image.Config, error) {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return png.DecodeConfig(bytes.NewReader(raw))
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return jpeg.DecodeConfig(bytes.NewReader(raw))
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return webp.DecodeConfig(bytes.NewReader(raw))
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return gif.DecodeConfig(bytes.NewReader(raw))
	default:
		return image.Config{}, fmt.Errorf("unrecognised image format")
	}
}

// freeformSourceHasAlpha — НЕСЁТ ЛИ ИСХОДНИК ПРОЗРАЧНОСТЬ НА САМОМ ДЕЛЕ.
//
// Спрашивается у декодированной картинки, а не у расширения: WebP с альфой декодируется в NRGBA
// ровно так же, как PNG, и терять её на кропе было бы потерей ровно того, ради чего вырез
// существует.
//
// ⚠ И ЭТО ИЗМЕРЕНИЕ ПИКСЕЛЕЙ, А НЕ ТИП. Тип отвечает на вопрос «МОЖЕТ ли эта картинка нести
// альфу», и ответ «да» у ЛЮБОГО фотоснимка, загруженного PNG, — то есть у большинства. Кроп такого
// снимка уезжал PNG'ом, а PNG фотографии это втрое-впятеро больше байтов, чем JPEG q90, при
// побайтово одинаковом содержании: платит за них потолок тела запроса и память процесса, у
// которого её пол-гигабайта. Сама проверка стоит один проход по пикселям и у прозрачной картинки —
// у той, ради которой всё это, — заканчивается на первом же углу (hasTransparentPixel).
func freeformSourceHasAlpha(src image.Image) bool {
	switch src.(type) {
	case *image.NRGBA, *image.RGBA, *image.NRGBA64, *image.RGBA64, *image.Paletted:
		return hasTransparentPixel(src)
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
func freeformOutlined(src image.Image, regions []freeformRegion, side int) (string, error) {
	scaled := freeformFit(src, side)
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
	// БУКВЫ — ВТОРЫМ ПРОХОДОМ, ПОСЛЕ ВСЕХ КОНТУРОВ. Плашка обязана лежать ПОВЕРХ линий, включая
	// чужие: две соседние области в кадре — обычное дело, и контур второй, прошедший по плашке
	// первой, срезал бы букве угол ровно там, где она отличается от соседней по алфавиту.
	for i, region := range regions {
		freeformLetterPlate(canvas, region, i, freeformOutlineColours[i%len(freeformOutlineColours)].rgba)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: freeformJPEGQuality}); err != nil {
		return "", err
	}
	return freeformDataURI("image/jpeg", buf.Bytes()), nil
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
func freeformCrop(src image.Image, region freeformRegion, keepAlpha bool, side int) (string, error) {
	return freeformCropAt(src, freeformCropRect(src.Bounds(), region), keepAlpha, side)
}

// freeformCropAt — the same crop at a rectangle already computed (the generation window pads it,
// window.go), so the cut and the paste-back read one rectangle.
func freeformCropAt(src image.Image, rect image.Rectangle, keepAlpha bool, side int) (string, error) {
	rect = rect.Intersect(src.Bounds())
	if rect.Empty() {
		return "", fmt.Errorf("the area lies outside the picture")
	}
	// ⚠ NO FULL-SIZE COPY OF THE RECTANGLE (G-03 r2 follow-up): a retouch window can be most of a
	// 40 MP frame, and the copy used to be made before the downscale. A rectangle that already fits
	// the side is copied as it is (≤ side² pixels); a larger one is scaled straight out of the source.
	var scaled image.Image
	if w, h, fits := freeformFitSize(rect.Dx(), rect.Dy(), side); fits {
		crop := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
		draw.Draw(crop, crop.Bounds(), src, rect.Min, draw.Src)
		scaled = crop
	} else {
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		leanScale(dst, dst.Bounds(), src, rect)
		scaled = dst
	}

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

// freeformCropRect — ПРЯМОУГОЛЬНИК КРОПА В ПИКСЕЛЯХ ИСХОДНИКА: bbox области плюс поля.
//
// ⚠ ОН ВЫНЕСЕН ИЗ freeformCrop, ПОТОМУ ЧТО У НЕГО ПОЯВИЛСЯ ВТОРОЙ ЧИТАТЕЛЬ, И ЭТИ ДВОЕ ОБЯЗАНЫ
// ГОВОРИТЬ ОДНО И ТО ЖЕ. Первый вырезает по нему картинку, которую увидит модель; второй вклеивает
// по нему ответ обратно в кадр (window.go). Две копии этой арифметики разошлись бы на округлении —
// и результат лёг бы рядом с тем местом, куда его просили положить, на пиксель-другой мимо.
func freeformCropRect(b image.Rectangle, region freeformRegion) image.Rectangle {
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
	return image.Rect(x0, y0, x1, y1).Intersect(b)
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
//
// leanScale, not Kernel.Scale (G-03 r2 follow-up): Scale's separable pass allocates max × source
// height × 32 bytes of scratch — ≈ 590 MB for a 12000 px tall source fitted to 1536.
func freeformFit(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h, fits := freeformFitSize(b.Dx(), b.Dy(), max)
	if fits {
		return src
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	leanScale(dst, dst.Bounds(), src, b)
	return dst
}

// freeformFitSize — the w×h a picture of that size is fitted to under a long side of max; fits =
// true when it already is (it is never enlarged).
func freeformFitSize(w, h, max int) (int, int, bool) {
	long := w
	if h > long {
		long = h
	}
	if long <= max || long == 0 {
		return w, h, true
	}
	scale := float64(max) / float64(long)
	return int(math.Max(1, math.Round(float64(w)*scale))), int(math.Max(1, math.Round(float64(h)*scale))), false
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
