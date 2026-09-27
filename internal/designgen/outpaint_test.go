package designgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// ═══ B-13 — kind=extend: the plan, the bodies, the composite, the money ═══

// TestTheExtendPlanGROWS_THE_SIDE_THE_TARGET_ASKS_FOR — the expansion table. Each row is a
// decision: which axis grows, where the source sits, where the odd pixel goes.
func TestTheExtendPlanGROWS_THE_SIDE_THE_TARGET_ASKS_FOR(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		w, h                             int
		ratio                            string
		cw, ch, left, right, top, bottom int
	}{
		{"2:3 → 21:9 grows the width", 200, 300, "21:9", 700, 300, 250, 250, 0, 0},
		{"9:16 → 9:21 grows the height", 900, 1600, "9:21", 900, 2100, 0, 0, 250, 250},
		{"1:1 → 4:3, the odd pixel goes right", 500, 500, "4:3", 667, 500, 83, 84, 0, 0},
		{"16:9 → 1:1 grows the height", 1600, 900, "1:1", 1600, 1600, 0, 0, 350, 350},
		{"3:4 → 9:16, the odd pixel goes down", 300, 400, "9:16", 300, 533, 0, 0, 66, 67},
	} {
		r, ok := entity.DesignExtendRatioValue(tc.ratio)
		require.True(t, ok, tc.ratio)
		p, err := planExtend(tc.w, tc.h, r)
		require.NoError(t, err, tc.name)
		require.Equal(t, image.Rect(0, 0, tc.cw, tc.ch), p.Canvas, tc.name)
		require.Equal(t, image.Rect(0, 0, tc.w, tc.h), p.Source, "%s: under the cap the source is untouched", tc.name)
		require.Equal(t, 1.0, p.Scale, tc.name)
		require.Equal(t, [4]int{tc.left, tc.right, tc.top, tc.bottom},
			[4]int{p.ExpandLeft, p.ExpandRight, p.ExpandTop, p.ExpandBottom}, tc.name)
		require.Equal(t, image.Pt(tc.left, tc.top), p.Offset, tc.name)
		require.Equal(t, p.Canvas.Dx(), p.ExpandLeft+p.Source.Dx()+p.ExpandRight, tc.name)
		require.Equal(t, p.Canvas.Dy(), p.ExpandTop+p.Source.Dy()+p.ExpandBottom, tc.name)
	}

	// The target is the source's own proportion (within 0.5 %): nothing to add, free and terminal.
	for _, tc := range []struct {
		w, h  int
		ratio string
	}{{300, 400, "3:4"}, {1000, 1000, "1:1"}, {1920, 1081, "16:9"}} {
		r, _ := entity.DesignExtendRatioValue(tc.ratio)
		_, err := planExtend(tc.w, tc.h, r)
		require.ErrorIs(t, err, errExtendNothingToAdd, "%dx%d → %s", tc.w, tc.h, tc.ratio)
		nothing, ok := ExtendTargetAddsNothing(tc.w, tc.h, tc.ratio)
		require.True(t, ok)
		require.True(t, nothing, "the door asks the same function")
		require.Equal(t, CodeTargetAspectMustExtend, classify(err).Code)
		require.False(t, classify(err).Retryable)
	}
	nothing, ok := ExtendTargetAddsNothing(200, 300, "21:9")
	require.True(t, ok)
	require.False(t, nothing)
	_, ok = ExtendTargetAddsNothing(200, 300, "auto")
	require.False(t, ok, "auto is not a target")
}

