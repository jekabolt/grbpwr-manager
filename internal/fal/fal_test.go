package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestClient points a client at a stub and shortens every wait, so a poll loop in a test is
// milliseconds rather than minutes.
func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	return New(Config{
		APIKey:       "test-key-not-a-real-one",
		BaseURL:      base,
		HTTPTimeout:  2 * time.Second,
		PollInterval: 5 * time.Millisecond,
		PollTimeout:  2 * time.Second,
		UnitUSD:      0.5,
	})
}

// TestARouteWithNoKeyREFUSES_AND_NAMES_THE_VARIABLE.
//
// ⚠ THE ASSERTION IS ON THE WORDS, AND IT IS THE POINT OF THE TEST. This sentence is what a person
// reads on the screen when they press GENERATE. «not configured» is a fact about the process;
// «FAL_KEY is not set» is a fact the person can act on, and the owner who has just typed a key into
// a dashboard needs to be able to tell, from the button alone, whether that was the missing piece.
func TestARouteWithNoKeyREFUSES_AND_NAMES_THE_VARIABLE(t *testing.T) {
	require.Contains(t, ErrNotConfigured.Error(), "FAL_KEY is not set")

	c := New(Config{}) // no key
	require.False(t, c.Enabled())

	_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/f.png"})
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = c.Collect(context.Background(), "req-1", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = c.Await(context.Background(), "req-1", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNotConfigured)

	// A NIL CLIENT IS A DISABLED CLIENT, so a caller need not nil-check before asking.
	var nilC *Client
	require.False(t, nilC.Enabled())
}

// TestTheKeyIsNeverPRINTED. A config printed with %v, %+v or %s must not carry the secret; all
// three route through Stringer.
func TestTheKeyIsNeverPRINTED(t *testing.T) {
	c := Config{APIKey: "sk-super-secret-value", BaseURL: "https://queue.fal.run"}
	for _, s := range []string{c.String(), strings.TrimSpace(strings.Join([]string{c.String()}, ""))} {
		require.NotContains(t, s, "sk-super-secret-value")
		require.Contains(t, s, "REDACTED")
	}
	// An UNSET key stays visibly unset: whether the provider is configured at all is diagnostic,
	// and hiding that would turn a redaction into a second mystery.
	require.NotContains(t, Config{}.String(), "REDACTED")
}

// TestThePollingPathDropsTheSubPathButTheSubmitKeepsIt.
//
// fal submits a model id WHOLE and polls it at its BASE. Getting this backwards produces a paid
// build whose result can never be collected — the most expensive shape of mistake this package can
// make, because the money is already gone when the mistake is discovered.
func TestThePollingPathDropsTheSubPathButTheSubmitKeepsIt(t *testing.T) {
	require.Equal(t, "hitem3d/hi3d", queuePath("hitem3d/hi3d/v3.0/multi-view-to-3d"))
	require.Equal(t, "fal-ai/flux", queuePath("fal-ai/flux/dev"))
	// A two-segment id has no sub-path to drop.
	require.Equal(t, "fal-ai/fast-sdxl", queuePath("fal-ai/fast-sdxl"))
	// A malformed id is returned as it is: inventing a namespace here would produce a silently
	// mangled URL instead of a readable provider answer.
	require.Equal(t, "nonsense", queuePath("nonsense"))
}

// hitem3dModel is the NAMED-SLOT family, spelled out rather than taken from DefaultModel3D.
//
// ⚠ IT IS A LITERAL BECAUSE THE DEFAULT MOVED AND THE BODY DID NOT. Reading the configured slug
// here would make this test follow whatever family happens to be the default, and the assertion it
// carries — «the named body still goes to the named endpoint» — would evaporate silently the moment
// the default changed. Which is exactly what happened when it changed to meshy/v7.
const hitem3dModel = "hitem3d/hi3d/v3.0/multi-view-to-3d"

// TestSubmitSendsTheViewsBY_NAME. This is the whole reason this provider was asked for: the bench
// knows which plate is the front, and this route is the first one that can be told.
func TestSubmitSendsTheViewsBY_NAME(t *testing.T) {
	var gotPath, gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "req-42",
			"status_url": srvStatusURL(r.Host, "hitem3d/hi3d", "req-42"),
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	id, err := c.Submit(context.Background(), Request3D{
		Model:    hitem3dModel,
		FrontURL: "https://cdn.example/front.png",
		BackURL:  "https://cdn.example/back.png",
		LeftURL:  "https://cdn.example/left.png",
		RightURL: "https://cdn.example/right.png",
		// STATED AND EXPECTED TO VANISH: this family's payload has no text field, so the hint is
		// dropped rather than refused — and the assertion below is what keeps «dropped» honest.
		TexturePrompt: "matte black jersey",
	})
	require.NoError(t, err)
	require.Equal(t, "req-42", id)

	require.Equal(t, "/"+hitem3dModel, gotPath, "the submit keeps the model's whole sub-path")
	// fal's own scheme is `Key`, not `Bearer`. A Bearer prefix here is a 401 on every call.
	require.Equal(t, "Key test-key-not-a-real-one", gotAuth)

	require.Equal(t, "https://cdn.example/front.png", body["front_image_url"])
	require.Equal(t, "https://cdn.example/back.png", body["back_image_url"])
	require.Equal(t, "https://cdn.example/left.png", body["left_image_url"])
	require.Equal(t, "https://cdn.example/right.png", body["right_image_url"])
	require.Equal(t, "glb", body["export_format"], "the band shows GLB and only GLB")
	require.Equal(t, true, body["enable_texture"], "an untextured mesh answers a different question")
	// STATED, NOT OMITTED: the provider's own default for enable_pbr is TRUE, and PBR maps
	// quadruple the download for lighting nuance a product tile does not show.
	require.Equal(t, false, body["enable_pbr"])

	// AND NOT ONE FIELD OF THE OTHER FAMILY. An `image_urls` list reaching this endpoint would be a
	// build with no front view at all; a `texture_prompt` reaching it would be a hint nothing reads,
	// recorded in the history as though it had been read.
	require.NotContains(t, body, "image_urls")
	require.NotContains(t, body, "texture_prompt")
}

// TestTheMESHY_FAMILY_SENDS_AN_ORDERED_LIST_WITH_THE_FRONT_FIRST.
//
// ⚠ THIS IS THE AXIS THE OWNER COMPLAINED ABOUT, AND THE MIGRATION MAKES THE CONTRACT WEAKER ON IT.
// meshy's endpoint takes «images of the same object from different angles» with no way to say which
// angle each one is, so the ONE guarantee left is that the front leads the list. A hole in the
// middle (no left plate) must close up rather than shift anything: there are no positions to hold.
func TestTheMESHY_FAMILY_SENDS_AN_ORDERED_LIST_WITH_THE_FRONT_FIRST(t *testing.T) {
	var gotPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "req-77",
			"status_url": srvStatusURL(r.Host, "meshy/v7", "req-77"),
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	id, err := c.Submit(context.Background(), Request3D{
		// The RIGHT plate is given and the LEFT is not — the ordinary half-filled bench.
		FrontURL:      "https://cdn.example/front.png",
		BackURL:       "https://cdn.example/back.png",
		RightURL:      "https://cdn.example/right.png",
		TexturePrompt: "colourway BLK — the exact value is #0a0a0a; matte heavy jersey",
	})
	require.NoError(t, err)
	require.Equal(t, "req-77", id)
	require.Equal(t, "/"+DefaultModel3D, gotPath, "the default slug IS the meshy one now")

	require.Equal(t, []any{
		"https://cdn.example/front.png",
		"https://cdn.example/back.png",
		"https://cdn.example/right.png",
	}, body["image_urls"], "front first, occupied views only, in front-back-left-right order")

	require.Equal(t, true, body["should_texture"], "an untextured mesh answers a different question")
	require.Equal(t, false, body["enable_pbr"])
	require.Equal(t, true, body["enable_safety_checker"])
	require.Equal(t, "colourway BLK — the exact value is #0a0a0a; matte heavy jersey", body["texture_prompt"])

	// NOT ONE NAMED FIELD OF THE OTHER FAMILY, and no export_format either: meshy's schema has
	// neither, and a body carrying keys the validator does not know is a 422 that reads like ours.
	for _, k := range []string{"front_image_url", "back_image_url", "left_image_url", "right_image_url", "export_format", "enable_texture"} {
		require.NotContains(t, body, k)
	}
	// AND THE PRICE DIALS ARE LEFT TO THE PROVIDER: a stated symmetry_mode or target_polycount
	// would freeze today's guess into every future build.
	require.NotContains(t, body, "symmetry_mode")
	require.NotContains(t, body, "target_polycount")
}

// TestAnEmptySteerIsOMITTED_NOT_SENT_EMPTY. An empty texture_prompt is not «no hint», it is a hint
// that says nothing — and the provider's own default behaviour is the better answer to that.
func TestAnEmptySteerIsOMITTED_NOT_SENT_EMPTY(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "req-78"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/front.png"})
	require.NoError(t, err)
	require.NotContains(t, body, "texture_prompt")
	// POSITIVE CONTROL: the body itself did arrive, so «the key is absent» is a statement about the
	// key and not about an empty request.
	require.Equal(t, []any{"https://cdn.example/front.png"}, body["image_urls"])
}

// TestWhichFamilyASlugBelongsTo. The prefix decides the body, so it decides whether the plates keep
// their names — and an unrecognised slug must take the NAMED body, which a list-shaped endpoint
// refuses loudly, rather than the list, which a named endpoint accepts as a build with no front.
func TestWhichFamilyASlugBelongsTo(t *testing.T) {
	for _, slug := range []string{
		"meshy/v7/multi-image-to-3d",
		"fal-ai/meshy/v6/multi-image-to-3d",
		"MESHY/v7/multi-image-to-3d",
	} {
		require.Truef(t, isMeshyFamily(slug), "slug %q", slug)
		require.Truef(t, New(Config{APIKey: "k", Model3D: slug}).AcceptsTexturePrompt(), "slug %q", slug)
	}
	for _, slug := range []string{
		hitem3dModel,
		"fal-ai/flux/dev",
		"",
		"meshyish/v1/thing",
	} {
		require.Falsef(t, isMeshyFamily(slug), "slug %q", slug)
	}
	// The CONFIGURED slug is what the question is about — an empty Model3D falls back to the
	// default, which is the meshy one.
	require.True(t, New(Config{APIKey: "k"}).AcceptsTexturePrompt())
	require.False(t, New(Config{APIKey: "k", Model3D: hitem3dModel}).AcceptsTexturePrompt())
	// A NIL CLIENT ANSWERS NO rather than panicking: it sends nothing at all.
	var nilC *Client
	require.False(t, nilC.AcceptsTexturePrompt())
}

// TestTheDEFAULT_IS_MESHY_V7_AND_ITS_ESTIMATE_IS_ITS_PRICE.
//
// Both halves moved together and must stay together: an estimate left at the retired provider's
// $0.60 would under-report every build by half, quietly, in the ledger the daily cap reads.
func TestTheDEFAULT_IS_MESHY_V7_AND_ITS_ESTIMATE_IS_ITS_PRICE(t *testing.T) {
	require.Equal(t, "meshy/v7/multi-image-to-3d", DefaultModel3D)
	require.Equal(t, "meshy/v7", queuePath(DefaultModel3D),
		"the status and result endpoints hang off the namespace, not off the whole slug")
	// $1.20 is meshy v7's textured price on fal's own model page. Said in the test's own words:
	// comparing a number with the function that computes it is not a comparison.
	require.Equal(t, "1.2", New(Config{APIKey: "k"}).CostUSD(1).String())
}

func srvStatusURL(host, base, id string) string {
	return "http://" + host + "/" + base + "/requests/" + id + "/status"
}

// TestABuildWithNoFrontIsREFUSED_LOCALLY. A build without a front is not a cheaper build, it is a
// wrong one: hitem3d reads front_image_url as the face of the object.
func TestABuildWithNoFrontIsREFUSED_LOCALLY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request left the process for a job that could be refused locally, for free")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Submit(context.Background(), Request3D{BackURL: "https://cdn.example/back.png"})
	require.ErrorIs(t, err, ErrNoFrontView)
}

