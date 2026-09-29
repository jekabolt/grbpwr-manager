// Package runblob is the adapter for runblob.io, a generation aggregator (video AND pictures): one
// POST to {endpoints.RunblobHost}/v1/{path}/generate submits a generation and answers with its price,
// one GET to /v1/{path}/generations/{id} reads its status. The key comes only from the admin panel
// (registry KeyFunc; there is no env key for this provider, R-05).
//
// WHO CALLS IT. Since B-31 the image transport of this package (images.go, designgen.ImageTransport)
// pays image.generate runs through Submit/Status when the panel routes that purpose to runblob; the
// VIDEO half still has no purpose (D-05 as amended by 06-BRIEFS-G: G2 names `video.generate`). The
// adapter itself knows no purpose and writes no ledger row: Submission.PriceUSD is the number a caller
// books as entity.AICostProvider — the provider's own price, returned at submit time.
//
// It is a port of the chat transports' guards, not a chat transport (a generation is not a
// completion; aiprov.Chatter does not fit), and every guard they learnt is here once more:
//
//   - the ENGAGED boundary — aiprov.ObserveWrite on the submit: a submit is money, so a written
//     request that then broke is engaged, a refused dial is free; a status read is a GET that buys
//     nothing and is never engaged, whatever happens to it;
//   - the READ CEILING — MaxResponseBytes, refused by name, never trimmed;
//   - the STATUS CLASSIFICATION — aiprov.ClassifyStatus, by status alone, with ONE split by method:
//     a 404 on the submit is the path (a setting: aiprov.ErrModelUnavailable), a 404 on the status
//     read is the generation id (ErrGenerationNotFound, code not_found) — the probe reads exactly that
//     404 on the zero uuid as «key accepted», so the two must never share a word;
//   - the SENTENCES — "runblob: API error (HTTP 401): Invalid API key", the provider's `detail`
//     bounded and never carrying the key;
//   - REDIRECTS REFUSED — the Bearer key and the caller's prompt have one destination;
//   - the PATH — the family path comes from a closed list and the id must be a uuid, both checked
//     before the wire, so no argument can steer the request (and the key) to another path.
//
// Every failure is an *aiprov.CallError; its Error() is the sentence, its fields are the facts.
//
// ⚠ WIRE SHAPES (runblob-specs/kling.json + the Nano Banana docs page, read 2026-09-28; the Kling
// video page read 2026-09-27): Bearer auth; POST /v1/{path}/generate → 201 {generation_id |
// task_uuid (Nano Banana), status:"pending", price:"0.2900" | "calculating", description}; GET
// /v1/{path}/generations/{id} → {status, video_url | image_url | result_image_url, model, message,
// error}; 401 {"detail":"Invalid API key"}; 402 {"detail":"INSUFFICIENT_CREDITS"}; statuses pending |
// processing | completed | failed, a failed job REFUNDED. Since H5 (2026-09-29) also ChatGPT Images
// (runblob-specs/chatgpt-images.md: task_uuid, result_image_url), Kling O1/O3 video (kling.json
// endpoints[1], [2]) and Seedance (seedance.json: task_uuid, a 201 with NO price, output.video_urls,
// errors as {code, message}). Everything not on those pages is marked UNVERIFIED (G-06) where it is
// used, and G-06 checks it live with the owner's key.
package runblob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Family paths — what stands between /v1/ and /generate | /generations/{id}. A CLOSED list: the
// caller owns the family's body (this adapter knows no family's fields), but not the path the key is
// sent to. One family is one path: `kling` (video) and `kling/o1-photo` (a picture) are two entries,
// two bodies, two answers — never a family plus a free-text sub-path.
const (
	// FamilyKling — video (runblob-specs/kling.json: 14 models, kling_2.5_turbo the default). The
	// answer's id is generation_id, its result video_url.
	FamilyKling = "kling"
	// FamilyVeo — UNVERIFIED (G-06): named on runblob's landing page (veo-3-fast / veo-3-quality,
	// POST /v1/veo/generate), absent from the docs nav on 2026-09-27 AND from the docs' own search
	// index on 2026-09-29 (catalogue/runblob.json): nothing routes to it.
	FamilyVeo = "veo"
	// PathGemini — Nano Banana pictures (the docs page read 2026-09-28): POST /v1/gemini/generate
	// answers {task_uuid, status, price}; GET /v1/gemini/generations/{task_uuid} answers
	// {task_uuid, status, prompt, result_image_url | null, message | null}. ONE picture per task.
	PathGemini = "gemini"
	// PathKlingO1Photo / PathKlingO3Photo — Kolors pictures (runblob-specs/kling.json): generation_id
	// on the submit, image_url on the status read.
	PathKlingO1Photo = "kling/o1-photo"
	PathKlingO3Photo = "kling/o3-photo"
	// PathKlingO1Video / PathKlingO3Video — Kling omni video (runblob-specs/kling.json endpoints[1],
	// [2]): generation_id on the submit, video_url on the status read. O1 takes no `model` (the
	// endpoint IS kling_o1); O3 takes model kling_o3 | kling_o3_pro.
	PathKlingO1Video = "kling/o1-video"
	PathKlingO3Video = "kling/o3-video"
	// PathSeedance — Seedance video (runblob-specs/seedance.json; the docs page read 2026-09-29):
	// task_uuid on the submit (and NO price in the 201), output.video_urls[] on the status read.
	PathSeedance = "seedance"
	// PathChatGPTImages — ChatGPT Images pictures (runblob-specs/chatgpt-images.md, read 2026-09-29):
	// task_uuid + price on the submit, result_image_url on the status read, like Nano Banana.
	PathChatGPTImages = "chatgpt-images"
)

