package designgen

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"runtime"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/require"
)

// ═══ G-03 r2 — the composite's memory budget (Codex BLOCKER 1), its encode (MAJOR 5), its drift (MAJOR 6) ═══

// totalAllocDuring — the bytes allocated while f runs (every allocation, garbage included): the
// budget this file holds is an ALLOCATION budget, so a full-size scratch buffer shows up whether or
// not the collector has already reclaimed it.
func totalAllocDuring(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestASourcePastTheWorkingCapIsREFUSED_BY_ITS_HEADER — a picture whose header declares more than
// CompositeMaxSourcePixels is refused from its header alone: at build time (before StartAttempt: free,
// terminal source_too_large, the route never called) and, for a run planned before the cap existed,
// in the composite after the money (the paid answer kept as delivered, *_not_composited, not retried).
// The header-only PNG could never be decoded — a refusal that tried would fail differently.
// MUTATION (measured red): make compositeSourceOverCap compare against math.MaxInt64 → every row
// fails with a decode error instead of source_too_large / the cap's words.
func TestASourcePastTheWorkingCapIsREFUSED_BY_ITS_HEADER(t *testing.T) {
	huge := pngHeaderSays(t, 6000, 3001) // 18.006 MP: over by one row of pixels
	mask := grayMask(t, 64, 64, image.Rect(10, 10, 20, 20))

	t.Run("inpaint build", func(t *testing.T) {
		objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": huge, "m/12.png": mask}}
		_, err := buildJob(context.Background(), media(11, 12), objs, inpaintRun("a brass button"), "medium")
		require.ErrorIs(t, err, errFreeformSourceTooLarge)
		v := classify(err)
		require.Equal(t, CodeSourceTooLarge, v.Code)
		require.False(t, v.Retryable)
		require.Equal(t, []string{"m/11.png"}, objs.asked, "refused before the mask is even read")
	})
	t.Run("extend build", func(t *testing.T) {
		objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": huge}}
		p := parseParams(entity.RawJSON(`{"extra_input_media_ids":[11],"extend":{"aspect_ratio":"21:9"}}`))
		job := Job{References: []string{"https://cdn.example/m/11.png"}}
		err := deriveExtendPlan(context.Background(), objs, p, &job)
		require.ErrorIs(t, err, errFreeformSourceTooLarge)
		require.Equal(t, CodeSourceTooLarge, classify(err).Code)
	})
	t.Run("inpaint composite after the money", func(t *testing.T) {
		w := testWorker(&fakeStore{}, media(11, 12), newFakeSink(ContentTypePNG), Providers{})
		w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": huge, "m/12.png": pngHeaderSays(t, 6000, 3001)}}
		crop := solidPNG(t, 64, 64, color.NRGBA{A: 255})
		out := &Outcome{Artifacts: []Artifact{{Bytes: crop, ContentType: ContentTypePNG}}}
		plan := InpaintPlan{SourceURL: "https://cdn.example/m/11.png", MaskURL: "https://cdn.example/m/12.png",
			Bounds: image.Rect(0, 0, 6000, 3001), Rect: image.Rect(0, 0, 64, 64), Scale: 1, Crop: image.Pt(64, 64)}
		err := w.postProcess(context.Background(), Job{Inpaint: &plan}, out)
		require.ErrorIs(t, err, errInpaintNotComposited)
		require.Contains(t, err.Error(), "at most 18 MP")
		require.Equal(t, crop, out.Artifacts[0].Bytes, "the paid crop is kept as delivered")
		v := classify(err)
		require.Equal(t, entity.DesignAttemptDelivered, v.State)
		require.False(t, v.Retryable)
	})
	t.Run("extend composite after the money", func(t *testing.T) {
		w := testWorker(&fakeStore{}, media(11), newFakeSink(ContentTypePNG), Providers{})
		w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": huge}}
		canvas := solidPNG(t, 64, 27, color.NRGBA{A: 255})
		out := &Outcome{Artifacts: []Artifact{{Bytes: canvas, ContentType: ContentTypePNG}}}
		plan := ExtendPlan{SourceURL: "https://cdn.example/m/11.png", Original: image.Rect(0, 0, 6000, 3001),
			Source: image.Rect(0, 0, 60, 27), Canvas: image.Rect(0, 0, 64, 27), Scale: 0.01}
		err := w.postProcess(context.Background(), Job{Extend: &plan}, out)
		require.ErrorIs(t, err, errExtendNotComposited)
		require.Contains(t, err.Error(), "at most 18 MP")
		require.Equal(t, canvas, out.Artifacts[0].Bytes)
	})
}

// TestTheInpaintCompositeHAS_NO_FULL_SIZE_SCRATCH — a 12 MP picture, a full-frame mask, a ≤ 1 MP
// answer: the composite allocates the decoded picture, one byte per pixel of alpha and the capped
// output — not a full-size fitted answer, a full-size copy, and Kernel.Scale's dr.Dx() × sr.Dy() ×
// 32-byte scratch (≈ 110 MB here). Measured ≈ 96 MB in all (decoded mask and picture, alpha, the
// output buffer); the bound is 2.4 × the decoded picture (≈ 110 MB); the old paste's three full-size
// buffers alone were ≈ 200 MB. MUTATION (measured red): leanScale in pasteThroughMask →
// xdraw.CatmullRom.Scale(band, rect, got, ab, xdraw.Src, nil) → 5098 MB (the scratch per band).
func TestTheInpaintCompositeHAS_NO_FULL_SIZE_SCRATCH(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a 12 MP picture")
	}
	const W, H = 4000, 3000
	src := image.NewRGBA(image.Rect(0, 0, W, H)) // decodes back as *image.RGBA: composited in place
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = uint8(i>>2), uint8(i>>10), 7, 0xff
	}
	var sb bytes.Buffer
	require.NoError(t, (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&sb, src))
	src = nil
	mask := grayMask(t, W, H, image.Rect(0, 0, W, H))
	rect := image.Rect(0, 0, W, H)
	size, scale := inpaintCropSize(rect)
	answer := solidPNG(t, size.X, size.Y, color.NRGBA{R: 9, G: 200, B: 30, A: 255})

	w := testWorker(&fakeStore{}, media(11, 12), newFakeSink(ContentTypePNG), Providers{})
	w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": sb.Bytes(), "m/12.png": mask}}
	plan := InpaintPlan{SourceURL: "https://cdn.example/m/11.png", MaskURL: "https://cdn.example/m/12.png",
		Bounds: rect, Rect: rect, Scale: scale, Crop: size}
	var art Artifact
	var err error
	alloc := totalAllocDuring(func() { art, err = w.compositeInpaint(context.Background(), plan, answer) })
	require.NoError(t, err)
	require.NotEmpty(t, art.Bytes)
	decoded := uint64(4 * W * H)
	require.Lessf(t, alloc, decoded*24/10, "the composite allocated %d MB for a %d MB picture",
		alloc>>20, decoded>>20)
}

