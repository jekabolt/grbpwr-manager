// Package gemini is the chat transport of Google's Gemini API (B-22): one POST to
// {endpoints.GeminiAPIBase}/v1beta/models/{model}:generateContent with the key in x-goog-api-key.
//
// IT IS oaichat's CONTRACT IN ANOTHER SPELLING, and every guard is the same one for the same reason
// (read oaichat's package comment first): one key read per request; the call's deadline derived from
// its own answer ceiling (aiprov.CompletionBudget); the «bought / not bought» boundary observed on the
// wire (aiprov.ObserveWrite) and nowhere else; the status judged before the body's fate; a read
// ceiling that refuses instead of trimming; every failure an *aiprov.CallError whose sentence starts
// "google: ". The router's fallback is only as honest as those flags, so they are not re-invented.
//
// WHAT IS DIFFERENT HERE, AND WHY:
//
//   - PICTURES ARE BYTES, NOT ADDRESSES. Gemini's inline parts carry the picture itself (base64);
//     a URL it cannot fetch. So this transport READS the picture — and a reader that follows any
//     address it is handed is a server-side fetch of somebody else's choosing. An https picture must
//     therefore be OUR media (Config.KeyFromURL, the host-checked bucket.ManagedObjectKeyFromURL)
//     and is read through the bucket (Config.Objects), under a per-picture and a total byte ceiling,
//     BEFORE anything is sent. A data: URI is decoded. Nothing else is accepted.
//   - REDIRECTS ARE REFUSED. net/http strips Authorization on a cross-host redirect but forwards
//     x-goog-api-key to wherever the Location points; nothing this transport asks for moves.
//   - GOOGLE REFUSES A BAD KEY WITH 400, NOT 401. The status matrix would book it as our bad
//     request; statusError reads Google's own bad-key mark and books key_rejected (same rule as the
//     probe's).
//   - THE BASE URL IS A CONSTANT (endpoints.GeminiAPIBase) with no Config knob: a base that can be
//     edited is a place the key can be sent. Tests in this package set the unexported field.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// provider is the billing key: CallError.Provider, ChatResult.Provider and the sentence prefix.
const provider = entity.AIProviderGoogle

const (
	// MaxImageParts caps how many pictures one request may carry — our guard, not Google's limit, for
	// the reason oaichat.MaxImageParts gives (pictures are billed as input: "how many" is "how much").
	// Duplicated, not imported: a transport importing another transport for one number would tie two
	// providers' packages together; the DraftDesignIdea door refuses above the same 16 first.
	MaxImageParts = 16

	// MaxInlineBytes is the RAW byte total of all pictures of one request. Base64 grows it by ~⅓, so
	// 15 MiB raw is ~20 MiB on the wire — toward Gemini's 20 MB request ceiling (UNVERIFIED (G-05):
	// the ceiling is from memory, and whether it is 20·10⁶ or 20·2²⁰ bytes decides whether a request
	// at the very edge is a 400). Above it the call is refused HERE, before any byte is read past the
	// ceiling and before any request: a 400 at Google would be booked as our bad request anyway, and
	// reading 60 MB of somebody's moodboard into a 0.5 GiB box to learn that is the worse failure.
	MaxInlineBytes = 15 << 20

	// MaxResponseBytes caps how much of a response is read; the cap REFUSES (aiprov.ErrResponseTooLarge)
	// rather than trims — see readCapped. Same number and reason as oaichat.MaxResponseBytes.
	MaxResponseBytes = 4 << 20 // 4 MiB

	// temperature — the same number the OpenAI-shaped path sends, so a fallback between providers does
	// not also change how deterministic the answer is.
	temperature = 0.2

	// maxErrorMessage bounds the provider's words in a sentence (runes).
	maxErrorMessage = 300

	// minScrubbedKey — see bounded.
	minScrubbedKey = 8
)

