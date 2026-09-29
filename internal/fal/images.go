package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ THE IMAGE TRANSPORT (H3): fal draws image.generate ═══
//
// Images is the designgen.ImageTransport over this client: a route row `image.generate → fal / <fal
// endpoint id>` (primary, fallback or «default for images») is paid here — one submit, a free wait,
// one download — and answered in the shape the image route already reads (orimages.Result: one decoded
// picture and a cost). The queue is the GENERIC JSON route's (generic.go: SubmitJSON / CollectFile);
// this file only builds the body, picks the file and says what every failure costs.
//
// THE SLUG IS THE ENDPOINT ID, AND ANY WELL-FORMED ONE IS SERVED. fal serves hundreds of image
// endpoints and the pricing catalogue lists a handful — a hint list for the panel, not a whitelist.
// Serves asks only ValidSlug (the path rule, slug.go); an unknown-but-well-formed slug reaches fal, and
// fal's 404 / 422 at the submit comes back as a FREE, terminal CallError (callJSON: ClassifyStatus), so
// the route's chain advances to its next candidate without money having moved.
//
// ⚠ THE MONEY BOUNDARY IS THE SUBMIT'S request id (runblob's rule, images.go there). Before it every
// refusal is free: a bad slug, a reference fal could not fetch, a 4xx at the gate. After it the request
// is bought: a wait that runs out, a result fal will not serve, a completed request with no file, a
// download that dies — each is an ENGAGED, NON-RETRYABLE CallError naming the request id, so the run
// closes `unknown` and the chain never buys the picture a second time (D-16). An ambiguous submit
// (written, no usable answer) is already engaged in callJSON (ErrSubmitUnconfirmed).
//
// MONEY. fal reports x-fal-billable-units on the result fetch (CollectFile → FileResult.BillableUnits)
// and no dollars. When the catalogue prices (fal, slug) PER CALL, the cost is units × that price and is
// marked a TABLE number (Usage.CostSource = entity.AICostTable): the ledger books cost_source `table`
// with pricing.Version, never `provider`. With no per-call row (per-megapixel endpoints, a slug the
// catalogue does not list) Cost stays 0 and the call is booked unpriced — never a guess.

// DefaultModelImage is the transport's own default slug (FAL_MODEL_IMAGE overrides it): what a route
// row naming fal with no model draws. FLUX1.1 [pro] — fal's general text-to-image endpoint.
const DefaultModelImage = "fal-ai/flux-pro/v1.1"

// maxFalImageBytes caps ONE downloaded picture: runblob's MaxImageBytes reasoning (a 4k PNG straight
// off a CDN can pass the cut-out's 25 MiB). Refused at the limit, never trimmed.
const maxFalImageBytes = 48 << 20

// falImageSizes maps orimages.Request.AspectRatio onto fal's `image_size` preset enum (the FLUX family's
// names; fal's presets carry no 3:2 / 2:3 / 21:9). "auto" and "" send nothing: the endpoint's default.
var falImageSizes = map[string]string{
	"1:1":  "square_hd",
	"3:4":  "portrait_4_3",
	"4:3":  "landscape_4_3",
	"9:16": "portrait_16_9",
	"16:9": "landscape_16_9",
}

// falImageFormats — the output_format words passed through; any other word is not sent.
var falImageFormats = map[string]bool{"png": true, "jpeg": true, "webp": true}

// ModelImage is the effective default slug of the image transport: FAL_MODEL_IMAGE, else
// DefaultModelImage. Nil-safe.
func (c *Client) ModelImage() string {
	if c != nil {
		if m := normSlug(c.cfg.ModelImage); m != "" {
			return m
		}
	}
	return DefaultModelImage
}

// Images is the image transport over one Client. A nil *Images is valid and permanently disabled.
type Images struct {
	c *Client
	// lookup is the catalogue row of (provider, slug) — pricing.Lookup; a field so a test can price a
	// slug the shipped catalogue does not list.
	lookup func(provider, slug string) (pricing.Model, bool)
	// warned — the one-time log lines already said (unknown ratio, unsent dials per slug).
	warned sync.Map
}

// NewImages builds the image transport over c — the SAME client (and so the same panel key) the
// cut-out / outpaint / fill routes use. A nil c is a disabled transport.
func NewImages(c *Client) *Images {
	return &Images{c: c, lookup: pricing.Lookup}
}

// Model is the transport's own default slug (designgen.ImageTransport). Nil-safe.
func (t *Images) Model() string {
	if t == nil {
		return DefaultModelImage
	}
	return t.c.ModelImage()
}

// Enabled — the client holds a key right now (the registry's KeyFunc, read per call). Nil-safe.
func (t *Images) Enabled() bool { return t != nil && t.c.Enabled() }