// TestARETIRED_MODEL_DOES_NOT_READ_AS_A_BUSY_SERVICE.
//
// ⚠ THIS IS THE DEFECT THAT ONCE TOOK DOWN BOTH AI FEATURES AT ONCE: a slug the provider had
// removed surfaced as an ordinary error, was classified as weather, and was retried to the attempt
// cap while the history row blamed the provider's availability. The two 404s of this API mean
// opposite things and are told apart BY PATH, never by the provider's English sentence.
func TestARETIRED_MODEL_DOES_NOT_READ_AS_A_BUSY_SERVICE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Not Found"})
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/f.png"})
	require.ErrorIs(t, err, ErrModelUnavailable, "404 on the submit path means the slug is gone")
	require.NotErrorIs(t, err, ErrRequestNotFound)
	require.Contains(t, err.Error(), DefaultModel3D, "the message has to name the slug to fix")

	_, err = c.Collect(context.Background(), "req-1", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrRequestNotFound, "404 on the request path means the id is worthless")
	require.NotErrorIs(t, err, ErrModelUnavailable)
}

// TestEveryStatusCodeGetsTheSentinelThatSaysWhatToDO. Classification is BY STATUS, never by the
// provider's wording, so a reworded message cannot silently reclassify a fault.
func TestEveryStatusCodeGetsTheSentinelThatSaysWhatToDO(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusPaymentRequired, ErrOutOfCredit},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusUnprocessableEntity, ErrBadRequest},
		{http.StatusBadRequest, ErrBadRequest},
		{http.StatusGone, ErrTaskFailed},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(`{"detail":[{"msg":"field required","type":"missing"}]}`))
		}))
		c := newTestClient(t, srv.URL)
		_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/f.png"})
		require.ErrorIsf(t, err, tc.want, "HTTP %d", tc.code)
		// The provider's own sentence is quoted for a human, even from the list-shaped 422 body.
		require.Contains(t, err.Error(), "field required")
		srv.Close()
	}
}

