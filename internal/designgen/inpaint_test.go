package designgen

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/require"
)

// ═══ B-14 — kind=inpaint: the real mask ═══

// grayMask — a w×h mask, black, with the rectangle `paint` painted white.
func grayMask(t *testing.T, w, h int, paint image.Rectangle) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := paint.Min.Y; y < paint.Max.Y; y++ {
		for x := paint.Min.X; x < paint.Max.X; x++ {
			img.SetGray(x, y, color.Gray{Y: 0xff})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// gradientPNG — an opaque w×h PNG whose every pixel differs from its neighbours.
func gradientPNG(t *testing.T, w, h int) ([]byte, *image.NRGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 5), B: uint8(x ^ y), A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes(), img
}

func inpaintRun(ask string) entity.DesignRun {
	r := testRun(9, entity.DesignRunKindInpaint)
	r.Params = entity.RawJSON(`{"inpaint":{"source_media_id":11,"mask_media_id":12}}`)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11},{"media_id":12}]}`)
	r.Ask.String, r.Ask.Valid = ask, ask != ""
	return r
}

// TestTheInpaintCropIS_PADDED_FLOORED_AND_CAPPED — bbox + 25 % each side, then the 512 floor inside
// the picture, then the 1 MP cap (the source scaled, floored so the product never rounds back over);
// and the fill ceiling covers the worst crop: 0.05 × (ceil(in MP) + ceil(out MP)) ≤ 0.15.
// Mutation: inpaintMaxPixels = 2e6 → red on both the cap and the money.
func TestTheInpaintCropIS_PADDED_FLOORED_AND_CAPPED(t *testing.T) {
	b := image.Rect(0, 0, 1024, 1536)
	// A 100×100 zone: 25 px each side → 150×150, then the 512 floor around its centre.
	r := inpaintRect(b, image.Rect(400, 600, 500, 700))
	require.Equal(t, image.Rect(194, 394, 706, 906), r)
	size, scale := inpaintCropSize(r)
	require.Equal(t, r.Size(), size)
	require.Equal(t, 1.0, scale)

	// A zone larger than the floor: the 25 % context is what sizes the crop (150 px each side).
	require.Equal(t, image.Rect(50, 150, 950, 1050), inpaintRect(b, image.Rect(200, 300, 800, 900)))
	// A zone at the corner: the floor is shifted back inside the picture.
	require.Equal(t, image.Rect(0, 0, 512, 512), inpaintRect(b, image.Rect(0, 0, 10, 10)))
	// A picture smaller than the floor gives its whole side.
	require.Equal(t, image.Rect(0, 0, 300, 200), inpaintRect(image.Rect(0, 0, 300, 200), image.Rect(10, 10, 20, 20)))

	ceiling, _ := fal.EstimatedRouteUSD(fal.RouteFill).Float64()
	worst := 0.0
	for _, zone := range []image.Rectangle{
		image.Rect(0, 0, 1024, 1536), image.Rect(100, 100, 900, 1400), image.Rect(10, 10, 20, 20),
	} {
		for _, pic := range []image.Rectangle{b, image.Rect(0, 0, 4000, 6000), image.Rect(0, 0, 1000, 1000)} {
			rr := inpaintRect(pic, zone.Intersect(pic))
			sz, sc := inpaintCropSize(rr)
			area := sz.X * sz.Y
			require.LessOrEqual(t, area, 1_000_000, "%v in %v", zone, pic)
			if rr.Dx()*rr.Dy() > 1_000_000 {
				require.Less(t, sc, 1.0)
				require.InDelta(t, float64(rr.Dx())/float64(rr.Dy()), float64(sz.X)/float64(sz.Y), 0.01)
			}
			mp := math.Ceil(float64(area) / 1e6)
			worst = math.Max(worst, 0.05*(mp+mp))
		}
	}
	require.LessOrEqual(t, worst, ceiling, "the worst crop must be covered by the fill ceiling")
	require.Equal(t, 0.10, worst, "the table reaches the cap (not a vacuous pass)")
}

// TestTheInpaintCompositeCHANGES_ONLY_THE_PAINT — a 64×64 picture, a 10×10 painted square, a
// flat-red answer: every pixel outside the square is the source's own, every pixel inside is red.
// Mutation: draw.Src instead of draw.Over in the DrawMask → red (outside turns transparent black).
func TestTheInpaintCompositeCHANGES_ONLY_THE_PAINT(t *testing.T) {
	srcBytes, src := gradientPNG(t, 64, 64)
	paint := image.Rect(20, 30, 30, 40)
	objs := &fakeObjects{byKey: map[string][]byte{
		"m/11.png": srcBytes,
		"m/12.png": grayMask(t, 64, 64, paint),
	}}
	job, err := buildJob(context.Background(), media(11, 12), objs, inpaintRun("a brass button"), "medium")
	require.NoError(t, err)
	require.NotNil(t, job.Inpaint)
	require.Equal(t, "a brass button", job.Prompt, "the ask verbatim — composePrompt is bypassed")
	require.Len(t, job.References, 1, "the mask is never a reference")
	require.True(t, strings.HasPrefix(job.InpaintMask, "data:image/png;base64,"))
	require.Equal(t, image.Rect(0, 0, 64, 64), job.Inpaint.Rect, "a 64 px picture gives its whole side")
	require.Equal(t, image.Pt(64, 64), job.Inpaint.Crop, "the size the crop travels at")
	require.True(t, strings.HasPrefix(job.References[0], "data:image/png;base64,"), "the crop is a lossless PNG")

	answer := solidPNG(t, 64, 64, color.NRGBA{R: 255, A: 255})
	prov := &fakeProvider{name: providerNameFalFill, out: &Outcome{
		Artifacts: []Artifact{{Bytes: answer, ContentType: ContentTypePNG}},
	}}
	sink := newFakeSink(ContentTypePNG, ContentTypeJPEG)
	w := testWorker(&fakeStore{}, media(11, 12), sink, Providers{Fill: prov})
	w.objects = objs
	require.NoError(t, w.execute(context.Background(), inpaintRun("a brass button"), "tok"))
	require.Len(t, prov.calls, 1)
	require.Len(t, sink.put, 1)
	gotImg, err := png.Decode(bytes.NewReader(sink.putBytes[0]))
	require.NoError(t, err)
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			got := color.NRGBAModel.Convert(gotImg.At(x, y)).(color.NRGBA)
			if image.Pt(x, y).In(paint) {
				require.Equal(t, color.NRGBA{R: 255, A: 255}, got, "inside (%d,%d)", x, y)
				continue
			}
			require.Equal(t, src.NRGBAAt(x, y), got, "outside (%d,%d) must be the source's own", x, y)
		}
	}
}

// TestAMaskThatFailsItsSecondLockIsREFUSED_BEFORE_ANY_SUBMIT — size mismatch, nothing painted, not a
// PNG, a mask row gone: each refused at build time (free, terminal), and the route is never called.
func TestAMaskThatFailsItsSecondLockIsREFUSED_BEFORE_ANY_SUBMIT(t *testing.T) {
	srcBytes, _ := gradientPNG(t, 64, 64)
	var jpg bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, image.NewGray(image.Rect(0, 0, 64, 64)), nil))
	for _, tc := range []struct {
		name  string
		mask  []byte
		media fakeMedia
		code  string
	}{
		{"mismatch", grayMask(t, 32, 64, image.Rect(0, 0, 5, 5)), media(11, 12), CodeMaskSizeMismatch},
		{"empty", grayMask(t, 64, 64, image.Rectangle{}), media(11, 12), CodeMaskEmpty},
		{"not a PNG", jpg.Bytes(), media(11, 12), CodeMaskInvalid},
		{"mask row gone", grayMask(t, 64, 64, image.Rect(0, 0, 5, 5)), media(11), CodeSourceGone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": srcBytes, "m/12.png": tc.mask}}
			_, err := buildJob(context.Background(), tc.media, objs, inpaintRun("x"), "medium")
			require.Error(t, err)
			v := classify(err)
			require.Equal(t, tc.code, v.Code)
			require.False(t, v.Retryable)
			require.Equal(t, entity.DesignAttemptFailed, v.State)

			prov := &fakeProvider{name: providerNameFalFill}
			w := testWorker(&fakeStore{}, tc.media, newFakeSink(ContentTypePNG, ContentTypeJPEG), Providers{Fill: prov})
			w.objects = objs
			_ = w.execute(context.Background(), inpaintRun("x"), "tok")
			require.Empty(t, prov.calls, "the fill route saw no request")
		})
	}
}

// TestTheFillRouteSENDS_THE_ASK_THE_CROP_AND_THE_MASK — the body on a stand, the history's prompt is
// the composed fill prompt the body carries (20-PROMPTS §3.3), the collect is priced by the fill
// route ($0.15 without a tariff).
func TestTheFillRouteSENDS_THE_ASK_THE_CROP_AND_THE_MASK(t *testing.T) {
	var submitted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/out.png":
			_, _ = w.Write(solidPNG(t, 64, 64, color.NRGBA{G: 255, A: 255}))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case strings.Contains(r.URL.Path, "/requests/"):
			_, _ = w.Write([]byte(`{"images":[{"url":"http://` + r.Host + `/out.png"}],"seed":1}`))
		case r.Method == http.MethodPost:
			require.Equal(t, "/"+fal.DefaultModelFill, r.URL.Path)
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &submitted))
			_, _ = w.Write([]byte(`{"request_id":"fill-1"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second})
	prov := NewFalFillProvider(c)
	job := Job{Kind: entity.DesignRunKindInpaint, Prompt: "a brass button",
		References: []string{"data:image/jpeg;base64,AAAA"}, InpaintMask: "data:image/png;base64,BBBB",
		Inpaint: &InpaintPlan{Rect: image.Rect(0, 0, 64, 64)}}

	// MUTATION (measured red): SentPrompt returning the bare job.Prompt again — the row would show
	// «a brass button» while the provider read the suffix too.
	const sent = "a brass button" + fillPromptSuffix
	require.Equal(t, sent, recordedPrompt(prov, job), "the row shows what the provider read")
	out, err := prov.Execute(context.Background(), job)
	require.NoError(t, err)
	require.True(t, out.Pending)
	require.Equal(t, map[string]any{"prompt": sent, "image_url": "data:image/jpeg;base64,AAAA",
		"mask_url": "data:image/png;base64,BBBB", "num_images": float64(1), "output_format": "png",
		"safety_tolerance": "2"}, submitted)

	got, err := prov.(Collector).Collect(context.Background(), job, "fill-1")
	require.NoError(t, err)
	require.Equal(t, "0.15", got.Price.Decimal.String())
	require.Len(t, got.Artifacts, 1)

	// No words: refused before the submit — on the BARE ask, so the suffix alone never travels.
	// MUTATION (measured red): the refusal checking fillPrompt(job.Prompt), which is never empty.
	job.Prompt = " "
	_, err = prov.Execute(context.Background(), job)
	require.ErrorIs(t, err, fal.ErrBadRequest)
}