// Serves reports whether slug is a well-formed fal endpoint id (see the section doc: a well-formed
// slug fal does not serve is fal's free 404, not ours to guess). "../x", a query, an OpenRouter
// "openai/GPT-Image-2" with capitals — false: the chooser skips the candidate before any row opens.
func (t *Images) Serves(slug string) bool {
	return t != nil && ValidSlug(normSlug(slug))
}

// Generate draws ONE picture: submit → wait → download (see the section doc).
//
// Mapping from orimages.Request: Model (empty = Model()) is the endpoint; Prompt goes as `prompt`;
// `num_images` is always 1 (one picture per call — the route's every call asks for one); AspectRatio
// becomes `image_size` through falImageSizes; OutputFormat png | jpeg | webp passes through;
// InputReferences go as `image_urls` on an edit endpoint (the slug contains "edit") and as `image_url`
// (the first; a warning names the dropped rest) elsewhere. Quality, Background, Resolution and
// OutputCompression have no counterpart shared by fal's endpoints and are not sent (said once per slug
// at debug).
func (t *Images) Generate(ctx context.Context, req orimages.Request) (*orimages.Result, error) {
	if !t.Enabled() {
		return nil, fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%w: %w", ErrNotConfigured, aiprov.ErrNotConfigured))
	}
	slug := normSlug(req.Model)
	if slug == "" {
		slug = t.Model()
	}
	if !ValidSlug(slug) {
		// The chooser never sends this (Serves said no); a direct caller hears it before the wire.
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%w: %q is not a fal endpoint id", ErrBadRequest, truncateText(slug, 80)))
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%w: a %s generation needs a prompt", ErrBadRequest, slug))
	}
	refs := make([]string, 0, len(req.InputReferences))
	for _, r := range req.InputReferences {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		// Checked HERE for every reference: SubmitJSON validates `*_url` keys, and `image_urls` is a
		// list under a key it does not read as one.
		if err := validateImageRef(r); err != nil {
			return nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("reference picture: %w", err))
		}
		refs = append(refs, r)
	}
	body := t.imageBody(ctx, slug, prompt, req, refs)

	id, err := t.c.SubmitJSON(ctx, slug, body)
	if err != nil {
		if _, spoke := aiprov.AsCallError(err); spoke {
			// callJSON's own classification: a 4xx / bare 503 at the gate is free; a written request
			// with no usable answer is engaged (ErrSubmitUnconfirmed) — either way already said.
			return nil, err
		}
		// SubmitJSON's local refusals (an over-cap data: URI, a bad `*_url`, the key gone between
		// Enabled and the submit): nothing left the process.
		code := aiprov.CodeBadRequest
		if errors.Is(err, ErrNotConfigured) {
			code = aiprov.CodeNotConfigured
		}
		return nil, fail(code, 0, false, false, err)
	}

	// ─── FROM HERE ON THE REQUEST IS BOUGHT: every failure below is engaged and terminal ───
	var buf bytes.Buffer
	fr, err := t.c.CollectFile(ctx, slug, id, pickFalImage, &buf, maxFalImageBytes)
	if err != nil {
		return t.chargedResult(slug, err), imageBought(err, id, slug, "its picture could not be collected")
	}
	raw := buf.Bytes()
	mediaType := falImageMediaType(fr.ContentType, raw)
	res := &orimages.Result{Model: slug}
	if cost := t.tableCost(slug, fr.BillableUnits); cost > 0 {
		res.Usage.Cost, res.Usage.CostSource = cost, entity.AICostTable
	}
	if !strings.HasPrefix(mediaType, "image/") {
		// Paid and not a picture: the charge (when the table prices it) rides the partial result.
		return costOnly(res), imageBought(fmt.Errorf("%w: the file is %q, not a picture", ErrUnexpectedResponse, mediaType),
			id, slug, "it delivered no picture")
	}
	res.Images = []orimages.Image{{Bytes: raw, MediaType: mediaType}}
	return res, nil
}

// imageBody is the endpoint's request. Every value is the caller's; nothing is invented.
func (t *Images) imageBody(ctx context.Context, slug, prompt string, req orimages.Request, refs []string) map[string]any {
	log := slog.Default()
	body := map[string]any{"prompt": prompt, "num_images": 1}
	if req.N > 1 {
		log.WarnContext(ctx, "fal: one picture per call; n was asked and one will come back",
			slog.Int("n", req.N), slog.String("model", slug))
	}
	switch ar := strings.TrimSpace(req.AspectRatio); {
	case ar == "" || strings.EqualFold(ar, "auto"):
	case falImageSizes[ar] != "":
		body["image_size"] = falImageSizes[ar]
	default:
		if t.once("ratio\x00" + ar) {
			log.WarnContext(ctx, "fal: an aspect ratio with no image_size preset is not sent; the endpoint's default size applies",
				slog.String("aspect_ratio", ar), slog.String("model", slug))
		}
	}
	if f := strings.ToLower(strings.TrimSpace(req.OutputFormat)); falImageFormats[f] {
		body["output_format"] = f
	}
	if len(refs) > 0 {
		if strings.Contains(slug, "edit") {
			body["image_urls"] = refs
		} else {
			body["image_url"] = refs[0]
			if len(refs) > 1 {
				log.WarnContext(ctx, "fal: this endpoint takes one reference picture; the first is sent and the rest are dropped",
					slog.String("model", slug), slog.Int("given", len(refs)))
			}
		}
	}
	if strings.TrimSpace(req.Quality) != "" || strings.TrimSpace(req.Background) != "" ||
		strings.TrimSpace(req.Resolution) != "" || req.OutputCompression != nil {
		if t.once("dials\x00" + slug) {
			log.DebugContext(ctx, "fal: quality / background / resolution / compression have no shared fal counterpart and are not sent",
				slog.String("model", slug), slog.String("quality", req.Quality),
				slog.String("background", req.Background), slog.String("resolution", req.Resolution))
		}
	}
	return body
}

