package runblob

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ B-31 — the image transport: submit → poll → download, and the money boundary at the 201 ═══
//
// FIXTURES from runblob-specs/kling.json and the Nano Banana docs page (read 2026-09-28): the
// request fields per family, the 201 answer's id field, the status words, the result url field, the
// 402 body. UNVERIFIED (G-06): every VALUE (prices, cdn paths, model labels, the `message` codes on a
// photo endpoint), the CDN's Content-Type, and that the CDN answers a plain GET without a token.

// pngB64 — a real one-pixel PNG: the transport hands back what it downloaded, and the sniffer must
// call it a picture.
const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYPj/HwADAgH/pZzT0QAAAABJRU5ErkJggg=="

func pngBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(pngB64)
	require.NoError(t, err)
	return b
}

// imageStand is a fake runblob for pictures: it records every request and answers the submit, the
// status reads (one answer per read, the last repeated) and the CDN GET.
type imageStand struct {
	t *testing.T
	// submit is the 201 (or refusal) body; submitStatus its HTTP status.
	submitStatus int
	submit       string
	// statuses are the status answers in order; each {status, body}. The last repeats.
	statuses []stand
	// cdn answers GET /cdn/out.png: status, content-type, bytes.
	cdnStatus int
	cdnType   string
	cdnBody   []byte

	mu      sync.Mutex
	paths   []string
	methods []string
	bodies  []string
	auths   []string
	reads   int
	srv     *httptest.Server
}

type stand struct {
	code int
	body string
}

func newImageStand(t *testing.T) *imageStand {
	t.Helper()
	s := &imageStand{t: t, submitStatus: http.StatusCreated, cdnStatus: http.StatusOK, cdnType: "image/png", cdnBody: pngBytes(t)}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

// url of the CDN picture this stand serves — the status answers name it.
func (s *imageStand) cdnURL() string { return s.srv.URL + "/cdn/out.png" }

func (s *imageStand) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.methods = append(s.methods, r.Method)
	s.bodies = append(s.bodies, string(b))
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	s.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/generate"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.submitStatus)
		_, _ = io.WriteString(w, s.submit)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/generations/"):
		s.mu.Lock()
		i := s.reads
		s.reads++
		s.mu.Unlock()
		if i >= len(s.statuses) {
			i = len(s.statuses) - 1
		}
		a := s.statuses[i]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.code)
		_, _ = io.WriteString(w, a.body)
	case r.Method == http.MethodGet && r.URL.Path == "/cdn/out.png":
		if s.cdnType != "" {
			w.Header().Set("Content-Type", s.cdnType)
		}
		w.WriteHeader(s.cdnStatus)
		_, _ = w.Write(s.cdnBody)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func (s *imageStand) recorded() (paths, methods, bodies, auths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]string(nil), s.methods...),
		append([]string(nil), s.bodies...), append([]string(nil), s.auths...)
}

// completedGemini / completedKling — a completed status answer naming the stand's CDN picture.
func (s *imageStand) completedGemini() stand {
	return stand{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"completed","prompt":"p","result_image_url":"` + s.cdnURL() + `","message":null}`}
}
func (s *imageStand) completedKling() stand {
	return stand{http.StatusOK, `{"generation_id":"` + genID + `","status":"completed","prompt":"p","image_url":"` + s.cdnURL() + `","model":"kling-o1-photo"}`}
}

var (
	processingGemini = stand{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"processing","prompt":"p","result_image_url":null,"message":null}`}
	pendingGemini    = stand{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"pending","prompt":"p","result_image_url":null,"message":null}`}
	failedGemini     = stand{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"failed","prompt":"p","result_image_url":null,"message":"CONTENT_POLICY_VIOLATION"}`}
	geminiSubmit     = `{"task_uuid":"` + taskUUID + `","status":"pending","price":"0.0210"}`
	klingSubmit      = `{"generation_id":"` + genID + `","status":"pending","price":"0.0290"}`
)

