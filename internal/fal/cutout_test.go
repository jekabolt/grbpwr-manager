package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// cutoutStub serves one background-removal lifecycle: the submit, the status, the result envelope
// and the picture itself.
type cutoutStub struct {
	statusAfter int    // how many status lookups answer IN_PROGRESS before COMPLETED
	notFoundFor int    // how many status lookups answer 404 first (the queue's read-after-write lag)
	units       string // x-fal-billable-units on the RESULT fetch; "" omits the header
	contentType string // what the provider calls the file it returns
	noImageURL  bool   // a COMPLETED request whose answer carries no picture url
	huge        bool   // serve more than maxCutoutBytes
	submitCode  int    // non-zero: answer the submit with this status instead of a request id
	payload     []byte // the picture

	// captured
	submitPath string
	submitBody map[string]any
	auth       string
	submits    int
	statusHits int
}

func (s *cutoutStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cutout.png":
			if s.huge {
				_, _ = io.CopyN(w, zeroes{}, maxCutoutBytes+4096)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(s.payload)
		case strings.HasSuffix(r.URL.Path, "/status"):
			s.statusHits++
			if s.statusHits <= s.notFoundFor {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"Request is not found"}`))
				return
			}
			st := StatusCompleted
			if s.statusHits <= s.notFoundFor+s.statusAfter {
				st = StatusInProgress
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": string(st)})
		case strings.Contains(r.URL.Path, "/requests/"):
			if s.units != "" {
				w.Header().Set(billableUnitsHeader, s.units)
			}
			img := map[string]any{"content_type": s.contentType}
			if !s.noImageURL {
				img["url"] = "http://" + r.Host + "/cutout.png"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"image": img})
		case r.Method == http.MethodPost:
			s.submits++
			s.submitPath, s.auth = r.URL.Path, r.Header.Get("Authorization")
			require.NoError(t, json.NewDecoder(r.Body).Decode(&s.submitBody))
			if s.submitCode != 0 {
				w.WriteHeader(s.submitCode)
				_, _ = w.Write([]byte(`{"detail":"model not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "cut-1"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}
}

// zeroes is an endless reader, so the size cap can be exercised without a 25 MiB literal.
type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { return len(p), nil }

// newCutoutClient points a client at a stub and shortens every wait. The cut-out tariff is left
// UNSET on purpose in most tests: that is the deployment shape this route will actually ship in.
func newCutoutClient(t *testing.T, base string) *Client {
	t.Helper()
	return New(Config{
		APIKey:       "test-key-not-a-real-one",
		BaseURL:      base,
		HTTPTimeout:  2 * time.Second,
		PollInterval: 5 * time.Millisecond,
		PollTimeout:  time.Second,
	})
}

// TestRemoveBackgroundWithNoKeyREFUSES_AND_NAMES_THE_VARIABLE.
//
// ⚠ ПРОВЕРЯЮТСЯ СЛОВА, И В ЭТОМ ВЕСЬ СМЫСЛ. Эту фразу человек читает на экране, нажав кнопку.
// «not configured» — факт о процессе; «FAL_KEY is not set» — факт, с которым можно что-то сделать,
// и владелец, только что вбивший ключ в дашборд, обязан понять по кнопке, тот ли ключ был нужен.
func TestRemoveBackgroundWithNoKeyREFUSES_AND_NAMES_THE_VARIABLE(t *testing.T) {
	require.Contains(t, ErrNotConfigured.Error(), "FAL_KEY is not set")

	c := New(Config{}) // no key
	_, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", &bytes.Buffer{})
	require.ErrorIs(t, err, ErrNotConfigured)

	// A nil client is a disabled client here too, and it still answers the question «which model
	// would you cut with» rather than an empty string a caller could read as «no route».
	var nilC *Client
	_, err = nilC.RemoveBackground(context.Background(), "https://cdn.example/a.png", &bytes.Buffer{})
	require.ErrorIs(t, err, ErrNotConfigured)
	require.Equal(t, DefaultModelCutout, nilC.ModelCutout())
}

// TestTheCutoutDefaultIsTheMIT_MODEL.
//
// ⚠ ЭТО ПРОВЕРКА ЛИЦЕНЗИИ, А НЕ ОПЕЧАТКИ. Соседние по качеству веса того же семейства (BRIA
// RMBG-2.0) — CC BY-NC, и попасть на них проще всего НЕ выбрав ничего: библиотека `rembg` грузит их
// по умолчанию. Умолчание, уехавшее на такой слаг, — это лицензионное обязательство, которое никто
// не принимал, и заметить его по экрану невозможно: картинки будут выходить правильные.
func TestTheCutoutDefaultIsTheMIT_MODEL(t *testing.T) {
	require.Equal(t, "fal-ai/birefnet/v2", DefaultModelCutout)
	require.NotContains(t, strings.ToLower(DefaultModelCutout), "bria")
	require.NotContains(t, strings.ToLower(DefaultModelCutout), "rmbg")

	// The slug is submitted WHOLE and polled at its BASE — the rule that, got backwards, produces a
	// paid request whose result can never be collected.
	require.Equal(t, "fal-ai/birefnet", queuePath(DefaultModelCutout))

	// An unset override leaves the default in force; a configured one wins.
	require.Equal(t, DefaultModelCutout, New(Config{}).ModelCutout())
	require.Equal(t, "vendor/matting/v9", New(Config{ModelCutout: " /vendor/matting/v9/ "}).ModelCutout())
}

// TestRemoveBackgroundSendsTheMattingBODY_AND_BRINGS_BACK_THE_BYTES.
//
// The body is asserted field by field because each field is a decision: `output_format: png` is the
// only format that can carry an alpha channel at all, and `refine_foreground` is what keeps the
// cut-out's edge from carrying the colour of the background it was cut from.
func TestRemoveBackgroundSendsTheMattingBODY_AND_BRINGS_BACK_THE_BYTES(t *testing.T) {
	stub := &cutoutStub{statusAfter: 2, units: "3", contentType: "image/png", payload: []byte("png-with-alpha")}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newCutoutClient(t, srv.URL)
	var dst bytes.Buffer
	res, err := c.RemoveBackground(context.Background(), "https://cdn.example/shirt.jpg", &dst)
	require.NoError(t, err)

	require.Equal(t, "/"+DefaultModelCutout, stub.submitPath, "the submit keeps the model's whole sub-path")
	// fal's own scheme is `Key`, not `Bearer`: a Bearer prefix is a 401 on every call.
	require.Equal(t, "Key test-key-not-a-real-one", stub.auth)
	require.Equal(t, "https://cdn.example/shirt.jpg", stub.submitBody["image_url"])
	require.Equal(t, "png", stub.submitBody["output_format"],
		"PNG is the only asked-for format because it is the only one that can carry the alpha "+
			"this whole route exists to produce")
	require.Equal(t, true, stub.submitBody["refine_foreground"],
		"without it the cut-out's edge keeps the colour of the background it was cut from")

	require.Equal(t, "png-with-alpha", dst.String())
	require.Equal(t, int64(len("png-with-alpha")), res.Bytes)
	require.NotEmpty(t, res.SHA256)
	require.Equal(t, "cut-1", res.RequestID)
	require.Equal(t, DefaultModelCutout, res.Model)
	require.Equal(t, "image/png", res.ContentType)

	require.Equal(t, 3.0, res.BillableUnits)
	require.False(t, res.UnitsAssumed, "the provider named the number; nothing was assumed")

	// AND NO URL CROSSES THE PACKAGE BOUNDARY. fal's artifact links expire; a stored link is a
	// picture that quietly stops existing, so CutoutResult has nowhere to put one.
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "http", "a CutoutResult carries bytes and money, never a link")
}

// TestTheCutoutTARIFF_IS_ITS_OWN.
//
// ⚠ ОДНА ПЕРЕМЕННАЯ НА ДВА МАРШРУТА БЫЛА БЫ УТВЕРЖДЕНИЕМ, ЧТО ЕДИНИЦА МАТИРУЮЩЕЙ МОДЕЛИ И ЕДИНИЦА
// СБОРКИ 3D — ОДНО ЧИСЛО. Они отличаются на два порядка, и общий тариф оценил бы двухцентовую
// операцию в доллар или доллар — в два цента, в зависимости лишь от того, под какой маршрут его
// настраивали. Ровно эта форма ошибки (умножение на выдуманный тариф) уже стоила здесь ста долларов.
func TestTheCutoutTARIFF_IS_ITS_OWN(t *testing.T) {
	// FAL_UNIT_USD is set and FAL_UNIT_USD_CUTOUT is not: the cut-out must NOT be priced off the 3D
	// rate, it must fall back to its own per-request estimate.
	c := New(Config{APIKey: "k", UnitUSD: 0.5})
	require.Equal(t, EstimatedCutoutUSD().String(), c.CostCutoutUSD(3).String(),
		"an unset cut-out tariff falls back to the cut-out's own estimate, never to the 3D rate")
	require.NotEqual(t, c.CostUSD(3).String(), c.CostCutoutUSD(3).String())

	// Configured: the real arithmetic, units × rate.
	c = New(Config{APIKey: "k", UnitUSD: 0.5, UnitUSDCutout: 0.03})
	require.Equal(t, "0.09", c.CostCutoutUSD(3).String())
	require.Equal(t, "1.5", c.CostUSD(3).String(), "the 3D rate is untouched by the cut-out one")

	// Zero units is zero money, and a nil client cannot spend.
	require.True(t, c.CostCutoutUSD(0).IsZero())
	var nilC *Client
	require.True(t, nilC.CostCutoutUSD(3).IsZero())

	// The estimate is a plausible price rather than a zero: «this run was free» is the worse lie.
	require.True(t, EstimatedCutoutUSD().IsPositive())
}

// TestAMissingBillingHeaderOnACutoutIsASSUMED_AND_FLAGGED. Same rule as the 3D route: recording
// nothing would make a paid cut-out read as free, and recording an assumption as the provider's own
// figure would be a different lie — so the number is produced AND the guess is flagged.
func TestAMissingBillingHeaderOnACutoutIsASSUMED_AND_FLAGGED(t *testing.T) {
	stub := &cutoutStub{units: "", contentType: "image/png", payload: []byte("x")}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newCutoutClient(t, srv.URL)
	res, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, 1.0, res.BillableUnits, "one unit per request is fal's marketplace default")
	require.True(t, res.UnitsAssumed, "the flag is what stops a guess hardening into a measurement")
}

