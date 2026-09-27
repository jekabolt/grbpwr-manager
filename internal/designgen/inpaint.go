package designgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/shopspring/decimal"
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
	// Crop — the size the crop (and its mask) TRAVELLED at: Rect × Scale. The answer is expected at
	// this size; one of another size is a drift the composite names instead of hiding (G-03, Codex 5).
	Crop image.Point
	// KeepAlpha: the picture carries transparency (for the record; the composite is a lossless PNG
	// whatever the picture was — encodeComposite).
	KeepAlpha bool
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
	// The mask is the picture's size, so it is held to the picture's working cap (G-03 r2, Codex 1):
	// a mask past it belongs to a picture no composite here may decode.
	if _, err := compositeSourceOverCap(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", errInpaintMaskUnreadable, err)
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
	// ⚠ HEADER FIRST, PIXELS LAST, ONE BIG RASTER AT A TIME (G-03 r2, Codex 1). The picture's header
	// is checked against CompositeMaxSourcePixels before anything is decoded (free, terminal:
	// source_too_large — buildJob runs before StartAttempt); the mask is decoded, measured and cut to
	// the crop, and DROPPED before the picture is decoded; neither is ever copied at full size.
	srcRaw, err := fetchStoredBytes(ctx, objects, srcURL)
	if err != nil {
		return fmt.Errorf("designgen: cannot read the picture to retouch: %w", err)
	}
	cfg, err := compositeSourceOverCap(srcRaw)
	if err != nil {
		if errors.Is(err, errFreeformSourceTooLarge) {
			return err
		}
		return fmt.Errorf("designgen: cannot read the picture to retouch: %w", err)
	}
	b := image.Rect(0, 0, cfg.Width, cfg.Height)
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
	box := maskPaintedBox(mask)
	if box.Empty() {
		return fmt.Errorf("%w: every pixel is below the paint threshold", errInpaintMaskEmpty)
	}
	// The mask is read at its own origin; the arithmetic below is in PICTURE coordinates (0,0 for
	// every decoder we have), so the box is moved there explicitly.
	box = box.Sub(mask.Bounds().Min).Add(b.Min)

	rect := inpaintRect(b, box)
	size, scale := inpaintCropSize(rect)

	maskCrop := image.NewGray(image.Rect(0, 0, size.X, size.Y))
	alpha := maskAlphaOver(mask, b.Min, rect)
	xdraw.NearestNeighbor.Scale(maskCrop, maskCrop.Bounds(), alpha, rect, xdraw.Src, nil)
	for i, v := range maskCrop.Pix { // re-threshold: nearest-neighbour never invents a grey, but say so
		if v >= MaskThreshold {
			maskCrop.Pix[i] = 0xff
		} else {
			maskCrop.Pix[i] = 0
		}
	}
	releaseHeap()

	src, err := freeformDecode(srcRaw)
	if err != nil {
		return fmt.Errorf("designgen: cannot read the picture to retouch: %w", err)
	}
	if src.Bounds() != b {
		return fmt.Errorf("designgen: the picture to retouch decoded at %v, its header said %v", src.Bounds(), b)
	}
	keepAlpha := freeformSourceHasAlpha(src)
	crop := image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))
	leanScale(crop, crop.Bounds(), src, rect)

	// ⚠ THE CROP TRAVELS AS A LOSSLESS PNG (G-03, Codex 4): the model paints inside the mask AROUND
	// these pixels, and a JPEG of them is another raster than the one the composite keeps. ≤ 1 MP of
	// RGBA is ≤ 4 MB before base64 (≤ 5.4 MB after), under fal.MaxDataURIBytes by construction.
	var cbuf bytes.Buffer
	const mt = "image/png"
	if err := png.Encode(&cbuf, crop); err != nil {
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
		SourceURL: srcURL, MaskURL: maskURL, Bounds: b, Rect: rect, Scale: scale, Crop: size,
		KeepAlpha: keepAlpha,
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

func (p falFillProvider) MissingCredential() string { return noKeySentence("fal", "FAL_KEY") }

func (p falFillProvider) Produces() []string { return []string{ContentTypePNG, ContentTypeJPEG} }

// SentPrompt — THE ASK, VERBATIM: that is exactly what the fill body's `prompt` carries (no craft
// paragraph, no captions — buildJob bypasses composePrompt for this kind), so the history row shows
// what the provider read.
func (p falFillProvider) SentPrompt(job Job) string { return job.Prompt }

// fillFamily — the one family whose fill body was read on the provider's page (2026-09-27). Any other
// FAL_MODEL_FILL is closed at the band and the door (FalRouteOf, G-03 Fable m-1) and refused here,
// before the submit, as the second lock.
func fillFamily(model string) string {
	if strings.HasPrefix(model, "fal-ai/flux-pro/v1/fill") {
		return "flux"
	}
	return ""
}

// fillBody — https://fal.ai/models/fal-ai/flux-pro/v1/fill/api (read 2026-09-27): prompt,
// image_url, mask_url (same dimensions), num_images 1, output_format png, safety_tolerance "2".
func fillBody(model string, job Job) (map[string]any, error) {
	if fillFamily(model) == "" {
		return nil, fmt.Errorf("%w: FAL_MODEL_FILL %q is not %s — this route builds no body for a slug it has "+
			"not read", fal.ErrBadOption, model, fal.DefaultModelFill)
	}
	return map[string]any{
		"prompt":           job.Prompt,
		"image_url":        job.References[0],
		"mask_url":         job.InpaintMask,
		"num_images":       1,
		"output_format":    "png",
		"safety_tolerance": "2",
	}, nil
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
	if err := falLocatorFits(model); err != nil {
		return nil, err
	}
	body, err := fillBody(model, job)
	if err != nil {
		return nil, err
	}
	// THE SUBMIT IS THE PAYMENT, SO IT OPENS THE LEDGER ROW (B-07) — see threedfal.go.
	h := job.beginCall(ctx, entity.AIProviderFal, model, 1)
	id, err := p.c.SubmitJSON(ctx, model, body)
	if err != nil {
		if out := chargedRouteOutcome(p.c, fal.RouteFill, job, err); out != nil {
			job.finishCall(ctx, h, falSubmitEnd(err, out.Price))
			return out, err
		}
		job.finishCall(ctx, h, falSubmitEnd(err, decimal.NullDecimal{}))
		return nil, err
	}
	locator := falLocator(model, id)
	job.finishCall(ctx, h, acceptedEnd(locator))
	return &Outcome{RequestID: locator, Model: model, Pending: true, Provider: entity.AIProviderFal}, nil
}

// Collect is the FREE half. The composite through the mask is postProcess's.
func (p falFillProvider) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	return collectRouteFile(ctx, p.c, fal.RouteFill, job, requestID)
}