// TestTheExtendCapFITS_THE_CEILING — the 3 MP cap and the $0.12 ceiling are ONE decision. The worst
// plan over every ratio and a spread of source sizes stays ≤ 3 MP, and the outpaint price of the
// worst plan (0.03 + 0.015 × (ceil(in MP) + ceil(out MP) − 1), fal's published formula) stays under
// fal.EstimatedRouteUSD(RouteOutpaint). Mutation: extendMaxPixels = 4e6 → red on both.
func TestTheExtendCapFITS_THE_CEILING(t *testing.T) {
	r21, _ := entity.DesignExtendRatioValue("21:9")
	p, err := planExtend(1024, 1536, r21)
	require.NoError(t, err)
	require.Less(t, p.Scale, 1.0, "a 1024×1536 → 21:9 canvas is 5.5 MP: the source is scaled down")
	require.LessOrEqual(t, p.Canvas.Dx()*p.Canvas.Dy(), 3_000_000)
	require.InDelta(t, 2650, p.Canvas.Dx(), 20, "a 2650×1136-class canvas")
	require.InDelta(t, 1136, p.Canvas.Dy(), 10)
	require.InDelta(t, float64(p.Source.Dx())/float64(p.Source.Dy()), 1024.0/1536.0, 0.005,
		"the downscale keeps the source's proportion")

	ceiling, _ := fal.EstimatedRouteUSD(fal.RouteOutpaint).Float64()
	worst := 0.0
	for _, ratio := range entity.DesignExtendRatios() {
		r, _ := entity.DesignExtendRatioValue(ratio)
		for _, sz := range [][2]int{{64, 64}, {1024, 1536}, {1536, 1024}, {3000, 4000}, {6000, 1000},
			{1000, 6000}, {7000, 7000}, {1733, 1733}, {2048, 1152}} {
			p, err := planExtend(sz[0], sz[1], r)
			if errors.Is(err, errExtendNothingToAdd) {
				continue
			}
			require.NoError(t, err)
			in := p.Source.Dx() * p.Source.Dy()
			out := p.Canvas.Dx() * p.Canvas.Dy()
			require.LessOrEqual(t, out, 3_000_000, "%v → %s", sz, ratio)
			cost := 0.03 + 0.015*(math.Ceil(float64(in)/1e6)+math.Ceil(float64(out)/1e6)-1)
			worst = math.Max(worst, cost)
		}
	}
	require.LessOrEqual(t, worst, ceiling, "the worst plan must be covered by the outpaint ceiling")
	require.Greater(t, worst, 0.09, "the table really reaches the cap (not a vacuous pass)")
}

// TestTheOutpaintBodyISPER_SLUG_FAMILY — flux gets expand_* in pixels, bria gets the canvas and the
// placement; any other slug is refused BEFORE the submit (no guessing a body with money on it).
func TestTheOutpaintBodyISPER_SLUG_FAMILY(t *testing.T) {
	r, _ := entity.DesignExtendRatioValue("4:3")
	plan, err := planExtend(500, 500, r)
	require.NoError(t, err)

	body, err := outpaintBody(fal.DefaultModelOutpaint, "https://cdn.example/m/11.png", plan)
	require.NoError(t, err)
	raw, _ := json.Marshal(body)
	require.JSONEq(t, `{"image_url":"https://cdn.example/m/11.png","expand_top":0,"expand_bottom":0,
		"expand_left":83,"expand_right":84,"auto_crop":false,"mode":"high","output_format":"png"}`, string(raw))

	body, err = outpaintBody("fal-ai/bria/expand", "https://cdn.example/m/11.png", plan)
	require.NoError(t, err)
	raw, _ = json.Marshal(body)
	require.JSONEq(t, `{"image_url":"https://cdn.example/m/11.png","canvas_size":[667,500],
		"original_image_size":[500,500],"original_image_location":[83,0]}`, string(raw))
	require.NotContains(t, string(raw), "aspect_ratio", "bria's aspect_ratio lacks 21:9 and overrides the placement")

	_, err = outpaintBody("fal-ai/some-other/outpainter", "https://cdn.example/m/11.png", plan)
	require.ErrorIs(t, err, fal.ErrBadOption)

	// And the route refuses it without a request leaving the process.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request left for a slug whose body was never read")
	}))
	defer srv.Close()
	c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, ModelOutpaint: "fal-ai/some-other/outpainter"})
	_, err = NewFalOutpaintProvider(c).Execute(context.Background(),
		Job{References: []string{"https://cdn.example/m/11.png"}, Extend: &plan})
	require.ErrorIs(t, err, fal.ErrBadOption)
	require.Equal(t, CodeBadRequest, classify(err).Code)
}

