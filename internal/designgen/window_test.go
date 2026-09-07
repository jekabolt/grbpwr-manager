package designgen

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// windowFixture — кадр 200×200 с областью 40×40 посередине.
//
// Фон непрозрачный и заметный, ОДИН пиксель в дальнем углу прозрачен: он делает исходник
// «настоящим PNG» (альфа измеряется, а не выводится из типа), поэтому композит тоже обязан выйти
// PNG'ом — а значит пиксели вне окна можно сравнивать ТОЧНО, а не «на глаз с допуском». Через JPEG
// такое сравнение невозможно в принципе, и проба про «остальное не тронуто» превратилась бы в
// проверку допуска.
func windowFixture(t *testing.T) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			// Плавный фон: одноцветный кадр не отличил бы «вклеено на место» от «вклеено куда-то».
			m.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	m.SetNRGBA(199, 199, color.NRGBA{})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, m))
	return buf.Bytes()
}

// windowAnswer — что «вернула модель»: сплошной цвет размером с окно.
func windowAnswer(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, m))
	return buf.Bytes()
}

// windowRun — прогон плейграунда с пресетом add_hardware: кадр с одной областью и фурнитура.
func windowRun() entity.DesignRun {
	r := testRun(1, entity.DesignRunKindFreeform)
	r.Params = entity.RawJSON(`{"freeform":{"preset":"add_hardware","items":[
	  {"media_id":11,"role":"subject","texts":["a D-ring goes here"],
	   "regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.30", "0.30") + `,` + point("0.50", "0.30") + `,` +
		point("0.50", "0.50") + `,` + point("0.30", "0.50") + `]}]},
	  {"media_id":12,"role":"hardware","texts":["the D-ring"]}]}}`)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11},{"media_id":12}]}`)
	return r
}