// ObjectFetcher reads one bucket object by key: dependency.FileStore.GetManagedObject, which refuses a
// key outside its allowed segments before any S3 call. The size is the object's (Stat), read BEFORE
// the body so an oversized picture is refused without being downloaded.
type ObjectFetcher interface {
	GetManagedObject(ctx context.Context, objectKey string) (io.ReadCloser, int64, error)
}

// Config configures the transport.
//
//	KeyFunc      asked for the key on EVERY request and by Enabled() — the registry's hook, so a key
//	             saved in the admin panel reaches the next request; "" (or a nil func) = disabled.
//	             Never serialised, never printed;
//	HTTPTimeout  the BASE of every request's budget (aiprov.CompletionBudget); <= 0 = the default;
//	Objects      the bucket reader for https pictures; nil = https pictures are refused;
//	KeyFromURL   the HOST-CHECKED url → key parser (bucket.ManagedObjectKeyFromURL bound to the bucket
//	             config); nil = https pictures are refused. Never ObjectKeyFromStoredURL: that one
//	             checks no host, and the host check is what makes "our media" mean ours.
type Config struct {
	KeyFunc     func() string
	HTTPTimeout time.Duration
	Objects     ObjectFetcher
	KeyFromURL  func(rawURL string) (string, error)
}

// Client is the Gemini chat transport. A nil *Client is valid and permanently disabled.
type Client struct {
	cfg Config
	// base is endpoints.GeminiAPIBase; unexported so no caller can point the key elsewhere.
	base string
	// budgetBase — the base of every call's budget; the answer's printing time is added per request.
	budgetBase time.Duration
	// ⚠ No http.Client.Timeout, on purpose (see oaichat.Client.http): the deadline is set PER REQUEST
	// from that request's ceiling. CheckRedirect refuses every redirect (see refuseRedirect).
	http *http.Client
}

var _ aiprov.Chatter = (*Client)(nil)

// New builds the transport. It validates nothing: a missing key just leaves it disabled.
func New(cfg Config) *Client {
	base := cfg.HTTPTimeout
	if base <= 0 {
		base = aiprov.DefaultBudgetBase
	}
	return &Client{
		cfg:        cfg,
		base:       endpoints.GeminiAPIBase,
		budgetBase: base,
		http:       &http.Client{CheckRedirect: refuseRedirect},
	}
}

// refuseRedirect keeps a redirect as the answer instead of following it (copied from
// probe.refuseRedirect): x-goog-api-key survives a cross-host redirect in net/http, and a generate
// call has no reason to go anywhere but the one URL it was built for. The 3xx then reaches
// statusError as an unnamed status — provider_error, not retryable, not engaged.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Enabled reports whether a key is configured right now. Nil-safe.
func (c *Client) Enabled() bool { return c != nil && c.key() != "" }

// BaseURL returns the API root, for log lines (a 404 may mean the base, not the slug). Nil-safe.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.base
}

// CompletionBase is the base this transport puts into aiprov.CompletionBudget on every request; a
// lease that must outlive a call is derived from THIS number (the router reads it). Nil-safe.
func (c *Client) CompletionBase() time.Duration {
	if c == nil || c.budgetBase <= 0 {
		return aiprov.DefaultBudgetBase
	}
	return c.budgetBase
}

func (c *Client) key() string {
	if c == nil || c.cfg.KeyFunc == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.KeyFunc())
}

