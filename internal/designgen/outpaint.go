package designgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"math"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/shopspring/decimal"
	xdraw "golang.org/x/image/draw"
)

// ═══════════════ TILE 9 «EXTEND IMAGE» — kind=extend on fal's outpaint route (PLAYGROUND phase 3) ═══════════════
//
// ONE PICTURE, ONE TARGET PROPORTION, NO WORDS. The server decides everything else: how many pixels
// each side grows (the source stays centred on the axis that grows), whether the canvas has to be
// scaled down to stay under the 3 MP cap the money ceiling was computed for, and — after the paid
// call — that THE ORIGINAL IS KEPT: the untouched source pixels are drawn back over the provider's
// answer with draw.Src, so «the original is kept» is a property of the bytes, not a promise of the
// model.
//
// THE PLAN IS FROZEN BEFORE THE MONEY (buildJob, before StartAttempt) and read twice after it — by
// the route (the body) and by the composite (where to paste). A plan recomputed after the call would
// paste by one arithmetic what was cut by another.

// providerNameFalOutpaint is what lands in design_run_attempt.provider: the ROUTE, not the vendor
// (the same argument as fal_cutout — three routes of one vendor, three tariffs).
const providerNameFalOutpaint = "fal_outpaint"

// CodeExtendNotComposited — THE CANVAS WAS BOUGHT, AND THE SOURCE COULD NOT BE PASTED BACK INTO IT.
// A DELIVERED attempt's code (window_not_composited's twin): the paid canvas is filed as it came,
// and the row says the source region is the provider's copy, not the original bytes.
const CodeExtendNotComposited = "extend_not_composited"

// CodeTargetAspectMustExtend — the worker's word for «the target adds no pixels», the door's own
// (entity.DesignErrorCodeTargetAspectMustExtend). Free and terminal.
const CodeTargetAspectMustExtend = entity.DesignErrorCodeTargetAspectMustExtend

var (
	// errExtendNothingToAdd — the frozen target is the source's own proportion (within
	// extendSameRatioTolerance): the door refused this already; this is the second lock, for a
	// picture whose stored dimensions were unknown. Free and terminal.
	errExtendNothingToAdd = errors.New("designgen: the target proportion adds no pixels to this picture")
	// errExtendNotComposited is raised BESIDE the artifact, never instead of it.
	errExtendNotComposited = errors.New("designgen: the extended canvas could not be composited with its source")
)

const (
	// extendMaxPixels — the canvas cap the outpaint money ceiling is computed for
	// (fal.defaultOutpaintUSD: 0.03 + 0.015 × (3 + 3 − 1) = 0.105 ≤ 0.12). Raise it and
	// TestTheExtendCapFITS_THE_CEILING goes red — the ceiling must move with it.
	extendMaxPixels = 3_000_000
	// extendSameRatioTolerance — a target within 0.5 % of the source's proportion adds nothing worth
	// a paid call (the door's target_aspect_must_extend uses the same function).
	extendSameRatioTolerance = 0.005
	// extendAnswerSlackPx / extendAnswerSlackFraction — how far each side of the provider's canvas
	// may drift from the frozen one and still be fitted to it: max(16 px, 2 % of the side) — a model
	// that rounds its output to a multiple of 16 px. Beyond it the canvas is kept as delivered and
	// the complaint says so.
	extendAnswerSlackPx       = 16
	extendAnswerSlackFraction = 0.02
)

// ExtendPlan — what an extend run froze about itself BEFORE it paid.
type ExtendPlan struct {
	// SourceURL is the stored url of the ORIGINAL picture; the composite reads it again after the call.
	SourceURL string
	// Original is the picture's own rectangle when the plan was made: a picture read back at another
	// size is not the picture that was planned, and is not pasted.
	Original image.Rectangle
	// Source is the source's rectangle AFTER the 3 MP downscale (origin 0,0); = Original's size when
	// Scale == 1.
	Source image.Rectangle
	// Canvas is the target canvas (origin 0,0); Offset is where Source sits in it.
	Canvas image.Rectangle
	Offset image.Point
	// The per-side expansion in pixels — what the flux body sends.
	ExpandTop, ExpandBottom, ExpandLeft, ExpandRight int
	// Scale is Source / Original (1 = untouched: the stored url travels, nothing is re-encoded).
	Scale float64
	// KeepAlpha: the source carries real transparency. EncodePNG: the composite is a PNG (alpha, or a
	// PNG source — so a PNG original's pixels come back byte-exact); otherwise JPEG q92.
	KeepAlpha bool
	EncodePNG bool
}

