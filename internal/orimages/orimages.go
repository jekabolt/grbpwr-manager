// Package orimages is the client for OpenRouter's IMAGE endpoint — a second, separate request path
// to the same provider that internal/openrouter talks to for text.
//
// WHY A SECOND PACKAGE AND NOT A SECOND METHOD. OpenRouter has TWO catalogues, and they barely
// overlap:
//
//	GET /api/v1/models          → 396 chat models. NO `openai/gpt-image-*` is among them.
//	GET /api/v1/images/models   →  48 image models, sent to POST /api/v1/images.
//
// (Both numbers measured against the live API on 2026-08-30; 39 of the 48 image models exist in no
// other catalogue.) So the existing chat() cannot reach GPT Image by "just changing the model" —
// the model is not the difference. The endpoint is, the request body is, and above all the
// RESPONSE is: pictures come back as base64 in the body, which is why this package carries its own
// (much larger, operator-tunable) read ceiling instead of the 4 MiB text one, a ceiling smaller
// than a single PNG.
//
// The client is optional and degrades gracefully: with no API key Enabled() is false and Generate
// returns ErrNotConfigured, so the rest of the app keeps working with generation simply
// unavailable. It performs NO RETRIES — see Generate for why that is a deliberate refusal.
package orimages

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultModel is the image model slug used when none is configured.
	//
	// IT IS gpt-image-2, AND THE OLDER ARGUMENT FOR gpt-image-1 IS DEAD — read this before
	// "fixing" it back. The catalogue fact that argument rested on is still true and still worth
	// knowing: gpt-image-1 and gpt-image-1-mini list `background: [auto, transparent, opaque]`,
	// gpt-image-2 lists `background: [auto, opaque]` only. What changed is that WE DO NOT WANT
	// TRANSPARENCY. The design band's own prompts order a background in words — "black vector line
	// art on a plain white background", "white seamless background" — and the raster is then
	// vectorised, where a background is not carried at all. An enum value nobody asks for is not a
	// reason to stay on the older model.
	//
	// ⚠ THE COROLLARY IS SHARP: with this default, sending `background: "transparent"` is a 400 on
	// every call. See the Background field for who must stop asking.
	//
	// Measured against GET /api/v1/images/models and .../openai/gpt-image-2/endpoints on
	// 2026-08-30. What gpt-image-2 accepts, in full:
	//
	//	aspect_ratio        1:1 3:2 2:3 4:3 3:4 16:9 9:16 21:9 auto   (a SUPERSET of gpt-image-1's
	//	                                                               1:1 3:2 2:3 auto)
	//	quality             auto low medium high                       (identical to gpt-image-1)
	//	background          auto opaque                                (gpt-image-1 also: transparent)
	//	n                   1..10                                      (identical)
	//	input_references    0..16                                      (identical)
	//	output_compression  0..100                                     (identical)
	//
	// MONEY MOVES DOWNWARD, which is the safe direction for a reservation: per token, output_image
	// costs $0.00003 on gpt-image-2 against $0.00004 on gpt-image-1 (−25%), input_image $0.000008
	// against $0.00001 (−20%), input_text $0.000005 on both. The static reserve in
	// internal/apisrv/admin (designPriceEstimate) therefore over-covers rather than under-covers,
	// and needs no emergency edit — but it is now loose, not tight.
	//
	// LIKE openrouter.defaultModel, THIS CONSTANT IS LOAD-BEARING: a slug retired by the provider
	// turns every generation into a 404 in 0.2 s. Anything put here must be checked against the
	// live catalogue before it is committed — see WarnIfModelRetired for the boot-time alarm.
	DefaultModel = "openai/gpt-image-2"

	// defaultBaseURL is the OpenRouter API root — the SAME root the chat client uses. The images
	// route hangs off it at /images; nothing about the host or the account differs.
	defaultBaseURL = "https://openrouter.ai/api/v1"

	// defaultTimeout bounds ONE generation call. It is minutes, not seconds, because that is what
	// the work costs: a high-quality 1024px render is tens of seconds of provider compute, and a
	// timeout shorter than the work turns every press into a call that is BILLED AND DISCARDED —
	// the provider finishes and charges, we already hung up.
	defaultTimeout = 180 * time.Second

	// defaultMaxResponseBytes is the read ceiling for one response body.
	//
	// SIZING. The body is JSON carrying base64: a PNG inflates by 4/3, plus the envelope. A
	// 1024×1024 PNG from this model runs a few MiB, so 24 MiB leaves room for a
	// handful of variants without being a licence to allocate. It is NOT larger for a reason:
	// production runs on a 0.5 GiB instance, where the read buffer, the decoded bytes and whatever
	// the caller does next all coexist. The ceiling is the OOM guard, so it is tunable
	// (OPENROUTER_IMAGES_MAX_RESPONSE_BYTES) — a box that grows should not need a deploy, and a box
	// that is dying should be rescuable by lowering one number.
	defaultMaxResponseBytes = 24 << 20 // 24 MiB

	// MaxInputReferences is the provider's own cap for this model family (0..16, measured on
	// gpt-image-1, -1-mini and -2 alike on 2026-08-30 — the three agree).
	//
	// EXPORTED because the caller has to be able to ASK, and the alternative is a copy of the
	// number living next to every caller — which is two numbers as soon as one of them is edited.
	MaxInputReferences = 16

	// maxImagesPerRequest is the provider's cap on `n` (1..10, measured; the same on all three
	// GPT Image slugs).
	//
	// ⚠️ `n` PRODUCES n VARIANTS OF ONE PROMPT, NOT n DIFFERENT VIEWS. Front/back/side are three
	// prompts, i.e. three billed calls — or one composite picture that gets split afterwards. A
	// caller that reads `n` as "how many views" will pay for three copies of the same drawing.
	maxImagesPerRequest = 10

	// modelProbeTimeout bounds the startup model probe: short on purpose, the probe is a courtesy.
	modelProbeTimeout = 3 * time.Second
)

