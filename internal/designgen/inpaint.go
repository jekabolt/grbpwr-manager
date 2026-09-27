package designgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	xdraw "golang.org/x/image/draw"
)

// ═══════════════ TILE 10's MASK ROUTE — kind=inpaint on fal's fill route (PLAYGROUND phase 3) ═══════════════
//
// THE REAL MASK, NOT THE PHASE-2 WINDOW. The phase-2 retouch replaced a whole padded rectangle («the
// rectangle around your zone may change»); this route repaints ONLY the pixels the person painted:
//
//   - the provider sees a padded crop around the paint (≤ 1 MP, the fill money ceiling) and the mask
//     cut the same way;
//   - after the paid call the answer is composited back through OUR mask, with draw.Over under an
//     alpha that is 0 outside the paint — so every pixel outside the mask is the source's own, by
//     construction, whatever the model did there (and whatever mask polarity it assumed: an
//     inverted answer is visible inside the paint, never harmful outside it).
//
// The plan is frozen before the money (buildJob) and read twice: the body and the composite.

const providerNameFalFill = "fal_fill"

// CodeInpaintNotComposited — the crop was bought and could not be pasted back through the mask: the
// crop is filed as delivered and the row says so (window_not_composited's twin).
const CodeInpaintNotComposited = "inpaint_not_composited"

// The worker's words for a mask that fails its second lock — the door's own codes.
const (
	CodeMaskSizeMismatch = entity.DesignErrorCodeMaskSizeMismatch
	CodeMaskEmpty        = entity.DesignErrorCodeMaskEmpty
	CodeMaskInvalid      = entity.DesignErrorCodeMaskInvalid
)

var (
	// errInpaintMaskGone — the mask's media row went away between the door and the pass. Free,
	// terminal (the errFreeformSourceGone seam).
	errInpaintMaskGone = errors.New("designgen: the mask of this retouch is gone")
	// errInpaintMaskMismatch — the mask and the picture read back at different sizes. Free, terminal.
	errInpaintMaskMismatch = errors.New("designgen: the mask is not the size of its picture")
	// errInpaintMaskEmpty — nothing is painted on the mask. Free, terminal.
	errInpaintMaskEmpty = errors.New("designgen: nothing is painted on the mask")
	// errInpaintMaskUnreadable — the mask is not a readable PNG. Free, terminal.
	errInpaintMaskUnreadable = errors.New("designgen: the mask is not a readable PNG")
	// errInpaintNotComposited is raised BESIDE the artifact, never instead of it.
	errInpaintNotComposited = errors.New("designgen: the retouched zone could not be composited through its mask")
)

const (
	// inpaintMaxPixels — the crop cap the fill money ceiling is computed for (fal.defaultFillUSD:
	// 0.05 × (1 MP in + 1 MP out) = 0.10 ≤ 0.15). Raise it and TestTheInpaintCapFITS_THE_CEILING
	// goes red — the ceiling must move with it.
	inpaintMaxPixels = 1_000_000
	// inpaintPadFraction — the context around the paint: 25 % of the painted box on each side.
	inpaintPadFraction = 0.25
	// inpaintMinSide — the crop's minimum side (the window's argument: a crop cut tight around a
	// small zone is a few dozen pixels the model upscales into mush).
	inpaintMinSide = 512
	// MaskThreshold — a mask pixel is PAINTED when its luma (premultiplied: a transparent pixel is
	// black) is ≥ this. The client paints pure white on black; the threshold absorbs nothing else.
	MaskThreshold = 128
)

// InpaintPlan — what a mask retouch froze about itself BEFORE it paid.
type InpaintPlan struct {
	// SourceURL / MaskURL — the stored urls; both are read again after the call.
	SourceURL, MaskURL string
	// Bounds is the picture's own rectangle; Rect the crop in picture pixels (sent, then pasted).
	Bounds, Rect image.Rectangle
	// Scale — crop pixels / picture pixels (1 = the crop travels at full resolution).
	Scale float64
	// KeepAlpha: the picture carries transparency. EncodePNG: the composite is a PNG (alpha, or a
	// PNG source — so a PNG original's untouched pixels come back byte-exact); else JPEG q92.
	KeepAlpha, EncodePNG bool
}

// MaskPaintedPixels — how many pixels of a mask are painted (luma ≥ MaskThreshold). THE ONE
// predicate: the door refuses zero (mask_empty), the worker rebuilds its alpha with it.
func MaskPaintedPixels(img image.Image) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if maskPainted(img.At(x, y)) {
				n++
			}
		}
	}
	return n
}

