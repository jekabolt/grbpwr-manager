package recraft

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DirectClient is the FALLBACK transport: Recraft's own API, spoken directly.
//
// The primary route is OpenRouter (owner rule P-5, and Recraft is present there). This client is
// kept behind RECRAFT_ROUTE=direct for the one capability the OpenRouter image endpoint does not
// expose: `strength`, the dial that says how far the redraw may depart from the approved raster.
// When the redraw comes back as "a different garment", that dial is the fix, and having no way to
// reach it would mean the requirement fails with nowhere to turn.
//
// Unlike Meshy — which has no OpenRouter presence at all, so a direct client is forced and that
// deviation is stated out loud — Recraft has no such excuse. This must stay a switch nobody flips
// by default.
type DirectClient struct {
	cfg  DirectConfig
	http *http.Client
}

// DirectConfig configures the fallback route. Every field needs its own viper.BindEnv line; see
// Config for why.
type DirectConfig struct {
	APIKey string `mapstructure:"api_key"` // RECRAFT_API_KEY; empty = this route is disabled
	// BaseURL — RECRAFT_BASE_URL; empty = defaultDirectBaseURL. Overridable so a contour can be
	// pointed at a stub without a deploy (and so tests can point it at httptest).
	BaseURL string `mapstructure:"base_url"`
	// HTTPTimeout — RECRAFT_HTTP_TIMEOUT; <=0 = defaultDirectTimeout.
	HTTPTimeout time.Duration `mapstructure:"http_timeout"`
	// CreditUSD — RECRAFT_CREDIT_USD; <=0 = defaultCreditUSD. Only converts the reported credits
	// into money; the raw credit count is reported unconverted alongside it.
	CreditUSD float64 `mapstructure:"credit_usd"`
}

const (
	// defaultDirectBaseURL is the Recraft external API root.
	defaultDirectBaseURL = "https://external.api.recraft.ai/v1"

	// imageToImagePath is THE ONLY generation route this package knows. Its sibling
	// /images/vectorize is the forbidden tracer; see the package doc.
	imageToImagePath = "/images/imageToImage"

	// defaultDirectTimeout bounds one generation call end to end. Vector generation is slower than a
	// chat completion — tens of seconds is normal, and the pro model is slower than the standard one.
	defaultDirectTimeout = 120 * time.Second

	// maxResponseBytes caps the JSON envelope we are willing to read. It is deliberately larger than
	// MaxSVGBytes: with response_format=b64_json the SVG travels base64-encoded inside this body and
	// grows by a third. Hitting the cap is REPORTED, never silently truncated — a silent cap is how
	// a 4 MiB limit turns a big picture into an unexplained parse error.
	maxResponseBytes = 12 << 20 // 12 MiB

	// defaultStrength is how far the model may depart from the approved raster.
	//
	// THE SCALE IS DIFFERENCE, NOT SIMILARITY (provider docs: "Defines the difference with the
	// original image, should lie in [0, 1], where 0 means almost identical, and 1 means miserable
	// similarity"). Reading it backwards would silently produce a different garment, which is worse
	// than an error because it looks like a result.
	//
	// The raster we send has already been approved by a human: the job is "the same garment, drawn
	// in vector", not "another take on it". Hence a low value, overridable per call for the case
	// where the source is a rough sketch rather than an approved flat.
	defaultStrength = 0.25

	// imagesPerCall is fixed at one, and it is a money decision, not a limitation: n>1 multiplies
	// the price of a press by n, and the band shows one picture per attempt anyway.
	imagesPerCall = 1

	// maxDownloadAttempts / downloadBackoff bound retries of the FREE, IDEMPOTENT part only —
	// fetching the produced SVG from the link the provider hands back. The paid POST above it is
	// never retried. Provider links are also short-lived, so "try again later" is no substitute for
	// retrying here and now: the picture has already been paid for.
	maxDownloadAttempts = 3
	downloadBackoff     = 250 * time.Millisecond

	// defaultCreditUSD is the published value of one API unit ($1.00 = 1000 units, verified
	// 2026-08-30), i.e. 80 units = $0.08 for V4 Vector and 300 = $0.30 for V4 Pro Vector. It only
	// translates `credits` into money for the ledger; when the provider changes its rate,
	// RECRAFT_CREDIT_USD overrides it without a deploy, and Credits stays the raw truth either way.
	defaultCreditUSD = 0.001
)