// ─────────────────────────── the composite ───────────────────────────

// compositeInpaintInto re-reads the mask and the picture and pastes the answer through the mask. A
// complaint, never a refusal: the money is spent.
//
// ⚠ IT RUNS AFTER THE PAYMENT, INSIDE THE ONE 0.5 GB PROCESS (G-03 r2, Codex BLOCKER 1), and is
// bounded by construction (composite_budget.go): every raster's header is checked before its pixels
// are decoded; the answer is checked against the planned crop before it is decoded; the mask becomes
// one byte per pixel of the crop rectangle and is dropped before the picture is decoded; the picture
// is drawn into in place (or copied once, when its decoded form cannot be encoded losslessly); the
// answer is scaled into the rectangle band by band — no full-size scratch; the PNG encode stops at the
// bucket's ceiling. Any refusal on the way is errInpaintNotComposited: the paid crop is filed as
// delivered, terminal — never an OOM that would kill the settle of this very run and every pickup
// after it.
func (w *Worker) compositeInpaintInto(ctx context.Context, plan InpaintPlan, out *Outcome) error {
	if w.objects == nil {
		return fmt.Errorf("%w: this worker has no object store to read the picture from", errInpaintNotComposited)
	}
	art, err := w.compositeInpaint(ctx, plan, out.Artifacts[0].Bytes)
	if err != nil {
		return fmt.Errorf("%w: %v", errInpaintNotComposited, err)
	}
	out.Artifacts[0] = art
	return nil
}