// TestACompletedCutoutWithNoPictureIsBILLED_AND_SAID_SO.
//
// ЭТО САМАЯ ДОРОГАЯ СТРОКА МАРШРУТА: запрос ЗАВЕРШЁН — значит картинка сделана и единицы потрачены —
// а ссылки, которой её забрать, в ответе нет. Деньги обязаны доехать до книги, поэтому ошибка несёт
// на себе заряд; и она обязана классифицироваться как «оплачено, показать нечего», а не как погода,
// иначе тот же пустой ответ будет куплен пять раз.
func TestACompletedCutoutWithNoPictureIsBILLED_AND_SAID_SO(t *testing.T) {
	stub := &cutoutStub{units: "2", noImageURL: true}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newCutoutClient(t, srv.URL)
	_, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", &bytes.Buffer{})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoCutout)
	// ⚠ И ЭТО ЖЕ — ОТВЕТ КЛАССИФИКАТОРУ, КОТОРЫЙ ПРО ЭТОТ МАРШРУТ НЕ ЗНАЕТ НИЧЕГО. ErrNoModel уже
	// значит «оплачено, показать нечего»: не повторять, попытку закрыть `unknown`. Свежий сентинел
	// без пары в классификаторе провалился бы в его retryable-умолчание.
	require.ErrorIs(t, err, ErrNoModel)

	units, ok := Charge(err)
	require.True(t, ok, "a billed failure must carry its charge; otherwise the spend never reaches the ledger")
	require.Equal(t, 2.0, units)
	require.Equal(t, DefaultModelCutout, ChargedModel(err))
}