// newDirectClient builds the fallback transport, applying defaults. An empty API key leaves it
// disabled rather than broken: the service then refuses up front with ErrNotConfigured.
func newDirectClient(cfg DirectConfig) *DirectClient {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultDirectBaseURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.CreditUSD <= 0 {
		cfg.CreditUSD = defaultCreditUSD
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultDirectTimeout
	}
	return &DirectClient{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// NewDirect exposes the fallback transport on its own, for a caller that wires routes itself.
func NewDirect(cfg DirectConfig) *DirectClient { return newDirectClient(cfg) }

// Enabled reports whether an API key is configured. Nil-safe.
func (c *DirectClient) Enabled() bool { return c != nil && c.cfg.APIKey != "" }

// BaseURL returns the effective API root. It exists for LOG LINES: a 404 can mean the model id is
// gone or that the base URL points at something without this route, and a log naming only the model
// sends the reader to the wrong knob. Nil-safe.
func (c *DirectClient) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.cfg.BaseURL
}

// CreditsUSD converts a provider credit count into money at the configured rate. Nil-safe.
func (c *DirectClient) CreditsUSD(credits float64) float64 {
	if c == nil || credits <= 0 {
		return 0
	}
	return credits * c.cfg.CreditUSD
}

// GenerateImage performs THE PAID CALL against POST {base}/images/imageToImage. Exactly once.
//
// NO RETRY LIVES HERE, and that is a deliberate refusal, not an omission. The provider does not
// promise idempotency on this route, so a repeat may render and bill a second image; a retry hidden
// inside an HTTP client would spend that money OUTSIDE the attempt ledger, where nobody could see
// it. Retrying is the worker's decision, taken against `next_attempt_at` with a cap of two, and
// with the previous attempt recorded — including the `unknown` case where we were charged for a
// picture that never reached us.
func (c *DirectClient) GenerateImage(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("%w: RECRAFT_API_KEY is not set", ErrNotConfigured)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("%w: no model id given", ErrBadRequest)
	}
	if req.Image.IsEmpty() {
		return nil, fmt.Errorf("%w: imageToImage needs an input image", ErrBadRequest)
	}
	strength := defaultStrength
	if req.Strength != nil {
		strength = *req.Strength
	}
	if strength < 0 || strength > 1 {
		return nil, fmt.Errorf("%w: strength %.3f is outside [0,1]", ErrBadRequest, strength)
	}

	env, err := c.postImageToImage(ctx, req, strength)
	if err != nil {
		return nil, err
	}
	// MONEY FIRST. Everything below this line happens AFTER the provider billed us: an envelope
	// with no image, an undecodable payload, a link that will not open. The run still fails, but
	// the charge is real and rides out with the error (see ChargedError).
	charged := func(err error) error { return wrapCharged(err, c.CreditsUSD(env.Credits), env.Credits, req.Model) }
	if len(env.Data) == 0 {
		return nil, charged(fmt.Errorf("%w: the response carries no image", ErrInvalidResponse))
	}
	raw, contentType, sourceURL, err := c.materialize(ctx, env.Data[0])
	if err != nil {
		return nil, charged(err)
	}
	return &GenerateResponse{
		Bytes:       raw,
		ContentType: contentType,
		Model:       strings.TrimSpace(req.Model),
		CostUSD:     c.CreditsUSD(env.Credits),
		Credits:     env.Credits,
		SourceURL:   sourceURL,
	}, nil
}

// apiEnvelope is the provider's response shape, with only the fields we read.
type apiEnvelope struct {
	Data []apiImage `json:"data"`
	// Credits is what the call cost, in API units. Integral in practice; float64 accepts both an
	// integer and a fractional count without a custom unmarshaller.
	Credits float64 `json:"credits"`
}

type apiImage struct {
	URL     string `json:"url"`
	B64JSON string `json:"b64_json"`
	ImageID string `json:"image_id"`
}

