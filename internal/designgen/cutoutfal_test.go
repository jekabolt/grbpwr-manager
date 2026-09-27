package designgen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hash/crc32"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────── СТЕНД ───────────────────────────

// cutoutStand is a fake fal queue that answers one background removal: submit → status → result →
// the picture. It serves whatever bytes it is given, which is the whole point — the route's job is
// to notice what came back, not to trust what it asked for.
type cutoutStand struct {
	payload []byte
	noImage bool // a COMPLETED request whose answer carries no picture url
	// failStatus makes the status lookup die, which is the closest a stand can come to «the pass
	// never got its answer»: the submit was accepted and paid, and this process is not going to
	// find out what came of it.
	failStatus bool
	units      string // x-fal-billable-units on the result fetch; "" omits the header
	srv        *httptest.Server
	submits    int
	lastBody   map[string]any
	imageURLs  []string
}

func newCutoutStand(t *testing.T, payload []byte) *cutoutStand {
	t.Helper()
	st := &cutoutStand{payload: payload, units: "1"}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/picture":
			_, _ = w.Write(st.payload)
		case strings.HasSuffix(r.URL.Path, "/status"):
			if st.failStatus {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
		case strings.Contains(r.URL.Path, "/requests/"):
			if st.units != "" {
				w.Header().Set("x-fal-billable-units", st.units)
			}
			img := map[string]any{"content_type": "image/png"}
			if !st.noImage {
				img["url"] = "http://" + r.Host + "/picture"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"image": img})
		case r.Method == http.MethodPost:
			st.submits++
			require.NoError(t, json.NewDecoder(r.Body).Decode(&st.lastBody))
			if u, ok := st.lastBody["image_url"].(string); ok {
				st.imageURLs = append(st.imageURLs, u)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "cut-77"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(st.srv.Close)
	return st
}

// provider wires the route at this stand, with a cut-out tariff that is NOT the 3D one, so a price
// computed off the wrong rate is visible in the number rather than hidden by a coincidence.
func (st *cutoutStand) provider() Provider {
	return NewFalCutoutProvider(fal.New(fal.Config{
		APIKey:        "test-key",
		BaseURL:       st.srv.URL,
		HTTPTimeout:   2 * time.Second,
		PollInterval:  5 * time.Millisecond,
		PollTimeout:   time.Second,
		UnitUSD:       0.5,  // the 3D rate — must never price a cut-out
		UnitUSDCutout: 0.03, // the cut-out rate
	}))
}

// cutoutPass — ОДИН УДАЧНЫЙ ПРОХОД ВОРКЕРА: сабмит, потом сбор по возвращённому id.
//
// Ровно то, что делает dispatch.execute на маршруте с Collector, и написано здесь отдельно затем,
// чтобы проверки доставки говорили про доставку, а не про склейку двух глаголов. Сам сабмит — это
// ПЛАТЁЖ, и то, что он отделён, проверяется своими пробами ниже.
func cutoutPass(t *testing.T, p Provider, job Job) (*Outcome, error) {
	t.Helper()
	sub, err := p.Execute(context.Background(), job)
	if err != nil {
		return sub, err
	}
	require.True(t, sub.Pending, "сабмит не доставляет картинку; Pending — это то слово, по которому "+
		"воркер закрывает попытку `accepted` и идёт собирать")
	require.NotEmpty(t, sub.RequestID, "без id возобновление невозможно, а значит невозможно и не платить дважды")
	require.False(t, sub.Price.Valid, "цену называет забор результата; ноль здесь сказал бы, что вырез бесплатен")
	col, ok := p.(Collector)
	require.True(t, ok, "маршрут, отдающий Pending без Collector, оставляет оплаченное задание висеть")
	return col.Collect(context.Background(), job, sub.RequestID)
}

// pngWithAlpha is a picture that HAS transparency: an opaque disc on a transparent ground, which is
// what a cut-out actually looks like.
func pngWithAlpha(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(0)
			if (x-w/2)*(x-w/2)+(y-h/2)*(y-h/2) < (w/4)*(w/4) {
				a = 255
			}
			m.SetNRGBA(x, y, color.NRGBA{R: 180, G: 40, B: 20, A: a})
		}
	}
	return encodePNG(t, m)
}

// pngFullyOpaque is the failure this route exists to notice: a perfectly valid PNG in which nothing
// was cut out. It is what a model that «removed the background» BY PAINTING returns.
func pngFullyOpaque(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	return encodePNG(t, m)
}

func encodePNG(t *testing.T, m image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, m))
	return buf.Bytes()
}

