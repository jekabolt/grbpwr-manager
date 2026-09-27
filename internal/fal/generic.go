package fal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ─────────────────────────── THE GENERIC JSON ROUTE (PLAYGROUND phase 3, B-12) ───────────────────────────
//
// ONE TRANSPORT PAIR FOR ANY fal MODEL THAT TAKES A JSON BODY AND ANSWERS WITH ONE FILE. The cut-out
// route was the first such model and carried its own queue code; the outpaint (tile 9 «Extend
// Image») and fill (tile 10, the mask retouch) routes are the second and third. They differ from the
// cut-out in exactly three things — the slug, the body and where the file sits in the answer — so
// the queue code (the wait, the grace, the money envelope, the capped download) is written ONCE, here,
// and the cut-out now rides on it (cutout.go keeps its signatures and its tests).
//
// THE MONEY SHAPE IS THE 3D ONE, NOT THE CUT-OUT ONE (Codex G-02 #4). A route whose tariff is set
// without a units ceiling books `tariff × whatever units fal reports`, and no reservation can be said
// to cover that — RouteCeilingUSD answers ok=false and the door refuses the kind in words
// (`route_reserve_unbounded`) instead of reserving a number below the booking.

// MaxDataURIBytes is the largest data: URI SubmitJSON lets out of the process, in bytes of the URI
// itself (base64 included). OURS, not fal's: fal does not document a request-body limit for inline
// pictures (07-PHASE3 §15.1), and a derived crop over this is a picture built wrong, refused for free
// before the paid request rather than by a provider 413 after it.
const MaxDataURIBytes = 8 << 20

// FileResult describes ONE delivered file — CutoutResult under a name that says what it is on every
// route. Same fields, same promise: bytes and money, never a link.
type FileResult = CutoutResult

// ErrNoFile — the request COMPLETED (so the file was made and the units are spent) and the answer
// carries no url to fetch it with. It wraps ErrNoModel for the same reason ErrNoCutout does: the run
// classifier reads sentinels, and ErrNoModel already means «paid, nothing to show — do not retry».
var ErrNoFile = fmt.Errorf("%w: the finished request carries no file", ErrNoModel)

// ErrDataURITooLarge — an inline picture over MaxDataURIBytes, refused before the request leaves.
// It wraps ErrBadRequest: the classifier makes it terminal, and nothing was billed.
var ErrDataURITooLarge = fmt.Errorf("%w: an inline picture is over the %d byte ceiling", ErrBadRequest, MaxDataURIBytes)

// FilePicker reads a finished request's result body and names THE file it delivered: its url and
// the content type the provider called it. A missing url must be ErrNoFile (or an error wrapping
// ErrNoModel) — anything else would be read as weather by the classifier.
type FilePicker func(body json.RawMessage) (url, contentType string, err error)

// PickImage reads `{"image":{"url":…,"content_type":…}}` — birefnet, bria/expand.
func PickImage(body json.RawMessage) (string, string, error) {
	var out struct {
		Image falFile `json:"image"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("%w: result body: %v", ErrUnexpectedResponse, err)
	}
	if u := strings.TrimSpace(out.Image.URL); u != "" {
		return u, strings.TrimSpace(out.Image.ContentType), nil
	}
	return "", "", ErrNoFile
}

// PickImages0 reads `{"images":[{"url":…,"content_type":…}, …]}` — flux-2-pro/outpaint,
// flux-pro/v1/fill. The FIRST file, deterministically: both routes are asked for one (`num_images`
// 1 on fill; outpaint answers one), and designgen's narrowToOneOutput holds the same rule one layer up.
func PickImages0(body json.RawMessage) (string, string, error) {
	var out struct {
		Images []falFile `json:"images"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("%w: result body: %v", ErrUnexpectedResponse, err)
	}
	if len(out.Images) > 0 {
		if u := strings.TrimSpace(out.Images[0].URL); u != "" {
			return u, strings.TrimSpace(out.Images[0].ContentType), nil
		}
	}
	return "", "", ErrNoFile
}