// knownPaths — the closed list, in the order the refusal sentence names it.
var knownPaths = []string{PathGemini, FamilyKling, PathKlingO1Photo, PathKlingO3Photo, FamilyVeo,
	PathKlingO1Video, PathKlingO3Video, PathSeedance, PathChatGPTImages}

const (
	// MaxResponseBytes caps how much of a response is read (the chat transports' number): every answer
	// here is a small JSON object — the video itself is a URL, never bytes on this path — and the cap
	// REFUSES rather than trims (see readCapped).
	MaxResponseBytes = 4 << 20 // 4 MiB

	// PriceCalculating is the price word of a generation whose cost is not known at submit time
	// (documented for Kling Motion Control). It is not a number, and not zero: PriceUSD stays NULL.
	PriceCalculating = "calculating"

	// errorMessageLimit bounds the provider's text inside a sentence or a Generation.Error.
	errorMessageLimit = 300

	// provider is the billing key: CallError.Provider and the sentence prefix.
	provider = entity.AIProviderRunblob
)

// ErrGenerationNotFound — the status read answered 404: the generation id is unknown to runblob (or
// belongs to another account). A run to abandon, not a setting to fix — which is why it is not
// aiprov.ErrModelUnavailable, the submit's 404. No prefix: it is raised mid-sentence ("runblob: no
// such generation: API error (HTTP 404): …"), the aiprov sentinel convention.
var ErrGenerationNotFound = errors.New("no such generation")