// imagesAt — an image transport over a client pointed at the stand, polling fast.
func imagesAt(s *imageStand) *Images {
	t := NewImages(newAt(s.srv.URL, 2*time.Second))
	t.poll = time.Millisecond
	return t
}

func imageCallErr(t *testing.T, err error) *aiprov.CallError {
	t.Helper()
	return callErr(t, err)
}

// ─── the surface ─────────────────────────────────────────────────────────────────────────────────

// TestImagesSurface — the default slug, Serves over the closed eight (and nothing else), Enabled
// following the key, nil-safety, and the pricing catalogue naming exactly the transport's slugs.
//
// MUTATION (measured red → green): a ninth slug added to imageSlugs without a catalogue row → red
// (the two lists must agree, or the panel offers a slug the transport refuses — or the reverse).
func TestImagesSurface(t *testing.T) {
	var none *Images
	require.False(t, none.Enabled())
	require.False(t, none.Serves(SlugGeminiStandard))
	require.Equal(t, SlugGeminiStandard, none.Model())

	tr := NewImages(New(Config{KeyFunc: key("")}))
	require.False(t, tr.Enabled(), "no key = disabled")
	require.Equal(t, "gemini/standard", tr.Model())
	for _, s := range []string{"gemini/standard", "gemini/pro", "gemini/v2", "gemini/v2_lite", "gemini/pro_vip", "gemini/v2_vip",
		"kling/o1-photo", "kling/o3-photo", "  gemini/pro  "} {
		require.True(t, tr.Serves(s), s)
	}
	for _, s := range []string{"", "openai/gpt-image-2", "gemini", "kling", "gemini/standard/x", "Gemini/standard", "kling/o1-video", "veo"} {
		require.False(t, tr.Serves(s), "%q must be skipped by the chooser", s)
	}
	require.True(t, NewImages(New(Config{KeyFunc: key("k")})).Enabled())

	// The panel's image rows (pricing) and the transport's Serves are one list: every served slug is
	// offered, and every offered image slug is served — except the chatgpt-images family, which lane H5
	// wires into this transport (listed ahead of it; pendingH5 empties when H5 lands). Video rows belong
	// to the video route, not this transport.
	pendingH5 := map[string]bool{} // H5 landed: every image row is served
	offered := map[string]bool{}
	for _, m := range pricing.Catalogue(entity.AIProviderRunblob) {
		if m.Kind != pricing.KindImage {
			continue
		}
		offered[m.Slug] = true
		require.True(t, tr.Serves(m.Slug) || pendingH5[m.Slug], "catalogue image row %q is not served by the transport", m.Slug)
	}
	got := ImageSlugs()
	for _, s := range got {
		require.True(t, offered[s], "served slug %q is missing from the catalogue", s)
	}
	require.GreaterOrEqual(t, len(got), 8)
}

// ─── the happy path, per family ──────────────────────────────────────────────────────────────────