// ErrNotConfigured is returned when Generate is called with no API key. Callers should surface it
// as a clear "not configured" precondition failure — and, upstream, as a locked button rather than
// as a run that waits forever for a provider nobody can call.
var ErrNotConfigured = errors.New("orimages: no OpenRouter API key is set")

// ErrBadRequest is returned when the REQUEST WE BUILT is one the provider would refuse, and it is
// raised HERE, before the round trip, for the things we can be certain about locally: an empty
// prompt, `n` outside the provider's range, more reference pictures than the model accepts, and a
// reference that is not a fetchable url.
//
// ⚠ IT IS A SENTINEL RATHER THAN PROSE BECAUSE THE CALLER HAS TO TELL IT FROM WEATHER. An
// unrecognised error is classified as a transient provider fault and RETRIED — five times, by the
// attempt cap — and every one of those retries repeats the exact same unacceptable request. A
// request we built wrong does not improve by being sent again.
var ErrBadRequest = errors.New("orimages: the request is one the provider will refuse")

// ErrModelUnavailable is returned when the provider answers 404: the configured image slug is not
// one it serves — retired, renamed, never existing, or (the trap this package exists for) a CHAT
// slug pointed at the image endpoint. Nothing about any of those is transient, so a caller owes the
// human "the setting is wrong", not "try again in a moment".
//
// Classification is BY STATUS ALONE; no substring of the provider's English sentence is matched, so
// a reworded message cannot silently reclassify the fault.
var ErrModelUnavailable = errors.New("orimages: the configured image model is not available at the provider")

// ErrRateLimited is returned on HTTP 429. It is transient and the request was not billed, so it is
// the one fault where waiting genuinely helps — but the WAITING BELONGS TO THE CALLER, not to this
// client. See Generate.
var ErrRateLimited = errors.New("orimages: the provider is rate-limiting this account")

// ErrProviderFailure is returned on any 5xx. OpenRouter documents image generation as all-or-
// nothing — "a generation is either completed and billed in full, or it fails and is not billed",
// with failures surfacing as 502 — so a 5xx is the one non-2xx a caller may retry without paying
// twice for the same picture.
var ErrProviderFailure = errors.New("orimages: the provider failed to produce the image")