// ExtendTargetAddsNothing — THE SAME TEST the worker's plan applies, for the door: true when ratio
// is within extendSameRatioTolerance of w:h (or w/h is unknown). ok = false for a ratio outside the
// nine.
func ExtendTargetAddsNothing(w, h int, ratio string) (nothing, ok bool) {
	r, ok := entity.DesignExtendRatioValue(ratio)
	if !ok {
		return false, false
	}
	if w <= 0 || h <= 0 {
		return false, true
	}
	_, _, _, _, adds := extendGeometry(w, h, r)
	return !adds, true
}

// extendGeometry — the canvas of a w×h source at target ratio r (width / height): the side that
// grows is the one the ratio asks for, the source is centred on it, the odd pixel goes right/bottom.
// adds = false when the target is the source's own proportion or rounds to it.
func extendGeometry(w, h int, r float64) (cw, ch, left, top int, adds bool) {
	src := float64(w) / float64(h)
	if math.Abs(r-src)/src <= extendSameRatioTolerance {
		return w, h, 0, 0, false
	}
	cw, ch = w, h
	if r > src {
		cw = int(math.Round(float64(h) * r))
	} else {
		ch = int(math.Round(float64(w) / r))
	}
	if cw <= w && ch <= h {
		return w, h, 0, 0, false
	}
	return cw, ch, (cw - w) / 2, (ch - h) / 2, true
}

// planExtend — THE WHOLE ARITHMETIC, one function, probed. The canvas over extendMaxPixels scales the
// SOURCE down (never the answer up) until the canvas fits; the loop takes the floor so the product
// never rounds back over the cap.
func planExtend(w, h int, r float64) (ExtendPlan, error) {
	if w <= 0 || h <= 0 || r <= 0 {
		return ExtendPlan{}, fmt.Errorf("%w: a %d×%d picture has no proportion", errExtendNothingToAdd, w, h)
	}
	sw, sh := w, h
	for i := 0; i < 16; i++ {
		cw, ch, left, top, adds := extendGeometry(sw, sh, r)
		if !adds {
			return ExtendPlan{}, fmt.Errorf("%w: the picture is %d×%d and the target is its own proportion",
				errExtendNothingToAdd, w, h)
		}
		if cw*ch <= extendMaxPixels {
			return ExtendPlan{
				Original:     image.Rect(0, 0, w, h),
				Source:       image.Rect(0, 0, sw, sh),
				Canvas:       image.Rect(0, 0, cw, ch),
				Offset:       image.Pt(left, top),
				ExpandLeft:   left,
				ExpandRight:  cw - sw - left,
				ExpandTop:    top,
				ExpandBottom: ch - sh - top,
				Scale:        float64(sw) / float64(w),
			}, nil
		}
		s := math.Sqrt(float64(extendMaxPixels) / float64(cw*ch))
		sw = int(math.Max(1, math.Floor(float64(sw)*s)))
		sh = int(math.Max(1, math.Floor(float64(sh)*s)))
	}
	return ExtendPlan{}, fmt.Errorf("%w: no canvas under %d pixels could be planned for %d×%d",
		errFreeformJobTooLarge, extendMaxPixels, w, h)
}

// extendScaledSource — THE downscale, one function for the body and for the composite, so the pixels
// pasted back are the pixels the model extended around.
func extendScaledSource(src image.Image, size image.Point) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