// SubmitJSON puts ONE request into a model's queue and returns its id. IT NEVER RETRIES: a second
// submit is a second charge.
//
// Refused for free, before anything leaves the process: an empty slug; a body that does not encode;
// any string that starts with "data:" and is longer than MaxDataURIBytes; any `*_url` field that is
// not a public http(s) url or a data: URI (validateImageRef — the provider must be able to fetch it
// itself, and a reference it cannot fetch does not improve on a retry).
//
// The queue path the result is polled on later is queuePath(model) — the slug's first two segments,
// exactly as fal's own client builds it (fal-js libs/client/src/queue.ts: status/result use
// `${owner}/${alias}/requests/${id}`, dropping the endpoint path), so `fal-ai/flux-pro/v1/fill` is
// submitted whole and polled at `fal-ai/flux-pro`.
func (c *Client) SubmitJSON(ctx context.Context, model string, input any) (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" {
		return "", fmt.Errorf("%w: no model slug to submit to", ErrBadRequest)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("%w: encoding the request body: %v", ErrBadRequest, err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return "", fmt.Errorf("%w: re-reading the request body: %v", ErrBadRequest, err)
	}
	if err := checkSubmitTree(tree, ""); err != nil {
		return "", err
	}

	var sub submitResponse
	if err := c.callJSON(ctx, http.MethodPost, "/"+model, json.RawMessage(raw), &sub, nil); err != nil {
		return "", err
	}
	id := strings.TrimSpace(sub.RequestID)
	if id == "" {
		// PAID AND LOST: the submit was accepted, and nothing identifies what it bought.
		return "", submitLost()
	}
	c.checkQueuePath(ctx, model, id, sub.StatusURL)
	return id, nil
}

// checkSubmitTree walks a decoded request body: every data: string is size-checked, every
// `*_url` key holding a string passes validateImageRef.
func checkSubmitTree(v any, key string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if err := checkSubmitTree(child, k); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := checkSubmitTree(child, key); err != nil {
				return err
			}
		}
	case string:
		if strings.HasPrefix(t, "data:") && len(t) > MaxDataURIBytes {
			return fmt.Errorf("%w: %s is %d bytes", ErrDataURITooLarge, key, len(t))
		}
		if strings.HasSuffix(key, "_url") {
			if err := validateImageRef(strings.TrimSpace(t)); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return nil
}

// CollectFile waits for a submitted request and writes THE file pick names into dst. IT IS FREE: a
// lookup of a request already paid for. Same wait as the cut-out (first poll 400 ms, back-off to
// FAL_POLL_INTERVAL, notFoundGrace, the ceiling bounds the WAIT never the FETCH), same money envelope
// (x-fal-billable-units read on the result fetch; every failure after it carries the charge).
//
// maxBytes caps the download and REFUSES at the limit (<= 0 = the cut-out cap, 25 MiB).
func (c *Client) CollectFile(ctx context.Context, model, requestID string, pick FilePicker, dst io.Writer, maxBytes int64) (*FileResult, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if dst == nil {
		// Refused before any request: the file is already paid for by the submit, and collecting it
		// to throw it away would spend it a second time, for nothing.
		return nil, errors.New("fal: CollectFile has nowhere to put the file")
	}
	if pick == nil {
		return nil, errors.New("fal: CollectFile was given no way to find the file in the answer")
	}
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" {
		return nil, fmt.Errorf("%w: collect was given no model slug", ErrBadRequest)
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("%w: collect was given no request id", ErrBadRequest)
	}
	if maxBytes <= 0 {
		maxBytes = maxCutoutBytes
	}
	return c.awaitFile(ctx, model, requestID, pick, dst, maxBytes)
}

// awaitFile polls the request until it completes, then downloads its file. See awaitCutout's old
// argument (kept on the functions below): no search of other namespaces — these routes answer in
// seconds, so the window in which a slug move could strand a paid request is one request long.
func (c *Client) awaitFile(ctx context.Context, model, requestID string, pick FilePicker, dst io.Writer, limit int64) (*FileResult, error) {
	base := "/" + queuePath(model) + "/requests/" + url.PathEscape(requestID)

	// THE CEILING BOUNDS THE WAIT, NEVER THE FETCH — a single ceiling over both would cut the
	// download of a file that finished in the last moment of the wait, spending the units and
	// delivering nothing.
	ceiling := c.cfg.PollTimeout
	waitCtx, cancel := context.WithTimeout(ctx, ceiling)
	defer cancel()

	interval := cutoutFirstPoll
	if p := c.PollInterval(); p < interval {
		interval = p
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()

	// A 404 IN THE FIRST MOMENTS IS A LAG, NOT AN ANSWER — see notFoundGrace.
	grace := notFoundGrace
	if half := ceiling / 2; grace > half {
		grace = half
	}
	started := time.Now()

	for {
		var st statusResponse
		err := c.callJSON(waitCtx, http.MethodGet, base+"/status", nil, &st, nil)
		switch {
		case err == nil:
			switch Status(strings.ToUpper(strings.TrimSpace(st.Status))) {
			case StatusCompleted:
				// THE RESULT ENVELOPE IS FETCHED UNDER THE PARENT ctx, NOT waitCtx: it is the request
				// the charge arrives in, and a job that completed at the ceiling must not lose its
				// file together with the evidence of its price. Each control-plane request is still
				// bounded by its own HTTPTimeout (callJSON), the download by its own.
				return c.collectFile(ctx, model, requestID, base, pick, dst, limit)
			case StatusInQueue, StatusInProgress:
			case "":
				return nil, fmt.Errorf("%w: request %s came back with no status", ErrUnexpectedResponse, requestID)
			default:
				return nil, fmt.Errorf("%w: request %s has unknown status %q", ErrUnexpectedResponse, requestID, st.Status)
			}
		case errors.Is(err, ErrRequestNotFound) && time.Since(started) < grace:
			// The queue has not caught up with its own submit yet.
		default:
			// A lookup killed by the ceiling must read as a ceiling, not as a transport hiccup.
			if waitCtx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
				return nil, waitErr(ctx, requestID, ceiling)
			}
			return nil, err
		}

		select {
		case <-waitCtx.Done():
			return nil, waitErr(ctx, requestID, ceiling)
		case <-timer.C:
			if next := interval * 2; next < c.PollInterval() {
				interval = next
			} else {
				interval = c.PollInterval()
			}
			timer.Reset(interval)
		}
	}
}