// ErrResponseTooLarge is returned when the response body exceeds the read ceiling.
//
// IT IS AN ERROR, NOT A TRIM, and for pictures that distinction is sharper than it is for text: a
// truncated base64 string still decodes — into a PREFIX OF AN IMAGE. That is a file that opens, is
// half grey, and is indistinguishable from a bad generation. The ceiling refuses instead, and names
// the knob (OPENROUTER_IMAGES_MAX_RESPONSE_BYTES) in the message.
var ErrResponseTooLarge = errors.New("orimages: the provider's response exceeded the read ceiling")

// ErrNoImages is returned when the provider answers 200 with an empty data array. It is separate
// from a transport fault because THE CALL WAS STILL BILLED: Generate returns the usage alongside
// this error precisely so the money reaches the ledger even though no picture did.
var ErrNoImages = errors.New("orimages: the provider returned no image")

// ErrUnauthorized is returned on HTTP 401/403: the key is missing, wrong, or not permitted here.
// Nothing was billed. It is named separately from the generic 4xx because it is the one fault an
// operator can fix in thirty seconds, and because "we do not know whether we were charged" is the
// wrong thing to tell a person whose key is simply absent.
var ErrUnauthorized = errors.New("orimages: the provider rejected the API key")

// ErrOutOfCredit is returned on HTTP 402: the account has no money left. Nothing was billed —
// that is the whole meaning of the status. It is separate from ErrUnauthorized because the remedy
// is different (top up, not re-key) and separate from ErrRateLimited because waiting does not help.
var ErrOutOfCredit = errors.New("orimages: the OpenRouter account is out of credit")

// Config is the image client configuration. Bound in config/cfg.go under `openrouter_images`;
// every field is optional except APIKey (without which the client is disabled).
type Config struct {
	// APIKey is the OpenRouter key. It is the SAME account as the chat client: the binding falls
	// back to OPENROUTER_API_KEY, so a working deployment needs no new secret at all. The
	// dedicated OPENROUTER_IMAGES_API_KEY exists only to let picture spend be moved to a separate
	// key later without touching code.
	APIKey string `mapstructure:"api_key"`
	// Model is the image slug (OPENROUTER_MODEL_IMAGE); empty = DefaultModel. It must come from
	// the IMAGE catalogue — a chat slug here answers 404, which is exactly ErrModelUnavailable.
	Model string `mapstructure:"model"`
	// BaseURL is the API root (OPENROUTER_IMAGES_BASE_URL, falling back to OPENROUTER_BASE_URL);
	// empty = defaultBaseURL.
	BaseURL string `mapstructure:"base_url"`
	// HTTPTimeout bounds one call (OPENROUTER_IMAGES_TIMEOUT); <= 0 = defaultTimeout.
	HTTPTimeout time.Duration `mapstructure:"http_timeout"`
	// MaxResponseBytes is the read ceiling (OPENROUTER_IMAGES_MAX_RESPONSE_BYTES); <= 0 =
	// defaultMaxResponseBytes.
	MaxResponseBytes int64 `mapstructure:"max_response_bytes"`
}

// Client is a configured OpenRouter image client. A nil *Client is a valid, permanently-disabled
// client (Enabled() == false), so callers need not nil-check.
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a client, applying defaults for model / base URL / timeout / ceiling. It does not
// validate the API key (an unset key just leaves the client disabled).
func New(cfg Config) *Client {
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = DefaultModel
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaultTimeout
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.HTTPTimeout}}
}

// Enabled reports whether an API key is configured. Nil-safe.
func (c *Client) Enabled() bool {
	return c != nil && strings.TrimSpace(c.cfg.APIKey) != ""
}

// Model returns the effective image model slug (for response provenance). Nil-safe.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.cfg.Model
}

// BaseURL returns the effective API root. Nil-safe.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.cfg.BaseURL
}