// pngHeaderSays — НАСТОЯЩИЙ PNG, ЧЕЙ ЗАГОЛОВОК ОБЪЯВЛЯЕТ ЧУЖОЙ РАЗМЕР.
//
// ⚠ ЭТО И ЕСТЬ ФОРМА БОМБЫ, И ОНА ЖЕ — РАЗЛИЧАЮЩИЙ ПРИЗНАК. Сжатых 30000×30000 в пробе не собрать:
// честная такая картинка это гигабайты памяти ровно там, где мы доказываем, что их не тратим.
// Поэтому берётся крошечный PNG, и в его IHDR переписываются ширина с высотой (CRC пересчитывается,
// иначе заголовок не прочитает и DecodeConfig). Получается файл в полторы сотни байт, ОБЪЯВЛЯЮЩИЙ
// гигапиксель, — ровно то, что присылает злонамеренный или сломанный поставщик.
//
// И у него есть свойство, ради которого он здесь: `png.Decode` на нём ПАДАЕТ (данных на объявленную
// канву нет). Значит вердикт `tooLarge` физически невозможно получить, если растр всё-таки трогали:
// декодировавшая редакция вернула бы «это не читаемый PNG», то есть жалобу про альфу. Убери потолок
// — и проба покраснеет по СМЫСЛУ, а не по счётчику.
func pngHeaderSays(t *testing.T, w, h int) []byte {
	t.Helper()
	raw := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	// PNG: 8 байт сигнатуры, затем чанк IHDR — 4 байта длины, 4 байта типа, 13 байт данных, 4 CRC.
	// Ширина и высота — первые восемь байт данных.
	const ihdrData = 8 + 4 + 4
	require.Greater(t, len(raw), ihdrData+13+4)
	require.Equal(t, "IHDR", string(raw[12:16]), "первый чанк PNG обязан быть IHDR")
	out := append([]byte(nil), raw...)
	binary.BigEndian.PutUint32(out[ihdrData:], uint32(w))
	binary.BigEndian.PutUint32(out[ihdrData+4:], uint32(h))
	// CRC считается по ТИПУ ЧАНКА И ЕГО ДАННЫМ — без него декодер отвергнет заголовок как битый, и
	// проба доказывала бы совсем другое.
	crc := crc32.ChecksumIEEE(out[12 : ihdrData+13])
	binary.BigEndian.PutUint32(out[ihdrData+13:], crc)

	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err, "заголовок обязан читаться, иначе проба меряет не то")
	require.Equal(t, w, cfg.Width)
	require.Equal(t, h, cfg.Height)
	_, err = png.Decode(bytes.NewReader(out))
	require.Error(t, err, "растра под объявленную канву здесь нет — и это то, что делает пробу различающей")
	return out
}

func cutoutJob(refs ...string) Job {
	return Job{
		RunID: 7,
		// A fat prompt that must NOT travel: the matting model has no text field at all.
		Prompt:     "cut the background out of this photograph and keep the garment crisp",
		References: refs,
		Outputs:    1,
	}
}

// ─────────────────────────── ДВЕРЬ ───────────────────────────

// TestTheCutoutRouteWithNoKeyNAMES_THE_VARIABLE.
//
// ⚠ ЭТУ ФРАЗУ ЧЕЛОВЕК ЧИТАЕТ НА ЭКРАНЕ. «the provider for this run kind is not configured» — факт о
// процессе; «no key for fal — set it in admin → AI providers (or FAL_KEY)» — факт, с которым можно
// что-то сделать: где ключ хранится теперь (панель) и какая переменная всё ещё работает. Владелец,
// только что вбивший ключ, обязан понять по кнопке, тот ли ключ был нужен.
func TestTheCutoutRouteWithNoKeyNAMES_THE_VARIABLE(t *testing.T) {
	p := NewFalCutoutProvider(nil) // no credentials — a fresh deployment
	require.False(t, p.Enabled())
	require.Equal(t, "no key for fal — set it in admin → AI providers (or FAL_KEY)", missingCredential(p),
		"the door asks the route which setting it lacks; a generic sentence sends the owner looking")

	_, err := p.Execute(context.Background(), cutoutJob("https://cdn.example/a.png"))
	require.ErrorIs(t, err, errProviderDisabled)
	require.Contains(t, err.Error(), "no key for fal — set it in admin → AI providers (or FAL_KEY)")

	// The pre-flight verdict of a route with no key is «this kind is not available», settled before
	// any money can move.
	require.Equal(t, CodeKindNotAvailable, classify(err).Code)
	require.False(t, classify(err).Retryable)
}

