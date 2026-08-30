// Package recraft is the VECTOR generation service (Recraft V4 Vector / V4 Pro Vector).
//
// It exists to answer exactly one requirement (owner spec P-3): «для генерации вектора выбери
// Recraft V4 Vector / V4 Pro Vector — нам нужен ровный вектор, а не какая-то хуйня с кучей
// полигонов, чтобы это легко было править».
//
// # THE FORK THIS PACKAGE IS BUILT AROUND — READ BEFORE CHANGING ANYTHING
//
// Recraft offers TWO ways to get an SVG out of a raster picture, and they are opposites:
//
//	vectorize                     — TRACES the raster. It follows pixel boundaries and emits
//	                                thousands of nodes: it produces LITERALLY the «куча полигонов»
//	                                the owner forbade. THIS PACKAGE NEVER CALLS IT.
//	imageToImage with a *vector*  — REDRAWS the thing with a vector model, using the approved raster
//	model                           only as a composition reference. The result is authored curves:
//	                                few nodes, clean geometry, editable by a human.
//
// The wording that circulates in the requirement notes — «генерятся в растре, потом переводятся в
// вектор» — reads, literally, as `vectorize`. That literal reading leads straight into the thing
// that was forbidden. The decision is recorded in the plan (34-PLAN.md §2, W-9) and is not to be
// re-litigated here. TestNoVectorizeAnywhereInPackage fails if the word ever appears in a string
// literal of this package, so a future "fix" cannot quietly reintroduce it.
//
// # IT IS A REDRAW, NOT A TRANSLATION — AND THAT CHANGES WHO ACCEPTS THE RESULT
//
// imageToImage does not convert the picture; it draws a new one that looks like it. A cuff can come
// back a different cuff, a pocket can move, a seam line can disappear. That is the price of clean
// curves, and it is the right trade for our purpose — but it means the SVG MUST BE ACCEPTED BY A
// HUMAN, side by side with the source raster, and must never be substituted for the approved flat
// automatically. Nothing in this package publishes anything: it returns bytes plus the numbers a
// person needs in order to judge them (see VectorResult.Stats).
//
// # WHAT THIS PACKAGE DOES NOT DO
//
// It does NOT convert the returned SVG into our editor's stroke model, and it will not as long as
// that model stores POLYLINES (`VectorStroke.pts` — an array of number pairs). Flattening cubic
// segments into line segments would recreate, by our own hand, the many-node mush the requirement
// is written against. The bytes are returned as they arrive; making curves editable is work on the
// editor, not a silent approximation here.
//
// # TWO ROUTES, ONE PRIMARY
//
// Owner rule P-5 is «всё, что можно, — через OpenRouter», and Recraft IS available there
// (`recraft/recraft-v4-vector`, `recraft/recraft-v4-pro-vector`, verified live 2026-08-30). So the
// primary route is OpenRouter's image endpoint, reached through a NARROW interface (ImageGenerator)
// implemented by internal/orimages — this package does not own that transport and does not
// duplicate it.
//
// The direct Recraft HTTP client in direct.go is the FALLBACK, behind RECRAFT_ROUTE=direct. It is
// kept for the one thing OpenRouter does not expose: the `strength` dial, i.e. how far the redraw
// may depart from the approved raster. Unlike Meshy — which has no OpenRouter presence at all and
// therefore forces a direct client — Recraft has no such excuse, and the direct route must stay a
// switch nobody flips by default.
//
// # MONEY
//
// One call = one paid image. Published prices (verified 2026-08-30, identical on both routes):
// V4 Vector $0.08, V4 Pro Vector $0.30. THE PAID CALL IS NEVER RETRIED INSIDE THIS PACKAGE:
// idempotency is not promised, so a hidden retry would spend a second $0.30 outside the attempt
// ledger where nobody could see it. Retrying is the worker's decision, with a cap of two.
package recraft

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Route names which transport carries the paid call.
type Route string

