package designgen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/stretchr/testify/require"
)

// ПРОБА ПОДСКАЗКИ О ПОВЕРХНОСТИ (`texture_prompt`) И ТОГО, ЧТО ПРО НЕЁ ГОВОРИТ ИСТОРИЯ.
//
// ЧТО ОНА ДОКАЗЫВАЕТ И ПОЧЕМУ ИМЕННО ТАК. Она смотрит на ИСХОДЯЩИЙ ЗАПРОС, а не на возврат
// хелпера: единственное утверждение, которое здесь чего-то стоит, — «то, что реально ушло в сеть».
// Проба, читающая хелпер, зеленела бы и в том мире, где место вызова хелпер игнорирует.
//
// И ВТОРАЯ ПОЛОВИНА — КОЛОНКА `design_run.prompt`. Панель истории показывает её человеку как
// «промпт прогона» рядом с настоящей ценой, поэтому проба сверяет ОТПРАВЛЕННОЕ С ЗАПИСАННЫМ одной
// строкой сравнения: два разных текста здесь и были дефектом, а не разница формулировок.

// ─────────────────────────── стенды ───────────────────────────

// threedSteerStand — поставщик Meshy, который ЗАПОМИНАЕТ тело сабмита. Отвечает так же, как
// настоящий: {"result": "<task id>"}.
type threedSteerStand struct {
	srv  *httptest.Server
	body chan string
}

func newThreedSteerStand(t *testing.T) *threedSteerStand {
	t.Helper()
	st := &threedSteerStand{body: make(chan string, 4)}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		st.body <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"task-777"}`))
	}))
	t.Cleanup(st.srv.Close)
	return st
}

// sentPrompt достаёт `texture_prompt` из перехваченного тела. Разбирается JSON, а не ищется
// подстрока: подстрока сошлась бы и с полем, которого в запросе нет.
func (st *threedSteerStand) sentPrompt(t *testing.T) string {
	t.Helper()
	return sentBody(t, st.body).TexturePrompt
}

// falSubmitStand — тот же приём для очереди fal: перехватывает тело сабмита и отвечает id.
type falSubmitStand struct {
	srv  *httptest.Server
	body chan string
}

func newFalSubmitStand(t *testing.T) *falSubmitStand {
	t.Helper()
	st := &falSubmitStand{body: make(chan string, 4)}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		st.body <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-777"}`))
	}))
	t.Cleanup(st.srv.Close)
	return st
}

// falBody — поля обоих семейств разом: проба обязана уметь сказать не только «что уехало», но и
// «какого поля в теле НЕ БЫЛО».
type falBody struct {
	TexturePrompt string   `json:"texture_prompt"`
	ImageURLs     []string `json:"image_urls"`
	FrontImageURL string   `json:"front_image_url"`
}

func sentBody(t *testing.T, ch chan string) falBody {
	t.Helper()
	select {
	case raw := <-ch:
		var body falBody
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("тело сабмита не разобралось: %v (%s)", err, raw)
		}
		return body
	default:
		t.Fatal("сабмита не было вовсе: запрос до поставщика не доехал")
		return falBody{}
	}
}

func newThreedSteerProvider(t *testing.T, baseURL string) Provider {
	t.Helper()
	return NewThreedProvider(meshy.New(meshy.Config{APIKey: "k", BaseURL: baseURL}))
}

// falRoute — маршрут fal с ЯВНО названным слагом. Слаг здесь параметр пробы, а не умолчание:
// именно он решает, несёт ли тело слова вообще.
func falRoute(t *testing.T, baseURL, model string) Provider {
	t.Helper()
	return NewFalThreedProvider(fal.New(fal.Config{
		APIKey: "k", BaseURL: baseURL, Model3D: model,
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond,
		PollTimeout: 200 * time.Millisecond, DownloadTimeout: 2 * time.Second,
	}))
}

// ─────────────────────────── снимок прогона ───────────────────────────

