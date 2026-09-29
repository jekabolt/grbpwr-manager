package designgen

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/runblob"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ H5 — every video family runblob has: the slug decides the path, the body and the locator ═══
//
// FIXTURES: the field names of runblob-specs/kling.json endpoints[0..2] and runblob-specs/seedance.json
// (+ the Seedance docs page, read 2026-09-29). UNVERIFIED (G-06): every VALUE (prices, cdn paths).

const videoSource = "https://cdn.example/m/41.png"

// TestVideoFamilyOf — the slug → family table, and the refusal of everything else.
//
// MUTATION (measured red → green): the kling_o* cases checked AFTER the kling_ prefix → kling_o1 lands
// on the main endpoint, red; the prefix loosened to `kling_` → kling_o5 accepted, red.
func TestVideoFamilyOf(t *testing.T) {
	for model, want := range map[string]videoFamily{
		"kling_2.5_turbo": videoFamKling, "kling_3_pro": videoFamKling, "kling_1.6": videoFamKling,
		"kling_o1": videoFamKlingO1, "kling_o3": videoFamKlingO3, "kling_o3_pro": videoFamKlingO3,
		"seedance-2.0-mini": videoFamSeedance, "doubao-seedance-2.0-face": videoFamSeedance,
		"doubao-seedance-2.0-fast-face": videoFamSeedance, "doubao-seedance-2.5-face": videoFamSeedance,
	} {
		got, ok := videoFamilyOf(model)
		require.True(t, ok, model)
		require.Equal(t, want, got, model)
		require.True(t, IsVideoModel(model), model)
	}
	for _, model := range []string{"", "veo_3", "kling_o5", "kling_", "kling", "seedance-3.0", "doubao-seedance-9-face",
		"gemini/standard", "Kling_2.5_turbo"} {
		_, ok := videoFamilyOf(model)
		require.False(t, ok, "%q must be refused", model)
		require.False(t, IsVideoModel(model), model)
	}
}