// TestTheCutoutKindREFUSES_AT_THE_DOOR_NAMING_FAL_KEY.
//
// ⚠ ЭТО ТА ЖЕ ФРАЗА, НО ИЗ ДРУГОГО МЕСТА, И ИМЕННО ЭТО МЕСТО ВИДИТ ЧЕЛОВЕК. Ни один Execute в
// пакете не достижим при отсутствующем ключе — предполёт отказывает раньше, у двери, ДО того как
// заведена попытка и зарезервированы деньги. Проверка выше меряет маршрут; эта — дверь, вместе с
// новым родом прогона, который до шва агента A вообще не имел маршрута.
func TestTheCutoutKindREFUSES_AT_THE_DOOR_NAMING_FAL_KEY(t *testing.T) {
	// No credentials — the state a fresh deployment is in before the owner opens the dashboard.
	w := newWorker(&Config{}, nil, nil, allSink{}, Providers{Cutout: NewFalCutoutProvider(nil)})
	err := w.PreflightKind(entity.DesignRunKindCutout)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no key for fal — set it in admin → AI providers (or FAL_KEY)",
		"«the provider for this run kind is not configured: fal_cutout» does not tell the owner "+
			"who has just typed a key whether that was the missing piece")
	var refusal *KindRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, CodeKindNotAvailable, refusal.RefusalReason())

	// With a key and a sink that can keep a PNG, the door lets the run as far as paying.
	withKey := Providers{Cutout: NewFalCutoutProvider(fal.New(fal.Config{APIKey: "k"}))}
	require.NoError(t, newWorker(&Config{}, nil, nil, newFakeSink(ContentTypePNG), withKey).
		PreflightKind(entity.DesignRunKindCutout))

	// ⚠ И ЭТО ОХРАНА, КОТОРАЯ БЕРЕЖЁТ НАСТОЯЩИЕ ДЕНЬГИ: хранилище, не умеющее PNG, отказывает
	// маршруту ДО платного вызова. Иначе за вырез платили бы на каждом проходе и отвергали бы его
	// при загрузке — пять раз за прогон, сколько живёт расхождение.
	err = newWorker(&Config{}, nil, nil, newFakeSink(ContentTypeJPEG), withKey).
		PreflightKind(entity.DesignRunKindCutout)
	require.ErrorIs(t, err, errSinkUnsupported)
	require.Equal(t, CodeOutputNotStorable, classify(err).Code)

	// An unwired slot refuses by name rather than dereferencing nil on a run that would already
	// have been admitted.
	err = newWorker(&Config{}, nil, nil, allSink{}, Providers{}).PreflightKind(entity.DesignRunKindCutout)
	require.ErrorIs(t, err, errRouteMissing)
	require.Contains(t, err.Error(), "cutout")
}

// TestTheCutoutRouteProducesTHE_ONE_FORMAT_THAT_CARRIES_ALPHA. Produces is what the pre-flight must
// be able to store, and for this route it is PNG for one reason: it is the only format in this band
// that has an alpha channel, and the alpha is the entire product.
func TestTheCutoutRouteProducesTHE_ONE_FORMAT_THAT_CARRIES_ALPHA(t *testing.T) {
	require.Equal(t, []string{ContentTypePNG}, NewFalCutoutProvider(nil).Produces())
	require.Equal(t, "fal_cutout", NewFalCutoutProvider(nil).Name(),
		"the attempt row names the ROUTE: two routes of this band now spend money at the same "+
			"vendor under two slugs and two tariffs, and one name for both makes a bill unreconcilable")
}

// TestTheCutoutRouteSendsNO_WORDS — see PromptCarrier.
//
// ⚠ БЕЗ ЭТОГО design_run.prompt ПОКАЗАЛ БЫ АБЗАЦ, КОТОРОГО ПРОВАЙДЕР НЕ ВИДЕЛ. Матирующая модель
// принимает одну ссылку и не имеет текстового поля вовсе; строка истории, приписывающая слова уже
// потраченным деньгам, — не пустая колонка, а ложное свидетельство. Ровно этот дефект был измерен
// на 3D-маршруте.
func TestTheCutoutRouteSendsNO_WORDS(t *testing.T) {
	job := cutoutJob("https://cdn.example/a.png")
	require.NotEmpty(t, job.Prompt, "the job carries words; the point is that they do not travel")
	require.Equal(t, "", recordedPrompt(NewFalCutoutProvider(nil), job),
		"the column must say what was sent, and nothing was")
}

// ─────────────────────────── ВХОД ───────────────────────────

// TestACutOutIsEXACTLY_ONE_PICTURE, and the refusal is free.
//
// Матирование — операция над КОНКРЕТНЫМ кадром. Получив список, маршрут мог бы либо взять первый —
// решить за человека, какую из его картинок он оплатил, — либо порезать все, купив N вырезов там,
// где прогон оценён в один. Обе развилки тихие, обе про деньги.
func TestACutOutIsEXACTLY_ONE_PICTURE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a request left the process for a job that could be refused locally, for free")
	}))
	defer srv.Close()

	p := NewFalCutoutProvider(fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second}))

	for _, refs := range [][]string{
		{},
		{"https://cdn.example/a.png", "https://cdn.example/b.png"},
		{"https://cdn.example/a.png", "https://cdn.example/b.png", "https://cdn.example/c.png"},
	} {
		out, err := p.Execute(context.Background(), cutoutJob(refs...))
		require.Errorf(t, err, "%d references", len(refs))
		require.Nil(t, out, "nothing was bought, so there is no outcome to record")
		require.ErrorIs(t, err, orimages.ErrBadRequest)
		// A run that cannot become sendable must not spend the whole attempt cap discovering it.
		require.False(t, classify(err).Retryable)
		require.Equal(t, CodeBadRequest, classify(err).Code)
	}
}

// ─────────────────────────── ДОСТАВКА ───────────────────────────