const (
	// RouteOpenRouter is the PRIMARY route (owner rule P-5). The call goes through the shared
	// OpenRouter image client (internal/orimages) with a Recraft vector slug.
	RouteOpenRouter Route = "openrouter"
	// RouteDirect is the FALLBACK: Recraft's own API, for when the `strength` dial is needed or the
	// OpenRouter route is unusable. Deliberately not the default.
	RouteDirect Route = "direct"
)

// Model identifiers, per route. They differ in spelling, and that difference is load-bearing: the
// OpenRouter catalogue namespaces the vendor (`recraft/recraft-v4-vector`) while Recraft's own API
// uses its internal id (`recraftv4_vector`). Sending one to the other is an instant 404.
//
// All four were verified against the live catalogues on 2026-08-30. They are constants AND
// env-overridable (RECRAFT_MODEL_VECTOR / RECRAFT_MODEL_VECTOR_PRO) for one reason: a baked-in
// provider slug is a thing that rots silently. This repo has lived through it once — a retired
// OpenRouter slug turned every AI feature into an HTTP 404 in 0.2 s, and the only fix was a deploy.
//
// Both must stay VECTOR models. Pointing either at a raster model does not fail at the request; it
// returns a PNG, which this package refuses as ErrNotVector rather than storing a bitmap under the
// name "vector".
const (
	// OpenRouter route ($0.08 / $0.30 per image).
	ModelORVector    = "recraft/recraft-v4-vector"
	ModelORVectorPro = "recraft/recraft-v4-pro-vector"
	// Direct route (same models, Recraft's own ids).
	ModelDirectVector    = "recraftv4_vector"
	ModelDirectVectorPro = "recraftv4_pro_vector"
)

// Tier selects WHICH of the owner's two vector models runs the call. It is a tier rather than a raw
// slug so that the choice offered to a human ("standard" $0.08 / "pro" $0.30) survives both a
// provider rename and a change of route: the slug is configuration, the tier is the contract.
type Tier string

const (
	TierVector    Tier = "vector"     // Recraft V4 Vector
	TierProVector Tier = "pro_vector" // Recraft V4 Pro Vector
)

// Tiers lists the selectable tiers, cheapest first. Callers that offer the choice to a human should
// build the control from this, so adding a tier never means hunting for a hardcoded pair.
func Tiers() []Tier { return []Tier{TierVector, TierProVector} }

// Valid reports whether t is one of the two tiers.
func (t Tier) Valid() bool { return t == TierVector || t == TierProVector }

// EstimatedUSD is the published price of one image on this tier. It is the number to RESERVE before
// the call; the number to RECORD after it is the cost the transport reports back.
func (t Tier) EstimatedUSD() float64 {
	switch t {
	case TierProVector:
		return 0.30
	case TierVector:
		return 0.08
	}
	return 0
}

// Sentinel faults. They are separate values because each sends a person somewhere DIFFERENT, and
// because the worker must decide, from the error alone, whether a retry can charge us twice.
var (
	// ErrNotConfigured — the active route has no credentials wired. The feature is off, not broken:
	// the button must refuse up front (kind_not_available) instead of parking a run in `pending`.
	ErrNotConfigured = errors.New("recraft: the vector provider is not configured")

	// ErrUnauthorized — the key was rejected (HTTP 401/403). Nothing generated, nothing charged.
	ErrUnauthorized = errors.New("recraft: the API key was rejected by the provider")

	// ErrInsufficientCredits — HTTP 402. Not weather: retrying fixes nothing, somebody must top up.
	ErrInsufficientCredits = errors.New("recraft: the provider reports insufficient credits")

	// ErrRateLimited — HTTP 429. The request was REFUSED, so it was not charged; this is the one
	// failure a caller may retry with a clear conscience.
	ErrRateLimited = errors.New("recraft: rate limited by the provider")

	// ErrModelUnavailable — HTTP 404, classified BY STATUS ALONE, never by matching a substring of
	// the provider's English sentence, so a reworded message cannot silently reclassify the fault.
	// It means the slug or the route is not served: read RECRAFT_MODEL_* / RECRAFT_ROUTE.
	ErrModelUnavailable = errors.New("recraft: the configured model or endpoint is not available at the provider")

	// ErrBadRequest — HTTP 400/422, or our own validation. We sent something unacceptable; a retry
	// repeats it exactly.
	ErrBadRequest = errors.New("recraft: the provider rejected the request")

	// ErrProviderFailure — 5xx or transport failure. THE ONLY HONEST THING TO SAY IS THAT WE DO NOT
	// KNOW whether the image was produced and billed. That is exactly the `unknown` attempt state,
	// and exactly why the paid call is not retried here.
	ErrProviderFailure = errors.New("recraft: the provider call failed")

	// ErrInvalidResponse — the call succeeded but the body is not what the contract promises: no
	// image, a truncated envelope, undecodable bytes, unparseable SVG.
	ErrInvalidResponse = errors.New("recraft: malformed provider response")

	// ErrNotVector — the provider returned a picture that is not SVG. In practice: a RASTER model
	// configured under a vector name. Separate because storing those bytes would quietly defeat the
	// whole requirement — the band would show a "vector" that is a bitmap.
	ErrNotVector = errors.New("recraft: the provider returned a raster image, not SVG")

	// ErrUnsafeSVG — the SVG carries active content (script, event handlers, javascript: links) or
	// an entity declaration. We publish these bytes from our own bucket into an admin's browser, so
	// they are refused rather than scrubbed: a partial scrub that misses one vector is worse than a
	// loud refusal of a file no legitimate generation produces.
	ErrUnsafeSVG = errors.New("recraft: the returned SVG contains active or unsafe content")
)