func (c *DirectClient) postImageToImage(ctx context.Context, req GenerateRequest, strength float64) (*apiEnvelope, error) {
	httpReq, err := c.buildRequest(ctx, req, strength)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Transport failure: the request may or may not have been served and billed. Say so.
		return nil, fmt.Errorf("%w: %v", ErrProviderFailure, err)
	}
	defer resp.Body.Close()

	body, truncated, err := readCapped(resp.Body, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %v", ErrProviderFailure, err)
	}
	if truncated {
		return nil, fmt.Errorf("%w: response exceeded %d bytes", ErrInvalidResponse, maxResponseBytes)
	}
	if err := classifyStatus(resp.StatusCode, body); err != nil {
		return nil, err
	}
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return &env, nil
}

// buildRequest assembles the imageToImage call: JSON when the provider can fetch the picture itself
// (the cheap path), multipart when we have to hand over the bytes.
//
// The bytes path is multipart rather than a base64 data URL on purpose: base64 inflates the picture
// by a third inside a process with 0.5 GiB of RAM.
func (c *DirectClient) buildRequest(ctx context.Context, req GenerateRequest, strength float64) (*http.Request, error) {
	endpoint := c.cfg.BaseURL + imageToImagePath

	var (
		body        io.Reader
		contentType string
	)
	if len(req.Image.Bytes) > 0 {
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		filename := strings.TrimSpace(req.Image.Filename)
		if filename == "" {
			filename = "source"
		}
		part, err := mw.CreateFormFile("image", filename)
		if err != nil {
			return nil, fmt.Errorf("recraft: building multipart body: %w", err)
		}
		if _, err := part.Write(req.Image.Bytes); err != nil {
			return nil, fmt.Errorf("recraft: building multipart body: %w", err)
		}
		for k, v := range formFields(req, strength) {
			if err := mw.WriteField(k, v); err != nil {
				return nil, fmt.Errorf("recraft: building multipart body: %w", err)
			}
		}
		if err := mw.Close(); err != nil {
			return nil, fmt.Errorf("recraft: building multipart body: %w", err)
		}
		body, contentType = buf, mw.FormDataContentType()
	} else {
		payload := map[string]any{"image_url": strings.TrimSpace(req.Image.URL)}
		for k, v := range formFields(req, strength) {
			payload[k] = v
		}
		// Numbers travel as numbers on the JSON path; the form path has only strings.
		payload["strength"] = strength
		payload["n"] = imagesPerCall
		if req.Seed != nil {
			payload["random_seed"] = *req.Seed
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("recraft: encoding request: %w", err)
		}
		body, contentType = bytes.NewReader(raw), "application/json"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("recraft: building request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "application/json")
	return httpReq, nil
}

// formFields are the parameters shared by both encodings, as strings.
//
// response_format=b64_json asks for the picture INSIDE the answer. That is the preferred delivery:
// one round trip instead of two, and it cannot expire between them. The provider is free to ignore
// it and hand back a link, which is what materialize handles.
func formFields(req GenerateRequest, strength float64) map[string]string {
	f := map[string]string{
		"prompt":          strings.TrimSpace(req.Prompt),
		"model":           strings.TrimSpace(req.Model),
		"strength":        strconv.FormatFloat(strength, 'f', -1, 64),
		"n":               strconv.Itoa(imagesPerCall),
		"response_format": "b64_json",
	}
	if s := strings.TrimSpace(req.NegativePrompt); s != "" {
		f["negative_prompt"] = s
	}
	if req.Seed != nil {
		f["random_seed"] = strconv.FormatInt(*req.Seed, 10)
	}
	return f
}

// materialize turns one response item into bytes: decoded from the envelope when the provider
// inlined it, downloaded when it handed back a link instead.
func (c *DirectClient) materialize(ctx context.Context, item apiImage) (raw []byte, contentType, sourceURL string, err error) {
	if b64 := strings.TrimSpace(item.B64JSON); b64 != "" {
		decoded, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil {
			return nil, "", "", fmt.Errorf("%w: undecodable b64_json: %v", ErrInvalidResponse, derr)
		}
		if len(decoded) > MaxSVGBytes {
			return nil, "", "", fmt.Errorf("%w: image is %d bytes, over the %d cap", ErrInvalidResponse, len(decoded), MaxSVGBytes)
		}
		// No content type travels with an inlined picture; the bytes are inspected by the caller.
		return decoded, "", "", nil
	}
	link := strings.TrimSpace(item.URL)
	if link == "" {
		return nil, "", "", fmt.Errorf("%w: the response item carries neither b64_json nor url", ErrInvalidResponse)
	}
	decoded, ct, err := c.download(ctx, link)
	if err != nil {
		return nil, "", link, err
	}
	return decoded, ct, link, nil
}

// download fetches the produced picture from the provider's link.
//
// THIS is where retries live, and the reason they are allowed here and nowhere else: a GET of an
// already-produced image is free and idempotent, so a repeat cannot charge us twice. It is also
// necessary — the picture has already been paid for, and giving up on the first hiccup would throw
// away money that has already left the account.
func (c *DirectClient) download(ctx context.Context, link string) ([]byte, string, error) {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, "", fmt.Errorf("%w: the response url %q is not an http(s) address", ErrInvalidResponse, link)
	}

	var lastErr error
	for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
		if attempt > 1 {
			timer := time.NewTimer(downloadBackoff * time.Duration(1<<(attempt-2)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, "", fmt.Errorf("%w: %v", ErrProviderFailure, ctx.Err())
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return nil, "", fmt.Errorf("recraft: building download request: %w", err)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: downloading the produced image: %v", ErrProviderFailure, err)
			continue
		}
		raw, truncated, readErr := readCapped(resp.Body, MaxSVGBytes)
		status := resp.StatusCode
		ct := resp.Header.Get("Content-Type")
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%w: downloading the produced image: %v", ErrProviderFailure, readErr)
			continue
		}
		if truncated {
			// Not retryable: a second identical download produces the same oversized file.
			return nil, "", fmt.Errorf("%w: the produced image exceeds %d bytes", ErrInvalidResponse, MaxSVGBytes)
		}
		if status >= 200 && status < 300 {
			return raw, ct, nil
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = fmt.Errorf("%w: downloading the produced image (HTTP %d)", ErrProviderFailure, status)
			continue
		}
		// A 4xx on a link we were just handed means it is gone or was never valid; a retry repeats it.
		return nil, "", fmt.Errorf("%w: the produced image is not retrievable (HTTP %d)", ErrInvalidResponse, status)
	}
	return nil, "", lastErr
}

// readCapped reads at most limit bytes and reports whether there was more. The extra byte is the
// whole point: without it, a body of exactly limit bytes and a body of a gigabyte look identical.
func readCapped(r io.Reader, limit int64) (data []byte, truncated bool, err error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return nil, true, nil
	}
	return b, false, nil
}