// TestACutOutWithAlphaIsDELIVERED_AND_PRICED_AT_ITS_OWN_RATE.
func TestACutOutWithAlphaIsDELIVERED_AND_PRICED_AT_ITS_OWN_RATE(t *testing.T) {
	st := newCutoutStand(t, pngWithAlpha(t, 64, 64))
	st.units = "2"

	out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/shirt.jpg"))
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Len(t, out.Artifacts, 1)
	require.Equal(t, ContentTypePNG, out.Artifacts[0].ContentType)
	require.Equal(t, st.payload, out.Artifacts[0].Bytes, "the delivered bytes are the ones filed")
	require.Empty(t, out.Artifacts[0].Kind,
		"an empty kind means «the one derived from the run kind», which is right for a route whose "+
			"single output is the run itself")
	require.False(t, out.Pending, "the COLLECT delivers; a pending outcome here would send the "+
		"dispatcher back for a second lookup of a request it already has in its hands")

	require.Equal(t, fal.DefaultModelCutout+"#cut-77", out.RequestID, "the locator: the slug it was queued under, and the id (G-03)")
	require.Equal(t, fal.DefaultModelCutout, out.Model)

	// ⚠ ЦЕНА ПО СВОЕМУ ТАРИФУ. Стенд настроен с двумя разными ставками именно для того, чтобы
	// вычисление по чужой было видно числом: 2 единицы × 0.03 = 0.06, а не 2 × 0.5 = 1.
	require.True(t, out.Price.Valid, "a delivered cut-out has a price, and NULL would say nobody knew")
	require.Equal(t, "0.06", out.Price.Decimal.String(), "2 units at FAL_UNIT_USD_CUTOUT=0.03")

	// The picture the run named is the picture that was sent.
	require.Equal(t, []string{"https://cdn.example/shirt.jpg"}, st.imageURLs)
	require.Equal(t, 1, st.submits, "a submit is a payment and is never repeated inside one pass")
}

// ─────────────────────── ДОСТАВЛЕНО, СОХРАНЕНО, ОБЖАЛОВАНО ───────────────────────

// TestACutOutWithNoAlphaIsKEPT_AND_COMPLAINED_ABOUT.
//
// ⚠ ЭТО ПРОВЕРКА ПИКСЕЛЕЙ, А НЕ ЯРЛЫКА, И РАЗНИЦА ЗДЕСЬ ВСЯ. Стенд отдаёт совершенно законный PNG с
// заголовком `image/png` — и с белым фоном. Именно такой ответ даёт модель, которая «убрала фон»
// РИСОВАНИЕМ, а не матированием; проверка типа пропустила бы его молча, человек увидел бы белый
// квадрат вместо выреза и решил бы, что сломан показ.
//
// Форма отказа — «доставлено, сохранено, обжаловано»: картинка оплачена и может быть полезна, её
// кладут в карточку, а строка попытки несёт жалобу. Тот же шов, что у errPatternNotSeamless.
func TestACutOutWithNoAlphaIsKEPT_AND_COMPLAINED_ABOUT(t *testing.T) {
	st := newCutoutStand(t, pngFullyOpaque(t, 32, 20))

	out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/a.png"))
	require.Error(t, err)
	require.ErrorIs(t, err, errCutoutNoAlpha)

	// THE PICTURE IS KEPT. Returning the error INSTEAD of the artifact would throw away something
	// already paid for.
	require.NotNil(t, out, "the artifact travels beside the complaint, never instead of it")
	require.Len(t, out.Artifacts, 1)
	require.Equal(t, st.payload, out.Artifacts[0].Bytes)
	require.True(t, out.Price.Valid, "the money is real whether or not the alpha is")

	// THE FIGURES TRAVEL WITH THE COMPLAINT: «there is no transparency» with nothing beside it is an
	// accusation nobody can check against the picture they are looking at.
	require.Contains(t, err.Error(), "640", "32×20 = 640 pixels, and the count is the evidence")
	require.Contains(t, err.Error(), "32×20")
	require.Contains(t, err.Error(), "the picture was kept")
}