// Chat implements aiprov.Chatter: one generateContent of req on model.
//
// req.UserAsParts has no meaning here — Gemini's user turn is always a list of parts — and is
// ignored. req.Effort is mapped only to switch thinking OFF (see thinkingOff); "low" | "medium" |
// "high" leave the model's own thinking default, because a thinking budget per word is a number
// nobody has measured yet.
//
// ⚠ ON AN ENGAGED FAILURE WHOSE ANSWER DID ARRIVE — no candidates, an empty message, the budget
// spent on thinking — Chat returns BOTH the partial result (Usage, FinishReason, Model, RequestID;
// Text "") AND the error: the call was paid for and its size is known. Every other failure returns a
// nil result.
func (c *Client) Chat(ctx context.Context, model string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	// ONE KEY PER REQUEST: read once here and carried to the header; a rotation that lands mid-flight
	// changes the NEXT request, never this one's header.
	key := c.key()
	if key == "" {
		return nil, fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%s: no API key is set: %w", provider, aiprov.ErrNotConfigured))
	}
	slug, err := modelSlug(model)
	if err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, err)
	}
	if strings.TrimSpace(req.User) == "" {
		// An empty user part is a 400 at Google that would read as the provider's fault; pictures alone
		// say nothing either.
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: a request needs a prompt, pictures alone say nothing", provider))
	}
	if len(req.ImageURLs) > MaxImageParts {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: %d pictures exceeds the %d-picture limit for one request", provider, len(req.ImageURLs), MaxImageParts))
	}
	ceiling := 0
	if req.MaxTokens > 0 {
		ceiling = req.MaxTokens
	}

	// ONE DEADLINE FOR THE PICTURES AND THE WIRE. aiprov.DefaultBudgetBase already counts "the
	// provider fetching whatever pictures the request points at"; here WE fetch them, so the reads sit
	// inside the same budget — a bucket that hangs cannot stretch a call past the lease the router
	// derived from CompletionBase.
	ctx, cancel := context.WithTimeout(ctx, aiprov.CompletionBudget(c.budgetBase, ceiling))
	defer cancel()

	pictures, err := c.pictures(ctx, req.ImageURLs)
	if err != nil {
		return nil, err
	}
	payload, err := buildRequest(slug, req, ceiling, pictures)
	if err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, err)
	}
	return c.post(ctx, slug, payload, key)
}

// slugPattern is a bare Gemini model name. The slug becomes a PATH SEGMENT of the URL, so anything
// else — a slash, "..", a "?" or "#", a ":" — would address another endpoint of the API with our key.
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// modelSlug validates the slug and drops Google's own resource prefix "models/" (the list-models API
// names every model that way, so a pasted name must not fail for it).
func modelSlug(model string) (string, error) {
	s := strings.TrimPrefix(strings.TrimSpace(model), "models/")
	if s == "" {
		// "" would reach Google as a 404 that reads as a retired model rather than our missing value.
		return "", fmt.Errorf("%s: a completion needs a model slug", provider)
	}
	if !slugPattern.MatchString(s) {
		return "", fmt.Errorf("%s: model slug %q is not a bare Gemini model name", provider, truncate(s, 60))
	}
	return s, nil
}

// ─── pictures ────────────────────────────────────────────────────────────────────────────────────

// mimeByExt / mimeByType — the picture types this transport inlines. UNVERIFIED (G-05): gif is on
// the list by the brief; Gemini's documented image types (from memory) are png, jpeg, webp, heic,
// heif — a gif may be a 400 (booked as our bad request, not engaged, so the router falls back).
var (
	mimeByExt = map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
	}
	mimeByType = map[string]string{
		"image/png": "image/png", "image/jpeg": "image/jpeg", "image/jpg": "image/jpeg", "image/webp": "image/webp", "image/gif": "image/gif",
	}
)