// TestACutoutOverTheSizeCapIsREFUSED_NOT_TRUNCATED. A PNG cut at the boundary decodes to nothing and
// reads as a provider defect. The money is still real, so the refusal carries the charge.
func TestACutoutOverTheSizeCapIsREFUSED_NOT_TRUNCATED(t *testing.T) {
	// ⚠ ЧИСЛО НАЗВАНО ЗДЕСЬ ЛИТЕРАЛОМ, И ЭТО НЕ ТАВТОЛОГИЯ, А ЕДИНСТВЕННЫЙ СПОСОБ ЕГО УДЕРЖАТЬ.
	// Стенд ниже отдаёт «на 4 КиБ больше потолка», то есть считает свою нагрузку ОТ САМОЙ КОНСТАНТЫ:
	// подними потолок до гигабайта — и нагрузка поднимется вместе с ним, проверка останется зелёной,
	// а бэкенд на полугигабайтной машине примет в память гигабайтную картинку. Измерено: без этой
	// строки мутация «25 MiB → 1 GiB» не роняет ни одной проверки.
	require.Equal(t, int64(25<<20), int64(maxCutoutBytes),
		"the cut-out ceiling is a memory budget on a 0.5 GB instance, not a round number")

	stub := &cutoutStub{units: "1", huge: true, contentType: "image/png"}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newCutoutClient(t, srv.URL)
	_, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", io.Discard)
	require.ErrorIs(t, err, ErrTooLarge)
	units, ok := Charge(err)
	require.True(t, ok)
	require.Equal(t, 1.0, units)
}