// TestACutOutTooBigToReadIsNOT_DECODED_AND_SAYS_SO.
//
// ⚠ ЭТО БЫЛА БОМБА ПОСЛЕ ОПЛАТЫ. Транспорт пускает файл до 25 MiB, а сжатый PNG объявляет канву
// какого угодно размера: прежняя редакция звала `png.Decode` на байты ПОСТАВЩИКА без единого
// потолка, то есть просила у процесса с пол-гигабайтом памяти десятки гигабайт — и уносила с собой
// чужие оплаченные прогоны из соседних горутин.
//
// ЧТО ДОКАЗЫВАЕТСЯ: (1) вердикт — `cutout_too_large`, отдельным словом от «нет альфы», потому что
// это ДРУГОЕ утверждение — мы не смотрели; (2) картинка при этом СОХРАНЕНА и оплачена, как у
// соседа; (3) растр не трогали — см. довод у pngHeaderSays: декодировавшая редакция вернула бы
// жалобу про нечитаемый PNG, а не про размер.
func TestACutOutTooBigToReadIsNOT_DECODED_AND_SAYS_SO(t *testing.T) {
	payload := pngHeaderSays(t, 30000, 30000)
	require.Less(t, len(payload), 4096, "бомба обязана быть маленькой — в этом её вся суть")

	st := newCutoutStand(t, payload)
	out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/a.png"))
	require.Error(t, err)
	require.ErrorIs(t, err, errCutoutTooLarge)
	require.NotErrorIs(t, err, errCutoutNoAlpha,
		"«не смотрели» и «прозрачности нет» — разные утверждения об одном оплаченном кадре")

	// КАРТИНКА СОХРАНЕНА И ОПЛАЧЕНА — тот же шов, что у cutout_no_alpha: решение не смотреть приняли
	// мы, а деньги ушли до того.
	require.NotNil(t, out)
	require.Len(t, out.Artifacts, 1)
	require.Equal(t, payload, out.Artifacts[0].Bytes)
	require.True(t, out.Price.Valid)

	// ЦИФРЫ ЕДУТ С ЖАЛОБОЙ: без них «слишком велик» — обвинение, которое нечем проверить.
	require.Contains(t, err.Error(), "30000×30000")
	require.Contains(t, err.Error(), "the picture was kept")

	// И КОД У НЕГО СВОЙ, ТЕРМИНАЛЬНЫЙ, ДОСТАВЛЕННЫЙ.
	v := classify(err)
	require.False(t, v.Retryable, "повтор купил бы ту же картинку и отказался бы читать её снова")
	require.Equal(t, CodeCutoutTooLarge, v.Code)
	require.Equal(t, "cutout_too_large", CodeCutoutTooLarge)
	require.Equal(t, entity.DesignAttemptDelivered, v.State)
}

// TestTheAlphaCeilingIsTHE_BUCKETS_OWN — потолок здесь НЕ СВОЙ, и это несущее свойство.
//
// Картинка, которую бакет отказался бы хранить, не должна быть развёрнута в память ради вопроса про
// альфу; две копии числа разошлись бы молча и в ту сторону, в которую дороже. Проба держит границу
// с ОБЕИХ сторон: то, что бакет берёт, — измеряется; то, что он не берёт, — нет.
func TestTheAlphaCeilingIsTHE_BUCKETS_OWN(t *testing.T) {
	side, pixels := bucket.ImageBudgetCeilings()
	require.True(t, bucket.ImageWithinBudget(side, 1), "сторона ровно в потолок ещё в бюджете")
	require.False(t, bucket.ImageWithinBudget(side+1, 1), "на пиксель шире стороны — уже нет")
	require.False(t, bucket.ImageWithinBudget(int(pixels/1000)+1, 1001), "площадь считается отдельно от стороны")

	// ЧЕСТНАЯ КАРТИНКА В БЮДЖЕТЕ ИЗМЕРЯЕТСЯ КАК ПРЕЖДЕ — иначе потолок был бы не сторожем, а
	// заглушкой на весь маршрут.
	require.True(t, cutoutAlphaVerdict(pngWithAlpha(t, 32, 32)).hasAlpha)
	require.False(t, cutoutAlphaVerdict(pngHeaderSays(t, side+1, 4)).hasAlpha)
	require.True(t, cutoutAlphaVerdict(pngHeaderSays(t, side+1, 4)).tooLarge,
		"сторона за потолком — тот же отказ мерить, что и площадь")
}

// TestAJPEGAnswerIsTHE_SAME_COMPLAINT. A JPEG cannot carry an alpha channel at all, so an answer in
// that format is a background that was painted rather than removed — the same failure, and it must
// read as the same one rather than as a decode error nobody can act on.
func TestAJPEGAnswerIsTHE_SAME_COMPLAINT(t *testing.T) {
	var jpg bytes.Buffer
	m := image.NewRGBA(image.Rect(0, 0, 24, 16))
	require.NoError(t, jpeg.Encode(&jpg, m, nil))

	st := newCutoutStand(t, jpg.Bytes())
	out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/a.png"))
	require.ErrorIs(t, err, errCutoutNoAlpha)
	require.NotNil(t, out)
	require.Len(t, out.Artifacts, 1)
	// ⚠ И ХРАНИТСЯ ОНА ПОД СВОИМ НАСТОЯЩИМ ТИПОМ. Ярлык `image/png` над JPEG-байтами оставил бы в
	// строке выдачи слово, которому противоречит сам файл: бакет всё равно определяет формат по
	// сигнатуре, так что честный ярлык не стоит ничего, а нечестный стоит доверия к строке.
	require.Equal(t, ContentTypeJPEG, out.Artifacts[0].ContentType)
	require.Contains(t, err.Error(), "image/jpeg")
	require.Contains(t, err.Error(), "no alpha channel")
}