// TestImagesGeminiGolden — a Nano Banana picture end to end: the EXACT submit body (prompt, model,
// quality, aspect_ratio, images — keys sorted), the poll until `completed`, the download from the
// url the status named WITHOUT an Authorization header, and the Result: the slug, one decoded PNG,
// Usage.Cost = the submit's price.
//
// MUTATION (measured red → green): Usage.Cost left 0 (the price not copied) → red; the Bearer added
// to the download request → the CDN row red; `quality` sent as "2k" on standard → the body golden red.
func TestImagesGeminiGolden(t *testing.T) {
	s := newImageStand(t)
	s.submit = geminiSubmit
	s.statuses = []stand{pendingGemini, processingGemini, s.completedGemini()}
	tr := imagesAt(s)

	res, err := tr.Generate(context.Background(), orimages.Request{
		Prompt: "  a coat on a hanger ", N: 1, Quality: "high", AspectRatio: "3:2",
		Background: "opaque", OutputFormat: "png", // no runblob counterpart: not sent
		InputReferences: []string{"https://cdn.grbpwr.com/m/1.png", " data:image/png;base64,AAAA "},
	})
	require.NoError(t, err)
	require.Equal(t, "gemini/standard", res.Model)
	require.Len(t, res.Images, 1)
	require.Equal(t, pngBytes(t), res.Images[0].Bytes)
	require.Equal(t, "image/png", res.Images[0].MediaType)
	require.InDelta(t, 0.021, res.Usage.Cost, 1e-9, "the provider's own price, in USD")
	require.Zero(t, res.Usage.Prompt+res.Usage.Completion, "no tokens on a per-call price")

	paths, methods, bodies, auths := s.recorded()
	require.Equal(t, []string{"/v1/gemini/generate", "/v1/gemini/generations/" + taskUUID,
		"/v1/gemini/generations/" + taskUUID, "/v1/gemini/generations/" + taskUUID, "/cdn/out.png"}, paths)
	require.Equal(t, []string{"POST", "GET", "GET", "GET", "GET"}, methods)
	require.Equal(t, `{"aspect_ratio":"3:2","images":["https://cdn.grbpwr.com/m/1.png","data:image/png;base64,AAAA"],`+
		`"model":"standard","prompt":"a coat on a hanger","quality":"standard"}`, bodies[0],
		"standard takes no 2k: `high` is sent as the family's standard tier")
	require.Equal(t, "Bearer "+testKey, auths[0])
	require.Equal(t, "Bearer "+testKey, auths[1])
	require.Empty(t, auths[4], "NO KEY ON THE DOWNLOAD: the url is the provider's, the host is whatever it names")
}

// TestImagesKlingGolden — a Kling O1 photo: the o1-photo path, `images_url` (not `images`),
// `img_resolution` from the quality dial, generation_id on the submit, image_url on the read.
//
// MUTATION (measured red → green): the Kling body built with the gemini keys (`images`, `quality`) →
// the golden red; the path built from the slug's first segment ("/v1/kling/generate") → red.
func TestImagesKlingGolden(t *testing.T) {
	s := newImageStand(t)
	s.submit = klingSubmit
	s.statuses = []stand{s.completedKling()}
	tr := imagesAt(s)

	res, err := tr.Generate(context.Background(), orimages.Request{
		Model: SlugKlingO1Photo, Prompt: "p", Quality: "high", AspectRatio: "16:9",
		InputReferences: []string{"https://cdn.grbpwr.com/m/1.png"},
	})
	require.NoError(t, err)
	require.Equal(t, "kling/o1-photo", res.Model)
	require.Len(t, res.Images, 1)
	require.InDelta(t, 0.029, res.Usage.Cost, 1e-9)

	paths, _, bodies, _ := s.recorded()
	require.Equal(t, []string{"/v1/kling/o1-photo/generate", "/v1/kling/o1-photo/generations/" + genID, "/cdn/out.png"}, paths)
	require.Equal(t, `{"aspect_ratio":"16:9","images_url":["https://cdn.grbpwr.com/m/1.png"],"img_resolution":"2k","prompt":"p"}`, bodies[0])
}