// TestVideoBodyPerFamily — the body each family documents, the path the submit goes to, the locator's
// tag, and the status read of the collect going back to the same family's path.
//
// MUTATION (measured red → green): O3's images_url spelled as O1's images_urls → the kling_o3_pro row
// red; Collect reading the main family's path whatever the locator says → the statusFamilies red.
func TestVideoBodyPerFamily(t *testing.T) {
	cases := []struct {
		model, path, tag string
		want             map[string]any
	}{
		{"kling_2.5_turbo", "kling", "kling", map[string]any{"prompt": "sway", "model": "kling_2.5_turbo",
			"image_url": videoSource, "duration": "5", "aspect_ratio": "9:16"}},
		{"kling_3_pro", "kling", "kling", map[string]any{"prompt": "sway", "model": "kling_3_pro",
			"image_url": videoSource, "duration": 5, "aspect_ratio": "9:16"}},
		{"kling_o1", "kling/o1-video", "kling-o1", map[string]any{"prompt": "sway",
			"images_urls": []string{videoSource}, "duration": "5", "aspect_ratio": "9:16"}},
		{"kling_o3_pro", "kling/o3-video", "kling-o3", map[string]any{"prompt": "sway", "model": "kling_o3_pro",
			"images_url": []string{videoSource}, "duration": "5", "aspect_ratio": "9:16"}},
		{"seedance-2.0-mini", "seedance", "seedance", map[string]any{"prompt": "sway", "model": "seedance-2.0-mini",
			"resolution": "720p", "duration": 5, "first_frame_url": videoSource, "aspect_ratio": "9:16"}},
		{"doubao-seedance-2.0-face", "seedance", "seedance", map[string]any{"prompt": "sway", "model": "doubao-seedance-2.0-face",
			"resolution": "720p", "duration": 5, "aspect_ratio": "9:16",
			"image_with_roles": []map[string]string{{"url": videoSource, "role": "first_frame"}}}},
		{"doubao-seedance-2.5-face", "seedance", "seedance", map[string]any{"prompt": "sway", "model": "doubao-seedance-2.5-face",
			"resolution": "720p", "duration": 5, "aspect_ratio": "adaptive",
			"image_with_roles": []map[string]string{{"url": videoSource, "role": "first_frame"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			tr := &fakeVideoTransport{enabled: true}
			route := videoRouteWith(tr, nil, nil)
			job := Job{RunID: 9, Kind: entity.DesignRunKindVideo, Prompt: "sway", References: []string{videoSource},
				VideoModel: tc.model, VideoAspectRatio: "9:16"}
			out, err := route.Execute(context.Background(), job)
			require.NoError(t, err)
			require.Equal(t, []string{tc.path}, tr.families)
			require.Equal(t, tc.want, tr.submits[0])
			require.Equal(t, tc.tag+"#"+videoGenID+"#0.29", out.RequestID)
			require.LessOrEqual(t, len(out.RequestID), providerRequestIDMax)

			_, err = route.Collect(context.Background(), job, out.RequestID)
			require.NoError(t, err)
			require.Equal(t, []string{tc.path}, tr.statusFamilies, "the status read follows the family")
		})
	}
}

// TestVideoUnknownSlugIsRefusedFree — a slug runblob does not serve as video (a stale route row, a
// hand-written param) is refused before the wire and before the ledger row: no submit, no row,
// provider_bad_request, terminal, and the sentence lists what is known.
func TestVideoUnknownSlugIsRefusedFree(t *testing.T) {
	for _, model := range []string{"veo_3", "kling_o5", "seedance-3.0"} {
		t.Run(model, func(t *testing.T) {
			tr := &fakeVideoTransport{enabled: true}
			job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindVideo, Prompt: "sway",
				References: []string{videoSource}, VideoModel: model}, entity.AIPurposeVideoGenerate)
			out, err := videoRouteWith(tr, nil, nil).Execute(context.Background(), job)
			require.Nil(t, out)
			require.Error(t, err)
			v := classify(err)
			require.Equal(t, CodeBadRequest, v.Code)
			require.False(t, v.Retryable)
			require.Contains(t, err.Error(), "is not a runblob video model")
			require.Contains(t, err.Error(), "kling_o3_pro")
			require.Contains(t, err.Error(), "doubao-seedance-2.5-face")
			require.Contains(t, err.Error(), "nothing was charged")
			require.Empty(t, tr.submits, "nothing reached the provider")
			require.Empty(t, ai.Rows(), "no ledger row: nothing was bought")
		})
	}
	t.Run("seedance mini/2.0 take at least 3 characters; 2.5 has no minimum", func(t *testing.T) {
		tr := &fakeVideoTransport{enabled: true}
		route := videoRouteWith(tr, nil, nil)
		_, err := route.Execute(context.Background(), Job{Prompt: "go", References: []string{videoSource}, VideoModel: VideoModelSeedanceMini})
		require.Equal(t, CodeBadRequest, classify(err).Code)
		require.Empty(t, tr.submits)
		_, err = route.Execute(context.Background(), Job{Prompt: "go", References: []string{videoSource}, VideoModel: VideoModelSeedance25Face})
		require.NoError(t, err)
		require.Len(t, tr.submits, 1)
	})
}

