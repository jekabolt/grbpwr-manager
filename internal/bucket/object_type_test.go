package bucket

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ObjectMediaType — ЧИТАТЕЛЬ ТОГО ЖЕ ОТОБРАЖЕНИЯ, КОТОРЫМ ПАКЕТ ПИШЕТ ИМЯ ОБЪЕКТА.
//
// Он стоит у ДЕНЕЖНОЙ двери (designRefuseNonPictureInputs), и обе его ошибки дороги в разные
// стороны: соврать «картинка» — оплаченный отказ у поставщика, соврать «не картинка» — зарубленный
// законный прогон. Поэтому проверяются оба ответа и ТРЕТИЙ — «не знаю».

// ОБРАТНОЕ ОТОБРАЖЕНИЕ ОБЯЗАНО БЫТЬ ОДНОЗНАЧНЫМ. Два типа под одним расширением сделали бы один из
// них невосстановимым — молча, и именно у денежной двери.
func TestExtensionToMimeTypeIsInjective(t *testing.T) {
	seen := map[string]ContentType{}
	for ct, ext := range mimeTypeToFileExtension {
		if prev, dup := seen[ext]; dup {
			t.Fatalf("extension %q is written for both %q and %q: one of them is unrecoverable", ext, prev, ct)
		}
		seen[ext] = ct
	}
	require.Len(t, extensionToMimeType, len(mimeTypeToFileExtension),
		"инверсия обязана сохранить каждый тип")
}

// АДРЕСА РОВНО ТОЙ ФОРМЫ, КАКАЯ ЛЕЖИТ НА БЕТЕ. Все 195 строк media там опознаются: 128 webp,
// 61 png, 3 svg, 2 glb, 1 mp4 — и ни одной с неизвестным расширением.
func TestObjectMediaTypeReadsWhatThisPackageWrote(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want string
		ok   bool
	}{
		{"https://cdn.grbpwr.com/grbpwr-com/design/2026/september/run-20-0-og.glb", "model/gltf-binary", true},
		{"https://cdn.grbpwr.com/grbpwr-com/design/2026/september/run-19-0-og.svg", "image/svg+xml", true},
		{"https://cdn.grbpwr.com/grbpwr-com/design/2026/september/a1b2c3-og.png", "image/png", true},
		{"https://cdn.grbpwr.com/grbpwr-com/design/2026/september/d4e5f6-og.webp", "image/webp", true},
		{"https://cdn.grbpwr.com/grbpwr-com/2026/july/202607221009309618576.mp4", "video/mp4", true},
		// Регистр — косметика адреса, а не другой тип.
		{"https://cdn.grbpwr.com/a/B/C.GLB", "model/gltf-binary", true},
		// Хвост запроса не часть имени объекта.
		{"https://cdn.grbpwr.com/a/b/c.png?v=2", "image/png", true},
		// «Не знаю» — третий ответ, и он ОБЯЗАН отличаться от обоих: у двери он читается как
		// «пропустить», и превращать его в тип значило бы придумать факт.
		{"https://cdn.grbpwr.com/legacy/no-extension-here", "", false},
		{"", "", false},
		{"https://cdn.grbpwr.com/a/b/c.heic", "", false},
	} {
		got, ok := ObjectMediaType(tc.url)
		require.Equalf(t, tc.ok, ok, "url %q", tc.url)
		require.Equalf(t, tc.want, got, "url %q", tc.url)
	}
}

// ⚠ ЗАМЕР, КОТОРЫЙ РЕШИЛ ВЫБОР ПРЕДИКАТА, ЗАПЕРТЫЙ ПРОБОЙ: НУЛЕВЫЕ РАЗМЕРЫ НЕ ГОДЯТСЯ.
//
// Второй кандидат на «это не растр» был `width == 0 && height == 0` у строки media. На бете он
// опознаёт 2 модели и 1 видео и ПРОПУСКАЕТ ВСЕ ТРИ SVG: вектор хранит размер, объявленный в его
// собственном viewBox (svgPixelSize), и строки читаются 502×865, 528×851, 528×851. Здесь это
// проверяется на той самой функции, а не на списке чисел: расширение — факт, который путь хранения
// ЗАЯВИЛ, а нулевой размер — следствие, верное лишь для части типов.
func TestZeroDimensionsWouldHaveMissedTheVectors(t *testing.T) {
	stats, err := inspectForTest(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 528 851"></svg>`)
	require.NoError(t, err)
	w, h := svgPixelSize(stats)
	require.Equal(t, [2]int{528, 851}, [2]int{w, h},
		"у хранимого вектора размеры НЕ нулевые — значит предикат «0×0» его не поймал бы")

	ct, ok := ObjectMediaType("https://cdn.grbpwr.com/x/run-19-0-og.svg")
	require.True(t, ok)
	require.Equal(t, "image/svg+xml", ct, "а расширение ловит его всегда")
}