// steerRun — 3D-прогон НА РЕАЛИСТИЧНОМ ЗАМОРОЖЕННОМ СНИМКЕ: тот самый, на котором владелец
// пожаловался. Просьба, заметка про изделие («перекрещенные лямки на СПИНЕ»), посадка, рецепт
// цвета, подача — и две плиты верстака, перёд и спина.
//
// СНИМОК, А НЕ РУЧНОЙ Job: предмет проверки — то, что собирает buildJob из замороженного JSON,
// поэтому проба обязана пройти через тот же разбор, что и живой прогон.
func steerRun(id int) entity.DesignRun {
	r := testRun(id, entity.DesignRunKindThreed)
	r.Ask = nullString("build the turntable of this top")
	r.Params = entity.RawJSON(`{
	  "views": ["front","back"],
	  "layout": "per_view",
	  "colour": {
	    "source": "picker",
	    "code": "BLK",
	    "hex": "#0a0a0a",
	    "words": "matte heavy jersey with a slight sheen"
	  },
	  "threed": {"presentation": "model", "body_type": "athletic", "fit_override": "slim"}
	}`)
	r.Inputs = entity.RawJSON(`{
	  "garment_note": "sleeveless fitted top; plain scoop front; crossed straps on the back",
	  "fit": "slim",
	  "slots": [
	    {"view_key": "front", "media_id": 21},
	    {"view_key": "back", "media_id": 22}
	  ]
	}`)
	return r
}

func steerWorker(t *testing.T, st *fakeStore, prov Provider) *Worker {
	t.Helper()
	return testWorker(st, media(21, 22), newFakeSink(ContentTypeGLB, ContentTypePNG),
		Providers{Threed: prov})
}

// ─────────────────────────── пробы ───────────────────────────

// ПОДСКАЗКА НЕСЁТ ПОВЕРХНОСТЬ И НЕ НЕСЁТ СИЛУЭТ.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `TexturePrompt: job.Prompt` (то есть прежний textureSteer — полный
// промпт прогона, обрезанный по потолку). Тогда в текстурную стадию уезжает «crossed straps on the
// back» — черта СПИНЫ, которую текстура штампует там, где красит, не понимая «где», — и нумерованные
// подписи к картинкам, протокола которых у этого поля нет вовсе.
//
// ВТОРАЯ МУТАЦИЯ: выбросить из стира рецепт цвета (оставить одну подачу). Тогда единственное, ради
// чего это поле существует, перестаёт до поставщика доезжать.
func TestThreedSteerCarriesTheSurfaceAndNotTheSilhouette(t *testing.T) {
	stand := newThreedSteerStand(t)
	st := &fakeStore{}
	w := steerWorker(t, st, newThreedSteerProvider(t, stand.srv.URL))

	require.NoError(t, w.execute(context.Background(), steerRun(31), "tok"))
	sent := stand.sentPrompt(t)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ПЕРВЫМ: пустая подсказка «не содержит лямок» ничуть не хуже правильной,
	// и без этой строки проба зеленела бы на реализации, выбрасывающей поле целиком.
	require.NotEmpty(t, strings.TrimSpace(sent), "подсказка ушла пустой: поверхность перестала управляться вовсе")

	require.Contains(t, sent, "BLK", "утверждение цвета — то единственное, ради чего это поле есть")
	require.Contains(t, sent, "#0a0a0a", "точное значение цвета обязано доехать")
	require.Contains(t, sent, "matte heavy jersey", "слова про ткань — вторая законная половина")
	require.Contains(t, sent, "presentation model", "подача меняет то, как обязана выглядеть поверхность")

	require.NotContains(t, sent, "crossed straps",
		"черта СПИНЫ в подсказке текстуры: стадия красит описанное там, где красит, не понимая «где»")
	require.NotContains(t, sent, "turntable", "просьба — приказ генератору картинок, а не описание поверхности")
	require.NotContains(t, sent, "image 1", "нумерованные подписи — протокол, которого у texture_prompt нет")
	require.NotContains(t, sent, "athletic", "телосложение — форма, а не поверхность; её несут плиты")
	require.NotContains(t, sent, "slim", "посадка — форма, а не поверхность")

	// И ПОТОЛОК ПОСТАВЩИКА ВЫДЕРЖАН БЕЗ ЕДИНОГО РЕЗА: стир короток по построению, поэтому
	// meshy.Submit его принял (иначе сабмита выше просто не случилось бы).
	require.LessOrEqual(t, len([]rune(sent)), meshy.MaxTexturePrompt)
}

// В КОЛОНКЕ ИСТОРИИ ЛЕЖИТ РОВНО ТО, ЧТО УЕХАЛО.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `RecordRunPrompt(..., job.Prompt)` — сегодняшнее поведение до этой
// волны. Панель показывала владельцу полный composePrompt рядом с настоящей ценой, а поставщик этих
// слов не видел: приписанные деньгам слова хуже пустой колонки, потому что выглядят уликой.
func TestThreedHistoryStoresExactlyWhatWasSent(t *testing.T) {
	stand := newThreedSteerStand(t)
	st := &fakeStore{}
	w := steerWorker(t, st, newThreedSteerProvider(t, stand.srv.URL))

	require.NoError(t, w.execute(context.Background(), steerRun(32), "tok"))
	sent := stand.sentPrompt(t)

	require.Len(t, st.recordedPrompts, 1, "проход обязан записать текст ровно один раз")
	require.Equal(t, sent, st.recordedPrompts[0],
		"записанное и отправленное — одна строка, а не две сборки")
	require.NotContains(t, st.recordedPrompts[0], "crossed straps",
		"в истории не должно оказаться слов, которых поставщик не получал")
}

