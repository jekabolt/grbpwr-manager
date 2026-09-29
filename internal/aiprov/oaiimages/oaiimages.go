// Package oaiimages is the image.generate transport of every OpenAI-shaped IMAGES API the stack talks
// to directly: OpenAI itself and apibost (which relays OpenAI's image models and a handful of other
// vendors' behind the same two endpoints). It revives B-25/B-26: the route rows
// `image.generate → openai / gpt-image-2` and `→ apibost / dall-e-3 | gpt-image-2 | flux-kontext-pro …`
// now run. It satisfies designgen.ImageTransport, as runblob.Images and *orimages.Client do.
//
// ONE PAID CALL, TWO ENDPOINTS:
//
//	POST {base}/images/generations   JSON — a prompt and nothing to look at;
//	POST {base}/images/edits         multipart — the same prompt plus the reference pictures as files.
//
// Both answer {data:[{b64_json | url}], usage?}. gpt-image-* always return base64 and REFUSE a
// `response_format`; dall-e-* default to a url, so they are asked for base64 explicitly; a relayed
// vendor answers whichever shape it answers — both are handled, a url downloaded at once (a link stored
// instead of bytes is a picture that disappears) with no key on that request.
//
// THE DIALS ARE PER FAMILY, read off the slug, never off the provider: apibost's gpt-image-2 speaks
// exactly OpenAI's gpt-image-2. A relayed non-OpenAI slug (flux-kontext-pro, gemini-2.5-flash-image,
// grok-3-image) gets only {model, prompt, n, size} — a relay refuses a dial it does not know with a 400.
//
// ⚠ THE MONEY BOUNDARY IS THE WIRE (aiprov.ObserveWrite), as in oaichat and orimages. Before the request
// is written every refusal is free (no key, a slug this transport does not draw, a reference it cannot
// vouch for, a connection that never opened); a non-2xx is the provider refusing at the gate — free,
// classified by aiprov.ClassifyStatus — EXCEPT a 408, which on a paid image POST is engaged and final
// (orimages' rule: the gateway may have taken the body and the picture may be rendered and billed on
// the far side of it). A round trip that broke after the write, and every 2xx that did not become a
// picture (an envelope that will not decode, no data, a base64 that will not decode, a url that will
// not download), is ENGAGED and never retryable: the ledger books it unknown/failed, the chain does
// not buy the picture a second time.
//
// ⚠ REFERENCES ARE READ HERE, SO THEY MUST BE OURS. The edits endpoint takes the pictures as file
// parts, so this transport reads them — and a reader that follows any address it is handed is a
// server-side fetch of somebody else's choosing. gemini's rule, the code's convention: an https
// reference must be OUR media (Config.KeyFromURL, the host-checked bucket.ManagedObjectKeyFromURL) and
// is read through the bucket (Config.Objects), under a per-picture and a total byte ceiling, BEFORE
// anything is sent; a data: URI is decoded; nothing else is accepted.
//
// MONEY. gpt-image-* report `usage{input_tokens, output_tokens}` — copied into Usage.Prompt/Completion.
// Neither provider states a price, so Usage.Cost stays 0 here: the ledger books the catalogue's
// per-call price for the (provider, slug) row as a table price (designgen imageCallEnd, lane H3 of
// commit H), which is where every transport that does not report its own charge is priced.
package oaiimages

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

const (
	// DefaultTimeout bounds ONE generation: the reference reads and the POST share it (gemini's rule —
	// a bucket that hangs cannot stretch a call). Minutes, not seconds, for orimages' reason: a
	// high-quality render is tens of seconds of provider compute, and a deadline shorter than the work
	// is a picture BILLED AND DISCARDED.
	DefaultTimeout = 180 * time.Second
	// downloadTimeout bounds the fetch of a url-shaped answer (runblob's number). It runs on the
	// CALLER's context, not on the POST's budget, which the render may have spent.
	downloadTimeout = 90 * time.Second

	// MaxResponseBytes caps the JSON answer: base64 of one picture (4/3 of its bytes) plus the
	// envelope. Refused by name, never trimmed — a cut base64 decodes into half a picture.
	MaxResponseBytes = 48 << 20 // 48 MiB
	// MaxImageBytes caps ONE downloaded picture of a url-shaped answer.
	MaxImageBytes = 32 << 20 // 32 MiB

	// MaxReferences is how many pictures one edits call may carry — our guard (a user-controlled list
	// turned into one request of arbitrary size), not the provider's limit.
	MaxReferences = 8
	// MaxReferenceBytes caps ONE reference picture; MaxReferenceTotalBytes all of them together, so
	// eight maximal pictures cannot stack a 128 MiB multipart body on a 0.5 GiB box.
	MaxReferenceBytes      = 16 << 20 // 16 MiB
	MaxReferenceTotalBytes = 32 << 20 // 32 MiB

	// errorLimit bounds the provider's words inside a sentence.
	errorLimit = 200

	openAIPrefix   = "openai/"
	gptImagePrefix = "gpt-image-"
	slugDallE2     = "dall-e-2"
	slugDallE3     = "dall-e-3"
)

