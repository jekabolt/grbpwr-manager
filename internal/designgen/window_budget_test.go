package designgen

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ G-03 r2 follow-up — the phase-2 generation window under the composite's memory budget ═══

// TestAWindowFramePastTheWorkingCapIsREFUSED_BY_ITS_HEADER — a frame whose header declares more than
// CompositeMaxSourcePixels: at build time a free, terminal source_too_large before any submit; after
// the money (a run planned before the cap) window_not_composited with the bought crop kept.
// The header-only PNG could never decode, so only the cap's own refusal makes these rows pass.
// MUTATION (measured red): drop the header check in deriveFreeformWindow / postProcess
// (freeformDecode instead of decodeCompositeSource) → a decode error, not source_too_large / the cap.
func TestAWindowFramePastTheWorkingCapIsREFUSED_BY_ITS_HEADER(t *testing.T) {
	huge := pngHeaderSays(t, 6000, 3001)
	t.Run("build", func(t *testing.T) {
		objs := &fakeObjects{byKey: map[string][]byte{"m/11.png": huge}}
		_, err := buildJob(context.Background(), media(11), objs, retouchRun(), "medium")
		require.ErrorIs(t, err, errFreeformSourceTooLarge)
		v := classify(err)
		require.Equal(t, CodeSourceTooLarge, v.Code)
		require.False(t, v.Retryable)
		require.Equal(t, entity.DesignAttemptFailed, v.State)
	})
	t.Run("composite after the money", func(t *testing.T) {
		w := testWorker(&fakeStore{}, media(11), newFakeSink(ContentTypePNG), Providers{})
		w.objects = &fakeObjects{byKey: map[string][]byte{"m/11.png": huge}}
		crop := windowAnswer(t, 48, 48, color.NRGBA{R: 255, A: 255})
		out := &Outcome{Artifacts: []Artifact{{Bytes: crop, ContentType: ContentTypePNG}}}
		win := GenerationWindow{SourceURL: "https://cdn.example/m/11.png", Rect: image.Rect(0, 0, 512, 512),
			Bounds: image.Rect(0, 0, 6000, 3001)}
		err := w.postProcess(context.Background(), Job{Window: &win}, out)
		require.ErrorIs(t, err, errWindowNotComposited)
		require.Contains(t, err.Error(), "at most 18 MP")
		require.Equal(t, crop, out.Artifacts[0].Bytes, "the bought crop is kept as delivered")
		v := classify(err)
		require.Equal(t, CodeWindowNotComposited, v.Code)
		require.Equal(t, entity.DesignAttemptDelivered, v.State)
	})
}

// TestTheWindowCompositeHAS_NO_FULL_SIZE_SCRATCH — a 12 MP frame, a full-frame retouch window, a 1 MP
// answer: the frame is drawn into in place and the answer scaled in bands — measured 26 MB (the
// decoded answer, one band, the capped output), where the old paste made a 48 MB copy plus
// Kernel.Scale's window-width × answer-height × 32-byte scratch (≈ 110 MB). MUTATION (measured red):
// leanScale in pasteReplacing → xdraw.CatmullRom.Scale(band, rect, got, ab, xdraw.Src, nil) →
// 5028 MB (the scratch per band).
func TestTheWindowCompositeHAS_NO_FULL_SIZE_SCRATCH(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a 12 MP picture")
	}
	const W, H = 4000, 3000
	src := image.NewRGBA(image.Rect(0, 0, W, H))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = uint8(i>>2), uint8(i>>10), 7, 0xff
	}
	answer := windowAnswer(t, 1154, 866, color.NRGBA{R: 9, G: 200, B: 30, A: 255})
	win := GenerationWindow{Rect: image.Rect(0, 0, W, H), Bounds: image.Rect(0, 0, W, H)}
	var art Artifact
	var err error
	alloc := totalAllocDuring(func() { art, err = compositeWindow(src, win, answer) })
	require.NoError(t, err)
	require.NotEmpty(t, art.Bytes)
	decoded := uint64(4 * W * H)
	require.Lessf(t, alloc, decoded, "the window composite allocated %d MB beside a %d MB frame",
		alloc>>20, decoded>>20)
}

// TestTheWindowPasteKEEPS_EVERY_BYTE_OUTSIDE_THE_WINDOW — the scaled path, band by band, into a
// half-transparent NRGBA frame drawn into in place: every pixel outside the frozen window is the
// frame's own NRGBA byte (the composite is a lossless PNG), every pixel inside is the answer.
// MUTATION (measured red): pasteReplacing(dst, dst.Bounds(), …) instead of the window rectangle →
// the answer covers the whole frame.
func TestTheWindowPasteKEEPS_EVERY_BYTE_OUTSIDE_THE_WINDOW(t *testing.T) {
	const W, H = 900, 700
	src := image.NewNRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: uint8(1 + (x+y)%254)})
		}
	}
	keep := image.NewNRGBA(src.Bounds())
	copy(keep.Pix, src.Pix)
	rect := image.Rect(100, 150, 700, 600)
	win := GenerationWindow{Rect: rect, Bounds: src.Bounds(), KeepAlpha: true}
	art, err := compositeWindow(src, win, windowAnswer(t, 300, 225, color.NRGBA{R: 250, G: 10, B: 10, A: 255}))
	require.NoError(t, err)
	require.Equal(t, ContentTypePNG, art.ContentType)
	got, err := png.Decode(bytes.NewReader(art.Bytes))
	require.NoError(t, err)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			c := color.NRGBAModel.Convert(got.At(x, y)).(color.NRGBA)
			if image.Pt(x, y).In(rect) {
				require.Equal(t, uint8(255), c.A, "inside (%d,%d)", x, y)
				require.InDelta(t, 250, int(c.R), 1, "inside (%d,%d)", x, y)
				continue
			}
			if c != keep.NRGBAAt(x, y) {
				t.Fatalf("outside (%d,%d): got %v, the frame's own is %v", x, y, c, keep.NRGBAAt(x, y))
			}
		}
	}
}