// МАРШРУТ, КОТОРЫЙ СЛОВ НЕ НЕСЁТ, ЗАПИСЫВАЕТ ПУСТО.
//
// У hitem3d в теле НЕТ текстового поля вовсе — это и было измерено на бете: оба прогона ушли
// четырьмя ссылками и без единого слова, а история показывала полный промпт.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `SentPrompt` без проверки семейства (всегда возвращает стир). Тогда
// колонка снова утверждает про деньги то, чего не было.
func TestHitem3dCarriesNoWordsAndTheColumnSaysSo(t *testing.T) {
	stand := newFalSubmitStand(t)
	st := &fakeStore{}
	w := steerWorker(t, st, falRoute(t, stand.srv.URL, "hitem3d/hi3d/v3.0/multi-view-to-3d"))

	// Проход дойдёт до сбора и провалится там (стенд не отдаёт модель) — нас интересует сабмит.
	_ = w.execute(context.Background(), steerRun(33), "tok")

	body := sentBody(t, stand.body)
	require.NotEmpty(t, body.FrontImageURL, "положительный контроль: именованное тело всё-таки ушло")
	require.Empty(t, body.TexturePrompt, "у hitem3d нет поля под слова: их некуда положить")
	require.Empty(t, body.ImageURLs, "именованное семейство не шлёт безымянный список")

	require.Len(t, st.recordedPrompts, 1)
	require.Equal(t, "", st.recordedPrompts[0],
		"маршрут слов не несёт — значит и в колонке слов быть не может")
}

// МАРШРУТ MESHY ЧЕРЕЗ fal: СТИР ЕДЕТ, СПИСОК СОБИРАЕТСЯ, КОЛОНКА СОВПАДАЕТ.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: не передавать `req.TexturePrompt = job.SurfaceSteer` в Execute —
// тогда единственный текст маршрута молча исчезает, а колонка обещает его.
//
// ⚠ ЧЕГО ЭТА ПРОБА НЕ ДОКАЗЫВАЕТ, ХОТЯ РАНЬШЕ УТВЕРЖДАЛА ОБРАТНОЕ. Здесь стояло «вторая мутация:
// собрать image_urls в порядке снимка, не начиная с переда». Покраснеть на ней она не могла:
// `referenceList` сортирует плиты по `viewRank` ДО всего остального, поэтому на всяком достижимом
// входе порядок снимка И ЕСТЬ «перёд первым», и позиционный `falViews` даёт ту же строку. Проверено:
// подмена falViews чисто позиционным оставляет эту пробу зелёной. Порядок в теле сторожит проба
// самого транспорта (fal.TestTheMESHY_FAMILY_SENDS_AN_ORDERED_LIST_WITH_THE_FRONT_FIRST), а
// отображение вида в слот — TestFalRefusesARunWhoseFrontPlateVanished ниже, где два порядка
// РАСХОДЯТСЯ.
func TestFalMeshySendsTheSteerWithTheFrontFirst(t *testing.T) {
	stand := newFalSubmitStand(t)
	st := &fakeStore{}
	w := steerWorker(t, st, falRoute(t, stand.srv.URL, "meshy/v7/multi-image-to-3d"))

	_ = w.execute(context.Background(), steerRun(34), "tok")

	body := sentBody(t, stand.body)
	require.Empty(t, body.FrontImageURL, "у meshy именованных полей нет — их присутствие означало бы чужое тело")
	require.Equal(t, []string{
		"https://cdn.example/m/21.png",
		"https://cdn.example/m/22.png",
	}, body.ImageURLs, "перёд обязан стоять НУЛЕВЫМ: это единственная гарантия безымянного списка")

	require.Contains(t, body.TexturePrompt, "BLK", "стир обязан доехать этим маршрутом")
	require.NotContains(t, body.TexturePrompt, "crossed straps")
	require.Len(t, st.recordedPrompts, 1)
	require.Equal(t, body.TexturePrompt, st.recordedPrompts[0],
		"записанное и отправленное — одна строка")
}