// deriveExtendPlan freezes job.Extend and, when the canvas cap scaled the source down, replaces
// job.References[0] with the downscaled source as a data URI (the provider must extend THE pixels
// the composite pastes). Otherwise the stored url travels unchanged. Free and terminal on refusal.
func deriveExtendPlan(ctx context.Context, objects objectFetcher, p runParams, job *Job) error {
	if len(job.References) == 0 {
		return fmt.Errorf("%w: the picture to extend could not be read any more", errFreeformSourceGone)
	}
	if len(job.References) != 1 {
		return fmt.Errorf("%w: an extend is one picture, and this run carries %d", fal.ErrBadRequest, len(job.References))
	}
	ratio := ""
	if p.Extend != nil {
		ratio = strings.TrimSpace(p.Extend.AspectRatio)
	}
	r, ok := entity.DesignExtendRatioValue(ratio)
	if !ok {
		return fmt.Errorf("%w: the frozen extend target %q is not one of %s", fal.ErrBadOption, ratio,
			strings.Join(entity.DesignExtendRatios(), " "))
	}
	if objects == nil {
		return fmt.Errorf("designgen: an extend run needs the picture it extends, and this worker has no object store")
	}
	srcURL := job.References[0]
	src, isPNG, err := fetchStoredPicture(ctx, objects, srcURL)
	if err != nil {
		return fmt.Errorf("designgen: cannot read the picture to extend: %w", err)
	}
	b := src.Bounds()
	if b.Dx() < windowMinSource || b.Dy() < windowMinSource {
		return fmt.Errorf("%w: it is %d×%d px, and an extend needs at least %d px on each side",
			errFreeformSourceTooSmall, b.Dx(), b.Dy(), windowMinSource)
	}
	plan, err := planExtend(b.Dx(), b.Dy(), r)
	if err != nil {
		return err
	}
	plan.Original = b
	plan.SourceURL = srcURL
	plan.KeepAlpha = freeformSourceHasAlpha(src)
	plan.EncodePNG = plan.KeepAlpha || isPNG
	if plan.Source.Size() != b.Size() {
		scaled := extendScaledSource(src, plan.Source.Size())
		var buf bytes.Buffer
		mt := "image/jpeg"
		if plan.KeepAlpha {
			mt = "image/png"
			err = png.Encode(&buf, scaled)
		} else {
			err = jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: freeformJPEGQuality})
		}
		if err != nil {
			return fmt.Errorf("designgen: cannot encode the scaled picture to extend: %w", err)
		}
		uri := freeformDataURI(mt, buf.Bytes())
		if len(uri) > fal.MaxDataURIBytes {
			return fmt.Errorf("%w: the picture to extend is %d bytes of inline data at %d×%d, against a "+
				"ceiling of %d — use a smaller picture", errFreeformJobTooLarge, len(uri),
				plan.Source.Dx(), plan.Source.Dy(), fal.MaxDataURIBytes)
		}
		job.References[0] = uri
	}
	job.Extend = &plan
	return nil
}

// fetchStoredPicture reads a bucket object by its STORED url (same key rule and byte ceiling as
// freeformFetchImage) and says whether its bytes are a PNG — which decides the composite's format.
func fetchStoredPicture(ctx context.Context, objects objectFetcher, rawURL string) (image.Image, bool, error) {
	raw, err := fetchStoredBytes(ctx, objects, rawURL)
	if err != nil {
		return nil, false, err
	}
	img, err := freeformDecode(raw)
	if err != nil {
		return nil, false, err
	}
	return img, isPNGBytes(raw), nil
}