// ─────────────── МОДЕЛЬ ПЕРЕЕХАЛА, А СБОРКА УЖЕ ОПЛАЧЕНА ───────────────

// movedModelStand — очередь, в которой ОДИН запрос живёт под ОДНИМ пространством имён. Всё, что
// приходит по чужому пути, отвечает 404 — ровно как отвечает fal на id, которого в этом namespace
// нет.
//
// СТЕНД СЧИТАЕТ ОБРАЩЕНИЯ ПО ПУТЯМ, потому что утверждение пробы — не «ответ пришёл», а «искали
// там, где сборка на самом деле лежит».
type movedModelStand struct {
	live  string // queuePath, под которым запрос существует
	paths map[string]int
}

func newMovedModelStand(t *testing.T, live string) (*movedModelStand, *httptest.Server) {
	t.Helper()
	st := &movedModelStand{live: live, paths: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/model.glb") {
			_, _ = w.Write([]byte("glTF-bytes"))
			return
		}
		st.paths[queueNamespaceOf(r.URL.Path)]++
		if !strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/"), st.live+"/requests/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Request is not found"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/status") {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
			return
		}
		w.Header().Set(billableUnitsHeader, "1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_mesh": map[string]any{"url": "http://" + r.Host + "/model.glb"},
		})
	}))
	t.Cleanup(srv.Close)
	return st, srv
}