// openAISlugs — the OpenAI image models this transport draws by name; any other `gpt-image-*` is drawn
// too (a new snapshot must not need a deploy). The panel's catalogue names the bare slugs; the
// OpenRouter form `openai/gpt-image-2` is accepted and stripped (the alias map lives in the transport).
var openAISlugs = map[string]bool{
	"gpt-image-1": true, "gpt-image-1-mini": true, "gpt-image-1.5": true, "gpt-image-2": true,
	"gpt-image-2.5-sunburst": true, slugDallE2: true, slugDallE3: true,
}

// ObjectFetcher reads one bucket object by key: dependency.FileStore.GetManagedObject, which refuses a
// key outside its allowed segments before any S3 call. The size is read BEFORE the body.
type ObjectFetcher interface {
	GetManagedObject(ctx context.Context, objectKey string) (io.ReadCloser, int64, error)
}

// Config configures one transport for ONE provider account.
//
//	Provider     the billing key (entity.AIProviderOpenAI | entity.AIProviderApibost): CallError.Provider,
//	             the sentence prefix, and which slugs Serves admits (openai: its own image models;
//	             anything else: any bare slug — a relay relays what it lists);
//	BaseURL      the API root (endpoints.OpenAIAPIBase / ApibostAPIBase); "/images/…" is appended;
//	KeyFunc      asked on EVERY request and by Enabled() — the registry's hook; "" = disabled. Never printed;
//	HTTP         optional: only its Transport is used (tests); redirects are refused on the API
//	             requests whatever it says, and its Timeout is ignored (the budget is per request);
//	DefaultSlug  the transport's own default (ImageTransport.Model);
//	Timeout      the per-generation budget; <= 0 = DefaultTimeout;
//	Objects,
//	KeyFromURL   the bucket reader and its HOST-CHECKED url → key parser for https references; either
//	             nil = https references are refused (data: URIs still work).
type Config struct {
	Provider    string
	BaseURL     string
	KeyFunc     func() string
	HTTP        *http.Client
	DefaultSlug string
	Timeout     time.Duration
	Objects     ObjectFetcher
	KeyFromURL  func(rawURL string) (string, error)
}

// Client is one configured image transport. A nil *Client is valid and permanently disabled.
type Client struct {
	cfg Config
	// api carries the key: redirects refused (oaichat.refuseRedirect — the key and the prompt have one
	// destination). No http.Client.Timeout: the deadline is per request.
	api *http.Client
	// download fetches a url-shaped answer: no Authorization header ever, redirects followed (no key
	// travels, and a CDN redirect is ordinary).
	download *http.Client
}

