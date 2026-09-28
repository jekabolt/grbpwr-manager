package designgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/shopspring/decimal"
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
	// KeepAlpha: the source carries real transparency (said for the record; the composite is a
	// lossless PNG whatever the source was — see encodeComposite).
	KeepAlpha bool
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
	return planExtendUnder(w, h, r, extendMaxPixels)
}

// planExtendUnder — planExtend under a canvas cap of maxPixels (≤ extendMaxPixels). deriveExtendPlan
// lowers the cap only when the scaled source would not fit in one inline data URI (G-03, Codex 9).
func planExtendUnder(w, h int, r float64, maxPixels int) (ExtendPlan, error) {
	if maxPixels <= 0 || maxPixels > extendMaxPixels {
		maxPixels = extendMaxPixels
	}
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
		if cw*ch <= maxPixels {
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
		s := math.Sqrt(float64(maxPixels) / float64(cw*ch))
		sw = int(math.Max(1, math.Floor(float64(sw)*s)))
		sh = int(math.Max(1, math.Floor(float64(sh)*s)))
	}
	return ExtendPlan{}, fmt.Errorf("%w: no canvas under %d pixels could be planned for %d×%d",
		errFreeformJobTooLarge, maxPixels, w, h)
}

// extendInlineCap — the largest data URI the scaled source may travel as: fal.MaxDataURIBytes. A var
// only so a probe can reach the shrink loop with a small picture.
var extendInlineCap = fal.MaxDataURIBytes

// extendShrinkTries — how many times deriveExtendPlan lowers the canvas cap to fit the inline URI.
const extendShrinkTries = 6

// extendScaledSource — THE downscale, one function for the body and for the composite, so the pixels
// pasted back are the pixels the model extended around.
//
// leanScale, not Kernel.Scale (G-03 r2, Codex 1): the separable Scale allocates size.X × source
// height × 32 bytes of scratch (≈ 180 MB for a 40 MP source), before and after the payment alike.
func extendScaledSource(src image.Image, size image.Point) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))
	leanScale(dst, dst.Bounds(), src, src.Bounds())
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
	// The header against CompositeMaxSourcePixels before a pixel is decoded (G-03 r2, Codex 1): free,
	// terminal source_too_large — the door's second lock, for a row with no stored dimensions.
	srcRaw, err := fetchStoredBytes(ctx, objects, srcURL)
	if err != nil {
		return fmt.Errorf("designgen: cannot read the picture to extend: %w", err)
	}
	src, err := decodeCompositeSource(srcRaw)
	if err != nil {
		if errors.Is(err, errFreeformSourceTooLarge) {
			return err
		}
		return fmt.Errorf("designgen: cannot read the picture to extend: %w", err)
	}
	b := src.Bounds()
	if b.Dx() < windowMinSource || b.Dy() < windowMinSource {
		return fmt.Errorf("%w: it is %d×%d px, and an extend needs at least %d px on each side",
			errFreeformSourceTooSmall, b.Dx(), b.Dy(), windowMinSource)
	}
	keepAlpha := freeformSourceHasAlpha(src)
	// ⚠ THE SCALED SOURCE TRAVELS AS A LOSSLESS PNG, ALPHA OR NOT (G-03, Codex 4). The composite
	// pastes extendScaledSource(src, plan.Source) back over the canvas; the provider must have extended
	// around EXACTLY those pixels, and a JPEG of them (the old opaque path) is a different raster — a
	// seam along the paste line. PNG is bigger, so the canvas cap is lowered until the URI fits
	// (G-03, Codex 9): the worker never refuses a picture for its inline size before trying smaller,
	// and whatever it still refuses is refused here, in buildJob, before StartAttempt — free.
	limit := extendMaxPixels
	for try := 0; ; try++ {
		plan, err := planExtendUnder(b.Dx(), b.Dy(), r, limit)
		if err != nil {
			return err
		}
		plan.Original = b
		plan.SourceURL = srcURL
		plan.KeepAlpha = keepAlpha
		if plan.Source.Size() == b.Size() {
			// Untouched: the stored url travels, nothing is re-encoded, no base64 in the body.
			job.Extend = &plan
			return nil
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, extendScaledSource(src, plan.Source.Size())); err != nil {
			return fmt.Errorf("designgen: cannot encode the scaled picture to extend: %w", err)
		}
		uri := freeformDataURI("image/png", buf.Bytes())
		if len(uri) <= extendInlineCap {
			job.References[0] = uri
			job.Extend = &plan
			return nil
		}
		if try+1 >= extendShrinkTries {
			return fmt.Errorf("%w: the picture to extend is still %d bytes of inline data at %d×%d after %d "+
				"smaller tries, against a ceiling of %d — use a smaller picture. Nothing was sent and nothing "+
				"was charged", errFreeformJobTooLarge, len(uri), plan.Source.Dx(), plan.Source.Dy(), try+1,
				extendInlineCap)
		}
		// A PNG's size follows its pixel count: lower the canvas by the overshoot, with a margin.
		limit = int(float64(plan.Canvas.Dx()*plan.Canvas.Dy()) * float64(extendInlineCap) / float64(len(uri)) * 0.9)
	}
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
func (p falOutpaintProvider) MissingCredential() string { return noKeySentence("fal", "FAL_KEY") }