// MaxResponseBytes returns the effective read ceiling. Nil-safe.
func (c *Client) MaxResponseBytes() int64 {
	if c == nil {
		return 0
	}
	return c.cfg.MaxResponseBytes
}

// Request is one image generation. Only Prompt is required; every other field omits itself from the
// wire when zero, leaving the provider's own default in force.
//
// The fields are the provider's, spelled the provider's way, because a translation layer over an
// enum that changes per model is a place for a value to rot. What each model actually accepts is
// published at GET /api/v1/images/models — this package deliberately does not carry a copy.
type Request struct {
	// Model overrides the client's configured slug for this one call. Empty = the configured slug.
	// It exists so a caller that needs the vector model (recraft/*) can reach it through the same
	// endpoint, since the endpoint really is the same one.
	Model string
	// Prompt is the instruction. Required.
	Prompt string
	// N is how many images to return, 1..10. Zero omits the field (provider default: 1).
	//
	// ⚠️ n VARIANTS OF ONE PROMPT, NOT n VIEWS. See maxImagesPerRequest.
	N int
	// AspectRatio is the provider's `aspect_ratio` enum. Empty omits it. Note this family takes
	// RATIOS, not pixel sizes, and THE SET IS PER-MODEL: gpt-image-2 (the default) takes
	// "1:1" "3:2" "2:3" "4:3" "3:4" "16:9" "9:16" "21:9" "auto"; gpt-image-1 and -1-mini take only
	// "1:1" "3:2" "2:3" "auto". A ratio valid here is not automatically valid on a slug set by
	// OPENROUTER_MODEL_IMAGE.
	AspectRatio string
	// Quality is "auto" | "low" | "medium" | "high" — the same four on every GPT Image slug
	// (measured 2026-08-30), so moving between them does not rename the dial. Empty omits it.
	//
	// IT IS THE PRICE DIAL, and the multiple is a TOKEN COUNT, not a rate: the catalogue prices one
	// output token, and quality decides how many of them a picture is. On gpt-image-1 that made
	// `high` roughly four times `medium`; OpenRouter publishes no per-quality token count for
	// gpt-image-2, so treat the ratio as unmeasured-but-similar rather than as gone. Whatever it
	// is, it must agree with what the caller reserved — see designgen.Config.ImageQuality.
	Quality string
	// Background is "auto" | "opaque" on the DEFAULT model, and additionally "transparent" on
	// gpt-image-1 / -1-mini. Empty omits it and leaves the provider's own default in force.
	//
	// ⚠ "transparent" IS A 400 AGAINST DefaultModel — gpt-image-2 does not list it (measured). That
	// is deliberate and not a loss: the design band asks for a background in the PROMPT ("a plain
	// white background", "white seamless background"), and the raster is vectorised afterwards,
	// where no background travels. A caller still passing "transparent" is asking for a fault, and
	// this package does NOT silently rewrite it — a dropped parameter is a picture that differs
	// from the one that was ordered, discovered much later.
	Background string
	// OutputFormat is "png" | "jpeg" | "webp" (and "svg" on Recraft vector models). Empty omits it.
	//
	// NOTE that no GPT Image slug lists `output_format` among its supported parameters at all —
	// not -1, not -1-mini, not -2 (measured 2026-08-30). designgen has been sending "png" against
	// gpt-image-1 regardless, so whatever the provider does with an unlisted key it has been doing
	// all along; the move to gpt-image-2 changes nothing here. It IS honoured on the Recraft vector
	// models this same endpoint reaches, which is why the field stays.
	OutputFormat string
	// OutputCompression is 0..100 for webp/jpeg. Nil omits it. It is a POINTER because 0 is a
	// meaningful value here and "unset" has to be distinguishable from it.
	OutputCompression *int
	// InputReferences are the pictures the model should look at: http(s) URLs the PROVIDER fetches,
	// or data: URIs when the caller only has bytes. 0..16.
	//
	// URLs are the cheap path and the intended one — our own public media addresses go straight
	// across, with no download, no re-encode and no base64 inflation through a 0.5 GiB process.
	InputReferences []string
}