var (
	// pathShape is the character set of a family path that cannot climb out of /v1/: one segment of
	// lower-case letters, digits and underscores joined by single inner hyphens (chatgpt-images),
	// optionally ONE sub-segment that may also carry a hyphen (o1-photo) — no dot, no query, no
	// percent, no empty segment, no leading or trailing hyphen on the first. Checked BEFORE the closed
	// list, so a path added to the list later with a bad spelling is still refused, not sent.
	pathShape = regexp.MustCompile(`^[a-z0-9_]+(-[a-z0-9_]+)*(/[a-z0-9_-]+)?$`)
	// uuidShape is a generation id (the probe's zero uuid is this shape). UNVERIFIED (G-06): that
	// runblob's generation_id is a uuid — the docs name the field, not its format; the probe was built
	// on the same assumption.
	uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Config configures the adapter for one runblob account.
//
//	KeyFunc      asked for the key on EVERY request and by Enabled() — the registry's hook, so a key
//	             saved in the admin panel reaches the next request; "" (or a nil func) = disabled.
//	             Never serialised, never printed;
//	HTTPTimeout  the deadline of every request (a submit answers at once with a pending generation;
//	             nothing prints, so nothing is added to it); <= 0 = aiprov.DefaultBudgetBase.
//	Transport    the http.RoundTripper every request goes through; nil = net/http's default. It is
//	             the seam for a stand in ANOTHER package's test (designgen's route tests cannot reach
//	             this package's private base): a RoundTripper that redirects platform.runblob.io to
//	             an httptest server. It is not a URL: nothing in config, env or the panel can set
//	             it, and every request is still BUILT against endpoints.RunblobHost.
//
// No base URL: the host is endpoints.RunblobHost, a constant — an editable base URL is a place a key
// can be sent.
type Config struct {
	KeyFunc     func() string
	HTTPTimeout time.Duration
	Transport   http.RoundTripper
}

// Client is the configured adapter. A nil *Client is valid and permanently disabled.
type Client struct {
	cfg Config
	// base is endpoints.RunblobHost; a field only so a test can point it at a stand.
	base string
	// budgetBase is the deadline of one request (aiprov.CompletionBudget with no ceiling = the base).
	budgetBase time.Duration
	// ⚠ No http.Client.Timeout: the deadline is set per request, as in every transport of the stack.
	http *http.Client
}

// New builds the adapter. It validates nothing: a missing key just leaves it disabled.
func New(cfg Config) *Client {
	base := cfg.HTTPTimeout
	if base <= 0 {
		base = aiprov.DefaultBudgetBase
	}
	return &Client{
		cfg:        cfg,
		base:       endpoints.RunblobHost,
		budgetBase: base,
		http:       &http.Client{Transport: cfg.Transport, CheckRedirect: refuseRedirect},
	}
}

// refuseRedirect keeps a redirect as the answer instead of following it (probe.refuseRedirect, same
// reason). net/http drops Authorization on a redirect to another host, but a subdomain keeps it, and a
// 307/308 re-POSTs the caller's prompt to wherever Location points. The request has exactly one
// legitimate destination; a 3xx comes back as a not-engaged refusal (aiprov.ClassifyStatus:
// provider_error, not retryable).
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Provider is the billing key this adapter books under. Nil-safe (a constant).
func (c *Client) Provider() string { return provider }

// Enabled reports whether a key is configured right now. Nil-safe.
func (c *Client) Enabled() bool {
	return c != nil && c.key() != ""
}

// BaseURL returns the host requests go to, for log lines. Nil-safe.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.base
}

// CompletionBase is the deadline of every request this adapter sends — the number a future caller
// that holds a lease across a submit must read, so the lease and the wire read one field. Nil-safe.
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

// ─── submit ──────────────────────────────────────────────────────────────────────────────────────

// Submission is an accepted submit: what was bought and how to find it again.
//
//	ID           generation_id (Kling) or task_uuid (Nano Banana, ChatGPT Images, Seedance) — a
//	             uuid, the id Status reads;
//	Status       the provider's word at submit time ("pending");
//	PriceUSD     the provider's price for THIS generation, in USD — what a caller books as
//	             entity.AICostProvider. NULL when the provider did not state it ("calculating",
//	             absent, zero): unknown is never booked as zero;
//	Description  the provider's description of the request, as sent;
//	Engaged      always true: the request was accepted, so it may be billed.
type Submission struct {
	ID          string
	Status      string
	PriceUSD    decimal.NullDecimal
	Description string
	Engaged     bool
}

// submitResponse is the documented 201 body. The id arrives under ONE of two names — generation_id
// (Kling, veo) or task_uuid (Nano Banana) — and the adapter reads whichever is present. price is RAW:
// a string ("0.2900", "calculating"), maybe a number or null (UNVERIFIED (G-06): only the string forms
// are documented) — parsePrice reads each.
type submitResponse struct {
	GenerationID string          `json:"generation_id"`
	TaskUUID     string          `json:"task_uuid"`
	Status       string          `json:"status"`
	Price        json.RawMessage `json:"price"`
	Description  string          `json:"description"`
}