// TestTheGenerationWindowCROPS_IN_AND_COMPOSITES_BACK.
//
// ⚠ БЕЗ ТРЕТЬЕГО ШАГА ПЕРВЫЕ ДВА БЕСПОЛЕЗНЫ. Пуговица занимает один-три процента кадра; модель,
// которой отдали ВЕСЬ кадр, пересоздаёт его целиком — маска у этого семейства мягкая, «остального
// не трону» она не обещает. То есть ради пуговицы человек получает другую фотографию. Приём
// (generation window) — это кроп → генерация по кропу → ВКЛЕЙКА обратно; без вклейки наружу
// выходит кроп вместо кадра, и разницы с «пересоздали фотографию» никакой, только хуже.
//
// Проба держит всю цепочку одним проходом воркера и утверждает четыре вещи:
//  1. к модели уехал КРОП, а полный кадр — НЕТ (иначе она ответит на кадр);
//  2. наружу вышел КАДР исходного размера, а не кроп;
//  3. пиксели ВНЕ окна побайтово равны исходнику — это и есть «остальное не тронуто», и здесь
//     это факт про наш композит, а не просьба к модели;
//  4. пиксели ВНУТРИ окна — это ответ модели, то есть вклеено именно то и именно туда.
func TestTheGenerationWindowCROPS_IN_AND_COMPOSITES_BACK(t *testing.T) {
	src := windowFixture(t)
	objs := &fakeObjects{byKey: map[string][]byte{
		"m/11.png": src,
		"m/12.png": windowAnswer(t, 24, 24, color.NRGBA{R: 10, G: 10, B: 10, A: 255}),
	}}
	// Окно: bbox 0.30..0.50 плюс 8 % его размера с каждой стороны → 56..104 в пикселях кадра.
	const x0, y0, x1, y1 = 56, 56, 104, 104
	answer := color.NRGBA{R: 255, G: 0, B: 255, A: 255}

	prov := &fakeProvider{name: "image", out: &Outcome{
		Artifacts: []Artifact{{
			Bytes:       windowAnswer(t, x1-x0, y1-y0, answer),
			ContentType: ContentTypePNG,
		}},
	}}
	store := &fakeStore{}
	sink := newFakeSink(ContentTypePNG, ContentTypeJPEG)
	w := testWorker(store, media(11, 12), sink, Providers{Image: prov})
	w.objects = objs

	require.NoError(t, w.execute(context.Background(), windowRun(), "tok"))

	// ─── 1. ЧТО УВИДЕЛА МОДЕЛЬ ───
	require.Len(t, prov.calls, 1)
	job := prov.calls[0]
	require.NotNil(t, job.Window, "оконный прогон обязан унести с собой координаты вклейки")
	require.NotContains(t, job.References, "https://cdn.example/m/11.png",
		"ПОЛНЫЙ КАДР НЕ ЕДЕТ: получив его, модель ответит на него — и вклеивать станет нечего")
	require.Contains(t, job.References, "https://cdn.example/m/12.png", "фурнитура едет как была")
	require.Len(t, job.References, 2,
		"ровно две картинки: фурнитура и кроп. ОБВЕДЁННОЙ КОПИИ ЗДЕСЬ БЫТЬ НЕ ДОЛЖНО — это тот же "+
			"полный кадр, только с контуром, и модель ответила бы на него")
	require.True(t, strings.HasPrefix(job.References[len(job.References)-1], "data:image/"),
		"кроп области — последняя картинка вызова, значит и последняя подпись")
	require.Contains(t, job.Prompt, "is a CLOSE CROP",
		"без этих слов модель читает крупный кусок ткани как самостоятельный предмет и отвечает "+
			"натюрмортом, который мы потом вклеим в кадр")

	// ─── 2. ЧТО ВЫШЛО НАРУЖУ ───
	require.Len(t, sink.put, 1)
	require.Equal(t, ContentTypePNG, sink.putTypes[0],
		"исходник нёс альфу, значит композит обязан быть PNG: JPEG вернул бы белый прямоугольник "+
			"на месте прозрачности")
	got, err := png.Decode(bytes.NewReader(sink.putBytes[0]))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 200, 200), got.Bounds(),
		"наружу выходит КАДР, а не кроп: человек просил дополнить свою фотографию")

	orig, err := png.Decode(bytes.NewReader(src))
	require.NoError(t, err)

	// ─── 3. ВНЕ ОКНА — ИСХОДНИК, ПОБАЙТОВО ───
	for _, p := range []image.Point{{X: 0, Y: 0}, {X: 10, Y: 190}, {X: 150, Y: 20}, {X: x0 - 1, Y: y0 - 1},
		{X: x1, Y: y1}, {X: 199, Y: 199}} {
		require.Equal(t, orig.At(p.X, p.Y), got.At(p.X, p.Y),
			"пиксель (%d,%d) вне окна обязан остаться тем же, каким был", p.X, p.Y)
	}

	// ─── 4. ВНУТРИ ОКНА — ОТВЕТ МОДЕЛИ ───
	for _, p := range []image.Point{{X: x0, Y: y0}, {X: 80, Y: 80}, {X: x1 - 1, Y: y1 - 1}} {
		require.Equal(t, color.NRGBA{R: answer.R, G: answer.G, B: answer.B, A: 255}, got.At(p.X, p.Y),
			"пиксель (%d,%d) внутри окна обязан быть тем, что вернула модель", p.X, p.Y)
	}

	require.Len(t, store.completed, 1)
	require.Len(t, store.completed[0].Outputs, 1, "оригинал кропа наружу не идёт: он часть композита")
	require.Empty(t, store.finished[0].ErrorCode)
}