// New builds the transport. It validates nothing: a missing key just leaves it disabled.
func New(cfg Config) *Client {
	var rt http.RoundTripper
	if cfg.HTTP != nil {
		rt = cfg.HTTP.Transport
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Client{
		cfg:      cfg,
		api:      &http.Client{Transport: rt, CheckRedirect: refuseRedirect},
		download: &http.Client{Transport: rt},
	}
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Model is the transport's own default slug (designgen.ImageTransport). Nil-safe.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.DefaultSlug)
}

// Enabled reports whether a key is configured right now. Nil-safe.
func (c *Client) Enabled() bool { return c != nil && c.key() != "" }

// Serves reports whether slug is one this transport draws (designgen.ImageTransport). Nil-safe.
func (c *Client) Serves(slug string) bool {
	if c == nil {
		return false
	}
	_, ok := c.wireSlug(slug)
	return ok
}

// wireSlug is the slug as it goes on the wire, or false when this transport does not draw it.
func (c *Client) wireSlug(slug string) (string, bool) {
	s := strings.TrimSpace(slug)
	if c.cfg.Provider == entity.AIProviderOpenAI {
		s = strings.TrimPrefix(s, openAIPrefix)
	}
	if s == "" || strings.Contains(s, "/") || strings.IndexFunc(s, unicode.IsSpace) >= 0 {
		return "", false
	}
	if c.cfg.Provider != entity.AIProviderOpenAI {
		return s, true
	}
	if openAISlugs[s] || (strings.HasPrefix(s, gptImagePrefix) && len(s) > len(gptImagePrefix)) {
		return s, true
	}
	return "", false
}

func (c *Client) key() string {
	if c == nil || c.cfg.KeyFunc == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.KeyFunc())
}

func (c *Client) provider() string {
	if c == nil || c.cfg.Provider == "" {
		return "ai"
	}
	return c.cfg.Provider
}

// ─── the family's dials ──────────────────────────────────────────────────────────────────────────

type family int

const (
	famGPTImage family = iota // gpt-image-*: quality low|medium|high|auto, background, output_format; b64 always
	famDallE3                 // quality standard|hd, 1792 sizes, response_format; no edits
	famDallE2                 // 1024x1024 only, response_format; edits take ONE picture
	famRelay                  // a relayed vendor: {model, prompt, n, size} and nothing else
)

func familyOf(slug string) family {
	switch {
	case strings.HasPrefix(slug, gptImagePrefix):
		return famGPTImage
	case slug == slugDallE3:
		return famDallE3
	case slug == slugDallE2:
		return famDallE2
	default:
		return famRelay
	}
}

// dials are the optional keys of one call, shared by both endpoints; "" = not sent.
type dials struct {
	size, quality, background, outputFormat, responseFormat string
}

func dialsFor(fam family, req orimages.Request) dials {
	var d dials
	d.size = sizeFor(fam, strings.TrimSpace(req.AspectRatio))
	switch fam {
	case famGPTImage:
		d.quality = strings.TrimSpace(req.Quality)
		d.background = strings.TrimSpace(req.Background)
		d.outputFormat = strings.TrimSpace(req.OutputFormat)
	case famDallE3:
		if strings.EqualFold(strings.TrimSpace(req.Quality), "high") {
			d.quality = "hd"
		}
		d.responseFormat = "b64_json"
	case famDallE2:
		d.responseFormat = "b64_json"
	}
	return d
}

// sizeFor maps the band's aspect ratio onto the family's pixel sizes; a ratio with no size of its own
// ("auto", "", 21:9, 5:4 …) sends none and leaves the provider's default.
func sizeFor(fam family, aspect string) string {
	var shape string
	switch aspect {
	case "1:1":
		shape = "square"
	case "3:2", "16:9", "4:3":
		shape = "landscape"
	case "2:3", "9:16", "3:4":
		shape = "portrait"
	default:
		return ""
	}
	switch {
	case shape == "square":
		return "1024x1024"
	case fam == famDallE2:
		return "" // dall-e-2 draws squares only
	case fam == famDallE3 && shape == "landscape":
		return "1792x1024"
	case fam == famDallE3:
		return "1024x1792"
	case shape == "landscape":
		return "1536x1024"
	default:
		return "1024x1536"
	}
}