// pictures turns the picture list into inline parts, refusing anything it cannot vouch for and
// anything that would take the request past MaxInlineBytes — all of it BEFORE the wire.
//
// Each picture is encoded as soon as it is read and its raw bytes dropped, so the peak is the
// base64 of the pictures, not the raw bytes plus the base64.
func (c *Client) pictures(ctx context.Context, urls []string) ([]inlinePart, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	out := make([]inlinePart, 0, len(urls))
	var total int64
	for i, raw := range urls {
		n := i + 1
		u := strings.TrimSpace(raw)
		// room is the PER-PICTURE ceiling: what is left of the total. A picture is never read past it.
		room := int64(MaxInlineBytes) - total
		var (
			mime string
			data []byte
			err  error
		)
		switch {
		case u == "":
			return nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: picture %d: empty picture address", provider, n))
		case strings.HasPrefix(u, "data:"):
			mime, data, err = decodeDataURI(u, room)
			if errors.Is(err, errOverCeiling) {
				return nil, overCeiling(n)
			}
			if err != nil {
				return nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: picture %d: %w", provider, n, err))
			}
		case strings.HasPrefix(u, "https://"):
			mime, data, err = c.readObject(ctx, n, u, room)
			if err != nil {
				return nil, err
			}
		default:
			// http://, file://, gopher:// … — nothing this transport reads.
			return nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf(
				"%s: picture %d: address must be our https media or a data:image/… URI, got %q", provider, n, truncate(u, 40)))
		}
		total += int64(len(data))
		out = append(out, inlinePart{InlineData: inlineData{MimeType: mime, Data: base64.StdEncoding.EncodeToString(data)}})
	}
	return out, nil
}

// errOverCeiling marks a picture that does not fit in what is left of MaxInlineBytes.
var errOverCeiling = errors.New("over the inline ceiling")

// overCeiling is the refusal of a request whose pictures do not fit: CodeTooLarge, NOT engaged
// (nothing was sent), NOT retryable (the same pictures will not shrink).
func overCeiling(n int) *aiprov.CallError {
	return fail(aiprov.CodeTooLarge, 0, false, false, fmt.Errorf(
		"%s: picture %d takes the pictures past the %d-byte inline ceiling; nothing was sent", provider, n, MaxInlineBytes))
}

// readObject reads ONE https picture: host-checked key, a known picture type, then the bucket read
// bounded by room. Every failure is a CallError: a picture that cannot be vouched for or read is
// CodeBadRequest (ours, not the provider's — and not retryable, so a bucket hiccup never feeds the
// PROVIDER's breaker), one that does not fit is CodeTooLarge.
func (c *Client) readObject(ctx context.Context, n int, rawURL string, room int64) (string, []byte, error) {
	if c.cfg.KeyFromURL == nil || c.cfg.Objects == nil {
		return "", nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: picture %d: no media reader is configured for https pictures", provider, n))
	}
	key, err := c.cfg.KeyFromURL(rawURL)
	if err != nil {
		return "", nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: picture %d is not our media: %w", provider, n, err))
	}
	mime, ok := mimeByExt[strings.ToLower(path.Ext(key))]
	if !ok {
		return "", nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: picture %d: %q is not a png, jpeg, webp or gif", provider, n, truncate(path.Base(key), 60)))
	}
	rc, size, err := c.cfg.Objects.GetManagedObject(ctx, key)
	if err != nil {
		return "", nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: picture %d could not be read: %w", provider, n, err))
	}
	defer rc.Close()
	if size > room {
		// Refused on the Stat size, before a byte of the body is downloaded.
		return "", nil, overCeiling(n)
	}
	// room+1: the one extra byte is the difference between "exactly fits" and "a size that lied".
	data, err := io.ReadAll(io.LimitReader(rc, room+1))
	if err != nil {
		return "", nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: picture %d could not be read: %w", provider, n, err))
	}
	if int64(len(data)) > room {
		return "", nil, overCeiling(n)
	}
	if len(data) == 0 {
		return "", nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: picture %d is an empty object", provider, n))
	}
	return mime, data, nil
}