// fetchStoredBytes — the bytes of a bucket object by its stored url, ≤ freeformMaxSourceBytes.
func fetchStoredBytes(ctx context.Context, objects objectFetcher, rawURL string) ([]byte, error) {
	key, err := bucket.ObjectKeyFromStoredURL(rawURL)
	if err != nil {
		return nil, err
	}
	rc, size, err := objects.GetManagedObject(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("object %q: %w", key, err)
	}
	defer rc.Close()
	if size > freeformMaxSourceBytes {
		return nil, fmt.Errorf("object %q is %d bytes, over the %d ceiling", key, size, freeformMaxSourceBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, freeformMaxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read object %q: %w", key, err)
	}
	if len(raw) > freeformMaxSourceBytes {
		return nil, fmt.Errorf("object %q is over the %d byte ceiling", key, freeformMaxSourceBytes)
	}
	return raw, nil
}

func isPNGBytes(raw []byte) bool {
	return len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'})
}

// ─────────────────────────── the route ───────────────────────────

// falOutpaintProvider is tile 9's route: Provider + Collector, submit = payment, collect = free.
type falOutpaintProvider struct{ c *fal.Client }

// NewFalOutpaintProvider wires kind=extend. A nil client is a disabled route, not a panic.
func NewFalOutpaintProvider(c *fal.Client) Provider { return falOutpaintProvider{c: c} }

func (p falOutpaintProvider) Name() string { return providerNameFalOutpaint }

func (p falOutpaintProvider) Enabled() bool { return p.c != nil && p.c.Enabled() }

// MissingCredential — the same key as every fal route; naming a slug variable here would send the
// owner to type a model where a key is missing.
func (p falOutpaintProvider) MissingCredential() string { return "FAL_KEY is not set" }

// Produces — what the composite may encode (PNG for alpha/PNG sources, JPEG otherwise) and what the
// provider may return; the pre-flight must be able to store both.
func (p falOutpaintProvider) Produces() []string { return []string{ContentTypePNG, ContentTypeJPEG} }

// SentPrompt — NOTHING: the outpaint body carries no words (the door refuses `ask` on this kind:
// extend_takes_no_words), so the history row must not claim any (PromptCarrier; the 3D lesson).
func (p falOutpaintProvider) SentPrompt(Job) string { return "" }

// outpaintFamily — which body a slug takes. ONLY the two families whose bodies were read on the
// provider's pages (2026-09-27); any other slug is refused before the submit — no guessing a body
// with money on it.
func outpaintFamily(model string) string {
	switch {
	case strings.HasPrefix(model, "fal-ai/flux-2-pro/outpaint"):
		return "flux"
	case strings.HasPrefix(model, "fal-ai/bria/expand"):
		return "bria"
	}
	return ""
}

// outpaintBody builds the request for the configured slug.
//   - flux (https://fal.ai/models/fal-ai/flux-2-pro/outpaint/api): image_url, expand_top/bottom/
//     left/right (int px), auto_crop (false: the canvas must contain the source whole), mode
//     "high", output_format "png" (the composite decides the stored format);
//   - bria (https://fal.ai/models/fal-ai/bria/expand/api): image_url, canvas_size [W,H],
//     original_image_size [w,h], original_image_location [x,y] (upper-left). No aspect_ratio: it
//     lacks 21:9 / 9:21 and would override the placement.
func outpaintBody(model, imageURL string, plan ExtendPlan) (any, error) {
	switch outpaintFamily(model) {
	case "flux":
		return map[string]any{
			"image_url":     imageURL,
			"expand_top":    plan.ExpandTop,
			"expand_bottom": plan.ExpandBottom,
			"expand_left":   plan.ExpandLeft,
			"expand_right":  plan.ExpandRight,
			"auto_crop":     false,
			"mode":          "high",
			"output_format": "png",
		}, nil
	case "bria":
		return map[string]any{
			"image_url":               imageURL,
			"canvas_size":             []int{plan.Canvas.Dx(), plan.Canvas.Dy()},
			"original_image_size":     []int{plan.Source.Dx(), plan.Source.Dy()},
			"original_image_location": []int{plan.Offset.X, plan.Offset.Y},
		}, nil
	}
	return nil, fmt.Errorf("%w: FAL_MODEL_OUTPAINT %q is neither %s nor fal-ai/bria/expand — this route builds "+
		"no body for a slug it has not read", fal.ErrBadOption, model, fal.DefaultModelOutpaint)
}

// Execute SUBMITS the frozen plan and returns at once with the request id.
func (p falOutpaintProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	if job.Extend == nil || len(job.References) != 1 {
		return nil, fmt.Errorf("%w: an extend run reached its route without its plan or its one picture",
			fal.ErrBadRequest)
	}
	model := p.c.ModelFor(fal.RouteOutpaint)
	body, err := outpaintBody(model, job.References[0], *job.Extend)
	if err != nil {
		return nil, err
	}
	id, err := p.c.SubmitJSON(ctx, model, body)
	if err != nil {
		if out := chargedRouteOutcome(p.c, fal.RouteOutpaint, err); out != nil {
			return out, err
		}
		return nil, err
	}
	return &Outcome{RequestID: id, Model: model, Pending: true}, nil
}

// Collect is the FREE half: the wait, the download, the price. The composite is postProcess's.
func (p falOutpaintProvider) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	return collectRouteFile(ctx, p.c, fal.RouteOutpaint, job, requestID)
}