// TestTheExtendScaleHAS_NO_SCRATCH — the extend's downscale (the body before the money and the paste
// after it) allocates about its ≤ 3 MP result (≈ 27 MB here, box-reduced source and staging
// included), not Kernel.Scale's size.X × source height × 32 bytes (≈ 154 MB).
// MUTATION (measured red): extendScaledSource back to xdraw.CatmullRom.Scale → 154 MB.
func TestTheExtendScaleHAS_NO_SCRATCH(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3000, 4000))
	size := image.Pt(1200, 1600)
	var out *image.NRGBA
	alloc := totalAllocDuring(func() { out = extendScaledSource(src, size) })
	require.Equal(t, size, out.Bounds().Size())
	// The result, its RGBA staging buffer and the 2× box-reduced source: under 4 results.
	require.Less(t, alloc, uint64(4*size.X*size.Y)*4, "the result and a little more, got %d MB", alloc>>20)
}

// TestTheBandedPasteKEEPS_EVERY_BYTE_OUTSIDE_THE_PAINT — the scaled path (a crop over 1 MP travels
// smaller and comes back smaller), band by band, into a half-transparent NRGBA source drawn into in
// place: every pixel outside the paint is the source's own NRGBA byte, every painted pixel is the
// answer's colour. MUTATION (measured red): draw.Src instead of draw.Over in pasteThroughMask →
// outside turns transparent black.
func TestTheBandedPasteKEEPS_EVERY_BYTE_OUTSIDE_THE_PAINT(t *testing.T) {
	const W, H = 1400, 1000
	src := image.NewNRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: uint8(1 + (x+y)%254)})
		}
	}
	var sb bytes.Buffer
	require.NoError(t, png.Encode(&sb, src))
	paint := image.Rect(150, 100, 1250, 900)
	objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": sb.Bytes(), "m/12.png": grayMask(t, W, H, paint)}}
	job, err := buildJob(context.Background(), media(11, 12), objs, inpaintRun("a brass button"), "medium")
	require.NoError(t, err)
	require.Less(t, job.Inpaint.Scale, 1.0, "the crop travels scaled: the answer is resampled on the way back")
	require.True(t, job.Inpaint.KeepAlpha)

	answer := solidPNG(t, job.Inpaint.Crop.X, job.Inpaint.Crop.Y, color.NRGBA{R: 250, G: 10, B: 10, A: 255})
	w := testWorker(&fakeStore{}, media(11, 12), newFakeSink(ContentTypePNG), Providers{})
	w.objects = objs
	art, err := w.compositeInpaint(context.Background(), *job.Inpaint, answer)
	require.NoError(t, err)
	require.Equal(t, ContentTypePNG, art.ContentType)
	got, err := png.Decode(bytes.NewReader(art.Bytes))
	require.NoError(t, err)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			c := color.NRGBAModel.Convert(got.At(x, y)).(color.NRGBA)
			if image.Pt(x, y).In(paint) {
				require.Equal(t, uint8(255), c.A, "inside (%d,%d)", x, y)
				require.InDelta(t, 250, int(c.R), 1, "inside (%d,%d)", x, y)
				continue
			}
			if c != src.NRGBAAt(x, y) {
				t.Fatalf("outside (%d,%d): got %v, the source's own is %v", x, y, c, src.NRGBAAt(x, y))
			}
		}
	}
}