// TestAGrayscalePNG_IsTHE_SAME_COMPLAINT covers the OTHER decode path: a grayscale PNG decodes to
// *image.Gray, which the fast NRGBA scan never sees. It has no alpha channel either, and the answer
// must not depend on which concrete type the decoder happened to return.
func TestAGrayscalePNG_IsTHE_SAME_COMPLAINT(t *testing.T) {
	g := image.NewGray(image.Rect(0, 0, 12, 10))
	for i := range g.Pix {
		g.Pix[i] = 200
	}
	st := newCutoutStand(t, encodePNG(t, g))

	out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/a.png"))
	require.ErrorIs(t, err, errCutoutNoAlpha)
	require.NotNil(t, out)
	require.Len(t, out.Artifacts, 1)
	require.Contains(t, err.Error(), "120", "12×10 = 120 pixels")
}

// TestASingleTranslucentPixelIsENOUGH_AND_IS_LOOKED_FOR_EVERYWHERE.
//
// ⚠ ЭТО ПРОВЕРКА ПОКРЫТИЯ ОБХОДА, А НЕ ПРИДИРКА. Обход выходит на первом же полупрозрачном пикселе,
// и «обошли всё» отличается от «обошли левый верхний угол» ровно тем, что второе даёт ЛОЖНУЮ ЖАЛОБУ
// на честный вырез, у которого прозрачна только кромка. Пиксель поставлен в последнюю строку и
// последний столбец — единственное место, куда неполный обход не дойдёт.
func TestASingleTranslucentPixelIsENOUGH_AND_IS_LOOKED_FOR_EVERYWHERE(t *testing.T) {
	const w, h = 40, 30
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	m.SetNRGBA(w-1, h-1, color.NRGBA{R: 10, G: 20, B: 30, A: 254})

	st := newCutoutStand(t, encodePNG(t, m))
	out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/a.png"))
	require.NoError(t, err, "a picture whose only transparency is in its last pixel still has transparency")
	require.Len(t, out.Artifacts, 1)
}

// TestAlphaIsFoundInEVERY_SHAPE_THE_DECODER_RETURNS.
//
// ⚠ ЭТО НЕ ПЕДАНТИЗМ, А ЗАКРЫТИЕ ИЗМЕРЕННОЙ ДЫРЫ. Быстрый обход читает полосу *image.NRGBA; всё
// остальное идёт общим путём через At().RGBA(). А `png.Decode` возвращает НЕ ТОЛЬКО NRGBA: у
// индексированного PNG с прозрачной записью палитры (самый обычный формат маленькой вырезки — он
// же самый лёгкий) это *image.Paletted, у шестнадцатибитного — *image.NRGBA64, и оба несут
// НАСТОЯЩУЮ прозрачность. Измерено: сломанный общий путь («ни один пиксель не прозрачен») не ронял
// ни одной проверки — честная вырезка молча получала бы жалобу `cutout_no_alpha`, а прогон —
// повторную покупку.
func TestAlphaIsFoundInEVERY_SHAPE_THE_DECODER_RETURNS(t *testing.T) {
	// Indexed, with a fully transparent palette entry → *image.Paletted.
	pal := color.Palette{color.NRGBA{A: 0}, color.NRGBA{R: 200, G: 30, B: 10, A: 255}}
	idx := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
	for i := range idx.Pix {
		idx.Pix[i] = 1
	}
	idx.SetColorIndex(7, 7, 0) // the only transparent pixel, in the far corner

	// Sixteen bits per channel, half-transparent → *image.NRGBA64.
	deep := image.NewNRGBA64(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			deep.SetNRGBA64(x, y, color.NRGBA64{R: 0x4000, G: 0x2000, B: 0x1000, A: 0x8000})
		}
	}

	for _, tc := range []struct {
		name string
		src  image.Image
		want string
	}{
		{"indexed png with a transparent palette entry", idx, "*image.Paletted"},
		{"16-bit png with alpha", deep, "*image.NRGBA64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := encodePNG(t, tc.src)
			decoded, err := png.Decode(bytes.NewReader(raw))
			require.NoError(t, err)
			require.Equal(t, tc.want, fmt.Sprintf("%T", decoded),
				"the point of this case is the concrete type the fast path does NOT handle; if the "+
					"decoder stopped returning it, this case is measuring nothing")

			require.True(t, cutoutAlphaVerdict(raw).hasAlpha,
				"a picture with real transparency must not be complained about")

			st := newCutoutStand(t, raw)
			out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/a.png"))
			require.NoError(t, err)
			require.Len(t, out.Artifacts, 1)
		})
	}
}

// ─────────────────────────── ДЕНЬГИ УПАВШЕГО ВЫЗОВА ───────────────────────────