// pickAnyImage reads `images[0]` (flux) and falls back to `image` (bria): the collect may run under
// a slug other than the submit's (a FAL_MODEL_* move between the two), and reading both shapes
// costs nothing where reading the wrong one loses a paid file.
func pickAnyImage(body json.RawMessage) (string, string, error) {
	u, ct, err := fal.PickImages0(body)
	if err == nil || !errors.Is(err, fal.ErrNoFile) {
		return u, ct, err
	}
	return fal.PickImage(body)
}

// collectRouteFile — the shared collect of the generic routes: one file, priced by the route's
// tariff (CostRouteUSD), the assumption and an over-ceiling charge said out loud.
func collectRouteFile(ctx context.Context, c *fal.Client, route fal.Route, job Job, requestID string) (*Outcome, error) {
	var buf bytes.Buffer
	res, err := c.CollectFile(ctx, c.ModelFor(route), requestID, pickAnyImage, &buf, 0)
	if err != nil {
		if out := chargedRouteOutcome(c, route, err); out != nil {
			return out, err
		}
		return nil, err
	}
	out := &Outcome{RequestID: res.RequestID, Model: res.Model}
	if usd := c.CostRouteUSD(route, res.BillableUnits); usd.IsPositive() {
		out.Price = decimal.NullDecimal{Decimal: usd, Valid: true}
	}
	if res.UnitsAssumed {
		slog.Default().WarnContext(ctx, string(route)+": fal reported no billable units; the attempt's price "+
			"is this deployment's own per-request figure, not the provider's charge",
			slog.Int("run_id", job.RunID), slog.String("request_id", res.RequestID),
			slog.String("price_usd", out.Price.Decimal.String()), slog.String("knob", route.UnitUSDEnv()))
	}
	if ceiling, ok := c.RouteUnitsCeiling(route); ok && res.BillableUnits > ceiling {
		slog.Default().ErrorContext(ctx, string(route)+": fal billed more units than the stated ceiling; the "+
			"reservation was short of this charge",
			slog.Int("run_id", job.RunID), slog.String("request_id", res.RequestID),
			slog.Float64("units", res.BillableUnits), slog.Float64("ceiling", ceiling),
			slog.String("knob", route.UnitsCeilingEnv()))
	}
	raw := buf.Bytes()
	out.Artifacts = append(out.Artifacts, Artifact{Bytes: raw, ContentType: cutoutContentType(raw)})
	return out, nil
}

// chargedRouteOutcome — a billed failure's Outcome, or nil when nobody said what it cost (NULL is not
// zero; see chargedCutoutOutcome).
func chargedRouteOutcome(c *fal.Client, route fal.Route, err error) *Outcome {
	var ce *fal.ChargedError
	if !errors.As(err, &ce) {
		return nil
	}
	usd := c.CostRouteUSD(route, ce.Units)
	if !usd.IsPositive() {
		return nil
	}
	return &Outcome{RequestID: ce.RequestID, Model: ce.Model, Price: decimal.NullDecimal{Decimal: usd, Valid: true}}
}

// ─────────────────────────── the composite ───────────────────────────

// compositeExtendInto re-reads the source and pastes it over the bought canvas. A complaint, never a
// refusal: the money is spent, and the canvas is useful as it came.
func (w *Worker) compositeExtendInto(ctx context.Context, plan ExtendPlan, out *Outcome) error {
	if w.objects == nil {
		return fmt.Errorf("%w: this worker has no object store to read the source from", errExtendNotComposited)
	}
	src, _, err := fetchStoredPicture(ctx, w.objects, plan.SourceURL)
	if err != nil {
		return fmt.Errorf("%w: the source could not be read back: %v", errExtendNotComposited, err)
	}
	fitted, err := compositeExtend(src, plan, out.Artifacts[0].Bytes)
	if err != nil {
		return fmt.Errorf("%w: %v", errExtendNotComposited, err)
	}
	out.Artifacts[0] = fitted
	return nil
}