// ПЕРЁД ПРОПАЛ МЕЖДУ СНИМКОМ И ПРОХОДОМ — И ЭТО ЕДИНСТВЕННЫЙ ДОСТИЖИМЫЙ ВХОД, ГДЕ «ПОРЯДОК СНИМКА»
// И «ПЕРЁД ПЕРВЫМ» РАСХОДЯТСЯ.
//
// ⚠ ПОЧЕМУ ФИКСТУРА ИМЕННО ТАКАЯ. `referenceList` сортирует плиты по `viewRank`, поэтому на всяком
// целом снимке первая плита — перёд, и позиционное правило неотличимо от именного. Различить их
// можно ровно там, где ПЕРВОЙ плиты в выдаче нет: строка медиа переда исчезла между заморозкой
// снимка и проходом (buildJob пропускает нерезолвящуюся картинку — см. его же комментарий), и
// `References[0]` оказывается СПИНОЙ, а `ReferenceViews[0]` честно говорит «back».
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `falViews`, раскладывающий ссылки ПО ПОЗИЦИИ вместо `ReferenceViews`.
// Тогда спина уезжает в `FrontURL`, поставщик строит модель лицом назад, прогон закрывается `done`,
// деньги списаны — тихий успех, неотличимый по истории от честной сборки.
//
// ЧЕМ ОНА ОТЛИЧАЕТСЯ ОТ TestTheFalBuildMapsViewsBY_NAME_NOT_BY_POSITION, КОТОРАЯ УЖЕ ЕСТЬ. Та
// сторожит ту же мутацию НА ЕДИНИЦЕ и делает это собранным вручную Job, чей список начинается со
// спины, — вход, которого `buildJob` не производит (плиты сортируются по `viewRank`). Эта — тот же
// довод НА ДОСТИЖИМОМ прогоне, через настоящие разбор снимка и резолв медиа, и потому отвечает на
// вопрос «а бывает ли так вообще» словом «да, вот так».
func TestFalRefusesARunWhoseFrontPlateVanished(t *testing.T) {
	stand := newFalSubmitStand(t)
	st := &fakeStore{}
	// Верстак называет перёд (21) и спину (22); резолвится только спина.
	w := testWorker(st, media(22), newFakeSink(ContentTypeGLB, ContentTypePNG),
		Providers{Threed: falRoute(t, stand.srv.URL, "meshy/v7/multi-image-to-3d")})

	require.NoError(t, w.execute(context.Background(), steerRun(35), "tok"))

	select {
	case raw := <-stand.body:
		t.Fatalf("сабмит ушёл на снимке без переда: куплена модель лицом назад (%s)", raw)
	default:
	}

	require.Len(t, st.failed, 1, "прогон обязан быть записан провалившимся, а не тихо закрыт")
	require.Contains(t, st.failed[0].LastError, "no front plate",
		"отказ обязан говорить словарём верстака, а не именем поля провайдера")
	require.False(t, st.failed[0].Retryable,
		"снимок не починится повтором: та же пропавшая картинка на пятом проходе")
}

// ДВЕ ПЛИТЫ ОДНОГО ВИДА — ОТКАЗ, А НЕ МОЛЧАЛИВАЯ ПЕРЕЗАПИСЬ.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `req.FrontURL = u` без проверки занятости — прежняя строка. Две
// плиты переда это два разных рисунка, и выбор последнего по порядку списка — покупка модели,
// которую никто не выбирал. Сегодня верстак дубль не породит, но реран исполняет ЗАМОРОЖЕННЫЙ
// снимок, а снимки доколорвейной эпохи такие пары несут законно.
func TestFalViewsRefusesTwoPlatesOfOneSide(t *testing.T) {
	dup := Job{
		References:     []string{"https://cdn.example/a.png", "https://cdn.example/b.png"},
		ReferenceViews: []string{entity.DesignViewFront, entity.DesignViewFront},
	}
	_, err := falViews(dup)
	require.ErrorIs(t, err, errDuplicateView, "дубль вида обязан быть отказом")
	require.Contains(t, err.Error(), entity.DesignViewFront, "отказ обязан назвать сторону")

	// ОТКАЗ ОБЯЗАН БЫТЬ ТЕРМИНАЛЬНЫМ. Классифицированный как погода, он сжёг бы весь потолок
	// попыток на снимке, который не может стать отправляемым.
	v := classify(err)
	require.False(t, v.Retryable, "снимок не починится повтором: тот же дубль на пятом проходе")
	require.Equal(t, CodeBadRequest, v.Code)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: без дубля тот же путь проходит и раскладывает виды по именам.
	ok := Job{
		References:     []string{"https://cdn.example/a.png", "https://cdn.example/b.png"},
		ReferenceViews: []string{entity.DesignViewFront, entity.DesignViewBack},
	}
	req, err := falViews(ok)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example/a.png", req.FrontURL)
	require.Equal(t, "https://cdn.example/b.png", req.BackURL)
}