// ChargedError marks a failure the provider MAY ALREADY HAVE BILLED, and carries the charge.
//
// «Деньги списаны, картинок нет» is a real, named state in the run ledger: the attempt goes to
// `unknown`, the run fails, and THE PRICE IS STILL WRITTEN DOWN. Without this the money simply
// disappears from the budget — the daily cap then stops matching reality, and the reason is
// invisible. Callers read it back with Charge().
//
// It exists because both transports can fail AFTER the meter ran: OpenRouter returns its usage
// alongside an empty data array, and the direct route reports credits in an envelope whose image
// then fails to decode.
type ChargedError struct {
	Err     error
	CostUSD float64
	Credits float64
	Model   string
}

func (e *ChargedError) Error() string {
	if e.CostUSD > 0 {
		return fmt.Sprintf("%v [charged $%.4f]", e.Err, e.CostUSD)
	}
	return fmt.Sprintf("%v [charged %.0f credits]", e.Err, e.Credits)
}

func (e *ChargedError) Unwrap() error { return e.Err }

// wrapCharged attaches a charge to an error, and only when there is one to attach — an unbilled
// failure must not read as a billed one.
func wrapCharged(err error, costUSD, credits float64, model string) error {
	if err == nil || (costUSD <= 0 && credits <= 0) {
		return err
	}
	return &ChargedError{Err: err, CostUSD: costUSD, Credits: credits, Model: model}
}

// Charge reports what a failed call cost us, when the transport knew. ok is false when the failure
// carried no charge — which is NOT the same as a charge of zero, and the worker must record the
// difference.
func Charge(err error) (costUSD, credits float64, ok bool) {
	var ce *ChargedError
	if errors.As(err, &ce) {
		return ce.CostUSD, ce.Credits, true
	}
	return 0, 0, false
}

// ImageGenerator is the NARROW transport contract: send a prompt and (optionally) one input picture
// to a named model, get bytes back. Nothing about vectors, tiers or Recraft appears in it, because
// the primary implementation is the shared OpenRouter image client (internal/orimages) which serves
// every picture-shaped feature, not just this one.
//
// This package OWNS the tier→slug tables and the SVG contract; the transport owns HTTP, money
// reporting and the provider envelope. Keeping the seam here is what lets the primary route be
// somebody else's package without this one duplicating it.
type ImageGenerator interface {
	GenerateImage(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)
}

// enabler is an OPTIONAL half of the transport contract. A transport that knows whether it holds a
// key can say so BEFORE a run row is created, which is the difference between an honest refusal on
// the button and a run parked in `pending` forever. Transports that do not implement it are assumed
// configured and answer at call time instead.
type enabler interface{ Enabled() bool }