// outpaintStand — a fal queue that records the submit and answers one canvas.
type outpaintStand struct {
	mu        sync.Mutex
	submitted map[string]any
	paths     []string
	canvas    []byte
	units     string
}

func (s *outpaintStand) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/canvas.png":
			_, _ = w.Write(s.canvas)
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case strings.Contains(r.URL.Path, "/requests/"):
			if s.units != "" {
				w.Header().Set("x-fal-billable-units", s.units)
			}
			_, _ = w.Write([]byte(`{"images":[{"url":"http://` + r.Host + `/canvas.png","content_type":"image/png"}]}`))
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			require.NoError(t, json.Unmarshal(raw, &s.submitted))
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"request_id":"out-1"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}
}

// TestTheOutpaintRouteSUBMITS_THEN_COLLECTS_FOR_FREE — the pair, end to end on a stand: the submit is
// the flux body of the frozen plan, the collect reads images[0], the price is the route's (no
// tariff → the $0.12 code ceiling), and the history row gets NO words.
func TestTheOutpaintRouteSUBMITS_THEN_COLLECTS_FOR_FREE(t *testing.T) {
	stand := &outpaintStand{canvas: solidPNG(t, 700, 300, color.NRGBA{R: 1, G: 2, B: 3, A: 255}), units: "4"}
	srv := httptest.NewServer(stand.handler(t))
	defer srv.Close()
	c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second})
	prov := NewFalOutpaintProvider(c)

	r, _ := entity.DesignExtendRatioValue("21:9")
	plan, err := planExtend(200, 300, r)
	require.NoError(t, err)
	job := Job{RunID: 3, Kind: entity.DesignRunKindExtend, Prompt: "composed words", References: []string{"https://cdn.example/m/11.png"}, Extend: &plan}

	require.Equal(t, "", recordedPrompt(prov, job), "the outpaint body carries no words, so the row claims none")
	out, err := prov.Execute(context.Background(), job)
	require.NoError(t, err)
	require.True(t, out.Pending)
	require.Equal(t, fal.DefaultModelOutpaint+"#out-1", out.RequestID, "the locator: the slug it was SUBMITTED to, and the id")
	require.Equal(t, fal.DefaultModelOutpaint, out.Model)
	require.EqualValues(t, 250, stand.submitted["expand_left"])
	require.EqualValues(t, 250, stand.submitted["expand_right"])
	require.Equal(t, "https://cdn.example/m/11.png", stand.submitted["image_url"])
	require.NotContains(t, stand.submitted, "prompt")

	col, ok := prov.(Collector)
	require.True(t, ok, "submit = payment, collect = free: the route must be resumable")
	got, err := col.Collect(context.Background(), job, "out-1")
	require.NoError(t, err)
	require.Len(t, got.Artifacts, 1)
	require.Equal(t, ContentTypePNG, got.Artifacts[0].ContentType)
	require.Equal(t, "0.12", got.Price.Decimal.String(), "no tariff: the route's own per-request ceiling")
	require.Contains(t, stand.paths, "GET /fal-ai/flux-2-pro/requests/out-1/status")

	// Under a tariff: tariff × units.
	c2 := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second,
		UnitUSDOutpaint: 0.015, UnitsCeilingOutpaint: 8})
	got, err = NewFalOutpaintProvider(c2).(Collector).Collect(context.Background(), job, "out-1")
	require.NoError(t, err)
	require.Equal(t, "0.06", got.Price.Decimal.String())
}

func solidPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// translucentSource — a 200×300 PNG whose every pixel is distinct and HALF-TRANSPARENT: the
// composite must return these exact bytes, and a draw.Over would blend them with the answer.
func translucentSource(t *testing.T) ([]byte, *image.NRGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 200, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 200; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 128})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes(), img
}