// noiseImage — an incompressible raster (opaque unless alpha < 255).
func noiseImage(w, h int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(88172645)
	for i := 0; i < len(img.Pix); i += 4 {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(seed), uint8(seed>>8), uint8(seed>>16), alpha
	}
	return img
}

// TestAnOversizedCompositeNEVER_DROPS_ALPHA_AND_NEVER_OVERSHOOTS — Codex MAJOR 5. Past the PNG
// ceiling: a picture with transparency is never stored as JPEG (the composite fails, the answer is
// kept as delivered); an opaque one steps down the JPEG ladder and what is returned FITS the ceiling;
// past the last step the composite fails instead of handing the upload bytes it will refuse.
// MUTATIONS (measured red): drop the alpha branch → the translucent row comes back as JPEG; return
// the first JPEG unchecked (the old code) → the ladder row returns a JPEG over the ceiling.
func TestAnOversizedCompositeNEVER_DROPS_ALPHA_AND_NEVER_OVERSHOOTS(t *testing.T) {
	keep := compositeMaxPNGBytes
	defer func() { compositeMaxPNGBytes = keep }()
	opaque := noiseImage(96, 96, 0xff)
	jpegLen := func(q int) int {
		var b bytes.Buffer
		require.NoError(t, jpeg.Encode(&b, opaque, &jpeg.Options{Quality: q}))
		return b.Len()
	}
	q92, q75 := jpegLen(92), jpegLen(75)
	require.Greater(t, q92, q75)

	t.Run("opaque: the ladder stops at the first JPEG that fits", func(t *testing.T) {
		compositeMaxPNGBytes = q75
		art, err := encodeComposite(opaque, false)
		require.NoError(t, err)
		require.Equal(t, ContentTypeJPEG, art.ContentType)
		require.LessOrEqual(t, len(art.Bytes), compositeMaxPNGBytes)
		_, err = jpeg.Decode(bytes.NewReader(art.Bytes))
		require.NoError(t, err)
	})
	t.Run("opaque: nothing fits", func(t *testing.T) {
		compositeMaxPNGBytes = q75 - 1
		_, err := encodeComposite(opaque, false)
		require.ErrorIs(t, err, errCompositeTooLarge)
	})
	t.Run("transparency is never a JPEG", func(t *testing.T) {
		compositeMaxPNGBytes = q92 // a JPEG of it would fit; the PNG does not
		_, err := encodeComposite(noiseImage(96, 96, 0x80), false)
		require.ErrorIs(t, err, errCompositeTooLarge)
		require.Contains(t, err.Error(), "transparency")
		_, err = encodeComposite(opaque, true)
		require.ErrorIs(t, err, errCompositeTooLarge, "a source that carried alpha is not flattened either")
	})
	t.Run("the encode stops at the ceiling instead of buffering the whole PNG", func(t *testing.T) {
		c := newCappedBuffer(100, 0)
		_, err := c.Write(make([]byte, 60))
		require.NoError(t, err)
		_, err = c.Write(make([]byte, 41))
		require.ErrorIs(t, err, errCompositeTooLarge)
		require.Len(t, c.buf, 60)
		require.LessOrEqual(t, cap(c.buf), 100)
	})
	t.Run("the paid answer is kept when the composite cannot be stored", func(t *testing.T) {
		compositeMaxPNGBytes = 64
		srcBytes, _ := translucentSource(t)
		w := testWorker(&fakeStore{}, media(11), newFakeSink(ContentTypePNG), Providers{})
		w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": srcBytes}}
		r, _ := entity.DesignExtendRatioValue("16:9")
		plan, err := planExtend(200, 300, r)
		require.NoError(t, err)
		plan.SourceURL, plan.KeepAlpha = "https://cdn.example/m/11.png", true
		canvas := solidPNG(t, plan.Canvas.Dx(), plan.Canvas.Dy(), color.NRGBA{R: 3, A: 255})
		out := &Outcome{Artifacts: []Artifact{{Bytes: canvas, ContentType: ContentTypePNG}}}
		err = w.postProcess(context.Background(), Job{Extend: &plan}, out)
		require.ErrorIs(t, err, errExtendNotComposited)
		require.Contains(t, err.Error(), "transparency")
		require.Equal(t, canvas, out.Artifacts[0].Bytes)
		require.Equal(t, entity.DesignAttemptDelivered, classify(err).State)
	})
}