// compositeExtend draws the provider's canvas, then the SOURCE over it with draw.Src — the original
// pixels REPLACE the provider's copy of them (Src, not Over: a translucent source must stay exactly
// as translucent as it was, not blended with whatever the model painted beneath it).
func compositeExtend(src image.Image, plan ExtendPlan, answer []byte) (Artifact, error) {
	if src.Bounds() != plan.Original {
		return Artifact{}, fmt.Errorf("the source read back is %v, and the plan was made against %v",
			src.Bounds(), plan.Original)
	}
	got, err := freeformDecode(answer)
	if err != nil {
		return Artifact{}, fmt.Errorf("the answer is not a readable picture: %w", err)
	}
	var source image.Image = src
	if plan.Source.Size() != src.Bounds().Size() {
		source = extendScaledSource(src, plan.Source.Size())
	}

	dst := image.NewNRGBA(plan.Canvas)
	ab := got.Bounds()
	if ab.Size() == plan.Canvas.Size() {
		draw.Draw(dst, dst.Bounds(), got, ab.Min, draw.Src)
	} else {
		if !extendNearSize(ab.Dx(), plan.Canvas.Dx()) || !extendNearSize(ab.Dy(), plan.Canvas.Dy()) {
			return Artifact{}, fmt.Errorf("the answer is %d×%d and the canvas was planned at %d×%d — "+
				"another size; the canvas is kept as delivered", ab.Dx(), ab.Dy(),
				plan.Canvas.Dx(), plan.Canvas.Dy())
		}
		// A model that rounded its canvas (a multiple of 16 px): fitted to the plan, then the source
		// goes on top exactly where it was planned.
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), got, ab, xdraw.Src, nil)
	}
	sb := source.Bounds()
	draw.Draw(dst, plan.Source.Add(plan.Offset), source, sb.Min, draw.Src)

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

// extendNearSize — a delivered side within max(16 px, 2 %) of the planned one.
func extendNearSize(got, want int) bool {
	slack := math.Max(extendAnswerSlackPx, extendAnswerSlackFraction*float64(want))
	return got > 0 && math.Abs(float64(got-want)) <= slack
}

// ─────────────────────────── the route object (band, door, reserve) ───────────────────────────

// FalRoute — ONE object the band, the door and the reserve read for a fal JSON route (the G-02
// lesson: a route's capability is one value, not three readings). Built from the SAME fal.Client
// the worker's provider gets.
type FalRoute struct {
	Kind  string
	Route fal.Route
	Model string
	// Ceiling is what the door reserves for ONE run; Bounded = false means there is no such number
	// (a tariff without its units ceiling) and the door refuses the kind: route_reserve_unbounded.
	Ceiling decimal.Decimal
	Bounded bool
	// Unbounded is the sentence the refusal says (the two variables named); '' when Bounded.
	Unbounded string
	// Flag is the units-ceiling variable to set.
	Flag string
}

// FalRouteOf — the route object of kind extend (outpaint) or inpaint (fill); ok = false for any other.
func FalRouteOf(c *fal.Client, kind string) (FalRoute, bool) {
	var r fal.Route
	switch kind {
	case entity.DesignRunKindExtend:
		r = fal.RouteOutpaint
	case entity.DesignRunKindInpaint:
		r = fal.RouteFill
	default:
		return FalRoute{}, false
	}
	ceiling, bounded := c.RouteCeilingUSD(r)
	out := FalRoute{Kind: kind, Route: r, Model: c.ModelFor(r), Ceiling: ceiling, Bounded: bounded,
		Flag: r.UnitsCeilingEnv()}
	if !bounded {
		out.Unbounded = r.UnitUSDEnv() + " is set and " + r.UnitsCeilingEnv() + " is not: a run would book " +
			r.UnitUSDEnv() + " × whatever units fal reports, and no reservation can cover that. Set " +
			r.UnitsCeilingEnv() + ", or unset " + r.UnitUSDEnv() + " to book the route's own per-request ceiling"
	}
	return out, true
}
