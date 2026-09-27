// Package anthropic is the chat transport for Anthropic's Messages API: one POST to
// {endpoints.AnthropicAPIBase}/v1/messages per call, spoken with a key stored in the admin panel
// (there is no env key for this provider, by design — R-05).
//
// It is a port of oaichat, not a variant of it, and every guard oaichat learnt the expensive way is
// here once more, on purpose, with the same words:
//
//   - the TIME BUDGET of every request — aiprov.CompletionBudget(base, the caller's ceiling), set per
//     request (see post: why the ceiling that buys time is the caller's, not DefaultMaxTokens);
//   - the ENGAGED boundary — aiprov.ObserveWrite, and oaichat's table of which failure falls on which
//     side (a non-2xx is a refusal at the gate; a written request that then broke, and any 2xx, are
//     engaged);
//   - the READ CEILING — MaxResponseBytes, refused by name, never trimmed;
//   - the STATUS CLASSIFICATION — aiprov.ClassifyStatus, by status alone, never by the provider's prose
//     (529 "overloaded" is a 5xx: weather, retryable);
//   - the SENTENCES — "anthropic: API error (HTTP 502): …", the provider's text bounded and never
//     carrying the key;
//   - REDIRECTS REFUSED — net/http strips Authorization on a cross-host redirect but forwards
//     x-api-key to wherever Location points.
//
// Every failure is an *aiprov.CallError; its Error() is the sentence, its fields are the facts.
//
// ⚠ WIRE SHAPES MARKED UNVERIFIED (G-05) below were written from the Anthropic documentation bundled
// with the tooling (the claude-api reference, cached 2026-06-24), not from a live call; G-05 checks
// them with the owner's key before this transport carries a route.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	// APIVersion is the anthropic-version header every Messages request carries (the probe sends the
	// same value). UNVERIFIED (G-05) that no newer value is required for the models routed here.
	APIVersion = "2023-06-01"

	// DefaultMaxTokens fills max_tokens when the caller set no ceiling. Anthropic REQUIRES the field
	// (a request without it is a 400 — our fault, booked as bad_request, the route dead): the callers
	// that pass no ceiling today — note markdown, email translate — get 4096, above anything those
	// features have ever printed. It is a guard against a runaway answer, NOT a request to print that
	// much — see post for why it buys no time.
	//
	// ⚠ ON CLAUDE SONNET 5 / OPUS 5 / FABLE, OMITTING `thinking` RUNS ADAPTIVE THINKING (UNVERIFIED
	// (G-05), bundled docs): its tokens count against this ceiling and bill as output. A call whose
	// thinking spends all 4096 ends as stop_reason max_tokens with no text — CodeBudgetExhausted,
	// engaged, with its usage. ChatRequest.Effort is not mapped in commit E (see buildRequest).
	DefaultMaxTokens = 4096

	// MaxImageParts caps how many pictures one request may carry — OUR guard, not the provider's
	// limit: images bill as input tokens, so "how many" is "how much". The SAME number as
	// oaichat.MaxImageParts (the draft-idea door refuses with openrouter.MaxImageParts, which is that
	// constant, BEFORE StartRun); duplicated rather than imported so one transport does not depend on
	// another, and pinned equal by TestSurface — a door that admits 16 and a transport that refuses
	// 16 would fail a paid run after its lease was taken.
	MaxImageParts = 16

	// MaxResponseBytes caps how much of a response is read (oaichat's number and reasoning: every
	// byte here is text, and the cap REFUSES rather than trims — see readCapped).
	MaxResponseBytes = 4 << 20 // 4 MiB

	// JSONInstruction is appended to the system prompt in JSON mode. Anthropic has no response_format
	// on the wire, so the ask is made in words and the object is cut out of the answer
	// (aiprov.ExtractJSONObject) before the caller parses it.
	JSONInstruction = "Answer with exactly one JSON object and nothing else."

	// errorMessageLimit bounds the provider's text inside a sentence: a log line and a CallError, not a
	// dump of whatever the gateway sent.
	errorMessageLimit = 300
)