// Image is one picture the provider returned.
type Image struct {
	// Bytes is the DECODED image. Base64 stays inside this package; a caller streaming this to a
	// bucket should never have to know how it arrived.
	Bytes []byte
	// MediaType is the provider's `media_type` ("image/png", "image/svg+xml", …). When the
	// provider omits it — documented as "only when it could not be determined" — it is sniffed
	// from the bytes, so this is never empty for anything a browser can display.
	MediaType string
}

// Usage is the accounting for one generation.
//
// COST IS THE FIELD THAT MATTERS. It is the provider's own charge in USD for this call — the only
// honest price for the run ledger, as opposed to a token count multiplied by a rate table that ages
// silently. A MISSING OR MISSPELLED TAG HERE IS INVISIBLE: the field stays zero, the call still
// succeeds, and every generation reads as free. That is why the tests assert non-zero numbers
// rather than "no error".
type Usage struct {
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	Total      int     `json:"total_tokens"`
	Cost       float64 `json:"cost"`
}

// Result is one completed generation.
type Result struct {
	// Model is the slug this call was sent to (provenance for the attempt row).
	Model string
	// Images are the returned pictures, decoded.
	Images []Image
	// Usage is the token accounting and, above all, the provider's own USD cost.
	Usage Usage
}

// --- wire types ---

type imageRequestWire struct {
	Model             string   `json:"model"`
	Prompt            string   `json:"prompt"`
	N                 int      `json:"n,omitempty"`
	AspectRatio       string   `json:"aspect_ratio,omitempty"`
	Quality           string   `json:"quality,omitempty"`
	Background        string   `json:"background,omitempty"`
	OutputFormat      string   `json:"output_format,omitempty"`
	OutputCompression *int     `json:"output_compression,omitempty"`
	InputReferences   []string `json:"input_references,omitempty"`
}

type apiError struct {
	Message string `json:"message"`
	Code    any    `json:"code"`
	Type    string `json:"type"`
}

type imageResponseWire struct {
	Created int64 `json:"created"`
	Data    []struct {
		B64JSON   string `json:"b64_json"`
		MediaType string `json:"media_type"`
	} `json:"data"`
	Usage Usage     `json:"usage"`
	Error *apiError `json:"error"`
}

// Generate performs ONE image generation and returns the decoded pictures with the provider's own
// cost, or a classified error.
//
// IT DOES NOT RETRY, AND THAT IS THE DESIGN. OpenRouter does not promise idempotency on this
// endpoint, so a retried request MAY be a second generation at a second price — and a retry hidden
// inside an HTTP client pays it outside the caller's attempt accounting, where nothing counts it
// and nothing caps it. Retrying is a decision about money; it belongs to whoever owns the attempt
// row and the budget, with the sentinels above (ErrRateLimited, ErrProviderFailure) telling it
// which faults are even eligible.
//
// ON A BILLED FAILURE THE MONEY STILL COMES BACK. A 200 that carries no pictures (ErrNoImages) or
// pictures that will not decode returns a non-nil *Result carrying Usage TOGETHER WITH the error:
// the call was charged, and a ledger that records only successes under-reports spend in exactly the
// case where spend was wasted. The caller writes the price and fails the run, publishing nothing.
// Every other error path returns a nil result, because nothing reached the provider or nothing was
// charged.
func (c *Client) Generate(ctx context.Context, req Request) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("orimages: marshal request: %w", err)
	}

	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/images"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("orimages: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	httpReq.Header.Set("X-Title", "grbpwr-products-manager")
	httpReq.Header.Set("HTTP-Referer", "https://admin.grbpwr.com")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("orimages: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readCapped(resp.Body, c.cfg.MaxResponseBytes, "image response")
	if err != nil {
		return nil, fmt.Errorf("orimages: read response: %w", err)
	}
	if err := classifyStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var ir imageResponseWire
	if err := json.Unmarshal(body, &ir); err != nil {
		return nil, fmt.Errorf("orimages: could not decode the image response envelope: %w", err)
	}
	// A 200 whose body carries an error object: rare, but the chat side has seen it, and treating
	// it as "no images" would blame the wrong thing.
	if ir.Error != nil && strings.TrimSpace(ir.Error.Message) != "" {
		return nil, fmt.Errorf("orimages: API error: %s", ir.Error.Message)
	}
	if len(ir.Data) == 0 {
		// The usage rides along: this call was billed, and this is the only place that spend would
		// otherwise vanish from.
		return &Result{Model: wire.Model, Usage: ir.Usage}, ErrNoImages
	}

	out := &Result{Model: wire.Model, Usage: ir.Usage, Images: make([]Image, 0, len(ir.Data))}
	for i, d := range ir.Data {
		raw, err := decodeB64(d.B64JSON)
		if err != nil {
			// Billed and broken: same rule as the empty-data case above — the usage goes back with
			// the error, and the caller publishes nothing. Images are dropped rather than returned
			// half-decoded, because a partial set is indistinguishable from a complete one once it
			// leaves this function.
			return &Result{Model: wire.Model, Usage: ir.Usage}, fmt.Errorf("orimages: image %d: %w", i+1, err)
		}
		if len(raw) == 0 {
			return &Result{Model: wire.Model, Usage: ir.Usage}, fmt.Errorf("orimages: image %d carried no bytes", i+1)
		}
		out.Images = append(out.Images, Image{Bytes: raw, MediaType: mediaTypeOf(d.MediaType, raw)})
	}
	return out, nil
}