// Produces — what may be stored: the composite's lossless PNG, its JPEG fallback for a picture too
// large for a PNG in the bucket (encodeComposite), and whatever the provider returned when the
// composite could not be made; the pre-flight must be able to store both.
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
	// The route row's slug (job.Model, B-24) before FAL_MODEL_OUTPAINT; a slug outpaintBody has not read
	// is refused below, free — the door's FalRoute.Unsupported is the same family test.
	model := firstNonEmpty(job.Model, p.c.ModelFor(fal.RouteOutpaint))
	if err := falLocatorFits(model); err != nil {
		return nil, err
	}
	body, err := outpaintBody(model, job.References[0], *job.Extend)
	if err != nil {
		return nil, err
	}
	// THE SUBMIT IS THE PAYMENT, SO IT OPENS THE LEDGER ROW (B-07) — see threedfal.go.
	h := job.beginCall(ctx, entity.AIProviderFal, model, 1)
	id, err := p.c.SubmitJSON(ctx, model, body)
	if err != nil {
		if out := chargedRouteOutcome(p.c, fal.RouteOutpaint, job, err); out != nil {
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

// ─── THE REQUEST LOCATOR (G-03, Codex 2) ───
//
// A fal request is found by TWO facts: the id and the namespace it was queued in (queuePath of the
// slug it was SUBMITTED to). The attempt row has one column for them — provider_request_id — so the
// accepted submit stores both, "<slug>#<id>", and the collect polls exactly that slug, whatever
// FAL_MODEL_OUTPAINT / FAL_MODEL_FILL say by the time it runs (a slug move between the submit and a
// resume used to poll bria's namespace for a flux request and lose the paid file). The collect row
// reports the same locator, so chargeAlreadyBooked keys one charge by one string. The 3D route stores
// the same form since G-03 r2. A bare id (a row written before this) is read against the current
// slug and then the route's known namespaces (fal.RouteLegacyModels / CutoutLegacyModels /
// retired3D) — G-03 r2, Codex 4.

const falLocatorSep = "#"

// falLocatorMaxSlug — the longest slug a locator carries: provider_request_id is VARCHAR(128) and a
// fal request id is a 36-character UUID. A longer FAL_MODEL_* is refused before the submit (free),
// never discovered as a failed accepted-write after it.
const falLocatorMaxSlug = 80

func falLocator(model, id string) string { return model + falLocatorSep + id }

// splitFalLocator — (slug, id); slug "" for a bare id.
func splitFalLocator(s string) (model, id string) {
	if i := strings.LastIndex(s, falLocatorSep); i > 0 && i < len(s)-1 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// providerRequestIDMax — design_run_attempt.provider_request_id is VARCHAR(128)
// (internal/store/sql/0340_design_runs.sql). Bytes are counted: a fal locator is ASCII, and a byte
// count can only be stricter than MySQL's character count.
const providerRequestIDMax = 128

// fitRequestLocator is the accepted locator as it will FIT the column (G-03 r2, Codex 8).
//
// falLocatorFits checks the SLUG before the submit; the id is fal's, and fal's schemas type it as a
// string, not as a 36-character UUID. A longer id arriving AFTER the charge used to make the
// accepted write fail — a paid job turned manual-reconcile over a column width. So the locator
// degrades, each step still resumable, and says so at ERROR level with the full value and the run:
//
//  1. "<slug>#<id>" — the whole locator;
//  2. "<namespace>#<id>" — the queue namespace (fal.QueueNamespace) is all a poll needs: the status
//     and result paths are built from it, never from the full slug (a 3D price read off it falls
//     back to the configured tariff);
//  3. "<id>" — a bare id: the collect polls today's slug and then the route's known namespaces
//     (the legacy path, collectRouteFile / CollectCutoutAt / fal.Await);
//  4. nothing fits — the locator is returned whole, the accepted write refuses it, and the run fails
//     closed (errAcceptedNotRecorded) with the id in last_error and in this log line.
func fitRequestLocator(ctx context.Context, runID int, locator string) string {
	if len(locator) <= providerRequestIDMax {
		return locator
	}
	stored := locator
	model, id := splitFalLocator(locator)
	switch ns := fal.QueueNamespace(model); {
	case model != "" && ns != "" && len(ns)+len(falLocatorSep)+len(id) <= providerRequestIDMax:
		stored = ns + falLocatorSep + id
	case len(id) <= providerRequestIDMax:
		stored = id
	}
	slog.Default().ErrorContext(ctx, "design: an accepted request locator is longer than provider_request_id "+
		"holds; a shorter, still resumable form is stored — keep this line to reconcile the paid job",
		slog.Int("run_id", runID), slog.String("locator", locator), slog.String("stored", stored),
		slog.Int("column_max", providerRequestIDMax))
	return stored
}

func falLocatorFits(model string) error {
	if len(model) > falLocatorMaxSlug {
		return fmt.Errorf("%w: the slug %q is %d characters, and a request is remembered by slug and id in "+
			"%d — this route will not submit what it could not resume", fal.ErrBadOption, model, len(model),
			falLocatorMaxSlug+1+36)
	}
	return nil
}

// collectRouteFile — the shared collect of the generic routes: one file, polled at the slug the job
// was SUBMITTED to (the locator), priced by the route's tariff (CostRouteUSD), the assumption and a
// booking over the reservation said out loud.
func collectRouteFile(ctx context.Context, c *fal.Client, route fal.Route, job Job, locator string) (*Outcome, error) {
	model, id := splitFalLocator(locator)
	models := []string{model}
	if model == "" {
		// A BARE id — written before the locator existed (G-03 r2, Codex 4): today's slug first,
		// then the namespaces this route is known to have used, instead of today's slug alone.
		models = c.RouteLegacyModels(route)
	}
	var buf bytes.Buffer
	res, err := c.CollectFileSearching(ctx, models, id, pickAnyImage, &buf, 0)
	if err != nil {
		if out := chargedRouteOutcome(c, route, job, err); out != nil {
			out.RequestID = locator
			job.recordCollect(ctx, out, err, chargedUnits(err), "unit", true)
			return out, err
		}
		// A charge nobody could price is still a charge; otherwise still running, or failed for good.
		_, charged := fal.Charge(err)
		job.recordCollect(ctx, nil, err, chargedUnits(err), "unit", charged)
		return nil, err
	}
	out := &Outcome{RequestID: locator, Model: res.Model, Provider: entity.AIProviderFal}
	if usd := c.CostRouteUSD(route, res.BillableUnits); usd.IsPositive() {
		out.Price = decimal.NullDecimal{Decimal: usd, Valid: true}
	}
	// DELIVERED: the submit's `accepted` row is priced with the number the attempt books (B-07) —
	// before the composite (postProcess), whose trouble is the attempt's complaint, not the call's.
	job.recordCollect(ctx, out, nil, reportedUnits(res.BillableUnits, res.UnitsAssumed), "unit", false)
	if res.UnitsAssumed {
		slog.Default().WarnContext(ctx, string(route)+": fal reported no billable units; the attempt's price "+
			"is this deployment's own per-request figure, not the provider's charge",
			slog.Int("run_id", job.RunID), slog.String("request_id", res.RequestID),
			slog.String("price_usd", out.Price.Decimal.String()), slog.String("knob", route.UnitUSDEnv()))
	}
	logRouteChargeOverReserve(ctx, c, route, job, locator, res.BillableUnits, out.Price)
	raw := buf.Bytes()
	out.Artifacts = append(out.Artifacts, Artifact{Bytes: raw, ContentType: cutoutContentType(raw)})
	return out, nil
}

// logRouteChargeOverReserve — what the collect says when a booking may not fit what the run reserved
// (G-03, Codex 5 + 10; the G-02 r2 wording of logThreedCeilingBreach). Two claims, and only one of
// them follows from the units: the door reserves max(the kind's table, tariff × ceiling), so units
// over FAL_UNITS_CEILING_* say only that the CEILING is wrong (the table may still cover the booking).
// «The reservation was below its booking» is said — at ERROR — only when the booked money really
// exceeds the run's own reserve; a provider that drew a bigger canvas than planned lands here too.
func logRouteChargeOverReserve(ctx context.Context, c *fal.Client, route fal.Route, job Job, locator string,
	units float64, booked decimal.NullDecimal) {
	attrs := []any{
		slog.Int("run_id", job.RunID), slog.String("request_id", locator),
		slog.Float64("units", units), slog.String("booked_usd", booked.Decimal.String()),
	}
	if job.RouteReservedUSD.Valid && booked.Valid && booked.Decimal.GreaterThan(job.RouteReservedUSD.Decimal) {
		slog.Default().ErrorContext(ctx, string(route)+": the booked charge is above this run's reservation — "+
			"the reservation was below its booking; raise "+route.UnitsCeilingEnv()+" or check the canvas fal drew",
			append(attrs, slog.String("reserved_usd", job.RouteReservedUSD.Decimal.String()))...)
		return
	}
	if ceiling, ok := c.RouteUnitsCeiling(route); ok && units > ceiling {
		slog.Default().WarnContext(ctx, string(route)+": fal billed more units than "+route.UnitsCeilingEnv()+
			" — raise the ceiling (this run's reservation still covered the booking, or no reservation was recorded)",
			append(attrs, slog.Float64("ceiling", ceiling))...)
	}
}

// chargedRouteOutcome — a billed failure's Outcome, or nil when nobody said what it cost (NULL is not
// zero; see chargedCutoutOutcome).
//
// ⚠ AN ASSUMED UNIT IS BOOKED AT THE ROUTE'S CEILING (G-03, Codex 3). A failure after a 2xx result
// with no billing header carries fal's one assumed unit; under a tariff that is `tariff × 1`, which may
// be well under what fal really billed for a multi-megapixel job. The conservative number is the one
// the door reserved per run: the route's own bounded ceiling.
func chargedRouteOutcome(c *fal.Client, route fal.Route, job Job, err error) *Outcome {
	var ce *fal.ChargedError
	if !errors.As(err, &ce) {
		return nil
	}
	usd := c.CostRouteUSD(route, ce.Units)
	if ce.Assumed {
		if ceiling, ok := c.RouteCeilingUSD(route); ok && ceiling.GreaterThan(usd) {
			usd = ceiling
		}
	}
	if !usd.IsPositive() {
		return nil
	}
	return &Outcome{RequestID: ce.RequestID, Model: ce.Model, Price: decimal.NullDecimal{Decimal: usd, Valid: true}}
}

// ─────────────────────────── the composite ───────────────────────────

// compositeExtendInto re-reads the source and pastes it over the bought canvas. A complaint, never a
// refusal: the money is spent, and the canvas is useful as it came.
//
// Bounded like the inpaint composite (G-03 r2, Codex 1; composite_budget.go): the source's header is
// capped before its pixels are decoded, the answer's header is checked against the planned canvas
// before its pixels are, the source is scaled without a scratch buffer and dropped before the canvas
// is made, and the encode stops at the bucket's ceiling.
func (w *Worker) compositeExtendInto(ctx context.Context, plan ExtendPlan, out *Outcome) error {
	if w.objects == nil {
		return fmt.Errorf("%w: this worker has no object store to read the source from", errExtendNotComposited)
	}
	raw, err := fetchStoredBytes(ctx, w.objects, plan.SourceURL)
	if err != nil {
		return fmt.Errorf("%w: the source could not be read back: %v", errExtendNotComposited, err)
	}
	src, err := decodeCompositeSource(raw)
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
	acfg, err := freeformDecodeConfig(answer)
	if err != nil {
		return Artifact{}, fmt.Errorf("the answer is not a readable picture: %w", err)
	}
	if (acfg.Width != plan.Canvas.Dx() || acfg.Height != plan.Canvas.Dy()) &&
		(!extendNearSize(acfg.Width, plan.Canvas.Dx()) || !extendNearSize(acfg.Height, plan.Canvas.Dy())) {
		return Artifact{}, fmt.Errorf("the answer is %d×%d and the canvas was planned at %d×%d — "+
			"another size; the canvas is kept as delivered", acfg.Width, acfg.Height,
			plan.Canvas.Dx(), plan.Canvas.Dy())
	}
	var source image.Image = src
	if plan.Source.Size() != src.Bounds().Size() {
		source = extendScaledSource(src, plan.Source.Size())
		releaseHeap()
	}
	got, err := freeformDecode(answer)
	if err != nil {
		return Artifact{}, fmt.Errorf("the answer is not a readable picture: %w", err)
	}

	dst := image.NewNRGBA(plan.Canvas)
	ab := got.Bounds()
	if ab.Size() == plan.Canvas.Size() {
		draw.Draw(dst, dst.Bounds(), got, ab.Min, draw.Src)
	} else {
		// A model that rounded its canvas (a multiple of 16 px): fitted to the plan, then the source
		// goes on top exactly where it was planned.
		leanScale(dst, dst.Bounds(), got, ab)
	}
	sb := source.Bounds()
	draw.Draw(dst, plan.Source.Add(plan.Offset), source, sb.Min, draw.Src)
	return encodeComposite(dst, plan.KeepAlpha)
}

// encodeComposite, the stored format of a composite and its bounded fallbacks: composite_budget.go.

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
	// Flag is the variable to set: the units ceiling, or — Unsupported — the slug.
	Flag string
	// Unsupported — the configured slug is not a family this route builds a body for (G-03, Fable
	// m-1). Bounded is false with it, so the band never advertises the tile, and the door refuses it
	// as kind_not_available instead of reserving a run every press of which would fail.
	Unsupported bool
}

// falRouteSlugSupported — the configured slug is a family whose body was read on the provider's pages.
func falRouteSlugSupported(r fal.Route, model string) bool {
	switch r {
	case fal.RouteOutpaint:
		return outpaintFamily(model) != ""
	case fal.RouteFill:
		return fillFamily(model) != ""
	}
	return false
}

func falRouteSlugs(r fal.Route) []string {
	if r == fal.RouteOutpaint {
		return []string{fal.DefaultModelOutpaint, "fal-ai/bria/expand"}
	}
	return []string{fal.DefaultModelFill}
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
	if !falRouteSlugSupported(r, out.Model) {
		out.Bounded, out.Unsupported, out.Flag = false, true, r.ModelEnv()
		out.Unbounded = r.ModelEnv() + " is " + strconv.Quote(out.Model) + ", a slug this route builds no request " +
			"body for — set it to one of " + strings.Join(falRouteSlugs(r), " | ") + ", or unset it"
		return out, true
	}
	if !bounded {
		out.Unbounded = r.UnitUSDEnv() + " is set and " + r.UnitsCeilingEnv() + " is not: a run would book " +
			r.UnitUSDEnv() + " × whatever units fal reports, and no reservation can cover that. Set " +
			r.UnitsCeilingEnv() + ", or unset " + r.UnitUSDEnv() + " to book the route's own per-request ceiling"
	}
	return out, true
}