// TestImagesBodyPerSlug — the quality and aspect mapping across the eight slugs: `high` is 2k only
// where the family takes it; a ratio outside the family's enum is NOT sent (the default applies,
// never a 4xx); `auto` reaches O3 and not O1; the default slug is Nano Banana standard.
//
// MUTATION (measured red → green): the aspect check dropped (every ratio sent) → the "5:4 on kling"
// row red; twoK ignored (2k on every gemini model) → the v2_lite row red.
func TestImagesBodyPerSlug(t *testing.T) {
	cases := []struct {
		name, slug, quality, aspect string
		want                        string
	}{
		{"default slug = gemini/standard, medium", "", "medium", "1:1",
			`{"aspect_ratio":"1:1","model":"standard","prompt":"p","quality":"standard"}`},
		{"pro takes 2k on high", SlugGeminiPro, "high", "",
			`{"model":"pro","prompt":"p","quality":"2k"}`},
		{"v2 takes 2k on high", SlugGeminiV2, "HIGH", "auto",
			`{"aspect_ratio":"auto","model":"v2","prompt":"p","quality":"2k"}`},
		{"v2_lite has no 2k", SlugGeminiV2Lite, "high", "5:4",
			`{"aspect_ratio":"5:4","model":"v2_lite","prompt":"p","quality":"standard"}`},
		{"pro_vip takes 2k; a ratio no family knows is dropped", SlugGeminiProVIP, "high", "7:5",
			`{"model":"pro_vip","prompt":"p","quality":"2k"}`},
		{"v2_vip, low", SlugGeminiV2VIP, "low", "9:16",
			`{"aspect_ratio":"9:16","model":"v2_vip","prompt":"p","quality":"standard"}`},
		{"o1-photo: 5:4 is gemini's, not kling's; medium = 1k", SlugKlingO1Photo, "medium", "5:4",
			`{"img_resolution":"1k","prompt":"p"}`},
		{"o1-photo: auto is O3's only", SlugKlingO1Photo, "", "auto",
			`{"img_resolution":"1k","prompt":"p"}`},
		{"o3-photo: auto and 2k", SlugKlingO3Photo, "high", "auto",
			`{"aspect_ratio":"auto","img_resolution":"2k","prompt":"p"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newImageStand(t)
			if strings.HasPrefix(tc.slug, "kling") {
				s.submit, s.statuses = klingSubmit, []stand{s.completedKling()}
			} else {
				s.submit, s.statuses = geminiSubmit, []stand{s.completedGemini()}
			}
			_, err := imagesAt(s).Generate(context.Background(), orimages.Request{Model: tc.slug, Prompt: "p", Quality: tc.quality, AspectRatio: tc.aspect})
			require.NoError(t, err)
			_, _, bodies, _ := s.recorded()
			require.Equal(t, tc.want, bodies[0])
		})
	}
}

// TestImagesOnePicturePerTask — n > 1 is not a runblob field: one picture comes back, the body carries
// no `n`, and nothing fails. Also: an unpriced submit ("calculating") and an unreadable price both
// book Usage.Cost 0 — unknown is never a number — and the unreadable one is still polled to its
// picture (the job is bought; dropping it would pay for nothing).
//
// MUTATION (measured red → green): Generate returning the submit's error when sub.ID is set (the
// «polled anyway» branch removed) → the "abc" row red.
func TestImagesOnePicturePerTask(t *testing.T) {
	for _, price := range []string{`"0.0210"`, `"calculating"`, `"abc"`} {
		t.Run("price "+price, func(t *testing.T) {
			s := newImageStand(t)
			s.submit = `{"task_uuid":"` + taskUUID + `","status":"pending","price":` + price + `}`
			s.statuses = []stand{s.completedGemini()}
			res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p", N: 3})
			require.NoError(t, err)
			require.Len(t, res.Images, 1)
			_, _, bodies, _ := s.recorded()
			require.NotContains(t, bodies[0], `"n"`)
			if price == `"0.0210"` {
				require.InDelta(t, 0.021, res.Usage.Cost, 1e-9)
			} else {
				require.Zero(t, res.Usage.Cost, "unknown is 0 = unpriced, never a guess")
			}
		})
	}
}

// ─── refusals before the wire ────────────────────────────────────────────────────────────────────

// TestImagesRefusalsBeforeTheWire — a data: reference on Kling, a reference that is not a fetchable
// url, more references than the family takes, an unknown slug, an empty prompt, no key: each is OUR
// mistake, refused before the stand sees anything, never engaged, never weather.
//
// MUTATION (measured red → green): the data: check on Kling removed → the row reaches the stand → red
// (the stand's count).
func TestImagesRefusalsBeforeTheWire(t *testing.T) {
	s := newImageStand(t)
	s.submit, s.statuses = geminiSubmit, []stand{s.completedGemini()}
	tr := imagesAt(s)
	five := []string{"https://a/1", "https://a/2", "https://a/3", "https://a/4", "https://a/5"}
	cases := []struct {
		name string
		req  orimages.Request
		code string
		want string
	}{
		{"a data: url on kling", orimages.Request{Model: SlugKlingO1Photo, Prompt: "p", InputReferences: []string{"data:image/png;base64,AAAA"}},
			aiprov.CodeBadRequest, "runblob: kling/o1-photo takes reference pictures by http(s) url only, and one was given as a data: url"},
		{"a reference that is not a url", orimages.Request{Prompt: "p", InputReferences: []string{"ftp://x/1.png"}},
			aiprov.CodeBadRequest, "runblob: a reference picture must be an http(s) url the provider can fetch"},
		{"a reference with no host", orimages.Request{Prompt: "p", InputReferences: []string{"https:///1.png"}},
			aiprov.CodeBadRequest, "runblob: a reference picture must be an http(s) url"},
		{"five references on standard (max 4)", orimages.Request{Prompt: "p", InputReferences: five},
			aiprov.CodeBadRequest, "runblob: 5 reference pictures in one call, and gemini/standard takes at most 4"},
		{"an unknown slug", orimages.Request{Model: "openai/gpt-image-2", Prompt: "p"},
			aiprov.CodeBadRequest, `runblob: "openai/gpt-image-2" is not an image model this transport draws`},
		{"an empty prompt", orimages.Request{Prompt: "   "},
			aiprov.CodeBadRequest, "runblob: a gemini/standard generation needs a prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tr.Generate(context.Background(), tc.req)
			require.Nil(t, res)
			ce := imageCallErr(t, err)
			require.Equal(t, tc.code, ce.Code)
			require.False(t, ce.Engaged)
			require.False(t, ce.Retryable)
			require.True(t, strings.HasPrefix(err.Error(), tc.want), err.Error())
		})
	}
	t.Run("five references on pro is fine (max 10)", func(t *testing.T) {
		_, err := tr.Generate(context.Background(), orimages.Request{Model: SlugGeminiPro, Prompt: "p", InputReferences: five})
		require.NoError(t, err)
	})
	t.Run("no key", func(t *testing.T) {
		off := NewImages(New(Config{KeyFunc: key("")}))
		off.c.base = s.srv.URL
		_, err := off.Generate(context.Background(), orimages.Request{Prompt: "p"})
		ce := imageCallErr(t, err)
		require.Equal(t, aiprov.CodeNotConfigured, ce.Code)
		require.ErrorIs(t, err, aiprov.ErrNotConfigured)
		require.False(t, ce.Engaged)
	})
	paths, _, _, _ := s.recorded()
	require.Equal(t, []string{"/v1/gemini/generate", "/v1/gemini/generations/" + taskUUID, "/cdn/out.png"}, paths,
		"only the one legal call (five refs on pro) reached the stand")
}

// ─── refusals at the gate: free, the chain may advance ───────────────────────────────────────────

// TestImagesGateRefusalsAreFree — a 402 on the submit is out_of_credits, NOT engaged (nothing bought:
// the route advances to the next candidate on a fresh attempt); a 401 / 403 is key_rejected; a 422 is
// bad_request; a 404 is model_unknown (the path); none polls, none downloads.
//
// MUTATION (measured red → green): Generate marking every submit error engaged → the 402 row red.
func TestImagesGateRefusalsAreFree(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   string
	}{
		{http.StatusPaymentRequired, `{"detail":"INSUFFICIENT_CREDITS"}`, aiprov.CodeOutOfCredits},
		{http.StatusUnauthorized, `{"detail":"Invalid or expired API key"}`, aiprov.CodeKeyRejected},
		{http.StatusForbidden, `{"detail":"forbidden"}`, aiprov.CodeKeyRejected},
		{http.StatusUnprocessableEntity, `{"detail":[{"loc":["body","prompt"],"msg":"too long"}]}`, aiprov.CodeBadRequest},
		{http.StatusNotFound, `{"detail":"Not Found"}`, aiprov.CodeModelUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			s := newImageStand(t)
			s.submitStatus, s.submit = tc.status, tc.body
			res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
			require.Nil(t, res)
			ce := imageCallErr(t, err)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, tc.status, ce.HTTPStatus)
			require.False(t, ce.Engaged, "a refusal at the gate bought nothing: the route may try the next candidate")
			require.False(t, ce.Retryable, "the matrix's word: re-sending the same request cannot end differently")
			paths, _, _, _ := s.recorded()
			require.Equal(t, []string{"/v1/gemini/generate"}, paths, "no poll, no download")
		})
	}
	t.Run("the 402 sentence carries the provider's word and never the key", func(t *testing.T) {
		s := newImageStand(t)
		s.submitStatus, s.submit = http.StatusPaymentRequired, `{"detail":"INSUFFICIENT_CREDITS for `+testKey+`"}`
		_, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
		require.Equal(t, "runblob: API error (HTTP 402): INSUFFICIENT_CREDITS for [key]", err.Error())
	})
}

// ─── the provider's own `failed`: refunded, free, terminal ───────────────────────────────────────

// TestImagesFailedIsRefundedAndFree — the status says `failed`: runblob refunds the task, so the
// CallError is NOT engaged (the ledger row reads free, no price booked) and NOT retryable (terminal
// for this candidate); the sentence names the id and the provider's code; no download is tried.
//
// MUTATION (measured red → green): the failed branch marked Engaged → red (the run would close
// `unknown` and the chain would stop on a refunded job).
func TestImagesFailedIsRefundedAndFree(t *testing.T) {
	s := newImageStand(t)
	s.submit = geminiSubmit
	s.statuses = []stand{processingGemini, failedGemini}
	res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.Nil(t, res, "no price rides with a refunded failure: Usage.Cost is not 0.021 anywhere")
	ce := imageCallErr(t, err)
	require.Equal(t, aiprov.CodeProviderError, ce.Code)
	require.False(t, ce.Engaged, "refunded by runblob's own rule")
	require.False(t, ce.Retryable, "terminal for this candidate")
	require.False(t, aiprov.Engaged(err))
	require.Equal(t, "runblob: generation "+taskUUID+" (gemini) failed at the provider and was refunded: CONTENT_POLICY_VIOLATION", err.Error())
	paths, _, _, _ := s.recorded()
	require.Equal(t, []string{"/v1/gemini/generate", "/v1/gemini/generations/" + taskUUID, "/v1/gemini/generations/" + taskUUID}, paths)

	t.Run("failed with no reason", func(t *testing.T) {
		s := newImageStand(t)
		s.submit = klingSubmit
		s.statuses = []stand{{http.StatusOK, `{"generation_id":"` + genID + `","status":"failed","image_url":null}`}}
		_, err := imagesAt(s).Generate(context.Background(), orimages.Request{Model: SlugKlingO3Photo, Prompt: "p"})
		require.Equal(t, "runblob: generation "+genID+" (kling/o3-photo) failed at the provider and was refunded: no reason given", err.Error())
	})
}

// ─── after the 201 everything is bought ──────────────────────────────────────────────────────────

// TestImagesPollCeilingIsBought — the job never finishes inside the family's ceiling: the CallError
// is ENGAGED and NOT retryable, code timeout, the id in the sentence — the run closes `unknown` and
// nobody buys the picture again (D-16).
//
// MUTATION (measured red → green): the ceiling failure built with Engaged:false → red (the chain
// would advance and pay a second provider for a picture runblob may still deliver).
func TestImagesPollCeilingIsBought(t *testing.T) {
	s := newImageStand(t)
	s.submit = geminiSubmit
	s.statuses = []stand{processingGemini}
	tr := imagesAt(s)
	tr.ceilingOf = func(imageFamily) time.Duration { return 30 * time.Millisecond }
	tr.poll = 5 * time.Millisecond

	res, err := tr.Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.Nil(t, res)
	ce := imageCallErr(t, err)
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
	require.True(t, ce.Engaged)
	require.True(t, aiprov.Engaged(err))
	require.False(t, ce.Retryable)
	require.Equal(t, "runblob: generation "+taskUUID+" (gemini) is bought and it was not finished within 30ms — the run must not be re-submitted", err.Error())
	paths, _, _, _ := s.recorded()
	require.GreaterOrEqual(t, len(paths), 3, "several polls before the ceiling")
	require.NotContains(t, paths, "/cdn/out.png")

	t.Run("the caller's deadline mid-poll is bought too", func(t *testing.T) {
		s := newImageStand(t)
		s.submit = geminiSubmit
		s.statuses = []stand{processingGemini}
		tr := imagesAt(s)
		tr.poll = 5 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		_, err := tr.Generate(ctx, orimages.Request{Prompt: "p"})
		ce := imageCallErr(t, err)
		require.True(t, ce.Engaged)
		require.False(t, ce.Retryable)
		require.Equal(t, aiprov.CodeTimeout, ce.Code)
		require.Contains(t, err.Error(), "generation "+taskUUID+" (gemini) is bought")
	})

	t.Run("an unknown status word keeps waiting, never guesses failed", func(t *testing.T) {
		s := newImageStand(t)
		s.submit = geminiSubmit
		s.statuses = []stand{{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"queued"}`}, s.completedGemini()}
		res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
		require.NoError(t, err)
		require.Len(t, res.Images, 1)
	})
}