// provider is the billing key: CallError.Provider, ChatResult.Provider and the sentence prefix.
const provider = entity.AIProviderAnthropic

// Config configures the transport for one Anthropic account.
//
//	KeyFunc      asked for the key on EVERY request and by Enabled() — the registry's hook, so a key
//	             saved in the admin panel reaches the next request; "" (or a nil func) = disabled.
//	             Never serialised, never printed;
//	HTTPTimeout  the BASE of every request's budget (aiprov.CompletionBudget); <= 0 = the default.
//
// No base URL: the host is endpoints.AnthropicAPIBase, a constant — an editable base URL is a place
// a key can be sent.
type Config struct {
	KeyFunc     func() string
	HTTPTimeout time.Duration
}

// Client is the configured transport. A nil *Client is valid and permanently disabled.
type Client struct {
	cfg Config
	// base is endpoints.AnthropicAPIBase; a field only so a test can point it at a stand.
	base string
	// budgetBase — the BASE of one call's budget; the printing time of the caller's ceiling is added
	// per request (aiprov.CompletionBudget).
	budgetBase time.Duration
	// ⚠ No http.Client.Timeout: one number for every request, fixed before the answer's size is known,
	// is what used to cut calls whose ceiling had been raised. The deadline is set per request in post.
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
		base:       endpoints.AnthropicAPIBase,
		budgetBase: base,
		http:       &http.Client{CheckRedirect: refuseRedirect},
	}
}

// refuseRedirect keeps a redirect as the answer instead of following it (probe.refuseRedirect, same
// reason): net/http drops Authorization on a redirect to another host but FORWARDS x-api-key to
// wherever Location points. The request has exactly one legitimate destination; a 3xx comes back as a
// not-engaged refusal (aiprov.ClassifyStatus: provider_error, not retryable).
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Enabled reports whether a key is configured right now. Nil-safe.
func (c *Client) Enabled() bool {
	return c != nil && c.key() != ""
}

// BaseURL returns the API root, for log lines (a 404 may mean the path, not the slug). Nil-safe.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.base
}

// CompletionBase is the base this transport puts into aiprov.CompletionBudget on EVERY request; the
// router's per-call bound and the draft lease (router.budget / ChainBudget) read this same number, so
// the lease and the wire cannot drift. Nil-safe.
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

// Chat implements aiprov.Chatter: one Messages call of req on model.
//
// ⚠ ON AN ENGAGED FAILURE WHOSE ANSWER DID ARRIVE — an empty message, a refusal, ErrBudgetExhausted, a
// JSON-mode answer with no object — Chat returns BOTH the partial result (Usage, FinishReason, Model,
// RequestID; Text "") AND the error: the call was paid for and its size is known (oaichat's contract).
// Every other failure returns a nil result.
func (c *Client) Chat(ctx context.Context, model string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	// ONE KEY PER REQUEST: read exactly once here and carried to the header; a rotation or a switch-off
	// that lands mid-flight changes the NEXT request, never the x-api-key of this one.
	key := c.key()
	if key == "" {
		return nil, fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%s: no API key is set: %w", provider, aiprov.ErrNotConfigured))
	}
	if strings.TrimSpace(model) == "" {
		// "" would reach the provider as a 400 that reads as its fault rather than our missing value.
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: a message needs a model slug", provider))
	}
	payload, err := buildRequest(model, req)
	if err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, err)
	}
	return c.post(ctx, model, payload, req, key)
}

// ─── request ─────────────────────────────────────────────────────────────────────────────────────

// messagesRequest — ⚠ THE FIELD ORDER IS THE BYTE ORDER, AND THE GOLDENS PIN IT.
//
// NO `temperature`, AND THAT IS A DECISION, NOT AN OMISSION. The OpenAI-shaped path sends 0.2; here a
// non-default temperature is REJECTED WITH A 400 by Claude Sonnet 5, Opus 4.7/4.8/5 and Fable
// (UNVERIFIED (G-05) — bundled docs, model-migration: "Setting temperature … to a non-default value
// returns a 400"). A 400 is a refusal at the gate: not engaged, so the router falls back — on EVERY
// call, which on the default slug (claude-sonnet-5) is an outage that reads as a fallback. Omitted,
// every model accepts the request at its own default. Put it back only per model family, after G-05.
//
// No `thinking`, no `output_config`: see buildRequest (Effort is not mapped in commit E).
type messagesRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	System    string `json:"system,omitempty"`
	Messages  []any  `json:"messages"`
}