// buildRequest validates the caller's Request and turns it into the wire body. Validation happens
// BEFORE the network on purpose: an out-of-range `n` or a nineteenth reference is our mistake, and
// finding it here costs nothing, while finding it at the provider costs a round trip and reads in
// the log like a provider fault.
func (c *Client) buildRequest(req Request) (imageRequestWire, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.cfg.Model
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return imageRequestWire{}, fmt.Errorf("%w: a generation needs a prompt", ErrBadRequest)
	}
	if req.N < 0 || req.N > maxImagesPerRequest {
		return imageRequestWire{}, fmt.Errorf("%w: n=%d is outside the supported range 1..%d",
			ErrBadRequest, req.N, maxImagesPerRequest)
	}
	// THE COUNT IS REFUSED, NEVER TRIMMED, and the caller is refused rather than the tail. A
	// silently shortened list produces a picture composed from less than what the run's frozen
	// snapshot says went into it — and the snapshot is the only record there will ever be of what
	// was asked. "Some of the inputs" filed as "all of them" is a lie a person cannot detect.
	if len(req.InputReferences) > MaxInputReferences {
		return imageRequestWire{}, fmt.Errorf("%w: %d reference pictures exceeds the %d the model accepts",
			ErrBadRequest, len(req.InputReferences), MaxInputReferences)
	}
	refs := make([]string, 0, len(req.InputReferences))
	for i, raw := range req.InputReferences {
		u := strings.TrimSpace(raw)
		if err := validateReference(u); err != nil {
			return imageRequestWire{}, fmt.Errorf("%w: reference %d: %v", ErrBadRequest, i+1, err)
		}
		refs = append(refs, u)
	}
	if len(refs) == 0 {
		refs = nil // omit the key entirely rather than sending an empty array
	}
	if req.OutputCompression != nil && (*req.OutputCompression < 0 || *req.OutputCompression > 100) {
		return imageRequestWire{}, fmt.Errorf("orimages: output_compression=%d is outside 0..100",
			*req.OutputCompression)
	}
	return imageRequestWire{
		Model:             model,
		Prompt:            prompt,
		N:                 req.N,
		AspectRatio:       strings.TrimSpace(req.AspectRatio),
		Quality:           strings.TrimSpace(req.Quality),
		Background:        strings.TrimSpace(req.Background),
		OutputFormat:      strings.TrimSpace(req.OutputFormat),
		OutputCompression: req.OutputCompression,
		InputReferences:   refs,
	}, nil
}