// decodeDataURI reads data:image/<type>[;…];base64,<payload>. The size is checked on the decoded
// length BEFORE decoding, so an oversized URI is refused without a second allocation of its size.
func decodeDataURI(u string, room int64) (string, []byte, error) {
	meta, payload, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
	payload = strings.TrimSpace(payload)
	if !ok || payload == "" {
		return "", nil, errors.New("data URI carries no payload")
	}
	params := strings.Split(meta, ";")
	mime, known := mimeByType[strings.ToLower(strings.TrimSpace(params[0]))]
	if !known {
		return "", nil, fmt.Errorf("data URI type %q is not a png, jpeg, webp or gif", truncate(params[0], 40))
	}
	if len(params) < 2 || !strings.EqualFold(strings.TrimSpace(params[len(params)-1]), "base64") {
		return "", nil, errors.New("data URI is not base64")
	}
	if int64(base64.StdEncoding.DecodedLen(len(payload))) > room+2 {
		// DecodedLen rounds up to whole 3-byte groups; the exact check follows the decode.
		return "", nil, errOverCeiling
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("data URI payload is not valid base64: %w", err)
	}
	if int64(len(data)) > room {
		return "", nil, errOverCeiling
	}
	return mime, data, nil
}

// ─── request ─────────────────────────────────────────────────────────────────────────────────────

// generateRequest — ⚠ THE FIELD ORDER IS THE BYTE ORDER, AND THE GOLDENS PIN IT.
//
// UNVERIFIED (G-05), written from memory of the REST reference: systemInstruction (a Content with
// no role), contents[].role "user", parts[].text / parts[].inlineData{mimeType,data},
// generationConfig{temperature, maxOutputTokens, responseMimeType, thinkingConfig{thinkingBudget}}.
type generateRequest struct {
	SystemInstruction *systemInstruction `json:"systemInstruction,omitempty"`
	Contents          []userContent      `json:"contents"`
	GenerationConfig  generationConfig   `json:"generationConfig"`
}

type systemInstruction struct {
	Parts []textPart `json:"parts"`
}

// userContent's Parts is []any so a text part and a picture part are two types, each with ALL its
// keys always present: one struct with omitempty on both would send `{}` for an empty text.
type userContent struct {
	Role  string `json:"role"`
	Parts []any  `json:"parts"`
}

type textPart struct {
	Text string `json:"text"`
}

type inlinePart struct {
	InlineData inlineData `json:"inlineData"`
}

type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type generationConfig struct {
	Temperature float64 `json:"temperature"`
	// MaxOutputTokens caps the answer; omitted when zero (Google's own default stands). UNVERIFIED
	// (G-05): whether thinking tokens count against it on 2.5 models.
	MaxOutputTokens  int             `json:"maxOutputTokens,omitempty"`
	ResponseMimeType string          `json:"responseMimeType,omitempty"`
	ThinkingConfig   *thinkingConfig `json:"thinkingConfig,omitempty"`
}

type thinkingConfig struct {
	ThinkingBudget int `json:"thinkingBudget"`
}

// buildRequest marshals the body. The text part comes FIRST, as in oaichat: it is the instruction.
func buildRequest(slug string, req aiprov.ChatRequest, ceiling int, pictures []inlinePart) ([]byte, error) {
	parts := make([]any, 0, len(pictures)+1)
	parts = append(parts, textPart{Text: req.User})
	for _, p := range pictures {
		parts = append(parts, p)
	}
	body := generateRequest{
		Contents: []userContent{{Role: "user", Parts: parts}},
		GenerationConfig: generationConfig{
			Temperature:     temperature,
			MaxOutputTokens: ceiling,
		},
	}
	if strings.TrimSpace(req.System) != "" {
		body.SystemInstruction = &systemInstruction{Parts: []textPart{{Text: req.System}}}
	}
	if req.JSONMode {
		// Gemini's own JSON mode: the answer is one JSON document, no fence to strip.
		body.GenerationConfig.ResponseMimeType = "application/json"
	}
	if thinkingOff(slug, req.Effort) {
		body.GenerationConfig.ThinkingConfig = &thinkingConfig{ThinkingBudget: 0}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", provider, err)
	}
	return payload, nil
}