// compositeInpaint — the stages of the paste, in the order that keeps one large raster alive at a
// time. dst is the picture's own decoded raster (or its one NRGBA copy); the answer (scaled into Rect
// when its size differs) goes in under OUR alpha with draw.Over: where the alpha is 0 — every pixel
// outside the paint — Over leaves dst's bytes untouched by construction.
func (w *Worker) compositeInpaint(ctx context.Context, plan InpaintPlan, answer []byte) (Artifact, error) {
	// 1. The answer: its header against the size the crop travelled at, before a pixel of it.
	acfg, err := freeformDecodeConfig(answer)
	if err != nil {
		return Artifact{}, fmt.Errorf("the answer is not a readable picture: %w", err)
	}
	// ⚠ A DRIFT IS NAMED, NOT HIDDEN (G-03, Codex 5). The answer is expected at the size the crop
	// travelled at (plan.Crop); a model that rounded it (within max(16 px, 2 %) a side — extend's own
	// slack) is fitted, anything else is another picture than the one planned — its money may not be
	// the money reserved, and scaling it into the crop would distort it silently. It is kept as
	// delivered and the attempt says why (inpaint_not_composited).
	if want := plan.Crop; want.X > 0 && want.Y > 0 && image.Pt(acfg.Width, acfg.Height) != want &&
		(!extendNearSize(acfg.Width, want.X) || !extendNearSize(acfg.Height, want.Y)) {
		return Artifact{}, fmt.Errorf("the answer is %d×%d and the crop was sent at %d×%d — another size; "+
			"the answer is kept as delivered", acfg.Width, acfg.Height, want.X, want.Y)
	}
	if !plan.Rect.In(plan.Bounds) || plan.Rect.Empty() {
		return Artifact{}, fmt.Errorf("the planned crop %v is not inside the picture %v", plan.Rect, plan.Bounds)
	}
	got, err := freeformDecode(answer)
	if err != nil {
		return Artifact{}, fmt.Errorf("the answer is not a readable picture: %w", err)
	}

	// 2. The mask → one byte per pixel of the crop rectangle; the decoded mask is dropped.
	mask, err := readInpaintMask(ctx, w.objects, plan.MaskURL)
	if err != nil {
		return Artifact{}, fmt.Errorf("the mask could not be read back: %v", err)
	}
	if mask.Bounds().Size() != plan.Bounds.Size() {
		return Artifact{}, fmt.Errorf("the mask read back is %v, the picture was planned at %v", mask.Bounds(), plan.Bounds)
	}
	alpha := maskAlphaOver(mask, plan.Bounds.Min, plan.Rect)
	releaseHeap()

	// 3. The picture, header-capped, decoded, and drawn into.
	raw, err := fetchStoredBytes(ctx, w.objects, plan.SourceURL)
	if err != nil {
		return Artifact{}, fmt.Errorf("the picture could not be read back: %v", err)
	}
	src, err := decodeCompositeSource(raw)
	if err != nil {
		return Artifact{}, fmt.Errorf("the picture could not be read back: %v", err)
	}
	if src.Bounds() != plan.Bounds {
		return Artifact{}, fmt.Errorf("the picture read back is %v, and the plan was made against %v", src.Bounds(), plan.Bounds)
	}
	dst := compositeCanvas(src)
	releaseHeap()

	pasteThroughMask(dst, plan.Rect, got, got.Bounds(), alpha)
	return encodeComposite(dst, plan.KeepAlpha)
}