func extendRun(params string) entity.DesignRun {
	r := testRun(5, entity.DesignRunKindExtend)
	r.Params = entity.RawJSON(params)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11}]}`)
	return r
}

// TestTheExtendCompositeKEEPS_THE_ORIGINAL_BYTE_FOR_BYTE — end to end through the worker: the plan is
// frozen before the call, the provider answers a canvas painted a translucent red everywhere, and
// the filed picture carries THE SOURCE'S OWN PIXELS in the source rectangle and the answer outside
// it. Mutation: draw.Over instead of draw.Src for the source → red (the half-transparent source
// blends with the red beneath).
func TestTheExtendCompositeKEEPS_THE_ORIGINAL_BYTE_FOR_BYTE(t *testing.T) {
	srcBytes, src := translucentSource(t)
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": srcBytes}}
	// 200×300 → 16:9: canvas 533×300, source at x 166..366.
	answer := solidPNG(t, 533, 300, color.NRGBA{R: 250, G: 0, B: 0, A: 200})
	prov := &fakeProvider{name: providerNameFalOutpaint, out: &Outcome{
		Artifacts: []Artifact{{Bytes: answer, ContentType: ContentTypePNG}},
	}}
	store := &fakeStore{}
	sink := newFakeSink(ContentTypePNG, ContentTypeJPEG)
	w := testWorker(store, media(11), sink, Providers{Outpaint: prov})
	w.objects = objs

	require.NoError(t, w.execute(context.Background(),
		extendRun(`{"extra_input_media_ids":[11],"extend":{"aspect_ratio":"16:9"}}`), "tok"))

	require.Len(t, prov.calls, 1)
	job := prov.calls[0]
	require.NotNil(t, job.Extend, "the plan was frozen before the call")
	require.Equal(t, []string{"https://cdn.example/m/11.png"}, job.References,
		"under the cap the stored url travels unchanged — no download for fal, no data URI")
	require.Equal(t, image.Rect(0, 0, 533, 300), job.Extend.Canvas)
	require.True(t, job.Extend.KeepAlpha)

	require.Len(t, sink.put, 1)
	require.Equal(t, ContentTypePNG, sink.putTypes[0], "a translucent source composites into a PNG")
	gotImg, err := png.Decode(bytes.NewReader(sink.putBytes[0]))
	require.NoError(t, err)
	got := gotImg.(*image.NRGBA)
	require.Equal(t, image.Rect(0, 0, 533, 300), got.Bounds())
	off := job.Extend.Offset
	for y := 0; y < 300; y++ {
		for x := 0; x < 200; x++ {
			require.Equal(t, src.NRGBAAt(x, y), got.NRGBAAt(x+off.X, y+off.Y),
				"source pixel (%d,%d) must be the original's own", x, y)
		}
	}
	require.Equal(t, color.NRGBA{R: 250, G: 0, B: 0, A: 200}, got.NRGBAAt(3, 150), "outside: the model's canvas")
	require.Equal(t, color.NRGBA{R: 250, G: 0, B: 0, A: 200}, got.NRGBAAt(530, 10))
}

// TestAnExtendAnswerOfTheWrongSizeISKEPT_AND_COMPLAINED — a canvas of another proportion is not
// pasted into; the paid canvas is filed as it came and the attempt names why. A canvas the model
// rounded (within 2 %) IS fitted and composited.
func TestAnExtendAnswerOfTheWrongSizeISKEPT_AND_COMPLAINED(t *testing.T) {
	srcBytes, src := translucentSource(t)
	r, _ := entity.DesignExtendRatioValue("16:9")
	plan, err := planExtend(200, 300, r)
	require.NoError(t, err)
	plan.SourceURL = "https://cdn.example/m/11.png"
	plan.Original = src.Bounds()
	plan.KeepAlpha = true

	w := testWorker(&fakeStore{}, media(11), newFakeSink(ContentTypePNG), Providers{})
	w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": srcBytes}}

	wrong := solidPNG(t, 300, 300, color.NRGBA{R: 9, A: 255})
	out := &Outcome{Artifacts: []Artifact{{Bytes: wrong, ContentType: ContentTypePNG}}}
	perr := w.postProcess(context.Background(), Job{Extend: &plan}, out)
	require.ErrorIs(t, perr, errExtendNotComposited)
	require.Equal(t, wrong, out.Artifacts[0].Bytes, "the paid canvas is kept as delivered")
	v := classify(perr)
	require.Equal(t, CodeExtendNotComposited, v.Code)
	require.Equal(t, entity.DesignAttemptDelivered, v.State)
	require.False(t, v.Retryable, "a second pass would buy a second canvas")

	rounded := solidPNG(t, 528, 304, color.NRGBA{R: 9, A: 255}) // each side rounded to a multiple of 16
	out = &Outcome{Artifacts: []Artifact{{Bytes: rounded, ContentType: ContentTypePNG}}}
	require.NoError(t, w.postProcess(context.Background(), Job{Extend: &plan}, out))
	gotImg, err := png.Decode(bytes.NewReader(out.Artifacts[0].Bytes))
	require.NoError(t, err)
	require.Equal(t, plan.Canvas, gotImg.Bounds(), "fitted to the planned canvas")
	require.Equal(t, src.NRGBAAt(7, 9), color.NRGBAModel.Convert(gotImg.At(7+plan.Offset.X, 9+plan.Offset.Y)))

	// The source cannot be read back: complaint, canvas kept.
	w.objects = &fakeObjects{byKey: map[string][]byte{}}
	out = &Outcome{Artifacts: []Artifact{{Bytes: rounded, ContentType: ContentTypePNG}}}
	require.ErrorIs(t, w.postProcess(context.Background(), Job{Extend: &plan}, out), errExtendNotComposited)
	require.Equal(t, rounded, out.Artifacts[0].Bytes)
}

// TestABigExtendSendsTHE_SCALED_SOURCE — over the 3 MP cap the provider must extend the pixels the
// composite will paste: the scaled source travels as a data URI, and the composite pastes THAT scaled
// source (the same function, the same scale).
func TestABigExtendSendsTHE_SCALED_SOURCE(t *testing.T) {
	big := solidPNG(t, 1024, 1536, color.NRGBA{R: 40, G: 90, B: 160, A: 255})
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": big}}
	job := Job{References: []string{"https://cdn.example/m/11.png"}}
	p := parseParams(entity.RawJSON(`{"extra_input_media_ids":[11],"extend":{"aspect_ratio":"21:9"}}`))
	require.NoError(t, deriveExtendPlan(context.Background(), objs, p, &job))
	require.NotNil(t, job.Extend)
	require.Less(t, job.Extend.Scale, 1.0)
	require.True(t, strings.HasPrefix(job.References[0], "data:image/png;base64,"),
		"the SCALED pixels travel as a lossless PNG, opaque or not — the raster the composite pastes (G-03)")
	require.LessOrEqual(t, len(job.References[0]), fal.MaxDataURIBytes)
	require.Equal(t, "https://cdn.example/m/11.png", job.Extend.SourceURL, "the composite re-reads the ORIGINAL")

	answer := solidPNG(t, job.Extend.Canvas.Dx(), job.Extend.Canvas.Dy(), color.NRGBA{R: 1, A: 255})
	src, _, err := fetchStoredPicture(context.Background(), objs, job.Extend.SourceURL)
	require.NoError(t, err)
	art, err := compositeExtend(src, *job.Extend, answer)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(art.Bytes))
	require.NoError(t, err)
	scaled := extendScaledSource(src, job.Extend.Source.Size())
	o := job.Extend.Offset
	require.Equal(t, color.NRGBAModel.Convert(scaled.At(10, 10)), color.NRGBAModel.Convert(img.At(10+o.X, 10+o.Y)))

	// The frozen target is not one of the nine: a free, terminal bad request.
	job = Job{References: []string{"https://cdn.example/m/11.png"}}
	err = deriveExtendPlan(context.Background(), objs, parseParams(entity.RawJSON(`{"extend":{"aspect_ratio":"auto"}}`)), &job)
	require.ErrorIs(t, err, fal.ErrBadOption)
	// A picture under the window minimum is refused before the money.
	tiny := &fakeObjects{byKey: map[string][]byte{"m/11.png": solidPNG(t, 40, 40, color.NRGBA{A: 255})}}
	job = Job{References: []string{"https://cdn.example/m/11.png"}}
	err = deriveExtendPlan(context.Background(), tiny, p, &job)
	require.ErrorIs(t, err, errFreeformSourceTooSmall)
	// The picture is gone: free and terminal.
	job = Job{}
	require.ErrorIs(t, deriveExtendPlan(context.Background(), objs, p, &job), errFreeformSourceGone)
}

// TestThePhase3ParamsAreREAD_BY_THEIR_SNAKE_CASE_NAMES — extend.aspect_ratio, inpaint.source_media_id
// and inpaint.mask_media_id through the door's own marshaller (protojson, UseProtoNames), and the
// camelCase half proving the tags carry the value (every one of the three is multi-word).
func TestThePhase3ParamsAreREAD_BY_THEIR_SNAKE_CASE_NAMES(t *testing.T) {
	written := &pb_common.DesignRunParams{
		Extend:  &pb_common.DesignExtendParams{AspectRatio: "21:9"},
		Inpaint: &pb_common.DesignInpaintParams{SourceMediaId: 41, MaskMediaId: 42},
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(written)
	require.NoError(t, err)
	p := parseParams(entity.RawJSON(raw))
	require.NotNil(t, p.Extend)
	require.Equal(t, "21:9", p.Extend.AspectRatio)
	require.NotNil(t, p.Inpaint)
	require.Equal(t, inpaintParams{SourceMediaID: 41, MaskMediaID: 42}, *p.Inpaint)

	camel, err := protojson.Marshal(written)
	require.NoError(t, err)
	require.Contains(t, string(camel), `"aspectRatio"`, "the fixture must really be camelCase")
	c := parseParams(entity.RawJSON(camel))
	require.NotNil(t, c.Extend)
	require.Empty(t, c.Extend.AspectRatio)
	require.NotNil(t, c.Inpaint)
	require.Zero(t, c.Inpaint.SourceMediaID)
	require.Zero(t, c.Inpaint.MaskMediaID)
}

// TestTheFalRouteObjectIS_THE_CLIENTS_OWN — bounded without a tariff, bounded with tariff + ceiling,
// UNBOUNDED with a tariff alone (the sentence names both variables); the kinds map to their routes.
func TestTheFalRouteObjectIS_THE_CLIENTS_OWN(t *testing.T) {
	r, ok := FalRouteOf(fal.New(fal.Config{APIKey: "k"}), entity.DesignRunKindExtend)
	require.True(t, ok)
	require.True(t, r.Bounded)
	require.Equal(t, "0.12", r.Ceiling.String())
	require.Equal(t, fal.DefaultModelOutpaint, r.Model)

	r, _ = FalRouteOf(fal.New(fal.Config{APIKey: "k", UnitUSDFill: 0.05, UnitsCeilingFill: 3}), entity.DesignRunKindInpaint)
	require.True(t, r.Bounded)
	require.Equal(t, "0.15", r.Ceiling.String())
	require.Equal(t, fal.DefaultModelFill, r.Model)

	r, _ = FalRouteOf(fal.New(fal.Config{APIKey: "k", UnitUSDOutpaint: 0.015}), entity.DesignRunKindExtend)
	require.False(t, r.Bounded)
	require.Contains(t, r.Unbounded, "FAL_UNIT_USD_OUTPAINT")
	require.Contains(t, r.Unbounded, "FAL_UNITS_CEILING_OUTPAINT")
	require.Equal(t, "FAL_UNITS_CEILING_OUTPAINT", r.Flag)

	_, ok = FalRouteOf(fal.New(fal.Config{}), entity.DesignRunKindCutout)
	require.False(t, ok)

	// The worker's routing: extend → Outpaint, a missing route is a named refusal, and both kinds
	// buy exactly one picture.
	out := NewFalOutpaintProvider(nil)
	got, err := Providers{Outpaint: out}.forKind(entity.DesignRunKindExtend)
	require.NoError(t, err)
	require.Equal(t, providerNameFalOutpaint, got.Name())
	_, err = Providers{}.forKind(entity.DesignRunKindExtend)
	require.ErrorIs(t, err, errRouteMissing)
	_, err = Providers{}.forKind(entity.DesignRunKindInpaint)
	require.ErrorIs(t, err, errRouteMissing)
	require.True(t, designKindBuysOnePicture(entity.DesignRunKindExtend))
	require.True(t, designKindBuysOnePicture(entity.DesignRunKindInpaint))
}