// collectFile reads the finished request's envelope — where fal reports the charge — and downloads
// the file pick names. Everything from the result fetch onwards carries the money.
func (c *Client) collectFile(ctx context.Context, model, requestID, base string, pick FilePicker, dst io.Writer, limit int64) (*FileResult, error) {
	var body json.RawMessage
	var hdr http.Header
	if err := c.callJSON(ctx, http.MethodGet, base, nil, &body, &hdr); err != nil {
		if hdr == nil {
			// A COMPLETED request whose result the provider refuses to serve (non-2xx): terminal and
			// possibly billed; no charge header came with the refusal.
			return nil, err
		}
		// ⚠ A 2xx WHOSE BODY IS OVER THE CAP OR NOT JSON (G-03, Codex 3): the job was made and billed,
		// and the charge travelled in the headers callJSON had already copied before reading the
		// body. It rides the error — the units fal named, or (no header) the one assumed unit flagged
		// Assumed so the pricing side books its conservative ceiling instead of a guess.
		units, assumed := billableUnits(hdr)
		return nil, chargedAssumed(err, units, assumed, requestID, model)
	}
	units, assumed := billableUnits(hdr)
	charged := func(err error) error { return chargedAssumed(err, units, assumed, requestID, model) }

	link, contentType, err := pick(body)
	if err != nil {
		return nil, charged(fmt.Errorf("%w: request %s", err, requestID))
	}
	res := &FileResult{
		RequestID:     requestID,
		Model:         model,
		ContentType:   contentType,
		BillableUnits: units,
		UnitsAssumed:  assumed,
	}
	n, sum, err := c.fetch(ctx, link, dst, limit)
	if err != nil {
		// Over the cap, or a transfer that died: made and billed either way. The bytes are lost;
		// the money is not, and must not be.
		return nil, charged(fmt.Errorf("fal: downloading the file of request %s: %w", requestID, err))
	}
	res.Bytes, res.SHA256 = n, sum
	return res, nil
}

// ─────────────────────────── the two new routes and their money ───────────────────────────

const (
	// DefaultModelOutpaint is tile 9's route (FAL_MODEL_OUTPAINT). The env fallback is
	// "fal-ai/bria/expand" (designgen builds a body per slug family and refuses any other slug).
	DefaultModelOutpaint = "fal-ai/flux-2-pro/outpaint"
	// DefaultModelFill is tile 10's mask route (FAL_MODEL_FILL).
	DefaultModelFill = "fal-ai/flux-pro/v1/fill"

	// Published prices, read 2026-09-27:
	//   - outpaint: "$0.03 for the first megapixel of output, plus $0.015 per extra megapixel of
	//     input and output, rounded up to the nearest megapixel" (https://fal.ai/models/fal-ai/flux-2-pro/outpaint);
	//   - fill: "$0.05 per megapixel. Images are billed by rounding up to the nearest megapixel"
	//     (https://fal.ai/models/fal-ai/flux-pro/v1/fill);
	//   - bria/expand: "$0.04 per generation" (https://fal.ai/models/fal-ai/bria/expand; under the
	//     outpaint ceiling, so the fallback slug needs no row of its own).
	//
	// ⚠ THESE ARE CEILINGS OF THE WORST PLAN THE WORKER CAN BUILD, not expectations. designgen caps
	// the extend canvas at 3 MP (the input is inside it, so ≤ 3 MP): 0.03 + 0.015 × (3 + 3 − 1) =
	// 0.105. It caps the fill crop at 1 MP in and 1 MP out: 0.05 × 2 = 0.10 (billing the input as well
	// is the pessimistic reading of «per megapixel»). Both are rounded up with margin, and the probes
	// in designgen hold the caps against these numbers — raise a cap and a probe goes red.
	defaultOutpaintUSD = 0.12
	defaultFillUSD     = 0.15
)