// classifyStatus maps an HTTP status onto one of the package sentinels. BY STATUS ALONE: the
// provider's prose is quoted into the message for a human to read, never matched on to decide what
// happened, so a reworded message cannot silently reclassify a fault.
func classifyStatus(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}
	msg := apiErrorMessage(body)
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d): %s", ErrUnauthorized, status, msg)
	case status == http.StatusPaymentRequired:
		return fmt.Errorf("%w (HTTP %d): %s", ErrInsufficientCredits, status, msg)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w (HTTP %d): %s", ErrRateLimited, status, msg)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w (HTTP %d): %s", ErrModelUnavailable, status, msg)
	case status >= 500:
		return fmt.Errorf("%w (HTTP %d): %s", ErrProviderFailure, status, msg)
	default:
		return fmt.Errorf("%w (HTTP %d): %s", ErrBadRequest, status, msg)
	}
}

// apiErrorMessage digs a human sentence out of an error body, trying the shapes providers actually
// use, and falls back to a bounded slice of the raw body so a log never says just "error".
func apiErrorMessage(body []byte) string {
	var shaped struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Code    string `json:"code"`
		Error   struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &shaped) == nil {
		for _, s := range []string{shaped.Error.Message, shaped.Message, shaped.Detail, shaped.Error.Code, shaped.Code} {
			if t := strings.TrimSpace(s); t != "" {
				return t
			}
		}
	}
	t := strings.TrimSpace(string(body))
	if t == "" {
		return "(empty body)"
	}
	if len(t) > 512 {
		return t[:512] + "…"
	}
	return t
}