// TestAnExtendCompositeCANNOT_REACH_THE_FALLBACK — the fallback is unreachable for an extend by
// construction: the worst PNG Go can write for a ≤ 3 MP canvas of 4-byte pixels (at the tallest
// canvas the side ceiling allows) is under the bucket's ceiling, and the bound is a real bound —
// incompressible noise with alpha encodes under it. MUTATION (measured red): extendMaxPixels = 6e6 →
// the worst case passes 21 MiB.
func TestAnExtendCompositeCANNOT_REACH_THE_FALLBACK(t *testing.T) {
	worst := compositePNGWorstCase(extendMaxPixels/12000, 12000, 4)
	require.LessOrEqual(t, worst, bucket.MaxVerbatimImageBytes)
	require.Less(t, compositePNGWorstCase(1732, 1732, 4), bucket.MaxVerbatimImageBytes)

	noise := noiseImage(700, 500, 0x80)
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, noise))
	require.LessOrEqual(t, b.Len(), compositePNGWorstCase(700, 500, 4), "the bound holds for noise")
	require.Greater(t, b.Len(), 700*500*3, "and noise really is incompressible (not a vacuous pass)")
}

// TestTheCeilingsCOVER_THE_FITTED_DRIFT — Codex MAJOR 6. Neither flux route takes an output size
// (fal's schemas, read 2026-09-27: the fill output is the input's size, the outpaint output is the
// input plus expand_*; bria's canvas_size is sent and IS the output size), so the output is fixed by
// what the plan sends, up to the provider's rounding. The composite fits a drift of max(16 px, 2 %)
// a side and refuses more; the reservation must cover the worst fitted drift at fal's rounded-up
// megapixel price. MUTATION (measured red): extendAnswerSlackFraction = 0.2 → the outpaint row overshoots (0.135 > 0.12).
func TestTheCeilingsCOVER_THE_FITTED_DRIFT(t *testing.T) {
	grow := func(side int) int {
		return side + int(math.Floor(math.Max(extendAnswerSlackPx, extendAnswerSlackFraction*float64(side))))
	}
	mp := func(w, h int) float64 { return math.Ceil(float64(w) * float64(h) / 1e6) }

	fillCeiling, _ := fal.EstimatedRouteUSD(fal.RouteFill).Float64()
	worstFill := 0.0
	for _, r := range []image.Rectangle{image.Rect(0, 0, 1000, 1000), image.Rect(0, 0, 2000, 500),
		image.Rect(0, 0, 512, 512), image.Rect(0, 0, 6000, 6000), image.Rect(0, 0, 12000, 700)} {
		size, _ := inpaintCropSize(r)
		cost := 0.05 * (mp(size.X, size.Y) + mp(grow(size.X), grow(size.Y)))
		worstFill = math.Max(worstFill, cost)
	}
	require.LessOrEqual(t, worstFill, fillCeiling+1e-9, "the fill reservation covers the fitted drift")
	require.Greater(t, worstFill, 0.10, "the drift really rounds up a megapixel (not a vacuous pass)")

	outCeiling, _ := fal.EstimatedRouteUSD(fal.RouteOutpaint).Float64()
	worstOut := 0.0
	for _, ratio := range entity.DesignExtendRatios() {
		r, _ := entity.DesignExtendRatioValue(ratio)
		for _, sz := range [][2]int{{1024, 1536}, {3000, 4000}, {6000, 1000}, {1000, 6000}, {1733, 1733}} {
			p, err := planExtend(sz[0], sz[1], r)
			if err != nil {
				continue
			}
			cost := 0.03 + 0.015*(mp(p.Source.Dx(), p.Source.Dy())+mp(grow(p.Canvas.Dx()), grow(p.Canvas.Dy()))-1)
			worstOut = math.Max(worstOut, cost)
		}
	}
	require.LessOrEqual(t, worstOut, outCeiling+1e-9, "the outpaint reservation covers the fitted drift")
}