// generationRequest is the JSON body of /images/generations. N is always 1 (see Generate).
type generationRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Background     string `json:"background,omitempty"`
	OutputFormat   string `json:"output_format,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
}

// ─── Generate ────────────────────────────────────────────────────────────────────────────────────

// Generate draws ONE picture (designgen.ImageTransport).
//
// Mapping from orimages.Request: Model (empty = DefaultSlug; `openai/` stripped on the openai
// transport) picks the family; Prompt as `prompt`; AspectRatio → `size` (sizeFor); Quality →
// `quality` (gpt-image: as given; dall-e-3: high → hd); Background and OutputFormat on gpt-image only;
// InputReferences switch the call to /images/edits with the pictures as file parts. N is sent as 1:
// the route's every call asks for one (imageCalls), and only data[0] is read. Resolution and
// OutputCompression have no counterpart here and are not sent.
func (c *Client) Generate(ctx context.Context, req orimages.Request) (*orimages.Result, error) {
	p := c.provider()
	key := c.key()
	if key == "" {
		return nil, c.fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%s: no API key is set: %w", p, aiprov.ErrNotConfigured))
	}
	asked := strings.TrimSpace(req.Model)
	if asked == "" {
		asked = c.Model()
	}
	slug, ok := c.wireSlug(asked)
	if !ok {
		// The chooser never sends this (Serves said no); a direct caller hears it before the wire.
		return nil, c.fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: %q is not an image model this transport draws", p, truncate(asked, 60)))
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, c.fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: a %s generation needs a prompt", p, slug))
	}
	if req.N > 1 {
		slog.Default().WarnContext(ctx, p+": one picture per call; n was asked and one will come back",
			slog.Int("n", req.N), slog.String("model", slug))
	}
	fam := familyOf(slug)
	d := dialsFor(fam, req)

	refs := make([]string, 0, len(req.InputReferences))
	for _, r := range req.InputReferences {
		if r = strings.TrimSpace(r); r != "" {
			refs = append(refs, r)
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var (
		endpoint, contentType string
		payload               []byte
	)
	if len(refs) == 0 {
		body, err := json.Marshal(generationRequest{
			Model: slug, Prompt: prompt, N: 1, Size: d.size, Quality: d.quality,
			Background: d.background, OutputFormat: d.outputFormat, ResponseFormat: d.responseFormat,
		})
		if err != nil {
			return nil, c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: marshal request: %w", p, err))
		}
		endpoint, contentType, payload = "/images/generations", "application/json", body
	} else {
		body, ct, err := c.editsBody(callCtx, slug, fam, prompt, d, refs)
		if err != nil {
			return nil, err
		}
		endpoint, contentType, payload = "/images/edits", ct, body
	}
	return c.post(ctx, callCtx, slug, endpoint, contentType, payload, key)
}

// editsBody is the multipart body of /images/edits — every refusal FREE and before the wire.
func (c *Client) editsBody(ctx context.Context, slug string, fam family, prompt string, d dials, refs []string) ([]byte, string, error) {
	p := c.provider()
	switch {
	case fam == famDallE3:
		return nil, "", c.fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: dall-e-3 has no edits endpoint and cannot take reference pictures (%d given)", p, len(refs)))
	case fam == famDallE2 && len(refs) > 1:
		return nil, "", c.fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: dall-e-2 edits one picture, and %d reference pictures were given", p, len(refs)))
	case len(refs) > MaxReferences:
		return nil, "", c.fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: %d reference pictures in one call, and this transport sends at most %d", p, len(refs), MaxReferences))
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fields := [][2]string{{"model", slug}, {"prompt", prompt}, {"n", "1"}, {"size", d.size},
		{"quality", d.quality}, {"background", d.background}, {"output_format", d.outputFormat},
		{"response_format", d.responseFormat}}
	for _, f := range fields {
		if f[1] == "" {
			continue
		}
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, "", c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build edits form: %w", p, err))
		}
	}
	field := "image[]"
	if fam == famDallE2 {
		field = "image"
	}
	var total int64
	for i, r := range refs {
		n := i + 1
		room := min(int64(MaxReferenceBytes), int64(MaxReferenceTotalBytes)-total)
		mt, data, err := c.reference(ctx, n, r, room)
		if err != nil {
			return nil, "", err
		}
		total += int64(len(data))
		ext := strings.TrimPrefix(mt, "image/")
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="reference-%d.%s"`, field, n, ext))
		// An explicit type: OpenAI refuses a file part labelled application/octet-stream.
		h.Set("Content-Type", mt)
		part, err := w.CreatePart(h)
		if err == nil {
			_, err = part.Write(data)
		}
		if err != nil {
			return nil, "", c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build edits form: %w", p, err))
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build edits form: %w", p, err))
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// ─── references ──────────────────────────────────────────────────────────────────────────────────

// mimeByExt / mimeByType — the picture types the edits endpoint takes (png, jpeg, webp).
var (
	mimeByExt  = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp"}
	mimeByType = map[string]string{"image/png": "image/png", "image/jpeg": "image/jpeg", "image/jpg": "image/jpeg", "image/webp": "image/webp"}
)

var errOverCeiling = errors.New("over the reference ceiling")

// reference reads ONE reference picture within room bytes: a data: URI decoded, an https url read
// through the bucket after the host check, anything else refused. Every failure is free: a picture we
// cannot vouch for or read is CodeBadRequest (ours — never fed to the provider's breaker), one that
// does not fit is CodeTooLarge.
func (c *Client) reference(ctx context.Context, n int, raw string, room int64) (string, []byte, error) {
	p := c.provider()
	switch {
	case strings.HasPrefix(raw, "data:"):
		mt, data, err := decodeDataURI(raw, room)
		if errors.Is(err, errOverCeiling) {
			return "", nil, c.overCeiling(n)
		}
		if err != nil {
			return "", nil, c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: reference %d: %w", p, n, err))
		}
		return mt, data, nil
	case strings.HasPrefix(raw, "https://"):
		return c.readObject(ctx, n, raw, room)
	default:
		return "", nil, c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf(
			"%s: reference %d: address must be our https media or a data:image/… URI, got %q", p, n, truncate(raw, 40)))
	}
}