// GenerateRequest is one paid image call, in the transport's vocabulary.
type GenerateRequest struct {
	// Model is the provider-native model id, ALREADY RESOLVED for the active route.
	Model string
	// Prompt says what is being drawn; NegativePrompt says what must not appear.
	Prompt         string
	NegativePrompt string
	// Image is the input reference. Empty = text-to-image (draw from nothing).
	Image ImageInput
	// Strength is how far the redraw may depart from Image, 0..1, where 0 is almost identical and 1
	// is barely related. nil = the transport's default. Transports without the dial (OpenRouter's
	// image endpoint does not expose it) ignore it — which is the single reason the direct route
	// still exists.
	Strength *float64
	// Seed makes a run reproducible; nil = the provider chooses.
	Seed *int64
}

// GenerateResponse is what a transport hands back. Fields it cannot know stay zero — an unknown
// price must read as unknown, never as free.
type GenerateResponse struct {
	// Bytes is the produced picture, whole.
	Bytes []byte
	// ContentType is what the transport believes it is ("" when it does not know). It is a hint
	// only: this package decides what the bytes are by looking at them.
	ContentType string
	// Model is the slug that actually answered — the transport may have substituted one.
	Model string
	// CostUSD is the real charge when the transport knows it (OpenRouter reports `usage.cost`;
	// the direct route converts Recraft's credits at the configured rate). 0 = unknown.
	CostUSD float64
	// Credits is the provider-native unit count, unconverted. 0 = not reported.
	Credits float64
	// SourceURL is the provider link the bytes were downloaded from, when they were not inlined.
	// FOR LOGS ONLY — provider links expire, so it must never be stored as if it were the picture.
	SourceURL string
}

// ImageInput is the raster the vector model redraws. At most one of URL / Bytes may be set.
//
// URL is the cheap path and the default: our design pictures already live in a public bucket, the
// provider fetches them itself, and the bytes never pass through this process (which has 0.5 GiB of
// RAM). Bytes exists for sources the provider cannot reach — a private object, or a picture that
// only exists in memory.
type ImageInput struct {
	URL         string
	Bytes       []byte
	ContentType string // for Bytes; defaults to application/octet-stream
	Filename    string // for Bytes; cosmetic, defaults to "source"
}

// IsEmpty reports whether no input picture was supplied at all.
func (i ImageInput) IsEmpty() bool { return strings.TrimSpace(i.URL) == "" && len(i.Bytes) == 0 }

// Config is the vector-service configuration, bound in config/cfg.go.
//
// EVERY FIELD NEEDS ITS OWN viper.BindEnv LINE. AutomaticEnv is switched off in this repo on
// purpose, so an unbound name unmarshals as empty — indistinguishable from a correctly-unset
// optional override, which is why the bindings carry a test of their own.
type Config struct {
	// Route — RECRAFT_ROUTE: "" (= openrouter, the P-5 default) | "openrouter" | "direct".
	Route string `mapstructure:"route"`
	// ModelVector / ModelVectorPro — RECRAFT_MODEL_VECTOR / RECRAFT_MODEL_VECTOR_PRO. They override
	// the slug for the ACTIVE ROUTE (the two routes spell the same models differently), so the
	// defaults are picked after the route is known.
	ModelVector    string `mapstructure:"model_vector"`
	ModelVectorPro string `mapstructure:"model_vector_pro"`
	// Direct is the fallback route's own configuration. Ignored while Route is openrouter.
	Direct DirectConfig `mapstructure:"direct"`
}

// Client is the vector service. A nil *Client is a valid, permanently-disabled client
// (Enabled() == false), so callers need not nil-check before asking.
type Client struct {
	route Route
	gen   ImageGenerator
	// models is the resolved tier→slug table for the active route.
	models map[Tier]string
}