func maskPainted(c color.Color) bool {
	return color.GrayModel.Convert(c).(color.Gray).Y >= MaskThreshold
}

// inpaintMaskAlpha — the mask as a 0/255 alpha at its own bounds, and the painted bounding box.
func inpaintMaskAlpha(img image.Image) (*image.Alpha, image.Rectangle) {
	b := img.Bounds()
	a := image.NewAlpha(b)
	box := image.Rectangle{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if maskPainted(img.At(x, y)) {
				a.SetAlpha(x, y, color.Alpha{A: 0xff})
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return a, box
}

// inpaintRect — the crop around a painted box: +25 % of the box on each side, then padRect to
// inpaintMinSide, inside the picture.
func inpaintRect(b, box image.Rectangle) image.Rectangle {
	px := int(math.Ceil(float64(box.Dx()) * inpaintPadFraction))
	py := int(math.Ceil(float64(box.Dy()) * inpaintPadFraction))
	grown := image.Rect(box.Min.X-px, box.Min.Y-py, box.Max.X+px, box.Max.Y+py).Intersect(b)
	return padRect(b, grown, inpaintMinSide)
}

// inpaintCropSize — the size the crop travels at: the rectangle itself, or scaled down (floored) to
// stay ≤ inpaintMaxPixels.
func inpaintCropSize(r image.Rectangle) (image.Point, float64) {
	area := r.Dx() * r.Dy()
	if area <= inpaintMaxPixels {
		return r.Size(), 1
	}
	s := math.Sqrt(float64(inpaintMaxPixels) / float64(area))
	w := int(math.Max(1, math.Floor(float64(r.Dx())*s)))
	h := int(math.Max(1, math.Floor(float64(r.Dy())*s)))
	return image.Pt(w, h), float64(w) / float64(r.Dx())
}

// readInpaintMask — the mask bytes, a PNG and only a PNG (the door's rule: the client uploads it
// verbatim, bit-exact; a re-encoded WebP would not be), decoded under the process's pixel budget.
func readInpaintMask(ctx context.Context, objects objectFetcher, maskURL string) (image.Image, error) {
	raw, err := fetchStoredBytes(ctx, objects, maskURL)
	if err != nil {
		return nil, err
	}
	if !isPNGBytes(raw) {
		return nil, fmt.Errorf("%w: its bytes are %s", errInpaintMaskUnreadable, cutoutContentType(raw))
	}
	img, err := freeformDecode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInpaintMaskUnreadable, err)
	}
	return img, nil
}