func (c *Client) overCeiling(n int) *aiprov.CallError {
	return c.fail(aiprov.CodeTooLarge, 0, false, false, fmt.Errorf(
		"%s: reference %d is larger than %d bytes or takes the references past %d; nothing was sent",
		c.provider(), n, MaxReferenceBytes, MaxReferenceTotalBytes))
}

// readObject reads ONE https reference through the bucket (gemini.readObject, same guards).
func (c *Client) readObject(ctx context.Context, n int, rawURL string, room int64) (string, []byte, error) {
	p := c.provider()
	bad := func(format string, a ...any) (string, []byte, error) {
		return "", nil, c.fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: reference %d "+format, append([]any{p, n}, a...)...))
	}
	if c.cfg.KeyFromURL == nil || c.cfg.Objects == nil {
		return bad("is an https url, and no media reader is configured for one")
	}
	key, err := c.cfg.KeyFromURL(rawURL)
	if err != nil {
		return bad("is not our media: %w", err)
	}
	mt, ok := mimeByExt[strings.ToLower(path.Ext(key))]
	if !ok {
		return bad("%q is not a png, jpeg or webp", truncate(path.Base(key), 60))
	}
	rc, size, err := c.cfg.Objects.GetManagedObject(ctx, key)
	if err != nil {
		return bad("could not be read: %w", err)
	}
	defer rc.Close()
	if size > room {
		return "", nil, c.overCeiling(n)
	}
	data, err := io.ReadAll(io.LimitReader(rc, room+1))
	if err != nil {
		return bad("could not be read: %w", err)
	}
	if int64(len(data)) > room {
		return "", nil, c.overCeiling(n)
	}
	if len(data) == 0 {
		return bad("is an empty object")
	}
	return mt, data, nil
}

// decodeDataURI reads data:image/<type>[;…];base64,<payload>, the size checked before decoding.
func decodeDataURI(u string, room int64) (string, []byte, error) {
	meta, payload, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
	payload = strings.TrimSpace(payload)
	if !ok || payload == "" {
		return "", nil, errors.New("data URI carries no payload")
	}
	params := strings.Split(meta, ";")
	mt, known := mimeByType[strings.ToLower(strings.TrimSpace(params[0]))]
	if !known {
		return "", nil, fmt.Errorf("data URI type %q is not a png, jpeg or webp", truncate(params[0], 40))
	}
	if len(params) < 2 || !strings.EqualFold(strings.TrimSpace(params[len(params)-1]), "base64") {
		return "", nil, errors.New("data URI is not base64")
	}
	if int64(base64.StdEncoding.DecodedLen(len(payload))) > room+2 {
		return "", nil, errOverCeiling
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("data URI payload is not valid base64: %w", err)
	}
	if int64(len(data)) > room {
		return "", nil, errOverCeiling
	}
	if len(data) == 0 {
		return "", nil, errors.New("data URI decodes to nothing")
	}
	return mt, data, nil
}

// ─── the wire ────────────────────────────────────────────────────────────────────────────────────

type apiError struct {
	Message string `json:"message"`
}

// imageResponse is the answer of both endpoints. Usage is read LENIENTLY (raw, best-effort): a
// provider that spells it unexpectedly costs the token counts, never the picture.
type imageResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
	Usage json.RawMessage `json:"usage"`
	Error *apiError       `json:"error"`
}