// TestAWindowIsNotTakenWhenTHE_ASK_IS_NOT_LOCAL — отрицательный контроль на три условия плана.
//
// Каждое из них — своя причина, и все три обязаны быть проверены отдельно, иначе «окно берётся
// всегда» проходит пробу выше.
func TestAWindowIsNotTakenWhenTHE_ASK_IS_NOT_LOCAL(t *testing.T) {
	area := func(cx, cy string) string {
		return `{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
			point(cx, cy) + `,` + point("0.9", cy) + `,` + point("0.9", "0.9") + `]}`
	}
	for _, c := range []struct {
		name   string
		params string
		want   bool
	}{
		{"add_hardware, одна область на предмете", `{"freeform":{"preset":"add_hardware","items":[
		  {"media_id":11,"role":"subject","regions":[` + area("0.1", "0.1") + `]}]}}`, true},
		{"другой пресет — просьба не локальна", `{"freeform":{"preset":"repaint_parts","items":[
		  {"media_id":11,"role":"subject","regions":[` + area("0.1", "0.1") + `]}]}}`, false},
		{"две области — два окна и другая арифметика денег", `{"freeform":{"preset":"add_hardware","items":[
		  {"media_id":11,"role":"subject","regions":[` + area("0.1", "0.1") + `,` + area("0.2", "0.2") + `]}]}}`, false},
		{"области на двух картинках", `{"freeform":{"preset":"add_hardware","items":[
		  {"media_id":11,"role":"subject","regions":[` + area("0.1", "0.1") + `]},
		  {"media_id":12,"role":"subject","regions":[` + area("0.2", "0.2") + `]}]}}`, false},
		{"область размечена на самой фурнитуре", `{"freeform":{"preset":"add_hardware","items":[
		  {"media_id":12,"role":"hardware","regions":[` + area("0.1", "0.1") + `]}]}}`, false},
		{"ни одной области", `{"freeform":{"preset":"add_hardware","items":[
		  {"media_id":11,"role":"subject"}]}}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := freeformWindowPlan(parseParams(entity.RawJSON(c.params)))
			require.Equal(t, c.want, plan != nil)
		})
	}
}

// TestAWindowThatCannotBeCompositedKEEPS_THE_PICTURE_AND_SAYS_SO.
//
// Всё, что может пойти не так при вклейке, происходит ПОСЛЕ того, как деньги ушли, и ни одна из
// причин не делает купленный кроп бесполезным: на нём видно, как села фурнитура. Поэтому — жалоба
// рядом с картинкой, а не отказ вместо неё; тот же шов, что у pattern_not_seamless. Но кроп это НЕ
// ТО, что просили, и человек, увидев в ленте крупный кусок вместо кадра, обязан найти в строке
// попытки, почему.
func TestAWindowThatCannotBeCompositedKEEPS_THE_PICTURE_AND_SAYS_SO(t *testing.T) {
	objs := &fakeObjects{
		byKey: map[string][]byte{
			"m/11.png": windowFixture(t),
			"m/12.png": windowAnswer(t, 24, 24, color.NRGBA{R: 10, G: 10, B: 10, A: 255}),
		},
		// Первое чтение (сборка задания) проходит, второе (вклейка, уже после оплаты) падает.
		failAfter: 1,
	}
	crop := windowAnswer(t, 48, 48, color.NRGBA{R: 255, G: 0, B: 255, A: 255})
	prov := &fakeProvider{name: "image", out: &Outcome{
		Artifacts: []Artifact{{Bytes: crop, ContentType: ContentTypePNG}},
	}}
	store := &fakeStore{}
	sink := newFakeSink(ContentTypePNG, ContentTypeJPEG)
	w := testWorker(store, media(11, 12), sink, Providers{Image: prov})
	w.objects = objs

	require.NoError(t, w.execute(context.Background(), windowRun(), "tok"))

	require.Len(t, sink.put, 1, "купленный кроп сохранён: выбросить оплаченное дороже, чем показать не то")
	require.Equal(t, crop, sink.putBytes[0], "наружу ушёл ответ модели как есть")
	require.Len(t, store.completed, 1, "прогон закрывается done: картинка есть")
	require.Equal(t, CodeWindowNotComposited, store.finished[0].ErrorCode)
	require.Equal(t, entity.DesignAttemptDelivered, store.finished[0].State)
	require.Empty(t, store.failed)
}

// TestACompositeREFUSES_A_FRAME_THAT_IS_NOT_THE_ONE_IT_WAS_CUT_FROM.
//
// Замороженный прямоугольник назван в пикселях КОНКРЕТНОГО кадра. Кадр другого размера значит, что
// читается другая картинка, и «подгоним масштабом» положило бы фурнитуру в правдоподобное, но
// чужое место — а такой кадр от честного не отличить ни человеку, ни истории.
func TestACompositeREFUSES_A_FRAME_THAT_IS_NOT_THE_ONE_IT_WAS_CUT_FROM(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	win := GenerationWindow{
		Rect:   image.Rect(10, 10, 30, 30),
		Bounds: image.Rect(0, 0, 200, 200),
	}
	_, err := compositeWindow(src, win, windowAnswer(t, 20, 20, color.NRGBA{A: 255}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "frozen against")
}