// New wires the service.
//
// orGen is the shared OpenRouter image transport (internal/orimages). It may be nil — a deployment
// that has not wired it simply leaves the primary route disabled, and Enabled() says so.
//
// THERE IS NO SILENT FAILOVER between routes. A misconfigured primary refuses with ErrNotConfigured
// naming the knob; it does not quietly start spending money through the other one, because "the
// cheap route was down so we used the other one" is exactly the kind of thing that must never
// happen without somebody typing it.
func New(cfg Config, orGen ImageGenerator) *Client {
	route := Route(strings.ToLower(strings.TrimSpace(cfg.Route)))
	if route == "" {
		route = RouteOpenRouter
	}

	c := &Client{route: route, models: map[Tier]string{}}
	switch route {
	case RouteDirect:
		c.gen = newDirectClient(cfg.Direct)
		c.models[TierVector] = firstNonEmpty(cfg.ModelVector, ModelDirectVector)
		c.models[TierProVector] = firstNonEmpty(cfg.ModelVectorPro, ModelDirectVectorPro)
	default:
		// An unknown RECRAFT_ROUTE lands here as the P-5 default rather than as a dead client: a
		// typo in a dashboard variable must not silently disable a paid feature. It is visible in
		// Route(), which the boot log prints.
		c.route = RouteOpenRouter
		c.gen = orGen
		c.models[TierVector] = firstNonEmpty(cfg.ModelVector, ModelORVector)
		c.models[TierProVector] = firstNonEmpty(cfg.ModelVectorPro, ModelORVectorPro)
	}
	return c
}

// NewWithGenerator builds a service on an explicit transport, bypassing route selection. It exists
// for tests and for a caller that has already decided which transport to spend money through.
func NewWithGenerator(route Route, gen ImageGenerator, models map[Tier]string) *Client {
	m := map[Tier]string{}
	for k, v := range models {
		m[k] = v
	}
	return &Client{route: route, gen: gen, models: m}
}

func firstNonEmpty(a, b string) string {
	if s := strings.TrimSpace(a); s != "" {
		return s
	}
	return b
}

// Route reports which transport this client spends money through. Nil-safe.
func (c *Client) Route() Route {
	if c == nil {
		return ""
	}
	return c.route
}

// Enabled reports whether the active route can actually be called. Nil-safe.
//
// A transport that cannot answer the question is assumed configured: this gate exists to catch the
// KNOWN-dead case up front, and guessing "disabled" from silence would refuse a working feature.
func (c *Client) Enabled() bool {
	if c == nil || c.gen == nil {
		return false
	}
	if e, ok := c.gen.(enabler); ok {
		return e.Enabled()
	}
	return true
}

// Model resolves a tier to the slug this client will actually send. Callers that RECORD which model
// answered must use this rather than the constants, or the history will name a model that was never
// called whenever an override is set. Nil-safe; an unknown tier resolves to "".
func (c *Client) Model(t Tier) string {
	if c == nil {
		return ""
	}
	return c.models[t]
}

// ImageToImageRequest is one paid vector redraw.
type ImageToImageRequest struct {
	// Tier picks the model. Empty = TierVector (the cheap one): if a caller forgets to choose, the
	// default must be the $0.08 press, never the $0.30 one.
	Tier Tier
	// Prompt describes WHAT is being drawn, in the vocabulary of the thing (a garment flat, a
	// technical drawing). Required: an empty prompt is a paid coin flip.
	Prompt string
	// Image is the approved raster used as the composition reference. Required here — a redraw
	// without a source is a generation, which is a different (and separately priced) press.
	Image ImageInput
	// Strength overrides how far the redraw may depart from that raster; nil = the transport's
	// default. Honoured only on the direct route; see GenerateRequest.Strength.
	Strength *float64
	// NegativePrompt lists what must not appear (e.g. "background, shadows, photorealism").
	NegativePrompt string
	// Seed makes a run reproducible; nil = the provider chooses.
	Seed *int64
}