// TestVideoLocatorFamilies — the locator round-trips every family, reads the pre-H5 `kling#…` form
// as it was written, and refuses a family tag nobody knows (its path cannot be guessed) — the collect
// then reads no status at all.
func TestVideoLocatorFamilies(t *testing.T) {
	price := decimal.NewNullDecimal(decimal.RequireFromString("1.8"))
	for _, fam := range videoFamilies {
		loc := videoLocator(fam, videoGenID, price)
		require.Equal(t, fam.tag+"#"+videoGenID+"#1.8", loc)
		got, id, p, ok := splitVideoLocator(loc)
		require.True(t, ok, loc)
		require.Equal(t, fam, got)
		require.Equal(t, videoGenID, id)
		require.True(t, p.Valid && p.Decimal.Equal(price.Decimal))

		unpriced := videoLocator(fam, videoGenID, decimal.NullDecimal{})
		require.Equal(t, fam.tag+"#"+videoGenID, unpriced)
		got, _, p, ok = splitVideoLocator(unpriced)
		require.True(t, ok)
		require.Equal(t, fam, got)
		require.False(t, p.Valid)
	}
	// Rows stored before H5: `kling#<id>#<price>` is the main Kling family, price and all.
	got, id, p, ok := splitVideoLocator("kling#" + videoGenID + "#0.2900")
	require.True(t, ok)
	require.Equal(t, videoFamKling, got)
	require.Equal(t, videoGenID, id)
	require.Equal(t, "0.29", p.Decimal.String())

	_, _, _, ok = splitVideoLocator("veo#" + videoGenID + "#0.5")
	require.False(t, ok, "an unknown family is never guessed")

	tr := &fakeVideoTransport{enabled: true}
	_, err := videoRouteWith(tr, nil, nil).Collect(context.Background(), Job{RunID: 70}, "veo#"+videoGenID)
	require.ErrorIs(t, err, errVideoNoResult)
	require.Empty(t, tr.statusOf, "no status read on a path nobody knows")
}

// ─── the wire, through the REAL adapter (runblob.Config.Transport → a stand) ────────────────────────

// videoStand — a fake runblob for clips: records every request, answers the submit and the status.
type videoStand struct {
	srv    *httptest.Server
	submit string
	status string

	mu     sync.Mutex
	paths  []string
	bodies []string
}

func newVideoStand(t *testing.T, submit, status string) *videoStand {
	t.Helper()
	s := &videoStand{submit: submit, status: status}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.bodies = append(s.bodies, string(b))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/generate"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, s.submit)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/generations/"):
			_, _ = io.WriteString(w, s.status)
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// videoOverWire — the real video route over the real adapter, pointed at the stand; clips stubbed.
func videoOverWire(t *testing.T, s *videoStand) *videoProvider {
	t.Helper()
	u, err := url.Parse(s.srv.URL)
	require.NoError(t, err)
	c := runblob.New(runblob.Config{KeyFunc: func() string { return "test-key" }, HTTPTimeout: 2 * time.Second,
		Transport: toStand{stand: u}})
	p := NewVideoProvider(c, nil).(*videoProvider)
	p.fetch = func(context.Context, string) ([]byte, error) { return mp4Bytes, nil }
	return p
}