// post is the ONE paid request. parent is the caller's context (the url download runs on it);
// callCtx carries the generation's budget.
func (c *Client) post(parent, callCtx context.Context, slug, endpoint, contentType string, payload []byte, key string) (*orimages.Result, error) {
	p := c.provider()
	ctx, wroteRequest := aiprov.ObserveWrite(callCtx)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.cfg.BaseURL, "/")+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build request: %w", p, err))
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.api.Do(httpReq)
	if err != nil {
		// The write flag, not the prose, tells a refused dial from a deadline in the render's 150th s.
		engaged := wroteRequest()
		code := aiprov.Interruption(ctx, err)
		return nil, c.fail(code, 0, engaged, !engaged && code != aiprov.CodeCanceled,
			fmt.Errorf("%s: request failed: %w", p, err))
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	body, readErr := readCapped(resp.Body, MaxResponseBytes)
	// THE STATUS IS JUDGED BEFORE THE BODY'S FATE (oaichat.post): a refusal at the gate stays free.
	if status < 200 || status >= 300 {
		if readErr != nil {
			body = []byte("response body unavailable: " + readErr.Error())
		}
		return nil, c.statusError(status, body, key)
	}
	// ─── FROM HERE ON A 2xx: THE REQUEST WAS SERVED, AND THEREFORE BILLED. Nothing below is retryable.
	if readErr != nil {
		code := aiprov.CodeTooLarge
		if !errors.Is(readErr, aiprov.ErrResponseTooLarge) {
			code = aiprov.Interruption(ctx, readErr)
		}
		return nil, c.fail(code, status, true, false, fmt.Errorf("%s: read response: %w", p, readErr))
	}
	var ir imageResponse
	if err := json.Unmarshal(body, &ir); err != nil {
		return nil, c.fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: could not decode the image response envelope: %w", p, err))
	}
	if ir.Error != nil && strings.TrimSpace(ir.Error.Message) != "" {
		return nil, c.fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: API error: %s", p, bounded(ir.Error.Message, key)))
	}
	// The usage rides along with every failure below: the call was billed, and this is the only place
	// its size would otherwise vanish from.
	res := &orimages.Result{Model: slug, Usage: usageOf(ir.Usage)}
	if len(ir.Data) == 0 {
		return res, c.fail(aiprov.CodeEmptyAnswer, status, true, false, fmt.Errorf("%s: the provider returned no image", p))
	}
	d := ir.Data[0]
	var (
		raw       []byte
		mediaType string
	)
	switch {
	case strings.TrimSpace(d.B64JSON) != "":
		raw, err = decodeB64(d.B64JSON)
		if err != nil {
			return res, c.fail(aiprov.CodeProviderError, status, true, false, fmt.Errorf("%s: image 1: %w", p, err))
		}
		mediaType = http.DetectContentType(raw)
	case strings.TrimSpace(d.URL) != "":
		raw, mediaType, err = c.fetch(parent, strings.TrimSpace(d.URL))
		if err != nil {
			return res, err
		}
	default:
		return res, c.fail(aiprov.CodeEmptyAnswer, status, true, false,
			fmt.Errorf("%s: image 1 carried neither b64_json nor url", p))
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return res, c.fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: image 1 is %q, not a picture", p, mediaType))
	}
	res.Images = []orimages.Image{{Bytes: raw, MediaType: mediaType}}
	return res, nil
}

// statusError classifies a non-2xx BY STATUS ALONE (aiprov.ClassifyStatus). Free — a refusal at the
// gate is not billed — except a 408, engaged and final (see the package doc). A 404 carries
// aiprov.ErrModelUnavailable (a setting, not weather), as oaichat's does.
func (c *Client) statusError(status int, body []byte, key string) error {
	p := c.provider()
	code, retryable := aiprov.ClassifyStatus(status)
	var err error
	if status == http.StatusNotFound {
		err = fmt.Errorf("%s: %w: API error (HTTP %d): %s", p, aiprov.ErrModelUnavailable, status, apiErrorMessage(body, key))
	} else {
		err = fmt.Errorf("%s: API error (HTTP %d): %s", p, status, apiErrorMessage(body, key))
	}
	if status == http.StatusRequestTimeout {
		return c.fail(code, status, true, false, err)
	}
	return c.fail(code, status, false, retryable, err)
}