// Submit posts body as one generation of the family at path: POST {base}/v1/{path}/generate.
//
// body is the family's request as the caller built it (model, prompt, duration, callback_url, …) and
// goes on the wire as JSON, keys sorted — this adapter adds no field and removes none.
//
// ⚠ ON AN ENGAGED FAILURE WHOSE ANSWER DECODED — a 2xx with no id, an id that is not a uuid, a
// price that is not a number — Submit returns BOTH the partial Submission (PriceUSD when it was
// readable, ID when it was usable) AND the error: the provider accepted the request and may be billing
// it, and a caller that drops the partial drops the price with it. Every other failure — an engaged
// one whose answer did not decode included — returns a nil Submission.
func (c *Client) Submit(ctx context.Context, path string, body map[string]any) (*Submission, error) {
	// ONE KEY PER REQUEST: read exactly once here and carried to the header; a rotation or a switch-off
	// that lands mid-flight changes the NEXT request, never the Authorization of this one.
	key := c.key()
	if key == "" {
		return nil, fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%s: no API key is set: %w", provider, aiprov.ErrNotConfigured))
	}
	if err := validPath(path); err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, err)
	}
	if len(body) == 0 {
		// An empty generate is a 422 at best — our missing value, booked as the provider's refusal.
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: a %s generation needs a request body", provider, path))
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: marshal %s request: %w", provider, path, err))
	}

	status, raw, err := c.exchange(ctx, http.MethodPost, "/v1/"+path+"/generate", payload, key)
	if err != nil {
		return nil, err
	}

	// ─── FROM HERE ON A 2xx: THE SUBMIT WAS ACCEPTED, SO IT MAY BE BILLED — every failure is engaged ───
	var sr submitResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: could not decode the %s submit answer: %w", provider, path, err))
	}
	sub := &Submission{
		ID:          firstNonBlank(sr.GenerationID, sr.TaskUUID),
		Status:      strings.TrimSpace(sr.Status),
		Description: sr.Description,
		Engaged:     true,
	}
	price, priceErr := parsePrice(sr.Price)
	sub.PriceUSD = price
	switch {
	case sub.ID == "":
		// The job may have been queued and billed, and there is no id to find it by.
		return sub, fail(aiprov.CodeEmptyAnswer, status, true, false,
			fmt.Errorf("%s: the %s submit was accepted (HTTP %d) with no generation_id / task_uuid", provider, path, status))
	case !uuidShape.MatchString(sub.ID):
		// An id Status would refuse before the wire is an id nobody can poll: the job is lost at the
		// moment it was bought, and the caller must hear it now, not at the first poll.
		id := sub.ID
		sub.ID = ""
		return sub, fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: the %s submit returned an id that is not a uuid: %q", provider, path, boundedMessage(id, key)))
	case priceErr != nil:
		// The generation IS running (the id is good); only its price is unreadable. The partial carries
		// the id, so the caller can still poll it — and books no number rather than a wrong one.
		return sub, fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: the %s submit answered with %s", provider, path, boundedMessage(priceErr.Error(), key)))
	}
	return sub, nil
}

// firstNonBlank — the first argument that is not blank, trimmed.
func firstNonBlank(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// parsePrice reads the submit's price into a KNOWN USD amount, NULL, or an error.
//
//	"0.2900" / 0.29      → 0.29
//	"calculating"        → NULL (documented: the price is not known yet)
//	absent / null / ""   → NULL
//	"0" / "0.0000"       → NULL — 0 is not a known price (the ledger's rule, oaichat.parseCost): a
//	                       zero would book a paid generation as free;
//	"abc" / negative     → an error: the provider stated a price and it is not one. Unknown is never
//	                       guessed. The error quotes the provider's text whole; Submit bounds it and
//	                       scrubs the key from it (a cut made here, before the scrub, could leave a
//	                       prefix of the key standing).
func parsePrice(raw json.RawMessage) (decimal.NullDecimal, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return decimal.NullDecimal{}, nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return decimal.NullDecimal{}, fmt.Errorf("a price that is not a string: %s", s)
		}
		s = strings.TrimSpace(str)
	}
	if s == "" || strings.EqualFold(s, PriceCalculating) {
		return decimal.NullDecimal{}, nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.NullDecimal{}, fmt.Errorf("a price that is not a number: %q", s)
	}
	if d.IsNegative() {
		return decimal.NullDecimal{}, fmt.Errorf("a negative price: %q", s)
	}
	if d.IsZero() {
		return decimal.NullDecimal{}, nil
	}
	return decimal.NullDecimal{Decimal: d, Valid: true}, nil
}

// ─── status ──────────────────────────────────────────────────────────────────────────────────────