// TestVideoWirePerFamily — the submit path and body on the wire, the id field each family answers
// with (generation_id for Kling, task_uuid for Seedance), the status path, and the result field
// (video_url, Seedance's output.video_urls[0]). Seedance's 201 states NO price: the ledger row is
// booked with no number and the locator carries none; the collect delivers unpriced.
//
// MUTATION (measured red → green): the adapter reading video_url alone (output.video_urls dropped) →
// the seedance collect answers errVideoNoResult, red.
func TestVideoWirePerFamily(t *testing.T) {
	const taskID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	kling201 := `{"generation_id":"` + videoGenID + `","status":"pending","price":"0.2900","description":"queued"}`
	klingDone := `{"generation_id":"` + videoGenID + `","status":"completed","video_url":"https://cdn.runblob.io/v/out.mp4","model":"%s"}`
	cases := []struct {
		model, submit, status, wantBody, wantLoc string
		wantPaths                                []string
		priced                                   bool
	}{
		{"kling_2.5_turbo", kling201, strings.Replace(klingDone, "%s", "kling_2.5_turbo", 1),
			`{"aspect_ratio":"16:9","duration":"5","image_url":"` + videoSource + `","model":"kling_2.5_turbo","prompt":"sway"}`,
			"kling#" + videoGenID + "#0.29",
			[]string{"POST /v1/kling/generate", "GET /v1/kling/generations/" + videoGenID}, true},
		{"kling_o1", strings.Replace(kling201, "0.2900", "0.9000", 1), strings.Replace(klingDone, "%s", "kling_o1", 1),
			`{"aspect_ratio":"16:9","duration":"5","images_urls":["` + videoSource + `"],"prompt":"sway"}`,
			"kling-o1#" + videoGenID + "#0.9",
			[]string{"POST /v1/kling/o1-video/generate", "GET /v1/kling/o1-video/generations/" + videoGenID}, true},
		{"kling_o3_pro", strings.Replace(kling201, "0.2900", "0.9000", 1), strings.Replace(klingDone, "%s", "kling_o3_pro", 1),
			`{"aspect_ratio":"16:9","duration":"5","images_url":["` + videoSource + `"],"model":"kling_o3_pro","prompt":"sway"}`,
			"kling-o3#" + videoGenID + "#0.9",
			[]string{"POST /v1/kling/o3-video/generate", "GET /v1/kling/o3-video/generations/" + videoGenID}, true},
		{"seedance-2.0-mini",
			`{"task_uuid":"` + taskID + `","model":"seedance-2.0-mini","status":"pending","output":null,"error":null}`,
			`{"task_uuid":"` + taskID + `","model":"seedance-2.0-mini","status":"completed","output":{"video_urls":["https://media.runblob.io/s/0.mp4"],"last_frame_url":null},"error":null}`,
			`{"aspect_ratio":"16:9","duration":5,"first_frame_url":"` + videoSource + `","model":"seedance-2.0-mini","prompt":"sway","resolution":"720p"}`,
			"seedance#" + taskID,
			[]string{"POST /v1/seedance/generate", "GET /v1/seedance/generations/" + taskID}, false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			s := newVideoStand(t, tc.submit, tc.status)
			route := videoOverWire(t, s)
			job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindVideo, Prompt: "sway",
				References: []string{videoSource}, VideoModel: tc.model, VideoAspectRatio: "16:9"}, entity.AIPurposeVideoGenerate)

			out, err := route.Execute(context.Background(), job)
			require.NoError(t, err)
			require.Equal(t, tc.wantLoc, out.RequestID)
			require.Equal(t, tc.wantBody, s.bodies[0], "keys sorted, spelled as the family documents")

			row := ai.Rows()[0]
			require.Equal(t, entity.AICallAccepted, row.Status)
			require.Equal(t, tc.priced, row.End.CostUSD.Valid, "priced at submit only when runblob stated a price")

			got, err := route.Collect(context.Background(), job, out.RequestID)
			require.NoError(t, err)
			require.Len(t, got.Artifacts, 1)
			require.Equal(t, tc.priced, got.Price.Valid)
			require.Equal(t, tc.model, got.Model)
			s.mu.Lock()
			require.Equal(t, tc.wantPaths, s.paths)
			s.mu.Unlock()
			require.Equal(t, entity.AICallOK, ai.Rows()[0].Status)
		})
	}

	t.Run("seedance failed: {code, message} reaches the sentence, refunded and free", func(t *testing.T) {
		s := newVideoStand(t,
			`{"task_uuid":"`+taskID+`","model":"seedance-2.0-mini","status":"pending","output":null,"error":null}`,
			`{"task_uuid":"`+taskID+`","model":"seedance-2.0-mini","status":"failed","output":null,"error":{"code":"GENERATION_FAILED","message":"upstream said no"}}`)
		route := videoOverWire(t, s)
		out, err := route.Collect(context.Background(), Job{RunID: 70}, "seedance#"+taskID)
		require.Nil(t, out)
		require.ErrorIs(t, err, errVideoFailed)
		require.Contains(t, err.Error(), "upstream said no")
		require.Equal(t, []string{"GET /v1/seedance/generations/" + taskID}, s.paths)
	})
}