// usageOf reads gpt-image's usage block: input_tokens → Prompt, output_tokens → Completion. Best-effort:
// an absent or odd block is zeros.
func usageOf(raw json.RawMessage) orimages.Usage {
	var u struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &u) != nil {
		return orimages.Usage{}
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	return orimages.Usage{Prompt: u.InputTokens, Completion: u.OutputTokens, Total: total}
}

// fetch downloads a url-shaped answer: no Authorization header, http(s) only, capped, typed from the
// CDN's header when it names an image, else sniffed. Every failure is BOUGHT (engaged, final).
func (c *Client) fetch(parent context.Context, rawURL string) ([]byte, string, error) {
	p := c.provider()
	bought := func(code string, status int, why string, err error) *aiprov.CallError {
		e := fmt.Errorf("%s: the picture was generated and %s — the call must not be re-sent", p, why)
		if err != nil {
			e = fmt.Errorf("%w: %v", e, err)
		}
		return c.fail(code, status, true, false, e)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, "", bought(aiprov.CodeProviderError, 0, fmt.Sprintf("its url is not fetchable: %q", truncate(rawURL, 80)), err)
	}
	ctx, cancel := context.WithTimeout(parent, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", bought(aiprov.CodeProviderError, 0, "its download could not be built", err)
	}
	resp, err := c.download.Do(req)
	if err != nil {
		return nil, "", bought(aiprov.Interruption(ctx, err), 0, "it could not be downloaded", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := readCapped(resp.Body, errorLimit*4)
		return nil, "", bought(aiprov.CodeProviderError, resp.StatusCode,
			fmt.Sprintf("its url answered HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(msg)), errorLimit)), nil)
	}
	raw, err := readCapped(resp.Body, MaxImageBytes)
	if err != nil {
		code := aiprov.CodeTooLarge
		if !errors.Is(err, aiprov.ErrResponseTooLarge) {
			code = aiprov.Interruption(ctx, err)
		}
		return nil, "", bought(code, resp.StatusCode, "its download could not be read", err)
	}
	if len(raw) == 0 {
		return nil, "", bought(aiprov.CodeEmptyAnswer, resp.StatusCode, "its url served no bytes", nil)
	}
	mt := http.DetectContentType(raw)
	if h, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil && strings.HasPrefix(h, "image/") {
		mt = h
	}
	return raw, mt, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────────────────────────

func (c *Client) fail(code string, status int, engaged, retryable bool, err error) *aiprov.CallError {
	prov := ""
	if c != nil {
		prov = c.cfg.Provider
	}
	return &aiprov.CallError{Provider: prov, Code: code, HTTPStatus: status, Engaged: engaged, Retryable: retryable, Err: err}
}

// readCapped reads at most limit bytes and REFUSES anything longer (the +1 byte tells «exactly at the
// ceiling» from «over it»).
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: larger than %d bytes", aiprov.ErrResponseTooLarge, limit)
	}
	return body, nil
}

// decodeB64 decodes the provider's base64, tolerating the unpadded form (orimages.decodeB64).
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "=")); err != nil {
			return nil, fmt.Errorf("b64_json was not valid base64: %w", err)
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("b64_json decoded to nothing")
	}
	return raw, nil
}

// apiErrorMessage pulls the provider's `error.message` (or a bare `message`) out of an error body,
// falling back to the raw body; bounded and scrubbed of the key either way.
func apiErrorMessage(body []byte, key string) string {
	var env struct {
		Error   *apiError `json:"error"`
		Message string    `json:"message"`
	}
	if json.Unmarshal(body, &env) == nil {
		if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
			return bounded(env.Error.Message, key)
		}
		if strings.TrimSpace(env.Message) != "" {
			return bounded(env.Message, key)
		}
	}
	return bounded(string(body), key)
}

// bounded scrubs the request's key FIRST (a cut made before the scrub could leave a prefix of it
// standing — oaichat.boundedMessage, Codex REVIEW-E #1: relays echo the refused header), then cuts.
func bounded(msg, key string) string {
	msg = strings.TrimSpace(msg)
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[key]")
	}
	return truncate(msg, errorLimit)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