// TestImagesStatusReadFailures — a status read the adapter calls retryable (a 503, a garbled envelope)
// is looked at again and the picture still arrives; a read it calls final (401 mid-job, a 404 on the
// id) ends the wait as BOUGHT AND UNKNOWN with that code.
//
// MUTATION (measured red → green): every status error treated as final → the 503 row red; every
// status error treated as retryable → the 404 row loops to the ceiling (timeout, not not_found) → red.
func TestImagesStatusReadFailures(t *testing.T) {
	t.Run("a 503 then a garbled body then completed", func(t *testing.T) {
		s := newImageStand(t)
		s.submit = geminiSubmit
		s.statuses = []stand{{http.StatusServiceUnavailable, `{"detail":"busy"}`}, {http.StatusOK, `{"task_uuid":`}, s.completedGemini()}
		res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
		require.NoError(t, err)
		require.Len(t, res.Images, 1)
		paths, _, _, _ := s.recorded()
		require.Len(t, paths, 5, "submit, three reads, download")
	})
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"the key rejected mid-job", http.StatusUnauthorized, `{"detail":"Invalid or expired API key"}`, aiprov.CodeKeyRejected},
		{"the id unknown to this key", http.StatusNotFound, `{"detail":"Generation not found"}`, aiprov.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newImageStand(t)
			s.submit = geminiSubmit
			s.statuses = []stand{{tc.status, tc.body}}
			res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
			require.Nil(t, res)
			ce := imageCallErr(t, err)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, tc.status, ce.HTTPStatus)
			require.True(t, ce.Engaged, "bought at the 201: a read the provider refuses does not un-buy it")
			require.False(t, ce.Retryable)
			require.Contains(t, err.Error(), "generation "+taskUUID+" (gemini) is bought and its status can no longer be read")
			require.NotContains(t, err.Error(), testKey)
		})
	}
}