// validateReference admits exactly the two forms the provider documents and rejects the rest. The
// provider fetches these addresses from ITS network, so an unvalidated string here is an outbound
// request we authored on someone else's behalf.
func validateReference(u string) error {
	switch {
	case u == "":
		return fmt.Errorf("empty reference address")
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return nil
	case strings.HasPrefix(u, "data:image/"):
		if !strings.Contains(u, ",") {
			return fmt.Errorf("data URI carries no payload")
		}
		return nil
	default:
		return fmt.Errorf("reference must be http(s):// or a data:image/… URI, got %q", truncate(u, 40))
	}
}

// classifyStatus turns an HTTP status into one of this package's sentinels, or nil for 2xx.
//
// THE SPLIT IS BY STATUS, NOT BY MESSAGE TEXT, and it exists so a caller can answer one question
// without reading English: may this be tried again, and would trying again cost money? 404, 401,
// 403 and 402 are settings faults (retrying repeats them for ever) and each names its own remedy;
// 429 and 5xx are weather that the provider documents as unbilled.
//
// 401/403 and 402 were once folded into the bare default branch, which reads to a caller as "an
// error we cannot classify" — i.e. "we may or may not have been charged". For a missing key and an
// empty balance that is false and expensive: both are provably unbilled, and both have a remedy a
// person can act on. Telling the truth here is what keeps a worker from burning attempts against a
// key that will never work.
func classifyStatus(status int, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: API error (HTTP %d): %s", ErrModelUnavailable, status, apiErrorMessage(body))
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: API error (HTTP %d): %s", ErrUnauthorized, status, apiErrorMessage(body))
	case status == http.StatusPaymentRequired:
		return fmt.Errorf("%w: API error (HTTP %d): %s", ErrOutOfCredit, status, apiErrorMessage(body))
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: API error (HTTP %d): %s", ErrRateLimited, status, apiErrorMessage(body))
	case status >= 500:
		return fmt.Errorf("%w: API error (HTTP %d): %s", ErrProviderFailure, status, apiErrorMessage(body))
	default:
		return fmt.Errorf("orimages: API error (HTTP %d): %s", status, apiErrorMessage(body))
	}
}

// decodeB64 decodes the provider's base64 payload, tolerating the unpadded form. Standard padded
// base64 is what the API sends; the raw fallback costs one branch and removes a class of "works in
// the test, fails on the wire" surprise.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("the provider returned an empty b64_json field")
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("b64_json was not valid base64: %w", err)
	}
	return raw, nil
}

// mediaTypeOf prefers the provider's own label and sniffs the bytes only when it is absent — the
// docs say media_type is "omitted only when it could not be determined". Sniffing is a fallback,
// never an override: the provider knows an SVG is an SVG, while http.DetectContentType will happily
// call it XML or plain text.
func mediaTypeOf(declared string, raw []byte) string {
	if d := strings.TrimSpace(declared); d != "" {
		return d
	}
	if bytes.Contains(raw[:min(len(raw), 256)], []byte("<svg")) {
		return "image/svg+xml"
	}
	return http.DetectContentType(raw)
}

// readCapped reads at most limit bytes and REFUSES anything longer instead of returning a prefix.
// It mirrors the helper of the same name in internal/openrouter — deliberately duplicated rather
// than shared, so neither package's ceiling can be moved by an edit aimed at the other.
//
// The extra byte is the whole trick: without reading limit+1 there is no way to tell a body that
// ends exactly at the ceiling from one that was cut at it.
func readCapped(r io.Reader, limit int64, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes (raise OPENROUTER_IMAGES_MAX_RESPONSE_BYTES)",
			ErrResponseTooLarge, what, limit)
	}
	return body, nil
}