// TestAnOversizedWindowCompositeNEVER_FLATTENS_ALPHA — past the PNG ceiling a window over a frame
// with transparency is not stored as JPEG (it used to be PNG-or-JPEG by KeepAlpha alone, with no
// size check at all): window_not_composited, the bought crop kept. An opaque frame steps down the
// JPEG ladder and fits. MUTATION (measured red): encodeComposite(dst, false) in compositeWindow →
// the transparent frame comes back as JPEG.
func TestAnOversizedWindowCompositeNEVER_FLATTENS_ALPHA(t *testing.T) {
	keep := compositeMaxPNGBytes
	defer func() { compositeMaxPNGBytes = keep }()
	compositeMaxPNGBytes = 60_000

	opaque := noiseImage(300, 300, 0xff)
	art, err := compositeWindow(opaque, GenerationWindow{Rect: image.Rect(10, 10, 60, 60), Bounds: opaque.Bounds()},
		windowAnswer(t, 50, 50, color.NRGBA{A: 255}))
	require.NoError(t, err)
	require.Equal(t, ContentTypeJPEG, art.ContentType)
	require.LessOrEqual(t, len(art.Bytes), compositeMaxPNGBytes)

	// A frame that carried alpha at plan time: never a JPEG, even when one would fit.
	_, err = compositeWindow(noiseImage(300, 300, 0xff),
		GenerationWindow{Rect: image.Rect(10, 10, 60, 60), Bounds: image.Rect(0, 0, 300, 300), KeepAlpha: true},
		windowAnswer(t, 50, 50, color.NRGBA{A: 255}))
	require.ErrorIs(t, err, errCompositeTooLarge)
	require.Contains(t, err.Error(), "transparency")
}

// TestTheFreeformDownscaleHAS_NO_SCRATCH — the derive side (the window's crop, every outlined copy):
// a 3000×12000 picture fitted to 1536 allocates about its result, not Kernel.Scale's 384 × 12000 ×
// 32 bytes (≈ 147 MB; 590 MB for a 12000 px square). MUTATION (measured red): freeformFit back to
// xdraw.CatmullRom.Scale → 143 MB against a 9 MB bound.
func TestTheFreeformDownscaleHAS_NO_SCRATCH(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3000, 12000))
	var out image.Image
	alloc := totalAllocDuring(func() { out = freeformFit(src, freeformMaxSide) })
	require.Equal(t, freeformMaxSide, out.Bounds().Dy())
	res := uint64(4 * out.Bounds().Dx() * out.Bounds().Dy())
	require.Less(t, alloc, 4*res, "the result, its staging and the box-reduced source, got %d MB", alloc>>20)

	// The window's crop scales straight out of the frame — no full-size copy of the rectangle first.
	var uri string
	var err error
	alloc = totalAllocDuring(func() {
		uri, err = freeformCropAt(src, image.Rect(0, 0, 3000, 12000), false, freeformMaxSide)
	})
	require.NoError(t, err)
	require.NotEmpty(t, uri)
	require.Less(t, alloc, uint64(4*3000*12000)/4, "a crop of the whole frame allocated %d MB", alloc>>20)
}

// TestTheBoxPreReductionAVERAGES — leanScale's cheap first step for a shrink of ≥ 2×: a k×k block
// average (premultiplied), the last partial block averaging only what it has; and a solid colour
// shrunk 5× through it and the kernel stays that colour. MUTATION (measured red): average without the
// partial-block count (divide by k² always) → the edge column darkens.
func TestTheBoxPreReductionAVERAGES(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 5, 2))
	for x := 0; x < 5; x++ {
		v := uint8(0)
		if x%2 == 1 {
			v = 200
		}
		src.SetNRGBA(x, 0, color.NRGBA{R: v, G: v, B: v, A: 255})
		src.SetNRGBA(x, 1, color.NRGBA{R: v, G: v, B: v, A: 255})
	}
	r := boxReduce(src, src.Bounds(), 2)
	require.Equal(t, image.Rect(0, 0, 3, 1), r.Bounds())
	require.Equal(t, color.RGBA{R: 100, G: 100, B: 100, A: 255}, r.RGBAAt(0, 0))
	require.Equal(t, color.RGBA{R: 0, G: 0, B: 0, A: 255}, r.RGBAAt(2, 0), "the partial block is its own pixels' mean")

	solid := image.NewNRGBA(image.Rect(0, 0, 1000, 750))
	for i := 0; i < len(solid.Pix); i += 4 {
		solid.Pix[i], solid.Pix[i+1], solid.Pix[i+2], solid.Pix[i+3] = 40, 90, 160, 255
	}
	dst := image.NewNRGBA(image.Rect(0, 0, 200, 150))
	leanScale(dst, dst.Bounds(), solid, solid.Bounds())
	for i := 0; i < len(dst.Pix); i += 4 {
		require.InDelta(t, 40, int(dst.Pix[i]), 1)
		require.InDelta(t, 90, int(dst.Pix[i+1]), 1)
		require.InDelta(t, 160, int(dst.Pix[i+2]), 1)
		require.Equal(t, uint8(255), dst.Pix[i+3])
	}
}