// СОСТАВ СТИРА, ПРОЧИТАННЫЙ ПРЯМО.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: дописать в стир ещё одно поле параметров (посадку, телосложение) или
// потерять вторую ткань многотканевого прогона. Первое возвращает в текстуру форму, второго не
// видно ни в одном сквозном прогоне, потому что вторая ткань живёт только в списке.
func TestSurfaceSteerIsTheColourTheClothsAndThePresentation(t *testing.T) {
	ctx := context.Background()
	p := runParams{
		Colour: &colourRecipe{
			Code: "BLK", Hex: "#0a0a0a", Words: "matte heavy jersey",
			Fabrics: []fabricUse{
				{Name: "body cloth", ColourCode: "BLK", ColourHex: "#0a0a0a", Words: "matte heavy jersey"},
				{Name: "contrast rib", ColourCode: "RED", Words: "ribbed knit", Parts: "cuffs and collar"},
			},
		},
		Threed: &threedParams{Presentation: "model", BodyType: "athletic", FitOverride: "slim"},
	}
	steer := surfaceSteer(ctx, p)

	require.Contains(t, steer, "colourway BLK — the exact value is #0a0a0a")
	require.Contains(t, steer, "matte heavy jersey")
	require.Contains(t, steer, "cuffs and collar: contrast rib, colourway RED, ribbed knit",
		"вторая ткань живёт ТОЛЬКО в списке: потеряв её, прогон красит всё изделие первой")
	require.Contains(t, steer, "presentation model")
	require.NotContains(t, steer, "athletic")
	require.NotContains(t, steer, "slim")

	// МОЛЧАНИЕ ПРАВДИВО: прогон, который про поверхность ничего не сказал, шлёт пусто, а не
	// выдуманный цвет. Пустое поле опускается телом запроса целиком.
	require.Equal(t, "", surfaceSteer(ctx, runParams{}))

	// И НЕЗАПОЛНЕННАЯ ПОДАЧА НЕ ПРЕВРАЩАЕТСЯ В СЛОВО «presentation».
	//
	// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `add("presentation " + t.Presentation)` без проверки на пустоту.
	// Контракт зовёт пустое значение «not stated; the generator picks», а голая метка — слово,
	// которое текстурной стадии пришлось бы ИСТОЛКОВАТЬ; подсказке этого нельзя.
	require.Equal(t, "", surfaceSteer(ctx, runParams{Threed: &threedParams{}}),
		"незаполненная подача обязана молчать, а не слать метку без значения")
	require.Equal(t, "presentation air", surfaceSteer(ctx, runParams{Threed: &threedParams{Presentation: "air"}}),
		"положительный контроль: заполненная подача едет")
}

// ПЕРВАЯ ТКАНЬ НЕ ГОВОРИТСЯ ДВАЖДЫ — НИ ПРИ ОДНОЙ ТКАНИ, НИ ПРИ ДВУХ.
//
// ⚠ ПРЕЖНЯЯ ПРОБА СТОРОЖИЛА НЕ ТУ ВЕТКУ. Она считала «colourway BLK» на ОДНОТКАНЕВОМ рецепте — там,
// где цикл по списку не исполняется вовсе, — и потому зеленела, пока многотканевый стир повторял
// первую ткань следом за скалярами. ЗАМЕРЕНО на этой самой паре тканей: «colourway BLK» дважды и
// «matte heavy jersey» дважды в одной строке.
//
// ЧТО ЗДЕСЬ ЗА ПРАВИЛО. Контракт colourRecipe.Fabrics говорит, что клиент повторяет цвет и слова
// ПЕРВОЙ ткани в скаляры `code`/`hex`/`words`. Значит скаляры — это и есть ткань один; список
// начинается со второй, у которой скаляры сказать нечего.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `for _, f := range cloths` вместо `cloths[1:]`. ИГЛА УНИКАЛЬНА:
// считается ЧИСЛО вхождений, поэтому «потерять вторую ткань» этой пробой не позеленеет — на неё
// стоит отдельное require ниже.
func TestSurfaceSteerNeverSaysThePrimaryClothTwice(t *testing.T) {
	ctx := context.Background()
	two := runParams{Colour: &colourRecipe{
		Code: "BLK", Hex: "#0a0a0a", Words: "matte heavy jersey",
		Fabrics: []fabricUse{
			{Name: "body cloth", ColourCode: "BLK", ColourHex: "#0a0a0a", Words: "matte heavy jersey"},
			{Name: "contrast rib", ColourCode: "RED", Words: "ribbed knit", Parts: "cuffs and collar"},
		},
	}}
	steer := surfaceSteer(ctx, two)

	require.Equalf(t, 1, strings.Count(steer, "colourway BLK"),
		"цвет первой ткани назван дважды: скаляры и есть её эхо (%s)", steer)
	require.Equalf(t, 1, strings.Count(steer, "matte heavy jersey"),
		"слова первой ткани названы дважды (%s)", steer)
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: «не повторять первую» не имеет права стать «не слать список вовсе».
	require.Contains(t, steer, "cuffs and collar: contrast rib, colourway RED, ribbed knit")

	// И ТА ЖЕ ПРОВЕРКА НА ОДНОТКАНЕВОМ РЕЦЕПТЕ — той ветке, где цикла нет вовсе.
	one := runParams{Colour: &colourRecipe{
		Code: "BLK", Hex: "#0a0a0a", Words: "matte heavy jersey",
		Fabrics: []fabricUse{{Name: "body cloth", ColourCode: "BLK", ColourHex: "#0a0a0a", Words: "matte heavy jersey"}},
	}}
	require.Equal(t, 1, strings.Count(surfaceSteer(ctx, one), "colourway BLK"),
		"одна ткань уже описана скалярами: повтор — это то же самое, сказанное дважды")
}