// TestABilledCutoutFailureCARRIES_ITS_MONEY.
//
// Запрос ЗАВЕРШЁН — значит картинка сделана и единицы потрачены — а ссылки, которой её забрать, в
// ответе нет. Без носителя заряда деньги терминального отказа исчезают: попытка закрывается с
// NULL-ценой, дневная книга не видит траты, и никто не может сказать, во что обошлись провалы.
func TestABilledCutoutFailureCARRIES_ITS_MONEY(t *testing.T) {
	st := newCutoutStand(t, nil)
	st.noImage = true
	st.units = "3"

	out, err := cutoutPass(t, st.provider(), cutoutJob("https://cdn.example/a.png"))
	require.Error(t, err)
	require.NotNil(t, out, "a billed failure must reach the ledger; nil here loses the spend")
	require.Empty(t, out.Artifacts, "there is nothing to file — only money to record")
	require.True(t, out.Price.Valid)
	require.Equal(t, "0.09", out.Price.Decimal.String(), "3 units at FAL_UNIT_USD_CUTOUT=0.03")
	// ⚠ И ИДЕНТИФИКАТОР ЗАПРОСА ТОЖЕ: маршрут синхронный, снаружи вызова id не существует, а
	// списание в счёте fal иначе не с чем сопоставить.
	require.Equal(t, fal.DefaultModelCutout+"#cut-77", out.RequestID)

	// The verdict: paid, nothing to show, do not repeat.
	require.False(t, classify(err).Retryable)
	require.Equal(t, CodeEmptyResponse, classify(err).Code)
}