// TestImagesDownloadFailuresAreBought — the picture url missing, unfetchable, answering a non-200,
// serving something that is not a picture, or over the cap: every one is engaged (the job is done
// and paid) and names the id; a CDN that mislabels a real PNG is still a picture (sniffed).
//
// MUTATION (measured red → green): the media-type check dropped → the text/html row files an HTML
// page as a picture → red; MaxImageBytes read as `limit` not `limit+1` in readCapped → the exact-cap
// row red.
func TestImagesDownloadFailuresAreBought(t *testing.T) {
	cases := []struct {
		name      string
		cdnStatus int
		cdnType   string
		cdnBody   []byte
		noURL     bool
		code      string
		want      string
	}{
		{name: "completed with no url", noURL: true, code: aiprov.CodeEmptyAnswer, want: "it completed with no picture url"},
		{name: "the CDN answers 403", cdnStatus: http.StatusForbidden, cdnType: "text/plain", cdnBody: []byte("expired"),
			code: aiprov.CodeProviderError, want: "its picture url answered HTTP 403: expired"},
		{name: "the CDN serves an html page as 200", cdnStatus: http.StatusOK, cdnType: "text/html", cdnBody: []byte("<html><body>gone</body></html>"),
			code: aiprov.CodeProviderError, want: `its picture url served "text/html; charset=utf-8", not a picture`},
		{name: "the CDN serves more than the cap", cdnStatus: http.StatusOK, cdnType: "image/png", cdnBody: make([]byte, MaxImageBytes+1),
			code: aiprov.CodeTooLarge, want: "its picture could not be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newImageStand(t)
			s.submit = geminiSubmit
			if tc.noURL {
				s.statuses = []stand{{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"completed","result_image_url":null}`}}
			} else {
				s.statuses = []stand{s.completedGemini()}
				s.cdnStatus, s.cdnType, s.cdnBody = tc.cdnStatus, tc.cdnType, tc.cdnBody
			}
			res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
			require.Nil(t, res)
			ce := imageCallErr(t, err)
			require.Equal(t, tc.code, ce.Code)
			require.True(t, ce.Engaged)
			require.False(t, ce.Retryable)
			require.Contains(t, err.Error(), "generation "+taskUUID+" (gemini) is bought and "+tc.want)
		})
	}
	t.Run("a mislabelled PNG is sniffed and kept; exactly the cap is fine", func(t *testing.T) {
		s := newImageStand(t)
		s.submit = geminiSubmit
		s.statuses = []stand{s.completedGemini()}
		s.cdnType = "application/octet-stream"
		res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
		require.NoError(t, err)
		require.Equal(t, "image/png", res.Images[0].MediaType)

		s2 := newImageStand(t)
		s2.submit = geminiSubmit
		s2.statuses = []stand{s2.completedGemini()}
		s2.cdnBody = append(pngBytes(t), make([]byte, MaxImageBytes-len(pngBytes(t)))...)
		res, err = imagesAt(s2).Generate(context.Background(), orimages.Request{Prompt: "p"})
		require.NoError(t, err)
		require.Len(t, res.Images[0].Bytes, MaxImageBytes)
	})
	t.Run("a picture url with a scheme nobody fetches", func(t *testing.T) {
		s := newImageStand(t)
		s.submit = geminiSubmit
		s.statuses = []stand{{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"completed","result_image_url":"file:///etc/passwd"}`}}
		_, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
		ce := imageCallErr(t, err)
		require.True(t, ce.Engaged)
		require.Contains(t, err.Error(), `its picture url is not fetchable: "file:///etc/passwd"`)
		paths, _, _, _ := s.recorded()
		require.Len(t, paths, 2, "nothing was fetched")
	})
}

// TestImagesSubmitWithoutIdIsUnknown — a 201 with neither id field: engaged, empty_answer, no poll (there
// is nothing to poll) — the transport hands the adapter's word straight up.
//
// MUTATION (measured red → green): Generate polling with an empty id → the adapter refuses the id
// before the wire (bad_request, not engaged) → red.
func TestImagesSubmitWithoutIdIsUnknown(t *testing.T) {
	s := newImageStand(t)
	s.submit = `{"status":"pending","price":"0.0210"}`
	res, err := imagesAt(s).Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.Nil(t, res)
	ce := imageCallErr(t, err)
	require.Equal(t, aiprov.CodeEmptyAnswer, ce.Code)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	paths, _, _, _ := s.recorded()
	require.Equal(t, []string{"/v1/gemini/generate"}, paths)
}