// apiErrorMessage best-effort pulls a human message out of an OpenRouter error body, falling back
// to the raw (truncated) body when it is not the expected shape.
func apiErrorMessage(body []byte) string {
	var env struct {
		Error   *apiError `json:"error"`
		Message string    `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
			return env.Error.Message
		}
		if strings.TrimSpace(env.Message) != "" {
			return env.Message
		}
	}
	return truncate(strings.TrimSpace(string(body)), 300)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- startup model probe ---
//
// Same shape, same reasoning and same silence contract as internal/openrouter's probe: a retired
// slug is invisible until somebody presses a button, the provider will publish the fact for free,
// and a boot check that can delay or fail a start is worse than the fault it reports.
//
// WHAT IS NOT THE SAME IS THE RESPONSE SHAPE, and copying the chat probe's struct would have made
// this probe permanently silent. The chat route answers {"data":{"endpoints":[…]}}; the image route
// answers {"id":…,"endpoints":[…]} — endpoints at the TOP LEVEL, with no data wrapper (measured
// against the live API). A struct expecting `data` would decode any image response to "no endpoints
// field", land in the silent branch, and never warn about anything.

// imageModelEndpointsResponse is the shape of GET /images/models/{slug}/endpoints.
//
// Endpoints is a POINTER to a slice, and that is the safety of the probe: `endpoints: []` is the
// alarm, so a body that does not carry the key at all — a reshaped API, an HTML page from a proxy —
// must not decode to "zero endpoints" and shout.
type imageModelEndpointsResponse struct {
	Endpoints *[]json.RawMessage `json:"endpoints"`
}

// CheckModel asks the provider whether the effective IMAGE slug has any live endpoint, via
// GET {base}/images/models/{slug}/endpoints.
//
// Returns ErrModelUnavailable when the slug has no endpoints or does not exist (404 — which is also
// what a chat slug gets here, the single most likely misconfiguration of this package), nil when it
// has at least one, ErrNotConfigured when the client is disabled, and an ordinary error for every
// "could not find out", which callers must treat as silence rather than as bad news.
func (c *Client) CheckModel(ctx context.Context) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	return c.checkModel(ctx, c.cfg.Model)
}

func (c *Client) checkModel(ctx context.Context, model string) error {
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/images/models/" + strings.TrimSpace(model) + "/endpoints"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("orimages: build model probe: %w", err)
	}
	// No Authorization header: the route is public, and sending the key would only add ways to get
	// a 401 that says nothing about the model.
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("orimages: model probe failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := readCapped(resp.Body, c.cfg.MaxResponseBytes, "model probe response")
	if err != nil {
		return fmt.Errorf("orimages: read model probe: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %q is not an image model the provider knows (a CHAT slug looks exactly like this)",
			ErrModelUnavailable, model)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("orimages: model probe (HTTP %d): %s", resp.StatusCode, apiErrorMessage(body))
	}
	var mr imageModelEndpointsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return fmt.Errorf("orimages: could not decode model probe: %w", err)
	}
	if mr.Endpoints == nil {
		return fmt.Errorf("orimages: model probe carried no endpoints field")
	}
	if len(*mr.Endpoints) == 0 {
		return fmt.Errorf("%w: %q has no live endpoints at the provider", ErrModelUnavailable, model)
	}
	return nil
}

// WarnIfModelRetired probes the effective image slug in the BACKGROUND and shouts in the log if the
// provider serves no endpoint for it. It returns immediately and is safe to call from a start-up
// path: the goroutine, the timeout and the recover are owned here so no call site can forget them.
//
// It changes nothing and refuses nothing. Anything other than a clear verdict is silence — a boot
// that cannot reach the network is not evidence that a model is gone, and a false alarm on this
// line would teach people to ignore the true one.
func (c *Client) WarnIfModelRetired() {
	if !c.Enabled() {
		return // no key: nothing is calling the provider anyway
	}
	model := c.cfg.Model
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Default().Warn("orimages model probe panicked", slog.Any("recovered", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), modelProbeTimeout)
		defer cancel()
		if err := c.checkModel(ctx, model); errors.Is(err, ErrModelUnavailable) {
			slog.Default().Error(
				"OPENROUTER IMAGE MODEL IS NOT SERVED — design generation will refuse",
				slog.String("model", model),
				slog.String("base_url", c.BaseURL()),
				slog.String("err", err.Error()))
		}
	}()
}