// СТИР НЕ ПЕРЕРАСТАЕТ ПОТОЛОК ПОСТАВЩИКА — НИ НА КАКОМ ВХОДЕ.
//
// ⚠ ДОВОД «СТИР КОРОТОК ПО ПРИРОДЕ» БЫЛ ЗАМЕРОМ ОПРОВЕРГНУТ. Ни `colour.words`, ни `fabrics[].words`,
// ни `fabrics[].parts`, ни ЧИСЛО тканей не ограничены нигде в полосе: 660 символов слов о цвете
// давали стир в 703 руны, восемь тканей — 1262, при потолке 600. Оба исхода ТЕРМИНАЛЬНЫ (прямой
// meshy отказывает локально, meshy через fal отвечает 422), то есть 3D умирало для такого колорвея
// навсегда — с ошибкой, называющей поле `texture_prompt`, о котором на верстаке никто не слышал.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `return strings.Join(parts, "; ")` вместо joinSteer — то есть
// отсутствие границы. ИГЛА УНИКАЛЬНА: проба не сверяет длину с самой собой, а гонит тот же стир
// через настоящий meshy.Submit, у которого потолок и стоит.
func TestSurfaceSteerStaysUnderTheProvidersCeiling(t *testing.T) {
	ctx := context.Background()

	// (а) ОДНО ДЛИННОЕ ПОЛЕ. Описание цвета, набранное человеком, а не заполнитель.
	long := strings.Repeat("matte heavy jersey with a slight sheen and a dry hand, ", 12)[:660]
	wordy := runParams{Colour: &colourRecipe{Code: "BLK", Hex: "#0a0a0a", Words: long}}
	a := surfaceSteer(ctx, wordy)
	require.LessOrEqual(t, len([]rune(a)), meshy.MaxTexturePrompt, "стир длиннее потолка = терминальный отказ")
	require.Contains(t, a, "colourway BLK — the exact value is #0a0a0a",
		"утверждение цвета обязано пережить границу: оно первое по важности")
	require.NotEmpty(t, strings.TrimSpace(a), "положительный контроль: граница — не «выбросить всё»")

	// (б) МНОГО ТКАНЕЙ. Число тканей не ограничено ни дверью, ни схемой.
	var many []fabricUse
	for i := 0; i < 8; i++ {
		many = append(many, fabricUse{
			Name: "contrast rib panel", ColourCode: "RED", ColourHex: "#b1121a",
			Words: "ribbed knit with a soft hand and a slight sheen", Parts: "cuffs, collar and the hem band",
		})
	}
	crowded := runParams{
		Colour: &colourRecipe{Code: "BLK", Hex: "#0a0a0a", Words: "matte heavy jersey", Fabrics: many},
		Threed: &threedParams{Presentation: "model"},
	}
	b := surfaceSteer(ctx, crowded)
	require.LessOrEqual(t, len([]rune(b)), meshy.MaxTexturePrompt)

	// (в) И ЭТО ПРОВЕРЯЕТСЯ ТЕМ САМЫМ ПОТОЛКОМ, А НЕ НАШЕЙ КОПИЕЙ ЧИСЛА: оба стира идут в
	// настоящий meshy.Submit, который выше потолка отказывает ЛОКАЛЬНО и терминально.
	stand := newThreedSteerStand(t)
	c := meshy.New(meshy.Config{APIKey: "k", BaseURL: stand.srv.URL})
	for _, steer := range []string{a, b} {
		_, err := c.Submit(ctx, meshy.Request{
			ImageURLs: []string{"https://example.com/front.png"}, TexturePrompt: steer,
		})
		require.NoError(t, err, "поставщик отказал в стире локально: маршрут 3D мёртв для этого колорвея")
	}

	// (г) ОБЫЧНЫЙ ПРОГОН ГРАНИЦЫ НЕ КАСАЕТСЯ ВОВСЕ — иначе «граница» была бы обрезкой всех.
	ordinary := surfaceSteer(ctx, runParams{
		Colour: &colourRecipe{Code: "BLK", Hex: "#0a0a0a", Words: "matte heavy jersey with a slight sheen"},
		Threed: &threedParams{Presentation: "model"},
	})
	require.Equal(t,
		"colourway BLK — the exact value is #0a0a0a; matte heavy jersey with a slight sheen; presentation model",
		ordinary, "на живых данных граница не режет ничего")
}