// TestAnUnfetchableSourceIsREFUSED_LOCALLY_FOR_FREE. A reference the provider cannot fetch is not
// weather and does not improve on a retry — and refusing it before the request leaves is the
// difference between a free refusal and a paid one.
func TestAnUnfetchableSourceIsREFUSED_LOCALLY_FOR_FREE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request left the process for a job that could be refused locally, for free")
	}))
	defer srv.Close()

	c := newCutoutClient(t, srv.URL)
	for _, ref := range []string{"", "   ", "s3://bucket/key.png", "not a url at all"} {
		_, err := c.RemoveBackground(context.Background(), ref, &bytes.Buffer{})
		require.ErrorIs(t, err, ErrBadImageURL, "reference %q", ref)
	}

	// A sink is not optional: «delivered» with nowhere to deliver to would spend the units and
	// throw the picture away.
	_, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nowhere to put")
}

// TestARetiredCutoutSlugIsITS_OWN_FAULT_NOT_WEATHER. A 404 on the SUBMIT path means the slug is
// gone — a setting to fix — and this repository has already paid once for that fault reading as a
// temporary outage on the screen.
func TestARetiredCutoutSlugIsITS_OWN_FAULT_NOT_WEATHER(t *testing.T) {
	stub := &cutoutStub{submitCode: http.StatusNotFound}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newCutoutClient(t, srv.URL)
	_, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", &bytes.Buffer{})
	require.ErrorIs(t, err, ErrModelUnavailable)
	require.NotErrorIs(t, err, ErrRequestNotFound, "a missing MODEL and a missing REQUEST are two faults")
	require.Equal(t, 1, stub.submits, "a submit is a payment; it is never repeated inside one call")
}

// TestA404InTheFirstMomentsIsALAG_NOT_AN_ANSWER.
//
// ⚠ ПЕРВЫЙ ОПРОС ИДЁТ БЕЗ ПАУЗЫ, А САБМИТ — ЭТО ОПЛАТА. Задержка «запись-потом-чтение» в очереди на
// секунду иначе выбросила бы вырез, купленный секундой раньше, и единственная дорога назад от
// выброшенного идентификатора — второй платёж.
func TestA404InTheFirstMomentsIsALAG_NOT_AN_ANSWER(t *testing.T) {
	stub := &cutoutStub{notFoundFor: 3, units: "1", contentType: "image/png", payload: []byte("kept")}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newCutoutClient(t, srv.URL)
	var dst bytes.Buffer
	res, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", &dst)
	require.NoError(t, err, "a queue that has not caught up with its own submit must not lose the picture")
	require.Equal(t, "kept", dst.String())
	require.Equal(t, "cut-1", res.RequestID)
	require.Equal(t, 1, stub.submits, "the wait resumed; it did not buy a second cut-out")
}

// TestA404ThatOUTLIVES_THE_GRACE_IS_TERMINAL. The other half: an id that really buys nothing must
// reach its verdict inside the wait rather than surfacing as a timeout, which points a worker the
// other way.
func TestA404ThatOUTLIVES_THE_GRACE_IS_TERMINAL(t *testing.T) {
	stub := &cutoutStub{notFoundFor: 1 << 30}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := New(Config{
		APIKey:       "k",
		BaseURL:      srv.URL,
		HTTPTimeout:  2 * time.Second,
		PollInterval: 5 * time.Millisecond,
		// Grace is half the ceiling, so a 300 ms ceiling gives a 150 ms grace and the verdict lands
		// well inside the wait.
		PollTimeout: 300 * time.Millisecond,
	})
	_, err := c.RemoveBackground(context.Background(), "https://cdn.example/a.png", &bytes.Buffer{})
	require.ErrorIs(t, err, ErrRequestNotFound)
	require.NotErrorIs(t, err, ErrTimedOut, "«this id buys nothing» and «the wait ran out» are different verdicts")
}

// TestTheCutoutSettingsAreNEVER_PRINTED_AND_ARE_PRINTED. The key stays redacted; the two new
// settings have to be visible, because a config dump that omits them cannot answer «which model did
// this deployment cut with, and at what rate».
func TestTheCutoutSettingsAreNEVER_PRINTED_AND_ARE_PRINTED(t *testing.T) {
	c := Config{APIKey: "sk-super-secret-value", ModelCutout: "vendor/matting/v9", UnitUSDCutout: 0.031}
	s := c.String()
	require.NotContains(t, s, "sk-super-secret-value")
	require.Contains(t, s, "REDACTED")
	require.Contains(t, s, "ModelCutout:vendor/matting/v9")
	require.Contains(t, s, "UnitUSDCutout:0.031")
}
