package designgen

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log/slog"
	"runtime"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// ═══════════════ THE MEMORY BUDGET OF A PHASE-3 COMPOSITE (G-03 r2, Codex BLOCKER 1) ═══════════════
//
// The extend and inpaint composites run AFTER the paid call, inside the one 0.5 GB backend process
// (.do/app.yaml: apps-s-1vcpu-0.5gb, instance_count 1). An allocation failure there is not an error
// a caller can classify: the Go runtime kills the whole process — the server, the other runs, and the
// settle of THIS run's money — and the next pickup collects the same paid answer and dies again.
// So the composite's memory is bounded BY CONSTRUCTION, in three places:
//
//  1. THE DOOR (admin: designRefuseUnusableMask / designRefuseExtendTarget) refuses a source over
//     CompositeMaxSourcePixels from its stored size or its header, before anything is reserved; the
//     worker's buildJob refuses it again from the header (free, terminal: source_too_large), and the
//     composite refuses it a third time from the header before decoding (a run derived before this
//     cap existed): the paid answer is then filed as delivered, with the route's *_not_composited.
//  2. NO FULL-SIZE SCRATCH: the provider's answer is scaled straight into the destination in bands
//     (compositeBandRows rows of one reused buffer) by leanScale, which computes each pixel directly
//     (Kernel.Transform) instead of Kernel.Scale's separable pass — whose scratch buffer is
//     dr.Dx() × sr.Dy() × 32 bytes (≈ 200 MB for a full-frame 6000 px paste of a 1 MP answer). The
//     mask is kept as one byte per pixel of the crop rectangle only; the decoded source is drawn into
//     in place when it is a raster Go can encode losslessly (NRGBA / RGBA / their 16-bit twins).
//  3. THE ENCODE IS CAPPED WHILE IT RUNS: cappedBuffer refuses the first byte past the bucket's
//     verbatim ceiling, so a noisy composite aborts at 21 MiB instead of buffering a 100 MiB PNG.
//
// THE PEAK at the cap (18 MP), live bytes: an 8-bit PNG source composited in place ≈ 4 × 18 MP
// (72 MB) + the crop's alpha (≤ 18 MB) + the capped output (≤ 21 MiB) + the ≤ 1 MP answer and one
// band (< 10 MB) ≈ 120 MB; a JPEG source (decoded YCbCr, copied to NRGBA) ≈ 140 MB; the exotic worst
// — a 16-bit PNG or a CMYK JPEG — ≈ 185 MB. The heap is collected explicitly between the stages
// (releaseHeap) so the garbage of one stage (the decoded mask, the raw bytes, the decoded JPEG) does
// not stack on the next under GOGC's 2× growth.

// CompositeMaxSourcePixels — THE WORKING PIXEL CAP of an extend or inpaint source (18 MP).
//
// Why 18: every 4K picture this playground itself makes fits (Gemini 3 Pro Image at 4K is 16.8–17.2
// MP across its ratios, e.g. 4096×4096, 3584×4800, 6336×2688), as do 12–16 MP phone photos; a 24 MP+
// camera original does not, and is refused with words that say to downscale it. The number follows
// from the budget above: per-pixel cost × 18 MP stays well under ~200 MB at the worst format, where
// the bucket's own 40 MP ceiling (bucket.ImageWithinBudget) would allow ≈ 400 MB. Exported for the
// door: ONE number — a copy at the door would drift from the worker's silently.
const CompositeMaxSourcePixels = 18_000_000

// compositeBandRows — the rows one band of the pasted answer covers (one reused buffer of
// rect width × compositeBandRows × 4 bytes: ≤ 3 MB at the 12000 px side ceiling).
const compositeBandRows = 64

// errCompositeTooLarge — the composite does not fit the bucket's verbatim ceiling in any format this
// picture may be stored as. Raised inside the route's *_not_composited error: the paid answer is
// filed as delivered, never retried.
var errCompositeTooLarge = errors.New("designgen: the composite is larger than the store accepts")