// queueNamespaceOf — первые два сегмента пути запроса, то есть тот самый queuePath, по которому
// клиент решил, где искать.
func queueNamespaceOf(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 {
		return p
	}
	return parts[0] + "/" + parts[1]
}

// TestAMovedDEFAULT_DOES_NOT_ORPHAN_A_PAID_BUILD.
//
// ⚠ ЭТО ДЕНЬГИ, И ТЕРЯЛИСЬ ОНИ ДВАЖДЫ ЗА ОДИН РАЗ. Сабмит — это платёж: попытка закрывается
// `accepted` с id, а сборка идёт минутами. Путь опроса выводится ИЗ СЛАГА МОДЕЛИ, поэтому релиз,
// приехавший в эти минуты, отправлял следующий опрос в `meshy/v7/requests/<id>/status` за id,
// купленным в `hitem3d/hi3d`. Провайдер отвечал 404, клиент — терминальным ErrRequestNotFound, и
// сборка пропадала. А поскольку 404 приходил на СТАТУС, заголовок x-fal-billable-units не читался
// вовсе — и в бухгалтерию платная сборка уезжала БЕСПЛАТНОЙ.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: вернуть опрос к `c.cfg.Model3D` (то есть убрать locateRequest из
// Collect и Await). ИГЛА УНИКАЛЬНА: единственный способ позеленеть — постучаться в то пространство
// имён, в котором сборка действительно лежит; проба это ещё и пересчитывает по путям.
func TestAMovedDEFAULT_DOES_NOT_ORPHAN_A_PAID_BUILD(t *testing.T) {
	// Сборка куплена под УШЕДШИМ слагом; клиент настроен на сегодняшний.
	st, srv := newMovedModelStand(t, queuePath(hitem3dModel))
	c := newTestClient(t, srv.URL)
	require.Equal(t, DefaultModel3D, c.Model(), "положительный контроль: клиент настроен на сегодняшний слаг")

	var model bytes.Buffer
	res, err := c.Collect(context.Background(), "req-moved", Sink{Model: &model})
	require.NoError(t, err, "оплаченная сборка не нашлась после переезда модели")
	require.Equal(t, "glTF-bytes", model.String(), "байты обязаны доехать, а не только статус")

	// ⚠ И ДЕНЬГИ ТОЖЕ. Это вторая половина дефекта: 404 на статусе означал ещё и цену NULL.
	require.Equal(t, 1.0, res.BillableUnits, "заряд провайдера читается только на выборке результата")

	require.Equal(t, 1, st.paths[queuePath(DefaultModel3D)], "настроенное пространство пробуется ПЕРВЫМ и один раз")
	require.GreaterOrEqual(t, st.paths[queuePath(hitem3dModel)], 1, "ушедшее пространство обязано быть опрошено")
}

// TestAWaitPastItsGraceLOOKS_WHERE_THE_BUILD_ACTUALLY_IS — тот же дефект на боевом пути.
//
// Воркер зовёт Await, а не Collect, и у Await впереди ЛЬГОТА: 404 в первые секунды — это отставание
// очереди, а не ответ (см. notFoundGrace). Поиск обязан стоять ровно ЗА льготой: раньше он тратил бы
// лишние обращения на нормальное отставание, позже — уже некуда, 404 к этому моменту терминален.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: та же — опрос по `c.cfg.Model3D`. ВТОРАЯ: поставить поиск ДО льготы;
// тогда `paths` настроенного пространства покажет одно обращение вместо нескольких, потому что
// первая же попытка уедет в чужой namespace.
func TestAWaitPastItsGraceLOOKS_WHERE_THE_BUILD_ACTUALLY_IS(t *testing.T) {
	st, srv := newMovedModelStand(t, queuePath(hitem3dModel))
	c := New(Config{
		APIKey: "k", BaseURL: srv.URL,
		HTTPTimeout: 2 * time.Second, PollInterval: 20 * time.Millisecond,
		PollTimeout: time.Second, // льгота = половина потолка = 500 мс
	})

	var model bytes.Buffer
	res, err := c.Await(context.Background(), "req-moved", Sink{Model: &model})
	require.NoError(t, err, "оплаченная сборка обязана найтись до того, как 404 станет терминальным")
	require.Equal(t, "glTF-bytes", model.String())
	require.Equal(t, 1.0, res.BillableUnits)

	require.Greater(t, st.paths[queuePath(DefaultModel3D)], 1,
		"льгота обязана отработать в настроенном пространстве, а не быть пропущена")
}