// once reports true the first time key is seen by this transport.
func (t *Images) once(key string) bool {
	_, seen := t.warned.LoadOrStore(key, true)
	return !seen
}

// tableCost — units × the catalogue's per-call price of (fal, slug), in USD; 0 when the catalogue has
// no per-call row for it (unpriced) or fal reported nothing. An ASSUMED unit (no header) is still one
// picture, which is exactly what a per-call row prices.
func (t *Images) tableCost(slug string, units float64) float64 {
	if units <= 0 || t.lookup == nil {
		return 0
	}
	m, ok := t.lookup(entity.AIProviderFal, slug)
	if !ok || !m.PerCallUSD.Valid || !m.PerCallUSD.Decimal.IsPositive() {
		return 0
	}
	return m.PerCallUSD.Decimal.Mul(decimal.NewFromFloat(units)).InexactFloat64()
}

// chargedResult — the partial answer of a bought request that failed AFTER fal named its charge
// (ChargedError): no picture, the table cost. nil when fal named none or the table does not price it —
// a Result with no cost would read as «failed, nothing owed» rather than «unknown».
func (t *Images) chargedResult(slug string, err error) *orimages.Result {
	units, ok := Charge(err)
	if !ok {
		return nil
	}
	if model := ChargedModel(err); model != "" {
		slug = model
	}
	cost := t.tableCost(slug, units)
	if cost <= 0 {
		return nil
	}
	return &orimages.Result{Model: slug, Usage: orimages.Usage{Cost: cost, CostSource: entity.AICostTable}}
}

// costOnly — res when it carries a cost, else nil (see chargedResult).
func costOnly(res *orimages.Result) *orimages.Result {
	if res == nil || res.Usage.Cost <= 0 {
		return nil
	}
	return res
}

// pickFalImage reads the file of an image endpoint's answer: `images[0]` (FLUX, nano-banana, seedream,
// gpt-image …), else `image` (the single-file endpoints). Neither → ErrNoFile (paid, nothing to show).
func pickFalImage(body json.RawMessage) (string, string, error) {
	u, ct, err := PickImages0(body)
	if errors.Is(err, ErrNoFile) {
		return PickImage(body)
	}
	return u, ct, err
}

// falImageMediaType prefers fal's own label when it names an image and sniffs the bytes otherwise (the
// runblob / orimages preference): an `application/octet-stream` on a real PNG must not refuse the
// picture, and an HTML error page served as 200 must not be filed as one.
func falImageMediaType(label string, raw []byte) string {
	if mt, _, err := mime.ParseMediaType(label); err == nil && strings.HasPrefix(mt, "image/") {
		return mt
	}
	return http.DetectContentType(raw)
}

// imageBought is the CallError of a failure AFTER the submit: engaged, never retryable, the request id
// in the sentence so a person can find the job the money went to. The code names what happened —
// the ceiling, the caller leaving, an empty answer, an oversize file, a request fal no longer knows —
// and otherwise keeps the inner CallError's (a status fal answered the result fetch with).
func imageBought(err error, id, slug, why string) *aiprov.CallError {
	code, status := aiprov.CodeProviderError, 0
	if ce, ok := aiprov.AsCallError(err); ok {
		if ce.Code != "" {
			code = ce.Code
		}
		status = ce.HTTPStatus
	}
	switch {
	case errors.Is(err, ErrTimedOut), errors.Is(err, context.DeadlineExceeded):
		code = aiprov.CodeTimeout
	case errors.Is(err, context.Canceled):
		code = aiprov.CodeCanceled
	case errors.Is(err, ErrNoModel):
		code = aiprov.CodeEmptyAnswer
	case errors.Is(err, ErrTooLarge):
		code = aiprov.CodeTooLarge
	case errors.Is(err, ErrRequestNotFound):
		code = aiprov.CodeNotFound
	}
	return fail(code, status, true, false,
		fmt.Errorf("fal: request %s (%s) is bought and %s — the run must not be re-submitted: %w", id, slug, why, err))
}

// truncateText cuts s to n bytes for a sentence.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