// Status words (runblob-specs/kling.json `conventions.statuses`; the Nano Banana page lists the same
// four). A failed job is REFUNDED («Your balance is automatically refunded on any failed task»).
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Generation is one status read.
//
//	Status    the provider's word — pending | processing | completed | failed;
//	VideoURL  a video result, once there is one — video_url (Kling), or the FIRST of
//	          output.video_urls (Seedance: one task is one clip, the list is its shape only);
//	ImageURL  a picture result, once there is one — image_url (Kling photo) or result_image_url (Nano
//	          Banana), whichever the family answers with; ResultURL picks the one that is set;
//	Model     the model that ran it (kling_2.5_turbo, kling-o1-photo, …);
//	Message   the provider's `message`: the error CODE of a failed job (TIMEOUT,
//	          CONTENT_POLICY_VIOLATION, OPENAI_DECLINED, …); null when none. Bounded, key-scrubbed;
//	Error     the provider's `error` text (the Kling video page's field), bounded and scrubbed of the
//	          key; "" when none. Failure joins the two for a sentence.
type Generation struct {
	Status   string
	VideoURL string
	ImageURL string
	Model    string
	Message  string
	Error    string
}

// ResultURL is the one result address a completed generation carries — the video's or the picture's.
func (g *Generation) ResultURL() string {
	if g == nil {
		return ""
	}
	return firstNonBlank(g.VideoURL, g.ImageURL)
}

// Failure is what a failed generation said about itself: `error` when set, else `message` (the
// code), else "".
func (g *Generation) Failure() string {
	if g == nil {
		return ""
	}
	return firstNonBlank(g.Error, g.Message)
}

// generationResponse is the documented status body across the families. error and message are RAW:
// null, a string, or an object (Seedance documents {code, message}) — errorText reads each without
// failing the envelope. output is RAW too: Seedance's {video_urls, last_frame_url} | null, read by
// outputVideoURL without failing the envelope on any other shape.
type generationResponse struct {
	Status         string          `json:"status"`
	VideoURL       string          `json:"video_url"`
	ImageURL       string          `json:"image_url"`
	ResultImageURL string          `json:"result_image_url"`
	Output         json.RawMessage `json:"output"`
	Model          string          `json:"model"`
	Message        json.RawMessage `json:"message"`
	Error          json.RawMessage `json:"error"`
}

