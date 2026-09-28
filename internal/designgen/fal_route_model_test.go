package designgen

import (
	"context"
	"image/color"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
)

// ═══ B-24 — THE fal ROWS' MODEL IS REAL ═══

// TestTheExtendRowsModelREACHES_THE_FAL_REQUEST — golden: image.extend is routed to fal at
// `fal-ai/bria/expand` in the panel while FAL_MODEL_OUTPAINT is unset (the flux default). The worker
// sends the run to the ROW's slug — its queue path and bria's body, byte for byte the fields bria reads
// — and the locator names that slug.
//
// MUTATION (measured red→green): the dispatch hook removed (job.Model not set from FalRouteModel) → the
// submit goes to /fal-ai/flux-2-pro/outpaint with the flux body.
func TestTheExtendRowsModelREACHES_THE_FAL_REQUEST(t *testing.T) {
	rg := newThreedRouteRig(t)
	rg.store.routeTo(entity.AIPurposeImageExtend, row(1, entity.AIProviderFal, "fal-ai/bria/expand"))
	rg.reload(t)

	srcBytes, _ := translucentSource(t)
	stand := &outpaintStand{canvas: solidPNG(t, 533, 300, color.NRGBA{R: 1, G: 2, B: 3, A: 255})}
	srv := httptest.NewServer(stand.handler(t))
	defer srv.Close()
	c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second})
	st := &fakeStore{}
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG, ContentTypeJPEG), Providers{Outpaint: NewFalOutpaintProvider(c)})
	w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": srcBytes}}
	w.c.FalRouteModel = func(kind string) string { return FalRouteModel(rg.reg, kind) }

	_ = w.execute(context.Background(), extendRun(`{"extra_input_media_ids":[11],"extend":{"aspect_ratio":"16:9"}}`), "tok")

	require.NotEmpty(t, stand.paths)
	require.Equal(t, "POST /fal-ai/bria/expand", stand.paths[0], "the row's slug is the queue the run is sent to")
	require.Equal(t, map[string]any{
		"image_url":               "https://cdn.example/m/11.png",
		"canvas_size":             []any{float64(533), float64(300)},
		"original_image_size":     []any{float64(200), float64(300)},
		"original_image_location": []any{float64(166), float64(0)},
	}, stand.submitted, "bria's body, not flux's")
	require.Equal(t, "fal-ai/bria/expand#out-1", st.finished[0].ProviderRequestId, "the locator names the row's slug")
}

// TestAnUnsupportedRowSlugCLOSES_THE_TILE — a row naming a slug the route builds no body for is the
// existing FalRoute.Unsupported path (the band hides the tile, the door refuses kind_not_available), in a
// sentence that sends the owner to the panel; the seeded row (no model) keeps the env slug.
//
// MUTATION (measured red→green): FalRoutesFunc ignoring the row's model (FalRouteModel not consulted) →
// the extend route stays the flux default, supported and bounded.
func TestAnUnsupportedRowSlugCLOSES_THE_TILE(t *testing.T) {
	rg := newThreedRouteRig(t)
	routes := FalRoutesFunc(rg.reg, fal.Config{APIKey: "k"})
	require.Equal(t, fal.DefaultModelOutpaint, routes()[entity.DesignRunKindExtend].Model, "the seeded row: the env slug")
	require.True(t, routes()[entity.DesignRunKindExtend].Bounded)

	rg.store.routeTo(entity.AIPurposeImageExtend, row(1, entity.AIProviderFal, "acme/some-outpainter"))
	rg.reload(t)
	r := routes()[entity.DesignRunKindExtend]
	require.True(t, r.Unsupported)
	require.False(t, r.Bounded, "the band does not list the tile")
	require.Equal(t, "acme/some-outpainter", r.Model)
	require.Contains(t, r.Unbounded, "the image.extend route's model is \"acme/some-outpainter\"")
	require.Contains(t, r.Unbounded, "in admin → AI providers")
	require.True(t, routes()[entity.DesignRunKindInpaint].Bounded, "inpaint's own row is untouched")
}

// TestTheCutoutRowsModelIS_THE_QUEUE — a cutout run carrying its row's slug (job.Model) is submitted to
// that matting queue, and its locator names it so the free collect polls there.
//
// MUTATION (measured red→green): falCutoutProvider.Execute back to p.c.ModelCutout() → the submit goes to
// /fal-ai/birefnet/v2.
func TestTheCutoutRowsModelIS_THE_QUEUE(t *testing.T) {
	stand := newRecStand(t, jsonAnswer(http.StatusOK, `{"request_id":"cut-1"}`))
	c := fal.New(fal.Config{APIKey: "k", BaseURL: stand.srv.URL})
	out, err := NewFalCutoutProvider(c).Execute(context.Background(), Job{Kind: entity.DesignRunKindCutout,
		Model: "fal-ai/birefnet/v2-heavy", References: []string{"https://cdn.example/m/11.png"}})
	require.NoError(t, err)
	require.Equal(t, []string{"POST /fal-ai/birefnet/v2-heavy"}, stand.requests())
	require.Equal(t, "fal-ai/birefnet/v2-heavy#cut-1", out.RequestID)
}

// TestABadRowSlugIS_NOT_A_PATH — Codex REVIEW-F1 #4: a row's model is POSTed as a path under the fal key, so
// only a plain owner/model[/variant] slug ever comes out of FalRouteModel; anything else is the env slug.
//
// MUTATION (measured red→green): falSlugRe not consulted → "../x" comes back as the model.
func TestABadRowSlugIS_NOT_A_PATH(t *testing.T) {
	rg := newThreedRouteRig(t)
	for _, bad := range []string{"../x", "fal-ai/../x", "acme/model?x=1", "acme/model#f", "acme/./model",
		"acme", "Acme/Model", "acme//model", "acme/mo del", "acme/model%2e%2e"} {
		rg.store.routeTo(entity.AIPurposeImageExtend, row(1, entity.AIProviderFal, bad))
		rg.reload(t)
		require.Empty(t, FalRouteModel(rg.reg, entity.DesignRunKindExtend), bad)
	}
	for _, good := range []string{"fal-ai/flux-2-pro/outpaint", "fal-ai/birefnet/v2", "acme/model-v1.5_x"} {
		rg.store.routeTo(entity.AIPurposeImageExtend, row(1, entity.AIProviderFal, "/"+good+"/"))
		rg.reload(t)
		require.Equal(t, good, FalRouteModel(rg.reg, entity.DesignRunKindExtend))
	}
}