// compositeSourceOverCap — the header of a stored picture declares more than CompositeMaxSourcePixels.
// Wrapped in errFreeformSourceTooLarge at build time (free, terminal: source_too_large) and in the
// route's *_not_composited after the money.
func compositeSourceOverCap(raw []byte) (image.Config, error) {
	cfg, err := freeformDecodeConfig(raw)
	if err != nil {
		return cfg, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > CompositeMaxSourcePixels {
		return cfg, fmt.Errorf("%w: it is %d×%d px (%.1f MP), and an extend or a retouch works on at most "+
			"%.0f MP — the result is composited at full size in this server's memory. Downscale the picture "+
			"and upload it again", errFreeformSourceTooLarge, cfg.Width, cfg.Height,
			float64(cfg.Width)*float64(cfg.Height)/1e6, float64(CompositeMaxSourcePixels)/1e6)
	}
	return cfg, nil
}

// decodeCompositeSource — the stored bytes of an extend / inpaint source, header-checked against
// CompositeMaxSourcePixels BEFORE a pixel is decoded.
func decodeCompositeSource(raw []byte) (image.Image, error) {
	if _, err := compositeSourceOverCap(raw); err != nil {
		return nil, err
	}
	return freeformDecode(raw)
}

// releaseHeap — a collection between the stages of a composite. The stages allocate tens of MB each
// and drop them (raw bytes → decoded picture → the next stage); under GOGC=100 the heap may grow to
// twice its peak live size before the collector runs, which is what would turn a 150 MB composite
// into a 300 MB process. One explicit GC of a heap of pointer-free pixel buffers costs milliseconds.
func releaseHeap() { runtime.GC() }

// leanScale — Catmull-Rom from sr of src onto dr of dst, with NO scratch buffer: Kernel.Transform
// computes each destination pixel from its source neighbourhood directly (the kernel widened by the
// scale when shrinking, exactly as Kernel.Scale does), where Kernel.Scale's separable pass first
// allocates dr.Dx() × sr.Dy() × 32 bytes. Only the pixels of dst ∩ dr are computed, so a band of dst
// costs a band. Deterministic: the same call twice gives the same pixels (extendScaledSource relies
// on it — the body and the composite must paste one raster).
func leanScale(dst draw.Image, dr image.Rectangle, src image.Image, sr image.Rectangle) {
	if dr.Empty() || sr.Empty() {
		return
	}
	kx := float64(dr.Dx()) / float64(sr.Dx())
	ky := float64(dr.Dy()) / float64(sr.Dy())
	s2d := f64.Aff3{
		kx, 0, float64(dr.Min.X) - float64(sr.Min.X)*kx,
		0, ky, float64(dr.Min.Y) - float64(sr.Min.Y)*ky,
	}
	// The transformed rectangle is dr up to float rounding; clip to dr so a rounding never touches
	// the pixel beside it.
	target := dst
	if sub, ok := dst.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		if d, ok := sub.SubImage(dr).(draw.Image); ok {
			target = d
		}
	}
	xdraw.CatmullRom.Transform(target, s2d, src, sr, xdraw.Src, nil)
}