// VectorResult is the SVG and everything the caller needs to store it, account for it and SHOW IT
// TO A PERSON.
//
// The bytes are handed back RAW AND UNCONVERTED, on purpose. Turning them into our editor's stroke
// model is not this package's business; see the package doc for why an automatic conversion would
// damage exactly the property the owner asked for. Nor is this result a replacement for the source
// flat: it is a redraw, and a human accepts or rejects it by eye.
type VectorResult struct {
	// SVG is the response body: checked, safe SVG text.
	SVG []byte
	// ContentType is always SVGContentType — stated so the caller stores it under the right type
	// rather than guessing from an extension.
	ContentType string
	// Model is the slug that actually answered; Tier is what the caller asked for; Route is which
	// transport spent the money. All three belong in the history row.
	Model string
	Tier  Tier
	Route Route
	// CostUSD is the real charge when the transport reported one, else 0 = unknown (NOT free).
	// Credits is the provider-native unit count when reported. EstimatedUSD is the tier's published
	// price — the reservation figure, kept beside the real one so a silent divergence is visible.
	CostUSD      float64
	Credits      float64
	EstimatedUSD float64
	// SourceURL is the provider link the bytes came from, when they were not inlined. FOR LOGS ONLY.
	SourceURL string
	// Stats is the shape of what came back. It is the measurement that tells a human whether this is
	// the «ровный вектор» that was asked for or a node soup: a redrawn garment is tens of paths and
	// hundreds of nodes; a traced raster is thousands.
	Stats SVGStats
}

// ImageToImage redraws an approved raster as a VECTOR with one of the two Recraft V4 vector models.
//
// It is one paid call and it is never retried here; see the package doc. On success the caller owns
// bytes that still have to be shown to a person before they mean anything.
func (c *Client) ImageToImage(ctx context.Context, req ImageToImageRequest) (*VectorResult, error) {
	if c == nil || c.gen == nil {
		return nil, fmt.Errorf("%w: no transport wired for route %q", ErrNotConfigured, c.Route())
	}
	if e, ok := c.gen.(enabler); ok && !e.Enabled() {
		return nil, fmt.Errorf("%w: the %s transport holds no API key", ErrNotConfigured, c.route)
	}
	if req.Tier == "" {
		req.Tier = TierVector
	}
	if !req.Tier.Valid() {
		return nil, fmt.Errorf("%w: unknown tier %q (want one of %v)", ErrBadRequest, req.Tier, Tiers())
	}
	model := c.Model(req.Tier)
	if model == "" {
		return nil, fmt.Errorf("%w: no model configured for tier %q on route %q", ErrNotConfigured, req.Tier, c.route)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("%w: prompt is required", ErrBadRequest)
	}
	if req.Image.IsEmpty() {
		return nil, fmt.Errorf("%w: an input image (URL or bytes) is required", ErrBadRequest)
	}
	if strings.TrimSpace(req.Image.URL) != "" && len(req.Image.Bytes) > 0 {
		return nil, fmt.Errorf("%w: set either Image.URL or Image.Bytes, not both", ErrBadRequest)
	}
	if req.Strength != nil && (*req.Strength < 0 || *req.Strength > 1) {
		return nil, fmt.Errorf("%w: strength %.3f is outside [0,1]", ErrBadRequest, *req.Strength)
	}

	resp, err := c.gen.GenerateImage(ctx, GenerateRequest{
		Model:          model,
		Prompt:         strings.TrimSpace(req.Prompt),
		NegativePrompt: strings.TrimSpace(req.NegativePrompt),
		Image:          req.Image,
		Strength:       req.Strength,
		Seed:           req.Seed,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Bytes) == 0 {
		return nil, fmt.Errorf("%w: the transport returned no bytes", ErrInvalidResponse)
	}

	// The bytes decide what they are. A content-type hint from the transport is not evidence: a
	// raster model configured under a vector name answers 200 with a perfectly labelled PNG.
	stats, err := InspectSVG(resp.Bytes)
	if err != nil {
		return nil, err
	}

	answered := strings.TrimSpace(resp.Model)
	if answered == "" {
		answered = model
	}
	return &VectorResult{
		SVG:          resp.Bytes,
		ContentType:  SVGContentType,
		Model:        answered,
		Tier:         req.Tier,
		Route:        c.route,
		CostUSD:      resp.CostUSD,
		Credits:      resp.Credits,
		EstimatedUSD: req.Tier.EstimatedUSD(),
		SourceURL:    resp.SourceURL,
		Stats:        stats,
	}, nil
}