// ВОССТАНОВЛЕННАЯ СБОРКА СТОИТ ТО, ЗА ЧТО ЕЁ КУПИЛИ — ПРОВЕРЕНО НА МЕСТЕ ВЫЗОВА.
//
// ⚠ ПОЧЕМУ ЭТА ПРОБА ЕСТЬ, ХОТЯ В ПАКЕТЕ fal УЖЕ ЕСТЬ СВОЯ. Та доказывает, что ВЫРАЖЕНИЕ
// `CostUSDFor(res.Model, …)` считает правильно. Она зеленела бы и в мире, где `threedfal.Collect`
// его не зовёт — а именно так дефект и выглядит: маршрут спрашивает цену у КЛИЕНТА
// (`p.c.CostUSD`), то есть по сегодняшней настроенной модели. Замерено: подмена места вызова на
// `p.c.CostUSD(res.BillableUnits)` не красит в этом пакете НИ ОДНОЙ пробы.
//
// СЦЕНАРИЙ. Прогон куплен под hitem3d и оценён в $0.60. Развёртывание переехало на meshy; отката
// поиска хватило, чтобы найти результат под старым слагом. Если цену взять по настроенной модели,
// в `price_actual` уедет $1.20 — вдвое против собственной оценки прогона, и по строке этого не
// объяснить ничем.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `p.c.CostUSD(res.BillableUnits)` и `Model: p.c.Model()` в
// threedfal.Collect. ИГЛА УНИКАЛЬНА: цены семейств различаются ровно вдвое, и проба сверяет и
// цену, и записанный слаг.
func TestARecoveredBuildIsPricedAsTheModelItWasBoughtAt(t *testing.T) {
	live := "hitem3d/hi3d" // пространство имён, под которым сборка реально лежит
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/model.glb"):
			_, _ = w.Write([]byte("glTF-bytes"))
			return
		case strings.HasSuffix(r.URL.Path, "/thumb.png"):
			_, _ = w.Write([]byte("png-bytes"))
			return
		}
		if !strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/"), live+"/requests/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Request is not found"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/status") {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
			return
		}
		w.Header().Set("x-fal-billable-units", "1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_mesh": map[string]any{"url": "http://" + r.Host + "/model.glb"},
			"thumbnail":  map[string]any{"url": "http://" + r.Host + "/thumb.png"},
		})
	}))
	t.Cleanup(srv.Close)

	// Маршрут настроен на СЕГОДНЯШНЮЮ модель; тарифа нет — значит цену называет сам пакет, и выбор
	// модели на неё влияет.
	prov := falRoute(t, srv.URL, "meshy/v7/multi-image-to-3d")
	coll, ok := prov.(Collector)
	require.True(t, ok, "положительный контроль: маршрут 3D обязан уметь собирать результат")

	out, err := coll.Collect(context.Background(), Job{RunID: 41}, "req-moved")
	require.NoError(t, err, "оплаченная сборка обязана найтись под слагом, под которым её купили")
	require.NotEmpty(t, out.Artifacts, "положительный контроль: байты действительно приехали")

	require.True(t, out.Price.Valid, "цена обязана быть записана, иначе платная сборка ляжет в учёт бесплатной")
	require.Equal(t, "0.6", out.Price.Decimal.String(),
		"сборка hitem3d обязана стоить hitem3d: по настроенной модели это было бы $1.2 — вдвое против её же оценки")
	require.Equal(t, "hitem3d/hi3d/v3.0/multi-view-to-3d", out.Model,
		"провенанс обязан называть слаг, ПОД КОТОРЫМ сборку нашли, а не сегодняшнюю настройку")
}