// textTurn is a turn whose content is a STRING — the user turn of a request without pictures.
type textTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// partsTurn is a turn whose content is a LIST OF BLOCKS — the user turn of a picture prompt:
//
//	{"role":"user","content":[
//	   {"type":"text","text":"..."},
//	   {"type":"image","source":{"type":"url","url":"https://…"}},
//	   {"type":"image","source":{"type":"base64","media_type":"image/png","data":"…"}}]}
//
// A type of its own, and Messages is []any, so textTurn.Content can stay a string (the compile-time
// guard lives in anthropic_test.go). UNVERIFIED (G-05): the block and source shapes.
type partsTurn struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// contentBlock is one member of a parts list; exactly one of Text / Source is set, by Type.
type contentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

// imageSource is Anthropic's picture source: {type:url, url} or {type:base64, media_type, data}.
type imageSource struct {
	Type      string `json:"type"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

// buildRequest marshals req for model.
//
// EFFORT IS NOT MAPPED IN COMMIT E. The callers send "none"/"minimal" today, meaning "do not think";
// the Anthropic spellings of that differ per model family (`thinking:{type:"disabled"}` is accepted by
// Sonnet 5 and Opus 4.7/4.8, a 400 on Fable and Opus 5.5; `output_config.effort` errors on Haiku 4.5 —
// UNVERIFIED (G-05)), and a wrong spelling is a 400 on every call. So nothing is sent, and the model
// runs at its own default — which on Sonnet 5 / Opus 5 / Fable is ADAPTIVE THINKING, not off (see
// DefaultMaxTokens). The mapping comes with G-05's answers, as one table.
func buildRequest(model string, req aiprov.ChatRequest) ([]byte, error) {
	if strings.TrimSpace(req.User) == "" {
		// A message with no user text is a 400 at Anthropic; pictures alone say nothing either.
		return nil, fmt.Errorf("%s: a message needs a user prompt", provider)
	}
	var user any = textTurn{Role: "user", Content: req.User}
	if len(req.ImageURLs) > 0 || req.UserAsParts {
		// UserAsParts: the caller's legacy multimodal shape even with no pictures — a one-block list.
		content, err := buildContentBlocks(req.User, req.ImageURLs)
		if err != nil {
			return nil, err
		}
		user = partsTurn{Role: "user", Content: content}
	}
	system := req.System
	if req.JSONMode {
		system = withJSONInstruction(system)
	}
	ceiling := req.MaxTokens
	if ceiling <= 0 {
		ceiling = DefaultMaxTokens
	}
	payload, err := json.Marshal(messagesRequest{
		Model:     model,
		MaxTokens: ceiling,
		System:    system,
		Messages:  []any{user},
	})
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", provider, err)
	}
	return payload, nil
}

// withJSONInstruction appends JSONInstruction after the caller's system prompt, byte for byte, or is
// the whole system prompt when the caller sent none.
func withJSONInstruction(system string) string {
	if strings.TrimSpace(system) == "" {
		return JSONInstruction
	}
	return system + "\n\n" + JSONInstruction
}

// buildContentBlocks turns a prompt plus picture addresses into the content list, refusing anything it
// cannot vouch for. The text block comes FIRST (oaichat's order and reason: it is the instruction).
func buildContentBlocks(userPrompt string, imageURLs []string) ([]contentBlock, error) {
	if len(imageURLs) > MaxImageParts {
		return nil, fmt.Errorf("%s: %d pictures exceeds the %d-picture limit for one request",
			provider, len(imageURLs), MaxImageParts)
	}
	blocks := make([]contentBlock, 0, len(imageURLs)+1)
	blocks = append(blocks, contentBlock{Type: "text", Text: userPrompt})
	for i, raw := range imageURLs {
		u := strings.TrimSpace(raw)
		src, err := imageSourceOf(u)
		if err != nil {
			return nil, fmt.Errorf("%s: picture %d: %w", provider, i+1, err)
		}
		blocks = append(blocks, contentBlock{Type: "image", Source: src})
	}
	return blocks, nil
}

// imageSourceOf is the source block of one picture address: an http(s) URL goes by reference (the
// provider fetches it), a data: URI goes as its bytes.
func imageSourceOf(u string) (*imageSource, error) {
	if err := validateImageURL(u); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(u, "data:") {
		return &imageSource{Type: "url", URL: u}, nil
	}
	// data:image/png;base64,AAAA — validateImageURL has seen the comma.
	header, payload, _ := strings.Cut(u, ",")
	params := strings.Split(strings.TrimPrefix(header, "data:"), ";")
	isBase64 := false
	for _, p := range params[1:] {
		if strings.EqualFold(strings.TrimSpace(p), "base64") {
			isBase64 = true
		}
	}
	if !isBase64 {
		// The base64 source carries base64 bytes and nothing else; a percent-encoded URI sent as one
		// is a 400 that reads as the provider's fault.
		return nil, fmt.Errorf("data URI must be base64-encoded")
	}
	if strings.TrimSpace(payload) == "" {
		return nil, fmt.Errorf("data URI carries no payload")
	}
	return &imageSource{Type: "base64", MediaType: strings.TrimSpace(params[0]), Data: payload}, nil
}

// validateImageURL admits exactly two forms and rejects the rest (oaichat.validateImageURL, copied).
//
// THE REJECTION IS THE POINT. The provider fetches whatever address we hand it, from its own network —
// an unvalidated string here is an outbound fetch we authored on someone else's behalf. `file://`,
// `gopher://` and friends have no business in a picture list, and an empty string would become a 400
// that reads as a provider fault rather than as our own missing value.
func validateImageURL(u string) error {
	switch {
	case u == "":
		return fmt.Errorf("empty picture address")
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return nil
	case strings.HasPrefix(u, "data:image/"):
		if !strings.Contains(u, ",") {
			return fmt.Errorf("data URI carries no payload")
		}
		return nil
	default:
		return fmt.Errorf("picture address must be http(s):// or a data:image/… URI, got %q", truncate(u, 40))
	}
}

// ─── the wire ────────────────────────────────────────────────────────────────────────────────────
//
// THE «BOUGHT / NOT BOUGHT» BOUNDARY is oaichat's, row for row (its table is the reference):
//
//	NOT ENGAGED: every refusal before the wire; DNS / refused dial / TLS / a write that broke —
//	             WroteRequest never fired; ANY non-2xx, including 404, 401, 402, 429, 529.
//	ENGAGED:     the request was written and then the budget ran out / the caller left / the edge
//	             dropped the connection; the body did not finish or outgrew the ceiling; ANY 2xx that
//	             cannot be used (broken envelope, error envelope, empty answer, refusal, spent budget,
//	             no JSON object in JSON mode).
//
// RETRYABLE only when the same request may succeed later AND nobody paid: a transport failure or a
// deadline before the write, 408, 429, 5xx (529 included). Never a caller's cancel, never engaged.

// messagesResponse is the envelope. UNVERIFIED (G-05): every tag below. id and usage are read
// LENIENTLY (raw JSON, parsed best-effort) so a provider that spells one unexpectedly costs that
// number, never the answer: a decode failure of the envelope is an engaged, paid-for fault.
type messagesResponse struct {
	ID         json.RawMessage `json:"id"`
	Model      string          `json:"model"`
	Content    []responseBlock `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      json.RawMessage `json:"usage"`
	Error      *apiError       `json:"error"`
}

// responseBlock is one content block of the answer. Only type "text" is the answer: thinking blocks
// (adaptive thinking is on by default on the current models) and anything else are not.
type responseBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// wireUsage is the usage block. ⚠ ANTHROPIC'S input_tokens EXCLUDES THE CACHE (aiprov.TokenUsage):
// the prompt is input + cache_read + cache_creation. A misspelled tag here is SILENT — every count
// stays zero and the call reads as free — which is why the tests assert non-zero numbers.
type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// apiError is the error object: {"type":"error","error":{"type":"not_found_error","message":"…"}}.
type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// post is THE ONLY PLACE THIS TRANSPORT TALKS TO /v1/messages.
func (c *Client) post(ctx context.Context, model string, payload []byte, req aiprov.ChatRequest, key string) (*aiprov.ChatResult, error) {
	// THE BUDGET IS BOUGHT BY THE CALLER'S CEILING, NOT BY DefaultMaxTokens, AND THAT IS WHAT KEEPS THE
	// WIRE AND THE ROUTER ON ONE NUMBER. The router bounds each call by CompletionBudget(CompletionBase(),
	// req.MaxTokens) and sizes the draft lease the same way; buying 4096 tokens' worth of time here for
	// a caller that asked for no ceiling would be 136 s the router never grants — its deadline would cut
	// first, and this number would lie. The callers with no ceiling run on the base, as they do on
	// OpenRouter today. The caller's deadline still wins: WithTimeout only ever shortens it.
	ctx, cancel := context.WithTimeout(ctx, aiprov.CompletionBudget(c.budgetBase, req.MaxTokens))
	defer cancel()
	ctx, wroteRequest := aiprov.ObserveWrite(ctx)

	endpoint := strings.TrimRight(c.base, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build request: %w", provider, err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", APIVersion)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// The same text on both sides of the boundary; the write flag, not the prose, decides it.
		engaged := wroteRequest()
		code := aiprov.Interruption(ctx, err)
		retryable := !engaged && code != aiprov.CodeCanceled
		return nil, fail(code, 0, engaged, retryable, fmt.Errorf("%s: request failed: %w", provider, err))
	}
	defer resp.Body.Close()

	body, err := readCapped(resp.Body, MaxResponseBytes, "messages response")
	// THE STATUS IS JUDGED BEFORE THE BODY'S FATE. A non-2xx is a refusal at the gate — no money moved,
	// whatever happened to the error body afterwards (cut, oversized, timed out): it stays a
	// not-engaged refusal with its own code and its 404 sentinel, so the router can still fall back.
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
	var mr messagesResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: could not decode API response envelope: %w", provider, err))
	}
	if mr.Error != nil && strings.TrimSpace(mr.Error.Message) != "" {
		return nil, fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: API error: %s", provider, boundedMessage(mr.Error.Message, key)))
	}
	res := result(model, &mr)
	text := res.Text
	res.Text = "" // a partial result never carries an answer; set again only on success
	switch {
	case mr.StopReason == "refusal":
		// A REFUSAL IS NEVER AN ANSWER, EVEN WITH TEXT. The classifier can stop the model mid-answer;
		// the docs say to discard that partial output (UNVERIFIED (G-05)). Engaged by the house rule for
		// a 2xx — a pre-output refusal is said to be unbilled, its usage then reads zero and prices zero.
		return res, fail(aiprov.CodeEmptyAnswer, status, true, false,
			fmt.Errorf("%s: the model refused to answer (stop_reason refusal)", provider))
	case text == "" && mr.StopReason == "max_tokens":
		// Deterministic: the ceiling (thinking included) was spent before a character of answer — the
		// setting is wrong, and the next press buys the same nothing.
		return res, fail(aiprov.CodeBudgetExhausted, status, true, false, fmt.Errorf(
			"%s: %w (%d completion tokens spent, none of them answer)", provider, aiprov.ErrBudgetExhausted, res.Usage.Completion))
	case text == "":
		return res, fail(aiprov.CodeEmptyAnswer, status, true, false,
			fmt.Errorf("%s: model returned an empty message", provider))
	}
	if req.JSONMode {
		js, ok := aiprov.ExtractJSONObject(text)
		if !ok {
			return res, fail(aiprov.CodeEmptyAnswer, status, true, false,
				fmt.Errorf("%s: JSON mode: the answer carries no JSON object (stop_reason %s)", provider, orNone(mr.StopReason)))
		}
		text = js
	}
	res.Text = text
	return res, nil
}

// result turns a decoded envelope into a ChatResult (Text: the answer as it came, trimmed). Engaged is
// always true: an answer came back, so the request was written and served. CostUSD stays NULL — the
// Messages API reports no charge; the ledger prices from the catalogue.
func result(model string, mr *messagesResponse) *aiprov.ChatResult {
	var text strings.Builder
	for _, b := range mr.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	r := &aiprov.ChatResult{
		Text:         strings.TrimSpace(text.String()),
		FinishReason: finishReason(mr.StopReason),
		Provider:     provider,
		Model:        model,
		Engaged:      true,
	}
	if m := strings.TrimSpace(mr.Model); m != "" {
		r.Model = m
	}
	var id string
	if json.Unmarshal(mr.ID, &id) == nil {
		r.RequestID = strings.TrimSpace(id)
	}
	var u wireUsage
	if len(mr.Usage) > 0 && json.Unmarshal(mr.Usage, &u) == nil {
		r.Usage = aiprov.TokenUsage{
			Prompt:     u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
			Cached:     u.CacheReadInputTokens,
			Completion: u.OutputTokens,
			// Reasoning stays 0: the usage block does not split thinking out of output_tokens
			// (UNVERIFIED (G-05)); Completion already holds it, so the price is whole.
		}
	}
	return r
}

// finishReason maps stop_reason onto the OpenAI words the callers already read ("stop", "length");
// anything else passes through as the provider said it. UNVERIFIED (G-05): the stop_reason values.
func finishReason(stop string) string {
	switch stop {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "refusal":
		return "refusal"
	default:
		return stop
	}
}

// statusError classifies a non-2xx answer BY STATUS ALONE (aiprov.ClassifyStatus). None of these is
// engaged: a refusal at the gate is not billed. A 404 opens with the ErrModelUnavailable sentence (a
// setting, not weather). Anthropic's 402 is billing_error → out_of_credits; a low balance reported as a
// 400 would read bad_request — the matrix does not read prose, G-05 says which one it is.
func statusError(status int, body []byte, key string) error {
	code, retryable := aiprov.ClassifyStatus(status)
	msg := apiErrorMessage(body, key)
	var err error
	if status == http.StatusNotFound {
		err = fmt.Errorf("%s: %w: API error (HTTP %d): %s", provider, aiprov.ErrModelUnavailable, status, msg)
	} else {
		err = fmt.Errorf("%s: API error (HTTP %d): %s", provider, status, msg)
	}
	return fail(code, status, false, retryable, err)
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

// readCapped reads at most limit bytes and REFUSES anything longer instead of handing back a prefix
// (oaichat.readCapped, copied): the limit+1 byte is the whole difference between "exactly at the
// ceiling" and "over it", and a prefix that happens to parse would be accepted as a whole answer.
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

// apiErrorMessage best-effort pulls error.message out of an error body, falling back to the raw body;
// either way bounded and scrubbed of the key.
func apiErrorMessage(body []byte, key string) string {
	var env struct {
		Error   *apiError `json:"error"`
		Message string    `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
			return boundedMessage(env.Error.Message, key)
		}
		if strings.TrimSpace(env.Message) != "" {
			return boundedMessage(env.Message, key)
		}
	}
	return boundedMessage(string(body), key)
}

// boundedMessage is the provider's text as a sentence may carry it: the key scrubbed FIRST (a cut
// made before the scrub could leave a prefix of the key standing), then at most errorMessageLimit
// runes. A gateway that echoes the request's headers in its error page must not put the key into a
// log line or a ledger row.
func boundedMessage(msg, key string) string {
	msg = strings.TrimSpace(msg)
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[key]")
	}
	return truncate(msg, errorMessageLimit)
}

// truncate cuts s to at most n runes, never inside one.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := 0
	for i := range s {
		if runes == n {
			return s[:i] + "…"
		}
		runes++
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