// thinkingOff — Effort "none" / "minimal" switches thinking off (thinkingBudget 0) on a FLASH model
// only.
//
// ⚠ THE ASYMMETRY IS DELIBERATE. A pro model cannot switch thinking off: thinkingBudget 0 there is a
// 400, and a 400 is booked as OUR bad request — every press of a feature that asks for "none" would
// fail over to the next candidate forever. So on pro the knob is not sent and the model thinks at its
// default (a slower, pricier answer, never a refused one). UNVERIFIED (G-05): "pro refuses 0" and
// "every flash accepts 0" (flash-lite, 2.0-flash, 3.x flash) are from memory.
func thinkingOff(slug, effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal":
		return strings.Contains(strings.ToLower(slug), "flash")
	}
	return false
}

// ─── the wire ────────────────────────────────────────────────────────────────────────────────────
//
// THE ENGAGED BOUNDARY is oaichat's, row for row (read its table): NOT engaged — everything refused
// before the wire (no key, bad slug, empty prompt, a picture refused or over the ceiling, marshal),
// DNS / refused connection / a write that did not complete, and EVERY non-2xx (a refusal at the gate
// is not billed — Google's 400 API_KEY_INVALID included). ENGAGED — the request written and then the
// deadline / a cancel / a cut connection; a body unread or over the read ceiling; any 2xx that cannot
// be used (broken envelope, no candidates, empty text).

// generateResponse is the envelope. UNVERIFIED (G-05): candidates[].content.parts[].text (+ thought),
// candidates[].finishReason, promptFeedback.blockReason, usageMetadata{promptTokenCount,
// candidatesTokenCount, thoughtsTokenCount, cachedContentTokenCount}, modelVersion, responseId.
// responseId and usageMetadata are read LENIENTLY (raw, best-effort per number): a field spelt
// unexpectedly costs that number, never the answer.
type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata map[string]json.RawMessage `json:"usageMetadata"`
	ModelVersion  string                     `json:"modelVersion"`
	ResponseID    json.RawMessage            `json:"responseId"`
	Error         *apiError                  `json:"error"`
}

// apiError is Google's error object. UNVERIFIED (G-05) — the bad-key body, from memory:
// {"error":{"code":400,"message":"API key not valid. Please pass a valid API key.",
// "status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo",
// "reason":"API_KEY_INVALID","domain":"googleapis.com",…}]}}.
type apiError struct {
	Message string `json:"message"`
	Status  string `json:"status"`
	Details []struct {
		Reason string `json:"reason"`
	} `json:"details"`
}