// ГРАНИЦА НЕ ВЫДУМЫВАЕТ СЛОВ: НЕПРЕРЫВНЫЙ ТЕКСТ БЕЗ ПРОБЕЛОВ ВЫБРАСЫВАЕТСЯ ЦЕЛИКОМ.
//
// ⚠ P3 ИЗ РЕВЬЮ. `cutAtWord` обещает «последнее ЦЕЛОЕ слово», но когда в доступном префиксе пробела
// нет вовсе, прежняя версия молча резала по границе бюджета — и в текстурную стадию уезжал ТОКЕН,
// КОТОРОГО НИКТО НЕ ПИСАЛ, неотличимый для поставщика от настоящего слова. `colour.words` — вольный
// текст: ссылка, склеенный список хексов, язык без пробелов — все достижимы.
//
// ⚠ ПОЧЕМУ ПРЕДЫДУЩАЯ ПРОБА ГРАНИЦЫ ЭТОГО НЕ ЛОВИЛА: она набрана естественной прозой, в которой
// пробелы есть на каждом десятке рун, поэтому ветка «пробела нет» в ней не исполняется ВОВСЕ.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: вернуть `if at > 0 { cut = cut[:at] }` — то есть оставить срез по
// бюджету, когда пробела не нашлось. ИГЛА УНИКАЛЬНА: проба требует, чтобы префикса выдуманного
// токена в стире не было ни одного.
func TestSurfaceSteerNeverInventsAPartialWord(t *testing.T) {
	ctx := context.Background()

	// 660 рун без единого пробела — ровно тот вход, который ревью назвало достижимым.
	blob := strings.Repeat("a", 660)
	steer := surfaceSteer(ctx, runParams{
		Colour: &colourRecipe{Code: "BLK", Hex: "#0a0a0a", Words: blob},
		Threed: &threedParams{Presentation: "model"},
	})

	require.LessOrEqual(t, len([]rune(steer)), meshy.MaxTexturePrompt, "граница обязана держаться и здесь")
	require.NotContains(t, steer, "aa",
		"кусок непроизносимого куска — выдуманное слово: подсказка обязана его выбросить, а не отрезать")
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: выбрасывается ИМЕННО непроизносимая часть, а не стир целиком.
	require.Contains(t, steer, "colourway BLK — the exact value is #0a0a0a",
		"утверждение цвета обязано пережить выброшенный кусок")

	// И ЦЕЛОЕ СЛОВО ВСЁ-ТАКИ РЕЖЕТСЯ ПО СЛОВУ — иначе «не выдумывать» стало бы «не резать никогда».
	require.Equal(t, "one two three", cutAtWord([]rune("one two three four"), 15))
	require.Equal(t, "", cutAtWord([]rune(strings.Repeat("x", 40)), 20),
		"нет пробела — нет и ответа: половина токена хуже его отсутствия")
}

// ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ САМОГО ПОТОЛКА: полный промпт прогона поставщик отказывает ЛОКАЛЬНО, и
// отказ терминальный. Проба держит сам довод починки — то, что дефект был не косметическим, и что
// потолок 600 рун по-прежнему сторожит поле, в которое больше не льют весь промпт.
func TestMeshyRefusesAnUncutRunPromptLocally(t *testing.T) {
	st := newThreedSteerStand(t)
	c := meshy.New(meshy.Config{APIKey: "k", BaseURL: st.srv.URL})

	_, err := c.Submit(context.Background(), meshy.Request{
		ImageURLs:     []string{"https://example.com/front.png"},
		TexturePrompt: longRunPrompt(),
	})
	if !errors.Is(err, meshy.ErrPromptTooLong) {
		t.Fatalf("err = %v, ждали meshy.ErrPromptTooLong", err)
	}
	if v := classify(err); v.Retryable {
		t.Errorf("отказ классифицирован как погода (%s): пять оплаченных попыток на неисправимую длину", v.Code)
	}
	select {
	case raw := <-st.body:
		t.Errorf("запрос всё-таки ушёл в сеть: %s", raw)
	default:
	}
}

// longRunPrompt — промпт ПРОГОНА, а не строка-заполнитель: те же разделы и тот же разделитель
// («\n\n»), которыми их склеивает composePrompt. Длина заведомо за потолком поставщика.
func longRunPrompt() string {
	var b strings.Builder
	b.WriteString("make the shoulder softer and the hem straight")
	for i := 0; i < 40; i++ {
		b.WriteString("\n\nreferences:\n- front — the collar stands, the placket is hidden, the cuff is doubled")
	}
	return b.String()
}