// outputVideoURL — the first non-blank entry of Seedance's output.video_urls, "" for null or any
// other shape.
func outputVideoURL(raw json.RawMessage) string {
	var out struct {
		VideoURLs []string `json:"video_urls"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return firstNonBlank(out.VideoURLs...)
}

// Status reads generation id of the family at path: GET {base}/v1/{path}/generations/{id}.
//
// NEVER ENGAGED: a GET buys nothing — the generation was paid for at ITS submit, and a status read
// that failed (timed out, cut, garbled) moved no money whatever happened to it. Such a failure is
// retryable (looking again is free), unless the caller left or the provider refused the read itself.
func (c *Client) Status(ctx context.Context, path, id string) (*Generation, error) {
	key := c.key()
	if key == "" {
		return nil, fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%s: no API key is set: %w", provider, aiprov.ErrNotConfigured))
	}
	if err := validPath(path); err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, err)
	}
	if !uuidShape.MatchString(id) {
		// The id is a path segment: "../", "?", "/" in it would steer the key to another endpoint.
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: a generation id must be a uuid, got %q", provider, truncate(id, 40)))
	}

	status, raw, err := c.exchange(ctx, http.MethodGet, "/v1/"+path+"/generations/"+id, nil, key)
	if err != nil {
		return nil, err
	}
	var gr generationResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fail(aiprov.CodeProviderError, status, false, true,
			fmt.Errorf("%s: could not decode the %s status answer: %w", provider, path, err))
	}
	g := &Generation{
		Status:   strings.TrimSpace(gr.Status),
		VideoURL: firstNonBlank(gr.VideoURL, outputVideoURL(gr.Output)),
		ImageURL: firstNonBlank(gr.ImageURL, gr.ResultImageURL),
		Model:    strings.TrimSpace(gr.Model),
		Message:  errorText(gr.Message, key),
		Error:    errorText(gr.Error, key),
	}
	if g.Status == "" {
		// A status read that names no status cannot be acted on; free, so looking again is the answer.
		return nil, fail(aiprov.CodeEmptyAnswer, status, false, true,
			fmt.Errorf("%s: the %s status answer (HTTP %d) carries no status", provider, path, status))
	}
	return g, nil
}

// ─── the wire ────────────────────────────────────────────────────────────────────────────────────
//
// THE «BOUGHT / NOT BOUGHT» BOUNDARY (the chat transports' table, split by method):
//
//	SUBMIT (POST — every one is a purchase):
//	  NOT ENGAGED: every refusal before the wire; DNS / refused dial / TLS / a write that broke —
//	               WroteRequest never fired; ANY non-2xx, 5xx included (the matrix: a refusal at
//	               the gate is not billed — see statusError for the one doubt about that);
//	  ENGAGED:     the request was written and then the deadline ran out / the caller left / the edge
//	               dropped the connection; the 2xx body did not finish or outgrew the ceiling; ANY
//	               2xx that cannot be used.
//	STATUS (GET — buys nothing): never engaged.
//
// RETRYABLE only when the same request may succeed later AND nobody paid: a submit's transport
// failure or deadline before the write, 408, 429, 5xx; a status read's every failure but a caller's
// cancel and a refusal the matrix calls final (401/402/403/404/422).

// exchange sends one request and hands back a 2xx status and body, or the *aiprov.CallError of
// everything else. It is THE ONLY PLACE THIS ADAPTER TALKS TO runblob.
func (c *Client) exchange(ctx context.Context, method, path string, payload []byte, key string) (int, []byte, error) {
	submit := method == http.MethodPost
	// The deadline is set PER REQUEST (no http.Client.Timeout). No ceiling is printed here, so the
	// budget is the base — the same function every transport asks, not a second formula.
	ctx, cancel := context.WithTimeout(ctx, aiprov.CompletionBudget(c.budgetBase, 0))
	defer cancel()
	ctx, wroteRequest := aiprov.ObserveWrite(ctx)

	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	endpoint := strings.TrimRight(c.base, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, nil, fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build request: %w", provider, err))
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Accept", "application/json")
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// The same text on both sides of the boundary; the write flag, not the prose, decides it — and
		// only on a submit: a GET that was written bought nothing.
		engaged := submit && wroteRequest()
		code := aiprov.Interruption(ctx, err)
		retryable := !engaged && code != aiprov.CodeCanceled
		return 0, nil, fail(code, 0, engaged, retryable, fmt.Errorf("%s: request failed: %w", provider, err))
	}
	defer resp.Body.Close()

	raw, err := readCapped(resp.Body, MaxResponseBytes, what(submit))
	// THE STATUS IS JUDGED BEFORE THE BODY'S FATE. A non-2xx is a refusal at the gate — no money moved,
	// whatever happened to the error body afterwards (cut, oversized, timed out): it stays a
	// not-engaged refusal with its own code and sentinel. The body, when it did arrive, only lends the
	// sentence its words.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err != nil {
			raw = []byte("response body unavailable: " + err.Error())
		}
		return 0, nil, statusError(submit, resp.StatusCode, raw, key)
	}
	if err != nil {
		code := aiprov.CodeTooLarge
		if !errors.Is(err, aiprov.ErrResponseTooLarge) {
			code = aiprov.Interruption(ctx, err)
		}
		// A submit's answer WAS there — engaged. A status read's is a free read that can be redone.
		return 0, nil, fail(code, resp.StatusCode, submit, !submit && code != aiprov.CodeCanceled,
			fmt.Errorf("%s: read response: %w", provider, err))
	}
	return resp.StatusCode, raw, nil
}

func what(submit bool) string {
	if submit {
		return "generate response"
	}
	return "status response"
}

// statusError classifies a non-2xx answer BY STATUS ALONE (aiprov.ClassifyStatus). None is engaged. The
// two 404s are told apart by the METHOD, never by the provider's English: on the submit it is the
// path (a setting — ErrModelUnavailable, model_unknown), on the status read it is the id (a run to
// abandon — ErrGenerationNotFound, not_found).
//
// ⚠ THE ONE DOUBT, SAID OUT LOUD: a 5xx on a SUBMIT is classified as the matrix says — not engaged,
// retryable. internal/fal learnt (G-03) that a gateway can lose the queue's answer AFTER the enqueue
// and books every 5xx but a bare 503 (and a 408) on its submit as engaged. runblob documents its own
// 500 as REFUNDED («Internal error / upload failure. Charge is refunded», runblob-specs/kling.json
// errors.http.500), which is the matrix's reading; the gateway in front of it is not documented
// (G-06). The image transport (images.go) never resubmits on its own: a not-engaged failure hands the
// run to the route's next candidate on a fresh attempt, exactly as the matrix promises.
func statusError(submit bool, status int, body []byte, key string) error {
	code, retryable := aiprov.ClassifyStatus(status)
	msg := apiErrorMessage(body, key)
	var err error
	switch {
	case status == http.StatusNotFound && submit:
		err = fmt.Errorf("%s: %w: API error (HTTP %d): %s", provider, aiprov.ErrModelUnavailable, status, msg)
	case status == http.StatusNotFound:
		code, retryable = aiprov.CodeNotFound, false
		err = fmt.Errorf("%s: %w: API error (HTTP %d): %s", provider, ErrGenerationNotFound, status, msg)
	default:
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

// validPath refuses a family path that is not of the plain shape or not on the closed list — before
// the wire, so a typo or an injected "kling/../x" never carries the key anywhere.
func validPath(path string) error {
	if !pathShape.MatchString(path) {
		return fmt.Errorf("%s: a family path is lower-case letters, digits, underscores and inner hyphens, with "+
			"at most one /sub-segment, got %q", provider, truncate(path, 40))
	}
	for _, known := range knownPaths {
		if path == known {
			return nil
		}
	}
	return fmt.Errorf("%s: unknown family path %q (known: %s)", provider, path, strings.Join(knownPaths, ", "))
}

// readCapped reads at most limit bytes and REFUSES anything longer instead of handing back a prefix
// (oaichat.readCapped, copied — aiprov has no shared one): the limit+1 byte is the whole difference
// between "exactly at the ceiling" and "over it", and a prefix that happens to parse would be accepted
// as a whole answer.
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

// apiErrorMessage pulls the provider's words out of an error body: `detail` as a string (documented:
// {"detail":"Invalid API key"}), as an object {code, message} (Seedance's documented shape), or as a
// list of {msg} (UNVERIFIED (G-06): the FastAPI validation shape a 422 is likely to carry), else
// `error.message` / `message`, else the raw body — always bounded and scrubbed of the key.
func apiErrorMessage(body []byte, key string) string {
	var env struct {
		Detail  json.RawMessage `json:"detail"`
		Message string          `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil {
		if msg := detailText(env.Detail); msg != "" {
			return boundedMessage(msg, key)
		}
		if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
			return boundedMessage(env.Error.Message, key)
		}
		if strings.TrimSpace(env.Message) != "" {
			return boundedMessage(env.Message, key)
		}
	}
	return boundedMessage(string(body), key)
}