// compositeCanvas — the raster the composite draws into. A freshly decoded NRGBA / RGBA / NRGBA64 /
// RGBA64 source is drawn into IN PLACE (it is ours: decoded for this call, shared with nothing), and
// png encodes each of them losslessly; any other decoded form (YCbCr, NYCbCrA, CMYK, Gray, Paletted)
// is copied once into an NRGBA — the copy the composite has always made — and the source dropped.
func compositeCanvas(src image.Image) draw.Image {
	switch m := src.(type) {
	case *image.NRGBA:
		return m
	case *image.RGBA:
		return m
	case *image.NRGBA64:
		return m
	case *image.RGBA64:
		return m
	}
	dst := image.NewNRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

// cappedBuffer — an io.Writer that refuses the first byte past max (errCompositeTooLarge), so an
// encoder that would produce 100 MiB stops at the ceiling instead of buffering it. Reused across the
// encodings of one composite (reset keeps the backing array).
type cappedBuffer struct {
	buf []byte
	max int
}

func newCappedBuffer(max, estimate int) *cappedBuffer {
	if estimate <= 0 || estimate > max {
		estimate = max
	}
	return &cappedBuffer{buf: make([]byte, 0, estimate), max: max}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if len(c.buf)+len(p) > c.max {
		return 0, errCompositeTooLarge
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *cappedBuffer) reset() { c.buf = c.buf[:0] }

// compositeJPEGQualities — the ladder of the opaque fallback, top down: the window's q92 first (the
// fidelity the owner asked for), then two steps down; past the last the composite is not stored.
var compositeJPEGQualities = []int{compositeWindowJPEGQuality, 85, 75}

// compositeMaxPNGBytes — the bucket's verbatim ceiling (≈ 21 MiB), the one every stored composite
// must fit. A var only so a probe can reach the fallbacks with a small picture.
var compositeMaxPNGBytes = bucket.MaxVerbatimImageBytes

// compositePNGWorstCase — the largest PNG Go's encoder can write for a w×h raster of bpp bytes per
// pixel: every row filtered (1 byte) and stored raw, deflate's stored-block framing (5 bytes per
// 64 KiB), IDAT chunk framing (12 bytes per chunk of ≥ 8 KiB) and the fixed headers. What makes the
// PNG→JPEG fallback unreachable for an extend (≤ 3 MP canvas × 4 bytes ≈ 12 MB) is this bound, held
// by a probe.
func compositePNGWorstCase(w, h, bpp int) int {
	raw := h * (1 + w*bpp)
	return raw + (raw/65535+1)*5 + (raw/8192+1)*12 + 1024
}

// encodeComposite — THE STORED FORMAT OF A PHASE-3 COMPOSITE: a lossless PNG, so the source pixels
// the composite keeps are the source's own DECODED pixels whatever the source's format was (G-03,
// Codex 4 = Fable m-5; the owner's fidelity over file size). The PNG must fit the bucket's verbatim
// ceiling (bucket.MaxVerbatimImageBytes), and the encode stops at it (cappedBuffer). When it does not
// fit (G-03 r2, Codex 5):
//
//   - a picture with TRANSPARENCY (keepAlpha, or any non-opaque pixel) is NEVER stored as JPEG — a
//     JPEG would turn every transparent pixel outside the mask opaque: the composite fails
//     (errCompositeTooLarge) and the paid answer is filed as delivered with *_not_composited;
//   - an opaque picture steps down a JPEG ladder (q92 → 85 → 75), each encode capped the same way;
//     past the last step the composite fails the same way. The fallback is logged.
//
// An extend never reaches the fallback: its canvas is ≤ 3 MP, whose worst-case PNG
// (compositePNGWorstCase) is ≈ 12 MB. An inpaint of a large noisy photo can.
func encodeComposite(img image.Image, keepAlpha bool) (Artifact, error) {
	b := img.Bounds()
	bpp := 4
	switch img.(type) {
	case *image.NRGBA64, *image.RGBA64:
		bpp = 8
	}
	out := newCappedBuffer(compositeMaxPNGBytes, compositePNGWorstCase(b.Dx(), b.Dy(), bpp))
	err := png.Encode(out, img)
	if err == nil {
		return Artifact{Bytes: out.buf, ContentType: ContentTypePNG}, nil
	}
	if !errors.Is(err, errCompositeTooLarge) {
		return Artifact{}, err
	}
	if keepAlpha || !compositeOpaque(img) {
		return Artifact{}, fmt.Errorf("%w: its lossless PNG passes the store's %d-byte ceiling, and the picture "+
			"carries transparency, which a JPEG would destroy — the answer is kept as delivered",
			errCompositeTooLarge, compositeMaxPNGBytes)
	}
	for _, q := range compositeJPEGQualities {
		out.reset()
		err := jpeg.Encode(out, img, &jpeg.Options{Quality: q})
		if err == nil {
			slog.Default().Warn("a composite is too large for a lossless PNG and is stored as JPEG",
				slog.Int("quality", q), slog.Int("jpeg_bytes", len(out.buf)), slog.Int("ceiling", compositeMaxPNGBytes),
				slog.Int("width", b.Dx()), slog.Int("height", b.Dy()))
			return Artifact{Bytes: out.buf, ContentType: ContentTypeJPEG}, nil
		}
		if !errors.Is(err, errCompositeTooLarge) {
			return Artifact{}, err
		}
	}
	return Artifact{}, fmt.Errorf("%w: neither its lossless PNG nor a JPEG down to q%d fits the store's %d-byte "+
		"ceiling — the answer is kept as delivered", errCompositeTooLarge,
		compositeJPEGQualities[len(compositeJPEGQualities)-1], compositeMaxPNGBytes)
}

// compositeOpaque — every pixel opaque. A raster type that cannot say is treated as NOT opaque: the
// wrong answer here must cost a failed composite, never a destroyed alpha.
func compositeOpaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return false
}

// maskAlphaOver — the painted pixels of a decoded mask as a 0/255 alpha over r (PICTURE coordinates;
// the mask's own origin is mapped onto the picture's, pictureMin). One byte per pixel of r only —
// the crop rectangle, never the whole picture.
func maskAlphaOver(mask image.Image, pictureMin image.Point, r image.Rectangle) *image.Alpha {
	a := image.NewAlpha(r)
	off := mask.Bounds().Min.Sub(pictureMin)
	g, isGray := mask.(*image.Gray)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := a.Pix[(y-r.Min.Y)*a.Stride:]
		for x := r.Min.X; x < r.Max.X; x++ {
			mx, my := x+off.X, y+off.Y
			var painted bool
			if isGray {
				painted = g.Pix[g.PixOffset(mx, my)] >= MaskThreshold
			} else {
				painted = maskPainted(mask.At(mx, my))
			}
			if painted {
				row[x-r.Min.X] = 0xff
			}
		}
	}
	return a
}