// TestAnUnpricedCutoutFailureRECORDS_NOTHING_RATHER_THAN_ZERO. «Nobody could say what it cost» and
// «it was free» are different claims about one run, and NULL is the schema's word for the first.
func TestAnUnpricedCutoutFailureRECORDS_NOTHING_RATHER_THAN_ZERO(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The submit itself is refused: nothing was billed, so nothing may be recorded.
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"detail":"slow down"}`))
	}))
	defer srv.Close()

	p := NewFalCutoutProvider(fal.New(fal.Config{
		APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second, UnitUSDCutout: 0.03,
	}))
	out, err := p.Execute(context.Background(), cutoutJob("https://cdn.example/a.png"))
	require.ErrorIs(t, err, fal.ErrRateLimited)
	require.Nil(t, out, "a refused request was not billed; an Outcome here would invent a spend")
	require.True(t, classify(err).Retryable, "a refused request costs nothing, so it may be repeated")
}

// ─────────────────────────── ЕДИНИЦА ИЗМЕРЕНИЯ ───────────────────────────

// TestTheAlphaVerdictSpeaksInFIGURES_OR_SAYS_WHY. The verdict is the sentence a person reads on the
// attempt row, so both of its shapes are asserted directly rather than through a paid pass.
func TestTheAlphaVerdictSpeaksInFIGURES_OR_SAYS_WHY(t *testing.T) {
	require.True(t, cutoutAlphaVerdict(pngWithAlpha(t, 16, 16)).hasAlpha)

	v := cutoutAlphaVerdict(pngFullyOpaque(t, 4, 5))
	require.False(t, v.hasAlpha)
	require.Contains(t, v.why, "20 pixels")
	require.Contains(t, v.why, "4×5")

	// Nothing at all is its own sentence: «no bytes» and «no transparency» are different faults, and
	// only one of them is about the picture.
	v = cutoutAlphaVerdict(nil)
	require.False(t, v.hasAlpha)
	require.Contains(t, v.why, "no bytes")

	// Garbage is named by what it is, not decoded into a panic.
	v = cutoutAlphaVerdict([]byte("this is not a picture at all"))
	require.False(t, v.hasAlpha)
	require.Contains(t, v.why, "not a readable PNG")
}

// TestTheContentTypeComesFromTHE_BYTES. The bucket sniffs the payload whatever label it is handed,
// so an honest label costs nothing — and a dishonest one leaves a word in the output row that the
// file itself contradicts.
func TestTheContentTypeComesFromTHE_BYTES(t *testing.T) {
	var jpg bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil))

	require.Equal(t, ContentTypePNG, cutoutContentType(pngWithAlpha(t, 8, 8)))
	require.Equal(t, ContentTypeJPEG, cutoutContentType(jpg.Bytes()))
	require.Equal(t, ContentTypeWEBP, cutoutContentType([]byte("RIFF\x00\x00\x00\x00WEBPVP8 ")))
	// An unrecognised payload is called the type this route ASKED for, so it goes through the
	// picture door and is refused there BY NAME instead of being dropped here as «unknown».
	require.Equal(t, ContentTypePNG, cutoutContentType([]byte("nonsense")))
}

// TestTheCutoutSentinelIsClassifiedWithItsOwnSeam.
//
// ⚠ ЭТА ПРОВЕРКА ЖИЛА ЗДЕСЬ КАК ЗАПИСЬ ДОЛГА, И ЕЁ ПЕРЕПИСАЛИ, КОГДА ДОЛГ ЗАКРЫЛИ. Раньше она
// утверждала СЕГОДНЯШНЕЕ поведение — сентинел без ветки проваливается в retryable-умолчание
// классификатора, — и краснела ровно в тот момент, когда ветку добавляли. Ветку добавили; теперь
// она утверждает обратное, и покраснеет, если ветку когда-нибудь уберут.
//
// ЧТО ИМЕННО СТОИТ ЗА ЭТИМИ ТРЕМЯ ПОЛЯМИ, ПО ОДНОМУ.
// Retryable=false — повтор покупает У ТОЙ ЖЕ МОДЕЛИ ТУ ЖЕ КАРТИНКУ, и так до потолка платных
// попыток; это единственное поле здесь, у которого есть цена в долларах.
// Code=cutout_no_alpha — строка истории называет СВОЙ отказ, а не `provider_unavailable`, за
// которым человек уходит смотреть чужую статус-страницу.
// State=delivered — картинка ОПЛАЧЕНА И СОХРАНЕНА; `failed` рядом с реальным списанием был бы
// ложью о деньгах.
func TestTheCutoutSentinelIsClassifiedWithItsOwnSeam(t *testing.T) {
	v := classify(errCutoutNoAlpha)
	require.False(t, v.Retryable, "вырез без альфы нельзя перепокупать: тот же вход у той же модели "+
		"даёт тот же ответ, а платит за него карточка")
	require.Equal(t, CodeCutoutNoAlpha, v.Code)
	require.Equal(t, "cutout_no_alpha", CodeCutoutNoAlpha)
	require.Equal(t, entity.DesignAttemptDelivered, v.State)

	// СОСЕД С ТЕМ ЖЕ ШВОМ, И ИМЕННО ОН БЫЛ ОБРАЗЦОМ: куплено, сохранено, пожаловались строкой.
	seam := classify(errPatternNotSeamless)
	require.False(t, seam.Retryable)
	require.Equal(t, CodePatternNotSeamless, seam.Code)
	require.Equal(t, entity.DesignAttemptDelivered, seam.State)

	// ⚠ И ОБЁРНУТЫЙ СЕНТИНЕЛ ТОЖЕ: Execute возвращает его С ЧИСЛАМИ (fmt.Errorf %w), и классификатор
	// обязан узнавать его сквозь обёртку — иначе ветка зелена на голом сентинеле и мертва на том
	// единственном значении, которое действительно приезжает с маршрута.
	wrapped := fmt.Errorf("%w: every one of the 64 pixels is fully opaque; the picture was kept",
		errCutoutNoAlpha)
	require.Equal(t, CodeCutoutNoAlpha, classify(wrapped).Code)
}

// ═══════════ ОПЛАЧЕННЫЙ САБМИТ ПЕРЕЖИВАЕТ ПОТЕРЮ ПРОХОДА ═══════════

// TestACutoutIsNOT_BOUGHT_TWICE_AFTER_A_LOST_PASS.
//
// ⚠ ЭТО ВТОРОЕ СПИСАНИЕ, А НЕ ЛИШНЯЯ СТРОКА В ИСТОРИИ. Между сабмитом и результатом стоит сеть, и
// проход может не дойти: редеплой посреди прохода, убитый под, оборванный ответ. Синхронный глагол
// не оставлял после себя НИЧЕГО — следующий проход начинал с нового POST, то есть покупал ту же
// картинку второй раз, и сам провайдер прямо предупреждает, что второй submit это второй charge.
//
// Проба ставит именно этот разрыв: сабмит проходит, статус отвечает 502 — оплачено, а чем
// кончилось, этот проход не узнает. Дальше воркер запускается заново на той же строке, и
// единственное, что стоит между ним и вторым платежом, — записанный `accepted` с request_id.
func TestACutoutIsNOT_BOUGHT_TWICE_AFTER_A_LOST_PASS(t *testing.T) {
	stand := newCutoutStand(t, pngWithAlpha(t, 48, 48))
	stand.failStatus = true

	store := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	w := testWorker(store, media(11), sink, Providers{Cutout: stand.provider()})

	run := testRun(5, entity.DesignRunKindCutout)
	run.Inputs = entity.RawJSON(`{"refs":[{"media_id":11}]}`)

	// ─── ПРОХОД ПЕРВЫЙ: заплатили и не узнали, чем кончилось.
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, stand.submits)
	require.Empty(t, sink.put, "собрать было нечего, значит и класть нечего")

	accepted := ""
	for _, f := range store.finished {
		if f.State == entity.DesignAttemptAccepted {
			accepted = f.ProviderRequestId
		}
	}
	require.Equal(t, fal.DefaultModelCutout+"#cut-77", accepted,
		"попытка обязана закрыться `accepted` С ИДЕНТИФИКАТОРОМ: это единственный след оплаченного "+
			"задания, и без него следующий проход начинает с нуля")

	// ─── ПРОХОД ВТОРОЙ: та же строка, тот же id, никакой оплаты.
	stand.failStatus = false
	store.getRun = &entity.DesignRun{
		Id: run.Id, Kind: run.Kind, Attempts: []entity.DesignRunAttempt{{
			RunId: run.Id, AttemptNo: 1, State: entity.DesignAttemptAccepted,
			ProviderRequestId: sql.NullString{String: accepted, Valid: true},
		}},
	}
	require.NoError(t, w.execute(context.Background(), run, "tok"))

	require.Equal(t, 1, stand.submits,
		"ВТОРОГО POST БЫТЬ НЕ ДОЛЖНО: у fal второй сабмит — второе списание за ту же картинку")
	require.Len(t, sink.put, 1, "картинка, купленная первым проходом, доехала вторым")
	require.Len(t, store.completed, 1)
}