// TestAnIdNOBODY_KNOWS_IS_STILL_TERMINAL — отрицательный контроль поиска.
//
// Без него предыдущие две пробы зеленели бы и в мире, где клиент считает найденным что угодно:
// «искать в других пространствах» не имеет права превратиться в «не признавать 404 никогда».
func TestAnIdNOBODY_KNOWS_IS_STILL_TERMINAL(t *testing.T) {
	st, srv := newMovedModelStand(t, "nobody/at-all")
	c := newTestClient(t, srv.URL)

	_, err := c.Collect(context.Background(), "req-ghost", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrRequestNotFound, "id, которого нет нигде, обязан остаться терминальным")
	require.NotErrorIs(t, err, ErrModelUnavailable, "404 на пути запроса — не про слаг")
	require.GreaterOrEqual(t, len(st.paths), 2, "положительный контроль: поиск всё-таки состоялся")
}

// TestACandidateTHAT_COULD_NOT_BE_ASKED_IS_NOT_A_DENIAL.
//
// ⚠ P1 FROM REVIEW, AND IT DISCARDS A PAID BUILD. The configured namespace 404s after the grace,
// the build really is sitting under the retired slug — and that slug's status request answers 429.
// A search that treats «could not ask» as «asked and told no» concludes the id buys nothing;
// ErrRequestNotFound classifies NON-RETRYABLE (CodeEmptyResponse), the run closes, and the only
// road back is a second submit — a second charge — for a model already built and waiting.
//
// WHAT THIS PROBE REQUIRES: the outcome stays RETRYABLE and carries the provider's own transient
// sentinel, so the run backs off and the next pass searches again with the rate limit expired.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `continue` на любой ошибке кандидата (то есть слить locateUnknown с
// locateDenied). ИГЛА УНИКАЛЬНА: отрицательный контроль ниже требует, чтобы ЧЕСТНЫЙ отказ всех
// кандидатов остался терминальным, поэтому «никогда не верить 404» этой парой не позеленеет.
func TestACandidateTHAT_COULD_NOT_BE_ASKED_IS_NOT_A_DENIAL(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ns := queueNamespaceOf(r.URL.Path)
		asked = append(asked, ns)
		if ns == queuePath(hitem3dModel) {
			// The namespace the build actually lives under is rate-limited right now.
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"detail":"rate limit exceeded"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Request is not found"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Collect(context.Background(), "req-paid", Sink{Model: &bytes.Buffer{}})

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRequestNotFound,
		"кандидат ответил 429, а не «не знаю такого»: оплаченная сборка списана бы в терминальный отказ")
	require.ErrorIs(t, err, ErrRateLimited,
		"вердикт обязан нести транзиентную причину провайдера, иначе классификатор не назовёт его погодой")
	require.Contains(t, err.Error(), "req-paid", "id — единственное, чем оплаченную сборку можно найти снова")
	require.Contains(t, asked, queuePath(hitem3dModel), "положительный контроль: кандидата всё-таки спрашивали")
}