// Route names one of the generic JSON routes whose money this package publishes.
type Route string

const (
	RouteOutpaint Route = "outpaint"
	RouteFill     Route = "fill"
)

// ModelEnv / UnitUSDEnv / UnitsCeilingEnv — the variable names a refusal or a warning says out loud.
func (r Route) ModelEnv() string {
	switch r {
	case RouteOutpaint:
		return "FAL_MODEL_OUTPAINT"
	case RouteFill:
		return "FAL_MODEL_FILL"
	}
	return ""
}

func (r Route) UnitUSDEnv() string {
	switch r {
	case RouteOutpaint:
		return "FAL_UNIT_USD_OUTPAINT"
	case RouteFill:
		return "FAL_UNIT_USD_FILL"
	}
	return ""
}

func (r Route) UnitsCeilingEnv() string {
	switch r {
	case RouteOutpaint:
		return "FAL_UNITS_CEILING_OUTPAINT"
	case RouteFill:
		return "FAL_UNITS_CEILING_FILL"
	}
	return ""
}

// ModelFor returns the effective slug of a route: the env override, else the code default.
// Nil-safe (a nil client answers the default, like ModelCutout), "" for an unknown route.
func (c *Client) ModelFor(r Route) string {
	override, def := "", ""
	switch r {
	case RouteOutpaint:
		def = DefaultModelOutpaint
		if c != nil {
			override = c.cfg.ModelOutpaint
		}
	case RouteFill:
		def = DefaultModelFill
		if c != nil {
			override = c.cfg.ModelFill
		}
	default:
		return ""
	}
	if m := strings.Trim(strings.TrimSpace(override), "/"); m != "" {
		return m
	}
	return def
}

// EstimatedRouteUSD is the code ceiling of ONE request off the route — what the door reserves and
// what the collect books when no tariff is configured. Exported for the reason EstimatedCutoutUSD
// is: the door and the collect read one expression. Zero for an unknown route.
func EstimatedRouteUSD(r Route) decimal.Decimal {
	switch r {
	case RouteOutpaint:
		return decimal.NewFromFloat(defaultOutpaintUSD)
	case RouteFill:
		return decimal.NewFromFloat(defaultFillUSD)
	}
	return decimal.Zero
}

func (c *Client) routeTariff(r Route) (unit, ceiling float64) {
	if c == nil {
		return 0, 0
	}
	switch r {
	case RouteOutpaint:
		return c.cfg.UnitUSDOutpaint, c.cfg.UnitsCeilingOutpaint
	case RouteFill:
		return c.cfg.UnitUSDFill, c.cfg.UnitsCeilingFill
	}
	return 0, 0
}

// RouteCeilingUSD — THE MOST ONE REQUEST OFF THIS ROUTE MAY BOOK ON THIS CLIENT, i.e. the number the
// door must reserve so the collect (CostRouteUSD) never books more. The 3D shape
// (RequestCeilingUSDForQuality):
//
//   - no tariff: the collect books EstimatedRouteUSD whatever the units → that, ok;
//   - a tariff AND a units ceiling: tariff × ceiling, ok;
//   - a tariff and no ceiling: 0, false — the booking is tariff × whatever fal reports, and no
//     reservation covers it. The door refuses (`route_reserve_unbounded`), the band hides the tile.
func (c *Client) RouteCeilingUSD(r Route) (decimal.Decimal, bool) {
	unit, ceiling := c.routeTariff(r)
	if unit <= 0 {
		return EstimatedRouteUSD(r), true
	}
	if ceiling <= 0 {
		return decimal.Zero, false
	}
	return decimal.NewFromFloat(unit).Mul(decimal.NewFromFloat(ceiling)), true
}

// RouteUnitsCeiling — the stated units ceiling under a tariff (ok = false: no tariff or no ceiling).
// The collect compares the reported units with it and says so when fal billed past the reserve.
func (c *Client) RouteUnitsCeiling(r Route) (float64, bool) {
	unit, ceiling := c.routeTariff(r)
	if unit <= 0 || ceiling <= 0 {
		return 0, false
	}
	return ceiling, true
}

// CostRouteUSD converts billable units into money: tariff × units, or — with no tariff — the
// route's per-request estimate (never a multiplication by a made-up unit price; the 3D lesson).
// Zero units (or a nil client) is zero money: nothing was reported, and chargedWith never wraps one.
func (c *Client) CostRouteUSD(r Route, units float64) decimal.Decimal {
	if c == nil || units <= 0 {
		return decimal.Zero
	}
	unit, _ := c.routeTariff(r)
	if unit <= 0 {
		return EstimatedRouteUSD(r)
	}
	return decimal.NewFromFloat(unit).Mul(decimal.NewFromFloat(units))
}