// detailText reads `detail`: a string, an object {code, message} ("CODE: message"), or a list whose
// members carry `msg` (joined with "; ").
func detailText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return codeAndMessage(obj.Code, obj.Message)
	}
	var list []struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	msgs := make([]string, 0, len(list))
	for _, d := range list {
		if m := strings.TrimSpace(d.Msg); m != "" {
			msgs = append(msgs, m)
		}
	}
	return strings.Join(msgs, "; ")
}

// errorText reads a generation's `error`: null → "", a string, or an object's message/detail, else the
// raw JSON — bounded and scrubbed of the key, since it lands in a log the moment a caller prints it.
func errorText(raw json.RawMessage, key string) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return boundedMessage(str, key)
	}
	var obj struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		// message first (the sentence the provider wrote), the code when that is all there is
		// (Seedance's {code, message} with a blank message).
		if m := firstNonBlank(obj.Message, obj.Code); m != "" {
			return boundedMessage(m, key)
		}
		if m := detailText(obj.Detail); m != "" {
			return boundedMessage(m, key)
		}
	}
	return boundedMessage(s, key)
}

// codeAndMessage joins an error object's code and message ("GENERATION_FAILED: …"); either alone
// stands alone; "" when both are blank.
func codeAndMessage(code, msg string) string {
	code, msg = strings.TrimSpace(code), strings.TrimSpace(msg)
	switch {
	case code != "" && msg != "":
		return code + ": " + msg
	case code != "":
		return code
	}
	return msg
}

// boundedMessage is the provider's text as a sentence may carry it: the key scrubbed FIRST (a cut made
// before the scrub could leave a prefix of the key standing), then at most errorMessageLimit runes. A
// gateway that echoes the request's headers in its error page must not put the key into a log line.
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