// post is the one place this transport talks to generateContent. ctx already carries the call's
// budget (Chat set it before reading the pictures).
func (c *Client) post(ctx context.Context, slug string, payload []byte, key string) (*aiprov.ChatResult, error) {
	ctx, wroteRequest := aiprov.ObserveWrite(ctx)

	endpoint := strings.TrimRight(c.base, "/") + "/v1beta/models/" + slug + ":generateContent"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build request: %w", provider, err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// The header, never ?key=: a query string lands in access logs and proxies.
	httpReq.Header.Set("x-goog-api-key", key)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Same text on both sides of the boundary; the WRITE FLAG decides, never the prose.
		engaged := wroteRequest()
		code := aiprov.Interruption(ctx, err)
		retryable := !engaged && code != aiprov.CodeCanceled
		return nil, fail(code, 0, engaged, retryable, fmt.Errorf("%s: request failed: %w", provider, err))
	}
	defer resp.Body.Close()

	body, err := readCapped(resp.Body, MaxResponseBytes, "generateContent response")
	// THE STATUS IS JUDGED BEFORE THE BODY'S FATE: a non-2xx is a refusal at the gate whatever
	// happened to its excuse; the body, when it arrived, only lends the sentence (and Google's
	// bad-key mark).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err != nil {
			body = []byte("response body unavailable: " + err.Error())
		}
		return nil, statusError(resp.StatusCode, body, key)
	}
	if err != nil {
		code := aiprov.CodeTooLarge
		if !errors.Is(err, aiprov.ErrResponseTooLarge) {
			code = aiprov.Interruption(ctx, err)
		}
		return nil, fail(code, resp.StatusCode, true, false, fmt.Errorf("%s: read response: %w", provider, err))
	}

	// ─── FROM HERE ON A 2xx: THE REQUEST WAS ACCEPTED AND SERVED, SO PAID FOR ───
	status := resp.StatusCode
	var gr generateResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: could not decode API response envelope: %w", provider, err))
	}
	if gr.Error != nil && strings.TrimSpace(gr.Error.Message) != "" {
		return nil, fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: API error: %s", provider, bounded(gr.Error.Message, key)))
	}
	res := result(slug, &gr)
	if len(gr.Candidates) == 0 {
		// A blocked prompt comes back as a 2xx with no candidates and a promptFeedback — the input was
		// read (and its tokens counted), so the partial rides along.
		return res, fail(aiprov.CodeEmptyAnswer, status, true, false,
			fmt.Errorf("%s: API response contained no candidates%s", provider, blocked(&gr)))
	}
	if res.Text == "" {
		// EMPTY-BECAUSE-THE-BUDGET-RAN-OUT IS ITS OWN FAULT (oaichat's split, same sentinel): MAX_TOKENS
		// with no text is a thinking model that spent the ceiling thinking — deterministic, "the setting
		// is wrong", not "try again".
		if res.FinishReason == "length" {
			return res, fail(aiprov.CodeBudgetExhausted, status, true, false, fmt.Errorf(
				"%s: %w (%d completion tokens spent, none of them answer)", provider, aiprov.ErrBudgetExhausted, res.Usage.Completion))
		}
		why := blocked(&gr)
		if why == "" && res.FinishReason != "" && res.FinishReason != "stop" {
			why = " (finish reason " + truncate(res.FinishReason, 40) + ")"
		}
		return res, fail(aiprov.CodeEmptyAnswer, status, true, false, fmt.Errorf("%s: model returned an empty message%s", provider, why))
	}
	return res, nil
}

// result turns an envelope into a ChatResult. Engaged is always true: an answer came back.
func result(slug string, gr *generateResponse) *aiprov.ChatResult {
	res := &aiprov.ChatResult{Provider: provider, Model: slug, Engaged: true}
	if m := strings.TrimSpace(gr.ModelVersion); m != "" {
		res.Model = m
	}
	var id string
	if json.Unmarshal(gr.ResponseID, &id) == nil {
		res.RequestID = strings.TrimSpace(id)
	}
	if len(gr.Candidates) > 0 {
		cand := gr.Candidates[0]
		var text strings.Builder
		for _, p := range cand.Content.Parts {
			if p.Thought {
				// A thought summary is not the answer (and would break a JSON answer). We never ask for
				// them (no includeThoughts), so this only guards a model that sends one anyway.
				continue
			}
			text.WriteString(p.Text)
		}
		res.Text = strings.TrimSpace(text.String())
		res.FinishReason = finishReason(cand.FinishReason)
	}
	// ⚠ aiprov.TokenUsage's convention (core.go): Completion INCLUDES reasoning. Gemini reports them
	// apart — candidatesTokenCount excludes thoughtsTokenCount — so they are ADDED here; the thinking
	// is billed at the output rate. promptTokenCount already includes cachedContentTokenCount
	// (Cached ⊆ Prompt). UNVERIFIED (G-05): both inclusions are from memory.
	u := gr.UsageMetadata
	thoughts := count(u, "thoughtsTokenCount")
	res.Usage = aiprov.TokenUsage{
		Prompt:     count(u, "promptTokenCount"),
		Completion: count(u, "candidatesTokenCount") + thoughts,
		Cached:     count(u, "cachedContentTokenCount"),
		Reasoning:  thoughts,
	}
	return res
}

// count reads one usage number, best-effort: a JSON number or a quoted one (proto3 JSON may quote
// integers); anything else, absent or negative is 0 — that one number, never the answer.
func count(m map[string]json.RawMessage, k string) int {
	raw, ok := m[k]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// finishReason maps Gemini's words onto the OpenAI ones the callers already read ("length" is what
// the empty-answer split and the callers' truncation checks look for). Anything else passes as-is.
// UNVERIFIED (G-05): the word list.
func finishReason(r string) string {
	switch strings.TrimSpace(r) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	}
	return r
}