// deriveInpaintPlan freezes job.Inpaint: reads the picture and its mask, refuses a mask of another
// size or with nothing painted (free, terminal — buildJob runs before StartAttempt), cuts the padded
// crop of both at the same size, and puts them in the job as two data URIs.
func deriveInpaintPlan(ctx context.Context, objects objectFetcher, maskURL string, job *Job) error {
	if len(job.References) == 0 {
		return fmt.Errorf("%w: the picture to retouch could not be read any more", errFreeformSourceGone)
	}
	if strings.TrimSpace(maskURL) == "" {
		return fmt.Errorf("%w: its media row is no longer there", errInpaintMaskGone)
	}
	if objects == nil {
		return fmt.Errorf("designgen: a mask retouch needs the picture and its mask, and this worker has no object store")
	}
	srcURL := job.References[0]
	src, isPNG, err := fetchStoredPicture(ctx, objects, srcURL)
	if err != nil {
		return fmt.Errorf("designgen: cannot read the picture to retouch: %w", err)
	}
	b := src.Bounds()
	if b.Dx() < windowMinSource || b.Dy() < windowMinSource {
		return fmt.Errorf("%w: it is %d×%d px, and a retouch needs at least %d px on each side",
			errFreeformSourceTooSmall, b.Dx(), b.Dy(), windowMinSource)
	}
	mask, err := readInpaintMask(ctx, objects, maskURL)
	if err != nil {
		if errors.Is(err, errInpaintMaskUnreadable) {
			return err
		}
		return fmt.Errorf("designgen: cannot read the mask: %w", err)
	}
	if mask.Bounds().Size() != b.Size() {
		return fmt.Errorf("%w: the picture is %d×%d and the mask %d×%d", errInpaintMaskMismatch,
			b.Dx(), b.Dy(), mask.Bounds().Dx(), mask.Bounds().Dy())
	}
	alpha, box := inpaintMaskAlpha(mask)
	if box.Empty() {
		return fmt.Errorf("%w: every pixel is below the paint threshold", errInpaintMaskEmpty)
	}
	// The mask is read at its own origin; the picture's is (0,0) for every decoder we have, but the
	// arithmetic below is in PICTURE coordinates, so the box is moved there explicitly.
	box = box.Sub(mask.Bounds().Min).Add(b.Min)
	alpha.Rect = alpha.Rect.Sub(mask.Bounds().Min).Add(b.Min)

	rect := inpaintRect(b, box)
	size, scale := inpaintCropSize(rect)
	keepAlpha := freeformSourceHasAlpha(src)

	crop := image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))
	xdraw.CatmullRom.Scale(crop, crop.Bounds(), src, rect, xdraw.Src, nil)
	maskCrop := image.NewGray(image.Rect(0, 0, size.X, size.Y))
	xdraw.NearestNeighbor.Scale(maskCrop, maskCrop.Bounds(), alpha, rect, xdraw.Src, nil)
	for i, v := range maskCrop.Pix { // re-threshold: nearest-neighbour never invents a grey, but say so
		if v >= MaskThreshold {
			maskCrop.Pix[i] = 0xff
		} else {
			maskCrop.Pix[i] = 0
		}
	}

	var cbuf bytes.Buffer
	mt := "image/jpeg"
	if keepAlpha {
		mt = "image/png"
		err = png.Encode(&cbuf, crop)
	} else {
		err = jpeg.Encode(&cbuf, crop, &jpeg.Options{Quality: freeformJPEGQuality})
	}
	if err != nil {
		return fmt.Errorf("designgen: cannot encode the retouch crop: %w", err)
	}
	var mbuf bytes.Buffer
	if err := png.Encode(&mbuf, maskCrop); err != nil {
		return fmt.Errorf("designgen: cannot encode the mask crop: %w", err)
	}
	cropURI := freeformDataURI(mt, cbuf.Bytes())
	maskURI := freeformDataURI("image/png", mbuf.Bytes())
	for what, uri := range map[string]string{"the retouch crop": cropURI, "the mask crop": maskURI} {
		if len(uri) > fal.MaxDataURIBytes {
			return fmt.Errorf("%w: %s is %d bytes of inline data, against a ceiling of %d",
				errFreeformJobTooLarge, what, len(uri), fal.MaxDataURIBytes)
		}
	}

	job.References = []string{cropURI}
	job.ReferenceViews = []string{""}
	job.InpaintMask = maskURI
	job.Inpaint = &InpaintPlan{
		SourceURL: srcURL, MaskURL: maskURL, Bounds: b, Rect: rect, Scale: scale,
		KeepAlpha: keepAlpha, EncodePNG: keepAlpha || isPNG,
	}
	return nil
}

// ─────────────────────────── the route ───────────────────────────

// falFillProvider is tile 10's mask route: Provider + Collector, submit = payment, collect = free.
type falFillProvider struct{ c *fal.Client }

// NewFalFillProvider wires kind=inpaint. A nil client is a disabled route, not a panic.
func NewFalFillProvider(c *fal.Client) Provider { return falFillProvider{c: c} }

func (p falFillProvider) Name() string { return providerNameFalFill }

func (p falFillProvider) Enabled() bool { return p.c != nil && p.c.Enabled() }

func (p falFillProvider) MissingCredential() string { return "FAL_KEY is not set" }

func (p falFillProvider) Produces() []string { return []string{ContentTypePNG, ContentTypeJPEG} }

// SentPrompt — THE ASK, VERBATIM: that is exactly what the fill body's `prompt` carries (no craft
// paragraph, no captions — buildJob bypasses composePrompt for this kind), so the history row shows
// what the provider read.
func (p falFillProvider) SentPrompt(job Job) string { return job.Prompt }

// fillBody — https://fal.ai/models/fal-ai/flux-pro/v1/fill/api (read 2026-09-27): prompt,
// image_url, mask_url (same dimensions), num_images 1, output_format png, safety_tolerance "2".
func fillBody(job Job) map[string]any {
	return map[string]any{
		"prompt":           job.Prompt,
		"image_url":        job.References[0],
		"mask_url":         job.InpaintMask,
		"num_images":       1,
		"output_format":    "png",
		"safety_tolerance": "2",
	}
}