// maskPaintedBox — the bounding box of the painted pixels, in the mask's own coordinates, without
// allocating anything the size of the mask.
func maskPaintedBox(mask image.Image) image.Rectangle {
	b := mask.Bounds()
	box := image.Rectangle{}
	g, isGray := mask.(*image.Gray)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		minX, maxX := -1, -1
		for x := b.Min.X; x < b.Max.X; x++ {
			var painted bool
			if isGray {
				painted = g.Pix[g.PixOffset(x, y)] >= MaskThreshold
			} else {
				painted = maskPainted(mask.At(x, y))
			}
			if painted {
				if minX < 0 {
					minX = x
				}
				maxX = x
			}
		}
		if minX >= 0 {
			box = box.Union(image.Rect(minX, y, maxX+1, y+1))
		}
	}
	return box
}

// pasteThroughMask — THE PASTE of a composite: the answer (got, its own rectangle ab) scaled onto
// rect of dst, band by band through one reused buffer, and drawn with draw.DrawMask under alpha and
// draw.Over — where the alpha is 0 (every pixel outside the paint) the standard library's Over is a
// no-op on dst's bytes, by construction. An answer already at rect's size is copied, not resampled.
func pasteThroughMask(dst draw.Image, rect image.Rectangle, got image.Image, ab image.Rectangle, alpha *image.Alpha) {
	same := ab.Size() == rect.Size()
	rows := compositeBandRows
	if rows > rect.Dy() {
		rows = rect.Dy()
	}
	pix := make([]uint8, 4*rect.Dx()*rows)
	for y0 := rect.Min.Y; y0 < rect.Max.Y; y0 += rows {
		y1 := y0 + rows
		if y1 > rect.Max.Y {
			y1 = rect.Max.Y
		}
		br := image.Rect(rect.Min.X, y0, rect.Max.X, y1)
		band := &image.RGBA{Pix: pix[:4*br.Dx()*br.Dy()], Stride: 4 * br.Dx(), Rect: br}
		if same {
			draw.Draw(band, br, got, ab.Min.Add(br.Min.Sub(rect.Min)), draw.Src)
		} else {
			leanScale(band, rect, got, ab)
		}
		draw.DrawMask(dst, br, band, br.Min, alpha, br.Min, draw.Over)
	}
}