// TestFillPromptDESCRIBES_THE_ZONE_AFTER_THE_ASK — 20-PROMPTS §3.3 (D2): FLUX Fill paints what its
// prompt describes, so the ask is followed by a description of the zone as continuing cloth. The ask
// is trimmed and stays FIRST. MUTATION (measured red): fillBody sending job.Prompt bare (the stand
// above), and the suffix text edited (this exact string).
func TestFillPromptDESCRIBES_THE_ZONE_AFTER_THE_ASK(t *testing.T) {
	require.Equal(t, "uncreased fabric continuing the surrounding cloth — the painted zone of a photograph of a "+
		"garment: the fill continues the surrounding cloth seamlessly, the same material, weave, colour, scale "+
		"and lighting, photographic.", fillPrompt("  uncreased fabric continuing the surrounding cloth \n"))
	require.True(t, strings.HasPrefix(fillPrompt("a brass button"), "a brass button — the painted zone"))

	body, err := fillBody(fal.DefaultModelFill, Job{Prompt: " a brass button ", References: []string{"x"}, InpaintMask: "m"})
	require.NoError(t, err)
	require.Equal(t, falFillProvider{}.SentPrompt(Job{Prompt: " a brass button "}), body["prompt"],
		"the history and the body carry one text")
}

// TestAnInpaintAnswerThatCannotGoBackIsKEPT_AND_COMPLAINED — the picture cannot be read back: the paid
// crop is filed as delivered with inpaint_not_composited.
func TestAnInpaintAnswerThatCannotGoBackIsKEPT_AND_COMPLAINED(t *testing.T) {
	w := testWorker(&fakeStore{}, media(11, 12), newFakeSink(ContentTypePNG), Providers{})
	w.objects = &fakeObjects{byKey: map[string][]byte{}}
	crop := solidPNG(t, 64, 64, color.NRGBA{A: 255})
	out := &Outcome{Artifacts: []Artifact{{Bytes: crop, ContentType: ContentTypePNG}}}
	plan := InpaintPlan{SourceURL: "https://cdn.example/m/11.png", MaskURL: "https://cdn.example/m/12.png",
		Bounds: image.Rect(0, 0, 64, 64), Rect: image.Rect(0, 0, 64, 64), Scale: 1}
	err := w.postProcess(context.Background(), Job{Inpaint: &plan}, out)
	require.ErrorIs(t, err, errInpaintNotComposited)
	require.Equal(t, crop, out.Artifacts[0].Bytes)
	v := classify(err)
	require.Equal(t, CodeInpaintNotComposited, v.Code)
	require.Equal(t, entity.DesignAttemptDelivered, v.State)
	require.False(t, v.Retryable)
}