// TestAnUnaskableCandidateKEEPS_AWAIT_RETRYABLE_TOO — тот же довод на боевом пути.
//
// Воркер зовёт Await, а не Collect. Здесь важно ещё и то, что ожидание ЗАКАНЧИВАЕТСЯ повторяемым
// вердиктом, а не докручивает потолок опроса в пространстве, про которое уже известно, что оно
// отвечает 404: откат прогона (30 с × 2ⁿ) — это ровно тот ответ, которого просит 429.
func TestAnUnaskableCandidateKEEPS_AWAIT_RETRYABLE_TOO(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if queueNamespaceOf(r.URL.Path) == queuePath(hitem3dModel) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":"upstream boom"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Request is not found"}`))
	}))
	defer srv.Close()

	c := New(Config{
		APIKey: "k", BaseURL: srv.URL,
		HTTPTimeout: 2 * time.Second, PollInterval: 20 * time.Millisecond,
		PollTimeout: time.Second, // льгота = половина потолка = 500 мс
	})
	_, err := c.Await(context.Background(), "req-paid", Sink{Model: &bytes.Buffer{}})

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRequestNotFound, "незавершённый поиск не имеет права стать приговором")
}

// TestARecoveredBuildIsPricedASWHAT_IT_WAS_BOUGHT_AS.
//
// ⚠ P2 FROM REVIEW. Finding the build was only half the repair. An hitem3d turntable was estimated
// at $0.60; the deployment then moves to meshy, the fallback recovers the result — and pricing it
// with the CONFIGURED model books meshy's $1.20. Nothing in the row explains why a run frozen
// before the deploy settled at twice its own estimate.
//
// МУТАЦИЯ, КОТОРУЮ ЭТО КРАСИТ: `CostUSDFor(c.cfg.Model3D, …)` вместо модели результата, или
// возврат Result без поля Model. ИГЛА УНИКАЛЬНА: цены двух семейств различаются вдвое, и проба
// сверяет ИМЕННО цену, а не только слаг.
func TestARecoveredBuildIsPricedASWHAT_IT_WAS_BOUGHT_AS(t *testing.T) {
	_, srv := newMovedModelStand(t, queuePath(hitem3dModel))
	// ⚠ БЕЗ ТАРИФА НАРОЧНО: FAL_UNIT_USD не задан ни на одном сегодняшнем развёртывании, и ровно
	// тогда цену называет САМ ПАКЕТ — то есть только тогда выбор модели вообще влияет на число.
	c := New(Config{
		APIKey: "k", BaseURL: srv.URL,
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond, PollTimeout: 2 * time.Second,
	})
	require.Equal(t, DefaultModel3D, c.Model(), "положительный контроль: настроено сегодняшнее семейство")

	res, err := c.Collect(context.Background(), "req-moved", Sink{Model: &bytes.Buffer{}})
	require.NoError(t, err)

	require.Equal(t, hitem3dModel, res.Model,
		"результат обязан назвать слаг, ПОД КОТОРЫМ его нашли, иначе цену считать не по чему")
	require.Equal(t, "0.6", c.CostUSDFor(res.Model, res.BillableUnits).String(),
		"сборка hitem3d стоит hitem3d, даже когда развёртывание уже переехало на meshy")
	require.Equal(t, "1.2", c.CostUSD(res.BillableUnits).String(),
		"отрицательный контроль: по НАСТРОЕННОЙ модели это была бы цена meshy — та самая ошибка")

	// И ТАРИФ, КОГДА ОН ЗАДАН, ОДИН НА РАЗВЁРТЫВАНИЕ: оператор ввёл одно число, второго нет и
	// выдумывать его по слагу — это догадка в костюме арифметики.
	tariffed := New(Config{APIKey: "k", BaseURL: srv.URL, UnitUSD: 0.5})
	require.Equal(t, tariffed.CostUSDFor(hitem3dModel, 3).String(), tariffed.CostUSDFor(DefaultModel3D, 3).String(),
		"заданный тариф не зависит от модели")
}

// TestTheRETIRED_MODELS_ESTIMATE_TRAVELS_WITH_ITS_SLUG. Малая проба на таблицу цен: неизвестный
// слаг обязан отвечать ЦЕНОЙ СЕГОДНЯШНЕЙ МОДЕЛИ, а не самой дешёвой.
//
// ⚠ ДВЕ ОШИБКИ ЗДЕСЬ НЕСИММЕТРИЧНЫ. Оценить старую сборку по сегодняшней (большей) цене — завысить
// одну строку; оценить сегодняшнюю по старой (меньшей) — ЗАНИЗИТЬ реальные траты в учёте, ровно та
// беда, ради которой вся эта бухгалтерия и существует.
func TestTheRETIRED_MODELS_ESTIMATE_TRAVELS_WITH_ITS_SLUG(t *testing.T) {
	require.Equal(t, "0.6", EstimatedRequestUSDFor(hitem3dModel).String())
	require.Equal(t, "1.2", EstimatedRequestUSDFor(DefaultModel3D).String())
	require.Equal(t, "1.2", EstimatedRequestUSDFor("").String(), "неизвестное = сегодняшняя цена")
	require.Equal(t, "1.2", EstimatedRequestUSDFor("some/model/nobody/wrote/down").String())
	require.Equal(t, EstimatedRequestUSD().String(), EstimatedRequestUSDFor("").String(),
		"старое имя обязано остаться тем же выражением, а не второй копией числа")
}

// falStub serves one queue lifecycle: status, then the result, then the artifacts.
type falStub struct {
	statusAfter int    // how many status calls answer IN_PROGRESS before COMPLETED
	units       string // x-fal-billable-units on the result fetch; "" omits the header
	modelKey    string // which key names the delivered file; "" = model_mesh (hitem3d's spelling)
	noModelURL  bool
	calls       int
}

// TestAModelNamedMODEL_GLB_IsStillDelivered.
//
// ⚠ THE MOST EXPENSIVE MISTAKE THIS PACKAGE COULD MAKE IS READING ONE SPELLING. meshy returns the
// finished file as `model_glb` where hitem3d returns `model_mesh`. A client that knows only the old
// key sees a COMPLETED request with no file: it raises ErrNoModel WITH THE CHARGE ATTACHED — money
// gone, nothing delivered — and the row blames the provider.
func TestAModelNamedMODEL_GLB_IsStillDelivered(t *testing.T) {
	stub := &falStub{units: "1", modelKey: "model_glb"}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var model, thumb bytes.Buffer
	res, err := c.Await(context.Background(), "req-13", Sink{Model: &model, Thumbnail: &thumb})
	require.NoError(t, err, "a delivered model must not read as a missing one")
	require.Equal(t, "glTF-bytes", model.String())
	require.Equal(t, int64(10), res.ModelBytes)
	require.Equal(t, "png-bytes", thumb.String(), "the thumbnail is spelled the same either way")
}

func (s *falStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			s.calls++
			st := StatusCompleted
			if s.calls <= s.statusAfter {
				st = StatusInProgress
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": string(st), "queue_position": 0})
		case strings.HasSuffix(r.URL.Path, "/model.glb"):
			_, _ = w.Write([]byte("glTF-bytes"))
		case strings.HasSuffix(r.URL.Path, "/thumb.png"):
			_, _ = w.Write([]byte("png-bytes"))
		case strings.Contains(r.URL.Path, "/requests/"):
			if s.units != "" {
				w.Header().Set(billableUnitsHeader, s.units)
			}
			out := map[string]any{"thumbnail": map[string]any{"url": "http://" + r.Host + "/thumb.png"}}
			if !s.noModelURL {
				// modelKey is how THIS family spells the delivered file. The two spellings are the
				// same fact and the client must read both — see resultBody.
				key := "model_mesh"
				if s.modelKey != "" {
					key = s.modelKey
				}
				out[key] = map[string]any{
					"url": "http://" + r.Host + "/model.glb", "content_type": "model/gltf-binary",
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}
}

// TestAwaitBringsBackTHE_BYTES_AND_THE_PROVIDER_S_OWN_CHARGE.
//
// The two halves are one test because they arrive on one response: fal reports what a request cost
// in a HEADER on the result fetch, and that is the only number in the whole exchange that comes
// from the provider rather than from our configuration.
func TestAwaitBringsBackTHE_BYTES_AND_THE_PROVIDER_S_OWN_CHARGE(t *testing.T) {
	stub := &falStub{statusAfter: 2, units: "3"}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var model, thumb bytes.Buffer
	res, err := c.Await(context.Background(), "req-7", Sink{Model: &model, Thumbnail: &thumb})
	require.NoError(t, err)

	require.Equal(t, "glTF-bytes", model.String())
	require.Equal(t, "png-bytes", thumb.String())
	require.Equal(t, int64(10), res.ModelBytes)
	require.NotEmpty(t, res.ModelSHA256)
	require.Equal(t, formatGLB, res.Format)

	require.Equal(t, 3.0, res.BillableUnits)
	require.False(t, res.UnitsAssumed, "the provider named the number; nothing was assumed")
	require.True(t, c.CostUSD(res.BillableUnits).Equal(c.CostUSD(3)))
	require.Equal(t, "1.5", c.CostUSD(res.BillableUnits).String(), "3 units at FAL_UNIT_USD=0.5")

	// AND NO URL CROSSES THE PACKAGE BOUNDARY. fal's artifact links expire; a stored link is a
	// model that quietly stops existing, so the Result has nowhere to put one.
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "http", "a Result must carry bytes and money, never a link")
}

// TestAMissingBillingHeaderIsASSUMED_AND_SAYS_SO.
//
// ⚠ RECORDING NOTHING WOULD MAKE A PAID 3D BUILD READ AS FREE, which is the failure the whole
// ledger exists to prevent; recording an assumption as though it were the provider's own figure
// would be a different lie. So the number is produced AND the guess is flagged, at the one place
// the decision is made.
func TestAMissingBillingHeaderIsASSUMED_AND_SAYS_SO(t *testing.T) {
	stub := &falStub{units: ""}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Await(context.Background(), "req-8", Sink{Model: &bytes.Buffer{}})
	require.NoError(t, err)
	require.Equal(t, 1.0, res.BillableUnits, "one unit per request is fal's marketplace default")
	require.True(t, res.UnitsAssumed, "the flag is what stops a guess hardening into a measurement")
	require.True(t, c.CostUSD(res.BillableUnits).IsPositive(), "«free» is the worse lie")
}

// TestA_COMPLETED_REQUEST_WITH_NO_MODEL_STILL_CARRIES_ITS_CHARGE.
//
// The most expensive line in the package: the build succeeded, the units are spent, and there is
// simply no file url to fetch it with. Dropping the charge here is exactly how paid failures came
// to be invisible to the ledger.
func TestA_COMPLETED_REQUEST_WITH_NO_MODEL_STILL_CARRIES_ITS_CHARGE(t *testing.T) {
	stub := &falStub{units: "2", noModelURL: true}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Await(context.Background(), "req-9", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNoModel)

	units, ok := Charge(err)
	require.True(t, ok, "the provider named a charge and it must reach the ledger")
	require.Equal(t, 2.0, units)

	// AND AN UNBILLED FAILURE IS NOT DRESSED AS A BILLED ONE: zero units means «nobody could say»,
	// which is a different claim from «it was free».
	_, ok = Charge(ErrNoModel)
	require.False(t, ok)
	require.Equal(t, ErrNoModel, chargedWith(ErrNoModel, 0, "req-9", DefaultModel3D),
		"zero units means «the provider did not say» and must not be wrapped as a charge of zero")
}

// TestAnUnfinishedRequestIsNOT_READY_AND_IS_NOT_CHARGED. Await loops on this rather than ending on
// it, and a charge attached here would be re-read on every poll of the same unfinished job.
func TestAnUnfinishedRequestIsNOT_READY_AND_IS_NOT_CHARGED(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/status"),
			"an unfinished request must not have its result fetched")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "IN_QUEUE", "queue_position": 4})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Collect(context.Background(), "req-10", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNotReady)
	_, ok := Charge(err)
	require.False(t, ok)
}

// TestThePollCeilingReadsAsACeiling. «The wait ran out, look again later» and «the request failed»
// point a worker in opposite directions, and only the first is true here — the id is still worth
// something.
func TestThePollCeilingReadsAsACeiling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "IN_PROGRESS"})
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL, PollInterval: time.Millisecond, PollTimeout: 30 * time.Millisecond})
	_, err := c.Await(context.Background(), "req-11", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrTimedOut)
	require.Contains(t, err.Error(), "req-11", "the id is the only thing that can find a paid job again")
}

// TestA_404_IN_THE_FIRST_SECONDS_IS_A_LAG_NOT_AN_ANSWER.
//
// The submit IS the payment, and the first lookup has no pause in front of it. Taken at face value,
// a read-after-write lag of one second would throw away a build bought a second earlier, and the
// only road back from a discarded id is a second charge.
func TestA_404_IN_THE_FIRST_SECONDS_IS_A_LAG_NOT_AN_ANSWER(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			n++
			if n < 3 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/model.glb") {
			_, _ = w.Write([]byte("glb"))
			return
		}
		w.Header().Set(billableUnitsHeader, "1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_mesh": map[string]any{"url": "http://" + r.Host + "/model.glb"},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Await(context.Background(), "req-12", Sink{Model: &bytes.Buffer{}})
	require.NoError(t, err, "a 404 inside the grace must not discard a paid build")
	require.Equal(t, "req-12", res.RequestID)
}

// TestAnOversizedArtifactIsREFUSED_NOT_TRUNCATED. A GLB cut at the boundary is a file that opens in
// nothing and looks like a provider defect for as long as it takes somebody to compare byte counts.
func TestAnOversizedArtifactIsREFUSED_NOT_TRUNCATED(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 64)
	_, err := readCapped(bytes.NewReader(big), 16)
	require.ErrorIs(t, err, ErrTooLarge)

	var sink bytes.Buffer
	r := newCapReader(bytes.NewReader(big), 16)
	_, err = sink.ReadFrom(r)
	require.ErrorIs(t, err, ErrTooLarge)
}

// TestAReferenceTheProviderCannotFetchIsREFUSED. The provider downloads these itself, so a bucket
// key or a file:// url is a mistake worth catching here rather than as a provider failure later.
func TestAReferenceTheProviderCannotFetchIsREFUSED(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	for _, bad := range []string{"design/plate.png", "file:///etc/passwd", "ftp://x/y.png", ""} {
		_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://ok/f.png", BackURL: bad})
		if bad == "" {
			continue // an empty optional view is simply omitted, not refused
		}
		require.ErrorIsf(t, err, ErrBadImageURL, "reference %q", bad)
	}
}

// TestCostUSDWithoutTariff — сторож у дефекта, который доехал до владельца.
//
// ЧТО СЛУЧИЛОСЬ. Прогон 17 на бете (2026-09-01) вернул СТО биллинговых единиц. Умолчание стоило
// доллар ЗА ЕДИНИЦУ — под доводом «маркетплейсные модели fal берут одну единицу за запрос», — и в
// бухгалтерию уехали 100.0000 USD при оценке 0.60. Одно это число съело дневной потолок и
// заблокировало генерацию.
//
// ЧЕГО ЭТОТ ТЕСТ ТРЕБУЕТ. Без заданного тарифа цена НЕ ЗАВИСИТ от числа единиц: провайдер честно
// называет их количество, но что он ими меряет — знает только его прайс, и умножение на выдуманный
// тариф даёт тем более убедительное враньё, чем больше единиц вернул провайдер. С заданным тарифом
// арифметика возвращается: развёртывание знает, что у этой модели является единицей.
func TestCostUSDWithoutTariff(t *testing.T) {
	t.Parallel()

	noTariff := New(Config{APIKey: "k"})

	one := noTariff.CostUSD(1)
	hundred := noTariff.CostUSD(100)

	require.True(t, one.IsPositive(), "«бесплатно» — худшая ложь, чем оценка")
	require.True(t, hundred.Equal(one),
		"без тарифа цена НЕ УМНОЖАЕТСЯ на единицы: 100 единиц дали %s против %s за одну",
		hundred, one)
	require.Less(t, hundred.InexactFloat64(), 5.0,
		"оценка сборки обязана остаться в порядке цены сборки, а не стать сотней долларов: %s",
		hundred)

	withTariff := New(Config{APIKey: "k", UnitUSD: 0.01})
	require.Equal(t, "1", withTariff.CostUSD(100).String(),
		"с заданным тарифом арифметика единиц возвращается")
}