// Execute SUBMITS the frozen crop and mask with the ask, and returns at once with the request id.
func (p falFillProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	if job.Inpaint == nil || len(job.References) != 1 || job.InpaintMask == "" {
		return nil, fmt.Errorf("%w: a mask retouch reached its route without its plan, crop or mask", fal.ErrBadRequest)
	}
	if strings.TrimSpace(job.Prompt) == "" {
		return nil, fmt.Errorf("%w: a mask retouch needs the words of what to paint (the door's words_required)",
			fal.ErrBadRequest)
	}
	model := p.c.ModelFor(fal.RouteFill)
	id, err := p.c.SubmitJSON(ctx, model, fillBody(job))
	if err != nil {
		if out := chargedRouteOutcome(p.c, fal.RouteFill, err); out != nil {
			return out, err
		}
		return nil, err
	}
	return &Outcome{RequestID: id, Model: model, Pending: true}, nil
}

// Collect is the FREE half. The composite through the mask is postProcess's.
func (p falFillProvider) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	return collectRouteFile(ctx, p.c, fal.RouteFill, job, requestID)
}

// ─────────────────────────── the composite ───────────────────────────

// compositeInpaintInto re-reads the picture and its mask and pastes the answer through the mask. A
// complaint, never a refusal: the money is spent.
func (w *Worker) compositeInpaintInto(ctx context.Context, plan InpaintPlan, out *Outcome) error {
	if w.objects == nil {
		return fmt.Errorf("%w: this worker has no object store to read the picture from", errInpaintNotComposited)
	}
	src, _, err := fetchStoredPicture(ctx, w.objects, plan.SourceURL)
	if err != nil {
		return fmt.Errorf("%w: the picture could not be read back: %v", errInpaintNotComposited, err)
	}
	mask, err := readInpaintMask(ctx, w.objects, plan.MaskURL)
	if err != nil {
		return fmt.Errorf("%w: the mask could not be read back: %v", errInpaintNotComposited, err)
	}
	fitted, err := compositeInpaint(src, mask, plan, out.Artifacts[0].Bytes)
	if err != nil {
		return fmt.Errorf("%w: %v", errInpaintNotComposited, err)
	}
	out.Artifacts[0] = fitted
	return nil
}

// compositeInpaint — dst = a copy of the picture; the answer (scaled into Rect when its size
// differs) is drawn with draw.DrawMask under OUR alpha and draw.Over: where the alpha is 0 — every
// pixel outside the paint — Over leaves the copy's bytes untouched by construction.
func compositeInpaint(src, mask image.Image, plan InpaintPlan, answer []byte) (Artifact, error) {
	if src.Bounds() != plan.Bounds {
		return Artifact{}, fmt.Errorf("the picture read back is %v, and the plan was made against %v", src.Bounds(), plan.Bounds)
	}
	if mask.Bounds().Size() != src.Bounds().Size() {
		return Artifact{}, fmt.Errorf("the mask read back is %v, the picture %v", mask.Bounds(), src.Bounds())
	}
	got, err := freeformDecode(answer)
	if err != nil {
		return Artifact{}, fmt.Errorf("the answer is not a readable picture: %w", err)
	}
	alpha, _ := inpaintMaskAlpha(mask)
	alpha.Rect = alpha.Rect.Sub(mask.Bounds().Min).Add(src.Bounds().Min)

	fittedAnswer := image.NewNRGBA(plan.Rect)
	if got.Bounds().Size() == plan.Rect.Size() {
		draw.Draw(fittedAnswer, plan.Rect, got, got.Bounds().Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(fittedAnswer, plan.Rect, got, got.Bounds(), xdraw.Src, nil)
	}

	dst := image.NewNRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	draw.DrawMask(dst, plan.Rect, fittedAnswer, plan.Rect.Min, alpha, plan.Rect.Min, draw.Over)

	var buf bytes.Buffer
	if plan.EncodePNG {
		if err := png.Encode(&buf, dst); err != nil {
			return Artifact{}, err
		}
		return Artifact{Bytes: buf.Bytes(), ContentType: ContentTypePNG}, nil
	}
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: compositeWindowJPEGQuality}); err != nil {
		return Artifact{}, err
	}
	return Artifact{Bytes: buf.Bytes(), ContentType: ContentTypeJPEG}, nil
}