// blocked names a promptFeedback.blockReason for the sentence (" (prompt blocked: SAFETY)"), "" when
// there is none. The reason is an enum word; it is bounded anyway.
func blocked(gr *generateResponse) string {
	if gr.PromptFeedback == nil || strings.TrimSpace(gr.PromptFeedback.BlockReason) == "" {
		return ""
	}
	return " (prompt blocked: " + truncate(strings.TrimSpace(gr.PromptFeedback.BlockReason), 40) + ")"
}

// statusError classifies a non-2xx by the ONE status matrix (aiprov.ClassifyStatus), with one
// Google-specific row: a 400 that carries Google's bad-key mark is key_rejected — Google refuses a
// bad key with 400 where every other provider says 401, and booking it as "bad_request" would send
// the owner looking for a fault in the request. None of these is engaged, and 404 carries
// ErrModelUnavailable, exactly as oaichat.
func statusError(status int, body []byte, key string) error {
	code, retryable := aiprov.ClassifyStatus(status)
	var env struct {
		Error *apiError `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	if status == http.StatusBadRequest && keyInvalid(env.Error) {
		code, retryable = aiprov.CodeKeyRejected, false
	}
	msg := strings.TrimSpace(string(body))
	if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
		msg = env.Error.Message
	}
	msg = bounded(msg, key)
	var err error
	if status == http.StatusNotFound {
		err = fmt.Errorf("%s: %w: API error (HTTP %d): %s", provider, aiprov.ErrModelUnavailable, status, msg)
	} else {
		err = fmt.Errorf("%s: API error (HTTP %d): %s", provider, status, msg)
	}
	return fail(code, status, false, retryable, err)
}

// keyInvalid is Google's bad-key mark: a detail whose reason is API_KEY_INVALID, or — should the
// details be absent — status INVALID_ARGUMENT with a message about the API key. The machine word
// is the rule; the message is the fallback the brief allows, and it only ever moves a 400 from
// bad_request to key_rejected (both are not engaged and not retryable, so no money and no fallback
// decision rides on it — only the badge's word).
func keyInvalid(e *apiError) bool {
	if e == nil {
		return false
	}
	for _, d := range e.Details {
		if d.Reason == "API_KEY_INVALID" {
			return true
		}
	}
	return e.Status == "INVALID_ARGUMENT" && strings.Contains(strings.ToLower(e.Message), "api key")
}

func fail(code string, status int, engaged, retryable bool, err error) *aiprov.CallError {
	return &aiprov.CallError{
		Provider:   provider,
		Code:       code,
		HTTPStatus: status,
		Engaged:    engaged,
		Retryable:  retryable,
		Err:        err,
	}
}

// readCapped reads at most limit bytes and REFUSES anything longer (copied from oaichat.readCapped —
// the same limit+1 trick: a prefix that parses would be a silently shortened answer).
func readCapped(r io.Reader, limit int64, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: %w: %s is larger than %d bytes", provider, aiprov.ErrResponseTooLarge, what, limit)
	}
	return body, nil
}

// bounded makes the provider's words fit a one-line sentence: the key never (a provider that quotes
// it back would put it in our logs), whitespace collapsed, at most maxErrorMessage runes.
//
// The key is replaced only when it is at least minScrubbedKey long: no provider issues a shorter one
// (Google's are 39 characters), and replacing a two-letter "key" would shred every word it occurs
// in — "API key" printed as "API [key]ey".
func bounded(msg, key string) string {
	if len(key) >= minScrubbedKey {
		msg = strings.ReplaceAll(msg, key, "[key]")
	}
	return truncate(strings.Join(strings.Fields(msg), " "), maxErrorMessage)
}

// truncate cuts s to n runes (never mid-rune: the sentence is shown to people).
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
